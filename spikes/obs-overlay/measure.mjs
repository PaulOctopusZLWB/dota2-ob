import { chromium } from "playwright";
import { execFileSync, spawn, spawnSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const spikeDir = dirname(fileURLToPath(import.meta.url));
const outputPath = join(spikeDir, "evidence", "browser-measurement.json");
const temporaryDir = await mkdtemp(join(tmpdir(), "dota2-ob-overlay-measure-"));
const serverBinary = join(temporaryDir, "overlay-spike");
const chromeData = join(temporaryDir, "chrome-data");
const serverAddress = "127.0.0.1:18838";
const chromeDebugAddress = "127.0.0.1:18839";
let serverProcess;
let chromeProcess;
let browser;

function wait(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

async function waitForURL(url, timeoutMs = 15_000) {
  const deadline = Date.now() + timeoutMs;
  let lastError;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url, { cache: "no-store" });
      if (response.ok) return;
    } catch (error) {
      lastError = error;
    }
    await wait(100);
  }
  throw new Error(`timed out waiting for ${url}: ${lastError || "not ready"}`);
}

function processTreeResources(rootPID) {
  const rows = execFileSync("ps", ["-eo", "pid=,ppid=,rss=,comm="], { encoding: "utf8" })
    .trim()
    .split("\n")
    .map((line) => line.trim().split(/\s+/, 4))
    .map(([pid, parentPID, rssKiB, command]) => ({
      pid: Number(pid),
      parentPID: Number(parentPID),
      rssKiB: Number(rssKiB),
      command
    }));
  const included = new Set([rootPID]);
  let changed = true;
  while (changed) {
    changed = false;
    for (const row of rows) {
      if (included.has(row.parentPID) && !included.has(row.pid)) {
        included.add(row.pid);
        changed = true;
      }
    }
  }
  const processes = rows.filter((row) => included.has(row.pid));
  const byCommand = new Map();
  for (const process of processes) {
    const current = byCommand.get(process.command) || { count: 0, rssKiB: 0 };
    current.count += 1;
    current.rssKiB += process.rssKiB;
    byCommand.set(process.command, current);
  }
  return {
    processCount: processes.length,
    totalRssKiB: processes.reduce((total, process) => total + process.rssKiB, 0),
    totalPssKiB: processes.reduce((total, process) => {
      try {
        const match = readFileSync(`/proc/${process.pid}/smaps_rollup`, "utf8").match(/^Pss:\s+(\d+) kB$/m);
        return total + (match ? Number(match[1]) : 0);
      } catch {
        return total;
      }
    }, 0),
    byCommand: Object.fromEntries([...byCommand.entries()].sort(([left], [right]) => left.localeCompare(right)))
  };
}

function metricMap(metrics) {
  return Object.fromEntries(metrics.map(({ name, value }) => [name, value]));
}

async function stop(child) {
  if (!child || child.exitCode !== null) return;
  child.kill("SIGTERM");
  await Promise.race([
    new Promise((resolve) => child.once("exit", resolve)),
    wait(2_000)
  ]);
  if (child.exitCode === null) child.kill("SIGKILL");
}

try {
  const build = spawnSync("go", ["build", "-o", serverBinary, "."], { cwd: spikeDir, encoding: "utf8" });
  if (build.status !== 0) throw new Error(`build overlay spike: ${build.stderr}`);

  serverProcess = spawn(serverBinary, ["-addr", serverAddress], { stdio: "ignore" });
  await waitForURL(`http://${serverAddress}/healthz`);

  chromeProcess = spawn("/usr/bin/google-chrome", [
    "--headless=new",
    "--no-first-run",
    "--no-default-browser-check",
    `--remote-debugging-address=${chromeDebugAddress.split(":")[0]}`,
    `--remote-debugging-port=${chromeDebugAddress.split(":")[1]}`,
    `--user-data-dir=${chromeData}`,
    "about:blank"
  ], { stdio: "ignore" });
  await waitForURL(`http://${chromeDebugAddress}/json/version`);

  browser = await chromium.connectOverCDP(`http://${chromeDebugAddress}`);
  const context = browser.contexts()[0];
  const page = context.pages()[0] || await context.newPage();
  await page.setViewportSize({ width: 1920, height: 1080 });
  let fixtureResponses = 0;
  page.on("response", (response) => {
    if (response.url().includes("/fixtures/")) fixtureResponses += 1;
  });
  await page.goto(`http://${serverAddress}/?fixture=healthy`);
  await page.waitForFunction(() => document.body.dataset.renderState === "visible");

  const session = await context.newCDPSession(page);
  await session.send("Performance.enable");
  await session.send("HeapProfiler.enable");
  await wait(5_000);
  fixtureResponses = 0;
  await session.send("HeapProfiler.collectGarbage");
  const beforeMetrics = metricMap((await session.send("Performance.getMetrics")).metrics);
  const resourcesBefore = processTreeResources(chromeProcess.pid);
  await wait(15_000);
  await session.send("HeapProfiler.collectGarbage");
  const afterMetrics = metricMap((await session.send("Performance.getMetrics")).metrics);
  const resourcesAfter = processTreeResources(chromeProcess.pid);
  const rendering = await page.evaluate(() => ({
    htmlBackground: getComputedStyle(document.documentElement).backgroundColor,
    bodyBackground: getComputedStyle(document.body).backgroundColor,
    fontFamily: getComputedStyle(document.body).fontFamily,
    chineseFontReady: document.fonts.check('16px "Noto Sans CJK SC"'),
    card: (() => {
      const rect = document.getElementById("overlay-card").getBoundingClientRect();
      return { x: rect.x, y: rect.y, width: rect.width, height: rect.height };
    })()
  }));

  let connected = true;
  await page.route("**/fixtures/healthy.json", (route) => connected ? route.continue() : route.abort("connectionfailed"));
  connected = false;
  const disconnectedAt = performance.now();
  await page.waitForFunction(() => document.body.dataset.renderState === "hidden", null, { timeout: 2_000 });
  const hiddenAfterMs = performance.now() - disconnectedAt;
  connected = true;
  const reconnectAt = performance.now();
  await page.waitForFunction(() => document.body.dataset.renderState === "visible", null, { timeout: 2_000 });
  const visibleAfterMs = performance.now() - reconnectAt;

  const evidence = {
    measuredAt: new Date().toISOString(),
    browser: {
      product: "Google Chrome",
      version: browser.version(),
      headless: true,
      canvas: { width: 1920, height: 1080 },
      warmupDurationMs: 5_000,
      fixtureResponsesDuringSample: fixtureResponses,
      sampleDurationMs: 15_000,
      processTreeBefore: resourcesBefore,
      processTreeAfter: resourcesAfter,
      processTreeRssDeltaKiB: resourcesAfter.totalRssKiB - resourcesBefore.totalRssKiB,
      processTreePssDeltaKiB: resourcesAfter.totalPssKiB - resourcesBefore.totalPssKiB,
      jsHeapUsedBeforeBytes: beforeMetrics.JSHeapUsedSize,
      jsHeapUsedAfterBytes: afterMetrics.JSHeapUsedSize,
      jsHeapUsedDeltaBytes: afterMetrics.JSHeapUsedSize - beforeMetrics.JSHeapUsedSize,
      documentCountBefore: beforeMetrics.Documents,
      documentCountAfter: afterMetrics.Documents,
      documentCountDelta: afterMetrics.Documents - beforeMetrics.Documents,
      nodeCountBefore: beforeMetrics.Nodes,
      nodeCountAfter: afterMetrics.Nodes,
      nodeCountDelta: afterMetrics.Nodes - beforeMetrics.Nodes,
      taskDurationDeltaSeconds: afterMetrics.TaskDuration - beforeMetrics.TaskDuration
    },
    reconnect: {
      configuredMaximumSilenceMs: 1250,
      analyticalClaimsHiddenAfterMs: Math.round(hiddenAfterMs),
      analyticalClaimsVisibleAfterReconnectMs: Math.round(visibleAfterMs)
    },
    rendering
  };
  await writeFile(outputPath, `${JSON.stringify(evidence, null, 2)}\n`, "utf8");
  process.stdout.write(`${JSON.stringify(evidence, null, 2)}\n`);
} finally {
  if (browser) await browser.close().catch(() => {});
  await stop(chromeProcess);
  await stop(serverProcess);
  await rm(temporaryDir, { recursive: true, force: true });
}

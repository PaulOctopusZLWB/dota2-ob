import { spawn, spawnSync } from "node:child_process";
import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, "../..");
const output = path.resolve(repo, "docs/evidence/m3/p2-overlay-timing.json");
const trials = Number(process.env.DOTA2_OB_P2_TRIALS || 100);
const address = "127.0.0.1:18838";
const runtime = fs.mkdtempSync(path.join(os.tmpdir(), "dota2-ob-p2-"));
const binary = path.join(runtime, "server");
const sessions = path.join(runtime, "sessions");
let server;
let browser;

const canvases = [
  { name: "1080p", width: 1920, height: 1080 },
  { name: "1440p", width: 2560, height: 1440 }
];
const scenarios = ["stale-deadline", "disconnect", "malformed", "schema-mismatch", "oversize", "missing-asset", "emergency-hide", "delayed-older-response"];

function wait(milliseconds) { return new Promise((resolve) => setTimeout(resolve, milliseconds)); }

async function ready(url, timeout = 20_000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    try { if ((await fetch(url)).ok) return; } catch {}
    await wait(50);
  }
  throw new Error(`server not ready: ${url}`);
}

async function stop(child) {
  if (!child || child.exitCode !== null) return;
  child.kill("SIGTERM");
  await Promise.race([new Promise((resolve) => child.once("exit", resolve)), wait(2_000)]);
  if (child.exitCode === null) child.kill("SIGKILL");
}

function visibleState() {
  const now = Date.now();
  return {
    schema_version: "overlay_state.v1",
    session_id: "p2-session",
    publication_time_ms: now,
    stale_deadline_ms: now + 5_000,
    visibility: "visible",
    decision_id: "p2-decision",
    evidence: [{
      record_schema_version: 1,
      session_id: "p2-session",
      sequence: 1,
      receive_time: new Date(now - 50).toISOString(),
      source: "gsi",
      provider_version: { state: "absent" },
      raw_payload_sha256: "a".repeat(64)
    }],
    confidence: "已观测",
    source_receive_time: new Date(now - 50).toISOString(),
    claim: { title: "天辉建立经济领先", body: "当前已观测经济领先 5000；领先不等同于胜势。", asset_key: "economy" }
  };
}

function unsafeBody(scenario) {
  const state = visibleState();
  if (scenario === "stale-deadline") state.stale_deadline_ms = Date.now() - 1;
  if (scenario === "malformed") return "{not-json";
  if (scenario === "schema-mismatch") state.schema_version = "overlay_state.v2";
  if (scenario === "oversize") return JSON.stringify({ ...state, padding: "x".repeat(65 * 1024) });
  if (scenario === "missing-asset") state.claim.asset_key = "missing-local-asset";
  if (scenario === "emergency-hide") return JSON.stringify({
    ...state, visibility: "hidden", health_code: "emergency_hide", decision_id: "",
    evidence: [], confidence: "", source_receive_time: null, claim: null
  });
  return JSON.stringify(state);
}

function percentile(values, fraction) {
  const ordered = [...values].sort((a, b) => a - b);
  return ordered[Math.max(0, Math.ceil(ordered.length * fraction) - 1)];
}

async function measureScenario(context, canvas, scenario, emptyFrameSHA) {
  const page = await context.newPage();
  await page.setViewportSize(canvas);
  let mode = "healthy";
  let delayedRelease;
  let unsafeCompleted;
  let unsafeCompletedResolve;
  await page.route("**/v1/overlay/state", async (route) => {
    if (mode === "healthy") {
      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(visibleState()) });
      return;
    }
    if (mode === "disconnect") {
      unsafeCompletedResolve?.(performance.now());
      await route.abort("connectionfailed");
      return;
    }
    if (mode === "delayed-healthy") {
      await new Promise((resolve) => { delayedRelease = resolve; });
      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(visibleState()) });
      return;
    }
    const body = unsafeBody(mode);
    await route.fulfill({ status: 200, contentType: "application/json", body });
    unsafeCompletedResolve?.(performance.now());
  });

  await page.goto(`http://${address}/overlay/`);
  await page.waitForFunction(() => document.body.dataset.renderState === "visible");
  const durations = [];
  let reappearances = 0;
  let nonEmptyFrames = 0;

  for (let trial = 0; trial < trials; trial += 1) {
    mode = "healthy";
    delayedRelease = undefined;
    await page.waitForFunction(() => document.body.dataset.renderState === "visible");
    unsafeCompleted = new Promise((resolve) => { unsafeCompletedResolve = resolve; });
    if (scenario === "delayed-older-response") {
      mode = "delayed-healthy";
      const delayedDeadline = Date.now() + 1_000;
      while (!delayedRelease && Date.now() < delayedDeadline) await wait(5);
      if (!delayedRelease) throw new Error("delayed healthy response did not start");
      mode = "schema-mismatch";
    } else {
      mode = scenario;
    }
    const completedAt = await unsafeCompleted;
    await page.waitForFunction(() => document.body.dataset.renderState === "hidden", null, { timeout: 2_000 });
    const hiddenAt = performance.now();
    durations.push(Math.round((hiddenAt - completedAt) * 1000) / 1000);
    if (scenario === "delayed-older-response") {
      let visibleAfterUnsafe = false;
      await page.evaluate(() => {
        window.__p2VisibleAfterUnsafe = false;
        window.__p2Observer?.disconnect();
        window.__p2Observer = new MutationObserver(() => {
          if (document.body.dataset.renderState === "visible") window.__p2VisibleAfterUnsafe = true;
        });
        window.__p2Observer.observe(document.body, { attributes: true, attributeFilter: ["data-render-state"] });
      });
      delayedRelease();
      await wait(300);
      visibleAfterUnsafe = await page.evaluate(() => window.__p2VisibleAfterUnsafe);
      if (visibleAfterUnsafe) reappearances += 1;
    }
    const frame = await page.screenshot({ omitBackground: true, animations: "disabled" });
    if (crypto.createHash("sha256").update(frame).digest("hex") !== emptyFrameSHA) nonEmptyFrames += 1;
    mode = "healthy";
    unsafeCompletedResolve = undefined;
    await page.waitForFunction(() => document.body.dataset.renderState === "visible", null, { timeout: 2_000 });
  }
  await page.close();
  return {
    canvas: canvas.name,
    scenario,
    trials,
    p50Ms: percentile(durations, 0.50),
    p95Ms: percentile(durations, 0.95),
    maxMs: Math.max(...durations),
    transientReappearances: reappearances,
    nonEmptyCapturedFrames: nonEmptyFrames
  };
}

try {
  const build = spawnSync("go", ["build", "-o", binary, "./cmd/dota2-ob"], { cwd: repo, encoding: "utf8" });
  if (build.status !== 0) throw new Error(build.stderr || "server build failed");
  server = spawn(binary, ["--addr", "127.0.0.1:18839", "--delivery-addr", address, "--data-dir", sessions], {
    env: { ...process.env, XDG_RUNTIME_DIR: runtime }, stdio: "ignore"
  });
  await ready(`http://${address}/overlay/`);
  browser = await chromium.launch({ channel: "chrome", headless: true });
  const context = await browser.newContext({ locale: "zh-CN" });
  const results = [];
  for (const canvas of canvases) {
    const emptyPage = await context.newPage();
    await emptyPage.setViewportSize(canvas);
    await emptyPage.setContent("<!doctype html><style>html,body{margin:0;background:transparent}</style>");
    const emptyFrame = await emptyPage.screenshot({ omitBackground: true, animations: "disabled" });
    const emptySHA = crypto.createHash("sha256").update(emptyFrame).digest("hex");
    await emptyPage.close();
    const measured = await Promise.all(scenarios.map((scenario) => measureScenario(context, canvas, scenario, emptySHA)));
    results.push(...measured);
  }
  const evidence = {
    protocol: "P2",
    measuredAt: new Date().toISOString(),
    baseCommit: spawnSync("git", ["rev-parse", "HEAD"], { cwd: repo, encoding: "utf8" }).stdout.trim(),
    sourceDiffSHA256: crypto.createHash("sha256").update(spawnSync("git", ["diff", "--binary", "HEAD"], { cwd: repo }).stdout).digest("hex"),
    browser: { product: "Google Chrome", version: browser.version(), headless: true },
    pollIntervalMs: 750,
    trialsPerScenarioPerCanvas: trials,
    timingStart: "completed unsafe response/transport error",
    timingStop: "DOM claim hidden followed by alpha-empty captured frame",
    results,
    passed: results.every((result) => result.maxMs < 2_000 && result.transientReappearances === 0 && result.nonEmptyCapturedFrames === 0)
  };
  fs.mkdirSync(path.dirname(output), { recursive: true, mode: 0o700 });
  fs.writeFileSync(output, `${JSON.stringify(evidence, null, 2)}\n`, { mode: 0o600 });
  process.stdout.write(`${JSON.stringify(evidence, null, 2)}\n`);
  if (!evidence.passed) process.exitCode = 1;
} finally {
  if (browser) await browser.close().catch(() => {});
  await stop(server);
  fs.rmSync(runtime, { recursive: true, force: true });
}

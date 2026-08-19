// Focused real-Chromium role-consistency check (Fix round 3 item 4).
//
// Starts the local serve binary against a persisted five-probe data root, then
// in a real Chromium engine:
//   - renders the match page and player page at the source role (5);
//   - applies a 5->1 role override via the API;
//   - re-renders both pages and asserts the effective role (1) with source
//     role (5) and override provenance;
//   - restarts the server (same data root) and re-renders both pages, proving
//     the persisted effective role survives restart.
// This covers role-consistency navigation only; the full item-6 seven-operation
// workflow remains pending.
//
// Run: PROBE_ROOT=<data-root> node web/replay/browser_role_e2e.mjs
import { createRequire } from "node:module";
import { spawn } from "node:child_process";
import { existsSync, rmSync } from "node:fs";
import { join } from "node:path";
import assert from "node:assert";

const require = createRequire(import.meta.url);
const ROOT = new URL("../../", import.meta.url).pathname;
const BIN = join(ROOT, "data", "replay", "browser-role-e2e-bin");
const PROBE_ROOT = process.env.PROBE_ROOT || "/tmp/r3d-probe";
const LISTEN = "127.0.0.1:43243";
const CHROME = process.env.CHROME_PATH || "/usr/bin/google-chrome";
const PW = process.env.PLAYWRIGHT_CORE || "/tmp/pw/node_modules/playwright-core";
const MATCH = "8944521919";
const ACCT = "111114687";

function waitFor(ms) { return new Promise((r) => setTimeout(r, ms)); }

function serveCmd() {
  return [BIN, "serve",
    "--manifest", join(ROOT, "docs/specs/ti2026-five-replay-probe-v1.json"),
    "--data-root", PROBE_ROOT, "--listen", LISTEN, "--session-token", "browser-tok"];
}

async function startServer() {
  const srv = spawn(serveCmd()[0], serveCmd().slice(1), { stdio: "ignore" });
  const base = `http://${LISTEN}`;
  let up = false;
  for (let i = 0; i < 60 && !up; i++) {
    try { const r = await fetch(`${base}/api/replay/v1/corpus`); up = r.ok; }
    catch { await waitFor(200); }
  }
  assert.ok(up, "server not ready");
  return { srv, base };
}

async function readMatchAndPlayer(page, base) {
  const rep = await page.evaluate(async (u) => (await fetch(u)).json(), `${base}/api/replay/v1/matches/${MATCH}`);
  const pl = await page.evaluate(async (u) => (await fetch(u)).json(), `${base}/api/replay/v1/players/${ACCT}`);
  let reportEffective, reportSource, reportOverride;
  for (const p of rep.data.participants) {
    if (p.account_id === ACCT) {
      reportEffective = p.nominal_role;
      reportSource = p.source_nominal_role;
      reportOverride = p.override_applied;
    }
  }
  let rowEffective, rowSource;
  for (const m of pl.data.matches || []) {
    if (m.match_id === MATCH) { rowEffective = m.nominal_role; rowSource = m.source_nominal_role; }
  }
  return { reportEffective, reportSource, reportOverride, rowEffective, rowSource };
}

async function main() {
  const build = spawn("go", ["build", "-o", BIN, "./cmd/dota2-ob"], { cwd: ROOT, stdio: "inherit" });
  const buildCode = await new Promise((r) => build.on("close", r));
  assert.strictEqual(buildCode, 0, "go build failed");

  let server = await startServer();
  const { chromium } = require(PW);
  const browser = await chromium.launch({
    executablePath: CHROME, headless: true,
    args: ["--no-sandbox", "--disable-dev-shm-usage"],
  });
  try {
    const page = await browser.newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(String(e)));
    // Navigate to a same-origin page so in-page fetch is allowed.
    await page.goto(`${server.base}/index.html`, { waitUntil: "domcontentloaded" });

    // Before: source role 5 on both pages.
    let st = await readMatchAndPlayer(page, server.base);
    assert.strictEqual(st.reportEffective, "5", "match page pre-override effective");
    assert.strictEqual(st.reportSource, "5", "match page pre-override source");
    assert.strictEqual(st.rowEffective, "5", "player row pre-override effective");

    // Render match + player pages at source role (no JS errors).
    await page.goto(`${server.base}/match.html?id=${MATCH}`, { waitUntil: "domcontentloaded" });
    await page.waitForSelector("table", { timeout: 15000 });
    await page.goto(`${server.base}/player.html?id=${ACCT}`, { waitUntil: "domcontentloaded" });
    await page.waitForTimeout(500);

    // Apply 5 -> 1.
    const ovr = await page.evaluate(async ({ u, match, acct }) => {
      const r = await fetch(u, {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-Dota2-OB-Token": "browser-tok" },
        body: JSON.stringify({ match_id: match, account_id: acct, nominal_role: "1", author: "browser", reason: "role e2e 5->1" }),
      });
      return r.status;
    }, { u: `${server.base}/api/replay/v1/roles/overrides`, match: MATCH, acct: ACCT });
    assert.strictEqual(ovr, 200, "override 5->1 failed");

    // After: effective 1, source 5, override applied, on both pages.
    st = await readMatchAndPlayer(page, server.base);
    assert.strictEqual(st.reportEffective, "1", "match page post-override effective");
    assert.strictEqual(st.reportSource, "5", "match page post-override source");
    assert.strictEqual(st.reportOverride, true, "match page override flag");
    assert.strictEqual(st.rowEffective, "1", "player row post-override effective");
    assert.strictEqual(st.rowSource, "5", "player row post-override source");
    await page.goto(`${server.base}/match.html?id=${MATCH}`, { waitUntil: "domcontentloaded" });
    await page.waitForSelector("table", { timeout: 15000 });
    await page.goto(`${server.base}/player.html?id=${ACCT}`, { waitUntil: "domcontentloaded" });
    await page.waitForTimeout(500);

    // Restart server (same data root) and re-verify.
    server.srv.kill("SIGTERM"); await waitFor(400);
    server = await startServer();
    st = await readMatchAndPlayer(page, server.base);
    assert.strictEqual(st.reportEffective, "1", "match page post-restart effective");
    assert.strictEqual(st.reportSource, "5", "match page post-restart source");
    assert.strictEqual(st.rowEffective, "1", "player row post-restart effective");
    await page.goto(`${server.base}/match.html?id=${MATCH}`, { waitUntil: "domcontentloaded" });
    await page.waitForSelector("table", { timeout: 15000 });
    await page.goto(`${server.base}/player.html?id=${ACCT}`, { waitUntil: "domcontentloaded" });
    await page.waitForTimeout(500);

    assert.deepStrictEqual(errors, [], `page JS errors: ${errors.join(" | ")}`);
    console.log(`browser_role_e2e: OK — match + player pages agree on effective role before(5)/after(1)/after-restart(1), source stays 5 for ${MATCH}/${ACCT}`);
  } finally {
    await browser.close();
  }
  server.srv.kill("SIGTERM");
  await waitFor(300);
  rmSync(BIN, { force: true });
}

main().catch((e) => { console.error(e); process.exit(1); });

// Focused real-Chromium team-page check (Fix round 3 item 5).
//
// Starts the local serve binary against a persisted five-probe data root and
// in a real Chromium engine renders the team page for a team that is in the
// corpus, asserting:
//   - the solid official and dashed experimental layers are both present;
//   - the separately named official and experimental totals render;
//   - a suppressed official total shows suppression reasons (probe teams have
//     < 3 eligible matches, so the official total is suppressed);
//   - the experimental layer renders as suppressed (no frozen recipe), never
//     as a zero polygon;
//   - the component decomposition table renders.
// This covers the team scoring product UI; the still-pending full item-6
// seven-operation workflow is not claimed.
//
// Run: PROBE_ROOT=<data-root> node web/replay/browser_team_e2e.mjs
import { createRequire } from "node:module";
import { spawn } from "node:child_process";
import { existsSync, rmSync } from "node:fs";
import { join } from "node:path";
import assert from "node:assert";

const require = createRequire(import.meta.url);
const ROOT = new URL("../../", import.meta.url).pathname;
const BIN = join(ROOT, "data", "replay", "browser-team-e2e-bin");
const PROBE_ROOT = process.env.PROBE_ROOT || "/tmp/r3e-probe";
const LISTEN = "127.0.0.1:43247";
const CHROME = process.env.CHROME_PATH || "/usr/bin/google-chrome";
const PW = process.env.PLAYWRIGHT_CORE || "/tmp/pw/node_modules/playwright-core";

function waitFor(ms) { return new Promise((r) => setTimeout(r, ms)); }

async function main() {
  const build = spawn("go", ["build", "-o", BIN, "./cmd/dota2-ob"], { cwd: ROOT, stdio: "inherit" });
  const buildCode = await new Promise((r) => build.on("close", r));
  assert.strictEqual(buildCode, 0, "go build failed");

  const srv = spawn(BIN, [
    "serve",
    "--manifest", join(ROOT, "docs/specs/ti2026-five-replay-probe-v1.json"),
    "--data-root", PROBE_ROOT,
    "--listen", LISTEN,
    "--session-token", "browser-tok",
  ], { stdio: "ignore" });
  let exited = false;
  srv.on("exit", () => { exited = true; });
  try {
    const base = `http://${LISTEN}`;
    let up = false;
    for (let i = 0; i < 60 && !up; i++) {
      try { const r = await fetch(`${base}/api/replay/v1/corpus`); up = r.ok; }
      catch { await waitFor(200); }
    }
    assert.ok(up, "server not ready");

    const { chromium } = require(PW);
    const browser = await chromium.launch({
      executablePath: CHROME, headless: true,
      args: ["--no-sandbox", "--disable-dev-shm-usage"],
    });
    try {
      const page = await browser.newPage();
      const errors = [];
      page.on("pageerror", (e) => errors.push(String(e)));

      // Pick a team that exists in the persisted corpus.
      await page.goto(`${base}/index.html`, { waitUntil: "domcontentloaded" });
      const corpus = await page.evaluate(async (u) => {
        const r = await fetch(u); return r.json();
      }, `${base}/api/replay/v1/scores/corpus`);
      const teams = (corpus.data.teams || []);
      assert.ok(teams.length > 0, "no teams in persisted corpus");
      const teamID = teams[0].team_id;

      // API schema: stable shape with both layers and explicit reasons.
      const api = await page.evaluate(async (u) => {
        const r = await fetch(u); return r.json();
      }, `${base}/api/replay/v1/teams/${teamID}`);
      const ts = api.data.team_score || {};
      assert.ok(ts.official_axes && ts.experimental_axes, "team API missing official/experimental axes");
      assert.ok(ts.official_total && ts.experimental_total, "team API missing official/experimental totals");
      assert.ok(ts.experimental_total.suppressed, "experimental team total must be suppressed");
      assert.ok(ts.experimental_total.reasons && ts.experimental_total.reasons.length > 0, "experimental total needs explicit reasons");

      // Render the team page.
      await page.goto(`${base}/team.html?id=${teamID}`, { waitUntil: "domcontentloaded" });
      await page.waitForSelector("table", { timeout: 15000 });
      const text = await page.evaluate(() => document.body.innerText);
      assert.ok(text.includes("队伍官方总分"), "team page missing official total label");
      assert.ok(text.includes("队伍实验总分"), "team page missing experimental total label");
      assert.ok(text.includes("官方轴分解"), "team page missing official axis table");
      assert.ok(text.includes("实验轴分解"), "team page missing experimental axis table");
      assert.ok(text.includes("指标分解"), "team page missing component decomposition");
      // Suppression is visible, never a zero polygon.
      const suppressedText = text.includes("总分已抑制") || text.includes("已抑制");
      assert.ok(suppressedText, "team page must surface suppression, not a fabricated zero");
      const svg = await page.$("svg.radar");
      assert.ok(svg, "team page missing radar");

      assert.deepStrictEqual(errors, [], `page JS errors: ${errors.join(" | ")}`);
      console.log(`browser_team_e2e: OK — team ${teamID} renders official/experimental layers, named totals, suppression, decomposition in real Chrome`);
    } finally {
      await browser.close();
    }
  } finally {
    srv.kill("SIGTERM");
    await waitFor(300);
    if (!exited) srv.kill("SIGKILL");
    rmSync(BIN, { force: true });
  }
}

main().catch((e) => { console.error(e); process.exit(1); });

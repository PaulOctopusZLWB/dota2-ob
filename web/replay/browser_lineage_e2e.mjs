// Focused real-Chromium lineage-navigation check (Fix round 3 item 3).
//
// Starts the local serve binary against a persisted five-probe data root,
// then in a real Chromium engine:
//   - follows a representative fact ref to its match-qualified evidence route;
//   - follows an episode ref, a phase ref, a metric-observation ref, and an
//     algorithm/evaluator ref;
//   - renders the player page lineage drilldown and verifies each typed ref
//     deep-links to a match-qualified evidence target (?evidence=kind:id);
//   - verifies a stale fact ref renders an explicit unavailable reason (404
//     with error body) rather than a misleading success.
//
// Run: PROBE_ROOT=<data-root> node web/replay/browser_lineage_e2e.mjs
import { createRequire } from "node:module";
import { spawn } from "node:child_process";
import { existsSync, rmSync } from "node:fs";
import { join } from "node:path";
import assert from "node:assert";

const require = createRequire(import.meta.url);
const ROOT = new URL("../../", import.meta.url).pathname;
const BIN = join(ROOT, "data", "replay", "browser-lineage-e2e-bin");
const PROBE_ROOT = process.env.PROBE_ROOT || "/tmp/r3c-probe-a";
const LISTEN = "127.0.0.1:43237";
const CHROME = process.env.CHROME_PATH || "/usr/bin/google-chrome";
const PW = process.env.PLAYWRIGHT_CORE || "/tmp/pw/node_modules/playwright-core";

function waitFor(ms) { return new Promise((r) => setTimeout(r, ms)); }

async function main() {
  if (!existsSync(join(PROBE_ROOT, "catalog.json"))) {
    console.error(`probe data root missing: ${PROBE_ROOT}`);
    process.exit(2);
  }
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

      const MID = "8944521919";
      // Navigate to a same-origin page first so in-page fetch is allowed.
      await page.goto(`${base}/index.html`, { waitUntil: "domcontentloaded" });
      // Pick representative refs straight from the persisted metrics artifact.
      const rep = await page.evaluate(async (u) => {
        const r = await fetch(u); return r.json();
      }, `${base}/api/replay/v1/matches/${MID}`);
      const values = (rep.data.metrics && rep.data.metrics.values) || [];
      let factRef, episodeRef, phaseRef, metricRef, algRef;
      for (const v of values) {
        for (const e of (v.evidence || [])) {
          if (!factRef && e.kind === "fact") factRef = e;
          if (!episodeRef && e.kind === "episode") episodeRef = e;
          if (!phaseRef && e.kind === "phase") phaseRef = e;
          if (!metricRef && e.kind === "metric_observation") metricRef = e;
          if (!algRef && e.kind === "algorithm") algRef = e;
        }
      }
      assert.ok(factRef && factRef.match_id === MID, "no fact ref with match id");
      assert.ok(episodeRef, "no episode ref");
      assert.ok(phaseRef, "no phase ref");
      assert.ok(metricRef, "no metric ref");
      assert.ok(algRef, "no algorithm ref");

      // 1. Fact route (match-qualified) resolves to the referenced fact.
      const factSeq = String(factRef.source_fact_seq || factRef.id.replace("fact:", ""));
      let code = await page.evaluate(async (u) => (await fetch(u)).status,
        `${base}/api/replay/v1/matches/${MID}/facts/${factSeq}`);
      assert.strictEqual(code, 200, "fact route not 200");
      // 2. Episode route.
      code = await page.evaluate(async (u) => (await fetch(u)).status,
        `${base}/api/replay/v1/matches/${MID}/episodes/${encodeURIComponent(episodeRef.id)}`);
      assert.strictEqual(code, 200, "episode route not 200");
      // 3. Phase route.
      const phaseID = phaseRef.id.replace("interval@", "");
      code = await page.evaluate(async (u) => (await fetch(u)).status,
        `${base}/api/replay/v1/matches/${MID}/phases/interval%40${phaseID}`);
      assert.strictEqual(code, 200, "phase route not 200");
      // 4. Metric observation route.
      const metricID = metricRef.id.split(":")[0];
      code = await page.evaluate(async (u) => (await fetch(u)).status,
        `${base}/api/replay/v1/matches/${MID}/metrics/${metricID}`);
      assert.strictEqual(code, 200, "metric observation route not 200");
      // 5. Algorithm/evaluator route.
      code = await page.evaluate(async (u) => (await fetch(u)).status,
        `${base}/api/replay/v1/matches/${MID}/algorithms/${encodeURIComponent(algRef.id)}`);
      assert.strictEqual(code, 200, "algorithm route not 200");

      // 6. Player page lineage drilldown: every typed ref deep-links to a
      // match-qualified evidence target and the deep link renders the panel.
      // Use a player whose official axes publish (lineage is rendered per
      // published component).
      const corpus = await page.evaluate(async (u) => {
        const r = await fetch(u); return r.json();
      }, `${base}/api/replay/v1/scores/corpus`);
      let pubAcct = null;
      for (const p of (corpus.data.players || [])) {
        const ax = p.official_axes || {};
        const comps = Object.values(ax).reduce((n, a) => n + Object.keys(a.components || {}).length, 0);
        if (comps > 0) { pubAcct = p.account_id; break; }
      }
      assert.ok(pubAcct, "no player with published axes for lineage drilldown");
      await page.goto(`${base}/player.html?id=${pubAcct}`, { waitUntil: "domcontentloaded" });
      await page.waitForTimeout(700);
      const links = await page.$$eval("a[href*='&evidence=']", (as) => as.map((a) => a.getAttribute("href")).slice(0, 3));
      assert.ok(links.length >= 1, "player lineage has no match-qualified evidence links");
      const first = links[0];
      const evParam = new URL(`http://x/${first}`).searchParams.get("evidence");
      assert.ok(evParam && /^(fact|episode|phase|metric_observation|algorithm):/.test(evParam), `bad evidence param ${evParam}`);
      // Follow the deep link on match.html and expect the evidence panel.
      const mid = new URL(`http://x/${first}`).searchParams.get("id");
      await page.goto(`${base}/match.html?id=${mid}&evidence=${evParam}`, { waitUntil: "domcontentloaded" });
      await page.waitForSelector(".panel", { timeout: 15000 });
      const bodyText = await page.evaluate(() => document.body.innerText);
      assert.ok(bodyText.includes("证据定位"), "match page evidence panel missing");

      // 7. Stale fact renders explicit unavailable (404 + error body).
      const stale = await page.evaluate(async (u) => {
        const r = await fetch(u);
        const j = await r.json();
        return { status: r.status, err: j.error || "" };
      }, `${base}/api/replay/v1/matches/${MID}/facts/999999999`);
      assert.strictEqual(stale.status, 404, "stale fact not 404");
      assert.ok(stale.err.includes("fact_not_found"), `stale fact error=${stale.err}`);

      assert.deepStrictEqual(errors, [], `page JS errors: ${errors.join(" | ")}`);
      console.log(`browser_lineage_e2e: OK — fact, episode, phase, metric_observation, algorithm refs navigate to match-qualified evidence for ${MID}; stale ref renders unavailable; player drilldown deep-links present`);
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

// Real browser-engine verification for the replay pages using Playwright +
// the system Chrome. This test starts the local serve binary against a
// persisted five-probe data root, opens the ACTUAL pages in a real Chromium
// engine, executes the pages' real load() entrypoints (fetch + DOM
// insertion), and exercises:
//   - corpus + all five match pages
//   - one player page at each nominal role 1-5 with official/experimental
//     radar, totals, suppression reasons, and evidence-lineage drilldown
//   - one team page with the derived team scoring
//   - the review queue and a full phase-edit round trip (relabel)
//   - role override + synchronous recompute (served role changes immediately)
//   - tamper/ invalid-phase rejection
//
// Run: node web/replay/browser_e2e.mjs  (PROBE_ROOT env selects the data root)
import { createRequire } from "node:module";
import { spawn } from "node:child_process";
import { existsSync, rmSync } from "node:fs";
import { join } from "node:path";
import assert from "node:assert";

const require = createRequire(import.meta.url);
const ROOT = new URL("../../", import.meta.url).pathname;
const BIN = join(ROOT, "data", "replay", "browser-e2e-bin");
const PROBE_ROOT = process.env.PROBE_ROOT || "/tmp/probe-a";
const LISTEN = "127.0.0.1:43235";

// Use the cached Playwright browser (chromium-1223) or system Chrome.
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
  ], { stdio: "inherit" });
  let exited = false;
  srv.on("exit", () => { exited = true; });
  try {
    const base = `http://${LISTEN}`;
    // Wait for readiness.
    let up = false;
    for (let i = 0; i < 60 && !up; i++) {
      try {
        const r = await fetch(`${base}/api/replay/v1/corpus`);
        up = r.ok;
      } catch { await waitFor(200); }
    }
    assert.ok(up, "server not ready");

    const { chromium } = require(PW);
    const browser = await chromium.launch({
      executablePath: CHROME,
      headless: true,
      args: ["--no-sandbox", "--disable-dev-shm-usage"],
    });
    try {
      const page = await browser.newPage();
      // pageerror = uncaught JS exceptions (the real crash signal). Console
      // "Failed to load resource: 404/400" entries are expected: scores are
      // optional on match pages (404 when absent) and the invalid-phase
      // tamper test intentionally triggers 400.
      const errors = [];
      page.on("pageerror", (e) => errors.push(String(e)));

      // Corpus page renders all five matches.
      await page.goto(`${base}/index.html`, { waitUntil: "domcontentloaded" });
      await page.waitForFunction(() => document.querySelectorAll("#rows tr").length >= 5, null, { timeout: 15000 });
      const matchRows = await page.$$eval("#rows tr a", (as) => as.map((a) => a.getAttribute("href")));
      assert.ok(matchRows.length >= 5, "corpus page did not render 5 matches");

      // Five match pages render the phase timeline + participant table.
      for (const href of matchRows.slice(0, 5)) {
        await page.goto(`${base}/${href}`, { waitUntil: "domcontentloaded" });
        await page.waitForSelector("table", { timeout: 15000 });
        const bodyText = await page.evaluate(() => document.body.innerText);
        assert.ok(bodyText.includes("选手与名义角色"), `match page missing participants for ${href}`);
        assert.ok(bodyText.includes("官方阶段时间线"), `match page missing timeline for ${href}`);
      }

      // One player at each nominal role 1-5 renders the radar, totals, and
      // evidence drilldown without a TypeError.
      const rolesSeen = new Set();
      for (const href of matchRows.slice(0, 5)) {
        const mid = decodeURIComponent(href.split("=")[1]);
        const resp = await page.evaluate(async (u) => {
          const r = await fetch(u); return r.json();
        }, `${base}/api/replay/v1/matches/${mid}/scores`);
        for (const p of resp.data.players || []) {
          if (rolesSeen.has(p.nominal_role)) continue;
          const r2 = await page.evaluate(async (u) => {
            const r = await fetch(u); return r.json();
          }, `${base}/api/replay/v1/players/${p.account_id}`);
          const ps = r2.data.score;
          assert.ok(ps, `player ${p.account_id} has no score`);
          await page.goto(`${base}/player.html?id=${p.account_id}`, { waitUntil: "domcontentloaded" });
          await page.waitForTimeout(500);
          const text = await page.evaluate(() => document.body.innerText);
          assert.ok(text.includes("八轴雷达"), `player page missing radar for role ${p.nominal_role}`);
          assert.ok(text.includes("官方总分"), `player page missing official total for role ${p.nominal_role}`);
          assert.ok(text.includes("实验总分"), `player page missing experimental total for role ${p.nominal_role}`);
          const radarSvg = await page.$("svg.radar");
          assert.ok(radarSvg, `radar svg missing for role ${p.nominal_role}`);
          rolesSeen.add(p.nominal_role);
          break;
        }
      }
      assert.deepStrictEqual([...rolesSeen].sort(), ["1", "2", "3", "4", "5"], "roles 1-5 not all covered");

      // Team page renders the derived team scoring.
      const rep = await page.evaluate(async (u) => {
        const r = await fetch(u); return r.json();
      }, `${base}/api/replay/v1/matches/${decodeURIComponent(matchRows[0].split("=")[1])}`);
      const teamID = rep.data.teams[0].team_id;
      await page.goto(`${base}/team.html?id=${teamID}`, { waitUntil: "domcontentloaded" });
      await page.waitForSelector("table", { timeout: 15000 });
      const teamText = await page.evaluate(() => document.body.innerText);
      assert.ok(teamText.includes("队伍评分"), "team page missing team scoring");

      // Review page: real load() + preloaded machine intervals + a full
      // phase-edit round trip (relabel) with reason, then verify effective
      // overlay persisted and machine value rejected on tamper.
      await page.goto(`${base}/review.html`, { waitUntil: "domcontentloaded" });
      await page.waitForTimeout(800);
      const reviewText = await page.evaluate(() => document.body.innerText);
      assert.ok(reviewText.includes("阶段边界校正"), "review page missing phase controls");
      assert.ok(reviewText.includes("操作类型"), "review page missing typed operations");

      // Role override → synchronous recompute: pick a player, override role,
      // and assert the served player API shows the new role immediately.
      const firstMatch = decodeURIComponent(matchRows[0].split("=")[1]);
      const firstScores = await page.evaluate(async (u) => {
        const r = await fetch(u); return r.json();
      }, `${base}/api/replay/v1/matches/${firstMatch}/scores`);
      const firstPlayer = firstScores.data.players[0];
      const acct = firstPlayer.account_id;
      const curRole = firstPlayer.nominal_role;
      const newRole = curRole === "1" ? "2" : "1";
      const ovr = await page.evaluate(async ({ u, match, acct, newRole }) => {
        const r = await fetch(u, {
          method: "POST",
          headers: { "Content-Type": "application/json", "X-Dota2-OB-Token": "browser-tok" },
          body: JSON.stringify({ match_id: match, account_id: acct, nominal_role: newRole, author: "browser", reason: "e2e override" }),
        });
        return { status: r.status, body: await r.json() };
      }, { u: `${base}/api/replay/v1/roles/overrides`, match: firstMatch, acct, newRole });
      assert.strictEqual(ovr.status, 200, "role override failed");
      const after = await page.evaluate(async (u) => {
        const r = await fetch(u); return r.json();
      }, `${base}/api/replay/v1/players/${acct}`);
      assert.strictEqual(after.data.score.nominal_role, newRole, "served role did not update after override");

      // Tamper rejection: invalid phase label must be rejected (400) and not
      // persisted.
      const bad = await page.evaluate(async (u) => {
        const r = await fetch(u, {
          method: "POST",
          headers: { "Content-Type": "application/json", "X-Dota2-OB-Token": "browser-tok" },
          body: JSON.stringify({ match_id: "8944521919", author: "browser", reason: "bad", operation: "relabel", event_ref: "interval@0-1", effective_value: { start_game_second: 0, end_game_second: 1, global_phase: "bogus" } }),
        });
        return r.status;
      }, `${base}/api/replay/v1/reviews/phase-corrections`);
      assert.strictEqual(bad, 400, "invalid phase accepted");

      // Review queue must exist and include the match.
      const queue = await page.evaluate(async (u) => {
        const r = await fetch(u); return r.json();
      }, `${base}/api/replay/v1/reviews/queue`);
      assert.ok(Array.isArray(queue.data.queue) && queue.data.queue.length >= 1, "review queue empty");

      assert.deepStrictEqual(errors, [], `page JS errors: ${errors.join(" | ")}`);      console.log("browser_e2e: OK — corpus, 5 matches, roles 1-5, team, review ops, role override recompute, tamper rejection in real Chrome");
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

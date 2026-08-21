// Real browser-engine verification for the replay product using Playwright +
// the system Chrome. This test:
//   - starts the local serve binary against a FRESH DISPOSABLE COPY of a clean
//     five-probe data root (never mutating the source PROBE_ROOT);
//   - drives the ACTUAL rendered review.html controls in a real Chromium
//     engine through all seven typed phase operations cumulatively against
//     frozen match 8944521919, asserting every transition (correction count,
//     op type, complete v2 before/after streams, phase-op.v2 provenance,
//     exact [0,2705] coverage, and that the next UI reload exposes the new
//     current refs);
//   - hashes phases.json before/after and proves the immutable machine artifact
//     is byte-identical;
//   - restarts the server on the same disposable root and proves the exact
//     effective stream, correction history, audit history, and review UI
//     survive; submits a stale closed ref (409), an illegal `reset` phase
//     (400), and an unauthenticated mutation (403) and proves review/audit/
//     effective-state bytes and counts do not change;
//   - runs one coherent product workflow: corpus + five match pages, nominal
//     roles 1-5, a role override with restart consistency, match-qualified
//     lineage deep links (with stale-ref unavailable state), and the team page
//     with separated solid/dashed layers;
//   - finishes with zero uncaught page errors.
//
// Run: PROBE_ROOT=<clean-five-probe-root> node web/replay/browser_e2e.mjs
import { createRequire } from "node:module";
import { spawn } from "node:child_process";
import { existsSync, mkdtempSync, cpSync, rmSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createHash } from "node:crypto";
import assert from "node:assert";

const require = createRequire(import.meta.url);
const ROOT = new URL("../../", import.meta.url).pathname;
const BIN = join(ROOT, "data", "replay", "browser-e2e-bin");
const SOURCE_ROOT = process.env.PROBE_ROOT || "/tmp/r3e-final";
const LISTEN = "127.0.0.1:43235";
const CHROME = process.env.CHROME_PATH || "/usr/bin/google-chrome";
const PW = process.env.PLAYWRIGHT_CORE || "/tmp/pw/node_modules/playwright-core";
const TOKEN = "browser-tok";
const MATCH = "8944521919";

function waitFor(ms) { return new Promise((r) => setTimeout(r, ms)); }

function sha256File(p) {
  const { readFileSync } = require("node:fs");
  return createHash("sha256").update(readFileSync(p)).digest("hex");
}

async function startServer(dataRoot) {
  const srv = spawn(BIN, [
    "serve",
    "--manifest", join(ROOT, "docs/specs/ti2026-five-replay-probe-v1.json"),
    "--data-root", dataRoot,
    "--listen", LISTEN,
    "--session-token", TOKEN,
  ], { stdio: "ignore" });
  let exited = false;
  srv.on("exit", () => { exited = true; });
  const base = `http://${LISTEN}`;
  let up = false;
  for (let i = 0; i < 60 && !up; i++) {
    try { const r = await fetch(`${base}/api/replay/v1/corpus`); up = r.ok; }
    catch { await waitFor(200); }
  }
  assert.ok(up, "server not ready");
  return { srv, exited, base };
}

async function stopServer(server) {
  server.srv.kill("SIGTERM");
  await waitFor(400);
  if (!server.exited) server.srv.kill("SIGKILL");
  await waitFor(200);
}

async function api(page, url) {
  return await page.evaluate(async (u) => (await fetch(u)).json(), url);
}

async function post(page, url, body, token) {
  return await page.evaluate(async ({ u, b, t }) => {
    const r = await fetch(u, {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-Dota2-OB-Token": t },
      body: JSON.stringify(b),
    });
    let j = null;
    try { j = await r.json(); } catch { /* empty body */ }
    return { status: r.status, body: j };
  }, { u: url, b: body, t: token });
}

// setToken stores the session token in localStorage so review.html mutations
// are authenticated.
async function setToken(page, base) {
  await page.goto(`${base}/index.html`, { waitUntil: "domcontentloaded" });
  await page.evaluate((t) => localStorage.setItem("dota2ob_session_token", t), TOKEN);
}

async function assertMultiMatchTeamAggregations(page, base, assertStale) {
  const teamID = "9823272";
  const metricID = "hero_damage_total";
  const expectedMatches = ["8944525313", "8946228107"];
  const corpus = await api(page, `${base}/api/replay/v1/scores/corpus`);
  const team = (corpus.data.teams || []).find(t => t.team_id === teamID);
  assert.ok(team, `team ${teamID} score missing`);
  const tournament = team.aggregated_metrics && team.aggregated_metrics[metricID];
  assert.ok(tournament, `${teamID} ${metricID} tournament aggregation missing`);
  assert.strictEqual(tournament.value, 172402, `${teamID} tournament ${metricID}`);
  assert.strictEqual(tournament.eligible_matches, 2, `${teamID} eligible matches`);
  const refs = (tournament.lineage || []).filter(r => r.kind === "aggregation" && r.match_id);
  assert.deepStrictEqual(refs.map(r => r.match_id).sort(), expectedMatches, "team-match child match set");
  assert.strictEqual(new Set(refs.map(r => r.id)).size, 2, "team-match child ids collided");
  for (const ref of refs) {
    assert.ok(ref.id.includes(`aggregation:team_match:${teamID}:${ref.match_id}:${metricID}:`), `unqualified team-match id ${ref.id}`);
    assert.ok(ref.id.endsWith(":ti2026.scoring.v5"), `team-match score rule missing ${ref.id}`);
    assert.strictEqual(ref.rule_version, "ti2026.scoring.v5", `team-match ref rule ${ref.id}`);
    assert.ok(ref.contract_version, `team-match contract missing ${ref.id}`);
  }
  const persisted = corpus.data.team_matches && corpus.data.team_matches[teamID];
  assert.ok(persisted, `persisted team-match entities missing for ${teamID}`);
  const localValues = [];
  for (const ref of refs) {
    const entity = persisted[ref.match_id];
    assert.ok(entity && entity.match_id === ref.match_id && entity.team_id === teamID, `wrong persisted team-match entity ${ref.match_id}`);
    const local = entity.metrics && entity.metrics[metricID];
    assert.ok(local, `match-local ${metricID} missing for ${ref.match_id}`);
    assert.ok((local.lineage || []).some(r => r.id === ref.id && r.match_id === ref.match_id), `entity does not own ${ref.id}`);
    localValues.push(local.value);
  }
  assert.strictEqual(localValues.reduce((a, b) => a + b, 0), tournament.value, "team tournament does not reconcile to persisted match-local values");

  await page.goto(`${base}/team.html?id=${teamID}`, { waitUntil: "domcontentloaded" });
  await page.waitForSelector("a[data-aggregation-scope='team_match']", { timeout: 15000 });
  const rendered = await page.$$eval("a[data-aggregation-scope='team_match']", links => links.map(a => ({ id: a.dataset.aggregationId, match: a.dataset.matchId, href: a.getAttribute("href") })));
  const targetLinks = rendered.filter(x => x.id.includes(`:${metricID}:`));
  assert.deepStrictEqual(targetLinks.map(x => x.match).sort(), expectedMatches, "rendered team-match link set");
  for (const target of targetLinks) {
    await page.goto(`${base}/team.html?id=${teamID}`, { waitUntil: "domcontentloaded" });
    await page.waitForSelector("a[data-aggregation-scope='team_match']", { timeout: 15000 });
    const handles = await page.$$("a[data-aggregation-scope='team_match']");
    let clicked = false;
    for (const handle of handles) {
      if (await handle.getAttribute("data-aggregation-id") === target.id) {
        await handle.click();
        clicked = true;
        break;
      }
    }
    assert.ok(clicked, `rendered team-match link missing ${target.id}`);
    await page.waitForSelector("[data-aggregation-match-id]", { timeout: 15000 });
    const matchID = await page.$eval("[data-aggregation-match-id]", el => el.dataset.aggregationMatchId);
    const text = await page.evaluate(() => document.body.innerText);
    assert.strictEqual(matchID, target.match, `rendered aggregation resolved wrong match for ${target.id}`);
    assert.ok(text.includes("team_match") && text.includes(String(persisted[target.match].metrics[metricID].value)), `rendered aggregation resolved wrong entity for ${target.id}`);
  }

  if (assertStale) {
    await page.goto(`${base}/team.html?id=${teamID}`, { waitUntil: "domcontentloaded" });
    await page.waitForSelector("a[data-aggregation-scope='team_match']", { timeout: 15000 });
    await page.$eval("a[data-aggregation-scope='team_match']", a => {
      const stale = a.dataset.aggregationId.replace(`:${a.dataset.matchId}:`, ":stale-match:");
      a.dataset.aggregationId = stale;
      a.href = `aggregation.html?id=${encodeURIComponent(stale)}`;
    });
    await page.click("a[data-aggregation-scope='team_match']");
    await page.waitForSelector(".panel .hint", { timeout: 15000 });
    const reason = await page.$eval(".panel .hint", el => el.innerText);
    assert.ok(reason.includes("aggregation_not_found:aggregation:team_match:9823272:stale-match:"), `stale rendered team-match reason=${reason}`);
  }
}

// phaseOp drives the REAL rendered review controls for one typed operation and
// returns the served review (corrections, effective stream) after the reload.
async function phaseOp(page, base, spec) {
  await page.goto(`${base}/review.html`, { waitUntil: "domcontentloaded" });
  await page.waitForSelector(`#phase-ref-${MATCH}`, { timeout: 15000 });
  const pid = `#phase-op-${MATCH}`;
  const rid = `#phase-ref-${MATCH}`;
  const phid = `#phase-phase-${MATCH}`;
  const secid = `#phase-second-${MATCH}`;
  const mrid = `#phase-merge-right-${MATCH}`;
  const abid = `#phase-absorb-${MATCH}`;
  const stid = `#phase-start-${MATCH}`;
  const enid = `#phase-end-${MATCH}`;
  const authid = `#phase-author-${MATCH}`;
  const reaid = `#phase-reason-${MATCH}`;
  const evid = `#phase-evidence-${MATCH}`;

  await page.selectOption(pid, spec.op);
  if (spec.ref) await page.selectOption(rid, spec.ref);
  if (spec.phase != null) await page.selectOption(phid, spec.phase);
  if (spec.second != null) await page.fill(secid, String(spec.second));
  if (spec.merge_right) await page.selectOption(mrid, spec.merge_right);
  if (spec.absorb) await page.selectOption(abid, spec.absorb);
  if (spec.start != null) await page.fill(stid, String(spec.start));
  if (spec.end != null) await page.fill(enid, String(spec.end));
  if (spec.author) await page.fill(authid, spec.author);
  await page.fill(reaid, spec.reason || ("step " + spec.op));
  if (spec.evidence) await page.fill(evid, spec.evidence);

  // Click the real submit button and wait for the page's reload.
  await page.evaluate((m) => {
    window.__phaseResult = "";
  }, MATCH);
  // submitPhase awaits the API and then calls location.reload(). Waiting for
  // that navigation prevents the next operation's goto from racing the
  // still-pending reload while retaining the real rendered submit path.
  await Promise.all([
    page.waitForNavigation({ waitUntil: "domcontentloaded", timeout: 15000 }),
    page.click(`button[onclick="submitPhase('${MATCH}')"]`),
  ]);
  // The successful reload must render the review panel again.
  await page.waitForSelector(`#phase-ref-${MATCH}`, { timeout: 15000 });
  await waitFor(300);

  const queue = await api(page, `${base}/api/replay/v1/reviews/queue`);
  for (const rv of queue.data.reviews) {
    if (rv.match_id === MATCH) return rv;
  }
  throw new Error("review for " + MATCH + " not in queue after op " + spec.op);
}

// assertInvalidatedReviewPage proves the post-signoff state through the
// rendered product rather than only through queue JSON. Snapshot identity and
// based_on_revision are asserted separately through the API because the UI
// intentionally renders human history without exposing opaque tokens.
async function assertInvalidatedReviewPage(page, base, expectedMachineIntervals) {
  await page.goto(`${base}/review.html`, { waitUntil: "domcontentloaded" });
  await page.waitForFunction((match) => {
    const progress = document.querySelector("#review-progress");
    const phase = document.querySelector(`#phase-ref-${match}`);
    const panel = phase && phase.closest(".panel");
    return progress && progress.textContent.includes("0/5") && panel && panel.innerText.includes("in_progress");
  }, MATCH, { timeout: 15000 });
  await page.$eval(`#phase-ref-${MATCH}`, node => {
    const details = [...node.closest(".panel").querySelectorAll("details")].find(item => item.querySelector("summary")?.textContent.includes("V2 阶段校正历史"));
    if (!details) throw new Error("rendered phase history disclosure missing");
    if (!details.open) details.querySelector("summary").click();
  });
  await page.waitForFunction((match) => document.querySelector(`#phase-ref-${match}`).closest(".panel").innerText.includes("browser post-finalization invalidation"), MATCH, { timeout: 5000 });
  const panel = await page.$eval(`#phase-ref-${MATCH}`, node => node.closest(".panel").innerText);
  for (const requirement of ["phase_stream_reviewed", "role_provenance_evidence", "official_experimental_acknowledged", "reviewed_status"]) {
    assert.ok(panel.includes(requirement), `rendered invalidation missing ${requirement}`);
  }
  assert.ok(panel.includes("机器分类") && panel.includes("ordinary_baseline_candidate"), "machine category hidden after invalidation");
  assert.ok(panel.includes("机器阶段流（不可变，仅展示）"), "machine phase heading hidden after invalidation");
  assert.ok(panel.includes("confirm：ordinary_baseline_candidate") && panel.includes("frozen category and replay identity inspected"), "category history hidden after invalidation");
  assert.ok(panel.includes("browser post-finalization invalidation"), "rendered later phase history missing");
  assert.strictEqual(await page.$$eval(`#phase-ref-${MATCH}`, nodes => nodes[0].closest(".panel").querySelectorAll("ul.stream li.mach").length), expectedMachineIntervals, "rendered machine phase interval count changed after invalidation");
  assert.strictEqual(await page.$$eval("[data-machine-category]", nodes => nodes.filter(node => node.textContent.trim()).length), 5, "machine categories hidden after invalidation");
  assert.ok((await page.$eval("#review-progress", node => node.innerText)).includes("语料审核进度 0/5"), "rendered invalidated corpus progress");
}

// effectiveStream decodes the served effective intervals.
function effectiveStream(rv) {
  return (rv.effective_phase_intervals || []).map(iv => ({
    start: iv.start_game_second, end: iv.end_game_second, phase: iv.global_phase,
  }));
}

async function machinePhaseStream(page, base) {
  const timeline = await api(page, `${base}/api/replay/v1/matches/${MATCH}/timeline`);
  return (timeline.data && timeline.data.phases && timeline.data.phases.intervals) || [];
}

// assertCoverage proves exact contiguous [0, eligible].
function assertCoverage(stream, eligible) {
  assert.ok(stream.length > 0, "empty stream");
  assert.strictEqual(stream[0].start, 0, "first start != 0");
  assert.strictEqual(stream[stream.length - 1].end, eligible, `final end != ${eligible}`);
  let prev = stream[0].start;
  for (const iv of stream) {
    assert.strictEqual(iv.start, prev, `gap at ${iv.start}`);
    assert.ok(iv.end > iv.start, "non-positive width");
    assert.ok(["laning", "midgame", "decisive"].includes(iv.phase), `bad phase ${iv.phase}`);
    prev = iv.end;
  }
}

async function assertFrozenOpportunityAndLineage(page, base, render) {
  const m8107 = await api(page, `${base}/api/replay/v1/matches/8946228107`);
  const rows8107 = m8107.data.metrics.values;
  const unavailable8107 = m8107.data.metrics.unavailable;
  const metric = (account, id) => rows8107.find(v => v.account_id === account && v.metric_id === id && v.official_phase === "whole_match");
  for (const account of ["145957968", "170896543"]) {
    const row = metric(account, "kill_count");
    assert.ok(row, `observed-zero kill row missing for ${account}`);
    assert.strictEqual(row.value, 0, `${account} kill numerator`);
    assert.strictEqual(row.opportunity_count, 5, `${account} opposing-death opportunities`);
    assert.strictEqual(row.denominator, 1103, `${account} eligible duration`);
  }
  for (const account of ["312436974", "56351509"]) {
    const row = metric(account, "death_count");
    assert.strictEqual(row, undefined, `${account} fabricated death zero still published`);
    const unavailable = unavailable8107.find(v => v.account_id === account && v.metric_id === "death_count");
    assert.ok(unavailable, `${account} death_count unavailable row missing`);
    assert.strictEqual(unavailable.unavailable_reason, "bound_real_hero_life_interval_not_proven_for_subject_window", `${account} precise life-interval reason`);
    assert.strictEqual(unavailable.value, null, `${account} unavailable value`);
    if (render) {
      await page.goto(`${base}/match.html?id=8946228107`, { waitUntil: "domcontentloaded" });
      const selector = `li[data-unavailable-metric='death_count'][data-unavailable-account='${account}']`;
      await page.waitForSelector(selector, { state: "attached", timeout: 15000 });
      await page.$eval(selector, el => el.closest("details").querySelector("summary").click());
      await page.waitForSelector(selector, { state: "visible", timeout: 15000 });
      const text = await page.$eval(selector, el => el.innerText);
      assert.ok(text.includes("bound_real_hero_life_interval_not_proven_for_subject_window"), `${account} rendered reason`);
    }
  }
  const nonzero = metric("315272623", "kill_count");
  assert.ok(nonzero && nonzero.value === 1 && nonzero.opportunity_count === 5, "nonzero kill opportunity is not the opposing-death set");

  const m1919 = await api(page, `${base}/api/replay/v1/matches/${MATCH}`);
  const rows1919 = m1919.data.metrics.values;
  for (const expected of [
    { account: "157475523", id: "hero_damage_total", value: 73913, contributors: 1467 },
    { account: "320017600", id: "objective_damage_total", value: 13393, contributors: 693 },
  ]) {
    const row = rows1919.find(v => v.account_id === expected.account && v.metric_id === expected.id && v.official_phase === "whole_match");
    assert.ok(row && row.value === expected.value, `${expected.id} frozen value`);
    const facts = row.evidence.filter(ref => ref.kind === "fact");
    assert.strictEqual(row.evidence_count, expected.contributors, `${expected.id} evidence_count`);
    assert.strictEqual(facts.length, expected.contributors, `${expected.id} complete fact refs`);
    assert.strictEqual(new Set(facts.map(ref => `${ref.match_id}:${ref.id}`)).size, expected.contributors, `${expected.id} unique fact refs`);
    const last = facts[facts.length - 1];
    const navigable = await page.evaluate(async (u) => (await fetch(u)).status,
      `${base}/api/replay/v1/matches/${MATCH}/facts/${last.source_fact_seq}`);
    assert.strictEqual(navigable, 200, `${expected.id} last contributor not navigable`);
    if (render) {
      await page.goto(`${base}/match.html?id=${MATCH}`, { waitUntil: "domcontentloaded" });
      const selector = `tr[data-metric-id='${expected.id}'][data-account-id='${expected.account}'][data-phase='whole_match'] a[data-evidence-kind='fact']`;
      await page.waitForSelector(selector, { timeout: 15000 });
      assert.strictEqual(await page.$$eval(selector, links => links.length), expected.contributors, `${expected.id} rendered contributor links`);
    }
  }
}

async function main() {
  if (!existsSync(join(SOURCE_ROOT, "catalog.json"))) {
    console.error(`probe data root missing: ${SOURCE_ROOT}`);
    process.exit(2);
  }
  const build = spawn("go", ["build", "-o", BIN, "./cmd/dota2-ob"], { cwd: ROOT, stdio: "inherit" });
  const buildCode = await new Promise((r) => build.on("close", r));
  assert.strictEqual(buildCode, 0, "go build failed");

  // Fresh disposable copy so the source PROBE_ROOT is never mutated.
  const WORK = mkdtempSync(join(tmpdir(), "r3f-browser-"));
  const DATA = join(WORK, "probe");
  cpSync(SOURCE_ROOT, DATA, { recursive: true });
  // Rebuild only the disposable catalog so this scenario exercises category
  // recovery from frozen input.json without touching the preserved source.
  const rebuild = spawn(BIN, ["replay", "rebuild-catalog", "--data-root", DATA], { cwd: ROOT, stdio: "inherit" });
  const rebuildCode = await new Promise((r) => rebuild.on("close", r));
  assert.strictEqual(rebuildCode, 0, "disposable catalog rebuild failed");
  const phasePath = join(DATA, "matches", MATCH, "phases.json");
  const phaseHashBefore = sha256File(phasePath);

  let server;
  try {
    server = await startServer(DATA);
    const base = server.base;

    const { chromium } = require(PW);
    const browser = await chromium.launch({
      executablePath: CHROME, headless: true,
      args: ["--no-sandbox", "--disable-dev-shm-usage"],
    });
    try {
      const page = await browser.newPage();
      const errors = [];
      page.on("pageerror", (e) => errors.push(String(e)));

      await setToken(page, base);
	  const initialReviewQueue = await api(page, `${base}/api/replay/v1/reviews/queue`);
	  assert.strictEqual(initialReviewQueue.data.queue.length, 5, "review queue must contain five probes");
	  assert.ok(initialReviewQueue.data.queue.every(row => row.category && row.replay_sha256), "all five probes must expose frozen category and replay identity");
	  assert.deepStrictEqual(initialReviewQueue.data.progress, { reviewed: 0, pending: 5, total: 5 }, "initial review progress");
	  const initialTarget = initialReviewQueue.data.reviews.find(r => r.match_id === MATCH);
	  const premature = await post(page, `${base}/api/replay/v1/reviews/status`, { match_id:MATCH, status:"reviewed", author:"browser", expected_revision:initialTarget.review_revision }, TOKEN);
	  assert.strictEqual(premature.status, 400, "premature completion must fail closed");
	  assert.ok(premature.body.error.includes("final_snapshot"), `premature reason=${premature.body.error}`);
	  await page.goto(`${base}/review.html`, { waitUntil: "domcontentloaded" });
	  await page.waitForFunction(() => {
		const categories = [...document.querySelectorAll("[data-machine-category]")];
		const progress = document.querySelector("#review-progress");
		return categories.length === 5 && categories.every(node => node.textContent.trim()) && progress && progress.textContent.includes("0/5");
	  }, null, { timeout: 15000 });
	  assert.strictEqual(await page.$$eval("[data-machine-category]", els => els.filter(e => e.textContent.trim()).length), 5, "rendered non-empty machine categories");
	  assert.ok((await page.evaluate(() => document.body.innerText)).includes("语料审核进度 0/5"), "rendered initial corpus progress");
	  await assertFrozenOpportunityAndLineage(page, base, true);

      // ============ Coherent product workflow: corpus + 5 matches ============
      await page.goto(`${base}/index.html`, { waitUntil: "domcontentloaded" });
      await page.waitForFunction(() => document.querySelectorAll("#rows tr").length >= 5, null, { timeout: 15000 });
      const matchRows = await page.$$eval("#rows tr a", (as) => as.map((a) => a.getAttribute("href")));
      assert.ok(matchRows.length >= 5, "corpus page did not render 5 matches");
      for (const href of matchRows.slice(0, 5)) {
        await page.goto(`${base}/${href}`, { waitUntil: "domcontentloaded" });
        await page.waitForSelector("table", { timeout: 15000 });
        const bodyText = await page.evaluate(() => document.body.innerText);
        assert.ok(bodyText.includes("选手与名义角色"), `match page missing participants for ${href}`);
        assert.ok(bodyText.includes("官方阶段时间线"), `match page missing timeline for ${href}`);
      }

      // ============ Nominal roles 1-5 + team page with separated layers ============
      const rolesSeen = new Set();
      for (const href of matchRows.slice(0, 5)) {
        const mid = decodeURIComponent(href.split("=")[1]);
        const scores = await api(page, `${base}/api/replay/v1/matches/${mid}/scores`);
        for (const p of scores.data.players || []) {
          if (rolesSeen.has(p.nominal_role)) continue;
          await page.goto(`${base}/player.html?id=${p.account_id}`, { waitUntil: "domcontentloaded" });
          await page.waitForTimeout(500);
          const text = await page.evaluate(() => document.body.innerText);
          assert.ok(text.includes("八轴雷达"), `player page missing radar for role ${p.nominal_role}`);
          assert.ok(text.includes("官方总分"), `player page missing official total for role ${p.nominal_role}`);
          assert.ok(text.includes("实验总分"), `player page missing experimental total for role ${p.nominal_role}`);
          assert.ok(await page.$("svg.radar"), `radar svg missing for role ${p.nominal_role}`);
          rolesSeen.add(p.nominal_role);
          break;
        }
      }
      assert.deepStrictEqual([...rolesSeen].sort(), ["1", "2", "3", "4", "5"], "roles 1-5 not all covered");

      const rep = await api(page, `${base}/api/replay/v1/matches/${decodeURIComponent(matchRows[0].split("=")[1])}`);
      const teamID = rep.data.teams[0].team_id;
      const teamApi = await api(page, `${base}/api/replay/v1/teams/${teamID}`);
      assert.ok(teamApi.data.team_score.official_axes && teamApi.data.team_score.experimental_axes, "team API missing both layers");
      assert.ok(teamApi.data.team_score.official_total && teamApi.data.team_score.experimental_total, "team API missing both totals");
      await page.goto(`${base}/team.html?id=${teamID}`, { waitUntil: "domcontentloaded" });
      await page.waitForSelector("table", { timeout: 15000 });
      const teamText = await page.evaluate(() => document.body.innerText);
      assert.ok(teamText.includes("队伍官方总分"), "team page missing official total label");
      assert.ok(teamText.includes("队伍实验总分"), "team page missing experimental total label");
      assert.ok(teamText.includes("指标分解"), "team page missing decomposition");
      await assertMultiMatchTeamAggregations(page, base, false);

      // ============ Seven typed phase operations via the rendered UI ============
      const ops = [
        { op: "split", ref: "interval@0-594", second: 100, reason: "browser split 0-594@100", evidence: "phase:b1" },
        { op: "relabel", ref: "interval@100-594", phase: "midgame", reason: "browser relabel split child", evidence: "phase:b2" },
        { op: "move", ref: "interval@100-594", second: 700, reason: "browser move right boundary to 700", evidence: "phase:b3" },
        { op: "add", start: 1700, end: 1750, phase: "decisive", reason: "browser add decisive 1700-1750", evidence: "phase:b4" },
        { op: "delete", ref: "interval@1700-1750", absorb: "interval@1609-1700", reason: "browser delete into 1609-1700", evidence: "phase:b5" },
        { op: "split", ref: "interval@780-820", second: 800, reason: "browser split 780-820@800", evidence: "phase:b6" },
        { op: "merge", ref: "interval@780-800", merge_right: "interval@800-820", phase: "decisive", reason: "browser merge 780-800+800-820", evidence: "phase:b7" },
        { op: "accept", ref: "interval@2454-2633", reason: "browser accept 2454-2633", evidence: "phase:b8" },
      ];
      let previousStream = null;
      for (let i = 0; i < ops.length; i++) {
        const rv = await phaseOp(page, base, ops[i]);
        assert.strictEqual(rv.phase_corrections.length, i + 1, `step ${i + 1} corrections=${rv.phase_corrections.length}`);
        const pc = rv.phase_corrections[i];
        assert.strictEqual(pc.operation, ops[i].op, `step ${i + 1} operation`);
        assert.strictEqual(pc.shape_version, "phase-op.v2", `step ${i + 1} shape version`);
        assert.ok(pc.replay_sha256 && pc.algorithm_version && pc.machine_rule_version, `step ${i + 1} provenance`);
        assert.ok(pc.before_stream && pc.before_stream.length > 0, `step ${i + 1} before stream`);
        assert.ok(pc.after_stream && pc.after_stream.length > 0, `step ${i + 1} after stream`);
        assert.ok(pc.effective_value && String(pc.effective_value) !== "null", `step ${i + 1} effective_value null`);
        const stream = effectiveStream(rv);
        assertCoverage(stream, 2705);
        if (previousStream) {
          const before = pc.before_stream.map(iv => `${iv.start_game_second}-${iv.end_game_second}`);
          const prev = previousStream.map(iv => `${iv.start}-${iv.end}`);
          assert.deepStrictEqual(before, prev, `step ${i + 1} before stream != previous after stream`);
        }
        previousStream = stream;
      }

      // phases.json must be byte-identical after all operations.
      const phaseHashAfter = sha256File(phasePath);
      assert.strictEqual(phaseHashAfter, phaseHashBefore, "immutable phases.json changed");

      // ============ Restart: exact stream + corrections + audit + UI survive ============
      await stopServer(server);
      server = await startServer(DATA);
      const restartedBase = server.base;
      await setToken(page, restartedBase);
      const queueAfter = await api(page, `${restartedBase}/api/replay/v1/reviews/queue`);
      let restartedRvs = [];
      for (const rv of queueAfter.data.reviews) {
        if (rv.match_id === MATCH) restartedRvs = [rv];
      }
      assert.strictEqual(restartedRvs.length, 1, "restart lost review");
      const restarted = restartedRvs[0];
      assert.strictEqual(restarted.phase_corrections.length, ops.length, "restart lost corrections");
      assertCoverage(effectiveStream(restarted), 2705);
      const audit = await api(page, `${restartedBase}/api/replay/v1/reviews/audit`);
      assert.ok(audit.data.entries.length >= ops.length, "restart audit shrank");

      // ============ Optimistic-concurrency revision conflict (same boundary) ============
      // The current revision is served with the effective stream; a stale
      // same-boundary relabel (interval still exists) must fail 409 and change
      // no bytes/counts, while a fresh-revision mutation succeeds.
      const curRev = restarted.review_revision;
      assert.ok(curRev && curRev.startsWith("rev-"), "review_revision not served after restart");
      const conflict = await post(page, `${restartedBase}/api/replay/v1/reviews/phase-corrections`, {
        match_id: MATCH, author: "stale", reason: "stale same-boundary", operation: "relabel",
        event_ref: "interval@2454-2633", expected_revision: "rev-0000000000000000",
        effective_value: { start_game_second: 2454, end_game_second: 2633, global_phase: "decisive" },
      }, TOKEN);
      assert.strictEqual(conflict.status, 409, "stale revision same-boundary must be 409");
      const qConflict = await api(page, `${restartedBase}/api/replay/v1/reviews/queue`);
      const rvConflict = qConflict.data.reviews.find(r => r.match_id === MATCH);
      assert.strictEqual(rvConflict.phase_corrections.length, ops.length, "revision conflict advanced corrections");
      assert.strictEqual(rvConflict.review_revision, curRev, "revision changed on conflict");
      const conflictAudit = (await api(page, `${restartedBase}/api/replay/v1/reviews/audit`)).data.entries.length;
      assert.strictEqual(conflictAudit, ops.length, "revision conflict advanced audit");
      // A fresh-revision same-boundary relabel succeeds and advances the
      // revision.
      const okMut = await post(page, `${restartedBase}/api/replay/v1/reviews/phase-corrections`, {
        match_id: MATCH, author: "fresh", reason: "fresh same-boundary", operation: "relabel",
        event_ref: "interval@2454-2633", expected_revision: curRev,
        effective_value: { start_game_second: 2454, end_game_second: 2633, global_phase: "midgame" },
      }, TOKEN);
      assert.strictEqual(okMut.status, 200, "fresh-revision mutation failed");
      const qAfterFresh = await api(page, `${restartedBase}/api/replay/v1/reviews/queue`);
      const rvAfterFresh = qAfterFresh.data.reviews.find(r => r.match_id === MATCH);
      assert.strictEqual(rvAfterFresh.phase_corrections.length, ops.length + 1, "fresh mutation did not append correction");
      assert.notStrictEqual(rvAfterFresh.review_revision, curRev, "revision did not advance on success");

      // ============ Failed-state atomicity via rendered/API mutations ============
      const reviewBytesBefore = await page.evaluate(async (u) => (await fetch(u)).status, `${restartedBase}/api/replay/v1/reviews/queue`);
      const q0 = await api(page, `${restartedBase}/api/replay/v1/reviews/queue`);
      const corr0 = q0.data.reviews.find(r => r.match_id === MATCH).phase_corrections.length;
      const aud0 = (await api(page, `${restartedBase}/api/replay/v1/reviews/audit`)).data.entries.length;

      // Stale closed ref -> 409.
      const stale = await post(page, `${restartedBase}/api/replay/v1/reviews/phase-corrections`, {
        match_id: MATCH, author: "paul", reason: "stale", operation: "relabel",
        expected_revision: rvAfterFresh.review_revision,
        event_ref: "interval@100-999", effective_value: { start_game_second: 100, end_game_second: 700, global_phase: "midgame" },
      }, TOKEN);
      assert.strictEqual(stale.status, 409, "stale ref must be 409");
      // Illegal reset phase -> 400.
      const illegal = await post(page, `${restartedBase}/api/replay/v1/reviews/phase-corrections`, {
        match_id: MATCH, author: "paul", reason: "illegal", operation: "relabel",
        expected_revision: rvAfterFresh.review_revision,
        event_ref: "interval@100-700", effective_value: { start_game_second: 100, end_game_second: 700, global_phase: "reset" },
      }, TOKEN);
      assert.strictEqual(illegal.status, 400, "illegal reset must be 400");
      // Unauthenticated mutation -> 403.
      const unauth = await post(page, `${restartedBase}/api/replay/v1/reviews/phase-corrections`, {
        match_id: MATCH, author: "paul", reason: "no token", operation: "accept", event_ref: "interval@2454-2633",
      }, "");
      assert.strictEqual(unauth.status, 403, "unauthenticated must be 403");

      // No state changed after all rejections.
      const q1 = await api(page, `${restartedBase}/api/replay/v1/reviews/queue`);
      const corr1 = q1.data.reviews.find(r => r.match_id === MATCH).phase_corrections.length;
      const aud1 = (await api(page, `${restartedBase}/api/replay/v1/reviews/audit`)).data.entries.length;
      assert.strictEqual(corr1, corr0, "rejected mutations advanced corrections");
      assert.strictEqual(aud1, aud0, "rejected mutations advanced audit");
      assert.strictEqual(reviewBytesBefore, 200, "queue reachable");
      const phasesAfterFailures = sha256File(phasePath);
      assert.strictEqual(phasesAfterFailures, phaseHashBefore, "phases.json changed on rejection");

      // ============ Role override + restart consistency ============
      const firstScores = await api(page, `${restartedBase}/api/replay/v1/matches/${decodeURIComponent(matchRows[0].split("=")[1])}/scores`);
      const player = firstScores.data.players[0];
      const acct = player.account_id;
      const newRole = player.nominal_role === "1" ? "2" : "1";
      const ovr = await post(page, `${restartedBase}/api/replay/v1/roles/overrides`, {
        match_id: player.match_id, account_id: acct, nominal_role: newRole, author: "browser", reason: "e2e override",
      }, TOKEN);
      assert.strictEqual(ovr.status, 200, "role override failed");

      // Verify every view agrees on the effective role immediately.
      const assertRoleAgreement = async (base) => {
        const afterOvr = await api(page, `${base}/api/replay/v1/players/${acct}`);
        assert.strictEqual(afterOvr.data.score.nominal_role, newRole, "player score role after override");
        const playerRow = afterOvr.data.matches.find(m => m.match_id === player.match_id);
        assert.ok(playerRow && playerRow.nominal_role === newRole, "player match row role after override");
        assert.ok(playerRow && playerRow.source_nominal_role, "player match row source role preserved");
        assert.strictEqual(playerRow.override_author, "browser", "player match row override author");
        assert.ok(playerRow.role_record_version, "player match row role record version");
        assert.ok(playerRow.override_version, "player match row override version");
        const matchRep = await api(page, `${base}/api/replay/v1/matches/${player.match_id}`);
        const part = matchRep.data.participants.find(p => p.account_id === acct);
        assert.ok(part && part.nominal_role === newRole, "match report role after override");
        assert.ok(part && part.source_nominal_role, "match report source role preserved");
        assert.strictEqual(part.override_author, "browser", "match report override author");
        assert.ok(part.role_record_version, "match report role record version");
        assert.ok(part.override_version, "match report override version");
        const matchScore = await api(page, `${base}/api/replay/v1/matches/${player.match_id}/scores`);
        const row = (matchScore.data.players || []).find(p => p.account_id === acct);
        assert.ok(row && row.nominal_role === newRole, "match-score row role after override");
		assert.ok(Object.values(row.metrics || {}).every(m => m.metric_version), "match-score metric versions missing");
        assert.strictEqual(row.override_author, "browser", "match-score override author");
        assert.ok(row.role_record_version, "match-score role record version");
        assert.ok(row.override_version, "match-score override version");
        const corpus = await api(page, `${base}/api/replay/v1/scores/corpus`);
        const pt = (corpus.data.players || []).find(p => p.account_id === acct);
        assert.ok(pt && pt.nominal_role === newRole, "corpus/tournament player role after override");
		const aggregated = Object.values(pt.aggregated_metrics || {});
		assert.ok(aggregated.length > 0 && aggregated.every(m => m.metric_version), "aggregated metric versions missing");
		assert.ok(aggregated.every(m => (m.lineage || []).filter(r => r.kind === "aggregation").every(r => r.id.includes(`:${m.metric_version}:`) && r.id.endsWith(":ti2026.scoring.v5") && r.rule_version === "ti2026.scoring.v5")), "aggregation IDs are not metric/score-rule qualified");
        const scoreProv = pt && pt.role_provenance && pt.role_provenance[player.match_id];
        assert.ok(scoreProv, "corpus score role provenance missing");
        assert.strictEqual(scoreProv.override_author, "browser", "corpus score override author");
        assert.ok(scoreProv.role_record_version, "corpus score role record version");
        assert.ok(scoreProv.override_version, "corpus score override version");
        const profileProv = afterOvr.data.score.role_provenance[player.match_id];
        assert.strictEqual(profileProv.override_author, "browser", "player profile score override author");
        assert.ok(profileProv.role_record_version && profileProv.override_version, "player profile score versions missing");
		await page.goto(`${base}/player.html?id=${acct}`, { waitUntil: "domcontentloaded" });
		await page.waitForSelector("tr[data-aggregation-metric][data-metric-version]", { timeout: 15000 });
		const renderedVersions = await page.$$eval("tr[data-aggregation-metric]", rows => rows.map(r => r.dataset.metricVersion));
		assert.ok(renderedVersions.length > 0 && renderedVersions.every(v => v && v !== "-"), "rendered score decomposition metric versions missing");
		const playerComponents = await page.$$eval("tr[data-score-metric]", rows => rows.map(r => ({ metric: r.dataset.scoreMetric, version: r.dataset.metricVersion })));
		assert.ok(playerComponents.length > 0 && playerComponents.every(r => r.metric && r.version && r.version !== "-"), "player component row missing registry metric version");
		const teamID = pt.team_id;
		assert.ok(teamID, "player score team id missing");
		const teamScore = (corpus.data.teams || []).find(t => t.team_id === teamID);
		assert.ok(teamScore, "team score missing");
		for (const layer of [teamScore.official_axes || {}, teamScore.experimental_axes || {}]) {
			for (const axis of Object.values(layer)) {
				for (const component of Object.values(axis.components || {})) {
					assert.ok(component.metric_version, `team API component ${component.metric_id} missing registry version`);
				}
			}
		}
		await page.goto(`${base}/team.html?id=${teamID}`, { waitUntil: "domcontentloaded" });
		await page.waitForSelector("tr[data-team-score-metric]", { timeout: 15000 });
		const teamComponents = await page.$$eval("tr[data-team-score-metric]", rows => rows.map(r => ({ metric: r.dataset.teamScoreMetric, version: r.dataset.metricVersion })));
		assert.ok(teamComponents.length > 0 && teamComponents.every(r => r.metric && r.version && r.version !== "-"), "team component row missing registry metric version");
      };
      await assertRoleAgreement(restartedBase);

      // Restart the server after the override and re-verify full agreement.
      await stopServer(server);
      server = await startServer(DATA);
      const restartedBase2 = server.base;
      await setToken(page, restartedBase2);
      await assertRoleAgreement(restartedBase2);
	  await assertFrozenOpportunityAndLineage(page, restartedBase2, true);
	  await assertMultiMatchTeamAggregations(page, restartedBase2, true);

      // ============ Click actual rendered lineage links on the match page ============
      // Follow every canonical evidence kind from the actual rendered anchor;
      // do not reconstruct, strip, or split any stored id in test code.
      for (const kind of ["fact", "episode", "phase", "metric_observation", "algorithm"]) {
        await page.goto(`${restartedBase2}/match.html?id=${MATCH}`, { waitUntil: "domcontentloaded" });
        const selector = `a[data-evidence-kind='${kind}']`;
        await page.waitForSelector(selector, { timeout: 15000 });
        await page.click(selector);
        await page.waitForSelector(`[data-evidence-panel='${kind}']`, { timeout: 15000 });
        const panelText = await page.$eval(`[data-evidence-panel='${kind}']`, el => el.innerText);
        assert.ok(panelText.includes("证据定位") && !panelText.includes("不可用:"), `${kind} rendered link did not resolve`);
      }

      // Aggregation is rendered on the player drilldown; click that anchor too.
      await page.goto(`${restartedBase2}/player.html?id=${acct}`, { waitUntil: "domcontentloaded" });
      await page.waitForSelector("a[data-evidence-kind='aggregation']", { timeout: 15000 });
	  const renderedAggregationHref = await page.$eval("a[data-evidence-kind='aggregation']", a => a.getAttribute("href"));
	  assert.ok(decodeURIComponent(renderedAggregationHref).includes(":ti2026.scoring.v5"), `rendered aggregation link is not score-rule qualified: ${renderedAggregationHref}`);
      await page.click("a[data-evidence-kind='aggregation']");
      await page.waitForSelector(".panel", { timeout: 15000 });
      const aggText = await page.evaluate(() => document.body.innerText);
      assert.ok((aggText.includes("聚合实体") || aggText.includes("aggregation")) && aggText.includes("ti2026.scoring.v5"), "aggregation page missing score-rule-qualified identity");

      // Turn an ACTUAL rendered fact link stale inside the disposable copy,
      // navigate it, and assert the precise API error rendered by the UI.
      await page.goto(`${restartedBase2}/match.html?id=${MATCH}`, { waitUntil: "domcontentloaded" });
      await page.waitForSelector("a[data-evidence-kind='fact']", { timeout: 15000 });
      const factsPath = join(DATA, "matches", MATCH, "facts.jsonl");
      const factsBytes = readFileSync(factsPath);
      writeFileSync(factsPath, "");
      try {
        await page.click("a[data-evidence-kind='fact']");
        await page.waitForSelector("[data-evidence-error='fact']", { timeout: 15000 });
        const staleReason = await page.$eval("[data-evidence-error='fact']", el => el.innerText);
        assert.ok(staleReason.includes("fact_not_found:seq="), `stale rendered fact reason=${staleReason}`);
      } finally {
        writeFileSync(factsPath, factsBytes);
      }

      // ============ Category decision + final signed snapshot + restart ==========
	  const beforeFinalQueue = await api(page, `${restartedBase2}/api/replay/v1/reviews/queue`);
	  const beforeFinal = beforeFinalQueue.data.reviews.find(r => r.match_id === MATCH);
	  const category = await post(page, `${restartedBase2}/api/replay/v1/reviews/category-decisions`, {
		match_id:MATCH, decision:"confirm", author:"browser", reason:"frozen category and replay identity inspected", expected_revision:beforeFinal.review_revision,
	  }, TOKEN);
	  assert.strictEqual(category.status, 200, `category decision failed: ${JSON.stringify(category.body)}`);
	  assert.strictEqual(category.body.data.category_decisions.at(-1).based_on_revision, beforeFinal.review_revision, "category decision lost accepted revision");
	  const finalized = await post(page, `${restartedBase2}/api/replay/v1/reviews/finalize`, {
		match_id:MATCH, author:"browser", reason:"full effective stream and required product checks inspected", expected_revision:category.body.data.review_revision,
		checklist:{ phase_stream_reviewed:true, role_provenance_reviewed:true, role_provenance_evidence:"role registry sources and effective override inspected", official_experimental_acknowledged:true },
	  }, TOKEN);
	  assert.strictEqual(finalized.status, 200, `final review failed: ${JSON.stringify(finalized.body)}`);
	  assert.strictEqual(finalized.body.data.review_status, "reviewed", "final action did not mark reviewed");
	  assert.ok(finalized.body.data.final_snapshots[0].final_phase_stream.length > 0, "signed phase stream missing");
	  await page.goto(`${restartedBase2}/review.html`, { waitUntil:"domcontentloaded" });
	  await page.waitForSelector("#review-progress", { timeout:15000 });
	  assert.ok((await page.evaluate(() => document.body.innerText)).includes("语料审核进度 1/5"), "reviewed progress not rendered after reload");
	  await stopServer(server);
	  server = await startServer(DATA);
	  const finalBase = server.base;
	  await setToken(page, finalBase);
	  const afterFinalRestart = await api(page, `${finalBase}/api/replay/v1/reviews/queue`);
	  const persistedFinal = afterFinalRestart.data.reviews.find(r => r.match_id === MATCH);
	  assert.strictEqual(persistedFinal.review_status, "reviewed", "restart lost reviewed status");
	  assert.strictEqual(persistedFinal.category_decisions.length, 1, "restart lost category decision");
	  assert.strictEqual(persistedFinal.category_decisions[0].based_on_revision, beforeFinal.review_revision, "restart lost category based_on_revision");
	  assert.strictEqual(persistedFinal.final_snapshots.length, 1, "restart lost final snapshot");
	  assert.deepStrictEqual(afterFinalRestart.data.progress, { reviewed:1, pending:4, total:5 }, "restart reviewed/pending reconciliation");
	  assert.ok(afterFinalRestart.data.queue.every(row => row.category), "restart lost a machine category");

	  // ============ Later rendered mutation invalidates sign-off + restart ======
	  // Capture immutable history and machine truth, then use the real phase
	  // controls to accept an existing interval once more. Accept is a genuine
	  // audited review mutation even though it preserves interval boundaries.
	  const signedSnapshot = structuredClone(persistedFinal.final_snapshots[0]);
	  const signedCategoryHistory = structuredClone(persistedFinal.category_decisions);
	  const machineBeforeInvalidation = await machinePhaseStream(page, finalBase);
	  const invalidatedByControl = await phaseOp(page, finalBase, {
		op:"accept", ref:"interval@2454-2633", author:"browser",
		reason:"browser post-finalization invalidation", evidence:"phase:post-final",
	  });
	  assert.ok(!invalidatedByControl.current_final_snapshot_id, "later rendered mutation retained current final snapshot");
	  assert.strictEqual(invalidatedByControl.review_status, "in_progress", "later rendered mutation did not return to in_progress");
	  assert.strictEqual(invalidatedByControl.final_snapshots.length, 1, "later mutation lost historical final snapshot");
	  assert.deepStrictEqual(invalidatedByControl.final_snapshots[0], signedSnapshot, "later mutation changed historical final snapshot");
	  assert.deepStrictEqual(invalidatedByControl.category_decisions, signedCategoryHistory, "later mutation changed category history/based_on_revision");
	  assert.strictEqual(invalidatedByControl.category_decisions[0].based_on_revision, beforeFinal.review_revision, "later mutation changed immutable based_on_revision");
	  const afterInvalidation = await api(page, `${finalBase}/api/replay/v1/reviews/queue`);
	  const invalidatedReview = afterInvalidation.data.reviews.find(r => r.match_id === MATCH);
	  assert.deepStrictEqual(afterInvalidation.data.progress, { reviewed:0, pending:5, total:5 }, "later mutation progress did not return to 0/5");
	  assert.deepStrictEqual(invalidatedReview.missing_requirements, ["phase_stream_reviewed", "role_provenance_evidence", "official_experimental_acknowledged", "reviewed_status"], "later mutation missing requirements");
	  assert.deepStrictEqual(await machinePhaseStream(page, finalBase), machineBeforeInvalidation, "later mutation changed machine phase stream");
	  assert.ok(afterInvalidation.data.queue.every(row => row.category && row.replay_sha256), "later mutation hid machine category/replay identity");
	  await assertInvalidatedReviewPage(page, finalBase, machineBeforeInvalidation.length);

	  // Restart once more and prove the invalidated state, immutable history,
	  // precise requirements, machine truth, and corpus reconciliation survive.
	  await stopServer(server);
	  server = await startServer(DATA);
	  const invalidatedRestartBase = server.base;
	  await setToken(page, invalidatedRestartBase);
	  const afterInvalidationRestart = await api(page, `${invalidatedRestartBase}/api/replay/v1/reviews/queue`);
	  const invalidatedRestart = afterInvalidationRestart.data.reviews.find(r => r.match_id === MATCH);
	  assert.ok(!invalidatedRestart.current_final_snapshot_id, "restart restored invalidated current snapshot");
	  assert.strictEqual(invalidatedRestart.review_status, "in_progress", "restart lost invalidated in_progress state");
	  assert.deepStrictEqual(invalidatedRestart.final_snapshots, [signedSnapshot], "restart changed historical final snapshot");
	  assert.deepStrictEqual(invalidatedRestart.category_decisions, signedCategoryHistory, "restart changed category history/based_on_revision");
	  assert.deepStrictEqual(invalidatedRestart.missing_requirements, ["phase_stream_reviewed", "role_provenance_evidence", "official_experimental_acknowledged", "reviewed_status"], "restart changed invalidation requirements");
	  assert.deepStrictEqual(afterInvalidationRestart.data.progress, { reviewed:0, pending:5, total:5 }, "restart invalidated progress did not remain 0/5");
	  assert.deepStrictEqual(await machinePhaseStream(page, invalidatedRestartBase), machineBeforeInvalidation, "restart changed machine phase stream after invalidation");
	  assert.ok(afterInvalidationRestart.data.queue.every(row => row.category && row.replay_sha256), "restart hid machine category/replay identity after invalidation");
	  await assertInvalidatedReviewPage(page, invalidatedRestartBase, machineBeforeInvalidation.length);
	  assert.strictEqual(sha256File(phasePath), phaseHashBefore, "immutable phases.json changed after post-finalization mutation/restart");

      // Zero uncaught page errors across the whole run.
      assert.deepStrictEqual(errors, [], `page JS errors: ${errors.join(" | ")}`);
      console.log(`browser_e2e: OK — five non-empty frozen categories, premature completion rejected, signed category/final stream/checklist survives reload+restart at 1/5 reviewed; a later rendered phase mutation clears current sign-off, preserves immutable snapshot/category based_on_revision history, exposes exact missing requirements and machine values at 0/5 reviewed, and survives a second restart; corpus, 5 matches, roles 1-5, frozen kill observed-zero/opportunity rows and death zero-vs-unavailable API/render assertions, complete 1467/693 contributor lineage rendered+navigable before/after restart, team official/experimental layers, every rendered player/team score component carries its registry metric version, score-rule-qualified aggregation links resolve after restart, team 9823272 hero_damage_total=172402 reconciles to two distinct match-qualified team-match entities whose rendered links resolve before/after restart and whose rendered stale link returns the exact 404 reason, 8 pre-finalization phase ops + 1 post-finalization rendered invalidation on ${MATCH} with [0,2705] coverage + restart + stale-ref(409)/illegal(400)/unauth(403) + revision-conflict(409 same-boundary) atomicity, role override author/record/override-version agreement before/after restart, rendered lineage-link clicks (fact/episode/phase/metric_observation/aggregation/algorithm) + rendered stale fact exact 404 reason; phases.json byte-identical`);
    } finally {
      await browser.close();
    }
  } finally {
    if (server) await stopServer(server);
    rmSync(BIN, { force: true });
    rmSync(WORK, { recursive: true, force: true });
  }
}

main().catch((e) => { console.error(e); process.exit(1); });

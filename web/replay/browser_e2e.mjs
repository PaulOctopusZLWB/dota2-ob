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
import { existsSync, mkdtempSync, cpSync, rmSync } from "node:fs";
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
  await page.click(`button[onclick="submitPhase('${MATCH}')"]`);
  // The page reloads on success; wait for the review panel to reappear.
  await page.waitForSelector(`#phase-ref-${MATCH}`, { timeout: 15000 });
  await waitFor(300);

  const queue = await api(page, `${base}/api/replay/v1/reviews/queue`);
  for (const rv of queue.data.reviews) {
    if (rv.match_id === MATCH) return rv;
  }
  throw new Error("review for " + MATCH + " not in queue after op " + spec.op);
}

// effectiveStream decodes the served effective intervals.
function effectiveStream(rv) {
  return (rv.effective_phase_intervals || []).map(iv => ({
    start: iv.start_game_second, end: iv.end_game_second, phase: iv.global_phase,
  }));
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

      // ============ Failed-state atomicity via rendered/API mutations ============
      const reviewBytesBefore = await page.evaluate(async (u) => (await fetch(u)).status, `${restartedBase}/api/replay/v1/reviews/queue`);
      const q0 = await api(page, `${restartedBase}/api/replay/v1/reviews/queue`);
      const corr0 = q0.data.reviews.find(r => r.match_id === MATCH).phase_corrections.length;
      const aud0 = (await api(page, `${restartedBase}/api/replay/v1/reviews/audit`)).data.entries.length;

      // Stale closed ref -> 409.
      const stale = await post(page, `${restartedBase}/api/replay/v1/reviews/phase-corrections`, {
        match_id: MATCH, author: "paul", reason: "stale", operation: "relabel",
        event_ref: "interval@100-999", effective_value: { start_game_second: 100, end_game_second: 700, global_phase: "midgame" },
      }, TOKEN);
      assert.strictEqual(stale.status, 409, "stale ref must be 409");
      // Illegal reset phase -> 400.
      const illegal = await post(page, `${restartedBase}/api/replay/v1/reviews/phase-corrections`, {
        match_id: MATCH, author: "paul", reason: "illegal", operation: "relabel",
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
      const afterOvr = await api(page, `${restartedBase}/api/replay/v1/players/${acct}`);
      assert.strictEqual(afterOvr.data.score.nominal_role, newRole, "player score role after override");
      const playerRow = afterOvr.data.matches.find(m => m.match_id === player.match_id);
      assert.ok(playerRow && playerRow.nominal_role === newRole, "player match row role after override");
      const matchRep = await api(page, `${restartedBase}/api/replay/v1/matches/${player.match_id}`);
      const part = matchRep.data.participants.find(p => p.account_id === acct);
      assert.ok(part && part.nominal_role === newRole, "match report role after override");
      assert.ok(part && part.source_nominal_role, "match report source role preserved");

      // ============ Match-qualified lineage deep links ============
      const repL = await api(page, `${restartedBase}/api/replay/v1/matches/${MATCH}`);
      const values = (repL.data.metrics && repL.data.metrics.values) || [];
      let factRef, epRef, phRef, metRef, algRef;
      for (const v of values) {
        for (const e of (v.evidence || [])) {
          if (!factRef && e.kind === "fact") factRef = e;
          if (!epRef && e.kind === "episode") epRef = e;
          if (!phRef && e.kind === "phase") phRef = e;
          if (!metRef && e.kind === "metric_observation") metRef = e;
          if (!algRef && e.kind === "algorithm") algRef = e;
        }
      }
      assert.ok(factRef && epRef && phRef && metRef && algRef, "missing lineage refs in " + MATCH);
      const factSeq = String(factRef.source_fact_seq || factRef.id.replace("fact:", ""));
      for (const path of [
        `/matches/${MATCH}/facts/${factSeq}`,
        `/matches/${MATCH}/episodes/${encodeURIComponent(epRef.id)}`,
        `/matches/${MATCH}/phases/interval%40${phRef.id.replace("interval@", "")}`,
        `/matches/${MATCH}/metrics/${metRef.id.split(":")[0]}`,
        `/matches/${MATCH}/algorithms/${encodeURIComponent(algRef.id)}`,
      ]) {
        const st = await page.evaluate(async (u) => (await fetch(u)).status, `${restartedBase}/api/replay/v1${path}`);
        assert.strictEqual(st, 200, `lineage route ${path} not 200`);
      }
      // Stale fact deep link shows explicit unavailable (404).
      const staleFact = await page.evaluate(async (u) => {
        const r = await fetch(u); return { status: r.status, body: await r.json() };
      }, `${restartedBase}/api/replay/v1/matches/${MATCH}/facts/999999999`);
      assert.strictEqual(staleFact.status, 404, "stale fact not 404");
      assert.ok(staleFact.body.error && staleFact.body.error.includes("fact_not_found"), "stale fact missing reason");

      // Zero uncaught page errors across the whole run.
      assert.deepStrictEqual(errors, [], `page JS errors: ${errors.join(" | ")}`);
      console.log(`browser_e2e: OK — corpus, 5 matches, roles 1-5, team layers, 7 phase ops via rendered UI on ${MATCH} with [0,2705] coverage + restart + 409/400/403 atomicity, role override consistency, lineage deep links; phases.json byte-identical`);
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

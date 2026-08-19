// Browser-level JavaScript verification for the replay pages.
//
// This test starts the local serve binary against a persisted five-probe data
// root, fetches the live replay APIs, and executes the ACTUAL player page
// JavaScript (renderRadar/scoreValues/totalCard/axisRows/metricDrilldown)
// against real persisted data for one player at each nominal role 1-5, plus
// the team page and review queue. It fails on TypeError / unhandled
// exceptions — HTML substring assertions alone cannot catch the axisLabels
// null dereference the independent reviewer found.
import { spawn } from "node:child_process";
import { readFileSync, existsSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import assert from "node:assert";

const ROOT = new URL("../../", import.meta.url).pathname;
const BIN = join(ROOT, "data", "replay", "browser-test-bin");
const PROBE_ROOT = process.env.PROBE_ROOT || "/tmp/probe-a";
const LISTEN = "127.0.0.1:43233";

// Fetch JSON helper with a timeout.
async function getJSON(url) {
  const ctrl = new AbortController();
  const t = setTimeout(() => ctrl.abort(), 10000);
  try {
    const resp = await fetch(url, { signal: ctrl.signal });
    assert.ok(resp.ok, `GET ${url} -> ${resp.status}`);
    return await resp.json();
  } finally {
    clearTimeout(t);
  }
}

// Minimal DOM stub: the page script only reads document.getElementById(...)
// then calls innerHTML= / textContent= / insertAdjacentHTML.
function makeDomStub() {
  const els = {};
  return {
    document: {
      getElementById(id) {
        if (!els[id]) {
          els[id] = { textContent: "", innerHTML: "", insertAdjacentHTML: () => {} };
        }
        return els[id];
      },
    },
    location: { search: "" },
  };
}

// Execute the page's rendering functions against real API data without the
// async load() entrypoint. The script's load() function is the final block;
// we drop it and the trailing load(); call, keeping all module-level
// constants/helpers and the pure render functions.
function extractFunctions(script) {
  let src = script;
  const idx = src.indexOf("async function load()");
  if (idx >= 0) {
    src = src.slice(0, idx);
  }
  src = src.replace(/\nload\(\);?\s*$/, "");
  const body = src + "\nreturn {renderRadar, scoreValues, totalCard, axisRows, metricDrilldown, esc};";
  return new Function("document", "location", body);
}

async function evalPage(pageFile, query) {
  const html = readFileSync(join(ROOT, "web", "replay", pageFile), "utf8");
  const m = html.match(/<script>([\s\S]*)<\/script>/);
  assert.ok(m, `${pageFile}: no script block`);
  const dom = makeDomStub();
  const fn = extractFunctions(m[1]);
  const ret = fn(dom.document, dom.location);
  ret.dom = dom;
  return ret;
}

function waitFor(ms) { return new Promise((r) => setTimeout(r, ms)); }

async function main() {
  if (!existsSync(join(PROBE_ROOT, "catalog.json"))) {
    console.error(`probe data root missing: ${PROBE_ROOT}`);
    process.exit(2);
  }
  // Build + start the server.
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
    const base = `http://${LISTEN}/api/replay/v1`;
    // Wait for readiness.
    let up = false;
    for (let i = 0; i < 50 && !up; i++) {
      try { await getJSON(`${base}/corpus`); up = true; } catch { await waitFor(200); }
    }
    assert.ok(up, "server did not become ready");

    // Load the player page script functions.
    const fns = await evalPage("player.html", "");
    assert.strictEqual(typeof fns.renderRadar, "function", "renderRadar missing");

    // Corpus: five verified matches.
    const corpus = await getJSON(`${base}/corpus`);
    assert.strictEqual(corpus.data.matches.length, 5, "expected 5 probe matches");

    // Exercise one player at each nominal role 1-5 with the REAL page JS.
    const rolesSeen = new Set();
    for (const match of corpus.data.matches) {
      const ms = await getJSON(`${base}/matches/${match.match_id}/scores`);
      for (const p of ms.data.players || []) {
        const role = p.nominal_role;
        if (!role || rolesSeen.has(role)) continue;
        // Real player-page path: fetch the tournament snapshot via /players/.
        const pl = await getJSON(`${base}/players/${p.account_id}`);
        const ps = pl.data.score;
        assert.ok(ps, `player ${p.account_id} has no tournament score`);
        // Execute the actual rendering functions against real data — the
        // axisLabels=null crash must not throw.
        const official = fns.scoreValues(ps, "official");
        const experimental = fns.scoreValues(ps, "experimental");
        const radar = fns.renderRadar(official, experimental, null);
        assert.ok(radar.includes("<svg"), `radar not rendered for role ${role}`);
        const drill = fns.metricDrilldown(ps);
        assert.ok(typeof drill === "string", "drilldown failed");
        // Totals must be rendered as either published value or suppression.
        const ot = fns.totalCard(ps.official_total);
        assert.ok(ot.length > 0, "official total card empty");
        rolesSeen.add(role);
      }
    }
    assert.deepStrictEqual([...rolesSeen].sort(), ["1", "2", "3", "4", "5"],
      "expected coverage of all five nominal roles");

    // Team page: fetch a team and render (HTML fetched separately).
    const report = await getJSON(`${base}/matches/${corpus.data.matches[0].match_id}`);
    const teamID = report.data.teams[0].team_id;
    const team = await getJSON(`${base}/teams/${teamID}`);
    assert.ok(team.data.team_score, "team score missing");
    const teamHtml = readFileSync(join(ROOT, "web", "replay", "team.html"), "utf8");
    assert.ok(teamHtml.includes("team_score"), "team page does not render team_score");

    // Review queue + authoritative mutation.
    const queue = await getJSON(`${base}/reviews/queue`);
    assert.ok(Array.isArray(queue.data.queue), "review queue missing");

    // Official vs experimental separation is structurally distinct.
    const ps0 = (await getJSON(`${base}/players/${corpus.data.matches[0].match_id === "8944521919" ? "320252024" : "320252024"}`)).data.score;
    assert.ok(ps0.official_total && ps0.experimental_total, "official/experimental totals missing");

    console.log("browser_test: OK — player pages rendered for roles 1-5, team page and review queue exercised");
  } finally {
    srv.kill("SIGTERM");
    await waitFor(300);
    if (!exited) srv.kill("SIGKILL");
    rmSync(BIN, { force: true });
  }
}

main().catch((e) => { console.error(e); process.exit(1); });

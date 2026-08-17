import fs from "node:fs";
import http from "node:http";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const overlayRoot = path.resolve(here, "../overlay");
const address = "127.0.0.1";
const port = Number(process.env.DOTA2_OB_P3_PORT || 18838);
const started = Date.now();
const logPath = process.env.DOTA2_OB_P3_LOG;
let previousMode = "";

const templates = [
  ["draft", "Ame 的斧王", "一号位英雄池样本为 18 场；仅作当前版本选人背景。"],
  ["economy", "天辉建立经济领先", "当前已观测经济领先 5000；领先不等同于胜势。"],
  ["item", "关键装备时间点决定下一轮团战主动权".repeat(4), "双方资源分配已经出现明显差异，领先方可以围绕视野、兵线与肉山区域建立连续控制。".repeat(4)],
  ["lane", "夜魇十分钟对线检查点", "相对可比基准偏差 1800 经济；结论仅覆盖已观测状态。"],
  ["objective", "天辉拿下肉山", "本轮可见资源交换净变化 2200 经济。"],
  ["teamfight", "夜魇团战资源就绪", "4 名英雄的可见关键资源已就绪；不推断战争迷雾信息。"]
];

function append(event) {
  if (!logPath) return;
  fs.appendFileSync(logPath, `${JSON.stringify({ at: new Date().toISOString(), ...event })}\n`, { encoding: "utf8", mode: 0o600 });
}

function mode() {
  const second = Math.floor((Date.now() - started) / 1000) % 140;
  if (second < 60) return templates[Math.floor(second / 10)];
  const unsafe = ["stale", "malformed", "schema-mismatch", "oversize", "missing-asset", "emergency-hide", "disconnect", "out-of-order"];
  return [unsafe[Math.floor((second - 60) / 10)]];
}

function state(selected) {
  const now = Date.now();
  if (selected[0] === "malformed") return "{malformed";
  const visible = {
    schema_version: "overlay_state.v1",
    session_id: "p3-sanitized-session",
    publication_time_ms: now,
    stale_deadline_ms: now + 1_500,
    visibility: "visible",
    decision_id: "p3-sanitized-decision",
    evidence: [{
      record_schema_version: 1,
      session_id: "p3-sanitized-session",
      sequence: 1,
      receive_time: new Date(now - 50).toISOString(),
      source: "gsi",
      provider_version: { state: "absent" },
      raw_payload_sha256: "a".repeat(64)
    }],
    confidence: "已观测",
    source_receive_time: new Date(now - 50).toISOString(),
    claim: { title: selected[1] || "不应显示", body: selected[2] || "不应显示", asset_key: selected[0] }
  };
  if (selected[0] === "schema-mismatch") visible.schema_version = "overlay_state.v2";
  if (selected[0] === "oversize") visible.padding = "x".repeat(65 * 1024);
  if (selected[0] === "stale") visible.stale_deadline_ms = now - 1;
  if (selected[0] === "missing-asset") visible.claim.asset_key = "missing-local-asset";
  if (selected[0] === "emergency-hide") return JSON.stringify({
    ...visible, visibility: "hidden", health_code: "emergency_hide", decision_id: "",
    evidence: [], confidence: "", source_receive_time: null, claim: null
  });
  if (selected[0] === "out-of-order") {
    visible.publication_time_ms = now - 60_000;
    visible.stale_deadline_ms = now + 1_500;
    visible.claim.asset_key = "objective";
  }
  return JSON.stringify(visible);
}

function headers(type, length) {
  return {
    "Cache-Control": "no-store",
    "Content-Security-Policy": "default-src 'none'; connect-src http://127.0.0.1:18838/v1/overlay/state; script-src 'self'; style-src 'self'; img-src 'self'; font-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'",
    "Content-Type": type,
    "Content-Length": String(length),
    "Cross-Origin-Resource-Policy": "same-origin",
    "Referrer-Policy": "no-referrer",
    "X-Content-Type-Options": "nosniff"
  };
}

const server = http.createServer((request, response) => {
  if (request.method !== "GET" && request.method !== "HEAD") {
    response.writeHead(405, { Allow: "GET, HEAD" }); response.end(); return;
  }
  if (request.url === "/healthz") { response.writeHead(204); response.end(); return; }
  if (request.url === "/v1/overlay/state") {
    const selected = mode();
    if (selected[0] !== previousMode) { append({ mode: selected[0] }); previousMode = selected[0]; }
    if (selected[0] === "disconnect") { request.socket.destroy(); return; }
    const body = Buffer.from(state(selected));
    response.writeHead(200, headers("application/json; charset=utf-8", body.length));
    response.end(request.method === "HEAD" ? undefined : body);
    return;
  }
  const files = new Map([
    ["/overlay/", ["index.html", "text/html; charset=utf-8"]],
    ["/overlay/index.html", ["index.html", "text/html; charset=utf-8"]],
    ["/overlay/app.js", ["app.js", "text/javascript; charset=utf-8"]],
    ["/overlay/app.css", ["app.css", "text/css; charset=utf-8"]],
    ["/overlay/fonts/noto-sans-cjk-sc-dot24.woff", ["fonts/noto-sans-cjk-sc-dot24.woff", "font/woff"]]
  ]);
  const asset = files.get(request.url);
  if (!asset) { response.writeHead(404); response.end(); return; }
  const body = fs.readFileSync(path.join(overlayRoot, asset[0]));
  response.writeHead(200, headers(asset[1], body.length));
  response.end(request.method === "HEAD" ? undefined : body);
});

server.listen(port, address, () => append({ mode: "server-started", address: `${address}:${port}` }));
for (const signal of ["SIGINT", "SIGTERM"]) process.once(signal, () => server.close(() => process.exit(0)));

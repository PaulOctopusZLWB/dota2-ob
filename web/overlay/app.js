(() => {
  "use strict";
  const body = document.body;
  const card = document.getElementById("overlay-card");
  const image = document.getElementById("asset-image");
  const fallback = document.getElementById("asset-fallback");
  const fields = {
    title: document.getElementById("title"),
    body: document.getElementById("body-copy"),
    evidence: document.getElementById("evidence"),
    source: document.getElementById("source"),
    confidence: document.getElementById("confidence"),
    age: document.getElementById("age")
  };
  const topKeys = ["schema_version","session_id","publication_time_ms","stale_deadline_ms","visibility","health_code","decision_id","evidence","confidence","source_receive_time","snapshot_id","claim"];
  const evidenceKeys = ["record_schema_version","session_id","sequence","receive_time","source","provider_version","raw_payload_sha256"];
  const observedKeys = ["state","value"];
  const claimKeys = ["title","body","asset_key"];
  const maxStateBytes = 64 * 1024;
  let nextRequestGeneration = 0;
  let latestSettledGeneration = 0;
  let lastPublication = 0;
  let staleDeadline = 0;

  function onlyKeys(value, allowed) {
    return value && typeof value === "object" && !Array.isArray(value) && Object.keys(value).every((key) => allowed.includes(key));
  }
  function plainText(value, maximum, emptyAllowed = false) {
    return typeof value === "string" && (emptyAllowed || value.length > 0) && [...value].length <= maximum && !/[<>\u0000-\u001f\u007f-\u009f\u061c\u200e\u200f\u202a-\u202e\u2066-\u2069]/u.test(value);
  }
  function validEvidence(entry, session) {
    const observed = entry?.provider_version;
    const present = observed?.state === "present";
    const absent = ["absent", "redacted", "unsupported", "invalid"].includes(observed?.state);
    return onlyKeys(entry, evidenceKeys) && evidenceKeys.every((key) => Object.hasOwn(entry, key)) &&
      Number.isInteger(entry.record_schema_version) && entry.record_schema_version > 0 && entry.session_id === session &&
      Number.isInteger(entry.sequence) && entry.sequence > 0 && !Number.isNaN(Date.parse(entry.receive_time)) &&
      plainText(entry.source, 64) && /^[a-f0-9]{64}$/.test(entry.raw_payload_sha256) &&
      onlyKeys(observed, observedKeys) && Object.hasOwn(observed, "state") &&
      ((present && Number.isSafeInteger(observed.value)) || (absent && !Object.hasOwn(observed, "value")));
  }
  function validState(state) {
    if (!onlyKeys(state, topKeys) || state.schema_version !== "overlay_state.v1" || !plainText(state.session_id, 128)) return false;
    if (!Number.isSafeInteger(state.publication_time_ms) || !Number.isSafeInteger(state.stale_deadline_ms) || state.stale_deadline_ms < state.publication_time_ms) return false;
    if (!Array.isArray(state.evidence) || !state.evidence.every((entry) => validEvidence(entry, state.session_id))) return false;
    for (let index = 0; index < state.evidence.length; index += 1) {
      if (index > 0 && state.evidence[index].sequence <= state.evidence[index - 1].sequence) return false;
    }
    if (state.snapshot_id && !/^[a-f0-9]{64}$/.test(state.snapshot_id)) return false;
    if (state.visibility === "hidden") return state.claim == null && plainText(state.health_code, 128);
    if (state.visibility !== "visible" || !plainText(state.decision_id, 128) || !plainText(state.confidence, 64) || Number.isNaN(Date.parse(state.source_receive_time))) return false;
    // The shared contract is wider than the accepted OBS safe-area envelope.
    // Presentation therefore fails closed above the proven 96/180 limits.
    return onlyKeys(state.claim, claimKeys) && plainText(state.claim.title, 96) && plainText(state.claim.body, 180) &&
      (state.claim.asset_key === "" || /^[a-z0-9]+(?:[._-][a-z0-9]+)*$/.test(state.claim.asset_key)) && state.evidence.length > 0;
  }
  function hide(reason) {
    body.dataset.renderState = "hidden";
    body.dataset.hideReason = reason;
    card.setAttribute("aria-hidden", "true");
    fields.title.textContent = "";
    fields.body.textContent = "";
    fields.evidence.textContent = "";
  }
  function render(state) {
    if (!validState(state)) { hide("invalid-state"); return; }
    if (state.publication_time_ms < lastPublication) { hide("out-of-order"); return; }
    lastPublication = state.publication_time_ms;
    staleDeadline = state.stale_deadline_ms;
    if (state.visibility !== "visible" || Date.now() > staleDeadline) { hide(state.health_code || "unsafe-state"); return; }
    fields.title.textContent = state.claim.title;
    fields.body.textContent = state.claim.body;
    fields.evidence.textContent = `${state.evidence.length} 条已提交证据`;
    const sources = [...new Set(state.evidence.map((item) => item.source))];
    fields.source.textContent = sources.slice(0, 3).join(" · ") + (sources.length > 3 ? " · …" : "");
    fields.confidence.textContent = state.confidence;
    fields.age.textContent = "实时";
    fallback.textContent = "析";
    image.classList.remove("loaded");
    image.removeAttribute("src");
    if (state.claim.asset_key) {
      image.onload = () => image.classList.add("loaded");
      image.onerror = () => image.classList.remove("loaded");
      image.src = `/overlay/assets/${state.claim.asset_key}.webp`;
    }
    body.dataset.renderState = "visible";
    body.dataset.hideReason = "none";
    card.setAttribute("aria-hidden", "false");
  }
  async function readBoundedState(response) {
    const declaredLength = Number(response.headers.get("Content-Length") || 0);
    if (declaredLength > maxStateBytes) throw new Error("oversized response");
    if (!response.body) throw new Error("missing response body");
    const reader = response.body.getReader();
    const chunks = [];
    let length = 0;
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      length += value.byteLength;
      if (length > maxStateBytes) {
        await reader.cancel("oversized response");
        throw new Error("oversized response");
      }
      chunks.push(value);
    }
    const bytes = new Uint8Array(length);
    let offset = 0;
    for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.byteLength; }
    return JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(bytes));
  }
  async function refresh() {
    const generation = ++nextRequestGeneration;
    try {
      const response = await fetch("/v1/overlay/state", { cache: "no-store", credentials: "omit" });
      if (!response.ok) throw new Error("unsafe response");
      const state = await readBoundedState(response);
      if (generation < latestSettledGeneration) return;
      latestSettledGeneration = generation;
      render(state);
    } catch {
      if (generation < latestSettledGeneration) return;
      latestSettledGeneration = generation;
      hide("disconnected");
    }
  }
  setInterval(() => {
    if (staleDeadline > 0 && Date.now() > staleDeadline) hide("connection-timeout");
  }, 50);
  setInterval(refresh, 250);
  refresh();
})();

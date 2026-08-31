(() => {
  "use strict";

  const allowedFixtures = new Set([
    "healthy",
    "draft-context",
    "lane-checkpoint",
    "item-timing-long",
    "objective-exchange",
    "teamfight-readiness",
    "missing-asset",
    "stale",
    "disconnected",
    "emergency-hide"
  ]);
  const allowedFamilies = new Set([
    "draft-context",
    "lane-checkpoint",
    "item-timing",
    "objective-exchange",
    "teamfight-readiness"
  ]);
  const topKeys = ["schemaVersion", "generatedAt", "health", "display"];
  const healthKeys = ["status", "renderSafe", "emergencyHidden", "maxSilenceMs"];
  const displayKeys = ["visible", "family", "kicker", "title", "body", "evidence", "confidenceLabel", "sourceLabel", "ageLabel", "asset"];
  const assetKeys = ["src", "alt", "fallback"];
  const fixture = new URLSearchParams(window.location.search).get("fixture") || "healthy";
  const body = document.body;
  const card = document.getElementById("overlay-card");
  const image = document.getElementById("asset-image");
  const fallback = document.getElementById("asset-fallback");
  const fields = {
    kicker: document.getElementById("kicker"),
    title: document.getElementById("title"),
    body: document.getElementById("body-copy"),
    evidence: document.getElementById("evidence"),
    confidenceLabel: document.getElementById("confidence"),
    sourceLabel: document.getElementById("source"),
    ageLabel: document.getElementById("age")
  };

  let lastSafeAt = null;
  let maxSilenceMs = 1250;
  let nextRequestGeneration = 0;
  let latestSettledGeneration = 0;

  function hasOnlyKeys(value, allowed) {
    return value && typeof value === "object" && !Array.isArray(value) &&
      Object.keys(value).every((key) => allowed.includes(key));
  }

  function isText(value, maximum) {
    return typeof value === "string" && value.length > 0 && value.length <= maximum && !/[<>]/.test(value);
  }

  function isValidState(state) {
    if (!hasOnlyKeys(state, topKeys) || !topKeys.every((key) => Object.hasOwn(state, key))) return false;
    if (state.schemaVersion !== "overlay-state/v1" || Number.isNaN(Date.parse(state.generatedAt))) return false;
    if (!hasOnlyKeys(state.health, healthKeys) || !healthKeys.every((key) => Object.hasOwn(state.health, key))) return false;
    if (!["healthy", "stale", "disconnected"].includes(state.health.status)) return false;
    if (typeof state.health.renderSafe !== "boolean" || typeof state.health.emergencyHidden !== "boolean") return false;
    if (!Number.isInteger(state.health.maxSilenceMs) || state.health.maxSilenceMs < 250 || state.health.maxSilenceMs >= 2000) return false;
    if (!hasOnlyKeys(state.display, displayKeys) || !displayKeys.every((key) => Object.hasOwn(state.display, key))) return false;
    if (typeof state.display.visible !== "boolean" || !allowedFamilies.has(state.display.family)) return false;
    for (const [key, maximum] of [["kicker", 40], ["title", 96], ["body", 180], ["evidence", 140], ["confidenceLabel", 32], ["sourceLabel", 64], ["ageLabel", 24]]) {
      if (!isText(state.display[key], maximum)) return false;
    }
    if (state.display.asset !== null) {
      if (!hasOnlyKeys(state.display.asset, assetKeys) || !assetKeys.every((key) => Object.hasOwn(state.display.asset, key))) return false;
      if (!/^\/assets\/[a-z0-9-]+\.(png|webp|svg)$/.test(state.display.asset.src)) return false;
      if (!isText(state.display.asset.alt, 48) || !isText(state.display.asset.fallback, 2)) return false;
    }
    return true;
  }

  function hide(reason) {
    body.dataset.renderState = "hidden";
    body.dataset.hideReason = reason;
    card.setAttribute("aria-hidden", "true");
  }

  function render(state) {
    const safe = state.health.status === "healthy" && state.health.renderSafe &&
      !state.health.emergencyHidden && state.display.visible;
    if (!safe) {
      hide(state.health.emergencyHidden ? "emergency-hide" : state.health.status);
      return;
    }

    for (const [key, element] of Object.entries(fields)) element.textContent = state.display[key];
    body.dataset.family = state.display.family;
    fallback.textContent = state.display.asset?.fallback || "析";
    image.classList.remove("loaded");
    image.removeAttribute("src");
    image.alt = "";
    if (state.display.asset) {
      image.alt = state.display.asset.alt;
      image.onload = () => image.classList.add("loaded");
      image.onerror = () => image.classList.remove("loaded");
      image.src = state.display.asset.src;
    }
    maxSilenceMs = state.health.maxSilenceMs;
    lastSafeAt = performance.now();
    body.dataset.renderState = "visible";
    body.dataset.hideReason = "none";
    card.setAttribute("aria-hidden", "false");
  }

  async function refresh() {
    const requestGeneration = ++nextRequestGeneration;
    if (!allowedFixtures.has(fixture)) {
      hide("invalid-fixture");
      return;
    }
    try {
      const response = await fetch(`/fixtures/${fixture}.json`, { cache: "no-store", credentials: "same-origin" });
      if (!response.ok) throw new Error(`fixture request failed: ${response.status}`);
      const state = await response.json();
      if (requestGeneration < latestSettledGeneration) return;
      latestSettledGeneration = requestGeneration;
      if (!isValidState(state)) {
        hide("invalid-state");
        return;
      }
      render(state);
    } catch {
      if (requestGeneration < latestSettledGeneration) return;
      latestSettledGeneration = requestGeneration;
      if (lastSafeAt === null) hide("disconnected");
    }
  }

  setInterval(() => {
    if (lastSafeAt !== null && performance.now() - lastSafeAt >= maxSilenceMs) hide("connection-timeout");
  }, 50);
  setInterval(refresh, 250);
  refresh();
})();

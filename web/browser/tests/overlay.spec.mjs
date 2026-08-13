import { expect, test } from "@playwright/test";

const canvasSizes = [
  { name: "1080p", width: 1920, height: 1080 },
  { name: "1440p", width: 2560, height: 1440 }
];

function visibleState(overrides = {}) {
  const now = Date.now();
  return {
    schema_version: "overlay_state.v1",
    session_id: "browser-session",
    publication_time_ms: now,
    stale_deadline_ms: now + 1500,
    visibility: "visible",
    decision_id: "decision-browser-1",
    evidence: [{
      record_schema_version: 1,
      session_id: "browser-session",
      sequence: 1,
      receive_time: new Date(now - 100).toISOString(),
      source: "gsi",
      provider_version: { state: "absent" },
      raw_payload_sha256: "a".repeat(64)
    }],
    confidence: "高置信度",
    source_receive_time: new Date(now - 100).toISOString(),
    claim: { title: "肉山窗口已经打开", body: "经济领先可转化为下一阶段的地图控制。", asset_key: "objective" },
    ...overrides
  };
}

async function routeState(page, factory = () => visibleState()) {
  await page.route("**/v1/overlay/state", (route) => route.fulfill({
    status: 200,
    contentType: "application/json",
    body: JSON.stringify(factory())
  }));
}

async function openVisible(page) {
  await page.goto("/overlay/");
  await expect(page.locator("body")).toHaveAttribute("data-render-state", "visible");
}

async function layout(page) {
  return page.locator("#overlay-card").evaluate((card) => {
    const rect = card.getBoundingClientRect();
    const header = card.querySelector(".card-header").getBoundingClientRect();
    const claimElement = card.querySelector(".claim");
    const claim = claimElement.getBoundingClientRect();
    const footer = card.querySelector("footer").getBoundingClientRect();
    return {
      inside: rect.left >= 0 && rect.top >= 0 && rect.right <= innerWidth && rect.bottom <= innerHeight,
      overflow: card.scrollWidth > card.clientWidth + 1 || card.scrollHeight > card.clientHeight + 1,
      ordered: header.bottom <= claim.top && claim.bottom <= footer.top,
      claimVisible: getComputedStyle(claimElement).visibility !== "hidden"
    };
  });
}

async function nativeViewportLayout(page) {
  return page.locator("#overlay-card").evaluate((card) => {
    const rect = card.getBoundingClientRect();
    const header = card.querySelector(".card-header").getBoundingClientRect();
    const claim = card.querySelector(".claim").getBoundingClientRect();
    const footer = card.querySelector("footer").getBoundingClientRect();
    const clearance = {
      left: rect.left,
      top: rect.top,
      right: innerWidth - rect.right,
      bottom: innerHeight - rect.bottom
    };
    return {
      clearance,
      inside: Object.values(clearance).every((value) => value >= 24),
      overflow: card.scrollWidth > card.clientWidth + 1 || card.scrollHeight > card.clientHeight + 1 ||
        document.documentElement.scrollWidth > document.documentElement.clientWidth + 1 ||
        document.documentElement.scrollHeight > document.documentElement.clientHeight + 1,
      ordered: header.bottom <= claim.top && claim.bottom <= footer.top
    };
  });
}

async function transparentFrame(page) {
  await page.waitForTimeout(250);
  return page.screenshot({ animations: "disabled", caret: "hide", omitBackground: true });
}

test("transparent OBS surface uses localhost resources only", async ({ page }) => {
  const remote = [];
  page.on("request", (request) => {
    if (new URL(request.url()).hostname !== "127.0.0.1") remote.push(request.url());
  });
  await routeState(page);
  await openVisible(page);
  expect(await page.evaluate(() => ({
    html: getComputedStyle(document.documentElement).backgroundColor,
    body: getComputedStyle(document.body).backgroundColor
  }))).toEqual({ html: "rgba(0, 0, 0, 0)", body: "rgba(0, 0, 0, 0)" });
  expect(remote).toEqual([]);
});

for (const canvas of canvasSizes) {
  test(`long Chinese copy remains inside the ${canvas.name} safe area`, async ({ page }, testInfo) => {
    await page.setViewportSize(canvas);
    await routeState(page, () => visibleState({
      claim: { title: "关键装备时间点决定下一轮团战主动权".repeat(4), body: "双方资源分配已经出现明显差异，领先方可以围绕视野、兵线与肉山区域建立连续控制。".repeat(4), asset_key: "item" }
    }));
    await openVisible(page);
    expect(await layout(page)).toEqual({ inside: true, overflow: false, ordered: true, claimVisible: true });
    const screenshot = await page.screenshot({ path: `./test-results/overlay-${canvas.name}.png`, animations: "disabled", omitBackground: true });
    await testInfo.attach(`overlay-${canvas.name}`, { body: screenshot, contentType: "image/png" });
  });
}

test("missing local asset key fails closed", async ({ page }) => {
  await routeState(page, () => visibleState({ claim: { title: "不可显示", body: "未知本地素材不能上屏。", asset_key: "missing-local-asset" } }));
  await page.goto("/overlay/");
  await expect(page.locator("body")).toHaveAttribute("data-render-state", "hidden");
  await expect(page.locator("#claim")).toBeHidden();
});

for (const unsafe of ["malformed", "stale", "hidden", "disconnected"]) {
  test(`${unsafe} state hides analytical claims`, async ({ page }) => {
    if (unsafe === "disconnected") {
      await page.route("**/v1/overlay/state", (route) => route.abort("connectionfailed"));
    } else {
      await routeState(page, () => {
        if (unsafe === "malformed") return { schema_version: "overlay_state.v1", raw_gsi: { player: "forbidden" } };
        if (unsafe === "stale") return visibleState({ stale_deadline_ms: Date.now() - 1 });
        return visibleState({ visibility: "hidden", health_code: "emergency_hide", claim: null, decision_id: "", evidence: [], confidence: "", source_receive_time: null });
      });
    }
    const started = Date.now();
    await page.goto("/overlay/");
    await expect(page.locator("body")).toHaveAttribute("data-render-state", "hidden", { timeout: 2000 });
    expect(Date.now() - started).toBeLessThan(2000);
    await expect(page.locator("#claim")).toBeHidden();
  });
}

test("presentation-oversized contract text fails closed before layout", async ({ page }) => {
  await routeState(page, () => visibleState({ claim: { title: "界".repeat(97), body: "正文", asset_key: "" } }));
  await page.goto("/overlay/");
  await expect(page.locator("body")).toHaveAttribute("data-render-state", "hidden");
  await expect(page.locator("#claim")).toBeHidden();
});

test("unknown nested state and unsafe asset keys fail closed", async ({ page }) => {
  await routeState(page, () => {
    const state = visibleState({ claim: { title: "标题", body: "正文", asset_key: "../../operator" } });
    state.evidence[0].provider_version = { state: "mystery" };
    return state;
  });
  await page.goto("/overlay/");
  await expect(page.locator("body")).toHaveAttribute("data-render-state", "hidden");
  await expect(page.locator("#claim")).toBeHidden();
});

test("oversized total response and unordered evidence fail closed", async ({ page }) => {
  await page.route("**/v1/overlay/state", (route) => route.fulfill({
    status: 200,
    contentType: "application/json",
    body: JSON.stringify({ ...visibleState(), padding: "x".repeat(65 * 1024) })
  }));
  await page.goto("/overlay/");
  await expect(page.locator("body")).toHaveAttribute("data-render-state", "hidden");
  await page.unroute("**/v1/overlay/state");
  await routeState(page, () => {
    const state = visibleState();
    state.evidence.push({ ...state.evidence[0], sequence: 1 });
    return state;
  });
  await page.reload();
  await expect(page.locator("body")).toHaveAttribute("data-render-state", "hidden");
});

test("valid response reconnects after connection loss", async ({ page }) => {
  let connected = true;
  await page.route("**/v1/overlay/state", (route) => connected ? route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(visibleState()) }) : route.abort("connectionfailed"));
  await openVisible(page);
  connected = false;
  await expect(page.locator("body")).toHaveAttribute("data-render-state", "hidden", { timeout: 2000 });
  connected = true;
  await expect(page.locator("body")).toHaveAttribute("data-render-state", "visible", { timeout: 2000 });
});

test("freshness-only publications do not rewrite an unchanged visible claim", async ({ page }) => {
  await page.addInitScript(() => {
    window.claimMutationCount = 0;
    addEventListener("DOMContentLoaded", () => {
      new MutationObserver((records) => { window.claimMutationCount += records.length; })
        .observe(document.getElementById("claim"), { subtree: true, childList: true, characterData: true, attributes: true });
    });
  });
  await routeState(page, () => visibleState());
  await openVisible(page);
  await page.waitForTimeout(100);
  const afterInitialRender = await page.evaluate(() => window.claimMutationCount);
  await page.waitForTimeout(1700);
  expect(await page.evaluate(() => window.claimMutationCount)).toBe(afterInitialRender);
  await expect(page.locator("body")).toHaveAttribute("data-render-state", "visible");
});

test("hostile null-origin page cannot invoke operator commands", async ({ page }) => {
  await page.goto("data:text/html,<title>hostile</title>");
  const result = await page.evaluate(async () => {
    try {
      await fetch("http://127.0.0.1:18838/v1/operator/commands", {
        method: "POST",
        headers: {
          "Authorization": "Bearer guessed-token",
          "Content-Type": "application/json",
          "X-Dota2-OB-CSRF": "operator-command"
        },
        body: "{}"
      });
      return "unexpected-success";
    } catch (error) {
      return error.name;
    }
  });
  expect(result).toBe("TypeError");
});

test("real capture and delivery listeners do not proxy each other's routes", async ({ request }) => {
  expect((await request.get("http://127.0.0.1:18839/v1/overlay/state")).status()).toBe(404);
  expect((await request.get("http://127.0.0.1:18839/api/latest")).status()).toBe(404);
  expect((await request.post("http://127.0.0.1:18838/gsi", { data: {} })).status()).toBe(404);
  expect((await request.get("http://127.0.0.1:18838/api/status")).status()).toBe(404);
});

test("older delayed response cannot revive a newer unsafe state", async ({ page }) => {
  const healthy = JSON.stringify(visibleState());
  const unsafe = JSON.stringify({ schema_version: "overlay_state.v1", raw_gsi: { forbidden: true } });
  await page.addInitScript(({ healthy, unsafe }) => {
    const response = (body) => new Response(body, { status: 200, headers: { "Content-Type": "application/json" } });
    let release;
    window.testPolls = 0;
    window.releaseOld = () => release();
    window.fetch = async () => {
      window.testPolls += 1;
      if (window.testPolls === 1) return response(healthy);
      if (window.testPolls === 2) return new Promise((resolve) => { release = () => resolve(response(healthy)); });
      if (window.testPolls === 3) return response(unsafe);
      return new Promise(() => {});
    };
  }, { healthy, unsafe });
  await openVisible(page);
  await expect.poll(() => page.evaluate(() => window.testPolls)).toBeGreaterThanOrEqual(3);
  await expect(page.locator("body")).toHaveAttribute("data-render-state", "hidden");
  await page.evaluate(() => window.releaseOld());
  await page.waitForTimeout(350);
  await expect(page.locator("body")).toHaveAttribute("data-render-state", "hidden");
});

const templateStates = [
  ["draft", "Ame 的斧王", "一号位英雄池样本为 18 场；仅作当前版本选人背景。"],
  ["economy", "天辉建立经济领先", "当前已观测经济领先 5000；领先不等同于胜势。"],
  ["item", "Ame 的闪烁匕首", "关键装备比基准提前 75 秒，下一轮资源交换值得关注。"],
  ["lane", "夜魇十分钟对线检查点", "相对可比基准偏差 1800 经济；结论仅覆盖已观测状态。"],
  ["objective", "天辉拿下肉山", "本轮可见资源交换净变化 2200 经济。"],
  ["teamfight", "夜魇团战资源就绪", "4 名英雄的可见关键资源已就绪；不推断战争迷雾信息。"]
];

const longestZhCN = [
  "item",
  "关键装备时间点决定下一轮团战主动权".repeat(4),
  "双方资源分配已经出现明显差异，领先方可以围绕视野、兵线与肉山区域建立连续控制。".repeat(4)
];

for (const [family, title, body] of [...templateStates, longestZhCN]) {
  test(`native 750x640 viewport contains ${family === "item" && title === longestZhCN[1] ? "longest zh-CN fixture" : `${family} template`}`, async ({ page }) => {
    await page.setViewportSize({ width: 750, height: 640 });
    await routeState(page, () => visibleState({ claim: { title, body, asset_key: family } }));
    await openVisible(page);
    await page.waitForTimeout(250);
    const geometry = await nativeViewportLayout(page);
    expect(geometry.inside, JSON.stringify(geometry.clearance)).toBe(true);
    expect(geometry.overflow).toBe(false);
    expect(geometry.ordered).toBe(true);
  });
}

for (const unsafe of ["malformed", "schema-mismatch", "oversize", "stale", "emergency-hide", "missing-asset", "disconnect", "out-of-order"]) {
  test(`native 750x640 ${unsafe} state settles to an empty complete-source frame`, async ({ page, context }) => {
    await page.setViewportSize({ width: 750, height: 640 });
    if (unsafe === "malformed") {
      await page.route("**/v1/overlay/state", (route) => route.fulfill({ status: 200, contentType: "application/json", body: "{malformed" }));
    } else if (unsafe === "disconnect") {
      await page.route("**/v1/overlay/state", (route) => route.abort("connectionfailed"));
    } else if (unsafe === "oversize") {
      await page.route("**/v1/overlay/state", (route) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ ...visibleState(), padding: "x".repeat(65 * 1024) }) }));
    } else if (unsafe === "out-of-order") {
      const healthy = JSON.stringify(visibleState());
      const older = JSON.stringify(visibleState({ publication_time_ms: Date.now() - 10_000, stale_deadline_ms: Date.now() + 5_000 }));
      await page.addInitScript(({ healthy, older }) => {
        const response = (body) => new Response(body, { status: 200, headers: { "Content-Type": "application/json" } });
        let polls = 0;
        window.fetch = async () => response(polls++ === 0 ? healthy : older);
      }, { healthy, older });
    } else {
      await routeState(page, () => {
        if (unsafe === "schema-mismatch") return visibleState({ schema_version: "overlay_state.v2" });
        if (unsafe === "stale") return visibleState({ stale_deadline_ms: Date.now() - 1 });
        if (unsafe === "emergency-hide") return visibleState({ visibility: "hidden", health_code: "emergency_hide", claim: null, decision_id: "", evidence: [], confidence: "", source_receive_time: null });
        return visibleState({ claim: { title: "素材缺失", body: "必须隐藏。", asset_key: "missing-local-asset" } });
      });
    }
    await page.goto("/overlay/");
    if (unsafe === "out-of-order") await expect(page.locator("body")).toHaveAttribute("data-render-state", "visible");
    await expect(page.locator("body")).toHaveAttribute("data-render-state", "hidden", { timeout: 2000 });
    const actual = await transparentFrame(page);
    const blank = await context.newPage();
    await blank.setViewportSize({ width: 750, height: 640 });
    await blank.setContent("<!doctype html><style>html,body{margin:0;background:transparent}</style>");
    const expected = await transparentFrame(blank);
    await blank.close();
    expect(actual.equals(expected)).toBe(true);
    await expect(page.locator("#claim")).toBeHidden();
  });
}

for (const canvas of canvasSizes) {
  for (const [family, title, body] of templateStates) {
    test(`${family} template matches the ${canvas.name} visual baseline`, async ({ page }) => {
      await page.setViewportSize(canvas);
      await routeState(page, () => visibleState({ claim: { title, body, asset_key: family } }));
      await openVisible(page);
      expect(await layout(page)).toEqual({ inside: true, overflow: false, ordered: true, claimVisible: true });
      await expect(page.locator("body")).toHaveAttribute("data-family", family);
      await expect(page).toHaveScreenshot(`overlay-${family}-${canvas.name}.png`, { animations: "disabled", caret: "hide", omitBackground: true });
    });
  }
}

for (const canvas of canvasSizes) {
  for (const unsafe of ["malformed", "stale", "disconnected", "emergency", "out-of-order", "missing-asset"]) {
    test(`${unsafe} fail-closed frame is empty at ${canvas.name}`, async ({ page }) => {
      await page.setViewportSize(canvas);
      if (unsafe === "disconnected") {
        await page.route("**/v1/overlay/state", (route) => route.abort("connectionfailed"));
      } else if (unsafe === "malformed") {
        await routeState(page, () => ({ schema_version: "overlay_state.v1", raw_gsi: {} }));
      } else if (unsafe === "stale") {
        await routeState(page, () => visibleState({ stale_deadline_ms: Date.now() - 1 }));
      } else if (unsafe === "emergency") {
        await routeState(page, () => visibleState({ visibility: "hidden", health_code: "emergency_hide", claim: null, decision_id: "", evidence: [], confidence: "", source_receive_time: null }));
      } else if (unsafe === "out-of-order") {
        let calls = 0;
        await routeState(page, () => visibleState({ publication_time_ms: calls++ === 0 ? Date.now() : Date.now() - 1_000 }));
      } else {
        await routeState(page, () => visibleState({ claim: { title: "素材缺失", body: "必须隐藏。", asset_key: "not-in-catalog" } }));
      }
      await page.goto("/overlay/");
      if (unsafe === "out-of-order") await page.waitForTimeout(350);
      await expect(page.locator("body")).toHaveAttribute("data-render-state", "hidden", { timeout: 2000 });
      await expect(page).toHaveScreenshot(`overlay-hidden-${unsafe}-${canvas.name}.png`, { animations: "disabled", caret: "hide", omitBackground: true });
    });
  }
}

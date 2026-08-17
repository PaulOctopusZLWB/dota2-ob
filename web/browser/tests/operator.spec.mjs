import { expect, test } from "@playwright/test";

function operatorState(overrides = {}) {
  return {
    schema_version: "operator_state.v1",
    session_id: "session-browser",
    policy_revision: 7,
    emergency_hidden: false,
    previews: [{
      candidate_id: "candidate-lane-1",
      rule_id: "lane-checkpoint",
      rule_version: "lane-checkpoint.v1",
      confidence: "已观测",
      sample_size: 18,
      expires_at_ms: 1_800_000_000_000,
      pinned: false,
      claim: { title: "十分钟对线检查点", body: "天辉相对基准领先 1800 经济。", asset_key: "lane" }
    }],
    ...overrides
  };
}

async function connect(page) {
  await page.goto("/operator/");
  await page.getByLabel("本次启动令牌").fill("local-test-token");
  await page.getByRole("button", { name: "连接本地会话" }).click();
  await expect(page.locator("body")).toHaveAttribute("data-connection", "online");
}

function acceptedResult(command, resultingRevision = command.expected_policy_revision + 1) {
  return {
    schema_version: "operator_command_result.v1",
    command_id: command.command_id,
    session_id: command.session_id,
    status: "accepted",
    previous_revision: command.expected_policy_revision,
    resulting_revision: resultingRevision,
    decision_ids: [`decision-${resultingRevision}`],
    reason: "operator_accepted"
  };
}

async function routeAcceptedCommands(page, requests) {
  await page.route("**/v1/operator/commands", async (route) => {
    const command = JSON.parse(route.request().postData());
    requests.push(command);
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(acceptedResult(command)) });
  });
}

test("operator previews localized candidates and submits revision-checked commands", async ({ page }) => {
  const requests = [];
  let stateReads = 0;
  await page.route("**/v1/operator/state", async (route) => {
    expect(route.request().headers().authorization).toBe("Bearer local-test-token");
    const state = stateReads++ === 0 ? operatorState() : operatorState({ policy_revision: 8, previews: [] });
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(state) });
  });
  await page.route("**/v1/operator/commands", async (route) => {
    requests.push(JSON.parse(route.request().postData()));
    expect(route.request().headers()["x-dota2-ob-csrf"]).toBe("operator-command");
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        schema_version: "operator_command_result.v1",
        command_id: requests.at(-1).command_id,
        session_id: "session-browser",
        status: "accepted",
        previous_revision: 7,
        resulting_revision: 8,
        decision_ids: ["decision-8"],
        reason: "operator_approved"
      })
    });
  });

  await page.goto("/operator/");
  await page.getByLabel("本次启动令牌").fill("local-test-token");
  await page.getByRole("button", { name: "连接本地会话" }).click();
  await expect(page.getByText("十分钟对线检查点")).toBeVisible();
  await expect(page.locator("#policy-revision")).toHaveText("7");
  await page.getByRole("button", { name: "批准上屏" }).click();

  await expect.poll(() => requests.length).toBe(1);
  expect(requests[0]).toMatchObject({
    schema_version: "operator_command.v1",
    session_id: "session-browser",
    action: "approve",
    target_candidate_id: "candidate-lane-1",
    expected_policy_revision: 7
  });
  expect(requests[0].command_id).toMatch(/^[0-9a-f-]{36}$/);
  expect(Number.isSafeInteger(requests[0].policy_time_ms)).toBe(true);
  await expect(page.locator("#policy-revision")).toHaveText("8");
  await expect(page.locator("#candidate-count")).toHaveText("0");
  expect(stateReads).toBe(2);
  expect(await page.evaluate(() => ({ local: localStorage.length, session: sessionStorage.length, cookie: document.cookie }))).toEqual({ local: 0, session: 0, cookie: "" });
});

test("accepted emergency hide and pin actions use refreshed authoritative inverse state", async ({ page }) => {
  const requests = [];
  const states = [
    operatorState(),
    operatorState({ policy_revision: 8, emergency_hidden: true }),
    operatorState({ policy_revision: 9, emergency_hidden: false }),
    operatorState({ policy_revision: 10, previews: [{ ...operatorState().previews[0], pinned: true }] })
  ];
  states.push(operatorState({ policy_revision: 11 }));
  let stateReads = 0;
  await page.route("**/v1/operator/state", (route) => route.fulfill({
    status: 200,
    contentType: "application/json",
    body: JSON.stringify(states[stateReads++])
  }));
  await routeAcceptedCommands(page, requests);
  await connect(page);

  await page.getByRole("button", { name: "紧急隐藏全部分析" }).click();
  await expect(page.getByRole("button", { name: "解除紧急隐藏" })).toBeEnabled();
  await page.getByRole("button", { name: "解除紧急隐藏" }).click();
  await expect(page.getByRole("button", { name: "紧急隐藏全部分析" })).toBeEnabled();
  await page.getByRole("button", { name: "置顶" }).click();
  await expect(page.getByRole("button", { name: "取消置顶" })).toBeEnabled();
  await page.getByRole("button", { name: "取消置顶" }).click();
  await expect(page.getByRole("button", { name: "置顶" })).toBeEnabled();

  expect(requests.map(({ action, expected_policy_revision }) => ({ action, expected_policy_revision }))).toEqual([
    { action: "emergency_hide", expected_policy_revision: 7 },
    { action: "clear_emergency_hide", expected_policy_revision: 8 },
    { action: "pin", expected_policy_revision: 9 },
    { action: "unpin", expected_policy_revision: 10 }
  ]);
  expect(stateReads).toBe(5);
});

for (const action of ["approve", "reject"]) {
  test(`accepted ${action} removes the candidate only after authoritative refresh`, async ({ page }) => {
    const requests = [];
    let stateReads = 0;
    await page.route("**/v1/operator/state", (route) => route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(stateReads++ === 0 ? operatorState() : operatorState({ policy_revision: 8, previews: [] }))
    }));
    await routeAcceptedCommands(page, requests);
    await connect(page);

    await page.getByRole("button", { name: action === "approve" ? "批准上屏" : "拒绝" }).click();
    await expect(page.locator("#candidate-count")).toHaveText("0");
    await expect(page.locator(".candidate-card")).toHaveCount(0);
    expect(requests[0].action).toBe(action);
    expect(stateReads).toBe(2);
  });
}

test("revision conflicts refresh authoritative state and replay the identical rejected result", async ({ page }) => {
  const requests = [];
  const responses = [];
  const committedResults = new Map();
  let stateReads = 0;
  const states = [
    operatorState(),
    operatorState({ policy_revision: 8 }),
    operatorState({ policy_revision: 8 }),
    operatorState({ policy_revision: 9, emergency_hidden: true })
  ];
  await page.route("**/v1/operator/state", (route) => route.fulfill({
    status: 200,
    contentType: "application/json",
    body: JSON.stringify(states[stateReads++])
  }));
  await page.route("**/v1/operator/commands", async (route) => {
    const command = JSON.parse(route.request().postData());
    requests.push(command);
    if (!committedResults.has(command.command_id)) {
      committedResults.set(command.command_id, command.expected_policy_revision === 7 ? {
        schema_version: "operator_command_result.v1",
        command_id: command.command_id,
        session_id: command.session_id,
        status: "rejected",
        previous_revision: 8,
        resulting_revision: 8,
        decision_ids: [],
        reason: "stale_revision"
      } : acceptedResult(command));
    }
    const result = committedResults.get(command.command_id);
    responses.push(structuredClone(result));
    await route.fulfill({
      status: result.status === "accepted" ? 200 : 409,
      contentType: "application/json",
      body: JSON.stringify(result)
    });
  });
  await connect(page);

  await page.getByRole("button", { name: "拒绝" }).click();
  await expect(page.locator("#policy-revision")).toHaveText("8");
  await expect(page.getByRole("button", { name: "拒绝" })).toBeEnabled();
  expect(stateReads).toBe(2);
  await page.getByRole("button", { name: "重试同一命令" }).click();
  await expect.poll(() => stateReads).toBe(3);
  expect(requests[1]).toEqual(requests[0]);
  expect(responses[1]).toEqual(responses[0]);

  await page.getByRole("button", { name: "紧急隐藏全部分析" }).click();
  await expect(page.getByRole("button", { name: "解除紧急隐藏" })).toBeEnabled();
  expect(requests[2].command_id).not.toBe(requests[0].command_id);
  expect(requests[2]).toMatchObject({ action: "emergency_hide", expected_policy_revision: 8 });
  expect(stateReads).toBe(4);
});

test("revision-conflict refresh failure clears stale state and leaves controls fail closed", async ({ page }) => {
  let stateReads = 0;
  await page.route("**/v1/operator/state", (route) => {
    stateReads++;
    return stateReads === 1
      ? route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(operatorState()) })
      : route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ reason: "state_unavailable" }) });
  });
  await page.route("**/v1/operator/commands", async (route) => {
    const command = JSON.parse(route.request().postData());
    await route.fulfill({ status: 409, contentType: "application/json", body: JSON.stringify({
      schema_version: "operator_command_result.v1",
      command_id: command.command_id,
      session_id: command.session_id,
      status: "rejected",
      previous_revision: 8,
      resulting_revision: 8,
      decision_ids: [],
      reason: "stale_revision"
    }) });
  });
  await connect(page);

  await page.getByRole("button", { name: "拒绝" }).click();
  await expect(page.locator("body")).toHaveAttribute("data-connection", "offline");
  await expect(page.locator(".candidate-card")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "紧急隐藏全部分析" })).toBeDisabled();
  await expect(page.getByRole("button", { name: "重试同一命令" })).toBeDisabled();
  expect(stateReads).toBe(2);
});

for (const failure of ["unavailable", "invalid", "wrong-session"]) {
  test(`accepted-command ${failure} refresh disables every policy control and clears stale candidates`, async ({ page }) => {
    let stateReads = 0;
    await page.route("**/v1/operator/state", (route) => {
      stateReads++;
      if (stateReads === 1) {
        return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(operatorState()) });
      }
      if (failure === "unavailable") {
        return route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ status: "rejected" }) });
      }
      const state = failure === "invalid" ? operatorState({ unknown: true }) : operatorState({ session_id: "other-session", policy_revision: 8 });
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(state) });
    });
    await routeAcceptedCommands(page, []);
    await connect(page);

    await page.getByRole("button", { name: "批准上屏" }).click();
    await expect(page.locator("body")).toHaveAttribute("data-connection", "offline");
    await expect(page.locator(".candidate-card")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "紧急隐藏全部分析" })).toBeDisabled();
    await expect(page.getByRole("button", { name: "停用规则" })).toBeDisabled();
    await expect(page.getByRole("button", { name: "启用规则" })).toBeDisabled();
    await expect(page.getByRole("button", { name: "重试同一命令" })).toBeDisabled();
  });
}

test("an accepted duplicate response refreshes state without synthesizing a second mutation", async ({ page }) => {
  const requests = [];
  let stateReads = 0;
  await page.route("**/v1/operator/state", (route) => route.fulfill({
    status: 200,
    contentType: "application/json",
    body: JSON.stringify(stateReads++ === 0 ? operatorState() : operatorState({ policy_revision: 8, emergency_hidden: true }))
  }));
  let committedResult;
  await page.route("**/v1/operator/commands", async (route) => {
    const command = JSON.parse(route.request().postData());
    requests.push(command);
    committedResult ||= acceptedResult(command);
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(committedResult) });
  });
  await connect(page);

  await page.getByRole("button", { name: "紧急隐藏全部分析" }).click();
  await expect(page.getByRole("button", { name: "解除紧急隐藏" })).toBeEnabled();
  await page.getByRole("button", { name: "重试同一命令" }).click();
  await expect(page.getByRole("button", { name: "解除紧急隐藏" })).toBeEnabled();

  expect(requests[1]).toEqual(requests[0]);
  expect(stateReads).toBe(3);
});

test("an older delayed refresh cannot revive the console after a newer failed reconnect", async ({ page }) => {
  let stateReads = 0;
  let releaseDelayed;
  const delayed = new Promise((resolve) => { releaseDelayed = resolve; });
  await page.route("**/v1/operator/state", async (route) => {
    stateReads++;
    if (stateReads === 1) return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(operatorState()) });
    if (stateReads === 2) {
      await delayed;
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(operatorState({ policy_revision: 8, previews: [] })) });
    }
    return route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ status: "rejected", reason: "operator_state_unavailable" }) });
  });
  await routeAcceptedCommands(page, []);
  await connect(page);
  await page.getByRole("button", { name: "批准上屏" }).click();
  await expect.poll(() => stateReads).toBe(2);

  await page.getByRole("button", { name: "连接本地会话" }).click();
  await expect(page.locator("body")).toHaveAttribute("data-connection", "offline");
  releaseDelayed();
  await page.waitForTimeout(100);
  await expect(page.locator("body")).toHaveAttribute("data-connection", "offline");
  await expect(page.locator(".candidate-card")).toHaveCount(0);
});

test("operator exposes every accepted action and replays the exact prior command id", async ({ page }) => {
  const requests = [];
  await page.route("**/v1/operator/state", (route) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(operatorState({ emergency_hidden: true })) }));
  await page.route("**/v1/operator/commands", async (route) => {
    const command = JSON.parse(route.request().postData());
    requests.push(command);
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({
      schema_version: "operator_command_result.v1", command_id: command.command_id, session_id: command.session_id,
      status: "rejected", previous_revision: 7, resulting_revision: 7, decision_ids: [], reason: "stale_revision"
    }) });
  });
  await page.goto("/operator/");
  await page.getByLabel("本次启动令牌").fill("local-test-token");
  await page.getByRole("button", { name: "连接本地会话" }).click();

  for (const label of ["批准上屏", "立即上屏", "拒绝", "置顶"]) {
    await expect(page.getByRole("button", { name: label })).toBeVisible();
  }
  await expect(page.getByRole("button", { name: "解除紧急隐藏" })).toBeVisible();
  await page.getByRole("button", { name: "拒绝" }).click();
  await expect.poll(() => requests.length).toBe(1);
  await page.getByRole("button", { name: "重试同一命令" }).click();
  await expect.poll(() => requests.length).toBe(2);
  expect(requests[1]).toEqual(requests[0]);

  await page.getByLabel("规则标识").fill("lane-checkpoint");
  await expect(page.getByRole("button", { name: "停用规则" })).toBeVisible();
  await expect(page.getByRole("button", { name: "启用规则" })).toBeVisible();
});

test("operator desk matches the 1440p visual baseline without clipping", async ({ page }) => {
  await page.setViewportSize({ width: 2560, height: 1440 });
  await page.route("**/v1/operator/state", (route) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(operatorState()) }));
  await page.goto("/operator/");
  await page.getByLabel("本次启动令牌").fill("visual-token");
  await page.getByRole("button", { name: "连接本地会话" }).click();
  const geometry = await page.locator(".console-shell").evaluate((shell) => ({
    inside: shell.getBoundingClientRect().left >= 0 && shell.getBoundingClientRect().right <= innerWidth,
    overflow: document.documentElement.scrollWidth > innerWidth + 1,
    cards: [...document.querySelectorAll(".candidate-card")].every((card) => card.scrollWidth <= card.clientWidth + 1 && card.scrollHeight <= card.clientHeight + 1)
  }));
  expect(geometry).toEqual({ inside: true, overflow: false, cards: true });
  await expect(page).toHaveScreenshot("operator-desk-1440p.png", { animations: "disabled", caret: "hide" });
});

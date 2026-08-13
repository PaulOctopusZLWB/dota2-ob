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

test("operator previews localized candidates and submits revision-checked commands", async ({ page }) => {
  const requests = [];
  await page.route("**/v1/operator/state", async (route) => {
    expect(route.request().headers().authorization).toBe("Bearer local-test-token");
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(operatorState()) });
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
  expect(await page.evaluate(() => ({ local: localStorage.length, session: sessionStorage.length, cookie: document.cookie }))).toEqual({ local: 0, session: 0, cookie: "" });
});

test("operator exposes every accepted action and replays the exact prior command id", async ({ page }) => {
  const requests = [];
  await page.route("**/v1/operator/state", (route) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(operatorState({ emergency_hidden: true })) }));
  await page.route("**/v1/operator/commands", async (route) => {
    const command = JSON.parse(route.request().postData());
    requests.push(command);
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({
      schema_version: "operator_command_result.v1", command_id: command.command_id, session_id: command.session_id,
      status: "rejected", previous_revision: 7, resulting_revision: 7, decision_ids: [], reason: "revision_conflict"
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

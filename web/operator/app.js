(() => {
  "use strict";

  const connectForm = document.getElementById("connect-form");
  const token = document.getElementById("token");
  const sessionID = document.getElementById("session-id");
  const revision = document.getElementById("policy-revision");
  const count = document.getElementById("candidate-count");
  const list = document.getElementById("candidate-list");
  const template = document.getElementById("candidate-template");
  const emergency = document.getElementById("emergency-toggle");
  const ruleForm = document.getElementById("rule-form");
  const ruleID = document.getElementById("rule-id");
  const ruleButtons = [...ruleForm.querySelectorAll("button[data-rule-action]")];
  const result = document.getElementById("result");
  const retry = document.getElementById("retry-command");
  const stateKeys = ["schema_version", "session_id", "policy_revision", "emergency_hidden", "previews"];
  const previewKeys = ["candidate_id", "rule_id", "rule_version", "confidence", "sample_size", "expires_at_ms", "pinned", "claim"];
  const claimKeys = ["title", "body", "asset_key"];
  let currentState = null;
  let lastCommand = null;

  function onlyKeys(value, allowed) {
    return value && typeof value === "object" && !Array.isArray(value) && Object.keys(value).every((key) => allowed.includes(key));
  }

  function plainText(value, maximum, emptyAllowed = false) {
    return typeof value === "string" && (emptyAllowed || value.length > 0) && [...value].length <= maximum && !/[<>\u0000-\u001f\u007f-\u009f\u061c\u200e\u200f\u202a-\u202e\u2066-\u2069]/u.test(value);
  }

  function validPreview(preview) {
    return onlyKeys(preview, previewKeys) && previewKeys.every((key) => Object.hasOwn(preview, key)) &&
      plainText(preview.candidate_id, 128) && plainText(preview.rule_id, 128) && plainText(preview.rule_version, 128) &&
      plainText(preview.confidence, 64) && Number.isSafeInteger(preview.sample_size) && preview.sample_size >= 0 &&
      Number.isSafeInteger(preview.expires_at_ms) && typeof preview.pinned === "boolean" &&
      onlyKeys(preview.claim, claimKeys) && plainText(preview.claim.title, 160) && plainText(preview.claim.body, 1024, true) &&
      (preview.claim.asset_key === "" || /^[a-z0-9]+(?:[._-][a-z0-9]+)*$/.test(preview.claim.asset_key));
  }

  function validState(state) {
    if (!onlyKeys(state, stateKeys) || !stateKeys.every((key) => Object.hasOwn(state, key)) || state.schema_version !== "operator_state.v1" ||
        !plainText(state.session_id, 128) || !Number.isSafeInteger(state.policy_revision) || typeof state.emergency_hidden !== "boolean" ||
        !Array.isArray(state.previews) || state.previews.length > 64 || !state.previews.every(validPreview)) return false;
    return state.previews.every((preview, index) => index === 0 || preview.candidate_id > state.previews[index - 1].candidate_id);
  }

  function setControls(enabled) {
    emergency.disabled = !enabled;
    ruleButtons.forEach((button) => { button.disabled = !enabled; });
  }

  function showResult(value, state) {
    result.dataset.state = state;
    result.textContent = typeof value === "string" ? value : JSON.stringify(value, null, 2);
  }

  function renderState(state) {
    if (!validState(state)) throw new Error("invalid operator state");
    currentState = state;
    document.body.dataset.connection = "online";
    document.body.dataset.emergency = String(state.emergency_hidden);
    sessionID.textContent = state.session_id;
    revision.textContent = String(state.policy_revision);
    count.textContent = String(state.previews.length);
    emergency.textContent = state.emergency_hidden ? "解除紧急隐藏" : "紧急隐藏全部分析";
    list.replaceChildren();
    if (state.previews.length === 0) {
      const empty = document.createElement("div");
      empty.className = "empty-state";
      const mark = document.createElement("span");
      mark.textContent = "清空";
      const copy = document.createElement("p");
      copy.textContent = "当前没有等待导播判断的候选。";
      empty.append(mark, copy);
      list.append(empty);
    }
    for (const preview of state.previews) {
      const card = template.content.firstElementChild.cloneNode(true);
      card.dataset.candidateId = preview.candidate_id;
      card.querySelector(".rule-name").textContent = `${preview.rule_id} · ${preview.rule_version}`;
      card.querySelector(".confidence").textContent = preview.confidence;
      card.querySelector(".sample").textContent = `样本 ${preview.sample_size}`;
      card.querySelector(".claim-title").textContent = preview.claim.title;
      card.querySelector(".claim-body").textContent = preview.claim.body;
      card.querySelector(".expiry").textContent = `失效时间 ${new Date(preview.expires_at_ms).toLocaleTimeString("zh-CN", { hour12: false })}`;
      const pin = card.querySelector(".pin");
      pin.dataset.action = preview.pinned ? "unpin" : "pin";
      pin.textContent = preview.pinned ? "取消置顶" : "置顶";
      list.append(card);
    }
    setControls(true);
  }

  async function readJSON(response) {
    const text = await response.text();
    if (text.length > 64 * 1024) throw new Error("oversized response");
    return JSON.parse(text);
  }

  async function connect() {
    const response = await fetch("/v1/operator/state", {
      method: "GET",
      cache: "no-store",
      credentials: "omit",
      headers: { "Authorization": `Bearer ${token.value}` }
    });
    if (!response.ok) throw new Error("operator state unavailable");
    renderState(await readJSON(response));
    showResult("本地策略会话已连接。", "accepted");
  }

  function buildCommand(action, candidate = "", rule = "") {
    if (!currentState) throw new Error("operator state unavailable");
    const command = {
      schema_version: "operator_command.v1",
      command_id: crypto.randomUUID(),
      session_id: currentState.session_id,
      action,
      expected_policy_revision: currentState.policy_revision,
      policy_time_ms: Date.now()
    };
    if (candidate) command.target_candidate_id = candidate;
    if (rule) command.target_rule_id = rule;
    return command;
  }

  async function submitCommand(command, remember = true) {
    showResult("正在提交并等待持久化审计结果…", "pending");
    const response = await fetch("/v1/operator/commands", {
      method: "POST",
      cache: "no-store",
      credentials: "omit",
      headers: {
        "Authorization": `Bearer ${token.value}`,
        "Content-Type": "application/json",
        "X-Dota2-OB-CSRF": "operator-command"
      },
      body: JSON.stringify(command)
    });
    const body = await readJSON(response);
    if (remember) {
      lastCommand = command;
      retry.disabled = false;
    }
    showResult(body, response.ok && body.status === "accepted" ? "accepted" : "rejected");
    if (response.ok && body.status === "accepted" && Number.isSafeInteger(body.resulting_revision)) {
      currentState.policy_revision = body.resulting_revision;
      revision.textContent = String(body.resulting_revision);
    }
  }

  connectForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    setControls(false);
    try {
      await connect();
    } catch {
      currentState = null;
      document.body.dataset.connection = "offline";
      sessionID.textContent = "连接失败";
      revision.textContent = "—";
      showResult("认证失败、状态不可用或响应不安全。", "rejected");
    }
  });

  list.addEventListener("click", async (event) => {
    const button = event.target.closest("button[data-action]");
    const card = event.target.closest(".candidate-card");
    if (!button || !card || !currentState) return;
    try {
      await submitCommand(buildCommand(button.dataset.action, card.dataset.candidateId));
    } catch {
      showResult("命令未获得可验证结果；输出保持关闭。", "rejected");
    }
  });

  emergency.addEventListener("click", async () => {
    if (!currentState) return;
    const action = currentState.emergency_hidden ? "clear_emergency_hide" : "emergency_hide";
    try {
      await submitCommand(buildCommand(action));
    } catch {
      showResult("紧急命令未获得可验证结果；请保持输出关闭。", "rejected");
    }
  });

  ruleForm.addEventListener("click", async (event) => {
    const button = event.target.closest("button[data-rule-action]");
    if (!button) return;
    event.preventDefault();
    if (!ruleForm.reportValidity() || !currentState) return;
    try {
      await submitCommand(buildCommand(button.dataset.ruleAction, "", ruleID.value));
    } catch {
      showResult("规则命令未获得可验证结果。", "rejected");
    }
  });

  retry.addEventListener("click", async () => {
    if (!lastCommand) return;
    try {
      await submitCommand(lastCommand, false);
    } catch {
      showResult("重复命令未获得可验证结果。", "rejected");
    }
  });
})();

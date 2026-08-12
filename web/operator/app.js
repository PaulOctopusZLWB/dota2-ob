(() => {
  "use strict";
  const form = document.getElementById("command-form");
  const token = document.getElementById("token");
  const command = document.getElementById("command");
  const result = document.getElementById("result");

  command.value = JSON.stringify({
    schema_version: "operator_command.v1",
    command_id: "",
    session_id: "",
    action: "emergency_hide",
    expected_policy_revision: 0,
    policy_time_ms: 0
  }, null, 2);

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    result.dataset.state = "pending";
    result.textContent = "正在提交…";
    try {
      JSON.parse(command.value);
      const response = await fetch("/v1/operator/commands", {
        method: "POST",
        cache: "no-store",
        credentials: "omit",
        headers: {
          "Authorization": `Bearer ${token.value}`,
          "Content-Type": "application/json",
          "X-Dota2-OB-CSRF": "operator-command"
        },
        body: command.value
      });
      const body = await response.json();
      result.dataset.state = response.ok ? "accepted" : "rejected";
      result.textContent = JSON.stringify(body, null, 2);
    } catch {
      result.dataset.state = "rejected";
      result.textContent = "invalid_command_or_connection";
    }
  });
})();

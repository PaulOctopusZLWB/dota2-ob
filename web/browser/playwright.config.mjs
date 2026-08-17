import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./tests",
  outputDir: "./test-results",
  timeout: 20_000,
  fullyParallel: false,
  workers: 1,
  reporter: "line",
  use: {
    baseURL: "http://127.0.0.1:18838",
    channel: "chrome",
    headless: true,
    locale: "zh-CN",
    trace: "retain-on-failure"
  },
  webServer: {
    command: "exec node ./server-harness.mjs",
    url: "http://127.0.0.1:18838/overlay/",
    reuseExistingServer: false,
    timeout: 30_000
  }
});

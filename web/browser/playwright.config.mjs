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
    command: "go build -o ./test-results/dota2-ob ../../cmd/dota2-ob && exec ./test-results/dota2-ob --addr 127.0.0.1:18839 --delivery-addr 127.0.0.1:18838 --data-dir ./test-results/sessions",
    url: "http://127.0.0.1:18838/overlay/",
    reuseExistingServer: false,
    timeout: 30_000
  }
});

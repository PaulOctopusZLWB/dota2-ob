import { spawnSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { createOwnedRuntime, ownedRuntimePaths, removeOwnedRuntime } from "./server-harness.mjs";

const runtimeRoot = createOwnedRuntime();
const playwright = path.join(path.dirname(fileURLToPath(import.meta.url)), "node_modules", ".bin", "playwright");
let status = 1;
try {
  const result = spawnSync(playwright, ["test"], {
    env: { ...process.env, DOTA2_OB_BROWSER_RUNTIME: runtimeRoot },
    stdio: "inherit"
  });

  if (result.error) throw result.error;
  status = result.status ?? 1;
  if (status === 0 && fs.existsSync(runtimeRoot) && !fs.existsSync(ownedRuntimePaths(runtimeRoot).binary)) {
    throw new Error("Playwright managed server did not use the suite-owned runtime");
  }
} finally {
  removeOwnedRuntime(runtimeRoot);
}
if (fs.existsSync(runtimeRoot)) throw new Error("browser test runtime cleanup failed");
process.exitCode = status;

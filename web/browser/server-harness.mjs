import { spawn, spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const runtimePrefix = "dota2-ob-browser-";
const ownershipMarker = ".dota2-ob-browser-owned";

export function createOwnedRuntime() {
  const runtimeRoot = fs.mkdtempSync(path.join(os.tmpdir(), runtimePrefix));
  fs.chmodSync(runtimeRoot, 0o700);
  fs.writeFileSync(path.join(runtimeRoot, ownershipMarker), "owned\n", { mode: 0o600 });
  return runtimeRoot;
}

export function removeOwnedRuntime(runtimeRoot) {
  if (!runtimeRoot) return;
  const resolved = path.resolve(runtimeRoot);
  if (path.dirname(resolved) !== path.resolve(os.tmpdir()) || !path.basename(resolved).startsWith(runtimePrefix)) {
    throw new Error("refusing to remove an unowned browser-test runtime");
  }
  if (!fs.existsSync(resolved)) return;
  const marker = path.join(resolved, ownershipMarker);
  if (!fs.existsSync(marker) || fs.readFileSync(marker, "utf8") !== "owned\n") {
    throw new Error("refusing to remove an unowned browser-test runtime");
  }
  fs.rmSync(resolved, { recursive: true, force: true });
}

export function ownedRuntimePaths(runtimeRoot) {
  return {
    binary: path.join(runtimeRoot, "server-bin"),
    sessions: path.join(runtimeRoot, "sessions")
  };
}

async function run() {
  const runtimeRoot = createOwnedRuntime();
  const { binary, sessions } = ownedRuntimePaths(runtimeRoot);
  try {
    const build = spawnSync("go", ["build", "-o", binary, "../../cmd/dota2-ob"], { stdio: "inherit" });
    if (build.error) throw build.error;
    if (build.status !== 0) process.exitCode = build.status ?? 1;
    if (process.exitCode) return;

    const server = spawn(binary, [
      "--addr", "127.0.0.1:18839",
      "--delivery-addr", "127.0.0.1:18838",
      "--data-dir", sessions
    ], {
      env: { ...process.env, XDG_RUNTIME_DIR: runtimeRoot },
      stdio: "inherit"
    });
    for (const signal of ["SIGINT", "SIGTERM", "SIGHUP"]) {
      process.once(signal, () => server.kill(signal));
    }
    const result = await new Promise((resolve, reject) => {
      server.once("error", reject);
      server.once("exit", (code, signal) => resolve({ code, signal }));
    });
    if (result.signal) process.exitCode = 1;
    else process.exitCode = result.code ?? 1;
  } finally {
    removeOwnedRuntime(runtimeRoot);
  }
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  await run();
}

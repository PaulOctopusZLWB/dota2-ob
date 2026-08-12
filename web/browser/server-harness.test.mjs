import assert from "node:assert/strict";
import { randomBytes } from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import { createOwnedRuntime, ownedRuntimePaths, removeOwnedRuntime } from "./server-harness.mjs";

test("owned runtime is unique, private, and cleanup leaves the canonical token untouched", () => {
  const canonicalRoot = fs.mkdtempSync(path.join(os.tmpdir(), "dota2-ob-canonical-test-"));
  const canonicalToken = path.join(canonicalRoot, "dota2-ob", "runtime", "operator.token");
  const canonicalTokenValue = randomBytes(32);
  fs.mkdirSync(path.dirname(canonicalToken), { recursive: true, mode: 0o700 });
  fs.writeFileSync(canonicalToken, canonicalTokenValue, { mode: 0o600 });

  const first = createOwnedRuntime();
  const second = createOwnedRuntime();
  try {
    assert.notEqual(first, second);
    assert.notEqual(first, canonicalRoot);
    assert.equal(fs.statSync(first).mode & 0o777, 0o700);
    const layout = ownedRuntimePaths(first);
    assert.notEqual(layout.binary, path.join(first, "dota2-ob"));
    assert.equal(layout.sessions.startsWith(first + path.sep), true);
    removeOwnedRuntime(first);
    assert.equal(fs.existsSync(first), false);
    assert.deepEqual(fs.readFileSync(canonicalToken), canonicalTokenValue);
    assert.equal(fs.existsSync(second), true);
  } finally {
    removeOwnedRuntime(first);
    removeOwnedRuntime(second);
    fs.rmSync(canonicalRoot, { recursive: true, force: true });
  }
});

test("cleanup refuses a prefixed directory without the harness ownership marker", () => {
  const unowned = fs.mkdtempSync(path.join(os.tmpdir(), "dota2-ob-browser-unowned-"));
  try {
    assert.throws(() => removeOwnedRuntime(unowned), /unowned browser-test runtime/);
    assert.equal(fs.existsSync(unowned), true);
  } finally {
    fs.rmSync(unowned, { recursive: true, force: true });
  }
});

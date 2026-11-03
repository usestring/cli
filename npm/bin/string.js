#!/usr/bin/env node
"use strict";
// Runs the native binary from the platform package npm installed beside this
// one: the optionalDependency whose os and cpu match this machine.
const { spawnSync } = require("node:child_process");
const fs = require("node:fs");

const target = `${process.platform}-${process.arch}`;
const binary = process.platform === "win32" ? "string.exe" : "string";
let path;
try {
  path = require.resolve(`@usestring/cli-${target}/bin/${binary}`);
} catch {
  console.error(`@usestring/cli has no binary for ${target}. Download one from https://github.com/usestring/cli/releases.`);
  process.exit(1);
}
if (process.platform !== "win32") {
  try {
    fs.accessSync(path, fs.constants.X_OK);
  } catch {
    fs.chmodSync(path, 0o755);
  }
}
const result = spawnSync(path, process.argv.slice(2), { stdio: "inherit" });
if (result.error) {
  console.error(result.error.message);
  process.exit(1);
}
if (result.signal) process.kill(process.pid, result.signal);
process.exit(result.status ?? 1);

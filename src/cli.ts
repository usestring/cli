#!/usr/bin/env node

import { run } from "./run.js";

process.stdout.on("error", (error: NodeJS.ErrnoException) => {
  if (error.code === "EPIPE") {
    process.exit(process.exitCode ?? 0);
  }
  throw error;
});

process.exitCode = await run(process.argv.slice(2), {
  env: process.env,
  request: (url, init) => fetch(url, init),
  stdout: (data) => process.stdout.write(data),
  stderr: (data) => process.stderr.write(data)
});

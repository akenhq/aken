#!/usr/bin/env node
'use strict';

const { spawn } = require('node:child_process');

const key = `${process.platform}-${process.arch}`;
const packages = {
  'linux-x64': '@akenhq/mcp-linux-x64',
  'linux-arm64': '@akenhq/mcp-linux-arm64',
  'darwin-arm64': '@akenhq/mcp-darwin-arm64',
};
const packageName = packages[key];
if (!packageName) {
  console.error(`aken-mcp: unsupported platform ${key} (supported: Linux x64 and arm64, macOS arm64).`);
  process.exit(1);
}

let binary;
try {
  binary = require.resolve(`${packageName}/bin/aken-mcp`);
} catch {
  console.error(`aken-mcp: ${packageName} is not installed. Installs with --omit=optional or --no-optional skip it. See https://github.com/akenhq/aken/blob/main/docs/install.md#install-the-mcp-on-your-machine`);
  process.exit(1);
}

const child = spawn(binary, process.argv.slice(2), { stdio: 'inherit' });
const handlers = new Map();
for (const signal of ['SIGINT', 'SIGTERM', 'SIGHUP']) {
  const handler = () => child.kill(signal);
  handlers.set(signal, handler);
  process.on(signal, handler);
}
child.on('error', (error) => {
  console.error(`aken-mcp: ${error.message}`);
  process.exit(1);
});
child.on('exit', (code, signal) => {
  if (code !== null) process.exit(code);
  for (const [name, handler] of handlers) {
    process.removeListener(name, handler);
  }
  process.kill(process.pid, signal);
});

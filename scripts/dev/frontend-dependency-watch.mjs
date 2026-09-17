#!/usr/bin/env node

import { watch } from 'node:fs';

const [hostPidText, notifyPidText, ...files] = process.argv.slice(2);
const hostPid = Number.parseInt(hostPidText ?? '', 10);
const notifyPid = Number.parseInt(notifyPidText ?? '', 10);

if (
  !Number.isSafeInteger(hostPid) ||
  hostPid <= 1 ||
  !Number.isSafeInteger(notifyPid) ||
  notifyPid <= 1 ||
  files.length === 0
) {
  console.error('usage: frontend-dependency-watch.mjs HOST_PID NOTIFY_PID PACKAGE_FILE...');
  process.exit(2);
}

const watchers = [];
let closing = false;

function close(exitCode, message) {
  if (closing) return;
  closing = true;
  clearInterval(parentCheck);
  for (const watcher of watchers) watcher.close();
  if (message) console.error(message);
  process.exit(exitCode);
}

const parentCheck = setInterval(() => {
  try {
    process.kill(hostPid, 0);
  } catch {
    close(0);
  }
}, 250);
parentCheck.unref();

for (const file of files) {
  try {
    const watcher = watch(file, { persistent: true }, (eventType) => {
      const message = `[source-frontend] dependency metadata changed: ${file} (${eventType})`;
      try {
        process.kill(notifyPid, 'SIGUSR1');
      } catch (error) {
        close(1, `${message}; cannot notify host process group: ${error.message}`);
        return;
      }
      setTimeout(() => close(0, message), 50);
    });
    watcher.on('error', (error) => {
      close(1, `[source-frontend] dependency watcher failed for ${file}: ${error.message}`);
    });
    watchers.push(watcher);
  } catch (error) {
    close(1, `[source-frontend] cannot watch dependency metadata ${file}: ${error.message}`);
  }
}

process.once('SIGINT', () => close(0));
process.once('SIGTERM', () => close(0));

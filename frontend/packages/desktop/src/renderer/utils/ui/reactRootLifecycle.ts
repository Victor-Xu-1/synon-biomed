/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

/**
 * Defer a root cleanup until the current React commit stack has completed.
 * Vite can dispose an entry while React is rendering during HMR; synchronous
 * root.unmount() in that window can race React's DOM deletion pass.
 */
export function scheduleReactRootCleanup(cleanup: () => void): void {
  queueMicrotask(cleanup);
}

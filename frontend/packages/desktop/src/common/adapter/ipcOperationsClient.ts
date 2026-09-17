/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

/**
 * IPC Bridge → HTTP/WS adapter.
 *
 * This file replaces the original IPC bridge calls with HTTP REST and WebSocket
 * calls routed through the Synon Biomed WebHost gateway. Electron-native
 * operations (window controls, native dialogs, auto-update, devtools, zoom,
 * CDP, deep links) remain as IPC.
 */

import { bridge } from '@office-ai/platform';
import { stubProvider } from './httpBridge';

// ---------------------------------------------------------------------------
// Notification — stays IPC (Electron-native Notification API)
// ---------------------------------------------------------------------------

export type INotificationOptions = {
  title: string;
  body: string;
  icon?: string;
  conversation_id?: string;
};

export const notification = {
  show: bridge.buildProvider<void, INotificationOptions>('notification.show'),
  clicked: bridge.buildEmitter<{ conversation_id?: string }>('notification.clicked'),
};

// ---------------------------------------------------------------------------
// Task management — stubbed (internal process management)
// ---------------------------------------------------------------------------

export const task = {
  stopAll: stubProvider<{ success: boolean; count: number }, void>('task.stopAll', {
    success: true,
    count: 0,
  }),
  getRunningCount: stubProvider<{ success: boolean; count: number }, void>('task.getRunningCount', {
    success: true,
    count: 0,
  }),
};

// ---------------------------------------------------------------------------
// WebUI lifecycle stays IPC because the desktop process owns it. Account
// credentials are managed by the canonical authenticated account APIs rather
// than a second local WebUI credential path.
// ---------------------------------------------------------------------------

export interface IWebUIStatus {
  running: boolean;
  port: number;
  allowRemote: boolean;
  localUrl: string;
  networkUrl?: string;
  lanIP?: string;
  adminUsername: string;
  initialPassword?: string;
}

export interface IWebUIStartResult {
  port: number;
  allowRemote: boolean;
  localUrl: string;
  networkUrl?: string;
  lanIP?: string;
  initialPassword?: string;
}

export const webui = {
  getStatus: bridge.buildProvider<IWebUIStatus, void>('webui.get-status'),
  start: bridge.buildProvider<IWebUIStartResult, { port?: number; allowRemote?: boolean }>('webui.start'),
  stop: bridge.buildProvider<void, void>('webui.stop'),
  statusChanged: bridge.buildEmitter<{
    running: boolean;
    port?: number;
    localUrl?: string;
    networkUrl?: string;
    lanIP?: string;
    initialPassword?: string;
  }>('webui.status-changed'),
};

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

/** Stable public facade over responsibility-specific HTTP, WS and IPC clients. */
export * from './ipcBridgeTypes';
export * from './ipcCoreClients';
export * from './ipcConversationClient';
export * from './ipcRuntimeClient';
export * from './ipcFileClient';
export * from './ipcProviderClient';
export * from './ipcDatabaseClient';
export * from './ipcPreviewClient';
export * from './ipcOperationsClient';

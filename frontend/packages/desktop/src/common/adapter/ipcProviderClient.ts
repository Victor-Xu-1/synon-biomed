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

import type { IMcpServer } from '../config/storage';
import type { SetConfigOptionRequest, SetConfigOptionResponse } from '../types/platform/acpTypes';
import { httpDelete, httpGet, httpPost, httpPut, stubProvider } from './httpBridge';
import type { IBridgeResponse } from './ipcBridgeTypes';
import { conversation } from './ipcConversationClient';

// ---------------------------------------------------------------------------
// Google Auth — stubbed (Electron-native OAuth flow)
// ---------------------------------------------------------------------------

export const googleAuth = {
  status: stubProvider<IBridgeResponse<{ account: string }>, { proxy?: string }>('googleAuth.status', {
    success: false,
    msg: 'Google Auth not available in backend mode',
  }),
};

// ---------------------------------------------------------------------------
// Synon Biomed expert diagnostics
// ---------------------------------------------------------------------------

export const synonBiomed = {
  getManagedAgents: httpGet<import('@/renderer/utils/synonBiomed/runtime/runtimeTypes').ManagedAgent[], void>(
    '/api/synonbiomed/experts'
  ),
};

export const acpConversation = {
  sendMessage: conversation.sendMessage,
  responseStream: conversation.responseStream,
  setConfigOption: httpPut<SetConfigOptionResponse, { conversation_id: string; option_id: string; value: string }>(
    (p) => `/api/conversations/${p.conversation_id}/config-options/${encodeURIComponent(p.option_id)}`,
    (p): SetConfigOptionRequest => ({ value: p.value })
  ),
};

// ---------------------------------------------------------------------------
// MCP Service — routed to /api/mcp/*
// ---------------------------------------------------------------------------

export const mcpService = {
  listServers: httpGet<IMcpServer[], void>('/api/mcp/servers'),
  createServer: httpPost<
    IMcpServer,
    Pick<IMcpServer, 'name' | 'description' | 'transport' | 'original_json' | 'builtin'>
  >('/api/mcp/servers'),
  importServers: httpPost<
    IMcpServer[],
    {
      servers: Array<Pick<IMcpServer, 'name' | 'description' | 'transport' | 'original_json' | 'builtin'>>;
    }
  >('/api/mcp/servers/import'),
  updateServer: httpPut<
    IMcpServer,
    {
      id: string;
      data: Partial<Pick<IMcpServer, 'name' | 'description' | 'transport' | 'original_json' | 'builtin'>>;
    }
  >(
    (p) => `/api/mcp/servers/${p.id}`,
    (p) => p.data
  ),
  deleteServer: httpDelete<void, { id: string }>((p) => `/api/mcp/servers/${p.id}`),
  toggleServer: httpPost<IMcpServer, { id: string }>(
    (p) => `/api/mcp/servers/${p.id}/toggle`,
    () => undefined
  ),
  batchImportServers: httpPost<
    IMcpServer[],
    {
      servers: Array<Partial<IMcpServer> & Pick<IMcpServer, 'name' | 'transport'>>;
    }
  >('/api/mcp/servers/import'),
  testMcpConnection: httpPost<
    {
      success: boolean;
      tools?: Array<{
        name: string;
        description?: string;
        input_schema?: unknown;
        _meta?: Record<string, unknown>;
      }>;
      error?: string;
      code?: string;
      details?: unknown;
      needsAuth?: boolean;
      needs_auth?: boolean;
      authMethod?: 'oauth' | 'basic';
      auth_method?: 'oauth' | 'basic';
      wwwAuthenticate?: string;
      www_authenticate?: string;
    },
    IMcpServer & { runtime_scope_id?: string }
  >('/api/mcp/test-connection'),
  checkOAuthStatus: httpPost<{ authenticated: boolean }, { server_url: string }>('/api/mcp/oauth/check-status'),
  loginMcpOAuth: httpPost<
    {
      success: boolean;
      pending?: boolean;
      auth_url?: string;
      redirect_uri?: string;
      error?: string;
    },
    { server_url: string }
  >('/api/mcp/oauth/login'),
  logoutMcpOAuth: httpPost<void, { server_url: string }>('/api/mcp/oauth/logout'),
  getAuthenticatedServers: httpGet<string[], void>('/api/mcp/oauth/authenticated'),
};

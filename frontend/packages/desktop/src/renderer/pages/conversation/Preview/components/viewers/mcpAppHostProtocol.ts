import type { SynonBiomedMcpAppTool, SynonBiomedMcpAppToolResult } from '@/renderer/services/mcp/synonBiomedMcpApps';

declare const __APP_VERSION__: string;

export const MCP_APP_PROTOCOL_VERSION = '2026-01-26';
export const MCP_APP_MESSAGE_LIMIT = 1024 * 1024;
const maxMcpAppTools = 128;

export type MCPAppRPCMessage = {
  jsonrpc: '2.0';
  id?: string | number;
  method?: string;
  params?: unknown;
  result?: unknown;
  error?: { code: number; message: string; data?: unknown };
};

export function parseMCPAppRPCMessage(value: unknown): MCPAppRPCMessage | null {
  const record = asRecord(value);
  if (!record || record.jsonrpc !== '2.0') return null;
  const id = record.id;
  if (id !== undefined && typeof id !== 'string' && typeof id !== 'number') return null;
  if (typeof id === 'number' && !Number.isFinite(id)) return null;
  if (record.method !== undefined && (typeof record.method !== 'string' || !record.method.trim())) return null;
  const hasMethod = typeof record.method === 'string';
  const hasResult = Object.hasOwn(record, 'result');
  const hasError = Object.hasOwn(record, 'error');
  if (!hasMethod && id === undefined) return null;
  if (hasMethod && (hasResult || hasError)) return null;
  if (!hasMethod && hasResult === hasError) return null;
  try {
    if (new TextEncoder().encode(JSON.stringify(record)).byteLength > MCP_APP_MESSAGE_LIMIT) return null;
  } catch {
    return null;
  }
  return record as MCPAppRPCMessage;
}

export function mcpAppInitializeResult(protocolVersion: unknown, darkMode: boolean): Record<string, unknown> {
  if (protocolVersion !== MCP_APP_PROTOCOL_VERSION) throw new Error('MCP_APP_PROTOCOL_UNSUPPORTED');
  return {
    protocolVersion: MCP_APP_PROTOCOL_VERSION,
    hostInfo: { name: 'Synon Biomed', version: __APP_VERSION__ },
    hostCapabilities: { serverTools: { listChanged: false }, logging: {} },
    hostContext: {
      displayMode: 'inline',
      availableDisplayModes: ['inline'],
      theme: darkMode ? 'dark' : 'light',
      platform: 'web',
    },
  };
}

export function normalizeMCPAppTools(value: unknown): SynonBiomedMcpAppTool[] {
  const result = asRecord(value);
  if (!Array.isArray(result?.tools) || result.tools.length === 0 || result.tools.length > maxMcpAppTools) {
    throw new Error('MCP_APP_TOOLS_INVALID');
  }
  const seen = new Set<string>();
  return result.tools.map((item) => {
    const tool = asRecord(item);
    const name = normalizedString(tool?.name);
    const inputSchema = asRecord(tool?.inputSchema);
    if (!name || name.length > 128 || seen.has(name) || inputSchema?.type !== 'object') {
      throw new Error('MCP_APP_TOOL_INVALID');
    }
    seen.add(name);
    const description = normalizedString(tool?.description);
    const normalized: SynonBiomedMcpAppTool = { name, inputSchema };
    if (description) normalized.description = description;
    return normalized;
  });
}

export function normalizeMCPAppToolResult(value: unknown): SynonBiomedMcpAppToolResult {
  const result = asRecord(value);
  if (!result || !Array.isArray(result.content) || result.content.length > 128) {
    return mcpAppInvalidToolResult();
  }
  let totalBytes = 0;
  const content = result.content.flatMap((item) => {
    const block = asRecord(item);
    if (!block || block.type !== 'text' || (block.text !== undefined && typeof block.text !== 'string')) return [];
    const text = typeof block.text === 'string' ? block.text : undefined;
    totalBytes += text ? new TextEncoder().encode(text).byteLength : 0;
    return [{ type: 'text', ...(text !== undefined ? { text } : {}) }];
  });
  if (content.length !== result.content.length || totalBytes > MCP_APP_MESSAGE_LIMIT) return mcpAppInvalidToolResult();
  return {
    content,
    ...(result.structuredContent !== undefined ? { structuredContent: result.structuredContent } : {}),
    ...(result.isError === true ? { isError: true } : {}),
  };
}

export function normalizeMCPAppModelContext(value: unknown): Record<string, unknown> | null {
  const params = asRecord(value);
  const context = asRecord(params?.content ?? params?.context ?? params);
  if (!context) return null;
  try {
    if (new TextEncoder().encode(JSON.stringify(context)).byteLength > MCP_APP_MESSAGE_LIMIT) return null;
  } catch {
    return null;
  }
  return context;
}

function mcpAppInvalidToolResult(): SynonBiomedMcpAppToolResult {
  return {
    content: [{ type: 'text', text: 'MCP App returned an invalid result' }],
    isError: true,
  };
}

function normalizedString(value: unknown): string | null {
  return typeof value === 'string' && value.trim() === value && value ? value : null;
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

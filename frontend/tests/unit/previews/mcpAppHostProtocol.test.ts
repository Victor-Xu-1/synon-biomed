import { describe, expect, it } from 'vitest';
import {
  MCP_APP_PROTOCOL_VERSION,
  mcpAppInitializeResult,
  normalizeMCPAppModelContext,
  normalizeMCPAppToolResult,
  normalizeMCPAppTools,
  parseMCPAppRPCMessage,
} from '@/renderer/pages/conversation/Preview/components/viewers/mcpAppHostProtocol';

describe('MCP App browser host protocol', () => {
  it('accepts one bounded JSON-RPC message and rejects ambiguous or oversized data', () => {
    expect(
      parseMCPAppRPCMessage({
        jsonrpc: '2.0',
        id: 1,
        method: 'ui/initialize',
        params: {},
      })
    ).toEqual(expect.objectContaining({ id: 1, method: 'ui/initialize' }));
    expect(parseMCPAppRPCMessage({ jsonrpc: '1.0', id: 1 })).toBeNull();
    expect(parseMCPAppRPCMessage({ jsonrpc: '2.0', id: {}, method: 'ping' })).toBeNull();
    expect(
      parseMCPAppRPCMessage({
        jsonrpc: '2.0',
        id: 1,
        method: 'ping',
        result: {},
      })
    ).toBeNull();
    expect(
      parseMCPAppRPCMessage({
        jsonrpc: '2.0',
        id: 1,
        method: 'ping',
        params: { value: 'x'.repeat(1_048_577) },
      })
    ).toBeNull();
  });

  it('advertises only implemented read-only host capabilities and the product identity', () => {
    expect(mcpAppInitializeResult(MCP_APP_PROTOCOL_VERSION, true)).toEqual({
      protocolVersion: MCP_APP_PROTOCOL_VERSION,
      hostInfo: { name: 'Synon Biomed', version: '0.1.1' },
      hostCapabilities: { serverTools: { listChanged: false }, logging: {} },
      hostContext: {
        displayMode: 'inline',
        availableDisplayModes: ['inline'],
        theme: 'dark',
        platform: 'web',
      },
    });
    expect(() => mcpAppInitializeResult('unsupported', false)).toThrow('MCP_APP_PROTOCOL_UNSUPPORTED');
  });

  it('normalizes bounded app tools, results, and model context', () => {
    expect(
      normalizeMCPAppTools({
        tools: [
          {
            name: 'get_structure',
            description: 'Get structure',
            inputSchema: { type: 'object' },
          },
        ],
      })
    ).toEqual([
      {
        name: 'get_structure',
        description: 'Get structure',
        inputSchema: { type: 'object' },
      },
    ]);
    expect(() =>
      normalizeMCPAppTools({
        tools: [
          { name: 'duplicate', inputSchema: { type: 'object' } },
          { name: 'duplicate', inputSchema: { type: 'object' } },
        ],
      })
    ).toThrow('MCP_APP_TOOL_INVALID');
    expect(
      normalizeMCPAppToolResult({
        content: [{ type: 'text', text: 'ok' }],
        structuredContent: { ok: true },
      })
    ).toEqual(
      expect.objectContaining({
        content: [{ type: 'text', text: 'ok' }],
        structuredContent: { ok: true },
      })
    );
    expect(
      normalizeMCPAppToolResult({
        content: [{ type: 'image', data: 'secret' }],
      }).isError
    ).toBe(true);
    expect(normalizeMCPAppModelContext({ content: { selectedAtoms: [1, 2] } })).toEqual({ selectedAtoms: [1, 2] });
  });
});

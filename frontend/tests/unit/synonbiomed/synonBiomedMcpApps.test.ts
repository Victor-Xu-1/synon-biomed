import { describe, expect, it, vi } from 'vitest';
import {
  createSynonBiomedMcpAppResourceTicket,
  loadSynonBiomedMcpAppViewerBindings,
  pinSynonBiomedMcpAppArtifact,
  pollSynonBiomedMcpAppRequest,
  registerSynonBiomedMcpApp,
  resolveSynonBiomedMcpAppRequest,
  serializeSynonBiomedMcpAppMountArguments,
  synonBiomedMcpAppSandboxUrl,
  unregisterSynonBiomedMcpApp,
} from '@/renderer/services/mcp/synonBiomedMcpApps';

describe('Synon Biomed MCP App bridge service', () => {
  it('normalizes viewer bindings and rejects incomplete entries', async () => {
    const fetchImpl = vi.fn().mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          bindings: [
            {
              server_id: 'viewer',
              server_name: 'Viewer',
              resource_uri: 'ui://viewer/index.html',
              open_tool: 'open',
              content_param: 'content',
              content_encoding: 'utf8',
              in_process: true,
            },
            {},
          ],
        }),
        { status: 200 }
      )
    );

    await expect(loadSynonBiomedMcpAppViewerBindings({ fetchImpl })).resolves.toEqual([
      expect.objectContaining({
        serverId: 'viewer',
        resourceUri: 'ui://viewer/index.html',
        openTool: 'open',
      }),
    ]);
  });

  it('creates one exact-origin resource mount with canonical open-tool input and result', async () => {
    const fetchImpl = vi.fn(
      async () =>
        new Response(
          JSON.stringify({
            ticket: 'ticket-1',
            expires_at: '2026-07-27T12:00:00Z',
            tool_input: { ket: '{"root":{"nodes":[]}}', filename: 'empty.ket' },
            tool_result: {
              content: [
                {
                  type: 'text',
                  text: 'Opened read-only molecule preview; edits are not saved.',
                },
              ],
              structuredContent: { ket: '{"root":{"nodes":[]}}' },
            },
          }),
          { status: 201 }
        )
    );
    await expect(
      createSynonBiomedMcpAppResourceTicket(
        'bundled:ketcher-chemistry',
        'ui://ketcher-chemistry/editor',
        { ket: '{"root":{"nodes":[]}}', filename: 'empty.ket' },
        {
          fetchImpl,
          pageLocation: {
            hostname: 'localhost',
            port: '8765',
            protocol: 'http:',
          },
        }
      )
    ).resolves.toEqual(
      expect.objectContaining({
        url: 'http://mcp-app.localhost:8765/mcp-app-resource?ticket=ticket-1',
        origin: 'http://mcp-app.localhost:8765',
        toolInput: expect.objectContaining({ filename: 'empty.ket' }),
        toolResult: expect.objectContaining({
          structuredContent: { ket: '{"root":{"nodes":[]}}' },
        }),
      })
    );
    expect(fetchImpl).toHaveBeenCalledWith(
      '/api/mcp/apps/resource-tickets',
      expect.objectContaining({
        method: 'POST',
        body: expect.stringContaining('"server_id":"bundled:ketcher-chemistry"'),
      })
    );
  });

  it('rejects non-loopback sandbox authorities and redacts HTTP response details', async () => {
    expect(() =>
      synonBiomedMcpAppSandboxUrl('ticket', {
        hostname: 'localhost.attacker.test',
        port: '8765',
        protocol: 'http:',
      })
    ).toThrow('localhost');
    expect(
      synonBiomedMcpAppSandboxUrl('ipv6-ticket', {
        hostname: '[::1]',
        port: '8765',
        protocol: 'http:',
      })
    ).toBe('http://mcp-app.localhost:8765/mcp-app-resource?ticket=ipv6-ticket');
    await expect(
      createSynonBiomedMcpAppResourceTicket(
        'bundled:ketcher-chemistry',
        'ui://ketcher-chemistry/editor',
        {},
        {
          fetchImpl: vi.fn(
            async () =>
              new Response(JSON.stringify({ detail: 'upstream secret /private/path' }), {
                status: 503,
              })
          ),
          pageLocation: {
            hostname: '127.0.0.1',
            port: '8765',
            protocol: 'http:',
          },
        }
      )
    ).rejects.toThrow('MCP_APP_HTTP_503');
    await expect(
      createSynonBiomedMcpAppResourceTicket(
        'bundled:ketcher-chemistry',
        'ui://ketcher-chemistry/editor',
        {},
        {
          fetchImpl: vi.fn(async () => new Response('upstream secret /private/path', { status: 503 })),
          pageLocation: {
            hostname: '127.0.0.1',
            port: '8765',
            protocol: 'http:',
          },
        }
      )
    ).rejects.not.toThrow('upstream secret');
  });

  it('rejects an oversized inline mount before creating a network request', async () => {
    const fetchImpl = vi.fn();
    await expect(
      createSynonBiomedMcpAppResourceTicket(
        'bundled:ketcher-chemistry',
        'ui://ketcher-chemistry/editor',
        { ket: 'x'.repeat(2 * 1024 * 1024 + 1), filename: 'too-large.ket' },
        {
          fetchImpl,
          pageLocation: {
            hostname: 'localhost',
            port: '8765',
            protocol: 'http:',
          },
        }
      )
    ).rejects.toThrow('MCP_APP_INPUT_TOO_LARGE');
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it('measures escaped UTF-8 input before allocating the rejected serialized field', () => {
    const stringify = vi.spyOn(JSON, 'stringify');
    try {
      expect(() =>
        serializeSynonBiomedMcpAppMountArguments({
          ket: '"'.repeat(1024 * 1024 + 1),
        })
      ).toThrow('MCP_APP_INPUT_TOO_LARGE');
      expect(stringify).not.toHaveBeenCalled();
    } finally {
      stringify.mockRestore();
    }
    const htmlSensitive = '<'.repeat(700 * 1024);
    const serialized = serializeSynonBiomedMcpAppMountArguments({
      ket: htmlSensitive,
    });
    expect(new TextEncoder().encode(serialized).byteLength).toBe(htmlSensitive.length + 10);
    expect(serialized).toContain('<<<');
  });

  it('accepts a valid mount whose bounded duplicate ticket response exceeds one MiB', async () => {
    const ket = 'C'.repeat(600 * 1024);
    const fetchImpl = vi.fn(
      async () =>
        new Response(
          JSON.stringify({
            ticket: 'near-limit-ticket',
            expires_at: '2026-07-27T12:00:00Z',
            tool_input: { ket, filename: 'near-limit.ket' },
            tool_result: {
              content: [
                {
                  type: 'text',
                  text: 'Opened read-only molecule preview; edits are not saved.',
                },
              ],
              structuredContent: { ket },
            },
          }),
          { status: 201 }
        )
    );
    await expect(
      createSynonBiomedMcpAppResourceTicket(
        'bundled:ketcher-chemistry',
        'ui://ketcher-chemistry/editor',
        { ket, filename: 'near-limit.ket' },
        {
          fetchImpl,
          pageLocation: {
            hostname: 'localhost',
            port: '8765',
            protocol: 'http:',
          },
        }
      )
    ).resolves.toEqual(expect.objectContaining({ toolInput: expect.objectContaining({ ket }) }));
  });

  it('uses the authenticated registration, long-poll, result, unregister, and durable pin contracts', async () => {
    const calls: Array<{ path: string; init?: RequestInit }> = [];
    const fetchImpl = vi.fn(async (path: RequestInfo | URL, init?: RequestInit) => {
      calls.push({ path: String(path), init });
      if (path === '/api/mcp/apps/registrations' && init?.method === 'POST') {
        return new Response(
          JSON.stringify({
            registration_id: 'registration-1',
            artifact_id: 'artifact-1',
            expires_at: '2026-08-03T12:00:00Z',
          }),
          { status: 201 }
        );
      }
      if (String(path).startsWith('/api/mcp/apps/requests?')) {
        return new Response(
          JSON.stringify({
            request_id: 'request-1',
            server_id: 'bundled:ketcher-chemistry',
            server_name: 'Ketcher Chemistry',
            artifact_id: 'artifact-1',
            tool: 'get_structure',
            arguments: {},
          }),
          { status: 200 }
        );
      }
      if (path === '/api/mcp/apps/results') return new Response(null, { status: 204 });
      if (String(path).startsWith('/api/mcp/apps/registrations?') && init?.method === 'DELETE') {
        return new Response(null, { status: 204 });
      }
      if (path === '/api/mcp/apps/pin') {
        return new Response(
          JSON.stringify({
            artifact_id: 'saved-artifact',
            version_id: 'saved-version',
            filename: 'edited.ket',
          }),
          { status: 201 }
        );
      }
      throw new Error(`unexpected request ${String(path)}`);
    });

    await expect(
      registerSynonBiomedMcpApp(
        {
          rootFrameId: 'root-1',
          frameId: 'frame-1',
          server: 'bundled:ketcher-chemistry',
          artifactId: 'artifact-1',
          tools: [
            {
              name: 'get_structure',
              description: 'Get structure',
              inputSchema: { type: 'object' },
            },
          ],
        },
        { fetchImpl }
      )
    ).resolves.toEqual(expect.objectContaining({ registrationId: 'registration-1' }));
    await expect(pollSynonBiomedMcpAppRequest('registration-1', undefined, { fetchImpl })).resolves.toEqual(
      expect.objectContaining({ requestId: 'request-1', tool: 'get_structure' })
    );
    await expect(
      resolveSynonBiomedMcpAppRequest(
        'registration-1',
        'request-1',
        {
          content: [{ type: 'text', text: 'ok' }],
          structuredContent: { ket: '{}' },
        },
        { fetchImpl }
      )
    ).resolves.toBeUndefined();
    await expect(unregisterSynonBiomedMcpApp('registration-1', { fetchImpl })).resolves.toBeUndefined();
    await expect(
      pinSynonBiomedMcpAppArtifact(
        {
          rootFrameId: 'root-1',
          frameId: 'frame-1',
          artifactId: 'artifact-1',
          filename: 'edited.ket',
          contentType: 'application/json',
          content: '{"root":{}}',
          contentEncoding: 'utf8',
          agentName: 'Ketcher Chemistry',
          tool: 'get_structure',
          arguments: {},
          idempotencyKey: 'save-1',
        },
        { fetchImpl }
      )
    ).resolves.toEqual({
      artifactId: 'saved-artifact',
      versionId: 'saved-version',
      filename: 'edited.ket',
    });

    const registrationBody = JSON.parse(String(calls[0]?.init?.body));
    expect(registrationBody).toEqual({
      root_frame_id: 'root-1',
      frame_id: 'frame-1',
      server: 'bundled:ketcher-chemistry',
      artifact_id: 'artifact-1',
      tools: [
        {
          name: 'get_structure',
          description: 'Get structure',
          inputSchema: { type: 'object' },
        },
      ],
    });
    const pinCall = calls.find((call) => call.path === '/api/mcp/apps/pin');
    expect(new Headers(pinCall?.init?.headers).get('Idempotency-Key')).toBe('save-1');
  });

  it('treats an empty long-poll as no work without parsing a response body', async () => {
    await expect(
      pollSynonBiomedMcpAppRequest('registration-1', undefined, {
        fetchImpl: vi.fn(async () => new Response(null, { status: 204 })),
      })
    ).resolves.toBeNull();
  });
});

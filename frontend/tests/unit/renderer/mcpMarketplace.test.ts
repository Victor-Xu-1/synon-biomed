import { describe, expect, it, vi } from 'vitest';
import { searchMcpMarketplace } from '@/renderer/services/mcp/mcpMarketplace';

describe('searchMcpMarketplace', () => {
  it('queries the same-origin registry proxy and normalizes safe and credentialed remotes', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input));
      expect(url.origin).toBe('http://localhost');
      expect(url.pathname).toBe('/api/mcp-servers/marketplace');
      expect(url.searchParams.get('search')).toBe('pubmed');
      expect(url.searchParams.get('version')).toBe('latest');
      expect(url.searchParams.get('limit')).toBe('40');
      expect(init?.headers).toEqual({ Accept: 'application/json' });

      return new Response(
        JSON.stringify({
          servers: [
            {
              server: {
                name: 'com.example/pubmed',
                title: 'PubMed MCP',
                description: 'Biomedical literature search.',
                version: '1.2.0',
                repository: { url: 'https://github.com/example/pubmed-mcp' },
                remotes: [
                  {
                    type: 'streamable-http',
                    url: 'https://mcp.example.com/pubmed',
                  },
                ],
              },
            },
            {
              server: {
                name: 'com.example/secured',
                description: 'Credential-backed biomedical service.',
                remotes: [
                  {
                    type: 'sse',
                    url: 'https://mcp.example.com/secured',
                    headers: [{ isRequired: true, isSecret: true, value: 'Bearer {api_key}' }],
                  },
                ],
              },
            },
            {
              server: {
                name: 'com.example/insecure',
                remotes: [{ type: 'streamable-http', url: 'http://mcp.example.com/insecure' }],
              },
            },
            {
              server: {
                name: 'com.example/description-auth',
                description: 'This service requires an API key.',
                remotes: [{ type: 'streamable-http', url: 'https://mcp.example.com/description-auth' }],
              },
            },
          ],
          metadata: { nextCursor: 'next-page', count: 3 },
        }),
        { status: 200, headers: { 'content-type': 'application/json' } }
      );
    });

    const result = await searchMcpMarketplace('pubmed', { fetchImpl: fetchMock });

    expect(result.nextCursor).toBe('next-page');
    expect(result.count).toBe(3);
    expect(result.entries).toHaveLength(4);
    expect(result.entries[0]).toMatchObject({
      id: 'com.example/pubmed',
      remoteUrl: 'https://mcp.example.com/pubmed',
      transport: 'streamable_http',
      authRequired: false,
      repositoryUrl: 'https://github.com/example/pubmed-mcp',
    });
    expect(result.entries[1]).toMatchObject({
      id: 'com.example/secured',
      remoteUrl: 'https://mcp.example.com/secured',
      transport: 'sse',
      authRequired: true,
    });
    expect(result.entries[2]).toMatchObject({
      id: 'com.example/insecure',
      remoteUrl: null,
      transport: null,
      authRequired: false,
    });
    expect(result.entries[3]).toMatchObject({
      id: 'com.example/description-auth',
      remoteUrl: 'https://mcp.example.com/description-auth',
      transport: 'streamable_http',
      authRequired: true,
    });
  });

  it('fails closed when the registry response is not successful', async () => {
    const fetchMock = vi.fn(async () => new Response('{"detail":"unavailable"}', { status: 503 })) as typeof fetch;

    await expect(searchMcpMarketplace('clinical', { fetchImpl: fetchMock })).rejects.toMatchObject({
      name: 'McpMarketplaceError',
      status: 503,
    });
  });
});

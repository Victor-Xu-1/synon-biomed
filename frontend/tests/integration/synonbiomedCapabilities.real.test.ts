import { describe, expect, it } from 'vitest';
import {
  loadSynonBiomedAgents,
  loadSynonBiomedMcpDirectoryHealth,
  loadSynonBiomedMcpServers,
  loadSynonBiomedMcpToolPermissions,
  loadSynonBiomedSkills,
} from '@/renderer/services/synonBiomedCapabilities';
import { createSynonBiomedTestFetch } from './synonbiomedTestAuth';

const gatewayBaseUrl = process.env.SYNON_BIOMED_GATEWAY_URL ?? 'http://127.0.0.1:8766';
const authenticatedFetch = createSynonBiomedTestFetch(gatewayBaseUrl);
const backendBaseUrl = process.env.SYNON_BIOMED_BACKEND_URL ?? 'http://127.0.0.1:8766';
const backendAuthenticatedFetch = createSynonBiomedTestFetch(backendBaseUrl);

async function waitForConnectedMcpServer(serverId: string) {
  const deadline = Date.now() + 60_000;
  let servers = await loadSynonBiomedMcpServers({
    baseUrl: gatewayBaseUrl,
    fetchImpl: await authenticatedFetch,
  });

  while (Date.now() < deadline) {
    const server = servers.find((candidate) => candidate.id === serverId);
    if (server?.connectionStatus === 'connected' && server.health.ok) return servers;
    await new Promise((resolve) => setTimeout(resolve, 500));
    servers = await loadSynonBiomedMcpServers({
      baseUrl: gatewayBaseUrl,
      fetchImpl: await authenticatedFetch,
    });
  }

  return servers;
}

describe('Synon Biomed capability gateway integration', () => {
  it('reads the dedicated Synon Biomed expert registry without generic agent endpoints', async () => {
    const agents = await loadSynonBiomedAgents({ baseUrl: gatewayBaseUrl, fetchImpl: await authenticatedFetch });

    expect(agents.length).toBeGreaterThan(0);
    expect(agents.some((agent) => agent.name === 'OPERON')).toBe(true);
    expect(agents.every((agent) => agent.userHidden === false)).toBe(true);
  });

  it('reads the real skills catalog without runtime asset synthesis', async () => {
    const skills = await loadSynonBiomedSkills({ baseUrl: gatewayBaseUrl, fetchImpl: await authenticatedFetch });

    expect(skills.length).toBeGreaterThan(0);
    expect(skills.some((skill) => skill.name === 'alphafold2')).toBe(true);
    expect(skills.every((skill) => skill.skillId.length > 0)).toBe(true);
  });

  it('reads connected biomedical MCP tools from the backend connector registry', { timeout: 70_000 }, async () => {
    const servers = await waitForConnectedMcpServer('bundled:pubmed');

    expect(servers.length).toBeGreaterThan(20);
    expect(servers).toContainEqual(
      expect.objectContaining({
        id: 'bundled:pubmed',
        name: 'pubmed',
        displayName: 'PubMed',
        transport: 'stdio',
        enabled: true,
        connectionStatus: 'connected',
        health: { ok: true },
      })
    );
    expect(servers.every((server) => typeof server.health.ok === 'boolean')).toBe(true);
    expect(
      servers
        .filter((server) => server.enabled && server.connectionStatus === 'connected')
        .every((server) => server.health.ok)
    ).toBe(true);
  });

  it('reads real MCP directory health and tool permissions from the Synon Biomed backend', async () => {
    const [health, permissions] = await Promise.all([
      loadSynonBiomedMcpDirectoryHealth({
        baseUrl: backendBaseUrl,
        fetchImpl: await backendAuthenticatedFetch,
      }),
      loadSynonBiomedMcpToolPermissions('bundled:pubmed', {
        baseUrl: backendBaseUrl,
        fetchImpl: await backendAuthenticatedFetch,
      }),
    ]);

    expect(health.ok).toBe(true);
    expect(permissions.tools.length).toBeGreaterThan(5);
    const searchPermission = permissions.tools.find((tool) => tool.toolName === 'search_articles');
    expect(searchPermission).toMatchObject({
      toolName: 'search_articles',
      title: 'search_articles',
      readOnlyHint: false,
    });
    expect(['allow', 'ask', 'deny']).toContain(searchPermission?.state);
    expect(searchPermission?.description.length).toBeGreaterThan(0);
    expect(permissions.skipApprovalsActive).toBe(false);
  });
});

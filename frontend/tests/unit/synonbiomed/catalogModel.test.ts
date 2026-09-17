/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { describe, expect, it, vi } from 'vitest';
import {
  buildSynonBiomedMcpServers,
  buildSynonBiomedSkills,
  mergeSynonBiomedMcpServers,
  mergeSynonBiomedSkills,
  fetchSynonBiomedCatalog,
  createSynonBiomedProject,
  loadSynonBiomedAssistants,
  summarizeSynonBiomedCatalog,
  type SynonBiomedCatalogError,
  type SynonBiomedCatalog,
} from '@/renderer/services/synonBiomedCatalog';

it('loads Guid assistants from the user-visible expert profiles instead of the runtime asset catalog', async () => {
  const fetchImpl = vi.fn(async (input: string) => {
    expect(input).toBe('/api/synonbiomed/expert-profiles');
    return new Response(
      JSON.stringify([
        {
          name: 'AIDD_EXPERT',
          displayName: 'AI药物研发专家',
          description: '药物研发',
          source: 'bundled',
          healthy: true,
          enabled: true,
          userHidden: false,
        },
        {
          name: 'BOOKMARKER',
          displayName: '会话重点标记专家',
          source: 'bundled',
          healthy: true,
          enabled: true,
          userHidden: true,
        },
        {
          name: 'USER_EXPERT',
          displayName: '用户专家',
          description: '用户创建',
          source: 'user',
          healthy: true,
          enabled: true,
          userHidden: false,
        },
      ]),
      { status: 200 }
    );
  });

  const assistants = await loadSynonBiomedAssistants(fetchImpl);

  expect(assistants.map((assistant) => assistant.agent_id)).toEqual(['AIDD_EXPERT', 'USER_EXPERT']);
  expect(assistants[0]).toMatchObject({
    name: 'AI Drug Discovery Expert',
    name_i18n: {
      'en-US': 'AI Drug Discovery Expert',
      'zh-CN': 'AI 药物研发专家',
    },
    source: 'builtin',
  });
  expect(assistants[1]).toMatchObject({ name: '用户专家', source: 'user', deletable: true });
});

const catalog: SynonBiomedCatalog = {
  product: 'Synon Biomed',
  runtime: {
    runtimeAssetsDir: '/home/victor_1/synonbiomed-workbench/synonbiomed/runtime/assets',
    agents: { count: 2, names: ['Gene Analysis', 'Variant Review'] },
    skills: { count: 3, names: ['PubMed Search', 'Variant Lookup', 'Trial Match'] },
    mcpServers: { count: 1, names: ['Biomed Tools'] },
    thirdPartyAssets: { count: 2, names: ['NCBI BLAST', 'PubChem'] },
    seedProjects: {
      count: 2,
      projects: [
        {
          slug: 'immunotherapy',
          name: 'Immunotherapy',
          rootFrameId: 'seed-root-1',
          artifactCount: 28,
          childFrameCount: 4,
          folderCount: 3,
        },
        {
          slug: 'crispr_screen',
          name: 'CRISPR Screen',
          rootFrameId: 'seed-root-2',
          artifactCount: 31,
          childFrameCount: 5,
          folderCount: 4,
        },
      ],
    },
  },
  backend: {
    baseUrl: 'http://127.0.0.1:8892',
    health: {
      status: 'healthy',
      service: 'synonbiomed',
      agentsRegistered: 14,
    },
    agents: { count: 14, names: ['Gene Analysis', 'Variant Review'] },
    projects: {
      count: 2,
      projects: [
        {
          projectId: 'proj_stat6',
          name: 'STAT6',
          description: 'STAT6 SBDD PPI workflow',
          conversationCount: 2,
          artifactCount: 23,
          createdAt: '2026-07-02T03:59:09.453Z',
          updatedAt: '2026-07-05T09:24:10.591Z',
          lastActiveAt: '2026-07-05T09:24:10.591Z',
        },
        {
          projectId: 'proj_example',
          name: 'Example project',
          description: 'Example biomedical workflow project',
          conversationCount: 3,
          artifactCount: 83,
          createdAt: '2026-07-02T03:59:09.453Z',
          updatedAt: '2026-07-02T03:59:09.453Z',
          lastActiveAt: '2026-07-02T03:59:09.453Z',
        },
      ],
    },
  },
};

describe('Synon Biomed catalog model', () => {
  it('summarizes runtime capability counts for the settings UI', () => {
    const summary = summarizeSynonBiomedCatalog(catalog);

    expect(summary.product).toBe('Synon Biomed');
    expect(summary.backendStatus).toBe('healthy');
    expect(summary.counts).toEqual({
      agents: 2,
      skills: 3,
      mcpServers: 1,
      thirdPartyAssets: 2,
      backendAgents: 14,
      seedProjects: 2,
      backendProjects: 2,
    });
    expect(summary.seedProjects.map((project) => project.slug)).toEqual(['immunotherapy', 'crispr_screen']);
    expect(summary.backendProjects.map((project) => project.name)).toEqual(['STAT6', 'Example project']);
    expect(summary.featuredAgents).toEqual(['Gene Analysis', 'Variant Review']);
    expect(summary.featuredSkills).toEqual(['PubMed Search', 'Variant Lookup', 'Trial Match']);
    expect(summary.integrationSurfaces.map((surface) => surface.id)).toEqual([
      'launch',
      'conversation',
      'workspace',
      'preview',
      'capabilities',
      'governance',
      'runtime',
    ]);
    expect(summary.integrationSurfaces.every((surface) => !('legacyModules' in surface))).toBe(true);
  });

  it('maps runtime skills into SynonAI skill catalog entries', () => {
    const skills = buildSynonBiomedSkills({
      ...catalog,
      runtime: {
        ...catalog.runtime,
        skills: { count: 2, names: ['alphafold2', 'cheminfo-render'] },
      },
    });

    expect(skills).toEqual([
      {
        name: 'alphafold2',
        description: 'Synon Biomed skill from runtime assets: alphafold2.',
        location: `${catalog.runtime.runtimeAssetsDir}/skills/alphafold2/SKILL.md`,
        relative_location: 'synonbiomed/skills/alphafold2/SKILL.md',
        is_auto_inject: false,
        is_custom: false,
        source: 'builtin',
      },
      {
        name: 'cheminfo-render',
        description: 'Synon Biomed skill from runtime assets: cheminfo-render.',
        location: `${catalog.runtime.runtimeAssetsDir}/skills/cheminfo-render/SKILL.md`,
        relative_location: 'synonbiomed/skills/cheminfo-render/SKILL.md',
        is_auto_inject: false,
        is_custom: false,
        source: 'builtin',
      },
    ]);
  });

  it('maps launchable runtime MCP servers into SynonAI tool catalog entries without rewriting third-party code', () => {
    const servers = buildSynonBiomedMcpServers({
      ...catalog,
      runtime: {
        ...catalog.runtime,
        mcpServers: { count: 2, names: ['mcp_pubmed', 'ketcher-chemistry'] },
      },
    });

    expect(servers.map((server) => server.id)).toEqual([
      'synonbiomed:mcp:mcp_pubmed',
      'synonbiomed:mcp:ketcher-chemistry',
    ]);
    expect(servers[0]).toMatchObject({
      name: 'mcp_pubmed',
      builtin: true,
      enabled: true,
      transport: {
        type: 'stdio',
        command: 'python',
        args: [`${catalog.runtime.runtimeAssetsDir}/mcp-servers/bio-tools/run_server.py`, 'mcp_pubmed'],
      },
    });
    expect(servers[1]).toMatchObject({
      name: 'ketcher-chemistry',
      builtin: true,
      enabled: true,
      transport: {
        type: 'stdio',
        command: 'synon-go',
        args: ['mcp-ketcher'],
      },
    });
  });

  it('uses Synon Biomed runtime skills as the native catalog and removes unrelated SynonAI builtin skills', () => {
    const baseSkills = [
      { name: 'cron', description: 'Scheduled tasks', source: 'builtin', is_custom: false },
      { name: 'xiaohongshu-post', description: 'Social media copywriting', source: 'builtin', is_custom: false },
      { name: 'lab-note-template', description: 'User lab note', source: 'custom', is_custom: true },
    ];
    const synonBiomedSkills = buildSynonBiomedSkills({
      ...catalog,
      runtime: {
        ...catalog.runtime,
        skills: { count: 2, names: ['alphafold2', 'pubmed-search'] },
      },
    });

    expect(mergeSynonBiomedSkills(baseSkills, synonBiomedSkills).map((skill) => skill.name)).toEqual([
      'alphafold2',
      'pubmed-search',
    ]);
  });

  it('does not fall back to the original skill catalog when Synon Biomed skills are unavailable', () => {
    const baseSkills = [{ name: 'cron', description: 'Scheduled tasks', source: 'builtin', is_custom: false }];

    expect(mergeSynonBiomedSkills(baseSkills, [])).toEqual([]);
  });

  it('uses Synon Biomed MCP servers as the native builtin tool catalog and removes unrelated builtin tools', () => {
    const baseServers = [
      {
        id: 'builtin-browser',
        name: 'browser-tools',
        enabled: true,
        transport: { type: 'stdio' as const, command: 'node', args: ['browser.js'] },
        created_at: 1,
        updated_at: 1,
        original_json: '{}',
        builtin: true,
      },
      {
        id: 'user-lims',
        name: 'LIMS Connector',
        enabled: true,
        transport: { type: 'stdio' as const, command: 'python', args: ['lims.py'] },
        created_at: 2,
        updated_at: 2,
        original_json: '{}',
        builtin: false,
      },
    ];
    const synonBiomedServers = buildSynonBiomedMcpServers({
      ...catalog,
      runtime: {
        ...catalog.runtime,
        mcpServers: { count: 2, names: ['mcp_pubmed', 'ketcher-chemistry'] },
      },
    });

    expect(mergeSynonBiomedMcpServers(baseServers, synonBiomedServers).map((server) => server.id)).toEqual([
      'synonbiomed:mcp:mcp_pubmed',
      'synonbiomed:mcp:ketcher-chemistry',
      'user-lims',
    ]);
  });

  it('keeps the original MCP catalog when Synon Biomed MCP servers are unavailable', () => {
    const baseServers = [
      {
        id: 'builtin-browser',
        name: 'browser-tools',
        enabled: true,
        transport: { type: 'stdio' as const, command: 'node', args: ['browser.js'] },
        created_at: 1,
        updated_at: 1,
        original_json: '{}',
        builtin: true,
      },
    ];

    expect(mergeSynonBiomedMcpServers(baseServers, [])).toEqual(baseServers);
  });
  it('fetches the Synon Biomed catalog from the web host API', async () => {
    const fetchImpl = vi.fn(async () => new Response(JSON.stringify(catalog), { status: 200 }));

    await expect(fetchSynonBiomedCatalog(fetchImpl)).resolves.toEqual(catalog);
    expect(fetchImpl).toHaveBeenCalledWith('/api/synonbiomed/catalog', {
      method: 'GET',
      headers: { Accept: 'application/json' },
    });
  });

  it('creates a Synon Biomed backend project through the SynonAI web host and returns a selectable workspace', async () => {
    const fetchImpl = vi.fn(
      async () =>
        new Response(
          JSON.stringify({
            project: {
              projectId: 'proj_created',
              name: 'SynonAI Oncology Review',
              description: 'Created from SynonAI',
              conversationCount: 0,
              artifactCount: 0,
              createdAt: '2026-07-10T01:00:00.000Z',
              updatedAt: '2026-07-10T01:00:00.000Z',
              lastActiveAt: '2026-07-10T01:00:00.000Z',
            },
          }),
          { status: 201 }
        )
    );

    await expect(
      createSynonBiomedProject(
        {
          name: 'SynonAI Oncology Review',
          description: 'Created from SynonAI',
          context: 'STAT6 workflow',
        },
        fetchImpl
      )
    ).resolves.toEqual({
      id: 'synonbiomed-project:proj_created',
      name: 'SynonAI Oncology Review',
      source: 'backend-project',
      description: 'Created from SynonAI',
      artifactCount: 0,
      conversationCount: 0,
      workspaceUri: 'synonbiomed://project/proj_created?name=SynonAI+Oncology+Review&artifacts=0&conversations=0',
      projectId: 'proj_created',
    });
    expect(fetchImpl).toHaveBeenCalledWith('/api/synonbiomed/projects', {
      method: 'POST',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: 'SynonAI Oncology Review',
        description: 'Created from SynonAI',
        context: 'STAT6 workflow',
      }),
    });
  });

  it('raises a typed error when the catalog API is unavailable', async () => {
    const fetchImpl = vi.fn(async () => new Response('backend unavailable', { status: 503 }));

    await expect(fetchSynonBiomedCatalog(fetchImpl)).rejects.toMatchObject({
      name: 'SynonBiomedCatalogError',
      status: 503,
      message: 'backend unavailable',
    } satisfies Partial<SynonBiomedCatalogError>);
  });
});

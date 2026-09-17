/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { Assistant } from '@/common/types/agent/assistantTypes';
import type { IMcpServer } from '@/common/config/storage';
import { loadSynonBiomedAgents, toSynonAIAssistant } from './synonBiomedCapabilities';

export const SYNON_BIOMED_ASSISTANTS_CACHE_KEY = 'synonbiomed.assistants.catalog.v1';

export type SynonBiomedSkillInfo = {
  name: string;
  description: string;
  location: string;
  relative_location?: string;
  is_auto_inject: boolean;
  is_custom: boolean;
  source: 'builtin';
};

type CountedNames = {
  count: number;
  names: string[];
};

export type SynonBiomedSeedProject = {
  slug: string;
  name: string;
  manifestPath?: string;
  rootFrameId: string;
  artifactCount: number;
  childFrameCount: number;
  folderCount: number;
};

export type SynonBiomedSeedProjectCatalog = {
  count: number;
  projects: SynonBiomedSeedProject[];
};

export type SynonBiomedBackendProject = {
  projectId: string;
  name: string;
  description: string | null;
  conversationCount: number;
  artifactCount: number;
  createdAt: string | null;
  updatedAt: string | null;
  lastActiveAt: string | null;
};

export type SynonBiomedBackendProjectCatalog = {
  count: number;
  projects: SynonBiomedBackendProject[];
};

export type SynonBiomedCatalog = {
  product: 'Synon Biomed' | string;
  runtime: {
    runtimeAssetsDir: string;
    agents: CountedNames;
    skills: CountedNames;
    mcpServers: CountedNames;
    thirdPartyAssets: CountedNames;
    seedProjects: SynonBiomedSeedProjectCatalog;
  };
  backend: {
    baseUrl: string;
    health: {
      status: string;
      service: string;
      agentsRegistered?: number;
    };
    agents: CountedNames;
    projects: SynonBiomedBackendProjectCatalog;
  };
};

export type SynonBiomedCatalogSummary = {
  product: string;
  backendStatus: string;
  runtimeAssetsDir: string;
  backendBaseUrl: string;
  counts: {
    agents: number;
    skills: number;
    mcpServers: number;
    thirdPartyAssets: number;
    backendAgents: number;
    seedProjects: number;
    backendProjects: number;
  };
  featuredAgents: string[];
  featuredSkills: string[];
  seedProjects: SynonBiomedSeedProject[];
  backendProjects: SynonBiomedBackendProject[];
  mcpServers: string[];
  thirdPartyAssets: string[];
  integrationSurfaces: SynonBiomedIntegrationSurface[];
};

export type SynonBiomedWorkspaceOption = {
  id: string;
  name: string;
  source: 'backend-project' | 'seed-project';
  description: string | null;
  artifactCount: number;
  conversationCount?: number;
  workspaceUri?: string;
  projectId?: string;
  slug?: string;
  manifestPath?: string;
  rootFrameId?: string;
};

export type SynonBiomedIntegrationSurfaceId =
  | 'launch'
  | 'conversation'
  | 'workspace'
  | 'preview'
  | 'capabilities'
  | 'governance'
  | 'runtime';

export type SynonBiomedIntegrationSurface = {
  id: SynonBiomedIntegrationSurfaceId;
  synonAiSurface:
    | 'guid'
    | 'conversation'
    | 'workspace'
    | 'preview'
    | 'settings-capabilities'
    | 'settings-governance'
    | 'web-host';
};

type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

export type SynonBiomedCreateProjectInput = {
  name: string;
  description?: string;
  context?: string;
};

const CATALOG_ENDPOINT = '/api/synonbiomed/catalog';
const PROJECTS_ENDPOINT = '/api/synonbiomed/projects';
const FEATURED_LIMIT = 8;
const SYNON_BIOMED_ASSISTANT_SORT_BASE = 900_000;
const SYNON_BIOMED_AGENT_BACKEND = 'synonbiomed';

const SURFACE_DEFINITIONS: SynonBiomedIntegrationSurface[] = [
  {
    id: 'launch',
    synonAiSurface: 'guid',
  },
  {
    id: 'conversation',
    synonAiSurface: 'conversation',
  },
  {
    id: 'workspace',
    synonAiSurface: 'workspace',
  },
  {
    id: 'preview',
    synonAiSurface: 'preview',
  },
  {
    id: 'capabilities',
    synonAiSurface: 'settings-capabilities',
  },
  {
    id: 'governance',
    synonAiSurface: 'settings-governance',
  },
  {
    id: 'runtime',
    synonAiSurface: 'web-host',
  },
];

export class SynonBiomedCatalogError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = 'SynonBiomedCatalogError';
    this.status = status;
  }
}

export class SynonBiomedAssistantCatalogError extends Error {
  readonly code = 'ASSISTANT_CATALOG_LOAD_FAILED' as const;

  constructor(cause: unknown) {
    super('Synon Biomed assistant catalog is unavailable', { cause });
    this.name = 'SynonBiomedAssistantCatalogError';
  }
}

export async function fetchSynonBiomedCatalog(fetchImpl: FetchLike = fetch): Promise<SynonBiomedCatalog> {
  const response = await fetchImpl(CATALOG_ENDPOINT, {
    method: 'GET',
    headers: { Accept: 'application/json' },
  });

  if (!response.ok) {
    throw new SynonBiomedCatalogError(response.status, await readErrorMessage(response));
  }

  return (await response.json()) as SynonBiomedCatalog;
}

export function summarizeSynonBiomedCatalog(catalog: SynonBiomedCatalog): SynonBiomedCatalogSummary {
  return {
    product: catalog.product,
    backendStatus: catalog.backend.health.status,
    runtimeAssetsDir: catalog.runtime.runtimeAssetsDir,
    backendBaseUrl: catalog.backend.baseUrl,
    counts: {
      agents: catalog.runtime.agents.count,
      skills: catalog.runtime.skills.count,
      mcpServers: catalog.runtime.mcpServers.count,
      thirdPartyAssets: catalog.runtime.thirdPartyAssets.count,
      backendAgents: catalog.backend.health.agentsRegistered ?? catalog.backend.agents.count,
      seedProjects: catalog.runtime.seedProjects.count,
      backendProjects: catalog.backend.projects.count,
    },
    featuredAgents: catalog.runtime.agents.names.slice(0, FEATURED_LIMIT),
    featuredSkills: catalog.runtime.skills.names.slice(0, FEATURED_LIMIT),
    seedProjects: catalog.runtime.seedProjects.projects.slice(0, FEATURED_LIMIT),
    backendProjects: catalog.backend.projects.projects.slice(0, FEATURED_LIMIT),
    mcpServers: catalog.runtime.mcpServers.names,
    thirdPartyAssets: catalog.runtime.thirdPartyAssets.names,
    integrationSurfaces: buildSynonBiomedIntegrationSurfaces(),
  };
}

export async function loadSynonBiomedCatalogSummary(fetchImpl: FetchLike = fetch): Promise<SynonBiomedCatalogSummary> {
  return summarizeSynonBiomedCatalog(await fetchSynonBiomedCatalog(fetchImpl));
}

function buildSynonBiomedProjectWorkspaceUri(project: SynonBiomedBackendProject): string {
  const params = new URLSearchParams();
  if (project.name) {
    params.set('name', project.name);
  }
  params.set('artifacts', String(project.artifactCount));
  params.set('conversations', String(project.conversationCount));

  return `synonbiomed://project/${encodeURIComponent(project.projectId)}?${params.toString()}`;
}

export async function createSynonBiomedProject(
  input: SynonBiomedCreateProjectInput,
  fetchImpl: FetchLike = fetch
): Promise<SynonBiomedWorkspaceOption> {
  const response = await fetchImpl(PROJECTS_ENDPOINT, {
    method: 'POST',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    body: JSON.stringify({
      name: input.name,
      description: input.description ?? '',
      context: input.context ?? '',
    }),
  });

  if (!response.ok) {
    throw new SynonBiomedCatalogError(response.status, await readErrorMessage(response));
  }

  const payload = (await response.json()) as { project?: SynonBiomedBackendProject };
  if (!payload.project?.projectId || !payload.project.name) {
    throw new SynonBiomedCatalogError(response.status, 'Synon Biomed project create response is invalid');
  }

  return toSynonBiomedBackendProjectWorkspaceOption(payload.project);
}

function toSynonBiomedBackendProjectWorkspaceOption(project: SynonBiomedBackendProject): SynonBiomedWorkspaceOption {
  return {
    id: `synonbiomed-project:${project.projectId}`,
    name: project.name,
    source: 'backend-project' as const,
    description: project.description,
    artifactCount: project.artifactCount,
    conversationCount: project.conversationCount,
    workspaceUri: buildSynonBiomedProjectWorkspaceUri(project),
    projectId: project.projectId,
  };
}

export function buildSynonBiomedWorkspaceOptions(summary: SynonBiomedCatalogSummary): SynonBiomedWorkspaceOption[] {
  const backendProjects: SynonBiomedWorkspaceOption[] = summary.backendProjects.map(
    toSynonBiomedBackendProjectWorkspaceOption
  );
  const seedProjects: SynonBiomedWorkspaceOption[] = summary.seedProjects.map((project) => ({
    id: `synonbiomed-seed:${project.slug}`,
    name: project.name,
    source: 'seed-project' as const,
    description: null as string | null,
    artifactCount: project.artifactCount,
    slug: project.slug,
    manifestPath: project.manifestPath,
    rootFrameId: project.rootFrameId,
  }));

  return [...backendProjects, ...seedProjects];
}

export function buildSynonBiomedIntegrationSurfaces(): SynonBiomedIntegrationSurface[] {
  return SURFACE_DEFINITIONS.map((surface) => ({
    id: surface.id,
    synonAiSurface: surface.synonAiSurface,
  }));
}

export function buildSynonBiomedSkills(catalog: SynonBiomedCatalog): SynonBiomedSkillInfo[] {
  return catalog.runtime.skills.names.map((skillName) => ({
    name: skillName,
    description: `Synon Biomed skill from runtime assets: ${skillName}.`,
    location: `${catalog.runtime.runtimeAssetsDir}/skills/${skillName}/SKILL.md`,
    relative_location: `synonbiomed/skills/${skillName}/SKILL.md`,
    is_auto_inject: false,
    is_custom: false,
    source: 'builtin',
  }));
}

export function mergeSynonBiomedSkills<T extends { name: string; source?: string; is_custom?: boolean }>(
  _baseSkills: T[],
  synonBiomedSkills: SynonBiomedSkillInfo[]
): Array<T | SynonBiomedSkillInfo> {
  return synonBiomedSkills;
}

function normalizeCatalogName(name: string): string {
  return name.trim().toLowerCase();
}

export async function loadSynonBiomedSkills(fetchImpl: FetchLike = fetch): Promise<SynonBiomedSkillInfo[]> {
  try {
    return buildSynonBiomedSkills(await fetchSynonBiomedCatalog(fetchImpl));
  } catch {
    return [];
  }
}

export function buildSynonBiomedMcpServers(catalog: SynonBiomedCatalog): IMcpServer[] {
  return catalog.runtime.mcpServers.names.flatMap((serverName) => {
    const server = buildSynonBiomedMcpServer(catalog.runtime.runtimeAssetsDir, serverName);
    return server ? [server] : [];
  });
}

export function mergeSynonBiomedMcpServers(baseServers: IMcpServer[], synonBiomedServers: IMcpServer[]): IMcpServer[] {
  if (synonBiomedServers.length === 0) return baseServers;

  const existingSynonBiomedServers = new Map(
    baseServers
      .filter((server) => server.builtin === true && isSynonBiomedMcpServer(server))
      .map((server) => [server.id, server])
  );
  const synonBiomedIds = new Set(synonBiomedServers.map((server) => server.id));
  const synonBiomedNames = new Set(synonBiomedServers.map((server) => normalizeCatalogName(server.name)));
  const userServers = baseServers.filter(
    (server) =>
      server.builtin !== true &&
      !synonBiomedIds.has(server.id) &&
      !synonBiomedNames.has(normalizeCatalogName(server.name))
  );

  return [...synonBiomedServers.map((server) => existingSynonBiomedServers.get(server.id) ?? server), ...userServers];
}

function isSynonBiomedMcpServer(server: Pick<IMcpServer, 'id' | 'name' | 'original_json'>): boolean {
  return server.id.startsWith('synonbiomed:mcp:') || server.original_json.includes('"synonbiomed"');
}
export async function loadSynonBiomedMcpServers(fetchImpl: FetchLike = fetch): Promise<IMcpServer[]> {
  try {
    return buildSynonBiomedMcpServers(await fetchSynonBiomedCatalog(fetchImpl));
  } catch {
    return [];
  }
}

export function buildSynonBiomedAssistants(catalog: SynonBiomedCatalog): Assistant[] {
  const agentNames =
    catalog.backend.agents.names.length > 0 ? catalog.backend.agents.names : catalog.runtime.agents.names;
  const agentStatus = catalog.backend.health.status === 'healthy' ? 'online' : 'offline';

  return agentNames.map((agentName, index) => {
    const id = `synonbiomed:${toSynonBiomedAgentSlug(agentName)}`;
    const displayName = toSynonBiomedAgentDisplayName(agentName);
    const description = `Synon Biomed expert agent connected through ${catalog.backend.baseUrl}.`;
    const zhDescription = `通过 ${catalog.backend.baseUrl} 接入的 Synon Biomed 专家 Agent。`;

    return {
      id,
      source: 'builtin',
      name: displayName,
      name_i18n: {
        'en-US': displayName,
        'zh-CN': displayName,
      },
      description,
      description_i18n: {
        'en-US': description,
        'zh-CN': zhDescription,
      },
      enabled: true,
      sort_order: SYNON_BIOMED_ASSISTANT_SORT_BASE + index,
      agent_id: agentName,
      agent: {
        type: 'synonbiomed',
        source: 'builtin',
        acp_backend: SYNON_BIOMED_AGENT_BACKEND,
      },
      enabled_skills: [] as string[],
      custom_skill_names: [] as string[],
      disabled_builtin_skills: [] as string[],
      context: `Use the Synon Biomed backend agent ${agentName} for biomedical research workflows.`,
      context_i18n: {
        'en-US': `Use the Synon Biomed backend agent ${agentName} for biomedical research workflows.`,
        'zh-CN': `使用 Synon Biomed 后端专家 ${agentName} 处理生物医药研究任务。`,
      },
      prompts: [] as string[],
      prompts_i18n: {},
      models: [] as string[],
      agent_status: agentStatus,
      agent_status_message:
        agentStatus === 'online'
          ? undefined
          : `Synon Biomed backend status is ${catalog.backend.health.status || 'unknown'}.`,
      deletable: false,
    } satisfies Assistant;
  });
}

export function mergeSynonBiomedAssistants(
  baseAssistants: Assistant[],
  synonBiomedAssistants: Assistant[]
): Assistant[] {
  if (synonBiomedAssistants.length === 0) return [];

  const existingSynonBiomedAssistants = new Map(
    baseAssistants
      .filter((assistant) => isSynonBiomedAssistant(assistant))
      .map((assistant) => [assistant.id, assistant])
  );

  return synonBiomedAssistants.map((assistant) => existingSynonBiomedAssistants.get(assistant.id) ?? assistant);
}

function isSynonBiomedAssistant(assistant: Assistant): boolean {
  return assistant.id.startsWith('synonbiomed:') || assistant.agent?.type === 'synonbiomed';
}
export async function loadSynonBiomedAssistants(fetchImpl: FetchLike = fetch): Promise<Assistant[]> {
  try {
    const agents = await loadSynonBiomedAgents({ fetchImpl: fetchImpl as typeof fetch });
    return await Promise.all(agents.map(toSynonAIAssistant));
  } catch (error) {
    throw new SynonBiomedAssistantCatalogError(error);
  }
}

async function readErrorMessage(response: Response): Promise<string> {
  const text = await response.text();
  if (!text.trim()) {
    return `Synon Biomed catalog request failed with HTTP ${response.status}`;
  }

  try {
    const payload = JSON.parse(text) as { message?: unknown; error?: unknown };
    if (typeof payload.message === 'string') return payload.message;
    if (typeof payload.error === 'string') return payload.error;
  } catch {
    // Non-JSON failures from proxies should still surface their response body.
  }

  return text;
}

function buildSynonBiomedMcpServer(runtimeAssetsDir: string, serverName: string): IMcpServer | null {
  const base = `${runtimeAssetsDir}/mcp-servers`;
  const transport =
    serverName === 'ketcher-chemistry'
      ? {
          type: 'stdio' as const,
          command: 'synon-go',
          args: ['mcp-ketcher'],
        }
      : serverName.startsWith('mcp_')
        ? {
            type: 'stdio' as const,
            command: 'python',
            args: [`${base}/bio-tools/run_server.py`, serverName],
          }
        : null;

  if (!transport) {
    return null;
  }

  const originalJson = JSON.stringify(
    {
      source: 'synonbiomed',
      name: serverName,
      transport,
    },
    null,
    2
  );

  return {
    id: `synonbiomed:mcp:${serverName}`,
    name: serverName,
    description: `Synon Biomed MCP server from runtime assets: ${serverName}.`,
    enabled: true,
    transport,
    created_at: 0,
    updated_at: 0,
    original_json: originalJson,
    builtin: true,
  };
}

function toSynonBiomedAgentSlug(agentName: string): string {
  return agentName
    .trim()
    .toLowerCase()
    .replace(/[_\s]+/g, '-')
    .replace(/[^a-z0-9-]/g, '')
    .replace(/-+/g, '-')
    .replace(/^-|-$/g, '');
}

function toSynonBiomedAgentDisplayName(agentName: string): string {
  return agentName
    .trim()
    .split(/[_\s-]+/)
    .filter(Boolean)
    .map((token) => (token.length <= 4 ? token.toUpperCase() : token[0].toUpperCase() + token.slice(1).toLowerCase()))
    .join(' ');
}

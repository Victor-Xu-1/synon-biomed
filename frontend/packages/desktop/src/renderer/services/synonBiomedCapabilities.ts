import type { Assistant } from '@/common/types/agent/assistantTypes';
import { resolveSynonBiomedBuiltinExpertText } from './agents/synonBiomedExpertLocalization';
import { isSynonBiomedHttpError, requestSynonBiomedJson } from './synonBiomedHttp';

export type SynonBiomedCapabilityOptions = {
  baseUrl?: string;
  fetchImpl?: typeof fetch;
};

export type SynonBiomedAgent = {
  name: string;
  displayName: string;
  description: string;
  healthy: boolean;
  enabled: boolean;
  source: string;
  skillsLocked: boolean;
  unrestricted: boolean;
  supportsPlanMode: boolean;
  userHidden: boolean;
  skillNames: string[];
};

export type SynonBiomedSkill = {
  name: string;
  displayName: string;
  description: string;
  description_i18n?: Record<string, string>;
  source: string;
  category: string | null;
  license: string | null;
  skillId: string;
  updatedAt: string | null;
  attachedAgents: string[];
  enabled: boolean;
};

export type SynonBiomedMcpServer = {
  id: string;
  name: string;
  displayName: string;
  description: string;
  description_i18n?: Record<string, string>;
  source: string;
  authRequired: boolean;
  oauthSupported: boolean;
  authState: string;
  authHint: string;
  apiKeyConfigurable: boolean;
  apiKeyConfigured: boolean;
  apiKeyLabel: string;
  transport: string;
  upstreams: unknown[];
  hostedBySynon: boolean;
  health: { ok: boolean };
  attachedAgents: string[];
  enabled: boolean;
  connectionStatus: string;
  usage?: SynonBiomedMcpUsage;
};

export type SynonBiomedMcpUsage = {
  invocationCount: number;
  lastUsedAt: string | null;
};

export type SynonBiomedMcpDirectoryHealth = {
  ok: boolean;
  detail: string;
};

export type SynonBiomedOptionalMcp = {
  id: string;
  name: string;
  displayName: string;
  description: string;
  category: string;
  license: string;
  repositoryUrl: string;
  installKind: string;
  installLabel: string;
  sizeLabel: string;
  requirements: string[];
  sourceRef: string;
  status: 'not-installed' | 'installing' | 'installed' | 'failed' | 'broken' | string;
  installed: boolean;
  installPath: string;
  error: string;
};

export type SynonBiomedMcpAuthorization = {
  authorizationUrl: string | null;
};

export type SynonBiomedMcpToolPermissionState = 'allow' | 'ask' | 'deny';

export type SynonBiomedMcpToolPermission = {
  toolName: string;
  title: string | null;
  description: string;
  readOnlyHint: boolean;
  state: SynonBiomedMcpToolPermissionState;
};

export type SynonBiomedMcpToolPermissions = {
  tools: SynonBiomedMcpToolPermission[];
  skipApprovalsActive: boolean;
};

export type SynonBiomedCustomMcpServer = {
  id: string;
  name: string;
  description: string | null;
  url: string;
  transport: 'sse' | 'streamable_http';
  oauthServerUrl: string | null;
  clientId: string | null;
  scopes: string | null;
  headersHelper: string | null;
  connected: boolean;
  attachedAgents: string[];
  createdAt: string | number | null;
  updatedAt: string | number | null;
};

export type SynonBiomedCustomMcpServerInput = {
  name: string;
  description?: string | null;
  url: string;
  transport: 'sse' | 'streamable_http';
  oauthServerUrl?: string | null;
  clientId?: string | null;
  scopes?: string | null;
  headersHelper?: string | null;
};

export class SynonBiomedCapabilityError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = 'SynonBiomedCapabilityError';
    this.status = status;
  }
}

type RecordValue = Record<string, unknown>;

type SkillsPayload = {
  skills?: unknown;
};

const CAPABILITY_REQUEST_TIMEOUT_MS = 15_000;
const SYNON_AI_ASSISTANT_SORT_BASE = 10000;

export async function loadSynonBiomedAgents(options: SynonBiomedCapabilityOptions = {}): Promise<SynonBiomedAgent[]> {
  const payload = await getJson<unknown>('/api/synonbiomed/expert-profiles', options);
  const payloadRecord = asRecord(payload);
  const agents = Array.isArray(payload) ? payload : Array.isArray(payloadRecord?.data) ? payloadRecord.data : [];

  return agents.map(toAgent).filter((agent): agent is SynonBiomedAgent => agent !== null && agent.userHidden === false);
}

export async function loadSynonBiomedSkills(options: SynonBiomedCapabilityOptions = {}): Promise<SynonBiomedSkill[]> {
  const payload = await getJson<SkillsPayload>('/api/skills/catalog', options);
  const skills = Array.isArray(payload.skills) ? payload.skills : [];

  const normalizedSkills: SynonBiomedSkill[] = [];
  for (const value of skills) {
    const skill = toSkill(value);
    if (!skill) continue;
    normalizedSkills.push(skill);
  }
  return normalizedSkills;
}

export async function setSynonBiomedSkillEnabled(
  name: string,
  enabled: boolean,
  options: SynonBiomedCapabilityOptions = {}
): Promise<void> {
  await requestJson(`/api/skills/catalog/${encodeURIComponent(name)}/enabled`, options, {
    method: 'PUT',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    body: JSON.stringify({ enabled }),
  });
}

export async function loadSynonBiomedMcpServers(
  options: SynonBiomedCapabilityOptions = {}
): Promise<SynonBiomedMcpServer[]> {
  const payload = await getJson<unknown>('/api/mcp-servers/connectors', options);
  const servers = Array.isArray(payload) ? payload : [];

  return servers.map(toMcpServer).filter((server): server is SynonBiomedMcpServer => server !== null);
}

export async function loadSynonBiomedOptionalMcps(
  options: SynonBiomedCapabilityOptions = {}
): Promise<SynonBiomedOptionalMcp[]> {
  const payload = await getJson<unknown>('/api/mcp-servers/optional', options);
  const items = Array.isArray(payload) ? payload : [];
  return items.map(toOptionalMcp).filter((item): item is SynonBiomedOptionalMcp => item !== null);
}

export async function startSynonBiomedOptionalMcpInstall(
  id: string,
  options: SynonBiomedCapabilityOptions = {}
): Promise<SynonBiomedOptionalMcp> {
  return requireOptionalMcp(
    await requestJson<unknown>(`/api/mcp-servers/optional/${encodeURIComponent(id)}/install`, options, {
      method: 'POST',
      headers: { Accept: 'application/json' },
    })
  );
}

export async function loadSynonBiomedCustomMcpServers(
  options: SynonBiomedCapabilityOptions = {}
): Promise<SynonBiomedCustomMcpServer[]> {
  const payload = await getJson<unknown>('/api/mcp-servers', options);
  const servers = Array.isArray(payload) ? payload : [];
  return servers.map(toCustomMcpServer).filter((server): server is SynonBiomedCustomMcpServer => server !== null);
}

export async function createSynonBiomedCustomMcpServer(
  input: SynonBiomedCustomMcpServerInput,
  options: SynonBiomedCapabilityOptions = {}
): Promise<SynonBiomedCustomMcpServer> {
  return requireCustomMcpServer(
    await requestJson<unknown>('/api/mcp-servers', options, jsonRequest('POST', toCustomMcpPayload(input)))
  );
}

export async function updateSynonBiomedCustomMcpServer(
  serverId: string,
  input: SynonBiomedCustomMcpServerInput,
  options: SynonBiomedCapabilityOptions = {}
): Promise<SynonBiomedCustomMcpServer> {
  return requireCustomMcpServer(
    await requestJson<unknown>(
      `/api/mcp-servers/${encodeURIComponent(serverId)}`,
      options,
      jsonRequest('PATCH', toCustomMcpPayload(input))
    )
  );
}

export async function deleteSynonBiomedCustomMcpServer(
  serverId: string,
  options: SynonBiomedCapabilityOptions = {}
): Promise<void> {
  await requestJson(`/api/mcp-servers/${encodeURIComponent(serverId)}`, options, { method: 'DELETE' });
}

export async function attachSynonBiomedMcpServerToAllAgents(
  serverId: string,
  options: SynonBiomedCapabilityOptions = {}
): Promise<unknown> {
  return requestJson(`/api/mcp-servers/${encodeURIComponent(serverId)}/attach-all`, options, { method: 'POST' });
}

export async function detachSynonBiomedMcpServerFromAllAgents(
  serverId: string,
  options: SynonBiomedCapabilityOptions = {}
): Promise<unknown> {
  return requestJson(`/api/mcp-servers/${encodeURIComponent(serverId)}/detach-all`, options, { method: 'DELETE' });
}

export async function loadSynonBiomedMcpDirectoryHealth(
  options: SynonBiomedCapabilityOptions = {}
): Promise<SynonBiomedMcpDirectoryHealth> {
  const payload = asRecord(await getJson<unknown>('/api/mcp-servers/directory-health', options));
  const health = asRecord(payload?.directoryHealth);
  return {
    ok: booleanValue(health?.ok),
    detail: stringValue(health?.detail),
  };
}

export async function setSynonBiomedMcpConnectorEnabled(
  serverId: string,
  enabled: boolean,
  options: SynonBiomedCapabilityOptions = {}
): Promise<unknown> {
  return requestJson(`/api/mcp-servers/connectors/${encodeURIComponent(serverId)}/enabled`, options, {
    method: 'PUT',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    body: JSON.stringify({ enabled }),
  });
}

export async function reconcileSynonBiomedMcpServers(
  options: SynonBiomedCapabilityOptions = {}
): Promise<Record<string, unknown>> {
  const payload = await requestJson<unknown>('/api/mcp-servers/reconcile', options, {
    method: 'POST',
    headers: { Accept: 'application/json' },
  });
  return asRecord(payload) ?? {};
}

export async function authorizeSynonBiomedMcpConnector(
  serverId: string,
  options: SynonBiomedCapabilityOptions = {}
): Promise<SynonBiomedMcpAuthorization> {
  const payload = asRecord(
    await requestJson<unknown>(`/api/mcp-servers/connectors/${encodeURIComponent(serverId)}/authorize`, options, {
      method: 'POST',
      headers: { Accept: 'application/json' },
    })
  );
  return {
    authorizationUrl: nullableStringValue(payload?.authorizationUrl ?? payload?.authorization_url),
  };
}

export async function configureSynonBiomedMcpConnectorAPIKey(
  serverId: string,
  apiKey: string,
  options: SynonBiomedCapabilityOptions = {}
): Promise<unknown> {
  return requestJson(`/api/mcp-servers/connectors/${encodeURIComponent(serverId)}/credential`, options, {
    method: 'PUT',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    body: JSON.stringify({ apiKey }),
  });
}

export async function disconnectSynonBiomedMcpConnector(
  serverId: string,
  options: SynonBiomedCapabilityOptions = {}
): Promise<unknown> {
  return requestJson(`/api/mcp-servers/connectors/${encodeURIComponent(serverId)}/disconnect`, options, {
    method: 'POST',
    headers: { Accept: 'application/json' },
  });
}

export async function loadSynonBiomedMcpToolPermissions(
  serverId: string,
  options: SynonBiomedCapabilityOptions = {}
): Promise<SynonBiomedMcpToolPermissions> {
  const payload = asRecord(
    await getJson<unknown>(`/api/mcp-servers/${encodeURIComponent(serverId)}/tool-permissions`, options)
  );
  const tools = Array.isArray(payload?.tools) ? payload.tools : [];
  return {
    tools: tools.map(toMcpToolPermission).filter((tool): tool is SynonBiomedMcpToolPermission => tool !== null),
    skipApprovalsActive: booleanValue(payload?.skipApprovalsActive),
  };
}

export async function updateSynonBiomedMcpToolPermission(
  serverId: string,
  toolName: string,
  state: SynonBiomedMcpToolPermissionState,
  options: SynonBiomedCapabilityOptions = {}
): Promise<unknown> {
  return requestJson(`/api/mcp-servers/${encodeURIComponent(serverId)}/tool-grants`, options, {
    method: 'POST',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    body: JSON.stringify({ toolName, decision: state === 'ask' ? null : state }),
  });
}

export function toSynonAIAssistant(agent: SynonBiomedAgent, index: number): Assistant {
  const status = agent.healthy ? 'online' : 'offline';
  const source = agent.source === 'user' ? 'user' : 'builtin';
  const fallbackDisplayName = resolveAgentDisplayName(agent.name, agent.displayName);
  const builtinText = source === 'builtin' ? resolveSynonBiomedBuiltinExpertText(agent.name) : null;
  const localizedName = {
    'en-US': builtinText?.['en-US']?.displayName ?? fallbackDisplayName,
    'zh-CN': builtinText?.['zh-CN']?.displayName ?? fallbackDisplayName,
  };
  const localizedDescription = {
    'en-US': builtinText?.['en-US']?.description ?? (agent.description || localizedName['en-US']),
    'zh-CN': builtinText?.['zh-CN']?.description ?? (agent.description || localizedName['zh-CN']),
  };

  return {
    id: `synonbiomed:${toAssistantSlug(agent.name)}`,
    source,
    name: localizedName['en-US'],
    name_i18n: localizedName,
    description: localizedDescription['en-US'],
    description_i18n: localizedDescription,
    enabled: agent.enabled,
    sort_order: SYNON_AI_ASSISTANT_SORT_BASE + index,
    agent_id: agent.name,
    agent: {
      type: 'synonbiomed',
      source: source === 'user' ? 'custom' : 'builtin',
      acp_backend: 'synonbiomed',
    },
    enabled_skills: agent.skillNames,
    custom_skill_names: [],
    disabled_builtin_skills: [],
    context: localizedDescription['en-US'],
    context_i18n: localizedDescription,
    prompts: [],
    prompts_i18n: {},
    models: [],
    agent_status: status,
    agent_status_message: agent.healthy ? undefined : 'Synon Biomed expert is not healthy.',
    deletable: source === 'user',
  };
}

async function getJson<T>(path: string, options: SynonBiomedCapabilityOptions): Promise<T> {
  return requestJson<T>(path, options, { headers: { Accept: 'application/json' } });
}

async function requestJson<T>(path: string, options: SynonBiomedCapabilityOptions, init: RequestInit): Promise<T> {
  try {
    return await requestSynonBiomedJson<T>(path, init, {
      baseUrl: options.baseUrl,
      fetchImpl: options.fetchImpl,
      timeoutMs: CAPABILITY_REQUEST_TIMEOUT_MS,
    });
  } catch (error) {
    if (isSynonBiomedHttpError(error)) {
      throw new SynonBiomedCapabilityError(
        error.status,
        error.backendMessage || `Synon Biomed capability request failed with HTTP ${error.status}`
      );
    }
    throw error;
  }
}

function toAgent(value: unknown): SynonBiomedAgent | null {
  const record = asRecord(value);
  const managedAgentId = stringValue(record?.id);
  const name = stringValue(record?.name) || managedAgentId;
  if (!name) return null;

  const status = stringValue(record?.status);
  const rawDisplayName = stringValue(record?.displayName) || stringValue(record?.name) || name;

  return {
    name,
    displayName: resolveAgentDisplayName(name, rawDisplayName),
    description: stringValue(record?.description),
    healthy: booleanValue(record?.healthy) || status === 'online' || status === 'healthy',
    enabled: record?.enabled !== false,
    source: stringValue(record?.source) || stringValue(record?.agent_source) || 'unknown',
    skillsLocked: booleanValue(record?.skillsLocked),
    unrestricted: booleanValue(record?.unrestricted),
    supportsPlanMode: booleanValue(record?.supportsPlanMode),
    userHidden: booleanValue(record?.userHidden),
    skillNames: stringArray(record?.skillNames),
  };
}

function resolveAgentDisplayName(name: string, value: string): string {
  if (name.trim().toUpperCase() === 'OPERON') return 'General Research Assistant';
  return value;
}

function toSkill(value: unknown): SynonBiomedSkill | null {
  const record = asRecord(value);
  const name = stringValue(record?.name);
  const skillId = stringValue(record?.skillId);
  if (!name || !skillId) return null;

  return {
    name,
    displayName: stringValue(record?.displayName) || name,
    description: stringValue(record?.description),
    description_i18n: stringRecord(record?.description_i18n ?? record?.descriptionI18n),
    source: stringValue(record?.source) || 'unknown',
    category: nullableStringValue(record?.category),
    license: nullableStringValue(record?.license),
    skillId,
    updatedAt: nullableStringValue(record?.updatedAt),
    attachedAgents: stringArray(record?.attachedAgents),
    enabled: record?.enabled !== false,
  };
}

function toMcpServer(value: unknown): SynonBiomedMcpServer | null {
  const record = asRecord(value);
  const id = stringValue(record?.id);
  const name = stringValue(record?.name);
  if (!id || !name) return null;

  return {
    id,
    name,
    displayName: stringValue(record?.displayName) || name,
    description: stringValue(record?.description),
    description_i18n: stringRecord(record?.description_i18n ?? record?.descriptionI18n),
    source: stringValue(record?.source) || 'unknown',
    authRequired: booleanValue(record?.authRequired),
    oauthSupported: booleanValue(record?.oauthSupported),
    authState: stringValue(record?.authState) || 'unknown',
    authHint: stringValue(record?.authHint),
    apiKeyConfigurable: booleanValue(record?.apiKeyConfigurable),
    apiKeyConfigured: booleanValue(record?.apiKeyConfigured),
    apiKeyLabel: stringValue(record?.apiKeyLabel) || 'API Key',
    transport: stringValue(record?.transport) || 'unknown',
    upstreams: Array.isArray(record?.upstreams) ? record.upstreams : [],
    hostedBySynon: booleanValue(record?.hostedBySynon),
    health: {
      ok: booleanValue(asRecord(record?.health)?.ok),
    },
    attachedAgents: stringArray(record?.attachedAgents),
    enabled: record?.enabled !== false,
    connectionStatus: stringValue(record?.connectionStatus) || 'unknown',
    usage: toMcpUsage(record?.usage ?? record),
  };
}

function toOptionalMcp(value: unknown): SynonBiomedOptionalMcp | null {
  const record = asRecord(value);
  const id = stringValue(record?.id);
  const name = stringValue(record?.name);
  if (!id || !name) return null;
  return {
    id,
    name,
    displayName: stringValue(record?.displayName) || name,
    description: stringValue(record?.description),
    category: stringValue(record?.category),
    license: stringValue(record?.license),
    repositoryUrl: stringValue(record?.repositoryUrl),
    installKind: stringValue(record?.installKind),
    installLabel: stringValue(record?.installLabel),
    sizeLabel: stringValue(record?.sizeLabel),
    requirements: stringArray(record?.requirements),
    sourceRef: stringValue(record?.sourceRef),
    status: stringValue(record?.status) || 'not-installed',
    installed: booleanValue(record?.installed),
    installPath: stringValue(record?.installPath),
    error: stringValue(record?.error),
  };
}

function requireOptionalMcp(value: unknown): SynonBiomedOptionalMcp {
  const item = toOptionalMcp(value);
  if (!item) throw new Error('Synon Biomed returned an invalid optional MCP response.');
  return item;
}

function toMcpUsage(value: unknown): SynonBiomedMcpUsage | undefined {
  const record = asRecord(value);
  if (!record) return undefined;
  const rawCount = record.invocationCount ?? record.callCount ?? record.totalCalls;
  const invocationCount =
    typeof rawCount === 'number' && Number.isSafeInteger(rawCount) && rawCount >= 0 ? rawCount : undefined;
  const rawLastUsedAt = record.lastUsedAt ?? record.lastCallAt ?? record.last_invoked_at;
  const lastUsedAt =
    typeof rawLastUsedAt === 'string' && rawLastUsedAt.trim() && Number.isFinite(Date.parse(rawLastUsedAt))
      ? rawLastUsedAt
      : null;
  if (invocationCount === undefined && lastUsedAt === null) return undefined;
  return { invocationCount: invocationCount ?? 0, lastUsedAt };
}

function toMcpToolPermission(value: unknown): SynonBiomedMcpToolPermission | null {
  const record = asRecord(value);
  const toolName = stringValue(record?.toolName);
  const rawState = record?.state;
  const state = rawState === null || rawState === undefined ? 'ask' : stringValue(rawState);
  if (!toolName || !isMcpToolPermissionState(state)) return null;

  return {
    toolName,
    title: nullableStringValue(record?.title),
    description: stringValue(record?.description),
    readOnlyHint: booleanValue(record?.readOnlyHint),
    state,
  };
}

function toCustomMcpServer(value: unknown): SynonBiomedCustomMcpServer | null {
  const record = asRecord(value);
  const id = stringValue(record?.id);
  const name = stringValue(record?.name);
  const transport = stringValue(record?.transport);
  if (!id || !name || (transport !== 'sse' && transport !== 'streamable_http')) return null;
  return {
    id,
    name,
    description: nullableStringValue(record?.description),
    url: stringValue(record?.url),
    transport,
    oauthServerUrl: nullableStringValue(record?.oauth_server_url),
    clientId: nullableStringValue(record?.client_id),
    scopes: nullableStringValue(record?.scopes),
    headersHelper: nullableStringValue(record?.headers_helper),
    connected: booleanValue(record?.is_connected),
    attachedAgents: stringArray(record?.attached_agents),
    createdAt: scalarValue(record?.created_at),
    updatedAt: scalarValue(record?.updated_at),
  };
}

function requireCustomMcpServer(value: unknown): SynonBiomedCustomMcpServer {
  const server = toCustomMcpServer(value);
  if (!server) throw new Error('Synon Biomed returned an invalid MCP server response.');
  return server;
}

function toCustomMcpPayload(input: SynonBiomedCustomMcpServerInput): Record<string, unknown> {
  return {
    name: input.name,
    description: input.description || null,
    url: input.url,
    transport: input.transport,
    oauth_server_url: input.oauthServerUrl || null,
    client_id: input.clientId || null,
    scopes: input.scopes || null,
    headers_helper: input.headersHelper || null,
  };
}

function jsonRequest(method: string, payload: unknown): RequestInit {
  return {
    method,
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  };
}

function isMcpToolPermissionState(value: string): value is SynonBiomedMcpToolPermissionState {
  return value === 'allow' || value === 'ask' || value === 'deny';
}

function toAssistantSlug(agentName: string): string {
  return agentName
    .trim()
    .toLowerCase()
    .replace(/[_\s]+/g, '-')
    .replace(/[^a-z0-9-]/g, '')
    .replace(/-+/g, '-')
    .replace(/^-|-$/g, '');
}

function asRecord(value: unknown): RecordValue | null {
  return value && typeof value === 'object' && !Array.isArray(value) ? (value as RecordValue) : null;
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value : '';
}

function stringRecord(value: unknown): Record<string, string> | undefined {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return undefined;
  const entries = Object.entries(value).filter((entry): entry is [string, string] => typeof entry[1] === 'string');
  return entries.length > 0 ? Object.fromEntries(entries) : undefined;
}

function nullableStringValue(value: unknown): string | null {
  const string = stringValue(value);
  return string || null;
}

function booleanValue(value: unknown): boolean {
  return value === true;
}

function scalarValue(value: unknown): string | number | null {
  return typeof value === 'string' || typeof value === 'number' ? value : null;
}

function stringArray(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string') : [];
}

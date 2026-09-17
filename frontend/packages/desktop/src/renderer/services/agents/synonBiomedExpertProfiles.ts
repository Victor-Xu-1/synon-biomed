import { requestSynonBiomedJson, type SynonBiomedGatewayOptions } from '../synonBiomedHttp';

const EXPERT_PROFILES_PATH = '/api/synonbiomed/expert-profiles';

export type SynonBiomedExpertProfile = {
  id?: string;
  name: string;
  source: 'bundled' | 'user' | string;
  displayName: string;
  description: string;
  systemPrompt: string;
  iconKey: string;
  colorKey: string;
  enabled: boolean;
  userHidden: boolean;
  unrestricted: boolean;
  skillNames: string[];
  connectorIds?: string[];
};

export type SynonBiomedExpertProfileInput = {
  name: string;
  displayName: string;
  description: string;
  systemPrompt: string;
  enabled: boolean;
};

export async function loadSynonBiomedExpertProfiles(
  options: SynonBiomedGatewayOptions = {}
): Promise<SynonBiomedExpertProfile[]> {
  const payload = await requestSynonBiomedJson<unknown[]>(EXPERT_PROFILES_PATH, { method: 'GET' }, options);
  return Array.isArray(payload)
    ? payload
        .map(toExpertProfile)
        .filter(isPresent)
        .filter((profile) => !profile.userHidden)
    : [];
}

export async function loadSynonBiomedExpertRuntimeConnectorIds(
  name: string,
  options: SynonBiomedGatewayOptions = {}
): Promise<string[]> {
  const payload = await requestSynonBiomedJson<unknown>(
    `/api/agents/${encodeURIComponent(name)}/mcp-servers?include_tools=false`,
    { method: 'GET' },
    options
  );
  if (!Array.isArray(payload)) return [];
  return payload
    .map((value) => (value && typeof value === 'object' ? stringValue((value as Record<string, unknown>).id) : ''))
    .filter((id): id is string => Boolean(id));
}

export async function loadSynonBiomedExpertProfilesWithRuntimeConnectors(
  options: SynonBiomedGatewayOptions = {}
): Promise<SynonBiomedExpertProfile[]> {
  const profiles = await loadSynonBiomedExpertProfiles(options);
  return Promise.all(profiles.map((profile) => enrichSynonBiomedExpertProfile(profile, options)));
}

async function enrichSynonBiomedExpertProfile(
  profile: SynonBiomedExpertProfile,
  options: SynonBiomedGatewayOptions
): Promise<SynonBiomedExpertProfile> {
  if (profile.source !== 'user') return profile;
  try {
    return {
      ...profile,
      connectorIds: await loadSynonBiomedExpertRuntimeConnectorIds(profile.name, options),
    };
  } catch (error) {
    console.error(`Failed to load runtime connectors for expert ${profile.name}:`, error);
    return profile;
  }
}

export async function createSynonBiomedExpertProfile(
  input: SynonBiomedExpertProfileInput,
  options: SynonBiomedGatewayOptions = {}
): Promise<SynonBiomedExpertProfile> {
  const payload = await requestSynonBiomedJson<unknown>(
    EXPERT_PROFILES_PATH,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(input),
    },
    options
  );
  const profile = toExpertProfile(payload);
  if (!profile) throw new Error('Synon Biomed returned an invalid expert profile');
  return profile;
}

export async function updateSynonBiomedExpertProfile(
  currentName: string,
  input: SynonBiomedExpertProfileInput,
  options: SynonBiomedGatewayOptions = {}
): Promise<SynonBiomedExpertProfile> {
  const payload = await requestSynonBiomedJson<unknown>(
    `${EXPERT_PROFILES_PATH}/${encodeURIComponent(currentName)}`,
    {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: input.name,
        displayName: input.displayName,
        description: input.description,
        systemPrompt: input.systemPrompt,
      }),
    },
    options
  );
  const profile = toExpertProfile(payload);
  if (!profile) throw new Error('Synon Biomed returned an invalid expert profile');
  if (profile.enabled !== input.enabled) {
    return setSynonBiomedExpertProfileEnabled(profile.name, input.enabled, options);
  }
  return profile;
}

export async function setSynonBiomedExpertProfileEnabled(
  name: string,
  enabled: boolean,
  options: SynonBiomedGatewayOptions = {}
): Promise<SynonBiomedExpertProfile> {
  const payload = await requestSynonBiomedJson<unknown>(
    `${EXPERT_PROFILES_PATH}/${encodeURIComponent(name)}/enabled`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ enabled }),
    },
    options
  );
  const profile = toExpertProfile(payload);
  if (!profile) throw new Error('Synon Biomed returned an invalid expert profile');
  return profile;
}

export async function deleteSynonBiomedExpertProfile(
  name: string,
  options: SynonBiomedGatewayOptions = {}
): Promise<void> {
  await requestSynonBiomedJson(`${EXPERT_PROFILES_PATH}/${encodeURIComponent(name)}`, { method: 'DELETE' }, options);
}

export async function loadSynonBiomedExpertInstructions(
  profile: SynonBiomedExpertProfile,
  options: SynonBiomedGatewayOptions = {}
): Promise<string> {
  if (profile.source === 'user') return profile.systemPrompt;
  const payload = await requestSynonBiomedJson<unknown>(
    `${EXPERT_PROFILES_PATH}/${encodeURIComponent(profile.name)}/custom-prompt`,
    { method: 'GET' },
    options
  );
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) return '';
  return stringValue((payload as Record<string, unknown>).prompt_text);
}

export async function saveSynonBiomedExpertInstructions(
  profile: SynonBiomedExpertProfile,
  instructions: string,
  options: SynonBiomedGatewayOptions = {}
): Promise<void> {
  if (profile.source === 'user') {
    await updateSynonBiomedExpertProfile(
      profile.name,
      {
        name: profile.name,
        displayName: profile.displayName,
        description: profile.description,
        systemPrompt: instructions,
        enabled: profile.enabled,
      },
      options
    );
    return;
  }

  const path = `${EXPERT_PROFILES_PATH}/${encodeURIComponent(profile.name)}/custom-prompt`;
  if (!instructions.trim()) {
    await requestSynonBiomedJson(path, { method: 'DELETE' }, options);
    return;
  }
  await requestSynonBiomedJson(
    path,
    {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ prompt_text: instructions }),
    },
    options
  );
}

export async function updateSynonBiomedExpertSkills(
  name: string,
  currentSkillNames: string[],
  nextSkillNames: string[],
  options: SynonBiomedGatewayOptions = {}
): Promise<SynonBiomedExpertProfile> {
  const current = new Set(currentSkillNames);
  const next = new Set(nextSkillNames);
  const payload = await requestSynonBiomedJson<unknown>(
    `${EXPERT_PROFILES_PATH}/${encodeURIComponent(name)}/skills`,
    {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        attach: nextSkillNames.filter((skillName) => !current.has(skillName)),
        detach: currentSkillNames.filter((skillName) => !next.has(skillName)),
      }),
    },
    options
  );
  const profile = toExpertProfile(payload);
  if (!profile) throw new Error('Synon Biomed returned an invalid expert capability profile');
  return profile;
}

export async function attachSynonBiomedExpertConnector(
  name: string,
  serverId: string,
  options: SynonBiomedGatewayOptions = {}
): Promise<void> {
  await requestSynonBiomedJson(
    `${EXPERT_PROFILES_PATH}/${encodeURIComponent(name)}/connectors`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ server_id: serverId }),
    },
    options
  );
}

export async function detachSynonBiomedExpertConnector(
  name: string,
  serverId: string,
  options: SynonBiomedGatewayOptions = {}
): Promise<void> {
  await requestSynonBiomedJson(
    `${EXPERT_PROFILES_PATH}/${encodeURIComponent(name)}/connectors/${encodeURIComponent(serverId)}`,
    { method: 'DELETE' },
    options
  );
}

export function normalizeSynonBiomedExpertName(value: string): string {
  return value
    .trim()
    .replace(/[^a-zA-Z0-9]+/gu, '_')
    .replace(/^_+|_+$/gu, '')
    .toUpperCase()
    .slice(0, 32);
}

function toExpertProfile(value: unknown): SynonBiomedExpertProfile | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const record = value as Record<string, unknown>;
  const name = stringValue(record.name);
  if (!name) return null;
  return {
    id: stringValue(record.id) || undefined,
    name,
    source: stringValue(record.source) || 'bundled',
    displayName: stringValue(record.displayName) || name,
    description: stringValue(record.description),
    systemPrompt: stringValue(record.systemPrompt),
    iconKey: stringValue(record.iconKey),
    colorKey: stringValue(record.colorKey),
    enabled: record.enabled !== false,
    userHidden: record.userHidden === true,
    unrestricted: record.unrestricted === true,
    skillNames: Array.isArray(record.skillNames)
      ? record.skillNames.filter((item): item is string => typeof item === 'string')
      : [],
    connectorIds: Array.isArray(record.connectorIds)
      ? record.connectorIds.filter((item): item is string => typeof item === 'string')
      : [],
  };
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value : '';
}

function isPresent<T>(value: T | null): value is T {
  return value !== null;
}

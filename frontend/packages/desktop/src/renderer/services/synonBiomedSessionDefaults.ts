import { loadSynonBiomedLlmModels, type SynonBiomedLlmModelsSnapshot } from '@/renderer/services/synonBiomedLlm';

export type SynonBiomedSessionDefaults = {
  defaultModelId?: string;
  subagentModelId?: string;
  effort?: 'low' | 'medium' | 'high';
};

export type SynonBiomedSessionDefaultsSnapshot = {
  defaults: SynonBiomedSessionDefaults;
  models: SynonBiomedLlmModelsSnapshot;
};

type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

const SETTINGS_PATH = '/api/settings/client';
const SETTING_KEYS = {
  defaultModelId: 'synonBiomed.session.defaultModelId',
  subagentModelId: 'synonBiomed.session.subagentModelId',
  effort: 'synonBiomed.session.effort',
} as const;
const EFFORT_VALUES = new Set<SynonBiomedSessionDefaults['effort']>(['low', 'medium', 'high']);

/** Load global session defaults and discard model IDs that are no longer available. */
export async function loadSynonBiomedSessionDefaults(
  fetchImpl: FetchLike = fetch
): Promise<SynonBiomedSessionDefaults> {
  return (await loadSynonBiomedSessionDefaultsSnapshot(fetchImpl)).defaults;
}

export async function loadSynonBiomedSessionDefaultsSnapshot(
  fetchImpl: FetchLike = fetch
): Promise<SynonBiomedSessionDefaultsSnapshot> {
  const settingsPath = `${SETTINGS_PATH}?keys=${encodeURIComponent(Object.values(SETTING_KEYS).join(','))}`;
  const [payload, models] = await Promise.all([
    requestJson<unknown>(settingsPath, undefined, fetchImpl),
    loadSynonBiomedLlmModels(fetchImpl),
  ]);
  const settings = unwrapSettings(payload);
  const availableModelIds = new Set(models.models.map((model) => model.id));
  return {
    defaults: compactDefaults({
      defaultModelId: validModelId(settings[SETTING_KEYS.defaultModelId], availableModelIds),
      subagentModelId: validModelId(settings[SETTING_KEYS.subagentModelId], availableModelIds),
      effort: validEffort(settings[SETTING_KEYS.effort]),
    }),
    models,
  };
}

/** Persist global session defaults; undefined values remove their backend setting. */
export async function saveSynonBiomedSessionDefaults(
  next: SynonBiomedSessionDefaults,
  fetchImpl: FetchLike = fetch
): Promise<void> {
  const normalized = compactDefaults({
    defaultModelId: optionalString(next.defaultModelId),
    subagentModelId: optionalString(next.subagentModelId),
    effort: validEffort(next.effort),
  });

  await requestJson<unknown>(
    SETTINGS_PATH,
    {
      method: 'PUT',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({
        [SETTING_KEYS.defaultModelId]: normalized.defaultModelId ?? null,
        [SETTING_KEYS.subagentModelId]: normalized.subagentModelId ?? null,
        [SETTING_KEYS.effort]: normalized.effort ?? null,
      }),
    },
    fetchImpl
  );
}

async function requestJson<T>(path: string, init: RequestInit | undefined, fetchImpl: FetchLike): Promise<T> {
  const response = await fetchImpl(path, {
    ...init,
    headers: {
      Accept: 'application/json',
      ...init?.headers,
    },
  });
  if (!response.ok) {
    const detail = await response.text().catch(() => '');
    throw new Error(`Client settings request failed: ${response.status}${detail ? ` ${detail}` : ''}`);
  }
  return response.json() as Promise<T>;
}

function unwrapSettings(payload: unknown): Record<string, unknown> {
  if (!isRecord(payload)) return {};
  return isRecord(payload.data) ? payload.data : payload;
}

function validModelId(value: unknown, availableModelIds: ReadonlySet<string>): string | undefined {
  const modelId = optionalString(value);
  return modelId && availableModelIds.has(modelId) ? modelId : undefined;
}

function validEffort(value: unknown): SynonBiomedSessionDefaults['effort'] {
  return typeof value === 'string' && EFFORT_VALUES.has(value as SynonBiomedSessionDefaults['effort'])
    ? (value as SynonBiomedSessionDefaults['effort'])
    : undefined;
}

function optionalString(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined;
}

function compactDefaults(next: SynonBiomedSessionDefaults): SynonBiomedSessionDefaults {
  return Object.fromEntries(Object.entries(next).filter(([, value]) => value !== undefined));
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

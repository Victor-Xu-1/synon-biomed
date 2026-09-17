import { requestSynonBiomedJson, type SynonBiomedGatewayOptions } from '../synonBiomedHttp';

export type SynonBiomedSkillUsage = {
  invocationCount: number;
  lastUsedAt: string | null;
};

export type SynonBiomedSkillUsageByName = Record<string, SynonBiomedSkillUsage>;

type RuntimeListEntry = {
  updatedAt?: unknown;
  updated_at?: unknown;
  value?: unknown;
};

export async function loadSynonBiomedSkillUsage(
  options: SynonBiomedGatewayOptions = {}
): Promise<SynonBiomedSkillUsageByName> {
  const payload = await requestSynonBiomedJson<unknown>(
    '/api/skills/usage',
    { method: 'GET' },
    { ...options, timeoutMs: options.timeoutMs ?? 10_000 }
  );

  return parseSkillUsageProjection(skillUsageRecords(payload));
}

export function findSynonBiomedSkillUsage(
  usageByName: SynonBiomedSkillUsageByName,
  skillName: string
): SynonBiomedSkillUsage | null {
  const normalizedName = skillName.trim().toLowerCase();
  if (!normalizedName) return null;

  for (const [name, usage] of Object.entries(usageByName)) {
    if (name.trim().toLowerCase() === normalizedName) return usage;
  }
  return null;
}

export function aggregateSynonBiomedSkillUsage(entries: unknown[]): SynonBiomedSkillUsageByName {
  const usageByName: SynonBiomedSkillUsageByName = {};

  for (const rawEntry of entries) {
    const entry = asRecord(rawEntry) as RuntimeListEntry | null;
    const value = asRecord(entry?.value);
    const name = stringValue(value?.skill ?? value?.skillName).trim();
    if (!name) continue;

    const usage = usageByName[name] ?? { invocationCount: 0, lastUsedAt: null };
    usage.invocationCount += 1;

    const timestamp = firstValidTimestamp(value?.createdAt, value?.created_at, entry?.updatedAt, entry?.updated_at);
    if (timestamp && (!usage.lastUsedAt || Date.parse(timestamp) > Date.parse(usage.lastUsedAt))) {
      usage.lastUsedAt = timestamp;
    }
    usageByName[name] = usage;
  }

  return usageByName;
}

function skillUsageRecords(payload: unknown): unknown[] {
  const root = asRecord(payload);
  const entries = root && Array.isArray(root.usage) ? root.usage : null;
  if (!entries) throw new Error('Synon Biomed skill usage response is invalid');
  return entries;
}

function parseSkillUsageProjection(entries: unknown[]): SynonBiomedSkillUsageByName {
  const usageByName: SynonBiomedSkillUsageByName = {};
  for (const rawEntry of entries) {
    const entry = asRecord(rawEntry);
    const name = stringValue(entry?.name).trim();
    const invocationCount = entry?.invocationCount;
    if (!name || typeof invocationCount !== 'number' || !Number.isSafeInteger(invocationCount) || invocationCount < 0) {
      throw new Error('Synon Biomed skill usage response is invalid');
    }
    const lastUsedAt = firstValidTimestamp(entry?.lastUsedAt);
    usageByName[name] = { invocationCount, lastUsedAt };
  }
  return usageByName;
}

function firstValidTimestamp(...values: unknown[]): string | null {
  for (const value of values) {
    if (typeof value !== 'string' || !value.trim()) continue;
    if (Number.isFinite(Date.parse(value))) return value;
  }
  return null;
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return value && typeof value === 'object' && !Array.isArray(value) ? (value as Record<string, unknown>) : null;
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value : '';
}

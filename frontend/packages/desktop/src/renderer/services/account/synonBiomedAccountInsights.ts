import { requestSynonBiomedJson, type SynonBiomedGatewayOptions } from '../synonBiomedHttp';
import type { AccountActivityDay, AccountTopSkill, SynonBiomedAccountInsights } from './accountInsightsModel';

const ACCOUNT_ACTIVITY_DAY_COUNT = 53 * 7;
const ACCOUNT_TOP_SKILL_LIMIT = 5;

export async function loadSynonBiomedAccountInsights(
  options: SynonBiomedGatewayOptions = {}
): Promise<SynonBiomedAccountInsights> {
  const utcOffsetMinutes = -new Date().getTimezoneOffset();
  const payload = await requestSynonBiomedJson<unknown>(
    `/api/account/overview?utc_offset_minutes=${utcOffsetMinutes}`,
    { method: 'GET' },
    { ...options, timeoutMs: options.timeoutMs ?? 10_000 }
  );
  return parseAccountOverview(payload);
}

export function parseAccountOverview(payload: unknown): SynonBiomedAccountInsights {
  const root = asRecord(payload);
  const metrics = asRecord(root?.metrics);
  const availability = asRecord(root?.availability);
  if (!root || !metrics || !availability) throw invalidOverview();

  const projectsAvailable = booleanValue(availability.projects);
  const skillsAvailable = booleanValue(availability.skills);
  const tokenUsageAvailable = booleanValue(availability.tokenUsage);
  const projectCount = optionalCount(metrics.projectCount, projectsAvailable);
  const artifactCount = optionalCount(metrics.artifactCount, projectsAvailable);
  const completionRate = optionalRate(metrics.completionRate);
  const totalTasks = countValue(metrics.totalTasks);
  const completedTasks = countValue(metrics.completedTasks);
  const currentStreak = countValue(metrics.currentStreak);
  const longestStreak = countValue(metrics.longestStreak);
  if (completedTasks > totalTasks || currentStreak > longestStreak) throw invalidOverview();
  if (
    (totalTasks === 0 && completionRate !== null) ||
    (totalTasks > 0 &&
      (completionRate === null || Math.abs(completionRate - completedTasks / totalTasks) > Number.EPSILON * 8))
  ) {
    throw invalidOverview();
  }
  const loadedAt = stringValue(root.loadedAt).trim();
  if (!loadedAt || !Number.isFinite(Date.parse(loadedAt))) throw invalidOverview();

  if (!Array.isArray(root.activityDays) || root.activityDays.length !== ACCOUNT_ACTIVITY_DAY_COUNT) {
    throw invalidOverview();
  }
  const activityDays = root.activityDays.map(parseActivityDay);
  for (let index = 1; index < activityDays.length; index += 1) {
    if (activityDays[index - 1].date >= activityDays[index].date) throw invalidOverview();
  }

  if (!Array.isArray(root.topSkills) || root.topSkills.length > ACCOUNT_TOP_SKILL_LIMIT) throw invalidOverview();
  const topSkills = root.topSkills.map(parseTopSkill);
  if (new Set(topSkills.map((skill) => skill.name)).size !== topSkills.length) throw invalidOverview();
  if (!skillsAvailable && topSkills.length > 0) throw invalidOverview();

  return {
    metrics: {
      totalTasks,
      completedTasks,
      projectCount,
      artifactCount,
      currentStreak,
      longestStreak,
      completionRate,
      recentTaskCount: countValue(metrics.recentTaskCount),
      activeDayCount: countValue(metrics.activeDayCount),
    },
    activityDays,
    topSkills,
    availability: {
      projects: projectsAvailable,
      skills: skillsAvailable,
      tokenUsage: tokenUsageAvailable,
    },
    loadedAt: new Date(loadedAt).toISOString(),
  };
}

function parseActivityDay(value: unknown): AccountActivityDay {
  const record = asRecord(value);
  const date = stringValue(record?.date).trim();
  if (!record || !/^\d{4}-\d{2}-\d{2}$/.test(date)) throw invalidOverview();
  return {
    date,
    count: countValue(record.count),
    tokenCount: countValue(record.tokenCount),
    isFuture: booleanValue(record.isFuture),
  };
}

function parseTopSkill(value: unknown): AccountTopSkill {
  const record = asRecord(value);
  const name = stringValue(record?.name).trim();
  if (!record || !name) throw invalidOverview();
  const invocationCount = countValue(record.invocationCount);
  const lastUsedAtValue = record.lastUsedAt;
  let lastUsedAt: string | null = null;
  if (lastUsedAtValue !== null && lastUsedAtValue !== undefined) {
    const timestamp = stringValue(lastUsedAtValue).trim();
    if (!timestamp || !Number.isFinite(Date.parse(timestamp))) throw invalidOverview();
    lastUsedAt = new Date(timestamp).toISOString();
  }
  return { name, invocationCount, lastUsedAt };
}

function optionalCount(value: unknown, available: boolean): number | null {
  if (value === null && !available) return null;
  return countValue(value);
}

function optionalRate(value: unknown): number | null {
  if (value === null) return null;
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0 || value > 1) throw invalidOverview();
  return value;
}

function countValue(value: unknown): number {
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0) throw invalidOverview();
  return value;
}

function booleanValue(value: unknown): boolean {
  if (typeof value !== 'boolean') throw invalidOverview();
  return value;
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return value && typeof value === 'object' && !Array.isArray(value) ? (value as Record<string, unknown>) : null;
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value : '';
}

function invalidOverview(): Error {
  return new Error('Synon Biomed account overview response is invalid');
}

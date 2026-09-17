import { requestSynonBiomedJson, type SynonBiomedGatewayOptions } from '../synonBiomedHttp';

export type SynonBiomedExpertUsage = {
  /** Number of root conversations started with this expert. */
  invocationCount: number;
  /** Latest activity time of those conversations, normalized to ISO 8601. */
  lastUsedAt: string | null;
};

export type SynonBiomedExpertUsageByName = Record<string, SynonBiomedExpertUsage>;

type ConversationPage = {
  items: unknown[];
  hasMore: boolean;
  nextCursor: string | null;
};

type ConversationRecord = {
  modified_at?: unknown;
  extra?: unknown;
  assistant?: unknown;
};

const CONVERSATION_PAGE_SIZE = 1000;
const MAX_CONVERSATION_USAGE_PAGES = 100;

/**
 * Builds the expert usage view from the existing conversation list contract.
 * This is intentionally read-only and keeps the usage projection in the UI;
 * the runtime remains the sole owner of conversation and expert execution.
 */
export async function loadSynonBiomedExpertUsage(
  options: SynonBiomedGatewayOptions = {}
): Promise<SynonBiomedExpertUsageByName> {
  const usageByName: SynonBiomedExpertUsageByName = {};
  let cursor: string | undefined;

  for (let pageNumber = 0; pageNumber < MAX_CONVERSATION_USAGE_PAGES; pageNumber += 1) {
    const query = new URLSearchParams({ limit: String(CONVERSATION_PAGE_SIZE) });
    if (cursor) query.set('cursor', cursor);

    // Cursor pagination is sequential by protocol; parallel page requests
    // cannot know the next cursor and would weaken the safety bound.
    // eslint-disable-next-line no-await-in-loop
    const payload = await requestSynonBiomedJson<unknown>(
      `/api/conversations?${query.toString()}`,
      { method: 'GET' },
      { ...options, timeoutMs: options.timeoutMs ?? 10_000 }
    );
    const page = parseConversationPage(payload);
    aggregateSynonBiomedExpertUsage(usageByName, page.items);

    if (!page.hasMore) return usageByName;
    if (!page.nextCursor || page.nextCursor === cursor) {
      throw new Error('Synon Biomed expert usage response contains an invalid pagination cursor');
    }
    cursor = page.nextCursor;
  }

  throw new Error('Synon Biomed expert usage exceeded the pagination safety limit');
}

export function findSynonBiomedExpertUsage(
  usageByName: SynonBiomedExpertUsageByName,
  expertName: string
): SynonBiomedExpertUsage | null {
  const normalizedName = expertName.trim().toLowerCase();
  if (!normalizedName) return null;

  for (const [name, usage] of Object.entries(usageByName)) {
    if (name.trim().toLowerCase() === normalizedName) return usage;
  }
  return null;
}

export function aggregateSynonBiomedExpertUsage(
  usageByName: SynonBiomedExpertUsageByName,
  entries: unknown[]
): SynonBiomedExpertUsageByName {
  for (const rawEntry of entries) {
    const entry = asRecord(rawEntry) as ConversationRecord | null;
    const name = expertNameFromConversation(entry);
    if (!name) continue;

    const usage = usageByName[name] ?? { invocationCount: 0, lastUsedAt: null };
    usage.invocationCount += 1;
    const lastUsedAt = normalizeTimestamp(entry?.modified_at);
    if (lastUsedAt && (!usage.lastUsedAt || Date.parse(lastUsedAt) > Date.parse(usage.lastUsedAt))) {
      usage.lastUsedAt = lastUsedAt;
    }
    usageByName[name] = usage;
  }

  return usageByName;
}

function parseConversationPage(payload: unknown): ConversationPage {
  const root = asRecord(payload);
  if (!root || !Array.isArray(root.items) || typeof root.has_more !== 'boolean') {
    throw new Error('Synon Biomed conversation list response is invalid');
  }

  const nextCursor = root.next_cursor;
  if (nextCursor !== null && nextCursor !== undefined && typeof nextCursor !== 'string') {
    throw new Error('Synon Biomed conversation list response is invalid');
  }

  return {
    items: root.items,
    hasMore: root.has_more,
    nextCursor: typeof nextCursor === 'string' && nextCursor.trim() ? nextCursor : null,
  };
}

function expertNameFromConversation(entry: ConversationRecord | null): string {
  const extra = asRecord(entry?.extra);
  const assistant = asRecord(entry?.assistant);
  return (stringValue(extra?.agent_name) || stringValue(assistant?.name)).trim();
}

function normalizeTimestamp(value: unknown): string | null {
  if (typeof value === 'number') {
    if (!Number.isFinite(value) || value < 0 || value > 8.64e15) return null;
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? null : date.toISOString();
  }
  if (typeof value === 'string' && value.trim()) {
    const parsed = Date.parse(value);
    if (Number.isFinite(parsed)) return new Date(parsed).toISOString();
  }
  return null;
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return value && typeof value === 'object' && !Array.isArray(value) ? (value as Record<string, unknown>) : null;
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value : '';
}

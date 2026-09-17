import { requestSynonBiomedJson } from '@/renderer/services/synonBiomedHttp';
import { getRendererAccountScopeToken, registerRendererAccountReset } from '@/renderer/services/rendererAccountScope';

export type RemoteToolDetailReference = {
  conversationId: string;
  messageId: string;
  branchId?: string;
  revision?: number;
  contentUrl?: string;
};

export type RemoteToolDetailNode = {
  path: string;
  kind: 'scalar' | 'object' | 'array';
  key?: string;
  index?: number;
  value?: string | number | boolean | null;
  total?: number;
  truncated?: boolean;
  originalBytes?: number;
};

export type RemoteToolDetailPage = {
  messageId: string;
  branchId: string;
  section: 'input' | 'output';
  path: string;
  revision: number;
  kind: 'scalar' | 'object' | 'array';
  total: number;
  from: number;
  items: RemoteToolDetailNode[];
  nextCursor: string | null;
};

const PAGE_CACHE_LIMIT = 256;
const pageCache = new Map<string, RemoteToolDetailPage>();
const UTF8_ENCODER = new TextEncoder();

export async function loadRemoteToolDetailPage(
  reference: RemoteToolDetailReference,
  options: { path: string; cursor?: string; signal?: AbortSignal }
): Promise<RemoteToolDetailPage> {
  const accountScope = getRendererAccountScopeToken();
  const cacheKey = `${accountScope}\0${reference.conversationId}\0${reference.messageId}\0${reference.branchId ?? ''}\0${reference.revision ?? 0}\0${options.path}\0${options.cursor ?? ''}`;
  const cached = pageCache.get(cacheKey);
  if (cached) return cached;
  const query = new URLSearchParams({ section: 'output', path: options.path, limit: '100' });
  if (reference.revision !== undefined) query.set('revision', String(reference.revision));
  if (reference.branchId) query.set('branch_id', reference.branchId);
  if (options.cursor) query.set('cursor', options.cursor);
  const payload = await requestSynonBiomedJson<unknown>(
    `/api/conversations/${encodeURIComponent(reference.conversationId)}/messages/${encodeURIComponent(reference.messageId)}/tool-detail?${query.toString()}`,
    { method: 'GET', signal: options.signal },
    { signal: options.signal }
  );
  if (getRendererAccountScopeToken() !== accountScope) {
    throw new DOMException('tool_detail_account_scope_changed', 'AbortError');
  }
  const page = normalizeRemoteToolDetailPage(payload, reference, options.path);
  pageCache.delete(cacheKey);
  pageCache.set(cacheKey, page);
  while (pageCache.size > PAGE_CACHE_LIMIT) {
    const oldest = pageCache.keys().next().value;
    if (typeof oldest !== 'string') break;
    pageCache.delete(oldest);
  }
  return page;
}

export function clearRemoteToolDetailCache(): void {
  pageCache.clear();
}

registerRendererAccountReset('conversation-tool-detail-pages', clearRemoteToolDetailCache);

function normalizeRemoteToolDetailPage(
  value: unknown,
  reference: RemoteToolDetailReference,
  expectedPath: string
): RemoteToolDetailPage {
  if (!isRecord(value)) throw invalidPage();
  const messageId = exactString(value.message_id);
  const branchId = exactString(value.branch_id);
  const section = exactString(value.section);
  const path = exactString(value.path, true);
  const kind = normalizeKind(value.kind);
  const revision = nonnegativeInteger(value.revision);
  const total = nonnegativeInteger(value.total);
  const from = nonnegativeInteger(value.from);
  if (
    messageId !== reference.messageId ||
    branchId === null ||
    !/^br_[0-9a-f]{8}$/u.test(branchId) ||
    (reference.branchId !== undefined && branchId !== reference.branchId) ||
    section !== 'output' ||
    path !== expectedPath ||
    !kind ||
    revision === null ||
    (reference.revision !== undefined && revision !== reference.revision) ||
    total === null ||
    from === null ||
    !Array.isArray(value.items)
  ) {
    throw invalidPage();
  }
  const items = value.items.map(normalizeNode);
  if (
    items.some((item) => !item) ||
    from > total ||
    from + items.length > total ||
    !validPageItems(kind, path, from, total, items as RemoteToolDetailNode[])
  )
    throw invalidPage();
  const nextCursor = value.next_cursor === null ? null : exactString(value.next_cursor);
  const hasMore = from + items.length < total;
  if (
    (value.next_cursor !== null && nextCursor === null) ||
    (hasMore && nextCursor === null) ||
    (!hasMore && nextCursor !== null)
  )
    throw invalidPage();
  return {
    messageId,
    branchId,
    section: 'output',
    path,
    revision,
    kind,
    total,
    from,
    items: items as RemoteToolDetailNode[],
    nextCursor,
  };
}

function validPageItems(
  kind: RemoteToolDetailPage['kind'],
  path: string,
  from: number,
  total: number,
  items: RemoteToolDetailNode[]
): boolean {
  if (kind === 'scalar') {
    return (
      total === 1 &&
      from === 0 &&
      items.length === 1 &&
      items[0].kind === 'scalar' &&
      items[0].path === path &&
      items[0].key === undefined &&
      items[0].index === undefined
    );
  }
  if (from < total && items.length === 0) return false;
  return items.every((item, itemOffset) => {
    if (kind === 'array') {
      const index = from + itemOffset;
      return item.key === undefined && item.index === index && item.path === `${path}/${index}`;
    }
    return (
      item.key !== undefined &&
      item.index === undefined &&
      item.path === `${path}/${escapeToolDetailPathSegment(item.key)}`
    );
  });
}

function escapeToolDetailPathSegment(value: string): string {
  return value.replace(/~/gu, '~0').replace(/\//gu, '~1');
}

function normalizeNode(value: unknown): RemoteToolDetailNode | null {
  if (!isRecord(value)) return null;
  const path = exactString(value.path, true);
  const kind = normalizeKind(value.kind);
  const key = value.key === undefined ? undefined : exactString(value.key);
  const index = value.index === undefined ? undefined : nonnegativeInteger(value.index);
  const total = value.total === undefined ? undefined : nonnegativeInteger(value.total);
  const truncated = value.truncated === undefined ? undefined : value.truncated === true;
  const originalBytes = value.original_bytes === undefined ? undefined : nonnegativeInteger(value.original_bytes);
  if (
    path === null ||
    !kind ||
    key === null ||
    index === null ||
    total === null ||
    truncated === false ||
    originalBytes === null
  )
    return null;
  if ((kind === 'array' || kind === 'object') && total === undefined) return null;
  const scalar = kind === 'scalar';
  const scalarValue = scalar ? value.value : undefined;
  if (
    scalar &&
    scalarValue !== null &&
    typeof scalarValue !== 'string' &&
    (typeof scalarValue !== 'number' || !Number.isFinite(scalarValue)) &&
    typeof scalarValue !== 'boolean'
  ) {
    return null;
  }
  if (
    (truncated === true || originalBytes !== undefined) &&
    (!scalar ||
      truncated !== true ||
      originalBytes === undefined ||
      typeof scalarValue !== 'string' ||
      originalBytes <= UTF8_ENCODER.encode(scalarValue).byteLength)
  )
    return null;
  return {
    path,
    kind,
    ...(key === undefined ? {} : { key }),
    ...(index === undefined ? {} : { index }),
    ...(scalar ? { value: scalarValue as string | number | boolean | null } : {}),
    ...(total === undefined ? {} : { total }),
    ...(truncated === true ? { truncated: true } : {}),
    ...(originalBytes === undefined ? {} : { originalBytes }),
  };
}

function normalizeKind(value: unknown): RemoteToolDetailNode['kind'] | null {
  return value === 'scalar' || value === 'object' || value === 'array' ? value : null;
}

function exactString(value: unknown, allowEmpty = false): string | null {
  if (
    typeof value !== 'string' ||
    value.trim() !== value ||
    (!allowEmpty && value.length === 0) ||
    value.length > 2048
  ) {
    return null;
  }
  return value;
}

function nonnegativeInteger(value: unknown): number | null {
  return Number.isSafeInteger(value) && Number(value) >= 0 ? Number(value) : null;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value && typeof value === 'object' && !Array.isArray(value));
}

function invalidPage(): Error {
  return new Error('Tool detail response is invalid');
}

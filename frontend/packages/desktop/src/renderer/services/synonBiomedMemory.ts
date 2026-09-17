export type SynonBiomedMemoryOptions = {
  baseUrl?: string;
  fetchImpl?: typeof fetch;
  signal?: AbortSignal;
};

export type SynonBiomedMemoryRow = {
  id: string;
  body: string;
  userId: string | null;
  categoryId: string | null;
  subjectProjectId: string | null;
  subjectArtifactId: string | null;
  subjectVersionId: string | null;
  subjectFrameId: string | null;
  sourceFrameId: string | null;
  origin: string;
  evidence: string;
  createdAt: string | null;
  updatedAt: string | null;
  lastSurfacedAt: string | null;
};

export type SynonBiomedMemoryEntity = {
  entityKey: string;
  label: string;
  projectId: string | null;
  rows: SynonBiomedMemoryRow[];
};

export type SynonBiomedMemorySession = {
  frameId: string;
  projectId: string;
  label: string;
  rowCount: number;
  newestAt: string | null;
};

export type SynonBiomedMemoryCategory = {
  id: string;
  name: string;
  guidance: string;
  autoRecall: boolean;
  rowCount: number;
};

export type SynonBiomedMemoryContext = {
  profileMarkdown: string;
  listingMarkdown: string;
  enabled: boolean | null;
  totalRows: number;
  entities: SynonBiomedMemoryEntity[];
  sessions: SynonBiomedMemorySession[];
  categories: SynonBiomedMemoryCategory[];
};

export type SynonBiomedCreateMemoryInput = {
  text: string;
  entity: string;
  category?: string;
};

export type SynonBiomedUpdateMemoryInput = {
  text: string;
  evidence?: string;
  category?: string | null;
};

export type SynonBiomedMemoryCategoryInput = {
  name: string;
  guidance: string;
  autoRecall: boolean;
};

export type SynonBiomedCreateMemoryCategoryInput = Omit<SynonBiomedMemoryCategoryInput, 'autoRecall'> & {
  autoRecall?: boolean;
};

type RecordValue = Record<string, unknown>;

export async function loadSynonBiomedMemoryContext(
  query: { projectId?: string } = {},
  options: SynonBiomedMemoryOptions = {}
): Promise<SynonBiomedMemoryContext> {
  const search = new URLSearchParams();
  if (query.projectId) search.set('project_id', query.projectId);
  const payload = asRecord(await requestJson(`/api/memory/context${search.size ? `?${search}` : ''}`, {}, options));
  return {
    profileMarkdown: stringValue(payload?.profile_md),
    listingMarkdown: stringValue(payload?.listing_md),
    enabled: nullableBooleanValue(payload?.memory_enabled),
    totalRows: numberValue(payload?.total_user_rows),
    entities: arrayValue(payload?.entities).map(toMemoryEntity).filter(isMemoryEntity),
    sessions: arrayValue(payload?.sessions).map(toMemorySession).filter(isMemorySession),
    categories: arrayValue(payload?.categories).map(toMemoryCategory).filter(isMemoryCategory),
  };
}

export async function loadSynonBiomedMemoryEnabled(options: SynonBiomedMemoryOptions = {}): Promise<boolean> {
  const payload = asRecord(await requestJson('/api/memory/enabled', {}, options));
  return payload?.enabled === true;
}

export async function loadSynonBiomedAutoMemoryEnabled(options: SynonBiomedMemoryOptions = {}): Promise<boolean> {
  const payload = asRecord(await requestJson('/api/memory/auto-enabled', {}, options));
  return payload?.enabled === true;
}

export async function loadSynonBiomedSessionMemories(
  frameId: string,
  options: SynonBiomedMemoryOptions = {}
): Promise<SynonBiomedMemoryRow[]> {
  const payload = asRecord(await requestJson(`/api/memory/sessions/${encodeURIComponent(frameId)}`, {}, options));
  return arrayValue(payload?.rows).map(toMemoryRow).filter(isMemoryRow);
}

export async function setSynonBiomedMemoryEnabled(
  enabled: boolean,
  options: SynonBiomedMemoryOptions = {}
): Promise<void> {
  await requestJson('/api/memory/enabled', jsonRequest('PUT', { enabled }), options);
}

export async function setSynonBiomedAutoMemoryEnabled(
  enabled: boolean,
  options: SynonBiomedMemoryOptions = {}
): Promise<void> {
  await requestJson('/api/memory/auto-enabled', jsonRequest('PUT', { enabled }), options);
}

export async function setSynonBiomedProjectMemoryEnabled(
  projectId: string,
  enabled: boolean,
  options: SynonBiomedMemoryOptions = {}
): Promise<void> {
  const normalizedProjectId = projectId.trim();
  if (!normalizedProjectId) throw new Error('Synon Biomed project id is required');
  await requestJson(
    `/api/projects/${encodeURIComponent(normalizedProjectId)}/memory/enabled`,
    jsonRequest('PUT', { enabled }),
    options
  );
}

export async function createSynonBiomedMemory(
  input: SynonBiomedCreateMemoryInput,
  options: SynonBiomedMemoryOptions = {}
): Promise<SynonBiomedMemoryRow> {
  const payload = await requestJson('/api/memories', jsonRequest('POST', input), options);
  const row = toMemoryRow(payload);
  if (!row) throw new Error('Synon Biomed memory create response is invalid');
  return row;
}

export async function updateSynonBiomedMemory(
  memoryId: string,
  input: SynonBiomedUpdateMemoryInput,
  options: SynonBiomedMemoryOptions = {}
): Promise<SynonBiomedMemoryRow | null> {
  const payload = await requestJson(
    `/api/memories/${encodeURIComponent(memoryId)}`,
    jsonRequest('PUT', input),
    options
  );
  return payload === undefined ? null : toMemoryRow(payload);
}

export async function deleteSynonBiomedMemory(memoryId: string, options: SynonBiomedMemoryOptions = {}): Promise<void> {
  await requestJson(`/api/memories/${encodeURIComponent(memoryId)}`, { method: 'DELETE' }, options);
}

export async function clearSynonBiomedMemories(options: SynonBiomedMemoryOptions = {}): Promise<void> {
  await requestJson('/api/memories', { method: 'DELETE' }, options);
}

export async function createSynonBiomedMemoryCategory(
  input: SynonBiomedCreateMemoryCategoryInput,
  options: SynonBiomedMemoryOptions = {}
): Promise<SynonBiomedMemoryCategory> {
  const payload = await requestJson(
    '/api/memory/categories',
    jsonRequest('POST', {
      name: input.name,
      guidance: input.guidance,
      auto_recall: input.autoRecall ?? true,
    }),
    options
  );
  const category = toMemoryCategory(payload);
  if (!category) throw new Error('Synon Biomed memory category create response is invalid');
  return category;
}

export async function updateSynonBiomedMemoryCategory(
  categoryId: string,
  input: SynonBiomedMemoryCategoryInput,
  options: SynonBiomedMemoryOptions = {}
): Promise<SynonBiomedMemoryCategory | null> {
  const payload = await requestJson(
    `/api/memory/categories/${encodeURIComponent(categoryId)}`,
    jsonRequest('PUT', {
      name: input.name,
      guidance: input.guidance,
      auto_recall: input.autoRecall,
    }),
    options
  );
  return payload === undefined ? null : toMemoryCategory(payload);
}

export async function deleteSynonBiomedMemoryCategory(
  categoryId: string,
  deleteFacts: boolean,
  options: SynonBiomedMemoryOptions = {}
): Promise<void> {
  await requestJson(
    `/api/memory/categories/${encodeURIComponent(categoryId)}?delete_facts=${String(deleteFacts)}`,
    { method: 'DELETE' },
    options
  );
}

async function requestJson(path: string, init: RequestInit, options: SynonBiomedMemoryOptions): Promise<unknown> {
  const response = await (options.fetchImpl ?? fetch)(toGatewayUrl(path, options.baseUrl), {
    ...init,
    credentials: 'include',
    headers: {
      Accept: 'application/json',
      ...init.headers,
    },
    signal: options.signal ?? init.signal,
  });
  if (!response.ok) {
    const payload = asRecord(await response.json().catch((): null => null));
    const detail = stringValue(payload?.detail) || stringValue(payload?.error);
    throw new Error(`Synon Biomed memory request failed: ${response.status} ${path}${detail ? ` ${detail}` : ''}`);
  }
  if (response.status === 204) return undefined;
  const text = await response.text();
  return text ? JSON.parse(text) : undefined;
}

function jsonRequest(method: 'POST' | 'PUT', body: unknown): RequestInit {
  return {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  };
}

function toGatewayUrl(path: string, baseUrl = ''): string {
  return `${baseUrl.replace(/\/+$/, '')}${path}`;
}

function toMemoryRow(value: unknown): SynonBiomedMemoryRow | null {
  const record = asRecord(value);
  const id = stringValue(record?.id);
  const body = stringValue(record?.body);
  if (!id || !body) return null;
  return {
    id,
    body,
    userId: nullableStringValue(record?.userId ?? record?.user_id),
    categoryId: nullableStringValue(record?.categoryId ?? record?.category_id),
    subjectProjectId: nullableStringValue(record?.subjectProjectId ?? record?.subject_project_id),
    subjectArtifactId: nullableStringValue(record?.subjectArtifactId ?? record?.subject_artifact_id),
    subjectVersionId: nullableStringValue(record?.subjectVersionId ?? record?.subject_version_id),
    subjectFrameId: nullableStringValue(record?.subjectFrameId ?? record?.subject_frame_id),
    sourceFrameId: nullableStringValue(record?.sourceFrameId ?? record?.source_frame_id),
    origin: stringValue(record?.origin) || 'user',
    evidence: stringValue(record?.evidence) || 'stated',
    createdAt: nullableStringValue(record?.createdAt ?? record?.created_at),
    updatedAt: nullableStringValue(record?.updatedAt ?? record?.updated_at),
    lastSurfacedAt: nullableStringValue(record?.lastSurfacedAt ?? record?.last_surfaced_at),
  };
}

function toMemoryEntity(value: unknown): SynonBiomedMemoryEntity | null {
  const record = asRecord(value);
  const entityKey = stringValue(record?.entity_key ?? record?.entityKey);
  if (!entityKey) return null;
  return {
    entityKey,
    label: stringValue(record?.label) || entityKey,
    projectId: nullableStringValue(record?.project_id ?? record?.projectId),
    rows: arrayValue(record?.rows).map(toMemoryRow).filter(isMemoryRow),
  };
}

function toMemorySession(value: unknown): SynonBiomedMemorySession | null {
  const record = asRecord(value);
  const frameId = stringValue(record?.frame_id ?? record?.frameId);
  const projectId = stringValue(record?.project_id ?? record?.projectId);
  if (!frameId || !projectId) return null;
  return {
    frameId,
    projectId,
    label: stringValue(record?.label) || frameId,
    rowCount: numberValue(record?.row_count ?? record?.rowCount),
    newestAt: nullableStringValue(record?.newest_at ?? record?.newestAt),
  };
}

function toMemoryCategory(value: unknown): SynonBiomedMemoryCategory | null {
  const record = asRecord(value);
  const id = stringValue(record?.id);
  const name = stringValue(record?.name);
  if (!id || !name) return null;
  return {
    id,
    name,
    guidance: stringValue(record?.guidance),
    autoRecall: record?.auto_recall === true || record?.autoRecall === true,
    rowCount: numberValue(record?.row_count ?? record?.rowCount),
  };
}

function asRecord(value: unknown): RecordValue | null {
  return value && typeof value === 'object' && !Array.isArray(value) ? (value as RecordValue) : null;
}

function arrayValue(value: unknown): unknown[] {
  return Array.isArray(value) ? value : [];
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value : '';
}

function nullableStringValue(value: unknown): string | null {
  const result = stringValue(value);
  return result || null;
}

function numberValue(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0;
}

function nullableBooleanValue(value: unknown): boolean | null {
  return typeof value === 'boolean' ? value : null;
}

function isMemoryRow(value: SynonBiomedMemoryRow | null): value is SynonBiomedMemoryRow {
  return value !== null;
}

function isMemoryEntity(value: SynonBiomedMemoryEntity | null): value is SynonBiomedMemoryEntity {
  return value !== null;
}

function isMemorySession(value: SynonBiomedMemorySession | null): value is SynonBiomedMemorySession {
  return value !== null;
}

function isMemoryCategory(value: SynonBiomedMemoryCategory | null): value is SynonBiomedMemoryCategory {
  return value !== null;
}

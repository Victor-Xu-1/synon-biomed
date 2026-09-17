export type McpMarketplaceSearchOptions = {
  fetchImpl?: typeof fetch;
  registryUrl?: string;
  signal?: AbortSignal;
  timeoutMs?: number;
};

export type McpMarketplaceEntry = {
  id: string;
  name: string;
  title: string;
  description: string;
  version: string | null;
  provider: string;
  repositoryUrl: string | null;
  websiteUrl: string | null;
  registryUrl: string;
  remoteUrl: string | null;
  transport: 'sse' | 'streamable_http' | null;
  authRequired: boolean;
};

export type McpMarketplaceSearchResult = {
  entries: McpMarketplaceEntry[];
  nextCursor: string | null;
  count: number;
};

export class McpMarketplaceError extends Error {
  readonly status: number | null;

  constructor(message: string, status: number | null = null) {
    super(message);
    this.name = 'McpMarketplaceError';
    this.status = status;
  }
}

const DEFAULT_REGISTRY_URL = '/api/mcp-servers/marketplace';
const DEFAULT_TIMEOUT_MS = 12_000;
const MAX_QUERY_LENGTH = 80;
const MAX_RESULTS = 40;

export async function searchMcpMarketplace(
  query = '',
  options: McpMarketplaceSearchOptions = {}
): Promise<McpMarketplaceSearchResult> {
  const runtimeOrigin = globalThis.location?.origin;
  const baseUrl = runtimeOrigin && runtimeOrigin !== 'null' ? runtimeOrigin : 'http://localhost';
  const url = new URL(options.registryUrl ?? DEFAULT_REGISTRY_URL, baseUrl);
  const normalizedQuery = query.trim().slice(0, MAX_QUERY_LENGTH);
  url.searchParams.set('version', 'latest');
  url.searchParams.set('limit', String(MAX_RESULTS));
  if (normalizedQuery) url.searchParams.set('search', normalizedQuery);

  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), options.timeoutMs ?? DEFAULT_TIMEOUT_MS);
  if (options.signal) {
    if (options.signal.aborted) controller.abort();
    else options.signal.addEventListener('abort', () => controller.abort(), { once: true });
  }

  try {
    const response = await (options.fetchImpl ?? fetch)(url.toString(), {
      headers: { Accept: 'application/json' },
      signal: controller.signal,
    });
    const payload = await readJson(response);
    if (!response.ok) {
      throw new McpMarketplaceError(`MCP Registry request failed (${response.status})`, response.status);
    }
    return normalizeSearchResult(payload);
  } catch (error) {
    if (error instanceof McpMarketplaceError) throw error;
    if (error instanceof DOMException && error.name === 'AbortError') {
      throw new McpMarketplaceError('MCP Registry request timed out');
    }
    throw new McpMarketplaceError('MCP Registry is temporarily unavailable');
  } finally {
    clearTimeout(timeout);
  }
}

async function readJson(response: Response): Promise<unknown> {
  try {
    return await response.json();
  } catch {
    throw new McpMarketplaceError('MCP Registry returned invalid JSON');
  }
}

function normalizeSearchResult(value: unknown): McpMarketplaceSearchResult {
  const record = asRecord(value);
  const rawServers = Array.isArray(record?.servers) ? record.servers : [];
  const seen = new Set<string>();
  const entries: McpMarketplaceEntry[] = [];
  for (const raw of rawServers) {
    const entry = toMarketplaceEntry(raw);
    if (!entry || seen.has(entry.id)) continue;
    seen.add(entry.id);
    entries.push(entry);
  }
  const metadata = asRecord(record?.metadata);
  return {
    entries,
    nextCursor: nullableString(metadata?.nextCursor ?? metadata?.next_cursor),
    count: integerValue(metadata?.count, entries.length),
  };
}

function toMarketplaceEntry(value: unknown): McpMarketplaceEntry | null {
  const wrapper = asRecord(value);
  const server = asRecord(wrapper?.server ?? value);
  const name = stringValue(server?.name).trim();
  if (!server || !name) return null;

  const remote = selectRemote(server?.remotes, server);
  const repository = asRecord(server.repository);
  const repositoryUrl = publicHttpsUrl(repository?.url);
  const websiteUrl = publicHttpsUrl(server.websiteUrl);
  return {
    id: name,
    name,
    title: stringValue(server.title).trim() || name,
    description: stringValue(server.description).trim() || 'MCP server listed in the official registry.',
    version: nullableString(server.version),
    provider: name.includes('/') ? name.slice(0, name.indexOf('/')) : name,
    repositoryUrl,
    websiteUrl,
    registryUrl: `https://registry.modelcontextprotocol.io/v0.1/servers?search=${encodeURIComponent(name)}`,
    remoteUrl: remote?.url ?? null,
    transport: remote?.transport ?? null,
    authRequired: remote?.authRequired ?? false,
  };
}

function selectRemote(
  value: unknown,
  server: Record<string, unknown> | null
): {
  url: string;
  transport: 'sse' | 'streamable_http';
  authRequired: boolean;
} | null {
  if (!Array.isArray(value)) return null;
  for (const raw of value) {
    const remote = asRecord(raw);
    const url = publicHttpsUrl(remote?.url);
    const transport = normalizeTransport(remote?.type);
    if (!url || !transport) continue;
    const headers = Array.isArray(remote?.headers) ? remote.headers : [];
    const authRequired =
      headers.some((header) => {
        const item = asRecord(header);
        return Boolean(item?.isRequired || item?.isSecret) || /\{[^}]+\}/.test(stringValue(item?.value));
      }) || descriptionRequiresCredentials(server);
    return { url, transport, authRequired };
  }
  return null;
}

function descriptionRequiresCredentials(server: Record<string, unknown> | null): boolean {
  const description = `${stringValue(server?.title)} ${stringValue(server?.description)}`;
  return (
    /\b(?:requires?|needs?|provide|set|use)\b[^.\n]{0,80}\b(?:api[-_\s]?key|token|oauth|credential|bearer)\b/i.test(
      description
    ) || /\b(?:api[-_\s]?key|token|oauth|credential|bearer)\b[^.\n]{0,40}\b(?:required|needed)\b/i.test(description)
  );
}

function normalizeTransport(value: unknown): 'sse' | 'streamable_http' | null {
  const normalized = stringValue(value).trim().toLowerCase();
  if (normalized === 'sse' || normalized === 'server-sent-events') return 'sse';
  if (normalized === 'streamable-http' || normalized === 'streamable_http' || normalized === 'http') {
    return 'streamable_http';
  }
  return null;
}

function publicHttpsUrl(value: unknown): string | null {
  if (typeof value !== 'string' || !value.trim()) return null;
  try {
    const url = new URL(value.trim());
    return url.protocol === 'https:' ? url.toString() : null;
  } catch {
    return null;
  }
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return value && typeof value === 'object' && !Array.isArray(value) ? (value as Record<string, unknown>) : null;
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value : '';
}

function nullableString(value: unknown): string | null {
  const normalized = stringValue(value).trim();
  return normalized || null;
}

function integerValue(value: unknown, fallback: number): number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : fallback;
}

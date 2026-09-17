import { withCsrfHeader } from '../csrf';

const maxMcpAppResponseBytes = 1024 * 1024;
const maxMcpAppTicketResponseBytes = 5 * 1024 * 1024;
const maxMcpAppMountInputBytes = 2 * 1024 * 1024;

export type SynonBiomedMcpAppViewerBinding = {
  serverId: string;
  serverName: string;
  resourceUri: string;
  openTool: string;
  contentParam: string;
  contentEncoding: 'utf8' | 'base64';
  nameParam: string | null;
  docsUrl: string | null;
  inProcess: boolean;
};

export type SynonBiomedMcpAppOptions = {
  fetchImpl?: typeof fetch;
  pageLocation?: Pick<Location, 'hostname' | 'port' | 'protocol'>;
};

export type SynonBiomedMcpAppToolResult = {
  content: Array<{ type: string; text?: string }>;
  structuredContent?: unknown;
  isError?: boolean;
};

export type SynonBiomedMcpAppTool = {
  name: string;
  description?: string;
  inputSchema: Record<string, unknown>;
};

export type SynonBiomedMcpAppRegistration = {
  registrationId: string;
  artifactId: string;
  expiresAt: string;
};

export type SynonBiomedMcpAppRequest = {
  requestId: string;
  serverId: string;
  serverName: string;
  artifactId: string;
  tool: string;
  arguments: Record<string, unknown>;
};

export type SynonBiomedMcpAppPinResult = {
  artifactId: string;
  versionId: string;
  filename: string;
};

export type SynonBiomedMcpAppResourceTicket = {
  url: string;
  origin: string;
  expiresAt: string;
  toolInput: Record<string, unknown>;
  toolResult: SynonBiomedMcpAppToolResult;
};

export async function loadSynonBiomedMcpAppViewerBindings(
  options: SynonBiomedMcpAppOptions = {}
): Promise<SynonBiomedMcpAppViewerBinding[]> {
  const payload = asRecord(await requestJson('/api/mcp-apps/viewer-bindings', undefined, options));
  if (!payload || !Array.isArray(payload.bindings))
    throw new Error('Synon Biomed MCP App bindings response is invalid');
  return payload.bindings
    .map(toViewerBinding)
    .filter((binding): binding is SynonBiomedMcpAppViewerBinding => binding !== null);
}

export async function createSynonBiomedMcpAppResourceTicket(
  serverId: string,
  resourceUri: string,
  argumentsValue: Record<string, unknown>,
  options: SynonBiomedMcpAppOptions = {}
): Promise<SynonBiomedMcpAppResourceTicket> {
  const serializedArguments = serializeSynonBiomedMcpAppMountArguments(argumentsValue);
  const payload = asRecord(
    await requestJson(
      '/api/mcp/apps/resource-tickets',
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: `{"server_id":${JSON.stringify(serverId)},"resource_uri":${JSON.stringify(
          resourceUri
        )},"arguments":${serializedArguments}}`,
      },
      options,
      maxMcpAppTicketResponseBytes
    )
  );
  const ticket = stringValue(payload?.ticket);
  const expiresAt = stringValue(payload?.expires_at);
  const toolInput = asRecord(payload?.tool_input);
  const toolResult = normalizeToolResult(payload?.tool_result);
  if (!ticket || !expiresAt || !toolInput || !toolResult) throw new Error('MCP_APP_TICKET_INVALID');
  const url = synonBiomedMcpAppSandboxUrl(ticket, options.pageLocation);
  return { url, origin: new URL(url).origin, expiresAt, toolInput, toolResult };
}

export async function registerSynonBiomedMcpApp(
  input: {
    rootFrameId: string;
    frameId?: string;
    server: string;
    artifactId: string;
    tools: SynonBiomedMcpAppTool[];
  },
  options: SynonBiomedMcpAppOptions = {}
): Promise<SynonBiomedMcpAppRegistration> {
  const payload = asRecord(
    await requestJson(
      '/api/mcp/apps/registrations',
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          root_frame_id: input.rootFrameId,
          frame_id: input.frameId ?? '',
          server: input.server,
          artifact_id: input.artifactId,
          tools: input.tools,
        }),
      },
      options
    )
  );
  const registrationId = stringValue(payload?.registration_id);
  const artifactId = stringValue(payload?.artifact_id);
  const expiresAt = stringValue(payload?.expires_at);
  if (!registrationId || !artifactId || !expiresAt) throw new Error('MCP_APP_REGISTRATION_INVALID');
  return { registrationId, artifactId, expiresAt };
}

export async function unregisterSynonBiomedMcpApp(
  registrationId: string,
  options: SynonBiomedMcpAppOptions = {}
): Promise<void> {
  await requestJson(
    `/api/mcp/apps/registrations?registration_id=${encodeURIComponent(registrationId)}`,
    { method: 'DELETE' },
    options
  );
}

export async function pollSynonBiomedMcpAppRequest(
  registrationId: string,
  signal?: AbortSignal,
  options: SynonBiomedMcpAppOptions = {}
): Promise<SynonBiomedMcpAppRequest | null> {
  const payload = await requestJson(
    `/api/mcp/apps/requests?registration_id=${encodeURIComponent(registrationId)}`,
    { method: 'GET', signal },
    options
  );
  if (payload === undefined) return null;
  const record = asRecord(payload);
  const requestId = stringValue(record?.request_id);
  const serverId = stringValue(record?.server_id);
  const serverName = stringValue(record?.server_name);
  const artifactId = stringValue(record?.artifact_id);
  const tool = stringValue(record?.tool);
  const argumentsValue = asRecord(record?.arguments);
  if (!requestId || !serverId || !serverName || !artifactId || !tool || !argumentsValue) {
    throw new Error('MCP_APP_REQUEST_INVALID');
  }
  return {
    requestId,
    serverId,
    serverName,
    artifactId,
    tool,
    arguments: argumentsValue,
  };
}

export async function resolveSynonBiomedMcpAppRequest(
  registrationId: string,
  requestId: string,
  result: SynonBiomedMcpAppToolResult,
  options: SynonBiomedMcpAppOptions = {}
): Promise<void> {
  await requestJson(
    '/api/mcp/apps/results',
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        registration_id: registrationId,
        request_id: requestId,
        content: result.content,
        structured_content: result.structuredContent,
        is_error: result.isError === true,
      }),
    },
    options
  );
}

export async function pinSynonBiomedMcpAppArtifact(
  input: {
    rootFrameId: string;
    frameId?: string;
    artifactId?: string;
    filename: string;
    contentType: string;
    content: string;
    contentEncoding: 'utf8' | 'base64';
    agentName: string;
    tool: string;
    arguments: Record<string, unknown>;
    idempotencyKey: string;
  },
  options: SynonBiomedMcpAppOptions = {}
): Promise<SynonBiomedMcpAppPinResult> {
  const payload = asRecord(
    await requestJson(
      '/api/mcp/apps/pin',
      {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Idempotency-Key': input.idempotencyKey,
        },
        body: JSON.stringify({
          root_frame_id: input.rootFrameId,
          frame_id: input.frameId ?? '',
          artifact_id: input.artifactId ?? '',
          filename: input.filename,
          content_type: input.contentType,
          content: input.content,
          content_encoding: input.contentEncoding,
          agent_name: input.agentName,
          tool: input.tool,
          arguments: input.arguments,
        }),
      },
      options,
      maxMcpAppTicketResponseBytes
    )
  );
  const artifactId = stringValue(payload?.artifact_id);
  const versionId = stringValue(payload?.version_id);
  const filename = stringValue(payload?.filename);
  if (!artifactId || !versionId || !filename) throw new Error('MCP_APP_PIN_INVALID');
  return { artifactId, versionId, filename };
}

// Ketcher accepts a flat object of string inputs. Serialize that closed shape
// here so an inline preview cannot allocate or send an unbounded request before
// the server applies the same 2 MiB tool-input contract.
export function serializeSynonBiomedMcpAppMountArguments(argumentsValue: Record<string, unknown>): string {
  const fields: string[] = [];
  let totalBytes = 2; // opening and closing braces
  for (const [key, value] of Object.entries(argumentsValue)) {
    if (typeof value !== 'string') throw new Error('MCP_APP_INPUT_INVALID');
    if (fields.length) totalBytes += 1;
    totalBytes += jsonStringUTF8ByteLength(key, maxMcpAppMountInputBytes - totalBytes);
    totalBytes += 1; // colon
    totalBytes += jsonStringUTF8ByteLength(value, maxMcpAppMountInputBytes - totalBytes);
    if (totalBytes > maxMcpAppMountInputBytes) throw new Error('MCP_APP_INPUT_TOO_LARGE');
    fields.push(`${JSON.stringify(key)}:${JSON.stringify(value)}`);
  }
  return `{${fields.join(',')}}`;
}

export function synonBiomedMcpAppSandboxUrl(
  ticket: string,
  pageLocation: Pick<Location, 'hostname' | 'port' | 'protocol'> | undefined = globalThis.location
): string {
  const hostname = pageLocation?.hostname.toLowerCase();
  if (!pageLocation || !hostname || !['localhost', '127.0.0.1', '::1', '[::1]'].includes(hostname)) {
    throw new Error('MCP App sandbox origin is available only on localhost');
  }
  if (pageLocation.protocol !== 'http:' && pageLocation.protocol !== 'https:') {
    throw new Error('MCP App sandbox protocol is invalid');
  }
  const port = pageLocation.port ? `:${pageLocation.port}` : '';
  return `${pageLocation.protocol}//mcp-app.localhost${port}/mcp-app-resource?ticket=${encodeURIComponent(ticket)}`;
}

async function requestJson(
  path: string,
  init: RequestInit | undefined,
  options: SynonBiomedMcpAppOptions,
  maxResponseBytes = maxMcpAppResponseBytes
): Promise<unknown> {
  const initWithCredentials: RequestInit = {
    ...init,
    credentials: 'include',
    headers: { Accept: 'application/json', ...init?.headers },
  };
  const response = await (options.fetchImpl ?? fetch)(path, withCsrfHeader(path, initWithCredentials));
  if (!response.ok) {
    await response.body?.cancel().catch((): void => {});
    throw new Error(`MCP_APP_HTTP_${response.status}`);
  }
  if (response.status === 204) return undefined;
  const text = await readBoundedResponseText(response, maxResponseBytes);
  if (!text) return undefined;
  try {
    return JSON.parse(text);
  } catch {
    throw new Error('MCP_APP_RESPONSE_INVALID');
  }
}

async function readBoundedResponseText(response: Response, maxBytes: number): Promise<string> {
  const declared = Number(response.headers.get('content-length') ?? 0);
  if (Number.isFinite(declared) && declared > maxBytes) {
    await response.body?.cancel().catch((): void => {});
    throw new Error('MCP_APP_RESPONSE_TOO_LARGE');
  }
  if (!response.body) return '';
  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let total = 0;
  try {
    while (true) {
      // oxlint-disable-next-line no-await-in-loop
      const { done, value } = await reader.read();
      if (done) break;
      if (!value) continue;
      total += value.byteLength;
      if (total > maxBytes) throw new Error('MCP_APP_RESPONSE_TOO_LARGE');
      chunks.push(value);
    }
  } finally {
    reader.releaseLock();
  }
  const bytes = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return new TextDecoder().decode(bytes);
}

function toViewerBinding(value: unknown): SynonBiomedMcpAppViewerBinding | null {
  const record = asRecord(value);
  const serverId = stringValue(record?.serverId ?? record?.server_id);
  const serverName = stringValue(record?.serverName ?? record?.server_name);
  const resourceUri = stringValue(record?.resourceUri ?? record?.resource_uri);
  const openTool = stringValue(record?.openTool ?? record?.open_tool);
  const contentParam = stringValue(record?.contentParam ?? record?.content_param);
  const encoding = stringValue(record?.contentEncoding ?? record?.content_encoding);
  if (!serverId || !serverName || !resourceUri || !openTool || !contentParam) return null;
  return {
    serverId,
    serverName,
    resourceUri,
    openTool,
    contentParam,
    contentEncoding: encoding === 'base64' ? 'base64' : 'utf8',
    nameParam: stringValue(record?.nameParam ?? record?.name_param),
    docsUrl: stringValue(record?.docsUrl ?? record?.docs_url),
    inProcess: record?.inProcess === true || record?.in_process === true,
  };
}

function normalizeToolResult(value: unknown): SynonBiomedMcpAppToolResult | null {
  const record = asRecord(value);
  if (!record || !Array.isArray(record.content)) return null;
  const content = record.content.flatMap((item) => {
    const block = asRecord(item);
    if (!block || typeof block.type !== 'string') return [];
    return [
      {
        type: block.type,
        ...(typeof block.text === 'string' ? { text: block.text } : {}),
      },
    ];
  });
  return {
    content,
    ...(record.structuredContent !== undefined ? { structuredContent: record.structuredContent } : {}),
    ...(record.isError === true ? { isError: true } : {}),
  };
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function stringValue(value: unknown): string | null {
  return typeof value === 'string' && value.trim() ? value : null;
}

function jsonStringUTF8ByteLength(value: string, remaining: number): number {
  let bytes = 2; // surrounding quotes
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code === 0x22 || code === 0x5c || [0x08, 0x09, 0x0a, 0x0c, 0x0d].includes(code)) {
      bytes += 2;
    } else if (code < 0x20) {
      bytes += 6;
    } else if (code <= 0x7f) {
      bytes += 1;
    } else if (code <= 0x7ff) {
      bytes += 2;
    } else if (code >= 0xd800 && code <= 0xdbff && index + 1 < value.length) {
      const next = value.charCodeAt(index + 1);
      if (next >= 0xdc00 && next <= 0xdfff) {
        bytes += 4;
        index += 1;
      } else {
        bytes += 6;
      }
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      bytes += 6;
    } else {
      bytes += 3;
    }
    if (bytes > remaining) return bytes;
  }
  return bytes;
}

import { BackendHttpError } from '@/common/adapter/httpBridge';
import { withTransientHistoryRetry } from '@/renderer/services/synonBiomedHistoryRetry';

export type SynonBiomedLineageMessage = {
  id: string;
  kind: 'text' | 'thinking' | 'tool' | 'status';
  position: 'left' | 'right' | 'center';
  content: string;
  createdAt: number | null;
};

export type SynonBiomedLineageMessagesResult = {
  items: SynonBiomedLineageMessage[];
  hasMoreBefore: boolean;
};

export type SynonBiomedLineageMessagesOptions = {
  fetchImpl?: typeof fetch;
  signal?: AbortSignal;
  limit?: number;
};

export async function loadSynonBiomedLineageMessages(
  frameId: string,
  options: SynonBiomedLineageMessagesOptions = {}
): Promise<SynonBiomedLineageMessagesResult> {
  return withTransientHistoryRetry(() => loadSynonBiomedLineageMessagesOnce(frameId, options), options.signal);
}

async function loadSynonBiomedLineageMessagesOnce(
  frameId: string,
  options: SynonBiomedLineageMessagesOptions = {}
): Promise<SynonBiomedLineageMessagesResult> {
  const fetchImpl = options.fetchImpl ?? fetch;
  const query = new URLSearchParams({ limit: String(options.limit ?? 200) });
  const encodedFrameId = encodeURIComponent(frameId);
  const path = '/api/conversations/' + encodedFrameId + '/messages?' + query;
  const response = await fetchImpl(path, {
    headers: { accept: 'application/json' },
    signal: options.signal,
  });

  if (!response.ok) {
    const body = await readLineageErrorBody(response);
    throw new BackendHttpError({ method: 'GET', path, status: response.status, body });
  }

  const payload: unknown = await response.json();
  if (!isRecord(payload) || !Array.isArray(payload.items)) {
    throw new Error('Synon Biomed lineage messages response is invalid');
  }

  return {
    items: payload.items.flatMap(normalizeLineageMessage),
    hasMoreBefore: payload.has_more_before === true,
  };
}

async function readLineageErrorBody(response: Response): Promise<unknown> {
  try {
    const contentType = response.headers.get('content-type')?.toLowerCase() ?? '';
    if (contentType.includes('application/json')) {
      return await response.json();
    }
    return await response.text();
  } catch {
    return '';
  }
}

function normalizeLineageMessage(value: unknown): SynonBiomedLineageMessage[] {
  if (!isRecord(value)) return [];
  const id = stringValue(value.id) || stringValue(value.msg_id);
  if (!id) return [];
  const type = stringValue(value.type);
  const content = isRecord(value.content) ? value.content : null;
  const position = value.position === 'right' || value.position === 'center' ? value.position : 'left';
  const createdAt = typeof value.created_at === 'number' && Number.isFinite(value.created_at) ? value.created_at : null;

  if (type === 'text') {
    const text = content ? stringValue(content.content) : stringValue(value.content);
    return text ? [{ id, kind: 'text', position, content: text, createdAt }] : [];
  }
  if (type === 'thinking') {
    const text = content ? stringValue(content.content) : '';
    return text ? [{ id, kind: 'thinking', position: 'left', content: text, createdAt }] : [];
  }
  if (type === 'tool_call' || type === 'tool_group' || type === 'acp_tool_call') {
    const name = content ? stringValue(content.description) || stringValue(content.name) : '';
    const status = content ? stringValue(content.status) : '';
    return [
      {
        id,
        kind: 'tool',
        position: 'left',
        content: [name, status].filter(Boolean).join(' · '),
        createdAt,
      },
    ];
  }
  if (type === 'agent_status' || type === 'tips' || type === 'plan') {
    const text = content
      ? stringValue(content.content) || stringValue(content.message) || stringValue(content.status)
      : '';
    return text ? [{ id, kind: 'status', position: 'center', content: text, createdAt }] : [];
  }
  return [];
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : '';
}

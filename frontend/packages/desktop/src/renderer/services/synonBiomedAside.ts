export type SynonBiomedAsideAnswer =
  | { status: 'ok'; answer: string; frameId: string }
  | { status: 'noAnswer'; frameId: string };

export type SynonBiomedAsideOptions = {
  fetchImpl?: typeof fetch;
  signal?: AbortSignal;
  pollIntervalMs?: number;
  timeoutMs?: number;
};

export type SynonBiomedBranchOptions = {
  fetchImpl?: typeof fetch;
  model?: string | null;
  signal?: AbortSignal;
};

const TERMINAL_STATUSES = new Set(['completed', 'failed', 'cancelled', 'canceled']);

export async function createSynonBiomedBranchSession(
  parentFrameId: string,
  request: string,
  options: SynonBiomedBranchOptions = {}
): Promise<{ frameId: string }> {
  const normalizedRequest = request.trim();
  if (!normalizedRequest) throw new Error('Question cannot be empty');
  const fetchImpl = options.fetchImpl ?? fetch;
  const response = await fetchImpl(`/api/frames/${encodeURIComponent(parentFrameId)}/aside`, {
    method: 'POST',
    headers: { accept: 'application/json', 'content-type': 'application/json' },
    body: JSON.stringify({
      request: normalizedRequest,
      model: options.model ?? null,
      intent_id: crypto.randomUUID(),
      as_session: true,
    }),
    signal: options.signal,
  });
  if (!response.ok) {
    throw new Error(`Synon Biomed branch create failed: ${response.status} ${await readError(response)}`);
  }
  const created: unknown = await response.json();
  const frameId = isRecord(created) ? stringValue(created.frame_id) : '';
  if (!frameId) throw new Error('Synon Biomed branch create response is invalid');
  return { frameId };
}

export async function askSynonBiomedAsideQuestion(
  parentFrameId: string,
  question: string,
  options: SynonBiomedAsideOptions = {}
): Promise<SynonBiomedAsideAnswer> {
  const request = question.trim();
  if (!request) throw new Error('Question cannot be empty');
  const fetchImpl = options.fetchImpl ?? fetch;
  const signal = options.signal;
  const createResponse = await fetchImpl(`/api/frames/${encodeURIComponent(parentFrameId)}/aside`, {
    method: 'POST',
    headers: { accept: 'application/json', 'content-type': 'application/json' },
    body: JSON.stringify({
      request,
      model: null,
      intent_id: crypto.randomUUID(),
      as_session: false,
    }),
    signal,
  });
  if (!createResponse.ok) {
    throw new Error(`Synon Biomed aside create failed: ${createResponse.status} ${await readError(createResponse)}`);
  }
  const created: unknown = await createResponse.json();
  const frameId = isRecord(created) ? stringValue(created.frame_id) : '';
  if (!frameId) throw new Error('Synon Biomed aside create response is invalid');

  const pollIntervalMs = options.pollIntervalMs ?? 750;
  const timeoutMs = options.timeoutMs ?? 120_000;
  const deadline = Date.now() + timeoutMs;
  const pollFrame = async (): Promise<SynonBiomedAsideAnswer> => {
    if (Date.now() >= deadline) throw new Error('Synon Biomed aside timed out');
    const frameResponse = await fetchImpl(`/api/frames/${encodeURIComponent(frameId)}?shallow=true`, {
      headers: { accept: 'application/json' },
      signal,
    });
    if (!frameResponse.ok) {
      throw new Error(`Synon Biomed aside status failed: ${frameResponse.status} ${await readError(frameResponse)}`);
    }
    const frame: unknown = await frameResponse.json();
    if (!isRecord(frame)) throw new Error('Synon Biomed aside frame response is invalid');
    const output = isRecord(frame.output_data) ? frame.output_data : null;
    const answer = output ? stringValue(output.response) : '';
    if (answer) return { status: 'ok', answer, frameId };
    const status = stringValue(frame.status).toLowerCase();
    if (status === 'failed') {
      throw new Error((output ? stringValue(output.error) : '') || 'Synon Biomed aside failed');
    }
    if (TERMINAL_STATUSES.has(status)) return { status: 'noAnswer', frameId };
    await abortableDelay(pollIntervalMs, signal);
    return pollFrame();
  };
  return pollFrame();
}

function abortableDelay(ms: number, signal?: AbortSignal): Promise<void> {
  if (signal?.aborted) return Promise.reject(signal.reason ?? new DOMException('Aborted', 'AbortError'));
  return new Promise((resolve, reject) => {
    const timer = setTimeout(resolve, ms);
    signal?.addEventListener(
      'abort',
      () => {
        clearTimeout(timer);
        reject(signal.reason ?? new DOMException('Aborted', 'AbortError'));
      },
      { once: true }
    );
  });
}

async function readError(response: Response): Promise<string> {
  const text = await response.text().catch(() => '');
  if (!text) return '';
  try {
    const payload: unknown = JSON.parse(text);
    return isRecord(payload) ? stringValue(payload.detail) || stringValue(payload.error) || text : text;
  } catch {
    return text;
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : '';
}

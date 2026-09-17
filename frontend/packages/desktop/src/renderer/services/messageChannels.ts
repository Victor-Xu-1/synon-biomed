import { BackendHttpError, httpRequest, isBackendHttpError } from '@/common/adapter/httpBridge';

export type MessageChannelId = 'feishu' | 'wechat';

export type MessageChannelStatus = {
  configured: boolean;
  paired: boolean;
};

export type MessageChannelStatuses = Record<MessageChannelId, MessageChannelStatus>;

export type MessageChannelQrStart = {
  sessionKey: string;
  qrCodeUrl: string;
  message?: string;
  expiresAt?: string;
};

export type MessageChannelQrStatus = 'wait' | 'scanned' | 'confirmed' | 'expired' | 'error';

export type MessageChannelQrPoll = {
  status: MessageChannelQrStatus;
  connected: boolean;
  message?: string;
};

export type MessageChannelUnpairResult = {
  unpaired: boolean;
  pairedUsersRevoked: number;
  restartScheduled: boolean;
};

type RecordValue = Record<string, unknown>;

const CHANNELS: MessageChannelId[] = ['feishu', 'wechat'];
const EMPTY_STATUS: MessageChannelStatus = { configured: false, paired: false };

export async function loadMessageChannelStatuses(): Promise<MessageChannelStatuses> {
  try {
    const payload = await httpRequest<unknown>('GET', '/api/adapters/message-channels', undefined, {
      silentStatuses: [404],
    });
    const record = asRecord(payload);
    const channels = asRecord(record?.channels) ?? record;
    return {
      feishu: readStatus(channels?.feishu),
      wechat: readStatus(channels?.wechat),
    };
  } catch (error) {
    // Older runtimes do not expose the read-only status endpoint yet. The QR
    // flow remains usable; an unavailable status must not turn the settings
    // page into a hard failure.
    if (isBackendHttpError(error) && error.status === 404) return emptyStatuses();
    throw error;
  }
}

export async function startMessageChannelQr(
  channel: MessageChannelId,
  sessionKey: string,
  signal?: AbortSignal
): Promise<MessageChannelQrStart> {
  const payload = await httpRequest<unknown>(
    'POST',
    `/api/adapters/${channel}/qr/start`,
    { sessionKey, force: true },
    { signal, timeoutMs: 40_000 }
  );
  const result = readResult(payload);
  const qrCodeUrl = safeQrCodeUrl(result?.qrCodeUrl);
  const returnedSessionKey = stringValue(result?.sessionKey) || sessionKey;
  if (!qrCodeUrl || !returnedSessionKey) {
    throw new Error('message channel QR payload is incomplete');
  }
  return {
    sessionKey: returnedSessionKey,
    qrCodeUrl,
    ...(stringValue(result?.message) ? { message: stringValue(result?.message) } : {}),
    ...(stringValue(result?.expiresAt) ? { expiresAt: stringValue(result?.expiresAt) } : {}),
  };
}

export async function pollMessageChannelQr(
  channel: MessageChannelId,
  sessionKey: string,
  signal?: AbortSignal
): Promise<MessageChannelQrPoll> {
  const payload = await httpRequest<unknown>(
    'POST',
    `/api/adapters/${channel}/qr/poll`,
    { sessionKey, timeoutMs: 30_000 },
    { signal, timeoutMs: 40_000 }
  );
  const result = readResult(payload);
  const status = normalizeQrStatus(result?.status);
  return {
    status,
    connected: result?.connected === true || status === 'confirmed',
    ...(stringValue(result?.message) ? { message: stringValue(result?.message) } : {}),
  };
}

export async function unpairMessageChannel(channel: MessageChannelId): Promise<MessageChannelUnpairResult> {
  const payload = await httpRequest<unknown>(
    'POST',
    '/api/adapters/message-channels/unpair',
    { channel },
    { timeoutMs: 40_000 }
  );
  const result = readResult(payload);
  if (!result || typeof result.unpaired !== 'boolean') {
    throw new Error('message channel unpair response is incomplete');
  }
  return {
    unpaired: result.unpaired,
    pairedUsersRevoked: integerValue(result.pairedUsersRevoked),
    restartScheduled: result.restartScheduled === true,
  };
}

export function emptyMessageChannelStatuses(): MessageChannelStatuses {
  return emptyStatuses();
}

function emptyStatuses(): MessageChannelStatuses {
  return {
    feishu: { ...EMPTY_STATUS },
    wechat: { ...EMPTY_STATUS },
  };
}

function readStatus(value: unknown): MessageChannelStatus {
  const record = asRecord(value);
  return {
    configured: record?.configured === true,
    paired: record?.paired === true,
  };
}

function readResult(value: unknown): RecordValue | null {
  const record = asRecord(value);
  return asRecord(record?.result) ?? record;
}

function asRecord(value: unknown): RecordValue | null {
  return value && typeof value === 'object' && !Array.isArray(value) ? (value as RecordValue) : null;
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : '';
}

function integerValue(value: unknown): number {
  return typeof value === 'number' && Number.isInteger(value) && value >= 0 ? value : 0;
}

function safeQrCodeUrl(value: unknown): string {
  const candidate = stringValue(value);
  if (/^data:image\/(?:png|jpeg|jpg|gif|webp|svg\+xml);/i.test(candidate)) return candidate;
  try {
    const parsed = new URL(candidate);
    return parsed.protocol === 'https:' || parsed.protocol === 'http:' ? candidate : '';
  } catch {
    return '';
  }
}

function normalizeQrStatus(value: unknown): MessageChannelQrStatus {
  const status = stringValue(value).toLowerCase();
  switch (status) {
    case 'wait':
    case 'waiting':
    case 'authorization_pending':
      return 'wait';
    case 'scaned':
    case 'scanned':
    case 'scan':
      return 'scanned';
    case 'confirmed':
    case 'connected':
    case 'success':
      return 'confirmed';
    case 'expired':
    case 'expired_token':
      return 'expired';
    default:
      return 'error';
  }
}

export function messageChannelErrorMessage(error: unknown): string {
  if (error instanceof BackendHttpError) return error.code || `HTTP_${error.status}`;
  return error instanceof Error && error.message ? error.message.slice(0, 160) : 'message channel request failed';
}

export const supportedMessageChannels = CHANNELS;

import { requestSynonBiomedJson, type SynonBiomedGatewayOptions } from './synonBiomedHttp';

export type SynonBiomedRuntimeUpdateStatus = {
  channel: string;
  current: string;
  latest: string | null;
  checkedAt: string | null;
  error: string | null;
  autoUpdate: boolean;
  required: string | null;
};

const optionalString = (value: unknown): string | null =>
  typeof value === 'string' && value.trim() ? value.trim() : null;

export async function checkSynonBiomedRuntimeUpdate(
  options: SynonBiomedGatewayOptions = {}
): Promise<SynonBiomedRuntimeUpdateStatus> {
  const payload = await requestSynonBiomedJson<unknown>(
    '/api/status/update/check',
    { method: 'POST' },
    { timeoutMs: 30_000, ...options }
  );
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) {
    throw new Error('Synon Biomed update status response is invalid');
  }
  const record = payload as Record<string, unknown>;
  const channel = optionalString(record.channel);
  const current = optionalString(record.current);
  if (!channel || !current || typeof record.autoUpdate !== 'boolean') {
    throw new Error('Synon Biomed update status response is invalid');
  }
  return {
    channel,
    current,
    latest: optionalString(record.latest),
    checkedAt: optionalString(record.checkedAt),
    error: optionalString(record.error),
    autoUpdate: record.autoUpdate,
    required: optionalString(record.required),
  };
}

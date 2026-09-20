import { BackendHttpError } from '@/common/adapter/httpBridge';

export type ScientificRuntimeOption = {
  id: string;
  estimatedInstallBytes: number;
  estimatedInstallMB: number;
  defaultEnabled: boolean;
  selected: boolean;
  available: boolean;
  status: string;
  environment?: string;
  errorCode?: string;
  phase?: string;
  phasePercent?: number;
  attempt?: number;
  updatedAt?: string;
};
export type ScientificRuntimeSettings = { configured: boolean; options: ScientificRuntimeOption[] };
export type ScientificRuntimeRequestOptions = { fetchImpl?: typeof fetch; signal?: AbortSignal };
const endpoint = '/api/preferences/scientific-runtimes';

async function request(options: ScientificRuntimeRequestOptions, method = 'GET', body?: unknown): Promise<unknown> {
  const response = await (options.fetchImpl ?? fetch)(endpoint, {
    method,
    signal: options.signal,
    credentials: 'include',
    headers: { Accept: 'application/json', ...(body ? { 'Content-Type': 'application/json' } : {}) },
    ...(body ? { body: JSON.stringify(body) } : {}),
  });
  const payload: unknown = await response.json();
  if (!response.ok) throw new BackendHttpError({ method, path: endpoint, status: response.status, body: payload });
  return payload;
}
const record = (value: unknown): Record<string, unknown> | null =>
  value !== null && typeof value === 'object' && !Array.isArray(value) ? (value as Record<string, unknown>) : null;
const text = (value: unknown): string | undefined =>
  typeof value === 'string' && value.trim() ? value.trim() : undefined;
const nonnegative = (value: unknown): number | undefined =>
  typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : undefined;

export async function loadScientificRuntimeSettings(
  options: ScientificRuntimeRequestOptions = {}
): Promise<ScientificRuntimeSettings> {
  const payload = record(await request(options));
  if (!payload || !Array.isArray(payload.options)) throw new Error('Scientific runtime settings are invalid');
  const entries = payload.options.map((raw): ScientificRuntimeOption => {
    const item = record(raw);
    const runtime = record(item?.runtime);
    const id = text(item?.id);
    const status = text(runtime?.status);
    const bytes = nonnegative(item?.estimated_install_bytes);
    const mb = nonnegative(item?.estimated_install_mb);
    if (
      !item ||
      !id ||
      !status ||
      !bytes ||
      !mb ||
      !Number.isSafeInteger(bytes) ||
      !Number.isSafeInteger(mb) ||
      typeof item.default_enabled !== 'boolean' ||
      typeof item.selected !== 'boolean' ||
      typeof item.available !== 'boolean'
    ) {
      throw new Error('Scientific runtime option is invalid');
    }
    const percent = nonnegative(runtime?.phase_percent);
    return {
      id,
      status,
      estimatedInstallBytes: bytes,
      estimatedInstallMB: mb,
      defaultEnabled: item.default_enabled,
      selected: item.selected,
      available: item.available,
      environment: text(runtime?.environment),
      errorCode: text(runtime?.last_error_code),
      phase: text(runtime?.phase),
      phasePercent: percent != null && percent <= 100 ? percent : undefined,
      attempt: nonnegative(runtime?.attempt),
      updatedAt: text(runtime?.updated_at),
    };
  });
  return { configured: payload.configured === true, options: entries };
}

export async function saveScientificRuntimeSelection(
  enabled: Record<string, boolean>,
  options: ScientificRuntimeRequestOptions = {}
): Promise<void> {
  await request(options, 'PUT', {
    enabled_ids: Object.keys(enabled)
      .filter((id) => enabled[id])
      .toSorted(),
  });
}

export async function retryScientificRuntime(id: string, options: ScientificRuntimeRequestOptions = {}): Promise<void> {
  await request(options, 'POST', { id });
}

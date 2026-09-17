export type SynonBiomedThirdPartyLicenses = {
  title: string;
  source: string;
  bytes: number;
  sha256: string;
  content: string;
};

export type SynonBiomedLicenseRequestOptions = {
  baseUrl?: string;
  fetchImpl?: typeof fetch;
  signal?: AbortSignal;
};

export async function loadSynonBiomedThirdPartyLicenses(
  options: SynonBiomedLicenseRequestOptions = {}
): Promise<SynonBiomedThirdPartyLicenses> {
  const fetchImpl = options.fetchImpl ?? fetch;
  const response = await fetchImpl(`${normalizeBaseUrl(options.baseUrl)}/api/synonbiomed/licenses/third-party`, {
    credentials: 'same-origin',
    headers: { accept: 'application/json' },
    signal: options.signal,
  });

  if (!response.ok) throw new Error(`Third-party license inventory request failed (${response.status})`);
  const payload: unknown = await response.json();
  if (!isLicenseSnapshot(payload)) throw new Error('Third-party license inventory response is invalid');
  return payload;
}

function normalizeBaseUrl(baseUrl?: string): string {
  return (baseUrl ?? '').replace(/\/$/, '');
}

function isLicenseSnapshot(value: unknown): value is SynonBiomedThirdPartyLicenses {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false;
  const record = value as Record<string, unknown>;
  return (
    typeof record.title === 'string' &&
    typeof record.source === 'string' &&
    typeof record.bytes === 'number' &&
    Number.isSafeInteger(record.bytes) &&
    record.bytes >= 0 &&
    typeof record.sha256 === 'string' &&
    /^[a-f0-9]{64}$/.test(record.sha256) &&
    typeof record.content === 'string'
  );
}

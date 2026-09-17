import { readSynonBiomedFileImportResponse } from './synonBiomedFileImportError';

export type SynonBiomedComputeCredentialStatus = {
  name: string;
  resolved: boolean;
};

export type SynonBiomedComputeSshOverrides = {
  user?: string;
  port?: number;
  identityFile?: string;
};

export type SynonBiomedComputeProvider = {
  name: string;
  displayName: string;
  family: string;
  checked: boolean;
  enabled?: boolean;
  probeError?: string;
  credentialStatus?: SynonBiomedComputeCredentialStatus;
  location?: string;
  endpoint?: string;
  skillName?: string;
  credentialName?: string;
  detailsMd?: string;
  probedAt?: string;
  scheduler?: string;
  home?: string;
  scratchRoot?: string | null;
  scratchRootSource?: string;
  dataRoots: string[];
  maxConcurrentJobs?: number | null;
  maxTimeoutSec?: number | null;
  appName?: string;
  environmentName?: string;
  egressPolicy?: SynonBiomedModalEgressPolicy | null;
  sshOverrides?: SynonBiomedComputeSshOverrides;
  managedFamily?: boolean;
};

export type SynonBiomedSshAlias = {
  alias: string;
  hostName?: string;
  user?: string;
  port?: number;
};

export type SynonBiomedSshAliasesSnapshot = {
  aliases: SynonBiomedSshAlias[];
  configFound: boolean;
  configPath: string;
  wildcardCount: number;
  isWsl: boolean;
};

export type SynonBiomedSshHostInput = {
  alias: string;
  initialContext?: string;
  dataRoots?: string[];
  overrides?: SynonBiomedComputeSshOverrides;
};

export type SynonBiomedInferenceProviderInput = {
  name: string;
  endpoint: string | number;
  skillName: string;
  credentialName?: string;
};

export type SynonBiomedComputeProviderDetailsInput = {
  detailsMd?: string;
  maxConcurrentJobs?: number | null;
  maxTimeoutSec?: number | null;
  appName?: string | null;
  environmentName?: string | null;
  egressPolicy?: SynonBiomedModalEgressPolicy | null;
  scratchRoot?: string | null;
  dataRoots?: string[];
};

export type SynonBiomedModalEgressPolicy = {
  mode: 'unrestricted' | 'allowlist' | 'blocked';
  mirror?: boolean;
  additional?: string[];
};

export type SynonBiomedModalProfile = {
  name: string;
  active: boolean;
  tokenIdMasked?: string;
};

export type SynonBiomedModalSettings = {
  provider: string;
  enabled: boolean;
  detailsMd: string;
  appName: string;
  environmentName: string;
  egressPolicy: SynonBiomedModalEgressPolicy | null;
  maxConcurrentJobs: number | null;
  maxTimeoutSec: number | null;
  profiles: SynonBiomedModalProfile[];
  ignoredProfiles: string[];
  tomlMissing: boolean;
  credsError: string | null;
  hasStoredCredential: boolean;
};

export type SynonBiomedBioNemoSettings = {
  enabled: boolean;
  override: boolean | null;
  mode: 'hosted' | 'local';
  hostedHost: string;
};

export type SynonBiomedComputeProbeResult = {
  scheduler?: string;
  cpus?: number;
  gpus?: number;
  memMb?: number;
  packageManagers?: string[];
};

export type SynonBiomedComputeGpuInfo = {
  available: boolean;
  name: string | null;
  memoryMb: number | null;
  cudaVersion: string | null;
  count: number;
};

export type SynonBiomedComputeGpuEnabled = {
  enabled: boolean;
  override: boolean | null;
  present: boolean;
  name: string | null;
};

export type SynonBiomedManagedEndpoint = {
  name: string;
  displayName: string;
  location: 'local' | 'remote' | string;
  state: string;
  port: number | null;
  endpoint: string | null;
  serviceDir: string | null;
  serviceDirBytes: number | null;
  lastError: string | null;
  url: string | null;
  skillName: string | null;
  credentialName: string | null;
  livePath: string | null;
  transcript: string | null;
  stateChangedAt: string | null;
};

export type SynonBiomedComputeJobHarvestFile = {
  exists: boolean;
  size: number;
};

export type SynonBiomedComputeJob = {
  jobId: string;
  environment: string;
  tierType: string;
  provider: string;
  frameId: string | null;
  projectId: string;
  state: string;
  startedAt: string | null;
  startedAtIso: string | null;
  intent: unknown;
  hardwareDetails: unknown;
  originToolUseId: string | null;
  rootFrameId: string | null;
  providerFamily: string;
  providerLabel: string;
  externalId: string | null;
  externalUrl: string | null;
  supportsTail: boolean;
  endedAtIso: string | null;
  harvest: { stdout: SynonBiomedComputeJobHarvestFile; stderr: SynonBiomedComputeJobHarvestFile } | null;
  errorKind: string | null;
  leftOnRemote: string[];
  systemHint: string | null;
};

export type SynonBiomedComputeJobLog = {
  exists: boolean;
  size: number;
  content: string;
  truncated: boolean;
};

export type SynonBiomedHostFileEntry = {
  name: string;
  isDirectory: boolean;
  size: number;
  mtime: number | null;
};

export type SynonBiomedHostDirectory = {
  entries: SynonBiomedHostFileEntry[];
  truncated: boolean;
  resolvedPath: string;
  roots: Record<string, string>;
};

export type SynonBiomedLocalHostInfo = {
  hostLabel: string;
  hostDetail: string;
};

export type SynonBiomedHostFileImport = {
  artifactId: string;
  versionId: string | null;
  filename: string;
};

type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

const COMPUTE_PROVIDERS_ENDPOINT = '/api/compute/providers';
const COMPUTE_SSH_HOSTS_ENDPOINT = '/api/compute/ssh-hosts';
const COMPUTE_INFERENCE_PROVIDERS_ENDPOINT = '/api/compute/inference-providers';
const COMPUTE_SSH_ALIASES_ENDPOINT = '/api/compute/ssh-config-aliases';
const COMPUTE_GPU_ENDPOINT = '/api/compute/gpu';
const COMPUTE_GPU_ENABLED_ENDPOINT = '/api/compute/gpu/enabled';
const COMPUTE_MANAGED_ENDPOINTS_ENDPOINT = '/api/compute/managed-endpoints';
const COMPUTE_JOBS_ENDPOINT = '/api/compute/jobs';
const COMPUTE_SESSION_ENDPOINT = '/api/compute/session';
const COMPUTE_MODAL_ENDPOINT = '/api/compute/byoc/modal';
const COMPUTE_BIONEMO_ENDPOINT = '/api/compute/bionemo/enabled';

export async function loadSynonBiomedModalSettings(fetchImpl: FetchLike = fetch): Promise<SynonBiomedModalSettings> {
  const record = objectRecord(await requestJson<unknown>(COMPUTE_MODAL_ENDPOINT, undefined, fetchImpl));
  if (!record) throw new Error('Synon Biomed returned invalid Modal settings');
  return {
    provider: stringValue(record.provider) || 'modal',
    enabled: record.enabled === true,
    detailsMd: stringValue(record.detailsMd),
    appName: stringValue(record.appName),
    environmentName: stringValue(record.environmentName),
    egressPolicy: toModalEgressPolicy(record.egressPolicy),
    maxConcurrentJobs: nullableNumber(record.maxConcurrentJobs) ?? null,
    maxTimeoutSec: nullableNumber(record.maxTimeoutSec) ?? null,
    profiles: Array.isArray(record.profiles)
      ? record.profiles.map(toModalProfile).filter((profile): profile is SynonBiomedModalProfile => profile !== null)
      : [],
    ignoredProfiles: stringArray(record.ignoredProfiles),
    tomlMissing: record.tomlMissing === true,
    credsError: nullableString(record.credsError) ?? null,
    hasStoredCredential: record.hasStoredCredential === true,
  };
}

export async function setSynonBiomedModalEnabled(enabled: boolean, fetchImpl: FetchLike = fetch): Promise<void> {
  await requestJson(`${COMPUTE_MODAL_ENDPOINT}/enabled`, jsonRequest('PUT', { enabled }), fetchImpl);
}

export async function loadSynonBiomedBioNemoSettings(
  fetchImpl: FetchLike = fetch
): Promise<SynonBiomedBioNemoSettings> {
  const record = objectRecord(await requestJson<unknown>(COMPUTE_BIONEMO_ENDPOINT, undefined, fetchImpl));
  if (!record) throw new Error('Synon Biomed returned invalid BioNeMo settings');
  const mode = record.mode === 'local' ? 'local' : 'hosted';
  return {
    enabled: record.enabled === true,
    override: typeof record.override === 'boolean' ? record.override : null,
    mode,
    hostedHost: stringValue(record.hostedHost) || 'health.api.nvidia.com',
  };
}

export async function setSynonBiomedBioNemoSettings(
  input: Pick<SynonBiomedBioNemoSettings, 'enabled' | 'mode' | 'hostedHost'>,
  fetchImpl: FetchLike = fetch
): Promise<SynonBiomedBioNemoSettings> {
  const record = objectRecord(
    await requestJson<unknown>(COMPUTE_BIONEMO_ENDPOINT, jsonRequest('PUT', input), fetchImpl)
  );
  if (!record) throw new Error('Synon Biomed returned invalid BioNeMo settings');
  return {
    enabled: record.enabled === true,
    override: typeof record.override === 'boolean' ? record.override : null,
    mode: record.mode === 'local' ? 'local' : 'hosted',
    hostedHost: stringValue(record.hostedHost) || input.hostedHost,
  };
}

export async function loadSynonBiomedComputeGpuInfo(fetchImpl: FetchLike = fetch): Promise<SynonBiomedComputeGpuInfo> {
  const record = objectRecord(await requestJson<unknown>(COMPUTE_GPU_ENDPOINT, undefined, fetchImpl));
  if (!record) throw new Error('Synon Biomed returned invalid GPU information');
  return {
    available: record.available === true,
    name: nullableString(record.gpu_name ?? record.name) ?? null,
    memoryMb: nullableNumber(record.gpu_memory_mb ?? record.memoryMb) ?? null,
    cudaVersion: nullableString(record.cuda_version ?? record.cudaVersion) ?? null,
    count: numberValue(record.gpu_count ?? record.count) ?? 0,
  };
}

export async function loadSynonBiomedComputeGpuEnabled(
  fetchImpl: FetchLike = fetch
): Promise<SynonBiomedComputeGpuEnabled> {
  const record = objectRecord(await requestJson<unknown>(COMPUTE_GPU_ENABLED_ENDPOINT, undefined, fetchImpl));
  if (!record) throw new Error('Synon Biomed returned an invalid GPU enabled state');
  return {
    enabled: record.enabled === true,
    override: typeof record.override === 'boolean' ? record.override : null,
    present: record.present === true,
    name: nullableString(record.name) ?? null,
  };
}

export async function setSynonBiomedComputeGpuEnabled(enabled: boolean, fetchImpl: FetchLike = fetch): Promise<void> {
  await requestJson(COMPUTE_GPU_ENABLED_ENDPOINT, jsonRequest('PUT', { enabled }), fetchImpl);
}

export async function loadSynonBiomedManagedEndpoints(
  options: { includeSizes?: boolean } = {},
  fetchImpl: FetchLike = fetch
): Promise<SynonBiomedManagedEndpoint[]> {
  const endpoint = `${COMPUTE_MANAGED_ENDPOINTS_ENDPOINT}${options.includeSizes ? '?sizes=1' : ''}`;
  const payload = await requestJson<unknown>(endpoint, undefined, fetchImpl);
  if (!Array.isArray(payload)) return [];
  return payload
    .map(toManagedEndpoint)
    .filter((managedEndpoint): managedEndpoint is SynonBiomedManagedEndpoint => managedEndpoint !== null);
}

export async function stopSynonBiomedManagedEndpoint(name: string, fetchImpl: FetchLike = fetch): Promise<void> {
  await requestJson(
    `${COMPUTE_MANAGED_ENDPOINTS_ENDPOINT}/${encodeURIComponent(name)}/stop`,
    { method: 'POST', headers: { Accept: 'application/json' } },
    fetchImpl
  );
}

export async function loadSynonBiomedComputeJobs(
  projectId: string,
  fetchImpl: FetchLike = fetch
): Promise<SynonBiomedComputeJob[]> {
  const payload = await requestJson<unknown>(
    `${COMPUTE_JOBS_ENDPOINT}?projectId=${encodeURIComponent(projectId)}`,
    undefined,
    fetchImpl
  );
  const rows = Array.isArray(payload) ? payload : objectRecord(payload)?.jobs;
  if (!Array.isArray(rows)) return [];
  return rows.map(toComputeJob).filter((job): job is SynonBiomedComputeJob => job !== null);
}

export async function loadSynonBiomedComputeJob(
  jobId: string,
  fetchImpl: FetchLike = fetch
): Promise<SynonBiomedComputeJob> {
  const payload = await requestJson<unknown>(
    `${COMPUTE_JOBS_ENDPOINT}/${encodeURIComponent(jobId)}`,
    undefined,
    fetchImpl
  );
  if (payload === null) throw new Error(`Synon Biomed compute job does not exist: ${jobId}`);
  const job = toComputeJob(payload);
  if (!job) throw new Error(`Synon Biomed returned an invalid compute job: ${jobId}`);
  return job;
}

export async function loadSynonBiomedComputeJobLog(
  jobId: string,
  options: { stream?: 'stdout' | 'stderr'; tail?: number } = {},
  fetchImpl: FetchLike = fetch
): Promise<SynonBiomedComputeJobLog> {
  const params = new URLSearchParams();
  if (options.stream) params.set('stream', options.stream);
  if (options.tail !== undefined) params.set('tail', String(options.tail));
  const suffix = params.size > 0 ? `?${params.toString()}` : '';
  const payload = await requestJson<unknown>(
    `${COMPUTE_JOBS_ENDPOINT}/${encodeURIComponent(jobId)}/logs${suffix}`,
    undefined,
    fetchImpl
  );
  if (typeof payload === 'string') return { exists: true, size: payload.length, content: payload, truncated: false };
  const record = objectRecord(payload);
  if (!record) throw new Error(`Synon Biomed returned invalid compute job logs: ${jobId}`);
  return {
    exists: record.exists !== false,
    size: numberValue(record.size) ?? 0,
    content: stringValue(record.content ?? record.text ?? record.log),
    truncated: record.truncated === true,
  };
}

export async function loadSynonBiomedSessionComputeProviders(
  rootFrameId: string,
  fetchImpl: FetchLike = fetch
): Promise<string[]> {
  return stringArray(
    await requestJson<unknown>(
      `${COMPUTE_SESSION_ENDPOINT}/${encodeURIComponent(rootFrameId)}/enabled`,
      undefined,
      fetchImpl
    )
  );
}

export async function setSynonBiomedSessionComputeProvider(
  rootFrameId: string,
  providerName: string,
  checked: boolean,
  fetchImpl: FetchLike = fetch
): Promise<void> {
  await requestJson(
    `${COMPUTE_SESSION_ENDPOINT}/${encodeURIComponent(rootFrameId)}/enabled/${encodeURIComponent(providerName)}`,
    jsonRequest('PUT', { checked }),
    fetchImpl
  );
}

export async function loadSynonBiomedComputeProviders(
  fetchImpl: FetchLike = fetch
): Promise<SynonBiomedComputeProvider[]> {
  const payload = await requestJson<unknown>(COMPUTE_PROVIDERS_ENDPOINT, undefined, fetchImpl);
  if (!Array.isArray(payload)) return [];
  return payload.map(toComputeProvider).filter((provider): provider is SynonBiomedComputeProvider => Boolean(provider));
}

export async function loadSynonBiomedComputeProvider(
  name: string,
  fetchImpl: FetchLike = fetch
): Promise<SynonBiomedComputeProvider> {
  const payload = await requestJson<unknown>(providerEndpoint(name), undefined, fetchImpl);
  const provider = toComputeProvider(payload);
  if (!provider) throw new Error(`Synon Biomed returned an invalid compute provider: ${name}`);
  return provider;
}

export async function loadSynonBiomedSshAliases(fetchImpl: FetchLike = fetch): Promise<SynonBiomedSshAliasesSnapshot> {
  const payload = await requestJson<unknown>(COMPUTE_SSH_ALIASES_ENDPOINT, undefined, fetchImpl);
  const record = objectRecord(payload);
  const aliases = Array.isArray(record?.aliases)
    ? record.aliases.map(toSshAlias).filter((alias): alias is SynonBiomedSshAlias => Boolean(alias))
    : [];
  return {
    aliases,
    configFound: record?.configFound === true,
    configPath: stringValue(record?.configPath),
    wildcardCount: numberValue(record?.wildcardCount) ?? 0,
    isWsl: record?.isWsl === true,
  };
}

export async function addSynonBiomedSshHost(
  input: SynonBiomedSshHostInput,
  fetchImpl: FetchLike = fetch
): Promise<void> {
  await requestJson(COMPUTE_SSH_HOSTS_ENDPOINT, jsonRequest('POST', compactObject(input)), fetchImpl);
}

export async function addSynonBiomedInferenceProvider(
  input: SynonBiomedInferenceProviderInput,
  fetchImpl: FetchLike = fetch
): Promise<{ warning?: string }> {
  return requestJson<{ warning?: string }>(
    COMPUTE_INFERENCE_PROVIDERS_ENDPOINT,
    jsonRequest('POST', compactObject(input)),
    fetchImpl
  );
}

export async function probeSynonBiomedComputeProvider(
  name: string,
  fetchImpl: FetchLike = fetch
): Promise<SynonBiomedComputeProbeResult> {
  return requestJson<SynonBiomedComputeProbeResult>(
    providerEndpoint(name, '/probe'),
    { method: 'POST', headers: { Accept: 'application/json' } },
    fetchImpl
  );
}

export async function saveSynonBiomedComputeProviderDetails(
  name: string,
  input: SynonBiomedComputeProviderDetailsInput,
  fetchImpl: FetchLike = fetch
): Promise<void> {
  const detailsPatch = compactObject({
    detailsMd: input.detailsMd,
    maxConcurrentJobs: input.maxConcurrentJobs,
    maxTimeoutSec: input.maxTimeoutSec,
    appName: input.appName,
    environmentName: input.environmentName,
    egressPolicy: input.egressPolicy,
  });
  if (Object.keys(detailsPatch).length > 0) {
    await requestJson(providerEndpoint(name), jsonRequest('PATCH', detailsPatch), fetchImpl);
  }
  if (Object.prototype.hasOwnProperty.call(input, 'scratchRoot')) {
    await requestJson(
      providerEndpoint(name, '/scratch-root'),
      jsonRequest('PUT', { scratchRoot: input.scratchRoot ?? null }),
      fetchImpl
    );
  }
  if (input.dataRoots !== undefined) {
    await requestJson(providerEndpoint(name, '/data-roots'), jsonRequest('PUT', { roots: input.dataRoots }), fetchImpl);
  }
}

export async function deleteSynonBiomedComputeProvider(
  provider: Pick<SynonBiomedComputeProvider, 'name' | 'family'>,
  fetchImpl: FetchLike = fetch
): Promise<void> {
  const endpoint =
    provider.family === 'infer' || provider.name.startsWith('infer:')
      ? `${COMPUTE_INFERENCE_PROVIDERS_ENDPOINT}/${encodeURIComponent(provider.name)}`
      : providerEndpoint(provider.name);
  await requestJson(endpoint, { method: 'DELETE', headers: { Accept: 'application/json' } }, fetchImpl);
}

export async function loadSynonBiomedLocalHostInfo(fetchImpl: FetchLike = fetch): Promise<SynonBiomedLocalHostInfo> {
  const record = objectRecord(await requestJson<unknown>('/api/compute/local/hostinfo', undefined, fetchImpl));
  if (!record) throw new Error('Synon Biomed returned invalid local host information');
  return {
    hostLabel: stringValue(record.hostLabel) || 'Local',
    hostDetail: stringValue(record.hostDetail),
  };
}

export async function loadSynonBiomedLocalDirectory(
  path: string | undefined,
  fetchImpl: FetchLike = fetch
): Promise<SynonBiomedHostDirectory> {
  return loadHostDirectory('/api/compute/local/files', path, fetchImpl);
}

export async function loadSynonBiomedRemoteDirectory(
  providerName: string,
  path: string | undefined,
  fetchImpl: FetchLike = fetch
): Promise<SynonBiomedHostDirectory> {
  return loadHostDirectory(providerEndpoint(providerName, '/files'), path, fetchImpl);
}

export function getSynonBiomedLocalFileUrl(
  path: string,
  disposition: 'attachment' | 'inline' = 'attachment',
  baseUrl = ''
): string {
  return hostFileUrl(baseUrl, '/api/compute/local/download', { path, disposition });
}

export function getSynonBiomedRemoteFileUrl(
  providerName: string,
  path: string,
  disposition: 'attachment' | 'inline' = 'attachment',
  baseUrl = ''
): string {
  return hostFileUrl(baseUrl, providerEndpoint(providerName, '/download'), { path, disposition });
}

export async function importSynonBiomedLocalFile(
  path: string,
  projectId: string,
  fetchImpl: FetchLike = fetch
): Promise<SynonBiomedHostFileImport> {
  return importHostFile('/api/compute/local/import', path, projectId, fetchImpl);
}

export async function importSynonBiomedRemoteFile(
  providerName: string,
  path: string,
  projectId: string,
  fetchImpl: FetchLike = fetch
): Promise<SynonBiomedHostFileImport> {
  return importHostFile(providerEndpoint(providerName, '/import'), path, projectId, fetchImpl);
}

async function loadHostDirectory(
  endpoint: string,
  path: string | undefined,
  fetchImpl: FetchLike
): Promise<SynonBiomedHostDirectory> {
  const query = path ? '?path=' + encodeURIComponent(path) : '';
  const record = objectRecord(await requestJson<unknown>(endpoint + query, undefined, fetchImpl));
  if (!record) throw new Error('Synon Biomed returned an invalid directory response');
  return {
    entries: Array.isArray(record.entries)
      ? record.entries.map(toHostFileEntry).filter((entry): entry is SynonBiomedHostFileEntry => entry !== null)
      : [],
    truncated: record.truncated === true,
    resolvedPath: stringValue(record.resolvedPath),
    roots: stringRecord(record.roots),
  };
}

async function importHostFile(
  endpoint: string,
  path: string,
  projectId: string,
  fetchImpl: FetchLike
): Promise<SynonBiomedHostFileImport> {
  const response = await fetchImpl(endpoint, jsonRequest('POST', { path, projectId }));
  const record = objectRecord(await readSynonBiomedFileImportResponse(response));
  const artifactId = stringValue(record?.artifactId ?? record?.artifact_id);
  if (!artifactId) throw new Error('Synon Biomed import response is missing an artifact ID');
  return {
    artifactId,
    versionId: nullableString(record?.versionId ?? record?.version_id) ?? null,
    filename: stringValue(record?.filename) || path.split('/').at(-1) || path,
  };
}

function hostFileUrl(baseUrl: string, endpoint: string, query: Record<string, string>): string {
  return baseUrl.replace(/\/+$/, '') + endpoint + '?' + new URLSearchParams(query).toString();
}

function providerEndpoint(name: string, suffix = ''): string {
  return `${COMPUTE_PROVIDERS_ENDPOINT}/${encodeURIComponent(name)}${suffix}`;
}

async function requestJson<T = unknown>(
  endpoint: string,
  init?: RequestInit,
  fetchImpl: FetchLike = fetch
): Promise<T> {
  const response = await fetchImpl(endpoint, init ?? { headers: { Accept: 'application/json' } });
  const text = await response.text();
  if (!response.ok) {
    const error = new Error(readBackendError(text, response.status)) as Error & { status: number };
    error.status = response.status;
    throw error;
  }
  if (!text) return undefined as T;
  try {
    return JSON.parse(text) as T;
  } catch {
    throw new Error(`Synon Biomed compute API returned invalid JSON: ${endpoint}`);
  }
}

function jsonRequest(method: 'POST' | 'PATCH' | 'PUT', body: unknown): RequestInit {
  return {
    method,
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  };
}

function readBackendError(text: string, status: number): string {
  if (text) {
    try {
      const payload = JSON.parse(text) as Record<string, unknown>;
      const message = stringValue(payload.detail) || stringValue(payload.message) || stringValue(payload.error);
      if (message) return message;
    } catch {
      return text;
    }
  }
  return `Synon Biomed compute API request failed: ${status}`;
}

function toComputeProvider(value: unknown): SynonBiomedComputeProvider | null {
  const record = objectRecord(value);
  if (!record) return null;
  const name = stringValue(record.name);
  if (!name) return null;

  const inferConfig = parseObject(record.inferConfig);
  const credentialStatus = toCredentialStatus(record.credentialStatus);
  const family = stringValue(record.family) || name.split(':', 1)[0] || 'unknown';
  return {
    name,
    displayName: stringValue(record.displayName) || name.replace(/^[^:]+:/, ''),
    family,
    checked: record.checked === true,
    enabled: optionalBoolean(record.enabled),
    probeError: optionalString(record.probeError),
    credentialStatus: credentialStatus ?? undefined,
    location: optionalString(record.location),
    endpoint: optionalString(record.endpoint) ?? optionalString(inferConfig?.url),
    skillName: optionalString(record.skillName) ?? optionalString(inferConfig?.skillName),
    credentialName: optionalString(record.credentialName) ?? optionalString(inferConfig?.credentialName),
    detailsMd: optionalString(record.detailsMd),
    probedAt: optionalString(record.probedAt),
    scheduler: optionalString(record.scheduler),
    home: optionalString(record.home),
    scratchRoot: nullableString(record.scratchRoot),
    scratchRootSource: optionalString(record.scratchRootSource),
    dataRoots: stringArray(record.dataRoots),
    maxConcurrentJobs: nullableNumber(record.maxConcurrentJobs),
    maxTimeoutSec: nullableNumber(record.maxTimeoutSec),
    appName: optionalString(record.appName),
    environmentName: optionalString(record.environmentName),
    egressPolicy: toModalEgressPolicy(record.egressPolicy),
    sshOverrides: toSshOverrides(record.sshOverrides) ?? undefined,
    managedFamily: record.managedFamily === true,
  };
}

function toManagedEndpoint(value: unknown): SynonBiomedManagedEndpoint | null {
  const record = objectRecord(value);
  if (!record) return null;
  const name = stringValue(record.name);
  if (!name) return null;
  return {
    name,
    displayName: stringValue(record.displayName ?? record.display_name) || name,
    location: stringValue(record.location) || 'remote',
    state: stringValue(record.state) || 'unknown',
    port: nullableNumber(record.port) ?? null,
    endpoint: nullableString(record.endpoint) ?? null,
    serviceDir: nullableString(record.serviceDir ?? record.service_dir) ?? null,
    serviceDirBytes: nullableNumber(record.serviceDirBytes ?? record.service_dir_bytes) ?? null,
    lastError: nullableString(record.lastError ?? record.last_error) ?? null,
    url: nullableString(record.url) ?? null,
    skillName: nullableString(record.skillName ?? record.skill_name) ?? null,
    credentialName: nullableString(record.credentialName ?? record.credential_name) ?? null,
    livePath: nullableString(record.livePath ?? record.live_path) ?? null,
    transcript: nullableString(record.transcript) ?? null,
    stateChangedAt: nullableString(record.stateChangedAt ?? record.state_changed_at) ?? null,
  };
}

function toComputeJob(value: unknown): SynonBiomedComputeJob | null {
  const record = objectRecord(value);
  if (!record) return null;
  const jobId = stringValue(record.jobId ?? record.job_id ?? record.id);
  if (!jobId) return null;
  const harvestRecord = objectRecord(record.harvest);
  return {
    jobId,
    environment: stringValue(record.environment),
    tierType: stringValue(record.tierType ?? record.tier_type),
    provider: stringValue(record.provider ?? record.providerName ?? record.provider_name),
    frameId: nullableString(record.frameId ?? record.frame_id) ?? null,
    projectId: stringValue(record.projectId ?? record.project_id),
    state: stringValue(record.state ?? record.status) || 'unknown',
    startedAt: timestampString(record.startedAt ?? record.started_at ?? record.createdAt ?? record.created_at),
    startedAtIso: nullableString(record.startedAtIso ?? record.started_at_iso) ?? null,
    intent: record.intent ?? null,
    hardwareDetails: record.hardwareDetails ?? record.hardware_details ?? null,
    originToolUseId: nullableString(record.originToolUseId ?? record.origin_tool_use_id) ?? null,
    rootFrameId: nullableString(record.rootFrameId ?? record.root_frame_id) ?? null,
    providerFamily: stringValue(record.providerFamily ?? record.provider_family),
    providerLabel: stringValue(record.providerLabel ?? record.provider_label) || stringValue(record.provider),
    externalId: nullableString(record.externalId ?? record.external_id) ?? null,
    externalUrl: nullableString(record.externalUrl ?? record.external_url) ?? null,
    supportsTail: record.supportsTail === true || record.supports_tail === true,
    endedAtIso: nullableString(record.endedAtIso ?? record.ended_at_iso) ?? null,
    harvest: harvestRecord
      ? { stdout: toHarvestFile(harvestRecord.stdout), stderr: toHarvestFile(harvestRecord.stderr) }
      : null,
    errorKind: nullableString(record.errorKind ?? record.error_kind) ?? null,
    leftOnRemote: stringArray(record.leftOnRemote ?? record.left_on_remote),
    systemHint: nullableString(record.systemHint ?? record.system_hint) ?? null,
  };
}

function toHarvestFile(value: unknown): SynonBiomedComputeJobHarvestFile {
  const record = objectRecord(value);
  return { exists: record?.exists === true, size: numberValue(record?.size) ?? 0 };
}

function toCredentialStatus(value: unknown): SynonBiomedComputeCredentialStatus | null {
  const record = objectRecord(value);
  if (!record) return null;
  return { name: stringValue(record.name), resolved: record.resolved === true };
}

function toModalProfile(value: unknown): SynonBiomedModalProfile | null {
  const record = objectRecord(value);
  if (!record) return null;
  const name = stringValue(record.name);
  if (!name) return null;
  return {
    name,
    active: record.active === true,
    tokenIdMasked: optionalString(record.tokenIdMasked),
  };
}

function toModalEgressPolicy(value: unknown): SynonBiomedModalEgressPolicy | null {
  const record = objectRecord(value);
  if (!record) return null;
  const mode = stringValue(record.mode);
  if (mode !== 'unrestricted' && mode !== 'allowlist' && mode !== 'blocked') return null;
  return {
    mode,
    ...(typeof record.mirror === 'boolean' ? { mirror: record.mirror } : {}),
    ...(Array.isArray(record.additional) ? { additional: stringArray(record.additional) } : {}),
  };
}

function toSshAlias(value: unknown): SynonBiomedSshAlias | null {
  if (typeof value === 'string' && value) return { alias: value };
  const record = objectRecord(value);
  if (!record) return null;
  const alias = stringValue(record.alias) || stringValue(record.name) || stringValue(record.host);
  if (!alias) return null;
  return {
    alias,
    hostName: optionalString(record.hostName) ?? optionalString(record.hostname),
    user: optionalString(record.user),
    port: numberValue(record.port),
  };
}

function toSshOverrides(value: unknown): SynonBiomedComputeSshOverrides | null {
  const record = parseObject(value);
  if (!record) return null;
  return compactObject({
    user: optionalString(record.user),
    port: numberValue(record.port),
    identityFile: optionalString(record.identityFile),
  });
}

function parseObject(value: unknown): Record<string, unknown> | null {
  if (typeof value === 'string') {
    try {
      return objectRecord(JSON.parse(value));
    } catch {
      return null;
    }
  }
  return objectRecord(value);
}

function toHostFileEntry(value: unknown): SynonBiomedHostFileEntry | null {
  const record = objectRecord(value);
  const name = stringValue(record?.name);
  if (!name) return null;
  return {
    name,
    isDirectory: record?.isDirectory === true,
    size: numberValue(record?.size) ?? 0,
    mtime: numberValue(record?.mtime) ?? null,
  };
}

function stringRecord(value: unknown): Record<string, string> {
  const record = objectRecord(value);
  if (!record) return {};
  return Object.fromEntries(
    Object.entries(record).filter((entry): entry is [string, string] => typeof entry[1] === 'string')
  );
}

function objectRecord(value: unknown): Record<string, unknown> | null {
  return value && typeof value === 'object' && !Array.isArray(value) ? (value as Record<string, unknown>) : null;
}

function compactObject<T extends Record<string, unknown>>(value: T): T {
  return Object.fromEntries(Object.entries(value).filter(([, item]) => item !== undefined && item !== '')) as T;
}

function stringArray(value: unknown): string[] {
  if (typeof value === 'string') {
    try {
      return stringArray(JSON.parse(value));
    } catch {
      return [];
    }
  }
  return Array.isArray(value)
    ? value.filter((item): item is string => typeof item === 'string' && item.length > 0)
    : [];
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value : '';
}

function optionalString(value: unknown): string | undefined {
  return typeof value === 'string' && value.length > 0 ? value : undefined;
}

function nullableString(value: unknown): string | null | undefined {
  return value === null ? null : optionalString(value);
}

function numberValue(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined;
}

function nullableNumber(value: unknown): number | null | undefined {
  return value === null ? null : numberValue(value);
}

function optionalBoolean(value: unknown): boolean | undefined {
  return typeof value === 'boolean' ? value : undefined;
}

function timestampString(value: unknown): string | null {
  return typeof value === 'string' ? value : typeof value === 'number' && Number.isFinite(value) ? String(value) : null;
}

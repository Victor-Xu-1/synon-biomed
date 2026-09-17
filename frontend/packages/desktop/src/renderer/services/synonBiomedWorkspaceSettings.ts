import { readSynonBiomedFileImportResponse } from './synonBiomedFileImportError';

export type SynonBiomedSettingsOptions = {
  baseUrl?: string;
  fetchImpl?: typeof fetch;
};

export type SynonBiomedAllowlistGroup = {
  id: string;
  label: string;
  description: string;
  locked: boolean;
  domains: string[];
};

export type SynonBiomedNetworkSettings = {
  groups: SynonBiomedAllowlistGroup[];
  disabledGroups: string[];
  activeKernelCount: number;
  hasSeenOnboarding: boolean;
  domains: string[];
  configDomains: string[];
  deniedDomains: string[];
};

export type SynonBiomedHostGrant = {
  id: string;
  hostPath: string;
  mountName: string;
  guestPath: string;
  mode: 'ro' | 'rw' | string;
};

export type SynonBiomedSecret = {
  id: string;
  provider: string;
  name: string;
  description: string;
  credentialType: string;
  buckets: string[];
  region: string;
  maskedPreview: string;
  maskedFields: string[];
  valueConfigured: boolean;
  credentialsConfigured: boolean;
  credentialFields: string[];
  createdAt: string;
  updatedAt: string;
};

export type SynonBiomedSecretInput = {
  provider: string;
  name: string;
  value?: string;
  credentials?: Record<string, string>;
  description?: string;
  credentialType?: string;
  buckets?: string[];
  region?: string;
};

export type SynonBiomedDataDirectoryMove = {
  id: string;
  source: string;
  target: string;
  migrate: boolean;
  copied: boolean;
  stagedAt: string;
  completedAt: string | null;
};

export type SynonBiomedDataDirectory = {
  current: string;
  resolved: string | null;
  defaultPath: string;
  source: string;
  usageBytes: number;
  freeBytes: number;
  activeFrames: number;
  configPath: string;
  pendingMove: SynonBiomedDataDirectoryMove | null;
  lastMove: SynonBiomedDataDirectoryMove | null;
};

export type SynonBiomedStorageRuleValues = {
  taskArtifacts: string;
  logs: string;
  toolResults: string;
  temp: string;
};

export type SynonBiomedStorageRules = {
  root: string;
  rules: SynonBiomedStorageRuleValues;
  paths: SynonBiomedStorageRuleValues;
  systemPaths: {
    taskRuns: string;
    workspace: string;
  };
};

export type SynonBiomedDiskUsage = {
  artifactsBytes: number;
  workspaceBytes: number;
  toolResultsBytes: number;
  condaBytes: number;
  availableBytes: number;
};

export type SynonBiomedCloudCredential = {
  id: string;
  provider: string;
  name: string;
  credentialType: string;
  connected: boolean;
  defaultBucket: string | null;
  region: string;
};

export type SynonBiomedCloudObject = {
  key: string;
  size: number;
  lastModified: string;
};

export type SynonBiomedCloudFolder = {
  folders: string[];
  files: SynonBiomedCloudObject[];
};

export type SynonBiomedCloudExportItem = {
  artifactId: string;
  key: string;
};

export type SynonBiomedCloudExportFailure = SynonBiomedCloudExportItem & {
  error: string;
};

export type SynonBiomedCloudBatchExportResult = {
  completed: SynonBiomedCloudExportItem[];
  failed: SynonBiomedCloudExportFailure[];
};

export type SynonBiomedCloudBatchExportProgress = {
  completed: number;
  total: number;
  current: SynonBiomedCloudExportItem;
  error?: string;
};

export type SynonBiomedUseIntent = {
  intent: 'commercial' | 'noncommercial';
  declared: boolean;
};

export type SynonBiomedContactEmail = {
  decision: 'allowed' | 'declined' | 'revoked' | null;
  email: string | null;
  noticeText: string;
  noticeVersion: string;
  noticeStale: boolean;
};

type RecordValue = Record<string, unknown>;

export async function loadSynonBiomedNetworkSettings(
  options: SynonBiomedSettingsOptions = {}
): Promise<SynonBiomedNetworkSettings> {
  const [builtinPayload, domainsPayload] = await Promise.all([
    requestJson('/api/preferences/builtin-allowlist', options),
    requestJson('/api/preferences/allowed-domains', options),
  ]);
  const builtin = asRecord(builtinPayload);
  const domains = asRecord(domainsPayload);
  return {
    groups: arrayValue(builtin?.groups).map(toAllowlistGroup).filter(isPresent),
    disabledGroups: stringArray(builtin?.disabledGroups),
    activeKernelCount: numberValue(builtin?.activeKernelCount),
    hasSeenOnboarding: builtin?.hasSeenOnboarding === true,
    domains: stringArray(domains?.domains),
    configDomains: stringArray(domains?.configDomains),
    deniedDomains: stringArray(domains?.deniedDomains),
  };
}

export async function updateSynonBiomedAllowlistGroups(
  disabledGroups: string[],
  options: SynonBiomedSettingsOptions = {}
): Promise<void> {
  await requestJson('/api/preferences/builtin-allowlist/disabled-groups', options, {
    method: 'PUT',
    body: JSON.stringify({ disabledGroups }),
  });
}

export async function addSynonBiomedAllowedDomain(
  domain: string,
  options: SynonBiomedSettingsOptions = {}
): Promise<void> {
  await requestJson('/api/preferences/allowed-domains', options, {
    method: 'POST',
    body: JSON.stringify({ domain }),
  });
}

export async function replaceSynonBiomedAllowedDomains(
  domains: string[],
  options: SynonBiomedSettingsOptions = {}
): Promise<void> {
  await requestJson('/api/preferences/allowed-domains', options, {
    method: 'PUT',
    body: JSON.stringify({ domains }),
  });
}

export async function removeSynonBiomedAllowedDomain(
  domain: string,
  options: SynonBiomedSettingsOptions = {}
): Promise<void> {
  await requestJson(`/api/preferences/allowed-domains/${encodeURIComponent(domain)}`, options, { method: 'DELETE' });
}

export async function loadSynonBiomedHostGrants(
  options: SynonBiomedSettingsOptions = {}
): Promise<SynonBiomedHostGrant[]> {
  const payload = asRecord(await requestJson('/api/preferences/host-grants', options));
  return arrayValue(payload?.grants).map(toHostGrant).filter(isPresent);
}

export async function updateSynonBiomedHostGrant(
  hostPath: string,
  mode: 'ro' | 'rw',
  options: SynonBiomedSettingsOptions = {}
): Promise<SynonBiomedHostGrant> {
  return requireHostGrant(
    await requestJson('/api/preferences/host-grants', options, {
      method: 'PATCH',
      body: JSON.stringify({ path: hostPath, mode }),
    })
  );
}

export async function revokeSynonBiomedHostGrant(
  hostPath: string,
  options: SynonBiomedSettingsOptions = {}
): Promise<void> {
  await requestJson('/api/preferences/host-grants', options, {
    method: 'DELETE',
    body: JSON.stringify({ path: hostPath }),
  });
}

export async function loadSynonBiomedSecrets(options: SynonBiomedSettingsOptions = {}): Promise<SynonBiomedSecret[]> {
  const payload = await requestJson('/api/secrets', options);
  const values = Array.isArray(payload) ? payload : arrayValue(asRecord(payload)?.secrets);
  return values.map(toSecret).filter(isPresent);
}

export async function createSynonBiomedSecret(
  input: SynonBiomedSecretInput,
  options: SynonBiomedSettingsOptions = {}
): Promise<void> {
  await requestJson('/api/secrets', options, { method: 'POST', body: JSON.stringify(input) });
}

export async function updateSynonBiomedSecret(
  id: string,
  input: Partial<SynonBiomedSecretInput>,
  options: SynonBiomedSettingsOptions = {}
): Promise<void> {
  await requestJson(`/api/secrets/${encodeURIComponent(id)}`, options, {
    method: 'PATCH',
    body: JSON.stringify(input),
  });
}

export async function deleteSynonBiomedSecret(id: string, options: SynonBiomedSettingsOptions = {}): Promise<void> {
  await requestJson(`/api/secrets/${encodeURIComponent(id)}`, options, { method: 'DELETE' });
}

export async function loadSynonBiomedStorageSettings(options: SynonBiomedSettingsOptions = {}): Promise<{
  dataDirectory: SynonBiomedDataDirectory;
  diskUsage: SynonBiomedDiskUsage;
  cloudCredentials: SynonBiomedCloudCredential[];
}> {
  const [dataDirectory, diskUsage, cloudCredentials] = await Promise.all([
    loadSynonBiomedDataDirectory(options),
    loadSynonBiomedDiskUsage(options),
    loadSynonBiomedCloudCredentials(options),
  ]);
  return { dataDirectory, diskUsage, cloudCredentials };
}

export async function loadSynonBiomedDataDirectory(
  options: SynonBiomedSettingsOptions = {}
): Promise<SynonBiomedDataDirectory> {
  const directoryPayload = await requestJson('/api/settings/data-dir', options);
  const directory = asRecord(directoryPayload);
  return {
    current: stringValue(directory?.current),
    resolved: nullableString(directory?.resolved),
    defaultPath: stringValue(directory?.default),
    source: stringValue(directory?.source),
    usageBytes: numberValue(directory?.usageBytes),
    freeBytes: numberValue(directory?.freeBytes),
    activeFrames: numberValue(directory?.activeFrames),
    configPath: stringValue(directory?.configPath),
    pendingMove: toDataDirectoryMove(directory?.pendingMove),
    lastMove: toDataDirectoryMove(directory?.lastMove),
  };
}

export async function loadSynonBiomedStorageRules(
  options: SynonBiomedSettingsOptions = {}
): Promise<SynonBiomedStorageRules> {
  return toStorageRules(await requestJson('/api/settings/storage-rules', options));
}

export async function saveSynonBiomedStorageRules(
  rules: SynonBiomedStorageRuleValues,
  options: SynonBiomedSettingsOptions = {}
): Promise<SynonBiomedStorageRules> {
  return toStorageRules(
    await requestJson('/api/settings/storage-rules', options, {
      method: 'PUT',
      body: JSON.stringify({ rules }),
    })
  );
}

export async function loadSynonBiomedDiskUsage(
  options: SynonBiomedSettingsOptions = {}
): Promise<SynonBiomedDiskUsage> {
  const usage = asRecord(await requestJson('/api/preferences/disk-usage', options));
  return {
    artifactsBytes: numberValue(asRecord(usage?.artifacts)?.totalBytes),
    workspaceBytes: numberValue(asRecord(usage?.workspace)?.totalBytes),
    toolResultsBytes: numberValue(asRecord(usage?.toolResults)?.totalBytes),
    condaBytes: numberValue(asRecord(usage?.conda)?.totalBytes),
    availableBytes: numberValue(usage?.availableBytes),
  };
}

export async function loadSynonBiomedCloudCredentials(
  options: SynonBiomedSettingsOptions = {}
): Promise<SynonBiomedCloudCredential[]> {
  const payload = await requestJson('/api/cloud-credentials', options);
  const values = Array.isArray(payload) ? payload : arrayValue(asRecord(payload)?.credentials);
  return values.map(toCloudCredential).filter(isPresent);
}

export async function changeSynonBiomedDataDirectory(
  input: { path: string; migrate: boolean; noRestart?: boolean },
  options: SynonBiomedSettingsOptions = {}
): Promise<{ restarting: boolean; restartRequired: boolean; move: SynonBiomedDataDirectoryMove | null }> {
  const payload = asRecord(
    await requestJson('/api/settings/data-dir', options, {
      method: 'POST',
      body: JSON.stringify(input),
    })
  );
  return {
    restarting: payload?.restarting === true,
    restartRequired: payload?.restartRequired === true,
    move: toDataDirectoryMove(payload?.move),
  };
}

export async function clearSynonBiomedLastDataDirectoryMove(
  deleteSource: boolean,
  options: SynonBiomedSettingsOptions = {}
): Promise<void> {
  await requestJson(`/api/settings/data-dir/last-move?deleteSource=${deleteSource ? '1' : '0'}`, options, {
    method: 'DELETE',
  });
}

export async function testSynonBiomedCloudCredential(
  id: string,
  options: SynonBiomedSettingsOptions = {}
): Promise<{ success: boolean; message: string }> {
  const payload = asRecord(
    await requestJson(`/api/cloud-credentials/${encodeURIComponent(id)}/test`, options, { method: 'POST' })
  );
  return { success: payload?.success === true, message: stringValue(payload?.message) };
}

export async function loadSynonBiomedCloudBuckets(
  id: string,
  options: SynonBiomedSettingsOptions = {}
): Promise<string[]> {
  return stringArray(await requestJson(`/api/cloud-credentials/${encodeURIComponent(id)}/buckets`, options));
}

export async function loadSynonBiomedCloudFolder(
  id: string,
  bucket: string,
  prefix: string,
  options: SynonBiomedSettingsOptions = {}
): Promise<SynonBiomedCloudFolder> {
  const query = new URLSearchParams({ bucket, prefix, page_limit: '5000' });
  const payload = asRecord(
    await requestJson(`/api/cloud-credentials/${encodeURIComponent(id)}/folder?${query.toString()}`, options)
  );
  return {
    folders: stringArray(payload?.folders),
    files: arrayValue(payload?.files).map(toCloudObject).filter(isPresent),
  };
}

export function getSynonBiomedCloudDownloadUrl(id: string, bucket: string, key: string, baseUrl = ''): string {
  const query = new URLSearchParams({ bucket, key });
  return `${baseUrl.replace(/\/+$/, '')}/api/cloud-credentials/${encodeURIComponent(id)}/download?${query.toString()}`;
}

export async function importSynonBiomedCloudObject(
  id: string,
  input: { bucket: string; key: string; projectId: string },
  options: SynonBiomedSettingsOptions = {}
): Promise<RecordValue> {
  const path = `/api/cloud-credentials/${encodeURIComponent(id)}/import`;
  const response = await (options.fetchImpl ?? fetch)(`${(options.baseUrl ?? '').replace(/\/+$/, '')}${path}`, {
    method: 'POST',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    body: JSON.stringify({ bucket: input.bucket, key: input.key, project_id: input.projectId }),
  });
  const payload = asRecord(await readSynonBiomedFileImportResponse(response));
  return payload ?? {};
}

export async function exportSynonBiomedArtifactToCloud(
  id: string,
  input: { artifactId: string; bucket: string; key: string },
  options: SynonBiomedSettingsOptions = {}
): Promise<RecordValue> {
  const payload = asRecord(
    await requestJson(`/api/cloud-credentials/${encodeURIComponent(id)}/export`, options, {
      method: 'POST',
      body: JSON.stringify({ artifact_id: input.artifactId, bucket: input.bucket, key: input.key }),
    })
  );
  return payload ?? {};
}

export async function exportSynonBiomedArtifactsToCloud(
  id: string,
  input: { bucket: string; items: SynonBiomedCloudExportItem[] },
  onProgress?: (progress: SynonBiomedCloudBatchExportProgress) => void,
  options: SynonBiomedSettingsOptions = {}
): Promise<SynonBiomedCloudBatchExportResult> {
  const result: SynonBiomedCloudBatchExportResult = { completed: [], failed: [] };
  for (const item of input.items) {
    let error: string | undefined;
    try {
      // oxlint-disable-next-line no-await-in-loop -- preserve progress order and bound remote cloud concurrency.
      await exportSynonBiomedArtifactToCloud(
        id,
        { artifactId: item.artifactId, bucket: input.bucket, key: item.key },
        options
      );
      result.completed.push(item);
    } catch (reason) {
      error = reason instanceof Error ? reason.message : String(reason);
      result.failed.push({ ...item, error });
    }
    onProgress?.({
      completed: result.completed.length + result.failed.length,
      total: input.items.length,
      current: item,
      ...(error ? { error } : {}),
    });
  }
  return result;
}

export async function loadSynonBiomedUseIntent(
  options: SynonBiomedSettingsOptions = {}
): Promise<SynonBiomedUseIntent> {
  const payload = asRecord(await requestJson('/api/preferences/use-intent', options));
  return {
    intent: payload?.intent === 'noncommercial' ? 'noncommercial' : 'commercial',
    declared: payload?.declared === true,
  };
}

export async function setSynonBiomedUseIntent(
  intent: SynonBiomedUseIntent['intent'],
  options: SynonBiomedSettingsOptions = {}
): Promise<SynonBiomedUseIntent> {
  const payload = asRecord(
    await requestJson('/api/preferences/use-intent', options, { method: 'PUT', body: JSON.stringify({ intent }) })
  );
  return { intent: payload?.intent === 'noncommercial' ? 'noncommercial' : 'commercial', declared: true };
}

export async function loadSynonBiomedContactEmail(
  options: SynonBiomedSettingsOptions = {}
): Promise<SynonBiomedContactEmail> {
  const payload = asRecord(await requestJson('/api/contact-email', options));
  return {
    decision:
      payload?.decision === 'allowed' || payload?.decision === 'declined' || payload?.decision === 'revoked'
        ? payload.decision
        : null,
    email: nullableString(payload?.email),
    noticeText: stringValue(payload?.notice_text),
    noticeVersion: stringValue(payload?.notice_version),
    noticeStale: payload?.notice_stale === true,
  };
}

export async function setSynonBiomedContactEmail(
  email: string,
  noticeVersion: string,
  options: SynonBiomedSettingsOptions = {}
): Promise<void> {
  await requestJson('/api/contact-email', options, {
    method: 'PUT',
    body: JSON.stringify({ email, notice_version: noticeVersion }),
  });
}

export async function removeSynonBiomedContactEmail(options: SynonBiomedSettingsOptions = {}): Promise<void> {
  await requestJson('/api/contact-email', options, { method: 'DELETE' });
}

async function requestJson(
  path: string,
  options: SynonBiomedSettingsOptions,
  init: RequestInit = {}
): Promise<unknown> {
  const response = await (options.fetchImpl ?? fetch)(`${(options.baseUrl ?? '').replace(/\/+$/, '')}${path}`, {
    ...init,
    headers: {
      Accept: 'application/json',
      ...(init.body ? { 'Content-Type': 'application/json' } : {}),
      ...init.headers,
    },
  });
  if (!response.ok) {
    const payload = asRecord(await response.json().catch((): null => null));
    throw new Error(
      stringValue(payload?.detail ?? payload?.error ?? payload?.message) || `Request failed: ${response.status}`
    );
  }
  if (response.status === 204) return null;
  const text = await response.text();
  return text ? JSON.parse(text) : null;
}

function toAllowlistGroup(value: unknown): SynonBiomedAllowlistGroup | null {
  const item = asRecord(value);
  const id = stringValue(item?.id);
  if (!id) return null;
  return {
    id,
    label: stringValue(item?.label),
    description: stringValue(item?.description),
    locked: item?.locked === true,
    domains: stringArray(item?.domains),
  };
}

function toHostGrant(value: unknown): SynonBiomedHostGrant | null {
  const item = asRecord(value);
  const hostPath = stringValue(item?.hostPath);
  if (!hostPath) return null;
  const mode = stringValue(item?.mode);
  return {
    id: stringValue(item?.id) || hostPath,
    hostPath,
    mountName: stringValue(item?.mountName),
    guestPath: stringValue(item?.guestPath),
    mode,
  };
}

function requireHostGrant(value: unknown): SynonBiomedHostGrant {
  const grant = toHostGrant(value);
  if (!grant) throw new Error('Synon Biomed returned an invalid host grant response.');
  return grant;
}

function toSecret(value: unknown): SynonBiomedSecret | null {
  const item = asRecord(value);
  const id = stringValue(item?.id);
  if (!id) return null;
  return {
    id,
    provider: stringValue(item?.provider),
    name: stringValue(item?.name),
    description: stringValue(item?.description),
    credentialType: stringValue(item?.credential_type ?? item?.credentialType),
    buckets: stringArray(item?.buckets),
    region: stringValue(item?.region),
    maskedPreview: stringValue(item?.masked_preview),
    maskedFields: stringArray(item?.masked_fields),
    valueConfigured: item?.valueConfigured === true,
    credentialsConfigured: item?.credentialsConfigured === true,
    credentialFields: stringArray(item?.credentialFields),
    createdAt: stringValue(item?.created_at ?? item?.createdAt),
    updatedAt: stringValue(item?.updated_at ?? item?.updatedAt),
  };
}

function toDataDirectoryMove(value: unknown): SynonBiomedDataDirectoryMove | null {
  const item = asRecord(value);
  const id = stringValue(item?.id);
  if (!id) return null;
  return {
    id,
    source: stringValue(item?.source),
    target: stringValue(item?.target),
    migrate: item?.migrate === true,
    copied: item?.copied === true,
    stagedAt: stringValue(item?.stagedAt),
    completedAt: nullableString(item?.completedAt),
  };
}

function toStorageRules(value: unknown): SynonBiomedStorageRules {
  const payload = asRecord(value);
  const rules = storageRuleValues(payload?.rules);
  return {
    root: stringValue(payload?.root),
    rules,
    paths: storageRuleValues(payload?.paths),
    systemPaths: {
      taskRuns: stringValue(asRecord(payload?.systemPaths)?.taskRuns),
      workspace: stringValue(asRecord(payload?.systemPaths)?.workspace),
    },
  };
}

function storageRuleValues(value: unknown): SynonBiomedStorageRuleValues {
  const item = asRecord(value);
  return {
    taskArtifacts: stringValue(item?.taskArtifacts),
    logs: stringValue(item?.logs),
    toolResults: stringValue(item?.toolResults),
    temp: stringValue(item?.temp),
  };
}

function toCloudCredential(value: unknown): SynonBiomedCloudCredential | null {
  const item = asRecord(value);
  const id = stringValue(item?.id);
  if (!id) return null;
  return {
    id,
    provider: stringValue(item?.provider),
    name: stringValue(item?.name),
    credentialType: stringValue(item?.credential_type),
    connected: item?.is_connected === true,
    defaultBucket: nullableString(item?.default_bucket),
    region: stringValue(item?.region),
  };
}

function toCloudObject(value: unknown): SynonBiomedCloudObject | null {
  const item = asRecord(value);
  const key = stringValue(item?.key);
  if (!key) return null;
  return { key, size: numberValue(item?.size), lastModified: stringValue(item?.last_modified ?? item?.lastModified) };
}

function asRecord(value: unknown): RecordValue | null {
  return value && typeof value === 'object' && !Array.isArray(value) ? (value as RecordValue) : null;
}
function arrayValue(value: unknown): unknown[] {
  return Array.isArray(value) ? value : [];
}
function stringArray(value: unknown): string[] {
  return arrayValue(value).filter((item): item is string => typeof item === 'string');
}
function stringValue(value: unknown): string {
  return typeof value === 'string' ? value : '';
}
function nullableString(value: unknown): string | null {
  const result = stringValue(value);
  return result || null;
}
function numberValue(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0;
}
function isPresent<T>(value: T | null): value is T {
  return value !== null;
}

export type SynonBiomedArtifactVersion = {
  versionId: string;
  versionNumber: number;
  artifactId: string;
  frameId: string | null;
  agentName: string | null;
  language: string | null;
  contentType: string | null;
  sizeBytes: number;
  createdAt: string | null;
  filePath: string | null;
  parentVersionId: string | null;
};

export type SynonBiomedArtifactLineage = {
  artifactId: string;
  versionId: string | null;
  versionNumber: number;
  filename: string | null;
  code: string | null;
  codeDescription: string | null;
  messages: unknown;
  environmentSnapshot: unknown;
  language: string | null;
  interactions: unknown;
  hasCellSources: boolean;
  hasMessages: boolean;
  hasEnvironment: boolean;
  pending: boolean;
  dependencyMappings: unknown;
};

export type SynonBiomedArtifactMutationResult = {
  artifactId: string | null;
  versionId: string | null;
  filename: string | null;
  folderId: string | null;
  raw: Record<string, unknown>;
};

export type SynonBiomedArtifactCopyInput = {
  artifactId: string;
  newFilename?: string;
  targetFolderId?: string | null;
};

export type SynonBiomedArtifactMoveInput = {
  artifactId: string;
  folderId: string | null;
  sortOrder?: number;
};

export type SynonBiomedArtifactPriority = 'user_starred' | 'user_hidden' | 'user_no_priority';

export type SynonBiomedArtifactPriorityInput = {
  artifactId: string;
  priority: SynonBiomedArtifactPriority;
};

export type SynonBiomedArtifactRenameInput = {
  artifactId: string;
  filename: string;
};

export type SynonBiomedTextArtifactVersionInput = {
  artifactId: string;
  content: string;
  contentType: string;
  parentVersionId?: string | null;
};

type RecordValue = Record<string, unknown>;

export async function loadSynonBiomedArtifactVersions(artifactId: string): Promise<SynonBiomedArtifactVersion[]> {
  const payload = await requestJson<unknown>(`/api/artifacts/${encodeURIComponent(artifactId)}/versions`);
  if (!Array.isArray(payload)) {
    throw new Error('Synon Biomed artifact versions response must be an array');
  }
  return payload.map(toArtifactVersion).filter((version): version is SynonBiomedArtifactVersion => version !== null);
}

export async function loadSynonBiomedArtifactLineage(
  artifactId: string,
  options: { slim?: boolean } = {}
): Promise<SynonBiomedArtifactLineage> {
  const query = options.slim ? '?slim=1' : '';
  const lineage = toArtifactLineage(
    await requestJson<unknown>(`/api/artifacts/${encodeURIComponent(artifactId)}/lineage${query}`)
  );
  if (!lineage) {
    throw new Error(`Synon Biomed artifact lineage response is invalid: ${artifactId}`);
  }
  return lineage;
}

export async function copySynonBiomedArtifact(
  input: SynonBiomedArtifactCopyInput
): Promise<SynonBiomedArtifactMutationResult> {
  return requestArtifactMutation(`/api/artifacts/${encodeURIComponent(input.artifactId)}/copy`, 'POST', {
    ...(input.newFilename ? { new_filename: input.newFilename } : {}),
    ...(input.targetFolderId !== undefined ? { target_folder_id: input.targetFolderId } : {}),
  });
}

export async function moveSynonBiomedArtifact(
  input: SynonBiomedArtifactMoveInput
): Promise<SynonBiomedArtifactMutationResult> {
  return requestArtifactMutation(`/api/artifacts/${encodeURIComponent(input.artifactId)}/folder`, 'PATCH', {
    folder_id: input.folderId,
    ...(input.sortOrder !== undefined ? { sort_order: input.sortOrder } : {}),
  });
}

export async function setSynonBiomedArtifactPriority(
  input: SynonBiomedArtifactPriorityInput
): Promise<SynonBiomedArtifactMutationResult> {
  return requestArtifactMutation(`/api/artifacts/${encodeURIComponent(input.artifactId)}/priority`, 'PATCH', {
    priority: input.priority,
  });
}

export async function renameSynonBiomedArtifact(
  input: SynonBiomedArtifactRenameInput
): Promise<SynonBiomedArtifactMutationResult> {
  return requestArtifactMutation(`/api/artifacts/${encodeURIComponent(input.artifactId)}/rename`, 'PATCH', {
    filename: input.filename,
  });
}

export async function deleteSynonBiomedArtifact(artifactId: string): Promise<SynonBiomedArtifactMutationResult> {
  return requestArtifactMutation(`/api/artifacts/${encodeURIComponent(artifactId)}`, 'DELETE');
}

export async function createSynonBiomedTextArtifactVersion(
  input: SynonBiomedTextArtifactVersionInput
): Promise<SynonBiomedArtifactMutationResult> {
  return requestArtifactMutation(`/api/artifacts/${encodeURIComponent(input.artifactId)}/versions`, 'POST', {
    content: input.content,
    content_type: input.contentType,
    ...(input.parentVersionId !== undefined ? { parent_version_id: input.parentVersionId } : {}),
  });
}

export function getSynonBiomedArtifactVersionContentUrl(versionId: string): string {
  return `/api/artifacts/versions/${encodeURIComponent(versionId)}`;
}

export async function loadSynonBiomedArtifactVersionText(versionId: string): Promise<string> {
  const response = await fetch(getSynonBiomedArtifactVersionContentUrl(versionId), {
    headers: { accept: 'text/plain, text/markdown, application/json, text/*;q=0.9, */*;q=0.1' },
  });
  if (!response.ok) {
    throw new Error(`Synon Biomed artifact version request failed: ${response.status}`);
  }
  return response.text();
}

async function requestArtifactMutation(
  path: string,
  method: 'POST' | 'PATCH' | 'DELETE',
  body?: Record<string, unknown>
): Promise<SynonBiomedArtifactMutationResult> {
  const payload = await requestJson<unknown>(path, {
    method,
    headers: {
      accept: 'application/json',
      ...(body ? { 'content-type': 'application/json' } : {}),
    },
    ...(body ? { body: JSON.stringify(body) } : {}),
  });
  const record = asRecord(payload) ?? {};
  return {
    artifactId:
      nullableString(record.id) ??
      nullableString(record.artifact_id) ??
      nullableString(record.new_artifact_id) ??
      nullableString(asRecord(record.artifact)?.id),
    versionId: nullableString(record.version_id),
    filename: nullableString(record.filename),
    folderId: nullableString(record.folder_id),
    raw: record,
  };
}

async function requestJson<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, init);
  if (!response.ok) {
    const detail = await response.text().catch(() => '');
    throw new Error(`Synon Biomed artifact request failed: ${response.status}${detail ? ` ${detail}` : ''}`);
  }
  return response.json() as Promise<T>;
}

function toArtifactVersion(value: unknown): SynonBiomedArtifactVersion | null {
  const record = asRecord(value);
  const versionId = stringValue(record?.version_id);
  const artifactId = stringValue(record?.artifact_id);
  if (!versionId || !artifactId) return null;
  return {
    versionId,
    versionNumber: numberValue(record?.version_number),
    artifactId,
    frameId: nullableString(record?.frame_id),
    agentName: nullableString(record?.agent_name),
    language: nullableString(record?.language),
    contentType: nullableString(record?.content_type),
    sizeBytes: numberValue(record?.size_bytes),
    createdAt: nullableString(record?.created_at),
    filePath: nullableString(record?.file_path),
    parentVersionId: nullableString(record?.parent_version_id),
  };
}

function toArtifactLineage(value: unknown): SynonBiomedArtifactLineage | null {
  const record = asRecord(value);
  const artifactId = stringValue(record?.artifact_id);
  if (!artifactId) return null;
  return {
    artifactId,
    versionId: nullableString(record?.version_id),
    versionNumber: numberValue(record?.version_number),
    filename: nullableString(record?.filename),
    code: nullableString(record?.code),
    codeDescription: nullableString(record?.code_description),
    messages: record?.messages ?? null,
    environmentSnapshot: record?.environment_snapshot ?? null,
    language: nullableString(record?.language),
    interactions: record?.interactions ?? null,
    hasCellSources: record?.has_cell_sources === true,
    hasMessages: record?.has_messages === true,
    hasEnvironment: record?.has_environment === true,
    pending: record?.pending === true,
    dependencyMappings: record?.dependency_mappings ?? null,
  };
}

function asRecord(value: unknown): RecordValue | null {
  return value && typeof value === 'object' && !Array.isArray(value) ? (value as RecordValue) : null;
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value : '';
}

function nullableString(value: unknown): string | null {
  return typeof value === 'string' ? value : null;
}

function numberValue(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0;
}

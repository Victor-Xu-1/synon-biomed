export type SynonBiomedArtifactMutationInput = {
  conversationId: string;
  artifactId: string;
};

export type SynonBiomedArtifactRenameInput = SynonBiomedArtifactMutationInput & {
  filename: string;
};

export type SynonBiomedArtifactRecord = {
  artifact_id?: string;
  id?: string;
  filename?: string;
  version_id?: string;
  [key: string]: unknown;
};

export type SynonBiomedUploadOptions = {
  chunkSize?: number;
  signal?: AbortSignal;
};

const DEFAULT_UPLOAD_CHUNK_SIZE = 8 * 1024 * 1024;

const workspaceArtifactUrl = ({ conversationId, artifactId }: SynonBiomedArtifactMutationInput): string =>
  `/api/conversations/${encodeURIComponent(conversationId)}/workspace/artifacts/${encodeURIComponent(artifactId)}`;

async function readJsonResponse<T>(response: Response): Promise<T> {
  if (!response.ok) {
    const detail = await response.text().catch(() => '');
    throw new Error(`Synon Biomed workspace request failed: ${response.status}${detail ? ` ${detail}` : ''}`);
  }
  return response.json() as Promise<T>;
}

export async function renameSynonBiomedArtifact(
  input: SynonBiomedArtifactRenameInput
): Promise<SynonBiomedArtifactRecord> {
  const response = await fetch(workspaceArtifactUrl(input), {
    method: 'PATCH',
    headers: { accept: 'application/json', 'content-type': 'application/json' },
    body: JSON.stringify({ filename: input.filename }),
  });
  return readJsonResponse<SynonBiomedArtifactRecord>(response);
}

export async function deleteSynonBiomedArtifact(
  input: SynonBiomedArtifactMutationInput
): Promise<Record<string, unknown>> {
  const response = await fetch(workspaceArtifactUrl(input), {
    method: 'DELETE',
    headers: { accept: 'application/json' },
  });
  return readJsonResponse<Record<string, unknown>>(response);
}

export async function uploadSynonBiomedArtifact(
  file: File,
  conversationId: string,
  onProgress?: (percent: number) => void,
  options: SynonBiomedUploadOptions = {}
): Promise<SynonBiomedArtifactRecord> {
  const requestedChunkSize = options.chunkSize ?? DEFAULT_UPLOAD_CHUNK_SIZE;
  const uploadsUrl = `/api/conversations/${encodeURIComponent(conversationId)}/workspace/uploads`;
  const initResponse = await fetch(uploadsUrl, {
    method: 'POST',
    headers: { accept: 'application/json', 'content-type': 'application/json' },
    body: JSON.stringify({
      filename: file.name,
      total_size: file.size,
      content_type: file.type || 'application/octet-stream',
      chunk_size: requestedChunkSize,
    }),
    signal: options.signal,
  });
  const init = await readJsonResponse<{ upload_id?: unknown; chunk_size?: unknown }>(initResponse);
  const uploadId = typeof init.upload_id === 'string' ? init.upload_id : '';
  const backendChunkSize = typeof init.chunk_size === 'number' ? init.chunk_size : requestedChunkSize;
  if (!uploadId || !Number.isSafeInteger(backendChunkSize) || backendChunkSize <= 0) {
    throw new Error('Synon Biomed upload initialization returned an invalid contract');
  }

  const uploadChunk = async (offset: number, chunkIndex: number): Promise<void> => {
    if (offset >= file.size) return;
    const chunk = file.slice(offset, Math.min(file.size, offset + backendChunkSize));
    const chunkResponse = await fetch(`${uploadsUrl}/${encodeURIComponent(uploadId)}/chunks/${chunkIndex}`, {
      method: 'POST',
      headers: { accept: 'application/json', 'content-type': 'application/octet-stream' },
      body: chunk,
      signal: options.signal,
    });
    await readJsonResponse<Record<string, unknown>>(chunkResponse);
    onProgress?.(Math.round((Math.min(file.size, offset + backendChunkSize) / Math.max(1, file.size)) * 100));
    await uploadChunk(offset + backendChunkSize, chunkIndex + 1);
  };

  if (file.size === 0) onProgress?.(100);
  await uploadChunk(0, 0);

  const finalizeResponse = await fetch(`${uploadsUrl}/${encodeURIComponent(uploadId)}/finalize`, {
    method: 'POST',
    headers: { accept: 'application/json' },
    signal: options.signal,
  });
  return readJsonResponse<SynonBiomedArtifactRecord>(finalizeResponse);
}

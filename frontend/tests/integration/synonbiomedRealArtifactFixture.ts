import { createRealConversationFixture } from './synonbiomedRealConversationFixture';
import { createSynonBiomedTestFetch } from './synonbiomedTestAuth';

type UploadedArtifact = {
  id?: string;
  artifact_id?: string;
};

export type RealArtifactFixture = {
  conversationId: string;
  artifactId: string;
  dispose: () => Promise<void>;
};

export async function createRealArtifactFixture(input: {
  gatewayBaseUrl: string;
  filename: string;
  contentType: string;
  content: Uint8Array | string;
  conversationId?: string;
  projectId?: string;
}): Promise<RealArtifactFixture> {
  const fetchImpl = await createSynonBiomedTestFetch(input.gatewayBaseUrl);
  const bytes = typeof input.content === 'string' ? Buffer.from(input.content, 'utf8') : Buffer.from(input.content);
  const ownedConversation = input.conversationId
    ? null
    : await createRealConversationFixture({
        gatewayBaseUrl: input.gatewayBaseUrl,
        projectId: input.projectId,
      });
  const conversationId = input.conversationId ?? ownedConversation!.conversationId;
  const workspaceBase = `/api/conversations/${encodeURIComponent(conversationId)}/workspace`;
  let artifactId: string | undefined;
  try {
    const uploadResponse = await fetchImpl(`${input.gatewayBaseUrl}${workspaceBase}/uploads`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({
        filename: input.filename,
        total_size: bytes.byteLength,
        content_type: input.contentType,
        chunk_size: 1024 * 1024,
      }),
    });
    assertResponse(uploadResponse, 'create artifact upload');
    const upload = (await uploadResponse.json()) as { upload_id: string };

    const chunkResponse = await fetchImpl(
      `${input.gatewayBaseUrl}${workspaceBase}/uploads/${encodeURIComponent(upload.upload_id)}/chunks/0`,
      {
        method: 'POST',
        headers: { 'content-type': 'application/octet-stream' },
        body: bytes,
      }
    );
    assertResponse(chunkResponse, 'upload artifact content');

    const finalizeResponse = await fetchImpl(
      `${input.gatewayBaseUrl}${workspaceBase}/uploads/${encodeURIComponent(upload.upload_id)}/finalize`,
      { method: 'POST' }
    );
    assertResponse(finalizeResponse, 'finalize artifact upload');
    const artifact = (await finalizeResponse.json()) as UploadedArtifact;
    artifactId = artifact.id ?? artifact.artifact_id;
    if (!artifactId) throw new Error('Synon Biomed did not return an artifact ID');

    return {
      conversationId,
      artifactId,
      dispose: async () => {
        let cleanupError: Error | null = null;
        try {
          const response = await fetchImpl(`${input.gatewayBaseUrl}/api/artifacts/${encodeURIComponent(artifactId!)}`, {
            method: 'DELETE',
          });
          if (!response.ok && response.status !== 404) {
            cleanupError = new Error(`delete artifact fixture failed: ${response.status} ${await response.text()}`);
          }
        } finally {
          await ownedConversation?.dispose().catch((error: unknown) => {
            cleanupError ??= error instanceof Error ? error : new Error(String(error));
          });
        }
        if (cleanupError) throw cleanupError;
      },
    };
  } catch (error) {
    if (artifactId) {
      await fetchImpl(`${input.gatewayBaseUrl}/api/artifacts/${encodeURIComponent(artifactId)}`, {
        method: 'DELETE',
      }).catch(() => undefined);
    }
    await ownedConversation?.dispose().catch(() => undefined);
    throw error;
  }
}

function assertResponse(response: Response, operation: string): void {
  if (!response.ok) throw new Error(`${operation} failed: ${response.status} ${response.statusText}`);
}

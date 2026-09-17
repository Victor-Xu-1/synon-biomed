import { createHash, randomBytes } from 'node:crypto';
import { afterEach, describe, expect, it } from 'vitest';
import { createRealConversationFixture, type RealConversationFixture } from './synonbiomedRealConversationFixture';
import { createSynonBiomedTestFetch } from './synonbiomedTestAuth';

const webBaseUrl = process.env.SYNON_BIOMED_GATEWAY_URL ?? 'http://127.0.0.1:8766';
const authenticatedFetch = createSynonBiomedTestFetch(webBaseUrl);
const chunkSize = 1024 * 1024;

type Conversation = {
  id: string;
  extra: { project_id: string };
};

type Artifact = {
  artifact_id?: string;
  id?: string;
  filename?: string;
};

type WorkspaceEntry = {
  name: string;
  relative_path: string;
  artifact_id?: string;
  can_rename?: boolean;
  can_delete?: boolean;
};

const cleanupArtifacts: Array<{ conversationId: string; artifactId: string }> = [];
let conversationFixture: RealConversationFixture | null = null;

async function requestJson<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await (await authenticatedFetch)(`${webBaseUrl}${path}`, init);
  if (!response.ok) {
    throw new Error(`${init?.method ?? 'GET'} ${path} failed: ${response.status} ${await response.text()}`);
  }
  return response.json() as Promise<T>;
}

afterEach(async () => {
  await Promise.all(
    cleanupArtifacts.splice(0).map(async ({ conversationId, artifactId }) => {
      const response = await (
        await authenticatedFetch
      )(
        `${webBaseUrl}/api/conversations/${encodeURIComponent(conversationId)}/workspace/artifacts/${encodeURIComponent(artifactId)}`,
        { method: 'DELETE' }
      );
      expect(response.ok, await response.text()).toBe(true);
    })
  );
  await conversationFixture?.dispose();
  conversationFixture = null;
});

describe('Synon Biomed real workspace lifecycle', () => {
  it('uploads, groups, renames, downloads, and deletes a project artifact through SynonAI', async () => {
    conversationFixture = await createRealConversationFixture({ gatewayBaseUrl: webBaseUrl });
    const conversation: Conversation = {
      id: conversationFixture.conversationId,
      extra: { project_id: conversationFixture.projectId },
    };

    const suffix = `${Date.now()}-${randomBytes(4).toString('hex')}`;
    const filename = `workspace-real-${suffix}.bin`;
    const renamedFilename = `workspace-real-${suffix}-renamed.bin`;
    const payload = randomBytes(chunkSize * 2 + 37);
    const workspaceBase = `/api/conversations/${encodeURIComponent(conversation.id)}/workspace`;

    const upload = await requestJson<{ upload_id: string; chunk_size: number }>(`${workspaceBase}/uploads`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({
        filename,
        total_size: payload.byteLength,
        content_type: 'application/octet-stream',
        chunk_size: chunkSize,
      }),
    });
    expect(upload.chunk_size).toBe(chunkSize);

    const uploadChunk = async (offset: number, chunkIndex: number): Promise<number> => {
      if (offset >= payload.byteLength) return chunkIndex;
      const response = await (
        await authenticatedFetch
      )(`${webBaseUrl}${workspaceBase}/uploads/${encodeURIComponent(upload.upload_id)}/chunks/${chunkIndex}`, {
        method: 'POST',
        headers: { 'content-type': 'application/octet-stream' },
        body: payload.subarray(offset, Math.min(payload.byteLength, offset + upload.chunk_size)),
      });
      if (!response.ok) {
        throw new Error(`chunk ${chunkIndex} upload failed: ${response.status} ${await response.text()}`);
      }
      return uploadChunk(offset + upload.chunk_size, chunkIndex + 1);
    };
    const chunkIndex = await uploadChunk(0, 0);
    expect(chunkIndex).toBe(3);

    const finalized = await requestJson<Artifact>(
      `${workspaceBase}/uploads/${encodeURIComponent(upload.upload_id)}/finalize`,
      { method: 'POST' }
    );
    const artifactId = finalized.artifact_id ?? finalized.id;
    expect(artifactId).toBeTruthy();
    cleanupArtifacts.push({ conversationId: conversation.id, artifactId: artifactId! });

    const root = await requestJson<WorkspaceEntry[]>(workspaceBase);
    expect(root).toContainEqual(expect.objectContaining({ relative_path: 'project-files' }));

    const projectFiles = await requestJson<WorkspaceEntry[]>(`${workspaceBase}?path=project-files`);
    expect(projectFiles).toContainEqual(
      expect.objectContaining({
        artifact_id: artifactId,
        name: filename,
        can_rename: true,
        can_delete: true,
      })
    );

    await requestJson<Artifact>(`${workspaceBase}/artifacts/${encodeURIComponent(artifactId!)}`, {
      method: 'PATCH',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ filename: renamedFilename }),
    });
    const renamedProjectFiles = await requestJson<WorkspaceEntry[]>(`${workspaceBase}?path=project-files`);
    expect(renamedProjectFiles).toContainEqual(
      expect.objectContaining({ artifact_id: artifactId, name: renamedFilename })
    );

    const contentResponse = await (
      await authenticatedFetch
    )(`${webBaseUrl}/api/artifacts/${encodeURIComponent(artifactId!)}`);
    if (!contentResponse.ok) {
      throw new Error(`artifact download failed: ${contentResponse.status} ${await contentResponse.text()}`);
    }
    const downloaded = Buffer.from(await contentResponse.arrayBuffer());
    expect(createHash('sha256').update(downloaded).digest('hex')).toBe(
      createHash('sha256').update(payload).digest('hex')
    );

    const cleanup = cleanupArtifacts.pop()!;
    await requestJson<Record<string, unknown>>(`${workspaceBase}/artifacts/${encodeURIComponent(cleanup.artifactId)}`, {
      method: 'DELETE',
    });
    const afterDelete = await requestJson<WorkspaceEntry[]>(`${workspaceBase}?path=project-files`);
    expect(afterDelete.some((entry) => entry.artifact_id === artifactId)).toBe(false);
  }, 60_000);
});

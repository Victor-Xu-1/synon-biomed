import { randomBytes } from 'node:crypto';
import { afterEach, describe, expect, it } from 'vitest';
import { createRealConversationFixture, type RealConversationFixture } from './synonbiomedRealConversationFixture';
import { createSynonBiomedTestFetch } from './synonbiomedTestAuth';
import { createControlledLlmFixture, type ControlledLlmFixture } from './synonbiomedControlledLlmFixture';

const webBaseUrl = process.env.SYNON_BIOMED_GATEWAY_URL ?? 'http://127.0.0.1:8766';
const authenticatedFetch = createSynonBiomedTestFetch(webBaseUrl);

type Conversation = {
  id: string;
  extra: { project_id: string };
};

type ArtifactRecord = {
  id?: string;
  artifact_id?: string;
  version_id?: string;
  version_number?: number;
  filename?: string;
  folder_id?: string | null;
  copied_artifact_id?: string;
  new_artifact_id?: string;
  artifact?: { id?: string; artifact_id?: string };
};

type FolderRecord = {
  id: string;
  is_user_uploads_folder?: boolean;
};

type AppliedEditRecord = {
  version_id: string;
  version_number: number;
  parent_version_id: string;
  carried_annotations: Array<{ id: string; selection_text?: string }>;
};

const cleanupArtifactIds: string[] = [];
let llmFixture: ControlledLlmFixture | null = null;
let conversationFixture: RealConversationFixture | null = null;

async function request(path: string, init?: RequestInit): Promise<Response> {
  const response = await (await authenticatedFetch)(`${webBaseUrl}${path}`, init);
  if (!response.ok) {
    throw new Error(`${init?.method ?? 'GET'} ${path} failed: ${response.status} ${await response.text()}`);
  }
  return response;
}

async function requestJson<T>(path: string, init?: RequestInit): Promise<T> {
  return (await request(path, init)).json() as Promise<T>;
}

afterEach(async () => {
  await llmFixture?.dispose();
  llmFixture = null;
  await Promise.all(
    cleanupArtifactIds
      .splice(0)
      .map((artifactId) =>
        request(`/api/artifacts/${encodeURIComponent(artifactId)}`, { method: 'DELETE' }).catch(() => undefined)
      )
  );
  await conversationFixture?.dispose();
  conversationFixture = null;
});

describe('Synon Biomed real artifact lifecycle', () => {
  it('creates versions, reads lineage, copies and moves a temporary artifact through SynonAI', async () => {
    conversationFixture = await createRealConversationFixture({ gatewayBaseUrl: webBaseUrl });
    const conversation: Conversation = {
      id: conversationFixture.conversationId,
      extra: { project_id: conversationFixture.projectId },
    };

    const suffix = `${Date.now().toString(36)}-${randomBytes(3).toString('hex')}`;
    const filename = `artifact-lifecycle-${suffix}.md`;
    const copyFilename = `artifact-lifecycle-${suffix}-copy.md`;
    const initialContent = `# Artifact lifecycle ${suffix}\n`;
    const updatedContent = `${initialContent}\nSecond version.\n`;
    const editedContent = `${initialContent}\nSecond immutable version.\n`;
    const workspaceBase = `/api/conversations/${encodeURIComponent(conversation.id)}/workspace`;
    const bytes = Buffer.from(initialContent, 'utf8');

    const upload = await requestJson<{ upload_id: string; chunk_size: number }>(`${workspaceBase}/uploads`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({
        filename,
        total_size: bytes.byteLength,
        content_type: 'text/markdown',
        chunk_size: 1024 * 1024,
      }),
    });
    await request(`${workspaceBase}/uploads/${encodeURIComponent(upload.upload_id)}/chunks/0`, {
      method: 'POST',
      headers: { 'content-type': 'application/octet-stream' },
      body: bytes,
    });
    const created = await requestJson<ArtifactRecord>(
      `${workspaceBase}/uploads/${encodeURIComponent(upload.upload_id)}/finalize`,
      { method: 'POST' }
    );
    const artifactId = created.id ?? created.artifact_id;
    expect(artifactId).toBeTruthy();
    cleanupArtifactIds.push(artifactId!);

    const metadata = await requestJson<ArtifactRecord>(`/api/artifacts/${encodeURIComponent(artifactId!)}/metadata`);
    expect(metadata).toMatchObject({ filename, version_number: 1 });

    const createdVersion = await requestJson<ArtifactRecord>(
      `/api/artifacts/${encodeURIComponent(artifactId!)}/versions`,
      {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({
          content: updatedContent,
          content_type: 'text/markdown',
          parent_version_id: metadata.version_id,
        }),
      }
    );
    expect(createdVersion.version_id).toBeTruthy();

    const versions = await requestJson<ArtifactRecord[]>(`/api/artifacts/${encodeURIComponent(artifactId!)}/versions`);
    expect(versions.map((version) => version.version_number)).toEqual(expect.arrayContaining([1, 2]));
    const latestVersion = versions.find((version) => version.version_number === 2);
    expect(latestVersion?.version_id).toBeTruthy();
    expect(
      await (await request(`/api/artifacts/versions/${encodeURIComponent(latestVersion!.version_id!)}`)).text()
    ).toBe(updatedContent);

    const annotation = await requestJson<{ id: string }>(
      `/api/artifacts/${encodeURIComponent(artifactId!)}/versions/${encodeURIComponent(latestVersion!.version_id!)}/annotations`,
      {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({
          type: 'text_selection',
          text: 'Make this statement precise and concise.',
          selection_text: 'Second version.',
        }),
      }
    );
    expect(annotation.id).toBeTruthy();

    llmFixture = await createControlledLlmFixture(webBaseUrl);
    const suggestion = await requestJson<{ suggestion: string }>(
      `/api/artifacts/${encodeURIComponent(artifactId!)}/versions/${encodeURIComponent(latestVersion!.version_id!)}/suggest-edits`,
      {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({
          selected_text: 'Second version.',
          annotation_text: 'Rewrite as a concise scientific statement without adding facts.',
          mode: 'edit',
        }),
      }
    );
    expect(suggestion.suggestion.trim().length).toBeGreaterThan(0);

    const applied = await requestJson<AppliedEditRecord>(
      `/api/artifacts/${encodeURIComponent(artifactId!)}/versions/${encodeURIComponent(latestVersion!.version_id!)}/apply-edit`,
      {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({
          selected_text: 'Second version.',
          replacement_text: 'Second immutable version.',
        }),
      }
    );
    expect(applied).toMatchObject({
      version_number: 3,
      parent_version_id: latestVersion!.version_id,
    });
    expect(applied.carried_annotations).toHaveLength(1);
    expect(applied.carried_annotations[0]?.id).toBeTruthy();
    expect(await (await request(`/api/artifacts/versions/${encodeURIComponent(applied.version_id)}`)).text()).toBe(
      editedContent
    );
    const carried = await requestJson<{ annotations: Array<{ id: string }> }>(
      `/api/artifacts/${encodeURIComponent(artifactId!)}/versions/${encodeURIComponent(applied.version_id)}/annotations`
    );
    expect(carried.annotations).toHaveLength(1);

    const lineage = await requestJson<Record<string, unknown>>(
      `/api/artifacts/${encodeURIComponent(artifactId!)}/lineage?slim=1`
    );
    expect(lineage).toMatchObject({ artifact_id: artifactId, version_number: 3, pending: false });

    const copied = await requestJson<ArtifactRecord>(`/api/artifacts/${encodeURIComponent(artifactId!)}/copy`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ new_filename: copyFilename, target_folder_id: null }),
    });
    const copiedArtifactId =
      copied.id ?? copied.artifact_id ?? copied.new_artifact_id ?? copied.copied_artifact_id ?? copied.artifact?.id;
    expect(copiedArtifactId).toBeTruthy();
    cleanupArtifactIds.push(copiedArtifactId!);
    expect(await (await request(`/api/artifacts/${encodeURIComponent(copiedArtifactId!)}`)).text()).toBe(editedContent);

    const folders = await requestJson<FolderRecord[]>(
      `/api/projects/${encodeURIComponent(conversation.extra.project_id)}/folders`
    );
    const targetFolder = folders.find((folder) => folder.is_user_uploads_folder) ?? folders[0];
    expect(targetFolder?.id).toBeTruthy();
    await request(`/api/artifacts/${encodeURIComponent(artifactId!)}/folder`, {
      method: 'PATCH',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ folder_id: targetFolder.id, sort_order: 0 }),
    });
    const projectArtifacts = await requestJson<ArtifactRecord[]>(
      `/api/projects/${encodeURIComponent(conversation.extra.project_id)}/artifacts`
    );
    const moved = projectArtifacts.find((artifact) => (artifact.id ?? artifact.artifact_id) === artifactId);
    expect(moved?.folder_id).toBe(targetFolder.id);
  }, 60_000);
});

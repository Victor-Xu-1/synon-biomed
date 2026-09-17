import { describe, expect, it } from 'vitest';
import { createSynonBiomedTestFetch } from './synonbiomedTestAuth';

const webHostBaseUrl = process.env.SYNON_BIOMED_GATEWAY_URL ?? 'http://127.0.0.1:8766';
const authenticatedFetch = createSynonBiomedTestFetch(webHostBaseUrl);

type NativeConversation = {
  id: string;
  type: string;
  name: string;
  source?: string;
  extra?: {
    backend?: string;
    project_id?: string;
    root_frame_id?: string;
  };
};

type NativeMessage = {
  id: string;
  conversation_id: string;
  type: string;
  position?: string;
  content: Record<string, unknown>;
  artifact_refs?: NativeArtifactReference[];
};

type NativeArtifactReference = {
  artifact_id: string;
  version_id: string;
};

type NativeScientificFile = {
  artifact_id: string;
  filename: string;
  content_type: string | null;
  preview_kind: string;
  content_url: string;
  is_intermediate: boolean;
};

type NativeWorkspaceEntry = {
  name: string;
  type: 'directory' | 'file';
  relative_path: string;
  read_only: boolean;
  artifact_id?: string;
  content_type?: string | null;
  content_url?: string;
};
type NativeScientificFilesArtifact = {
  id: string;
  conversation_id: string;
  kind: 'scientific_files';
  status: string;
  payload: {
    project_id: string;
    root_frame_id: string;
    files: NativeScientificFile[];
  };
};
type NativeConversationCandidate = {
  conversation: NativeConversation;
  messages: NativeMessage[];
  artifactCollection: NativeScientificFilesArtifact;
};

async function getJson<T>(path: string): Promise<T> {
  const response = await (
    await authenticatedFetch
  )(`${webHostBaseUrl}${path}`, {
    headers: { accept: 'application/json' },
  });
  if (!response.ok) {
    throw new Error(`WebHost request failed: ${response.status} ${path}: ${await response.text()}`);
  }
  return (await response.json()) as T;
}

async function postJson<T>(path: string, body: unknown): Promise<T> {
  const response = await (
    await authenticatedFetch
  )(`${webHostBaseUrl}${path}`, {
    method: 'POST',
    headers: { accept: 'application/json', 'content-type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!response.ok) {
    throw new Error(`WebHost request failed: ${response.status} ${path}: ${await response.text()}`);
  }
  return (await response.json()) as T;
}

function exactArtifactReferences(messages: NativeMessage[]): NativeArtifactReference[] {
  const unique = new Map<string, NativeArtifactReference>();
  for (const message of messages) {
    for (const reference of message.artifact_refs ?? []) {
      const artifactId = reference.artifact_id.trim();
      const versionId = reference.version_id.trim();
      if (!artifactId || !versionId) continue;
      unique.set(`${artifactId}\u0000${versionId}`, { artifact_id: artifactId, version_id: versionId });
    }
  }
  return [...unique.values()];
}

async function selectNativeConversation(items: NativeConversation[]): Promise<NativeConversationCandidate> {
  const rejected: string[] = [];
  for (const conversation of items) {
    if (
      conversation.type !== 'acp' ||
      conversation.source !== 'synonbiomed' ||
      !conversation.extra?.project_id ||
      conversation.extra.root_frame_id !== conversation.id
    ) {
      continue;
    }
    try {
      const page = await getJson<{ items: NativeMessage[] }>(
        `/api/conversations/${encodeURIComponent(conversation.id)}/messages?limit=200&content_mode=full`
      );
      const references = exactArtifactReferences(page.items);
      if (references.length === 0 || references.length > 400) {
        rejected.push(`${conversation.id}: expected between 1 and 400 exact artifact references`);
        continue;
      }
      const collections = await postJson<NativeScientificFilesArtifact[]>(
        `/api/conversations/${encodeURIComponent(conversation.id)}/artifacts`,
        { references }
      );
      const collection = collections[0];
      const previews = new Set(collection?.payload.files.map((file) => file.preview_kind) ?? []);
      const messageTypes = new Set(page.items.map((message) => message.type));
      if (
        collections.length === 1 &&
        page.items.length > 10 &&
        messageTypes.has('text') &&
        messageTypes.has('thinking') &&
        messageTypes.has('tool_call') &&
        collection.payload.files.length > 10 &&
        previews.has('image') &&
        previews.has('csv') &&
        previews.has('markdown')
      ) {
        return { conversation, messages: page.items, artifactCollection: collection };
      }
      rejected.push(`${conversation.id}: missing required native message or preview coverage`);
    } catch (error) {
      rejected.push(`${conversation.id}: ${error instanceof Error ? error.message : String(error)}`);
    }
  }
  throw new Error(`no migrated native conversation satisfies the integration contract: ${rejected.join('; ')}`);
}

describe('Synon Biomed native SynonAI conversation integration', () => {
  it('loads a real root frame, persisted messages, and scientific files through WebHost', async () => {
    const conversations = await getJson<{ items: NativeConversation[]; total: number }>('/api/conversations?limit=100');
    expect(conversations.total).toBeGreaterThan(0);
    const candidate = await selectNativeConversation(conversations.items);
    const { conversation, messages, artifactCollection } = candidate;
    const conversationId = conversation.id;
    const projectId = conversation.extra!.project_id!;

    expect(conversation).toMatchObject({
      id: conversationId,
      type: 'acp',
      source: 'synonbiomed',
      extra: {
        backend: 'synonbiomed',
        project_id: projectId,
        root_frame_id: conversationId,
      },
    });
    expect(conversationId).not.toBe(projectId);

    const detail = await getJson<NativeConversation>(`/api/conversations/${encodeURIComponent(conversationId)}`);
    expect(detail).toMatchObject({
      id: conversationId,
      type: 'acp',
      extra: { backend: 'synonbiomed', project_id: projectId, root_frame_id: conversationId },
    });

    expect(messages.length).toBeGreaterThan(10);
    expect(messages.some((message) => message.type === 'text' && message.position === 'right')).toBe(true);
    expect(messages.some((message) => message.type === 'thinking' && message.position === 'left')).toBe(true);
    expect(messages.some((message) => message.type === 'tool_call' && message.position === 'left')).toBe(true);
    expect(
      messages.some((message) => message.type === 'tool_call' && typeof message.content.description === 'string')
    ).toBe(true);
    expect(messages.every((message) => message.conversation_id === conversationId)).toBe(true);
    expect(JSON.stringify(messages)).not.toContain('synonbiomed-project-summary');
    expect(JSON.stringify(messages)).not.toContain('skill_discovery signal');

    expect(artifactCollection).toMatchObject({
      id: `synonbiomed-files:${conversationId}`,
      conversation_id: conversationId,
      kind: 'scientific_files',
      status: 'active',
      payload: {
        project_id: projectId,
        root_frame_id: conversationId,
      },
    });
    const files = artifactCollection.payload.files;
    expect(files.length).toBeGreaterThan(10);
    expect(files.every((file) => file.is_intermediate === false)).toBe(true);
    expect(files.some((file) => file.preview_kind === 'image')).toBe(true);
    expect(files.some((file) => file.preview_kind === 'csv')).toBe(true);
    expect(files.some((file) => file.preview_kind === 'markdown')).toBe(true);

    const image = files.find((file) => file.preview_kind === 'image');
    expect(image).toBeDefined();
    const imageResponse = await (await authenticatedFetch)(`${webHostBaseUrl}${image!.content_url}`);
    expect(imageResponse.ok).toBe(true);
    expect(imageResponse.headers.get('content-type')).toMatch(/^image\//);
    expect((await imageResponse.arrayBuffer()).byteLength).toBeGreaterThan(100);

    const workspaceRoot = await getJson<NativeWorkspaceEntry[]>(
      `/api/conversations/${encodeURIComponent(conversationId)}/workspace?path=.`
    );
    expect(workspaceRoot.length).toBeGreaterThan(0);
    expect(
      workspaceRoot.every(
        (entry) => entry.type === 'directory' && entry.relative_path.length > 0 && typeof entry.read_only === 'boolean'
      )
    ).toBe(true);

    let workspacePath = '';
    let sessionFiles: NativeWorkspaceEntry[] = [];
    for (const directory of workspaceRoot) {
      const entries = await getJson<NativeWorkspaceEntry[]>(
        `/api/conversations/${encodeURIComponent(conversationId)}/workspace?path=${encodeURIComponent(directory.relative_path)}`
      );
      if (entries.length > 10 && entries.some((entry) => entry.type === 'file' && entry.artifact_id)) {
        workspacePath = directory.relative_path;
        sessionFiles = entries;
        break;
      }
    }

    expect(sessionFiles.length).toBeGreaterThan(10);
    expect(sessionFiles.every((entry) => entry.type === 'file' && typeof entry.read_only === 'boolean')).toBe(true);
    expect(sessionFiles.every((entry) => entry.relative_path.startsWith(`${workspacePath}/`))).toBe(true);
    expect(sessionFiles.every((entry) => entry.artifact_id && entry.content_url)).toBe(true);

    const report = sessionFiles.find((entry) => entry.content_type === 'text/markdown');
    expect(report).toBeDefined();
    expect(report).toMatchObject({ content_type: 'text/markdown' });
    const reportResponse = await (await authenticatedFetch)(`${webHostBaseUrl}${report!.content_url}`);
    expect(reportResponse.ok).toBe(true);
    expect(reportResponse.headers.get('content-type')).toContain('text/markdown');
    expect((await reportResponse.text()).trim().length).toBeGreaterThan(100);

    const searchResults = await getJson<NativeWorkspaceEntry[]>(
      `/api/conversations/${encodeURIComponent(conversationId)}/workspace?path=${encodeURIComponent(workspacePath)}&search=${encodeURIComponent(report!.name)}`
    );
    expect(
      searchResults.some((entry) => entry.name === report!.name && entry.artifact_id === report!.artifact_id)
    ).toBe(true);
  });
});

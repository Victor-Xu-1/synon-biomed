import { randomUUID } from 'node:crypto';
import { createSynonBiomedTestFetch } from './synonbiomedTestAuth';

export type RealConversationFixture = {
  conversationId: string;
  projectId: string;
  fetchImpl: typeof fetch;
  dispose: () => Promise<void>;
};

export type RealConversationSummary = {
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

export async function createRealConversationFixture(input: {
  gatewayBaseUrl: string;
  projectId?: string;
}): Promise<RealConversationFixture> {
  const fetchImpl = await createSynonBiomedTestFetch(input.gatewayBaseUrl);
  let projectId = input.projectId;
  let ownedProjectId: string | null = null;

  if (!projectId) {
    const projectResponse = await fetchImpl(`${input.gatewayBaseUrl}/api/projects`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({
        name: `Real integration ${randomUUID().slice(0, 8)}`,
        description: 'Disposable project created by the real integration fixture.',
      }),
    });
    if (projectResponse.status !== 201) {
      throw new Error(
        `create conversation fixture project failed: ${projectResponse.status} ${await projectResponse.text()}`
      );
    }
    const project = (await projectResponse.json()) as { project_id?: string };
    projectId = project.project_id;
    if (!projectId) throw new Error('create conversation fixture project did not return project_id');
    ownedProjectId = projectId;
  }

  try {
    const response = await fetchImpl(`${input.gatewayBaseUrl}/api/conversations`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({
        name: `Real integration ${randomUUID().slice(0, 8)}`,
        assistant: { id: 'synonbiomed:OPERON', locale: 'zh-CN', conversation_overrides: {} },
        extra: { project_id: projectId },
      }),
    });
    if (!response.ok) {
      throw new Error(`create conversation fixture failed: ${response.status} ${await response.text()}`);
    }
    const payload = (await response.json()) as { id?: string };
    if (!payload.id) throw new Error('create conversation fixture did not return id');
    const conversationId = payload.id;

    return {
      conversationId,
      projectId,
      fetchImpl,
      dispose: async () => {
        let cleanupError: Error | null = null;
        try {
          const deletedFrame = await fetchImpl(
            `${input.gatewayBaseUrl}/api/conversations/${encodeURIComponent(conversationId)}`,
            { method: 'DELETE' }
          );
          if (!deletedFrame.ok && deletedFrame.status !== 404) {
            cleanupError = new Error(
              `delete conversation fixture failed: ${deletedFrame.status} ${await deletedFrame.text()}`
            );
          }
        } finally {
          if (ownedProjectId) {
            const deletedProject = await fetchImpl(
              `${input.gatewayBaseUrl}/api/projects/${encodeURIComponent(ownedProjectId)}`,
              { method: 'DELETE' }
            );
            if (!deletedProject.ok && deletedProject.status !== 404 && !cleanupError) {
              cleanupError = new Error(
                `delete conversation fixture project failed: ${deletedProject.status} ${await deletedProject.text()}`
              );
            }
          }
        }
        if (cleanupError) throw cleanupError;
      },
    };
  } catch (error) {
    if (ownedProjectId) {
      await fetchImpl(`${input.gatewayBaseUrl}/api/projects/${encodeURIComponent(ownedProjectId)}`, {
        method: 'DELETE',
      }).catch(() => undefined);
    }
    throw error;
  }
}

export async function loadRealConversationSummaries(
  gatewayBaseUrl: string,
  fetchImpl: typeof fetch
): Promise<RealConversationSummary[]> {
  const response = await fetchImpl(`${gatewayBaseUrl}/api/conversations?limit=100`);
  if (!response.ok) throw new Error(`list real conversations failed: ${response.status} ${await response.text()}`);
  const payload = (await response.json()) as { items?: RealConversationSummary[] };
  return Array.isArray(payload.items) ? payload.items : [];
}

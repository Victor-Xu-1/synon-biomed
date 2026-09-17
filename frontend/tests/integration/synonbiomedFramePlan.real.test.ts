import { loadSynonBiomedFramePlanArtifact } from '@/renderer/services/synonBiomedRuntimeOperations';
import { describe, expect, it } from 'vitest';
import { createRealArtifactFixture, type RealArtifactFixture } from './synonbiomedRealArtifactFixture';
import { createRealConversationFixture } from './synonbiomedRealConversationFixture';

const gatewayBaseUrl = process.env.SYNON_BIOMED_GATEWAY_URL ?? 'http://127.0.0.1:8766';

describe('Synon Biomed frame plan gateway', () => {
  it('discovers a real plan artifact attached to an isolated completed frame', async () => {
    const conversation = await createRealConversationFixture({ gatewayBaseUrl });
    let plan: RealArtifactFixture | null = null;
    try {
      const frameId = conversation.conversationId;
      plan = await createRealArtifactFixture({
        gatewayBaseUrl,
        conversationId: frameId,
        filename: `plan_isolated-${Date.now()}.json`,
        contentType: 'application/json',
        content: JSON.stringify({ version: 3, task_summary: 'Isolated plan fixture', phases: [] }),
      });
      const response = await conversation.fetchImpl(
        `${gatewayBaseUrl}/api/frames/${encodeURIComponent(frameId)}/artifacts`
      );
      expect(response.ok).toBe(true);
      const artifacts = (await response.json()) as Array<{
        id: string;
        version_id?: string | null;
        filename: string;
        created_at?: string | null;
      }>;
      const latestPlan = artifacts
        .filter((artifact) => /^plan_.+\.json$/i.test(artifact.filename))
        .toSorted((left, right) => (right.created_at ?? '').localeCompare(left.created_at ?? ''))[0];
      expect(latestPlan).toBeDefined();

      await expect(
        loadSynonBiomedFramePlanArtifact(frameId, {
          baseUrl: gatewayBaseUrl,
          fetchImpl: conversation.fetchImpl,
        })
      ).resolves.toMatchObject({
        artifactId: latestPlan!.id,
        filename: latestPlan!.filename,
        versionId: latestPlan!.version_id ?? null,
      });
    } finally {
      try {
        await plan?.dispose();
      } finally {
        await conversation.dispose();
      }
    }
  });
});

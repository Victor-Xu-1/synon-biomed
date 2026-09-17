import { describe, expect, it } from 'vitest';
import { createSynonBiomedTestFetch } from './synonbiomedTestAuth';

const gatewayBaseUrl = process.env.SYNON_BIOMED_GATEWAY_URL ?? 'http://127.0.0.1:8766';
const fixtureName = 'SYNON_AI_EXPERT_LIFECYCLE_TEST';
const profilesPath = `${gatewayBaseUrl}/api/synonbiomed/expert-profiles`;

type ExpertProfile = {
  name: string;
  source: string;
  displayName: string;
  description: string;
  systemPrompt: string;
  enabled: boolean;
};

describe('Synon Biomed expert profile gateway', () => {
  it('creates, projects, updates, disables, and deletes a user expert through the authenticated WebHost', async () => {
    const fetchImpl = await createSynonBiomedTestFetch(gatewayBaseUrl);

    const readProfiles = async (): Promise<ExpertProfile[]> => {
      const response = await fetchImpl(profilesPath, { headers: { accept: 'application/json' } });
      expect(response.ok).toBe(true);
      return (await response.json()) as ExpertProfile[];
    };
    const deleteFixture = async () => {
      if (!(await readProfiles()).some((profile) => profile.name === fixtureName)) return;
      const response = await fetchImpl(`${profilesPath}/${fixtureName}`, { method: 'DELETE' });
      expect(response.status).toBe(204);
    };

    await deleteFixture();
    try {
      const createResponse = await fetchImpl(profilesPath, {
        method: 'POST',
        headers: { accept: 'application/json', 'content-type': 'application/json' },
        body: JSON.stringify({
          name: fixtureName,
          displayName: 'SynonAI lifecycle expert',
          description: 'Real expert profile lifecycle fixture',
          systemPrompt: 'Review biomedical evidence and cite primary sources.',
          enabled: true,
          skillNames: [],
          unrestricted: false,
        }),
      });
      expect(createResponse.status).toBe(200);
      await expect(createResponse.json()).resolves.toMatchObject({
        name: fixtureName,
        source: 'user',
        displayName: 'SynonAI lifecycle expert',
        enabled: true,
      });

      const assistantResponse = await fetchImpl(`${gatewayBaseUrl}/api/assistants`, {
        headers: { accept: 'application/json' },
      });
      expect(assistantResponse.ok).toBe(true);
      const assistantPayload = (await assistantResponse.json()) as {
        data: Array<{ agent_id: string; source: string; deletable: boolean; name: string }>;
      };
      expect(assistantPayload.data).toContainEqual(
        expect.objectContaining({
          agent_id: fixtureName,
          source: 'user',
          deletable: true,
          name: 'SynonAI lifecycle expert',
        })
      );

      const updateResponse = await fetchImpl(`${profilesPath}/${fixtureName}`, {
        method: 'PATCH',
        headers: { accept: 'application/json', 'content-type': 'application/json' },
        body: JSON.stringify({
          displayName: 'SynonAI updated expert',
          description: 'Updated through the fusion WebHost',
          systemPrompt: 'Audit the evidence before reporting a conclusion.',
        }),
      });
      expect(updateResponse.ok).toBe(true);

      const disableResponse = await fetchImpl(`${profilesPath}/${fixtureName}/enabled`, {
        method: 'POST',
        headers: { accept: 'application/json', 'content-type': 'application/json' },
        body: JSON.stringify({ enabled: false }),
      });
      expect(disableResponse.ok).toBe(true);
      await expect(disableResponse.json()).resolves.toMatchObject({
        name: fixtureName,
        displayName: 'SynonAI updated expert',
        description: 'Updated through the fusion WebHost',
        systemPrompt: 'Audit the evidence before reporting a conclusion.',
        enabled: false,
      });
    } finally {
      await deleteFixture();
    }

    expect((await readProfiles()).some((profile) => profile.name === fixtureName)).toBe(false);
  });
});

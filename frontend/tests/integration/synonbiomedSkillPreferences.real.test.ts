import { describe, expect, it } from 'vitest';
import { createSynonBiomedTestFetch } from './synonbiomedTestAuth';

const gatewayBaseUrl = process.env.SYNON_BIOMED_GATEWAY_URL ?? 'http://127.0.0.1:8766';
const fixtureSkill = 'alphafold2';

type WorkspaceSkill = { name: string; enabled?: boolean };

describe('Synon Biomed skill preferences gateway', () => {
  it('persists a real skill enabled preference and restores the original state', async () => {
    const fetchImpl = await createSynonBiomedTestFetch(gatewayBaseUrl);
    const listSkills = async (): Promise<WorkspaceSkill[]> => {
      const response = await fetchImpl(`${gatewayBaseUrl}/api/skills/catalog`, {
        headers: { accept: 'application/json' },
      });
      expect(response.ok).toBe(true);
      const payload = (await response.json()) as { skills: WorkspaceSkill[] };
      return payload.skills;
    };
    const original = (await listSkills()).find((skill) => skill.name === fixtureSkill);
    expect(original).toBeDefined();
    const originalEnabled = original!.enabled !== false;
    const candidate = !originalEnabled;

    try {
      const update = await fetchImpl(`${gatewayBaseUrl}/api/skills/catalog/${fixtureSkill}/enabled`, {
        method: 'PUT',
        headers: { accept: 'application/json', 'content-type': 'application/json' },
        body: JSON.stringify({ enabled: candidate }),
      });
      expect(update.ok).toBe(true);
      await expect(update.json()).resolves.toEqual({ name: fixtureSkill, enabled: candidate });
      expect((await listSkills()).find((skill) => skill.name === fixtureSkill)?.enabled !== false).toBe(candidate);
    } finally {
      const restore = await fetchImpl(`${gatewayBaseUrl}/api/skills/catalog/${fixtureSkill}/enabled`, {
        method: 'PUT',
        headers: { accept: 'application/json', 'content-type': 'application/json' },
        body: JSON.stringify({ enabled: originalEnabled }),
      });
      expect(restore.ok).toBe(true);
    }

    expect((await listSkills()).find((skill) => skill.name === fixtureSkill)?.enabled !== false).toBe(originalEnabled);
  });
});

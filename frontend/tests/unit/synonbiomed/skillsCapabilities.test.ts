import { describe, expect, it, vi } from 'vitest';
import { loadSynonBiomedSkills, setSynonBiomedSkillEnabled } from '@/renderer/services/synonBiomedCapabilities';

describe('Synon Biomed skill capabilities', () => {
  it('loads rich catalog metadata with the durable enabled preference', async () => {
    const fetchImpl = vi.fn<typeof fetch>(async (input) => {
      const url = String(input);
      if (url === 'http://fusion.test/api/skills/catalog') {
        return new Response(
          JSON.stringify({
            skills: [
              {
                name: 'alphafold2',
                displayName: 'AlphaFold2',
                description: 'Protein structure prediction',
                source: 'synon_llm',
                category: 'biomodels',
                license: 'Apache-2.0',
                skillId: 'bundled:alphafold2',
                attachedAgents: ['OPERON'],
                enabled: false,
              },
            ],
          })
        );
      }
      throw new Error(`unexpected URL: ${url}`);
    });

    await expect(loadSynonBiomedSkills({ baseUrl: 'http://fusion.test', fetchImpl })).resolves.toEqual([
      {
        name: 'alphafold2',
        displayName: 'AlphaFold2',
        description: 'Protein structure prediction',
        source: 'synon_llm',
        category: 'biomodels',
        license: 'Apache-2.0',
        skillId: 'bundled:alphafold2',
        updatedAt: null,
        attachedAgents: ['OPERON'],
        enabled: false,
      },
    ]);
  });

  it('writes enabled state through the restricted workspace skill contract', async () => {
    const fetchImpl = vi
      .fn<typeof fetch>()
      .mockResolvedValue(
        new Response(JSON.stringify({ ok: true, name: 'alphafold2', enabled: false }), { status: 200 })
      );

    await setSynonBiomedSkillEnabled('alphafold2', false, { baseUrl: 'http://fusion.test', fetchImpl });

    expect(fetchImpl).toHaveBeenCalledWith(
      'http://fusion.test/api/skills/catalog/alphafold2/enabled',
      expect.objectContaining({
        method: 'PUT',
        body: JSON.stringify({ enabled: false }),
      })
    );
  });
});

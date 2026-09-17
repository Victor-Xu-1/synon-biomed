import { describe, expect, it, vi } from 'vitest';
import {
  createSynonBiomedExpertProfile,
  deleteSynonBiomedExpertProfile,
  loadSynonBiomedExpertProfiles,
  loadSynonBiomedExpertProfilesWithRuntimeConnectors,
  loadSynonBiomedExpertRuntimeConnectorIds,
  normalizeSynonBiomedExpertName,
  setSynonBiomedExpertProfileEnabled,
  updateSynonBiomedExpertProfile,
} from '@/renderer/services/agents/synonBiomedExpertProfiles';

const profile = {
  id: 'profile-1',
  name: 'LITERATURE_REVIEWER',
  source: 'user',
  displayName: 'Literature Reviewer',
  description: 'Reviews primary evidence',
  systemPrompt: 'Cite primary sources.',
  iconKey: 'flask',
  colorKey: 'green',
  enabled: true,
  userHidden: false,
  unrestricted: false,
  skillNames: ['literature-review'],
  connectorIds: ['bundled:pubmed'],
};

describe('Synon Biomed expert profile service', () => {
  it('uses the restricted expert profile bridge for the complete lifecycle', async () => {
    const fetchImpl = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(new Response(JSON.stringify([profile]), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify(profile), { status: 200 }))
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ ...profile, displayName: 'Evidence Reviewer' }), { status: 200 })
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ ...profile, displayName: 'Evidence Reviewer', enabled: false }), { status: 200 })
      )
      .mockResolvedValueOnce(new Response(JSON.stringify({ ...profile, enabled: false }), { status: 200 }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    const options = { baseUrl: 'http://127.0.0.1:28081', fetchImpl };

    await expect(loadSynonBiomedExpertProfiles(options)).resolves.toEqual([profile]);
    await createSynonBiomedExpertProfile(
      {
        name: profile.name,
        displayName: profile.displayName,
        description: profile.description,
        systemPrompt: profile.systemPrompt,
        enabled: true,
      },
      options
    );
    await updateSynonBiomedExpertProfile(
      profile.name,
      {
        name: profile.name,
        displayName: 'Evidence Reviewer',
        description: profile.description,
        systemPrompt: profile.systemPrompt,
        enabled: false,
      },
      options
    );
    await setSynonBiomedExpertProfileEnabled(profile.name, false, options);
    await deleteSynonBiomedExpertProfile(profile.name, options);

    expect(fetchImpl.mock.calls.map(([url, init]) => [url, init?.method])).toEqual([
      ['http://127.0.0.1:28081/api/synonbiomed/expert-profiles', 'GET'],
      ['http://127.0.0.1:28081/api/synonbiomed/expert-profiles', 'POST'],
      ['http://127.0.0.1:28081/api/synonbiomed/expert-profiles/LITERATURE_REVIEWER', 'PATCH'],
      ['http://127.0.0.1:28081/api/synonbiomed/expert-profiles/LITERATURE_REVIEWER/enabled', 'POST'],
      ['http://127.0.0.1:28081/api/synonbiomed/expert-profiles/LITERATURE_REVIEWER/enabled', 'POST'],
      ['http://127.0.0.1:28081/api/synonbiomed/expert-profiles/LITERATURE_REVIEWER', 'DELETE'],
    ]);
  });

  it('normalizes a human-entered profile identifier to the backend contract', () => {
    expect(normalizeSynonBiomedExpertName(' literature reviewer 2026 ')).toBe('LITERATURE_REVIEWER_2026');
  });

  it('preserves bundled profile metadata for presentation-layer localization', async () => {
    const bundled = {
      ...profile,
      name: 'OPERON',
      source: 'bundled',
      displayName: '通用科研助手',
      description: '通用科研计算助手',
    };
    const fetchImpl = vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify([bundled]), { status: 200 }));

    await expect(loadSynonBiomedExpertProfiles({ fetchImpl })).resolves.toEqual([bundled]);
  });

  it('loads effective runtime connectors for user profiles without changing bundled projection data', async () => {
    const userProfile = { ...profile, name: 'MY_RESEARCH_EXPERT', connectorIds: [] };
    const runtimeConnectors = [{ id: 'bundled:pubmed' }, { id: 'bundled:chembl' }];
    const fetchImpl = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(new Response(JSON.stringify(runtimeConnectors), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify([userProfile]), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify(runtimeConnectors), { status: 200 }));

    await expect(loadSynonBiomedExpertRuntimeConnectorIds(userProfile.name, { fetchImpl })).resolves.toEqual([
      'bundled:pubmed',
      'bundled:chembl',
    ]);
    await expect(loadSynonBiomedExpertProfilesWithRuntimeConnectors({ fetchImpl })).resolves.toEqual([
      { ...userProfile, connectorIds: ['bundled:pubmed', 'bundled:chembl'] },
    ]);
    expect(fetchImpl.mock.calls.map(([url, init]) => [url, init?.method])).toEqual([
      ['/api/agents/MY_RESEARCH_EXPERT/mcp-servers?include_tools=false', 'GET'],
      ['/api/synonbiomed/expert-profiles', 'GET'],
      ['/api/agents/MY_RESEARCH_EXPERT/mcp-servers?include_tools=false', 'GET'],
    ]);
  });
});

import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  activateSynonBiomedLlmProfile,
  deleteSynonBiomedLlmProfile,
  loadSynonBiomedLlmModels,
  loadSynonBiomedLlmProviders,
  saveSynonBiomedLlmProfile,
  testSynonBiomedLlmProfile,
  type SynonBiomedLlmProfile,
} from '@/renderer/services/synonBiomedLlm';

const profile: SynonBiomedLlmProfile = {
  id: 'profile-1',
  name: 'DeepSeek',
  provider: 'deepseek',
  baseUrl: 'https://api.deepseek.com/v1',
  model: 'deepseek-chat',
  temperature: 0.2,
  maxTokens: 2048,
  createdAt: '2026-07-10T00:00:00.000Z',
  updatedAt: '2026-07-10T00:00:00.000Z',
  hasApiKey: true,
  apiKeySource: 'stored',
};

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('Synon Biomed LLM service', () => {
  it('preserves explicit null budgets on the wire instead of dropping the reset', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify({ ok: true, profile })));
    await saveSynonBiomedLlmProfile({ ...profile, maxTokens: null }, fetchMock);
    const body = JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body));
    expect(body).toHaveProperty('maxTokens', null);
    expect(body).not.toHaveProperty('apiKey');
  });
  it('loads provider profiles and native model selector entries from Synon LLM Core', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            ok: true,
            templates: [
              {
                provider: 'deepseek',
                label: 'DeepSeek',
                defaultBaseUrl: 'https://api.deepseek.com/v1',
                modelExamples: ['deepseek-chat'],
                protocol: 'openai',
              },
            ],
            activeProfileId: profile.id,
            profiles: [profile],
          })
        )
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            data: [
              {
                id: 'deepseek-chat',
                display_name: 'deepseek-chat',
                description: 'DeepSeek',
                provider: 'deepseek',
                profile_id: profile.id,
                model: 'deepseek-chat',
                active: true,
              },
            ],
            default_model_id: 'deepseek-chat',
            has_more: false,
          })
        )
      );
    vi.stubGlobal('fetch', fetchMock);

    const providers = await loadSynonBiomedLlmProviders();
    const models = await loadSynonBiomedLlmModels();

    expect(providers.activeProfileId).toBe(profile.id);
    expect(providers.profiles).toEqual([profile]);
    expect(providers.templates[0]).toMatchObject({ provider: 'deepseek', protocol: 'openai' });
    expect(models.defaultModelId).toBe('deepseek-chat');
    expect(models.models[0]).toMatchObject({ profileId: profile.id, active: true });
    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      '/api/llm/providers',
      expect.objectContaining({ headers: { Accept: 'application/json' } })
    );
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/v1/models',
      expect.objectContaining({ headers: { Accept: 'application/json' } })
    );
  });

  it('saves, activates, tests and deletes profiles without exposing stored credentials', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(new Response(JSON.stringify({ ok: true, profile })))
      .mockResolvedValueOnce(new Response(JSON.stringify({ ok: true, profile })))
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ ok: true, result: { text: 'Provider reachable.', model: profile.model } }))
      )
      .mockResolvedValueOnce(new Response(JSON.stringify({ ok: true, activeProfileId: undefined, profiles: [] })));
    vi.stubGlobal('fetch', fetchMock);

    await saveSynonBiomedLlmProfile({
      name: profile.name,
      provider: profile.provider,
      baseUrl: profile.baseUrl,
      model: profile.model,
      apiKey: 'secret-value',
      temperature: profile.temperature,
      maxTokens: profile.maxTokens,
    });
    await activateSynonBiomedLlmProfile(profile);
    const result = await testSynonBiomedLlmProfile(profile.id);
    await deleteSynonBiomedLlmProfile(profile.id);

    expect(result).toEqual({ text: 'Provider reachable.', model: 'deepseek-chat' });
    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      '/api/llm/providers',
      expect.objectContaining({
        method: 'POST',
        body: expect.stringContaining('secret-value'),
      })
    );
    const activateBody = JSON.parse(String(fetchMock.mock.calls[1]?.[1]?.body)) as Record<string, unknown>;
    expect(activateBody).toMatchObject({ id: profile.id, copyApiKeyFrom: profile.id });
    expect(activateBody).not.toHaveProperty('apiKey');
    expect(fetchMock).toHaveBeenNthCalledWith(
      3,
      '/api/llm/test',
      expect.objectContaining({ method: 'POST', body: JSON.stringify({ profileId: profile.id }) })
    );
    expect(fetchMock).toHaveBeenNthCalledWith(
      4,
      `/api/llm/providers/${profile.id}`,
      expect.objectContaining({ method: 'DELETE' })
    );
  });
});

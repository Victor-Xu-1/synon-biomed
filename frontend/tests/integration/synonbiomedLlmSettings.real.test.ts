import { describe, expect, it } from 'vitest';
import { createSynonBiomedTestFetch } from './synonbiomedTestAuth';
import { createControlledLlmFixture } from './synonbiomedControlledLlmFixture';

const gatewayBaseUrl = process.env.SYNON_BIOMED_GATEWAY_URL ?? 'http://127.0.0.1:8766';
const authenticatedFetch = createSynonBiomedTestFetch(gatewayBaseUrl);

type Profile = {
  id: string;
  name: string;
  provider: string;
  baseUrl: string;
  model: string;
  temperature?: number;
  maxTokens?: number;
  hasApiKey: boolean;
};

type Snapshot = {
  ok: boolean;
  activeProfileId?: string;
  profiles: Profile[];
  templates: Array<{ provider: string }>;
};

async function requestJson<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await (await authenticatedFetch)(`${gatewayBaseUrl}${path}`, init);
  if (!response.ok) throw new Error(`${init?.method ?? 'GET'} ${path}: ${response.status} ${await response.text()}`);
  return response.json() as Promise<T>;
}

describe('Synon Biomed real LLM settings lifecycle', () => {
  it('lists, tests, creates, deletes and restores real LLM Core profiles', async () => {
    const fixture = await createControlledLlmFixture(gatewayBaseUrl);
    const active = fixture.profile;
    try {
      const initial = await requestJson<Snapshot>('/api/llm/providers');
      expect(initial.activeProfileId).toBe(active.id);
      expect(active.hasApiKey).toBe(true);
      expect(initial.templates.length).toBeGreaterThan(0);

      const testResult = await requestJson<{ ok: boolean; result: { text: string; model: string } }>('/api/llm/test', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ profileId: active!.id }),
      });
      expect(testResult.ok).toBe(true);
      expect(testResult.result.model).toBe(active!.model);
      expect(testResult.result.text.trim().length).toBeGreaterThan(0);

      const temporaryName = `codex-real-llm-${Date.now()}`;
      let temporaryId: string | undefined;
      try {
        const created = await requestJson<{ profile: Profile }>('/api/llm/providers', {
          method: 'POST',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify({
            name: temporaryName,
            provider: active!.provider,
            baseUrl: active!.baseUrl,
            model: active!.model,
            temperature: active!.temperature,
            maxTokens: active!.maxTokens,
            copyApiKeyFrom: active!.id,
          }),
        });
        temporaryId = created.profile.id;
        const during = await requestJson<Snapshot>('/api/llm/providers');
        expect(during.activeProfileId).toBe(temporaryId);
        expect(during.profiles.find((profile) => profile.id === temporaryId)).toMatchObject({
          name: temporaryName,
          hasApiKey: true,
        });
      } finally {
        if (temporaryId) {
          await requestJson(`/api/llm/providers/${encodeURIComponent(temporaryId)}`, { method: 'DELETE' });
        }
        await requestJson('/api/llm/providers', {
          method: 'POST',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify({
            id: active.id,
            name: active.name,
            provider: active.provider,
            baseUrl: active.baseUrl,
            model: active.model,
            temperature: active.temperature,
            maxTokens: active.maxTokens,
            copyApiKeyFrom: active.id,
          }),
        });
      }

      const restored = await requestJson<Snapshot>('/api/llm/providers');
      expect(restored.activeProfileId).toBe(active.id);
      expect(restored.profiles.some((profile) => profile.name === temporaryName)).toBe(false);

      const models = await requestJson<{
        data: Array<{ profile_id: string; active: boolean }>;
        default_model_id: string;
      }>('/v1/models');
      expect(models.data.some((model) => model.profile_id === active.id && model.active)).toBe(true);
      expect(models.default_model_id).toBeTruthy();
    } finally {
      await fixture.dispose();
    }
  }, 60_000);
});

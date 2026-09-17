import { describe, expect, it } from 'vitest';
import { createSynonBiomedTestFetch } from './synonbiomedTestAuth';

const gatewayBaseUrl = process.env.SYNON_BIOMED_GATEWAY_URL ?? 'http://127.0.0.1:8766';
const settingKey = 'tools.speechToText';

describe('Synon Biomed speech settings gateway', () => {
  it('persists and restores the complete speech configuration through the authenticated WebHost', async () => {
    const fetchImpl = await createSynonBiomedTestFetch(gatewayBaseUrl);
    const readUrl = `${gatewayBaseUrl}/api/settings/client?keys=${encodeURIComponent(settingKey)}`;
    const originalResponse = await fetchImpl(readUrl, { headers: { accept: 'application/json' } });
    expect(originalResponse.ok).toBe(true);
    const original = (await originalResponse.json()) as Record<string, unknown>;

    const candidate = {
      enabled: false,
      openai: {
        api_key: '',
        base_url: 'https://api.openai.com/v1',
        model: 'gpt-4o-mini-transcribe',
        language: 'zh',
        prompt: '以下是简体中文普通话。',
      },
      deepgram: { api_key: '', model: 'nova-3', language: 'zh-CN' },
    };

    try {
      const write = await fetchImpl(`${gatewayBaseUrl}/api/settings/client`, {
        method: 'PUT',
        headers: { accept: 'application/json', 'content-type': 'application/json' },
        body: JSON.stringify({ [settingKey]: candidate }),
      });
      expect(write.ok).toBe(true);

      const persistedResponse = await fetchImpl(readUrl, { headers: { accept: 'application/json' } });
      expect(persistedResponse.ok).toBe(true);
      const persisted = (await persistedResponse.json()) as Record<string, unknown>;
      expect(persisted[settingKey]).toEqual(candidate);
    } finally {
      const restoreValue = Object.prototype.hasOwnProperty.call(original, settingKey) ? original[settingKey] : null;
      const restore = await fetchImpl(`${gatewayBaseUrl}/api/settings/client`, {
        method: 'PUT',
        headers: { accept: 'application/json', 'content-type': 'application/json' },
        body: JSON.stringify({ [settingKey]: restoreValue }),
      });
      expect(restore.ok).toBe(true);
    }
  });
});

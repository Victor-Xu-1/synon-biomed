import { describe, expect, it, vi } from 'vitest';
import { checkSynonBiomedRuntimeUpdate } from '@/renderer/services/synonBiomedRuntimeUpdate';

describe('checkSynonBiomedRuntimeUpdate', () => {
  it('posts to the runtime update endpoint and normalizes its status', async () => {
    const fetchImpl = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          channel: 'stable',
          current: '0.1.0',
          latest: '0.2.0',
          checkedAt: '2026-08-09T08:00:00Z',
          error: null,
          autoUpdate: false,
          required: null,
        }),
        { status: 200, headers: { 'content-type': 'application/json' } }
      )
    );

    await expect(checkSynonBiomedRuntimeUpdate({ fetchImpl })).resolves.toEqual({
      channel: 'stable',
      current: '0.1.0',
      latest: '0.2.0',
      checkedAt: '2026-08-09T08:00:00Z',
      error: null,
      autoUpdate: false,
      required: null,
    });
    expect(fetchImpl).toHaveBeenCalledWith(
      '/api/status/update/check',
      expect.objectContaining({ method: 'POST', credentials: 'include' })
    );
  });

  it('rejects incomplete update status payloads', async () => {
    const fetchImpl = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ current: '0.1.0' }), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      })
    );

    await expect(checkSynonBiomedRuntimeUpdate({ fetchImpl })).rejects.toThrow(
      'Synon Biomed update status response is invalid'
    );
  });
});

import { describe, expect, it, vi } from 'vitest';
import { askSynonBiomedAsideQuestion, createSynonBiomedBranchSession } from '@/renderer/services/synonBiomedAside';

describe('Synon Biomed aside service', () => {
  it('creates a hidden v1.1 aside and returns the persisted frame response', async () => {
    const fetchImpl = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ frame_id: 'aside-1', prefix_len: 4 }), {
          status: 200,
          headers: { 'content-type': 'application/json' },
        })
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ id: 'aside-1', status: 'processing', output_data: {} }), {
          status: 200,
          headers: { 'content-type': 'application/json' },
        })
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({ id: 'aside-1', status: 'completed', output_data: { response: 'Independent answer' } }),
          { status: 200, headers: { 'content-type': 'application/json' } }
        )
      ) as unknown as typeof fetch;

    await expect(
      askSynonBiomedAsideQuestion('parent/1', '  Check independently  ', { fetchImpl, pollIntervalMs: 0 })
    ).resolves.toEqual({ status: 'ok', answer: 'Independent answer', frameId: 'aside-1' });

    const [createUrl, createInit] = fetchImpl.mock.calls[0] as [string, RequestInit];
    expect(createUrl).toBe('/api/frames/parent%2F1/aside');
    expect(createInit.method).toBe('POST');
    expect(JSON.parse(String(createInit.body))).toMatchObject({
      request: 'Check independently',
      model: null,
      as_session: false,
      intent_id: expect.stringMatching(/^[0-9a-f-]{36}$/),
    });
    expect(fetchImpl).toHaveBeenNthCalledWith(2, '/api/frames/aside-1?shallow=true', expect.any(Object));
  });

  it('creates a v1.1 branch session with the selected model', async () => {
    const fetchImpl = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ frame_id: 'branch-1', prefix_len: 9 }), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      })
    ) as unknown as typeof fetch;

    await expect(
      createSynonBiomedBranchSession('parent/1', '  Continue independently  ', {
        fetchImpl,
        model: 'deepseek-v4-flash',
      })
    ).resolves.toEqual({ frameId: 'branch-1' });

    const [url, init] = fetchImpl.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/frames/parent%2F1/aside');
    expect(init.method).toBe('POST');
    expect(JSON.parse(String(init.body))).toMatchObject({
      request: 'Continue independently',
      model: 'deepseek-v4-flash',
      as_session: true,
      intent_id: expect.stringMatching(/^[0-9a-f-]{36}$/),
    });
  });
});

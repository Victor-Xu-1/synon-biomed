import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  createSynonBiomedSessionDefaults,
  loadSynonBiomedSessionOptions,
  SYNON_BIOMED_SESSION_DEFAULTS,
  updateSynonBiomedSessionConfig,
} from '@/renderer/services/synonBiomedSessionOptions';

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe('Synon Biomed session options service', () => {
  it('owns one immutable optional-features-off default and returns isolated draft copies', () => {
    const first = createSynonBiomedSessionDefaults('AIDD_EXPERT');
    const second = createSynonBiomedSessionDefaults('AIDD_EXPERT');

    expect(SYNON_BIOMED_SESSION_DEFAULTS).toMatchObject({
      delegation: false,
      autoReview: false,
      memory: false,
      targetAgent: 'OPERON',
    });
    expect(first).toEqual({
      delegation: false,
      autoReview: false,
      memory: false,
      targetAgent: 'AIDD_EXPERT',
      goalText: null,
      asRoutine: false,
    });
    expect(first).not.toBe(second);
  });

  it('reproduces v1.1 precedence from original input, input data and frame agent', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          id: 'frame-stat6',
          agent_name: 'OPERON',
          input_data: {
            ultra_mode: true,
            verifier_mode: 'on',
            memory_mode: 'off',
          },
          context_data: {
            _original_input: {
              ultra_mode: false,
              verifier_mode: 'off',
            },
          },
        })
      )
    );

    const options = await loadSynonBiomedSessionOptions('frame-stat6', fetchMock);

    expect(options).toEqual({
      delegation: false,
      autoReview: false,
      memory: false,
      targetAgent: 'OPERON',
      asRoutine: false,
      goalText: null,
    });
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/frames/frame-stat6',
      expect.objectContaining({ headers: { Accept: 'application/json' } })
    );
  });

  it('keeps every optional module off without a persisted override', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          id: 'frame-stat6',
          agent_name: 'OPERON',
          input_data: {},
          context_data: { _original_input: {} },
        })
      )
    );

    await expect(loadSynonBiomedSessionOptions('frame-stat6', fetchMock)).resolves.toMatchObject({
      delegation: false,
      autoReview: false,
      memory: false,
      targetAgent: 'OPERON',
    });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('loads a conversational next-turn expert switch from durable session metadata', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          id: 'frame-stat6',
          agent_name: 'OPERON',
          input_data: {},
          context_data: { _original_input: { target_agent: 'AIDD_EXPERT' } },
        })
      )
    );

    await expect(loadSynonBiomedSessionOptions('frame-stat6', fetchMock)).resolves.toMatchObject({
      targetAgent: 'AIDD_EXPERT',
    });
  });

  it('persists verifier and memory mode through the exact v1.1 session-config contract', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(new Response(null, { status: 204 }));

    await updateSynonBiomedSessionConfig('frame-stat6', { autoReview: true, memory: false }, fetchMock);

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/frames/frame-stat6/session-config',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ verifier_mode: 'on', memory_mode: 'off' }),
      })
    );
  });

  it('surfaces backend validation detail for a rejected session update', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(
      new Response(JSON.stringify({ detail: 'frame is archived' }), {
        status: 409,
        headers: { 'content-type': 'application/json' },
      })
    );

    await expect(updateSynonBiomedSessionConfig('frame-stat6', { autoReview: false }, fetchMock)).rejects.toThrow(
      'frame is archived'
    );
  });

  it('aborts a stalled session read and exposes the existing retry path', async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn<typeof fetch>(
      (_url, init) =>
        new Promise<Response>((_resolve, reject) => {
          init?.signal?.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), {
            once: true,
          });
        })
    );

    const request = loadSynonBiomedSessionOptions('frame-stalled', fetchMock);
    const rejection = expect(request).rejects.toThrow('Synon Biomed session request timed out');
    await vi.advanceTimersByTimeAsync(8_000);

    await rejection;
  });

  it('keeps the session deadline active while reading the response body', async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn<typeof fetch>((_url, init) =>
      Promise.resolve(
        new Response(
          new ReadableStream({
            start(controller) {
              init?.signal?.addEventListener(
                'abort',
                () => controller.error(new DOMException('Aborted', 'AbortError')),
                { once: true }
              );
            },
          }),
          { status: 200, headers: { 'content-type': 'application/json' } }
        )
      )
    );

    const request = loadSynonBiomedSessionOptions('frame-stalled-body', fetchMock);
    const rejection = expect(request).rejects.toThrow('Synon Biomed session request timed out');
    await vi.advanceTimersByTimeAsync(8_000);

    await rejection;
  });
});

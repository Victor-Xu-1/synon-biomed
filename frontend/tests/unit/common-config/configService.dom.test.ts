import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { configService } from '@/common/config/configService';

describe('configService WebUI bootstrap', () => {
  beforeEach(() => {
    configService.reset();
  });

  afterEach(() => {
    configService.reset();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it('loads only the scoped public UI preferences before authentication', async () => {
    const fetchMock = vi.fn(
      async () =>
        new Response(
          JSON.stringify({
            language: 'zh-CN',
            'theme.activeId': 'synonbiomed-light',
            'theme.userThemes': [],
          }),
          {
            status: 200,
            headers: { 'content-type': 'application/json' },
          }
        )
    );
    vi.stubGlobal('fetch', fetchMock);

    await configService.initialize();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/settings/client?scope=ui-preferences',
      expect.objectContaining({ method: 'GET' })
    );
    expect(configService.get('language')).toBe('zh-CN');
  });

  it('keeps an empty WebUI theme migration local until the user is authenticated', async () => {
    const fetchMock = vi.fn(
      async () =>
        new Response(JSON.stringify({}), {
          status: 200,
          headers: { 'content-type': 'application/json' },
        })
    );
    vi.stubGlobal('fetch', fetchMock);

    await configService.initialize();
    await Promise.resolve();

    expect(fetchMock.mock.calls.map(([, init]) => init?.method)).toEqual(['GET']);
    expect(configService.get('theme.activeId')).toBeDefined();
  });

  it('aborts a stalled preference request so renderer startup can recover', async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn(
      (_url: string, init?: RequestInit) =>
        new Promise<Response>((_resolve, reject) => {
          init?.signal?.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), {
            once: true,
          });
        })
    );
    vi.stubGlobal('fetch', fetchMock);

    const initialization = configService.initialize();
    await vi.advanceTimersByTimeAsync(8_000);

    await expect(initialization).rejects.toThrow('config_request_timeout');
    expect(configService.isInitialized()).toBe(false);
  });

  it('keeps the preference deadline active while reading the response body', async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn((_url: string, init?: RequestInit) =>
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
    vi.stubGlobal('fetch', fetchMock);

    const initialization = configService.initialize();
    const rejection = expect(initialization).rejects.toThrow('config_request_timeout');
    await vi.advanceTimersByTimeAsync(8_000);

    await rejection;
  });
});

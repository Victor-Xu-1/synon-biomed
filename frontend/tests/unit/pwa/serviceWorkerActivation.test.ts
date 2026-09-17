import { readFile } from 'node:fs/promises';
import vm from 'node:vm';
import { resolve } from 'node:path';
import { describe, expect, it, vi } from 'vitest';

type ExtendableEvent = {
  waitUntil(promise: Promise<unknown>): void;
};

type ServiceWorkerListener = (event: ExtendableEvent) => void;

async function activateServiceWorker(cacheKeys: string[]) {
  const listeners = new Map<string, ServiceWorkerListener>();
  const deleteCache = vi.fn(async () => true);
  const claim = vi.fn(async () => undefined);
  const navigate = vi.fn(async () => undefined);
  const matchAll = vi.fn(async () => [{ url: 'http://127.0.0.1:28081/#/login', navigate }]);

  const source = await readFile(resolve(process.cwd(), 'public/sw.js'), 'utf8');
  vm.runInNewContext(source, {
    self: {
      location: { href: 'http://127.0.0.1:28081/sw.js' },
      addEventListener: (type: string, listener: ServiceWorkerListener) => listeners.set(type, listener),
      skipWaiting: async () => undefined,
      clients: { claim, matchAll },
    },
    caches: {
      keys: async () => cacheKeys,
      delete: deleteCache,
      open: async () => ({
        addAll: async () => undefined,
        match: async () => undefined,
        put: async () => undefined,
        delete: async () => true,
      }),
    },
    URL,
    Request,
    Response,
    Promise,
    Set,
    console,
  });

  let activation: Promise<unknown> | undefined;
  listeners.get('activate')?.({
    waitUntil(promise) {
      activation = promise;
    },
  });
  expect(activation).toBeDefined();
  await activation;

  return { claim, deleteCache, matchAll, navigate };
}

describe('WebUI service worker activation', () => {
  it('claims clients without navigating them on first installation', async () => {
    const result = await activateServiceWorker([]);

    expect(result.claim).toHaveBeenCalledTimes(1);
    expect(result.deleteCache).not.toHaveBeenCalled();
    expect(result.matchAll).not.toHaveBeenCalled();
    expect(result.navigate).not.toHaveBeenCalled();
  });

  it('deletes an obsolete SynonAI cache and refreshes controlled clients during an upgrade', async () => {
    const result = await activateServiceWorker(['synon-ai-webui-v1', 'synon-ai-webui-v2']);

    expect(result.deleteCache).toHaveBeenCalledTimes(1);
    expect(result.deleteCache).toHaveBeenCalledWith('synon-ai-webui-v1');
    expect(result.matchAll).toHaveBeenCalledTimes(1);
    expect(result.navigate).toHaveBeenCalledWith('http://127.0.0.1:28081/#/login');
  });

  it('leaves unrelated origin caches untouched and does not refresh clients for them', async () => {
    const result = await activateServiceWorker(['third-party-cache', 'synon-ai-webui-v2']);

    expect(result.deleteCache).not.toHaveBeenCalled();
    expect(result.matchAll).not.toHaveBeenCalled();
    expect(result.navigate).not.toHaveBeenCalled();
  });
});

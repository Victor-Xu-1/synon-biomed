import { createKeyedSnapshotStore } from '@/renderer/services/keyedSnapshotStore';
import { describe, expect, it, vi } from 'vitest';

describe('keyedSnapshotStore', () => {
  it('keeps the last good snapshot while a refresh fails', async () => {
    const store = createKeyedSnapshotStore<string[]>();
    store.write('owner-a:conversation-a', ['stable']);

    await expect(
      store.load('owner-a:conversation-a', async () => {
        throw new Error('temporary');
      })
    ).rejects.toThrow('temporary');

    expect(store.read('owner-a:conversation-a')?.value).toEqual(['stable']);
  });

  it('singleflights equal reads and aborts a force-replaced request', async () => {
    const store = createKeyedSnapshotStore<string>();
    const first = deferred<string>();
    let firstSignal: AbortSignal | undefined;
    const loader = vi.fn((signal: AbortSignal) => {
      firstSignal = signal;
      signal.addEventListener('abort', () => first.reject(new DOMException('Aborted', 'AbortError')), { once: true });
      return first.promise;
    });

    const firstRead = store.load('owner-a:conversation-a', loader);
    expect(store.load('owner-a:conversation-a', loader)).toBe(firstRead);
    const replacement = store.load('owner-a:conversation-a', async () => 'fresh', true);

    await expect(firstRead).rejects.toMatchObject({ name: 'AbortError' });
    await expect(replacement).resolves.toBe('fresh');
    expect(firstSignal?.aborted).toBe(true);
    expect(store.read('owner-a:conversation-a')?.value).toBe('fresh');
  });

  it('ensures from cache without loading and joins an authoritative refresh already in flight', async () => {
    const store = createKeyedSnapshotStore<string>();
    const loader = vi.fn(async () => 'unexpected');
    store.write('owner-a:project-a', 'cached');

    await expect(store.ensure('owner-a:project-a', loader)).resolves.toBe('cached');
    expect(loader).not.toHaveBeenCalled();

    const refresh = deferred<string>();
    const force = store.load('owner-a:project-a', () => refresh.promise, true);
    const ensured = store.ensure('owner-a:project-a', loader);
    expect(ensured).toBe(force);
    refresh.resolve('fresh');

    await expect(ensured).resolves.toBe('fresh');
    expect(loader).not.toHaveBeenCalled();
    expect(store.read('owner-a:project-a')?.value).toBe('fresh');
  });

  it('never returns an expired snapshot', () => {
    let now = 0;
    const store = createKeyedSnapshotStore<string>({ ttlMs: 100, now: () => now });
    store.write('owner-a:conversation-a', 'value');
    now = 101;

    expect(store.read('owner-a:conversation-a')).toBeUndefined();
  });
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

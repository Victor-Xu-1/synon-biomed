import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  clearOnboardingCompletionOwnerSession,
  createOnboardingCompletionAuthority,
  onboardingCompletionStorageKey,
  primeOnboardingCompletion,
  type OnboardingCompletionStorage,
} from '@/renderer/services/onboardingCompletionAuthority';

const onboardingService = vi.hoisted(() => ({
  load: vi.fn<() => Promise<boolean>>(),
}));

vi.mock('@/renderer/services/onboardingService', () => ({
  loadOnboardingCompletion: onboardingService.load,
}));

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function memoryStorage(): OnboardingCompletionStorage & { values: Map<string, string> } {
  const values = new Map<string, string>();
  return {
    values,
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => values.set(key, value),
    removeItem: (key) => values.delete(key),
  };
}

describe('onboardingCompletionAuthority', () => {
  afterEach(() => {
    clearOnboardingCompletionOwnerSession();
    onboardingService.load.mockReset();
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it('single-flights the initial fail-closed read for one owner', async () => {
    const completion = deferred<boolean>();
    const loadCompletion = vi.fn(() => completion.promise);
    const authority = createOnboardingCompletionAuthority({ loadCompletion, storage: memoryStorage() });

    authority.activateOwner('owner-a');
    const first = authority.ensureCompletion('owner-a');
    const duplicate = authority.ensureCompletion('owner-a');

    expect(duplicate).toBe(first);
    expect(loadCompletion).toHaveBeenCalledTimes(1);
    expect(authority.getSnapshot()).toMatchObject({ ownerId: 'owner-a', state: 'checking' });

    completion.resolve(false);
    await first;
    expect(authority.getSnapshot()).toMatchObject({ ownerId: 'owner-a', state: 'required' });
  });

  it('uses only an owner-scoped positive session hint and closes on authoritative false', async () => {
    const storage = memoryStorage();
    storage.setItem(onboardingCompletionStorageKey('owner-a'), 'true');
    const completion = deferred<boolean>();
    const loadCompletion = vi.fn(() => completion.promise);
    const authority = createOnboardingCompletionAuthority({ loadCompletion, storage });

    authority.activateOwner('owner-a');
    expect(authority.getSnapshot()).toMatchObject({ state: 'complete', revalidating: false, source: 'session' });

    const revalidation = authority.ensureCompletion('owner-a');
    expect(authority.getSnapshot()).toMatchObject({ state: 'complete', revalidating: true });
    completion.resolve(false);
    await revalidation;

    expect(authority.getSnapshot()).toMatchObject({ state: 'required', revalidating: false, source: 'authority' });
    expect(storage.getItem(onboardingCompletionStorageKey('owner-a'))).toBeNull();
  });

  it('does not reuse a positive snapshot across an owner switch', () => {
    const storage = memoryStorage();
    storage.setItem(onboardingCompletionStorageKey('owner-a'), 'true');
    const authority = createOnboardingCompletionAuthority({
      loadCompletion: vi.fn(() => Promise.resolve(true)),
      storage,
    });

    authority.activateOwner('owner-a');
    expect(authority.getSnapshot()).toMatchObject({ ownerId: 'owner-a', state: 'complete' });

    authority.activateOwner('owner-b');
    expect(authority.getSnapshot()).toMatchObject({ ownerId: 'owner-b', state: 'checking', source: 'none' });
    expect(storage.getItem(onboardingCompletionStorageKey('owner-a'))).toBeNull();
  });

  it('fails closed on errors and performs one explicit retry', async () => {
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined);
    const loadCompletion = vi
      .fn<() => Promise<boolean>>()
      .mockRejectedValueOnce(new Error('backend unavailable'))
      .mockResolvedValueOnce(true);
    const authority = createOnboardingCompletionAuthority({ loadCompletion, storage: memoryStorage() });

    authority.activateOwner('owner-a');
    await authority.ensureCompletion('owner-a');
    expect(authority.getSnapshot()).toMatchObject({ state: 'error' });

    await authority.retry('owner-a');
    expect(loadCompletion).toHaveBeenCalledTimes(2);
    expect(authority.getSnapshot()).toMatchObject({ state: 'complete', source: 'authority' });
    expect(consoleError).toHaveBeenCalledWith('[onboarding-completion] authoritative read failed');
  });

  it('aborts a stalled injected loader at the configured authority timeout', async () => {
    vi.useFakeTimers();
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined);
    let observedSignal: AbortSignal | undefined;
    const loadCompletion = vi.fn(({ signal }: { signal?: AbortSignal }) => {
      observedSignal = signal;
      return new Promise<boolean>((_resolve, reject) => {
        signal?.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true });
      });
    });
    const authority = createOnboardingCompletionAuthority({
      loadCompletion,
      storage: memoryStorage(),
      timeoutMs: 8_000,
    });

    authority.activateOwner('owner-a');
    const completion = authority.ensureCompletion('owner-a');
    await vi.advanceTimersByTimeAsync(8_000);
    await completion;

    expect(observedSignal?.aborted).toBe(true);
    expect(authority.getSnapshot()).toMatchObject({ state: 'error', revalidating: false });
    expect(consoleError).toHaveBeenCalledWith('[onboarding-completion] authoritative read failed');
  });

  it('single-flights authenticated bootstrap primes through the shared authority', async () => {
    const completion = deferred<boolean>();
    onboardingService.load.mockImplementation(() => completion.promise);
    clearOnboardingCompletionOwnerSession('owner-a');

    const primed = primeOnboardingCompletion('owner-a');
    const duplicate = primeOnboardingCompletion('owner-a');

    expect(duplicate).toBe(primed);
    await vi.waitFor(() => expect(onboardingService.load).toHaveBeenCalledTimes(1));

    completion.resolve(true);
    await primed;
    await duplicate;
    expect(onboardingService.load).toHaveBeenCalledTimes(1);

    clearOnboardingCompletionOwnerSession('owner-a');
  });

  it('publishes a successful completion mutation before stale route reads can redirect', async () => {
    const storage = memoryStorage();
    const completion = deferred<boolean>();
    const authority = createOnboardingCompletionAuthority({
      loadCompletion: vi.fn(() => completion.promise),
      storage,
    });

    authority.activateOwner('owner-a');
    const staleRead = authority.ensureCompletion('owner-a');
    authority.confirmCompletion('owner-a');

    expect(authority.getSnapshot()).toEqual({
      ownerId: 'owner-a',
      state: 'complete',
      revalidating: false,
      source: 'authority',
    });
    expect(storage.getItem(onboardingCompletionStorageKey('owner-a'))).toBe('true');

    completion.resolve(false);
    await staleRead;
    expect(authority.getSnapshot()).toMatchObject({ state: 'complete', source: 'authority' });
  });
});

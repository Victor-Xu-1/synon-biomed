import { useCallback, useEffect, useSyncExternalStore } from 'react';

export type OnboardingCompletionState = 'checking' | 'complete' | 'required' | 'error';
export type OnboardingCompletionSource = 'none' | 'session' | 'authority';

export type OnboardingCompletionSnapshot = {
  ownerId: string | null;
  state: OnboardingCompletionState;
  revalidating: boolean;
  source: OnboardingCompletionSource;
};

export type OnboardingCompletionStorage = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>;

type CompletionLoader = (options: { signal?: AbortSignal }) => Promise<boolean>;

type AuthorityOptions = {
  loadCompletion?: CompletionLoader;
  storage?: OnboardingCompletionStorage | null;
  timeoutMs?: number;
};

const loadAuthoritativeCompletion: CompletionLoader = async (options) => {
  const { loadOnboardingCompletion } = await import('@renderer/services/onboardingService');
  return loadOnboardingCompletion(options);
};

type OwnerState = {
  ownerId: string;
  generation: number;
  controller: AbortController | null;
  inFlight: Promise<void> | null;
};

const STORAGE_PREFIX = 'synonbiomed.onboarding.complete.v1:';
const EMPTY_SNAPSHOT: OnboardingCompletionSnapshot = {
  ownerId: null,
  state: 'checking',
  revalidating: false,
  source: 'none',
};

export function onboardingCompletionStorageKey(ownerId: string): string {
  return `${STORAGE_PREFIX}${encodeURIComponent(ownerId)}`;
}

function browserSessionStorage(): OnboardingCompletionStorage | null {
  if (typeof window === 'undefined') return null;
  try {
    return window.sessionStorage;
  } catch {
    return null;
  }
}

export function createOnboardingCompletionAuthority(options: AuthorityOptions = {}) {
  const loadCompletion = options.loadCompletion ?? loadAuthoritativeCompletion;
  const storage = options.storage === undefined ? browserSessionStorage() : options.storage;
  // Production request deadlines belong to the shared HTTP transport policy.
  // This authority only owns cancellation when the authenticated owner
  // changes.  Tests may inject a timeout to exercise abort cleanup without
  // creating a product-specific onboarding gate.
  const timeoutMs = options.timeoutMs;
  const listeners = new Set<() => void>();
  let snapshot = EMPTY_SNAPSHOT;
  let owner: OwnerState | null = null;

  const publish = (next: OnboardingCompletionSnapshot) => {
    snapshot = next;
    for (const listener of listeners) listener();
  };

  const readPositiveSessionHint = (ownerId: string): boolean => {
    try {
      return storage?.getItem(onboardingCompletionStorageKey(ownerId)) === 'true';
    } catch {
      return false;
    }
  };

  const writePositiveSessionHint = (ownerId: string) => {
    try {
      storage?.setItem(onboardingCompletionStorageKey(ownerId), 'true');
    } catch {
      // The authoritative in-memory result remains valid for this mounted session.
    }
  };

  const removePositiveSessionHint = (ownerId: string) => {
    try {
      storage?.removeItem(onboardingCompletionStorageKey(ownerId));
    } catch {
      // Storage is only a performance hint and never an authorization authority.
    }
  };

  const activateOwner = (ownerId: string): void => {
    const normalizedOwnerId = ownerId.trim();
    if (!normalizedOwnerId) return;
    if (owner?.ownerId === normalizedOwnerId) return;

    if (owner) {
      const previousOwnerId = owner.ownerId;
      owner.generation += 1;
      owner.controller?.abort('onboarding_owner_changed');
      removePositiveSessionHint(previousOwnerId);
    }

    owner = {
      ownerId: normalizedOwnerId,
      generation: 0,
      controller: null,
      inFlight: null,
    };
    const hasPositiveHint = readPositiveSessionHint(normalizedOwnerId);
    publish({
      ownerId: normalizedOwnerId,
      state: hasPositiveHint ? 'complete' : 'checking',
      revalidating: false,
      source: hasPositiveHint ? 'session' : 'none',
    });
  };

  const startCompletionRead = (ownerId: string, retry: boolean): Promise<void> => {
    const activeOwner = owner;
    if (!activeOwner || activeOwner.ownerId !== ownerId) return Promise.resolve();
    if (activeOwner.inFlight) return activeOwner.inFlight;
    if (!retry && snapshot.state === 'complete' && snapshot.source === 'authority') return Promise.resolve();
    if (!retry && snapshot.state === 'error') return Promise.resolve();

    const generation = activeOwner.generation + 1;
    activeOwner.generation = generation;
    const controller = new AbortController();
    activeOwner.controller = controller;
    const preservePositiveHint = snapshot.state === 'complete' && snapshot.source === 'session';
    publish({
      ownerId,
      state: preservePositiveHint ? 'complete' : 'checking',
      revalidating: preservePositiveHint,
      source: preservePositiveHint ? 'session' : 'none',
    });

    const timeoutId =
      timeoutMs !== undefined && timeoutMs > 0
        ? setTimeout(() => controller.abort('onboarding_completion_timeout'), timeoutMs)
        : undefined;
    const operation = (async () => {
      try {
        const complete = await loadCompletion({ signal: controller.signal });
        if (owner !== activeOwner || activeOwner.generation !== generation) return;
        if (complete) {
          writePositiveSessionHint(ownerId);
          publish({ ownerId, state: 'complete', revalidating: false, source: 'authority' });
        } else {
          removePositiveSessionHint(ownerId);
          publish({ ownerId, state: 'required', revalidating: false, source: 'authority' });
        }
      } catch {
        if (owner !== activeOwner || activeOwner.generation !== generation) return;
        removePositiveSessionHint(ownerId);
        console.error('[onboarding-completion] authoritative read failed');
        publish({ ownerId, state: 'error', revalidating: false, source: 'authority' });
      } finally {
        if (timeoutId !== undefined) clearTimeout(timeoutId);
        if (owner === activeOwner && activeOwner.generation === generation) {
          activeOwner.controller = null;
          activeOwner.inFlight = null;
        }
      }
    })();
    activeOwner.inFlight = operation;
    return operation;
  };

  const ensureCompletion = (ownerId: string): Promise<void> => {
    const normalizedOwnerId = ownerId.trim();
    if (!normalizedOwnerId) return Promise.resolve();
    if (!owner) activateOwner(normalizedOwnerId);
    return startCompletionRead(normalizedOwnerId, false);
  };

  const retry = (ownerId: string): Promise<void> => {
    const normalizedOwnerId = ownerId.trim();
    if (!normalizedOwnerId) return Promise.resolve();
    if (!owner) activateOwner(normalizedOwnerId);
    return startCompletionRead(normalizedOwnerId, true);
  };

  const confirmCompletion = (ownerId: string): void => {
    const normalizedOwnerId = ownerId.trim();
    if (!normalizedOwnerId) return;
    if (!owner || owner.ownerId !== normalizedOwnerId) activateOwner(normalizedOwnerId);
    const activeOwner = owner;
    if (!activeOwner || activeOwner.ownerId !== normalizedOwnerId) return;
    activeOwner.generation += 1;
    activeOwner.controller?.abort('onboarding_completed');
    activeOwner.controller = null;
    activeOwner.inFlight = null;
    writePositiveSessionHint(normalizedOwnerId);
    publish({ ownerId: normalizedOwnerId, state: 'complete', revalidating: false, source: 'authority' });
  };

  const clearOwner = (ownerId?: string): void => {
    const normalizedOwnerId = ownerId?.trim();
    if (normalizedOwnerId) removePositiveSessionHint(normalizedOwnerId);
    if (!owner || (normalizedOwnerId && owner.ownerId !== normalizedOwnerId)) return;
    removePositiveSessionHint(owner.ownerId);
    owner.generation += 1;
    owner.controller?.abort('onboarding_owner_session_cleared');
    owner = null;
    publish(EMPTY_SNAPSHOT);
  };

  return {
    activateOwner,
    ensureCompletion,
    retry,
    confirmCompletion,
    clearOwner,
    subscribe: (listener: () => void) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    getSnapshot: () => snapshot,
  };
}

const onboardingCompletionAuthority = createOnboardingCompletionAuthority();

export function clearOnboardingCompletionOwnerSession(ownerId?: string): void {
  onboardingCompletionAuthority.clearOwner(ownerId);
}

export function primeOnboardingCompletion(ownerId: string): Promise<void> {
  const normalizedOwnerId = ownerId.trim();
  if (!normalizedOwnerId) return Promise.resolve();
  onboardingCompletionAuthority.activateOwner(normalizedOwnerId);
  return onboardingCompletionAuthority.ensureCompletion(normalizedOwnerId);
}

export function confirmOnboardingCompletion(ownerId: string): void {
  onboardingCompletionAuthority.confirmCompletion(ownerId);
}

export function useOnboardingCompletionGate(ownerId: string): {
  state: OnboardingCompletionState;
  revalidating: boolean;
  retry: () => Promise<void>;
} {
  const snapshot = useSyncExternalStore(
    onboardingCompletionAuthority.subscribe,
    onboardingCompletionAuthority.getSnapshot,
    onboardingCompletionAuthority.getSnapshot
  );

  useEffect(() => {
    if (!ownerId) return;
    void primeOnboardingCompletion(ownerId);
  }, [ownerId]);

  const retry = useCallback(() => onboardingCompletionAuthority.retry(ownerId), [ownerId]);
  if (!ownerId || snapshot.ownerId !== ownerId) {
    return { state: 'checking', revalidating: false, retry };
  }
  return { state: snapshot.state, revalidating: snapshot.revalidating, retry };
}

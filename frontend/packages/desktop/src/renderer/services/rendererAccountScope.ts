/**
 * Renderer-local account boundary for state that lives outside React/SWR.
 *
 * SWR is remounted per authenticated owner by RealtimeProvider, but module
 * singletons otherwise survive login/logout and HMR. Every singleton cache
 * either includes this scope in its key or registers a bounded reset here.
 */

type RendererAccountReset = () => void;

type RendererAccountScopeState = {
  resets: Map<string, RendererAccountReset>;
  ownerId: string;
  generation: number;
};

const scopeStateKey = '__SYNON_BIOMED_RENDERER_ACCOUNT_SCOPE__' as const;
const globalScope = globalThis as typeof globalThis & {
  [scopeStateKey]?: RendererAccountScopeState;
};
const state = (globalScope[scopeStateKey] ??= {
  resets: new Map(),
  ownerId: '',
  generation: 0,
});

export function getRendererAccountOwnerId(): string {
  return state.ownerId;
}

export function getRendererAccountScopeToken(): string {
  return JSON.stringify([state.generation, state.ownerId]);
}

export function rendererAccountScopedKey(localKey: string): string {
  return JSON.stringify([state.generation, state.ownerId, localKey]);
}

export function registerRendererAccountReset(key: string, reset: RendererAccountReset): () => void {
  state.resets.set(key, reset);
  return () => {
    if (state.resets.get(key) === reset) state.resets.delete(key);
  };
}

/**
 * Advances the account generation before clearing registered state so an
 * already-running request from the previous owner can never repopulate a key
 * visible to the next owner.
 */
export function setRendererAccountOwner(nextOwnerId: string | null | undefined): boolean {
  const normalized = nextOwnerId?.trim() ?? '';
  if (normalized === state.ownerId) return false;
  state.ownerId = normalized;
  state.generation += 1;
  for (const reset of state.resets.values()) {
    try {
      reset();
    } catch (error) {
      console.error('[account-scope] renderer cache reset failed', error);
    }
  }
  return true;
}

export function resetRendererAccountScopeForTest(): void {
  state.ownerId = '';
  state.generation = 0;
}

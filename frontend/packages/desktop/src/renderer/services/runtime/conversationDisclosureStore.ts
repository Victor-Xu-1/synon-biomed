import { useCallback, useMemo, useSyncExternalStore } from 'react';
import { registerRendererAccountReset } from '@/renderer/services/rendererAccountScope';

const MAX_DISCLOSURE_ENTRIES = 2048;

type DisclosureEntry = {
  expanded: boolean;
  touchedAt: number;
};

class ConversationDisclosureStore {
  private readonly entries = new Map<string, DisclosureEntry>();
  private readonly listeners = new Map<string, Set<() => void>>();

  read(key: string, fallback: boolean): boolean {
    return this.entries.get(key)?.expanded ?? fallback;
  }

  subscribe(key: string, listener: () => void): () => void {
    const listeners = this.listeners.get(key) ?? new Set<() => void>();
    listeners.add(listener);
    this.listeners.set(key, listeners);
    return () => {
      listeners.delete(listener);
      if (listeners.size === 0) this.listeners.delete(key);
    };
  }

  setManual(key: string, expanded: boolean): void {
    this.write(key, { expanded, touchedAt: Date.now() });
  }

  clear(): void {
    if (this.entries.size === 0) return;
    this.entries.clear();
    for (const listeners of this.listeners.values()) {
      for (const listener of listeners) listener();
    }
  }

  resetForTests(): void {
    this.entries.clear();
    this.listeners.clear();
  }

  private write(key: string, entry: DisclosureEntry): void {
    const previous = this.entries.get(key);
    if (previous?.expanded === entry.expanded) return;
    this.entries.delete(key);
    this.entries.set(key, entry);
    this.evictOldEntries();
    for (const listener of this.listeners.get(key) ?? []) listener();
  }

  private evictOldEntries(): void {
    if (this.entries.size <= MAX_DISCLOSURE_ENTRIES) return;
    const candidates = [...this.entries.entries()]
      .filter(([key]) => !this.listeners.has(key))
      .toSorted((left, right) => left[1].touchedAt - right[1].touchedAt);
    for (const [key] of candidates) {
      if (this.entries.size <= MAX_DISCLOSURE_ENTRIES) break;
      this.entries.delete(key);
    }
  }
}

export const conversationDisclosureStore = new ConversationDisclosureStore();

registerRendererAccountReset('conversation-disclosure-state', () => conversationDisclosureStore.clear());

export type ConversationDisclosureIdentity = {
  conversationId: string;
  branchId?: string;
  operationId: string;
  path: string;
};

export function useConversationDisclosure(
  identity: ConversationDisclosureIdentity,
  options: { defaultExpanded?: boolean } = {}
): { expanded: boolean; setExpanded: (expanded: boolean) => void; toggle: () => void } {
  const defaultExpanded = options.defaultExpanded === true;
  const key = useMemo(
    () =>
      `${encodeURIComponent(identity.conversationId)}::${encodeURIComponent(identity.branchId ?? '')}::${encodeURIComponent(
        identity.operationId
      )}::${encodeURIComponent(identity.path)}`,
    [identity.branchId, identity.conversationId, identity.operationId, identity.path]
  );
  const subscribe = useCallback((listener: () => void) => conversationDisclosureStore.subscribe(key, listener), [key]);
  const getSnapshot = useCallback(() => conversationDisclosureStore.read(key, defaultExpanded), [defaultExpanded, key]);
  const expanded = useSyncExternalStore(subscribe, getSnapshot, getSnapshot);

  const setExpanded = useCallback(
    (nextExpanded: boolean) => conversationDisclosureStore.setManual(key, nextExpanded),
    [key]
  );
  const toggle = useCallback(
    () => setExpanded(!conversationDisclosureStore.read(key, defaultExpanded)),
    [defaultExpanded, key, setExpanded]
  );
  return { expanded, setExpanded, toggle };
}

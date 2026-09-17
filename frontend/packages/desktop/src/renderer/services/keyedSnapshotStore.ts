/**
 * Small owner-keyed stale-while-revalidate store for expensive renderer reads.
 *
 * Values are replaced atomically only after a successful authoritative read.
 * Failed or aborted refreshes retain the last good snapshot, while superseded
 * requests are cancelled at the fetch boundary instead of merely ignored.
 */
export type KeyedSnapshot<T> = Readonly<{
  value: T;
  updatedAt: number;
}>;

type InFlight<T> = {
  controller: AbortController;
  promise: Promise<T>;
};

export type KeyedSnapshotStore<T> = {
  read: (key: string) => KeyedSnapshot<T> | undefined;
  write: (key: string, value: T) => void;
  update: (key: string, updater: (current: T | undefined) => T) => T;
  ensure: (key: string, loader: (signal: AbortSignal) => Promise<T>) => Promise<T>;
  load: (key: string, loader: (signal: AbortSignal) => Promise<T>, force?: boolean) => Promise<T>;
  cancel: (key: string) => void;
  clear: () => void;
};

export function createKeyedSnapshotStore<T>(options?: {
  maxEntries?: number;
  ttlMs?: number;
  now?: () => number;
}): KeyedSnapshotStore<T> {
  const maxEntries = Math.max(1, options?.maxEntries ?? 16);
  const ttlMs = Math.max(1, options?.ttlMs ?? 10 * 60 * 1000);
  const now = options?.now ?? Date.now;
  const snapshots = new Map<string, KeyedSnapshot<T>>();
  const inFlight = new Map<string, InFlight<T>>();

  const cancel = (key: string) => {
    const request = inFlight.get(key);
    if (!request) return;
    inFlight.delete(key);
    if (!request.controller.signal.aborted) request.controller.abort('snapshot_request_superseded');
  };

  const write = (key: string, value: T) => {
    snapshots.delete(key);
    snapshots.set(key, { value, updatedAt: now() });
    while (snapshots.size > maxEntries) {
      const oldest = snapshots.keys().next().value as string | undefined;
      if (oldest === undefined) break;
      snapshots.delete(oldest);
      cancel(oldest);
    }
  };

  const load = (key: string, loader: (signal: AbortSignal) => Promise<T>, force = false): Promise<T> => {
    const current = inFlight.get(key);
    if (current && !force) return current.promise;
    if (current) cancel(key);

    const controller = new AbortController();
    let tracked: InFlight<T>;
    let loaded: Promise<T>;
    try {
      loaded = loader(controller.signal);
    } catch (error) {
      loaded = Promise.reject(error);
    }
    const promise = loaded
      .then((value) => {
        if (inFlight.get(key) === tracked) write(key, value);
        return value;
      })
      .finally(() => {
        if (inFlight.get(key) === tracked) inFlight.delete(key);
      });
    tracked = { controller, promise };
    inFlight.set(key, tracked);
    return promise;
  };

  return {
    read(key) {
      const snapshot = snapshots.get(key);
      if (!snapshot) return undefined;
      if (now() - snapshot.updatedAt > ttlMs) {
        snapshots.delete(key);
        return undefined;
      }
      snapshots.delete(key);
      snapshots.set(key, snapshot);
      return snapshot;
    },
    write,
    update(key, updater) {
      const next = updater(snapshots.get(key)?.value);
      write(key, next);
      return next;
    },
    ensure(key, loader) {
      const current = inFlight.get(key);
      if (current) return current.promise;
      const snapshot = snapshots.get(key);
      if (snapshot && now() - snapshot.updatedAt <= ttlMs) {
        snapshots.delete(key);
        snapshots.set(key, snapshot);
        return Promise.resolve(snapshot.value);
      }
      if (snapshot) snapshots.delete(key);
      return load(key, loader);
    },
    load,
    cancel,
    clear() {
      for (const key of inFlight.keys()) cancel(key);
      snapshots.clear();
    },
  };
}

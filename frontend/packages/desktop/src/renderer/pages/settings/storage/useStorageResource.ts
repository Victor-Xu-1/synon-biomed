import { useCallback, useEffect, useRef, useState } from 'react';

export type StorageResource<T> = {
  data: T | null;
  loading: boolean;
  failed: boolean;
  refresh: (force?: boolean) => Promise<void>;
  replace: (data: T) => void;
  cancel: () => void;
};

// Each resource owns its request lifetime. Slow scans cannot block metadata,
// overwrite a later result, or publish state after navigation/unmount.
export function useStorageResource<T>(
  load: (signal: AbortSignal, force: boolean) => Promise<T>,
  enabled = true
): StorageResource<T> {
  const [state, setState] = useState<{ data: T | null; loading: boolean; failed: boolean }>({
    data: null,
    loading: false,
    failed: false,
  });
  const generation = useRef(0);
  const request = useRef<AbortController | null>(null);
  const cancel = useCallback(() => {
    generation.current++;
    request.current?.abort();
    request.current = null;
    setState((current) => ({ ...current, loading: false }));
  }, []);
  const refresh = useCallback(
    async (force = true) => {
      const currentGeneration = ++generation.current;
      request.current?.abort();
      const controller = new AbortController();
      request.current = controller;
      setState((current) => ({ ...current, loading: true, failed: false }));
      try {
        const data = await load(controller.signal, force);
        if (generation.current === currentGeneration && !controller.signal.aborted) {
          setState({ data, loading: false, failed: false });
        }
      } catch {
        if (generation.current === currentGeneration && !controller.signal.aborted) {
          setState((current) => ({ ...current, loading: false, failed: true }));
        }
      } finally {
        if (request.current === controller) request.current = null;
      }
    },
    [load]
  );
  const replace = useCallback((data: T) => {
    generation.current++;
    request.current?.abort();
    request.current = null;
    setState({ data, loading: false, failed: false });
  }, []);
  useEffect(() => {
    if (enabled) void refresh(false);
    return () => {
      generation.current++;
      request.current?.abort();
      request.current = null;
    };
  }, [enabled, refresh]);
  return { ...state, refresh, replace, cancel };
}

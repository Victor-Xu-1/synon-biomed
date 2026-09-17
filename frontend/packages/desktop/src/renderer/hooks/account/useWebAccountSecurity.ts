import { useCallback, useEffect, useRef, useState } from 'react';
import {
  loadWebAccountSecurity,
  revokeAllWebAccountSessions,
  revokeOtherWebAccountDevices,
  revokeWebAccountDevice,
  type WebAccountSecurity,
} from '@/renderer/services/account/webAccountSecurity';

type SecurityState = {
  data: WebAccountSecurity | null;
  loading: boolean;
  error: Error | null;
  mutating: boolean;
};

export function useWebAccountSecurity() {
  const [version, setVersion] = useState(0);
  const mutationController = useRef<AbortController | null>(null);
  const [state, setState] = useState<SecurityState>({
    data: null,
    loading: true,
    error: null,
    mutating: false,
  });

  useEffect(() => {
    const controller = new AbortController();
    setState((current) => ({ ...current, loading: true, error: null }));
    void loadWebAccountSecurity(controller.signal)
      .then((data) => {
        if (!controller.signal.aborted) {
          setState((current) => ({ ...current, data, loading: false, error: null }));
        }
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          setState((current) => ({
            ...current,
            loading: false,
            error: error instanceof Error ? error : new Error('Unable to load account security'),
          }));
        }
      });
    return () => controller.abort();
  }, [version]);

  useEffect(
    () => () => {
      mutationController.current?.abort();
    },
    []
  );

  const mutate = useCallback(async (operation: (signal: AbortSignal) => Promise<{ signedOut: boolean }>) => {
    mutationController.current?.abort();
    const controller = new AbortController();
    mutationController.current = controller;
    setState((current) => ({ ...current, mutating: true, error: null }));
    try {
      const result = await operation(controller.signal);
      if (!controller.signal.aborted && !result.signedOut) setVersion((value) => value + 1);
      return result;
    } catch (error) {
      if (!controller.signal.aborted) {
        setState((current) => ({
          ...current,
          mutating: false,
          error: error instanceof Error ? error : new Error('Unable to update account security'),
        }));
      }
      throw error;
    } finally {
      if (!controller.signal.aborted) {
        setState((current) => ({ ...current, mutating: false }));
      }
    }
  }, []);

  return {
    ...state,
    retry: useCallback(() => setVersion((value) => value + 1), []),
    revokeDevice: useCallback(
      (deviceID: string) => mutate((signal) => revokeWebAccountDevice(deviceID, signal)),
      [mutate]
    ),
    revokeOtherDevices: useCallback(() => mutate((signal) => revokeOtherWebAccountDevices(signal)), [mutate]),
    revokeAll: useCallback(() => mutate((signal) => revokeAllWebAccountSessions(signal)), [mutate]),
  };
}

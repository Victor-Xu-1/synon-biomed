import { useCallback, useEffect, useState } from 'react';
import { loadSynonBiomedAccountInsights } from '@/renderer/services/account/synonBiomedAccountInsights';
import type { SynonBiomedAccountInsights } from '@/renderer/services/account/accountInsightsModel';

type AccountInsightsState = {
  data: SynonBiomedAccountInsights | null;
  loading: boolean;
  error: Error | null;
};

export function useSynonBiomedAccountInsights() {
  const [requestVersion, setRequestVersion] = useState(0);
  const [state, setState] = useState<AccountInsightsState>({
    data: null,
    loading: true,
    error: null,
  });

  useEffect(() => {
    const controller = new AbortController();
    setState((current) => ({ ...current, loading: true, error: null }));

    void loadSynonBiomedAccountInsights({ signal: controller.signal })
      .then((data) => {
        if (!controller.signal.aborted) setState({ data, loading: false, error: null });
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        setState((current) => ({
          data: current.data,
          loading: false,
          error: error instanceof Error ? error : new Error('Unable to load account insights'),
        }));
      });

    return () => controller.abort();
  }, [requestVersion]);

  const retry = useCallback(() => setRequestVersion((version) => version + 1), []);
  return { ...state, retry };
}

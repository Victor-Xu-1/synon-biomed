/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { useEffect, useState, type Dispatch, type SetStateAction } from 'react';
import {
  createSynonBiomedSessionDefaults,
  loadSynonBiomedSessionOptions,
  type SynonBiomedSessionOptions,
} from '@/renderer/services/synonBiomedSessionOptions';

const SESSION_OPTIONS_RETRY_DELAYS_MS = [1_000, 2_000, 5_000, 10_000, 30_000] as const;

export type SynonBiomedSessionOptionsLoadState = {
  conversationId: string;
  status: 'loading' | 'ready' | 'retrying';
};

type SynonBiomedSessionOptionsState = {
  options: SynonBiomedSessionOptions;
  setOptions: Dispatch<SetStateAction<SynonBiomedSessionOptions>>;
  loadState: SynonBiomedSessionOptionsLoadState;
};

/**
 * Session options are an optional control-plane projection. They retry for as
 * long as the route remains mounted, but never own the core composer gate.
 */
export function useSynonBiomedSessionOptionsState(
  conversationId: string,
  agentName?: string
): SynonBiomedSessionOptionsState {
  const [options, setOptions] = useState<SynonBiomedSessionOptions>(() => createSynonBiomedSessionDefaults(agentName));
  const [loadState, setLoadState] = useState<SynonBiomedSessionOptionsLoadState>({
    conversationId,
    status: 'loading',
  });

  useEffect(() => {
    let active = true;
    let retryTimer: ReturnType<typeof setTimeout> | null = null;
    let attempt = 0;

    setOptions(createSynonBiomedSessionDefaults(agentName));
    setLoadState({ conversationId, status: 'loading' });

    const load = () => {
      void loadSynonBiomedSessionOptions(conversationId)
        .then((loaded) => {
          if (!active) return;
          setOptions(loaded);
          setLoadState({ conversationId, status: 'ready' });
        })
        .catch(() => {
          if (!active) return;
          const delay = SESSION_OPTIONS_RETRY_DELAYS_MS[Math.min(attempt, SESSION_OPTIONS_RETRY_DELAYS_MS.length - 1)];
          attempt += 1;
          setLoadState({ conversationId, status: 'retrying' });
          retryTimer = setTimeout(() => {
            retryTimer = null;
            if (!active) return;
            load();
          }, delay);
        });
    };

    load();
    return () => {
      active = false;
      if (retryTimer !== null) clearTimeout(retryTimer);
    };
  }, [agentName, conversationId]);

  return { options, setOptions, loadState };
}

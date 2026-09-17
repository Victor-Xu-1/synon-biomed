/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { Assistant } from '@/common/types/agent/assistantTypes';
import { loadSynonBiomedAssistants, SYNON_BIOMED_ASSISTANTS_CACHE_KEY } from '@/renderer/services/synonBiomedCatalog';
import { useCallback, useState } from 'react';
import useSWR, { useSWRConfig } from 'swr';

const AUTOMATIC_CATALOG_RETRY_LIMIT = 5;
const AUTOMATIC_CATALOG_RETRY_DELAY_MS = 1_000;
const MAX_INITIAL_CATALOG_REQUEST_ATTEMPTS = AUTOMATIC_CATALOG_RETRY_LIMIT + 1;

type CatalogRecoveryCoordinator = {
  initialRequestAttempts: number;
  hasLoadedSuccessfully: boolean;
  diagnosticLogged: boolean;
  retryTimer: number | undefined;
};

// SWR caches are provider-scoped. Sharing recovery state by cache keeps every
// React instance on one lifecycle while allowing independent windows and test
// providers to recover without affecting one another.
const catalogRecoveryCoordinators = new WeakMap<object, CatalogRecoveryCoordinator>();

const getCatalogRecoveryCoordinator = (cache: object): CatalogRecoveryCoordinator => {
  const existing = catalogRecoveryCoordinators.get(cache);
  if (existing) return existing;
  const coordinator: CatalogRecoveryCoordinator = {
    initialRequestAttempts: 0,
    hasLoadedSuccessfully: false,
    diagnosticLogged: false,
    retryTimer: undefined,
  };
  catalogRecoveryCoordinators.set(cache, coordinator);
  return coordinator;
};

export type SynonBiomedAssistantCatalogStatus = 'loading' | 'ready' | 'error';

type UseSynonBiomedAssistantsLoaderResult = {
  /**
   * User-visible Synon Biomed profile catalog returned by the backend.
   * Guid, conversation, cron, and settings share this cache and ordering.
   */
  assistants: Assistant[];
  catalogStatus: SynonBiomedAssistantCatalogStatus;
  hasUsableCatalog: boolean;
  retry: () => Promise<void>;
};

/**
 * Loads the same user-visible profile catalog consumed by settings. Internal
 * technical agents and generic Agent onboarding sources never enter this cache.
 */
export const useSynonBiomedAssistantsLoader = (): UseSynonBiomedAssistantsLoaderResult => {
  const { cache } = useSWRConfig();
  const recovery = getCatalogRecoveryCoordinator(cache);
  const [catalogRecoveryExhausted, setCatalogRecoveryExhausted] = useState(
    () => recovery.initialRequestAttempts >= MAX_INITIAL_CATALOG_REQUEST_ATTEMPTS
  );
  // Synon Biomed assistants share their own cache so settings / guid / conversation
  // all see the same list without duplicate HTTP calls.
  const {
    data: assistantList,
    isValidating,
    mutate,
  } = useSWR(
    SYNON_BIOMED_ASSISTANTS_CACHE_KEY,
    async () => {
      if (!recovery.hasLoadedSuccessfully) {
        if (recovery.initialRequestAttempts >= MAX_INITIAL_CATALOG_REQUEST_ATTEMPTS) {
          setCatalogRecoveryExhausted(true);
          throw new Error('Synon Biomed assistant catalog automatic recovery is exhausted');
        }
        recovery.initialRequestAttempts += 1;
      }
      try {
        return await loadSynonBiomedAssistants();
      } catch (catalogError) {
        // Count the actual failed request here. SWR may discard an in-flight
        // result during React development remounts, in which case lifecycle
        // callbacks alone do not see every network attempt.
        if (
          !recovery.hasLoadedSuccessfully &&
          recovery.initialRequestAttempts >= MAX_INITIAL_CATALOG_REQUEST_ATTEMPTS
        ) {
          setCatalogRecoveryExhausted(true);
        }
        throw catalogError;
      }
    },
    {
      shouldRetryOnError: true,
      keepPreviousData: true,
      isPaused: () =>
        !recovery.hasLoadedSuccessfully && recovery.initialRequestAttempts >= MAX_INITIAL_CATALOG_REQUEST_ATTEMPTS,
      // Browser focus/reconnect events can fire repeatedly while the initial
      // request is already in recovery. Enable background refresh only after a
      // usable catalog has loaded so those events cannot bypass the retry budget.
      revalidateOnFocus: recovery.hasLoadedSuccessfully,
      revalidateOnReconnect: recovery.hasLoadedSuccessfully,
      onSuccess: () => {
        if (recovery.retryTimer !== undefined) {
          window.clearTimeout(recovery.retryTimer);
          recovery.retryTimer = undefined;
        }
        recovery.initialRequestAttempts = 0;
        recovery.hasLoadedSuccessfully = true;
        recovery.diagnosticLogged = false;
        setCatalogRecoveryExhausted(false);
      },
      onError: () => {
        if (!recovery.diagnosticLogged) {
          recovery.diagnosticLogged = true;
          console.error('[SynonBiomedAssistants]', { code: 'ASSISTANT_CATALOG_LOAD_FAILED' });
        }
        if (recovery.hasLoadedSuccessfully) return;
        if (recovery.initialRequestAttempts >= MAX_INITIAL_CATALOG_REQUEST_ATTEMPTS) {
          setCatalogRecoveryExhausted(true);
        }
      },
      onErrorRetry: (_catalogError, _cacheKey, _config, revalidate, { retryCount }) => {
        if (recovery.hasLoadedSuccessfully) return;
        if (recovery.initialRequestAttempts >= MAX_INITIAL_CATALOG_REQUEST_ATTEMPTS) {
          setCatalogRecoveryExhausted(true);
          return;
        }
        if (recovery.retryTimer !== undefined) return;
        recovery.retryTimer = window.setTimeout(() => {
          recovery.retryTimer = undefined;
          if (recovery.initialRequestAttempts >= MAX_INITIAL_CATALOG_REQUEST_ATTEMPTS) return;
          void revalidate({ retryCount });
        }, AUTOMATIC_CATALOG_RETRY_DELAY_MS);
      },
    }
  );
  const assistants = assistantList ?? [];

  const retry = useCallback(async () => {
    if (recovery.retryTimer !== undefined) {
      window.clearTimeout(recovery.retryTimer);
      recovery.retryTimer = undefined;
    }
    recovery.initialRequestAttempts = 0;
    if (assistantList === undefined) recovery.hasLoadedSuccessfully = false;
    setCatalogRecoveryExhausted(false);
    await mutate(undefined, { populateCache: false, revalidate: true, throwOnError: false });
  }, [assistantList, mutate, recovery]);

  const catalogUnavailable = assistantList === undefined && !isValidating && catalogRecoveryExhausted;

  return {
    assistants,
    catalogStatus: assistantList !== undefined ? 'ready' : catalogUnavailable ? 'error' : 'loading',
    hasUsableCatalog: assistantList !== undefined,
    retry,
  };
};

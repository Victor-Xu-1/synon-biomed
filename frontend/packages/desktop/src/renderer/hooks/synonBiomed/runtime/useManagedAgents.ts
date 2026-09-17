/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { ManagedAgent } from '@/renderer/utils/synonBiomed/runtime/runtimeTypes';
import { MANAGED_AGENTS_SWR_KEY, fetchManagedAgents } from '@/renderer/utils/synonBiomed/runtime/runtimeTypes';
import { SYNON_BIOMED_ASSISTANTS_CACHE_KEY } from '@/renderer/services/synonBiomedCatalog';
import useSWR, { useSWRConfig } from 'swr';

export type UseManagedAgentsResult = {
  agents: ManagedAgent[];
  isLoading: boolean;
  isRefreshing: boolean;
  error: unknown;
  revalidate: () => Promise<ManagedAgent[] | undefined>;
  refreshCatalog: () => Promise<ManagedAgent[] | undefined>;
};

const isSynonBiomedManagedAgent = (agent: ManagedAgent): boolean =>
  agent.agent_type === 'synonbiomed' || agent.backend === 'synonbiomed';

const filterSynonBiomedManagedAgents = (agents?: ManagedAgent[]): ManagedAgent[] =>
  (agents ?? []).filter(isSynonBiomedManagedAgent);

type ScopedMutate = ReturnType<typeof useSWRConfig>['mutate'];

export async function refreshManagedAgentCatalogAndAssistants(
  mutate: ScopedMutate
): Promise<ManagedAgent[] | undefined> {
  const [agents] = await Promise.all([
    mutate<ManagedAgent[]>(MANAGED_AGENTS_SWR_KEY),
    mutate(SYNON_BIOMED_ASSISTANTS_CACHE_KEY),
  ]);
  return agents;
}

/**
 * Hook for Synon Biomed-only diagnostics surfaces. Reads the dedicated
 * `/api/synonbiomed/experts` diagnostics view (`MANAGED_AGENTS_SWR_KEY`) so
 * unavailable Synon Biomed experts stay visible to assistant and runtime views.
 *
 * `revalidate` refreshes only the Synon Biomed management key. It is the right
 * action for diagnostics-only changes that should not invalidate assistant
 * selection.
 *
 * `refreshCatalog` refreshes the management catalog plus assistant list caches
 * after structural or status changes that can affect Synon Biomed assistants.
 * Business assistant pickers must not depend on this hook.
 *
 * Do not use this to reintroduce generic agent connection or marketplace UI.
 */
export const useManagedAgents = (): UseManagedAgentsResult => {
  const { mutate } = useSWRConfig();
  const { data, isLoading, isValidating, error } = useSWR<ManagedAgent[]>(MANAGED_AGENTS_SWR_KEY, fetchManagedAgents);

  const revalidateManaged = () => mutate<ManagedAgent[]>(MANAGED_AGENTS_SWR_KEY);

  return {
    agents: filterSynonBiomedManagedAgents(data),
    isLoading,
    isRefreshing: isValidating && !isLoading,
    error,
    revalidate: revalidateManaged,
    refreshCatalog: () => refreshManagedAgentCatalogAndAssistants(mutate),
  };
};

/**
 * Lightweight runtime catalog read model for assistant-bound Synon Biomed rows.
 * Uses the same `/api/synonbiomed/experts` payload as the diagnostics hook.
 */
export const useManagedAgentRuntimeCatalog = (): ManagedAgent[] => {
  const { data } = useSWR<ManagedAgent[]>(MANAGED_AGENTS_SWR_KEY, fetchManagedAgents);
  return filterSynonBiomedManagedAgents(data);
};

/**
 * Non-hook entry point for settings/tooling surfaces that need the Synon Biomed
 * diagnostics catalog without mutating a renderer identity cache from outside
 * its React scope.
 */
export async function getManagedAgents(): Promise<ManagedAgent[]> {
  return filterSynonBiomedManagedAgents(await fetchManagedAgents());
}

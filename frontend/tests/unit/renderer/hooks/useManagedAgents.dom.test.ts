/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 *
 * Unit tests for renderer/hooks/agent/useManagedAgents.ts.
 *
 * The Agent settings management surface must read the Synon Biomed management
 * view. Diagnostics-only actions can refresh the management cache only;
 * catalog-changing or health actions that affect generated assistants must also
 * invalidate assistant list caches.
 */

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { renderHook, act } from '@testing-library/react';

vi.mock('swr', () => {
  const mutate = vi.fn().mockResolvedValue(undefined);
  return {
    default: vi.fn(() => ({ data: [], error: null, isLoading: false })),
    mutate,
    useSWRConfig: () => ({ mutate }),
  };
});

vi.mock('@/common', () => ({
  ipcBridge: {
    acpConversation: {},
  },
}));

vi.mock('@/renderer/utils/synonBiomed/runtime/runtimeTypes', () => ({
  MANAGED_AGENTS_SWR_KEY: 'agents.managed',
  fetchManagedAgents: vi.fn(),
}));

vi.mock('@/renderer/services/synonBiomedCatalog', () => ({
  SYNON_BIOMED_ASSISTANTS_CACHE_KEY: 'synonbiomed.assistants.catalog.v1',
}));

import { getManagedAgents, useManagedAgents } from '@/renderer/hooks/synonBiomed/runtime/useManagedAgents';
import useSWR, { mutate } from 'swr';
import { fetchManagedAgents } from '@/renderer/utils/synonBiomed/runtime/runtimeTypes';

describe('useManagedAgents', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('subscribes to the management SWR key with the managed fetcher', () => {
    (useSWR as any).mockReturnValue({ data: [], error: null, isLoading: false });

    renderHook(() => useManagedAgents());

    expect(useSWR).toHaveBeenCalledWith('agents.managed', fetchManagedAgents);
  });

  it('exposes only Synon Biomed agents returned by SWR', () => {
    const agents = [
      {
        id: 'AIDD_EXPERT',
        name: 'AIDD Expert',
        agent_type: 'synonbiomed',
        agent_source: 'builtin',
        backend: 'synonbiomed',
        enabled: true,
        available: true,
      },
      {
        id: 'claude',
        name: 'Claude Code',
        agent_type: 'acp',
        agent_source: 'builtin',
        backend: 'claude',
      },
      {
        id: 'custom',
        name: 'Custom Agent',
        agent_type: 'acp',
        agent_source: 'custom',
        backend: 'custom',
      },
    ];
    (useSWR as any).mockReturnValue({ data: agents, error: null, isLoading: false });

    const { result } = renderHook(() => useManagedAgents());

    expect(result.current.agents).toEqual([agents[0]]);
  });

  it('falls back to an empty list when SWR has no data yet', () => {
    (useSWR as any).mockReturnValue({ data: undefined, error: null, isLoading: true });

    const { result } = renderHook(() => useManagedAgents());

    expect(result.current.agents).toEqual([]);
  });

  it('revalidate refreshes only the management key', async () => {
    (useSWR as any).mockReturnValue({ data: [], error: null, isLoading: false });

    const { result } = renderHook(() => useManagedAgents());

    await act(async () => {
      await result.current.revalidate();
    });

    expect(mutate).toHaveBeenCalledWith('agents.managed');
    expect(mutate).not.toHaveBeenCalledWith('agents.legacyDetected');
  });

  it('refreshCatalog refreshes only the managed and dedicated Synon expert catalogs', async () => {
    (useSWR as any).mockReturnValue({ data: [], error: null, isLoading: false });

    const { result } = renderHook(() => useManagedAgents());

    await act(async () => {
      await result.current.refreshCatalog();
    });

    expect(mutate).toHaveBeenCalledWith('agents.managed');
    expect(mutate).toHaveBeenCalledWith('synonbiomed.assistants.catalog.v1');
    expect(mutate).not.toHaveBeenCalledWith('assistants.list');
    expect(mutate).not.toHaveBeenCalledWith('assistants');
  });

  it('does not expose the old custom-agent backend rescan entry point', () => {
    (useSWR as any).mockReturnValue({ data: [], error: null, isLoading: false });

    const { result } = renderHook(() => useManagedAgents());

    expect(result.current).not.toHaveProperty('refreshCustomAgents');
  });

  it('getManagedAgents fetches only the Synon Biomed management catalog without invalidating legacy Agent caches', async () => {
    const managedAgents = [
      {
        id: 'ONCOLOGY_EXPERT',
        name: 'Oncology Expert',
        agent_type: 'synonbiomed',
        agent_source: 'builtin',
        backend: 'synonbiomed',
        enabled: true,
      },
      {
        id: 'managed-1',
        name: 'Managed Agent',
        agent_type: 'acp',
        agent_source: 'builtin',
        enabled: true,
      },
    ];
    (fetchManagedAgents as any).mockResolvedValue(managedAgents);

    const result = await getManagedAgents();

    expect(fetchManagedAgents).toHaveBeenCalledTimes(1);
    expect(mutate).not.toHaveBeenCalled();
    expect(mutate).not.toHaveBeenCalledWith('agents.legacyDetected');
    expect(result).toEqual([managedAgents[0]]);
  });
});

/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, renderHook, waitFor } from '@testing-library/react';
import { createElement, type PropsWithChildren } from 'react';
import { SWRConfig } from 'swr';

vi.mock('@/common', () => ({
  ipcBridge: {
    assistants: {
      list: { invoke: vi.fn(), provider: vi.fn() },
    },
  },
}));

import { ipcBridge } from '@/common';
import { useSynonBiomedAssistantsLoader } from '@/renderer/pages/guid/hooks/useSynonBiomedAssistantsLoader';
import { useConversationAssistants } from '@/renderer/pages/conversation/hooks/useConversationAssistants';

const expertCatalogResponse = (name = 'ONCOLOGY_EXPERT', displayName = 'Oncology Expert') =>
  new Response(
    JSON.stringify([
      {
        name,
        displayName,
        description: `${displayName} description`,
        healthy: true,
        enabled: true,
        source: 'bundled',
        skillsLocked: false,
        unrestricted: false,
        supportsPlanMode: true,
        userHidden: false,
        skillNames: [],
      },
    ]),
    { status: 200 }
  );

const renderLoader = (
  provider = new Map(),
  config: { dedupingInterval?: number; focusThrottleInterval?: number; revalidateOnMount?: boolean } = {}
) => {
  const Wrapper = ({ children }: PropsWithChildren) =>
    createElement(
      SWRConfig,
      {
        value: {
          provider: () => provider,
          dedupingInterval: config.dedupingInterval ?? 0,
          focusThrottleInterval: config.focusThrottleInterval,
          revalidateOnMount: config.revalidateOnMount,
        },
      },
      children
    );
  return renderHook(() => useSynonBiomedAssistantsLoader(), { wrapper: Wrapper });
};

const legacyCatalog = [
  {
    id: 'synonbiomed:legacy-aidd',
    name: 'Legacy AIDD',
    enabled: true,
    source: 'builtin',
    agent: { type: 'synonbiomed', source: 'builtin', acp_backend: 'legacy-ipc' },
  },
];

const renderSharedCatalogs = () => {
  const provider = new Map();
  const Wrapper = ({ children }: PropsWithChildren) =>
    createElement(SWRConfig, { value: { provider: () => provider, dedupingInterval: 0 } }, children);
  return renderHook(
    () => ({
      experts: useSynonBiomedAssistantsLoader(),
      legacy: useConversationAssistants(),
    }),
    { wrapper: Wrapper }
  );
};

describe('useSynonBiomedAssistantsLoader integration', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('uses Synon Biomed expert assistants as the only Guid agent catalog without reading old agents', async () => {
    const fetchImpl = vi.fn().mockImplementation(async () => expertCatalogResponse());
    vi.stubGlobal('fetch', fetchImpl);

    const { result } = renderLoader();

    await waitFor(() => expect(result.current.assistants).toHaveLength(1));

    expect(fetchImpl).toHaveBeenCalledWith(
      '/api/synonbiomed/expert-profiles',
      expect.objectContaining({ headers: { Accept: 'application/json' } })
    );
    expect(ipcBridge.assistants.list.invoke).not.toHaveBeenCalled();
    expect(result.current.assistants.map((assistant) => assistant.id)).toEqual(['synonbiomed:oncology-expert']);
    expect(result.current.catalogStatus).toBe('ready');
  });

  it('exposes a recoverable initial failure from the real catalog fetcher without pretending the catalog is empty', async () => {
    const privateFailure = `catalog failed for alice@example.com at /home/alice/private with ${[
      'sk',
      'private-value',
    ].join('-')} ${'payload '.repeat(1_000)}`;
    const fetchImpl = vi.fn().mockRejectedValue(new Error(privateFailure));
    vi.stubGlobal('fetch', fetchImpl);
    const diagnostic = vi.spyOn(console, 'error').mockImplementation(() => undefined);

    try {
      const { result } = renderLoader();
      await waitFor(() => expect(result.current.catalogStatus).toBe('error'), { timeout: 8_000 });

      expect(result.current.assistants).toEqual([]);
      expect(result.current.hasUsableCatalog).toBe(false);
      expect(fetchImpl).toHaveBeenCalledTimes(6);
      expect(result.current.retry).toBeTypeOf('function');
      fetchImpl.mockImplementation(async () => expertCatalogResponse('OPERON', 'General Research Assistant'));
      await act(async () => {
        await expect(result.current.retry()).resolves.toBeUndefined();
      });
      await waitFor(() => expect(result.current.catalogStatus).toBe('ready'));
      expect(result.current.assistants.map((item) => item.id)).toEqual(['synonbiomed:operon']);
      const output = JSON.stringify(diagnostic.mock.calls);
      expect(output).toContain('ASSISTANT_CATALOG_LOAD_FAILED');
      expect(output).not.toMatch(/alice@example\.com|\/home\/alice|private-value|payload/);
      expect(output.length).toBeLessThan(300);
    } finally {
      diagnostic.mockRestore();
    }
  });

  it('recovers a transient initial catalog failure without requiring a user click', async () => {
    const fetchImpl = vi
      .fn()
      .mockRejectedValueOnce(new Error('backend is restarting'))
      .mockImplementationOnce(async () => expertCatalogResponse('OPERON', 'General Research Assistant'));
    vi.stubGlobal('fetch', fetchImpl);
    const diagnostic = vi.spyOn(console, 'error').mockImplementation(() => undefined);

    try {
      const { result } = renderLoader();
      await waitFor(() => expect(result.current.catalogStatus).toBe('ready'), { timeout: 5_000 });
      expect(result.current.assistants.map((item) => item.id)).toEqual(['synonbiomed:operon']);
      expect(fetchImpl).toHaveBeenCalledTimes(2);
    } finally {
      diagnostic.mockRestore();
    }
  });

  it('shares successful recovery across a remount and keeps focus revalidation enabled', async () => {
    const fetchImpl = vi
      .fn()
      .mockImplementationOnce(async () => expertCatalogResponse('OPERON', 'General Research Assistant'))
      .mockImplementation(async () => expertCatalogResponse('ONCOLOGY_EXPERT', 'Oncology Expert'));
    vi.stubGlobal('fetch', fetchImpl);
    const provider = new Map();

    const firstMount = renderLoader(provider, { dedupingInterval: 2_000, focusThrottleInterval: 0 });
    await waitFor(() => expect(firstMount.result.current.catalogStatus).toBe('ready'));
    expect(firstMount.result.current.assistants.map((item) => item.id)).toEqual(['synonbiomed:operon']);
    firstMount.unmount();
    const callsBeforeRemount = fetchImpl.mock.calls.length;

    const secondMount = renderLoader(provider, {
      dedupingInterval: 2_000,
      focusThrottleInterval: 0,
      revalidateOnMount: false,
    });
    await waitFor(() => expect(secondMount.result.current.catalogStatus).toBe('ready'));
    expect(secondMount.result.current.assistants.map((item) => item.id)).toEqual(['synonbiomed:operon']);
    expect(fetchImpl).toHaveBeenCalledTimes(callsBeforeRemount);

    await act(async () => {
      window.dispatchEvent(new Event('focus'));
    });
    await waitFor(() => expect(fetchImpl.mock.calls.length).toBeGreaterThan(callsBeforeRemount), { timeout: 2_000 });
    await waitFor(() =>
      expect(secondMount.result.current.assistants.map((item) => item.id)).toEqual(['synonbiomed:oncology-expert'])
    );
  });

  it('retains an existing assistant identity when the real catalog fetcher fails during revalidation', async () => {
    const fetchImpl = vi
      .fn()
      .mockImplementationOnce(async () => expertCatalogResponse('OPERON', 'General Research Assistant'))
      .mockRejectedValueOnce(new Error('catalog refresh failed for /home/alice with token=private-value'))
      .mockImplementationOnce(async () => expertCatalogResponse('ONCOLOGY_EXPERT', 'Oncology Expert'));
    vi.stubGlobal('fetch', fetchImpl);
    const diagnostic = vi.spyOn(console, 'error').mockImplementation(() => undefined);
    const { result } = renderLoader();
    await waitFor(() => expect(result.current.assistants.map((item) => item.id)).toEqual(['synonbiomed:operon']));

    await act(async () => {
      await result.current.retry();
    });

    expect(result.current.assistants.map((item) => item.id)).toEqual(['synonbiomed:operon']);
    expect(result.current.catalogStatus).toBe('ready');
    expect(result.current.hasUsableCatalog).toBe(true);
    expect(JSON.stringify(diagnostic.mock.calls)).toContain('ASSISTANT_CATALOG_LOAD_FAILED');
    expect(JSON.stringify(diagnostic.mock.calls)).not.toMatch(/\/home\/alice|private-value/);

    await act(async () => {
      await result.current.retry();
    });
    await waitFor(() => expect(result.current.catalogStatus).toBe('ready'));
    expect(result.current.assistants.map((item) => item.id)).toEqual(['synonbiomed:oncology-expert']);
    diagnostic.mockRestore();
  });

  it('keeps expert and legacy catalogs isolated in one shared SWR provider through revalidation failure', async () => {
    const fetchImpl = vi
      .fn()
      .mockImplementationOnce(async () => expertCatalogResponse('OPERON', 'General Research Assistant'))
      .mockRejectedValueOnce(new Error('expert refresh unavailable'));
    vi.stubGlobal('fetch', fetchImpl);
    vi.mocked(ipcBridge.assistants.list.invoke).mockResolvedValue(legacyCatalog as never);
    const diagnostic = vi.spyOn(console, 'error').mockImplementation(() => undefined);
    try {
      const { result } = renderSharedCatalogs();
      await waitFor(() => {
        expect(result.current.experts.assistants.map((item) => item.id)).toEqual(['synonbiomed:operon']);
        expect(result.current.legacy.presetAssistants.map((item) => item.id)).toEqual(['synonbiomed:legacy-aidd']);
      });

      await act(async () => {
        await result.current.experts.retry();
        await result.current.legacy.refresh();
      });

      expect(result.current.experts.assistants.map((item) => item.id)).toEqual(['synonbiomed:operon']);
      expect(result.current.experts.catalogStatus).toBe('ready');
      expect(result.current.legacy.presetAssistants.map((item) => item.id)).toEqual(['synonbiomed:legacy-aidd']);
    } finally {
      diagnostic.mockRestore();
    }
  });
});

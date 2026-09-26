/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import React from 'react';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import SynonBiomedExpertsSettings from '@/renderer/pages/settings/SynonBiomedExpertsSettings';
import { renderWithI18n } from '../i18nTestUtils';

const mocks = vi.hoisted(() => ({
  loadProfiles: vi.fn(),
  loadSkills: vi.fn(),
  loadServers: vi.fn(),
  loadExpertUsage: vi.fn(),
  setEnabled: vi.fn(),
}));

vi.mock('@arco-design/web-react', async () => {
  const actual = await vi.importActual<typeof import('@arco-design/web-react')>('@arco-design/web-react');
  return {
    ...actual,
    Message: {
      ...actual.Message,
      useMessage: () => [{ success: vi.fn(), error: vi.fn(), warning: vi.fn() }, <div key='messages' />],
    },
  };
});

vi.mock('@/renderer/services/agents/synonBiomedExpertProfiles', () => ({
  loadSynonBiomedExpertProfiles: mocks.loadProfiles,
  loadSynonBiomedExpertProfilesWithRuntimeConnectors: mocks.loadProfiles,
  loadSynonBiomedExpertInstructions: vi.fn(async () => ''),
  normalizeSynonBiomedExpertName: (value: string) => value.trim().toUpperCase(),
  createSynonBiomedExpertProfile: vi.fn(),
  deleteSynonBiomedExpertProfile: vi.fn(),
  saveSynonBiomedExpertInstructions: vi.fn(),
  setSynonBiomedExpertProfileEnabled: mocks.setEnabled,
  updateSynonBiomedExpertProfile: vi.fn(),
  updateSynonBiomedExpertSkills: vi.fn(),
  attachSynonBiomedExpertConnector: vi.fn(),
  detachSynonBiomedExpertConnector: vi.fn(),
}));

vi.mock('@/renderer/services/synonBiomedCapabilities', () => ({
  loadSynonBiomedSkills: mocks.loadSkills,
  loadSynonBiomedMcpServers: mocks.loadServers,
}));

vi.mock('@/renderer/services/agents/synonBiomedExpertUsage', () => ({
  loadSynonBiomedExpertUsage: mocks.loadExpertUsage,
  findSynonBiomedExpertUsage: (usageByName: Record<string, unknown>, name: string) => usageByName[name] ?? null,
}));

vi.mock('@/renderer/pages/settings/components/SettingsPageWrapper', () => ({
  default: ({ children }: { children: React.ReactNode }) => <div data-testid='settings-wrapper'>{children}</div>,
}));

describe('SynonBiomedExpertsSettings', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    window.history.replaceState(null, '', '/');
    Object.defineProperty(window, 'matchMedia', {
      configurable: true,
      value: vi.fn().mockImplementation((query: string) => ({
        matches: false,
        media: query,
        onchange: null,
        addListener: vi.fn(),
        removeListener: vi.fn(),
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        dispatchEvent: vi.fn(),
      })),
    });
    mocks.loadProfiles.mockResolvedValue([
      {
        name: 'AIDD_EXPERT',
        displayName: 'AI药物研发专家',
        description: '药物发现与设计',
        source: 'builtin',
        enabled: true,
        skillNames: ['chembl'],
        userHidden: false,
      },
    ]);
    mocks.loadSkills.mockResolvedValue([
      { name: 'chembl', displayName: 'ChEMBL', enabled: true },
      { name: 'citation-management', displayName: 'Citation management', enabled: true },
    ]);
    mocks.loadServers.mockResolvedValue([
      {
        id: 'bundled:chembl',
        name: 'chembl',
        displayName: 'ChEMBL',
        enabled: true,
        attachedAgents: ['AIDD_EXPERT'],
      },
    ]);
    mocks.loadExpertUsage.mockResolvedValue({
      AIDD_EXPERT: {
        invocationCount: 3,
        lastUsedAt: '2026-08-15T10:20:00.000Z',
      },
    });
    mocks.setEnabled.mockReset();
  });

  it('renders the shared settings header and the real expert catalog', async () => {
    await renderWithI18n(<SynonBiomedExpertsSettings />, 'zh-CN');

    expect(screen.getByTestId('settings-wrapper')).toBeInTheDocument();
    expect(screen.getByTestId('experts-header')).toHaveTextContent('专家');
    await waitFor(() => expect(screen.getByText('AI 药物研发专家')).toBeInTheDocument());
    expect(screen.getByText(/AI 驱动的药物发现与设计专家/)).toBeInTheDocument();
    expect(screen.getByTestId('expert-usage-count-AIDD_EXPERT')).toHaveTextContent('使用 3 次');
    expect(screen.getByTestId('expert-last-used-AIDD_EXPERT')).toHaveTextContent('最近使用');
    const expertCard = screen.getByTestId('expert-card-AIDD_EXPERT');
    expect(expertCard).toHaveClass('expert-card');
    expect(expertCard.parentElement).toHaveClass('expert-grid');
    expect(expertCard.querySelector('[data-settings-generated-asset^="artwork:"]')).toBeInTheDocument();
    expect(mocks.loadProfiles).toHaveBeenCalledOnce();
  });

  it('localizes bundled expert metadata for the English interface', async () => {
    await renderWithI18n(<SynonBiomedExpertsSettings />, 'en-US');

    await waitFor(() => expect(screen.getByText('AI Drug Discovery Expert')).toBeInTheDocument());
    expect(screen.getByText(/generative models, virtual screening/)).toBeInTheDocument();
  });

  it('renders the expert list before optional catalog enrichment completes', async () => {
    let releaseSkills!: (value: unknown[]) => void;
    let releaseServers!: (value: unknown[]) => void;
    let releaseUsage!: (value: Record<string, unknown>) => void;
    mocks.loadSkills.mockReturnValue(
      new Promise<unknown[]>((resolve) => {
        releaseSkills = resolve;
      })
    );
    mocks.loadServers.mockReturnValue(
      new Promise<unknown[]>((resolve) => {
        releaseServers = resolve;
      })
    );
    mocks.loadExpertUsage.mockReturnValue(
      new Promise<Record<string, unknown>>((resolve) => {
        releaseUsage = resolve;
      })
    );

    await renderWithI18n(<SynonBiomedExpertsSettings />, 'zh-CN');

    expect(await screen.findByText('AI 药物研发专家')).toBeInTheDocument();
    expect(screen.getByTestId('expert-list-page')).toBeInTheDocument();

    releaseSkills([]);
    releaseServers([]);
    releaseUsage({});
  });

  it('opens expert details in a modal without replacing the expert list', async () => {
    await renderWithI18n(<SynonBiomedExpertsSettings />, 'zh-CN');

    const expert = await screen.findByText('AI 药物研发专家');
    fireEvent.click(expert);

    await waitFor(() => expect(screen.getByTestId('expert-detail-modal')).toBeInTheDocument());
    expect(screen.getByTestId('expert-list-page')).toBeInTheDocument();
    expect(within(screen.getByTestId('expert-list-page')).getByText('AI 药物研发专家')).toBeInTheDocument();
    expect(screen.getByTestId('expert-skill-coverage')).toHaveTextContent('Skills 1/2');
    expect(screen.getByTestId('expert-connector-coverage')).toHaveTextContent('MCP 1/1');

    fireEvent.click(screen.getByRole('button', { name: '取消' }));
    await waitFor(() => expect(screen.queryByTestId('expert-detail-modal')).not.toBeInTheDocument());
  });

  it('opens and clears the create-expert deep link without requiring a router wrapper', async () => {
    window.history.replaceState(null, '', '/#/settings/experts?create=1');
    await renderWithI18n(<SynonBiomedExpertsSettings />, 'zh-CN');

    expect(await screen.findByRole('dialog', { name: '添加专家' })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '取消' }));
    await waitFor(() => expect(screen.queryByRole('dialog', { name: '添加专家' })).not.toBeInTheDocument());
    expect(window.location.hash).toBe('#/settings/experts');
  });

  it('serializes expert enable changes while a backend update is pending', async () => {
    const profile = {
      name: 'MY_EXPERT',
      displayName: '我的专家',
      description: '用于回归测试',
      source: 'user',
      enabled: false,
      skillNames: [],
      userHidden: false,
    };
    mocks.loadProfiles.mockResolvedValue([profile]);
    let resolveUpdate!: (value: typeof profile) => void;
    mocks.setEnabled.mockReturnValue(
      new Promise<typeof profile>((resolve) => {
        resolveUpdate = resolve;
      })
    );

    await renderWithI18n(<SynonBiomedExpertsSettings />, 'zh-CN');
    const toggle = await screen.findByRole('switch', { name: '启用 我的专家' });
    fireEvent.click(toggle);

    await waitFor(() => expect(toggle).toBeDisabled());
    fireEvent.click(toggle);
    expect(mocks.setEnabled).toHaveBeenCalledTimes(1);
    expect(mocks.setEnabled).toHaveBeenCalledWith('MY_EXPERT', true);

    resolveUpdate({ ...profile, enabled: true });
    await waitFor(() => expect(toggle).toBeChecked());
    expect(toggle).toBeEnabled();
  });
});

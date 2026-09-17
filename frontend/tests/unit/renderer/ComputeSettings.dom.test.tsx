import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import React from 'react';
import { describe, expect, it, vi } from 'vitest';

import { renderWithI18n } from '../i18nTestUtils';

const computeMocks = vi.hoisted(() => ({
  loadProviders: vi.fn().mockResolvedValue([]),
  loadAliases: vi.fn().mockResolvedValue({
    aliases: [],
    configFound: true,
    configPath: '~/.ssh/config',
    wildcardCount: 0,
    isWsl: true,
  }),
  loadGpuInfo: vi.fn().mockResolvedValue(null),
  loadGpuEnabled: vi.fn().mockResolvedValue(null),
  loadManagedEndpoints: vi.fn().mockResolvedValue([]),
  loadJobs: vi.fn().mockResolvedValue([]),
  loadModalSettings: vi.fn().mockResolvedValue({
    enabled: true,
    appName: '',
    environmentName: '',
    detailsMd: '',
    egressPolicy: null,
    maxConcurrentJobs: null,
    maxTimeoutSec: null,
    profiles: [],
  }),
}));

vi.mock('@/renderer/services/synonBiomedCompute', () => ({
  addSynonBiomedInferenceProvider: vi.fn(),
  addSynonBiomedSshHost: vi.fn(),
  deleteSynonBiomedComputeProvider: vi.fn(),
  loadSynonBiomedBioNemoSettings: vi.fn(),
  loadSynonBiomedComputeGpuEnabled: computeMocks.loadGpuEnabled,
  loadSynonBiomedComputeGpuInfo: computeMocks.loadGpuInfo,
  loadSynonBiomedComputeJob: vi.fn(),
  loadSynonBiomedComputeJobLog: vi.fn(),
  loadSynonBiomedComputeJobs: computeMocks.loadJobs,
  loadSynonBiomedComputeProvider: vi.fn(),
  loadSynonBiomedComputeProviders: computeMocks.loadProviders,
  loadSynonBiomedManagedEndpoints: computeMocks.loadManagedEndpoints,
  loadSynonBiomedModalSettings: computeMocks.loadModalSettings,
  loadSynonBiomedSessionComputeProviders: vi.fn().mockResolvedValue({ providers: [] }),
  loadSynonBiomedSshAliases: computeMocks.loadAliases,
  probeSynonBiomedComputeProvider: vi.fn(),
  saveSynonBiomedComputeProviderDetails: vi.fn(),
  setSynonBiomedBioNemoSettings: vi.fn(),
  setSynonBiomedComputeGpuEnabled: vi.fn(),
  setSynonBiomedModalEnabled: vi.fn(),
  setSynonBiomedSessionComputeProvider: vi.fn(),
  stopSynonBiomedManagedEndpoint: vi.fn(),
}));

vi.mock('@/renderer/services/synonBiomedGateway', () => ({
  loadSynonBiomedProjects: vi.fn().mockResolvedValue([]),
}));

vi.mock('@/renderer/services/synonBiomedWorkspaceSettings', () => ({
  loadSynonBiomedSecrets: vi.fn().mockResolvedValue([]),
}));

import { ComputeSettingsContent } from '@/renderer/pages/settings/ComputeSettings';

describe('ComputeSettings configuration dialogs', () => {
  it('keeps the overview mounted while SSH and cloud-provider configuration open as standard dialogs', async () => {
    await renderWithI18n(<ComputeSettingsContent />);

    const overview = await screen.findByTestId('synon-biomed-compute-section');
    await waitFor(() => expect(computeMocks.loadProviders).toHaveBeenCalled());

    fireEvent.click(screen.getByRole('button', { name: '添加 SSH 主机' }));
    const sshDialog = await screen.findByRole('dialog', { name: '添加 SSH 主机' });
    expect(overview).toBeInTheDocument();
    fireEvent.click(within(sshDialog).getByRole('button', { name: '取消' }));
    await waitFor(() => expect(sshDialog).not.toBeInTheDocument());

    fireEvent.click(screen.getAllByRole('button', { name: '配置' })[0]);
    const cloudDialog = await screen.findByRole('dialog', { name: 'Modal' });
    expect(overview).toBeInTheDocument();
    fireEvent.click(within(cloudDialog).getByRole('button', { name: '取消' }));
    await waitFor(() => expect(cloudDialog).not.toBeInTheDocument());
  });
});

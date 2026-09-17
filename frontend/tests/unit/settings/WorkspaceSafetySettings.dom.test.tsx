import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { Message } from '@arco-design/web-react';
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { NetworkSettingsContent } from '@/renderer/pages/settings/NetworkSettings';
import { renderWithSettingsI18n } from './settingsI18nTestUtils';

const services = vi.hoisted(() => ({
  loadNetwork: vi.fn(),
  updateGroups: vi.fn(),
  addDomain: vi.fn(),
  removeDomain: vi.fn(),
  replaceDomains: vi.fn(),
}));

vi.mock('@/renderer/services/synonBiomedWorkspaceSettings', () => ({
  loadSynonBiomedNetworkSettings: services.loadNetwork,
  updateSynonBiomedAllowlistGroups: services.updateGroups,
  addSynonBiomedAllowedDomain: services.addDomain,
  removeSynonBiomedAllowedDomain: services.removeDomain,
  replaceSynonBiomedAllowedDomains: services.replaceDomains,
}));

const networkSnapshot = {
  groups: [
    {
      id: 'pkg',
      label: 'Package management',
      description: 'pip and conda',
      locked: true,
      domains: ['pypi.org', 'repo.anaconda.com'],
    },
    {
      id: 'nih',
      label: 'NCBI / NIH',
      description: 'PubMed',
      locked: false,
      domains: ['*.ncbi.nlm.nih.gov'],
    },
  ],
  disabledGroups: [],
  activeKernelCount: 2,
  hasSeenOnboarding: true,
  domains: ['3dmol.org'],
  configDomains: ['api.internal.example'],
  deniedDomains: ['transfer.sh'],
};

describe('workspace network settings', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.spyOn(Message, 'success').mockImplementation(() => ({ close: vi.fn() }));
    vi.spyOn(Message, 'error').mockImplementation(() => ({ close: vi.fn() }));
    vi.spyOn(Message, 'info').mockImplementation(() => ({ close: vi.fn() }));
    services.loadNetwork.mockResolvedValue(networkSnapshot);
    services.updateGroups.mockResolvedValue(undefined);
    services.addDomain.mockResolvedValue(undefined);
    services.removeDomain.mockResolvedValue(undefined);
    services.replaceDomains.mockResolvedValue(undefined);
  });

  it('shows complete network groups, domains and real group/add mutations', async () => {
    await renderWithSettingsI18n(<NetworkSettingsContent />);

    expect(await screen.findByText('预设域名组')).toBeInTheDocument();
    expect(screen.getByTestId('builtin-network-groups')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: /包管理/ }));
    expect(screen.getByText('pypi.org')).toBeInTheDocument();
    expect(screen.getByText('repo.anaconda.com')).toBeInTheDocument();
    expect(screen.getByText('3dmol.org')).toBeInTheDocument();
    expect(screen.getByText('api.internal.example')).toBeInTheDocument();
    expect(screen.getByText('受保护的网络目标')).toBeInTheDocument();
    expect(screen.getAllByText('查看 1 个域名')).toHaveLength(2);

    fireEvent.click(screen.getByRole('switch', { name: '启用 NCBI / NIH' }));
    await waitFor(() => expect(services.updateGroups).toHaveBeenCalledWith(['nih']));

    fireEvent.click(screen.getByRole('button', { name: '添加域名' }));
    fireEvent.change(screen.getByTestId('allowed-domain-input'), { target: { value: 'example.org' } });
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: '确定' }));
    await waitFor(() => expect(services.addDomain).toHaveBeenCalledWith('example.org'));
  });

  it('shows a recoverable error state when the initial network request fails', async () => {
    services.loadNetwork.mockRejectedValueOnce(new Error('backend unavailable')).mockResolvedValueOnce(networkSnapshot);
    await renderWithSettingsI18n(<NetworkSettingsContent />);

    expect(await screen.findByRole('alert')).toHaveTextContent('无法加载网络设置。');
    fireEvent.click(screen.getByRole('button', { name: '重新加载' }));
    expect(await screen.findByTestId('builtin-network-groups')).toBeInTheDocument();
    await waitFor(() => expect(services.loadNetwork).toHaveBeenCalledTimes(2));
  });

  it('clears only user-authorized domains through the replace contract', async () => {
    await renderWithSettingsI18n(<NetworkSettingsContent />);
    await screen.findByText('3dmol.org');
    fireEvent.click(screen.getByRole('button', { name: '全部清除' }));
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByText(/内置分组和配置文件域名不会改变/)).toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole('button', { name: '确定' }));
    await waitFor(() => expect(services.replaceDomains).toHaveBeenCalledWith([]));
  });

  it('renders network controls in English', async () => {
    await renderWithSettingsI18n(<NetworkSettingsContent />, 'en-US');

    expect(await screen.findByRole('heading', { name: 'Network' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Add domain' })).toBeInTheDocument();
  });
});

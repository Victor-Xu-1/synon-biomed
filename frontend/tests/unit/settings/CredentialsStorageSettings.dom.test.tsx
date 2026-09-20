import { fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { MemoryRouter } from 'react-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { CredentialsSettingsContent } from '@/renderer/pages/settings/CredentialsSettings';
import { StorageSettingsContent } from '@/renderer/pages/settings/StorageSettings';
import { renderWithSettingsI18n } from './settingsI18nTestUtils';

const mocks = vi.hoisted(() => ({
  loadSecrets: vi.fn(),
  createSecret: vi.fn(),
  updateSecret: vi.fn(),
  deleteSecret: vi.fn(),
  loadDirectory: vi.fn(),
  loadUsage: vi.fn(),
  loadStorageRules: vi.fn(),
  saveStorageRules: vi.fn(),
  loadCloud: vi.fn(),
  changeDirectory: vi.fn(),
  clearLastMove: vi.fn(),
  testCloud: vi.fn(),
  loadProjects: vi.fn(),
}));

vi.mock('@arco-design/web-react', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@arco-design/web-react')>();
  return {
    ...actual,
    Message: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
  };
});

vi.mock('@/renderer/services/synonBiomedWorkspaceSettings', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/renderer/services/synonBiomedWorkspaceSettings')>();
  return {
    ...actual,
    loadSynonBiomedSecrets: mocks.loadSecrets,
    createSynonBiomedSecret: mocks.createSecret,
    updateSynonBiomedSecret: mocks.updateSecret,
    deleteSynonBiomedSecret: mocks.deleteSecret,
    loadSynonBiomedDataDirectory: mocks.loadDirectory,
    loadSynonBiomedDiskUsage: mocks.loadUsage,
    loadSynonBiomedStorageRules: mocks.loadStorageRules,
    saveSynonBiomedStorageRules: mocks.saveStorageRules,
    loadSynonBiomedCloudCredentials: mocks.loadCloud,
    changeSynonBiomedDataDirectory: mocks.changeDirectory,
    clearSynonBiomedLastDataDirectoryMove: mocks.clearLastMove,
    testSynonBiomedCloudCredential: mocks.testCloud,
  };
});

vi.mock('@/renderer/services/synonBiomedGateway', () => ({
  loadSynonBiomedProjects: mocks.loadProjects,
}));

vi.mock('@/renderer/pages/settings/components/SettingsPageWrapper', () => ({
  default: ({ children }: React.PropsWithChildren) => <div>{children}</div>,
}));

vi.mock('@/renderer/pages/settings/components/SettingsPageHeader', () => ({
  default: ({ title, description, actions }: { title: string; description?: string; actions?: React.ReactNode }) => (
    <header>
      <h1>{title}</h1>
      <p>{description}</p>
      {actions}
    </header>
  ),
}));

vi.mock('@/renderer/pages/settings/NetworkSettings', () => ({
  SettingsSection: ({
    title,
    description,
    actions,
    children,
  }: React.PropsWithChildren<{ title: string; description?: string; actions?: React.ReactNode }>) => (
    <section>
      <h2>{title}</h2>
      <p>{description}</p>
      {actions}
      {children}
    </section>
  ),
  EmptyText: ({ children }: React.PropsWithChildren) => <div>{children}</div>,
  RefreshButton: ({ onClick }: { onClick: () => void }) => <button onClick={onClick}>刷新</button>,
}));

vi.mock('@/renderer/pages/settings/components/SynonBiomedCloudBrowser', () => ({
  default: () => <div>云目录浏览器</div>,
}));

const redactedSecret = (overrides: Record<string, unknown>) => ({
  id: 'secret-1',
  provider: 'generic',
  name: 'API_KEY',
  description: '',
  credentialType: 'api_key',
  buckets: [],
  region: '',
  maskedPreview: 'configured',
  maskedFields: [],
  valueConfigured: true,
  credentialsConfigured: false,
  credentialFields: [],
  createdAt: '',
  updatedAt: '',
  ...overrides,
});

describe('Credentials and Storage settings', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.createSecret.mockResolvedValue(undefined);
    mocks.updateSecret.mockResolvedValue(undefined);
    mocks.deleteSecret.mockResolvedValue(undefined);
    mocks.changeDirectory.mockResolvedValue({ restarting: false, restartRequired: true, move: null });
    mocks.clearLastMove.mockResolvedValue(undefined);
    mocks.loadCloud.mockResolvedValue([]);
    mocks.loadProjects.mockResolvedValue([]);
    mocks.loadStorageRules.mockResolvedValue({
      root: '/data/current',
      rules: { taskArtifacts: 'task_runs/artifacts', logs: 'shell_tasks', toolResults: 'tool-results', temp: 'tmp' },
      paths: {
        taskArtifacts: '/data/current/task_runs/artifacts',
        logs: '/data/current/shell_tasks',
        toolResults: '/data/current/tool-results',
        temp: '/data/current/tmp',
      },
      systemPaths: { taskRuns: '/data/current/task_runs', workspace: '/data/current/workspace' },
    });
  });

  it('renders all v1.1 service credential connections and edits redacted credentials without exposing values', async () => {
    mocks.loadSecrets.mockResolvedValue([
      redactedSecret({
        id: 'aws-1',
        provider: 'aws',
        name: 'AWS Research',
        credentialType: 'access_key',
        valueConfigured: false,
        credentialsConfigured: true,
        credentialFields: ['access_key_id', 'secret_access_key'],
        maskedFields: ['access_key_id', 'secret_access_key'],
      }),
      redactedSecret({ id: 'nvidia-1', name: 'NVIDIA_API_KEY' }),
      redactedSecret({ id: 'llm-1', provider: 'llm', name: 'GLM-5.2' }),
    ]);

    await renderWithSettingsI18n(<CredentialsSettingsContent />);

    await waitFor(() => expect(screen.getByTestId('credential-provider-aws')).toHaveTextContent('已连接'));
    for (const provider of ['AWS', 'GitHub', 'Google Cloud', '文献访问', 'Microsoft Azure', 'Modal', 'NVIDIA API']) {
      expect(screen.getByText(provider)).toBeInTheDocument();
    }
    expect(screen.getByTestId('credential-provider-nvidia')).toHaveTextContent('已连接');
    expect(screen.queryByText('GLM-5.2')).not.toBeInTheDocument();

    fireEvent.click(screen.getByTestId('credential-provider-aws').querySelector('button')!);
    expect(await screen.findByRole('dialog')).toHaveTextContent('编辑 AWS');
    expect(screen.getByLabelText('访问密钥 ID')).toHaveAttribute('placeholder', '已保存，留空保持原值');
    expect(screen.queryByDisplayValue(/AKIA|secret/i)).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('名称'), { target: { value: 'AWS Renamed' } });
    fireEvent.click(screen.getByRole('button', { name: '保存更改' }));
    await waitFor(() =>
      expect(mocks.updateSecret).toHaveBeenCalledWith(
        'aws-1',
        expect.objectContaining({ provider: 'aws', name: 'AWS Renamed', credentialType: 'access_key' })
      )
    );
    expect(mocks.updateSecret.mock.calls[0][1]).not.toHaveProperty('credentials');
  });

  it('shows data location immediately while the real disk scan remains independently loading', async () => {
    let resolveUsage!: (value: unknown) => void;
    mocks.loadDirectory.mockResolvedValue({
      current: '/data/current',
      resolved: null,
      defaultPath: '/data/default',
      source: 'flag',
      usageBytes: 1024,
      freeBytes: 2048,
      activeFrames: 0,
      configPath: '/config/synonbiomed.toml',
      pendingMove: null,
      lastMove: null,
    });
    mocks.loadUsage.mockReturnValue(
      new Promise((resolve) => {
        resolveUsage = resolve;
      })
    );

    await renderWithSettingsI18n(
      <MemoryRouter>
        <StorageSettingsContent />
      </MemoryRouter>
    );

    expect(await screen.findAllByText('/data/current')).not.toHaveLength(0);
    expect(screen.getByText('正在扫描文件… 其他设置仍可使用。')).toBeInTheDocument();
    expect(mocks.loadDirectory).toHaveBeenCalledWith(expect.objectContaining({ includeUsage: false }));
    fireEvent.click(screen.getAllByRole('button', { name: '更改位置' }).at(-1)!);
    const location = await screen.findByLabelText('新位置');
    expect(location).toHaveValue('/data/current');
    fireEvent.change(location, { target: { value: '/data/new' } });
    await waitFor(() => expect(screen.getAllByRole('button', { name: '更改位置' }).at(-1)!).not.toBeDisabled());
    fireEvent.click(screen.getAllByRole('button', { name: '更改位置' }).at(-1)!);
    await waitFor(() => expect(mocks.changeDirectory).toHaveBeenCalledWith({ path: '/data/new', migrate: true }));

    resolveUsage({ artifactsBytes: 10, workspaceBytes: 20, toolResultsBytes: 30, condaBytes: 40, availableBytes: 50 });
    expect(await screen.findByText('100 B')).toBeInTheDocument();
    expect(screen.getByText('50 B')).toBeInTheDocument();
  });

  it('renders credentials and storage in English', async () => {
    mocks.loadSecrets.mockResolvedValue([]);
    mocks.loadDirectory.mockResolvedValue({
      current: '/data/current',
      resolved: null,
      defaultPath: '/data/default',
      source: 'pointer',
      usageBytes: 0,
      freeBytes: 1024,
      activeFrames: 0,
      configPath: '',
      pendingMove: null,
      lastMove: null,
    });
    mocks.loadUsage.mockResolvedValue({
      artifactsBytes: 0,
      workspaceBytes: 0,
      toolResultsBytes: 0,
      condaBytes: 0,
      availableBytes: 1024,
    });

    const credentials = await renderWithSettingsI18n(<CredentialsSettingsContent />, 'en-US');
    expect(await screen.findByRole('heading', { name: 'Credentials' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Add custom credential/ })).toBeInTheDocument();
    fireEvent.click(screen.getByTestId('credential-provider-aws').querySelector('button')!);
    expect(await screen.findByLabelText('Access key ID')).toBeInTheDocument();
    credentials.unmount();

    await renderWithSettingsI18n(
      <MemoryRouter>
        <StorageSettingsContent />
      </MemoryRouter>,
      'en-US'
    );
    expect(await screen.findByRole('heading', { name: 'Storage' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Data location' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Cloud storage' })).toBeInTheDocument();
  });

  it('edits and saves the project task file storage rules', async () => {
    mocks.loadDirectory.mockResolvedValue({
      current: '/data/current',
      resolved: null,
      defaultPath: '/data/default',
      source: 'pointer',
      usageBytes: 0,
      freeBytes: 1024,
      activeFrames: 0,
      configPath: '',
      pendingMove: null,
      lastMove: null,
    });
    mocks.loadUsage.mockResolvedValue({
      artifactsBytes: 0,
      workspaceBytes: 0,
      toolResultsBytes: 0,
      condaBytes: 0,
      availableBytes: 1024,
    });
    mocks.saveStorageRules.mockResolvedValue({
      root: '/data/current',
      rules: { taskArtifacts: 'runs/artifacts', logs: 'shell_tasks', toolResults: 'tool-results', temp: 'tmp' },
      paths: {
        taskArtifacts: '/data/current/runs/artifacts',
        logs: '/data/current/shell_tasks',
        toolResults: '/data/current/tool-results',
        temp: '/data/current/tmp',
      },
      systemPaths: { taskRuns: '/data/current/task_runs', workspace: '/data/current/workspace' },
    });

    await renderWithSettingsI18n(
      <MemoryRouter>
        <StorageSettingsContent />
      </MemoryRouter>,
      'en-US'
    );

    fireEvent.click(await screen.findByRole('button', { name: 'Edit rules' }));
    fireEvent.change(await screen.findByRole('textbox', { name: 'Generated task files' }), {
      target: { value: 'runs/artifacts' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() =>
      expect(mocks.saveStorageRules).toHaveBeenCalledWith({
        taskArtifacts: 'runs/artifacts',
        logs: 'shell_tasks',
        toolResults: 'tool-results',
        temp: 'tmp',
      })
    );
  });
});

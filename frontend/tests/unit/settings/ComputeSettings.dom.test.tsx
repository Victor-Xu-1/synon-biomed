import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react';
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ComputeSettingsContent } from '@/renderer/pages/settings/ComputeSettings';
import { renderWithSettingsI18n } from './settingsI18nTestUtils';

const mocks = vi.hoisted(() => ({
  loadProviders: vi.fn(),
  loadProvider: vi.fn(),
  loadAliases: vi.fn(),
  probe: vi.fn(),
  saveDetails: vi.fn(),
  addSsh: vi.fn(),
  addInference: vi.fn(),
  remove: vi.fn(),
  loadGpuInfo: vi.fn(),
  loadGpuEnabled: vi.fn(),
  setGpuEnabled: vi.fn(),
  loadManagedEndpoints: vi.fn(),
  stopManagedEndpoint: vi.fn(),
  loadJobs: vi.fn(),
  loadJob: vi.fn(),
  loadJobLog: vi.fn(),
  loadSessionProviders: vi.fn(),
  setSessionProvider: vi.fn(),
  loadModalSettings: vi.fn(),
  setModalEnabled: vi.fn(),
  loadBioNemoSettings: vi.fn(),
  setBioNemoSettings: vi.fn(),
  loadSecrets: vi.fn(),
  loadProjects: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
}));

vi.mock('@/renderer/services/synonBiomedCompute', () => ({
  loadSynonBiomedComputeProviders: mocks.loadProviders,
  loadSynonBiomedComputeProvider: mocks.loadProvider,
  loadSynonBiomedSshAliases: mocks.loadAliases,
  probeSynonBiomedComputeProvider: mocks.probe,
  saveSynonBiomedComputeProviderDetails: mocks.saveDetails,
  addSynonBiomedSshHost: mocks.addSsh,
  addSynonBiomedInferenceProvider: mocks.addInference,
  deleteSynonBiomedComputeProvider: mocks.remove,
  loadSynonBiomedComputeGpuInfo: mocks.loadGpuInfo,
  loadSynonBiomedComputeGpuEnabled: mocks.loadGpuEnabled,
  setSynonBiomedComputeGpuEnabled: mocks.setGpuEnabled,
  loadSynonBiomedManagedEndpoints: mocks.loadManagedEndpoints,
  stopSynonBiomedManagedEndpoint: mocks.stopManagedEndpoint,
  loadSynonBiomedComputeJobs: mocks.loadJobs,
  loadSynonBiomedComputeJob: mocks.loadJob,
  loadSynonBiomedComputeJobLog: mocks.loadJobLog,
  loadSynonBiomedSessionComputeProviders: mocks.loadSessionProviders,
  setSynonBiomedSessionComputeProvider: mocks.setSessionProvider,
  loadSynonBiomedModalSettings: mocks.loadModalSettings,
  setSynonBiomedModalEnabled: mocks.setModalEnabled,
  loadSynonBiomedBioNemoSettings: mocks.loadBioNemoSettings,
  setSynonBiomedBioNemoSettings: mocks.setBioNemoSettings,
}));

vi.mock('@/renderer/services/synonBiomedWorkspaceSettings', () => ({
  loadSynonBiomedSecrets: mocks.loadSecrets,
}));

vi.mock('@/renderer/services/synonBiomedGateway', () => ({
  loadSynonBiomedProjects: mocks.loadProjects,
}));

vi.mock('@arco-design/web-react', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@arco-design/web-react')>();
  return {
    ...actual,
    Message: { ...actual.Message, success: mocks.success, error: mocks.error },
  };
});

const providers = [
  {
    name: 'ssh:hpc-a',
    displayName: 'HPC A',
    family: 'ssh',
    checked: true,
    detailsMd: '## Cluster\nSlurm cluster',
    scratchRoot: '/scratch/victor',
    scratchRootSource: 'user',
    dataRoots: ['/data/shared'],
    maxConcurrentJobs: 4,
  },
  {
    name: 'infer:boltz-local',
    displayName: 'Boltz Local',
    family: 'infer',
    checked: false,
    endpoint: 'http://127.0.0.1:9000',
    skillName: 'boltz2-nim',
    credentialName: 'NVIDIA_API_KEY',
    managedFamily: true,
  },
];

const computeJob = {
  jobId: 'compute-fixture-job-0001',
  environment: 'python',
  tierType: 'gpu',
  provider: 'fixture-nim',
  frameId: 'frame-a',
  projectId: 'project-a',
  state: 'running',
  startedAt: '2026-07-11T08:00:00Z',
  startedAtIso: '2026-07-11T08:00:00Z',
  intent: { kind: 'inference' },
  hardwareDetails: { gpu: 'fixture' },
  originToolUseId: 'tool-compute-1',
  rootFrameId: 'root-a',
  providerFamily: 'infer',
  providerLabel: 'fixture-nim',
  externalId: 'sandbox-1',
  externalUrl: 'https://compute.example/jobs/sandbox-1',
  supportsTail: true,
  endedAtIso: null,
  harvest: null,
  errorKind: null,
  leftOnRemote: [],
  systemHint: null,
};

describe('ComputeSettingsContent', () => {
  beforeEach(() => {
    mocks.loadProviders.mockResolvedValue(providers);
    mocks.loadProvider.mockResolvedValue(providers[0]);
    mocks.loadAliases.mockResolvedValue({
      aliases: [{ alias: 'hpc-a', hostName: 'hpc.example.org', user: 'victor' }],
      configFound: true,
      configPath: '/home/victor_1/.ssh/config',
      wildcardCount: 0,
      isWsl: true,
    });
    mocks.probe.mockResolvedValue({ scheduler: 'slurm', cpus: 64, gpus: 4 });
    mocks.saveDetails.mockResolvedValue(undefined);
    mocks.addSsh.mockResolvedValue(undefined);
    mocks.addInference.mockResolvedValue({});
    mocks.remove.mockResolvedValue(undefined);
    mocks.loadGpuInfo.mockResolvedValue({
      available: true,
      name: 'NVIDIA A100',
      memoryMb: 81920,
      cudaVersion: '12.4',
      count: 2,
    });
    mocks.loadGpuEnabled.mockResolvedValue({ enabled: true, override: true, present: true, name: 'NVIDIA A100' });
    mocks.setGpuEnabled.mockResolvedValue(undefined);
    mocks.loadManagedEndpoints.mockResolvedValue([
      {
        name: 'boltz2-local',
        displayName: 'Boltz2 Local',
        location: 'local',
        state: 'live',
        port: 9000,
        endpoint: 'http://127.0.0.1:9000',
        serviceDir: '/srv/boltz2',
        serviceDirBytes: 1024,
        lastError: null,
      },
    ]);
    mocks.stopManagedEndpoint.mockResolvedValue(undefined);
    mocks.loadProjects.mockResolvedValue([
      {
        projectId: 'project-a',
        name: 'Compute fixture',
        description: null,
        context: null,
        conversationCount: 1,
        artifactCount: 0,
        createdAt: null,
        updatedAt: null,
        lastActiveAt: null,
      },
    ]);
    mocks.loadJobs.mockResolvedValue([computeJob]);
    mocks.loadJob.mockResolvedValue({
      ...computeJob,
      harvest: { stdout: { exists: true, size: 70009 }, stderr: { exists: true, size: 8 } },
    });
    mocks.loadJobLog.mockImplementation(async (_jobId: string, options: { stream?: string }) => ({
      exists: true,
      size: options.stream === 'stderr' ? 8 : 70009,
      content: options.stream === 'stderr' ? 'warning\n' : 'complete\n',
      truncated: false,
    }));
    mocks.loadSessionProviders.mockResolvedValue(['infer:fixture-nim']);
    mocks.setSessionProvider.mockResolvedValue(undefined);
    mocks.loadModalSettings.mockResolvedValue({
      provider: 'modal',
      enabled: true,
      detailsMd: 'Modal notes',
      appName: 'synonbiomed-research',
      environmentName: 'main',
      egressPolicy: { mode: 'allowlist', mirror: true, additional: ['api.example.com'] },
      maxConcurrentJobs: 10,
      maxTimeoutSec: 43200,
      profiles: [{ name: 'Synon Biomed (stored)', active: true, tokenIdMasked: 'ak-a····test' }],
      ignoredProfiles: [],
      tomlMissing: true,
      credsError: null,
      hasStoredCredential: true,
    });
    mocks.setModalEnabled.mockResolvedValue(undefined);
    mocks.loadBioNemoSettings.mockResolvedValue({
      enabled: true,
      override: true,
      mode: 'hosted',
      hostedHost: 'health.api.nvidia.com',
    });
    mocks.setBioNemoSettings.mockResolvedValue({
      enabled: true,
      override: true,
      mode: 'hosted',
      hostedHost: 'health.api.nvidia.com',
    });
    mocks.loadSecrets.mockResolvedValue([
      { id: 'NVIDIA_API_KEY', provider: 'nvidia', name: 'NVIDIA_API_KEY', maskedPreview: 'nvap····test' },
    ]);
  });

  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it('renders native SynonAI provider cards with actionable lifecycle controls', async () => {
    await renderWithSettingsI18n(<ComputeSettingsContent />);

    const sshCard = await screen.findByTestId('synon-biomed-compute-provider-ssh-hpc-a');
    expect(within(sshCard).getByText('HPC A')).toBeInTheDocument();
    expect(within(sshCard).getByText('/scratch/victor')).toBeInTheDocument();
    expect(within(sshCard).getByRole('button', { name: '探测 HPC A' })).toBeInTheDocument();
    expect(within(sshCard).getByRole('button', { name: '编辑 HPC A' })).toBeInTheDocument();
    expect(within(sshCard).getByRole('button', { name: '删除 HPC A' })).toBeInTheDocument();
    expect(screen.getByText('Boltz Local')).toBeInTheDocument();
    expect(mocks.loadProviders).toHaveBeenCalledTimes(1);
  });

  it('probes and edits one provider through the exact native compute service', async () => {
    await renderWithSettingsI18n(<ComputeSettingsContent />);
    await screen.findByText('HPC A');

    fireEvent.click(screen.getByRole('button', { name: '探测 HPC A' }));
    await waitFor(() => expect(mocks.probe).toHaveBeenCalledWith('ssh:hpc-a'));

    fireEvent.click(screen.getByRole('button', { name: '编辑 HPC A' }));
    const dialog = await screen.findByRole('dialog', { name: '编辑计算提供方' });
    expect(mocks.loadProvider).toHaveBeenCalledWith('ssh:hpc-a');

    fireEvent.change(within(dialog).getByLabelText('说明'), { target: { value: '## Cluster\nUpdated' } });
    fireEvent.change(within(dialog).getByLabelText('暂存目录'), { target: { value: '/scratch/new' } });
    fireEvent.change(within(dialog).getByLabelText('数据目录'), { target: { value: '/data/shared\n/datasets' } });
    fireEvent.change(within(dialog).getByLabelText('最大并发任务'), { target: { value: '8' } });
    fireEvent.click(within(dialog).getByRole('button', { name: '保存' }));

    await waitFor(() =>
      expect(mocks.saveDetails).toHaveBeenCalledWith('ssh:hpc-a', {
        detailsMd: '## Cluster\nUpdated',
        scratchRoot: '/scratch/new',
        dataRoots: ['/data/shared', '/datasets'],
        maxConcurrentJobs: 8,
      })
    );
    expect(mocks.loadProviders).toHaveBeenCalledTimes(3);
  });

  it('opens the v1.1 SSH host subpage and submits the real SSH registration contract', async () => {
    await renderWithSettingsI18n(<ComputeSettingsContent />);
    await screen.findByText('HPC A');

    fireEvent.click(screen.getByRole('button', { name: '添加 SSH 主机' }));
    const sshDialog = await screen.findByRole('dialog', { name: '添加 SSH 主机' });
    expect(within(sshDialog).getByText(/不会复制任何凭证/)).toBeInTheDocument();

    const aliasSelect = within(sshDialog).getByRole('combobox', { name: '主机别名' });
    const aliasInput = within(aliasSelect).getByRole('textbox');
    fireEvent.change(aliasInput, { target: { value: 'hpc-a' } });
    fireEvent.click(await screen.findByRole('option', { name: /hpc-a/ }));
    fireEvent.change(within(sshDialog).getByLabelText('说明（可选）'), {
      target: { value: 'Use sbatch on the gpu partition.' },
    });
    fireEvent.click(within(sshDialog).getByRole('button', { name: /高级覆盖/ }));
    fireEvent.change(within(sshDialog).getByLabelText('用户名'), { target: { value: 'victor' } });
    fireEvent.click(within(sshDialog).getByRole('button', { name: '添加' }));

    await waitFor(() =>
      expect(mocks.addSsh).toHaveBeenCalledWith({
        alias: 'hpc-a',
        initialContext: 'Use sbatch on the gpu partition.',
        overrides: { user: 'victor' },
      })
    );
  });

  it('adds and immediately probes a v1.1 inference endpoint without legacy agent surfaces', async () => {
    await renderWithSettingsI18n(<ComputeSettingsContent />);
    await screen.findByText('HPC A');

    fireEvent.click(screen.getByRole('button', { name: '添加模型端点' }));
    const dialog = await screen.findByRole('dialog', { name: '添加模型端点' });
    expect(within(dialog).getByText('AFold3 / OpenFold3（NVIDIA NIM）')).toBeInTheDocument();
    fireEvent.change(within(dialog).getByLabelText('端点名称'), { target: { value: 'boltz-new' } });
    fireEvent.change(within(dialog).getByLabelText('端点 URL'), { target: { value: 'http://127.0.0.1:9100' } });
    fireEvent.change(within(dialog).getByLabelText('关联 Skill'), { target: { value: 'boltz2-nim' } });
    fireEvent.change(within(dialog).getByLabelText('凭证名称'), { target: { value: 'NVIDIA_API_KEY' } });
    fireEvent.click(within(dialog).getByRole('button', { name: '保存并探测' }));

    await waitFor(() =>
      expect(mocks.addInference).toHaveBeenCalledWith({
        name: 'boltz-new',
        endpoint: 'http://127.0.0.1:9100',
        skillName: 'boltz2-nim',
        credentialName: 'NVIDIA_API_KEY',
      })
    );
    expect(mocks.probe).toHaveBeenCalledWith('infer:boltz-new');
    expect(document.querySelector('iframe')).toBeNull();
  });

  it('reproduces the complete v1.1 Modal provider settings workflow', async () => {
    await renderWithSettingsI18n(<ComputeSettingsContent />);
    await screen.findByText('HPC A');

    fireEvent.click(screen.getAllByRole('button', { name: '配置' })[0]);
    const modalDialog = await screen.findByRole('dialog', { name: 'Modal' });
    expect(within(modalDialog).getByLabelText('默认应用')).toHaveValue('synonbiomed-research');
    expect(within(modalDialog).getByLabelText('环境')).toHaveValue('main');
    expect(within(modalDialog).getByLabelText('允许域名')).toHaveValue('api.example.com');
    expect(within(modalDialog).getByText('Synon Biomed (stored)')).toBeInTheDocument();
    expect(within(modalDialog).getByLabelText('并发任务')).toHaveValue('10');
    expect(within(modalDialog).getByLabelText('默认容器超时')).toHaveValue('12');

    fireEvent.change(within(modalDialog).getByLabelText('详情'), { target: { value: 'Updated Modal notes' } });
    fireEvent.click(within(modalDialog).getByRole('button', { name: '保存' }));
    await waitFor(() =>
      expect(mocks.saveDetails).toHaveBeenCalledWith('byoc:modal', {
        appName: 'synonbiomed-research',
        environmentName: 'main',
        detailsMd: 'Updated Modal notes',
        egressPolicy: { mode: 'allowlist', mirror: true, additional: ['api.example.com'] },
        maxConcurrentJobs: 10,
        maxTimeoutSec: 43200,
      })
    );
  });

  it('shows the v1.1 BioNeMo family status separately from endpoint registration', async () => {
    await renderWithSettingsI18n(<ComputeSettingsContent />);
    await screen.findByText('HPC A');

    fireEvent.click(screen.getAllByRole('button', { name: '配置' })[1]);
    const dialog = await screen.findByRole('dialog', { name: 'NVIDIA BioNeMo NIM' });
    expect(within(dialog).getByRole('radio', { name: '远程' })).toBeChecked();
    expect(within(dialog).getByText('health.api.nvidia.com')).toBeInTheDocument();
    expect(within(dialog).getByText('NVIDIA_API_KEY')).toBeInTheDocument();
    expect(within(dialog).getByText('nvap····test')).toBeInTheDocument();
    expect(within(dialog).getByText('Boltz Local')).toBeInTheDocument();
    expect(dialog.querySelector('.arco-modal-footer')).toBeInTheDocument();
    expect(dialog.querySelector('footer')).not.toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: '断开' })).toBeInTheDocument();
    expect(within(dialog).queryByRole('button', { name: '保存并探测' })).not.toBeInTheDocument();
  });

  it('renders the v1.1 grouped compute control plane with host GPU and managed endpoint actions', async () => {
    await renderWithSettingsI18n(<ComputeSettingsContent />);

    expect(await screen.findByRole('heading', { name: '主机 GPU' })).toBeInTheDocument();
    expect(screen.getByText('NVIDIA A100')).toBeInTheDocument();
    expect(screen.getByRole('switch', { name: '主机 GPU' })).toBeChecked();
    expect(screen.getByRole('heading', { name: 'SSH 主机' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: '模型端点' })).toBeInTheDocument();
    expect(screen.getByText('Boltz2 Local')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('switch', { name: '主机 GPU' }));
    await waitFor(() => expect(mocks.setGpuEnabled).toHaveBeenCalledWith(false));

    fireEvent.click(screen.getByRole('button', { name: '停止 Boltz2 Local' }));
    await waitFor(() => expect(mocks.stopManagedEndpoint).toHaveBeenCalledWith('boltz2-local'));

    expect(screen.queryByText('提供方')).not.toBeInTheDocument();
    expect(screen.queryByText('可用')).not.toBeInTheDocument();
    expect(screen.queryByText('需要处理')).not.toBeInTheDocument();
  });

  it('reproduces the v1.1 compute job workflow with project filtering, logs and session controls', async () => {
    await renderWithSettingsI18n(<ComputeSettingsContent />);

    expect(await screen.findByRole('heading', { name: '计算作业' })).toBeInTheDocument();
    expect(mocks.loadProjects).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(mocks.loadJobs).toHaveBeenCalledWith('project-a'));

    const row = await screen.findByTestId('compute-job-compute-fixture-job-0001');
    expect(within(row).getByText('fixture-nim')).toBeInTheDocument();
    expect(within(row).getByText('python · gpu')).toBeInTheDocument();
    fireEvent.click(within(row).getByRole('button', { name: '查看作业 compute-fixture-job-0001' }));

    const dialog = await screen.findByRole('dialog', { name: '计算作业详情' });
    expect(within(dialog).getByText('complete')).toBeInTheDocument();
    expect(within(dialog).getByRole('switch', { name: '会话计算 infer:fixture-nim' })).toBeChecked();
    fireEvent.click(within(dialog).getByRole('tab', { name: '标准错误' }));
    expect(await within(dialog).findByText('warning')).toBeInTheDocument();

    fireEvent.click(within(dialog).getByRole('switch', { name: '会话计算 infer:fixture-nim' }));
    await waitFor(() => expect(mocks.setSessionProvider).toHaveBeenCalledWith('root-a', 'infer:fixture-nim', false));
  });
});

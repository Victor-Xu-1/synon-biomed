import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { MemoryRouter } from 'react-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

const mocks = vi.hoisted(() => ({
  loadAll: vi.fn(),
  stopKernel: vi.fn(),
  loadJobs: vi.fn(),
  loadJob: vi.fn(),
  loadJobLog: vi.fn(),
  kernelWake: null as null | (() => void),
  jobWake: null as null | ((event: { project_id: string }) => void),
  reconnect: null as null | (() => void),
}));

vi.mock('@/common', () => ({
  ipcBridge: {
    kernel: {
      executionCellUpdate: {
        on: (callback: () => void) => {
          mocks.kernelWake = callback;
          return () => {
            if (mocks.kernelWake === callback) mocks.kernelWake = null;
          };
        },
      },
    },
    compute: {
      jobUpdate: {
        on: (callback: (event: { project_id: string }) => void) => {
          mocks.jobWake = callback;
          return () => {
            if (mocks.jobWake === callback) mocks.jobWake = null;
          };
        },
      },
      jobLogChunk: { on: () => () => {} },
    },
    realtime: {
      reconnected: {
        on: (callback: () => void) => {
          mocks.reconnect = callback;
          return () => {
            if (mocks.reconnect === callback) mocks.reconnect = null;
          };
        },
      },
    },
  },
}));

vi.mock('@icon-park/react', () => ({
  ArrowRightUp: () => <span />,
  ArrowLeft: () => <span />,
  ArrowRight: () => <span />,
  CodeOne: () => <span />,
  Copy: () => <span />,
  Down: () => <span />,
  LinkOne: () => <span />,
  More: () => <span />,
  PauseOne: () => <span />,
  Refresh: () => <span />,
  Right: () => <span />,
}));
vi.mock('@/renderer/services/synonBiomedNotebook', () => ({
  loadSynonBiomedAllKernels: mocks.loadAll,
  stopSynonBiomedKernel: mocks.stopKernel,
}));
vi.mock('@/renderer/services/synonBiomedCompute', () => ({
  loadSynonBiomedComputeJobs: mocks.loadJobs,
  loadSynonBiomedComputeJob: mocks.loadJob,
  loadSynonBiomedComputeJobLog: mocks.loadJobLog,
}));

import SynonBiomedComputeRuntimePanel from '@/renderer/components/synonBiomed/runtime/SynonBiomedComputeRuntimePanel';

const kernel = (overrides: Record<string, unknown> = {}) => ({
  frameId: 'frame-1',
  rootFrameId: 'root-1',
  projectId: 'project-1',
  projectName: 'Personal Workspace',
  sessionTitle: 'CRBN Ligand Design',
  agentName: 'OPERON',
  delegateName: null,
  environment: 'chem',
  language: 'python',
  kernelKind: 'analysis',
  kernelId: 'kernel-1',
  busy: false,
  starting: false,
  pidVisible: true,
  executionCount: 9,
  cellCount: 9,
  currentCell: null,
  lastCell: {
    source: 'Generating 2D structure image grid',
    endedAt: '2026-08-21T00:00:00Z',
  },
  lastUsed: '2026-08-21T00:00:00Z',
  lastDescription: 'Generating 2D structure image grid',
  rssBytes: 33200000,
  cpuPct: 0,
  ...overrides,
});

const inventory = {
  kernels: [
    kernel(),
    kernel({
      frameId: 'frame-2',
      rootFrameId: 'root-2',
      sessionTitle: '你好',
      kernelId: 'kernel-2',
      executionCount: 3,
      cellCount: 3,
      lastDescription: 'Parsing compound data with error handling',
      lastCell: {
        source: 'Parsing compound data with error handling',
        endedAt: '2026-08-20T22:00:00Z',
      },
      rssBytes: 13200000,
    }),
  ],
  hasHistory: true,
  machine: {
    sampledAt: new Date().toISOString(),
    totalMemoryBytes: 1000000000,
    availableMemoryBytes: 500000000,
    cores: 16,
    hostCores: 16,
    busyCores: 0,
    diskTotalBytes: 2000000000,
    diskAvailableBytes: 1000000000,
    cpuPct: 0,
    kernelRssBytes: 46400000,
    kernelCpuPct: 0,
    kernelCount: 2,
  },
};

const renderPanel = async (panel: React.ReactElement) => {
  let rendered: Awaited<ReturnType<typeof renderWithI18n>> | null = null;
  await act(async () => {
    rendered = await renderWithI18n(<MemoryRouter>{panel}</MemoryRouter>, 'zh-CN');
    await Promise.resolve();
  });
  await waitFor(() => expect(mocks.loadAll).toHaveBeenCalled());
  await waitFor(() => expect(mocks.loadJobs).toHaveBeenCalled());
  if (!rendered) throw new Error('panel did not render');
  return rendered;
};

describe('SynonBiomedComputeRuntimePanel', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.loadAll.mockResolvedValue(inventory);
    mocks.loadJobs.mockResolvedValue([]);
    mocks.loadJob.mockResolvedValue(null);
    mocks.loadJobLog.mockResolvedValue({ exists: false, size: 0, content: '', truncated: false });
    mocks.stopKernel.mockResolvedValue({
      ok: true,
      mode: 'clear',
      interrupted: false,
      reason: null,
    });
    Object.defineProperty(HTMLCanvasElement.prototype, 'getContext', {
      configurable: true,
      value: () => ({
        scale: vi.fn(),
        clearRect: vi.fn(),
        beginPath: vi.fn(),
        moveTo: vi.fn(),
        lineTo: vi.fn(),
        stroke: vi.fn(),
        set strokeStyle(_value: string) {},
        set lineWidth(_value: number) {},
        set lineJoin(_value: CanvasLineJoin) {},
      }),
    });
  });

  it('renders machine metrics, current session first, and the cross-session divider', async () => {
    await renderPanel(
      <SynonBiomedComputeRuntimePanel
        rootFrameId='root-1'
        projectId='project-1'
        projectName='Personal Workspace'
        sessionTitle='CRBN Ligand Design'
      />
    );

    const panel = await screen.findByTestId('synon-biomed-compute-runtime-panel');
    await waitFor(() => expect(panel).toHaveTextContent('Generating 2D structure image grid'));
    expect(panel).toHaveTextContent('44.3 MB');
    expect(panel).toHaveTextContent('0.0 cores');
    expect(panel).toHaveTextContent('CRBN Ligand Design');
    expect(panel).toHaveTextContent('你好');
    expect(screen.getByTestId('compute-session-divider')).toHaveTextContent('1 个其他会话');
    expect(screen.getAllByTestId('kernel-row')).toHaveLength(2);
  });

  it('stops an idle kernel through the workspace clear contract', async () => {
    await renderPanel(<SynonBiomedComputeRuntimePanel rootFrameId='root-1' projectId='project-1' />);

    const stopButtons = await screen.findAllByRole('button', {
      name: '停止/终止 kernel',
    });
    fireEvent.click(stopButtons[0]);
    expect(await screen.findByTestId('stop-kernel-drawer')).toBeInTheDocument();
    fireEvent.change(screen.getByTestId('stop-reason-input'), {
      target: { value: '  \u91ca\u653e\u8d44\u6e90  ' },
    });
    fireEvent.click(screen.getByTestId('stop-kill'));
    await waitFor(() =>
      expect(mocks.stopKernel).toHaveBeenCalledWith('frame-1', 'kernel-1', {
        mode: 'clear',
        reason: '\u91ca\u653e\u8d44\u6e90',
      })
    );
  });

  it('keeps the interrupt reason and offers a force kill when the same cell does not settle', async () => {
    const busyInventory = {
      ...inventory,
      kernels: [
        kernel({
          busy: true,
          currentCell: {
            tag: 'cell-busy',
            source: 'while True: pass',
            origin: 'agent',
            startedAt: new Date().toISOString(),
            humanDescription: 'Running an expensive calculation',
          },
          cpuPct: 95,
        }),
      ],
      machine: { ...inventory.machine, kernelCount: 1, kernelCpuPct: 95 },
    };
    mocks.loadAll.mockResolvedValue(busyInventory);
    mocks.stopKernel.mockResolvedValueOnce({
      ok: true,
      mode: 'interrupt',
      interrupted: true,
      reason: 'use a cheaper path',
    });

    await renderPanel(<SynonBiomedComputeRuntimePanel rootFrameId='root-1' projectId='project-1' />);

    fireEvent.click(
      await screen.findByRole('button', {
        name: '\u505c\u6b62/\u7ec8\u6b62 kernel',
      })
    );
    fireEvent.change(screen.getByTestId('stop-reason-input'), {
      target: { value: 'use a cheaper path' },
    });
    fireEvent.click(screen.getByTestId('stop-interrupt'));
    await waitFor(() =>
      expect(mocks.stopKernel).toHaveBeenCalledWith('frame-1', 'kernel-1', {
        mode: 'interrupt',
        reason: 'use a cheaper path',
      })
    );

    const forceButton = await screen.findByTestId('kernel-force-kill', {}, { timeout: 7_000 });
    fireEvent.click(forceButton);
    await waitFor(() =>
      expect(mocks.stopKernel).toHaveBeenLastCalledWith('frame-1', 'kernel-1', {
        mode: 'clear',
        reason: 'use a cheaper path',
        force: true,
      })
    );
  }, 10_000);

  it('opens the reference session actions menu and stops every kernel in that session', async () => {
    await renderPanel(<SynonBiomedComputeRuntimePanel rootFrameId='root-1' projectId='project-1' />);

    const sessionActions = await screen.findAllByRole('button', {
      name: '\u4f1a\u8bdd\u64cd\u4f5c',
    });
    fireEvent.click(sessionActions[0]);
    fireEvent.click(await screen.findByRole('menuitem', { name: '\u5168\u90e8\u505c\u6b62' }));
    expect(
      await screen.findByText('\u505c\u6b62\u6b64\u4f1a\u8bdd\u7684\u6240\u6709 kernel\uff1f')
    ).toBeInTheDocument();
    fireEvent.click(screen.getByTestId('stop-kill'));
    await waitFor(() =>
      expect(mocks.stopKernel).toHaveBeenCalledWith('frame-1', 'kernel-1', {
        mode: 'clear',
        reason: null,
      })
    );
  });

  it('shows the reference empty state when there are no live kernels', async () => {
    mocks.loadAll.mockResolvedValue({
      kernels: [],
      hasHistory: true,
      machine: {
        ...inventory.machine,
        kernelRssBytes: 0,
        kernelCpuPct: 0,
        kernelCount: 0,
      },
    });

    await renderPanel(<SynonBiomedComputeRuntimePanel rootFrameId='root-idle' projectId='project-1' />);

    expect(await screen.findByText('暂无实时 kernel')).toBeInTheDocument();
    expect(screen.getByText('任一会话开始计算后，kernel 会立即显示在这里。')).toBeInTheDocument();
  });

  it('keeps a visible retry path when the global inventory fails', async () => {
    mocks.loadAll.mockRejectedValue(new Error('global inventory unavailable'));

    await renderPanel(<SynonBiomedComputeRuntimePanel rootFrameId='root-error' projectId='project-1' />);

    expect(await screen.findByText('计算资源读取失败')).toBeInTheDocument();
    expect(screen.queryByText('global inventory unavailable')).toBeNull();
    expect(screen.getByRole('button', { name: '刷新计算资源' })).toBeInTheDocument();
  });
});

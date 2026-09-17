import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';
import type { SynonBiomedRuntimeSnapshot } from '@/renderer/components/synonBiomed/runtime/runtimeOperationsModel';

vi.mock('@arco-design/web-react', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@arco-design/web-react')>();
  return {
    ...actual,
    Message: {
      useMessage: () => [{ success: vi.fn(), error: vi.fn(), info: vi.fn() }, null],
    },
  };
});

const runtimeMocks = vi.hoisted(() => ({
  loadSnapshot: vi.fn(),
  loadExecutionLog: vi.fn(),
  resume: vi.fn(),
  resolveInput: vi.fn(),
  resolveAskUser: vi.fn(),
  loadPlan: vi.fn(),
  approvePlan: vi.fn(),
  discardPlan: vi.fn(),
  subscribeInvalidation: vi.fn(() => () => {}),
}));

const realtimeState = vi.hoisted(() => ({ status: 'connected' }));

const frameReadMocks = vi.hoisted(() => ({
  invalidate: vi.fn(),
}));

const annotationMocks = vi.hoisted(() => ({
  loadFrameVerification: vi.fn(),
}));

vi.mock('@/renderer/services/synonBiomedFrameReads', () => ({
  invalidateSynonBiomedFrameReads: frameReadMocks.invalidate,
}));

vi.mock('@/renderer/services/synonBiomedAnnotations', () => ({
  loadSynonBiomedFrameVerification: annotationMocks.loadFrameVerification,
}));

vi.mock('@/renderer/services/synonBiomedRuntimeOperations', () => ({
  loadSynonBiomedRuntimeSnapshot: runtimeMocks.loadSnapshot,
  loadSynonBiomedExecutionLogPage: runtimeMocks.loadExecutionLog,
  resumeSynonBiomedFrame: runtimeMocks.resume,
  resolveSynonBiomedInputRequest: runtimeMocks.resolveInput,
  resolveSynonBiomedAskUserRequest: runtimeMocks.resolveAskUser,
  loadSynonBiomedPlanDocument: runtimeMocks.loadPlan,
  approveSynonBiomedPlan: runtimeMocks.approvePlan,
  discardSynonBiomedPlan: runtimeMocks.discardPlan,
  subscribeSynonBiomedRuntimeInvalidation: runtimeMocks.subscribeInvalidation,
}));

vi.mock('@/renderer/hooks/context/RealtimeContext', () => ({
  useRealtime: () => ({
    runtime: {},
    snapshot: { identity: 'authenticated', status: realtimeState.status },
  }),
}));

import SynonBiomedRuntimeOperations from '@/renderer/components/synonBiomed/runtime/SynonBiomedRuntimeOperations';
import { emitter } from '@/renderer/utils/emitter';

const abortOptions = () => expect.objectContaining({ signal: expect.any(AbortSignal) });

const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
};

const failedSnapshot = {
  frameId: 'frame-failed',
  rootFrameId: 'frame-failed',
  projectId: null,
  agentName: 'OPERON',
  isHidden: false,
  status: 'failed',
  statusDescription: 'Checking safe-mol availability',
  error: 'daemon restarted while frame was running',
  failureKind: 'service_interrupted',
  errorStatus: null,
  modelId: null,
  taskMetrics: null,
  createdAt: '2026-07-11T00:00:00.000Z',
  completedAt: '2026-07-11T00:16:03.905Z',
  updatedAt: '2026-07-11T00:16:03.905Z',
  runtimeElapsedMs: null,
  runtimeObservedAt: null,
  runtimeStartedAt: null,
  runtimeFinishedAt: null,
  runtimeActive: null,
  runtimeAttempt: null,
  runtimeInputRevision: null,
  canCancel: false,
  canResume: true,
  pendingInputRequests: [],
  taskPlan: null,
  planApproval: null,
} as const;

const pendingSnapshot = {
  frameId: 'frame-waiting',
  rootFrameId: 'frame-waiting',
  status: 'processing',
  statusDescription: 'Checking API keys for NVIDIA NIMs',
  error: null,
  updatedAt: '2026-07-11T03:59:15.607Z',
  canCancel: true,
  canResume: false,
  pendingInputRequests: [
    {
      requestId: 'request-1',
      toolId: 'call-1',
      kind: 'local_exec',
      tool: 'python',
      code: 'print("NGC_API_KEY")',
      description: null,
      environment: 'python',
      mode: 'live',
      questions: [],
    },
  ],
  taskPlan: null,
  planApproval: null,
} as const;

const askUserSnapshot = {
  ...pendingSnapshot,
  status: 'awaiting_user_response',
  pendingInputRequests: [
    {
      requestId: 'ask-user-request',
      toolId: 'ask-user-tool',
      kind: 'ask_user',
      tool: 'ask_user',
      code: null,
      description: null,
      environment: null,
      mode: null,
      questions: [
        {
          header: 'Candidate',
          question: 'Choose a candidate',
          multiSelect: false,
          options: [{ label: 'Candidate A', description: 'Public fixture', pros: null, cons: null, smiles: null }],
        },
      ],
    },
  ],
} as const;

const awaitingPlanSnapshot = {
  frameId: 'frame-plan',
  rootFrameId: 'frame-plan',
  status: 'awaiting_plan_approval',
  statusDescription: 'Plan ready for review',
  error: null,
  updatedAt: '2026-07-11T04:30:00.000Z',
  canCancel: true,
  canResume: false,
  pendingInputRequests: [],
  taskPlan: { artifactId: 'artifact-plan', versionId: 'version-plan' },
  planApproval: { artifactId: 'artifact-plan', versionId: 'version-plan' },
} as const;

describe('SynonBiomedRuntimeOperations', () => {
  beforeEach(() => {
    for (const mock of Object.values(runtimeMocks)) mock.mockReset();
    frameReadMocks.invalidate.mockReset();
    annotationMocks.loadFrameVerification.mockReset();
    realtimeState.status = 'connected';
    runtimeMocks.loadExecutionLog.mockResolvedValue({ records: [], nextBefore: null, total: 0 });
    runtimeMocks.subscribeInvalidation.mockImplementation(() => () => {});
  });

  it('renders the permanent status center for an active task', async () => {
    let resolveSnapshot!: (value: SynonBiomedRuntimeSnapshot) => void;
    runtimeMocks.loadSnapshot.mockImplementation(
      () => new Promise<SynonBiomedRuntimeSnapshot>((resolve) => (resolveSnapshot = resolve))
    );

    const onStop = vi.fn();
    const { unmount } = await renderWithI18n(
      <SynonBiomedRuntimeOperations
        conversationId='frame-running'
        runtimeState='idle'
        onResumed={vi.fn()}
        onStop={onStop}
      />
    );

    await act(async () => {
      resolveSnapshot({
        ...failedSnapshot,
        status: 'processing',
        statusDescription: 'Querying public databases',
        error: null,
        failureKind: null,
        taskMetrics: {
          total: {
            inputTokens: 20_000,
            outputTokens: 4_500,
            cacheReadTokens: 2_000,
            cacheWriteTokens: 0,
            totalTokens: 24_500,
            modelCallCount: 3,
            toolCallCount: 9,
          },
          latest: {
            inputTokens: 12_000,
            outputTokens: 3_500,
            cacheReadTokens: 1_000,
            cacheWriteTokens: 0,
            totalTokens: 15_500,
            modelCallCount: 2,
            toolCallCount: 4,
          },
        },
        canCancel: true,
        canResume: false,
      });
    });
    const center = screen.getByTestId('synon-biomed-runtime-status');
    expect(center).toHaveAttribute('data-state', 'running');
    expect(center).toHaveTextContent('任务正在运行');
    expect(center).not.toHaveTextContent('Querying public databases');
    expect(screen.getByTestId('synon-biomed-task-details-trigger')).toBeInTheDocument();
    expect(screen.queryByTestId('synon-biomed-task-status-panel')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '查看执行记录' })).not.toBeInTheDocument();
    fireEvent.click(screen.getByTestId('synon-biomed-task-details-trigger'));
    expect(screen.getByTestId('synon-biomed-task-metric-tokens')).toHaveTextContent('24.5K');
    expect(screen.getByTestId('synon-biomed-task-metric-tokens')).toHaveTextContent('最新一轮 15.5K');
    expect(screen.getByTestId('synon-biomed-task-metric-model-calls')).toHaveTextContent('3');
    expect(screen.getByTestId('synon-biomed-task-metric-model-calls')).toHaveTextContent('最新一轮 2');
    expect(screen.getByTestId('synon-biomed-task-metric-tools')).toHaveTextContent('9');
    expect(screen.getByTestId('synon-biomed-task-metric-tools')).toHaveTextContent('最新一轮 4');
    act(() => fireEvent.click(screen.getByTestId('synon-biomed-task-pause')));
    expect(onStop).toHaveBeenCalledOnce();
    unmount();
  });

  it('reloads the authoritative snapshot after pause and exposes the cohesive resume pill', async () => {
    const cancelledSnapshot = {
      ...failedSnapshot,
      status: 'cancelled',
      statusDescription: 'Stopped by user',
      error: null,
      failureKind: null,
      canCancel: false,
      canResume: true,
    } as const;
    runtimeMocks.loadSnapshot
      .mockResolvedValueOnce({
        ...failedSnapshot,
        status: 'processing',
        statusDescription: 'Running query',
        error: null,
        failureKind: null,
        canCancel: true,
        canResume: false,
      })
      .mockResolvedValueOnce(cancelledSnapshot);
    let resolveStop!: () => void;
    const onStop = vi.fn(() => new Promise<void>((resolve) => (resolveStop = resolve)));

    const { unmount } = await renderWithI18n(
      <SynonBiomedRuntimeOperations
        conversationId='frame-pause'
        runtimeState='running'
        onResumed={vi.fn()}
        onStop={onStop}
      />
    );

    await waitFor(() => expect(screen.getByTestId('synon-biomed-task-pause')).toBeInTheDocument());
    fireEvent.click(screen.getByTestId('synon-biomed-task-pause'));
    expect(onStop).toHaveBeenCalledOnce();
    await act(async () => {
      resolveStop();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    await waitFor(() => expect(onStop).toHaveBeenCalledOnce());
    await waitFor(() => expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(2));
    await waitFor(() => {
      const indicator = screen.getByTestId('synon-biomed-task-status-indicator');
      expect(indicator).toHaveAttribute('data-state', 'paused');
      expect(indicator.tagName).toBe('DIV');
      expect(screen.getByTestId('synon-biomed-task-status-action')).toHaveAccessibleName('继续运行');
    });
    expect(screen.queryByTestId('synon-biomed-task-pause')).not.toBeInTheDocument();
    await act(async () => unmount());
  });

  it('coalesces realtime invalidations and renders only authoritative pending actions', async () => {
    vi.useFakeTimers();
    try {
      const snapshotResolvers: Array<(value: SynonBiomedRuntimeSnapshot) => void> = [];
      let invalidate: (() => void) | undefined;
      runtimeMocks.subscribeInvalidation.mockImplementation((_frameId: string, listener: () => void) => {
        invalidate = listener;
        return vi.fn();
      });
      runtimeMocks.loadSnapshot.mockImplementation(
        () => new Promise<SynonBiomedRuntimeSnapshot>((resolve) => snapshotResolvers.push(resolve))
      );

      const view = await renderWithI18n(
        <SynonBiomedRuntimeOperations conversationId='frame-failed' runtimeState='idle' onResumed={vi.fn()} />
      );
      await act(async () => {
        snapshotResolvers[0]({
          ...failedSnapshot,
          status: 'processing',
          statusDescription: 'Running query',
          error: null,
          failureKind: null,
          canCancel: true,
          canResume: false,
        });
      });
      expect(screen.getByTestId('synon-biomed-runtime-status')).toHaveAttribute('data-state', 'running');
      expect(runtimeMocks.subscribeInvalidation).toHaveBeenCalledWith('frame-failed', expect.any(Function));
      await act(async () => {
        invalidate?.();
        invalidate?.();
        await vi.advanceTimersByTimeAsync(80);
      });
      expect(screen.queryByTestId('synon-biomed-task-status-panel')).not.toBeInTheDocument();
      await act(async () => {
        snapshotResolvers[1]({
          ...failedSnapshot,
          status: 'processing',
          statusDescription: 'Waiting for local execution approval',
          error: null,
          failureKind: null,
          canCancel: true,
          canResume: false,
          pendingInputRequests: pendingSnapshot.pendingInputRequests,
        });
      });
      expect(screen.getByRole('region', { name: '等待操作授权' })).toBeInTheDocument();
      expect(screen.getByTestId('synon-biomed-runtime-status')).toHaveAttribute('data-state', 'waiting_input');
      expect(screen.queryByTestId('synon-biomed-task-status-panel')).not.toBeInTheDocument();
      expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(2);

      await act(async () => {
        invalidate?.();
        await vi.advanceTimersByTimeAsync(80);
      });
      await act(async () => {
        snapshotResolvers[2]({
          ...failedSnapshot,
          status: 'processing',
          statusDescription: 'Running after approval',
          error: null,
          failureKind: null,
          canCancel: true,
          canResume: false,
          pendingInputRequests: [],
        });
      });
      expect(screen.queryByRole('region', { name: '等待操作授权' })).not.toBeInTheDocument();
      expect(screen.getByTestId('synon-biomed-runtime-status')).toHaveAttribute('data-state', 'running');
      expect(screen.queryByTestId('synon-biomed-task-status-panel')).not.toBeInTheDocument();
      expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(3);

      view.unmount();
    } finally {
      vi.useRealTimers();
    }
  });

  it('finishes initial loading when runtime state changes while the authoritative snapshot is pending', async () => {
    const snapshot = deferred<SynonBiomedRuntimeSnapshot>();
    runtimeMocks.loadSnapshot.mockReturnValue(snapshot.promise);

    const view = await renderWithI18n(
      <SynonBiomedRuntimeOperations conversationId='frame-waiting' runtimeState='running' onResumed={vi.fn()} />
    );

    view.rerender(
      <SynonBiomedRuntimeOperations
        conversationId='frame-waiting'
        runtimeState='waiting_confirmation'
        onResumed={vi.fn()}
      />
    );
    expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(1);

    await act(async () => snapshot.resolve(askUserSnapshot as SynonBiomedRuntimeSnapshot));

    expect(screen.getByTestId('synon-biomed-ask-user-card')).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Choose a candidate' })).toBeInTheDocument();
  });

  it('opens a new local continuation before submitting an Ask User answer', async () => {
    const resumedSnapshot = {
      ...askUserSnapshot,
      status: 'processing',
      pendingInputRequests: [],
    } as SynonBiomedRuntimeSnapshot;
    runtimeMocks.loadSnapshot.mockResolvedValue(askUserSnapshot);
    const answer = deferred<{ snapshot: SynonBiomedRuntimeSnapshot; runtime: Record<string, unknown> }>();
    runtimeMocks.resolveAskUser.mockReturnValue(answer.promise);
    const onResumeStarted = vi.fn();
    const onResumeFailed = vi.fn();
    const onResumed = vi.fn();

    await renderWithI18n(
      <SynonBiomedRuntimeOperations
        conversationId='frame-waiting'
        runtimeState='waiting_input'
        onResumeStarted={onResumeStarted}
        onResumeFailed={onResumeFailed}
        onResumed={onResumed}
      />
    );

    fireEvent.click(await screen.findByRole('radio', { name: /Candidate A/ }));
    expect(onResumeStarted).toHaveBeenCalledTimes(1);
    expect(runtimeMocks.resolveAskUser).toHaveBeenCalledTimes(1);
    expect(onResumed).not.toHaveBeenCalled();

    await act(async () =>
      answer.resolve({
        snapshot: resumedSnapshot,
        runtime: {
          state: 'running',
          can_send_message: false,
          has_task: true,
          task_status: 'running',
          is_processing: true,
          pending_confirmations: 0,
          turn_id: 'frame-waiting',
        },
      })
    );
    expect(onResumed).toHaveBeenCalledWith('frame-waiting', expect.objectContaining({ state: 'running' }));
    expect(onResumeFailed).not.toHaveBeenCalled();
  });

  it('releases the local continuation gate when Ask User submission fails', async () => {
    runtimeMocks.loadSnapshot.mockResolvedValue(askUserSnapshot);
    runtimeMocks.resolveAskUser.mockRejectedValue(new Error('network unavailable'));
    const onResumeStarted = vi.fn();
    const onResumeFailed = vi.fn();

    await renderWithI18n(
      <SynonBiomedRuntimeOperations
        conversationId='frame-waiting'
        runtimeState='waiting_input'
        onResumeStarted={onResumeStarted}
        onResumeFailed={onResumeFailed}
        onResumed={vi.fn()}
      />
    );

    fireEvent.click(await screen.findByRole('radio', { name: /Candidate A/ }));
    await waitFor(() => expect(onResumeFailed).toHaveBeenCalledWith('network unavailable'));
    expect(onResumeStarted).toHaveBeenCalledTimes(1);
  });

  it('does not reload a settled snapshot for presentation-only runtime state changes', async () => {
    const snapshot = deferred<SynonBiomedRuntimeSnapshot>();
    runtimeMocks.loadSnapshot.mockReturnValue(snapshot.promise);

    const view = await renderWithI18n(
      <SynonBiomedRuntimeOperations conversationId='frame-stable' runtimeState='running' onResumed={vi.fn()} />
    );
    await waitFor(() => expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(1));
    await act(async () => {
      snapshot.resolve(failedSnapshot);
    });

    await act(async () => {
      view.rerender(
        <SynonBiomedRuntimeOperations
          conversationId='frame-stable'
          runtimeState='waiting_confirmation'
          onResumed={vi.fn()}
        />
      );
    });

    expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(1);
    act(() => view.unmount());
  });

  it('preserves the last confirmed snapshot while showing reconnecting state after a realtime refresh fails', async () => {
    vi.useFakeTimers();
    try {
      let invalidate: (() => void) | undefined;
      let resolveInitial!: (value: SynonBiomedRuntimeSnapshot) => void;
      let rejectRefresh!: (reason: Error) => void;
      runtimeMocks.subscribeInvalidation.mockImplementation((_frameId: string, listener: () => void) => {
        invalidate = listener;
        return vi.fn();
      });
      runtimeMocks.loadSnapshot
        .mockImplementationOnce(() => new Promise<SynonBiomedRuntimeSnapshot>((resolve) => (resolveInitial = resolve)))
        .mockImplementationOnce(
          () => new Promise<SynonBiomedRuntimeSnapshot>((_resolve, reject) => (rejectRefresh = reject))
        );

      const view = await renderWithI18n(
        <SynonBiomedRuntimeOperations conversationId='frame-refresh' runtimeState='running' onResumed={vi.fn()} />
      );
      await act(async () => {
        resolveInitial({
          ...failedSnapshot,
          status: 'processing',
          statusDescription: 'Running verified query',
          error: null,
          failureKind: null,
          canCancel: true,
          canResume: false,
        });
      });

      await act(async () => {
        invalidate?.();
        vi.advanceTimersByTime(80);
        await Promise.resolve();
      });
      expect(screen.getByTestId('synon-biomed-runtime-status')).toHaveAttribute('data-state', 'running');
      expect(screen.getByTestId('synon-biomed-task-status-spinner')).toBeInTheDocument();
      await act(async () => {
        rejectRefresh(new Error('refresh unavailable'));
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(screen.getByTestId('synon-biomed-runtime-status')).toHaveAttribute('data-state', 'loading');
      expect(screen.getByTestId('synon-biomed-runtime-status')).not.toHaveTextContent(/任务正在运行|Task running/i);
      expect(screen.queryByTestId('runtime-snapshot-error')).not.toBeInTheDocument();
      expect(screen.queryByTestId('synon-biomed-task-status-panel')).not.toBeInTheDocument();
      act(() => view.unmount());
    } finally {
      vi.useRealTimers();
    }
  });

  it('never renders a previous conversation snapshot while the next owner is loading', async () => {
    let resolveFirst!: (value: SynonBiomedRuntimeSnapshot) => void;
    let resolveSecond!: (value: SynonBiomedRuntimeSnapshot) => void;
    runtimeMocks.loadSnapshot
      .mockImplementationOnce(() => new Promise<SynonBiomedRuntimeSnapshot>((resolve) => (resolveFirst = resolve)))
      .mockImplementationOnce(() => new Promise<SynonBiomedRuntimeSnapshot>((resolve) => (resolveSecond = resolve)));

    const view = await renderWithI18n(
      <SynonBiomedRuntimeOperations conversationId='frame-owner-a' runtimeState='idle' onResumed={vi.fn()} />
    );
    await act(async () =>
      resolveFirst({
        ...failedSnapshot,
        frameId: 'frame-owner-a',
        rootFrameId: 'frame-owner-a',
        error: 'owner-a-private-failure',
        failureKind: 'generic',
      })
    );
    expect(screen.queryByText('owner-a-private-failure')).not.toBeInTheDocument();

    act(() =>
      view.rerender(
        <SynonBiomedRuntimeOperations conversationId='frame-owner-b' runtimeState='idle' onResumed={vi.fn()} />
      )
    );
    expect(screen.queryByText('owner-a-private-failure')).not.toBeInTheDocument();
    expect(screen.getByTestId('synon-biomed-runtime-status')).toHaveAttribute('data-state', 'loading');

    await waitFor(() => expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(2));
    await act(async () =>
      resolveSecond({
        ...failedSnapshot,
        frameId: 'frame-owner-b',
        rootFrameId: 'frame-owner-b',
        error: 'owner-b-failure',
        failureKind: 'generic',
      })
    );
    expect(screen.queryByText('owner-b-failure')).not.toBeInTheDocument();
  });

  it('aborts an obsolete snapshot request when conversation ownership changes', async () => {
    const snapshotResolvers: Array<(value: SynonBiomedRuntimeSnapshot) => void> = [];
    runtimeMocks.loadSnapshot.mockImplementation(
      () => new Promise<SynonBiomedRuntimeSnapshot>((resolve) => snapshotResolvers.push(resolve))
    );
    const view = await renderWithI18n(
      <SynonBiomedRuntimeOperations conversationId='frame-owner-a' runtimeState='running' onResumed={vi.fn()} />
    );
    const ownerASignal = runtimeMocks.loadSnapshot.mock.calls[0][1].signal as AbortSignal;

    act(() =>
      view.rerender(
        <SynonBiomedRuntimeOperations conversationId='frame-owner-b' runtimeState='running' onResumed={vi.fn()} />
      )
    );
    await waitFor(() => expect(snapshotResolvers).toHaveLength(2));
    expect(ownerASignal.aborted).toBe(true);

    await act(async () => {
      snapshotResolvers[0]({
        ...failedSnapshot,
        frameId: 'frame-owner-a',
        rootFrameId: 'frame-owner-a',
        error: 'owner-a-stale-error',
      });
    });
    expect(screen.queryByText('owner-a-stale-error')).not.toBeInTheDocument();

    await act(async () => {
      snapshotResolvers[1]({
        ...failedSnapshot,
        frameId: 'frame-owner-b',
        rootFrameId: 'frame-owner-b',
        error: 'owner-b-current-error',
        failureKind: 'generic',
      });
    });
    expect(screen.queryByText('owner-b-current-error')).not.toBeInTheDocument();
  });

  it('exposes execution-record actions through task details only', async () => {
    runtimeMocks.loadSnapshot.mockResolvedValue({
      ...pendingSnapshot,
      frameId: 'frame-no-details',
      rootFrameId: 'frame-no-details',
    });

    await renderWithI18n(
      <SynonBiomedRuntimeOperations conversationId='frame-no-details' runtimeState='running' onResumed={vi.fn()} />
    );

    const status = await screen.findByTestId('synon-biomed-runtime-status');
    await waitFor(() => expect(status).toHaveAttribute('data-state', 'waiting_input'));
    expect(screen.getByTestId('synon-biomed-task-details-trigger')).toBeInTheDocument();
    expect(screen.queryByTestId('synon-biomed-task-status-panel')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '查看执行记录' })).not.toBeInTheDocument();
    expect(runtimeMocks.loadExecutionLog).not.toHaveBeenCalled();
  });

  it('never exposes a stale plan document after conversation ownership changes', async () => {
    const snapshotResolvers: Array<(value: SynonBiomedRuntimeSnapshot) => void> = [];
    let resolveOwnerAPlan!: (value: Record<string, unknown>) => void;
    runtimeMocks.loadSnapshot.mockImplementation(
      () => new Promise<SynonBiomedRuntimeSnapshot>((resolve) => snapshotResolvers.push(resolve))
    );
    const snapshotFor = (frameId: string): SynonBiomedRuntimeSnapshot => ({
      ...awaitingPlanSnapshot,
      frameId,
      rootFrameId: frameId,
      taskPlan: { artifactId: `artifact-${frameId}`, versionId: `version-${frameId}` },
      planApproval: { artifactId: `artifact-${frameId}`, versionId: `version-${frameId}` },
    });
    runtimeMocks.loadPlan.mockImplementationOnce(
      () => new Promise<Record<string, unknown>>((resolve) => (resolveOwnerAPlan = resolve))
    );

    const view = await renderWithI18n(
      <SynonBiomedRuntimeOperations
        conversationId='frame-owner-a'
        runtimeState='waiting_confirmation'
        onResumed={vi.fn()}
      />
    );
    await act(async () => snapshotResolvers[0](snapshotFor('frame-owner-a')));
    await act(async () => {
      emitter.emit('synonbiomed.runtime.plan.open', 'frame-owner-a');
      await Promise.resolve();
    });
    await waitFor(() =>
      expect(runtimeMocks.loadPlan).toHaveBeenCalledWith(
        'artifact-frame-owner-a',
        expect.objectContaining({ signal: expect.any(AbortSignal) })
      )
    );
    const ownerAPlanDocumentSignal = runtimeMocks.loadPlan.mock.calls[0][1].signal as AbortSignal;

    act(() => {
      view.rerender(
        <SynonBiomedRuntimeOperations
          conversationId='frame-owner-b'
          runtimeState='waiting_confirmation'
          onResumed={vi.fn()}
        />
      );
    });
    await waitFor(() => expect(snapshotResolvers).toHaveLength(2));
    await act(async () => snapshotResolvers[1](snapshotFor('frame-owner-b')));
    expect(ownerAPlanDocumentSignal.aborted).toBe(true);
    await screen.findByTestId('synon-biomed-runtime-status');
    await act(async () => {
      resolveOwnerAPlan({
        version: 1,
        taskSummary: 'OWNER_A_PRIVATE_PLAN',
        phases: [],
        feasibility: null,
      });
      await Promise.resolve();
    });

    expect(screen.queryByText('OWNER_A_PRIVATE_PLAN')).not.toBeInTheDocument();
  });

  it('aborts an approval submission when conversation ownership changes', async () => {
    const ownerAPending = {
      ...pendingSnapshot,
      frameId: 'frame-owner-a',
      rootFrameId: 'frame-owner-a',
    } as SynonBiomedRuntimeSnapshot;
    const ownerBPending = {
      ...pendingSnapshot,
      frameId: 'frame-owner-b',
      rootFrameId: 'frame-owner-b',
      pendingInputRequests: pendingSnapshot.pendingInputRequests.map((request) => ({
        ...request,
        requestId: 'req-owner-b',
      })),
    } as SynonBiomedRuntimeSnapshot;
    runtimeMocks.loadSnapshot.mockImplementation(async (frameId: string) =>
      frameId === 'frame-owner-a' ? ownerAPending : ownerBPending
    );
    let resolveApproval!: (value: { snapshot: SynonBiomedRuntimeSnapshot; runtime: Record<string, unknown> }) => void;
    runtimeMocks.resolveInput.mockImplementation(
      () =>
        new Promise<{ snapshot: SynonBiomedRuntimeSnapshot; runtime: Record<string, unknown> }>(
          (resolve) => (resolveApproval = resolve)
        )
    );
    const onResumed = vi.fn();
    const view = await renderWithI18n(
      <SynonBiomedRuntimeOperations conversationId='frame-owner-a' runtimeState='running' onResumed={onResumed} />
    );

    fireEvent.click(await screen.findByRole('button', { name: '允许 本次' }));
    await waitFor(() => expect(runtimeMocks.resolveInput).toHaveBeenCalledOnce());
    const ownerASignal = runtimeMocks.resolveInput.mock.calls[0][4].signal as AbortSignal;
    act(() => {
      view.rerender(
        <SynonBiomedRuntimeOperations conversationId='frame-owner-b' runtimeState='idle' onResumed={onResumed} />
      );
    });
    expect(await screen.findByRole('region', { name: '等待操作授权' })).toBeInTheDocument();
    expect(ownerASignal.aborted).toBe(true);
    await act(async () =>
      resolveApproval({
        snapshot: { ...ownerAPending, status: 'processing', pendingInputRequests: [] },
        runtime: { state: 'running' },
      })
    );
    expect(onResumed).not.toHaveBeenCalled();
  });

  it('aborts a plan action when conversation ownership changes', async () => {
    const ownerAPlan = {
      ...awaitingPlanSnapshot,
      frameId: 'frame-owner-a',
      rootFrameId: 'frame-owner-a',
    } as SynonBiomedRuntimeSnapshot;
    const ownerBPending = {
      ...pendingSnapshot,
      frameId: 'frame-owner-b',
      rootFrameId: 'frame-owner-b',
      pendingInputRequests: pendingSnapshot.pendingInputRequests.map((request) => ({
        ...request,
        requestId: 'req-owner-b',
      })),
    } as SynonBiomedRuntimeSnapshot;
    runtimeMocks.loadSnapshot.mockImplementation(async (frameId: string) =>
      frameId === 'frame-owner-a' ? ownerAPlan : ownerBPending
    );
    runtimeMocks.loadPlan.mockResolvedValue({
      version: 1,
      taskSummary: 'Owner A plan',
      phases: [],
      feasibility: null,
    });
    let resolvePlan!: (value: { snapshot: SynonBiomedRuntimeSnapshot; runtime: Record<string, unknown> }) => void;
    runtimeMocks.approvePlan.mockImplementation(
      () =>
        new Promise<{ snapshot: SynonBiomedRuntimeSnapshot; runtime: Record<string, unknown> }>(
          (resolve) => (resolvePlan = resolve)
        )
    );
    const onResumed = vi.fn();
    const view = await renderWithI18n(
      <SynonBiomedRuntimeOperations
        conversationId='frame-owner-a'
        runtimeState='waiting_confirmation'
        onResumed={onResumed}
      />
    );

    await screen.findByTestId('synon-biomed-runtime-status');
    await act(async () => {
      emitter.emit('synonbiomed.runtime.plan.open', 'frame-owner-a');
      await Promise.resolve();
    });
    fireEvent.click(await screen.findByRole('button', { name: '批准并执行' }));
    await waitFor(() => expect(runtimeMocks.approvePlan).toHaveBeenCalledOnce());
    const ownerASignal = runtimeMocks.approvePlan.mock.calls[0][1].signal as AbortSignal;
    act(() => {
      view.rerender(
        <SynonBiomedRuntimeOperations conversationId='frame-owner-b' runtimeState='idle' onResumed={onResumed} />
      );
    });
    expect(await screen.findByRole('region', { name: '等待操作授权' })).toBeInTheDocument();
    expect(ownerASignal.aborted).toBe(true);
    await act(async () =>
      resolvePlan({
        snapshot: { ...ownerAPlan, status: 'processing', planApproval: null },
        runtime: { state: 'running' },
      })
    );
    expect(onResumed).not.toHaveBeenCalled();
  });

  it('uses the connected heartbeat and offline recovery while an active task lacks realtime authority', async () => {
    vi.useFakeTimers();
    try {
      const activeSnapshot = {
        ...failedSnapshot,
        status: 'processing',
        error: null,
        failureKind: null,
        canCancel: true,
        canResume: false,
      } as const;
      let resolveSnapshot!: (value: SynonBiomedRuntimeSnapshot) => void;
      runtimeMocks.loadSnapshot.mockImplementation(
        () => new Promise<SynonBiomedRuntimeSnapshot>((resolve) => (resolveSnapshot = resolve))
      );

      const connectedView = await renderWithI18n(
        <SynonBiomedRuntimeOperations conversationId='frame-connected' runtimeState='running' onResumed={vi.fn()} />
      );
      await act(async () => {
        resolveSnapshot(activeSnapshot);
      });
      expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(1);
      await act(async () => {
        vi.advanceTimersByTime(4999);
        await Promise.resolve();
      });
      expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(1);
      await act(async () => {
        vi.advanceTimersByTime(1);
        await Promise.resolve();
      });
      expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(2);
      act(() => connectedView.unmount());

      realtimeState.status = 'offline';
      runtimeMocks.loadSnapshot.mockClear();
      const offlineView = await renderWithI18n(
        <SynonBiomedRuntimeOperations conversationId='frame-offline' runtimeState='running' onResumed={vi.fn()} />
      );
      await act(async () => {
        resolveSnapshot(activeSnapshot);
      });
      expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(1);
      await act(async () => {
        vi.advanceTimersByTime(2500);
        resolveSnapshot(activeSnapshot);
      });
      expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(2);
      act(() => offlineView.unmount());
    } finally {
      vi.useRealTimers();
    }
  });

  it('refreshes an active runtime snapshot on a bounded connected heartbeat', async () => {
    vi.useFakeTimers();
    try {
      const activeSnapshot = {
        ...failedSnapshot,
        status: 'processing',
        statusDescription: 'Running after the realtime event stream went quiet',
        error: null,
        failureKind: null,
        canCancel: true,
        canResume: false,
      } as const;
      runtimeMocks.loadSnapshot.mockResolvedValue(activeSnapshot);
      const view = await renderWithI18n(
        <SynonBiomedRuntimeOperations
          conversationId='frame-heartbeat'
          runtimeState='waiting_input'
          onResumed={vi.fn()}
        />
      );
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(1);
      await act(async () => vi.advanceTimersByTimeAsync(5000 - 1));
      expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(1);
      await act(async () => vi.advanceTimersByTimeAsync(1));
      expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(2);
      expect(frameReadMocks.invalidate).toHaveBeenCalledWith('frame-heartbeat');
      act(() => view.unmount());
    } finally {
      vi.useRealTimers();
    }
  });

  it('keeps reconciling an active snapshot when the parent gate reaches idle first', async () => {
    vi.useFakeTimers();
    try {
      const activeSnapshot = {
        ...failedSnapshot,
        status: 'processing',
        error: null,
        failureKind: null,
        canCancel: true,
        canResume: false,
      } as const;
      const completedSnapshot = {
        ...activeSnapshot,
        status: 'completed',
        completedAt: '2026-07-11T04:05:00.000Z',
        canCancel: false,
      } as const;
      runtimeMocks.loadSnapshot.mockResolvedValueOnce(activeSnapshot).mockResolvedValueOnce(completedSnapshot);
      const onRuntimeUpdated = vi.fn();
      const onResumed = vi.fn();
      const view = await renderWithI18n(
        <SynonBiomedRuntimeOperations
          conversationId='frame-terminal-race'
          runtimeState='running'
          onResumed={onResumed}
          onRuntimeUpdated={onRuntimeUpdated}
        />
      );
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(1);

      act(() => {
        view.rerender(
          <SynonBiomedRuntimeOperations
            conversationId='frame-terminal-race'
            runtimeState='idle'
            onResumed={onResumed}
            onRuntimeUpdated={onRuntimeUpdated}
          />
        );
      });
      await act(async () => vi.advanceTimersByTimeAsync(5000));

      expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(2);
      expect(onRuntimeUpdated).toHaveBeenCalledWith(
        completedSnapshot.rootFrameId,
        expect.objectContaining({ state: 'idle', task_status: 'finished', is_processing: false }),
        'refresh'
      );
      await act(async () => vi.advanceTimersByTimeAsync(5000));
      expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(2);
      act(() => view.unmount());
    } finally {
      vi.useRealTimers();
    }
  });

  it('refreshes immediately when a terminal realtime status arrives', async () => {
    vi.useFakeTimers();
    try {
      const activeSnapshot = {
        ...failedSnapshot,
        status: 'processing',
        error: null,
        failureKind: null,
        canCancel: true,
        canResume: false,
      } as const;
      const completedSnapshot = {
        ...activeSnapshot,
        status: 'completed',
        completedAt: '2026-07-11T04:05:00.000Z',
        canCancel: false,
      } as const;
      let invalidate: ((event?: { terminal_status?: 'completed' | 'failed' | 'cancelled' }) => void) | undefined;
      runtimeMocks.subscribeInvalidation.mockImplementation((_frameId: string, listener: typeof invalidate) => {
        invalidate = listener;
        return vi.fn();
      });
      runtimeMocks.loadSnapshot.mockResolvedValueOnce(activeSnapshot).mockResolvedValueOnce(completedSnapshot);
      const onRuntimeUpdated = vi.fn();
      const view = await renderWithI18n(
        <SynonBiomedRuntimeOperations
          conversationId='frame-terminal-event'
          runtimeState='running'
          onResumed={vi.fn()}
          onRuntimeUpdated={onRuntimeUpdated}
        />
      );
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(1);

      await act(async () => {
        invalidate?.({ terminal_status: 'completed' });
        await vi.advanceTimersByTimeAsync(0);
      });

      expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(2);
      expect(screen.getByTestId('synon-biomed-runtime-status')).toHaveAttribute('data-state', 'completed');
      expect(onRuntimeUpdated).toHaveBeenLastCalledWith(
        completedSnapshot.rootFrameId,
        expect.objectContaining({ state: 'idle', task_status: 'finished', is_processing: false }),
        'refresh'
      );
      act(() => view.unmount());
    } finally {
      vi.useRealTimers();
    }
  });

  it('publishes authoritative review activity for duplicate-action gating', async () => {
    const reviewingSnapshot = {
      ...failedSnapshot,
      status: 'completed',
      error: null,
      failureKind: null,
      runtimeStage: 'reviewing',
      reviewStatus: 'processing',
      reviewKind: 'scientific',
      reviewProfile: 'MEDCHEM_EXPERT',
      reviewFrameId: 'review-frame',
      reviewDescription: null,
      reviewTrigger: 'manual',
      reviewVerdict: null,
      reviewIssueCount: 0,
      reviewBlockingIssueCount: 0,
      canCancel: false,
      canResume: false,
    } as const;
    const completedSnapshot = {
      ...reviewingSnapshot,
      runtimeStage: 'review_completed',
      reviewStatus: 'completed',
      reviewVerdict: 'pass',
    } as const;
    let invalidate: ((event?: { terminal_status?: 'completed' | 'failed' | 'cancelled' }) => void) | undefined;
    runtimeMocks.subscribeInvalidation.mockImplementation((_frameId: string, listener: typeof invalidate) => {
      invalidate = listener;
      return vi.fn();
    });
    runtimeMocks.loadSnapshot.mockResolvedValueOnce(reviewingSnapshot).mockResolvedValueOnce(completedSnapshot);
    const onReviewActivityChange = vi.fn();

    await renderWithI18n(
      <SynonBiomedRuntimeOperations
        conversationId='frame-review-activity'
        runtimeState='idle'
        onResumed={vi.fn()}
        onReviewActivityChange={onReviewActivityChange}
      />
    );
    await waitFor(() => expect(onReviewActivityChange).toHaveBeenCalledWith(true));

    await act(async () => {
      invalidate?.({ terminal_status: 'completed' });
      await Promise.resolve();
    });

    await waitFor(() => expect(onReviewActivityChange).toHaveBeenLastCalledWith(false));
  });

  it('opens the durable passing review record from completed task details', async () => {
    runtimeMocks.loadSnapshot.mockResolvedValue({
      ...failedSnapshot,
      frameId: 'frame-reviewed',
      rootFrameId: 'frame-reviewed',
      status: 'completed',
      statusDescription: null,
      error: null,
      failureKind: null,
      runtimeStage: 'review_completed',
      reviewStatus: 'completed',
      reviewKind: 'runner_completion_review',
      reviewProfile: 'STANDARD',
      reviewFrameId: 'completion-review-1',
      reviewDescription: 'Manual review completed',
      reviewTrigger: 'manual',
      reviewVerdict: 'pass',
      reviewIssueCount: 0,
      reviewBlockingIssueCount: 0,
      canResume: false,
    });
    annotationMocks.loadFrameVerification.mockResolvedValue({
      checks: [
        {
          id: 'review-check-1',
          rootFrameId: 'frame-reviewed',
          artifactVersionId: null,
          claimId: null,
          claim: 'The delivered report passed the completion review.',
          verdict: 'pass',
          severity: null,
          evidence: 'The reviewer verified the report and its source inventory.',
          rebuttal: null,
          reviewerIndex: 0,
          reviewerModel: 'reviewer-model',
          reviewerFrameId: 'completion-review-1',
          sourceRef: null,
          status: 'resolved',
          reflagCount: null,
          createdAt: '2026-09-12T11:14:12Z',
        },
      ],
      claims: [],
      running: [],
    });

    await renderWithI18n(
      <SynonBiomedRuntimeOperations conversationId='frame-reviewed' runtimeState='idle' onResumed={vi.fn()} />
    );
    await waitFor(() =>
      expect(screen.getByTestId('synon-biomed-runtime-status')).toHaveAttribute('data-state', 'completed-reviewed')
    );
    fireEvent.click(screen.getByTestId('synon-biomed-task-details-trigger'));
    fireEvent.click(screen.getByTestId('synon-biomed-task-open-review'));

    expect(await screen.findByText('The delivered report passed the completion review.')).toBeInTheDocument();
    expect(annotationMocks.loadFrameVerification).toHaveBeenCalledWith(
      'frame-reviewed',
      undefined,
      expect.any(Function)
    );
  });

  it('publishes the initial authority and a changed heartbeat runtime to the parent conversation gate', async () => {
    vi.useFakeTimers();
    try {
      const activeSnapshot = {
        ...failedSnapshot,
        status: 'processing',
        error: null,
        failureKind: null,
        canCancel: true,
        canResume: false,
      } as const;
      const completedSnapshot = {
        ...activeSnapshot,
        status: 'completed',
        completedAt: '2026-07-11T04:05:00.000Z',
        canCancel: false,
        canResume: false,
      } as const;
      runtimeMocks.loadSnapshot.mockResolvedValueOnce(activeSnapshot).mockResolvedValueOnce(completedSnapshot);
      const onRuntimeUpdated = vi.fn();
      const view = await renderWithI18n(
        <SynonBiomedRuntimeOperations
          conversationId='frame-heartbeat-parent'
          runtimeState='running'
          onResumed={vi.fn()}
          onRuntimeUpdated={onRuntimeUpdated}
        />
      );
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(1);
      expect(onRuntimeUpdated).toHaveBeenCalledWith(
        activeSnapshot.rootFrameId,
        expect.objectContaining({ state: 'running', task_status: 'running', is_processing: true }),
        'initial'
      );
      await act(async () => vi.advanceTimersByTimeAsync(5000));
      expect(onRuntimeUpdated).toHaveBeenLastCalledWith(
        'frame-failed',
        expect.objectContaining({ state: 'idle', task_status: 'finished', is_processing: false }),
        'refresh'
      );
      act(() => view.unmount());
    } finally {
      vi.useRealTimers();
    }
  });

  it('keeps realtime recovery silent while preserving background state refresh', async () => {
    realtimeState.status = 'protocol-error';
    let resolveSnapshot!: (value: SynonBiomedRuntimeSnapshot) => void;
    runtimeMocks.loadSnapshot.mockImplementation(
      () => new Promise<SynonBiomedRuntimeSnapshot>((resolve) => (resolveSnapshot = resolve))
    );

    const view = await renderWithI18n(
      <SynonBiomedRuntimeOperations conversationId='frame-protocol' runtimeState='running' onResumed={vi.fn()} />
    );
    expect(screen.queryByTestId('runtime-realtime-protocol-error')).not.toBeInTheDocument();
    expect(screen.queryByText('实时更新暂不可用')).not.toBeInTheDocument();

    act(() => view.unmount());
    resolveSnapshot(failedSnapshot);
  });

  it('never overlaps offline recovery requests when the backend is slow', async () => {
    vi.useFakeTimers();
    try {
      realtimeState.status = 'reconnecting';
      const activeSnapshot = {
        ...failedSnapshot,
        status: 'processing',
        error: null,
        failureKind: null,
        canCancel: true,
        canResume: false,
      } as const;
      const snapshotResolvers: Array<(value: SynonBiomedRuntimeSnapshot) => void> = [];
      runtimeMocks.loadSnapshot.mockImplementation(
        () => new Promise<SynonBiomedRuntimeSnapshot>((resolve) => snapshotResolvers.push(resolve))
      );

      const view = await renderWithI18n(
        <SynonBiomedRuntimeOperations conversationId='frame-slow' runtimeState='running' onResumed={vi.fn()} />
      );
      await act(async () => {
        snapshotResolvers[0](activeSnapshot);
      });

      await act(async () => vi.advanceTimersByTimeAsync(2500));
      expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(2);
      await act(async () => vi.advanceTimersByTimeAsync(7500));
      expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(2);

      await act(async () => snapshotResolvers[1](activeSnapshot));
      await act(async () => vi.advanceTimersByTimeAsync(2499));
      expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(2);
      await act(async () => vi.advanceTimersByTimeAsync(1));
      expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(3);
      act(() => view.unmount());
    } finally {
      vi.useRealTimers();
    }
  });

  it('backs off offline recovery failures and resets the interval after success', async () => {
    vi.useFakeTimers();
    try {
      realtimeState.status = 'offline';
      const activeSnapshot = {
        ...failedSnapshot,
        status: 'processing',
        error: null,
        failureKind: null,
        canCancel: true,
        canResume: false,
      } as const;
      let resolveInitial!: (value: SynonBiomedRuntimeSnapshot) => void;
      runtimeMocks.loadSnapshot
        .mockImplementationOnce(() => new Promise<SynonBiomedRuntimeSnapshot>((resolve) => (resolveInitial = resolve)))
        .mockRejectedValueOnce(new Error('network unavailable'))
        .mockResolvedValue(activeSnapshot);

      const view = await renderWithI18n(
        <SynonBiomedRuntimeOperations conversationId='frame-backoff' runtimeState='running' onResumed={vi.fn()} />
      );
      await act(async () => {
        resolveInitial(activeSnapshot);
      });
      expect(screen.queryByTestId('long-task-status-running')).not.toBeInTheDocument();

      await act(async () => vi.advanceTimersByTimeAsync(2500));
      expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(2);
      await act(async () => vi.advanceTimersByTimeAsync(2500));
      expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(2);
      await act(async () => vi.advanceTimersByTimeAsync(2500));
      expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(3);
      await act(async () => vi.advanceTimersByTimeAsync(2499));
      expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(3);
      await act(async () => vi.advanceTimersByTimeAsync(1));
      expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(4);

      act(() => view.unmount());
    } finally {
      vi.useRealTimers();
    }
  });

  it('keeps a cancelled task visible and resumes it from the single status center', async () => {
    let resolveSnapshot!: (value: SynonBiomedRuntimeSnapshot) => void;
    const cancelledSnapshot = {
      ...failedSnapshot,
      status: 'cancelled',
      statusDescription: 'Stopped by user',
      error: null,
      failureKind: null,
    } as const;
    runtimeMocks.loadSnapshot.mockImplementation(
      () => new Promise<SynonBiomedRuntimeSnapshot>((resolve) => (resolveSnapshot = resolve))
    );
    runtimeMocks.resume.mockResolvedValue({
      snapshot: {
        ...cancelledSnapshot,
        status: 'processing',
        statusDescription: 'Continuing with the currently selected model',
        canCancel: true,
        canResume: false,
      },
      runtime: {
        state: 'running',
        can_send_message: false,
        has_task: true,
        task_status: 'running',
        is_processing: true,
        pending_confirmations: 0,
        turn_id: 'frame-failed',
      },
    });
    const onResumeStarted = vi.fn();
    const onResumed = vi.fn();
    await renderWithI18n(
      <SynonBiomedRuntimeOperations
        conversationId='frame-failed'
        runtimeState='idle'
        onResumeStarted={onResumeStarted}
        onResumed={onResumed}
      />
    );

    await act(async () => {
      resolveSnapshot(cancelledSnapshot);
    });
    expect(screen.getByTestId('synon-biomed-runtime-status')).toHaveAttribute('data-state', 'cancelled');
    expect(screen.getByText('任务已暂停')).toBeInTheDocument();
    expect(screen.queryByText('任务运行失败')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '继续运行' }));
    await waitFor(() =>
      expect(runtimeMocks.resume).toHaveBeenCalledWith(
        'frame-failed',
        {},
        expect.objectContaining({ signal: expect.any(AbortSignal) })
      )
    );
    expect(onResumeStarted).toHaveBeenCalledOnce();
    expect(onResumed).toHaveBeenCalledWith('frame-failed', expect.objectContaining({ state: 'running' }));
  });

  it('keeps a completed task capsule with details closed by default', async () => {
    const completedSnapshot = {
      ...failedSnapshot,
      status: 'completed',
      statusDescription: 'All requested checks completed',
      error: null,
      failureKind: null,
      canCancel: false,
      canResume: false,
    } as const;
    let resolveSnapshot!: (value: SynonBiomedRuntimeSnapshot) => void;
    runtimeMocks.loadSnapshot.mockImplementation(
      () => new Promise<SynonBiomedRuntimeSnapshot>((resolve) => (resolveSnapshot = resolve))
    );

    await renderWithI18n(
      <SynonBiomedRuntimeOperations conversationId='frame-completed' runtimeState='idle' onResumed={vi.fn()} />
    );
    await act(async () => {
      resolveSnapshot(completedSnapshot);
    });

    expect(screen.getByTestId('synon-biomed-runtime-status')).toHaveAttribute('data-state', 'completed');
    expect(screen.getByText('任务已完成')).toBeInTheDocument();
    expect(screen.queryByText('All requested checks completed')).not.toBeInTheDocument();
    expect(screen.queryByText('任务运行失败')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '查看执行记录' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '继续运行' })).not.toBeInTheDocument();
    expect(screen.getByTestId('synon-biomed-task-details-trigger')).toBeInTheDocument();
    expect(screen.queryByTestId('synon-biomed-task-status-panel')).not.toBeInTheDocument();
  });

  it('does not flash a stale completed capsule after a new run is accepted', async () => {
    const staleSnapshot = {
      ...failedSnapshot,
      status: 'completed',
      statusDescription: 'Previous task completed',
      error: null,
      failureKind: null,
      canCancel: false,
      canResume: false,
      runtimeAttempt: null,
    } as const;
    let resolveSnapshot!: (value: SynonBiomedRuntimeSnapshot) => void;
    runtimeMocks.loadSnapshot.mockImplementation(
      () => new Promise<SynonBiomedRuntimeSnapshot>((resolve) => (resolveSnapshot = resolve))
    );

    await renderWithI18n(
      <SynonBiomedRuntimeOperations conversationId='frame-new-run' runtimeState='starting' onResumed={vi.fn()} />
    );
    await act(async () => {
      resolveSnapshot(staleSnapshot);
    });

    expect(screen.getByTestId('synon-biomed-runtime-status')).toHaveAttribute('data-state', 'starting');
    expect(screen.getByText('任务正在启动')).toBeInTheDocument();
    expect(screen.getByTestId('synon-biomed-task-status-spinner')).toBeInTheDocument();
    expect(screen.queryByText('任务已完成')).not.toBeInTheDocument();
  });

  it.each([
    ['processing', 'running', { ...failedSnapshot, status: 'processing', error: null, failureKind: null }],
    ['completed', 'completed', { ...failedSnapshot, status: 'completed', error: null, failureKind: null }],
    ['cancelled', 'cancelled', { ...failedSnapshot, status: 'cancelled', error: null, failureKind: null }],
    ['failed', 'failed', { ...failedSnapshot, status: 'failed' }],
    ['unknown', 'unavailable', { ...failedSnapshot, status: 'future_runtime_state', error: null, failureKind: null }],
  ])('renders one permanent status center for %s snapshots', async (_label, expectedState, nextSnapshot) => {
    runtimeMocks.loadSnapshot.mockResolvedValue(nextSnapshot as SynonBiomedRuntimeSnapshot);

    await renderWithI18n(
      <SynonBiomedRuntimeOperations conversationId='frame-terminal' runtimeState='idle' onResumed={vi.fn()} />
    );

    await waitFor(() =>
      expect(screen.getByTestId('synon-biomed-runtime-status')).toHaveAttribute('data-state', expectedState)
    );
    expect(screen.getAllByTestId('synon-biomed-runtime-status')).toHaveLength(1);
  });

  it('shows a safe unavailable status when runtime snapshot loading fails', async () => {
    runtimeMocks.loadSnapshot.mockRejectedValue(new Error('runtime failed with key sk-ant-api03-shouldNotLeak123456'));

    await renderWithI18n(
      <SynonBiomedRuntimeOperations conversationId='frame-load-failed' runtimeState='idle' onResumed={vi.fn()} />
    );

    await waitFor(() => expect(runtimeMocks.loadSnapshot).toHaveBeenCalledOnce());
    expect(screen.getByTestId('synon-biomed-runtime-status')).toHaveAttribute('data-state', 'unavailable');
    expect(screen.queryByTestId('runtime-snapshot-error')).not.toBeInTheDocument();
    expect(screen.queryByTestId('synon-biomed-task-status-panel')).not.toBeInTheDocument();
    expect(screen.queryByText('shouldNotLeak')).not.toBeInTheDocument();
  });

  it('keeps an active runtime in neutral confirmation when its first snapshot fails transiently', async () => {
    runtimeMocks.loadSnapshot.mockRejectedValue(new Error('temporary runtime failure'));

    await renderWithI18n(
      <SynonBiomedRuntimeOperations
        conversationId='frame-active-load-failed'
        runtimeState='running'
        onResumed={vi.fn()}
      />
    );

    await waitFor(() => expect(runtimeMocks.loadSnapshot).toHaveBeenCalledOnce());
    expect(screen.getByTestId('synon-biomed-runtime-status')).toHaveAttribute('data-state', 'loading');
    expect(screen.getByTestId('synon-biomed-task-status-spinner')).toBeInTheDocument();
    expect(screen.getByTestId('synon-biomed-task-status-indicator')).toHaveAttribute('role', 'status');
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('renders the local-exec approval card and defaults to an explicit once-only grant', async () => {
    runtimeMocks.loadSnapshot.mockResolvedValue(pendingSnapshot);
    runtimeMocks.resolveInput.mockResolvedValue({
      snapshot: { ...pendingSnapshot, pendingInputRequests: [] },
      runtime: {
        state: 'running',
        can_send_message: false,
        has_task: true,
        task_status: 'running',
        is_processing: true,
        pending_confirmations: 0,
        turn_id: 'frame-waiting',
      },
    });
    const onResumed = vi.fn();

    await renderWithI18n(
      <SynonBiomedRuntimeOperations conversationId='frame-waiting' runtimeState='running' onResumed={onResumed} />
    );

    const region = await screen.findByRole('region', { name: '等待操作授权' });
    expect(region).toHaveTextContent('运行 Python 代码？');
    expect(region).toHaveTextContent('在隔离环境中运行。');
    expect(region).toHaveTextContent('print("NGC_API_KEY")');
    expect(screen.getByRole('button', { name: '允许 本次' })).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '允许 本次' }));
    await waitFor(() =>
      expect(runtimeMocks.resolveInput).toHaveBeenCalledWith(
        'frame-waiting',
        pendingSnapshot.pendingInputRequests[0],
        'allow',
        'once',
        abortOptions()
      )
    );
    expect(onResumed).toHaveBeenCalledWith('frame-waiting', expect.objectContaining({ state: 'running' }));
  });

  it('does not cancel an outstanding approval or issue a second decision on rapid clicks', async () => {
    runtimeMocks.loadSnapshot.mockResolvedValue(pendingSnapshot);
    const request = deferred<unknown>();
    runtimeMocks.resolveInput.mockReturnValue(request.promise);
    await renderWithI18n(
      <SynonBiomedRuntimeOperations conversationId='frame-waiting' runtimeState='running' onResumed={vi.fn()} />
    );
    const allow = await screen.findByRole('button', { name: '允许 本次' });
    fireEvent.click(allow);
    fireEvent.click(allow);
    fireEvent.click(screen.getByRole('button', { name: '拒绝' }));
    expect(runtimeMocks.resolveInput).toHaveBeenCalledTimes(1);
    const options = runtimeMocks.resolveInput.mock.calls[0][4];
    expect(options.signal.aborted).toBe(false);
    await act(async () =>
      request.resolve({
        snapshot: { ...pendingSnapshot, pendingInputRequests: [] },
        runtime: { state: 'running', can_send_message: false, has_task: true },
      })
    );
  });

  it('supports v1.1 once scope without a persistent grant', async () => {
    runtimeMocks.loadSnapshot.mockResolvedValue(pendingSnapshot);
    runtimeMocks.resolveInput.mockResolvedValue({
      snapshot: { ...pendingSnapshot, pendingInputRequests: [] },
      runtime: {
        state: 'running',
        can_send_message: false,
        has_task: true,
        task_status: 'running',
        is_processing: true,
        pending_confirmations: 0,
        turn_id: 'frame-waiting',
      },
    });

    await renderWithI18n(
      <SynonBiomedRuntimeOperations conversationId='frame-waiting' runtimeState='running' onResumed={vi.fn()} />
    );

    await screen.findByRole('region', { name: '等待操作授权' });
    fireEvent.click(screen.getByRole('button', { name: '更改允许范围' }));
    fireEvent.click(await screen.findByRole('menuitemradio', { name: '本次 仅本次调用' }));
    fireEvent.click(await screen.findByRole('button', { name: '允许 本次' }));

    await waitFor(() =>
      expect(runtimeMocks.resolveInput).toHaveBeenCalledWith(
        'frame-waiting',
        pendingSnapshot.pendingInputRequests[0],
        'allow',
        'once',
        abortOptions()
      )
    );
  });

  it('submits a project grant from the single approval action', async () => {
    const errorSpy = vi.spyOn(console, 'error');
    runtimeMocks.loadSnapshot.mockResolvedValue(pendingSnapshot);
    runtimeMocks.resolveInput.mockResolvedValue({
      snapshot: { ...pendingSnapshot, pendingInputRequests: [] },
      runtime: {
        state: 'running',
        can_send_message: false,
        has_task: true,
        task_status: 'running',
        is_processing: true,
        pending_confirmations: 0,
        turn_id: 'frame-waiting',
      },
    });

    await renderWithI18n(
      <SynonBiomedRuntimeOperations conversationId='frame-waiting' runtimeState='running' onResumed={vi.fn()} />
    );

    await screen.findByRole('region', { name: '等待操作授权' });
    fireEvent.click(screen.getByRole('button', { name: '更改允许范围' }));
    fireEvent.click(await screen.findByRole('menuitemradio', { name: '本项目 在本项目中记住' }));
    fireEvent.click(await screen.findByRole('button', { name: '允许 本项目' }));

    await waitFor(() =>
      expect(runtimeMocks.resolveInput).toHaveBeenCalledWith(
        'frame-waiting',
        pendingSnapshot.pendingInputRequests[0],
        'allow',
        'project',
        abortOptions()
      )
    );
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
    expect(errorSpy).not.toHaveBeenCalled();
  });

  it('reconciles a stale approval card after the backend returns a 409', async () => {
    const staleApprovalError = Object.assign(new Error('stale approval'), {
      name: 'SynonBiomedHttpError',
      status: 409,
      code: 'INPUT_REQUEST_NOT_ACTIVE',
    });
    const settledSnapshot = {
      ...pendingSnapshot,
      status: 'processing',
      statusDescription: 'Continuing after the approval request settled',
      pendingInputRequests: [],
    } as SynonBiomedRuntimeSnapshot;
    runtimeMocks.loadSnapshot.mockResolvedValueOnce(pendingSnapshot).mockResolvedValueOnce(settledSnapshot);
    runtimeMocks.resolveInput.mockRejectedValue(staleApprovalError);
    const onResumed = vi.fn();

    await renderWithI18n(
      <SynonBiomedRuntimeOperations conversationId='frame-waiting' runtimeState='running' onResumed={onResumed} />
    );

    await screen.findByRole('region', { name: '等待操作授权' });
    fireEvent.click(screen.getByRole('button', { name: '允许 本次' }));

    await waitFor(() => expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(2));
    expect(screen.queryByRole('region', { name: '等待操作授权' })).not.toBeInTheDocument();
    expect(onResumed).toHaveBeenCalledWith('frame-waiting', expect.objectContaining({ state: 'running' }));
  });

  it('submits a global grant from the single approval action', async () => {
    runtimeMocks.loadSnapshot.mockResolvedValue(pendingSnapshot);
    runtimeMocks.resolveInput.mockResolvedValue({
      snapshot: { ...pendingSnapshot, pendingInputRequests: [] },
      runtime: {
        state: 'running',
        can_send_message: false,
        has_task: true,
        task_status: 'running',
        is_processing: true,
        pending_confirmations: 0,
        turn_id: 'frame-waiting',
      },
    });

    await renderWithI18n(
      <SynonBiomedRuntimeOperations conversationId='frame-waiting' runtimeState='running' onResumed={vi.fn()} />
    );

    await screen.findByRole('region', { name: '等待操作授权' });
    fireEvent.click(screen.getByRole('button', { name: '更改允许范围' }));
    fireEvent.click(await screen.findByRole('menuitemradio', { name: '全局 跨所有项目记住' }));
    fireEvent.click(await screen.findByRole('button', { name: '允许 全局' }));

    await waitFor(() =>
      expect(runtimeMocks.resolveInput).toHaveBeenCalledWith(
        'frame-waiting',
        pendingSnapshot.pendingInputRequests[0],
        'allow',
        'always',
        abortOptions()
      )
    );
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
  });

  it('clears approval scope UI when a different pending request becomes authoritative', async () => {
    let invalidate: (() => void) | undefined;
    const nextLoad = deferred<SynonBiomedRuntimeSnapshot>();
    const nextSnapshot = {
      ...pendingSnapshot,
      updatedAt: '2026-07-11T04:00:15.607Z',
      pendingInputRequests: [
        {
          ...pendingSnapshot.pendingInputRequests[0],
          requestId: 'request-2',
          toolId: 'call-2',
        },
      ],
    } as SynonBiomedRuntimeSnapshot;
    runtimeMocks.subscribeInvalidation.mockImplementation((_frameId: string, listener: () => void) => {
      invalidate = listener;
      return vi.fn();
    });
    runtimeMocks.loadSnapshot.mockResolvedValueOnce(pendingSnapshot).mockReturnValueOnce(nextLoad.promise);
    runtimeMocks.resolveInput.mockResolvedValue({
      snapshot: { ...pendingSnapshot, pendingInputRequests: [] },
      runtime: {
        state: 'running',
        can_send_message: false,
        has_task: true,
        task_status: 'running',
        is_processing: true,
        pending_confirmations: 0,
        turn_id: 'frame-waiting',
      },
    });

    await renderWithI18n(
      <SynonBiomedRuntimeOperations conversationId='frame-waiting' runtimeState='running' onResumed={vi.fn()} />
    );

    await screen.findByRole('region', { name: '等待操作授权' });
    fireEvent.click(screen.getByRole('button', { name: '更改允许范围' }));
    fireEvent.click(await screen.findByRole('menuitemradio', { name: '本项目 在本项目中记住' }));
    fireEvent.click(await screen.findByRole('button', { name: '允许 本项目' }));
    await waitFor(() =>
      expect(runtimeMocks.resolveInput).toHaveBeenCalledWith(
        'frame-waiting',
        pendingSnapshot.pendingInputRequests[0],
        'allow',
        'project',
        abortOptions()
      )
    );
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();

    vi.useFakeTimers();
    try {
      await act(async () => {
        invalidate?.();
        await vi.advanceTimersByTimeAsync(80);
      });
      expect(runtimeMocks.loadSnapshot).toHaveBeenCalledTimes(2);
      await act(async () => nextLoad.resolve(nextSnapshot));

      expect(screen.getByRole('button', { name: '允许 本次' })).toBeInTheDocument();
      expect(screen.getByRole('button', { name: '更改允许范围' })).toHaveAttribute('aria-expanded', 'false');
    } finally {
      vi.useRealTimers();
    }
  });

  it('renders ask_user as a native question card and submits the typed answer contract', async () => {
    const askSnapshot = {
      ...pendingSnapshot,
      frameId: 'frame-ask',
      rootFrameId: 'frame-ask',
      status: 'awaiting_user_response',
      pendingInputRequests: [
        {
          requestId: 'request-ask-1',
          toolId: 'toolu_ask_1',
          kind: 'ask_user',
          tool: 'ask_user',
          code: null,
          description: null,
          environment: null,
          mode: null,
          questions: [
            {
              header: 'Assay endpoint',
              question: 'Which endpoint should be primary?',
              multiSelect: false,
              options: [
                {
                  label: 'pIC50',
                  description: 'Log potency',
                  pros: null,
                  cons: null,
                  smiles: null,
                },
                {
                  label: 'IC50',
                  description: 'Raw potency',
                  pros: null,
                  cons: null,
                  smiles: null,
                },
              ],
            },
          ],
        },
      ],
    } as const;
    runtimeMocks.loadSnapshot.mockResolvedValue(askSnapshot);
    runtimeMocks.resolveAskUser.mockResolvedValue({
      snapshot: {
        ...askSnapshot,
        status: 'processing',
        pendingInputRequests: [],
      },
      runtime: {
        state: 'running',
        can_send_message: false,
        has_task: true,
        task_status: 'running',
        is_processing: true,
        pending_confirmations: 0,
        turn_id: 'frame-ask',
      },
    });
    const onResumed = vi.fn();
    await renderWithI18n(
      <SynonBiomedRuntimeOperations conversationId='frame-ask' runtimeState='running' onResumed={onResumed} />
    );

    const card = await screen.findByTestId('synon-biomed-ask-user-card');
    expect(card).toHaveTextContent('Which endpoint should be primary?');
    expect(screen.queryByRole('region', { name: '运行控制' })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('radio', { name: /pIC50/ }));

    await waitFor(() =>
      expect(runtimeMocks.resolveAskUser).toHaveBeenCalledWith(
        'frame-ask',
        askSnapshot.pendingInputRequests[0],
        {
          action: 'answer',
          answers: { 'Which endpoint should be primary?': 'pIC50' },
        },
        abortOptions()
      )
    );
    expect(onResumed).toHaveBeenCalledWith('frame-ask', expect.objectContaining({ state: 'running' }));
  });

  it('opens plan review from its dedicated runtime event, not the task capsule', async () => {
    runtimeMocks.loadSnapshot.mockResolvedValue(awaitingPlanSnapshot);
    runtimeMocks.loadPlan.mockResolvedValue({
      version: 3,
      taskSummary: 'Characterize STAT6 binding pockets',
      phases: [
        {
          id: 'phase-0',
          name: 'Plan',
          delegations: [
            {
              id: 'delegation-0',
              name: 'Structure review',
              agentName: 'OPERON',
              steps: [
                {
                  id: 'phase-0-0',
                  title: 'Fetch structures',
                  description: 'Load curated PDB structures.',
                },
              ],
            },
          ],
          steps: [
            {
              id: 'phase-0-0',
              title: 'Fetch structures',
              description: 'Load curated PDB structures.',
            },
            {
              id: 'phase-0-standalone',
              title: 'Confirm outputs',
              description: 'Keep phase-owned work visible alongside delegated steps.',
            },
          ],
        },
      ],
      feasibility: {
        confidence: 'high',
        rationale: 'Required public structures are available.',
      },
    });
    runtimeMocks.approvePlan.mockResolvedValue({
      snapshot: {
        ...awaitingPlanSnapshot,
        status: 'processing',
        planApproval: null,
      },
      runtime: {
        state: 'running',
        can_send_message: false,
        has_task: true,
        task_status: 'running',
        is_processing: true,
        pending_confirmations: 0,
        turn_id: 'frame-plan',
      },
    });
    const onResumed = vi.fn();

    await renderWithI18n(
      <SynonBiomedRuntimeOperations
        conversationId='frame-plan'
        runtimeState='waiting_confirmation'
        onResumed={onResumed}
      />
    );

    const region = await screen.findByTestId('synon-biomed-runtime-status');
    await waitFor(() => expect(region).toHaveAttribute('data-state', 'waiting_approval'));
    expect(region).toHaveTextContent('任务等待审批');
    expect(screen.queryByRole('button', { name: '批准并执行' })).not.toBeInTheDocument();
    await act(async () => {
      emitter.emit('synonbiomed.runtime.plan.open', 'frame-plan');
      await Promise.resolve();
    });
    expect(await screen.findByText('Characterize STAT6 binding pockets')).toBeInTheDocument();
    expect(screen.getByText('Structure review')).toBeInTheDocument();
    expect(screen.getByText('OPERON')).toBeInTheDocument();
    expect(screen.getByText('Fetch structures')).toBeInTheDocument();
    expect(screen.getAllByText('Fetch structures')).toHaveLength(1);
    expect(screen.getByText('Confirm outputs')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '批准并执行' }));
    await waitFor(() => expect(runtimeMocks.approvePlan).toHaveBeenCalledWith('frame-plan', abortOptions()));
    expect(onResumed).toHaveBeenCalledWith('frame-plan', expect.objectContaining({ state: 'running' }));
  });

  it.each(['processing', 'completed'] as const)(
    'opens the plan bound to the current %s task read-only without historical or approval UI',
    async (status) => {
      const planDocument = deferred<{
        version: number;
        taskSummary: string;
        phases: never[];
        feasibility: null;
      }>();
      runtimeMocks.loadSnapshot.mockResolvedValue({
        ...failedSnapshot,
        frameId: 'frame-completed',
        rootFrameId: 'frame-completed',
        status,
        error: null,
        taskPlan: { artifactId: 'artifact-current-plan', versionId: 'version-current-plan' },
        planApproval: null,
        canCancel: status === 'processing',
        canResume: false,
      });
      runtimeMocks.loadPlan.mockReturnValue(planDocument.promise);
      const document = {
        version: 1,
        taskSummary: 'Current task plan',
        phases: [] as never[],
        feasibility: null as null,
      };

      await renderWithI18n(
        <SynonBiomedRuntimeOperations conversationId='frame-completed' runtimeState='idle' onResumed={vi.fn()} />
      );
      await waitFor(() =>
        expect(screen.getByTestId('synon-biomed-runtime-status')).toHaveAttribute(
          'data-state',
          status === 'processing' ? 'running' : 'completed'
        )
      );
      fireEvent.click(screen.getByTestId('synon-biomed-task-details-trigger'));
      fireEvent.click(await screen.findByTestId('synon-biomed-task-open-plan'));
      await act(async () => {
        planDocument.resolve(document);
        await planDocument.promise;
      });

      expect(await screen.findByText('Current task plan')).toBeInTheDocument();
      expect(runtimeMocks.loadPlan).toHaveBeenCalledWith('artifact-current-plan', abortOptions());
      expect(screen.queryByText(/这是历史计划，仅供查看/)).not.toBeInTheDocument();
      expect(screen.queryByRole('button', { name: '批准并执行' })).not.toBeInTheDocument();
      expect(screen.queryByRole('button', { name: '丢弃计划并结束任务' })).not.toBeInTheDocument();
      expect(screen.getByTestId('synon-biomed-runtime-status')).toHaveAttribute(
        'data-state',
        status === 'processing' ? 'running' : 'completed'
      );
    }
  );

  it('does not expose an unrelated conversation artifact when the current task has no bound plan', async () => {
    runtimeMocks.loadSnapshot.mockResolvedValue({
      ...failedSnapshot,
      frameId: 'frame-without-plan',
      rootFrameId: 'frame-without-plan',
      status: 'completed',
      error: null,
      taskPlan: null,
      canResume: false,
    });

    await renderWithI18n(
      <SynonBiomedRuntimeOperations conversationId='frame-without-plan' runtimeState='idle' onResumed={vi.fn()} />
    );
    await waitFor(() => expect(runtimeMocks.loadSnapshot).toHaveBeenCalledWith('frame-without-plan', abortOptions()));
    fireEvent.click(screen.getByTestId('synon-biomed-task-details-trigger'));

    expect(screen.queryByTestId('synon-biomed-task-open-plan')).not.toBeInTheDocument();
  });
});

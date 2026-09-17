import { ipcBridge } from '@/common';
import { Refresh } from '@icon-park/react';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { loadSynonBiomedComputeJobs, type SynonBiomedComputeJob } from '@/renderer/services/synonBiomedCompute';
import {
  loadSynonBiomedAllKernels,
  stopSynonBiomedKernel,
  type SynonBiomedKernel,
  type SynonBiomedKernelStopRequest,
  type SynonBiomedMachineMetrics,
} from '@/renderer/services/synonBiomedNotebook';
import ComputeMachineHeader from './ComputeMachineHeader';
import ComputeJobsDialog from './ComputeJobsDialog';
import ComputeSessionSection, {
  ComputeSessionDivider,
  type ComputeStopTarget,
  type PendingKernelStop,
} from './ComputeSessionSection';
import {
  formatAge,
  formatMemory,
  groupKernels,
  mergeRingSamples,
  sumNullable,
  type ComputeRingPoint,
} from './computeRuntimeModel';
import './SynonBiomedComputeRuntimePanel.css';

const REFRESH_INTERVAL_MS = 2_000;
const ACTIVE_JOB_REFRESH_MS = 10_000;
const IDLE_JOB_REFRESH_MS = 60_000;
const RING_WINDOW_MS = 65_000;
const STALE_AFTER_MS = 15_000;

type SynonBiomedComputeRuntimePanelProps = {
  rootFrameId: string;
  projectId: string | null;
  projectName?: string;
  sessionTitle?: string;
};

type RuntimeState = {
  kernels: SynonBiomedKernel[];
  jobs: SynonBiomedComputeJob[];
  machine: SynonBiomedMachineMetrics | null;
  loading: boolean;
  error: string | null;
  jobsError: boolean;
};

const initialState: RuntimeState = {
  kernels: [],
  jobs: [],
  machine: null,
  loading: true,
  error: null,
  jobsError: false,
};

const isTerminalJobState = (state: string): boolean =>
  ['completed', 'done', 'failed', 'cancelled', 'canceled', 'succeeded', 'timed_out', 'orphaned'].includes(
    state.trim().toLowerCase()
  );

const kernelCellToken = (kernel: SynonBiomedKernel): string | null =>
  kernel.currentCell?.tag || kernel.currentCell?.startedAt || null;

const SynonBiomedComputeRuntimePanel: React.FC<SynonBiomedComputeRuntimePanelProps> = ({
  rootFrameId,
  projectId,
  projectName = 'Synon Biomed',
  sessionTitle = '',
}) => {
  const { t } = useTranslation();
  const [state, setState] = useState<RuntimeState>(initialState);
  const [refreshing, setRefreshing] = useState(false);
  const [collapsedSessions, setCollapsedSessions] = useState<Set<string>>(() => new Set());
  const [expandedRows, setExpandedRows] = useState<Set<string>>(() => new Set());
  const [stopTarget, setStopTarget] = useState<ComputeStopTarget | null>(null);
  const [pendingStops, setPendingStops] = useState<Map<string, PendingKernelStop>>(() => new Map());
  const [stopClock, setStopClock] = useState(() => Date.now());
  const [jobsDialog, setJobsDialog] = useState<{ open: boolean; jobId: string | null }>({
    open: false,
    jobId: null,
  });
  const kernelRefreshInFlightRef = useRef(false);
  const ringsRef = useRef<Map<string, ComputeRingPoint[]>>(new Map());
  const [, setRingRevision] = useState(0);

  const refreshKernels = useCallback(
    async (quiet = false) => {
      if (kernelRefreshInFlightRef.current) return;
      kernelRefreshInFlightRef.current = true;
      if (quiet) setRefreshing(true);
      else setState((current) => ({ ...current, loading: true, error: null }));
      try {
        const inventory = await loadSynonBiomedAllKernels();
        mergeRingSamples(ringsRef.current, inventory.kernels, inventory.machine?.sampledAt ?? null, RING_WINDOW_MS);
        const kernelsById = new Map(inventory.kernels.map((kernel) => [kernel.kernelId, kernel]));
        setPendingStops((current) => {
          let changed = false;
          const next = new Map<string, PendingKernelStop>();
          for (const [kernelId, pending] of current) {
            const kernel = kernelsById.get(kernelId);
            if (!kernel) {
              changed = true;
              continue;
            }
            if (pending.mode === 'interrupt' && (!kernel.busy || kernelCellToken(kernel) !== pending.cellToken)) {
              changed = true;
              continue;
            }
            next.set(kernelId, pending);
          }
          return changed ? next : current;
        });
        setStopTarget((current) => {
          if (!current) return null;
          if (current.kind === 'kernel') return kernelsById.has(current.kernelId) ? current : null;
          return inventory.kernels.some((kernel) => kernel.rootFrameId === current.rootFrameId) ? current : null;
        });
        setRingRevision((value) => value + 1);
        setState((current) => ({
          ...current,
          kernels: inventory.kernels,
          machine: inventory.machine,
          loading: false,
          error: null,
        }));
      } catch (reason) {
        console.warn('[SynonBiomedComputeRuntimePanel] Failed to refresh kernel inventory', {
          errorName: reason instanceof Error ? reason.name : typeof reason,
        });
        setState((current) => ({
          ...current,
          loading: false,
          error: t('conversation.synonRuntime.computeRuntime.loadingFailed'),
        }));
      } finally {
        kernelRefreshInFlightRef.current = false;
        setRefreshing(false);
      }
    },
    [t]
  );

  const refreshJobs = useCallback(async () => {
    if (!projectId) {
      setState((current) => ({ ...current, jobs: [], jobsError: false }));
      return 0;
    }
    try {
      const jobs = (await loadSynonBiomedComputeJobs(projectId)).filter((job) => !isTerminalJobState(job.state));
      setState((current) => ({ ...current, jobs, jobsError: false }));
      return jobs.length;
    } catch {
      setState((current) => ({ ...current, jobsError: true }));
      return 0;
    }
  }, [projectId]);

  useEffect(() => {
    let disposed = false;
    let timer: ReturnType<typeof setInterval> | undefined;
    const run = async () => {
      if (!disposed) await refreshKernels(true);
    };
    void refreshKernels(false);
    timer = setInterval(() => void run(), REFRESH_INTERVAL_MS);
    return () => {
      disposed = true;
      if (timer) clearInterval(timer);
    };
  }, [refreshKernels]);

  useEffect(() => {
    let disposed = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const poll = async () => {
      const count = await refreshJobs();
      if (!disposed) timer = setTimeout(() => void poll(), count > 0 ? ACTIVE_JOB_REFRESH_MS : IDLE_JOB_REFRESH_MS);
    };
    void poll();
    return () => {
      disposed = true;
      if (timer) clearTimeout(timer);
    };
  }, [refreshJobs]);

  useEffect(() => {
    let kernelTimer: ReturnType<typeof setTimeout> | undefined;
    let jobTimer: ReturnType<typeof setTimeout> | undefined;
    const scheduleKernels = () => {
      if (kernelTimer) return;
      kernelTimer = setTimeout(() => {
        kernelTimer = undefined;
        void refreshKernels(true);
      }, 100);
    };
    const scheduleJobs = () => {
      if (jobTimer) return;
      jobTimer = setTimeout(() => {
        jobTimer = undefined;
        void refreshJobs();
      }, 100);
    };
    const disposeKernel = ipcBridge.kernel.executionCellUpdate.on(scheduleKernels);
    const disposeJob = ipcBridge.compute.jobUpdate.on((event) => {
      if (!projectId || event.project_id === projectId) scheduleJobs();
    });
    const disposeReconnect = ipcBridge.realtime.reconnected.on(() => {
      scheduleKernels();
      scheduleJobs();
    });
    return () => {
      disposeKernel();
      disposeJob();
      disposeReconnect();
      if (kernelTimer) clearTimeout(kernelTimer);
      if (jobTimer) clearTimeout(jobTimer);
    };
  }, [projectId, refreshJobs, refreshKernels]);

  useEffect(() => {
    const hasWaitingInterrupt = [...pendingStops.values()].some((pending) => pending.mode === 'interrupt');
    if (!hasWaitingInterrupt && state.kernels.length === 0) return;
    setStopClock(Date.now());
    const timer = setInterval(() => setStopClock(Date.now()), 1_000);
    return () => clearInterval(timer);
  }, [pendingStops, state.kernels.length]);

  const groups = useMemo(
    () => groupKernels(state.kernels, rootFrameId, projectName, sessionTitle),
    [projectName, rootFrameId, sessionTitle, state.kernels]
  );
  const otherJobCount = state.jobs.filter((job) => job.rootFrameId !== rootFrameId).length;
  const sampleTimestamp = state.machine?.sampledAt ? Date.parse(state.machine.sampledAt) : Number.NaN;
  const stale = Number.isFinite(sampleTimestamp) && Date.now() - sampleTimestamp > STALE_AFTER_MS;

  const toggleSession = (id: string) =>
    setCollapsedSessions((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  const toggleRow = (id: string) =>
    setExpandedRows((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  const submitStop = async (kernels: SynonBiomedKernel[], request: SynonBiomedKernelStopRequest) => {
    if (kernels.length === 0) return;
    const reason = request.reason?.trim() || null;
    const requestedAt = Date.now();
    const effectiveMode = request.force ? 'clear' : request.mode;
    setPendingStops((current) => {
      const next = new Map(current);
      for (const kernel of kernels) {
        next.set(kernel.kernelId, {
          mode: effectiveMode,
          reason,
          cellToken: kernelCellToken(kernel),
          requestedAt,
        });
      }
      return next;
    });

    const results = await Promise.allSettled(
      kernels.map((kernel) =>
        stopSynonBiomedKernel(kernel.frameId, kernel.kernelId, {
          ...request,
          reason,
        })
      )
    );
    const failures = results
      .map((result, index) => ({ result, kernelId: kernels[index].kernelId }))
      .filter(
        (entry): entry is { result: PromiseRejectedResult; kernelId: string } => entry.result.status === 'rejected'
      );
    if (failures.length > 0) {
      const failed = new Set(failures.map((failure) => failure.kernelId));
      setPendingStops((current) => {
        const next = new Map(current);
        for (const kernelId of failed) next.delete(kernelId);
        return next;
      });
    }
    if (failures.length === results.length) {
      throw failures[0].result.reason;
    }
    if (failures.length > 0) {
      const first = failures[0].result.reason;
      setState((current) => ({
        ...current,
        error: first instanceof Error ? first.message : t('conversation.synonRuntime.computeRuntime.stopFailed'),
      }));
    }
    await refreshKernels(true);
  };

  const forceStop = async (kernel: SynonBiomedKernel) => {
    const pending = pendingStops.get(kernel.kernelId);
    await submitStop([kernel], {
      mode: 'clear',
      reason: pending?.reason ?? null,
      force: true,
    });
  };

  return (
    <section
      className='synon-compute-pane relative flex min-h-0 flex-1 flex-col overflow-hidden border-0'
      data-testid='synon-biomed-compute-runtime-panel'
      data-stale={stale ? 'true' : undefined}
      aria-label={t('conversation.synonRuntime.computeRuntime.title')}
    >
      <div className='flex min-h-0 flex-1 flex-col overflow-y-auto'>
        <ComputeMachineHeader kernels={state.kernels} machine={state.machine} stale={stale} />

        {state.error && (
          <div className='mx-10px mt-8px flex items-center gap-8px rounded-8px bg-danger-1 px-10px py-8px text-11px text-danger-6'>
            <span className='min-w-0 flex-1 truncate'>{state.error}</span>
            <button
              type='button'
              className='inline-flex size-24px items-center justify-center rounded-5px border-0 bg-transparent hover:bg-danger-2'
              aria-label={t('conversation.synonRuntime.computeRuntime.refresh')}
              onClick={() => void refreshKernels(true)}
            >
              <Refresh theme='outline' size={13} />
            </button>
          </div>
        )}

        {state.loading ? (
          <div className='px-16px py-24px text-center text-12px text-t-tertiary'>
            {t('conversation.synonRuntime.computeRuntime.loading')}
          </div>
        ) : groups.length === 0 ? (
          <div className='flex min-h-240px flex-1 flex-col items-center justify-center gap-4px px-20px py-40px text-center'>
            <div className='text-14px text-t-secondary'>{t('conversation.synonRuntime.computeRuntime.empty')}</div>
            <div className='max-w-340px text-12px text-t-tertiary'>
              {t('conversation.synonRuntime.computeRuntime.emptyHint')}
            </div>
          </div>
        ) : (
          <div className='flex flex-col gap-4px px-8px pt-8px'>
            {groups.map((group, index) => (
              <React.Fragment key={group.rootFrameId}>
                <ComputeSessionSection
                  group={group}
                  collapsed={collapsedSessions.has(group.rootFrameId)}
                  expandedRows={expandedRows}
                  stopTarget={stopTarget}
                  pendingStops={pendingStops}
                  now={stopClock}
                  rings={ringsRef.current}
                  onToggleSession={() => toggleSession(group.rootFrameId)}
                  onToggleRow={toggleRow}
                  onOpenStop={setStopTarget}
                  onCloseStop={() => setStopTarget(null)}
                  onSubmitStop={submitStop}
                  onForceStop={forceStop}
                />
                {index === 0 && group.current && groups.length > 1 && (
                  <ComputeSessionDivider
                    label={t('conversation.synonRuntime.computeRuntime.otherSessions', {
                      sessions: groups.length - 1,
                      kernels: groups.slice(1).reduce((total, item) => total + item.kernels.length, 0),
                      memory: formatMemory(sumNullable(groups.slice(1).map((item) => item.rssBytes))),
                    })}
                  />
                )}
              </React.Fragment>
            ))}
            {state.machine?.sampledAt &&
              Date.now() - Date.parse(state.machine.sampledAt) > REFRESH_INTERVAL_MS * 2.5 && (
                <div className='px-10px pb-16px pt-10px text-11px tabular-nums text-t-tertiary'>
                  {t('conversation.synonRuntime.computeRuntime.sampledAgo', {
                    age: `${Math.max(0, Math.round((Date.now() - Date.parse(state.machine.sampledAt)) / 1000))}s ago`,
                  })}
                </div>
              )}
          </div>
        )}

        {(state.jobs.length > 0 || state.jobsError) && (
          <div className='mt-auto border-t border-solid border-[var(--color-border-2)] px-8px pb-12px pt-8px'>
            <div className='px-8px pb-4px text-10px font-600 uppercase tracking-wide text-t-tertiary'>
              {t('conversation.synonRuntime.computeRuntime.runsElsewhere')}
            </div>
            {state.jobsError && (
              <button
                type='button'
                className='w-full rounded-6px border-0 bg-transparent px-8px py-8px text-left text-11px text-danger-6 hover:bg-danger-1'
                onClick={() => setJobsDialog({ open: true, jobId: null })}
              >
                {t('conversation.synonRuntime.computeRuntime.jobs.loadFailed')}
              </button>
            )}
            {state.jobs.map((job) => (
              <button
                key={job.jobId}
                type='button'
                data-testid='compute-job-row'
                className='flex w-full items-center gap-8px rounded-8px border-0 bg-transparent px-8px py-6px text-left text-12px hover:bg-fill-1'
                onClick={() => setJobsDialog({ open: true, jobId: job.jobId })}
              >
                <span className='size-6px rounded-full bg-primary-6' />
                <span className='min-w-0 flex-1 truncate text-t-secondary'>{computeJobLabel(job)}</span>
                <span className='shrink-0 rounded-4px bg-fill-2 px-5px py-1px text-9px uppercase text-t-tertiary'>
                  {job.providerLabel || job.provider}
                </span>
                <span className='text-11px text-t-tertiary'>{formatAge(job.startedAtIso ?? job.startedAt)}</span>
              </button>
            ))}
          </div>
        )}
      </div>

      {refreshing && (
        <div className='pointer-events-none absolute right-8px top-8px text-t-tertiary' aria-hidden='true'>
          <Refresh theme='outline' size={12} className='animate-spin' />
        </div>
      )}
      <ComputeJobsDialog
        open={jobsDialog.open}
        jobs={state.jobs}
        initialJobId={jobsDialog.jobId}
        otherCount={otherJobCount}
        loadError={state.jobsError}
        onClose={() => setJobsDialog({ open: false, jobId: null })}
      />
    </section>
  );
};

const computeJobLabel = (job: SynonBiomedComputeJob): string => {
  if (typeof job.intent === 'string' && job.intent.trim()) return job.intent;
  if (job.intent && typeof job.intent === 'object') {
    try {
      const encoded = JSON.stringify(job.intent);
      if (encoded !== '{}') return encoded;
    } catch {
      // Fall through to the stable environment label.
    }
  }
  return job.environment || job.providerLabel || job.provider;
};

export default SynonBiomedComputeRuntimePanel;

import { ArrowRightUp, Down, More, PauseOne } from '@icon-park/react';
import { Dropdown } from '@arco-design/web-react';
import classNames from 'classnames';
import React, { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import pythonKernelIcon from '@/renderer/assets/icons/python-kernel.svg';
import type { SynonBiomedKernel, SynonBiomedKernelStopRequest } from '@/renderer/services/synonBiomedNotebook';
import {
  compactSource,
  formatAge,
  formatCoresFromPercent,
  formatKernelEnvironment,
  formatMemory,
  kernelExecutionKind,
  observedKernelProcesses,
  type ComputeRingPoint,
  type ComputeSessionGroup,
} from './computeRuntimeModel';
import KernelStopDrawer from './KernelStopDrawer';

export type ComputeStopTarget = { kind: 'kernel'; kernelId: string } | { kind: 'session'; rootFrameId: string };

export type PendingKernelStop = {
  mode: 'interrupt' | 'clear';
  reason: string | null;
  cellToken: string | null;
  requestedAt: number;
};

type ComputeSessionSectionProps = {
  group: ComputeSessionGroup;
  collapsed: boolean;
  expandedRows: Set<string>;
  stopTarget: ComputeStopTarget | null;
  pendingStops: Map<string, PendingKernelStop>;
  now: number;
  rings: Map<string, ComputeRingPoint[]>;
  onToggleSession: () => void;
  onToggleRow: (kernelId: string) => void;
  onOpenStop: (target: ComputeStopTarget) => void;
  onCloseStop: () => void;
  onSubmitStop: (kernels: SynonBiomedKernel[], request: SynonBiomedKernelStopRequest) => Promise<void>;
  onForceStop: (kernel: SynonBiomedKernel) => Promise<void>;
};

const ComputeSessionSection: React.FC<ComputeSessionSectionProps> = ({
  group,
  collapsed,
  expandedRows,
  stopTarget,
  pendingStops,
  now,
  rings,
  onToggleSession,
  onToggleRow,
  onOpenStop,
  onCloseStop,
  onSubmitStop,
  onForceStop,
}) => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [menuVisible, setMenuVisible] = useState(false);
  const sessionStopOpen = stopTarget?.kind === 'session' && stopTarget.rootFrameId === group.rootFrameId;
  const sessionMenu = (
    <div
      role='menu'
      aria-label={t('conversation.synonRuntime.computeRuntime.sessionActions')}
      className='min-w-156px rounded-8px border border-solid border-[var(--color-border-2)] bg-[var(--color-bg-popup)] p-4px shadow-lg'
      onClick={(event) => event.stopPropagation()}
    >
      <button
        type='button'
        role='menuitem'
        className='flex h-30px w-full items-center gap-8px rounded-6px border-0 bg-transparent px-8px text-left text-12px text-t-primary hover:bg-fill-2'
        onClick={() => {
          setMenuVisible(false);
          void navigate(`/conversation/${encodeURIComponent(group.rootFrameId)}`);
        }}
      >
        <ArrowRightUp theme='outline' size={14} />
        {t('conversation.synonRuntime.computeRuntime.openSession')}
      </button>
      <button
        type='button'
        role='menuitem'
        className='flex h-30px w-full items-center gap-8px rounded-6px border-0 bg-transparent px-8px text-left text-12px text-danger-6 hover:bg-danger-1'
        onClick={() => {
          setMenuVisible(false);
          onOpenStop({ kind: 'session', rootFrameId: group.rootFrameId });
        }}
      >
        <PauseOne theme='outline' size={14} />
        {t('conversation.synonRuntime.computeRuntime.stopAll')}
      </button>
    </div>
  );
  return (
    <section className='pb-6px'>
      <div className='rounded-10px transition-colors'>
        <div className='group flex w-full items-center gap-8px rounded-8px px-10px pb-6px pt-10px'>
          <button
            type='button'
            className='synon-compute-session-row min-w-0 flex-1 cursor-pointer rounded-6px border-0 bg-transparent p-0 text-left'
            aria-expanded={!collapsed}
            data-testid='synon-biomed-compute-session-group'
            onClick={onToggleSession}
          >
            <Down
              theme='outline'
              size={12}
              className={classNames('shrink-0 transition-transform', collapsed && '-rotate-90')}
              aria-hidden='true'
            />
            {group.busyCount > 0 ? (
              <span className='synon-compute-running-dots shrink-0 text-t-tertiary' aria-hidden='true'>
                <span />
                <span />
                <span />
              </span>
            ) : (
              <span className='synon-compute-idle-ring' aria-hidden='true' />
            )}
            <span className='flex min-w-0 items-center gap-6px whitespace-nowrap'>
              <span className='shrink-0 text-11px text-t-tertiary'>{group.projectName} ›</span>
              <span className='block min-w-0 truncate text-13px font-500 text-t-primary'>{group.title}</span>
            </span>
            {group.current && (
              <span className='shrink-0 rounded-4px bg-fill-2 px-4px py-1px text-9px font-600 uppercase tracking-wide text-t-tertiary'>
                {t('conversation.synonRuntime.computeRuntime.sessionCurrent')}
              </span>
            )}
            <span className='synon-compute-session-summary ml-auto shrink-0 text-11px tabular-nums text-t-tertiary'>
              {t('conversation.synonRuntime.computeRuntime.sessionSummaryCompact', {
                kernels: group.kernels.length,
                memory: formatMemory(group.rssBytes),
                cores: formatCoresFromPercent(group.cpuPct),
              })}
            </span>
          </button>
          <Dropdown
            trigger='click'
            position='br'
            popupVisible={menuVisible}
            onVisibleChange={setMenuVisible}
            getPopupContainer={() => document.body}
            droplist={sessionMenu}
          >
            <button
              type='button'
              className='inline-flex size-24px shrink-0 items-center justify-center rounded-5px border-0 bg-transparent p-0 text-t-tertiary opacity-0 transition-opacity hover:bg-fill-2 hover:text-t-primary group-hover:opacity-100 focus-visible:opacity-100'
              aria-label={t('conversation.synonRuntime.computeRuntime.sessionActions')}
              aria-haspopup='menu'
              aria-expanded={menuVisible}
              onClick={(event) => event.stopPropagation()}
            >
              <More theme='outline' size={13} />
            </button>
          </Dropdown>
        </div>
      </div>
      {sessionStopOpen && (
        <div className='synon-compute-session-stop-drawer'>
          <KernelStopDrawer
            kernels={group.kernels}
            scope='session'
            onClose={onCloseStop}
            onSubmit={(request) => onSubmitStop(group.kernels, request)}
          />
        </div>
      )}
      {!collapsed &&
        group.kernels.map((kernel) => (
          <KernelRow
            key={kernel.kernelId}
            kernel={kernel}
            ring={rings.get(kernel.kernelId) ?? []}
            expanded={expandedRows.has(kernel.kernelId)}
            stopDrawerOpen={stopTarget?.kind === 'kernel' && stopTarget.kernelId === kernel.kernelId}
            pendingStop={pendingStops.get(kernel.kernelId) ?? null}
            now={now}
            onToggle={() => onToggleRow(kernel.kernelId)}
            onOpenStop={() => onOpenStop({ kind: 'kernel', kernelId: kernel.kernelId })}
            onCloseStop={onCloseStop}
            onSubmitStop={(request) => onSubmitStop([kernel], request)}
            onForceStop={() => onForceStop(kernel)}
          />
        ))}
    </section>
  );
};

const KernelRow: React.FC<{
  kernel: SynonBiomedKernel;
  ring: ComputeRingPoint[];
  expanded: boolean;
  stopDrawerOpen: boolean;
  pendingStop: PendingKernelStop | null;
  now: number;
  onToggle: () => void;
  onOpenStop: () => void;
  onCloseStop: () => void;
  onSubmitStop: (request: SynonBiomedKernelStopRequest) => Promise<void>;
  onForceStop: () => Promise<void>;
}> = ({
  kernel,
  ring,
  expanded,
  stopDrawerOpen,
  pendingStop,
  now,
  onToggle,
  onOpenStop,
  onCloseStop,
  onSubmitStop,
  onForceStop,
}) => {
  const { t } = useTranslation();
  const idle = !kernel.busy && !kernel.starting;
  const currentCellToken = kernel.currentCell?.tag || kernel.currentCell?.startedAt || null;
  const unresponsive =
    pendingStop?.mode === 'interrupt' &&
    pendingStop.cellToken !== null &&
    pendingStop.cellToken === currentCellToken &&
    now - pendingStop.requestedAt >= 5_000;
  const source =
    kernel.busy || kernel.starting
      ? kernel.currentCell?.source || ''
      : kernel.lastCell?.source || kernel.lastDescription || '';
  const executionKind = kernelExecutionKind(kernel);
  const observation = observedKernelProcesses(kernel, now);
  const observationLabel =
    observation.status === 'stale'
      ? t('conversation.synonRuntime.computeRuntime.observationStale')
      : observation.status === 'unavailable'
        ? t('conversation.synonRuntime.computeRuntime.processUnavailable')
        : observation.names.join(' + ');
  const status = kernel.starting
    ? t('conversation.synonRuntime.computeRuntime.status.starting')
    : kernel.busy
      ? observation.memoryPressure?.status === 'pressured'
        ? t('conversation.synonRuntime.computeRuntime.memoryPressure')
        : t('conversation.synonRuntime.computeRuntime.status.busy')
      : t('conversation.synonRuntime.computeRuntime.cellsRun', {
          age: formatAge(kernel.lastCell?.endedAt ?? kernel.lastUsed),
          count: kernel.executionCount || kernel.cellCount,
        });
  const primaryLabel = kernel.busy ? observationLabel : status;
  const runningAge = kernel.busy ? formatRunningDuration(kernel.currentCell?.startedAt) : '';
  const cpuHot = kernel.cpuPct != null && kernel.cpuPct >= 80;
  const environmentLabel = formatKernelEnvironment(
    kernel.environment,
    t('conversation.synonRuntime.computeRuntime.softwareRuntime')
  );

  return (
    <div
      className={classNames(
        'rounded-10px border border-solid transition-colors',
        expanded || stopDrawerOpen ? 'bg-fill-1' : ''
      )}
      style={{
        borderColor: expanded || stopDrawerOpen ? 'var(--color-border-2)' : 'transparent',
      }}
      data-testid='kernel-row'
    >
      <div
        role='button'
        tabIndex={0}
        aria-expanded={expanded || stopDrawerOpen}
        className='synon-compute-kernel-row group cursor-pointer rounded-10px px-10px py-8px transition-colors hover:bg-fill-1'
        onClick={onToggle}
        onKeyDown={(event) => {
          if (event.key === 'Enter' || event.key === ' ') {
            event.preventDefault();
            onToggle();
          }
        }}
      >
        <Down
          theme='outline'
          size={11}
          className={classNames('transition-transform', !expanded && '-rotate-90')}
          aria-hidden='true'
        />
        <span
          className='flex size-22px items-center justify-center rounded-6px bg-primary-1 text-primary-6'
          aria-label={executionKind}
        >
          {executionKind !== 'Python' ? (
            <span className='font-mono text-9px font-600'>{executionKind}</span>
          ) : (
            <img src={pythonKernelIcon} alt='' className='size-14px' />
          )}
        </span>
        <div className='min-w-0'>
          <div
            className={classNames(
              'flex min-w-0 items-center gap-6px text-13px',
              idle ? 'font-normal italic text-t-tertiary' : 'font-500 text-t-primary'
            )}
          >
            <span className='min-w-0 truncate' title={primaryLabel}>
              {primaryLabel}
            </span>
            {kernel.busy && <span className='shrink-0 text-10px font-normal text-t-tertiary'>{status}</span>}
            {kernel.busy && runningAge && (
              <span className='shrink-0 font-mono text-10px font-normal tabular-nums text-t-tertiary'>
                {runningAge}
              </span>
            )}
          </div>
          <div className='truncate text-11px text-t-tertiary' title={environmentLabel}>
            {t('conversation.synonRuntime.computeRuntime.environmentLabel', { environment: environmentLabel || '—' })}
          </div>
          {observation.status === 'partial' && (
            <div className='text-10px text-t-tertiary'>
              {t('conversation.synonRuntime.computeRuntime.observationPartial')}
            </div>
          )}
          {!kernel.busy && source && (
            <div className='truncate text-11px text-t-tertiary opacity-70'>{compactSource(source)}</div>
          )}
        </div>
        <ResourceColumn
          label='rss'
          value={formatMemory(kernel.rssBytes)}
          ring={ring}
          pick={(point) => point.rss}
          color='#16a36a'
        />
        <ResourceColumn
          label='cpu'
          value={formatCoresFromPercent(kernel.cpuPct)}
          ring={ring}
          pick={(point) => point.cpu}
          color={cpuHot ? '#a87922' : '#1677ff'}
          warning={cpuHot}
          className='synon-compute-resource-column--cpu'
        />
        {unresponsive ? (
          <span
            className='inline-flex items-center gap-6px whitespace-nowrap'
            onClick={(event) => event.stopPropagation()}
          >
            <span className='text-10px font-500 text-danger-6'>
              {t('conversation.synonRuntime.computeRuntime.notResponding')}
            </span>
            <button
              type='button'
              data-testid='kernel-force-kill'
              className='synon-compute-stop-kill inline-flex h-24px items-center rounded-6px border-0 px-8px text-11px font-500 text-white'
              title={t('conversation.synonRuntime.computeRuntime.forceKillTitle')}
              onClick={() => void onForceStop()}
            >
              {t('conversation.synonRuntime.computeRuntime.forceKill')}
            </button>
          </span>
        ) : pendingStop ? (
          <span className='size-24px' aria-label={t('conversation.synonRuntime.computeRuntime.stoppingKernel')} />
        ) : (
          <button
            type='button'
            className='synon-compute-row-action inline-flex size-24px items-center justify-center rounded-6px border-0 bg-transparent p-0 text-t-tertiary hover:bg-fill-2 hover:text-t-primary'
            aria-label={t('conversation.synonRuntime.computeRuntime.stopKernel')}
            title={t('conversation.synonRuntime.computeRuntime.stopKernel')}
            onClick={(event) => {
              event.stopPropagation();
              onOpenStop();
            }}
          >
            <PauseOne theme='outline' size={13} />
          </button>
        )}
      </div>
      {stopDrawerOpen ? (
        <div className='synon-compute-row-stop-drawer'>
          <KernelStopDrawer kernels={[kernel]} onClose={onCloseStop} onSubmit={onSubmitStop} />
        </div>
      ) : expanded ? (
        <div className='synon-compute-row-drawer text-11px text-t-tertiary'>
          <div className='mb-4px'>{t('conversation.synonRuntime.computeRuntime.observedProcesses')}</div>
          {observation.memoryPressure && observation.memoryPressure.status !== 'unavailable' && (
            <div data-testid='kernel-memory-pressure' className='mb-8px'>
              {t('conversation.synonRuntime.computeRuntime.memoryBudget', {
                current: formatMemory(observation.memoryPressure.currentBytes),
                limit:
                  observation.memoryPressure.limitBytes > 0 ? formatMemory(observation.memoryPressure.limitBytes) : '—',
                stall: Math.round(observation.memoryPressure.fullStallPercent),
              })}
            </div>
          )}
          {observation.processes.length === 0 ? (
            <div>{observationLabel}</div>
          ) : (
            <ul className='mb-8px list-none p-0'>
              {observation.processes.map((process) => (
                <li key={`${process.pid}:${process.startIdentity}`} className='break-all font-mono'>
                  {process.name} · PID {process.pid} · PPID {process.parentPid} · {process.state}
                  {process.nameSource === 'process_name' &&
                    ` · ${t('conversation.synonRuntime.computeRuntime.processNameOnly')}`}
                </li>
              ))}
            </ul>
          )}
          <div className='mb-4px flex items-center gap-8px uppercase tracking-wide'>
            <span>{t('conversation.synonRuntime.computeRuntime.submittedSource')}</span>
            <span>·</span>
            <span>
              {kernel.pidVisible
                ? t('conversation.synonRuntime.computeRuntime.processVisible')
                : t('conversation.synonRuntime.computeRuntime.processUnavailable')}
            </span>
          </div>
          <div className='max-h-96px overflow-auto whitespace-pre-wrap rounded-6px bg-fill-2 px-8px py-6px font-mono text-11px leading-18px text-t-secondary'>
            {source || t('conversation.synonRuntime.computeRuntime.noCurrentCall')}
          </div>
        </div>
      ) : null}
    </div>
  );
};

const ResourceColumn: React.FC<{
  label: string;
  value: string;
  ring: ComputeRingPoint[];
  pick: (point: ComputeRingPoint) => number | null;
  color: string;
  warning?: boolean;
  className?: string;
}> = ({ label, value, ring, pick, color, warning = false, className }) => (
  <span className={classNames('synon-compute-resource-column', className)}>
    <span className='flex items-baseline gap-4px whitespace-nowrap'>
      <span
        className={classNames('font-mono text-11px tabular-nums', warning ? 'font-600' : 'text-t-secondary')}
        style={warning ? { color: '#a87922' } : undefined}
      >
        {value}
      </span>
      <span className='text-9px font-600 uppercase tracking-wide text-t-tertiary'>{label}</span>
    </span>
    <SparklineCanvas ring={ring} pick={pick} color={color} />
  </span>
);

const formatRunningDuration = (value?: string): string => {
  const startedAt = value ? Date.parse(value) : Number.NaN;
  if (!Number.isFinite(startedAt)) return '';
  const totalSeconds = Math.max(0, Math.floor((Date.now() - startedAt) / 1_000));
  const seconds = String(totalSeconds % 60).padStart(2, '0');
  const totalMinutes = Math.floor(totalSeconds / 60);
  if (totalMinutes < 60) return `${totalMinutes}:${seconds}`;
  const minutes = String(totalMinutes % 60).padStart(2, '0');
  return `${Math.floor(totalMinutes / 60)}:${minutes}:${seconds}`;
};

const SparklineCanvas: React.FC<{
  ring: ComputeRingPoint[];
  pick: (point: ComputeRingPoint) => number | null;
  color: string;
}> = ({ ring, pick, color }) => {
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const ratio = window.devicePixelRatio || 1;
    const width = canvas.clientWidth || 92;
    const height = canvas.clientHeight || 20;
    canvas.width = Math.round(width * ratio);
    canvas.height = Math.round(height * ratio);
    const context = canvas.getContext('2d');
    if (!context) return;
    context.scale(ratio, ratio);
    context.clearRect(0, 0, width, height);
    const points = ring
      .map((point) => ({ at: point.at, value: pick(point) }))
      .filter((point): point is { at: number; value: number } => point.value != null && Number.isFinite(point.value));
    if (points.length === 0) return;
    const minAt = points[0].at;
    const maxAt = points[points.length - 1].at;
    const values = points.map((point) => point.value);
    const minValue = Math.min(...values);
    const maxValue = Math.max(...values);
    context.strokeStyle = color;
    context.lineWidth = 1.25;
    context.lineJoin = 'round';
    context.beginPath();
    if (points.length === 1) {
      context.moveTo(0, height / 2);
      context.lineTo(width, height / 2);
    } else {
      points.forEach((point, index) => {
        const x =
          maxAt === minAt
            ? (index / Math.max(1, points.length - 1)) * width
            : ((point.at - minAt) / (maxAt - minAt)) * width;
        const y =
          maxValue === minValue
            ? height / 2
            : height - 2 - ((point.value - minValue) / (maxValue - minValue)) * (height - 5);
        if (index === 0) context.moveTo(x, y);
        else context.lineTo(x, y);
      });
    }
    context.stroke();
  }, [color, pick, ring]);
  return <canvas ref={canvasRef} className='synon-compute-sparkline' aria-hidden='true' />;
};

export const ComputeSessionDivider: React.FC<{ label: string }> = ({ label }) => (
  <div
    className='flex items-center gap-10px px-10px pb-2px pt-4px'
    aria-hidden='true'
    data-testid='compute-session-divider'
  >
    <span className='h-px flex-1 bg-[var(--color-border-2)] opacity-40' />
    <span className='shrink-0 text-10px tabular-nums text-t-tertiary'>{label}</span>
    <span className='h-px flex-1 bg-[var(--color-border-2)] opacity-40' />
  </div>
);

export default ComputeSessionSection;

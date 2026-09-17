/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { ipcBridge } from '@/common';
import {
  loadSynonBiomedComputeJob,
  loadSynonBiomedComputeJobLog,
  type SynonBiomedComputeJob,
  type SynonBiomedComputeJobLog,
} from '@/renderer/services/synonBiomedCompute';
import { Button, Modal, Tag } from '@arco-design/web-react';
import { ArrowLeft, Copy, LinkOne, Right } from '@icon-park/react';
import classNames from 'classnames';
import React, { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { formatAge } from './computeRuntimeModel';

type ComputeJobsDialogProps = {
  open: boolean;
  jobs: SynonBiomedComputeJob[];
  initialJobId: string | null;
  otherCount: number;
  loadError: boolean;
  onClose: () => void;
};

const EMPTY_LOG: SynonBiomedComputeJobLog = {
  exists: false,
  size: 0,
  content: '',
  truncated: false,
};

const ComputeJobsDialog: React.FC<ComputeJobsDialogProps> = ({
  open,
  jobs,
  initialJobId,
  otherCount,
  loadError,
  onClose,
}) => {
  const { t } = useTranslation();
  const [selectedJobId, setSelectedJobId] = useState<string | null>(null);

  useEffect(() => {
    setSelectedJobId(open ? initialJobId : null);
  }, [initialJobId, open]);

  const selected = selectedJobId ? (jobs.find((job) => job.jobId === selectedJobId) ?? null) : null;
  return (
    <Modal
      visible={open}
      title={t('conversation.synonRuntime.computeRuntime.jobs.title')}
      footer={null}
      unmountOnExit
      onCancel={onClose}
      style={{ width: 'min(760px, 92vw)' }}
    >
      <div className='relative h-[min(60vh,520px)] min-w-0 overflow-hidden' data-testid='compute-jobs-modal'>
        <div
          aria-hidden={Boolean(selectedJobId)}
          inert={Boolean(selectedJobId) || undefined}
          className={classNames(
            'absolute inset-0 flex flex-col overflow-y-auto pt-3 transition-transform duration-200',
            selectedJobId ? '-translate-x-full' : 'translate-x-0'
          )}
        >
          {loadError ? (
            <ComputeJobsEmpty
              title={t('conversation.synonRuntime.computeRuntime.jobs.loadFailed')}
              body={t('conversation.synonRuntime.computeRuntime.jobs.loadFailedHint')}
            />
          ) : jobs.length === 0 ? (
            <ComputeJobsEmpty
              title={t('conversation.synonRuntime.computeRuntime.jobs.empty')}
              body={
                otherCount > 0
                  ? t('conversation.synonRuntime.computeRuntime.jobs.runningElsewhere', {
                      count: otherCount,
                    })
                  : t('conversation.synonRuntime.computeRuntime.jobs.emptyHint')
              }
            />
          ) : (
            <div className='flex flex-1 flex-col gap-1'>
              {jobs.map((job) => (
                <ComputeJobRow key={job.jobId} job={job} onOpen={() => setSelectedJobId(job.jobId)} />
              ))}
            </div>
          )}
          <div className='mt-auto flex justify-end pt-4'>
            <Button type='text' size='small' onClick={onClose}>
              {t('common.close')}
            </Button>
          </div>
        </div>

        <div
          aria-hidden={!selectedJobId}
          inert={!selectedJobId || undefined}
          className={classNames(
            'absolute inset-0 transition-transform duration-200',
            selectedJobId ? 'translate-x-0' : 'translate-x-full'
          )}
        >
          {selectedJobId && (
            <ComputeJobDetail
              jobId={selectedJobId}
              listed={selected}
              onBack={() => (jobs.length > 0 ? setSelectedJobId(null) : onClose())}
            />
          )}
        </div>
      </div>
    </Modal>
  );
};

const ComputeJobsEmpty: React.FC<{ title: string; body: string }> = ({ title, body }) => (
  <div className='flex flex-1 flex-col items-center justify-center gap-4px px-24px text-center'>
    <div className='text-14px text-t-secondary'>{title}</div>
    <div className='max-w-380px text-12px text-t-tertiary'>{body}</div>
  </div>
);

const ComputeJobRow: React.FC<{ job: SynonBiomedComputeJob; onOpen: () => void }> = ({ job, onOpen }) => {
  const { t } = useTranslation();
  return (
    <div
      role='button'
      tabIndex={0}
      data-testid='compute-job-row'
      className='flex w-full cursor-pointer items-start gap-12px rounded-6px px-8px py-10px text-left hover:bg-fill-1'
      onClick={onOpen}
      onKeyDown={(event) => {
        if (event.target === event.currentTarget && (event.key === 'Enter' || event.key === ' ')) {
          event.preventDefault();
          onOpen();
        }
      }}
    >
      <span className='mt-5px size-7px shrink-0 rounded-full bg-primary-6' />
      <div className='min-w-0 flex-1'>
        <div className='truncate text-14px text-t-primary'>{computeIntentLabel(job)}</div>
        <div className='mt-4px flex items-center gap-8px text-11px text-t-tertiary'>
          <Tag size='small' color='gray'>
            {job.providerLabel || job.provider}
          </Tag>
          {job.externalId && <span className='truncate font-mono'>{compactId(job.externalId)}</span>}
          <span>{job.providerFamily === 'ssh' || job.providerFamily === 'infer' ? '' : job.tierType}</span>
          <span className='ml-auto shrink-0'>{formatAge(job.startedAtIso ?? job.startedAt)}</span>
        </div>
      </div>
      <span className='sr-only'>{t('conversation.synonRuntime.computeRuntime.jobs.openDetails')}</span>
      <Right theme='outline' size={12} className='mt-4px shrink-0 text-t-tertiary' />
    </div>
  );
};

const ComputeJobDetail: React.FC<{
  jobId: string;
  listed: SynonBiomedComputeJob | null;
  onBack: () => void;
}> = ({ jobId, listed, onBack }) => {
  const { t } = useTranslation();
  const [detail, setDetail] = useState<SynonBiomedComputeJob | null>(listed);
  const [logs, setLogs] = useState<Record<'stdout' | 'stderr', SynonBiomedComputeJobLog>>({
    stdout: EMPTY_LOG,
    stderr: EMPTY_LOG,
  });
  const [stream, setStream] = useState<'stdout' | 'stderr'>('stdout');
  const [loading, setLoading] = useState(true);
  const [missing, setMissing] = useState(false);
  const [copied, setCopied] = useState(false);

  const running = detail ? !isTerminalJob(detail) : true;
  useEffect(() => {
    let disposed = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const refresh = async () => {
      try {
        const [nextDetail, stdout, stderr] = await Promise.all([
          loadSynonBiomedComputeJob(jobId),
          loadSynonBiomedComputeJobLog(jobId, { stream: 'stdout', tail: 262_144 }).catch(() => EMPTY_LOG),
          loadSynonBiomedComputeJobLog(jobId, { stream: 'stderr', tail: 262_144 }).catch(() => EMPTY_LOG),
        ]);
        if (disposed) return;
        setDetail(nextDetail);
        setLogs({ stdout, stderr });
        setMissing(false);
        if (!isTerminalJob(nextDetail)) timer = setTimeout(() => void refresh(), 5_000);
      } catch {
        if (!disposed) setMissing(true);
      } finally {
        if (!disposed) setLoading(false);
      }
    };
    void refresh();
    const unsubscribe = ipcBridge.compute.jobLogChunk.on((event) => {
      if (event.job_id !== jobId || !event.chunk) return;
      const key = event.stream === 'err' ? 'stderr' : 'stdout';
      setLogs((current) => ({
        ...current,
        [key]: appendJobLog(current[key], event.chunk),
      }));
    });
    return () => {
      disposed = true;
      unsubscribe();
      if (timer) clearTimeout(timer);
    };
  }, [jobId]);

  if ((missing && !detail) || (!loading && !detail)) {
    return (
      <div className='flex h-full min-h-0 flex-col gap-12px pt-12px' data-testid='compute-job-detail'>
        <Button type='text' size='small' className='self-start' icon={<ArrowLeft size={13} />} onClick={onBack}>
          {t('conversation.synonRuntime.computeRuntime.jobs.back')}
        </Button>
        <div className='text-11px text-t-tertiary'>
          {missing
            ? t('conversation.synonRuntime.computeRuntime.jobs.missing')
            : t('conversation.synonRuntime.computeRuntime.jobs.loading')}
        </div>
      </div>
    );
  }
  if (!detail) return null;
  const activeLog = logs[stream];
  const externalUrl = safeExternalURL(detail.externalUrl);
  const harvestWarning =
    isTerminalJob(detail) && (detail.errorKind === 'harvest_failed' || detail.errorKind === 'over_cap');

  const copyId = async () => {
    try {
      await navigator.clipboard.writeText(detail.externalId || detail.jobId);
      setCopied(true);
      setTimeout(() => setCopied(false), 1_500);
    } catch {
      setCopied(false);
    }
  };

  return (
    <div className='flex h-full min-h-0 flex-col gap-12px pt-12px' data-testid='compute-job-detail'>
      <div className='flex items-start gap-8px'>
        <Button
          type='text'
          size='small'
          className='-ml-8px shrink-0'
          icon={<ArrowLeft theme='outline' size={13} />}
          onClick={onBack}
        >
          {t('conversation.synonRuntime.computeRuntime.jobs.back')}
        </Button>
        <div className='line-clamp-3 min-w-0 flex-1 break-words pt-5px text-14px leading-20px text-t-primary'>
          {computeIntentLabel(detail)}
        </div>
        <Tag size='small' color={jobStateColor(detail.state)} className='mt-4px shrink-0 uppercase'>
          {displayJobState(detail.state)}
        </Tag>
      </div>

      {harvestWarning && (
        <div
          className='rounded-8px bg-warning-1 px-12px py-10px text-12px text-warning-7'
          data-testid='compute-job-harvest-warn'
        >
          <div className='font-600'>{t('conversation.synonRuntime.computeRuntime.jobs.outputsRemote')}</div>
          <div className='mt-3px'>
            {detail.systemHint || t('conversation.synonRuntime.computeRuntime.jobs.outputsRemoteHint')}
          </div>
          {detail.leftOnRemote.length > 0 && (
            <ul className='mb-0 mt-6px max-h-60px overflow-auto pl-18px font-mono text-11px'>
              {detail.leftOnRemote.slice(0, 5).map((value) => (
                <li key={value} className='truncate'>
                  {value}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}

      <dl className='grid grid-cols-2 gap-x-24px gap-y-6px rounded-8px bg-fill-1 px-12px py-10px text-11px'>
        <DetailItem label={t('conversation.synonRuntime.computeRuntime.jobs.provider')} value={detail.providerLabel} />
        <DetailItem
          label={t('conversation.synonRuntime.computeRuntime.jobs.hardware')}
          value={
            detail.providerFamily === 'ssh' || detail.providerFamily === 'infer'
              ? '—'
              : readableValue(detail.hardwareDetails || detail.tierType)
          }
        />
        <DetailItem
          label={t('conversation.synonRuntime.computeRuntime.jobs.state')}
          value={displayJobState(detail.state)}
        />
        <DetailItem
          label={t('conversation.synonRuntime.computeRuntime.jobs.started')}
          value={`${formatAge(detail.startedAtIso ?? detail.startedAt)} ${t('conversation.synonRuntime.computeRuntime.jobs.ago')}`}
        />
        <div className='col-span-2 flex min-w-0 items-center gap-8px'>
          <dt className='shrink-0 text-t-tertiary'>{t('conversation.synonRuntime.computeRuntime.jobs.id')}</dt>
          <dd className='m-0 min-w-0 flex-1 truncate font-mono text-t-secondary'>
            {detail.externalId || detail.jobId}
          </dd>
          <button
            type='button'
            className='inline-flex size-24px items-center justify-center rounded-5px border-0 bg-transparent text-t-tertiary hover:bg-fill-2'
            aria-label={copied ? t('common.copySuccess') : t('conversation.synonRuntime.computeRuntime.jobs.copyId')}
            onClick={() => void copyId()}
          >
            <Copy theme='outline' size={12} />
          </button>
          {externalUrl && (
            <a
              href={externalUrl}
              target='_blank'
              rel='noreferrer'
              className='inline-flex size-24px items-center justify-center rounded-5px text-t-tertiary hover:bg-fill-2'
              aria-label={t('conversation.synonRuntime.computeRuntime.jobs.openRemote')}
            >
              <LinkOne theme='outline' size={12} />
            </a>
          )}
        </div>
        {detail.endedAtIso && (
          <DetailItem
            label={t('conversation.synonRuntime.computeRuntime.jobs.ended')}
            value={`${formatAge(detail.endedAtIso)} ${t('conversation.synonRuntime.computeRuntime.jobs.ago')}`}
          />
        )}
      </dl>

      <div className='flex min-h-0 flex-1 flex-col gap-8px'>
        <div className='flex items-center justify-between gap-12px'>
          <div
            className='flex rounded-6px bg-fill-1 p-2px'
            role='tablist'
            aria-label={t('conversation.synonRuntime.computeRuntime.jobs.logs')}
          >
            {(['stdout', 'stderr'] as const).map((name) => (
              <button
                key={name}
                type='button'
                role='tab'
                aria-selected={stream === name}
                className={classNames(
                  'rounded-5px border-0 px-8px py-4px font-mono text-11px',
                  stream === name ? 'bg-1 text-t-primary shadow-sm' : 'bg-transparent text-t-tertiary'
                )}
                onClick={() => setStream(name)}
              >
                {name} {logs[name].size > 0 ? `· ${formatBytes(logs[name].size)}` : ''}
              </button>
            ))}
          </div>
          {running && detail.supportsTail && (
            <span className='flex items-center gap-5px text-11px text-t-tertiary'>
              <span className='size-5px animate-pulse rounded-full bg-success-6' />
              {activeLog.content
                ? t('conversation.synonRuntime.computeRuntime.jobs.streaming')
                : t('conversation.synonRuntime.computeRuntime.jobs.waitingOutput')}
            </span>
          )}
        </div>
        <div className='min-h-0 flex-1 overflow-auto rounded-8px bg-fill-1'>
          {activeLog.truncated && (
            <div className='px-10px pt-8px text-10px uppercase text-t-tertiary'>
              {t('conversation.synonRuntime.computeRuntime.jobs.truncated', {
                size: formatBytes(activeLog.size),
              })}
            </div>
          )}
          <pre className='m-0 whitespace-pre p-10px font-mono text-11px leading-18px text-t-secondary'>
            {activeLog.content || t('conversation.synonRuntime.computeRuntime.jobs.noLog')}
          </pre>
        </div>
      </div>
    </div>
  );
};

const DetailItem: React.FC<{ label: string; value: React.ReactNode }> = ({ label, value }) => (
  <div className='flex min-w-0 items-center gap-8px'>
    <dt className='shrink-0 text-t-tertiary'>{label}</dt>
    <dd className='m-0 min-w-0 truncate text-t-secondary'>{value}</dd>
  </div>
);

const appendJobLog = (current: SynonBiomedComputeJobLog, chunk: string): SynonBiomedComputeJobLog => {
  const combined = current.content + chunk;
  const bounded = combined.length > 262_144 ? combined.slice(-262_144) : combined;
  return {
    exists: true,
    size: current.size + new TextEncoder().encode(chunk).length,
    content: bounded,
    truncated: current.truncated || bounded.length < combined.length,
  };
};

const computeIntentLabel = (job: SynonBiomedComputeJob): string =>
  readableValue(job.intent) || job.environment || job.providerLabel || job.provider;

const readableValue = (value: unknown): string => {
  if (typeof value === 'string') return value;
  if (typeof value === 'number' || typeof value === 'boolean') return String(value);
  if (!value) return '';
  try {
    return JSON.stringify(value);
  } catch {
    return '';
  }
};

const isTerminalJob = (job: SynonBiomedComputeJob): boolean =>
  ['done', 'completed', 'succeeded', 'failed', 'timed_out', 'orphaned', 'cancelled', 'canceled'].includes(
    job.state.trim().toLowerCase()
  ) || Boolean(job.endedAtIso);

const jobStateColor = (state: string): 'green' | 'red' | 'gray' => {
  const normalized = state.trim().toLowerCase();
  if (['running', 'harvesting'].includes(normalized)) return 'green';
  if (['failed', 'timed_out', 'cancelled', 'canceled'].includes(normalized)) return 'red';
  return 'gray';
};

const displayJobState = (state: string): string => {
  const normalized = state.trim().toLowerCase();
  if (normalized === 'harvesting') return 'running';
  if (normalized === 'timed_out') return 'timed out';
  return normalized || 'unknown';
};

const compactId = (value: string): string => (value.length > 12 ? `${value.slice(0, 10)}…` : value);

const safeExternalURL = (value: string | null): string | null => {
  if (!value) return null;
  try {
    const parsed = new URL(value);
    return parsed.protocol === 'https:' || parsed.protocol === 'http:' ? parsed.href : null;
  } catch {
    return null;
  }
};

const formatBytes = (value: number): string => {
  if (!Number.isFinite(value) || value <= 0) return '0 B';
  if (value < 1024) return `${Math.round(value)} B`;
  if (value < 1024 ** 2) return `${(value / 1024).toFixed(1)} KB`;
  return `${(value / 1024 ** 2).toFixed(1)} MB`;
};

export default ComputeJobsDialog;

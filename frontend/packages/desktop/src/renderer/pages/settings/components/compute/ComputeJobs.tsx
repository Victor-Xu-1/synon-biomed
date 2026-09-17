import { Button, Message, Modal, Spin, Switch, Tag } from '@arco-design/web-react';
import React, { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  loadSynonBiomedComputeJob,
  loadSynonBiomedComputeJobLog,
  loadSynonBiomedSessionComputeProviders,
  setSynonBiomedSessionComputeProvider,
  type SynonBiomedComputeJob,
  type SynonBiomedComputeJobLog,
  type SynonBiomedComputeProvider,
  type SynonBiomedManagedEndpoint,
} from '@/renderer/services/synonBiomedCompute';
import { ComputeDetail, computeStateLabel, normalizeComputeTestId } from './ComputeSettingsPrimitives';

export const ComputeJobRow: React.FC<{
  job: SynonBiomedComputeJob;
  onOpen: () => void;
}> = ({ job, onOpen }) => {
  const { t, i18n } = useTranslation();
  return (
    <div
      data-testid={`compute-job-${normalizeComputeTestId(job.jobId)}`}
      className='grid grid-cols-[minmax(0,1fr)_auto] items-center gap-12px px-14px py-12px border-b border-arco-2 last:border-b-0 hover:bg-fill-1'
    >
      <div className='min-w-0'>
        <div className='flex flex-wrap items-center gap-7px'>
          <span className='text-14px font-600 text-t-primary'>{job.providerLabel || job.provider || job.jobId}</span>
          <Tag size='small' color={computeStateTone(job.state)}>
            {computeStateLabel(job.state, t)}
          </Tag>
          {job.providerFamily ? (
            <Tag size='small' color='gray'>
              {job.providerFamily}
            </Tag>
          ) : null}
        </div>
        <div className='mt-4px flex flex-wrap gap-x-14px gap-y-2px text-12px text-t-tertiary'>
          <span>{[job.environment, job.tierType].filter(Boolean).join(' · ') || '-'}</span>
          <span className='font-mono'>{job.jobId}</span>
          <span>{formatComputeTime(job.startedAtIso ?? job.startedAt, i18n?.resolvedLanguage ?? i18n?.language)}</span>
        </div>
        {job.systemHint || job.errorKind ? (
          <div className={`mt-4px text-12px ${job.errorKind ? 'text-red-6' : 'text-t-secondary'}`}>
            {job.errorKind ?? job.systemHint}
          </div>
        ) : null}
      </div>
      <Button
        type='secondary'
        size='small'
        aria-label={`${t('settings.computeViewJob')} ${job.jobId}`}
        onClick={onOpen}
      >
        {t('settings.computeDetails')}
      </Button>
    </div>
  );
};

export const ComputeJobDetailModal: React.FC<{
  job: SynonBiomedComputeJob | null;
  providerNames: string[];
  onClose: () => void;
}> = ({ job, providerNames, onClose }) => {
  const { t, i18n } = useTranslation();
  const [detail, setDetail] = useState<SynonBiomedComputeJob | null>(null);
  const [logs, setLogs] = useState<Record<'stdout' | 'stderr', SynonBiomedComputeJobLog | null>>({
    stdout: null,
    stderr: null,
  });
  const [activeStream, setActiveStream] = useState<'stdout' | 'stderr'>('stdout');
  const [enabledProviders, setEnabledProviders] = useState<string[]>([]);
  const [pendingProvider, setPendingProvider] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [loadFailed, setLoadFailed] = useState(false);

  useEffect(() => {
    if (!job) return;
    let alive = true;
    setLoading(true);
    setLoadFailed(false);
    setDetail(null);
    setLogs({ stdout: null, stderr: null });
    setActiveStream('stdout');
    void loadSynonBiomedComputeJob(job.jobId)
      .then(async (nextDetail) => {
        const [stdout, stderr, sessionProviders] = await Promise.all([
          loadSynonBiomedComputeJobLog(job.jobId, {
            stream: 'stdout',
            tail: 65536,
          }).catch((): null => null),
          loadSynonBiomedComputeJobLog(job.jobId, {
            stream: 'stderr',
            tail: 65536,
          }).catch((): null => null),
          nextDetail.rootFrameId
            ? loadSynonBiomedSessionComputeProviders(nextDetail.rootFrameId).catch((): string[] => [])
            : Promise.resolve([]),
        ]);
        if (!alive) return;
        setDetail(nextDetail);
        setLogs({ stdout, stderr });
        setEnabledProviders(sessionProviders);
      })
      .catch((error) => {
        if (!alive) return;
        console.error('Failed to load compute job details:', error);
        setLoadFailed(true);
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [job]);

  const visibleProviders = useMemo(
    () => Array.from(new Set([...providerNames, ...enabledProviders])).sort((left, right) => left.localeCompare(right)),
    [enabledProviders, providerNames]
  );

  const toggleSessionProvider = async (providerName: string, checked: boolean) => {
    if (!detail?.rootFrameId) return;
    const previous = enabledProviders;
    setEnabledProviders((current) =>
      checked ? Array.from(new Set([...current, providerName])) : current.filter((name) => name !== providerName)
    );
    setPendingProvider(providerName);
    try {
      await setSynonBiomedSessionComputeProvider(detail.rootFrameId, providerName, checked);
    } catch (error) {
      setEnabledProviders(previous);
      console.error('Failed to update session compute provider:', error);
      Message.error(t('settings.computeWorkspace.sessionUpdateFailed'));
    } finally {
      setPendingProvider(null);
    }
  };

  const activeLog = logs[activeStream];
  return (
    <Modal
      visible={Boolean(job)}
      title={t('settings.computeJobDetails')}
      onCancel={onClose}
      footer={null}
      unmountOnExit
      style={{ width: 'min(860px, 94vw)' }}
    >
      {loading ? (
        <div className='h-260px flex items-center justify-center'>
          <Spin />
        </div>
      ) : loadFailed ? (
        <div role='alert' className='px-14px py-12px border border-red-2 bg-red-1 rd-6px text-12px text-red-6'>
          {t('settings.computeWorkspace.jobDetailLoadFailed')}
        </div>
      ) : detail ? (
        <div className='flex flex-col gap-16px'>
          <div className='flex flex-col sm:flex-row sm:items-start justify-between gap-10px'>
            <div className='min-w-0'>
              <div className='flex flex-wrap items-center gap-7px'>
                <h3 className='m-0 text-16px font-650 text-t-primary'>{detail.providerLabel || detail.provider}</h3>
                <Tag size='small' color={computeStateTone(detail.state)}>
                  {computeStateLabel(detail.state, t)}
                </Tag>
                <Tag size='small' color='gray'>
                  {[detail.environment, detail.tierType].filter(Boolean).join(' · ')}
                </Tag>
              </div>
              <div className='mt-4px text-12px text-t-tertiary font-mono break-all'>{detail.jobId}</div>
            </div>
            {detail.externalUrl ? (
              <a href={detail.externalUrl} target='_blank' rel='noreferrer' className='text-12px text-link-6 shrink-0'>
                {t('settings.computeOpenRemoteJob')}
              </a>
            ) : null}
          </div>

          <div className='grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-x-18px gap-y-10px py-12px border-y border-arco-2'>
            <ComputeDetail
              label={t('settings.computeStartedAt')}
              value={formatComputeTime(
                detail.startedAtIso ?? detail.startedAt,
                i18n?.resolvedLanguage ?? i18n?.language
              )}
            />
            <ComputeDetail
              label={t('settings.computeEndedAt')}
              value={formatComputeTime(detail.endedAtIso, i18n?.resolvedLanguage ?? i18n?.language)}
            />
            <ComputeDetail label={t('settings.computeProjectId')} value={detail.projectId || '-'} />
            <ComputeDetail label={t('settings.computeFrameId')} value={detail.frameId ?? '-'} />
            <ComputeDetail label={t('settings.computeRootFrameId')} value={detail.rootFrameId ?? '-'} />
            <ComputeDetail label={t('settings.computeExternalId')} value={detail.externalId ?? '-'} />
          </div>

          {detail.intent !== null || detail.hardwareDetails !== null || detail.systemHint || detail.errorKind ? (
            <section>
              <h4 className='m-0 mb-8px text-13px font-650 text-t-primary'>{t('settings.computeRunContext')}</h4>
              <div className='grid grid-cols-1 md:grid-cols-2 gap-8px'>
                {detail.intent !== null ? (
                  <StructuredField label={t('settings.computeWorkspace.intent')} value={detail.intent} />
                ) : null}
                {detail.hardwareDetails !== null ? (
                  <StructuredField label={t('settings.computeHardware')} value={detail.hardwareDetails} />
                ) : null}
                {detail.systemHint ? (
                  <StructuredField label={t('settings.computeSystemHint')} value={detail.systemHint} />
                ) : null}
                {detail.errorKind ? (
                  <StructuredField label={t('settings.computeError')} value={detail.errorKind} danger />
                ) : null}
              </div>
            </section>
          ) : null}

          <section>
            <div className='flex items-center justify-between gap-8px mb-8px'>
              <h4 className='m-0 text-13px font-650 text-t-primary'>{t('settings.computeLogs')}</h4>
              <div role='tablist' aria-label={t('settings.computeLogs')} className='flex p-2px bg-fill-2 rd-6px'>
                {(['stdout', 'stderr'] as const).map((stream) => (
                  <Button
                    key={stream}
                    type={activeStream === stream ? 'secondary' : 'text'}
                    size='mini'
                    role='tab'
                    aria-selected={activeStream === stream}
                    onClick={() => setActiveStream(stream)}
                  >
                    {stream === 'stdout' ? t('settings.computeStdout') : t('settings.computeStderr')}
                  </Button>
                ))}
              </div>
            </div>
            <pre className='m-0 min-h-120px max-h-260px overflow-auto whitespace-pre-wrap break-words bg-fill-1 border border-arco-2 rd-6px px-12px py-10px text-12px leading-5 font-mono text-t-secondary'>
              {activeLog?.content || t('settings.computeLogEmpty')}
            </pre>
            {activeLog ? (
              <div className='mt-5px text-11px text-t-tertiary'>
                {formatBytes(activeLog.size)}
                {activeLog.truncated ? ` · ${t('settings.computeLogTruncated')}` : ''}
              </div>
            ) : null}
          </section>

          {detail.rootFrameId && visibleProviders.length > 0 ? (
            <section className='border-t border-arco-2 pt-14px'>
              <h4 className='m-0 text-13px font-650 text-t-primary'>{t('settings.computeSessionProviders')}</h4>
              <p className='m-0 mt-3px mb-9px text-12px text-t-tertiary'>{t('settings.computeSessionProvidersHint')}</p>
              <div className='flex flex-col border border-arco-2 rd-6px overflow-hidden'>
                {visibleProviders.map((providerName) => (
                  <div
                    key={providerName}
                    className='flex items-center justify-between gap-10px px-12px py-9px border-b border-arco-2 last:border-b-0'
                  >
                    <span className='text-12px text-t-primary font-mono break-all'>{providerName}</span>
                    <Switch
                      size='small'
                      aria-label={`${t('settings.computeSessionToggle')} ${providerName}`}
                      checked={enabledProviders.includes(providerName)}
                      loading={pendingProvider === providerName}
                      onChange={(checked) => void toggleSessionProvider(providerName, checked)}
                    />
                  </div>
                ))}
              </div>
            </section>
          ) : null}
        </div>
      ) : null}
    </Modal>
  );
};

const StructuredField: React.FC<{
  label: string;
  value: unknown;
  danger?: boolean;
}> = ({ label, value, danger }) => (
  <div className={`px-11px py-9px border rd-6px ${danger ? 'border-red-2 bg-red-1' : 'border-arco-2 bg-fill-1'}`}>
    <div className='text-11px text-t-tertiary mb-4px'>{label}</div>
    <pre
      className={`m-0 whitespace-pre-wrap break-words text-12px leading-5 font-mono ${
        danger ? 'text-red-6' : 'text-t-secondary'
      }`}
    >
      {formatStructuredValue(value)}
    </pre>
  </div>
);

export function buildSessionProviderNames(
  providers: SynonBiomedComputeProvider[],
  endpoints: SynonBiomedManagedEndpoint[],
  job: SynonBiomedComputeJob | null
): string[] {
  const names = providers.filter((provider) => provider.family === 'infer').map((provider) => provider.name);
  names.push(...endpoints.map((endpoint) => `infer:${endpoint.name}`));
  if (job?.providerFamily && job.provider)
    names.push(job.provider.includes(':') ? job.provider : `${job.providerFamily}:${job.provider}`);
  return Array.from(new Set(names.filter(Boolean))).toSorted((left, right) => left.localeCompare(right));
}

const computeStateTone = (state: string): string => {
  const normalized = state.toLowerCase();
  if (['completed', 'succeeded', 'success'].includes(normalized)) return 'green';
  if (['failed', 'error', 'cancelled', 'canceled'].includes(normalized)) return 'red';
  if (['running', 'starting', 'queued', 'pending'].includes(normalized)) return 'arcoblue';
  return 'gray';
};

const formatComputeTime = (value: string | null, locale?: string): string => {
  if (!value) return '-';
  const numeric = Number(value);
  const date = Number.isFinite(numeric) && /^\d+$/.test(value) ? new Date(numeric) : new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString(locale, { hour12: false });
};

const formatStructuredValue = (value: unknown): string => {
  if (typeof value === 'string') return value;
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
};

const formatBytes = (size: number): string => {
  if (size < 1024) return `${size} B`;
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`;
  return `${(size / (1024 * 1024)).toFixed(1)} MB`;
};

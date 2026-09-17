import { Button, Tag, Tooltip } from '@arco-design/web-react';
import { Delete, Edit, Refresh } from '@icon-park/react';
import React from 'react';
import { useTranslation } from 'react-i18next';
import type { SynonBiomedComputeProvider, SynonBiomedManagedEndpoint } from '@/renderer/services/synonBiomedCompute';
import { ComputeDetail, computeStateLabel, normalizeComputeTestId } from './ComputeSettingsPrimitives';

const familyTone: Record<string, string> = {
  infer: 'arcoblue',
  byoc: 'green',
  ssh: 'purple',
  local: 'gray',
  proxy: 'orange',
};

export const ManagedEndpointRow: React.FC<{
  endpoint: SynonBiomedManagedEndpoint;
  pending: boolean;
  onStop: () => void;
}> = ({ endpoint, pending, onStop }) => {
  const { t } = useTranslation();
  const isLive = ['live', 'running', 'ready'].includes(endpoint.state.toLowerCase());
  return (
    <div className='compute-endpoint-row flex flex-col md:flex-row md:items-center justify-between gap-10px px-14px py-12px border border-arco-2 rd-6px'>
      <div className='min-w-0'>
        <div className='flex flex-wrap items-center gap-7px'>
          <span className='text-14px font-600 text-t-primary'>{endpoint.displayName}</span>
          <Tag size='small' color={isLive ? 'green' : endpoint.lastError ? 'red' : 'gray'}>
            {computeStateLabel(endpoint.state, t)}
          </Tag>
          <Tag size='small' color='gray'>
            {endpoint.location}
          </Tag>
        </div>
        <div className='mt-3px text-12px text-t-tertiary truncate'>
          {endpoint.endpoint ?? endpoint.serviceDir ?? endpoint.name}
        </div>
        {endpoint.lastError ? <div className='mt-4px text-12px text-red-6'>{endpoint.lastError}</div> : null}
      </div>
      <Button
        type='secondary'
        size='small'
        status='warning'
        loading={pending}
        disabled={!isLive}
        aria-label={`${t('settings.computeStop')} ${endpoint.displayName}`}
        onClick={onStop}
      >
        {t('settings.computeStop')}
      </Button>
    </div>
  );
};

export const ComputeProviderCard: React.FC<{
  provider: SynonBiomedComputeProvider;
  pending: boolean;
  onProbe: () => void;
  onEdit: () => void;
  onDelete: () => void;
}> = ({ provider, pending, onProbe, onEdit, onDelete }) => {
  const { t } = useTranslation();
  const hasError = Boolean(provider.probeError);
  const credential = provider.credentialStatus;
  const statusLabel = provider.checked
    ? hasError
      ? t('settings.computeStatusAttention')
      : t('settings.computeStatusHealthy')
    : t('settings.computeStatusUnchecked');
  const summaryDetails = [
    provider.endpoint ? { label: t('settings.computeEndpoint'), value: provider.endpoint } : null,
    provider.skillName ? { label: t('settings.computeSkill'), value: provider.skillName } : null,
    provider.location ? { label: t('settings.computeLocation'), value: provider.location } : null,
    provider.scratchRoot ? { label: t('settings.computeScratchRoot'), value: provider.scratchRoot } : null,
    (provider.dataRoots ?? []).length > 0
      ? {
          label: t('settings.computeDataRoots'),
          value: (provider.dataRoots ?? []).join(', '),
        }
      : null,
    provider.maxConcurrentJobs !== undefined && provider.maxConcurrentJobs !== null
      ? {
          label: t('settings.computeConcurrency'),
          value: String(provider.maxConcurrentJobs),
        }
      : null,
  ].filter((item): item is { label: string; value: string } => Boolean(item));

  return (
    <div
      data-testid={`synon-biomed-compute-provider-${normalizeComputeTestId(provider.name)}`}
      className='compute-provider-card px-14px py-12px flex flex-col gap-10px'
    >
      <div className='flex flex-col md:flex-row md:items-start justify-between gap-10px'>
        <div className='min-w-0'>
          <div className='flex flex-wrap items-center gap-7px min-w-0'>
            <h3 className='m-0 text-15px font-650 text-t-primary truncate'>{provider.displayName}</h3>
            <Tag size='small' color={familyTone[provider.family] ?? 'gray'}>
              {provider.family}
            </Tag>
            <Tag size='small' color={provider.checked && !hasError ? 'green' : hasError ? 'red' : 'gray'}>
              {statusLabel}
            </Tag>
          </div>
          <div className='text-12px text-t-tertiary mt-3px truncate'>{provider.name}</div>
          {credential ? (
            <div className={`text-12px mt-5px ${credential.resolved ? 'text-green-6' : 'text-red-6'}`}>
              {credential.name}:{' '}
              {credential.resolved ? t('settings.computeCredentialResolved') : t('settings.computeCredentialMissing')}
            </div>
          ) : null}
        </div>
        <div className='flex items-center gap-4px shrink-0'>
          <Button
            type='secondary'
            size='small'
            icon={<Refresh theme='outline' size='14' />}
            aria-label={`${t('settings.computeProbe')} ${provider.displayName}`}
            loading={pending}
            onClick={onProbe}
          >
            {t('settings.computeProbe')}
          </Button>
          <Tooltip content={t('common.edit')}>
            <Button
              type='text'
              icon={<Edit theme='outline' size='16' />}
              aria-label={`${t('common.edit')} ${provider.displayName}`}
              onClick={onEdit}
            />
          </Tooltip>
          <Tooltip content={t('common.delete')}>
            <Button
              type='text'
              status='danger'
              icon={<Delete theme='outline' size='16' />}
              aria-label={`${t('common.delete')} ${provider.displayName}`}
              onClick={onDelete}
            />
          </Tooltip>
        </div>
      </div>
      {summaryDetails.length > 0 ? (
        <div className='grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-x-16px gap-y-8px text-12px border-t border-arco-2 pt-10px'>
          {summaryDetails.map((item) => (
            <ComputeDetail key={item.label} label={item.label} value={item.value} />
          ))}
        </div>
      ) : null}
      {provider.probeError ? (
        <div className='text-12px leading-5 text-red-6 bg-red-1 border border-red-2 rd-8px px-10px py-8px'>
          {provider.probeError}
        </div>
      ) : null}
    </div>
  );
};

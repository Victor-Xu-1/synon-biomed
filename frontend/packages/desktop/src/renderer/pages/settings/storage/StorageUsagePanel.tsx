import React, { useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  loadSynonBiomedEnvironmentUsage,
  type SynonBiomedDiskUsage,
} from '@/renderer/services/synonBiomedWorkspaceSettings';
import { RefreshButton, SettingsSection } from '../components/SettingsPrimitives';
import { StorageError, StorageLoading } from './StorageFeedback';
import { formatStorageBytes } from './storagePresentation';
import { useStorageResource, type StorageResource } from './useStorageResource';

const categories = [
  { key: 'artifactsBytes', id: 'artifacts', label: 'artifacts' },
  { key: 'workspaceBytes', id: 'workspace', label: 'workspace' },
  { key: 'condaBytes', id: 'runtime', label: 'runtimeStorage' },
  { key: 'toolResultsBytes', id: 'tools', label: 'toolResults' },
  { key: 'logsBytes', id: 'logs', label: 'taskLogs' },
  { key: 'tempBytes', id: 'temp', label: 'tempDirectory' },
] as const;

const loadEnvironments = (signal: AbortSignal, refresh: boolean) =>
  loadSynonBiomedEnvironmentUsage({ signal, refresh });

function ScanTime({ value }: { value?: string | null }) {
  const { t, i18n } = useTranslation();
  const parsed = value ? new Date(value) : null;
  if (!parsed || !Number.isFinite(parsed.getTime())) return <span>{t('settings.storageSettings.noScanTime')}</span>;
  return (
    <span>
      {t('settings.storageSettings.scannedAt')}{' '}
      <time dateTime={value!}>
        {parsed.toLocaleString(i18n.language, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })}
      </time>
    </span>
  );
}

export function StorageUsagePanel({
  resource,
  onManageSoftware,
}: {
  resource: StorageResource<SynonBiomedDiskUsage>;
  onManageSoftware?: () => void;
}) {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState(false);
  const environments = useStorageResource(loadEnvironments, false);
  const usage = resource.data;
  const total = categories.reduce((sum, category) => sum + (usage?.[category.key] ?? 0), 0);
  const hasMeasurement = categories.some((category) => usage?.[category.key] != null);
  const complete = usage && categories.every((category) => usage[category.key] != null) && !usage.warnings?.length;
  const refresh = () => {
    void resource.refresh(true);
    if (expanded) void environments.refresh(true);
  };
  const toggle = () => {
    setExpanded((current) => !current);
    if (!expanded) void environments.refresh(false);
    else environments.cancel();
  };
  return (
    <SettingsSection
      className='storage-usage-panel'
      title={t('settings.storageSettings.diskUsage')}
      description={t('settings.storageSettings.usageDescription')}
      actions={<RefreshButton loading={resource.loading} onClick={refresh} />}
    >
      {resource.failed && <StorageError retained={!!usage} onRetry={refresh} />}
      {!usage && resource.loading && <StorageLoading scan />}
      {usage && (
        <>
          <div className='storage-usage-summary'>
            <div>
              <span className='storage-eyebrow'>
                {t(complete ? 'settings.storageSettings.measuredTotal' : 'settings.storageSettings.partialTotal')}
              </span>
              <strong>{formatStorageBytes(hasMeasurement ? total : null)}</strong>
            </div>
            <div className='storage-scan-meta'>
              <ScanTime value={usage.scannedAt} />
              {resource.loading && <span role='status'>{t('settings.storageSettings.refreshingUsage')}</span>}
            </div>
          </div>
          <div className='storage-usage-bar' aria-hidden='true'>
            {categories.map((category) => (
              <span
                key={category.id}
                data-storage-category={category.id}
                style={{ width: `${total > 0 ? ((usage[category.key] ?? 0) / total) * 100 : 0}%` }}
              />
            ))}
          </div>
          <div className='storage-usage-list'>
            {categories.map((category) => (
              <React.Fragment key={category.id}>
                <div className='storage-usage-row' data-storage-category={category.id}>
                  {category.id === 'runtime' ? (
                    <button
                      type='button'
                      className='storage-category-label'
                      aria-expanded={expanded}
                      aria-controls='storage-environment-details'
                      onClick={toggle}
                    >
                      <i aria-hidden='true' />
                      {t(`settings.storageSettings.${category.label}`)}
                      <span aria-hidden='true'>{expanded ? '−' : '+'}</span>
                    </button>
                  ) : (
                    <span className='storage-category-label'>
                      <i aria-hidden='true' />
                      {t(`settings.storageSettings.${category.label}`)}
                    </span>
                  )}
                  <strong>{formatStorageBytes(usage[category.key])}</strong>
                </div>
                {category.id === 'runtime' && expanded && (
                  <div id='storage-environment-details' className='storage-environment-details'>
                    <p>{t('settings.storageSettings.environmentDetailsHint')}</p>
                    {environments.failed && (
                      <StorageError retained={!!environments.data} onRetry={() => void environments.refresh(true)} />
                    )}
                    {environments.loading && !environments.data && <StorageLoading scan />}
                    {environments.data && (
                      <>
                        <div className='storage-environment-row'>
                          <span>{t('settings.storageSettings.packageCache')}</span>
                          <span>{formatStorageBytes(environments.data.packageCacheBytes)}</span>
                        </div>
                        {environments.data.environments.map((environment) => (
                          <div className='storage-environment-row' key={environment.name}>
                            <code>{environment.name}</code>
                            <span>{formatStorageBytes(environment.bytes)}</span>
                          </div>
                        ))}
                        {!environments.data.environments.length && (
                          <p>{t('settings.storageSettings.noEnvironments')}</p>
                        )}
                        {(environments.data.truncated || environments.data.warnings.length > 0) && (
                          <p role='status'>{t('settings.storageSettings.partialScan')}</p>
                        )}
                        <ScanTime value={environments.data.scannedAt} />
                      </>
                    )}
                    <button type='button' className='settings-action-button' onClick={onManageSoftware}>
                      {t('settings.storageSettings.openCompute')}
                    </button>
                  </div>
                )}
              </React.Fragment>
            ))}
          </div>
          {!!usage.warnings?.length && (
            <p className='storage-feedback' role='status'>
              {t('settings.storageSettings.partialScan')}
            </p>
          )}
          <div className='storage-capacity-row'>
            <span>{t('settings.storageSettings.volumeAvailable')}</span>
            <strong>{formatStorageBytes(usage.availableBytes)}</strong>
          </div>
          <p className='storage-caption'>
            {t(
              usage.accounting === 'logical-unique-within-category'
                ? 'settings.storageSettings.uniqueAccounting'
                : 'settings.storageSettings.entryAccounting'
            )}
          </p>
          <p className='storage-caption'>{t('settings.storageSettings.capacityHint')}</p>
        </>
      )}
    </SettingsSection>
  );
}

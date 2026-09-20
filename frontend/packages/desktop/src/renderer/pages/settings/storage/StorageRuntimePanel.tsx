import React, { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { loadScientificRuntimeSettings, retryScientificRuntime } from '@/renderer/services/scientificRuntimeSettings';
import { scientificRuntimePresentation } from '@/renderer/utils/scientificRuntimePresentation';
import { SettingsSection, RefreshButton } from '../components/SettingsPrimitives';
import { StorageError, StorageLoading } from './StorageFeedback';
import { StorageRuntimeSelectionDialog } from './StorageRuntimeSelectionDialog';
import { useStorageResource } from './useStorageResource';

const load = (signal: AbortSignal) => loadScientificRuntimeSettings({ signal });
const states: Record<string, string> = {
  waiting_for_selection: 'softwareWaiting',
  scheduled: 'softwareQueued',
  preparing: 'softwarePreparing',
  retrying: 'softwareRetrying',
  ready: 'softwareReady',
  failed: 'softwareFailed',
  stopped: 'softwareStopped',
  disabled: 'softwareUnavailable',
};
const activeStates = new Set(['scheduled', 'preparing', 'retrying']);

export function StorageRuntimePanel() {
  const { t } = useTranslation();
  const resource = useStorageResource(load);
  const [editing, setEditing] = useState(false);
  const [retrying, setRetrying] = useState<string | null>(null);
  const [retryFailed, setRetryFailed] = useState(false);
  const pending = useRef(false);
  const items = resource.data?.options ?? [];
  const active = items.some((item) => activeStates.has(item.status));
  useEffect(() => {
    // Poll only while an actual operation is active; failures require an
    // explicit retry, and unmount aborts the underlying request.
    if (!active || resource.loading || resource.failed) return;
    const timer = window.setTimeout(() => {
      void resource.refresh(false);
    }, 3000);
    return () => window.clearTimeout(timer);
  }, [active, resource.data, resource.loading, resource.failed, resource.refresh]);
  const retry = async (id: string) => {
    if (pending.current) return;
    pending.current = true;
    setRetrying(id);
    setRetryFailed(false);
    try {
      await retryScientificRuntime(id);
      await resource.refresh(false);
    } catch {
      setRetryFailed(true);
    } finally {
      pending.current = false;
      setRetrying(null);
    }
  };
  return (
    <SettingsSection
      className='storage-runtime-panel'
      title={t('settings.storageSettings.scientificTools')}
      description={t('settings.storageSettings.scientificToolsHint')}
      actions={
        <div className='storage-actions'>
          <RefreshButton loading={resource.loading} onClick={() => resource.refresh(false)} />
          <button
            type='button'
            className='settings-action-button'
            disabled={!resource.data || resource.failed || items.every((item) => !item.available)}
            onClick={() => setEditing(true)}
          >
            {t('settings.storageSettings.selectSoftware')}
          </button>
        </div>
      }
    >
      {resource.failed && <StorageError retained={!!resource.data} onRetry={() => void resource.refresh(false)} />}
      {resource.loading && !resource.data && <StorageLoading />}
      {resource.data && (
        <>
          <p className='storage-caption'>
            {resource.data.configured
              ? t('settings.storageSettings.softwareReadySummary', {
                  ready: items.filter((item) => item.status === 'ready').length,
                  total: items.length,
                })
              : t('settings.storageSettings.softwareNotConfirmed')}
          </p>
          <details className='storage-disclosure storage-runtime-disclosure' open={active || undefined}>
            <summary>{t('settings.storageSettings.softwareDetails')}</summary>
            <div className='storage-runtime-list'>
              {items.map((item) => {
                const presentation = scientificRuntimePresentation(item.id, t);
                return (
                  <div className='storage-runtime-row' key={item.id} data-testid={`storage-runtime-${item.id}`}>
                    <div className='storage-runtime-name'>
                      <strong>{presentation.title}</strong>
                      <span>{presentation.description}</span>
                    </div>
                    <div className='storage-runtime-status' data-status={item.status}>
                      <span>{t(`settings.storageSettings.${states[item.status] ?? 'softwareUnknown'}`)}</span>
                      {item.status === 'preparing' && item.phasePercent != null && (
                        <progress
                          value={item.phasePercent}
                          max={100}
                          aria-label={t('settings.storageSettings.softwarePhaseProgress')}
                        />
                      )}
                      {(item.status === 'failed' || item.status === 'stopped') && item.selected && (
                        <button
                          type='button'
                          className='settings-action-button'
                          disabled={retrying !== null}
                          onClick={() => void retry(item.id)}
                        >
                          {t(retrying === item.id ? 'settings.storageSettings.softwareRetrying' : 'common.retry')}
                        </button>
                      )}
                    </div>
                  </div>
                );
              })}
            </div>
          </details>
        </>
      )}
      {retryFailed && (
        <p className='storage-feedback storage-feedback--error' role='alert'>
          {t('settings.storageSettings.softwareRetryFailed')}
        </p>
      )}
      {editing && resource.data && (
        <StorageRuntimeSelectionDialog
          items={items}
          onClose={() => setEditing(false)}
          onSaved={() => {
            setEditing(false);
            void resource.refresh(false);
          }}
        />
      )}
    </SettingsSection>
  );
}

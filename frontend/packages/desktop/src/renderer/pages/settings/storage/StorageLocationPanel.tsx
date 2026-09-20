import { FolderOpen } from '@icon-park/react';
import React from 'react';
import { useTranslation } from 'react-i18next';
import type { SynonBiomedDataDirectory } from '@/renderer/services/synonBiomedWorkspaceSettings';
import { SettingsSection } from '../components/SettingsPrimitives';
import { StorageError, StorageLoading } from './StorageFeedback';
import { StoragePath } from './StoragePath';
import { directoryChangeAllowed, directorySourceKey } from './storagePresentation';
import type { StorageResource } from './useStorageResource';

export function StorageLocationPanel({
  resource,
  onChange,
  onFinishMove,
}: {
  resource: StorageResource<SynonBiomedDataDirectory>;
  onChange: () => void;
  onFinishMove: (remove: boolean) => void;
}) {
  const { t } = useTranslation();
  const directory = resource.data;
  const allowed = !resource.failed && !resource.loading && directoryChangeAllowed(directory);
  const disabledReason =
    !directory || directory.activeFrames == null
      ? 'loadingLocation'
      : directory.pendingMove
        ? 'movePendingRestart'
        : directory.activeFrames > 0
          ? 'stopTasksFirst'
          : 'changeLocation';
  return (
    <SettingsSection
      className='storage-location-panel'
      title={t('settings.storageSettings.dataLocation')}
      description={t('settings.storageSettings.locationHint')}
      actions={
        <button
          type='button'
          className='settings-action-button'
          disabled={!allowed}
          title={t(`settings.storageSettings.${disabledReason}`)}
          onClick={onChange}
        >
          {t('settings.storageSettings.changeLocation')}
        </button>
      }
    >
      {resource.failed && <StorageError retained={!!directory} onRetry={() => void resource.refresh()} />}
      {!directory && resource.loading && <StorageLoading />}
      {directory && (
        <>
          <div className='storage-location-body'>
            <div className='storage-location-icon' aria-hidden='true'>
              <FolderOpen size={25} />
            </div>
            <div className='storage-location-main'>
              <StoragePath value={directory.current} label={t('settings.storageSettings.rootDirectory')} />
              <div className='storage-location-meta'>
                <span className='storage-source-badge'>{t(directorySourceKey(directory.source))}</span>
                <span>{t('settings.storageSettings.directorySafety')}</span>
              </div>
              {directory.resolved && (
                <details className='storage-disclosure'>
                  <summary>{t('settings.storageSettings.resolvedPath')}</summary>
                  <StoragePath value={directory.resolved} label={t('settings.storageSettings.resolvedPath')} />
                </details>
              )}
            </div>
          </div>
          {(directory.activeFrames ?? 0) > 0 && (
            <p className='storage-feedback' role='status'>
              {t('settings.storageSettings.activeTasks', { count: directory.activeFrames })}
            </p>
          )}
          {directory.pendingMove && (
            <p className='storage-feedback' role='status'>
              {t('settings.storageSettings.movingTo')} <code>{directory.pendingMove.target}</code>{' '}
              {t('settings.storageSettings.movingToSuffix')}
            </p>
          )}
          {directory.lastMove && (
            <div className='storage-move-history'>
              <p>
                {t('settings.storageSettings.lastMove')} <code>{directory.lastMove.source}</code> →{' '}
                <code>{directory.lastMove.target}</code>
              </p>
              <div className='storage-actions'>
                <button type='button' className='settings-action-button' onClick={() => onFinishMove(false)}>
                  {t('settings.storageSettings.keepAndComplete')}
                </button>
                <button type='button' className='settings-text-danger-button' onClick={() => onFinishMove(true)}>
                  {t('settings.storageSettings.deleteAndComplete')}
                </button>
              </div>
            </div>
          )}
        </>
      )}
    </SettingsSection>
  );
}

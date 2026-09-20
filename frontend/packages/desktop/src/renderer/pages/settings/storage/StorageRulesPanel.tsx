import React from 'react';
import { useTranslation } from 'react-i18next';
import type { SynonBiomedStorageRules } from '@/renderer/services/synonBiomedWorkspaceSettings';
import { SettingsSection } from '../components/SettingsPrimitives';
import { StorageError, StorageLoading } from './StorageFeedback';
import { StoragePath } from './StoragePath';
import { storageRuleRows } from './storagePresentation';
import type { StorageResource } from './useStorageResource';

export function StorageRulesPanel({
  resource,
  canEdit,
  onEdit,
}: {
  resource: StorageResource<SynonBiomedStorageRules>;
  canEdit: boolean;
  onEdit: () => void;
}) {
  const { t } = useTranslation();
  return (
    <SettingsSection
      className='storage-rules-panel'
      title={t('settings.storageSettings.saveRules')}
      description={t('settings.storageSettings.rulesHint')}
      actions={
        <button
          type='button'
          className='settings-action-button'
          disabled={!canEdit || !resource.data || resource.failed}
          onClick={onEdit}
        >
          {t('settings.storageSettings.editRules')}
        </button>
      }
    >
      {resource.failed && <StorageError retained={!!resource.data} onRetry={() => void resource.refresh()} />}
      {!resource.data && resource.loading && <StorageLoading />}
      {resource.data && (
        <>
          <div className='storage-rule-list'>
            {storageRuleRows.map((row) => (
              <div className='storage-rule-row' key={row.key}>
                <div>
                  <h3>{t(row.label)}</h3>
                  <p>{t(row.description)}</p>
                </div>
                <code className='storage-relative-path'>{resource.data!.rules[row.key]}</code>
                <details className='storage-disclosure'>
                  <summary>{t('settings.storageSettings.fullPath')}</summary>
                  <StoragePath value={resource.data!.paths[row.key]} label={t(row.label)} />
                </details>
              </div>
            ))}
          </div>
          <details className='storage-disclosure storage-system-paths'>
            <summary>{t('settings.storageSettings.protectedDirectories')}</summary>
            <p>{t('settings.storageSettings.protectedDirectoriesHint')}</p>
            <StoragePath value={resource.data.systemPaths.taskRuns} label={t('settings.storageSettings.taskRecords')} />
            <StoragePath value={resource.data.systemPaths.workspace} label={t('settings.storageSettings.workspace')} />
          </details>
        </>
      )}
    </SettingsSection>
  );
}

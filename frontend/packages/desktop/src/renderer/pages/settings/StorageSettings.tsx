import React, { useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  loadSynonBiomedCloudCredentials,
  loadSynonBiomedDataDirectory,
  loadSynonBiomedDiskUsage,
  loadSynonBiomedStorageRules,
} from '@/renderer/services/synonBiomedWorkspaceSettings';
import SettingsPageHeader from './components/SettingsPageHeader';
import SettingsPageWrapper from './components/SettingsPageWrapper';
import { StorageCloudPanel } from './storage/StorageCloudPanel';
import { StorageLocationDialog, StorageMoveCompletionDialog, StorageRulesDialog } from './storage/StorageDialogs';
import { StorageLocationPanel } from './storage/StorageLocationPanel';
import { StorageRulesPanel } from './storage/StorageRulesPanel';
import { StorageUsagePanel } from './storage/StorageUsagePanel';
import { StorageRuntimePanel } from './storage/StorageRuntimePanel';
import { directoryChangeAllowed } from './storage/storagePresentation';
import { useStorageResource } from './storage/useStorageResource';

const loadDirectory = (signal: AbortSignal) => loadSynonBiomedDataDirectory({ signal, includeUsage: false });
const loadUsage = (signal: AbortSignal, refresh: boolean) => loadSynonBiomedDiskUsage({ signal, refresh });
const loadRules = (signal: AbortSignal) => loadSynonBiomedStorageRules({ signal });
const loadCloud = (signal: AbortSignal) => loadSynonBiomedCloudCredentials({ signal });

export const StorageSettingsContent: React.FC = () => {
  const { t } = useTranslation();
  const directory = useStorageResource(loadDirectory);
  const usage = useStorageResource(loadUsage);
  const rules = useStorageResource(loadRules);
  const cloud = useStorageResource(loadCloud);
  const [dialog, setDialog] = useState<'location' | 'rules' | 'keep-source' | 'delete-source' | null>(null);
  const canEdit = !directory.failed && !directory.loading && directoryChangeAllowed(directory.data);
  const refreshMetadata = () => {
    void directory.refresh();
    void rules.refresh();
    void cloud.refresh();
  };
  return (
    <SettingsPageWrapper>
      <div className='settings-storage-workspace' data-testid='synon-storage-settings'>
        <SettingsPageHeader
          title={t('settings.storageSettings.title')}
          description={t('settings.storageSettings.description')}
          actions={
            <button
              type='button'
              className='settings-action-button'
              disabled={directory.loading || rules.loading || cloud.loading}
              onClick={refreshMetadata}
            >
              {t('settings.storageSettings.refreshStatus')}
            </button>
          }
        />
        <StorageLocationPanel
          resource={directory}
          onChange={() => setDialog('location')}
          onFinishMove={(remove) => setDialog(remove ? 'delete-source' : 'keep-source')}
        />
        <div className='storage-detail-grid'>
          <StorageUsagePanel
            resource={usage}
            onManageSoftware={() => document.getElementById('storage-software')?.scrollIntoView({ block: 'start' })}
          />
          <StorageRulesPanel resource={rules} canEdit={canEdit} onEdit={() => setDialog('rules')} />
        </div>
        <div id='storage-software'>
          <StorageRuntimePanel />
        </div>
        <StorageCloudPanel resource={cloud} />
      </div>
      {dialog === 'location' && directory.data && (
        <StorageLocationDialog
          directory={directory.data}
          onClose={() => setDialog(null)}
          beforeSave={usage.cancel}
          onSaved={() => {
            setDialog(null);
            refreshMetadata();
            void usage.refresh(true);
          }}
        />
      )}
      {dialog === 'rules' && rules.data && (
        <StorageRulesDialog
          rules={rules.data}
          onClose={() => setDialog(null)}
          beforeSave={usage.cancel}
          onSaved={(value) => {
            rules.replace(value);
            setDialog(null);
            void usage.refresh(true);
          }}
        />
      )}
      {(dialog === 'keep-source' || dialog === 'delete-source') && directory.data?.lastMove && (
        <StorageMoveCompletionDialog
          remove={dialog === 'delete-source'}
          source={directory.data.lastMove.source}
          onClose={() => setDialog(null)}
          onSaved={() => {
            setDialog(null);
            void directory.refresh();
            void usage.refresh(true);
          }}
        />
      )}
    </SettingsPageWrapper>
  );
};

export default StorageSettingsContent;

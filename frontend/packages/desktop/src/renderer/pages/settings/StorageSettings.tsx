import { Checkbox, Input, Message, Modal, Spin, Tag } from '@arco-design/web-react';
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import { ipcBridge } from '@/common';
import { loadSynonBiomedProjects, type SynonBiomedProject } from '@/renderer/services/synonBiomedGateway';
import {
  changeSynonBiomedDataDirectory,
  clearSynonBiomedLastDataDirectoryMove,
  loadSynonBiomedCloudCredentials,
  loadSynonBiomedDataDirectory,
  loadSynonBiomedDiskUsage,
  loadSynonBiomedStorageRules,
  saveSynonBiomedStorageRules,
  testSynonBiomedCloudCredential,
  type SynonBiomedCloudCredential,
  type SynonBiomedDataDirectory,
  type SynonBiomedDiskUsage,
  type SynonBiomedStorageRuleValues,
  type SynonBiomedStorageRules,
} from '@/renderer/services/synonBiomedWorkspaceSettings';
import SettingsPageHeader from './components/SettingsPageHeader';
import SettingsPageWrapper from './components/SettingsPageWrapper';
import SynonBiomedCloudBrowser from './components/SynonBiomedCloudBrowser';
import { SettingsGeneratedEmptyArtwork } from './components/SettingsGeneratedAsset';
import { EmptyText, RefreshButton, SettingsSection } from './components/SettingsPrimitives';

export const StorageSettingsContent: React.FC = () => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [directory, setDirectory] = useState<SynonBiomedDataDirectory | null>(null);
  const [usage, setUsage] = useState<SynonBiomedDiskUsage | null>(null);
  const [storageRules, setStorageRules] = useState<SynonBiomedStorageRules | null>(null);
  const [cloud, setCloud] = useState<SynonBiomedCloudCredential[]>([]);
  const [projects, setProjects] = useState<SynonBiomedProject[]>([]);
  const [openCredentialId, setOpenCredentialId] = useState<string | null>(null);
  const [directoryLoading, setDirectoryLoading] = useState(false);
  const [usageLoading, setUsageLoading] = useState(false);
  const [storageRulesLoading, setStorageRulesLoading] = useState(false);
  const [cloudLoading, setCloudLoading] = useState(false);
  const [directoryFailed, setDirectoryFailed] = useState(false);
  const [usageFailed, setUsageFailed] = useState(false);
  const [storageRulesFailed, setStorageRulesFailed] = useState(false);
  const [cloudFailed, setCloudFailed] = useState(false);
  const [locationEditor, setLocationEditor] = useState(false);
  const [newLocation, setNewLocation] = useState('');
  const [migrateData, setMigrateData] = useState(true);
  const [savingLocation, setSavingLocation] = useState(false);
  const [clearMoveMode, setClearMoveMode] = useState<'keep-source' | 'delete-source' | null>(null);
  const [rulesEditor, setRulesEditor] = useState(false);
  const [draftRules, setDraftRules] = useState<SynonBiomedStorageRuleValues | null>(null);
  const [savingRules, setSavingRules] = useState(false);

  const refreshDirectory = useCallback(async () => {
    setDirectoryLoading(true);
    setDirectoryFailed(false);
    try {
      setDirectory(await loadSynonBiomedDataDirectory());
    } catch (error) {
      console.error('Failed to load data directory:', error);
      setDirectoryFailed(true);
    } finally {
      setDirectoryLoading(false);
    }
  }, []);

  const refreshUsage = useCallback(async () => {
    setUsageLoading(true);
    setUsageFailed(false);
    try {
      setUsage(await loadSynonBiomedDiskUsage());
    } catch (error) {
      console.error('Failed to scan disk usage:', error);
      setUsageFailed(true);
    } finally {
      setUsageLoading(false);
    }
  }, []);

  const refreshStorageRules = useCallback(async () => {
    setStorageRulesLoading(true);
    setStorageRulesFailed(false);
    try {
      setStorageRules(await loadSynonBiomedStorageRules());
    } catch (error) {
      console.error('Failed to load storage rules:', error);
      setStorageRulesFailed(true);
    } finally {
      setStorageRulesLoading(false);
    }
  }, []);

  const refreshCloud = useCallback(async () => {
    setCloudLoading(true);
    setCloudFailed(false);
    try {
      const [credentials, projectList] = await Promise.all([
        loadSynonBiomedCloudCredentials(),
        loadSynonBiomedProjects(),
      ]);
      setCloud(credentials);
      setProjects(projectList);
    } catch (error) {
      console.error('Failed to load cloud storage:', error);
      setCloudFailed(true);
    } finally {
      setCloudLoading(false);
    }
  }, []);

  const refresh = useCallback(() => {
    void refreshDirectory();
    void refreshUsage();
    void refreshStorageRules();
    void refreshCloud();
  }, [refreshCloud, refreshDirectory, refreshStorageRules, refreshUsage]);

  useEffect(() => refresh(), [refresh]);

  const canChangeLocation = directory !== null && directory.activeFrames === 0 && !directory.pendingMove;
  const normalizedNewLocation = newLocation.trim();
  const locationChanged = normalizedNewLocation !== '' && normalizedNewLocation !== directory?.current;
  const totalUsage = useMemo(
    () =>
      (usage?.artifactsBytes ?? 0) +
      (usage?.workspaceBytes ?? 0) +
      (usage?.toolResultsBytes ?? 0) +
      (usage?.condaBytes ?? 0),
    [usage]
  );

  const openLocationEditor = () => {
    if (!directory) return;
    setNewLocation(directory.current);
    setMigrateData(true);
    setLocationEditor(true);
  };

  const saveLocation = async () => {
    if (!locationChanged) return;
    setSavingLocation(true);
    try {
      const result = await changeSynonBiomedDataDirectory({ path: normalizedNewLocation, migrate: migrateData });
      setLocationEditor(false);
      Message.success(
        result.restarting
          ? t('settings.storageSettings.locationRestarting')
          : result.restartRequired
            ? t('settings.storageSettings.locationRestartRequired')
            : t('settings.storageSettings.locationUpdated')
      );
      if (!result.restarting) await refreshDirectory();
    } catch (error) {
      console.error('Failed to change data directory:', error);
      Message.error(t('settings.storageSettings.locationChangeFailed'));
    } finally {
      setSavingLocation(false);
    }
  };

  const chooseLocation = async () => {
    try {
      const selected = await ipcBridge.dialog.showOpen.invoke({ properties: ['openDirectory', 'createDirectory'] });
      if (selected?.[0]) setNewLocation(selected[0]);
    } catch (error) {
      console.error('Failed to choose data directory:', error);
      Message.error(t('settings.storageSettings.locationPickerFailed'));
    }
  };

  const openRulesEditor = () => {
    if (!storageRules) return;
    setDraftRules({ ...storageRules.rules });
    setRulesEditor(true);
  };

  const saveRules = async () => {
    if (!draftRules) return;
    setSavingRules(true);
    try {
      setStorageRules(await saveSynonBiomedStorageRules(draftRules));
      setRulesEditor(false);
      Message.success(t('settings.storageSettings.rulesUpdated'));
    } catch (error) {
      console.error('Failed to save storage rules:', error);
      Message.error(t('settings.storageSettings.rulesChangeFailed'));
    } finally {
      setSavingRules(false);
    }
  };

  const clearLastMove = async () => {
    if (!clearMoveMode) return;
    setSavingLocation(true);
    try {
      await clearSynonBiomedLastDataDirectoryMove(clearMoveMode === 'delete-source');
      setClearMoveMode(null);
      Message.success(t('settings.storageSettings.moveCompleted'));
      await refreshDirectory();
    } catch (error) {
      console.error('Failed to complete data move:', error);
      Message.error(t('settings.storageSettings.moveCompleteFailed'));
    } finally {
      setSavingLocation(false);
    }
  };

  const testConnection = async (credential: SynonBiomedCloudCredential) => {
    try {
      const result = await testSynonBiomedCloudCredential(credential.id);
      if (result.message) console.info('Cloud credential test result:', result.message);
      (result.success ? Message.success : Message.error)(
        result.success
          ? t('settings.storageSettings.connectionHealthy')
          : t('settings.storageSettings.connectionFailed')
      );
      await refreshCloud();
    } catch (error) {
      console.error('Failed to test cloud credential:', error);
      Message.error(t('settings.storageSettings.connectionTestFailed'));
    }
  };

  return (
    <SettingsPageWrapper>
      <div className='settings-storage-page flex flex-col' data-testid='synon-storage-settings'>
        <SettingsPageHeader
          title={t('settings.storageSettings.title')}
          description={t('settings.storageSettings.description')}
          actions={
            <RefreshButton
              loading={directoryLoading || usageLoading || storageRulesLoading || cloudLoading}
              onClick={refresh}
            />
          }
        />
        <SettingsSection
          className='storage-location-section'
          title={t('settings.storageSettings.dataLocation')}
          description={t('settings.storageSettings.dataLocationDescription')}
          icon='storage'
          actions={
            <button
              type='button'
              className='settings-action-button'
              disabled={!canChangeLocation}
              title={dataDirectoryDisabledReason(directory, t)}
              onClick={openLocationEditor}
            >
              {t('settings.storageSettings.changeLocation')}
            </button>
          }
        >
          {directoryLoading && !directory ? (
            <div className='h-82px flex-center'>
              <Spin />
            </div>
          ) : directoryFailed ? (
            <InlineError message={t('settings.storageSettings.directoryLoadFailed')} onRetry={refreshDirectory} />
          ) : (
            <div className='py-14px'>
              <code className='block text-12px text-t-primary break-all'>{directory?.current || '-'}</code>
              <div className='text-11px text-t-tertiary mt-6px'>
                {t('settings.storageSettings.directoryUsage', {
                  used: formatBytes(directory?.usageBytes ?? 0),
                  free: formatBytes(directory?.freeBytes ?? 0),
                  source: formatSource(directory?.source, t),
                })}
              </div>
              {(directory?.activeFrames ?? 0) > 0 ? (
                <div className='mt-9px text-12px text-warning-6'>
                  {t('settings.storageSettings.activeTasks', { count: directory?.activeFrames })}
                </div>
              ) : null}
              {directory?.pendingMove ? (
                <div className='mt-9px text-12px text-warning-6'>
                  {t('settings.storageSettings.movingTo')} <code>{directory.pendingMove.target}</code>
                  {t('settings.storageSettings.movingToSuffix')}
                </div>
              ) : null}
            </div>
          )}
          {directory?.lastMove ? (
            <div className='border-t border-arco-1 py-10px flex flex-wrap items-center gap-8px'>
              <div className='min-w-0 flex-1 text-11px text-t-secondary'>
                {t('settings.storageSettings.lastMove')} <code className='break-all'>{directory.lastMove.source}</code>{' '}
                → <code className='break-all'>{directory.lastMove.target}</code>
              </div>
              <button type='button' className='settings-action-button' onClick={() => setClearMoveMode('keep-source')}>
                {t('settings.storageSettings.keepAndComplete')}
              </button>
              <button
                type='button'
                className='settings-text-danger-button'
                onClick={() => setClearMoveMode('delete-source')}
              >
                {t('settings.storageSettings.deleteAndComplete')}
              </button>
            </div>
          ) : null}
        </SettingsSection>

        <div className='storage-secondary-grid'>
          <SettingsSection
            className='storage-rules-section'
            title={t('settings.storageSettings.saveRules')}
            description={t('settings.storageSettings.saveRulesDescription')}
            icon='storage'
            actions={
              <button
                type='button'
                className='settings-action-button'
                disabled={!storageRules || directory?.activeFrames !== 0}
                title={directory?.activeFrames ? t('settings.storageSettings.stopTasksFirst') : undefined}
                onClick={openRulesEditor}
              >
                {t('settings.storageSettings.editRules')}
              </button>
            }
          >
            {storageRulesLoading && !storageRules ? (
              <div className='h-104px flex-center'>
                <Spin />
              </div>
            ) : storageRulesFailed ? (
              <InlineError message={t('settings.storageSettings.rulesLoadFailed')} onRetry={refreshStorageRules} />
            ) : storageRules ? (
              <div className='py-10px'>
                <div className='pb-10px text-11px text-t-tertiary'>
                  {t('settings.storageSettings.rootDirectory')} <code className='break-all'>{storageRules.root}</code>
                </div>
                <div className='grid grid-cols-1 md:grid-cols-2 gap-x-20px'>
                  {storageRuleRows.map(({ key, label, description }) => (
                    <div key={key} className='min-w-0 py-10px border-t border-arco-1'>
                      <div className='text-12px font-600 text-t-primary'>{t(label)}</div>
                      <div className='text-11px text-t-tertiary mt-3px'>{t(description)}</div>
                      <code className='block text-11px text-t-secondary break-all mt-5px'>
                        {storageRules.rules[key]} → {storageRules.paths[key]}
                      </code>
                    </div>
                  ))}
                </div>
                <div className='border-t border-arco-1 pt-10px text-11px text-t-tertiary'>
                  {t('settings.storageSettings.systemDirectories', {
                    taskRuns: storageRules.systemPaths.taskRuns,
                    workspace: storageRules.systemPaths.workspace,
                  })}
                </div>
              </div>
            ) : null}
          </SettingsSection>

          <SettingsSection
            className='storage-usage-section'
            title={t('settings.storageSettings.diskUsage')}
            icon='storage'
          >
            {usageLoading && !usage ? (
              <div className='h-104px flex flex-col items-center justify-center gap-7px text-11px text-t-tertiary'>
                <Spin size={18} /> {t('settings.storageSettings.scanning')}
              </div>
            ) : usageFailed ? (
              <InlineError message={t('settings.storageSettings.usageLoadFailed')} onRetry={refreshUsage} />
            ) : (
              <>
                <div className='grid grid-cols-2 md:grid-cols-4 gap-12px py-14px'>
                  <Usage label={t('settings.storageSettings.artifacts')} value={usage?.artifactsBytes ?? 0} />
                  <Usage label={t('settings.storageSettings.workspace')} value={usage?.workspaceBytes ?? 0} />
                  <Usage label={t('settings.storageSettings.toolResults')} value={usage?.toolResultsBytes ?? 0} />
                  <Usage label={t('settings.storageSettings.condaEnvironments')} value={usage?.condaBytes ?? 0} />
                </div>
                <div className='border-t border-arco-1 py-10px flex flex-wrap justify-between gap-8px text-11px text-t-tertiary'>
                  <span>{t('settings.storageSettings.total', { value: formatBytes(totalUsage) })}</span>
                  <span>
                    {t('settings.storageSettings.availableSpace', { value: formatBytes(usage?.availableBytes ?? 0) })}
                  </span>
                </div>
              </>
            )}
          </SettingsSection>
        </div>

        <SettingsSection
          className='storage-cloud-section'
          title={t('settings.storageSettings.cloudStorage')}
          description={t('settings.storageSettings.cloudStorageDescription')}
          icon='storage'
          actions={
            <button
              type='button'
              className='settings-action-button'
              onClick={() => void navigate('/settings/credentials')}
            >
              {t('settings.storageSettings.openCredentials')}
            </button>
          }
        >
          {cloudLoading && cloud.length === 0 ? (
            <div className='h-84px flex-center'>
              <Spin />
            </div>
          ) : cloudFailed ? (
            <InlineError message={t('settings.storageSettings.cloudLoadFailed')} onRetry={refreshCloud} />
          ) : (
            cloud.map((credential) => {
              const open = openCredentialId === credential.id;
              return (
                <React.Fragment key={credential.id}>
                  <div className='flex flex-wrap sm:flex-nowrap items-center gap-10px min-h-54px py-8px border-b border-arco-1 last:border-b-0'>
                    <div className='min-w-0 flex-1'>
                      <div className='text-13px font-600 text-t-primary'>{credential.name}</div>
                      <div className='text-11px text-t-tertiary mt-2px'>
                        {credential.defaultBucket || t('settings.storageSettings.noDefaultBucket')}
                        {credential.region ? ` · ${credential.region}` : ''}
                      </div>
                    </div>
                    <Tag size='small' color={credential.connected ? 'green' : 'gray'}>
                      {credential.connected
                        ? t('settings.storageSettings.connected')
                        : t('settings.storageSettings.notConnected')}
                    </Tag>
                    <button
                      type='button'
                      className='settings-action-button'
                      disabled={!credential.connected}
                      onClick={() => setOpenCredentialId(open ? null : credential.id)}
                    >
                      {open ? t('settings.storageSettings.collapse') : t('settings.storageSettings.browse')}
                    </button>
                    <button
                      type='button'
                      className='settings-action-button'
                      onClick={() => void testConnection(credential)}
                    >
                      {t('settings.storageSettings.testConnection')}
                    </button>
                  </div>
                  {open ? <SynonBiomedCloudBrowser credential={credential} projects={projects} /> : null}
                </React.Fragment>
              );
            })
          )}
          {!cloudLoading && !cloudFailed && cloud.length === 0 ? (
            <EmptyText>
              <SettingsGeneratedEmptyArtwork id='storage' className='settings-empty-artwork-illustration' />
              {t('settings.storageSettings.cloudEmpty')}
            </EmptyText>
          ) : null}
        </SettingsSection>
      </div>

      <Modal
        title={t('settings.storageSettings.changeLocationTitle')}
        visible={locationEditor}
        onCancel={() => setLocationEditor(false)}
        onOk={() => void saveLocation()}
        confirmLoading={savingLocation}
        okButtonProps={{ disabled: !locationChanged }}
        okText={t('settings.storageSettings.changeLocation')}
        unmountOnExit
        style={{ maxWidth: 520, width: 'calc(100vw - 32px)' }}
      >
        <div className='flex flex-col gap-12px'>
          <label className='flex flex-col gap-6px'>
            <span className='text-12px font-600 text-t-primary'>{t('settings.storageSettings.newLocation')}</span>
            <div className='flex gap-8px'>
              <Input
                className='flex-1'
                value={newLocation}
                onChange={setNewLocation}
                aria-label={t('settings.storageSettings.newLocation')}
                placeholder={t('settings.storageSettings.newLocationPlaceholder')}
              />
              <button type='button' className='settings-action-button shrink-0' onClick={() => void chooseLocation()}>
                {t('settings.storageSettings.chooseLocation')}
              </button>
            </div>
          </label>
          <Checkbox checked={migrateData} disabled={!locationChanged} onChange={setMigrateData}>
            {t('settings.storageSettings.migrateExisting', { value: formatBytes(directory?.usageBytes ?? 0) })}
          </Checkbox>
          {directory?.source === 'flag' ? (
            <p className='m-0 text-11px leading-5 text-warning-7'>
              {t('settings.storageSettings.flagOverride', {
                path: directory.configPath || t('settings.storageSettings.configFile'),
              })}
            </p>
          ) : null}
          <p className='m-0 text-11px leading-5 text-t-tertiary'>
            {t('settings.storageSettings.locationRequirements')}
          </p>
        </div>
      </Modal>

      <Modal
        title={t('settings.storageSettings.editRulesTitle')}
        visible={rulesEditor}
        onCancel={() => setRulesEditor(false)}
        onOk={() => void saveRules()}
        confirmLoading={savingRules}
        okButtonProps={{ disabled: !draftRules || storageRuleRows.some(({ key }) => !draftRules[key].trim()) }}
        okText={t('common.save')}
        unmountOnExit
        style={{ maxWidth: 560, width: 'calc(100vw - 32px)' }}
      >
        <div className='flex flex-col gap-12px'>
          <p className='m-0 text-11px leading-5 text-t-tertiary'>{t('settings.storageSettings.rulesRequirements')}</p>
          {draftRules
            ? storageRuleRows.map(({ key, label, description }) => (
                <label key={key} className='flex flex-col gap-5px'>
                  <span className='text-12px font-600 text-t-primary'>{t(label)}</span>
                  <span className='text-11px text-t-tertiary'>{t(description)}</span>
                  <Input
                    value={draftRules[key]}
                    onChange={(value) => setDraftRules((current) => (current ? { ...current, [key]: value } : current))}
                    aria-label={t(label)}
                    placeholder='folder/subfolder'
                  />
                </label>
              ))
            : null}
        </div>
      </Modal>

      <Modal
        title={
          clearMoveMode === 'delete-source'
            ? t('settings.storageSettings.deleteOldLocationTitle')
            : t('settings.storageSettings.completeMoveTitle')
        }
        visible={clearMoveMode !== null}
        onCancel={() => setClearMoveMode(null)}
        onOk={() => void clearLastMove()}
        confirmLoading={savingLocation}
        okButtonProps={clearMoveMode === 'delete-source' ? { status: 'danger' } : undefined}
        okText={
          clearMoveMode === 'delete-source'
            ? t('settings.storageSettings.deleteAndCompleteShort')
            : t('settings.storageSettings.keepAndCompleteShort')
        }
        unmountOnExit
        style={{ maxWidth: 520, width: 'calc(100vw - 32px)' }}
      >
        {clearMoveMode === 'delete-source'
          ? t('settings.storageSettings.deleteOldLocationBody', { path: directory?.lastMove?.source ?? '' })
          : t('settings.storageSettings.completeMoveBody')}
      </Modal>
    </SettingsPageWrapper>
  );
};

const InlineError: React.FC<{ message: string; onRetry: () => void }> = ({ message, onRetry }) => {
  const { t } = useTranslation();
  return (
    <div role='alert' className='py-14px flex flex-wrap items-center justify-between gap-8px'>
      <span className='text-12px text-danger-6'>{message}</span>
      <button type='button' className='settings-action-button' onClick={onRetry}>
        {t('common.retry')}
      </button>
    </div>
  );
};

const Usage: React.FC<{ label: string; value: number }> = ({ label, value }) => (
  <div>
    <div className='text-18px font-650 text-t-primary'>{formatBytes(value)}</div>
    <div className='text-11px text-t-tertiary mt-3px'>{label}</div>
  </div>
);

function formatBytes(bytes: number): string {
  if (bytes <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  return `${(bytes / 1024 ** index).toFixed(index > 1 ? 1 : 0)} ${units[index]}`;
}

function formatSource(source: string | undefined, t: ReturnType<typeof useTranslation>['t']): string {
  return source === 'flag'
    ? t('settings.storageSettings.sourceFlag')
    : source === 'env'
      ? t('settings.storageSettings.sourceEnv')
      : source === 'config'
        ? t('settings.storageSettings.sourceConfig')
        : source === 'pointer'
          ? t('settings.storageSettings.sourcePointer')
          : t('settings.storageSettings.sourceDefault');
}

function dataDirectoryDisabledReason(
  directory: SynonBiomedDataDirectory | null,
  t: ReturnType<typeof useTranslation>['t']
): string {
  if (!directory) return t('settings.storageSettings.loadingLocation');
  if (directory.activeFrames > 0) return t('settings.storageSettings.stopTasksFirst');
  if (directory.pendingMove) return t('settings.storageSettings.movePendingRestart');
  return t('settings.storageSettings.changeLocation');
}

const storageRuleRows: Array<{
  key: keyof SynonBiomedStorageRuleValues;
  label: string;
  description: string;
}> = [
  {
    key: 'taskArtifacts',
    label: 'settings.storageSettings.taskArtifacts',
    description: 'settings.storageSettings.taskArtifactsDescription',
  },
  {
    key: 'logs',
    label: 'settings.storageSettings.taskLogs',
    description: 'settings.storageSettings.taskLogsDescription',
  },
  {
    key: 'toolResults',
    label: 'settings.storageSettings.toolResultsDirectory',
    description: 'settings.storageSettings.toolResultsDirectoryDescription',
  },
  {
    key: 'temp',
    label: 'settings.storageSettings.tempDirectory',
    description: 'settings.storageSettings.tempDirectoryDescription',
  },
];

const StorageSettings: React.FC = () => <StorageSettingsContent />;

export default StorageSettings;

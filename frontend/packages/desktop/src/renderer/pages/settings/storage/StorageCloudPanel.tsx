import { Message, Tag } from '@arco-design/web-react';
import { CloudStorage } from '@icon-park/react';
import React, { useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import { loadSynonBiomedProjects } from '@/renderer/services/synonBiomedGateway';
import {
  testSynonBiomedCloudCredential,
  type SynonBiomedCloudCredential,
} from '@/renderer/services/synonBiomedWorkspaceSettings';
import SynonBiomedCloudBrowser from '../components/SynonBiomedCloudBrowser';
import { SettingsSection } from '../components/SettingsPrimitives';
import { StorageError, StorageLoading } from './StorageFeedback';
import { useStorageResource, type StorageResource } from './useStorageResource';

const loadProjects = () => loadSynonBiomedProjects();

export function StorageCloudPanel({ resource }: { resource: StorageResource<SynonBiomedCloudCredential[]> }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [opened, setOpened] = useState<string | null>(null);
  const [testing, setTesting] = useState<string | null>(null);
  const pending = useRef(false);
  const projects = useStorageResource(loadProjects, false);
  const test = async (credential: SynonBiomedCloudCredential) => {
    if (pending.current) return;
    pending.current = true;
    setTesting(credential.id);
    try {
      const result = await testSynonBiomedCloudCredential(credential.id);
      (result.success ? Message.success : Message.error)(
        t(result.success ? 'settings.storageSettings.connectionHealthy' : 'settings.storageSettings.connectionFailed')
      );
      await resource.refresh();
    } catch {
      Message.error(t('settings.storageSettings.connectionTestFailed'));
    } finally {
      pending.current = false;
      setTesting(null);
    }
  };
  return (
    <SettingsSection
      className='storage-cloud-panel'
      title={t('settings.storageSettings.cloudStorage')}
      description={t('settings.storageSettings.cloudStorageDescription')}
      actions={
        <button type='button' className='settings-action-button' onClick={() => void navigate('/settings/credentials')}>
          {t('settings.storageSettings.openCredentials')}
        </button>
      }
    >
      {resource.failed && <StorageError retained={!!resource.data} onRetry={() => void resource.refresh()} />}
      {resource.loading && !resource.data && <StorageLoading />}
      {resource.data?.length === 0 && (
        <div className='storage-cloud-empty'>
          <CloudStorage size={27} aria-hidden='true' />
          <p>{t('settings.storageSettings.cloudEmpty')}</p>
        </div>
      )}
      {resource.data?.map((credential) => (
        <div className='storage-cloud-connection' key={credential.id}>
          <div className='storage-cloud-row'>
            <div className='storage-cloud-name'>
              <strong>{credential.name}</strong>
              <span>
                {credential.defaultBucket || t('settings.storageSettings.noDefaultBucket')}
                {credential.region ? ` · ${credential.region}` : ''}
              </span>
            </div>
            <Tag size='small' color={credential.connected ? 'green' : 'gray'}>
              {t(credential.connected ? 'settings.storageSettings.connected' : 'settings.storageSettings.notConnected')}
            </Tag>
            <div className='storage-actions'>
              <button
                type='button'
                className='settings-action-button'
                disabled={!credential.connected}
                aria-expanded={opened === credential.id}
                onClick={() => {
                  setOpened(opened === credential.id ? null : credential.id);
                  if (opened !== credential.id && !projects.data) void projects.refresh(false);
                }}
              >
                {t(opened === credential.id ? 'settings.storageSettings.collapse' : 'settings.storageSettings.browse')}
              </button>
              <button
                type='button'
                className='settings-action-button'
                disabled={testing !== null}
                onClick={() => void test(credential)}
              >
                {t('settings.storageSettings.testConnection')}
              </button>
            </div>
          </div>
          {opened === credential.id && (
            <div className='storage-cloud-browser'>
              {projects.loading && !projects.data && <StorageLoading />}
              {projects.failed && <StorageError onRetry={() => void projects.refresh()} />}
              {projects.data && <SynonBiomedCloudBrowser credential={credential} projects={projects.data} />}
            </div>
          )}
        </div>
      ))}
    </SettingsSection>
  );
}

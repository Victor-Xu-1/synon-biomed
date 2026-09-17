import { loadSynonBiomedComputeProviders, loadSynonBiomedLocalHostInfo } from '@/renderer/services/synonBiomedCompute';
import {
  loadSynonBiomedCloudBuckets,
  loadSynonBiomedCloudCredentials,
} from '@/renderer/services/synonBiomedWorkspaceSettings';
import { Alert, Button, Modal, Select, Spin } from '@arco-design/web-react';
import { FolderOpen, Refresh } from '@icon-park/react';
import React, { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { redactErrorText } from '@/renderer/pages/conversation/platforms/acp/errorDiagnostics';
import { FileBrowser, type SynonBiomedFilesHost } from './FileBrowser';

export const FilesOverlay: React.FC<{
  visible: boolean;
  projectId: string;
  onClose: () => void;
  onImported: () => void;
}> = ({ visible, projectId, onClose, onImported }) => {
  const { t } = useTranslation();
  const [hosts, setHosts] = useState<SynonBiomedFilesHost[]>([]);
  const [selectedHostId, setSelectedHostId] = useState('local');
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState(false);
  const [revision, setRevision] = useState(0);
  const [buckets, setBuckets] = useState<string[]>([]);
  const [bucket, setBucket] = useState('');
  const [bucketLoading, setBucketLoading] = useState(false);
  const [bucketError, setBucketError] = useState(false);

  useEffect(() => {
    if (!visible) return;
    let active = true;
    setLoading(true);
    setLoadError(false);
    void Promise.all([
      loadSynonBiomedLocalHostInfo(),
      loadSynonBiomedComputeProviders(),
      loadSynonBiomedCloudCredentials(),
    ])
      .then(([local, providers, credentials]) => {
        if (!active) return;
        const nextHosts: SynonBiomedFilesHost[] = [
          {
            id: 'local',
            kind: 'local',
            label: local.hostLabel,
            detail: local.hostDetail,
          },
          ...providers
            .filter((provider) => provider.family === 'ssh' || provider.name.startsWith('ssh:'))
            .map(
              (provider): SynonBiomedFilesHost => ({
                id: provider.name,
                kind: 'ssh',
                label: provider.displayName,
                detail: provider.probeError,
                providerName: provider.name,
              })
            ),
          ...credentials.map(
            (credential): SynonBiomedFilesHost => ({
              id: 'cloud:' + credential.id,
              kind: 'cloud',
              label: credential.name,
              detail: credential.connected ? credential.provider : t('conversation.fileBrowser.notConnected'),
              credential,
            })
          ),
        ];
        setHosts(nextHosts);
        setSelectedHostId((current) => (nextHosts.some((host) => host.id === current) ? current : 'local'));
      })
      .catch((error: unknown) => {
        console.warn('[FilesOverlay] Failed to load file hosts:', diagnostic(error));
        if (active) setLoadError(true);
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [revision, t, visible]);

  const selectedHost = useMemo(
    () => hosts.find((host) => host.id === selectedHostId) ?? hosts[0],
    [hosts, selectedHostId]
  );

  useEffect(() => {
    if (!visible || selectedHost?.kind !== 'cloud') {
      setBuckets([]);
      setBucket('');
      setBucketError(false);
      return;
    }
    let active = true;
    setBucketLoading(true);
    setBucketError(false);
    void loadSynonBiomedCloudBuckets(selectedHost.credential.id)
      .then((nextBuckets) => {
        if (!active) return;
        setBuckets(nextBuckets);
        setBucket((current) => {
          if (current && nextBuckets.includes(current)) return current;
          return selectedHost.credential.defaultBucket && nextBuckets.includes(selectedHost.credential.defaultBucket)
            ? selectedHost.credential.defaultBucket
            : (nextBuckets[0] ?? '');
        });
      })
      .catch((error: unknown) => {
        console.warn('[FilesOverlay] Failed to load cloud buckets:', diagnostic(error));
        if (active) {
          setBuckets([]);
          setBucket('');
          setBucketError(true);
        }
      })
      .finally(() => {
        if (active) setBucketLoading(false);
      });
    return () => {
      active = false;
    };
  }, [selectedHost, visible]);

  return (
    <Modal
      visible={visible}
      title={
        <span className='flex items-center gap-8px'>
          <FolderOpen theme='outline' />
          {t('conversation.fileBrowser.workbench')}
        </span>
      }
      footer={null}
      unmountOnExit
      onCancel={onClose}
      style={{ width: 1180, maxWidth: 'calc(100vw - 32px)' }}
    >
      <div className='min-h-0 flex flex-col' style={{ height: 'min(720px, calc(100vh - 150px))' }}>
        <div className='h-48px shrink-0 flex flex-wrap items-center gap-8px border-b border-arco-2 px-12px'>
          <Select
            aria-label={t('conversation.fileBrowser.host')}
            value={selectedHostId}
            loading={loading}
            className='w-240px max-w-full'
            onChange={setSelectedHostId}
          >
            {hosts.map((host) => (
              <Select.Option key={host.id} value={host.id}>
                {host.label}
                {host.detail ? ' · ' + host.detail : ''}
              </Select.Option>
            ))}
          </Select>
          {selectedHost?.kind === 'cloud' && (
            <Select
              aria-label={t('conversation.fileBrowser.bucket')}
              value={bucket || undefined}
              loading={bucketLoading}
              placeholder={t('conversation.fileBrowser.selectBucket')}
              className='w-240px max-w-full'
              onChange={setBucket}
            >
              {buckets.map((item) => (
                <Select.Option key={item} value={item}>
                  {item}
                </Select.Option>
              ))}
            </Select>
          )}
          <div className='flex-1' />
          <Button
            type='text'
            icon={<Refresh theme='outline' />}
            loading={loading}
            onClick={() => setRevision((value) => value + 1)}
          >
            {t('common.refresh')}
          </Button>
        </div>

        {loading && hosts.length === 0 ? (
          <div className='min-h-0 flex-1 flex-center' role='status'>
            <Spin />
          </div>
        ) : loadError ? (
          <div className='min-h-0 flex-1 flex flex-col items-center justify-center gap-10px p-20px'>
            <Alert type='error' content={t('conversation.fileBrowser.hostLoadFailed')} />
            <Button onClick={() => setRevision((value) => value + 1)}>{t('common.retry')}</Button>
          </div>
        ) : bucketError ? (
          <div className='min-h-0 flex-1 flex-center p-20px'>
            <Alert type='error' content={t('conversation.fileBrowser.bucketLoadFailed')} />
          </div>
        ) : selectedHost ? (
          <FileBrowser
            key={selectedHost.id + ':' + bucket}
            host={selectedHost}
            cloudBucket={bucket}
            projectId={projectId}
            onImported={onImported}
          />
        ) : null}
      </div>
    </Modal>
  );
};

const diagnostic = (error: unknown): string =>
  redactErrorText(error instanceof Error ? error.message : String(error || 'unknown error'));

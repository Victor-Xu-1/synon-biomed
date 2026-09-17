import { Message, Modal, Select, Spin } from '@arco-design/web-react';
import { Download, FolderClose, Left, Refresh, Upload } from '@icon-park/react';
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { SynonBiomedProject } from '@/renderer/services/synonBiomedGateway';
import {
  getSynonBiomedCloudDownloadUrl,
  importSynonBiomedCloudObject,
  loadSynonBiomedCloudBuckets,
  loadSynonBiomedCloudFolder,
  type SynonBiomedCloudCredential,
  type SynonBiomedCloudFolder,
  type SynonBiomedCloudObject,
} from '@/renderer/services/synonBiomedWorkspaceSettings';

type Props = {
  credential: SynonBiomedCloudCredential;
  projects: SynonBiomedProject[];
};

const EMPTY_FOLDER: SynonBiomedCloudFolder = { folders: [], files: [] };

const SynonBiomedCloudBrowser: React.FC<Props> = ({ credential, projects }) => {
  const { t, i18n } = useTranslation();
  const [buckets, setBuckets] = useState<string[]>([]);
  const [bucket, setBucket] = useState(credential.defaultBucket ?? '');
  const [prefix, setPrefix] = useState('');
  const [folder, setFolder] = useState(EMPTY_FOLDER);
  const [loading, setLoading] = useState(true);
  const [importing, setImporting] = useState<SynonBiomedCloudObject | null>(null);
  const [projectId, setProjectId] = useState(projects[0]?.projectId ?? '');

  const loadBuckets = useCallback(async () => {
    setLoading(true);
    try {
      const values = await loadSynonBiomedCloudBuckets(credential.id);
      setBuckets(values);
      setBucket((current) => current || credential.defaultBucket || values[0] || '');
    } catch (error) {
      console.error('Failed to load cloud buckets:', error);
      Message.error(t('settings.storageSettings.browser.loadBucketsFailed'));
    } finally {
      setLoading(false);
    }
  }, [credential.defaultBucket, credential.id, t]);

  const loadFolder = useCallback(async () => {
    if (!bucket) {
      setFolder(EMPTY_FOLDER);
      return;
    }
    setLoading(true);
    try {
      setFolder(await loadSynonBiomedCloudFolder(credential.id, bucket, prefix));
    } catch (error) {
      console.error('Failed to load cloud folder:', error);
      Message.error(t('settings.storageSettings.browser.loadFolderFailed'));
    } finally {
      setLoading(false);
    }
  }, [bucket, credential.id, prefix, t]);

  useEffect(() => void loadBuckets(), [loadBuckets]);
  useEffect(() => void loadFolder(), [loadFolder]);

  const parentPrefix = useMemo(() => {
    const normalized = prefix.replace(/\/$/, '');
    const index = normalized.lastIndexOf('/');
    return index >= 0 ? `${normalized.slice(0, index + 1)}` : '';
  }, [prefix]);

  const runImport = async () => {
    if (!importing || !projectId) return;
    try {
      await importSynonBiomedCloudObject(credential.id, { bucket, key: importing.key, projectId });
      Message.success(t('settings.storageSettings.browser.imported', { name: fileName(importing.key) }));
      setImporting(null);
    } catch (error) {
      console.error('Failed to import cloud object:', error);
      Message.error(t('settings.storageSettings.browser.importFailed'));
    }
  };

  return (
    <div className='border-t border-arco-1 bg-fill-1 px-12px py-10px' data-testid={`cloud-browser-${credential.id}`}>
      <div className='flex flex-wrap items-center gap-8px'>
        <Select
          value={bucket}
          onChange={(value) => {
            setBucket(value);
            setPrefix('');
          }}
          placeholder={t('settings.storageSettings.browser.selectBucket')}
          className='min-w-180px'
          aria-label={t('settings.storageSettings.browser.selectBucket')}
        >
          {buckets.map((value) => (
            <Select.Option key={value} value={value}>
              {value}
            </Select.Option>
          ))}
        </Select>
        <button
          type='button'
          className='settings-icon-button'
          title={t('settings.storageSettings.browser.refreshFolder')}
          onClick={() => void loadFolder()}
        >
          <Refresh size='14' />
        </button>
        <code className='min-w-0 flex-1 truncate text-11px text-t-tertiary'>/{prefix}</code>
      </div>

      {loading ? (
        <div className='h-96px flex-center'>
          <Spin />
        </div>
      ) : !bucket ? (
        <div className='py-18px text-center text-12px text-t-tertiary'>
          {t('settings.storageSettings.browser.noBuckets')}
        </div>
      ) : (
        <div className='mt-9px border border-arco-1 bg-1'>
          {prefix ? (
            <button
              type='button'
              className='w-full h-38px px-10px flex items-center gap-8px border-0 border-b border-arco-1 bg-transparent text-12px text-t-secondary hover:bg-fill-1'
              onClick={() => setPrefix(parentPrefix)}
            >
              <Left size='13' /> {t('settings.storageSettings.browser.parentFolder')}
            </button>
          ) : null}
          {folder.folders.map((value) => (
            <button
              key={value}
              type='button'
              className='w-full h-40px px-10px flex items-center gap-8px border-0 border-b border-arco-1 bg-transparent text-12px text-t-primary hover:bg-fill-1'
              onClick={() => setPrefix(value)}
            >
              <FolderClose size='15' className='text-t-tertiary' />
              <span className='truncate'>{folderName(value)}</span>
            </button>
          ))}
          {folder.files.map((item) => (
            <div
              key={item.key}
              className='min-h-42px px-10px py-6px flex items-center gap-8px border-b border-arco-1 last:border-b-0'
            >
              <div className='min-w-0 flex-1'>
                <div className='truncate text-12px text-t-primary'>{fileName(item.key)}</div>
                <div className='text-10px text-t-tertiary mt-2px'>
                  {formatBytes(item.size)}
                  {item.lastModified ? ` · ${formatDate(item.lastModified, i18n.resolvedLanguage)}` : ''}
                </div>
              </div>
              <a
                className='settings-icon-button'
                href={getSynonBiomedCloudDownloadUrl(credential.id, bucket, item.key)}
                download={fileName(item.key)}
                title={t('settings.storageSettings.browser.download')}
              >
                <Download size='14' />
              </a>
              <button
                type='button'
                className='settings-icon-button'
                title={t('settings.storageSettings.browser.importProject')}
                onClick={() => setImporting(item)}
              >
                <Upload size='14' />
              </button>
            </div>
          ))}
          {folder.folders.length === 0 && folder.files.length === 0 ? (
            <div className='py-18px text-center text-12px text-t-tertiary'>
              {t('settings.storageSettings.browser.emptyFolder')}
            </div>
          ) : null}
        </div>
      )}

      <Modal
        title={t('settings.storageSettings.browser.importTitle', {
          name: importing ? fileName(importing.key) : '',
        })}
        visible={importing !== null}
        onCancel={() => setImporting(null)}
        onOk={() => void runImport()}
        okButtonProps={{ disabled: !projectId }}
      >
        <div className='flex flex-col gap-8px'>
          <div className='text-12px text-t-secondary'>{t('settings.storageSettings.browser.selectTargetProject')}</div>
          <Select
            value={projectId}
            onChange={setProjectId}
            placeholder={t('settings.storageSettings.browser.selectProject')}
          >
            {projects.map((project) => (
              <Select.Option key={project.projectId} value={project.projectId}>
                {project.name}
              </Select.Option>
            ))}
          </Select>
        </div>
      </Modal>
    </div>
  );
};

function folderName(value: string): string {
  return value.replace(/\/$/, '').split('/').at(-1) || value;
}
function fileName(value: string): string {
  return value.split('/').at(-1) || value;
}
function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 ** 2) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / 1024 ** 2).toFixed(1)} MB`;
}
function formatDate(value: string, locale?: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString(locale);
}

export default SynonBiomedCloudBrowser;

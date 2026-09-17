import {
  getSynonBiomedArtifactContentUrl,
  type SynonBiomedProjectArtifact,
} from '@/renderer/services/synonBiomedGateway';
import {
  exportSynonBiomedArtifactsToCloud,
  loadSynonBiomedCloudBuckets,
  loadSynonBiomedCloudCredentials,
  type SynonBiomedCloudCredential,
} from '@/renderer/services/synonBiomedWorkspaceSettings';
import { Button, Empty, Input, Message, Modal, Progress, Select } from '@arco-design/web-react';
import { Download, UploadOne } from '@icon-park/react';
import React, { useState } from 'react';
import { useTranslation } from 'react-i18next';

type BatchStatus = {
  kind: 'download' | 'cloud';
  completed: number;
  total: number;
  failed: Array<{ artifactId: string; filename: string }>;
};

export const ArtifactBatchActions: React.FC<{
  artifacts: SynonBiomedProjectArtifact[];
  onSelectionChange: (artifactIds: string[]) => void;
}> = ({ artifacts, onSelectionChange }) => {
  const { t } = useTranslation();
  const [status, setStatus] = useState<BatchStatus | null>(null);
  const [downloading, setDownloading] = useState(false);
  const [exportVisible, setExportVisible] = useState(false);
  const [credentials, setCredentials] = useState<SynonBiomedCloudCredential[]>([]);
  const [credentialId, setCredentialId] = useState('');
  const [buckets, setBuckets] = useState<string[]>([]);
  const [bucket, setBucket] = useState('');
  const [prefix, setPrefix] = useState('');
  const [exporting, setExporting] = useState(false);
  const [loadingExport, setLoadingExport] = useState(false);

  const downloadSelected = async () => {
    if (downloading || artifacts.length === 0) return;
    setDownloading(true);
    const failed: BatchStatus['failed'] = [];
    setStatus({ kind: 'download', completed: 0, total: artifacts.length, failed: [] });
    const succeeded = new Set<string>();

    for (const [index, artifact] of artifacts.entries()) {
      try {
        // Keep browser downloads sequential to preserve prompt order and bound blob memory.
        // eslint-disable-next-line no-await-in-loop
        const response = await fetch(getSynonBiomedArtifactContentUrl(artifact.artifactId), {
          credentials: 'same-origin',
        });
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        // eslint-disable-next-line no-await-in-loop
        const url = URL.createObjectURL(await response.blob());
        triggerDownload(url, artifact.filename);
        URL.revokeObjectURL(url);
        succeeded.add(artifact.artifactId);
      } catch (error) {
        console.warn('[ArtifactBatchActions] Artifact download failed', {
          artifactId: artifact.artifactId,
          errorName: error instanceof Error ? error.name : typeof error,
        });
        failed.push({
          artifactId: artifact.artifactId,
          filename: artifact.filename,
        });
      }
      setStatus({ kind: 'download', completed: index + 1, total: artifacts.length, failed: [...failed] });
    }

    onSelectionChange(
      artifacts.filter((artifact) => !succeeded.has(artifact.artifactId)).map((artifact) => artifact.artifactId)
    );
    setDownloading(false);
    if (failed.length > 0) {
      Message.warning(
        t('common.batchPartialFailure', {
          succeeded: artifacts.length - failed.length,
          failed: failed.length,
        })
      );
    } else {
      Message.success(t('common.batchDownloadComplete'));
    }
  };

  const loadBuckets = async (nextCredentialId: string, nextCredentials = credentials) => {
    setCredentialId(nextCredentialId);
    setBuckets([]);
    setBucket('');
    if (!nextCredentialId) return;
    try {
      const nextBuckets = await loadSynonBiomedCloudBuckets(nextCredentialId);
      const credential = nextCredentials.find((item) => item.id === nextCredentialId);
      setBuckets(nextBuckets);
      setBucket(
        credential?.defaultBucket && nextBuckets.includes(credential.defaultBucket)
          ? credential.defaultBucket
          : (nextBuckets[0] ?? '')
      );
    } catch (error) {
      console.warn('[ArtifactBatchActions] Failed to load cloud buckets', {
        errorName: error instanceof Error ? error.name : typeof error,
      });
      Message.error(t('common.cloudBucketsLoadFailed'));
    }
  };

  const openCloudExport = async () => {
    setExportVisible(true);
    setLoadingExport(true);
    setPrefix('');
    setStatus(null);
    try {
      const connected = (await loadSynonBiomedCloudCredentials()).filter((credential) => credential.connected);
      setCredentials(connected);
      await loadBuckets(connected[0]?.id ?? '', connected);
    } catch (error) {
      console.warn('[ArtifactBatchActions] Failed to load cloud credentials', {
        errorName: error instanceof Error ? error.name : typeof error,
      });
      Message.error(t('common.cloudCredentialsLoadFailed'));
    } finally {
      setLoadingExport(false);
    }
  };

  const exportSelected = async () => {
    if (!credentialId || !bucket || exporting) return;
    setExporting(true);
    try {
      const items = buildCloudExportItems(artifacts, prefix);
      setStatus({ kind: 'cloud', completed: 0, total: items.length, failed: [] });
      const result = await exportSynonBiomedArtifactsToCloud(credentialId, { bucket, items }, (progress) => {
        setStatus((current) => ({
          kind: 'cloud',
          completed: progress.completed,
          total: progress.total,
          failed: progress.error
            ? [
                ...(current?.failed ?? []),
                {
                  artifactId: progress.current.artifactId,
                  filename: artifactFilename(artifacts, progress.current.artifactId),
                },
              ]
            : (current?.failed ?? []),
        }));
      });

      const failedIds = new Set(result.failed.map((item) => item.artifactId));
      onSelectionChange(
        artifacts.filter((artifact) => failedIds.has(artifact.artifactId)).map((artifact) => artifact.artifactId)
      );
      if (result.failed.length > 0) {
        Message.warning(
          t('common.batchPartialFailure', {
            succeeded: result.completed.length,
            failed: result.failed.length,
          })
        );
      } else {
        setExportVisible(false);
        Message.success(t('common.batchCloudExportComplete'));
      }
    } catch (error) {
      console.warn('[ArtifactBatchActions] Cloud export failed', {
        errorName: error instanceof Error ? error.name : typeof error,
      });
      Message.error(t('common.cloudExportFailed'));
    } finally {
      setExporting(false);
    }
  };

  return (
    <>
      <div className='flex flex-wrap items-center gap-6px'>
        <span className='text-12px text-t-secondary'>{t('common.selectedCount', { count: artifacts.length })}</span>
        <Button
          size='small'
          icon={<Download theme='outline' />}
          loading={downloading}
          disabled={exporting}
          onClick={() => void downloadSelected()}
        >
          {t('common.batchDownload')}
        </Button>
        <Button
          size='small'
          icon={<UploadOne theme='outline' />}
          loading={loadingExport}
          disabled={downloading}
          onClick={() => void openCloudExport()}
        >
          {t('common.batchCloudExport')}
        </Button>
        <Button size='small' type='text' disabled={downloading || exporting} onClick={() => onSelectionChange([])}>
          {t('common.clearSelection')}
        </Button>
      </div>
      {status && (
        <div className='mt-8px' data-testid='artifact-batch-progress'>
          <Progress
            size='small'
            percent={status.total === 0 ? 0 : Math.round((status.completed / status.total) * 100)}
            status={status.completed === status.total && status.failed.length > 0 ? 'error' : 'normal'}
          />
          <div className='mt-3px text-11px text-t-tertiary'>
            {status.kind === 'download' ? t('common.batchDownloading') : t('common.batchExporting')}
            {' · '}
            {status.completed}/{status.total}
            {status.failed.length > 0 ? ` · ${t('common.batchFailedCount', { count: status.failed.length })}` : ''}
          </div>
          {status.failed.length > 0 && (
            <div role='alert' className='mt-4px max-h-64px overflow-y-auto text-11px text-danger-6'>
              {status.failed.map((item) => (
                <div key={item.artifactId}>
                  {item.filename}: {t('common.failed')}
                </div>
              ))}
            </div>
          )}
        </div>
      )}

      <Modal
        title={t('common.batchCloudExport')}
        visible={exportVisible}
        onCancel={() => {
          if (!exporting) setExportVisible(false);
        }}
        onOk={() => void exportSelected()}
        confirmLoading={exporting}
        okText={t('common.export')}
        cancelText={t('common.cancel')}
        okButtonProps={{ disabled: !credentialId || !bucket || artifacts.length === 0 }}
        unmountOnExit
      >
        {credentials.length === 0 && !loadingExport ? (
          <Empty description={t('common.cloudCredentialsEmpty')} />
        ) : (
          <div className='flex flex-col gap-12px'>
            <label className='flex flex-col gap-5px text-12px text-t-secondary'>
              {t('common.cloudCredential')}
              <Select
                aria-label={t('common.cloudCredential')}
                value={credentialId}
                onChange={(value) => void loadBuckets(value)}
              >
                {credentials.map((credential) => (
                  <Select.Option key={credential.id} value={credential.id}>
                    {credential.name}
                  </Select.Option>
                ))}
              </Select>
            </label>
            <label className='flex flex-col gap-5px text-12px text-t-secondary'>
              {t('common.exportBucket')}
              <Select aria-label={t('common.exportBucket')} value={bucket} onChange={setBucket}>
                {buckets.map((item) => (
                  <Select.Option key={item} value={item}>
                    {item}
                  </Select.Option>
                ))}
              </Select>
            </label>
            <label className='flex flex-col gap-5px text-12px text-t-secondary'>
              {t('common.cloudPrefix')}
              <Input
                aria-label={t('common.cloudPrefix')}
                value={prefix}
                onChange={setPrefix}
                placeholder='project/exports/'
              />
            </label>
            <div className='text-11px text-t-tertiary'>{t('common.batchCloudExportHint')}</div>
            {status?.kind === 'cloud' && (
              <Progress
                percent={status.total === 0 ? 0 : Math.round((status.completed / status.total) * 100)}
                status={status.completed === status.total && status.failed.length > 0 ? 'error' : 'normal'}
              />
            )}
          </div>
        )}
      </Modal>
    </>
  );
};

export function buildCloudExportItems(
  artifacts: SynonBiomedProjectArtifact[],
  prefix: string
): Array<{ artifactId: string; key: string }> {
  const normalizedPrefix = prefix.trim().replace(/^\/+|\/+$/g, '');
  const counts = new Map<string, number>();
  return artifacts.map((artifact) => {
    const count = counts.get(artifact.filename) ?? 0;
    counts.set(artifact.filename, count + 1);
    const filename = count === 0 ? artifact.filename : withArtifactSuffix(artifact.filename, artifact.artifactId);
    return {
      artifactId: artifact.artifactId,
      key: normalizedPrefix ? normalizedPrefix + '/' + filename : filename,
    };
  });
}

function withArtifactSuffix(filename: string, artifactId: string): string {
  const dot = filename.lastIndexOf('.');
  const suffix = '-' + artifactId.slice(0, 8);
  return dot > 0 ? filename.slice(0, dot) + suffix + filename.slice(dot) : filename + suffix;
}

function artifactFilename(artifacts: SynonBiomedProjectArtifact[], artifactId: string): string {
  return artifacts.find((artifact) => artifact.artifactId === artifactId)?.filename ?? artifactId;
}

function triggerDownload(url: string, filename: string): void {
  const anchor = document.createElement('a');
  anchor.href = url;
  anchor.download = filename;
  anchor.style.display = 'none';
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
}

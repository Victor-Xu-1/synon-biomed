import {
  getSynonBiomedLocalFileUrl,
  getSynonBiomedRemoteFileUrl,
  importSynonBiomedLocalFile,
  importSynonBiomedRemoteFile,
  loadSynonBiomedLocalDirectory,
  loadSynonBiomedRemoteDirectory,
  type SynonBiomedHostFileEntry,
} from '@/renderer/services/synonBiomedCompute';
import {
  getSynonBiomedCloudDownloadUrl,
  importSynonBiomedCloudObject,
  loadSynonBiomedCloudFolder,
  type SynonBiomedCloudCredential,
} from '@/renderer/services/synonBiomedWorkspaceSettings';
import { SynonBiomedPdfArtifactViewer } from '@/renderer/pages/artifact/SynonBiomedPdfArtifactViewer';
import { Button, Empty, Message, Spin } from '@arco-design/web-react';
import { ArrowLeft, Copy, Download, FolderOpen, Refresh, Upload } from '@icon-park/react';
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { redactErrorText } from '@/renderer/pages/conversation/platforms/acp/errorDiagnostics';
import { isSynonBiomedFileImportError } from '@/renderer/services/synonBiomedFileImportError';

export type SynonBiomedFilesHost =
  | { id: 'local'; kind: 'local'; label: string; detail?: string }
  | { id: string; kind: 'ssh'; label: string; detail?: string; providerName: string }
  | { id: string; kind: 'cloud'; label: string; detail?: string; credential: SynonBiomedCloudCredential };

type BrowserEntry = SynonBiomedHostFileEntry & {
  path: string;
};

type ImportStatus = 'downloading' | 'imported' | 'error';

export const FileBrowser: React.FC<{
  host: SynonBiomedFilesHost;
  cloudBucket?: string;
  projectId: string;
  onImported: () => void | Promise<void>;
}> = ({ host, cloudBucket, projectId, onImported }) => {
  const { t } = useTranslation();
  const [requestedPath, setRequestedPath] = useState<string | undefined>();
  const [resolvedPath, setResolvedPath] = useState('');
  const [entries, setEntries] = useState<BrowserEntry[]>([]);
  const [selected, setSelected] = useState<BrowserEntry | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState(false);
  const [truncated, setTruncated] = useState(false);
  const [importing, setImporting] = useState(false);
  const [importStatuses, setImportStatuses] = useState<Record<string, ImportStatus>>({});
  const [revision, setRevision] = useState(0);
  const importGenerationRef = React.useRef(0);

  useEffect(() => {
    setRequestedPath(undefined);
    setResolvedPath('');
    setSelected(null);
    setImportStatuses({});
    setImporting(false);
    importGenerationRef.current += 1;
  }, [cloudBucket, host.id]);

  useEffect(() => {
    if (host.kind === 'cloud' && !cloudBucket) {
      setEntries([]);
      setLoading(false);
      setLoadError(false);
      setTruncated(false);
      return;
    }
    let active = true;
    setLoading(true);
    setLoadError(false);
    setTruncated(false);
    setSelected(null);

    const load =
      host.kind === 'local'
        ? loadSynonBiomedLocalDirectory(requestedPath)
        : host.kind === 'ssh'
          ? loadSynonBiomedRemoteDirectory(host.providerName, requestedPath)
          : loadSynonBiomedCloudFolder(host.credential.id, cloudBucket ?? '', requestedPath ?? '');

    void load
      .then((snapshot) => {
        if (!active) return;
        if ('entries' in snapshot) {
          const cwd = snapshot.resolvedPath;
          setResolvedPath(cwd);
          setEntries(
            snapshot.entries.map((entry) => ({
              ...entry,
              path: joinPath(cwd, entry.name),
            }))
          );
          setTruncated(snapshot.truncated);
          return;
        }
        const prefix = requestedPath ?? '';
        setResolvedPath(prefix);
        setEntries([
          ...snapshot.folders.map((folder) => ({
            name: cloudEntryName(folder),
            path: folder,
            isDirectory: true,
            size: 0,
            mtime: null as number | null,
          })),
          ...snapshot.files.map((file) => ({
            name: cloudEntryName(file.key),
            path: file.key,
            isDirectory: false,
            size: file.size,
            mtime: parseTimestamp(file.lastModified),
          })),
        ]);
      })
      .catch((error: unknown) => {
        console.warn('[FileBrowser] Failed to load directory:', diagnostic(error));
        if (active) {
          setEntries([]);
          setLoadError(true);
          setTruncated(false);
        }
      })
      .finally(() => {
        if (active) setLoading(false);
      });

    return () => {
      active = false;
    };
  }, [cloudBucket, host, requestedPath, revision]);

  const canGoUp = host.kind === 'cloud' ? Boolean(resolvedPath) : resolvedPath.split('/').filter(Boolean).length > 1;
  const selectedUrl = useMemo(
    () => (selected ? fileUrl(host, selected.path, cloudBucket, 'inline') : ''),
    [cloudBucket, host, selected]
  );
  const downloadUrl = useMemo(
    () => (selected ? fileUrl(host, selected.path, cloudBucket, 'attachment') : ''),
    [cloudBucket, host, selected]
  );

  const importSelected = useCallback(async () => {
    if (!selected || selected.isDirectory || importing) return;
    const path = selected.path;
    const generation = importGenerationRef.current;
    setImporting(true);
    setImportStatuses((current) => ({ ...current, [path]: 'downloading' }));
    try {
      if (host.kind === 'local') {
        await importSynonBiomedLocalFile(selected.path, projectId);
      } else if (host.kind === 'ssh') {
        await importSynonBiomedRemoteFile(host.providerName, selected.path, projectId);
      } else {
        await importSynonBiomedCloudObject(host.credential.id, {
          bucket: cloudBucket ?? '',
          key: selected.path,
          projectId,
        });
      }
      if (generation !== importGenerationRef.current) return;
      setImportStatuses((current) => ({ ...current, [path]: 'imported' }));
      Message.success(t('conversation.fileBrowser.imported'));
      try {
        await onImported();
      } catch (error) {
        console.warn('[FileBrowser] Failed to refresh imported artifacts:', diagnostic(error));
      }
    } catch (error) {
      if (generation !== importGenerationRef.current) return;
      console.warn('[FileBrowser] Failed to import file:', diagnostic(error));
      setImportStatuses((current) => ({ ...current, [path]: 'error' }));
      Message.error(t(importFailureTranslationKey(error)));
    } finally {
      if (generation === importGenerationRef.current) setImporting(false);
    }
  }, [cloudBucket, host, importing, onImported, projectId, selected, t]);

  if (host.kind === 'cloud' && !cloudBucket) {
    return <Empty className='py-60px' description={t('conversation.fileBrowser.selectBucket')} />;
  }

  return (
    <div className='min-h-0 flex-1 grid grid-cols-1 md:grid-cols-[minmax(320px,0.9fr)_minmax(320px,1.1fr)]'>
      <section className='min-h-0 flex flex-col border-r border-arco-2'>
        <div className='h-44px shrink-0 flex items-center gap-6px border-b border-arco-2 px-10px'>
          <Button
            type='text'
            size='small'
            icon={<ArrowLeft theme='outline' />}
            aria-label={t('conversation.fileBrowser.upDirectory')}
            disabled={!canGoUp || loading}
            onClick={() => setRequestedPath(parentPath(resolvedPath, host.kind === 'cloud'))}
          />
          <code className='min-w-0 flex-1 truncate text-11px text-t-secondary' title={resolvedPath}>
            {resolvedPath || '/'}
          </code>
          <Button
            type='text'
            size='small'
            icon={<Refresh theme='outline' />}
            aria-label={t('common.refresh')}
            loading={loading}
            onClick={() => setRevision((value) => value + 1)}
          />
        </div>
        <div
          className='min-h-0 flex-1 overflow-y-auto p-6px'
          role='list'
          aria-label={t('conversation.fileBrowser.hostFiles')}
        >
          {truncated ? (
            <div
              className='mx-4px mb-6px rd-4px bg-warning-1 px-8px py-6px text-11px text-warning-7'
              data-testid='file-browser-truncated'
              role='status'
            >
              {t('conversation.fileBrowser.truncated')}
            </div>
          ) : null}
          {loading ? (
            <div className='h-full flex-center' role='status'>
              <Spin />
            </div>
          ) : loadError ? (
            <div className='h-full flex flex-col items-center justify-center gap-10px px-16px text-center'>
              <div role='alert' className='text-12px text-danger-6'>
                {t('conversation.fileBrowser.directoryLoadFailed')}
              </div>
              <Button size='small' onClick={() => setRevision((value) => value + 1)}>
                {t('common.retry')}
              </Button>
            </div>
          ) : entries.length === 0 ? (
            <Empty className='py-48px' description={t('conversation.fileBrowser.emptyDirectory')} />
          ) : (
            entries.map((entry) => (
              <Button
                key={entry.path}
                type='text'
                long
                className={`mb-2px !h-38px !justify-start ${selected?.path === entry.path ? 'bg-fill-2' : ''}`}
                aria-label={t(
                  entry.isDirectory
                    ? 'conversation.fileBrowser.openDirectoryNamed'
                    : 'conversation.fileBrowser.selectFileNamed',
                  { name: entry.name }
                )}
                onClick={() => {
                  if (entry.isDirectory) setRequestedPath(entry.path);
                  else setSelected(entry);
                }}
              >
                <span className='w-full min-w-0 flex items-center gap-8px text-left'>
                  <FolderOpen theme={entry.isDirectory ? 'filled' : 'outline'} size={15} className='shrink-0' />
                  <span className='min-w-0 flex-1 truncate'>{entry.name}</span>
                  {!entry.isDirectory && importStatuses[entry.path] ? (
                    <span
                      className={`shrink-0 text-10px ${
                        importStatuses[entry.path] === 'error' ? 'text-danger-6' : 'text-t-tertiary'
                      }`}
                      data-testid={`file-import-status-${entry.path}`}
                      role='status'
                    >
                      {t(importStatusTranslationKey(importStatuses[entry.path]))}
                    </span>
                  ) : null}
                  {!entry.isDirectory && (
                    <span className='shrink-0 text-10px text-t-tertiary'>{formatBytes(entry.size)}</span>
                  )}
                </span>
              </Button>
            ))
          )}
        </div>
      </section>

      <section className='min-h-320px flex flex-col'>
        {selected ? (
          <>
            <div className='shrink-0 flex flex-wrap items-center justify-between gap-8px border-b border-arco-2 px-12px py-8px'>
              <div className='min-w-0'>
                <div className='truncate text-13px font-600 text-t-primary'>{selected.name}</div>
                <div className='mt-2px truncate font-mono text-10px text-t-tertiary'>{selected.path}</div>
              </div>
              <div className='flex items-center gap-4px'>
                <Button
                  type='text'
                  size='small'
                  icon={<Copy theme='outline' />}
                  onClick={() => {
                    void navigator.clipboard
                      .writeText(selected.path)
                      .then(() => Message.success(t('conversation.fileBrowser.pathCopied')))
                      .catch((error: unknown) => {
                        console.warn('[FileBrowser] Failed to copy path:', diagnostic(error));
                        Message.error(t('conversation.fileBrowser.pathCopyFailed'));
                      });
                  }}
                >
                  {t('conversation.fileBrowser.copyPath')}
                </Button>
                <Button type='text' size='small' icon={<Download theme='outline' />} href={downloadUrl}>
                  {t('common.download')}
                </Button>
                <Button
                  type='primary'
                  size='small'
                  icon={<Upload theme='outline' />}
                  loading={importing}
                  onClick={() => void importSelected()}
                >
                  {t('conversation.fileBrowser.importProject')}
                </Button>
              </div>
            </div>
            <FilePreview entry={selected} url={selectedUrl} />
          </>
        ) : (
          <div className='h-full flex-center'>
            <Empty description={t('conversation.fileBrowser.selectPreview')} />
          </div>
        )}
      </section>
    </div>
  );
};

const FilePreview: React.FC<{ entry: BrowserEntry; url: string }> = ({ entry, url }) => {
  const { t } = useTranslation();
  const kind = previewKind(entry.name);
  const [text, setText] = useState('');
  const [error, setError] = useState(false);

  useEffect(() => {
    if (kind !== 'text') return;
    const controller = new AbortController();
    setText('');
    setError(false);
    void fetch(url, {
      headers: { Range: 'bytes=0-262143' },
      credentials: 'same-origin',
      signal: controller.signal,
    })
      .then(async (response) => {
        if (!response.ok && response.status !== 206) throw new Error(`HTTP ${response.status}`);
        return response.text();
      })
      .then(setText)
      .catch((reason: unknown) => {
        if (!controller.signal.aborted) {
          console.warn('[FileBrowser] Failed to load text preview:', diagnostic(reason));
          setError(true);
        }
      });
    return () => controller.abort();
  }, [kind, url]);

  if (kind === 'image') {
    return <img src={url} alt={entry.name} className='min-h-0 flex-1 size-full object-contain bg-fill-1' />;
  }
  if (kind === 'pdf') {
    return (
      <div className='min-h-0 flex-1 overflow-hidden'>
        <SynonBiomedPdfArtifactViewer filename={entry.name} contentUrl={url} onSelectionChange={() => undefined} />
      </div>
    );
  }
  if (kind === 'text') {
    return error ? (
      <div role='alert' className='p-16px text-12px text-danger-6'>
        {t('conversation.fileBrowser.previewLoadFailed')}
      </div>
    ) : (
      <pre className='m-0 min-h-0 flex-1 overflow-auto whitespace-pre-wrap break-words p-14px font-mono text-11px leading-17px text-t-secondary'>
        {text}
      </pre>
    );
  }
  return (
    <div className='h-full flex-center'>
      <Empty description={t('conversation.fileBrowser.unsupportedPreview', { size: formatBytes(entry.size) })} />
    </div>
  );
};

function fileUrl(
  host: SynonBiomedFilesHost,
  path: string,
  cloudBucket: string | undefined,
  disposition: 'attachment' | 'inline'
): string {
  if (host.kind === 'local') return getSynonBiomedLocalFileUrl(path, disposition);
  if (host.kind === 'ssh') return getSynonBiomedRemoteFileUrl(host.providerName, path, disposition);
  return getSynonBiomedCloudDownloadUrl(host.credential.id, cloudBucket ?? '', path);
}

function joinPath(parent: string, name: string): string {
  if (!parent || parent === '/') return '/' + name.replace(/^\/+/, '');
  return parent.replace(/\/+$/, '') + '/' + name.replace(/^\/+/, '');
}

function importFailureTranslationKey(
  error: unknown
):
  | 'conversation.fileBrowser.importFailed'
  | 'conversation.fileBrowser.importPermission'
  | 'conversation.fileBrowser.importNotFound'
  | 'conversation.fileBrowser.importNotDirectory'
  | 'conversation.fileBrowser.importNotFile'
  | 'conversation.fileBrowser.importTooLarge'
  | 'conversation.fileBrowser.importOutsideRoots'
  | 'conversation.fileBrowser.importConnection' {
  if (!isSynonBiomedFileImportError(error)) return 'conversation.fileBrowser.importFailed';
  switch (error.kind) {
    case 'permission':
      return 'conversation.fileBrowser.importPermission';
    case 'not_found':
      return 'conversation.fileBrowser.importNotFound';
    case 'not_a_directory':
      return 'conversation.fileBrowser.importNotDirectory';
    case 'not_a_file':
      return 'conversation.fileBrowser.importNotFile';
    case 'too_large':
      return 'conversation.fileBrowser.importTooLarge';
    case 'outside_roots':
      return 'conversation.fileBrowser.importOutsideRoots';
    case 'connection':
      return 'conversation.fileBrowser.importConnection';
    default:
      return 'conversation.fileBrowser.importFailed';
  }
}

function importStatusTranslationKey(
  status: ImportStatus
):
  | 'conversation.fileBrowser.downloading'
  | 'conversation.fileBrowser.importedStatus'
  | 'conversation.fileBrowser.error' {
  switch (status) {
    case 'downloading':
      return 'conversation.fileBrowser.downloading';
    case 'imported':
      return 'conversation.fileBrowser.importedStatus';
    case 'error':
      return 'conversation.fileBrowser.error';
  }
}

function parentPath(path: string, cloud: boolean): string | undefined {
  const normalized = path.replace(/\/+$/, '');
  const index = normalized.lastIndexOf('/');
  if (cloud) return index < 0 ? '' : normalized.slice(0, index + 1);
  if (index <= 0) return '/';
  return normalized.slice(0, index);
}

function cloudEntryName(path: string): string {
  return path.replace(/\/+$/, '').split('/').at(-1) || path;
}

function parseTimestamp(value: string): number | null {
  const timestamp = Date.parse(value);
  return Number.isFinite(timestamp) ? timestamp : null;
}

function previewKind(filename: string): 'image' | 'pdf' | 'text' | 'none' {
  const extension = filename.split('.').at(-1)?.toLowerCase() ?? '';
  if (['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg'].includes(extension)) return 'image';
  if (extension === 'pdf') return 'pdf';
  if (
    [
      'txt',
      'md',
      'mdx',
      'json',
      'jsonl',
      'csv',
      'tsv',
      'yaml',
      'yml',
      'toml',
      'xml',
      'html',
      'css',
      'js',
      'ts',
      'tsx',
      'py',
      'r',
      'sh',
      'log',
    ].includes(extension)
  ) {
    return 'text';
  }
  return 'none';
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

const diagnostic = (error: unknown): string =>
  redactErrorText(error instanceof Error ? error.message : String(error || 'unknown error'));

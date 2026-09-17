import {
  copySynonBiomedArtifact,
  getSynonBiomedArtifactVersionContentUrl,
  loadSynonBiomedArtifactLineage,
  loadSynonBiomedArtifactVersions,
  moveSynonBiomedArtifact,
  type SynonBiomedArtifactLineage,
  type SynonBiomedArtifactVersion,
} from '@/renderer/services/synonBiomedArtifacts';
import {
  getSynonBiomedArtifactContentUrl,
  loadSynonBiomedArtifact,
  loadSynonBiomedProjectArtifacts,
  loadSynonBiomedProjectFolders,
  type SynonBiomedProjectArtifact,
  type SynonBiomedProjectFolder,
} from '@/renderer/services/synonBiomedGateway';
import { resolveSynonBiomedArtifactPreviewPlan } from '@/renderer/services/synonBiomedArtifactPreview';
import {
  exportSynonBiomedArtifactToCloud,
  loadSynonBiomedCloudBuckets,
  loadSynonBiomedStorageSettings,
  type SynonBiomedCloudCredential,
} from '@/renderer/services/synonBiomedWorkspaceSettings';
import {
  cachePreviewModule,
  LazyExcelPreview as ExcelPreview,
  LazySynonBiomedGenomeViewer as SynonBiomedGenomeViewer,
  LazySynonBiomedHdf5Viewer as SynonBiomedHdf5Viewer,
  LazySynonBiomedMoleculeViewer as SynonBiomedMoleculeViewer,
  LazySynonBiomedMsaViewer as SynonBiomedMsaViewer,
  LazySynonBiomedNotebookViewer as SynonBiomedNotebookViewer,
  LazyOfficeDocPreview as OfficeDocPreview,
  LazyPptPreview as PptViewer,
  LazySynonBiomedSequenceViewer as SynonBiomedSequenceViewer,
  LazySynonBiomedStructureViewer as SynonBiomedStructureViewer,
  LazySynonBiomedTableViewer as SynonBiomedTableViewer,
  LazyUnsupportedPreview as UnsupportedPreview,
} from '@/renderer/pages/conversation/Preview/components/viewers/scientificPreviewLoaders';
import PreviewLoadingState from '@/renderer/components/media/PreviewLoadingState';
import SynonBiomedNotesModal from '@/renderer/components/synonBiomed/notes/SynonBiomedNotesModal';
import type { SynonBiomedArtifactTextSelection } from './artifactTextSelection';
import type { SynonBiomedArtifactCanvasSelection } from './artifactCanvasSelection';
import { ArtifactAnnotationsPanel, ArtifactVerificationPanel } from './ArtifactAnnotationPanels';
import { ArtifactEditRefinementPanel } from './ArtifactEditRefinementPanel';
import { ArtifactSelectionAnnotationModal } from './ArtifactSelectionAnnotationModal';
import {
  loadSynonBiomedArtifactAnnotations,
  type SynonBiomedAppliedArtifactEdit,
  type SynonBiomedArtifactAnnotation,
} from '@/renderer/services/synonBiomedAnnotations';
import { Button, Empty, Input, Message, Modal, Select, Spin, Tabs } from '@arco-design/web-react';
import { Comment, Copy, Download, FileText, FolderOpen, Left, Magic, Notes, Upload } from '@icon-park/react';
import React, { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useLocation, useNavigate, useParams } from 'react-router';

const ROOT_FOLDER_VALUE = '__project_root__';
const SynonBiomedMcpAppArtifactViewer = React.lazy(
  cachePreviewModule(
    () => import('@/renderer/pages/conversation/Preview/components/viewers/SynonBiomedMcpAppArtifactViewer')
  )
);
const SynonBiomedLatexArtifactViewer = React.lazy(cachePreviewModule(() => import('./SynonBiomedLatexArtifactViewer')));
const SynonBiomedTextArtifactViewer = React.lazy(cachePreviewModule(() => import('./SynonBiomedTextArtifactViewer')));
const SynonBiomedPdfArtifactViewer = React.lazy(cachePreviewModule(() => import('./SynonBiomedPdfArtifactViewer')));
const SynonBiomedImageArtifactViewer = React.lazy(
  cachePreviewModule(() =>
    import('./SynonBiomedImageArtifactViewer').then((module) => ({
      default: module.SynonBiomedImageArtifactViewer,
    }))
  )
);
const AudioPreview = React.lazy(cachePreviewModule(() => import('./AudioPreview')));
const VideoPreview = React.lazy(cachePreviewModule(() => import('./VideoPreview')));

type ArtifactSnapshot = {
  artifact: SynonBiomedProjectArtifact;
  relatedArtifacts: SynonBiomedProjectArtifact[];
  versions: SynonBiomedArtifactVersion[];
  lineage: SynonBiomedArtifactLineage | null;
  folders: SynonBiomedProjectFolder[];
};

const ArtifactPreview: React.FC = () => {
  const { t } = useTranslation();
  const { artifactId } = useParams();
  const navigate = useNavigate();
  const location = useLocation();
  const [snapshot, setSnapshot] = useState<ArtifactSnapshot | null>(null);
  const [loading, setLoading] = useState(Boolean(artifactId));
  const [failed, setFailed] = useState(false);
  const [selectedVersionId, setSelectedVersionId] = useState<string | null>(null);
  const [activeInspectorTab, setActiveInspectorTab] = useState(() => inspectorTabFromSearch(location.search));
  const [canvasSelection, setCanvasSelection] = useState<SynonBiomedArtifactCanvasSelection | null>(null);
  const [annotationSelection, setAnnotationSelection] = useState<SynonBiomedArtifactCanvasSelection | null>(null);
  const [refinementSelection, setRefinementSelection] = useState<SynonBiomedArtifactTextSelection | null>(null);
  const [previewAnnotations, setPreviewAnnotations] = useState<SynonBiomedArtifactAnnotation[]>([]);
  const [annotationRevision, setAnnotationRevision] = useState(0);
  const [copyVisible, setCopyVisible] = useState(false);
  const [copyFilename, setCopyFilename] = useState('');
  const [copyFolderId, setCopyFolderId] = useState(ROOT_FOLDER_VALUE);
  const [moveVisible, setMoveVisible] = useState(false);
  const [moveFolderId, setMoveFolderId] = useState(ROOT_FOLDER_VALUE);
  const [exportVisible, setExportVisible] = useState(false);
  const [exportCredentials, setExportCredentials] = useState<SynonBiomedCloudCredential[]>([]);
  const [exportCredentialId, setExportCredentialId] = useState('');
  const [exportBuckets, setExportBuckets] = useState<string[]>([]);
  const [exportBucket, setExportBucket] = useState('');
  const [exportKey, setExportKey] = useState('');
  const [mutating, setMutating] = useState(false);
  const [notesVisible, setNotesVisible] = useState(false);
  const [messageApi, messageContextHolder] = Message.useMessage();

  useEffect(() => {
    setActiveInspectorTab(inspectorTabFromSearch(location.search));
  }, [location.search]);

  useEffect(() => {
    if (!artifactId) {
      setLoading(false);
      setSnapshot(null);
      return;
    }

    let active = true;
    setLoading(true);
    setFailed(false);
    setSelectedVersionId(null);
    setCanvasSelection(null);
    setAnnotationSelection(null);
    setRefinementSelection(null);

    void loadArtifactSnapshot(artifactId)
      .then((nextSnapshot) => {
        if (!active) return;
        setSnapshot(nextSnapshot);
        setMoveFolderId(nextSnapshot.artifact.folderId ?? ROOT_FOLDER_VALUE);
      })
      .catch((error) => {
        console.error('[ArtifactPreview] Failed to load artifact snapshot', error);
        if (active) {
          setSnapshot(null);
          setFailed(true);
        }
      })
      .finally(() => {
        if (active) setLoading(false);
      });

    return () => {
      active = false;
    };
  }, [artifactId]);

  const selectedVersion = useMemo(
    () => snapshot?.versions.find((version) => version.versionId === selectedVersionId) ?? null,
    [selectedVersionId, snapshot?.versions]
  );
  const annotationVersionId = selectedVersion?.versionId ?? snapshot?.artifact.versionId ?? null;

  useEffect(() => {
    if (!snapshot || !annotationVersionId) {
      setPreviewAnnotations([]);
      return;
    }
    let active = true;
    void loadSynonBiomedArtifactAnnotations(snapshot.artifact.artifactId, annotationVersionId)
      .then((collection) => {
        if (active) setPreviewAnnotations(collection.annotations);
      })
      .catch(() => {
        if (active) setPreviewAnnotations([]);
      });
    return () => {
      active = false;
    };
  }, [annotationRevision, annotationVersionId, snapshot]);

  if (loading) {
    return (
      <div className='size-full flex-center'>
        <Spin />
      </div>
    );
  }

  if (!snapshot || failed) {
    return (
      <div className='size-full flex-center px-24px'>
        <Empty description={failed ? t('preview.artifact.loadFailed') : t('preview.artifact.notFound')} />
      </div>
    );
  }

  const { artifact, relatedArtifacts, versions, lineage, folders } = snapshot;
  const latexResourceUrls = buildLatexArtifactResourceUrls(relatedArtifacts);
  const contentUrl = selectedVersion
    ? getSynonBiomedArtifactVersionContentUrl(selectedVersion.versionId)
    : getSynonBiomedArtifactContentUrl(artifact.artifactId);
  const displayVersion = selectedVersion?.versionNumber || artifact.versionNumber;
  const displayContentType = selectedVersion?.contentType ?? artifact.contentType;
  const displaySize = selectedVersion?.sizeBytes ?? artifact.sizeBytes;
  const activeVersionId = selectedVersion?.versionId ?? artifact.versionId;

  const handleVersionApplied = async (result: SynonBiomedAppliedArtifactEdit) => {
    const nextSnapshot = await loadArtifactSnapshot(result.artifactId);
    setSnapshot(nextSnapshot);
    setSelectedVersionId(result.versionId);
    setCanvasSelection(null);
    setAnnotationRevision((revision) => revision + 1);
    messageApi.success(t('preview.artifact.versionCreated', { version: result.versionNumber }));
  };

  const handleCopy = async () => {
    const filename = copyFilename.trim();
    if (!filename) return;
    setMutating(true);
    try {
      const result = await copySynonBiomedArtifact({
        artifactId: artifact.artifactId,
        newFilename: filename,
        targetFolderId: copyFolderId === ROOT_FOLDER_VALUE ? null : copyFolderId,
      });
      setCopyVisible(false);
      messageApi.success(t('preview.artifact.copySucceeded'));
      if (result.artifactId) {
        void navigate(`/artifacts/${encodeURIComponent(result.artifactId)}`);
      }
    } catch (error) {
      console.error('[ArtifactPreview] Failed to copy artifact', error);
      messageApi.error(t('preview.artifact.copyFailed'));
    } finally {
      setMutating(false);
    }
  };

  const handleMove = async () => {
    setMutating(true);
    try {
      const targetFolderId = moveFolderId === ROOT_FOLDER_VALUE ? null : moveFolderId;
      await moveSynonBiomedArtifact({
        artifactId: artifact.artifactId,
        folderId: targetFolderId,
      });
      setSnapshot((current) =>
        current
          ? {
              ...current,
              artifact: { ...current.artifact, folderId: targetFolderId },
            }
          : current
      );
      setMoveVisible(false);
      messageApi.success(t('preview.artifact.moveSucceeded'));
    } catch (error) {
      console.error('[ArtifactPreview] Failed to move artifact', error);
      messageApi.error(t('preview.artifact.moveFailed'));
    } finally {
      setMutating(false);
    }
  };

  const loadExportCredential = async (credentialId: string, credentials = exportCredentials) => {
    setExportCredentialId(credentialId);
    setExportBucket('');
    setExportBuckets([]);
    if (!credentialId) return;
    try {
      const buckets = await loadSynonBiomedCloudBuckets(credentialId);
      const credential = credentials.find((item) => item.id === credentialId);
      setExportBuckets(buckets);
      setExportBucket(credential?.defaultBucket || buckets[0] || '');
    } catch (error) {
      console.error('[ArtifactPreview] Failed to load cloud buckets', error);
      messageApi.error(t('preview.artifact.bucketLoadFailed'));
    }
  };

  const openCloudExport = async () => {
    setExportVisible(true);
    setExportKey(artifact.filename);
    setMutating(true);
    try {
      const storage = await loadSynonBiomedStorageSettings();
      const connected = storage.cloudCredentials.filter((item) => item.connected);
      setExportCredentials(connected);
      const initial = connected[0]?.id ?? '';
      if (initial) await loadExportCredential(initial, connected);
    } catch (error) {
      console.error('[ArtifactPreview] Failed to load cloud credentials', error);
      messageApi.error(t('preview.artifact.credentialsLoadFailed'));
    } finally {
      setMutating(false);
    }
  };

  const handleCloudExport = async () => {
    if (!exportCredentialId || !exportBucket || !exportKey.trim()) return;
    setMutating(true);
    try {
      await exportSynonBiomedArtifactToCloud(exportCredentialId, {
        artifactId: artifact.artifactId,
        bucket: exportBucket,
        key: exportKey.trim(),
      });
      setExportVisible(false);
      messageApi.success(t('preview.artifact.exportSucceeded'));
    } catch (error) {
      console.error('[ArtifactPreview] Failed to export artifact to cloud', error);
      messageApi.error(t('preview.artifact.exportFailed'));
    } finally {
      setMutating(false);
    }
  };

  return (
    <main className='artifact-page size-full overflow-hidden bg-1'>
      {messageContextHolder}
      <div className='artifact-shell size-full max-w-1600px mx-auto flex flex-col'>
        <header className='artifact-header min-h-68px px-18px md:px-28px py-12px flex flex-wrap items-center gap-12px border-b border-solid border-[var(--color-border-2)]'>
          <Button
            type='text'
            aria-label={t('preview.artifact.backToProject')}
            title={t('preview.artifact.backToProject')}
            icon={<Left theme='outline' size={17} />}
            onClick={() =>
              artifact.projectId
                ? void navigate(`/projects/${encodeURIComponent(artifact.projectId)}`)
                : void navigate(-1)
            }
          />
          <FileText theme='outline' size={21} className='shrink-0 text-t-secondary' />
          <div className='min-w-180px flex-1'>
            <h1 className='m-0 truncate text-17px leading-24px font-[600] text-t-primary'>{artifact.filename}</h1>
            <div className='mt-2px text-11px text-t-tertiary'>
              {displayContentType ?? t('preview.artifact.unknownType')} · {formatBytes(displaySize)} ·{' '}
              {t('preview.artifact.versionLabel', {
                version: displayVersion || 1,
              })}
            </div>
          </div>
          <div className='flex items-center gap-6px'>
            {artifact.projectId && (artifact.frameId || artifact.rootFrameId) && (
              <Button icon={<Notes theme='outline' size={15} />} onClick={() => setNotesVisible(true)}>
                {t('preview.artifact.notes')}
              </Button>
            )}
            <a href={contentUrl} download={artifact.filename} className='no-underline'>
              <Button icon={<Download theme='outline' size={15} />}>{t('preview.artifact.download')}</Button>
            </a>
            <Button
              aria-label={t('preview.artifact.copyFile')}
              icon={<Copy theme='outline' size={15} />}
              onClick={() => {
                setCopyFilename(makeCopyFilename(artifact.filename, t('preview.artifact.copySuffix')));
                setCopyVisible(true);
              }}
            >
              {t('preview.artifact.copy')}
            </Button>
            <Button
              aria-label={t('preview.artifact.moveToFolder')}
              icon={<FolderOpen theme='outline' size={15} />}
              onClick={() => setMoveVisible(true)}
            >
              {t('preview.artifact.move')}
            </Button>
            <Button
              aria-label={t('preview.artifact.exportToCloud')}
              icon={<Upload theme='outline' size={15} />}
              onClick={() => void openCloudExport()}
            >
              {t('preview.artifact.export')}
            </Button>
          </div>
        </header>

        <div className='min-h-0 flex-1 grid grid-cols-1 xl:grid-cols-[minmax(0,1fr)_360px]'>
          <section
            className='artifact-preview-pane min-h-360px overflow-hidden bg-fill-1'
            aria-label={t('preview.artifact.filePreview')}
          >
            <React.Suspense fallback={<PreviewLoadingState label={t('preview.artifact.preparingDataViewer')} />}>
              <ArtifactContent
                artifactId={artifact.artifactId}
                versionId={activeVersionId}
                rootFrameId={artifact.rootFrameId ?? artifact.frameId}
                filename={artifact.filename}
                contentType={displayContentType}
                contentUrl={contentUrl}
                annotations={previewAnnotations}
                resourceUrls={latexResourceUrls}
                onSelectionChange={setCanvasSelection}
                onAnnotationClick={() => setActiveInspectorTab('annotations')}
              />
            </React.Suspense>
          </section>
          <aside className='artifact-inspector min-h-0 overflow-y-auto border-t xl:border-t-0 xl:border-l border-solid border-[var(--color-border-2)] bg-1'>
            <Tabs activeTab={activeInspectorTab} onChange={setActiveInspectorTab} className='artifact-inspector-tabs'>
              <Tabs.TabPane key='details' title={t('preview.artifact.tabs.details')}>
                <ArtifactDetails
                  artifact={artifact}
                  folder={folders.find((item) => item.folderId === artifact.folderId)}
                />
              </Tabs.TabPane>
              <Tabs.TabPane key='versions' title={t('preview.artifact.tabs.versions')}>
                <ArtifactVersions
                  versions={versions}
                  selectedVersionId={selectedVersionId ?? artifact.versionId}
                  onSelect={(versionId) => {
                    setSelectedVersionId(versionId);
                    setCanvasSelection(null);
                  }}
                />
              </Tabs.TabPane>
              <Tabs.TabPane key='annotations' title={t('preview.artifact.tabs.annotations')}>
                {activeVersionId ? (
                  <ArtifactAnnotationsPanel
                    artifactId={artifact.artifactId}
                    versionId={activeVersionId}
                    refreshToken={annotationRevision}
                    onVersionApplied={handleVersionApplied}
                    onAnnotationsChange={setPreviewAnnotations}
                  />
                ) : (
                  <Empty description={t('preview.artifact.noAnnotatableVersion')} />
                )}
              </Tabs.TabPane>
              <Tabs.TabPane key='verification' title={t('preview.artifact.tabs.verification')}>
                {activeVersionId ? (
                  <ArtifactVerificationPanel
                    versionId={activeVersionId}
                    rootFrameId={artifact.rootFrameId ?? artifact.frameId}
                  />
                ) : (
                  <Empty description={t('preview.artifact.noVerificationRecord')} />
                )}
              </Tabs.TabPane>
              <Tabs.TabPane key='lineage' title={t('preview.artifact.tabs.lineage')}>
                <ArtifactLineage lineage={lineage} />
              </Tabs.TabPane>
            </Tabs>
          </aside>
        </div>
      </div>

      {canvasSelection && activeVersionId && (
        <div
          className='fixed z-9998 flex items-center gap-4px border border-solid border-[var(--color-border-2)] bg-1 p-4px shadow-lg'
          style={selectionToolbarStyle(canvasSelection)}
          role='toolbar'
          aria-label={t('preview.artifact.selectionActions')}
          onMouseDown={(event) => event.preventDefault()}
        >
          <Button
            type='text'
            size='small'
            icon={<Comment theme='outline' size={14} />}
            onMouseDown={(event) => {
              event.preventDefault();
              setAnnotationSelection(canvasSelection);
              setCanvasSelection(null);
            }}
          >
            {t('preview.artifact.annotate')}
          </Button>
          {canvasSelection.type === 'text_selection' && (
            <Button
              type='text'
              size='small'
              icon={<Magic theme='outline' size={14} />}
              onMouseDown={(event) => {
                event.preventDefault();
                setRefinementSelection(canvasSelection);
                setCanvasSelection(null);
              }}
            >
              {t('preview.artifact.refine')}
            </Button>
          )}
        </div>
      )}

      {annotationSelection && activeVersionId && (
        <ArtifactSelectionAnnotationModal
          artifactId={artifact.artifactId}
          versionId={activeVersionId}
          selection={annotationSelection}
          onCancel={() => setAnnotationSelection(null)}
          onCreated={(annotation) => {
            setAnnotationSelection(null);
            setActiveInspectorTab('annotations');
            setPreviewAnnotations((current) => [...current.filter((item) => item.id !== annotation.id), annotation]);
            setAnnotationRevision((revision) => revision + 1);
            messageApi.success(t('preview.artifact.selectionAnnotationAdded'));
          }}
        />
      )}

      {refinementSelection && activeVersionId && (
        <ArtifactEditRefinementPanel
          artifactId={artifact.artifactId}
          versionId={activeVersionId}
          selectedText={refinementSelection.text}
          initialInstruction=''
          onClose={() => setRefinementSelection(null)}
          onApplied={handleVersionApplied}
        />
      )}

      <Modal
        title={t('preview.artifact.copyFile')}
        visible={copyVisible}
        onCancel={() => setCopyVisible(false)}
        onOk={() => void handleCopy()}
        confirmLoading={mutating}
        okButtonProps={{ disabled: !copyFilename.trim() }}
        okText={t('preview.artifact.copy')}
        cancelText={t('common.cancel')}
        unmountOnExit
      >
        <div className='flex flex-col gap-14px'>
          <label className='flex flex-col gap-6px text-12px text-t-secondary'>
            {t('preview.artifact.filename')}
            <Input aria-label={t('preview.artifact.copiedFilename')} value={copyFilename} onChange={setCopyFilename} />
          </label>
          <FolderSelect
            label={t('preview.artifact.targetFolder')}
            value={copyFolderId}
            folders={folders}
            onChange={setCopyFolderId}
          />
        </div>
      </Modal>

      <Modal
        title={t('preview.artifact.moveToFolder')}
        visible={moveVisible}
        onCancel={() => setMoveVisible(false)}
        onOk={() => void handleMove()}
        confirmLoading={mutating}
        okText={t('preview.artifact.move')}
        cancelText={t('common.cancel')}
        unmountOnExit
      >
        <FolderSelect
          label={t('preview.artifact.targetFolder')}
          value={moveFolderId}
          folders={folders}
          onChange={setMoveFolderId}
        />
      </Modal>

      <Modal
        title={t('preview.artifact.exportToCloud')}
        visible={exportVisible}
        onCancel={() => setExportVisible(false)}
        onOk={() => void handleCloudExport()}
        confirmLoading={mutating}
        okText={t('preview.artifact.export')}
        cancelText={t('common.cancel')}
        okButtonProps={{
          disabled: !exportCredentialId || !exportBucket || !exportKey.trim(),
        }}
        unmountOnExit
      >
        {exportCredentials.length === 0 && !mutating ? (
          <Empty description={t('preview.artifact.noCloudCredentials')} />
        ) : (
          <div className='flex flex-col gap-14px'>
            <label className='flex flex-col gap-6px text-12px text-t-secondary'>
              {t('preview.artifact.cloudCredential')}
              <Select
                aria-label={t('preview.artifact.cloudCredential')}
                value={exportCredentialId}
                onChange={(value) => void loadExportCredential(value)}
              >
                {exportCredentials.map((credential) => (
                  <Select.Option key={credential.id} value={credential.id}>
                    {credential.name}
                  </Select.Option>
                ))}
              </Select>
            </label>
            <label className='flex flex-col gap-6px text-12px text-t-secondary'>
              {t('preview.artifact.bucket')}
              <Select aria-label={t('preview.artifact.exportBucket')} value={exportBucket} onChange={setExportBucket}>
                {exportBuckets.map((bucket) => (
                  <Select.Option key={bucket} value={bucket}>
                    {bucket}
                  </Select.Option>
                ))}
              </Select>
            </label>
            <label className='flex flex-col gap-6px text-12px text-t-secondary'>
              {t('preview.artifact.objectPath')}
              <Input
                aria-label={t('preview.artifact.cloudObjectPath')}
                value={exportKey}
                onChange={setExportKey}
                placeholder='folder/file.ext'
              />
            </label>
          </div>
        )}
      </Modal>
      {artifact.projectId && (artifact.frameId || artifact.rootFrameId) && (
        <SynonBiomedNotesModal
          visible={notesVisible}
          target={{
            projectId: artifact.projectId,
            targetType: 'artifact',
            targetFrameId: artifact.frameId || artifact.rootFrameId || '',
            targetArtifactId: artifact.artifactId,
          }}
          onClose={() => setNotesVisible(false)}
        />
      )}
    </main>
  );
};

async function loadArtifactSnapshot(artifactId: string): Promise<ArtifactSnapshot> {
  const artifact = await loadSynonBiomedArtifact(artifactId);
  const [versionsResult, lineageResult, foldersResult, relatedArtifactsResult] = await Promise.allSettled([
    loadSynonBiomedArtifactVersions(artifactId),
    loadSynonBiomedArtifactLineage(artifactId, { slim: true }),
    artifact.projectId ? loadSynonBiomedProjectFolders(artifact.projectId) : Promise.resolve([]),
    artifact.projectId ? loadSynonBiomedProjectArtifacts(artifact.projectId) : Promise.resolve([artifact]),
  ]);
  return {
    artifact,
    relatedArtifacts: relatedArtifactsResult.status === 'fulfilled' ? relatedArtifactsResult.value : [artifact],
    versions: versionsResult.status === 'fulfilled' ? versionsResult.value : [],
    lineage: lineageResult.status === 'fulfilled' ? lineageResult.value : null,
    folders: foldersResult.status === 'fulfilled' ? foldersResult.value : [],
  };
}

const FolderSelect: React.FC<{
  label: string;
  value: string;
  folders: SynonBiomedProjectFolder[];
  onChange: (folderId: string) => void;
}> = ({ label, value, folders, onChange }) => {
  const { t } = useTranslation();
  return (
    <label className='flex flex-col gap-6px text-12px text-t-secondary'>
      {label}
      <Select aria-label={label} value={value} onChange={onChange}>
        <Select.Option key={ROOT_FOLDER_VALUE} value={ROOT_FOLDER_VALUE}>
          {t('preview.artifact.projectRoot')}
        </Select.Option>
        {folders.map((folder) => (
          <Select.Option key={folder.folderId} value={folder.folderId}>
            {folder.name}
          </Select.Option>
        ))}
      </Select>
    </label>
  );
};

const ArtifactDetails: React.FC<{
  artifact: SynonBiomedProjectArtifact;
  folder?: SynonBiomedProjectFolder;
}> = ({ artifact, folder }) => {
  const { t, i18n } = useTranslation();
  return (
    <dl className='m-0 px-16px pb-18px grid grid-cols-[92px_minmax(0,1fr)] gap-x-10px gap-y-12px text-12px'>
      <Detail label={t('preview.artifact.details.filename')} value={artifact.filename} />
      <Detail
        label={t('preview.artifact.details.contentType')}
        value={artifact.contentType ?? t('preview.artifact.unknown')}
      />
      <Detail label={t('preview.artifact.details.size')} value={formatBytes(artifact.sizeBytes)} />
      <Detail
        label={t('preview.artifact.details.currentVersion')}
        value={t('preview.artifact.versionLabel', {
          version: artifact.versionNumber || 1,
        })}
      />
      <Detail label={t('preview.artifact.details.folder')} value={folder?.name ?? t('preview.artifact.projectRoot')} />
      <Detail label={t('preview.artifact.details.agent')} value={artifact.agentName ?? 'Synon Biomed'} />
      <Detail
        label={t('preview.artifact.details.project')}
        value={artifact.projectId ?? t('preview.artifact.notLinked')}
        mono
      />
      <Detail
        label={t('preview.artifact.details.task')}
        value={artifact.frameId ?? artifact.rootFrameId ?? t('preview.artifact.notLinked')}
        mono
      />
      <Detail
        label={t('preview.artifact.details.checksum')}
        value={artifact.checksum ?? t('preview.artifact.notProvided')}
        mono
      />
      <Detail
        label={t('preview.artifact.details.createdAt')}
        value={formatDate(artifact.createdAt, i18n.language, t('preview.artifact.unknown'))}
      />
    </dl>
  );
};

const Detail: React.FC<{ label: string; value: string; mono?: boolean }> = ({ label, value, mono }) => (
  <>
    <dt className='text-t-tertiary'>{label}</dt>
    <dd className={`m-0 min-w-0 break-all text-t-primary ${mono ? 'font-mono text-11px' : ''}`}>{value}</dd>
  </>
);

const ArtifactVersions: React.FC<{
  versions: SynonBiomedArtifactVersion[];
  selectedVersionId: string | null;
  onSelect: (versionId: string) => void;
}> = ({ versions, selectedVersionId, onSelect }) => {
  const { t, i18n } = useTranslation();
  if (versions.length === 0) return <Empty description={t('preview.artifact.noVersions')} />;
  return (
    <div className='border-t border-solid border-[var(--color-border-2)]'>
      {versions
        .toSorted((left, right) => right.versionNumber - left.versionNumber)
        .map((version) => (
          <button
            type='button'
            key={version.versionId}
            className={`w-full px-16px py-12px border-x-0 border-t-0 border-b border-solid border-[var(--color-border-2)] bg-transparent text-left hover:bg-fill-1 ${
              selectedVersionId === version.versionId ? 'bg-fill-2' : ''
            }`}
            onClick={() => onSelect(version.versionId)}
          >
            <div className='flex items-center justify-between gap-10px'>
              <span className='text-13px font-[600] text-t-primary'>
                {t('preview.artifact.versionLabel', {
                  version: version.versionNumber,
                })}
              </span>
              <span className='text-11px text-t-tertiary'>{formatBytes(version.sizeBytes)}</span>
            </div>
            <div className='mt-4px text-11px text-t-tertiary'>
              {formatDate(version.createdAt, i18n.language, t('preview.artifact.unknown'))}
            </div>
            {version.agentName && <div className='mt-3px text-11px text-t-secondary'>{version.agentName}</div>}
          </button>
        ))}
    </div>
  );
};

const ArtifactLineage: React.FC<{
  lineage: SynonBiomedArtifactLineage | null;
}> = ({ lineage }) => {
  const { t } = useTranslation();
  if (!lineage) return <Empty description={t('preview.artifact.lineage.empty')} />;
  if (lineage.pending)
    return <div className='px-16px pb-18px text-12px text-t-secondary'>{t('preview.artifact.lineage.pending')}</div>;
  return (
    <div className='px-16px pb-18px flex flex-col gap-16px text-12px'>
      <LineageSection
        title={t('preview.artifact.lineage.description')}
        value={lineage.codeDescription ?? t('preview.artifact.lineage.noDescription')}
      />
      {lineage.code && <LineageSection title={t('preview.artifact.lineage.code')} value={lineage.code} code />}
      <LineageFlags lineage={lineage} />
      {lineage.environmentSnapshot != null && (
        <LineageSection
          title={t('preview.artifact.lineage.environment')}
          value={formatStructuredValue(lineage.environmentSnapshot)}
          code
        />
      )}
      {lineage.dependencyMappings != null && (
        <LineageSection
          title={t('preview.artifact.lineage.dependencies')}
          value={formatStructuredValue(lineage.dependencyMappings)}
          code
        />
      )}
    </div>
  );
};

const LineageSection: React.FC<{
  title: string;
  value: string;
  code?: boolean;
}> = ({ title, value, code }) => (
  <section>
    <h3 className='m-0 mb-6px text-12px font-[600] text-t-primary'>{title}</h3>
    {code ? (
      <pre className='m-0 max-h-240px overflow-auto whitespace-pre-wrap break-all text-11px leading-18px text-t-secondary font-mono bg-fill-1 px-10px py-8px'>
        {value}
      </pre>
    ) : (
      <p className='m-0 whitespace-pre-wrap break-words leading-19px text-t-secondary'>{value}</p>
    )}
  </section>
);

const LineageFlags: React.FC<{ lineage: SynonBiomedArtifactLineage }> = ({ lineage }) => {
  const { t } = useTranslation();
  return (
    <div className='grid grid-cols-3 border border-solid border-[var(--color-border-2)]'>
      <Flag label={t('preview.artifact.lineage.messages')} active={lineage.hasMessages} />
      <Flag label={t('preview.artifact.lineage.environmentShort')} active={lineage.hasEnvironment} />
      <Flag label={t('preview.artifact.lineage.cellSources')} active={lineage.hasCellSources} />
    </div>
  );
};

const Flag: React.FC<{ label: string; active: boolean }> = ({ label, active }) => {
  const { t } = useTranslation();
  return (
    <div className='px-7px py-8px text-center border-r last:border-r-0 border-y-0 border-l-0 border-solid border-[var(--color-border-2)]'>
      <div className='text-11px text-t-tertiary'>{label}</div>
      <div className={`mt-2px text-11px ${active ? 'text-success-6' : 'text-t-tertiary'}`}>
        {active ? t('preview.artifact.lineage.recorded') : t('preview.artifact.lineage.none')}
      </div>
    </div>
  );
};

const ArtifactContent: React.FC<{
  artifactId: string;
  versionId: string;
  rootFrameId: string | null;
  filename: string;
  contentType: string | null;
  contentUrl: string;
  annotations: SynonBiomedArtifactAnnotation[];
  resourceUrls: Readonly<Record<string, string>>;
  onSelectionChange: (selection: SynonBiomedArtifactCanvasSelection | null) => void;
  onAnnotationClick: (annotation: SynonBiomedArtifactAnnotation) => void;
}> = ({
  artifactId,
  versionId,
  rootFrameId,
  filename,
  contentType,
  contentUrl,
  annotations,
  resourceUrls,
  onSelectionChange,
  onAnnotationClick,
}) => {
  const { t } = useTranslation();
  const plan = resolveSynonBiomedArtifactPreviewPlan({ filename, contentType });
  const isTiffArtifact =
    contentType === 'image/tiff' || filename.toLowerCase().endsWith('.tif') || filename.toLowerCase().endsWith('.tiff');

  if (contentType?.startsWith('image/') && !isTiffArtifact) {
    return (
      <SynonBiomedImageArtifactViewer
        filename={filename}
        contentUrl={contentUrl}
        annotations={annotations}
        onSelectionChange={onSelectionChange}
        onAnnotationClick={onAnnotationClick}
      />
    );
  }

  if (contentType === 'application/pdf') {
    return (
      <SynonBiomedPdfArtifactViewer
        filename={filename}
        contentUrl={contentUrl}
        annotations={annotations}
        onSelectionChange={onSelectionChange}
        onAnnotationClick={onAnnotationClick}
      />
    );
  }

  if (contentType?.startsWith('video/')) {
    return <VideoPreview url={contentUrl} filename={filename} />;
  }

  if (contentType?.startsWith('audio/')) {
    return <AudioPreview url={contentUrl} filename={filename} />;
  }

  if (plan.type === 'video') {
    return <VideoPreview url={contentUrl} filename={filename} />;
  }

  if (plan.type === 'audio') {
    return <AudioPreview url={contentUrl} filename={filename} />;
  }

  if (plan.type === 'word') {
    return <OfficeDocPreview artifactId={artifactId} versionId={versionId} />;
  }

  if (plan.type === 'ppt') {
    return <PptViewer artifactId={artifactId} versionId={versionId} />;
  }

  if (plan.type === 'table' && ['.xlsx', '.xlsm'].some((extension) => filename.toLowerCase().endsWith(extension))) {
    return <ExcelPreview artifactId={artifactId} versionId={versionId} />;
  }

  if (plan.type === 'structure') {
    return (
      <SynonBiomedStructureViewer filename={filename} contentUrl={contentUrl} rootFrameId={rootFrameId ?? undefined} />
    );
  }

  if (plan.type === 'molecule') {
    const interactiveFormat = filename.toLowerCase().match(/\.(ket|rxn)$/)?.[1] as 'ket' | 'rxn' | undefined;
    if (interactiveFormat) {
      return (
        <SynonBiomedMcpAppArtifactViewer
          filename={filename}
          contentUrl={contentUrl}
          contentParam={interactiveFormat}
          rootFrameId={rootFrameId ?? undefined}
          frameId={rootFrameId ?? undefined}
          artifactId={artifactId}
        />
      );
    }
    return <SynonBiomedMoleculeViewer filename={filename} contentUrl={contentUrl} />;
  }

  if (plan.type === 'table') {
    return <SynonBiomedTableViewer filename={filename} contentUrl={contentUrl} />;
  }

  if (plan.type === 'msa') {
    return <SynonBiomedMsaViewer filename={filename} contentUrl={contentUrl} />;
  }

  if (plan.type === 'genome') {
    return <SynonBiomedGenomeViewer filename={filename} contentUrl={contentUrl} />;
  }

  if (plan.type === 'sequence') {
    return <SynonBiomedSequenceViewer filename={filename} contentUrl={contentUrl} />;
  }

  if (plan.type === 'notebook') {
    return <SynonBiomedNotebookViewer filename={filename} contentUrl={contentUrl} />;
  }

  if (plan.type === 'hdf5') {
    return <SynonBiomedHdf5Viewer filename={filename} contentUrl={contentUrl} />;
  }

  if (plan.type === 'unsupported') {
    return <UnsupportedPreview filename={filename} contentType={contentType} downloadUrl={contentUrl} />;
  }

  if (plan.type === 'latex') {
    return (
      <SynonBiomedLatexArtifactViewer
        filename={filename}
        contentUrl={contentUrl}
        annotations={annotations}
        resourceUrls={resourceUrls}
        onSelectionChange={onSelectionChange}
        onAnnotationClick={onAnnotationClick}
      />
    );
  }

  if (plan.fetchText && (plan.type === 'code' || plan.type === 'markdown' || plan.type === 'html')) {
    return (
      <SynonBiomedTextArtifactViewer
        filename={filename}
        contentUrl={contentUrl}
        kind={plan.type}
        language={plan.language}
        annotations={annotations}
        onSelectionChange={onSelectionChange}
        onAnnotationClick={onAnnotationClick}
      />
    );
  }

  return (
    <div className='size-full min-h-360px flex-center flex-col gap-12px text-t-secondary'>
      <FileText theme='outline' size={30} />
      <a href={contentUrl} target='_blank' rel='noreferrer' className='text-13px text-[rgb(var(--primary-6))]'>
        {t('preview.artifact.openOriginal')}
      </a>
    </div>
  );
};

const LATEX_IMAGE_EXTENSIONS = new Set(['avif', 'gif', 'jpeg', 'jpg', 'pdf', 'png', 'svg', 'webp']);

function buildLatexArtifactResourceUrls(artifacts: SynonBiomedProjectArtifact[]): Readonly<Record<string, string>> {
  const resources: Record<string, string> = {};
  for (const artifact of artifacts) {
    const extension = artifact.filename.split('.').at(-1)?.toLowerCase() ?? '';
    if (!artifact.contentType?.startsWith('image/') && !LATEX_IMAGE_EXTENSIONS.has(extension)) continue;
    const contentUrl = getSynonBiomedArtifactContentUrl(artifact.artifactId);
    for (const value of [artifact.filename, artifact.filePath]) {
      if (!value) continue;
      const normalized = value.trim().replaceAll('\\', '/').replace(/^\.\//, '').toLowerCase();
      if (!normalized || normalized.includes('../')) continue;
      const basename = normalized.split('/').at(-1) ?? normalized;
      resources[normalized] = contentUrl;
      resources[basename] = contentUrl;
      resources[basename.replace(/\.[a-z0-9]+$/i, '')] = contentUrl;
    }
  }
  return resources;
}

function selectionToolbarStyle(selection: SynonBiomedArtifactCanvasSelection): React.CSSProperties {
  const left = Math.max(68, Math.min(selection.x, window.innerWidth - 68));
  const top = Math.max(12, Math.min(selection.y + 8, window.innerHeight - 48));
  return { left, top, transform: 'translateX(-50%)' };
}

function makeCopyFilename(filename: string, suffix: string): string {
  const dotIndex = filename.lastIndexOf('.');
  if (dotIndex <= 0) return `${filename}-${suffix}`;
  return `${filename.slice(0, dotIndex)}-${suffix}${filename.slice(dotIndex)}`;
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function formatDate(value: string | null, language: string, unknownLabel: string): string {
  if (!value) return unknownLabel;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString(language);
}

function formatStructuredValue(value: unknown): string {
  if (typeof value === 'string') return value;
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

export default ArtifactPreview;

function inspectorTabFromSearch(search: string): string {
  const requested = new URLSearchParams(search).get('tab');
  return requested === 'versions' ||
    requested === 'annotations' ||
    requested === 'verification' ||
    requested === 'lineage'
    ? requested
    : 'details';
}

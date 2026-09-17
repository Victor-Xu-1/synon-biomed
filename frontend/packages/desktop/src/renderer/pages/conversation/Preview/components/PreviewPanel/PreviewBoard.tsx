/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { insertArtifactReferenceIntoActiveComposer } from '@/renderer/components/chat/SendBox/composerReferenceBridge';
import PreviewLoadingState from '@/renderer/components/media/PreviewLoadingState';
import { normalizeSynonBiomedJsonLines } from '@/renderer/services/synonBiomedArtifactPreview';
import {
  createSynonBiomedTextArtifactVersion,
  getSynonBiomedArtifactVersionContentUrl,
} from '@/renderer/services/synonBiomedArtifacts';
import { Message } from '@arco-design/web-react';
import {
  Close as CloseGlyph,
  Download as DownloadGlyph,
  Edit as EditGlyph,
  FullScreen as FullScreenGlyph,
  LayoutOne as LayoutOneGlyph,
  LayoutThree as LayoutThreeGlyph,
  LayoutTwo as LayoutTwoGlyph,
  MessageOne as MessageOneGlyph,
  OffScreen as OffScreenGlyph,
  Save as SaveGlyph,
} from '@icon-park/react';
import React, { useEffect, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { PreviewToolbarExtrasProvider } from '../../context/PreviewToolbarExtrasContext';
import { type PreviewTab, usePreviewContext } from '../../context/PreviewContext';
import { getPreviewVersionContentType } from '../../previewContentPersistence';
import CodeEditor from '../editors/CodeEditor';
import DelimitedTableEditor from '../editors/DelimitedTableEditor';
import { isPreviewTabEditable } from '../editors/previewEditingPolicy';
import {
  LazyDiffPreview as DiffPreview,
  LazyExcelPreview as ExcelPreview,
  LazyHTMLRenderer as HTMLRenderer,
  LazyImagePreview as ImagePreview,
  LazyMediaPreview as MediaPreview,
  LazyMarkdownPreview as MarkdownPreview,
  LazyOfficeDocPreview as OfficeDocPreview,
  LazyPDFPreview as PDFPreview,
  LazyPptPreview as PptViewer,
  LazySynonBiomedGenomeViewer as SynonBiomedGenomeViewer,
  LazySynonBiomedHdf5Viewer as SynonBiomedHdf5Viewer,
  LazySynonBiomedJsonViewer as SynonBiomedJsonViewer,
  LazySynonBiomedMcpAppArtifactViewer as SynonBiomedMcpAppArtifactViewer,
  LazySynonBiomedMcpAppViewer as SynonBiomedMcpAppViewer,
  LazySynonBiomedMoleculeViewer as SynonBiomedMoleculeViewer,
  LazySynonBiomedMsaViewer as SynonBiomedMsaViewer,
  LazySynonBiomedNotebookViewer as SynonBiomedNotebookViewer,
  LazySynonBiomedSequenceViewer as SynonBiomedSequenceViewer,
  LazySynonBiomedStructureViewer as SynonBiomedStructureViewer,
  LazySynonBiomedTableViewer as SynonBiomedTableViewer,
  LazyUnsupportedPreview as UnsupportedPreview,
  LazyArchivePreview as ArchivePreview,
  LazyURLPreview as URLViewer,
} from '../viewers/scientificPreviewLoaders';
import { ketcherMcpAppSaveContract } from '../viewers/mcpAppSaveContracts';
import { downloadPreviewTab } from './previewDownload';
import PreviewRegionCommentLayer from './PreviewRegionCommentLayer';
import { formatPreviewRegionCommentForComposer } from './previewRegionCommentModel';

const noopToolbarExtras = { setExtras: (): void => undefined };
const PREVIEW_BOARD_COLUMNS_KEY = 'synon-ai_preview_board_columns';
const DocumentWysiwygEditor = React.lazy(() => import('../editors/DocumentWysiwygEditor'));

type PreviewBoardColumns = 1 | 2 | 3;

function readPreviewBoardColumns(): PreviewBoardColumns {
  try {
    const stored = Number.parseInt(localStorage.getItem(PREVIEW_BOARD_COLUMNS_KEY) ?? '', 10);
    return stored === 1 || stored === 2 || stored === 3 ? stored : 2;
  } catch {
    return 2;
  }
}

function persistPreviewBoardColumns(columns: PreviewBoardColumns): void {
  try {
    localStorage.setItem(PREVIEW_BOARD_COLUMNS_KEY, String(columns));
  } catch {
    // Storage can be unavailable in privacy-restricted webviews. The selected
    // layout still applies for the current preview session.
  }
}

function getBoardScrollBehavior(): ScrollBehavior {
  return window.matchMedia?.('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth';
}

type PreviewBoardFullscreenLayerProps = React.PropsWithChildren<{
  active: boolean;
  label: string;
}>;

/**
 * Promote a board tile to the document top layer without changing the board's
 * grid. The board remains mounted underneath the opaque layer, so returning
 * from fullscreen restores the exact two/three-column layout instead of
 * reflowing the other files.
 */
const PreviewBoardFullscreenLayer: React.FC<PreviewBoardFullscreenLayerProps> = ({ active, label, children }) => {
  useEffect(() => {
    if (!active || typeof document === 'undefined') return;
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      document.body.style.overflow = previousOverflow;
    };
  }, [active]);

  if (!active || typeof document === 'undefined') return <>{children}</>;

  return createPortal(
    <div
      className='preview-panel preview-board__fullscreen-layer'
      data-testid='preview-board-fullscreen-layer'
      role='dialog'
      aria-modal='true'
      aria-label={label}
    >
      {children}
    </div>,
    document.body
  );
};

type PreviewBoardTileProps = {
  tab: PreviewTab;
  conversationId?: string;
  focused: boolean;
  active: boolean;
  editing: boolean;
  saving: boolean;
  regionCommentMode: boolean;
  onActivate: () => void;
  onStartEdit: () => void;
  onCancelEdit: () => void;
  onSaveEdit: () => void;
  onContentChange: (content: string) => void;
  onToggleFocus: () => void;
  onToggleRegionComment: () => void;
  onExitRegionComment: () => void;
  onClose: () => void;
};

const PreviewBoardTile: React.FC<PreviewBoardTileProps> = ({
  tab,
  conversationId,
  focused,
  active,
  editing,
  saving,
  regionCommentMode,
  onActivate,
  onStartEdit,
  onCancelEdit,
  onSaveEdit,
  onContentChange,
  onToggleFocus,
  onToggleRegionComment,
  onExitRegionComment,
  onClose,
}) => {
  const { t, i18n } = useTranslation();
  const { addToSendBox } = usePreviewContext();
  const [messageApi, messageContextHolder] = Message.useMessage();
  const fileName = tab.metadata?.file_name || tab.title;
  const canAddToMessage = Boolean(tab.metadata?.artifactId && tab.metadata?.versionId);
  const canEdit = useMemo(() => isPreviewTabEditable(tab), [tab]);

  const addToMessage = () => {
    if (!tab.metadata?.artifactId || !tab.metadata?.versionId) {
      messageApi.error(t('preview.board.addUnavailable'));
      return;
    }
    const inserted = insertArtifactReferenceIntoActiveComposer({
      filename: fileName,
      artifactId: tab.metadata.artifactId,
      versionId: tab.metadata.versionId,
    });
    if (inserted) messageApi.success(t('preview.board.addedToMessage'));
    else messageApi.error(t('preview.board.addUnavailable'));
  };

  const download = async () => {
    try {
      await downloadPreviewTab(tab);
    } catch {
      messageApi.error(t('messages.downloadFailed'));
    }
  };

  return (
    <PreviewBoardFullscreenLayer active={focused} label={fileName}>
      <article
        className={`preview-board__tile${
          focused ? ' preview-board__tile--fullscreen' : ''
        }${active ? ' preview-board__tile--active' : ''}`}
        data-preview-tab-id={tab.id}
        data-editing={editing || undefined}
        data-testid='preview-board-tile'
        onPointerDown={onActivate}
      >
        {React.Children.toArray(messageContextHolder)}
        <header className='preview-board__tile-header'>
          <span className='preview-board__file-name' title={fileName}>
            {fileName}
          </span>
          <div className='preview-board__tile-actions'>
            {editing ? (
              <>
                <BoardIconButton label={t('preview.board.cancelEdit')} disabled={saving} onClick={onCancelEdit}>
                  <CloseGlyph theme='outline' size={16} />
                </BoardIconButton>
                <BoardIconButton
                  label={t('preview.board.saveFile')}
                  disabled={saving || !tab.isDirty}
                  onClick={onSaveEdit}
                >
                  <SaveGlyph theme='outline' size={16} />
                </BoardIconButton>
              </>
            ) : (
              <>
                {canEdit ? (
                  <BoardIconButton label={t('preview.board.editFile')} onClick={onStartEdit}>
                    <EditGlyph theme='outline' size={16} />
                  </BoardIconButton>
                ) : null}
                <BoardIconButton
                  label={t('preview.board.addToMessage')}
                  disabled={!canAddToMessage}
                  onClick={addToMessage}
                >
                  <MessageOneGlyph theme='outline' size={16} />
                </BoardIconButton>
                <BoardIconButton
                  label={
                    regionCommentMode ? t('preview.regionComment.finishMode') : t('preview.regionComment.startMode')
                  }
                  pressed={regionCommentMode}
                  onClick={onToggleRegionComment}
                >
                  <RegionCommentGlyph />
                </BoardIconButton>
                <BoardIconButton label={t('preview.downloadFile')} onClick={() => void download()}>
                  <DownloadGlyph theme='outline' size={16} />
                </BoardIconButton>
                <BoardIconButton
                  label={focused ? t('preview.board.restoreFile') : t('preview.board.expandFile')}
                  pressed={focused}
                  onClick={onToggleFocus}
                >
                  {focused ? (
                    <OffScreenGlyph theme='outline' size={16} />
                  ) : (
                    <FullScreenGlyph theme='outline' size={16} />
                  )}
                </BoardIconButton>
                <BoardIconButton label={t('preview.board.closeFile')} onClick={onClose}>
                  <CloseGlyph theme='outline' size={16} />
                </BoardIconButton>
              </>
            )}
          </div>
        </header>
        {tab.metadata?.truncated ? <div className='preview-board__notice'>{t('preview.truncatedBanner')}</div> : null}
        <div
          className={`preview-board__tile-body preview-panel__body preview-panel__body--${tab.content_type}${
            regionCommentMode ? ' preview-panel__body--region-commenting' : ''
          }`}
        >
          <PreviewToolbarExtrasProvider value={noopToolbarExtras}>
            <React.Suspense fallback={<PreviewLoadingState label={t('common.loading')} />}>
              <PreviewBoardTileContent
                tab={tab}
                conversationId={conversationId}
                editing={editing}
                onContentChange={onContentChange}
              />
            </React.Suspense>
          </PreviewToolbarExtrasProvider>
          <PreviewRegionCommentLayer
            active={regionCommentMode}
            source={{
              fileName,
              contentType: tab.content_type,
              artifactId: tab.metadata?.artifactId,
              versionId: tab.metadata?.versionId,
              filePath: tab.metadata?.file_path,
            }}
            onExit={onExitRegionComment}
            onAdd={(comment) => {
              addToSendBox(formatPreviewRegionCommentForComposer(comment, i18n.resolvedLanguage));
              messageApi.success(t('preview.regionComment.added'));
            }}
          />
        </div>
      </article>
    </PreviewBoardFullscreenLayer>
  );
};

const PreviewBoardTileContent: React.FC<{
  tab: PreviewTab;
  conversationId?: string;
  editing: boolean;
  onContentChange: (content: string) => void;
}> = ({ tab, conversationId, editing, onContentChange }) => {
  const { t } = useTranslation();
  const { content, content_type: type, metadata } = tab;
  const fileName = metadata?.file_name || tab.title;

  if (metadata?.missingFile) {
    return (
      <div className='preview-board__empty-state'>
        <strong>{t('preview.missingFile.title')}</strong>
        <span>{metadata.file_path || t('preview.errors.missingFilePath')}</span>
      </div>
    );
  }
  if (type === 'markdown') {
    if (editing) {
      return (
        <DocumentWysiwygEditor
          format='markdown'
          value={content}
          fileName={fileName}
          filePath={metadata?.file_path}
          workspace={metadata?.workspace}
          conversationId={conversationId}
          onChange={onContentChange}
        />
      );
    }
    return (
      <MarkdownPreview
        content={content}
        file_path={metadata?.file_path}
        workspace={metadata?.workspace}
        companionArtifactUrls={metadata?.companionArtifactUrls}
      />
    );
  }
  if (type === 'html') {
    if (editing) {
      return (
        <DocumentWysiwygEditor
          format='html'
          value={content}
          fileName={fileName}
          filePath={metadata?.file_path}
          workspace={metadata?.workspace}
          conversationId={conversationId}
          onChange={onContentChange}
        />
      );
    }
    return (
      <HTMLRenderer
        content={content}
        file_path={metadata?.file_path}
        workspace={metadata?.workspace}
        isDirty={tab.isDirty}
        copySuccessMessage={t('preview.html.copySuccess')}
      />
    );
  }
  if (type === 'diff') return <DiffPreview content={content} metadata={metadata} hideToolbar />;
  if (type === 'code' || type === 'latex') {
    const lowerName = fileName.toLowerCase();
    const isJsonLines = metadata?.language === 'jsonl' || lowerName.endsWith('.jsonl') || lowerName.endsWith('.ndjson');
    if (metadata?.language === 'json' || isJsonLines || lowerName.endsWith('.json')) {
      let jsonContent = content;
      if (isJsonLines) {
        try {
          jsonContent = normalizeSynonBiomedJsonLines(content);
        } catch {
          // Keep the source unchanged so the viewer can display its bounded invalid-document state.
        }
      }
      return (
        <SynonBiomedJsonViewer
          filename={fileName}
          content={jsonContent}
          readOnly={!editing}
          onContentChange={onContentChange}
        />
      );
    }
    return (
      <CodeEditor
        value={content}
        onChange={onContentChange}
        language={metadata?.language}
        fileName={metadata?.file_name}
        readOnly={!editing}
        targetLine={metadata?.targetLine}
        targetColumn={metadata?.targetColumn}
      />
    );
  }
  if (type === 'pdf') return <PDFPreview file_path={metadata?.file_path} content={content} />;
  if (type === 'ppt') {
    return (
      <PptViewer
        file_path={metadata?.file_path}
        artifactId={metadata?.artifactId}
        versionId={metadata?.versionId}
        content={content}
        workspace={metadata?.workspace}
      />
    );
  }
  if (type === 'word') {
    return (
      <OfficeDocPreview
        file_path={metadata?.file_path}
        artifactId={metadata?.artifactId}
        versionId={metadata?.versionId}
        content={content}
        workspace={metadata?.workspace}
      />
    );
  }
  if (type === 'excel') {
    return (
      <ExcelPreview
        file_path={metadata?.file_path}
        artifactId={metadata?.artifactId}
        versionId={metadata?.versionId}
        content={content}
        workspace={metadata?.workspace}
      />
    );
  }
  if (type === 'image') {
    return (
      <ImagePreview
        file_path={metadata?.file_path}
        content={content}
        file_name={fileName}
        workspace={metadata?.workspace}
      />
    );
  }
  if (type === 'audio' || type === 'video') {
    return (
      <MediaPreview
        mediaType={type}
        filename={fileName}
        content={metadata?.contentUrl ?? content}
        filePath={metadata?.file_path}
      />
    );
  }
  if (type === 'structure') {
    return (
      <SynonBiomedStructureViewer
        filename={fileName}
        contentUrl={metadata?.contentUrl}
        content={metadata?.contentUrl ? undefined : content}
        rootFrameId={metadata?.rootFrameId}
        conversationId={conversationId}
        companionArtifactUrls={metadata?.companionArtifactUrls}
      />
    );
  }
  if (type === 'molecule') {
    const interactiveFormat = fileName.toLowerCase().match(/\.(ket|rxn)$/)?.[1];
    if (interactiveFormat === 'ket' || interactiveFormat === 'rxn') {
      if (metadata?.contentUrl) {
        return (
          <SynonBiomedMcpAppArtifactViewer
            filename={fileName}
            contentUrl={metadata.contentUrl}
            contentParam={interactiveFormat}
            rootFrameId={metadata.rootFrameId}
            frameId={metadata.rootFrameId}
            artifactId={metadata.artifactId}
          />
        );
      }
      return (
        <SynonBiomedMcpAppViewer
          serverId='bundled:ketcher-chemistry'
          serverName='Ketcher Chemistry'
          openTool='open_sketcher'
          resourceUri='ui://ketcher-chemistry/editor'
          docsUrl='https://github.com/epam/ketcher/blob/master/documentation/help.md#ketcher-overview'
          rootFrameId={metadata?.rootFrameId}
          frameId={metadata?.rootFrameId}
          artifactId={metadata?.artifactId}
          initialArguments={{
            [interactiveFormat]: content,
            filename: fileName,
          }}
          saveContract={ketcherMcpAppSaveContract}
        />
      );
    }
    return (
      <SynonBiomedMoleculeViewer
        filename={fileName}
        contentUrl={metadata?.contentUrl}
        content={metadata?.contentUrl ? undefined : content}
      />
    );
  }
  if (type === 'table') {
    if (editing) {
      return <DelimitedTableEditor filename={fileName} content={content} onChange={onContentChange} />;
    }
    return (
      <SynonBiomedTableViewer
        filename={fileName}
        contentUrl={metadata?.contentUrl}
        content={metadata?.contentUrl ? undefined : content}
      />
    );
  }
  if (type === 'msa') {
    return (
      <SynonBiomedMsaViewer
        filename={fileName}
        contentUrl={metadata?.contentUrl}
        content={metadata?.contentUrl ? undefined : content}
      />
    );
  }
  if (type === 'genome') {
    return (
      <SynonBiomedGenomeViewer
        filename={fileName}
        contentUrl={metadata?.contentUrl}
        content={metadata?.contentUrl ? undefined : content}
      />
    );
  }
  if (type === 'sequence') {
    return (
      <SynonBiomedSequenceViewer
        filename={fileName}
        contentUrl={metadata?.contentUrl}
        content={metadata?.contentUrl ? undefined : content}
      />
    );
  }
  if (type === 'notebook') {
    return (
      <SynonBiomedNotebookViewer
        filename={fileName}
        contentUrl={metadata?.contentUrl}
        content={metadata?.contentUrl ? undefined : content}
      />
    );
  }
  if (type === 'hdf5') return <SynonBiomedHdf5Viewer filename={fileName} contentUrl={metadata?.contentUrl} />;
  if (type === 'archive') return <ArchivePreview filename={fileName} contentUrl={metadata?.contentUrl} />;
  if (type === 'unsupported') {
    return <UnsupportedPreview filename={fileName} downloadUrl={metadata?.contentUrl} filePath={metadata?.file_path} />;
  }
  if (type === 'url') return <URLViewer url={content} title={metadata?.title} />;

  return <div className='preview-board__empty-state'>{t('preview.errors.conversionFailed')}</div>;
};

const PreviewBoard: React.FC<{ conversationId?: string }> = ({ conversationId }) => {
  const { t } = useTranslation();
  const {
    isOpen,
    tabs,
    activeTabId,
    closePreview,
    closeTab,
    switchTab,
    updateTabContent,
    commitTabContent,
    saveContent,
  } = usePreviewContext();
  const [messageApi, messageContextHolder] = Message.useMessage();
  const [focusedTabId, setFocusedTabId] = useState<string | null>(null);
  const [isBoardFullscreen, setIsBoardFullscreen] = useState(false);
  const [editingTabId, setEditingTabId] = useState<string | null>(null);
  const [regionCommentTabId, setRegionCommentTabId] = useState<string | null>(null);
  const [savingTabId, setSavingTabId] = useState<string | null>(null);
  const [saveAnnouncement, setSaveAnnouncement] = useState('');
  const [columns, setColumns] = useState<PreviewBoardColumns>(readPreviewBoardColumns);
  const tileRefs = useRef(new Map<string, HTMLElement>());

  useEffect(() => {
    if (focusedTabId && !tabs.some((tab) => tab.id === focusedTabId)) setFocusedTabId(null);
    if (editingTabId && !tabs.some((tab) => tab.id === editingTabId)) setEditingTabId(null);
    if (regionCommentTabId && !tabs.some((tab) => tab.id === regionCommentTabId)) setRegionCommentTabId(null);
  }, [editingTabId, focusedTabId, regionCommentTabId, tabs]);

  useEffect(() => {
    if (!focusedTabId && !isBoardFullscreen) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      if (focusedTabId) setFocusedTabId(null);
      else setIsBoardFullscreen(false);
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [focusedTabId, isBoardFullscreen]);

  useEffect(() => {
    if (!activeTabId || focusedTabId) return;
    const tile = tileRefs.current.get(activeTabId);
    tile?.scrollIntoView({
      block: 'nearest',
      inline: 'nearest',
      behavior: getBoardScrollBehavior(),
    });
  }, [activeTabId, focusedTabId]);

  const closeBoard = () => {
    if (tabs.some((tab) => tab.isDirty)) {
      messageApi.warning(t('preview.board.unsavedCloseBlocked'));
      return;
    }
    setIsBoardFullscreen(false);
    closePreview();
  };

  const changeColumns = (nextColumns: PreviewBoardColumns) => {
    setColumns(nextColumns);
    persistPreviewBoardColumns(nextColumns);
  };

  const cancelEdit = (tab: PreviewTab) => {
    updateTabContent(tab.id, tab.originalContent ?? tab.content);
    setEditingTabId(null);
  };

  const saveEdit = async (tab: PreviewTab) => {
    if (!tab.isDirty || savingTabId) return;
    setSavingTabId(tab.id);
    try {
      if (tab.metadata?.artifactId) {
        const created = await createSynonBiomedTextArtifactVersion({
          artifactId: tab.metadata.artifactId,
          content: tab.content,
          contentType: getPreviewVersionContentType(tab.content_type, tab.metadata?.file_name || tab.title),
          parentVersionId: tab.metadata.versionId,
        });
        if (!created.versionId) throw new Error('Artifact version response did not include a version id');
        commitTabContent(tab.id, tab.content, {
          versionId: created.versionId,
          contentUrl: getSynonBiomedArtifactVersionContentUrl(created.versionId),
        });
      } else {
        const saved = await saveContent(tab.id, tab.content);
        if (!saved) throw new Error('Workspace file save returned false');
      }
      setEditingTabId(null);
      setSaveAnnouncement(t('preview.board.saveSucceeded'));
    } catch {
      messageApi.error(t('preview.board.saveFailed'));
    } finally {
      setSavingTabId(null);
    }
  };

  if (!isOpen || tabs.length === 0) return null;

  return (
    <PreviewBoardFullscreenLayer active={isBoardFullscreen} label={t('preview.board.title')}>
      <section
        className='preview-panel preview-board'
        data-columns={columns}
        data-testid='preview-board'
        style={{ '--preview-board-columns': columns } as React.CSSProperties}
      >
        {React.Children.toArray(messageContextHolder)}
        <span className='sr-only' role='status' aria-live='polite'>
          {saveAnnouncement}
        </span>
        <header className='preview-board__header'>
          <div className='min-w-0'>
            <h2>{t('preview.board.title')}</h2>
            <p>{t('preview.board.fileCount', { count: tabs.length })}</p>
          </div>
          <div className='preview-board__header-actions'>
            {!focusedTabId ? (
              <div className='preview-board__layout-switcher' aria-label={t('preview.board.layout')} role='group'>
                <BoardIconButton
                  label={t('preview.board.oneColumn')}
                  pressed={columns === 1}
                  onClick={() => changeColumns(1)}
                >
                  <LayoutOneGlyph theme='outline' size={16} />
                </BoardIconButton>
                <BoardIconButton
                  label={t('preview.board.twoColumns')}
                  pressed={columns === 2}
                  onClick={() => changeColumns(2)}
                >
                  <LayoutTwoGlyph theme='outline' size={16} />
                </BoardIconButton>
                <BoardIconButton
                  label={t('preview.board.threeColumns')}
                  pressed={columns === 3}
                  onClick={() => changeColumns(3)}
                >
                  <LayoutThreeGlyph theme='outline' size={16} />
                </BoardIconButton>
              </div>
            ) : null}
            <BoardIconButton
              label={isBoardFullscreen ? t('preview.board.exitFullscreen') : t('preview.board.openFullscreen')}
              pressed={isBoardFullscreen}
              onClick={() => setIsBoardFullscreen((current) => !current)}
            >
              {isBoardFullscreen ? (
                <OffScreenGlyph theme='outline' size={16} />
              ) : (
                <FullScreenGlyph theme='outline' size={16} />
              )}
            </BoardIconButton>
            <BoardIconButton label={t('preview.board.closeBoard')} onClick={closeBoard}>
              <CloseGlyph theme='outline' size={16} />
            </BoardIconButton>
          </div>
        </header>
        <div className='preview-board__grid'>
          {tabs.map((tab) => (
            <div
              key={tab.id}
              ref={(node) => {
                if (node) tileRefs.current.set(tab.id, node);
                else tileRefs.current.delete(tab.id);
              }}
            >
              <PreviewBoardTile
                tab={tab}
                conversationId={conversationId}
                active={tab.id === activeTabId}
                focused={tab.id === focusedTabId}
                editing={tab.id === editingTabId}
                saving={tab.id === savingTabId}
                regionCommentMode={tab.id === regionCommentTabId}
                onActivate={() => switchTab(tab.id)}
                onStartEdit={() => {
                  switchTab(tab.id);
                  setRegionCommentTabId(null);
                  setEditingTabId(tab.id);
                }}
                onCancelEdit={() => cancelEdit(tab)}
                onSaveEdit={() => void saveEdit(tab)}
                onContentChange={(content) => updateTabContent(tab.id, content)}
                onToggleFocus={() => {
                  switchTab(tab.id);
                  setFocusedTabId((current) => (current === tab.id ? null : tab.id));
                }}
                onToggleRegionComment={() => {
                  switchTab(tab.id);
                  setRegionCommentTabId((current) => (current === tab.id ? null : tab.id));
                }}
                onExitRegionComment={() => setRegionCommentTabId(null)}
                onClose={() => {
                  if (tab.isDirty || tab.id === editingTabId) {
                    messageApi.warning(t('preview.board.unsavedCloseBlocked'));
                    return;
                  }
                  closeTab(tab.id);
                }}
              />
            </div>
          ))}
        </div>
      </section>
    </PreviewBoardFullscreenLayer>
  );
};

type BoardIconButtonProps = {
  label: string;
  children: React.ReactNode;
  onClick: () => void;
  disabled?: boolean;
  pressed?: boolean;
};

const BoardIconButton: React.FC<BoardIconButtonProps> = ({ label, children, onClick, disabled, pressed }) => (
  <button
    type='button'
    className='preview-board__icon-button'
    aria-label={label}
    title={label}
    aria-pressed={pressed}
    disabled={disabled}
    onClick={(event) => {
      event.stopPropagation();
      onClick();
    }}
  >
    {children}
  </button>
);

const RegionCommentGlyph = () => (
  <svg width='16' height='16' viewBox='0 0 24 24' fill='none' stroke='currentColor' strokeWidth='1.8'>
    <path d='M7 18.5 3.5 21l1-4.2A8.5 8.5 0 1 1 7 18.5Z' />
    <path d='M8 9h8M8 13h5' />
  </svg>
);

export default PreviewBoard;

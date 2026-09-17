/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { ipcBridge } from '@/common';
import { toLocalFileHref } from '@/renderer/components/Markdown/markdownUtils';
import { PreviewToolbarExtrasProvider, type PreviewToolbarExtras } from '../../context/PreviewToolbarExtrasContext';
import { usePreviewContext } from '../../context/PreviewContext';
import { Link } from '@arco-design/web-react';
import React, { useCallback, useEffect, useLayoutEffect, useMemo, useState } from 'react';
import { createPortal } from 'react-dom';
import CodeEditor from '../editors/CodeEditor';
import DelimitedTableEditor from '../editors/DelimitedTableEditor';
import { getPreviewEditingMode } from '../editors/previewEditingPolicy';
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
import PreviewLoadingState from '@/renderer/components/media/PreviewLoadingState';
import { normalizeSynonBiomedJsonLines } from '@/renderer/services/synonBiomedArtifactPreview';
import { PreviewToolbar, PreviewConfirmModals, type CloseTabConfirmState, type PreviewTab } from '.';
import PreviewArtifactActions from './PreviewArtifactActions';
import { usePreviewHistory } from '../../hooks';
import { useTranslation } from 'react-i18next';
import PreviewBoard from './PreviewBoard';
import { downloadPreviewTab } from './previewDownload';
import PreviewRegionCommentLayer from './PreviewRegionCommentLayer';
import { formatPreviewRegionCommentForComposer } from './previewRegionCommentModel';
import './preview.css';

const DocumentWysiwygEditor = React.lazy(() => import('../editors/DocumentWysiwygEditor'));

export const PreviewFullscreenLayer: React.FC<React.PropsWithChildren<{ active: boolean }>> = ({
  active,
  children,
}) => {
  useLayoutEffect(() => {
    if (!active || typeof document === 'undefined') return;
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      document.body.style.overflow = previousOverflow;
    };
  }, [active]);

  if (active && typeof document !== 'undefined') {
    return createPortal(children, document.body);
  }
  return <>{children}</>;
};

/**
 * 预览面板主组件
 * Main preview panel component
 *
 * 支持多 Tab 切换，每个 Tab 可以显示不同类型的内容
 * Supports multiple tabs, each tab can display different types of content
 */
const SinglePreviewPanel: React.FC<{ conversationId?: string }> = ({ conversationId }) => {
  const { t, i18n } = useTranslation();
  const {
    isOpen,
    tabs,
    activeTabId,
    activeTab,
    closeTab,
    switchTab,
    updateContent,
    markContentSaved,
    saveContent,
    addDomSnippet,
    addToSendBox,
  } = usePreviewContext();
  // 视图状态 / View states
  const [viewMode, setViewMode] = useState<'source' | 'preview'>('preview');
  const [isEditing, setIsEditing] = useState(false);
  const [isSavingEdit, setIsSavingEdit] = useState(false);
  const [isFullscreen, setIsFullscreen] = useState(false);
  const [inspectMode, setInspectMode] = useState(false);
  const [regionCommentMode, setRegionCommentMode] = useState(false);
  const [toolbarExtras, setToolbarExtras] = useState<PreviewToolbarExtras | null>(null);

  // 切换文件时把视图模式复位为预览，避免上一个文件的 source 模式串到下一个文件（如代码文件丢失语法高亮）。
  // 注意：单预览浏览模式下打开新文件会复用当前 tab 的 id，所以这里要监听实际显示的文件标识（路径 + 类型），
  // 而不是 activeTabId（它不会变）。
  // Reset view mode to preview when the displayed file changes so a previous file's source mode does not
  // leak into the next one (e.g. a code file losing syntax highlighting). In single-preview browse mode a
  // new file reuses the active tab's id, so we key on the file identity (path + type), not activeTabId.
  useEffect(() => {
    setViewMode('preview');
    setIsEditing(false);
    setRegionCommentMode(false);
  }, [activeTabId, activeTab?.metadata?.file_path, activeTab?.content_type]);

  useEffect(() => {
    if (isEditing) setRegionCommentMode(false);
  }, [isEditing]);

  useEffect(() => {
    if (!isFullscreen) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setIsFullscreen(false);
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [isFullscreen]);

  // 确认对话框状态 / Confirmation dialog states
  const [closeTabConfirm, setCloseTabConfirm] = useState<CloseTabConfirmState>({
    show: false,
    tabId: null,
  });

  // eslint-disable-next-line max-len
  const { snapshotSaving, historyTarget, handleSaveSnapshot, messageApi, messageContextHolder } = usePreviewHistory({
    activeTab,
    updateContent,
    markContentSaved,
  });

  const setToolbarExtrasCallback = useCallback((extras: PreviewToolbarExtras | null) => {
    setToolbarExtras(extras);
  }, []);

  // 处理 HTML 审核模式元素选中 / Handle HTML inspect mode element selection
  const handleElementSelected = useCallback(
    (element: { html: string; tag: string }) => {
      addDomSnippet(element.tag, element.html);
    },
    [addDomSnippet]
  );

  const toolbarExtrasContextValue = useMemo(
    () => ({
      setExtras: setToolbarExtrasCallback,
    }),
    [setToolbarExtrasCallback]
  );

  // 使用 useCallback 包装 updateContent，确保引用稳定 / Wrap updateContent with useCallback for stable reference
  const handleContentChange = useCallback(
    (new_content: string) => {
      // 严格的类型检查，防止 Event 对象被错误传递 / Strict type checking to prevent Event object from being passed incorrectly
      if (typeof new_content !== 'string') {
        return;
      }
      try {
        updateContent(new_content);
      } catch {
        console.warn('[PreviewPanel] Failed to update preview content');
      }
    },
    [updateContent]
  );

  // 处理关闭tab / Handle close tab
  const handleCloseTab = useCallback(
    (tabId: string) => {
      const targetTab = tabs.find((candidate) => candidate.id === tabId);
      // 如果tab有未保存的修改，显示确认对话框 / If tab has unsaved changes, show confirmation dialog
      if (targetTab?.isDirty) {
        setCloseTabConfirm({ show: true, tabId });
      } else {
        // 没有未保存的修改，直接关闭 / No unsaved changes, close directly
        closeTab(tabId);
      }
    },
    [tabs, closeTab]
  );

  // 保存并关闭tab / Save and close tab
  const handleSaveAndCloseTab = useCallback(async () => {
    if (!closeTabConfirm.tabId) return;

    try {
      const success = await saveContent(closeTabConfirm.tabId);
      if (!success) {
        messageApi.error(t('common.saveFailed'));
        return;
      }
      closeTab(closeTabConfirm.tabId);
      setCloseTabConfirm({ show: false, tabId: null });
    } catch {
      messageApi.error(t('common.saveFailed'));
    }
  }, [closeTabConfirm.tabId, saveContent, closeTab, messageApi, t]);

  // 不保存直接关闭tab / Close tab without saving
  const handleCloseWithoutSave = useCallback(() => {
    if (!closeTabConfirm.tabId) return;
    closeTab(closeTabConfirm.tabId);
    setCloseTabConfirm({ show: false, tabId: null });
  }, [closeTabConfirm.tabId, closeTab]);

  // 取消关闭tab / Cancel close tab
  const handleCancelCloseTab = useCallback(() => {
    setCloseTabConfirm({ show: false, tabId: null });
  }, []);

  // 如果预览面板未打开，不渲染 / Don't render if preview panel is not open
  if (!isOpen || !activeTab) return null;

  const { content, content_type, metadata } = activeTab;
  const isMarkdown = content_type === 'markdown';
  const isHTML = content_type === 'html';
  const isEditable = metadata?.editable !== false && !metadata?.truncated;
  const editingMode = useMemo(() => getPreviewEditingMode(activeTab), [activeTab]);
  const canEdit = editingMode !== null;

  const handleStartEditing = () => {
    if (!canEdit) return;
    setIsEditing(true);
  };

  const handleCancelEditing = () => {
    updateContent(activeTab.originalContent ?? activeTab.content);
    setIsEditing(false);
    setViewMode('preview');
  };

  const handleSaveEditing = async () => {
    if (!activeTab.isDirty || snapshotSaving || isSavingEdit) return;
    setIsSavingEdit(true);
    try {
      const saved = historyTarget?.artifact_id ? await handleSaveSnapshot() : await saveContent();
      if (!saved) return;
      setIsEditing(false);
      setViewMode('preview');
    } finally {
      setIsSavingEdit(false);
    }
  };

  // 对所有有 file_path 的文件显示"在系统中打开"按钮（统一在工具栏显示）
  // Show "Open in System" button for all files with file_path (unified in toolbar)
  const showOpenInSystemButton = Boolean(metadata?.file_path && !metadata?.contentUrl);

  // 下载文件到本地 / Download file to local system
  const handleDownload = useCallback(async () => {
    try {
      await downloadPreviewTab(activeTab);
    } catch {
      console.warn('[PreviewPanel] Failed to download file');
      messageApi.error(t('messages.downloadFailed'));
    }
  }, [activeTab, messageApi, t]);

  // 在系统默认应用中打开文件 / Open file in system default application
  const handleOpenInSystem = useCallback(async () => {
    if (!metadata?.file_path) {
      try {
        messageApi.error(t('preview.openInSystemFailed'));
      } catch {
        // Context holder may be unmounted
      }
      return;
    }

    try {
      // 使用系统默认应用打开文件 / Open file with system default application
      await ipcBridge.shell.openFile.invoke(metadata.file_path);
      try {
        messageApi.success(t('preview.openInSystemSuccess'));
      } catch {
        // Context holder may be unmounted after async operation
      }
    } catch {
      try {
        messageApi.error(t('preview.openInSystemFailed'));
      } catch {
        // Context holder may be unmounted after async operation
      }
    }
  }, [metadata?.file_path, messageApi, t]);

  const renderMissingFile = () => {
    const filePath = metadata?.file_path;
    const externalHref = filePath ? toLocalFileHref(filePath) : undefined;

    return (
      <div className='flex flex-1 flex-col items-center justify-center gap-10px px-24px text-center'>
        <div className='text-15px font-medium text-t-primary'>{t('preview.missingFile.title')}</div>
        <div className='max-w-560px break-all text-12px leading-18px text-t-secondary'>
          {filePath || t('preview.errors.missingFilePath')}
        </div>
        {externalHref && (
          <Link href={externalHref} target='_blank' rel='noreferrer' className='text-13px'>
            {t('preview.missingFile.openInNewTab')}
          </Link>
        )}
      </div>
    );
  };

  // 渲染预览内容 / Render preview content
  const renderContent = () => {
    if (metadata?.missingFile) return renderMissingFile();

    // Markdown 模式 / Markdown mode
    if (isMarkdown) {
      if (isEditing) {
        return (
          <DocumentWysiwygEditor
            key={activeTabId ?? undefined}
            format='markdown'
            value={content}
            fileName={metadata?.file_name || activeTab.title}
            filePath={metadata?.file_path}
            workspace={metadata?.workspace}
            conversationId={conversationId}
            onChange={handleContentChange}
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

    // HTML 模式 / HTML mode
    if (isHTML) {
      if (isEditing) {
        return (
          <DocumentWysiwygEditor
            key={activeTabId ?? undefined}
            format='html'
            value={content}
            fileName={metadata?.file_name || activeTab.title}
            filePath={metadata?.file_path}
            workspace={metadata?.workspace}
            conversationId={conversationId}
            onChange={handleContentChange}
          />
        );
      }
      return (
        <div className='flex-1 overflow-hidden'>
          <HTMLRenderer
            content={content}
            file_path={metadata?.file_path}
            workspace={metadata?.workspace}
            isDirty={activeTab?.isDirty}
            inspectMode={inspectMode}
            copySuccessMessage={t('preview.html.copySuccess')}
            onElementSelected={handleElementSelected}
          />
        </div>
      );
    }

    // 其他类型：全屏预览 / Other types: Full-screen preview
    if (content_type === 'diff') {
      return (
        <DiffPreview
          content={content}
          metadata={metadata}
          hideToolbar
          viewMode={viewMode}
          onViewModeChange={setViewMode}
        />
      );
    } else if (content_type === 'code' || content_type === 'latex') {
      const fileName = metadata?.file_name?.toLowerCase() ?? '';
      const isJsonLines = metadata?.language === 'jsonl' || fileName.endsWith('.jsonl') || fileName.endsWith('.ndjson');
      if (metadata?.language === 'json' || isJsonLines || fileName.endsWith('.json')) {
        let jsonContent = content;
        if (isJsonLines) {
          try {
            jsonContent = normalizeSynonBiomedJsonLines(content);
          } catch {
            // Keep the exact source visible; the JSON viewer reports a bounded invalid-document state.
          }
        }
        return (
          <SynonBiomedJsonViewer
            key={activeTabId ?? undefined}
            filename={metadata?.file_name || activeTab.title}
            content={jsonContent}
            readOnly={!isEditing || !isEditable}
            onContentChange={handleContentChange}
          />
        );
      }

      // 统一的代码查看器：预览态只读，显式进入编辑后才允许修改。
      // Unified code viewer: read-only in preview mode and writable only after entering edit mode explicitly.
      return (
        <div className='flex-1 overflow-hidden'>
          <CodeEditor
            key={activeTabId ?? undefined}
            value={content}
            onChange={handleContentChange}
            language={metadata?.language}
            fileName={metadata?.file_name}
            readOnly={!isEditing || !isEditable}
            targetLine={metadata?.targetLine}
            targetColumn={metadata?.targetColumn}
          />
        </div>
      );
    } else if (content_type === 'pdf') {
      return <PDFPreview file_path={metadata?.file_path} content={content} />;
    } else if (content_type === 'ppt') {
      return (
        <PptViewer
          file_path={metadata?.file_path}
          artifactId={metadata?.artifactId}
          versionId={metadata?.versionId}
          content={content}
          workspace={metadata?.workspace}
        />
      );
    } else if (content_type === 'word') {
      return (
        <OfficeDocPreview
          file_path={metadata?.file_path}
          artifactId={metadata?.artifactId}
          versionId={metadata?.versionId}
          content={content}
          workspace={metadata?.workspace}
        />
      );
    } else if (content_type === 'excel') {
      return (
        <ExcelPreview
          file_path={metadata?.file_path}
          artifactId={metadata?.artifactId}
          versionId={metadata?.versionId}
          content={content}
          workspace={metadata?.workspace}
        />
      );
    } else if (content_type === 'image') {
      return (
        <ImagePreview
          file_path={metadata?.file_path}
          content={content}
          file_name={metadata?.file_name || metadata?.title}
          workspace={metadata?.workspace}
        />
      );
    } else if (content_type === 'audio' || content_type === 'video') {
      return (
        <MediaPreview
          mediaType={content_type}
          filename={metadata?.file_name || activeTab.title}
          content={metadata?.contentUrl ?? content}
          filePath={metadata?.file_path}
        />
      );
    } else if (content_type === 'structure') {
      return (
        <SynonBiomedStructureViewer
          filename={metadata?.file_name || activeTab.title}
          contentUrl={metadata?.contentUrl}
          content={metadata?.contentUrl ? undefined : content}
          rootFrameId={metadata?.rootFrameId}
          conversationId={conversationId}
          companionArtifactUrls={metadata?.companionArtifactUrls}
        />
      );
    } else if (content_type === 'molecule') {
      const filename = metadata?.file_name || activeTab.title;
      const interactiveFormat = filename.toLowerCase().match(/\.(ket|rxn)$/)?.[1];
      if (interactiveFormat === 'ket' || interactiveFormat === 'rxn') {
        if (metadata?.contentUrl) {
          return (
            <SynonBiomedMcpAppArtifactViewer
              filename={filename}
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
            initialArguments={{ [interactiveFormat]: content, filename }}
            saveContract={ketcherMcpAppSaveContract}
          />
        );
      }
      return (
        <SynonBiomedMoleculeViewer
          filename={filename}
          contentUrl={metadata?.contentUrl}
          content={metadata?.contentUrl ? undefined : content}
        />
      );
    } else if (content_type === 'table') {
      if (isEditing && editingMode === 'delimited-table') {
        return (
          <DelimitedTableEditor
            key={activeTabId ?? undefined}
            filename={metadata?.file_name || activeTab.title}
            content={content}
            onChange={handleContentChange}
          />
        );
      }
      return (
        <SynonBiomedTableViewer
          filename={metadata?.file_name || activeTab.title}
          contentUrl={metadata?.contentUrl}
          content={metadata?.contentUrl ? undefined : content}
        />
      );
    } else if (content_type === 'msa') {
      return (
        <SynonBiomedMsaViewer
          filename={metadata?.file_name || activeTab.title}
          contentUrl={metadata?.contentUrl}
          content={metadata?.contentUrl ? undefined : content}
        />
      );
    } else if (content_type === 'genome') {
      return (
        <SynonBiomedGenomeViewer
          filename={metadata?.file_name || activeTab.title}
          contentUrl={metadata?.contentUrl}
          content={metadata?.contentUrl ? undefined : content}
        />
      );
    } else if (content_type === 'sequence') {
      return (
        <SynonBiomedSequenceViewer
          filename={metadata?.file_name || activeTab.title}
          contentUrl={metadata?.contentUrl}
          content={metadata?.contentUrl ? undefined : content}
        />
      );
    } else if (content_type === 'notebook') {
      return (
        <SynonBiomedNotebookViewer
          filename={metadata?.file_name || activeTab.title}
          contentUrl={metadata?.contentUrl}
          content={metadata?.contentUrl ? undefined : content}
        />
      );
    } else if (content_type === 'hdf5') {
      return (
        <SynonBiomedHdf5Viewer filename={metadata?.file_name || activeTab.title} contentUrl={metadata?.contentUrl} />
      );
    } else if (content_type === 'archive') {
      return <ArchivePreview filename={metadata?.file_name || activeTab.title} contentUrl={metadata?.contentUrl} />;
    } else if (content_type === 'unsupported') {
      return (
        <UnsupportedPreview
          filename={metadata?.file_name || activeTab.title}
          downloadUrl={metadata?.contentUrl}
          filePath={metadata?.file_path}
        />
      );
    } else if (content_type === 'url') {
      // URL 预览模式 / URL preview mode
      return <URLViewer url={content} title={metadata?.title} />;
    }

    return null;
  };

  // 将 tabs 转换为 PreviewTab 类型 / Convert tabs to PreviewTab type
  const previewTabs: PreviewTab[] = tabs.map((tab) => ({
    id: tab.id,
    title: tab.metadata?.file_name || tab.title,
    isDirty: tab.isDirty,
  }));

  return (
    <PreviewToolbarExtrasProvider value={toolbarExtrasContextValue}>
      <PreviewFullscreenLayer active={isFullscreen}>
        <div
          data-testid='preview-panel-shell'
          role={isFullscreen ? 'dialog' : undefined}
          aria-modal={isFullscreen || undefined}
          className={
            isFullscreen
              ? 'preview-panel preview-panel--fullscreen fixed inset-0 z-[2000] size-full flex flex-col overflow-hidden bg-1'
              : 'preview-panel h-full flex flex-col overflow-hidden bg-1 rounded-[16px]'
          }
        >
          {messageContextHolder}

          {/* 确认对话框 / Confirmation modals */}
          {/* eslint-disable-next-line max-len */}
          <PreviewConfirmModals
            closeTabConfirm={closeTabConfirm}
            onSaveAndCloseTab={handleSaveAndCloseTab}
            onCloseWithoutSave={handleCloseWithoutSave}
            onCancelCloseTab={handleCancelCloseTab}
          />

          {/* v1.1 parity: one unified file header. The previous tab strip duplicated the filename and controls. */}
          <PreviewToolbar
            content_type={content_type}
            file_name={metadata?.file_name || activeTab.title}
            canEdit={canEdit}
            isEditing={isEditing}
            isDirty={Boolean(activeTab.isDirty)}
            isSaving={snapshotSaving || isSavingEdit}
            isFullscreen={isFullscreen}
            showOpenInSystemButton={showOpenInSystemButton}
            historyTarget={historyTarget}
            onEdit={handleStartEditing}
            onCancelEdit={handleCancelEditing}
            onSaveEdit={() => void handleSaveEditing()}
            onFullscreenToggle={() => setIsFullscreen((current) => !current)}
            onOpenInSystem={handleOpenInSystem}
            onDownload={handleDownload}
            onClose={() => {
              setIsFullscreen(false);
              handleCloseTab(activeTab.id);
            }}
            inspectMode={inspectMode}
            onInspectModeToggle={isHTML && !isEditing ? () => setInspectMode(!inspectMode) : undefined}
            regionCommentMode={regionCommentMode}
            onRegionCommentModeToggle={!isEditing ? () => setRegionCommentMode((current) => !current) : undefined}
            leftExtra={toolbarExtras?.left}
            rightExtra={
              <>
                {toolbarExtras?.right}
                {metadata?.artifactId ? (
                  <PreviewArtifactActions
                    artifactId={metadata.artifactId}
                    versionId={metadata.versionId}
                    fileName={metadata.file_name || activeTab.title}
                    onRenamed={(fileName) => markContentSaved({ file_name: fileName, title: fileName })}
                    onDeleted={() => {
                      setIsFullscreen(false);
                      closeTab(activeTab.id);
                    }}
                  />
                ) : null}
              </>
            }
            tabs={previewTabs}
            activeTabId={activeTabId}
            onSwitchTab={switchTab}
          />

          {metadata?.truncated && (
            <div className='sticky top-0 z-1 px-16px py-10px text-12px bg-warning-1 text-warning-7 border-b border-warning-3'>
              {t('preview.truncatedBanner')}
            </div>
          )}

          {/* 预览内容 / Preview content */}
          <div
            className={`preview-panel__body preview-panel__body--${content_type}${
              regionCommentMode ? ' preview-panel__body--region-commenting' : ''
            }`}
          >
            <React.Suspense fallback={<PreviewLoadingState label={t('common.loading')} />}>
              {renderContent()}
            </React.Suspense>
            <PreviewRegionCommentLayer
              active={regionCommentMode}
              source={{
                fileName: metadata?.file_name || activeTab.title,
                contentType: content_type,
                artifactId: metadata?.artifactId,
                versionId: metadata?.versionId,
                filePath: metadata?.file_path,
              }}
              onExit={() => setRegionCommentMode(false)}
              onAdd={(comment) => {
                addToSendBox(formatPreviewRegionCommentForComposer(comment, i18n.resolvedLanguage));
                messageApi.success(t('preview.regionComment.added'));
              }}
            />
          </div>
        </div>
      </PreviewFullscreenLayer>
    </PreviewToolbarExtrasProvider>
  );
};

export const shouldRenderPreviewBoard = (
  presentationMode: 'single' | 'board',
  tabCount: number,
  hasArchiveTab = false
) => presentationMode === 'board' && (tabCount > 1 || hasArchiveTab);

const PreviewPanel: React.FC<{ conversationId?: string }> = ({ conversationId }) => {
  const { presentationMode, tabs } = usePreviewContext();
  const hasArchiveTab = tabs.some((tab) => tab.content_type === 'archive');
  return shouldRenderPreviewBoard(presentationMode, tabs.length, hasArchiveTab) ? (
    <PreviewBoard conversationId={conversationId} />
  ) : (
    <SinglePreviewPanel conversationId={conversationId} />
  );
};

export default PreviewPanel;

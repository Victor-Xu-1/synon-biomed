/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { ipcBridge } from '@/common';
import { insertArtifactReferenceIntoActiveComposer } from '@/renderer/components/chat/SendBox/composerReferenceBridge';
import { downloadFileFromPath, downloadFileFromUrl } from '@/renderer/utils/file/download';
import type { IDirOrFile } from '@/common/adapter/ipcBridge';
import type { PreviewContentType } from '@/common/types/office/preview';
import type { OpenPreviewOptions, PreviewMetadata } from '@/renderer/pages/conversation/Preview/context/PreviewContext';
import {
  buildBase64PreviewDataUrl,
  getContentTypeByExtension,
  getLocalBinaryPreviewMimeType,
} from '@/renderer/pages/conversation/Preview/fileUtils';
import { emitter } from '@/renderer/utils/emitter';
import {
  LARGE_TEXT_PREVIEW_MAX_LENGTH,
  LARGE_TEXT_PREVIEW_THRESHOLD,
} from '@/renderer/pages/conversation/Preview/constants';
import { classifyPreviewError, previewErrorToI18nKey } from '@/renderer/utils/previewError';
import { removeWorkspaceEntry, renameWorkspaceEntry } from '@/renderer/utils/file/workspaceFs';
import { useCallback } from 'react';
import type { MessageApi, RenameModalState, DeleteModalState } from '../types';
import type { FileOrFolderItem } from '@/renderer/utils/file/fileTypes';
import {
  isSynonBiomedArtifactPreviewEditable,
  resolveSynonBiomedArtifactPreviewPlan,
  SYNON_BIOMED_TEXT_ACCEPT_HEADER,
} from '@/renderer/services/synonBiomedArtifactPreview';
import { deleteSynonBiomedArtifact, renameSynonBiomedArtifact } from '@/renderer/services/synonBiomedWorkspaceApi';
import { createSynonBiomedCompanionArtifactUrls } from '@/renderer/services/synonBiomedArtifactReferences';

type RemoteArtifactPreviewMetadata = PreviewMetadata & {
  artifactId?: string;
  versionId?: string;
  contentUrl?: string;
};
interface UseWorkspaceFileOpsOptions {
  conversation_id?: string;
  workspace: string;
  eventPrefix: 'acp';
  messageApi: MessageApi;
  t: (key: string) => string;

  // Dependencies from useWorkspaceTree
  setSelected: React.Dispatch<React.SetStateAction<string[]>>;
  selectedKeysRef: React.MutableRefObject<string[]>;
  selectedNodeRef: React.MutableRefObject<{ relativePath: string; fullPath: string } | null>;
  ensureNodeSelected: (nodeData: IDirOrFile, options?: { emit?: boolean }) => void;
  refreshWorkspace: () => void;

  // Dependencies from useWorkspaceModals (will be created next)
  renameModal: RenameModalState;
  deleteModal: DeleteModalState;
  renameLoading: boolean;
  setRenameLoading: React.Dispatch<React.SetStateAction<boolean>>;
  closeRenameModal: () => void;
  closeDeleteModal: () => void;
  closeContextMenu: () => void;
  setRenameModal: React.Dispatch<React.SetStateAction<RenameModalState>>;
  setDeleteModal: React.Dispatch<React.SetStateAction<DeleteModalState>>;

  // Dependencies from preview context
  openPreview: (
    content: string,
    type: PreviewContentType,
    metadata?: PreviewMetadata,
    options?: OpenPreviewOptions
  ) => void;
}

/**
 * useWorkspaceFileOps - 文件操作逻辑（打开、删除、重命名、预览、添加到聊天）
 * File operations logic (open, delete, rename, preview, add to chat)
 */
export function useWorkspaceFileOps(params: UseWorkspaceFileOpsOptions) {
  const {
    conversation_id,
    workspace,
    eventPrefix,
    messageApi,
    t,
    setSelected,
    selectedKeysRef,
    selectedNodeRef,
    ensureNodeSelected,
    refreshWorkspace,
    renameModal,
    deleteModal,
    renameLoading,
    setRenameLoading,
    closeRenameModal,
    closeDeleteModal,
    closeContextMenu,
    setRenameModal,
    setDeleteModal,
    openPreview,
  } = params;

  /**
   * 打开文件或文件夹（使用系统默认程序）
   * Open file or folder with system default handler
   */
  const handleOpenNode = useCallback(
    async (nodeData: IDirOrFile | null) => {
      if (!nodeData) return;
      try {
        await ipcBridge.shell.openFile.invoke(nodeData.fullPath);
      } catch {
        messageApi.error(t('conversation.workspace.contextMenu.openFailed'));
      }
    },
    [messageApi, t]
  );

  /**
   * 在系统文件管理器中定位文件/文件夹
   * Reveal item in system file explorer
   */
  const handleRevealNode = useCallback(
    async (nodeData: IDirOrFile | null) => {
      if (!nodeData) return;
      try {
        await ipcBridge.shell.showItemInFolder.invoke(nodeData.fullPath);
      } catch {
        messageApi.error(t('conversation.workspace.contextMenu.revealFailed'));
      }
    },
    [messageApi, t]
  );

  /**
   * 显示删除确认弹窗
   * Show delete confirmation modal
   */
  const handleDeleteNode = useCallback(
    (nodeData: IDirOrFile | null, options?: { emit?: boolean }) => {
      if (!nodeData || !nodeData.relativePath) return;
      ensureNodeSelected(nodeData, { emit: Boolean(options?.emit) });
      closeContextMenu();
      setDeleteModal({ visible: true, target: nodeData, loading: false });
    },
    [closeContextMenu, ensureNodeSelected, setDeleteModal]
  );

  /**
   * 确认删除操作
   * Confirm delete operation
   */
  const handleDeleteConfirm = useCallback(async () => {
    if (!deleteModal.target) return;
    try {
      setDeleteModal((prev) => ({ ...prev, loading: true }));
      if (deleteModal.target.artifactId && conversation_id) {
        await deleteSynonBiomedArtifact({
          conversationId: conversation_id,
          artifactId: deleteModal.target.artifactId,
        });
      } else {
        await removeWorkspaceEntry(deleteModal.target.fullPath, workspace);
      }

      messageApi.success(t('conversation.workspace.contextMenu.deleteSuccess'));
      setSelected([]);
      selectedKeysRef.current = [];
      selectedNodeRef.current = null;
      emitter.emit(`${eventPrefix}.selected.file`, []);
      closeDeleteModal();
      setTimeout(() => refreshWorkspace(), 200);
    } catch {
      messageApi.error(t('conversation.workspace.contextMenu.deleteFailed'));
      setDeleteModal((prev) => ({ ...prev, loading: false }));
    }
  }, [
    deleteModal.target,
    closeDeleteModal,
    eventPrefix,
    messageApi,
    refreshWorkspace,
    t,
    setSelected,
    selectedKeysRef,
    selectedNodeRef,
    setDeleteModal,
    conversation_id,
    workspace,
  ]);

  /**
   * 超时包装器
   * Wrap promise with timeout guard
   */
  const waitWithTimeout = useCallback(<T>(promise: Promise<T>, timeoutMs = 8000) => {
    return new Promise<T>((resolve, reject) => {
      const timer = window.setTimeout(() => {
        reject(new Error('timeout'));
      }, timeoutMs);

      promise
        .then((value) => {
          window.clearTimeout(timer);
          resolve(value);
        })
        .catch((error) => {
          window.clearTimeout(timer);
          reject(error);
        });
    });
  }, []);

  /**
   * 确认重命名操作
   * Confirm rename operation
   */
  const handleRenameConfirm = useCallback(async () => {
    const target = renameModal.target;
    if (!target) return;
    if (renameLoading) return;
    const trimmedName = renameModal.value.trim();

    if (!trimmedName) {
      messageApi.warning(t('conversation.workspace.contextMenu.renameEmpty'));
      return;
    }

    if (trimmedName === target.name) {
      closeRenameModal();
      return;
    }

    try {
      setRenameLoading(true);
      await waitWithTimeout<unknown>(
        target.artifactId && conversation_id
          ? renameSynonBiomedArtifact({
              conversationId: conversation_id,
              artifactId: target.artifactId,
              filename: trimmedName,
            })
          : renameWorkspaceEntry(target.fullPath, trimmedName, workspace)
      );

      closeRenameModal();
      setSelected([]);
      selectedKeysRef.current = [];
      selectedNodeRef.current = null;
      emitter.emit(`${eventPrefix}.selected.file`, []);
      refreshWorkspace();
      messageApi.success(t('conversation.workspace.contextMenu.renameSuccess'));
    } catch (error) {
      if (error instanceof Error && error.message === 'timeout') {
        messageApi.error(t('conversation.workspace.contextMenu.renameTimeout'));
      } else {
        messageApi.error(t('conversation.workspace.contextMenu.renameFailed'));
      }
    } finally {
      setRenameLoading(false);
    }
  }, [
    closeRenameModal,
    eventPrefix,
    messageApi,
    renameLoading,
    renameModal,
    refreshWorkspace,
    t,
    waitWithTimeout,
    setSelected,
    selectedKeysRef,
    selectedNodeRef,
    setRenameLoading,
    conversation_id,
    workspace,
  ]);

  /**
   * 添加到聊天
   * Add to chat
   */
  const handleAddToChat = useCallback(
    (nodeData: IDirOrFile | null) => {
      if (!nodeData || !nodeData.fullPath) return;
      ensureNodeSelected(nodeData);
      closeContextMenu();

      if (nodeData.artifactId) {
        const inserted = insertArtifactReferenceIntoActiveComposer({
          filename: nodeData.name,
          artifactId: nodeData.artifactId,
          versionId: nodeData.versionId,
          conversationId: conversation_id,
        });
        if (!inserted) {
          messageApi.error(t('conversation.workspace.contextMenu.composerUnavailable'));
          return;
        }
        messageApi.success(t('conversation.workspace.contextMenu.addedToChat'));
        return;
      }

      const payload: FileOrFolderItem = {
        path: nodeData.fullPath,
        name: nodeData.name,
        isFile: Boolean(nodeData.isFile),
        relativePath: nodeData.relativePath || undefined,
      };

      emitter.emit(`${eventPrefix}.selected.file.append`, [payload]);
      messageApi.success(t('conversation.workspace.contextMenu.addedToChat'));
    },
    [closeContextMenu, conversation_id, ensureNodeSelected, eventPrefix, messageApi, t]
  );

  /**
   * 预览文件
   * Preview file
   */
  const handlePreviewFile = useCallback(
    async (nodeData: IDirOrFile | null, companionNodes: readonly IDirOrFile[] = []) => {
      if (!nodeData || !nodeData.isFile || (!nodeData.contentUrl && !nodeData.fullPath)) return;

      try {
        closeContextMenu();

        const ext = nodeData.name.toLowerCase().split('.').pop() || '';
        if (nodeData.contentUrl) {
          const plan = resolveSynonBiomedArtifactPreviewPlan({
            filename: nodeData.name,
            contentType: nodeData.contentType,
            previewKind: nodeData.previewKind,
          });
          let content = nodeData.contentUrl;
          let isLargeTextTruncated = false;
          if (plan.fetchText) {
            const response = await fetch(nodeData.contentUrl, {
              headers: { accept: SYNON_BIOMED_TEXT_ACCEPT_HEADER },
            });
            if (!response.ok) {
              throw new Error(`Artifact request failed: ${response.status}`);
            }
            content = await response.text();
            if (plan.type === 'code' && content.length > LARGE_TEXT_PREVIEW_THRESHOLD) {
              content = content.slice(0, LARGE_TEXT_PREVIEW_MAX_LENGTH);
              isLargeTextTruncated = true;
            }
          }
          const metadata: RemoteArtifactPreviewMetadata = {
            title: nodeData.name,
            file_name: nodeData.name,
            ...(nodeData.fullPath ? { file_path: nodeData.fullPath } : {}),
            workspace,
            // Structure operations use the same frame-scoped backend contract
            // as message artifacts. Workspace previews are opened from the
            // project file panel, so carry the active conversation frame
            // explicitly instead of relying on a later UI fallback.
            rootFrameId: conversation_id?.trim() || undefined,
            artifactId: nodeData.artifactId,
            versionId: nodeData.versionId,
            contentUrl: nodeData.contentUrl,
            companionArtifactUrls: createSynonBiomedCompanionArtifactUrls(companionNodes),
            language: plan.language ?? ext,
            truncated: isLargeTextTruncated,
            editable: isSynonBiomedArtifactPreviewEditable(plan) && !isLargeTextTruncated,
          };
          openPreview(content, plan.type, metadata, { presentation: 'board' });
          return;
        }

        let contentType: PreviewContentType = getContentTypeByExtension(nodeData.name);
        let content = '';
        let contentUrl: string | undefined;
        let isLargeTextTruncated = false;

        // 根据文件类型读取内容 / Read content based on file type
        if (
          contentType === 'pdf' ||
          contentType === 'word' ||
          contentType === 'excel' ||
          contentType === 'ppt' ||
          contentType === 'audio' ||
          contentType === 'video' ||
          contentType === 'unsupported'
        ) {
          content = '';
        } else if (getLocalBinaryPreviewMimeType(nodeData.name, contentType)) {
          const mimeType = getLocalBinaryPreviewMimeType(nodeData.name, contentType);
          if (!mimeType) throw null;
          const encoded = await ipcBridge.fs.readFileBuffer.invoke({ path: nodeData.fullPath, workspace });
          if (encoded == null) throw null;
          contentUrl = buildBase64PreviewDataUrl(encoded, mimeType);
        } else if (contentType === 'image') {
          // 图片: 读取为 Base64 格式 / Image: Read as Base64 format
          content = await ipcBridge.fs.getImageBase64.invoke({ path: nodeData.fullPath, workspace });
          if (content == null) {
            throw null;
          }
        } else {
          // 文本文件：使用 UTF-8 编码读取 / Text files: Read using UTF-8 encoding
          content = await ipcBridge.fs.readFile.invoke({ path: nodeData.fullPath, workspace });
          if (content == null) {
            throw null;
          }

          // 大文本仅保留前一段预览内容，避免切换/关闭 tab 时卡顿
          // Keep only first chunk for large text preview to reduce tab switch/close jank
          if (contentType === 'code' && content.length > LARGE_TEXT_PREVIEW_THRESHOLD) {
            content = content.slice(0, LARGE_TEXT_PREVIEW_MAX_LENGTH);
            isLargeTextTruncated = true;
          }
        }

        // 项目文件在同一画板中累积打开，便于并排比较和查阅多类科研产物。
        // Project files accumulate in one board for side-by-side inspection across artifact formats.
        openPreview(
          content,
          contentType,
          {
            title: nodeData.name,
            file_name: nodeData.name,
            file_path: nodeData.fullPath,
            ...(contentUrl ? { contentUrl } : {}),
            workspace: workspace,
            // Local workspace structures still belong to the active
            // conversation and must be able to call frame-scoped actions.
            rootFrameId: conversation_id?.trim() || undefined,
            language: ext,
            truncated: isLargeTextTruncated,
            // Markdown 和图片文件默认为只读模式
            // Binary and truncated previews stay read-only; complete text files remain editable.
            editable: contentType === 'image' || isLargeTextTruncated ? false : undefined,
          },
          { presentation: 'board' }
        );
      } catch (error) {
        const kind = classifyPreviewError(error);
        messageApi.error(t(previewErrorToI18nKey(kind)));
      }
    },
    [closeContextMenu, conversation_id, openPreview, workspace, messageApi, t]
  );

  /**
   * 打开重命名弹窗
   * Open rename modal
   */
  const openRenameModal = useCallback(
    (nodeData: IDirOrFile | null) => {
      if (!nodeData) return;
      ensureNodeSelected(nodeData);
      closeContextMenu();
      setRenameModal({ visible: true, value: nodeData.name, target: nodeData });
    },
    [closeContextMenu, ensureNodeSelected, setRenameModal]
  );

  /**
   * 下载文件到本地（直接从磁盘读取二进制，不经过预览）
   * Download file to local system (read binary directly from disk, bypassing preview)
   */
  const handleDownloadFile = useCallback(
    async (nodeData: IDirOrFile | null) => {
      if (!nodeData || !nodeData.isFile || !nodeData.fullPath) return;
      closeContextMenu();

      try {
        if (nodeData.contentUrl) {
          await downloadFileFromUrl(nodeData.contentUrl, nodeData.name);
        } else {
          await downloadFileFromPath(nodeData.fullPath, nodeData.name, workspace);
        }
        messageApi.success(t('conversation.workspace.contextMenu.downloadSuccess'));
      } catch {
        messageApi.error(t('conversation.workspace.contextMenu.downloadFailed'));
      }
    },
    [closeContextMenu, messageApi, t]
  );

  return {
    handleOpenNode,
    handleRevealNode,
    handleDeleteNode,
    handleDeleteConfirm,
    handleRenameConfirm,
    handleAddToChat,
    handlePreviewFile,
    openRenameModal,
    handleDownloadFile,
  };
}

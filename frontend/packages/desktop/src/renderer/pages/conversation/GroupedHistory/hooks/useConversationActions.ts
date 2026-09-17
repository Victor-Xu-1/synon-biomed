/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { TChatConversation } from '@/common/config/storage';
import type { SynonBiomedExecutionRecord } from '@/renderer/components/synonBiomed/runtime/runtimeOperationsModel';
import type { SynonBiomedProject } from '@/renderer/services/synonBiomedGateway';
import type { SynonBiomedSessionUpdate } from '@/renderer/services/synonBiomedSessionActions';
import { emitter } from '@/renderer/utils/emitter';
import { blockMobileInputFocus, blurActiveElement } from '@/renderer/utils/ui/focus';
import { Message, Modal } from '@arco-design/web-react';
import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate, useParams } from 'react-router';
import { redactErrorText } from '@/renderer/pages/conversation/platforms/acp/errorDiagnostics';
import { useAuth } from '@/renderer/hooks/context/AuthContext';
import { prefetchConversationRoute, warmConversationNavigation } from '@/renderer/pages/conversation/conversationRoute';
import { beginConversationRoutePerformance } from '@/renderer/pages/conversation/conversationRoutePerformance';

type UseConversationActionsParams = {
  batchMode: boolean;
  onSessionClick?: () => void;
  onBatchModeChange?: (value: boolean) => void;
  selectedConversationIds: Set<string>;
  setSelectedConversationIds: React.Dispatch<React.SetStateAction<Set<string>>>;
  toggleSelectedConversation: (conversation: TChatConversation) => void;
};

type ConversationActionRuntime = typeof import('../conversationActionRuntime');

let conversationActionRuntimePromise: Promise<ConversationActionRuntime> | null = null;

const loadConversationActionRuntime = (): Promise<ConversationActionRuntime> => {
  conversationActionRuntimePromise ??= import('../conversationActionRuntime');
  return conversationActionRuntimePromise;
};

const conversationProjectId = (conversation: TChatConversation | null): string | null => {
  if (!conversation) return null;
  const value = (conversation.extra as unknown as Record<string, unknown>).project_id;
  return typeof value === 'string' && value ? value : null;
};

export const useConversationActions = ({
  batchMode,
  onSessionClick,
  onBatchModeChange,
  selectedConversationIds,
  setSelectedConversationIds,
  toggleSelectedConversation,
}: UseConversationActionsParams) => {
  const [editorTarget, setEditorTarget] = useState<TChatConversation | null>(null);
  const [editorLoading, setEditorLoading] = useState(false);
  const [editorError, setEditorError] = useState<string | null>(null);
  const [moveTarget, setMoveTarget] = useState<TChatConversation | null>(null);
  const [moveProjects, setMoveProjects] = useState<SynonBiomedProject[]>([]);
  const [moveLoading, setMoveLoading] = useState(false);
  const [moveError, setMoveError] = useState<string | null>(null);
  const [notebookTarget, setNotebookTarget] = useState<TChatConversation | null>(null);
  const [notebookRecords, setNotebookRecords] = useState<SynonBiomedExecutionRecord[]>([]);
  const [notebookLoading, setNotebookLoading] = useState(false);
  const [notebookError, setNotebookError] = useState<string | null>(null);
  const [dropdownVisibleId, setDropdownVisibleId] = useState<string | null>(null);
  const { id } = useParams();
  const { user } = useAuth();
  const { t } = useTranslation();
  const navigate = useNavigate();

  useEffect(() => {
    if (batchMode) setDropdownVisibleId(null);
  }, [batchMode]);

  const refreshHistory = useCallback(() => {
    emitter.emit('chat.history.refresh');
  }, []);

  const handleConversationPrefetch = useCallback(
    (_conversation: TChatConversation) => {
      if (batchMode || !user?.id) return;
      void prefetchConversationRoute().catch((): undefined => undefined);
    },
    [batchMode, user?.id]
  );

  const prepareConversationNavigation = useCallback(
    async (conversationId: string, summary?: TChatConversation): Promise<void> => {
      if (!user?.id) return;
      await warmConversationNavigation({
        ownerId: user.id,
        conversationId,
        ...(summary ? { summary } : {}),
      });
    },
    [user?.id]
  );

  const handleConversationClick = useCallback(
    (conversation: TChatConversation, allowBatchSelection = true) => {
      setDropdownVisibleId(null);
      if (batchMode && allowBatchSelection) {
        toggleSelectedConversation(conversation);
        return;
      }
      if (id === conversation.id) {
        onSessionClick?.();
        return;
      }
      blockMobileInputFocus();
      blurActiveElement();
      beginConversationRoutePerformance();
      void prepareConversationNavigation(conversation.id, conversation).catch((): undefined => undefined);
      void navigate(`/conversation/${conversation.id}`);
      onSessionClick?.();
    },
    [batchMode, id, navigate, onSessionClick, prepareConversationNavigation, toggleSelectedConversation]
  );

  const removeConversation = useCallback(
    async (conversationId: string) => {
      const { deleteSynonBiomedSession } = await loadConversationActionRuntime();
      await deleteSynonBiomedSession(conversationId);
      emitter.emit('conversation.deleted', conversationId);
      emitter.emit('synonbiomed.projects.refresh');
      if (id === conversationId) void navigate('/guid', { replace: true });
      return true;
    },
    [id, navigate]
  );

  const handleDeleteClick = useCallback(
    (conversationId: string) => {
      setDropdownVisibleId(null);
      Modal.confirm({
        title: t('conversation.history.deleteTitle'),
        icon: null,
        content: t('conversation.history.deleteConfirm'),
        okText: t('conversation.history.confirmDelete'),
        cancelText: t('conversation.history.cancelDelete'),
        okButtonProps: { status: 'warning' },
        onOk: async () => {
          try {
            await removeConversation(conversationId);
            refreshHistory();
            Message.success(t('conversation.history.deleteSuccess'));
          } catch (error) {
            console.error('Failed to remove Synon Biomed session:', diagnostic(error));
            Message.error(t('conversation.history.deleteFailed'));
            throw error;
          }
        },
        style: { borderRadius: '8px' },
        alignCenter: true,
        getPopupContainer: () => document.body,
      });
    },
    [refreshHistory, removeConversation, t]
  );

  const handleBatchDelete = useCallback(() => {
    if (selectedConversationIds.size === 0) {
      Message.warning(t('conversation.history.batchNoSelection'));
      return;
    }
    Modal.confirm({
      title: t('conversation.history.batchDelete'),
      icon: null,
      content: t('conversation.history.batchDeleteConfirm', {
        count: selectedConversationIds.size,
      }),
      okText: t('conversation.history.confirmDelete'),
      cancelText: t('conversation.history.cancelDelete'),
      okButtonProps: { status: 'warning' },
      onOk: async () => {
        const selectedIds = Array.from(selectedConversationIds);
        const results = await Promise.allSettled(selectedIds.map(removeConversation));
        const failedIds = selectedIds.filter((_conversationId, index) => results[index]?.status === 'rejected');
        const deletedCount = selectedIds.length - failedIds.length;

        if (deletedCount > 0) {
          refreshHistory();
        }
        setSelectedConversationIds(new Set(failedIds));

        if (failedIds.length > 0) {
          console.error('Failed to delete some Synon Biomed sessions:', failedIds.length);
          Message.error(t('conversation.history.deleteFailed'));
          throw new Error(`Failed to delete ${failedIds.length} Synon Biomed session(s)`);
        }

        Message.success(t('conversation.history.batchDeleteSuccess', { count: deletedCount }));
        onBatchModeChange?.(false);
      },
      style: { borderRadius: '8px' },
      alignCenter: true,
      getPopupContainer: () => document.body,
    });
  }, [onBatchModeChange, refreshHistory, removeConversation, selectedConversationIds, setSelectedConversationIds, t]);

  const handleEditStart = useCallback((conversation: TChatConversation) => {
    setDropdownVisibleId(null);
    setEditorError(null);
    setEditorTarget(conversation);
  }, []);

  const handleEditorSubmit = useCallback(
    async (update: SynonBiomedSessionUpdate) => {
      if (!editorTarget || editorLoading) return;
      setEditorLoading(true);
      setEditorError(null);
      try {
        const { updateSynonBiomedSession } = await loadConversationActionRuntime();
        await updateSynonBiomedSession(editorTarget.id, update);
        refreshHistory();
        setEditorTarget(null);
        Message.success(t('conversation.history.renameSuccess'));
      } catch (error) {
        console.warn('[ConversationActions] Failed to rename session:', diagnostic(error));
        setEditorError(t('conversation.history.renameFailed'));
        Message.error(t('conversation.history.renameFailed'));
      } finally {
        setEditorLoading(false);
      }
    },
    [editorLoading, editorTarget, refreshHistory, t]
  );

  const handleEditorCancel = useCallback(() => {
    if (!editorLoading) setEditorTarget(null);
  }, [editorLoading]);

  const handleMoveStart = useCallback(
    async (conversation: TChatConversation) => {
      setDropdownVisibleId(null);
      setMoveTarget(conversation);
      setMoveProjects([]);
      setMoveError(null);
      setMoveLoading(true);
      try {
        const { loadSynonBiomedProjects } = await loadConversationActionRuntime();
        setMoveProjects(await loadSynonBiomedProjects());
      } catch (error) {
        console.warn('[ConversationActions] Failed to load projects for move:', diagnostic(error));
        setMoveError(t('conversation.history.sessionMove.loadFailed'));
      } finally {
        setMoveLoading(false);
      }
    },
    [t]
  );

  const handleMoveConfirm = useCallback(
    async (projectId: string) => {
      if (!moveTarget || moveLoading) return;
      setMoveLoading(true);
      setMoveError(null);
      try {
        const { moveSynonBiomedSession } = await loadConversationActionRuntime();
        await moveSynonBiomedSession(moveTarget.id, projectId);
        refreshHistory();
        emitter.emit('synonbiomed.projects.refresh');
        setMoveTarget(null);
        Message.success(t('conversation.history.sessionMove.success'));
      } catch (error) {
        console.warn('[ConversationActions] Failed to move session:', diagnostic(error));
        setMoveError(t('conversation.history.sessionMove.failed'));
      } finally {
        setMoveLoading(false);
      }
    },
    [moveLoading, moveTarget, refreshHistory, t]
  );

  const handleMoveCancel = useCallback(() => {
    if (!moveLoading) setMoveTarget(null);
  }, [moveLoading]);

  const handleExport = useCallback(
    async (conversation: TChatConversation) => {
      setDropdownVisibleId(null);
      try {
        const { downloadFileFromUrl, getSynonBiomedSessionExportUrl, sanitizeFileName } =
          await loadConversationActionRuntime();
        await downloadFileFromUrl(
          getSynonBiomedSessionExportUrl(conversation.id),
          `${sanitizeFileName(conversation.name || conversation.id)}.synon-session.json`
        );
      } catch (error) {
        console.error('Failed to export Synon Biomed session:', diagnostic(error));
        Message.error(t('conversation.history.exportFailed'));
      }
    },
    [t]
  );

  const handleDownloadArtifacts = useCallback(
    async (conversation: TChatConversation) => {
      setDropdownVisibleId(null);
      try {
        const { downloadFileFromUrl, getSynonBiomedSessionArtifactsDownloadUrl, sanitizeFileName } =
          await loadConversationActionRuntime();
        await downloadFileFromUrl(
          getSynonBiomedSessionArtifactsDownloadUrl(conversation.id),
          `${sanitizeFileName(conversation.name || conversation.id)}-artifacts.zip`
        );
      } catch (error) {
        console.error('Failed to download Synon Biomed session artifacts:', diagnostic(error));
        Message.error(t('conversation.history.downloadArtifactsFailed'));
      }
    },
    [t]
  );

  const handleViewNotebook = useCallback(
    async (conversation: TChatConversation) => {
      setDropdownVisibleId(null);
      setNotebookTarget(conversation);
      setNotebookRecords([]);
      setNotebookError(null);
      setNotebookLoading(true);
      try {
        const { loadSynonBiomedExecutionLogPage } = await loadConversationActionRuntime();
        setNotebookRecords((await loadSynonBiomedExecutionLogPage(conversation.id)).records);
      } catch (error) {
        console.warn('[ConversationActions] Failed to load session notebook:', diagnostic(error));
        setNotebookError(t('conversation.history.sessionNotebook.loadFailed'));
      } finally {
        setNotebookLoading(false);
      }
    },
    [t]
  );

  const handleNotebookClose = useCallback(() => {
    setNotebookTarget(null);
    setNotebookRecords([]);
    setNotebookError(null);
  }, []);

  const handleMenuVisibleChange = useCallback((conversationId: string, visible: boolean) => {
    setDropdownVisibleId(visible ? conversationId : null);
  }, []);

  const handleOpenMenu = useCallback((conversation: TChatConversation) => {
    setDropdownVisibleId(conversation.id);
  }, []);

  return {
    editorTarget,
    editorLoading,
    editorError,
    handleEditStart,
    handleEditorSubmit,
    handleEditorCancel,
    moveTarget,
    moveProjects,
    moveCurrentProjectId: conversationProjectId(moveTarget),
    moveLoading,
    moveError,
    handleMoveStart,
    handleMoveConfirm,
    handleMoveCancel,
    notebookTarget,
    notebookRecords,
    notebookLoading,
    notebookError,
    handleViewNotebook,
    handleNotebookClose,
    dropdownVisibleId,
    handleConversationPrefetch,
    prepareConversationNavigation,
    handleConversationClick,
    handleDeleteClick,
    handleBatchDelete,
    handleExport,
    handleDownloadArtifacts,
    handleMenuVisibleChange,
    handleOpenMenu,
  };
};

const diagnostic = (error: unknown): string =>
  redactErrorText(error instanceof Error ? error.message : String(error || 'unknown error'));

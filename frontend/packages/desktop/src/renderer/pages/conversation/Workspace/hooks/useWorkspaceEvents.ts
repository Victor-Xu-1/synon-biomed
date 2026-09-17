/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { ipcBridge } from '@/common';
import type { IDirOrFile } from '@/common/adapter/ipcBridge';
import { emitter, useAddEventListener } from '@/renderer/utils/emitter';
import { useCallback, useEffect, useRef } from 'react';
import type { ContextMenuState } from '../types';

interface UseWorkspaceEventsOptions {
  conversation_id: string;
  eventPrefix: 'acp';

  // Dependencies from useWorkspaceTree
  ensureWorkspace: () => Promise<unknown>;
  refreshWorkspace: () => void;
  clearSelection: () => void;
  setFiles: React.Dispatch<React.SetStateAction<IDirOrFile[]>>;
  setSelected: React.Dispatch<React.SetStateAction<string[]>>;
  setExpandedKeys: React.Dispatch<React.SetStateAction<string[]>>;
  setTreeKey: React.Dispatch<React.SetStateAction<number>>;
  selectedNodeRef: React.MutableRefObject<{
    relativePath: string;
    fullPath: string;
  } | null>;
  selectedKeysRef: React.MutableRefObject<string[]>;

  // Dependencies from useWorkspaceModals
  closeContextMenu: () => void;
  setContextMenu: React.Dispatch<React.SetStateAction<ContextMenuState>>;
  closeRenameModal: () => void;
  closeDeleteModal: () => void;
}

/**
 * useWorkspaceEvents - 管理所有事件监听器
 * Manage all event listeners
 */
export function useWorkspaceEvents(options: UseWorkspaceEventsOptions) {
  const {
    conversation_id,
    eventPrefix,
    ensureWorkspace,
    refreshWorkspace,
    clearSelection,
    setFiles,
    setSelected,
    selectedNodeRef,
    selectedKeysRef,
    closeContextMenu,
    setContextMenu,
    closeRenameModal,
    closeDeleteModal,
  } = options;

  /**
   * 监听对话切换事件 - 重置所有状态
   * Listen to conversation switch event - reset conversation-local UI state.
   * Project artifacts are owner/project scoped and are loaded by
   * useWorkspaceTree. Refreshing them here would turn every conversation
   * switch into a forced network round trip even when the project is unchanged.
   */
  useEffect(() => {
    setSelected([]);
    selectedNodeRef.current = null;
    selectedKeysRef.current = [];
    setContextMenu({ visible: false, x: 0, y: 0, node: null });
    closeRenameModal();
    closeDeleteModal();
    void ensureWorkspace();
    emitter.emit(`${eventPrefix}.selected.file`, []);
  }, [
    conversation_id,
    eventPrefix,
    ensureWorkspace,
    setSelected,
    selectedNodeRef,
    selectedKeysRef,
    setContextMenu,
    closeRenameModal,
    closeDeleteModal,
  ]);

  /**
   * 节流的刷新函数 - 避免 Agent 连续 tool_call 导致工作空间反复刷新
   * Throttled refresh - prevent rapid workspace refreshes during agent tool calls
   */
  const throttleTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const pendingRef = useRef(false);
  const refreshGenerationRef = useRef(0);
  const terminalSeenRef = useRef(false);
  const terminalResetTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    refreshGenerationRef.current += 1;
    pendingRef.current = false;
    terminalSeenRef.current = false;
    if (throttleTimerRef.current) {
      clearTimeout(throttleTimerRef.current);
      throttleTimerRef.current = null;
    }
    if (terminalResetTimerRef.current) {
      clearTimeout(terminalResetTimerRef.current);
      terminalResetTimerRef.current = null;
    }
    return () => {
      refreshGenerationRef.current += 1;
      pendingRef.current = false;
      terminalSeenRef.current = false;
      if (throttleTimerRef.current) {
        clearTimeout(throttleTimerRef.current);
        throttleTimerRef.current = null;
      }
      if (terminalResetTimerRef.current) {
        clearTimeout(terminalResetTimerRef.current);
        terminalResetTimerRef.current = null;
      }
    };
  }, [conversation_id, refreshWorkspace]);

  const throttledRefresh = useCallback(() => {
    if (throttleTimerRef.current) {
      pendingRef.current = true; // Mark pending so trailing refresh fires after window
      return;
    }
    const generation = refreshGenerationRef.current;
    refreshWorkspace();
    throttleTimerRef.current = setTimeout(() => {
      if (refreshGenerationRef.current !== generation) return;
      throttleTimerRef.current = null;
      if (pendingRef.current) {
        pendingRef.current = false;
        refreshWorkspace(); // Fire trailing refresh for any calls missed during throttle window
      }
    }, 2000);
  }, [refreshWorkspace]);

  const refreshAtTerminalBoundary = useCallback(() => {
    if (terminalSeenRef.current) return;
    terminalSeenRef.current = true;
    throttledRefresh();
    if (terminalResetTimerRef.current) clearTimeout(terminalResetTimerRef.current);
    const generation = refreshGenerationRef.current;
    terminalResetTimerRef.current = setTimeout(() => {
      if (refreshGenerationRef.current !== generation) return;
      terminalSeenRef.current = false;
      terminalResetTimerRef.current = null;
    }, 2000);
  }, [throttledRefresh]);

  /**
   * 监听 Agent 响应流 - 自动刷新工作空间（节流）
   * Listen to agent response stream - auto refresh workspace (throttled)
   */
  useEffect(() => {
    const handleResponse = (data: { type: string; data?: unknown; conversation_id?: string }) => {
      if (data.conversation_id && data.conversation_id !== conversation_id) return;

      // A new response means a later turn may legitimately reach another
      // terminal boundary before the dedupe window elapses.
      terminalSeenRef.current = false;

      if (data.type === 'acp_tool_call') {
        const acpData = data.data as { update?: { kind?: string; status?: string } } | undefined;
        const kind = acpData?.update?.kind;
        const status = acpData?.update?.status;
        const shouldRefresh = kind === 'edit' || kind === 'execute' || (status === 'completed' && kind !== 'read');
        if (shouldRefresh) throttledRefresh();
      }
      if (data.type === 'tool_call') {
        const toolData = data.data as { status?: string } | undefined;
        if (toolData?.status === 'completed') throttledRefresh();
      }
    };
    const unsubscribe = ipcBridge.acpConversation.responseStream.on(handleResponse);

    return () => {
      unsubscribe();
    };
  }, [conversation_id, eventPrefix, throttledRefresh]);

  // Both signals are durable terminal authorities. runtime.statusChanged also
  // covers explicit cancellation paths that do not emit turn.completed. They
  // share one deduped invalidation boundary so a dual publication refreshes
  // the artifact tree exactly once.
  useEffect(() => {
    const disposeTurn = ipcBridge.conversation.turnCompleted.on((event) => {
      if (event.session_id === conversation_id) refreshAtTerminalBoundary();
    });
    const disposeRuntime = ipcBridge.runtime.statusChanged.on((event) => {
      if (event.scope.kind !== 'conversation' || event.scope.id !== conversation_id) return;
      const terminal =
        event.terminal_status === 'completed' ||
        event.terminal_status === 'failed' ||
        event.terminal_status === 'cancelled';
      if (terminal) {
        refreshAtTerminalBoundary();
      } else {
        terminalSeenRef.current = false;
      }
    });
    return () => {
      disposeRuntime();
      disposeTurn();
    };
  }, [conversation_id, refreshAtTerminalBoundary]);

  /**
   * 监听手动刷新工作空间事件
   * Listen to manual refresh workspace event
   */
  useAddEventListener(`${eventPrefix}.workspace.refresh`, () => refreshWorkspace(), [refreshWorkspace]);

  /**
   * 监听清空选中文件事件（发送消息后）
   * Listen to clear selected files event (after sending message)
   */
  useAddEventListener(`${eventPrefix}.selected.file.clear`, () => clearSelection(), [clearSelection]);

  /**
   * 监听选中文件变化事件（sendbox 中关闭标签时同步状态）(#1083)
   * Listen to selected files change event (sync state when closing tags in sendbox)
   */
  useAddEventListener(
    `${eventPrefix}.selected.file`,
    (
      items: Array<{
        path: string;
        name: string;
        isFile: boolean;
        relativePath?: string;
      }>
    ) => {
      // Extract relative paths from items, filter out files (only keep folders in tree selection)
      // 从 items 中提取相对路径，过滤掉文件（树选中状态只保留文件夹）
      const newKeys = items.filter((item) => !item.isFile && item.relativePath).map((item) => item.relativePath!);
      setSelected(newKeys);
      selectedKeysRef.current = newKeys;

      // Update selectedNodeRef based on items
      // 根据 items 更新 selectedNodeRef
      const folders = items.filter((item) => !item.isFile);
      if (folders.length > 0) {
        const lastFolder = folders[folders.length - 1];
        selectedNodeRef.current = lastFolder.relativePath
          ? {
              relativePath: lastFolder.relativePath,
              fullPath: lastFolder.path,
            }
          : null;
      } else {
        selectedNodeRef.current = null;
      }
    },
    [setSelected, selectedKeysRef, selectedNodeRef]
  );

  /**
   * 监听搜索工作空间响应
   * Listen to search workspace response
   */
  useEffect(() => {
    return ipcBridge.conversation.responseSearchWorkSpace.provider((data) => {
      if (data.match) setFiles([data.match]);
      return Promise.resolve();
    });
  }, [setFiles]);

  /**
   * 监听右键菜单外部点击 - 关闭菜单
   * Listen to clicks outside context menu - close menu
   */
  useEffect(() => {
    const handleClose = () => {
      closeContextMenu();
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        closeContextMenu();
      }
    };
    window.addEventListener('click', handleClose);
    window.addEventListener('scroll', handleClose, true);
    window.addEventListener('keydown', handleKeyDown);
    return () => {
      window.removeEventListener('click', handleClose);
      window.removeEventListener('scroll', handleClose, true);
      window.removeEventListener('keydown', handleKeyDown);
    };
  }, [closeContextMenu]);
}

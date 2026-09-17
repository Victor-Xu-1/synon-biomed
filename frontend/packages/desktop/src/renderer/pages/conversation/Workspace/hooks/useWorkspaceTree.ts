/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { ipcBridge } from '@/common';
import type { IDirOrFile } from '@/common/adapter/ipcBridge';
import { isBackendHttpError } from '@/common/adapter/httpBridge';
import { emitter } from '@/renderer/utils/emitter';
import { dispatchWorkspaceHasFilesEvent } from '@/renderer/utils/workspace/workspaceEvents';
import { useAuth } from '@/renderer/hooks/context/AuthContext';
import { createKeyedSnapshotStore } from '@/renderer/services/keyedSnapshotStore';
import type { WorkspaceArtifactPage } from '@/common/adapter/workspaceMapper';
import { useCallback, useEffect, useRef, useState } from 'react';
import type { SelectedNodeRef } from '../types';
import { getFirstLevelKeys, mergeLoadedChildren } from '../utils/treeHelpers';

interface UseWorkspaceTreeOptions {
  workspace: string;
  conversation_id: string;
  projectId?: string;
  eventPrefix: 'acp';
}

const workspaceSnapshotStore = createKeyedSnapshotStore<IDirOrFile[]>({ maxEntries: 24 });
const projectArtifactPageStore = createKeyedSnapshotStore<WorkspaceArtifactPage>({ maxEntries: 48 });
const PROJECT_ARTIFACT_PAGE_LIMIT = 24;

type ProjectArtifactPageState = {
  query: string;
  total: number;
  hasMore: boolean;
  nextCursor?: string;
  loadingMore: boolean;
  loadMoreError: boolean;
};

const EMPTY_PROJECT_ARTIFACT_PAGE_STATE: ProjectArtifactPageState = {
  query: '',
  total: 0,
  hasMore: false,
  loadingMore: false,
  loadMoreError: false,
};

function workspaceSnapshotKey(ownerId: string, authorityId: string, requestIdentity: string, search = ''): string {
  return JSON.stringify([ownerId.trim() || '__pending__', authorityId, requestIdentity, search]);
}

function projectArtifactPageKey(
  ownerId: string,
  projectId: string,
  conversationId: string,
  query: string,
  cursor?: string
): string {
  return JSON.stringify([ownerId.trim() || '__pending__', projectId, conversationId, query, cursor ?? '__first__']);
}

function mergeProjectArtifactPages(current: IDirOrFile[], incoming: IDirOrFile[]): IDirOrFile[] {
  const currentRoot = current[0];
  const incomingRoot = incoming[0];
  if (!currentRoot || !incomingRoot || currentRoot.isFile || incomingRoot.isFile) return incoming;
  const mergedChildren: IDirOrFile[] = [];
  const seen = new Set<string>();
  for (const child of [...(currentRoot.children ?? []), ...(incomingRoot.children ?? [])]) {
    const key = `${child.artifactId ?? ''}\0${child.versionId ?? ''}\0${child.relativePath}`;
    if (seen.has(key)) continue;
    seen.add(key);
    mergedChildren.push(child);
  }
  return [{ ...incomingRoot, children: mergedChildren }];
}

function isAbortError(error: unknown): boolean {
  return Boolean(error && typeof error === 'object' && 'name' in error && error.name === 'AbortError');
}

/**
 * useWorkspaceTree - 合并树状态管理和选择逻辑
 * Merge tree state management and selection logic
 */
export function useWorkspaceTree({ workspace, conversation_id, projectId, eventPrefix }: UseWorkspaceTreeOptions) {
  const { user } = useAuth();
  const ownerId = user?.id?.trim() ?? '';
  const isSynonBiomedWorkspace = workspace.startsWith('synonbiomed://');
  const canonicalProjectId = projectId?.trim() ?? '';
  const canLoadWorkspace = !isSynonBiomedWorkspace || (ownerId !== '' && canonicalProjectId !== '');
  // Synon Biomed's project-files endpoint is filtered by the active
  // conversation on the backend. Keep both the snapshot and page caches
  // conversation-scoped so switching conversations never flashes another
  // conversation's files while the new page is loading.
  const workspaceAuthorityId = isSynonBiomedWorkspace
    ? `conversation:${conversation_id}:project:${canonicalProjectId}`
    : `conversation:${conversation_id}:workspace:${workspace}`;
  const rootRequestPath = isSynonBiomedWorkspace ? `${workspace}/project-files` : workspace;
  const rootRequestIdentity = isSynonBiomedWorkspace ? 'project-files' : rootRequestPath;
  const rootSnapshotKey = canLoadWorkspace
    ? workspaceSnapshotKey(ownerId, workspaceAuthorityId, rootRequestIdentity)
    : '';
  const initialSnapshot = rootSnapshotKey ? workspaceSnapshotStore.read(rootSnapshotKey) : undefined;
  const initialProjectPageKey =
    isSynonBiomedWorkspace && canLoadWorkspace
      ? projectArtifactPageKey(ownerId, canonicalProjectId, conversation_id, '')
      : '';
  const initialProjectPage = initialProjectPageKey ? projectArtifactPageStore.read(initialProjectPageKey) : undefined;
  // Tree state / 树状态
  const [files, setFiles] = useState<IDirOrFile[]>(initialProjectPage?.value.items ?? initialSnapshot?.value ?? []);
  const [loading, setLoading] = useState(false);
  const [projectArtifactPage, setProjectArtifactPage] = useState<ProjectArtifactPageState>(() =>
    initialProjectPage
      ? {
          query: '',
          total: initialProjectPage.value.total,
          hasMore: initialProjectPage.value.hasMore,
          ...(initialProjectPage.value.nextCursor ? { nextCursor: initialProjectPage.value.nextCursor } : {}),
          loadingMore: false,
          loadMoreError: false,
        }
      : EMPTY_PROJECT_ARTIFACT_PAGE_STATE
  );
  const [treeKey, setTreeKey] = useState(Math.random());
  const [expandedKeys, setExpandedKeys] = useState<string[]>([]);

  // Selection state / 选中状态
  const [selected, setSelected] = useState<string[]>([]);

  // 标记是否为首次加载（用于区分初始化和后续刷新）
  // Track if this is the first load (to distinguish initialization from subsequent refreshes)
  const isFirstLoadRef = useRef(!initialSnapshot);
  const selectedKeysRef = useRef<string[]>([]);
  const selectedNodeRef = useRef<SelectedNodeRef | null>(null);
  const loadSeqRef = useRef(0);
  const activeLoadKeysRef = useRef(new Set<string>());

  useEffect(() => {
    loadSeqRef.current += 1;
    const snapshot = rootSnapshotKey ? workspaceSnapshotStore.read(rootSnapshotKey) : undefined;
    const projectPage = initialProjectPageKey ? projectArtifactPageStore.read(initialProjectPageKey) : undefined;
    const initialFiles = projectPage?.value.items ?? snapshot?.value ?? [];
    isFirstLoadRef.current = initialFiles.length === 0;
    setFiles(initialFiles);
    setProjectArtifactPage(
      projectPage
        ? {
            query: '',
            total: projectPage.value.total,
            hasMore: projectPage.value.hasMore,
            ...(projectPage.value.nextCursor ? { nextCursor: projectPage.value.nextCursor } : {}),
            loadingMore: false,
            loadMoreError: false,
          }
        : EMPTY_PROJECT_ARTIFACT_PAGE_STATE
    );
    setExpandedKeys(initialFiles.length ? getFirstLevelKeys(initialFiles) : []);
    return () => {
      loadSeqRef.current += 1;
      for (const key of activeLoadKeysRef.current) workspaceSnapshotStore.cancel(key);
      activeLoadKeysRef.current.clear();
    };
  }, [initialProjectPageKey, isSynonBiomedWorkspace, rootSnapshotKey]);

  // Loading time tracker / 加载时间追踪
  const lastLoadingTime = useRef(Date.now());

  /**
   * 设置 loading 状态（带防抖，避免图标闪烁）
   * Set loading state with debounce to avoid icon flickering
   */
  const setLoadingHandler = useCallback((newState: boolean) => {
    if (newState) {
      lastLoadingTime.current = Date.now();
      setLoading(true);
    } else {
      // 确保loading动画保持至少1秒 / Ensure loading animation lasts at least 1 second
      if (Date.now() - lastLoadingTime.current > 0) {
        setLoading(false);
      } else {
        setTimeout(() => {
          setLoading(false);
        }, 0);
      }
    }
  }, []);

  const requestProjectArtifactPage = useCallback(
    (query: string, cursor: string | undefined, mode: 'load' | 'ensure' | 'force'): Promise<IDirOrFile[]> => {
      if (!canLoadWorkspace || !rootSnapshotKey || !isSynonBiomedWorkspace) {
        return Promise.resolve([] as IDirOrFile[]);
      }
      const normalizedQuery = query.trim();
      const seq = ++loadSeqRef.current;
      const requestKey = projectArtifactPageKey(ownerId, canonicalProjectId, conversation_id, normalizedQuery, cursor);
      const cached = projectArtifactPageStore.read(requestKey);
      const append = Boolean(cursor);
      const showLoading = !append && (mode !== 'ensure' || !cached);
      if (append) {
        setProjectArtifactPage((current) => ({ ...current, loadingMore: true, loadMoreError: false }));
      } else if (showLoading) {
        setLoadingHandler(true);
      }
      const loader = (signal: AbortSignal) =>
        ipcBridge.conversation.getWorkspacePage.invoke(
          {
            path: rootRequestPath,
            workspace,
            conversation_id,
            search: normalizedQuery,
            limit: PROJECT_ARTIFACT_PAGE_LIMIT,
            ...(cursor ? { cursor } : {}),
          },
          signal
        );
      const request =
        mode === 'ensure'
          ? projectArtifactPageStore.ensure(requestKey, loader)
          : projectArtifactPageStore.load(requestKey, loader, mode === 'force');
      return request
        .then((page) => {
          if (seq !== loadSeqRef.current) return page.items;
          setFiles((current) => (append ? mergeProjectArtifactPages(current, page.items) : page.items));
          setProjectArtifactPage({
            query: normalizedQuery,
            total: page.total,
            hasMore: page.hasMore,
            ...(page.nextCursor ? { nextCursor: page.nextCursor } : {}),
            loadingMore: false,
            loadMoreError: false,
          });
          setExpandedKeys((current) => [...new Set([...current, ...getFirstLevelKeys(page.items)])]);
          const hasFiles = page.items.some((node) => node.isFile || (node.children?.length ?? 0) > 0);
          const wasFirstLoad = isFirstLoadRef.current;
          isFirstLoadRef.current = false;
          if (hasFiles) dispatchWorkspaceHasFilesEvent(true, conversation_id, wasFirstLoad);
          return page.items;
        })
        .catch((error) => {
          if (cursor && isBackendHttpError(error) && error.status === 409) {
            setProjectArtifactPage((current) => ({
              ...current,
              hasMore: false,
              nextCursor: undefined,
              loadingMore: false,
              loadMoreError: false,
            }));
            return requestProjectArtifactPage(normalizedQuery, undefined, 'force');
          }
          if (!isAbortError(error)) console.error('[workspace] project artifact page failed');
          if (seq === loadSeqRef.current) {
            setProjectArtifactPage((current) => ({ ...current, loadingMore: false, loadMoreError: Boolean(cursor) }));
          }
          return projectArtifactPageStore.read(requestKey)?.value.items ?? [];
        })
        .finally(() => {
          if (showLoading && seq === loadSeqRef.current) setLoadingHandler(false);
        });
    },
    [
      canLoadWorkspace,
      canonicalProjectId,
      conversation_id,
      isSynonBiomedWorkspace,
      ownerId,
      rootRequestPath,
      rootSnapshotKey,
      setLoadingHandler,
      workspace,
    ]
  );

  /**
   * 加载工作空间文件树
   * Load workspace file tree
   */
  // Track the latest request to ignore stale/aborted responses
  const requestWorkspace = useCallback(
    (path: string, search: string | undefined, mode: 'load' | 'ensure' | 'force') => {
      if (isSynonBiomedWorkspace) {
        return requestProjectArtifactPage(search ?? '', undefined, mode);
      }
      if (!canLoadWorkspace || !rootSnapshotKey) {
        loadSeqRef.current += 1;
        setFiles([]);
        setLoading(false);
        return Promise.resolve([] as IDirOrFile[]);
      }
      const seq = ++loadSeqRef.current;
      const requestPath = isSynonBiomedWorkspace && path === workspace ? `${workspace}/project-files` : path;
      const requestIdentity =
        isSynonBiomedWorkspace && requestPath.startsWith(`${workspace}/`)
          ? requestPath.slice(workspace.length + 1)
          : requestPath;
      const requestKey = workspaceSnapshotKey(ownerId, workspaceAuthorityId, requestIdentity, search || '');
      const hasSnapshot = mode === 'ensure' && Boolean(workspaceSnapshotStore.read(requestKey));
      const showLoading = mode !== 'ensure' || !hasSnapshot;
      if (showLoading) setLoadingHandler(true);
      activeLoadKeysRef.current.add(requestKey);
      const loader = (signal: AbortSignal) =>
        ipcBridge.conversation.getWorkspace.invoke(
          { path: requestPath, workspace, conversation_id, search: search || '' },
          signal
        );
      const request =
        mode === 'ensure'
          ? workspaceSnapshotStore.ensure(requestKey, loader)
          : workspaceSnapshotStore.load(requestKey, loader, mode === 'force');
      return request
        .then((res) => {
          if (seq !== loadSeqRef.current) {
            return res;
          }

          // On refresh, splice already-lazy-loaded subtrees from the old tree
          // back into the new response — the backend only returns one level at
          // a time, so a root refresh would otherwise collapse every dir the
          // user had expanded via loadMore. Skipped for searches and the very
          // first load (no prior tree to merge). Functional setState reads the
          // latest files snapshot without a stale closure.
          if (!isSynonBiomedWorkspace && !search && !isFirstLoadRef.current) {
            setFiles((prev) => {
              const next = mergeLoadedChildren(res, prev);
              workspaceSnapshotStore.write(requestKey, next);
              return next;
            });
          } else {
            workspaceSnapshotStore.write(requestKey, res);
            setFiles(res);
          }
          // 只在搜索时才重置 Tree key，否则保持选中状态
          // Only reset Tree key when searching, otherwise keep selection state
          if (search) {
            setTreeKey(Math.random());
          }

          // 首次加载时展开第一层，后续刷新时保留用户已展开的目录
          // On first load expand first level; on subsequent refreshes preserve user-expanded dirs
          if (isFirstLoadRef.current) {
            setExpandedKeys(getFirstLevelKeys(res));
          } else {
            setExpandedKeys((prev) => {
              const firstLevel = getFirstLevelKeys(res);
              // Merge: keep user-expanded keys + ensure first level is always expanded
              return [...new Set([...prev, ...firstLevel])];
            });
          }

          // 根据是否有文件决定工作空间面板的展开/折叠状态
          // Determine workspace panel expand/collapse state based on files
          const hasFiles = res.length > 0 && (res[0]?.children?.length ?? 0) > 0;

          const wasFirstLoad = isFirstLoadRef.current;
          if (isFirstLoadRef.current) {
            isFirstLoadRef.current = false;
          }

          // Only dispatch expand signal when there are files; never actively
          // collapse — avoids fighting with explicit expand and
          // prevents flicker when workspace starts empty.
          if (hasFiles) {
            dispatchWorkspaceHasFilesEvent(true, conversation_id, wasFirstLoad);
          }

          return res;
        })
        .catch((err) => {
          if (!isAbortError(err)) console.error('[workspace] snapshot refresh failed');
          return workspaceSnapshotStore.read(requestKey)?.value ?? [];
        })
        .finally(() => {
          activeLoadKeysRef.current.delete(requestKey);
          if (showLoading && seq === loadSeqRef.current) {
            setLoadingHandler(false);
          }
        });
    },
    [
      canLoadWorkspace,
      conversation_id,
      isSynonBiomedWorkspace,
      ownerId,
      requestProjectArtifactPage,
      rootSnapshotKey,
      setLoadingHandler,
      workspace,
      workspaceAuthorityId,
    ]
  );

  const loadWorkspace = useCallback(
    (path: string, search?: string, force = false) => requestWorkspace(path, search, force ? 'force' : 'load'),
    [requestWorkspace]
  );

  const ensureWorkspace = useCallback(
    () =>
      isSynonBiomedWorkspace
        ? requestProjectArtifactPage(projectArtifactPage.query, undefined, 'ensure')
        : requestWorkspace(workspace, undefined, 'ensure'),
    [isSynonBiomedWorkspace, projectArtifactPage.query, requestProjectArtifactPage, requestWorkspace, workspace]
  );

  const searchWorkspace = useCallback(
    (query: string) => requestProjectArtifactPage(query, undefined, 'load'),
    [requestProjectArtifactPage]
  );

  const loadMoreWorkspace = useCallback(() => {
    if (!projectArtifactPage.hasMore || !projectArtifactPage.nextCursor || projectArtifactPage.loadingMore) {
      return Promise.resolve(false);
    }
    return requestProjectArtifactPage(
      projectArtifactPage.query,
      projectArtifactPage.nextCursor,
      projectArtifactPage.loadMoreError ? 'load' : 'ensure'
    ).then(() => true);
  }, [projectArtifactPage, requestProjectArtifactPage]);

  /**
   * 刷新工作空间
   * Refresh workspace
   */
  const refreshWorkspace = useCallback(() => {
    return isSynonBiomedWorkspace
      ? requestProjectArtifactPage(projectArtifactPage.query, undefined, 'force')
      : loadWorkspace(workspace, undefined, true);
  }, [isSynonBiomedWorkspace, loadWorkspace, projectArtifactPage.query, requestProjectArtifactPage, workspace]);

  /**
   * 确保节点被选中，并可选地发送事件
   * Ensure node is selected and optionally emit event
   */
  const ensureNodeSelected = useCallback(
    (nodeData: IDirOrFile, options?: { emit?: boolean }) => {
      const key = nodeData.relativePath;
      const shouldEmit = Boolean(options?.emit);

      if (!key) {
        setSelected([]);
        selectedKeysRef.current = [];
        if (!nodeData.isFile && nodeData.fullPath) {
          // 记录最后选中的文件夹 / Remember the latest selected folder
          selectedNodeRef.current = {
            relativePath: key ?? '',
            fullPath: nodeData.fullPath,
          };
        }
        if (shouldEmit && nodeData.fullPath) {
          emitter.emit(`${eventPrefix}.selected.file`, [
            {
              path: nodeData.fullPath,
              name: nodeData.name,
              isFile: nodeData.isFile,
              relativePath: nodeData.relativePath,
            },
          ]);
        } else if (shouldEmit) {
          emitter.emit(`${eventPrefix}.selected.file`, []);
        }
        return;
      }

      setSelected([key]);
      selectedKeysRef.current = [key];

      if (!nodeData.isFile) {
        selectedNodeRef.current = {
          relativePath: key,
          fullPath: nodeData.fullPath,
        };
        if (shouldEmit && nodeData.fullPath) {
          // 将文件夹对象发给发送框 / Emit folder object to send box
          emitter.emit(`${eventPrefix}.selected.file`, [
            {
              path: nodeData.fullPath,
              name: nodeData.name,
              isFile: false,
              relativePath: nodeData.relativePath,
            },
          ]);
        }
      } else if (nodeData.fullPath) {
        selectedNodeRef.current = null;
        if (shouldEmit) {
          // 选中文件时，将文件信息广播 / Broadcast file info when selected
          emitter.emit(`${eventPrefix}.selected.file`, [
            {
              path: nodeData.fullPath,
              name: nodeData.name,
              isFile: true,
              relativePath: nodeData.relativePath,
            },
          ]);
        }
      }
    },
    [eventPrefix]
  );

  /**
   * 清空选中状态
   * Clear selection state
   */
  const clearSelection = useCallback(() => {
    setSelected([]);
    selectedKeysRef.current = [];
    selectedNodeRef.current = null;
  }, []);

  return {
    // State / 状态
    files,
    loading,
    projectArtifactPage,
    treeKey,
    expandedKeys,
    selected,
    selectedKeysRef,
    selectedNodeRef,

    // Actions / 操作
    setFiles,
    setTreeKey,
    setExpandedKeys,
    setSelected,
    loadWorkspace,
    ensureWorkspace,
    searchWorkspace,
    loadMoreWorkspace,
    refreshWorkspace,
    ensureNodeSelected,
    clearSelection,
  };
}

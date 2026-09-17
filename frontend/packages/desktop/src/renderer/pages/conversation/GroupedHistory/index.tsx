/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { TChatConversation } from '@/common/config/storage';
import { DndContext, DragOverlay, closestCenter } from '@dnd-kit/core';
import { SortableContext, verticalListSortingStrategy } from '@dnd-kit/sortable';
import { Plus } from '@icon-park/react';
import classNames from 'classnames';
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useLocation, useParams } from 'react-router';
import type { SynonBiomedProject } from '@/renderer/services/synonBiomedGateway';
import { getActivityTime } from '@/renderer/utils/chat/timeline';
import { emitter } from '@/renderer/utils/emitter';

import ConversationRow from './ConversationRow';
import ProjectHistorySection from './ProjectHistorySection';
import SortableConversationRow from './SortableConversationRow';
import { SiderSectionHeader } from './SiderSectionHeader';
import { useBatchSelection } from './hooks/useBatchSelection';
import { useConversationActions } from './hooks/useConversationActions';
import { useConversations } from './hooks/useConversations';
import { useDragAndDrop } from './hooks/useDragAndDrop';
import {
  persistPreferredProjectId,
  readPreferredProjectId,
  resolveActiveProjectId,
  resolveMostRecentProjectId,
} from './projectSelectionModel';
import {
  resolveProjectSummaryNavigationTarget,
  type ProjectNavigationTarget,
} from '@/renderer/pages/project/projectNavigationModel';
import { beginConversationRoutePerformance } from '@/renderer/pages/conversation/conversationRoutePerformance';
import type { ConversationRowProps, WorkspaceGroupedHistoryProps } from './types';

const BatchSelectionPanel = React.lazy(() => import('./BatchSelectionPanel'));
const DragOverlayContent = React.lazy(() => import('./DragOverlayContent'));
const SessionEditorModal = React.lazy(() => import('./SessionEditorModal'));
const SessionMoveModal = React.lazy(() => import('./SessionMoveModal'));
const SessionNotebookModal = React.lazy(() => import('./SessionNotebookModal'));

const WorkspaceGroupedHistory: React.FC<WorkspaceGroupedHistoryProps> = ({
  onSessionClick,
  collapsed = false,
  tooltipEnabled = false,
  batchMode = false,
  onBatchModeChange,
  onStartTask,
  onActiveProjectChange,
}) => {
  const { id, projectId } = useParams();
  const location = useLocation();
  const { t } = useTranslation();
  const [projectBatchMode, setProjectBatchMode] = useState(false);
  const [selectLatestProjectOnLanding, setSelectLatestProjectOnLanding] = useState(
    () => (location.state as { selectLatestProject?: boolean } | null)?.selectLatestProject === true
  );

  const {
    conversations,
    isConversationGenerating,
    hasCompletionUnread,
    pinnedConversations,
    timelineSections,
    collapsedSections,
    toggleSection,
    hasMoreConversations,
    isLoadingMoreConversations,
    loadMoreConversations,
  } = useConversations();

  const SectionLabel = useCallback(
    ({ sectionKey, label, trailing }: { sectionKey: string; label: string; trailing?: React.ReactNode }) => {
      const isCollapsed = collapsedSections.has(sectionKey);
      const listName = sectionKey === 'conversations' ? t('conversation.history.taskList') : label;
      return (
        <SiderSectionHeader
          testId={`sider-${sectionKey}-header`}
          label={label}
          expanded={!isCollapsed}
          toggleLabel={t(
            isCollapsed ? 'conversation.history.expandNamedList' : 'conversation.history.collapseNamedList',
            { name: listName }
          )}
          onToggle={() => toggleSection(sectionKey)}
          trailing={trailing}
        />
      );
    },
    [collapsedSections, t, toggleSection]
  );

  const conversationProjectId = useMemo(() => {
    const currentConversation = conversations.find((conversation) => conversation.id === id);
    const value = (currentConversation?.extra as Record<string, unknown> | undefined)?.project_id;
    return typeof value === 'string' && value ? value : null;
  }, [conversations, id]);
  const [availableProjects, setAvailableProjects] = useState<SynonBiomedProject[] | null>(null);
  const [preferredProjectId, setPreferredProjectId] = useState<string | undefined>(readPreferredProjectId);
  const mostRecentProjectId = useMemo(
    () => (availableProjects ? resolveMostRecentProjectId(availableProjects) : null),
    [availableProjects]
  );
  const activeProjectId = useMemo(
    () =>
      resolveActiveProjectId({
        routeProjectId: projectId,
        conversationProjectId: conversationProjectId ?? undefined,
        preferredProjectId:
          selectLatestProjectOnLanding && mostRecentProjectId ? mostRecentProjectId : preferredProjectId,
        availableProjectIds: availableProjects?.map((project) => project.projectId) ?? null,
      }),
    [
      availableProjects,
      conversationProjectId,
      mostRecentProjectId,
      preferredProjectId,
      projectId,
      selectLatestProjectOnLanding,
    ]
  );

  useEffect(() => {
    if (projectId) setPreferredProjectId(projectId);
    else if (conversationProjectId) setPreferredProjectId(conversationProjectId);
  }, [conversationProjectId, projectId]);

  useEffect(() => {
    if (availableProjects === null) return;
    setPreferredProjectId((current) => (current === activeProjectId ? current : (activeProjectId ?? undefined)));
    if (selectLatestProjectOnLanding) setSelectLatestProjectOnLanding(false);
    persistPreferredProjectId(activeProjectId);
    onActiveProjectChange?.(activeProjectId);
  }, [activeProjectId, availableProjects, onActiveProjectChange, selectLatestProjectOnLanding]);

  const handleProjectsChange = useCallback((projects: SynonBiomedProject[]) => {
    setAvailableProjects(projects);
  }, []);

  const handleProjectSelect = useCallback((nextProjectId: string) => {
    setSelectLatestProjectOnLanding(false);
    setPreferredProjectId(nextProjectId);
    persistPreferredProjectId(nextProjectId);
  }, []);

  const resolveImmediateProjectTarget = useCallback(
    (nextProjectId: string): ProjectNavigationTarget | null => {
      let latestConversation: TChatConversation | null = null;
      for (const conversation of conversations) {
        const value = (conversation.extra as Record<string, unknown> | undefined)?.project_id;
        if (value !== nextProjectId) continue;
        if (!latestConversation || getActivityTime(conversation) > getActivityTime(latestConversation)) {
          latestConversation = conversation;
        }
      }
      if (latestConversation) {
        return {
          pathname: `/conversation/${encodeURIComponent(latestConversation.id)}`,
        };
      }

      const project = availableProjects?.find((candidate) => candidate.projectId === nextProjectId);
      // A project summary carries its latest visible root frame, so switching
      // projects never waits for the full, decorated workbench collection.
      // Older persisted summaries without that field retain the authoritative
      // bench fallback rather than guessing a route.
      return resolveProjectSummaryNavigationTarget(project);
    },
    [availableProjects, conversations]
  );

  // The paginated conversation history is the sole sidebar task authority.
  // Re-reading the fully decorated bench collection on every project click
  // duplicated ownership, blocked route settlement, and performed expensive
  // per-bench runtime decoration that the sidebar never displays.
  const selectableTaskConversations = useMemo(() => {
    if (!activeProjectId) return [];
    return conversations.filter((conversation) => {
      const value = (conversation.extra as Record<string, unknown> | undefined)?.project_id;
      return value === activeProjectId;
    });
  }, [activeProjectId, conversations]);

  const {
    selectedConversationIds,
    setSelectedConversationIds,
    selectedCount,
    allSelected,
    toggleSelectedConversation,
    handleToggleSelectAll,
  } = useBatchSelection(batchMode, selectableTaskConversations);

  const {
    editorTarget,
    editorLoading,
    editorError,
    handleEditorSubmit,
    handleEditorCancel,
    moveTarget,
    moveProjects,
    moveCurrentProjectId,
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
    handleEditStart,
    handleExport,
    handleDownloadArtifacts,
    handleMenuVisibleChange,
    handleOpenMenu,
  } = useConversationActions({
    batchMode,
    onSessionClick,
    onBatchModeChange,
    selectedConversationIds,
    setSelectedConversationIds,
    toggleSelectedConversation,
  });

  const prepareProjectNavigation = useCallback(
    async (target: ProjectNavigationTarget): Promise<void> => {
      if (!target.pathname.startsWith('/conversation/')) return;
      const encodedConversationId = target.pathname.slice('/conversation/'.length);
      let conversationId = encodedConversationId;
      try {
        conversationId = decodeURIComponent(encodedConversationId);
      } catch {
        return;
      }
      const summary = conversations.find((conversation) => conversation.id === conversationId);
      beginConversationRoutePerformance();
      await prepareConversationNavigation(conversationId, summary);
    },
    [conversations, prepareConversationNavigation]
  );

  const { sensors, activeId, activeConversation, handleDragStart, handleDragEnd, handleDragCancel, isDragEnabled } =
    useDragAndDrop({
      pinnedConversations,
      batchMode,
      collapsed,
    });

  const getConversationRowProps = useCallback(
    (conversation: TChatConversation, selectionEnabled = true): ConversationRowProps => ({
      conversation,
      isGenerating: isConversationGenerating(conversation.id),
      hasCompletionUnread: hasCompletionUnread(conversation.id),
      collapsed,
      tooltipEnabled,
      batchMode: batchMode && selectionEnabled,
      checked: selectedConversationIds.has(conversation.id),
      selected: id === conversation.id,
      menuVisible: dropdownVisibleId !== null && dropdownVisibleId === conversation.id,
      onConversationPrefetch: handleConversationPrefetch,
      onToggleChecked: toggleSelectedConversation,
      onConversationClick: (selectedConversation) => handleConversationClick(selectedConversation, selectionEnabled),
      onOpenMenu: handleOpenMenu,
      onMenuVisibleChange: handleMenuVisibleChange,
      onEditStart: handleEditStart,
      onMove: handleMoveStart,
      onDelete: handleDeleteClick,
      onExport: handleExport,
      onDownloadArtifacts: handleDownloadArtifacts,
      onViewNotebook: handleViewNotebook,
    }),
    [
      collapsed,
      tooltipEnabled,
      batchMode,
      isConversationGenerating,
      hasCompletionUnread,
      selectedConversationIds,
      id,
      dropdownVisibleId,
      toggleSelectedConversation,
      handleConversationPrefetch,
      handleConversationClick,
      handleOpenMenu,
      handleMenuVisibleChange,
      handleEditStart,
      handleMoveStart,
      handleDeleteClick,
      handleExport,
      handleDownloadArtifacts,
      handleViewNotebook,
    ]
  );

  const renderConversation = (conversation: TChatConversation, dimIcon = false) => {
    const rowProps = getConversationRowProps(conversation);
    return <ConversationRow key={conversation.id} {...rowProps} dimIcon={dimIcon} />;
  };

  // Collect all sortable IDs for the pinned section
  const pinnedIds = useMemo(() => pinnedConversations.map((c) => c.id), [pinnedConversations]);

  const taskSections = useMemo(
    () =>
      timelineSections
        .map((section) => {
          const seen = new Set<string>();
          const sectionConversations = section.items
            .flatMap((item) =>
              item.type === 'conversation' && item.conversation
                ? [item.conversation]
                : item.type === 'workspace' && item.workspaceGroup
                  ? item.workspaceGroup.conversations
                  : []
            )
            .filter((conversation) => {
              if (seen.has(conversation.id)) return false;
              seen.add(conversation.id);
              if (!activeProjectId) return false;
              const value = (conversation.extra as Record<string, unknown> | undefined)?.project_id;
              return value === activeProjectId;
            })
            .toSorted((left, right) => getActivityTime(right) - getActivityTime(left));
          return {
            ...section,
            items: sectionConversations.map((conversation) => ({
              type: 'conversation' as const,
              time: getActivityTime(conversation),
              conversation,
            })),
          };
        })
        .filter((section) => section.items.length > 0),
    [activeProjectId, timelineSections]
  );
  const handleStartTask = onStartTask
    ? () => (activeProjectId ? onStartTask(activeProjectId) : emitter.emit('synonbiomed.projects.create'))
    : undefined;
  const shouldRenderTaskSection = taskSections.length > 0 || !!onStartTask;

  return (
    <>
      {editorTarget && (
        <React.Suspense fallback={null}>
          <SessionEditorModal
            conversation={editorTarget}
            loading={editorLoading}
            error={editorError}
            onCancel={handleEditorCancel}
            onSubmit={handleEditorSubmit}
          />
        </React.Suspense>
      )}
      {moveTarget && (
        <React.Suspense fallback={null}>
          <SessionMoveModal
            conversation={moveTarget}
            projects={moveProjects}
            currentProjectId={moveCurrentProjectId}
            loading={moveLoading}
            error={moveError}
            onCancel={handleMoveCancel}
            onSubmit={handleMoveConfirm}
          />
        </React.Suspense>
      )}
      {notebookTarget && (
        <React.Suspense fallback={null}>
          <SessionNotebookModal
            conversation={notebookTarget}
            records={notebookRecords}
            loading={notebookLoading}
            error={notebookError}
            onClose={handleNotebookClose}
          />
        </React.Suspense>
      )}

      <div className='synon-sidebar-navigation'>
        {handleStartTask && (
          <div className={classNames('synon-sidebar-new-chat-wrap px-8px pt-4px pb-2px', collapsed && 'px-4px')}>
            <button
              type='button'
              data-testid='sider-new-chat'
              aria-label={t('conversation.history.newChat')}
              className={classNames(
                'synon-sidebar-new-chat w-full h-32px flex items-center border-none bg-transparent rd-8px cursor-pointer text-t-tertiary hover:text-t-primary hover:bg-fill-2 transition-colors',
                collapsed ? 'justify-center px-0' : 'gap-6px px-4px text-left'
              )}
              onClick={handleStartTask}
            >
              <span className='size-24px shrink-0 rd-8px flex items-center justify-center border border-solid border-arco-2 bg-transparent'>
                <Plus theme='outline' size='16' />
              </span>
              {!collapsed && (
                <span className='text-14px font-[500] leading-none'>{t('conversation.history.newChat')}</span>
              )}
            </button>
          </div>
        )}

        {/* L1: Pinned section */}
        <DndContext
          sensors={sensors}
          collisionDetection={closestCenter}
          onDragStart={handleDragStart}
          onDragEnd={handleDragEnd}
          onDragCancel={handleDragCancel}
        >
          {pinnedConversations.length > 0 && (
            <div className='min-w-0'>
              {!collapsed && <SectionLabel sectionKey='pinned' label={t('conversation.history.pinnedSection')} />}
              {!collapsedSections.has('pinned') && (
                <SortableContext items={pinnedIds} strategy={verticalListSortingStrategy}>
                  <div className='min-w-0'>
                    {pinnedConversations.map((conversation) => {
                      const props = getConversationRowProps(conversation, false);
                      return isDragEnabled ? (
                        <SortableConversationRow key={conversation.id} {...props} />
                      ) : (
                        <ConversationRow key={conversation.id} {...props} />
                      );
                    })}
                  </div>
                </SortableContext>
              )}
            </div>
          )}

          <DragOverlay dropAnimation={null}>
            {activeId && activeConversation ? (
              <React.Suspense fallback={null}>
                <DragOverlayContent conversation={activeConversation} />
              </React.Suspense>
            ) : null}
          </DragOverlay>
        </DndContext>

        <ProjectHistorySection
          collapsed={collapsed}
          activeProjectId={activeProjectId}
          onProjectsChange={handleProjectsChange}
          onProjectSelect={handleProjectSelect}
          resolveImmediateProjectTarget={resolveImmediateProjectTarget}
          onPrepareNavigation={prepareProjectNavigation}
          batchMode={projectBatchMode}
          onBatchModeChange={(nextBatchMode) => {
            if (nextBatchMode) onBatchModeChange?.(false);
            setProjectBatchMode(nextBatchMode);
          }}
        />

        {/* L1: Tasks section — peer to projects, internally split by timeline */}
        {shouldRenderTaskSection && (
          <div
            className='synon-sidebar-task-section min-w-0'
            data-testid='sider-task-section'
            data-project-id={activeProjectId ?? undefined}
          >
            {batchMode && !collapsed && (
              <React.Suspense fallback={null}>
                <BatchSelectionPanel
                  scope='tasks'
                  selectedCount={selectedCount}
                  allSelected={allSelected}
                  onToggleSelectAll={handleToggleSelectAll}
                  onDelete={handleBatchDelete}
                />
              </React.Suspense>
            )}
            {taskSections.map((section) => (
              <div key={section.timeline} className='min-w-0'>
                {!collapsed && (
                  <div className='synon-sidebar-timeline flex items-center px-16px h-24px select-none'>
                    <span className='text-12px text-t-secondary font-[500] leading-none'>{section.timeline}</span>
                  </div>
                )}
                {section.items.map((item) =>
                  item.type === 'conversation' && item.conversation ? renderConversation(item.conversation) : null
                )}
              </div>
            ))}
            {taskSections.length === 0 && !collapsed && (
              <div className='px-16px py-8px text-12px leading-18px text-t-tertiary' role='status'>
                {!activeProjectId
                  ? t('conversation.history.createProjectFirst')
                  : t('conversation.history.noProjectTasks')}
              </div>
            )}
            {hasMoreConversations && !collapsed && (
              <button
                type='button'
                className='mx-16px my-6px h-30px w-[calc(100%-32px)] rounded-6px border border-solid border-arco-2 bg-transparent text-12px text-t-secondary hover:bg-fill-2 hover:text-t-primary disabled:cursor-wait disabled:opacity-60'
                disabled={isLoadingMoreConversations}
                onClick={loadMoreConversations}
              >
                {t(isLoadingMoreConversations ? 'conversation.history.loadingMore' : 'conversation.history.loadMore')}
              </button>
            )}
          </div>
        )}
      </div>
    </>
  );
};

export default WorkspaceGroupedHistory;

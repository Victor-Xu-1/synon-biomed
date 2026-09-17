/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { fireEvent, screen } from '@testing-library/react';
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

type MockTimelineSection = {
  timeline: string;
  items: Array<{
    type: 'workspace' | 'conversation';
    time: number;
    workspaceGroup?: {
      workspace: string;
      display_name: string;
      conversations: Array<{ id: string; name: string; extra?: Record<string, unknown> }>;
    };
    conversation?: { id: string; name: string; extra?: Record<string, unknown> };
  }>;
};

const navigateMock = vi.fn();
const groupedHistoryState = vi.hoisted(() => ({
  timelineSections: [] as MockTimelineSection[],
  params: {} as { id?: string; projectId?: string },
  locationState: null as { selectLatestProject?: boolean } | null,
  projects: [{ projectId: 'proj_stat6' }] as Array<{
    projectId: string;
    createdAt?: string | null;
    updatedAt?: string | null;
    lastActiveAt?: string | null;
  }>,
}));
const conversationPaginationState = vi.hoisted(() => ({
  hasMore: false,
  loading: false,
  loadMore: vi.fn(),
}));

const projectWorkspaceItem = {
  type: 'workspace' as const,
  time: 2,
  workspaceGroup: {
    workspace: '/workspace/stat6',
    display_name: 'STAT6',
    conversations: [{ id: 'project-task-1', name: 'STAT6', extra: { project_id: 'proj_stat6' } }],
  },
};

vi.mock('react-router', async () => {
  const actual = await vi.importActual<typeof import('react-router')>('react-router');
  return {
    ...actual,
    useNavigate: () => navigateMock,
    useParams: () => groupedHistoryState.params,
    useLocation: () => ({ pathname: '/guid', search: '', hash: '', state: groupedHistoryState.locationState }),
  };
});

vi.mock('@arco-design/web-react', () => {
  // oxlint-disable-next-line unicorn/consistent-function-scoping -- vi.mock factories are hoisted and cannot close over a module-level React component.
  const Menu = ({ children }: { children: React.ReactNode }) => <div>{children}</div>;
  Menu.Item = ({ children }: { children: React.ReactNode }) => <div>{children}</div>;

  return {
    Button: ({
      children,
      onClick,
      disabled,
      className,
    }: {
      children: React.ReactNode;
      onClick?: () => void;
      disabled?: boolean;
      className?: string;
    }) => (
      <button type='button' className={className} disabled={disabled} onClick={onClick}>
        {children}
      </button>
    ),
    Dropdown: ({ children }: { children: React.ReactNode }) => <>{children}</>,
    Empty: ({ description }: { description: React.ReactNode }) => <div>{description}</div>,
    Input: () => <input aria-label='rename' />,
    Menu,
    Modal: ({ children }: { children: React.ReactNode }) => <>{children}</>,
    Tooltip: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  };
});

vi.mock('@dnd-kit/core', () => ({
  DndContext: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  DragOverlay: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  closestCenter: vi.fn(),
}));

vi.mock('@dnd-kit/sortable', () => ({
  SortableContext: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  verticalListSortingStrategy: {},
}));

vi.mock('@/renderer/components/base/SynonModal', () => ({
  default: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));

vi.mock('@/renderer/components/settings/DirectorySelectionModal', () => ({
  default: () => null,
}));

vi.mock('@/renderer/hooks/context/LayoutContext', () => ({
  useLayoutContext: () => ({ isMobile: false }),
}));

vi.mock('@/renderer/pages/conversation/components/WorkspaceCollapse', () => ({
  default: ({
    header,
    trailing,
    children,
  }: {
    header: React.ReactNode;
    trailing?: React.ReactNode;
    children: React.ReactNode;
  }) => (
    <div data-testid='workspace-collapse'>
      <div>
        {header}
        {trailing}
      </div>
      {children}
    </div>
  ),
}));

vi.mock('@/renderer/pages/conversation/GroupedHistory/ProjectHistorySection', () => ({
  default: ({
    onProjectsChange,
    batchMode,
    onBatchModeChange,
  }: {
    onProjectsChange?: (projects: Array<{ projectId: string }>) => void;
    batchMode?: boolean;
    onBatchModeChange?: (value: boolean) => void;
  }) => {
    React.useEffect(() => {
      onProjectsChange?.(groupedHistoryState.projects);
    }, [onProjectsChange]);
    return (
      <div data-testid='gateway-project-history'>
        Synon gateway projects
        <button type='button' data-testid='mock-project-batch-manage' onClick={() => onBatchModeChange?.(!batchMode)}>
          projects batch
        </button>
        {batchMode && <div data-testid='mock-project-batch-panel'>projects panel</div>}
      </div>
    );
  },
}));

vi.mock('@/renderer/pages/conversation/GroupedHistory/SessionEditorModal', () => ({ default: () => null }));
vi.mock('@/renderer/pages/conversation/GroupedHistory/SessionMoveModal', () => ({ default: () => null }));
vi.mock('@/renderer/pages/conversation/GroupedHistory/SessionNotebookModal', () => ({ default: () => null }));

vi.mock('@/renderer/pages/conversation/GroupedHistory/ConversationRow', () => ({
  default: ({ conversation }: { conversation: { name?: string; title?: string } }) => (
    <div data-testid='conversation-row'>{conversation.name ?? conversation.title}</div>
  ),
}));

vi.mock('@/renderer/pages/conversation/GroupedHistory/SortableConversationRow', () => ({
  default: ({ conversation }: { conversation: { name?: string; title?: string } }) => (
    <div data-testid='sortable-conversation-row'>{conversation.name ?? conversation.title}</div>
  ),
}));

vi.mock('@/renderer/pages/conversation/GroupedHistory/DragOverlayContent', () => ({
  default: () => null,
}));

vi.mock('@/renderer/pages/conversation/GroupedHistory/hooks/useBatchSelection', () => ({
  useBatchSelection: () => ({
    selectedConversationIds: new Set<string>(),
    setSelectedConversationIds: vi.fn(),
    selectedCount: 0,
    allSelected: false,
    toggleSelectedConversation: vi.fn(),
    handleToggleSelectAll: vi.fn(),
  }),
}));

vi.mock('@/renderer/pages/conversation/GroupedHistory/hooks/useConversationActions', () => ({
  useConversationActions: () => ({
    editorTarget: null,
    editorLoading: false,
    editorError: null,
    handleEditorSubmit: vi.fn(),
    handleEditorCancel: vi.fn(),
    moveTarget: null,
    moveProjects: [],
    moveCurrentProjectId: null,
    moveLoading: false,
    moveError: null,
    handleMoveStart: vi.fn(),
    handleMoveConfirm: vi.fn(),
    handleMoveCancel: vi.fn(),
    notebookTarget: null,
    notebookRecords: [],
    notebookLoading: false,
    notebookError: null,
    handleViewNotebook: vi.fn(),
    handleNotebookClose: vi.fn(),
    dropdownVisibleId: null,
    handleConversationClick: vi.fn(),
    handleDeleteClick: vi.fn(),
    handleBatchDelete: vi.fn(),
    handleEditStart: vi.fn(),
    handleExport: vi.fn(),
    handleDownloadArtifacts: vi.fn(),
    handleMenuVisibleChange: vi.fn(),
    handleOpenMenu: vi.fn(),
    handleRemoveProject: vi.fn(),
    removeProjectTarget: null,
    removeProjectLoading: false,
    handleRemoveProjectCancel: vi.fn(),
    handleRemoveProjectConfirm: vi.fn(),
  }),
}));

vi.mock('@/renderer/pages/conversation/GroupedHistory/hooks/useExport', () => ({
  useExport: () => ({
    exportTask: null,
    exportModalVisible: false,
    exportTargetPath: '',
    exportModalLoading: false,
    showExportDirectorySelector: false,
    setShowExportDirectorySelector: vi.fn(),
    closeExportModal: vi.fn(),
    handleSelectExportDirectoryFromModal: vi.fn(),
    handleSelectExportFolder: vi.fn(),
    handleConfirmExport: vi.fn(),
  }),
}));

vi.mock('@/renderer/pages/conversation/GroupedHistory/hooks/useDragAndDrop', () => ({
  useDragAndDrop: () => ({
    sensors: [],
    activeId: null,
    activeConversation: null,
    handleDragStart: vi.fn(),
    handleDragEnd: vi.fn(),
    handleDragCancel: vi.fn(),
    isDragEnabled: false,
  }),
}));

vi.mock('@/renderer/pages/conversation/GroupedHistory/hooks/useConversations', () => ({
  useConversations: () => ({
    conversations: [{ id: 'task-1', name: 'STAT6 SBDD PPI流程' }],
    isConversationGenerating: () => false,
    hasCompletionUnread: () => false,
    expandedWorkspaces: ['/workspace/stat6'],
    pinnedConversations: [],
    timelineSections: [...groupedHistoryState.timelineSections],
    handleToggleWorkspace: vi.fn(),
    collapsedSections: new Set<string>(),
    toggleSection: vi.fn(),
    hasMoreConversations: conversationPaginationState.hasMore,
    isLoadingMoreConversations: conversationPaginationState.loading,
    loadMoreConversations: conversationPaginationState.loadMore,
  }),
}));

import WorkspaceGroupedHistory from '@/renderer/pages/conversation/GroupedHistory';

describe('GroupedHistory project and task sections', () => {
  beforeEach(() => {
    localStorage.removeItem('synonbiomed.active-project-id');
    navigateMock.mockReset();
    groupedHistoryState.params = {};
    groupedHistoryState.locationState = null;
    groupedHistoryState.projects = [{ projectId: 'proj_stat6' }];
    conversationPaginationState.hasMore = false;
    conversationPaginationState.loading = false;
    conversationPaginationState.loadMore.mockReset();
    groupedHistoryState.timelineSections = [
      {
        timeline: '今天',
        items: [
          projectWorkspaceItem,
          { type: 'conversation', time: 1, conversation: { id: 'task-1', name: 'STAT6 SBDD PPI流程' } },
        ],
      },
    ];
  });

  it('uses the first backend project as the default task context', async () => {
    const onStartTask = vi.fn();

    await renderWithI18n(
      React.createElement(WorkspaceGroupedHistory as React.ComponentType<Record<string, unknown>>, {
        collapsed: false,
        tooltipEnabled: false,
        onStartTask,
      })
    );

    expect(screen.getByTestId('gateway-project-history')).toBeInTheDocument();
    expect(screen.queryByTestId('workspace-collapse')).not.toBeInTheDocument();
    const newChat = screen.getByRole('button', { name: '新任务' });
    const projects = screen.getByTestId('gateway-project-history');
    expect(newChat.compareDocumentPosition(projects) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(screen.queryByText('任务列表')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '折叠任务列表' })).not.toBeInTheDocument();
    expect(screen.getByText('今天')).toBeInTheDocument();
    expect(screen.getByText('STAT6')).toBeInTheDocument();

    fireEvent.click(newChat);

    expect(onStartTask).toHaveBeenCalledWith('proj_stat6');
  });

  it('opens a fresh chat in the most recently active project after login', async () => {
    localStorage.setItem('synonbiomed.active-project-id', 'proj_older');
    groupedHistoryState.locationState = { selectLatestProject: true };
    groupedHistoryState.projects = [
      {
        projectId: 'proj_older',
        updatedAt: '2026-07-01T00:00:00Z',
        lastActiveAt: '2026-07-02T00:00:00Z',
      },
      {
        projectId: 'proj_latest',
        updatedAt: '2026-08-02T00:00:00Z',
        lastActiveAt: '2026-08-03T00:00:00Z',
      },
    ];
    const onStartTask = vi.fn();

    await renderWithI18n(
      React.createElement(WorkspaceGroupedHistory as React.ComponentType<Record<string, unknown>>, {
        collapsed: false,
        tooltipEnabled: false,
        onStartTask,
      })
    );

    fireEvent.click(screen.getByRole('button', { name: '新任务' }));
    expect(onStartTask).toHaveBeenCalledWith('proj_latest');
    expect(localStorage.getItem('synonbiomed.active-project-id')).toBe('proj_latest');
  });

  it('keeps the new task action visible when every existing task is grouped under a project', async () => {
    groupedHistoryState.timelineSections = [
      {
        timeline: '今天',
        items: [projectWorkspaceItem],
      },
    ];
    const onStartTask = vi.fn();

    await renderWithI18n(
      React.createElement(WorkspaceGroupedHistory as React.ComponentType<Record<string, unknown>>, {
        collapsed: false,
        tooltipEnabled: false,
        onStartTask,
      }),
      'en-US'
    );

    expect(screen.getByTestId('gateway-project-history')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'New chat' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Collapse Task list' })).not.toBeInTheDocument();
    expect(screen.getByText('STAT6')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'New chat' }));

    expect(onStartTask).toHaveBeenCalledWith('proj_stat6');
  });

  it('filters tasks through the paginated conversation authority and starts the new task in that project', async () => {
    groupedHistoryState.params = { projectId: 'proj_stat6' };
    groupedHistoryState.timelineSections = [
      {
        timeline: '今天',
        items: [
          projectWorkspaceItem,
          {
            type: 'conversation',
            time: 1,
            conversation: { id: 'legacy-task', name: 'Legacy task', extra: { project_id: 'proj_stat6' } },
          },
        ],
      },
    ];
    const onStartTask = vi.fn();

    await renderWithI18n(
      React.createElement(WorkspaceGroupedHistory as React.ComponentType<Record<string, unknown>>, {
        collapsed: false,
        tooltipEnabled: false,
        onStartTask,
      })
    );

    expect(screen.getByText('Legacy task')).toBeInTheDocument();
    expect(screen.getByText('STAT6')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '新任务' }));
    expect(onStartTask).toHaveBeenCalledWith('proj_stat6');
  });

  it('removes the task batch-management control while retaining task history', async () => {
    const onBatchModeChange = vi.fn();

    await renderWithI18n(
      React.createElement(WorkspaceGroupedHistory as React.ComponentType<Record<string, unknown>>, {
        collapsed: false,
        tooltipEnabled: false,
        onStartTask: vi.fn(),
        onBatchModeChange,
        batchMode: false,
      })
    );

    expect(screen.queryByTestId('sider-toolbar-new-task')).not.toBeInTheDocument();
    expect(screen.getByTestId('sider-new-chat')).toBeInTheDocument();
    expect(screen.queryByTestId('sider-start-task')).not.toBeInTheDocument();
    expect(screen.queryByTestId('sider-batch-manage')).not.toBeInTheDocument();
    expect(screen.getByText('STAT6')).toBeInTheDocument();
    expect(onBatchModeChange).not.toHaveBeenCalled();
  });

  it('loads older sidebar tasks only when the user requests the next cursor page', async () => {
    conversationPaginationState.hasMore = true;

    await renderWithI18n(
      React.createElement(WorkspaceGroupedHistory as React.ComponentType<Record<string, unknown>>, {
        collapsed: false,
        tooltipEnabled: false,
        onStartTask: vi.fn(),
      })
    );

    const loadMore = screen.getByRole('button', { name: '加载更多任务' });
    fireEvent.click(loadMore);
    expect(conversationPaginationState.loadMore).toHaveBeenCalledOnce();
  });

  it('keeps an externally controlled task batch panel isolated from project batch mode', async () => {
    const Harness = () => {
      const [taskBatchMode, setTaskBatchMode] = React.useState(true);
      return (
        <WorkspaceGroupedHistory
          collapsed={false}
          tooltipEnabled={false}
          onStartTask={vi.fn()}
          batchMode={taskBatchMode}
          onBatchModeChange={setTaskBatchMode}
        />
      );
    };

    await renderWithI18n(<Harness />);
    const taskPanel = await screen.findByTestId('tasks-batch-panel');
    expect(screen.getByTestId('sider-task-section')).toContainElement(taskPanel);
    expect(screen.queryByTestId('mock-project-batch-panel')).not.toBeInTheDocument();

    fireEvent.click(screen.getByTestId('mock-project-batch-manage'));
    expect(screen.getByTestId('mock-project-batch-panel')).toBeInTheDocument();
    expect(screen.queryByTestId('tasks-batch-panel')).not.toBeInTheDocument();
  });
});

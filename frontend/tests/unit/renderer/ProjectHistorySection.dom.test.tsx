import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import React from 'react';
import { describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';
import { emitter } from '@/renderer/utils/emitter';

const navigateMock = vi.fn();
const locationState = { pathname: '/projects/proj_example' };
const projectFixture = {
  projectId: 'proj_example',
  latestConversationId: 'frame-latest',
  name: 'Example project',
  description: 'Example project description',
  context: 'Use GRCh38.',
  conversationCount: 4,
  artifactCount: 83,
  createdAt: null,
  updatedAt: null,
  lastActiveAt: null,
};
const secondProjectFixture = {
  ...projectFixture,
  projectId: 'proj_second',
  name: 'Second project',
};
const loadProjectsMock = vi.fn(async () => [projectFixture]);
const createProjectMock = vi.fn(async () => ({
  ...projectFixture,
  projectId: 'proj_created',
  name: 'STAT6 program',
  description: 'Structure program',
  context: 'Use reviewed structures only.',
  conversationCount: 0,
  artifactCount: 0,
}));
const updateProjectMock = vi.fn(async () => ({
  ...projectFixture,
  name: 'Example project updated',
}));
const deleteProjectMock = vi.fn(async () => undefined);
const loadProjectArtifactsMock = vi.fn(async () => [{ artifactId: 'artifact-1' }, { artifactId: 'artifact-2' }]);
const loadProjectBenchesMock = vi.fn();
const downloadFileFromUrlMock = vi.fn(async () => undefined);
const loadProjectOrderMock = vi.fn<() => Promise<string[]>>(async () => []);
const saveProjectOrderMock = vi.fn<(ids: readonly string[]) => Promise<void>>(async () => undefined);
const { copyTextMock } = vi.hoisted(() => ({
  copyTextMock: vi.fn(async () => undefined),
}));

vi.mock('@/renderer/utils/ui/clipboard', () => ({ copyText: copyTextMock }));

vi.mock('@arco-design/web-react', async () => {
  const actual = await vi.importActual<typeof import('@arco-design/web-react')>('@arco-design/web-react');
  return {
    ...actual,
    Message: {
      error: vi.fn(),
      info: vi.fn(),
      success: vi.fn(),
    },
    Tooltip: ({ children }: { children: React.ReactNode }) => <>{children}</>,
    Modal: ({
      visible,
      title,
      children,
      onCancel,
    }: {
      visible?: boolean;
      title?: React.ReactNode;
      children?: React.ReactNode;
      onCancel?: () => void;
    }) =>
      visible ? (
        <div role='dialog' aria-label={String(title)}>
          <button type='button' aria-label='关闭' onClick={onCancel} />
          {children}
        </div>
      ) : null,
  };
});

vi.mock('@/renderer/services/synonBiomedGateway', () => ({
  loadSynonBiomedProjects: (...args: unknown[]) => loadProjectsMock(...args),
  createSynonBiomedProject: (...args: unknown[]) => createProjectMock(...args),
  updateSynonBiomedProject: (...args: unknown[]) => updateProjectMock(...args),
  deleteSynonBiomedProject: (...args: unknown[]) => deleteProjectMock(...args),
  loadSynonBiomedProjectArtifacts: (...args: unknown[]) => loadProjectArtifactsMock(...args),
  loadSynonBiomedProjectBenches: (...args: unknown[]) => loadProjectBenchesMock(...args),
}));

vi.mock('@/renderer/services/projectSidebarOrder', () => ({
  loadProjectSidebarOrder: () => loadProjectOrderMock(),
  saveProjectSidebarOrder: (ids: readonly string[]) => saveProjectOrderMock(ids),
}));

vi.mock('@/renderer/utils/file/download', () => ({
  downloadFileFromUrl: (...args: unknown[]) => downloadFileFromUrlMock(...args),
}));

vi.mock('react-router', () => ({
  useNavigate: () => navigateMock,
  useLocation: () => locationState,
}));

import ProjectHistorySection from '@/renderer/pages/conversation/GroupedHistory/ProjectHistorySection';

describe('ProjectHistorySection', () => {
  beforeEach(() => {
    navigateMock.mockReset();
    locationState.pathname = '/projects/proj_example';
    loadProjectsMock.mockReset();
    loadProjectsMock.mockResolvedValue([projectFixture]);
    createProjectMock.mockClear();
    updateProjectMock.mockClear();
    deleteProjectMock.mockClear();
    loadProjectArtifactsMock.mockClear();
    loadProjectBenchesMock.mockReset();
    loadProjectBenchesMock.mockResolvedValue([
      {
        frameId: 'frame-latest',
        rootFrameId: 'frame-latest',
        parentFrameId: null,
        projectId: 'proj_example',
        name: 'Latest task',
        taskSummary: null,
        agentName: null,
        status: null,
        statusDescription: null,
        createdAt: '2026-08-10T00:00:00Z',
        updatedAt: '2026-08-10T00:00:00Z',
        completedAt: null,
        lastActivityAt: '2026-08-10T00:00:00Z',
        hasImageOutput: false,
      },
    ]);
    downloadFileFromUrlMock.mockClear();
    loadProjectOrderMock.mockReset();
    loadProjectOrderMock.mockResolvedValue([]);
    saveProjectOrderMock.mockReset();
    saveProjectOrderMock.mockResolvedValue(undefined);
  });

  it('renders Synon Biomed projects from the real gateway client and opens the SynonAI project route', async () => {
    const onProjectsChange = vi.fn();
    const onProjectSelect = vi.fn();
    const onPrepareNavigation = vi.fn().mockResolvedValue(undefined);
    const resolveImmediateProjectTarget = vi.fn(() => ({ pathname: '/conversation/frame-latest' }) as const);
    await renderWithI18n(
      <ProjectHistorySection
        collapsed={false}
        activeProjectId='proj_example'
        onProjectsChange={onProjectsChange}
        onProjectSelect={onProjectSelect}
        resolveImmediateProjectTarget={resolveImmediateProjectTarget}
        onPrepareNavigation={onPrepareNavigation}
      />
    );

    const collapse = await screen.findByRole('button', {
      name: '折叠项目列表',
    });
    expect(collapse).toHaveAttribute('aria-expanded', 'true');
    const project = await screen.findByRole('button', {
      name: 'Example project',
    });
    expect(screen.getByTestId('project-row-proj_example')).toHaveStyle({
      position: 'relative',
    });
    expect(project).toHaveTextContent('4 个任务');
    expect(project).toHaveTextContent('83 个文件');

    const projectListTitle = screen.getByRole('button', { name: '项目列表' });
    fireEvent.click(projectListTitle);
    expect(screen.queryByRole('button', { name: 'Example project' })).not.toBeInTheDocument();
    expect(screen.queryByRole('dialog', { name: '新建项目' })).not.toBeInTheDocument();
    fireEvent.click(projectListTitle);
    expect(await screen.findByRole('button', { name: 'Example project' })).toBeInTheDocument();

    fireEvent.click(collapse);
    expect(screen.queryByRole('button', { name: 'Example project' })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '展开项目列表' }));

    fireEvent.click(screen.getByRole('button', { name: 'Example project' }));

    expect(loadProjectsMock).toHaveBeenCalledTimes(1);
    expect(onProjectsChange).toHaveBeenCalledWith([projectFixture]);
    expect(onProjectSelect).toHaveBeenCalledWith('proj_example');
    expect(resolveImmediateProjectTarget).toHaveBeenCalledWith('proj_example');
    expect(onPrepareNavigation).toHaveBeenCalledWith({
      pathname: '/conversation/frame-latest',
    });
    await waitFor(() => expect(navigateMock).toHaveBeenCalledWith('/conversation/frame-latest'));
    expect(loadProjectBenchesMock).not.toHaveBeenCalled();
    expect(navigateMock).not.toHaveBeenCalledWith('/projects/proj_example');
  });

  it('opens a project from the already loaded conversation index without waiting for the bench read model', async () => {
    const resolveImmediateProjectTarget = vi.fn(() => ({ pathname: '/conversation/frame-cached' }) as const);
    await renderWithI18n(
      <ProjectHistorySection
        collapsed={false}
        activeProjectId={null}
        resolveImmediateProjectTarget={resolveImmediateProjectTarget}
      />
    );

    fireEvent.click(await screen.findByRole('button', { name: 'Example project' }));

    expect(resolveImmediateProjectTarget).toHaveBeenCalledWith('proj_example');
    await waitFor(() => expect(navigateMock).toHaveBeenCalledWith('/conversation/frame-cached'));
    expect(loadProjectBenchesMock).not.toHaveBeenCalled();
  });

  it('uses the compact drag handle and omits the folder icon for every project row', async () => {
    loadProjectsMock.mockResolvedValue([projectFixture, secondProjectFixture]);
    await renderWithI18n(<ProjectHistorySection collapsed={false} activeProjectId={null} />);

    for (const project of [projectFixture, secondProjectFixture]) {
      const openButton = await screen.findByRole('button', {
        name: project.name,
      });
      const dragHandle = screen.getByTestId(`project-drag-handle-${project.projectId}`);

      expect(openButton.querySelector('svg')).toBeNull();
      expect(openButton).toHaveClass('pl-22px');
      expect(dragHandle).toHaveClass('left-1px', 'size-18px');
    }
  });

  it('recreates the v1.1 three-field project creation flow and opens the created project', async () => {
    await renderWithI18n(
      <ProjectHistorySection collapsed={false} activeProjectId={null} onBatchModeChange={vi.fn()} />
    );

    const createProjectAction = await screen.findByTestId('project-create-action');
    expect(createProjectAction).toHaveAccessibleName('新建项目');
    expect(screen.getByText('项目列表')).toBeInTheDocument();
    expect(createProjectAction).not.toHaveTextContent('创建');
    expect(screen.queryByTestId('project-batch-manage')).not.toBeInTheDocument();

    fireEvent.click(createProjectAction);
    const createDialog = await screen.findByRole('dialog', { name: '新建项目' });
    expect(createDialog).toBeInTheDocument();

    fireEvent.change(screen.getByRole('textbox', { name: '名称' }), {
      target: { value: 'STAT6 program' },
    });
    fireEvent.change(screen.getByRole('textbox', { name: '说明' }), {
      target: { value: 'Structure program' },
    });
    fireEvent.change(screen.getByRole('textbox', { name: 'Agent Context' }), {
      target: { value: 'Use reviewed structures only.' },
    });
    fireEvent.click(within(createDialog).getByRole('button', { name: '创建' }));

    await waitFor(() =>
      expect(createProjectMock).toHaveBeenCalledWith({
        name: 'STAT6 program',
        description: 'Structure program',
        context: 'Use reviewed structures only.',
      })
    );
    await waitFor(() =>
      expect(navigateMock).toHaveBeenCalledWith('/guid', {
        state: {
          resetAssistant: true,
          workspace: 'synonbiomed://project/proj_created',
        },
      })
    );
  });

  it('navigates rapid project selections without starting competing bench lookups', async () => {
    loadProjectsMock.mockResolvedValue([projectFixture, secondProjectFixture]);
    const resolveImmediateProjectTarget = vi.fn((projectId: string) => ({
      pathname: `/conversation/${projectId}-latest`,
    }));

    await renderWithI18n(
      <ProjectHistorySection
        collapsed={false}
        activeProjectId={null}
        resolveImmediateProjectTarget={resolveImmediateProjectTarget}
      />
    );
    fireEvent.click(await screen.findByRole('button', { name: 'Example project' }));
    fireEvent.click(screen.getByRole('button', { name: 'Second project' }));

    await waitFor(() => expect(navigateMock).toHaveBeenCalledWith('/conversation/proj_second-latest'));
    expect(navigateMock).not.toHaveBeenCalledWith('/conversation/proj_example-latest');
    expect(loadProjectBenchesMock).not.toHaveBeenCalled();
  });

  it('does not let an older project refresh resurrect a deleted row', async () => {
    let resolveInitial!: (value: unknown[]) => void;
    let resolveRefresh!: (value: unknown[]) => void;
    loadProjectsMock
      .mockImplementationOnce(() => new Promise<unknown[]>((resolve) => (resolveInitial = resolve)))
      .mockImplementationOnce(() => new Promise<unknown[]>((resolve) => (resolveRefresh = resolve)));

    await renderWithI18n(<ProjectHistorySection collapsed={false} activeProjectId={null} />);
    emitter.emit('synonbiomed.projects.refresh');
    resolveRefresh([]);
    await waitFor(() => expect(screen.queryByTestId('project-row-proj_example')).not.toBeInTheDocument());

    resolveInitial([projectFixture]);
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(screen.queryByTestId('project-row-proj_example')).not.toBeInTheDocument();
  });

  it('edits and deletes a project from the v1.1 project operation menu', async () => {
    await renderWithI18n(<ProjectHistorySection collapsed={false} activeProjectId='proj_example' />);
    const menuButton = await screen.findByRole('button', {
      name: 'Example project 项目操作',
    });

    fireEvent.click(menuButton);
    fireEvent.click(screen.getByRole('menuitem', { name: '编辑项目' }));
    expect(screen.getByRole('textbox', { name: '名称' })).toHaveValue('Example project');
    expect(screen.getByRole('textbox', { name: '说明' })).toHaveValue('Example project description');
    expect(screen.getByRole('textbox', { name: 'Agent Context' })).toHaveValue('Use GRCh38.');
    fireEvent.change(screen.getByRole('textbox', { name: '名称' }), {
      target: { value: 'Example project updated' },
    });
    loadProjectsMock.mockResolvedValue([{ ...projectFixture, name: 'Example project updated' }]);
    fireEvent.click(screen.getByRole('button', { name: '保存' }));

    await waitFor(() =>
      expect(updateProjectMock).toHaveBeenCalledWith('proj_example', {
        name: 'Example project updated',
        description: 'Example project description',
        context: 'Use GRCh38.',
      })
    );
    expect(navigateMock).toHaveBeenCalledWith('/projects/proj_example', {
      replace: true,
      state: { synonBiomedProjectRevision: expect.any(Number) },
    });

    fireEvent.click(screen.getByRole('button', { name: 'Example project updated 项目操作' }));
    fireEvent.click(screen.getByRole('menuitem', { name: '删除项目' }));
    expect(screen.getByRole('dialog', { name: '删除项目' })).toHaveTextContent('Example project updated');
    loadProjectsMock.mockResolvedValue([]);
    fireEvent.click(screen.getByRole('button', { name: '确认删除' }));

    await waitFor(() => expect(deleteProjectMock).toHaveBeenCalledWith('proj_example'));
    expect(navigateMock).toHaveBeenCalledWith('/guid', { replace: true });
    await waitFor(() =>
      expect(screen.queryByRole('button', { name: 'Example project updated' })).not.toBeInTheDocument()
    );
  });

  it('exposes and copies the authoritative project id from the project menu', async () => {
    await renderWithI18n(<ProjectHistorySection collapsed={false} activeProjectId='proj_example' />);
    fireEvent.click(await screen.findByRole('button', { name: 'Example project 项目操作' }));

    expect(screen.queryByText('项目 ID')).not.toBeInTheDocument();
    expect(screen.queryByText('proj_example')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('menuitem', { name: '复制项目 ID' }));
    await waitFor(() => expect(copyTextMock).toHaveBeenCalledWith('proj_example'));
  });

  it('leaves a conversation route when deleting its active project', async () => {
    locationState.pathname = '/conversation/frame-owned-by-project';
    await renderWithI18n(<ProjectHistorySection collapsed={false} activeProjectId='proj_example' />, 'en-US');

    fireEvent.click(
      await screen.findByRole('button', {
        name: 'Example project project actions',
      })
    );
    fireEvent.click(screen.getByRole('menuitem', { name: 'Delete project' }));
    fireEvent.click(screen.getByRole('button', { name: 'Delete project', exact: true }));

    await waitFor(() => expect(deleteProjectMock).toHaveBeenCalledWith('proj_example'));
    expect(navigateMock).toHaveBeenCalledWith('/guid', { replace: true });
  });

  it('downloads every visible project artifact from the v1.1 project menu', async () => {
    await renderWithI18n(<ProjectHistorySection collapsed={false} activeProjectId='proj_example' />, 'en-US');
    fireEvent.click(
      await screen.findByRole('button', {
        name: 'Example project project actions',
      })
    );
    fireEvent.click(screen.getByRole('menuitem', { name: 'Download project artifacts' }));

    await waitFor(() => expect(loadProjectArtifactsMock).toHaveBeenCalledWith('proj_example'));
    await waitFor(() =>
      expect(downloadFileFromUrlMock).toHaveBeenCalledWith(
        expect.stringContaining('/api/artifacts/download?'),
        'Example project-artifacts.zip'
      )
    );
    expect(downloadFileFromUrlMock.mock.calls[0][0]).toContain('artifact-1%2Cartifact-2');
  });

  it('portals the project menu above the scrolling sidebar and supports menu keyboard navigation', async () => {
    await renderWithI18n(<ProjectHistorySection collapsed={false} activeProjectId='proj_example' />, 'en-US');
    const trigger = await screen.findByRole('button', {
      name: 'Example project project actions',
    });

    fireEvent.click(trigger);
    const menu = await screen.findByRole('menu', {
      name: 'Example project project actions',
    });
    expect(menu.parentElement).toBe(document.body);
    expect(menu).toHaveStyle({ backgroundColor: '#fff', opacity: '1' });

    fireEvent.keyDown(menu, { key: 'End' });
    expect(screen.getByRole('menuitem', { name: 'Delete project' })).toHaveFocus();
    fireEvent.keyDown(menu, { key: 'Escape' });
    await waitFor(() => expect(trigger).toHaveFocus());
  });

  it('expands project batch controls, selects projects, and deletes only the selected projects', async () => {
    loadProjectsMock.mockResolvedValue([projectFixture, secondProjectFixture]);
    const onBatchModeChange = vi.fn();

    await renderWithI18n(
      <ProjectHistorySection
        collapsed={false}
        activeProjectId='proj_example'
        batchMode
        onBatchModeChange={onBatchModeChange}
      />
    );

    await screen.findByRole('button', { name: 'Example project' });
    expect(screen.getByTestId('projects-batch-panel')).toBeInTheDocument();
    expect(screen.getByTestId('projects-batch-delete')).toBeDisabled();

    fireEvent.click(screen.getByTestId('project-batch-checkbox-proj_example'));
    expect(screen.getByTestId('projects-batch-delete')).toBeEnabled();
    fireEvent.click(screen.getByTestId('projects-batch-delete'));
    fireEvent.click(screen.getByTestId('project-batch-delete-confirm'));

    await waitFor(() => expect(deleteProjectMock).toHaveBeenCalledTimes(1));
    expect(deleteProjectMock).toHaveBeenCalledWith('proj_example');
    expect(deleteProjectMock).not.toHaveBeenCalledWith('proj_second');
    expect(onBatchModeChange).toHaveBeenCalledWith(false);
    expect(navigateMock).toHaveBeenCalledWith('/guid', { replace: true });
  });

  it('reorders only from the pointer handle and persists the resulting ids', async () => {
    loadProjectsMock.mockResolvedValue([projectFixture, secondProjectFixture]);
    await renderWithI18n(<ProjectHistorySection collapsed={false} activeProjectId='proj_example' />);

    const firstHandle = await screen.findByTestId('project-drag-handle-proj_example');
    const secondRow = screen.getByTestId('project-row-proj_second');
    expect(screen.getByRole('button', { name: 'Example project' })).not.toHaveAttribute('draggable');

    fireEvent.dragStart(firstHandle, {
      dataTransfer: { effectAllowed: 'none', setData: vi.fn() },
    });
    fireEvent.dragOver(secondRow);
    fireEvent.drop(secondRow);

    await waitFor(() => expect(saveProjectOrderMock).toHaveBeenCalledWith(['proj_second', 'proj_example']));
    const rows = screen.getAllByTestId(/^project-row-/);
    expect(rows.map((row) => row.getAttribute('data-testid'))).toEqual([
      'project-row-proj_second',
      'project-row-proj_example',
    ]);
  });

  it('supports keyboard reorder from the focused handle without opening the project', async () => {
    loadProjectsMock.mockResolvedValue([projectFixture, secondProjectFixture]);
    await renderWithI18n(<ProjectHistorySection collapsed={false} activeProjectId='proj_example' />);

    const secondHandle = await screen.findByTestId('project-drag-handle-proj_second');
    fireEvent.keyDown(secondHandle, { key: 'ArrowUp' });

    await waitFor(() => expect(saveProjectOrderMock).toHaveBeenCalledWith(['proj_second', 'proj_example']));
    expect(navigateMock).not.toHaveBeenCalled();
    expect(screen.getByText('已将项目“Second project”移动到第 1 位')).toBeInTheDocument();
  });

  it('rolls back the optimistic order and exposes an actionable error when persistence fails', async () => {
    loadProjectsMock.mockResolvedValue([projectFixture, secondProjectFixture]);
    loadProjectOrderMock.mockResolvedValue(['proj_example', 'proj_second']);
    saveProjectOrderMock.mockRejectedValueOnce(new Error('disk full'));
    await renderWithI18n(<ProjectHistorySection collapsed={false} activeProjectId='proj_example' />);

    fireEvent.keyDown(await screen.findByTestId('project-drag-handle-proj_second'), { key: 'ArrowUp' });

    expect(await screen.findByTestId('project-order-error')).toHaveTextContent('已恢复原顺序');
    await waitFor(() => {
      const rows = screen.getAllByTestId(/^project-row-/);
      expect(rows.map((row) => row.getAttribute('data-testid'))).toEqual([
        'project-row-proj_example',
        'project-row-proj_second',
      ]);
    });
  });

  it('serializes rapid writes and rolls back to the last confirmed durable order after consecutive failures', async () => {
    loadProjectsMock.mockResolvedValue([projectFixture, secondProjectFixture]);
    loadProjectOrderMock.mockResolvedValue(['proj_example', 'proj_second']);
    let rejectFirst!: (reason?: unknown) => void;
    let rejectSecond!: (reason?: unknown) => void;
    const firstWrite = new Promise<void>((_resolve, reject) => {
      rejectFirst = reject;
    });
    const secondWrite = new Promise<void>((_resolve, reject) => {
      rejectSecond = reject;
    });
    saveProjectOrderMock.mockReturnValueOnce(firstWrite).mockReturnValueOnce(secondWrite);
    await renderWithI18n(<ProjectHistorySection collapsed={false} activeProjectId='proj_example' />);

    const secondHandle = await screen.findByTestId('project-drag-handle-proj_second');
    fireEvent.keyDown(secondHandle, { key: 'ArrowUp' });
    await waitFor(() => expect(saveProjectOrderMock).toHaveBeenCalledTimes(1));
    fireEvent.keyDown(screen.getByTestId('project-drag-handle-proj_second'), {
      key: 'ArrowDown',
    });
    expect(saveProjectOrderMock).toHaveBeenCalledTimes(1);

    rejectFirst(new Error('first failed'));
    await waitFor(() => expect(saveProjectOrderMock).toHaveBeenCalledTimes(2));
    rejectSecond(new Error('second failed'));

    expect(await screen.findByTestId('project-order-error')).toHaveTextContent('已恢复原顺序');
    await waitFor(() => {
      const rows = screen.getAllByTestId(/^project-row-/);
      expect(rows.map((row) => row.getAttribute('data-testid'))).toEqual([
        'project-row-proj_example',
        'project-row-proj_second',
      ]);
    });
  });

  it('reads the durable order again after a remount and appends a newly discovered project', async () => {
    loadProjectsMock.mockResolvedValue([projectFixture, secondProjectFixture]);
    loadProjectOrderMock.mockResolvedValue(['proj_second']);
    const first = await renderWithI18n(<ProjectHistorySection collapsed={false} activeProjectId='proj_example' />);

    await waitFor(() => expect(saveProjectOrderMock).toHaveBeenCalledWith(['proj_second', 'proj_example']));
    first.unmount();
    saveProjectOrderMock.mockClear();

    await renderWithI18n(<ProjectHistorySection collapsed={false} activeProjectId='proj_example' />);
    await waitFor(() => {
      const rows = screen.getAllByTestId(/^project-row-/);
      expect(rows.map((row) => row.getAttribute('data-testid'))).toEqual([
        'project-row-proj_second',
        'project-row-proj_example',
      ]);
    });
    expect(loadProjectOrderMock).toHaveBeenCalledTimes(2);
  });
});

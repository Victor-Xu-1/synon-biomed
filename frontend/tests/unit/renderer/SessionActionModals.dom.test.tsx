import { fireEvent, screen } from '@testing-library/react';
import type { TChatConversation } from '@/common/config/storage';
import type { SynonBiomedProject } from '@/renderer/services/synonBiomedGateway';
import React from 'react';
import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

const originalConsoleError = console.error;

beforeAll(() => {
  vi.spyOn(console, 'error').mockImplementation((...args: unknown[]) => {
    if (String(args[0]).includes('Accessing element.ref was removed in React 19')) return;
    originalConsoleError(...args);
  });
});

afterAll(() => {
  vi.restoreAllMocks();
});

import SessionEditorModal from '@/renderer/pages/conversation/GroupedHistory/SessionEditorModal';
import SessionMoveModal from '@/renderer/pages/conversation/GroupedHistory/SessionMoveModal';
import SessionNotebookModal from '@/renderer/pages/conversation/GroupedHistory/SessionNotebookModal';

const conversation: TChatConversation = {
  id: 'frame-1',
  type: 'acp',
  name: 'STAT6 analysis',
  desc: 'Structure workflow',
  created_at: 1,
  modified_at: 1,
  status: 'finished',
  extra: { backend: 'synonbiomed', project_id: 'project-a' },
};

const projects: SynonBiomedProject[] = [
  {
    projectId: 'project-a',
    name: 'Current project',
    description: null,
    context: null,
    conversationCount: 1,
    artifactCount: 0,
    createdAt: null,
    updatedAt: null,
    lastActiveAt: null,
  },
  {
    projectId: 'project-b',
    name: 'Target project',
    description: null,
    context: null,
    conversationCount: 0,
    artifactCount: 0,
    createdAt: null,
    updatedAt: null,
    lastActiveAt: null,
  },
];

describe('v1.1 session action modals', () => {
  it('edits both the title and description', async () => {
    const onSubmit = vi.fn();
    await renderWithI18n(
      <SessionEditorModal
        conversation={conversation}
        loading={false}
        error={null}
        onCancel={vi.fn()}
        onSubmit={onSubmit}
      />
    );

    expect(await screen.findByRole('dialog', { name: '编辑任务' })).toHaveStyle({
      width: 'min(520px, calc(100vw - 32px))',
    });
    expect(screen.getByRole('textbox', { name: '标题' })).toHaveValue('STAT6 analysis');
    expect(screen.getByRole('textbox', { name: '说明' })).toHaveValue('Structure workflow');
    fireEvent.change(screen.getByRole('textbox', { name: '标题' }), { target: { value: 'STAT6 updated' } });
    fireEvent.change(screen.getByRole('textbox', { name: '说明' }), { target: { value: 'Updated workflow' } });
    fireEvent.click(screen.getByRole('button', { name: '保存' }));

    expect(onSubmit).toHaveBeenCalledWith({ name: 'STAT6 updated', taskSummary: 'Updated workflow' });
  });

  it('offers every project except the current project and submits the default target', async () => {
    const onSubmit = vi.fn();
    await renderWithI18n(
      <SessionMoveModal
        conversation={conversation}
        projects={projects}
        currentProjectId='project-a'
        loading={false}
        error={null}
        onCancel={vi.fn()}
        onSubmit={onSubmit}
      />
    );

    expect(await screen.findByRole('dialog', { name: '移动任务到项目' })).toHaveStyle({
      width: 'min(520px, calc(100vw - 32px))',
    });
    expect(screen.getByRole('dialog', { name: '移动任务到项目' })).toHaveTextContent('STAT6 analysis');
    expect(screen.queryByText('Current project')).not.toBeInTheDocument();
    expect(screen.getByText('Target project')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '移动' }));
    expect(onSubmit).toHaveBeenCalledWith('project-b');
  });

  it('renders execution cells from the real notebook model', async () => {
    await renderWithI18n(
      <SessionNotebookModal
        conversation={conversation}
        records={[
          {
            kind: 'cell',
            id: 'cell-1',
            cellIndex: 0,
            language: 'python',
            source: 'print("STAT6")',
            stdout: 'STAT6\n',
            stderr: '',
            exitStatus: 'ok',
            errorLine: null,
            kernelKind: 'python',
            environment: 'base',
            filesWritten: [],
            at: null,
          },
        ]}
        loading={false}
        error={null}
        onClose={vi.fn()}
      />,
      'en-US'
    );

    expect(await screen.findByRole('dialog', { name: 'Task notebook' })).toBeInTheDocument();
    expect(screen.getByRole('region', { name: 'Execution cell 1' })).toHaveTextContent('print("STAT6")');
    expect(screen.getByText('STAT6')).toBeInTheDocument();
  });
});

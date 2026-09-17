import React from 'react';
import { fireEvent, screen, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { describe, expect, it, vi } from 'vitest';
import type { SynonBiomedDelegateLineage } from '@/renderer/services/synonBiomedDelegateLineage';
import { SynonBiomedDelegateDockView } from '@/renderer/pages/conversation/components/SynonBiomedDelegateDock';
import { renderWithI18n } from '../i18nTestUtils';

vi.mock('@icon-park/react', () => ({
  Left: () => <span aria-hidden='true'>left</span>,
  Right: () => <span aria-hidden='true'>right</span>,
  Robot: () => <span aria-hidden='true'>robot</span>,
  Down: () => <span aria-hidden='true'>down</span>,
}));
vi.mock('@/renderer/pages/conversation/components/SynonBiomedLineageMessagesDrawer', () => ({
  default: ({
    child,
    onOpenFull,
  }: {
    child: { frameId: string; label: string } | null;
    onOpenFull: (id: string) => void;
  }) =>
    child ? (
      <div role='dialog' aria-label='子任务消息抽屉'>
        <span>{child.label} 消息</span>
        <button onClick={() => onOpenFull(child.frameId)}>打开完整子任务</button>
      </div>
    ) : null,
}));

const frame = (
  frameId: string,
  ordinal: number,
  status: 'needs-input' | 'running' | 'failed' | 'completed' | 'stopped',
  directChildCount = 0
) => ({
  frameId,
  rootFrameId: 'root',
  parentFrameId: frameId === 'root' ? null : 'root',
  ordinal,
  label: frameId,
  agentName: 'RESEARCHER',
  status,
  statusDescription: status === 'needs-input' ? '请选择实验类型' : null,
  taskSummary: null,
  messageCount: ordinal + 1,
  directChildCount,
});

describe('SynonBiomedDelegateDock', () => {
  it('shows parallel work as a prioritized v1.1-style summary and previews a child before opening it', async () => {
    const lineage: SynonBiomedDelegateLineage = {
      rootFrameId: 'root',
      currentFrameId: 'root',
      current: frame('root', 0, 'running'),
      ancestors: [],
      directChildren: [
        frame('done-child', 1, 'completed'),
        frame('running-child', 2, 'running', 1),
        frame('question-child', 4, 'needs-input'),
      ],
      rootChildren: [],
      totals: { needsInput: 1, running: 1, failed: 0, completed: 1, stopped: 0 },
    };

    await renderWithI18n(
      <MemoryRouter initialEntries={['/conversation/root']}>
        <Routes>
          <Route path='/conversation/root' element={<SynonBiomedDelegateDockView lineage={lineage} />} />
          <Route path='/conversation/:frameId' element={<div>子任务已打开</div>} />
        </Routes>
      </MemoryRouter>
    );

    fireEvent.click(screen.getByRole('button', { name: '3 个子任务，打开列表' }));
    const list = screen.getByRole('list', { name: '子任务' });
    const rows = within(list).getAllByRole('button');
    expect(rows.map((row) => row.textContent)).toEqual([
      expect.stringContaining('子 Agent 4 · question-child'),
      expect.stringContaining('子 Agent 2 · running-child'),
      expect.stringContaining('子 Agent 1 · done-child'),
    ]);
    expect(rows[1]).toHaveTextContent('1 个子任务');

    fireEvent.click(rows[0]);
    expect(screen.getByRole('dialog', { name: '子任务消息抽屉' })).toHaveTextContent('question-child 消息');
    fireEvent.click(screen.getByRole('button', { name: '打开完整子任务' }));
    expect(screen.getByText('子任务已打开')).toBeInTheDocument();
  });

  it('shows a direct parent action and the full ancestor path inside a nested child', async () => {
    const root = frame('root', 0, 'running');
    const parent = { ...frame('parent', 1, 'running'), parentFrameId: 'root' };
    const current = { ...frame('grandchild', 2, 'completed'), parentFrameId: 'parent' };
    const lineage: SynonBiomedDelegateLineage = {
      rootFrameId: 'root',
      currentFrameId: 'grandchild',
      current,
      ancestors: [root, parent],
      directChildren: [],
      rootChildren: [],
      totals: { needsInput: 0, running: 0, failed: 0, completed: 0, stopped: 0 },
    };

    await renderWithI18n(
      <MemoryRouter initialEntries={['/conversation/grandchild']}>
        <Routes>
          <Route path='/conversation/grandchild' element={<SynonBiomedDelegateDockView lineage={lineage} />} />
          <Route path='/conversation/:frameId' element={<div>父任务已打开</div>} />
        </Routes>
      </MemoryRouter>
    );

    expect(screen.getByLabelText('任务层级')).toHaveTextContent('root/parent/grandchild');
    fireEvent.click(screen.getByRole('button', { name: '返回父任务 parent' }));
    expect(screen.getByText('父任务已打开')).toBeInTheDocument();
  });

  it('renders one child as a direct compact action without a popover in English', async () => {
    const child = frame('only-child', 1, 'running');
    const lineage: SynonBiomedDelegateLineage = {
      rootFrameId: 'root',
      currentFrameId: 'root',
      current: frame('root', 0, 'running'),
      ancestors: [],
      directChildren: [child],
      rootChildren: [child],
      totals: { needsInput: 0, running: 1, failed: 0, completed: 0, stopped: 0 },
    };

    await renderWithI18n(
      <MemoryRouter>
        <SynonBiomedDelegateDockView lineage={lineage} />
      </MemoryRouter>,
      'en-US'
    );

    expect(screen.getByRole('button', { name: 'Open subagent 1 only-child' })).toHaveTextContent('Running');
    expect(screen.queryByRole('list', { name: 'Subagents' })).not.toBeInTheDocument();
  });
});

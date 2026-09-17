import { fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

const loadMessages = vi.fn();
vi.mock('@/renderer/services/synonBiomedLineageMessages', () => ({
  loadSynonBiomedLineageMessages: (...args: unknown[]) => loadMessages(...args),
}));
vi.mock('@/renderer/hooks/context/LayoutContext', () => ({
  useLayoutContext: () => ({ isMobile: false }),
}));
vi.mock('@/renderer/components/Markdown', () => ({
  default: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));

import SynonBiomedLineageMessagesDrawer from '@/renderer/pages/conversation/components/SynonBiomedLineageMessagesDrawer';

const child = {
  frameId: 'child-1',
  rootFrameId: 'root',
  parentFrameId: 'root',
  ordinal: 1,
  label: 'literature-review',
  agentName: 'RESEARCHER',
  status: 'completed' as const,
  statusDescription: null,
  taskSummary: 'Review the evidence',
  messageCount: 3,
  directChildCount: 0,
};

describe('SynonBiomedLineageMessagesDrawer', () => {
  it('renders child text, thinking, tool summaries, and the full-task action', async () => {
    loadMessages.mockResolvedValue({
      items: [
        {
          id: 'u',
          kind: 'text',
          position: 'right',
          content: 'Review this',
          createdAt: 1,
        },
        {
          id: 'a',
          kind: 'text',
          position: 'left',
          content: '## Findings',
          createdAt: 2,
        },
        {
          id: 't',
          kind: 'thinking',
          position: 'left',
          content: 'Checking',
          createdAt: 3,
        },
        {
          id: 'x',
          kind: 'tool',
          position: 'left',
          content: 'Searching · completed',
          createdAt: 4,
        },
      ],
      hasMoreBefore: false,
    });
    const onOpenFull = vi.fn();

    await renderWithI18n(<SynonBiomedLineageMessagesDrawer child={child} onClose={vi.fn()} onOpenFull={onOpenFull} />);

    expect(await screen.findByText('Review this')).toBeInTheDocument();
    expect(screen.getByText('你')).toBeInTheDocument();
    expect(screen.getByText('思考')).toBeInTheDocument();
    expect(screen.getByText('工具')).toBeInTheDocument();
    expect(screen.getByText('## Findings')).toBeInTheDocument();
    expect(screen.getByText('Searching · completed')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '打开完整子任务' }));
    expect(onOpenFull).toHaveBeenCalledWith('child-1');
    await waitFor(() =>
      expect(loadMessages).toHaveBeenCalledWith('child-1', expect.objectContaining({ signal: expect.any(AbortSignal) }))
    );
  });

  it('renders the child transcript chrome in English', async () => {
    loadMessages.mockResolvedValue({
      items: [
        { id: 'u-en', kind: 'text', position: 'right', content: 'Review this', createdAt: 1 },
        { id: 't-en', kind: 'thinking', position: 'left', content: 'Checking', createdAt: 2 },
        { id: 'x-en', kind: 'tool', position: 'left', content: '', createdAt: 3 },
      ],
      hasMoreBefore: false,
    });

    await renderWithI18n(
      <SynonBiomedLineageMessagesDrawer child={child} onClose={vi.fn()} onOpenFull={vi.fn()} />,
      'en-US'
    );

    expect(await screen.findByText('You')).toBeInTheDocument();
    expect(screen.getByText('Thinking')).toBeInTheDocument();
    expect(screen.getByText('Tool')).toBeInTheDocument();
    expect(screen.getByText('Tool call')).toBeInTheDocument();
    expect(screen.getByText('Child Agent 1 · literature-review')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Open full child task' })).toBeInTheDocument();
  });
});

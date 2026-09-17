import React from 'react';
import { fireEvent, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { describe, expect, it, vi } from 'vitest';
import type { IMessageToolCall } from '@/common/chat/chatLib';
import MessageSubagentTurn from '@/renderer/pages/conversation/Messages/components/MessageSubagentTurn';
import { renderWithI18n } from '../i18nTestUtils';

vi.mock('@icon-park/react', () => ({
  Right: () => <span aria-hidden='true'>right</span>,
  Robot: () => <span aria-hidden='true'>robot</span>,
}));

const message = {
  id: 'delegate-message',
  conversation_id: 'frame-parent',
  type: 'tool_call',
  position: 'left',
  content: {
    call_id: 'call-delegate',
    name: 'delegate',
    args: { task: 'Find CRBN ligands' },
    status: 'running',
    description: 'Delegating CRBN literature research',
    subagent: {
      ordinal: 1,
      frameId: 'frame-child',
      rootFrameId: 'frame-parent',
      parentFrameId: 'frame-parent',
      agentName: 'RESEARCHER',
      delegateName: 'Literature research',
      status: 'processing',
      messageCount: 4,
      latestAction: 'Searching CRBN ligand structures',
    },
  },
} as IMessageToolCall;

describe('MessageSubagentTurn', () => {
  it('shows the child status and opens the real child conversation', async () => {
    await renderWithI18n(
      <MemoryRouter initialEntries={['/conversation/frame-parent']}>
        <Routes>
          <Route path='/conversation/frame-parent' element={<MessageSubagentTurn message={message} />} />
          <Route path='/conversation/:frameId' element={<div>child conversation opened</div>} />
        </Routes>
      </MemoryRouter>,
      'zh-CN'
    );

    expect(screen.getByText('子 Agent 1 · Literature research')).toBeInTheDocument();
    expect(screen.getByText('Searching CRBN ligand structures')).toBeInTheDocument();
    expect(screen.getByText('运行中 · 4 条消息')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '打开子 Agent 1 Literature research' }));
    expect(screen.getByText('child conversation opened')).toBeInTheDocument();
  });

  it('keeps an unlinked delegation visible but disables navigation', async () => {
    const unlinked = {
      ...message,
      content: {
        ...message.content,
        subagent: { ordinal: 2, delegateName: 'Structure search', status: 'processing' },
      },
    } as IMessageToolCall;

    await renderWithI18n(
      <MemoryRouter>
        <MessageSubagentTurn message={unlinked} />
      </MemoryRouter>,
      'en-US'
    );

    expect(
      screen.getByRole('button', { name: 'Subagent 2 Structure search is being created', disabled: true })
    ).toBeDisabled();
  });

  it('does not invent a user-waiting state from a legacy child status hint', async () => {
    const legacyWaiting = {
      ...message,
      content: {
        ...message.content,
        subagent: { ...message.content.subagent, status: 'awaiting_user_response' },
      },
    } as IMessageToolCall;

    await renderWithI18n(
      <MemoryRouter>
        <MessageSubagentTurn message={legacyWaiting} />
      </MemoryRouter>,
      'zh-CN'
    );

    const child = screen.getByRole('button', { name: '打开子 Agent 1 Literature research' });
    expect(child).toHaveAttribute('data-subagent-state', 'running');
    expect(screen.getByText('运行中 · 4 条消息')).toBeInTheDocument();
    expect(screen.queryByText(/等待/)).not.toBeInTheDocument();
  });
});

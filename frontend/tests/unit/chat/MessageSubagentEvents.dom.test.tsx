import React from 'react';
import { fireEvent, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { describe, expect, it, vi } from 'vitest';
import type { IMessageToolCall } from '@/common/chat/chatLib';
import MessageSubagentEvents from '@/renderer/pages/conversation/Messages/components/MessageSubagentEvents';
import { renderWithI18n } from '../i18nTestUtils';

vi.mock('@icon-park/react', () => ({
  Attention: () => <span aria-hidden='true'>warning</span>,
  CheckOne: () => <span aria-hidden='true'>check</span>,
  MessageOne: () => <span aria-hidden='true'>message</span>,
  Right: () => <span aria-hidden='true'>right</span>,
  Robot: () => <span aria-hidden='true'>robot</span>,
  Warning: () => <span aria-hidden='true'>warning</span>,
}));

const message = {
  id: 'subagent-events',
  conversation_id: 'frame-parent',
  type: 'tool_call',
  position: 'left',
  content: {
    call_id: 'subagent-events',
    name: 'subagent_events',
    args: {},
    status: 'completed',
    subagentEvents: [
      {
        kind: 'completion',
        frameId: 'frame-child',
        ordinal: 1,
        childName: 'Literature research',
        bullets: ['Found three CRBN ligand structures', 'Ranked the supporting evidence'],
        wallSeconds: 12,
      },
      {
        kind: 'info',
        frameId: 'frame-child',
        ordinal: 1,
        childName: 'Literature research',
        text: 'The pocket annotation is ready.',
      },
      {
        kind: 'question',
        frameId: 'frame-child',
        ordinal: 1,
        childName: 'Literature research',
        text: 'Which assay should I prioritize?',
      },
    ],
  },
} as IMessageToolCall;

describe('MessageSubagentEvents', () => {
  it('groups completion and info rows while surfacing questions immediately', async () => {
    await renderWithI18n(
      <MemoryRouter initialEntries={['/conversation/frame-parent']}>
        <Routes>
          <Route path='/conversation/frame-parent' element={<MessageSubagentEvents message={message} />} />
          <Route path='/conversation/:frameId' element={<div>child conversation opened</div>} />
        </Routes>
      </MemoryRouter>,
      'en-US'
    );

    expect(screen.getByRole('button', { name: '1 message · 1 subagent finished' })).toHaveAttribute(
      'aria-expanded',
      'false'
    );
    expect(screen.queryByText('Found three CRBN ligand structures')).not.toBeInTheDocument();
    expect(screen.getByText('Literature research asked a question')).toBeInTheDocument();
    expect(screen.getByText('Which assay should I prioritize?')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '1 message · 1 subagent finished' }));
    expect(screen.getByText('Found three CRBN ligand structures')).toBeInTheDocument();
    expect(screen.getByText('Ranked the supporting evidence')).toBeInTheDocument();
    expect(screen.getByText('The pocket annotation is ready.')).toBeInTheDocument();
    expect(screen.getByText('12s')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Open question from Literature research' }));
    expect(screen.getByText('child conversation opened')).toBeInTheDocument();
  });
});

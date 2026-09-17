import { fireEvent, screen, within } from '@testing-library/react';
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { IMessageToolCall } from '@/common/chat/chatLib';
import MessageToolCall from '@/renderer/pages/conversation/Messages/components/MessageToolCall';
import { conversationDisclosureStore } from '@/renderer/services/runtime/conversationDisclosureStore';
import { renderWithI18n } from '../i18nTestUtils';

vi.mock('@/renderer/components/base/FileChangesPanel', () => ({ default: () => null }));
vi.mock('@/renderer/hooks/file/useDiffPreviewHandlers', () => ({
  useDiffPreviewHandlers: () => ({ handleFileClick: vi.fn(), handleDiffClick: vi.fn() }),
}));
vi.mock('@/renderer/components/synonBiomed/runtime/AskUserHistoryCard', () => ({ default: () => null }));
vi.mock('@/renderer/pages/conversation/Messages/components/MessageSubagentTurn', () => ({ default: () => null }));
vi.mock('@/renderer/pages/conversation/Messages/components/MessageSubagentEvents', () => ({ default: () => null }));

const message = {
  id: 'tool-message-1',
  msg_id: 'tool-message-1',
  conversation_id: 'conversation-1',
  type: 'tool_call',
  position: 'left',
  content: {
    call_id: 'call-1',
    name: 'search_records',
    args: { query: 'STAT6' },
    status: 'running',
    output: '3 records found',
  },
} as IMessageToolCall;

describe('MessageToolCall', () => {
  beforeEach(() => conversationDisclosureStore.resetForTests());

  it('routes an inferred search call through the canonical research summary', async () => {
    await renderWithI18n(<MessageToolCall message={message} />, 'en-US');

    const toggle = screen.getByTestId('tool-chip');
    expect(toggle).not.toBeDisabled();
    expect(toggle).toHaveAccessibleName(/Search sources.*Running/i);
    expect(toggle).toHaveAttribute('aria-expanded', 'false');
    expect(screen.queryByRole('region', { name: 'Sources' })).not.toBeInTheDocument();
    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute('aria-expanded', 'true');
    expect(toggle).not.toHaveTextContent('search_records');
    expect(screen.queryByTestId('tool-technical-detail')).not.toBeInTheDocument();

    const sources = screen.getByRole('region', { name: 'Sources' });
    expect(within(sources).getByText('query')).toBeInTheDocument();
    expect(within(sources).getByText('STAT6')).toBeInTheDocument();
    expect(within(sources).getByText(/No usable sources were found/)).toBeInTheDocument();
    expect(screen.getByTestId('show-output-toggle')).toHaveAttribute('aria-expanded', 'false');
    fireEvent.click(screen.getByTestId('show-output-toggle'));
    expect(screen.getByText('3 records found')).toBeInTheDocument();

    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute('aria-expanded', 'false');
    expect(screen.queryByRole('region', { name: 'Sources' })).not.toBeInTheDocument();
  });

  it('announces a canceled transcript tool without presenting it as pending', async () => {
    await renderWithI18n(
      <MessageToolCall
        message={{ ...message, content: { ...message.content, status: 'canceled', output: undefined } }}
      />,
      'en-US'
    );

    expect(screen.getByTestId('tool-chip')).toHaveTextContent('Stopped');
    expect(screen.queryByText('Pending')).not.toBeInTheDocument();
  });
});

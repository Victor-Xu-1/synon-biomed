import type { IMessageText } from '@/common/chat/chatLib';
import MessageText from '@/renderer/pages/conversation/Messages/components/MessageText';
import { act, fireEvent, screen } from '@testing-library/react';
import React from 'react';
import { describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

const copyTextMock = vi.hoisted(() => vi.fn().mockResolvedValue(undefined));

vi.mock('@/renderer/utils/ui/clipboard', () => ({
  copyText: copyTextMock,
}));

vi.mock('@/renderer/hooks/context/ConversationContext', () => ({
  useConversationContextSafe: () => ({
    conversation_id: 'conversation-1',
    currentFrameId: 'frame-1',
    projectId: 'project-1',
    workspace: 'synonbiomed://project-1',
  }),
}));

vi.mock('@/renderer/hooks/context/LayoutContext', () => ({
  useLayoutContext: () => ({ isMobile: false }),
}));

vi.mock('@/renderer/pages/conversation/Preview/hooks/useLocalFilePreview', () => ({
  useLocalFilePreview: () => vi.fn(),
}));

vi.mock('@/renderer/pages/conversation/Messages/useSynonBiomedArtifactLinkPreview', () => ({
  useSynonBiomedArtifactResolver: () => ({ handleLink: vi.fn(), resolveImage: vi.fn() }),
}));

vi.mock('@renderer/components/Markdown', () => ({
  default: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
}));

const message: IMessageText = {
  id: 'assistant-text-1',
  msg_id: 'assistant-text-1',
  conversation_id: 'conversation-1',
  type: 'text',
  position: 'left',
  created_at: 1,
  content: { content: 'Intermediate streaming text.' },
};

describe('MessageText final-turn actions', () => {
  it('does not reserve or render an action row for intermediate assistant text', async () => {
    await renderWithI18n(<MessageText message={message} showCopyRow={false} />);

    expect(screen.queryByTestId('message-text-actions')).not.toBeInTheDocument();
    expect(screen.queryByTestId('transcript-actions')).not.toBeInTheDocument();
  });

  it('keeps only the copy action on the final assistant text', async () => {
    await renderWithI18n(<MessageText message={{ ...message, id: 'assistant-final' }} showCopyRow />);

    const actions = screen.getByTestId('message-text-actions');
    const copyButton = actions.querySelector('button');

    expect(copyButton).toBeTruthy();
    expect(actions.querySelectorAll('button')).toHaveLength(1);
    expect(actions).not.toHaveTextContent('20:42');
    expect(screen.queryByRole('button', { name: /note/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /annotation/i })).not.toBeInTheDocument();

    await act(async () => {
      fireEvent.click(copyButton as HTMLButtonElement);
    });
    expect(copyTextMock).toHaveBeenCalledWith('Intermediate streaming text.');
  });

  it('keeps partial text visible without duplicating the task failure capsule', async () => {
    await renderWithI18n(
      <MessageText message={{ ...message, status: 'error', terminal_status: 'failed' }} showCopyRow />
    );

    expect(screen.getByTestId('message-text-content')).toHaveTextContent('Intermediate streaming text.');
    expect(screen.queryByTestId('assistant-message-failure')).not.toBeInTheDocument();
  });
});

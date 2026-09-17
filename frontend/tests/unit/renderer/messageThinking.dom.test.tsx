/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import React from 'react';
import { fireEvent, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import type { IMessageThinking } from '@/common/chat/chatLib';
import MessageThinking from '@/renderer/pages/conversation/Messages/components/MessageThinking';
import { renderWithI18n } from '../i18nTestUtils';

function createThinkingMessage(createdAt: number): IMessageThinking {
  return {
    id: 'thinking-1',
    type: 'thinking',
    msg_id: 'msg-1',
    conversation_id: 'conversation-1',
    position: 'left',
    created_at: createdAt,
    content: {
      content: 'analyzing',
      status: 'thinking',
    },
  };
}

describe('MessageThinking', () => {
  it('uses the collapsed workspace thinking preview and preserves the mounted body', async () => {
    const message = createThinkingMessage(Date.now());
    message.content.content = `${'analyzing evidence '.repeat(8)}final detail`;
    await renderWithI18n(<MessageThinking message={message} />, 'en-US');

    const toggle = screen.getByRole('button', { name: /Thinking/ });
    expect(toggle).toHaveAttribute('aria-expanded', 'false');
    expect(screen.getByTestId('thinking-preview')).toHaveTextContent('...');
    const body = screen.getByTestId('thinking-body');
    expect(body).toHaveAttribute('aria-hidden', 'true');

    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute('aria-expanded', 'true');
    expect(body).toHaveAttribute('aria-hidden', 'false');
    expect(screen.getByText(/final detail/)).toBeInTheDocument();

    fireEvent.click(toggle);
    expect(body).toHaveAttribute('aria-hidden', 'true');
    expect(screen.getByText(/final detail/)).toBeInTheDocument();
  });

  it('localizes the thinking label for Chinese conversations', async () => {
    await renderWithI18n(<MessageThinking message={createThinkingMessage(Date.now())} />, 'zh-CN');
    expect(screen.getByRole('button', { name: /思考/ })).toBeInTheDocument();
  });
});

/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { screen } from '@testing-library/react';
import React from 'react';
import { describe, expect, it, vi } from 'vitest';
import type { TChatConversation } from '@/common/config/storage';
import { renderWithI18n } from '../../i18nTestUtils';

vi.mock('@/common', () => ({
  ipcBridge: {},
}));

vi.mock('@/renderer/hooks/synonBiomed/runtime/usePresetAssistantInfo', () => ({
  usePresetAssistantInfo: () => ({
    info: {
      assistantId: 'assistant-test',
      name: 'Synon Biomed',
      logo: undefined,
      isEmoji: false,
      isFallback: false,
    },
    isLoading: false,
  }),
}));

vi.mock('@/renderer/pages/conversation/components/ChatLayout', () => ({
  default: ({ children }: { children?: React.ReactNode }) => <div data-testid='chat-layout'>{children}</div>,
}));

vi.mock('@/renderer/pages/conversation/platforms/acp/AcpChat', () => ({
  default: () => null,
}));

vi.mock('@/renderer/pages/conversation/hooks/useActiveLease', () => ({
  useActiveLease: () => undefined,
}));

vi.mock('@/renderer/pages/conversation/utils/conversationCache', () => ({
  getConversationOrNull: vi.fn(),
}));

vi.mock('@/renderer/pages/conversation/utils/conversationCreateError', () => ({
  getConversationCreateErrorMessage: vi.fn(),
}));

vi.mock('@/renderer/pages/conversation/utils/conversationAssistantIdentity', () => ({
  resolveConversationBackend: () => 'synonbiomed',
}));

import ChatConversation from '@/renderer/pages/conversation/components/ChatConversation';

const conversation = {
  id: 'conversation-test',
  name: '测试会话',
  type: 'acp',
  extra: {
    workspace: 'synonbiomed://workspace-test',
  },
} as TChatConversation;

describe('ChatConversation workspace panel', () => {
  it('does not render project or notebook tabs above the file-first workspace', async () => {
    await renderWithI18n(<ChatConversation conversation={conversation} />);

    expect(screen.getByTestId('chat-layout')).toBeInTheDocument();
    expect(screen.queryByRole('tab', { name: '项目' })).not.toBeInTheDocument();
    expect(screen.queryByRole('tab', { name: '笔记本' })).not.toBeInTheDocument();
  });
});

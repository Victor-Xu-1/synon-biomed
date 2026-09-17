/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { cleanup, render, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => key,
  }),
}));

vi.mock('@arco-design/web-react', () => {
  const Layout = ({ children }: { children: React.ReactNode }) => <section>{children}</section>;
  Layout.Content = ({ children }: { children: React.ReactNode }) => <main>{children}</main>;
  return { Layout };
});

vi.mock('@/renderer/hooks/context/LayoutContext', () => ({
  useLayoutContext: () => ({ isMobile: false }),
}));

vi.mock('@/renderer/hooks/ui/useResizableSplit', () => ({
  useResizableSplit: ({ unit }: { unit?: string }) => ({
    splitRatio: unit === 'px' ? 260 : 60,
    setSplitRatio: vi.fn(),
    createDragHandle: () => <div data-testid={unit === 'px' ? 'workspace-drag-handle' : 'preview-drag-handle'} />,
  }),
}));

vi.mock('@/renderer/pages/conversation/hooks/useContainerWidth', () => ({
  useContainerWidth: () => ({
    containerRef: { current: null },
    containerWidth: 1200,
  }),
}));

vi.mock('@/renderer/pages/conversation/hooks/useLayoutConstraints', () => ({
  useLayoutConstraints: vi.fn(),
}));

vi.mock('@/renderer/pages/conversation/hooks/useWorkspaceCollapse', () => ({
  useWorkspaceCollapse: () => ({
    rightSiderCollapsed: true,
    setRightSiderCollapsed: vi.fn(),
  }),
}));

vi.mock('@/renderer/pages/conversation/Preview/context/PreviewContext', () => ({
  usePreviewContext: () => ({ isOpen: true, presentationMode: 'board' }),
}));

vi.mock('@/renderer/pages/conversation/components/ChatLayout/MobileWorkspaceOverlay', () => ({
  default: () => null,
}));

vi.mock('@/renderer/pages/conversation/components/ChatLayout/WorkspacePanelHeader', () => ({
  default: () => null,
  DesktopWorkspaceToggle: () => null,
}));

vi.mock('@/renderer/pages/conversation/Preview/components/PreviewPanel/PreviewPanel', () => ({
  default: () => <div data-testid='preview-panel-mock' />,
}));

vi.mock('@/renderer/pages/conversation/utils/detectPlatform', () => ({
  isMacEnvironment: () => false,
  isWindowsEnvironment: () => false,
}));

vi.mock('@/renderer/utils/platform', () => ({
  isElectronDesktop: () => false,
}));

vi.mock('@/renderer/utils/workspace/workspaceEvents', () => ({
  dispatchWorkspaceToggleEvent: vi.fn(),
}));

vi.mock('@/renderer/utils/workspace/workspaceToggleOwnership', () => ({
  resolveWorkspaceToggleOwner: () => 'conversation-header',
}));

import ChatLayout from '@/renderer/pages/conversation/components/ChatLayout';

describe('ChatLayout preview split', () => {
  afterEach(() => {
    cleanup();
  });

  it('keeps the conversation visible beside a multi-file preview board on desktop', async () => {
    render(
      <ChatLayout workspaceEnabled={false} sider={<div data-testid='workspace-sider' />}>
        <div data-testid='conversation-content'>Conversation content</div>
      </ChatLayout>
    );

    const conversationContent = screen.getByTestId('conversation-content');
    const chatArea = conversationContent.closest('main')?.parentElement;
    expect(chatArea).toHaveStyle({ display: 'flex' });

    await waitFor(() => expect(screen.getByTestId('preview-panel-mock')).toBeInTheDocument());
    expect(screen.getByTestId('preview-drag-handle')).toBeInTheDocument();
  });

  it('reserves a safe desktop gap so preview resizing cannot cover conversation text', async () => {
    render(
      <ChatLayout workspaceEnabled={false} sider={<div data-testid='workspace-sider' />}>
        <div data-testid='conversation-content'>Conversation content</div>
      </ChatLayout>
    );

    const conversationContent = screen.getByTestId('conversation-content');
    const chatArea = conversationContent.closest('main')?.parentElement;
    const splitRow = chatArea?.parentElement;

    expect(chatArea).toHaveStyle({ minWidth: '360px' });
    expect(splitRow).toHaveStyle({ columnGap: '24px' });
  });
});

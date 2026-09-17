import DesktopConversationTitle from '@/renderer/components/layout/Titlebar/DesktopConversationTitle';
import { Message } from '@arco-design/web-react';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { SWRConfig } from 'swr';

const { getConversation, updateConversation } = vi.hoisted(() => ({
  getConversation: vi.fn(),
  updateConversation: vi.fn(),
}));

vi.mock('@/common', () => ({
  ipcBridge: {
    conversation: {
      get: { invoke: getConversation },
      update: { invoke: updateConversation },
    },
  },
}));

vi.mock('@/renderer/pages/conversation/utils/conversationCache', () => ({
  getConversationOrNull: getConversation,
  refreshConversationCache: vi.fn(),
}));

vi.mock('@/renderer/utils/emitter', () => ({
  emitter: { emit: vi.fn() },
}));

let resizeCallback: ResizeObserverCallback | undefined;

class ResizeObserverMock {
  constructor(callback: ResizeObserverCallback) {
    resizeCallback = callback;
  }

  observe() {}
  disconnect() {}
  unobserve() {}
}

const renderTitle = () =>
  render(
    <SWRConfig value={{ provider: () => new Map(), dedupingInterval: 0 }}>
      <DesktopConversationTitle conversationId='conversation-1' fallbackTitle='Synon Biomed' />
    </SWRConfig>
  );

describe('DesktopConversationTitle', () => {
  beforeEach(() => {
    vi.stubGlobal('ResizeObserver', ResizeObserverMock);
    vi.spyOn(Message, 'success').mockImplementation(() => undefined as never);
    vi.spyOn(Message, 'error').mockImplementation(() => undefined as never);
    getConversation.mockResolvedValue({ id: 'conversation-1', name: 'A complete scientific conversation title' });
    updateConversation.mockResolvedValue(true);
  });

  afterEach(() => {
    cleanup();
    document.body.replaceChildren();
    resizeCallback = undefined;
    vi.clearAllMocks();
    vi.unstubAllGlobals();
  });

  it('keeps an overflowing title on one line and fades its right edge without a plus control', async () => {
    renderTitle();
    const titleButton = await screen.findByRole('button', { name: 'A complete scientific conversation title' });
    const titleText = titleButton.querySelector('.app-titlebar-conversation__text') as HTMLSpanElement;
    Object.defineProperty(titleText, 'clientWidth', { configurable: true, value: 100 });
    Object.defineProperty(titleText, 'scrollWidth', { configurable: true, value: 260 });
    act(() => resizeCallback?.([], {} as ResizeObserver));

    expect(titleButton).toHaveClass('app-titlebar-conversation--overflowing');
    expect(titleButton).not.toHaveTextContent('+');
  });

  it('edits in place and saves automatically when focus moves elsewhere', async () => {
    renderTitle();
    fireEvent.click(await screen.findByRole('button', { name: 'A complete scientific conversation title' }));

    const input = await screen.findByRole('textbox');
    fireEvent.change(input, { target: { value: 'Edited scientific title' } });
    fireEvent.blur(input);

    await waitFor(() => {
      expect(updateConversation).toHaveBeenCalledWith({
        id: 'conversation-1',
        updates: { name: 'Edited scientific title' },
      });
    });
  });

  it('tracks the active chat surface width and horizontal position', async () => {
    const titlebar = document.createElement('div');
    titlebar.className = 'app-titlebar';
    titlebar.getBoundingClientRect = () => ({ left: 20, width: 1200, height: 36 }) as DOMRect;
    const surface = document.createElement('div');
    surface.className = 'chat-surface-fluid';
    surface.getBoundingClientRect = () => ({ left: 260, width: 820, height: 140 }) as DOMRect;
    document.body.append(titlebar, surface);

    renderTitle();
    const titleButton = await screen.findByRole('button', { name: 'A complete scientific conversation title' });
    await waitFor(() => {
      expect(titleButton).toHaveStyle({ left: '240px', width: '820px' });
    });
  });
});

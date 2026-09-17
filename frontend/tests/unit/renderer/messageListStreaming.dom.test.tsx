/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import React, { type PropsWithChildren } from 'react';
import { act, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { IMessageText } from '@/common/chat/chatLib';
import {
  MessageListProvider,
  MessagePaginationProvider,
  useMergeLiveMessage,
} from '@/renderer/pages/conversation/Messages/hooks';
import {
  createDefaultConversationRuntimeView,
  type ConversationRuntimeView,
} from '@/renderer/pages/conversation/runtime/conversationRuntimeViewStore';
import MessageList from '@/renderer/pages/conversation/Messages/MessageList';
const activityHarness = vi.hoisted(() => ({
  view: undefined as ConversationRuntimeView | undefined,
  connection: 'connected',
}));
vi.mock('@/renderer/hooks/context/RealtimeContext', () => ({
  useRealtime: () => ({ snapshot: { status: activityHarness.connection } }),
}));

const virtuosoHarness = vi.hoisted(() => ({
  notifyBottomChanged: null as null | ((atBottom: boolean) => void),
  followOutput: null as null | ((atBottom: boolean) => false | 'auto' | 'smooth'),
  initialTopMostItemIndex: null as null | number,
  atBottomThreshold: null as null | number,
  scrollTo: vi.fn(),
  scrollIntoView: vi.fn(),
  scrollToIndex: vi.fn(),
}));

const messageTextHarness = vi.hoisted(() => ({
  props: [] as Array<{ messageId: string; showCopyRow?: boolean }>,
}));

vi.mock('react-virtuoso', async () => {
  const ReactModule = await vi.importActual<typeof import('react')>('react');
  const Virtuoso = ReactModule.forwardRef<
    {
      scrollIntoView: typeof virtuosoHarness.scrollIntoView;
      scrollTo: typeof virtuosoHarness.scrollTo;
      scrollToIndex: typeof virtuosoHarness.scrollToIndex;
    },
    Record<string, any>
  >((props, forwardedRef) => {
    const scrollerRef = ReactModule.useRef<HTMLDivElement>(null);
    ReactModule.useImperativeHandle(
      forwardedRef,
      () => ({
        scrollIntoView: virtuosoHarness.scrollIntoView,
        scrollTo: virtuosoHarness.scrollTo,
        scrollToIndex: virtuosoHarness.scrollToIndex,
      }),
      []
    );
    ReactModule.useLayoutEffect(() => {
      props.scrollerRef?.(scrollerRef.current);
      return () => props.scrollerRef?.(null);
    }, [props.scrollerRef]);
    virtuosoHarness.notifyBottomChanged = (atBottom) => props.atBottomStateChange?.(atBottom);
    virtuosoHarness.followOutput = props.followOutput ?? null;
    virtuosoHarness.initialTopMostItemIndex = props.initialTopMostItemIndex ?? null;
    virtuosoHarness.atBottomThreshold = props.atBottomThreshold ?? null;
    const Header = props.components?.Header;
    const Footer = props.components?.Footer;
    const List = props.components?.List ?? 'div';
    return ReactModule.createElement(
      'div',
      {
        ref: scrollerRef,
        className: props.className,
        style: props.style,
        'data-testid': props['data-testid'],
        onPointerDown: props.onPointerDown,
        onScroll: props.onScroll,
        onWheel: props.onWheel,
      },
      Header ? ReactModule.createElement(Header, { context: props.context }) : null,
      ReactModule.createElement(
        List,
        { context: props.context },
        (props.data ?? []).map((item: unknown, offset: number) =>
          ReactModule.createElement(
            'div',
            { key: props.computeItemKey?.(props.firstItemIndex + offset, item) ?? offset },
            props.itemContent?.(props.firstItemIndex + offset, item, props.context)
          )
        )
      ),
      Footer ? ReactModule.createElement(Footer, { context: props.context }) : null
    );
  });
  Virtuoso.displayName = 'TestVirtuoso';
  return { Virtuoso };
});

vi.mock('react-i18next', () => {
  const t = (_key: string, options?: { defaultValue?: string }) => options?.defaultValue ?? _key;
  return { useTranslation: () => ({ t }) };
});

vi.mock('react-router', () => ({
  useLocation: () => ({
    key: 'location-key',
    state: {},
  }),
}));

vi.mock('@arco-design/web-react', () => ({
  Image: {
    PreviewGroup: ({ children }: PropsWithChildren) => <>{children}</>,
  },
}));

vi.mock('@/renderer/hooks/context/ConversationContext', () => ({
  useConversationContextSafe: () => null,
}));

vi.mock('@/renderer/pages/conversation/runtime/useConversationRuntimeView', () => ({
  useConversationRuntimeView: () => ({
    isProcessing: activityHarness.view?.isProcessing ?? false,
    view: activityHarness.view,
  }),
}));

vi.mock('@/renderer/pages/conversation/Messages/artifacts', () => {
  const artifacts: never[] = [];
  return {
    useConversationArtifacts: () => artifacts,
    useSyncConversationArtifactWindow: () => () => {},
  };
});

vi.mock('@/renderer/pages/conversation/Messages/components/SynonBiomedUserMessageActions', () => ({
  default: ({ children }: PropsWithChildren) => <>{children}</>,
}));

vi.mock('@/renderer/pages/conversation/Messages/components/MessageText', () => ({
  default: ({ message, showCopyRow }: { message: IMessageText; showCopyRow?: boolean }) => {
    messageTextHarness.props.push({ messageId: message.id, showCopyRow });
    return <div>{message.content.content}</div>;
  },
}));

vi.mock('@/renderer/pages/conversation/Messages/components/MessageTips', () => ({
  default: () => <div>tips</div>,
}));

vi.mock('@/renderer/pages/conversation/Messages/components/MessageToolCall', () => ({
  default: () => <div>tool_call</div>,
}));

vi.mock('@/renderer/pages/conversation/Messages/components/MessageAgentStatus', () => ({
  default: () => <div>agent_status</div>,
}));

vi.mock('@/renderer/pages/conversation/Messages/components/MessagePermission', () => ({
  default: () => <div>permission</div>,
}));

vi.mock('@/renderer/pages/conversation/Messages/acp/MessageAcpPermission', () => ({
  default: () => <div>acp_permission</div>,
}));

vi.mock('@/renderer/pages/conversation/Messages/components/MessageThinking', () => ({
  default: () => <div>thinking</div>,
}));

vi.mock('@/renderer/pages/conversation/Messages/components/MessageCronTrigger', () => ({
  default: () => <div>cron_trigger</div>,
}));

vi.mock('@/renderer/pages/conversation/Messages/components/MessageSkillSuggest', () => ({
  default: () => <div>skill_suggest</div>,
}));

vi.mock('@/renderer/pages/conversation/Messages/components/MessageToolGroupSummary', () => ({
  default: () => <div>tool_summary</div>,
}));

vi.mock('@/renderer/pages/conversation/Messages/MessageFileChanges', () => ({
  __esModule: true,
  default: () => <div>file_changes</div>,
  parseDiff: vi.fn(),
}));

vi.mock('@/renderer/pages/conversation/Messages/components/SelectionReplyButton', () => ({
  default: () => null,
}));

vi.mock('@icon-park/react', () => ({
  Down: () => <span>down</span>,
  UpSmall: () => <span>up</span>,
  CloseSmall: () => <span>close</span>,
}));

function createTextMessage(content: string): IMessageText {
  return {
    id: 'message-1',
    msg_id: 'msg-1',
    conversation_id: 'conversation-1',
    type: 'text',
    position: 'left',
    content: {
      content,
    },
    created_at: 1,
  };
}

function Wrapper({ children, messages }: PropsWithChildren<{ messages: IMessageText[] }>): JSX.Element {
  return <MessageListProvider value={messages}>{children}</MessageListProvider>;
}

function setScrollableMetrics(
  scroller: HTMLDivElement,
  {
    clientHeight = 400,
    scrollHeight = 1000,
    scrollTop = 600,
  }: {
    clientHeight?: number;
    scrollHeight?: number;
    scrollTop?: number;
  } = {}
): void {
  Object.defineProperty(scroller, 'clientHeight', {
    configurable: true,
    writable: true,
    value: clientHeight,
  });
  Object.defineProperty(scroller, 'scrollHeight', {
    configurable: true,
    writable: true,
    value: scrollHeight,
  });
  Object.defineProperty(scroller, 'scrollTop', {
    configurable: true,
    writable: true,
    value: scrollTop,
  });
  scroller.scrollTo = vi.fn(({ top }: ScrollToOptions) => {
    if (typeof top === 'number') {
      scroller.scrollTop = top;
    }
  });
}

function reportDetachedFromBottom(): void {
  act(() => {
    virtuosoHarness.notifyBottomChanged?.(false);
    vi.runOnlyPendingTimers();
  });
}

describe('MessageList streaming scroll behavior', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-05-26T12:00:00.000Z'));
    vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => window.setTimeout(() => callback(0), 0));
    vi.stubGlobal('cancelAnimationFrame', (handle: number) => window.clearTimeout(handle));
    virtuosoHarness.notifyBottomChanged = null;
    virtuosoHarness.followOutput = null;
    virtuosoHarness.initialTopMostItemIndex = null;
    virtuosoHarness.atBottomThreshold = null;
    virtuosoHarness.scrollTo.mockReset();
    virtuosoHarness.scrollIntoView.mockReset();
    virtuosoHarness.scrollToIndex.mockReset();
    messageTextHarness.props.length = 0;
    activityHarness.view = undefined;
    activityHarness.connection = 'connected';
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllTimers();
    vi.useRealTimers();
  });

  it('attaches activity only to the live tail and stops animation after disconnection', () => {
    activityHarness.view = {
      ...createDefaultConversationRuntimeView('conversation-1'),
      hasTask: true,
      hasBackendRuntime: true,
      hydrated: true,
      state: 'running',
      isProcessing: true,
    };
    const renderWindow = (hasMoreAfter: boolean) => (
      <MessagePaginationProvider
        key={String(hasMoreAfter)}
        value={{
          oldestCursor: null,
          newestCursor: null,
          hasMoreBefore: false,
          hasMoreAfter,
          isLoadingBefore: false,
          isLoadingAfter: false,
          isLoadingAnchor: false,
        }}
      >
        <Wrapper messages={[createTextMessage('latest progress')]}>
          <MessageList />
        </Wrapper>
      </MessagePaginationProvider>
    );
    const { rerender } = render(renderWindow(false));
    expect(screen.getByTestId('transcript-activity-spinner')).toBeInTheDocument();
    expect(screen.getByTestId('transcript-activity').closest('[data-source-message-id]')).toHaveAttribute(
      'data-source-message-id',
      'message-1'
    );
    expect(screen.queryByText('messages.processing')).not.toBeInTheDocument();
    rerender(renderWindow(true));
    expect(screen.queryByTestId('transcript-activity')).not.toBeInTheDocument();
    activityHarness.connection = 'offline';
    rerender(renderWindow(false));
    expect(screen.queryByTestId('transcript-activity')).not.toBeInTheDocument();
    expect(screen.queryByTestId('transcript-activity-spinner')).not.toBeInTheDocument();
    activityHarness.view = { ...activityHarness.view, state: 'idle', isProcessing: false };
    rerender(renderWindow(false));
    expect(screen.queryByTestId('transcript-activity')).not.toBeInTheDocument();
  });

  it('moves the inline indicator to new text without leaving it on the previous memoized message', () => {
    activityHarness.view = {
      ...createDefaultConversationRuntimeView('conversation-1'),
      hasTask: true,
      hasBackendRuntime: true,
      hydrated: true,
      state: 'running',
      isProcessing: true,
    };
    const first = createTextMessage('first progress');
    const second = { ...createTextMessage('second progress'), id: 'message-2', msg_id: 'msg-2' };
    let append!: () => void;
    const AppendMessage = () => {
      const merge = useMergeLiveMessage();
      append = () => merge(second, true);
      return null;
    };
    render(
      <Wrapper messages={[first]}>
        <AppendMessage />
        <MessageList />
      </Wrapper>
    );
    expect(screen.getByTestId('transcript-activity').closest('[data-source-message-id]')).toHaveAttribute(
      'data-source-message-id',
      first.id
    );
    act(() => {
      append();
      vi.runOnlyPendingTimers();
    });
    expect(screen.getAllByTestId('transcript-activity-spinner')).toHaveLength(1);
    expect(screen.getByTestId('transcript-activity').closest('[data-source-message-id]')).toHaveAttribute(
      'data-source-message-id',
      second.id
    );
  });

  it('starts at the latest row and delegates streamed auto-follow to the stable Virtuoso container', () => {
    const firstMessage = createTextMessage('hello');
    const { rerender } = render(
      <Wrapper messages={[firstMessage]}>
        <MessageList />
      </Wrapper>
    );

    const scroller = screen.getByTestId('message-list-scroller') as HTMLDivElement;
    setScrollableMetrics(scroller);
    expect(virtuosoHarness.initialTopMostItemIndex).toBe(0);
    expect(virtuosoHarness.atBottomThreshold).toBe(128);
    expect(virtuosoHarness.followOutput?.(false)).toBe('auto');

    act(() => {
      vi.runOnlyPendingTimers();
    });
    virtuosoHarness.scrollTo.mockClear();
    virtuosoHarness.scrollToIndex.mockClear();

    const growthSteps = [
      { content: 'hello world'.repeat(4), scrollHeight: 1080 },
      { content: 'hello world'.repeat(8), scrollHeight: 1160 },
      { content: 'hello world'.repeat(12), scrollHeight: 1240 },
    ];

    for (const step of growthSteps) {
      rerender(
        <Wrapper messages={[createTextMessage(step.content)]}>
          <MessageList />
        </Wrapper>
      );
      scroller.scrollHeight = step.scrollHeight;
      reportDetachedFromBottom();
      expect(screen.getByTestId('message-list-scroller')).toBe(scroller);
      expect(virtuosoHarness.followOutput?.(false)).toBe('auto');
    }

    expect(virtuosoHarness.scrollTo).not.toHaveBeenCalled();
    expect(virtuosoHarness.scrollToIndex).not.toHaveBeenCalled();
  });

  it('does not detach streaming when the user clicks content or scrolls toward the tail', () => {
    render(
      <Wrapper messages={[createTextMessage('hello')]}>
        <MessageList />
      </Wrapper>
    );

    const scroller = screen.getByTestId('message-list-scroller');
    fireEvent.pointerDown(screen.getByText('hello'));
    fireEvent.wheel(scroller, { deltaY: 120 });

    expect(virtuosoHarness.followOutput?.(false)).toBe('auto');
  });

  it('positions the first painted history window at its latest row', () => {
    const messages = ['first', 'second', 'third'].map((content, index) =>
      Object.assign(createTextMessage(content), {
        id: `message-${index + 1}`,
        msg_id: `msg-${index + 1}`,
        created_at: index + 1,
      })
    );

    render(
      <Wrapper messages={messages}>
        <MessageList />
      </Wrapper>
    );

    expect(virtuosoHarness.initialTopMostItemIndex).toBe(0);
    expect(virtuosoHarness.scrollTo).toHaveBeenCalledWith({
      top: Number.MAX_SAFE_INTEGER,
      behavior: 'auto',
    });
    expect(virtuosoHarness.scrollToIndex).not.toHaveBeenCalled();
  });

  it('leaves only the branch editor actions on branchable user messages', () => {
    const userMessage: IMessageText = {
      ...createTextMessage('Branchable prompt'),
      position: 'right',
      content: {
        content: 'Branchable prompt',
        synonBiomed: { messageIndex: 0, blockIndex: 0, branchId: 'br_00000001' },
      },
    };

    render(
      <Wrapper messages={[userMessage]}>
        <MessageList />
      </Wrapper>
    );

    expect(screen.getByText('Branchable prompt')).toBeInTheDocument();
    expect(messageTextHarness.props).toContainEqual({
      messageId: userMessage.id,
      showCopyRow: false,
    });
  });

  it('stops auto-following streamed updates after the user scrolls up', () => {
    const firstMessage = createTextMessage('hello');
    const { rerender } = render(
      <Wrapper messages={[firstMessage]}>
        <MessageList />
      </Wrapper>
    );

    const scroller = screen.getByTestId('message-list-scroller') as HTMLDivElement;
    setScrollableMetrics(scroller);

    act(() => {
      vi.runOnlyPendingTimers();
    });
    virtuosoHarness.scrollTo.mockClear();
    virtuosoHarness.scrollToIndex.mockClear();

    fireEvent.wheel(scroller, { deltaY: -240 });
    scroller.scrollTop = 480;
    vi.setSystemTime(new Date('2026-05-26T12:00:00.250Z'));
    fireEvent.scroll(scroller);

    rerender(
      <Wrapper messages={[createTextMessage('hello world'.repeat(10))]}>
        <MessageList />
      </Wrapper>
    );
    scroller.scrollHeight = 1200;
    reportDetachedFromBottom();

    expect(virtuosoHarness.scrollTo).not.toHaveBeenCalled();
    expect(virtuosoHarness.scrollToIndex).not.toHaveBeenCalled();
  });
});

/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import React, { type PropsWithChildren } from 'react';
import { fireEvent, screen, waitFor, type RenderOptions } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { VirtuosoMockContext } from 'react-virtuoso';
import { MESSAGE_DEFAULT_ITEM_HEIGHT } from '@/renderer/pages/conversation/Messages/messageVirtualWindow';
import type { IConversationArtifact } from '@/common/adapter/ipcBridge';
import type { IMessageText, IMessageToolCall, TMessage } from '@/common/chat/chatLib';
import {
  MessageListLoadingProvider,
  MessageListProvider,
  MessagePaginationProvider,
} from '@/renderer/pages/conversation/Messages/hooks';
import MessageList from '@/renderer/pages/conversation/Messages/MessageList';
vi.mock('@/renderer/hooks/context/RealtimeContext', () => ({
  useRealtime: () => ({ snapshot: { status: 'connected' } }),
}));
import { renderWithI18n } from '../i18nTestUtils';

const render = (ui: React.ReactElement, options?: RenderOptions) => renderWithI18n(ui, 'zh-CN', options);

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
  useConversationContextSafe: () => ({
    conversation_id: 'conversation-1',
    type: 'unsupportedRuntime',
  }),
}));

let mockIsProcessing = false;
vi.mock('@/renderer/pages/conversation/runtime/useConversationRuntimeView', () => ({
  useConversationRuntimeView: () => ({
    isProcessing: mockIsProcessing,
    view: { taskStatus: null },
  }),
}));

vi.mock('@/renderer/pages/conversation/Messages/components/ConversationAnnotationsContext', () => ({
  ConversationAnnotationsProvider: ({ children }: PropsWithChildren) => <>{children}</>,
}));

let mockConversationArtifacts: IConversationArtifact[] = [];
vi.mock('@/renderer/pages/conversation/Messages/artifacts', () => ({
  useConversationArtifacts: () => mockConversationArtifacts,
  useSyncConversationArtifactWindow: () => () => {},
}));

const mockScrollToBottom = vi.fn();
const mockScrollToLastUser = vi.fn();
let mockScrollVisibility = {
  showScrollButton: false,
  showLastUserButton: false,
};
vi.mock('@/renderer/pages/conversation/Messages/useConversationScrollController', () => ({
  useConversationScrollController: () => ({
    handleAtBottomStateChange: () => {},
    handleRangeChanged: () => {},
    handleLastUserPositionChange: () => {},
    handleTotalListHeightChanged: () => {},
    handleUserScrollIntent: () => {},
    canLoadPreviousPage: () => true,
    followOutput: () => false,
    ...mockScrollVisibility,
    scrollToBottom: mockScrollToBottom,
    scrollToLastUser: mockScrollToLastUser,
  }),
}));

vi.mock('@/renderer/pages/conversation/Messages/components/MessageText', () => ({
  default: ({ message, showCopyRow }: { message: IMessageText; showCopyRow?: boolean }) => (
    <div data-testid={`msgtext-${message.id}`} data-copy-row={String(showCopyRow ?? true)}>
      {message.content.content}
    </div>
  ),
}));

vi.mock('@/renderer/pages/conversation/Messages/components/MessageTips', () => ({
  default: () => <div>tips</div>,
}));

vi.mock('@/renderer/pages/conversation/Messages/components/MessageToolCall', () => ({
  default: ({ message }: { message: IMessageToolCall }) => (
    <div>
      tool_call:
      {message.content.subagent?.frameId ?? (message.content.subagentEvents ? 'subagent-events' : 'plain')}
    </div>
  ),
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
vi.mock('@/renderer/pages/conversation/Messages/components/MessageScientificFiles', () => ({
  default: () => <div>scientific_files</div>,
  ScientificFilePreviewStrip: ({ files = [] }: { files?: Array<{ artifact_id: string }> }) => (
    <div
      data-testid='artifact-reference-strip'
      data-file-count={files.length}
      data-artifact-ids={files.map((file) => file.artifact_id).join(',')}
    >
      referenced_files
    </div>
  ),
}));

vi.mock('@/renderer/pages/conversation/Messages/components/MessageToolGroupSummary', () => ({
  default: () => <div data-testid='tool-summary'>tool_summary</div>,
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

function createTextMessage(): IMessageText {
  return {
    id: 'message-1',
    msg_id: 'msg-1',
    conversation_id: 'conversation-1',
    type: 'text',
    position: 'left',
    content: {
      content: 'streaming reply',
    },
    created_at: 1,
  };
}

function Wrapper({
  children,
  messages = [createTextMessage()],
  loading = false,
  isLoadingBefore = false,
  groupBoundaryMessageIds = [],
}: PropsWithChildren<{
  messages?: TMessage[];
  loading?: boolean;
  isLoadingBefore?: boolean;
  groupBoundaryMessageIds?: string[];
}>): JSX.Element {
  return (
    <VirtuosoMockContext.Provider value={{ viewportHeight: 480, itemHeight: 48 }}>
      <MessageListLoadingProvider value={loading}>
        <MessagePaginationProvider
          value={{
            hasMoreBefore: false,
            hasMoreAfter: false,
            isLoadingBefore,
            isLoadingAnchor: false,
            groupBoundaryMessageIds,
          }}
        >
          <MessageListProvider value={messages}>{children}</MessageListProvider>
        </MessagePaginationProvider>
      </MessageListLoadingProvider>
    </VirtuosoMockContext.Provider>
  );
}

describe('MessageList', () => {
  beforeEach(() => {
    vi.spyOn(Element.prototype, 'scrollTo').mockImplementation(function (
      this: Element,
      optionsOrX?: ScrollToOptions | number,
      y?: number
    ) {
      const top = typeof optionsOrX === 'object' ? (optionsOrX.top ?? 0) : (y ?? 0);
      Object.defineProperty(this, 'scrollTop', { configurable: true, value: top, writable: true });
      this.dispatchEvent(new Event('scroll'));
    });
    mockIsProcessing = false;
    mockConversationArtifacts = [];
    mockScrollVisibility = {
      showScrollButton: false,
      showLastUserButton: false,
    };
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('virtualizes a long history into real rows without placeholder flashes', async () => {
    const messages = Array.from(
      { length: 160 },
      (_, index): IMessageText => ({
        ...createTextMessage(),
        id: `message-${index}`,
        msg_id: `msg-${index}`,
        content: { content: `message ${index}` },
        created_at: index + 1,
      })
    );

    const view = await render(<MessageList />, {
      wrapper: ({ children }) => <Wrapper messages={messages}>{children}</Wrapper>,
    });

    expect(screen.getByTestId('message-list-scroller')).toHaveClass('overflow-x-hidden');
    expect(screen.getByTestId('message-list-scroller')).not.toHaveStyle({ visibility: 'hidden' });
    await waitFor(() => expect(document.querySelectorAll('[data-testid^="msgtext-"]').length).toBeGreaterThan(0));
    const mountedRows = document.querySelectorAll('[data-testid^="msgtext-"]').length;
    expect(mountedRows).toBeLessThan(messages.length);
    view.unmount();
    await new Promise((resolve) => setTimeout(resolve, 20));
  });

  it('moves the virtual window to an off-screen message without mounting the full history', async () => {
    const messages = Array.from(
      { length: 200 },
      (_, index): IMessageText => ({
        ...createTextMessage(),
        id: `stable-message-${index}`,
        msg_id: `stable-msg-${index}`,
        content: { content: `stable message ${index}` },
        created_at: index + 1,
      })
    );

    const view = await render(<MessageList />, {
      wrapper: ({ children }) => <Wrapper messages={messages}>{children}</Wrapper>,
    });

    const scroller = screen.getByTestId('message-list-scroller');
    expect(scroller.querySelectorAll('[data-testid^="msgtext-"]').length).toBeLessThan(messages.length);
    expect(screen.queryByTestId('msgtext-stable-message-150')).not.toBeInTheDocument();

    Object.defineProperty(scroller, 'scrollTop', {
      configurable: true,
      value: 150 * MESSAGE_DEFAULT_ITEM_HEIGHT,
      writable: true,
    });
    fireEvent.scroll(scroller);

    await waitFor(() => expect(screen.getByTestId('msgtext-stable-message-150')).toBeInTheDocument());
    expect(scroller.querySelectorAll('[data-testid^="msgtext-"]').length).toBeLessThan(messages.length);

    view.unmount();
    await new Promise((resolve) => setTimeout(resolve, 20));
  });

  it('renders message rows with external margin spacing inside the stable list', async () => {
    await render(<MessageList />, {
      wrapper: ({ children }) => <Wrapper>{children}</Wrapper>,
    });

    expect(screen.getByTestId('message-list-scroller')).toBeInTheDocument();
    expect(screen.getByTestId('message-list-scroller')).toHaveClass('overflow-x-hidden');
    expect(screen.getByTestId('message-list-content')).toBeInTheDocument();

    const messageRow = screen.getByTestId('message-text-left');
    expect(messageRow.className).toContain('m-t-10px');
    expect(messageRow.className).not.toContain('pt-10px');
  });

  it('retains real completed thinking rows alongside active thinking in timeline order', async () => {
    const messages = [
      {
        id: 'thinking-complete',
        conversation_id: 'conversation-1',
        type: 'thinking',
        position: 'left',
        content: { content: 'finished reasoning', status: 'done' },
        created_at: 1,
      },
      {
        id: 'thinking-active',
        conversation_id: 'conversation-1',
        type: 'thinking',
        position: 'left',
        content: { content: 'active reasoning', status: 'thinking' },
        created_at: 2,
      },
    ] as TMessage[];

    await render(<MessageList />, {
      wrapper: ({ children }) => <Wrapper messages={messages}>{children}</Wrapper>,
    });

    expect(await screen.findAllByText('thinking')).toHaveLength(2);
    expect(screen.getAllByTestId('message-thinking-left')).toHaveLength(2);
  });

  it('renders only exact canonical message artifact references and does not infer from tool output', async () => {
    const saveMessage = {
      id: 'save-message',
      conversation_id: 'conversation-1',
      type: 'tool_call',
      position: 'left',
      created_at: 2,
      content: {
        call_id: 'save-call',
        name: 'save_artifacts',
        description: 'Save final figures',
        status: 'completed',
        args: { files: ['figure.png'] },
        output: JSON.stringify({
          artifacts: [{ artifact_id: 'artifact-1', filename: 'figure.png' }],
        }),
      },
    } as IMessageToolCall;
    mockConversationArtifacts = [
      {
        id: 'scientific-files:conversation-1',
        conversation_id: 'conversation-1',
        kind: 'scientific_files',
        status: 'active',
        payload: {
          project_id: 'project-1',
          root_frame_id: 'conversation-1',
          files: [
            {
              artifact_id: 'artifact-1',
              version_id: 'version-1',
              version_number: 1,
              project_id: 'project-1',
              root_frame_id: 'conversation-1',
              frame_id: 'conversation-1',
              creating_frame_id: 'conversation-1',
              filename: 'figure.png',
              content_type: 'image/png',
              size_bytes: 128,
              preview_kind: 'image',
              content_url: '/api/artifacts/artifact-1/content',
              created_at: 2,
              updated_at: 2,
              agent_name: 'OPERON',
              is_user_upload: false,
              is_intermediate: false,
            },
          ],
        },
        created_at: 2,
        updated_at: 2,
      },
    ];
    const assistantMessage = {
      ...createTextMessage(),
      artifact_refs: [
        {
          artifact_id: 'artifact-1',
          version_id: 'version-1',
          relation: 'produced',
          availability: 'available',
        },
      ],
    } as IMessageText;

    await render(<MessageList />, {
      wrapper: ({ children }) => <Wrapper messages={[assistantMessage, saveMessage]}>{children}</Wrapper>,
    });

    expect(await screen.findByTestId('artifact-reference-strip')).toHaveAttribute('data-file-count', '1');
    expect(screen.getByTestId('tool-summary')).not.toHaveAttribute('data-file-count');
    expect(screen.queryByText('scientific_files')).not.toBeInTheDocument();
  });

  it('renders attached project files directly after the user message that sent them', async () => {
    mockConversationArtifacts = [
      {
        id: 'scientific-files:conversation-1',
        conversation_id: 'conversation-1',
        kind: 'scientific_files',
        status: 'active',
        payload: {
          project_id: 'project-1',
          root_frame_id: 'conversation-1',
          files: [
            {
              artifact_id: 'uploaded-artifact',
              version_id: 'uploaded-version',
              version_number: 1,
              project_id: 'project-1',
              root_frame_id: 'conversation-1',
              frame_id: 'conversation-1',
              creating_frame_id: 'conversation-1',
              filename: 'attached.skill',
              content_type: 'application/octet-stream',
              size_bytes: 256,
              preview_kind: 'text',
              content_url: '/api/artifacts/uploaded-artifact/content',
              created_at: 2,
              updated_at: 2,
              agent_name: null,
              is_user_upload: true,
              is_intermediate: false,
            },
          ],
        },
        created_at: 2,
        updated_at: 2,
      },
    ];
    const userMessage = {
      ...createTextMessage(),
      id: 'user-attached-file',
      msg_id: 'user-attached-file',
      position: 'right',
      content: { content: '把这个 skill 读一下' },
      created_at: 2,
      artifact_refs: [
        {
          artifact_id: 'uploaded-artifact',
          version_id: 'uploaded-version',
          relation: 'attached',
          availability: 'available',
        },
      ],
    } as IMessageText;

    await render(<MessageList />, {
      wrapper: ({ children }) => <Wrapper messages={[userMessage]}>{children}</Wrapper>,
    });

    const strip = await screen.findByTestId('artifact-reference-strip');
    expect(strip).toHaveAttribute('data-file-count', '1');
    expect(strip).toHaveAttribute('data-artifact-ids', 'uploaded-artifact');
    expect(screen.getByTestId('msgtext-user-attached-file').closest('[data-item-index]')?.nextElementSibling).toBe(
      strip.closest('[data-item-index]')
    );
  });

  it('places each generated file directly after the assistant turn that owns its canonical reference', async () => {
    const scientificFile = (artifactId: string, versionId: string, createdAt: number) => ({
      artifact_id: artifactId,
      version_id: versionId,
      version_number: 1,
      project_id: 'project-1',
      root_frame_id: 'conversation-1',
      frame_id: 'conversation-1',
      creating_frame_id: 'conversation-1',
      filename: `${artifactId}.csv`,
      content_type: 'text/csv',
      size_bytes: 128,
      preview_kind: 'csv',
      content_url: `/api/artifacts/${artifactId}/versions/${versionId}`,
      created_at: createdAt,
      updated_at: createdAt,
      agent_name: 'OPERON',
      is_user_upload: false,
      is_intermediate: false,
    });
    mockConversationArtifacts = [
      {
        id: 'scientific-files:conversation-1',
        conversation_id: 'conversation-1',
        kind: 'scientific_files',
        status: 'active',
        payload: {
          project_id: 'project-1',
          root_frame_id: 'conversation-1',
          files: [
            scientificFile('artifact-first', 'version-first', 2),
            scientificFile('artifact-second', 'version-second', 5),
          ],
        },
        created_at: 2,
        updated_at: 5,
      },
    ];
    const assistantMessage = (id: string, content: string, createdAt: number, artifactId: string, versionId: string) =>
      ({
        ...createTextMessage(),
        id,
        msg_id: id,
        content: { content },
        created_at: createdAt,
        artifact_refs: [
          {
            artifact_id: artifactId,
            version_id: versionId,
            relation: 'produced',
            availability: 'available',
          },
        ],
      }) as IMessageText;
    const userMessage = {
      ...createTextMessage(),
      id: 'user-second',
      msg_id: 'user-second',
      position: 'right',
      content: { content: 'continue' },
      created_at: 3,
    } as IMessageText;

    await render(<MessageList />, {
      wrapper: ({ children }) => (
        <Wrapper
          messages={[
            assistantMessage('assistant-first', 'first result', 2, 'artifact-first', 'version-first'),
            userMessage,
            assistantMessage('assistant-second', 'second result', 5, 'artifact-second', 'version-second'),
            {
              ...userMessage,
              id: 'user-third',
              msg_id: 'user-third',
              content: { content: 'reuse result' },
              created_at: 6,
            },
            assistantMessage('assistant-third', 'third result', 7, 'artifact-first', 'version-first'),
          ]}
        >
          {children}
        </Wrapper>
      ),
    });

    const strips = screen.getAllByTestId('artifact-reference-strip');
    expect(strips).toHaveLength(3);
    expect(strips[0]).toHaveAttribute('data-artifact-ids', 'artifact-first');
    expect(strips[1]).toHaveAttribute('data-artifact-ids', 'artifact-second');
    expect(strips[2]).toHaveAttribute('data-artifact-ids', 'artifact-first');
    expect(screen.getByTestId('msgtext-assistant-first').closest('[data-item-index]')?.nextElementSibling).toBe(
      strips[0].closest('[data-item-index]')
    );
    expect(screen.getByTestId('msgtext-assistant-second').closest('[data-item-index]')?.nextElementSibling).toBe(
      strips[1].closest('[data-item-index]')
    );
    expect(screen.getByTestId('msgtext-assistant-third').closest('[data-item-index]')?.nextElementSibling).toBe(
      strips[2].closest('[data-item-index]')
    );
    expect(screen.queryByText('scientific_files')).not.toBeInTheDocument();
  });

  it('does not claim scientific files from save_artifacts names, filenames, or JSON output without canonical refs', async () => {
    const saveMessage = {
      id: 'save-message',
      conversation_id: 'conversation-1',
      type: 'tool_call',
      position: 'left',
      created_at: 2,
      content: {
        call_id: 'save-call',
        name: 'save_artifacts',
        status: 'completed',
        args: {},
        output: JSON.stringify({
          artifacts: [{ artifact_id: 'artifact-1', filename: 'figure.png' }],
        }),
      },
    } as IMessageToolCall;
    mockConversationArtifacts = [
      {
        id: 'scientific-files:conversation-1',
        conversation_id: 'conversation-1',
        kind: 'scientific_files',
        status: 'active',
        payload: {
          project_id: 'project-1',
          root_frame_id: 'conversation-1',
          files: [
            {
              artifact_id: 'artifact-1',
              version_id: 'version-1',
              version_number: 1,
              project_id: 'project-1',
              root_frame_id: 'conversation-1',
              frame_id: 'conversation-1',
              creating_frame_id: 'conversation-1',
              filename: 'figure.png',
              content_type: 'image/png',
              size_bytes: 128,
              preview_kind: 'image',
              content_url: '/api/artifacts/artifact-1/content',
              created_at: 2,
              updated_at: 2,
              agent_name: 'OPERON',
              is_user_upload: false,
              is_intermediate: false,
            },
          ],
        },
        created_at: 2,
        updated_at: 2,
      },
    ];
    await render(<MessageList />, {
      wrapper: ({ children }) => <Wrapper messages={[createTextMessage(), saveMessage]}>{children}</Wrapper>,
    });
    expect(screen.queryByTestId('artifact-reference-strip')).not.toBeInTheDocument();
    expect(screen.queryByText('scientific_files')).not.toBeInTheDocument();
  });

  it('keeps the two Claude-style navigation controls mounted and exposes their active states accessibly', async () => {
    mockScrollVisibility = {
      showScrollButton: true,
      showLastUserButton: true,
    };

    const userMessage = {
      ...createTextMessage(),
      id: 'user-message-1',
      msg_id: 'user-message-1',
      position: 'right',
    } as IMessageText;
    const { unmount } = await render(<MessageList />, {
      wrapper: ({ children }) => <Wrapper messages={[userMessage]}>{children}</Wrapper>,
    });

    expect(screen.getByRole('button', { name: '滚动到对话底部' })).toHaveAttribute('aria-hidden', 'false');
    expect(screen.getByRole('button', { name: '跳到你的上一条消息' })).toHaveAttribute('aria-hidden', 'false');
    expect(screen.queryByTestId('jump-to-last-seen')).not.toBeInTheDocument();
    unmount();
  });

  it('renders the v1.1 transcript start and historical-loading boundary states', async () => {
    const { unmount } = await render(
      <Wrapper>
        <MessageList />
      </Wrapper>
    );

    expect(screen.getByTestId('transcript-start-marker')).toHaveTextContent('对话开始');
    unmount();

    await render(
      <Wrapper isLoadingBefore>
        <MessageList />
      </Wrapper>
    );
    expect(screen.getByTestId('transcript-load-older')).toHaveTextContent('正在加载更早的消息...');
  });

  it('uses container-responsive fluid width for standalone message rows', async () => {
    await render(<MessageList />, {
      wrapper: ({ children }) => <Wrapper>{children}</Wrapper>,
    });

    const messageRow = screen.getByTestId('message-text-left');
    expect(messageRow.className).toContain('chat-surface-fluid');
    expect(messageRow.className).toContain('box-border');
    expect(messageRow.className).toContain('w-full');
    expect(messageRow.className).not.toContain('w-[calc(100%-24px)]');
    expect(messageRow.className).not.toContain('md:w-[calc(100%-clamp(80px,10vw,240px))]');
    expect(messageRow.className).not.toContain('max-w-780px');
  });

  it('shows the copy row only on the last AI text of each turn', async () => {
    // Turn 1: thinking + text(a) + tool + text(b) -> row only on text(b).
    // A user message ends the turn. Turn 2: text(c) -> row on text(c).
    const messages = [
      {
        id: 'think-1',
        type: 'thinking',
        position: 'left',
        content: { content: 'thinking' },
        created_at: 1,
      },
      {
        id: 'text-a',
        type: 'text',
        position: 'left',
        content: { content: 'a' },
        created_at: 2,
      },
      {
        id: 'tool-1',
        type: 'tool_call',
        position: 'left',
        content: { call_id: 'tool-1', name: 'python', args: {}, status: 'completed' },
        created_at: 3,
      },
      {
        id: 'text-b',
        type: 'text',
        position: 'left',
        content: { content: 'b' },
        created_at: 4,
      },
      {
        id: 'user-1',
        type: 'text',
        position: 'right',
        content: { content: 'q' },
        created_at: 5,
      },
      {
        id: 'text-c',
        type: 'text',
        position: 'left',
        content: { content: 'c' },
        created_at: 6,
      },
    ] as unknown as IMessageText[];

    await render(<MessageList />, {
      wrapper: ({ children }) => <Wrapper messages={messages}>{children}</Wrapper>,
    });

    // Intermediate AI text (followed by a tool then another text) hides the row.
    expect(screen.getByTestId('msgtext-text-a').getAttribute('data-copy-row')).toBe('false');
    // Last AI text of turn 1 (after the tool block) keeps the row — fallback strategy.
    expect(screen.getByTestId('msgtext-text-b').getAttribute('data-copy-row')).toBe('true');
    // User message always keeps its own row.
    expect(screen.getByTestId('msgtext-user-1').getAttribute('data-copy-row')).toBe('true');
    // Turn 2's only/last text keeps the row.
    expect(screen.getByTestId('msgtext-text-c').getAttribute('data-copy-row')).toBe('true');
  });

  it('does not insert a copy row between a progress note and later operations', async () => {
    const messages = [
      {
        id: 'progress-text',
        type: 'text',
        position: 'left',
        content: { content: 'The selected dataset is suitable. Next I will prepare the analysis.' },
        created_at: 1,
      },
      {
        id: 'tool-after-progress',
        type: 'tool_call',
        position: 'left',
        content: { call_id: 'tool-after-progress', name: 'python', args: {}, status: 'completed' },
        created_at: 2,
      },
      {
        id: 'next-user',
        type: 'text',
        position: 'right',
        content: { content: 'continue' },
        created_at: 3,
      },
    ] as unknown as IMessageText[];

    await render(<MessageList />, {
      wrapper: ({ children }) => <Wrapper messages={messages}>{children}</Wrapper>,
    });

    expect(screen.getByTestId('msgtext-progress-text').getAttribute('data-copy-row')).toBe('false');
  });

  it('withholds the streaming turn copy row but keeps earlier finished turns', async () => {
    mockIsProcessing = true;
    // Turn 1 finished (text-a), then a user message, then turn 2 still streaming (text-b).
    const messages = [
      {
        id: 'text-a',
        type: 'text',
        position: 'left',
        content: { content: 'a' },
        created_at: 1,
      },
      {
        id: 'user-1',
        type: 'text',
        position: 'right',
        content: { content: 'q' },
        created_at: 2,
      },
      {
        id: 'text-b',
        type: 'text',
        position: 'left',
        content: { content: 'b' },
        created_at: 3,
      },
    ] as unknown as IMessageText[];

    await render(<MessageList />, {
      wrapper: ({ children }) => <Wrapper messages={messages}>{children}</Wrapper>,
    });

    // Earlier finished turn keeps its row even while a later turn streams.
    expect(screen.getByTestId('msgtext-text-a').getAttribute('data-copy-row')).toBe('true');
    // The in-progress final turn withholds its row until streaming ends.
    expect(screen.getByTestId('msgtext-text-b').getAttribute('data-copy-row')).toBe('false');
  });

  it('does not append unreferenced scientific file collections to the end of the transcript', async () => {
    mockConversationArtifacts = [
      {
        id: 'synonbiomed-files:frame-1',
        conversation_id: 'conversation-1',
        kind: 'scientific_files',
        status: 'active',
        payload: {
          project_id: 'proj-1',
          root_frame_id: 'frame-1',
          files: [
            {
              artifact_id: 'artifact-1',
              version_id: 'version-1',
              version_number: 1,
              project_id: 'proj-1',
              root_frame_id: 'frame-1',
              frame_id: 'frame-1',
              creating_frame_id: 'frame-1',
              filename: 'result.csv',
              content_type: 'text/csv',
              size_bytes: 128,
              preview_kind: 'csv',
              content_url: '/api/artifacts/artifact-1',
              created_at: 1,
              updated_at: 1,
              agent_name: 'OPERON',
              is_user_upload: false,
              is_intermediate: false,
            },
          ],
        },
        created_at: 2,
        updated_at: 2,
      },
    ] as IConversationArtifact[];

    await render(<MessageList />, {
      wrapper: ({ children }) => <Wrapper>{children}</Wrapper>,
    });

    expect(screen.queryByTestId('conversation-artifact-scientific_files')).not.toBeInTheDocument();
    expect(screen.queryByText('scientific_files')).not.toBeInTheDocument();
  });
  it('renders the empty slot when there are no messages', async () => {
    await render(<MessageList emptySlot={<div>empty state</div>} />, {
      wrapper: ({ children }) => <Wrapper messages={[]}>{children}</Wrapper>,
    });

    expect(screen.getByText('empty state')).toBeInTheDocument();
  });

  it('does not create virtual rows for empty or internal cancellation text', async () => {
    const visible = { ...createTextMessage(), id: 'visible', msg_id: 'visible-msg' };
    const empty = {
      ...createTextMessage(),
      id: 'empty',
      msg_id: 'empty-msg',
      content: { content: '' },
    };
    const cancellation = {
      ...createTextMessage(),
      id: 'cancelled',
      msg_id: 'cancelled-msg',
      terminal_status: 'cancelled' as const,
      content: { content: 'user_cancelled' },
    };

    await render(<MessageList />, {
      wrapper: ({ children }) => <Wrapper messages={[empty, cancellation, visible]}>{children}</Wrapper>,
    });

    expect(screen.queryByTestId('msgtext-empty')).not.toBeInTheDocument();
    expect(screen.queryByTestId('msgtext-cancelled')).not.toBeInTheDocument();
    expect(screen.getByTestId('msgtext-visible')).toBeInTheDocument();
    expect(screen.getAllByTestId('message-text-left')).toHaveLength(1);
  });

  it('keeps delegation turns outside ordinary tool summary groups', async () => {
    const messages = [
      {
        id: 'tool-before',
        conversation_id: 'conversation-1',
        type: 'tool_call',
        position: 'left',
        content: {
          call_id: 'tool-before',
          name: 'read_file',
          args: {},
          status: 'completed',
        },
        created_at: 1,
      },
      {
        id: 'delegate',
        conversation_id: 'conversation-1',
        type: 'tool_call',
        position: 'left',
        content: {
          call_id: 'delegate',
          name: 'delegate',
          args: {},
          status: 'running',
          subagent: {
            ordinal: 1,
            frameId: 'frame-child',
            status: 'processing',
          },
        },
        created_at: 2,
      },
      {
        id: 'tool-after',
        conversation_id: 'conversation-1',
        type: 'tool_call',
        position: 'left',
        content: {
          call_id: 'tool-after',
          name: 'web_search',
          args: {},
          status: 'completed',
        },
        created_at: 3,
      },
    ] as TMessage[];

    await render(<MessageList />, {
      wrapper: ({ children }) => <Wrapper messages={messages}>{children}</Wrapper>,
    });

    expect(await screen.findByText('tool_call:frame-child')).toBeInTheDocument();
    expect(await screen.findAllByText('tool_summary')).toHaveLength(2);
  });

  it('keeps a stable tool summary boundary when an earlier cursor page is prepended', async () => {
    const messages = [
      {
        id: 'older-tool',
        conversation_id: 'conversation-1',
        type: 'tool_call',
        position: 'left',
        content: { call_id: 'older-tool', name: 'read_file', args: {}, status: 'completed' },
        created_at: 1,
      },
      {
        id: 'existing-window-tool',
        conversation_id: 'conversation-1',
        type: 'tool_call',
        position: 'left',
        content: {
          call_id: 'existing-window-tool',
          name: 'web_search',
          args: {},
          status: 'completed',
        },
        created_at: 2,
      },
    ] as TMessage[];

    await render(<MessageList />, {
      wrapper: ({ children }) => (
        <Wrapper messages={messages} groupBoundaryMessageIds={['existing-window-tool']}>
          {children}
        </Wrapper>
      ),
    });

    expect(await screen.findAllByTestId('tool-summary')).toHaveLength(2);
  });

  it('keeps child-frame notifications outside ordinary tool summary groups', async () => {
    const messages = [
      {
        id: 'tool-before',
        conversation_id: 'conversation-1',
        type: 'tool_call',
        position: 'left',
        content: {
          call_id: 'tool-before',
          name: 'read_file',
          args: {},
          status: 'completed',
        },
        created_at: 1,
      },
      {
        id: 'subagent-events',
        conversation_id: 'conversation-1',
        type: 'tool_call',
        position: 'left',
        content: {
          call_id: 'subagent-events',
          name: 'subagent_events',
          args: {},
          status: 'completed',
          subagentEvents: [
            {
              kind: 'question',
              frameId: 'frame-child',
              ordinal: 1,
              childName: 'Literature research',
              text: 'Which assay should I prioritize?',
            },
          ],
        },
        created_at: 2,
      },
      {
        id: 'tool-after',
        conversation_id: 'conversation-1',
        type: 'tool_call',
        position: 'left',
        content: {
          call_id: 'tool-after',
          name: 'web_search',
          args: {},
          status: 'completed',
        },
        created_at: 3,
      },
    ] as TMessage[];

    await render(<MessageList />, {
      wrapper: ({ children }) => <Wrapper messages={messages}>{children}</Wrapper>,
    });

    expect(screen.getByText(/tool_call:\s*subagent-events/)).toBeInTheDocument();
    expect(screen.getAllByText('tool_summary')).toHaveLength(2);
  });

  it('keeps a compact loading state visible while the initial message batch is loading', async () => {
    await render(<MessageList emptySlot={<div>empty state</div>} />, {
      wrapper: ({ children }) => (
        <Wrapper messages={[]} loading>
          {children}
        </Wrapper>
      ),
    });

    expect(screen.getByTestId('message-list-loading-surface')).toHaveAttribute('aria-busy', 'true');
    expect(screen.getByTestId('message-list-loading-surface')).toHaveAttribute('role', 'status');
    expect(screen.getByTestId('conversation-loading-indicator')).toBeInTheDocument();
    expect(screen.queryByAltText('SYNON-Biomed')).not.toBeInTheDocument();
    expect(screen.queryByTestId('conversation-loading-composer')).not.toBeInTheDocument();
    expect(screen.queryByTestId('message-list-skeleton')).not.toBeInTheDocument();
    expect(screen.queryByText('empty state')).not.toBeInTheDocument();
  });
});

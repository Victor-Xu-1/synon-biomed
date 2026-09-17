import { ConfigProvider } from '@arco-design/web-react';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { IMessageText } from '@/common/chat/chatLib';
import { ConversationAnnotationsProvider } from '@/renderer/pages/conversation/Messages/components/ConversationAnnotationsContext';
import MessageTranscriptAnnotations from '@/renderer/pages/conversation/Messages/components/MessageTranscriptAnnotations';
import { renderWithI18n } from '../i18nTestUtils';

const mocks = vi.hoisted(() => ({
  load: vi.fn(),
  create: vi.fn(),
  update: vi.fn(),
  remove: vi.fn(),
}));

vi.mock('@arco-design/web-react', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@arco-design/web-react')>();
  return {
    ...actual,
    Message: {
      useMessage: () => [{ success: vi.fn(), error: vi.fn() }, null],
    },
  };
});

vi.mock('@/renderer/services/synonBiomedAnnotations', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/renderer/services/synonBiomedAnnotations')>();
  return {
    ...actual,
    loadSynonBiomedTranscriptAnnotations: mocks.load,
    createSynonBiomedTranscriptAnnotation: mocks.create,
    updateSynonBiomedTranscriptAnnotation: mocks.update,
    deleteSynonBiomedTranscriptAnnotation: mocks.remove,
  };
});

const message: IMessageText = {
  id: 'message-local-4',
  msg_id: 'message-uuid-4',
  conversation_id: 'frame-1',
  type: 'text',
  position: 'left',
  createdAt: Date.now(),
  content: { content: 'STAT6 assay evidence from two independent runs.' },
};

const existing = {
  id: 'transcript-1',
  rootFrameId: 'frame-1',
  messageUuid: 'message-uuid-4',
  messageIndex: 4,
  blockIndex: 0,
  source: 'assistant' as const,
  toolName: null,
  anchorText: 'STAT6 assay evidence from two independent runs.',
  startOffset: null,
  endOffset: null,
  kind: 'bookmark' as const,
  origin: 'user' as const,
  readAt: null,
  note: 'Review assay variance',
  createdAt: '2026-07-13T00:00:00.000Z',
  updatedAt: '2026-07-13T00:00:00.000Z',
};

describe('Message transcript annotations', () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset();
    mocks.load.mockResolvedValue([]);
    mocks.create.mockResolvedValue({ ...existing, id: 'transcript-created', note: 'Follow up on evidence' });
    mocks.update.mockImplementation(
      async (_frameId: string, _id: string, patch: { note?: string; read?: boolean }) => ({
        ...existing,
        note: patch.note ?? existing.note,
        readAt: patch.read ? '2026-07-13T00:01:00.000Z' : existing.readAt,
      })
    );
    mocks.remove.mockResolvedValue(undefined);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('does not create a layout observer when the message has no annotations', async () => {
    const resizeObserverConstructor = vi.fn();
    class EmptyAnnotationResizeObserver {
      constructor() {
        resizeObserverConstructor();
      }

      observe() {}
      disconnect() {}
    }
    vi.stubGlobal('ResizeObserver', EmptyAnnotationResizeObserver);
    const contentRootRef = React.createRef<HTMLDivElement>();

    await renderWithI18n(
      <ConfigProvider>
        <ConversationAnnotationsProvider frameId='frame-1' ownerId='owner-a'>
          <div ref={contentRootRef}>{message.content.content}</div>
          <MessageTranscriptAnnotations
            message={message}
            messageIndex={4}
            anchorText={message.content.content}
            contentRootRef={contentRootRef}
          />
        </ConversationAnnotationsProvider>
      </ConfigProvider>,
      'zh-CN'
    );

    await waitFor(() => expect(mocks.load).toHaveBeenCalledTimes(1));
    expect(resizeObserverConstructor).not.toHaveBeenCalled();
  });

  it('loads once for the conversation and creates a bookmark anchored by message UUID and index', async () => {
    await renderWithI18n(
      <ConfigProvider>
        <ConversationAnnotationsProvider frameId='frame-1' ownerId='owner-a'>
          <MessageTranscriptAnnotations message={message} messageIndex={4} anchorText={message.content.content} />
        </ConversationAnnotationsProvider>
      </ConfigProvider>,
      'zh-CN'
    );

    await waitFor(() => expect(mocks.load).toHaveBeenCalledTimes(1));
    fireEvent.click(screen.getByRole('button', { name: '添加消息批注' }));
    fireEvent.change(screen.getByRole('textbox', { name: '消息批注备注' }), {
      target: { value: 'Follow up on evidence' },
    });
    fireEvent.click(screen.getByRole('button', { name: '添加' }));

    await waitFor(() =>
      expect(mocks.create).toHaveBeenCalledWith(
        'frame-1',
        expect.objectContaining({
          messageUuid: 'message-uuid-4',
          messageIndex: 4,
          source: 'assistant',
          kind: 'bookmark',
          anchorText: message.content.content,
          note: 'Follow up on evidence',
        })
      )
    );
    expect(await screen.findByRole('button', { name: '消息批注 1' })).toBeInTheDocument();
  });

  it('keeps the empty annotation action visible when used by the mobile message layout', async () => {
    await renderWithI18n(
      <ConfigProvider>
        <ConversationAnnotationsProvider frameId='frame-1' ownerId='owner-a'>
          <MessageTranscriptAnnotations
            message={message}
            messageIndex={4}
            anchorText={message.content.content}
            alwaysVisible
          />
        </ConversationAnnotationsProvider>
      </ConfigProvider>,
      'zh-CN'
    );

    const action = await screen.findByRole('button', { name: '添加消息批注' });
    expect(action).toHaveClass('text-t-secondary');
    expect(action).not.toHaveClass('opacity-0');
    expect(action).not.toHaveClass('pointer-events-none');
  });

  it('opens an existing unread bookmark, marks it read and saves its note', async () => {
    mocks.load.mockResolvedValue([existing]);
    await renderWithI18n(
      <ConfigProvider>
        <ConversationAnnotationsProvider frameId='frame-1' ownerId='owner-a'>
          <MessageTranscriptAnnotations message={message} messageIndex={4} anchorText={message.content.content} />
        </ConversationAnnotationsProvider>
      </ConfigProvider>,
      'zh-CN'
    );

    const annotationButton = await screen.findByRole('button', { name: '打开消息批注 Review assay variance' });
    fireEvent.click(annotationButton);
    await waitFor(() => expect(mocks.update).toHaveBeenCalledWith('frame-1', 'transcript-1', { read: true }));

    const note = screen.getByRole('textbox', { name: '消息批注备注' });
    fireEvent.change(note, { target: { value: 'Variance confirmed' } });
    fireEvent.click(screen.getByRole('button', { name: '保存' }));
    await waitFor(() =>
      expect(mocks.update).toHaveBeenCalledWith('frame-1', 'transcript-1', { note: 'Variance confirmed' })
    );
  });

  it('fences a pending annotation response when conversation ownership changes', async () => {
    let resolveOwnerA!: (value: (typeof existing)[]) => void;
    mocks.load.mockImplementation((_frameId: string, options: { ownerId: string }) => {
      if (options.ownerId === 'owner-a') {
        return new Promise<(typeof existing)[]>((resolve) => {
          resolveOwnerA = resolve;
        });
      }
      return Promise.resolve([{ ...existing, id: 'owner-b-note', note: 'Owner B evidence' }]);
    });
    const content = (ownerId: string) => (
      <ConfigProvider>
        <ConversationAnnotationsProvider frameId='frame-1' ownerId={ownerId}>
          <MessageTranscriptAnnotations message={message} messageIndex={4} anchorText={message.content.content} />
        </ConversationAnnotationsProvider>
      </ConfigProvider>
    );

    const view = await renderWithI18n(content('owner-a'), 'zh-CN');
    await waitFor(() => expect(mocks.load).toHaveBeenCalledWith('frame-1', { ownerId: 'owner-a' }));
    view.rerender(content('owner-b'));

    expect(await screen.findByRole('button', { name: '打开消息批注 Owner B evidence' })).toBeInTheDocument();
    resolveOwnerA([{ ...existing, id: 'owner-a-note', note: 'Owner A evidence' }]);
    await Promise.resolve();

    expect(screen.queryByRole('button', { name: '打开消息批注 Owner A evidence' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: '打开消息批注 Owner B evidence' })).toBeInTheDocument();
  });
});

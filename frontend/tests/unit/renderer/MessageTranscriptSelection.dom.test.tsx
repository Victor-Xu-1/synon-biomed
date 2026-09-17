import type { IMessageText } from '@/common/chat/chatLib';
import { ConversationAnnotationsProvider } from '@/renderer/pages/conversation/Messages/components/ConversationAnnotationsContext';
import MessageTranscriptAnnotations from '@/renderer/pages/conversation/Messages/components/MessageTranscriptAnnotations';
import { ConfigProvider } from '@arco-design/web-react';
import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import React, { useRef } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

const mocks = vi.hoisted(() => ({ load: vi.fn(), create: vi.fn(), update: vi.fn(), remove: vi.fn(), error: vi.fn() }));

vi.mock('@arco-design/web-react', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@arco-design/web-react')>();
  return { ...actual, Message: { useMessage: () => [{ success: vi.fn(), error: mocks.error }, null] } };
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
  content: { content: 'STAT6 then STAT6 evidence.' },
};

const rangedAnnotation = {
  id: 'transcript-range-1',
  rootFrameId: 'frame-1',
  messageUuid: 'message-uuid-4',
  messageIndex: 4,
  blockIndex: 0,
  source: 'assistant' as const,
  toolName: null,
  anchorText: 'STAT6',
  startOffset: 11,
  endOffset: 16,
  kind: 'annotation' as const,
  origin: 'user' as const,
  readAt: '2026-07-14T00:00:00.000Z',
  note: 'Second occurrence',
  createdAt: '2026-07-14T00:00:00.000Z',
  updatedAt: '2026-07-14T00:00:00.000Z',
};

function Harness({ text = message.content.content }: { text?: string }) {
  const rootRef = useRef<HTMLDivElement>(null);
  const highlightHostRef = useRef<HTMLDivElement>(null);
  return (
    <ConfigProvider>
      <ConversationAnnotationsProvider frameId='frame-1' ownerId='owner-a'>
        <div ref={rootRef} data-testid='message-root' className='relative'>
          {text}
          <div ref={highlightHostRef} />
        </div>
        <MessageTranscriptAnnotations
          message={message}
          messageIndex={4}
          anchorText={text}
          contentRootRef={rootRef}
          highlightHostRef={highlightHostRef}
          alwaysVisible
        />
      </ConversationAnnotationsProvider>
    </ConfigProvider>
  );
}

describe('selected-text transcript annotations', () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset();
    mocks.load.mockResolvedValue([]);
    mocks.create.mockResolvedValue(rangedAnnotation);
    mocks.update.mockImplementation(async (_frame: string, _id: string, patch: { note?: string }) => ({
      ...rangedAnnotation,
      note: patch.note ?? rangedAnnotation.note,
    }));
    mocks.remove.mockResolvedValue(undefined);
    vi.stubGlobal(
      'ResizeObserver',
      class {
        observe() {}
        disconnect() {}
      }
    );
    Object.defineProperty(Range.prototype, 'getClientRects', {
      configurable: true,
      value: vi.fn(() => [
        { left: 20, top: 10, right: 70, bottom: 28, width: 50, height: 18, x: 20, y: 10, toJSON: () => ({}) },
      ]),
    });
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({
      left: 0,
      top: 0,
      right: 300,
      bottom: 80,
      width: 300,
      height: 80,
      x: 0,
      y: 0,
      toJSON: () => ({}),
    });
  });

  it('creates a persisted annotation for the exact selected occurrence', async () => {
    await renderWithI18n(<Harness />);
    await waitFor(() => expect(mocks.load).toHaveBeenCalledOnce());
    const root = screen.getByTestId('message-root');
    const range = document.createRange();
    range.setStart(root.firstChild!, 11);
    range.setEnd(root.firstChild!, 16);
    const selection = window.getSelection()!;
    selection.removeAllRanges();
    selection.addRange(range);
    act(() => document.dispatchEvent(new Event('selectionchange')));

    fireEvent.click(await screen.findByRole('button', { name: '批注所选文本' }));
    selection.removeAllRanges();
    act(() => document.dispatchEvent(new Event('selectionchange')));
    fireEvent.change(screen.getByRole('textbox', { name: '消息批注备注' }), { target: { value: 'Second result' } });
    fireEvent.click(screen.getByRole('button', { name: '添加' }));

    await waitFor(() =>
      expect(mocks.create).toHaveBeenCalledWith(
        'frame-1',
        expect.objectContaining({
          messageUuid: 'message-uuid-4',
          anchorText: 'STAT6',
          startOffset: 11,
          endOffset: 16,
          kind: 'annotation',
          note: 'Second result',
        })
      )
    );
  });

  it('restores a highlight and opens, edits, then deletes it through the API', async () => {
    mocks.load.mockResolvedValue([rangedAnnotation]);
    await renderWithI18n(<Harness />);
    const root = await screen.findByTestId('message-root');
    await waitFor(() => expect(root.querySelector('[data-annotation-id="transcript-range-1"]')).not.toBeNull());

    fireEvent.click(screen.getByRole('button', { name: /Second occurrence/ }));
    expect(await screen.findByText('Second occurrence')).toBeInTheDocument();
    fireEvent.change(screen.getByRole('textbox', { name: '消息批注备注' }), { target: { value: 'Edited note' } });
    fireEvent.click(screen.getByRole('button', { name: '保存' }));
    await waitFor(() =>
      expect(mocks.update).toHaveBeenCalledWith('frame-1', 'transcript-range-1', { note: 'Edited note' })
    );

    fireEvent.click(screen.getByRole('button', { name: '打开消息批注 Edited note' }));
    fireEvent.click(screen.getByRole('button', { name: '删除' }));
    await waitFor(() => expect(mocks.remove).toHaveBeenCalledWith('frame-1', 'transcript-range-1'));
  });

  it('marks an ambiguous stale anchor unresolved instead of attaching it to the wrong occurrence', async () => {
    mocks.load.mockResolvedValue([{ ...rangedAnnotation, startOffset: 2, endOffset: 7 }]);
    await renderWithI18n(<Harness />);
    const button = await screen.findByRole('button', { name: /锚点失效/ });
    expect(button).toHaveTextContent('锚点失效');
    fireEvent.click(button);
    expect(screen.getByRole('status')).toHaveTextContent('系统未自动附着到不确定位置');
  });

  it('keeps load and save failures visible and retryable', async () => {
    mocks.load.mockRejectedValueOnce(new Error('annotation backend unavailable')).mockResolvedValueOnce([]);
    await renderWithI18n(<Harness />);
    const retry = await screen.findByRole('button', { name: '重新加载会话批注' });
    fireEvent.click(retry);
    await waitFor(() => expect(mocks.load).toHaveBeenCalledTimes(2));

    mocks.create.mockRejectedValueOnce(new Error('save rejected'));
    fireEvent.click(screen.getByRole('button', { name: '添加消息批注' }));
    fireEvent.click(screen.getByRole('button', { name: '添加' }));
    await waitFor(() => expect(mocks.error).toHaveBeenCalledWith('保存会话批注失败。'));
  });
});

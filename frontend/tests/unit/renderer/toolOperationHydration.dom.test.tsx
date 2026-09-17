import { act, renderHook } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { ipcBridge } from '@/common';
import { useToolOperationDetail } from '@/renderer/pages/conversation/Messages/components/useToolOperationDetail';
import type { NormalizedToolCall } from '@/common/chat/normalizeToolCall';
vi.mock('@/common', () => ({ ipcBridge: { database: { getConversationMessage: { invoke: vi.fn() } } } }));
const item: NormalizedToolCall = {
  key: 'call',
  name: 'web_fetch',
  status: 'completed',
  conversationId: 'frame',
  messageId: 'message',
  input: '{"url":"https://first.example/a"}',
  truncated: true,
};
const message = (conversation_id = 'frame', id = 'message') => ({
  id,
  conversation_id,
  type: 'tool_call',
  content: {
    call_id: 'call',
    name: 'web_fetch',
    status: 'completed',
    input: { url: 'https://first.example/a' },
    output: '{"url":"https://first.example/a","bytes_read":100}',
  },
});

describe('tool detail hydration ownership', () => {
  it('keeps a failed load bounded until explicit retry', async () => {
    const invoke = vi.mocked(ipcBridge.database.getConversationMessage.invoke);
    invoke.mockReset().mockRejectedValue(new Error('unavailable'));
    const { result } = renderHook(() => useToolOperationDetail(item, true));
    await act(() => result.current.loadFullItem());
    await act(() => result.current.loadFullItem());
    expect(invoke).toHaveBeenCalledTimes(1);
    invoke.mockResolvedValue(message() as never);
    act(() => result.current.retryFullItem());
    await act(() => result.current.loadFullItem());
    expect(invoke).toHaveBeenCalledTimes(2);
    expect(result.current.loadError).toBe(false);
    expect(result.current.displayItem.output).toContain('first.example');
  });
  it.each([
    ['other-frame', 'message'],
    ['frame', 'other-message'],
  ])('rejects a response owned by %s / %s', async (frame, id) => {
    vi.mocked(ipcBridge.database.getConversationMessage.invoke).mockResolvedValue(message(frame, id) as never);
    const { result } = renderHook(() => useToolOperationDetail(item, true));
    await act(() => result.current.loadFullItem());
    expect(result.current.loadError).toBe(true);
    expect(result.current.displayItem.output).toBeUndefined();
  });
  it('does not commit a delayed response after operation identity changes', async () => {
    let resolve!: (value: unknown) => void;
    vi.mocked(ipcBridge.database.getConversationMessage.invoke).mockImplementation(
      () =>
        new Promise((r) => {
          resolve = r;
        }) as never
    );
    const { result, rerender } = renderHook(({ current }) => useToolOperationDetail(current, true), {
      initialProps: { current: item },
    });
    let request!: Promise<void>;
    act(() => {
      request = result.current.loadFullItem();
    });
    rerender({
      current: { ...item, key: 'next', messageId: 'next-message', input: '{"url":"https://next.example/a"}' },
    });
    await act(async () => {
      resolve(message());
      await request;
    });
    expect(result.current.displayItem.key).toBe('next');
    expect(result.current.displayItem.output).toBeUndefined();
  });
});

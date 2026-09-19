import React, { type PropsWithChildren } from 'react';
import { act, renderHook } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { transformMessage, preferTextMessageVersion, type IMessageText, type TMessage } from '@/common/chat/chatLib';
import { MessageListProvider, useMessageList, useMergeLiveMessage } from '@/renderer/pages/conversation/Messages/hooks';
import { mergeLoadedPageWithCurrent } from '@/renderer/pages/conversation/Messages/messageWindowReconciliation';

afterEach(() => vi.restoreAllMocks());

it('does not replay a live publication across tool boundaries, but retains genuinely new text', () => {
  const wrapper = ({ children }: PropsWithChildren) => <MessageListProvider value={[]}>{children}</MessageListProvider>;
  const frames: FrameRequestCallback[] = [];
  vi.spyOn(window, 'requestAnimationFrame').mockImplementation((callback) => {
    frames.push(callback);
    return frames.length;
  });
  const { result } = renderHook(() => ({ merge: useMergeLiveMessage(), messages: useMessageList() }), { wrapper });
  const publish = (sequence: number, text: string, add = false) => {
    const message = transformMessage({
      type: 'text',
      msg_id: 'segment-69',
      conversation_id: 'conversation',
      data: text,
      source_publication_sequence: sequence,
      publication_boundary_id: `publication:${sequence}`,
    } as Parameters<typeof transformMessage>[0]);
    act(() => result.current.merge(message, add));
    act(() => frames.shift()?.(16));
  };
  publish(1285, 'A');
  publish(1287, 'B');
  const tool = {
    id: 'tool',
    msg_id: 'tool',
    conversation_id: 'conversation',
    type: 'tips',
    content: { content: 'Tool completed' },
  } as TMessage;
  act(() => result.current.merge(tool));
  act(() => frames.shift()?.(16));
  publish(1285, 'A');
  expect(result.current.messages).toHaveLength(2);
  expect((result.current.messages[0] as IMessageText).content.content).toBe('AB');
  publish(1285, 'A', true); // An insertion hint cannot bypass publication identity.
  expect(result.current.messages).toHaveLength(2);
  publish(1286, 'A'); // An unseen publication is not discarded because a larger sequence arrived.
  expect(result.current.messages).toHaveLength(2);
  expect((result.current.messages[0] as IMessageText).content.content).toBe('ABA');
});

it('retires live-only duplicate rows only when the same identity is covered by history', () => {
  const snapshot = {
    id: 'segment-69',
    msg_id: 'segment-69',
    conversation_id: 'conversation',
    type: 'text',
    position: 'left',
    content: { content: 'A' },
    history_coverage_through: 1285,
  } as IMessageText;
  const live = { ...snapshot, history_coverage_through: undefined, source_publication_sequence: 1285 };
  const duplicate = { ...live, id: 'segment-69:segment:2' };
  const legitimate = { ...live, id: 'segment-70', msg_id: 'segment-70', source_publication_sequence: 1286 };
  const merged = mergeLoadedPageWithCurrent('conversation', [snapshot], [live, duplicate, legitimate], true);
  expect(merged.map((item) => item.id)).toEqual(['segment-69', 'segment-70']);
});

it('does not append a delayed delta already represented by the loaded history snapshot', () => {
  const text = '访问受限，将核对其他来源。';
  const snapshot = {
    id: 'segment-6',
    msg_id: 'segment-6',
    conversation_id: 'conversation',
    type: 'text',
    position: 'left',
    status: 'finish',
    content: { content: text },
    history_coverage_through: 564,
  } as TMessage;
  const wrapper = ({ children }: PropsWithChildren) => (
    <MessageListProvider value={[snapshot]}>{children}</MessageListProvider>
  );
  const frames: FrameRequestCallback[] = [];
  vi.spyOn(window, 'requestAnimationFrame').mockImplementation((callback) => {
    frames.push(callback);
    return frames.length;
  });
  const { result } = renderHook(() => ({ merge: useMergeLiveMessage(), messages: useMessageList() }), { wrapper });
  const publish = (sequence: number) => {
    const message = transformMessage({
      type: 'text',
      msg_id: 'segment-6',
      conversation_id: 'conversation',
      data: text,
      status: 'pending',
      source_publication_sequence: sequence,
      publication_boundary_id: `publication:${sequence}`,
    } as Parameters<typeof transformMessage>[0]);
    act(() => result.current.merge(message));
    act(() => frames.shift()?.(16));
  };
  publish(529);
  expect((result.current.messages[0] as IMessageText).content.content).toBe(text);
  // Identical text at a genuinely new publication is not a replay.
  publish(565);
  expect((result.current.messages[0] as IMessageText).content.content).toBe(text + text);
});

it('prefers authoritative snapshot coverage over a longer stale live copy', () => {
  const snapshot = {
    id: 'segment',
    msg_id: 'segment',
    conversation_id: 'conversation',
    type: 'text',
    position: 'left',
    content: { content: '一次说明。' },
    history_coverage_through: 564,
  } as IMessageText;
  const live = {
    ...snapshot,
    content: { content: '一次说明。一次说明。' },
    history_coverage_through: undefined,
    source_publication_sequence: 529,
  } as IMessageText;
  expect(preferTextMessageVersion(snapshot, live).content.content).toBe('一次说明。');
});

it('retains longer live text while an in-progress history snapshot catches up', () => {
  const snapshot = {
    id: 'segment-running',
    msg_id: 'segment-running',
    conversation_id: 'conversation',
    type: 'text',
    position: 'left',
    status: 'work',
    content: { content: '已输出前缀' },
    history_coverage_through: 900,
  } as IMessageText;
  const live = {
    ...snapshot,
    status: 'pending',
    content: { content: '已输出前缀，正在继续整理中间过程' },
    history_coverage_through: undefined,
    source_publication_sequence: 900,
  } as IMessageText;
  expect(preferTextMessageVersion(snapshot, live).content.content).toBe('已输出前缀，正在继续整理中间过程');
});

it('does not let compact history erase a richer live tool record', () => {
  const persisted = {
    id: 'tool-1',
    msg_id: 'tool-1',
    conversation_id: 'conversation',
    type: 'tool_call',
    position: 'left',
    content: {
      call_id: 'call-1',
      name: 'save_artifacts',
      status: 'running',
      input: '{"files":["docking_components_fixed.csv"]}',
      output: '保存结果…',
      _compact: { truncated: true, original_size: 12000 },
    },
  } as TMessage;
  const live = {
    ...persisted,
    content: {
      ...persisted.content,
      input: '{"files":["docking_components_fixed.csv"],"human_description":"保存修复后的组件列表"}',
      output: '{"ok":true,"artifacts":[{"filename":"docking_components_fixed.csv","rows":14}]}',
      _compact: undefined,
    },
  } as TMessage;
  const merged = mergeLoadedPageWithCurrent('conversation', [persisted], [live]);
  expect(merged).toHaveLength(1);
  expect((merged[0] as Extract<TMessage, { type: 'tool_call' }>).content.output).toContain('"rows":14');
  expect((merged[0] as Extract<TMessage, { type: 'tool_call' }>).content._compact).toBeUndefined();
});

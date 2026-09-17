import type { TMessage } from '@/common/chat/chatLib';
import { buildMessagePresentationList } from '@/renderer/pages/conversation/Messages/messageListProjection';
import { describe, expect, it } from 'vitest';

const tool = (id: string, name: string, createdAt: number): TMessage =>
  ({
    id,
    type: 'tool_call',
    position: 'left',
    created_at: createdAt,
    content: { call_id: id, name, args: {}, output: JSON.stringify({ ok: true }), status: 'completed' },
  }) as unknown as TMessage;

const text = (id: string, content: string, createdAt: number): TMessage =>
  ({
    id,
    type: 'text',
    position: 'left',
    created_at: createdAt,
    content: { content },
  }) as unknown as TMessage;

describe('message list projection controller', () => {
  it('omits orphan assistant delimiters without hiding user input or scientific symbols', () => {
    const user = { ...text('user', '></', 1), position: 'right' } as TMessage;
    const projected = buildMessagePresentationList(
      [text('orphan', '></', 0), user, text('formula', 'p < 0.05', 2), text('symbol', '∞', 3)],
      [],
      []
    );
    expect(projected.map((item) => item.id)).toEqual(['user', 'formula', 'symbol']);
  });

  it('groups adjacent tool activity and honors durable page boundaries', () => {
    const first = buildMessagePresentationList(
      [tool('tool-1', 'web_search', 1), tool('tool-2', 'web_fetch', 2)],
      [],
      []
    );
    expect(first).toHaveLength(1);
    expect(first[0].type).toBe('tool_summary');
    expect(first[0]).toMatchObject({ sourceMessageIds: ['tool-1', 'tool-2'] });

    const split = buildMessagePresentationList(
      [tool('tool-1', 'web_search', 1), tool('tool-2', 'web_fetch', 2)],
      [],
      ['tool-2']
    );
    expect(split.map((item) => item.type)).toEqual(['tool_summary', 'tool_summary']);
  });

  it('splits adjacent tool-only history at a semantic phase change', () => {
    const projected = buildMessagePresentationList(
      [tool('search-1', 'web_search', 1), tool('fetch-1', 'web_fetch', 2), tool('analysis-1', 'python', 3)],
      [],
      []
    );
    expect(projected.map((item) => item.type)).toEqual(['tool_summary', 'tool_summary']);
    expect(projected[0]).toMatchObject({ sourceMessageIds: ['search-1', 'fetch-1'] });
    expect(projected[1]).toMatchObject({ sourceMessageIds: ['analysis-1'] });
  });

  it('keeps real public progress prose between tool groups', () => {
    const projected = buildMessagePresentationList(
      [
        tool('search-1', 'web_search', 1),
        text('progress-1', '已经找到候选资料，接下来核对完整证据。', 2),
        tool('fetch-1', 'web_fetch', 3),
      ],
      [],
      []
    );
    expect(projected.map((item) => item.type)).toEqual(['tool_summary', 'text', 'tool_summary']);
    expect(projected[1]).toMatchObject({ id: 'progress-1' });
  });

  it('treats an empty assistant segment as a durable tool-group boundary', () => {
    const projected = buildMessagePresentationList(
      [tool('search-1', 'web_search', 1), text('boundary', '   ', 2), tool('search-2', 'web_search', 3)],
      [],
      []
    );
    expect(projected.map((item) => item.type)).toEqual(['tool_summary', 'tool_summary']);
  });

  it('does not project hidden transport rows', () => {
    const hidden = {
      id: 'hidden',
      type: 'text',
      position: 'left',
      created_at: 1,
      hidden: true,
      content: { content: 'internal transport row' },
    } as unknown as TMessage;
    expect(buildMessagePresentationList([hidden], [], [])).toEqual([]);
  });
});

import type { IMessageToolCall } from '@/common/chat/chatLib';
import { isStandaloneToolCall } from '@/renderer/pages/conversation/Messages/messagePresentationModel';
import { describe, expect, it } from 'vitest';

const toolCall = (name: string, extra: Partial<IMessageToolCall['content']> = {}): IMessageToolCall =>
  ({
    id: `message-${name}`,
    msg_id: `message-${name}`,
    conversation_id: 'frame-1',
    type: 'tool_call',
    position: 'left',
    created_at: 1,
    content: { call_id: `tool-${name}`, name, args: {}, ...extra },
  }) as IMessageToolCall;

describe('conversation message presentation model', () => {
  it('keeps ask_user and child-frame turns outside generic tool summaries', () => {
    expect(isStandaloneToolCall(toolCall('ask_user'))).toBe(true);
    expect(isStandaloneToolCall(toolCall('delegate', { subagent: { frameId: 'child-1', ordinal: 1 } }))).toBe(true);
    expect(isStandaloneToolCall(toolCall('python'))).toBe(false);
  });
});

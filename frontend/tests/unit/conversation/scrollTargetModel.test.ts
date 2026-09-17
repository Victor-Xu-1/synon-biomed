import { describe, expect, it } from 'vitest';
import type { TMessage } from '@/common/chat/chatLib';
import { findPreviousUserTaskIndex, isUserTaskMessage } from '@/renderer/pages/conversation/Messages/scrollTargetModel';

const message = (overrides: Partial<TMessage>): TMessage =>
  ({
    id: 'message',
    conversation_id: 'conversation',
    type: 'text',
    position: 'left',
    content: { content: '' },
    ...overrides,
  }) as TMessage;

describe('conversation scroll target model', () => {
  it('recognizes only visible logical user text tasks', () => {
    expect(isUserTaskMessage(message({ position: 'right' }))).toBe(true);
    expect(isUserTaskMessage(message({ type: 'tool_call', position: 'right' }))).toBe(false);
    expect(isUserTaskMessage(message({ type: 'agent_status', position: 'right' }))).toBe(false);
    expect(isUserTaskMessage(message({ hidden: true, position: 'right' }))).toBe(false);
    expect(
      isUserTaskMessage(
        message({
          position: 'right',
          content: { content: '', synonBiomed: { messageIndex: 3, blockIndex: 1, branchId: null } },
        })
      )
    ).toBe(false);
  });

  it('finds the nearest earlier user task and skips assistant/tool/status rows', () => {
    const messages = [
      message({ id: 'first-user', position: 'right' }),
      message({ id: 'assistant', position: 'left' }),
      message({ id: 'tool', type: 'tool_call', position: 'left' }),
      message({ id: 'second-user', position: 'right' }),
      message({ id: 'status', type: 'agent_status', position: 'center' }),
    ];

    expect(findPreviousUserTaskIndex(messages, 5)).toBe(3);
    expect(findPreviousUserTaskIndex(messages, 3)).toBe(0);
    expect(findPreviousUserTaskIndex(messages, 1)).toBe(0);
    expect(findPreviousUserTaskIndex(messages, 0)).toBe(-1);
  });
});

import { describe, expect, it } from 'vitest';
import type { IMessageAcpToolCall, IMessageToolCall } from '@/common/chat/chatLib';
import { normalizeAcpToolCall, normalizeToolCall } from '@/common/chat/normalizeToolCall';

describe('normalizeToolCall', () => {
  it('normalizes compact snake_case acp tool calls from history responses', () => {
    const result = normalizeAcpToolCall({
      id: 'message-1',
      conversation_id: 'conversation-1',
      type: 'acp_tool_call',
      content: {
        _compact: {
          truncated: true,
          original_size: 90000,
          preview_chars: 4096,
        },
        update: {
          session_update: 'tool_call',
          tool_call_id: 'tool-1',
          status: 'completed',
          title: 'rg',
          kind: 'search',
          raw_input: { pattern: 'needle', path: '.' },
          content: [{ type: 'content', content: { type: 'text', text: 'preview' } }],
        },
      },
    } as unknown as IMessageAcpToolCall);

    expect(result).toMatchObject({
      key: 'tool-1',
      name: 'rg',
      status: 'completed',
      description: '"needle" in .',
      output: 'preview',
      truncated: true,
      messageId: 'message-1',
      conversationId: 'conversation-1',
    });
  });

  it('preserves linked child-frame metadata for a Synon Biomed delegation', () => {
    const result = normalizeToolCall({
      id: 'delegate-message',
      conversation_id: 'frame-parent',
      type: 'tool_call',
      position: 'left',
      content: {
        call_id: 'call-delegate',
        name: 'delegate',
        args: { task: 'Find CRBN ligands' },
        status: 'running',
        description: 'Delegating CRBN literature research',
        subagent: {
          ordinal: 1,
          frameId: 'frame-child',
          rootFrameId: 'frame-parent',
          parentFrameId: 'frame-parent',
          agentName: 'RESEARCHER',
          delegateName: 'Literature research',
          status: 'processing',
          messageCount: 4,
          latestAction: 'Searching CRBN ligand structures',
        },
      },
    } as IMessageToolCall);

    expect(result?.subagent).toEqual({
      ordinal: 1,
      frameId: 'frame-child',
      rootFrameId: 'frame-parent',
      parentFrameId: 'frame-parent',
      agentName: 'RESEARCHER',
      delegateName: 'Literature research',
      status: 'processing',
      messageCount: 4,
      latestAction: 'Searching CRBN ligand structures',
    });
  });

  it('marks compact transcript tool calls for on-demand full detail loading', () => {
    const result = normalizeToolCall({
      id: 'message-compact',
      conversation_id: 'conversation-compact',
      type: 'tool_call',
      position: 'left',
      content: {
        call_id: 'call-compact',
        name: 'Python',
        input: '{"code":"preview"}',
        output: 'preview output',
        status: 'completed',
        _compact: { truncated: true, original_size: 90_000, result_count: 10 },
      },
    } as unknown as IMessageToolCall);

    expect(result).toMatchObject({
      key: 'call-compact',
      truncated: true,
      compactResultCount: 10,
      messageId: 'message-compact',
      conversationId: 'conversation-compact',
      input: '{"code":"preview"}',
      output: 'preview output',
    });
  });

  it('preserves the canonical canceled state for transcript tool history', () => {
    const result = normalizeToolCall({
      id: 'tool-message',
      conversation_id: 'frame-cancelled',
      type: 'tool_call',
      position: 'left',
      content: {
        call_id: 'call-cancelled',
        name: 'WebSearch',
        args: { query: 'NEK7' },
        status: 'canceled',
      },
    } as IMessageToolCall);

    expect(result).toMatchObject({
      key: 'call-cancelled',
      name: 'WebSearch',
      status: 'canceled',
    });
  });
});

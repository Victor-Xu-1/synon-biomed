import type { TMessage } from '@/common/chat/chatLib';
import { mergeConversationToolOutput } from '@/renderer/services/runtime/conversationTimelineToolOutput';
import { describe, expect, it } from 'vitest';

const tool = (
  id: string,
  conversationId: string,
  status: 'running' | 'completed',
  overrides: Partial<Extract<TMessage, { type: 'tool_call' }>['content']> = {}
): Extract<TMessage, { type: 'tool_call' }> => ({
  id,
  msg_id: id,
  conversation_id: conversationId,
  type: 'tool_call',
  position: 'left',
  status: status === 'completed' ? 'finish' : 'work',
  created_at: 10,
  content: {
    call_id: id,
    name: 'python',
    args: {},
    status,
    ...overrides,
  },
});

describe('conversation timeline tool output', () => {
  it('never carries messages across A to B to A route hydration', () => {
    const a = mergeConversationToolOutput(
      [tool('a-only', 'conversation-a', 'completed'), tool('b-only', 'conversation-b', 'completed')],
      'conversation-a',
      []
    );
    expect(a.map((message) => message.conversation_id)).toEqual(['conversation-a']);

    const b = mergeConversationToolOutput([...a, tool('b-only', 'conversation-b', 'completed')], 'conversation-b', []);
    expect(b.map((message) => message.conversation_id)).toEqual(['conversation-b']);

    const returnedA = mergeConversationToolOutput(
      [...b, tool('a-returned', 'conversation-a', 'completed')],
      'conversation-a',
      []
    );
    expect(returnedA.map((message) => message.id)).toEqual(['a-returned']);
  });

  it('merges stdout into the durable tool row without creating a shadow entity', () => {
    const persisted = tool('call-python-1', 'frame-root', 'running', {
      operation_id: 'operation-python-1',
      description: 'Analyzing structures',
      input: { human_description: 'Analyzing structures' },
    });
    const result = mergeConversationToolOutput([persisted], 'frame-root', [
      {
        frameId: 'frame-root',
        toolStdout: [
          {
            toolUseId: 'operation-python-1',
            execId: 'exec-python-1',
            stdout: 'Loaded 12 structures\n',
            stderr: '',
          },
        ],
      },
    ]);

    expect(result).toHaveLength(1);
    expect(result[0]).toMatchObject({
      id: 'call-python-1',
      hidden: false,
      content: {
        call_id: 'call-python-1',
        operation_id: 'operation-python-1',
        description: 'Analyzing structures',
        output: 'Loaded 12 structures\n',
        status: 'running',
        streaming: true,
      },
    });
  });

  it('does not revive a completed tool from a delayed stdout buffer', () => {
    const completed = tool('call-python-complete', 'frame-root', 'completed', {
      output: 'final durable output',
    });
    const result = mergeConversationToolOutput([completed], 'frame-root', [
      {
        frameId: 'frame-root',
        toolStdout: [{ toolUseId: 'call-python-complete', stdout: 'late bytes', stderr: '' }],
      },
    ]);
    expect(result).toEqual([completed]);
  });

  it('preserves prior attempts and updates only the newest active operation', () => {
    const firstAttempt = tool('attempt-event-1', 'frame-root', 'completed', {
      call_id: 'call-download-1',
      operation_id: 'operation-download',
      attempt: 1,
      revision: 2,
      output: 'first attempt evidence',
    });
    const secondAttempt = tool('attempt-event-2', 'frame-root', 'running', {
      call_id: 'call-download-1',
      operation_id: 'operation-download',
      attempt: 2,
      revision: 1,
    });
    const result = mergeConversationToolOutput([firstAttempt, secondAttempt], 'frame-root', [
      {
        frameId: 'frame-root',
        toolStdout: [{ toolUseId: 'operation-download', stdout: 'retry progress', stderr: '' }],
      },
    ]);

    expect(result).toHaveLength(2);
    expect(result[0]).toEqual(firstAttempt);
    expect(result[1]).toMatchObject({
      content: { attempt: 2, output: 'retry progress', streaming: true },
    });
  });

  it('uses only the newest execution snapshot when one tool call has recovered through multiple exec ids', () => {
    const result = mergeConversationToolOutput([tool('call-recovered', 'frame-root', 'running')], 'frame-root', [
      {
        frameId: 'frame-root',
        toolStdout: [
          {
            toolUseId: 'call-recovered',
            execId: 'exec-new',
            startedAt: '2026-09-11T08:00:01Z',
            stdout: 'new execution',
            stderr: '',
          },
          {
            toolUseId: 'call-recovered',
            execId: 'exec-old',
            startedAt: '2026-09-11T08:00:00Z',
            stdout: 'stale execution',
            stderr: '',
          },
        ],
      },
    ]);

    expect(result[0]).toMatchObject({ content: { output: 'new execution', streaming: true } });
  });
});

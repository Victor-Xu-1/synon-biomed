import { describe, expect, it } from 'vitest';
import { normalizeToolMessages } from '@/common/chat/normalizeToolCall';

describe('normalizeToolMessages', () => {
  it('does not splice a conflicting later input and detail reference onto an earlier terminal receipt', () => {
    const messages = ['first', 'second'].map((source, index) => ({
      id: source,
      conversation_id: 'frame',
      type: 'tool_call',
      content: {
        call_id: 'same-call',
        name: 'web_fetch',
        status: 'completed',
        input: { url: `https://${source}.example/article` },
        output: JSON.stringify({ url: `https://${source}.example/article` }),
        revision: index + 1,
      },
    }));
    const [tool] = normalizeToolMessages(messages as Parameters<typeof normalizeToolMessages>[0]);
    expect(tool.messageId).toBe('first');
    expect(tool.input).toContain('first.example');
    expect(tool.output).toContain('first.example');
    expect(tool.revision).toBe(1);
  });
  it('keeps bounded observed progress on the same durable tool call', () => {
    const messages = [
      {
        id: 'message-progress',
        conversation_id: 'conversation-a',
        type: 'tool_call',
        content: {
          call_id: 'environment-call',
          name: 'manage_environments',
          status: 'running',
          progress: {
            phase: 'downloading_packages',
            phasePercent: 64.2,
            bytesPerSecond: 224_320,
            bytesCompleted: 50_000_000,
            bytesTotal: 100_000_000,
            completedItems: 1,
            totalItems: 8,
            elapsedMs: 30_000,
            indeterminate: false,
          },
        },
      },
    ];

    expect(normalizeToolMessages(messages as Parameters<typeof normalizeToolMessages>[0])).toMatchObject([
      {
        key: 'environment-call',
        status: 'running',
        progress: {
          phase: 'downloading_packages',
          phasePercent: 64.2,
          bytesPerSecond: 224_320,
          bytesCompleted: 50_000_000,
          bytesTotal: 100_000_000,
          completedItems: 1,
          totalItems: 8,
          elapsedMs: 30_000,
        },
      },
    ]);
  });

  it('reconciles a resumed durable tool call into one terminal row', () => {
    const messages = [
      {
        id: 'message-terminal',
        conversation_id: 'conversation-a',
        type: 'tool_call',
        content: {
          call_id: 'call-resumed',
          name: 'python',
          status: 'error',
          output: 'safe failure summary',
        },
      },
      {
        id: 'message-stale-live',
        conversation_id: 'conversation-a',
        type: 'tool_call',
        content: {
          call_id: 'call-resumed',
          name: 'python',
          status: 'running',
          description: 'continued analysis',
        },
      },
    ];

    const tools = normalizeToolMessages(messages as Parameters<typeof normalizeToolMessages>[0]);

    expect(tools).toHaveLength(1);
    expect(tools[0]).toMatchObject({
      key: 'call-resumed',
      status: 'error',
      output: 'safe failure summary',
      description: 'continued analysis',
    });
  });

  it('does not let a conflicting later terminal update overwrite the first receipt', () => {
    const messages = ['error', 'completed'].map((status, index) => ({
      id: `message-terminal-${index}`,
      conversation_id: 'conversation-a',
      type: 'tool_call',
      content: {
        call_id: 'call-terminal',
        name: 'python',
        status,
        output: index === 0 ? 'failed evidence' : 'conflicting success',
      },
    }));

    const [tool] = normalizeToolMessages(messages as Parameters<typeof normalizeToolMessages>[0]);
    expect(tool.status).toBe('error');
    expect(tool.output).toBe('failed evidence');
  });

  it('maps an unsupported history status to unknown instead of an active pending state', () => {
    const [tool] = normalizeToolMessages([
      {
        id: 'message-unsupported-status',
        conversation_id: 'conversation-a',
        type: 'tool_call',
        content: { call_id: 'call-unsupported', name: 'python', status: 'successful' },
      } as Parameters<typeof normalizeToolMessages>[0][number],
    ]);

    expect(tool.status).toBe('unknown');
  });

  it('projects a completed transport envelope with a failed outcome as a failed tool step', () => {
    const messages = [
      {
        id: 'message-semantic-failure',
        conversation_id: 'conversation-a',
        type: 'acp_tool_call',
        content: {
          update: {
            sessionUpdate: 'tool_call_update',
            tool_call_id: 'call-oom',
            title: 'python',
            kind: 'execute',
            status: 'completed',
            content: [
              {
                type: 'content',
                content: {
                  type: 'text',
                  text: JSON.stringify({
                    ok: false,
                    status: 'failed',
                    code: 'python_memory_exhausted',
                  }),
                },
              },
            ],
          },
        },
      },
    ];

    expect(normalizeToolMessages(messages as Parameters<typeof normalizeToolMessages>[0])).toMatchObject([
      { key: 'call-oom', status: 'error' },
    ]);
  });

  it('keeps reused call ids isolated across explicit execution attempts', () => {
    const messages = [1, 2].map((attempt) => ({
      id: `message-${attempt}`,
      conversation_id: 'conversation-a',
      type: 'tool_call',
      content: {
        call_id: 'reused-call',
        operation_id: 'reused-call',
        attempt,
        revision: attempt,
        name: 'python',
        status: attempt === 1 ? 'interrupted' : 'running',
      },
    }));

    expect(normalizeToolMessages(messages as Parameters<typeof normalizeToolMessages>[0])).toMatchObject([
      { key: '1:reused-call', attempt: 1, status: 'interrupted' },
      { key: '2:reused-call', attempt: 2, status: 'running' },
    ]);
  });

  it('preserves the stable public description from compact metadata when the input preview is truncated', () => {
    const messages = [
      {
        id: 'message-compact-description',
        conversation_id: 'conversation-a',
        type: 'tool_call',
        content: {
          call_id: 'compact-call',
          name: 'python',
          status: 'completed',
          input: '{"human_description":"Compare',
          _compact: {
            truncated: true,
            human_description: 'Compare the candidate structures',
          },
          synonBiomed: { messageIndex: 4, blockIndex: 0, branchId: 'br_deadbeef' },
        },
      },
    ];

    expect(normalizeToolMessages(messages as Parameters<typeof normalizeToolMessages>[0])).toMatchObject([
      {
        key: 'compact-call',
        truncated: true,
        humanDescription: 'Compare the candidate structures',
        branchId: 'br_deadbeef',
      },
    ]);
  });
});

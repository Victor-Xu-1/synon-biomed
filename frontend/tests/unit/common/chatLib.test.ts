/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { describe, expect, it } from 'vitest';
import type { IResponseMessage } from '@/common/adapter/ipcBridge';
import {
  composeMessage,
  normalizeAgentStreamError,
  normalizeTextMessageContent,
  preferTextMessageVersion,
  transformMessage,
  type IMessageText,
  type IMessageTips,
  type IMessageAcpToolCall,
  type IMessageThinking,
  type TMessage,
} from '@/common/chat/chatLib';

const CONVERSATION_ID = 'conversation-1';

function createThinkingMessage(msgId: string, content: string): IMessageThinking {
  return {
    id: `thinking-${content}`,
    type: 'thinking',
    msg_id: msgId,
    conversation_id: CONVERSATION_ID,
    position: 'left',
    content: {
      content,
      status: 'thinking',
    },
  };
}

function createThinkingDoneMessage(msgId: string, duration: number): IMessageThinking {
  return {
    id: `thinking-done-${msgId}`,
    type: 'thinking',
    msg_id: msgId,
    conversation_id: CONVERSATION_ID,
    position: 'left',
    content: {
      content: '',
      duration,
      status: 'done',
    },
  };
}

function createToolCallMessage(toolCallId: string): IMessageAcpToolCall {
  return {
    id: toolCallId,
    type: 'acp_tool_call',
    msg_id: toolCallId,
    conversation_id: CONVERSATION_ID,
    position: 'left',
    content: {
      session_id: 'session-1',
      update: {
        sessionUpdate: 'tool_call',
        tool_call_id: toolCallId,
        status: 'completed',
        title: 'Read file',
        kind: 'read',
      },
    },
  };
}

describe('composeMessage', () => {
  it('does not let a stale empty history snapshot erase newer terminal artifact references', () => {
    const persisted = {
      id: 'persisted-answer',
      type: 'text',
      msg_id: 'answer-1',
      conversation_id: CONVERSATION_ID,
      position: 'left',
      content: { content: 'complete answer' },
      artifact_refs: [],
    } as IMessageText;
    const live = {
      ...persisted,
      id: 'live-answer',
      content: { content: 'complete answer' },
      artifact_refs: [{ artifact_id: 'artifact-1', version_id: 'version-1', relation: 'produced' as const }],
    };
    expect(preferTextMessageVersion(persisted, live).artifact_refs).toEqual(live.artifact_refs);
  });

  it('keeps the persisted terminal failure when a richer live text version wins', () => {
    const persisted = {
      id: 'persisted-answer',
      type: 'text',
      msg_id: 'answer-1',
      conversation_id: CONVERSATION_ID,
      position: 'left',
      content: { content: 'partial' },
      status: 'error',
      terminal_status: 'failed',
    } as IMessageText;
    const live = {
      ...persisted,
      id: 'live-answer',
      content: { content: 'partial response from the live stream' },
      status: 'work' as const,
      terminal_status: undefined,
    };

    const reconciled = preferTextMessageVersion(persisted, live);
    expect(reconciled.content.content).toBe('partial response from the live stream');
    expect(reconciled.status).toBe('error');
    expect(reconciled.terminal_status).toBe('failed');
  });

  it('keeps an optimistic same-input supersession while durable history catches up', () => {
    const persisted = {
      id: 'persisted-answer',
      type: 'text',
      msg_id: 'answer-1',
      conversation_id: CONVERSATION_ID,
      position: 'left',
      content: { content: 'partial' },
      status: 'error',
      terminal_status: 'failed',
    } as IMessageText;
    const continuing = { ...persisted, status: 'finish' as const, terminal_superseded: true };

    expect(preferTextMessageVersion(persisted, continuing)).toMatchObject({
      status: 'finish',
      terminal_status: 'failed',
      terminal_superseded: true,
    });
  });

  it('carries a live terminal failure into the rendered error-card contract', () => {
    const transformed = transformMessage({
      type: 'error',
      data: 'provider response truncated',
      msg_id: 'assistant-answer-1',
      conversation_id: CONVERSATION_ID,
      position: 'left',
      status: 'error',
      terminal_status: 'failed',
    });

    expect(transformed).toMatchObject({
      type: 'tips',
      status: 'error',
      terminal_status: 'failed',
    });
  });

  it('uses the durable tool publication id for immediate full-detail hydration', () => {
    const transformed = transformMessage({
      type: 'tool_call',
      data: { call_id: 'call-1', name: 'python', status: 'running' },
      msg_id: 'transcript-tool:stream-1:1:call-1',
      conversation_id: CONVERSATION_ID,
    });

    expect(transformed).toMatchObject({
      id: 'transcript-tool:stream-1:1:call-1',
      msg_id: 'transcript-tool:stream-1:1:call-1',
      type: 'tool_call',
    });
  });

  it('preserves terminal state on a typed error tip so continuation can retire it', () => {
    const transformed = transformMessage({
      type: 'tips',
      data: { content: 'response incomplete', type: 'error' },
      msg_id: 'assistant-tip-1',
      conversation_id: CONVERSATION_ID,
      position: 'center',
      status: 'error',
      terminal_status: 'failed',
      terminal_superseded: false,
    });

    expect(transformed).toMatchObject({
      type: 'tips',
      status: 'error',
      terminal_status: 'failed',
      terminal_superseded: false,
    });
  });

  it('preserves, unions, and authoritatively replaces canonical artifact references', () => {
    const first = transformMessage({
      type: 'text',
      data: 'answer',
      msg_id: 'answer-1',
      conversation_id: CONVERSATION_ID,
      artifact_refs: [{ artifact_id: 'artifact-1', version_id: 'version-1', relation: 'produced' }],
    });
    expect(first?.artifact_refs).toEqual([
      { artifact_id: 'artifact-1', version_id: 'version-1', relation: 'produced' },
    ]);
    let list = composeMessage(first, []);
    list = composeMessage(
      transformMessage({
        type: 'content',
        data: ' complete',
        msg_id: 'answer-1',
        conversation_id: CONVERSATION_ID,
        artifact_refs: [{ artifact_id: 'artifact-2', version_id: 'version-2', relation: 'produced' }],
      }),
      list
    );
    expect(list[0].artifact_refs?.map((reference) => reference.artifact_id)).toEqual(['artifact-1', 'artifact-2']);
    list = composeMessage(
      transformMessage({
        type: 'content',
        data: 'replacement',
        msg_id: 'answer-1',
        conversation_id: CONVERSATION_ID,
        replace: true,
        artifact_refs: [],
      }),
      list
    );
    expect(list[0].artifact_refs).toEqual([]);
  });

  it('preserves thinking boundaries once a tool message has been inserted', () => {
    let list: TMessage[] = [];

    list = composeMessage(createThinkingMessage('msg-1', 'alpha'), list);
    list = composeMessage(createThinkingMessage('msg-1', 'beta'), list);

    expect(list).toHaveLength(1);
    expect(list[0].type).toBe('thinking');
    expect((list[0] as IMessageThinking).content.content).toBe('alphabeta');

    list = composeMessage(createToolCallMessage('tool-1'), list);
    list = composeMessage(createThinkingMessage('msg-1', 'gamma'), list);

    expect(list).toHaveLength(3);
    expect(list.map((message) => message.type)).toEqual(['thinking', 'acp_tool_call', 'thinking']);
    expect((list[0] as IMessageThinking).content.content).toBe('alphabeta');
    expect((list[2] as IMessageThinking).content.content).toBe('gamma');
  });

  it('merges thinking done updates back into the latest matching thinking message', () => {
    let list: TMessage[] = [];

    list = composeMessage(createThinkingMessage('msg-1', 'alpha'), list);
    list = composeMessage(createToolCallMessage('tool-1'), list);
    list = composeMessage(createThinkingDoneMessage('msg-1', 3200), list);

    expect(list).toHaveLength(2);
    expect(list.map((message) => message.type)).toEqual(['thinking', 'acp_tool_call']);
    expect((list[0] as IMessageThinking).content.status).toBe('done');
    expect((list[0] as IMessageThinking).content.duration).toBe(3200);
  });
});

describe('normalizeAgentStreamError', () => {
  it('treats resolution-only error metadata as structured', () => {
    expect(
      normalizeAgentStreamError({
        message: 'Agent is still responding',
        resolution: {
          kind: 'wait_for_current_response',
        },
      })
    ).toEqual({
      message: 'Agent is still responding',
      resolution: {
        kind: 'wait_for_current_response',
      },
    });
  });

  it('drops unknown resolution kind and target values', () => {
    expect(
      normalizeAgentStreamError({
        message: 'Provider authentication failed',
        resolution: {
          kind: 'check_provider_credentials',
          target: 'unexpected_settings',
        },
      })
    ).toEqual({
      message: 'Provider authentication failed',
      resolution: {
        kind: 'check_provider_credentials',
      },
    });

    expect(
      normalizeAgentStreamError({
        message: 'Unknown recovery action',
        resolution: {
          kind: 'open_secret_panel',
          target: 'provider_settings',
        },
      })
    ).toBeUndefined();
  });

  it('preserves workspace path metadata on structured errors', () => {
    expect(
      normalizeAgentStreamError({
        message: 'The current Agent failed to run in this workspace path.',
        code: 'WORKSPACE_PATH_RUNTIME_UNAVAILABLE',
        workspacePath: '/tmp/Archive ',
      })
    ).toEqual({
      message: 'The current Agent failed to run in this workspace path.',
      code: 'WORKSPACE_PATH_RUNTIME_UNAVAILABLE',
      workspacePath: '/tmp/Archive ',
    });
  });

  it('preserves the rawError diagnostic summary on internal errors', () => {
    expect(
      normalizeAgentStreamError({
        message: 'Something went wrong, please try again.',
        code: 'SYNON_AI_INTERNAL_ERROR',
        rawError: {
          name: 'Error',
          message: 'connect ECONNREFUSED',
          code: 'ECONNREFUSED',
          status: 500,
          stack: 'Error: connect ECONNREFUSED\n    at frame',
        },
      })
    ).toEqual({
      message: 'Something went wrong, please try again.',
      code: 'SYNON_AI_INTERNAL_ERROR',
      rawError: {
        name: 'Error',
        message: 'connect ECONNREFUSED',
        code: 'ECONNREFUSED',
        status: 500,
        stack: 'Error: connect ECONNREFUSED\n    at frame',
      },
    });
  });

  it('drops malformed rawError fields and keeps only valid ones', () => {
    expect(
      normalizeAgentStreamError({
        message: 'Something went wrong, please try again.',
        code: 'SYNON_AI_INTERNAL_ERROR',
        rawError: {
          name: 'Error',
          message: 42,
          status: 'not-a-number',
          extra: 'ignored',
        },
      })
    ).toEqual({
      message: 'Something went wrong, please try again.',
      code: 'SYNON_AI_INTERNAL_ERROR',
      rawError: {
        name: 'Error',
      },
    });
  });

  it('omits rawError when it has no usable fields', () => {
    expect(
      normalizeAgentStreamError({
        message: 'Something went wrong, please try again.',
        code: 'SYNON_AI_INTERNAL_ERROR',
        rawError: { unrelated: true },
      })
    ).toEqual({
      message: 'Something went wrong, please try again.',
      code: 'SYNON_AI_INTERNAL_ERROR',
    });
  });
});

describe('normalizeTextMessageContent', () => {
  it('normalizes JSON string text content from persisted DB payloads', () => {
    expect(
      normalizeTextMessageContent(
        JSON.stringify({
          content: 'persisted response',
          retired_metadata: true,
        })
      )
    ).toEqual({
      content: 'persisted response',
    });
  });

  it('keeps ordinary string text messages as plain content', () => {
    expect(normalizeTextMessageContent('hello')).toEqual({
      content: 'hello',
    });
  });

  it('lets stream-level replace override a plain text payload', () => {
    expect(normalizeTextMessageContent('replacement text', { replace: true })).toEqual({
      content: 'replacement text',
      replace: true,
    });
  });

  it('preserves validated canonical branch coordinates from persisted messages', () => {
    expect(
      normalizeTextMessageContent({
        content: 'branch prompt',
        synonBiomed: { messageIndex: 4, blockIndex: 0, branchId: 'br_12ab34cd' },
      })
    ).toEqual({
      content: 'branch prompt',
      synonBiomed: { messageIndex: 4, blockIndex: 0, branchId: 'br_12ab34cd' },
    });
  });

  it('drops malformed branch coordinates instead of authorizing a guessed fork source', () => {
    expect(
      normalizeTextMessageContent({
        content: 'branch prompt',
        synonBiomed: { messageIndex: -1, blockIndex: 0, branchId: 'main' },
      })
    ).toEqual({ content: 'branch prompt' });
  });
});

describe('transformMessage', () => {
  it('returns undefined for hidden system stream messages', () => {
    const message: IResponseMessage = {
      type: 'system',
      data: 'cron metadata',
      msg_id: 'system-1',
      conversation_id: CONVERSATION_ID,
      hidden: true,
    };

    expect(transformMessage(message)).toBeUndefined();
  });

  it('uses explicit stream position for team projected user text messages', () => {
    const message: IResponseMessage = {
      type: 'text',
      data: {
        content: '你好',
      },
      msg_id: 'team-user-message-1',
      conversation_id: CONVERSATION_ID,
      position: 'right',
      status: 'finish',
      replace: true,
    };

    const transformed = transformMessage(message) as IMessageText;

    expect(transformed.type).toBe('text');
    expect(transformed.position).toBe('right');
    expect(transformed.status).toBe('finish');
    expect(transformed.content).toEqual({
      content: '你好',
      replace: true,
    });
  });

  it('keeps plain text stream messages left when no explicit position is present', () => {
    const message: IResponseMessage = {
      type: 'text',
      data: {
        content: 'agent response',
      },
      msg_id: 'agent-message-1',
      conversation_id: CONVERSATION_ID,
    };

    const transformed = transformMessage(message) as IMessageText;

    expect(transformed.type).toBe('text');
    expect(transformed.position).toBe('left');
    expect(transformed.content.content).toBe('agent response');
  });

  it('preserves structured agent stream error metadata', () => {
    const message: IResponseMessage = {
      type: 'error',
      data: {
        message: 'The model provider rejected the request',
        code: 'USER_LLM_PROVIDER_AUTH_FAILED',
        ownership: 'user_llm_provider',
        detail: 'Provider returned 401.',
        workspacePath: '/tmp/provider-test',
        retryable: false,
        feedback_recommended: false,
        resolution: {
          kind: 'check_provider_credentials',
          target: 'provider_settings',
        },
      },
      msg_id: 'error-1',
      conversation_id: CONVERSATION_ID,
    };

    const transformed = transformMessage(message) as IMessageTips;

    expect(transformed.type).toBe('tips');
    expect(transformed.content.content).toBe('The model provider rejected the request');
    expect(transformed.content.error).toEqual({
      message: 'The model provider rejected the request',
      code: 'USER_LLM_PROVIDER_AUTH_FAILED',
      ownership: 'user_llm_provider',
      detail: 'Provider returned 401.',
      workspacePath: '/tmp/provider-test',
      retryable: false,
      feedback_recommended: false,
      resolution: {
        kind: 'check_provider_credentials',
        target: 'provider_settings',
      },
    });
  });

  it('preserves structured metadata on live tips error messages', () => {
    const message: IResponseMessage = {
      type: 'tips',
      data: {
        content: 'SynonAI failed while sending the message',
        type: 'error',
        source: 'send_failed',
        code: 'INTERNAL_ERROR',
        error: {
          message: 'SynonAI failed while sending the message',
          code: 'SYNON_AI_INTERNAL_ERROR',
          ownership: 'synon-ai',
          detail: 'Failed to write Codex sandbox config',
          retryable: true,
          feedback_recommended: true,
          resolution: {
            kind: 'send_feedback',
            target: 'feedback',
          },
        },
      },
      msg_id: 'tips-error-1',
      conversation_id: CONVERSATION_ID,
    };

    const transformed = transformMessage(message) as IMessageTips;

    expect(transformed.type).toBe('tips');
    expect(transformed.content.error).toEqual({
      message: 'SynonAI failed while sending the message',
      code: 'SYNON_AI_INTERNAL_ERROR',
      ownership: 'synon-ai',
      detail: 'Failed to write Codex sandbox config',
      retryable: true,
      feedback_recommended: true,
      resolution: {
        kind: 'send_feedback',
        target: 'feedback',
      },
    });
  });

  it('preserves info tip type with code and params', () => {
    const message: IResponseMessage = {
      type: 'tips',
      data: {
        content: 'Select a slash command to continue',
        type: 'info',
        code: 'acp.empty_turn.choose_command',
        params: {
          command_count: 3,
        },
      },
      msg_id: 'tips-info-1',
      conversation_id: CONVERSATION_ID,
    };

    const transformed = transformMessage(message) as IMessageTips;

    expect(transformed.type).toBe('tips');
    expect(transformed.content).toMatchObject({
      content: 'Select a slash command to continue',
      type: 'info',
      code: 'acp.empty_turn.choose_command',
      params: {
        command_count: 3,
      },
    });
  });
});

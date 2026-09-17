/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { describe, expect, it } from 'vitest';
import { normalizeDbMessage } from '@/renderer/pages/conversation/Messages/hooks';
import type { IMessageText, IMessageTips } from '@/common/chat/chatLib';

describe('normalizeDbMessage', () => {
  it('validates and preserves canonical persisted artifact references', () => {
    const normalized = normalizeDbMessage({
      id: 'message-with-artifact',
      msg_id: 'message-with-artifact',
      type: 'text',
      conversation_id: 'conversation-1',
      position: 'left',
      status: 'finish',
      content: { content: 'result' },
      artifact_refs: [
        {
          artifact_id: 'artifact-1',
          version_id: 'version-1',
          relation: 'produced',
          availability: 'available',
          attempt: 1,
          source_event_id: 4,
          ordinal: 0,
        },
      ],
    } as IMessageText) as IMessageText;
    expect(normalized.artifact_refs).toHaveLength(1);
    expect(normalized.artifact_refs?.[0]).toMatchObject({ artifact_id: 'artifact-1', version_id: 'version-1' });
  });

  it('fails closed for malformed persisted artifact references', () => {
    expect(() =>
      normalizeDbMessage({
        id: 'message-with-invalid-artifact',
        type: 'text',
        conversation_id: 'conversation-1',
        content: { content: 'result' },
        artifact_refs: [{ artifact_id: 'artifact-1', version_id: '', relation: 'produced' }],
      } as unknown as IMessageText)
    ).toThrow('conversation_artifact_refs_invalid');
  });

  it('keeps persisted info tip localization metadata from db content', () => {
    const normalized = normalizeDbMessage({
      id: 'tip-info',
      type: 'tips',
      conversation_id: 'conversation-1',
      position: 'center',
      status: 'finish',
      content: JSON.stringify({
        content: '',
        type: 'info',
        code: 'ACP_EMPTY_TURN',
        params: {
          provider: 'OpenCode',
        },
      }),
    } as unknown as IMessageTips) as IMessageTips;

    expect(normalized.content).toEqual({
      content: '',
      type: 'info',
      code: 'ACP_EMPTY_TURN',
      params: {
        provider: 'OpenCode',
      },
    });
  });

  it('keeps structured error metadata from persisted tips', () => {
    const normalized = normalizeDbMessage({
      id: 'tip-structured',
      type: 'tips',
      conversation_id: 'conversation-1',
      position: 'center',
      status: 'error',
      content: JSON.stringify({
        content: 'The upstream Agent failed while handling the request',
        type: 'error',
        source: 'send_failed',
        code: 'BAD_GATEWAY',
        error: {
          message: 'The upstream Agent failed while handling the request',
          code: 'UNKNOWN_UPSTREAM_ERROR',
          ownership: 'unknown_upstream',
          detail: 'ACP init failed: config file is invalid',
          retryable: true,
          feedback_recommended: true,
          resolution: {
            kind: 'start_new_session',
            target: 'new_conversation',
          },
        },
      }),
    } as unknown as IMessageTips) as IMessageTips;

    expect(normalized.content.error).toEqual({
      message: 'The upstream Agent failed while handling the request',
      code: 'UNKNOWN_UPSTREAM_ERROR',
      ownership: 'unknown_upstream',
      detail: 'ACP init failed: config file is invalid',
      retryable: true,
      feedback_recommended: true,
      resolution: {
        kind: 'start_new_session',
        target: 'new_conversation',
      },
    });
  });

  it('restores persisted send failure tips as structured agent errors', () => {
    const normalized = normalizeDbMessage({
      id: 'tip-1',
      type: 'tips',
      conversation_id: 'conversation-1',
      position: 'center',
      status: 'error',
      content: JSON.stringify({
        content: 'Bad gateway: ACP init failed: config file is invalid',
        type: 'error',
        source: 'send_failed',
        code: 'BAD_GATEWAY',
      }),
    } as unknown as IMessageTips) as IMessageTips;

    expect(normalized.content).toEqual({
      content: 'Bad gateway: ACP init failed: config file is invalid',
      type: 'error',
      error: {
        message: 'Bad gateway: ACP init failed: config file is invalid',
        code: 'UNKNOWN_UPSTREAM_ERROR',
        ownership: 'unknown_upstream',
        detail: 'Bad gateway: ACP init failed: config file is invalid',
        retryable: true,
        feedback_recommended: true,
      },
    });
  });

  it('prefers persisted workspace runtime errors over legacy unknown-upstream payloads', () => {
    const normalized = normalizeDbMessage({
      id: 'tip-runtime-workspace',
      type: 'tips',
      conversation_id: 'conversation-1',
      position: 'center',
      status: 'error',
      content: JSON.stringify({
        content: 'The current Agent failed to run in this workspace path',
        type: 'error',
        source: 'send_failed',
        code: 'WORKSPACE_PATH_CONTAINS_WHITESPACE_RUNTIME_UNSUPPORTED',
        details: {
          workspace_path: '/Users/zhoukai/Documents/Archive ',
        },
        error: {
          message: 'The current Agent failed to run in this workspace path',
          code: 'UNKNOWN_UPSTREAM_ERROR',
          ownership: 'unknown_upstream',
          detail: '/Users/zhoukai/Documents/Archive . Make sure the workspace path exists and is accessible.',
          retryable: true,
          feedback_recommended: true,
        },
      }),
    } as unknown as IMessageTips) as IMessageTips;

    expect(normalized.content.error).toEqual({
      message: 'The current Agent failed to run in this workspace path',
      code: 'WORKSPACE_PATH_RUNTIME_UNAVAILABLE',
      ownership: 'synon-ai',
      detail: '/Users/zhoukai/Documents/Archive . Make sure the workspace path exists and is accessible.',
      workspacePath: '/Users/zhoukai/Documents/Archive ',
      retryable: false,
      feedback_recommended: false,
    });
  });

  it('drops unknown metadata when persisted text content is already an object', () => {
    const normalized = normalizeDbMessage({
      id: 'message-object',
      type: 'text',
      conversation_id: 'conversation-1',
      position: 'left',
      status: 'finish',
      content: {
        content: 'persisted response',
        retired_metadata: true,
      },
    } as unknown as IMessageText) as IMessageText;

    expect(normalized.content).toEqual({
      content: 'persisted response',
    });
  });

  it('drops unknown metadata when persisted text content is a JSON string', () => {
    const normalized = normalizeDbMessage({
      id: 'message-json',
      type: 'text',
      conversation_id: 'conversation-1',
      position: 'left',
      status: 'finish',
      content: JSON.stringify({
        content: 'persisted JSON response',
        retired_metadata: true,
      }),
    } as unknown as IMessageText) as IMessageText;

    expect(normalized.content).toEqual({
      content: 'persisted JSON response',
    });
  });

  it('keeps ordinary recovered text messages as plain text content', () => {
    const normalized = normalizeDbMessage({
      id: 'ordinary-text',
      type: 'text',
      conversation_id: 'conversation-1',
      position: 'left',
      status: 'finish',
      content: {
        content: 'ordinary assistant response',
      },
    } as unknown as IMessageText) as IMessageText;

    expect(normalized.content).toEqual({
      content: 'ordinary assistant response',
    });
  });
});

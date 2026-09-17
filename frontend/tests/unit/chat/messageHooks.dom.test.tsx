/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import React, { useEffect } from 'react';
import { act, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { IMessageAcpToolCall, TMessage } from '@/common/chat/chatLib';
import {
  MessageListProvider,
  useAddOrUpdateMessage,
  useMessageList,
} from '@/renderer/pages/conversation/Messages/hooks';

const createImageToolCall = (id: string): IMessageAcpToolCall => ({
  id,
  msg_id: id,
  conversation_id: 'conv-1',
  type: 'acp_tool_call',
  content: {
    sessionId: 'sess-1',
    update: {
      sessionUpdate: 'tool_call_update',
      tool_call_id: id,
      status: 'completed',
      title: 'Image generation',
      kind: 'execute',
      rawOutput: {
        saved_path: `/Users/test/.codex/generated_images/session/${id}.png`,
        result: `iVBORw0KGgo${'A'.repeat(128 * 1024)}`,
      },
    },
  },
});

const existingTextMessage: TMessage = {
  id: 'text-1',
  msg_id: 'text-1',
  conversation_id: 'conv-1',
  type: 'text',
  position: 'left',
  content: {
    content: 'hello',
  },
};

const MessageListProbe: React.FC<{ message: TMessage; add?: boolean }> = ({ message, add = false }) => {
  const addOrUpdateMessage = useAddOrUpdateMessage();
  const messages = useMessageList();

  useEffect(() => {
    addOrUpdateMessage(message, add);
  }, [add, addOrUpdateMessage, message]);

  const acpMessage = messages.find((item): item is IMessageAcpToolCall => item.type === 'acp_tool_call');
  const rawOutput = acpMessage?.content.update.rawOutput;

  return (
    <div>
      <div data-testid='message-count'>{messages.length}</div>
      <div data-testid='last-message-type'>{messages.at(-1)?.type ?? ''}</div>
      <div data-testid='last-text-content'>
        {messages.at(-1)?.type === 'text' ? messages.at(-1)?.content.content : ''}
      </div>
      <div data-testid='has-result'>{String(Boolean(rawOutput?.result))}</div>
      <div data-testid='image-path'>{rawOutput?.image?.path ?? ''}</div>
    </div>
  );
};

const renderMessageListProbe = (message: TMessage, options?: { add?: boolean; initial?: TMessage[] }) =>
  render(
    <MessageListProvider value={options?.initial ?? []}>
      <MessageListProbe message={message} add={options?.add} />
    </MessageListProvider>
  );

const flushNextMessageUpdate = () => {
  act(() => {
    vi.advanceTimersToNextTimer();
  });
};

describe('conversation message hooks ACP sanitization', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('sanitizes an ACP image message inserted into an empty list', () => {
    renderMessageListProbe(createImageToolCall('ig_first_image'));

    flushNextMessageUpdate();

    expect(screen.getByTestId('message-count')).toHaveTextContent('1');
    expect(screen.getByTestId('has-result')).toHaveTextContent('false');
    expect(screen.getByTestId('image-path')).toHaveTextContent(
      '/Users/test/.codex/generated_images/session/ig_first_image.png'
    );
  });

  it('sanitizes an ACP image message without msg_id inserted into an empty list', () => {
    const message = createImageToolCall('ig_first_image_without_msg_id') as IMessageAcpToolCall & {
      msg_id?: string;
    };
    delete message.msg_id;

    renderMessageListProbe(message);

    flushNextMessageUpdate();

    expect(screen.getByTestId('message-count')).toHaveTextContent('1');
    expect(screen.getByTestId('has-result')).toHaveTextContent('false');
    expect(screen.getByTestId('image-path')).toHaveTextContent(
      '/Users/test/.codex/generated_images/session/ig_first_image_without_msg_id.png'
    );
  });

  it('sanitizes an ACP image message inserted with add=true', () => {
    renderMessageListProbe(createImageToolCall('ig_added_image'), {
      add: true,
      initial: [existingTextMessage],
    });

    flushNextMessageUpdate();

    expect(screen.getByTestId('message-count')).toHaveTextContent('2');
    expect(screen.getByTestId('has-result')).toHaveTextContent('false');
    expect(screen.getByTestId('image-path')).toHaveTextContent(
      '/Users/test/.codex/generated_images/session/ig_added_image.png'
    );
  });

  it('sanitizes a new ACP image message appended to a non-empty list', () => {
    renderMessageListProbe(createImageToolCall('ig_appended_image'), {
      initial: [existingTextMessage],
    });

    flushNextMessageUpdate();

    expect(screen.getByTestId('message-count')).toHaveTextContent('2');
    expect(screen.getByTestId('last-message-type')).toHaveTextContent('acp_tool_call');
    expect(screen.getByTestId('has-result')).toHaveTextContent('false');
    expect(screen.getByTestId('image-path')).toHaveTextContent(
      '/Users/test/.codex/generated_images/session/ig_appended_image.png'
    );
  });

  it('keeps non-ACP messages unchanged when inserted with add=true', () => {
    renderMessageListProbe(
      {
        ...existingTextMessage,
        id: 'text-2',
        msg_id: 'text-2',
        content: {
          content: 'world',
        },
      },
      {
        add: true,
        initial: [existingTextMessage],
      }
    );

    flushNextMessageUpdate();

    expect(screen.getByTestId('message-count')).toHaveTextContent('2');
    expect(screen.getByTestId('last-message-type')).toHaveTextContent('text');
    expect(screen.getByTestId('has-result')).toHaveTextContent('false');
  });

  it('removes a rejected candidate across a tool boundary before appending its correction', () => {
    const toolMessage: TMessage = {
      id: 'tool-1',
      msg_id: 'tool-1',
      conversation_id: 'conv-1',
      type: 'tool_call',
      position: 'left',
      content: {
        call_id: 'call-1',
        name: 'WebFetch',
        args: {},
        status: 'completed',
      },
    };
    const rejected = { ...existingTextMessage, id: 'assistant-1', msg_id: 'assistant-1' };
    const rejectedAfterTool = {
      ...rejected,
      id: 'assistant-1-after-tool',
      content: { content: 'second rejected segment' },
    };
    const reset: TMessage = {
      ...rejected,
      content: { content: '', replace: true },
    };
    const rendered = renderMessageListProbe(reset, { initial: [rejected, toolMessage, rejectedAfterTool] });

    flushNextMessageUpdate();

    expect(screen.getByTestId('message-count')).toHaveTextContent('1');
    expect(screen.getByTestId('last-message-type')).toHaveTextContent('tool_call');

    rendered.rerender(
      <MessageListProvider value={[rejected, toolMessage, rejectedAfterTool]}>
        <MessageListProbe message={{ ...rejected, content: { content: 'verified replacement' } }} />
      </MessageListProvider>
    );
    flushNextMessageUpdate();

    expect(screen.getByTestId('message-count')).toHaveTextContent('2');
    expect(screen.getByTestId('last-message-type')).toHaveTextContent('text');
    expect(screen.getByTestId('last-text-content')).toHaveTextContent('verified replacement');
  });

  it('replaces every rejected assistant segment sharing the generation id', () => {
    const toolMessage: TMessage = {
      id: 'tool-2',
      msg_id: 'tool-2',
      conversation_id: 'conv-1',
      type: 'tool_call',
      position: 'left',
      content: { call_id: 'call-2', name: 'WebFetch', args: {}, status: 'completed' },
    };
    const beforeTool = {
      ...existingTextMessage,
      id: 'assistant-generation-a',
      msg_id: 'assistant-generation',
      content: { content: 'rejected before tool' },
    };
    const afterTool = {
      ...beforeTool,
      id: 'assistant-generation-b',
      content: { content: 'rejected after tool' },
    };
    renderMessageListProbe(
      { ...afterTool, content: { content: 'bounded failure', replace: true } },
      { initial: [beforeTool, toolMessage, afterTool] }
    );

    flushNextMessageUpdate();

    expect(screen.getByTestId('message-count')).toHaveTextContent('2');
    expect(screen.getByTestId('last-message-type')).toHaveTextContent('text');
    expect(screen.getByTestId('last-text-content')).toHaveTextContent('bounded failure');
  });
});

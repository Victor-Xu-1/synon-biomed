/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { IMessageToolCall } from '@/common/chat/chatLib';
import AskUserHistoryCard from '@/renderer/components/synonBiomed/runtime/AskUserHistoryCard';
import React from 'react';
import MessageSubagentEvents from './MessageSubagentEvents';
import MessageSubagentTurn from './MessageSubagentTurn';
import MessageToolGroupSummary from './MessageToolGroupSummary';

// Standalone tool calls are limited to dedicated interactive projections.
// Every ordinary tool call, including unexpected compatibility input, reuses
// the same public summary boundary as grouped transcript tools so no second raw
// name/input/output renderer can compete with the canonical conversation UI.
const MessageToolCall: React.FC<{
  message: IMessageToolCall;
}> = ({ message }) => {
  if (message.content.name === 'ask_user') {
    return (
      <AskUserHistoryCard
        conversationId={message.conversation_id}
        sourceBranchId={message.content.synonBiomed?.branchId ?? null}
        toolUseId={message.content.call_id}
        input={message.content.input ?? message.content.args}
        output={message.content.output}
      />
    );
  }

  if (message.content.subagent) return <MessageSubagentTurn message={message} />;
  if (message.content.subagentEvents) return <MessageSubagentEvents message={message} />;
  return <MessageToolGroupSummary messages={[message]} />;
};

export default MessageToolCall;

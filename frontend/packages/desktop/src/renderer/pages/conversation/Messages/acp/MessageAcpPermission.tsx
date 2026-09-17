/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { IMessageAcpPermission } from '@/common/chat/chatLib';
import { ipcBridge } from '@/common';
import React from 'react';
import { useTranslation } from 'react-i18next';
import {
  ConversationApprovalCard,
  type ConversationApprovalOption,
} from '@/renderer/pages/conversation/Messages/components/MessagePermission';

interface MessageAcpPermissionProps {
  message: IMessageAcpPermission;
}

const MessageAcpPermission: React.FC<MessageAcpPermissionProps> = React.memo(({ message }) => {
  const { options = [], tool_call } = message.content || {};
  const { t } = useTranslation();
  const title = tool_call?.title || tool_call?.raw_input?.description || t('messages.permissionRequest');

  if (!tool_call) {
    return null;
  }

  const approvalOptions: ConversationApprovalOption[] = options.map((option, index) => ({
    label: option.name || `${t('messages.option')} ${index + 1}`,
    value: option.option_id || `option_${index}`,
    alwaysAllow: option.kind === 'allow_always',
  }));

  return (
    <ConversationApprovalCard
      title={title}
      description={tool_call.raw_input?.description}
      command={tool_call.raw_input?.command || tool_call.title}
      options={approvalOptions}
      cardTestId='message-acp-permission-card'
      optionTestIdPrefix='message-acp-permission-option'
      confirmTestId='message-acp-permission-confirm'
      onConfirm={(option) =>
        ipcBridge.conversation.confirmation.confirm.invoke({
          conversation_id: message.conversation_id,
          call_id: tool_call.tool_call_id || message.id,
          msg_id: message.msg_id || message.id,
          data: option.value,
          always_allow: option.alwaysAllow ?? false,
        })
      }
    />
  );
});

export default MessageAcpPermission;

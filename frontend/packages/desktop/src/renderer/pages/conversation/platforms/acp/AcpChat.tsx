/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { IConversationMcpStatus, TChatConversation } from '@/common/config/storage';
import { ConversationProvider } from '@/renderer/hooks/context/ConversationContext';
import { useAuth } from '@/renderer/hooks/context/AuthContext';
import { CHAT_SURFACE_CONTAINER_CLASS } from '@/renderer/pages/conversation/utils/chatSurfaceWidth';
import FlexFullContainer from '@renderer/components/layout/FlexFullContainer';
import MessageList from '@renderer/pages/conversation/Messages/MessageList';
import { ConversationArtifactProvider } from '@renderer/pages/conversation/Messages/artifacts';
import {
  MessageListLoadingProvider,
  MessageListLoadErrorProvider,
  MessageListProvider,
  MessagePaginationProvider,
  useMessageLstCache,
} from '@renderer/pages/conversation/Messages/hooks';
import { isMessageRequestAbort } from '@renderer/pages/conversation/Messages/messageRequestAbort';
import { usePendingConfirmationsRecovery } from '@renderer/pages/conversation/Messages/usePendingConfirmationsRecovery';
import { useConversationRuntimeView } from '@/renderer/pages/conversation/runtime/useConversationRuntimeView';
import HOC from '@renderer/utils/ui/HOC';
import React, { useCallback, useEffect, useState } from 'react';
import SynonBiomedDelegateDock from '../../components/SynonBiomedDelegateDock';
import AcpSendBox from './AcpSendBox';
import { useAcpMessage } from './useAcpMessage';
import {
  type SynonBiomedConversationSyncIssue,
  useSynonBiomedConversationSync,
} from './useSynonBiomedConversationSync';

const AcpChat: React.FC<{
  conversation: TChatConversation;
  conversation_id: string;
  workspace?: string;
  backend: string;
  projectId?: string;
  currentFrameId?: string;
  session_mode?: string;
  agent_name?: string;
  cron_job_id?: string;
  hideSendBox?: boolean;
  emptySlot?: React.ReactNode;
  loadedSkills?: string[];
  loadedMcpServers?: string[];
  loadedMcpStatuses?: IConversationMcpStatus[];
  assistantId?: string;
}> = ({
  conversation,
  conversation_id,
  workspace,
  backend,
  projectId,
  currentFrameId,
  session_mode,
  agent_name,
  cron_job_id,
  hideSendBox,
  emptySlot,
  loadedSkills,
  loadedMcpServers,
  loadedMcpStatuses,
  assistantId,
}) => {
  const { user } = useAuth();
  const [syncIssue, setSyncIssue] = useState<SynonBiomedConversationSyncIssue | null>(null);
  const isSynonBiomedConversation =
    backend.trim().toLowerCase() === 'synonbiomed' || Boolean(workspace?.startsWith('synonbiomed://'));
  const refreshMessages = useMessageLstCache(conversation_id, user?.id ?? '');
  const retryMessageLoad = useCallback(() => {
    void refreshMessages(false, 'refresh').catch((error) => {
      console.error('[AcpChat] Failed to retry conversation messages:', error);
    });
  }, [refreshMessages]);
  const hydrateMessagesAfterRuntimeMutation = useCallback(() => {
    void refreshMessages(false, 'refresh').catch((error) => {
      if (isMessageRequestAbort(error)) return;
      console.error('[AcpChat] Failed to hydrate messages after runtime mutation');
    });
  }, [refreshMessages]);
  // Synon Biomed has one approval authority: the runtime snapshot's
  // pending_input_requests rendered by SynonBiomedRuntimeOperations. Keep the
  // compatibility confirmation recovery only for non-Synon ACP backends so
  // one request can never produce two competing approval cards.
  usePendingConfirmationsRecovery(conversation_id, {
    enabled: !isSynonBiomedConversation,
  });
  const messageState = useAcpMessage(conversation_id, {
    skipWarmup: isSynonBiomedConversation,
    initialConversation: conversation,
  });
  const runtimeView = useConversationRuntimeView(conversation_id, conversation);
  const handleSyncIssue = useCallback((issue: SynonBiomedConversationSyncIssue | null) => {
    setSyncIssue(issue);
  }, []);
  useEffect(() => setSyncIssue(null), [conversation_id]);
  useSynonBiomedConversationSync({
    conversationId: conversation_id,
    initialConversation: conversation,
    workspace,
    backend,
    enabled: messageState.aiProcessing || messageState.running || runtimeView.isProcessing,
    initialRuntime: conversation.runtime,
    runtimeActive: runtimeView.isProcessing && !runtimeView.view.localSubmitting,
    pendingLocalSend: runtimeView.view.localSubmitting,
    refreshMessages,
    getLastStreamActivityAt: messageState.getLastStreamActivityAt,
    onSettled: messageState.resetState,
    onIssue: handleSyncIssue,
  });

  return (
    <ConversationProvider
      value={{
        conversation_id: conversation_id,
        workspace,
        type: 'acp',
        projectId,
        currentFrameId,
        cron_job_id,
        hideSendBox,
        loadedSkills,
        loadedMcpServers,
        loadedMcpStatuses,
        assistantId,
      }}
    >
      <ConversationArtifactProvider conversation_id={conversation_id}>
        <div className={`${CHAT_SURFACE_CONTAINER_CLASS} flex-1 flex flex-col px-20px min-h-0`}>
          <FlexFullContainer>
            <MessageList
              className='flex-1'
              emptySlot={emptySlot}
              onRetryLoad={retryMessageLoad}
              ownerId={user?.id ?? ''}
            />
          </FlexFullContainer>
          {isSynonBiomedConversation && (
            <SynonBiomedDelegateDock
              conversationId={conversation_id}
              polling={messageState.aiProcessing || messageState.running || runtimeView.isProcessing}
            />
          )}
          {!hideSendBox && syncIssue?.kind !== 'missing' && (
            <AcpSendBox
              conversation_id={conversation_id}
              backend={backend}
              session_mode={session_mode}
              agent_name={agent_name}
              workspacePath={workspace}
              messageState={messageState}
              onRuntimeMutation={hydrateMessagesAfterRuntimeMutation}
              initialConversation={conversation}
              ownerId={user?.id}
            ></AcpSendBox>
          )}
        </div>
      </ConversationArtifactProvider>
    </ConversationProvider>
  );
};

export default HOC.Wrapper(
  MessageListProvider,
  MessageListLoadingProvider,
  MessageListLoadErrorProvider,
  MessagePaginationProvider
)(AcpChat);

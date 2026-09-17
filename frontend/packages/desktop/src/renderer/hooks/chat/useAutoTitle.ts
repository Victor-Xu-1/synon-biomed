import { useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { ipcBridge } from '@/common';
import type { TChatConversation } from '@/common/config/storage';
import { deriveAutoTitleFromMessages } from '@/renderer/utils/chat/autoTitle';
import { DEFAULT_MESSAGE_PAGE_LIMIT, loadLatestConversationMessages } from '@/renderer/utils/chat/messagePagination';
import { loadLatestConversationMessagesShared } from '@/renderer/pages/conversation/Messages/messageWindowPrefetch';
import { getSynonBiomedBranchSelectionRevision } from '@/renderer/services/synonBiomedConversationBranches';
import { emitter } from '@/renderer/utils/emitter';
import { getConversationOrNull } from '@/renderer/pages/conversation/utils/conversationCache';

type AutoTitleSyncOptions = {
  /**
   * The conversation route has already fetched this authoritative snapshot.
   * Reusing it avoids a second detail read during first paint.
   */
  conversation?: TChatConversation;
};

export const useAutoTitle = (ownerId = '', initialConversation?: TChatConversation) => {
  const { t } = useTranslation();

  const syncTitleFromHistory = useCallback(
    async (conversation_id: string, fallbackContent?: string, options?: AutoTitleSyncOptions) => {
      const defaultTitle = t('conversation.welcome.newConversation');
      try {
        const conversation =
          options?.conversation?.id === conversation_id
            ? options.conversation
            : initialConversation?.id === conversation_id
              ? initialConversation
              : await getConversationOrNull(conversation_id);
        if (!conversation || conversation.name !== defaultTitle) {
          return;
        }

        const messages = ownerId.trim()
          ? await loadLatestConversationMessagesShared({
              ownerId,
              conversationId: conversation_id,
              branchRevision: getSynonBiomedBranchSelectionRevision(conversation_id),
              limit: DEFAULT_MESSAGE_PAGE_LIMIT,
              contentMode: 'compact',
            })
          : await loadLatestConversationMessages(conversation_id, {
              limit: DEFAULT_MESSAGE_PAGE_LIMIT,
              contentMode: 'compact',
            });
        const newTitle = deriveAutoTitleFromMessages(messages.items, fallbackContent);
        if (!newTitle) {
          return;
        }

        const success = await ipcBridge.conversation.update.invoke({
          id: conversation_id,
          updates: { name: newTitle },
        });
        if (!success) {
          return;
        }

        emitter.emit('chat.history.refresh');
      } catch (error) {
        console.error('Failed to auto-update conversation title:', error);
      }
    },
    [initialConversation, ownerId, t]
  );

  const checkAndUpdateTitle = useCallback(
    async (conversation_id: string, messageContent: string) => {
      await syncTitleFromHistory(conversation_id, messageContent);
    },
    [syncTitleFromHistory]
  );

  return {
    checkAndUpdateTitle,
    syncTitleFromHistory,
  };
};

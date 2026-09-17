import { useCallback, useEffect } from 'react';
import useSWR from 'swr';
import {
  clearSendBoxDraft,
  loadSendBoxDraft,
  saveSendBoxDraft,
  sendBoxDraftStorageKey,
  type AcpSendBoxDraft,
} from './sendBoxDraftPersistence';
export type { FileOrFolderItem } from '@/renderer/utils/file/fileTypes';

type Draft = AcpSendBoxDraft;

/**
 * 当前支持的对话类型以及对应的草稿对象
 */
type DraftConversationType = Draft['_type'];
type SendBoxDraftStore = {
  [K in DraftConversationType]: Map<string, Extract<Draft, { _type: K }>>;
};

const store: SendBoxDraftStore = {
  acp: new Map(),
};

function assertNever(value: never): never {
  throw new Error(`Unsupported draft conversation type: ${String(value)}`);
}

const draftIdentity = (ownerId: string, conversationId: string): string =>
  sendBoxDraftStorageKey(ownerId, conversationId) ?? `${ownerId.trim() || 'local'}:${conversationId}`;

const setDraft = (type: DraftConversationType, ownerId: string, conversationId: string, draft: Draft | undefined) => {
  const identity = draftIdentity(ownerId, conversationId);
  switch (type) {
    case 'acp':
      if (draft) {
        store.acp.set(identity, draft);
        saveSendBoxDraft(ownerId, conversationId, draft);
      } else {
        store.acp.delete(identity);
        clearSendBoxDraft(ownerId, conversationId);
      }
      return;
    default:
      assertNever(type);
  }
};

const evictDraft = (type: DraftConversationType, ownerId: string, conversationId: string): void => {
  const identity = draftIdentity(ownerId, conversationId);
  switch (type) {
    case 'acp':
      store.acp.delete(identity);
      return;
    default:
      assertNever(type);
  }
};

const getDraft = (type: DraftConversationType, ownerId: string, conversationId: string): Draft | undefined => {
  const identity = draftIdentity(ownerId, conversationId);
  switch (type) {
    case 'acp': {
      const current = store.acp.get(identity);
      if (current) return current;
      const persisted = loadSendBoxDraft(ownerId, conversationId);
      if (persisted) store.acp.set(identity, persisted);
      return persisted;
    }
    default:
      return assertNever(type);
  }
};

/**
 * 获得一种类型下的会话草稿操作的 React Hook
 */
export const getSendBoxDraftHook = <K extends DraftConversationType>(
  type: K,
  initialValue: Extract<Draft, { _type: K }>
) => {
  function useDraft(conversation_id: string, ownerId = 'local') {
    const storageKey = sendBoxDraftStorageKey(ownerId, conversation_id);
    const swrRet = useSWR([`/send-box/${type}/draft`, ownerId, conversation_id], ([_, owner, id]) => {
      return (getDraft(type, owner, id) as Extract<Draft, { _type: K }> | undefined) ?? null;
    });

    useEffect(() => {
      if (!storageKey || typeof window === 'undefined') return;
      const handleStorage = (event: StorageEvent) => {
        if (event.key !== storageKey) return;
        evictDraft(type, ownerId, conversation_id);
        const next = getDraft(type, ownerId, conversation_id) as Extract<Draft, { _type: K }> | undefined;
        void swrRet.mutate(next ?? null, { revalidate: false });
      };
      window.addEventListener('storage', handleStorage);
      return () => window.removeEventListener('storage', handleStorage);
    }, [conversation_id, ownerId, storageKey, swrRet, type]);

    const mutateDraft = useCallback(
      (draft: (k: Extract<Draft, { _type: K }>) => typeof k | undefined): void => {
        const currentDraft =
          (getDraft(type, ownerId, conversation_id) as Extract<Draft, { _type: K }> | undefined) ??
          swrRet.data ??
          initialValue;
        const newDraft = draft(currentDraft);
        setDraft(type, ownerId, conversation_id, newDraft);

        swrRet.mutate(newDraft ?? null, { revalidate: false }).catch((error) => {
          console.error('Failed to mutate draft:', error);
        });
      },
      [conversation_id, initialValue, ownerId, swrRet, type]
    );

    return {
      get data() {
        return swrRet.data ?? undefined;
      },
      mutate: mutateDraft,
    };
  }

  return useDraft;
};

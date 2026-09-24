import { useCallback } from 'react';
import type { StagedSessionOptions } from '@/renderer/hooks/chat/sendBoxDraftPersistence';
import { getSendBoxDraftHook, type FileOrFolderItem } from '@/renderer/hooks/chat/useSendBoxDraft';
import { createSetUploadFile } from '@/renderer/hooks/chat/useSendBoxFiles';
import {
  normalizeComposerContextItems,
  type ComposerContextItem,
} from '@/renderer/components/chat/SendBox/composerCompositionModel';

const useAcpSendBoxDraft = getSendBoxDraftHook('acp', {
  _type: 'acp',
  atPath: [],
  content: '',
  uploadFile: [],
  contextItems: [],
});

const EMPTY_AT_PATH: Array<string | FileOrFolderItem> = [];
const EMPTY_UPLOAD_FILES: string[] = [];
const EMPTY_CONTEXT_ITEMS: ComposerContextItem[] = [];

export const useAcpSendBoxDraftController = (conversationId: string, ownerId?: string) => {
  const { data, mutate, replaceContentIfUnchanged } = useAcpSendBoxDraft(conversationId, ownerId);
  const atPath = data?.atPath ?? EMPTY_AT_PATH;
  const uploadFile = data?.uploadFile ?? EMPTY_UPLOAD_FILES;
  const content = data?.content ?? '';
  const contextItems = normalizeComposerContextItems(data?.contextItems ?? EMPTY_CONTEXT_ITEMS);
  const stagedPlanMode = data?.planMode === true;

  const setAtPath = useCallback(
    (nextAtPath: Array<string | FileOrFolderItem>) => {
      mutate((previous) => ({ ...previous, atPath: nextAtPath }));
    },
    [data, mutate]
  );
  const setUploadFile = createSetUploadFile(mutate, data);
  const setContent = useCallback(
    (nextContent: string) => {
      mutate((previous) => ({ ...previous, content: nextContent }));
    },
    [data, mutate]
  );
  const setContextItems = useCallback(
    (nextItems: ComposerContextItem[] | ((current: ComposerContextItem[]) => ComposerContextItem[])) => {
      mutate((previous) => {
        const current = normalizeComposerContextItems(previous.contextItems);
        const next = typeof nextItems === 'function' ? nextItems(current) : nextItems;
        return { ...previous, contextItems: normalizeComposerContextItems(next) };
      });
    },
    [data, mutate]
  );
  const setStagedPlanMode = useCallback(
    (enabled: boolean) => {
      mutate((previous) => {
        if (enabled === (previous.planMode === true)) return previous;
        return { ...previous, ...(enabled ? { planMode: true as const } : { planMode: undefined }) };
      });
    },
    [data, mutate]
  );
  const setStagedSessionOptions = useCallback(
    (options: StagedSessionOptions | null) => {
      mutate((previous) => ({ ...previous, stagedSessionOptions: options ?? undefined }));
    },
    [data, mutate]
  );

  return {
    atPath,
    uploadFile,
    setAtPath,
    setUploadFile,
    content,
    setContent,
    replaceContentIfUnchanged,
    contextItems,
    setContextItems,
    stagedPlanMode,
    setStagedPlanMode,
    stagedSessionOptions: data?.stagedSessionOptions ?? null,
    setStagedSessionOptions,
  };
};

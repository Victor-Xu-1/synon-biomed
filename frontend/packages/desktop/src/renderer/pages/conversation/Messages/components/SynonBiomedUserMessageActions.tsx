import type { IMessageText } from '@/common/chat/chatLib';
import { useConversationRuntimeView } from '@/renderer/pages/conversation/runtime/useConversationRuntimeView';
import {
  SYNON_BIOMED_SESSION_WORKFLOW_CAPABILITIES,
  createSynonBiomedBranchMutationId,
  forkSynonBiomedUserMessage,
  selectSynonBiomedBranch,
  type SynonBiomedConversationBranchState,
} from '@/renderer/services/synonBiomedConversationBranches';
import { ensureConversationRuntime } from '@/renderer/pages/conversation/utils/ensureConversationRuntime';
import { Message } from '@arco-design/web-react';
import { ArrowUp, Edit, Left, Right } from '@icon-park/react';
import React, { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import './SynonBiomedUserMessageActions.css';

type Props = {
  message: IMessageText;
  branchState?: SynonBiomedConversationBranchState | null;
  children?: React.ReactNode;
};

type BranchNavigation = {
  choices: string[];
  currentIndex: number;
};

function resolveBranchNavigation(
  branchState: SynonBiomedConversationBranchState | null | undefined,
  branchId: string,
  messageIndex: number
): BranchNavigation | null {
  if (!branchState || !branchId || !Number.isInteger(messageIndex) || messageIndex < 0) return null;
  const branchesById = new Map(branchState.branches.map((branch) => [branch.id, branch]));
  let lineageBranch = branchesById.get(branchId);
  if (!lineageBranch) return null;

  const visited = new Set<string>();
  let forkChoice: (typeof branchState.branches)[number] | null = null;
  while (lineageBranch && !visited.has(lineageBranch.id)) {
    visited.add(lineageBranch.id);
    if (lineageBranch.parentId && lineageBranch.forkPoint === messageIndex) {
      forkChoice = lineageBranch;
      break;
    }
    lineageBranch = lineageBranch.parentId ? branchesById.get(lineageBranch.parentId) : undefined;
  }

  const parentId = forkChoice?.parentId ?? branchId;
  const parent = parentId ? branchesById.get(parentId) : undefined;
  if (!parent) return null;
  const siblings = branchState.branches
    .filter((branch) => branch.parentId === parent.id && branch.forkPoint === messageIndex)
    .toSorted(
      (left, right) => (left.createdAt ?? '').localeCompare(right.createdAt ?? '') || left.id.localeCompare(right.id)
    );
  const choices = [parent.id, ...siblings.map((branch) => branch.id)];
  if (choices.length <= 1) return null;
  const currentIndex = choices.indexOf(forkChoice?.id ?? parent.id);
  return currentIndex >= 0 ? { choices, currentIndex } : null;
}

const SynonBiomedUserMessageActions: React.FC<Props> = ({ message, branchState, children }) => {
  const { t } = useTranslation();
  const metadata = message.content.synonBiomed;
  const runtime = useConversationRuntimeView(message.conversation_id);
  const authorityKey = `${message.conversation_id}\u0000${message.id}\u0000${metadata?.branchId ?? ''}`;
  const authorityRef = useRef(authorityKey);
  authorityRef.current = authorityKey;
  const mountedRef = useRef(true);
  const editorRef = useRef<HTMLTextAreaElement>(null);
  const editButtonRef = useRef<HTMLButtonElement>(null);
  const mutationRef = useRef<{ authority: string; content: string; id: string } | null>(null);
  const [visibleOwner, setVisibleOwner] = useState<string | null>(null);
  const [value, setValue] = useState(message.content.content);
  const [submittingOwner, setSubmittingOwner] = useState<string | null>(null);
  const visible = visibleOwner === authorityKey;
  const submitting = submittingOwner === authorityKey;
  const branchNavigation =
    metadata && SYNON_BIOMED_SESSION_WORKFLOW_CAPABILITIES.branchSelection.state === 'supported'
      ? resolveBranchNavigation(branchState, metadata.branchId ?? '', metadata.messageIndex)
      : null;

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  useEffect(() => {
    setVisibleOwner(null);
    mutationRef.current = null;
  }, [authorityKey]);

  useEffect(() => {
    if (!visible) return;
    const editor = editorRef.current;
    if (!editor) return;
    editor.focus();
    editor.setSelectionRange(editor.value.length, editor.value.length);
  }, [visible]);

  useEffect(() => {
    if (!visible) return;
    const editor = editorRef.current;
    if (!editor) return;
    editor.style.height = 'auto';
    const nextHeight = Math.min(Math.max(editor.scrollHeight, 22), 288);
    editor.style.height = `${nextHeight}px`;
    editor.style.overflowY = editor.scrollHeight > 288 ? 'auto' : 'hidden';
  }, [value, visible]);

  if (
    SYNON_BIOMED_SESSION_WORKFLOW_CAPABILITIES.messageFork.state !== 'supported' ||
    message.position !== 'right' ||
    !metadata ||
    metadata.blockIndex !== 0
  ) {
    return <>{children}</>;
  }

  const submit = async () => {
    const editedContent = value.trim();
    if (!editedContent || submitting) return;
    const requestAuthority = authorityKey;
    const existingMutation = mutationRef.current;
    const clientMutationId =
      existingMutation?.authority === requestAuthority && existingMutation.content === editedContent
        ? existingMutation.id
        : createSynonBiomedBranchMutationId();
    mutationRef.current = { authority: requestAuthority, content: editedContent, id: clientMutationId };
    setSubmittingOwner(requestAuthority);
    runtime.markSendStarted();
    try {
      const result = await forkSynonBiomedUserMessage({
        rootFrameId: message.conversation_id,
        messageIndex: metadata.messageIndex,
        sourceClientMessageId: message.msg_id || message.id,
        clientMutationId,
        editedContent,
        sourceBranchId: metadata.branchId,
      });
      const ensured = await ensureConversationRuntime(message.conversation_id);
      // The runtime gate belongs to the conversation, not this message-row
      // component. Settle it before branch selection replaces the old history
      // and unmounts this action.
      runtime.markSendAccepted(ensured.runtime.turn_id ?? message.conversation_id, ensured.runtime);
      if (!mountedRef.current || authorityRef.current !== requestAuthority) return;
      selectSynonBiomedBranch(message.conversation_id, result.branchId);
      mutationRef.current = null;
      setVisibleOwner(null);
      Message.success(t('conversation.synonRuntime.userMessageActions.branchCreated'));
    } catch {
      runtime.markSendFailed('branch_create_failed');
      if (!mountedRef.current || authorityRef.current !== requestAuthority) return;
      console.error('[user-message-branch] create failed');
      Message.error(t('conversation.synonRuntime.userMessageActions.branchCreateFailed'));
    } finally {
      if (mountedRef.current) {
        setSubmittingOwner((current) => (current === requestAuthority ? null : current));
      }
    }
  };

  const cancelEditing = () => {
    if (submitting) return;
    mutationRef.current = null;
    setVisibleOwner((current) => (current === authorityKey ? null : current));
    queueMicrotask(() => editButtonRef.current?.focus());
  };

  const beginEditing = () => {
    setValue(message.content.content);
    mutationRef.current = null;
    setVisibleOwner(authorityKey);
  };

  return (
    <div className='synon-biomed-user-message' data-testid='user-message-actions'>
      {visible ? (
        <div data-testid='inline-user-message-editor' className='synon-biomed-user-message__editor'>
          <textarea
            ref={editorRef}
            value={value}
            rows={1}
            disabled={submitting}
            className='synon-biomed-user-message__textarea'
            onChange={(event) => setValue(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === 'Escape') {
                event.preventDefault();
                cancelEditing();
                return;
              }
              if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) {
                event.preventDefault();
                void submit();
              }
            }}
            aria-label={t('conversation.synonRuntime.userMessageActions.editHistoricalMessage')}
          />
          <div className='synon-biomed-user-message__editor-footer'>
            <button
              type='button'
              disabled={submitting}
              className='synon-biomed-user-message__cancel'
              onClick={cancelEditing}
            >
              {t('common.cancel')}
            </button>
            <button
              type='button'
              disabled={!value.trim() || submitting}
              aria-busy={submitting}
              aria-label={t('conversation.synonRuntime.userMessageActions.saveAndSubmit')}
              title={t('conversation.synonRuntime.userMessageActions.saveAndSubmit')}
              className='synon-biomed-user-message__submit'
              onClick={() => void submit()}
            >
              <ArrowUp theme='outline' size={16} strokeWidth={4} />
            </button>
          </div>
        </div>
      ) : (
        <div className='synon-biomed-user-message__row' data-testid='user-message-action-row'>
          <button
            ref={editButtonRef}
            type='button'
            aria-label={t('conversation.synonRuntime.userMessageActions.editAndBranch')}
            title={t('conversation.synonRuntime.userMessageActions.editAndBranch')}
            disabled={runtime.isProcessing}
            onClick={beginEditing}
            className='synon-biomed-user-message__edit-action'
          >
            <Edit theme='outline' size={15} strokeWidth={3} />
          </button>
          <div className='synon-biomed-user-message__content'>{children}</div>
        </div>
      )}

      {!visible && branchNavigation ? (
        <div className='synon-biomed-user-message__branches'>
          <div className='flex items-center gap-2px text-12px text-t-tertiary'>
            <button
              type='button'
              aria-label={t('conversation.synonRuntime.userMessageActions.previousBranch')}
              disabled={runtime.isProcessing || branchNavigation.currentIndex === 0}
              onClick={() =>
                selectSynonBiomedBranch(
                  message.conversation_id,
                  branchNavigation.choices[branchNavigation.currentIndex - 1]
                )
              }
              className='flex h-28px w-28px items-center justify-center rounded-4px border-0 bg-transparent text-t-tertiary hover:bg-fill-2 hover:text-t-primary disabled:opacity-40'
            >
              <Left theme='outline' size={14} />
            </button>
            <span className='min-w-34px text-center tabular-nums'>
              {t('conversation.synonRuntime.userMessageActions.branchPosition', {
                current: branchNavigation.currentIndex + 1,
                total: branchNavigation.choices.length,
              })}
            </span>
            <button
              type='button'
              aria-label={t('conversation.synonRuntime.userMessageActions.nextBranch')}
              disabled={runtime.isProcessing || branchNavigation.currentIndex === branchNavigation.choices.length - 1}
              onClick={() =>
                selectSynonBiomedBranch(
                  message.conversation_id,
                  branchNavigation.choices[branchNavigation.currentIndex + 1]
                )
              }
              className='flex h-28px w-28px items-center justify-center rounded-4px border-0 bg-transparent text-t-tertiary hover:bg-fill-2 hover:text-t-primary disabled:opacity-40'
            >
              <Right theme='outline' size={14} />
            </button>
          </div>
        </div>
      ) : null}
    </div>
  );
};

export default SynonBiomedUserMessageActions;

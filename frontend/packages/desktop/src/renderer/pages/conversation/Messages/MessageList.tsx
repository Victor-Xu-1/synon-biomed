/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { TMessage } from '@/common/chat/chatLib';
import { useConversationContextSafe } from '@/renderer/hooks/context/ConversationContext';
import { useRealtime } from '@/renderer/hooks/context/RealtimeContext';
import { useConversationRuntimeView } from '@/renderer/pages/conversation/runtime/useConversationRuntimeView';
import { markTerminalProjectionReady } from '@/renderer/pages/conversation/runtime/conversationRuntimeViewStore';
import { CHAT_SURFACE_WIDTH_CLASS } from '@/renderer/pages/conversation/utils/chatSurfaceWidth';
import { iconColors } from '@/renderer/styles/colors';
import { CHAT_MESSAGE_JUMP_EVENT, type ChatMessageJumpDetail } from '@/renderer/utils/chat/chatMinimapEvents';
import { Button, Image } from '@arco-design/web-react';
import { Down, UpSmall } from '@icon-park/react';
import MessageAcpPermission from '@renderer/pages/conversation/Messages/acp/MessageAcpPermission';
import classNames from 'classnames';
import React, {
  createContext,
  useContext,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import { Virtuoso, type Components, type ListProps, type VirtuosoHandle } from 'react-virtuoso';
import { useTranslation } from 'react-i18next';
import { useLocation } from 'react-router';
import './messages.css';
import HOC from '@renderer/utils/ui/HOC';
import { useConversationArtifacts, useSyncConversationArtifactWindow } from './artifacts';
import { isUserTaskMessage } from './scrollTargetModel';
import {
  useLoadAnchorMessageWindow,
  useLoadNextMessagePage,
  useLoadPreviousMessagePage,
  useMessageList,
  useMessageListLoadError,
  useMessageListLoading,
  useMessagePaginationState,
} from './hooks';
import MessageTips from './components/MessageTips';
import MessageText from './components/MessageText';
import MessagePermission from './components/MessagePermission';
import MessageFileChanges from './MessageFileChanges';
import MessageAgentStatus from './components/MessageAgentStatus';
import SynonBiomedTimelinePlan from '@/renderer/components/synonBiomed/runtime/SynonBiomedTimelinePlan';
import MessageToolCall from './components/MessageToolCall';
import MessageToolGroupSummary from './components/MessageToolGroupSummary';
import { hasRenderableTerminalAnswerForLatestUserTurn } from './terminalProjectionReadiness';
import MessageCronTrigger from './components/MessageCronTrigger';
import MessageSkillSuggest from './components/MessageSkillSuggest';
import { ScientificFilePreviewStrip } from './components/MessageScientificFiles';
import SynonBiomedUserMessageActions from './components/SynonBiomedUserMessageActions';
import MessageThinking from './components/MessageThinking';
import TranscriptActivity, { TranscriptActivityContext } from './components/TranscriptActivity';
import { projectTranscriptActivity } from './transcriptActivityModel';
import ConversationLoadingSurface from '../components/ConversationLoadingSurface';
import { useConversationScrollController } from './useConversationScrollController';
import SelectionReplyButton from './components/SelectionReplyButton';
import { ConversationAnnotationsProvider } from './components/ConversationAnnotationsContext';
import { areMessageItemPropsEqual } from './messageItemMemoModel';
import { projectTerminalFailuresForDisplay } from '../runtime/terminalFailureMessagesModel';
import {
  buildMessagePresentationList,
  getProcessedItemAnchorId,
  getProcessedItemRowKey,
  matchesTargetMessage,
  type MessageListProcessedItem,
} from './messageListProjection';
import {
  MESSAGE_DEFAULT_ITEM_HEIGHT,
  reconcileMessageVirtualWindow,
  type MessageVirtualWindow,
} from './messageVirtualWindow';
import {
  SYNON_BIOMED_BRANCH_SELECTION_EVENT,
  SYNON_BIOMED_SESSION_WORKFLOW_CAPABILITIES,
  loadSynonBiomedConversationBranches,
  type SynonBiomedBranchSelectionEventDetail,
  type SynonBiomedConversationBranchState,
} from '@/renderer/services/synonBiomedConversationBranches';

type ConversationLocationState = {
  targetMessageId?: string;
  fromConversationSearch?: boolean;
};

type MessageVirtuosoContext = {
  header: React.ReactNode;
  footer: React.ReactNode;
};

// The composer-safe footer intentionally leaves roughly 100 px below the last
// transcript row. Treat that reserved clearance as the attached tail instead
// of showing a false "scroll to bottom" state.
const MESSAGE_AT_BOTTOM_THRESHOLD_PX = 128;

const MessageVirtuosoList = React.forwardRef<HTMLDivElement, ListProps & { context: MessageVirtuosoContext }>(
  ({ context: _context, style, ...props }, forwardedRef) => {
    return (
      <div
        {...props}
        ref={forwardedRef}
        data-testid='message-list-content'
        style={{
          ...style,
          overflowAnchor: 'none',
        }}
      />
    );
  }
);
MessageVirtuosoList.displayName = 'MessageVirtuosoList';

const MessageVirtuosoHeader: React.FC<{ context: MessageVirtuosoContext }> = ({ context }) => <>{context.header}</>;
const MessageVirtuosoFooter: React.FC<{ context: MessageVirtuosoContext }> = ({ context }) => <>{context.footer}</>;
const MESSAGE_VIRTUOSO_COMPONENTS: Components<MessageListProcessedItem, MessageVirtuosoContext> = {
  List: MessageVirtuosoList,
  Header: MessageVirtuosoHeader,
  Footer: MessageVirtuosoFooter,
};

const highlightStyle: React.CSSProperties = {
  backgroundColor: 'var(--color-aou-1)',
  boxShadow: '0 0 0 1px var(--color-aou-6-brand) inset',
  borderRadius: '12px',
};

const getUnhandledMessageType = (_message: never): string => 'unknown';

// Image preview context
export const ImagePreviewContext = createContext<{ inPreviewGroup: boolean }>({
  inPreviewGroup: false,
});

const MessageItem: React.FC<{
  message: TMessage;
  branchState?: SynonBiomedConversationBranchState | null;
  highlighted?: boolean;
  rowWidthClass: string;
  showCopyRow?: boolean;
  messageIndex?: number;
  isLastUserMessage?: boolean;
}> = React.memo(
  HOC((props) => {
    const activity = useContext(TranscriptActivityContext);
    const { message, highlighted, rowWidthClass, isLastUserMessage, messageIndex } = props as {
      message: TMessage;
      highlighted?: boolean;
      rowWidthClass: string;
      isLastUserMessage?: boolean;
      messageIndex?: number;
    };
    return (
      <div
        id={`message-${message.id}`}
        data-testid={`message-${message.type}-${message.position}`}
        data-message-type={message.type}
        data-message-position={message.position}
        data-source-message-id={message.id}
        data-message-index={Number.isSafeInteger(messageIndex) ? messageIndex : undefined}
        data-last-user-msg={isLastUserMessage ? '' : undefined}
        className={classNames(
          `${rowWidthClass} w-full min-w-0 box-border flex items-start message-item [&>div]:max-w-full px-8px m-t-10px`,
          message.type,
          {
            'justify-center': message.position === 'center',
            'justify-end': message.position === 'right',
            'justify-start': message.position === 'left',
          }
        )}
        style={highlighted ? highlightStyle : undefined}
      >
        {message.position !== 'right' && !['tool_call', 'tool_group', 'acp_tool_call'].includes(message.type) ? (
          <TranscriptActivity activity={activity} />
        ) : null}
        {props.children}
      </div>
    );
  })(
    ({
      message,
      branchState,
      showCopyRow,
      messageIndex,
    }: {
      message: TMessage;
      branchState?: SynonBiomedConversationBranchState | null;
      highlighted?: boolean;
      rowWidthClass: string;
      showCopyRow?: boolean;
      messageIndex?: number;
      isLastUserMessage?: boolean;
    }) => {
      const { t } = useTranslation();
      switch (message.type) {
        case 'text': {
          const canShowUserMessageActions =
            SYNON_BIOMED_SESSION_WORKFLOW_CAPABILITIES.messageFork.state === 'supported' &&
            message.position === 'right' &&
            message.content.synonBiomed?.blockIndex === 0;
          const textMessage = (
            <MessageText
              message={message}
              messageIndex={messageIndex}
              showCopyRow={canShowUserMessageActions ? false : showCopyRow}
            />
          );
          if (!canShowUserMessageActions) return textMessage;
          return (
            <SynonBiomedUserMessageActions message={message} branchState={branchState}>
              {textMessage}
            </SynonBiomedUserMessageActions>
          );
        }
        case 'tips':
          return <MessageTips message={message}></MessageTips>;
        case 'tool_call':
          return <MessageToolCall message={message}></MessageToolCall>;
        case 'tool_group':
          return <MessageToolGroupSummary messages={[message]} />;
        case 'agent_status':
          return <MessageAgentStatus message={message}></MessageAgentStatus>;
        case 'permission':
          return <MessagePermission message={message}></MessagePermission>;
        case 'acp_permission':
          return <MessageAcpPermission message={message}></MessageAcpPermission>;
        case 'acp_tool_call':
          return <MessageToolGroupSummary messages={[message]} />;
        case 'plan':
          return <SynonBiomedTimelinePlan message={message} />;
        case 'thinking':
          return <MessageThinking message={message}></MessageThinking>;
        case 'available_commands':
          return null;
        default:
          return (
            <div>
              {t('messages.unknownMessageType', {
                type: getUnhandledMessageType(message),
              })}
            </div>
          );
      }
    }
  ),
  areMessageItemPropsEqual
);

const MessageList: React.FC<{
  className?: string;
  emptySlot?: React.ReactNode;
  onRetryLoad?: () => void;
  ownerId?: string;
}> = ({ className, emptySlot, onRetryLoad, ownerId = '' }) => {
  const { t } = useTranslation();
  const list = useMessageList();
  const { snapshot: realtimeSnapshot } = useRealtime();
  const messageListLoadError = useMessageListLoadError();
  const isMessageListLoading = useMessageListLoading();
  const pagination = useMessagePaginationState();
  const artifacts = useConversationArtifacts();
  const syncConversationArtifactWindow = useSyncConversationArtifactWindow();
  const conversationContext = useConversationContextSafe();
  const conversationId = conversationContext?.conversation_id ?? '';
  const rowWidthClass = CHAT_SURFACE_WIDTH_CLASS;
  const loadPreviousMessagePage = useLoadPreviousMessagePage(conversationContext?.conversation_id);
  const loadNextMessagePage = useLoadNextMessagePage(conversationContext?.conversation_id);
  const loadAnchorMessageWindow = useLoadAnchorMessageWindow(conversationContext?.conversation_id);
  // While the agent is still streaming, the in-progress turn's last text keeps
  // moving down, so we defer its copy/timestamp row until the turn finishes to
  // avoid the row flashing in and the layout reflowing mid-stream.
  const { isProcessing, view: runtimeView } = useConversationRuntimeView(conversationContext?.conversation_id ?? '');
  const terminalAnswerReady = useMemo(
    () =>
      hasRenderableTerminalAnswerForLatestUserTurn(list, {
        isLatestWindow: !pagination.hasMoreAfter && !isMessageListLoading,
      }),
    [list, pagination.hasMoreAfter, isMessageListLoading]
  );
  useLayoutEffect(() => {
    if (!conversationId || !runtimeView?.terminalProjectionPending || !terminalAnswerReady) return;
    markTerminalProjectionReady(conversationId);
  }, [conversationId, runtimeView?.terminalProjectionPending, terminalAnswerReady]);
  const presentationList = useMemo(() => projectTerminalFailuresForDisplay(list, isProcessing), [isProcessing, list]);
  const location = useLocation();
  const locationState = (location.state || {}) as ConversationLocationState;
  const targetMessageId = locationState.targetMessageId;
  const [highlightedMessageId, setHighlightedMessageId] = useState<string | undefined>();
  const [ownedBranchState, setOwnedBranchState] = useState<{
    conversationId: string;
    value: SynonBiomedConversationBranchState;
  } | null>(null);
  const handledTargetKeyRef = useRef<string>('');
  const loadingTargetKeyRef = useRef<string>('');
  const pendingMessageJumpRef = useRef<{
    conversationId: string;
    messageId: string;
    align: ScrollLogicalPosition;
    behavior: ScrollBehavior;
  } | null>(null);
  const pageLoadRequestTokenRef = useRef(0);
  const previousPageLoadInFlightRef = useRef<{ conversationId: string; token: number } | null>(null);
  const nextPageLoadInFlightRef = useRef<{ conversationId: string; token: number } | null>(null);
  const virtuosoRef = useRef<VirtuosoHandle>(null);
  const scrollerElementRef = useRef<HTMLDivElement | null>(null);
  const committedVirtualWindowRef = useRef<{
    conversationId: string;
    value: MessageVirtualWindow;
  } | null>(null);
  const hasBranchCoordinates = useMemo(
    () =>
      list.some(
        (message) => message.type === 'text' && message.position === 'right' && Boolean(message.content.synonBiomed)
      ),
    [list]
  );
  const branchState = ownedBranchState?.conversationId === conversationId ? ownedBranchState.value : null;
  const artifactWindowReferences = useMemo(() => {
    const references: Array<{ artifact_id: string; version_id: string }> = [];
    const seen = new Set<string>();
    for (const message of list) {
      for (const reference of message.artifact_refs ?? []) {
        if (reference.availability && reference.availability !== 'available') continue;
        const artifactId = reference.artifact_id.trim();
        const versionId = reference.version_id.trim();
        if (!artifactId || !versionId) continue;
        const key = `${artifactId}\0${versionId}`;
        if (seen.has(key)) continue;
        seen.add(key);
        references.push({ artifact_id: artifactId, version_id: versionId });
      }
    }
    return references;
  }, [list]);

  useLayoutEffect(() => {
    syncConversationArtifactWindow(artifactWindowReferences);
  }, [artifactWindowReferences, syncConversationArtifactWindow]);

  useEffect(() => {
    if (
      !conversationId ||
      !hasBranchCoordinates ||
      SYNON_BIOMED_SESSION_WORKFLOW_CAPABILITIES.branchSelection.state !== 'supported' ||
      typeof window === 'undefined'
    ) {
      setOwnedBranchState(null);
      return;
    }

    let active = true;
    let requestGeneration = 0;
    const refresh = () => {
      const generation = ++requestGeneration;
      void loadSynonBiomedConversationBranches(conversationId)
        .then((value) => {
          if (active && generation === requestGeneration) setOwnedBranchState({ conversationId, value });
        })
        .catch(() => {
          if (active && generation === requestGeneration) {
            setOwnedBranchState((current) => (current?.conversationId === conversationId ? null : current));
          }
          console.error('[message-branches] load failed');
        });
    };
    const handleSelection = (event: Event) => {
      const detail = (event as CustomEvent<SynonBiomedBranchSelectionEventDetail>).detail;
      if (detail?.conversationId === conversationId) refresh();
    };

    refresh();
    window.addEventListener(SYNON_BIOMED_BRANCH_SELECTION_EVENT, handleSelection);
    return () => {
      active = false;
      window.removeEventListener(SYNON_BIOMED_BRANCH_SELECTION_EVENT, handleSelection);
    };
  }, [conversationId, hasBranchCoordinates]);

  const messageIndexById = useMemo(() => {
    const indexes = new Map<string, number>();
    list.forEach((message, index) => {
      // Preserve findIndex semantics if malformed history contains duplicate IDs.
      if (!indexes.has(message.id)) indexes.set(message.id, index);
    });
    return indexes;
  }, [list]);

  const processedList = useMemo(
    () => buildMessagePresentationList(presentationList, artifacts, pagination.groupBoundaryMessageIds ?? []),
    [artifacts, pagination.groupBoundaryMessageIds, presentationList]
  );
  const processedRowKeys = useMemo(() => processedList.map(getProcessedItemRowKey), [processedList]);
  const virtualWindow = useMemo(() => {
    const committed = committedVirtualWindowRef.current;
    return reconcileMessageVirtualWindow(
      committed?.conversationId === conversationId ? committed.value : null,
      processedRowKeys
    );
  }, [conversationId, processedRowKeys]);
  useLayoutEffect(() => {
    committedVirtualWindowRef.current = { conversationId, value: virtualWindow };
  }, [conversationId, virtualWindow]);
  // An AI reply can be split into several messages (thinking / multiple text /
  // tool blocks). The hover copy row should appear once per turn, after the
  // actual closing text — never between a progress note and later operations.
  // Collect the id of the last AI text in each turn; a turn runs until the next
  // user (right) message. Files and artifacts don't end a response, but a tool
  // block after text proves that text was a progress note rather than the close.
  // While the conversation is still streaming, the final turn's
  // row is withheld (it would otherwise appear then shift down as more text
  // streams in); earlier, already-finished turns always keep their row.
  const aiCopyRowTextIds = useMemo(() => {
    const ids = new Set<string>();
    let pendingTextId: string | undefined;
    let pendingTextFollowedByTool = false;
    let lastTurnTextId: string | undefined;
    const flush = () => {
      if (pendingTextId && !pendingTextFollowedByTool) ids.add(pendingTextId);
      pendingTextId = undefined;
      pendingTextFollowedByTool = false;
    };
    for (const item of processedList) {
      if (
        'type' in item &&
        (item.type === 'file_summary' ||
          item.type === 'tool_summary' ||
          item.type === 'referenced_files' ||
          item.type === 'artifact')
      ) {
        if (item.type === 'tool_summary' && pendingTextId) pendingTextFollowedByTool = true;
        continue;
      }
      const message = item as TMessage;
      if (message.position === 'right') {
        flush();
        continue;
      }
      if (message.type === 'text') {
        pendingTextId = message.id;
        pendingTextFollowedByTool = false;
      }
    }
    lastTurnTextId = pendingTextId;
    flush();
    // The final turn is the one that may still be streaming; hide its row until done.
    if (isProcessing && lastTurnTextId) ids.delete(lastTurnTextId);
    return ids;
  }, [processedList, isProcessing]);

  const lastUserMessageId = useMemo(() => {
    for (let index = processedList.length - 1; index >= 0; index -= 1) {
      const item = processedList[index];
      if ('type' in item && ['file_summary', 'tool_summary', 'artifact'].includes(item.type)) continue;
      const message = item as TMessage;
      if (isUserTaskMessage(message)) return message.id;
    }
    return null;
  }, [processedList]);
  const lastUserRowIndex = useMemo(
    () => (lastUserMessageId ? processedList.findIndex((item) => matchesTargetMessage(item, lastUserMessageId)) : -1),
    [lastUserMessageId, processedList]
  );

  const scrollProcessedMessageIntoView = useCallback(
    (messageId: string, options?: { behavior?: ScrollBehavior; block?: ScrollLogicalPosition }): boolean => {
      const targetIndex = processedList.findIndex((item) => matchesTargetMessage(item, messageId));
      if (targetIndex < 0 || !virtuosoRef.current) return false;
      const block = options?.block;
      virtuosoRef.current.scrollToIndex({
        index: targetIndex,
        align: block === 'center' ? 'center' : block === 'end' ? 'end' : 'start',
        behavior: options?.behavior === 'smooth' ? 'smooth' : 'auto',
      });
      return true;
    },
    [processedList]
  );

  const scrollToBottomItem = useCallback(
    (behavior: ScrollBehavior = 'auto'): boolean => {
      if (!virtuosoRef.current || processedList.length === 0) return false;
      virtuosoRef.current.scrollTo({
        // Pixel scrolling is the unambiguous tail primitive. scrollToIndex has
        // different coordinate semantics for a concrete row versus the
        // firstItemIndex window; the browser safely clamps this to the exact
        // current scrollHeight after Virtuoso measures variable-height rows.
        top: Number.MAX_SAFE_INTEGER,
        behavior: behavior === 'smooth' ? 'smooth' : 'auto',
      });
      return true;
    },
    [processedList.length]
  );

  const {
    handleAtBottomStateChange,
    handleRangeChanged,
    handleLastUserPositionChange,
    handleTotalListHeightChanged,
    handleUserScrollIntent,
    canLoadPreviousPage,
    followOutput,
    showScrollButton,
    showLastUserButton,
    scrollToBottom,
    scrollToLastUser,
  } = useConversationScrollController({
    conversationId,
    messages: list,
    itemCount: processedList.length,
    lastUserMessageId,
    lastUserRowIndex,
    scrollMessageIntoView: scrollProcessedMessageIntoView,
    scrollToBottomItem,
  });

  const handleMessageListPointerDown = useCallback(
    (event: React.PointerEvent<HTMLDivElement>) => {
      if (event.target !== event.currentTarget) return;
      const bounds = event.currentTarget.getBoundingClientRect();
      if (event.clientX >= bounds.right - 20) handleUserScrollIntent();
    },
    [handleUserScrollIntent]
  );

  const updateLastUserPosition = useCallback(
    (scroller: HTMLDivElement) => {
      const lastUserRow = scroller.querySelector<HTMLElement>('[data-last-user-msg]');
      const scrollerTop = scroller.getBoundingClientRect().top;
      if (lastUserRow) {
        handleLastUserPositionChange(lastUserRow.getBoundingClientRect().bottom <= scrollerTop);
        return;
      }
      const firstVisibleRow = Array.from(scroller.querySelectorAll<HTMLElement>('[data-source-message-id]')).find(
        (row) => row.getBoundingClientRect().bottom > scrollerTop
      );
      const firstVisibleId = firstVisibleRow?.dataset.sourceMessageId;
      if (!firstVisibleId) return;
      const firstVisibleIndex = processedList.findIndex((item) => matchesTargetMessage(item, firstVisibleId));
      if (firstVisibleIndex >= 0) handleLastUserPositionChange(lastUserRowIndex < firstVisibleIndex);
    },
    [handleLastUserPositionChange, lastUserRowIndex, processedList]
  );

  const handleMessageListScroll = useCallback(
    (event: React.UIEvent<HTMLDivElement>) => updateLastUserPosition(event.currentTarget),
    [updateLastUserPosition]
  );

  const setScrollerRef = useCallback((element: HTMLElement | Window | null) => {
    scrollerElementRef.current = element instanceof HTMLDivElement ? element : null;
  }, []);

  useEffect(() => {
    const frame = requestAnimationFrame(() => {
      if (scrollerElementRef.current) updateLastUserPosition(scrollerElementRef.current);
    });
    return () => cancelAnimationFrame(frame);
  }, [processedList, updateLastUserPosition]);

  const loadPreviousAtBoundary = useCallback(() => {
    if (
      canLoadPreviousPage() &&
      pagination.hasMoreBefore &&
      !pagination.isLoadingBefore &&
      previousPageLoadInFlightRef.current?.conversationId !== conversationId
    ) {
      const token = ++pageLoadRequestTokenRef.current;
      previousPageLoadInFlightRef.current = { conversationId, token };
      void loadPreviousMessagePage().finally(() => {
        const flight = previousPageLoadInFlightRef.current;
        if (flight?.conversationId === conversationId && flight.token === token) {
          previousPageLoadInFlightRef.current = null;
        }
      });
    }
  }, [
    canLoadPreviousPage,
    conversationId,
    loadPreviousMessagePage,
    pagination.hasMoreBefore,
    pagination.isLoadingBefore,
  ]);

  const loadNextAtBoundary = useCallback(() => {
    if (
      pagination.hasMoreAfter &&
      !pagination.isLoadingAfter &&
      nextPageLoadInFlightRef.current?.conversationId !== conversationId
    ) {
      const token = ++pageLoadRequestTokenRef.current;
      nextPageLoadInFlightRef.current = { conversationId, token };
      void loadNextMessagePage().finally(() => {
        const flight = nextPageLoadInFlightRef.current;
        if (flight?.conversationId === conversationId && flight.token === token) {
          nextPageLoadInFlightRef.current = null;
        }
      });
    }
  }, [conversationId, loadNextMessagePage, pagination.hasMoreAfter, pagination.isLoadingAfter]);

  useEffect(() => {
    if (!targetMessageId || processedList.length === 0) {
      return;
    }

    const targetKey = `${location.key}:${targetMessageId}`;
    if (handledTargetKeyRef.current === targetKey) {
      return;
    }

    const targetIndex = processedList.findIndex((item) => matchesTargetMessage(item, targetMessageId));
    if (targetIndex === -1) {
      if (loadingTargetKeyRef.current !== targetKey) {
        loadingTargetKeyRef.current = targetKey;
        void loadAnchorMessageWindow(targetMessageId).then((loaded) => {
          if (!loaded) {
            loadingTargetKeyRef.current = '';
          }
        });
      }
      return;
    }

    handledTargetKeyRef.current = targetKey;
    loadingTargetKeyRef.current = '';
    setHighlightedMessageId(targetMessageId);
    requestAnimationFrame(() => {
      scrollProcessedMessageIntoView(targetMessageId, {
        behavior: 'smooth',
        block: 'center',
      });
    });

    const timer = window.setTimeout(() => {
      setHighlightedMessageId((current) => (current === targetMessageId ? undefined : current));
    }, 2400);

    return () => window.clearTimeout(timer);
  }, [loadAnchorMessageWindow, location.key, processedList, scrollProcessedMessageIntoView, targetMessageId]);

  useEffect(() => {
    const handleMessageJump = (event: Event) => {
      const detail = (event as CustomEvent<ChatMessageJumpDetail>).detail;
      if (!detail || !detail.conversation_id) return;
      if (!conversationContext?.conversation_id || detail.conversation_id !== conversationContext.conversation_id)
        return;

      const targetIndex = processedList.findIndex((item) => {
        if (
          (item as { type?: string }).type === 'file_summary' ||
          (item as { type?: string }).type === 'tool_summary' ||
          (item as { type?: string }).type === 'artifact'
        ) {
          return false;
        }
        const message = item as TMessage;
        if (detail.messageId && message.id === detail.messageId) return true;
        if (detail.msgId && message.msg_id === detail.msgId) return true;
        return false;
      });
      if (targetIndex < 0) {
        const anchorMessageId = detail.messageId;
        if (!anchorMessageId) return;
        pendingMessageJumpRef.current = {
          conversationId: detail.conversation_id,
          messageId: anchorMessageId,
          align: detail.align || 'start',
          behavior: detail.behavior || 'smooth',
        };
        void loadAnchorMessageWindow(anchorMessageId).then((loaded) => {
          if (!loaded && pendingMessageJumpRef.current?.messageId === anchorMessageId) {
            pendingMessageJumpRef.current = null;
          }
        });
        return;
      }

      requestAnimationFrame(() => {
        scrollProcessedMessageIntoView(getProcessedItemAnchorId(processedList[targetIndex]), {
          block: detail.align || 'start',
          behavior: detail.behavior || 'smooth',
        });
      });
    };

    window.addEventListener(CHAT_MESSAGE_JUMP_EVENT, handleMessageJump);
    return () => {
      window.removeEventListener(CHAT_MESSAGE_JUMP_EVENT, handleMessageJump);
    };
  }, [conversationContext?.conversation_id, loadAnchorMessageWindow, processedList, scrollProcessedMessageIntoView]);

  useLayoutEffect(() => {
    const pending = pendingMessageJumpRef.current;
    if (!pending || pending.conversationId !== conversationId) return;
    if (!processedList.some((item) => matchesTargetMessage(item, pending.messageId))) return;
    pendingMessageJumpRef.current = null;
    setHighlightedMessageId(pending.messageId);
    requestAnimationFrame(() => {
      scrollProcessedMessageIntoView(pending.messageId, {
        block: pending.align,
        behavior: pending.behavior,
      });
    });
  }, [conversationId, processedList, scrollProcessedMessageIntoView]);

  // Click scroll button
  const handleScrollButtonClick = () => {
    scrollToBottom('smooth');
  };

  const renderItem = useCallback(
    (_index: number, item: MessageListProcessedItem) => {
      const highlighted = matchesTargetMessage(item, highlightedMessageId);
      if ('type' in item && item.type === 'referenced_files') {
        return (
          <div
            key={item.id}
            id={`message-${getProcessedItemAnchorId(item)}`}
            data-testid='message-artifact-references'
            className={`${rowWidthClass} w-full min-w-0 box-border message-item px-8px m-t-6px`}
            style={highlighted ? highlightStyle : undefined}
          >
            <ScientificFilePreviewStrip files={item.files} inline />
          </div>
        );
      }
      if ('type' in item && item.type === 'artifact') {
        return (
          <div
            key={item.id}
            id={`message-${getProcessedItemAnchorId(item)}`}
            data-conversation-artifact-kind={item.artifact.kind}
            data-testid={`conversation-artifact-${item.artifact.kind}`}
            className={`${rowWidthClass} w-full min-w-0 box-border message-item px-8px m-t-10px`}
            style={highlighted ? highlightStyle : undefined}
          >
            {item.artifact.kind === 'cron_trigger' ? (
              <MessageCronTrigger artifact={item.artifact} />
            ) : (
              <MessageSkillSuggest artifact={item.artifact} />
            )}
          </div>
        );
      }
      if ('type' in item && ['file_summary', 'tool_summary'].includes(item.type)) {
        return (
          <div
            key={item.id}
            id={`message-${getProcessedItemAnchorId(item)}`}
            className={`${rowWidthClass} w-full min-w-0 box-border message-item px-8px m-t-10px ${item.type}`}
            style={highlighted ? highlightStyle : undefined}
          >
            {item.type === 'file_summary' && <MessageFileChanges diffsChanges={item.diffs} />}
            {item.type === 'tool_summary' && (
              <MessageToolGroupSummary messages={item.messages}></MessageToolGroupSummary>
            )}
          </div>
        );
      }
      const message = item as TMessage;
      // User messages keep their own copy row; AI text only shows it at the turn end.
      const showCopyRow = message.position !== 'left' || message.type !== 'text' || aiCopyRowTextIds.has(message.id);
      const originalMessageIndex = messageIndexById.get(message.id) ?? -1;
      return (
        <MessageItem
          message={message}
          branchState={branchState}
          key={message.id}
          highlighted={highlighted}
          rowWidthClass={rowWidthClass}
          showCopyRow={showCopyRow}
          messageIndex={originalMessageIndex >= 0 ? originalMessageIndex : _index}
          isLastUserMessage={message.id === lastUserMessageId}
        ></MessageItem>
      );
    },
    [aiCopyRowTextIds, branchState, highlightedMessageId, lastUserMessageId, messageIndexById, rowWidthClass]
  );
  const liveTailKey =
    !pagination.hasMoreAfter && !pagination.isLoadingAfter && !pagination.isLoadingAnchor && processedList.length
      ? getProcessedItemRowKey(processedList[processedList.length - 1])
      : null;
  const tailActivity = useMemo(
    () => projectTranscriptActivity(runtimeView, realtimeSnapshot.status, processedList.at(-1)),
    [runtimeView, realtimeSnapshot.status, processedList]
  );
  const renderVirtuosoItem = useCallback(
    (absoluteIndex: number, item: MessageListProcessedItem) => (
      <TranscriptActivityContext.Provider value={getProcessedItemRowKey(item) === liveTailKey ? tailActivity : null}>
        {renderItem(absoluteIndex - virtualWindow.firstItemIndex, item)}
      </TranscriptActivityContext.Provider>
    ),
    [renderItem, virtualWindow.firstItemIndex, liveTailKey, tailActivity]
  );

  const virtuosoContext = useMemo<MessageVirtuosoContext>(
    () => ({
      header: (
        <>
          {messageListLoadError ? (
            <div
              data-testid='message-list-refresh-error'
              role='status'
              className='transcript-boundary-indicator gap-8px text-danger-6'
            >
              <span>{t('conversation.synonRuntime.messageList.refreshFailed')}</span>
              {onRetryLoad ? (
                <Button size='mini' loading={isMessageListLoading} onClick={onRetryLoad}>
                  {t('conversation.synonRuntime.messageList.retry')}
                </Button>
              ) : null}
            </div>
          ) : null}
          {pagination.isLoadingBefore ? (
            <div data-testid='transcript-load-older' aria-live='polite' className='transcript-boundary-indicator'>
              <span className='transcript-boundary-indicator__spinner' aria-hidden='true' />
              {t('conversation.synonRuntime.messageList.loadingEarlier')}
            </div>
          ) : !pagination.hasMoreBefore ? (
            <div data-testid='transcript-start-marker' className='transcript-boundary-indicator'>
              {t('conversation.synonRuntime.messageList.conversationStarted')}
            </div>
          ) : (
            <div className='h-10px' />
          )}
        </>
      ),
      footer: (
        <>
          {pagination.isLoadingAfter ? (
            <div data-testid='transcript-load-newer' aria-live='polite' className='transcript-boundary-indicator'>
              <span className='transcript-boundary-indicator__spinner' aria-hidden='true' />
              {t('conversation.synonRuntime.messageList.loadingNewer')}
            </div>
          ) : null}
          <div className='h-20px' />
        </>
      ),
    }),
    [
      isMessageListLoading,
      messageListLoadError,
      onRetryLoad,
      pagination.hasMoreBefore,
      pagination.isLoadingAfter,
      pagination.isLoadingBefore,
      t,
    ]
  );

  if (processedList.length === 0 && isMessageListLoading) {
    return <ConversationLoadingSurface testId='message-list-loading-surface' />;
  }

  if (processedList.length === 0 && messageListLoadError) {
    return (
      <div
        data-testid='message-list-load-error'
        className='relative flex-1 h-full flex flex-col items-center justify-center gap-10px px-20px text-center'
        role='alert'
      >
        <p className='m-0 text-13px text-t-secondary'>{t('conversation.synonRuntime.messageList.loadFailed')}</p>
        {onRetryLoad ? (
          <Button size='small' loading={isMessageListLoading} onClick={onRetryLoad}>
            {t('conversation.synonRuntime.messageList.retry')}
          </Button>
        ) : null}
      </div>
    );
  }

  if (processedList.length === 0 && emptySlot) {
    return <div className='relative flex-1 h-full flex items-center justify-center'>{emptySlot}</div>;
  }

  return (
    <ConversationAnnotationsProvider frameId={conversationContext?.conversation_id} ownerId={ownerId}>
      <div className={classNames('relative flex flex-col flex-1 min-h-0 h-full', className)}>
        {/* Use PreviewGroup to wrap all messages for cross-message image preview */}
        <Image.PreviewGroup actionsLayout={['zoomIn', 'zoomOut', 'originalSize', 'rotateLeft', 'rotateRight']}>
          <ImagePreviewContext.Provider value={{ inPreviewGroup: true }}>
            <Virtuoso<MessageListProcessedItem, MessageVirtuosoContext>
              key={`${conversationId}:${processedList.length > 0 ? 'ready' : 'empty'}`}
              ref={virtuosoRef}
              scrollerRef={setScrollerRef}
              data={processedList}
              firstItemIndex={virtualWindow.firstItemIndex}
              initialTopMostItemIndex={0}
              defaultItemHeight={MESSAGE_DEFAULT_ITEM_HEIGHT}
              computeItemKey={(_absoluteIndex, item) => getProcessedItemRowKey(item)}
              itemContent={renderVirtuosoItem}
              components={MESSAGE_VIRTUOSO_COMPONENTS}
              context={virtuosoContext}
              atBottomStateChange={handleAtBottomStateChange}
              totalListHeightChanged={handleTotalListHeightChanged}
              atBottomThreshold={MESSAGE_AT_BOTTOM_THRESHOLD_PX}
              followOutput={followOutput}
              rangeChanged={({ startIndex, endIndex }) =>
                handleRangeChanged(startIndex - virtualWindow.firstItemIndex, endIndex - virtualWindow.firstItemIndex)
              }
              startReached={loadPreviousAtBoundary}
              endReached={loadNextAtBoundary}
              increaseViewportBy={{ top: 600, bottom: 900 }}
              minOverscanItemCount={{ top: 4, bottom: 6 }}
              data-testid='message-list-scroller'
              className='conversation-transcript-scroll flex-1 min-h-0 h-full overflow-x-hidden overflow-y-auto pb-10px box-border'
              style={{ overflowAnchor: 'none' }}
              onPointerDown={handleMessageListPointerDown}
              onKeyDown={(event) => {
                if (['ArrowUp', 'PageUp', 'Home'].includes(event.key)) {
                  handleUserScrollIntent('away-from-tail');
                } else if (['ArrowDown', 'PageDown', 'End'].includes(event.key)) {
                  handleUserScrollIntent('toward-tail');
                }
              }}
              onScroll={handleMessageListScroll}
              onWheel={(event) => {
                if (event.deltaY < 0) handleUserScrollIntent('away-from-tail');
                else if (event.deltaY > 0) handleUserScrollIntent('toward-tail');
              }}
            />
          </ImagePreviewContext.Provider>
        </Image.PreviewGroup>

        <button
          type='button'
          aria-label={t('conversation.synonRuntime.messageList.scrollToBottom')}
          title={t('conversation.synonRuntime.messageList.scrollToBottom')}
          aria-hidden={!showScrollButton}
          inert={showScrollButton ? undefined : true}
          className={classNames('conversation-scroll-control conversation-scroll-control--bottom', {
            'conversation-scroll-control--visible': showScrollButton,
          })}
          onClick={handleScrollButtonClick}
        >
          <Down theme='outline' size={16} fill={iconColors.secondary} />
        </button>

        <button
          type='button'
          data-testid='jump-to-last-prompt'
          aria-label={t('conversation.synonRuntime.messageList.jumpToPreviousMessage')}
          title={t('conversation.synonRuntime.messageList.jumpToPreviousMessage')}
          aria-hidden={!showLastUserButton}
          inert={showLastUserButton ? undefined : true}
          className={classNames('conversation-scroll-control conversation-scroll-control--last-user', {
            'conversation-scroll-control--visible': showLastUserButton,
          })}
          onClick={scrollToLastUser}
        >
          <UpSmall theme='outline' size={14} />
          <span>{t('conversation.synonRuntime.messageList.previousMessage')}</span>
        </button>

        <SelectionReplyButton messages={list} />
      </div>
    </ConversationAnnotationsProvider>
  );
};

export default MessageList;

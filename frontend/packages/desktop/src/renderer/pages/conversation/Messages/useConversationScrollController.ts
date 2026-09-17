/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import type { TMessage } from '@/common/chat/chatLib';
import { isSynonBiomedOptimisticUserMessage } from './optimisticUserMessage';

type ScrollElementIntoViewOptions = {
  behavior?: ScrollBehavior;
  block?: ScrollLogicalPosition;
};

type UseConversationScrollControllerOptions = {
  conversationId: string;
  messages: TMessage[];
  itemCount: number;
  lastUserMessageId: string | null;
  lastUserRowIndex: number;
  scrollMessageIntoView: (messageId: string, options?: ScrollElementIntoViewOptions) => boolean;
  scrollToBottomItem: (behavior?: ScrollBehavior) => boolean;
};

type FollowOutput = false | 'auto' | 'smooth';

export type ConversationScrollIntent = 'away-from-tail' | 'toward-tail';

export type ConversationScrollController = {
  handleAtBottomStateChange: (atBottom: boolean) => void;
  handleRangeChanged: (startIndex: number, endIndex: number) => void;
  handleLastUserPositionChange: (aboveViewport: boolean) => void;
  handleTotalListHeightChanged: (height: number) => void;
  handleUserScrollIntent: (intent?: ConversationScrollIntent) => void;
  canLoadPreviousPage: () => boolean;
  followOutput: (atBottom: boolean) => FollowOutput;
  showScrollButton: boolean;
  showLastUserButton: boolean;
  scrollToBottom: (behavior?: ScrollBehavior) => void;
  scrollToLastUser: () => void;
};

/**
 * Keep one scroll authority: React Virtuoso owns geometry and anchoring while
 * this controller owns only product intent and the two Claude-style actions.
 */
export function useConversationScrollController({
  conversationId,
  messages,
  itemCount,
  lastUserMessageId,
  lastUserRowIndex,
  scrollMessageIntoView,
  scrollToBottomItem,
}: UseConversationScrollControllerOptions): ConversationScrollController {
  const [atBottom, setAtBottom] = useState(true);
  const [followingOutput, setFollowingOutput] = useState(true);
  const [lastUserAboveViewport, setLastUserAboveViewport] = useState(false);
  const userDetachedRef = useRef(false);
  const userTowardTailIntentRef = useRef(false);
  const initialScrollRequestedRef = useRef(false);
  const pendingTailFollowFrameRef = useRef<number | null>(null);
  const lastListHeightRef = useRef(0);
  const previousTailIdRef = useRef(messages.at(-1)?.id ?? null);

  const scheduleAttachedTailFollow = useCallback(() => {
    if (itemCount <= 0 || userDetachedRef.current || pendingTailFollowFrameRef.current !== null) return;
    pendingTailFollowFrameRef.current = requestAnimationFrame(() => {
      pendingTailFollowFrameRef.current = null;
      if (!userDetachedRef.current) scrollToBottomItem('auto');
    });
  }, [itemCount, scrollToBottomItem]);

  useLayoutEffect(() => {
    if (pendingTailFollowFrameRef.current !== null) {
      cancelAnimationFrame(pendingTailFollowFrameRef.current);
      pendingTailFollowFrameRef.current = null;
    }
    setAtBottom(true);
    setFollowingOutput(true);
    setLastUserAboveViewport(false);
    userDetachedRef.current = false;
    userTowardTailIntentRef.current = false;
    initialScrollRequestedRef.current = false;
    lastListHeightRef.current = 0;
    previousTailIdRef.current = messages.at(-1)?.id ?? null;
  }, [conversationId]);

  useLayoutEffect(() => {
    if (itemCount <= 0 || initialScrollRequestedRef.current) return;
    initialScrollRequestedRef.current = true;
    scrollToBottomItem('auto');
  }, [itemCount, scrollToBottomItem]);

  useLayoutEffect(() => {
    // `followOutput` covers newly appended Virtuoso rows, but a streaming tool
    // group or text segment usually grows in place without changing the item
    // count. Coalesce those updates into the next animation frame so Virtuoso
    // has measured the resized row before its own scrollToIndex authority moves
    // the viewport. Never write scrollTop directly from React.
    scheduleAttachedTailFollow();
  }, [messages, scheduleAttachedTailFollow]);

  useEffect(
    () => () => {
      if (pendingTailFollowFrameRef.current !== null) {
        cancelAnimationFrame(pendingTailFollowFrameRef.current);
      }
    },
    []
  );

  const handleAtBottomStateChange = useCallback((nextAtBottom: boolean) => {
    setAtBottom(nextAtBottom);
    // Virtuoso can briefly report `atBottom=true` while a variable-height
    // streamed row is being remeasured. That geometry signal must not undo an
    // explicit user detach, otherwise followOutput immediately pulls the
    // viewport down again and the page oscillates while the user scrolls.
    // Reattach only after a user gesture toward the tail or an explicit jump.
    if (nextAtBottom && (!userDetachedRef.current || userTowardTailIntentRef.current)) {
      userDetachedRef.current = false;
      userTowardTailIntentRef.current = false;
      setFollowingOutput(true);
    }
  }, []);

  const handleRangeChanged = useCallback(
    (startIndex: number, endIndex: number) => {
      if (lastUserRowIndex < 0) {
        setLastUserAboveViewport(false);
      } else if (lastUserRowIndex < startIndex) {
        setLastUserAboveViewport(true);
      } else if (lastUserRowIndex > endIndex) {
        setLastUserAboveViewport(false);
      }
    },
    [lastUserRowIndex]
  );

  const handleLastUserPositionChange = useCallback((aboveViewport: boolean) => {
    setLastUserAboveViewport(aboveViewport);
  }, []);

  const handleTotalListHeightChanged = useCallback(
    (height: number) => {
      if (!Number.isFinite(height) || height < 0) return;
      const previousHeight = lastListHeightRef.current;
      lastListHeightRef.current = height;
      if (height > previousHeight) scheduleAttachedTailFollow();
    },
    [scheduleAttachedTailFollow]
  );

  const handleUserScrollIntent = useCallback((intent: ConversationScrollIntent = 'away-from-tail') => {
    if (intent === 'toward-tail') {
      userTowardTailIntentRef.current = true;
      return;
    }
    userDetachedRef.current = true;
    userTowardTailIntentRef.current = false;
    setFollowingOutput(false);
  }, []);

  const canLoadPreviousPage = useCallback(() => userDetachedRef.current, []);

  const scrollToBottom = useCallback(
    (behavior: ScrollBehavior = 'smooth') => {
      if (itemCount <= 0) return;
      userDetachedRef.current = false;
      userTowardTailIntentRef.current = false;
      setFollowingOutput(true);
      scrollToBottomItem(behavior);
      setAtBottom(true);
    },
    [itemCount, scrollToBottomItem]
  );

  const scrollToLastUser = useCallback(() => {
    if (lastUserRowIndex < 0 || !lastUserMessageId) return;
    userDetachedRef.current = true;
    userTowardTailIntentRef.current = false;
    setFollowingOutput(false);
    scrollMessageIntoView(lastUserMessageId, { behavior: 'smooth', block: 'start' });
  }, [lastUserMessageId, lastUserRowIndex, scrollMessageIntoView]);

  useEffect(() => {
    const tail = messages.at(-1);
    const previousTailId = previousTailIdRef.current;
    previousTailIdRef.current = tail?.id ?? null;
    if (!tail || tail.id === previousTailId || tail.position !== 'right') return;
    if (!isSynonBiomedOptimisticUserMessage(tail.id)) return;
    userDetachedRef.current = false;
    userTowardTailIntentRef.current = false;
    setFollowingOutput(true);
    scrollToBottomItem('auto');
    setAtBottom(true);
  }, [messages, scrollToBottomItem]);

  const followOutput = useCallback((_virtuosoAtBottom: boolean): FollowOutput => {
    return userDetachedRef.current ? false : 'auto';
  }, []);

  return {
    handleAtBottomStateChange,
    handleRangeChanged,
    handleLastUserPositionChange,
    handleTotalListHeightChanged,
    handleUserScrollIntent,
    canLoadPreviousPage,
    followOutput,
    showScrollButton: itemCount > 0 && (!atBottom || !followingOutput),
    showLastUserButton: lastUserRowIndex >= 0 && lastUserAboveViewport,
    scrollToBottom,
    scrollToLastUser,
  };
}

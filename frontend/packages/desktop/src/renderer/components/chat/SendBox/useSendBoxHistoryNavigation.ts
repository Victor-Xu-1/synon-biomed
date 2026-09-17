import { isCaretOnFirstLine } from '@/renderer/utils/chat/messageHistory';
import type React from 'react';
import { useCallback, useEffect, useRef, useState } from 'react';

type Props = {
  conversationId?: string;
  inputHistory: string[];
  latestInputRef: React.MutableRefObject<string>;
  setInputRef: React.MutableRefObject<(value: string) => void>;
  containerRef: React.RefObject<HTMLDivElement | null>;
};

export const useSendBoxHistoryNavigation = ({
  conversationId,
  inputHistory,
  latestInputRef,
  setInputRef,
  containerRef,
}: Props) => {
  const [historyNavigationIndex, setHistoryNavigationIndex] = useState<number | null>(null);
  const historyDraftRef = useRef<string | null>(null);

  const applyHistoryInput = useCallback(
    (value: string) => {
      setInputRef.current(value);
      requestAnimationFrame(() => {
        const textarea = containerRef.current?.querySelector('textarea');
        if (!(textarea instanceof HTMLTextAreaElement)) return;
        const caret = textarea.value.length;
        textarea.setSelectionRange(caret, caret);
      });
    },
    [containerRef, setInputRef]
  );

  const clearHistoryNavigation = useCallback(
    (restoreDraft = false) => {
      const draft = historyDraftRef.current;
      historyDraftRef.current = null;
      setHistoryNavigationIndex(null);
      if (restoreDraft && draft !== null) applyHistoryInput(draft);
    },
    [applyHistoryInput]
  );

  useEffect(() => clearHistoryNavigation(false), [clearHistoryNavigation, conversationId]);

  const handleHistoryKeyDown = useCallback(
    (event: React.KeyboardEvent) => {
      if (event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return false;
      if (!(event.currentTarget instanceof HTMLTextAreaElement)) return false;
      if (event.key === 'Escape' && historyNavigationIndex !== null) {
        event.preventDefault();
        clearHistoryNavigation(true);
        return true;
      }
      if (!inputHistory.length) return false;
      if (event.key === 'ArrowUp') {
        if (historyNavigationIndex === null && !isCaretOnFirstLine(event.currentTarget)) return false;
        const nextIndex =
          historyNavigationIndex === null ? 0 : Math.min(historyNavigationIndex + 1, inputHistory.length - 1);
        const nextValue = inputHistory[nextIndex];
        if (nextValue === undefined) return false;
        if (historyNavigationIndex === null) historyDraftRef.current = latestInputRef.current;
        event.preventDefault();
        setHistoryNavigationIndex(nextIndex);
        applyHistoryInput(nextValue);
        return true;
      }
      if (event.key === 'ArrowDown' && historyNavigationIndex !== null) {
        event.preventDefault();
        if (historyNavigationIndex === 0) {
          clearHistoryNavigation(true);
          return true;
        }
        const nextValue = inputHistory[historyNavigationIndex - 1];
        if (nextValue === undefined) {
          clearHistoryNavigation(true);
          return true;
        }
        setHistoryNavigationIndex(historyNavigationIndex - 1);
        applyHistoryInput(nextValue);
        return true;
      }
      return false;
    },
    [applyHistoryInput, clearHistoryNavigation, historyNavigationIndex, inputHistory, latestInputRef]
  );

  return {
    clearHistoryNavigation,
    handleHistoryKeyDown,
    historyNavigationIndex,
  };
};

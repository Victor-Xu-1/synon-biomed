import { useTitleRename } from '@/renderer/pages/conversation/hooks/useTitleRename';
import { getConversationOrNull } from '@/renderer/pages/conversation/utils/conversationCache';
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import useSWR from 'swr';

type DesktopConversationTitleProps = {
  conversationId: string;
  fallbackTitle: string;
};

type ChatSurfaceAlignment = {
  left: number;
  width: number;
};

const DesktopConversationTitle: React.FC<DesktopConversationTitleProps> = ({ conversationId, fallbackTitle }) => {
  const { t } = useTranslation();
  const { data: conversation } = useSWR(conversationId ? `conversation/${conversationId}` : null, () =>
    getConversationOrNull(conversationId)
  );
  const title = conversation?.name?.trim() || fallbackTitle;
  const titleRef = useRef<HTMLSpanElement | null>(null);
  const cancelBlurRef = useRef(false);
  const [isOverflowing, setIsOverflowing] = useState(false);
  const [alignment, setAlignment] = useState<ChatSurfaceAlignment | null>(null);
  const { editingTitle, setEditingTitle, titleDraft, setTitleDraft, renameLoading, submitTitleRename } = useTitleRename(
    { title, conversation_id: conversationId }
  );

  const measureOverflow = useCallback(() => {
    const node = titleRef.current;
    setIsOverflowing(Boolean(node && node.scrollWidth > node.clientWidth + 1));
  }, []);

  useEffect(() => {
    measureOverflow();
    const node = titleRef.current;
    if (!node || typeof ResizeObserver === 'undefined') {
      window.addEventListener('resize', measureOverflow);
      return () => window.removeEventListener('resize', measureOverflow);
    }

    const observer = new ResizeObserver(measureOverflow);
    observer.observe(node);
    return () => observer.disconnect();
  }, [measureOverflow, title]);

  useEffect(() => {
    let disposed = false;
    let observer: ResizeObserver | undefined;
    let mountObserver: MutationObserver | undefined;
    let removeWindowListener: (() => void) | undefined;

    const alignToChatSurface = () => {
      if (disposed) return false;
      const titlebar = document.querySelector<HTMLElement>('.app-titlebar');
      const surfaces = Array.from(document.querySelectorAll<HTMLElement>('.chat-surface-fluid')).filter((element) => {
        const rect = element.getBoundingClientRect();
        return rect.width > 0 && rect.height > 0;
      });
      const surface = surfaces.at(-1);

      if (!titlebar || !surface) {
        return false;
      }

      const update = () => {
        const titlebarRect = titlebar.getBoundingClientRect();
        const surfaceRect = surface.getBoundingClientRect();
        setAlignment({
          left: Math.max(0, surfaceRect.left - titlebarRect.left),
          width: Math.max(0, surfaceRect.width),
        });
      };

      update();
      if (typeof ResizeObserver !== 'undefined') {
        observer = new ResizeObserver(update);
        observer.observe(titlebar);
        observer.observe(surface);
      }
      window.addEventListener('resize', update);
      removeWindowListener = () => window.removeEventListener('resize', update);
      return true;
    };

    if (!alignToChatSurface() && typeof MutationObserver !== 'undefined') {
      mountObserver = new MutationObserver(() => {
        if (alignToChatSurface()) mountObserver?.disconnect();
      });
      mountObserver.observe(document.body, { childList: true, subtree: true });
    }
    return () => {
      disposed = true;
      observer?.disconnect();
      mountObserver?.disconnect();
      removeWindowListener?.();
    };
  }, [conversationId]);

  const alignmentStyle = alignment
    ? { left: `${alignment.left}px`, width: `${alignment.width}px`, transform: 'translateY(-50%)' }
    : undefined;

  if (editingTitle) {
    return (
      <div className='app-titlebar-conversation app-titlebar-conversation--editing' style={alignmentStyle}>
        <input
          autoFocus
          value={titleDraft}
          maxLength={120}
          disabled={renameLoading}
          className='app-titlebar-conversation__input'
          aria-label={t('conversation.history.renamePlaceholder')}
          onChange={(event) => setTitleDraft(event.target.value)}
          onFocus={(event) => event.currentTarget.select()}
          onBlur={() => {
            if (cancelBlurRef.current) {
              cancelBlurRef.current = false;
              return;
            }
            void submitTitleRename();
          }}
          onKeyDown={(event) => {
            if (event.key === 'Enter') event.currentTarget.blur();
            if (event.key === 'Escape') {
              cancelBlurRef.current = true;
              setTitleDraft(title);
              setEditingTitle(false);
            }
          }}
        />
      </div>
    );
  }

  return (
    <button
      type='button'
      className={`app-titlebar-conversation ${isOverflowing ? 'app-titlebar-conversation--overflowing' : ''}`}
      aria-label={title}
      title={title}
      style={alignmentStyle}
      onClick={() => setEditingTitle(true)}
    >
      <span ref={titleRef} className='app-titlebar-conversation__text'>
        {title}
      </span>
    </button>
  );
};

export default DesktopConversationTitle;

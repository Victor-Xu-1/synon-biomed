/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { ipcBridge } from '@/common';
import type { IMessageSearchItem } from '@/common/types/conversation/messageSearch';
import SynonModal from '@/renderer/components/base/SynonModal';
import SynonBiomedAvatar from '@/renderer/components/synonBiomed/SynonBiomedAvatar';
import { usePresetAssistantInfo } from '@/renderer/hooks/synonBiomed/runtime/usePresetAssistantInfo';
import { resolveConversationLeadingMark } from '@/renderer/pages/conversation/utils/conversationAssistantIdentity';
import { blockMobileInputFocus, blurActiveElement } from '@/renderer/utils/ui/focus';
import { useAgentLogos } from '@/renderer/utils/synonBiomed/runtime/runtimeLogo';
import { Spin } from '@arco-design/web-react';
import { Close, CloseSmall, MessageOne, Search } from '@icon-park/react';
import classNames from 'classnames';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { flushSync } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import './ConversationSearchPopover.css';

const PAGE_SIZE = 30;
const SEARCH_DEBOUNCE_MS = 120;
const PAGE_KEYBOARD_STEP = 6;
const MRU_STORAGE_KEY = 'conversation.search.mru.v1';
const MRU_MAX_ENTRIES = 200;
const MRU_HALF_LIFE_MS = 7 * 24 * 60 * 60 * 1000;
const SNIPPET_MAX_LENGTH = 150;

type SearchMruEntry = {
  conversationId: string;
  lastUsedAt: number;
  useCount: number;
};

const escapeRegExp = (value: string): string => value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

const buildSnippet = (text: string, keyword: string, maxLength = SNIPPET_MAX_LENGTH): string => {
  const normalized = text.replace(/\s+/g, ' ').trim();
  if (!normalized || normalized.length <= maxLength) return normalized;
  const lowerKeyword = keyword.trim().toLocaleLowerCase();
  const matchIndex = lowerKeyword ? normalized.toLocaleLowerCase().indexOf(lowerKeyword) : -1;
  const start = Math.max(0, Math.min(normalized.length - maxLength, matchIndex < 0 ? 0 : matchIndex - 42));
  const end = Math.min(normalized.length, start + maxLength);
  return `${start > 0 ? '…' : ''}${normalized.slice(start, end).trim()}${end < normalized.length ? '…' : ''}`;
};

const renderHighlightedText = (text: string, keyword: string) => {
  const normalizedKeyword = keyword.trim();
  if (!normalizedKeyword) return text;
  const pattern = new RegExp(`(${escapeRegExp(normalizedKeyword)})`, 'ig');
  const lowerKeyword = normalizedKeyword.toLocaleLowerCase();
  return text.split(pattern).map((part, index) =>
    part.toLocaleLowerCase() === lowerKeyword ? (
      <mark key={`${part}-${index}`} className='conversation-search-modal__highlight'>
        {part}
      </mark>
    ) : (
      <React.Fragment key={`${part}-${index}`}>{part}</React.Fragment>
    )
  );
};

const formatTime = (timestamp: number): string => {
  if (!timestamp) return '';
  return new Intl.DateTimeFormat(undefined, {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(timestamp);
};

const readSearchMru = (): SearchMruEntry[] => {
  try {
    const parsed = JSON.parse(localStorage.getItem(MRU_STORAGE_KEY) || '[]');
    if (!Array.isArray(parsed)) return [];
    return parsed
      .filter(
        (entry): entry is SearchMruEntry =>
          typeof entry?.conversationId === 'string' &&
          Number.isFinite(entry?.lastUsedAt) &&
          Number.isFinite(entry?.useCount) &&
          entry.useCount > 0
      )
      .slice(0, MRU_MAX_ENTRIES);
  } catch {
    return [];
  }
};

const mruScore = (entry: SearchMruEntry | undefined): number => {
  if (!entry) return 0;
  const age = Math.max(0, Date.now() - entry.lastUsedAt);
  return Math.log1p(entry.useCount) * Math.pow(0.5, age / MRU_HALF_LIFE_MS);
};

const rankSearchItems = (
  items: IMessageSearchItem[],
  query: string,
  mruEntries: SearchMruEntry[]
): IMessageSearchItem[] => {
  const mruByID = new Map(mruEntries.map((entry) => [entry.conversationId, entry]));
  return items.toSorted((left, right) => {
    if (query) {
      const relevance = (right.relevance || 0) - (left.relevance || 0);
      if (relevance !== 0) return relevance;
    }
    const usage = mruScore(mruByID.get(right.conversation.id)) - mruScore(mruByID.get(left.conversation.id));
    if (usage !== 0) return usage;
    return (right.conversation.modified_at || 0) - (left.conversation.modified_at || 0);
  });
};

const projectNameForItem = (item: IMessageSearchItem): string => {
  if (item.project_name?.trim()) return item.project_name.trim();
  const extra = item.conversation.extra as { project_name?: unknown };
  return typeof extra.project_name === 'string' && extra.project_name.trim() ? extra.project_name.trim() : '';
};

interface ConversationSearchPopoverProps {
  onSessionClick?: () => void;
  onConversationSelect?: () => void;
  disabled?: boolean;
  buttonClassName?: string;
  label?: string;
  fullWidth?: boolean;
  renderTrigger?: (props: { onClick: () => void; isActive: boolean }) => React.ReactNode;
}

const ConversationAgentMark: React.FC<{ conversation: IMessageSearchItem['conversation'] }> = ({ conversation }) => {
  const logos = useAgentLogos();
  const { info: assistantInfo } = usePresetAssistantInfo(conversation);
  const leadingMark = resolveConversationLeadingMark(conversation, assistantInfo, logos);
  if (leadingMark.kind === 'emoji') {
    return (
      <span className='conversation-search-modal__agent-mark' title={leadingMark.label}>
        {leadingMark.value}
      </span>
    );
  }
  if (leadingMark.kind === 'image') {
    return (
      <img
        src={leadingMark.value}
        alt={leadingMark.label}
        title={leadingMark.label}
        className='conversation-search-modal__agent-image'
      />
    );
  }
  if (leadingMark.kind === 'assistant_fallback') {
    return <SynonBiomedAvatar size={18} />;
  }
  return <MessageOne theme='outline' size='17' className='conversation-search-modal__agent-mark' />;
};

const ConversationSearchPopover: React.FC<ConversationSearchPopoverProps> = ({
  onSessionClick,
  onConversationSelect,
  disabled = false,
  buttonClassName,
  label,
  fullWidth = false,
  renderTrigger,
}) => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [visible, setVisible] = useState(false);
  const [keyword, setKeyword] = useState('');
  const [debouncedKeyword, setDebouncedKeyword] = useState('');
  const [items, setItems] = useState<IMessageSearchItem[]>([]);
  const [page, setPage] = useState(0);
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [loadFailed, setLoadFailed] = useState(false);
  const [activeIndex, setActiveIndex] = useState(0);
  const [announcement, setAnnouncement] = useState('');
  const [mruEntries, setMruEntries] = useState<SearchMruEntry[]>(readSearchMru);
  const requestGeneration = useRef(0);
  const resultsRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!visible) return;
    const timer = window.setTimeout(() => setDebouncedKeyword(keyword.trim()), SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(timer);
  }, [keyword, visible]);

  const runSearch = useCallback(
    async (pageToLoad: number, append: boolean) => {
      const generation = ++requestGeneration.current;
      setLoadFailed(false);
      if (append) {
        setLoadingMore(true);
      } else {
        setLoading(true);
      }
      try {
        const result = await ipcBridge.database.searchConversationMessages.invoke({
          keyword: debouncedKeyword,
          page: pageToLoad,
          page_size: PAGE_SIZE,
        });
        if (requestGeneration.current !== generation) return;
        const ranked = rankSearchItems(result.items, debouncedKeyword, mruEntries);
        setItems((previous) => (append ? [...previous, ...ranked] : ranked));
        setPage(pageToLoad);
        setHasMore(result.has_more);
        if (!append) setActiveIndex(0);
      } catch (error) {
        if (requestGeneration.current !== generation) return;
        console.error('[ConversationSearchPopover] Search failed:', error);
        setLoadFailed(true);
        if (!append) {
          setItems([]);
          setPage(0);
          setHasMore(false);
        }
      } finally {
        if (requestGeneration.current === generation) {
          setLoading(false);
          setLoadingMore(false);
        }
      }
    },
    [debouncedKeyword, mruEntries]
  );

  useEffect(() => {
    if (!visible) return;
    void runSearch(0, false);
    return () => {
      requestGeneration.current += 1;
    };
  }, [runSearch, visible]);

  useEffect(() => {
    if (!visible || loading || loadFailed) return;
    const timer = window.setTimeout(() => {
      setAnnouncement(
        items.length === 0
          ? t('conversation.historySearch.empty')
          : t('conversation.historySearch.resultCount', { count: items.length })
      );
    }, 300);
    return () => window.clearTimeout(timer);
  }, [items.length, loadFailed, loading, t, visible]);

  useEffect(() => {
    const active = resultsRef.current?.querySelector<HTMLElement>(`[data-search-index='${activeIndex}']`);
    active?.scrollIntoView({ block: 'nearest' });
  }, [activeIndex]);

  const resetSearchState = useCallback(() => {
    requestGeneration.current += 1;
    setVisible(false);
    setKeyword('');
    setDebouncedKeyword('');
    setItems([]);
    setPage(0);
    setHasMore(false);
    setLoading(false);
    setLoadingMore(false);
    setLoadFailed(false);
    setActiveIndex(0);
    setAnnouncement('');
  }, []);

  const rememberConversation = useCallback((conversationId: string) => {
    setMruEntries((previous) => {
      const current = previous.find((entry) => entry.conversationId === conversationId);
      const next = [
        {
          conversationId,
          lastUsedAt: Date.now(),
          useCount: (current?.useCount || 0) + 1,
        },
        ...previous.filter((entry) => entry.conversationId !== conversationId),
      ]
        .toSorted((left, right) => right.lastUsedAt - left.lastUsedAt)
        .slice(0, MRU_MAX_ENTRIES);
      try {
        localStorage.setItem(MRU_STORAGE_KEY, JSON.stringify(next));
      } catch {
        // Search remains functional when persistent browser storage is unavailable.
      }
      return next;
    });
  }, []);

  const handleResultClick = useCallback(
    async (item: IMessageSearchItem) => {
      blockMobileInputFocus();
      blurActiveElement();
      rememberConversation(item.conversation.id);
      flushSync(resetSearchState);
      onConversationSelect?.();
      await Promise.resolve(
        navigate(`/conversation/${item.conversation.id}`, {
          state: {
            ...(item.message_id ? { targetMessageId: item.message_id } : {}),
            fromConversationSearch: true,
          },
        })
      );
      onSessionClick?.();
    },
    [navigate, onConversationSelect, onSessionClick, rememberConversation, resetSearchState]
  );

  const handleLoadMore = useCallback(() => {
    if (!visible || loading || loadingMore || !hasMore) return;
    void runSearch(page + 1, true);
  }, [hasMore, loading, loadingMore, page, runSearch, visible]);

  const handleOpen = useCallback(() => {
    if (!disabled) setVisible(true);
  }, [disabled]);

  useEffect(() => {
    const handleGlobalSearchShortcut = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.isComposing) return;
      const isCmdOrCtrl = event.metaKey || event.ctrlKey;
      if (!isCmdOrCtrl || !event.shiftKey || event.key.toLocaleLowerCase() !== 'f' || event.altKey) return;
      event.preventDefault();
      handleOpen();
    };
    document.addEventListener('keydown', handleGlobalSearchShortcut, true);
    return () => document.removeEventListener('keydown', handleGlobalSearchShortcut, true);
  }, [handleOpen]);

  const groupedResults = useMemo(() => {
    const groups = new Map<string, Array<{ item: IMessageSearchItem; index: number }>>();
    items.forEach((item, index) => {
      const project = projectNameForItem(item) || t('conversation.historySearch.otherProject');
      const group = groups.get(project) || [];
      group.push({ item, index });
      groups.set(project, group);
    });
    return [...groups.entries()];
  }, [items, t]);

  const handleInputKeyDown = useCallback(
    (event: React.KeyboardEvent<HTMLInputElement>) => {
      if (event.nativeEvent.isComposing) return;
      if (event.key === 'Escape') {
        event.preventDefault();
        resetSearchState();
        return;
      }
      if (items.length === 0) return;
      let nextIndex = activeIndex;
      switch (event.key) {
        case 'ArrowDown':
          nextIndex = Math.min(items.length - 1, activeIndex + 1);
          break;
        case 'ArrowUp':
          nextIndex = Math.max(0, activeIndex - 1);
          break;
        case 'PageDown':
          nextIndex = Math.min(items.length - 1, activeIndex + PAGE_KEYBOARD_STEP);
          break;
        case 'PageUp':
          nextIndex = Math.max(0, activeIndex - PAGE_KEYBOARD_STEP);
          break;
        case 'Home':
          nextIndex = 0;
          break;
        case 'End':
          nextIndex = items.length - 1;
          break;
        case 'Enter':
          event.preventDefault();
          void handleResultClick(items[activeIndex]);
          return;
        default:
          return;
      }
      event.preventDefault();
      setActiveIndex(nextIndex);
    },
    [activeIndex, handleResultClick, items, resetSearchState]
  );

  const resultContent = useMemo(() => {
    if (loading && items.length === 0) {
      return (
        <div className='conversation-search-modal__loading'>
          <Spin size={18} />
          <span>{t('common.loading')}</span>
        </div>
      );
    }
    if (loadFailed) {
      return (
        <div className='conversation-search-modal__state conversation-search-modal__state--error'>
          <span>{t('conversation.historySearch.loadFailed')}</span>
          <button type='button' onClick={() => void runSearch(0, false)}>
            {t('conversation.historySearch.retry')}
          </button>
        </div>
      );
    }
    if (items.length === 0) {
      return <div className='conversation-search-modal__state'>{t('conversation.historySearch.empty')}</div>;
    }
    return (
      <div
        id='conversation-search-results'
        ref={resultsRef}
        role='listbox'
        aria-label={t('conversation.historySearch.results')}
        className='conversation-search-modal__result-scroll'
        onScroll={(event) => {
          const target = event.currentTarget;
          if (target.scrollHeight - target.scrollTop - target.clientHeight < 56) handleLoadMore();
        }}
      >
        {groupedResults.map(([projectName, group]) => (
          <section key={projectName} className='conversation-search-modal__group'>
            <div className='conversation-search-modal__group-title'>{projectName}</div>
            {group.map(({ item, index }) => {
              const snippet = buildSnippet(item.preview_text, debouncedKeyword);
              const timestamp = item.message_created_at || item.conversation.modified_at;
              return (
                <button
                  id={`conversation-search-result-${index}`}
                  data-search-index={index}
                  role='option'
                  aria-selected={index === activeIndex}
                  key={`${item.conversation.id}-${item.message_id || 'conversation'}`}
                  type='button'
                  className={classNames('conversation-search-modal__result', {
                    'conversation-search-modal__result--active': index === activeIndex,
                  })}
                  onMouseEnter={() => setActiveIndex(index)}
                  onClick={() => void handleResultClick(item)}
                >
                  <ConversationAgentMark conversation={item.conversation} />
                  <span className='conversation-search-modal__result-copy'>
                    <span className='conversation-search-modal__result-title'>
                      {renderHighlightedText(
                        item.conversation.name || t('conversation.historySearch.untitled'),
                        debouncedKeyword
                      )}
                    </span>
                    <span className='conversation-search-modal__result-detail'>
                      {snippet
                        ? renderHighlightedText(snippet, debouncedKeyword)
                        : t('conversation.historySearch.recentConversation')}
                    </span>
                  </span>
                  <span className='conversation-search-modal__result-meta'>
                    {item.match_count && item.match_count > 1 ? (
                      <span>{t('conversation.historySearch.matches', { count: item.match_count })}</span>
                    ) : null}
                    <time>{formatTime(timestamp)}</time>
                  </span>
                </button>
              );
            })}
          </section>
        ))}
        {loadingMore ? (
          <div className='conversation-search-modal__loading-more'>
            <Spin size={14} />
            <span>{t('conversation.historySearch.loadingMore')}</span>
          </div>
        ) : null}
      </div>
    );
  }, [
    activeIndex,
    debouncedKeyword,
    groupedResults,
    handleLoadMore,
    handleResultClick,
    items,
    loadFailed,
    loading,
    loadingMore,
    runSearch,
    t,
  ]);

  const triggerAriaLabel = t('conversation.historySearch.tooltip');
  const triggerClassName = fullWidth
    ? 'conversation-search-trigger-full h-34px w-full p-0 bg-transparent border-none outline-none flex items-center justify-start gap-8px pl-10px pr-8px rd-0.5rem cursor-pointer shrink-0 transition-all group text-t-primary focus:outline-none focus-visible:outline-none'
    : 'h-34px w-34px p-0 bg-transparent rd-0.5rem flex items-center justify-center cursor-pointer shrink-0 transition-all border border-solid border-transparent text-t-secondary hover:text-t-primary';

  return (
    <>
      {renderTrigger ? (
        renderTrigger({ onClick: handleOpen, isActive: visible })
      ) : (
        <button
          type='button'
          aria-label={triggerAriaLabel}
          className={classNames(
            triggerClassName,
            {
              'hover:bg-fill-3 active:bg-fill-4': !disabled && fullWidth,
              'hover:bg-fill-2 hover:border-[color:var(--color-border-2)]': !disabled && !fullWidth,
              'opacity-50 cursor-not-allowed': disabled,
              'bg-aou-2 text-primary border-[color:var(--color-primary-light-3)]': visible && !disabled && !fullWidth,
            },
            buttonClassName
          )}
          onClick={handleOpen}
          disabled={disabled}
        >
          <Search theme='outline' size='16' fill='currentColor' className='conversation-search-trigger__icon' />
          {fullWidth && label ? <span className='collapsed-hidden text-14px font-500'>{label}</span> : null}
        </button>
      )}

      <SynonModal
        visible={visible}
        onCancel={resetSearchState}
        footer={null}
        showCustomClose={false}
        unmountOnExit
        className='conversation-search-modal'
        maskStyle={{ backdropFilter: 'none', WebkitBackdropFilter: 'none' }}
        style={{ width: 'min(620px, calc(100vw - 32px))' }}
        contentStyle={{ background: 'transparent', overflow: 'hidden', maxHeight: 'min(72vh, 560px)' }}
      >
        <div className='conversation-search-modal__panel'>
          <div className='conversation-search-modal__topline'>
            <span className='conversation-search-modal__title'>{t('conversation.historySearch.title')}</span>
            <span className='conversation-search-modal__shortcut'>{t('conversation.historySearch.shortcut')}</span>
            <button
              type='button'
              className='conversation-search-modal__close-btn'
              onClick={resetSearchState}
              aria-label={t('common.close')}
            >
              <Close size={15} />
            </button>
          </div>
          <div className='conversation-search-modal__searchbar'>
            <Search theme='outline' size='17' className='conversation-search-modal__search-icon' />
            <input
              autoFocus
              value={keyword}
              placeholder={t('conversation.historySearch.placeholder')}
              onChange={(event) => setKeyword(event.target.value)}
              onKeyDown={handleInputKeyDown}
              role='combobox'
              aria-expanded='true'
              aria-controls='conversation-search-results'
              aria-activedescendant={items[activeIndex] ? `conversation-search-result-${activeIndex}` : undefined}
              aria-autocomplete='list'
              className='conversation-search-modal__search-input'
            />
            {keyword ? (
              <button
                type='button'
                className='conversation-search-modal__clear-btn'
                onClick={() => setKeyword('')}
                aria-label={t('common.clearSearch')}
              >
                <CloseSmall theme='outline' size='14' />
              </button>
            ) : null}
          </div>
          <div aria-live='polite' aria-atomic='true' className='conversation-search-modal__live'>
            {announcement}
          </div>
          <div className='conversation-search-modal__body'>{resultContent}</div>
          <div className='conversation-search-modal__footer' aria-hidden='true'>
            <span>
              <kbd>↑</kbd>
              <kbd>↓</kbd>
              {t('conversation.historySearch.navigate')}
            </span>
            <span>
              <kbd>{t('conversation.historySearch.openKey')}</kbd>
              {t('conversation.historySearch.open')}
            </span>
            <span>
              <kbd>{t('conversation.historySearch.closeKey')}</kbd>
              {t('conversation.historySearch.close')}
            </span>
          </div>
        </div>
      </SynonModal>
    </>
  );
};

export default ConversationSearchPopover;

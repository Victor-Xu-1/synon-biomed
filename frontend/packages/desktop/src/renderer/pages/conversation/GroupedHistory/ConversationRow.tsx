/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import FlexFullContainer from '@/renderer/components/layout/FlexFullContainer';
import { cleanupSiderTooltips, getSiderTooltipProps } from '@/renderer/utils/ui/siderTooltip';
import { useLayoutContext } from '@/renderer/hooks/context/LayoutContext';
import { Checkbox, Dropdown, Menu, Message, Tooltip } from '@arco-design/web-react';
import { Copy, DeleteOne, Download, EditOne, MoreOne, MoveOne } from '@icon-park/react';
import classNames from 'classnames';
import React, { useCallback, useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { copyText } from '@/renderer/utils/ui/clipboard';

import type { ConversationRowProps } from './types';
import { resolveConversationTaskIndicatorState } from './conversationTaskIndicatorModel';

export {
  resolveConversationTaskIndicatorState,
  type ConversationTaskIndicatorState,
} from './conversationTaskIndicatorModel';

export const CONVERSATION_PREFETCH_INTENT_MS = 80;
export const CONVERSATION_MENU_BOUNDARY_DISTANCE = {
  left: 8,
  bottom: 8,
} as const;

const ConversationRow: React.FC<ConversationRowProps> = (props) => {
  const {
    conversation,
    isGenerating,
    hasCompletionUnread,
    collapsed,
    tooltipEnabled,
    batchMode,
    checked,
    selected,
    menuVisible,
    dimIcon = false,
    onToggleChecked,
    onConversationPrefetch,
    onConversationClick,
    onOpenMenu,
    onMenuVisibleChange,
    onEditStart,
    onMove,
    onDelete,
    onDownloadArtifacts,
  } = props;
  const prefetchTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const menuButtonRef = useRef<HTMLButtonElement | null>(null);
  const cancelPointerPrefetch = useCallback(() => {
    if (prefetchTimerRef.current === null) return;
    clearTimeout(prefetchTimerRef.current);
    prefetchTimerRef.current = null;
  }, []);
  const schedulePointerPrefetch = useCallback(() => {
    cancelPointerPrefetch();
    prefetchTimerRef.current = setTimeout(() => {
      prefetchTimerRef.current = null;
      onConversationPrefetch(conversation);
    }, CONVERSATION_PREFETCH_INTENT_MS);
  }, [cancelPointerPrefetch, conversation, onConversationPrefetch]);
  useEffect(() => cancelPointerPrefetch, [cancelPointerPrefetch]);
  const layout = useLayoutContext();
  const isMobile = layout?.isMobile ?? false;
  const { t } = useTranslation();
  const siderTooltipProps = getSiderTooltipProps(tooltipEnabled);
  const inlineNameTooltipEnabled = !collapsed && !isMobile && !!conversation.name;
  const taskIndicatorState = resolveConversationTaskIndicatorState(conversation, isGenerating);
  const isRunning = taskIndicatorState === 'running';
  const menuId = `conversation-menu-${conversation.id}`;

  const focusFirstMenuItem = useCallback(() => {
    requestAnimationFrame(() => {
      document.getElementById(menuId)?.querySelector<HTMLButtonElement>('[role="menuitem"]:not(:disabled)')?.focus();
    });
  }, [menuId]);

  const restoreMenuButtonFocus = useCallback(() => {
    requestAnimationFrame(() => menuButtonRef.current?.focus());
  }, []);

  const handleMenuVisibleChange = useCallback(
    (visible: boolean) => {
      onMenuVisibleChange(conversation.id, visible);
    },
    [conversation.id, onMenuVisibleChange]
  );

  useEffect(() => {
    if (menuVisible) focusFirstMenuItem();
  }, [focusFirstMenuItem, menuVisible]);

  const handleMenuKeyDown = useCallback(
    (event: React.KeyboardEvent<HTMLDivElement>) => {
      const items = Array.from(
        event.currentTarget.querySelectorAll<HTMLButtonElement>('[role="menuitem"]:not(:disabled)')
      );
      const currentIndex = items.indexOf(document.activeElement as HTMLButtonElement);

      if (event.key === 'Escape') {
        event.preventDefault();
        event.stopPropagation();
        onMenuVisibleChange(conversation.id, false);
        restoreMenuButtonFocus();
        return;
      }
      if (!items.length || !['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return;

      event.preventDefault();
      const nextIndex =
        event.key === 'Home'
          ? 0
          : event.key === 'End'
            ? items.length - 1
            : (currentIndex + (event.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length;
      items[nextIndex]?.focus();
    },
    [conversation.id, onMenuVisibleChange, restoreMenuButtonFocus]
  );

  const handleRowClick = () => {
    cleanupSiderTooltips();
    if (batchMode) {
      onToggleChecked(conversation);
      return;
    }
    onConversationClick(conversation);
  };

  const handleRowContextMenu = (event: React.MouseEvent<HTMLDivElement>) => {
    event.preventDefault();
    event.stopPropagation();
    cleanupSiderTooltips();
    if (batchMode) {
      return;
    }
    onOpenMenu(conversation);
  };

  const renderCompletionUnreadDot = () => {
    if (batchMode || !hasCompletionUnread || isRunning) {
      return null;
    }

    return (
      <span className='absolute right-8px top-1/2 -translate-y-1/2 flex items-center justify-center group-hover:hidden'>
        <span className='h-8px w-8px rounded-full bg-#2C7FFF shadow-[0_0_0_2px_rgba(44,127,255,0.18)]' />
      </span>
    );
  };

  return (
    <>
      <Tooltip
        key={conversation.id}
        {...siderTooltipProps}
        content={conversation.name || t('conversation.welcome.newConversation')}
        position='right'
      >
        <div
          id={'c-' + conversation.id}
          className={classNames(
            'chat-history__item synon-sidebar-conversation-row h-34px rd-8px flex items-center group cursor-pointer relative overflow-hidden shrink-0 conversation-item [&.conversation-item+&.conversation-item]:mt-2px min-w-0 transition-colors',
            collapsed ? 'justify-center px-0' : 'justify-start gap-8px pr-16px',
            // dimIcon means this row sits inside a project/cron parent — visually indent the row content while keeping the bg full-width
            !collapsed && (dimIcon ? 'pl-34px' : 'pl-10px'),
            {
              'hover:bg-fill-3': !batchMode && !selected,
              '!bg-fill-3': selected,
              'bg-[rgba(var(--primary-6),0.08)]': batchMode && checked,
            }
          )}
          onClick={handleRowClick}
          onPointerEnter={schedulePointerPrefetch}
          onPointerLeave={cancelPointerPrefetch}
          onFocus={() => onConversationPrefetch(conversation)}
          onTouchStart={() => onConversationPrefetch(conversation)}
          onContextMenu={handleRowContextMenu}
        >
          {batchMode && (
            <span
              className='mr-8px flex-center'
              onClick={(event) => {
                event.stopPropagation();
                onToggleChecked(conversation);
              }}
            >
              <Checkbox checked={checked} />
            </span>
          )}
          <span className='synon-sidebar-conversation-indicator size-12px flex items-center justify-center shrink-0'>
            {taskIndicatorState === 'running' && !batchMode ? (
              <span
                data-testid={`conversation-running-${conversation.id}`}
                role='status'
                aria-label={t('common.loading')}
                className='size-12px flex items-center justify-center'
              >
                <span
                  className='size-10px animate-spin rounded-full'
                  style={{
                    backgroundImage:
                      'conic-gradient(from 0deg, transparent 0deg, transparent 105deg, var(--color-text-1) 360deg)',
                    maskImage: 'radial-gradient(farthest-side, transparent calc(100% - 1.5px), #000 0)',
                    WebkitMaskImage: 'radial-gradient(farthest-side, transparent calc(100% - 1.5px), #000 0)',
                  }}
                  aria-hidden='true'
                />
              </span>
            ) : taskIndicatorState === 'paused' ? (
              <span
                data-testid={`conversation-paused-${conversation.id}`}
                className='size-10px flex items-center justify-center gap-2px'
                aria-label={t('conversation.synonRuntime.runtimeOperations.taskPaused')}
                role='status'
              >
                <span className='h-7px w-2px rounded-full bg-[var(--color-text-3)]' aria-hidden='true' />
                <span className='h-7px w-2px rounded-full bg-[var(--color-text-3)]' aria-hidden='true' />
              </span>
            ) : taskIndicatorState === 'success' ? (
              <span
                data-testid={`conversation-success-${conversation.id}`}
                className='size-7px rounded-full bg-success-6 shadow-[0_0_0_2px_rgba(var(--success-6),0.12)]'
                aria-label={t('common.success')}
                role='status'
              />
            ) : taskIndicatorState === 'attention' ? (
              <span
                data-testid={`conversation-attention-${conversation.id}`}
                className='size-7px rounded-full bg-orange-6 shadow-[0_0_0_2px_rgba(var(--orange-6),0.12)]'
                aria-label={t('common.error')}
                role='status'
              />
            ) : (
              <span
                className='size-7px rounded-full border border-solid border-[var(--color-text-3)]'
                aria-hidden='true'
              />
            )}
          </span>
          <FlexFullContainer className='h-24px min-w-0 flex-1 collapsed-hidden'>
            <Tooltip
              content={conversation.name}
              disabled={!inlineNameTooltipEnabled}
              trigger='hover'
              popupVisible={inlineNameTooltipEnabled ? undefined : false}
              unmountOnExit
              popupHoverStay={false}
              position='top'
            >
              <div className='chat-history__item-name synon-sidebar-conversation-name overflow-hidden text-ellipsis block w-full text-14px font-[500] lh-24px whitespace-nowrap min-w-0 text-t-primary'>
                <span className='block overflow-hidden text-ellipsis whitespace-nowrap'>{conversation.name}</span>
              </div>
            </Tooltip>
          </FlexFullContainer>

          {renderCompletionUnreadDot()}
          {!batchMode && (
            <div
              className={classNames(
                'absolute right-8px top-1/2 -translate-y-1/2 items-center justify-end !collapsed-hidden',
                {
                  flex: isMobile || menuVisible,
                  'hidden group-hover:flex': !isMobile && !menuVisible,
                }
              )}
              onClick={(event) => {
                event.stopPropagation();
              }}
            >
              <Dropdown
                droplist={
                  <Menu
                    id={menuId}
                    role='menu'
                    aria-label={t('common.more')}
                    theme='light'
                    className='app-overlay-menu synon-sidebar-conversation-menu w-[min(224px,calc(100vw-16px))] box-border p-4px'
                    style={{ maxWidth: 'calc(100vw - 16px)' }}
                    onKeyDown={handleMenuKeyDown}
                    onClickMenuItem={(key) => {
                      if (key === 'copy-id') {
                        void copyText(conversation.id)
                          .then(() => Message.success(t('conversation.history.taskIdCopied')))
                          .catch(() => Message.error(t('conversation.history.taskIdCopyFailed')));
                        return;
                      }
                      if (key === 'rename') {
                        onEditStart(conversation);
                        return;
                      }
                      if (key === 'move') {
                        onMove(conversation);
                        return;
                      }
                      if (key === 'download-artifacts') {
                        onDownloadArtifacts(conversation);
                        return;
                      }
                      if (key === 'delete') {
                        onDelete(conversation.id);
                      }
                    }}
                  >
                    <Menu.Item key='copy-id'>
                      <div className='flex items-center gap-8px'>
                        <Copy theme='outline' size='14' />
                        <span>{t('conversation.history.copyTaskId')}</span>
                      </div>
                    </Menu.Item>
                    <Menu.Item key='rename'>
                      <div className='flex items-center gap-8px'>
                        <EditOne theme='outline' size='14' />
                        <span>{t('conversation.history.rename')}</span>
                      </div>
                    </Menu.Item>
                    <Menu.Item key='move'>
                      <div className='flex items-center gap-8px'>
                        <MoveOne theme='outline' size='14' />
                        <span>{t('conversation.history.move')}</span>
                      </div>
                    </Menu.Item>
                    <Menu.Item key='download-artifacts'>
                      <div className='flex items-center gap-8px'>
                        <Download theme='outline' size='14' />
                        <span>{t('conversation.history.downloadArtifacts')}</span>
                      </div>
                    </Menu.Item>
                    <Menu.Item key='delete'>
                      <div className='flex items-center gap-8px'>
                        <DeleteOne theme='outline' size='14' />
                        <span>{t('conversation.history.deleteTitle')}</span>
                      </div>
                    </Menu.Item>
                  </Menu>
                }
                trigger='click'
                position='br'
                popupVisible={menuVisible}
                onVisibleChange={handleMenuVisibleChange}
                getPopupContainer={() => document.body}
                triggerProps={{
                  autoFitPosition: true,
                  autoFixPosition: true,
                  boundaryDistance: CONVERSATION_MENU_BOUNDARY_DISTANCE,
                  escToClose: true,
                  popupStyle: {
                    backgroundColor: '#fff',
                    maxWidth: 'calc(100vw - 16px)',
                    opacity: 1,
                    zIndex: 1000,
                  },
                }}
                unmountOnExit={false}
              >
                <button
                  ref={menuButtonRef}
                  type='button'
                  data-testid={`conversation-row-menu-${conversation.id}`}
                  aria-label={t('common.more')}
                  aria-haspopup='menu'
                  aria-expanded={menuVisible}
                  aria-controls={menuVisible ? menuId : undefined}
                  className={classNames(
                    'flex-center cursor-pointer transition-colors text-t-secondary hover:text-t-primary size-20px rd-4px border-none bg-transparent sider-action-btn',
                    {
                      flex: isMobile || menuVisible,
                      'hidden group-hover:flex': !isMobile && !menuVisible,
                    }
                  )}
                  onClick={(event) => {
                    event.stopPropagation();
                    onOpenMenu(conversation);
                  }}
                  onKeyDown={(event) => {
                    if (event.key === 'ArrowDown' && !menuVisible) {
                      event.preventDefault();
                      handleMenuVisibleChange(true);
                    }
                    if (event.key === 'Escape' && menuVisible) {
                      event.preventDefault();
                      handleMenuVisibleChange(false);
                      restoreMenuButtonFocus();
                    }
                  }}
                >
                  <MoreOne theme='outline' size='14' fill='currentColor' className='block leading-none' />
                </button>
              </Dropdown>
            </div>
          )}
        </div>
      </Tooltip>
    </>
  );
};

export default ConversationRow;

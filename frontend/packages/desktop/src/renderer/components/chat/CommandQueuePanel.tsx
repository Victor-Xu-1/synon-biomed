import type { ConversationCommandQueueItem } from '@/renderer/pages/conversation/platforms/useConversationCommandQueue';
import {
  type Modifier,
  closestCenter,
  DndContext,
  KeyboardSensor,
  PointerSensor,
  useSensor,
  useSensors,
  type DragEndEvent,
} from '@dnd-kit/core';
import {
  sortableKeyboardCoordinates,
  SortableContext,
  useSortable,
  verticalListSortingStrategy,
} from '@dnd-kit/sortable';
import { CSS } from '@dnd-kit/utilities';
import { Button, Dropdown, Menu, Tooltip, Typography } from '@arco-design/web-react';
import {
  Attention,
  CornerDownRight,
  Delete,
  Drag,
  EditOne,
  MessageOne,
  MoreOne,
  PauseOne,
  PlayOne,
  Redo,
  Switch,
} from '@icon-park/react';
import React, { useMemo, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import './CommandQueuePanel.css';

const getCommandPreview = (input: string): string => input.replace(/\s+/g, ' ').trim();
const restrictQueueDragToVerticalAxis: Modifier = ({ transform }) => ({ ...transform, x: 0 });

const createRestrictToQueueContainerModifier =
  (queueContainerRef: React.RefObject<HTMLDivElement | null>): Modifier =>
  ({ draggingNodeRect, overlayNodeRect, transform }) => {
    const container = queueContainerRef.current?.getBoundingClientRect();
    const active = overlayNodeRect ?? draggingNodeRect;
    if (!container || !active) return transform;
    return {
      ...transform,
      y: Math.min(Math.max(transform.y, container.top - active.top), container.bottom - active.bottom),
    };
  };

export type CommandQueuePanelProps = {
  items: ConversationCommandQueueItem[];
  isInterrupted: boolean;
  isQueueingEnabled: boolean;
  isSendNowDisabled: boolean;
  onInteractionLock: () => void;
  onInteractionUnlock: () => void;
  onEdit?: (item: ConversationCommandQueueItem) => void;
  onOpenInSideChat?: (item: ConversationCommandQueueItem) => void;
  onReorder: (activeCommandId: string, overCommandId: string) => void;
  onRemove: (commandId: string) => void;
  onRetry: (commandId: string) => void;
  onSendNow: (commandId: string) => void;
  onQueueingChange: (enabled: boolean) => void;
  onResumeInterruptedQueue: () => void;
};

type QueueRowProps = Pick<
  CommandQueuePanelProps,
  'onEdit' | 'onOpenInSideChat' | 'onRemove' | 'onRetry' | 'onSendNow' | 'onQueueingChange'
> & {
  item: ConversationCommandQueueItem;
  isReorderable: boolean;
  isQueueingEnabled: boolean;
  isSendNowDisabled: boolean;
  onDragHandlePointerDown: (event: React.PointerEvent<HTMLButtonElement>) => void;
};

const QueueRow: React.FC<QueueRowProps> = ({
  item,
  isReorderable,
  isQueueingEnabled,
  isSendNowDisabled,
  onEdit,
  onOpenInSideChat,
  onRemove,
  onRetry,
  onSendNow,
  onQueueingChange,
  onDragHandlePointerDown,
}) => {
  const { t } = useTranslation();
  const { attributes, listeners, setNodeRef, setActivatorNodeRef, transform, transition, isDragging } = useSortable({
    id: item.id,
    disabled: !isReorderable,
  });
  const preview = getCommandPreview(item.input);
  const contextCount = item.files.length + item.contextItems.length;
  const failed = item.pausedReason === 'send_failed';
  const style: React.CSSProperties = {
    transform: CSS.Transform.toString(transform),
    transition,
    opacity: isDragging ? 0.58 : 1,
    zIndex: isDragging ? 2 : undefined,
    position: 'relative',
  };

  return (
    <div ref={setNodeRef} style={style} className='conversation-command-queue__row' data-command-id={item.id}>
      <div className='conversation-command-queue__message'>
        <span className='conversation-command-queue__leading' data-queue-leading='true'>
          {isReorderable ? (
            <button
              {...attributes}
              {...listeners}
              ref={setActivatorNodeRef}
              type='button'
              className='conversation-command-queue__drag'
              aria-label={t('conversation.commandQueue.reorder')}
              onPointerDown={(event) => {
                onDragHandlePointerDown(event);
                listeners?.onPointerDown?.(event);
              }}
            >
              <Drag theme='outline' size='13' strokeWidth={2.5} aria-hidden='true' />
            </button>
          ) : null}
          <CornerDownRight
            className='conversation-command-queue__arrow'
            theme='outline'
            size='14'
            strokeWidth={2.3}
            aria-hidden='true'
          />
        </span>
        {failed ? (
          <Tooltip
            position='top'
            content={
              <div className='conversation-command-queue__tooltip'>
                <div>{t('conversation.commandQueue.failedTooltip')}</div>
                <div className='conversation-command-queue__tooltip-remedy'>
                  {t('conversation.commandQueue.failedRemedy')}
                </div>
              </div>
            }
          >
            <span
              className='conversation-command-queue__warning'
              title={t('conversation.commandQueue.failedTooltip')}
              aria-label={t('conversation.commandQueue.failedTooltip')}
            >
              <Attention theme='outline' size='14' aria-hidden='true' />
            </span>
          </Tooltip>
        ) : null}
        <Typography.Ellipsis rows={1} showTooltip className='conversation-command-queue__preview'>
          {preview}
        </Typography.Ellipsis>
        {contextCount > 0 ? (
          <span className='conversation-command-queue__files'>
            {t('conversation.commandQueue.contextItems', { count: contextCount })}
          </span>
        ) : null}
      </div>
      <div className='conversation-command-queue__actions'>
        <Tooltip
          position='top'
          content={
            failed ? (
              <div className='conversation-command-queue__tooltip'>
                <div>{t('conversation.commandQueue.retryTooltip')}</div>
                <div className='conversation-command-queue__tooltip-remedy'>
                  {t('conversation.commandQueue.retryRemedy')}
                </div>
              </div>
            ) : (
              t('conversation.commandQueue.sendNowTooltip')
            )
          }
        >
          {failed ? (
            <Button
              size='small'
              type='text'
              className='conversation-command-queue__primary-action'
              aria-label={`${t('conversation.commandQueue.retry')}: ${preview}`}
              onClick={() => onRetry(item.id)}
            >
              <Redo theme='outline' size='14' aria-hidden='true' />
              <span>{t('conversation.commandQueue.retry')}</span>
            </Button>
          ) : (
            <Button
              size='small'
              type='text'
              className='conversation-command-queue__primary-action'
              disabled={isSendNowDisabled}
              aria-label={`${t('conversation.commandQueue.sendNow')}: ${preview}`}
              onClick={() => onSendNow(item.id)}
            >
              <CornerDownRight theme='outline' size='14' aria-hidden='true' />
              <span>{t('conversation.commandQueue.sendNow')}</span>
            </Button>
          )}
        </Tooltip>
        <Button
          size='small'
          type='text'
          shape='circle'
          className='conversation-command-queue__icon-action'
          aria-label={t('conversation.commandQueue.remove')}
          onClick={() => onRemove(item.id)}
        >
          <Delete theme='outline' size='14' strokeWidth={2.5} aria-hidden='true' />
        </Button>
        <Dropdown
          trigger='click'
          droplist={
            <Menu>
              {onEdit ? (
                <Menu.Item key='edit' onClick={() => onEdit(item)}>
                  <span className='conversation-command-queue__menu-item'>
                    <EditOne theme='outline' size='14' aria-hidden='true' />
                    <span>{t('conversation.commandQueue.edit')}</span>
                  </span>
                </Menu.Item>
              ) : null}
              {onOpenInSideChat ? (
                <Menu.Item key='side-chat' onClick={() => onOpenInSideChat(item)}>
                  <span className='conversation-command-queue__menu-item'>
                    <MessageOne theme='outline' size='14' aria-hidden='true' />
                    <span>{t('conversation.commandQueue.openInSideChat')}</span>
                  </span>
                </Menu.Item>
              ) : null}
              <Menu.Item key='queue-mode' onClick={() => onQueueingChange(!isQueueingEnabled)}>
                <span className='conversation-command-queue__menu-item'>
                  <Switch theme='outline' size='14' aria-hidden='true' />
                  <span>
                    {t(
                      isQueueingEnabled
                        ? 'conversation.commandQueue.turnOffQueueing'
                        : 'conversation.commandQueue.turnOnQueueing'
                    )}
                  </span>
                </span>
              </Menu.Item>
            </Menu>
          }
        >
          <Button
            size='small'
            type='text'
            shape='circle'
            className='conversation-command-queue__icon-action'
            aria-label={t('conversation.commandQueue.moreActions')}
          >
            <MoreOne theme='outline' size='14' strokeWidth={2.5} aria-hidden='true' />
          </Button>
        </Dropdown>
      </div>
    </div>
  );
};

const CommandQueuePanel: React.FC<CommandQueuePanelProps> = ({
  items,
  isInterrupted,
  isQueueingEnabled,
  isSendNowDisabled,
  onInteractionLock,
  onInteractionUnlock,
  onEdit,
  onOpenInSideChat,
  onReorder,
  onRemove,
  onRetry,
  onSendNow,
  onQueueingChange,
  onResumeInterruptedQueue,
}) => {
  const { t } = useTranslation();
  const queueContainerRef = useRef<HTMLDivElement | null>(null);
  const activeDragHandleRef = useRef<HTMLButtonElement | null>(null);
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates })
  );
  const dragModifiers = useMemo(
    () => [restrictQueueDragToVerticalAxis, createRestrictToQueueContainerModifier(queueContainerRef)],
    []
  );

  if (items.length === 0) return null;

  const finishDrag = () => {
    onInteractionUnlock();
    activeDragHandleRef.current?.blur();
    activeDragHandleRef.current = null;
  };
  const handleDragEnd = ({ active, over }: DragEndEvent) => {
    finishDrag();
    if (over && active.id !== over.id) onReorder(String(active.id), String(over.id));
  };

  return (
    <div
      data-testid='conversation-command-queue'
      data-queue-visual-authority='codex'
      data-queue-surface-authority='composer-top-tray'
      aria-label={t('conversation.commandQueue.controlTitle')}
      className='conversation-command-queue'
    >
      {isInterrupted ? (
        <>
          <div className='conversation-command-queue__paused' role='status'>
            <span className='conversation-command-queue__paused-label'>
              <PauseOne theme='outline' size='14' aria-hidden='true' />
              <span>{t('conversation.commandQueue.interrupted')}</span>
            </span>
            <Button
              size='small'
              type='text'
              className='conversation-command-queue__resume'
              onClick={onResumeInterruptedQueue}
            >
              <PlayOne theme='outline' size='14' aria-hidden='true' />
              <span>{t('conversation.commandQueue.resume')}</span>
            </Button>
          </div>
          <div className='conversation-command-queue__divider' aria-hidden='true' />
        </>
      ) : null}
      <DndContext
        sensors={sensors}
        collisionDetection={closestCenter}
        modifiers={dragModifiers}
        onDragStart={onInteractionLock}
        onDragCancel={finishDrag}
        onDragEnd={handleDragEnd}
      >
        <SortableContext items={items.map((item) => item.id)} strategy={verticalListSortingStrategy}>
          <div
            ref={queueContainerRef}
            data-command-queue-list='true'
            data-drag-axis='vertical'
            data-drag-bounds='queue'
            className='conversation-command-queue__list'
          >
            {items.map((item) => (
              <QueueRow
                key={item.id}
                item={item}
                isReorderable={items.length > 1}
                isQueueingEnabled={isQueueingEnabled}
                isSendNowDisabled={isSendNowDisabled}
                onEdit={onEdit}
                onOpenInSideChat={onOpenInSideChat}
                onRemove={onRemove}
                onRetry={onRetry}
                onSendNow={onSendNow}
                onQueueingChange={onQueueingChange}
                onDragHandlePointerDown={(event) => {
                  activeDragHandleRef.current = event.currentTarget;
                }}
              />
            ))}
          </div>
        </SortableContext>
      </DndContext>
    </div>
  );
};

export default CommandQueuePanel;

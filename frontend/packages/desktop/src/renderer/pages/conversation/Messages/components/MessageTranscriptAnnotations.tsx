import type { IMessageText } from '@/common/chat/chatLib';
import type {
  SynonBiomedTranscriptAnnotation,
  SynonBiomedTranscriptAnnotationKind,
} from '@/renderer/services/synonBiomedAnnotations';
import { Button, Input, Message, Modal, Select, Spin, Tooltip } from '@arco-design/web-react';
import { Bookmark, Delete, Refresh } from '@icon-park/react';
import React, { type RefObject, useCallback, useEffect, useMemo, useState } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { annotationsForMessage, useConversationAnnotations } from './ConversationAnnotationsContext';
import {
  domRangeForOffsets,
  resolveTranscriptAnchor,
  selectionAnchorFromRange,
  type TranscriptAnchorResolution,
  type TranscriptSelectionAnchor,
} from './transcriptAnchorModel';

const MAX_ANCHOR_LENGTH = 1200;

type Highlight = {
  annotation: SynonBiomedTranscriptAnnotation;
  resolution: TranscriptAnchorResolution;
  rects: Array<{ left: number; top: number; width: number; height: number }>;
};

const MessageTranscriptAnnotations: React.FC<{
  message: IMessageText;
  messageIndex: number;
  anchorText: string;
  contentRootRef?: RefObject<HTMLDivElement | null>;
  highlightHostRef?: RefObject<HTMLDivElement | null>;
  alwaysVisible?: boolean;
}> = ({ message, messageIndex, anchorText, contentRootRef, highlightHostRef, alwaysVisible = false }) => {
  const { t } = useTranslation();
  const context = useConversationAnnotations();
  const [visible, setVisible] = useState(false);
  const [note, setNote] = useState('');
  const [kind, setKind] = useState<SynonBiomedTranscriptAnnotationKind>('bookmark');
  const [editing, setEditing] = useState<SynonBiomedTranscriptAnnotation | null>(null);
  const [selectionAnchor, setSelectionAnchor] = useState<TranscriptSelectionAnchor | null>(null);
  const [draftAnchor, setDraftAnchor] = useState<TranscriptSelectionAnchor | null>(null);
  const [saving, setSaving] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [highlights, setHighlights] = useState<Highlight[]>([]);
  const [messageApi, messageContextHolder] = Message.useMessage();
  const messageAnnotations = useMemo(
    () => annotationsForMessage(context?.annotations ?? [], message.msg_id, messageIndex),
    [context?.annotations, message.msg_id, messageIndex]
  );

  const openEdit = useCallback(
    (annotation: SynonBiomedTranscriptAnnotation) => {
      setEditing(annotation);
      setSelectionAnchor(null);
      setDraftAnchor(null);
      setNote(annotation.note);
      setKind(annotation.kind);
      setVisible(true);
      if (!annotation.readAt && context) {
        void context.update(annotation.id, { read: true }).catch((): void => undefined);
      }
    },
    [context]
  );

  useEffect(() => {
    const root = contentRootRef?.current;
    if (!root) return;
    const captureSelection = () => {
      const selection = window.getSelection();
      if (!selection || selection.rangeCount === 0 || selection.isCollapsed) {
        setSelectionAnchor(null);
        return;
      }
      setSelectionAnchor(selectionAnchorFromRange(root, selection.getRangeAt(0)));
    };
    document.addEventListener('selectionchange', captureSelection);
    return () => document.removeEventListener('selectionchange', captureSelection);
  }, [contentRootRef]);

  useEffect(() => {
    const root = contentRootRef?.current;
    if (!root || messageAnnotations.length === 0) {
      setHighlights((current) => (current.length === 0 ? current : []));
      return;
    }
    let frame = 0;
    const measure = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        const renderedText = root.textContent ?? '';
        const rootRect = root.getBoundingClientRect();
        setHighlights(
          messageAnnotations.map((annotation) => {
            const resolution = resolveTranscriptAnchor(renderedText, annotation);
            if (resolution.status !== 'resolved') return { annotation, resolution, rects: [] as Highlight['rects'] };
            const range = domRangeForOffsets(root, resolution.startOffset, resolution.endOffset);
            const rects =
              range && typeof range.getClientRects === 'function'
                ? Array.from(range.getClientRects()).map((rect) => ({
                    left: rect.left - rootRect.left + root.scrollLeft,
                    top: rect.top - rootRect.top + root.scrollTop,
                    width: rect.width,
                    height: rect.height,
                  }))
                : [];
            return { annotation, resolution, rects };
          })
        );
      });
    };
    measure();
    const resizeObserver = new ResizeObserver(measure);
    resizeObserver.observe(root);
    return () => {
      cancelAnimationFrame(frame);
      resizeObserver.disconnect();
    };
  }, [anchorText, contentRootRef, messageAnnotations]);

  useEffect(() => {
    const root = contentRootRef?.current;
    if (!root || highlights.length === 0) return;
    const openFromHighlight = (event: MouseEvent) => {
      const target = event.target as Element | null;
      if (target?.closest('a, button, input, textarea, select')) return;
      const rootRect = root.getBoundingClientRect();
      const x = event.clientX - rootRect.left + root.scrollLeft;
      const y = event.clientY - rootRect.top + root.scrollTop;
      const match = highlights.find(({ rects }) =>
        rects.some(
          (rect) => x >= rect.left && x <= rect.left + rect.width && y >= rect.top && y <= rect.top + rect.height
        )
      );
      if (match) openEdit(match.annotation);
    };
    root.addEventListener('click', openFromHighlight);
    return () => root.removeEventListener('click', openFromHighlight);
  }, [contentRootRef, highlights, openEdit]);

  if (!context) return null;

  const openWholeMessage = () => {
    setEditing(null);
    setSelectionAnchor(null);
    setDraftAnchor(null);
    setNote('');
    setKind('bookmark');
    setVisible(true);
  };

  const openSelectedText = () => {
    if (!selectionAnchor) return;
    setEditing(null);
    setDraftAnchor(selectionAnchor);
    setNote('');
    setKind('annotation');
    setVisible(true);
  };

  const save = async () => {
    setSaving(true);
    try {
      if (editing) {
        await context.update(editing.id, { note: note.trim() });
        messageApi.success(t('conversation.transcriptAnnotations.updated'));
      } else {
        const persistedAnchor = draftAnchor ?? {
          anchorText: anchorText.slice(0, MAX_ANCHOR_LENGTH),
          startOffset: null,
          endOffset: null,
        };
        await context.create({
          messageUuid: message.msg_id ?? message.id,
          messageIndex,
          blockIndex: 0,
          source: 'assistant',
          anchorText: persistedAnchor.anchorText.slice(0, MAX_ANCHOR_LENGTH),
          startOffset: persistedAnchor.startOffset,
          endOffset: persistedAnchor.endOffset,
          kind,
          note: note.trim(),
        });
        messageApi.success(
          t(
            kind === 'bookmark'
              ? 'conversation.transcriptAnnotations.bookmarked'
              : 'conversation.transcriptAnnotations.selectionAnnotated'
          )
        );
      }
      setVisible(false);
      setSelectionAnchor(null);
      setDraftAnchor(null);
      window.getSelection()?.removeAllRanges();
    } catch (error) {
      console.warn('[MessageTranscriptAnnotations] Failed to save transcript annotation', {
        errorName: error instanceof Error ? error.name : typeof error,
      });
      messageApi.error(t('conversation.transcriptAnnotations.saveFailed'));
    } finally {
      setSaving(false);
    }
  };

  const remove = async (annotation: SynonBiomedTranscriptAnnotation) => {
    setDeleting(true);
    try {
      await context.remove(annotation.id);
      setVisible(false);
      messageApi.success(t('conversation.transcriptAnnotations.deleted'));
    } catch (error) {
      console.warn('[MessageTranscriptAnnotations] Failed to delete transcript annotation', {
        errorName: error instanceof Error ? error.name : typeof error,
      });
      messageApi.error(t('conversation.transcriptAnnotations.deleteFailed'));
    } finally {
      setDeleting(false);
    }
  };

  const editingResolution = editing
    ? resolveTranscriptAnchor(contentRootRef?.current?.textContent ?? anchorText, editing)
    : null;
  const previewText = editing?.anchorText ?? draftAnchor?.anchorText ?? anchorText;
  const orphaned = highlights.filter(({ resolution }) => resolution.status === 'orphaned');

  return (
    <>
      {messageContextHolder}
      {highlightHostRef?.current &&
        createPortal(
          <div aria-hidden='true' data-testid='transcript-highlight-layer'>
            {highlights.flatMap(({ annotation, rects }) =>
              rects.map((rect, index) => (
                <mark
                  key={`${annotation.id}-${index}`}
                  data-annotation-id={annotation.id}
                  className='absolute bg-warning-3/65 ring-1 ring-warning-5/35'
                  style={rect}
                />
              ))
            )}
          </div>,
          highlightHostRef.current
        )}

      {selectionAnchor && (
        <Button
          type='secondary'
          size='mini'
          aria-label={t('conversation.transcriptAnnotations.annotateSelection')}
          onMouseDown={(event) => event.preventDefault()}
          onClick={openSelectedText}
        >
          {t('conversation.transcriptAnnotations.annotateSelected')}
        </Button>
      )}
      <Tooltip
        content={
          messageAnnotations.length
            ? t('conversation.transcriptAnnotations.annotationCount', { count: messageAnnotations.length })
            : t('conversation.transcriptAnnotations.addWholeMessage')
        }
      >
        <Button
          type='text'
          size='mini'
          className={
            messageAnnotations.length
              ? 'text-warning-6'
              : alwaysVisible
                ? 'text-t-secondary'
                : 'opacity-0 pointer-events-none group-hover:opacity-100 group-hover:pointer-events-auto focus:opacity-100 focus:pointer-events-auto'
          }
          aria-label={
            messageAnnotations.length
              ? t('conversation.transcriptAnnotations.messageAnnotationCount', { count: messageAnnotations.length })
              : t('conversation.transcriptAnnotations.addMessage')
          }
          icon={<Bookmark theme={messageAnnotations.length ? 'filled' : 'outline'} size={15} />}
          onClick={openWholeMessage}
        />
      </Tooltip>
      {context.loading && <Spin size={12} aria-label={t('conversation.transcriptAnnotations.loading')} />}
      {context.error && (
        <Tooltip content={t('conversation.transcriptAnnotations.loadFailed')}>
          <Button
            type='text'
            size='mini'
            status='danger'
            aria-label={t('conversation.transcriptAnnotations.reload')}
            icon={<Refresh />}
            onClick={() => void context.refresh()}
          />
        </Tooltip>
      )}
      {messageAnnotations.map((annotation) => {
        const resolution = highlights.find((item) => item.annotation.id === annotation.id)?.resolution;
        const isOrphaned = resolution?.status === 'orphaned';
        return (
          <Tooltip
            key={annotation.id}
            content={
              isOrphaned
                ? t('conversation.transcriptAnnotations.orphanedTooltip')
                : annotation.note ||
                  t(
                    annotation.kind === 'bookmark'
                      ? 'conversation.transcriptAnnotations.kind.bookmark'
                      : 'conversation.transcriptAnnotations.kind.annotation'
                  )
            }
          >
            <button
              type='button'
              className={`max-w-140px truncate border-0 bg-transparent px-3px text-10px cursor-pointer hover:text-t-primary ${isOrphaned ? 'text-danger-6' : 'text-t-tertiary'}`}
              aria-label={t('conversation.transcriptAnnotations.openNamed', {
                name: annotation.note || annotation.id,
                state: isOrphaned ? ` ${t('conversation.transcriptAnnotations.orphaned')}` : '',
              })}
              onClick={() => openEdit(annotation)}
            >
              {isOrphaned
                ? t('conversation.transcriptAnnotations.orphaned')
                : t(
                    annotation.kind === 'bookmark'
                      ? 'conversation.transcriptAnnotations.kind.bookmark'
                      : 'conversation.transcriptAnnotations.kind.annotation'
                  )}
            </button>
          </Tooltip>
        );
      })}

      <Modal
        title={t(
          editing
            ? 'conversation.transcriptAnnotations.editTitle'
            : draftAnchor
              ? 'conversation.transcriptAnnotations.selectionTitle'
              : 'conversation.transcriptAnnotations.addTitle'
        )}
        visible={visible}
        onCancel={() => setVisible(false)}
        onOk={() => void save()}
        confirmLoading={saving}
        okText={t(editing ? 'common.save' : 'common.add')}
        cancelText={t('common.cancel')}
        unmountOnExit
        footer={(cancelButton, okButton) => (
          <div className='flex items-center justify-between'>
            <div>
              {editing && (
                <Button
                  type='text'
                  status='danger'
                  loading={deleting}
                  icon={<Delete theme='outline' size={14} />}
                  onClick={() => void remove(editing)}
                >
                  {t('common.delete')}
                </Button>
              )}
            </div>
            <div className='flex gap-8px'>
              {cancelButton}
              {okButton}
            </div>
          </div>
        )}
      >
        <div className='flex flex-col gap-14px'>
          {editingResolution?.status === 'orphaned' && (
            <div role='status' className='rd-4px bg-danger-1 px-10px py-8px text-12px text-danger-6'>
              {t('conversation.transcriptAnnotations.orphanedDetail')}
            </div>
          )}
          <div className='max-h-100px overflow-auto whitespace-pre-wrap border-l-2 border-solid border-[var(--color-border-3)] pl-10px text-11px leading-18px text-t-tertiary'>
            {previewText.slice(0, 500)}
          </div>
          <label className='flex flex-col gap-6px text-12px text-t-secondary'>
            {t('conversation.transcriptAnnotations.type')}
            <Select
              aria-label={t('conversation.transcriptAnnotations.typeAria')}
              value={kind}
              disabled={Boolean(editing)}
              onChange={setKind}
            >
              <Select.Option value='bookmark'>{t('conversation.transcriptAnnotations.kind.bookmark')}</Select.Option>
              <Select.Option value='annotation'>
                {t('conversation.transcriptAnnotations.kind.annotation')}
              </Select.Option>
            </Select>
          </label>
          <label className='flex flex-col gap-6px text-12px text-t-secondary'>
            {t('conversation.transcriptAnnotations.note')}
            <Input.TextArea
              aria-label={t('conversation.transcriptAnnotations.noteAria')}
              value={note}
              autoSize={{ minRows: 3, maxRows: 7 }}
              maxLength={4000}
              showWordLimit
              onChange={setNote}
            />
          </label>
          {orphaned.length > 0 && !editing && (
            <span className='text-11px text-t-tertiary'>
              {t('conversation.transcriptAnnotations.orphanedCount', { count: orphaned.length })}
            </span>
          )}
        </div>
      </Modal>
    </>
  );
};

export default MessageTranscriptAnnotations;

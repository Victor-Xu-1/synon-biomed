import React, { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  capturePreviewRegionViewport,
  collectVisibleTextInPreviewRegion,
  normalizePreviewRegion,
  PREVIEW_REGION_COMMENT_MAX_LENGTH,
  previewRegionToStyle,
  type PreviewRegionComment,
  type PreviewRegionCommentSource,
  type PreviewRegionPoint,
  type PreviewRegionRect,
} from './previewRegionCommentModel';

type PreviewRegionCommentLayerProps = {
  active: boolean;
  source: PreviewRegionCommentSource;
  onAdd: (comment: PreviewRegionComment) => void;
  onExit: () => void;
};

const PreviewRegionCommentLayer: React.FC<PreviewRegionCommentLayerProps> = ({ active, source, onAdd, onExit }) => {
  const { t } = useTranslation();
  const layerRef = useRef<HTMLDivElement>(null);
  const noteRef = useRef<HTMLTextAreaElement>(null);
  const [dragStart, setDragStart] = useState<PreviewRegionPoint | null>(null);
  const [dragRegion, setDragRegion] = useState<PreviewRegionRect | null>(null);
  const [region, setRegion] = useState<PreviewRegionRect | null>(null);
  const [visibleText, setVisibleText] = useState('');
  const [note, setNote] = useState('');

  useEffect(() => {
    if (!active) {
      setDragStart(null);
      setDragRegion(null);
      setRegion(null);
      setVisibleText('');
      setNote('');
      return;
    }

    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      if (region || dragStart || dragRegion) {
        setDragStart(null);
        setDragRegion(null);
        setRegion(null);
        setVisibleText('');
        setNote('');
      } else {
        onExit();
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [active, dragRegion, dragStart, onExit, region]);

  useEffect(() => {
    if (region) noteRef.current?.focus();
  }, [region]);

  if (!active) return null;

  const clearSelection = () => {
    setDragStart(null);
    setDragRegion(null);
    setRegion(null);
    setVisibleText('');
    setNote('');
  };

  const selectFullView = () => {
    const layer = layerRef.current;
    if (!layer) return;
    const bounds = layer.getBoundingClientRect();
    const nextRegion: PreviewRegionRect = {
      leftPercent: 0,
      topPercent: 0,
      widthPercent: 100,
      heightPercent: 100,
    };
    setRegion(nextRegion);
    setDragRegion(null);
    setVisibleText(collectVisibleTextInPreviewRegion(layer.parentElement ?? layer, bounds));
  };

  const finishSelection = (event: React.PointerEvent<HTMLDivElement>) => {
    const layer = layerRef.current;
    if (!layer || !dragStart) return;
    const bounds = layer.getBoundingClientRect();
    const nextRegion = normalizePreviewRegion(
      dragStart,
      { x: event.clientX, y: event.clientY },
      { left: bounds.left, top: bounds.top, width: bounds.width, height: bounds.height }
    );
    setDragStart(null);
    setDragRegion(null);
    setRegion(nextRegion);
    if (!nextRegion) {
      setVisibleText('');
      return;
    }

    const selectedRect = new DOMRect(
      bounds.left + (nextRegion.leftPercent / 100) * bounds.width,
      bounds.top + (nextRegion.topPercent / 100) * bounds.height,
      (nextRegion.widthPercent / 100) * bounds.width,
      (nextRegion.heightPercent / 100) * bounds.height
    );
    setVisibleText(collectVisibleTextInPreviewRegion(layer.parentElement ?? layer, selectedRect));
  };

  const submit = () => {
    if (!region || !note.trim()) return;
    const layer = layerRef.current;
    const viewport = layer
      ? capturePreviewRegionViewport(layer.parentElement ?? layer, layer.getBoundingClientRect())
      : undefined;
    onAdd({ source, region, note: note.trim(), visibleText, viewport });
    clearSelection();
  };

  return (
    <div
      ref={layerRef}
      className='preview-region-comment-layer'
      data-preview-region-comment-ui
      data-testid='preview-region-comment-layer'
      aria-label={t('preview.regionComment.canvasLabel')}
      role='application'
      tabIndex={0}
      onPointerDown={(event) => {
        if (event.button !== 0 || region) return;
        event.preventDefault();
        event.currentTarget.setPointerCapture?.(event.pointerId);
        setDragStart({ x: event.clientX, y: event.clientY });
        setDragRegion(null);
        setVisibleText('');
      }}
      onPointerMove={(event) => {
        const layer = layerRef.current;
        if (!layer || !dragStart || region) return;
        const bounds = layer.getBoundingClientRect();
        setDragRegion(
          normalizePreviewRegion(
            dragStart,
            { x: event.clientX, y: event.clientY },
            { left: bounds.left, top: bounds.top, width: bounds.width, height: bounds.height },
            0
          )
        );
      }}
      onPointerUp={finishSelection}
      onPointerCancel={clearSelection}
    >
      <div className='preview-region-comment-hint' data-preview-region-comment-ui>
        <span>{t('preview.regionComment.dragHint')}</span>
        <button type='button' onClick={selectFullView} onPointerDown={(event) => event.stopPropagation()}>
          {t('preview.regionComment.selectFullView')}
        </button>
        <button type='button' onClick={onExit} onPointerDown={(event) => event.stopPropagation()}>
          {t('preview.regionComment.done')}
        </button>
      </div>

      {region || dragRegion ? (
        <>
          <div
            className='preview-region-comment-selection'
            style={previewRegionToStyle(region ?? dragRegion!)}
            aria-hidden='true'
          >
            {region ? <span>1</span> : null}
          </div>
          {region ? (
            <div
              className='preview-region-comment-composer'
              data-preview-region-comment-ui
              role='dialog'
              aria-label={t('preview.regionComment.composerLabel')}
              onPointerDown={(event) => event.stopPropagation()}
            >
              <div className='preview-region-comment-composer__source' title={source.fileName}>
                {source.fileName}
              </div>
              <textarea
                ref={noteRef}
                aria-label={t('preview.regionComment.noteLabel')}
                placeholder={t('preview.regionComment.placeholder')}
                value={note}
                maxLength={PREVIEW_REGION_COMMENT_MAX_LENGTH}
                onChange={(event) => setNote(event.target.value)}
                onKeyDown={(event) => {
                  if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') {
                    event.preventDefault();
                    submit();
                  }
                }}
              />
              <div className='preview-region-comment-composer__footer'>
                <span>{t('preview.regionComment.shortcut')}</span>
                <div>
                  <button type='button' onClick={clearSelection}>
                    {t('common.cancel')}
                  </button>
                  <button type='button' className='is-primary' disabled={!note.trim()} onClick={submit}>
                    {t('preview.regionComment.addToConversation')}
                  </button>
                </div>
              </div>
            </div>
          ) : null}
        </>
      ) : null}
    </div>
  );
};

export default PreviewRegionCommentLayer;

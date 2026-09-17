import type { SynonBiomedArtifactAnnotation } from '@/renderer/services/synonBiomedAnnotations';
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { clampPercent, type SynonBiomedArtifactPointSelection } from './artifactCanvasSelection';

export const SynonBiomedImageArtifactViewer: React.FC<{
  filename: string;
  contentUrl: string;
  annotations?: SynonBiomedArtifactAnnotation[];
  onSelectionChange: (selection: SynonBiomedArtifactPointSelection | null) => void;
  onAnnotationClick?: (annotation: SynonBiomedArtifactAnnotation) => void;
}> = ({ filename, contentUrl, annotations = [], onSelectionChange, onAnnotationClick }) => {
  const { t } = useTranslation();
  const imageRef = useRef<HTMLImageElement>(null);
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    setLoaded(false);
    onSelectionChange(null);
  }, [contentUrl, onSelectionChange]);

  const selectPoint = useCallback(
    (event: React.MouseEvent<HTMLImageElement>) => {
      const rect = event.currentTarget.getBoundingClientRect();
      if (rect.width <= 0 || rect.height <= 0) return;
      const xPercent = clampPercent(((event.clientX - rect.left) / rect.width) * 100);
      const yPercent = clampPercent(((event.clientY - rect.top) / rect.height) * 100);
      onSelectionChange({
        type: 'point',
        text: t('preview.artifactAnnotations.imagePosition', {
          x: xPercent.toFixed(1),
          y: yPercent.toFixed(1),
        }),
        x: event.clientX,
        y: event.clientY,
        xPercent,
        yPercent,
        pageNumber: null,
      });
    },
    [onSelectionChange, t]
  );

  return (
    <div className='size-full min-h-360px overflow-auto p-16px flex-center bg-fill-1'>
      <div className='relative max-w-full max-h-full leading-0' data-testid='artifact-image-canvas'>
        <img
          ref={imageRef}
          src={contentUrl}
          alt={filename}
          draggable={false}
          className='block max-w-full max-h-[calc(100vh-140px)] object-contain cursor-crosshair select-none'
          onLoad={() => setLoaded(true)}
          onClick={selectPoint}
        />
        {loaded &&
          annotations
            .filter(
              (annotation) => annotation.type === 'point' && annotation.xPercent != null && annotation.yPercent != null
            )
            .map((annotation) => (
              <button
                type='button'
                key={annotation.id}
                className='absolute size-20px -translate-x-1/2 -translate-y-1/2 rounded-full border-2 border-solid border-white bg-[rgb(var(--primary-6))] text-10px leading-16px font-[700] text-white shadow-md hover:scale-110'
                style={{ left: `${annotation.xPercent}%`, top: `${annotation.yPercent}%` }}
                title={annotation.text}
                aria-label={t('preview.artifactAnnotations.viewNamed', { label: annotation.label })}
                onClick={(event) => {
                  event.stopPropagation();
                  onAnnotationClick?.(annotation);
                }}
              >
                {annotation.label || '•'}
              </button>
            ))}
      </div>
    </div>
  );
};

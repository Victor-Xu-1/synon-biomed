import React from 'react';
import { useTranslation } from 'react-i18next';

type PreviewLoadingStateProps = {
  label?: string;
  progress?: number | null;
};

const PreviewLoadingState: React.FC<PreviewLoadingStateProps> = ({ label, progress = null }) => {
  const { t } = useTranslation();
  const normalizedProgress = progress == null ? null : Math.min(100, Math.max(0, progress));

  return (
    <div
      className='size-full min-h-120px flex flex-col items-center justify-center gap-10px bg-1 text-t-secondary'
      role='status'
      aria-live='polite'
      data-preview-loading='true'
    >
      <div className='h-2px w-112px overflow-hidden rounded-1px bg-fill-3' aria-hidden='true'>
        <div
          className={`h-full bg-t-primary ${normalizedProgress == null ? 'w-2/5 animate-pulse' : ''}`}
          style={normalizedProgress == null ? undefined : { width: `${normalizedProgress}%` }}
        />
      </div>
      <span className='text-12px leading-18px'>{label ?? t('preview.loadingDefault')}</span>
      {normalizedProgress != null && (
        <span className='text-11px tabular-nums text-t-tertiary'>{normalizedProgress}%</span>
      )}
    </div>
  );
};

export default PreviewLoadingState;

import React, { createContext } from 'react';
import { useTranslation } from 'react-i18next';
import type { TranscriptActivityState } from '../transcriptActivityModel';

export const TranscriptActivityContext = createContext<TranscriptActivityState>(null);

const TranscriptActivity: React.FC<{ activity: TranscriptActivityState }> = ({ activity }) => {
  const { t } = useTranslation();
  if (!activity?.spinning) return null;
  return (
    <span
      className='transcript-activity-inline'
      data-testid='transcript-activity'
      data-activity-state={activity.spinning ? 'active' : 'waiting'}
      role='status'
      aria-live='polite'
      aria-atomic='true'
      aria-label={t(activity.labelKey)}
      title={t(activity.labelKey)}
    >
      <span
        className='transcript-boundary-indicator__spinner'
        data-testid='transcript-activity-spinner'
        aria-hidden='true'
      />
    </span>
  );
};

export default React.memo(TranscriptActivity);

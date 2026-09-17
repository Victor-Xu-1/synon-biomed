import classNames from 'classnames';
import React from 'react';
import { useTranslation } from 'react-i18next';

type ConversationLoadingSurfaceProps = {
  testId?: string;
  className?: string;
};

const ConversationLoadingSurface: React.FC<ConversationLoadingSurfaceProps> = ({
  testId = 'conversation-loading-surface',
  className,
}) => {
  const { t } = useTranslation();
  return (
    <div
      className={classNames('relative flex-1 h-full flex items-center justify-center', className)}
      data-testid={testId}
      aria-busy='true'
      role='status'
      aria-label={t('conversation.loading')}
    >
      <span
        data-testid='conversation-loading-indicator'
        className='block h-18px w-18px animate-spin rounded-full border-2px border-solid border-fill-3 border-t-primary'
        aria-hidden='true'
      />
    </div>
  );
};

export default ConversationLoadingSurface;

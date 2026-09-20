import { Spin } from '@arco-design/web-react';
import React from 'react';
import { useTranslation } from 'react-i18next';

export function StorageLoading({ scan = false }: { scan?: boolean }) {
  const { t } = useTranslation();
  return (
    <div className='storage-loading' role='status'>
      <Spin size={18} />
      <span>{t(scan ? 'settings.storageSettings.scanHint' : 'common.loading')}</span>
    </div>
  );
}

export function StorageError({ onRetry, retained = false }: { onRetry: () => void; retained?: boolean }) {
  const { t } = useTranslation();
  return (
    <div className='storage-feedback storage-feedback--error' role='alert'>
      <span>
        {t(retained ? 'settings.storageSettings.refreshFailedRetained' : 'settings.storageSettings.loadFailed')}
      </span>
      <button type='button' className='settings-action-button' onClick={onRetry}>
        {t('common.retry')}
      </button>
    </div>
  );
}

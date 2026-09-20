import { Message } from '@arco-design/web-react';
import { Copy } from '@icon-park/react';
import React from 'react';
import { useTranslation } from 'react-i18next';

export function StoragePath({ value, label }: { value: string; label: string }) {
  const { t } = useTranslation();
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      Message.success(t('settings.storageSettings.pathCopied'));
    } catch {
      Message.error(t('settings.storageSettings.pathCopyFailed'));
    }
  };
  return (
    <div className='storage-path'>
      <code aria-label={label}>{value}</code>
      <button
        type='button'
        className='storage-icon-button'
        aria-label={`${t('settings.storageSettings.copyPath')}: ${label}`}
        title={t('settings.storageSettings.copyPath')}
        onClick={() => void copy()}
      >
        <Copy size={16} />
      </button>
    </div>
  );
}

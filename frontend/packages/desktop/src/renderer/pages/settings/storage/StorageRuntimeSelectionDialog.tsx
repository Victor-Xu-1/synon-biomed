import { Checkbox, Modal } from '@arco-design/web-react';
import React, { useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  saveScientificRuntimeSelection,
  type ScientificRuntimeOption,
} from '@/renderer/services/scientificRuntimeSettings';
import { scientificRuntimePresentation } from '@/renderer/utils/scientificRuntimePresentation';

export function StorageRuntimeSelectionDialog({
  items,
  onClose,
  onSaved,
}: {
  items: ScientificRuntimeOption[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useTranslation();
  const [selected, setSelected] = useState(Object.fromEntries(items.map((item) => [item.id, item.selected])));
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState(false);
  const pending = useRef(false);
  const save = async () => {
    if (pending.current) return;
    pending.current = true;
    setBusy(true);
    setFailed(false);
    try {
      await saveScientificRuntimeSelection(selected);
      onSaved();
    } catch {
      setFailed(true);
    } finally {
      pending.current = false;
      setBusy(false);
    }
  };
  return (
    <Modal
      visible
      title={t('settings.storageSettings.scientificTools')}
      onOk={() => void save()}
      onCancel={() => {
        if (!pending.current) onClose();
      }}
      confirmLoading={busy}
      okButtonProps={{ disabled: busy }}
      closable={!busy}
      maskClosable={!busy}
      escToExit={!busy}
      okText={t('settings.storageSettings.saveAndPrepare')}
      style={{ width: 'calc(100vw - 32px)', maxWidth: 640 }}
    >
      <p className='storage-dialog-notice'>{t('settings.storageSettings.softwareSelectionNotice')}</p>
      <div className='storage-runtime-selection'>
        {items.map((item) => {
          const presentation = scientificRuntimePresentation(item.id, t);
          return (
            <label key={item.id}>
              <Checkbox
                checked={selected[item.id]}
                disabled={busy || item.required || !item.available}
                onChange={(checked) => setSelected((current) => ({ ...current, [item.id]: checked }))}
                aria-label={presentation.title}
              />
              <span>
                <strong>{presentation.title}</strong>
                <small>{presentation.description}</small>
                {item.required && <small>{t('settings.environments.required')}</small>}
              </span>
              <small>
                {item.required
                  ? t('settings.environments.included')
                  : t('settings.storageSettings.estimatedSoftwareSize', { value: item.estimatedInstallMB })}
              </small>
            </label>
          );
        })}
      </div>
      {failed && (
        <p className='storage-feedback storage-feedback--error' role='alert'>
          {t('settings.storageSettings.softwareSaveFailed')}
        </p>
      )}
    </Modal>
  );
}

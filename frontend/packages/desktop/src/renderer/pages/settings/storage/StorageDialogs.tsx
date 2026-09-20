import { Checkbox, Input, Message, Modal } from '@arco-design/web-react';
import React, { useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ipcBridge } from '@/common';
import {
  changeSynonBiomedDataDirectory,
  clearSynonBiomedLastDataDirectoryMove,
  loadSynonBiomedDataDirectory,
  saveSynonBiomedStorageRules,
  SynonBiomedSettingsRequestError,
  type SynonBiomedDataDirectory,
  type SynonBiomedStorageRules,
} from '@/renderer/services/synonBiomedWorkspaceSettings';
import { StorageError, StorageLoading } from './StorageFeedback';
import { directoryChangeAllowed, formatStorageBytes, storageRuleRows } from './storagePresentation';
import { useStorageResource } from './useStorageResource';

function useStorageMutation() {
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState(false);
  const [errorKey, setErrorKey] = useState<string | null>(null);
  const pending = useRef(false);
  const run = async (action: () => Promise<void>) => {
    if (pending.current) return;
    pending.current = true;
    setBusy(true);
    setFailed(false);
    setErrorKey(null);
    try {
      await action();
    } catch (error) {
      setFailed(true);
      const keyByStatus: Record<number, string> = {
        400: 'settings.storageSettings.invalidTarget',
        401: 'settings.storageSettings.signInAgain',
        403: 'settings.storageSettings.ownerRequired',
        409: 'settings.storageSettings.stateConflict',
        507: 'settings.storageSettings.insufficientSpace',
      };
      setErrorKey(error instanceof SynonBiomedSettingsRequestError ? (keyByStatus[error.status] ?? null) : null);
    } finally {
      pending.current = false;
      setBusy(false);
    }
  };
  return { busy, failed, errorKey, run, canClose: () => !pending.current };
}

const loadEstimate = (signal: AbortSignal) => loadSynonBiomedDataDirectory({ signal });

export function StorageLocationDialog({
  directory,
  onClose,
  onSaved,
  beforeSave,
}: {
  directory: SynonBiomedDataDirectory;
  onClose: () => void;
  onSaved: () => void;
  beforeSave: () => void;
}) {
  const { t } = useTranslation();
  const estimate = useStorageResource(loadEstimate);
  const [path, setPath] = useState(directory.current);
  const [migrate, setMigrate] = useState(true);
  const mutation = useStorageMutation();
  const changed = path.trim() !== '' && path.trim() !== directory.current;
  const canSave =
    changed &&
    directoryChangeAllowed(estimate.data ?? directory) &&
    (!migrate || (!estimate.loading && !estimate.failed && estimate.data?.usageBytes != null));
  const choose = async () => {
    try {
      const selected = await ipcBridge.dialog.showOpen.invoke({ properties: ['openDirectory', 'createDirectory'] });
      if (selected?.[0]) setPath(selected[0]);
    } catch {
      Message.error(t('settings.storageSettings.locationPickerFailed'));
    }
  };
  const save = () => {
    if (!canSave) return;
    void mutation.run(async () => {
      beforeSave();
      const result = await changeSynonBiomedDataDirectory({ path: path.trim(), migrate });
      Message.success(
        t(
          result.restarting
            ? 'settings.storageSettings.locationRestarting'
            : result.restartRequired
              ? 'settings.storageSettings.locationRestartRequired'
              : 'settings.storageSettings.locationUpdated'
        )
      );
      onSaved();
    });
  };
  return (
    <Modal
      visible
      title={t('settings.storageSettings.changeLocationTitle')}
      className='storage-edit-modal'
      onCancel={() => {
        if (mutation.canClose()) onClose();
      }}
      onOk={save}
      confirmLoading={mutation.busy}
      okButtonProps={{ disabled: !canSave || mutation.busy }}
      maskClosable={!mutation.busy}
      escToExit={!mutation.busy}
      closable={!mutation.busy}
      okText={t('settings.storageSettings.changeLocation')}
      unmountOnExit
      style={{ maxWidth: 600, width: 'calc(100vw - 32px)' }}
    >
      <p className='storage-dialog-notice'>{t('settings.storageSettings.locationChangeWarning')}</p>
      <label className='storage-field'>
        <span>{t('settings.storageSettings.newLocation')}</span>
        <div className='storage-path-input'>
          <Input
            value={path}
            onChange={setPath}
            disabled={mutation.busy}
            aria-label={t('settings.storageSettings.newLocation')}
            placeholder={t('settings.storageSettings.newLocationPlaceholder')}
          />
          <button
            type='button'
            className='settings-action-button'
            disabled={mutation.busy}
            onClick={() => void choose()}
          >
            {t('settings.storageSettings.chooseLocation')}
          </button>
        </div>
      </label>
      <Checkbox
        checked={migrate}
        disabled={mutation.busy}
        onChange={(value) => {
          setMigrate(value);
          if (value && !estimate.data) void estimate.refresh(false);
          if (!value) estimate.cancel();
        }}
      >
        {t('settings.storageSettings.migrateExisting', { value: formatStorageBytes(estimate.data?.usageBytes) })}
      </Checkbox>
      {migrate && estimate.loading && <StorageLoading scan />}
      {migrate && estimate.failed && <StorageError onRetry={() => void estimate.refresh()} />}
      {migrate && !estimate.loading && !estimate.failed && estimate.data?.usageBytes == null && (
        <p className='storage-feedback' role='alert'>
          {t('settings.storageSettings.estimateUnavailable')}
        </p>
      )}
      {!migrate && <p className='storage-feedback'>{t('settings.storageSettings.switchWithoutCopy')}</p>}
      {directory.source === 'flag' && (
        <p className='storage-feedback'>
          {t('settings.storageSettings.flagOverride', {
            path: directory.configPath || t('settings.storageSettings.configFile'),
          })}
        </p>
      )}
      <p className='storage-caption'>{t('settings.storageSettings.locationRequirements')}</p>
      {mutation.failed && (
        <p role='alert' className='storage-feedback storage-feedback--error'>
          {t(mutation.errorKey ?? 'settings.storageSettings.locationChangeFailed')}
        </p>
      )}
    </Modal>
  );
}

export function StorageRulesDialog({
  rules,
  onClose,
  onSaved,
  beforeSave,
}: {
  rules: SynonBiomedStorageRules;
  onClose: () => void;
  onSaved: (value: SynonBiomedStorageRules) => void;
  beforeSave: () => void;
}) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState({ ...rules.rules });
  const mutation = useStorageMutation();
  const valid = storageRuleRows.every(({ key }) => draft[key].trim() !== '');
  const save = () => {
    if (!valid) return;
    void mutation.run(async () => {
      beforeSave();
      const updated = await saveSynonBiomedStorageRules(draft);
      Message.success(t('settings.storageSettings.rulesUpdated'));
      onSaved(updated);
    });
  };
  return (
    <Modal
      visible
      title={t('settings.storageSettings.editRulesTitle')}
      className='storage-edit-modal'
      onCancel={() => {
        if (mutation.canClose()) onClose();
      }}
      onOk={save}
      confirmLoading={mutation.busy}
      okButtonProps={{ disabled: !valid || mutation.busy }}
      maskClosable={!mutation.busy}
      escToExit={!mutation.busy}
      closable={!mutation.busy}
      okText={t('common.save')}
      unmountOnExit
      style={{ maxWidth: 600, width: 'calc(100vw - 32px)' }}
    >
      <p className='storage-dialog-notice'>{t('settings.storageSettings.rulesRequirements')}</p>
      {storageRuleRows.map((row) => (
        <label className='storage-field' key={row.key}>
          <span>{t(row.label)}</span>
          <small>{t(row.description)}</small>
          <Input
            value={draft[row.key]}
            disabled={mutation.busy}
            onChange={(value) => setDraft((current) => ({ ...current, [row.key]: value }))}
            aria-label={t(row.label)}
            placeholder='folder/subfolder'
          />
        </label>
      ))}
      {mutation.failed && (
        <p role='alert' className='storage-feedback storage-feedback--error'>
          {t(mutation.errorKey ?? 'settings.storageSettings.rulesChangeFailed')}
        </p>
      )}
    </Modal>
  );
}

export function StorageMoveCompletionDialog({
  remove,
  source,
  onClose,
  onSaved,
}: {
  remove: boolean;
  source: string;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useTranslation();
  const mutation = useStorageMutation();
  return (
    <Modal
      visible
      title={t(
        remove ? 'settings.storageSettings.deleteOldLocationTitle' : 'settings.storageSettings.completeMoveTitle'
      )}
      onCancel={() => {
        if (mutation.canClose()) onClose();
      }}
      onOk={() =>
        void mutation.run(async () => {
          await clearSynonBiomedLastDataDirectoryMove(remove);
          Message.success(t('settings.storageSettings.moveCompleted'));
          onSaved();
        })
      }
      confirmLoading={mutation.busy}
      okButtonProps={{ status: remove ? 'danger' : undefined, disabled: mutation.busy }}
      maskClosable={!mutation.busy}
      escToExit={!mutation.busy}
      closable={!mutation.busy}
      okText={t(
        remove ? 'settings.storageSettings.deleteAndCompleteShort' : 'settings.storageSettings.keepAndCompleteShort'
      )}
      unmountOnExit
      style={{ maxWidth: 560, width: 'calc(100vw - 32px)' }}
    >
      <p>
        {t(remove ? 'settings.storageSettings.deleteOldLocationBody' : 'settings.storageSettings.completeMoveBody', {
          path: source,
        })}
      </p>
      {mutation.failed && (
        <p role='alert' className='storage-feedback storage-feedback--error'>
          {t(mutation.errorKey ?? 'settings.storageSettings.moveCompleteFailed')}
        </p>
      )}
    </Modal>
  );
}

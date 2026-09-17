import { Input, Modal } from '@arco-design/web-react';
import React, { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { SynonBiomedSecret, SynonBiomedSecretInput } from '@/renderer/services/synonBiomedWorkspaceSettings';
import {
  buildCredentialInput,
  initialCredentialValues,
  localizeCredentialValidationError,
  validateCredentialValues,
  type LocalizedCredentialEditorField,
  type LocalizedCredentialProviderDefinition,
} from '../models/credentialCatalog';

type Props = {
  visible: boolean;
  provider: LocalizedCredentialProviderDefinition | null;
  secret: SynonBiomedSecret | null;
  saving: boolean;
  onCancel: () => void;
  onSave: (input: SynonBiomedSecretInput) => Promise<void>;
};

const CredentialEditorModal: React.FC<Props> = ({ visible, provider, secret, saving, onCancel, onSave }) => {
  const { t } = useTranslation();
  const [values, setValues] = useState<Record<string, string>>({});
  const [error, setError] = useState('');

  useEffect(() => {
    if (!visible || !provider) return;
    setValues(initialCredentialValues(provider, secret));
    setError('');
  }, [provider, secret, visible]);

  const submit = async () => {
    if (!provider) return;
    const validationError = validateCredentialValues(provider, values, secret !== null);
    if (validationError) {
      setError(localizeCredentialValidationError(validationError, provider, t));
      return;
    }
    setError('');
    await onSave(buildCredentialInput(provider, values, secret !== null));
  };

  return (
    <Modal
      title={
        secret
          ? t('settings.credentialsSettings.editor.editTitle', {
              name: provider?.label ?? t('settings.credentialsSettings.credentialFallback'),
            })
          : t('settings.credentialsSettings.editor.connectTitle', {
              name: provider?.label ?? t('settings.credentialsSettings.credentialFallback'),
            })
      }
      visible={visible}
      onCancel={onCancel}
      onOk={() => void submit()}
      confirmLoading={saving}
      okText={secret ? t('settings.credentialsSettings.editor.saveChanges') : t('common.save')}
      unmountOnExit
      style={{ maxWidth: 620, width: 'calc(100vw - 32px)' }}
    >
      {provider ? (
        <div className='flex flex-col gap-14px' data-testid='credential-editor'>
          <div className='text-12px leading-5 text-t-secondary'>{provider.description}</div>
          {secret && (secret.valueConfigured || secret.credentialsConfigured) ? (
            <div className='px-10px py-8px border border-arco-2 bg-fill-1 text-11px leading-5 text-t-secondary'>
              {t('settings.credentialsSettings.editor.redactedHint')}
            </div>
          ) : null}
          {provider.fields.map((field) => (
            <CredentialField
              key={field.key}
              field={field}
              value={values[field.key] ?? ''}
              configured={
                secret !== null &&
                (field.kind === 'value'
                  ? secret.valueConfigured
                  : field.kind === 'credential' &&
                    field.key !== 'description' &&
                    (secret.credentialFields.includes(field.key) || secret.maskedFields.includes(field.key)))
              }
              onChange={(value) => {
                setValues((current) => ({ ...current, [field.key]: value }));
                setError('');
              }}
            />
          ))}
          {error ? (
            <div role='alert' className='text-12px text-danger-6'>
              {error}
            </div>
          ) : null}
        </div>
      ) : null}
    </Modal>
  );
};

const CredentialField: React.FC<{
  field: LocalizedCredentialEditorField;
  value: string;
  configured: boolean;
  onChange: (value: string) => void;
}> = ({ field, value, configured, onChange }) => {
  const { t } = useTranslation();
  const placeholder = configured ? t('settings.credentialsSettings.editor.configuredPlaceholder') : field.placeholder;
  const inputProps = {
    value,
    onChange,
    placeholder,
    'aria-label': field.label,
  };
  return (
    <label className='flex flex-col gap-6px'>
      <span className='text-12px font-600 text-t-primary'>
        {field.label}
        {field.required ? <span className='ml-3px text-danger-6'>*</span> : null}
      </span>
      {field.multiline ? (
        <Input.TextArea {...inputProps} autoSize={{ minRows: 4, maxRows: 8 }} />
      ) : field.secret ? (
        <Input.Password {...inputProps} autoComplete='new-password' />
      ) : (
        <Input {...inputProps} />
      )}
      {field.helper ? <span className='text-11px leading-4 text-t-tertiary'>{field.helper}</span> : null}
    </label>
  );
};

export default CredentialEditorModal;

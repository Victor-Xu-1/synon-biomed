import { Message, Modal, Select, Tag } from '@arco-design/web-react';
import { Delete, Edit } from '@icon-park/react';
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  createSynonBiomedSecret,
  deleteSynonBiomedSecret,
  loadSynonBiomedSecrets,
  updateSynonBiomedSecret,
  type SynonBiomedSecret,
  type SynonBiomedSecretInput,
} from '@/renderer/services/synonBiomedWorkspaceSettings';
import CredentialEditorModal from './components/CredentialEditorModal';
import SettingsPageHeader from './components/SettingsPageHeader';
import SettingsPageWrapper from './components/SettingsPageWrapper';
import { SettingsGeneratedIcon } from './components/SettingsGeneratedAsset';
import {
  CREDENTIAL_PROVIDERS,
  CUSTOM_CREDENTIAL_PROVIDER,
  credentialDisplayName,
  credentialProviderForSecret,
  isModelCredential,
  isCustomCredential,
  localizeCredentialProvider,
  type LocalizedCredentialProviderDefinition,
} from './models/credentialCatalog';
import { EmptyText, RefreshButton, SettingsSection } from './components/SettingsPrimitives';

type EditorState = { provider: LocalizedCredentialProviderDefinition; secret: SynonBiomedSecret | null } | null;

export const CredentialsSettingsContent: React.FC = () => {
  const { t, i18n } = useTranslation();
  const [secrets, setSecrets] = useState<SynonBiomedSecret[]>([]);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [filter, setFilter] = useState('all');
  const [editor, setEditor] = useState<EditorState>(null);
  const [deleteTarget, setDeleteTarget] = useState<SynonBiomedSecret | null>(null);

  const refresh = useCallback(async () => {
    setLoading(true);
    try {
      setSecrets(await loadSynonBiomedSecrets());
    } catch (error) {
      console.error('Failed to load credentials:', error);
      Message.error(t('settings.credentialsSettings.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [t]);
  useEffect(() => void refresh(), [refresh]);

  const providers = useMemo(
    () => CREDENTIAL_PROVIDERS.map((provider) => localizeCredentialProvider(provider, t)),
    [i18n.resolvedLanguage, t]
  );
  const customProvider = useMemo(
    () => localizeCredentialProvider(CUSTOM_CREDENTIAL_PROVIDER, t),
    [i18n.resolvedLanguage, t]
  );
  const visibleSecrets = useMemo(() => secrets.filter((secret) => !isModelCredential(secret)), [secrets]);
  const customSecrets = useMemo(() => visibleSecrets.filter(isCustomCredential), [visibleSecrets]);
  const serviceSecrets = useMemo(
    () =>
      new Map(
        providers.map((provider) => [
          provider.id,
          visibleSecrets.filter((secret) => credentialProviderForSecret(secret)?.id === provider.id),
        ])
      ),
    [providers, visibleSecrets]
  );
  const visibleCustom = filter === 'all' || filter === 'generic' ? customSecrets : [];
  const visibleProviders = providers.filter((provider) => filter === 'all' || filter === provider.id);
  const deleteProvider = deleteTarget
    ? isCustomCredential(deleteTarget)
      ? customProvider
      : (providers.find((provider) => provider.id === credentialProviderForSecret(deleteTarget)?.id) ?? null)
    : null;

  const save = async (input: SynonBiomedSecretInput) => {
    if (!editor) return;
    setSaving(true);
    try {
      if (editor.secret) await updateSynonBiomedSecret(editor.secret.id, input);
      else await createSynonBiomedSecret(input);
      Message.success(
        editor.secret ? t('settings.credentialsSettings.updated') : t('settings.credentialsSettings.saved')
      );
      setEditor(null);
      await refresh();
    } catch (error) {
      console.error('Failed to save credential:', error);
      Message.error(t('settings.credentialsSettings.saveFailed'));
      throw error;
    } finally {
      setSaving(false);
    }
  };

  const remove = async () => {
    if (!deleteTarget) return;
    setSaving(true);
    try {
      await deleteSynonBiomedSecret(deleteTarget.id);
      Message.success(t('settings.credentialsSettings.disconnected'));
      setDeleteTarget(null);
      await refresh();
    } catch (error) {
      console.error('Failed to delete credential:', error);
      Message.error(t('settings.credentialsSettings.deleteFailed'));
    } finally {
      setSaving(false);
    }
  };

  return (
    <SettingsPageWrapper>
      <div className='flex flex-col gap-16px' data-testid='synon-credentials-settings'>
        <SettingsPageHeader
          title={t('settings.credentialsSettings.title')}
          description={t('settings.credentialsSettings.description')}
          actions={<RefreshButton loading={loading} onClick={refresh} />}
        />
        <div className='flex flex-wrap items-center justify-between gap-10px'>
          <Select
            value={filter}
            onChange={setFilter}
            className='w-220px max-w-full'
            aria-label={t('settings.credentialsSettings.filter')}
          >
            <Select.Option value='all'>
              {t('settings.credentialsSettings.all', { count: visibleSecrets.length })}
            </Select.Option>
            <Select.Option value='generic'>
              {t('settings.credentialsSettings.customCount', { count: customSecrets.length })}
            </Select.Option>
            {providers.map((provider) => (
              <Select.Option key={provider.id} value={provider.id}>
                {provider.label} ({serviceSecrets.get(provider.id)?.length ?? 0})
              </Select.Option>
            ))}
          </Select>
          <button
            type='button'
            className='settings-action-button'
            onClick={() => setEditor({ provider: customProvider, secret: null })}
          >
            {t('settings.credentialsSettings.addCustom')}
          </button>
        </div>

        {filter === 'all' || filter === 'generic' ? (
          <SettingsSection
            title={t('settings.credentialsSettings.customTitle')}
            description={t('settings.credentialsSettings.customDescription')}
            icon='credentials'
          >
            {visibleCustom.map((secret) => (
              <CredentialRow
                key={secret.id}
                secret={secret}
                provider={customProvider}
                onEdit={() => setEditor({ provider: customProvider, secret })}
                onDelete={() => setDeleteTarget(secret)}
              />
            ))}
            {visibleCustom.length === 0 ? (
              <EmptyText>
                <SettingsGeneratedIcon id='credentials' className='settings-empty-artwork-icon' />
                {t('settings.credentialsSettings.customEmpty')}
              </EmptyText>
            ) : null}
          </SettingsSection>
        ) : null}

        {visibleProviders.length > 0 ? (
          <SettingsSection
            title={t('settings.credentialsSettings.servicesTitle')}
            description={t('settings.credentialsSettings.servicesDescription')}
            icon='credentials'
          >
            {visibleProviders.map((provider) => {
              const connected = serviceSecrets.get(provider.id) ?? [];
              const primary = connected[0] ?? null;
              return (
                <div
                  key={provider.id}
                  className='min-h-62px py-9px flex flex-wrap sm:flex-nowrap items-center gap-10px border-b border-arco-1 last:border-b-0'
                  data-testid={`credential-provider-${provider.id}`}
                >
                  <div className='min-w-0 flex-1'>
                    <div className='flex items-center gap-7px'>
                      <span className='text-13px font-600 text-t-primary'>{provider.label}</span>
                      <Tag size='small' color={primary ? 'green' : 'gray'}>
                        {primary
                          ? t('settings.credentialsSettings.connected')
                          : t('settings.credentialsSettings.notConnected')}
                      </Tag>
                    </div>
                    <div className='mt-3px text-11px text-t-tertiary'>
                      {primary
                        ? t('settings.credentialsSettings.serviceSummary', {
                            name: credentialDisplayName(primary, provider),
                            description: provider.description,
                          })
                        : provider.description}
                    </div>
                  </div>
                  <div className='flex items-center gap-6px'>
                    <button
                      type='button'
                      className='settings-action-button'
                      onClick={() => setEditor({ provider, secret: primary })}
                    >
                      {primary ? <Edit size='13' /> : null}
                      {primary ? t('common.edit') : t('settings.credentialsSettings.connect')}
                    </button>
                    {primary ? (
                      <button
                        type='button'
                        className='settings-text-danger-button'
                        onClick={() => setDeleteTarget(primary)}
                      >
                        {t('settings.credentialsSettings.disconnect')}
                      </button>
                    ) : null}
                  </div>
                </div>
              );
            })}
          </SettingsSection>
        ) : null}
      </div>

      <CredentialEditorModal
        visible={editor !== null}
        provider={editor?.provider ?? null}
        secret={editor?.secret ?? null}
        saving={saving}
        onCancel={() => setEditor(null)}
        onSave={save}
      />
      <Modal
        title={t('settings.credentialsSettings.disconnectTitle')}
        visible={deleteTarget !== null}
        onCancel={() => setDeleteTarget(null)}
        onOk={() => void remove()}
        confirmLoading={saving}
        okButtonProps={{ status: 'danger' }}
        okText={t('settings.credentialsSettings.disconnect')}
        unmountOnExit
      >
        {t('settings.credentialsSettings.disconnectBody', {
          name:
            deleteTarget && deleteProvider
              ? credentialDisplayName(deleteTarget, deleteProvider)
              : t('settings.credentialsSettings.credentialFallback'),
        })}
      </Modal>
    </SettingsPageWrapper>
  );
};

const CredentialRow: React.FC<{
  secret: SynonBiomedSecret;
  provider: LocalizedCredentialProviderDefinition;
  onEdit: () => void;
  onDelete: () => void;
}> = ({ secret, provider, onEdit, onDelete }) => {
  const { t } = useTranslation();
  return (
    <div className='flex items-center gap-12px min-h-54px py-8px border-b border-arco-1 last:border-b-0'>
      <div className='min-w-0 flex-1'>
        <div className='text-13px font-600 text-t-primary'>{secret.name || provider.label}</div>
        <div className='text-11px text-t-tertiary mt-2px'>
          {secret.description || t('settings.credentialsSettings.encryptedLocally')}
        </div>
      </div>
      <span className='text-11px text-t-tertiary'>
        {secret.maskedPreview || t('settings.credentialsSettings.configured')}
      </span>
      <button type='button' className='settings-icon-button' title={t('common.edit')} onClick={onEdit}>
        <Edit size='14' />
      </button>
      <button type='button' className='settings-icon-button' title={t('common.delete')} onClick={onDelete}>
        <Delete size='14' />
      </button>
    </div>
  );
};

const CredentialsSettings: React.FC = () => <CredentialsSettingsContent />;

export default CredentialsSettings;

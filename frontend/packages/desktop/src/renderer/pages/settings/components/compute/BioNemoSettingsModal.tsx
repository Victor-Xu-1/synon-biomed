import { Button, Input, Message, Modal, Radio, Spin, Tag } from '@arco-design/web-react';
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  loadSynonBiomedBioNemoSettings,
  setSynonBiomedBioNemoSettings,
  type SynonBiomedBioNemoSettings,
  type SynonBiomedComputeProvider,
} from '@/renderer/services/synonBiomedCompute';
import { loadSynonBiomedSecrets, type SynonBiomedSecret } from '@/renderer/services/synonBiomedWorkspaceSettings';

export const BioNemoSettingsModal: React.FC<{
  visible: boolean;
  providers: SynonBiomedComputeProvider[];
  onClose: () => void;
  onChanged: () => Promise<void>;
  onOpenCredentials: () => void;
  onOpenSkills: () => void;
}> = ({ visible, providers, onClose, onChanged, onOpenCredentials, onOpenSkills }) => {
  const { t } = useTranslation();
  const [settings, setSettings] = useState<SynonBiomedBioNemoSettings | null>(null);
  const [draft, setDraft] = useState<SynonBiomedBioNemoSettings | null>(null);
  const [secrets, setSecrets] = useState<SynonBiomedSecret[]>([]);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [loadFailed, setLoadFailed] = useState(false);

  const refresh = useCallback(async () => {
    setLoading(true);
    setLoadFailed(false);
    try {
      const [nextSettings, nextSecrets] = await Promise.all([
        loadSynonBiomedBioNemoSettings(),
        loadSynonBiomedSecrets().catch((): SynonBiomedSecret[] => []),
      ]);
      setSettings(nextSettings);
      setDraft(nextSettings);
      setSecrets(nextSecrets);
    } catch (loadError) {
      console.error('Failed to load BioNeMo settings:', loadError);
      setLoadFailed(true);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (visible) void refresh();
  }, [refresh, visible]);

  const registeredProviders = useMemo(
    () =>
      providers.filter(
        (provider) =>
          provider.family === 'infer' && (provider.managedFamily || provider.credentialName === 'NVIDIA_API_KEY')
      ),
    [providers]
  );
  const credential = secrets.find((secret) => secret.name === 'NVIDIA_API_KEY');

  const commit = async (enabled: boolean) => {
    if (!draft || saving) return;
    setSaving(true);
    try {
      const next = await setSynonBiomedBioNemoSettings({
        enabled,
        mode: draft.mode,
        hostedHost: draft.hostedHost.trim() || 'health.api.nvidia.com',
      });
      setSettings(next);
      setDraft(next);
      await onChanged();
      Message.success(
        enabled
          ? t('settings.computeWorkspace.bioNemo.connectedMessage')
          : t('settings.computeWorkspace.bioNemo.disconnectedMessage')
      );
      if (!enabled) onClose();
    } catch (saveError) {
      console.error('Failed to update BioNeMo settings:', saveError);
      Message.error(t('settings.computeWorkspace.bioNemo.updateFailed'));
    } finally {
      setSaving(false);
    }
  };

  const disconnect = () =>
    Modal.confirm({
      title: t('settings.computeWorkspace.bioNemo.disconnectTitle'),
      content: t('settings.computeWorkspace.bioNemo.disconnectBody'),
      okButtonProps: { status: 'danger' },
      onOk: () => commit(false),
    });

  const modalFooter = settings ? (
    <div className='flex flex-wrap justify-end gap-8px'>
      {settings.enabled ? (
        <Button status='danger' loading={saving} onClick={disconnect}>
          {t('settings.computeWorkspace.bioNemo.disconnect')}
        </Button>
      ) : null}
      <Button onClick={onClose}>{t('settings.computeWorkspace.bioNemo.close')}</Button>
      {!settings.enabled ? (
        <Button type='primary' loading={saving} onClick={() => void commit(true)}>
          {t('settings.computeWorkspace.bioNemo.connect')}
        </Button>
      ) : null}
    </div>
  ) : null;

  return (
    <Modal
      visible={visible}
      title='NVIDIA BioNeMo NIM'
      onCancel={onClose}
      footer={modalFooter}
      unmountOnExit
      style={{ width: 'min(680px, calc(100vw - 28px))' }}
    >
      {loading && !settings ? (
        <div className='h-260px flex items-center justify-center'>
          <Spin />
        </div>
      ) : loadFailed ? (
        <div role='alert' className='border border-red-2 bg-red-1 px-14px py-12px text-12px text-red-6'>
          {t('settings.computeWorkspace.bioNemo.loadFailed')}
        </div>
      ) : draft && settings ? (
        <div className='flex flex-col gap-18px'>
          <div className='flex flex-wrap items-center gap-8px'>
            <Tag color={draft.mode === 'hosted' ? 'arcoblue' : 'green'}>
              {draft.mode === 'hosted'
                ? t('settings.computeWorkspace.bioNemo.remote')
                : t('settings.computeWorkspace.bioNemo.local')}
            </Tag>
            {draft.mode === 'hosted' ? (
              <span className='text-12px text-t-tertiary'>
                {t('settings.computeWorkspace.bioNemo.via')} <code>{draft.hostedHost}</code>
              </span>
            ) : null}
            <Tag color={settings.enabled ? 'green' : 'gray'}>
              {settings.enabled
                ? t('settings.computeWorkspace.bioNemo.connected')
                : t('settings.computeWorkspace.bioNemo.notConnected')}
            </Tag>
          </div>

          <section>
            <h3 className='m-0 mb-8px text-13px font-650 text-t-primary'>
              {t('settings.computeWorkspace.bioNemo.runtimeMode')}
            </h3>
            <Radio.Group
              aria-label={t('settings.computeWorkspace.bioNemo.runtimeModeAria')}
              value={draft.mode}
              disabled={settings.enabled}
              onChange={(mode) =>
                setDraft((current) => (current ? { ...current, mode: mode === 'local' ? 'local' : 'hosted' } : current))
              }
            >
              <Radio value='hosted'>{t('settings.computeWorkspace.bioNemo.remote')}</Radio>
              <Radio value='local'>{t('settings.computeWorkspace.bioNemo.local')}</Radio>
            </Radio.Group>
            <p className='m-0 mt-6px text-11px leading-5 text-t-tertiary'>
              {settings.enabled
                ? t('settings.computeWorkspace.bioNemo.modeLocked')
                : t('settings.computeWorkspace.bioNemo.modeHint')}
            </p>
            {draft.mode === 'hosted' ? (
              <Input
                aria-label={t('settings.computeWorkspace.bioNemo.hostedHost')}
                className='mt-9px'
                value={draft.hostedHost}
                disabled={settings.enabled}
                placeholder='health.api.nvidia.com'
                onChange={(hostedHost) => setDraft((current) => (current ? { ...current, hostedHost } : current))}
              />
            ) : null}
          </section>

          <section>
            <h3 className='m-0 mb-8px text-13px font-650 text-t-primary'>
              {t('settings.computeWorkspace.bioNemo.familyCredential')}
            </h3>
            <div className='flex flex-wrap items-center gap-8px border border-arco-2 px-12px py-10px'>
              <code className='min-w-0 flex-1 text-12px'>NVIDIA_API_KEY</code>
              {credential?.maskedPreview ? (
                <code className='text-11px text-t-tertiary'>{credential.maskedPreview}</code>
              ) : null}
              <Tag size='small' color={credential ? 'green' : 'orange'}>
                {credential
                  ? t('settings.computeWorkspace.bioNemo.saved')
                  : t('settings.computeWorkspace.bioNemo.missing')}
              </Tag>
              <Button size='mini' onClick={onOpenCredentials}>
                {credential
                  ? t('settings.computeWorkspace.bioNemo.manage')
                  : t('settings.computeWorkspace.bioNemo.add')}
              </Button>
            </div>
          </section>

          <section>
            <h3 className='m-0 mb-8px text-13px font-650 text-t-primary'>
              {t('settings.computeWorkspace.bioNemo.registeredEndpoints')}
            </h3>
            <div className='flex flex-col border border-arco-2'>
              {registeredProviders.map((provider) => (
                <div
                  key={provider.name}
                  className='flex flex-wrap items-center gap-8px border-b border-arco-2 px-12px py-9px last:border-b-0'
                >
                  <code className='min-w-0 flex-1 text-12px'>{provider.displayName}</code>
                  <Tag size='small' color={provider.probeError ? 'red' : provider.checked ? 'green' : 'gray'}>
                    {provider.probeError
                      ? t('settings.computeWorkspace.bioNemo.probeFailed')
                      : provider.checked
                        ? t('settings.computeWorkspace.bioNemo.available')
                        : t('settings.computeWorkspace.bioNemo.notProbed')}
                  </Tag>
                  <Tag size='small' color='gray'>
                    {provider.location ?? draft.mode}
                  </Tag>
                </div>
              ))}
              {registeredProviders.length === 0 ? (
                <div className='px-12px py-11px text-12px text-t-tertiary'>
                  {t('settings.computeWorkspace.bioNemo.noEndpoints')}
                </div>
              ) : null}
            </div>
          </section>

          <section>
            <h3 className='m-0 mb-5px text-13px font-650 text-t-primary'>
              {t('settings.computeWorkspace.bioNemo.toolkitSkills')}
            </h3>
            <p className='m-0 mb-8px text-12px text-t-tertiary'>
              {t('settings.computeWorkspace.bioNemo.skillsDescription')}
            </p>
            <Button size='small' onClick={onOpenSkills}>
              {t('settings.computeWorkspace.bioNemo.openSkills')}
            </Button>
          </section>
        </div>
      ) : null}
    </Modal>
  );
};

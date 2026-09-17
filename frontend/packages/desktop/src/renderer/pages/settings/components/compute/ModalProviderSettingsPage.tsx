import { Button, Input, InputNumber, Message, Modal, Radio, Spin, Switch, Tag } from '@arco-design/web-react';
import { Refresh } from '@icon-park/react';
import React, { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  loadSynonBiomedModalSettings,
  saveSynonBiomedComputeProviderDetails,
  setSynonBiomedModalEnabled,
  type SynonBiomedModalEgressPolicy,
  type SynonBiomedModalSettings,
} from '@/renderer/services/synonBiomedCompute';

type ModalDraft = Pick<
  SynonBiomedModalSettings,
  'appName' | 'environmentName' | 'detailsMd' | 'egressPolicy' | 'maxConcurrentJobs' | 'maxTimeoutSec'
>;

const emptyDraft: ModalDraft = {
  appName: '',
  environmentName: '',
  detailsMd: '',
  egressPolicy: null,
  maxConcurrentJobs: null,
  maxTimeoutSec: null,
};

export const ModalProviderSettingsPage: React.FC<{
  visible: boolean;
  onClose: () => void;
  onOpenCredentials: () => void;
  onTry: (prompt: string) => void;
}> = ({ visible, onClose, onOpenCredentials, onTry }) => {
  const { t } = useTranslation();
  const [settings, setSettings] = useState<SynonBiomedModalSettings | null>(null);
  const [draft, setDraft] = useState<ModalDraft>(emptyDraft);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [loadFailed, setLoadFailed] = useState(false);

  const refresh = useCallback(async () => {
    setLoading(true);
    setLoadFailed(false);
    try {
      const next = await loadSynonBiomedModalSettings();
      setSettings(next);
      setDraft({
        appName: next.appName,
        environmentName: next.environmentName,
        detailsMd: next.detailsMd,
        egressPolicy: next.egressPolicy,
        maxConcurrentJobs: next.maxConcurrentJobs,
        maxTimeoutSec: next.maxTimeoutSec,
      });
    } catch (loadError) {
      console.error('Failed to load Modal settings:', loadError);
      setLoadFailed(true);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (visible) void refresh();
  }, [refresh, visible]);

  const toggleEnabled = (enabled: boolean) => {
    const commit = async () => {
      setSaving(true);
      try {
        await setSynonBiomedModalEnabled(enabled);
        await refresh();
        Message.success(
          enabled ? t('settings.computeWorkspace.modal.enabled') : t('settings.computeWorkspace.modal.disabled')
        );
      } catch (toggleError) {
        console.error('Failed to update Modal enabled state:', toggleError);
        Message.error(t('settings.computeWorkspace.modal.updateFailed'));
      } finally {
        setSaving(false);
      }
    };
    if (!enabled) {
      Modal.confirm({
        title: t('settings.computeWorkspace.modal.disableTitle'),
        content: t('settings.computeWorkspace.modal.disableBody'),
        okButtonProps: { status: 'danger' },
        onOk: commit,
      });
      return;
    }
    void commit();
  };

  const save = async () => {
    if (!settings || saving) return;
    setSaving(true);
    try {
      await saveSynonBiomedComputeProviderDetails('byoc:modal', {
        appName: draft.appName.trim() || null,
        environmentName: draft.environmentName.trim() || null,
        detailsMd: draft.detailsMd.trim(),
        egressPolicy: normalizeEgressPolicy(draft.egressPolicy),
        maxConcurrentJobs: draft.maxConcurrentJobs,
        maxTimeoutSec: draft.maxTimeoutSec,
      });
      await refresh();
      Message.success(t('settings.computeWorkspace.modal.saved'));
    } catch (saveError) {
      console.error('Failed to save Modal settings:', saveError);
      Message.error(t('settings.computeWorkspace.modal.saveFailed'));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      visible={visible}
      title='Modal'
      onCancel={onClose}
      footer={
        <div className='flex justify-end gap-8px'>
          <Button onClick={onClose}>{t('common.cancel')}</Button>
          <Button type='primary' loading={saving} disabled={!settings?.enabled} onClick={() => void save()}>
            {t('common.save')}
          </Button>
        </div>
      }
      unmountOnExit
      style={{ width: 'min(720px, calc(100vw - 32px))' }}
    >
      <div className='max-h-[min(72vh,760px)] overflow-y-auto pr-4px' data-testid='modal-provider-settings'>
        <header className='mb-20px'>
          <div className='flex flex-wrap items-start gap-8px'>
            <div className='min-w-0 flex-1'>
              <p className='m-0 text-13px leading-6 text-t-secondary'>
                {t('settings.computeWorkspace.modal.description')}
              </p>
              <div className='mt-8px flex flex-wrap gap-8px text-12px'>
                <a href='https://modal.com/' target='_blank' rel='noreferrer' className='text-link-6'>
                  modal.com
                </a>
                <a href='https://modal.com/docs' target='_blank' rel='noreferrer' className='text-link-6'>
                  {t('settings.computeWorkspace.modal.documentation')}
                </a>
              </div>
            </div>
            <Button
              type='secondary'
              size='small'
              icon={<Refresh size={14} />}
              loading={loading}
              onClick={() => void refresh()}
            >
              {t('settings.computeWorkspace.modal.recheck')}
            </Button>
            <Switch
              aria-label={t('settings.computeWorkspace.modal.enableAria')}
              checked={settings?.enabled === true}
              loading={saving}
              disabled={!settings}
              onChange={toggleEnabled}
            />
          </div>
        </header>

        {loading && !settings ? (
          <div className='h-260px flex items-center justify-center'>
            <Spin />
          </div>
        ) : loadFailed ? (
          <div role='alert' className='border border-red-2 bg-red-1 px-14px py-12px text-12px text-red-6'>
            {t('settings.computeWorkspace.modal.loadFailed')}
          </div>
        ) : settings ? (
          <div className='flex flex-col gap-22px'>
            <Section
              title={t('settings.computeWorkspace.modal.tryTitle')}
              description={
                settings.enabled
                  ? t('settings.computeWorkspace.modal.tryEnabledDescription')
                  : t('settings.computeWorkspace.modal.tryDisabledDescription')
              }
            >
              <div className='grid grid-cols-1 gap-8px sm:grid-cols-2'>
                <TryCard
                  title={t('settings.computeWorkspace.modal.gpuWorkspaceTitle')}
                  description={t('settings.computeWorkspace.modal.gpuWorkspaceDescription')}
                  disabled={!settings.enabled}
                  onClick={() => onTry('Set up a GPU workspace on Modal for my current research project.')}
                />
                <TryCard
                  title={t('settings.computeWorkspace.modal.predictGfpTitle')}
                  description={t('settings.computeWorkspace.modal.predictGfpDescription')}
                  disabled={!settings.enabled}
                  onClick={() => onTry('Predict the 3D structure of GFP using Modal GPU compute.')}
                />
              </div>
            </Section>

            <Section
              title={t('settings.computeWorkspace.modal.defaultAppTitle')}
              description={t('settings.computeWorkspace.modal.defaultAppDescription')}
            >
              <Input
                aria-label={t('settings.computeWorkspace.modal.defaultAppTitle')}
                value={draft.appName}
                disabled={!settings.enabled}
                placeholder='synonbiomed-...'
                onChange={(appName) => setDraft((current) => ({ ...current, appName }))}
              />
            </Section>

            <Section
              title={t('settings.computeWorkspace.modal.environmentTitle')}
              description={t('settings.computeWorkspace.modal.environmentDescription')}
            >
              <Input
                aria-label={t('settings.computeWorkspace.modal.environmentTitle')}
                value={draft.environmentName}
                disabled={!settings.enabled}
                placeholder={t('settings.computeWorkspace.modal.environmentPlaceholder')}
                onChange={(environmentName) => setDraft((current) => ({ ...current, environmentName }))}
              />
            </Section>

            <Section
              title={t('settings.computeWorkspace.modal.networkTitle')}
              description={t('settings.computeWorkspace.modal.networkDescription')}
            >
              <Radio.Group
                aria-label={t('settings.computeWorkspace.modal.networkTitle')}
                direction='vertical'
                value={draft.egressPolicy?.mode ?? 'unrestricted'}
                disabled={!settings.enabled}
                onChange={(mode) =>
                  setDraft((current) => ({
                    ...current,
                    egressPolicy: buildEgressPolicy(String(mode), current.egressPolicy),
                  }))
                }
              >
                <Radio value='unrestricted'>{t('settings.computeWorkspace.modal.unrestricted')}</Radio>
                <Radio value='allowlist'>{t('settings.computeWorkspace.modal.allowlist')}</Radio>
                <Radio value='blocked'>{t('settings.computeWorkspace.modal.blocked')}</Radio>
              </Radio.Group>
              {draft.egressPolicy?.mode === 'allowlist' ? (
                <div className='mt-10px flex flex-col gap-8px'>
                  <Input.TextArea
                    aria-label={t('settings.computeWorkspace.modal.allowedDomains')}
                    disabled={!settings.enabled}
                    value={(draft.egressPolicy.additional ?? []).join('\n')}
                    placeholder={'api.example.com\n*.data.example.com'}
                    autoSize={{ minRows: 3, maxRows: 7 }}
                    onChange={(value) =>
                      setDraft((current) => ({
                        ...current,
                        egressPolicy: {
                          mode: 'allowlist',
                          mirror: current.egressPolicy?.mirror ?? true,
                          additional: splitLines(value),
                        },
                      }))
                    }
                  />
                  <Switch
                    aria-label={t('settings.computeWorkspace.modal.mergeAllowlist')}
                    checked={draft.egressPolicy.mirror !== false}
                    disabled={!settings.enabled}
                    checkedText={t('settings.computeWorkspace.modal.mergeEnvironment')}
                    uncheckedText={t('settings.computeWorkspace.modal.customOnly')}
                    onChange={(mirror) =>
                      setDraft((current) => ({
                        ...current,
                        egressPolicy: { mode: 'allowlist', mirror, additional: current.egressPolicy?.additional ?? [] },
                      }))
                    }
                  />
                </div>
              ) : null}
            </Section>

            <Section
              title={t('settings.computeWorkspace.modal.credentialsTitle')}
              description={t('settings.computeWorkspace.modal.credentialsDescription')}
            >
              <div className='flex flex-col gap-8px'>
                {settings.profiles.map((profile) => (
                  <div
                    key={profile.name}
                    className='flex flex-wrap items-center gap-8px border-b border-arco-2 py-8px last:border-b-0'
                  >
                    <span className='min-w-0 flex-1 text-13px text-t-primary'>{profile.name}</span>
                    {profile.tokenIdMasked ? (
                      <code className='text-11px text-t-tertiary'>{profile.tokenIdMasked}</code>
                    ) : null}
                    <Tag size='small' color={profile.active ? 'green' : 'gray'}>
                      {profile.active
                        ? t('settings.computeWorkspace.modal.active')
                        : t('settings.computeWorkspace.modal.inactive')}
                    </Tag>
                  </div>
                ))}
                {settings.profiles.length === 0 ? (
                  <div
                    role='alert'
                    className='border border-orange-2 bg-orange-1 px-12px py-10px text-12px text-orange-7'
                  >
                    {t('settings.computeWorkspace.modal.credentialsMissing')}
                  </div>
                ) : null}
                <Button size='small' className='self-start' onClick={onOpenCredentials}>
                  {t('settings.computeWorkspace.modal.openCredentials')}
                </Button>
              </div>
            </Section>

            <Section title={t('settings.computeWorkspace.modal.costAndRuntime')}>
              <div className='grid grid-cols-1 gap-12px sm:grid-cols-2'>
                <NumberField
                  label={t('settings.computeWorkspace.modal.concurrentJobs')}
                  unit={t('settings.computeWorkspace.modal.jobsUnit')}
                >
                  <InputNumber
                    aria-label={t('settings.computeWorkspace.modal.concurrentJobs')}
                    min={1}
                    max={1024}
                    disabled={!settings.enabled}
                    value={draft.maxConcurrentJobs ?? undefined}
                    placeholder='10'
                    onChange={(value) =>
                      setDraft((current) => ({ ...current, maxConcurrentJobs: nullableNumber(value) }))
                    }
                  />
                </NumberField>
                <NumberField
                  label={t('settings.computeWorkspace.modal.defaultTimeout')}
                  unit={t('settings.computeWorkspace.modal.hoursUnit')}
                >
                  <InputNumber
                    aria-label={t('settings.computeWorkspace.modal.defaultTimeout')}
                    min={1}
                    max={23}
                    disabled={!settings.enabled}
                    value={draft.maxTimeoutSec == null ? undefined : Math.round(draft.maxTimeoutSec / 3600)}
                    placeholder='12'
                    onChange={(value) => {
                      const hours = nullableNumber(value);
                      setDraft((current) => ({ ...current, maxTimeoutSec: hours == null ? null : hours * 3600 }));
                    }}
                  />
                </NumberField>
              </div>
            </Section>

            <Section
              title={t('settings.computeWorkspace.modal.detailsTitle')}
              description={t('settings.computeWorkspace.modal.detailsDescription')}
            >
              <Input.TextArea
                aria-label={t('settings.computeWorkspace.modal.detailsTitle')}
                value={draft.detailsMd}
                disabled={!settings.enabled}
                maxLength={32768}
                showWordLimit
                autoSize={{ minRows: 5, maxRows: 12 }}
                onChange={(detailsMd) => setDraft((current) => ({ ...current, detailsMd }))}
              />
            </Section>
          </div>
        ) : null}
      </div>
    </Modal>
  );
};

const Section: React.FC<{ title: string; description?: string; children: React.ReactNode }> = ({
  title,
  description,
  children,
}) => (
  <section>
    <h2 className='m-0 text-15px font-650 text-t-primary'>{title}</h2>
    {description ? (
      <p className='m-0 mt-4px mb-10px text-12px leading-5 text-t-tertiary'>{description}</p>
    ) : (
      <div className='h-10px' />
    )}
    {children}
  </section>
);

const TryCard: React.FC<{ title: string; description: string; disabled: boolean; onClick: () => void }> = ({
  title,
  description,
  disabled,
  onClick,
}) => (
  <button
    type='button'
    disabled={disabled}
    className='min-h-82px border border-arco-2 bg-2 px-13px py-11px text-left disabled:opacity-50'
    onClick={onClick}
  >
    <div className='text-13px font-600 text-t-primary'>$ {title}</div>
    <p className='m-0 mt-5px text-11px leading-5 text-t-tertiary'>{description}</p>
  </button>
);

const NumberField: React.FC<{ label: string; unit: string; children: React.ReactNode }> = ({
  label,
  unit,
  children,
}) => (
  <label className='flex flex-col gap-5px text-12px text-t-secondary'>
    <span>{label}</span>
    <div className='flex items-center gap-8px'>
      {children}
      <span className='shrink-0 text-t-tertiary'>{unit}</span>
    </div>
  </label>
);

function buildEgressPolicy(mode: string, current: SynonBiomedModalEgressPolicy | null): SynonBiomedModalEgressPolicy {
  if (mode === 'allowlist') return { mode, mirror: current?.mirror ?? true, additional: current?.additional ?? [] };
  return { mode: mode === 'blocked' ? 'blocked' : 'unrestricted' };
}

function normalizeEgressPolicy(policy: SynonBiomedModalEgressPolicy | null): SynonBiomedModalEgressPolicy {
  return policy ?? { mode: 'unrestricted' };
}

function splitLines(value: string): string[] {
  return value
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean);
}

function nullableNumber(value: string | number | undefined): number | null {
  return value === undefined || value === '' ? null : Number(value);
}

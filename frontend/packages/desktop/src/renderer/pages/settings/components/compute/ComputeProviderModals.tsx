import { Button, Input, InputNumber, Message, Modal, Select, Spin } from '@arco-design/web-react';
import React, { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  addSynonBiomedInferenceProvider,
  addSynonBiomedSshHost,
  loadSynonBiomedComputeProvider,
  probeSynonBiomedComputeProvider,
  saveSynonBiomedComputeProviderDetails,
  type SynonBiomedComputeProvider,
  type SynonBiomedComputeProviderDetailsInput,
  type SynonBiomedInferenceProviderInput,
  type SynonBiomedSshAliasesSnapshot,
  type SynonBiomedSshHostInput,
} from '@/renderer/services/synonBiomedCompute';

export const EditProviderModal: React.FC<{
  provider: SynonBiomedComputeProvider | null;
  onClose: () => void;
  onSaved: () => Promise<void>;
}> = ({ provider, onClose, onSaved }) => {
  const { t } = useTranslation();
  const supportsWorkspaceSettings = provider?.family === 'ssh' || provider?.family === 'byoc';
  const [draft, setDraft] = useState<SynonBiomedComputeProviderDetailsInput>({
    detailsMd: '',
    scratchRoot: null,
    dataRoots: [],
    maxConcurrentJobs: null,
  });
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!provider) return;
    let alive = true;
    setLoading(true);
    void loadSynonBiomedComputeProvider(provider.name)
      .then((details) => {
        if (!alive) return;
        setDraft({
          detailsMd: details.detailsMd ?? '',
          scratchRoot: details.scratchRoot ?? null,
          dataRoots: details.dataRoots,
          maxConcurrentJobs: details.maxConcurrentJobs ?? null,
        });
      })
      .catch((error) => {
        console.error('Failed to load Synon Biomed compute provider details:', error);
        Message.error(t('settings.computeWorkspace.providerDetailsLoadFailed'));
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [provider, t]);

  const submit = async () => {
    if (!provider || saving) return;
    setSaving(true);
    try {
      await saveSynonBiomedComputeProviderDetails(provider.name, {
        detailsMd: draft.detailsMd?.trim() ?? '',
        ...(supportsWorkspaceSettings
          ? {
              scratchRoot: draft.scratchRoot?.trim() || null,
              dataRoots: draft.dataRoots ?? [],
              maxConcurrentJobs: draft.maxConcurrentJobs ?? null,
            }
          : {}),
      });
      await onSaved();
    } catch (error) {
      console.error('Failed to save Synon Biomed compute provider details:', error);
      Message.error(t('settings.computeWorkspace.providerDetailsSaveFailed'));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      visible={Boolean(provider)}
      title={t('settings.computeEdit')}
      onCancel={onClose}
      onOk={() => void submit()}
      okText={t('common.save')}
      cancelText={t('common.cancel')}
      okButtonProps={{ loading: saving, disabled: loading }}
      unmountOnExit
      style={{ width: 'min(680px, 92vw)' }}
    >
      {loading ? (
        <div className='h-180px flex items-center justify-center'>
          <Spin />
        </div>
      ) : (
        <div className='grid grid-cols-1 md:grid-cols-2 gap-x-14px gap-y-12px'>
          <Field label={t('settings.computeDetails')} className='md:col-span-2'>
            <Input.TextArea
              aria-label={t('settings.computeDetails')}
              value={draft.detailsMd ?? ''}
              onChange={(detailsMd) => setDraft((current) => ({ ...current, detailsMd }))}
              autoSize={{ minRows: 4, maxRows: 9 }}
            />
          </Field>
          {supportsWorkspaceSettings ? (
            <>
              <Field label={t('settings.computeScratchRoot')}>
                <Input
                  aria-label={t('settings.computeScratchRoot')}
                  value={draft.scratchRoot ?? ''}
                  onChange={(scratchRoot) => setDraft((current) => ({ ...current, scratchRoot }))}
                  placeholder={t('settings.computeScratchRootPlaceholder')}
                />
              </Field>
              <Field label={t('settings.computeConcurrency')}>
                <InputNumber
                  aria-label={t('settings.computeConcurrency')}
                  min={0}
                  max={1024}
                  value={draft.maxConcurrentJobs ?? undefined}
                  onChange={(maxConcurrentJobs) =>
                    setDraft((current) => ({
                      ...current,
                      maxConcurrentJobs: maxConcurrentJobs === undefined ? null : Number(maxConcurrentJobs),
                    }))
                  }
                  placeholder='0'
                />
              </Field>
              <Field label={t('settings.computeDataRoots')} className='md:col-span-2'>
                <Input.TextArea
                  aria-label={t('settings.computeDataRoots')}
                  value={(draft.dataRoots ?? []).join('\n')}
                  onChange={(value) =>
                    setDraft((current) => ({
                      ...current,
                      dataRoots: splitLines(value),
                    }))
                  }
                  placeholder={'/data/shared\n/datasets'}
                  autoSize={{ minRows: 3, maxRows: 7 }}
                />
              </Field>
            </>
          ) : null}
        </div>
      )}
    </Modal>
  );
};

export const AddSshHostModal: React.FC<{
  visible: boolean;
  aliases: SynonBiomedSshAliasesSnapshot;
  onClose: () => void;
  onAdded: () => Promise<void>;
}> = ({ visible, aliases, onClose, onAdded }) => {
  const { t } = useTranslation();
  const [draft, setDraft] = useState({
    alias: '',
    initialContext: '',
    user: '',
    port: undefined as number | undefined,
    identityFile: '',
  });
  const [advanced, setAdvanced] = useState(false);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!visible) return;
    setDraft({
      alias: '',
      initialContext: '',
      user: '',
      port: undefined,
      identityFile: '',
    });
    setAdvanced(false);
  }, [visible]);

  const submit = async () => {
    if (!draft.alias.trim() || saving) return;
    setSaving(true);
    try {
      const overrides = compactDraft({
        user: draft.user.trim() || undefined,
        port: draft.port,
        identityFile: draft.identityFile.trim() || undefined,
      });
      const input: SynonBiomedSshHostInput = {
        alias: draft.alias.trim(),
        ...(draft.initialContext.trim() ? { initialContext: draft.initialContext.trim() } : {}),
        ...(Object.keys(overrides).length > 0 ? { overrides } : {}),
      };
      await addSynonBiomedSshHost(input);
      await onAdded();
    } catch (error) {
      console.error('Failed to add SSH host:', error);
      Message.error(t('settings.computeWorkspace.addSsh.saveFailed'));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      visible={visible}
      title={t('settings.computeWorkspace.addSsh.title')}
      onCancel={onClose}
      footer={
        <div className='flex justify-end gap-8px'>
          <Button onClick={onClose}>{t('common.cancel')}</Button>
          <Button type='primary' disabled={!draft.alias.trim()} loading={saving} onClick={() => void submit()}>
            {t('common.add')}
          </Button>
        </div>
      }
      unmountOnExit
      style={{ width: 'min(620px, calc(100vw - 32px))' }}
    >
      <p className='m-0 mb-16px text-13px leading-6 text-t-secondary'>
        {t('settings.computeWorkspace.addSsh.description')}
      </p>
      {!aliases.configFound ? (
        <div
          role='alert'
          className='mb-18px px-14px py-12px border border-arco-2 bg-fill-1 rd-6px text-12px leading-5 text-t-primary'
        >
          {t('settings.computeWorkspace.addSsh.configMissing', {
            path: aliases.configPath || '~/.ssh/config',
          })}
          {aliases.isWsl
            ? t('settings.computeWorkspace.addSsh.wslHint')
            : t('settings.computeWorkspace.addSsh.configHint')}
        </div>
      ) : null}
      <div className='flex flex-col gap-16px'>
        <Field label={t('settings.computeWorkspace.addSsh.hostAlias')}>
          <Select
            aria-label={t('settings.computeWorkspace.addSsh.hostAlias')}
            value={draft.alias || undefined}
            onChange={(alias) => setDraft((current) => ({ ...current, alias }))}
            allowCreate
            allowClear
            placeholder={t('settings.computeWorkspace.addSsh.hostAliasPlaceholder')}
          >
            {aliases.aliases.map((alias) => (
              <Select.Option key={alias.alias} value={alias.alias}>
                {alias.alias}
                {alias.hostName ? ` · ${alias.hostName}` : ''}
              </Select.Option>
            ))}
          </Select>
        </Field>
        <Field label={t('settings.computeWorkspace.addSsh.notes')}>
          <Input.TextArea
            aria-label={t('settings.computeWorkspace.addSsh.notes')}
            value={draft.initialContext}
            onChange={(initialContext) => setDraft((current) => ({ ...current, initialContext }))}
            placeholder={t('settings.computeWorkspace.addSsh.notesPlaceholder')}
            autoSize={{ minRows: 5, maxRows: 10 }}
          />
        </Field>
        <button
          type='button'
          aria-expanded={advanced}
          className='w-full border-0 border-t border-arco-2 bg-transparent pt-14px text-left text-13px font-600 text-t-secondary cursor-pointer'
          onClick={() => setAdvanced((value) => !value)}
        >
          {advanced ? '▾' : '▸'} {t('settings.computeWorkspace.addSsh.advanced')}
        </button>
        {advanced ? (
          <div className='grid grid-cols-1 sm:grid-cols-2 gap-14px'>
            <Field label={t('settings.computeWorkspace.addSsh.username')}>
              <Input
                aria-label={t('settings.computeWorkspace.addSsh.username')}
                value={draft.user}
                onChange={(user) => setDraft((current) => ({ ...current, user }))}
              />
            </Field>
            <Field label={t('settings.computeWorkspace.addSsh.port')}>
              <InputNumber
                aria-label={t('settings.computeWorkspace.addSsh.port')}
                min={1}
                max={65535}
                value={draft.port}
                onChange={(port) =>
                  setDraft((current) => ({
                    ...current,
                    port: port === undefined ? undefined : Number(port),
                  }))
                }
              />
            </Field>
            <Field label={t('settings.computeWorkspace.addSsh.identityFile')} className='sm:col-span-2'>
              <Input
                aria-label={t('settings.computeWorkspace.addSsh.identityFile')}
                value={draft.identityFile}
                onChange={(identityFile) => setDraft((current) => ({ ...current, identityFile }))}
                placeholder={t('settings.computeWorkspace.addSsh.identityFilePlaceholder')}
              />
            </Field>
          </div>
        ) : null}
      </div>
    </Modal>
  );
};

type EndpointPreset = 'openfold3' | 'boltz2' | 'custom';
type EndpointDraft = Omit<SynonBiomedInferenceProviderInput, 'endpoint'> & {
  endpoint: string;
};
const endpointPresets: Record<EndpointPreset, EndpointDraft> = {
  openfold3: {
    name: 'openfold3-service',
    credentialName: 'NVIDIA_API_KEY',
    endpoint: 'https://health.api.nvidia.com/v1/biology/openfold/openfold3/predict',
    skillName: 'openfold3-nim',
  },
  boltz2: {
    name: 'boltz2-service',
    credentialName: 'NVIDIA_API_KEY',
    endpoint: 'https://health.api.nvidia.com/v1/biology/mit/boltz2/predict',
    skillName: 'boltz2-nim',
  },
  custom: { name: '', credentialName: '', endpoint: '', skillName: '' },
};

export const InferenceEndpointModal: React.FC<{
  visible: boolean;
  providers: SynonBiomedComputeProvider[];
  onClose: () => void;
  onAdded: () => Promise<void>;
}> = ({ visible, providers, onClose, onAdded }) => {
  const { t } = useTranslation();
  const [preset, setPreset] = useState<EndpointPreset>('openfold3');
  const [draft, setDraft] = useState<EndpointDraft>(endpointPresets.openfold3);
  const [saving, setSaving] = useState(false);
  const [probing, setProbing] = useState(false);
  useEffect(() => {
    if (visible) {
      setPreset('openfold3');
      setDraft(endpointPresets.openfold3);
    }
  }, [visible]);
  const choosePreset = (value: EndpointPreset) => {
    setPreset(value);
    setDraft({ ...endpointPresets[value] });
  };
  const valid = Boolean(draft.name.trim() && draft.endpoint.trim() && draft.skillName.trim());

  const probeExisting = async () => {
    const existing = providers.find((provider) => provider.name === `infer:${draft.name.trim()}`);
    if (!existing) {
      Message.error(t('settings.computeWorkspace.inference.existingNotFound'));
      return;
    }
    setProbing(true);
    try {
      await probeSynonBiomedComputeProvider(existing.name);
      Message.success(t('settings.computeWorkspace.inference.probeComplete'));
      await onAdded();
    } catch (error) {
      console.error('Failed to probe inference endpoint:', error);
      Message.error(t('settings.computeWorkspace.probeFailed'));
    } finally {
      setProbing(false);
    }
  };

  const saveAndProbe = async () => {
    if (!valid || saving) return;
    setSaving(true);
    try {
      const input: SynonBiomedInferenceProviderInput = {
        name: draft.name.trim(),
        endpoint: draft.endpoint.trim(),
        skillName: draft.skillName.trim(),
        ...(draft.credentialName?.trim() ? { credentialName: draft.credentialName.trim() } : {}),
      };
      const result = await addSynonBiomedInferenceProvider(input);
      if (result.warning) {
        console.warn('Inference endpoint warning:', result.warning);
        Message.warning(t('settings.computeWorkspace.inference.savedWithWarning'));
      }
      await probeSynonBiomedComputeProvider(`infer:${input.name}`);
      Message.success(t('settings.computeWorkspace.inference.savedAndProbed'));
      await onAdded();
    } catch (error) {
      console.error('Failed to save inference endpoint:', error);
      Message.error(t('settings.computeWorkspace.inference.saveFailed'));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      visible={visible}
      title={t('settings.computeWorkspace.inference.title')}
      onCancel={onClose}
      footer={
        <div className='flex flex-wrap justify-end gap-8px'>
          <Button onClick={onClose}>{t('common.cancel')}</Button>
          <Button loading={probing} onClick={() => void probeExisting()}>
            {t('settings.computeWorkspace.inference.probeExisting')}
          </Button>
          <Button type='primary' disabled={!valid} loading={saving} onClick={() => void saveAndProbe()}>
            {t('settings.computeWorkspace.inference.saveAndProbe')}
          </Button>
        </div>
      }
      unmountOnExit
      style={{ width: 'min(620px, calc(100vw - 32px))' }}
    >
      <p className='m-0 mb-16px text-12px leading-5 text-t-secondary'>
        {t('settings.computeWorkspace.inference.description')}
      </p>
      <div className='flex flex-col gap-13px'>
        <Field label={t('settings.computeWorkspace.inference.preset')}>
          <Select
            aria-label={t('settings.computeWorkspace.inference.preset')}
            value={preset}
            onChange={(value) => choosePreset(String(value) as EndpointPreset)}
          >
            <Select.Option value='openfold3'>AFold3 / OpenFold3（NVIDIA NIM）</Select.Option>
            <Select.Option value='boltz2'>Boltz2（NVIDIA NIM）</Select.Option>
            <Select.Option value='custom'>{t('settings.computeWorkspace.inference.customPreset')}</Select.Option>
          </Select>
        </Field>
        <Field label={t('settings.computeWorkspace.inference.endpointName')}>
          <Input
            aria-label={t('settings.computeWorkspace.inference.endpointName')}
            value={draft.name}
            onChange={(name) => setDraft((current) => ({ ...current, name }))}
          />
        </Field>
        <Field label={t('settings.computeWorkspace.inference.credentialName')}>
          <Input
            aria-label={t('settings.computeWorkspace.inference.credentialName')}
            value={draft.credentialName ?? ''}
            onChange={(credentialName) => setDraft((current) => ({ ...current, credentialName }))}
          />
        </Field>
        <Field label={t('settings.computeWorkspace.inference.endpointUrl')}>
          <Input
            aria-label={t('settings.computeWorkspace.inference.endpointUrl')}
            value={draft.endpoint}
            onChange={(endpoint) => setDraft((current) => ({ ...current, endpoint }))}
          />
        </Field>
        <Field label={t('settings.computeWorkspace.inference.linkedSkill')}>
          <Input
            aria-label={t('settings.computeWorkspace.inference.linkedSkill')}
            value={draft.skillName}
            onChange={(skillName) => setDraft((current) => ({ ...current, skillName }))}
          />
        </Field>
      </div>
    </Modal>
  );
};

const Field: React.FC<{
  label: string;
  className?: string;
  children: React.ReactNode;
}> = ({ label, className, children }) => (
  <label className={`flex flex-col gap-5px text-12px text-t-secondary ${className ?? ''}`}>
    <span>{label}</span>
    {children}
  </label>
);

const splitLines = (value: string): string[] =>
  value
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean);
const compactDraft = <T extends Record<string, unknown>>(value: T): T =>
  Object.fromEntries(Object.entries(value).filter(([, item]) => item !== undefined && item !== '')) as T;

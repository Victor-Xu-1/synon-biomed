import { Button, Input, Message, Modal, Select, Spin, Tag, Tooltip } from '@arco-design/web-react';
import { CheckOne, Delete, Edit, Refresh } from '@icon-park/react';
import type { TFunction } from 'i18next';
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import SettingsPageHeader from './components/SettingsPageHeader';
import SettingsPageWrapper from './components/SettingsPageWrapper';
import {
  SettingsGeneratedEmptyArtwork,
  SettingsGeneratedIcon,
  type SettingsGeneratedIconId,
} from './components/SettingsGeneratedAsset';
import {
  activateSynonBiomedLlmProfile,
  deleteSynonBiomedLlmProfile,
  loadSynonBiomedLlmProviders,
  saveSynonBiomedLlmProfile,
  testSynonBiomedLlmProfile,
  type SynonBiomedLlmProfile,
  type SynonBiomedLlmProfileInput,
  type SynonBiomedLlmProviderTemplate,
  type SynonBiomedLlmProvidersSnapshot,
} from '@/renderer/services/synonBiomedLlm';

type EditorState = {
  profile?: SynonBiomedLlmProfile;
};

type ProfileDraft = {
  name: string;
  provider: string;
  baseUrl: string;
  model: string;
  apiKey: string;
  temperature: number;
};

type NumericOption = {
  value: number;
  labelKey: string;
};

const TEMPERATURE_OPTIONS: NumericOption[] = [
  { value: 0.1, labelKey: 'settings.modelsTemperatureVeryStable' },
  { value: 0.2, labelKey: 'settings.modelsTemperatureStable' },
  { value: 0.5, labelKey: 'settings.modelsTemperatureBalanced' },
  { value: 0.8, labelKey: 'settings.modelsTemperatureFlexible' },
  { value: 1.2, labelKey: 'settings.modelsTemperatureCreative' },
  { value: 2, labelKey: 'settings.modelsTemperatureVeryCreative' },
];

function resolveModelIcon(
  profile: Pick<SynonBiomedLlmProfile, 'name' | 'provider' | 'model'>
): SettingsGeneratedIconId {
  const searchable = `${profile.name} ${profile.provider} ${profile.model}`.toLowerCase();
  if (/openai|ark/.test(searchable)) return 'connector-database';
  if (/azure/.test(searchable)) return 'connector-clipboard';
  if (/anthropic|claude/.test(searchable)) return 'connector-cube';
  if (/google|gemini/.test(searchable)) return 'connector-dna';
  if (/mistral/.test(searchable)) return 'connector-bars';
  if (/protein|bio|science|chem/.test(searchable)) return 'connector-protein';
  return 'models';
}

export const SynonBiomedModelsSettingsContent: React.FC = () => {
  const { t } = useTranslation();
  const [snapshot, setSnapshot] = useState<SynonBiomedLlmProvidersSnapshot>({
    profiles: [],
    templates: [],
  });
  const [loading, setLoading] = useState(true);
  const [editor, setEditor] = useState<EditorState | null>(null);
  const [pendingProfileId, setPendingProfileId] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      setSnapshot(await loadSynonBiomedLlmProviders());
    } catch (error) {
      console.error('Failed to load Synon LLM profiles:', error);
      Message.error(t('settings.modelsLoadError'));
    } finally {
      setLoading(false);
    }
  }, [t]);

  useEffect(() => {
    void load();
  }, [load]);

  const activeProfile = useMemo(
    () => snapshot.profiles.find((profile) => profile.id === snapshot.activeProfileId),
    [snapshot.activeProfileId, snapshot.profiles]
  );

  const runProfileAction = useCallback(
    async (profileId: string, action: () => Promise<unknown>, successMessage?: string) => {
      setPendingProfileId(profileId);
      try {
        await action();
        if (successMessage) Message.success(successMessage);
        await load();
      } catch (error) {
        console.error('Synon LLM profile action failed:', error);
        Message.error(t('settings.modelsActionFailed'));
      } finally {
        setPendingProfileId(null);
      }
    },
    [load, t]
  );

  const handleTest = useCallback(
    async (profile: SynonBiomedLlmProfile) => {
      setPendingProfileId(profile.id);
      try {
        await testSynonBiomedLlmProfile(profile.id);
        // Providers may return an internal deployment alias (for example a
        // routed model name). The user needs the configured profile ID here;
        // the alias belongs in diagnostics, not the connection verdict.
        Message.success(t('settings.modelsTestSuccess', { model: profile.model }));
      } catch (error) {
        console.error('Synon LLM profile test failed:', error);
        Message.error(t('settings.modelsTestFailed'));
      } finally {
        setPendingProfileId(null);
      }
    },
    [t]
  );

  const handleDelete = useCallback(
    (profile: SynonBiomedLlmProfile) => {
      Modal.confirm({
        title: t('settings.modelsDeleteTitle'),
        content: t('settings.modelsDeleteConfirm', { name: profile.name }),
        okButtonProps: { status: 'danger' },
        onOk: () =>
          runProfileAction(profile.id, () => deleteSynonBiomedLlmProfile(profile.id), t('settings.modelsDeleted')),
      });
    },
    [runProfileAction, t]
  );

  const headerActions = (
    <Button
      type='secondary'
      icon={<Refresh theme='outline' size='14' />}
      onClick={() => void load()}
      loading={loading}
      aria-label={t('common.refresh')}
    >
      {t('common.refresh')}
    </Button>
  );

  return (
    <div className='settings-models-page flex flex-col'>
      <SettingsPageHeader
        data-testid='models-header'
        title={t('settings.models')}
        description={t('settings.modelsDescription')}
        actions={headerActions}
      />

      <div className='settings-summary-strip'>
        <StatusItem icon='connector-cube' label={t('settings.modelsProfiles')} value={snapshot.profiles.length} />
        <StatusItem
          icon='connector-bars'
          label={t('settings.modelsActive')}
          value={activeProfile?.model || t('settings.modelsUnconfigured')}
        />
        <SummaryActionItem
          data-testid='synon-biomed-model-add'
          icon='connector-database'
          label={t('settings.modelsAdd')}
          onClick={() => setEditor({})}
        />
      </div>

      {loading && snapshot.profiles.length === 0 ? (
        <div className='h-180px flex items-center justify-center'>
          <Spin />
        </div>
      ) : snapshot.profiles.length === 0 ? (
        <div className='border border-dashed border-arco-2 rd-8px py-36px text-center'>
          <SettingsGeneratedEmptyArtwork id='governance' className='settings-empty-artwork-illustration' />
          <div className='text-14px font-600 text-t-primary'>{t('settings.modelsEmpty')}</div>
          <div className='mt-5px text-12px text-t-secondary'>{t('settings.modelsEmptyHint')}</div>
        </div>
      ) : (
        <div className='settings-list flex flex-col' data-testid='synon-biomed-model-profiles'>
          {snapshot.profiles.map((profile) => {
            const active = profile.id === snapshot.activeProfileId;
            const pending = pendingProfileId === profile.id;
            return (
              <div
                key={profile.id}
                data-testid={`synon-biomed-model-profile-${profile.id}`}
                className='settings-list-row settings-model-profile px-8px py-13px'
              >
                <div className='settings-model-profile__main'>
                  <div className='settings-model-profile__identity'>
                    <SettingsGeneratedIcon id={resolveModelIcon(profile)} className='settings-models-profile__icon' />
                    <div className='settings-model-profile__identity-copy'>
                      <div className='settings-model-profile__identity-name'>{profile.name}</div>
                      <div className='settings-model-profile__identity-model'>{profile.model}</div>
                      <div className='settings-model-profile__identity-tags'>
                        {active && <Tag color='green'>{t('settings.modelsCurrent')}</Tag>}
                        <Tag color='gray'>{profile.provider}</Tag>
                      </div>
                    </div>
                  </div>
                  <div className='settings-model-profile__detail settings-model-profile__model-id'>
                    <span className='settings-model-profile__detail-label'>{t('settings.modelsModelId')}</span>
                    <span className='settings-model-profile__detail-value' title={profile.model}>
                      {profile.model || '-'}
                    </span>
                  </div>
                  <div className='settings-model-profile__detail settings-model-profile__base-url'>
                    <span className='settings-model-profile__detail-label'>{t('settings.modelsBaseUrl')}</span>
                    <span className='settings-model-profile__detail-value' title={profile.baseUrl}>
                      {profile.baseUrl || '-'}
                    </span>
                  </div>
                  <div className='settings-model-profile__key'>
                    <span className='settings-model-profile__detail-label'>{t('settings.modelsCredential')}</span>
                    <span
                      className={
                        profile.hasApiKey
                          ? 'settings-model-profile__key-value is-saved'
                          : 'settings-model-profile__key-value'
                      }
                    >
                      <span className='settings-model-profile__key-dot' aria-hidden='true' />
                      {profile.hasApiKey ? t('settings.modelsKeySaved') : t('settings.modelsKeyMissing')}
                    </span>
                    <span className='settings-model-profile__runtime'>
                      {t('settings.modelsRuntimeParameters', {
                        temperature: numericOptionLabel(TEMPERATURE_OPTIONS, profile.temperature ?? 0.2, t),
                      })}
                    </span>
                  </div>
                  <div className='settings-model-profile__actions'>
                    <Tooltip content={t('settings.modelsSetCurrent')}>
                      <Button
                        type='outline'
                        className='settings-model-profile__activate'
                        icon={<CheckOne theme='outline' size='16' />}
                        aria-label={t('settings.modelsSetCurrentNamed', {
                          name: profile.name,
                        })}
                        disabled={active}
                        loading={pending}
                        onClick={() =>
                          void runProfileAction(
                            profile.id,
                            () => activateSynonBiomedLlmProfile(profile),
                            t('settings.modelsActivated')
                          )
                        }
                      >
                        {t('settings.modelsSetCurrent')}
                      </Button>
                    </Tooltip>
                    <Button
                      type='secondary'
                      size='small'
                      aria-label={t('settings.modelsTestNamed', {
                        name: profile.name,
                      })}
                      loading={pending}
                      onClick={() => void handleTest(profile)}
                    >
                      {t('settings.modelsTest')}
                    </Button>
                    <Tooltip content={t('common.edit')}>
                      <Button
                        type='outline'
                        className='settings-model-profile__edit'
                        icon={<Edit theme='outline' size='16' />}
                        aria-label={t('settings.modelsEditNamed', {
                          name: profile.name,
                        })}
                        onClick={() => setEditor({ profile })}
                      >
                        {t('common.edit')}
                      </Button>
                    </Tooltip>
                    <Tooltip content={t('common.delete')}>
                      <Button
                        type='outline'
                        status='danger'
                        icon={<Delete theme='outline' size='16' />}
                        aria-label={t('settings.modelsDeleteNamed', {
                          name: profile.name,
                        })}
                        onClick={() => handleDelete(profile)}
                      >
                        {t('common.delete')}
                      </Button>
                    </Tooltip>
                  </div>
                </div>
              </div>
            );
          })}
        </div>
      )}

      <ProfileEditorModal
        editor={editor}
        templates={snapshot.templates}
        onClose={() => setEditor(null)}
        onSaved={async (input) => {
          await saveSynonBiomedLlmProfile(input);
          setEditor(null);
          Message.success(t('settings.modelsSaved'));
          await load();
        }}
      />
    </div>
  );
};

const SynonBiomedModelsSettings: React.FC = () => (
  <SettingsPageWrapper>
    <SynonBiomedModelsSettingsContent />
  </SettingsPageWrapper>
);

const StatusItem: React.FC<{ icon: SettingsGeneratedIconId; label: string; value: React.ReactNode }> = ({
  icon,
  label,
  value,
}) => (
  <div className='settings-summary-item'>
    <SettingsGeneratedIcon id={icon} className='settings-summary-item__icon' />
    <div className='settings-summary-item__copy min-w-0'>
      <span className='settings-summary-item__label'>{label}</span>
      <strong className='text-t-primary font-600 truncate'>{value}</strong>
    </div>
  </div>
);

const SummaryActionItem: React.FC<{
  'data-testid': string;
  icon: SettingsGeneratedIconId;
  label: string;
  onClick: () => void;
}> = ({ 'data-testid': dataTestId, icon, label, onClick }) => (
  <button
    type='button'
    className='settings-summary-item settings-summary-action'
    data-testid={dataTestId}
    aria-label={label}
    onClick={onClick}
  >
    <SettingsGeneratedIcon id={icon} className='settings-summary-item__icon' />
    <span className='settings-summary-action__label'>{label}</span>
  </button>
);

const ProfileEditorModal: React.FC<{
  editor: EditorState | null;
  templates: SynonBiomedLlmProviderTemplate[];
  onClose: () => void;
  onSaved: (input: SynonBiomedLlmProfileInput) => Promise<void>;
}> = ({ editor, templates, onClose, onSaved }) => {
  const { t } = useTranslation();
  const defaultTemplate = preferredProviderTemplate(templates);
  const [draft, setDraft] = useState<ProfileDraft>(() => createDraft(editor?.profile, defaultTemplate));
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (editor) setDraft(createDraft(editor.profile, preferredProviderTemplate(templates)));
  }, [editor, templates]);

  const selectedTemplate = templates.find((template) => template.provider === draft.provider) ?? defaultTemplate;
  const temperatureOptions = withCurrentNumericOption(TEMPERATURE_OPTIONS, draft.temperature);
  const modelOptions = Array.from(
    new Set([draft.model, ...(selectedTemplate?.modelExamples ?? [])].map((model) => model.trim()).filter(Boolean))
  );
  const title = editor?.profile ? t('settings.modelsEdit') : t('settings.modelsAdd');
  const valid = Boolean(draft.name.trim() && draft.provider && draft.baseUrl.trim() && draft.model.trim());

  const submit = async () => {
    if (!editor || !valid || saving) return;
    setSaving(true);
    try {
      await onSaved({
        id: editor.profile?.id,
        name: draft.name.trim(),
        provider: draft.provider,
        baseUrl: draft.baseUrl.trim(),
        model: draft.model.trim(),
        apiKey: draft.apiKey.trim() || undefined,
        copyApiKeyFrom: editor.profile?.id,
        temperature: draft.temperature,
        ...(editor.profile ? { maxTokens: null } : {}),
      });
    } catch (error) {
      console.error('Failed to save Synon LLM profile:', error);
      Message.error(t('settings.modelsSaveFailed'));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      visible={Boolean(editor)}
      title={title}
      className='synon-biomed-model-editor-modal'
      onCancel={onClose}
      onOk={() => void submit()}
      okText={t('common.save')}
      cancelText={t('common.cancel')}
      okButtonProps={{ disabled: !valid, loading: saving }}
      unmountOnExit
      style={{ width: 'min(620px, 92vw)' }}
    >
      <div className='grid grid-cols-1 md:grid-cols-2 gap-x-14px gap-y-12px'>
        <Field label={t('settings.modelsName')} className='md:col-span-2'>
          <Input
            aria-label={t('settings.modelsName')}
            value={draft.name}
            onChange={(name) => setDraft((current) => ({ ...current, name }))}
            placeholder={t('settings.modelsNamePlaceholder')}
          />
        </Field>
        <Field label={t('settings.modelsProvider')}>
          <Select
            aria-label={t('settings.modelsProvider')}
            data-testid='synon-biomed-provider-select'
            value={draft.provider}
            getPopupContainer={getEditorPopupContainer}
            onChange={(provider) => {
              const template = templates.find((item) => item.provider === provider);
              setDraft((current) => ({
                ...current,
                provider,
                baseUrl: template?.defaultBaseUrl || current.baseUrl,
                model: template?.modelExamples[0] || '',
              }));
            }}
          >
            {templates.map((template) => (
              <Select.Option key={template.provider} value={template.provider}>
                {localizeProviderTemplate(template, t).label}
              </Select.Option>
            ))}
          </Select>
        </Field>
        <Field label={t('settings.modelsModelId')}>
          <Select
            aria-label={t('settings.modelsModelId')}
            data-testid='synon-biomed-model-id-select'
            value={draft.model || undefined}
            getPopupContainer={getEditorPopupContainer}
            onChange={(model) =>
              setDraft((current) => ({
                ...current,
                model: typeof model === 'string' ? model : '',
              }))
            }
            placeholder={t('settings.customModelPlaceholder')}
            showSearch
            allowClear
            allowCreate
          >
            {modelOptions.map((model) => (
              <Select.Option key={model} value={model}>
                {model}
              </Select.Option>
            ))}
          </Select>
        </Field>
        <Field label={t('settings.modelsBaseUrl')} className='md:col-span-2'>
          <Input
            aria-label={t('settings.modelsBaseUrl')}
            value={draft.baseUrl}
            onChange={(baseUrl) => setDraft((current) => ({ ...current, baseUrl }))}
            placeholder={selectedTemplate?.defaultBaseUrl}
          />
        </Field>
        <Field label={t('settings.modelsApiKey')} className='md:col-span-2'>
          <Input.Password
            aria-label={t('settings.modelsApiKey')}
            value={draft.apiKey}
            onChange={(apiKey) => setDraft((current) => ({ ...current, apiKey }))}
            placeholder={editor?.profile?.hasApiKey ? t('settings.modelsKeepKey') : t('settings.modelsKeyPlaceholder')}
            autoComplete='new-password'
          />
        </Field>
        <Field label={t('settings.modelsTemperature')} description={t('settings.modelsTemperatureHint')}>
          <Select
            aria-label={t('settings.modelsTemperature')}
            data-testid='synon-biomed-temperature-select'
            value={String(draft.temperature)}
            getPopupContainer={getEditorPopupContainer}
            onChange={(temperature) =>
              setDraft((current) => ({
                ...current,
                temperature: Number(temperature ?? 0.2),
              }))
            }
          >
            {temperatureOptions.map((option) => (
              <Select.Option key={`temperature-${option.value}`} value={String(option.value)}>
                {t(option.labelKey)}
              </Select.Option>
            ))}
          </Select>
        </Field>
      </div>
    </Modal>
  );
};

const Field: React.FC<{
  label: string;
  description?: string;
  className?: string;
  children: React.ReactNode;
}> = ({ label, description, className, children }) => (
  <div className={`flex flex-col gap-5px text-12px text-t-secondary ${className ?? ''}`}>
    <span>{label}</span>
    {children}
    {description ? <span className='text-11px leading-18px text-t-tertiary'>{description}</span> : null}
  </div>
);

function createDraft(profile?: SynonBiomedLlmProfile, template?: SynonBiomedLlmProviderTemplate): ProfileDraft {
  return {
    name: profile?.name ?? '',
    provider: profile?.provider ?? template?.provider ?? '',
    baseUrl: profile?.baseUrl ?? template?.defaultBaseUrl ?? '',
    model: profile?.model ?? template?.modelExamples[0] ?? '',
    apiKey: '',
    temperature: profile?.temperature ?? 0.2,
  };
}

function withCurrentNumericOption(options: NumericOption[], value: number): NumericOption[] {
  return options.some((option) => option.value === value)
    ? options
    : [...options, { value, labelKey: 'settings.modelsCurrentValue' }];
}

function numericOptionLabel(options: NumericOption[], value: number, t: TFunction): string {
  const option = withCurrentNumericOption(options, value).find((item) => item.value === value);
  return option ? t(option.labelKey) : t('settings.modelsCurrentValue');
}

function getEditorPopupContainer(): Element {
  return document.body;
}

function preferredProviderTemplate(
  templates: SynonBiomedLlmProviderTemplate[]
): SynonBiomedLlmProviderTemplate | undefined {
  return (
    templates.find((template) => template.provider.toLowerCase() === 'deepseek') ??
    templates.find((template) => template.modelExamples.length > 0 && template.defaultBaseUrl) ??
    templates[0]
  );
}

const LOCAL_PROVIDER_LABEL_KEYS: Partial<Record<string, string>> = {
  custom: 'settings.modelsProviderCustomCompatible',
  moonshot: 'settings.modelsProviderMoonshotChina',
  'moonshot-global': 'settings.modelsProviderMoonshotGlobal',
  ollama: 'settings.modelsProviderOllamaLocal',
  'lm-studio': 'settings.modelsProviderLmStudioLocal',
  vllm: 'settings.modelsProviderVllmSelfHosted',
};

function localizeProviderTemplate(
  template: SynonBiomedLlmProviderTemplate,
  t: TFunction
): SynonBiomedLlmProviderTemplate {
  const labelKey = LOCAL_PROVIDER_LABEL_KEYS[template.provider.toLowerCase()];
  return labelKey ? { ...template, label: t(labelKey) } : template;
}

export default SynonBiomedModelsSettings;

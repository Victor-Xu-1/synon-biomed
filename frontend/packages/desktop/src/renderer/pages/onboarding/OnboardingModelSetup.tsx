/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { Button, Input, Message, Select, Spin } from '@arco-design/web-react';
import { Plus } from '@icon-park/react';
import React, { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  activateSynonBiomedLlmProfile,
  loadSynonBiomedLlmProviders,
  saveSynonBiomedLlmProfile,
  testSynonBiomedLlmProfile,
  type SynonBiomedLlmProfile,
  type SynonBiomedLlmProvidersSnapshot,
} from '@/renderer/services/synonBiomedLlm';
import styles from './onboarding.module.css';

type ModelDraft = {
  provider: string;
  model: string;
  baseUrl: string;
  apiKey: string;
};

const EMPTY_SNAPSHOT: SynonBiomedLlmProvidersSnapshot = { profiles: [], templates: [] };

/**
 * Compact first-run model configuration. Setup is optional: finishing the
 * onboarding flow never requires a profile here because deployments can also
 * provide runner credentials through environment configuration.
 */
const OnboardingModelSetup: React.FC = () => {
  const { t } = useTranslation();
  const [snapshot, setSnapshot] = useState<SynonBiomedLlmProvidersSnapshot | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [draft, setDraft] = useState<ModelDraft | null>(null);
  const [saving, setSaving] = useState(false);
  const [pendingId, setPendingId] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setSnapshot(await loadSynonBiomedLlmProviders());
      setLoadFailed(false);
    } catch (error) {
      console.error('[OnboardingModelSetup] llm_profiles_failed', error);
      setSnapshot(EMPTY_SNAPSHOT);
      setLoadFailed(true);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const profiles = snapshot?.profiles ?? [];
  const templates = snapshot?.templates ?? [];
  const activeProfile =
    snapshot?.activeProfileId != null
      ? (profiles.find((profile) => profile.id === snapshot.activeProfileId) ?? null)
      : null;
  const selectedTemplate = templates.find((template) => template.provider === draft?.provider);
  const draftValid = Boolean(draft && draft.provider && draft.model.trim() && draft.baseUrl.trim());

  const openDraft = () => {
    const template = templates.find((item) => item.modelExamples.length > 0 && item.defaultBaseUrl) ?? templates[0];
    setDraft({
      provider: template?.provider ?? '',
      model: template?.modelExamples[0] ?? '',
      baseUrl: template?.defaultBaseUrl ?? '',
      apiKey: '',
    });
  };

  const updateDraft = (patch: Partial<ModelDraft>) => {
    setDraft((current) => {
      if (!current) return current;
      const next = { ...current, ...patch };
      if (patch.provider && patch.provider !== current.provider) {
        const template = templates.find((item) => item.provider === patch.provider);
        next.baseUrl = template?.defaultBaseUrl ?? '';
        next.model = template?.modelExamples[0] ?? '';
      }
      return next;
    });
  };

  const save = async () => {
    if (!draft || !draftValid || saving) return;
    setSaving(true);
    try {
      const providerLabel = selectedTemplate?.label ?? draft.provider;
      const savedProfile = await saveSynonBiomedLlmProfile({
        name: `${providerLabel} · ${draft.model.trim()}`.slice(0, 64),
        provider: draft.provider,
        baseUrl: draft.baseUrl.trim(),
        model: draft.model.trim(),
        apiKey: draft.apiKey.trim() || undefined,
      });
      if (savedProfile.id !== snapshot?.activeProfileId) {
        await activateSynonBiomedLlmProfile(savedProfile);
      }
      Message.success(t('guid.onboarding.model.saved'));
      setDraft(null);
      await load();
    } catch (error) {
      console.error('[OnboardingModelSetup] save_failed', error);
      Message.error(t('guid.onboarding.model.saveFailed'));
    } finally {
      setSaving(false);
    }
  };

  const setActiveProfile = async (profile: SynonBiomedLlmProfile) => {
    setPendingId(profile.id);
    try {
      await activateSynonBiomedLlmProfile(profile);
      Message.success(t('guid.onboarding.model.activated'));
      await load();
    } catch (error) {
      console.error('[OnboardingModelSetup] activate_failed', error);
      Message.error(t('guid.onboarding.model.actionFailed'));
    } finally {
      setPendingId(null);
    }
  };

  const testProfile = async (profile: SynonBiomedLlmProfile) => {
    setPendingId(profile.id);
    try {
      await testSynonBiomedLlmProfile(profile.id);
      Message.success(t('guid.onboarding.model.testSuccess', { model: profile.model }));
    } catch (error) {
      console.error('[OnboardingModelSetup] test_failed', error);
      Message.error(t('guid.onboarding.model.testFailed'));
    } finally {
      setPendingId(null);
    }
  };

  return (
    <div className={styles.modelSetup} data-testid='onboarding-model-setup'>
      {snapshot === null ? (
        <div className={styles.modelLoading}>
          <Spin size={16} />
        </div>
      ) : (
        <>
          <div className={styles.modelStatus} data-testid='onboarding-model-status'>
            <span>
              <strong>{activeProfile?.name ?? t('guid.onboarding.model.unconfigured')}</strong>
              <small>{activeProfile?.model ?? t('guid.onboarding.model.unconfiguredHint')}</small>
            </span>
          </div>
          {profiles.length > 0 && (
            <div className={styles.optionList} data-testid='onboarding-model-profiles'>
              {profiles.map((profile) => {
                const active = profile.id === snapshot.activeProfileId;
                return (
                  <div key={profile.id} className={styles.modelRow}>
                    <span>
                      <strong>{profile.name}</strong>
                      <small>{profile.model}</small>
                      {active && <small className={styles.runtimeMeta}>{t('guid.onboarding.model.current')}</small>}
                    </span>
                    <span className={styles.modelRowActions}>
                      {!active && (
                        <Button
                          size='mini'
                          loading={pendingId === profile.id}
                          onClick={() => void setActiveProfile(profile)}
                        >
                          {t('guid.onboarding.model.setActive')}
                        </Button>
                      )}
                      <Button size='mini' loading={pendingId === profile.id} onClick={() => void testProfile(profile)}>
                        {t('guid.onboarding.model.test')}
                      </Button>
                    </span>
                  </div>
                );
              })}
            </div>
          )}
          {loadFailed && <p className={styles.suggestionStatus}>{t('guid.onboarding.model.loadError')}</p>}
          {draft === null ? (
            <Button
              type='secondary'
              icon={<Plus theme='outline' size='14' />}
              onClick={openDraft}
              data-testid='onboarding-model-add'
            >
              {t('guid.onboarding.model.add')}
            </Button>
          ) : (
            <div className={styles.modelForm} data-testid='onboarding-model-form'>
              <label className={styles.fieldLabel}>
                <span>{t('guid.onboarding.model.provider')}</span>
                <Select
                  aria-label={t('guid.onboarding.model.provider')}
                  value={draft.provider || undefined}
                  onChange={(provider: string) => updateDraft({ provider })}
                >
                  {templates.map((template) => (
                    <Select.Option key={template.provider} value={template.provider}>
                      {template.label}
                    </Select.Option>
                  ))}
                </Select>
              </label>
              <label className={styles.fieldLabel}>
                <span>{t('guid.onboarding.model.modelId')}</span>
                <Input
                  aria-label={t('guid.onboarding.model.modelId')}
                  value={draft.model}
                  onChange={(model) => updateDraft({ model })}
                  placeholder={selectedTemplate?.modelExamples.join(', ')}
                />
              </label>
              <label className={`${styles.fieldLabel} ${styles.modelFormWide}`}>
                <span>{t('guid.onboarding.model.baseUrl')}</span>
                <Input
                  aria-label={t('guid.onboarding.model.baseUrl')}
                  value={draft.baseUrl}
                  onChange={(baseUrl) => updateDraft({ baseUrl })}
                  placeholder={selectedTemplate?.defaultBaseUrl}
                />
              </label>
              <label className={`${styles.fieldLabel} ${styles.modelFormWide}`}>
                <span>{t('guid.onboarding.model.apiKey')}</span>
                <Input.Password
                  aria-label={t('guid.onboarding.model.apiKey')}
                  value={draft.apiKey}
                  onChange={(apiKey) => updateDraft({ apiKey })}
                  placeholder={t('guid.onboarding.model.apiKeyPlaceholder')}
                  autoComplete='new-password'
                />
              </label>
              <div className={styles.modelFormActions}>
                <Button type='primary' loading={saving} disabled={!draftValid} onClick={() => void save()}>
                  {t('guid.onboarding.model.save')}
                </Button>
                <Button disabled={saving} onClick={() => setDraft(null)}>
                  {t('common.cancel')}
                </Button>
              </div>
            </div>
          )}
          <p className={styles.suggestionStatus}>{t('guid.onboarding.model.skipHint')}</p>
        </>
      )}
    </div>
  );
};

export default OnboardingModelSetup;

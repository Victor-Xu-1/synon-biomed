/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { SpeechToTextConfig } from '@/common/types/provider/speech';
import SynonSelect from '@/renderer/components/base/SynonSelect';
import { SPEECH_TO_TEXT_CONFIG_CHANGED_EVENT } from '@/renderer/services/SpeechToTextService';
import { getClientBusinessSetting, setClientBusinessSetting } from '@/renderer/services/clientBusinessSettings';
import { Divider, Form, Input, Switch } from '@arco-design/web-react';
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import SpeechTestPanel from './SpeechTestPanel';
import {
  DEEPGRAM_SPEECH_MODEL_PRESETS,
  DEFAULT_SPEECH_TO_TEXT_CONFIG,
  LOCAL_SPEECH_MODEL,
  OPENAI_SPEECH_MODEL_PRESETS,
  SPEECH_LANGUAGE_OPTIONS,
  applySpeechSource,
  buildModelOptions,
  deriveSpeechSource,
  getAutoTranscriptionPrompt,
  isValidHttpUrl,
  migrateSpeechLanguage,
  normalizeSpeechToTextConfig,
  type SpeechSource,
} from './speechSettingsUtils';

type OpenAIField = keyof NonNullable<SpeechToTextConfig['openai']>;
type DeepgramField = keyof NonNullable<SpeechToTextConfig['deepgram']>;
type LocalField = keyof NonNullable<SpeechToTextConfig['local']>;

type VoiceInputSectionProps = {
  embedded?: boolean;
};

const FieldLabel: React.FC<{ labelKey: string; requirement: 'required' | 'optional' }> = ({
  labelKey,
  requirement,
}) => {
  const { t } = useTranslation();
  return (
    <span className='inline-flex items-center gap-6px'>
      <span>{t(labelKey)}</span>
      <span aria-hidden='true' className='text-12px text-t-tertiary'>
        ({t(requirement === 'required' ? 'settings.speechToTextRequired' : 'settings.speechToTextOptional')})
      </span>
    </span>
  );
};

const VoiceInputSection: React.FC<VoiceInputSectionProps> = ({ embedded = false }) => {
  const { t } = useTranslation();
  const [config, setConfig] = useState<SpeechToTextConfig>(DEFAULT_SPEECH_TO_TEXT_CONFIG);
  // Source is UI state, only initialized from the stored config. A purely
  // derived source would snap "custom" back to "openai" while base_url is
  // still empty, making custom mode unreachable for fresh users.
  const [source, setSource] = useState<SpeechSource>('local');
  const lastCustomBaseUrlRef = useRef('');

  useEffect(() => {
    let cancelled = false;

    const loadSpeechConfig = async () => {
      try {
        const stored = await getClientBusinessSetting('tools.speechToText');
        if (cancelled) {
          return;
        }
        const normalized = migrateSpeechLanguage(normalizeSpeechToTextConfig(stored));
        setConfig(normalized);
        setSource(deriveSpeechSource(normalized));
        if (deriveSpeechSource(normalized) === 'custom') {
          lastCustomBaseUrlRef.current = normalized.openai?.base_url ?? '';
        }
      } catch (error) {
        console.error('Failed to load speech-to-text config:', error);
      }
    };

    void loadSpeechConfig();

    return () => {
      cancelled = true;
    };
  }, []);

  const updateConfig = useCallback((updater: (current: SpeechToTextConfig) => SpeechToTextConfig) => {
    setConfig((current) => {
      const next = normalizeSpeechToTextConfig(updater(current));
      void setClientBusinessSetting('tools.speechToText', next).catch((error) => {
        console.error('Failed to save speech-to-text config:', error);
      });
      if (typeof window !== 'undefined') {
        window.dispatchEvent(new CustomEvent(SPEECH_TO_TEXT_CONFIG_CHANGED_EVENT));
      }
      return next;
    });
  }, []);

  const handleSourceChange = useCallback(
    (value: string) => {
      setSource(value as SpeechSource);
      updateConfig((current) => {
        if (deriveSpeechSource(current) === 'custom') {
          lastCustomBaseUrlRef.current = current.openai?.base_url ?? '';
        }
        return applySpeechSource(current, value as SpeechSource, lastCustomBaseUrlRef.current);
      });
    },
    [updateConfig]
  );

  const handleOpenAIChange = useCallback(
    (field: OpenAIField, value: string) => {
      updateConfig(
        (current) =>
          ({
            ...current,
            openai: { ...DEFAULT_SPEECH_TO_TEXT_CONFIG.openai, ...current.openai, [field]: value },
          }) as SpeechToTextConfig
      );
    },
    [updateConfig]
  );

  const handleDeepgramChange = useCallback(
    (field: DeepgramField, value: string | boolean) => {
      updateConfig(
        (current) =>
          ({
            ...current,
            deepgram: { ...DEFAULT_SPEECH_TO_TEXT_CONFIG.deepgram, ...current.deepgram, [field]: value },
          }) as SpeechToTextConfig
      );
    },
    [updateConfig]
  );

  const handleLocalChange = useCallback(
    (field: LocalField, value: string) => {
      updateConfig(
        (current) =>
          ({
            ...current,
            local: { ...DEFAULT_SPEECH_TO_TEXT_CONFIG.local, ...current.local, [field]: value },
          }) as SpeechToTextConfig
      );
    },
    [updateConfig]
  );

  const isDeepgram = source === 'deepgram';
  const isLocal = source === 'local';
  const isCustom = source === 'custom';
  const activeLanguage =
    (isLocal ? config.local?.language : isDeepgram ? config.deepgram?.language : config.openai?.language) ?? '';
  const activeModel =
    (isLocal ? config.local?.model : isDeepgram ? config.deepgram?.model : config.openai?.model) ?? '';
  const activeApiKey = isLocal ? '' : ((isDeepgram ? config.deepgram?.api_key : config.openai?.api_key) ?? '');
  const modelPresets = isLocal
    ? [LOCAL_SPEECH_MODEL]
    : isDeepgram
      ? DEEPGRAM_SPEECH_MODEL_PRESETS
      : OPENAI_SPEECH_MODEL_PRESETS;
  const customBaseUrl = config.openai?.base_url ?? '';
  const isBaseUrlInvalid = isCustom && customBaseUrl.trim() !== '' && !isValidHttpUrl(customBaseUrl);

  const handleModelChange = useCallback(
    (value: string) => {
      if (isLocal) {
        handleLocalChange('model', value);
      } else if (isDeepgram) {
        handleDeepgramChange('model', value);
      } else {
        handleOpenAIChange('model', value);
      }
    },
    [handleDeepgramChange, handleOpenAIChange, isDeepgram]
  );

  const handleLanguageChange = useCallback(
    (value: string) => {
      if (isLocal) {
        handleLocalChange('language', value);
        return;
      }
      if (isDeepgram) {
        handleDeepgramChange('language', value);
        return;
      }
      // Whisper-family `zh` is script-ambiguous: pair the language with a
      // same-script prompt (undefined clears it for non-Chinese languages).
      updateConfig(
        (current) =>
          ({
            ...current,
            openai: {
              ...DEFAULT_SPEECH_TO_TEXT_CONFIG.openai,
              ...current.openai,
              language: value,
              prompt: getAutoTranscriptionPrompt(value),
            },
          }) as SpeechToTextConfig
      );
    },
    [handleDeepgramChange, handleLocalChange, isDeepgram, isLocal, updateConfig]
  );

  const handleApiKeyChange = useCallback(
    (value: string) => {
      if (isDeepgram) {
        handleDeepgramChange('api_key', value);
      } else {
        handleOpenAIChange('api_key', value);
      }
    },
    [handleDeepgramChange, handleOpenAIChange, isDeepgram]
  );

  return (
    <div
      className={embedded ? 'py-12px' : 'px-[12px] md:px-[32px] py-[24px] bg-2 rd-8px border border-arco-2'}
      data-testid='voice-input-settings'
    >
      <div className={`flex items-center justify-between gap-12px ${config.enabled ? 'mb-8px' : ''}`}>
        <div className='flex flex-col gap-4px'>
          <span className='text-14px text-t-primary'>{t('settings.speechToText')}</span>
          {!embedded && <span className='text-13px text-t-secondary'>{t('settings.speechToTextDescription')}</span>}
        </div>
        <Switch
          aria-label={t('settings.speechToText')}
          checked={config.enabled}
          onChange={(checked) => updateConfig((current) => ({ ...current, enabled: checked }))}
        />
      </div>

      {config.enabled && (
        <>
          <Divider className='mt-0px mb-20px' />

          <Form layout='horizontal' labelAlign='left' className='space-y-12px'>
            <Form.Item label={t('settings.speechToTextSource')}>
              <SynonSelect value={source} onChange={handleSourceChange}>
                <SynonSelect.Option value='local'>{t('settings.speechToTextSourceLocal')}</SynonSelect.Option>
                <SynonSelect.Option value='openai'>{t('settings.speechToTextSourceOpenAI')}</SynonSelect.Option>
                <SynonSelect.Option value='deepgram'>{t('settings.speechToTextSourceDeepgram')}</SynonSelect.Option>
                <SynonSelect.Option value='custom'>{t('settings.speechToTextSourceCustom')}</SynonSelect.Option>
              </SynonSelect>
            </Form.Item>

            {isLocal && <div className='text-13px text-t-secondary'>{t('settings.speechToTextLocalInfo')}</div>}

            {isCustom && (
              <Form.Item
                label={<FieldLabel labelKey='settings.speechToTextBaseUrl' requirement='required' />}
                validateStatus={isBaseUrlInvalid ? 'error' : undefined}
                help={isBaseUrlInvalid ? t('settings.speechToTextBaseUrlInvalid') : undefined}
              >
                <Input
                  value={customBaseUrl}
                  placeholder={t('settings.speechToTextBaseUrlPlaceholder')}
                  onChange={(value) => handleOpenAIChange('base_url', value)}
                />
              </Form.Item>
            )}

            {!isLocal && (
              <Form.Item
                label={
                  <FieldLabel labelKey='settings.speechToTextApiKey' requirement={isCustom ? 'optional' : 'required'} />
                }
              >
                <Input.Password value={activeApiKey} visibilityToggle onChange={handleApiKeyChange} />
              </Form.Item>
            )}

            <Form.Item label={t('settings.speechToTextModel')}>
              <SynonSelect
                value={activeModel || undefined}
                onChange={handleModelChange}
                allowCreate={isCustom && !isLocal}
                showSearch={isCustom && !isLocal}
                placeholder={isCustom ? t('settings.speechToTextModelPlaceholder') : undefined}
              >
                {buildModelOptions(modelPresets, activeModel).map((model) => {
                  return (
                    <SynonSelect.Option key={model} value={model}>
                      {model}
                      <span className='text-12px text-t-tertiary ml-8px'>{t('settings.speechToTextWholeBadge')}</span>
                    </SynonSelect.Option>
                  );
                })}
              </SynonSelect>
            </Form.Item>

            <Form.Item label={t('settings.speechToTextLanguage')}>
              <SynonSelect value={activeLanguage} onChange={handleLanguageChange}>
                {SPEECH_LANGUAGE_OPTIONS.map((option) => (
                  <SynonSelect.Option key={option.value || 'auto'} value={option.value}>
                    {option.label ?? t('settings.speechToTextLanguageAuto')}
                  </SynonSelect.Option>
                ))}
              </SynonSelect>
            </Form.Item>
          </Form>
          <SpeechTestPanel config={config} source={source} />
        </>
      )}
    </div>
  );
};

export default VoiceInputSection;

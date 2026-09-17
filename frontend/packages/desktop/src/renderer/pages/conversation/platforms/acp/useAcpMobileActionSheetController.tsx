import type { IConversationMcpStatus } from '@/common/config/storage';
import type { AcpModelInfo } from '@/common/types/platform/acpTypes';
import { isBackendHttpError } from '@/common/adapter/httpBridge';
import type { AcpDerivedOption, useAcpConfigOptions } from '@/renderer/hooks/synonBiomed/runtime/useAcpConfigOptions';
import {
  type MobileActionSheetEntry,
  type MobileActionSheetOption,
} from '@/renderer/components/chat/MobileActionSheet';
import type { SynonBiomedSessionOptions } from '@/renderer/services/synonBiomedSessionOptions';
import { Message } from '@arco-design/web-react';
import { Attention, Brain, MagicHat, SettingTwo, Shield } from '@icon-park/react';
import type { TFunction, i18n as I18n } from 'i18next';
import React, { useMemo, type Dispatch, type SetStateAction } from 'react';
import { configErrorMessageKey, safeErrorDiagnostic } from './acpSendBoxDiagnostics';

type UseAcpMobileActionSheetControllerArgs = {
  isMobile: boolean;
  runtimeMode: AcpDerivedOption | null;
  currentMode?: string;
  runtimeThoughtLevel: AcpDerivedOption | null;
  runtimeConfig: Pick<ReturnType<typeof useAcpConfigOptions>, 'setStatus'>;
  canSwitchModel: boolean;
  model_info: AcpModelInfo | null;
  isModelListLoading: boolean;
  modelListError: unknown;
  retryModelList: () => Promise<void>;
  selectModel: (modelId: string) => void;
  synonSessionOptions: SynonBiomedSessionOptions;
  sessionOptionsReady: boolean;
  setSynonSessionOptions: Dispatch<SetStateAction<SynonBiomedSessionOptions>>;
  updatePersistentSessionOption: (key: 'autoReview' | 'memory', value: boolean) => Promise<void>;
  mobileSpecialists: Array<{ id: string; label: string; agentId: string }>;
  mobileComputeProviders: Array<{ name: string; label: string }>;
  mobileEnabledCompute: string[];
  handleSessionComputeChange: (providerName: string, checked: boolean) => Promise<void>;
  handleSheetModeChange: (mode: string) => Promise<void>;
  handleThoughtLevelSetOption: (optionId: string, value: string) => Promise<boolean>;
  attachEntries: MobileActionSheetEntry[];
  loadedSkills: string[];
  loadedMcpStatuses: IConversationMcpStatus[];
  selectedSkillNames: string[];
  selectedMcpServerIds: string[];
  onSelectSkill: (name: string) => void;
  onSelectMcpServer: (server: IConversationMcpStatus) => void;
  t: TFunction;
  i18n: I18n;
};

export const useAcpMobileActionSheetController = ({
  isMobile,
  runtimeMode,
  currentMode,
  runtimeThoughtLevel,
  runtimeConfig,
  canSwitchModel,
  model_info,
  isModelListLoading,
  modelListError,
  retryModelList,
  selectModel,
  synonSessionOptions,
  sessionOptionsReady,
  setSynonSessionOptions,
  updatePersistentSessionOption,
  mobileSpecialists,
  mobileComputeProviders,
  mobileEnabledCompute,
  handleSessionComputeChange,
  handleSheetModeChange,
  handleThoughtLevelSetOption,
  attachEntries,
  loadedSkills,
  loadedMcpStatuses,
  selectedSkillNames,
  selectedMcpServerIds,
  onSelectSkill,
  onSelectMcpServer,
  t,
  i18n,
}: UseAcpMobileActionSheetControllerArgs): MobileActionSheetEntry[] => {
  const sheetEntries = useMemo<MobileActionSheetEntry[]>(() => {
    if (!isMobile) return [];

    const availableModes =
      runtimeMode?.options.map((item) => ({
        value: item.value,
        label: item.label,
        description: item.description ?? undefined,
      })) ?? [];
    const modeOptions: MobileActionSheetOption[] = availableModes.map((mode) => ({
      key: mode.value,
      label: i18n.exists(`agentMode.${mode.value}`) ? t(`agentMode.${mode.value}`) : mode.label,
      description: mode.description,
      active: (runtimeMode?.currentValue ?? currentMode) === mode.value,
    }));

    const modelOptions: MobileActionSheetOption[] = canSwitchModel
      ? (model_info?.available_models ?? []).map((model) => ({
          key: model.id,
          label: model.label || model.id,
          description: model.description,
          active: model_info?.current_model_id === model.id,
        }))
      : [];

    const currentModelLabel =
      model_info?.current_model_label || model_info?.current_model_id || t('conversation.welcome.useCliModel');
    const currentModeLabel = modeOptions.find((opt) => opt.active)?.label ?? t('agentMode.default');

    const entries: MobileActionSheetEntry[] = [];

    entries.push({
      key: 'session-options',
      disabled: !sessionOptionsReady,
      icon: <SettingTwo theme='outline' size='16' />,
      label: t('conversation.synonRuntime.sessionOptions.title'),
      meta: [
        synonSessionOptions.delegation ? t('conversation.synonRuntime.sessionOptions.delegation') : '',
        synonSessionOptions.autoReview ? t('conversation.synonRuntime.sendBox.reviewShort') : '',
      ]
        .filter(Boolean)
        .join(' · '),
      submenu: {
        title: t('conversation.synonRuntime.sessionOptions.title'),
        options: [
          {
            key: 'delegation',
            label: t('conversation.synonRuntime.sessionOptions.delegation'),
            description: t('conversation.synonRuntime.sessionOptions.delegationDescription'),
            active: synonSessionOptions.delegation,
          },
          {
            key: 'autoReview',
            label: t('conversation.synonRuntime.sessionOptions.autoReview'),
            description: t('conversation.synonRuntime.sessionOptions.autoReviewDescription'),
            active: synonSessionOptions.autoReview,
          },
          {
            key: 'memory',
            label: t('conversation.synonRuntime.sessionOptions.memory'),
            description: t('conversation.synonRuntime.sessionOptions.memoryDescription'),
            active: synonSessionOptions.memory,
          },
        ],
        onSelect: (key) => {
          if (!sessionOptionsReady) return;
          if (key === 'delegation') {
            setSynonSessionOptions((current) => ({
              ...current,
              delegation: !current.delegation,
            }));
          } else if (key === 'autoReview' || key === 'memory') {
            const nextValue = !synonSessionOptions[key];
            void updatePersistentSessionOption(key, nextValue).catch((error) => {
              console.error('[AcpSendBox] Failed to update session option', safeErrorDiagnostic(error));
              Message.error(t('conversation.synonRuntime.sessionOptions.updateFailed'));
            });
          }
        },
      },
    });

    if (mobileSpecialists.length > 0) {
      entries.push({
        key: 'specialist',
        icon: <Brain theme='outline' size='16' />,
        label: t('conversation.synonRuntime.sessionOptions.expert'),
        meta:
          synonSessionOptions.targetAgent === 'OPERON'
            ? t('conversation.synonRuntime.sessionOptions.none')
            : mobileSpecialists.find((item) => item.agentId === synonSessionOptions.targetAgent)?.label ||
              synonSessionOptions.targetAgent,
        submenu: {
          title: t('conversation.synonRuntime.sessionOptions.expert'),
          options: [
            {
              key: 'OPERON',
              label: t('conversation.synonRuntime.sessionOptions.none'),
              active: synonSessionOptions.targetAgent === 'OPERON',
            },
            ...mobileSpecialists
              .filter((specialist) => specialist.agentId !== 'OPERON')
              .map((specialist) => ({
                key: specialist.agentId,
                label: specialist.label,
                active: specialist.agentId === synonSessionOptions.targetAgent,
              })),
          ],
          onSelect: (agentId) =>
            setSynonSessionOptions((current) => ({
              ...current,
              targetAgent: agentId,
            })),
        },
      });
    }

    if (mobileComputeProviders.length > 0) {
      entries.push({
        key: 'session-compute',
        icon: <SettingTwo theme='outline' size='16' />,
        label: t('conversation.synonRuntime.sessionOptions.compute'),
        meta: String(mobileEnabledCompute.length),
        submenu: {
          title: t('conversation.synonRuntime.sessionOptions.compute'),
          options: mobileComputeProviders.map((provider) => ({
            key: provider.name,
            label: provider.label,
            active: mobileEnabledCompute.includes(provider.name),
          })),
          onSelect: (providerName) => {
            const checked = !mobileEnabledCompute.includes(providerName);
            void handleSessionComputeChange(providerName, checked);
          },
        },
      });
    }

    if (modelListError) {
      const authenticationError = isBackendHttpError(modelListError) && [401, 403].includes(modelListError.status);
      entries.push({
        key: 'model',
        icon: <Attention theme='outline' size='16' />,
        label: authenticationError
          ? t('conversation.welcome.modelListAuthError')
          : t('conversation.welcome.modelListUnavailable'),
        description: authenticationError
          ? t('conversation.welcome.modelListAuthErrorHint')
          : t('conversation.welcome.modelListUnavailableHint'),
        meta: t('common.retry'),
        onClick: () => void retryModelList().catch(() => {}),
      });
    } else if (isModelListLoading && !model_info) {
      entries.push({
        key: 'model',
        icon: <Brain theme='outline' size='16' />,
        label: t('conversation.welcome.modelListLoading'),
        disabled: true,
      });
    } else if (modelOptions.length > 0) {
      entries.push({
        key: 'model',
        icon: <Brain theme='outline' size='16' />,
        label: t('common.model'),
        meta: currentModelLabel,
        submenu: {
          title: t('common.model'),
          options: modelOptions,
          onSelect: (id) => selectModel(id),
        },
      });
    } else {
      // Keep the model identity visible on compact layouts even when the
      // provider exposes no switchable alternatives. Desktop already shows
      // this same read-only identity; omitting it from mobile made the two
      // layouts disagree about the active runtime.
      entries.push({
        key: 'model',
        icon: <Brain theme='outline' size='16' />,
        label: t('common.model'),
        meta: currentModelLabel,
        disabled: true,
      });
    }

    if (runtimeThoughtLevel) {
      entries.push({
        key: 'thought-level',
        icon: <Brain theme='outline' size='16' />,
        label: t('agent.thoughtLevel.label'),
        meta:
          runtimeThoughtLevel.options.find((item) => item.value === runtimeThoughtLevel.currentValue)?.label ||
          runtimeThoughtLevel.currentValue ||
          '',
        submenu: {
          title: t('agent.thoughtLevel.label'),
          options: runtimeThoughtLevel.options.map((item) => ({
            key: item.value,
            label: item.label,
            description: item.description ?? undefined,
            active: runtimeThoughtLevel.currentValue === item.value,
          })),
          onSelect: (value) => {
            void handleThoughtLevelSetOption(runtimeThoughtLevel.id, value)
              .then((applied) => {
                if (applied) Message.success(t('agent.thoughtLevel.switchSuccess'));
              })
              .catch((error) => Message.error(t(configErrorMessageKey(error))));
          },
        },
      });
    }

    if (modeOptions.length > 0) {
      entries.push({
        key: 'send-mode',
        icon: <Shield theme='outline' size='16' />,
        label: t('conversation.synonRuntime.sendBox.sendMode'),
        description: t('conversation.synonRuntime.sendBox.sendModeDescription'),
        meta: currentModeLabel,
        disabled: runtimeConfig.setStatus.state === 'setting',
        submenu: {
          title: t('conversation.synonRuntime.sendBox.sendMode'),
          options: modeOptions,
          onSelect: (key) => void handleSheetModeChange(key),
        },
      });
    }

    attachEntries.forEach((entry, idx) => {
      entries.push({
        ...entry,
        dividerBefore: idx === 0 ? entries.length > 0 : false,
      });
    });

    if (loadedSkills.length > 0) {
      const skillOptions: MobileActionSheetOption[] = loadedSkills.map((name) => ({
        key: name,
        label: name,
        active: selectedSkillNames.includes(name),
      }));
      entries.push({
        key: 'skills',
        icon: <MagicHat theme='outline' size='16' />,
        label: t('common.skills'),
        variant: 'muted',
        submenu: {
          title: t('common.skills'),
          options: skillOptions,
          onSelect: onSelectSkill,
        },
      });
    }

    if (loadedMcpStatuses.length > 0) {
      const mcpOptions: MobileActionSheetOption[] = loadedMcpStatuses.map((item) => ({
        key: item.id,
        label: item.name,
        active: selectedMcpServerIds.includes(item.id),
        description:
          item.status === 'loaded'
            ? undefined
            : item.reason
              ? `${t(`conversation.mcp.status.${item.status}` as const)} · ${item.reason}`
              : t(`conversation.mcp.status.${item.status}` as const),
      }));
      entries.push({
        key: 'mcp',
        icon: <Shield theme='outline' size='16' />,
        label: t('conversation.mcp.loaded'),
        variant: 'muted',
        submenu: {
          title: t('conversation.mcp.loaded'),
          options: mcpOptions,
          onSelect: (serverId) => {
            const server = loadedMcpStatuses.find((item) => item.id === serverId);
            if (server?.status === 'loaded') onSelectMcpServer(server);
          },
        },
      });
    }

    return entries;
  }, [
    attachEntries,
    canSwitchModel,
    currentMode,
    handleSessionComputeChange,
    handleSheetModeChange,
    handleThoughtLevelSetOption,
    i18n,
    isMobile,
    isModelListLoading,
    loadedMcpStatuses,
    loadedSkills,
    mobileComputeProviders,
    mobileEnabledCompute,
    mobileSpecialists,
    model_info,
    modelListError,
    onSelectMcpServer,
    onSelectSkill,
    retryModelList,
    runtimeConfig.setStatus.state,
    runtimeMode,
    runtimeThoughtLevel,
    selectModel,
    selectedMcpServerIds,
    selectedSkillNames,
    synonSessionOptions,
    sessionOptionsReady,
    t,
    updatePersistentSessionOption,
  ]);
  return sheetEntries;
};

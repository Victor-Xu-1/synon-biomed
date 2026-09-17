import { ipcBridge } from '@/common';
import type { AssistantEditorViewModel, AssistantListItem } from './types';
import { useManagedAgentRuntimeCatalog } from '@/renderer/hooks/synonBiomed/runtime/useManagedAgents';
import {
  buildAgentRuntimeModeState,
  buildAgentRuntimeModelInfo,
  buildAgentRuntimeThoughtLevelOption,
} from '@/renderer/utils/synonBiomed/runtime/runtimeCatalog';
import type { AgentModeOption } from '@/renderer/utils/synonBiomed/runtime/runtimeTypes';
import { useAgentLogos, resolveAgentAvatar } from '@/renderer/utils/synonBiomed/runtime/runtimeLogo';
import SynonBiomedAvatar, { isRobotAvatar } from '@/renderer/components/synonBiomed/SynonBiomedAvatar';
import type { AvailableBackend } from './types';
import { Avatar, Select, Tag } from '@arco-design/web-react';
import React, { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import IdentitySection from './editor/IdentitySection';
import PromptsSection from './editor/PromptsSection';
import DefaultsSection from './editor/DefaultsSection';
import RulesSection from './editor/RulesSection';

export type AssistantEditorSectionsProps = {
  editor: AssistantEditorViewModel;
  activeAssistant: AssistantListItem | null;
};

const getEditorSelectPopupContainer = (node: HTMLElement) =>
  node.closest('[data-editor-popup-root]') ?? node.parentElement ?? document.body;

const readonlySelectionSummary = (items: string[], emptyLabel: string) =>
  items.length > 0 ? items.join(', ') : emptyLabel;

const AssistantEditorSections: React.FC<AssistantEditorSectionsProps> = ({ editor, activeAssistant }) => {
  const { t, i18n } = useTranslation();
  const localeKey = i18n.language;
  const managedAgentRuntimeCatalog = useManagedAgentRuntimeCatalog();
  const agentLogos = useAgentLogos();
  const [rulesExpanded, setRulesExpanded] = useState(false);
  const [addingPrompt, setAddingPrompt] = useState(false);
  const [newPromptDraft, setNewPromptDraft] = useState('');
  const [editingPromptIndex, setEditingPromptIndex] = useState<number | null>(null);
  const [editingPromptDraft, setEditingPromptDraft] = useState('');
  const { isCreating, profile, agent, prompts, defaults, rules, skills } = editor;
  const editName = profile.name;
  const setEditName = profile.setName;
  const editDescription = profile.description;
  const setEditDescription = profile.setDescription;
  const editAvatar = profile.avatar;
  const setEditAvatar = profile.setAvatar;
  const setEditAvatarPreview = profile.setAvatarPreview;
  const editAvatarImage = profile.avatarImage;
  const editAgent = agent.value;
  const setEditAgent = agent.setValue;
  const availableBackends = agent.availableBackends;

  // Render the agent's own avatar (icon/logo) for a dropdown row. Falls back to
  // the shared Synon Biomed avatar when the catalog has no explicit logo.
  const renderAgentAvatar = (option: AvailableBackend) => {
    const avatar = resolveAgentAvatar(agentLogos, {
      icon: option.icon,
      backend: option.runtimeKey,
      isExtension: option.isExtension,
    });
    return (
      <Avatar
        size={20}
        shape='square'
        style={{ backgroundColor: avatar.kind === 'image' ? 'transparent' : 'var(--color-fill-2)' }}
      >
        {avatar.kind === 'image' ? (
          <img src={avatar.value} alt={option.name} className='h-full w-full object-contain' />
        ) : avatar.kind === 'emoji' && !isRobotAvatar(avatar.value) ? (
          <span className='text-14px leading-none'>{avatar.value}</span>
        ) : (
          <SynonBiomedAvatar size={14} />
        )}
      </Avatar>
    );
  };
  const editRecommendedPromptsText = prompts.text;
  const setEditRecommendedPromptsText = prompts.setText;
  const defaultModelMode = defaults.model.mode;
  const setDefaultModelMode = defaults.model.setMode;
  const defaultModelValue = defaults.model.value;
  const setDefaultModelValue = defaults.model.setValue;
  const defaultPermissionMode = defaults.permission.mode;
  const setDefaultPermissionMode = defaults.permission.setMode;
  const defaultPermissionValue = defaults.permission.value;
  const setDefaultPermissionValue = defaults.permission.setValue;
  const defaultThoughtLevelMode = defaults.thoughtLevel.mode;
  const setDefaultThoughtLevelMode = defaults.thoughtLevel.setMode;
  const defaultThoughtLevelValue = defaults.thoughtLevel.value;
  const setDefaultThoughtLevelValue = defaults.thoughtLevel.setValue;
  const defaultSkillsMode = defaults.skills.mode;
  const setDefaultSkillsMode = defaults.skills.setMode;
  const defaultMcpMode = defaults.mcps.mode;
  const setDefaultMcpMode = defaults.mcps.setMode;
  const availableMcpServers = defaults.mcps.availableServers;
  const selectedMcpIds = defaults.mcps.selectedIds;
  const setSelectedMcpIds = defaults.mcps.setSelectedIds;
  const editContext = rules.content;
  const setEditContext = rules.setContent;
  const promptViewMode = rules.viewMode;
  const setPromptViewMode = rules.setViewMode;
  const availableSkills = skills.availableSkills;
  const selectedSkills = skills.selectedSkills;
  const setSelectedSkills = skills.setSelectedSkills;
  const pendingSkills = skills.pendingSkills;
  const builtinAutoSkills = skills.builtinAutoSkills;
  const disabledBuiltinSkills = skills.disabledBuiltinSkills;
  const setDisabledBuiltinSkills = skills.setDisabledBuiltinSkills;
  const isBuiltin = activeAssistant?.source === 'builtin';
  const isReadOnlyAssistant = isBuiltin;
  const isIdentityLocked = isBuiltin;
  const isDescriptionReadOnly = isBuiltin;
  const showSkills = isCreating || activeAssistant !== null;
  const currentBackend = availableBackends.find((option) => option.id === editAgent);
  const currentAgentRuntimeCatalog = useMemo(
    () =>
      currentBackend
        ? managedAgentRuntimeCatalog.find((agentMetadata) => agentMetadata.id === currentBackend.id)
        : null,
    [currentBackend, managedAgentRuntimeCatalog]
  );
  const currentAgentRuntimeModelInfo = useMemo(
    () => buildAgentRuntimeModelInfo(currentAgentRuntimeCatalog),
    [currentAgentRuntimeCatalog]
  );
  const modelOptions = useMemo(() => {
    if (currentAgentRuntimeModelInfo && currentAgentRuntimeModelInfo.available_models.length > 0) {
      return currentAgentRuntimeModelInfo.available_models.map((model) => ({
        key: `${editAgent}-${model.id}`,
        value: model.id,
        label: model.label,
        description: model.description,
      }));
    }

    if (currentBackend && currentBackend.modelOptions.length > 0) {
      return currentBackend.modelOptions.map((model: { value: string; label: string; description?: string }) => ({
        key: `${editAgent}-${model.value}`,
        value: model.value,
        label: model.label,
        description: model.description,
      }));
    }

    return [];
  }, [currentAgentRuntimeModelInfo, currentBackend, editAgent]);
  const permissionOptions = useMemo<AgentModeOption[]>(
    () =>
      buildAgentRuntimeModeState(currentAgentRuntimeCatalog).options.map((option) => {
        const key = `agentMode.${option.value}`;
        return {
          value: option.value,
          label: i18n.exists(key) ? t(key) : option.label,
          description: option.description,
        };
      }),
    [currentAgentRuntimeCatalog, i18n, localeKey, t]
  );
  const thoughtLevelOption = useMemo(
    () => buildAgentRuntimeThoughtLevelOption(currentAgentRuntimeCatalog),
    [currentAgentRuntimeCatalog]
  );
  const thoughtLevelOptions = thoughtLevelOption?.options ?? [];
  const recommendedPromptItems = useMemo(
    () =>
      editRecommendedPromptsText
        .split('\n')
        .map((item) => item.trim())
        .filter(Boolean),
    [editRecommendedPromptsText]
  );
  const readOnlyLabel = t('common.readOnly');
  const rulesContainerHeight = rulesExpanded ? '440px' : promptViewMode === 'edit' ? '280px' : '240px';
  const autoSkillNames = builtinAutoSkills.map((skill) => skill.name);
  const autoDefaultOptionLabel = t('settings.assistantSelectAutoRememberLastUsed');
  const selectedItemsLabel = (count: number) =>
    t('settings.assistantSelectedCount', {
      count,
    });
  const handlePickAvatarImage = async () => {
    try {
      const selectedFiles = await ipcBridge.dialog.showOpen.invoke({
        properties: ['openFile'],
        filters: [
          {
            name: t('settings.assistantAvatarImageFiles'),
            extensions: ['png', 'jpg', 'jpeg', 'webp', 'gif', 'svg'],
          },
        ],
      });
      const nextAvatar = selectedFiles?.[0];
      if (nextAvatar) {
        const previewBase64 = await ipcBridge.fs.getImageBase64.invoke({ path: nextAvatar });
        setEditAvatar(nextAvatar);
        setEditAvatarPreview(previewBase64 || undefined);
      }
    } catch (error) {
      console.error('Failed to pick assistant avatar image:', error);
    }
  };
  const editableSkillOptions = useMemo(() => {
    const optionMap = new Map<string, { value: string; label: string; isAuto?: boolean; disabled?: boolean }>();

    pendingSkills.forEach((skill) => {
      optionMap.set(skill.name, { value: skill.name, label: skill.name });
    });

    availableSkills.forEach((skill) => {
      optionMap.set(skill.name, {
        value: skill.name,
        label: skill.name,
      });
    });

    builtinAutoSkills.forEach((skill) => {
      optionMap.set(skill.name, {
        value: skill.name,
        label: skill.name,
        isAuto: true,
      });
    });

    return Array.from(optionMap.values());
  }, [availableSkills, builtinAutoSkills, pendingSkills, t]);
  const selectedSkillValues = useMemo(
    () =>
      Array.from(
        new Set([
          ...selectedSkills,
          ...builtinAutoSkills
            .filter((skill) => !disabledBuiltinSkills.includes(skill.name))
            .map((skill) => skill.name),
        ])
      ),
    [builtinAutoSkills, disabledBuiltinSkills, selectedSkills]
  );

  const applyPromptItems = (items: string[]) => {
    setEditRecommendedPromptsText(items.join('\n'));
  };

  const handleBeginPromptEdit = (index: number) => {
    setEditingPromptIndex(index);
    setEditingPromptDraft(recommendedPromptItems[index] ?? '');
  };

  const handleSavePromptEdit = () => {
    if (editingPromptIndex === null) return;
    const trimmed = editingPromptDraft.trim();
    if (!trimmed) return;
    const nextItems = [...recommendedPromptItems];
    nextItems[editingPromptIndex] = trimmed;
    applyPromptItems(nextItems);
    setEditingPromptIndex(null);
    setEditingPromptDraft('');
  };

  const handleDeletePrompt = (index: number) => {
    applyPromptItems(recommendedPromptItems.filter((_, promptIndex) => promptIndex !== index));
    if (editingPromptIndex === index) {
      setEditingPromptIndex(null);
      setEditingPromptDraft('');
    }
  };

  const handleAddPrompt = () => {
    const trimmed = newPromptDraft.trim();
    if (!trimmed) return;
    applyPromptItems([...recommendedPromptItems, trimmed]);
    setAddingPrompt(false);
    setNewPromptDraft('');
  };

  const handleSkillSelectionChange = (values: string[]) => {
    const nextSelected = values.filter((value) => !autoSkillNames.includes(value));
    const nextDisabledAuto = autoSkillNames.filter((skillName) => !values.includes(skillName));
    setSelectedSkills(nextSelected);
    setDisabledBuiltinSkills(nextDisabledAuto);
  };

  const renderAvatarPreview = () => {
    if (editAvatarImage) {
      return (
        <img
          src={editAvatarImage}
          alt=''
          className='h-full w-full rounded-inherit object-cover'
          style={{ display: 'block' }}
        />
      );
    }

    if (editAvatar && !isRobotAvatar(editAvatar)) {
      return <span className='text-20px'>{editAvatar}</span>;
    }

    return <SynonBiomedAvatar size={20} />;
  };

  return (
    <div className='flex flex-col gap-16px pb-24px'>
      <IdentitySection
        isIdentityLocked={isIdentityLocked}
        isDescriptionReadOnly={isDescriptionReadOnly}
        editAvatar={editAvatar}
        editName={editName}
        setEditName={setEditName}
        editDescription={editDescription}
        setEditDescription={setEditDescription}
        setEditAvatar={setEditAvatar}
        setEditAvatarPreview={setEditAvatarPreview}
        builtinAvatarOptions={profile.builtinAvatarOptions}
        onPickAvatarImage={() => void handlePickAvatarImage()}
        renderAvatarPreview={renderAvatarPreview}
        readOnlyLabel={readOnlyLabel}
      />

      <PromptsSection
        isReadOnly={isReadOnlyAssistant}
        recommendedPromptItems={recommendedPromptItems}
        addingPrompt={addingPrompt}
        setAddingPrompt={setAddingPrompt}
        newPromptDraft={newPromptDraft}
        setNewPromptDraft={setNewPromptDraft}
        editingPromptIndex={editingPromptIndex}
        setEditingPromptIndex={setEditingPromptIndex}
        editingPromptDraft={editingPromptDraft}
        setEditingPromptDraft={setEditingPromptDraft}
        onAddPrompt={handleAddPrompt}
        onBeginPromptEdit={handleBeginPromptEdit}
        onSavePromptEdit={handleSavePromptEdit}
        onDeletePrompt={handleDeletePrompt}
        readOnlyLabel={readOnlyLabel}
      />

      <div
        className='rounded-12px border border-arco-2 bg-2 px-[12px] py-[16px] md:rounded-16px md:px-[24px] md:py-[20px]'
        data-testid='assistant-card-engine'
      >
        <div className='mb-12px flex items-center gap-8px'>
          <div className='text-14px font-500 text-t-primary'>{t('settings.assistantEngineSection')}</div>
          <span className='rounded-6px border border-warning-8 bg-warning-8 px-8px py-2px text-10px font-600 text-white'>
            {t('settings.assistantOnlyNewConversation')}
          </span>
        </div>
        <div className='flex items-center gap-12px'>
          <div className='w-86px flex-shrink-0 text-13px text-t-secondary'>
            <span className='flex items-center gap-6px leading-none'>
              <span className='inline-flex shrink-0 items-center text-t-tertiary'>
                <SynonBiomedAvatar size={14} />
              </span>
              <span>{t('settings.assistantMainAgent')}</span>
            </span>
          </div>
          <div className='min-w-0 flex-1'>
            <Select
              className='w-full'
              getPopupContainer={getEditorSelectPopupContainer}
              value={editAgent}
              onChange={(value) => setEditAgent(value as string)}
              disabled={isIdentityLocked}
              data-testid='select-assistant-agent'
              renderFormat={(_option, value) => {
                const selected = availableBackends.find((item) => item.id === value);
                if (!selected) return (value as string) ?? '';
                return (
                  <span className='flex items-center gap-8px'>
                    {renderAgentAvatar(selected)}
                    <span className='truncate'>{selected.name}</span>
                  </span>
                );
              }}
            >
              {availableBackends.map((option) => (
                <Select.Option key={option.id} value={option.id}>
                  <span className='flex items-center gap-8px'>
                    {renderAgentAvatar(option)}
                    <span className='truncate'>{option.name}</span>
                    {option.isExtension ? (
                      <Tag size='small' color='arcoblue'>
                        {t('settings.assistantExtensionBadge')}
                      </Tag>
                    ) : null}
                  </span>
                </Select.Option>
              ))}
            </Select>
            <div className='mt-6px text-11px text-t-tertiary'>{t('settings.assistantEngineAffectsDefaults')}</div>
          </div>
        </div>
      </div>

      <DefaultsSection
        key={`assistant-defaults-${localeKey}-${editAgent}`}
        localeKey={localeKey}
        isReadOnlyAssistant={isReadOnlyAssistant}
        isCreating={isCreating}
        showSkills={showSkills}
        defaultModelMode={defaultModelMode}
        setDefaultModelMode={setDefaultModelMode}
        defaultModelValue={defaultModelValue}
        setDefaultModelValue={setDefaultModelValue}
        defaultPermissionMode={defaultPermissionMode}
        setDefaultPermissionMode={setDefaultPermissionMode}
        defaultPermissionValue={defaultPermissionValue}
        setDefaultPermissionValue={setDefaultPermissionValue}
        defaultThoughtLevelMode={defaultThoughtLevelMode}
        setDefaultThoughtLevelMode={setDefaultThoughtLevelMode}
        defaultThoughtLevelValue={defaultThoughtLevelValue}
        setDefaultThoughtLevelValue={setDefaultThoughtLevelValue}
        defaultSkillsMode={defaultSkillsMode}
        setDefaultSkillsMode={setDefaultSkillsMode}
        defaultMcpMode={defaultMcpMode}
        setDefaultMcpMode={setDefaultMcpMode}
        modelOptions={modelOptions}
        permissionOptions={permissionOptions}
        showThoughtLevelDefault={thoughtLevelOption !== null}
        thoughtLevelOptions={thoughtLevelOptions}
        editableSkillOptions={editableSkillOptions}
        selectedSkillValues={selectedSkillValues}
        enabledMcpServers={availableMcpServers}
        selectedMcpIds={selectedMcpIds}
        setSelectedMcpIds={setSelectedMcpIds}
        handleSkillSelectionChange={handleSkillSelectionChange}
        selectedItemsLabel={selectedItemsLabel}
        autoDefaultOptionLabel={autoDefaultOptionLabel}
        readonlySelectionSummary={readonlySelectionSummary}
      />

      <RulesSection
        isReadOnly={isReadOnlyAssistant}
        promptViewMode={promptViewMode}
        setPromptViewMode={setPromptViewMode}
        rulesExpanded={rulesExpanded}
        setRulesExpanded={setRulesExpanded}
        rulesContainerHeight={rulesContainerHeight}
        editContext={editContext}
        setEditContext={setEditContext}
        readOnlyLabel={readOnlyLabel}
      />
    </div>
  );
};

export default AssistantEditorSections;

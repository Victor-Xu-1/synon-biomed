import type { AssistantDetail } from '@/common/types/agent/assistantTypes';

export type ResolvedGuidAssistantDefaults = {
  modelId?: string;
  permissionMode?: string;
  thoughtLevel?: string;
  skillMode: 'auto' | 'fixed';
  skillIds: string[];
  disabledBuiltinSkillIds: string[];
  mcpIds?: string[];
};

export const resolveGuidAssistantDefaults = (
  detail: AssistantDetail | null | undefined
): ResolvedGuidAssistantDefaults => {
  if (!detail) {
    return {
      modelId: undefined,
      permissionMode: undefined,
      thoughtLevel: undefined,
      skillMode: 'auto',
      skillIds: [],
      disabledBuiltinSkillIds: [],
      mcpIds: undefined,
    };
  }

  const modelId =
    detail.defaults.model.mode === 'fixed'
      ? detail.defaults.model.value
      : detail.defaults.model.mode === 'auto'
        ? detail.preferences.last_model_id
        : undefined;

  const permissionMode =
    detail.defaults.permission.mode === 'fixed'
      ? detail.defaults.permission.value
      : detail.defaults.permission.mode === 'auto'
        ? detail.preferences.last_permission_value
        : undefined;

  const thoughtLevelDefault = detail.defaults.thought_level ?? { mode: 'auto' };
  const thoughtLevel =
    thoughtLevelDefault.mode === 'fixed'
      ? thoughtLevelDefault.value
      : thoughtLevelDefault.mode === 'auto'
        ? detail.preferences.last_thought_level_value
        : undefined;

  const skillMode: ResolvedGuidAssistantDefaults['skillMode'] =
    detail.defaults.skills.mode === 'auto' ? 'auto' : 'fixed';
  const skillIds =
    skillMode === 'fixed'
      ? (detail.defaults.skills.value ?? [])
      : skillMode === 'auto'
        ? (detail.preferences.last_skill_ids ?? [])
        : [];

  const disabledBuiltinSkillIds =
    skillMode === 'fixed'
      ? (detail.capabilities.default_disabled_builtin_skill_ids ?? [])
      : skillMode === 'auto'
        ? (detail.preferences.last_disabled_builtin_skill_ids ?? [])
        : [];

  const mcpIds =
    detail.defaults.mcps.mode === 'fixed'
      ? (detail.defaults.mcps.value ?? [])
      : detail.defaults.mcps.mode === 'auto'
        ? detail.preferences.last_mcp_ids?.length
          ? detail.preferences.last_mcp_ids
          : undefined
        : undefined;

  return {
    modelId: modelId || undefined,
    permissionMode: permissionMode || undefined,
    thoughtLevel: thoughtLevel || undefined,
    skillMode,
    skillIds,
    disabledBuiltinSkillIds,
    mcpIds,
  };
};

/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

export type FeedbackModule = {
  readonly i18nKey: string;
  readonly descriptionI18nKey: string;
  readonly tag: string;
};

export const FEEDBACK_MODULES = [
  {
    i18nKey: 'settings.bugReportModuleSynonBiomed',
    descriptionI18nKey: 'settings.bugReportModuleSynonBiomedDescription',
    tag: 'synon-biomed',
  },
  {
    i18nKey: 'settings.bugReportModuleLlmConfig',
    descriptionI18nKey: 'settings.bugReportModuleLlmConfigDescription',
    tag: 'model-auth',
  },
  {
    i18nKey: 'settings.bugReportModuleMcp',
    descriptionI18nKey: 'settings.bugReportModuleMcpDescription',
    tag: 'mcp-tools',
  },
  {
    i18nKey: 'settings.bugReportModuleSkills',
    descriptionI18nKey: 'settings.bugReportModuleSkillsDescription',
    tag: 'skills',
  },
  {
    i18nKey: 'settings.bugReportModuleChat',
    descriptionI18nKey: 'settings.bugReportModuleChatDescription',
    tag: 'conversation-session',
  },
  {
    i18nKey: 'settings.bugReportModuleSession',
    descriptionI18nKey: 'settings.bugReportModuleSessionDescription',
    tag: 'search-history',
  },
  {
    i18nKey: 'settings.bugReportModuleWorkspace',
    descriptionI18nKey: 'settings.bugReportModuleWorkspaceDescription',
    tag: 'workspace-preview',
  },
  {
    i18nKey: 'settings.bugReportModuleScheduledTask',
    descriptionI18nKey: 'settings.bugReportModuleScheduledTaskDescription',
    tag: 'scheduled-task',
  },
  {
    i18nKey: 'settings.bugReportModuleDisplaySettings',
    descriptionI18nKey: 'settings.bugReportModuleDisplaySettingsDescription',
    tag: 'display-desktop',
  },
  {
    i18nKey: 'settings.bugReportModuleSystemSettings',
    descriptionI18nKey: 'settings.bugReportModuleSystemSettingsDescription',
    tag: 'system-settings',
  },
  {
    i18nKey: 'settings.bugReportModuleOther',
    descriptionI18nKey: 'settings.bugReportModuleOtherDescription',
    tag: 'other',
  },
] as const satisfies readonly FeedbackModule[];

export type FeedbackModuleTag = (typeof FEEDBACK_MODULES)[number]['tag'];

export type FeedbackDiagnosticsProfile = FeedbackModuleTag | 'global-summary';

export type FeedbackDiagnosticsExplicitContext = {
  agentId?: string;
  assistantDefinitionId?: string;
  assistantId?: string;
  conversationId?: string;
  mcpServerId?: string;
  mcpServerName?: string;
  messageId?: string;
  modelId?: string;
  msgId?: string;
  providerId?: string;
  routePath?: string;
  slotId?: string;
};

export type FeedbackDiagnosticsContextInput = {
  explicitContext?: FeedbackDiagnosticsExplicitContext;
  explicitProfiles?: FeedbackDiagnosticsProfile[];
  routeAtOpen?: string;
  routeAtSubmit?: string;
  selectedModule?: string;
};

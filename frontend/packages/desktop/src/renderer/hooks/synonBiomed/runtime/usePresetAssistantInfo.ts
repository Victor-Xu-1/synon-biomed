/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import type { TChatConversation } from '@/common/config/storage';
import { ipcBridge } from '@/common';
import { assistantRuntimeKey, type Assistant } from '@/common/types/agent/assistantTypes';
import { resolveLocaleKey } from '@/common/utils';
import { isLikelyLocalFilePath, resolveAssistantAvatar } from '@/renderer/utils/model/assistantAvatar';
import useSWR from 'swr';
export interface PresetAssistantInfo {
  name: string;
  logo: string;
  isEmoji: boolean;
  isFallback?: boolean;
  backend?: string;
  assistantId?: string;
}

/**
 * 从 conversation extra 中解析预设助手 ID
 * Resolve preset assistant ID from conversation extra
 *
 * 处理向后兼容：
 * - preset_assistant_id: 新格式 'builtin-xxx'
 * - enabled_skills: Gemini Cowork 会话的旧格式
 */
/**
 * Resolve the explicit assistant identity stored on a conversation.
 */
export function resolveAssistantConfigId(conversation: TChatConversation): string | null {
  const extra = conversation.extra as {
    assistant_id?: unknown;
    preset_assistant_id?: unknown;
  };
  const assistant_id = typeof extra?.assistant_id === 'string' ? extra.assistant_id.trim() : '';
  const preset_assistant_id = typeof extra?.preset_assistant_id === 'string' ? extra.preset_assistant_id.trim() : '';
  return assistant_id || preset_assistant_id || null;
}

function collectExplicitAssistantIdentityCandidates(conversation: TChatConversation): string[] {
  const extra = conversation.extra as {
    assistant_id?: unknown;
    preset_assistant_id?: unknown;
  };
  const assistant_id = typeof extra?.assistant_id === 'string' ? extra.assistant_id.trim() : '';
  const preset_assistant_id = typeof extra?.preset_assistant_id === 'string' ? extra.preset_assistant_id.trim() : '';
  return [assistant_id, preset_assistant_id].filter(
    (value, index, values) => Boolean(value) && values.indexOf(value) === index
  );
}

function normalizeAssistantIdentityCandidate(value: string): string {
  return value.replace('builtin-', '');
}

function findAssistantByIdentityCandidates(
  assistants: Assistant[] | null | undefined,
  candidates: string[]
): Assistant | undefined {
  if (!assistants?.length || !candidates.length) return undefined;

  for (const rawCandidate of candidates) {
    const candidate = normalizeAssistantIdentityCandidate(rawCandidate);
    const match = assistants.find((assistant) => {
      const ids = new Set([assistant.id, `builtin-${assistant.id}`, `ext-${assistant.id}`]);
      return ids.has(rawCandidate) || ids.has(candidate);
    });
    if (match) return match;
  }

  return undefined;
}

function hasExplicitAssistantIdentity(conversation: TChatConversation): boolean {
  const extra = conversation.extra as {
    assistant_id?: unknown;
    preset_assistant_id?: unknown;
  };
  const assistant_id = typeof extra?.assistant_id === 'string' ? extra.assistant_id.trim() : '';
  const preset_assistant_id = typeof extra?.preset_assistant_id === 'string' ? extra.preset_assistant_id.trim() : '';
  return Boolean(assistant_id || preset_assistant_id);
}

/**
 * 规范化头像：支持 emoji / 内置 svg / 扩展资源 URL
 * Normalize avatar to either emoji text or a renderable image URL
 */
function normalizeAvatar(avatar: string | undefined): { logo: string; isEmoji: boolean; isFallback?: boolean } {
  const resolved = resolveAssistantAvatar(avatar);
  if (resolved.kind === 'image') {
    return { logo: resolved.value, isEmoji: false };
  }

  if (resolved.kind === 'fallback') {
    return { logo: '', isEmoji: false, isFallback: true };
  }

  return { logo: resolved.value, isEmoji: true };
}

function normalizeAssistantLabel(value: string | undefined): string {
  return (value || '')
    .normalize('NFKC')
    .replace(/[*_`>#]/g, ' ')
    .replace(/\s+/g, ' ')
    .trim()
    .toLowerCase();
}

function extractLegacyPresetPayload(conversation: TChatConversation): {
  rules: string;
  enabled_skills: string[];
  hasPayload: boolean;
} {
  const extra = conversation.extra as {
    preset_context?: unknown;
    preset_rules?: unknown;
    enabled_skills?: unknown;
  };
  const preset_context = typeof extra?.preset_context === 'string' ? extra.preset_context.trim() : '';
  const preset_rules = typeof extra?.preset_rules === 'string' ? extra.preset_rules.trim() : '';
  const enabled_skills = Array.isArray(extra?.enabled_skills)
    ? extra.enabled_skills.filter((skill): skill is string => typeof skill === 'string' && skill.trim().length > 0)
    : [];

  return {
    rules: preset_context || preset_rules,
    enabled_skills,
    hasPayload: Boolean(preset_context || preset_rules || enabled_skills.length > 0),
  };
}

function extractAssistantNameFromRules(rules: string): string | null {
  const trimmed = rules.trim();
  if (!trimmed) return null;

  const headingMatch = trimmed.match(/^\s*#\s+(.+?)\s*$/m);
  if (headingMatch?.[1]) return headingMatch[1].trim();

  const zhAssistantMatch = trimmed.match(/你是\s+\*\*([^*]+)\*\*/);
  if (zhAssistantMatch?.[1]) return zhAssistantMatch[1].trim();

  const enAssistantMatch = trimmed.match(/you are\s+\*\*([^*]+)\*\*/i);
  if (enAssistantMatch?.[1]) return enAssistantMatch[1].trim();

  return null;
}

function matchesAssistantName(candidate: string | null, names: Array<string | undefined>): boolean {
  if (!candidate) return false;
  const normalizedCandidate = normalizeAssistantLabel(candidate);
  if (!normalizedCandidate) return false;
  return names.some((name) => normalizeAssistantLabel(name) === normalizedCandidate);
}

function hasMatchingEnabledSkills(candidateSkills: string[] | undefined, enabled_skills: string[]): boolean {
  if (!candidateSkills?.length || !enabled_skills.length) return false;
  const normalizedCandidate = [...candidateSkills].map((skill) => skill.trim()).toSorted();
  const normalizedEnabled = [...enabled_skills].map((skill) => skill.trim()).toSorted();
  if (normalizedCandidate.length !== normalizedEnabled.length) return false;
  return normalizedCandidate.every((skill, index) => skill === normalizedEnabled[index]);
}

/**
 * Build assistant info from a backend-provided Assistant record.
 */
function buildPresetInfoFromAssistant(assistant: Assistant, locale: string): PresetAssistantInfo {
  const localeKey = resolveLocaleKey(locale);
  const name = assistant.name_i18n?.[localeKey] || assistant.name_i18n?.[locale] || assistant.name || assistant.id;
  const avatar = typeof assistant.avatar === 'string' ? assistant.avatar : '';
  const normalized = normalizeAvatar(avatar);
  return {
    name,
    logo: normalized.logo,
    isEmoji: normalized.isEmoji,
    isFallback: normalized.isFallback,
    backend: assistantRuntimeKey(assistant) || undefined,
    assistantId: assistant.id,
  };
}

function buildPresetInfoFromConversationAssistant(
  assistant: NonNullable<TChatConversation['assistant']>
): PresetAssistantInfo {
  const normalized = normalizeAvatar(assistant.avatar);
  return {
    name: assistant.name,
    logo: normalized.logo,
    isEmoji: normalized.isEmoji,
    isFallback: normalized.isFallback,
    backend: assistant.backend,
    assistantId: assistant.id,
  };
}

function inferLegacyAssistantInfo(
  conversation: TChatConversation,
  locale: string,
  assistants?: Assistant[] | null
): PresetAssistantInfo | null {
  const { rules, enabled_skills } = extractLegacyPresetPayload(conversation);
  const extractedName = extractAssistantNameFromRules(rules);

  const byName = assistants?.find((assistant) =>
    matchesAssistantName(extractedName, [
      assistant.id,
      assistant.name,
      assistant.name_i18n?.['zh-CN'],
      assistant.name_i18n?.['en-US'],
    ])
  );
  if (byName) return buildPresetInfoFromAssistant(byName, locale);

  const bySkills = assistants?.filter((assistant) =>
    hasMatchingEnabledSkills(assistant.enabled_skills, enabled_skills)
  );
  if (bySkills?.length === 1) return buildPresetInfoFromAssistant(bySkills[0], locale);

  return null;
}

/**
 * 获取预设助手信息的 Hook
 * Hook to get preset assistant info from conversation
 *
 * @param conversation - 会话对象 / Conversation object
 * @returns 预设助手信息或 null / Preset assistant info or null
 */
export function usePresetAssistantInfo(conversation: TChatConversation | undefined): {
  info: PresetAssistantInfo | null;
  isLoading: boolean;
} {
  const { i18n } = useTranslation();
  // Merged assistant catalog (builtin + user) from backend
  const { data: assistantsList, isLoading: isLoadingAssistants } = useSWR('assistants', () =>
    ipcBridge.assistants.list.invoke().catch(() => [] as Assistant[])
  );

  return useMemo(() => {
    if (!conversation) return { info: null, isLoading: false };

    const locale = i18n.language || 'en-US';

    if (conversation.assistant) {
      const snapshotAvatar =
        typeof conversation.assistant.avatar === 'string' ? conversation.assistant.avatar.trim() : '';
      if (snapshotAvatar && isLikelyLocalFilePath(snapshotAvatar)) {
        const snapshotCandidates = [
          conversation.assistant.id,
          ...collectExplicitAssistantIdentityCandidates(conversation),
        ].filter((value, index, values) => Boolean(value) && values.indexOf(value) === index);
        const catalogAssistant = findAssistantByIdentityCandidates(assistantsList, snapshotCandidates);
        if (catalogAssistant) {
          return { info: buildPresetInfoFromAssistant(catalogAssistant, locale), isLoading: false };
        }
        if (isLoadingAssistants) return { info: null as PresetAssistantInfo | null, isLoading: true };
      }

      return {
        info: buildPresetInfoFromConversationAssistant(conversation.assistant),
        isLoading: false,
      };
    }

    const explicitAssistantCandidates = collectExplicitAssistantIdentityCandidates(conversation);
    const hasExplicitAssistantId = hasExplicitAssistantIdentity(conversation);
    const assistantMatch = hasExplicitAssistantId
      ? findAssistantByIdentityCandidates(assistantsList, explicitAssistantCandidates)
      : undefined;

    if (assistantMatch) {
      return { info: buildPresetInfoFromAssistant(assistantMatch, locale), isLoading: false };
    }

    const inferredInfo = inferLegacyAssistantInfo(conversation, locale, assistantsList);
    if (inferredInfo) return { info: inferredInfo, isLoading: false };

    const { hasPayload } = extractLegacyPresetPayload(conversation);
    if ((hasPayload || explicitAssistantCandidates.length > 0) && isLoadingAssistants) {
      return { info: null, isLoading: true };
    }

    return { info: null, isLoading: false };
  }, [conversation, i18n.language, assistantsList, isLoadingAssistants]);
}

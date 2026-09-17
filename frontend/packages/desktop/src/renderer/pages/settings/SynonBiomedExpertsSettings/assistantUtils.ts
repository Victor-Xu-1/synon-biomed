import { resolveExtensionAssetUrl } from '@/renderer/utils/platform';
import { isBackendRelativeAssetPath, isLikelyLocalFilePath } from '@/renderer/utils/model/assistantAvatar';
import type { AssistantListItem, AvailableBackend } from './types';
import type { ManagedAgent } from '@/renderer/utils/synonBiomed/runtime/runtimeTypes';

export type AssistantListFilter = 'all' | 'enabled' | 'disabled' | 'builtin' | 'user';
export const ASSISTANT_SORT_ORDER_GAP = 1000;

/**
 * Check if a string is an emoji.
 */
export const isEmoji = (str: string): boolean => {
  if (!str) return false;
  const emojiRegex =
    /^(?:\p{Emoji_Presentation}|\p{Emoji}\ufe0f)(?:\u200d(?:\p{Emoji_Presentation}|\p{Emoji}\ufe0f))*$/u;
  return emojiRegex.test(str);
};

/**
 * Resolve an avatar string to an image src URL, or undefined if it is not an image.
 */
export const resolveAvatarImageSrc = (avatar: string | undefined): string | undefined => {
  const value = avatar?.trim();
  if (!value) return undefined;

  if (isLikelyLocalFilePath(value)) return undefined;
  if (value.startsWith('/') && !isBackendRelativeAssetPath(value)) return undefined;

  const resolved = resolveExtensionAssetUrl(value) || value;
  const isImage = /\.(svg|png|jpe?g|webp|gif)$/i.test(resolved) || /^(https?:|file:\/\/|data:|\/)/i.test(resolved);
  return isImage ? resolved : undefined;
};

/**
 * Sort assistants by sort order.
 */
export const sortAssistants = (list: AssistantListItem[]): AssistantListItem[] =>
  [...list].toSorted((a, b) => a.sort_order - b.sort_order);

/**
 * Reorder assistants by moving `activeId` to the position of `overId`.
 */
export const reorderAssistantList = (
  assistants: AssistantListItem[],
  activeId: string,
  overId: string
): AssistantListItem[] => {
  const activeIndex = assistants.findIndex((assistant) => assistant.id === activeId);
  const overIndex = assistants.findIndex((assistant) => assistant.id === overId);
  if (activeIndex < 0 || overIndex < 0 || activeIndex === overIndex) {
    return assistants;
  }

  const nextAssistants = [...assistants];
  const [movedAssistant] = nextAssistants.splice(activeIndex, 1);
  nextAssistants.splice(overIndex, 0, movedAssistant);
  return nextAssistants;
};

/**
 * Build deterministic sort_order updates for a reordered assistant list.
 */
export const buildAssistantSortUpdates = (
  previousAssistants: AssistantListItem[],
  nextAssistants: AssistantListItem[]
): Array<{ id: string; sort_order: number }> =>
  nextAssistants
    .map((assistant, index) => ({
      id: assistant.id,
      sort_order: (index + 1) * ASSISTANT_SORT_ORDER_GAP,
      previous: previousAssistants.find((item) => item.id === assistant.id),
    }))
    .filter(({ previous, sort_order }) => previous?.sort_order !== sort_order)
    .map(({ id, sort_order }) => ({ id, sort_order }));

/**
 * Apply normalized sort_order values to a reordered assistant list.
 */
export const applyAssistantSortOrders = (assistants: AssistantListItem[]): AssistantListItem[] =>
  assistants.map((assistant, index) => ({
    ...assistant,
    sort_order: (index + 1) * ASSISTANT_SORT_ORDER_GAP,
  }));

/**
 * Apply search and management filter to assistant list.
 */
export const filterAssistants = (
  assistants: AssistantListItem[],
  query: string,
  filter: AssistantListFilter,
  localeKey: string
): AssistantListItem[] => {
  const normalizedQuery = query.trim().toLowerCase();

  return assistants.filter((assistant) => {
    if (normalizedQuery) {
      const searchableText = [
        assistant.name_i18n?.[localeKey] || assistant.name,
        assistant.description_i18n?.[localeKey] || assistant.description || '',
      ]
        .join(' ')
        .toLowerCase();

      if (!searchableText.includes(normalizedQuery)) return false;
    }

    switch (filter) {
      case 'enabled':
        return assistant.enabled !== false;
      case 'disabled':
        return assistant.enabled === false;
      case 'builtin':
        return assistant.source === 'builtin';
      case 'user':
        return assistant.source === 'user';
      case 'all':
      default:
        return true;
    }
  });
};

/**
 * Split assistants into enabled and disabled groups while preserving order.
 */
export const groupAssistantsByEnabled = (assistants: AssistantListItem[]) => ({
  enabledAssistants: assistants.filter((assistant) => assistant.enabled !== false),
  disabledAssistants: assistants.filter((assistant) => assistant.enabled === false),
});

export type AssistantEnabledFilter = 'all' | 'enabled' | 'disabled';

export const filterByEnabled = (
  assistants: AssistantListItem[],
  filter: AssistantEnabledFilter
): AssistantListItem[] => {
  switch (filter) {
    case 'enabled':
      return assistants.filter((assistant) => assistant.enabled !== false);
    case 'disabled':
      return assistants.filter((assistant) => assistant.enabled === false);
    default:
      return assistants;
  }
};

const ASSISTANT_EDITOR_AGENT_TYPES = new Set(['synonbiomed']);

const isAssistantEditorAgent = (agent: ManagedAgent): boolean => ASSISTANT_EDITOR_AGENT_TYPES.has(agent.agent_type);

export const buildAssistantEditorBackends = (
  agents: ManagedAgent[],
  localeKey: string,
  currentAgentId?: string
): AvailableBackend[] => {
  const backendMap = new Map<string, AvailableBackend>();

  for (const agent of agents) {
    if (!isAssistantEditorAgent(agent)) {
      continue;
    }

    const agentId = agent.id?.trim() || '';
    const status = agent.status;
    const isCurrent = Boolean(currentAgentId && agentId === currentAgentId);
    const isSelectable = agent.enabled !== false && (status === 'online' || status === 'unchecked');
    if (!agentId || backendMap.has(agentId) || (!isSelectable && !isCurrent)) {
      continue;
    }

    const runtimeKey = (agent.backend || agent.agent_type || '').trim();
    if (!runtimeKey) {
      continue;
    }

    backendMap.set(agentId, {
      id: agentId,
      name: agent.name_i18n?.[localeKey] || agent.name,
      runtimeKey,
      isExtension: agent.isExtension,
      icon: agent.avatar || agent.icon,
      modelOptions: [],
    });
  }

  return [...backendMap.values()];
};

import { ipcBridge } from '@/common';
import { resolveLocaleKey } from '@/common/utils';
import type { Assistant } from '@/common/types/agent/assistantTypes';
import { loadSynonBiomedAgents, toSynonAIAssistant } from '@/renderer/services/synonBiomedCapabilities';
import {
  applyAssistantSortOrders,
  buildAssistantSortUpdates,
  reorderAssistantList,
} from '@/renderer/pages/settings/SynonBiomedExpertsSettings/assistantUtils';
import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';

/**
 * Manages the visible Synon Biomed expert list with SynonAI's native list,
 * selection, and ordering interactions.
 */
export const useAssistantList = () => {
  const { i18n } = useTranslation();
  const [assistants, setAssistants] = useState<Assistant[]>([]);
  const [activeAssistantId, setActiveAssistantId] = useState<string | null>(null);
  const localeKey = resolveLocaleKey(i18n.language);
  const previousLocaleKeyRef = useRef(localeKey);

  const loadAssistants = useCallback(async () => {
    try {
      const list = (await loadSynonBiomedAgents()).map((agent, index) => toSynonAIAssistant(agent, index));
      setAssistants(list);
      setActiveAssistantId((prev) => {
        if (prev && list.some((assistant) => assistant.id === prev)) return prev;
        return list[0]?.id ?? null;
      });
    } catch (error) {
      console.error('Failed to load assistants:', error);
    }
  }, []);

  const reorderAssistants = useCallback(
    async (activeId: string, overId: string) => {
      const reorderedAssistants = reorderAssistantList(assistants, activeId, overId);
      if (reorderedAssistants === assistants) return;

      const normalizedAssistants = applyAssistantSortOrders(reorderedAssistants);
      const sortUpdates = buildAssistantSortUpdates(assistants, normalizedAssistants);
      if (sortUpdates.length === 0) {
        setAssistants(normalizedAssistants);
        return;
      }

      const previousAssistants = assistants;
      setAssistants(normalizedAssistants);

      try {
        await Promise.all(sortUpdates.map((update) => ipcBridge.assistants.setState.invoke(update)));
      } catch (error) {
        console.error('Failed to reorder assistants:', error);
        setAssistants(previousAssistants);
      }
    },
    [assistants]
  );

  useEffect(() => {
    void loadAssistants();
  }, [loadAssistants]);

  useEffect(() => {
    const localeChanged = previousLocaleKeyRef.current !== localeKey;
    previousLocaleKeyRef.current = localeKey;
    if (localeChanged) void loadAssistants();
  }, [loadAssistants, localeKey]);

  const activeAssistant = assistants.find((assistant) => assistant.id === activeAssistantId) ?? null;

  return {
    assistants,
    setAssistants,
    activeAssistantId,
    setActiveAssistantId,
    activeAssistant,
    loadAssistants,
    reorderAssistants,
    localeKey,
  };
};

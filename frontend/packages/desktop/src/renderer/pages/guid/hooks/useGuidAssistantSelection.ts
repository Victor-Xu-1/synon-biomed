/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { assistantRuntimeKey, isSynonBiomedAssistant, type Assistant } from '@/common/types/agent/assistantTypes';
import { configService } from '@/common/config/configService';
import type { AcpModelInfo } from '../types';
import type { AgentModeOption } from '@/renderer/utils/synonBiomed/runtime/runtimeTypes';
import {
  buildAgentRuntimeModeState,
  buildAgentRuntimeModelInfo,
  buildAgentRuntimeSlashCommands,
  buildAgentRuntimeThoughtLevelOption,
  type AgentRuntimeCatalog,
  type AgentRuntimeDerivedOption,
} from '@/renderer/utils/synonBiomed/runtime/runtimeCatalog';
import type { SlashCommandItem } from '@/common/chat/slash/types';
import { useManagedAgentRuntimeCatalog } from '@/renderer/hooks/synonBiomed/runtime/useManagedAgents';
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useSynonBiomedAssistantsLoader } from './useSynonBiomedAssistantsLoader';
import {
  loadSynonBiomedSessionDefaults,
  type SynonBiomedSessionDefaults,
} from '@/renderer/services/synonBiomedSessionDefaults';

export {
  buildAgentRuntimeModeState,
  buildAgentRuntimeModelInfo,
  buildAgentRuntimeSlashCommands,
  type AgentRuntimeCatalog,
};

export type GuidAssistantSelectionResult = {
  selectedAssistantId: string | null;
  setSelectedAssistantId: (assistantId: string) => void;
  defaultAssistantId: string | null;
  selectedAssistant: Assistant | undefined;
  selectedAssistantBackend: string;
  selectedAssistantAvailable: boolean;
  assistants: Assistant[];
  assistantCatalogStatus: 'loading' | 'ready' | 'error';
  hasUsableAssistantCatalog: boolean;
  retryAssistantCatalog: () => Promise<void>;
  selectedMode: string;
  setSelectedMode: (mode: React.SetStateAction<string>, options?: { persistPreference?: boolean }) => void;
  selectedAcpModel: string | null;
  setSelectedAcpModel: (model: React.SetStateAction<string | null>, options?: { persistPreference?: boolean }) => void;
  currentAcpCachedModelInfo: AcpModelInfo | null;
  currentAgentAvailableCommands: SlashCommandItem[];
  currentAgentModeOptions: AgentModeOption[];
  currentThoughtLevelOption: AgentRuntimeDerivedOption | null;
  selectedThoughtLevelValue: string;
  setSelectedThoughtLevelValue: (
    value: React.SetStateAction<string>,
    options?: { persistPreference?: boolean }
  ) => void;
  sessionDefaults: SynonBiomedSessionDefaults;
};

export function resolveInitialAssistantModel(models: string[]): string | null {
  if (models.length > 0) {
    return models[0];
  }

  return null;
}

export function buildAssistantModelInfo(models: string[]): AcpModelInfo | null {
  if (models.length > 0) {
    return {
      current_model_id: models[0],
      current_model_label: models[0],
      available_models: models.map((model) => ({ id: model, label: model })),
    } satisfies AcpModelInfo;
  }

  return null;
}

export function resolveAssistantSelectionKey(
  savedKey: string | undefined,
  assistants: Assistant[]
): string | undefined {
  if (!savedKey) return undefined;

  if (savedKey.startsWith('custom:')) {
    const assistantId = savedKey.slice(7);
    return assistants.some((assistant) => assistant.id === assistantId && isSynonBiomedAssistant(assistant))
      ? assistantId
      : undefined;
  }

  if (assistants.some((assistant) => assistant.id === savedKey && isSynonBiomedAssistant(assistant))) {
    return savedKey;
  }

  return undefined;
}

function readPersistedGuidAssistantSelectionKey(assistants: Assistant[]): string | undefined {
  const savedKey = configService.get('guid.lastAssistantId');
  const enabledAssistants = assistants.filter(
    (assistant) => assistant.enabled !== false && isSynonBiomedAssistant(assistant)
  );
  return resolveAssistantSelectionKey(savedKey, enabledAssistants);
}

function persistGuidAssistantSelectionKey(assistantId: string): void {
  void configService.set('guid.lastAssistantId', assistantId).catch((error) => {
    console.error('[Guid] Failed to persist selected assistant:', error);
  });
}

export function pickDefaultAssistantSelectionKey(assistants: Assistant[]): string | null {
  const enabledAssistants = assistants.filter(
    (assistant) => assistant.enabled !== false && isSynonBiomedAssistant(assistant)
  );
  const preferred = enabledAssistants.find((assistant) => assistant.agent_id === 'OPERON') ?? enabledAssistants[0];
  return preferred?.id ?? null;
}

type UseGuidAssistantSelectionOptions = {
  resetAssistant?: boolean;
  preselectAssistantId?: string;
  locationKey?: string;
};

export const useGuidAssistantSelection = ({
  resetAssistant,
  preselectAssistantId,
  locationKey,
}: UseGuidAssistantSelectionOptions): GuidAssistantSelectionResult => {
  const [selectedAssistantIdState, _setSelectedAssistantId] = useState<string | null>(null);
  const [selectedMode, _setSelectedMode] = useState<string>('default');
  const [selectedAcpModel, _setSelectedAcpModel] = useState<string | null>(null);
  const [selectedThoughtLevelValue, _setSelectedThoughtLevelValue] = useState<string>('');
  const [sessionDefaults, setSessionDefaults] = useState<SynonBiomedSessionDefaults>({});
  const explicitModelSelectionRef = useRef(false);
  const explicitThoughtSelectionRef = useRef(false);
  const {
    assistants,
    catalogStatus: assistantCatalogStatus,
    hasUsableCatalog: hasUsableAssistantCatalog,
    retry: retryAssistantCatalog,
  } = useSynonBiomedAssistantsLoader();
  const managedAgentRuntimeCatalog = useManagedAgentRuntimeCatalog();

  useEffect(() => {
    let active = true;
    void loadSynonBiomedSessionDefaults()
      .then((defaults) => {
        if (active) setSessionDefaults(defaults);
      })
      .catch((error) => {
        console.error('[Guid] Failed to load session defaults:', error);
      });
    return () => {
      active = false;
    };
  }, []);

  const setSelectedMode = useCallback(
    (mode: React.SetStateAction<string>, _options?: { persistPreference?: boolean }) => {
      _setSelectedMode((prev) => {
        const nextMode = typeof mode === 'function' ? mode(prev) : mode;
        return nextMode;
      });
    },
    []
  );

  const setSelectedAcpModel = useCallback(
    (modelId: React.SetStateAction<string | null>, _options?: { persistPreference?: boolean }) => {
      explicitModelSelectionRef.current = true;
      _setSelectedAcpModel((prev) => {
        const nextModelId = typeof modelId === 'function' ? modelId(prev) : modelId;
        return nextModelId;
      });
    },
    []
  );

  const setSelectedThoughtLevelValue = useCallback(
    (value: React.SetStateAction<string>, _options?: { persistPreference?: boolean }) => {
      explicitThoughtSelectionRef.current = true;
      _setSelectedThoughtLevelValue((prev) => {
        const nextValue = typeof value === 'function' ? value(prev) : value;
        return nextValue;
      });
    },
    []
  );

  const setSelectedAssistantId = useCallback(
    (assistantId: string) => {
      const normalizedId = resolveAssistantSelectionKey(assistantId, assistants) ?? assistantId;
      _setSelectedAssistantId(normalizedId);
      persistGuidAssistantSelectionKey(normalizedId);
    },
    [assistants]
  );

  const resetHandledRef = useRef(false);
  const prevLocationKeyRef = useRef(locationKey);
  if (locationKey !== prevLocationKeyRef.current) {
    prevLocationKeyRef.current = locationKey;
    resetHandledRef.current = false;
  }

  useLayoutEffect(() => {
    if (assistants.length === 0) return;
    if (resetHandledRef.current) return;

    if (preselectAssistantId) {
      const resolvedPreselect = resolveAssistantSelectionKey(preselectAssistantId, assistants);
      if (resolvedPreselect) {
        resetHandledRef.current = true;
        _setSelectedAssistantId(resolvedPreselect);
        return;
      }
    }

    if (resetAssistant) {
      resetHandledRef.current = true;
      const fallbackId =
        readPersistedGuidAssistantSelectionKey(assistants) ?? pickDefaultAssistantSelectionKey(assistants);
      _setSelectedAssistantId(fallbackId);
    }
  }, [assistants, preselectAssistantId, resetAssistant]);

  useEffect(() => {
    if (assistants.length === 0) return;
    if (resetAssistant) return;
    if (preselectAssistantId && resolveAssistantSelectionKey(preselectAssistantId, assistants)) return;
    if (!selectedAssistantIdState || !assistants.some((assistant) => assistant.id === selectedAssistantIdState)) {
      _setSelectedAssistantId(
        readPersistedGuidAssistantSelectionKey(assistants) ?? pickDefaultAssistantSelectionKey(assistants)
      );
    }
  }, [assistants, preselectAssistantId, resetAssistant, selectedAssistantIdState]);

  const selectedAssistant = useMemo(
    () =>
      selectedAssistantIdState ? assistants.find((assistant) => assistant.id === selectedAssistantIdState) : undefined,
    [assistants, selectedAssistantIdState]
  );
  const selectedAssistantId = selectedAssistant?.id ?? null;
  const selectedAssistantBackend = assistantRuntimeKey(selectedAssistant);
  const selectedAssistantModels = selectedAssistant?.models ?? [];
  const selectedManagedAgentRuntimeCatalog = useMemo(
    () =>
      selectedAssistant?.agent_id
        ? managedAgentRuntimeCatalog.find((agent) => agent.id === selectedAssistant.agent_id)
        : undefined,
    [managedAgentRuntimeCatalog, selectedAssistant?.agent_id]
  );
  const selectedAgentRuntimeModelInfo = useMemo(
    () => buildAgentRuntimeModelInfo(selectedManagedAgentRuntimeCatalog),
    [selectedManagedAgentRuntimeCatalog]
  );
  const currentAgentAvailableCommands = useMemo(
    () => buildAgentRuntimeSlashCommands(selectedManagedAgentRuntimeCatalog),
    [selectedManagedAgentRuntimeCatalog]
  );
  const selectedAgentRuntimeModeState = useMemo(
    () => buildAgentRuntimeModeState(selectedManagedAgentRuntimeCatalog),
    [selectedManagedAgentRuntimeCatalog]
  );
  const selectedAgentRuntimeThoughtLevelOption = useMemo(
    () => buildAgentRuntimeThoughtLevelOption(selectedManagedAgentRuntimeCatalog),
    [selectedManagedAgentRuntimeCatalog]
  );
  const currentThoughtLevelOption = useMemo<AgentRuntimeDerivedOption | null>(() => {
    if (!selectedAgentRuntimeThoughtLevelOption) return null;
    return {
      ...selectedAgentRuntimeThoughtLevelOption,
      currentValue: selectedThoughtLevelValue || selectedAgentRuntimeThoughtLevelOption.currentValue,
    };
  }, [selectedAgentRuntimeThoughtLevelOption, selectedThoughtLevelValue]);
  const currentAgentModeOptions = selectedAgentRuntimeModeState.options;

  const selectedAssistantAvailable = useMemo(() => {
    return selectedAssistant?.agent_status === 'online';
  }, [selectedAssistant]);

  const modelSelectionScopeRef = useRef<string | null>(null);
  useEffect(() => {
    const runtimeModelId =
      selectedAgentRuntimeModelInfo?.current_model_id || selectedAgentRuntimeModelInfo?.available_models[0]?.id;
    const fallbackModelId =
      runtimeModelId ||
      (selectedAssistantModels.length > 0 ? resolveInitialAssistantModel(selectedAssistantModels) : null);
    const availableModelIds = new Set(
      selectedAgentRuntimeModelInfo?.available_models.map((model) => model.id) ?? selectedAssistantModels
    );
    const selectionScope = selectedAssistantId ?? '';

    _setSelectedAcpModel((previousModelId) => {
      const scopeChanged = modelSelectionScopeRef.current !== selectionScope;
      modelSelectionScopeRef.current = selectionScope;

      if (scopeChanged) explicitModelSelectionRef.current = false;

      if (
        !scopeChanged &&
        previousModelId &&
        (availableModelIds.size === 0 || availableModelIds.has(previousModelId))
      ) {
        return previousModelId;
      }

      return fallbackModelId;
    });
  }, [selectedAssistantId, selectedAssistantModels, selectedAgentRuntimeModelInfo]);

  useEffect(() => {
    const configuredModelId = sessionDefaults.defaultModelId;
    if (!configuredModelId || explicitModelSelectionRef.current) return;
    const availableModelIds = new Set(
      selectedAgentRuntimeModelInfo?.available_models.map((model) => model.id) ?? selectedAssistantModels
    );
    if (availableModelIds.size === 0 || availableModelIds.has(configuredModelId)) {
      _setSelectedAcpModel(configuredModelId);
    }
  }, [selectedAgentRuntimeModelInfo, selectedAssistantModels, sessionDefaults.defaultModelId]);

  useEffect(() => {
    const fallbackMode =
      selectedAgentRuntimeModeState.currentMode || selectedAgentRuntimeModeState.options[0]?.value || 'default';
    _setSelectedMode(fallbackMode);
  }, [selectedAgentRuntimeModeState]);

  const thoughtLevelSelectionScopeRef = useRef<string | null>(null);
  useEffect(() => {
    const optionValues = new Set(selectedAgentRuntimeThoughtLevelOption?.options.map((option) => option.value) ?? []);
    const fallbackThoughtLevel =
      selectedAgentRuntimeThoughtLevelOption?.currentValue ||
      selectedAgentRuntimeThoughtLevelOption?.options[0]?.value ||
      '';
    const selectionScope = selectedAssistantId ?? '';

    _setSelectedThoughtLevelValue((previousValue) => {
      const scopeChanged = thoughtLevelSelectionScopeRef.current !== selectionScope;
      thoughtLevelSelectionScopeRef.current = selectionScope;
      if (scopeChanged) explicitThoughtSelectionRef.current = false;

      if (!selectedAgentRuntimeThoughtLevelOption) {
        return '';
      }

      if (!scopeChanged && previousValue && optionValues.has(previousValue)) {
        return previousValue;
      }

      return fallbackThoughtLevel;
    });
  }, [selectedAgentRuntimeThoughtLevelOption, selectedAssistantId]);

  useEffect(() => {
    const configuredEffort = sessionDefaults.effort;
    if (!configuredEffort || explicitThoughtSelectionRef.current || !selectedAgentRuntimeThoughtLevelOption) return;
    const optionValues = new Set(selectedAgentRuntimeThoughtLevelOption.options.map((option) => option.value));
    if (optionValues.has(configuredEffort)) _setSelectedThoughtLevelValue(configuredEffort);
  }, [selectedAgentRuntimeThoughtLevelOption, sessionDefaults.effort]);

  const currentAcpCachedModelInfo = useMemo(() => {
    if (selectedAgentRuntimeModelInfo) {
      return selectedAgentRuntimeModelInfo;
    }

    return buildAssistantModelInfo(selectedAssistantModels);
  }, [selectedAssistantModels, selectedAgentRuntimeModelInfo]);

  const defaultAssistantId = useMemo(() => pickDefaultAssistantSelectionKey(assistants), [assistants]);

  return {
    selectedAssistantId,
    setSelectedAssistantId,
    defaultAssistantId,
    selectedAssistant,
    selectedAssistantBackend,
    selectedAssistantAvailable,
    assistants,
    assistantCatalogStatus,
    hasUsableAssistantCatalog,
    retryAssistantCatalog,
    selectedMode,
    setSelectedMode,
    selectedAcpModel,
    setSelectedAcpModel,
    currentAcpCachedModelInfo,
    currentAgentAvailableCommands,
    currentAgentModeOptions,
    currentThoughtLevelOption,
    selectedThoughtLevelValue,
    setSelectedThoughtLevelValue,
    sessionDefaults,
  };
};

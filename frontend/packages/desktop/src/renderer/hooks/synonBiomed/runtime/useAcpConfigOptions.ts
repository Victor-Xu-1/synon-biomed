/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { ipcBridge } from '@/common';
import { isBackendHttpError } from '@/common/adapter/httpBridge';
import type { IResponseMessage } from '@/common/adapter/ipcBridge';
import type {
  AcpConfigOptionDto,
  AcpConfigSelectOptionDto,
  SetConfigOptionResponse,
} from '@/common/types/platform/acpTypes';
import { ensureConversationRuntime } from '@/renderer/pages/conversation/utils/ensureConversationRuntime';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import useSWR from 'swr';

export type AcpDerivedSelectOption = {
  value: string;
  label: string;
  description?: string | null;
};

export type AcpDerivedOption = {
  id: string;
  category: string;
  currentValue: string | null;
  options: AcpDerivedSelectOption[];
};

export type AcpConfigSetStatus = { state: 'idle' } | { state: 'setting'; optionId: string; requestedValue: string };

export type AcpConfigSetErrorKind =
  | 'command_ack'
  | 'confirmation_timeout'
  | 'config_update_in_progress'
  | 'config_not_observed'
  | 'unknown';

type ReloadOptions = {
  /** Passive runtime refreshes must not replace a usable snapshot with a transient error. */
  surfaceError?: boolean;
};

const CONFIG_UPDATE_RETRY_DELAYS_MS = [120, 240, 480] as const;
const CONFIG_OBSERVATION_RETRY_DELAYS_MS = [50, 100, 200, 300] as const;

const optionLabel = (option: AcpConfigSelectOptionDto): string => option.name || option.label || option.value;

export function getOptionCurrentValue(option: AcpConfigOptionDto | null | undefined): string | null {
  return option?.current_value ?? null;
}

export function findConfigOption(
  options: AcpConfigOptionDto[] | null | undefined,
  category: string,
  fallbackIds: string[] = []
): AcpConfigOptionDto | null {
  if (!options?.length) return null;
  return (
    options.find((option) => option.category === category) ||
    options.find((option) => fallbackIds.includes(option.id)) ||
    null
  );
}

export function deriveSelectOption(
  options: AcpConfigOptionDto[] | null | undefined,
  category: string,
  fallbackIds: string[] = []
): AcpDerivedOption | null {
  const option = findConfigOption(options, category, fallbackIds);
  if (!option || (option.option_type ?? option.type) !== 'select') return null;
  return {
    id: option.id,
    category,
    currentValue: getOptionCurrentValue(option),
    options: option.options.map((choice) => ({
      value: choice.value,
      label: optionLabel(choice),
      description: choice.description,
    })),
  };
}

export function hasObservedValue(
  response: SetConfigOptionResponse,
  optionId: string,
  requestedValue: string
): response is SetConfigOptionResponse & { config_options: AcpConfigOptionDto[] } {
  if (response.confirmation !== 'observed') return false;
  const option = response.config_options?.find((candidate) => candidate.id === optionId);
  return getOptionCurrentValue(option) === requestedValue;
}

export function classifyConfigSetError(error: unknown): AcpConfigSetErrorKind {
  if (error instanceof Error) {
    if (error.message.includes('command_ack')) return 'command_ack';
    if (error.message.includes('config_update_in_progress')) return 'config_update_in_progress';
    if (error.message.includes('config_not_observed')) return 'config_not_observed';
  }
  if (isBackendHttpError(error)) {
    if (error.code === 'confirmation_timeout') return 'confirmation_timeout';
    if (error.code === 'config_update_in_progress') return 'config_update_in_progress';
  }
  return 'unknown';
}

type AcpConfigOptionsKey = readonly ['acp-config-options', string];

const getRuntimeConfigOptionsKey = (conversation_id: string): AcpConfigOptionsKey =>
  ['acp-config-options', conversation_id] as const;

const statusByConversation = new Map<string, AcpConfigSetStatus>();
const statusListeners = new Map<string, Set<(status: AcpConfigSetStatus) => void>>();

function getConversationSetStatus(conversation_id: string): AcpConfigSetStatus {
  return statusByConversation.get(conversation_id) ?? { state: 'idle' };
}

function setConversationSetStatus(conversation_id: string, status: AcpConfigSetStatus): void {
  statusByConversation.set(conversation_id, status);
  statusListeners.get(conversation_id)?.forEach((listener) => listener(status));
}

function subscribeConversationSetStatus(
  conversation_id: string,
  listener: (status: AcpConfigSetStatus) => void
): () => void {
  const listeners = statusListeners.get(conversation_id) ?? new Set<(status: AcpConfigSetStatus) => void>();
  listeners.add(listener);
  statusListeners.set(conversation_id, listeners);
  return () => {
    listeners.delete(listener);
    if (listeners.size === 0) statusListeners.delete(conversation_id);
  };
}

function delay(milliseconds: number): Promise<void> {
  return new Promise((resolve) => {
    window.setTimeout(resolve, milliseconds);
  });
}

function isTransientConfigUpdateError(error: unknown): boolean {
  if (isBackendHttpError(error)) {
    return (
      error.code === 'config_update_in_progress' ||
      error.code === 'confirmation_timeout' ||
      [408, 409, 425, 429, 502, 503, 504].includes(error.status)
    );
  }
  if (!(error instanceof Error)) return false;
  return error.message.includes('config_update_in_progress') || error.message.includes('confirmation_timeout');
}

const ensureRuntimeConfigOptions = async ([, conversation_id]: AcpConfigOptionsKey): Promise<AcpConfigOptionDto[]> =>
  (await ensureConversationRuntime(conversation_id)).config_options;

const configOptionsInFlight = new Map<string, Promise<AcpConfigOptionDto[]>>();

function fetchConfigOptionsOnce(key: AcpConfigOptionsKey): Promise<AcpConfigOptionDto[]> {
  const [, conversation_id] = key;
  const existing = configOptionsInFlight.get(conversation_id);
  if (existing) return existing;

  const promise = ensureRuntimeConfigOptions(key).finally(() => {
    if (configOptionsInFlight.get(conversation_id) === promise) {
      configOptionsInFlight.delete(conversation_id);
    }
  });
  configOptionsInFlight.set(conversation_id, promise);
  return promise;
}

export function useAcpConfigOptions({
  conversation_id,
  prepareRuntime,
  enabled = true,
}: {
  conversation_id: string;
  prepareRuntime?: () => Promise<void>;
  enabled?: boolean;
}) {
  const [setStatus, setSetStatus] = useState<AcpConfigSetStatus>(() => getConversationSetStatus(conversation_id));
  const [reloadPending, setReloadPending] = useState(false);
  const [loadError, setLoadError] = useState<unknown>(null);
  const loadGenerationRef = useRef(0);
  const optionsRef = useRef<AcpConfigOptionDto[] | null>(null);
  const key = useMemo(() => getRuntimeConfigOptionsKey(conversation_id), [conversation_id]);
  const {
    data: snapshotData,
    mutate,
    isLoading,
  } = useSWR<AcpConfigOptionDto[] | null>(enabled ? key : null, fetchConfigOptionsOnce, {
    revalidateOnMount: false,
  });
  const configOptions = enabled ? (snapshotData ?? null) : null;

  useEffect(() => {
    optionsRef.current = configOptions;
  }, [configOptions]);

  useEffect(() => {
    setSetStatus(getConversationSetStatus(conversation_id));
    return subscribeConversationSetStatus(conversation_id, setSetStatus);
  }, [conversation_id]);

  useEffect(() => {
    loadGenerationRef.current += 1;
    setReloadPending(false);
    setLoadError(null);
  }, [conversation_id, enabled]);

  const replaceSnapshot = useCallback(
    (next: AcpConfigOptionDto[]) => {
      optionsRef.current = next;
      void mutate(next, false);
    },
    [mutate]
  );

  const reload = useCallback(
    async ({ surfaceError = true }: ReloadOptions = {}) => {
      // A running task can emit session_active while a config update is being
      // acknowledged. Do not start a competing ensure/reload request: the
      // update path owns the authoritative snapshot until it settles.
      if (getConversationSetStatus(conversation_id).state === 'setting') {
        return optionsRef.current ?? [];
      }
      const generation = ++loadGenerationRef.current;
      setReloadPending(true);
      setLoadError(null);
      try {
        await prepareRuntime?.();
        const next = await fetchConfigOptionsOnce(key);
        if (generation === loadGenerationRef.current) replaceSnapshot(next);
        return next;
      } catch (error) {
        if (generation === loadGenerationRef.current) {
          // Once a conversation has a usable config snapshot, a passive refresh
          // failure must not turn a running task's controls into a false
          // "loading error" state. Explicit retries still surface their error.
          if (surfaceError || !(optionsRef.current && optionsRef.current.length > 0)) {
            setLoadError(error);
          }
        }
        throw error;
      } finally {
        if (generation === loadGenerationRef.current) setReloadPending(false);
      }
    },
    [key, prepareRuntime, replaceSnapshot]
  );

  const setConfigOption = useCallback(
    async (optionId: string, value: string) => {
      if (getConversationSetStatus(conversation_id).state === 'setting') {
        throw new Error('config_update_in_progress');
      }
      // Invalidate any passive session_active refresh that was already in
      // flight. Its failure or stale snapshot must not overwrite this update.
      ++loadGenerationRef.current;
      setConversationSetStatus(conversation_id, { state: 'setting', optionId, requestedValue: value });
      setLoadError(null);
      try {
        await prepareRuntime?.();
        // The initial ensure is already coalesced by fetchConfigOptionsOnce.
        // Re-reading it before every update creates a race with the active
        // task's session_active event; only fetch when this hook has no usable
        // option snapshot or the requested option is not present.
        const currentOptions = optionsRef.current;
        if (!currentOptions?.some((option) => option.id === optionId)) {
          replaceSnapshot(await fetchConfigOptionsOnce(key));
        }

        let response: SetConfigOptionResponse | null = null;
        let lastError: unknown = null;
        for (let attempt = 0; attempt <= CONFIG_UPDATE_RETRY_DELAYS_MS.length; attempt += 1) {
          try {
            // Config writes are intentionally serialized; parallel writes can
            // make the runtime acknowledge the wrong value.
            // eslint-disable-next-line no-await-in-loop
            response = await ipcBridge.acpConversation.setConfigOption.invoke({
              conversation_id,
              option_id: optionId,
              value,
            });
            break;
          } catch (error) {
            lastError = error;
            const retryDelay = CONFIG_UPDATE_RETRY_DELAYS_MS[attempt];
            if (retryDelay === undefined || !isTransientConfigUpdateError(error)) throw error;
            // eslint-disable-next-line no-await-in-loop
            await delay(retryDelay);
          }
        }
        if (!response) throw lastError ?? new Error('config_update_failed');
        const confirmedResponse = response as SetConfigOptionResponse;

        const observedOptions = confirmedResponse.config_options;
        const observedOption = Array.isArray(observedOptions)
          ? observedOptions.find((option) => option.id === optionId && getOptionCurrentValue(option) === value)
          : undefined;
        if (confirmedResponse.confirmation === 'observed' && observedOption && Array.isArray(observedOptions)) {
          replaceSnapshot(observedOptions);
          return observedOptions;
        }

        // Some ACP runtimes acknowledge the command first and publish the
        // observed config on the response stream a moment later. Reconcile a
        // bounded number of real runtime snapshots before reporting failure.
        if (confirmedResponse.confirmation === 'command_ack') {
          for (const retryDelay of CONFIG_OBSERVATION_RETRY_DELAYS_MS) {
            const observed = optionsRef.current?.find(
              (option) => option.id === optionId && getOptionCurrentValue(option) === value
            );
            if (observed) return optionsRef.current ?? [];
            // eslint-disable-next-line no-await-in-loop
            await delay(retryDelay);
            try {
              // eslint-disable-next-line no-await-in-loop
              const next = await fetchConfigOptionsOnce(key);
              replaceSnapshot(next);
              if (hasObservedValue({ confirmation: 'observed', config_options: next }, optionId, value)) {
                return next;
              }
            } catch {
              // The bounded observation loop will retry; the original command
              // acknowledgement remains the authoritative acceptance signal.
            }
          }
          throw new Error('command_ack');
        }

        throw new Error('config_not_observed');
      } finally {
        setConversationSetStatus(conversation_id, { state: 'idle' });
      }
    },
    [conversation_id, key, prepareRuntime, replaceSnapshot]
  );

  useEffect(() => {
    if (!enabled) return;
    void reload({ surfaceError: false }).catch(() => {});
  }, [enabled, reload]);

  useEffect(() => {
    if (!enabled) return;
    const handler = (message: IResponseMessage) => {
      if (message.conversation_id !== conversation_id) return;
      if (message.type === 'acp_config_option' && message.data) {
        const optionPayload = message.data as { config_options?: AcpConfigOptionDto[] } | AcpConfigOptionDto[];
        const next = Array.isArray(optionPayload) ? optionPayload : optionPayload.config_options;
        if (Array.isArray(next)) {
          setLoadError(null);
          replaceSnapshot(next);
        }
      }
      if (message.type === 'agent_status') {
        const statusPayload = message.data as { status?: string } | undefined;
        if (statusPayload?.status === 'session_active') void reload({ surfaceError: false }).catch(() => {});
      }
    };
    return ipcBridge.acpConversation.responseStream.on(handler);
  }, [conversation_id, enabled, reload, replaceSnapshot]);

  return {
    configOptions,
    isLoading: isLoading || reloadPending,
    loadError,
    setStatus,
    mode: deriveSelectOption(configOptions, 'mode', ['mode']),
    model: deriveSelectOption(configOptions, 'model', ['model']),
    thoughtLevel: deriveSelectOption(configOptions, 'thought_level', ['thought_level', 'reasoning_effort']),
    reload,
    setConfigOption,
  };
}

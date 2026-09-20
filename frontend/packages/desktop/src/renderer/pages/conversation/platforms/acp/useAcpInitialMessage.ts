/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { ipcBridge } from '@/common';
import type { TMessage } from '@/common/chat/chatLib';
import type { TConversationRuntimeSummary } from '@/common/config/storage';
import { decodeArtifactReferences, type ArtifactReferenceWire } from '@/common/adapter/messageStreamProtocol';
import { parseError, uuid } from '@/common/utils';
import { emitter } from '@/renderer/utils/emitter';
import { buildDisplayMessage } from '@/renderer/utils/file/messageFiles';
import { useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { getConversationRuntimeWorkspaceErrorMessage } from '../../utils/conversationCreateError';
import { buildSendFailureError } from './buildSendFailureError';
import { setSynonBiomedSessionComputeProvider } from '@/renderer/services/synonBiomedCompute';

type DraftPrefillPayload = {
  input: string;
  files: string[];
  artifactRefs: ArtifactReferenceWire[];
  injectSkills: string[];
  injectMcpServerIds: string[];
};

type UseAcpInitialMessageParams = {
  conversation_id: string;
  backend: string;
  workspacePath?: string;
  streamReady: boolean;
  setAiProcessing: (value: boolean) => void;
  resetState: () => void;
  markSendStarted?: () => void;
  markSendAccepted?: (turn_id: string, runtime: TConversationRuntimeSummary, msg_id?: string) => void;
  markSendFailed?: (reason: string) => void;
  checkAndUpdateTitle: (conversation_id: string, input: string) => void;
  addOrUpdateMessage: (message: TMessage, prepend?: boolean) => void;
  onDraftPrefill?: (payload: DraftPrefillPayload) => void;
};

const initialMessageSendsInFlight = new Set<string>();
const initialMessageFailures = new Set<string>();

const normalizeInitialCapabilityIds = (value: unknown): string[] | undefined => {
  if (!Array.isArray(value)) return undefined;
  const normalized = Array.from(
    new Set(
      value
        .filter((item): item is string => typeof item === 'string')
        .map((item) => item.trim())
        .filter((item) => item.length > 0 && item.length <= 512)
    )
  ).slice(0, 64);
  return normalized.length > 0 ? normalized : undefined;
};

/**
 * Side-effect-only hook that checks sessionStorage for an initial message
 * and sends it when the ACP conversation first mounts.
 */
export const useAcpInitialMessage = ({
  conversation_id,
  backend,
  workspacePath,
  streamReady,
  setAiProcessing,
  resetState,
  markSendStarted,
  markSendAccepted,
  markSendFailed,
  checkAndUpdateTitle,
  addOrUpdateMessage,
  onDraftPrefill,
}: UseAcpInitialMessageParams): void => {
  const { t } = useTranslation();

  useEffect(() => {
    if (!streamReady) return;

    const storageKey = `acp_initial_message_${conversation_id}`;
    const storedMessage = sessionStorage.getItem(storageKey);

    if (!storedMessage) return;
    if (initialMessageSendsInFlight.has(storageKey) || initialMessageFailures.has(storageKey)) return;
    initialMessageSendsInFlight.add(storageKey);

    let parsed: Record<string, unknown> = {};
    try {
      const candidate = JSON.parse(storedMessage) as unknown;
      if (candidate && typeof candidate === 'object' && !Array.isArray(candidate)) {
        parsed = candidate as Record<string, unknown>;
      }
    } catch (error) {
      console.error('[useAcpInitialMessage] Invalid stored initial message:', error);
      initialMessageSendsInFlight.delete(storageKey);
      return;
    }

    if (parsed.draft_only === true) {
      // A staged task never starts by itself: hand the payload to the composer
      // and drop the queued message so nothing is sent until the user acts.
      sessionStorage.removeItem(storageKey);
      initialMessageSendsInFlight.delete(storageKey);
      const input = typeof parsed.input === 'string' ? parsed.input : '';
      const files = Array.isArray(parsed.files)
        ? parsed.files.filter((file: unknown): file is string => typeof file === 'string')
        : [];
      const artifactRefs = Array.isArray(parsed.artifact_refs)
        ? parsed.artifact_refs
            .map((reference: unknown) => decodeArtifactReferences([reference])?.[0])
            .filter(
              (reference: ArtifactReferenceWire | undefined): reference is ArtifactReferenceWire =>
                reference !== undefined
            )
        : [];
      const computeProviders = Array.isArray(parsed.compute_providers)
        ? parsed.compute_providers.filter(
            (provider: unknown): provider is string => typeof provider === 'string' && provider.trim().length > 0
          )
        : [];
      const injectSkills = normalizeInitialCapabilityIds(parsed.inject_skills) ?? [];
      const injectMcpServerIds = normalizeInitialCapabilityIds(parsed.inject_mcp_server_ids) ?? [];
      const applyDraft = async () => {
        if (computeProviders.length > 0) {
          try {
            await Promise.all(
              computeProviders.map((provider: string) =>
                setSynonBiomedSessionComputeProvider(conversation_id, provider, true)
              )
            );
          } catch (error) {
            console.error('[useAcpInitialMessage] Failed to restore compute providers for the staged draft:', error);
          }
        }
        onDraftPrefill?.({ input, files, artifactRefs, injectSkills, injectMcpServerIds });
      };
      void applyDraft();
      return;
    }

    const sendInitialMessage = async () => {
      try {
        const initialMessage = parsed;
        const input = typeof initialMessage.input === 'string' ? initialMessage.input : '';
        const files = Array.isArray(initialMessage.files) ? initialMessage.files : [];
        const artifact_refs = Array.isArray(initialMessage.artifact_refs)
          ? initialMessage.artifact_refs
              .map((reference: unknown) => decodeArtifactReferences([reference])?.[0])
              .filter(
                (reference: ArtifactReferenceWire | undefined): reference is ArtifactReferenceWire =>
                  reference !== undefined
              )
          : undefined;
        const inject_skills = normalizeInitialCapabilityIds(initialMessage.inject_skills);
        const inject_mcp_server_ids = normalizeInitialCapabilityIds(initialMessage.inject_mcp_server_ids);
        const rawSessionOptions =
          initialMessage.session_options && typeof initialMessage.session_options === 'object'
            ? (initialMessage.session_options as Record<string, unknown>)
            : null;
        const sessionOptions = rawSessionOptions
          ? {
              ultra_mode: rawSessionOptions.ultra_mode === true,
              ...(typeof rawSessionOptions.plan_mode === 'boolean' ? { plan_mode: rawSessionOptions.plan_mode } : {}),
              verifier_mode: rawSessionOptions.verifier_mode === 'on' ? ('on' as const) : ('off' as const),
              memory_mode: rawSessionOptions.memory_mode === 'on' ? ('on' as const) : ('off' as const),
              target_agent:
                typeof rawSessionOptions.target_agent === 'string' && rawSessionOptions.target_agent.trim()
                  ? rawSessionOptions.target_agent
                  : 'OPERON',
              model:
                typeof rawSessionOptions.model === 'string' && rawSessionOptions.model.trim()
                  ? rawSessionOptions.model.trim()
                  : undefined,
              subagent_model:
                typeof rawSessionOptions.subagent_model === 'string' && rawSessionOptions.subagent_model.trim()
                  ? rawSessionOptions.subagent_model.trim()
                  : undefined,
              effort:
                typeof rawSessionOptions.effort === 'string' &&
                ['low', 'medium', 'high'].includes(rawSessionOptions.effort)
                  ? (rawSessionOptions.effort as 'low' | 'medium' | 'high')
                  : undefined,
            }
          : undefined;
        const computeProviders = Array.isArray(initialMessage.compute_providers)
          ? initialMessage.compute_providers.filter(
              (provider: unknown): provider is string => typeof provider === 'string' && provider.trim().length > 0
            )
          : [];
        const displayMessage = buildDisplayMessage(input, files, workspacePath || '');

        markSendStarted?.();
        setAiProcessing(true);

        await Promise.all(
          computeProviders.map((provider: string) =>
            setSynonBiomedSessionComputeProvider(conversation_id, provider, true)
          )
        );

        void checkAndUpdateTitle(conversation_id, input);
        const result = await ipcBridge.acpConversation.sendMessage.invoke({
          input: displayMessage,
          conversation_id: conversation_id,
          files,
          artifact_refs,
          ...(inject_skills ? { inject_skills } : {}),
          ...(inject_mcp_server_ids ? { inject_mcp_server_ids } : {}),
          session_options: sessionOptions,
        });
        markSendAccepted?.(result.turn_id, result.runtime, result.msg_id);

        // Remove only the payload accepted by the runtime. A newer draft may
        // already have replaced the same key while this request was pending.
        if (sessionStorage.getItem(storageKey) === storedMessage) {
          sessionStorage.removeItem(storageKey);
        }

        // Initial message sent successfully
        emitter.emit('chat.history.refresh');
      } catch (error) {
        initialMessageFailures.add(storageKey);
        const errorMessageText =
          getConversationRuntimeWorkspaceErrorMessage(error, t) || parseError(error) || t('common.unknownError');
        markSendFailed?.(errorMessageText);
        console.error('[useAcpInitialMessage] Error sending initial message:', error);
        console.error('[useAcpInitialMessage] Error details:', {
          name: (error as Error)?.name,
          message: errorMessageText,
          conversation_id,
        });

        const errorMessage: TMessage = {
          id: uuid(),
          msg_id: uuid(),
          conversation_id: conversation_id,
          type: 'tips',
          position: 'center',
          content: {
            content: errorMessageText,
            type: 'error',
            error: buildSendFailureError(error, errorMessageText),
          },
          created_at: Date.now() + 2,
        };
        addOrUpdateMessage(errorMessage, true);
        resetState();
        setAiProcessing(false); // Keep the prop-setter in sync with the hook reset
      } finally {
        initialMessageSendsInFlight.delete(storageKey);
      }
    };

    sendInitialMessage().catch((error) => {
      console.error('Failed to send initial message:', error);
    });
  }, [
    addOrUpdateMessage,
    backend,
    checkAndUpdateTitle,
    conversation_id,
    markSendAccepted,
    markSendFailed,
    markSendStarted,
    onDraftPrefill,
    resetState,
    setAiProcessing,
    streamReady,
    t,
    workspacePath,
  ]);
};

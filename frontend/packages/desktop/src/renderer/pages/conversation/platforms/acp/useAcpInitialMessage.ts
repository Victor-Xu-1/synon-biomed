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

type DraftStagedSessionOptions = {
  delegation: boolean;
  autoReview: boolean;
  memory: boolean;
  targetAgent: string | null;
};

type DraftPrefillPayload = {
  input: string;
  files: string[];
  artifactRefs: ArtifactReferenceWire[];
  injectSkills: string[];
  injectMcpServerIds: string[];
  sessionOptions: DraftStagedSessionOptions | null;
  planMode: boolean;
};

type DraftRestoreIssue = {
  providers: string[];
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
  onDraftRestoreIssue?: (issue: DraftRestoreIssue) => void;
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
  onDraftRestoreIssue,
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
      // A staged task never starts by itself. The composer write happens
      // first because the send-box draft store is durable; only after the
      // payload is safely in the composer may the queued copy be dropped.
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
      const injectSkills = normalizeInitialCapabilityIds(parsed.inject_skills) ?? [];
      const injectMcpServerIds = normalizeInitialCapabilityIds(parsed.inject_mcp_server_ids) ?? [];
      const rawSessionOptions =
        parsed.session_options && typeof parsed.session_options === 'object'
          ? (parsed.session_options as Record<string, unknown>)
          : null;
      const sessionOptions: DraftStagedSessionOptions | null = rawSessionOptions
        ? {
            delegation: rawSessionOptions.ultra_mode === true,
            autoReview: rawSessionOptions.verifier_mode === 'on',
            memory: rawSessionOptions.memory_mode === 'on',
            targetAgent:
              typeof rawSessionOptions.target_agent === 'string' && rawSessionOptions.target_agent.trim()
                ? rawSessionOptions.target_agent.trim()
                : null,
          }
        : null;
      const planMode = rawSessionOptions?.plan_mode === true;
      const computeProviders = Array.isArray(parsed.compute_providers)
        ? parsed.compute_providers.filter(
            (provider: unknown): provider is string => typeof provider === 'string' && provider.trim().length > 0
          )
        : [];

      onDraftPrefill?.({
        input,
        files,
        artifactRefs,
        injectSkills,
        injectMcpServerIds,
        sessionOptions,
        planMode,
      });

      if (computeProviders.length === 0) {
        sessionStorage.removeItem(storageKey);
        initialMessageSendsInFlight.delete(storageKey);
        return;
      }

      // Compute-provider restoration is asynchronous. The queued payload stays
      // until it succeeds so a reload or a failed request can retry instead of
      // losing the staged task; late completion is fenced to this effect run.
      let cancelled = false;
      const restoreProviders = async () => {
        try {
          await Promise.all(
            computeProviders.map((provider: string) =>
              setSynonBiomedSessionComputeProvider(conversation_id, provider, true)
            )
          );
          initialMessageSendsInFlight.delete(storageKey);
          if (cancelled) return;
          if (sessionStorage.getItem(storageKey) === storedMessage) {
            sessionStorage.removeItem(storageKey);
          }
        } catch (error) {
          initialMessageSendsInFlight.delete(storageKey);
          if (cancelled) return;
          console.error('[useAcpInitialMessage] Failed to restore compute providers for the staged draft:', error);
          // The staged task itself is already in the composer; keep the queued
          // payload so the next mount retries the provider restore, and tell
          // the user the compute selection needs attention.
          onDraftRestoreIssue?.({ providers: computeProviders });
        }
      };
      void restoreProviders();
      return () => {
        cancelled = true;
      };
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
    onDraftRestoreIssue,
    resetState,
    setAiProcessing,
    streamReady,
    t,
    workspacePath,
  ]);
};

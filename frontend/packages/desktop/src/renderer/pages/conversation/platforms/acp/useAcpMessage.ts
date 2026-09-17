/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { ipcBridge } from '@/common';
import { isErrorTipMessage, transformMessage } from '@/common/chat/chatLib';
import type { AvailableCommand } from '@/common/chat/chatLib';
import { mapAcpCommandsToSlashCommands } from '@/common/chat/slash/acpMapping';
import type { SlashCommandItem } from '@/common/chat/slash/types';
import type { IResponseMessage } from '@/common/adapter/ipcBridge';
import type { TChatConversation, TokenUsageData } from '@/common/config/storage';
import { safeErrorDiagnostic } from '@/common/utils/errorRedaction';
import { useMergeLiveMessage, useUpdateMessageList } from '@/renderer/pages/conversation/Messages/hooks';
import { logStreamTerminalObserved } from '@/renderer/pages/conversation/runtime/useConversationRuntimeView';
import { getConversationOrNull } from '@/renderer/pages/conversation/utils/conversationCache';
import { isConversationProcessing } from '@/renderer/pages/conversation/utils/conversationRuntime';
import { ensureConversationRuntime } from '@/renderer/pages/conversation/utils/ensureConversationRuntime';
import type { ThoughtData } from '@/renderer/components/chat/ThoughtDisplay';
import type { ConversationStreamingFrame } from '@/renderer/services/runtime/conversationStreamingModel';
import { mergeConversationToolOutput } from '@/renderer/services/runtime/conversationTimelineToolOutput';
import { ConversationStreamPublicationFence } from '@/renderer/services/runtime/conversationStreamPublicationFence';
import {
  readConversationStreamingNow,
  useConversationStreaming,
} from '@/renderer/services/runtime/useConversationStreaming';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

export type UseAcpMessageReturn = {
  thought: ThoughtData;
  setThought: React.Dispatch<React.SetStateAction<ThoughtData>>;
  running: boolean;
  streamReady: boolean;
  hasHydratedRunningState: boolean;
  acpStatus: 'connecting' | 'connected' | 'authenticated' | 'session_active' | 'disconnected' | 'error' | null;
  aiProcessing: boolean;
  setAiProcessing: React.Dispatch<React.SetStateAction<boolean>>;
  resetState: () => void;
  tokenUsage: TokenUsageData | null;
  context_limit: number;
  hasThinkingMessage: boolean;
  slashCommands: SlashCommandItem[];
  fetchSlashCommands: () => void;
  /** Last durable message.stream publication accepted for this conversation. */
  getLastStreamActivityAt: () => number;
};

const slashCommandsInFlight = new Map<string, Promise<SlashCommandItem[]>>();

type RequestTraceState = {
  startTime: number;
  backendCategory: 'synon_biomed' | 'acp' | 'anthropic' | 'openai_compatible' | 'unknown';
  modelFamily: 'anthropic' | 'deepseek' | 'gemini' | 'openai' | 'local' | 'unknown';
  sessionCategory: 'default' | 'research' | 'plan' | 'manual' | 'auto' | 'unknown';
};

function classifyRequestBackend(value: unknown): RequestTraceState['backendCategory'] {
  switch (typeof value === 'string' ? value.trim().toLowerCase() : '') {
    case 'synonbiomed':
    case 'synon-biomed':
      return 'synon_biomed';
    case 'acp':
      return 'acp';
    case 'anthropic':
    case 'claude':
    case 'claude-agent-sdk':
      return 'anthropic';
    case 'openai':
    case 'openai-compatible':
    case 'deepseek':
      return 'openai_compatible';
    default:
      return 'unknown';
  }
}

function classifyRequestModel(value: unknown): RequestTraceState['modelFamily'] {
  const model = typeof value === 'string' ? value.trim().toLowerCase() : '';
  if (/^(?:anthropic[/:_-])?claude(?:[/:_-]|$)/.test(model)) return 'anthropic';
  if (/^(?:deepseek[/:_-])/.test(model)) return 'deepseek';
  if (/^(?:google[/:_-])?gemini(?:[/:_-]|$)/.test(model)) return 'gemini';
  if (/^(?:openai[/:_-])?(?:gpt|o[134])(?:[/:_-]|$)/.test(model)) return 'openai';
  if (/^(?:local|ollama|lmstudio)[/:_-]/.test(model)) return 'local';
  return 'unknown';
}

function classifyRequestSession(value: unknown): RequestTraceState['sessionCategory'] {
  const session = typeof value === 'string' ? value.trim().toLowerCase() : '';
  switch (session) {
    case 'default':
    case 'research':
    case 'plan':
    case 'manual':
    case 'auto':
      return session;
    default:
      return 'unknown';
  }
}

function requestTraceStartTime(value: unknown): number {
  const now = Date.now();
  const timestamp = Number(value);
  return Number.isSafeInteger(timestamp) && timestamp >= 0 && timestamp <= now ? timestamp : now;
}

function requestTraceDuration(value: number): number {
  if (!Number.isFinite(value) || value <= 0) return 0;
  return Math.min(Number.MAX_SAFE_INTEGER, Math.round(value));
}

function normalizeThoughtMessage(message: IResponseMessage): IResponseMessage | undefined {
  const data = message.data;
  const record = data && typeof data === 'object' ? (data as Record<string, unknown>) : undefined;
  const status = record?.status === 'done' ? 'done' : 'thinking';
  const content =
    typeof data === 'string'
      ? data
      : data && typeof data === 'object'
        ? [
            (data as { description?: unknown }).description,
            (data as { content?: unknown }).content,
            (data as { text?: unknown }).text,
            (data as { subject?: unknown }).subject,
          ].find((value): value is string => typeof value === 'string' && value.trim().length > 0)
        : undefined;
  if (!content?.trim() && status !== 'done') return undefined;

  const subject =
    data && typeof data === 'object' && typeof (data as { subject?: unknown }).subject === 'string'
      ? (data as { subject: string }).subject.trim()
      : undefined;
  return {
    ...message,
    type: 'thinking',
    data: {
      content: content ?? '',
      subject: subject || undefined,
      ...(typeof record?.duration === 'number' ? { duration: record.duration } : {}),
      ...(typeof record?.duration_ms === 'number' ? { duration_ms: record.duration_ms } : {}),
      status,
    },
  };
}

function logRequestTrace(
  code: 'REQUEST_TRACE_START' | 'REQUEST_TRACE_FINISH' | 'REQUEST_TRACE_ERROR',
  trace: RequestTraceState,
  duration_ms?: number
): void {
  console.log('[RequestTrace]', {
    code,
    backend_category: trace.backendCategory,
    model_family: trace.modelFamily,
    session_category: trace.sessionCategory,
    ...(duration_ms === undefined ? {} : { duration_ms: requestTraceDuration(duration_ms) }),
  });
}

function fetchAcpSlashCommands(conversation_id: string): Promise<SlashCommandItem[]> {
  const existing = slashCommandsInFlight.get(conversation_id);
  if (existing) return existing;

  const promise = ipcBridge.conversation.getSlashCommands
    .invoke({ conversation_id })
    .then((result) => {
      if (!result || !Array.isArray(result) || result.length === 0) return [];
      return mapAcpCommandsToSlashCommands(result);
    })
    .finally(() => {
      if (slashCommandsInFlight.get(conversation_id) === promise) {
        slashCommandsInFlight.delete(conversation_id);
      }
    });
  slashCommandsInFlight.set(conversation_id, promise);
  return promise;
}

export const useAcpMessage = (
  conversation_id: string,
  options?: {
    skipWarmup?: boolean;
    initialConversation?: TChatConversation;
  }
): UseAcpMessageReturn => {
  const mergeLiveMessage = useMergeLiveMessage();
  const updateMessageList = useUpdateMessageList();
  const [running, setRunning] = useState(false);
  const [streamSubscriptionConversationId, setStreamSubscriptionConversationId] = useState<string | null>(null);
  const [hasHydratedRunningState, setHasHydratedRunningState] = useState(false);
  const [thought, setThought] = useState<ThoughtData>({
    description: '',
    subject: '',
  });
  const [acpStatus, setAcpStatus] = useState<
    'connecting' | 'connected' | 'authenticated' | 'session_active' | 'disconnected' | 'error' | null
  >(null);
  const [aiProcessing, setAiProcessing] = useState(false); // New loading state for AI response
  const [tokenUsage, setTokenUsage] = useState<TokenUsageData | null>(null);
  const [context_limit, setContextLimit] = useState<number>(0);
  const [slashCommands, setSlashCommands] = useState<SlashCommandItem[]>([]);

  // Use refs to sync state for immediate access in event handlers
  const runningRef = useRef(running);
  const aiProcessingRef = useRef(aiProcessing);
  // Track whether current turn has content output
  const hasContentInTurnRef = useRef(false);

  // Guard: after finish arrives, prevent auto-recover from setting running=true
  // until a new 'start' signal arrives for the next turn
  const turnFinishedRef = useRef(false);
  const terminalMessageIdsRef = useRef(new Set<string>());
  const requiresExplicitStartRef = useRef(false);

  // Track whether current turn has a thinking message in the conversation
  const hasThinkingMessageRef = useRef(false);
  const [hasThinkingMessage, setHasThinkingMessage] = useState(false);
  const activeThinkingRef = useRef<{ msgId: string; startedAt: number } | null>(null);
  const publicationFenceRef = useRef(new ConversationStreamPublicationFence());
  const publicationConversationRef = useRef(conversation_id);
  const lastStreamActivityAtRef = useRef(Date.now());
  const liveToolStdout = useConversationStreaming(conversation_id);

  // Track request trace state for displaying complete request lifecycle
  const requestTraceRef = useRef<RequestTraceState | null>(null);
  const clearRequestTrace = useCallback(() => {
    requestTraceRef.current = null;
  }, []);
  const finalizeRequestTrace = useCallback((code: 'REQUEST_TRACE_FINISH' | 'REQUEST_TRACE_ERROR') => {
    const trace = requestTraceRef.current;
    requestTraceRef.current = null;
    if (!trace) return;
    logRequestTrace(code, trace, Date.now() - trace.startTime);
  }, []);

  // Throttle thought updates to reduce render frequency
  const thoughtThrottleRef = useRef<{
    lastUpdate: number;
    pending: ThoughtData | null;
    timer: ReturnType<typeof setTimeout> | null;
  }>({ lastUpdate: 0, pending: null, timer: null });

  const throttledSetThought = useMemo(() => {
    const THROTTLE_MS = 50;
    return (data: ThoughtData) => {
      const now = Date.now();
      const ref = thoughtThrottleRef.current;
      if (now - ref.lastUpdate >= THROTTLE_MS) {
        ref.lastUpdate = now;
        ref.pending = null;
        if (ref.timer) {
          clearTimeout(ref.timer);
          ref.timer = null;
        }
        setThought(data);
      } else {
        ref.pending = data;
        if (!ref.timer) {
          ref.timer = setTimeout(
            () => {
              ref.lastUpdate = Date.now();
              ref.timer = null;
              if (ref.pending) {
                setThought(ref.pending);
                ref.pending = null;
              }
            },
            THROTTLE_MS - (now - ref.lastUpdate)
          );
        }
      }
    };
  }, []);

  const clearThought = useCallback(() => {
    const ref = thoughtThrottleRef.current;
    ref.pending = null;
    if (ref.timer) {
      clearTimeout(ref.timer);
      ref.timer = null;
    }
    setThought((current) => (current.subject || current.description ? { subject: '', description: '' } : current));
  }, []);

  // Clean up throttle timer
  useEffect(() => {
    return () => {
      if (thoughtThrottleRef.current.timer) {
        clearTimeout(thoughtThrottleRef.current.timer);
      }
    };
  }, []);

  const applyToolStdoutSnapshot = useCallback(
    (frames: ConversationStreamingFrame[]) => {
      updateMessageList((messages) => mergeConversationToolOutput(messages, conversation_id, frames));
    },
    [conversation_id, updateMessageList]
  );

  const completeActiveThinking = useCallback(
    (
      boundaryMessage: Pick<IResponseMessage, 'conversation_id' | 'created_at'>,
      completeOptions?: {
        duration?: number;
      }
    ) => {
      const activeThinking = activeThinkingRef.current;
      if (!activeThinking) return;

      const endTime = boundaryMessage.created_at ?? Date.now();
      const duration = completeOptions?.duration ?? Math.max(0, endTime - activeThinking.startedAt);

      mergeLiveMessage({
        id: `${activeThinking.msgId}-thinking-done`,
        type: 'thinking',
        msg_id: activeThinking.msgId,
        conversation_id: boundaryMessage.conversation_id,
        position: 'left',
        created_at: endTime,
        content: {
          content: '',
          duration,
          status: 'done',
        },
      });

      activeThinkingRef.current = null;
    },
    [mergeLiveMessage]
  );

  const handleResponseMessage = useCallback(
    (message: IResponseMessage) => {
      if (conversation_id !== message.conversation_id) {
        return;
      }
      if (publicationConversationRef.current !== conversation_id) {
        publicationFenceRef.current.reset();
        publicationConversationRef.current = conversation_id;
      }
      if (!publicationFenceRef.current.accept(message)) return;

      if (message.type !== 'start') {
        if (requiresExplicitStartRef.current) return;
        // A later attempt may already be active, but data from any completed
        // attempt remains permanently fenced by its durable message id.
        if (terminalMessageIdsRef.current.has(message.msg_id)) return;
        if (turnFinishedRef.current) {
          // Resume/retry attempts have a new durable assistant msg_id. Accept
          // that authority even when an older client missed the attempt-start
          // event, while continuing to fence late data from terminal attempts.
          turnFinishedRef.current = false;
          hasContentInTurnRef.current = false;
        }
      }

      if (message.type === 'skill_suggest' || message.type === 'cron_trigger') {
        return;
      }

      if (isErrorTipMessage(message)) {
        finalizeRequestTrace('REQUEST_TRACE_ERROR');
        terminalMessageIdsRef.current.add(message.msg_id);
        turnFinishedRef.current = true;
        setRunning(false);
        runningRef.current = false;
        setAiProcessing(false);
        aiProcessingRef.current = false;
        clearThought();
        hasContentInTurnRef.current = false;
        hasThinkingMessageRef.current = false;
        activeThinkingRef.current = null;
        setHasThinkingMessage(false);
        const transformedMessage = transformMessage(message);
        lastStreamActivityAtRef.current = Date.now();
        if (transformedMessage) {
          mergeLiveMessage(
            transformedMessage,
            false,
            message.artifact_refs !== undefined
              ? {
                  msgId: message.msg_id,
                  references: message.artifact_refs,
                  mode: 'replace',
                }
              : undefined
          );
        }
        return;
      }

      const shouldCompleteThinking =
        activeThinkingRef.current &&
        ![
          'thought',
          'thinking',
          'start',
          'request_trace',
          'acp_context_usage',
          'acp_model_info',
          'codex_model_info',
          'available_commands',
          'slash_commands_updated',
          'agent_status',
          'user_content',
        ].includes(message.type);

      if (shouldCompleteThinking) {
        completeActiveThinking(message);
      }

      const transformedMessage = transformMessage(message);
      // Count activity only after the protocol message has been transformed
      // successfully. A malformed publication must not keep the stale-stream
      // recovery watchdog quiet while the visible projection is frozen.
      lastStreamActivityAtRef.current = Date.now();
      switch (message.type) {
        // `thought` is accepted only as an inbound provider alias. Both names
        // immediately converge on the same typed thinking reducer.
        case 'thought':
        case 'thinking': {
          const normalizedThinking = normalizeThoughtMessage(message);
          const thinkingMessage = normalizedThinking ? transformMessage(normalizedThinking) : undefined;
          const thinkingData = normalizedThinking?.data as
            | {
                status?: string;
                duration?: number;
                duration_ms?: number;
              }
            | undefined;
          if (thinkingData?.status === 'done') {
            if (activeThinkingRef.current?.msgId === message.msg_id) {
              completeActiveThinking(message, {
                duration: thinkingData.duration ?? thinkingData.duration_ms,
              });
            }
            break;
          }

          // Only set running for active thinking, not for done signal
          if (!runningRef.current && !turnFinishedRef.current) {
            setRunning(true);
            runningRef.current = true;
          }
          if (!activeThinkingRef.current) {
            activeThinkingRef.current = {
              msgId: message.msg_id,
              startedAt: message.created_at ?? Date.now(),
            };
          } else if (activeThinkingRef.current.msgId !== message.msg_id) {
            activeThinkingRef.current = {
              msgId: message.msg_id,
              startedAt: message.created_at ?? Date.now(),
            };
          }
          hasThinkingMessageRef.current = true;
          setHasThinkingMessage(true);
          if (thinkingMessage) mergeLiveMessage(thinkingMessage);
          break;
        }
        case 'start':
          // New turn starting — clear the finished guard and content flag
          clearRequestTrace();
          requiresExplicitStartRef.current = false;
          turnFinishedRef.current = false;
          hasContentInTurnRef.current = false;
          setRunning(true);
          runningRef.current = true;
          if (message.artifact_refs !== undefined) {
            mergeLiveMessage(undefined, false, {
              msgId: message.msg_id,
              references: message.artifact_refs,
              mode: 'union',
            });
          }
          // Don't reset aiProcessing here - let content arrival handle it
          break;
        case 'finish':
          {
            if (message.artifact_refs !== undefined) {
              mergeLiveMessage(undefined, false, {
                msgId: message.msg_id,
                references: message.artifact_refs,
                mode: 'replace',
              });
            }
            logStreamTerminalObserved(conversation_id, message.turn_id, 'acp', message.type);
            // Mark turn as finished to prevent auto-recover from late messages
            terminalMessageIdsRef.current.add(message.msg_id);
            turnFinishedRef.current = true;
            // Immediate state reset (notification is handled by centralized hook)
            setRunning(false);
            runningRef.current = false;
            setAiProcessing(false);
            aiProcessingRef.current = false;
            clearThought();
            hasContentInTurnRef.current = false;
            hasThinkingMessageRef.current = false;
            activeThinkingRef.current = null;
            setHasThinkingMessage(false);
            // Log request completion
            finalizeRequestTrace('REQUEST_TRACE_FINISH');
          }
          break;
        case 'text':
        case 'content': {
          // First content token — AI has started responding, clear processing indicator
          if (!hasContentInTurnRef.current) {
            hasContentInTurnRef.current = true;
            setAiProcessing(false);
            aiProcessingRef.current = false;
          }
          // Auto-recover running state only if turn hasn't finished
          if (!runningRef.current && !turnFinishedRef.current) {
            setRunning(true);
            runningRef.current = true;
          }
          // Clear thought when final answer arrives
          clearThought();
          if (transformedMessage) mergeLiveMessage(transformedMessage);
          break;
        }
        case 'tool_call': {
          if (!runningRef.current && !turnFinishedRef.current) {
            setRunning(true);
            runningRef.current = true;
          }
          if (transformedMessage) {
            const [withBufferedStdout] = mergeConversationToolOutput(
              [transformedMessage],
              conversation_id,
              readConversationStreamingNow(conversation_id)
            );
            mergeLiveMessage(withBufferedStdout ?? transformedMessage);
          }
          break;
        }
        case 'agent_status': {
          // Auto-recover running state only if turn hasn't finished
          if (!runningRef.current && !turnFinishedRef.current) {
            setRunning(true);
            runningRef.current = true;
          }
          // Update ACP/Agent status
          const agentData = message.data as {
            status?: 'connecting' | 'connected' | 'authenticated' | 'session_active' | 'disconnected' | 'error';
            backend?: string;
          };
          if (agentData?.status) {
            setAcpStatus(agentData.status);
            // Reset running state when authentication is complete
            if (['authenticated', 'session_active'].includes(agentData.status)) {
              setRunning(false);
              runningRef.current = false;
            }
            // Reset all loading states on error or disconnect so UI doesn't stay stuck
            if (['error', 'disconnected'].includes(agentData.status)) {
              setRunning(false);
              runningRef.current = false;
              setAiProcessing(false);
              aiProcessingRef.current = false;
            }
          }
          mergeLiveMessage(transformedMessage);
          break;
        }
        case 'user_content':
          mergeLiveMessage(transformedMessage);
          break;
        case 'acp_permission':
          // Auto-recover running state only if turn hasn't finished
          if (!runningRef.current && !turnFinishedRef.current) {
            setRunning(true);
            runningRef.current = true;
          }
          mergeLiveMessage(transformedMessage);
          break;
        case 'acp_model_info':
          // Model info updates are handled by SynonBiomedModelSelector, no action needed here.
          break;
        case 'slash_commands_updated':
          // Slash commands became available (often during bootstrap when
          // agent_status events are suppressed). Update acpStatus so
          // useSlashCommands re-fetches.
          setAcpStatus((prev) => prev ?? 'session_active');
          break;
        case 'available_commands': {
          const cmdData = message.data as { commands?: AvailableCommand[] };
          if (cmdData?.commands && Array.isArray(cmdData.commands)) {
            setSlashCommands(mapAcpCommandsToSlashCommands(cmdData.commands));
          }
          break;
        }
        case 'acp_context_usage': {
          const usageData = message.data as { used: number; size: number };
          if (usageData && typeof usageData.used === 'number') {
            setTokenUsage({ total_tokens: usageData.used });
            if (usageData.size > 0) {
              setContextLimit(usageData.size);
            }
          }
          break;
        }
        case 'request_trace':
          {
            const trace = message.data as Record<string, unknown>;
            requestTraceRef.current = {
              startTime: requestTraceStartTime(trace.timestamp),
              backendCategory: classifyRequestBackend(trace.backend),
              modelFamily: classifyRequestModel(trace.model_id),
              sessionCategory: classifyRequestSession(trace.session_mode),
            };
            logRequestTrace('REQUEST_TRACE_START', requestTraceRef.current);
          }
          break;
        case 'error':
          logStreamTerminalObserved(conversation_id, message.turn_id, 'acp', message.type);
          // Stop all loading states when error occurs
          terminalMessageIdsRef.current.add(message.msg_id);
          turnFinishedRef.current = true;
          setRunning(false);
          runningRef.current = false;
          setAiProcessing(false);
          aiProcessingRef.current = false;
          activeThinkingRef.current = null;
          mergeLiveMessage(
            transformedMessage,
            false,
            message.artifact_refs !== undefined
              ? {
                  msgId: message.msg_id,
                  references: message.artifact_refs,
                  mode: 'replace',
                }
              : undefined
          );
          // Log request error
          finalizeRequestTrace('REQUEST_TRACE_ERROR');
          break;
        default:
          if (!transformedMessage) break;
          // Auto-recover running state only if turn hasn't finished
          if (!runningRef.current && !turnFinishedRef.current) {
            setRunning(true);
            runningRef.current = true;
          }
          mergeLiveMessage(transformedMessage);
          break;
      }
    },
    [
      conversation_id,
      clearRequestTrace,
      mergeLiveMessage,
      completeActiveThinking,
      finalizeRequestTrace,
      throttledSetThought,
      clearThought,
      setRunning,
      setAiProcessing,
      setAcpStatus,
    ]
  );

  useEffect(() => {
    const unsubscribe = ipcBridge.acpConversation.responseStream.on(handleResponseMessage);
    setStreamSubscriptionConversationId(conversation_id);
    return () => {
      setStreamSubscriptionConversationId((current) => (current === conversation_id ? null : current));
      clearRequestTrace();
      unsubscribe();
    };
  }, [clearRequestTrace, handleResponseMessage]);

  useEffect(() => {
    applyToolStdoutSnapshot(liveToolStdout);
  }, [applyToolStdoutSnapshot, liveToolStdout]);

  // Reset state when conversation changes and restore actual running status
  useEffect(() => {
    let cancelled = false;

    clearRequestTrace();
    clearThought();
    setAcpStatus(null);
    setTokenUsage(null);
    setContextLimit(0);
    setSlashCommands([]);
    hasContentInTurnRef.current = false;
    turnFinishedRef.current = false;
    terminalMessageIdsRef.current.clear();
    lastStreamActivityAtRef.current = Date.now();
    requiresExplicitStartRef.current = false;
    hasThinkingMessageRef.current = false;
    activeThinkingRef.current = null;
    applyToolStdoutSnapshot([]);
    setHasThinkingMessage(false);
    setHasHydratedRunningState(false);

    // Clear running/processing immediately for the new conversation. Hydration only
    // turns these back on when the backend reports runtime processing state. Otherwise
    // conversation.get's idle branch raced with useAcpInitialMessage's
    // setAiProcessing(true) and hid ThoughtDisplay until the first stream event.
    setRunning(false);
    runningRef.current = false;
    setAiProcessing(false);
    aiProcessingRef.current = false;

    const initialConversation =
      options?.initialConversation?.id === conversation_id ? options.initialConversation : undefined;
    const conversationRequest = initialConversation
      ? Promise.resolve(initialConversation)
      : getConversationOrNull(conversation_id);

    void conversationRequest
      .then((res) => {
        if (cancelled) {
          return;
        }

        if (!res) {
          setRunning(false);
          runningRef.current = false;
          setAiProcessing(false);
          aiProcessingRef.current = false;
          setHasHydratedRunningState(true);
          return;
        }
        const isRunning = isConversationProcessing(res);
        setRunning(isRunning);
        runningRef.current = isRunning;
        if (isRunning) {
          setAiProcessing(true);
          aiProcessingRef.current = true;
        }
        setHasHydratedRunningState(true);

        // Restore persisted context usage data
        if (res.type === 'acp' && res.extra?.last_token_usage) {
          const { last_token_usage, last_context_limit } = res.extra;
          if (last_token_usage.total_tokens > 0) {
            setTokenUsage(last_token_usage);
          }
          if (last_context_limit && last_context_limit > 0) {
            setContextLimit(last_context_limit);
          }
        }
      })
      .catch((error: unknown) => {
        if (cancelled) return;
        setRunning(false);
        runningRef.current = false;
        setAiProcessing(false);
        aiProcessingRef.current = false;
        setHasHydratedRunningState(true);

        if (error instanceof TypeError && error.message.includes('Failed to fetch')) {
          console.warn('[useAcpMessage] Failed to hydrate conversation state:', safeErrorDiagnostic(error));
          return;
        }

        throw error;
      });

    return () => {
      cancelled = true;
    };
  }, [applyToolStdoutSnapshot, clearRequestTrace, clearThought, conversation_id, options?.initialConversation]);

  // Fetch slash commands via HTTP after runtime ensure completes.
  // WebSocket push of available_commands arrives during warmup when no
  // StreamRelay is listening, so the initial load must come from HTTP.
  useEffect(() => {
    if (options?.skipWarmup) return;
    let cancelled = false;
    void ensureConversationRuntime(conversation_id)
      .then(() => {
        if (cancelled) return;
        return fetchAcpSlashCommands(conversation_id);
      })
      .then((commands) => {
        if (cancelled) return;
        if (!commands?.length) return;
        setSlashCommands(commands);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [conversation_id, options?.skipWarmup]);

  const resetState = useCallback(() => {
    clearRequestTrace();
    requiresExplicitStartRef.current = true;
    turnFinishedRef.current = true;
    setRunning(false);
    runningRef.current = false;
    setAiProcessing(false);
    aiProcessingRef.current = false;
    clearThought();
    hasContentInTurnRef.current = false;
    hasThinkingMessageRef.current = false;
    activeThinkingRef.current = null;
    setHasThinkingMessage(false);
    applyToolStdoutSnapshot([]);
  }, [applyToolStdoutSnapshot, clearRequestTrace, clearThought]);

  const fetchSlashCommands = useCallback(() => {
    void ensureConversationRuntime(conversation_id)
      .then(() => fetchAcpSlashCommands(conversation_id))
      .then((commands) => {
        if (!commands.length) return;
        setSlashCommands(commands);
      })
      .catch(() => {});
  }, [conversation_id]);

  const getLastStreamActivityAt = useCallback(() => lastStreamActivityAtRef.current, []);

  return {
    thought,
    setThought,
    running,
    streamReady: streamSubscriptionConversationId === conversation_id,
    hasHydratedRunningState,
    acpStatus,
    aiProcessing,
    setAiProcessing,
    resetState,
    tokenUsage,
    context_limit,
    hasThinkingMessage,
    slashCommands,
    fetchSlashCommands,
    getLastStreamActivityAt,
  };
};

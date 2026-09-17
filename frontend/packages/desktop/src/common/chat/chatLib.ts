/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { AcpPermissionRequest, PlanUpdate, ToolCallUpdate } from '@/common/types/platform/acpTypes';
import type { AcpAvailableCommand } from '@/common/chat/slash/types';
import type { ArtifactReferenceWire, IResponseMessage, ToolLifecycleStatus } from '../adapter/messageStreamProtocol';
import { uuid } from '../utils';
import { sanitizeAcpToolCallContent, sanitizeAcpToolUpdate } from './acpToolCallOutput';
import {
  isTextPublicationCovered,
  mergeTextPublicationCoverage,
  type TextPublicationRange,
} from './textPublicationCoverage';
export { isTextPublicationCovered, mergeTextPublicationCoverage } from './textPublicationCoverage';

export { sanitizeAcpToolCallContent } from './acpToolCallOutput';

const normalizePath = (path: string): string => path.replace(/\\/g, '/');

/**
 * 安全的路径拼接函数，兼容Windows和Mac
 * @param basePath 基础路径
 * @param relativePath 相对路径
 * @returns 拼接后的绝对路径
 */
export const joinPath = (basePath: string, relativePath: string): string => {
  const base = normalizePath(basePath);
  const relative = normalizePath(relativePath);

  // 去掉base路径末尾的斜杠
  const cleanBase = base.replace(/\/+$/, '');

  // 处理相对路径中的 ./ 和 ../
  const parts = relative.split('/');
  const resultParts = [];

  for (const part of parts) {
    if (part === '.' || part === '') {
      continue; // 跳过 . 和空字符串
    } else if (part === '..') {
      // 处理上级目录
      if (resultParts.length > 0) {
        resultParts.pop(); // 移除最后一个部分
      }
    } else {
      resultParts.push(part);
    }
  }

  // 拼接路径
  const result = cleanBase + '/' + resultParts.join('/');

  // 确保路径格式正确
  return result.replace(/\/+/g, '/'); // 将多个连续的斜杠替换为单个
};

/**
 * @description 跟对话相关的消息类型申明 及相关处理
 */

type TMessageType =
  | 'text'
  | 'tips'
  | 'tool_call'
  | 'tool_group'
  | 'agent_status'
  | 'permission'
  | 'acp_permission'
  | 'acp_tool_call'
  | 'plan'
  | 'thinking'
  | 'available_commands';

interface IMessage<T extends TMessageType, Content> {
  /**
   * 唯一ID
   */
  id: string;
  /**
   * 消息来源ID，
   */
  msg_id?: string;

  /** Durable coordinate of a live increment, retained through normalization. */
  source_publication_sequence?: number;
  /** Accepted history snapshot coverage; not the message creation sequence. */
  history_coverage_through?: number;
  /** Locally applied text publications, compressed without covering unseen gaps. */
  text_publication_ranges?: readonly TextPublicationRange[];

  //消息会话ID
  conversation_id: string;
  /**
   * 消息类型
   */
  type: T;
  /**
   * 消息内容
   */
  content: Content;
  /**
   * 消息创建时间
   */
  created_at?: number;
  /**
   * 消息位置
   */
  position?: 'left' | 'right' | 'center' | 'pop';
  /**
   * 消息状态
   */
  status?: 'finish' | 'pending' | 'error' | 'work';
  /** Durable terminal state projected by the Synon Biomed transcript backend. */
  terminal_status?: 'completed' | 'failed' | 'cancelled';
  /** True after a later attempt for the same logical input has been accepted. */
  terminal_superseded?: boolean;
  /**
   * Hidden from UI display but persisted to DB and sent to agent.
   */
  hidden?: boolean;
  /** Canonical artifact/version identities attached to this persisted message. */
  artifact_refs?: ArtifactReferenceWire[];
}

export type CronMessageMeta = {
  source: 'cron';
  cron_job_id: string;
  cron_job_name: string;
  triggered_at: number;
};

export type SynonBiomedMessageCoordinates = {
  messageIndex: number;
  blockIndex: number;
  branchId: string | null;
};

export type IMessageText = IMessage<
  'text',
  {
    content: string;
    /** Backend explicitly replaced the accumulated text for this msg_id. */
    replace?: boolean;
    /** Replace all assistant text segments owned by assistantAttemptId. */
    replaceScope?: 'attempt';
    /** Exact durable attempt identity; never inferred from msg_id prefixes. */
    assistantAttemptId?: string;
    cronMeta?: CronMessageMeta;
    synonBiomed?: SynonBiomedMessageCoordinates;
  }
>;

export type AgentErrorOwnership = 'synon-ai' | 'user_agent' | 'user_llm_provider' | 'unknown_upstream';

export type AgentErrorResolutionKind =
  | 'retry'
  | 'wait_for_current_response'
  | 'start_new_session'
  | 'reconnect_agent'
  | 'check_agent_login'
  | 'check_agent_installation'
  | 'check_agent_version'
  | 'check_local_command'
  | 'check_provider_credentials'
  | 'check_provider_billing'
  | 'check_provider_base_url'
  | 'change_model'
  | 'reduce_context'
  | 'send_feedback';

export type AgentErrorResolutionTarget = 'provider_settings' | 'agent_settings' | 'new_conversation' | 'feedback';

export type AgentErrorResolution = {
  kind: AgentErrorResolutionKind;
  target?: AgentErrorResolutionTarget;
};

/** Redacted, size-bounded summary of the original error, for telemetry only. */
export type AgentStreamRawErrorSummary = {
  name?: string;
  message?: string;
  code?: string;
  status?: number;
  stack?: string;
};

export type AgentStreamErrorInfo = {
  message: string;
  code?: string;
  ownership?: AgentErrorOwnership;
  detail?: string;
  workspacePath?: string;
  retryable?: boolean;
  feedback_recommended?: boolean;
  resolution?: AgentErrorResolution;
  /**
   * Diagnostic summary of the original underlying error, preserved on
   * unclassified ("internal") failures so they can be located in telemetry.
   * Redacted of secrets/PII before it reaches here.
   */
  rawError?: AgentStreamRawErrorSummary;
};

export type IMessageTips = IMessage<
  'tips',
  {
    content: string;
    type: 'error' | 'info' | 'success' | 'warning';
    code?: string;
    params?: Record<string, unknown>;
    error?: AgentStreamErrorInfo;
  }
>;

export const isErrorTipMessage = (message: IResponseMessage): boolean => {
  if (message.type !== 'tips' || !message.data || typeof message.data !== 'object') {
    return false;
  }

  const tipData = message.data as { type?: unknown };
  return tipData.type === 'error';
};

export type IToolCallSubagent = {
  ordinal: number;
  frameId?: string;
  rootFrameId?: string;
  parentFrameId?: string;
  agentName?: string;
  delegateName?: string;
  status?: string;
  statusDescription?: string;
  taskSummary?: string;
  messageCount?: number;
  latestAction?: string;
  superseded?: boolean;
};

export type IToolCallSubagentEvent = {
  kind: 'completion' | 'question' | 'info';
  frameId: string;
  ordinal: number;
  childName: string;
  text?: string;
  bullets?: string[];
  wallSeconds?: number;
};

export type IMessageToolCall = IMessage<
  'tool_call',
  {
    call_id: string;
    name: string;
    args: Record<string, unknown> | string;
    error?: string;
    status?: ToolLifecycleStatus;
    input?: Record<string, unknown> | string;
    output?: string;
    description?: string;
    attempt?: number;
    operation_id?: string;
    parent_operation_id?: string;
    revision?: number;
    phase?: string;
    settlement_reason?: string;
    /** Linked child-frame state for Synon Biomed delegate/send_message turns. */
    subagent?: IToolCallSubagent;
    /** Completion and message notifications returned by Synon Biomed child frames. */
    subagentEvents?: IToolCallSubagentEvent[];
    /** Live Synon Biomed stdout is merged into this durable tool call. */
    streaming?: boolean;
    /** The auxiliary stdout snapshot could not be recovered after a gap or reconnect. */
    streamingRecoveryError?: boolean;
    /** Observed progress for the same durable tool call. */
    progress?: {
      phase: string;
      message?: string;
      phasePercent?: number;
      bytesPerSecond?: number;
      bytesCompleted?: number;
      bytesTotal?: number;
      completedItems?: number;
      totalItems?: number;
      elapsedMs?: number;
      indeterminate?: boolean;
    };
    /** Immutable source coordinates for canonical Transcript history actions. */
    synonBiomed?: SynonBiomedMessageCoordinates;
  }
>;

type IMessageToolGroupConfirmationDetailsBase<Type, Extra extends object> = {
  type: Type;
  title: string;
} & Extra;

export type IMessageToolGroup = IMessage<
  'tool_group',
  Array<{
    call_id: string;
    description: string;
    name: string;
    render_output_as_markdown: boolean;
    result_display?:
      | string
      | {
          file_diff: string;
          file_name: string;
        }
      | {
          img_url: string;
          relative_path: string;
        };
    status: 'Executing' | 'Success' | 'Error' | 'Canceled' | 'Pending' | 'Confirming';
    confirmationDetails?:
      | IMessageToolGroupConfirmationDetailsBase<
          'edit',
          {
            file_name: string;
            file_diff: string;
            isModifying?: boolean;
          }
        >
      | IMessageToolGroupConfirmationDetailsBase<
          'exec',
          {
            rootCommand: string;
            command: string;
          }
        >
      | IMessageToolGroupConfirmationDetailsBase<
          'info',
          {
            urls?: string[];
            prompt: string;
          }
        >
      | IMessageToolGroupConfirmationDetailsBase<
          'mcp',
          {
            tool_name: string;
            tool_display_name: string;
            server_name: string;
          }
        >;
  }>
>;

// Unified agent status message type for all ACP-based agents (Claude, Qwen, Codex, etc.)
export type IMessageAgentStatus = IMessage<
  'agent_status',
  {
    backend: string; // Agent identifier: 'claude', 'qwen', 'codex', 'remote', etc.
    status: 'connecting' | 'connected' | 'authenticated' | 'session_active' | 'error';
    /** Display name for the agent (e.g. extension-contributed adapter name) / Agent 显示名称 */
    agent_name?: string;
    // Optional legacy fields for backward compatibility
    session_id?: string;
    is_connected?: boolean;
    has_active_session?: boolean;
  }
>;

export type IMessageAcpPermission = IMessage<'acp_permission', AcpPermissionRequest>;

export type IMessagePermission = IMessage<'permission', IConfirmation>;

export type IMessageAcpToolCall = IMessage<'acp_tool_call', ToolCallUpdate>;

export const mergeAcpToolCallContent = (
  existing: IMessageAcpToolCall['content'],
  incoming: IMessageAcpToolCall['content']
): IMessageAcpToolCall['content'] => ({
  ...existing,
  ...incoming,
  update: sanitizeAcpToolUpdate({
    ...existing.update,
    ...incoming.update,
  }),
});

export const isTextContentReplacement = (content: IMessageText['content'] | undefined): boolean =>
  content?.replace === true;

export const mergeTextMessageContent = (
  existing: IMessageText['content'],
  incoming: IMessageText['content']
): IMessageText['content'] => {
  const { replace: _existingReplace, ...existingRest } = existing;
  const { replace: incomingReplace, ...incomingRest } = incoming;

  return {
    ...existingRest,
    ...incomingRest,
    content: incomingReplace ? incoming.content : existing.content + incoming.content,
    ...(incomingReplace ? { replace: true } : {}),
  };
};

export type ArtifactReferenceMergeMode = 'union' | 'replace';

export const mergeArtifactReferences = (
  existing: ArtifactReferenceWire[] | undefined,
  incoming: ArtifactReferenceWire[] | undefined,
  mode: ArtifactReferenceMergeMode
): ArtifactReferenceWire[] | undefined => {
  if (incoming === undefined) return existing;
  if (mode === 'replace') return incoming.map((reference) => ({ ...reference }));
  const merged = new Map<string, ArtifactReferenceWire>();
  for (const reference of existing ?? []) {
    merged.set(`${reference.artifact_id}\0${reference.version_id}`, {
      ...reference,
    });
  }
  for (const reference of incoming) {
    merged.set(`${reference.artifact_id}\0${reference.version_id}`, {
      ...reference,
    });
  }
  return Array.from(merged.values());
};

export const patchTextMessageArtifactReferences = (
  list: TMessage[],
  msgId: string,
  incoming: ArtifactReferenceWire[],
  mode: ArtifactReferenceMergeMode
): TMessage[] => {
  for (let index = list.length - 1; index >= 0; index--) {
    const message = list[index];
    if (message.type !== 'text' || message.msg_id !== msgId) continue;
    const next = list.slice();
    next[index] = {
      ...message,
      artifact_refs: mergeArtifactReferences(message.artifact_refs, incoming, mode),
    };
    return next;
  }
  return list;
};

export const preferTextMessageVersion = (primary: IMessageText, secondary: IMessageText): IMessageText => {
  const primaryIsReplace = isTextContentReplacement(primary.content);
  const secondaryIsReplace = isTextContentReplacement(secondary.content);
  const primaryHasAuthoritativeTerminalContent =
    primary.terminal_status !== undefined &&
    primary.terminal_superseded !== true &&
    primary.content.content.trim().length > 0;
  const secondaryWouldEraseTerminalContent =
    secondary.content.content.trim().length === 0 && secondary.terminal_superseded !== true;

  const selected = isTextPublicationCovered(primary, secondary)
    ? primary
    : primaryHasAuthoritativeTerminalContent && secondaryWouldEraseTerminalContent
      ? primary
      : primaryIsReplace !== secondaryIsReplace
        ? primaryIsReplace
          ? primary
          : secondary
        : secondary.content.content.length > primary.content.content.length
          ? secondary
          : primary;
  const terminalSuperseded = primary.terminal_superseded === true || secondary.terminal_superseded === true;
  const terminalState = primary.terminal_status
    ? {
        status: terminalSuperseded
          ? ('finish' as const)
          : (primary.status ?? (primary.terminal_status === 'failed' ? ('error' as const) : ('finish' as const))),
        terminal_status: primary.terminal_status,
        ...(terminalSuperseded ? { terminal_superseded: true } : {}),
      }
    : undefined;
  const reconciledReferences = mergeArtifactReferences(secondary.artifact_refs, primary.artifact_refs, 'union');
  const publicationCoverage = mergeTextPublicationCoverage(primary, secondary);
  if (
    terminalState === undefined &&
    reconciledReferences === undefined &&
    Object.keys(publicationCoverage).length === 0
  )
    return selected;
  return {
    ...selected,
    ...publicationCoverage,
    ...terminalState,
    ...(reconciledReferences === undefined ? {} : { artifact_refs: reconciledReferences }),
  };
};

export type IMessagePlan = IMessage<
  'plan',
  {
    session_id: string;
    entries: PlanUpdate['update']['entries'];
  }
>;

export type IMessageThinking = IMessage<
  'thinking',
  {
    content: string;
    subject?: string;
    duration?: number;
    replace?: boolean;
    status: 'thinking' | 'done';
  }
>;

// Available commands from ACP agents (Claude, etc.)
export type AvailableCommand = AcpAvailableCommand;

export type IMessageAvailableCommands = IMessage<
  'available_commands',
  {
    commands: AvailableCommand[];
  }
>;

// eslint-disable-next-line max-len
export type TMessage =
  | IMessageText
  | IMessageTips
  | IMessageToolCall
  | IMessageToolGroup
  | IMessageAgentStatus
  | IMessagePermission
  | IMessageAcpPermission
  | IMessageAcpToolCall
  | IMessagePlan
  | IMessageThinking
  | IMessageAvailableCommands;

// 统一所有需要用户交互的用户类型
export interface IConfirmation<Option = unknown> {
  title?: string;
  id: string;
  action?: string;
  description: string;
  call_id: string;
  options: Array<{
    label: string;
    value: Option;
    params?: Record<string, string>; // Translation interpolation parameters
  }>;
  /**
   * Command type for exec confirmations (e.g., 'curl', 'npm', 'git')
   * Used for "always allow" permission memory
   */
  command_type?: string;
}

const isObject = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value);

type RawTextMessageContent = {
  content?: unknown;
  replace?: unknown;
  replace_scope?: unknown;
  replaceScope?: unknown;
  assistant_attempt_id?: unknown;
  assistantAttemptId?: unknown;
  cronMeta?: unknown;
  synonBiomed?: unknown;
};

type NormalizeTextMessageContentOptions = {
  replace?: boolean;
  replaceScope?: 'attempt';
  assistantAttemptId?: string;
};

const isAssistantAttemptId = (value: unknown): value is string =>
  typeof value === 'string' &&
  value.length > 0 &&
  value.length <= 256 &&
  value.trim() === value &&
  !Array.from(value).some((character) => {
    const codePoint = character.codePointAt(0) ?? 0;
    return codePoint <= 0x1f || codePoint === 0x7f;
  });

const isCronMessageMeta = (value: unknown): value is CronMessageMeta =>
  isObject(value) &&
  value.source === 'cron' &&
  typeof value.cron_job_id === 'string' &&
  typeof value.cron_job_name === 'string' &&
  typeof value.triggered_at === 'number';

const isSynonBiomedMessageCoordinates = (
  value: unknown
): value is NonNullable<IMessageText['content']['synonBiomed']> =>
  isObject(value) &&
  Number.isSafeInteger(value.messageIndex) &&
  Number(value.messageIndex) >= 0 &&
  Number.isSafeInteger(value.blockIndex) &&
  Number(value.blockIndex) >= 0 &&
  (value.branchId === null || (typeof value.branchId === 'string' && /^br_[0-9a-f]{8}$/.test(value.branchId)));

const normalizeTextMessageContentObject = (
  data: RawTextMessageContent,
  options?: NormalizeTextMessageContentOptions
): IMessageText['content'] => {
  const content = typeof data.content === 'string' ? data.content : String(data.content ?? '');
  const cronMeta = isCronMessageMeta(data.cronMeta) ? data.cronMeta : undefined;
  const synonBiomed = isSynonBiomedMessageCoordinates(data.synonBiomed) ? data.synonBiomed : undefined;
  const replace = options?.replace === true || data.replace === true;
  const rawReplaceScope = options?.replaceScope ?? data.replaceScope ?? data.replace_scope;
  const replaceScope = rawReplaceScope === 'attempt' ? rawReplaceScope : undefined;
  const rawAssistantAttemptId = options?.assistantAttemptId ?? data.assistantAttemptId ?? data.assistant_attempt_id;
  const assistantAttemptId = isAssistantAttemptId(rawAssistantAttemptId) ? rawAssistantAttemptId : undefined;

  return {
    content,
    ...(replace ? { replace: true } : {}),
    ...(replaceScope ? { replaceScope } : {}),
    ...(assistantAttemptId ? { assistantAttemptId } : {}),
    ...(cronMeta ? { cronMeta } : {}),
    ...(synonBiomed ? { synonBiomed } : {}),
  };
};

export const normalizeTextMessageContent = (
  raw: unknown,
  options?: NormalizeTextMessageContentOptions
): IMessageText['content'] => {
  if (typeof raw === 'string') {
    try {
      const parsed = JSON.parse(raw) as unknown;
      if (isObject(parsed)) {
        return normalizeTextMessageContentObject(parsed as RawTextMessageContent, options);
      }
    } catch {
      // Plain text is the common streaming shape.
    }

    return {
      content: raw,
      ...(options?.replace === true ? { replace: true } : {}),
      ...(options?.replaceScope === 'attempt' ? { replaceScope: 'attempt' as const } : {}),
      ...(isAssistantAttemptId(options?.assistantAttemptId) ? { assistantAttemptId: options.assistantAttemptId } : {}),
    };
  }

  if (isObject(raw)) {
    return normalizeTextMessageContentObject(raw as RawTextMessageContent, options);
  }

  return {
    content: String(raw ?? ''),
    ...(options?.replace === true ? { replace: true } : {}),
    ...(options?.replaceScope === 'attempt' ? { replaceScope: 'attempt' as const } : {}),
    ...(isAssistantAttemptId(options?.assistantAttemptId) ? { assistantAttemptId: options.assistantAttemptId } : {}),
  };
};

const AGENT_ERROR_OWNERSHIPS = new Set<AgentErrorOwnership>([
  'synon-ai',
  'user_agent',
  'user_llm_provider',
  'unknown_upstream',
]);

const AGENT_ERROR_RESOLUTION_KINDS = new Set<AgentErrorResolutionKind>([
  'retry',
  'wait_for_current_response',
  'start_new_session',
  'reconnect_agent',
  'check_agent_login',
  'check_agent_installation',
  'check_agent_version',
  'check_local_command',
  'check_provider_credentials',
  'check_provider_billing',
  'check_provider_base_url',
  'change_model',
  'reduce_context',
  'send_feedback',
]);

const AGENT_ERROR_RESOLUTION_TARGETS = new Set<AgentErrorResolutionTarget>([
  'provider_settings',
  'agent_settings',
  'new_conversation',
  'feedback',
]);

export const normalizeAgentErrorResolution = (value: unknown): AgentErrorResolution | undefined => {
  if (!isObject(value) || typeof value.kind !== 'string') {
    return undefined;
  }

  if (!AGENT_ERROR_RESOLUTION_KINDS.has(value.kind as AgentErrorResolutionKind)) {
    return undefined;
  }

  const target =
    typeof value.target === 'string' && AGENT_ERROR_RESOLUTION_TARGETS.has(value.target as AgentErrorResolutionTarget)
      ? (value.target as AgentErrorResolutionTarget)
      : undefined;

  return {
    kind: value.kind as AgentErrorResolutionKind,
    ...(target ? { target } : {}),
  };
};

const normalizeRawErrorSummary = (value: unknown): AgentStreamRawErrorSummary | undefined => {
  if (!isObject(value)) return undefined;

  const name = typeof value.name === 'string' ? value.name : undefined;
  const message = typeof value.message === 'string' ? value.message : undefined;
  const code = typeof value.code === 'string' ? value.code : undefined;
  const status = typeof value.status === 'number' && Number.isFinite(value.status) ? value.status : undefined;
  const stack = typeof value.stack === 'string' ? value.stack : undefined;

  if (
    name === undefined &&
    message === undefined &&
    code === undefined &&
    status === undefined &&
    stack === undefined
  ) {
    return undefined;
  }

  return {
    ...(name !== undefined ? { name } : {}),
    ...(message !== undefined ? { message } : {}),
    ...(code !== undefined ? { code } : {}),
    ...(status !== undefined ? { status } : {}),
    ...(stack !== undefined ? { stack } : {}),
  };
};

export const normalizeAgentStreamError = (value: unknown): AgentStreamErrorInfo | undefined => {
  if (!isObject(value) || typeof value.message !== 'string') {
    return undefined;
  }

  const code = typeof value.code === 'string' ? value.code : undefined;
  const ownership =
    typeof value.ownership === 'string' && AGENT_ERROR_OWNERSHIPS.has(value.ownership as AgentErrorOwnership)
      ? (value.ownership as AgentErrorOwnership)
      : undefined;
  const detail = typeof value.detail === 'string' ? value.detail : undefined;
  const workspacePath = typeof value.workspacePath === 'string' ? value.workspacePath : undefined;
  const retryable = typeof value.retryable === 'boolean' ? value.retryable : undefined;
  const feedback_recommended = typeof value.feedback_recommended === 'boolean' ? value.feedback_recommended : undefined;
  const resolution = normalizeAgentErrorResolution(value.resolution);
  const rawError = normalizeRawErrorSummary(value.rawError);

  if (
    !code &&
    !ownership &&
    !detail &&
    !workspacePath &&
    retryable === undefined &&
    feedback_recommended === undefined &&
    !resolution &&
    !rawError
  ) {
    return undefined;
  }

  return {
    message: value.message,
    ...(code ? { code } : {}),
    ...(ownership ? { ownership } : {}),
    ...(detail ? { detail } : {}),
    ...(workspacePath ? { workspacePath } : {}),
    ...(retryable !== undefined ? { retryable } : {}),
    ...(feedback_recommended !== undefined ? { feedback_recommended } : {}),
    ...(resolution ? { resolution } : {}),
    ...(rawError ? { rawError } : {}),
  };
};

/**
 * @description 将后端返回的消息转换为前端消息
 * */
const isChatMessagePosition = (value: unknown): value is NonNullable<TMessage['position']> =>
  value === 'left' || value === 'right' || value === 'center' || value === 'pop';

const isChatMessageStatus = (value: unknown): value is NonNullable<TMessage['status']> =>
  value === 'finish' || value === 'pending' || value === 'error' || value === 'work';

export const transformMessage = (message: IResponseMessage): TMessage | undefined => {
  const created_at = message.created_at ?? Date.now();
  switch (message.type) {
    case 'error': {
      const errorData = message.data;
      const structuredError = normalizeAgentStreamError(errorData);
      const errorText =
        typeof errorData === 'string'
          ? errorData
          : ((errorData as { message?: string })?.message ?? JSON.stringify(errorData));
      return {
        id: uuid(),
        type: 'tips',
        msg_id: message.msg_id,
        position: 'center',
        ...(isChatMessageStatus(message.status) ? { status: message.status } : {}),
        ...(message.terminal_status !== undefined ? { terminal_status: message.terminal_status } : {}),
        ...(message.terminal_superseded !== undefined ? { terminal_superseded: message.terminal_superseded } : {}),
        conversation_id: message.conversation_id,
        created_at,
        content: {
          content: errorText,
          type: 'error',
          ...(structuredError ? { error: structuredError } : {}),
        },
      };
    }
    case 'tips': {
      const data = message.data as {
        content: string;
        type?: 'error' | 'info' | 'success' | 'warning';
        code?: unknown;
        params?: unknown;
        error?: unknown;
      };
      const tipType = data.type ?? 'warning';
      const tipCode = typeof data.code === 'string' ? data.code : undefined;
      const tipParams = isObject(data.params) ? data.params : undefined;
      const structuredError =
        tipType === 'error'
          ? (normalizeAgentStreamError(data.error) ?? normalizeAgentStreamError({ ...data, message: data.content }))
          : undefined;
      return {
        id: uuid(),
        type: 'tips',
        msg_id: message.msg_id,
        position: 'center',
        ...(isChatMessageStatus(message.status) ? { status: message.status } : {}),
        ...(message.terminal_status !== undefined ? { terminal_status: message.terminal_status } : {}),
        ...(message.terminal_superseded !== undefined ? { terminal_superseded: message.terminal_superseded } : {}),
        conversation_id: message.conversation_id,
        created_at,
        content: {
          content: data.content,
          type: tipType,
          ...(tipCode ? { code: tipCode } : {}),
          ...(tipParams ? { params: tipParams } : {}),
          ...(structuredError ? { error: structuredError } : {}),
        },
        ...(message.hidden && { hidden: true }),
      };
    }
    case 'text':
    case 'content':
    case 'user_content': {
      const data = message.data;
      const position = isChatMessagePosition(message.position)
        ? message.position
        : message.type === 'user_content'
          ? 'right'
          : 'left';
      const status = isChatMessageStatus(message.status) ? message.status : undefined;
      return {
        id: message.msg_id,
        type: 'text',
        msg_id: message.msg_id,
        position,
        ...(status ? { status } : {}),
        conversation_id: message.conversation_id,
        created_at,
        content: normalizeTextMessageContent(data, {
          replace: message.replace === true,
          replaceScope: message.replace_scope,
          assistantAttemptId: message.assistant_attempt_id,
        }),
        ...(Number.isSafeInteger(message.source_publication_sequence) && Number(message.source_publication_sequence) > 0
          ? { source_publication_sequence: message.source_publication_sequence }
          : {}),
        ...(message.artifact_refs !== undefined ? { artifact_refs: message.artifact_refs } : {}),
        ...(message.terminal_status !== undefined ? { terminal_status: message.terminal_status } : {}),
        ...(message.terminal_superseded !== undefined ? { terminal_superseded: message.terminal_superseded } : {}),
        ...(message.hidden && { hidden: true }),
      };
    }
    case 'tool_call': {
      return {
        // Durable transcript tool publications use msg_id as the history row
        // identity. Keep that exact identity in the live row so full-detail
        // hydration works before the next history reconciliation.
        id: message.msg_id,
        type: 'tool_call',
        msg_id: message.msg_id,
        conversation_id: message.conversation_id,
        position: 'left',
        created_at,
        content: message.data as IMessageToolCall['content'],
      };
    }
    case 'tool_group': {
      return {
        type: 'tool_group',
        id: uuid(),
        msg_id: message.msg_id,
        conversation_id: message.conversation_id,
        created_at,
        content: message.data as IMessageToolGroup['content'],
      };
    }
    case 'agent_status': {
      return {
        id: uuid(),
        type: 'agent_status',
        msg_id: message.msg_id,
        position: 'center',
        conversation_id: message.conversation_id,
        created_at,
        content: message.data as IMessageAgentStatus['content'],
      };
    }
    case 'permission': {
      return {
        id: uuid(),
        type: 'permission',
        msg_id: message.msg_id,
        position: 'left',
        conversation_id: message.conversation_id,
        created_at,
        content: message.data as IMessagePermission['content'],
      };
    }
    case 'acp_permission': {
      return {
        id: uuid(),
        type: 'acp_permission',
        msg_id: message.msg_id,
        position: 'left',
        conversation_id: message.conversation_id,
        created_at,
        content: message.data as IMessageAcpPermission['content'],
      };
    }
    case 'acp_tool_call': {
      return {
        id: uuid(),
        type: 'acp_tool_call',
        msg_id: message.msg_id,
        position: 'left',
        conversation_id: message.conversation_id,
        created_at,
        content: message.data as IMessageAcpToolCall['content'],
      };
    }
    case 'plan': {
      return {
        id: uuid(),
        type: 'plan',
        msg_id: message.msg_id,
        position: 'left',
        conversation_id: message.conversation_id,
        created_at,
        content: message.data as IMessagePlan['content'],
      };
    }
    case 'thinking': {
      const data = message.data as {
        content: string;
        subject?: string;
        duration?: number;
        duration_ms?: number;
        status: 'thinking' | 'done';
      };
      return {
        id: uuid(),
        type: 'thinking',
        msg_id: message.msg_id,
        position: 'left',
        conversation_id: message.conversation_id,
        created_at,
        content: {
          content: data.content,
          subject: data.subject,
          duration: data.duration ?? data.duration_ms,
          ...(message.replace === true ? { replace: true } : {}),
          status: data.status,
        },
      };
    }
    // Disabled: available_commands messages are too noisy and distracting in the chat UI
    case 'available_commands':
      return undefined;
    case 'start':
    case 'finish':
    case 'thought':
    case 'skill_suggest':
    case 'cron_trigger':
    case 'info': // Stream retry notifications and similar transient agent updates
    case 'system': // Cron system responses, ignored
    case 'acp_model_info': // Model info updates are handled by SynonBiomedModelSelector.
    case 'codex_model_info': // Legacy Codex model info updates
    case 'acp_context_usage': // Context usage updates, handled by AcpSendBox
    case 'request_trace': // Request trace events, logged to F12 console (not persisted)
      return undefined;
    default: {
      console.warn(
        `[transformMessage] Unsupported message type '${message.type}'. All non-standard message types should be pre-processed by respective AgentManagers.`
      );
      return undefined;
    }
  }
};

/**
 * @description 将消息合并到消息列表中
 * */
export const composeMessage = (
  message: TMessage | undefined,
  list: TMessage[] | undefined,
  messageHandler: (type: 'update' | 'insert', message: TMessage) => void = () => {}
): TMessage[] => {
  if (!message) return list || [];
  const normalizedMessage =
    message.type === 'acp_tool_call'
      ? ({
          ...message,
          content: sanitizeAcpToolCallContent(message.content),
        } as TMessage)
      : message;
  if (!list?.length) {
    messageHandler('insert', normalizedMessage);
    return [normalizedMessage];
  }
  const last = list[list.length - 1];

  const updateMessage = (index: number, nextMessage: TMessage, change = true) => {
    nextMessage.id = list[index].id;
    list[index] = nextMessage;
    if (change) messageHandler('update', nextMessage);
    return list.slice();
  };
  const pushMessage = (nextMessage: TMessage) => {
    list.push(nextMessage);
    messageHandler('insert', nextMessage);
    return list.slice();
  };

  if (message.type === 'tool_group') {
    const remainingToolsMap = new Map(message.content.map((t) => [t.call_id, t] as const));
    if (remainingToolsMap.size === 0) return list;

    const updatesToReport: TMessage[] = [];

    const updatedList = list.map((existingMessage) => {
      if (existingMessage.type !== 'tool_group') return existingMessage;
      if (!existingMessage.content.length) return existingMessage;

      let didMergeIntoThisMessage = false;
      const new_content = existingMessage.content.map((tool) => {
        const newToolData = remainingToolsMap.get(tool.call_id);
        if (!newToolData) return tool;
        didMergeIntoThisMessage = true;
        remainingToolsMap.delete(tool.call_id);
        // Create new object instead of mutating original
        return { ...tool, ...newToolData };
      });

      if (!didMergeIntoThisMessage) return existingMessage;
      const updatedMessage = {
        ...existingMessage,
        content: new_content,
      } as TMessage;
      updatesToReport.push(updatedMessage);
      return updatedMessage;
    });

    const didUpdateExisting = updatesToReport.length > 0;
    for (const updatedMessage of updatesToReport) {
      messageHandler('update', updatedMessage);
    }

    const baseList = didUpdateExisting ? updatedList : list;

    // If there are new tool calls, append them as a new tool_group message (without mutating inputs)
    if (remainingToolsMap.size > 0) {
      const newTools = Array.from(remainingToolsMap.values());
      const insertMessage = { ...message, content: newTools } as TMessage;
      messageHandler('insert', insertMessage);
      return baseList.concat(insertMessage);
    }
    // No new tools appended; return a new list only if something was updated
    return didUpdateExisting ? baseList : list;
  }

  // Handle Gemini tool_call message merging
  if (message.type === 'tool_call') {
    for (let i = 0, len = list.length; i < len; i++) {
      const msg = list[i];
      if (msg.type === 'tool_call' && msg.content.call_id === message.content.call_id) {
        // Create new object instead of mutating original
        return updateMessage(i, {
          ...msg,
          content: { ...msg.content, ...message.content },
        });
      }
    }
    // If no existing tool call found, add new one
    return pushMessage(message);
  }

  // Handle acp_tool_call message merging
  if (message.type === 'acp_tool_call') {
    for (let i = 0, len = list.length; i < len; i++) {
      const msg = list[i];
      if (msg.type === 'acp_tool_call' && msg.content.update?.tool_call_id === message.content.update?.tool_call_id) {
        // Create new object instead of mutating original
        const merged = mergeAcpToolCallContent(msg.content, message.content);
        return updateMessage(i, { ...msg, content: merged });
      }
    }
    // If no existing tool call found, add new one
    return pushMessage(normalizedMessage);
  }

  if (message.type === 'plan') {
    for (let i = 0, len = list.length; i < len; i++) {
      const msg = list[i];
      if (msg.type === 'plan' && msg.content.session_id === message.content.session_id) {
        // Create new object instead of mutating original
        const merged = { ...msg.content, ...message.content };
        return updateMessage(i, { ...msg, content: merged });
      }
    }
    return pushMessage(message);
    // If no existing plan found, add new one
  }

  // Handle thinking message merging — only merge contiguous streaming chunks
  if (message.type === 'thinking') {
    if (message.content.status === 'done') {
      for (let i = list.length - 1; i >= 0; i--) {
        const msg = list[i];
        if (msg.type !== 'thinking' || msg.msg_id !== message.msg_id) continue;

        const merged = {
          ...msg.content,
          status: 'done' as const,
          duration: message.content.duration,
          subject: message.content.subject || msg.content.subject,
        };
        return updateMessage(i, { ...msg, content: merged });
      }
    }

    if (last.type === 'thinking' && last.msg_id === message.msg_id) {
      // Otherwise append content
      const merged = {
        ...last.content,
        content: last.content.content + message.content.content,
        subject: message.content.subject || last.content.subject,
      };
      return updateMessage(list.length - 1, { ...last, content: merged });
    }
    return pushMessage(message);
  }

  if (last.msg_id !== message.msg_id || last.type !== message.type) {
    return pushMessage(message);
  }
  if (message.type === 'text' && last.type === 'text') {
    message.content = mergeTextMessageContent(last.content, message.content);
    message.artifact_refs = mergeArtifactReferences(
      last.artifact_refs,
      message.artifact_refs,
      message.content.replace === true ? 'replace' : 'union'
    );
  }
  return updateMessage(list.length - 1, Object.assign({}, last, message));
};

export const handleImageGenerationWithWorkspace = (message: TMessage, workspace: string): TMessage => {
  // 只处理text类型的消息
  if (message.type !== 'text') {
    return message;
  }

  // 深拷贝消息以避免修改原始对象
  const processedMessage = {
    ...message,
    content: {
      ...message.content,
      content: message.content.content.replace(/!\[([^\]]*)\]\(([^)]+)\)/g, (match, alt, imagePath) => {
        // 如果是绝对路径、http链接或data URL，保持不变
        if (
          imagePath.startsWith('http') ||
          imagePath.startsWith('data:') ||
          imagePath.startsWith('/') ||
          imagePath.startsWith('file:') ||
          imagePath.startsWith('\\') ||
          /^[A-Za-z]:/.test(imagePath)
        ) {
          return match;
        }
        // 如果是相对路径，与workspace拼接
        const absolutePath = joinPath(workspace, imagePath);
        return `![${alt}](${encodeURI(absolutePath)})`;
      }),
    },
  };

  return processedMessage;
};

/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

/**
 * IPC Bridge → HTTP/WS adapter.
 *
 * This file replaces the original IPC bridge calls with HTTP REST and WebSocket
 * calls routed through the Synon Biomed WebHost gateway. Electron-native
 * operations (window controls, native dialogs, auto-update, devtools, zoom,
 * CDP, deep links) remain as IPC.
 */

import type { IConfirmation } from '@/common/chat/chatLib';
import type { AcpSlashCommandApiItem } from '@/common/chat/slash/types';
import type { TChatConversation, TConversationRuntimeSummary } from '../config/storage';
import { buildCreateConversationBody, fromApiConversation } from './apiModelMapper';
import {
  httpDelete,
  httpGet,
  httpPatch,
  httpPost,
  httpRequest,
  realtimeReconnectedEmitter,
  stubProvider,
  unavailableEmitter,
  withResponseMap,
  wsEmitter,
  wsMappedEmitter,
} from './httpBridge';
import { decodeMessageStreamPayload, type ArtifactReferenceWire, type IResponseMessage } from './messageStreamProtocol';
import {
  decodeConversationCancelAcknowledgement,
  decodeConversationRuntimeEnsure,
  decodeConversationSendAccepted,
  decodeConversationTurnCompleted,
} from './conversationRuntimeProtocol';
import {
  absoluteToRelativePath,
  fromBackendWorkspaceArtifactPage,
  fromBackendWorkspaceList,
  type WorkspaceArtifactPage,
} from './workspaceMapper';
import type {
  ConversationSideQuestionResult,
  IConversationArtifact,
  IConversationArtifactStatus,
  IConversationListChangedEvent,
  IConversationTurnCompletedEvent,
  ICreateConversationParams,
  IDirOrFile,
  IResetConversationParams,
  ISendMessageParams,
  IToolStdoutChunkEvent,
} from './ipcBridgeTypes';

// ---------------------------------------------------------------------------
// Conversation — REST + WS
// ---------------------------------------------------------------------------

const buildStoppedSynonBiomedRuntime = (): TConversationRuntimeSummary => ({
  state: 'idle',
  can_send_message: true,
  has_task: false,
  task_status: 'finished',
  is_processing: false,
  pending_confirmations: 0,
  turn_id: null,
});

const cancelSynonBiomedFrame = httpPost<unknown, { conversation_id: string; turn_id: string }>(
  (params) => `/api/frames/${encodeURIComponent(params.conversation_id)}/cancel?reason=user`,
  () => ({})
);

let invalidMessageStreamDiagnosed = false;

const mapMessageStreamPayload = (raw: unknown): IResponseMessage => {
  const decoded = decodeMessageStreamPayload(raw);
  if (decoded) return decoded;
  if (!invalidMessageStreamDiagnosed) {
    invalidMessageStreamDiagnosed = true;
    console.warn('[realtime]', 'realtime_message_stream_invalid');
  }
  throw new Error('realtime_message_stream_invalid');
};

export type IConversationHistoryRebasedEvent = {
  conversation_id: string;
  project_id: string;
  root_frame_id: string;
  activation_id: string;
  authority_generation: number;
  target_epoch: number;
  active_branch_id: string;
  branch_generation: number;
  realtime_high_water: number;
};

export type IRealtimeCursorResetEvent = { cursor: number };

const HISTORY_ACTIVATION_PATTERN = /^[0-9a-f]{64}$/;
const HISTORY_BRANCH_PATTERN = /^br_[0-9a-f]{8}$/;
const isSafeIntegerAtLeast = (value: unknown, minimum: number): value is number =>
  Number.isSafeInteger(value) && (value as number) >= minimum;

const mapConversationHistoryRebased = (raw: unknown): IConversationHistoryRebasedEvent => {
  if (typeof raw !== 'object' || raw === null || Array.isArray(raw)) {
    throw new Error('realtime_history_rebase_invalid');
  }
  const payload = raw as Record<string, unknown>;
  if (
    typeof payload.conversation_id !== 'string' ||
    payload.conversation_id.trim().length === 0 ||
    typeof payload.project_id !== 'string' ||
    payload.project_id.trim().length === 0 ||
    typeof payload.root_frame_id !== 'string' ||
    payload.root_frame_id.trim().length === 0 ||
    typeof payload.activation_id !== 'string' ||
    !HISTORY_ACTIVATION_PATTERN.test(payload.activation_id) ||
    !isSafeIntegerAtLeast(payload.authority_generation, 2) ||
    !isSafeIntegerAtLeast(payload.target_epoch, 2) ||
    typeof payload.active_branch_id !== 'string' ||
    !HISTORY_BRANCH_PATTERN.test(payload.active_branch_id) ||
    !isSafeIntegerAtLeast(payload.branch_generation, 1) ||
    !isSafeIntegerAtLeast(payload.realtime_high_water, 0)
  ) {
    throw new Error('realtime_history_rebase_invalid');
  }
  return {
    conversation_id: payload.conversation_id.trim(),
    project_id: payload.project_id.trim(),
    root_frame_id: payload.root_frame_id.trim(),
    activation_id: payload.activation_id,
    authority_generation: payload.authority_generation,
    target_epoch: payload.target_epoch,
    active_branch_id: payload.active_branch_id,
    branch_generation: payload.branch_generation,
    realtime_high_water: payload.realtime_high_water,
  };
};

const mapRealtimeCursorReset = (raw: unknown): IRealtimeCursorResetEvent => {
  if (typeof raw !== 'object' || raw === null || Array.isArray(raw)) {
    throw new Error('realtime_cursor_reset_invalid');
  }
  const cursor = (raw as Record<string, unknown>).cursor;
  if (!isSafeIntegerAtLeast(cursor, 0)) throw new Error('realtime_cursor_reset_invalid');
  return { cursor };
};

export type ConversationWorkspaceRequest = {
  conversation_id: string;
  workspace: string;
  path: string;
  search?: string;
};

export type ConversationWorkspacePageRequest = ConversationWorkspaceRequest & {
  limit?: number;
  cursor?: string;
};

export type ConversationArtifactReference = Pick<ArtifactReferenceWire, 'artifact_id' | 'version_id'>;

export function requestConversationArtifacts(
  params: {
    conversation_id: string;
    references: ConversationArtifactReference[];
    version_ids?: string[];
  },
  signal?: AbortSignal
): Promise<IConversationArtifact[]> {
  return httpRequest(
    'POST',
    `/api/conversations/${encodeURIComponent(params.conversation_id)}/artifacts`,
    { references: params.references, version_ids: params.version_ids },
    { timeoutMs: 15_000, signal }
  );
}

export async function requestConversationWorkspace(
  params: ConversationWorkspaceRequest,
  signal?: AbortSignal
): Promise<IDirOrFile[]> {
  const rel = absoluteToRelativePath(params.path, params.workspace);
  const search = params.search ? `&search=${encodeURIComponent(params.search)}` : '';
  const url = `/api/conversations/${encodeURIComponent(
    params.conversation_id
  )}/workspace?path=${encodeURIComponent(rel)}${search}`;
  const raw = await httpRequest<Array<{ name: string; type: string }>>('GET', url, undefined, {
    timeoutMs: 15_000,
    signal,
  });
  return fromBackendWorkspaceList(raw, params.workspace, rel);
}

export async function requestConversationWorkspacePage(
  params: ConversationWorkspacePageRequest,
  signal?: AbortSignal
): Promise<WorkspaceArtifactPage> {
  const rel = absoluteToRelativePath(params.path, params.workspace);
  const query = new URLSearchParams({
    path: rel,
    limit: String(params.limit ?? 200),
  });
  if (params.search) query.set('search', params.search);
  if (params.cursor) query.set('cursor', params.cursor);
  const raw = await httpRequest<unknown>(
    'GET',
    `/api/conversations/${encodeURIComponent(params.conversation_id)}/workspace?${query.toString()}`,
    undefined,
    { timeoutMs: 15_000, signal }
  );
  return fromBackendWorkspaceArtifactPage(raw, params.workspace, rel);
}

export const conversation = {
  create: withResponseMap(
    httpPost<TChatConversation, ICreateConversationParams>('/api/conversations', (p) => buildCreateConversationBody(p)),
    fromApiConversation
  ),
  createWithConversation: withResponseMap(
    httpPost<TChatConversation, { conversation: TChatConversation }>('/api/conversations/clone', (p) => {
      const { model: _rawModel, ...rest } = p.conversation as TChatConversation & {
        model?: unknown;
      };
      const clonedConversation: Record<string, unknown> = { ...rest };
      return {
        conversation: clonedConversation,
      };
    }),
    fromApiConversation
  ),
  get: withResponseMap(
    httpGet<TChatConversation, { id: string }>((p) => `/api/conversations/${p.id}`, {
      silentStatuses: [404],
    }),
    fromApiConversation
  ),
  getAssociateConversation: withResponseMap(
    httpGet<TChatConversation[], { conversation_id: string }>(
      (p) => `/api/conversations/${p.conversation_id}/associated`
    ),
    (list) => list.map(fromApiConversation)
  ),
  remove: httpDelete<boolean, { id: string }>((p) => `/api/conversations/${p.id}`),
  update: httpPatch<boolean, { id: string; updates: Partial<TChatConversation>; merge_extra?: boolean }>(
    (p) => `/api/conversations/${p.id}`,
    (p) => {
      const updates = p.updates as Record<string, unknown>;
      const { model: _rawModel, ...rest } = updates;
      return {
        ...rest,
        merge_extra: p.merge_extra,
      };
    }
  ),
  reset: httpPost<void, IResetConversationParams>((p) => `/api/conversations/${p.id}/reset`),
  ensureRuntime: withResponseMap(
    httpPost<unknown, { conversation_id: string }>(
      (p) => `/api/conversations/${p.conversation_id}/runtime/ensure`,
      () => undefined,
      { timeoutMs: 15_000 }
    ),
    decodeConversationRuntimeEnsure
  ),
  activeLease: httpPost<void, { conversation_id: string }>(
    (p) => `/api/conversations/${p.conversation_id}/active-lease`,
    () => undefined
  ),
  stop: {
    provider: () => {},
    invoke: async (params: { conversation_id: string; turn_id: string }) => {
      const response = await cancelSynonBiomedFrame.invoke(params);
      decodeConversationCancelAcknowledgement(response, params.conversation_id);
      return { runtime: buildStoppedSynonBiomedRuntime() };
    },
  },
  activeCount: httpGet<{ count: number }>('/api/conversations/active-count'),
  sendMessage: withResponseMap(
    httpPost<unknown, ISendMessageParams>(
      (p) => `/api/conversations/${p.conversation_id}/messages`,
      (p) => ({
        content: p.input,
        files: p.files,
        artifact_refs: p.artifact_refs?.map((reference) => ({
          artifact_id: reference.artifact_id,
          version_id: reference.version_id,
        })),
        message_context: p.message_context,
        loading_id: p.loading_id,
        inject_skills: p.inject_skills,
        inject_mcp_server_ids: p.inject_mcp_server_ids,
        session_options: p.session_options,
      })
    ),
    decodeConversationSendAccepted
  ),
  getSlashCommands: httpGet<AcpSlashCommandApiItem[], { conversation_id: string }>(
    (p) => `/api/conversations/${p.conversation_id}/slash-commands`
  ),
  askSideQuestion: httpPost<ConversationSideQuestionResult, { conversation_id: string; question: string }>(
    (p) => `/api/conversations/${p.conversation_id}/side-question`,
    (p) => ({ question: p.question })
  ),
  listArtifacts: {
    provider: () => {},
    invoke: requestConversationArtifacts,
  },
  updateArtifact: httpPatch<
    IConversationArtifact,
    {
      conversation_id: string;
      artifact_id: string;
      status: IConversationArtifactStatus;
    }
  >(
    (p) => `/api/conversations/${p.conversation_id}/artifacts/${p.artifact_id}`,
    (p) => ({ status: p.status })
  ),
  responseStream: wsMappedEmitter<IResponseMessage>('durable', 'message.stream', mapMessageStreamPayload),
  historyRebased: wsMappedEmitter<IConversationHistoryRebasedEvent>(
    'durable',
    'conversation.historyRebased',
    mapConversationHistoryRebased
  ),
  userCreated: wsEmitter<{
    conversation_id: string;
    msg_id: string;
    content: string;
    position: 'right';
    status: 'finish';
    hidden: boolean;
    created_at: number;
  }>('durable', 'message.userCreated'),
  artifactStream: unavailableEmitter<IConversationArtifact>('realtime_event_unavailable_conversation_artifact'),
  turnCompleted: wsMappedEmitter<IConversationTurnCompletedEvent>(
    'durable',
    'turn.completed',
    decodeConversationTurnCompleted
  ),
  listChanged: wsEmitter<IConversationListChangedEvent>('durable', 'conversation.listChanged'),
  // Uses httpRequest directly (instead of httpGet + withResponseMap) because the
  // response mapper needs `workspace` from params to build fullPath/relativePath,
  // and withResponseMap's map function does not receive the original params.
  getWorkspace: {
    provider: () => {},
    invoke: requestConversationWorkspace,
  },
  getWorkspacePage: {
    provider: () => {},
    invoke: requestConversationWorkspacePage,
  },
  responseSearchWorkSpace: stubProvider<void, { file: number; dir: number; match?: IDirOrFile }>(
    'responseSearchWorkSpace',
    undefined as unknown as void
  ),
  confirmation: {
    add: wsEmitter<IConfirmation<unknown> & { conversation_id: string }>('durable', 'confirmation.add'),
    update: wsEmitter<IConfirmation<unknown> & { conversation_id: string }>('durable', 'confirmation.update'),
    confirm: httpPost<
      void,
      {
        conversation_id: string;
        msg_id: string;
        data: unknown;
        call_id: string;
        always_allow?: boolean;
      }
    >(
      (p) => `/api/conversations/${p.conversation_id}/confirmations/${encodeURIComponent(p.call_id)}/confirm`,
      (p) => ({
        msg_id: p.msg_id,
        data: p.data,
        always_allow: p.always_allow ?? false,
      })
    ),
    list: httpGet<IConfirmation<unknown>[], { conversation_id: string }>(
      (p) => `/api/conversations/${p.conversation_id}/confirmations`
    ),
    remove: wsEmitter<{ conversation_id: string; id: string }>('durable', 'confirmation.remove'),
  },
};

export const realtime = {
  reconnected: realtimeReconnectedEmitter(),
  cursorReset: wsMappedEmitter<IRealtimeCursorResetEvent>('control', 'realtime.cursorReset', mapRealtimeCursorReset),
  toolStdoutChunk: wsEmitter<IToolStdoutChunkEvent>('durable', 'tool_stdout_chunk'),
};

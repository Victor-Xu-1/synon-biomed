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

import type { ISessionMcpServer } from '../config/storage';
import type { ArtifactReferenceWire } from './messageStreamProtocol';
import type { ConversationSendAccepted, ConversationTurnCompletedEvent } from './conversationRuntimeProtocol';

// ---------------------------------------------------------------------------
// Shared types (re-exported for consumers)
// ---------------------------------------------------------------------------

export interface ISendMessageParams {
  input: string;
  conversation_id: string;
  files?: string[];
  artifact_refs?: ArtifactReferenceWire[];
  message_context?: 'onboarding_first_task';
  loading_id?: string;
  inject_skills?: string[];
  inject_mcp_server_ids?: string[];
  session_options?: {
    ultra_mode: boolean;
    verifier_mode: 'on' | 'off';
    memory_mode: 'on' | 'off';
    target_agent: string;
    model?: string;
    subagent_model?: string;
    effort?: 'low' | 'medium' | 'high';
    plan_mode?: boolean;
    target_branch_id?: string;
    expected_branch_id?: string;
    expected_generation?: number;
  };
}

// Server-assigned identifier for the newly created user message. Clients must
// use this as the canonical msg_id when rendering an optimistic bubble so the
// local state aligns with DB rows and WebSocket stream events.
export type ISendMessageResult = ConversationSendAccepted;

export interface ICreateConversationParams {
  type?: 'acp';
  id?: string;
  name?: string;
  assistant?: {
    id: string;
    locale?: string;
    conversation_overrides?: {
      model?: string;
      permission?: string;
      skill_ids?: string[];
      disabled_builtin_skill_ids?: string[];
      mcp_ids?: string[];
    };
  };
  extra: {
    workspace?: string;
    custom_workspace?: boolean;
    default_files?: string[];
    cli_path?: string;
    web_search_engine?: 'google' | 'default';
    context?: string;
    context_file_name?: string;
    /** Transient: preset opt-in skills. Consumed by backend create handler
     *  and stripped before persistence. */
    preset_enabled_skills?: string[];
    /** Transient: auto-inject skills the user opted out of on the Guid page.
     *  Consumed by backend create handler and stripped before persistence. */
    exclude_auto_inject_skills?: string[];
    selected_mcp_server_ids?: string[];
    selected_session_mcp_servers?: ISessionMcpServer[];
    thought_level?: string;
    cached_config_options?: import('../types/platform/acpTypes').AcpSessionConfigOption[];
    pending_config_options?: Record<string, string>;
    extra_skill_paths?: string[];
  };
}

export interface IResetConversationParams {
  id?: string;
}

export interface IDirOrFile {
  name: string;
  fullPath: string;
  relativePath: string;
  isDir: boolean;
  isFile: boolean;
  children?: Array<IDirOrFile>;
  /** Remote Synon Biomed project assets are browsable but not local filesystem entries. */
  readOnly?: boolean;
  /** Remote artifact capabilities exposed by the Synon Biomed backend. */
  canRename?: boolean;
  canDelete?: boolean;
  artifactId?: string;
  versionId?: string;
  contentType?: string;
  sizeBytes?: number;
  contentUrl?: string;
  previewKind?: string;
}

export interface IFileMetadata {
  name: string;
  path: string;
  size: number;
  type: string;
  lastModified: number;
  isDirectory?: boolean;
}

export type IWorkspaceFlatFile = {
  name: string;
  fullPath: string;
  relativePath: string;
};

export type { IResponseMessage } from './messageStreamProtocol';

export type IConversationArtifactKind = 'cron_trigger' | 'skill_suggest' | 'scientific_files';
export type IConversationArtifactStatus = 'active' | 'pending' | 'dismissed' | 'saved';

export interface IConversationArtifactBase<
  Kind extends IConversationArtifactKind,
  Payload extends Record<string, unknown>,
> {
  id: string;
  conversation_id: string;
  cron_job_id?: string;
  kind: Kind;
  status: IConversationArtifactStatus;
  payload: Payload;
  created_at: number;
  updated_at: number;
}

export type ICronTriggerArtifact = IConversationArtifactBase<
  'cron_trigger',
  {
    cron_job_id: string;
    cron_job_name: string;
    triggered_at: number;
  }
>;

export type ISkillSuggestArtifact = IConversationArtifactBase<
  'skill_suggest',
  {
    cron_job_id: string;
    name: string;
    description: string;
    skillContent?: string;
    skill_content?: string;
  }
>;

export type ISynonBiomedScientificFile = {
  artifact_id: string;
  version_id: string | null;
  version_number: number;
  project_id: string | null;
  root_frame_id: string | null;
  frame_id: string | null;
  creating_frame_id: string | null;
  filename: string;
  content_type: string | null;
  size_bytes: number;
  preview_kind: string;
  content_url: string;
  created_at: number;
  updated_at: number;
  agent_name: string | null;
  is_user_upload: boolean;
  is_intermediate: boolean;
  availability?: 'available' | 'deleted' | 'missing';
};

export type IScientificFilesArtifact = IConversationArtifactBase<
  'scientific_files',
  {
    project_id: string;
    root_frame_id: string;
    files: ISynonBiomedScientificFile[];
  }
>;
export type IConversationArtifact = ICronTriggerArtifact | ISkillSuggestArtifact | IScientificFilesArtifact;

export type IConversationTurnCompletedEvent = ConversationTurnCompletedEvent;

export interface IConversationListChangedEvent {
  conversation_id: string;
  action: 'created' | 'updated' | 'deleted';
  source?: string;
  root_frame_id?: string;
  frame_id?: string;
}

export type ConversationSideQuestionResult =
  | { status: 'ok'; answer: string }
  | { status: 'noAnswer' }
  | { status: 'unsupported' }
  | { status: 'invalid'; reason: 'emptyQuestion' }
  | { status: 'toolsRequired' };

export interface IBridgeResponse<D = {}> {
  success: boolean;
  data?: D;
  msg?: string;
}

export type IRealtimeReconnectedEvent = {
  timestamp: number;
};

export type IToolStdoutChunkEvent = {
  frame_id: string;
  root_frame_id: string;
  exec_id?: string;
  tool_use_id?: string;
  tool_name?: string;
  started_at?: string;
  background?: boolean;
  status?: 'queued' | 'running' | 'persisting';
  chunk: string;
  chunk_sequence?: number;
  chunk_start_byte?: number;
  chunk_end_byte?: number;
};

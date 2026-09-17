/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { SpeechToTextConfig } from '@/common/types/provider/speech';
import type { Theme } from '@/common/theme/types';
import { storage } from '@office-ai/platform';

// 系统配置存储
export const ConfigStorage = storage.buildStorage<IConfigStorageRefer>('agent.config');

// 系统环境变量存储
export const EnvStorage = storage.buildStorage<IEnvStorageRefer>('agent.env');

export interface IConfigStorageRefer {
  language: string;
  theme: string; // @deprecated migrated to theme.activeId/theme.userThemes
  colorScheme: string; // @deprecated migrated to theme.activeId/theme.userThemes
  /** Persisted app-wide UI zoom factor for Display settings */
  'ui.zoomFactor'?: number;
  /** Per-region configurable font sizes (px), set in Appearance settings */
  'ui.fontSize.chat'?: number;
  'ui.fontSize.markdown'?: number;
  'ui.fontSize.code'?: number;
  /** Last-known main window size and position, restored on next launch */
  'window.bounds'?: { x?: number; y?: number; width: number; height: number };
  /** 桌面模式下是否自动启用 WebUI / Auto-enable WebUI in desktop mode */
  'webui.desktop.enabled'?: boolean;
  /** 桌面模式下是否允许远程访问 / Allow remote access in desktop mode */
  'webui.desktop.allowRemote'?: boolean;
  /** 桌面模式下 WebUI 端口 / WebUI port in desktop mode */
  'webui.desktop.port'?: number;
  customCss: string; // 自定义 CSS 样式 // @deprecated migrated to theme.activeId/theme.userThemes
  'css.themes': ICssTheme[]; // 自定义 CSS 主题列表 / Custom CSS themes list // @deprecated migrated to theme.activeId/theme.userThemes
  'css.activeThemeId': string; // 当前激活的主题 ID / Currently active theme ID // @deprecated migrated to theme.activeId/theme.userThemes
  /** Active unified theme ID */
  'theme.activeId': string;
  /** User-created themes */
  'theme.userThemes': Theme[];
  // 是否在粘贴文件到工作区时询问确认（true = 不再询问）
  'workspace.pasteConfirm'?: boolean;
  // 上传的文件是否保存到工作区目录（true = 保存到工作区，false = 保存到缓存目录）
  'upload.saveToWorkspace'?: boolean;
  // 关闭窗口时最小化到系统托盘 / Minimize to system tray when closing window
  'system.closeToTray'?: boolean;
  // 任务完成时显示系统通知 / Show system notification when task completes
  'system.notificationEnabled'?: boolean;
  // 定时任务完成时显示系统通知 / Show system notification when scheduled task completes
  'system.cronNotificationEnabled'?: boolean;
  // 阻止系统休眠以保证定时任务执行 / Prevent system sleep to ensure scheduled tasks run
  'system.keepAwake'?: boolean;
  // Automatically preview newly created Office files in the current workspace
  'system.autoPreviewOfficeFiles'?: boolean;
  // Desktop Pet: whether the desktop pet feature is enabled
  'pet.enabled'?: boolean;
  // Desktop Pet: size in pixels (200, 280, or 360)
  'pet.size'?: number;
  // Desktop Pet: do not disturb mode (pet stays idle, ignores AI events)
  'pet.dnd'?: boolean;
  // Desktop Pet: whether tool-call confirmations are routed to the pet's bubble
  // (true) or remain in the main chat window (false). Default true.
  'pet.confirmEnabled'?: boolean;
}

/**
 * Legacy config keys that may still exist on disk from older SynonAI releases.
 *
 * New business truth must not be added here. Keep this surface migration-only:
 * renderer/process code may read these keys during one-shot imports into the
 * Synon Biomed backend, but all current writes use the fusion-owned services.
 */
export interface ILegacyConfigStorageRefer extends IConfigStorageRefer {
  'google.config'?: {
    /** Proxy URL for Google OAuth endpoint reachability / Google OAuth 端点代理 */
    proxy?: string;
  };
  /** Global LLM prompt timeout in seconds (default: 300). Per-backend promptTimeout overrides this. */
  'acp.promptTimeout'?: number;
  'mcp.config'?: IMcpServer[];
  'tools.imageGenerationModel'?: TProviderWithModel & {
    /** @deprecated Image generation is now controlled via built-in MCP server toggle */
    switch?: boolean;
  };
  'tools.speechToText'?: SpeechToTextConfig;
  'model.config'?: unknown;
}

export interface IEnvStorageRefer {
  'synon-ai.dir': {
    workDir: string;
    cacheDir: string;
    logDir?: string;
  };
}

export type TChatConversationStatus = 'pending' | 'running' | 'finished' | 'error' | 'cancelled';
export type TConversationRuntimeStateKind =
  | 'idle'
  | 'starting'
  | 'running'
  | 'cancelling'
  | 'paused'
  | 'waiting_approval'
  | 'waiting_confirmation'
  | 'waiting_input';

export type TConversationRuntimeSummary = {
  state: TConversationRuntimeStateKind;
  can_send_message: boolean;
  has_task: boolean;
  task_status?: TChatConversationStatus;
  is_processing: boolean;
  pending_confirmations: number;
  turn_id: string | null;
};

export type TConversationAssistantIdentity = {
  id: string;
  source: string;
  name: string;
  avatar: string;
  backend: string;
};

interface IChatConversation<T, Extra> {
  created_at: number;
  modified_at: number;
  name: string;
  desc?: string;
  id: string;
  type: T;
  extra: Extra;
  model: TProviderWithModel;
  status?: TChatConversationStatus | undefined;
  runtime?: TConversationRuntimeSummary;
  /** Explicit assistant identity for assistant-led conversations */
  assistant?: TConversationAssistantIdentity;
}

// Token 使用统计数据类型
export interface TokenUsageData {
  total_tokens: number;
}

export type TChatConversation =
  | Omit<
      IChatConversation<
        'acp',
        {
          workspace?: string;
          backend: string;
          cli_path?: string;
          custom_workspace?: boolean;
          agent_name?: string;
          preset_context?: string; // 智能助手的预设规则/提示词 / Preset context from smart assistant
          /** Skills snapshot for this conversation — authoritative list, written
           * once at creation. Join with `GET /api/skills` for descriptions. */
          skills?: string[];
          /** MCP server id snapshot chosen when the conversation was created. */
          mcp_server_ids?: string[];
          /** MCP server name snapshot chosen when the conversation was created. */
          mcp_servers?: string[];
          /** Conversation-scoped MCP status snapshot shown in the sendbox menu. */
          mcp_statuses?: IConversationMcpStatus[];
          /** Session-only MCP server snapshot persisted at creation time. */
          session_mcp_servers?: ISessionMcpServer[];
          /** 预设助手 ID，用于在会话面板显示助手名称和头像 / Preset assistant ID for displaying name and avatar in conversation panel */
          preset_assistant_id?: string;
          /** 是否置顶会话 / Whether this conversation is pinned */
          pinned?: boolean;
          /** 置顶时间戳（毫秒）/ Pin timestamp in milliseconds */
          pinned_at?: number;
          /** ACP 后端的 session UUID，用于会话恢复 / ACP backend session UUID for session resume */
          acp_session_id?: string;
          /** Conversation ID that owns the ACP session / 拥有该 ACP session 的会话 ID */
          acp_session_conversation_id?: string;
          /** ACP session 最后更新时间 / Last update time of ACP session */
          acp_session_updated_at?: number;
          /** Last context usage from usage_update */
          last_token_usage?: TokenUsageData;
          /** Context window capacity from usage_update */
          last_context_limit?: number;
          /** Persisted session mode for resume support / 持久化的会话模式，用于恢复 */
          session_mode?: string;
          /** Persisted model ID for resume support / 持久化的模型 ID，用于恢复 */
          current_model_id?: string;
          /** Cached config options from ACP backend / 缓存的 ACP 配置选项 */
          cached_config_options?: import('@/common/types/platform/acpTypes').AcpSessionConfigOption[];
          /** Pending config option selections from Guid page / Guid 页面待应用的配置选项 */
          pending_config_options?: Record<string, string>;
          /** Legacy marker for pre-provider-probe health-check conversations */
          is_health_check?: boolean;
          /** Cron job ID that spawned this conversation */
          cron_job_id?: string;
          /** Synon Biomed project and frame identity for native workspace actions. */
          project_id?: string;
          project_name?: string;
          root_frame_id?: string;
          parent_frame_id?: string | null;
          conversation_type?: string;
          frame_status?: string | null;
          status_description?: string | null;
        }
      >,
      'model'
    >
  | Omit<
      IChatConversation<
        'synonbiomed-project',
        {
          backend: 'synonbiomed';
          workspace: string;
          custom_workspace: false;
          project_id: string;
          conversation_count?: number;
          artifact_count?: number;
          agent_name?: string;
          pinned?: boolean;
          pinned_at?: number;
        }
      >,
      'model'
    >;

export type IChatConversationRefer = {
  'chat.history': TChatConversation[];
};

export type ModelType =
  | 'text' // 文本对话
  | 'vision' // 视觉理解
  | 'function_calling' // 工具调用
  | 'image_generation' // 图像生成
  | 'web_search' // 网络搜索
  | 'reasoning' // 推理模型
  | 'embedding' // 嵌入模型
  | 'rerank' // 重排序模型
  | 'excludeFromPrimary'; // 排除：不适合作为主力模型

export type ModelCapability = {
  type: ModelType;
  /**
   * 是否为用户手动选择，如果为true，则表示用户手动选择了该类型，否则表示用户手动禁止了该模型；如果为undefined，则表示使用默认值
   */
  isUserSelected?: boolean;
};

export interface IProvider {
  id: string;
  platform: string;
  name: string;
  base_url: string;
  api_key: string;
  models: string[];
  /**
   * 模型能力标签列表。打了标签就是支持，没打就是不支持
   */
  capabilities?: ModelCapability[];
  /**
   * 上下文token限制，可选字段，只在明确知道时填写
   */
  context_limit?: number;
  /**
   * 每个模型的协议覆盖配置。映射模型名称到协议字符串。
   * 仅在 platform 为 'new-api' 时使用。
   * Per-model protocol overrides. Maps model name to protocol string.
   * Only used when platform is 'new-api'.
   * e.g. { "gemini-2.5-pro": "gemini", "claude-sonnet-4": "anthropic", "gpt-4o": "openai" }
   */
  model_protocols?: Record<string, string>;
  /**
   * AWS Bedrock specific configuration
   * Only used when platform is 'bedrock'
   */
  bedrock_config?: {
    auth_method: 'accessKey' | 'profile';
    region: string;
    // For access key method
    access_key_id?: string;
    secret_access_key?: string;
    // For profile method
    profile?: string;
  };
  /**
   * 供应商启用状态，默认为 true
   * Provider enabled state, defaults to true
   */
  enabled?: boolean;
  /**
   * 各个模型的启用状态，默认全部为 true
   * Individual model enabled states, defaults to all true
   */
  model_enabled?: Record<string, boolean>;
  /**
   * 各个模型的健康检测结果（仅用于 UI 显示，不影响启用状态）
   * Model health check results (for UI display only, does not affect enabled state)
   */
  model_health?: Record<
    string,
    {
      status: 'unknown' | 'healthy' | 'unhealthy';
      last_check?: number; // 时间戳 / timestamp
      latency?: number; // 延迟时间（毫秒）/ latency in milliseconds
      error?: string; // 错误信息 / error message
    }
  >;
  is_full_url?: boolean;
}

export type TProviderWithModel = Omit<IProvider, 'models'> & {
  use_model: string;
};

// MCP Server Configuration Types
export type McpTransportType = 'stdio' | 'sse' | 'http';

export interface IMcpServerTransportStdio {
  type: 'stdio';
  command: string;
  args?: string[];
  env?: Record<string, string>;
}

export interface IMcpServerTransportSSE {
  type: 'sse';
  url: string;
  headers?: Record<string, string>;
}

export interface IMcpServerTransportHTTP {
  type: 'http';
  url: string;
  headers?: Record<string, string>;
}

export interface IMcpServerTransportStreamableHTTP {
  type: 'streamable_http';
  url: string;
  headers?: Record<string, string>;
}

export type IMcpServerTransport =
  | IMcpServerTransportStdio
  | IMcpServerTransportSSE
  | IMcpServerTransportHTTP
  | IMcpServerTransportStreamableHTTP;

export interface IMcpServer {
  id: string;
  name: string;
  description?: string;
  enabled: boolean; // 是否默认启用（新会话默认勾选）
  transport: IMcpServerTransport;
  tools?: IMcpTool[];
  last_test_status?: 'connected' | 'disconnected' | 'error' | 'testing'; // 最近一次检测结果
  last_connected?: number;
  created_at: number;
  updated_at: number;
  original_json: string; // 存储原始JSON配置，用于编辑时的准确显示
  /** Built-in MCP server managed by SynonAI (hide edit/delete in UI) */
  builtin?: boolean;
}

export type ISessionMcpServer = Pick<IMcpServer, 'id' | 'name' | 'transport'>;

export type IConversationMcpStatusKind = 'loaded' | 'failed' | 'unsupported';

export interface IConversationMcpStatus {
  id: string;
  name: string;
  status: IConversationMcpStatusKind;
  reason?: string;
}

/** Stable ID for the built-in image generation MCP server */
export const BUILTIN_IMAGE_GEN_ID = 'builtin-image-gen';
export const BUILTIN_IMAGE_GEN_NAME = 'synon-ai-image-generation';
export const BUILTIN_IMAGE_GEN_LEGACY_NAMES = ['SynonAI Image Generation', BUILTIN_IMAGE_GEN_ID] as const;

export interface IMcpTool {
  name: string;
  description?: string;
  input_schema?: unknown;
  _meta?: Record<string, unknown>;
}

/**
 * CSS 主题配置接口 / CSS Theme configuration interface
 * 用于存储用户自定义的 CSS 皮肤 / Used to store user-defined CSS skins
 */
export interface ICssTheme {
  id: string; // 唯一标识 / Unique identifier
  name: string; // 主题名称 / Theme name
  cover?: string; // 封面图片 base64 或 URL / Cover image base64 or URL
  css: string; // CSS 样式代码 / CSS style code
  is_preset?: boolean; // 是否为预设主题 / Whether it's a preset theme
  created_at: number; // 创建时间 / Creation time
  updated_at: number; // 更新时间 / Update time
}

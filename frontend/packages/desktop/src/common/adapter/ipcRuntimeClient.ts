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

import { bridge } from '@office-ai/platform';
import { httpGet, withResponseMap, wsEmitter } from './httpBridge';
import type { IBridgeResponse } from './ipcBridgeTypes';

export const runtime = {
  statusChanged: wsEmitter<IRuntimeStatusEvent>('durable', 'runtime.statusChanged'),
};

export interface IKernelExecutionCellUpdateEvent {
  frame_id: string;
  root_frame_id: string;
  phase: string;
  origin: string;
  language?: string;
  environment?: string;
  kernel_target?: string;
  tool_use_id?: string;
}

export const kernel = {
  executionCellUpdate: wsEmitter<IKernelExecutionCellUpdateEvent>('durable', 'execution_cell_update'),
};

export interface IComputeJobUpdateEvent {
  job_id: string;
  project_id: string;
  root_frame_id?: string;
  frame_id?: string;
  provider?: string;
  state?: string;
  intent?: unknown;
}

export interface IComputeJobLogChunkEvent {
  job_id: string;
  project_id: string;
  root_frame_id?: string;
  frame_id?: string;
  stream: 'out' | 'err';
  chunk: string;
}

export const compute = {
  jobUpdate: wsEmitter<IComputeJobUpdateEvent>('durable', 'compute_job_update'),
  jobLogChunk: wsEmitter<IComputeJobLogChunkEvent>('durable', 'compute_job_log_chunk'),
};

// ---------------------------------------------------------------------------
// CDP status / config types (used by application, stays IPC)
// ---------------------------------------------------------------------------

export interface ICdpStatus {
  enabled: boolean;
  port: number | null;
  startupEnabled: boolean;
  instances: Array<{
    pid: number;
    port: number;
    cwd: string;
    startTime: number;
  }>;
  configEnabled: boolean;
  isDevMode: boolean;
}

export interface ICdpConfig {
  enabled?: boolean;
  port?: number;
}

export type RuntimeStatusScopeKind = 'conversation' | 'mcp';
export type RuntimeResourceKind = 'node' | 'acp_tool';
export type RuntimeStatusPhase =
  | 'waiting_for_lock'
  | 'waiting_input'
  | 'downloading'
  | 'extracting'
  | 'validating'
  | 'ready'
  | 'failed';
export type RuntimeFailureKind =
  | 'timeout'
  | 'download_failed'
  | 'http_status'
  | 'checksum_mismatch'
  | 'validation_failed'
  | 'unsupported_platform'
  | 'bundled_resource_missing'
  | 'bundled_resource_invalid'
  | 'unknown';

export interface IRuntimeStatusScope {
  kind: RuntimeStatusScopeKind;
  id: string;
}

export interface IRuntimeStatusEvent {
  resource: RuntimeResourceKind;
  resource_id?: string;
  scope: IRuntimeStatusScope;
  phase: RuntimeStatusPhase;
  failure_kind?: RuntimeFailureKind;
  message?: string;
  root_frame_id?: string;
  frame_id?: string;
  status_code?: number;
  terminal_status?: 'completed' | 'failed' | 'cancelled';
  /** Transcript publication boundary that requires a durable history card refresh. */
  boundary_kind?: 'attempt' | 'tool';
  tool_call_id?: string;
  tool_name?: string;
  source_publication_sequence?: number;
  publication_boundary_id?: string;
}

export interface IStartOnBootStatus {
  supported: boolean;
  enabled: boolean;
  isPackaged: boolean;
  platform: string;
}

/** Hardware acceleration / GPU recovery status — see process/utils/gpuRecovery */
export type IGpuOverride = 'force-on' | 'force-off';

export interface IGpuStatus {
  /** User-set override; null means follow auto-recovery */
  userOverride: IGpuOverride | null;
  /** Whether auto-recovery has disabled hardware acceleration after repeated crashes */
  autoDisabled: boolean;
  crashCount: number;
  lastCrashAt: number | null;
}

export interface IAppRestartResult {
  restarted: boolean;
  manualRestartRequired: boolean;
  reason?: 'dev-mode';
}

export type IRendererLogLevel = 'debug' | 'info' | 'warn' | 'error';

export interface IRendererLogEntry {
  level: IRendererLogLevel;
  tag: string;
  message: string;
  data?: unknown;
}

// ---------------------------------------------------------------------------
// Application — stays IPC (Electron-native)
// ---------------------------------------------------------------------------

export const application = {
  restart: bridge.buildProvider<IAppRestartResult, void>('restart-app'),
  openDevTools: bridge.buildProvider<boolean, void>('open-dev-tools'),
  isDevToolsOpened: bridge.buildProvider<boolean, void>('is-dev-tools-opened'),
  systemInfo: withResponseMap(
    httpGet<
      {
        cache_dir: string;
        work_dir: string;
        log_dir: string;
        platform: string;
        arch: string;
      },
      void
    >('/api/system/info'),
    (raw) => ({
      cacheDir: raw.cache_dir,
      workDir: raw.work_dir,
      logDir: raw.log_dir,
      platform: raw.platform,
      arch: raw.arch,
    })
  ),
  getPath: bridge.buildProvider<string, { name: 'desktop' | 'home' | 'downloads' }>('app.get-path'),
  // Electron-local: copies cache dir + persists to ProcessEnv, paired with restart.
  // The backend reads SYNON_AI_*_DIR env vars on boot, so it does not own this config.
  updateSystemInfo: bridge.buildProvider<void, { cacheDir: string; workDir: string; logDir?: string }>(
    'update-system-info'
  ),
  getZoomFactor: bridge.buildProvider<number, void>('app.get-zoom-factor'),
  setZoomFactor: bridge.buildProvider<number, { factor: number }>('app.set-zoom-factor'),
  getCdpStatus: bridge.buildProvider<IBridgeResponse<ICdpStatus>, void>('app.get-cdp-status'),
  updateCdpConfig: bridge.buildProvider<IBridgeResponse<ICdpConfig>, Partial<ICdpConfig>>('app.update-cdp-config'),
  getStartOnBootStatus: bridge.buildProvider<IBridgeResponse<IStartOnBootStatus>, void>('app.get-start-on-boot-status'),
  setStartOnBoot: bridge.buildProvider<IBridgeResponse<IStartOnBootStatus>, { enabled: boolean }>(
    'app.set-start-on-boot'
  ),
  getGpuStatus: bridge.buildProvider<IBridgeResponse<IGpuStatus>, void>('app.get-gpu-status'),
  setGpuOverride: bridge.buildProvider<IBridgeResponse<IGpuStatus>, { override: IGpuOverride | null }>(
    'app.set-gpu-override'
  ),
  writeRendererLog: bridge.buildProvider<void, IRendererLogEntry>('app.write-renderer-log'),
  logStream: bridge.buildEmitter<{
    level: 'log' | 'warn' | 'error';
    tag: string;
    message: string;
    data?: unknown;
  }>('app.log-stream'),
  devToolsStateChanged: bridge.buildEmitter<{ isOpen: boolean }>('app.devtools-state-changed'),
};

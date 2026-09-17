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
import type { PreviewHistoryTarget, PreviewSnapshotInfo } from '../types/office/preview';
import type { Theme } from '@/common/theme/types';
import { httpPost, httpPut, unavailableEmitter } from './httpBridge';
import { httpGetClientSetting } from './ipcClientSettings';

// ---------------------------------------------------------------------------
// Preview History — routed to /api/preview-history/*
// ---------------------------------------------------------------------------

function mapPreviewTarget(target: PreviewHistoryTarget): Record<string, unknown> {
  return {
    ...target,
    content_type: target.contentType,
    contentType: undefined,
  };
}

export const previewHistory = {
  list: httpPost<PreviewSnapshotInfo[], { target: PreviewHistoryTarget }>('/api/preview-history/list', (p) => ({
    target: mapPreviewTarget(p.target),
  })),
  save: httpPost<PreviewSnapshotInfo, { target: PreviewHistoryTarget; content: string }>(
    '/api/preview-history/save',
    (p) => ({
      target: mapPreviewTarget(p.target),
      content: p.content,
    })
  ),
  getContent: httpPost<
    { snapshot: PreviewSnapshotInfo; content: string } | null,
    { target: PreviewHistoryTarget; snapshot_id: string }
  >('/api/preview-history/get-content', (p) => ({
    target: mapPreviewTarget(p.target),
    snapshot_id: p.snapshot_id,
  })),
};

// Preview panel
export const preview = {
  open: unavailableEmitter<{
    content: string;
    content_type: import('../types/office/preview').PreviewContentType;
    metadata?: {
      title?: string;
      file_name?: string;
    };
  }>('realtime_event_unavailable_preview_open'),
};

// ---------------------------------------------------------------------------
// Document conversion
// ---------------------------------------------------------------------------

export const document = {
  convert: httpPost<
    import('../types/office/conversion').DocumentConversionResponse,
    import('../types/office/conversion').DocumentConversionRequest
  >('/api/document/convert'),
};

// ---------------------------------------------------------------------------
// Office Previews — routed to /api/*-preview/*
// ---------------------------------------------------------------------------

export const pptPreview = {
  start: httpPost<
    { url: string; error?: string },
    {
      file_path?: string;
      artifact_id?: string;
      version_id?: string;
      workspace?: string;
    }
  >('/api/ppt-preview/start'),
  stop: httpPost<void, { file_path?: string; artifact_id?: string; version_id?: string }>('/api/ppt-preview/stop'),
  status: unavailableEmitter<{
    state: 'starting' | 'installing' | 'ready' | 'error';
    message?: string;
  }>('realtime_event_unavailable_ppt_preview_status'),
};

export const wordPreview = {
  start: httpPost<
    { url: string; error?: string },
    {
      file_path?: string;
      artifact_id?: string;
      version_id?: string;
      workspace?: string;
    }
  >('/api/word-preview/start'),
  stop: httpPost<void, { file_path?: string; artifact_id?: string; version_id?: string }>('/api/word-preview/stop'),
  status: unavailableEmitter<{
    state: 'starting' | 'installing' | 'ready' | 'error';
    message?: string;
  }>('realtime_event_unavailable_word_preview_status'),
};

export const excelPreview = {
  start: httpPost<
    { url: string; error?: string },
    {
      file_path?: string;
      artifact_id?: string;
      version_id?: string;
      workspace?: string;
    }
  >('/api/excel-preview/start'),
  stop: httpPost<void, { file_path?: string; artifact_id?: string; version_id?: string }>('/api/excel-preview/stop'),
  status: unavailableEmitter<{
    state: 'starting' | 'installing' | 'ready' | 'error';
    message?: string;
  }>('realtime_event_unavailable_excel_preview_status'),
};

// ---------------------------------------------------------------------------
// Deep Link — stays IPC (Electron protocol handler)
// ---------------------------------------------------------------------------

export const deepLink = {
  received: bridge.buildEmitter<{
    action: string;
    params: Record<string, string>;
  }>('deep-link.received'),
};

// ---------------------------------------------------------------------------
// Window Controls — stays IPC (Electron-native)
// ---------------------------------------------------------------------------

export const windowControls = {
  minimize: bridge.buildProvider<void, void>('window-controls:minimize'),
  maximize: bridge.buildProvider<void, void>('window-controls:maximize'),
  unmaximize: bridge.buildProvider<void, void>('window-controls:unmaximize'),
  close: bridge.buildProvider<void, void>('window-controls:close'),
  isMaximized: bridge.buildProvider<boolean, void>('window-controls:is-maximized'),
  maximizedChanged: bridge.buildEmitter<{ is_maximized: boolean }>('window-controls:maximized-changed'),
};

// ---------------------------------------------------------------------------
// Theme — stays IPC (main process owns the resolved-theme cache)
// ---------------------------------------------------------------------------

export const theme = {
  // main → all renderers: the resolved active theme changed
  changed: bridge.buildEmitter<Theme>('theme:changed'),
  // renderer → main: publish a newly resolved theme (main caches + re-emits `changed`)
  setActive: bridge.buildProvider<void, Theme>('theme:set-active'),
  // any window → main: pull the currently cached resolved theme on load (null if none yet)
  requestCurrent: bridge.buildProvider<Theme | null, void>('theme:request-current'),
};

// ---------------------------------------------------------------------------
// System Settings — routed to /api/settings/* unless they need Electron-native side effects.
// ---------------------------------------------------------------------------

export const systemSettings = {
  getCloseToTray: bridge.buildProvider<boolean, void>('system-settings:get-close-to-tray'),
  setCloseToTray: bridge.buildProvider<void, { enabled: boolean }>('system-settings:set-close-to-tray'),
  getNotificationEnabled: httpGetClientSetting<boolean>('notificationEnabled'),
  setNotificationEnabled: httpPut<void, { enabled: boolean }>('/api/settings/client', (p) => ({
    notificationEnabled: p.enabled,
  })),
  getCronNotificationEnabled: httpGetClientSetting<boolean>('cronNotificationEnabled'),
  setCronNotificationEnabled: httpPut<void, { enabled: boolean }>('/api/settings/client', (p) => ({
    cronNotificationEnabled: p.enabled,
  })),
  getKeepAwake: httpGetClientSetting<boolean>('keepAwake'),
  setKeepAwake: httpPut<void, { enabled: boolean }>('/api/settings/client', (p) => ({
    keepAwake: p.enabled,
  })),
  changeLanguage: httpPut<void, { language: string }>('/api/settings/client', (p) => ({
    language: p.language,
  })),
  languageChanged: unavailableEmitter<{ language: string }>('realtime_event_unavailable_language_changed'),
  getSaveUploadToWorkspace: httpGetClientSetting<boolean>('saveUploadToWorkspace'),
  setSaveUploadToWorkspace: httpPut<void, { enabled: boolean }>('/api/settings/client', (p) => ({
    saveUploadToWorkspace: p.enabled,
  })),
  getPetEnabled: bridge.buildProvider<boolean, void>('system-settings:get-pet-enabled'),
  setPetEnabled: bridge.buildProvider<void, { enabled: boolean }>('system-settings:set-pet-enabled'),
  getPetSize: bridge.buildProvider<number, void>('system-settings:get-pet-size'),
  setPetSize: bridge.buildProvider<void, { size: number }>('system-settings:set-pet-size'),
  getPetDnd: bridge.buildProvider<boolean, void>('system-settings:get-pet-dnd'),
  setPetDnd: bridge.buildProvider<void, { dnd: boolean }>('system-settings:set-pet-dnd'),
  getPetConfirmEnabled: bridge.buildProvider<boolean, void>('system-settings:get-pet-confirm-enabled'),
  setPetConfirmEnabled: bridge.buildProvider<void, { enabled: boolean }>('system-settings:set-pet-confirm-enabled'),
};

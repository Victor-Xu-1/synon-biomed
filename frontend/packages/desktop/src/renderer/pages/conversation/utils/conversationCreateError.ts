/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { isBackendHttpError } from '@/common/adapter/httpBridge';
import { parseError } from '@/common/utils';
import type { TFunction } from 'i18next';

export type WorkspacePathErrorCode = 'WORKSPACE_PATH_UNAVAILABLE' | 'WORKSPACE_PATH_RUNTIME_UNAVAILABLE';
export type ConversationCreateErrorCode = 'WORKSPACE_PATH_UNAVAILABLE';
export type ConversationRuntimeWorkspaceErrorCode = 'WORKSPACE_PATH_RUNTIME_UNAVAILABLE';

const BACKEND_ERROR_CODE_MAP: Record<string, WorkspacePathErrorCode> = {
  WORKSPACE_PATH_UNAVAILABLE: 'WORKSPACE_PATH_UNAVAILABLE',
  WORKSPACE_PATH_RUNTIME_UNAVAILABLE: 'WORKSPACE_PATH_RUNTIME_UNAVAILABLE',
  WORKSPACE_PATH_CONTAINS_WHITESPACE_UNSUPPORTED: 'WORKSPACE_PATH_UNAVAILABLE',
  WORKSPACE_PATH_CONTAINS_WHITESPACE_RUNTIME_UNSUPPORTED: 'WORKSPACE_PATH_RUNTIME_UNAVAILABLE',
  WORKSPACE_TRAILING_WHITESPACE_UNSUPPORTED: 'WORKSPACE_PATH_UNAVAILABLE',
};

type EmbeddedBackendErrorPayload = {
  code?: string;
  error?: string;
  details?: unknown;
};

type WorkspacePathErrorDetails = {
  workspace_path?: string;
};

const getEmbeddedBackendErrorPayload = (error: unknown): EmbeddedBackendErrorPayload | undefined => {
  const parsedError = parseError(error);
  const rawMessage =
    typeof error === 'string'
      ? error
      : error instanceof Error
        ? error.message
        : typeof parsedError === 'string'
          ? parsedError
          : '';

  if (!rawMessage) {
    return undefined;
  }

  const jsonStart = rawMessage.indexOf('{');
  if (jsonStart < 0) {
    return undefined;
  }

  try {
    const payload = JSON.parse(rawMessage.slice(jsonStart)) as EmbeddedBackendErrorPayload;
    if (payload && typeof payload === 'object') {
      return payload;
    }
  } catch {
    return undefined;
  }

  return undefined;
};

const getWorkspacePathFromDetails = (details: unknown): string | undefined => {
  if (!details || typeof details !== 'object') {
    return undefined;
  }

  const workspacePath = (details as WorkspacePathErrorDetails).workspace_path;
  return typeof workspacePath === 'string' && workspacePath.trim() ? workspacePath : undefined;
};

const getWorkspacePathErrorPayload = (error: unknown): EmbeddedBackendErrorPayload | undefined => {
  if (isBackendHttpError(error)) {
    return {
      code: error.code,
      error: error.backendMessage,
      details: error.details,
    };
  }

  return getEmbeddedBackendErrorPayload(error);
};

export const getWorkspacePathFromErrorDetails = (error: unknown): string | undefined => {
  const payload = getWorkspacePathErrorPayload(error);
  return getWorkspacePathFromDetails(payload?.details);
};

export const normalizeWorkspacePathErrorCode = (error: unknown): WorkspacePathErrorCode | undefined => {
  const payload = getWorkspacePathErrorPayload(error);
  if (payload) {
    const mappedCode = payload.code ? BACKEND_ERROR_CODE_MAP[payload.code] : undefined;
    if (mappedCode) {
      return mappedCode;
    }
  }

  return undefined;
};

export const normalizeConversationCreateErrorCode = (error: unknown): ConversationCreateErrorCode | undefined => {
  const workspaceCode = normalizeWorkspacePathErrorCode(error);
  if (workspaceCode === 'WORKSPACE_PATH_UNAVAILABLE') {
    return workspaceCode;
  }

  return undefined;
};

export const normalizeConversationRuntimeWorkspaceErrorCode = (
  error: unknown
): ConversationRuntimeWorkspaceErrorCode | undefined => {
  const code = normalizeWorkspacePathErrorCode(error);
  return code === 'WORKSPACE_PATH_RUNTIME_UNAVAILABLE' ? code : undefined;
};

export const getConversationCreateErrorMessage = (error: unknown, t: TFunction): string => {
  const normalizedCode = normalizeConversationCreateErrorCode(error);
  const workspacePath = getWorkspacePathFromErrorDetails(error);

  if (normalizedCode && workspacePath) {
    return t(`conversation.createError.pathVariants.${normalizedCode}`, {
      workspacePath,
    });
  }

  return t('conversation.createFailed');
};

export const getConversationRuntimeWorkspaceErrorMessage = (error: unknown, t: TFunction): string => {
  const normalizedCode = normalizeConversationRuntimeWorkspaceErrorCode(error);
  const workspacePath = getWorkspacePathFromErrorDetails(error);

  if (normalizedCode) {
    if (workspacePath) {
      return t(`conversation.agentError.codes.${normalizedCode}.bodyWithPath`, {
        workspacePath,
      });
    }

    return t(`conversation.agentError.codes.${normalizedCode}.body`);
  }

  return t('common.unknownError');
};

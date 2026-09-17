/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { TProviderWithModel } from '../config/storage';

export type ApiProviderWithModel = {
  provider_id: string;
  model: string;
  use_model?: string;
};

// ── Frontend → Backend ──────────────────────────────────────────────────

/** Minimal shape of a create-conversation request consumed by the body builder. */
export type CreateConversationBodyInput = {
  type?: 'acp';
  id?: string;
  name?: string;
  assistant?: unknown;
  extra?: unknown;
};

/**
 * Build the HTTP body for `POST /api/conversations`.
 *
 * Synon Biomed conversations are assistant-first ACP conversations. Runtime
 * model defaults are carried in assistant/config options, not in the retired
 * generic top-level `model` field.
 */
export function buildCreateConversationBody(p: CreateConversationBodyInput): Record<string, unknown> {
  const hasAssistant = p.assistant !== undefined && p.assistant !== null;
  const body: Record<string, unknown> = {
    type: hasAssistant ? undefined : p.type,
    id: p.id,
    name: p.name,
    assistant: p.assistant,
    extra: p.extra,
  };
  return body;
}

// ── Backend → Frontend ──────────────────────────────────────────────────

export function fromApiModel(raw: ApiProviderWithModel): TProviderWithModel {
  return {
    id: raw.provider_id,
    platform: '',
    name: '',
    base_url: '',
    api_key: '',
    use_model: raw.use_model ?? raw.model,
  };
}

function fromApiModelOptional(raw?: ApiProviderWithModel | null): TProviderWithModel | undefined {
  return raw ? fromApiModel(raw) : undefined;
}

export function fromApiConversation<T>(raw: T): T {
  if (!raw || typeof raw !== 'object') return raw;

  const r = raw as T & {
    model?: ApiProviderWithModel | null;
    extra?: Record<string, unknown> | null;
  };
  const next = { ...r } as unknown as T & {
    model?: TProviderWithModel;
    extra?: Record<string, unknown> | null;
  };

  if ('model' in r) {
    next.model = fromApiModelOptional(r.model);
  }

  const extra = r.extra;
  if (extra && typeof extra === 'object' && !('custom_workspace' in extra)) {
    const workspace = typeof extra.workspace === 'string' ? extra.workspace : '';
    const isTemporary = extra.is_temporary_workspace === true;
    next.extra = {
      ...extra,
      custom_workspace: workspace.length > 0 && !isTemporary,
    };
  }

  return next;
}

export function fromApiPaginatedConversations<T>(result: {
  items: T[];
  total: number;
  has_more: boolean;
  next_cursor?: string | null;
}): {
  items: T[];
  total: number;
  has_more: boolean;
  next_cursor?: string | null;
} {
  return {
    ...result,
    items: result.items.map(fromApiConversation),
  };
}

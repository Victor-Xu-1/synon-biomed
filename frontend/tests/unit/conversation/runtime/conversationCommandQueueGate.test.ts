/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { describe, expect, it } from 'vitest';
import {
  createQueuedCommandItem,
  getCommandQueueExecutionGate,
  normalizeQueueState,
  reorderQueuedCommand,
  validateQueuedCommandItem,
  type ConversationCommandQueueItem,
} from '@/renderer/pages/conversation/platforms/useConversationCommandQueue';

describe('getCommandQueueExecutionGate', () => {
  it('keeps the legacy path gated by hydration and busy state', () => {
    expect(getCommandQueueExecutionGate({ isBusy: true, isHydrated: true })).toEqual({
      hydrated: true,
      canExecute: false,
      isProcessing: true,
    });

    expect(getCommandQueueExecutionGate({ isBusy: false, isHydrated: false })).toEqual({
      hydrated: false,
      canExecute: true,
      isProcessing: false,
    });
  });

  it('does not execute runtime-gated commands before hydration', () => {
    expect(
      getCommandQueueExecutionGate({
        isBusy: false,
        runtimeGate: {
          hydrated: false,
          canSendMessage: true,
          isProcessing: false,
        },
      })
    ).toEqual({
      hydrated: false,
      canExecute: true,
      isProcessing: false,
    });
  });

  it('does not execute when runtime cannot send', () => {
    expect(
      getCommandQueueExecutionGate({
        isBusy: false,
        runtimeGate: {
          hydrated: true,
          canSendMessage: false,
          isProcessing: false,
        },
      })
    ).toEqual({
      hydrated: true,
      canExecute: false,
      isProcessing: false,
    });
  });

  it('does not execute while runtime is processing', () => {
    expect(
      getCommandQueueExecutionGate({
        isBusy: false,
        runtimeGate: {
          hydrated: true,
          canSendMessage: true,
          isProcessing: true,
        },
      })
    ).toEqual({
      hydrated: true,
      canExecute: false,
      isProcessing: true,
    });
  });

  it('executes only when runtime is hydrated, sendable, and idle', () => {
    expect(
      getCommandQueueExecutionGate({
        isBusy: true,
        runtimeGate: {
          hydrated: true,
          canSendMessage: true,
          isProcessing: false,
        },
      })
    ).toEqual({
      hydrated: true,
      canExecute: true,
      isProcessing: false,
    });
  });
});

describe('queued composer composition', () => {
  it('preserves context-only Skill and MCP selections across queue persistence', () => {
    const item = createQueuedCommandItem({
      input: '',
      files: [],
      contextItems: [
        { kind: 'skill', name: 'single-cell-analysis', label: 'single-cell-analysis' },
        { kind: 'mcp', serverId: 'pubmed', label: 'PubMed' },
      ],
    });
    const normalized = normalizeQueueState({ items: [item], isPaused: false });
    expect(normalized.items[0]?.contextItems).toEqual(item.contextItems);
    expect(validateQueuedCommandItem(item, { items: [], isPaused: false })).toMatchObject({ ok: true });
  });
});

describe('reorderQueuedCommand', () => {
  it('moves a selected task to the requested position without changing the remaining order', () => {
    const items = [
      { id: 'one', input: 'one', files: [], contextItems: [], created_at: 1 },
      { id: 'two', input: 'two', files: [], contextItems: [], created_at: 2 },
      { id: 'three', input: 'three', files: [], contextItems: [], created_at: 3 },
    ] satisfies ConversationCommandQueueItem[];

    expect(reorderQueuedCommand(items, 'three', 'one').map((item) => item.id)).toEqual(['three', 'one', 'two']);
    expect(reorderQueuedCommand(items, 'one', 'one')).toBe(items);
  });
});

/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import {
  createConversationListRefreshController,
  createUnknownConversationRefreshGuard,
  shouldRefreshConversationListAfterChange,
} from '@/renderer/pages/conversation/GroupedHistory/hooks/useConversationListSync';
import { afterEach, describe, expect, it, vi } from 'vitest';

describe('conversation list refresh controller', () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it('coalesces a burst of sidebar events into one delayed refresh', async () => {
    vi.useFakeTimers();
    const load = vi.fn(async () => undefined);
    const controller = createConversationListRefreshController(load, 120);

    for (let index = 0; index < 32; index += 1) controller.schedule();
    await vi.advanceTimersByTimeAsync(119);
    expect(load).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(load).toHaveBeenCalledTimes(1);
  });

  it('allows one follow-up refresh when events arrive during an in-flight request', async () => {
    vi.useFakeTimers();
    let release: (() => void) | undefined;
    const load = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          release = resolve;
        })
    );
    const controller = createConversationListRefreshController(load, 120);

    const first = controller.run();
    await Promise.resolve();
    controller.run();
    controller.schedule();
    expect(load).toHaveBeenCalledTimes(1);

    release?.();
    await first;
    await vi.runAllTimersAsync();
    expect(load).toHaveBeenCalledTimes(2);
  });

  it('admits one discovery refresh per unknown streaming conversation', () => {
    const guard = createUnknownConversationRefreshGuard(4);

    expect(guard.shouldRefresh('conversation-a', false)).toBe(true);
    for (let index = 0; index < 64; index += 1) {
      expect(guard.shouldRefresh('conversation-a', false)).toBe(false);
    }
    expect(guard.shouldRefresh('conversation-known', true)).toBe(false);
    guard.remember('conversation-announced');
    expect(guard.shouldRefresh('conversation-announced', false)).toBe(false);
    expect(guard.size()).toBe(2);
  });

  it('bounds unknown streaming conversation retention and evicts oldest ids', () => {
    const guard = createUnknownConversationRefreshGuard(2);

    expect(guard.shouldRefresh('conversation-a', false)).toBe(true);
    expect(guard.shouldRefresh('conversation-b', false)).toBe(true);
    expect(guard.shouldRefresh('conversation-c', false)).toBe(true);
    expect(guard.size()).toBe(2);
    expect(guard.shouldRefresh('conversation-a', false)).toBe(true);
  });

  it('does not reload the full sidebar collection for active known conversation updates', () => {
    expect(
      shouldRefreshConversationListAfterChange({
        action: 'updated',
        known: true,
        active: true,
      })
    ).toBe(false);
    expect(
      shouldRefreshConversationListAfterChange({
        action: 'updated',
        known: true,
        active: false,
      })
    ).toBe(true);
  });

  it('still refreshes collection membership changes and unknown conversations', () => {
    expect(
      shouldRefreshConversationListAfterChange({
        action: 'created',
        known: true,
        active: true,
      })
    ).toBe(true);
    expect(
      shouldRefreshConversationListAfterChange({
        action: 'deleted',
        known: true,
        active: true,
      })
    ).toBe(true);
    expect(
      shouldRefreshConversationListAfterChange({
        action: 'updated',
        known: false,
        active: true,
      })
    ).toBe(true);
  });
});

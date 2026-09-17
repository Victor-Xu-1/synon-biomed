import { act, renderHook } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ipcBridge } from '@/common';
import { ACTIVE_LEASE_RENEW_INTERVAL_MS, useActiveLease } from '@/renderer/pages/conversation/hooks/useActiveLease';

vi.mock('@/common', () => ({
  ipcBridge: {
    conversation: {
      activeLease: {
        invoke: vi.fn().mockResolvedValue(undefined),
      },
    },
  },
}));

const conversationActiveLease = vi.mocked(ipcBridge.conversation.activeLease.invoke);

function setVisibilityState(value: DocumentVisibilityState) {
  Object.defineProperty(document, 'visibilityState', {
    configurable: true,
    value,
  });
}

describe('useActiveLease', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.clearAllMocks();
    setVisibilityState('visible');
  });

  it('renews conversation lease immediately and every 30 seconds while visible', async () => {
    renderHook(() => useActiveLease({ id: 'conv-1' }));

    expect(conversationActiveLease).toHaveBeenCalledTimes(1);
    expect(conversationActiveLease).toHaveBeenCalledWith({ conversation_id: 'conv-1' });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(ACTIVE_LEASE_RENEW_INTERVAL_MS);
    });

    expect(conversationActiveLease).toHaveBeenCalledTimes(2);
  });

  it('does not renew while hidden until the page becomes visible', () => {
    setVisibilityState('hidden');
    renderHook(() => useActiveLease({ id: 'conv-1' }));

    expect(conversationActiveLease).not.toHaveBeenCalled();

    act(() => {
      setVisibilityState('visible');
      document.dispatchEvent(new Event('visibilitychange'));
    });

    expect(conversationActiveLease).toHaveBeenCalledTimes(1);
    expect(conversationActiveLease).toHaveBeenCalledWith({ conversation_id: 'conv-1' });
  });

  it('stops renewing after unmount', async () => {
    const { unmount } = renderHook(() => useActiveLease({ id: 'conv-1' }));

    unmount();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(ACTIVE_LEASE_RENEW_INTERVAL_MS * 2);
    });

    expect(conversationActiveLease).toHaveBeenCalledTimes(1);
  });

  it('keeps renewing after window blur while document remains visible', async () => {
    renderHook(() => useActiveLease({ id: 'conv-1' }));

    act(() => {
      window.dispatchEvent(new Event('blur'));
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ACTIVE_LEASE_RENEW_INTERVAL_MS);
    });

    expect(conversationActiveLease).toHaveBeenCalledTimes(2);
  });

  it('does not renew when the conversation does not support an active runtime lease', async () => {
    renderHook(() => useActiveLease({ id: 'proj_example', enabled: false }));

    expect(conversationActiveLease).not.toHaveBeenCalled();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(ACTIVE_LEASE_RENEW_INTERVAL_MS);
    });

    expect(conversationActiveLease).not.toHaveBeenCalled();
  });
});

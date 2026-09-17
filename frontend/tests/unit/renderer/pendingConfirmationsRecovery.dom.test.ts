import { renderHook } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  removeOn: vi.fn(),
  updateMessageList: vi.fn(),
}));

vi.mock('@/common', () => ({
  ipcBridge: {
    conversation: {
      confirmation: {
        list: { invoke: mocks.list },
        remove: { on: mocks.removeOn },
      },
    },
  },
}));

vi.mock('@/renderer/pages/conversation/Messages/hooks', () => ({
  useUpdateMessageList: () => mocks.updateMessageList,
}));

import { usePendingConfirmationsRecovery } from '@/renderer/pages/conversation/Messages/usePendingConfirmationsRecovery';

describe('usePendingConfirmationsRecovery', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.list.mockResolvedValue([]);
    mocks.removeOn.mockReturnValue(() => {});
  });

  it('stays inactive when Synon Biomed owns pending input recovery', async () => {
    renderHook(() => usePendingConfirmationsRecovery('frame-stat6', { enabled: false }));

    await Promise.resolve();

    expect(mocks.list).not.toHaveBeenCalled();
    expect(mocks.removeOn).not.toHaveBeenCalled();
  });
});

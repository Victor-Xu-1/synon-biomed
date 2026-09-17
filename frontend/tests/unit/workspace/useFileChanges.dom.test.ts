/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { useFileChanges } from '@/renderer/pages/conversation/Workspace/hooks/useFileChanges';
import { renderHook } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

const ipcMocks = vi.hoisted(() => ({
  init: vi.fn().mockResolvedValue(null),
  dispose: vi.fn().mockResolvedValue(undefined),
}));

vi.mock('@/common', () => ({
  ipcBridge: {
    fileSnapshot: {
      init: { invoke: ipcMocks.init },
      dispose: { invoke: ipcMocks.dispose },
      compare: { invoke: vi.fn() },
      stageFile: { invoke: vi.fn() },
      stageAll: { invoke: vi.fn() },
      unstageFile: { invoke: vi.fn() },
      unstageAll: { invoke: vi.fn() },
      discardFile: { invoke: vi.fn() },
      resetFile: { invoke: vi.fn() },
    },
  },
}));

describe('useFileChanges', () => {
  it('does not initialize local file snapshots for a read-only Synon Biomed workspace', async () => {
    const { unmount } = renderHook(() => useFileChanges({ workspace: 'synonbiomed://proj_stat6', enabled: false }));
    await Promise.resolve();
    unmount();

    expect(ipcMocks.init).not.toHaveBeenCalled();
    expect(ipcMocks.dispose).not.toHaveBeenCalled();
  });
});

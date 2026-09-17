/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { describe, expect, it, vi } from 'vitest';
import { scheduleReactRootCleanup } from '@/renderer/utils/ui/reactRootLifecycle';

describe('react root lifecycle', () => {
  it('defers HMR cleanup until the current render stack has completed', async () => {
    const cleanup = vi.fn();

    scheduleReactRootCleanup(cleanup);

    expect(cleanup).not.toHaveBeenCalled();
    await Promise.resolve();
    expect(cleanup).toHaveBeenCalledOnce();
  });
});

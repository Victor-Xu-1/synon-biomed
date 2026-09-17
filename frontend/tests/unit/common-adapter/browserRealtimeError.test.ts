/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 *
 * @vitest-environment node
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

type BridgeEmitter = {
  emit: (name: string, data: unknown) => void;
};

type BridgeAdapter = {
  emit: (name: string, data: unknown) => void;
  on: (emitter: BridgeEmitter) => void;
};

type BrowserRuntimeWindow = {
  __emitBridgeCallback?: (name: string, data: unknown) => void;
};

const platformMock = vi.hoisted(() => ({
  adapter: vi.fn(),
  provider: vi.fn(),
}));

vi.mock('@office-ai/platform', () => ({
  bridge: { adapter: platformMock.adapter },
  logger: { provider: platformMock.provider },
}));

async function loadBrowserAdapter() {
  vi.resetModules();
  platformMock.adapter.mockClear();
  platformMock.provider.mockClear();
  const runtimeWindow: BrowserRuntimeWindow = {};
  const socketConstructor = vi.fn();
  vi.stubGlobal('window', runtimeWindow);
  vi.stubGlobal('WebSocket', socketConstructor);

  await import('@/common/adapter/browser');

  const adapter = platformMock.adapter.mock.calls[0]?.[0] as BridgeAdapter | undefined;
  if (!adapter) throw new Error('browser adapter did not initialize');
  return { adapter, runtimeWindow, socketConstructor };
}

describe('browser local bridge adapter', () => {
  beforeEach(() => {
    vi.spyOn(console, 'log').mockImplementation(() => undefined);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it('does not create a WebSocket or own realtime reconnect state at import time', async () => {
    const { socketConstructor } = await loadBrowserAdapter();

    expect(socketConstructor).not.toHaveBeenCalled();
    expect(platformMock.adapter).toHaveBeenCalledTimes(1);
    expect(platformMock.provider).toHaveBeenCalledTimes(1);
  });

  it('forwards only local bridge callbacks through the registered emitter', async () => {
    const { adapter, runtimeWindow, socketConstructor } = await loadBrowserAdapter();
    const emit = vi.fn();
    const payload = { frame_id: 'frame-1', status: 'running' };

    adapter.on({ emit });
    adapter.emit('frame_update', payload);
    runtimeWindow.__emitBridgeCallback?.('turn.completed', { status: 'completed' });

    expect(emit).toHaveBeenNthCalledWith(1, 'frame_update', payload);
    expect(emit).toHaveBeenNthCalledWith(2, 'turn.completed', { status: 'completed' });
    expect(socketConstructor).not.toHaveBeenCalled();
  });
});

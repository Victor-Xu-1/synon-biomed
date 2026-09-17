import { describe, expect, it, vi } from 'vitest';
import {
  createAnimationFrameCoalescer,
  createMolstarViewportResizeScheduler,
  disposeMolstarUiResources,
  MOLSTAR_THUMBNAIL_PIXEL_SCALE,
  MOLSTAR_VIEWPORT_PIXEL_SCALE,
} from '@/renderer/pages/conversation/Preview/components/viewers/molstarViewportPerformance';

const createFrameHost = () => {
  let nextHandle = 1;
  const callbacks = new Map<number, FrameRequestCallback>();
  return {
    host: {
      requestAnimationFrame: vi.fn((callback: FrameRequestCallback) => {
        const handle = nextHandle++;
        callbacks.set(handle, callback);
        return handle;
      }),
      cancelAnimationFrame: vi.fn((handle: number) => callbacks.delete(handle)),
    },
    flush: () => {
      const pending = [...callbacks.entries()];
      callbacks.clear();
      pending.forEach(([, callback]) => callback(16));
    },
  };
};

describe('molstarViewportPerformance', () => {
  it('disposes Molstar synchronously and defers exactly one nested React root unmount', () => {
    const plugin = { dispose: vi.fn() };
    const reactRoot = { unmount: vi.fn() };
    let deferred: (() => void) | undefined;
    const schedule = vi.fn((operation: () => void) => {
      deferred = operation;
    });

    disposeMolstarUiResources(plugin, reactRoot, schedule);

    expect(plugin.dispose).toHaveBeenCalledOnce();
    expect(schedule).toHaveBeenCalledOnce();
    expect(reactRoot.unmount).not.toHaveBeenCalled();
    deferred?.();
    expect(reactRoot.unmount).toHaveBeenCalledOnce();
  });

  it('keeps high-DPI detail without rendering every device pixel', () => {
    expect(MOLSTAR_VIEWPORT_PIXEL_SCALE).toBe(1);
    expect(MOLSTAR_THUMBNAIL_PIXEL_SCALE).toBe(0.6);
  });

  it('coalesces repeated UI work into one animation frame', () => {
    const { host, flush } = createFrameHost();
    const operation = vi.fn();
    const frame = createAnimationFrameCoalescer(host, operation);

    frame.schedule();
    frame.schedule();
    frame.schedule();
    expect(host.requestAnimationFrame).toHaveBeenCalledOnce();
    expect(operation).not.toHaveBeenCalled();

    flush();
    expect(operation).toHaveBeenCalledOnce();
  });

  it('ignores duplicate resize notifications and applies only the latest size', () => {
    const { host, flush } = createFrameHost();
    const resize = vi.fn();
    const scheduler = createMolstarViewportResizeScheduler(host, resize);

    scheduler.notify(800, 600);
    scheduler.notify(800.2, 600.1);
    scheduler.notify(840, 620);
    expect(host.requestAnimationFrame).toHaveBeenCalledOnce();
    flush();
    expect(resize).toHaveBeenCalledOnce();

    scheduler.notify(840, 620);
    flush();
    expect(resize).toHaveBeenCalledOnce();
  });

  it('cancels pending frame work during viewer disposal', () => {
    const { host } = createFrameHost();
    const operation = vi.fn();
    const frame = createAnimationFrameCoalescer(host, operation);
    frame.schedule();
    frame.cancel();
    expect(host.cancelAnimationFrame).toHaveBeenCalledOnce();
    expect(operation).not.toHaveBeenCalled();
  });
});

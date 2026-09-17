import { describe, expect, it, vi } from 'vitest';
import { installWebChunkRecovery, isWebChunkLoadFailure } from '@/renderer/services/webChunkRecovery';

class MemoryStorage {
  private readonly values = new Map<string, string>();

  getItem(key: string): string | null {
    return this.values.get(key) ?? null;
  }

  setItem(key: string, value: string): void {
    this.values.set(key, value);
  }

  removeItem(key: string): void {
    this.values.delete(key);
  }
}

function preloadError(reason?: unknown): Event {
  const event = new Event('vite:preloadError', { cancelable: true });
  if (reason !== undefined) Object.defineProperty(event, 'payload', { value: reason });
  return event;
}

function rejection(reason: unknown): Event {
  const event = new Event('unhandledrejection', { cancelable: true });
  Object.defineProperty(event, 'reason', { value: reason });
  return event;
}

describe('webChunkRecovery', () => {
  it('recognizes browser and bundler chunk failures without matching ordinary errors', () => {
    expect(isWebChunkLoadFailure(new TypeError('Failed to fetch dynamically imported module: /assets/view.js'))).toBe(
      true
    );
    expect(isWebChunkLoadFailure(new Error('Loading chunk AuthenticatedWorkspace failed'))).toBe(true);
    expect(isWebChunkLoadFailure(new Error('network request failed'))).toBe(false);
  });

  it('reloads the exact route once and suppresses a second recovery loop', () => {
    const target = new EventTarget();
    const storage = new MemoryStorage();
    const reload = vi.fn();
    let timestamp = 1_000;
    let href = 'http://127.0.0.1:8765/#/conversation/frame-1';
    const install = () =>
      installWebChunkRecovery({
        target: target as unknown as Window,
        storage,
        href: () => href,
        reload,
        now: () => timestamp,
        schedule: (callback) => callback(),
        guardMs: 60_000,
      });

    const disposeFirst = install();
    const first = preloadError(
      new TypeError(
        'Expected a JavaScript-or-Wasm module script but the server responded with a MIME type of text/html'
      )
    );
    expect(target.dispatchEvent(first)).toBe(false);
    expect(first.defaultPrevented).toBe(true);
    expect(reload).toHaveBeenCalledTimes(1);
    disposeFirst();

    // Simulate the newly loaded document. The session marker survives, so an
    // immediately repeated missing chunk remains visible instead of reloading,
    // even after navigation to another lazy route.
    timestamp += 100;
    href = 'http://127.0.0.1:8765/#/settings/skills';
    const disposeSecond = install();
    const repeated = preloadError(new TypeError('Failed to fetch dynamically imported module'));
    expect(target.dispatchEvent(repeated)).toBe(true);
    expect(repeated.defaultPrevented).toBe(false);
    expect(reload).toHaveBeenCalledTimes(1);
    disposeSecond();

    // A later deployment can recover after the bounded guard expires.
    timestamp += 60_001;
    const disposeLater = install();
    expect(target.dispatchEvent(preloadError())).toBe(false);
    expect(reload).toHaveBeenCalledTimes(2);
    disposeLater();
  });

  it('recovers rejected dynamic imports but ignores unrelated promise failures', () => {
    const target = new EventTarget();
    const storage = new MemoryStorage();
    const reload = vi.fn();
    const dispose = installWebChunkRecovery({
      target: target as unknown as Window,
      storage,
      href: () => 'http://127.0.0.1:8765/#/guid',
      reload,
      now: () => 5_000,
      schedule: (callback) => callback(),
    });

    expect(target.dispatchEvent(rejection(new Error('ordinary model request failed')))).toBe(true);
    expect(reload).not.toHaveBeenCalled();
    expect(target.dispatchEvent(rejection(new TypeError('Importing a module script failed. /assets/route.js')))).toBe(
      false
    );
    expect(reload).toHaveBeenCalledTimes(1);
    dispose();
  });

  it('does not reload when the loop guard cannot be persisted', () => {
    const target = new EventTarget();
    const reload = vi.fn();
    const storage = {
      getItem: () => null,
      setItem: () => {
        throw new Error('storage unavailable');
      },
      removeItem: () => undefined,
    };
    const dispose = installWebChunkRecovery({
      target: target as unknown as Window,
      storage,
      href: () => 'http://127.0.0.1:8765/#/guid',
      reload,
      schedule: (callback) => callback(),
    });
    const event = preloadError();
    expect(target.dispatchEvent(event)).toBe(true);
    expect(event.defaultPrevented).toBe(false);
    expect(reload).not.toHaveBeenCalled();
    dispose();
  });

  it('does not reload when the loop guard cannot be read reliably', () => {
    const target = new EventTarget();
    const reload = vi.fn();
    const storage = {
      getItem: () => {
        throw new Error('storage unavailable');
      },
      setItem: vi.fn(),
      removeItem: () => undefined,
    };
    const dispose = installWebChunkRecovery({
      target: target as unknown as Window,
      storage,
      href: () => 'http://127.0.0.1:8765/#/guid',
      reload,
      schedule: (callback) => callback(),
    });
    const event = preloadError();
    expect(target.dispatchEvent(event)).toBe(true);
    expect(event.defaultPrevented).toBe(false);
    expect(storage.setItem).not.toHaveBeenCalled();
    expect(reload).not.toHaveBeenCalled();
    dispose();
  });
});

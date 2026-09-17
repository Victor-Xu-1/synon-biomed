import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  RealtimeClient,
  buildRealtimeEndpoint,
  type RealtimeConnectionStatus,
  type RealtimeSocket,
} from '@/common/adapter/realtimeClient';
import { REALTIME_MAX_INBOUND_BYTES, type RealtimeProtocolMessage } from '@/common/adapter/realtimeProtocol';

type Listener = (event: Event | MessageEvent) => void;

class FakeTarget {
  readonly listeners = new Map<string, Set<EventListenerOrEventListenerObject>>();

  addEventListener(type: string, listener: EventListenerOrEventListenerObject) {
    const listeners = this.listeners.get(type) ?? new Set();
    listeners.add(listener);
    this.listeners.set(type, listeners);
  }

  removeEventListener(type: string, listener: EventListenerOrEventListenerObject) {
    this.listeners.get(type)?.delete(listener);
  }

  dispatch(type: string) {
    for (const listener of this.listeners.get(type) ?? []) {
      if (typeof listener === 'function') listener(new Event(type));
      else listener.handleEvent(new Event(type));
    }
  }

  count(): number {
    return [...this.listeners.values()].reduce((count, listeners) => count + listeners.size, 0);
  }
}

class FakeDocumentTarget extends FakeTarget {
  visibilityState = 'hidden';
}

class FakeSocket implements RealtimeSocket {
  readyState = 0;
  readonly sent: string[] = [];
  closed = false;
  onRemove?: () => void;
  onClose?: () => void;
  private readonly listeners = new Map<string, Set<Listener>>();

  addEventListener(type: string, listener: Listener) {
    const listeners = this.listeners.get(type) ?? new Set();
    listeners.add(listener);
    this.listeners.set(type, listeners);
  }

  removeEventListener(type: string, listener: Listener) {
    this.listeners.get(type)?.delete(listener);
    this.onRemove?.();
  }

  send(data: string) {
    this.sent.push(data);
  }

  close() {
    if (this.closed) return;
    this.closed = true;
    this.readyState = 3;
    this.emit('close', new Event('close'));
    this.onClose?.();
  }

  open() {
    this.readyState = 1;
    this.emit('open', new Event('open'));
  }

  message(value: unknown, raw = false) {
    const data = raw ? value : JSON.stringify(value);
    this.emit('message', { data } as MessageEvent);
  }

  listenerCount(): number {
    return [...this.listeners.values()].reduce((count, listeners) => count + listeners.size, 0);
  }

  private emit(type: string, event: Event | MessageEvent) {
    for (const listener of this.listeners.get(type) ?? []) listener(event);
  }
}

class FakeStorage {
  readonly values = new Map<string, string>();
  readonly writes: string[] = [];
  getItem(key: string) {
    return this.values.get(key) ?? null;
  }
  setItem(key: string, value: string) {
    this.writes.push(value);
    this.values.set(key, value);
  }
}

class ManualTimers {
  private nextID = 1;
  readonly callbacks = new Map<number, () => void>();
  readonly active = new Set<number>();
  onSet?: (id: number) => void;
  onClear?: (id: number) => void;
  throwOnSet?: (id: number) => boolean;
  throwOnClear?: (id: number) => boolean;
  readonly setTimer = ((callback: () => void) => {
    const id = this.nextID++;
    this.callbacks.set(id, callback);
    this.active.add(id);
    this.onSet?.(id);
    if (this.throwOnSet?.(id)) {
      this.active.delete(id);
      throw new Error('timer denied');
    }
    return id;
  }) as unknown as typeof setTimeout;
  readonly clearTimer = ((timer: number) => {
    this.active.delete(timer);
    this.onClear?.(timer);
    if (this.throwOnClear?.(timer)) throw new Error('timer cleanup denied');
  }) as unknown as typeof clearTimeout;

  invoke(id: number): void {
    this.active.delete(id);
    this.callbacks.get(id)?.();
  }
}

const ownerDigest = async (owner: string) => (owner === 'owner-a' ? 'a'.repeat(64) : 'b'.repeat(64));

function setup(
  overrides: {
    online?: boolean;
    isOnline?: () => boolean;
    storage?: FakeStorage;
    digestOwner?: (owner: string) => Promise<string | null>;
    createSocket?: (url: string) => RealtimeSocket;
    endpoint?: () => { origin: string; url: string };
    setTimer?: typeof setTimeout;
    clearTimer?: typeof clearTimeout;
    onStatus?: (status: RealtimeConnectionStatus) => void;
    onMessage?: (message: RealtimeProtocolMessage) => void;
    onDiagnostic?: (diagnostic: { code: string }) => void;
  } = {}
) {
  const sockets: Array<{ url: string; socket: FakeSocket }> = [];
  const statuses: RealtimeConnectionStatus[] = [];
  const messages: RealtimeProtocolMessage[] = [];
  const diagnostics: string[] = [];
  const windowTarget = new FakeTarget();
  const documentTarget = new FakeDocumentTarget();
  const online = { value: overrides.online ?? true };
  const createSocket =
    overrides.createSocket ??
    ((url: string) => {
      const socket = new FakeSocket();
      sockets.push({ url, socket });
      return socket;
    });
  const client = new RealtimeClient({
    endpoint: overrides.endpoint ?? (() => ({ origin: 'http://example.test', url: 'ws://example.test/api/events/ws' })),
    createSocket,
    storage: overrides.storage,
    digestOwner: overrides.digestOwner ?? ownerDigest,
    isOnline: overrides.isOnline ?? (() => online.value),
    random: () => 0,
    setTimer: overrides.setTimer,
    clearTimer: overrides.clearTimer,
    windowTarget: windowTarget as unknown as EventTarget,
    documentTarget: documentTarget as unknown as EventTarget & { visibilityState: string },
    onStatus: overrides.onStatus ?? ((status) => statuses.push(status)),
    onMessage: overrides.onMessage ?? ((message) => messages.push(message)),
    onDiagnostic: overrides.onDiagnostic ?? (({ code }) => diagnostics.push(code)),
  });
  return { client, sockets, statuses, messages, diagnostics, windowTarget, documentTarget, online };
}

const connect = (socket: FakeSocket, cursor: number) => {
  socket.open();
  socket.message({ type: 'connected', cursor });
};

const durable = (sequence: number) => ({
  type: 'text_chunk',
  _event_id: `event-${sequence}`,
  _sequence: sequence,
  _kind: 'fanout',
});

const assertObserverFailure = (): never => {
  throw new Error('observer failed');
};

describe('RealtimeClient', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it('builds only same-origin WebUI or loopback Electron event endpoints', () => {
    expect(
      buildRealtimeEndpoint({
        location: { protocol: 'https:', host: 'lab.test:8765', origin: 'https://lab.test:8765' },
      })
    ).toEqual({ origin: 'https://lab.test:8765', url: 'wss://lab.test:8765/api/events/ws' });
    expect(buildRealtimeEndpoint({ backendPort: 8766 })).toEqual({
      origin: 'http://127.0.0.1:8766',
      url: 'ws://127.0.0.1:8766/api/events/ws',
    });
    expect(() => buildRealtimeEndpoint({ backendPort: 0 })).toThrow('invalid realtime backend port');
    expect(() =>
      buildRealtimeEndpoint({ location: { protocol: 'ftp:', host: 'lab.test', origin: 'ftp://lab.test' } })
    ).toThrow('invalid realtime web protocol');
  });

  it('binds one endpoint per identity generation and rejects inconsistent authority', async () => {
    let calls = 0;
    const endpoints = [
      { origin: 'http://a.test', url: 'ws://a.test/api/events/ws' },
      { origin: 'http://b.test', url: 'ws://b.test/api/events/ws' },
    ];
    const bound = setup({ endpoint: () => endpoints[calls++] });
    await bound.client.setIdentity('owner-a');
    expect(calls).toBe(1);
    expect(new URL(bound.sockets[0].url).host).toBe('a.test');
    bound.sockets[0].socket.close();
    vi.advanceTimersByTime(1_000);
    expect(calls).toBe(1);
    expect(new URL(bound.sockets[1].url).host).toBe('a.test');
    bound.client.dispose();

    const invalid = setup({ endpoint: () => ({ origin: 'http://a.test', url: 'ws://b.test/api/events/ws' }) });
    await invalid.client.setIdentity('owner-a');
    expect(invalid.sockets).toHaveLength(0);
    expect(invalid.diagnostics).toEqual(['realtime_invalid_endpoint']);
    expect(invalid.statuses.at(-1)).toBe('protocol-error');
    invalid.client.dispose();
  });

  it('does not connect or reconnect while identity is unknown or unauthenticated', async () => {
    const { client, sockets, statuses } = setup();
    await client.setIdentity(undefined);
    await client.setIdentity(null);
    vi.advanceTimersByTime(60_000);
    expect(sockets).toHaveLength(0);
    expect(statuses).toEqual(['auth-required']);
    client.dispose();
  });

  it('times out a socket that never completes the connected handshake', async () => {
    const { client, sockets } = setup();
    await client.setIdentity('owner-a');
    sockets[0].socket.open();
    vi.advanceTimersByTime(15_000);
    expect(sockets[0].socket.closed).toBe(true);
    vi.advanceTimersByTime(1_000);
    expect(sockets).toHaveLength(2);
    client.dispose();
  });

  it.each([{ type: 'pong' }, { type: 'error', code: 'EARLY' }, { type: 'file_changed', path: '/tmp/a' }, durable(1)])(
    'rejects every pre-handshake message %#',
    async (payload) => {
      const { client, sockets, messages, diagnostics } = setup();
      await client.setIdentity('owner-a');
      sockets[0].socket.open();
      expect(() => sockets[0].socket.message(payload)).not.toThrow();
      expect(sockets[0].socket.closed).toBe(true);
      expect(messages).toHaveLength(0);
      expect(diagnostics).toEqual(['realtime_before_connected']);
      client.dispose();
    }
  );

  it.each([
    ['unknown', { type: 'unknown' }, false, 'realtime_unknown'],
    ['invalid pong', { type: 'pong', timestamp: 1 }, false, 'realtime_invalid'],
    ['malformed', '{', true, 'realtime_malformed'],
    ['oversized', 'x'.repeat(REALTIME_MAX_INBOUND_BYTES + 1), true, 'realtime_oversized'],
  ])('rejects pre-handshake parser failure %s immediately', async (_name, value, raw, diagnostic) => {
    const { client, sockets, messages, diagnostics } = setup();
    await client.setIdentity('owner-a');
    sockets[0].socket.open();
    sockets[0].socket.message(value, raw);
    expect(sockets[0].socket.closed).toBe(true);
    expect(messages).toHaveLength(0);
    expect(diagnostics).toEqual([diagnostic]);
    vi.advanceTimersByTime(1_000);
    expect(sockets).toHaveLength(2);
    client.dispose();
  });
  it('abandons stale connecting flows after dispose or an identity switch', async () => {
    let client: RealtimeClient;
    const disposed = setup({ onStatus: (status) => status === 'connecting' && client.dispose() });
    client = disposed.client;
    await expect(client.setIdentity('owner-a')).resolves.toBeUndefined();
    expect(disposed.sockets).toHaveLength(0);
    expect(client.connectionStatus).toBe('idle');
    expect(vi.getTimerCount()).toBe(0);
    const storage = new FakeStorage();
    const key = `synon.realtime.cursor.v1:${encodeURIComponent('http://example.test')}:scope=global:owner=${'b'.repeat(64)}`;
    storage.values.set(key, '7');
    let switched: Promise<void> | undefined;
    let didSwitch = false;
    const current = setup({
      storage,
      onStatus: (status) => {
        if (status === 'connecting' && !didSwitch) {
          didSwitch = true;
          switched = client.setIdentity('owner-b');
        }
      },
    });
    client = current.client;
    await expect(client.setIdentity('owner-a')).resolves.toBeUndefined();
    await switched;
    expect(current.sockets).toHaveLength(1);
    expect(new URL(current.sockets[0].url).searchParams.get('after_sequence')).toBe('7');
    client.dispose();
  });

  it.each(['status', 'factory'] as const)('keeps one attempt when online reenters from %s', async (order) => {
    const created: FakeSocket[] = [];
    let current: ReturnType<typeof setup>;
    let factoryWake = false;
    current = setup({
      onStatus: (status) => {
        if (order === 'status' && status === 'connecting') current.windowTarget.dispatch('online');
      },
      createSocket: () => {
        const socket = new FakeSocket();
        created.push(socket);
        if (order === 'factory' && !factoryWake) {
          factoryWake = true;
          current.windowTarget.dispatch('online');
        }
        return socket;
      },
    });
    await current.client.setIdentity('owner-a');
    expect(created.filter((socket) => !socket.closed)).toHaveLength(1);
    expect(created[0].listenerCount()).toBe(3);
    expect(vi.getTimerCount()).toBe(1);
    current.client.dispose();
    expect(created.every((socket) => socket.closed && socket.listenerCount() === 0)).toBe(true);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('waits for digest and stored cursor binding before online or visibility can connect', async () => {
    const storage = new FakeStorage();
    const key = `synon.realtime.cursor.v1:${encodeURIComponent('http://example.test')}:scope=global:owner=${'a'.repeat(64)}`;
    storage.values.set(key, '7');
    let current: ReturnType<typeof setup>;
    current = setup({
      storage,
      digestOwner: async (owner) => {
        current.windowTarget.dispatch('online');
        current.documentTarget.visibilityState = 'visible';
        current.documentTarget.dispatch('visibilitychange');
        return ownerDigest(owner);
      },
    });
    await current.client.setIdentity('owner-a');
    expect(current.sockets).toHaveLength(1);
    expect(new URL(current.sockets[0].url).searchParams.get('after_sequence')).toBe('7');
    current.client.dispose();
  });

  it.each(['return', 'throw'] as const)('finalizes storage %s before creating one authority socket', async (mode) => {
    const storage = new FakeStorage();
    let current: ReturnType<typeof setup>;
    vi.spyOn(storage, 'getItem').mockImplementation(() => {
      current.windowTarget.dispatch('online');
      current.documentTarget.visibilityState = 'visible';
      current.documentTarget.dispatch('visibilitychange');
      if (mode === 'throw') throw new Error('storage denied');
      return '7';
    });
    current = setup({ storage });
    await current.client.setIdentity('owner-a');
    expect(current.sockets).toHaveLength(1);
    expect(new URL(current.sockets[0].url).searchParams.get('after_sequence')).toBe(mode === 'return' ? '7' : 'latest');
    connect(current.sockets[0].socket, 8);
    if (mode === 'throw') expect(storage.values.size).toBe(0);
    current.client.dispose();
    expect(vi.getTimerCount()).toBe(0);
  });

  it.each(['invalid', 'throw'] as const)('rejects endpoint %s after reentrant wake without resources', async (mode) => {
    let current: ReturnType<typeof setup>;
    current = setup({
      endpoint: () => {
        current.windowTarget.dispatch('online');
        current.documentTarget.visibilityState = 'visible';
        current.documentTarget.dispatch('visibilitychange');
        if (mode === 'throw') throw new Error('endpoint denied');
        return { origin: 'http://a.test', url: 'ws://b.test/api/events/ws' };
      },
    });
    await current.client.setIdentity('owner-a');
    expect(current.sockets).toHaveLength(0);
    expect(current.client.connectionStatus).toBe('protocol-error');
    expect(vi.getTimerCount()).toBe(0);
    current.client.dispose();
  });

  it.each(['dispose', 'switch'] as const)('discards an initializing candidate after %s', async (action) => {
    const storage = new FakeStorage();
    const key = (digest: string) =>
      `synon.realtime.cursor.v1:${encodeURIComponent('http://example.test')}:scope=global:owner=${digest}`;
    storage.values.set(key('a'.repeat(64)), '7');
    storage.values.set(key('b'.repeat(64)), '3');
    let client: RealtimeClient;
    let switched: Promise<void> | undefined;
    let reentered = false;
    const current = setup({
      storage,
      digestOwner: async (owner) => {
        if (owner === 'owner-a' && !reentered) {
          reentered = true;
          if (action === 'dispose') client.dispose();
          else switched = client.setIdentity('owner-b');
        }
        return ownerDigest(owner);
      },
    });
    client = current.client;
    await client.setIdentity('owner-a');
    await switched;
    expect(current.sockets).toHaveLength(action === 'dispose' ? 0 : 1);
    if (action === 'switch') {
      expect(new URL(current.sockets[0].url).searchParams.get('after_sequence')).toBe('3');
      client.dispose();
    }
    expect(vi.getTimerCount()).toBe(0);
  });

  it('does not let connection cleanup overwrite a reentrant identity removal', async () => {
    const timers = new ManualTimers();
    let client: RealtimeClient;
    let armed = false;
    let removed: Promise<void> | undefined;
    const clearTimer = ((timer: number) => {
      timers.clearTimer(timer as unknown as ReturnType<typeof setTimeout>);
      if (armed && !removed) removed = client.setIdentity(null);
    }) as unknown as typeof clearTimeout;
    const current = setup({ setTimer: timers.setTimer, clearTimer });
    client = current.client;
    await client.setIdentity('owner-a');
    connect(current.sockets[0].socket, 0);
    armed = true;
    current.windowTarget.dispatch('offline');
    await removed;
    expect(client.connectionStatus).toBe('auth-required');
    expect(client as unknown as { bindingPhase: string; binding: unknown; attempt: unknown }).toMatchObject({
      bindingPhase: 'none',
      binding: null,
      attempt: null,
    });
    expect(current.sockets).toHaveLength(1);
    expect(current.sockets[0].socket.listenerCount()).toBe(0);
    expect(timers.active.size).toBe(0);
    client.dispose();
  });

  it.each(['listener', 'timer'] as const)('keeps close cleanup authoritative across %s reentry', async (boundary) => {
    const timers = new ManualTimers();
    const current = setup({ setTimer: timers.setTimer, clearTimer: timers.clearTimer });
    await current.client.setIdentity('owner-a');
    connect(current.sockets[0].socket, 0);
    let reentered = false;
    const wake = () => {
      if (reentered) return;
      reentered = true;
      current.windowTarget.dispatch('online');
    };
    if (boundary === 'listener') current.sockets[0].socket.onRemove = wake;
    else timers.onClear = wake;

    current.sockets[0].socket.close();
    expect(current.sockets).toHaveLength(1);
    expect(current.sockets[0].socket.listenerCount()).toBe(0);
    expect(timers.active.size).toBe(1);
    timers.invoke([...timers.active][0]);
    expect(current.sockets).toHaveLength(2);
    expect(current.sockets[1].socket.listenerCount()).toBe(3);
    current.client.dispose();
    expect(timers.active.size).toBe(0);
    expect(current.sockets.every(({ socket }) => socket.closed && socket.listenerCount() === 0)).toBe(true);
  });

  it.each(['dispose', 'switch'] as const)('ignores socket-close wake during %s cleanup', async (action) => {
    const timers = new ManualTimers();
    const current = setup({ setTimer: timers.setTimer, clearTimer: timers.clearTimer });
    await current.client.setIdentity('owner-a');
    connect(current.sockets[0].socket, 0);
    current.sockets[0].socket.onClose = () => current.windowTarget.dispatch('online');
    if (action === 'dispose') current.client.dispose();
    else await current.client.setIdentity('owner-b');
    expect(current.sockets).toHaveLength(action === 'dispose' ? 1 : 2);
    expect(current.sockets.filter(({ socket }) => !socket.closed)).toHaveLength(action === 'dispose' ? 0 : 1);
    if (action === 'switch') current.client.dispose();
    expect(timers.active.size).toBe(0);
    expect(current.sockets.every(({ socket }) => socket.listenerCount() === 0)).toBe(true);
  });

  it.each(['endpoint', 'digest'] as const)('abandons stale %s provider results', async (boundary) => {
    let client: RealtimeClient;
    let switched: Promise<void> | undefined;
    let entered = false;
    const reenter = () => {
      if (entered) return;
      entered = true;
      switched = client.setIdentity('owner-b');
    };
    const current = setup({
      endpoint: () => {
        if (boundary === 'endpoint') reenter();
        return { origin: 'http://example.test', url: 'ws://example.test/api/events/ws' };
      },
      digestOwner: async (owner) => {
        if (boundary === 'digest' && owner === 'owner-a') reenter();
        return ownerDigest(owner);
      },
    });
    client = current.client;
    await client.setIdentity('owner-a');
    await switched;
    expect(current.sockets).toHaveLength(1);
    client.dispose();
  });
  it('revalidates connected and diagnostic observers before continuing the old frame', async () => {
    let client: RealtimeClient;
    let switched: Promise<void> | undefined;
    const messages: RealtimeProtocolMessage[] = [];
    const connected = setup({
      onStatus: (status) => {
        if (status === 'connected') switched = client.setIdentity('owner-b');
      },
      onMessage: (message) => messages.push(message),
    });
    client = connected.client;
    await client.setIdentity('owner-a');
    connect(connected.sockets[0].socket, 0);
    await switched;
    expect(messages).toHaveLength(0);
    expect(connected.sockets[0].socket.closed).toBe(true);
    expect(vi.getTimerCount()).toBe(1);
    client.dispose();
    const disposed = setup({
      onMessage: (message) => {
        if (message.kind === 'connected') client.dispose();
      },
    });
    client = disposed.client;
    await client.setIdentity('owner-a');
    connect(disposed.sockets[0].socket, 0);
    expect(client.connectionStatus).toBe('idle');
    expect(vi.getTimerCount()).toBe(0);
    const diagnostic = setup({ onDiagnostic: () => client.dispose() });
    client = diagnostic.client;
    await client.setIdentity('owner-a');
    diagnostic.sockets[0].socket.open();
    diagnostic.sockets[0].socket.message({ type: 'pong', extra: true });
    expect(client.connectionStatus).toBe('idle');
    expect(vi.getTimerCount()).toBe(0);
  });
  it.each(['dispose', 'switch'] as const)('closes a stale socket returned after factory %s reentry', async (action) => {
    const created: FakeSocket[] = [];
    let client: RealtimeClient;
    let switched: Promise<void> | undefined;
    const current = setup({
      createSocket: () => {
        const socket = new FakeSocket();
        created.push(socket);
        if (created.length === 1) {
          if (action === 'dispose') client.dispose();
          else switched = client.setIdentity('owner-b');
        }
        return socket;
      },
    });
    client = current.client;
    await expect(client.setIdentity('owner-a')).resolves.toBeUndefined();
    await switched;
    expect(created[0].closed).toBe(true);
    expect(created[0].listenerCount()).toBe(0);
    expect(created).toHaveLength(action === 'dispose' ? 1 : 2);
    expect(vi.getTimerCount()).toBe(action === 'dispose' ? 0 : 1);
    client.dispose();
  });
  it('does not schedule reconnect work after a reconnecting observer disposes', async () => {
    let client: RealtimeClient;
    const current = setup({ onStatus: (status) => status === 'reconnecting' && client.dispose() });
    client = current.client;
    await client.setIdentity('owner-a');
    current.sockets[0].socket.close();
    expect(client.connectionStatus).toBe('idle');
    expect(vi.getTimerCount()).toBe(0);
  });
  it('isolates throwing status and diagnostic observers', async () => {
    const status = setup({ onStatus: () => assertObserverFailure() });
    await expect(status.client.setIdentity('owner-a')).resolves.toBeUndefined();
    expect(status.sockets).toHaveLength(1);
    status.client.dispose();

    const diagnostic = setup({ onDiagnostic: () => assertObserverFailure() });
    await diagnostic.client.setIdentity('owner-a');
    connect(diagnostic.sockets[0].socket, 0);
    expect(() => diagnostic.sockets[0].socket.message({ type: 'unknown' })).not.toThrow();
    diagnostic.client.dispose();
  });

  it('replays a durable event when its observer throws instead of committing its cursor', async () => {
    const storage = new FakeStorage();
    const observed = setup({
      storage,
      onMessage: (message) => {
        if (message.kind === 'durable') assertObserverFailure();
      },
    });
    await observed.client.setIdentity('owner-a');
    connect(observed.sockets[0].socket, 0);
    expect(() => observed.sockets[0].socket.message(durable(5))).not.toThrow();
    expect([...storage.values.values()]).toEqual(['0']);
    expect(observed.sockets[0].socket.closed).toBe(true);
    vi.advanceTimersByTime(1_000);
    expect(new URL(observed.sockets[1].url).searchParams.get('after_sequence')).toBe('0');
    observed.client.dispose();
  });

  it('does not commit an old durable event after observer-driven identity reentry', async () => {
    const storage = new FakeStorage();
    let identitySwitch: Promise<void> | undefined;
    let client: RealtimeClient;
    const observed = setup({
      storage,
      onMessage: (message) => {
        if (message.kind === 'durable') identitySwitch = client.setIdentity('owner-b');
      },
    });
    client = observed.client;
    await client.setIdentity('owner-a');
    connect(observed.sockets[0].socket, 0);
    observed.sockets[0].socket.message(durable(5));
    await identitySwitch;
    expect([...storage.values.values()]).toEqual(['0']);
    expect(new URL(observed.sockets[1].url).searchParams.get('after_sequence')).toBe('latest');
    client.dispose();
  });

  it('keeps cursor monotonic when a durable observer synchronously delivers a newer sequence', async () => {
    const storage = new FakeStorage();
    const sequences: number[] = [];
    let current: ReturnType<typeof setup>;
    current = setup({
      storage,
      onMessage: (message) => {
        if (message.kind !== 'durable') return;
        sequences.push(message.sequence);
        if (message.sequence === 1) current.sockets[0].socket.message(durable(2));
      },
    });
    await current.client.setIdentity('owner-a');
    connect(current.sockets[0].socket, 0);
    current.sockets[0].socket.message(durable(1));
    expect(sequences).toEqual([1, 2]);
    expect(storage.writes).toEqual(['0', '2']);
    current.client.dispose();
  });

  it('dispatches a synchronously nested durable sequence only once while it is in flight', async () => {
    const storage = new FakeStorage();
    let dispatches = 0;
    let current: ReturnType<typeof setup>;
    current = setup({
      storage,
      onMessage: (message) => {
        if (message.kind !== 'durable') return;
        dispatches++;
        if (dispatches === 1) current.sockets[0].socket.message(durable(message.sequence));
      },
    });
    await current.client.setIdentity('owner-a');
    connect(current.sockets[0].socket, 0);
    current.sockets[0].socket.message(durable(1));
    expect(dispatches).toBe(1);
    expect(storage.writes).toEqual(['0', '1']);
    current.client.dispose();
  });

  it.each(['throw', 'switch'] as const)('does not commit nested durable work after inner %s', async (action) => {
    const storage = new FakeStorage();
    let client: RealtimeClient;
    let current: ReturnType<typeof setup>;
    let switched: Promise<void> | undefined;
    current = setup({
      storage,
      onMessage: (message) => {
        if (message.kind !== 'durable') return;
        if (message.sequence === 1) current.sockets[0].socket.message(durable(2));
        else if (action === 'throw') assertObserverFailure();
        else switched = client.setIdentity('owner-b');
      },
    });
    client = current.client;
    await client.setIdentity('owner-a');
    connect(current.sockets[0].socket, 0);
    current.sockets[0].socket.message(durable(1));
    await switched;
    expect(storage.writes).toEqual(['0']);
    if (action === 'throw') expect(current.sockets[0].socket.closed).toBe(true);
    else expect(new URL(current.sockets[1].url).searchParams.get('after_sequence')).toBe('latest');
    client.dispose();
  });

  it('uses latest initially, resumes a validated cursor, and accepts an authoritative future clamp', async () => {
    const storage = new FakeStorage();
    const { client, sockets, diagnostics } = setup({ storage });
    await client.setIdentity('owner-a');
    expect(new URL(sockets[0].url).searchParams.get('after_sequence')).toBe('latest');
    connect(sockets[0].socket, 19);
    expect([...storage.values.values()]).toEqual(['19']);

    sockets[0].socket.close();
    vi.advanceTimersByTime(1_000);
    expect(new URL(sockets[1].url).searchParams.get('after_sequence')).toBe('19');
    sockets[1].socket.message(durable(20));
    expect([...storage.values.values()]).toEqual(['19']);
    expect(sockets[1].socket.closed).toBe(true);
    vi.advanceTimersByTime(2_000);
    expect(new URL(sockets[2].url).searchParams.get('after_sequence')).toBe('19');
    connect(sockets[2].socket, 4);
    expect([...storage.values.values()]).toEqual(['4']);
    sockets[2].socket.message(durable(5));
    expect([...storage.values.values()]).toEqual(['5']);
    sockets[2].socket.message({ type: 'connected', cursor: 2 });
    expect([...storage.values.values()]).toEqual(['5']);
    expect(diagnostics).toEqual(['realtime_before_connected', 'realtime_duplicate_connected']);
    client.dispose();
  });

  it('isolates owner cursors and never puts raw identity into storage keys', async () => {
    const storage = new FakeStorage();
    const { client, sockets } = setup({ storage });
    await client.setIdentity('owner-a');
    connect(sockets[0].socket, 8);
    expect([...storage.values.keys()].join('')).not.toContain('owner-a');

    await client.setIdentity('owner-b');
    expect(sockets[0].socket.closed).toBe(true);
    expect(new URL(sockets[1].url).searchParams.get('after_sequence')).toBe('latest');
    connect(sockets[1].socket, 3);
    await client.setIdentity('owner-a');
    expect(new URL(sockets[2].url).searchParams.get('after_sequence')).toBe('8');
    client.dispose();
  });

  it('falls back to identity-local memory when crypto is unavailable', async () => {
    const storage = new FakeStorage();
    const { client, sockets } = setup({ storage, digestOwner: async () => null });
    await client.setIdentity('owner-a');
    connect(sockets[0].socket, 6);
    expect(storage.values.size).toBe(0);
    sockets[0].socket.close();
    vi.advanceTimersByTime(1_000);
    expect(new URL(sockets[1].url).searchParams.get('after_sequence')).toBe('6');
    await client.setIdentity('owner-b');
    await client.setIdentity('owner-a');
    expect(new URL(sockets[3].url).searchParams.get('after_sequence')).toBe('latest');
    client.dispose();
  });

  it('keeps a memory cursor when browser storage reads or writes fail', async () => {
    for (const failingMethod of ['getItem', 'setItem'] as const) {
      const storage = new FakeStorage();
      vi.spyOn(storage, failingMethod).mockImplementation(() => {
        throw new Error('storage denied');
      });
      const { client, sockets } = setup({ storage });
      await client.setIdentity('owner-a');
      connect(sockets[0].socket, 11);
      sockets[0].socket.close();
      vi.advanceTimersByTime(1_000);
      expect(new URL(sockets[1].url).searchParams.get('after_sequence')).toBe('11');
      client.dispose();
    }
  });

  it.each(['', ' ', '0x10', '1e2', '+1', '01', '-0', '9007199254740992'])(
    'rejects non-canonical stored cursor %j',
    async (raw) => {
      const storage = new FakeStorage();
      const bootstrap = setup({ storage });
      await bootstrap.client.setIdentity('owner-a');
      connect(bootstrap.sockets[0].socket, 7);
      const key = [...storage.values.keys()][0];
      bootstrap.client.dispose();
      storage.values.set(key, raw);

      const resumed = setup({ storage });
      await resumed.client.setIdentity('owner-a');
      expect(new URL(resumed.sockets[0].url).searchParams.get('after_sequence')).toBe('latest');
      resumed.client.dispose();
    }
  );

  it('deduplicates durable gaps while direct messages never advance the cursor', async () => {
    const storage = new FakeStorage();
    const { client, sockets, messages } = setup({ storage });
    await client.setIdentity('owner-a');
    connect(sockets[0].socket, 0);
    sockets[0].socket.message(durable(2));
    sockets[0].socket.message(durable(2));
    sockets[0].socket.message({ type: 'file_changed', path: '/tmp/a' });
    sockets[0].socket.message(durable(9));
    expect(messages.map((message) => message.kind)).toEqual(['connected', 'durable', 'direct', 'durable']);
    expect([...storage.values.values()]).toEqual(['9']);
    client.dispose();
  });

  it('does not let unknown forward-compatible traffic satisfy pong', async () => {
    const { client, sockets, diagnostics, statuses } = setup();
    await client.setIdentity('owner-a');
    connect(sockets[0].socket, 0);
    vi.advanceTimersByTime(25_000);
    expect(sockets[0].socket.sent).toEqual(['{"type":"ping"}']);
    sockets[0].socket.message({ type: 'unknown_direct' });
    vi.advanceTimersByTime(10_000);
    expect(sockets[0].socket.closed).toBe(true);
    expect(diagnostics).toEqual(['realtime_unknown']);
    expect(statuses.at(-1)).toBe('reconnecting');
    client.dispose();
  });

  it.each(['malformed', 'oversized', 'unsafe-sequence', 'invalid-pong'] as const)(
    'closes immediately on connected %s corruption and reconnects from the last committed cursor',
    async (failure) => {
      const storage = new FakeStorage();
      const { client, sockets, diagnostics } = setup({ storage });
      await client.setIdentity('owner-a');
      connect(sockets[0].socket, 4);
      if (failure === 'malformed') sockets[0].socket.message('{', true);
      else if (failure === 'oversized') sockets[0].socket.message('x'.repeat(REALTIME_MAX_INBOUND_BYTES + 1), true);
      else if (failure === 'unsafe-sequence') {
        sockets[0].socket.message({ ...durable(5), _sequence: Number.MAX_SAFE_INTEGER + 1 });
      } else sockets[0].socket.message({ type: 'pong', timestamp: 1 });
      expect(sockets[0].socket.closed).toBe(true);
      expect([...storage.values.values()]).toEqual(['4']);
      expect(diagnostics).toEqual([
        failure === 'malformed'
          ? 'realtime_malformed'
          : failure === 'oversized'
            ? 'realtime_oversized'
            : 'realtime_invalid',
      ]);

      vi.advanceTimersByTime(1_000);
      expect(new URL(sockets[1].url).searchParams.get('after_sequence')).toBe('4');
      client.dispose();
    }
  );

  it('clears the deadline only for exact pong and restarts heartbeat', async () => {
    const { client, sockets, diagnostics } = setup();
    await client.setIdentity('owner-a');
    connect(sockets[0].socket, 0);
    vi.advanceTimersByTime(25_000);
    sockets[0].socket.message({ type: 'pong' });
    expect(diagnostics).toEqual([]);
    vi.advanceTimersByTime(10_000);
    expect(sockets[0].socket.closed).toBe(false);
    vi.advanceTimersByTime(15_000);
    expect(sockets[0].socket.sent).toHaveLength(2);
    client.dispose();
  });

  it('caps exponential reconnect delay and wakes immediately when online or visible', async () => {
    let attempts = 0;
    const created: FakeSocket[] = [];
    const { client, windowTarget, documentTarget } = setup({
      createSocket: () => {
        attempts++;
        if (attempts < 7) throw new Error('offline');
        const socket = new FakeSocket();
        created.push(socket);
        return socket;
      },
    });
    await client.setIdentity('owner-a');
    for (const delay of [1_000, 2_000, 4_000, 8_000, 16_000, 30_000]) vi.advanceTimersByTime(delay);
    expect(attempts).toBe(7);
    connect(created[0], 0);
    documentTarget.visibilityState = 'visible';
    documentTarget.dispatch('visibilitychange');
    expect(created[0].sent).toEqual(['{"type":"ping"}']);
    created[0].close();
    windowTarget.dispatch('online');
    expect(attempts).toBe(8);
    client.dispose();
  });

  it('resets reconnect backoff only after the replacement socket stays connected for four seconds', async () => {
    const { client, sockets } = setup();
    await client.setIdentity('owner-a');

    sockets[0].socket.close();
    vi.advanceTimersByTime(1_000);
    expect(sockets).toHaveLength(2);

    connect(sockets[1].socket, 0);
    vi.advanceTimersByTime(3_999);
    sockets[1].socket.close();
    vi.advanceTimersByTime(1_999);
    expect(sockets).toHaveLength(2);
    vi.advanceTimersByTime(1);
    expect(sockets).toHaveLength(3);

    connect(sockets[2].socket, 0);
    vi.advanceTimersByTime(4_000);
    sockets[2].socket.close();
    vi.advanceTimersByTime(999);
    expect(sockets).toHaveLength(3);
    vi.advanceTimersByTime(1);
    expect(sockets).toHaveLength(4);

    client.dispose();
  });

  it('reports offline and removes socket, wake, and visibility listeners on dispose', async () => {
    const { client, sockets, online, windowTarget, documentTarget, statuses } = setup();
    await client.setIdentity('owner-a');
    connect(sockets[0].socket, 0);
    online.value = false;
    windowTarget.dispatch('offline');
    expect(statuses.at(-1)).toBe('offline');
    expect(sockets[0].socket.listenerCount()).toBe(0);
    online.value = true;
    windowTarget.dispatch('online');
    expect(sockets).toHaveLength(2);
    client.dispose();
    expect(sockets[1].socket.listenerCount()).toBe(0);
    expect(windowTarget.count() + documentTarget.count()).toBe(0);
    vi.advanceTimersByTime(60_000);
    expect(sockets).toHaveLength(2);
  });

  it('attempts loopback realtime even when the browser reports offline', async () => {
    const current = setup({
      online: false,
      endpoint: () => ({ origin: 'http://127.0.0.1:8765', url: 'ws://127.0.0.1:8765/api/events/ws' }),
    });
    await current.client.setIdentity('owner-a');
    expect(current.sockets).toHaveLength(1);
    expect(current.client.connectionStatus).toBe('connecting');
    connect(current.sockets[0].socket, 0);
    current.windowTarget.dispatch('offline');
    expect(current.sockets[0].socket.closed).toBe(false);
    expect(current.client.connectionStatus).toBe('connected');
    current.client.dispose();
  });

  it('continues suppressing remote realtime while the browser reports offline', async () => {
    const current = setup({ online: false });
    await current.client.setIdentity('owner-a');
    expect(current.sockets).toHaveLength(0);
    expect(current.client.connectionStatus).toBe('offline');
    current.client.dispose();
  });

  it('does not ping an open socket before connected authority is established', async () => {
    const { client, sockets, windowTarget, documentTarget } = setup();
    await client.setIdentity('owner-a');
    sockets[0].socket.open();
    windowTarget.dispatch('online');
    documentTarget.visibilityState = 'visible';
    documentTarget.dispatch('visibilitychange');
    expect(sockets[0].socket.sent).toEqual([]);
    expect(vi.getTimerCount()).toBe(1);
    client.dispose();
  });
  it('keeps disposed identity state immutable and allocates no later resources', async () => {
    const endpoint = vi.fn(() => ({ origin: 'http://example.test', url: 'ws://example.test/api/events/ws' }));
    const { client, sockets } = setup({ endpoint });
    await client.setIdentity('owner-a');
    connect(sockets[0].socket, 5);
    client.dispose();
    await client.setIdentity('owner-b');
    expect(client as unknown as { bindingPhase: string; binding: unknown; attempt: unknown }).toMatchObject({
      bindingPhase: 'none',
      binding: null,
      attempt: null,
    });
    expect(endpoint).toHaveBeenCalledTimes(1);
    expect(sockets).toHaveLength(1);
    expect(vi.getTimerCount()).toBe(0);
  });

  it.each(['handshake', 'heartbeat', 'pong', 'reconnect'] as const)(
    'installs one owned %s timer across synchronous provider reentry',
    async (timerKind) => {
      const timers = new ManualTimers();
      let current: ReturnType<typeof setup>;
      let armed = timerKind === 'handshake';
      let reentered = false;
      timers.onSet = () => {
        if (!armed || reentered) return;
        reentered = true;
        if (timerKind === 'handshake') connect(current.sockets[0].socket, 0);
        else current.windowTarget.dispatch('online');
      };
      current = setup({ setTimer: timers.setTimer, clearTimer: timers.clearTimer });
      await current.client.setIdentity('owner-a');

      if (timerKind === 'heartbeat') {
        armed = true;
        connect(current.sockets[0].socket, 0);
      } else if (timerKind === 'pong') {
        connect(current.sockets[0].socket, 0);
        armed = true;
        timers.invoke([...timers.active][0]);
      } else if (timerKind === 'reconnect') {
        connect(current.sockets[0].socket, 0);
        armed = true;
        current.sockets[0].socket.close();
      }

      expect(timers.active.size).toBe(1);
      expect(current.sockets.filter(({ socket }) => !socket.closed)).toHaveLength(1);
      if (timerKind === 'handshake') {
        timers.invoke([...timers.active][0]);
        expect(current.sockets[0].socket.closed).toBe(false);
      }
      if (timerKind === 'heartbeat' || timerKind === 'pong') {
        expect(current.sockets[0].socket.sent).toEqual(['{"type":"ping"}']);
      }
      current.client.dispose();
      expect(timers.active.size).toBe(0);
      expect(current.sockets.every(({ socket }) => socket.listenerCount() === 0)).toBe(true);
    }
  );

  it.each(['dispose', 'switch'] as const)('clears a timer returned after provider %s reentry', async (action) => {
    const timers = new ManualTimers();
    let client: RealtimeClient;
    let switched: Promise<void> | undefined;
    let reentered = false;
    timers.onSet = () => {
      if (reentered) return;
      reentered = true;
      if (action === 'dispose') client.dispose();
      else switched = client.setIdentity('owner-b');
    };
    const current = setup({ setTimer: timers.setTimer, clearTimer: timers.clearTimer });
    client = current.client;
    await client.setIdentity('owner-a');
    await switched;
    expect(timers.active.size).toBe(action === 'dispose' ? 0 : 1);
    expect(current.sockets.filter(({ socket }) => !socket.closed)).toHaveLength(action === 'dispose' ? 0 : 1);
    if (action === 'switch') client.dispose();
    expect(timers.active.size).toBe(0);
    expect(current.sockets.every(({ socket }) => socket.listenerCount() === 0)).toBe(true);
  });

  it.each([
    ['handshake', 1],
    ['heartbeat', 2],
    ['pong', 3],
    ['reconnect', 3],
  ] as const)('converges after %s timer provider failure', async (timerKind, throwAt) => {
    const timers = new ManualTimers();
    timers.throwOnSet = (id) => id === throwAt;
    const current = setup({ setTimer: timers.setTimer, clearTimer: timers.clearTimer });
    await expect(current.client.setIdentity('owner-a')).resolves.toBeUndefined();
    if (timerKind === 'heartbeat') connect(current.sockets[0].socket, 0);
    else if (timerKind === 'pong') {
      connect(current.sockets[0].socket, 0);
      timers.invoke([...timers.active][0]);
    } else if (timerKind === 'reconnect') {
      connect(current.sockets[0].socket, 0);
      current.sockets[0].socket.close();
    }

    expect(current.diagnostics).toContain('realtime_timer_install_failed');
    expect(current.sockets[0].socket.closed).toBe(true);
    expect(current.client.connectionStatus).toBe(timerKind === 'reconnect' ? 'protocol-error' : 'reconnecting');
    expect(timers.active.size).toBe(timerKind === 'reconnect' ? 0 : 1);
    current.client.dispose();
    expect(timers.active.size).toBe(0);
    expect(current.sockets.every(({ socket }) => socket.listenerCount() === 0)).toBe(true);
  });

  it('bounds a failed compensation timer and recovers on a later wake', async () => {
    const timers = new ManualTimers();
    timers.throwOnSet = (id) => id <= 2;
    const current = setup({ setTimer: timers.setTimer, clearTimer: timers.clearTimer });
    await expect(current.client.setIdentity('owner-a')).resolves.toBeUndefined();
    expect(current.diagnostics.filter((code) => code === 'realtime_timer_install_failed')).toHaveLength(2);
    expect(current.client.connectionStatus).toBe('protocol-error');
    expect(timers.active.size).toBe(0);
    timers.throwOnSet = undefined;
    current.windowTarget.dispatch('online');
    expect(current.sockets.filter(({ socket }) => !socket.closed)).toHaveLength(1);
    expect(timers.active.size).toBe(1);
    current.client.dispose();
  });

  it.each(['dispose', 'switch'] as const)('ignores a stale timer throw after provider %s', async (action) => {
    const timers = new ManualTimers();
    timers.throwOnSet = (id) => id === 1;
    let client: RealtimeClient;
    let switched: Promise<void> | undefined;
    timers.onSet = (id) => {
      if (id !== 1) return;
      if (action === 'dispose') client.dispose();
      else switched = client.setIdentity('owner-b');
    };
    const current = setup({ setTimer: timers.setTimer, clearTimer: timers.clearTimer });
    client = current.client;
    await expect(client.setIdentity('owner-a')).resolves.toBeUndefined();
    await switched;
    expect(current.diagnostics).not.toContain('realtime_timer_install_failed');
    expect(current.sockets.filter(({ socket }) => !socket.closed)).toHaveLength(action === 'dispose' ? 0 : 1);
    if (action === 'switch') client.dispose();
    expect(timers.active.size).toBe(0);
  });

  it('fails closed when online state throws and makes cleared timer callbacks inert', async () => {
    const timers = new ManualTimers();
    const onlineFailure = setup({
      setTimer: timers.setTimer,
      clearTimer: timers.clearTimer,
      isOnline: () => {
        throw new Error('online denied');
      },
    });
    await expect(onlineFailure.client.setIdentity('owner-a')).resolves.toBeUndefined();
    expect(onlineFailure.client.connectionStatus).toBe('protocol-error');
    expect(onlineFailure.diagnostics).toContain('realtime_online_check_failed');
    expect(onlineFailure.sockets).toHaveLength(0);

    const cleanup = setup({ setTimer: timers.setTimer, clearTimer: timers.clearTimer });
    await cleanup.client.setIdentity('owner-a');
    connect(cleanup.sockets[0].socket, 0);
    const stale = [...timers.active][0];
    timers.throwOnClear = () => true;
    cleanup.client.dispose();
    expect(cleanup.diagnostics).toContain('realtime_timer_cleanup_failed');
    timers.invoke(stale);
    expect(cleanup.client.connectionStatus).toBe('idle');
    expect(cleanup.sockets[0].socket.listenerCount()).toBe(0);
  });

  it.each(['handshake', 'reconnect', 'heartbeat', 'pong'] as const)(
    'does not let a stale %s callback mutate current timer ownership',
    async (timerKind) => {
      const timers = new ManualTimers();
      const current = setup({ setTimer: timers.setTimer, clearTimer: timers.clearTimer });
      await current.client.setIdentity('owner-a');

      if (timerKind === 'handshake') {
        const stale = [...timers.active][0];
        await current.client.setIdentity('owner-b');
        timers.invoke(stale);
      } else if (timerKind === 'reconnect') {
        connect(current.sockets[0].socket, 0);
        current.sockets[0].socket.close();
        const stale = [...timers.active][0];
        await current.client.setIdentity('owner-b');
        connect(current.sockets[1].socket, 0);
        current.sockets[1].socket.close();
        timers.invoke(stale);
      } else if (timerKind === 'heartbeat') {
        connect(current.sockets[0].socket, 0);
        const stale = [...timers.active][0];
        await current.client.setIdentity('owner-b');
        connect(current.sockets[1].socket, 0);
        timers.invoke(stale);
        expect(current.sockets[1].socket.sent).toEqual([]);
      } else {
        connect(current.sockets[0].socket, 0);
        timers.invoke([...timers.active][0]);
        const stale = [...timers.active][0];
        await current.client.setIdentity('owner-b');
        connect(current.sockets[1].socket, 0);
        timers.invoke([...timers.active][0]);
        timers.invoke(stale);
        expect(current.sockets[1].socket.closed).toBe(false);
      }

      expect(current.sockets.filter(({ socket }) => !socket.closed)).toHaveLength(timerKind === 'reconnect' ? 0 : 1);
      current.client.dispose();
      expect(timers.active.size).toBe(0);
      expect(current.sockets.every(({ socket }) => socket.listenerCount() === 0)).toBe(true);
    }
  );

  it('clears timer handle zero for connected, dispose, and reconnect paths', async () => {
    const timers = new Map<number, () => void>();
    let nextTimer = 0;
    const setTimer = ((callback: () => void) => {
      const id = nextTimer++;
      timers.set(id, () => {
        timers.delete(id);
        callback();
      });
      return id;
    }) as unknown as typeof setTimeout;
    const clearTimer = ((id: number) => timers.delete(id)) as unknown as typeof clearTimeout;

    const connected = setup({ setTimer, clearTimer });
    await connected.client.setIdentity('owner-a');
    connect(connected.sockets[0].socket, 0);
    nextTimer = 0;
    timers.get(1)?.();
    expect(timers.has(0)).toBe(true);
    connected.sockets[0].socket.message({ type: 'pong' });
    expect(timers.has(0)).toBe(false);
    connected.client.dispose();
    expect(timers.size).toBe(0);

    nextTimer = 0;
    const reconnect = setup({
      setTimer,
      clearTimer,
      createSocket: () => {
        throw new Error('connect failed');
      },
    });
    await reconnect.client.setIdentity('owner-a');
    reconnect.client.dispose();
    expect(timers.size).toBe(0);
  });
});

import type { RealtimeConnectionStatus } from './realtimeClient';
import type { RealtimeProtocolMessage } from './realtimeProtocol';

export type RealtimeBusinessMessage = Extract<RealtimeProtocolMessage, { kind: 'durable' | 'direct' | 'control' }>;
export type RealtimeIdentityState = 'checking' | 'unauthenticated' | 'authenticated';
export type RealtimeRuntimeSnapshot = Readonly<{ identity: RealtimeIdentityState; status: RealtimeConnectionStatus }>;
export type RealtimeRuntimeClientObservers = {
  onStatus: (status: RealtimeConnectionStatus) => void;
  onMessage: (message: RealtimeProtocolMessage) => void;
};
export type RealtimeRuntimeClient = Pick<RealtimeRuntime, 'setIdentity' | 'dispose'>;
export type RealtimeRuntimeOptions = {
  createClient: (observers: RealtimeRuntimeClientObservers) => RealtimeRuntimeClient;
};
export type RealtimeRuntime = {
  getSnapshot: () => RealtimeRuntimeSnapshot;
  subscribeStatus: (listener: () => void) => () => void;
  subscribe: (
    kind: RealtimeBusinessMessage['kind'],
    type: string,
    listener: (message: RealtimeBusinessMessage) => void
  ) => () => void;
  subscribeReconnected: (listener: () => void) => () => void;
  setIdentity: (owner?: string | null) => Promise<void>;
  dispose: () => void;
};

const INITIAL_SNAPSHOT: RealtimeRuntimeSnapshot = Object.freeze({ identity: 'checking', status: 'idle' });
const NO_PENDING_IDENTITY = Symbol('no-pending-identity');
type PendingIdentity = string | null | undefined | typeof NO_PENDING_IDENTITY;

class Runtime implements RealtimeRuntime {
  private snapshot = INITIAL_SNAPSHOT;
  private client: RealtimeRuntimeClient | null = null;
  private creatingClient = false;
  private pendingIdentity: PendingIdentity = NO_PENDING_IDENTITY;
  private disposed = false;
  private dispatchEpoch = 0;
  private validatedConnected = false;
  private leftConnected = false;
  private readonly statusListeners = new Set<() => void>();
  private readonly eventListeners = new Map<
    RealtimeBusinessMessage['kind'],
    Map<string, Set<(message: RealtimeBusinessMessage) => void>>
  >();
  private readonly reconnectListeners = new Set<() => void>();

  constructor(private readonly options: RealtimeRuntimeOptions) {}

  getSnapshot = (): RealtimeRuntimeSnapshot => this.snapshot;

  subscribeStatus = (listener: () => void): (() => void) => this.addListener(this.statusListeners, listener);

  subscribe = (
    kind: RealtimeBusinessMessage['kind'],
    type: string,
    listener: (message: RealtimeBusinessMessage) => void
  ): (() => void) => {
    if (this.disposed || !type.trim()) return () => {};
    const byType = this.eventListeners.get(kind) ?? new Map();
    const listeners = byType.get(type) ?? new Set();
    byType.set(type, listeners);
    this.eventListeners.set(kind, byType);
    return this.addListener(listeners, listener, () => {
      if (listeners.size === 0 && byType.get(type) === listeners) byType.delete(type);
      if (byType.size === 0 && this.eventListeners.get(kind) === byType) this.eventListeners.delete(kind);
    });
  };

  subscribeReconnected = (listener: () => void): (() => void) => this.addListener(this.reconnectListeners, listener);

  async setIdentity(owner?: string | null): Promise<void> {
    const epoch = ++this.dispatchEpoch;
    if (this.disposed) return;
    const identity = typeof owner === 'string' ? owner.trim() : owner;
    const forwarded = identity === '' ? null : identity;
    const state: RealtimeIdentityState =
      forwarded === undefined ? 'checking' : forwarded === null ? 'unauthenticated' : 'authenticated';
    this.resetConnectionHistory();
    this.updateSnapshot(state, state === 'unauthenticated' ? 'auth-required' : 'idle');
    if (epoch !== this.dispatchEpoch) return;

    if (this.creatingClient) {
      this.pendingIdentity = forwarded;
      return;
    }
    if (!this.client) {
      this.pendingIdentity = NO_PENDING_IDENTITY;
      this.creatingClient = true;
      let candidate: RealtimeRuntimeClient;
      try {
        candidate = this.options.createClient({ onStatus: this.handleStatus, onMessage: this.handleMessage });
      } finally {
        this.creatingClient = false;
      }
      if (this.disposed) {
        candidate.dispose();
        return;
      }
      this.client = candidate;
      if (this.pendingIdentity !== NO_PENDING_IDENTITY) {
        const pending = this.pendingIdentity;
        this.pendingIdentity = NO_PENDING_IDENTITY;
        await candidate.setIdentity(pending);
        return;
      }
    }
    await this.client.setIdentity(forwarded);
  }

  dispose = (): void => {
    this.dispatchEpoch++;
    if (this.disposed) return;
    this.disposed = true;
    this.pendingIdentity = NO_PENDING_IDENTITY;
    this.resetConnectionHistory();
    this.updateSnapshot('checking', 'idle');
    this.statusListeners.clear();
    for (const byType of this.eventListeners.values()) {
      for (const listeners of byType.values()) listeners.clear();
    }
    this.eventListeners.clear();
    this.reconnectListeners.clear();
    this.client?.dispose();
    this.client = null;
  };

  private readonly handleStatus = (status: RealtimeConnectionStatus): void => {
    if (this.disposed) return;
    if (this.validatedConnected && status !== 'connected') this.leftConnected = true;
    this.updateSnapshot(this.snapshot.identity, status);
  };

  private readonly handleMessage = (message: RealtimeProtocolMessage): void => {
    if (this.disposed) return;
    if (message.kind === 'connected') {
      if (this.snapshot.status !== 'connected') return;
      if (!this.validatedConnected) this.validatedConnected = true;
      else if (this.leftConnected) {
        this.leftConnected = false;
        this.notify(this.reconnectListeners, (listener) => listener());
      }
      return;
    }
    if (message.kind === 'pong') return;
    const listeners = this.eventListeners.get(message.kind)?.get(message.type);
    if (listeners) this.notify(listeners, (listener) => listener(message));
  };

  private updateSnapshot(identity: RealtimeIdentityState, status: RealtimeConnectionStatus): void {
    if (this.snapshot.identity === identity && this.snapshot.status === status) return;
    this.snapshot = Object.freeze({ identity, status });
    this.notify(this.statusListeners, (listener) => listener());
  }

  private resetConnectionHistory(): void {
    this.validatedConnected = this.leftConnected = false;
  }

  private addListener<T>(listeners: Set<T>, listener: T, afterRemove?: () => void): () => void {
    if (this.disposed) return () => {};
    listeners.add(listener);
    let active = true;
    return () => {
      if (!active) return;
      active = false;
      listeners.delete(listener);
      afterRemove?.();
    };
  }

  private notify<T>(listeners: Set<T>, invoke: (listener: T) => void): void {
    const epoch = this.dispatchEpoch;
    for (const listener of new Set(listeners)) {
      if (epoch !== this.dispatchEpoch) return;
      if (!listeners.has(listener)) continue;
      try {
        invoke(listener);
      } catch {
        // Consumer failures cannot participate in transport state transitions.
      }
      if (epoch !== this.dispatchEpoch) return;
    }
  }
}

export const createRealtimeRuntime = (options: RealtimeRuntimeOptions): RealtimeRuntime => new Runtime(options);

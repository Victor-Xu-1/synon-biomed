import { parseRealtimeMessage, type RealtimeProtocolMessage } from './realtimeProtocol';
export type RealtimeConnectionStatus =
  | 'idle'
  | 'connecting'
  | 'connected'
  | 'reconnecting'
  | 'offline'
  | 'auth-required'
  | 'protocol-error';
export type RealtimeEndpoint = { url: string; origin: string };
export type RealtimeDiagnostic = { code: string };
type SocketListener = (event: Event | MessageEvent) => void;
export type RealtimeSocket = {
  readonly readyState: number;
  addEventListener: (type: string, listener: SocketListener) => void;
  removeEventListener: (type: string, listener: SocketListener) => void;
  send: (data: string) => void;
  close: () => void;
};
type StorageLike = Pick<Storage, 'getItem' | 'setItem'>;
type Timer = ReturnType<typeof setTimeout>;
type BindingPhase = 'none' | 'initializing' | 'ready';
type AttemptPhase = 'connecting' | 'handshaking' | 'connected' | 'reconnect-wait' | 'retiring';
type AttemptTimerLease = { handle: Timer | null };
type TimerInstallResult = 'installed' | 'handled';
type IdentityBinding = {
  readonly generation: number;
  readonly owner: string;
  readonly endpoint: RealtimeEndpoint;
  storageKey: string | null;
  cursor: number | null;
  memoryOnly: boolean;
  reconnectAttempt: number;
};
type ConnectionAttempt = {
  readonly token: number;
  readonly binding: IdentityBinding;
  phase: AttemptPhase;
  socket: RealtimeSocket | null;
  detachSocket: (() => void) | null;
  reconnectTimer: AttemptTimerLease | null;
  handshakeTimer: AttemptTimerLease | null;
  heartbeatTimer: AttemptTimerLease | null;
  pongDeadline: AttemptTimerLease | null;
  stabilityTimer: AttemptTimerLease | null;
  inFlightDurable: Set<number>;
};
type AttemptTimerKey = 'reconnectTimer' | 'handshakeTimer' | 'heartbeatTimer' | 'pongDeadline' | 'stabilityTimer';
export type RealtimeClientOptions = {
  endpoint: () => RealtimeEndpoint;
  createSocket?: (url: string) => RealtimeSocket;
  storage?: StorageLike;
  digestOwner?: (owner: string) => Promise<string | null>;
  isOnline?: () => boolean;
  random?: () => number;
  setTimer?: typeof setTimeout;
  clearTimer?: typeof clearTimeout;
  windowTarget?: EventTarget;
  documentTarget?: EventTarget & { visibilityState?: string };
  onStatus?: (status: RealtimeConnectionStatus) => void;
  onMessage?: (message: RealtimeProtocolMessage) => void;
  onDiagnostic?: (diagnostic: RealtimeDiagnostic) => void;
};
const CURSOR_NAMESPACE = 'synon.realtime.cursor.v1';
const OPEN = 1;
const HANDSHAKE_DEADLINE_MS = 15_000;
const HEARTBEAT_MS = 25_000;
const PONG_DEADLINE_MS = 10_000;
const RECONNECT_STABLE_MS = 4_000;
const isLoopbackEndpoint = (endpoint: RealtimeEndpoint): boolean => {
  try {
    const hostname = new URL(endpoint.origin).hostname.toLowerCase();
    return hostname === 'localhost' || hostname === '127.0.0.1' || hostname === '[::1]' || hostname === '::1';
  } catch {
    return false;
  }
};
const defaultDigestOwner = async (owner: string): Promise<string | null> => {
  try {
    if (!globalThis.crypto?.subtle) return null;
    const digest = await globalThis.crypto.subtle.digest('SHA-256', new TextEncoder().encode(owner));
    return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, '0')).join('');
  } catch {
    return null;
  }
};

const normalizeEndpoint = (endpoint: RealtimeEndpoint): RealtimeEndpoint | null => {
  try {
    const origin = new URL(endpoint.origin);
    const socket = new URL(endpoint.url);
    const socketProtocol = origin.protocol === 'https:' ? 'wss:' : origin.protocol === 'http:' ? 'ws:' : null;
    if (
      !socketProtocol ||
      origin.username ||
      origin.password ||
      origin.pathname !== '/' ||
      origin.search ||
      origin.hash ||
      socket.protocol !== socketProtocol ||
      socket.host !== origin.host ||
      socket.username ||
      socket.password ||
      socket.pathname !== '/api/events/ws' ||
      socket.search ||
      socket.hash
    ) {
      return null;
    }
    return { origin: origin.origin, url: `${socketProtocol}//${socket.host}/api/events/ws` };
  } catch {
    return null;
  }
};

export function buildRealtimeEndpoint(
  input: {
    backendPort?: number;
    location?: Pick<Location, 'protocol' | 'host' | 'origin'>;
  } = {}
): RealtimeEndpoint {
  if (input.backendPort !== undefined) {
    if (!Number.isSafeInteger(input.backendPort) || input.backendPort <= 0 || input.backendPort > 65_535) {
      throw new Error('invalid realtime backend port');
    }
    const origin = `http://127.0.0.1:${input.backendPort}`;
    return { origin, url: `ws://127.0.0.1:${input.backendPort}/api/events/ws` };
  }
  const location = input.location ?? window.location;
  if (location.protocol !== 'http:' && location.protocol !== 'https:') {
    throw new Error('invalid realtime web protocol');
  }
  const scheme = location.protocol === 'https:' ? 'wss:' : 'ws:';
  return { origin: location.origin, url: `${scheme}//${location.host}/api/events/ws` };
}

export class RealtimeClient {
  private readonly options: Required<
    Pick<
      RealtimeClientOptions,
      'endpoint' | 'createSocket' | 'digestOwner' | 'isOnline' | 'random' | 'setTimer' | 'clearTimer'
    >
  > &
    RealtimeClientOptions;
  private status: RealtimeConnectionStatus = 'idle';
  private bindingPhase: BindingPhase = 'none';
  private binding: IdentityBinding | null = null;
  private attempt: ConnectionAttempt | null = null;
  private nextAttemptToken = 0;
  private generation = 0;
  private disposed = false;

  constructor(options: RealtimeClientOptions) {
    this.options = {
      ...options,
      createSocket: options.createSocket ?? ((url) => new WebSocket(url) as unknown as RealtimeSocket),
      digestOwner: options.digestOwner ?? defaultDigestOwner,
      isOnline: options.isOnline ?? (() => typeof navigator === 'undefined' || navigator.onLine),
      random: options.random ?? Math.random,
      setTimer: options.setTimer ?? setTimeout,
      clearTimer: options.clearTimer ?? clearTimeout,
    };
    options.windowTarget?.addEventListener('online', this.handleWake);
    options.windowTarget?.addEventListener('offline', this.handleOffline);
    options.documentTarget?.addEventListener('visibilitychange', this.handleVisibility);
  }

  get connectionStatus(): RealtimeConnectionStatus {
    return this.status;
  }

  async setIdentity(owner?: string | null): Promise<void> {
    if (this.disposed) return;
    const generation = ++this.generation;
    this.stopConnection();
    if (this.disposed || generation !== this.generation) return;
    this.binding = null;
    this.bindingPhase = 'initializing';
    const identity = owner?.trim() || undefined;
    if (owner !== undefined && !identity) {
      this.bindingPhase = 'none';
      return this.setStatus('auth-required');
    }
    if (!identity) {
      this.bindingPhase = 'none';
      return this.setStatus('idle');
    }
    this.setStatus('idle');
    if (!this.isInitializing(generation)) return;

    let endpoint: RealtimeEndpoint | null = null;
    try {
      endpoint = normalizeEndpoint(this.options.endpoint());
    } catch {
      // Endpoint providers are an untrusted host boundary.
    }
    if (!this.isInitializing(generation)) return;
    if (!endpoint) {
      this.bindingPhase = 'none';
      this.safeDiagnostic('realtime_invalid_endpoint');
      if (generation !== this.generation || this.disposed) return;
      this.setStatus('protocol-error');
      return;
    }
    let digest: string | null = null;
    try {
      digest = await this.options.digestOwner(identity);
    } catch {
      // Digest failures are an identity-local memory-only fallback.
    }
    if (!this.isInitializing(generation)) return;
    let storageKey: string | null = null;
    let cursor: number | null = null;
    let memoryOnly = true;
    if (digest && /^[a-f0-9]{64}$/i.test(digest)) {
      const candidateKey = `${CURSOR_NAMESPACE}:${encodeURIComponent(endpoint.origin)}:scope=global:owner=${digest}`;
      ({ storageKey, cursor, memoryOnly } = this.loadCursorCandidate(candidateKey));
    }
    if (!this.isInitializing(generation)) return;
    const binding: IdentityBinding = {
      generation,
      owner: identity,
      endpoint,
      storageKey,
      cursor,
      memoryOnly,
      reconnectAttempt: 0,
    };
    this.binding = binding;
    this.bindingPhase = 'ready';
    this.connect(binding);
  }

  dispose(): void {
    if (this.disposed) return;
    this.disposed = true;
    this.generation++;
    this.bindingPhase = 'none';
    this.binding = null;
    this.stopConnection();
    this.options.windowTarget?.removeEventListener('online', this.handleWake);
    this.options.windowTarget?.removeEventListener('offline', this.handleOffline);
    this.options.documentTarget?.removeEventListener('visibilitychange', this.handleVisibility);
    this.setStatus('idle');
  }

  private isInitializing(generation: number): boolean {
    return !this.disposed && this.bindingPhase === 'initializing' && generation === this.generation;
  }

  private isBindingCurrent(binding: IdentityBinding): boolean {
    return (
      !this.disposed &&
      this.bindingPhase === 'ready' &&
      this.binding === binding &&
      binding.generation === this.generation
    );
  }

  private isAttemptCurrent(attempt: ConnectionAttempt, socket?: RealtimeSocket): boolean {
    return (
      this.isBindingCurrent(attempt.binding) &&
      this.attempt === attempt &&
      this.attempt.token === attempt.token &&
      (socket === undefined || attempt.socket === socket)
    );
  }

  private installAttempt(binding: IdentityBinding, phase: AttemptPhase): ConnectionAttempt | null {
    if (!this.isBindingCurrent(binding) || this.attempt !== null) return null;
    const attempt: ConnectionAttempt = {
      token: ++this.nextAttemptToken,
      binding,
      phase,
      socket: null,
      detachSocket: null,
      reconnectTimer: null,
      handshakeTimer: null,
      heartbeatTimer: null,
      pongDeadline: null,
      stabilityTimer: null,
      inFlightDurable: new Set(),
    };
    this.attempt = attempt;
    return attempt;
  }

  private connect(binding: IdentityBinding): void {
    if (!this.isBindingCurrent(binding)) return;
    if (this.attempt && this.isAttemptCurrent(this.attempt)) {
      if (this.attempt.phase !== 'reconnect-wait') return;
      if (!this.retireAttempt(this.attempt) || !this.isBindingCurrent(binding) || this.attempt) return;
    }
    const attempt = this.installAttempt(binding, 'connecting');
    if (!attempt) return;
    const online = this.readOnline();
    if (!this.isAttemptCurrent(attempt)) return;
    if (online === null) return this.handleOnlineCheckFailure(attempt);
    if (!online && !isLoopbackEndpoint(binding.endpoint)) {
      const retired = this.retireAttempt(attempt);
      if (retired && this.isBindingCurrent(binding) && !this.attempt) this.setStatus('offline');
      return;
    }
    this.setStatus(binding.reconnectAttempt > 0 ? 'reconnecting' : 'connecting');
    if (!this.isAttemptCurrent(attempt)) return;
    const url = new URL(binding.endpoint.url);
    url.searchParams.set('after_sequence', binding.cursor === null ? 'latest' : String(binding.cursor));

    let socket: RealtimeSocket;
    try {
      socket = this.options.createSocket(url.toString());
    } catch {
      if (this.isAttemptCurrent(attempt)) {
        const retired = this.retireAttempt(attempt, false);
        if (retired && this.isBindingCurrent(binding) && !this.attempt) this.scheduleReconnect(binding);
      }
      return;
    }
    if (!this.isAttemptCurrent(attempt)) {
      socket.close();
      return;
    }
    attempt.socket = socket;
    attempt.phase = 'handshaking';
    const onMessage: SocketListener = (event) => this.handleMessage(attempt, (event as MessageEvent).data);
    const onClose: SocketListener = () => this.handleClose(attempt);
    const onError: SocketListener = () => socket.close();
    attempt.detachSocket = () => {
      for (const [type, listener] of [
        ['message', onMessage],
        ['close', onClose],
        ['error', onError],
      ] as const) {
        try {
          socket.removeEventListener(type, listener);
        } catch {
          this.safeDiagnostic('realtime_listener_cleanup_failed');
        }
      }
    };
    for (const [type, listener] of [
      ['message', onMessage],
      ['close', onClose],
      ['error', onError],
    ] as const) {
      socket.addEventListener(type, listener);
      if (!this.isAttemptCurrent(attempt, socket)) {
        this.retireAttempt(attempt);
        return;
      }
    }
    if (
      this.installAttemptTimer(
        attempt,
        'handshakeTimer',
        HANDSHAKE_DEADLINE_MS,
        () => attempt.phase === 'handshaking' && attempt.socket === socket,
        () => {
          this.safeDiagnostic('realtime_handshake_timeout');
          if (this.isAttemptCurrent(attempt, socket)) socket.close();
        }
      ) === 'handled'
    )
      return;
  }

  private handleMessage(attempt: ConnectionAttempt, raw: unknown): void {
    const socket = attempt.socket;
    if (!socket || !this.isAttemptCurrent(attempt, socket)) return;
    const parsed = parseRealtimeMessage(raw);
    if (parsed.ok === false) {
      this.safeDiagnostic(`realtime_${parsed.reason}`);
      if (!this.isAttemptCurrent(attempt, socket)) return;
      if (attempt.phase !== 'connected') {
        if (parsed.reason !== 'unknown') this.setStatus('protocol-error');
        if (this.isAttemptCurrent(attempt, socket)) socket.close();
        return;
      }
      if (parsed.reason !== 'unknown') {
        this.setStatus('protocol-error');
        if (this.isAttemptCurrent(attempt, socket)) socket.close();
      }
      return;
    }
    const message = parsed.message;
    if (message.kind === 'connected') {
      if (attempt.phase === 'connected') {
        this.safeDiagnostic('realtime_duplicate_connected');
        if (!this.isAttemptCurrent(attempt, socket)) return;
        this.setStatus('protocol-error');
        if (this.isAttemptCurrent(attempt, socket)) socket.close();
        return;
      }
      this.clearAttemptTimer(attempt, 'handshakeTimer');
      if (!this.isAttemptCurrent(attempt, socket)) return;
      attempt.phase = 'connected';
      attempt.binding.cursor = message.cursor;
      this.persistCursor(attempt.binding);
      if (!this.isAttemptCurrent(attempt, socket)) return;
      this.setStatus('connected');
      if (!this.isAttemptCurrent(attempt, socket)) return;
      const delivered = this.dispatchMessage(message);
      if (!this.isAttemptCurrent(attempt, socket)) return;
      if (!delivered) socket.close();
      else {
        this.scheduleReconnectReset(attempt);
        this.scheduleHeartbeat(attempt);
      }
      return;
    }
    if (attempt.phase !== 'connected') {
      this.safeDiagnostic('realtime_before_connected');
      if (!this.isAttemptCurrent(attempt, socket)) return;
      this.setStatus('protocol-error');
      if (this.isAttemptCurrent(attempt, socket)) socket.close();
      return;
    }
    if (this.status === 'protocol-error') {
      this.setStatus('connected');
      this.scheduleReconnectReset(attempt);
    }
    if (!this.isAttemptCurrent(attempt, socket)) return;
    if (message.kind === 'pong') {
      if (attempt.pongDeadline !== null) {
        this.clearAttemptTimer(attempt, 'pongDeadline');
        if (this.isAttemptCurrent(attempt, socket)) this.scheduleHeartbeat(attempt);
      }
      return;
    }
    if (message.kind === 'durable') {
      if (
        (attempt.binding.cursor !== null && message.sequence <= attempt.binding.cursor) ||
        attempt.inFlightDurable.has(message.sequence)
      )
        return;
      attempt.inFlightDurable.add(message.sequence);
      let delivered: boolean;
      try {
        delivered = this.dispatchMessage(message);
      } finally {
        attempt.inFlightDurable.delete(message.sequence);
      }
      if (!delivered) {
        if (!this.isAttemptCurrent(attempt, socket)) return;
        this.setStatus('protocol-error');
        if (this.isAttemptCurrent(attempt, socket)) socket.close();
        return;
      }
      if (!this.isAttemptCurrent(attempt, socket) || attempt.phase !== 'connected') return;
      if (attempt.binding.cursor !== null && message.sequence <= attempt.binding.cursor) return;
      attempt.binding.cursor = message.sequence;
      this.persistCursor(attempt.binding);
      return;
    }
    if (!this.dispatchMessage(message)) {
      if (!this.isAttemptCurrent(attempt, socket)) return;
      this.setStatus('protocol-error');
      if (this.isAttemptCurrent(attempt, socket)) socket.close();
    }
  }

  private dispatchMessage(message: RealtimeProtocolMessage): boolean {
    try {
      this.options.onMessage?.(message);
      return true;
    } catch {
      this.safeDiagnostic('realtime_observer_failed');
      return false;
    }
  }

  private safeDiagnostic(code: string): void {
    try {
      this.options.onDiagnostic?.({ code });
    } catch {
      // Observers cannot participate in transport state transitions.
    }
  }
  private scheduleHeartbeat(attempt: ConnectionAttempt): void {
    if (
      !this.isAttemptCurrent(attempt) ||
      attempt.phase !== 'connected' ||
      attempt.heartbeatTimer !== null ||
      attempt.pongDeadline !== null ||
      this.status !== 'connected'
    )
      return;
    if (
      this.installAttemptTimer(
        attempt,
        'heartbeatTimer',
        HEARTBEAT_MS,
        () => attempt.phase === 'connected' && this.status === 'connected' && attempt.pongDeadline === null,
        () => this.sendPing(attempt)
      ) === 'handled'
    )
      return;
  }

  private scheduleReconnectReset(attempt: ConnectionAttempt): void {
    if (
      !this.isAttemptCurrent(attempt) ||
      attempt.phase !== 'connected' ||
      attempt.binding.reconnectAttempt === 0 ||
      attempt.stabilityTimer !== null ||
      this.status !== 'connected'
    )
      return;
    this.installAttemptTimer(
      attempt,
      'stabilityTimer',
      RECONNECT_STABLE_MS,
      () => attempt.phase === 'connected' && this.status === 'connected',
      () => {
        attempt.binding.reconnectAttempt = 0;
      }
    );
  }

  private sendPing(attempt: ConnectionAttempt): void {
    const socket = attempt.socket;
    if (
      !socket ||
      !this.isAttemptCurrent(attempt, socket) ||
      socket.readyState !== OPEN ||
      attempt.phase !== 'connected' ||
      this.status !== 'connected' ||
      attempt.pongDeadline !== null
    )
      return;
    try {
      socket.send('{"type":"ping"}');
    } catch {
      if (this.isAttemptCurrent(attempt, socket)) socket.close();
      return;
    }
    if (!this.isAttemptCurrent(attempt, socket)) return;
    if (
      this.installAttemptTimer(
        attempt,
        'pongDeadline',
        PONG_DEADLINE_MS,
        () => attempt.phase === 'connected' && attempt.socket === socket && socket.readyState === OPEN,
        () => socket.close()
      ) === 'handled'
    )
      return;
  }

  private handleClose(attempt: ConnectionAttempt): void {
    if (!this.isAttemptCurrent(attempt)) return;
    const retired = this.retireAttempt(attempt, false);
    if (retired && this.isBindingCurrent(attempt.binding) && !this.attempt) this.scheduleReconnect(attempt.binding);
  }

  private scheduleReconnect(binding: IdentityBinding): void {
    if (!this.isBindingCurrent(binding)) return;
    const attempt = this.installAttempt(binding, 'reconnect-wait');
    if (!attempt) return;
    const online = this.readOnline();
    if (!this.isAttemptCurrent(attempt)) return;
    if (online === null) return this.handleOnlineCheckFailure(attempt);
    if (!online && !isLoopbackEndpoint(binding.endpoint)) {
      const retired = this.retireAttempt(attempt);
      if (retired && this.isBindingCurrent(binding) && !this.attempt) this.setStatus('offline');
      return;
    }
    this.setStatus('reconnecting');
    if (!this.isAttemptCurrent(attempt)) return;
    const capped = Math.min(1_000 * 2 ** binding.reconnectAttempt, 30_000);
    const delay = capped + Math.floor(this.options.random() * 250);
    if (!this.isAttemptCurrent(attempt)) return;
    binding.reconnectAttempt++;
    if (
      this.installAttemptTimer(
        attempt,
        'reconnectTimer',
        delay,
        () => attempt.phase === 'reconnect-wait' && this.isBindingCurrent(binding),
        () => {
          if (this.attempt === attempt) this.attempt = null;
          this.connect(binding);
        }
      ) === 'handled'
    )
      return;
  }

  private stopConnection(): void {
    const binding = this.binding;
    const attempt = this.attempt;
    if (binding) binding.reconnectAttempt = 0;
    if (attempt) this.retireAttempt(attempt);
  }

  private retireAttempt(attempt: ConnectionAttempt, closeSocket = true): boolean {
    if (attempt.phase === 'retiring') return false;
    const wasPublished = this.attempt === attempt && this.attempt.token === attempt.token;
    attempt.phase = 'retiring';
    const socket = attempt.socket;
    attempt.socket = null;
    const detachSocket = attempt.detachSocket;
    attempt.detachSocket = null;
    detachSocket?.();
    for (const key of [
      'reconnectTimer',
      'handshakeTimer',
      'heartbeatTimer',
      'pongDeadline',
      'stabilityTimer',
    ] as const) {
      this.clearAttemptTimer(attempt, key);
    }
    attempt.inFlightDurable.clear();
    if (closeSocket && socket) {
      try {
        socket.close();
      } catch {
        this.safeDiagnostic('realtime_socket_cleanup_failed');
      }
    }
    if (wasPublished && this.attempt === attempt) this.attempt = null;
    return wasPublished && this.attempt === null;
  }

  private clearAttemptTimer(attempt: ConnectionAttempt, key: AttemptTimerKey): void {
    const lease = attempt[key];
    attempt[key] = null;
    if (lease && lease.handle !== null) this.clearTimerHandle(lease.handle);
  }

  private installAttemptTimer(
    attempt: ConnectionAttempt,
    key: AttemptTimerKey,
    delay: number,
    eligible: () => boolean,
    onFire: () => void
  ): TimerInstallResult {
    if (!this.isAttemptCurrent(attempt) || attempt[key] !== null || !eligible()) return 'handled';
    const lease: AttemptTimerLease = { handle: null };
    attempt[key] = lease;
    let handle: Timer;
    try {
      handle = this.options.setTimer(() => {
        if (attempt[key] !== lease) return;
        attempt[key] = null;
        if (this.isAttemptCurrent(attempt) && eligible()) onFire();
      }, delay);
    } catch {
      if (attempt[key] === lease) attempt[key] = null;
      this.handleTimerInstallFailure(attempt, key);
      return 'handled';
    }
    lease.handle = handle;
    if (!this.isAttemptCurrent(attempt) || attempt[key] !== lease || !eligible()) {
      if (attempt[key] === lease) attempt[key] = null;
      this.clearTimerHandle(handle);
      return 'handled';
    }
    return 'installed';
  }

  private handleTimerInstallFailure(attempt: ConnectionAttempt, key: AttemptTimerKey): void {
    if (!this.isAttemptCurrent(attempt)) return;
    const binding = attempt.binding;
    if (!this.retireAttempt(attempt)) return;
    this.safeDiagnostic('realtime_timer_install_failed');
    if (!this.isBindingCurrent(binding) || this.attempt) return;
    const online = this.readOnline();
    if (!this.isBindingCurrent(binding) || this.attempt) return;
    if (online === null) {
      this.safeDiagnostic('realtime_online_check_failed');
      if (this.isBindingCurrent(binding) && !this.attempt) this.setStatus('protocol-error');
    } else if (!online && !isLoopbackEndpoint(binding.endpoint)) {
      this.setStatus('offline');
    } else if (key === 'reconnectTimer') {
      this.setStatus('protocol-error');
    } else {
      this.scheduleReconnect(binding);
    }
  }

  private handleOnlineCheckFailure(attempt: ConnectionAttempt): void {
    if (!this.isAttemptCurrent(attempt)) return;
    const binding = attempt.binding;
    if (!this.retireAttempt(attempt)) return;
    this.safeDiagnostic('realtime_online_check_failed');
    if (this.isBindingCurrent(binding) && !this.attempt) this.setStatus('protocol-error');
  }

  private readOnline(): boolean | null {
    try {
      return this.options.isOnline();
    } catch {
      return null;
    }
  }

  private clearTimerHandle(handle: Timer): void {
    try {
      this.options.clearTimer(handle);
    } catch {
      this.safeDiagnostic('realtime_timer_cleanup_failed');
    }
  }

  private loadCursorCandidate(storageKey: string): {
    storageKey: string | null;
    cursor: number | null;
    memoryOnly: boolean;
  } {
    if (!this.options.storage) return { storageKey: null, cursor: null, memoryOnly: true };
    try {
      const raw = this.options.storage.getItem(storageKey);
      if (raw === null || !/^(0|[1-9]\d*)$/.test(raw)) return { storageKey, cursor: null, memoryOnly: false };
      const value = Number(raw);
      const cursor = Number.isSafeInteger(value) && value >= 0 ? value : null;
      return { storageKey, cursor, memoryOnly: false };
    } catch {
      return { storageKey: null, cursor: null, memoryOnly: true };
    }
  }

  private persistCursor(binding: IdentityBinding): void {
    if (!this.isBindingCurrent(binding) || binding.cursor === null || !binding.storageKey || !this.options.storage)
      return;
    const storageKey = binding.storageKey;
    try {
      this.options.storage.setItem(storageKey, String(binding.cursor));
    } catch {
      if (this.isBindingCurrent(binding) && binding.storageKey === storageKey) {
        binding.storageKey = null;
        binding.memoryOnly = true;
      }
    }
  }

  private setStatus(status: RealtimeConnectionStatus): void {
    if (status === this.status) return;
    this.status = status;
    try {
      this.options.onStatus?.(status);
    } catch {
      // Observers cannot participate in transport state transitions.
    }
  }

  private readonly handleWake = (): void => {
    const binding = this.binding;
    if (!binding || !this.isBindingCurrent(binding)) return;
    const attempt = this.attempt;
    if (!attempt) {
      this.connect(binding);
      return;
    }
    if (!this.isAttemptCurrent(attempt)) return;
    if (attempt.phase === 'reconnect-wait') {
      const retired = this.retireAttempt(attempt);
      if (retired && this.isBindingCurrent(binding) && !this.attempt) this.connect(binding);
    } else if (attempt.phase === 'connected') {
      this.sendPing(attempt);
    }
  };

  private readonly handleOffline = (): void => {
    const binding = this.binding;
    if (!binding || !this.isBindingCurrent(binding)) return;
    // Browser network hints describe upstream connectivity, not whether a
    // loopback source-development or packaged backend is reachable. Keep the
    // local socket authoritative and let its real close/handshake drive the
    // existing bounded reconnect path.
    if (isLoopbackEndpoint(binding.endpoint)) return;
    this.stopConnection();
    if (this.isBindingCurrent(binding) && !this.attempt) this.setStatus('offline');
  };

  private readonly handleVisibility = (): void => {
    if (this.options.documentTarget?.visibilityState === 'visible') this.handleWake();
  };
}

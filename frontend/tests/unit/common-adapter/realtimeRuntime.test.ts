/**
 * @vitest-environment node
 */

import { describe, expect, it, vi } from 'vitest';
import {
  createRealtimeRuntime,
  type RealtimeRuntimeClient,
  type RealtimeRuntimeClientObservers,
} from '@/common/adapter/realtimeRuntime';
import type { RealtimeConnectionStatus } from '@/common/adapter/realtimeClient';
import type { RealtimeProtocolMessage } from '@/common/adapter/realtimeProtocol';

class FakeClient implements RealtimeRuntimeClient {
  readonly identities: Array<string | null | undefined> = [];
  disposeCount = 0;

  constructor(private readonly observers: RealtimeRuntimeClientObservers) {}

  async setIdentity(identity?: string | null): Promise<void> {
    this.identities.push(identity);
  }

  dispose(): void {
    this.disposeCount++;
  }

  status(status: RealtimeConnectionStatus): void {
    this.observers.onStatus(status);
  }

  message(message: RealtimeProtocolMessage): void {
    this.observers.onMessage(message);
  }
}

function setup() {
  let client: FakeClient | null = null;
  const createClient = vi.fn((observers: RealtimeRuntimeClientObservers) => {
    client = new FakeClient(observers);
    return client;
  });
  const runtime = createRealtimeRuntime({ createClient });
  return {
    runtime,
    createClient,
    client: () => {
      if (!client) throw new Error('client was not created');
      return client;
    },
  };
}

const connected = (cursor = 0): RealtimeProtocolMessage => ({
  kind: 'connected',
  cursor,
  payload: { type: 'connected', cursor },
});

describe('RealtimeRuntime', () => {
  it('constructs one client lazily and preserves the three identity states', async () => {
    const { runtime, createClient, client } = setup();
    const initial = runtime.getSnapshot();

    expect(createClient).not.toHaveBeenCalled();
    expect(initial).toEqual({ identity: 'checking', status: 'idle' });
    expect(runtime.getSnapshot()).toBe(initial);
    expect('send' in runtime).toBe(false);
    expect('emit' in runtime).toBe(false);

    await runtime.setIdentity(undefined);
    expect(createClient).toHaveBeenCalledTimes(1);
    expect(runtime.getSnapshot()).toEqual({ identity: 'checking', status: 'idle' });

    await runtime.setIdentity(null);
    expect(runtime.getSnapshot()).toEqual({ identity: 'unauthenticated', status: 'auth-required' });

    await runtime.setIdentity('  owner-a  ');
    expect(runtime.getSnapshot()).toEqual({ identity: 'authenticated', status: 'idle' });
    expect(client().identities).toEqual([undefined, null, 'owner-a']);
    expect(createClient).toHaveBeenCalledTimes(1);
    expect(JSON.stringify(runtime.getSnapshot())).not.toContain('owner-a');
  });

  it('keeps the newest identity state when client promises settle out of order', async () => {
    const pending: Array<() => void> = [];
    const identities: Array<string | null | undefined> = [];
    const runtime = createRealtimeRuntime({
      createClient: () => ({
        setIdentity(identity?: string | null) {
          identities.push(identity);
          return new Promise<void>((resolve) => pending.push(resolve));
        },
        dispose() {},
      }),
    });

    const first = runtime.setIdentity('owner-a');
    const second = runtime.setIdentity('owner-b');
    pending[1]();
    await second;
    pending[0]();
    await first;

    expect(identities).toEqual(['owner-a', 'owner-b']);
    expect(runtime.getSnapshot()).toEqual({ identity: 'authenticated', status: 'idle' });
  });

  it('creates only one client when the injected factory reenters identity setup', async () => {
    let runtime: ReturnType<typeof createRealtimeRuntime>;
    let client: FakeClient | null = null;
    const createClient = vi.fn((observers: RealtimeRuntimeClientObservers) => {
      observers.onStatus('connecting');
      void runtime.setIdentity('owner-b');
      client = new FakeClient(observers);
      return client;
    });
    runtime = createRealtimeRuntime({ createClient });

    await runtime.setIdentity('owner-a');

    expect(createClient).toHaveBeenCalledTimes(1);
    expect(client?.identities).toEqual(['owner-b']);
    expect(runtime.getSnapshot()).toEqual({ identity: 'authenticated', status: 'idle' });
  });

  it('does not carry a pending identity across failed client construction', async () => {
    let runtime: ReturnType<typeof createRealtimeRuntime>;
    let attempts = 0;
    let client: FakeClient | null = null;
    const createClient = vi.fn((observers: RealtimeRuntimeClientObservers) => {
      attempts++;
      if (attempts === 1) {
        void runtime.setIdentity('owner-b');
        throw new Error('factory failure');
      }
      client = new FakeClient(observers);
      return client;
    });
    runtime = createRealtimeRuntime({ createClient });

    await expect(runtime.setIdentity('owner-a')).rejects.toThrow('factory failure');
    await runtime.setIdentity('owner-c');

    expect(createClient).toHaveBeenCalledTimes(2);
    expect(client?.identities).toEqual(['owner-c']);
  });

  it('publishes stable status snapshots and isolates throwing or removed observers', async () => {
    const { runtime, client } = setup();
    const statuses: RealtimeConnectionStatus[] = [];
    await runtime.setIdentity('owner-a');
    const throwing = runtime.subscribeStatus(() => {
      throw new Error('observer failure');
    });
    const unsubscribe = runtime.subscribeStatus(() => statuses.push(runtime.getSnapshot().status));
    const idle = runtime.getSnapshot();

    client().status('connecting');
    const connecting = runtime.getSnapshot();
    client().status('connecting');

    expect(connecting).not.toBe(idle);
    expect(runtime.getSnapshot()).toBe(connecting);
    expect(statuses).toEqual(['connecting']);

    throwing();
    unsubscribe();
    client().status('connected');
    expect(statuses).toEqual(['connecting']);
  });

  it('delivers complete tagged business messages without payload-based rerouting', async () => {
    const { runtime, client } = setup();
    const durableReceived: RealtimeProtocolMessage[] = [];
    const directReceived: RealtimeProtocolMessage[] = [];
    const controlReceived: RealtimeProtocolMessage[] = [];
    runtime.subscribe('durable', 'file_changed', () => {
      throw new Error('observer failure');
    });
    const unsubscribeDurable = runtime.subscribe('durable', 'file_changed', (message) => durableReceived.push(message));
    const unsubscribeDirect = runtime.subscribe('direct', 'file_changed', (message) => directReceived.push(message));
    const unsubscribeControl = runtime.subscribe('control', 'error', (message) => controlReceived.push(message));
    await runtime.setIdentity('owner-a');

    const durable: RealtimeProtocolMessage = {
      kind: 'durable',
      type: 'file_changed',
      eventId: 'event-7',
      sequence: 7,
      deliveryKind: 'fanout',
      payload: { type: 'spoofed', name: 'other', event: 'other', data: { type: 'other' } },
    };
    const direct: RealtimeProtocolMessage = {
      kind: 'direct',
      type: 'file_changed',
      payload: { type: 'file_changed', path: '/workspace/a.csv' },
    };
    const control: RealtimeProtocolMessage = {
      kind: 'control',
      type: 'error',
      payload: { type: 'error', code: 'BOUNDED' },
    };

    client().message(connected());
    client().message({ kind: 'pong', payload: { type: 'pong' } });
    client().message(durable);
    client().message(direct);
    client().message(control);

    expect(durableReceived).toEqual([durable]);
    expect(directReceived).toEqual([direct]);
    expect(controlReceived).toEqual([control]);
    expect(durableReceived[0]).toBe(durable);
    expect(durableReceived[0]).toMatchObject({
      kind: 'durable',
      eventId: 'event-7',
      sequence: 7,
      deliveryKind: 'fanout',
    });
    expect(directReceived[0]).toMatchObject({ kind: 'direct', type: 'file_changed' });

    unsubscribeDurable();
    unsubscribeDirect();
    unsubscribeControl();
    client().message(direct);
    expect(directReceived).toHaveLength(1);
  });

  it.each([
    ['durable', 'file_changed', 'owner-b'],
    ['direct', 'file_deleted', null],
    ['control', 'error', 'owner-b'],
  ] as const)('stops stale %s dispatch after an identity transition', async (kind, type, nextOwner) => {
    const { runtime, client } = setup();
    const later = vi.fn();
    const stopTransition = runtime.subscribe(kind, type, () => {
      void runtime.setIdentity(nextOwner);
    });
    runtime.subscribe(kind, type, later);
    await runtime.setIdentity('owner-a');
    const message =
      kind === 'durable'
        ? ({ kind, type, eventId: 'event-1', sequence: 1, deliveryKind: 'fanout', payload: { type } } as const)
        : ({ kind, type, payload: { type } } as const);

    client().message(message);
    expect(later).not.toHaveBeenCalled();

    stopTransition();
    client().message(message);
    expect(later).toHaveBeenCalledTimes(1);
  });

  it('emits reconnected exactly once after a validated reconnect for the same identity', async () => {
    const { runtime, client } = setup();
    const reconnected = vi.fn();
    runtime.subscribeReconnected(() => {
      throw new Error('observer failure');
    });
    runtime.subscribeReconnected(reconnected);
    await runtime.setIdentity('owner-a');

    client().status('connected');
    client().message(connected(1));
    client().message(connected(1));
    expect(reconnected).not.toHaveBeenCalled();

    client().status('reconnecting');
    client().status('connected');
    client().message(connected(2));
    client().message(connected(2));
    expect(reconnected).toHaveBeenCalledTimes(1);

    client().status('offline');
    client().status('connected');
    client().message(connected(3));
    expect(reconnected).toHaveBeenCalledTimes(2);

    await runtime.setIdentity('owner-b');
    client().status('connected');
    client().message(connected(0));
    await runtime.setIdentity(null);
    expect(reconnected).toHaveBeenCalledTimes(2);
  });

  it('stops stale reconnected dispatch after an identity transition', async () => {
    const { runtime, client } = setup();
    const later = vi.fn();
    const stopTransition = runtime.subscribeReconnected(() => {
      void runtime.setIdentity('owner-b');
    });
    runtime.subscribeReconnected(later);
    await runtime.setIdentity('owner-a');

    client().status('connected');
    client().message(connected(1));
    client().status('reconnecting');
    client().status('connected');
    client().message(connected(2));
    expect(later).not.toHaveBeenCalled();

    stopTransition();
    client().status('connected');
    client().message(connected(3));
    client().status('reconnecting');
    client().status('connected');
    client().message(connected(4));
    expect(later).toHaveBeenCalledTimes(1);
  });

  it('stops the old status dispatch when a listener reenters the same identity', async () => {
    const { runtime, client } = setup();
    const statuses: RealtimeConnectionStatus[] = [];
    let reentered = false;
    await runtime.setIdentity('owner-a');
    runtime.subscribeStatus(() => {
      if (!reentered && runtime.getSnapshot().status === 'connecting') {
        reentered = true;
        void runtime.setIdentity('owner-a');
      }
    });
    runtime.subscribeStatus(() => statuses.push(runtime.getSnapshot().status));

    client().status('connecting');

    expect(statuses).toEqual(['idle']);
    expect(client().identities).toEqual(['owner-a', 'owner-a']);
  });

  it('honors listener removal during keyed event dispatch', async () => {
    const { runtime, client } = setup();
    const removed = vi.fn();
    let removeLater = () => {};
    runtime.subscribe('control', 'error', () => removeLater());
    removeLater = runtime.subscribe('control', 'error', removed);
    await runtime.setIdentity('owner-a');

    client().message({ kind: 'control', type: 'error', payload: { type: 'error' } });
    removeLater();

    expect(removed).not.toHaveBeenCalled();
  });

  it('handles unsubscribe and dispose reentry without delivering stale observers', async () => {
    const { runtime, client } = setup();
    const later = vi.fn();
    await runtime.setIdentity('owner-a');
    runtime.subscribe('direct', 'file_deleted', () => runtime.dispose());
    runtime.subscribe('direct', 'file_deleted', later);

    client().message({ kind: 'direct', type: 'file_deleted', payload: { type: 'file_deleted' } });
    client().status('connected');

    expect(later).not.toHaveBeenCalled();
    expect(client().disposeCount).toBe(1);
    expect(runtime.getSnapshot()).toEqual({ identity: 'checking', status: 'idle' });
    runtime.dispose();
    await runtime.setIdentity('owner-b');
    expect(client().disposeCount).toBe(1);
  });
});

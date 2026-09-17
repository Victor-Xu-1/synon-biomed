/**
 * @vitest-environment jsdom
 */

import React, { useState } from 'react';
import { act, cleanup, render, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import useSWR, { useSWRConfig } from 'swr';
import { RealtimeProvider, useRealtime } from '@/renderer/hooks/context/RealtimeContext';
import type { RealtimeRuntime, RealtimeRuntimeSnapshot } from '@/common/adapter/realtimeRuntime';

const auth = vi.hoisted(() => ({
  value: {
    status: 'checking' as 'checking' | 'authenticated' | 'unauthenticated' | 'unavailable',
    user: null as null | { id?: string; username: string },
  },
}));

vi.mock('@/renderer/hooks/context/AuthContext', () => ({ useAuth: () => auth.value }));

class FakeRuntime implements RealtimeRuntime {
  snapshot: RealtimeRuntimeSnapshot = Object.freeze({ identity: 'checking', status: 'idle' });
  identities: Array<string | null | undefined> = [];
  listeners = new Set<() => void>();
  subscribeStatus = (listener: () => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };
  getSnapshot = () => this.snapshot;
  subscribe = () => () => {};
  subscribeReconnected = () => () => {};
  setIdentity = async (owner?: string | null) => {
    this.identities.push(owner);
  };
  dispose = vi.fn();

  status(status: RealtimeRuntimeSnapshot['status']) {
    this.snapshot = Object.freeze({ ...this.snapshot, status });
    for (const listener of this.listeners) listener();
  }
}

const Snapshot = () => {
  const { snapshot } = useRealtime();
  return <output>{`${snapshot.identity}:${snapshot.status}`}</output>;
};

const OwnerScopedData = ({ load }: { load: () => Promise<string> }) => {
  const { mutate } = useSWRConfig();
  scopedMutate = mutate;
  const { data } = useSWR('owned-conversation', load);
  return <output data-testid='owned-data'>{data ?? 'empty'}</output>;
};

let scopedMutate: ReturnType<typeof useSWRConfig>['mutate'] | undefined;

const OwnerScopedLocalState = () => {
  const [ownerAtMount] = useState(auth.value.user?.id ?? 'none');
  return <output data-testid='owner-at-mount'>{ownerAtMount}</output>;
};

describe('RealtimeProvider', () => {
  beforeEach(() => {
    auth.value = { status: 'checking', user: null };
  });

  afterEach(() => cleanup());

  it('maps only authoritative auth status and trimmed user id to runtime identity', async () => {
    const runtime = new FakeRuntime();
    const view = render(
      <RealtimeProvider runtime={runtime}>
        <Snapshot />
      </RealtimeProvider>
    );
    const rerender = async () => {
      await act(async () => {
        view.rerender(
          <RealtimeProvider runtime={runtime}>
            <Snapshot />
          </RealtimeProvider>
        );
      });
    };

    await act(async () => {});
    auth.value = { status: 'authenticated', user: { id: '  owner-a  ', username: 'ignored-name' } };
    await rerender();
    auth.value = { status: 'checking', user: { id: 'owner-a', username: 'ignored-name' } };
    await rerender();
    auth.value = { status: 'unavailable', user: null };
    await rerender();
    auth.value = { status: 'authenticated', user: { id: '   ', username: 'must-not-fallback' } };
    await rerender();
    auth.value = { status: 'authenticated', user: { username: 'still-must-not-fallback' } };
    await rerender();
    auth.value = { status: 'unauthenticated', user: null };
    await rerender();

    expect(runtime.identities).toEqual([undefined, 'owner-a', null]);
  });

  it('preserves the authenticated owner scope across transient auth unavailability', async () => {
    auth.value = { status: 'authenticated', user: { id: 'owner-a', username: 'owner-a' } };
    const runtime = new FakeRuntime();
    const view = render(
      <RealtimeProvider runtime={runtime}>
        <OwnerScopedLocalState />
      </RealtimeProvider>
    );
    await act(async () => {});
    expect(view.getByTestId('owner-at-mount').textContent).toBe('owner-a');

    auth.value = { status: 'unavailable', user: null };
    await act(async () => {
      view.rerender(
        <RealtimeProvider runtime={runtime}>
          <OwnerScopedLocalState />
        </RealtimeProvider>
      );
    });

    expect(runtime.identities).toEqual(['owner-a']);
    expect(view.getByTestId('owner-at-mount').textContent).toBe('owner-a');
  });

  it('exposes useSyncExternalStore snapshots and unsubscribes on unmount', async () => {
    const runtime = new FakeRuntime();
    const view = render(
      <RealtimeProvider runtime={runtime}>
        <Snapshot />
      </RealtimeProvider>
    );
    expect(view.container.textContent).toBe('checking:idle');
    expect(runtime.listeners.size).toBe(1);

    await act(async () => runtime.status('connecting'));
    expect(view.container.textContent).toBe('checking:connecting');

    view.unmount();
    expect(runtime.listeners.size).toBe(0);
  });

  it('does not expose cached owner data while the next identity is loading', async () => {
    auth.value = { status: 'authenticated', user: { id: 'owner-a', username: 'owner-a' } };
    let resolveOwnerB: ((value: string) => void) | undefined;
    const ownerB = new Promise<string>((resolve) => {
      resolveOwnerB = resolve;
    });
    const load = vi.fn(() => (auth.value.user?.id === 'owner-a' ? Promise.resolve('private-owner-a') : ownerB));
    const runtime = new FakeRuntime();
    const view = render(
      <RealtimeProvider runtime={runtime}>
        <OwnerScopedData load={load} />
        <OwnerScopedLocalState />
      </RealtimeProvider>
    );

    await waitFor(() => expect(view.getByTestId('owned-data').textContent).toBe('private-owner-a'));
    expect(view.getByTestId('owner-at-mount').textContent).toBe('owner-a');
    await act(async () => scopedMutate?.('owned-conversation', 'updated-owner-a', { revalidate: false }));
    expect(view.getByTestId('owned-data').textContent).toBe('updated-owner-a');

    auth.value = { status: 'authenticated', user: { id: 'owner-b', username: 'owner-b' } };
    await act(async () => {
      view.rerender(
        <RealtimeProvider runtime={runtime}>
          <OwnerScopedData load={load} />
          <OwnerScopedLocalState />
        </RealtimeProvider>
      );
    });
    expect(view.queryByText('private-owner-a')).toBeNull();
    expect(view.getByTestId('owner-at-mount').textContent).toBe('owner-b');
    expect(view.getByTestId('owned-data').textContent).toBe('empty');
    await waitFor(() => expect(load).toHaveBeenCalledTimes(2));
    await act(async () => resolveOwnerB?.('private-owner-b'));
    await waitFor(() => expect(view.getByTestId('owned-data').textContent).toBe('private-owner-b'));
  });

  it('fails explicitly when consumed outside the provider', () => {
    expect(() => render(<Snapshot />)).toThrow('useRealtime must be used within a RealtimeProvider');
  });
});

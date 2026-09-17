import React, { createContext, useContext, useEffect, useMemo, useRef, useSyncExternalStore } from 'react';
import { SWRConfig } from 'swr';
import type { RealtimeRuntime, RealtimeRuntimeSnapshot } from '@/common/adapter/realtimeRuntime';
import { useAuth } from './AuthContext';

export type RealtimeContextValue = Readonly<{
  runtime: RealtimeRuntime;
  snapshot: RealtimeRuntimeSnapshot;
}>;

const RealtimeContext = createContext<RealtimeContextValue | null>(null);

const createIdentityCache = () => new Map();

export const RealtimeProvider: React.FC<React.PropsWithChildren<{ runtime: RealtimeRuntime }>> = ({
  children,
  runtime,
}) => {
  const { status, user } = useAuth();
  const snapshot = useSyncExternalStore(runtime.subscribeStatus, runtime.getSnapshot, runtime.getSnapshot);
  const authenticatedOwner = typeof user?.id === 'string' ? user.id.trim() || null : null;
  const lastAuthenticatedOwner = useRef<string | null>(null);
  const identityPending = status === 'checking' || status === 'unavailable';
  const owner = identityPending
    ? (lastAuthenticatedOwner.current ?? undefined)
    : status === 'authenticated'
      ? authenticatedOwner
      : null;
  const identityScope = owner === undefined ? 'checking' : owner === null ? 'anonymous' : `owner:${owner}`;

  useEffect(() => {
    if (status === 'authenticated' && authenticatedOwner) {
      lastAuthenticatedOwner.current = authenticatedOwner;
    } else if (status === 'unauthenticated') {
      lastAuthenticatedOwner.current = null;
    }
  }, [authenticatedOwner, status]);

  useEffect(() => {
    void runtime.setIdentity(owner);
  }, [owner, runtime]);

  useEffect(() => {
    if (import.meta.env.DEV) {
      console.debug('[realtime]', `realtime_state_${snapshot.identity}_${snapshot.status}`);
    }
  }, [snapshot.identity, snapshot.status]);

  const value = useMemo<RealtimeContextValue>(() => ({ runtime, snapshot }), [runtime, snapshot]);
  return (
    <SWRConfig key={identityScope} value={{ provider: createIdentityCache }}>
      <RealtimeContext.Provider value={value}>{children}</RealtimeContext.Provider>
    </SWRConfig>
  );
};

export function useRealtime(): RealtimeContextValue {
  const context = useContext(RealtimeContext);
  if (!context) throw new Error('useRealtime must be used within a RealtimeProvider');
  return context;
}

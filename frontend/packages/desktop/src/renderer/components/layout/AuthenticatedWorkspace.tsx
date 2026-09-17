/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import React, { useEffect, useRef, useState } from 'react';
import { useLocation } from 'react-router';
import { bindRealtimeRuntime } from '@/common/adapter/httpBridge';
import { createRendererRealtimeRuntime } from '@/common/adapter/realtimeRenderer';
import type { RealtimeRuntime } from '@/common/adapter/realtimeRuntime';
import { RealtimeProvider } from '@renderer/hooks/context/RealtimeContext';
import { ConversationHistoryProvider } from '@renderer/hooks/context/ConversationHistoryContext';
import { PreviewProvider } from '@renderer/pages/conversation/Preview/context/PreviewContext';
import Layout from '@renderer/components/layout/Layout';
import Sider from '@renderer/components/layout/Sider';
import { prefetchConversationRoute } from '@renderer/pages/conversation/conversationRoute';

// v1.1 mounts the stable workspace shell before its transport is ready. Keep
// the same boundary here without creating a socket during render: consumers
// can mount against this inert runtime, then switch to the bound runtime from
// the effect below without flashing a full-page loader.
const deferredRealtimeSnapshot: ReturnType<RealtimeRuntime['getSnapshot']> = Object.freeze({
  identity: 'checking',
  status: 'idle',
});

const deferredRealtimeRuntime: RealtimeRuntime = {
  getSnapshot: () => deferredRealtimeSnapshot,
  subscribeStatus: () => () => {},
  subscribe: () => () => {},
  subscribeReconnected: () => () => {},
  setIdentity: async () => {},
  dispose: () => {},
};

export const InitialConversationPrefetchAuthority: React.FC = () => {
  const location = useLocation();
  const startedRequests = useRef(new Set<string>());

  useEffect(() => {
    const match = /^\/conversation\/([^/]+)$/.exec(location.pathname);
    if (!match) return;

    const requestKey = match[1];
    if (startedRequests.current.has(requestKey)) return;
    startedRequests.current.add(requestKey);

    void prefetchConversationRoute().catch(() => {
      console.warn('[route-prefetch] initial conversation module unavailable');
    });

    return undefined;
  }, [location.pathname]);

  return null;
};

const AuthenticatedWorkspace: React.FC = () => {
  const [realtime, setRealtime] = useState<{
    runtime: ReturnType<typeof createRendererRealtimeRuntime>;
    release: () => void;
  } | null>(null);
  const [bindingFailure, setBindingFailure] = useState<unknown>(null);

  useEffect(() => {
    let active = true;
    const runtime = createRendererRealtimeRuntime();
    let release: (() => void) | null = null;
    try {
      release = bindRealtimeRuntime(runtime);
      if (active) setRealtime({ runtime, release });
    } catch (error) {
      runtime.dispose();
      if (active) setBindingFailure(error);
    }
    return () => {
      active = false;
      release?.();
      runtime.dispose();
    };
  }, []);

  if (bindingFailure) throw bindingFailure;

  return (
    <RealtimeProvider runtime={realtime?.runtime ?? deferredRealtimeRuntime}>
      <PreviewProvider>
        <ConversationHistoryProvider>
          <InitialConversationPrefetchAuthority />
          <Layout sider={<Sider />} />
        </ConversationHistoryProvider>
      </PreviewProvider>
    </RealtimeProvider>
  );
};

export default AuthenticatedWorkspace;

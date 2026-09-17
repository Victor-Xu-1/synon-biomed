import {
  RealtimeClient,
  buildRealtimeEndpoint,
  type RealtimeClientOptions,
  type RealtimeEndpoint,
} from './realtimeClient';
import { createRealtimeRuntime, type RealtimeRuntime } from './realtimeRuntime';

type StorageLike = Pick<Storage, 'getItem' | 'setItem'>;
type RendererWindow = EventTarget & {
  readonly location: Pick<Location, 'protocol' | 'host' | 'origin'>;
  readonly __backendPort?: number;
};
type RendererDocument = EventTarget & { readonly visibilityState?: string };

export type RendererRealtimeOptions = Pick<
  RealtimeClientOptions,
  'createSocket' | 'digestOwner' | 'isOnline' | 'random' | 'setTimer' | 'clearTimer' | 'onDiagnostic'
> & {
  window?: RendererWindow;
  document?: RendererDocument;
  getStorage?: () => StorageLike | undefined;
};

const resolveEndpoint = (runtimeWindow: RendererWindow): RealtimeEndpoint => {
  if (runtimeWindow.__backendPort !== undefined) {
    return buildRealtimeEndpoint({ backendPort: runtimeWindow.__backendPort });
  }
  return buildRealtimeEndpoint({ location: runtimeWindow.location });
};

const readStorage = (options: RendererRealtimeOptions, runtimeWindow: RendererWindow): StorageLike | undefined => {
  try {
    if (options.getStorage) return options.getStorage();
    return 'localStorage' in runtimeWindow ? (runtimeWindow.localStorage as StorageLike | undefined) : undefined;
  } catch {
    return undefined;
  }
};

export const createRendererRealtimeRuntime = (options: RendererRealtimeOptions = {}): RealtimeRuntime => {
  const runtimeWindow = options.window ?? window;
  const runtimeDocument = options.document ?? document;
  const onDiagnostic = options.onDiagnostic ?? ((diagnostic) => console.warn('[realtime]', diagnostic.code));
  const setTimer = options.setTimer ?? globalThis.setTimeout;
  const clearTimer = options.clearTimer ?? globalThis.clearTimeout;
  const scheduleTimer = ((callback: Parameters<typeof setTimeout>[0], delay?: number) =>
    setTimer(callback, delay)) as typeof setTimeout;
  const cancelTimer = ((handle: Parameters<typeof clearTimeout>[0]) => clearTimer(handle)) as typeof clearTimeout;

  return createRealtimeRuntime({
    createClient: ({ onMessage, onStatus }) =>
      new RealtimeClient({
        endpoint: () => resolveEndpoint(runtimeWindow),
        createSocket: options.createSocket,
        storage: readStorage(options, runtimeWindow),
        digestOwner: options.digestOwner,
        isOnline: options.isOnline,
        random: options.random,
        setTimer: scheduleTimer,
        clearTimer: cancelTimer,
        windowTarget: runtimeWindow,
        documentTarget: runtimeDocument,
        onDiagnostic,
        onMessage,
        onStatus,
      }),
  });
};

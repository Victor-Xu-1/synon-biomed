import { bridge, logger } from '@office-ai/platform';

type BridgeEmitter = { emit: (name: string, data: unknown) => void };
type WebRuntimeWindow = Window & {
  __emitBridgeCallback?: (name: string, data: unknown) => void;
};

const runtimeWindow = window as WebRuntimeWindow;
let emitter: BridgeEmitter | null = null;

const emitLocal = (name: string, data: unknown): void => {
  emitter?.emit(name, data);
};

bridge.adapter({
  emit: emitLocal,
  on(nextEmitter) {
    emitter = nextEmitter;
    runtimeWindow.__emitBridgeCallback = emitLocal;
  },
});

logger.provider({
  log(log) {
    console.log('web.log', log.type, ...log.logs);
  },
  path() {
    return Promise.resolve('');
  },
});

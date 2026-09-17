/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

/**
 * IPC Bridge → HTTP/WS adapter.
 *
 * This file replaces the original IPC bridge calls with HTTP REST and WebSocket
 * calls routed through the Synon Biomed WebHost gateway. Electron-native
 * operations (window controls, native dialogs, auto-update, devtools, zoom,
 * CDP, deep links) remain as IPC.
 */

import { httpRequest } from './httpBridge';

export const httpGetClientSetting = <T>(key: string) => ({
  provider: () => {},
  invoke: (async () => {
    const data = await httpRequest<Record<string, T | undefined>>(
      'GET',
      `/api/settings/client?keys=${encodeURIComponent(key)}`
    );
    return data?.[key];
  }) as () => Promise<T | undefined>,
});

import { ipcBridge } from '@/common';
import { httpRequest } from '@/common/adapter/httpBridge';

import type { ConfigKey } from './configKeys';
import type { ILegacyConfigStorageRefer, IMcpServer } from './storage';
import { BUILTIN_IMAGE_GEN_ID, BUILTIN_IMAGE_GEN_LEGACY_NAMES, BUILTIN_IMAGE_GEN_NAME } from './storage';

export type ConfigFile = {
  get<K extends keyof ILegacyConfigStorageRefer>(key: K): Promise<ILegacyConfigStorageRefer[K]>;
  set<K extends keyof ILegacyConfigStorageRefer>(key: K, value: ILegacyConfigStorageRefer[K]): Promise<unknown>;
};

const LEGACY_MCP_CONFIG_KEY = 'mcp.config' as const;
type LegacyBusinessConfigKey =
  | 'google.config'
  | 'acp.promptTimeout'
  | 'mcp.config'
  | 'tools.imageGenerationModel'
  | 'tools.speechToText';
type LegacyConfigKey = ConfigKey | LegacyBusinessConfigKey;

type LegacyMcpConfigFile = ConfigFile & {
  get(key: typeof LEGACY_MCP_CONFIG_KEY): Promise<unknown>;
  set(key: typeof LEGACY_MCP_CONFIG_KEY, value: unknown): Promise<unknown>;
};

type LegacyConfigFile = ConfigFile & {
  get(key: LegacyConfigKey): Promise<unknown>;
};

const ALL_LEGACY_KEYS: LegacyConfigKey[] = [
  'acp.promptTimeout',
  'language',
  'theme',
  'colorScheme',
  'ui.zoomFactor',
  'ui.fontSize.chat',
  'ui.fontSize.markdown',
  'ui.fontSize.code',
  'webui.desktop.enabled',
  'webui.desktop.allowRemote',
  'webui.desktop.port',
  'customCss',
  'css.themes',
  'css.activeThemeId',
  'tools.imageGenerationModel',
  'tools.speechToText',
  'workspace.pasteConfirm',
  'upload.saveToWorkspace',
  'pet.enabled',
  'pet.size',
  'pet.dnd',
  'pet.confirmEnabled',
  'system.closeToTray',
  'system.notificationEnabled',
  'system.cronNotificationEnabled',
  'system.keepAwake',
  'system.autoPreviewOfficeFiles',
];

export async function migrateConfigStorage(configFile: ConfigFile): Promise<void> {
  const legacyConfigFile = configFile as LegacyConfigFile;
  const entries: Record<string, unknown> = {};

  const legacyEntries = await Promise.all(
    ALL_LEGACY_KEYS.map(async (key) => {
      try {
        const value = await legacyConfigFile.get(key);
        return [key, value] as const;
      } catch {
        return [key, undefined] as const;
      }
    })
  );

  for (const [key, value] of legacyEntries) {
    if (value !== undefined && value !== null) {
      entries[key] = value;
    }
  }

  if (Object.keys(entries).length === 0) {
    console.info('[Migration] configStorage migration skipped — no legacy keys found');
  } else {
    // Merge strategy: only write keys that don't already exist in the backend DB.
    // This prevents overwriting user's runtime changes on repeated migrations.
    const existing = await fetchExistingClientKeys();
    const newEntries: Record<string, unknown> = {};
    for (const [key, value] of Object.entries(entries)) {
      if (!(key in existing)) {
        newEntries[key] = value;
      }
    }

    if (Object.keys(newEntries).length > 0) {
      await setBackendClientPreferences(newEntries);
      console.info(
        '[Migration] configStorage migration completed, migrated %d/%d keys (skipped %d existing)',
        Object.keys(newEntries).length,
        Object.keys(entries).length,
        Object.keys(entries).length - Object.keys(newEntries).length
      );
    } else {
      console.info(
        '[Migration] configStorage migration skipped — all %d keys already exist in backend',
        Object.keys(entries).length
      );
    }
  }
}

export async function migrateLegacyMcpConfigToDb(configFile: ConfigFile): Promise<void> {
  const legacyConfigFile = configFile as LegacyMcpConfigFile;
  const backendPrefs = await fetchExistingClientKeys();
  const backendLegacy = backendPrefs[LEGACY_MCP_CONFIG_KEY];
  const fileLegacy = await legacyConfigFile.get(LEGACY_MCP_CONFIG_KEY).catch((): undefined => undefined);
  const legacyServers = Array.isArray(backendLegacy) ? backendLegacy : Array.isArray(fileLegacy) ? fileLegacy : [];

  if (legacyServers.length === 0) {
    console.info('[Migration] legacy MCP migration skipped — no legacy servers found');
    return;
  }

  const existing = await ipcBridge.mcpService.listServers.invoke();
  const existingNames = new Set((existing ?? []).map((server) => server.name));
  const importableServers = legacyServers.filter(isImportableMcpServer).map(normalizeLegacyMcpServer);
  const missing = importableServers.filter((server) => !existingNames.has(server.name));

  console.info(
    '[Migration] legacy MCP migration found %d servers, importing %d missing, skipping %d existing',
    legacyServers.length,
    missing.length,
    legacyServers.length - missing.length
  );

  if (missing.length > 0) {
    await ipcBridge.mcpService.batchImportServers.invoke({ servers: missing });
  }

  await setBackendClientPreferences({ [LEGACY_MCP_CONFIG_KEY]: null });
  await legacyConfigFile.set(LEGACY_MCP_CONFIG_KEY, []);
}

function isImportableMcpServer(
  server: unknown
): server is Partial<IMcpServer> & Pick<IMcpServer, 'name' | 'transport'> {
  if (!server || typeof server !== 'object') return false;
  const candidate = server as Partial<IMcpServer>;
  return typeof candidate.name === 'string' && candidate.name.length > 0 && Boolean(candidate.transport);
}

function normalizeLegacyMcpServer(
  server: Partial<IMcpServer> & Pick<IMcpServer, 'name' | 'transport'>
): Partial<IMcpServer> & Pick<IMcpServer, 'name' | 'transport'> {
  const isLegacyImageGen =
    server.builtin === true &&
    (server.id === BUILTIN_IMAGE_GEN_ID ||
      server.name === BUILTIN_IMAGE_GEN_NAME ||
      BUILTIN_IMAGE_GEN_LEGACY_NAMES.includes(server.name as (typeof BUILTIN_IMAGE_GEN_LEGACY_NAMES)[number]));

  if (!isLegacyImageGen) return server;

  return {
    ...server,
    name: BUILTIN_IMAGE_GEN_NAME,
    builtin: true,
  };
}

type BackendClientPreferences = Record<string, unknown>;

async function fetchExistingClientKeys(): Promise<Record<string, unknown>> {
  try {
    return (await httpRequest<Record<string, unknown>>('GET', '/api/settings/client')) || {};
  } catch {
    return {};
  }
}

async function setBackendClientPreferences(entries: BackendClientPreferences): Promise<void> {
  await httpRequest<void>('PUT', '/api/settings/client', entries);
}

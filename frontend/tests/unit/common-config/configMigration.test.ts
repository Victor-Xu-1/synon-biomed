/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 *
 * Unit tests for common/config/configMigration.ts.
 */

import { describe, it, expect, beforeEach, vi } from 'vitest';
// Phase 8 §8.5 gate: N3 helper must be imported by at least one N3 domain test.
// Consumed by the helper smoke-test block at the bottom; plan explicitly allows
// an extra demo test that is NOT counted against the T1-T6 clause.
import { createMockHttpBridge } from '../_helpers/mockHttpBridge';

// Mock dependencies BEFORE importing the module under test.
vi.mock('@/common/adapter/httpBridge', () => ({
  httpRequest: vi.fn(),
}));

vi.mock('@/common', () => ({ ipcBridge: {} }));

// Import after mocks are registered.
import { migrateConfigStorage, type ConfigFile } from '@/common/config/configMigration';
import { httpRequest } from '@/common/adapter/httpBridge';

describe('configMigration', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  describe('migrateConfigStorage', () => {
    it('skips migration when no keys are found', async () => {
      const configFile: ConfigFile = {
        get: vi.fn().mockRejectedValue(new Error('not found')),
        set: vi.fn(),
      };
      const infoSpy = vi.spyOn(console, 'info').mockImplementation(() => {});

      await migrateConfigStorage(configFile);

      expect(httpRequest).not.toHaveBeenCalledWith('PUT', expect.anything(), expect.anything());
      expect(infoSpy).toHaveBeenCalledWith(expect.stringContaining('skipped'));
      expect(configFile.set).not.toHaveBeenCalled();
    });

    it('collects multiple legacy keys and sends one PUT with merge strategy', async () => {
      const configFile: ConfigFile = {
        get: vi.fn((key: string) => {
          if (key === 'language') return Promise.resolve('zh-CN');
          if (key === 'theme') return Promise.resolve('dark');
          return Promise.reject(new Error('not found'));
        }),
        set: vi.fn(),
      };
      (httpRequest as ReturnType<typeof vi.fn>).mockImplementation((method: string) => {
        if (method === 'GET') return Promise.resolve({});
        return Promise.resolve(undefined);
      });
      vi.spyOn(console, 'info').mockImplementation(() => {});

      await migrateConfigStorage(configFile);

      expect(httpRequest).toHaveBeenCalledWith('PUT', '/api/settings/client', {
        language: 'zh-CN',
        theme: 'dark',
      });
      expect(configFile.set).not.toHaveBeenCalled();
    });

    it('skips keys that already exist in backend', async () => {
      const configFile: ConfigFile = {
        get: vi.fn((key: string) => {
          if (key === 'language') return Promise.resolve('en');
          if (key === 'theme') return Promise.resolve('dark');
          return Promise.reject(new Error('not found'));
        }),
        set: vi.fn(),
      };
      (httpRequest as ReturnType<typeof vi.fn>).mockImplementation((method: string) => {
        if (method === 'GET') return Promise.resolve({ theme: 'light' });
        return Promise.resolve(undefined);
      });
      vi.spyOn(console, 'info').mockImplementation(() => {});

      await migrateConfigStorage(configFile);

      expect(httpRequest).toHaveBeenCalledWith('PUT', '/api/settings/client', {
        language: 'en',
      });
    });

    it('ignores null values', async () => {
      const configFile: ConfigFile = {
        get: vi.fn((key: string) => {
          if (key === 'language') return Promise.resolve('en');
          if (key === 'theme') return Promise.resolve(null);
          return Promise.reject(new Error('not found'));
        }),
        set: vi.fn(),
      };
      (httpRequest as ReturnType<typeof vi.fn>).mockImplementation((method: string) => {
        if (method === 'GET') return Promise.resolve({});
        return Promise.resolve(undefined);
      });
      vi.spyOn(console, 'info').mockImplementation(() => {});

      await migrateConfigStorage(configFile);

      const putCall = (httpRequest as ReturnType<typeof vi.fn>).mock.calls.find((c: unknown[]) => c[0] === 'PUT');
      expect(putCall?.[2]).toEqual({ language: 'en' });
      expect(putCall?.[2]).not.toHaveProperty('theme');
    });

    it('handles configFile.get exceptions by skipping those keys', async () => {
      const configFile: ConfigFile = {
        get: vi.fn((key: string) => {
          if (key === 'language') return Promise.resolve('en');
          throw new Error('access error');
        }),
        set: vi.fn(),
      };
      (httpRequest as ReturnType<typeof vi.fn>).mockImplementation((method: string) => {
        if (method === 'GET') return Promise.resolve({});
        return Promise.resolve(undefined);
      });
      vi.spyOn(console, 'info').mockImplementation(() => {});

      await migrateConfigStorage(configFile);

      expect(httpRequest).toHaveBeenCalledWith('PUT', '/api/settings/client', {
        language: 'en',
      });
    });

    it('does not probe removed ACP cache keys during migration', async () => {
      const configFile: ConfigFile = {
        get: vi.fn((key: string) => {
          if (key === 'language') return Promise.resolve('en');
          return Promise.reject(new Error('not found'));
        }),
        set: vi.fn(),
      };
      (httpRequest as ReturnType<typeof vi.fn>).mockImplementation((method: string) => {
        if (method === 'GET') return Promise.resolve({});
        return Promise.resolve(undefined);
      });
      vi.spyOn(console, 'info').mockImplementation(() => {});

      await migrateConfigStorage(configFile);

      expect(configFile.get).not.toHaveBeenCalledWith('acp.cachedInitializeResult');
      expect(configFile.get).not.toHaveBeenCalledWith('acp.cached_config_options');
      expect(configFile.get).not.toHaveBeenCalledWith('acp.cachedModes');
    });

    it('does not probe removed ACP/Codex legacy config blobs during migration', async () => {
      const configFile: ConfigFile = {
        get: vi.fn((key: string) => {
          if (key === 'language') return Promise.resolve('en');
          return Promise.reject(new Error('not found'));
        }),
        set: vi.fn(),
      };
      (httpRequest as ReturnType<typeof vi.fn>).mockImplementation((method: string) => {
        if (method === 'GET') return Promise.resolve({});
        return Promise.resolve(undefined);
      });
      vi.spyOn(console, 'info').mockImplementation(() => {});

      await migrateConfigStorage(configFile);

      expect(configFile.get).not.toHaveBeenCalledWith('acp.config');
      expect(configFile.get).not.toHaveBeenCalledWith('codex.config');
    });
  });

  describe('mockHttpBridge helper reachability (Phase 8 §8.5 smoke)', () => {
    it('createMockHttpBridge exposes the frozen public API surface', () => {
      const mock = createMockHttpBridge({ unmatched: 'warn' });
      expect(typeof mock.onGet).toBe('function');
      expect(typeof mock.onPost).toBe('function');
      expect(typeof mock.emit).toBe('function');
      expect(typeof mock.reset).toBe('function');
      expect(typeof mock.asModule).toBe('function');
      expect(mock.routeCount).toBe(0);
      expect(mock.wsListenerCount).toBe(0);
    });
  });
});

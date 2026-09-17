/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  getClientBusinessSetting: vi.fn(),
  loadLegacySynonBiomedMcpServers: vi.fn(),
  listServers: vi.fn(),
}));

vi.mock('@/renderer/services/clientBusinessSettings', () => ({
  getClientBusinessSetting: mocks.getClientBusinessSetting,
}));

vi.mock('@/common/adapter/ipcBridge', () => ({
  mcpService: {
    listServers: { invoke: mocks.listServers },
  },
}));

vi.mock('@/renderer/services/synonBiomedCatalog', () => ({
  loadSynonBiomedMcpServers: mocks.loadLegacySynonBiomedMcpServers,
  mergeSynonBiomedMcpServers: (_baseServers: unknown[], synonBiomedServers: unknown[]) => synonBiomedServers,
}));

import { ensureBackendMcpCatalog } from '@/renderer/hooks/mcp/catalog';

describe('ensureBackendMcpCatalog', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.getClientBusinessSetting.mockResolvedValue([
      {
        id: 'fake-synon-builtin',
        name: 'pubmed',
        enabled: true,
        transport: { type: 'stdio', command: 'python', args: ['/runtime/fake.py'] },
        created_at: 0,
        updated_at: 0,
        original_json: '{}',
        builtin: true,
      },
    ]);
    mocks.loadLegacySynonBiomedMcpServers.mockResolvedValue([
      {
        id: 'synonbiomed:mcp:pubmed',
        name: 'pubmed',
        enabled: true,
        transport: { type: 'stdio', command: 'python', args: ['/runtime/fake.py'] },
        created_at: 0,
        updated_at: 0,
        original_json: '{}',
        builtin: true,
      },
    ]);
    mocks.listServers.mockResolvedValue([
      {
        id: 'user-1',
        name: 'user one',
        enabled: true,
        transport: { type: 'stdio', command: 'user', args: [] },
        created_at: 2,
        updated_at: 2,
        original_json: '{}',
        builtin: false,
      },
    ]);
  });

  it('does not mirror backend-owned Synon Biomed connectors into SynonAI local MCP transports', async () => {
    const result = await ensureBackendMcpCatalog();

    expect(result.userServers.map((server) => server.id)).toEqual(['user-1']);
    expect(result.builtinServers).toEqual([]);
    expect(result.allServers.map((server) => server.id)).toEqual(['user-1']);
    expect(mocks.getClientBusinessSetting).not.toHaveBeenCalled();
    expect(mocks.loadLegacySynonBiomedMcpServers).not.toHaveBeenCalled();
  });
});

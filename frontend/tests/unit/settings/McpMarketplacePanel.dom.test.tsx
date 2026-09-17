import { fireEvent, screen, waitFor } from '@testing-library/react';
import { Message } from '@arco-design/web-react';
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { McpMarketplacePanel } from '@/renderer/pages/settings/ToolsSettings/McpMarketplacePanel';
import { renderWithI18n } from '../i18nTestUtils';

const mocks = vi.hoisted(() => ({
  createServer: vi.fn(),
}));

vi.mock('@/renderer/services/synonBiomedCapabilities', () => ({
  createSynonBiomedCustomMcpServer: mocks.createServer,
}));

describe('McpMarketplacePanel', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.spyOn(Message, 'success').mockReturnValue(() => undefined);
    mocks.createServer.mockResolvedValue({ id: 'custom:pubmed' });
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input));
        expect(url.origin).toBe(globalThis.location.origin);
        expect(url.pathname).toBe('/api/mcp-servers/marketplace');
        return new Response(
          JSON.stringify({
            servers: [
              {
                server: {
                  name: 'com.example/pubmed',
                  title: 'PubMed MCP',
                  description: 'Biomedical literature search.',
                  remotes: [{ type: 'streamable-http', url: 'https://mcp.example.com/pubmed' }],
                },
              },
            ],
          }),
          { status: 200 }
        );
      })
    );
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('loads a real registry result and adds a safe HTTPS connector through the existing service', async () => {
    const onInstalled = vi.fn(async () => undefined);
    await renderWithI18n(<McpMarketplacePanel onInstalled={onInstalled} />, 'zh-CN');

    expect(await screen.findByText('PubMed MCP')).toBeInTheDocument();
    expect(screen.getByTestId('synon-biomed-mcp-marketplace-grid')).toHaveClass(
      'grid-cols-1',
      'xl:grid-cols-3',
      '2xl:grid-cols-4'
    );
    expect(screen.getByRole('listitem')).toHaveClass('synon-mcp-card');
    expect(screen.getByRole('listitem')).toHaveTextContent('未添加');
    expect(screen.getByRole('listitem')).toHaveTextContent('尚未使用');
    expect(screen.getByRole('listitem')).toHaveTextContent('调用 0 次');
    fireEvent.click(screen.getByRole('button', { name: '添加到工作区' }));

    await waitFor(() =>
      expect(mocks.createServer).toHaveBeenCalledWith({
        name: 'pubmed',
        description: 'PubMed MCP · Biomedical literature search.',
        url: 'https://mcp.example.com/pubmed',
        transport: 'streamable_http',
      })
    );
    await waitFor(() => expect(onInstalled).toHaveBeenCalledTimes(1));
  });
});

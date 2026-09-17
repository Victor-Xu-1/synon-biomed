import React from 'react';
import { act, fireEvent, render, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import SynonBiomedMcpAppViewer from '@/renderer/pages/conversation/Preview/components/viewers/SynonBiomedMcpAppViewer';
import SynonBiomedMcpAppArtifactViewer from '@/renderer/pages/conversation/Preview/components/viewers/SynonBiomedMcpAppArtifactViewer';
import {
  createSynonBiomedMcpAppResourceTicket,
  pinSynonBiomedMcpAppArtifact,
  pollSynonBiomedMcpAppRequest,
  registerSynonBiomedMcpApp,
  resolveSynonBiomedMcpAppRequest,
  unregisterSynonBiomedMcpApp,
} from '@/renderer/services/mcp/synonBiomedMcpApps';

vi.mock('@/renderer/components/media/PreviewLoadingState', () => ({
  default: ({ label }: { label: string }) => <div data-testid='loading'>{label}</div>,
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock('@/renderer/services/mcp/synonBiomedMcpApps', async (loadOriginal) => {
  const original = await loadOriginal<typeof import('@/renderer/services/mcp/synonBiomedMcpApps')>();
  return {
    ...original,
    createSynonBiomedMcpAppResourceTicket: vi.fn(),
    pinSynonBiomedMcpAppArtifact: vi.fn(),
    pollSynonBiomedMcpAppRequest: vi.fn(),
    registerSynonBiomedMcpApp: vi.fn(),
    resolveSynonBiomedMcpAppRequest: vi.fn(),
    unregisterSynonBiomedMcpApp: vi.fn(),
  };
});

describe('Synon Biomed MCP App viewer', () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.clearAllMocks();
  });

  const prepareRegistration = () => {
    vi.mocked(registerSynonBiomedMcpApp).mockResolvedValue({
      registrationId: 'registration-1',
      artifactId: 'artifact-1',
      expiresAt: '2026-08-03T12:00:00Z',
    });
    vi.mocked(pollSynonBiomedMcpAppRequest).mockImplementation((_registrationId, signal) => {
      return new Promise((resolve) => signal?.addEventListener('abort', () => resolve(null), { once: true }));
    });
    vi.mocked(unregisterSynonBiomedMcpApp).mockResolvedValue();
  };

  it('mounts on the isolated origin and accepts messages only from the exact iframe generation', async () => {
    vi.mocked(createSynonBiomedMcpAppResourceTicket).mockResolvedValue({
      url: 'http://mcp-app.localhost:38180/mcp-app-resource?ticket=test-ticket',
      origin: 'http://mcp-app.localhost:38180',
      expiresAt: '2026-07-27T12:00:00Z',
      toolInput: { ket: '{"root":{"nodes":[]}}', filename: 'empty.ket' },
      toolResult: {
        content: [{ type: 'text', text: 'Opened molecule sketcher.' }],
        structuredContent: { ket: '{"root":{"nodes":[]}}' },
      },
    });
    prepareRegistration();

    const view = render(
      <SynonBiomedMcpAppViewer
        serverId='bundled:ketcher-chemistry'
        serverName='Ketcher Chemistry'
        openTool='open_sketcher'
        resourceUri='ui://ketcher-chemistry/editor'
        rootFrameId='root-1'
        artifactId='artifact-1'
        initialArguments={{
          ket: '{"root":{"nodes":[]}}',
          filename: 'empty.ket',
        }}
      />
    );
    const iframe = await waitFor(() => {
      const element = view.container.querySelector('iframe');
      expect(element).toBeTruthy();
      return element as HTMLIFrameElement;
    });
    expect(iframe.getAttribute('sandbox')).toBe('allow-scripts allow-same-origin');
    expect(iframe.getAttribute('allow')).toContain("camera 'none'");
    expect(view.queryByText('preview.scientific.mcpApp.readOnly')).toBeNull();
    await waitFor(() =>
      expect(iframe.getAttribute('src')).toBe('http://mcp-app.localhost:38180/mcp-app-resource?ticket=test-ticket')
    );
    if (!iframe.contentWindow) throw new Error('test iframe did not create a contentWindow');
    const postMessage = vi.spyOn(iframe.contentWindow, 'postMessage');

    act(() => {
      window.dispatchEvent(
        new MessageEvent('message', {
          source: iframe.contentWindow,
          origin: 'http://attacker.test',
          data: {
            jsonrpc: '2.0',
            id: 1,
            method: 'ui/initialize',
            params: { protocolVersion: '2026-01-26' },
          },
        })
      );
      window.dispatchEvent(
        new MessageEvent('message', {
          source: window,
          origin: 'http://mcp-app.localhost:38180',
          data: {
            jsonrpc: '2.0',
            id: 2,
            method: 'ui/initialize',
            params: { protocolVersion: '2026-01-26' },
          },
        })
      );
    });
    expect(postMessage).not.toHaveBeenCalled();

    act(() => {
      window.dispatchEvent(
        new MessageEvent('message', {
          source: iframe.contentWindow,
          origin: 'http://mcp-app.localhost:38180',
          data: {
            jsonrpc: '2.0',
            id: 3,
            method: 'ui/initialize',
            params: { protocolVersion: '2026-01-26' },
          },
        })
      );
      window.dispatchEvent(
        new MessageEvent('message', {
          source: iframe.contentWindow,
          origin: 'http://mcp-app.localhost:38180',
          data: { jsonrpc: '2.0', method: 'ui/notifications/initialized' },
        })
      );
    });

    expect(postMessage).toHaveBeenCalledWith(
      expect.objectContaining({
        id: 3,
        result: expect.objectContaining({
          hostInfo: { name: 'Synon Biomed', version: '0.1.1' },
        }),
      }),
      'http://mcp-app.localhost:38180'
    );
    expect(postMessage).toHaveBeenCalledWith(
      {
        jsonrpc: '2.0',
        method: 'ui/notifications/tool-input',
        params: { arguments: expect.any(Object) },
      },
      'http://mcp-app.localhost:38180'
    );
    expect(postMessage).toHaveBeenCalledWith(
      expect.objectContaining({
        jsonrpc: '2.0',
        method: 'ui/notifications/tool-result',
      }),
      'http://mcp-app.localhost:38180'
    );
    const toolsListRequest = postMessage.mock.calls
      .map(([message]) => message as { id?: string; method?: string })
      .find((message) => message.method === 'tools/list');
    expect(toolsListRequest?.id).toBeTruthy();
    act(() => {
      window.dispatchEvent(
        new MessageEvent('message', {
          source: iframe.contentWindow,
          origin: 'http://mcp-app.localhost:38180',
          data: {
            jsonrpc: '2.0',
            id: toolsListRequest?.id,
            result: {
              tools: [
                {
                  name: 'get_structure',
                  description: 'Get structure',
                  inputSchema: { type: 'object' },
                },
              ],
            },
          },
        })
      );
    });
    await waitFor(() => expect(registerSynonBiomedMcpApp).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(view.getByTestId('mcp-app-viewer')).toHaveAttribute('data-state', 'ready'));

    view.unmount();
    await waitFor(() => expect(unregisterSynonBiomedMcpApp).toHaveBeenCalledWith('registration-1'));
    const calls = postMessage.mock.calls.length;
    act(() => {
      window.dispatchEvent(
        new MessageEvent('message', {
          source: iframe.contentWindow,
          origin: 'http://mcp-app.localhost:38180',
          data: { jsonrpc: '2.0', id: 4, method: 'ping' },
        })
      );
    });
    expect(postMessage).toHaveBeenCalledTimes(calls);
  });

  it('installs the exact receiver before iframe navigation can initialize synchronously', async () => {
    vi.mocked(createSynonBiomedMcpAppResourceTicket).mockResolvedValue({
      url: 'http://mcp-app.localhost:38180/mcp-app-resource?ticket=early-ticket',
      origin: 'http://mcp-app.localhost:38180',
      expiresAt: '2026-07-27T12:00:00Z',
      toolInput: {},
      toolResult: { content: [], structuredContent: {} },
    });
    const originalSrc = Object.getOwnPropertyDescriptor(HTMLIFrameElement.prototype, 'src');
    let earlyPostMessage: ReturnType<typeof vi.spyOn> | null = null;
    Object.defineProperty(HTMLIFrameElement.prototype, 'src', {
      configurable: true,
      get() {
        return this.getAttribute('src') ?? '';
      },
      set(value: string) {
        this.setAttribute('src', value);
        if (!value.startsWith('http://mcp-app.localhost:38180/')) return;
        const target = this.contentWindow;
        if (!target) throw new Error('test iframe did not create a contentWindow');
        earlyPostMessage = vi.spyOn(target, 'postMessage');
        window.dispatchEvent(
          new MessageEvent('message', {
            source: target,
            origin: 'http://mcp-app.localhost:38180',
            data: {
              jsonrpc: '2.0',
              id: 'early-init',
              method: 'ui/initialize',
              params: { protocolVersion: '2026-01-26' },
            },
          })
        );
      },
    });
    try {
      render(
        <SynonBiomedMcpAppViewer
          serverId='bundled:ketcher-chemistry'
          serverName='Ketcher Chemistry'
          openTool='open_sketcher'
          resourceUri='ui://ketcher-chemistry/editor'
          initialArguments={{}}
        />
      );
      await waitFor(() =>
        expect(earlyPostMessage).toHaveBeenCalledWith(
          expect.objectContaining({
            id: 'early-init',
            result: expect.any(Object),
          }),
          'http://mcp-app.localhost:38180'
        )
      );
    } finally {
      if (originalSrc) Object.defineProperty(HTMLIFrameElement.prototype, 'src', originalSrc);
    }
  });

  it('relays an authenticated broker call to the exact iframe and returns its bounded result', async () => {
    vi.mocked(createSynonBiomedMcpAppResourceTicket).mockResolvedValue({
      url: 'http://mcp-app.localhost:38180/mcp-app-resource?ticket=test-ticket',
      origin: 'http://mcp-app.localhost:38180',
      expiresAt: '2026-07-27T12:00:00Z',
      toolInput: {},
      toolResult: { content: [], structuredContent: {} },
    });
    vi.mocked(registerSynonBiomedMcpApp).mockResolvedValue({
      registrationId: 'registration-1',
      artifactId: 'artifact-1',
      expiresAt: '2026-08-03T12:00:00Z',
    });
    vi.mocked(pollSynonBiomedMcpAppRequest)
      .mockResolvedValueOnce({
        requestId: 'request-1',
        serverId: 'bundled:ketcher-chemistry',
        serverName: 'Ketcher Chemistry',
        artifactId: 'artifact-1',
        tool: 'get_structure',
        arguments: {},
      })
      .mockImplementation((_registrationId, signal) => {
        return new Promise((resolve) => signal?.addEventListener('abort', () => resolve(null), { once: true }));
      });
    vi.mocked(resolveSynonBiomedMcpAppRequest).mockResolvedValue();
    vi.mocked(unregisterSynonBiomedMcpApp).mockResolvedValue();
    const view = render(
      <SynonBiomedMcpAppViewer
        serverId='bundled:ketcher-chemistry'
        serverName='Ketcher Chemistry'
        openTool='open_sketcher'
        resourceUri='ui://ketcher-chemistry/editor'
        rootFrameId='root-1'
        artifactId='artifact-1'
        initialArguments={{}}
      />
    );
    const iframe = await waitFor(() => {
      const element = view.container.querySelector('iframe');
      expect(element).toBeTruthy();
      return element as HTMLIFrameElement;
    });
    if (!iframe.contentWindow) throw new Error('test iframe did not create a contentWindow');
    await waitFor(() => expect(iframe.src).toBe('http://mcp-app.localhost:38180/mcp-app-resource?ticket=test-ticket'));
    const postMessage = vi.spyOn(iframe.contentWindow, 'postMessage');
    act(() => {
      window.dispatchEvent(
        new MessageEvent('message', {
          source: iframe.contentWindow,
          origin: 'http://mcp-app.localhost:38180',
          data: {
            jsonrpc: '2.0',
            id: 'init-1',
            method: 'ui/initialize',
            params: { protocolVersion: '2026-01-26' },
          },
        })
      );
      window.dispatchEvent(
        new MessageEvent('message', {
          source: iframe.contentWindow,
          origin: 'http://mcp-app.localhost:38180',
          data: { jsonrpc: '2.0', method: 'ui/notifications/initialized' },
        })
      );
    });
    const toolsListRequest = postMessage.mock.calls
      .map(([message]) => message as { id?: string; method?: string })
      .find((message) => message.method === 'tools/list');
    act(() => {
      window.dispatchEvent(
        new MessageEvent('message', {
          source: iframe.contentWindow,
          origin: 'http://mcp-app.localhost:38180',
          data: {
            jsonrpc: '2.0',
            id: toolsListRequest?.id,
            result: {
              tools: [{ name: 'get_structure', inputSchema: { type: 'object' } }],
            },
          },
        })
      );
    });
    const toolCall = await waitFor(() => {
      const call = postMessage.mock.calls
        .map(([message]) => message as { id?: string; method?: string })
        .find((message) => message.method === 'tools/call');
      expect(call).toBeTruthy();
      return call;
    });
    act(() => {
      window.dispatchEvent(
        new MessageEvent('message', {
          source: iframe.contentWindow,
          origin: 'http://mcp-app.localhost:38180',
          data: {
            jsonrpc: '2.0',
            id: toolCall.id,
            result: {
              content: [{ type: 'text', text: 'ok' }],
              structuredContent: { ket: '{"root":{}}' },
            },
          },
        })
      );
    });
    await waitFor(() =>
      expect(resolveSynonBiomedMcpAppRequest).toHaveBeenCalledWith(
        'registration-1',
        'request-1',
        expect.objectContaining({ structuredContent: { ket: '{"root":{}}' } })
      )
    );
    view.unmount();
  });

  it('saves edited Ketcher content through get_structure and the durable pin endpoint', async () => {
    vi.mocked(createSynonBiomedMcpAppResourceTicket).mockResolvedValue({
      url: 'http://mcp-app.localhost:38180/mcp-app-resource?ticket=save-ticket',
      origin: 'http://mcp-app.localhost:38180',
      expiresAt: '2026-08-03T12:00:00Z',
      toolInput: { ket: '{"root":{"nodes":[]}}', filename: 'molecule.ket' },
      toolResult: {
        content: [],
        structuredContent: { ket: '{"root":{"nodes":[]}}' },
      },
    });
    prepareRegistration();
    vi.mocked(pinSynonBiomedMcpAppArtifact).mockResolvedValue({
      artifactId: 'saved-artifact',
      versionId: 'saved-version',
      filename: 'molecule.ket',
    });
    const view = render(
      <SynonBiomedMcpAppViewer
        serverId='bundled:ketcher-chemistry'
        serverName='Ketcher Chemistry'
        openTool='open_sketcher'
        resourceUri='ui://ketcher-chemistry/editor'
        rootFrameId='root-1'
        frameId='frame-1'
        artifactId='artifact-1'
        initialArguments={{
          ket: '{"root":{"nodes":[]}}',
          filename: 'molecule.ket',
        }}
        saveContract={{
          appTool: 'get_structure',
          resultField: 'ket',
          mimeType: 'application/json',
          extension: '.ket',
          filenameStem: 'sketcher',
          hasChangeField: 'has_change',
        }}
      />
    );
    const iframe = await waitFor(() => {
      const element = view.container.querySelector('iframe');
      expect(element).toBeTruthy();
      return element as HTMLIFrameElement;
    });
    if (!iframe.contentWindow) throw new Error('test iframe did not create a contentWindow');
    await waitFor(() => expect(iframe.src).toContain('save-ticket'));
    const postMessage = vi.spyOn(iframe.contentWindow, 'postMessage');
    act(() => {
      window.dispatchEvent(
        new MessageEvent('message', {
          source: iframe.contentWindow,
          origin: 'http://mcp-app.localhost:38180',
          data: {
            jsonrpc: '2.0',
            id: 'init',
            method: 'ui/initialize',
            params: { protocolVersion: '2026-01-26' },
          },
        })
      );
      window.dispatchEvent(
        new MessageEvent('message', {
          source: iframe.contentWindow,
          origin: 'http://mcp-app.localhost:38180',
          data: { jsonrpc: '2.0', method: 'ui/notifications/initialized' },
        })
      );
    });
    const toolsListRequest = postMessage.mock.calls
      .map(([message]) => message as { id?: string; method?: string })
      .find((message) => message.method === 'tools/list');
    act(() => {
      window.dispatchEvent(
        new MessageEvent('message', {
          source: iframe.contentWindow,
          origin: 'http://mcp-app.localhost:38180',
          data: {
            jsonrpc: '2.0',
            id: toolsListRequest?.id,
            result: {
              tools: [{ name: 'get_structure', inputSchema: { type: 'object' } }],
            },
          },
        })
      );
      window.dispatchEvent(
        new MessageEvent('message', {
          source: iframe.contentWindow,
          origin: 'http://mcp-app.localhost:38180',
          data: {
            jsonrpc: '2.0',
            method: 'ui/update-model-context',
            params: { has_change: true },
          },
        })
      );
    });
    await waitFor(() => expect(view.getByRole('button', { name: 'preview.scientific.mcpApp.save' })).toBeEnabled());
    fireEvent.click(view.getByRole('button', { name: 'preview.scientific.mcpApp.save' }));
    const saveCall = await waitFor(() => {
      const call = postMessage.mock.calls
        .map(([message]) => message as { id?: string; method?: string })
        .find((message) => message.method === 'tools/call');
      expect(call).toBeTruthy();
      return call;
    });
    act(() => {
      window.dispatchEvent(
        new MessageEvent('message', {
          source: iframe.contentWindow,
          origin: 'http://mcp-app.localhost:38180',
          data: {
            jsonrpc: '2.0',
            id: saveCall.id,
            result: {
              content: [],
              structuredContent: {
                ket: '{"root":{"nodes":[{"type":"atom"}]}}',
              },
            },
          },
        })
      );
    });
    await waitFor(() =>
      expect(pinSynonBiomedMcpAppArtifact).toHaveBeenCalledWith(
        expect.objectContaining({
          rootFrameId: 'root-1',
          frameId: 'frame-1',
          filename: 'molecule.ket',
          content: '{"root":{"nodes":[{"type":"atom"}]}}',
          tool: 'get_structure',
        })
      )
    );
  });

  it('stops an underreported artifact stream at the real 2 MiB boundary before mounting the app', async () => {
    const chunk = new Uint8Array(1024 * 1024);
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(
        new ReadableStream<Uint8Array>({
          start(controller) {
            controller.enqueue(chunk);
            controller.enqueue(chunk);
            controller.enqueue(new Uint8Array([1]));
            controller.close();
          },
        }),
        { status: 200, headers: { 'content-length': '16' } }
      )
    );
    const view = render(
      <SynonBiomedMcpAppArtifactViewer filename='too-large.ket' contentUrl='/artifact/content' contentParam='ket' />
    );
    await waitFor(() => expect(view.getByRole('alert')).toBeTruthy());
    expect(createSynonBiomedMcpAppResourceTicket).not.toHaveBeenCalled();
  });

  it('rejects an oversized inline .ket preview before requesting a resource ticket', async () => {
    const view = render(
      <SynonBiomedMcpAppViewer
        serverId='bundled:ketcher-chemistry'
        serverName='Ketcher Chemistry'
        openTool='open_sketcher'
        resourceUri='ui://ketcher-chemistry/editor'
        initialArguments={{
          ket: 'x'.repeat(2 * 1024 * 1024 + 1),
          filename: 'too-large.ket',
        }}
      />
    );
    await waitFor(() => expect(view.getByRole('alert')).toHaveTextContent('preview.scientific.mcpApp.inputTooLarge'));
    expect(createSynonBiomedMcpAppResourceTicket).not.toHaveBeenCalled();
  });
});

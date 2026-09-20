import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { Message } from '@arco-design/web-react';
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { SynonBiomedMcpSettingsContent } from '@/renderer/pages/settings/ToolsSettings/SynonBiomedMcpSettings';
import { renderWithI18n } from '../i18nTestUtils';

describe('SynonBiomedMcpSettingsContent', () => {
  beforeEach(() => {
    const originalConsoleError = console.error;
    vi.spyOn(console, 'error').mockImplementation((...args: unknown[]) => {
      if (typeof args[0] === 'string' && args[0].startsWith('Accessing element.ref was removed in React 19.')) {
        return;
      }
      originalConsoleError(...args);
    });
    vi.spyOn(Message, 'success').mockReturnValue(() => undefined);
    vi.spyOn(Message, 'error').mockReturnValue(() => undefined);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('renders the reference-style connector cards while preserving operational detail accessibly', async () => {
    const fetchMock = vi.fn(async (input: string) => {
      if (input === '/api/mcp-servers/directory-health') {
        return new Response(JSON.stringify({ directoryHealth: { ok: true } }), { status: 200 });
      }
      return new Response(
        JSON.stringify([
          {
            id: 'bundled:pubmed',
            name: 'pubmed',
            displayName: 'PubMed',
            description: 'PubMed — Biomedical literature search and metadata.',
            source: 'bundled',
            authState: 'not-required',
            transport: 'stdio',
            upstreams: [{ name: 'NCBI PubMed / PMC' }],
            hostedBySynon: false,
            health: { ok: true },
            attachedAgents: ['OPERON'],
            enabled: true,
            connectionStatus: 'connected',
            usage: { invocationCount: 4, lastUsedAt: '2026-08-17T10:20:00Z' },
          },
          {
            id: 'bundled:ketcher-chemistry',
            name: 'ketcher-chemistry',
            displayName: 'Ketcher Chemistry',
            description: 'Interactive molecule sketcher.',
            source: 'bundled',
            authState: 'not-required',
            transport: 'stdio',
            upstreams: [],
            hostedBySynon: false,
            health: { ok: true },
            attachedAgents: ['OPERON'],
            enabled: true,
            connectionStatus: 'connected',
            usage: { invocationCount: 0, lastUsedAt: null },
          },
        ]),
        { status: 200, headers: { 'content-type': 'application/json' } }
      );
    });
    vi.stubGlobal('fetch', fetchMock);

    await renderWithI18n(<SynonBiomedMcpSettingsContent />, 'en-US');

    await waitFor(() => expect(screen.getByText('PubMed')).toBeInTheDocument());
    expect(screen.getByText('Ketcher Chemistry')).toBeInTheDocument();
    const pubmedCardElement = screen.getByTestId('synon-biomed-mcp-pubmed');
    const ketcherCardElement = screen.getByTestId('synon-biomed-mcp-ketcher-chemistry');
    const pubmedCard = within(pubmedCardElement);
    const ketcherCard = within(ketcherCardElement);
    expect(pubmedCardElement).toHaveAttribute('data-mcp-visual', 'pubmed');
    expect(ketcherCardElement).toHaveAttribute('data-mcp-visual', 'ketcher-chemistry');
    expect(pubmedCardElement.querySelector('.mcp-connector-visual--artwork')).toBeNull();
    expect(screen.getByTestId('synon-biomed-mcp-grid')).toHaveClass('synon-mcp-grid');
    expect(pubmedCard.getByText('Connected')).toBeInTheDocument();
    expect(pubmedCard.getByTestId('synon-biomed-mcp-permissions-pubmed')).toHaveTextContent('Configure');
    expect(pubmedCard.getByText('PubMed — Biomedical literature search and metadata.')).toBeInTheDocument();
    expect(ketcherCard.getByText('Connected')).toBeInTheDocument();
    expect(pubmedCard.queryByText('No authentication')).toBeNull();
    expect(ketcherCard.queryByText('No authentication')).toBeNull();
    expect(pubmedCard.queryByText('NCBI PubMed / PMC')).toBeNull();
    expect(pubmedCard.queryByTestId('synon-biomed-mcp-usage-pubmed')).toBeNull();
    expect(ketcherCard.queryByTestId('synon-biomed-mcp-usage-ketcher-chemistry')).toBeNull();
    expect(pubmedCardElement.getAttribute('data-mcp-usage-summary')).toContain('4 calls');
    expect(pubmedCardElement.getAttribute('aria-label')).toContain('No authentication');
    expect(pubmedCardElement.getAttribute('aria-label')).toContain('NCBI PubMed / PMC');
    expect(pubmedCardElement.getAttribute('aria-label')).toContain('Last used');
    expect(ketcherCardElement.getAttribute('aria-label')).toContain('Not used yet');
    expect(screen.queryByText(/python|node|run_server\.py|server\.js/i)).toBeNull();
    expect(screen.queryByText(/Add via chat|Import from JSON|One-click import|Image Generation/i)).toBeNull();
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/mcp-servers/connectors',
      expect.objectContaining({ headers: { Accept: 'application/json' } })
    );
    expect(screen.getByTestId('synon-biomed-mcp-directory-health')).toHaveTextContent('Connected 2/2');
    expect(screen.getByTestId('synon-biomed-mcp-directory-health')).not.toHaveTextContent('Directory healthy');
  });

  it('refreshes a new owner until bundled connector initialization finishes', async () => {
    let connectorLoads = 0;
    const fetchMock = vi.fn(async (input: string) => {
      if (input === '/api/mcp-servers/directory-health') {
        return new Response(JSON.stringify({ directoryHealth: { ok: true } }), { status: 200 });
      }
      if (input === '/api/mcp-servers/connectors') {
        connectorLoads += 1;
        const connected = connectorLoads > 1;
        return new Response(
          JSON.stringify([
            {
              id: 'bundled:pubmed',
              name: 'pubmed',
              displayName: 'PubMed',
              description: 'Biomedical literature search.',
              source: 'bundled',
              authState: 'not-required',
              transport: 'stdio',
              upstreams: [],
              health: { ok: connected },
              attachedAgents: ['OPERON'],
              enabled: true,
              connectionStatus: connected ? 'connected' : 'connecting',
            },
          ]),
          { status: 200 }
        );
      }
      return new Response(JSON.stringify([]), { status: 200 });
    });
    vi.stubGlobal('fetch', fetchMock);

    await renderWithI18n(<SynonBiomedMcpSettingsContent />, 'en-US');

    await waitFor(
      () => expect(screen.getByTestId('synon-biomed-mcp-directory-health')).toHaveTextContent('Connected 1/1'),
      { timeout: 2500 }
    );
    expect(connectorLoads).toBe(2);
  });

  it('disables a connector through the real lifecycle endpoint and reloads state', async () => {
    let enabled = true;
    const fetchMock = vi.fn(async (input: string, init?: RequestInit) => {
      if (input === '/api/mcp-servers/directory-health') {
        return new Response(JSON.stringify({ directoryHealth: { ok: true } }), { status: 200 });
      }
      if (input === '/api/mcp-servers/connectors/bundled%3Apubmed/enabled') {
        enabled = JSON.parse(String(init?.body)).enabled as boolean;
        return new Response(JSON.stringify({ enabled }), { status: 200 });
      }
      return new Response(
        JSON.stringify([
          {
            id: 'bundled:pubmed',
            name: 'pubmed',
            displayName: 'PubMed',
            description: 'Biomedical literature search.',
            source: 'bundled',
            authState: 'not-required',
            transport: 'stdio',
            upstreams: [],
            health: { ok: enabled },
            attachedAgents: ['OPERON'],
            enabled,
            connectionStatus: enabled ? 'connected' : 'disabled',
          },
        ]),
        { status: 200 }
      );
    });
    vi.stubGlobal('fetch', fetchMock);
    await renderWithI18n(<SynonBiomedMcpSettingsContent />, 'en-US');

    const toggle = await screen.findByTestId('synon-biomed-mcp-enabled-pubmed');
    fireEvent.click(toggle);

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/mcp-servers/connectors/bundled%3Apubmed/enabled',
        expect.objectContaining({ method: 'PUT', body: '{"enabled":false}' })
      )
    );
    await waitFor(() =>
      expect(within(screen.getByTestId('synon-biomed-mcp-pubmed')).getByText('Disabled')).toBeVisible()
    );
  });

  it('runs backend reconcile and refreshes directory health and connector inventory', async () => {
    const fetchMock = vi.fn(async (input: string) => {
      if (input === '/api/mcp-servers/directory-health') {
        return new Response(JSON.stringify({ directoryHealth: { ok: true } }), { status: 200 });
      }
      if (input === '/api/mcp-servers/reconcile') {
        return new Response(JSON.stringify({ reconciled: 1 }), { status: 200 });
      }
      return new Response(JSON.stringify([]), { status: 200 });
    });
    vi.stubGlobal('fetch', fetchMock);
    await renderWithI18n(<SynonBiomedMcpSettingsContent />, 'en-US');

    fireEvent.click(await screen.findByTestId('synon-biomed-mcp-reconcile'));

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith('/api/mcp-servers/reconcile', expect.objectContaining({ method: 'POST' }))
    );
    await waitFor(() =>
      expect(fetchMock.mock.calls.filter(([path]) => path === '/api/mcp-servers/connectors')).toHaveLength(2)
    );
  });

  it('loads real tool permissions and writes a deny grant', async () => {
    const fetchMock = vi.fn(async (input: string, init?: RequestInit) => {
      if (input === '/api/mcp-servers/directory-health') {
        return new Response(JSON.stringify({ directoryHealth: { ok: true } }), { status: 200 });
      }
      if (input === '/api/mcp-servers/bundled%3Apubmed/tool-permissions') {
        return new Response(
          JSON.stringify({
            tools: [{ toolName: 'search_articles', title: 'Search Articles', readOnlyHint: true, state: 'allow' }],
            skipApprovalsActive: false,
          }),
          { status: 200 }
        );
      }
      if (input === '/api/mcp-servers/bundled%3Apubmed/tool-grants') {
        return new Response(JSON.stringify({ success: true, body: init?.body }), { status: 200 });
      }
      return new Response(
        JSON.stringify([
          {
            id: 'bundled:pubmed',
            name: 'pubmed',
            displayName: 'PubMed',
            description: 'Biomedical literature search.',
            source: 'bundled',
            authState: 'not-required',
            transport: 'stdio',
            upstreams: [],
            health: { ok: true },
            attachedAgents: ['OPERON'],
            enabled: true,
            connectionStatus: 'connected',
          },
        ]),
        { status: 200 }
      );
    });
    vi.stubGlobal('fetch', fetchMock);
    await renderWithI18n(<SynonBiomedMcpSettingsContent />, 'en-US');

    fireEvent.click(await screen.findByTestId('synon-biomed-mcp-permissions-pubmed'));
    expect(await screen.findByText('Search Articles')).toBeVisible();
    fireEvent.click(screen.getByTestId('synon-biomed-mcp-permission-search_articles-deny'));

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/mcp-servers/bundled%3Apubmed/tool-grants',
        expect.objectContaining({ method: 'POST', body: '{"toolName":"search_articles","decision":"deny"}' })
      )
    );
  });

  it('clears unsaved credentials when closing and reopening configuration', async () => {
    const fetchMock = vi.fn(
      async (input: string) =>
        new Response(
          JSON.stringify(
            input === '/api/mcp-servers/directory-health'
              ? { directoryHealth: { ok: true } }
              : [
                  {
                    ...connectorFixture('bundled:credential', 'credential', 'Credential Provider'),
                    apiKeyConfigurable: true,
                    apiKeyLabel: 'Provider key',
                    authRequired: true,
                  },
                ]
          ),
          { status: 200 }
        )
    );
    vi.stubGlobal('fetch', fetchMock);
    await renderWithI18n(<SynonBiomedMcpSettingsContent />, 'en-US');
    fireEvent.click(await screen.findByTestId('synon-biomed-mcp-configure-credential'));
    fireEvent.change(await screen.findByTestId('synon-biomed-mcp-api-key-input'), {
      target: { value: 'unsaved-test-secret' },
    });
    fireEvent.click(screen.getAllByRole('button', { name: 'Close' })[0]);
    await waitFor(() => expect(screen.queryByTestId('synon-biomed-mcp-api-key-input')).toBeNull());
    fireEvent.click(screen.getByTestId('synon-biomed-mcp-configure-credential'));
    expect(await screen.findByTestId('synon-biomed-mcp-api-key-input')).toHaveValue('');
    expect(fetchMock.mock.calls.some(([url]) => url.endsWith('/credential'))).toBe(false);
  });

  it('starts credential authorization for connectors that require authentication', async () => {
    const assignMock = vi.fn();
    const popup = {
      opener: window,
      location: { assign: assignMock },
      close: vi.fn(),
      closed: false,
    };
    const openMock = vi.fn(() => popup);
    vi.stubGlobal('open', openMock);
    const fetchMock = vi.fn(async (input: string) => {
      if (input === '/api/mcp-servers/directory-health') {
        return new Response(JSON.stringify({ directoryHealth: { ok: true } }), { status: 200 });
      }
      if (input === '/api/mcp-servers/connectors/remote%3Aclinical/authorize') {
        return new Response(JSON.stringify({ authorizationUrl: 'https://provider.example/authorize' }), {
          status: 200,
        });
      }
      return new Response(
        JSON.stringify([
          {
            id: 'remote:clinical',
            name: 'clinical',
            displayName: 'Clinical Remote',
            description: 'Credential-backed connector.',
            source: 'directory',
            oauthSupported: true,
            authState: 'required',
            transport: 'http',
            upstreams: [],
            health: { ok: false },
            attachedAgents: [],
            enabled: true,
            connectionStatus: 'disconnected',
          },
        ]),
        { status: 200 }
      );
    });
    vi.stubGlobal('fetch', fetchMock);
    await renderWithI18n(<SynonBiomedMcpSettingsContent />, 'en-US');

    fireEvent.click(await screen.findByTestId('synon-biomed-mcp-configure-clinical'));
    expect(openMock).not.toHaveBeenCalled();
    expect(await screen.findByText('Account sign-in')).toBeInTheDocument();
    fireEvent.click(screen.getByTestId('mcp-configuration-authorize'));

    expect(openMock).toHaveBeenCalledWith('about:blank', '_blank');
    expect(popup.opener).toBeNull();
    await waitFor(() => expect(assignMock).toHaveBeenCalledWith('https://provider.example/authorize'));
  });

  it('rejects an insecure authorization URL before navigating the OAuth popup', async () => {
    const assignMock = vi.fn();
    const popup = {
      opener: window,
      location: { assign: assignMock },
      close: vi.fn(),
      closed: false,
    };
    vi.stubGlobal(
      'open',
      vi.fn(() => popup)
    );
    const fetchMock = vi.fn(async (input: string) => {
      if (input === '/api/mcp-servers/directory-health') {
        return new Response(JSON.stringify({ directoryHealth: { ok: true } }), { status: 200 });
      }
      if (input === '/api/mcp-servers/connectors/remote%3Aclinical/authorize') {
        return new Response(JSON.stringify({ authorizationUrl: 'http://provider.example/authorize' }), { status: 200 });
      }
      return new Response(
        JSON.stringify([
          {
            ...connectorFixture('remote:clinical', 'clinical', 'Clinical Remote'),
            source: 'directory',
            oauthSupported: true,
            authState: 'required',
            transport: 'http',
            health: { ok: false },
            connectionStatus: 'disconnected',
          },
        ]),
        { status: 200 }
      );
    });
    vi.stubGlobal('fetch', fetchMock);
    await renderWithI18n(<SynonBiomedMcpSettingsContent />, 'en-US');

    fireEvent.click(await screen.findByTestId('synon-biomed-mcp-configure-clinical'));
    fireEvent.click(await screen.findByTestId('mcp-configuration-authorize'));

    await waitFor(() => expect(popup.close).toHaveBeenCalledTimes(1));
    expect(assignMock).not.toHaveBeenCalled();
  });

  it('configures a hosted MCP key through a password-only modal without echoing the secret', async () => {
    let configured = false;
    const fetchMock = vi.fn(async (input: string) => {
      if (input === '/api/mcp-servers/directory-health') {
        return new Response(JSON.stringify({ directoryHealth: { ok: true } }), { status: 200 });
      }
      if (input === '/api/mcp-servers/connectors/bundled%3Atamarind-bio/credential') {
        configured = true;
        return new Response(JSON.stringify({ apiKeyConfigured: true }), { status: 200 });
      }
      if (input === '/api/mcp-servers/connectors/bundled%3Atamarind-bio/disconnect') {
        configured = false;
        return new Response(JSON.stringify({ id: 'bundled:tamarind-bio' }), { status: 200 });
      }
      return new Response(
        JSON.stringify([
          {
            ...connectorFixture('bundled:tamarind-bio', 'tamarind-bio', 'Tamarind Bio'),
            authRequired: true,
            authState: configured ? 'authorized' : 'unauthorized',
            authHint: 'Compute jobs require confirmation.',
            apiKeyConfigurable: true,
            apiKeyConfigured: configured,
            apiKeyLabel: 'Tamarind API Key',
            transport: 'streamable-http',
            health: { ok: configured },
            connectionStatus: configured ? 'connected' : 'auth_required',
          },
        ]),
        { status: 200 }
      );
    });
    vi.stubGlobal('fetch', fetchMock);
    await renderWithI18n(<SynonBiomedMcpSettingsContent />, 'en-US');

    fireEvent.click(await screen.findByTestId('synon-biomed-mcp-configure-tamarind-bio'));
    const input = await screen.findByTestId('synon-biomed-mcp-api-key-input');
    expect(input).toHaveAttribute('type', 'password');
    expect(input).toHaveAttribute('autocomplete', 'new-password');
    fireEvent.change(input, { target: { value: 'provider-secret' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save and check connection' }));

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/mcp-servers/connectors/bundled%3Atamarind-bio/credential',
        expect.objectContaining({ method: 'PUT', body: '{"apiKey":"provider-secret"}' })
      )
    );
    await waitFor(() => expect(input).toHaveValue(''));
    expect(document.body).not.toHaveTextContent('provider-secret');
    expect(await screen.findByTestId('mcp-configuration-status')).toHaveTextContent('Connected');
    fireEvent.click(screen.getAllByRole('button', { name: 'Close' })[0]);
    expect(screen.getByTestId('synon-biomed-mcp-configure-tamarind-bio')).toHaveTextContent('Configure');

    fireEvent.click(screen.getByTestId('synon-biomed-mcp-more-tamarind-bio'));
    fireEvent.click(await screen.findByTestId('synon-biomed-mcp-disconnect-tamarind-bio'));
    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/mcp-servers/connectors/bundled%3Atamarind-bio/disconnect',
        expect.objectContaining({ method: 'POST' })
      )
    );
    await waitFor(() =>
      expect(screen.getByTestId('synon-biomed-mcp-configure-tamarind-bio')).toHaveTextContent('Configure')
    );
    expect(screen.queryByTestId('synon-biomed-mcp-disconnect-tamarind-bio')).toBeNull();
  });

  it('reports a blocked OAuth window without starting an unusable authorization flow', async () => {
    const openMock = vi.fn(() => null);
    vi.stubGlobal('open', openMock);
    const fetchMock = vi.fn(async (input: string) => {
      if (input === '/api/mcp-servers/directory-health') {
        return new Response(JSON.stringify({ directoryHealth: { ok: true } }), { status: 200 });
      }
      return new Response(
        JSON.stringify([
          {
            ...connectorFixture('remote:clinical', 'clinical', 'Clinical Remote'),
            source: 'directory',
            oauthSupported: true,
            authState: 'required',
            connectionStatus: 'disconnected',
          },
        ]),
        { status: 200 }
      );
    });
    vi.stubGlobal('fetch', fetchMock);
    await renderWithI18n(<SynonBiomedMcpSettingsContent />, 'en-US');

    fireEvent.click(await screen.findByTestId('synon-biomed-mcp-configure-clinical'));
    fireEvent.click(await screen.findByTestId('mcp-configuration-authorize'));

    expect(openMock).toHaveBeenCalledWith('about:blank', '_blank');
    expect(Message.error).toHaveBeenCalledWith('Allow pop-ups for this site, then connect credentials again.');
    await waitFor(() => expect(screen.getByTestId('mcp-configuration-authorize')).not.toBeDisabled());
    expect(fetchMock).not.toHaveBeenCalledWith(
      '/api/mcp-servers/connectors/remote%3Aclinical/authorize',
      expect.anything()
    );
  });

  it('does not retain connector A tools when connector B permission loading fails', async () => {
    const consoleWarning = vi.spyOn(console, 'warn').mockImplementation(() => undefined);
    const fetchMock = vi.fn(async (input: string) => {
      if (input === '/api/mcp-servers/directory-health') {
        return new Response(JSON.stringify({ directoryHealth: { ok: true } }), { status: 200 });
      }
      if (input === '/api/mcp-servers/bundled%3Aalpha/tool-permissions') {
        return new Response(
          JSON.stringify({
            tools: [{ toolName: 'alpha_tool', title: 'Alpha Tool', readOnlyHint: true, state: 'allow' }],
          }),
          { status: 200 }
        );
      }
      if (input === '/api/mcp-servers/bundled%3Abeta/tool-permissions') {
        return new Response(JSON.stringify({ detail: 'Beta permissions unavailable' }), { status: 503 });
      }
      return new Response(
        JSON.stringify([
          connectorFixture('bundled:alpha', 'alpha', 'Alpha'),
          connectorFixture('bundled:beta', 'beta', 'Beta'),
        ]),
        { status: 200 }
      );
    });
    vi.stubGlobal('fetch', fetchMock);
    await renderWithI18n(<SynonBiomedMcpSettingsContent />, 'en-US');

    fireEvent.click(await screen.findByTestId('synon-biomed-mcp-permissions-alpha'));
    expect(await screen.findByTestId('synon-biomed-mcp-permission-alpha_tool-allow')).toBeVisible();
    const close = document.querySelector<HTMLElement>('.arco-modal-close-icon');
    expect(close).not.toBeNull();
    fireEvent.click(close!);
    fireEvent.click(await screen.findByTestId('synon-biomed-mcp-permissions-beta'));

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/mcp-servers/bundled%3Abeta/tool-permissions',
        expect.objectContaining({ headers: { Accept: 'application/json' } })
      )
    );
    await waitFor(() => expect(screen.queryByTestId('synon-biomed-mcp-permission-alpha_tool-allow')).toBeNull());
    expect(consoleWarning).toHaveBeenCalledWith('[McpPermissionsModal] Failed to load MCP tool permissions', {
      errorName: 'SynonBiomedCapabilityError',
    });
  });

  it('keeps local MCPs deferred and exposes an explicit first-use install action', async () => {
    const fetchMock = vi.fn(async (input: string, init?: RequestInit) => {
      if (input === '/api/mcp-servers/directory-health') {
        return new Response(JSON.stringify({ directoryHealth: { ok: true } }), { status: 200 });
      }
      if (input === '/api/mcp-servers/optional/bundled%3Arenkin-local/install') {
        expect(init?.method).toBe('POST');
        return new Response(
          JSON.stringify({
            id: 'bundled:renkin-local',
            name: 'renkin-local',
            displayName: 'RENKIN retrosynthesis',
            description: 'Local retrosynthesis planning.',
            category: 'Small-molecule design',
            license: 'MIT',
            repositoryUrl: 'https://github.com/kent-tokyo/renkin',
            installKind: 'cargo-git',
            installLabel: 'Install from the official Git repository',
            sizeLabel: 'Small Rust binary; no model weights',
            requirements: ['Rust toolchain (cargo)'],
            sourceRef: 'https://github.com/kent-tokyo/renkin',
            status: 'installed',
            installed: true,
            installPath: 'mcp-servers/managed/renkin-local',
          }),
          { status: 200 }
        );
      }
      if (input === '/api/mcp-servers/optional') {
        return new Response(
          JSON.stringify([
            {
              id: 'bundled:renkin-local',
              name: 'renkin-local',
              displayName: 'RENKIN retrosynthesis',
              description: 'Local retrosynthesis planning.',
              category: 'Small-molecule design',
              license: 'MIT',
              repositoryUrl: 'https://github.com/kent-tokyo/renkin',
              installKind: 'cargo-git',
              installLabel: 'Install from the official Git repository',
              sizeLabel: 'Small Rust binary; no model weights',
              requirements: ['Rust toolchain (cargo)'],
              sourceRef: 'https://github.com/kent-tokyo/renkin',
              status: 'not-installed',
              installed: false,
            },
          ]),
          { status: 200 }
        );
      }
      return new Response(JSON.stringify([]), { status: 200 });
    });
    vi.stubGlobal('fetch', fetchMock);
    await renderWithI18n(<SynonBiomedMcpSettingsContent />, 'en-US');

    fireEvent.click(screen.getByTestId('synon-biomed-mcp-add'));
    fireEvent.click(await screen.findByText('Free & local'));
    const card = await screen.findByTestId('synon-biomed-mcp-optional-renkin-local');
    expect(card).toHaveAttribute('aria-label', 'RENKIN retrosynthesis. Not installed');
    const installButton = within(card).getByTestId('synon-biomed-mcp-optional-install-renkin-local');
    expect(installButton).toHaveTextContent('Install');
    fireEvent.click(installButton);

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/mcp-servers/optional/bundled%3Arenkin-local/install',
        expect.objectContaining({ method: 'POST' })
      )
    );
    await waitFor(() => expect(card).toHaveAttribute('aria-label', 'RENKIN retrosynthesis. Installed'));
  });
  it('searches displayed translations and keeps the library filter when closing connector discovery', async () => {
    const fixture = {
      ...connectorFixture('bundled:example', 'example', 'Example'),
      description_i18n: { 'zh-CN': '中文检索说明' },
    };
    const fetchMock = vi.fn(
      async (input: string) =>
        new Response(
          JSON.stringify(
            input.endsWith('directory-health')
              ? { directoryHealth: { ok: true } }
              : input.endsWith('/connectors')
                ? [fixture]
                : []
          ),
          { status: 200 }
        )
    );
    vi.stubGlobal('fetch', fetchMock);
    await renderWithI18n(<SynonBiomedMcpSettingsContent />, 'zh-CN');
    expect(await screen.findByText('中文检索说明')).toBeVisible();
    fireEvent.change(screen.getByRole('textbox', { name: '搜索连接器...' }), { target: { value: '中文检索' } });
    expect(screen.getByText('Example')).toBeVisible();
    fireEvent.click(screen.getByTestId('synon-biomed-mcp-add'));
    fireEvent.click(await screen.findByText('免费与本地'));
    await screen.findByTestId('synon-biomed-mcp-optional');
    fireEvent.click(screen.getByRole('dialog').querySelector('.arco-modal-close-icon')!);
    expect(screen.getByRole('textbox', { name: '搜索连接器...' })).toHaveValue('中文检索');
    expect(screen.getByText('Example')).toBeVisible();
    expect(fetchMock.mock.calls.every(([path]) => !path.endsWith('/install'))).toBe(true);
  });

  it('reports a failed first load instead of presenting it as an empty inventory', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => undefined);
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response('unavailable', { status: 503 }))
    );
    await renderWithI18n(<SynonBiomedMcpSettingsContent />, 'en-US');
    expect(await screen.findByTestId('synon-biomed-mcp-load-error')).toBeVisible();
    expect(screen.queryByText('No connectors installed yet.')).toBeNull();
  });

  it('keeps pagination outside the scroll area, resets page after filtering and preserves full localized copy', async () => {
    const connectors = Array.from({ length: 13 }, (_, i) =>
      connectorFixture(`bundled:item-${i}`, `item-${i}`, `Connector ${i}`)
    );
    connectors[0] = {
      ...connectors[0],
      displayName: 'bioRxiv',
      description: 'bioRxiv/medRxiv preprints: full text and metadata.',
    };
    vi.stubGlobal(
      'fetch',
      vi.fn(
        async (input: string) =>
          new Response(
            JSON.stringify(
              input.endsWith('directory-health')
                ? { directoryHealth: { ok: true } }
                : input.endsWith('/connectors')
                  ? connectors
                  : []
            ),
            { status: 200 }
          )
      )
    );
    await renderWithI18n(<SynonBiomedMcpSettingsContent />, 'en-US');
    expect(await screen.findByRole('heading', { name: 'Connectors 13' })).toBeVisible();
    expect(screen.getByText('bioRxiv/medRxiv preprints: full text and metadata.')).toBeVisible();
    const footer = screen.getByTestId('mcp-library-footer');
    const scroll = screen.getByTestId('mcp-library-scroll');
    expect(scroll).not.toContainElement(footer);
    fireEvent.click(within(footer).getByRole('button', { name: 'Connector pagination 2', exact: true }));
    expect(screen.getByText('Connector 12')).toBeVisible();
    scroll.scrollTop = 100;
    fireEvent.change(screen.getByRole('textbox', { name: 'Search connectors...' }), { target: { value: 'preprints' } });
    await waitFor(() => expect(screen.getByText('bioRxiv')).toBeVisible());
    expect(scroll.scrollTop).toBe(0);
    expect(screen.queryByText('Connector 12')).toBeNull();
    fireEvent.change(screen.getByTestId('synon-biomed-mcp-filter'), { target: { value: 'needs-attention' } });
    expect(screen.getByText('No connectors match the current filters.')).toBeVisible();
  });
});

function connectorFixture(id: string, name: string, displayName: string) {
  return {
    id,
    name,
    displayName,
    description: `${displayName} connector.`,
    source: 'bundled',
    authState: 'not-required',
    transport: 'stdio',
    upstreams: [],
    health: { ok: true },
    attachedAgents: ['OPERON'],
    enabled: true,
    connectionStatus: 'connected',
  };
}

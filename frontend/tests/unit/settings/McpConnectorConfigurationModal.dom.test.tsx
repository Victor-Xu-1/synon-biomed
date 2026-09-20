import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { describe, expect, it, vi } from 'vitest';
import type { SynonBiomedMcpServer } from '@/renderer/services/synonBiomedCapabilities';
import { McpConnectorConfigurationModal } from '@/renderer/pages/settings/ToolsSettings/McpConnectorConfigurationModal';
import { renderWithI18n } from '../i18nTestUtils';

const server: SynonBiomedMcpServer = {
  id: 'bundled:provider',
  name: 'provider',
  displayName: 'Provider',
  description: '',
  source: 'bundled',
  authRequired: true,
  oauthSupported: true,
  authState: 'unauthorized',
  authHint: '',
  apiKeyConfigurable: true,
  apiKeyConfigured: false,
  apiKeyLabel: 'Provider Token',
  transport: 'streamable-http',
  upstreams: [
    { name: 'Provider', credentialUrl: 'https://provider.example/signup', infoUrl: 'https://provider.example/docs' },
  ],
  hostedBySynon: false,
  health: { ok: false },
  attachedAgents: [],
  enabled: true,
  connectionStatus: 'auth_required',
};

const actions = () => ({
  onCancel: vi.fn(),
  onSaveKey: vi.fn(async () => undefined),
  onAuthorize: vi.fn(async () => true),
  onRefresh: vi.fn(async () => undefined),
  onPermissions: vi.fn(),
});

describe('McpConnectorConfigurationModal', () => {
  it('does not report a stale successful status after inventory reload fails', async () => {
    await renderWithI18n(
      <McpConnectorConfigurationModal
        server={{ ...server, connectionStatus: 'connected', health: { ok: true } }}
        statusUnavailable
        {...actions()}
      />,
      'en-US'
    );
    expect(screen.getByTestId('mcp-configuration-status')).toHaveTextContent('Status unavailable');
    expect(screen.getByTestId('mcp-configuration-status')).not.toHaveTextContent('Connected');
  });
  it('shows both supported methods and official links without automatically authorizing or collecting account passwords', async () => {
    const callbacks = actions();
    await renderWithI18n(<McpConnectorConfigurationModal server={server} {...callbacks} />, 'en-US');
    expect(screen.getByRole('link', { name: /Apply/ })).toHaveAttribute('href', 'https://provider.example/signup');
    expect(screen.getByRole('link', { name: /Apply/ })).toHaveAttribute('rel', 'noopener noreferrer');
    expect(screen.getByLabelText('Provider Token')).toHaveAttribute('type', 'password');
    expect(screen.getByText(/only on the provider's sign-in page/)).toBeInTheDocument();
    expect(callbacks.onAuthorize).not.toHaveBeenCalled();
    fireEvent.click(screen.getByTestId('mcp-configuration-authorize'));
    await screen.findByText(/Complete sign-in in the provider window/);
    expect(screen.getByTestId('mcp-configuration-status')).toHaveTextContent('Authorization required');
    fireEvent.click(screen.getByTestId('mcp-configuration-refresh'));
    await waitFor(() => expect(callbacks.onRefresh).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(screen.getByTestId('mcp-configuration-refresh')).not.toBeDisabled());
  });

  it('does not equate saving a key with a successful connection and clears the secret draft', async () => {
    const callbacks = actions();
    await renderWithI18n(
      <McpConnectorConfigurationModal
        server={{ ...server, oauthSupported: false, apiKeyConfigured: true }}
        {...callbacks}
      />,
      'en-US'
    );
    const input = screen.getByLabelText('Provider Token');
    expect(input).toHaveValue('');
    expect(screen.getByText(/Leave this field empty/)).toBeInTheDocument();
    fireEvent.change(input, { target: { value: ' test-secret ' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save and check connection' }));
    await waitFor(() => expect(input).toHaveValue(''));
    expect(callbacks.onSaveKey).toHaveBeenCalledWith('test-secret');
    expect(screen.getByTestId('mcp-configuration-status')).toHaveTextContent('Authorization failed');
    expect(screen.getByRole('status')).toHaveTextContent('saving does not guarantee provider access');
    expect(document.body).not.toHaveTextContent('test-secret');
  });

  it('keeps the form recoverable after failure without rendering provider secrets', async () => {
    const callbacks = actions();
    callbacks.onSaveKey.mockRejectedValueOnce(new Error('secret-echo-from-provider'));
    await renderWithI18n(<McpConnectorConfigurationModal server={server} {...callbacks} />, 'en-US');
    fireEvent.change(screen.getByLabelText('Provider Token'), { target: { value: 'retry-key' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save and check connection' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not complete this action');
    expect(document.body).not.toHaveTextContent('secret-echo-from-provider');
    expect(screen.getByLabelText('Provider Token')).toHaveValue('retry-key');
    fireEvent.click(screen.getByRole('button', { name: 'Save and check connection' }));
    await waitFor(() => expect(screen.getByLabelText('Provider Token')).toHaveValue(''));
    expect(callbacks.onSaveKey).toHaveBeenCalledTimes(2);
  });

  it('prevents duplicate saves and dismissal while a credential operation is pending', async () => {
    const callbacks = actions();
    let finish!: () => void;
    callbacks.onSaveKey.mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          finish = resolve;
        })
    );
    await renderWithI18n(<McpConnectorConfigurationModal server={server} {...callbacks} />, 'en-US');
    const input = screen.getByLabelText('Provider Token');
    fireEvent.change(input, { target: { value: 'key' } });
    fireEvent.keyDown(input, { key: 'Enter', keyCode: 13 });
    fireEvent.keyDown(input, { key: 'Enter', keyCode: 13 });
    expect(callbacks.onSaveKey).toHaveBeenCalledTimes(1);
    expect(screen.getByRole('button', { name: 'Close' })).toBeDisabled();
    expect(screen.getByTestId('mcp-configuration-authorize')).toBeDisabled();
    await act(async () => finish());
    expect(input).toHaveValue('');
  });

  it('renders Chinese guidance and omits unsupported forms', async () => {
    await renderWithI18n(
      <McpConnectorConfigurationModal server={{ ...server, apiKeyConfigurable: false }} {...actions()} />,
      'zh-CN'
    );
    expect(screen.getByText('账号登录授权')).toBeInTheDocument();
    expect(screen.queryByTestId('synon-biomed-mcp-api-key-input')).toBeNull();
    expect(screen.getByTestId('mcp-configuration-status')).toHaveTextContent('待授权');
  });
});

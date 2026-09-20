import { Button, Input, Modal } from '@arco-design/web-react';
import React, { useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { SynonBiomedMcpServer } from '@/renderer/services/synonBiomedCapabilities';
import { connectorConfigurationState, connectorProviderLinks, validConnectorKey } from './mcpConnectorConfiguration';
import './McpConnectorConfigurationModal.css';

// Mounted only while open and keyed by connector ID: secret drafts never survive
// closing or switching providers. Existing credentials are never read back.
export const McpConnectorConfigurationModal: React.FC<{
  server: SynonBiomedMcpServer;
  statusUnavailable?: boolean;
  onCancel: () => void;
  onSaveKey: (key: string) => Promise<void>;
  onAuthorize: () => Promise<boolean>;
  onRefresh: () => Promise<void>;
  onPermissions: () => void;
}> = ({ server, statusUnavailable = false, onCancel, onSaveKey, onAuthorize, onRefresh, onPermissions }) => {
  const { t } = useTranslation();
  const [key, setKey] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(false);
  const [notice, setNotice] = useState<'saved' | 'authorizationStarted' | null>(null);
  const pending = useRef(false);
  const links = connectorProviderLinks(server.upstreams);
  const state = statusUnavailable ? 'unknown' : connectorConfigurationState(server);

  const run = async (action: () => Promise<void>) => {
    if (pending.current) return;
    pending.current = true;
    setBusy(true);
    setError(false);
    setNotice(null);
    try {
      await action();
    } catch {
      // Provider errors may contain request details. Render only safe recovery text.
      setError(true);
    } finally {
      pending.current = false;
      setBusy(false);
    }
  };
  const save = () => {
    if (!validConnectorKey(key)) return;
    void run(async () => {
      await onSaveKey(key.trim());
      setKey('');
      setNotice('saved');
    });
  };

  return (
    <Modal
      visible
      title={t('settings.mcpConfiguration.title', { name: server.displayName })}
      className='mcp-configuration-modal'
      style={{ width: 'min(600px, calc(100vw - 32px))' }}
      maskClosable={!busy}
      escToExit={!busy}
      closable={!busy}
      onCancel={() => {
        if (!pending.current) onCancel();
      }}
      footer={
        <Button disabled={busy} onClick={onCancel}>
          {t('common.close')}
        </Button>
      }
      unmountOnExit
    >
      <div className='mcp-configuration' data-testid='mcp-configuration'>
        <section className='mcp-configuration__status' aria-live='polite'>
          <div>
            <span className='mcp-configuration__label'>{t('settings.mcpConfiguration.currentStatus')}</span>
            <strong data-testid='mcp-configuration-status'>{t(`settings.mcpConfiguration.states.${state}`)}</strong>
          </div>
          <Button disabled={busy} onClick={() => void run(onRefresh)} data-testid='mcp-configuration-refresh'>
            {t('common.refresh')}
          </Button>
        </section>
        <section className='mcp-configuration__section'>
          <h3>{t('settings.mcpConfiguration.provider')}</h3>
          <p>{t('settings.mcpConfiguration.providerHint')}</p>
          {links.length ? (
            links.map((link) => (
              <a
                key={link.url}
                href={link.url}
                target='_blank'
                rel='noopener noreferrer'
                className='mcp-configuration__link'
              >
                <span>
                  {t(`settings.mcpConfiguration.links.${link.kind}`)}
                  {link.name ? ` · ${link.name}` : ''} ↗
                </span>
                <small>{link.url}</small>
              </a>
            ))
          ) : (
            <p>{t('settings.mcpConfiguration.noProviderLink')}</p>
          )}
        </section>
        {server.oauthSupported ? (
          <section className='mcp-configuration__section'>
            <h3>{t('settings.mcpConfiguration.account')}</h3>
            <p>{t('settings.mcpConfiguration.accountHint')}</p>
            <Button
              type='primary'
              disabled={busy}
              data-testid='mcp-configuration-authorize'
              onClick={() =>
                void run(async () => {
                  if (await onAuthorize()) setNotice('authorizationStarted');
                })
              }
            >
              {t('settings.mcpConfiguration.signIn')}
            </Button>
          </section>
        ) : null}
        {server.apiKeyConfigurable ? (
          <section className='mcp-configuration__section'>
            <h3>{t('settings.mcpConfiguration.key')}</h3>
            <p>
              {t(server.apiKeyConfigured ? 'settings.mcpConfiguration.keyStored' : 'settings.mcpConfiguration.keyHint')}
            </p>
            <label htmlFor='synon-biomed-mcp-api-key'>
              {server.apiKeyLabel || t('settings.synonBiomedMcpApiKeyLabel')}
            </label>
            <Input.Password
              id='synon-biomed-mcp-api-key'
              data-testid='synon-biomed-mcp-api-key-input'
              value={key}
              disabled={busy}
              autoComplete='new-password'
              placeholder={t('settings.synonBiomedMcpApiKeyPlaceholder')}
              onChange={setKey}
              onPressEnter={save}
            />
            {key && !validConnectorKey(key) ? <p role='alert'>{t('settings.mcpConfiguration.keyInvalid')}</p> : null}
            <p>{t('settings.synonBiomedMcpApiKeySecurityHint')}</p>
            <Button type='primary' loading={busy} disabled={busy || !validConnectorKey(key)} onClick={save}>
              {t('settings.mcpConfiguration.save')}
            </Button>
          </section>
        ) : null}
        {!server.oauthSupported && !server.apiKeyConfigurable ? (
          <p>{t('settings.mcpConfiguration.unsupported')}</p>
        ) : null}
        {error ? (
          <p role='alert' className='text-danger-6'>
            {t('settings.mcpConfiguration.error')}
          </p>
        ) : null}
        {notice ? <p role='status'>{t(`settings.mcpConfiguration.${notice}`)}</p> : null}
        <section className='mcp-configuration__safety'>
          <p>{t('settings.mcpConfiguration.costHint')}</p>
          <Button disabled={busy} onClick={onPermissions}>
            {t('settings.synonBiomedMcpPermissions')}
          </Button>
        </section>
      </div>
    </Modal>
  );
};

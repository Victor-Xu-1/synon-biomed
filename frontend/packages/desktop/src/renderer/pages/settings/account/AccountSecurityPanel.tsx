import { Button, Message, Modal } from '@arco-design/web-react';
import { Refresh, Shield } from '@icon-park/react';
import React from 'react';
import { useTranslation } from 'react-i18next';
import { useWebAccountSecurity } from '@/renderer/hooks/account/useWebAccountSecurity';
import type { WebAccountDevice } from '@/renderer/services/account/webAccountSecurity';
import './AccountSecurityPanel.css';

type AccountSecurityPanelProps = {
  onSignedOut: () => Promise<void>;
};

const methodTranslationKeys: Record<string, string> = {
  local: 'settings.accountSecurity.method.local',
  google: 'settings.accountSecurity.method.google',
  apple: 'settings.accountSecurity.method.apple',
  wechat: 'settings.accountSecurity.method.wechat',
};

const networkTranslationKeys: Record<string, string> = {
  loopback: 'settings.accountSecurity.network.loopback',
  private: 'settings.accountSecurity.network.private',
  remote: 'settings.accountSecurity.network.remote',
  unknown: 'settings.accountSecurity.network.unknown',
};

const AccountSecurityPanel: React.FC<AccountSecurityPanelProps> = ({ onSignedOut }) => {
  const { t, i18n } = useTranslation();
  const security = useWebAccountSecurity();
  const trustedDevices = security.data?.devices.filter((device) => !device.legacy) ?? [];
  const hasLegacySessions = security.data?.devices.some((device) => device.legacy) ?? false;

  const methodLabel = (method: string) => t(methodTranslationKeys[method] ?? 'settings.accountSecurity.unknownMethod');
  const networkLabel = (network: string) =>
    t(networkTranslationKeys[network] ?? 'settings.accountSecurity.unknownNetwork');
  const formatDate = (value: string) =>
    new Intl.DateTimeFormat(i18n.language, {
      dateStyle: 'medium',
      timeStyle: 'short',
    }).format(new Date(value));

  const revokeDevice = async (device: WebAccountDevice) => {
    try {
      const result = await security.revokeDevice(device.id);
      if (result.signedOut) await onSignedOut();
      Message.success(t('settings.accountSecurity.sessionRevoked'));
    } catch {
      Message.error(t('settings.accountSecurity.actionFailed'));
    }
  };

  const revokeOtherDevices = async () => {
    try {
      await security.revokeOtherDevices();
      Message.success(t('settings.accountSecurity.otherSessionsRevoked'));
    } catch {
      Message.error(t('settings.accountSecurity.actionFailed'));
    }
  };

  const confirmRevokeAll = () => {
    Modal.confirm({
      title: t('settings.accountSecurity.revokeAllTitle'),
      content: t('settings.accountSecurity.revokeAllDescription'),
      okButtonProps: { status: 'danger' },
      okText: t('settings.accountSecurity.revokeAllConfirm'),
      cancelText: t('common.cancel'),
      onOk: async () => {
        try {
          await security.revokeAll();
          await onSignedOut();
        } catch {
          Message.error(t('settings.accountSecurity.actionFailed'));
          throw new Error('account_security_revoke_all_failed');
        }
      },
    });
  };

  return (
    <section className='account-security' aria-labelledby='account-security-title'>
      <header className='account-security__header'>
        <div className='account-security__title'>
          <span className='account-security__icon' aria-hidden='true'>
            <Shield theme='outline' size={18} />
          </span>
          <div>
            <h2 id='account-security-title'>{t('settings.accountSecurity.title')}</h2>
            <p>{t('settings.accountSecurity.description')}</p>
          </div>
        </div>
        <Button
          size='mini'
          type='text'
          loading={security.loading}
          aria-label={t('settings.accountSecurity.refresh')}
          onClick={security.retry}
        >
          <Refresh theme='outline' size={14} />
          {t('settings.accountSecurity.refresh')}
        </Button>
      </header>

      {security.loading && !security.data ? (
        <div className='account-security__state' role='status'>
          <span className='account-security__pulse' aria-hidden='true' />
          {t('settings.accountSecurity.loading')}
        </div>
      ) : security.error && !security.data ? (
        <div className='account-security__state account-security__state--error' role='alert'>
          <span>{t('settings.accountSecurity.loadFailed')}</span>
          <Button size='mini' onClick={security.retry}>
            {t('common.retry')}
          </Button>
        </div>
      ) : security.data ? (
        <div className='account-security__content'>
          <div className='account-security__methods'>
            <div>
              <h3>{t('settings.accountSecurity.methodsTitle')}</h3>
              <p>{t('settings.accountSecurity.methodsDescription')}</p>
            </div>
            <div className='account-security__method-list'>
              {security.data.loginMethods.map((method) => (
                <span className='account-security__method' key={method}>
                  <span className={`account-security__method-mark account-security__method-mark--${method}`}>
                    {method === 'google' ? 'G' : method === 'apple' ? 'A' : method === 'wechat' ? 'W' : 'S'}
                  </span>
                  {methodLabel(method)}
                  <span>{t('settings.accountSecurity.connected')}</span>
                </span>
              ))}
            </div>
          </div>

          {!security.data.managed ? (
            <div className='account-security__local-mode' role='note'>
              <strong>{t('settings.accountSecurity.localModeTitle')}</strong>
              <span>{t('settings.accountSecurity.localModeDescription')}</span>
            </div>
          ) : null}

          <div className='account-security__section-heading'>
            <div>
              <h3>{t('settings.accountSecurity.sessionsTitle')}</h3>
              <p>{t('settings.accountSecurity.sessionsDescription')}</p>
            </div>
            <Button
              size='mini'
              disabled={
                security.mutating ||
                !security.data.managed ||
                (!hasLegacySessions && trustedDevices.every((device) => device.current))
              }
              onClick={() => void revokeOtherDevices()}
            >
              {t('settings.accountSecurity.revokeOthers')}
            </Button>
          </div>
          <div className='account-security__sessions'>
            {trustedDevices.map((device) => (
              <article className='account-security__session' key={device.id}>
                <span className='account-security__device' aria-hidden='true' />
                <div className='account-security__session-copy'>
                  <div>
                    <strong>{device.userAgent || t('settings.accountSecurity.unknownBrowser')}</strong>
                    {device.current ? (
                      <span className='account-security__current'>{t('settings.accountSecurity.current')}</span>
                    ) : null}
                  </div>
                  <span>
                    {t('settings.accountSecurity.sessionMeta', {
                      method: methodLabel(device.authMethod),
                      network: networkLabel(device.networkClass),
                      ip: device.ipAddress || '—',
                      time: formatDate(device.lastSeenAt),
                    })}
                  </span>
                </div>
                {!device.current ? (
                  <Button
                    size='mini'
                    type='text'
                    disabled={security.mutating}
                    onClick={() => void revokeDevice(device)}
                  >
                    {t('settings.accountSecurity.revoke')}
                  </Button>
                ) : null}
              </article>
            ))}
          </div>

          {hasLegacySessions ? (
            <aside
              className='account-security__legacy-note'
              role='note'
              aria-label={t('settings.accountSecurity.legacyNoticeLabel')}
            >
              <strong>{t('settings.accountSecurity.legacyNoticeTitle')}</strong>
              <span>{t('settings.accountSecurity.legacyNoticeDescription')}</span>
            </aside>
          ) : null}

          <footer className='account-security__footer'>
            <div>
              <strong>{t('settings.accountSecurity.revokeAllTitle')}</strong>
              <span>{t('settings.accountSecurity.revokeAllHint')}</span>
            </div>
            <Button
              status='danger'
              size='small'
              disabled={security.mutating || !security.data.managed}
              onClick={confirmRevokeAll}
            >
              {t('settings.accountSecurity.revokeAll')}
            </Button>
          </footer>
        </div>
      ) : null}
    </section>
  );
};

export default AccountSecurityPanel;

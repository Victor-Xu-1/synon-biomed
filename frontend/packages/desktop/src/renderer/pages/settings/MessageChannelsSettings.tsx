import { Spin } from '@arco-design/web-react';
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import larkLogo from '@/renderer/assets/channel-logos/lark.svg';
import weixinLogo from '@/renderer/assets/channel-logos/weixin.svg';
import AccessibleActionDialog from '@/renderer/components/common/AccessibleActionDialog';
import { SettingsSection } from './components/SettingsPrimitives';
import {
  emptyMessageChannelStatuses,
  loadMessageChannelStatuses,
  messageChannelErrorMessage,
  pollMessageChannelQr,
  startMessageChannelQr,
  unpairMessageChannel,
  type MessageChannelId,
  type MessageChannelStatus,
  type MessageChannelStatuses,
} from '@/renderer/services/messageChannels';
import './MessageChannelsSettings.css';

type ChannelDefinition = {
  id: MessageChannelId;
  nameKey: 'feishu' | 'wechat';
  descriptionKey: 'feishuDescription' | 'wechatDescription';
  logo: string;
};

const CHANNELS: ChannelDefinition[] = [
  {
    id: 'feishu',
    nameKey: 'feishu',
    descriptionKey: 'feishuDescription',
    logo: larkLogo,
  },
  {
    id: 'wechat',
    nameKey: 'wechat',
    descriptionKey: 'wechatDescription',
    logo: weixinLogo,
  },
];

const MessageChannelsSettings: React.FC = () => {
  const { t } = useTranslation();
  const [statuses, setStatuses] = useState<MessageChannelStatuses>(() => emptyMessageChannelStatuses());
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState(false);

  const refresh = useCallback(async () => {
    setLoading(true);
    setLoadError(false);
    try {
      setStatuses(await loadMessageChannelStatuses());
    } catch (error) {
      console.error('Failed to load message channel status:', messageChannelErrorMessage(error));
      setLoadError(true);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => void refresh(), [refresh]);

  const markPaired = useCallback((channel: MessageChannelId) => {
    setStatuses((current) => ({
      ...current,
      [channel]: { configured: true, paired: true },
    }));
  }, []);

  return (
    <SettingsSection
      className='general-message-channels-section'
      title={t('settings.generalSettings.messageChannels.title')}
      description={t('settings.generalSettings.messageChannels.description')}
      icon='network'
    >
      <div className='message-channels-settings' data-testid='message-channels-settings'>
        {loadError ? (
          <div className='message-channels-settings__notice' role='alert'>
            <span>{t('settings.generalSettings.messageChannels.loadFailed')}</span>
            <button type='button' className='message-channel-card__secondary-button' onClick={() => void refresh()}>
              {t('settings.generalSettings.messageChannels.retry')}
            </button>
          </div>
        ) : null}
        <div className='message-channels-grid' aria-busy={loading}>
          {CHANNELS.map((channel) => (
            <MessageChannelCard
              key={channel.id}
              channel={channel}
              status={statuses[channel.id]}
              statusLoading={loading}
              onPaired={markPaired}
              onUnpaired={refresh}
            />
          ))}
        </div>
      </div>
    </SettingsSection>
  );
};

const MessageChannelCard: React.FC<{
  channel: ChannelDefinition;
  status: MessageChannelStatus;
  statusLoading: boolean;
  onPaired: (channel: MessageChannelId) => void;
  onUnpaired: () => Promise<void>;
}> = ({ channel, status, statusLoading, onPaired, onUnpaired }) => {
  const { t } = useTranslation();
  const [phase, setPhase] = useState<'idle' | 'starting' | 'waiting' | 'scanned' | 'success' | 'expired' | 'error'>(
    'idle'
  );
  const [qrCodeUrl, setQrCodeUrl] = useState('');
  const [unpairing, setUnpairing] = useState(false);
  const [unpairDialogOpen, setUnpairDialogOpen] = useState(false);
  const [unpairError, setUnpairError] = useState(false);
  const mountedRef = useRef(true);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const controllerRef = useRef<AbortController | null>(null);

  const stopPolling = useCallback(() => {
    if (timerRef.current !== null) {
      clearTimeout(timerRef.current);
      timerRef.current = null;
    }
    controllerRef.current?.abort();
    controllerRef.current = null;
  }, []);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      stopPolling();
    };
  }, [stopPolling]);

  const pollOnce = useCallback(
    async (key: string, controller: AbortController) => {
      if (!mountedRef.current) return;
      try {
        const result = await pollMessageChannelQr(channel.id, key, controller.signal);
        if (!mountedRef.current || controllerRef.current !== controller) return;
        if (result.status === 'confirmed' && result.connected) {
          stopPolling();
          setPhase('success');
          onPaired(channel.id);
          return;
        }
        if (result.status === 'expired') {
          stopPolling();
          setPhase('expired');
          return;
        }
        if (result.status === 'confirmed') {
          stopPolling();
          setPhase('error');
          return;
        }
        if (result.status === 'error') {
          stopPolling();
          setPhase('error');
          return;
        }
        setPhase(result.status === 'scanned' ? 'scanned' : 'waiting');
        timerRef.current = setTimeout(() => void pollOnce(key, controller), 450);
      } catch (requestError) {
        if (!mountedRef.current || controller.signal.aborted || controllerRef.current !== controller) return;
        console.error(`Failed to poll ${channel.id} QR pairing:`, messageChannelErrorMessage(requestError));
        stopPolling();
        setPhase('error');
      }
    },
    [channel.id, onPaired, stopPolling]
  );

  const start = useCallback(async () => {
    stopPolling();
    setPhase('starting');
    setQrCodeUrl('');
    const key = createSessionKey();
    const controller = new AbortController();
    controllerRef.current = controller;
    try {
      const result = await startMessageChannelQr(channel.id, key, controller.signal);
      if (!mountedRef.current) return;
      setQrCodeUrl(result.qrCodeUrl);
      setPhase('waiting');
      void pollOnce(result.sessionKey, controller);
    } catch (requestError) {
      if (!mountedRef.current || controller.signal.aborted) return;
      console.error(`Failed to start ${channel.id} QR pairing:`, messageChannelErrorMessage(requestError));
      setPhase('error');
    }
  }, [channel.id, pollOnce, stopPolling]);

  const reset = useCallback(() => {
    stopPolling();
    setPhase('idle');
    setQrCodeUrl('');
  }, [stopPolling]);

  const requestUnpair = useCallback(() => {
    setUnpairError(false);
    setUnpairDialogOpen(true);
  }, []);

  const confirmUnpair = useCallback(async () => {
    setUnpairError(false);
    setUnpairing(true);
    try {
      await unpairMessageChannel(channel.id);
      reset();
      await onUnpaired();
      if (mountedRef.current) setUnpairDialogOpen(false);
    } catch (requestError) {
      console.error(`Failed to unpair ${channel.id}:`, messageChannelErrorMessage(requestError));
      if (mountedRef.current) setUnpairError(true);
    } finally {
      if (mountedRef.current) setUnpairing(false);
    }
  }, [channel.id, onUnpaired, reset]);

  const isActive = phase === 'starting' || phase === 'waiting' || phase === 'scanned';
  const isPaired = status.paired || phase === 'success';
  const statusClass = isPaired ? 'success' : isActive ? 'active' : undefined;
  const statusLabel = isActive
    ? phase === 'starting'
      ? t('settings.generalSettings.messageChannels.loading')
      : phase === 'scanned'
        ? t('settings.generalSettings.messageChannels.scannedWaiting')
        : t('settings.generalSettings.messageChannels.waitingForScan')
    : isPaired
      ? t('settings.generalSettings.messageChannels.paired')
      : statusLoading
        ? t('settings.generalSettings.messageChannels.loading')
        : status.configured
          ? t('settings.generalSettings.messageChannels.configured')
          : t('settings.generalSettings.messageChannels.notConfigured');

  return (
    <article className='message-channel-card' data-testid={`message-channel-card-${channel.id}`}>
      <div className='message-channel-card__header'>
        <span className='message-channel-card__logo' aria-hidden='true'>
          <img src={channel.logo} alt='' />
        </span>
        <div className='message-channel-card__heading'>
          <h3 className='message-channel-card__title'>
            {t(`settings.generalSettings.messageChannels.${channel.nameKey}`)}
          </h3>
          <p className='message-channel-card__description'>
            {t(`settings.generalSettings.messageChannels.${channel.descriptionKey}`)}
          </p>
        </div>
        <span
          className={`message-channel-card__status${statusClass ? ` message-channel-card__status--${statusClass}` : ''}`}
        >
          {statusLabel}
        </span>
      </div>
      <div className='message-channel-card__body' aria-live='polite'>
        {qrCodeUrl && isActive ? (
          <div className='message-channel-card__qr-frame' data-testid={`message-channel-qr-${channel.id}`}>
            <img
              src={qrCodeUrl}
              alt={t('settings.generalSettings.messageChannels.qrAlt')}
              referrerPolicy='no-referrer'
            />
          </div>
        ) : phase === 'success' || isPaired ? (
          <div className='message-channel-card__placeholder' data-testid={`message-channel-success-${channel.id}`}>
            <span
              className='message-channel-card__success-mark'
              aria-label={t('settings.generalSettings.messageChannels.connected')}
            >
              ✓
            </span>
          </div>
        ) : phase === 'starting' ? (
          <div className='message-channel-card__placeholder'>
            <Spin size={22} />
          </div>
        ) : phase === 'error' ? (
          <div className='message-channel-card__error' role='alert'>
            {t('settings.generalSettings.messageChannels.requestFailed')}
          </div>
        ) : (
          <div className='message-channel-card__placeholder'>
            {phase === 'expired'
              ? t('settings.generalSettings.messageChannels.expired')
              : t('settings.generalSettings.messageChannels.scanHint')}
          </div>
        )}
        {phase === 'scanned' ? (
          <div className='message-channel-card__status-copy'>
            {t('settings.generalSettings.messageChannels.scannedWaiting')}
          </div>
        ) : phase === 'waiting' ? (
          <div className='message-channel-card__status-copy'>
            {t('settings.generalSettings.messageChannels.waitingForScan')}
          </div>
        ) : null}
        <div className='message-channel-card__footer'>
          {isActive ? (
            <button type='button' className='message-channel-card__secondary-button' onClick={reset}>
              {t('settings.generalSettings.messageChannels.cancel')}
            </button>
          ) : null}
          {isPaired && !isActive ? (
            <button
              type='button'
              className='message-channel-card__danger-button'
              onClick={requestUnpair}
              disabled={unpairing}
            >
              {t('settings.generalSettings.messageChannels.unpair')}
            </button>
          ) : null}
          <button
            type='button'
            className={isActive ? 'message-channel-card__secondary-button' : 'message-channel-card__primary-button'}
            onClick={() => void start()}
            disabled={isActive || unpairing}
          >
            {phase === 'expired' || phase === 'error' || isPaired
              ? t('settings.generalSettings.messageChannels.regenerate')
              : t('settings.generalSettings.messageChannels.scanToPair')}
          </button>
        </div>
      </div>
      <AccessibleActionDialog
        title={t('settings.generalSettings.messageChannels.unpairTitle', {
          channel: t(`settings.generalSettings.messageChannels.${channel.nameKey}`),
        })}
        visible={unpairDialogOpen}
        confirmText={t('settings.generalSettings.messageChannels.unpair')}
        cancelText={t('settings.generalSettings.messageChannels.cancel')}
        busy={unpairing}
        danger
        initialFocus='cancel'
        onCancel={() => {
          if (!unpairing) setUnpairDialogOpen(false);
        }}
        onConfirm={confirmUnpair}
      >
        <p className='m-0'>
          {t('settings.generalSettings.messageChannels.unpairConfirm', {
            channel: t(`settings.generalSettings.messageChannels.${channel.nameKey}`),
          })}
        </p>
        {unpairError ? (
          <p className='m-t-12px m-b-0 text-danger' role='alert'>
            {t('settings.generalSettings.messageChannels.unpairFailed')}
          </p>
        ) : null}
      </AccessibleActionDialog>
    </article>
  );
};

function createSessionKey(): string {
  if (typeof crypto === 'undefined') throw new Error('Secure randomness is unavailable');
  if (typeof crypto.randomUUID === 'function') return crypto.randomUUID();
  if (typeof crypto.getRandomValues !== 'function') throw new Error('Secure randomness is unavailable');
  const values = new Uint32Array(2);
  crypto.getRandomValues(values);
  return `message-channel-${Date.now()}-${Array.from(values, (value) => value.toString(16).padStart(8, '0')).join('')}`;
}

export default MessageChannelsSettings;

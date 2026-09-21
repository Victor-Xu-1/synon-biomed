/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import React, { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Message, Tooltip } from '@arco-design/web-react';
import { ArrowCircleLeft, CloseOne, Down, SettingTwo, UpdateRotation, Wallet } from '@icon-park/react';
import classNames from 'classnames';
import type { SiderTooltipProps } from '@renderer/utils/ui/siderTooltip';
import { checkSynonBiomedRuntimeUpdate } from '@renderer/services/synonBiomedRuntimeUpdate';

interface SiderFooterProps {
  isMobile: boolean;
  isSettings: boolean;
  collapsed?: boolean;
  username?: string;
  avatarDataUrl?: string | null;
  siderTooltipProps: SiderTooltipProps;
  onSettingsClick: () => void;
  onAccountClick: () => void;
  onPlansUsageClick: () => void;
  onSettingsIntent?: () => void;
  showLogout?: boolean;
  onLogoutClick?: () => void;
}

const formatUsername = (username: string | undefined, fallback: string) => {
  const normalized = username?.trim();
  if (!normalized) return fallback;
  return `${normalized.charAt(0).toUpperCase()}${normalized.slice(1)}`;
};

const SiderFooter: React.FC<SiderFooterProps> = ({
  isMobile,
  isSettings,
  collapsed = false,
  username,
  avatarDataUrl,
  siderTooltipProps,
  onSettingsClick,
  onAccountClick,
  onPlansUsageClick,
  onSettingsIntent,
  showLogout = false,
  onLogoutClick,
}) => {
  const { t } = useTranslation();
  const [menuOpen, setMenuOpen] = useState(false);
  const [checkingUpdates, setCheckingUpdates] = useState(false);
  const footerRef = useRef<HTMLDivElement | null>(null);
  const displayName = formatUsername(username, t('settings.synonBiomedLocalUser'));
  const avatarLetter = displayName.charAt(0).toUpperCase();
  const settingsLabel = isSettings ? t('common.back') : t('common.settings');

  useEffect(() => {
    if (!menuOpen) return;

    const handlePointerDown = (event: MouseEvent) => {
      if (!footerRef.current?.contains(event.target as Node)) {
        setMenuOpen(false);
      }
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setMenuOpen(false);
      }
    };

    document.addEventListener('mousedown', handlePointerDown);
    window.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('mousedown', handlePointerDown);
      window.removeEventListener('keydown', handleKeyDown);
    };
  }, [menuOpen]);

  const runAndClose = (action?: () => void) => {
    setMenuOpen(false);
    action?.();
  };

  const checkForUpdates = async () => {
    if (checkingUpdates) return;
    setMenuOpen(false);
    setCheckingUpdates(true);
    try {
      const status = await checkSynonBiomedRuntimeUpdate();
      if (status.latest) {
        Message.info(t('settings.synonBiomedUpdateAvailable', { version: status.latest }));
      } else {
        Message.success(t('settings.synonBiomedUpToDate', { version: status.current }));
      }
    } catch (error) {
      console.error('Failed to check for Synon Biomed updates:', error);
      Message.error(t('settings.synonBiomedUpdateCheckFailed'));
    } finally {
      setCheckingUpdates(false);
    }
  };

  const toggleMenu = () => {
    const nextOpen = !menuOpen;
    if (nextOpen) onSettingsIntent?.();
    setMenuOpen(nextOpen);
  };

  return (
    <div ref={footerRef} className='relative shrink-0 sider-footer mt-auto px-8px py-8px'>
      {menuOpen && (
        <div
          role='menu'
          aria-label={t('settings.synonBiomedAccountMenu')}
          className={classNames(
            'app-overlay-menu absolute z-40 bottom-[calc(100%+6px)] p-6px',
            collapsed ? 'left-6px w-184px' : 'left-8px right-8px'
          )}
        >
          <button
            type='button'
            role='menuitem'
            data-testid='synon-account-profile'
            className='w-full px-10px py-8px mb-4px border-x-0 border-t-0 border-b border-solid border-[var(--color-border-2)] bg-transparent rd-6px text-left cursor-pointer hover:bg-fill-3'
            onClick={() => runAndClose(onAccountClick)}
          >
            <div className='flex items-center gap-9px'>
              <span className='size-28px flex items-center justify-center shrink-0 rd-full bg-[rgba(var(--primary-6),0.12)] text-[rgb(var(--primary-6))] text-12px font-[600]'>
                {avatarDataUrl ? (
                  <img src={avatarDataUrl} alt='' className='size-full rd-full object-cover' />
                ) : (
                  avatarLetter
                )}
              </span>
              <span className='min-w-0'>
                <span className='block truncate text-13px font-[600] text-t-primary'>{displayName}</span>
              </span>
            </div>
          </button>
          <button
            type='button'
            role='menuitem'
            className='w-full h-36px px-10px flex items-center gap-10px border-none bg-transparent rd-6px text-14px text-t-primary cursor-pointer hover:bg-fill-3'
            onPointerEnter={onSettingsIntent}
            onFocus={onSettingsIntent}
            onClick={() => runAndClose(onSettingsClick)}
          >
            {isSettings ? <ArrowCircleLeft theme='outline' size={16} /> : <SettingTwo theme='outline' size={16} />}
            <span>{settingsLabel}</span>
          </button>
          <button
            type='button'
            role='menuitem'
            className='w-full h-36px px-10px flex items-center gap-10px border-none bg-transparent rd-6px text-14px text-t-primary cursor-pointer hover:bg-fill-3'
            onClick={() => runAndClose(onPlansUsageClick)}
          >
            <Wallet theme='outline' size={16} />
            <span>{t('settings.plansUsageSettings.menuLabel')}</span>
          </button>
          <button
            type='button'
            role='menuitem'
            disabled={checkingUpdates}
            className='w-full h-36px px-10px flex items-center gap-10px border-none bg-transparent rd-6px text-14px text-t-primary cursor-pointer hover:bg-fill-3 disabled:cursor-wait disabled:opacity-60'
            onClick={() => void checkForUpdates()}
          >
            <UpdateRotation theme='outline' size={16} className={checkingUpdates ? 'animate-spin' : undefined} />
            <span>{t('settings.synonBiomedCheckUpdates')}</span>
          </button>
          {showLogout && onLogoutClick && (
            <button
              type='button'
              role='menuitem'
              className='w-full h-36px px-10px flex items-center gap-10px border-none bg-transparent rd-6px text-14px text-t-primary cursor-pointer hover:bg-fill-3'
              onClick={() => runAndClose(onLogoutClick)}
            >
              <CloseOne theme='outline' size={16} />
              <span>{t('settings.googleLogout')}</span>
            </button>
          )}
        </div>
      )}

      <Tooltip {...siderTooltipProps} content={displayName} position='right'>
        <button
          type='button'
          aria-label={t('settings.synonBiomedOpenAccount')}
          aria-haspopup='menu'
          aria-expanded={menuOpen}
          className={classNames(
            'w-full min-w-0 border-none bg-transparent flex items-center rd-8px cursor-pointer text-t-primary transition-colors hover:bg-fill-3 active:bg-fill-4',
            collapsed ? 'h-40px justify-center px-0' : 'h-48px gap-10px px-8px',
            isMobile && 'sider-footer-btn-mobile'
          )}
          onClick={toggleMenu}
        >
          <span className='size-30px flex items-center justify-center shrink-0 rd-full bg-[rgba(var(--primary-6),0.12)] text-[rgb(var(--primary-6))] text-13px font-[600]'>
            {avatarDataUrl ? (
              <img src={avatarDataUrl} alt='' className='size-full rd-full object-cover' />
            ) : (
              avatarLetter
            )}
          </span>
          {!collapsed && (
            <>
              <span className='min-w-0 flex-1 text-left'>
                <span className='block text-14px leading-20px font-[500] truncate'>{displayName}</span>
              </span>
              <Down
                theme='outline'
                size={13}
                className={classNames('shrink-0 text-t-tertiary transition-transform', menuOpen && 'rotate-180')}
              />
            </>
          )}
        </button>
      </Tooltip>
    </div>
  );
};

export default SiderFooter;

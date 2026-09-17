import classNames from 'classnames';
import { Loading } from '@icon-park/react';
import React, { Suspense, useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useLocation, useNavigate } from 'react-router';
import { usePreviewContext } from '@renderer/pages/conversation/Preview/context/PreviewContext';
import { cleanupSiderTooltips, getSiderTooltipProps } from '@renderer/utils/ui/siderTooltip';
import { useAuth } from '@renderer/hooks/context/AuthContext';
import { useLayoutContext } from '@renderer/hooks/context/LayoutContext';
import { blurActiveElement } from '@renderer/utils/ui/focus';
import { useThemeContext } from '@renderer/hooks/context/ThemeContext';
import { useSynonBiomedUserProfile } from '@renderer/hooks/useSynonBiomedUserProfile';
import { SiderSearchEntry } from './SiderNav';
import SiderFooter from './SiderFooter';
import siderStyles from './Sider.module.css';
import { loadSettingsSider, prefetchSettingsRoute } from '../routeModules';

const WorkspaceGroupedHistory = React.lazy(() => import('@renderer/pages/conversation/GroupedHistory'));
const SettingsSider = React.lazy(loadSettingsSider);

export const SiderHistoryLoading: React.FC = () => {
  const { t } = useTranslation();

  return (
    <div
      data-testid='sider-history-loading'
      role='status'
      aria-label={t('common.loading')}
      className='flex items-center gap-6px px-12px py-12px text-12px text-t-tertiary'
    >
      <span className='size-14px shrink-0 flex items-center justify-center' aria-hidden='true'>
        <Loading theme='outline' size={14} className='animate-spin' />
      </span>
      <span>{t('common.loading')}</span>
    </div>
  );
};

interface SiderProps {
  onSessionClick?: () => void;
  collapsed?: boolean;
}

const Sider: React.FC<SiderProps> = ({ onSessionClick, collapsed = false }) => {
  const layout = useLayoutContext();
  const isMobile = layout?.isMobile ?? false;
  const location = useLocation();
  const { pathname, search, hash } = location;

  const navigate = useNavigate();
  const { closePreview } = usePreviewContext();
  const { logout, status, user } = useAuth();
  const { profile: userProfile } = useSynonBiomedUserProfile(user?.id, user?.username ?? 'User');
  const { theme, setTheme } = useThemeContext();
  const [isBatchMode, setIsBatchMode] = useState(false);
  const isSettings = pathname.startsWith('/settings');
  const lastNonSettingsPathRef = useRef('/guid');
  const showLogout = status === 'authenticated';

  useEffect(() => {
    if (!pathname.startsWith('/settings')) {
      lastNonSettingsPathRef.current = `${pathname}${search}${hash}`;
    }
  }, [pathname, search, hash]);

  const handleNewChat = (projectId?: string) => {
    cleanupSiderTooltips();
    blurActiveElement();
    closePreview();
    setIsBatchMode(false);
    Promise.resolve(
      navigate('/guid', {
        state: {
          resetAssistant: true,
          ...(projectId ? { workspace: `synonbiomed://project/${encodeURIComponent(projectId)}` } : {}),
        },
      })
    ).catch((error) => {
      console.error('Navigation failed:', error);
    });
    if (onSessionClick) {
      onSessionClick();
    }
  };

  const handleSettingsClick = () => {
    cleanupSiderTooltips();
    blurActiveElement();
    if (isSettings) {
      const target = lastNonSettingsPathRef.current || '/guid';
      Promise.resolve(navigate(target)).catch((error) => {
        console.error('Navigation failed:', error);
      });
    } else {
      void prefetchSettingsRoute('general').catch((): undefined => undefined);
      Promise.resolve(navigate('/settings/general')).catch((error) => {
        console.error('Navigation failed:', error);
      });
    }
    if (onSessionClick) {
      onSessionClick();
    }
  };

  const handleSettingsIntent = useCallback(() => {
    void prefetchSettingsRoute('general').catch((): undefined => undefined);
  }, []);

  const handleAccountClick = () => {
    cleanupSiderTooltips();
    blurActiveElement();
    void prefetchSettingsRoute('account').catch((): undefined => undefined);
    Promise.resolve(navigate('/settings/account')).catch((error) => {
      console.error('Navigation failed:', error);
    });
    if (onSessionClick) {
      onSessionClick();
    }
  };

  const handlePlansUsageClick = () => {
    cleanupSiderTooltips();
    blurActiveElement();
    void prefetchSettingsRoute('plans-usage').catch((): undefined => undefined);
    Promise.resolve(navigate('/settings/plans-usage')).catch((error) => {
      console.error('Navigation failed:', error);
    });
    if (onSessionClick) {
      onSessionClick();
    }
  };

  const handleConversationSelect = () => {
    cleanupSiderTooltips();
    blurActiveElement();
    closePreview();
    setIsBatchMode(false);
  };

  const handleQuickThemeToggle = () => {
    void setTheme(theme === 'dark' ? 'light' : 'dark');
  };

  const handleLogout = useCallback(async () => {
    cleanupSiderTooltips();
    blurActiveElement();
    closePreview();
    try {
      await logout();
    } catch (error) {
      console.error('Logout failed:', error);
      return; // logout 失败时不执行后续操作
    }
    if (onSessionClick) {
      onSessionClick();
    }
  }, [closePreview, logout, onSessionClick]);

  useEffect(() => {
    if (!showLogout) return;

    const handleKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.shiftKey && event.key.toLowerCase() === 'l') {
        event.preventDefault();
        handleLogout();
      }
    };

    window.addEventListener('keydown', handleKeyDown);
    return () => {
      window.removeEventListener('keydown', handleKeyDown);
    };
  }, [handleLogout, showLogout]);

  const tooltipEnabled = collapsed && !isMobile;
  const siderTooltipProps = getSiderTooltipProps(tooltipEnabled);

  const workspaceHistoryProps = {
    collapsed,
    tooltipEnabled,
    onSessionClick,
    batchMode: isBatchMode,
    onBatchModeChange: setIsBatchMode,
  };
  return (
    <div className='size-full flex flex-col'>
      {/* Main content area */}
      <div className='flex-1 min-h-0 overflow-hidden'>
        {isSettings ? (
          <Suspense fallback={<div className='size-full' />}>
            <SettingsSider collapsed={collapsed} tooltipEnabled={tooltipEnabled} />
          </Suspense>
        ) : (
          <div className='size-full flex flex-col gap-2px'>
            {/* Search entry — desktop moves this into the titlebar toolbar;
                mobile keeps it here in the sidebar. */}
            {isMobile && (
              <SiderSearchEntry
                isMobile={isMobile}
                collapsed={collapsed}
                siderTooltipProps={siderTooltipProps}
                onConversationSelect={handleConversationSelect}
                onSessionClick={onSessionClick}
              />
            )}
            {/* Divider between fixed top nav and scrollable content area */}
            {isMobile && (
              <div
                className={classNames(
                  'shrink-0 mt-6px mb-2px h-1px bg-[var(--color-border-2)]',
                  collapsed ? 'mx-6px' : 'mx-10px'
                )}
              />
            )}
            {/* Scrollable content: pinned → projects → conversations */}
            <div
              className={classNames('flex-1 min-h-0 overflow-y-auto', siderStyles.scrollArea)}
              data-testid='sider-history-scroll'
            >
              <Suspense fallback={<SiderHistoryLoading />}>
                <WorkspaceGroupedHistory {...workspaceHistoryProps} onStartTask={handleNewChat} />
              </Suspense>
            </div>
          </div>
        )}
      </div>
      {/* Footer */}
      <SiderFooter
        isMobile={isMobile}
        isSettings={isSettings}
        collapsed={collapsed}
        theme={theme}
        username={userProfile.displayName}
        avatarDataUrl={userProfile.avatarDataUrl}
        siderTooltipProps={siderTooltipProps}
        onSettingsClick={handleSettingsClick}
        onAccountClick={handleAccountClick}
        onPlansUsageClick={handlePlansUsageClick}
        onSettingsIntent={handleSettingsIntent}
        onThemeToggle={handleQuickThemeToggle}
        showLogout={showLogout}
        onLogoutClick={handleLogout}
      />
    </div>
  );
};

export default Sider;

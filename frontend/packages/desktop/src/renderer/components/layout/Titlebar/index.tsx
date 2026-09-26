import React, { useEffect, useMemo, useRef, useState } from 'react';
import classNames from 'classnames';
import { ArrowCircleLeft, ArrowLeft, ArrowRight, ExpandLeft, ExpandRight, Search, Star } from '@icon-park/react';
import { Tooltip } from '@arco-design/web-react';
import { useTranslation } from 'react-i18next';
import { useLocation, useNavigate } from 'react-router';

import ConversationSearchPopover from '@renderer/pages/conversation/GroupedHistory/ConversationSearchPopover';
import { getConversationOrNull } from '@/renderer/pages/conversation/utils/conversationCache';
import MobileConversationBrand from './MobileConversationBrand';
import WindowControls from '../WindowControls';
import {
  WORKSPACE_STATE_EVENT,
  dispatchWorkspaceStateRequestEvent,
  dispatchWorkspaceToggleEvent,
} from '@renderer/utils/workspace/workspaceEvents';
import type { WorkspaceStateDetail } from '@renderer/utils/workspace/workspaceEvents';
import { useLayoutContext } from '@/renderer/hooks/context/LayoutContext';
import { useNavigationHistory } from '@/renderer/hooks/context/NavigationHistoryContext';
import { isElectronDesktop, isMacOS, isWindows, openExternalUrl } from '@/renderer/utils/platform';
import { resolveWorkspaceToggleOwner } from '@/renderer/utils/workspace/workspaceToggleOwnership';
import './titlebar.css';
import ProjectCommandPalette from './ProjectCommandPalette';
import DesktopConversationTitle from './DesktopConversationTitle';

interface TitlebarProps {
  workspaceAvailable: boolean;
}

// Claude-desktop-style sidebar toggle icon: a rounded rectangle with a vertical divider
// near the left edge, indicating a collapsible side panel. Rendered as inline SVG since
// @icon-park doesn't ship this exact shape.
//
// Uses a 48-unit viewBox to match @icon-park's stroke scale, so passing the same
// `strokeWidth` value here and to @icon-park icons produces visually identical lines.
//
// The rect spans y=10..38 (height 28), slightly taller than @icon-park's
// ArrowLeft/ArrowRight (which span y=12..36) so the sidebar icon reads a
// touch larger. The rect remains centered at y=24, matching the arrows'
// centerline so all three icons stay on the same visual baseline.
const SidebarIcon: React.FC<{ size?: number; strokeWidth?: number }> = ({ size = 18, strokeWidth = 4 }) => (
  <svg
    width={size}
    height={size}
    viewBox='0 0 48 48'
    fill='none'
    stroke='currentColor'
    strokeWidth={strokeWidth}
    strokeLinecap='round'
    strokeLinejoin='round'
    aria-hidden='true'
    focusable='false'
  >
    <rect x='6' y='10' width='36' height='28' rx='5' />
    <line x1='18' y1='10' x2='18' y2='38' />
  </svg>
);

const GITHUB_REPO_URL = 'https://github.com/Victor-Xu-1/synon-biomed';

/**
 * Star shortcut rendered right after the history-forward arrow: hovering
 * explains the action and clicking sends the user to the GitHub repository
 * where they can star it. (Starring on the user's behalf would require their
 * GitHub credentials, which the app intentionally never touches, so we jump
 * instead.) It uses the same @icon-park icon set, size and stroke width as
 * the adjacent navigation arrows so the whole row stays optically uniform.
 */
const GitHubStarButton: React.FC<{ iconSize: number; iconStroke?: number }> = ({ iconSize, iconStroke }) => {
  const { t } = useTranslation();
  return (
    <Tooltip content={t('common.starOnGitHub')} position='bottom'>
      <button
        type='button'
        className='app-titlebar__button app-titlebar__button--nav synon-biomed-github-star'
        data-testid='github-star-button'
        aria-label={t('common.starOnGitHub')}
        onClick={() => {
          void openExternalUrl(GITHUB_REPO_URL);
        }}
      >
        <Star theme='outline' size={iconSize} fill='currentColor' strokeWidth={iconStroke} />
      </button>
    </Tooltip>
  );
};

const Titlebar: React.FC<TitlebarProps> = ({ workspaceAvailable }) => {
  const { t } = useTranslation();
  const appTitle = useMemo(() => 'Synon Biomed', []);
  const [workspaceCollapsed, setWorkspaceCollapsed] = useState(true);
  const [workspaceStateReady, setWorkspaceStateReady] = useState(false);
  const [mobileCenterTitle, setMobileCenterTitle] = useState(appTitle);
  const [mobileCenterOffset, setMobileCenterOffset] = useState(0);
  const layout = useLayoutContext();
  const navigationHistory = useNavigationHistory();
  const location = useLocation();
  const navigate = useNavigate();
  const containerRef = useRef<HTMLDivElement | null>(null);
  const menuRef = useRef<HTMLDivElement | null>(null);
  const toolbarRef = useRef<HTMLDivElement | null>(null);
  const lastNonSettingsPathRef = useRef('/guid');

  // Keep the workspace toggle icon synchronized with the preview panel.
  useEffect(() => {
    if (typeof window === 'undefined') {
      return undefined;
    }
    setWorkspaceStateReady(false);
    const handler = (event: Event) => {
      const customEvent = event as CustomEvent<WorkspaceStateDetail>;
      if (typeof customEvent.detail?.collapsed === 'boolean') {
        setWorkspaceCollapsed(customEvent.detail.collapsed);
        setWorkspaceStateReady(true);
      }
    };
    window.addEventListener(WORKSPACE_STATE_EVENT, handler as EventListener);
    dispatchWorkspaceStateRequestEvent();
    return () => {
      window.removeEventListener(WORKSPACE_STATE_EVENT, handler as EventListener);
    };
  }, [location.pathname]);

  const isDesktopRuntime = isElectronDesktop();
  const isMacRuntime = isDesktopRuntime && isMacOS();
  const showWindowControls = isDesktopRuntime && !isMacRuntime;
  const workspaceToggleOwner = resolveWorkspaceToggleOwner({
    isElectronDesktop: isDesktopRuntime,
    isMac: isMacRuntime,
    isWindows: isWindows(),
  });
  const showWorkspaceButton = workspaceAvailable && workspaceToggleOwner === 'titlebar';

  const workspaceTooltip = workspaceCollapsed ? t('common.expandWorkspace') : t('common.collapseWorkspace');
  const backToChatTooltip = t('common.back');
  const isSettingsRoute = location.pathname.startsWith('/settings');
  const iconSize = 18;
  // Desktop uses slimmer strokes to match macOS-native chrome aesthetics;
  // mobile keeps the default weight so icons stay legible at larger sizes.
  const desktopIconStroke = layout?.isMobile ? undefined : 2.5;
  // Keep the primary sidebar toggle in the titlebar.
  const showSiderToggle = Boolean(layout?.setSiderCollapsed) && !(layout?.isMobile && isSettingsRoute);
  const showBackToChatButton = Boolean(layout?.isMobile && isSettingsRoute);
  const siderTooltip = layout?.siderCollapsed ? t('common.expandSidebar') : t('common.collapseSidebar');
  // Desktop has room for navigation history; mobile keeps the back-to-chat action.
  const showHistoryNav = Boolean(navigationHistory) && !layout?.isMobile;
  const historyBackTooltip = t('common.historyBack');
  const historyForwardTooltip = t('common.forward');
  // Conversation search moved from the sidebar into the titlebar toolbar
  // Desktop search lives between the sidebar toggle and history navigation.
  const showSearchButton = !layout?.isMobile;
  const searchTooltip = t('conversation.historySearch.tooltip');
  const conversationMatch = location.pathname.match(/^\/conversation\/([^/]+)/);
  const conversationId = conversationMatch?.[1];

  const handleSiderToggle = () => {
    if (!showSiderToggle || !layout?.setSiderCollapsed) return;
    layout.setSiderCollapsed(!layout.siderCollapsed);
  };

  const handleWorkspaceToggle = () => {
    if (!workspaceAvailable) {
      return;
    }
    dispatchWorkspaceToggleEvent();
  };

  const handleBackToChat = () => {
    const target = lastNonSettingsPathRef.current;
    if (target && !target.startsWith('/settings')) {
      void navigate(target);
      return;
    }
    void navigate(-1);
  };

  useEffect(() => {
    if (!isSettingsRoute) {
      const path = `${location.pathname}${location.search}${location.hash}`;
      lastNonSettingsPathRef.current = path;
      try {
        sessionStorage.setItem('synon-ai:last-non-settings-path', path);
      } catch {
        // ignore
      }
      return;
    }
    try {
      const stored = sessionStorage.getItem('synon-ai:last-non-settings-path');
      if (stored) {
        lastNonSettingsPathRef.current = stored;
      }
    } catch {
      // ignore
    }
  }, [isSettingsRoute, location.pathname, location.search, location.hash]);

  useEffect(() => {
    if (!layout?.isMobile) {
      setMobileCenterTitle(appTitle);
      return;
    }

    // Single agent mode: show conversation name
    const match = location.pathname.match(/^\/conversation\/([^/]+)/);
    const conversation_id = match?.[1];
    if (!conversation_id) {
      setMobileCenterTitle(appTitle);
      return;
    }

    let cancelled = false;
    void getConversationOrNull(conversation_id)
      .then((conversation) => {
        if (cancelled) return;
        setMobileCenterTitle(conversation?.name || appTitle);
      })
      .catch(() => {
        if (cancelled) return;
        setMobileCenterTitle(appTitle);
      });

    return () => {
      cancelled = true;
    };
  }, [appTitle, layout?.isMobile, location.pathname]);

  useEffect(() => {
    if (!layout?.isMobile) {
      setMobileCenterOffset(0);
      return;
    }

    const updateOffset = () => {
      const leftWidth = menuRef.current?.offsetWidth || 0;
      const rightWidth = toolbarRef.current?.offsetWidth || 0;
      setMobileCenterOffset((leftWidth - rightWidth) / 2);
    };

    updateOffset();

    if (typeof ResizeObserver === 'undefined') {
      window.addEventListener('resize', updateOffset);
      return () => window.removeEventListener('resize', updateOffset);
    }

    const observer = new ResizeObserver(() => updateOffset());
    if (containerRef.current) observer.observe(containerRef.current);
    if (menuRef.current) observer.observe(menuRef.current);
    if (toolbarRef.current) observer.observe(toolbarRef.current);

    return () => observer.disconnect();
  }, [layout?.isMobile, showBackToChatButton, showWorkspaceButton, mobileCenterTitle]);

  const mobileCenterStyle = layout?.isMobile
    ? ({
        '--app-titlebar-mobile-center-offset': `${workspaceAvailable ? mobileCenterOffset : 0}px`,
      } as React.CSSProperties)
    : undefined;

  const menuStyle: React.CSSProperties = useMemo(() => {
    if (!isMacRuntime || !showSiderToggle) return {};
    // macOS: sit the menu buttons right next to the traffic lights (which occupy ~70px).
    // Mobile keeps its own layout (no traffic lights).
    const marginLeft = layout?.isMobile ? '0px' : '76px';
    return {
      marginLeft,
    };
  }, [isMacRuntime, showSiderToggle, layout?.isMobile]);

  return (
    <>
      <div
        ref={containerRef}
        style={mobileCenterStyle}
        className={classNames('flex items-center gap-8px app-titlebar bg-2 border-b border-[var(--border-base)]', {
          'app-titlebar--mobile': layout?.isMobile,
          'app-titlebar--mobile-conversation': layout?.isMobile && workspaceAvailable,
          'app-titlebar--desktop': isDesktopRuntime,
          'app-titlebar--mac': isMacRuntime,
        })}
      >
        <div ref={menuRef} className='app-titlebar__menu' style={menuStyle}>
          {showBackToChatButton && (
            <button
              type='button'
              className={classNames('app-titlebar__button', layout?.isMobile && 'app-titlebar__button--mobile')}
              onClick={handleBackToChat}
              aria-label={backToChatTooltip}
            >
              <ArrowCircleLeft theme='outline' size={iconSize} fill='currentColor' />
            </button>
          )}
          {showSiderToggle && (
            <button
              type='button'
              data-testid='sider-toggle'
              className={classNames('app-titlebar__button', layout?.isMobile && 'app-titlebar__button--mobile')}
              onClick={handleSiderToggle}
              aria-label={siderTooltip}
              aria-expanded={!layout?.siderCollapsed}
            >
              <SidebarIcon size={iconSize} strokeWidth={desktopIconStroke} />
            </button>
          )}
          {showSearchButton && (
            <ConversationSearchPopover
              renderTrigger={({ onClick }) => (
                <button
                  type='button'
                  className='app-titlebar__button'
                  onClick={onClick}
                  aria-label={searchTooltip}
                  title={searchTooltip}
                >
                  <Search
                    theme='outline'
                    size={iconSize}
                    fill='currentColor'
                    strokeWidth={desktopIconStroke}
                    className='block leading-none'
                    style={{ lineHeight: 0 }}
                  />
                </button>
              )}
            />
          )}
          {showHistoryNav && (
            <>
              <button
                type='button'
                className='app-titlebar__button app-titlebar__button--nav'
                onClick={() => navigationHistory?.back()}
                disabled={!navigationHistory?.canBack}
                aria-label={historyBackTooltip}
                title={historyBackTooltip}
              >
                <ArrowLeft theme='outline' size={iconSize} fill='currentColor' strokeWidth={desktopIconStroke} />
              </button>
              <button
                type='button'
                className='app-titlebar__button app-titlebar__button--nav'
                onClick={() => navigationHistory?.forward()}
                disabled={!navigationHistory?.canForward}
                aria-label={historyForwardTooltip}
                title={historyForwardTooltip}
              >
                <ArrowRight theme='outline' size={iconSize} fill='currentColor' strokeWidth={desktopIconStroke} />
              </button>
              <GitHubStarButton iconSize={iconSize} iconStroke={desktopIconStroke} />
            </>
          )}
        </div>
        <div
          className={classNames('app-titlebar__brand', {
            'app-titlebar__brand--centered': layout?.isMobile || !location.pathname.match(/^\/conversation\//),
          })}
          aria-label={layout?.isMobile ? mobileCenterTitle : conversationId ? undefined : appTitle}
          title={layout?.isMobile ? mobileCenterTitle : conversationId ? undefined : appTitle}
        >
          {layout?.isMobile ? (
            conversationId ? (
              <MobileConversationBrand conversation_id={conversationId} fallbackTitle={mobileCenterTitle} />
            ) : (
              <span className='app-titlebar__brand-mobile'>
                <span className='app-titlebar__brand-text'>{mobileCenterTitle}</span>
              </span>
            )
          ) : (
            conversationId && <DesktopConversationTitle conversationId={conversationId} fallbackTitle={appTitle} />
          )}
        </div>
        <div ref={toolbarRef} className='app-titlebar__toolbar'>
          {layout?.isMobile && <div id='app-titlebar-actions-slot' className='app-titlebar__actions-slot' />}
          {showWorkspaceButton && (
            <button
              type='button'
              className={classNames(
                'app-titlebar__button app-titlebar__button--workspace',
                layout?.isMobile && 'app-titlebar__button--mobile'
              )}
              onClick={handleWorkspaceToggle}
              aria-label={workspaceTooltip}
              title={workspaceTooltip}
              data-testid='workspace-toggle'
              disabled={!workspaceStateReady}
            >
              {workspaceCollapsed ? (
                <ExpandRight theme='outline' size={iconSize} fill='currentColor' />
              ) : (
                <ExpandLeft theme='outline' size={iconSize} fill='currentColor' />
              )}
            </button>
          )}
          {showWindowControls && <WindowControls />}
        </div>
      </div>
      <ProjectCommandPalette />
    </>
  );
};

export default Titlebar;

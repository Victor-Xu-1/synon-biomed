import type { PresetAssistantInfo } from '@/renderer/hooks/synonBiomed/runtime/usePresetAssistantInfo';
import { useLayoutContext } from '@/renderer/hooks/context/LayoutContext';
import { useResizableSplit } from '@/renderer/hooks/ui/useResizableSplit';
import MobileWorkspaceOverlay from './MobileWorkspaceOverlay';
import WorkspacePanelHeader, { DesktopWorkspaceToggle } from './WorkspacePanelHeader';
import { useContainerWidth } from '@/renderer/pages/conversation/hooks/useContainerWidth';
import { useLayoutConstraints } from '@/renderer/pages/conversation/hooks/useLayoutConstraints';
import { useWorkspaceCollapse } from '@/renderer/pages/conversation/hooks/useWorkspaceCollapse';
import { usePreviewContext } from '@/renderer/pages/conversation/Preview/context/PreviewContext';
import { dispatchWorkspaceToggleEvent } from '@/renderer/utils/workspace/workspaceEvents';
import { resolveWorkspaceToggleOwner } from '@/renderer/utils/workspace/workspaceToggleOwnership';
import classNames from 'classnames';
import { isMacEnvironment, isWindowsEnvironment } from '@/renderer/pages/conversation/utils/detectPlatform';
import { isElectronDesktop } from '@/renderer/utils/platform';
import {
  CHAT_PREVIEW_GAP_PX,
  DEFAULT_WORKSPACE_PANEL_PX,
  MAX_WORKSPACE_PANEL_PX,
  MIN_CHAT_PANEL_PX,
  MIN_PREVIEW_PANEL_PX,
  MIN_WORKSPACE_PANEL_PX,
  WORKSPACE_WIDTH_STORAGE_KEY,
  WORKSPACE_HEADER_HEIGHT,
  calcLayoutMetrics,
} from '@/renderer/pages/conversation/utils/layoutCalc';
import { Layout as ArcoLayout } from '@arco-design/web-react';
import { ExpandLeft, ExpandRight } from '@icon-park/react';
import React, { useEffect, useState } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import './chat-layout.css';

const PreviewPanel = React.lazy(
  () => import('@/renderer/pages/conversation/Preview/components/PreviewPanel/PreviewPanel')
);

// headerExtra allows injecting custom actions (e.g., model picker) into the header's right area
const ChatLayout: React.FC<{
  children: React.ReactNode;
  title?: React.ReactNode;
  sider: React.ReactNode;
  backend?: string;
  /** Preset assistant info — when provided, badge shows assistant identity instead of backend */
  presetAssistant?: PresetAssistantInfo & { id?: string };
  /** Fallback agent name (used when no presetAssistant, e.g. from conversation.extra.agent_name) */
  agent_name?: string;
  headerExtra?: React.ReactNode;
  workspaceEnabled?: boolean;
  /** Conversation ID for mode switching */
  conversation_id?: string;
  /** Custom tabs slot; when provided, replaces the default ConversationTabs */
  tabsSlot?: React.ReactNode;
  /** Workspace path for opening in external tools */
  workspacePath?: string;
  /** Authoritative temp-workspace flag from `conversation.extra.is_temporary_workspace`. */
  isTemporaryWorkspace?: boolean;
  /**
   * Stable key for persisting the workspace collapse preference. Defaults to
   * `conversation_id` for Synon Biomed conversations.
   */
  workspacePreferenceKey?: string;
  /** Custom rename handler; when provided, replaces the default conversation.update rename flow */
  onRenameTitle?: (new_name: string) => Promise<boolean>;
  /** Optional override for the leading icon shown before the title. */
  headerLeading?: React.ReactNode;
}> = (props) => {
  const { t } = useTranslation();
  const { conversation_id, workspacePath, isTemporaryWorkspace } = props;
  const { workspaceEnabled = true, workspacePreferenceKey } = props;
  const layout = useLayoutContext();
  const workspaceToggleOwner = resolveWorkspaceToggleOwner({
    isElectronDesktop: isElectronDesktop(),
    isMac: isMacEnvironment(),
    isWindows: isWindowsEnvironment(),
  });
  const isDesktop = !layout?.isMobile;
  const isMobile = Boolean(layout?.isMobile);

  // Preview panel state
  const { isOpen: isPreviewOpen, presentationMode } = usePreviewContext();
  const isPreviewBoard = isPreviewOpen && presentationMode === 'board';

  // --- Hook A: workspace collapse ---
  const { rightSiderCollapsed, setRightSiderCollapsed } = useWorkspaceCollapse({
    workspaceEnabled,
    isMobile,
    conversation_id,
    preferenceKey: workspacePreferenceKey ?? conversation_id,
    isTemporaryWorkspace,
  });
  // --- Hook B: container width ---
  const { containerRef, containerWidth } = useContainerWidth();

  const {
    splitRatio: workspaceWidthPxPref,
    setSplitRatio: setWorkspaceWidthPxPref,
    createDragHandle: createWorkspaceDragHandle,
  } = useResizableSplit({
    unit: 'px',
    defaultWidth: DEFAULT_WORKSPACE_PANEL_PX,
    minWidth: MIN_WORKSPACE_PANEL_PX,
    maxWidth: MAX_WORKSPACE_PANEL_PX,
    storageKey: WORKSPACE_WIDTH_STORAGE_KEY,
  });

  // Pre-hook metrics: compute dynamic min/max for the chat-preview split hook
  const { dynamicChatMinRatio, dynamicChatMaxRatio } = calcLayoutMetrics({
    containerWidth,
    workspaceWidthPx: workspaceWidthPxPref,
    chatSplitRatio: 60, // placeholder; only dynamicChatMinRatio/dynamicChatMaxRatio are used here
    workspaceEnabled,
    isDesktop,
    isPreviewOpen,
    rightSiderCollapsed,
    isMobile,
  });

  const {
    splitRatio: chatSplitRatio,
    setSplitRatio: setChatSplitRatio,
    createDragHandle: createPreviewDragHandle,
  } = useResizableSplit({
    defaultWidth: 60,
    minWidth: dynamicChatMinRatio,
    maxWidth: dynamicChatMaxRatio,
    storageKey: 'chat-preview-split-ratio',
  });

  // Full metrics with real chatSplitRatio
  const { chatFlex, workspaceWidthPx, mobileWorkspaceHandleRight } = calcLayoutMetrics({
    containerWidth,
    workspaceWidthPx: workspaceWidthPxPref,
    chatSplitRatio,
    workspaceEnabled,
    isDesktop,
    isPreviewOpen,
    rightSiderCollapsed,
    isMobile,
  });

  // The workspace panel is now file-first. Keep a compact header only when it
  // still owns an actual control (Linux panel toggle or Electron folder opener)
  // so the browser right rail does not retain an empty project/notebook bar.
  const shouldRenderWorkspaceHeader =
    workspaceToggleOwner === 'workspace-panel' ||
    (isElectronDesktop() && Boolean(workspacePath) && !isTemporaryWorkspace);

  // --- Hook E: layout constraints ---
  useLayoutConstraints({
    containerWidth,
    workspaceEnabled,
    isDesktop,
    isPreviewOpen,
    rightSiderCollapsed,
    setRightSiderCollapsed,
    workspaceWidthPx: workspaceWidthPxPref,
    setWorkspaceWidthPx: setWorkspaceWidthPxPref,
    chatSplitRatio,
    setChatSplitRatio,
    dynamicChatMinRatio,
    dynamicChatMaxRatio,
  });

  const [mobileActionsSlot, setMobileActionsSlot] = useState<HTMLElement | null>(null);
  useEffect(() => {
    if (!layout?.isMobile) {
      setMobileActionsSlot(null);
      return;
    }
    const findSlot = () => document.getElementById('app-titlebar-actions-slot');
    setMobileActionsSlot(findSlot());
    const observer = new MutationObserver(() => {
      const next = findSlot();
      setMobileActionsSlot((prev) => (prev === next ? prev : next));
    });
    observer.observe(document.body, { childList: true, subtree: true });
    return () => observer.disconnect();
  }, [layout?.isMobile]);

  const headerBlock = (
    <>
      {layout?.isMobile && mobileActionsSlot && props.headerExtra && createPortal(props.headerExtra, mobileActionsSlot)}
      {props.tabsSlot}
    </>
  );

  return (
    <ArcoLayout
      className='size-full color-black '
      style={{
        // fontFamily: `cursive,"anthropicSans","anthropicSans Fallback",system-ui,Segoe UI,Roboto,Helvetica,Arial,sans-serif`,
      }}
    >
      <div ref={containerRef} className='flex flex-1 relative w-full overflow-hidden'>
        {workspaceToggleOwner === 'conversation-header' && workspaceEnabled && (
          <button
            type='button'
            className='workspace-header__toggle absolute right-8px top-8px z-20'
            aria-label={t(rightSiderCollapsed ? 'common.expandWorkspace' : 'common.collapseWorkspace')}
            title={t(rightSiderCollapsed ? 'common.expandWorkspace' : 'common.collapseWorkspace')}
            data-testid='workspace-toggle'
            onClick={() => dispatchWorkspaceToggleEvent()}
          >
            {rightSiderCollapsed ? <ExpandRight size={16} /> : <ExpandLeft size={16} />}
          </button>
        )}
        {/* Unified layout: single DOM structure prevents children unmount/remount on preview toggle */}
        <div
          className='flex flex-col min-w-0'
          style={{
            flexGrow: 1,
            flexShrink: 1,
            flexBasis: 0,
          }}
        >
          <div className='shrink-0 !bg-1'>{headerBlock}</div>
          <div
            className='flex flex-1 min-h-0 relative'
            data-testid='chat-preview-layout'
            style={{ columnGap: isDesktop && isPreviewOpen ? `${CHAT_PREVIEW_GAP_PX}px` : undefined }}
          >
            {/* Chat area - always mounted, never unmounted on preview toggle */}
            <div
              data-testid='chat-preview-pane'
              className='flex flex-col relative min-w-0'
              style={{
                flexGrow: isPreviewOpen && isDesktop ? 0 : 1,
                flexShrink: 1,
                flexBasis: isPreviewOpen && isDesktop ? `${chatFlex}%` : 0,
                display: isPreviewOpen && isMobile ? 'none' : 'flex',
                minWidth: isPreviewOpen && isDesktop ? `${MIN_CHAT_PANEL_PX}px` : '240px',
              }}
              onClick={() => {
                if (window.innerWidth < 768 && !rightSiderCollapsed) setRightSiderCollapsed(true);
              }}
            >
              <ArcoLayout.Content className='flex flex-col flex-1 bg-1 overflow-hidden'>
                {props.children}
              </ArcoLayout.Content>
            </div>
            {/* Preview panel - conditionally rendered */}
            {isPreviewOpen && (
              <div
                data-testid='chat-preview-panel'
                className={classNames(
                  'preview-panel flex flex-col relative overflow-visible rounded-[15px]',
                  isDesktop ? (isPreviewBoard ? 'mt-[12px] mb-[12px] mr-[12px]' : 'mb-[12px] mr-[12px]') : 'm-[8px]'
                )}
                style={{
                  flexGrow: 1,
                  flexShrink: 1,
                  flexBasis: 0,
                  border: '1px solid var(--bg-3)',
                  minWidth: isDesktop ? `${MIN_PREVIEW_PANEL_PX}px` : 0,
                  maxWidth: isMobile ? 'calc(100% - 16px)' : undefined,
                  width: isMobile ? 'calc(100% - 16px)' : undefined,
                  boxSizing: 'border-box',
                }}
              >
                {isDesktop &&
                  createPreviewDragHandle({
                    className: 'chat-preview-resize-handle absolute top-0 bottom-0 z-30',
                    style: { width: '20px', left: '-20px' },
                    linePlacement: 'end',
                    lineClassName: 'opacity-30 group-hover:opacity-100 group-active:opacity-100',
                    lineStyle: { width: '2px' },
                  })}
                <div className='h-full w-full overflow-hidden rounded-[15px]'>
                  <React.Suspense
                    fallback={
                      <div
                        className='flex size-full items-center justify-center text-13px text-t-secondary'
                        role='status'
                      >
                        {t('common.loading')}
                      </div>
                    }
                  >
                    <PreviewPanel conversationId={conversation_id} />
                  </React.Suspense>
                </div>
              </div>
            )}
          </div>
        </div>
        {workspaceEnabled && !layout?.isMobile && (
          <div
            className={classNames('!bg-1 relative chat-layout-right-sider layout-sider')}
            style={{
              flexGrow: 0,
              flexShrink: 0,
              flexBasis: rightSiderCollapsed ? '0px' : `${Math.round(workspaceWidthPx)}px`,
              width: rightSiderCollapsed ? '0px' : `${Math.round(workspaceWidthPx)}px`,
              minWidth: rightSiderCollapsed ? '0px' : `${MIN_WORKSPACE_PANEL_PX}px`,
              overflow: 'hidden',
              borderLeft: rightSiderCollapsed ? 'none' : '1px solid var(--bg-3)',
            }}
          >
            {isDesktop &&
              !rightSiderCollapsed &&
              createWorkspaceDragHandle({ className: 'absolute left-0 top-0 bottom-0', style: {}, reverse: true })}
            {shouldRenderWorkspaceHeader && (
              <WorkspacePanelHeader
                showToggle={workspaceToggleOwner === 'workspace-panel'}
                collapsed={rightSiderCollapsed}
                onToggle={() => dispatchWorkspaceToggleEvent()}
                togglePlacement='right'
                workspacePath={workspacePath}
                isTemporaryWorkspace={isTemporaryWorkspace}
              />
            )}
            <ArcoLayout.Content
              style={{ height: shouldRenderWorkspaceHeader ? `calc(100% - ${WORKSPACE_HEADER_HEIGHT}px)` : '100%' }}
            >
              {props.sider}
            </ArcoLayout.Content>
          </div>
        )}

        {/* Mobile workspace overlay: backdrop + fixed panel + floating collapse handle */}
        {workspaceEnabled && layout?.isMobile && (
          <MobileWorkspaceOverlay
            rightSiderCollapsed={rightSiderCollapsed}
            setRightSiderCollapsed={setRightSiderCollapsed}
            workspaceWidthPx={workspaceWidthPx}
            mobileWorkspaceHandleRight={mobileWorkspaceHandleRight}
            sider={props.sider}
            workspacePath={workspacePath}
            isTemporaryWorkspace={isTemporaryWorkspace}
          />
        )}

        {/* Desktop expand button when workspace is collapsed */}
        {workspaceToggleOwner === 'workspace-panel' && workspaceEnabled && rightSiderCollapsed && !layout?.isMobile && (
          <DesktopWorkspaceToggle />
        )}
      </div>
    </ArcoLayout>
  );
};

export default ChatLayout;

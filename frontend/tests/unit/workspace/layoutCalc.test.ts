import {
  DEFAULT_WORKSPACE_PANEL_PX,
  MAX_WORKSPACE_PANEL_PX,
  MIN_WORKSPACE_PANEL_PX,
  WORKSPACE_WIDTH_STORAGE_KEY,
  calcLayoutMetrics,
} from '@/renderer/pages/conversation/utils/layoutCalc';
import { describe, expect, it } from 'vitest';

describe('workspace workspace split sizing', () => {
  it('opens Files, Compute, and Notebook as a substantive 480px right pane', () => {
    expect(DEFAULT_WORKSPACE_PANEL_PX).toBe(480);
    expect(MAX_WORKSPACE_PANEL_PX).toBe(1200);
    expect(MIN_WORKSPACE_PANEL_PX).toBe(176);
    expect(WORKSPACE_WIDTH_STORAGE_KEY).toBe('chat-workspace-width-px:claude-science-v1');
    expect(
      calcLayoutMetrics({
        containerWidth: 1650,
        workspaceWidthPx: DEFAULT_WORKSPACE_PANEL_PX,
        chatSplitRatio: 60,
        workspaceEnabled: true,
        isDesktop: true,
        isPreviewOpen: false,
        rightSiderCollapsed: false,
        isMobile: false,
      }).workspaceWidthPx
    ).toBe(480);
  });

  it('lets the files rail contract to the compact card layout without collapsing it', () => {
    expect(
      calcLayoutMetrics({
        containerWidth: 1200,
        workspaceWidthPx: 120,
        chatSplitRatio: 60,
        workspaceEnabled: true,
        isDesktop: true,
        isPreviewOpen: false,
        rightSiderCollapsed: false,
        isMobile: false,
      }).workspaceWidthPx
    ).toBe(MIN_WORKSPACE_PANEL_PX);
  });

  it('still protects the minimum chat width on a narrow desktop', () => {
    expect(
      calcLayoutMetrics({
        containerWidth: 600,
        workspaceWidthPx: DEFAULT_WORKSPACE_PANEL_PX,
        chatSplitRatio: 60,
        workspaceEnabled: true,
        isDesktop: true,
        isPreviewOpen: false,
        rightSiderCollapsed: false,
        isMobile: false,
      }).workspaceWidthPx
    ).toBe(240);
  });
});

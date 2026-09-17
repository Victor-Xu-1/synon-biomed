/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import WorkspaceTabBar from '@/renderer/pages/conversation/Workspace/components/WorkspaceTabBar';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import React from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

vi.mock('@icon-park/react', () => ({
  BranchOne: () => <span />,
  ChartHistogram: () => <span />,
  FolderClose: () => <span />,
}));
vi.mock('@arco-design/web-react', () => ({
  Dropdown: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  Tabs: Object.assign(({ children }: { children: React.ReactNode }) => <div>{children}</div>, {
    TabPane: ({ title }: { title: React.ReactNode }) => <div>{title}</div>,
  }),
}));

afterEach(cleanup);

describe('WorkspaceTabBar', () => {
  it('removes the local filesystem changes tab from read-only Synon Biomed projects', () => {
    render(
      <WorkspaceTabBar
        t={(key) => key}
        readOnly
        activeTab='files'
        onTabChange={vi.fn()}
        changeCount={0}
        branch={null}
      />
    );

    expect(screen.getByText('conversation.workspace.changes.filesTab')).toBeInTheDocument();
    expect(screen.queryByText('conversation.workspace.changes.tab')).toBeNull();
  });

  it('renders only the simplified Files/Compute workspace tabs', async () => {
    const onTabChange = vi.fn();
    render(
      <WorkspaceTabBar
        t={(key) => key}
        readOnly
        showCompute
        activeTab='compute'
        onTabChange={onTabChange}
        changeCount={0}
        branch={null}
      />
    );

    const tabs = screen.getAllByRole('tab');
    expect(tabs.map((tab) => tab.textContent)).toEqual([
      'conversation.workspace.changes.filesTab',
      'conversation.synonRuntime.sessionOptions.compute',
    ]);
    expect(tabs[1]).toHaveAttribute('aria-selected', 'true');
    expect(tabs[1]).toHaveAttribute('data-state', 'selected');
    expect(tabs[1]).toHaveAttribute('tabindex', '0');
    expect(tabs[1]).toHaveClass('synon-workspace-tab--selected');
    expect(tabs[0]).toHaveAttribute('data-state', 'idle');
    expect(tabs[0]).toHaveAttribute('tabindex', '-1');
    expect(tabs[0]).not.toHaveClass('synon-workspace-tab--selected');
    fireEvent.keyDown(tabs[1], { key: 'ArrowRight' });
    expect(onTabChange).toHaveBeenCalledWith('files');
    await Promise.resolve();
    expect(tabs[0]).toHaveFocus();
  });
});

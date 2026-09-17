/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import WorkspaceToolbar from '@/renderer/pages/conversation/Workspace/components/WorkspaceToolbar';
import { cleanup, render, screen } from '@testing-library/react';
import React from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

vi.mock('@/renderer/utils/platform', () => ({ isElectronDesktop: () => false }));
vi.mock('@/renderer/components/media/UploadProgressBar', () => ({
  default: () => <div data-testid='upload-progress' />,
}));
vi.mock('@icon-park/react', () => ({
  Down: () => <span />,
  Plus: () => <span data-testid='upload-action' />,
  Refresh: () => <span data-testid='refresh-action' />,
  Search: () => <span />,
}));
vi.mock('@arco-design/web-react', () => ({
  Dropdown: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  Input: () => <input />,
  Menu: Object.assign(({ children }: { children: React.ReactNode }) => <>{children}</>, {
    Item: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  }),
  Tooltip: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));

afterEach(cleanup);

describe('WorkspaceToolbar', () => {
  it('keeps refresh but removes upload controls for a read-only Synon Biomed workspace', () => {
    render(
      <WorkspaceToolbar
        t={(key) => key}
        readOnly
        isWorkspaceCollapsed={false}
        setIsWorkspaceCollapsed={vi.fn()}
        workspaceDisplayName='Example project'
        showSearch={false}
        searchText=''
        setSearchText={vi.fn()}
        onSearch={vi.fn()}
        searchInputRef={{ current: null }}
        loading={false}
        refreshWorkspace={vi.fn()}
        handleSelectHostFiles={vi.fn()}
        handleUploadDeviceFiles={vi.fn()}
        setShowHostFileSelector={vi.fn()}
      />
    );

    expect(screen.getByTestId('refresh-action')).toBeInTheDocument();
    expect(screen.queryByTestId('upload-action')).toBeNull();
    expect(screen.queryByTestId('upload-progress')).toBeNull();
  });

  it('shows upload controls for a remote Synon Biomed artifact workspace without enabling local changes', () => {
    render(
      <WorkspaceToolbar
        t={(key) => key}
        readOnly
        allowUpload
        isWorkspaceCollapsed={false}
        setIsWorkspaceCollapsed={vi.fn()}
        workspaceDisplayName='Example project'
        showSearch={false}
        searchText=''
        setSearchText={vi.fn()}
        onSearch={vi.fn()}
        searchInputRef={{ current: null }}
        loading={false}
        refreshWorkspace={vi.fn()}
        handleSelectHostFiles={vi.fn()}
        handleUploadDeviceFiles={vi.fn()}
        setShowHostFileSelector={vi.fn()}
      />
    );

    expect(screen.getByTestId('upload-action')).toBeInTheDocument();
    expect(screen.getByTestId('upload-progress')).toBeInTheDocument();
  });
});

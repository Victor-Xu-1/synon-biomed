/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { IDirOrFile } from '@/common/adapter/ipcBridge';
import WorkspaceContextMenu from '@/renderer/pages/conversation/Workspace/components/WorkspaceContextMenu';
import { cleanup, render, screen } from '@testing-library/react';
import React from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

afterEach(cleanup);

describe('WorkspaceContextMenu', () => {
  it('exposes remote artifact lifecycle actions without exposing local filesystem actions', () => {
    const artifact: IDirOrFile = {
      name: 'STAT6_report.md',
      fullPath: 'synonbiomed://proj_stat6/frame-stat6/artifact-report',
      relativePath: 'frame-stat6/artifact-report',
      isDir: false,
      isFile: true,
      readOnly: true,
      artifactId: 'artifact-report',
      contentUrl: '/api/artifacts/artifact-report',
      canRename: true,
      canDelete: true,
    };

    const handleAddToChat = vi.fn();

    render(
      <WorkspaceContextMenu
        visible
        style={{ top: 10, left: 10 }}
        node={artifact}
        t={(key) => key}
        handleAddToChat={handleAddToChat}
        handleOpenNode={vi.fn()}
        handleRevealNode={vi.fn()}
        handlePreviewFile={vi.fn()}
        handleViewArtifactDetails={vi.fn()}
        handleDownloadFile={vi.fn()}
        handleDeleteNode={vi.fn()}
        openRenameModal={vi.fn()}
        closeContextMenu={vi.fn()}
      />
    );

    expect(screen.getByRole('menuitem', { name: 'conversation.workspace.contextMenu.preview' })).toBeInTheDocument();
    expect(screen.getByRole('menuitem', { name: 'conversation.workspace.contextMenu.download' })).toBeInTheDocument();
    expect(screen.getByRole('menuitem', { name: 'conversation.workspace.contextMenu.details' })).toBeInTheDocument();
    const addToChat = screen.getByRole('menuitem', { name: 'conversation.workspace.contextMenu.addToChat' });
    addToChat.click();
    expect(handleAddToChat).toHaveBeenCalledWith(artifact);
    expect(screen.queryByRole('menuitem', { name: 'conversation.workspace.contextMenu.open' })).toBeNull();
    expect(screen.queryByRole('menuitem', { name: 'conversation.workspace.contextMenu.openLocation' })).toBeNull();
    expect(screen.getByRole('menuitem', { name: 'common.delete' })).toBeInTheDocument();
    expect(screen.getByRole('menuitem', { name: 'conversation.workspace.contextMenu.rename' })).toBeInTheDocument();
  });
});

/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { fireEvent, render, screen } from '@testing-library/react';
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { SynonBiomedWorkspaceOption } from '@/renderer/services/synonBiomedCatalog';

const addRecentWorkspaceMock = vi.fn();

vi.mock('@/common', () => ({
  ipcBridge: {
    dialog: {
      showOpen: {
        invoke: vi.fn(),
      },
    },
  },
}));

vi.mock('@/renderer/components/workspace', () => ({
  addRecentWorkspace: (...args: unknown[]) => addRecentWorkspaceMock(...args),
  getRecentWorkspaces: () => [],
}));

vi.mock('@arco-design/web-react', () => ({
  Tooltip: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => {
      const dictionary: Record<string, string> = {
        'guid.workspace.workInProject': 'Work in project',
        'guid.workspace.searchPlaceholder': 'Search project',
        'guid.workspace.noProject': 'No project',
        'guid.workspace.clearWorkspace': 'Clear workspace',
        'guid.workspace.chooseDifferentFolder': 'Choose folder',
      };
      return dictionary[key] || key;
    },
  }),
}));

import GuidWorkspaceFootnote from '@/renderer/pages/guid/components/GuidWorkspaceFootnote';

const stat6WorkspaceUri = 'synonbiomed://project/proj_stat6?name=STAT6&artifacts=23&conversations=2';

const workspaceOptions: SynonBiomedWorkspaceOption[] = [
  {
    id: 'synonbiomed-project:proj_stat6',
    name: 'STAT6',
    source: 'backend-project',
    description: 'STAT6 SBDD PPI workflow',
    artifactCount: 23,
    conversationCount: 2,
    workspaceUri: stat6WorkspaceUri,
    projectId: 'proj_stat6',
  },
];

describe('GuidWorkspaceFootnote', () => {
  beforeEach(() => {
    addRecentWorkspaceMock.mockReset();
  });

  it('selects a Synon Biomed backend project without storing it as a local recent folder', () => {
    const onSelectWorkspace = vi.fn();

    render(
      <GuidWorkspaceFootnote
        workspaceDir=''
        onSelectWorkspace={onSelectWorkspace}
        onClearWorkspace={vi.fn()}
        synonBiomedWorkspaceOptions={workspaceOptions}
      />
    );

    fireEvent.click(screen.getByTestId('workspace-selector-btn'));
    fireEvent.click(screen.getByText('STAT6'));

    expect(onSelectWorkspace).toHaveBeenCalledWith(stat6WorkspaceUri);
    expect(addRecentWorkspaceMock).not.toHaveBeenCalled();
  });

  it('shows the Synon Biomed project name in the active workspace pill', () => {
    render(
      <GuidWorkspaceFootnote
        workspaceDir={stat6WorkspaceUri}
        onSelectWorkspace={vi.fn()}
        onClearWorkspace={vi.fn()}
        synonBiomedWorkspaceOptions={workspaceOptions}
      />
    );

    expect(screen.getByText('STAT6')).toBeInTheDocument();
  });

  it('resolves a project navigation URI without catalog query metadata to the project name', () => {
    render(
      <GuidWorkspaceFootnote
        workspaceDir='synonbiomed://project/proj_stat6'
        onSelectWorkspace={vi.fn()}
        onClearWorkspace={vi.fn()}
        synonBiomedWorkspaceOptions={workspaceOptions}
      />
    );

    expect(screen.getByText('STAT6')).toBeInTheDocument();
  });
});

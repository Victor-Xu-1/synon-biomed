import GuidActionRow from '@/renderer/pages/guid/components/GuidActionRow';
import { cleanup, fireEvent, screen } from '@testing-library/react';
import React from 'react';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

afterEach(cleanup);

describe('GuidActionRow attachment actions', () => {
  it('matches the formal conversation menu structure while keeping unavailable actions disabled', async () => {
    const openFileSelector = vi.fn();
    const onSelectProjectFiles = vi.fn();
    const onSelectSkill = vi.fn();
    const onSelectMcpServer = vi.fn();
    await renderWithI18n(
      <GuidActionRow
        files={[]}
        onFilesUploaded={vi.fn()}
        openFileSelector={openFileSelector}
        onSelectProjectFiles={onSelectProjectFiles}
        loadedSkills={['autodock-vina']}
        loadedMcpStatuses={[{ id: 'bundled:pubmed', name: 'PubMed', status: 'loaded' }]}
        selectedSkillNames={[]}
        selectedMcpServerIds={[]}
        onSelectSkill={onSelectSkill}
        onSelectMcpServer={onSelectMcpServer}
        modelSelectorNode={<span>model</span>}
        loading={false}
        isButtonDisabled
        onSend={vi.fn()}
      />,
      'en-US',
      { wrapper: MemoryRouter }
    );

    const attachTrigger = screen.getByRole('button', { name: 'Add to message' });
    expect(attachTrigger).toHaveClass('composer-icon-control');
    fireEvent.click(attachTrigger);

    const menu = screen.getByRole('menu', { name: 'Add to message' });
    expect(menu).toHaveClass('app-overlay-menu', 'composer-control-menu');
    expect(menu).toHaveStyle({ width: 'min(210px, calc(100vw - 24px))' });
    expect(screen.getByRole('menuitem', { name: 'Loaded Skills · 1' })).toBeInTheDocument();
    expect(screen.getByRole('menuitem', { name: 'Loaded MCP · 1' })).toBeInTheDocument();
    expect(screen.getByRole('menuitem', { name: 'Add file' })).toBeInTheDocument();
    const projectFilesItem = screen.getByRole('menuitem', { name: 'Your files' });
    expect(projectFilesItem).toBeInTheDocument();
    expect(screen.getByRole('menuitem', { name: 'View plan' })).toBeDisabled();
    expect(screen.getByRole('menuitem', { name: 'Review findings' })).toBeDisabled();
    expect(screen.getByRole('menuitem', { name: 'Save as Skill' })).toBeDisabled();
    expect(screen.queryByText('Upload from device')).not.toBeInTheDocument();

    fireEvent.click(projectFilesItem);
    expect(onSelectProjectFiles).toHaveBeenCalledTimes(1);
    expect(openFileSelector).not.toHaveBeenCalled();
  });
});

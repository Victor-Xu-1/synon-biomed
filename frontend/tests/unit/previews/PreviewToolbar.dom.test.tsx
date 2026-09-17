import { fireEvent, render, screen } from '@testing-library/react';
import React from 'react';
import { describe, expect, it, vi } from 'vitest';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

import PreviewToolbar from '@/renderer/pages/conversation/Preview/components/PreviewPanel/PreviewToolbar';

const baseProps = {
  content_type: 'markdown',
  file_name: 'report.md',
  canEdit: true,
  isEditing: false,
  isDirty: false,
  isSaving: false,
  isFullscreen: false,
  showOpenInSystemButton: false,
  historyTarget: null,
  onEdit: vi.fn(),
  onCancelEdit: vi.fn(),
  onSaveEdit: vi.fn(),
  onFullscreenToggle: vi.fn(),
  onOpenInSystem: vi.fn(),
  onDownload: vi.fn(),
  onClose: vi.fn(),
};

describe('PreviewToolbar CS parity controls', () => {
  it('shows every available action directly without a More menu', () => {
    render(<PreviewToolbar {...baseProps} />);

    expect(screen.getByRole('button', { name: 'preview.edit' })).toBeVisible();
    expect(screen.getByRole('button', { name: 'preview.openFullscreen' })).toBeVisible();
    expect(screen.getByRole('button', { name: 'preview.downloadFile' })).toBeVisible();
    expect(screen.getByRole('button', { name: 'preview.closePreview' })).toBeVisible();
    expect(screen.queryByRole('button', { name: 'common.more' })).not.toBeInTheDocument();
  });

  it('keeps exit fullscreen directly available and removes the history control', () => {
    render(
      <PreviewToolbar
        {...baseProps}
        isFullscreen
        historyTarget={{ artifact_id: 'artifact-1', version_id: 'version-1' }}
      />
    );

    expect(screen.getByRole('button', { name: 'preview.exitFullscreen' })).toBeVisible();
    expect(screen.queryByRole('button', { name: 'preview.historyVersions' })).not.toBeInTheDocument();
  });

  it('switches to cancel and save-new-version actions while editing', () => {
    const onCancelEdit = vi.fn();
    const onSaveEdit = vi.fn();
    const { rerender } = render(
      <PreviewToolbar {...baseProps} isEditing isDirty={false} onCancelEdit={onCancelEdit} onSaveEdit={onSaveEdit} />
    );

    const save = screen.getByRole('button', { name: 'preview.saveChanges' });
    expect(save).toBeDisabled();
    expect(screen.queryByRole('button', { name: 'preview.edit' })).not.toBeInTheDocument();

    rerender(<PreviewToolbar {...baseProps} isEditing isDirty onCancelEdit={onCancelEdit} onSaveEdit={onSaveEdit} />);
    fireEvent.click(screen.getByRole('button', { name: 'preview.saveChanges' }));
    expect(onSaveEdit).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole('button', { name: 'common.cancel' }));
    expect(onCancelEdit).toHaveBeenCalledTimes(1);
  });

  it('hides edit for binary previews', () => {
    render(<PreviewToolbar {...baseProps} content_type='image' canEdit={false} />);
    expect(screen.queryByRole('button', { name: 'preview.edit' })).not.toBeInTheDocument();
  });

  it('keeps file identity and tab switching in the same single toolbar row', () => {
    const onSwitchTab = vi.fn();
    render(
      <PreviewToolbar
        {...baseProps}
        tabs={[
          { id: 'report', title: 'report.md' },
          { id: 'figure', title: 'figure.png' },
        ]}
        activeTabId='report'
        onSwitchTab={onSwitchTab}
      />
    );

    expect(screen.getAllByText('report.md')).toHaveLength(1);
    fireEvent.click(screen.getByRole('button', { name: 'preview.currentFile' }));
    fireEvent.click(screen.getByText('figure.png'));
    expect(onSwitchTab).toHaveBeenCalledWith('figure');
  });
});

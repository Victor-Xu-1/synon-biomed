import { cleanup, fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import SynonBiomedNotesModal from '@/renderer/components/synonBiomed/notes/SynonBiomedNotesModal';
import { renderWithI18n } from '../i18nTestUtils';

const notes = vi.hoisted(() => ({
  load: vi.fn(),
  create: vi.fn(),
  update: vi.fn(),
  remove: vi.fn(),
}));

vi.mock('@/renderer/components/Markdown', () => ({
  default: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
}));

vi.mock('@/renderer/services/synonBiomedNotes', () => ({
  loadSynonBiomedNotes: notes.load,
  createSynonBiomedNote: notes.create,
  updateSynonBiomedNote: notes.update,
  deleteSynonBiomedNote: notes.remove,
}));

const target = {
  projectId: 'project-1',
  targetType: 'artifact' as const,
  targetFrameId: 'frame-1',
  targetArtifactId: 'artifact-1',
};

const savedNote = {
  id: 'note-1',
  projectId: 'project-1',
  targetType: 'artifact' as const,
  targetFrameId: 'frame-1',
  targetMessageIndex: null,
  targetArtifactId: 'artifact-1',
  content: 'Review the assay controls.',
  createdAt: '2026-07-15T08:00:00Z',
  updatedAt: '2026-07-15T08:00:00Z',
  targetName: null,
  messagePreview: null,
};

describe('SynonBiomedNotesModal', () => {
  beforeEach(() => {
    notes.load.mockResolvedValue([]);
    notes.create.mockResolvedValue(savedNote);
    notes.update.mockResolvedValue(savedNote);
    notes.remove.mockResolvedValue(undefined);
  });

  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it('creates a note through the localized Chinese workflow', async () => {
    await renderWithI18n(<SynonBiomedNotesModal visible target={target} onClose={vi.fn()} />);

    fireEvent.change(await screen.findByRole('textbox', { name: '笔记内容' }), {
      target: { value: savedNote.content },
    });
    fireEvent.click(screen.getByRole('button', { name: '添加笔记' }));

    await waitFor(() => expect(notes.create).toHaveBeenCalledWith(target, savedNote.content));
    expect(await screen.findByText(savedNote.content)).toBeInTheDocument();
  });

  it('renders the empty notes workflow in English', async () => {
    await renderWithI18n(<SynonBiomedNotesModal visible target={target} onClose={vi.fn()} />, 'en-US');

    expect(await screen.findByText('Notes')).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: 'Note content' })).toHaveAttribute(
      'placeholder',
      'Add a note linked to this content'
    );
    expect(await screen.findByText('No notes')).toBeInTheDocument();
  });
});

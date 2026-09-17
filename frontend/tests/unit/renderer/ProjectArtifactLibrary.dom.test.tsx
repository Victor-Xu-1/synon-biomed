import { cleanup, fireEvent, screen } from '@testing-library/react';
import React from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { SynonBiomedProjectArtifact } from '@/renderer/services/synonBiomedGateway';
import { renderWithI18n } from '../i18nTestUtils';

vi.mock('@/renderer/pages/project/ArtifactBatchActions', () => ({
  ArtifactBatchActions: ({
    artifacts,
    onSelectionChange,
  }: {
    artifacts: SynonBiomedProjectArtifact[];
    onSelectionChange: (ids: string[]) => void;
  }) => (
    <div data-testid='batch-actions'>
      selected:{artifacts.map((artifact) => artifact.artifactId).join(',')}
      <button type='button' onClick={() => onSelectionChange([])}>
        clear mocked selection
      </button>
    </div>
  ),
}));

import ProjectArtifactLibrary from '@/renderer/pages/project/ProjectArtifactLibrary';

const artifact = (artifactId: string, filename: string): SynonBiomedProjectArtifact => ({
  artifactId,
  versionId: null,
  versionNumber: 1,
  projectId: 'project-1',
  rootFrameId: null,
  frameId: null,
  creatingFrameId: null,
  filename,
  contentType: 'text/plain',
  sizeBytes: 10,
  createdAt: '2026-07-14T08:00:00Z',
  updatedAt: '2026-07-14T08:00:00Z',
  checksum: null,
  filePath: null,
  folderId: null,
  priority: null,
  isUserUpload: false,
  agentName: 'OPERON',
  isIntermediate: false,
});

describe('ProjectArtifactLibrary selection', () => {
  afterEach(cleanup);

  it('drives batch actions from individual and select-all controls', async () => {
    const artifacts = [artifact('artifact-a', 'a.txt'), artifact('artifact-b', 'b.txt')];
    await renderWithI18n(
      <ProjectArtifactLibrary
        artifacts={artifacts}
        benches={[]}
        onOpenArtifact={vi.fn()}
        onOpenConversation={vi.fn()}
      />
    );

    expect(screen.queryByTestId('batch-actions')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('checkbox', { name: '选择 a.txt' }));
    expect(screen.getByTestId('batch-actions')).toHaveTextContent('selected:artifact-a');

    fireEvent.click(screen.getByRole('checkbox', { name: '选择全部产物' }));
    expect(screen.getByTestId('batch-actions')).toHaveTextContent('selected:artifact-a,artifact-b');

    fireEvent.click(screen.getByRole('button', { name: 'clear mocked selection' }));
    expect(screen.queryByTestId('batch-actions')).not.toBeInTheDocument();
  });
});

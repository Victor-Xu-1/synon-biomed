import type { SynonBiomedProjectArtifact } from '@/renderer/services/synonBiomedGateway';
import {
  buildProjectArtifactContext,
  formatProjectArtifactSize,
  getReferenceableProjectArtifacts,
} from '@/renderer/pages/guid/utils/guidProjectFilesModel';
import { describe, expect, it } from 'vitest';

const makeArtifact = (overrides: Partial<SynonBiomedProjectArtifact> = {}): SynonBiomedProjectArtifact => ({
  artifactId: 'artifact-1',
  versionId: 'version-1',
  versionNumber: 1,
  projectId: 'project-1',
  rootFrameId: null,
  frameId: null,
  creatingFrameId: null,
  filename: 'report.md',
  contentType: 'text/markdown',
  sizeBytes: 1024,
  createdAt: '2026-08-10T10:00:00.000Z',
  updatedAt: '2026-08-10T10:00:00.000Z',
  checksum: null,
  filePath: null,
  folderId: null,
  priority: null,
  isUserUpload: false,
  agentName: 'Synon Biomed',
  isIntermediate: false,
  ...overrides,
});

describe('guid project files model', () => {
  it('keeps uploaded and generated artifacts from the selected project and orders newest first', () => {
    const artifacts = [
      makeArtifact({ artifactId: 'older', filename: 'zeta.md' }),
      makeArtifact({ artifactId: 'newer', filename: 'alpha.md', updatedAt: '2026-08-11T10:00:00.000Z' }),
      makeArtifact({ artifactId: 'upload', isUserUpload: true }),
      makeArtifact({ artifactId: 'intermediate', isIntermediate: true }),
      makeArtifact({ artifactId: 'unversioned', versionId: null }),
      makeArtifact({ artifactId: 'other-project', projectId: 'project-2' }),
    ];

    expect(getReferenceableProjectArtifacts(artifacts, 'project-1').map((artifact) => artifact.artifactId)).toEqual([
      'newer',
      'upload',
      'older',
    ]);
  });

  it('converts a project artifact into exact typed composer context', () => {
    expect(
      buildProjectArtifactContext(
        makeArtifact({ artifactId: 'artifact-42', versionId: 'version-7', filename: 'synthesis route.md' })
      )
    ).toEqual({
      kind: 'artifact',
      artifactId: 'artifact-42',
      versionId: 'version-7',
      projectId: 'project-1',
      label: 'synthesis route.md',
      contentType: 'text/markdown',
      sizeBytes: 1024,
    });
  });

  it('rejects project artifacts without an exact immutable version', () => {
    expect(buildProjectArtifactContext(makeArtifact({ versionId: null }))).toBeNull();
  });

  it('formats artifact sizes for the compact project picker', () => {
    expect(formatProjectArtifactSize(0)).toBe('0 B');
    expect(formatProjectArtifactSize(1024)).toBe('1.0 KB');
    expect(formatProjectArtifactSize(1024 * 1024)).toBe('1.0 MB');
  });
});

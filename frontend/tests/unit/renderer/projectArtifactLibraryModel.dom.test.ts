import {
  buildProjectArtifactGroups,
  classifyProjectArtifact,
} from '@/renderer/pages/project/projectArtifactLibraryModel';
import type { SynonBiomedProjectArtifact, SynonBiomedProjectBench } from '@/renderer/services/synonBiomedGateway';
import { describe, expect, it } from 'vitest';

const benches: SynonBiomedProjectBench[] = [
  {
    frameId: 'frame-1',
    rootFrameId: 'frame-1',
    parentFrameId: null,
    projectId: 'proj_example',
    name: 'Genome assembly',
    taskSummary: null,
    agentName: 'OPERON',
    status: 'completed',
    statusDescription: null,
    createdAt: '2026-07-08T08:00:00Z',
    updatedAt: '2026-07-08T09:00:00Z',
    completedAt: '2026-07-08T09:00:00Z',
    lastActivityAt: '2026-07-08T09:00:00Z',
    hasImageOutput: true,
  },
];

const artifact = (
  artifactId: string,
  filename: string,
  overrides: Partial<SynonBiomedProjectArtifact> = {}
): SynonBiomedProjectArtifact => ({
  artifactId,
  versionId: `version-${artifactId}`,
  versionNumber: 1,
  projectId: 'proj_example',
  rootFrameId: null,
  frameId: null,
  creatingFrameId: null,
  filename,
  contentType: 'application/octet-stream',
  sizeBytes: 100,
  createdAt: '2026-07-08T10:00:00Z',
  updatedAt: '2026-07-08T10:00:00Z',
  checksum: null,
  filePath: null,
  folderId: null,
  priority: null,
  isUserUpload: false,
  agentName: 'OPERON',
  isIntermediate: false,
  ...overrides,
});

describe('projectArtifactLibraryModel', () => {
  it('groups visible artifacts by uploads, task, and project ownership', () => {
    const groups = buildProjectArtifactGroups({
      benches,
      artifacts: [
        artifact('upload', 'reference.fasta', { isUserUpload: true, agentName: null }),
        artifact('generated', 'structure.png', {
          rootFrameId: 'frame-1',
          frameId: 'frame-1',
          contentType: 'image/png',
        }),
        artifact('project', 'project-notes.md', { contentType: 'text/markdown' }),
        artifact('intermediate', 'scratch.json', { isIntermediate: true, contentType: 'application/json' }),
      ],
      query: '',
    });

    expect(groups.map((group) => [group.kind, group.labelKey ?? group.label, group.artifacts.length])).toEqual([
      ['uploads', 'uploads', 1],
      ['frame', 'Genome assembly', 1],
      ['project', 'projectFiles', 1],
    ]);
    expect(groups.flatMap((group) => group.artifacts).map((item) => item.artifactId)).not.toContain('intermediate');
  });

  it('returns one search-results group and matches filename, type, and expert case-insensitively', () => {
    const artifacts = [
      artifact('image', 'Structure.PNG', { contentType: 'image/png', rootFrameId: 'frame-1' }),
      artifact('table', 'scores.csv', { contentType: 'text/csv', agentName: 'AIDD_EXPERT' }),
    ];

    expect(buildProjectArtifactGroups({ benches, artifacts, query: 'structure' })[0]).toMatchObject({
      kind: 'search',
      labelKey: 'searchResults',
      artifacts: [{ artifactId: 'image' }],
    });
    expect(buildProjectArtifactGroups({ benches, artifacts, query: 'AIDD' })[0].artifacts[0].artifactId).toBe('table');
    expect(buildProjectArtifactGroups({ benches, artifacts, query: 'TEXT/CSV' })[0].artifacts[0].artifactId).toBe(
      'table'
    );
  });

  it('classifies previews without inventing scientific renderers', () => {
    expect(classifyProjectArtifact(artifact('image', 'plot.png', { contentType: 'image/png' }))).toBe('image');
    expect(classifyProjectArtifact(artifact('markdown', 'report.md', { contentType: 'text/markdown' }))).toBe(
      'markdown'
    );
    expect(classifyProjectArtifact(artifact('table', 'results.csv', { contentType: 'text/csv' }))).toBe('table');
    expect(classifyProjectArtifact(artifact('structure', 'complex.pdb', { contentType: 'chemical/x-pdb' }))).toBe(
      'structure'
    );
    expect(classifyProjectArtifact(artifact('pdf', 'paper.pdf', { contentType: 'application/pdf' }))).toBe('pdf');
  });
});

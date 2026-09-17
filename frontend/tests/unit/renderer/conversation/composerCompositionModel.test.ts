import {
  addComposerContextItem,
  buildComposerCapabilityPayload,
  createComposerArtifactContext,
  normalizeComposerContextItems,
  removeComposerContextItem,
} from '@/renderer/components/chat/SendBox/composerCompositionModel';
import { describe, expect, it } from 'vitest';

describe('composerCompositionModel', () => {
  it('builds one deterministic payload for an artifact, a fixed Skill, and a fixed MCP connector', () => {
    const items = normalizeComposerContextItems([
      {
        kind: 'artifact',
        artifactId: 'artifact-1',
        versionId: 'version-2',
        label: 'cohort.csv',
        contentType: 'text/csv',
        sizeBytes: 42,
      },
      { kind: 'skill', name: 'single-cell-analysis', label: 'single-cell-analysis' },
      { kind: 'mcp', serverId: 'pubmed', label: 'PubMed' },
    ]);

    expect(buildComposerCapabilityPayload(items)).toEqual({
      artifactRefs: [
        {
          artifact_id: 'artifact-1',
          version_id: 'version-2',
          relation: 'attached',
          filename: 'cohort.csv',
          content_type: 'text/csv',
          size_bytes: 42,
        },
      ],
      injectSkills: ['single-cell-analysis'],
      injectMcpServerIds: ['pubmed'],
    });
  });

  it('deduplicates semantic identities and rejects incomplete persisted entries', () => {
    const items = normalizeComposerContextItems([
      { kind: 'skill', name: 'Review', label: 'Review' },
      { kind: 'skill', name: 'review', label: 'duplicate' },
      { kind: 'mcp', serverId: 'pubmed', label: 'PubMed' },
      { kind: 'artifact', artifactId: 'artifact-1', versionId: '', label: 'missing version' },
      { kind: 'unknown', label: 'ignored' },
    ]);
    expect(items).toHaveLength(2);
    expect(buildComposerCapabilityPayload(items).injectSkills).toEqual(['Review']);
  });

  it('adds and removes context without mutating the previous draft', () => {
    const original = [{ kind: 'skill', name: 'review', label: 'review' }] as const;
    const added = addComposerContextItem(original, { kind: 'mcp', serverId: 'pubmed', label: 'PubMed' });
    expect(original).toHaveLength(1);
    expect(added).toHaveLength(2);
    expect(removeComposerContextItem(added, 'skill:review')).toEqual([
      { kind: 'mcp', serverId: 'pubmed', label: 'PubMed' },
    ]);
  });

  it('creates only exact versioned artifact context for generated-file reuse', () => {
    expect(
      createComposerArtifactContext({
        artifactId: 'artifact-1',
        versionId: 'version-2',
        projectId: 'project-7',
        label: 'analysis.csv',
      })
    ).toEqual({
      kind: 'artifact',
      artifactId: 'artifact-1',
      versionId: 'version-2',
      projectId: 'project-7',
      label: 'analysis.csv',
    });
    expect(
      createComposerArtifactContext({
        artifactId: 'artifact-1',
        label: 'analysis.csv',
      })
    ).toBeNull();
  });
});

import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  copySynonBiomedArtifact,
  createSynonBiomedTextArtifactVersion,
  getSynonBiomedArtifactVersionContentUrl,
  loadSynonBiomedArtifactLineage,
  loadSynonBiomedArtifactVersions,
  moveSynonBiomedArtifact,
} from '@/renderer/services/synonBiomedArtifacts';

describe('Synon Biomed artifact lifecycle service', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('maps real version and lineage response shapes', async () => {
    const fetchMock = vi.fn(async (input: string | URL | Request) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url;
      if (url === '/api/artifacts/artifact-1/versions') {
        return Response.json([
          {
            version_id: 'version-2',
            version_number: 2,
            artifact_id: 'artifact-1',
            frame_id: 'frame-1',
            agent_name: 'OPERON',
            language: 'markdown',
            content_type: 'text/markdown',
            size_bytes: 84,
            created_at: '2026-07-11T02:00:00.000Z',
            file_path: '/state/artifact-1/v2-report.md',
            parent_version_id: 'version-1',
          },
        ]);
      }
      if (url === '/api/artifacts/artifact-1/lineage?slim=1') {
        return Response.json({
          artifact_id: 'artifact-1',
          version_id: 'version-2',
          version_number: 2,
          filename: 'report.md',
          code: 'python analysis.py',
          code_description: '运行统计分析',
          messages: [{ role: 'assistant', text: '完成分析' }],
          environment_snapshot: { python: '3.12' },
          language: 'python',
          interactions: [{ tool: 'python' }],
          has_cell_sources: true,
          has_messages: true,
          has_environment: true,
          pending: false,
          dependency_mappings: [{ artifact_id: 'input-1' }],
        });
      }
      return Response.json({ detail: 'not found' }, { status: 404 });
    });
    vi.stubGlobal('fetch', fetchMock);

    await expect(loadSynonBiomedArtifactVersions('artifact-1')).resolves.toEqual([
      expect.objectContaining({
        versionId: 'version-2',
        versionNumber: 2,
        artifactId: 'artifact-1',
        parentVersionId: 'version-1',
      }),
    ]);
    await expect(loadSynonBiomedArtifactLineage('artifact-1', { slim: true })).resolves.toMatchObject({
      artifactId: 'artifact-1',
      versionId: 'version-2',
      codeDescription: '运行统计分析',
      hasMessages: true,
      hasEnvironment: true,
      pending: false,
    });
    expect(getSynonBiomedArtifactVersionContentUrl('version-2')).toBe('/api/artifacts/versions/version-2');
  });

  it('uses the native copy, move and text-version contracts', async () => {
    const observed: Array<{ url: string; method: string; body?: unknown }> = [];
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url;
      observed.push({
        url,
        method: init?.method ?? 'GET',
        body: typeof init?.body === 'string' ? JSON.parse(init.body) : undefined,
      });
      return Response.json({ id: 'artifact-copy', version_id: 'version-copy', filename: 'report-copy.md' });
    });
    vi.stubGlobal('fetch', fetchMock);

    await copySynonBiomedArtifact({
      artifactId: 'artifact-1',
      newFilename: 'report-copy.md',
      targetFolderId: 'folder-2',
    });
    await moveSynonBiomedArtifact({ artifactId: 'artifact-1', folderId: 'folder-2', sortOrder: 3 });
    await createSynonBiomedTextArtifactVersion({
      artifactId: 'artifact-1',
      content: '# updated',
      contentType: 'text/markdown',
      parentVersionId: 'version-2',
    });

    expect(observed).toEqual([
      {
        url: '/api/artifacts/artifact-1/copy',
        method: 'POST',
        body: { new_filename: 'report-copy.md', target_folder_id: 'folder-2' },
      },
      {
        url: '/api/artifacts/artifact-1/folder',
        method: 'PATCH',
        body: { folder_id: 'folder-2', sort_order: 3 },
      },
      {
        url: '/api/artifacts/artifact-1/versions',
        method: 'POST',
        body: { content: '# updated', content_type: 'text/markdown', parent_version_id: 'version-2' },
      },
    ]);
  });
});

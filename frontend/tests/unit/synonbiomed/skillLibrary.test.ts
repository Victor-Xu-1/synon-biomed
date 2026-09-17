import { describe, expect, it, vi } from 'vitest';
import type { SynonBiomedSkillLibraryError } from '@/renderer/services/skills/synonBiomedSkillLibrary';
import {
  importSynonBiomedRepositorySkills,
  importSynonBiomedSkillFile,
  loadSynonBiomedSkillDrafts,
  loadSynonBiomedSkillFileContent,
  loadSynonBiomedSkillFiles,
  loadSynonBiomedSkillSources,
  previewSynonBiomedSkillRepository,
  saveSynonBiomedSkillDraftFile,
} from '@/renderer/services/skills/synonBiomedSkillLibrary';

describe('Synon Biomed skill library service', () => {
  it('normalizes drafts, imported sources, repository previews and file content', async () => {
    const fetchImpl = vi.fn<typeof fetch>(async (input) => {
      const url = String(input);
      if (url.endsWith('/api/skills/drafts')) {
        return json({
          drafts: [
            'local-molecular-docking',
            {
              name: 'my-skill',
              display_name: 'My Skill',
              description: 'Personal workflow',
              files: ['SKILL.md', 'references/notes.md'],
              updated_at: '2026-07-14T00:00:00Z',
            },
          ],
        });
      }
      if (url.endsWith('/api/marketplace/sources')) {
        return json({
          sources: [
            {
              slug: 'research-skills',
              repo: 'https://github.com/example/research-skills',
              pinned_sha: 'abc123',
              license: 'Apache-2.0',
              skill_names: ['literature-review'],
              imported_at: '2026-07-13T00:00:00Z',
            },
          ],
        });
      }
      if (url.endsWith('/api/marketplace/preview')) {
        return json({
          repo: 'https://github.com/example/research-skills',
          slug: 'research-skills',
          sha: 'abc123',
          license: 'Apache-2.0',
          skills: [{ name: 'literature-review', display_name: 'Literature Review', description: 'Review papers' }],
        });
      }
      if (url.endsWith('/api/skills/catalog/my-skill/files')) return json({ files: ['SKILL.md'] });
      if (url.includes('/api/skills/catalog/my-skill/content')) return json({ content: '# My Skill' });
      throw new Error(`unexpected URL: ${url}`);
    });
    const options = { baseUrl: 'http://fusion.test', fetchImpl };

    await expect(loadSynonBiomedSkillDrafts(options)).resolves.toEqual([
      {
        name: 'local-molecular-docking',
        displayName: 'Local Molecular Docking',
        description: '',
        files: [],
        updatedAt: null,
      },
      {
        name: 'my-skill',
        displayName: 'My Skill',
        description: 'Personal workflow',
        files: ['SKILL.md', 'references/notes.md'],
        updatedAt: '2026-07-14T00:00:00Z',
      },
    ]);
    await expect(loadSynonBiomedSkillSources(options)).resolves.toEqual([
      {
        slug: 'research-skills',
        repo: 'https://github.com/example/research-skills',
        sha: 'abc123',
        license: 'Apache-2.0',
        skills: ['literature-review'],
        importedAt: '2026-07-13T00:00:00Z',
        removable: true,
      },
    ]);
    await expect(
      previewSynonBiomedSkillRepository('https://github.com/example/research-skills', options)
    ).resolves.toMatchObject({
      slug: 'research-skills',
      sha: 'abc123',
      skills: [{ name: 'literature-review', displayName: 'Literature Review', selected: true }],
    });
    await expect(loadSynonBiomedSkillFiles('my-skill', options)).resolves.toEqual(['SKILL.md']);
    await expect(loadSynonBiomedSkillFileContent('my-skill', 'SKILL.md', options)).resolves.toBe('# My Skill');
  });

  it('writes exact repository, multipart import and draft edit contracts', async () => {
    const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
      const url = String(input);
      if (url.endsWith('/api/marketplace/import')) return json({ imported: ['literature-review'], skipped: [] });
      if (url.endsWith('/api/skills/import')) {
        expect(init?.body).toBeInstanceOf(FormData);
        const body = init?.body as FormData;
        expect(body.get('name')).toBe('uploaded-skill');
        expect((body.get('file') as File).name).toBe('SKILL.md');
        return json({ name: 'uploaded-skill' }, 201);
      }
      if (url.endsWith('/api/skills/my-skill/edit')) return json({ ok: true });
      throw new Error(`unexpected URL: ${url}`);
    });
    const options = { baseUrl: 'http://fusion.test', fetchImpl };
    const preview = {
      repo: 'https://github.com/example/research-skills',
      slug: 'research-skills',
      sha: 'abc123',
      license: 'Apache-2.0',
      skills: [],
    };

    await importSynonBiomedRepositorySkills(preview, ['literature-review'], options);
    expect(fetchImpl).toHaveBeenCalledWith(
      'http://fusion.test/api/marketplace/import',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({
          repo: preview.repo,
          sha: preview.sha,
          skills: ['literature-review'],
          license: preview.license,
        }),
      })
    );

    await importSynonBiomedSkillFile(
      new File(['# Uploaded'], 'SKILL.md', { type: 'text/markdown' }),
      'uploaded-skill',
      options
    );
    await saveSynonBiomedSkillDraftFile('my-skill', 'SKILL.md', '# Original', '# Updated', options);
    expect(fetchImpl).toHaveBeenLastCalledWith(
      'http://fusion.test/api/skills/my-skill/edit',
      expect.objectContaining({
        body: JSON.stringify({ path: 'SKILL.md', old_string: '# Original', new_string: '# Updated' }),
      })
    );
  });

  it('returns typed, non-leaking HTTP failures', async () => {
    const fetchImpl = vi.fn<typeof fetch>().mockResolvedValue(json({ detail: 'Repository is not allowed' }, 400));

    await expect(
      previewSynonBiomedSkillRepository('file:///etc/passwd', { baseUrl: 'http://fusion.test', fetchImpl })
    ).rejects.toEqual(
      expect.objectContaining<SynonBiomedSkillLibraryError>({ status: 400, message: 'Repository is not allowed' })
    );
  });
});

function json(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), { status, headers: { 'content-type': 'application/json' } });
}

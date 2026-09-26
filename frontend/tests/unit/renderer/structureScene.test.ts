import { describe, expect, it, vi } from 'vitest';
import {
  loadStructureSceneSources,
  parseStructureSceneManifest,
  STRUCTURE_SCENE_MANIFEST_SCHEMA,
} from '@/renderer/pages/conversation/Preview/components/viewers/structureScene';

describe('structure scene delivery contract', () => {
  it('accepts exact mother and derived version references', () => {
    const manifest = parseStructureSceneManifest({
      schema: STRUCTURE_SCENE_MANIFEST_SCHEMA,
      scene_id: 'scene-1',
      mother_structure: { name: 'mother.pdb', version_id: 'mother-v1' },
      derived_structures: [{ name: 'pocket.pdb', version_id: 'pocket-v1' }],
    });

    expect(manifest).toEqual({
      schema: STRUCTURE_SCENE_MANIFEST_SCHEMA,
      scene_id: 'scene-1',
      mother_structure: { name: 'mother.pdb', version_id: 'mother-v1' },
      derived_structures: [{ name: 'pocket.pdb', version_id: 'pocket-v1' }],
    });
  });

  it('rejects duplicate structure versions and missing references', () => {
    expect(
      parseStructureSceneManifest({
        schema: STRUCTURE_SCENE_MANIFEST_SCHEMA,
        scene_id: 'scene-1',
        mother_structure: { name: 'mother.pdb', version_id: 'same-v1' },
        derived_structures: [{ name: 'pocket.pdb', version_id: 'same-v1' }],
      })
    ).toBeNull();
  });

  it('resolves the scene layers through the existing companion URL map', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          schema: STRUCTURE_SCENE_MANIFEST_SCHEMA,
          scene_id: 'scene-1',
          mother_structure: { name: 'mother.pdb', version_id: 'mother-v1' },
          derived_structures: [{ name: 'pocket.pdb', version_id: 'pocket-v1' }],
        }),
        { status: 200, headers: { 'content-type': 'application/json' } }
      )
    );
    vi.stubGlobal('fetch', fetchMock);

    await expect(
      loadStructureSceneSources(
        {
          'scene.json': '/artifacts/scene.json',
          'mother.pdb': '/artifacts/mother.pdb',
          'pocket.pdb': '/artifacts/pocket.pdb',
        },
        new AbortController().signal
      )
    ).resolves.toEqual({
      sceneId: 'scene-1',
      sources: [
        { name: 'mother.pdb', versionId: 'mother-v1', url: '/artifacts/mother.pdb' },
        { name: 'pocket.pdb', versionId: 'pocket-v1', url: '/artifacts/pocket.pdb' },
      ],
    });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});

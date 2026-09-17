import { describe, expect, it } from 'vitest';
import type { ISynonBiomedScientificFile } from '@/common/adapter/ipcBridge';
import type { ArtifactReferenceWire } from '@/common/adapter/messageStreamProtocol';
import { matchScientificFilesToArtifactReferences } from '@/renderer/pages/conversation/Messages/components/artifactReferenceModel';

const file = (artifactId: string, versionId: string | null, filename: string): ISynonBiomedScientificFile => ({
  artifact_id: artifactId,
  version_id: versionId,
  version_number: 1,
  project_id: 'project-1',
  root_frame_id: 'frame-1',
  frame_id: 'frame-1',
  creating_frame_id: 'frame-1',
  filename,
  content_type: 'image/png',
  size_bytes: 128,
  preview_kind: 'image',
  content_url: `/api/artifacts/${artifactId}/content`,
  created_at: 1,
  updated_at: 1,
  agent_name: 'OPERON',
  is_user_upload: false,
  is_intermediate: false,
});

describe('artifactReferenceModel', () => {
  it('matches only the exact canonical artifact and version in reference order', () => {
    const refs: ArtifactReferenceWire[] = [
      { artifact_id: 'artifact-b', version_id: 'version-2', relation: 'produced', availability: 'available' },
      { artifact_id: 'artifact-a', version_id: 'version-1', relation: 'produced', availability: 'available' },
    ];
    const files = [
      file('artifact-a', 'version-1', 'same.png'),
      file('artifact-a', 'version-current', 'same.png'),
      file('artifact-b', 'version-2', 'same.png'),
      file('artifact-c', null, 'same.png'),
    ];
    expect(matchScientificFilesToArtifactReferences(refs, files, new Set(['produced']))).toEqual([files[2], files[0]]);
  });

  it('keeps typed unavailable versions visible without substituting another version', () => {
    const refs: ArtifactReferenceWire[] = [
      { artifact_id: 'artifact-a', version_id: 'version-1', relation: 'produced', availability: 'deleted' },
      { artifact_id: 'artifact-b', version_id: 'version-1', relation: 'cited', availability: 'available' },
      { artifact_id: 'artifact-c', version_id: 'version-1', relation: 'produced', availability: 'missing' },
    ];
    const matched = matchScientificFilesToArtifactReferences(
      refs,
      [
        file('artifact-a', 'version-1', 'a.png'),
        file('artifact-b', 'version-1', 'b.png'),
        file('artifact-c', 'version-1', 'c.png'),
      ],
      new Set(['produced'])
    );
    expect(matched).toEqual([
      expect.objectContaining({
        artifact_id: 'artifact-a',
        version_id: 'version-1',
        availability: 'deleted',
        content_url: '',
      }),
      expect.objectContaining({
        artifact_id: 'artifact-c',
        version_id: 'version-1',
        availability: 'missing',
        content_url: '',
      }),
    ]);
    expect(matched).not.toContainEqual(expect.objectContaining({ artifact_id: 'artifact-b' }));
  });
});

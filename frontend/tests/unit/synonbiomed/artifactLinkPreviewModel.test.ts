import { describe, expect, it } from 'vitest';

import {
  getSynonBiomedArtifactImageFilename,
  getSynonBiomedArtifactReferenceId,
  getSynonBiomedRelativeArtifactFilename,
  resolveSynonBiomedArtifactRootFrameId,
  resolveSynonBiomedArtifactFile,
} from '@/renderer/pages/conversation/Messages/useSynonBiomedArtifactLinkPreview';
import type { ISynonBiomedScientificFile } from '@/common/adapter/ipcBridge';
import type { ArtifactReferenceWire } from '@/common/adapter/messageStreamProtocol';
import type { ConversationArtifactIndex } from '@/renderer/pages/conversation/Messages/artifacts';
import { createSynonBiomedCompanionArtifactUrls } from '@/renderer/services/synonBiomedArtifactReferences';

describe('Synon Biomed artifact link preview model', () => {
  it('extracts raw and URL-encoded artifact references', () => {
    const artifactId = 'f0af1025-7698-432a-b4ad-841099d2cd4b';

    expect(getSynonBiomedArtifactReferenceId(`{{artifact:${artifactId}}}`)).toBe(artifactId);
    expect(getSynonBiomedArtifactReferenceId(`%7B%7Bartifact%3A${artifactId}%7D%7D`)).toBe(artifactId);
    expect(getSynonBiomedArtifactReferenceId('{{artifact:}}')).toBeNull();
    expect(getSynonBiomedArtifactReferenceId('https://example.test/file.csv')).toBeNull();
  });

  it('keeps filename fallback resolution constrained to safe relative paths', () => {
    expect(getSynonBiomedRelativeArtifactFilename('reports/docking_results.csv?download=1')).toBe(
      'docking_results.csv'
    );
    expect(getSynonBiomedRelativeArtifactFilename('../docking_results.csv')).toBeNull();
    expect(getSynonBiomedRelativeArtifactFilename('/absolute/docking_results.csv')).toBeNull();
    expect(getSynonBiomedArtifactImageFilename('./molecule_grid.png')).toBe('molecule_grid.png');
  });

  it('indexes one unambiguous companion URL and rejects ambiguous or unavailable filenames', () => {
    expect(
      createSynonBiomedCompanionArtifactUrls([
        { name: 'report.md', contentUrl: '/report' },
        {
          name: 'plots',
          children: [
            { name: 'curve.png', contentUrl: '/curve' },
            { name: 'duplicate.csv', contentUrl: '/first' },
            { name: 'duplicate.csv', contentUrl: '/second' },
            { name: 'deleted.csv', contentUrl: '/deleted', availability: 'deleted' },
          ],
        },
      ])
    ).toEqual({ 'report.md': '/report', 'curve.png': '/curve' });
  });

  it('uses the exact message artifact version before a conversation-wide filename fallback', () => {
    const oldFile = {
      artifact_id: 'artifact-report',
      version_id: 'version-old',
      filename: 'final_report.md',
    } as ISynonBiomedScientificFile;
    const newFile = {
      artifact_id: 'artifact-report',
      version_id: 'version-new',
      filename: 'final_report.md',
    } as ISynonBiomedScientificFile;
    const index: ConversationArtifactIndex = {
      byFilename: new Map([['final_report.md', newFile]]),
      byArtifactId: new Map([['artifact-report', newFile]]),
      byVersionId: new Map([
        ['version-old', oldFile],
        ['version-new', newFile],
      ]),
    };
    const references = [
      {
        artifact_id: 'artifact-report',
        version_id: 'version-old',
        relation: 'produced',
        availability: 'available',
      } as ArtifactReferenceWire,
    ];

    expect(resolveSynonBiomedArtifactFile(index, null, 'final_report.md', references)).toBe(oldFile);
    expect(resolveSynonBiomedArtifactFile(index, 'version-old', null, references)).toBe(oldFile);
  });

  it('keeps the artifact root frame for unified preview operations', () => {
    expect(
      resolveSynonBiomedArtifactRootFrameId({ root_frame_id: 'root-1', frame_id: 'frame-1' }, 'conversation-1')
    ).toBe('root-1');
    expect(resolveSynonBiomedArtifactRootFrameId({ root_frame_id: null, frame_id: 'frame-1' }, 'conversation-1')).toBe(
      'frame-1'
    );
    expect(resolveSynonBiomedArtifactRootFrameId({ root_frame_id: null, frame_id: null }, 'conversation-1')).toBe(
      'conversation-1'
    );
  });
});

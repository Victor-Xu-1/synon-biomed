import type { ArtifactReferenceWire } from '@/common/adapter/messageStreamProtocol';
import { presentArtifactReferenceContent } from '@/renderer/pages/conversation/Messages/components/artifactReferencePresentation';

const index = {
  byFilename: new Map(),
  byArtifactId: new Map(),
  byVersionId: new Map(),
};

const references: ArtifactReferenceWire[] = [
  { artifact_id: 'artifact-1', version_id: 'version-1', relation: 'produced' },
];

describe('presentArtifactReferenceContent', () => {
  it('removes bare internal artifact tokens from settled user-visible prose', () => {
    expect(presentArtifactReferenceContent('结果已保存。 {{artifact:version-1}}', references, index, true)).toBe(
      '结果已保存。'
    );
    expect(presentArtifactReferenceContent('结果已保存。 {{artifact:unknown}}', references, index, true)).toBe(
      '结果已保存。'
    );
  });

  it('preserves artifact tokens used as Markdown link destinations', () => {
    expect(presentArtifactReferenceContent('[下载报告]({{artifact:version-1}})', references, index, true)).toBe(
      '[下载报告]({{artifact:version-1}})'
    );
  });

  it('canonicalizes a versioned hash preview URL to the same artifact reference', () => {
    expect(
      presentArtifactReferenceContent(
        '[可访问链接](/#/artifacts/artifact-1?version=version-1)',
        references,
        index,
        true
      )
    ).toBe('[可访问链接]({{artifact:version-1}})');
  });

  it('repairs a historical artifact path only when the conversation identifies one version', () => {
    const legacyReferences: ArtifactReferenceWire[] = [
      {
        artifact_id: '6f178870-3417-5c3f-81f5-4b50744f1c54',
        version_id: '4cfcf43e-43dd-432c-94c3-3785e40d446b',
        relation: 'produced',
        filename: '1CRN.cif',
      },
    ];
    expect(
      presentArtifactReferenceContent(
        '可通过以下链接访问：`/artifacts/6f178870-3417-5c3f-81f5-4b50744f1c54`',
        legacyReferences,
        index,
        true
      )
    ).toBe('可通过以下链接访问：[1CRN.cif]({{artifact:4cfcf43e-43dd-432c-94c3-3785e40d446b}})');
  });

  it('leaves an artifact path unchanged when its version cannot be determined uniquely', () => {
    const ambiguousReferences: ArtifactReferenceWire[] = [
      { artifact_id: 'artifact-ambiguous', version_id: 'version-a', relation: 'produced' },
      { artifact_id: 'artifact-ambiguous', version_id: 'version-b', relation: 'produced' },
    ];
    expect(presentArtifactReferenceContent('`/artifacts/artifact-ambiguous`', ambiguousReferences, index, true)).toBe(
      '`/artifacts/artifact-ambiguous`'
    );
  });
});

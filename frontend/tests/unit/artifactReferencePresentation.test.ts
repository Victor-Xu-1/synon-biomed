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
});

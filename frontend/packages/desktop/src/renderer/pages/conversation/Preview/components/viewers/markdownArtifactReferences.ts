import { getSynonBiomedArtifactVersionContentUrl } from '@/renderer/services/synonBiomedArtifacts';

const SYNON_BIOMED_ARTIFACT_VERSION_REFERENCE =
  /\{\{artifact:([0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12})\}\}/gi;

export const resolveMarkdownArtifactVersionReferences = (markdown: string): string =>
  markdown.replace(SYNON_BIOMED_ARTIFACT_VERSION_REFERENCE, (_reference, versionId: string) =>
    getSynonBiomedArtifactVersionContentUrl(versionId)
  );

export const isSameOriginArtifactVersionUrl = (value?: string): boolean =>
  Boolean(value && /^\/api\/artifacts\/(?:versions\/[0-9a-f-]+|[0-9a-f-]+\/versions\/[0-9a-f-]+)$/i.test(value));

type MarkdownUrlNode = {
  type?: unknown;
  url?: unknown;
  children?: unknown;
};

function rewriteMarkdownArtifactUrlNodes(node: unknown, resolve: (reference: string) => string | null): void {
  if (!node || typeof node !== 'object') return;
  const candidate = node as MarkdownUrlNode;
  if ((candidate.type === 'link' || candidate.type === 'image') && typeof candidate.url === 'string') {
    candidate.url = resolve(candidate.url) ?? candidate.url;
  }
  if (Array.isArray(candidate.children)) {
    for (const child of candidate.children) rewriteMarkdownArtifactUrlNodes(child, resolve);
  }
}

export const createMarkdownArtifactReferenceResolverPlugin =
  (resolve: (reference: string) => string | null) =>
  () =>
  (tree: unknown): void => {
    rewriteMarkdownArtifactUrlNodes(tree, resolve);
  };

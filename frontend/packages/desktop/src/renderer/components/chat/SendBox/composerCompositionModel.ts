import type { ArtifactReferenceWire } from '@/common/adapter/messageStreamProtocol';

export const MAX_COMPOSER_CONTEXT_ITEMS = 64;

type ComposerContextBase = {
  label: string;
};

export type ComposerArtifactContext = ComposerContextBase & {
  kind: 'artifact';
  artifactId: string;
  versionId: string;
  projectId?: string;
  contentType?: string;
  sizeBytes?: number;
};

export type ComposerSkillContext = ComposerContextBase & {
  kind: 'skill';
  name: string;
};

export type ComposerMcpContext = ComposerContextBase & {
  kind: 'mcp';
  serverId: string;
};

export type ComposerContextItem = ComposerArtifactContext | ComposerSkillContext | ComposerMcpContext;

export type ComposerCapabilityPayload = {
  artifactRefs: ArtifactReferenceWire[];
  injectSkills: string[];
  injectMcpServerIds: string[];
};

export type ComposerArtifactContextInput = {
  artifactId: string;
  versionId?: string;
  projectId?: string | null;
  label: string;
  contentType?: string;
  sizeBytes?: number;
};

const MAX_IDENTIFIER_LENGTH = 512;
const MAX_LABEL_LENGTH = 256;

function normalizedString(value: unknown, limit: number): string {
  return typeof value === 'string' ? value.trim().slice(0, limit) : '';
}

export function composerContextItemKey(item: ComposerContextItem): string {
  switch (item.kind) {
    case 'artifact':
      return `artifact:${item.artifactId}:${item.versionId}`;
    case 'skill':
      return `skill:${item.name.toLocaleLowerCase()}`;
    case 'mcp':
      return `mcp:${item.serverId.toLocaleLowerCase()}`;
  }
}

function normalizeComposerContextItem(value: unknown): ComposerContextItem | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const candidate = value as Record<string, unknown>;
  const kind = normalizedString(candidate.kind, 32);
  const label = normalizedString(candidate.label, MAX_LABEL_LENGTH);
  if (!label) return null;

  if (kind === 'artifact') {
    const artifactId = normalizedString(candidate.artifactId, MAX_IDENTIFIER_LENGTH);
    const versionId = normalizedString(candidate.versionId, MAX_IDENTIFIER_LENGTH);
    if (!artifactId || !versionId) return null;
    const projectId = normalizedString(candidate.projectId, MAX_IDENTIFIER_LENGTH);
    const contentType = normalizedString(candidate.contentType, MAX_LABEL_LENGTH);
    const sizeBytes =
      typeof candidate.sizeBytes === 'number' && Number.isSafeInteger(candidate.sizeBytes) && candidate.sizeBytes >= 0
        ? candidate.sizeBytes
        : undefined;
    return {
      kind,
      artifactId,
      versionId,
      label,
      ...(projectId ? { projectId } : {}),
      ...(contentType ? { contentType } : {}),
      ...(sizeBytes === undefined ? {} : { sizeBytes }),
    };
  }

  if (kind === 'skill') {
    const name = normalizedString(candidate.name, MAX_IDENTIFIER_LENGTH);
    return name ? { kind, name, label } : null;
  }

  if (kind === 'mcp') {
    const serverId = normalizedString(candidate.serverId, MAX_IDENTIFIER_LENGTH);
    return serverId ? { kind, serverId, label } : null;
  }

  return null;
}

export function normalizeComposerContextItems(value: unknown): ComposerContextItem[] {
  if (!Array.isArray(value)) return [];
  const result: ComposerContextItem[] = [];
  const seen = new Set<string>();
  for (const candidate of value) {
    const item = normalizeComposerContextItem(candidate);
    if (!item) continue;
    const key = composerContextItemKey(item);
    if (seen.has(key)) continue;
    seen.add(key);
    result.push(item);
    if (result.length >= MAX_COMPOSER_CONTEXT_ITEMS) break;
  }
  return result;
}

export function addComposerContextItem(
  current: readonly ComposerContextItem[],
  next: ComposerContextItem
): ComposerContextItem[] {
  return normalizeComposerContextItems([...current, next]);
}

export function createComposerArtifactContext(input: ComposerArtifactContextInput): ComposerArtifactContext | null {
  const normalized = normalizeComposerContextItems([
    {
      kind: 'artifact',
      artifactId: input.artifactId,
      versionId: input.versionId,
      ...(input.projectId ? { projectId: input.projectId } : {}),
      label: input.label,
      ...(input.contentType ? { contentType: input.contentType } : {}),
      ...(input.sizeBytes === undefined ? {} : { sizeBytes: input.sizeBytes }),
    },
  ]);
  const item = normalized[0];
  return item?.kind === 'artifact' ? item : null;
}

export function removeComposerContextItem(current: readonly ComposerContextItem[], key: string): ComposerContextItem[] {
  return current.filter((item) => composerContextItemKey(item) !== key);
}

export function buildComposerCapabilityPayload(items: readonly ComposerContextItem[]): ComposerCapabilityPayload {
  const normalized = normalizeComposerContextItems(items);
  return {
    artifactRefs: normalized
      .filter((item): item is ComposerArtifactContext => item.kind === 'artifact')
      .map((item) => {
        const reference: ArtifactReferenceWire = {
          artifact_id: item.artifactId,
          version_id: item.versionId,
          relation: 'attached',
          filename: item.label,
        };
        if (item.contentType) reference.content_type = item.contentType;
        if (item.sizeBytes !== undefined) reference.size_bytes = item.sizeBytes;
        return reference;
      }),
    injectSkills: normalized
      .filter((item): item is ComposerSkillContext => item.kind === 'skill')
      .map((item) => item.name),
    injectMcpServerIds: normalized
      .filter((item): item is ComposerMcpContext => item.kind === 'mcp')
      .map((item) => item.serverId),
  };
}

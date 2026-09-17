import type { SynonBiomedProjectArtifact } from '@/renderer/services/synonBiomedGateway';
import {
  createComposerArtifactContext,
  type ComposerArtifactContext,
} from '@/renderer/components/chat/SendBox/composerCompositionModel';

export const getReferenceableProjectArtifacts = (
  artifacts: readonly SynonBiomedProjectArtifact[],
  projectId?: string | null
): SynonBiomedProjectArtifact[] => {
  const normalizedProjectId = projectId?.trim() ?? '';
  return artifacts
    .filter((artifact) => !artifact.isIntermediate)
    .filter((artifact) => typeof artifact.versionId === 'string' && artifact.versionId.trim().length > 0)
    .filter((artifact) => !normalizedProjectId || !artifact.projectId || artifact.projectId === normalizedProjectId)
    .toSorted((left, right) => {
      const leftDate = Date.parse(left.updatedAt ?? left.createdAt ?? '') || 0;
      const rightDate = Date.parse(right.updatedAt ?? right.createdAt ?? '') || 0;
      if (leftDate !== rightDate) return rightDate - leftDate;
      return left.filename.localeCompare(right.filename);
    });
};

export const buildProjectArtifactContext = (artifact: SynonBiomedProjectArtifact): ComposerArtifactContext | null =>
  createComposerArtifactContext({
    artifactId: artifact.artifactId,
    versionId: artifact.versionId ?? undefined,
    projectId: artifact.projectId,
    label: artifact.filename,
    contentType: artifact.contentType,
    sizeBytes: artifact.sizeBytes,
  });

export const formatProjectArtifactSize = (sizeBytes: number): string => {
  if (!Number.isFinite(sizeBytes) || sizeBytes <= 0) return '0 B';
  if (sizeBytes < 1024) return `${Math.round(sizeBytes)} B`;
  const kilobytes = sizeBytes / 1024;
  if (kilobytes < 1024) return `${kilobytes.toFixed(kilobytes >= 100 ? 0 : 1)} KB`;
  const megabytes = kilobytes / 1024;
  return `${megabytes.toFixed(megabytes >= 100 ? 0 : 1)} MB`;
};

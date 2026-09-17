import type { ISynonBiomedScientificFile } from '@/common/adapter/ipcBridge';
import type { ArtifactReferenceRelation, ArtifactReferenceWire } from '@/common/adapter/messageStreamProtocol';

export const artifactReferenceKey = (artifactId: string, versionId: string): string => `${artifactId}\0${versionId}`;

export function indexScientificFilesByArtifactReference(
  files: ISynonBiomedScientificFile[]
): ReadonlyMap<string, ISynonBiomedScientificFile> {
  const byVersion = new Map<string, ISynonBiomedScientificFile>();
  for (const file of files) {
    if (file.version_id === null) continue;
    byVersion.set(artifactReferenceKey(file.artifact_id, file.version_id), file);
  }
  return byVersion;
}

export function matchScientificFilesToArtifactReferences(
  references: ArtifactReferenceWire[] | undefined,
  files: ISynonBiomedScientificFile[],
  relations: ReadonlySet<ArtifactReferenceRelation>,
  indexedFiles?: ReadonlyMap<string, ISynonBiomedScientificFile>
): ISynonBiomedScientificFile[] {
  if (!references?.length) return [];
  const byVersion = indexedFiles ?? indexScientificFilesByArtifactReference(files);
  const matched: ISynonBiomedScientificFile[] = [];
  for (const reference of references) {
    if (!relations.has(reference.relation)) continue;
    if (reference.availability === 'deleted' || reference.availability === 'missing') {
      matched.push({
        artifact_id: reference.artifact_id,
        version_id: reference.version_id,
        version_number: 0,
        project_id: null,
        root_frame_id: null,
        frame_id: null,
        creating_frame_id: null,
        filename: reference.filename || reference.artifact_id,
        content_type: reference.content_type ?? null,
        size_bytes: reference.size_bytes ?? 0,
        preview_kind: 'unavailable',
        content_url: '',
        created_at: 0,
        updated_at: 0,
        agent_name: null,
        is_user_upload: false,
        is_intermediate: false,
        availability: reference.availability,
      });
      continue;
    }
    const file = byVersion.get(artifactReferenceKey(reference.artifact_id, reference.version_id));
    if (file) matched.push(file);
  }
  return matched;
}

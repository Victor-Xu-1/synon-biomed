import { ipcBridge } from '@/common';
import { uploadFileViaHttp } from '@/renderer/services/FileService';
import { uploadSynonBiomedArtifact, type SynonBiomedArtifactRecord } from '@/renderer/services/synonBiomedWorkspaceApi';
import { getSynonBiomedArtifactVersionContentUrl } from '@/renderer/services/synonBiomedArtifacts';

export type MaterializedDocumentResource = {
  source: string;
  displaySource: string;
  label: string;
};

type DocumentResourceDependencies = {
  uploadFile: typeof uploadFileViaHttp;
  uploadArtifact: typeof uploadSynonBiomedArtifact;
  copyFiles: typeof ipcBridge.fs.copyFilesToWorkspace.invoke;
  readImage: typeof ipcBridge.fs.getImageBase64.invoke;
};

const defaultDependencies: DocumentResourceDependencies = {
  uploadFile: uploadFileViaHttp,
  uploadArtifact: uploadSynonBiomedArtifact,
  copyFiles: ipcBridge.fs.copyFilesToWorkspace.invoke,
  readImage: ipcBridge.fs.getImageBase64.invoke,
};

export async function materializeDocumentLocalResource({
  file,
  kind,
  filePath,
  workspace,
  conversationId,
  dependencies = defaultDependencies,
}: {
  file: File;
  kind: 'image' | 'file';
  filePath?: string;
  workspace?: string;
  conversationId?: string;
  dependencies?: DocumentResourceDependencies;
}): Promise<MaterializedDocumentResource> {
  if (workspace?.startsWith('synonbiomed://')) {
    if (!conversationId) throw new Error('DOCUMENT_RESOURCE_CONVERSATION_REQUIRED');
    const artifact = await dependencies.uploadArtifact(file, conversationId);
    const versionId = artifactVersionId(artifact);
    const source = `{{artifact:${versionId}}}`;
    return {
      source,
      displaySource: getSynonBiomedArtifactVersionContentUrl(versionId),
      label: artifact.filename?.trim() || file.name,
    };
  }

  if (!filePath || !workspace) throw new Error('DOCUMENT_RESOURCE_WORKSPACE_REQUIRED');
  const uploadedPath = await dependencies.uploadFile(file, conversationId);
  const targetDirectory = directoryOf(filePath) || workspace;
  const copied = await dependencies.copyFiles({
    file_paths: [uploadedPath],
    workspace: targetDirectory,
  });
  const copiedPath = copied.copied_files?.[0];
  if (!copiedPath) throw new Error('DOCUMENT_RESOURCE_COPY_FAILED');

  const source = relativeDocumentResourcePath(filePath, copiedPath);
  const displaySource =
    kind === 'image' ? ((await dependencies.readImage({ path: copiedPath, workspace })) ?? source) : source;
  return { source, displaySource, label: basename(copiedPath) || file.name };
}

export function relativeDocumentResourcePath(documentPath: string, resourcePath: string): string {
  const documentParts = normalizePath(documentPath).split('/');
  documentParts.pop();
  const resourceParts = normalizePath(resourcePath).split('/');
  if (documentParts[0]?.toLowerCase() !== resourceParts[0]?.toLowerCase()) return resourcePath;

  let common = 0;
  while (
    common < documentParts.length &&
    common < resourceParts.length &&
    documentParts[common].toLowerCase() === resourceParts[common].toLowerCase()
  ) {
    common += 1;
  }
  const relative = [
    ...Array.from({ length: documentParts.length - common }, () => '..'),
    ...resourceParts.slice(common),
  ].join('/');
  return relative || basename(resourcePath);
}

function artifactVersionId(artifact: SynonBiomedArtifactRecord): string {
  const versionId = artifact.version_id?.trim();
  if (!versionId) throw new Error('DOCUMENT_RESOURCE_VERSION_REQUIRED');
  return versionId;
}

function directoryOf(filePath: string): string {
  const normalized = normalizePath(filePath);
  return normalized.slice(0, normalized.lastIndexOf('/'));
}

function basename(filePath: string): string {
  return normalizePath(filePath).split('/').at(-1) ?? '';
}

function normalizePath(filePath: string): string {
  return filePath.replaceAll('\\', '/').replace(/\/+$/, '');
}

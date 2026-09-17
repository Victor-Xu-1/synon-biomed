import type { SynonBiomedProjectArtifact, SynonBiomedProjectBench } from '@/renderer/services/synonBiomedGateway';

// Keep this pure model's module stem distinct from the ProjectArtifactLibrary component on case-insensitive filesystems.

export type ProjectArtifactGroupKind = 'uploads' | 'frame' | 'project' | 'search';
export type ProjectArtifactVisualKind = 'image' | 'markdown' | 'table' | 'structure' | 'pdf' | 'media' | 'file';

export type ProjectArtifactGroup = {
  id: string;
  kind: ProjectArtifactGroupKind;
  label: string;
  labelKey?: 'searchResults' | 'uploads' | 'projectFiles';
  rootFrameId: string | null;
  latestAt: string | null;
  artifacts: SynonBiomedProjectArtifact[];
};

export type BuildProjectArtifactGroupsInput = {
  artifacts: SynonBiomedProjectArtifact[];
  benches: SynonBiomedProjectBench[];
  query: string;
};

export function getVisibleProjectArtifacts(artifacts: SynonBiomedProjectArtifact[]): SynonBiomedProjectArtifact[] {
  return artifacts.filter((artifact) => !artifact.isIntermediate);
}

export function buildProjectArtifactGroups({
  artifacts,
  benches,
  query,
}: BuildProjectArtifactGroupsInput): ProjectArtifactGroup[] {
  const normalizedQuery = query.trim().toLocaleLowerCase();
  const visibleArtifacts = sortArtifacts(getVisibleProjectArtifacts(artifacts));

  if (normalizedQuery) {
    const matches = visibleArtifacts.filter((artifact) => matchesArtifactQuery(artifact, normalizedQuery));
    return matches.length === 0
      ? []
      : [
          {
            id: 'search',
            kind: 'search',
            label: '',
            labelKey: 'searchResults',
            rootFrameId: null,
            latestAt: latestArtifactDate(matches),
            artifacts: matches,
          },
        ];
  }

  const benchByFrameId = new Map<string, SynonBiomedProjectBench>();
  benches.forEach((bench) => {
    benchByFrameId.set(bench.frameId, bench);
    benchByFrameId.set(bench.rootFrameId, bench);
  });

  const uploads: SynonBiomedProjectArtifact[] = [];
  const projectFiles: SynonBiomedProjectArtifact[] = [];
  const artifactsByRootFrame = new Map<string, SynonBiomedProjectArtifact[]>();

  visibleArtifacts.forEach((artifact) => {
    if (artifact.isUserUpload) {
      uploads.push(artifact);
      return;
    }

    const ownerFrameId = artifact.rootFrameId ?? artifact.frameId ?? artifact.creatingFrameId;
    const ownerBench = ownerFrameId ? benchByFrameId.get(ownerFrameId) : undefined;
    if (!ownerBench) {
      projectFiles.push(artifact);
      return;
    }

    const rootFrameId = ownerBench.rootFrameId;
    const frameArtifacts = artifactsByRootFrame.get(rootFrameId) ?? [];
    frameArtifacts.push(artifact);
    artifactsByRootFrame.set(rootFrameId, frameArtifacts);
  });

  const groups: ProjectArtifactGroup[] = [];
  if (uploads.length > 0) {
    groups.push({
      id: 'uploads',
      kind: 'uploads',
      label: '',
      labelKey: 'uploads',
      rootFrameId: null,
      latestAt: latestArtifactDate(uploads),
      artifacts: uploads,
    });
  }

  const seenRootFrames = new Set<string>();
  benches.forEach((bench) => {
    if (seenRootFrames.has(bench.rootFrameId)) return;
    seenRootFrames.add(bench.rootFrameId);
    const frameArtifacts = artifactsByRootFrame.get(bench.rootFrameId);
    if (!frameArtifacts?.length) return;
    groups.push({
      id: `frame:${bench.rootFrameId}`,
      kind: 'frame',
      label: bench.name,
      rootFrameId: bench.rootFrameId,
      latestAt: latestArtifactDate(frameArtifacts),
      artifacts: frameArtifacts,
    });
  });

  if (projectFiles.length > 0) {
    groups.push({
      id: 'project',
      kind: 'project',
      label: '',
      labelKey: 'projectFiles',
      rootFrameId: null,
      latestAt: latestArtifactDate(projectFiles),
      artifacts: projectFiles,
    });
  }

  return groups;
}

export function classifyProjectArtifact(artifact: SynonBiomedProjectArtifact): ProjectArtifactVisualKind {
  const filename = artifact.filename.toLocaleLowerCase();
  const contentType = artifact.contentType?.toLocaleLowerCase() ?? '';
  if (contentType.startsWith('image/')) return 'image';
  if (contentType.includes('markdown') || filename.endsWith('.md') || filename.endsWith('.mdx')) return 'markdown';
  if (
    contentType.includes('csv') ||
    contentType.includes('spreadsheet') ||
    filename.endsWith('.csv') ||
    filename.endsWith('.tsv') ||
    filename.endsWith('.xlsx') ||
    filename.endsWith('.xls')
  ) {
    return 'table';
  }
  if (
    contentType.startsWith('chemical/') ||
    filename.endsWith('.pdb') ||
    filename.endsWith('.cif') ||
    filename.endsWith('.mmcif') ||
    filename.endsWith('.mol') ||
    filename.endsWith('.sdf')
  ) {
    return 'structure';
  }
  if (contentType === 'application/pdf' || filename.endsWith('.pdf')) return 'pdf';
  if (contentType.startsWith('audio/') || contentType.startsWith('video/')) return 'media';
  return 'file';
}

function matchesArtifactQuery(artifact: SynonBiomedProjectArtifact, normalizedQuery: string): boolean {
  return [artifact.filename, artifact.contentType, artifact.agentName]
    .filter((value): value is string => Boolean(value))
    .some((value) => value.toLocaleLowerCase().includes(normalizedQuery));
}

function sortArtifacts(artifacts: SynonBiomedProjectArtifact[]): SynonBiomedProjectArtifact[] {
  return artifacts.toSorted((left, right) => {
    const timeDifference = artifactTimestamp(right) - artifactTimestamp(left);
    return timeDifference || left.filename.localeCompare(right.filename, 'zh-CN');
  });
}

function latestArtifactDate(artifacts: SynonBiomedProjectArtifact[]): string | null {
  return artifacts.reduce<string | null>((latest, artifact) => {
    const candidate = artifact.updatedAt ?? artifact.createdAt;
    if (!candidate) return latest;
    return !latest || artifactTimestamp(artifact) > Date.parse(latest) ? candidate : latest;
  }, null);
}

function artifactTimestamp(artifact: SynonBiomedProjectArtifact): number {
  const value = artifact.updatedAt ?? artifact.createdAt;
  if (!value) return 0;
  const timestamp = Date.parse(value);
  return Number.isNaN(timestamp) ? 0 : timestamp;
}

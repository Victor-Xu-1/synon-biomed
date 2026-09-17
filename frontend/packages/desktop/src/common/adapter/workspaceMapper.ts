/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { IDirOrFile, IWorkspaceFlatFile } from './ipcBridge';

export type RawFsEntry = {
  name: string;
  type: string;
  relative_path?: string;
  read_only?: boolean;
  can_rename?: boolean;
  can_delete?: boolean;
  artifact_id?: string;
  version_id?: string | null;
  content_type?: string | null;
  size_bytes?: number;
  content_url?: string;
  preview_kind?: string;
};
export type RawWorkspaceFlatFile = { name: string; full_path: string; relative_path: string };

export type WorkspaceArtifactPage = {
  items: IDirOrFile[];
  nextCursor?: string;
  hasMore: boolean;
  total: number;
};

type RawWorkspaceArtifactPage = {
  contract: string;
  items: RawFsEntry[];
  next_cursor: string | null;
  has_more: boolean;
  total: number;
};

// ── Path helpers ───────────────────────────────────────────────────────

function normalizeSlashes(p: string): string {
  return p.replace(/\\/g, '/');
}

function stripTrailingSlash(p: string): string {
  return p.replace(/\/+$/, '');
}

// ── Frontend → Backend ─────────────────────────────────────────────────

export function absoluteToRelativePath(absolutePath: string, workspace: string): string {
  if (!absolutePath || !workspace) return absolutePath || '.';
  const abs = stripTrailingSlash(normalizeSlashes(absolutePath));
  const ws = stripTrailingSlash(normalizeSlashes(workspace));
  if (abs === ws) return '.';
  if (abs.startsWith(ws + '/')) {
    return abs.slice(ws.length + 1) || '.';
  }
  return absolutePath;
}

// ── Backend → Frontend ─────────────────────────────────────────────────

export function fromBackendFsEntry(item: RawFsEntry, workspace: string, parentRelPath: string): IDirOrFile {
  const ws = stripTrailingSlash(workspace);
  const name = item.name || '';
  const isDir = item.type === 'directory';
  const fallbackRelativePath = parentRelPath ? `${parentRelPath}/${name}` : name;
  const relativePath = item.relative_path || fallbackRelativePath;
  return {
    name,
    fullPath: `${ws}/${relativePath}`,
    relativePath,
    isDir,
    isFile: !isDir,
    ...(item.read_only === true ? { readOnly: true } : {}),
    ...(item.can_rename === true ? { canRename: true } : {}),
    ...(item.can_delete === true ? { canDelete: true } : {}),
    ...(typeof item.artifact_id === 'string' ? { artifactId: item.artifact_id } : {}),
    ...(typeof item.version_id === 'string' ? { versionId: item.version_id } : {}),
    ...(typeof item.content_type === 'string' ? { contentType: item.content_type } : {}),
    ...(typeof item.size_bytes === 'number' ? { sizeBytes: item.size_bytes } : {}),
    ...(typeof item.content_url === 'string' ? { contentUrl: item.content_url } : {}),
    ...(typeof item.preview_kind === 'string' ? { previewKind: item.preview_kind } : {}),
  };
}

export function fromBackendWorkspaceList(raw: RawFsEntry[], workspace: string, relPath: string): IDirOrFile[] {
  const ws = stripTrailingSlash(workspace);
  const base = relPath === '.' ? '' : relPath;
  const children = raw.map((item) => fromBackendFsEntry(item, ws, base));

  if (relPath === '.' || !relPath) {
    const rootName = ws.split('/').pop() || '';
    return [
      {
        name: rootName,
        fullPath: ws,
        relativePath: '',
        isDir: true,
        isFile: false,
        children,
      },
    ];
  }

  const dirName = relPath.split('/').pop() || '';
  return [
    {
      name: dirName,
      fullPath: `${ws}/${relPath}`,
      relativePath: relPath,
      isDir: true,
      isFile: false,
      children,
    },
  ];
}

export function fromBackendWorkspaceArtifactPage(
  raw: unknown,
  workspace: string,
  relPath: string
): WorkspaceArtifactPage {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) throw new Error('workspace_artifact_page_invalid');
  const page = raw as Partial<RawWorkspaceArtifactPage>;
  if (
    page.contract !== 'synon.project-artifact-page.v1' ||
    !Array.isArray(page.items) ||
    typeof page.has_more !== 'boolean' ||
    !Number.isSafeInteger(page.total) ||
    Number(page.total) < 0 ||
    (page.next_cursor !== null && typeof page.next_cursor !== 'string') ||
    (page.has_more && (!page.next_cursor || page.next_cursor.length > 2048)) ||
    (!page.has_more && page.next_cursor !== null)
  ) {
    throw new Error('workspace_artifact_page_invalid');
  }
  for (const item of page.items) {
    if (!item || typeof item !== 'object' || typeof item.name !== 'string' || typeof item.type !== 'string') {
      throw new Error('workspace_artifact_page_invalid');
    }
  }
  return {
    items: fromBackendWorkspaceList(page.items, workspace, relPath),
    ...(page.next_cursor ? { nextCursor: page.next_cursor } : {}),
    hasMore: page.has_more,
    total: Number(page.total),
  };
}

export function fromBackendWorkspaceFlatFiles(raw: RawWorkspaceFlatFile[]): IWorkspaceFlatFile[] {
  return raw.map((item) => ({
    name: item.name,
    fullPath: item.full_path,
    relativePath: item.relative_path,
  }));
}

/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { describe, expect, it } from 'vitest';
import {
  fromBackendWorkspaceFlatFiles,
  fromBackendWorkspaceList,
  type RawWorkspaceFlatFile,
} from '@/common/adapter/workspaceMapper';

describe('workspaceMapper', () => {
  it('preserves stable artifact paths and remote preview metadata for Synon Biomed workspace nodes', () => {
    const [root] = fromBackendWorkspaceList(
      [
        {
          name: 'STAT6_report.md',
          type: 'file',
          relative_path: 'frame-stat6/artifact-report',
          read_only: true,
          can_rename: true,
          can_delete: true,
          artifact_id: 'artifact-report',
          version_id: 'version-report',
          content_type: 'text/markdown',
          size_bytes: 4096,
          content_url: '/api/artifacts/artifact-report',
        },
      ],
      'synonbiomed://proj_stat6',
      '.'
    );

    expect(root?.children).toEqual([
      {
        name: 'STAT6_report.md',
        fullPath: 'synonbiomed://proj_stat6/frame-stat6/artifact-report',
        relativePath: 'frame-stat6/artifact-report',
        isDir: false,
        isFile: true,
        readOnly: true,
        canRename: true,
        canDelete: true,
        artifactId: 'artifact-report',
        versionId: 'version-report',
        contentType: 'text/markdown',
        sizeBytes: 4096,
        contentUrl: '/api/artifacts/artifact-report',
      },
    ]);
  });
  it('maps workspace flat files from backend snake_case to frontend camelCase', () => {
    const raw: RawWorkspaceFlatFile[] = [
      {
        name: 'main.ts',
        full_path: '/workspace/src/main.ts',
        relative_path: 'src/main.ts',
      },
    ];

    expect(fromBackendWorkspaceFlatFiles(raw)).toEqual([
      {
        name: 'main.ts',
        fullPath: '/workspace/src/main.ts',
        relativePath: 'src/main.ts',
      },
    ]);
  });

  it('does not leak snake_case path fields', () => {
    const [file] = fromBackendWorkspaceFlatFiles([
      {
        name: 'README.md',
        full_path: '/workspace/README.md',
        relative_path: 'README.md',
      },
    ]);

    expect(file).toBeDefined();
    expect((file as Record<string, unknown>).full_path).toBeUndefined();
    expect((file as Record<string, unknown>).relative_path).toBeUndefined();
    expect(file?.fullPath).toBe('/workspace/README.md');
    expect(file?.relativePath).toBe('README.md');
  });
});

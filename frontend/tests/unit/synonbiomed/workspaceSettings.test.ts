import { describe, expect, it, vi } from 'vitest';
import {
  addSynonBiomedAllowedDomain,
  changeSynonBiomedDataDirectory,
  clearSynonBiomedLastDataDirectoryMove,
  createSynonBiomedSecret,
  exportSynonBiomedArtifactToCloud,
  exportSynonBiomedArtifactsToCloud,
  getSynonBiomedCloudDownloadUrl,
  importSynonBiomedCloudObject,
  loadSynonBiomedContactEmail,
  loadSynonBiomedDataDirectory,
  loadSynonBiomedDiskUsage,
  loadSynonBiomedNetworkSettings,
  loadSynonBiomedSecrets,
  loadSynonBiomedStorageRules,
  loadSynonBiomedStorageSettings,
  loadSynonBiomedCloudBuckets,
  loadSynonBiomedCloudFolder,
  loadSynonBiomedHostGrants,
  loadSynonBiomedUseIntent,
  replaceSynonBiomedAllowedDomains,
  revokeSynonBiomedHostGrant,
  setSynonBiomedContactEmail,
  setSynonBiomedUseIntent,
  saveSynonBiomedStorageRules,
  updateSynonBiomedAllowlistGroups,
  updateSynonBiomedHostGrant,
  updateSynonBiomedSecret,
} from '@/renderer/services/synonBiomedWorkspaceSettings';

function json(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status, headers: { 'content-type': 'application/json' } });
}

describe('Synon Biomed workspace settings service', () => {
  it('loads and saves the project-file storage rules contract', async () => {
    const fetchImpl = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      if (String(input).endsWith('/api/settings/storage-rules') && init?.method === 'PUT') {
        return json({
          root: '/data',
          rules: {
            taskArtifacts: 'generated/task-files',
            logs: 'runtime/logs',
            toolResults: 'cache/tool-results',
            temp: 'scratch',
          },
          paths: {
            taskArtifacts: '/data/generated/task-files',
            logs: '/data/runtime/logs',
            toolResults: '/data/cache/tool-results',
            temp: '/data/scratch',
          },
          systemPaths: { taskRuns: '/data/task_runs', workspace: '/data/workspace' },
        });
      }
      return json({
        root: '/data',
        rules: { taskArtifacts: 'task_runs/artifacts', logs: 'shell_tasks', toolResults: 'tool-results', temp: 'tmp' },
        paths: {
          taskArtifacts: '/data/task_runs/artifacts',
          logs: '/data/shell_tasks',
          toolResults: '/data/tool-results',
          temp: '/data/tmp',
        },
        systemPaths: { taskRuns: '/data/task_runs', workspace: '/data/workspace' },
      });
    });

    const initial = await loadSynonBiomedStorageRules({ fetchImpl });
    expect(initial.rules.taskArtifacts).toBe('task_runs/artifacts');
    const updated = await saveSynonBiomedStorageRules(
      {
        taskArtifacts: 'generated/task-files',
        logs: 'runtime/logs',
        toolResults: 'cache/tool-results',
        temp: 'scratch',
      },
      { fetchImpl }
    );
    expect(updated.paths.toolResults).toBe('/data/cache/tool-results');
    expect(fetchImpl).toHaveBeenCalledWith(
      '/api/settings/storage-rules',
      expect.objectContaining({
        method: 'PUT',
        body: JSON.stringify({
          rules: {
            taskArtifacts: 'generated/task-files',
            logs: 'runtime/logs',
            toolResults: 'cache/tool-results',
            temp: 'scratch',
          },
        }),
      })
    );
  });

  it('projects v1.1 network settings and preserves mutation contracts', async () => {
    const fetchImpl = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith('/api/preferences/builtin-allowlist')) {
        return json({
          groups: [{ id: 'nih', label: 'NCBI / NIH', description: 'PubMed', locked: false, domains: ['*.nih.gov'] }],
          disabledGroups: ['nih'],
          activeKernelCount: 2,
          hasSeenOnboarding: true,
        });
      }
      if (url.endsWith('/api/preferences/allowed-domains')) {
        if (init?.method === 'POST' || init?.method === 'PUT') return json({});
        return json({ domains: ['3dmol.org'], configDomains: [], deniedDomains: ['localhost'] });
      }
      if (url.endsWith('/api/preferences/builtin-allowlist/disabled-groups')) return json({});
      return json({ detail: 'not found' }, 404);
    });

    const snapshot = await loadSynonBiomedNetworkSettings({ fetchImpl });
    expect(snapshot.groups[0]).toEqual(expect.objectContaining({ id: 'nih', domains: ['*.nih.gov'] }));
    expect(snapshot.domains).toEqual(['3dmol.org']);
    expect(snapshot.activeKernelCount).toBe(2);
    expect(snapshot.hasSeenOnboarding).toBe(true);

    await updateSynonBiomedAllowlistGroups([], { fetchImpl });
    await addSynonBiomedAllowedDomain('example.org', { fetchImpl });
    await replaceSynonBiomedAllowedDomains([], { fetchImpl });
    expect(fetchImpl).toHaveBeenCalledWith(
      '/api/preferences/builtin-allowlist/disabled-groups',
      expect.objectContaining({ method: 'PUT', body: JSON.stringify({ disabledGroups: [] }) })
    );
    expect(fetchImpl).toHaveBeenCalledWith(
      '/api/preferences/allowed-domains',
      expect.objectContaining({ method: 'POST', body: JSON.stringify({ domain: 'example.org' }) })
    );
    expect(fetchImpl).toHaveBeenCalledWith(
      '/api/preferences/allowed-domains',
      expect.objectContaining({ method: 'PUT', body: JSON.stringify({ domains: [] }) })
    );
  });

  it('loads, updates, and revokes persistent host directory grants', async () => {
    const fetchImpl = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith('/api/preferences/host-grants') && !init?.method) {
        return json({
          grants: [
            {
              id: '/data/project',
              hostPath: '/data/project',
              mountName: 'project',
              guestPath: '/workspace/project',
              mode: 'ro',
            },
          ],
        });
      }
      if (url.endsWith('/api/preferences/host-grants') && init?.method === 'PATCH') {
        return json({
          id: '/data/project',
          hostPath: '/data/project',
          mountName: 'project',
          guestPath: '/workspace/project',
          mode: 'rw',
          createdAt: '2026-08-17T00:00:00.000Z',
        });
      }
      if (url.endsWith('/api/preferences/host-grants') && init?.method === 'DELETE')
        return new Response(null, { status: 204 });
      return json({ detail: 'not found' }, 404);
    });

    const [grant] = await loadSynonBiomedHostGrants({ fetchImpl });
    expect(grant).toEqual(
      expect.objectContaining({ hostPath: '/data/project', guestPath: '/workspace/project', mode: 'ro' })
    );
    const updated = await updateSynonBiomedHostGrant('/data/project', 'rw', { fetchImpl });
    expect(updated.mode).toBe('rw');
    await revokeSynonBiomedHostGrant('/data/project', { fetchImpl });
    expect(fetchImpl).toHaveBeenCalledWith(
      '/api/preferences/host-grants',
      expect.objectContaining({ method: 'PATCH', body: JSON.stringify({ path: '/data/project', mode: 'rw' }) })
    );
    expect(fetchImpl).toHaveBeenCalledWith(
      '/api/preferences/host-grants',
      expect.objectContaining({ method: 'DELETE', body: JSON.stringify({ path: '/data/project' }) })
    );
  });

  it('creates secrets without reading them back and projects storage totals', async () => {
    const fetchImpl = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith('/api/secrets') && init?.method === 'POST') return json({ id: 'secret-1' }, 201);
      if (url.endsWith('/api/settings/data-dir'))
        return json({
          current: '/data',
          default: '/default',
          source: 'flag',
          usageBytes: 12,
          freeBytes: 34,
          activeFrames: 1,
        });
      if (url.endsWith('/api/preferences/disk-usage'))
        return json({
          artifacts: { totalBytes: 1 },
          workspace: { totalBytes: 2 },
          toolResults: { totalBytes: 3 },
          conda: { totalBytes: 4 },
          availableBytes: 5,
        });
      if (url.endsWith('/api/cloud-credentials'))
        return json([
          {
            id: 'cloud-1',
            provider: 's3',
            name: 'Research',
            credential_type: 'access_key',
            is_connected: true,
            default_bucket: 'bucket-a',
          },
        ]);
      return json({ detail: 'not found' }, 404);
    });

    await createSynonBiomedSecret({ provider: 'generic', name: 'API_KEY', value: 'secret' }, { fetchImpl });
    const snapshot = await loadSynonBiomedStorageSettings({ fetchImpl });
    expect(snapshot.dataDirectory).toEqual(expect.objectContaining({ current: '/data', activeFrames: 1 }));
    expect(snapshot.diskUsage.condaBytes).toBe(4);
    expect(snapshot.cloudCredentials[0]).toEqual(
      expect.objectContaining({ connected: true, defaultBucket: 'bucket-a' })
    );
  });

  it('projects redacted service credentials and preserves update and data-directory move contracts', async () => {
    const fetchImpl = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith('/api/secrets') && !init?.method) {
        return json({
          secrets: [
            {
              id: 'aws-1',
              provider: 'aws',
              name: 'AWS Research',
              credentialType: 'access_key',
              valueConfigured: false,
              credentialsConfigured: true,
              credentialFields: ['access_key_id', 'secret_access_key'],
              masked_preview: 'configured',
              masked_fields: ['access_key_id', 'secret_access_key'],
            },
          ],
        });
      }
      if (url.endsWith('/api/secrets/aws-1') && init?.method === 'PATCH') return json({ ok: true });
      if (url.endsWith('/api/settings/data-dir') && init?.method === 'POST') {
        return json({
          restarting: true,
          restartRequired: true,
          move: {
            id: 'move-1',
            source: '/data',
            target: '/new-data',
            migrate: true,
            copied: false,
            stagedAt: '2026-07-13T06:00:00Z',
          },
        });
      }
      if (url.endsWith('/api/settings/data-dir')) {
        return json({
          current: '/data',
          default: '/default',
          source: 'flag',
          configPath: '/config/synonbiomed.toml',
          usageBytes: 12,
          freeBytes: 34,
          activeFrames: 0,
          lastMove: {
            id: 'move-0',
            source: '/old-data',
            target: '/data',
            migrate: true,
            copied: true,
            stagedAt: '2026-07-12T06:00:00Z',
            completedAt: '2026-07-12T06:01:00Z',
          },
        });
      }
      if (url.includes('/api/settings/data-dir/last-move') && init?.method === 'DELETE')
        return new Response(null, { status: 204 });
      if (url.endsWith('/api/preferences/disk-usage')) {
        return json({
          artifacts: { totalBytes: 10 },
          workspace: { totalBytes: 20 },
          toolResults: { totalBytes: 30 },
          conda: { totalBytes: 40 },
          availableBytes: 50,
        });
      }
      return json({ detail: 'not found' }, 404);
    });

    const [secret] = await loadSynonBiomedSecrets({ fetchImpl });
    expect(secret).toEqual(
      expect.objectContaining({
        id: 'aws-1',
        credentialsConfigured: true,
        credentialFields: ['access_key_id', 'secret_access_key'],
      })
    );
    await updateSynonBiomedSecret('aws-1', { name: 'AWS Updated', region: 'us-east-2' }, { fetchImpl });
    expect(fetchImpl).toHaveBeenCalledWith(
      '/api/secrets/aws-1',
      expect.objectContaining({
        method: 'PATCH',
        body: JSON.stringify({ name: 'AWS Updated', region: 'us-east-2' }),
      })
    );

    const directory = await loadSynonBiomedDataDirectory({ fetchImpl });
    expect(directory).toEqual(
      expect.objectContaining({
        current: '/data',
        configPath: '/config/synonbiomed.toml',
        lastMove: expect.objectContaining({ source: '/old-data', target: '/data', copied: true }),
      })
    );
    expect(await loadSynonBiomedDiskUsage({ fetchImpl })).toEqual(
      expect.objectContaining({ artifactsBytes: 10, condaBytes: 40, availableBytes: 50 })
    );
    expect(await changeSynonBiomedDataDirectory({ path: '/new-data', migrate: true }, { fetchImpl })).toEqual(
      expect.objectContaining({ restarting: true, move: expect.objectContaining({ target: '/new-data' }) })
    );
    await clearSynonBiomedLastDataDirectoryMove(false, { fetchImpl });
    expect(fetchImpl).toHaveBeenCalledWith(
      '/api/settings/data-dir/last-move?deleteSource=0',
      expect.objectContaining({ method: 'DELETE' })
    );
  });

  it('projects and writes v1.1 authorization and contact settings', async () => {
    const fetchImpl = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith('/api/preferences/use-intent')) {
        return init?.method === 'PUT'
          ? json({ intent: 'noncommercial', declared: true })
          : json({ intent: 'commercial', declared: false });
      }
      if (url.endsWith('/api/contact-email')) {
        return init?.method === 'PUT'
          ? json({ decision: 'allowed', email: 'lab@example.org' })
          : json({
              decision: null,
              email: null,
              notice_text: 'Disclosure',
              notice_version: 'notice-1',
              notice_stale: false,
            });
      }
      return json({ detail: 'not found' }, 404);
    });

    expect(await loadSynonBiomedUseIntent({ fetchImpl })).toEqual({ intent: 'commercial', declared: false });
    expect(await loadSynonBiomedContactEmail({ fetchImpl })).toEqual(
      expect.objectContaining({ decision: null, noticeText: 'Disclosure', noticeVersion: 'notice-1' })
    );
    await setSynonBiomedUseIntent('noncommercial', { fetchImpl });
    await setSynonBiomedContactEmail('lab@example.org', 'notice-1', { fetchImpl });
    expect(fetchImpl).toHaveBeenCalledWith(
      '/api/preferences/use-intent',
      expect.objectContaining({ method: 'PUT', body: JSON.stringify({ intent: 'noncommercial' }) })
    );
    expect(fetchImpl).toHaveBeenCalledWith(
      '/api/contact-email',
      expect.objectContaining({
        method: 'PUT',
        body: JSON.stringify({ email: 'lab@example.org', notice_version: 'notice-1' }),
      })
    );
  });

  it('preserves cloud browse, download and import contracts', async () => {
    const fetchImpl = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith('/buckets')) return json(['bucket-a']);
      if (url.includes('/folder?')) {
        return json({
          folders: ['source/results/'],
          files: [{ key: 'source/input.txt', size: 12, last_modified: '2026-07-12T12:00:00Z' }],
        });
      }
      if (url.endsWith('/import') && init?.method === 'POST') return json({ id: 'artifact-1' });
      if (url.endsWith('/export') && init?.method === 'POST') return json({ exported: true });
      return json({ detail: 'not found' }, 404);
    });

    expect(await loadSynonBiomedCloudBuckets('cloud-1', { fetchImpl })).toEqual(['bucket-a']);
    expect(await loadSynonBiomedCloudFolder('cloud-1', 'bucket-a', 'source/', { fetchImpl })).toEqual({
      folders: ['source/results/'],
      files: [{ key: 'source/input.txt', size: 12, lastModified: '2026-07-12T12:00:00Z' }],
    });
    await importSynonBiomedCloudObject(
      'cloud-1',
      { bucket: 'bucket-a', key: 'source/input.txt', projectId: 'project-1' },
      { fetchImpl }
    );
    expect(fetchImpl).toHaveBeenLastCalledWith(
      '/api/cloud-credentials/cloud-1/import',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ bucket: 'bucket-a', key: 'source/input.txt', project_id: 'project-1' }),
      })
    );
    expect(getSynonBiomedCloudDownloadUrl('cloud-1', 'bucket-a', 'source/input.txt')).toBe(
      '/api/cloud-credentials/cloud-1/download?bucket=bucket-a&key=source%2Finput.txt'
    );
    await exportSynonBiomedArtifactToCloud(
      'cloud-1',
      { artifactId: 'artifact-1', bucket: 'bucket-a', key: 'results/output.txt' },
      { fetchImpl }
    );
    expect(fetchImpl).toHaveBeenLastCalledWith(
      '/api/cloud-credentials/cloud-1/export',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ artifact_id: 'artifact-1', bucket: 'bucket-a', key: 'results/output.txt' }),
      })
    );
  });

  it('maps an oversized cloud import to the shared typed file error', async () => {
    const fetchImpl = vi
      .fn()
      .mockResolvedValue(
        Response.json({ remoteKind: 'too_large', detail: 'cloud object exceeds a private limit' }, { status: 413 })
      );

    await expect(
      importSynonBiomedCloudObject(
        'cloud-1',
        { bucket: 'bucket-a', key: 'source/large.bin', projectId: 'project-1' },
        { fetchImpl }
      )
    ).rejects.toMatchObject({
      name: 'SynonBiomedFileImportError',
      status: 413,
      kind: 'too_large',
      message: 'Synon Biomed file import failed (413) [too_large]',
    });
  });

  it('reports progress and preserves partial failures during batch cloud export', async () => {
    const fetchImpl = vi.fn(async (_input: string | URL | Request, init?: RequestInit) => {
      const body = JSON.parse(String(init?.body)) as { artifact_id: string };
      return body.artifact_id === 'artifact-b' ? json({ detail: 'quota exceeded' }, 507) : json({ exported: true });
    });
    const progress = vi.fn();

    const result = await exportSynonBiomedArtifactsToCloud(
      'cloud-1',
      {
        bucket: 'bucket-a',
        items: [
          { artifactId: 'artifact-a', key: 'exports/a.csv' },
          { artifactId: 'artifact-b', key: 'exports/b.csv' },
          { artifactId: 'artifact-c', key: 'exports/c.csv' },
        ],
      },
      progress,
      { fetchImpl }
    );

    expect(result.completed.map((item) => item.artifactId)).toEqual(['artifact-a', 'artifact-c']);
    expect(result.failed).toEqual([{ artifactId: 'artifact-b', key: 'exports/b.csv', error: 'quota exceeded' }]);
    expect(progress).toHaveBeenCalledTimes(3);
    expect(progress).toHaveBeenLastCalledWith(
      expect.objectContaining({
        completed: 3,
        total: 3,
        current: expect.objectContaining({ artifactId: 'artifact-c' }),
      })
    );
  });
});

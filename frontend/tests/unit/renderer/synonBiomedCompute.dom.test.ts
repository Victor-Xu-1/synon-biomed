import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  addSynonBiomedInferenceProvider,
  addSynonBiomedSshHost,
  deleteSynonBiomedComputeProvider,
  getSynonBiomedLocalFileUrl,
  getSynonBiomedRemoteFileUrl,
  importSynonBiomedLocalFile,
  importSynonBiomedRemoteFile,
  loadSynonBiomedLocalDirectory,
  loadSynonBiomedLocalHostInfo,
  loadSynonBiomedRemoteDirectory,
  loadSynonBiomedComputeGpuEnabled,
  loadSynonBiomedComputeGpuInfo,
  loadSynonBiomedComputeJob,
  loadSynonBiomedComputeJobLog,
  loadSynonBiomedComputeJobs,
  loadSynonBiomedBioNemoSettings,
  loadSynonBiomedModalSettings,
  loadSynonBiomedSessionComputeProviders,
  loadSynonBiomedManagedEndpoints,
  loadSynonBiomedComputeProvider,
  loadSynonBiomedComputeProviders,
  loadSynonBiomedSshAliases,
  probeSynonBiomedComputeProvider,
  saveSynonBiomedComputeProviderDetails,
  setSynonBiomedComputeGpuEnabled,
  setSynonBiomedBioNemoSettings,
  setSynonBiomedModalEnabled,
  setSynonBiomedSessionComputeProvider,
  stopSynonBiomedManagedEndpoint,
} from '@/renderer/services/synonBiomedCompute';

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('Synon Biomed compute service', () => {
  it('loads provider summaries, details and SSH aliases from the native compute API', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify([
            {
              name: 'ssh:hpc-a',
              displayName: 'HPC A',
              family: 'ssh',
              checked: true,
              scratchRoot: '/scratch/victor',
              dataRoots: ['/data/shared'],
              maxConcurrentJobs: 4,
            },
          ])
        )
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            name: 'ssh:hpc-a',
            displayName: 'HPC A',
            family: 'ssh',
            checked: true,
            detailsMd: '## Cluster\nSlurm cluster',
            scratchRoot: '/scratch/victor',
            scratchRootSource: 'user',
            dataRoots: ['/data/shared'],
            maxConcurrentJobs: 4,
          })
        )
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            aliases: [{ alias: 'hpc-a', hostName: 'hpc.example.org', user: 'victor' }],
            configFound: true,
            configPath: '/home/victor_1/.ssh/config',
            wildcardCount: 0,
            isWsl: true,
          })
        )
      );
    vi.stubGlobal('fetch', fetchMock);

    const providers = await loadSynonBiomedComputeProviders();
    const details = await loadSynonBiomedComputeProvider('ssh:hpc-a');
    const aliases = await loadSynonBiomedSshAliases();

    expect(providers[0]).toMatchObject({
      name: 'ssh:hpc-a',
      scratchRoot: '/scratch/victor',
      dataRoots: ['/data/shared'],
      maxConcurrentJobs: 4,
    });
    expect(details).toMatchObject({ detailsMd: '## Cluster\nSlurm cluster', scratchRootSource: 'user' });
    expect(aliases.aliases[0]).toMatchObject({ alias: 'hpc-a', hostName: 'hpc.example.org' });
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/compute/providers/ssh%3Ahpc-a',
      expect.objectContaining({ headers: { Accept: 'application/json' } })
    );
    expect(fetchMock).toHaveBeenNthCalledWith(
      3,
      '/api/compute/ssh-config-aliases',
      expect.objectContaining({ headers: { Accept: 'application/json' } })
    );
  });

  it('maps the native Modal and BioNeMo family settings without legacy UI adapters', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            provider: 'modal',
            enabled: true,
            detailsMd: 'Modal notes',
            appName: 'synonbiomed-research',
            environmentName: 'main',
            egressPolicy: { mode: 'allowlist', mirror: true, additional: ['api.example.com'] },
            maxConcurrentJobs: 10,
            maxTimeoutSec: 43200,
            profiles: [{ name: 'Synon Biomed (stored)', active: true, tokenIdMasked: 'ak-a····test' }],
            ignoredProfiles: ['legacy'],
            tomlMissing: true,
            hasStoredCredential: true,
          })
        )
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({ enabled: true, override: true, mode: 'hosted', hostedHost: 'health.api.nvidia.com' })
        )
      )
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({ enabled: false, override: false, mode: 'local', hostedHost: 'health.api.nvidia.com' })
        )
      );
    vi.stubGlobal('fetch', fetchMock);

    await expect(loadSynonBiomedModalSettings()).resolves.toMatchObject({
      enabled: true,
      appName: 'synonbiomed-research',
      maxTimeoutSec: 43200,
      profiles: [{ name: 'Synon Biomed (stored)', active: true }],
    });
    await expect(loadSynonBiomedBioNemoSettings()).resolves.toEqual({
      enabled: true,
      override: true,
      mode: 'hosted',
      hostedHost: 'health.api.nvidia.com',
    });
    await setSynonBiomedModalEnabled(false);
    await expect(
      setSynonBiomedBioNemoSettings({ enabled: false, mode: 'local', hostedHost: 'health.api.nvidia.com' })
    ).resolves.toEqual({ enabled: false, override: false, mode: 'local', hostedHost: 'health.api.nvidia.com' });

    expect(fetchMock).toHaveBeenNthCalledWith(
      3,
      '/api/compute/byoc/modal/enabled',
      expect.objectContaining({ method: 'PUT', body: JSON.stringify({ enabled: false }) })
    );
    expect(fetchMock).toHaveBeenNthCalledWith(
      4,
      '/api/compute/bionemo/enabled',
      expect.objectContaining({
        method: 'PUT',
        body: JSON.stringify({ enabled: false, mode: 'local', hostedHost: 'health.api.nvidia.com' }),
      })
    );
  });

  it('uses the exact Synon Biomed mutation contracts for provider lifecycle operations', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ warning: 'credential is not saved' })))
      .mockResolvedValueOnce(new Response(JSON.stringify({ scheduler: 'none', cpus: 0, gpus: 0 })))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    vi.stubGlobal('fetch', fetchMock);

    await addSynonBiomedSshHost({
      alias: 'hpc-a',
      initialContext: 'Slurm cluster',
      dataRoots: ['/data/shared'],
      overrides: { user: 'victor', port: 2222, identityFile: '~/.ssh/id_ed25519' },
    });
    await addSynonBiomedInferenceProvider({
      name: 'boltz-local',
      endpoint: 'http://127.0.0.1:9000',
      skillName: 'boltz2-nim',
      credentialName: 'NVIDIA_API_KEY',
    });
    await probeSynonBiomedComputeProvider('infer:boltz-local');
    await saveSynonBiomedComputeProviderDetails('ssh:hpc-a', {
      detailsMd: '## Cluster\nUpdated',
      maxConcurrentJobs: 8,
      scratchRoot: '/scratch/victor',
      dataRoots: ['/data/shared', '/datasets'],
    });
    await deleteSynonBiomedComputeProvider({ name: 'ssh:hpc-a', family: 'ssh' });
    await deleteSynonBiomedComputeProvider({ name: 'infer:boltz-local', family: 'infer' });

    expect(JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body))).toEqual({
      alias: 'hpc-a',
      initialContext: 'Slurm cluster',
      dataRoots: ['/data/shared'],
      overrides: { user: 'victor', port: 2222, identityFile: '~/.ssh/id_ed25519' },
    });
    expect(fetchMock.mock.calls[1]?.[0]).toBe('/api/compute/inference-providers');
    expect(fetchMock.mock.calls[2]?.[0]).toBe('/api/compute/providers/infer%3Aboltz-local/probe');
    expect(fetchMock.mock.calls[3]).toEqual([
      '/api/compute/providers/ssh%3Ahpc-a',
      expect.objectContaining({
        method: 'PATCH',
        body: JSON.stringify({ detailsMd: '## Cluster\nUpdated', maxConcurrentJobs: 8 }),
      }),
    ]);
    expect(fetchMock.mock.calls[4]).toEqual([
      '/api/compute/providers/ssh%3Ahpc-a/scratch-root',
      expect.objectContaining({ method: 'PUT', body: JSON.stringify({ scratchRoot: '/scratch/victor' }) }),
    ]);
    expect(fetchMock.mock.calls[5]).toEqual([
      '/api/compute/providers/ssh%3Ahpc-a/data-roots',
      expect.objectContaining({ method: 'PUT', body: JSON.stringify({ roots: ['/data/shared', '/datasets'] }) }),
    ]);
    expect(fetchMock.mock.calls[6]?.[0]).toBe('/api/compute/providers/ssh%3Ahpc-a');
    expect(fetchMock.mock.calls[7]?.[0]).toBe('/api/compute/inference-providers/infer%3Aboltz-local');
  });

  it('surfaces backend validation details instead of replacing them with a generic error', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn<typeof fetch>().mockResolvedValue(
        new Response(JSON.stringify({ detail: "alias 'missing' must be defined in ~/.ssh/config" }), {
          status: 400,
          headers: { 'content-type': 'application/json' },
        })
      )
    );

    await expect(addSynonBiomedSshHost({ alias: 'missing' })).rejects.toThrow(
      "alias 'missing' must be defined in ~/.ssh/config"
    );
  });

  it('surfaces a stable probe failure without retrying the provider endpoint', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValueOnce(
      new Response(JSON.stringify({ detail: 'inference endpoint is unreachable' }), {
        status: 502,
        headers: { 'content-type': 'application/json' },
      })
    );
    vi.stubGlobal('fetch', fetchMock);

    await expect(probeSynonBiomedComputeProvider('infer:boltz-local')).rejects.toThrow(
      'inference endpoint is unreachable'
    );
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('uses the exact v1.1 GPU, managed endpoint and compute job contracts', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            available: true,
            gpu_name: 'NVIDIA A100',
            gpu_memory_mb: 81920,
            cuda_version: '12.4',
            gpu_count: 2,
          })
        )
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ enabled: true, override: true, present: true, name: 'NVIDIA A100' }))
      )
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify([
            {
              name: 'boltz2-local',
              displayName: 'Boltz2 Local',
              location: 'local',
              state: 'live',
              port: 9000,
              endpoint: 'http://127.0.0.1:9000',
              serviceDir: '/srv/boltz2',
              serviceDirBytes: 1024,
            },
          ])
        )
      )
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            jobs: [
              {
                jobId: 'compute-fixture-job-0001',
                environment: 'python',
                tierType: 'gpu',
                provider: 'fixture-nim',
                frameId: 'frame-a',
                projectId: 'project-a',
                state: 'running',
                startedAt: '2026-07-11T08:00:00Z',
                startedAtIso: '2026-07-11T08:00:00Z',
                intent: { kind: 'inference' },
                hardwareDetails: { gpu: 'fixture' },
                originToolUseId: 'tool-compute-1',
                rootFrameId: 'root-a',
                providerFamily: 'infer',
                providerLabel: 'fixture-nim',
                externalId: 'sandbox-1',
                externalUrl: 'https://compute.example/jobs/sandbox-1',
                supportsTail: true,
                endedAtIso: null,
                errorKind: null,
                leftOnRemote: [],
                systemHint: null,
              },
            ],
          })
        )
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            jobId: 'compute-fixture-job-0001',
            environment: 'python',
            tierType: 'gpu',
            provider: 'fixture-nim',
            frameId: 'frame-a',
            projectId: 'project-a',
            state: 'running',
            startedAt: '2026-07-11T08:00:00Z',
            startedAtIso: '2026-07-11T08:00:00Z',
            intent: { kind: 'inference' },
            hardwareDetails: { gpu: 'fixture' },
            originToolUseId: 'tool-compute-1',
            rootFrameId: 'root-a',
            providerFamily: 'infer',
            providerLabel: 'fixture-nim',
            externalId: 'sandbox-1',
            externalUrl: 'https://compute.example/jobs/sandbox-1',
            supportsTail: true,
            endedAtIso: null,
            harvest: {
              stdout: { exists: true, size: 70009 },
              stderr: { exists: true, size: 8 },
            },
            errorKind: null,
            leftOnRemote: [],
            systemHint: null,
          })
        )
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ exists: true, size: 14, text: 'step 1\nstep 2\n', truncated: false }))
      )
      .mockResolvedValueOnce(new Response(JSON.stringify(['infer:fixture-nim'])))
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    vi.stubGlobal('fetch', fetchMock);

    await expect(loadSynonBiomedComputeGpuInfo()).resolves.toMatchObject({
      available: true,
      name: 'NVIDIA A100',
      memoryMb: 81920,
      count: 2,
    });
    await expect(loadSynonBiomedComputeGpuEnabled()).resolves.toEqual({
      enabled: true,
      override: true,
      present: true,
      name: 'NVIDIA A100',
    });
    await setSynonBiomedComputeGpuEnabled(false);
    await expect(loadSynonBiomedManagedEndpoints({ includeSizes: true })).resolves.toEqual([
      expect.objectContaining({ name: 'boltz2-local', state: 'live', serviceDirBytes: 1024 }),
    ]);
    await stopSynonBiomedManagedEndpoint('boltz2-local');
    await expect(loadSynonBiomedComputeJobs('project-a')).resolves.toEqual([
      expect.objectContaining({
        jobId: 'compute-fixture-job-0001',
        state: 'running',
        rootFrameId: 'root-a',
        providerLabel: 'fixture-nim',
      }),
    ]);
    await expect(loadSynonBiomedComputeJob('compute-fixture-job-0001')).resolves.toMatchObject({
      state: 'running',
      supportsTail: true,
      harvest: { stdout: { exists: true, size: 70009 }, stderr: { exists: true, size: 8 } },
    });
    await expect(
      loadSynonBiomedComputeJobLog('compute-fixture-job-0001', { stream: 'stderr', tail: 65536 })
    ).resolves.toEqual({
      exists: true,
      size: 14,
      content: 'step 1\nstep 2\n',
      truncated: false,
    });
    await expect(loadSynonBiomedSessionComputeProviders('root-a')).resolves.toEqual(['infer:fixture-nim']);
    await setSynonBiomedSessionComputeProvider('root-a', 'infer:fixture-nim', false);

    expect(fetchMock.mock.calls.map(([url, init]) => `${init?.method ?? 'GET'} ${url}`)).toEqual([
      'GET /api/compute/gpu',
      'GET /api/compute/gpu/enabled',
      'PUT /api/compute/gpu/enabled',
      'GET /api/compute/managed-endpoints?sizes=1',
      'POST /api/compute/managed-endpoints/boltz2-local/stop',
      'GET /api/compute/jobs?projectId=project-a',
      'GET /api/compute/jobs/compute-fixture-job-0001',
      'GET /api/compute/jobs/compute-fixture-job-0001/logs?stream=stderr&tail=65536',
      'GET /api/compute/session/root-a/enabled',
      'PUT /api/compute/session/root-a/enabled/infer%3Afixture-nim',
    ]);
    expect(fetchMock.mock.calls[2]?.[1]?.body).toBe(JSON.stringify({ enabled: false }));
    expect(fetchMock.mock.calls[9]?.[1]?.body).toBe(JSON.stringify({ checked: false }));
  });

  it('reports a missing compute job returned as JSON null', async () => {
    vi.stubGlobal('fetch', vi.fn<typeof fetch>().mockResolvedValue(new Response('null')));
    await expect(loadSynonBiomedComputeJob('missing-job')).rejects.toThrow(
      'Synon Biomed compute job does not exist: missing-job'
    );
  });

  it('uses the live v1.1 local and SSH file browse, download and import contracts', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(new Response(JSON.stringify({ hostLabel: 'workstation', hostDetail: 'victor' })))
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            entries: [{ name: 'results', isDirectory: true, size: 0, mtime: 123 }],
            truncated: false,
            roots: { home: '/home/victor' },
            resolvedPath: '/home/victor',
          })
        )
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            entries: [{ name: 'output.csv', isDirectory: false, size: 42, mtime: 456 }],
            truncated: false,
            roots: { home: '/home/remote' },
            resolvedPath: '/scratch/job',
          })
        )
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ artifactId: 'artifact-local', versionId: 'version-local', filename: 'a.txt' }))
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ artifact_id: 'artifact-remote', version_id: null, filename: 'output.csv' }))
      );

    await expect(loadSynonBiomedLocalHostInfo(fetchMock)).resolves.toEqual({
      hostLabel: 'workstation',
      hostDetail: 'victor',
    });
    await expect(loadSynonBiomedLocalDirectory('/home/victor', fetchMock)).resolves.toMatchObject({
      resolvedPath: '/home/victor',
      entries: [{ name: 'results', isDirectory: true }],
    });
    await expect(loadSynonBiomedRemoteDirectory('ssh:hpc-a', '/scratch/job', fetchMock)).resolves.toMatchObject({
      resolvedPath: '/scratch/job',
      entries: [{ name: 'output.csv', size: 42 }],
    });
    await expect(importSynonBiomedLocalFile('/tmp/a.txt', 'project-a', fetchMock)).resolves.toMatchObject({
      artifactId: 'artifact-local',
    });
    await expect(
      importSynonBiomedRemoteFile('ssh:hpc-a', '/scratch/job/output.csv', 'project-a', fetchMock)
    ).resolves.toMatchObject({ artifactId: 'artifact-remote' });

    expect(fetchMock.mock.calls.map(([url, init]) => [url, init?.method ?? 'GET'])).toEqual([
      ['/api/compute/local/hostinfo', 'GET'],
      ['/api/compute/local/files?path=%2Fhome%2Fvictor', 'GET'],
      ['/api/compute/providers/ssh%3Ahpc-a/files?path=%2Fscratch%2Fjob', 'GET'],
      ['/api/compute/local/import', 'POST'],
      ['/api/compute/providers/ssh%3Ahpc-a/import', 'POST'],
    ]);
    expect(getSynonBiomedLocalFileUrl('/tmp/a b.txt', 'inline')).toBe(
      '/api/compute/local/download?path=%2Ftmp%2Fa+b.txt&disposition=inline'
    );
    expect(getSynonBiomedRemoteFileUrl('ssh:hpc-a', '/scratch/out.csv')).toBe(
      '/api/compute/providers/ssh%3Ahpc-a/download?path=%2Fscratch%2Fout.csv&disposition=attachment'
    );
  });

  it('preserves a typed remote import reason without exposing backend detail', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(
        Response.json({ remoteKind: 'too_large', detail: '/private/result.csv exceeds a limit' }, { status: 413 })
      );

    await expect(
      importSynonBiomedRemoteFile('ssh:hpc-a', '/scratch/result.csv', 'project-a', fetchMock)
    ).rejects.toMatchObject({
      name: 'SynonBiomedFileImportError',
      status: 413,
      kind: 'too_large',
      message: 'Synon Biomed file import failed (413) [too_large]',
    });
  });
});

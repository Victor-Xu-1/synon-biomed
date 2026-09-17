import { chmod, mkdir, readFile, rm, rmdir, stat, writeFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { homedir } from 'node:os';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { probeSynonBiomedComputeProvider } from '../../packages/desktop/src/renderer/services/synonBiomedCompute';
import { createSynonBiomedTestFetch } from './synonbiomedTestAuth';

const gatewayBaseUrl = process.env.SYNON_BIOMED_GATEWAY_URL ?? 'http://127.0.0.1:8766';
const authenticatedFetch = createSynonBiomedTestFetch(gatewayBaseUrl);

type Provider = {
  name: string;
  family: string;
  detailsMd?: string;
  scratchRoot?: string | null;
  scratchRootSource?: string;
  dataRoots?: string[];
  maxConcurrentJobs?: number | null;
};

async function request(path: string, init?: RequestInit): Promise<Response> {
  const response = await (await authenticatedFetch)(`${gatewayBaseUrl}${path}`, init);
  if (!response.ok) throw new Error(`${init?.method ?? 'GET'} ${path}: ${response.status} ${await response.text()}`);
  return response;
}

async function requestJson<T>(path: string, init?: RequestInit): Promise<T> {
  return (await request(path, init)).json() as Promise<T>;
}

async function jsonMutation(path: string, method: 'POST' | 'PATCH' | 'PUT', body: unknown): Promise<Response> {
  return request(path, {
    method,
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(body),
  });
}

describe('Synon Biomed real compute provider lifecycle', () => {
  it('creates, probes, edits and removes inference and SSH providers through the SynonAI gateway', async () => {
    const initial = await requestJson<Provider[]>('/api/compute/providers');
    const initialNames = new Set(initial.map((provider) => provider.name));
    const suffix = `${Date.now().toString(36)}-${process.pid}`;
    const inferenceSlug = `codex-compute-${suffix}`.slice(0, 63);
    const inferenceName = `infer:${inferenceSlug}`;
    const sshAlias = `codex_ssh_${suffix}`.replace(/[^A-Za-z0-9_.-]/g, '_').slice(0, 63);
    const sshName = `ssh:${sshAlias}`;
    const scratchRoot = `/tmp/${inferenceSlug}/scratch`;
    const dataRoots = [`/tmp/${inferenceSlug}/data`, `/tmp/${inferenceSlug}/datasets`];

    const probeServer = createServer((_req, res) => {
      res.writeHead(200, { 'content-type': 'application/json' });
      res.end(JSON.stringify({ status: 'ok' }));
    });
    await new Promise<void>((resolve, reject) => {
      probeServer.once('error', reject);
      probeServer.listen(0, '127.0.0.1', resolve);
    });
    const address = probeServer.address();
    if (!address || typeof address === 'string') throw new Error('probe server did not expose a TCP port');

    const sshDir = join(homedir(), '.ssh');
    const sshConfigPath = join(sshDir, 'config');
    const sshDirExisted = await stat(sshDir)
      .then(() => true)
      .catch(() => false);
    const originalSshConfig = await readFile(sshConfigPath).catch(() => null);
    let inferenceCreated = false;
    let sshCreated = false;
    const rendererFetch = async (input: string, init?: RequestInit) =>
      (await authenticatedFetch)(`${gatewayBaseUrl}${input}`, init);

    try {
      const inferenceResponse = await jsonMutation('/api/compute/inference-providers', 'POST', {
        name: inferenceSlug,
        endpoint: `http://127.0.0.1:${address.port}`,
        skillName: 'codex-compute-probe',
      });
      expect([200, 201]).toContain(inferenceResponse.status);
      inferenceCreated = true;

      const probe = await probeSynonBiomedComputeProvider(inferenceName, rendererFetch);
      expect(probe).toMatchObject({ scheduler: 'none', cpus: 0, gpus: 0 });

      await jsonMutation(`/api/compute/providers/${encodeURIComponent(inferenceName)}`, 'PATCH', {
        detailsMd: '## Codex real lifecycle\nTemporary provider',
      });

      const inferenceDetail = await requestJson<Provider>(
        `/api/compute/providers/${encodeURIComponent(inferenceName)}`
      );
      expect(inferenceDetail).toMatchObject({
        name: inferenceName,
        family: 'infer',
        detailsMd: '## Codex real lifecycle\nTemporary provider',
      });

      await mkdir(sshDir, { recursive: true, mode: 0o700 });
      const existing = originalSshConfig?.toString('utf8') ?? '';
      const separator = existing.length === 0 || existing.endsWith('\n') ? '' : '\n';
      await writeFile(
        sshConfigPath,
        `${existing}${separator}\nHost ${sshAlias}\n  HostName 127.0.0.1\n  User ${process.env.USER ?? 'victor_1'}\n  Port 9\n`,
        { mode: 0o600 }
      );
      await chmod(sshConfigPath, 0o600);

      const aliases = await requestJson<{ aliases: Array<string | { alias: string }> }>(
        '/api/compute/ssh-config-aliases'
      );
      expect(
        aliases.aliases.some((entry) => (typeof entry === 'string' ? entry === sshAlias : entry.alias === sshAlias))
      ).toBe(true);

      await jsonMutation('/api/compute/ssh-hosts', 'POST', {
        alias: sshAlias,
        initialContext: 'Temporary real lifecycle provider',
        dataRoots: [`/tmp/${sshAlias}/data`],
        overrides: { user: process.env.USER ?? 'victor_1', port: 9 },
      });
      sshCreated = true;
      await jsonMutation(`/api/compute/providers/${encodeURIComponent(sshName)}`, 'PATCH', {
        detailsMd: '## Codex real SSH lifecycle\nTemporary provider',
        maxConcurrentJobs: 3,
      });
      await jsonMutation(`/api/compute/providers/${encodeURIComponent(sshName)}/scratch-root`, 'PUT', {
        scratchRoot,
      });
      await jsonMutation(`/api/compute/providers/${encodeURIComponent(sshName)}/data-roots`, 'PUT', {
        roots: dataRoots,
      });

      const withSsh = await requestJson<Provider[]>('/api/compute/providers');
      expect(withSsh.some((provider) => provider.name === sshName && provider.family === 'ssh')).toBe(true);
      const sshDetail = await requestJson<Provider>(`/api/compute/providers/${encodeURIComponent(sshName)}`);
      expect(sshDetail).toMatchObject({
        name: sshName,
        family: 'ssh',
        detailsMd: '## Codex real SSH lifecycle\nTemporary provider',
        scratchRoot,
        scratchRootSource: 'user',
        dataRoots,
        maxConcurrentJobs: 3,
      });
    } finally {
      if (sshCreated) {
        await request(`/api/compute/providers/${encodeURIComponent(sshName)}`, { method: 'DELETE' }).catch(
          () => undefined
        );
      }
      if (inferenceCreated) {
        await request(`/api/compute/inference-providers/${encodeURIComponent(inferenceName)}`, {
          method: 'DELETE',
        }).catch(() => undefined);
      }
      if (originalSshConfig) {
        await writeFile(sshConfigPath, originalSshConfig, { mode: 0o600 });
      } else {
        await rm(sshConfigPath, { force: true });
        if (!sshDirExisted) await rmdir(sshDir).catch(() => undefined);
      }
      await new Promise<void>((resolve) => probeServer.close(() => resolve()));
    }

    const restored = await requestJson<Provider[]>('/api/compute/providers');
    expect(restored.some((provider) => provider.name === inferenceName || provider.name === sshName)).toBe(false);
    expect(new Set(restored.map((provider) => provider.name))).toEqual(initialNames);
  }, 60_000);
});

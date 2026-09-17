import { describe, expect, it } from 'vitest';
import {
  readSynonBiomedFileImportResponse,
  SynonBiomedFileImportError,
} from '@/renderer/services/synonBiomedFileImportError';

describe('Synon Biomed file import error contract', () => {
  it('preserves a closed backend reason without exposing its raw detail', async () => {
    const response = Response.json(
      { remoteKind: 'outside_roots', detail: '/private/user/path is outside roots' },
      { status: 400 }
    );

    await expect(readSynonBiomedFileImportResponse(response)).rejects.toMatchObject({
      name: 'SynonBiomedFileImportError',
      status: 400,
      kind: 'outside_roots',
      message: 'Synon Biomed file import failed (400) [outside_roots]',
    });
  });

  it.each([
    [403, 'permission'],
    [404, 'not_found'],
    [413, 'too_large'],
    [502, 'connection'],
  ] as const)('maps HTTP %d to %s when no typed reason is present', async (status, kind) => {
    await expect(readSynonBiomedFileImportResponse(new Response('upstream secret', { status }))).rejects.toEqual(
      new SynonBiomedFileImportError(status, kind)
    );
  });

  it('returns successful JSON for both local and cloud import callers', async () => {
    await expect(readSynonBiomedFileImportResponse(Response.json({ artifact_id: 'artifact-1' }))).resolves.toEqual({
      artifact_id: 'artifact-1',
    });
  });
});

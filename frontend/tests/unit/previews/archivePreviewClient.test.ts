/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { fetchArchiveListing } from '@/renderer/pages/conversation/Preview/components/viewers/archivePreviewClient';
import type { ArchivePreviewError } from '@/renderer/pages/conversation/Preview/components/viewers/archivePreviewClient';
import { afterEach, describe, expect, it, vi } from 'vitest';

const listing = {
  filename: 'binding_modes.zip',
  containers: [],
  entries: [
    { path: 'images', name: 'images', size: 0, directory: true, archive: false },
    { path: 'report.md', name: 'report.md', size: 42, directory: false, archive: false },
  ],
};

describe('fetchArchiveListing', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('recovers from a transient route restart without misreporting archive corruption', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response('route unavailable', { status: 404 }))
      .mockResolvedValueOnce(Response.json(listing));
    vi.stubGlobal('fetch', fetchMock);

    await expect(fetchArchiveListing('/artifact/archive', { retryDelays: [0] })).resolves.toEqual(listing);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it('normalizes an empty container path from an older backend during rolling activation', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(Response.json({ ...listing, containers: null })));

    await expect(fetchArchiveListing('/artifact/archive', { retryDelays: [] })).resolves.toEqual(listing);
  });

  it.each([
    [422, 'invalid'],
    [413, 'limit'],
    [403, 'unavailable'],
  ] as const)('maps HTTP %i to %s', async (status, reason) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('failed', { status })));

    await expect(fetchArchiveListing('/artifact/archive', { retryDelays: [] })).rejects.toMatchObject<
      Partial<ArchivePreviewError>
    >({ reason, status });
  });

  it('rejects malformed successful responses as unavailable service data', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(Response.json({ entries: 'not-an-array' })));

    await expect(fetchArchiveListing('/artifact/archive', { retryDelays: [] })).rejects.toMatchObject<
      Partial<ArchivePreviewError>
    >({ reason: 'unavailable' });
  });
});

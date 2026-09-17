import { beforeEach, describe, expect, it, vi } from 'vitest';

const frameReadMocks = vi.hoisted(() => ({ invalidate: vi.fn() }));

vi.mock('@/renderer/services/synonBiomedFrameReads', () => ({
  invalidateSynonBiomedFrameReads: frameReadMocks.invalidate,
}));

import { requestSynonBiomedFrameAudit } from '@/renderer/services/synonBiomedAnnotations';

describe('Synon Biomed manual review service', () => {
  beforeEach(() => frameReadMocks.invalidate.mockReset());

  it('starts a one-shot audit and invalidates the cached runtime projection', async () => {
    const fetchImpl = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          ok: true,
          frame_id: 'completion-review-1',
          root_frame_id: 'frame-1',
          status: 'processing',
        }),
        { status: 202, headers: { 'content-type': 'application/json' } }
      )
    );

    await expect(requestSynonBiomedFrameAudit('frame-1', fetchImpl)).resolves.toMatchObject({
      frame_id: 'completion-review-1',
      status: 'processing',
    });
    expect(fetchImpl).toHaveBeenCalledWith(
      '/api/frames/frame-1/audit',
      expect.objectContaining({ method: 'POST', body: '{}' })
    );
    expect(frameReadMocks.invalidate).toHaveBeenCalledWith('frame-1');
  });
});

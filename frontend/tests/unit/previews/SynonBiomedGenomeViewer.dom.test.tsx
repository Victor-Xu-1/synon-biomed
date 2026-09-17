import { fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

const igvMocks = vi.hoisted(() => ({
  search: vi.fn(async () => true),
  createBrowser: vi.fn(),
  removeBrowser: vi.fn(),
}));

vi.mock('igv', () => ({
  default: {
    createBrowser: igvMocks.createBrowser,
    removeBrowser: igvMocks.removeBrowser,
  },
}));

import SynonBiomedGenomeViewer from '@/renderer/pages/conversation/Preview/components/viewers/SynonBiomedGenomeViewer';

describe('SynonBiomedGenomeViewer', () => {
  it('loads an authenticated VCF URL into IGV and exposes SynonAI genome navigation', async () => {
    igvMocks.createBrowser.mockResolvedValue({ search: igvMocks.search });
    await renderWithI18n(
      <SynonBiomedGenomeViewer
        filename='variants.vcf'
        contentUrl='/api/artifacts/genome-fixture'
        initialLocus='chr1:999900-1000100'
      />,
      'en-US'
    );

    expect(await screen.findByRole('region', { name: 'Genome browser' })).toBeInTheDocument();
    await waitFor(() => expect(igvMocks.createBrowser).toHaveBeenCalledTimes(1));
    expect(igvMocks.createBrowser.mock.calls[0]?.[1]).toMatchObject({
      reference: {
        id: 'hg38',
        name: 'Human GRCh38 / hg38',
        format: 'chromsizes',
        fastaURL: '/genomes/ucsc/hg38.chrom.sizes',
      },
      loadDefaultGenomes: false,
      showSequence: false,
      locus: 'chr1:999900-1000100',
      tracks: [
        {
          name: 'variants.vcf',
          url: '/api/artifacts/genome-fixture',
          type: 'variant',
          format: 'vcf',
          indexed: false,
        },
      ],
    });
    expect(igvMocks.createBrowser.mock.calls[0]?.[1]).not.toHaveProperty('genome');
    expect(JSON.stringify(igvMocks.createBrowser.mock.calls[0]?.[1])).not.toMatch(/igv\.org|github\.com|ucsc\.edu/u);

    fireEvent.change(screen.getByRole('textbox', { name: 'Genome location' }), {
      target: { value: 'chr1:1000000-1000100' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Go to genome location' }));
    await waitFor(() => expect(igvMocks.search).toHaveBeenCalledWith('chr1:1000000-1000100'));
  });
});

import { fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

const seqVizMock = vi.hoisted(() => vi.fn());

vi.mock('seqviz', () => ({
  SeqViz: (props: Record<string, unknown>) => {
    seqVizMock(props);
    return <div data-testid='seqviz-canvas'>{String(props.name)}</div>;
  },
}));

import SynonBiomedSequenceViewer from '@/renderer/pages/conversation/Preview/components/viewers/SynonBiomedSequenceViewer';

const genBankFixture = `LOCUS       SYNON001                 120 bp    DNA     circular SYN 11-JUL-2026
DEFINITION  Synthetic Synon Biomed test plasmid.
ACCESSION   SYNON001
VERSION     SYNON001.1
FEATURES             Location/Qualifiers
     source          1..120
                     /organism="synthetic construct"
     promoter        1..20
                     /label="synon_promoter"
     CDS             21..90
                     /gene="synA"
                     /label="synA CDS"
ORIGIN
        1 atgcatgcat gcatgcatgc atgcatgcat gcatgcatgc atgcatgcat gcatgcatgc
       61 atgcatgcat gcatgcatgc atgcatgcat gcatgcatgc atgcatgcat gcatgcatgc
//`;

describe('SynonBiomedSequenceViewer', () => {
  it('renders a real parsed GenBank document and updates native SeqViz controls', async () => {
    await renderWithI18n(<SynonBiomedSequenceViewer filename='synon-test.gbk' content={genBankFixture} />, 'en-US');

    expect(await screen.findByRole('region', { name: 'Sequence preview' })).toBeInTheDocument();
    expect(await screen.findByText('120 bp · 2 annotations')).toBeInTheDocument();
    await waitFor(() => expect(seqVizMock).toHaveBeenCalled());
    expect(seqVizMock.mock.lastCall?.[0]).toMatchObject({
      name: 'SYNON001',
      viewer: 'both',
      seqType: 'dna',
      disableExternalFonts: true,
    });

    fireEvent.click(screen.getByRole('radio', { name: 'Linear' }));
    fireEvent.change(screen.getByRole('slider', { name: 'Sequence zoom' }), { target: { value: '75' } });
    await waitFor(() =>
      expect(seqVizMock.mock.lastCall?.[0]).toMatchObject({ viewer: 'linear', zoom: { linear: 75 } })
    );
  });

  it('renders bounded FASTQ quality statistics while visualizing the first read', async () => {
    await renderWithI18n(
      <SynonBiomedSequenceViewer filename='reads.fastq' content={'@read-001\nACGT\n+\nIIII\n@read-002\nAA\n+\n!!'} />,
      'en-US'
    );

    expect(await screen.findByText('2 reads · first 4 bp · Q0–Q40 (mean 26.67)')).toBeInTheDocument();
    await waitFor(() => expect(seqVizMock).toHaveBeenCalledWith(expect.objectContaining({ name: 'read-001' })));
  });
});

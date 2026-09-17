import { fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

vi.mock('@nightingale-elements/nightingale-msa', () => ({}));

import SynonBiomedMsaViewer from '@/renderer/pages/conversation/Preview/components/viewers/SynonBiomedMsaViewer';

describe('SynonBiomedMsaViewer', () => {
  it('renders parsed alignment data through the Nightingale custom element', async () => {
    const { container } = await renderWithI18n(
      <SynonBiomedMsaViewer filename='aligned.fasta' content={'>alpha\nAC-E\n>beta\nACFE'} />,
      'en-US'
    );

    expect(await screen.findByRole('region', { name: 'Multiple sequence alignment preview' })).toBeInTheDocument();
    expect(await screen.findByText('2 sequences · 4 positions')).toBeInTheDocument();

    await waitFor(() => expect(container.querySelector('nightingale-msa')).not.toBeNull());
    const msa = container.querySelector('nightingale-msa') as HTMLElement & {
      data?: Array<{ name: string; sequence: string }>;
    };
    expect(msa.getAttribute('color-scheme')).toBe('clustal2');
    expect(msa.data).toEqual([
      { name: 'alpha', sequence: 'AC-E' },
      { name: 'beta', sequence: 'ACFE' },
    ]);

    fireEvent.change(screen.getByRole('combobox', { name: 'Color scheme' }), { target: { value: 'nucleotide' } });
    expect(msa.getAttribute('color-scheme')).toBe('nucleotide');
  });
});

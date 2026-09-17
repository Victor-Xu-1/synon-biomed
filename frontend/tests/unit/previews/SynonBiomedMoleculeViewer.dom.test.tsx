import { fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

const { renderMoleculeSvg } = vi.hoisted(() => ({
  renderMoleculeSvg: vi.fn(async (smiles: string) => `<svg><text>${smiles}</text></svg>`),
}));
vi.mock('@/renderer/services/rdkitBrowser', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/renderer/services/rdkitBrowser')>()),
  renderMoleculeSvg,
}));

import SynonBiomedMoleculeViewer, {
  parseSmilesRecords,
} from '@/renderer/pages/conversation/Preview/components/viewers/SynonBiomedMoleculeViewer';
import { RDKitRenderError } from '@/renderer/services/rdkitBrowser';

describe('SynonBiomedMoleculeViewer', () => {
  beforeEach(() => {
    renderMoleculeSvg.mockReset();
    renderMoleculeSvg.mockImplementation(async (smiles: string) => `<svg><text>${smiles}</text></svg>`);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('parses, renders, and filters a real multi-row SMILES document through RDKit', async () => {
    expect(parseSmilesRecords('# comment\nCCO ethanol\nCCN', (index) => `Molecule ${index}`)).toEqual([
      { id: '0-CCO', smiles: 'CCO', name: 'ethanol' },
      { id: '1-CCN', smiles: 'CCN', name: 'Molecule 2' },
    ]);

    await renderWithI18n(
      <SynonBiomedMoleculeViewer filename='hits.smi' content={'CCO ethanol\nCCN ethylamine'} />,
      'en-US'
    );

    expect(await screen.findByRole('img', { name: 'Molecular structure of ethanol' })).toBeInTheDocument();
    expect(screen.getByRole('img', { name: 'Molecular structure of ethylamine' })).toBeInTheDocument();
    expect(renderMoleculeSvg).toHaveBeenNthCalledWith(1, 'CCO', 280, 190);
    expect(renderMoleculeSvg).toHaveBeenNthCalledWith(2, 'CCN', 280, 190);

    fireEvent.change(screen.getByRole('textbox', { name: 'Search molecules' }), { target: { value: 'ethylamine' } });
    await waitFor(() =>
      expect(screen.queryByRole('img', { name: 'Molecular structure of ethanol' })).not.toBeInTheDocument()
    );
    expect(screen.getByRole('img', { name: 'Molecular structure of ethylamine' })).toBeInTheDocument();
  });

  it('surfaces a bounded runtime initialization error instead of reporting infrastructure failure as invalid data', async () => {
    const warning = vi.spyOn(console, 'warn').mockImplementation(() => undefined);
    renderMoleculeSvg.mockRejectedValueOnce(
      new RDKitRenderError('runtime_unavailable', 'sensitive runtime implementation detail')
    );

    await renderWithI18n(<SynonBiomedMoleculeViewer filename='hits.smi' content='CCO ethanol' />, 'en-US');

    expect(await screen.findByText('Failed to initialize the molecule file viewer.')).toBeInTheDocument();
    expect(screen.queryByText('No drawable SMILES')).not.toBeInTheDocument();
    expect(warning).toHaveBeenCalledWith('[SynonBiomedMoleculeViewer] Failed to load molecules', {
      code: 'initialize-failed',
      errorName: 'ScientificPreviewError',
      status: undefined,
    });
  });
});

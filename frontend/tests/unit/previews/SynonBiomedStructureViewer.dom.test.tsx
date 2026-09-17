import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { gzipSync } from 'node:zlib';
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

const structureViewerCss = readFileSync(
  resolve(
    process.cwd(),
    'packages/desktop/src/renderer/pages/conversation/Preview/components/viewers/SynonBiomedStructureViewer.css'
  ),
  'utf8'
);

const molstarMocks = vi.hoisted(() => {
  const engine = {
    plugin: {},
    load: vi.fn(async () => ({
      objects: [],
      atomCount: 0,
      hasProtein: false,
      hasLigand: false,
    })),
    loadDockingEnsemble: vi.fn(async () => ({
      hasLigand: true,
      ligandCount: 24,
      residueCount: 8,
      distanceCount: 4,
    })),
    resize: vi.fn(),
    captureImage: vi.fn(async () => 'data:image/png;base64,MOLSTAR_CAPTURE'),
    resetCamera: vi.fn(),
    clickNativeControl: vi.fn(() => true),
    isNativeControlActive: vi.fn(() => true),
    applyRepresentationPreset: vi.fn(async () => undefined),
    applyRepresentationStyle: vi.fn(async () => undefined),
    applyPocketFocus: vi.fn(async () => ({
      hasLigand: true,
      ligandCount: 24,
      residueCount: 8,
      distanceCount: 4,
    })),
    setPocketInteractionStrengths: vi.fn(async () => undefined),
    applyDockingPocket: vi.fn(async () => ({
      hasLigand: true,
      ligandCount: 24,
      residueCount: 8,
      distanceCount: 4,
    })),
    clearDockingPocket: vi.fn(async () => ({
      hasLigand: true,
      ligandCount: 24,
      residueCount: 0,
      distanceCount: 0,
    })),
    replaceDockingComparison: vi.fn(async () => ({
      hasLigand: true,
      ligandCount: 48,
      residueCount: 10,
      distanceCount: 6,
    })),
    replaceDockingSelection: vi.fn(async () => ({
      hasLigand: true,
      ligandCount: 72,
      residueCount: 12,
      distanceCount: 8,
    })),
    clearDockingSelection: vi.fn(async () => ({
      hasLigand: false,
      ligandCount: 0,
      residueCount: 0,
      distanceCount: 0,
    })),
    cancelDockingRefinement: vi.fn(),
    setDockingProteinVisible: vi.fn(),
    setStructureObjectVisible: vi.fn(),
    setStructureLigandColor: vi.fn(async () => undefined),
    replaceDockingLayers: vi.fn(async () => ({
      hasLigand: true,
      ligandCount: 24,
      residueCount: 0,
      distanceCount: 0,
    })),
    clearPocketFocus: vi.fn(async () => undefined),
    getSelectedLigandResidueName: vi.fn(() => undefined as string | undefined),
    getTrajectoryModelState: vi.fn(() => ({ index: 0, count: 1 })),
    getStructureComposition: vi.fn(() => ({
      objects: [],
      atomCount: 0,
      hasProtein: false,
      hasLigand: false,
    })),
    getPrimaryLigandDepictionSource: vi.fn(
      () =>
        undefined as
          | {
              residueName: string;
              interactionResidueName: string;
              molBlock: string;
              complexPdb?: string;
              atomCount: number;
              hasProtein: boolean;
            }
          | undefined
    ),
    getElectrostaticInputSource: vi.fn(() => ({
      content: 'ATOM      1  N   ALA A   1       0.000   0.000   0.000  1.00  0.00           N\nEND\n',
      ligands: [{ key: 'LIG', molBlock: 'validated mol block', atomCount: 4 }],
      atomCount: 1,
    })),
    setElectrostaticPotentials: vi.fn(async () => undefined),
    advanceTrajectoryModel: vi.fn(async () => ({ index: 0, count: 1 })),
    exportCurrentPose: vi.fn(() => ({
      content: 'current pose structure',
      atomCount: 6,
      selectionOnly: false,
    })),
    setBackgroundColor: vi.fn(),
    dispose: vi.fn(),
  };
  const create = vi.fn(async (host: HTMLElement) => {
    const nativeUi = document.createElement('div');
    nativeUi.className = 'msp-plugin';
    nativeUi.dataset.testid = 'fake-molstar-native-ui';
    nativeUi.textContent = 'Reset Zoom Orient Axes Reset Axes Fullscreen';
    const controls = [
      { title: 'Select Animation' },
      { title: 'Reset Zoom' },
      { title: 'Screenshot / State Snapshot' },
      { title: 'Toggle Selection Mode' },
      { label: 'Fullscreen' },
    ];
    controls.forEach(({ title, label }) => {
      const button = document.createElement('button');
      button.type = 'button';
      if (title) button.title = title;
      if (label) button.textContent = label;
      nativeUi.appendChild(button);
    });
    host.appendChild(nativeUi);
    return engine;
  });
  return { create, engine };
});
vi.mock('@/renderer/pages/conversation/Preview/components/viewers/molstarStructureEngine', () => ({
  createMolstarStructureEngine: molstarMocks.create,
  isElectrostaticSurfaceRepresentation: (representation: string) =>
    ['surface', 'pocket-surface', 'ligand-surface'].includes(representation),
}));
const rdkitMocks = vi.hoisted(() => ({
  renderMoleculeSvg: vi.fn(
    async () =>
      "<svg><rect style='fill:#FFFFFF' width='184' height='126'/><path style='stroke:#000000' d='M0 0'/><path style='stroke:#FF0000' d='M1 1'/></svg>"
  ),
  validateAndRenderMolBlock: vi.fn(async () => ({
    svg: "<svg><rect style='fill:#FFFFFF' width='184' height='126'/><path style='stroke:#000000' d='M0 0'/></svg>",
    molBlock: 'validated mol block',
    smiles: 'C1=CC=CC=C1',
  })),
}));
vi.mock('@/renderer/services/rdkitBrowser', () => ({
  renderMoleculeSvg: rdkitMocks.renderMoleculeSvg,
  validateAndRenderMolBlock: rdkitMocks.validateAndRenderMolBlock,
}));
vi.mock('@/renderer/pages/conversation/Preview/components/viewers/interactionDiagramLigandSource', () => ({
  loadCurrentInteractionDiagramCandidateSmiles: vi.fn(async () => 'CO'),
}));
const electrostaticTransportMocks = vi.hoisted(() => {
  const dx = [
    'object 1 class gridpositions counts 2 2 2',
    'origin 0 0 0',
    'delta 1 0 0',
    'delta 0 1 0',
    'delta 0 0 1',
    'object 2 class gridconnections counts 2 2 2',
    'object 3 class array type double rank 0 items 8 data follows',
    '-1 0 1 2 3 4 5 6',
  ].join('\n');
  return {
    dx,
    decode: vi.fn(async () => ({ protein: dx, ligand: dx })),
  };
});
vi.mock(
  '@/renderer/pages/conversation/Preview/components/viewers/structureElectrostaticMap',
  async (importOriginal) => {
    const actual =
      await importOriginal<
        typeof import('@/renderer/pages/conversation/Preview/components/viewers/structureElectrostaticMap')
      >();
    return {
      ...actual,
      decodeStructureElectrostaticMaps: electrostaticTransportMocks.decode,
    };
  }
);

import SynonBiomedStructureViewer, {
  prepareLigandDepictionSvg,
  resolveLigandDepictionViewBox,
  resolvePdbLigandResidueName,
} from '@/renderer/pages/conversation/Preview/components/viewers/SynonBiomedStructureViewer';

const electrostaticDX = electrostaticTransportMocks.dx;

const electrostaticResponse = () => ({
  ok: true,
  status: 'completed',
  encoding: 'gzip+base64',
  potential_maps: {
    protein: { dx_gzip_base64: gzipSync(electrostaticDX).toString('base64') },
    ligand: { dx_gzip_base64: gzipSync(electrostaticDX).toString('base64') },
  },
  report: {
    contract: true,
    contract_version: '2.0',
    ok: true,
    engine: 'APBS',
    calculation_mode: 'separate-components',
    grid_alignment: 'shared-frame',
    engine_version: '3.4.1',
    preparation_engine: 'PDB2PQR + RDKit',
    preparation_engine_version: 'pdb2pqr 3.7.1; RDKit 2024.3.5',
    protein_charge_method: 'PDB2PQR AMBER with PROPKA protonation',
    ligand_charge_method: 'RDKit Gasteiger with explicit hydrogens',
    force_field: 'AMBER',
    ph: 7.4,
    ionic_strength_molar: 0.15,
    potential_unit: 'kT/e',
    color_range: [-5, 5],
    input_sha256: 'a'.repeat(64),
    protein_atom_count: 8,
    ligand_atom_count: 4,
    total_atom_count: 12,
    mesh_spacing_angstrom: 0.65,
    grids: {
      protein: {
        counts: [2, 2, 2],
        origin: [0, 0, 0],
        delta: [1, 1, 1],
        value_count: 8,
        minimum: -1,
        maximum: 6,
      },
      ligand: {
        counts: [2, 2, 2],
        origin: [0, 0, 0],
        delta: [1, 1, 1],
        value_count: 8,
        minimum: -1,
        maximum: 6,
      },
    },
    warnings: [],
  },
  runtime: {},
});

const batchElectrostaticResponse = (keys: readonly string[], warnings: readonly string[] = []) => {
  const value = electrostaticResponse();
  delete value.potential_maps.ligand;
  delete value.report.grids.ligand;
  const ligandGrid = {
    counts: [2, 2, 2],
    origin: [0, 0, 0],
    delta: [1, 1, 1],
    value_count: 8,
    minimum: -1,
    maximum: 6,
  };
  return {
    ...value,
    potential_maps: {
      ...value.potential_maps,
      ligands: Object.fromEntries(
        keys.map((key) => [key, { dx_gzip_base64: gzipSync(electrostaticDX).toString('base64') }])
      ),
    },
    report: {
      ...value.report,
      ligand_atom_count: keys.length * 4,
      total_atom_count: value.report.protein_atom_count + keys.length * 4,
      ligand_atom_counts: Object.fromEntries(keys.map((key) => [key, 4])),
      grids: {
        ...value.report.grids,
        ligands: Object.fromEntries(keys.map((key) => [key, { ...ligandGrid }])),
      },
      warnings: [...warnings],
    },
  };
};

let toolbarResizeHeight: number | null = null;

const expandCompoundListFromRight = async () => {
  const expandButton = await screen.findByRole('button', {
    name: 'Expand the compound list from the right',
  });
  expect(expandButton).toHaveAttribute('aria-expanded', 'false');
  fireEvent.click(expandButton);
};

describe('SynonBiomedStructureViewer', () => {
  it('keeps the electrostatic scale as one top-anchored content-height overlay', () => {
    const rules = structureViewerCss.match(/\.synon-biomed-molstar__electrostatic-legend\s*\{[^}]*\}/g) ?? [];
    expect(rules).toHaveLength(1);
    expect(rules[0]).toContain('top: 0;');
    expect(rules[0]).not.toContain('bottom:');
    expect(rules[0]).not.toContain('height: 100%');
    const warningRule = structureViewerCss.match(
      /\.synon-biomed-molstar__electrostatic-legend-scale > small\[role='status'\]\s*\{[^}]*\}/
    )?.[0];
    expect(warningRule).toContain('overflow-wrap: anywhere;');
    expect(warningRule).toContain('white-space: normal;');
  });

  it('keeps element colors while adapting ligand carbon bonds to the canvas theme', () => {
    const source =
      "<svg viewBox='0 0 276 189'><rect style='fill:#FFFFFF'/><path style='stroke:#000000'/><path style='stroke:#FF0000'/></svg>";
    expect(prepareLigandDepictionSvg(source, 'dark')).toContain('stroke:#FFFFFF');
    expect(prepareLigandDepictionSvg(source, 'dark')).toContain('stroke:#FF0000');
    expect(prepareLigandDepictionSvg(source, 'dark')).not.toContain('<rect');
    expect(prepareLigandDepictionSvg(source, 'dark')).toContain("viewBox='-16.560 -11.340 309.120 211.680'");
    expect(prepareLigandDepictionSvg(source, 'white')).toContain('stroke:#000000');
  });

  it('fits the mounted 2D ligand to its real graphical bounds with extra vertical room', () => {
    expect(resolveLigandDepictionViewBox({ x: 10, y: 20, width: 200, height: 100 })).toBe(
      '-6.000 8.000 232.000 124.000'
    );
    expect(resolveLigandDepictionViewBox({ x: 0, y: 0, width: 0, height: 100 })).toBeNull();
  });
  beforeEach(() => {
    vi.clearAllMocks();
    const pocketSummary = { hasLigand: true, ligandCount: 24, residueCount: 8, distanceCount: 4 };
    molstarMocks.engine.load.mockReset().mockResolvedValue({
      objects: [],
      atomCount: 0,
      hasProtein: false,
      hasLigand: false,
    });
    molstarMocks.engine.loadDockingEnsemble.mockReset().mockResolvedValue(pocketSummary);
    molstarMocks.engine.applyRepresentationPreset.mockReset().mockResolvedValue(undefined);
    molstarMocks.engine.applyRepresentationStyle.mockReset().mockResolvedValue(undefined);
    molstarMocks.engine.applyPocketFocus.mockReset().mockResolvedValue(pocketSummary);
    molstarMocks.engine.setPocketInteractionStrengths.mockReset().mockResolvedValue(undefined);
    molstarMocks.engine.applyDockingPocket.mockReset().mockResolvedValue(pocketSummary);
    molstarMocks.engine.clearDockingPocket.mockReset().mockResolvedValue({
      ...pocketSummary,
      residueCount: 0,
      distanceCount: 0,
    });
    molstarMocks.engine.replaceDockingComparison.mockReset().mockResolvedValue({
      ...pocketSummary,
      ligandCount: 48,
      residueCount: 10,
      distanceCount: 6,
    });
    molstarMocks.engine.replaceDockingSelection.mockReset().mockResolvedValue({
      ...pocketSummary,
      ligandCount: 72,
      residueCount: 12,
      distanceCount: 8,
    });
    molstarMocks.engine.clearDockingSelection.mockReset().mockResolvedValue({
      hasLigand: false,
      ligandCount: 0,
      residueCount: 0,
      distanceCount: 0,
    });
    molstarMocks.engine.replaceDockingLayers.mockReset().mockResolvedValue({
      ...pocketSummary,
      residueCount: 0,
      distanceCount: 0,
    });
    molstarMocks.engine.clearPocketFocus.mockReset().mockResolvedValue(undefined);
    molstarMocks.engine.setStructureLigandColor.mockReset().mockResolvedValue(undefined);
    molstarMocks.engine.setElectrostaticPotentials.mockReset().mockResolvedValue(undefined);
    electrostaticTransportMocks.decode.mockReset();
    electrostaticTransportMocks.decode.mockResolvedValue({
      protein: electrostaticTransportMocks.dx,
      ligand: electrostaticTransportMocks.dx,
    });
    molstarMocks.engine.getSelectedLigandResidueName.mockReturnValue(undefined);
    molstarMocks.engine.getPrimaryLigandDepictionSource.mockReturnValue(undefined);
    molstarMocks.engine.getElectrostaticInputSource.mockReset();
    molstarMocks.engine.getElectrostaticInputSource.mockReturnValue({
      content: 'ATOM      1  N   ALA A   1       0.000   0.000   0.000  1.00  0.00           N\nEND\n',
      ligands: [{ key: 'LIG', molBlock: 'validated mol block', atomCount: 4 }],
      atomCount: 1,
    });
    toolbarResizeHeight = null;
    vi.stubGlobal(
      'ResizeObserver',
      class {
        constructor(private readonly callback: ResizeObserverCallback) {}
        observe(target: Element) {
          if (toolbarResizeHeight !== null && target.getAttribute('data-testid') === 'synon-biomed-molstar-stage') {
            this.callback(
              [
                {
                  target,
                  contentRect: { height: toolbarResizeHeight },
                } as unknown as ResizeObserverEntry,
              ],
              this as unknown as ResizeObserver
            );
          }
        }
        disconnect() {}
      }
    );
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) =>
        String(input).includes('/structure-electrostatic-map')
          ? new Response(JSON.stringify(electrostaticResponse()), {
              status: 200,
              headers: { 'Content-Type': 'application/json' },
            })
          : new Response('HEADER TEST\nATOM      1  N   MET A   1', {
              status: 200,
            })
      )
    );
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it('opens a publication SVG generated by the independent backend interaction engine', async () => {
    const ensemble = [
      'REMARK 900 SYNON BIOMED DOCKING COMPLEX ENSEMBLE',
      'REMARK 900 REFERENCE LIGAND REF SOURCE G7I CHAIN A RESIDUE 201',
      'ATOM      1 CA   GLY A  16      27.817 -18.400  -4.985  1.00 46.96           C',
      'HETATM    6 C1   REF A 201      29.000 -24.000   0.000  1.00  0.00           C',
      'REMARK 900 DOCKED LIGAND D01 CANDIDATE MDM2-012 RANK 1 POSE 1 AFFINITY -10.026 KCAL/MOL',
      'HETATM    2 C1   D01 Z 101      31.000 -28.000   2.000  1.00  0.00           C',
      'HETATM    3 O1   D01 Z 101      32.000 -28.000   2.000  1.00  0.00           O',
      'REMARK 900 DOCKED LIGAND D02 CANDIDATE MDM2-025 RANK 2 POSE 1 AFFINITY -9.876 KCAL/MOL',
      'HETATM    4 C1   D02 Z 102      32.000 -27.000   1.000  1.00  0.00           C',
      'HETATM    5 O1   D02 Z 102      33.000 -27.000   1.000  1.00  0.00           O',
      'END',
    ].join('\n');
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input) === '/api/artifacts/candidate.smi') {
        return new Response('candidate_id\tcanonical_smiles\nMDM2-012\tCO\n', {
          status: 200,
        });
      }
      const requestBody = init?.body ? JSON.parse(String(init.body)) : {};
      return new Response(
        JSON.stringify({
          ok: true,
          status: 'completed',
          ...(requestBody.report_only
            ? {}
            : {
                svg: '<svg xmlns="http://www.w3.org/2000/svg"><text>Synon interaction diagram</text></svg>',
                png_base64: 'iVBORw0KGgo=',
              }),
          report: {
            engine: 'Synon 2D Interaction Engine',
            engine_release: '1.0.0',
            ligand_label: 'MDM2-012',
            pose_label: 'Pose within candidate 1 · -10.026 kcal/mol',
            hydrogen_bond_count: 1,
            salt_bridge_count: 0,
            width: 1710,
            height: 2400,
            png_dpi: 300,
            interactions: [
              {
                kind: 'hydrogen-bond',
                strength_index: 0.82,
                strength_level: 'strong',
                ligand_position: [31, -28, 2],
                protein_position: [27.817, -18.4, -4.985],
                residue: { name: 'GLY', number: 16, chain: 'A' },
              },
            ],
          },
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } }
      );
    });
    vi.stubGlobal('fetch', fetchMock);
    await renderWithI18n(
      <SynonBiomedStructureViewer
        filename='docking_complex_ensemble.pdb'
        content={ensemble}
        rootFrameId='frame-1'
        companionArtifactUrls={{
          'final_candidates.smi': '/api/artifacts/candidate.smi',
        }}
      />,
      'en-US'
    );
    const toggle = await screen.findByRole('button', {
      name: '2D interactions',
    });
    await waitFor(() => expect(toggle).toBeEnabled());
    await waitFor(() =>
      expect(molstarMocks.engine.setPocketInteractionStrengths).toHaveBeenCalledWith(
        [
          expect.objectContaining({
            ligand_label: 'MDM2-012',
            strength_index: 0.82,
          }),
        ],
        expect.any(Object)
      )
    );
    const strengthCalls = fetchMock.mock.calls.filter(([input]) =>
      String(input).includes('/structure-interaction-diagram')
    );
    expect(strengthCalls).toHaveLength(1);
    expect(JSON.parse(String(strengthCalls[0]?.[1]?.body))).toMatchObject({
      ligand_residue_name: 'D01',
      smiles: 'CO',
      ligand_label: 'MDM2-012',
      report_only: true,
    });

    fireEvent.click(toggle);

    const panel = await screen.findByTestId('synon-biomed-molstar-interaction-diagram');
    await waitFor(() =>
      expect(within(panel).getByRole('img')).toHaveAttribute('src', expect.stringMatching(/^data:image\/svg\+xml/))
    );
    expect(within(panel).getByText(/Synon 2D Interaction Engine 1.0.0/)).toBeInTheDocument();
    expect(
      within(panel).getByRole('button', {
        name: 'Download publication diagram as SVG',
      })
    ).toBeEnabled();
    expect(
      within(panel).getByRole('button', {
        name: 'Download publication diagram as 300 DPI PNG',
      })
    ).toBeEnabled();
    fireEvent.click(within(panel).getByRole('button', { name: 'Expand interaction diagram' }));
    const expandedPanel = await screen.findByTestId('synon-biomed-molstar-interaction-diagram');
    expect(expandedPanel).not.toBe(panel);
    expect(panel).not.toBeInTheDocument();
    expect(expandedPanel).toHaveClass('synon-biomed-molstar__interaction-panel--expanded');
    expect(
      within(expandedPanel).getByRole('button', {
        name: 'Restore interaction diagram',
      })
    ).toHaveAttribute('aria-pressed', 'true');
    const endpointCalls = fetchMock.mock.calls.filter(([input]) =>
      String(input).includes('/structure-interaction-diagram')
    );
    expect(endpointCalls).toHaveLength(2);
    const fullCalls = endpointCalls.filter(([, init]) => !JSON.parse(String(init?.body)).report_only);
    expect(fullCalls).toHaveLength(1);
    expect(fullCalls[0]?.[0]).toBe('/api/frames/frame-1/structure-interaction-diagram');
    expect(JSON.parse(String(fullCalls[0]?.[1]?.body))).toMatchObject({
      ligand_residue_name: 'D01',
      smiles: 'CO',
      ligand_label: 'MDM2-012',
    });

    fireEvent.click(toggle);
    await waitFor(() =>
      expect(screen.queryByTestId('synon-biomed-molstar-interaction-diagram')).not.toBeInTheDocument()
    );
    fireEvent.click(toggle);
    await screen.findByTestId('synon-biomed-molstar-interaction-diagram');
    expect(
      fetchMock.mock.calls.filter(([input]) => String(input).includes('/structure-interaction-diagram'))
    ).toHaveLength(2);
  });

  it('loads the artifact into a v1.1-style single Mol* viewer', async () => {
    const { unmount } = await renderWithI18n(
      <SynonBiomedStructureViewer filename='complex.pdb' contentUrl='/api/artifacts/structure-1' />,
      'en-US'
    );

    expect(screen.getByRole('region', { name: '3D structure preview' })).toBeInTheDocument();
    await waitFor(() =>
      expect(molstarMocks.create).toHaveBeenCalledWith(
        expect.any(HTMLElement),
        expect.objectContaining({
          mode: 'viewport',
          onSelectionChange: expect.any(Function),
        })
      )
    );
    expect(fetch).toHaveBeenCalledWith(
      '/api/artifacts/structure-1',
      expect.objectContaining({ headers: { accept: 'text/plain, chemical/*' } })
    );
    await waitFor(() =>
      expect(molstarMocks.engine.load).toHaveBeenCalledWith(expect.stringContaining('ATOM'), 'complex.pdb', 'pdb')
    );
    expect(screen.getByTestId('fake-molstar-native-ui')).toHaveTextContent('Reset Zoom');
    expect(screen.getByTestId('fake-molstar-native-ui')).not.toHaveTextContent('Measurements');
    expect(screen.queryByText('PDB Structure · Mol*')).not.toBeInTheDocument();
    expect(screen.queryByText('Read-only structure preview')).not.toBeInTheDocument();
    expect(screen.getByText('Drag to rotate · Wheel to zoom · Shift + drag to pan')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Views & interactions' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Ligand 3D editor' })).not.toBeInTheDocument();

    unmount();
    expect(molstarMocks.engine.dispose).toHaveBeenCalledTimes(1);
  });

  it('keeps binary BCIF data intact and routes it through Mol* mmCIF parsing', async () => {
    const binary = new Uint8Array([0x83, 0xa7, 0x76, 0x65, 0x72, 0x73, 0x69, 0x6f, 0x6e]).buffer;
    const arrayBufferMock = vi.fn(async () => binary);
    const textMock = vi.fn(async () => 'corrupted text');
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => ({
        arrayBuffer: arrayBufferMock,
        headers: new Headers({ 'content-length': String(binary.byteLength) }),
        ok: true,
        status: 200,
        text: textMock,
      }))
    );

    await renderWithI18n(
      <SynonBiomedStructureViewer filename='assembly.bcif' contentUrl='/api/artifacts/assembly' />,
      'en-US'
    );

    await waitFor(() => expect(molstarMocks.engine.load).toHaveBeenCalledWith(binary, 'assembly.bcif', 'mmcif'));
    expect(screen.queryByText('BCIF Structure · Mol*')).not.toBeInTheDocument();
    expect(fetch).toHaveBeenCalledWith(
      '/api/artifacts/assembly',
      expect.objectContaining({
        headers: { accept: 'application/octet-stream, chemical/*' },
      })
    );
    expect(arrayBufferMock).toHaveBeenCalledTimes(1);
    expect(textMock).not.toHaveBeenCalled();
  });

  it('lists and toggles real docking poses while keeping pocket state synchronized', async () => {
    const ensemble = [
      'REMARK 900 SYNON BIOMED DOCKING COMPLEX ENSEMBLE',
      'ATOM      1 CA   GLY A  16      27.817 -18.400  -4.985  1.00 46.96           C',
      'REMARK 900 REFERENCE LIGAND REF SOURCE G7I CHAIN A RESIDUE 201',
      'HETATM    2 C1   REF Z   1      30.588 -29.829   3.037  1.00 18.75           C',
      'REMARK 900 DOCKED LIGAND D01 CANDIDATE MDM2-012 RANK 1 AFFINITY -10.026 KCAL/MOL',
      'HETATM    3 C1   D01 Z 101      31.000 -28.000   2.000  1.00  0.00           C',
      'REMARK 900 DOCKED LIGAND D02 CANDIDATE MDM2-025 RANK 2 AFFINITY -9.876 KCAL/MOL',
      'HETATM    4 C1   D02 Z 102      32.000 -27.000   1.000  1.00  0.00           C',
      'END',
    ].join('\n');

    await renderWithI18n(
      <SynonBiomedStructureViewer
        filename='docking_complex_ensemble.pdb'
        content={ensemble}
        companionArtifactUrls={{
          'final_candidates.smi': '/api/artifacts/candidates/versions/latest',
        }}
      />,
      'en-US'
    );

    const card = await screen.findByTestId('synon-biomed-docking-score-card');
    const hostShell = screen.getByTestId('synon-biomed-structure-canvas').parentElement;
    expect(card).toHaveAttribute('data-collapsed', 'true');
    expect(card).toHaveStyle({ width: '28px' });
    expect(hostShell).toHaveStyle({ paddingRight: '0px' });
    expect(screen.queryByTestId('synon-biomed-docking-compound-list')).not.toBeInTheDocument();
    await expandCompoundListFromRight();
    const list = await screen.findByTestId('synon-biomed-docking-compound-list');
    expect(card).not.toHaveAttribute('data-collapsed');
    expect(card).toHaveStyle({ width: '180px' });
    expect(hostShell).toHaveStyle({ paddingRight: '180px' });
    const rankOne = within(list).getByRole('button', {
      name: 'Select #1 MDM2-012 · Pose within candidate 1/1 · -10.026 kcal/mol',
    });
    const rankTwo = within(list).getByRole('button', {
      name: 'Select #2 MDM2-025 · Pose within candidate 1/1 · -9.876 kcal/mol',
    });
    const reference = within(list).getByRole('button', {
      name: 'Select G7I · Reference co-crystal ligand',
    });
    expect(rankOne).toHaveAttribute('aria-pressed', 'true');
    expect(rankOne).toBeEnabled();
    expect(card).toHaveTextContent('-10.026');
    expect(molstarMocks.engine.loadDockingEnsemble).toHaveBeenCalledWith(
      ensemble,
      'docking_complex_ensemble.pdb',
      'pdb',
      'D01',
      expect.objectContaining({
        ligandColor: 0x0f766e,
        interactionVisibility: {
          ionic: false,
          'pi-stacking': true,
          'cation-pi': false,
          'halogen-bonds': false,
          'hydrogen-bonds': true,
          'weak-hydrogen-bonds': false,
          hydrophobic: false,
          'metal-coordination': false,
          'water-bridges': false,
        },
      })
    );
    expect(molstarMocks.engine.load).not.toHaveBeenCalled();
    expect(screen.getByRole('button', { name: 'Pocket' })).toHaveAttribute('data-active', 'true');
    const interactionLegend = screen.getByTestId('synon-biomed-pocket-interaction-legend');
    expect(interactionLegend).toHaveAttribute('data-collapsed', 'true');
    expect(within(interactionLegend).queryByText('Hydrogen bond')).not.toBeInTheDocument();
    const expandInteractionLegend = within(interactionLegend).getByRole('button', {
      name: 'Expand the interaction legend',
    });
    expect(expandInteractionLegend).toHaveAttribute('aria-expanded', 'false');
    fireEvent.click(expandInteractionLegend);
    expect(interactionLegend).not.toHaveAttribute('data-collapsed');
    expect(interactionLegend).toHaveTextContent('Hydrogen bond');
    expect(interactionLegend).toHaveTextContent('Salt bridge / ionic');
    expect(interactionLegend).toHaveTextContent('π–π stacking');
    expect(interactionLegend).toHaveTextContent('Hydrophobic / van der Waals');
    expect(
      within(interactionLegend).getByRole('button', {
        name: 'Hydrogen bond ON',
      })
    ).toHaveAttribute('aria-pressed', 'true');
    expect(within(interactionLegend).getByRole('button', { name: 'π–π stacking ON' })).toHaveAttribute(
      'aria-pressed',
      'true'
    );
    expect(within(interactionLegend).getByRole('button', { name: 'Cation–π OFF' })).toHaveAttribute(
      'aria-pressed',
      'false'
    );
    expect(
      within(interactionLegend).getByRole('button', {
        name: 'Salt bridge / ionic OFF',
      })
    ).toHaveAttribute('aria-pressed', 'false');
    fireEvent.click(
      within(interactionLegend).getByRole('button', {
        name: 'Collapse the interaction legend',
      })
    );
    expect(interactionLegend).toHaveAttribute('data-collapsed', 'true');
    expect(screen.getByRole('button', { name: 'Initial' })).not.toHaveAttribute('data-active', 'true');

    fireEvent.click(rankTwo);
    await waitFor(() =>
      expect(molstarMocks.engine.replaceDockingComparison).toHaveBeenLastCalledWith(
        'D01',
        'D02',
        expect.objectContaining({ focusCamera: false })
      )
    );
    expect(rankOne).toHaveAttribute('aria-pressed', 'true');
    expect(rankTwo).toHaveAttribute('aria-pressed', 'true');

    fireEvent.click(rankOne);
    await waitFor(() =>
      expect(molstarMocks.engine.applyDockingPocket).toHaveBeenLastCalledWith(
        'D02',
        expect.objectContaining({ focusCamera: false })
      )
    );
    expect(rankOne).toHaveAttribute('aria-pressed', 'false');
    expect(rankTwo).toHaveAttribute('aria-pressed', 'true');

    fireEvent.click(reference);
    await waitFor(() =>
      expect(molstarMocks.engine.replaceDockingComparison).toHaveBeenLastCalledWith(
        'D02',
        'REF',
        expect.objectContaining({ focusCamera: false })
      )
    );
    fireEvent.click(rankTwo);
    await waitFor(() =>
      expect(molstarMocks.engine.applyDockingPocket).toHaveBeenLastCalledWith(
        'REF',
        expect.objectContaining({ focusCamera: false })
      )
    );

    fireEvent.click(reference);
    await waitFor(() => expect(molstarMocks.engine.clearDockingSelection).toHaveBeenCalledOnce());
    expect(reference).toHaveAttribute('aria-pressed', 'false');
    fireEvent.click(reference);
    await waitFor(() => expect(reference).toHaveAttribute('aria-pressed', 'true'));
    await waitFor(() =>
      expect(molstarMocks.engine.applyDockingPocket).toHaveBeenLastCalledWith(
        'REF',
        expect.objectContaining({ focusCamera: false })
      )
    );

    fireEvent.click(screen.getByRole('button', { name: 'Pocket' }));
    await waitFor(() => expect(molstarMocks.engine.clearDockingPocket).toHaveBeenLastCalledWith(['REF']));
    expect(screen.getByRole('button', { name: 'Pocket' })).not.toHaveAttribute('data-active', 'true');
    expect(screen.queryByTestId('synon-biomed-pocket-interaction-legend')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Initial' })).toHaveAttribute('data-active', 'true');

    fireEvent.click(screen.getByRole('button', { name: 'Pocket' }));
    await waitFor(() =>
      expect(molstarMocks.engine.applyDockingPocket).toHaveBeenLastCalledWith(
        'REF',
        expect.objectContaining({ focusCamera: true })
      )
    );
    expect(screen.getByRole('button', { name: 'Pocket' })).toHaveAttribute('data-active', 'true');
    expect(screen.getByTestId('synon-biomed-pocket-interaction-legend')).toHaveAttribute('data-collapsed', 'true');
  });

  it('opens a docking ensemble in pocket mode and restores a full-protein panorama from Initial', async () => {
    const ensemble = [
      'REMARK 900 SYNON BIOMED DOCKING COMPLEX ENSEMBLE',
      'ATOM      1 CA   GLY A  16      27.817 -18.400  -4.985  1.00 46.96           C',
      'REMARK 900 REFERENCE LIGAND REF SOURCE G7I CHAIN A RESIDUE 201',
      'HETATM    2 C1   REF Z   1      30.588 -29.829   3.037  1.00 18.75           C',
      'REMARK 900 DOCKED LIGAND D01 CANDIDATE MDM2-012 RANK 1 AFFINITY -10.026 KCAL/MOL',
      'HETATM    3 C1   D01 Z 101      31.000 -28.000   2.000  1.00  0.00           C',
      'REMARK 900 DOCKED LIGAND D02 CANDIDATE MDM2-025 RANK 2 AFFINITY -9.876 KCAL/MOL',
      'HETATM    4 C1   D02 Z 102      32.000 -27.000   1.000  1.00  0.00           C',
      'END',
    ].join('\n');

    await renderWithI18n(
      <SynonBiomedStructureViewer
        filename='docking_complex_ensemble.pdb'
        content={ensemble}
        conversationId='frame-electrostatic-docking'
      />,
      'en-US'
    );

    const pocket = await screen.findByRole('button', { name: 'Pocket' });
    const initial = screen.getByRole('button', { name: 'Initial' });
    await waitFor(() => expect(pocket).toHaveAttribute('data-active', 'true'));
    expect(initial).not.toHaveAttribute('data-active', 'true');

    fireEvent.click(initial);

    await waitFor(() =>
      expect(molstarMocks.engine.replaceDockingLayers).toHaveBeenLastCalledWith(['D01'], [], expect.any(Array))
    );
    expect(molstarMocks.engine.resetCamera).toHaveBeenCalledTimes(1);
    expect(initial).toHaveAttribute('data-active', 'true');
    expect(pocket).not.toHaveAttribute('data-active', 'true');
  });

  it('publishes the clicked ligand immediately while serializing the underlying structure update', async () => {
    const ensemble = [
      'REMARK 900 SYNON BIOMED DOCKING COMPLEX ENSEMBLE',
      'ATOM      1 CA   GLY A  16      27.817 -18.400  -4.985  1.00 46.96           C',
      'REMARK 900 REFERENCE LIGAND REF SOURCE G7I CHAIN A RESIDUE 201',
      'HETATM    2 C1   REF Z   1      30.588 -29.829   3.037  1.00 18.75           C',
      'REMARK 900 DOCKED LIGAND D01 CANDIDATE MDM2-012 RANK 1 AFFINITY -10.026 KCAL/MOL',
      'HETATM    3 C1   D01 Z 101      31.000 -28.000   2.000  1.00  0.00           C',
      'REMARK 900 DOCKED LIGAND D02 CANDIDATE MDM2-025 RANK 2 AFFINITY -9.876 KCAL/MOL',
      'HETATM    4 C1   D02 Z 102      32.000 -27.000   1.000  1.00  0.00           C',
      'END',
    ].join('\n');
    let finishProjection!: (value: {
      hasLigand: boolean;
      ligandCount: number;
      residueCount: number;
      distanceCount: number;
    }) => void;
    await renderWithI18n(
      <SynonBiomedStructureViewer
        filename='docking_complex_ensemble.pdb'
        content={ensemble}
        conversationId='frame-electrostatic-layers'
      />,
      'en-US'
    );

    await expandCompoundListFromRight();
    const list = await screen.findByTestId('synon-biomed-docking-compound-list');
    const rankOne = within(list).getByRole('button', {
      name: 'Select #1 MDM2-012 · Pose within candidate 1/1 · -10.026 kcal/mol',
    });
    const rankTwo = within(list).getByRole('button', {
      name: 'Select #2 MDM2-025 · Pose within candidate 1/1 · -9.876 kcal/mol',
    });
    const reference = within(list).getByRole('button', {
      name: 'Select G7I · Reference co-crystal ligand',
    });

    fireEvent.click(screen.getByRole('button', { name: 'Initial' }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Pocket' })).not.toHaveAttribute('data-active'));

    molstarMocks.engine.replaceDockingLayers.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finishProjection = resolve;
        })
    );

    await act(async () => {
      fireEvent.click(rankTwo);
      await Promise.resolve();
    });
    await waitFor(() =>
      expect(molstarMocks.engine.replaceDockingLayers).toHaveBeenLastCalledWith(['D01', 'D02'], [], expect.any(Array))
    );
    await act(async () => {
      fireEvent.click(reference);
      await Promise.resolve();
    });

    expect(rankOne).toHaveAttribute('aria-pressed', 'true');
    expect(rankTwo).toHaveAttribute('aria-pressed', 'true');
    expect(reference).toHaveAttribute('aria-pressed', 'true');
    expect(screen.queryByTestId('synon-biomed-docking-pose-loading')).not.toBeInTheDocument();
    expect(molstarMocks.engine.replaceDockingLayers).toHaveBeenCalledTimes(2);
    expect(molstarMocks.engine.cancelDockingRefinement).toHaveBeenCalledTimes(2);
    await act(async () => {
      finishProjection({
        hasLigand: true,
        ligandCount: 24,
        residueCount: 8,
        distanceCount: 4,
      });
    });
    expect(rankTwo).toHaveAttribute('aria-pressed', 'true');
    await waitFor(() =>
      expect(molstarMocks.engine.replaceDockingLayers).toHaveBeenLastCalledWith(
        ['D01', 'D02', 'REF'],
        [],
        expect.any(Array)
      )
    );
    expect(molstarMocks.engine.replaceDockingLayers).toHaveBeenCalledTimes(3);
  });

  it('shows the new comparison selection immediately while pocket refinement finishes', async () => {
    const ensemble = [
      'REMARK 900 SYNON BIOMED DOCKING COMPLEX ENSEMBLE',
      'ATOM      1 CA   GLY A  16      27.817 -18.400  -4.985  1.00 46.96           C',
      'REMARK 900 REFERENCE LIGAND REF SOURCE G7I CHAIN A RESIDUE 201',
      'HETATM    2 C1   REF Z   1      30.588 -29.829   3.037  1.00 18.75           C',
      'REMARK 900 DOCKED LIGAND D01 CANDIDATE MDM2-012 RANK 1 AFFINITY -10.026 KCAL/MOL',
      'HETATM    3 C1   D01 Z 101      31.000 -28.000   2.000  1.00  0.00           C',
      'REMARK 900 DOCKED LIGAND D02 CANDIDATE MDM2-025 RANK 2 AFFINITY -9.876 KCAL/MOL',
      'HETATM    4 C1   D02 Z 102      32.000 -27.000   1.000  1.00  0.00           C',
      'END',
    ].join('\n');
    let finishPocket!: (value: {
      hasLigand: boolean;
      ligandCount: number;
      residueCount: number;
      distanceCount: number;
    }) => void;

    await renderWithI18n(
      <SynonBiomedStructureViewer filename='docking_complex_ensemble.pdb' content={ensemble} />,
      'en-US'
    );
    await expandCompoundListFromRight();
    const list = await screen.findByTestId('synon-biomed-docking-compound-list');
    const pocket = screen.getByRole('button', { name: 'Pocket' });
    await waitFor(() => expect(pocket).toHaveAttribute('data-active', 'true'));

    molstarMocks.engine.replaceDockingComparison.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finishPocket = resolve;
        })
    );
    fireEvent.click(
      within(list).getByRole('button', {
        name: 'Select #2 MDM2-025 · Pose within candidate 1/1 · -9.876 kcal/mol',
      })
    );

    expect(
      within(list).getByRole('button', {
        name: 'Select #1 MDM2-012 · Pose within candidate 1/1 · -10.026 kcal/mol',
      })
    ).toHaveAttribute('aria-pressed', 'true');
    expect(
      within(list).getByRole('button', {
        name: 'Select #2 MDM2-025 · Pose within candidate 1/1 · -9.876 kcal/mol',
      })
    ).toHaveAttribute('aria-pressed', 'true');
    expect(screen.queryByTestId('synon-biomed-docking-pose-loading')).not.toBeInTheDocument();
    await waitFor(() =>
      expect(molstarMocks.engine.replaceDockingComparison).toHaveBeenLastCalledWith(
        'D01',
        'D02',
        expect.objectContaining({ focusCamera: false })
      )
    );

    await act(async () => {
      finishPocket({
        hasLigand: true,
        ligandCount: 24,
        residueCount: 8,
        distanceCount: 4,
      });
    });
    expect(
      within(list).getByRole('button', {
        name: 'Select #2 MDM2-025 · Pose within candidate 1/1 · -9.876 kcal/mol',
      })
    ).toHaveAttribute('aria-pressed', 'true');
    expect(pocket).toHaveAttribute('data-active', 'true');
  });

  it('uses list multi-selection to compare real ligand poses with fixed distinct colors', async () => {
    const ensemble = [
      'REMARK 900 SYNON BIOMED DOCKING COMPLEX ENSEMBLE',
      'ATOM      1 CA   GLY A  16      27.817 -18.400  -4.985  1.00 46.96           C',
      'REMARK 900 REFERENCE LIGAND REF SOURCE G7I CHAIN A RESIDUE 201',
      'HETATM    2 C1   REF Z   1      30.588 -29.829   3.037  1.00 18.75           C',
      'REMARK 900 DOCKED LIGAND D01 CANDIDATE MDM2-012 RANK 1 POSE 1 AFFINITY -10.026 KCAL/MOL',
      'HETATM    3 C1   D01 Z 101      31.000 -28.000   2.000  1.00  0.00           C',
      'REMARK 900 DOCKED LIGAND D02 CANDIDATE MDM2-012 RANK 1 POSE 2 AFFINITY -9.901 KCAL/MOL',
      'HETATM    4 C1   D02 Z 102      32.000 -27.000   1.000  1.00  0.00           C',
      'END',
    ].join('\n');

    await renderWithI18n(
      <SynonBiomedStructureViewer filename='docking_complex_ensemble.pdb' content={ensemble} />,
      'en-US'
    );
    await expandCompoundListFromRight();
    const list = await screen.findByTestId('synon-biomed-docking-compound-list');
    fireEvent.click(
      within(list).getByRole('button', {
        name: 'Select G7I · Reference co-crystal ligand',
      })
    );
    expect(molstarMocks.engine.loadDockingEnsemble).toHaveBeenCalledTimes(1);
    expect(molstarMocks.engine.load).not.toHaveBeenCalled();
    await waitFor(() =>
      expect(molstarMocks.engine.replaceDockingComparison).toHaveBeenLastCalledWith(
        'D01',
        'REF',
        expect.objectContaining({
          focusCamera: false,
          primaryColor: 0x0f766e,
          secondaryColor: 0xea580c,
        })
      )
    );
    expect(screen.queryByRole('button', { name: 'Compare' })).not.toBeInTheDocument();
  });

  it('keeps the selected left-side view when the visible docking ligands change', async () => {
    const ensemble = [
      'REMARK 900 SYNON BIOMED DOCKING COMPLEX ENSEMBLE',
      'ATOM      1 CA   GLY A  16      27.817 -18.400  -4.985  1.00 46.96           C',
      'REMARK 900 REFERENCE LIGAND REF SOURCE G7I CHAIN A RESIDUE 201',
      'HETATM    2 C1   REF Z   1      30.588 -29.829   3.037  1.00 18.75           C',
      'REMARK 900 DOCKED LIGAND D01 CANDIDATE MDM2-012 RANK 1 POSE 1 AFFINITY -10.026 KCAL/MOL',
      'HETATM    3 C1   D01 Z 101      31.000 -28.000   2.000  1.00  0.00           C',
      'REMARK 900 DOCKED LIGAND D02 CANDIDATE MDM2-025 RANK 2 POSE 1 AFFINITY -9.876 KCAL/MOL',
      'HETATM    4 C1   D02 Z 102      32.000 -27.000   1.000  1.00  0.00           C',
      'END',
    ].join('\n');

    await renderWithI18n(
      <SynonBiomedStructureViewer
        filename='docking_complex_ensemble.pdb'
        content={ensemble}
        conversationId='frame-electrostatic-docking-selection'
      />,
      'en-US'
    );

    await expandCompoundListFromRight();
    const list = await screen.findByTestId('synon-biomed-docking-compound-list');
    await waitFor(() =>
      expect(
        within(list).getByRole('button', {
          name: 'Select #1 MDM2-012 · Pose within candidate 1/1 · -10.026 kcal/mol',
        })
      ).toHaveAttribute('aria-pressed', 'true')
    );
    const proteinSurface = screen.getByRole('button', {
      name: 'Protein surface',
    });
    const pocket = screen.getByRole('button', { name: 'Pocket' });
    fireEvent.click(proteinSurface);
    await waitFor(() =>
      expect(molstarMocks.engine.applyDockingPocket).toHaveBeenLastCalledWith(
        'D01',
        expect.objectContaining({
          displayLayers: ['surface'],
          focusCamera: false,
        })
      )
    );

    fireEvent.click(
      within(list).getByRole('button', {
        name: 'Select #2 MDM2-025 · Pose within candidate 1/1 · -9.876 kcal/mol',
      })
    );

    await waitFor(() =>
      expect(molstarMocks.engine.replaceDockingComparison).toHaveBeenLastCalledWith(
        'D01',
        'D02',
        expect.objectContaining({
          displayLayers: ['surface'],
          focusCamera: false,
        })
      )
    );
    expect(proteinSurface).toHaveAttribute('aria-pressed', 'true');
    expect(pocket).toHaveAttribute('aria-pressed', 'true');
  });

  it('requests one batch and binds a distinct electrostatic potential for every selected docking ligand', async () => {
    const ensemble = [
      'REMARK 900 SYNON BIOMED DOCKING COMPLEX ENSEMBLE',
      'ATOM      1 CA   GLY A  16      27.817 -18.400  -4.985  1.00 46.96           C',
      'REMARK 900 REFERENCE LIGAND REF SOURCE G7I CHAIN A RESIDUE 201',
      'HETATM    4 C1   REF Z   1      30.000 -29.000   3.000  1.00 18.75           C',
      'REMARK 900 DOCKED LIGAND D01 CANDIDATE MDM2-012 RANK 1 POSE 1 AFFINITY -10.026 KCAL/MOL',
      'HETATM    2 C1   D01 Z 101      31.000 -28.000   2.000  1.00  0.00           C',
      'REMARK 900 DOCKED LIGAND D02 CANDIDATE MDM2-012 RANK 1 POSE 2 AFFINITY -9.901 KCAL/MOL',
      'HETATM    3 C1   D02 Z 102      32.000 -27.000   1.000  1.00  0.00           C',
      'END',
    ].join('\n');
    molstarMocks.engine.getElectrostaticInputSource.mockReturnValue({
      content: 'ATOM receptor coordinates\nEND\n',
      ligands: [
        { key: 'D01', molBlock: 'D01 electrostatic mol block', atomCount: 1 },
        { key: 'D02', molBlock: 'D02 electrostatic mol block', atomCount: 1 },
      ],
      atomCount: 3,
    });
    electrostaticTransportMocks.decode.mockResolvedValueOnce({
      protein: 'protein-batch-dx',
      ligands: { D01: 'ligand-D01-dx', D02: 'ligand-D02-dx' },
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      if (String(input).includes('/structure-electrostatic-map')) {
        return new Response(
          JSON.stringify(
            batchElectrostaticResponse(
              ['D01', 'D02'],
              ['non-protein HETATM records were excluded from the protein potential']
            )
          ),
          {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          }
        );
      }
      return new Response('HEADER TEST\nATOM      1  N   MET A   1', { status: 200 });
    });
    vi.stubGlobal('fetch', fetchMock);

    await renderWithI18n(
      <SynonBiomedStructureViewer
        filename='docking_complex_ensemble.pdb'
        content={ensemble}
        conversationId='frame-electrostatic-multi-ligand'
      />,
      'en-US'
    );
    await expandCompoundListFromRight();
    const list = await screen.findByTestId('synon-biomed-docking-compound-list');
    fireEvent.click(
      within(list).getByRole('button', {
        name: 'Select #1 MDM2-012 · Pose within candidate 2/2 · -9.901 kcal/mol',
      })
    );
    await waitFor(() =>
      expect(molstarMocks.engine.replaceDockingComparison).toHaveBeenLastCalledWith(
        'D01',
        'D02',
        expect.objectContaining({ focusCamera: false })
      )
    );

    fireEvent.click(screen.getByRole('button', { name: 'Ligand surface' }));
    await waitFor(
      () =>
        expect(
          fetchMock.mock.calls.filter(([input]) => String(input).includes('/structure-electrostatic-map'))
        ).toHaveLength(1),
      { timeout: 3000 }
    );
    const electrostaticBodies = fetchMock.mock.calls
      .filter(([input]) => String(input).includes('/structure-electrostatic-map'))
      .map(([, init]) => JSON.parse(String((init as RequestInit).body)));
    expect(electrostaticBodies).toHaveLength(1);
    expect(electrostaticBodies[0]).not.toHaveProperty('ligand_mol_block');
    expect(electrostaticBodies[0].ligand_mol_blocks).toEqual([
      { key: 'D01', mol_block: 'D01 electrostatic mol block' },
      { key: 'D02', mol_block: 'D02 electrostatic mol block' },
    ]);
    expect(molstarMocks.engine.getElectrostaticInputSource).toHaveBeenLastCalledWith(['D01', 'D02']);
    expect(molstarMocks.engine.setElectrostaticPotentials).toHaveBeenLastCalledWith(
      {
        protein: { source: 'protein-batch-dx', label: 'docking_complex_ensemble-protein-apbs.dx' },
        ligands: {
          D01: { source: 'ligand-D01-dx', label: 'docking_complex_ensemble-D01-ligand-apbs.dx' },
          D02: { source: 'ligand-D02-dx', label: 'docking_complex_ensemble-D02-ligand-apbs.dx' },
        },
      },
      [-5, 5]
    );
    const electrostaticLegend = screen.getByTestId('synon-biomed-electrostatic-legend');
    fireEvent.click(within(electrostaticLegend).getByRole('button', { name: 'Expand the electrostatic scale' }));
    expect(
      within(electrostaticLegend).getByText(
        'Scientific warning: non-protein HETATM records were excluded from the protein potential'
      )
    ).toBeInTheDocument();
  });

  it('rolls the Molstar graph and React selection back together when replacement APBS fails', async () => {
    const ensemble = [
      'REMARK 900 SYNON BIOMED DOCKING COMPLEX ENSEMBLE',
      'ATOM      1 CA   GLY A  16      27.817 -18.400  -4.985  1.00 46.96           C',
      'REMARK 900 REFERENCE LIGAND REF SOURCE G7I CHAIN A RESIDUE 201',
      'HETATM    4 C1   REF Z   1      30.000 -29.000   3.000  1.00 18.75           C',
      'REMARK 900 DOCKED LIGAND D01 CANDIDATE MDM2-012 RANK 1 POSE 1 AFFINITY -10.026 KCAL/MOL',
      'HETATM    2 C1   D01 Z 101      31.000 -28.000   2.000  1.00  0.00           C',
      'REMARK 900 DOCKED LIGAND D02 CANDIDATE MDM2-012 RANK 1 POSE 2 AFFINITY -9.901 KCAL/MOL',
      'HETATM    3 C1   D02 Z 102      32.000 -27.000   1.000  1.00  0.00           C',
      'END',
    ].join('\n');
    molstarMocks.engine.getElectrostaticInputSource
      .mockReturnValueOnce({
        content: 'ATOM receptor coordinates\nEND\n',
        ligands: [{ key: 'D01', molBlock: 'D01 electrostatic mol block', atomCount: 1 }],
        atomCount: 2,
      })
      .mockReturnValueOnce({
        content: 'ATOM receptor coordinates\nEND\n',
        ligands: [
          { key: 'D01', molBlock: 'D01 electrostatic mol block', atomCount: 1 },
          { key: 'D02', molBlock: 'D02 electrostatic mol block', atomCount: 1 },
        ],
        atomCount: 3,
      });
    electrostaticTransportMocks.decode.mockResolvedValueOnce({
      protein: 'protein-D01-dx',
      ligand: 'ligand-D01-dx',
    });
    let electrostaticRequestCount = 0;
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      if (String(input).includes('/structure-electrostatic-map')) {
        electrostaticRequestCount += 1;
        if (electrostaticRequestCount === 1) {
          return new Response(JSON.stringify(electrostaticResponse()), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          });
        }
        return new Response(
          JSON.stringify({
            ok: false,
            status: 'failed',
            code: 'electrostatic_map_failed',
            message: 'replacement electrostatic map failed',
            retryable: false,
          }),
          { status: 422, headers: { 'Content-Type': 'application/json' } }
        );
      }
      return new Response('HEADER TEST\nATOM      1  N   MET A   1', { status: 200 });
    });
    vi.stubGlobal('fetch', fetchMock);

    await renderWithI18n(
      <SynonBiomedStructureViewer
        filename='docking_complex_ensemble.pdb'
        content={ensemble}
        conversationId='frame-electrostatic-rollback'
      />,
      'en-US'
    );
    await expandCompoundListFromRight();
    const list = await screen.findByTestId('synon-biomed-docking-compound-list');
    const firstPose = within(list).getByRole('button', {
      name: 'Select #1 MDM2-012 · Pose within candidate 1/2 · -10.026 kcal/mol',
    });
    const secondPose = within(list).getByRole('button', {
      name: 'Select #1 MDM2-012 · Pose within candidate 2/2 · -9.901 kcal/mol',
    });
    fireEvent.click(screen.getByRole('button', { name: 'Ligand surface' }));
    await waitFor(() => expect(molstarMocks.engine.setElectrostaticPotentials).toHaveBeenCalledTimes(1));
    expect(firstPose).toHaveAttribute('aria-pressed', 'true');
    expect(secondPose).toHaveAttribute('aria-pressed', 'false');

    fireEvent.click(secondPose);
    await waitFor(() => expect(electrostaticRequestCount).toBe(2));
    await waitFor(() => expect(secondPose).toHaveAttribute('aria-pressed', 'false'));
    expect(firstPose).toHaveAttribute('aria-pressed', 'true');
    expect(screen.getByRole('button', { name: 'Ligand surface' })).toHaveAttribute('aria-pressed', 'true');
    expect(screen.getByTestId('synon-biomed-electrostatic-legend')).toBeInTheDocument();
    expect(molstarMocks.engine.replaceDockingComparison).toHaveBeenLastCalledWith(
      'D01',
      'D02',
      expect.objectContaining({ displayLayers: [], focusCamera: false })
    );
    expect(molstarMocks.engine.applyDockingPocket).toHaveBeenLastCalledWith(
      'D01',
      expect.objectContaining({ displayLayers: ['ligand-surface'], focusCamera: false })
    );
    expect(molstarMocks.engine.replaceDockingComparison.mock.invocationCallOrder.at(-1)).toBeLessThan(
      molstarMocks.engine.applyDockingPocket.mock.invocationCallOrder.at(-1)!
    );
    expect(molstarMocks.engine.setElectrostaticPotentials).toHaveBeenCalledTimes(2);
    expect(molstarMocks.engine.setElectrostaticPotentials).toHaveBeenLastCalledWith(
      {
        protein: { source: 'protein-D01-dx', label: 'docking_complex_ensemble-protein-apbs.dx' },
        ligands: {
          D01: { source: 'ligand-D01-dx', label: 'docking_complex_ensemble-D01-ligand-apbs.dx' },
        },
      },
      [-5, 5]
    );
  });

  it('stacks compatible display layers while keeping competing layers exclusive', async () => {
    const ensemble = [
      'REMARK 900 SYNON BIOMED DOCKING COMPLEX ENSEMBLE',
      'ATOM      1 CA   GLY A  16      27.817 -18.400  -4.985  1.00 46.96           C',
      'REMARK 900 REFERENCE LIGAND REF SOURCE G7I CHAIN A RESIDUE 201',
      'HETATM    2 C1   REF Z   1      30.588 -29.829   3.037  1.00 18.75           C',
      'REMARK 900 DOCKED LIGAND D01 CANDIDATE MDM2-012 RANK 1 POSE 1 AFFINITY -10.026 KCAL/MOL',
      'HETATM    3 C1   D01 Z 101      31.000 -28.000   2.000  1.00  0.00           C',
      'END',
    ].join('\n');

    await renderWithI18n(
      <SynonBiomedStructureViewer
        filename='docking_complex_ensemble.pdb'
        content={ensemble}
        conversationId='frame-electrostatic-stacking'
      />,
      'en-US'
    );

    const proteinSurface = await screen.findByRole('button', {
      name: 'Protein surface',
    });
    const pocketSurface = screen.getByRole('button', {
      name: 'Pocket surface',
    });
    const ligandSurface = screen.getByRole('button', {
      name: 'Ligand surface',
    });
    const ballAndStick = screen.getByRole('button', {
      name: 'Ball-and-stick',
    });
    const line = screen.getByRole('button', { name: 'Line' });

    fireEvent.click(proteinSurface);
    await waitFor(() =>
      expect(molstarMocks.engine.applyDockingPocket).toHaveBeenLastCalledWith(
        'D01',
        expect.objectContaining({
          displayLayers: ['surface'],
          focusCamera: false,
        })
      )
    );
    fireEvent.click(ligandSurface);
    await waitFor(() =>
      expect(molstarMocks.engine.applyDockingPocket).toHaveBeenLastCalledWith(
        'D01',
        expect.objectContaining({
          displayLayers: ['surface', 'ligand-surface'],
          focusCamera: false,
        })
      )
    );
    expect(proteinSurface).toHaveAttribute('aria-pressed', 'true');
    expect(ligandSurface).toHaveAttribute('aria-pressed', 'true');

    fireEvent.click(pocketSurface);
    await waitFor(() =>
      expect(molstarMocks.engine.applyDockingPocket).toHaveBeenLastCalledWith(
        'D01',
        expect.objectContaining({
          displayLayers: ['ligand-surface', 'pocket-surface'],
          focusCamera: false,
        })
      )
    );
    expect(proteinSurface).toHaveAttribute('aria-pressed', 'false');
    expect(pocketSurface).toHaveAttribute('aria-pressed', 'true');
    expect(ligandSurface).toHaveAttribute('aria-pressed', 'true');

    fireEvent.click(ballAndStick);
    await waitFor(() =>
      expect(molstarMocks.engine.applyDockingPocket).toHaveBeenLastCalledWith(
        'D01',
        expect.objectContaining({
          displayLayers: ['ligand-surface', 'pocket-surface', 'ball-and-stick'],
          focusCamera: false,
        })
      )
    );
    fireEvent.click(line);
    await waitFor(() =>
      expect(molstarMocks.engine.applyDockingPocket).toHaveBeenLastCalledWith(
        'D01',
        expect.objectContaining({
          displayLayers: ['ligand-surface', 'pocket-surface', 'line'],
          focusCamera: false,
        })
      )
    );
    expect(ballAndStick).toHaveAttribute('aria-pressed', 'false');
    expect(line).toHaveAttribute('aria-pressed', 'true');
  });

  it('commits an explicit empty layer set when the final docking surface is turned off', async () => {
    const ensemble = [
      'REMARK 900 SYNON BIOMED DOCKING COMPLEX ENSEMBLE',
      'REMARK 900 REFERENCE LIGAND REF SOURCE G7I CHAIN A RESIDUE 201',
      'ATOM      1 CA   GLY A  16      27.817 -18.400  -4.985  1.00 46.96           C',
      'HETATM    6 C1   REF A 201      29.000 -24.000   0.000  1.00  0.00           C',
      'REMARK 900 DOCKED LIGAND D01 CANDIDATE MDM2-012 RANK 1 POSE 1 AFFINITY -10.026 KCAL/MOL',
      'HETATM    2 C1   D01 Z 101      31.000 -28.000   2.000  1.00  0.00           C',
      'END',
    ].join('\n');

    await renderWithI18n(
      <SynonBiomedStructureViewer
        filename='docking_complex_ensemble.pdb'
        content={ensemble}
        conversationId='frame-electrostatic-final-layer-off'
      />,
      'en-US'
    );

    const proteinSurface = await screen.findByRole('button', { name: 'Protein surface' });
    fireEvent.click(proteinSurface);
    await waitFor(() => expect(proteinSurface).toHaveAttribute('aria-pressed', 'true'));
    expect(screen.getByTestId('synon-biomed-electrostatic-legend')).toBeInTheDocument();

    fireEvent.click(proteinSurface);
    await waitFor(() =>
      expect(molstarMocks.engine.applyDockingPocket).toHaveBeenLastCalledWith(
        'D01',
        expect.objectContaining({ displayLayers: [], focusCamera: false })
      )
    );
    expect(proteinSurface).toHaveAttribute('aria-pressed', 'false');
    expect(screen.queryByTestId('synon-biomed-electrostatic-legend')).not.toBeInTheDocument();
  });

  it('keeps the surface toggle and legend off when docking refinement rejects the graph', async () => {
    const ensemble = [
      'REMARK 900 SYNON BIOMED DOCKING COMPLEX ENSEMBLE',
      'REMARK 900 REFERENCE LIGAND REF SOURCE G7I CHAIN A RESIDUE 201',
      'ATOM      1 CA   GLY A  16      27.817 -18.400  -4.985  1.00 46.96           C',
      'HETATM    6 C1   REF A 201      29.000 -24.000   0.000  1.00  0.00           C',
      'REMARK 900 DOCKED LIGAND D01 CANDIDATE MDM2-012 RANK 1 POSE 1 AFFINITY -10.026 KCAL/MOL',
      'HETATM    2 C1   D01 Z 101      31.000 -28.000   2.000  1.00  0.00           C',
      'END',
    ].join('\n');
    molstarMocks.engine.applyDockingPocket.mockRejectedValueOnce(new Error('surface graph failed'));

    await renderWithI18n(
      <SynonBiomedStructureViewer
        filename='docking_complex_ensemble.pdb'
        content={ensemble}
        conversationId='frame-electrostatic-refinement-failure'
      />,
      'en-US'
    );

    const proteinSurface = await screen.findByRole('button', { name: 'Protein surface' });
    fireEvent.click(proteinSurface);
    await waitFor(() =>
      expect(screen.getByRole('status')).toHaveTextContent('That structure action could not be completed.')
    );
    expect(proteinSurface).toHaveAttribute('aria-pressed', 'false');
    expect(screen.queryByTestId('synon-biomed-electrostatic-legend')).not.toBeInTheDocument();
  });

  it('rebuilds native docking interactions when a legend provider is toggled without strength data', async () => {
    const ensemble = [
      'REMARK 900 SYNON BIOMED DOCKING COMPLEX ENSEMBLE',
      'REMARK 900 REFERENCE LIGAND REF SOURCE G7I CHAIN A RESIDUE 201',
      'ATOM      1 CA   GLY A  16      27.817 -18.400  -4.985  1.00 46.96           C',
      'HETATM    6 C1   REF A 201      29.000 -24.000   0.000  1.00  0.00           C',
      'REMARK 900 DOCKED LIGAND D01 CANDIDATE MDM2-012 RANK 1 POSE 1 AFFINITY -10.026 KCAL/MOL',
      'HETATM    2 C1   D01 Z 101      31.000 -28.000   2.000  1.00  0.00           C',
      'END',
    ].join('\n');

    await renderWithI18n(
      <SynonBiomedStructureViewer filename='docking_complex_ensemble.pdb' content={ensemble} />,
      'en-US'
    );
    const interactionLegend = await screen.findByTestId('synon-biomed-pocket-interaction-legend');
    fireEvent.click(within(interactionLegend).getByRole('button', { name: 'Expand the interaction legend' }));
    fireEvent.click(within(interactionLegend).getByRole('button', { name: 'Hydrogen bond ON' }));

    await waitFor(() =>
      expect(molstarMocks.engine.applyDockingPocket).toHaveBeenLastCalledWith(
        'D01',
        expect.objectContaining({
          displayLayers: [],
          focusCamera: false,
          interactionVisibility: expect.objectContaining({ 'hydrogen-bonds': false }),
        })
      )
    );
    expect(within(interactionLegend).getByRole('button', { name: 'Hydrogen bond OFF' })).toHaveAttribute(
      'aria-pressed',
      'false'
    );
  });

  it('does not let a retired strength request restore an OFF legend provider', async () => {
    const ensemble = [
      'REMARK 900 SYNON BIOMED DOCKING COMPLEX ENSEMBLE',
      'REMARK 900 REFERENCE LIGAND REF SOURCE G7I CHAIN A RESIDUE 201',
      'ATOM      1 CA   GLY A  16      27.817 -18.400  -4.985  1.00 46.96           C',
      'HETATM    6 C1   REF A 201      29.000 -24.000   0.000  1.00  0.00           C',
      'REMARK 900 DOCKED LIGAND D01 CANDIDATE MDM2-012 RANK 1 POSE 1 AFFINITY -10.026 KCAL/MOL',
      'HETATM    2 C1   D01 Z 101      31.000 -28.000   2.000  1.00  0.00           C',
      'END',
    ].join('\n');
    let resolveStrengthReport: (response: Response) => void;
    const strengthReport = new Promise<Response>((resolve) => {
      resolveStrengthReport = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      if (String(input).includes('/structure-interaction-diagram')) return strengthReport;
      return new Response('unused', { status: 404 });
    });
    vi.stubGlobal('fetch', fetchMock);

    await renderWithI18n(
      <SynonBiomedStructureViewer
        filename='docking_complex_ensemble.pdb'
        content={ensemble}
        rootFrameId='frame-strength-race'
        companionArtifactUrls={{ 'final_candidates.smi': '/api/artifacts/candidates/versions/latest' }}
      />,
      'en-US'
    );
    await waitFor(() =>
      expect(fetchMock.mock.calls.some(([input]) => String(input).includes('/structure-interaction-diagram'))).toBe(
        true
      )
    );

    const interactionLegend = await screen.findByTestId('synon-biomed-pocket-interaction-legend');
    fireEvent.click(within(interactionLegend).getByRole('button', { name: 'Expand the interaction legend' }));
    fireEvent.click(within(interactionLegend).getByRole('button', { name: 'Hydrogen bond ON' }));
    await waitFor(() =>
      expect(within(interactionLegend).getByRole('button', { name: 'Hydrogen bond OFF' })).toHaveAttribute(
        'aria-pressed',
        'false'
      )
    );

    await act(async () => {
      resolveStrengthReport!(
        new Response(
          JSON.stringify({
            ok: true,
            status: 'completed',
            report: {
              interactions: [
                {
                  kind: 'hydrogen-bond',
                  strength_index: 0.82,
                  ligand_position: [31, -28, 2],
                  protein_position: [27.817, -18.4, -4.985],
                  residue: { name: 'GLY', number: 16, chain: 'A' },
                },
              ],
            },
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } }
        )
      );
      await Promise.resolve();
    });

    const populatedStrengthCalls = () =>
      molstarMocks.engine.setPocketInteractionStrengths.mock.calls.filter(
        ([records]) => Array.isArray(records) && records.length > 0
      );
    await waitFor(() => expect(populatedStrengthCalls()).toHaveLength(1));
    expect(populatedStrengthCalls()[0]?.[1]).toEqual(expect.objectContaining({ 'hydrogen-bonds': false }));
  });

  it('restores the previous native and strength interaction state when a provider update fails', async () => {
    const ensemble = [
      'REMARK 900 SYNON BIOMED DOCKING COMPLEX ENSEMBLE',
      'REMARK 900 REFERENCE LIGAND REF SOURCE G7I CHAIN A RESIDUE 201',
      'ATOM      1 CA   GLY A  16      27.817 -18.400  -4.985  1.00 46.96           C',
      'HETATM    6 C1   REF A 201      29.000 -24.000   0.000  1.00  0.00           C',
      'REMARK 900 DOCKED LIGAND D01 CANDIDATE MDM2-012 RANK 1 POSE 1 AFFINITY -10.026 KCAL/MOL',
      'HETATM    2 C1   D01 Z 101      31.000 -28.000   2.000  1.00  0.00           C',
      'END',
    ].join('\n');
    let rejected = false;
    molstarMocks.engine.setPocketInteractionStrengths.mockImplementation(async (_records, visibility) => {
      if (!rejected && visibility?.['hydrogen-bonds'] === false) {
        rejected = true;
        throw new Error('strength graph failed');
      }
    });

    await renderWithI18n(
      <SynonBiomedStructureViewer filename='docking_complex_ensemble.pdb' content={ensemble} />,
      'en-US'
    );
    const interactionLegend = await screen.findByTestId('synon-biomed-pocket-interaction-legend');
    fireEvent.click(within(interactionLegend).getByRole('button', { name: 'Expand the interaction legend' }));
    fireEvent.click(within(interactionLegend).getByRole('button', { name: 'Hydrogen bond ON' }));

    await waitFor(() =>
      expect(screen.getByRole('status')).toHaveTextContent('That structure action could not be completed.')
    );
    expect(within(interactionLegend).getByRole('button', { name: 'Hydrogen bond ON' })).toHaveAttribute(
      'aria-pressed',
      'true'
    );
    expect(molstarMocks.engine.applyDockingPocket).toHaveBeenLastCalledWith(
      'D01',
      expect.objectContaining({
        focusCamera: false,
        interactionVisibility: expect.objectContaining({ 'hydrogen-bonds': true }),
      })
    );
    expect(molstarMocks.engine.setPocketInteractionStrengths).toHaveBeenLastCalledWith(
      [],
      expect.objectContaining({ 'hydrogen-bonds': true })
    );
  });

  it('lists protein, reference, and ranked compounds with real multi-selection and resizable edges', async () => {
    const ensemble = [
      'REMARK 900 SYNON BIOMED DOCKING COMPLEX ENSEMBLE',
      'ATOM      1 CA   GLY A  16      27.817 -18.400  -4.985  1.00 46.96           C',
      'REMARK 900 REFERENCE LIGAND REF SOURCE G7I CHAIN A RESIDUE 201',
      'HETATM    2 C1   REF Z   1      30.588 -29.829   3.037  1.00 18.75           C',
      'REMARK 900 DOCKED LIGAND D03 CANDIDATE MDM2-030 RANK 3 AFFINITY -9.100 KCAL/MOL',
      'HETATM    3 C1   D03 Z 103      33.000 -26.000   0.500  1.00  0.00           C',
      'REMARK 900 DOCKED LIGAND D01 CANDIDATE MDM2-010 RANK 1 AFFINITY -10.100 KCAL/MOL',
      'HETATM    4 C1   D01 Z 101      31.000 -28.000   2.000  1.00  0.00           C',
      'REMARK 900 DOCKED LIGAND D02 CANDIDATE MDM2-020 RANK 2 AFFINITY -9.700 KCAL/MOL',
      'HETATM    5 C1   D02 Z 102      32.000 -27.000   1.000  1.00  0.00           C',
      'END',
    ].join('\n');

    await renderWithI18n(
      <SynonBiomedStructureViewer
        filename='docking_complex_ensemble.pdb'
        content={ensemble}
        companionArtifactUrls={{
          'final_candidates.smi': '/api/artifacts/candidates/versions/latest',
        }}
      />,
      'en-US'
    );

    await expandCompoundListFromRight();
    const list = await screen.findByTestId('synon-biomed-docking-compound-list');
    const orderedLabels = Array.from(
      list.querySelectorAll<HTMLButtonElement>('.synon-biomed-molstar__compound-main')
    ).map((button) => button.getAttribute('aria-label'));
    expect(orderedLabels).toEqual([
      'Show or hide the protein receptor',
      'Select G7I · Reference co-crystal ligand',
      'Select #1 MDM2-010 · Pose within candidate 1/1 · -10.100 kcal/mol',
      'Select #2 MDM2-020 · Pose within candidate 1/1 · -9.700 kcal/mol',
      'Select #3 MDM2-030 · Pose within candidate 1/1 · -9.100 kcal/mol',
    ]);
    const depictionPanel = await screen.findByTestId('synon-biomed-docking-ligand-depiction');
    expect(depictionPanel).toHaveAttribute('data-collapsed', 'true');
    expect(within(depictionPanel).queryByRole('img')).not.toBeInTheDocument();
    fireEvent.click(
      within(depictionPanel).getByRole('button', {
        name: 'Expand the 2D structure upward',
      })
    );
    expect(
      await within(depictionPanel).findByRole('img', {
        name: '2D structure of MDM2-010',
      })
    ).toBeInTheDocument();
    fireEvent.click(
      within(depictionPanel).getByRole('button', {
        name: 'Collapse the 2D structure downward',
      })
    );
    expect(depictionPanel).toHaveAttribute('data-collapsed', 'true');
    expect(within(depictionPanel).queryByRole('img')).not.toBeInTheDocument();
    fireEvent.click(
      within(depictionPanel).getByRole('button', {
        name: 'Expand the 2D structure upward',
      })
    );
    expect(
      await within(depictionPanel).findByRole('img', {
        name: '2D structure of MDM2-010',
      })
    ).toBeInTheDocument();

    const protein = within(list).getByRole('button', {
      name: 'Show or hide the protein receptor',
    });
    fireEvent.click(protein);
    expect(molstarMocks.engine.setDockingProteinVisible).toHaveBeenLastCalledWith(false);
    expect(protein).toHaveAttribute('aria-pressed', 'false');
    fireEvent.click(protein);
    expect(molstarMocks.engine.setDockingProteinVisible).toHaveBeenLastCalledWith(true);

    fireEvent.click(
      within(list).getByRole('button', {
        name: 'Select #2 MDM2-020 · Pose within candidate 1/1 · -9.700 kcal/mol',
      })
    );
    await waitFor(() =>
      expect(molstarMocks.engine.replaceDockingComparison).toHaveBeenLastCalledWith(
        'D01',
        'D02',
        expect.objectContaining({ focusCamera: false })
      )
    );
    fireEvent.click(
      within(list).getByRole('button', {
        name: 'Select #3 MDM2-030 · Pose within candidate 1/1 · -9.100 kcal/mol',
      })
    );
    await waitFor(() =>
      expect(molstarMocks.engine.replaceDockingSelection).toHaveBeenLastCalledWith(
        ['D01', 'D02', 'D03'],
        expect.objectContaining({
          focusCamera: false,
          ligandColors: [0x0f766e, 0xea580c, 0x7c3aed],
        })
      )
    );
    expect(
      within(screen.getByTestId('synon-biomed-docking-ligand-depiction')).getByText('MDM2-030')
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Next selected ligand' }));
    expect(
      within(screen.getByTestId('synon-biomed-docking-ligand-depiction')).getByText('MDM2-010')
    ).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Previous selected ligand' })).toBeInTheDocument();

    fireEvent.click(
      within(list).getByRole('button', {
        name: 'Set the carbon atom and carbon bond color for MDM2-030',
      })
    );
    fireEvent.click(
      within(list).getByRole('button', {
        name: 'Set MDM2-030 carbon atoms and bonds to blue',
      })
    );
    await waitFor(() =>
      expect(molstarMocks.engine.replaceDockingSelection).toHaveBeenLastCalledWith(
        ['D01', 'D02', 'D03'],
        expect.objectContaining({
          focusCamera: false,
          ligandColors: [0x0f766e, 0xea580c, 0x2563eb],
        })
      )
    );

    fireEvent.click(within(list).getByRole('button', { name: 'Select all' }));
    await waitFor(() =>
      expect(molstarMocks.engine.replaceDockingSelection).toHaveBeenLastCalledWith(
        ['REF', 'D01', 'D02', 'D03'],
        expect.objectContaining({
          ligandColors: [0xea580c, 0x0f766e, 0xea580c, 0x2563eb],
        })
      )
    );
    expect(within(list).getByRole('button', { name: 'Select all' })).toBeDisabled();

    fireEvent.click(within(list).getByRole('button', { name: 'Clear all' }));
    await waitFor(() => expect(molstarMocks.engine.clearDockingSelection).toHaveBeenCalled());
    expect(within(list).getByRole('button', { name: 'Clear all' })).toBeDisabled();

    const card = screen.getByTestId('synon-biomed-docking-score-card');
    fireEvent.keyDown(
      screen.getByRole('separator', {
        name: 'Drag to resize the compound list width',
      }),
      {
        key: 'ArrowLeft',
      }
    );
    expect(card).toHaveStyle({ width: '196px' });
    fireEvent.keyDown(
      screen.getByRole('separator', {
        name: 'Drag to resize the compound list height',
      }),
      {
        key: 'ArrowDown',
      }
    );
    expect(card).toHaveStyle({ height: '736px' });

    fireEvent.click(
      screen.getByRole('button', {
        name: 'Collapse the compound list to the right',
      })
    );
    expect(card).toHaveAttribute('data-collapsed', 'true');
    expect(card).toHaveStyle({ width: '28px', height: '28px' });
    expect(screen.queryByTestId('synon-biomed-docking-compound-list')).not.toBeInTheDocument();
    expect(
      screen.getByRole('button', {
        name: 'Expand the compound list from the right',
      })
    ).toHaveAttribute('aria-expanded', 'false');
  });

  it('passes multi-model molecule files directly to Mol* instead of a custom frame navigator', async () => {
    await renderWithI18n(
      <SynonBiomedStructureViewer filename='docking-poses.sdf' content='first\n$$$$\nsecond\n$$$$\nthird\n$$$$' />,
      'en-US'
    );

    await waitFor(() =>
      expect(molstarMocks.engine.load).toHaveBeenCalledWith(expect.stringContaining('$$$$'), 'docking-poses.sdf', 'sdf')
    );
    expect(screen.queryByRole('button', { name: 'Next model' })).not.toBeInTheDocument();
  });

  it('renders parsed protein and ligand objects for ordinary CIF files and connects their visibility toggles', async () => {
    molstarMocks.engine.load.mockResolvedValueOnce({
      objects: [
        {
          id: 'protein',
          kind: 'protein',
          atomCount: 2222,
          residueNames: ['ALA', 'ARG'],
        },
        {
          id: 'ligand',
          kind: 'ligand',
          atomCount: 88,
          residueNames: ['A1JB1'],
        },
      ],
      atomCount: 2751,
      hasProtein: true,
      hasLigand: true,
    });
    molstarMocks.engine.getPrimaryLigandDepictionSource.mockReturnValueOnce({
      residueName: 'A1JB1',
      interactionResidueName: 'LIG',
      molBlock: 'A1JB1 mol block',
      complexPdb: 'ATOM protein\nHETATM ligand LIG\nEND\n',
      atomCount: 44,
      hasProtein: true,
    });

    await renderWithI18n(<SynonBiomedStructureViewer filename='9R1Z.cif' content='data_9R1Z' />, 'zh-CN');

    await waitFor(() =>
      expect(molstarMocks.engine.applyPocketFocus).toHaveBeenCalledWith(
        expect.objectContaining({
          expandRadius: 4.5,
          showDistances: false,
          focusCamera: true,
        })
      )
    );
    expect(molstarMocks.engine.load.mock.invocationCallOrder[0]).toBeLessThan(
      molstarMocks.engine.applyPocketFocus.mock.invocationCallOrder[0]
    );
    expect(molstarMocks.engine.setBackgroundColor).toHaveBeenCalledWith(0xffffff);
    expect(screen.getByRole('button', { name: '口袋' })).toHaveAttribute('data-active', 'true');

    const card = await screen.findByTestId('synon-biomed-structure-object-card');
    const hostShell = screen.getByTestId('synon-biomed-structure-canvas').parentElement;
    expect(card).toHaveAttribute('data-collapsed', 'true');
    expect(card).toHaveStyle({ width: '28px' });
    expect(hostShell).toHaveStyle({ paddingRight: '0px' });
    expect(screen.queryByTestId('synon-biomed-structure-object-list')).not.toBeInTheDocument();
    const expandCompoundList = within(card).getByRole('button', {
      name: '从右侧展开化合物列表',
    });
    expect(expandCompoundList).toHaveAttribute('aria-expanded', 'false');
    fireEvent.click(expandCompoundList);

    const list = await screen.findByTestId('synon-biomed-structure-object-list');
    expect(card).not.toHaveAttribute('data-collapsed');
    expect(card).toHaveStyle({ width: '180px' });
    expect(hostShell).toHaveStyle({ paddingRight: '180px' });
    expect(screen.getByRole('button', { name: '向右收起化合物列表' })).toHaveAttribute('aria-expanded', 'true');
    expect(within(list).getByText('化合物列表')).toBeInTheDocument();
    expect(within(list).getByText('蛋白')).toBeInTheDocument();
    expect(within(list).getByText('Ligand')).toBeInTheDocument();
    expect(within(list).getByText('A1JB1')).toBeInTheDocument();
    expect(within(list).queryByText(/EDO/)).not.toBeInTheDocument();
    expect(within(list).getByRole('button', { name: '一键全选' })).toBeDisabled();
    expect(within(list).getByRole('button', { name: '一键取消' })).toBeEnabled();

    const depiction = await screen.findByTestId('synon-biomed-structure-ligand-depiction');
    expect(depiction).toHaveAttribute('data-collapsed', 'true');
    expect(within(depiction).queryByRole('img')).not.toBeInTheDocument();
    fireEvent.click(within(depiction).getByRole('button', { name: '向上展开二维结构' }));
    expect(await within(depiction).findByRole('img', { name: 'A1JB1 的二维结构' })).toBeInTheDocument();
    expect(rdkitMocks.validateAndRenderMolBlock).toHaveBeenCalledWith('A1JB1 mol block', 276, 189);

    fireEvent.click(within(list).getByRole('button', { name: '显示或隐藏 ligand' }));
    expect(molstarMocks.engine.setStructureObjectVisible).toHaveBeenCalledWith('ligand', false);
    expect(within(list).getByRole('button', { name: '显示或隐藏 ligand' })).toHaveAttribute('aria-pressed', 'false');
    expect(within(list).getByRole('button', { name: '一键全选' })).toBeEnabled();

    fireEvent.click(
      within(list).getByRole('button', {
        name: '设置 Ligand 的碳原子和碳键颜色',
      })
    );
    const orange = within(list).getByRole('button', {
      name: '将 Ligand 的碳原子和碳键设为橙色',
    });
    fireEvent.click(orange);
    await waitFor(() => expect(molstarMocks.engine.setStructureLigandColor).toHaveBeenCalledWith(0xea580c));

    fireEvent.click(screen.getByRole('button', { name: '向右收起化合物列表' }));
    expect(card).toHaveAttribute('data-collapsed', 'true');
    expect(card).toHaveStyle({ width: '28px', height: '28px' });
    expect(hostShell).toHaveStyle({ paddingRight: '0px' });
    expect(screen.queryByTestId('synon-biomed-structure-object-list')).not.toBeInTheDocument();
  });

  it('uses the parsed primary ligand PDB context for ordinary CIF 2D interactions', async () => {
    molstarMocks.engine.load.mockResolvedValueOnce({
      objects: [
        {
          id: 'protein',
          kind: 'protein',
          atomCount: 2222,
          residueNames: ['ALA'],
        },
        {
          id: 'ligand',
          kind: 'ligand',
          atomCount: 44,
          residueNames: ['A1JB1'],
        },
      ],
      atomCount: 2266,
      hasProtein: true,
      hasLigand: true,
    });
    molstarMocks.engine.getPrimaryLigandDepictionSource.mockReturnValueOnce({
      residueName: 'A1JB1',
      interactionResidueName: 'LIG',
      molBlock: 'A1JB1 mol block',
      complexPdb: 'ATOM      1  CA  ALA A   1\nHETATM   2  C1  LIG Z   1\nEND\n',
      atomCount: 44,
      hasProtein: true,
    });
    const fetchMock = vi.fn(
      async () =>
        new Response(
          JSON.stringify({
            ok: true,
            status: 'completed',
            svg: '<svg xmlns="http://www.w3.org/2000/svg"><text>CIF interaction</text></svg>',
            png_base64: 'iVBORw0KGgo=',
            report: {
              engine: 'Synon 2D Interaction Engine',
              engine_release: '1.0.0',
              ligand_label: 'A1JB1',
              pose_label: 'Primary ligand conformer',
              hydrogen_bond_count: 1,
              salt_bridge_count: 0,
              width: 1710,
              height: 2400,
              png_dpi: 300,
              interactions: [
                {
                  kind: 'hydrogen-bond',
                  strength_index: 0.82,
                  strength_level: 'strong',
                  ligand_position: [31, -28, 2],
                  protein_position: [27.817, -18.4, -4.985],
                  residue: { name: 'GLY', number: 16, chain: 'A' },
                },
              ],
            },
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } }
        )
    );
    vi.stubGlobal('fetch', fetchMock);

    await renderWithI18n(
      <SynonBiomedStructureViewer filename='9R1Z.cif' content='data_9R1Z' rootFrameId='frame-1' />,
      'en-US'
    );

    const renderedStrengthCalls = () =>
      molstarMocks.engine.setPocketInteractionStrengths.mock.calls.filter(
        ([records]) => Array.isArray(records) && records.length > 0
      );
    await waitFor(() => expect(renderedStrengthCalls()).toHaveLength(1));
    expect(renderedStrengthCalls()[0]?.[0]).toEqual([
      expect.objectContaining({
        ligand_label: 'A1JB1',
        strength_index: 0.82,
      }),
    ]);
    const interactionLegend = await screen.findByTestId('synon-biomed-pocket-interaction-legend');
    fireEvent.click(within(interactionLegend).getByRole('button', { name: 'Expand the interaction legend' }));
    expect(within(interactionLegend).getByText('Labels: relative strength 0–1 (not distance)')).toHaveAttribute(
      'data-status',
      'ready'
    );

    const pocketButton = screen.getByRole('button', { name: 'Pocket' });
    fireEvent.click(pocketButton);
    await waitFor(() => expect(pocketButton).not.toHaveAttribute('data-active', 'true'));
    fireEvent.click(pocketButton);
    await waitFor(() => expect(pocketButton).toHaveAttribute('data-active', 'true'));
    await waitFor(() => expect(renderedStrengthCalls()).toHaveLength(2));

    fireEvent.click(await screen.findByRole('button', { name: 'Expand the left toolbar' }));
    const interactionButton = await screen.findByRole('button', {
      name: '2D interactions',
    });
    await waitFor(() => expect(interactionButton).toBeEnabled());
    fireEvent.click(interactionButton);
    await screen.findByTestId('synon-biomed-molstar-interaction-diagram');

    expect(fetchMock).toHaveBeenCalledTimes(2);
    const reportOnlyCall = fetchMock.mock.calls.find(([, init]) => JSON.parse(String(init?.body)).report_only);
    expect(reportOnlyCall).toBeDefined();
    expect(JSON.parse(String(reportOnlyCall?.[1]?.body))).toMatchObject({
      ligand_residue_name: 'LIG',
      smiles: 'C1=CC=CC=C1',
      ligand_label: 'A1JB1',
      report_only: true,
    });
    const [requestUrl, requestInit] = fetchMock.mock.calls.find(
      ([, init]) => !JSON.parse(String(init?.body)).report_only
    ) as unknown as [string, RequestInit];
    expect(requestUrl).toBe('/api/frames/frame-1/structure-interaction-diagram');
    expect(JSON.parse(String(requestInit.body))).toMatchObject({
      content: 'ATOM      1  CA  ALA A   1\nHETATM   2  C1  LIG Z   1\nEND\n',
      filename: '9R1Z-interaction.pdb',
      ligand_residue_name: 'LIG',
      smiles: 'C1=CC=CC=C1',
      ligand_label: 'A1JB1',
      report_only: false,
    });
  });

  it('shows only the protein component for a parsed receptor-only structure', async () => {
    molstarMocks.engine.load.mockResolvedValueOnce({
      objects: [
        {
          id: 'protein',
          kind: 'protein',
          atomCount: 2222,
          residueNames: ['ALA', 'ARG'],
        },
      ],
      atomCount: 2222,
      hasProtein: true,
      hasLigand: false,
    });

    await renderWithI18n(<SynonBiomedStructureViewer filename='receptor.pdb' content='ATOM' />, 'en-US');

    await expandCompoundListFromRight();
    const list = await screen.findByTestId('synon-biomed-structure-object-list');
    expect(within(list).getByText('Protein')).toBeInTheDocument();
    expect(within(list).getByText('2222 atoms')).toBeInTheDocument();
    expect(within(list).queryByText('Ligand')).not.toBeInTheDocument();
  });

  it('shows only the ligand component for a parsed standalone SDF structure', async () => {
    molstarMocks.engine.load.mockResolvedValueOnce({
      objects: [{ id: 'ligand', kind: 'ligand', atomCount: 34, residueNames: ['LIG'] }],
      atomCount: 34,
      hasProtein: false,
      hasLigand: true,
    });
    molstarMocks.engine.getPrimaryLigandDepictionSource.mockReturnValueOnce({
      residueName: 'LIG',
      interactionResidueName: 'LIG',
      molBlock: 'standalone SDF mol block',
      atomCount: 34,
      hasProtein: false,
    });

    await renderWithI18n(<SynonBiomedStructureViewer filename='ligand.sdf' content='ligand' />, 'en-US');

    await expandCompoundListFromRight();
    const list = await screen.findByTestId('synon-biomed-structure-object-list');
    expect(within(list).getByText('Ligand')).toBeInTheDocument();
    expect(within(list).getByText('LIG')).toBeInTheDocument();
    expect(within(list).queryByText('Protein')).not.toBeInTheDocument();
    const depiction = await screen.findByTestId('synon-biomed-structure-ligand-depiction');
    expect(depiction).toHaveAttribute('data-collapsed', 'true');
    fireEvent.click(
      within(depiction).getByRole('button', {
        name: 'Expand the 2D structure upward',
      })
    );
    expect(await within(depiction).findByRole('img', { name: '2D structure of LIG' })).toBeInTheDocument();
    expect(rdkitMocks.validateAndRenderMolBlock).toHaveBeenCalledWith('standalone SDF mol block', 276, 189);
    expect(screen.getByRole('button', { name: '2D interactions' })).toBeDisabled();
  });

  it('adds localized hover and focus descriptions to native Mol* controls', async () => {
    await renderWithI18n(
      <SynonBiomedStructureViewer
        filename='complex.pdb'
        content='HEADER TEST\nATOM      1  N   MET A   1'
        conversationId='frame-electrostatic-toolbar'
      />,
      'zh-CN'
    );

    const selectionDescription = '选择模式：开启后可点击选择结构中的原子、残基或其他对象。';
    await waitFor(() =>
      expect(screen.getByRole('button', { name: selectionDescription })).toHaveAttribute('title', selectionDescription)
    );
    expect(screen.getByRole('button', { name: selectionDescription })).toHaveAttribute(
      'data-synon-molstar-control',
      'selectionMode'
    );
    const selectionButton = screen.getByRole('button', {
      name: selectionDescription,
    });
    fireEvent.pointerOver(selectionButton);
    expect(screen.getByRole('tooltip')).toHaveTextContent(selectionDescription);
    fireEvent.scroll(window);
    expect(screen.getByRole('tooltip')).toHaveTextContent(selectionDescription);
    fireEvent.pointerOut(selectionButton, { relatedTarget: document.body });
    await waitFor(() => expect(screen.queryByRole('tooltip')).not.toBeInTheDocument());

    fireEvent.focus(selectionButton);
    expect(screen.getByRole('tooltip')).toHaveTextContent(selectionDescription);
    fireEvent.blur(selectionButton);
    await waitFor(() => expect(screen.queryByRole('tooltip')).not.toBeInTheDocument());

    const fullscreenDescription = '全屏查看：将结构预览扩展到整个可用窗口。';
    expect(screen.getByRole('button', { name: fullscreenDescription })).toHaveAttribute('title', fullscreenDescription);
  });
  it('lays out every available structure action in the full-height vertical rail', async () => {
    molstarMocks.engine.load.mockResolvedValueOnce({
      objects: [
        { id: 'protein', kind: 'protein', atomCount: 100, residueNames: ['ALA'] },
        { id: 'ligand', kind: 'ligand', atomCount: 12, residueNames: ['LIG'] },
      ],
      atomCount: 112,
      hasProtein: true,
      hasLigand: true,
    });
    molstarMocks.engine.applyPocketFocus.mockResolvedValueOnce({
      hasLigand: false,
      ligandCount: 0,
      residueCount: 0,
      distanceCount: 0,
    });
    await renderWithI18n(
      <SynonBiomedStructureViewer
        filename='complex.pdb'
        content='HEADER TEST\nATOM      1  N   MET A   1'
        conversationId='frame-electrostatic-surfaces'
      />,
      'en-US'
    );

    const toolbar = screen.getByRole('toolbar', {
      name: 'Structure quick actions',
    });
    const controlDock = screen.getByTestId('synon-biomed-molstar-control-dock');
    const quickActions = screen.getByTestId('synon-biomed-molstar-quick-actions');
    expect(quickActions).toHaveAttribute('data-collapsed', 'true');
    expect(controlDock).toHaveAttribute('data-collapsed', 'true');
    fireEvent.click(screen.getByRole('button', { name: 'Expand the left toolbar' }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Initial' })).toBeEnabled());

    expect(screen.getByTestId('synon-biomed-molstar-stage')).toHaveAttribute('data-layout', 'docked');
    expect(controlDock).toHaveAttribute('data-surface', 'integrated');
    expect(toolbar.parentElement).toBe(controlDock);
    expect(screen.queryByText('Display style')).not.toBeInTheDocument();
    expect(within(toolbar).getByRole('group', { name: 'Display style' })).toBeInTheDocument();
    expect(
      within(toolbar)
        .getAllByRole('button')
        .map((button) => button.getAttribute('aria-label') || button.textContent)
    ).toEqual([
      'Pocket',
      'Initial',
      'Ball-and-stick',
      'Line',
      'Protein surface',
      'Pocket surface',
      'Ligand surface',
      '2D interactions',
      'Energy minimization',
      'Dark',
      'White',
      'Snapshot',
      'Save fixed pose',
      'Collapse the left toolbar',
    ]);
    expect(screen.getByRole('button', { name: 'Initial' })).toHaveAttribute('aria-pressed', 'true');
    expect(screen.getByRole('button', { name: 'Ball-and-stick' })).toHaveAttribute('aria-pressed', 'false');
    const collapseToolbar = screen.getByRole('button', {
      name: 'Collapse the left toolbar',
    });
    fireEvent.click(collapseToolbar);
    expect(screen.getByTestId('synon-biomed-molstar-quick-actions')).toHaveAttribute('data-collapsed', 'true');
    expect(screen.getByRole('button', { name: 'Expand the left toolbar' })).toHaveAttribute('aria-expanded', 'false');
    fireEvent.click(screen.getByRole('button', { name: 'Expand the left toolbar' }));
    expect(screen.queryByRole('button', { name: 'View' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Background' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Snapshot' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Pocket' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Energy minimization' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Style' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Parameters' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Select' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Advanced' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'More actions' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Dark' })).toHaveAttribute('aria-pressed', 'false');
    expect(screen.getByRole('button', { name: 'White' })).toHaveAttribute('aria-pressed', 'true');
    const backgroundCallsBeforeSelection = molstarMocks.engine.setBackgroundColor.mock.calls.length;
    fireEvent.click(screen.getByRole('button', { name: 'Dark' }));
    expect(molstarMocks.engine.setBackgroundColor).toHaveBeenLastCalledWith(0x172033);
    expect(molstarMocks.engine.setBackgroundColor).toHaveBeenCalledTimes(backgroundCallsBeforeSelection + 1);
    expect(screen.getByRole('button', { name: 'Snapshot' })).toHaveAttribute(
      'title',
      'Save a PNG snapshot of the current structure.'
    );
    expect(screen.queryByRole('button', { name: 'Fit and center' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Align principal axes' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Reset axes' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Fullscreen' })).not.toBeInTheDocument();
    expect(screen.queryByTestId('synon-biomed-electrostatic-legend')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Ball-and-stick' }));
    await waitFor(() => expect(molstarMocks.engine.applyRepresentationStyle).toHaveBeenCalledWith('ball-and-stick'));
    expect(screen.getByRole('button', { name: 'Ball-and-stick' })).toHaveAttribute('aria-pressed', 'true');
    fireEvent.click(screen.getByRole('button', { name: 'Protein surface' }));
    await waitFor(() => expect(molstarMocks.engine.applyRepresentationStyle).toHaveBeenCalledWith('surface'));
    expect(molstarMocks.engine.setElectrostaticPotentials).toHaveBeenCalledWith(
      {
        protein: { source: electrostaticDX, label: 'complex-protein-apbs.dx' },
        ligands: { LIG: { source: electrostaticDX, label: 'complex-LIG-ligand-apbs.dx' } },
      },
      [-5, 5]
    );
    expect(fetch).toHaveBeenCalledWith(
      '/api/frames/frame-electrostatic-surfaces/structure-electrostatic-map',
      expect.objectContaining({ method: 'POST' })
    );
    expect(screen.queryByRole('button', { name: 'Electrostatic' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Protein surface' })).toHaveAttribute('aria-pressed', 'true');
    expect(screen.getByTestId('synon-biomed-electrostatic-legend')).toHaveAttribute('data-collapsed', 'true');

    fireEvent.click(screen.getByRole('button', { name: 'Pocket' }));
    await waitFor(() =>
      expect(molstarMocks.engine.applyPocketFocus).toHaveBeenCalledWith(
        expect.objectContaining({ expandRadius: 4.5, showDistances: false })
      )
    );
    expect(screen.queryByRole('dialog', { name: 'Pocket preview' })).not.toBeInTheDocument();
    expect(screen.queryByTestId('synon-biomed-pocket-summary')).not.toBeInTheDocument();
    expect(screen.queryByTestId('synon-biomed-pocket-interactions')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Pocket' })).toHaveAttribute('data-active', 'true');
    expect(screen.queryByRole('slider')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Apply pocket parameters' })).not.toBeInTheDocument();

    expect(toolbar).toBeInTheDocument();
    expect(screen.queryByRole('group', { name: 'Structure actions' })).not.toBeInTheDocument();
    expect(screen.queryByTestId('synon-biomed-molstar-selection-toggle')).not.toBeInTheDocument();
    expect(screen.queryByTestId('synon-biomed-molstar-selection-clear')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Advanced' })).not.toBeInTheDocument();
    expect(screen.getByTestId('synon-biomed-molstar-stage')).not.toHaveAttribute('data-advanced-open');
  });

  it('moves trailing structure actions into the vertical-more menu when the rail is short', async () => {
    toolbarResizeHeight = 400;
    molstarMocks.engine.load.mockResolvedValueOnce({
      objects: [
        { id: 'protein', kind: 'protein', atomCount: 100, residueNames: ['ALA'] },
        { id: 'ligand', kind: 'ligand', atomCount: 12, residueNames: ['LIG'] },
      ],
      atomCount: 112,
      hasProtein: true,
      hasLigand: true,
    });
    molstarMocks.engine.applyPocketFocus.mockResolvedValueOnce({
      hasLigand: false,
      ligandCount: 0,
      residueCount: 0,
      distanceCount: 0,
    });
    await renderWithI18n(
      <SynonBiomedStructureViewer filename='complex.pdb' content='HEADER TEST\nATOM      1  N   MET A   1' />,
      'en-US'
    );

    await waitFor(() => expect(screen.getByRole('button', { name: 'Initial' })).toBeEnabled());
    expect(screen.getByTestId('synon-biomed-molstar-quick-pocket')).toBeInTheDocument();
    expect(screen.queryByTestId('synon-biomed-molstar-minimize')).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'More actions' }));
    const structureActions = screen.getByRole('group', {
      name: 'Structure actions',
    });
    expect(
      within(structureActions).getByRole('button', {
        name: 'Energy minimization',
      })
    ).toHaveClass('synon-biomed-molstar__style-button');
    const pocket = screen.getByTestId('synon-biomed-molstar-quick-pocket');
    fireEvent.click(pocket);
    await waitFor(() => expect(molstarMocks.engine.applyPocketFocus).toHaveBeenCalledTimes(2));
  });

  it('renders the structure modules as a left-side vertical tool rail', async () => {
    await renderWithI18n(
      <SynonBiomedStructureViewer filename='complex.pdb' content='HEADER TEST\nATOM      1  N   MET A   1' />,
      'en-US'
    );

    const toolbar = await screen.findByTestId('synon-biomed-molstar-quick-toolbar');
    const actions = screen.getByTestId('synon-biomed-molstar-quick-actions');

    expect(toolbar).toHaveAttribute('aria-orientation', 'vertical');
    expect(toolbar).toHaveAttribute('data-placement', 'left');
    expect(actions).toHaveAttribute('data-orientation', 'vertical');
    expect(within(actions).queryByRole('button', { name: 'More actions' })).not.toBeInTheDocument();
    expect(within(actions).getByRole('button', { name: 'Dark' })).toBeInTheDocument();
    expect(within(actions).getByRole('button', { name: 'White' })).toBeInTheDocument();
    expect(within(actions).getByRole('button', { name: 'Snapshot' })).toBeInTheDocument();
    expect(within(actions).queryByRole('button', { name: 'Advanced' })).not.toBeInTheDocument();
    expect(within(actions).getByRole('button', { name: 'Save fixed pose' })).toBeInTheDocument();
  });

  it('disables ligand-dependent actions for a receptor-only structure', async () => {
    molstarMocks.engine.load.mockResolvedValueOnce({
      objects: [{ id: 'protein', kind: 'protein', atomCount: 100, residueNames: ['ALA'] }],
      atomCount: 100,
      hasProtein: true,
      hasLigand: false,
    });

    await renderWithI18n(
      <SynonBiomedStructureViewer filename='receptor.pdbqt' content='ATOM      1  N   MET A   1' />,
      'en-US'
    );

    const pocketButton = await screen.findByRole('button', { name: 'Pocket' });
    await waitFor(() => expect(screen.getByRole('button', { name: 'Protein surface' })).toBeEnabled());
    expect(pocketButton).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Pocket surface' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Ligand surface' })).toBeDisabled();
    expect(molstarMocks.engine.applyPocketFocus).not.toHaveBeenCalled();
  });

  it('disables protein-dependent actions for a ligand-only docking ensemble', async () => {
    const ensemble = [
      'REMARK 900 SYNON BIOMED DOCKING COMPLEX ENSEMBLE',
      'REMARK 900 REFERENCE LIGAND REF SOURCE G7I CHAIN A RESIDUE 201',
      'HETATM    1 C1   REF A 201      29.000 -24.000   0.000  1.00  0.00           C',
      'REMARK 900 DOCKED LIGAND D01 CANDIDATE MDM2-012 RANK 1 POSE 1 AFFINITY -10.026 KCAL/MOL',
      'HETATM    2 C1   D01 Z 101      31.000 -28.000   2.000  1.00  0.00           C',
      'END',
    ].join('\n');

    await renderWithI18n(
      <SynonBiomedStructureViewer filename='ligand_only_docking_ensemble.pdb' content={ensemble} />,
      'en-US'
    );

    await waitFor(() => expect(screen.getByRole('button', { name: 'Ligand surface' })).toBeEnabled());
    expect(screen.getByRole('button', { name: 'Protein surface' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Pocket surface' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Pocket' })).toBeDisabled();
  });

  it('uses distinct protein, pocket, and ligand surface actions without redundant selection controls', async () => {
    molstarMocks.engine.load.mockResolvedValueOnce({
      objects: [
        { id: 'protein', kind: 'protein', atomCount: 100, residueNames: ['ALA'] },
        { id: 'ligand', kind: 'ligand', atomCount: 12, residueNames: ['LIG'] },
      ],
      atomCount: 112,
      hasProtein: true,
      hasLigand: true,
    });
    molstarMocks.engine.applyPocketFocus.mockResolvedValueOnce({
      hasLigand: false,
      ligandCount: 0,
      residueCount: 0,
      distanceCount: 0,
    });
    await renderWithI18n(
      <SynonBiomedStructureViewer
        filename='complex.pdb'
        content='HEADER TEST\nATOM      1  N   MET A   1'
        conversationId='frame-electrostatic-surface-controls'
      />,
      'zh-CN'
    );

    await waitFor(() => expect(screen.getByRole('button', { name: '初始' })).toBeEnabled());

    expect(screen.queryByText('视图与结构操作')).not.toBeInTheDocument();
    const proteinSurface = screen.getByRole('button', { name: '蛋白表面' });
    const pocketSurface = screen.getByRole('button', { name: '口袋表面' });
    const ligandSurface = screen.getByRole('button', {
      name: 'Ligand 表面',
    });
    const pocket = screen.getByRole('button', { name: '口袋' });

    fireEvent.click(pocket);
    await waitFor(() => expect(molstarMocks.engine.applyPocketFocus).toHaveBeenCalledTimes(2));
    expect(pocket).toHaveAttribute('data-active', 'true');

    fireEvent.click(proteinSurface);
    await waitFor(() =>
      expect(molstarMocks.engine.applyPocketFocus).toHaveBeenLastCalledWith(
        expect.objectContaining({
          focusCamera: false,
          displayLayers: ['surface'],
        })
      )
    );
    fireEvent.click(ligandSurface);
    await waitFor(() =>
      expect(molstarMocks.engine.applyPocketFocus).toHaveBeenLastCalledWith(
        expect.objectContaining({
          focusCamera: false,
          displayLayers: ['surface', 'ligand-surface'],
        })
      )
    );
    fireEvent.click(pocketSurface);
    await waitFor(() =>
      expect(molstarMocks.engine.applyPocketFocus).toHaveBeenLastCalledWith(
        expect.objectContaining({
          focusCamera: false,
          displayLayers: ['ligand-surface', 'pocket-surface'],
        })
      )
    );

    expect(pocket).toHaveAttribute('data-active', 'true');
    expect(proteinSurface).toHaveAttribute('aria-pressed', 'false');
    expect(pocketSurface).toHaveAttribute('aria-pressed', 'true');
    expect(ligandSurface).toHaveAttribute('aria-pressed', 'true');
    expect(molstarMocks.engine.applyRepresentationStyle).not.toHaveBeenCalled();

    const electrostaticLegend = screen.getByTestId('synon-biomed-electrostatic-legend');
    expect(electrostaticLegend).toHaveAttribute('data-collapsed', 'true');
    expect(within(electrostaticLegend).queryByText('负电性（−）')).not.toBeInTheDocument();
    const expandElectrostaticLegend = within(electrostaticLegend).getByRole('button', {
      name: '展开电性标尺',
    });
    expect(expandElectrostaticLegend).toHaveAttribute('aria-expanded', 'false');
    fireEvent.click(expandElectrostaticLegend);
    expect(electrostaticLegend).not.toHaveAttribute('data-collapsed');
    expect(within(electrostaticLegend).getByText('负电性（−）')).toBeInTheDocument();
    expect(within(electrostaticLegend).getByText('中性（0）')).toBeInTheDocument();
    expect(within(electrostaticLegend).getByText('正电性（+）')).toBeInTheDocument();
    expect(
      within(electrostaticLegend).getByRole('img', {
        name: '负电到正电的电性渐变',
      })
    ).toBeInTheDocument();
    expect(
      within(electrostaticLegend).getByText('APBS 3.4.1 · -5 至 5 kT/e · pH 7.4 · 蛋白/Ligand 独立电势')
    ).toBeInTheDocument();

    expect(screen.getByRole('button', { name: '能量最小化' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '初始' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '卡通' })).not.toBeInTheDocument();

    expect(screen.queryByRole('button', { name: '开启鼠标选择' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '清除选择' })).not.toBeInTheDocument();
  });

  it('submits minimization to the unified backend and reloads the returned coordinates', async () => {
    const minimized = 'minimized structure';
    const sourceContent =
      'HETATM    1  C1  LIG A   1       0.000   0.000   0.000  1.00  0.00           C\nHETATM    2  C2  LIG A   1       1.000   0.000   0.000  1.00  0.00           C\nEND\n';
    expect(resolvePdbLigandResidueName(sourceContent)).toBe('LIG');
    molstarMocks.engine.getSelectedLigandResidueName.mockReturnValue('LIG');
    molstarMocks.engine.exportCurrentPose.mockReturnValueOnce({
      content: 'ATOM      1  C1  LIG A   1       0.000   0.000   0.000  1.00  0.00           C\nEND\n',
      atomCount: 1,
      selectionOnly: false,
    });
    const fetchMock = vi.fn(async () => {
      return new Response(
        JSON.stringify({
          ok: true,
          status: 'completed',
          content: minimized,
          filename: 'ligand-minimized.sdf',
          format: 'sdf',
          atom_count: 6,
          ligand_atom_count: 6,
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } }
      );
    });
    vi.stubGlobal('fetch', fetchMock);

    await act(async () => {
      await renderWithI18n(
        <SynonBiomedStructureViewer filename='ligand.sdf' content={sourceContent} rootFrameId='frame-1' />,
        'en-US'
      );
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    await waitFor(() => expect(screen.getByRole('button', { name: 'Energy minimization' })).toBeEnabled());
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Energy minimization' }));
      await vi.waitFor(() =>
        expect(molstarMocks.engine.load).toHaveBeenCalledWith(minimized, 'ligand-minimized.sdf', 'sdf')
      );
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [requestUrl, requestInit] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(String(requestUrl)).toBe('/api/frames/frame-1/structure-minimization');
    expect(requestInit.method).toBe('POST');
    expect(JSON.parse(String(requestInit.body))).toMatchObject({
      content: 'ATOM      1  C1  LIG A   1       0.000   0.000   0.000  1.00  0.00           C\nEND\n',
      filename: 'ligand-current-pose.pdb',
      force_field: 'uff',
      scope: 'ligand',
      protein_environment: 'fixed',
      ligand_residue_name: 'LIG',
      max_iterations: 200,
      tolerance: 1e-4,
    });
    expect(screen.getByText('Ligand energy minimization completed (6 heavy atoms).')).toBeInTheDocument();
  });

  it('invalidates an open ordinary-structure interaction diagram after minimization', async () => {
    const composition = {
      objects: [
        {
          id: 'protein' as const,
          kind: 'protein' as const,
          atomCount: 12,
          residueNames: ['ALA'],
        },
        {
          id: 'ligand' as const,
          kind: 'ligand' as const,
          atomCount: 2,
          residueNames: ['LIG'],
        },
      ],
      atomCount: 14,
      hasProtein: true,
      hasLigand: true,
    };
    molstarMocks.engine.load.mockResolvedValueOnce(composition).mockResolvedValueOnce({ ...composition });
    molstarMocks.engine.getSelectedLigandResidueName.mockReturnValue('LIG');
    molstarMocks.engine.exportCurrentPose.mockReturnValue({
      content: 'ATOM before minimization\nHETATM ligand before minimization\nEND\n',
      atomCount: 14,
      selectionOnly: false,
    });
    molstarMocks.engine.getPrimaryLigandDepictionSource.mockImplementation(() => ({
      residueName: 'LIG',
      interactionResidueName: 'LIG',
      molBlock: molstarMocks.engine.load.mock.calls.length > 1 ? 'ligand after' : 'ligand before',
      complexPdb:
        molstarMocks.engine.load.mock.calls.length > 1
          ? 'ATOM after minimization\nHETATM ligand after minimization\nEND\n'
          : 'ATOM before minimization\nHETATM ligand before minimization\nEND\n',
      atomCount: 2,
      hasProtein: true,
    }));

    let interactionRequests = 0;
    const fetchMock = vi.fn(async (input: RequestInfo | URL, _init?: RequestInit) => {
      const path = String(input);
      if (path.endsWith('/structure-minimization')) {
        return new Response(
          JSON.stringify({
            ok: true,
            status: 'completed',
            content: 'minimized structure',
            filename: 'complex-minimized.pdb',
            format: 'pdb',
            atom_count: 14,
            ligand_atom_count: 2,
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } }
        );
      }
      if (path.endsWith('/structure-interaction-diagram')) {
        interactionRequests += 1;
        return new Response(
          JSON.stringify({
            ok: true,
            status: 'completed',
            svg: `<svg xmlns="http://www.w3.org/2000/svg"><text>generation-${interactionRequests}</text></svg>`,
            png_base64: 'iVBORw0KGgo=',
            report: {
              engine: 'Synon 2D Interaction Engine',
              engine_release: '1.0.0',
              ligand_label: 'LIG',
              pose_label: 'Primary ligand conformer',
              hydrogen_bond_count: interactionRequests,
              salt_bridge_count: 0,
              width: 1710,
              height: 2400,
              png_dpi: 300,
              interactions: [],
            },
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } }
        );
      }
      throw new Error(`unexpected request: ${path}`);
    });
    vi.stubGlobal('fetch', fetchMock);

    await act(async () => {
      await renderWithI18n(
        <SynonBiomedStructureViewer filename='complex.pdb' content='ATOM initial' rootFrameId='frame-1' />,
        'en-US'
      );
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    fireEvent.click(await screen.findByRole('button', { name: 'Expand the left toolbar' }));
    const interactionButton = screen.getByRole('button', {
      name: '2D interactions',
    });
    await waitFor(() => expect(interactionButton).toBeEnabled());
    fireEvent.click(interactionButton);
    await screen.findByTestId('synon-biomed-molstar-interaction-diagram');
    const fullInteractionRequests = () =>
      fetchMock.mock.calls.filter(
        ([input, init]) =>
          String(input).endsWith('/structure-interaction-diagram') && !JSON.parse(String(init?.body)).report_only
      );
    expect(fullInteractionRequests()).toHaveLength(1);

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Energy minimization' }));
      await vi.waitFor(() => expect(molstarMocks.engine.load).toHaveBeenCalledTimes(2));
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    await waitFor(() =>
      expect(screen.queryByTestId('synon-biomed-molstar-interaction-diagram')).not.toBeInTheDocument()
    );
    await waitFor(() => expect(interactionButton).toBeEnabled());

    fireEvent.click(interactionButton);
    await waitFor(() => expect(fullInteractionRequests()).toHaveLength(2));
    const interactionBodies = fullInteractionRequests().map(([, init]) =>
      JSON.parse(String((init as RequestInit).body))
    );
    expect(interactionBodies.map((body) => body.content)).toEqual([
      'ATOM before minimization\nHETATM ligand before minimization\nEND\n',
      'ATOM after minimization\nHETATM ligand after minimization\nEND\n',
    ]);
  });

  it('does not allow minimization until a ligand has been selected in the 3D canvas', async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);

    await renderWithI18n(
      <SynonBiomedStructureViewer
        filename='complex.pdb'
        content='ATOM      1  CA  ALA A   1       0.000   0.000   0.000  1.00 20.00           C\nEND\n'
        rootFrameId='frame-1'
      />,
      'en-US'
    );

    const minimize = await screen.findByRole('button', {
      name: 'Energy minimization',
    });
    expect(minimize).toBeDisabled();
    fireEvent.click(minimize);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('merges minimized ligand coordinates back into the docking ensemble without replacing the receptor', async () => {
    const ensemble = [
      'REMARK 900 SYNON BIOMED DOCKING COMPLEX ENSEMBLE',
      'REMARK 900 REFERENCE LIGAND REF SOURCE ZOI CHAIN A RESIDUE 201',
      'REMARK 900 DOCKED LIGAND D01 CANDIDATE TYK2-026 RANK 1 POSE 1 AFFINITY -11.011 KCAL/MOL',
      'ATOM      1 CA   GLY A  16      27.817 -18.400  -4.985  1.00 46.96           C',
      'HETATM    2 C1   REF A 201      29.000 -24.000   0.000  1.00  0.00           C',
      'HETATM    3 C1   D01 Z 101      31.000 -28.000   2.000  1.00  0.00           C',
      'HETATM    4 O1   D01 Z 101      32.000 -28.000   2.000  1.00  0.00           O',
      'END',
      '',
    ].join('\n');
    const minimizedPose = [
      'REMARK 900 DOCKING POSE PREVIEW',
      'ATOM      1 CA   GLY A  16      27.817 -18.400  -4.985  1.00 46.96           C',
      'HETATM    3 C1   D01 Z 101      31.500 -28.500   2.500  1.00  0.00           C',
      'HETATM    4 O1   D01 Z 101      32.500 -28.500   2.500  1.00  0.00           O',
      'END',
      '',
    ].join('\n');
    molstarMocks.engine.getSelectedLigandResidueName.mockReturnValue('D01');
    const fetchMock = vi.fn(
      async () =>
        new Response(
          JSON.stringify({
            ok: true,
            status: 'completed',
            content: minimizedPose,
            filename: 'docking-current-pose-minimized.pdb',
            format: 'pdb',
            atom_count: 2,
            ligand_atom_count: 2,
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } }
        )
    );
    vi.stubGlobal('fetch', fetchMock);

    await act(async () => {
      await renderWithI18n(
        <SynonBiomedStructureViewer filename='docking.pdb' content={ensemble} rootFrameId='frame-1' />,
        'en-US'
      );
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    await waitFor(() => expect(screen.getByRole('button', { name: 'Energy minimization' })).toBeEnabled());
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Energy minimization' }));
      await vi.waitFor(() => expect(molstarMocks.engine.loadDockingEnsemble).toHaveBeenCalledTimes(2));
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    const requestCall = fetchMock.mock.calls[0];
    expect(requestCall).toBeDefined();
    const request = JSON.parse(String((requestCall![1] as RequestInit).body));
    expect(request.content).toContain('REMARK 900 DOCKING POSE PREVIEW');
    expect(request.content).toContain('ATOM      1 CA   GLY');
    expect(request.content).toContain('HETATM    3 C1   D01');
    expect(molstarMocks.engine.exportCurrentPose).not.toHaveBeenCalled();
    const reloadedEnsemble = molstarMocks.engine.loadDockingEnsemble.mock.calls[1]?.[0] as string;
    expect(reloadedEnsemble).toContain('REMARK 900 SYNON BIOMED DOCKING COMPLEX ENSEMBLE');
    expect(reloadedEnsemble).toContain('ATOM      1 CA   GLY');
    expect(reloadedEnsemble).toContain('HETATM    3 C1   D01 Z 101      31.500 -28.500   2.500');
    expect(molstarMocks.engine.load).not.toHaveBeenCalledWith(minimizedPose, expect.anything(), expect.anything());
  });

  it('recalculates an active docking electrostatic surface after ligand minimization', async () => {
    const ensemble = [
      'REMARK 900 SYNON BIOMED DOCKING COMPLEX ENSEMBLE',
      'REMARK 900 REFERENCE LIGAND REF SOURCE ZOI CHAIN A RESIDUE 201',
      'REMARK 900 DOCKED LIGAND D01 CANDIDATE TYK2-026 RANK 1 POSE 1 AFFINITY -11.011 KCAL/MOL',
      'ATOM      1 CA   GLY A  16      27.817 -18.400  -4.985  1.00 46.96           C',
      'HETATM    4 C1   REF A 201      29.000 -24.000   0.000  1.00  0.00           C',
      'HETATM    2 C1   D01 Z 101      31.000 -28.000   2.000  1.00  0.00           C',
      'HETATM    3 O1   D01 Z 101      32.000 -28.000   2.000  1.00  0.00           O',
      'END',
    ].join('\n');
    const minimizedPose = [
      'ATOM      1 CA   GLY A  16      27.817 -18.400  -4.985  1.00 46.96           C',
      'HETATM    2 C1   D01 Z 101      31.500 -28.500   2.500  1.00  0.00           C',
      'HETATM    3 O1   D01 Z 101      32.500 -28.500   2.500  1.00  0.00           O',
      'END',
    ].join('\n');
    molstarMocks.engine.getSelectedLigandResidueName.mockReturnValue('D01');
    molstarMocks.engine.getElectrostaticInputSource
      .mockReturnValueOnce({
        content: 'ATOM receptor before minimization\nEND\n',
        ligands: [{ key: 'D01', molBlock: 'D01 before minimization', atomCount: 2 }],
        atomCount: 3,
      })
      .mockReturnValueOnce({
        content: 'ATOM receptor after minimization\nEND\n',
        ligands: [{ key: 'D01', molBlock: 'D01 after minimization', atomCount: 2 }],
        atomCount: 3,
      });
    electrostaticTransportMocks.decode
      .mockResolvedValueOnce({ protein: 'protein-before-dx', ligand: 'ligand-before-dx' })
      .mockResolvedValueOnce({ protein: 'protein-after-dx', ligand: 'ligand-after-dx' });
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      if (String(input).includes('/structure-electrostatic-map')) {
        return new Response(JSON.stringify(electrostaticResponse()), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        });
      }
      if (String(input).includes('/structure-minimization')) {
        return new Response(
          JSON.stringify({
            ok: true,
            status: 'completed',
            content: minimizedPose,
            filename: 'docking-current-pose-minimized.pdb',
            format: 'pdb',
            atom_count: 2,
            ligand_atom_count: 2,
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } }
        );
      }
      return new Response('not found', { status: 404 });
    });
    vi.stubGlobal('fetch', fetchMock);

    await act(async () => {
      await renderWithI18n(
        <SynonBiomedStructureViewer
          filename='docking.pdb'
          content={ensemble}
          rootFrameId='frame-electrostatic-minimize'
        />,
        'en-US'
      );
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    fireEvent.click(await screen.findByRole('button', { name: 'Ligand surface' }));
    await waitFor(() => expect(molstarMocks.engine.setElectrostaticPotentials).toHaveBeenCalledTimes(1));

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Energy minimization' }));
      await vi.waitFor(() =>
        expect(molstarMocks.engine.applyDockingPocket).toHaveBeenLastCalledWith(
          'D01',
          expect.objectContaining({ displayLayers: ['ligand-surface'], focusCamera: false })
        )
      );
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(molstarMocks.engine.loadDockingEnsemble).toHaveBeenCalledTimes(2);
    const electrostaticBodies = fetchMock.mock.calls
      .filter(([input]) => String(input).includes('/structure-electrostatic-map'))
      .map(([, init]) => JSON.parse(String((init as RequestInit).body)));
    expect(electrostaticBodies.map((body) => body.ligand_mol_block)).toEqual([
      'D01 before minimization',
      'D01 after minimization',
    ]);
    expect(molstarMocks.engine.loadDockingEnsemble.mock.calls[1]?.[4]).toEqual(
      expect.objectContaining({ displayLayers: [] })
    );
    expect(molstarMocks.engine.applyDockingPocket).toHaveBeenLastCalledWith(
      'D01',
      expect.objectContaining({ displayLayers: ['ligand-surface'], focusCamera: false })
    );
    expect(molstarMocks.engine.setElectrostaticPotentials.mock.invocationCallOrder[1]).toBeLessThan(
      molstarMocks.engine.applyDockingPocket.mock.invocationCallOrder.at(-1)!
    );
  });

  it('uses the active conversation as the frame fallback for workspace previews', async () => {
    molstarMocks.engine.getSelectedLigandResidueName.mockReturnValue('LIG');
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      expect(String(input)).toBe('/api/frames/conversation-frame/structure-minimization');
      return new Response(
        JSON.stringify({
          ok: true,
          status: 'completed',
          content: 'conversation minimized structure',
          filename: 'ligand-minimized.sdf',
          format: 'sdf',
          atom_count: 2,
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } }
      );
    });
    vi.stubGlobal('fetch', fetchMock);

    await act(async () => {
      await renderWithI18n(
        <SynonBiomedStructureViewer
          filename='ligand.sdf'
          content='initial structure'
          conversationId='conversation-frame'
        />,
        'en-US'
      );
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    await waitFor(() => expect(screen.getByRole('button', { name: 'Energy minimization' })).toBeEnabled());
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Energy minimization' }));
      await vi.waitFor(() =>
        expect(molstarMocks.engine.load).toHaveBeenCalledWith(
          'conversation minimized structure',
          'ligand-minimized.sdf',
          'sdf'
        )
      );
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});

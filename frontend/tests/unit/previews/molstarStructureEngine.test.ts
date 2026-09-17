import { describe, expect, it, vi } from 'vitest';
import {
  applyDockingVisibilitySwap,
  createLatestDockingRefinementQueue,
  createPoseAtoms,
  queueDockingRefinement,
  createCompletePocketInteractionProps,
  createPocketInteractionProps,
  createMolstarViewportSpec,
  filterStructureLociByResidueName,
  isProteinAtom,
  POCKET_INTERACTION_BRIDGE_NAMES,
  POCKET_INTERACTION_PROVIDER_NAMES,
  POCKET_INTERACTION_VISUAL_GROUPS,
  POCKET_INTERACTION_REPRESENTATION_TYPE_PARAMS,
  POCKET_DISTANCE_MARKER_LIMIT,
  STANDARD_LIGAND_STICK_TYPE_PARAMS,
  POCKET_LIGAND_STICK_TYPE_PARAMS,
  POCKET_SURFACE_TYPE_PARAMS,
  STANDARD_PROTEIN_SURFACE_TYPE_PARAMS,
  ELECTROSTATIC_SURFACE_TYPE_PARAMS,
  ELECTROSTATIC_NEUTRAL_COLOR,
  INITIAL_REPRESENTATION_PRESET_PARAMS,
  isLigandLociClick,
  normalizeStructureElementClickLoci,
  resolvePocketLigandLoci,
  resolveAtomicHierarchyLocation,
  resolveAtomicWorldPosition,
  selectPrimaryPocketLigandLoci,
  resolveElectrostaticColorTheme,
  resolveElectrostaticColorParams,
  isElectrostaticSurfaceRepresentation,
  isStructureLigandCarbonColorRepresentation,
  resolveSurfaceComponentType,
  resolveElectrostaticSurfaceVolumeRole,
  resolveDockingLigandElectrostaticSurfaces,
  shouldReuseDockingLigandVisual,
  shouldFocusPocketCamera,
  shouldIncludeProteinContextForSurface,
  DOCKING_PROTEIN_VISIBILITY_TAGS,
  isDockingFixedProteinHidden,
  resolveDockingProjectionScope,
  resolvePocketInteractionRenderVisibility,
  resolvePocketLayerPlan,
  isStructureDoubleClick,
  focusStructureLociFromDoubleClick,
} from '@/renderer/pages/conversation/Preview/components/viewers/molstarStructureEngine';
import {
  ELECTROSTATIC_COLOR_STOPS,
  resolveSurfaceColorSettings,
} from '@/renderer/pages/conversation/Preview/components/viewers/molstarElectrostaticTheme';
import { Column } from 'molstar/lib/mol-data/db';
import { OrderedSet } from 'molstar/lib/mol-data/int';
import { InteractionsProvider } from 'molstar/lib/mol-model-props/computed/interactions';
import { AtomPartialCharge } from 'molstar/lib/mol-model-formats/structure/property/partial-charge';
import { PluginBehaviors } from 'molstar/lib/mol-plugin/behavior';
import { Bond, StructureElement } from 'molstar/lib/mol-model/structure';
import type { MoleculeType } from 'molstar/lib/mol-model/structure/model/types';
import type { Structure, Unit } from 'molstar/lib/mol-model/structure';
import {
  applyDockingLigandBondDefinitions,
  createDockingLigandShapeData,
  formatDockingLigandMolBlock,
  inferDockingAromaticCycles,
  resolveDockingBondOrder,
  resolveDockingLigandElement,
  resolveDockingLigandAtomRadius,
} from '@/renderer/pages/conversation/Preview/components/viewers/molstarDockingLigandShape';
import { selectMolstarInteractionFeatureAnchors } from '@/renderer/pages/conversation/Preview/components/viewers/molstarInteractionAnchors';
import { Color } from 'molstar/lib/mol-util/color';
import { Vec3 } from 'molstar/lib/mol-math/linear-algebra';
import { createMolstarInteractionStrengthLabelData } from '@/renderer/pages/conversation/Preview/components/viewers/molstarInteractionStrengthLabels';
import { isDisplayLigandResidueName } from '@/renderer/pages/conversation/Preview/components/viewers/structureComposition';

describe('3D interaction strength labels', () => {
  const records = [
    {
      kind: 'hydrogen-bond',
      strength_index: 0.82,
      strength_level: 'strong',
      ligand_position: [1, 2, 3],
      protein_position: [3, 4, 5],
      ligand_label: 'TYK2-026',
      residue: { name: 'LYS', number: 642, chain: 'A' },
    },
    {
      kind: 'hydrophobic',
      strength_index: 0.61,
      ligand_position: [0, 0, 0],
      protein_position: [2, 0, 0],
      ligand_label: 'TYK2-026',
      residue: { name: 'VAL', number: 690, chain: 'A' },
    },
  ];

  it('places a bounded relative strength value at the interaction midpoint', () => {
    const data = createMolstarInteractionStrengthLabelData(records, {
      hydrophobic: false,
    });

    expect(data.labels).toHaveLength(1);
    expect(data.labels[0]).toMatchObject({ text: '0.82', position: [2, 3, 4] });
    expect(data.labels[0].tooltip).toContain('relative strength 0.82');
  });

  it('uses the same ON/OFF visibility contract as the interaction lines', () => {
    const data = createMolstarInteractionStrengthLabelData(records, {
      'hydrogen-bonds': false,
      hydrophobic: true,
    });

    expect(data.labels.map((label) => label.text)).toEqual(['0.61']);
  });

  it('filters weak hydrogen-bond strength labels through the weak-hydrogen toggle', () => {
    const weakHydrogenBond = {
      kind: 'weak-hydrogen-bond',
      strength_index: 0.45,
      ligand_position: [1, 1, 1],
      protein_position: [3, 1, 1],
      residue: { name: 'SER', number: 17, chain: 'A' },
    };

    expect(
      createMolstarInteractionStrengthLabelData([weakHydrogenBond], {
        'weak-hydrogen-bonds': false,
      }).labels
    ).toEqual([]);
    expect(
      createMolstarInteractionStrengthLabelData([weakHydrogenBond], {
        'weak-hydrogen-bonds': true,
      }).labels
    ).toHaveLength(1);
  });

  it('rejects missing coordinates instead of inventing a label position', () => {
    const data = createMolstarInteractionStrengthLabelData([
      {
        kind: 'hydrogen-bond',
        strength_index: 0.9,
        ligand_position: null,
        protein_position: [1, 2, 3],
      },
    ]);

    expect(data.labels).toEqual([]);
  });
});

describe('3D interaction feature anchors', () => {
  it('marks multi-atom interaction centers once without adding false single-atom targets', () => {
    expect(
      selectMolstarInteractionFeatureAnchors([
        {
          key: 'ligand-ring:2',
          position: [1, 2, 3],
          memberCount: 6,
          interactionType: 2,
          tooltip: 'Cation Pi interaction center',
        },
        {
          key: 'protein-atom:2',
          position: [4, 5, 6],
          memberCount: 1,
          interactionType: 2,
          tooltip: 'Cation Pi interaction center',
        },
        {
          key: 'ligand-ring:2',
          position: [1, 2, 3],
          memberCount: 6,
          interactionType: 2,
          tooltip: 'Cation Pi interaction center',
        },
      ])
    ).toEqual([
      expect.objectContaining({
        key: 'ligand-ring:2',
        position: [1, 2, 3],
        interactionType: 2,
      }),
    ]);
  });
});

describe('docking ligand element normalization', () => {
  it('overlays authoritative CIF bond orders and aromatic flags by atom name', () => {
    const atom = (label: string, position: readonly [number, number, number]) => ({
      position,
      element: 'C',
      label,
      residueName: 'LIG',
      formalCharge: 0,
      carbonColor: Color(0x0f766e),
    });
    const data = applyDockingLigandBondDefinitions(
      {
        atoms: [atom('C1', [0, 0, 0]), atom('C2', [1.4, 0, 0]), atom('C3', [2.8, 0, 0])],
        bonds: [
          { atomA: 0, atomB: 1, aromatic: false, order: 1 },
          { atomA: 1, atomB: 2, aromatic: false, order: 1 },
        ],
        aromaticCycles: [],
        carbonColor: Color(0x0f766e),
      },
      [
        { atomIdA: 'C1', atomIdB: 'C2', order: 2, aromatic: true },
        { atomIdA: 'C2', atomIdB: 'C3', order: 1, aromatic: true },
        { atomIdA: 'C3', atomIdB: 'H1', order: 1, aromatic: false },
      ]
    );

    expect(data.bonds).toEqual([
      { atomA: 0, atomB: 1, aromatic: true, chemicalAromatic: true, order: 2 },
      { atomA: 1, atomB: 2, aromatic: true, chemicalAromatic: true, order: 1 },
    ]);
  });

  it('serializes parsed ligand atoms, chemistry-safe bonds and formal charges for RDKit', () => {
    const molBlock = formatDockingLigandMolBlock(
      {
        atoms: [
          {
            position: [0, 0, 0],
            element: 'N',
            label: 'N1',
            residueName: 'A1JB1',
            formalCharge: 1,
            carbonColor: Color(0x0f766e),
          },
          {
            position: [1.4, 0, 0],
            element: 'C',
            label: 'C1',
            residueName: 'A1JB1',
            formalCharge: 0,
            carbonColor: Color(0x0f766e),
          },
        ],
        bonds: [{ atomA: 0, atomB: 1, aromatic: true, order: 1 }],
        aromaticCycles: [],
        carbonColor: Color(0x0f766e),
      },
      'A1JB1\nignored'
    );

    expect(molBlock).toContain('A1JB1 ignored\n  Synon Biomed');
    expect(molBlock).toContain('  2  1  0  0  0  0            999 V2000');
    expect(molBlock).toContain('  1  2  1  0  0  0  0');
    expect(molBlock).toContain('M  CHG  1   1   1');
    expect(molBlock).toMatch(/M  END\n$/);
  });

  it('preserves only parser-declared aromatic chemistry in the RDKit boundary', () => {
    const baseAtom = {
      position: [0, 0, 0] as const,
      element: 'C',
      label: 'C',
      residueName: 'LIG',
      formalCharge: 0,
      carbonColor: Color(0x0f766e),
    };
    const visualInference = formatDockingLigandMolBlock({
      atoms: [baseAtom, { ...baseAtom, position: [1.4, 0, 0] }],
      bonds: [{ atomA: 0, atomB: 1, aromatic: true, order: 1 }],
      aromaticCycles: [[0, 1]],
      carbonColor: Color(0x0f766e),
    });
    const declaredChemistry = formatDockingLigandMolBlock({
      atoms: [baseAtom, { ...baseAtom, position: [1.4, 0, 0] }],
      bonds: [
        {
          atomA: 0,
          atomB: 1,
          aromatic: true,
          chemicalAromatic: true,
          order: 1,
        },
      ],
      aromaticCycles: [[0, 1]],
      carbonColor: Color(0x0f766e),
    });
    const coordinateFileChemistry = formatDockingLigandMolBlock(
      {
        atoms: [baseAtom, { ...baseAtom, position: [1.4, 0, 0] }],
        bonds: [
          {
            atomA: 0,
            atomB: 1,
            aromatic: true,
            chemicalAromatic: true,
            order: 1,
          },
        ],
        aromaticCycles: [[0, 1]],
        carbonColor: Color(0x0f766e),
      },
      'LIG',
      { preserveAromaticBonds: false }
    );

    expect(visualInference).toContain('  1  2  1  0  0  0  0');
    expect(declaredChemistry).toContain('  1  2  4  0  0  0  0');
    expect(coordinateFileChemistry).toContain('  1  2  1  0  0  0  0');
  });

  it('omits unbonded hydrogens from coordinate-file ligand mol blocks', () => {
    const carbon = {
      position: [0, 0, 0] as const,
      element: 'C',
      label: 'C1',
      residueName: 'LIG',
      formalCharge: 0,
      carbonColor: Color(0x0f766e),
    };
    const oxygen = {
      ...carbon,
      position: [1.2, 0, 0] as const,
      element: 'O',
      label: 'O1',
    };
    const disconnectedHydrogen = {
      ...carbon,
      position: [4, 0, 0] as const,
      element: 'H',
      label: 'H1',
    };

    const molBlock = formatDockingLigandMolBlock({
      atoms: [carbon, oxygen, disconnectedHydrogen],
      bonds: [{ atomA: 0, atomB: 1, aromatic: false, order: 2 }],
      aromaticCycles: [],
      carbonColor: Color(0x0f766e),
    });

    expect(molBlock).toContain('  2  1  0  0  0  0            999 V2000');
    expect(molBlock).not.toContain(' H   0  0');
  });

  it('uses ligand atom names to keep carbon atoms and carbon bonds on the selected row color', () => {
    expect(resolveDockingLigandElement('C17', '')).toBe('C');
    expect(resolveDockingLigandElement(' C4 ', 'X')).toBe('C');
    expect(resolveDockingLigandElement('N2', 'C')).toBe('N');
    expect(resolveDockingLigandElement('CL1', 'C')).toBe('CL');
    expect(resolveDockingLigandElement('BR1', 'C')).toBe('BR');
  });

  it('matches the SDF stick-like atom-to-bond proportions instead of oversized ligand spheres', () => {
    expect(resolveDockingLigandAtomRadius('C')).toBe(0.17);
    expect(resolveDockingLigandAtomRadius('N')).toBe(0.18);
    expect(resolveDockingLigandAtomRadius('H')).toBe(0.1);
  });

  it('preserves declared multiple bonds and safely infers short carbonyl and nitrile bonds', () => {
    expect(resolveDockingBondOrder('C', 'O', 1.23, 3, 1, 1)).toBe(2);
    expect(resolveDockingBondOrder('C', 'N', 1.16, 2, 1, 1)).toBe(3);
    expect(resolveDockingBondOrder('C', 'C', 1.52, 4, 4, 1)).toBe(1);
    expect(resolveDockingBondOrder('C', 'C', 1.52, 4, 4, 2)).toBe(2);
  });

  it('infers planar aromatic rings when PDB bonds do not carry aromatic flags', () => {
    const atoms = Array.from({ length: 6 }, (_, index) => {
      const angle = (Math.PI * 2 * index) / 6;
      return {
        position: [Math.cos(angle) * 1.4, Math.sin(angle) * 1.4, 0] as const,
        element: 'C',
      };
    });
    const bonds = Array.from({ length: 6 }, (_, index) => ({
      atomA: index,
      atomB: (index + 1) % 6,
      aromatic: false,
    }));

    expect(inferDockingAromaticCycles(atoms, bonds)).toEqual([[0, 1, 2, 3, 4, 5]]);
    expect(
      inferDockingAromaticCycles(
        atoms.map((atom, index) => ({
          position: [atom.position[0], atom.position[1], index % 2 === 0 ? 0.6 : -0.6] as const,
          element: atom.element,
        })),
        bonds
      )
    ).toEqual([]);
  });
});

const createChargeColumn = (values: number[]) => ({
  rowCount: values.length,
  isDefined: true,
  value: (index: number) => values[index],
});

const createStructureWithCharges = (
  formalCharges: number[],
  moleculeTypes: readonly MoleculeType[] = [0 as MoleculeType]
) => {
  const model = {
    _staticPropertyData: {},
    customProperties: { has: () => true },
    atomicHierarchy: {
      atoms: { pdbx_formal_charge: createChargeColumn(formalCharges) },
      derived: { residue: { moleculeType: moleculeTypes } },
    },
  };
  return {
    models: [model],
    units: [
      {
        kind: 0,
        elements: formalCharges.map((_, index) => index),
        residueIndex: formalCharges.map((_, index) => Math.min(index, moleculeTypes.length - 1)),
        model,
      },
    ],
  } as unknown as Structure;
};

const createPocketLigandUnit = (
  id: number,
  residueName: string,
  altId: string,
  occupancy: number,
  atomCount: number
) => {
  const elements = Array.from({ length: atomCount }, (_, index) => index);
  return {
    kind: 0,
    id,
    elements,
    residueIndex: elements.map(() => 0),
    model: {
      atomicHierarchy: {
        atoms: {
          auth_comp_id: { value: () => residueName },
          label_comp_id: { value: () => residueName },
          label_alt_id: { value: () => altId },
        },
      },
      atomicConformation: {
        occupancy: { value: () => occupancy },
      },
    },
  } as unknown as Unit;
};

describe('molstarStructureEngine protein surface and ligand representation', () => {
  it('resolves CIF hierarchy metadata through the selected model atom element', () => {
    const residueIndex = Array<number>(24).fill(-1);
    const chainIndex = Array<number>(24).fill(-1);
    residueIndex[23] = 701;
    chainIndex[23] = 4;

    expect(resolveAtomicHierarchyLocation({ elements: [23], residueIndex, chainIndex }, 0)).toEqual({
      atomIndex: 23,
      residueIndex: 701,
      chainIndex: 4,
    });
  });

  it('preserves hierarchy chain, insertion, alternate-location, and occupancy metadata for PDB serialization', () => {
    const residueIndex = Array<number>(24).fill(-1);
    const chainIndex = Array<number>(24).fill(-1);
    residueIndex[23] = 0;
    chainIndex[23] = 0;
    const unit = {
      kind: 0,
      id: 7,
      chainGroupId: 3,
      elements: [23],
      residueIndex,
      chainIndex,
      conformation: {
        operator: { instanceId: 'ASM-1', name: '1_555' },
        position: (_atomIndex: number, target: number[]) => {
          target[0] = 11;
          target[1] = 12;
          target[2] = 13;
          return target;
        },
      },
      model: {
        atomicHierarchy: {
          atoms: {
            auth_atom_id: { value: () => 'CA' },
            label_atom_id: { value: () => 'CA' },
            auth_comp_id: { value: () => 'ALA' },
            label_comp_id: { value: () => 'ALA' },
            label_alt_id: { value: () => 'B' },
            type_symbol: { value: () => 'C' },
          },
          residues: {
            auth_seq_id: { value: () => 42 },
            label_seq_id: { value: () => 41 },
            pdbx_PDB_ins_code: { value: () => 'C' },
          },
          chains: {
            auth_asym_id: { value: () => 'CHAIN_A' },
            label_asym_id: { value: () => 'A' },
          },
          derived: { residue: { moleculeType: [5 as MoleculeType] } },
        },
        atomicConformation: {
          occupancy: { value: () => 0.75 },
          B_iso_or_equiv: { value: () => 18.5 },
        },
      },
    } as unknown as Unit.Atomic;
    const structure = {} as Structure;
    const selection = StructureElement.Loci(structure, [
      { unit, indices: OrderedSet.ofBounds(0, 1) as OrderedSet<StructureElement.UnitIndex> },
    ]);

    expect(createPoseAtoms(structure, selection)).toEqual([
      expect.objectContaining({
        chainId: 'CHAIN_A',
        chainKey: '["ASM-1",3,"CHAIN_A"]',
        residueNumber: 42,
        alternateLocation: 'B',
        insertionCode: 'C',
        occupancy: 0.75,
        temperatureFactor: 18.5,
        x: 11,
        y: 12,
        z: 13,
      }),
    ]);
  });

  it('uses the same solvent/additive exclusion for PDB and mmCIF compound lists', () => {
    expect(isDisplayLigandResidueName('A1JB1')).toBe(true);
    expect(isDisplayLigandResidueName('EDO')).toBe(false);
    expect(isDisplayLigandResidueName('GOL')).toBe(false);
  });

  it('selects one primary ligand conformer by atom count and occupancy', () => {
    const structure = {} as Structure;
    const conformerA = createPocketLigandUnit(1, 'A1JB1', 'A', 0.6, 44);
    const conformerB = createPocketLigandUnit(2, 'A1JB1', 'B', 0.206, 44);
    const additive = createPocketLigandUnit(3, 'EDO', '', 1, 4);
    const loci = StructureElement.Loci(
      structure,
      [conformerA, conformerB, additive].map((unit) => ({
        unit,
        indices: OrderedSet.ofSortedArray(unit.elements as StructureElement.UnitIndex[]),
      }))
    );

    const primary = selectPrimaryPocketLigandLoci(loci);

    expect(StructureElement.Loci.size(primary)).toBe(44);
    expect(primary.elements).toHaveLength(1);
    expect(primary.elements[0]?.unit.id).toBe(1);
  });

  it('lets the ligand click finish before starting expensive docking refinement', async () => {
    const states: string[] = [];
    const refine = vi.fn(async () => undefined);

    const refinement = queueDockingRefinement(refine, (state) => states.push(state));

    expect(states).toEqual(['pending']);
    expect(refine).not.toHaveBeenCalled();

    await Promise.resolve();
    expect(refine).toHaveBeenCalledOnce();

    await refinement;
    expect(states).toEqual(['pending', 'ready']);
  });

  it('propagates a docking-refinement failure after publishing its failed state', async () => {
    const failure = new Error('surface graph failed');
    const states: string[] = [];

    await expect(
      queueDockingRefinement(
        async () => {
          throw failure;
        },
        (state) => states.push(state)
      )
    ).rejects.toBe(failure);
    expect(states).toEqual(['pending', 'failed']);
  });

  it('debounces pocket refinement so rapid ligand clicks compute only the latest selection', async () => {
    vi.useFakeTimers();
    try {
      const queue = createLatestDockingRefinementQueue(120);
      const first = vi.fn(async () => undefined);
      const latest = vi.fn(async () => undefined);
      const states: string[] = [];

      const firstRefinement = queue.queue(first, (state) => states.push(`first:${state}`));
      const latestRefinement = queue.queue(latest, (state) => states.push(`latest:${state}`));
      expect(first).not.toHaveBeenCalled();
      expect(latest).not.toHaveBeenCalled();

      await vi.advanceTimersByTimeAsync(120);
      expect(first).not.toHaveBeenCalled();
      expect(latest).toHaveBeenCalledOnce();
      expect(states).toEqual(['first:pending', 'latest:pending', 'latest:ready']);
      await expect(firstRefinement).resolves.toBeUndefined();
      await expect(latestRefinement).resolves.toBeUndefined();
    } finally {
      vi.useRealTimers();
    }
  });

  it('retires an in-flight docking refinement without publishing stale completion state', async () => {
    vi.useFakeTimers();
    try {
      const queue = createLatestDockingRefinementQueue(120);
      let markStarted!: () => void;
      let release!: () => void;
      const started = new Promise<void>((resolve) => {
        markStarted = resolve;
      });
      const blocked = new Promise<void>((resolve) => {
        release = resolve;
      });
      const states: string[] = [];
      const refinement = queue.queue(
        async () => {
          markStarted();
          await blocked;
          return 'stale-result';
        },
        (state) => states.push(state)
      );

      await vi.advanceTimersByTimeAsync(120);
      await started;
      queue.cancel();
      release();

      await expect(refinement).resolves.toBeUndefined();
      expect(states).toEqual(['pending']);
    } finally {
      vi.useRealTimers();
    }
  });

  it('reveals a prepared docking graph before hiding the previous graph', () => {
    const updates: Array<[string, boolean]> = [];

    applyDockingVisibilitySwap(['old-trajectory'], ['next-trajectory'], (ref, hidden) => {
      updates.push([ref, hidden]);
    });

    expect(updates).toEqual([
      ['next-trajectory', false],
      ['old-trajectory', true],
    ]);
  });

  it('reuses a per-ligand graph when a row is toggled with the same color', () => {
    expect(shouldReuseDockingLigandVisual(0x0f766e, 0x0f766e, true)).toBe(true);
    expect(shouldReuseDockingLigandVisual(0x0f766e, 0xea580c, true)).toBe(false);
    expect(shouldReuseDockingLigandVisual(0x0f766e, 0x0f766e, false)).toBe(false);
  });

  it('selects one cached docking ligand without rebuilding the receptor', () => {
    const structure = {} as Structure;
    const unit = {
      id: 7,
      kind: 0,
      elements: [0, 1, 2, 3],
      model: {
        atomicHierarchy: {
          atoms: {
            auth_comp_id: {
              value: (index: number) => ['D01', 'D01', 'D02', 'D02'][index],
            },
            label_comp_id: { value: () => '' },
          },
        },
      },
    } as unknown as Unit.Atomic;
    const allLigands = StructureElement.Loci(structure, [{ unit, indices: OrderedSet.ofBounds(0, 4) }]);

    const selected = filterStructureLociByResidueName(allLigands, 'd02');

    expect(StructureElement.Loci.size(selected)).toBe(2);
    expect(OrderedSet.has(selected.elements[0].indices, 2)).toBe(true);
    expect(OrderedSet.has(selected.elements[0].indices, 3)).toBe(true);
    expect(selected.structure).toBe(structure);
  });

  it('focuses the camera only when a pocket view explicitly permits it', () => {
    expect(shouldFocusPocketCamera({})).toBe(true);
    expect(shouldFocusPocketCamera({ focusCamera: true })).toBe(true);
    expect(shouldFocusPocketCamera({ focusCamera: false })).toBe(false);
  });

  it('keeps Molstar world coordinates from applying a symmetry operator twice', () => {
    const operatorMatrix = [1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 10, 0, 0, 1];
    const unit = {
      id: 1,
      kind: 0,
      elements: [0],
      conformation: {
        position: (_atomIndex: number, target: number[]) => {
          target[0] = 11;
          target[1] = 2;
          target[2] = 3;
          return target;
        },
        operator: { matrix: operatorMatrix },
      },
      model: {
        atomicHierarchy: {
          atoms: {
            label_atom_id: { value: () => 'C1' },
            auth_atom_id: { value: () => 'C1' },
            auth_comp_id: { value: () => 'LIG' },
            label_comp_id: { value: () => 'LIG' },
            type_symbol: { value: () => 'C' },
            pdbx_formal_charge: { isDefined: false, value: () => 0 },
          },
        },
      },
      bonds: {
        offset: Int32Array.from([0, 0]),
        b: Int32Array.from([]),
        edgeProps: { flags: Int32Array.from([]), order: Int8Array.from([]) },
      },
    } as unknown as Unit.Atomic;
    const structure = {} as Structure;
    const loci = StructureElement.Loci(structure, [
      { unit, indices: OrderedSet.ofBounds(0, 1) as OrderedSet<StructureElement.UnitIndex> },
    ]);

    expect(createDockingLigandShapeData(loci, Color(0x0f766e)).atoms[0]?.position).toEqual([11, 2, 3]);
    expect(resolveAtomicWorldPosition(unit, 0, Vec3())).toEqual([11, 2, 3]);
  });

  it('keeps polar hydrogens while hiding non-polar hydrogens in the initial view', () => {
    expect(INITIAL_REPRESENTATION_PRESET_PARAMS).toMatchObject({
      ignoreHydrogens: true,
      ignoreHydrogensVariant: 'non-polar',
    });
  });

  it('uses the whole ligand for standalone surface modes and keeps all as a safe fallback', () => {
    expect(resolveSurfaceComponentType(true, true)).toBe('protein');
    expect(resolveSurfaceComponentType(false, true)).toBe('ligand');
    expect(resolveSurfaceComponentType(false, false)).toBe('all');
  });

  it('uses a restrained molecular surface that leaves room for the unchanged ligand sticks', () => {
    expect(STANDARD_PROTEIN_SURFACE_TYPE_PARAMS).toEqual({
      probeRadius: 1.4,
      alpha: 0.7,
      quality: 'medium',
    });
  });

  it('keeps a translucent pocket surface with protein context instead of an isolated opaque blob', () => {
    expect(POCKET_SURFACE_TYPE_PARAMS).toEqual({
      probeRadius: 1.4,
      alpha: 0.7,
      quality: 'medium',
    });
    expect(shouldIncludeProteinContextForSurface('pocket')).toBe(true);
    expect(shouldIncludeProteinContextForSurface('ligand')).toBe(true);
    expect(shouldIncludeProteinContextForSurface('protein')).toBe(false);
  });

  it('keeps compatible electrostatic surfaces in the pocket layer plan', () => {
    expect(resolvePocketLayerPlan(['surface', 'ligand-surface', 'surface'])).toEqual({
      layers: ['surface', 'ligand-surface'],
      hasBallAndStick: false,
      hasLine: false,
      hasProteinSurface: true,
      hasPocketSurface: false,
      hasLigandSurface: true,
      hasProteinOverlay: true,
    });
    expect(resolvePocketLayerPlan(['pocket-surface'])).toMatchObject({
      hasPocketSurface: true,
      hasProteinSurface: false,
      hasProteinOverlay: false,
    });
  });

  it('uses the full receptor projection only when the protein surface is requested', () => {
    expect(resolveDockingProjectionScope([])).toBe('pocket');
    expect(resolveDockingProjectionScope(['pocket-surface'])).toBe('pocket');
    expect(resolveDockingProjectionScope(['ligand-surface'])).toBe('pocket');
    expect(resolveDockingProjectionScope(['surface'])).toBe('structure');
    expect(resolveDockingProjectionScope(['surface', 'ligand-surface'])).toBe('structure');
  });

  it('keeps native interactions visible while report-derived strength labels are present', () => {
    expect(resolvePocketInteractionRenderVisibility(0)).toEqual({
      nativeHidden: false,
      strengthHidden: true,
    });
    expect(resolvePocketInteractionRenderVisibility(2)).toEqual({
      nativeHidden: false,
      strengthHidden: false,
    });
  });

  it('keeps the fixed receptor hidden only for a full-receptor surface projection or protein OFF', () => {
    expect(isDockingFixedProteinHidden(true, 'overview')).toBe(false);
    expect(isDockingFixedProteinHidden(true, 'pocket')).toBe(false);
    expect(isDockingFixedProteinHidden(true, 'structure')).toBe(true);
    expect(isDockingFixedProteinHidden(false, 'overview')).toBe(true);
    expect(isDockingFixedProteinHidden(false, 'pocket')).toBe(true);
    expect(isDockingFixedProteinHidden(false, 'structure')).toBe(true);
  });

  it('assigns every receptor-owned docking layer to the protein visibility control', () => {
    expect(DOCKING_PROTEIN_VISIBILITY_TAGS).toEqual(
      expect.arrayContaining([
        'synon-biomed-pocket-layer-protein-surface',
        'synon-biomed-pocket-layer-pocket-surface',
        'synon-biomed-layer-protein-surface',
        'synon-biomed-layer-pocket-surface',
        'synon-biomed-pocket-interactions',
        'synon-biomed-pocket-interaction-strengths',
      ])
    );
    expect(DOCKING_PROTEIN_VISIBILITY_TAGS).not.toContain('synon-biomed-pocket-interaction-strength-lines');
    expect(DOCKING_PROTEIN_VISIBILITY_TAGS).not.toContain('synon-biomed-pocket-ligand');
  });

  it('keeps the ligand at the native Mol* stick scale instead of enlarging its atom spheres', () => {
    expect(STANDARD_LIGAND_STICK_TYPE_PARAMS).toEqual({
      sizeFactor: 0.15,
      sizeAspectRatio: 2 / 3,
      ignoreHydrogens: true,
      ignoreHydrogensVariant: 'all',
    });
  });

  it('uses the same hydrogen-free stick contract for pocket ligands from every structure format', () => {
    expect(POCKET_LIGAND_STICK_TYPE_PARAMS).toEqual(STANDARD_LIGAND_STICK_TYPE_PARAMS);
    expect(POCKET_LIGAND_STICK_TYPE_PARAMS).toMatchObject({
      ignoreHydrogens: true,
      ignoreHydrogensVariant: 'all',
    });
  });

  it('anchors pocket interaction lines to the same visible heavy-atom model', () => {
    expect(POCKET_INTERACTION_REPRESENTATION_TYPE_PARAMS).toMatchObject({
      ignoreHydrogens: true,
      ignoreHydrogensVariant: 'all',
      includeParent: true,
      parentDisplay: 'between',
    });
  });

  it('uses the shared translucent surface opacity with the sampled reference endpoints', () => {
    expect(ELECTROSTATIC_SURFACE_TYPE_PARAMS).toEqual({
      probeRadius: 1.4,
      alpha: 0.7,
      quality: 'medium',
    });
    expect(resolveElectrostaticColorParams('partial-charge')).toEqual({
      domain: [-1, 1],
      list: { kind: 'interpolate', colors: [0xd70201, 0xfdfdfd, 0x010efd] },
    });
    expect(resolveElectrostaticColorParams('partial-charge', 0.25)).toEqual({
      domain: [-0.25, 0.25],
      list: { kind: 'interpolate', colors: [0xd70201, 0xfdfdfd, 0x010efd] },
    });
    expect(resolveElectrostaticColorParams('formal-charge')).toEqual({
      domain: [-3, 3],
      list: { kind: 'set', colors: [0xd70201, 0xfdfdfd, 0x010efd] },
    });
    expect(resolveElectrostaticColorParams('residue-charge')).toBeUndefined();
    expect(resolveElectrostaticColorParams('no-charge-data')).toBeUndefined();
    expect(ELECTROSTATIC_NEUTRAL_COLOR).toBe(0xfdfdfd);
  });

  it('uses one electrostatic color contract for every user-facing surface scope', () => {
    expect(isElectrostaticSurfaceRepresentation('surface')).toBe(true);
    expect(isElectrostaticSurfaceRepresentation('pocket-surface')).toBe(true);
    expect(isElectrostaticSurfaceRepresentation('ligand-surface')).toBe(true);
    expect(isElectrostaticSurfaceRepresentation('line')).toBe(false);
    expect(ELECTROSTATIC_COLOR_STOPS).toEqual({
      negative: '#d70201',
      neutral: '#fdfdfd',
      positive: '#010efd',
    });
  });

  it('maps protein and pocket surfaces separately from the ligand potential', () => {
    const complex = { hasProtein: true, hasLigand: true };
    expect(resolveElectrostaticSurfaceVolumeRole('protein', complex)).toBe('protein');
    expect(resolveElectrostaticSurfaceVolumeRole('pocket', complex)).toBe('protein');
    expect(resolveElectrostaticSurfaceVolumeRole('ligand', complex)).toBe('ligand');
    expect(resolveElectrostaticSurfaceVolumeRole('protein', { hasProtein: false, hasLigand: true })).toBe('ligand');
  });

  it('binds each selected docking ligand surface to its own potential volume', () => {
    const first = { ref: 'd01-volume', range: [-5, 5] as const };
    const second = { ref: 'd02-volume', range: [-5, 5] as const };

    expect(
      resolveDockingLigandElectrostaticSurfaces(['D01', 'D02', 'D01'], {
        ligands: { D01: first, D02: second },
      })
    ).toEqual([
      { residueName: 'D01', volume: first },
      { residueName: 'D02', volume: second },
    ]);
    expect(() => resolveDockingLigandElectrostaticSurfaces(['D01', 'D02'], { ligands: { D01: first } })).toThrow(
      'MOLSTAR_ELECTROSTATIC_LIGAND_VOLUME_MISSING:D02'
    );
  });

  it('samples the verified APBS volume at surface vertices with a fixed physical scale', () => {
    const settings = resolveSurfaceColorSettings({ models: [], units: [] } as unknown as Structure, {
      ref: 'apbs-volume-ref',
      range: [-5, 5],
    });

    expect(settings).toEqual({
      color: 'external-volume',
      colorParams: {
        volume: { ref: 'apbs-volume-ref' },
        coloring: {
          name: 'absolute-value',
          params: {
            domain: { name: 'custom', params: [-5, 5] },
            list: {
              kind: 'interpolate',
              colors: [0xd70201, 0xfdfdfd, 0x010efd],
            },
          },
        },
        defaultColor: 0xfdfdfd,
        normalOffset: 0,
        usePalette: false,
      },
    });
  });

  it('keeps the compound carbon picker from replacing electrostatic surface themes', () => {
    expect(isStructureLigandCarbonColorRepresentation('ball-and-stick')).toBe(true);
    expect(isStructureLigandCarbonColorRepresentation('line')).toBe(true);
    expect(isStructureLigandCarbonColorRepresentation('molecular-surface')).toBe(false);
    expect(isStructureLigandCarbonColorRepresentation('interactions')).toBe(false);
  });

  it('uses an explicit neutral theme for standalone structures without charge data', () => {
    const structure = { models: [], units: [] } as unknown as Structure;

    expect(resolveElectrostaticColorTheme(structure)).toBe('no-charge-data');
  });

  it('does not mistake an all-zero formal-charge column for electrostatic data', () => {
    const structure = createStructureWithCharges([0, 0, 0]);

    expect(resolveElectrostaticColorTheme(structure)).toBe('no-charge-data');
  });

  it('keeps residue charge coloring for proteins without atom-level charges', () => {
    const structure = createStructureWithCharges([0, 0], [5 as MoleculeType]);

    expect(resolveElectrostaticColorTheme(structure)).toBe('residue-charge');
  });

  it('uses non-zero formal charges when they are available', () => {
    const structure = createStructureWithCharges([-1, 0, 1]);

    expect(resolveElectrostaticColorTheme(structure)).toBe('formal-charge');
  });

  it('uses non-zero parsed partial charges before formal charges', () => {
    const structure = createStructureWithCharges([0, 0]);
    const provider = vi.spyOn(AtomPartialCharge.Provider, 'get').mockReturnValue({
      data: Column.ofFloatArray([-0.25, 0.4]),
    });

    try {
      expect(resolveElectrostaticColorTheme(structure)).toBe('partial-charge');
    } finally {
      provider.mockRestore();
    }
  });

  it('recognizes a ligand atom click without treating protein or empty clicks as ligand focus', () => {
    const structure = {} as Structure;
    const unit = { id: 7 } as Unit;
    const ligandLoci = StructureElement.Loci(structure, [{ unit, indices: OrderedSet.ofBounds(3, 6) }]);

    expect(
      isLigandLociClick(StructureElement.Loci(structure, [{ unit, indices: OrderedSet.ofSingleton(4) }]), ligandLoci)
    ).toBe(true);
    expect(
      isLigandLociClick(StructureElement.Loci(structure, [{ unit, indices: OrderedSet.ofSingleton(1) }]), ligandLoci)
    ).toBe(false);
    expect(isLigandLociClick(StructureElement.Loci(structure, []), ligandLoci)).toBe(false);
  });

  it('recognizes two clicks on the same structure inside the double-click window', () => {
    const firstStructure = {} as Structure;
    const secondStructure = {} as Structure;
    const unit = { id: 7 } as Unit;
    const firstClick = StructureElement.Loci(firstStructure, [{ unit, indices: OrderedSet.ofSingleton(3) }]);
    const secondClick = StructureElement.Loci(firstStructure, [{ unit, indices: OrderedSet.ofSingleton(4) }]);
    const otherStructureClick = StructureElement.Loci(secondStructure, [{ unit, indices: OrderedSet.ofSingleton(4) }]);

    expect(isStructureDoubleClick(firstClick, 1000, secondClick, 1250)).toBe(true);
    expect(isStructureDoubleClick(firstClick, 1000, secondClick, 1321)).toBe(false);
    expect(isStructureDoubleClick(firstClick, 1000, otherStructureClick, 1100)).toBe(false);
    expect(isStructureDoubleClick(firstClick, 1000, secondClick, 999)).toBe(false);
  });

  it('focuses a double-clicked structure locus without requesting a pocket-state mutation', () => {
    const structure = {} as Structure;
    const unit = { id: 7 } as Unit;
    const firstClick = StructureElement.Loci(structure, [{ unit, indices: OrderedSet.ofSingleton(3) }]);
    const secondClick = StructureElement.Loci(structure, [{ unit, indices: OrderedSet.ofSingleton(4) }]);
    const focusLoci = vi.fn();

    expect(focusStructureLociFromDoubleClick(firstClick, 1000, secondClick, 1250, focusLoci)).toBe(true);
    expect(focusLoci).toHaveBeenCalledOnce();
    expect(focusLoci).toHaveBeenCalledWith(
      secondClick,
      expect.objectContaining({ durationMs: 260, minRadius: 5.5, optimizeDirection: true })
    );
  });

  it('normalizes a single picked ligand bond from the default stick representation', () => {
    const structure = {} as Structure;
    const unit = { id: 7 } as Unit;
    const bond = Bond.Location(structure, unit, 4, structure, unit, 5);
    const normalized = normalizeStructureElementClickLoci(Bond.Loci(structure, [bond]));

    expect(normalized).toBeDefined();
    expect(StructureElement.Loci.size(normalized!)).toBe(1);
  });

  it('preflights an empty ligand selection before a pocket state change', () => {
    const structure = {} as Structure;
    const emptyLoci = StructureElement.Loci(structure, []);
    const queryLigandLoci = vi.fn(() => emptyLoci);

    const resolved = resolvePocketLigandLoci(structure, undefined, queryLigandLoci);

    expect(queryLigandLoci).toHaveBeenCalledWith(structure);
    expect(StructureElement.Loci.isEmpty(resolved)).toBe(true);
  });
});

describe('molstarStructureEngine pocket interactions', () => {
  it('keeps the ligand camera pivot stable by removing automatic click focus and empty-canvas reset', () => {
    const createDefaultSpec = vi.fn(() => ({
      behaviors: [
        { transformer: PluginBehaviors.Camera.FocusLoci },
        { transformer: PluginBehaviors.Representation.FocusLoci },
        { transformer: PluginBehaviors.State.SnapshotControls },
      ],
    })) as unknown as Parameters<typeof createMolstarViewportSpec>[0];
    const spec = createMolstarViewportSpec(createDefaultSpec);
    const transformers = spec.behaviors.map((behavior) => behavior.transformer);

    expect(createDefaultSpec).toHaveBeenCalledOnce();
    expect(transformers).not.toContain(PluginBehaviors.Camera.FocusLoci);
    expect(transformers).not.toContain(PluginBehaviors.Representation.FocusLoci);
    expect(transformers).toContain(PluginBehaviors.State.SnapshotControls);
  });

  it('enables every Mol* non-covalent interaction provider used by the pocket view', () => {
    const props = createCompletePocketInteractionProps(InteractionsProvider.defaultParams);

    expect(Object.keys(props.providers)).toEqual(POCKET_INTERACTION_PROVIDER_NAMES);
    expect(Object.values(props.providers).every((provider) => provider.name === 'on')).toBe(true);
    expect(Object.keys(props.bridges)).toEqual(POCKET_INTERACTION_BRIDGE_NAMES);
    expect(Object.values(props.bridges).every((provider) => provider.name === 'on')).toBe(true);
    expect(props.providers.ionic.params).toEqual({ distanceMax: 5 });
    expect(props.providers.hydrophobic.params).toEqual({ distanceMax: 4 });
  });

  it('disables providers at the computation boundary instead of only hiding their legend rows', () => {
    const props = createPocketInteractionProps(InteractionsProvider.defaultParams, {
      'hydrogen-bonds': true,
      'pi-stacking': true,
      'cation-pi': true,
      ionic: false,
      hydrophobic: false,
      'water-bridges': false,
    });

    expect(props.providers['hydrogen-bonds'].name).toBe('on');
    expect(props.providers['pi-stacking'].name).toBe('on');
    expect(props.providers['cation-pi'].name).toBe('on');
    expect(props.providers.ionic.name).toBe('off');
    expect(props.providers.hydrophobic.name).toBe('off');
    expect(props.bridges['water-bridges'].name).toBe('off');
  });

  it('keeps global bridge visuals out of the ligand-parent representation', () => {
    expect(POCKET_INTERACTION_VISUAL_GROUPS.ligand).toEqual(['intra-unit', 'inter-unit']);
    expect(POCKET_INTERACTION_VISUAL_GROUPS.ligand).not.toContain('bridge');
    expect(POCKET_INTERACTION_VISUAL_GROUPS.pocketBridges).toEqual(['bridge']);
  });

  it('keeps enough nearest-contact distances visible without covering the ligand', () => {
    expect(POCKET_DISTANCE_MARKER_LIMIT).toBe(8);
  });

  it('classifies every atom through its residue type instead of comparing local indices with trace atoms', () => {
    const lookup = {
      residueIndex: [1, 0],
      model: {
        atomicHierarchy: {
          derived: {
            residue: {
              moleculeType: [0 as MoleculeType, 5 as MoleculeType],
            },
          },
        },
      },
    };

    expect(isProteinAtom(lookup, 0)).toBe(true);
    expect(isProteinAtom(lookup, 1)).toBe(false);
  });
});

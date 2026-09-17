/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { OrderedSet } from 'molstar/lib/mol-data/int';
import { Cylinders } from 'molstar/lib/mol-geo/geometry/cylinders/cylinders';
import { CylindersBuilder } from 'molstar/lib/mol-geo/geometry/cylinders/cylinders-builder';
import { Spheres } from 'molstar/lib/mol-geo/geometry/spheres/spheres';
import { SpheresBuilder } from 'molstar/lib/mol-geo/geometry/spheres/spheres-builder';
import { Vec3 } from 'molstar/lib/mol-math/linear-algebra';
import { Shape, ShapeGroup } from 'molstar/lib/mol-model/shape';
import { Unit } from 'molstar/lib/mol-model/structure';
import { BondType } from 'molstar/lib/mol-model/structure/model/types';
import type { StructureElement } from 'molstar/lib/mol-model/structure';
import { PluginStateObject } from 'molstar/lib/mol-plugin-state/objects';
import { StateTransformer } from 'molstar/lib/mol-state';
import { Color } from 'molstar/lib/mol-util/color';
import { ParamDefinition as PD } from 'molstar/lib/mol-util/param-definition';

type DockingLigandShapeAtom = {
  position: readonly [number, number, number];
  element: string;
  label: string;
  residueName: string;
  formalCharge: number;
  carbonColor: Color;
};

type DockingLigandShapeBond = {
  atomA: number;
  atomB: number;
  aromatic: boolean;
  chemicalAromatic?: boolean;
  order: number;
};

export type DockingLigandShapeData = {
  atoms: readonly DockingLigandShapeAtom[];
  bonds: readonly DockingLigandShapeBond[];
  aromaticCycles: readonly (readonly number[])[];
  carbonColor: Color;
};

export type DockingLigandBondDefinition = {
  atomIdA: string;
  atomIdB: string;
  order: number;
  aromatic: boolean;
};

const formatV2000Integer = (value: number, width: number): string => Math.trunc(value).toString().padStart(width, ' ');

const formatV2000Coordinate = (value: number): string =>
  (Number.isFinite(value) ? value : 0).toFixed(4).padStart(10, ' ');

/** Serializes the parsed primary ligand through one format-neutral RDKit boundary. */
export const formatDockingLigandMolBlock = (
  data: DockingLigandShapeData,
  title = 'Ligand',
  { preserveAromaticBonds = true }: { preserveAromaticBonds?: boolean } = {}
): string => {
  const connectedAtomIndices = new Set(data.bonds.flatMap((bond) => [bond.atomA, bond.atomB]));
  const serializedAtomIndex = new Map<number, number>();
  const atoms = data.atoms.filter((atom, index) => {
    // Coordinate-only PDB/mmCIF files can contain explicit hydrogen records
    // without any bond topology. Sending those detached atoms to RDKit makes
    // an otherwise valid ligand a disconnected radical mixture. Keep bonded
    // polar hydrogens, but let RDKit reconstruct hydrogens that have no
    // chemically meaningful connection.
    const included = atom.element !== 'H' || connectedAtomIndices.has(index);
    if (included) serializedAtomIndex.set(index, serializedAtomIndex.size);
    return included;
  });
  const bonds = data.bonds.flatMap((bond) => {
    const atomA = serializedAtomIndex.get(bond.atomA);
    const atomB = serializedAtomIndex.get(bond.atomB);
    return atomA === undefined || atomB === undefined ? [] : [{ ...bond, atomA, atomB }];
  });

  if (atoms.length === 0) throw new Error('MOLSTAR_LIGAND_EMPTY');
  if (atoms.length > 999 || bonds.length > 999) throw new Error('MOLSTAR_LIGAND_V2000_LIMIT');

  const safeTitle =
    title
      .replace(/[\r\n]+/g, ' ')
      .trim()
      .slice(0, 80) || 'Ligand';
  const lines = [
    safeTitle,
    '  Synon Biomed',
    '',
    `${formatV2000Integer(atoms.length, 3)}${formatV2000Integer(bonds.length, 3)}  0  0  0  0            999 V2000`,
  ];
  atoms.forEach((atom) => {
    const element = atom.element.trim().toUpperCase().slice(0, 3) || 'C';
    lines.push(
      `${formatV2000Coordinate(atom.position[0])}${formatV2000Coordinate(atom.position[1])}${formatV2000Coordinate(
        atom.position[2]
      )} ${element.padEnd(3, ' ')} 0  0  0  0  0  0  0  0  0  0  0  0`
    );
  });
  bonds.forEach((bond) => {
    // Geometry-only aromatic inference improves the 3D ring accent, but it is
    // intentionally not promoted into chemical bond semantics for RDKit.
    const bondType =
      preserveAromaticBonds && bond.chemicalAromatic ? 4 : Math.max(1, Math.min(3, Math.round(bond.order)));
    lines.push(
      `${formatV2000Integer(bond.atomA + 1, 3)}${formatV2000Integer(bond.atomB + 1, 3)}${formatV2000Integer(
        bondType,
        3
      )}  0  0  0  0`
    );
  });

  const chargedAtoms = atoms
    .map((atom, index) => ({ index: index + 1, charge: Math.trunc(atom.formalCharge) }))
    .filter(({ charge }) => charge !== 0 && charge >= -15 && charge <= 15);
  for (let offset = 0; offset < chargedAtoms.length; offset += 8) {
    const group = chargedAtoms.slice(offset, offset + 8);
    lines.push(
      `M  CHG${formatV2000Integer(group.length, 3)}${group
        .map(({ index, charge }) => `${formatV2000Integer(index, 4)}${formatV2000Integer(charge, 4)}`)
        .join('')}`
    );
  }
  lines.push('M  END');
  return `${lines.join('\n')}\n`;
};

type DockingAromaticAtom = Pick<DockingLigandShapeAtom, 'position' | 'element'>;
type DockingAromaticBond = Pick<DockingLigandShapeBond, 'atomA' | 'atomB' | 'aromatic'>;

const dockingBondKey = (atomA: number, atomB: number): string =>
  atomA < atomB ? `${atomA}:${atomB}` : `${atomB}:${atomA}`;

const isPlanarAromaticCycle = (
  cycle: readonly number[],
  atoms: readonly DockingAromaticAtom[],
  bondsByKey: ReadonlyMap<string, DockingAromaticBond>
): boolean => {
  const positions = cycle.map((atomIndex) => atoms[atomIndex]?.position).filter(Boolean) as Array<
    readonly [number, number, number]
  >;
  if (positions.length !== cycle.length) return false;
  const allowedElements = new Set(['C', 'N', 'O', 'S', 'P']);
  if (cycle.some((atomIndex) => !allowedElements.has(atoms[atomIndex]?.element ?? ''))) return false;

  const normal = Vec3();
  const center = Vec3();
  let totalBondLength = 0;
  for (let index = 0; index < positions.length; index += 1) {
    const current = positions[index];
    const next = positions[(index + 1) % positions.length];
    center[0] += current[0];
    center[1] += current[1];
    center[2] += current[2];
    normal[0] += (current[1] - next[1]) * (current[2] + next[2]);
    normal[1] += (current[2] - next[2]) * (current[0] + next[0]);
    normal[2] += (current[0] - next[0]) * (current[1] + next[1]);
    totalBondLength += Vec3.distance(
      Vec3.create(current[0], current[1], current[2]),
      Vec3.create(next[0], next[1], next[2])
    );
    if (!bondsByKey.has(dockingBondKey(cycle[index], cycle[(index + 1) % cycle.length]))) return false;
  }
  const normalLength = Vec3.magnitude(normal);
  if (normalLength < 1e-6 || totalBondLength / cycle.length > 1.52) return false;
  Vec3.scale(center, center, 1 / positions.length);
  Vec3.scale(normal, normal, 1 / normalLength);
  return positions.every((position) => {
    const offset = Vec3.sub(Vec3(), Vec3.create(position[0], position[1], position[2]), center);
    return Math.abs(Vec3.dot(offset, normal)) <= 0.18;
  });
};

export const inferDockingAromaticCycles = (
  atoms: readonly DockingAromaticAtom[],
  bonds: readonly DockingAromaticBond[]
): number[][] => {
  const adjacency = new Map<number, number[]>();
  const bondsByKey = new Map<string, DockingAromaticBond>();
  bonds.forEach((bond) => {
    bondsByKey.set(dockingBondKey(bond.atomA, bond.atomB), bond);
    adjacency.set(bond.atomA, [...(adjacency.get(bond.atomA) ?? []), bond.atomB]);
    adjacency.set(bond.atomB, [...(adjacency.get(bond.atomB) ?? []), bond.atomA]);
  });

  const cycles = new Map<string, number[]>();
  const visit = (start: number, current: number, path: number[], visited: Set<number>) => {
    if (path.length > 6) return;
    for (const neighbor of adjacency.get(current) ?? []) {
      if (neighbor === start) {
        if (path.length < 5 || path.length > 6 || path[1] > path[path.length - 1]) continue;
        const edgeBonds = path.map((atomIndex, index) =>
          bondsByKey.get(dockingBondKey(atomIndex, path[(index + 1) % path.length]))
        );
        const explicitlyAromatic = edgeBonds.every((bond) => bond?.aromatic);
        if (!explicitlyAromatic && !isPlanarAromaticCycle(path, atoms, bondsByKey)) continue;
        cycles.set(path.toSorted((left, right) => left - right).join(':'), [...path]);
        continue;
      }
      if (neighbor < start || visited.has(neighbor) || path.length === 6) continue;
      visited.add(neighbor);
      visit(start, neighbor, [...path, neighbor], visited);
      visited.delete(neighbor);
    }
  };

  for (let start = 0; start < atoms.length; start += 1) {
    visit(start, start, [start], new Set([start]));
  }
  return [...cycles.values()];
};

export const applyDockingLigandBondDefinitions = (
  data: DockingLigandShapeData,
  definitions: readonly DockingLigandBondDefinition[]
): DockingLigandShapeData => {
  if (definitions.length === 0 || data.atoms.length === 0) return data;
  const atomIndexByLabel = new Map<string, number>();
  data.atoms.forEach((atom, index) => {
    const label = atom.label.trim().toUpperCase();
    if (label && !atomIndexByLabel.has(label)) atomIndexByLabel.set(label, index);
  });
  const bondsByKey = new Map(data.bonds.map((bond) => [dockingBondKey(bond.atomA, bond.atomB), bond]));
  definitions.forEach((definition) => {
    const atomA = atomIndexByLabel.get(definition.atomIdA.trim().toUpperCase());
    const atomB = atomIndexByLabel.get(definition.atomIdB.trim().toUpperCase());
    if (atomA === undefined || atomB === undefined || atomA === atomB) return;
    const aromatic = definition.aromatic;
    bondsByKey.set(dockingBondKey(atomA, atomB), {
      atomA,
      atomB,
      aromatic,
      chemicalAromatic: aromatic,
      order: Math.max(1, Math.min(3, Math.round(definition.order))),
    });
  });
  const bonds = [...bondsByKey.values()];
  return {
    ...data,
    bonds,
    aromaticCycles: inferDockingAromaticCycles(data.atoms, bonds),
  };
};

export const resolveDockingLigandShapeResidueNames = (loci: unknown): string[] => {
  if (!ShapeGroup.isLoci(loci)) return [];
  const data = loci.shape.sourceData as Partial<DockingLigandShapeData>;
  if (!Array.isArray(data.atoms) || !Array.isArray(data.bonds)) return [];

  const residueNames = new Set<string>();
  for (const group of loci.groups) {
    OrderedSet.forEach(group.ids, (groupId) => {
      const atom =
        loci.shape.name === 'Active docking ligand bonds'
          ? data.atoms?.[data.bonds?.[Math.floor(groupId / 2)]?.atomA ?? -1]
          : data.atoms?.[groupId];
      const residueName = atom?.residueName?.trim().toUpperCase();
      if (residueName) residueNames.add(residueName);
    });
  }
  return [...residueNames];
};

const EMPTY_DOCKING_LIGAND_SHAPE: DockingLigandShapeData = {
  atoms: [],
  bonds: [],
  aromaticCycles: [],
  carbonColor: Color(0x1f8a70),
};

const elementColor = (element: string, carbonColor: Color): Color => {
  switch (element) {
    case 'C':
      return carbonColor;
    case 'N':
      return Color(0x315efb);
    case 'O':
      return Color(0xe5484d);
    case 'S':
      return Color(0xe3b341);
    case 'P':
      return Color(0xe8792e);
    case 'F':
    case 'CL':
      return Color(0x44a047);
    case 'BR':
      return Color(0x9c4f2d);
    case 'I':
      return Color(0x7e57c2);
    case 'H':
      return Color(0xf4f6f8);
    default:
      return Color(0x8b95a5);
  }
};

export const resolveDockingLigandAtomRadius = (element: string): number => {
  if (element === 'H') return 0.1;
  if (element === 'C') return 0.17;
  return 0.18;
};

export const resolveDockingBondOrder = (
  elementA: string,
  elementB: string,
  distance: number,
  degreeA: number,
  degreeB: number,
  declaredOrder: number
): number => {
  if (declaredOrder >= 2) return Math.min(3, Math.round(declaredOrder));
  if (!Number.isFinite(distance) || distance <= 0) return 1;
  const elements = [elementA, elementB].toSorted().join('-');
  if (distance <= 1.22 && /^(C-C|C-N)$/.test(elements) && degreeA <= 2 && degreeB <= 2) return 3;
  if (distance <= 1.3 && !/(BR|CL|F|H|I)/.test(elements)) return 2;
  return 1;
};

export const resolveDockingLigandElement = (atomName: string, typeSymbol: string): string => {
  const normalizedName = atomName.trim().replace(/^\d+/, '').toUpperCase();
  if (normalizedName.startsWith('CL')) return 'CL';
  if (normalizedName.startsWith('BR')) return 'BR';
  const nameElement = normalizedName[0];
  if (nameElement && ['H', 'C', 'N', 'O', 'S', 'P', 'F', 'I'].includes(nameElement)) return nameElement;

  const normalizedSymbol = typeSymbol.trim().toUpperCase();
  return ['H', 'C', 'N', 'O', 'S', 'P', 'F', 'CL', 'BR', 'I'].includes(normalizedSymbol)
    ? normalizedSymbol
    : normalizedSymbol || 'C';
};

const buildDockingLigandAtomShape = (data: DockingLigandShapeData, previousSpheres?: Spheres) => {
  const builder = SpheresBuilder.create(Math.max(32, data.atoms.length), 32, previousSpheres);
  data.atoms.forEach((atom, index) => {
    builder.add(atom.position[0], atom.position[1], atom.position[2], index);
  });
  return Shape.create(
    'Active docking ligand atoms',
    data,
    builder.getSpheres(),
    (groupId) => {
      const atom = data.atoms[groupId];
      return elementColor(atom?.element ?? '', atom?.carbonColor ?? data.carbonColor);
    },
    (groupId) => resolveDockingLigandAtomRadius(data.atoms[groupId]?.element ?? ''),
    (groupId) => data.atoms[groupId]?.label ?? 'Docking ligand atom'
  );
};

const buildDockingLigandBondShape = (data: DockingLigandShapeData, previousCylinders?: Cylinders) => {
  const builder = CylindersBuilder.create(Math.max(32, data.bonds.length * 6), 32, previousCylinders);
  const colors: Color[] = [];
  const labels: string[] = [];
  let group = 0;
  for (const bond of data.bonds) {
    const atomA = data.atoms[bond.atomA];
    const atomB = data.atoms[bond.atomB];
    if (!atomA || !atomB) continue;
    const start = Vec3.create(atomA.position[0], atomA.position[1], atomA.position[2]);
    const end = Vec3.create(atomB.position[0], atomB.position[1], atomB.position[2]);
    const direction = Vec3.normalize(Vec3(), Vec3.sub(Vec3(), end, start));
    const reference = Math.abs(direction[2]) < 0.85 ? Vec3.create(0, 0, 1) : Vec3.create(0, 1, 0);
    const perpendicular = Vec3.normalize(Vec3(), Vec3.cross(Vec3(), direction, reference));
    const laneOffsets = bond.aromatic
      ? [0]
      : bond.order >= 3
        ? [-0.13, 0, 0.13]
        : bond.order === 2
          ? [-0.085, 0.085]
          : [0];
    for (const laneOffset of laneOffsets) {
      const laneStart = Vec3.scaleAndAdd(Vec3(), start, perpendicular, laneOffset);
      const laneEnd = Vec3.scaleAndAdd(Vec3(), end, perpendicular, laneOffset);
      const midpoint = Vec3.lerp(Vec3(), laneStart, laneEnd, 0.5);
      builder.add(
        laneStart[0],
        laneStart[1],
        laneStart[2],
        midpoint[0],
        midpoint[1],
        midpoint[2],
        1,
        true,
        true,
        2,
        group
      );
      colors.push(elementColor(atomA.element, atomA.carbonColor));
      labels.push(`${atomA.label} – ${atomB.label}`);
      group += 1;
      builder.add(midpoint[0], midpoint[1], midpoint[2], laneEnd[0], laneEnd[1], laneEnd[2], 1, true, true, 2, group);
      colors.push(elementColor(atomB.element, atomB.carbonColor));
      labels.push(`${atomA.label} – ${atomB.label}`);
      group += 1;
    }
  }

  const renderedAromaticBonds = new Set<string>();
  for (const cycle of data.aromaticCycles) {
    const center = cycle.reduce((sum, atomIndex) => {
      const atom = data.atoms[atomIndex];
      if (!atom) return sum;
      sum[0] += atom.position[0];
      sum[1] += atom.position[1];
      sum[2] += atom.position[2];
      return sum;
    }, Vec3());
    Vec3.scale(center, center, 1 / cycle.length);
    for (let index = 0; index < cycle.length; index += 1) {
      const atomAIndex = cycle[index];
      const atomBIndex = cycle[(index + 1) % cycle.length];
      const key = dockingBondKey(atomAIndex, atomBIndex);
      if (renderedAromaticBonds.has(key)) continue;
      renderedAromaticBonds.add(key);
      const atomA = data.atoms[atomAIndex];
      const atomB = data.atoms[atomBIndex];
      if (!atomA || !atomB) continue;
      const start = Vec3.create(atomA.position[0], atomA.position[1], atomA.position[2]);
      const end = Vec3.create(atomB.position[0], atomB.position[1], atomB.position[2]);
      Vec3.lerp(start, start, center, 0.18);
      Vec3.lerp(end, end, center, 0.18);
      builder.addFixedCountDashes(start, end, 4, 0.48, true, true, false, true, group);
      colors.push(elementColor(atomA.element, atomA.carbonColor));
      labels.push(`${atomA.label} – ${atomB.label} aromatic`);
      group += 1;
    }
  }
  return Shape.create(
    'Active docking ligand bonds',
    data,
    builder.getCylinders(),
    (groupId) => colors[groupId] ?? data.carbonColor,
    () => 0.115,
    (groupId) => labels[groupId] ?? 'Docking ligand bond'
  );
};

const createDockingLigandAtomShapeProvider = (data: DockingLigandShapeData) => ({
  label: 'Active docking ligand atoms',
  data,
  params: PD.withDefaults(Spheres.Params, {
    quality: 'medium',
    sizeFactor: 1,
  }),
  getShape: (_context: unknown, nextData: DockingLigandShapeData, _props: unknown, previous?: Shape<Spheres>) =>
    buildDockingLigandAtomShape(nextData, previous?.geometry),
  geometryUtils: Spheres.Utils,
});

const createDockingLigandBondShapeProvider = (data: DockingLigandShapeData) => ({
  label: 'Active docking ligand bonds',
  data,
  params: PD.withDefaults(Cylinders.Params, {
    quality: 'medium',
    sizeFactor: 1,
    sizeAspectRatio: 1,
  }),
  getShape: (_context: unknown, nextData: DockingLigandShapeData, _props: unknown, previous?: Shape<Cylinders>) =>
    buildDockingLigandBondShape(nextData, previous?.geometry),
  geometryUtils: Cylinders.Utils,
});

const DockingLigandShapeTransform = StateTransformer.builderFactory('synon-biomed');

export const SynonBiomedDockingLigandAtoms = DockingLigandShapeTransform({
  name: 'docking-ligand-atoms',
  display: { name: 'Active docking ligand atoms' },
  from: PluginStateObject.Root,
  to: PluginStateObject.Shape.Provider,
  params: {
    data: PD.Value<DockingLigandShapeData>(EMPTY_DOCKING_LIGAND_SHAPE, {
      isHidden: true,
    }),
  },
})({
  apply({ params }) {
    return new PluginStateObject.Shape.Provider(createDockingLigandAtomShapeProvider(params.data), {
      label: 'Active docking ligand atoms',
    });
  },
  update({ b, newParams }) {
    b.data.data = newParams.data;
    return StateTransformer.UpdateResult.Updated;
  },
});

export const SynonBiomedDockingLigandBonds = DockingLigandShapeTransform({
  name: 'docking-ligand-bonds',
  display: { name: 'Active docking ligand bonds' },
  from: PluginStateObject.Root,
  to: PluginStateObject.Shape.Provider,
  params: {
    data: PD.Value<DockingLigandShapeData>(EMPTY_DOCKING_LIGAND_SHAPE, {
      isHidden: true,
    }),
  },
})({
  apply({ params }) {
    return new PluginStateObject.Shape.Provider(createDockingLigandBondShapeProvider(params.data), {
      label: 'Active docking ligand bonds',
    });
  },
  update({ b, newParams }) {
    b.data.data = newParams.data;
    return StateTransformer.UpdateResult.Updated;
  },
});

export const createDockingLigandShapeData = (
  loci: StructureElement.Loci,
  carbonColor: Color,
  carbonColorsByResidue: ReadonlyMap<string, Color> = new Map()
): DockingLigandShapeData => {
  const atoms: Array<DockingLigandShapeAtom & { unitId: number; unitIndex: number }> = [];
  const atomIndexByUnit = new Map<string, number>();

  for (const element of loci.elements) {
    const unit = element.unit;
    if (!Unit.isAtomic(unit)) continue;
    const hierarchy = unit.model.atomicHierarchy;
    OrderedSet.forEach(element.indices, (unitIndex) => {
      const atomIndex = unit.elements[unitIndex];
      const position = unit.conformation.position(atomIndex, Vec3());
      const atomNumber = atoms.length;
      const atomLabel =
        hierarchy.atoms.label_atom_id.value(atomIndex).trim() ||
        hierarchy.atoms.auth_atom_id.value(atomIndex).trim() ||
        `Atom ${atomNumber + 1}`;
      const residueName =
        hierarchy.atoms.auth_comp_id.value(atomIndex).trim() || hierarchy.atoms.label_comp_id.value(atomIndex).trim();
      const formalCharge = hierarchy.atoms.pdbx_formal_charge.isDefined
        ? Number(hierarchy.atoms.pdbx_formal_charge.value(atomIndex))
        : 0;
      atoms.push({
        position: [position[0], position[1], position[2]],
        element: resolveDockingLigandElement(atomLabel, hierarchy.atoms.type_symbol.value(atomIndex)),
        label: atomLabel,
        residueName,
        formalCharge: Number.isFinite(formalCharge) ? Math.trunc(formalCharge) : 0,
        carbonColor: carbonColorsByResidue.get(residueName.toUpperCase()) ?? carbonColor,
        unitId: unit.id,
        unitIndex,
      });
      atomIndexByUnit.set(`${unit.id}:${unitIndex}`, atomNumber);
    });
  }

  const bonds: DockingLigandShapeBond[] = [];
  for (const element of loci.elements) {
    const unit = element.unit;
    if (!Unit.isAtomic(unit)) continue;
    const { offset, b } = unit.bonds;
    OrderedSet.forEach(element.indices, (unitIndex) => {
      const atomA = atomIndexByUnit.get(`${unit.id}:${unitIndex}`);
      if (atomA === undefined) return;
      for (let edge = offset[unitIndex]; edge < offset[unitIndex + 1]; edge += 1) {
        const neighborUnitIndex = b[edge];
        const atomB = atomIndexByUnit.get(`${unit.id}:${neighborUnitIndex}`);
        if (atomB === undefined || atomA >= atomB) continue;
        const chemicalAromatic = (unit.bonds.edgeProps.flags[edge] & BondType.Flag.Aromatic) !== 0;
        bonds.push({
          atomA,
          atomB,
          aromatic: chemicalAromatic,
          chemicalAromatic,
          order: unit.bonds.edgeProps.order[edge] ?? 1,
        });
      }
    });
  }

  // Match the structure viewer's default chemistry convention: suppress only
  // carbon-bound (non-polar) hydrogens while retaining donor/polar hydrogens.
  const hiddenAtoms = new Set<number>();
  for (const bond of bonds) {
    const atomA = atoms[bond.atomA];
    const atomB = atoms[bond.atomB];
    if (atomA.element === 'H' && atomB.element === 'C') {
      hiddenAtoms.add(bond.atomA);
    }
    if (atomB.element === 'H' && atomA.element === 'C') {
      hiddenAtoms.add(bond.atomB);
    }
  }

  const visibleAtoms: DockingLigandShapeAtom[] = [];
  const visibleIndexByOriginal = new Map<number, number>();
  atoms.forEach((atom, index) => {
    if (hiddenAtoms.has(index)) return;
    visibleIndexByOriginal.set(index, visibleAtoms.length);
    visibleAtoms.push({
      position: atom.position,
      element: atom.element,
      label: atom.label,
      residueName: atom.residueName,
      formalCharge: atom.formalCharge,
      carbonColor: atom.carbonColor,
    });
  });
  const visibleBonds: DockingLigandShapeBond[] = bonds.flatMap((bond) => {
    const atomA = visibleIndexByOriginal.get(bond.atomA);
    const atomB = visibleIndexByOriginal.get(bond.atomB);
    return atomA === undefined || atomB === undefined
      ? []
      : [
          {
            atomA,
            atomB,
            aromatic: bond.aromatic,
            chemicalAromatic: bond.chemicalAromatic,
            order: bond.order,
          },
        ];
  });
  const aromaticCycles = inferDockingAromaticCycles(visibleAtoms, visibleBonds);
  const aromaticBondKeys = new Set(
    aromaticCycles.flatMap((cycle) =>
      cycle.map((atomIndex, index) => dockingBondKey(atomIndex, cycle[(index + 1) % cycle.length]))
    )
  );
  const atomDegrees = visibleBonds.reduce((degrees, bond) => {
    degrees[bond.atomA] = (degrees[bond.atomA] ?? 0) + 1;
    degrees[bond.atomB] = (degrees[bond.atomB] ?? 0) + 1;
    return degrees;
  }, Array<number>(visibleAtoms.length).fill(0));

  return {
    atoms: visibleAtoms,
    bonds: visibleBonds.map((bond) => {
      const atomA = visibleAtoms[bond.atomA];
      const atomB = visibleAtoms[bond.atomB];
      const aromatic = bond.aromatic || aromaticBondKeys.has(dockingBondKey(bond.atomA, bond.atomB));
      const chargedTerminal =
        (atomA.formalCharge !== 0 && atomDegrees[bond.atomA] === 1) ||
        (atomB.formalCharge !== 0 && atomDegrees[bond.atomB] === 1);
      return {
        atomA: bond.atomA,
        atomB: bond.atomB,
        aromatic,
        chemicalAromatic: bond.chemicalAromatic,
        order: aromatic
          ? 1
          : chargedTerminal
            ? 1
            : resolveDockingBondOrder(
                atomA.element,
                atomB.element,
                Vec3.distance(
                  Vec3.create(atomA.position[0], atomA.position[1], atomA.position[2]),
                  Vec3.create(atomB.position[0], atomB.position[1], atomB.position[2])
                ),
                atomDegrees[bond.atomA],
                atomDegrees[bond.atomB],
                bond.order
              ),
      };
    }),
    aromaticCycles,
    carbonColor,
  };
};

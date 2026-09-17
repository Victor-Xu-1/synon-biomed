import { OrderedSet } from 'molstar/lib/mol-data/int';
import { StructureElement, Unit, type Structure } from 'molstar/lib/mol-model/structure';
import { StructureQuery } from 'molstar/lib/mol-model/structure/query/query';
import { StructureSelectionQueries } from 'molstar/lib/mol-plugin-state/helpers/structure-selection-query';

export type StructureObjectKind = 'protein' | 'ligand';

export type StructureObjectSummary = {
  id: StructureObjectKind;
  kind: StructureObjectKind;
  atomCount: number;
  residueNames: string[];
};

export type MolstarStructureComposition = {
  objects: StructureObjectSummary[];
  atomCount: number;
  hasProtein: boolean;
  hasLigand: boolean;
};

const STANDALONE_LIGAND_FORMATS = new Set(['mol', 'sdf', 'mol2', 'xyz']);

// Common crystallization/cryoprotection additives are part of the parsed
// structure, but they are not user-facing binding compounds. Keep the list at
// this semantic boundary so PDB and mmCIF use exactly the same classification.
const STRUCTURE_ADDITIVE_RESIDUE_NAMES = new Set(['ACT', 'DMS', 'EDO', 'EOH', 'GOL', 'MPD', 'PEG', 'PG4', 'PGE']);

export const isDisplayLigandResidueName = (name: string): boolean =>
  !STRUCTURE_ADDITIVE_RESIDUE_NAMES.has(name.trim().toUpperCase());

const residueNames = (loci: StructureElement.Loci): string[] => {
  const names = new Set<string>();
  for (const { unit, indices } of loci.elements) {
    if (!Unit.isAtomic(unit)) continue;
    for (let index = 0; index < OrderedSet.size(indices); index += 1) {
      const unitIndex = OrderedSet.getAt(indices, index);
      const atom = unit.elements[unitIndex];
      const name =
        unit.model.atomicHierarchy.atoms.auth_comp_id.value(atom).trim() ||
        unit.model.atomicHierarchy.atoms.label_comp_id.value(atom).trim();
      if (name) names.add(name);
    }
  }
  return [...names].toSorted();
};

const selection = (structure: Structure, kind: 'all' | StructureObjectKind): StructureElement.Loci =>
  StructureQuery.loci(StructureSelectionQueries[kind].query, structure);

/** Builds the sidebar model from Mol* semantics after parsing, independent of the source extension. */
export const summarizeStructureComposition = (
  structure: Structure | undefined,
  format = ''
): MolstarStructureComposition => {
  if (!structure) return { objects: [], atomCount: 0, hasProtein: false, hasLigand: false };

  const all = selection(structure, 'all');
  const protein = selection(structure, 'protein');
  let ligand = selection(structure, 'ligand');
  const atomCount = StructureElement.Loci.size(all);
  const proteinAtomCount = StructureElement.Loci.size(protein);
  let ligandAtomCount = StructureElement.Loci.size(ligand);

  if (proteinAtomCount === 0 && ligandAtomCount === 0 && atomCount > 0 && STANDALONE_LIGAND_FORMATS.has(format)) {
    ligand = all;
    ligandAtomCount = atomCount;
  }

  const ligandResidueNames = residueNames(ligand);
  const displayLigandResidueNames =
    proteinAtomCount > 0 ? ligandResidueNames.filter(isDisplayLigandResidueName) : ligandResidueNames;
  const hasDisplayLigand = ligandAtomCount > 0 && (displayLigandResidueNames.length > 0 || proteinAtomCount === 0);

  const objects: StructureObjectSummary[] = [];
  if (proteinAtomCount > 0) {
    objects.push({ id: 'protein', kind: 'protein', atomCount: proteinAtomCount, residueNames: residueNames(protein) });
  }
  if (hasDisplayLigand) {
    objects.push({
      id: 'ligand',
      kind: 'ligand',
      atomCount: ligandAtomCount,
      residueNames: displayLigandResidueNames,
    });
  }

  return {
    objects,
    atomCount,
    hasProtein: proteinAtomCount > 0,
    hasLigand: hasDisplayLigand,
  };
};

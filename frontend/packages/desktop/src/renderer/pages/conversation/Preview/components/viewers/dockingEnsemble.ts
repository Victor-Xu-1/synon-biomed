export type DockingEnsembleEntry = {
  residueName: string;
  candidateId: string;
  rank: number;
  poseRank: number;
  affinityKcalMol?: number;
  kind: 'reference' | 'candidate';
};

export type DockingEnsemble = {
  entries: DockingEnsembleEntry[];
  source: string;
  proteinLines: readonly string[];
  ligandLinesByResidue: ReadonlyMap<string, readonly string[]>;
};

const ENSEMBLE_MARKER = 'REMARK 900 SYNON BIOMED DOCKING COMPLEX ENSEMBLE';
const REFERENCE_PATTERN =
  /^REMARK 900 REFERENCE LIGAND (\S+) SOURCE (\S+) CHAIN \S+ RESIDUE \S+(?: AFFINITY (-?\d+(?:\.\d+)?) KCAL\/MOL)?\s*$/;
const CANDIDATE_PATTERN =
  /^REMARK 900 DOCKED LIGAND (\S+) CANDIDATE (\S+) RANK (\d+)(?: POSE (\d+))? AFFINITY (-?\d+(?:\.\d+)?) KCAL\/MOL(?: RUN \d+ SOURCE_MODE \d+)?\s*$/;

/**
 * Reads the self-describing docking ensemble emitted by the canonical docking
 * execution pack. Ordinary PDB files deliberately return null and keep the
 * standard Mol* path unchanged.
 */
export const parseDockingEnsemble = (source: string): DockingEnsemble | null => {
  if (!source.includes(ENSEMBLE_MARKER)) return null;

  let reference: DockingEnsembleEntry | undefined;
  const candidates: DockingEnsembleEntry[] = [];
  const proteinLines: string[] = [];
  const ligandLinesByResidue = new Map<string, string[]>();
  for (const line of source.split(/\r?\n/)) {
    const recordName = line.slice(0, 6).trim();
    if (recordName === 'ATOM') {
      proteinLines.push(line);
    } else if (recordName === 'HETATM') {
      const residueName = line.slice(17, 20).trim();
      const residueLines = ligandLinesByResidue.get(residueName) ?? [];
      residueLines.push(line);
      ligandLinesByResidue.set(residueName, residueLines);
    }

    const referenceMatch = REFERENCE_PATTERN.exec(line);
    if (referenceMatch) {
      const affinityKcalMol = referenceMatch[3] === undefined ? undefined : Number(referenceMatch[3]);
      reference = {
        residueName: referenceMatch[1],
        candidateId: referenceMatch[2],
        rank: 0,
        poseRank: 0,
        ...(Number.isFinite(affinityKcalMol) ? { affinityKcalMol } : {}),
        kind: 'reference',
      };
      continue;
    }

    const candidateMatch = CANDIDATE_PATTERN.exec(line);
    if (!candidateMatch) continue;
    const rank = Number(candidateMatch[3]);
    const poseRank = Number(candidateMatch[4] || 1);
    const affinityKcalMol = Number(candidateMatch[5]);
    if (
      !Number.isSafeInteger(rank) ||
      rank < 1 ||
      !Number.isSafeInteger(poseRank) ||
      poseRank < 1 ||
      !Number.isFinite(affinityKcalMol)
    )
      continue;
    candidates.push({
      residueName: candidateMatch[1],
      candidateId: candidateMatch[2],
      rank,
      poseRank,
      affinityKcalMol,
      kind: 'candidate',
    });
  }

  const sortedCandidates = candidates.toSorted(
    (left, right) =>
      left.rank - right.rank || left.poseRank - right.poseRank || left.candidateId.localeCompare(right.candidateId)
  );
  if (!reference || sortedCandidates.length === 0) return null;

  const uniqueResidues = new Set([reference.residueName, ...sortedCandidates.map((entry) => entry.residueName)]);
  if (uniqueResidues.size !== sortedCandidates.length + 1) return null;
  if ([...uniqueResidues].some((residueName) => !ligandLinesByResidue.get(residueName)?.length)) {
    return null;
  }
  return {
    entries: [reference, ...sortedCandidates],
    source,
    proteinLines,
    ligandLinesByResidue,
  };
};

/** Builds a display-only PDB with one fixed receptor and exactly one ligand. */
export const selectDockingEnsembleEntries = (ensemble: DockingEnsemble, indices: number[]): string => {
  const entries = indices.map((index) => {
    const entry = ensemble.entries[index];
    if (!entry) throw new RangeError(`Docking ensemble entry ${index} is unavailable`);
    return entry;
  });
  if (new Set(entries.map((entry) => entry.residueName)).size !== entries.length) {
    throw new RangeError('Docking comparison entries must be distinct');
  }

  const output = [
    'REMARK 900 DOCKING POSE PREVIEW',
    ...entries.map(
      (entry) =>
        `REMARK 900 DISPLAYED ${entry.kind.toUpperCase()} ${
          entry.candidateId
        } RANK ${entry.rank} POSE ${entry.poseRank}`
    ),
    ...ensemble.proteinLines,
  ];
  for (const entry of entries) {
    const ligandLines = ensemble.ligandLinesByResidue.get(entry.residueName);
    if (!ligandLines?.length) {
      throw new RangeError(`Docking ensemble residue ${entry.residueName} is unavailable`);
    }
    output.push(...ligandLines);
    output.push('TER');
  }
  output.push('END', '');
  return output.join('\n');
};

export const selectDockingEnsembleEntry = (ensemble: DockingEnsemble, index: number): string =>
  selectDockingEnsembleEntries(ensemble, [index]);

const pdbAtomSerial = (line: string): number | undefined => {
  const serial = Number.parseInt(line.slice(6, 11).trim(), 10);
  return Number.isSafeInteger(serial) && serial > 0 ? serial : undefined;
};

/**
 * Merges minimized coordinates back into the authoritative docking ensemble.
 * Only the selected ligand's fixed-width XYZ columns are replaced; receptor
 * records, rankings, every other ligand and all self-describing REMARK records
 * remain unchanged.
 */
export const mergeDockingEnsembleLigandCoordinates = (
  ensemble: DockingEnsemble,
  residueName: string,
  minimizedPose: string
): DockingEnsemble => {
  const normalizedResidueName = residueName.trim().toUpperCase();
  const coordinatesBySerial = new Map<number, string>();
  for (const line of minimizedPose.split(/\r?\n/)) {
    if (line.slice(0, 6).trim() !== 'HETATM' || line.slice(17, 20).trim().toUpperCase() !== normalizedResidueName)
      continue;
    const serial = pdbAtomSerial(line);
    const coordinates = line.slice(30, 54);
    if (serial === undefined || coordinates.length !== 24 || coordinatesBySerial.has(serial)) {
      throw new Error('MINIMIZED_DOCKING_LIGAND_COORDINATES_INVALID');
    }
    coordinatesBySerial.set(serial, coordinates);
  }

  const expectedLigandLines = ensemble.ligandLinesByResidue.get(normalizedResidueName);
  if (!expectedLigandLines?.length || coordinatesBySerial.size !== expectedLigandLines.length) {
    throw new Error('MINIMIZED_DOCKING_LIGAND_ATOM_COUNT_MISMATCH');
  }

  let replaced = 0;
  const source = ensemble.source
    .split(/\r?\n/)
    .map((line) => {
      if (line.slice(0, 6).trim() !== 'HETATM' || line.slice(17, 20).trim().toUpperCase() !== normalizedResidueName)
        return line;
      const serial = pdbAtomSerial(line);
      const coordinates = serial === undefined ? undefined : coordinatesBySerial.get(serial);
      if (!coordinates) throw new Error('MINIMIZED_DOCKING_LIGAND_SERIAL_MISMATCH');
      replaced += 1;
      return `${line.padEnd(54).slice(0, 30)}${coordinates}${line.padEnd(54).slice(54)}`;
    })
    .join('\n');
  if (replaced !== expectedLigandLines.length) {
    throw new Error('MINIMIZED_DOCKING_LIGAND_REPLACEMENT_INCOMPLETE');
  }

  const merged = parseDockingEnsemble(source);
  if (!merged) throw new Error('MINIMIZED_DOCKING_ENSEMBLE_INVALID');
  return merged;
};

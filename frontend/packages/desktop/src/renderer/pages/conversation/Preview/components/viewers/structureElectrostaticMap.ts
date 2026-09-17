export type StructureElectrostaticGridReport = {
  counts: [number, number, number];
  origin: [number, number, number];
  delta: [number, number, number];
  value_count: number;
  minimum: number;
  maximum: number;
};

export type StructureElectrostaticMapRole = 'protein' | 'ligand';

export type StructureElectrostaticPotentialMaps = Partial<Record<StructureElectrostaticMapRole, string>> & {
  ligands?: Record<string, string>;
};

export type StructureElectrostaticMapReport = {
  contract: boolean;
  contract_version: '2.0';
  ok: boolean;
  engine: 'APBS';
  calculation_mode: 'separate-components';
  grid_alignment: 'shared-frame';
  engine_version: string;
  preparation_engine: string;
  preparation_engine_version: string;
  protein_charge_method: string;
  ligand_charge_method: string;
  force_field: string;
  ph: number;
  ionic_strength_molar: number;
  potential_unit: 'kT/e';
  color_range: [number, number];
  input_sha256: string;
  protein_atom_count: number;
  ligand_atom_count: number;
  total_atom_count: number;
  mesh_spacing_angstrom: number;
  grids: Partial<Record<StructureElectrostaticMapRole, StructureElectrostaticGridReport>> & {
    ligands?: Record<string, StructureElectrostaticGridReport>;
  };
  ligand_atom_counts?: Record<string, number>;
  warnings: string[];
};

export type StructureElectrostaticEncodedMap = {
  dx_gzip_base64: string;
};

export type StructureElectrostaticMapResponse = {
  ok: boolean;
  status: 'completed';
  encoding: 'gzip+base64';
  potential_maps: Partial<Record<StructureElectrostaticMapRole, StructureElectrostaticEncodedMap>> & {
    ligands?: Record<string, StructureElectrostaticEncodedMap>;
  };
  report: StructureElectrostaticMapReport;
  runtime: unknown;
};

export const MAX_STRUCTURE_ELECTROSTATIC_BATCH_LIGANDS = 8;
const MAX_COMPRESSED_BASE64_LENGTH = 24 << 20;
const MAX_TOTAL_COMPRESSED_BASE64_LENGTH = 36 << 20;
const MAX_DECOMPRESSED_DX_LENGTH = 64 << 20;
const MAX_TOTAL_DECOMPRESSED_DX_LENGTH = 128 << 20;
const MAX_GRID_VALUES = 6_000_000;
const MAX_TOTAL_GRID_VALUES = 24_000_000;
const MAX_PROTEIN_ATOMS = 200_000;
const MAX_LIGAND_ATOMS = 2_048;
const MAX_TOTAL_LIGAND_ATOMS = 8_192;
const ELECTROSTATIC_MAP_ROLES: readonly StructureElectrostaticMapRole[] = ['protein', 'ligand'];

const isRecord = (value: unknown): value is Record<string, unknown> =>
  Boolean(value && typeof value === 'object' && !Array.isArray(value));

const isLigandKey = (value: string): boolean =>
  value === value.trim() && /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(value);

const isBoundedText = (value: unknown, maximum: number): value is string =>
  typeof value === 'string' && value.trim().length > 0 && value.length <= maximum && !value.includes('\0');

const isElectrostaticGridReport = (value: unknown): value is StructureElectrostaticGridReport => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false;
  const grid = value as Partial<StructureElectrostaticGridReport>;
  return Boolean(
    Array.isArray(grid.counts) &&
    grid.counts.length === 3 &&
    grid.counts.every((count) => Number.isInteger(count) && count > 0) &&
    Array.isArray(grid.origin) &&
    grid.origin.length === 3 &&
    grid.origin.every(Number.isFinite) &&
    Array.isArray(grid.delta) &&
    grid.delta.length === 3 &&
    grid.delta.every((step) => Number.isFinite(step) && step > 0) &&
    Number.isInteger(grid.value_count) &&
    grid.value_count <= MAX_GRID_VALUES &&
    grid.value_count === grid.counts.reduce((product, count) => product * count, 1) &&
    Number.isFinite(grid.minimum) &&
    Number.isFinite(grid.maximum) &&
    grid.minimum <= grid.maximum
  );
};

const gridFramesMatch = (left: StructureElectrostaticGridReport, right: StructureElectrostaticGridReport): boolean =>
  left.counts.every((count, index) => count === right.counts[index]) &&
  left.origin.every((value, index) => Math.abs(value - right.origin[index]) <= 1e-6) &&
  left.delta.every((value, index) => Math.abs(value - right.delta[index]) <= 1e-6);

export const isStructureElectrostaticMapResponse = (value: unknown): value is StructureElectrostaticMapResponse => {
  if (!isRecord(value)) return false;
  const response = value as Partial<StructureElectrostaticMapResponse>;
  const report = response.report;
  const maps = response.potential_maps;
  const grids = report?.grids;
  if (!isRecord(maps) || !isRecord(grids)) return false;

  const batchMaps = maps.ligands;
  const batchCounts = report?.ligand_atom_counts;
  const batchGrids = grids.ligands;
  const hasAnyBatchField = batchMaps !== undefined || batchCounts !== undefined || batchGrids !== undefined;
  const isBatch = isRecord(batchMaps) && isRecord(batchCounts) && isRecord(batchGrids);
  if (hasAnyBatchField !== isBatch) return false;

  const mapRoles = Object.keys(maps).filter((role) => role !== 'ligands');
  const gridRoles = Object.keys(grids).filter((role) => role !== 'ligands');
  const expectedRoles = ELECTROSTATIC_MAP_ROLES.filter((role) =>
    role === 'protein' ? Number(report?.protein_atom_count) > 0 : !isBatch && Number(report?.ligand_atom_count) > 0
  );
  if (
    mapRoles.length !== expectedRoles.length ||
    gridRoles.length !== expectedRoles.length ||
    mapRoles.some((role) => !expectedRoles.includes(role as StructureElectrostaticMapRole)) ||
    gridRoles.some((role) => !expectedRoles.includes(role as StructureElectrostaticMapRole))
  ) {
    return false;
  }

  const batchKeys = isBatch ? Object.keys(batchMaps) : [];
  if (isBatch) {
    const normalizedKeys = new Set(batchKeys.map((key) => key.toUpperCase()));
    const countKeys = Object.keys(batchCounts);
    const gridKeys = Object.keys(batchGrids);
    if (
      batchKeys.length === 0 ||
      batchKeys.length > MAX_STRUCTURE_ELECTROSTATIC_BATCH_LIGANDS ||
      normalizedKeys.size !== batchKeys.length ||
      batchKeys.some((key) => !isLigandKey(key)) ||
      countKeys.length !== batchKeys.length ||
      gridKeys.length !== batchKeys.length ||
      batchKeys.some((key) => !(key in batchCounts) || !(key in batchGrids)) ||
      maps.ligand !== undefined ||
      grids.ligand !== undefined
    ) {
      return false;
    }
  }

  if (expectedRoles.length === 0 && batchKeys.length === 0) return false;
  let totalCompressedLength = 0;
  let totalGridValues = 0;
  let referenceGrid: StructureElectrostaticGridReport | undefined;
  const validateComponent = (encoded: unknown, grid: unknown): boolean => {
    if (
      !isRecord(encoded) ||
      typeof encoded.dx_gzip_base64 !== 'string' ||
      encoded.dx_gzip_base64.length === 0 ||
      encoded.dx_gzip_base64.length > MAX_COMPRESSED_BASE64_LENGTH ||
      !isElectrostaticGridReport(grid)
    ) {
      return false;
    }
    totalCompressedLength += encoded.dx_gzip_base64.length;
    totalGridValues += grid.value_count;
    if (referenceGrid && !gridFramesMatch(referenceGrid, grid)) return false;
    referenceGrid = grid;
    return true;
  };
  for (const role of expectedRoles) {
    if (!validateComponent(maps[role], grids[role])) return false;
  }
  if (isBatch) {
    let reportedLigandAtoms = 0;
    for (const key of batchKeys) {
      const atomCount = batchCounts[key];
      if (
        !Number.isInteger(atomCount) ||
        atomCount <= 0 ||
        atomCount > MAX_LIGAND_ATOMS ||
        !validateComponent(batchMaps[key], batchGrids[key])
      ) {
        return false;
      }
      reportedLigandAtoms += atomCount;
    }
    if (reportedLigandAtoms !== report?.ligand_atom_count) return false;
  }
  return Boolean(
    response.ok === true &&
    response.status === 'completed' &&
    response.encoding === 'gzip+base64' &&
    totalCompressedLength <= MAX_TOTAL_COMPRESSED_BASE64_LENGTH &&
    totalGridValues <= MAX_TOTAL_GRID_VALUES &&
    report?.contract === true &&
    report.contract_version === '2.0' &&
    report.ok === true &&
    report.engine === 'APBS' &&
    report.calculation_mode === 'separate-components' &&
    report.grid_alignment === 'shared-frame' &&
    report.potential_unit === 'kT/e' &&
    isBoundedText(report.engine_version, 64) &&
    report.preparation_engine === 'PDB2PQR + RDKit' &&
    isBoundedText(report.preparation_engine_version, 256) &&
    report.force_field === 'AMBER' &&
    report.mesh_spacing_angstrom === 0.65 &&
    typeof report.input_sha256 === 'string' &&
    /^[a-f0-9]{64}$/i.test(report.input_sha256) &&
    Number.isFinite(report.ph) &&
    report.ph >= 0 &&
    report.ph <= 14 &&
    Number.isFinite(report.ionic_strength_molar) &&
    report.ionic_strength_molar >= 0 &&
    report.ionic_strength_molar <= 1 &&
    Array.isArray(report.color_range) &&
    report.color_range.length === 2 &&
    report.color_range[0] === -5 &&
    report.color_range[1] === 5 &&
    Number.isInteger(report.protein_atom_count) &&
    report.protein_atom_count >= 0 &&
    report.protein_atom_count <= MAX_PROTEIN_ATOMS &&
    Number.isInteger(report.ligand_atom_count) &&
    report.ligand_atom_count >= 0 &&
    report.ligand_atom_count <= MAX_TOTAL_LIGAND_ATOMS &&
    (isBatch || report.ligand_atom_count <= MAX_LIGAND_ATOMS) &&
    (report.protein_atom_count > 0
      ? report.protein_charge_method === 'PDB2PQR AMBER with PROPKA protonation'
      : report.protein_charge_method === 'not applicable') &&
    (report.ligand_atom_count > 0
      ? report.ligand_charge_method === 'RDKit Gasteiger with explicit hydrogens'
      : report.ligand_charge_method === 'not applicable') &&
    Number.isInteger(report.total_atom_count) &&
    report.total_atom_count > 0 &&
    report.total_atom_count === report.protein_atom_count + report.ligand_atom_count &&
    Array.isArray(report.warnings) &&
    report.warnings.length <= 32 &&
    report.warnings.every(
      (warning) => typeof warning === 'string' && warning.length > 0 && warning.length <= 256 && !warning.includes('\0')
    )
  );
};

const decodeStructureElectrostaticDX = async (
  encoded: string,
  outputLimit: number
): Promise<{ dx: string; byteLength: number }> => {
  let compressed: Uint8Array;
  try {
    compressed = Uint8Array.from(atob(encoded), (character) => character.charCodeAt(0));
  } catch (reason) {
    throw new Error('STRUCTURE_ELECTROSTATIC_BASE64_INVALID', {
      cause: reason,
    });
  }
  const limit = Math.min(MAX_DECOMPRESSED_DX_LENGTH, outputLimit);
  if (limit <= 0) throw new Error('STRUCTURE_ELECTROSTATIC_DX_INVALID');
  const stream = new Blob([compressed]).stream().pipeThrough(new DecompressionStream('gzip'));
  const reader = stream.getReader();
  const chunks: Uint8Array[] = [];
  let totalBytes = 0;
  try {
    while (true) {
      // Read the stream serially so decompressed bytes are bounded before the next chunk is accepted.
      // eslint-disable-next-line no-await-in-loop
      const { done, value } = await reader.read();
      if (done) break;
      totalBytes += value.byteLength;
      if (totalBytes > limit) {
        // Await cancellation before releasing the reader so the decompressor cannot keep producing data.
        // eslint-disable-next-line no-await-in-loop
        await reader.cancel('STRUCTURE_ELECTROSTATIC_DX_INVALID');
        throw new Error('STRUCTURE_ELECTROSTATIC_DX_INVALID');
      }
      chunks.push(value);
    }
  } catch (reason) {
    if (reason instanceof Error && reason.message === 'STRUCTURE_ELECTROSTATIC_DX_INVALID') throw reason;
    throw new Error('STRUCTURE_ELECTROSTATIC_DX_INVALID', { cause: reason });
  } finally {
    reader.releaseLock();
  }
  if (totalBytes === 0) throw new Error('STRUCTURE_ELECTROSTATIC_DX_INVALID');
  const decodedBytes = new Uint8Array(totalBytes);
  let offset = 0;
  for (const chunk of chunks) {
    decodedBytes.set(chunk, offset);
    offset += chunk.byteLength;
  }
  let dx: string;
  try {
    dx = new TextDecoder('utf-8', { fatal: true }).decode(decodedBytes);
  } catch (reason) {
    throw new Error('STRUCTURE_ELECTROSTATIC_DX_INVALID', { cause: reason });
  }
  if (!dx.includes('class gridpositions counts') || !dx.includes('data follows')) {
    throw new Error('STRUCTURE_ELECTROSTATIC_DX_INVALID');
  }
  return { dx, byteLength: totalBytes };
};

export async function decodeStructureElectrostaticMaps(
  response: StructureElectrostaticMapResponse
): Promise<StructureElectrostaticPotentialMaps> {
  if (!isStructureElectrostaticMapResponse(response)) throw new Error('STRUCTURE_ELECTROSTATIC_RESPONSE_INVALID');
  if (typeof DecompressionStream !== 'function') throw new Error('STRUCTURE_ELECTROSTATIC_GZIP_UNSUPPORTED');
  const encodedMaps: Array<{ role: StructureElectrostaticMapRole | 'batch-ligand'; key?: string; encoded: string }> =
    ELECTROSTATIC_MAP_ROLES.flatMap((role) => {
      const encoded = response.potential_maps[role]?.dx_gzip_base64;
      return encoded ? [{ role, encoded }] : [];
    });
  for (const [key, encoded] of Object.entries(response.potential_maps.ligands ?? {})) {
    encodedMaps.push({ role: 'batch-ligand', key, encoded: encoded.dx_gzip_base64 });
  }
  const decoded: StructureElectrostaticPotentialMaps = {};
  let totalDecodedBytes = 0;
  for (const { role, key, encoded } of encodedMaps) {
    // Decode one grid at a time so the aggregate memory ceiling remains enforceable.
    // eslint-disable-next-line no-await-in-loop
    const { dx, byteLength } = await decodeStructureElectrostaticDX(
      encoded,
      MAX_TOTAL_DECOMPRESSED_DX_LENGTH - totalDecodedBytes
    );
    totalDecodedBytes += byteLength;
    if (role === 'batch-ligand') {
      decoded.ligands ??= {};
      decoded.ligands[key!] = dx;
    } else {
      decoded[role] = dx;
    }
  }
  return decoded;
}

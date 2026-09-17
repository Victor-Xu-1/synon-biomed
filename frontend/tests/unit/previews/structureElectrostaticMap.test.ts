import { gzipSync } from 'node:zlib';
import { describe, expect, it } from 'vitest';
import {
  decodeStructureElectrostaticMaps,
  isStructureElectrostaticMapResponse,
  type StructureElectrostaticMapResponse,
} from '@/renderer/pages/conversation/Preview/components/viewers/structureElectrostaticMap';

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

const response = (): StructureElectrostaticMapResponse => ({
  ok: true,
  status: 'completed',
  encoding: 'gzip+base64',
  potential_maps: {
    protein: { dx_gzip_base64: gzipSync(dx).toString('base64') },
    ligand: { dx_gzip_base64: gzipSync(dx).toString('base64') },
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

const batchResponse = (): StructureElectrostaticMapResponse => {
  const value = response();
  delete value.potential_maps.ligand;
  delete value.report.grids.ligand;
  value.potential_maps.ligands = {
    D01: { dx_gzip_base64: gzipSync(dx).toString('base64') },
    D02: { dx_gzip_base64: gzipSync(dx).toString('base64') },
  };
  value.report.ligand_atom_count = 9;
  value.report.total_atom_count = value.report.protein_atom_count + 9;
  value.report.ligand_atom_counts = { D01: 4, D02: 5 };
  value.report.grids.ligands = {
    D01: {
      counts: [2, 2, 2],
      origin: [0, 0, 0],
      delta: [1, 1, 1],
      value_count: 8,
      minimum: -1,
      maximum: 6,
    },
    D02: {
      counts: [2, 2, 2],
      origin: [0, 0, 0],
      delta: [1, 1, 1],
      value_count: 8,
      minimum: -1,
      maximum: 6,
    },
  };
  return value;
};

describe('structure electrostatic map transport', () => {
  it('validates and decodes the bounded APBS OpenDX response', async () => {
    const value = response();
    expect(isStructureElectrostaticMapResponse(value)).toBe(true);
    expect(await decodeStructureElectrostaticMaps(value)).toEqual({ protein: dx, ligand: dx });
  });

  it('rejects a response that labels a non-APBS payload as an electrostatic map', () => {
    const value = response() as unknown as { report: { engine: string } };
    value.report.engine = 'residue-charge';
    expect(isStructureElectrostaticMapResponse(value)).toBe(false);
  });

  it('accepts a standalone protein map without inventing a ligand field', async () => {
    const proteinOnly = response();
    delete proteinOnly.potential_maps.ligand;
    delete proteinOnly.report.grids.ligand;
    proteinOnly.report.ligand_atom_count = 0;
    proteinOnly.report.total_atom_count = proteinOnly.report.protein_atom_count;
    proteinOnly.report.ligand_charge_method = 'not applicable';
    expect(isStructureElectrostaticMapResponse(proteinOnly)).toBe(true);
    expect(await decodeStructureElectrostaticMaps(proteinOnly)).toEqual({ protein: dx });
  });

  it('validates and decodes keyed ligand maps from one bounded batch response', async () => {
    const value = batchResponse();
    expect(isStructureElectrostaticMapResponse(value)).toBe(true);
    expect(await decodeStructureElectrostaticMaps(value)).toEqual({
      protein: dx,
      ligands: { D01: dx, D02: dx },
    });
  });

  it('rejects mixed, incomplete, duplicate, and misaligned batch contracts', () => {
    const mixed = batchResponse();
    mixed.potential_maps.ligand = { dx_gzip_base64: gzipSync(dx).toString('base64') };
    expect(isStructureElectrostaticMapResponse(mixed)).toBe(false);

    const incomplete = batchResponse();
    delete incomplete.report.ligand_atom_counts;
    expect(isStructureElectrostaticMapResponse(incomplete)).toBe(false);

    const duplicate = batchResponse();
    duplicate.potential_maps.ligands!.d01 = duplicate.potential_maps.ligands!.D01;
    duplicate.report.ligand_atom_counts!.d01 = 4;
    duplicate.report.grids.ligands!.d01 = duplicate.report.grids.ligands!.D01;
    expect(isStructureElectrostaticMapResponse(duplicate)).toBe(false);

    const countMismatch = batchResponse();
    countMismatch.report.ligand_atom_counts!.D02 = 6;
    expect(isStructureElectrostaticMapResponse(countMismatch)).toBe(false);

    const misaligned = batchResponse();
    misaligned.report.grids.ligands!.D02.origin = [0.5, 0, 0];
    expect(isStructureElectrostaticMapResponse(misaligned)).toBe(false);
  });

  it('rejects missing or misaligned component potential maps', () => {
    const missing = response();
    delete missing.potential_maps.ligand;
    expect(isStructureElectrostaticMapResponse(missing)).toBe(false);

    const misaligned = response();
    misaligned.report.grids.ligand!.origin = [0.5, 0, 0];
    expect(isStructureElectrostaticMapResponse(misaligned)).toBe(false);
  });

  it('rejects atom, grid, provenance, color-range, and warning values outside the transport contract', () => {
    const oversizedProtein = response();
    oversizedProtein.report.protein_atom_count = 200_001;
    oversizedProtein.report.total_atom_count = 200_001 + oversizedProtein.report.ligand_atom_count;
    expect(isStructureElectrostaticMapResponse(oversizedProtein)).toBe(false);

    const oversizedLegacyLigand = response();
    oversizedLegacyLigand.report.ligand_atom_count = 2_049;
    oversizedLegacyLigand.report.total_atom_count = oversizedLegacyLigand.report.protein_atom_count + 2_049;
    expect(isStructureElectrostaticMapResponse(oversizedLegacyLigand)).toBe(false);

    const oversizedBatch = batchResponse();
    const encoded = oversizedBatch.potential_maps.ligands!.D01;
    const grid = oversizedBatch.report.grids.ligands!.D01;
    oversizedBatch.potential_maps.ligands = {};
    oversizedBatch.report.grids.ligands = {};
    oversizedBatch.report.ligand_atom_counts = {};
    for (let index = 1; index <= 5; index += 1) {
      const key = `D0${index}`;
      oversizedBatch.potential_maps.ligands[key] = encoded;
      oversizedBatch.report.grids.ligands[key] = { ...grid };
      oversizedBatch.report.ligand_atom_counts[key] = 1_800;
    }
    oversizedBatch.report.ligand_atom_count = 9_000;
    oversizedBatch.report.total_atom_count = oversizedBatch.report.protein_atom_count + 9_000;
    expect(isStructureElectrostaticMapResponse(oversizedBatch)).toBe(false);

    const aggregateGridOverflow = batchResponse();
    const largeGrid = {
      counts: [100, 100, 600],
      origin: [0, 0, 0],
      delta: [1, 1, 1],
      value_count: 6_000_000,
      minimum: -1,
      maximum: 1,
    };
    aggregateGridOverflow.report.grids.protein = largeGrid;
    aggregateGridOverflow.potential_maps.ligands = {};
    aggregateGridOverflow.report.grids.ligands = {};
    aggregateGridOverflow.report.ligand_atom_counts = {};
    for (let index = 1; index <= 4; index += 1) {
      const key = `D0${index}`;
      aggregateGridOverflow.potential_maps.ligands[key] = encoded;
      aggregateGridOverflow.report.grids.ligands[key] = { ...largeGrid };
      aggregateGridOverflow.report.ligand_atom_counts[key] = 1;
    }
    aggregateGridOverflow.report.ligand_atom_count = 4;
    aggregateGridOverflow.report.total_atom_count = aggregateGridOverflow.report.protein_atom_count + 4;
    expect(isStructureElectrostaticMapResponse(aggregateGridOverflow)).toBe(false);

    const wrongColorRange = response();
    wrongColorRange.report.color_range = [-10, 10];
    expect(isStructureElectrostaticMapResponse(wrongColorRange)).toBe(false);

    const invalidDigest = response();
    invalidDigest.report.input_sha256 = 'not-a-digest';
    expect(isStructureElectrostaticMapResponse(invalidDigest)).toBe(false);

    const invalidProvenance = response();
    invalidProvenance.report.preparation_engine = 'unknown';
    expect(isStructureElectrostaticMapResponse(invalidProvenance)).toBe(false);

    const mismatchedChargeMethod = response();
    mismatchedChargeMethod.report.ligand_charge_method = 'not applicable';
    expect(isStructureElectrostaticMapResponse(mismatchedChargeMethod)).toBe(false);

    const invalidWarning = response();
    invalidWarning.report.warnings = [''];
    expect(isStructureElectrostaticMapResponse(invalidWarning)).toBe(false);
  });
});

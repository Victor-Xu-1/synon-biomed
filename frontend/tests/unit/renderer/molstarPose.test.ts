import { describe, expect, it } from 'vitest';
import {
  formatMolstarPosePdb,
  type MolstarPoseAtom,
} from '@/renderer/pages/conversation/Preview/components/viewers/molstarPose';

describe('Mol* pose export', () => {
  it('writes a portable PDB snapshot with atom records and an explicit terminator', () => {
    const content = formatMolstarPosePdb([
      {
        recordName: 'HETATM',
        serial: 1,
        atomName: 'C1',
        residueName: 'LIG',
        chainId: 'A',
        residueNumber: 1,
        x: 1.2345,
        y: -2,
        z: 3.5,
        element: 'C',
      },
      {
        recordName: 'ATOM',
        serial: 2,
        atomName: 'CA',
        residueName: 'ASN',
        chainId: 'B',
        residueNumber: 351,
        x: 4,
        y: 5,
        z: 6,
        element: 'C',
      },
    ]);

    expect(content).toContain('HETATM');
    expect(content).toContain('ATOM');
    expect(content).toContain('  1.234');
    expect(content.endsWith('\nEND\n')).toBe(true);
  });

  it('keeps the export deterministic for an empty pose', () => {
    expect(formatMolstarPosePdb([])).toBe('\nEND\n');
  });

  it('preserves alternate locations and insertion codes while separating colliding chain instances', () => {
    const atoms: MolstarPoseAtom[] = [
      {
        recordName: 'ATOM',
        serial: 1,
        atomName: 'CA',
        residueName: 'ALA',
        chainId: 'A',
        chainKey: 'unit-1:A',
        residueNumber: 12,
        alternateLocation: 'B',
        insertionCode: 'C',
        x: 1,
        y: 2,
        z: 3,
        element: 'C',
      },
      {
        recordName: 'ATOM',
        serial: 2,
        atomName: 'N',
        residueName: 'GLY',
        chainId: 'A',
        chainKey: 'unit-2:A',
        residueNumber: 12,
        x: 4,
        y: 5,
        z: 6,
        element: 'N',
      },
    ];

    const lines = formatMolstarPosePdb(atoms).split('\n');
    expect(lines[0]?.[16]).toBe('B');
    expect(lines[0]?.[21]).toBe('A');
    expect(lines[0]?.[26]).toBe('C');
    expect(lines[1]?.[21]).toBe('B');
    expect(lines[0]).toHaveLength(80);
    expect(lines[1]).toHaveLength(80);
  });

  it('reserves native one-character chain IDs before assigning symmetry-copy fallbacks', () => {
    const atom = (serial: number, chainId: string, chainKey: string): MolstarPoseAtom => ({
      recordName: 'ATOM',
      serial,
      atomName: 'CA',
      residueName: 'ALA',
      chainId,
      chainKey,
      residueNumber: 1,
      x: serial,
      y: 0,
      z: 0,
      element: 'C',
    });
    const lines = formatMolstarPosePdb([
      atom(1, 'A', 'operator-1:A'),
      atom(2, 'A', 'operator-2:A'),
      atom(3, 'B', 'operator-1:B'),
    ]).split('\n');

    expect(lines.map((line) => line[21]).slice(0, 3)).toEqual(['A', 'C', 'B']);
  });

  it('fails closed instead of truncating identifiers, serials, coordinates, or chain instances', () => {
    const atom = (overrides: Partial<MolstarPoseAtom> = {}): MolstarPoseAtom => ({
      recordName: 'ATOM',
      serial: 1,
      atomName: 'CA',
      residueName: 'ALA',
      chainId: 'A',
      residueNumber: 1,
      x: 0,
      y: 0,
      z: 0,
      element: 'C',
      ...overrides,
    });

    expect(() => formatMolstarPosePdb([atom({ serial: 100_000 })])).toThrow('MOLSTAR_PDB_FIELD_UNREPRESENTABLE:serial');
    expect(() => formatMolstarPosePdb([atom({ recordName: 'INVALID' as MolstarPoseAtom['recordName'] })])).toThrow(
      'MOLSTAR_PDB_FIELD_UNREPRESENTABLE:recordName'
    );
    expect(() => formatMolstarPosePdb([atom({ residueName: 'LONG' })])).toThrow(
      'MOLSTAR_PDB_FIELD_UNREPRESENTABLE:residueName'
    );
    expect(() => formatMolstarPosePdb([atom({ x: 10_000 })])).toThrow('MOLSTAR_PDB_FIELD_UNREPRESENTABLE:x');
    expect(() =>
      formatMolstarPosePdb(
        Array.from({ length: 63 }, (_, index) => atom({ serial: index + 1, chainId: 'A', chainKey: `unit-${index}` }))
      )
    ).toThrow('MOLSTAR_PDB_FIELD_UNREPRESENTABLE:chainId');
  });
});

import { describe, expect, it } from 'vitest';
import {
  mergeDockingEnsembleLigandCoordinates,
  parseDockingEnsemble,
  selectDockingEnsembleEntries,
  selectDockingEnsembleEntry,
} from '@/renderer/pages/conversation/Preview/components/viewers/dockingEnsemble';

const fixture = [
  'REMARK 900 SYNON BIOMED DOCKING COMPLEX ENSEMBLE',
  'ATOM      1 CA   GLY A  16      27.817 -18.400  -4.985  1.00 46.96           C',
  'REMARK 900 REFERENCE LIGAND REF SOURCE G7I CHAIN A RESIDUE 201',
  'HETATM    2 C1   REF Z   1      30.588 -29.829   3.037  1.00 18.75           C',
  'REMARK 900 DOCKED LIGAND D01 CANDIDATE MDM2-012 RANK 1 POSE 1 AFFINITY -10.026 KCAL/MOL',
  'HETATM    3 C1   D01 Z 101      31.000 -28.000   2.000  1.00  0.00           C',
  'REMARK 900 DOCKED LIGAND D02 CANDIDATE MDM2-012 RANK 1 POSE 2 AFFINITY -9.901 KCAL/MOL',
  'HETATM    4 C1   D02 Z 102      32.000 -27.000   1.000  1.00  0.00           C',
  'REMARK 900 DOCKED LIGAND D03 CANDIDATE MDM2-025 RANK 2 POSE 1 AFFINITY -9.876 KCAL/MOL',
  'HETATM    5 C1   D03 Z 103      33.000 -26.000   0.000  1.00  0.00           C',
  'END',
].join('\n');

describe('docking ensemble projection', () => {
  it('discovers the reference and ranked candidates from the self-describing PDB', () => {
    const ensemble = parseDockingEnsemble(fixture);
    expect(ensemble?.entries).toEqual([
      {
        residueName: 'REF',
        candidateId: 'G7I',
        rank: 0,
        poseRank: 0,
        kind: 'reference',
      },
      {
        residueName: 'D01',
        candidateId: 'MDM2-012',
        rank: 1,
        poseRank: 1,
        affinityKcalMol: -10.026,
        kind: 'candidate',
      },
      {
        residueName: 'D02',
        candidateId: 'MDM2-012',
        rank: 1,
        poseRank: 2,
        affinityKcalMol: -9.901,
        kind: 'candidate',
      },
      {
        residueName: 'D03',
        candidateId: 'MDM2-025',
        rank: 2,
        poseRank: 1,
        affinityKcalMol: -9.876,
        kind: 'candidate',
      },
    ]);
  });

  it('accepts the canonical execution-pack run provenance without falling back to an all-ligand view', () => {
    const currentExecutionPack = fixture.replace(
      'AFFINITY -10.026 KCAL/MOL',
      'AFFINITY -10.026 KCAL/MOL RUN 3 SOURCE_MODE 1'
    );
    const ensemble = parseDockingEnsemble(currentExecutionPack);

    expect(ensemble?.entries[1]).toMatchObject({
      residueName: 'D01',
      candidateId: 'MDM2-012',
      rank: 1,
      poseRank: 1,
      affinityKcalMol: -10.026,
    });
    const selected = selectDockingEnsembleEntry(ensemble!, 1);
    expect(selected).toContain('HETATM    3 C1   D01');
    expect(selected).not.toContain('HETATM    2 C1   REF');
    expect(selected).not.toContain('HETATM    4 C1   D02');
    expect(selected).not.toContain('HETATM    5 C1   D03');
  });

  it('reads a real reference-ligand Vina rescore when the execution pack records one', () => {
    const rescored = fixture.replace(
      'REFERENCE LIGAND REF SOURCE G7I CHAIN A RESIDUE 201',
      'REFERENCE LIGAND REF SOURCE G7I CHAIN A RESIDUE 201 AFFINITY -10.442 KCAL/MOL'
    );

    expect(parseDockingEnsemble(rescored)?.entries[0]).toMatchObject({
      candidateId: 'G7I',
      kind: 'reference',
      affinityKcalMol: -10.442,
    });
  });

  it('keeps one fixed receptor and exactly one selected ligand in the display projection', () => {
    const ensemble = parseDockingEnsemble(fixture);
    expect(ensemble).not.toBeNull();
    const selected = selectDockingEnsembleEntry(ensemble!, 2);

    expect(selected).toContain('ATOM      1 CA   GLY A  16');
    expect(selected).toContain('HETATM    4 C1   D02');
    expect(selected).not.toContain('HETATM    2 C1   REF');
    expect(selected).not.toContain('HETATM    3 C1   D01');
    expect(selected).not.toContain('HETATM    5 C1   D03');
    expect(selected.endsWith('\nEND\n')).toBe(true);
  });

  it('projects any two distinct ligands for comparison while keeping one receptor', () => {
    const ensemble = parseDockingEnsemble(fixture);
    const selected = selectDockingEnsembleEntries(ensemble!, [3, 0]);

    expect(selected.match(/^ATOM/gm)).toHaveLength(1);
    expect(selected).toContain('HETATM    2 C1   REF');
    expect(selected).toContain('HETATM    5 C1   D03');
    expect(selected).not.toContain('HETATM    3 C1   D01');
    expect(selected).not.toContain('HETATM    4 C1   D02');
    expect(selected.indexOf('HETATM    5 C1   D03')).toBeLessThan(selected.indexOf('HETATM    2 C1   REF'));
  });

  it('merges only the minimized ligand coordinates and preserves the ensemble contract', () => {
    const ensemble = parseDockingEnsemble(fixture)!;
    const minimizedPose = [
      'REMARK 900 DOCKING POSE PREVIEW',
      'ATOM      1 CA   GLY A  16      99.999  99.999  99.999  1.00 46.96           C',
      'HETATM    3 C1   D01 Z 101      31.750 -28.250   2.500  1.00  0.00           C',
      'END',
    ].join('\n');

    const merged = mergeDockingEnsembleLigandCoordinates(ensemble, 'D01', minimizedPose);

    expect(merged.source).toContain('REMARK 900 SYNON BIOMED DOCKING COMPLEX ENSEMBLE');
    expect(merged.source).toContain('ATOM      1 CA   GLY A  16      27.817 -18.400  -4.985');
    expect(merged.source).toContain('HETATM    3 C1   D01 Z 101      31.750 -28.250   2.500');
    expect(merged.source).toContain('HETATM    4 C1   D02 Z 102      32.000 -27.000   1.000');
    expect(merged.entries).toEqual(ensemble.entries);
  });

  it('leaves ordinary PDB structures on the standard preview path', () => {
    expect(parseDockingEnsemble('HEADER ORDINARY\nATOM      1  N   MET A   1\nEND')).toBeNull();
  });
});

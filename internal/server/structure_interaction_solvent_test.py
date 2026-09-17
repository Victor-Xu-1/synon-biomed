"""Focused physical and depiction regressions for pocket solvent accessibility."""
import math
import unittest
from contextlib import ExitStack
from pathlib import Path
from tempfile import TemporaryDirectory
from types import SimpleNamespace
from unittest.mock import patch

import numpy as np
from rdkit import Chem
from rdkit.Chem import rdFreeSASA

from structure_interaction_solvent import receptor_surface_exposure
import structure_interaction_diagram as analysis
from structure_interaction_diagram import (
    fibonacci_sphere, ligand_solvent_exposure, external_solvent_grid, points_connected_to_external_solvent,
)


class ReceptorExposureTest(unittest.TestCase):
    def test_report_only_does_not_run_new_or_existing_depiction_computations(self):
        molecule=Chem.MolFromSmiles('CC')
        records=[{'kind':'hydrogen-bond','distance':2.8}]
        protein=SimpleNamespace(GetNumAtoms=lambda:10)
        fingerprint=SimpleNamespace(generate=lambda *a,**k:{})
        replacements={
            'bounded_text':'pdb', 'split_complex':('protein','ligand',[(0.,0.,0.),(1.5,0.,0.)]),
            'pocket_residues':[], 'localized_protein_block':'END',
            'ligand_from_smiles':(molecule,molecule),
            'MoleculeStandardizer':lambda path:protein, 'interaction_records':records,
        }
        with TemporaryDirectory() as directory, ExitStack() as stack:
            for name,value in replacements.items():
                stack.enter_context(patch.object(analysis,name,return_value=value))
            stack.enter_context(patch.object(analysis.plf,'Fingerprint',return_value=fingerprint))
            for name in ('ligand_solvent_exposure','pocket_receptor_exposure','render_publication_svg'):
                stack.enter_context(patch.object(analysis,name,side_effect=AssertionError('slow computation on quick path')))
            args=SimpleNamespace(input=str(Path(directory)/'input.pdb'),ligand_residue_name='LIG',
                                 smiles='CC',ligand_label='test',pose_label='',report_only=True)
            report=analysis.analyze(args)
        self.assertFalse(report['svg'])
        self.assertEqual(report['interactions'],records)
        self.assertEqual(report['solvent_exposure'],[])
        self.assertEqual(report['solvent_openings'],[])

    def compute(self, protein, ligand, bulk=True):
        protein = np.asarray(protein,dtype=float).reshape(-1,3)
        ligand = np.asarray(ligand,dtype=float).reshape(-1,3)
        return receptor_surface_exposure(
            protein,np.full(len(protein),1.7), [('A',1,'ALA')]*len(protein),
            ligand,np.full(len(ligand),1.7),{('A',1,'ALA')},fibonacci_sphere(2048),
            lambda points: np.full(len(points),bulk,dtype=bool),
        )[('A',1,'ALA')]

    def test_isolated_sphere_has_analytic_area_and_full_exposure(self):
        result=self.compute([[0,0,0]],[])
        self.assertAlmostEqual(result['accessible_area'],4*math.pi*3.1**2)
        self.assertAlmostEqual(result['fraction'],1)

    def test_selected_ligand_occludes_residue_surface(self):
        result=self.compute([[0,0,0]],[[3,0,0]])
        # Equal expanded spheres: remaining fraction = 1/2 + d/(4r).
        self.assertAlmostEqual(result['fraction'],.5+3/(4*3.1),delta=.01)

    def test_closed_cavity_is_not_bulk_exposure(self):
        result=self.compute([[0,0,0]],[],False)
        self.assertEqual(result['accessible_area'],0)
        self.assertEqual(result['fraction'],0)
        self.assertGreater(result['local_sasa_area'],100)

    def test_real_grid_distinguishes_closed_shell_from_open_mouth(self):
        shell = sorted({(x,y,z) for x in range(-8,9,2) for y in range(-8,9,2)
                        for z in range(-8,9,2) if max(abs(x),abs(y),abs(z))==8})
        fractions=[]
        for open_mouth in (False,True):
            atoms=np.asarray([p for p in shell if not(open_mouth and p[2]==8 and abs(p[0])<=4 and abs(p[1])<=4)],dtype=float)
            external,lower=external_solvent_grid(np.asarray([[0.,0.,0.]]),np.asarray([1.7]),atoms,np.full(len(atoms),1.7))
            protein=np.vstack(([[0.,0.,0.]],atoms))
            result=receptor_surface_exposure(
                protein,np.full(len(protein),1.7),[('A',1,'ALA')]+[('A',2,'GLY')]*len(atoms),
                np.empty((0,3)),np.asarray([]),{('A',1,'ALA')},fibonacci_sphere(512),
                lambda p:points_connected_to_external_solvent(p,external,lower),
            )[('A',1,'ALA')]
            self.assertGreater(result['local_sasa_area'],100)
            fractions.append(result['fraction'])
        self.assertEqual(fractions[0],0)
        self.assertGreater(fractions[1],.5)

    def test_surface_matches_independent_freesasa_not_a_visual_fixture(self):
        protein=[[0,0,0],[2.8,.5,0],[1,2.7,1]]
        ligand=[[3,3,1],[-2,0,1]]
        result=self.compute(protein,ligand)
        mol=Chem.RWMol()
        conformer=Chem.Conformer(5)
        for i,point in enumerate(protein+ligand):
            mol.AddAtom(Chem.Atom(6))
            conformer.SetAtomPosition(i,point)
        mol.AddConformer(conformer)
        opts=rdFreeSASA.SASAOpts()
        opts.probeRadius=1.4
        rdFreeSASA.CalcSASA(mol,[1.7]*5,opts=opts)
        independent=sum(float(mol.GetAtomWithIdx(i).GetProp('SASA')) for i in range(3))
        self.assertAlmostEqual(result['local_sasa_area'],independent,delta=3.0)

    def test_context_includes_non_pocket_receptor_occluders(self):
        result=receptor_surface_exposure(
            np.asarray([[0,0,0],[3,0,0.]]),np.asarray([1.7,1.7]),
            [('A',1,'ALA'),('A',2,'GLY')],np.empty((0,3)),np.asarray([]),
            {('A',1,'ALA')},fibonacci_sphere(512),lambda p:np.ones(len(p),dtype=bool),
        )
        self.assertEqual(set(result),{('A',1,'ALA')})
        self.assertLess(result[('A',1,'ALA')]['fraction'],.8)

    def test_unavailable_residue_is_not_fabricated(self):
        with self.assertRaises(ValueError):
            receptor_surface_exposure(
                np.asarray([[0,0,0.]]),np.asarray([1.7]),[('A',1,'ALA')],
                np.empty((0,3)),np.asarray([]),{('A',2,'GLY')},fibonacci_sphere(512),
                lambda p:np.ones(len(p),dtype=bool),
            )

    def test_private_directions_preserve_public_exposure_records(self):
        molecule=Chem.MolFromSmiles('CC')
        pdb='ATOM      1  C   ALA A   1       4.000   0.000   0.000  1.00  0.00           C\nEND'
        coordinates=[(0.,0.,0.),(1.5,0.,0.)]
        expected=ligand_solvent_exposure(molecule,coordinates,pdb)
        patches={}
        actual=ligand_solvent_exposure(molecule,coordinates,pdb,accessible_patches=patches)
        self.assertEqual(actual,expected)
        self.assertEqual({i:len(p) for i,p in patches.items()},
                         {item['atom_index']:item['accessible_sample_count'] for item in actual})
        self.assertTrue(all('accessible_patches' not in item for item in actual))


if __name__=='__main__':
    unittest.main()

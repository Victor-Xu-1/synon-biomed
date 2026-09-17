from __future__ import annotations

import math
import unittest
import xml.etree.ElementTree as ET
from types import SimpleNamespace

import numpy as np
from rdkit import Chem
from rdkit.Chem import AllChem

from structure_interaction_diagram import (
    POCKET_RADIUS,
    external_solvent_grid,
    interaction_records,
    interaction_strength,
    ligand_solvent_exposure,
    normalized_element,
    pocket_residues,
    solvent_opening_regions,
)
from structure_interaction_renderer import (
    distribute_residue_slots,
    enforce_residues_outside_pocket,
    pocket_wall_radius,
    projected_pocket_paths,
    projected_opening_direction,
    representative_interactions,
    resolve_outside_residue_collisions,
    render_publication_svg,
)


def pdb_line(atom_name: str, element: str) -> str:
    return f"HETATM    1 {atom_name:<4} LIG Z   1       0.000   0.000   0.000  1.00  0.00          {element:>2}"


class InteractionEngineUnitTest(unittest.TestCase):
    def test_exposure_layer_is_below_real_rdkit_drawing_without_invented_walls(self):
        molecule = Chem.MolFromSmiles("CCO")
        svg = render_publication_svg(
            molecule, [], [], [(0,0,0),(1.5,0,0),(2.5,1,0)],
            [{'atom_index':0,'accessible_fraction':.12},
             {'atom_index':1,'accessible_fraction':0},
             {'atom_index':2,'accessible_fraction':.35}],
            'Ethanol', 'Test pose', 'test', 'test',
        )
        root = ET.fromstring(svg)
        ns = {'s':'http://www.w3.org/2000/svg'}
        exposure = root.find("s:g[@class='solvent-exposure']",ns)
        ligand = root.find("s:g[@transform='translate(210 82)']",ns)
        self.assertLess(list(root).index(exposure),list(root).index(ligand))
        self.assertEqual([e.get('data-exposure-atom') for e in exposure],['0','2'])
        self.assertEqual(len(root.find("s:g[@class='projected-pocket-walls']",ns)),0)
        self.assertNotIn('solvent-channel',svg)

    def test_interaction_records_include_real_three_dimensional_endpoints(self) -> None:
        ligand = Chem.AddHs(Chem.MolFromSmiles("CO"))
        protein = Chem.AddHs(Chem.MolFromSmiles("N"))
        self.assertEqual(AllChem.EmbedMolecule(ligand, randomSeed=7), 0)
        self.assertEqual(AllChem.EmbedMolecule(protein, randomSeed=11), 0)
        residue = SimpleNamespace(name="ASN", number=739, chain="A")
        records = interaction_records(
            {
                (None, residue): {
                    "Hydrophobic": [
                        {
                            "indices": {"ligand": [0]},
                            "parent_indices": {"ligand": [0], "protein": [0]},
                            "distance": 3.4,
                        }
                    ]
                }
            },
            ligand,
            protein,
        )

        self.assertEqual(len(records), 1)
        self.assertEqual(len(records[0]["ligand_position"]), 3)
        self.assertEqual(len(records[0]["protein_position"]), 3)
        self.assertTrue(all(math.isfinite(value) for value in records[0]["ligand_position"]))

    def test_external_solvent_grid_rejects_unbounded_geometry(self) -> None:
        with self.assertRaisesRegex(ValueError, "bounded cell limit"):
            external_solvent_grid(
                np.asarray([[0.0, 0.0, 0.0], [1000.0, 0.0, 0.0]]),
                np.asarray([1.7, 1.7]),
                np.asarray([[0.0, 4.0, 0.0]]),
                np.asarray([1.7]),
            )

    def test_projected_solvent_opening_preserves_measured_direction(self) -> None:
        projection = np.asarray([[-1.0, 0.0], [0.0, 1.0], [0.0, 0.0]])

        direction = projected_opening_direction([1.0, 0.0, 0.0], projection)

        self.assertLess(direction[0], 0.0)
        self.assertIsNone(projected_opening_direction([0.,0.,1.],projection))

    def test_pocket_residue_contract_matches_molstar_radius(self) -> None:
        self.assertEqual(POCKET_RADIUS, 4.5)
        pdb = "\n".join([
            "ATOM      1  C   ALA A   1       4.400   0.000   0.000  1.00  0.00           C",
            "ATOM      2  C   GLY A   2       4.600   0.000   0.000  1.00  0.00           C",
            "END",
        ])

        residues = pocket_residues(pdb, [(0.0, 0.0, 0.0)])

        self.assertEqual([(item["name"], item["number"]) for item in residues], [("ALA", 1)])

    def test_recovers_mixed_case_halogen_name_from_truncated_element_column(self) -> None:
        self.assertEqual(normalized_element(pdb_line("Cl", "C").ljust(80)), "Cl")
        self.assertEqual(normalized_element(pdb_line("Br", "B").ljust(80)), "Br")
        self.assertEqual(normalized_element(pdb_line("CA", "C").ljust(80)), "C")

    def test_selects_shortest_distinct_representative_contacts(self) -> None:
        records = [
            {"kind": "hydrogen-bond", "direction": "protein-to-ligand", "distance": 3.4, "ligand_atom_indices": [2]},
            {"kind": "hydrogen-bond", "direction": "protein-to-ligand", "distance": 2.8, "ligand_atom_indices": [2]},
            {"kind": "hydrogen-bond", "direction": "protein-to-ligand", "distance": 3.0, "ligand_atom_indices": [5]},
            {"kind": "hydrogen-bond", "direction": "protein-to-ligand", "distance": 3.2, "ligand_atom_indices": [7]},
            {"kind": "hydrophobic", "direction": None, "distance": 3.1, "ligand_atom_indices": [8]},
        ]

        selected = representative_interactions(records)

        self.assertEqual([(record["distance"], record["ligand_atom_indices"]) for record in selected], [(2.8, [2])])

        vdw_selected = representative_interactions([
            {"kind": "vdw-contact", "direction": None, "distance": 3.8, "ligand_atom_indices": [4]},
            {"kind": "vdw-contact", "direction": None, "distance": 3.3, "ligand_atom_indices": [4]},
        ])
        self.assertEqual([(record["distance"], record["ligand_atom_indices"]) for record in vdw_selected], [(3.3, [4])])

    def test_projected_pocket_walls_are_open_and_contact_derived(self) -> None:
        atom_points = {0: (400.0, 400.0), 1: (600.0, 400.0), 2: (500.0, 600.0)}
        residues = [
            {"angle": -2.4, "projected_contact": (360.0, 330.0)},
            {"angle": -0.7, "projected_contact": (650.0, 330.0)},
            {"angle": 0.7, "projected_contact": (650.0, 650.0)},
            {"angle": 2.4, "projected_contact": (360.0, 650.0)},
        ]

        paths = projected_pocket_paths(atom_points, (500.0, 470.0), residues)

        # Local contact-supported fragments must not be forced into two
        # continuous walls across unsupported angular sectors.
        self.assertGreaterEqual(len(paths), 2)
        self.assertTrue(all(path.startswith("M ") for path in paths))
        self.assertTrue(all(not path.endswith("Z") for path in paths))

        split_paths = projected_pocket_paths(
            atom_points,
            (500.0, 470.0),
            residues,
            # Put mouths on contact-supported contour sectors. The old x-only
            # split fixture placed mouths in already unsupported gaps.
            opening_points=[(600.0, 374.0), (575.0, 508.0)],
        )
        self.assertTrue(split_paths)
        self.assertNotEqual(split_paths, paths)

    def test_solvent_exposure_is_pose_derived_and_classified(self) -> None:
        molecule = Chem.MolFromSmiles("CC")
        pdb = "\n".join([
            "ATOM      1  C   ALA A   1       0.000   0.000   3.000  1.00  0.00           C",
            "ATOM      2  C   ALA A   1       0.000   0.000  -3.000  1.00  0.00           C",
            "END",
        ])

        result = ligand_solvent_exposure(molecule, [(0.0, 0.0, 0.0), (1.5, 0.0, 0.0)], pdb)

        self.assertEqual(len(result), 2)
        self.assertTrue(all(0.0 <= item["accessible_fraction"] <= 1.0 for item in result))
        self.assertTrue(all(item["classification"] in {"buried", "partially-exposed", "solvent-exposed"} for item in result))
        self.assertTrue(all(len(item["accessible_direction_3d"]) == 3 for item in result))
        self.assertTrue(all(item["accessible_sample_count"] >= 0 for item in result))

    def test_adjacent_accessible_atoms_form_one_opening_region(self) -> None:
        molecule = Chem.MolFromSmiles("CCC")
        exposure = [
            {"atom_index": 0, "accessible_fraction": 0.32},
            {"atom_index": 1, "accessible_fraction": 0.18},
            {"atom_index": 2, "accessible_fraction": 0.02},
        ]

        regions = solvent_opening_regions(molecule, exposure)

        self.assertEqual(regions, [{
            "atom_indices": [0, 1],
            "mean_accessible_fraction": 0.25,
            "maximum_accessible_fraction": 0.32,
            "direction_3d": [0.0, 0.0, 0.0],
            "accessible_sample_count": 0,
        }])

    def test_opposite_bulk_solvent_directions_remain_distinct_openings(self) -> None:
        molecule = Chem.MolFromSmiles("CC")
        exposure = [
            {
                "atom_index": 0,
                "accessible_fraction": 0.25,
                "accessible_direction_3d": [1.0, 0.0, 0.0],
                "accessible_sample_count": 24,
                "coordinate_3d": [0.0, 0.0, 0.0],
            },
            {
                "atom_index": 1,
                "accessible_fraction": 0.25,
                "accessible_direction_3d": [-1.0, 0.0, 0.0],
                "accessible_sample_count": 24,
                "coordinate_3d": [1.5, 0.0, 0.0],
            },
        ]

        regions = solvent_opening_regions(molecule, exposure)

        self.assertEqual(len(regions), 2)
        self.assertEqual({tuple(region["direction_3d"]) for region in regions}, {(1.0, 0.0, 0.0), (-1.0, 0.0, 0.0)})

    def test_residue_labels_are_projected_outside_the_pocket_wall(self) -> None:
        atom_points = {0: (400.0, 500.0), 1: (600.0, 500.0)}
        center = (500.0, 500.0)
        item = {
            "angle": 0.0,
            "position": (620.0, 500.0),
            "projected_contact": (640.0, 500.0),
        }

        enforce_residues_outside_pocket([item], atom_points, center)

        wall_radius = pocket_wall_radius(list(atom_points.values()), center, item, 0.0)
        label_radius = item["position"][0] - center[0]
        self.assertGreaterEqual(label_radius, wall_radius + 68.0)

    def test_outer_residue_collisions_are_resolved_tangentially(self) -> None:
        atom_points = {0: (400.0, 500.0), 1: (600.0, 500.0)}
        center = (500.0, 500.0)
        items = [
            {"angle": math.pi, "position": (250.0, 490.0), "projected_contact": (360.0, 500.0)},
            {"angle": math.pi, "position": (250.0, 500.0), "projected_contact": (360.0, 500.0)},
            {"angle": math.pi, "position": (250.0, 510.0), "projected_contact": (360.0, 500.0)},
        ]

        resolve_outside_residue_collisions(items, atom_points, center, minimum_separation=100.0)

        for first_index, first in enumerate(items):
            for second in items[first_index + 1 :]:
                self.assertGreaterEqual(
                    math.hypot(
                        second["position"][0] - first["position"][0],
                        second["position"][1] - first["position"][1],
                    ),
                    98.0,
                )

    def test_bottom_boundary_labels_keep_a_readable_lane_gap(self) -> None:
        atom_points = {0: (400.0, 500.0), 1: (600.0, 500.0)}
        center = (500.0, 500.0)
        items = [
            {"angle": 1.2, "position": (1180.0, 820.0), "projected_contact": (580.0, 560.0)},
            {"angle": 1.4, "position": (1235.0, 820.0), "projected_contact": (560.0, 580.0)},
            {"angle": 1.6, "position": (1310.0, 820.0), "projected_contact": (520.0, 590.0)},
        ]

        resolve_outside_residue_collisions(items, atom_points, center)

        for first_index, first in enumerate(items):
            for second in items[first_index + 1 :]:
                self.assertGreaterEqual(
                    math.hypot(
                        second["position"][0] - first["position"][0],
                        second["position"][1] - first["position"][1],
                    ),
                    118.0,
                )

    def test_interaction_strength_uses_potential_not_display_distance(self) -> None:
        strong = interaction_strength(
            "hydrogen-bond",
            3.0,
            {
                "vina_hbond_potential": 0.92,
                "donor_atom_angle_deviation": 4.0,
                "acceptor_atom_angle_deviation": 8.0,
            },
        )
        weak = interaction_strength(
            "hydrogen-bond",
            3.4,
            {
                "vina_hbond_potential": 0.34,
                "donor_atom_angle_deviation": 20.0,
                "acceptor_atom_angle_deviation": 45.0,
            },
        )

        self.assertEqual(strong["strength_level"], "strong")
        self.assertEqual(weak["strength_level"], "weak")
        self.assertGreater(strong["strength_index"], weak["strength_index"])

    def test_vdw_strength_uses_surface_complementarity(self) -> None:
        result = interaction_strength("vdw-contact", 3.40, {}, vdw_radius_sum=3.70)

        self.assertGreater(result["strength_index"], 0.8)
        self.assertEqual(result["strength_level"], "strong")

    def test_all_residues_use_even_outer_slots_with_interaction_preference(self) -> None:
        atom_points = {0: (450.0, 500.0), 1: (550.0, 500.0)}
        center = (500.0, 500.0)
        items = [
            {
                "key": ("A", index, "GLY"),
                "angle": index * 0.08,
                "wall_angle": index * 0.08,
                "position": (650.0, 500.0 + index),
                "projected_contact": (620.0, 500.0),
            }
            for index in range(8)
        ]
        items[0]["interaction_anchor_angle"] = 0.0
        items[0]["interaction_anchor_priority"] = 0.9

        distribute_residue_slots(items, atom_points, center)

        ordered = sorted(items, key=lambda item: float(item["angle"]) % math.tau)
        neighbour_distances = [
            math.hypot(
                ordered[(index + 1) % len(ordered)]["position"][0] - item["position"][0],
                ordered[(index + 1) % len(ordered)]["position"][1] - item["position"][1],
            )
            for index, item in enumerate(ordered)
        ]
        self.assertGreater(min(neighbour_distances), 120.0)
        self.assertLess(max(neighbour_distances) / min(neighbour_distances), 1.25)
        interaction_delta = (float(items[0]["angle"]) + math.pi) % math.tau - math.pi
        self.assertLess(abs(interaction_delta), 0.65)


if __name__ == "__main__":
    unittest.main()

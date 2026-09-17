"""Focused regressions for schematic pocket boundary safety."""
import math
import unittest
import xml.etree.ElementTree as ET

from structure_interaction_pocket_geometry import (
    WALL_CLEARANCE, boundary_segments, boundary_contours,
    drawing_points, solvent_halos, solvent_boundary_segments,
)


class PocketGeometryTest(unittest.TestCase):
    def test_openings_represent_multiple_atoms_and_do_not_flip_to_other_side(self):
        atoms = {0:(600.,450.),1:(950.,450.)}
        seeds = [(x,450.) for x in range(600,951,10)]
        patches={0:[(-1.,0.)],1:[(1.,0.)]}
        arcs=solvent_boundary_segments(seeds,atoms,patches,[])
        self.assertTrue(any(p[0]<550 for arc in arcs for p in arc))
        self.assertTrue(any(p[0]>1000 for arc in arcs for p in arc))
        self.assertFalse(any(700<p[0]<850 for arc in arcs for p in arc))
        self.assertEqual(solvent_boundary_segments(seeds,atoms,{},[]),[])

    def test_local_outline_retains_a_deep_concave_bay(self):
        # A U-shaped drawing must not be replaced by a straight convex-hull lid.
        points = [(x, 600.) for x in range(600, 1001, 10)]
        points += [(600., y) for y in range(300, 601, 10)]
        points += [(1000., y) for y in range(300, 601, 10)]
        contours = boundary_contours(points)
        self.assertEqual(len(contours), 1)
        contour = contours[0]
        self.assertTrue(any(740 < x < 860 and 500 < y < 580 for x, y in contour))

    def setUp(self):
        self.points = [(x, y) for x in range(700,901,10) for y in (350.,550.)]
        self.points += [(x, y) for y in range(350,551,10) for x in (700.,900.)]
        self.center = (800., 450.)

    def assert_safe(self, segments, points):
        for segment in segments:
            for start, end in zip(segment, segment[1:]):
                # Check each rendered chord, not only its safe anchors.
                for step in range(11):
                    sample = (start[0] + (end[0] - start[0]) * step / 10,
                              start[1] + (end[1] - start[1]) * step / 10)
                    # All SVG strokes are sampled <=10px apart. Reserve a
                    # further 5px for the unsampled midpoint of each bond.
                    distance = min(math.dist(sample, p) for p in points)
                    self.assertGreaterEqual(distance, WALL_CLEARANCE + 5)

    def test_opposite_contacts_do_not_join_across_ligand(self):
        segments = boundary_segments(self.points, self.center, [-math.pi + .1, -.1], [], [])
        self.assertEqual(len(segments), 2)
        self.assert_safe(segments, self.points)
        for segment in segments:
            self.assertFalse(min(p[0] for p in segment) < 700 and max(p[0] for p in segment) > 900)

    def test_dense_support_stays_outside_all_bonds_and_corners(self):
        segments = boundary_segments(self.points, self.center, [n * math.pi / 8 for n in range(16)], [], [])
        self.assertTrue(segments)
        self.assert_safe(segments, self.points)

    def test_solvent_opening_remains_a_gap(self):
        mouth = min(boundary_contours(self.points)[0],key=lambda p:math.dist(p,(960,450)))
        segments = boundary_segments(self.points, self.center, [0], [mouth], [])
        self.assertTrue(segments)
        for segment in segments:
            for x, y in segment:
                self.assertGreaterEqual(math.dist((x,y), mouth),42)

    def test_labels_and_viewport_are_not_crossed(self):
        label = (926,450)
        segments = boundary_segments(self.points, self.center, [0, math.pi], [], [label])
        self.assertTrue(segments)
        for segment in segments:
            for p in segment:
                self.assertGreaterEqual(math.dist(p,label),74)
                self.assertTrue(32 < p[0] < 1568 and 32 < p[1] < 894)

    def test_no_contact_evidence_means_no_wall(self):
        self.assertEqual(boundary_segments(self.points,self.center,[],[],[]),[])
        self.assertEqual(boundary_segments([],self.center,[0],[],[]),[])

    def test_rdkit_glyph_control_points_expand_clearance(self):
        glyph = '<path class="atom-0" d="M 210 40 Q 220 30 230 40 L 230 60 Z"/>'
        points = self.points + drawing_points(glyph,(700,350))
        segments = boundary_segments(points,self.center,[0],[],[])
        self.assertTrue(segments)
        self.assert_safe(segments,points)

    def test_collinear_inputs_are_finite(self):
        segments = boundary_segments([(700,450),(900,450)],self.center,[0,math.pi],[],[])
        self.assertTrue(segments)
        self.assertTrue(all(math.isfinite(v) for s in segments for p in s for v in p))

    def test_contour_has_bounded_chords_and_no_extra_spline(self):
        for points in boundary_contours(self.points):
            self.assertTrue(all(math.dist(a,b) <= 4.51 for a,b in zip(points,points[1:]+points[:1])))

    def test_glyph_parser_accepts_exponents_but_rejects_unknown_commands(self):
        points = drawing_points('<path d="M 1e2 20 L 110 30 Z"/>',(0,0))
        self.assertIn((100,20), points)
        self.assertIn((110,30), points)
        with self.assertRaises(ValueError):
            drawing_points('<path d="M 100 20 A 5 5 0 1 1 110 30"/>',(0,0))
        with self.assertRaises(ValueError):
            drawing_points('<path d="M 1e99 0 L 0 0"/>',(0,0))

    def test_closed_ring_emits_only_its_outer_boundary(self):
        points = [(800+120*math.cos(n*math.tau/100),450+120*math.sin(n*math.tau/100)) for n in range(100)]
        contours = boundary_contours(points)
        self.assertEqual(len(contours),1)
        self.assertTrue(all(math.dist(p,(800,450))>165 for p in contours[0]))

    def test_caching_is_order_independent_and_does_not_mutate_inputs(self):
        original = self.points[:]
        first = boundary_contours(self.points)
        self.assertIs(first,boundary_contours(list(reversed(self.points))))
        self.assertEqual(self.points,original)

    def test_separate_components_do_not_acquire_an_invented_bridge(self):
        contours = boundary_contours([(500,450),(1100,450)])
        self.assertEqual(len(contours),2)
        self.assertTrue(all(not any(650 < x < 950 for x,y in c) for c in contours))

    def test_local_outline_rejects_unbounded_and_nonfinite_inputs(self):
        for points in ([(math.nan,450)], [(5000,450)], [(0,0)]*60001):
            with self.assertRaises(ValueError):
                boundary_contours(points)

    def test_exposure_shading_uses_all_exposed_atoms_not_only_region_representatives(self):
        atoms = {i:(400+i*50,400) for i in range(7)}
        exposure = [{'atom_index':i,'accessible_fraction':fraction}
                    for i,fraction in enumerate([0,.079,.08,.12,.2,.4,1])]
        root = ET.fromstring('<svg>'+solvent_halos(atoms,exposure)+'</svg>')
        circles = list(root)
        self.assertEqual([int(e.get('data-exposure-atom')) for e in circles],[2,3,4,5,6])
        self.assertEqual([float(e.get('data-solvent-exposure')) for e in circles],[.08,.12,.2,.4,1])
        self.assertEqual([float(e.get('cx')) for e in circles],[500,550,600,650,700])
        self.assertEqual(sorted(float(e.get('opacity')) for e in circles),[float(e.get('opacity')) for e in circles])
        self.assertTrue(all(e.get('fill')=='url(#ligand-exposure)' for e in circles))

    def test_no_exposure_evidence_means_no_halos(self):
        self.assertEqual(solvent_halos({0:(300,400)},[]),'')
        self.assertEqual(solvent_halos({0:(300,400)},[{'atom_index':0,'accessible_fraction':0}]),'')

    def test_invalid_exposure_is_not_silently_colored(self):
        for item in ({'atom_index':5,'accessible_fraction':.3},
                     {'atom_index':0,'accessible_fraction':float('nan')},
                     {'atom_index':0,'accessible_fraction':1.5}):
            with self.assertRaises(ValueError):
                solvent_halos({0:(300,400)},[item])


if __name__ == "__main__":
    unittest.main()

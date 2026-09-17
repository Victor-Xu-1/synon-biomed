#!/usr/bin/env python3
"""Focused regression tests for single-cell matrix and metadata contracts."""

from __future__ import annotations

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import anndata as ad
import numpy as np
import pandas as pd
from scipy import sparse

SKILL_SCRIPTS = Path(__file__).resolve().parents[1] / "synonbiomed" / "single-cell-rna-analysis" / "scripts"
sys.path.insert(0, str(SKILL_SCRIPTS))

from analysis_core import composition_table
from annotation_core import apply_cluster_annotations, validate_cluster_annotations
from comparison_core import compare_paired_composition
from matrix_manifest import (
    attach_observation_table,
    inspect_wide_matrix,
    load_observation_table,
    load_sparse_wide_matrix,
)


class ObservationTableContractTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temp_dir = tempfile.TemporaryDirectory()
        self.root = Path(self.temp_dir.name)
        self.matrix = self.root / "matrix.tsv"
        self.matrix.write_text("gene\tc3\tc1\tc2\nG1\t1\t2\t0\nG2\t0\t3\t4\n", encoding="utf-8")
        self.observations = self.root / "observations.csv"
        self.observations.write_text(
            "cells,samples,cell.types,treatment.group\n"
            "c1,s1,T,pre\n"
            "c2,s2,B,post\n"
            "c3,s1,T,pre\n"
            "unrelated,s9,B,post\n",
            encoding="utf-8",
        )

    def tearDown(self) -> None:
        self.temp_dir.cleanup()

    def load_aligned_observations(self) -> pd.DataFrame:
        return load_observation_table(
            self.observations,
            id_column="cells",
            column_map={
                "sample_id": "samples",
                "cell_type": "cell.types",
                "treatment": "treatment.group",
            },
            expected_cell_ids=("c3", "c1", "c2"),
        )

    def test_aligns_external_metadata_to_matrix_cell_order(self) -> None:
        observations = self.load_aligned_observations()
        self.assertEqual(list(observations.index), ["c3", "c1", "c2"])
        self.assertEqual(list(observations["sample_id"]), ["s1", "s1", "s2"])
        spec = inspect_wide_matrix(self.matrix)
        adata = attach_observation_table(load_sparse_wide_matrix(spec), observations)
        self.assertEqual(list(adata.obs_names), ["c3", "c1", "c2"])
        self.assertEqual(list(adata.obs["cell_type"]), ["T", "T", "B"])

    def test_rejects_missing_matrix_cells(self) -> None:
        incomplete = self.root / "incomplete.csv"
        incomplete.write_text("cells,samples\nc1,s1\nc2,s2\n", encoding="utf-8")
        with self.assertRaisesRegex(ValueError, "missing 1 matrix cells"):
            load_observation_table(
                incomplete,
                id_column="cells",
                column_map={"sample_id": "samples"},
                expected_cell_ids=("c1", "c2", "c3"),
            )

    def test_rejects_conflicting_embedded_metadata(self) -> None:
        observations = self.load_aligned_observations()
        adata = ad.AnnData(
            X=sparse.csr_matrix(np.ones((3, 1), dtype=np.float32)),
            obs=pd.DataFrame({"sample_id": ["wrong", "s1", "s2"]}, index=observations.index),
        )
        with self.assertRaisesRegex(ValueError, "conflicts with embedded metadata"):
            attach_observation_table(adata, observations)

    def test_composition_is_normalized_within_each_sample(self) -> None:
        adata = ad.AnnData(
            X=sparse.csr_matrix(np.ones((4, 1), dtype=np.float32)),
            obs=pd.DataFrame(
                {
                    "sample_id": ["s1", "s1", "s2", "s2"],
                    "treatment": ["pre", "pre", "post", "post"],
                    "cell_type": ["T", "B", "T", "T"],
                },
                index=["c1", "c2", "c3", "c4"],
            ),
        )
        result = composition_table(adata, ["sample_id", "treatment"], population_key="cell_type")
        totals = result.groupby("sample_id", observed=True)["proportion"].sum()
        self.assertTrue(np.allclose(totals.to_numpy(), 1.0))
        s1_t = result[(result["sample_id"] == "s1") & (result["cell_type"] == "T")]
        self.assertEqual(float(s1_t.iloc[0]["proportion"]), 0.5)


class CompositionComparisonTests(unittest.TestCase):
    def test_uses_only_complete_subject_pairs_and_reports_exact_deltas(self) -> None:
        composition = pd.DataFrame(
            [
                {"patient_id": "p1", "timepoint": "pre", "cluster": "0", "proportion": 0.10},
                {"patient_id": "p1", "timepoint": "post", "cluster": "0", "proportion": 0.30},
                {"patient_id": "p1", "timepoint": "pre", "cluster": "1", "proportion": 0.90},
                {"patient_id": "p1", "timepoint": "post", "cluster": "1", "proportion": 0.70},
                {"patient_id": "p2", "timepoint": "pre", "cluster": "0", "proportion": 0.20},
                {"patient_id": "p2", "timepoint": "post", "cluster": "0", "proportion": 0.25},
                {"patient_id": "p2", "timepoint": "pre", "cluster": "1", "proportion": 0.80},
                {"patient_id": "p2", "timepoint": "post", "cluster": "1", "proportion": 0.75},
                {"patient_id": "p3", "timepoint": "pre", "cluster": "0", "proportion": 1.00},
            ]
        )
        result = compare_paired_composition(
            composition,
            subject_key="patient_id",
            contrast_key="timepoint",
            contrast_a="pre",
            contrast_b="post",
            population_key="cluster",
        )
        self.assertEqual(result.audit["status"], "passed")
        self.assertEqual(result.audit["paired_subjects"], 2)
        self.assertEqual(result.audit["complete_subject_ids"], ["p1", "p2"])
        self.assertEqual(result.audit["incomplete_subject_ids"], ["p3"])
        self.assertEqual(result.audit["population_semantics"], "generated_cluster_ids_unannotated")
        cluster_zero = result.statistics[result.statistics["population"] == "0"].iloc[0]
        self.assertAlmostEqual(float(cluster_zero["mean_delta_b_minus_a"]), 0.125)

    def test_reports_single_pair_as_descriptive_only(self) -> None:
        composition = pd.DataFrame(
            [
                {"patient_id": "p1", "timepoint": "pre", "cell_type": "T", "proportion": 0.4},
                {"patient_id": "p1", "timepoint": "post", "cell_type": "T", "proportion": 0.6},
            ]
        )
        result = compare_paired_composition(
            composition,
            subject_key="patient_id",
            contrast_key="timepoint",
            contrast_a="pre",
            contrast_b="post",
            population_key="cell_type",
        )
        self.assertEqual(result.audit["status"], "descriptive_only_single_pair")
        self.assertTrue(np.isnan(result.statistics.iloc[0]["p_value"]))


class ClusterAnnotationTests(unittest.TestCase):
    def setUp(self) -> None:
        self.markers = pd.DataFrame(
            [
                {"group": "0", "names": "CD3D", "scores": 10.0},
                {"group": "0", "names": "CD8A", "scores": 9.0},
                {"group": "1", "names": "MS4A1", "scores": 12.0},
                {"group": "1", "names": "CD79A", "scores": 8.0},
            ]
        )

    def test_validates_marker_evidence_before_applying_labels(self) -> None:
        mapping = pd.DataFrame(
            [
                {"cluster": "0", "cell_type": "CD8 T", "evidence_markers": "CD3D;CD8A"},
                {"cluster": "1", "cell_type": "B cell", "evidence_markers": "MS4A1;CD79A"},
            ]
        )
        checked = validate_cluster_annotations(
            self.markers,
            mapping,
            observed_clusters=["0", "1"],
        )
        adata = ad.AnnData(
            X=sparse.csr_matrix(np.ones((3, 1), dtype=np.float32)),
            obs=pd.DataFrame({"cluster": ["0", "1", "0"]}, index=["c1", "c2", "c3"]),
        )
        annotated = apply_cluster_annotations(adata, checked)
        self.assertEqual(list(annotated.obs["cell_type"].astype(str)), ["CD8 T", "B cell", "CD8 T"])

    def test_rejects_fabricated_marker_evidence(self) -> None:
        mapping = pd.DataFrame(
            [
                {"cluster": "0", "cell_type": "CD8 T", "evidence_markers": "CD8A"},
                {"cluster": "1", "cell_type": "Treg", "evidence_markers": "FOXP3"},
            ]
        )
        with self.assertRaisesRegex(ValueError, "FOXP3"):
            validate_cluster_annotations(
                self.markers,
                mapping,
                observed_clusters=["0", "1"],
            )


class PipelinePreflightTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temp_dir = tempfile.TemporaryDirectory()
        self.root = Path(self.temp_dir.name)
        self.pipeline = SKILL_SCRIPTS / "analysis_pipeline.py"

    def tearDown(self) -> None:
        self.temp_dir.cleanup()

    def run_preflight(self, *arguments: str) -> dict[str, object]:
        result = subprocess.run(
            [sys.executable, str(self.pipeline), *arguments, "--output-dir", str(self.root / "out"), "--preflight-only"],
            check=True,
            capture_output=True,
            text=True,
        )
        return json.loads(result.stdout)

    def test_manifest_exposes_sample_id_as_available_metadata(self) -> None:
        first = self.root / "first.csv"
        second = self.root / "second.csv"
        first.write_text("gene,c1\nG1,1\nG2,0\n", encoding="utf-8")
        second.write_text("gene,c2\nG1,0\nG2,1\n", encoding="utf-8")
        manifest = self.root / "manifest.csv"
        manifest.write_text(
            f"sample_id,path,treatment\ns1,{first},pre\ns2,{second},post\n",
            encoding="utf-8",
        )
        preflight = self.run_preflight(
            "--manifest",
            str(manifest),
            "--composition-columns",
            "sample_id,treatment",
        )
        self.assertEqual(preflight["status"], "passed")
        self.assertEqual(preflight["sample_count"], 2)
        self.assertEqual(preflight["composition_columns_available"], ["sample_id", "treatment"])

    def test_external_metadata_makes_comparison_and_population_ready(self) -> None:
        matrix = self.root / "matrix.tsv"
        matrix.write_text("gene\tc1\tc2\nG1\t1\t0\nG2\t0\t1\n", encoding="utf-8")
        observations = self.root / "observations.csv"
        observations.write_text(
            "cells,samples,cell.types,treatment.group\nc1,s1,T,pre\nc2,s2,B,post\n",
            encoding="utf-8",
        )
        base = (
            "--matrix",
            str(matrix),
            "--obs-file",
            str(observations),
            "--obs-id-column",
            "cells",
            "--obs-column",
            "sample_id=samples",
            "--obs-column",
            "treatment=treatment.group",
            "--composition-columns",
            "sample_id,treatment",
        )
        missing_population = self.run_preflight(*base, "--population-key", "cell_type")
        self.assertEqual(missing_population["status"], "metadata_mapping_required")
        ready = self.run_preflight(
            *base,
            "--obs-column",
            "cell_type=cell.types",
            "--population-key",
            "cell_type",
            "--subject-key",
            "sample_id",
            "--contrast-key",
            "treatment",
            "--contrast-a",
            "pre",
            "--contrast-b",
            "post",
        )
        self.assertEqual(ready["status"], "passed")
        self.assertEqual(ready["sample_count"], 2)
        self.assertTrue(ready["comparison_ready"])
        self.assertTrue(ready["population_key_available"])
        self.assertTrue(ready["cell_type_claims_supported"])

    def test_generated_clusters_are_not_declared_cell_types(self) -> None:
        first = self.root / "first.csv"
        second = self.root / "second.csv"
        first.write_text("gene,c1\nG1,1\nG2,0\n", encoding="utf-8")
        second.write_text("gene,c2\nG1,0\nG2,1\n", encoding="utf-8")
        manifest = self.root / "manifest.csv"
        manifest.write_text(
            f"sample_id,path,patient_id,timepoint\ns1,{first},p1,pre\ns2,{second},p1,post\n",
            encoding="utf-8",
        )
        preflight = self.run_preflight(
            "--manifest",
            str(manifest),
            "--composition-columns",
            "sample_id,patient_id,timepoint",
            "--subject-key",
            "patient_id",
            "--contrast-key",
            "timepoint",
            "--contrast-a",
            "pre",
            "--contrast-b",
            "post",
        )
        self.assertTrue(preflight["comparison_ready"])
        self.assertFalse(preflight["cell_type_claims_supported"])
        self.assertEqual(preflight["population_semantics"], "generated_cluster_ids_unannotated")


if __name__ == "__main__":
    unittest.main()

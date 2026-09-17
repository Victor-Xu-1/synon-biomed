from __future__ import annotations

import json
import pathlib
import sys
import unittest
from unittest.mock import patch

import anyio


LIB = pathlib.Path(__file__).resolve().parents[1] / "lib"
sys.path.insert(0, str(LIB))

import mcp_chembl.server as chembl
from mcp_servers_common.tier1 import invoke_handler


class _Drugs:
    def __init__(self) -> None:
        self.calls: list[tuple] = []

    def paginate(self, path, key, params, max_records, start_offset=0):
        self.calls.append((path, key, params, max_records, start_offset))
        return [], 0

    def get_molecules(self, *_args, **_kwargs):
        raise AssertionError("a null-sentinel ChEMBL ID reached get_molecules")

    def substructure_search(self, *_args, **_kwargs):
        raise AssertionError("a null-sentinel SMILES reached substructure_search")

    def similarity_search(self, *_args, **_kwargs):
        raise AssertionError("a null-sentinel SMILES reached similarity_search")


class _Targets:
    def __init__(self) -> None:
        self.calls: list[tuple] = []

    def paginate(self, resource, params, max_records, start_offset=0):
        self.calls.append((resource, params, max_records, start_offset))
        return [], 0


class ChEMBLNullSentinelContractTests(unittest.TestCase):
    def test_compound_search_routes_to_name_without_sentinel_structure(self) -> None:
        drugs = _Drugs()
        with patch.object(chembl, "_drugs", return_value=drugs):
            payload = json.loads(chembl.compound_search({
                "chembl_id": None,
                "limit": 10,
                "max_phase": None,
                "name": "divarasib",
                "similarity_threshold": None,
                "smiles": "None",
            }))

        self.assertEqual(payload["count"], 0)
        self.assertEqual(len(drugs.calls), 2)
        for _, _, params, _, _ in drugs.calls:
            self.assertNotIn("None", params.values())

    def test_target_search_drops_sentinel_ids_but_keeps_real_filters(self) -> None:
        targets = _Targets()
        with patch.object(chembl, "_targets", return_value=targets):
            json.loads(chembl.target_search({
                "gene_symbol": "KRAS",
                "limit": 5,
                "organism": "Homo sapiens",
                "target_chembl_id": "None",
                "target_name": "None",
                "target_type": "SINGLE PROTEIN",
            }))

        self.assertEqual(len(targets.calls), 1)
        params = targets.calls[0][1]
        self.assertNotIn("target_chembl_id", params)
        self.assertNotIn("pref_name__icontains", params)
        self.assertEqual(params["organism__icontains"], "Homo sapiens")
        self.assertEqual(params["target_type"], "SINGLE PROTEIN")
        self.assertNotIn("None", params.values())

    def test_drug_search_resolves_name_before_indication_enrichment(self) -> None:
        class Drugs:
            def __init__(self) -> None:
                self.parent_filter = None

            def paginate(self, path, key, params, max_records):
                self.assert_query(path, key, params, max_records)
                return [{
                    "molecule_chembl_id": "CHEMBL190",
                    "molecule_hierarchy": {"parent_chembl_id": "CHEMBL190"},
                }], 1

            @staticmethod
            def assert_query(path, key, params, max_records):
                assert path == "/molecule.json"
                assert key == "molecules"
                assert params == {"pref_name__iexact": "theophylline"}
                assert max_records == 50

            def search_drugs_by_indication(self, *args, **kwargs):
                self.parent_filter = kwargs["parent_filter"]
                return {
                    "drugs": [],
                    "total_parents": 0,
                    "indication_query": {},
                    "total_indication_rows": 0,
                }

        drugs = Drugs()
        with patch.object(chembl, "_drugs", return_value=drugs):
            payload = json.loads(chembl.drug_search({
                "drug_name": "theophylline",
                "indication": "asthma",
                "limit": 15,
                "max_phase": None,
                "molecule_chembl_id": None,
                "only_approved": False,
            }))

        self.assertEqual(payload["count"], 0)
        self.assertEqual(drugs.parent_filter, {"CHEMBL190"})

    def test_recoverable_upstream_error_is_typed_result(self) -> None:
        class UpstreamError(RuntimeError):
            pass

        async def run() -> str:
            return await invoke_handler(
                lambda _args: (_ for _ in ()).throw(UpstreamError("HTTP 500")),
                {},
                (UpstreamError,),
            )

        payload = json.loads(anyio.run(run))
        self.assertFalse(payload["ok"])
        self.assertEqual(payload["code"], "upstream_unavailable")
        self.assertTrue(payload["retryable"])
        self.assertTrue(payload["sourceUnavailable"])

    def test_programming_error_is_not_hidden_as_source_unavailable(self) -> None:
        async def run() -> str:
            return await invoke_handler(
                lambda _args: (_ for _ in ()).throw(ValueError("bug")),
                {},
                (RuntimeError,),
            )

        with self.assertRaisesRegex(ValueError, "bug"):
            anyio.run(run)

    def test_bioactivity_maps_both_pchembl_bounds_without_sentinels(self) -> None:
        calls: list[dict] = []

        def single_page(_client, resource, params, limit, offset=0):
            calls.append({
                "resource": resource,
                "params": params,
                "limit": limit,
                "offset": offset,
            })
            return [], 0

        with (
            patch.object(chembl, "_bio", return_value=object()),
            patch.object(chembl, "_single_page", side_effect=single_page),
        ):
            json.loads(chembl.get_bioactivity({
                "activity_type": "IC50",
                "limit": 50,
                "max_pchembl": 8,
                "max_value": 1000,
                "min_pchembl": 7,
                "min_value": "None",
                "molecule_chembl_id": "CHEMBL4535757",
                "target_chembl_id": "None",
                "unit": "nM",
            }))

        params = calls[0]["params"]
        self.assertEqual(params["pchembl_value__gte"], 7)
        self.assertEqual(params["pchembl_value__lte"], 8)
        self.assertNotIn("target_chembl_id", params)
        self.assertNotIn("standard_value__gte", params)
        self.assertNotIn("None", params.values())


if __name__ == "__main__":
    unittest.main()

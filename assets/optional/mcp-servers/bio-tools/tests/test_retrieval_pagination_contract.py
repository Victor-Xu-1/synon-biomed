from __future__ import annotations

import json
import inspect
import pathlib
import sys
import unittest
from unittest.mock import AsyncMock, patch


LIB = pathlib.Path(__file__).resolve().parents[1] / "lib"
sys.path.insert(0, str(LIB))

import mcp_chembl.server as chembl
import mcp_biorxiv.server as biorxiv
import mcp_clinical_trials.server as clinical_trials
import mcp_pubmed.server as pubmed
from mcp_cellguide.client import CellGuideClient
import mcp_cellguide.server as cellguide
import mcp_chemistry.server as chemistry
import mcp_literature.server as literature
import mcp_pubmed.marshal as pubmed_marshal


class RetrievalPaginationContractTests(unittest.TestCase):
    def test_discovery_schemas_use_broad_defaults(self) -> None:
        cases = (
            ("mcp_biorxiv", ("search_by_funder", "search_preprints",
                              "search_published_preprints"), "limit"),
            ("mcp_clinical_trials", ("search_by_eligibility",
                                      "search_by_sponsor", "search_trials",
                                      "search_investigators"),
             "page_size"),
            ("mcp_pubmed", ("search_articles",), "max_results"),
            ("mcp_chembl", ("compound_search", "drug_search",
                            "get_bioactivity", "get_mechanism",
                            "target_search"), "limit"),
        )
        for package, tools, property_name in cases:
            schema_path = LIB / package / "schemas.json"
            schemas = json.loads(schema_path.read_text(encoding="utf-8"))
            by_name = {tool["name"]: tool for tool in schemas["tools"]}
            for tool_name in tools:
                value = by_name[tool_name]["input_schema"]["properties"][property_name]
                self.assertEqual(value["default"], 50)

    def test_discovery_runtime_defaults_match_broad_schemas(self) -> None:
        self.assertEqual(biorxiv._limit({}), 50)
        self.assertEqual(chembl.DEFAULT_LIMIT, 50)
        self.assertEqual(inspect.signature(cellguide.search_cell_types)
                         .parameters["limit"].default, 50)
        self.assertEqual(inspect.signature(chemistry.chebi_search)
                         .parameters["max_results"].default, 50)
        self.assertEqual(inspect.signature(literature.arxiv_search)
                         .parameters["max_results"].default, 50)

        with patch.object(clinical_trials, "_fetch_page",
                          return_value={"studies": []}) as fetch:
            clinical_trials.search_trials({"condition": "melanoma"})
        self.assertEqual(fetch.call_args.args[1], 50)

        class PubMedSearch:
            def search_page(self, _query, **kwargs):
                self.kwargs = kwargs
                return {"pmids": [], "count": 0}

        search = PubMedSearch()
        with patch.object(pubmed, "_search", return_value=search):
            pubmed.search_articles({"query": "kinase inhibitor"})
        self.assertEqual(search.kwargs["retmax"], 50)

    def test_cellguide_search_exposes_ranked_pagination(self) -> None:
        client = CellGuideClient()
        client.get_celltype_metadata = AsyncMock(return_value={
            "CL:1": {"id": "CL:1", "name": "T cell", "synonyms": []},
            "CL:2": {"id": "CL:2", "name": "memory T cell", "synonyms": []},
            "CL:3": {"id": "CL:3", "name": "regulatory T cell", "synonyms": []},
        })

        import asyncio
        page, total = asyncio.run(client.search_cell_types_page("T cell", limit=1, offset=1))

        self.assertEqual(total, 3)
        self.assertEqual(len(page), 1)
        self.assertEqual(page[0]["id"], "CL:2")

    def test_pubmed_response_exposes_next_retstart(self) -> None:
        page = pubmed_marshal.search_articles_page_response(
            {"pmids": ["11", "12"], "count": 7},
            "kinase inhibitor",
            retstart=2,
            max_results=2,
        )

        self.assertTrue(page["has_more"])
        self.assertEqual(page["next_retstart"], 4)

    def test_chembl_compound_search_forwards_offset(self) -> None:
        class Drugs:
            def paginate(self, path, key, params, max_records, start_offset=0):
                self.call = (path, key, params, max_records, start_offset)
                return [{"molecule_chembl_id": "CHEMBL42"}], 99

        drugs = Drugs()
        with patch.object(chembl, "_drugs", return_value=drugs):
            page = json.loads(chembl.compound_search({
                "name": "kinase",
                "limit": 20,
                "offset": 40,
            }))

        self.assertEqual(drugs.call[-1], 40)
        self.assertEqual(page["offset"], 40)
        self.assertEqual(page["next_offset"], 41)
        self.assertTrue(page["has_more"])

    def test_chembl_target_search_forwards_offset(self) -> None:
        class Targets:
            def paginate(self, resource, params, max_records, start_offset=0):
                self.call = (resource, params, max_records, start_offset)
                return [{"target_chembl_id": "CHEMBL_TARGET"}], 12

        targets = Targets()
        with patch.object(chembl, "_targets", return_value=targets):
            page = json.loads(chembl.target_search({
                "target_name": "kinase",
                "limit": 5,
                "offset": 5,
            }))

        self.assertEqual(targets.call[-1], 5)
        self.assertEqual(page["offset"], 5)
        self.assertEqual(page["next_offset"], 6)

    def test_chembl_bioactivity_search_forwards_offset(self) -> None:
        class Bioactivity:
            def _get(self, resource, params):
                self.call = (resource, params)
                return {
                    "activities": [{"activity_id": 7}],
                    "page_meta": {"total_count": 14},
                }

        bioactivity = Bioactivity()
        with patch.object(chembl, "_bio", return_value=bioactivity):
            page = json.loads(chembl.get_bioactivity({
                "target_chembl_id": "CHEMBL_TARGET",
                "limit": 5,
                "offset": 10,
            }))

        self.assertEqual(bioactivity.call[1]["offset"], 10)
        self.assertEqual(page["next_offset"], 11)
        self.assertTrue(page["has_more"])

    def test_chembl_drug_search_advances_by_scanned_candidates(self) -> None:
        class Drugs:
            def search_drugs_by_indication(self, *_args, **kwargs):
                self.start_offset = kwargs["start_offset"]
                return {
                    "drugs": [{
                        "parent_molecule_chembl_id": "CHEMBL42",
                        "max_phase": 2,
                    }],
                    "total_parents": 20,
                    "candidate_count": 5,
                    "indication_query": {},
                    "total_indication_rows": 30,
                }

            def get_molecules(self, *_args, **_kwargs):
                return [{"molecule_chembl_id": "CHEMBL42"}]

        drugs = Drugs()
        with patch.object(chembl, "_drugs", return_value=drugs):
            page = json.loads(chembl.drug_search({
                "indication": "melanoma",
                "limit": 5,
                "offset": 10,
            }))

        self.assertEqual(drugs.start_offset, 10)
        self.assertEqual(page["candidates_scanned"], 5)
        self.assertEqual(page["next_offset"], 15)
        self.assertTrue(page["has_more"])

    def test_clinical_investigator_search_forwards_page_token(self) -> None:
        response = {
            "studies": [],
            "nextPageToken": "continuation-token",
        }
        with patch.object(clinical_trials, "_fetch_page",
                          return_value=response) as fetch:
            page = json.loads(clinical_trials.search_investigators({
                "condition": "melanoma",
                "page_size": 50,
                "page_token": "current-token",
            }))

        self.assertEqual(fetch.call_args.args[2], "current-token")
        self.assertTrue(page["has_more"])
        self.assertEqual(page["next_page_token"], "continuation-token")


if __name__ == "__main__":
    unittest.main()

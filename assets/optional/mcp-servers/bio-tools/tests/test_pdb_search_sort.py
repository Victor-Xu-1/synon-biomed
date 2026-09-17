from __future__ import annotations

import unittest

from pdb_structures.search import search_structures


class _SearchClient:
    def __init__(self) -> None:
        self.payloads: list[dict] = []

    def post_search(self, payload: dict) -> dict:
        self.payloads.append(payload)
        return {
            "total_count": 2,
            "result_set": [
                {"identifier": "9V09", "score": 1.0},
                {"identifier": "9FJX", "score": 0.9},
            ],
        }


class PDBSearchSortTests(unittest.TestCase):
    def test_release_date_sort_is_sent_with_resolution_tiebreaker(self) -> None:
        client = _SearchClient()

        result = search_structures(
            client,
            uniprot_accession="Q96SW2",
            max_rows=100,
            sort_by="initial_release_date",
            sort_direction="desc",
        )

        self.assertEqual(
            client.payloads[0]["request_options"]["sort"],
            [
                {
                    "sort_by": "rcsb_accession_info.initial_release_date",
                    "direction": "desc",
                },
                {
                    "sort_by": "rcsb_entry_info.resolution_combined",
                    "direction": "asc",
                },
            ],
        )
        self.assertEqual(result["sort_by"], "initial_release_date")
        self.assertEqual(result["sort_direction"], "desc")
        self.assertEqual([row["pdb_id"] for row in result["records"]], ["9V09", "9FJX"])

    def test_invalid_sort_contract_fails_before_network(self) -> None:
        client = _SearchClient()
        with self.assertRaisesRegex(ValueError, "sort_by"):
            search_structures(client, text="CRBN", sort_by="release_guess")
        with self.assertRaisesRegex(ValueError, "sort_direction"):
            search_structures(client, text="CRBN", sort_direction="newest")
        self.assertEqual(client.payloads, [])


if __name__ == "__main__":
    unittest.main()

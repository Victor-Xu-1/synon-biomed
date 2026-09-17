from __future__ import annotations

import unittest
from unittest.mock import patch

from mcp_genes_ontologies.server import get_uniprot_entries


class _UniProt:
    def fetch_fields(self, accessions, fields, fmt="tsv") -> str:
        assert accessions == ["Q96SW2"]
        assert fields == ["accession", "protein_name", "organism_name"]
        assert fmt == "tsv"
        return "Entry\tProtein names\tOrganism\nQ96SW2\tProtein cereblon\tHomo sapiens\n"


class UniProtFieldRecordTests(unittest.TestCase):
    def test_records_use_requested_field_names_not_display_headers(self) -> None:
        with patch("mcp_genes_ontologies.server._uniprot", return_value=_UniProt()):
            result = get_uniprot_entries(
                ["Q96SW2"],
                fields=["accession", "protein_name", "organism_name"],
            )

        self.assertEqual(
            result["records"],
            [
                {
                    "accession": "Q96SW2",
                    "protein_name": "Protein cereblon",
                    "organism_name": "Homo sapiens",
                }
            ],
        )
        self.assertEqual(result["response_headers"], ["Entry", "Protein names", "Organism"])


if __name__ == "__main__":
    unittest.main()

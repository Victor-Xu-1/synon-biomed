from __future__ import annotations

import unittest

from uniprot_fetch.client import UniProtClient


class _Response:
    links: dict[str, dict[str, str]] = {}


class _Session:
    def __init__(self) -> None:
        self.calls: list[dict[str, object]] = []

    def get_text(self, url: str, params: dict | None = None):
        self.calls.append({"url": url, "params": dict(params or {})})
        return "Entry\tFunction [CC]\nP24941\tCyclin-dependent kinase.\n", _Response()


class UniProtFieldAliasTests(unittest.TestCase):
    def test_common_annotation_names_are_mapped_to_official_rest_fields(self) -> None:
        session = _Session()
        client = UniProtClient(session=session)

        client.fetch_fields(
            ["P24941"],
            [
                "accession",
                "function",
                "subcellular_location",
                "domain",
            ],
        )

        self.assertEqual(
            session.calls[0]["params"]["fields"],
            "accession,cc_function,cc_subcellular_location,ft_domain",
        )

    def test_alias_and_official_field_are_not_requested_twice(self) -> None:
        session = _Session()
        client = UniProtClient(session=session)

        client.fetch_fields(["P24941"], ["function", "cc_function"])

        self.assertEqual(session.calls[0]["params"]["fields"], "cc_function")


if __name__ == "__main__":
    unittest.main()

from __future__ import annotations

import json
import unittest
from unittest.mock import MagicMock, patch

from pubmed_search.client import IDCONV_URL, PubMedSearch


class PubMedSearchIDConverterTests(unittest.TestCase):
    def test_id_converter_uses_required_query_parameters_without_api_key(self) -> None:
        session = MagicMock()
        session.headers = {}
        payload = {
            "status": "ok",
            "records": [
                {
                    "requested-id": "10.1000/example",
                    "pmid": "123",
                    "pmcid": "PMC456",
                    "doi": "10.1000/example",
                }
            ],
        }
        response = MagicMock()
        response.status_code = 200
        response.text = json.dumps(payload)
        response.content = response.text.encode("utf-8")
        response.json.return_value = payload
        session.get.return_value = response

        client = PubMedSearch(
            email="runtime@example.invalid",
            api_key="test-secret-must-not-enter-idconv-url",
            sleep_s=0,
            max_retries=0,
            session=session,
        )
        try:
            with patch("pubmed_search.client.pace"):
                records = client.convert_ids(["10.1000/example"], from_type="doi")
        finally:
            client.close()

        self.assertEqual(records[0]["pmcid"], "PMC456")
        session.post.assert_not_called()
        session.get.assert_called_once()
        url = session.get.call_args.args[0]
        params = session.get.call_args.kwargs["params"]
        self.assertEqual(url, IDCONV_URL)
        self.assertEqual(params["ids"], "10.1000/example")
        self.assertEqual(params["idtype"], "doi")
        self.assertNotIn("api_key", params)


if __name__ == "__main__":
    unittest.main()

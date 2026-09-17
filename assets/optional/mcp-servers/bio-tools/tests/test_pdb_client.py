from __future__ import annotations

import httpx
import unittest
from unittest.mock import patch

from pdb_structures.client import PDBClient


class PDBClientRetryTests(unittest.TestCase):
    def test_default_retry_budget_fits_mcp_transport_window(self) -> None:
        client = PDBClient(transport=httpx.MockTransport(lambda _request: httpx.Response(204)))
        try:
            timeout = client._client.timeout.read
            backoff = sum(min(2.0 * (2**attempt), 8.0) for attempt in range(client.max_retries))
            self.assertLess(timeout * (client.max_retries + 1) + backoff, 60.0)
        finally:
            client.close()

    def test_read_timeout_retries_and_returns_next_success(self) -> None:
        attempts = 0

        def handler(request: httpx.Request) -> httpx.Response:
            nonlocal attempts
            attempts += 1
            if attempts == 1:
                raise httpx.ReadTimeout("transient read timeout", request=request)
            return httpx.Response(200, json={"total_count": 0, "result_set": []})

        client = PDBClient(transport=httpx.MockTransport(handler), min_interval_s=0)
        try:
            with patch("pdb_structures.client.time.sleep"):
                result = client.post_search({"query": {}})
        finally:
            client.close()

        self.assertEqual(result, {"total_count": 0, "result_set": []})
        self.assertEqual(attempts, 2)


if __name__ == "__main__":
    unittest.main()

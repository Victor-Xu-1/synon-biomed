from __future__ import annotations

import unittest
from unittest.mock import patch

from bindingdb_affinities.client import BindingDbApiError, BindingDbClient


class _Response:
    def __init__(self, text: str, status_code: int = 200):
        self.text = text
        self.status_code = status_code
        self.content = text.encode("utf-8")
        self.headers: dict[str, str] = {}


class _Session:
    def __init__(self, responses: list[_Response]):
        self._responses = list(responses)
        self.calls = 0
        self.headers: dict[str, str] = {}

    def get(self, *_args, **_kwargs) -> _Response:
        self.calls += 1
        return self._responses.pop(0)


class BindingDbClientRetryTests(unittest.TestCase):
    def test_transient_empty_success_response_uses_bounded_retry(self) -> None:
        session = _Session(
            [
                _Response(""),
                _Response('{"getTargetByCompoundResponse":{"bdb.affinities":[]}}'),
            ]
        )
        client = BindingDbClient(
            session=session, min_interval_s=0, max_retries=1
        )

        with patch("bindingdb_affinities.client.time.sleep"):
            result = client.get_json_root(
                "getTargetByCompound", {"smiles": "CCO", "cutoff": 0.85}
            )

        self.assertEqual(result, {"bdb.affinities": []})
        self.assertEqual(session.calls, 2)

    def test_persistent_empty_success_response_remains_an_error(self) -> None:
        session = _Session([_Response(""), _Response("")])
        client = BindingDbClient(
            session=session, min_interval_s=0, max_retries=1
        )

        with patch("bindingdb_affinities.client.time.sleep"):
            with self.assertRaisesRegex(BindingDbApiError, "empty response body"):
                client.get_json_root(
                    "getTargetByCompound", {"smiles": "CCO", "cutoff": 0.85}
                )

        self.assertEqual(session.calls, 2)


if __name__ == "__main__":
    unittest.main()

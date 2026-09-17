import json

import anyio
import pytest
import requests

from biomart_query.client import BiomartClient, BiomartError
from biomart_query.introspect import _get_metadata
from gtex_expression.tool import DEFAULT_DATASET as DEFAULT_GTEX_DATASET
from mcp_servers_common.tier1 import invoke_handler
from mcp_servers_common.ua import require_contact_email


def test_biomart_default_retry_schedule_fits_mcp_handler_budget() -> None:
    client = BiomartClient()
    worst_case = (
        client.timeout * (client.max_retries + 1)
        + sum(client.backoff_base * (2**attempt)
              for attempt in range(client.max_retries))
    )
    assert worst_case < 100.0


def test_gtex_default_is_current_adult_release() -> None:
    assert DEFAULT_GTEX_DATASET == "gtex_v10"


def test_biomart_metadata_attempt_is_capped_for_audited_fallback() -> None:
    observed: list[float] = []

    class TimeoutSession:
        def get(self, _url, *, params, timeout, headers):
            del params, headers
            observed.append(timeout)
            raise requests.Timeout("fixture timeout")

    client = BiomartClient(
        timeout=60.0, max_retries=0, min_request_interval=0,
        session=TimeoutSession(),
    )
    with pytest.raises(BiomartError):
        _get_metadata(client, {"type": "registry"})
    assert observed == [12.0]


def test_tier1_contact_consent_failure_is_actionable_tool_result(monkeypatch) -> None:
    monkeypatch.delenv("OPERON_CONTACT_EMAIL", raising=False)
    monkeypatch.delenv("NCBI_EMAIL", raising=False)

    def handler(_arguments: dict) -> str:
        require_contact_email()
        raise AssertionError("unreachable")

    payload = anyio.run(invoke_handler, handler, {})
    result = json.loads(payload)
    assert result["code"] == "contact_email_required"
    assert result["retryable"] is False
    assert "Settings" in result["error"]

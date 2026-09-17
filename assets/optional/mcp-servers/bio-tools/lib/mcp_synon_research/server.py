"""Independent Synon-research MCP server."""

from __future__ import annotations

from typing import Any

from mcp.types import ToolAnnotations
from mcp_servers_common.gate import apply_gate_mcpserver
from mcp_servers_common.mcp_compat import MCPServer

from .bridge import PackError, call_source_async, describe_source, list_sources


READ_ONLY = ToolAnnotations(readOnlyHint=True)
mcp = MCPServer("Synon-research")


def _error(exc: PackError) -> dict[str, Any]:
    return {"ok": False, "error": {"code": exc.code, "message": str(exc)}}


@mcp.tool(annotations=READ_ONLY)
def life_science_sources(query: str = "", max_results: int = 20) -> dict[str, Any]:
    """List executable sources in the configured Synon-research pack.

    This is capability discovery, not scientific evidence. Use a focused query
    or pagination-friendly max_results, then inspect one exact source contract.
    """
    try:
        return {"ok": True, **list_sources(query=query, max_results=max_results)}
    except PackError as exc:
        return _error(exc)


@mcp.tool(annotations=READ_ONLY)
def life_science_source_contract(source_id: str) -> dict[str, Any]:
    """Describe one exact external source and its whitelisted JSON operations."""
    try:
        return {"ok": True, **describe_source(source_id)}
    except PackError as exc:
        return _error(exc)


@mcp.tool(annotations=READ_ONLY)
async def life_science_source(
    source_id: str,
    operation: str,
    input: dict[str, Any],
    timeout_seconds: int = 60,
) -> dict[str, Any]:
    """Execute one whitelisted JSON helper from the configured source pack.

    The operation runs without a shell, from a fixed operator-configured root,
    with bounded input, timeout, output and a content-hash provenance receipt.
    """
    try:
        response = await call_source_async(source_id, operation, input, timeout_seconds)
        return {"ok": bool(response.get("result", {}).get("ok", False)), **response}
    except PackError as exc:
        return _error(exc)


def main() -> None:
    apply_gate_mcpserver(mcp)
    mcp.run()


if __name__ == "__main__":
    main()

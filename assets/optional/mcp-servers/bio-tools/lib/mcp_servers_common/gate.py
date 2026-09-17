"""Fail-closed serving gate for the standalone per-domain servers.

The aggregate (mcp_bio) applies deferred.json itself; when a domain server
runs STANDALONE (run_server.py mcp_<domain>) it must enforce the exact same
gate, or splitting the aggregate into per-domain connectors would silently
re-enable legally-deferred tools (KEGG/CADD/PanglaoDB license gate, plus any
domain/tool deferrals). Single source of truth stays mcp_bio/domains.json +
mcp_bio/deferred.json — read here as data files (no module import, so a
single domain server never pays for importing the whole 26-server fleet).

Contract (same as mcp_bio.load_deferred): unknown names in the gate fail
CLOSED with a RuntimeError; a server whose every tool is gated refuses to
start rather than serving an empty tool list.
"""

from __future__ import annotations

import importlib.resources
import json
import threading

import anyio


def _load(resource: str) -> dict:
    with importlib.resources.files("mcp_bio").joinpath(resource).open(
        "r", encoding="utf-8"
    ) as f:
        return json.load(f)


def gated_tool_names() -> frozenset[str]:
    """Union of all tool names deferred.json removes from serving."""
    domains = _load("domains.json")
    gate = _load("deferred.json")
    all_tools = {t for roster in domains.values() for t in roster}
    bad_domains = set(gate.get("domains", [])) - set(domains)
    bad_tools = (set(gate.get("tools", []))
                 | set(gate.get("license_tools", []))) - all_tools
    if bad_domains or bad_tools:
        raise RuntimeError(
            "deferred.json names unknown to domains.json — failing closed: "
            f"domains={sorted(bad_domains)} tools={sorted(bad_tools)}")
    names = set(gate.get("tools", [])) | set(gate.get("license_tools", []))
    for d in gate.get("domains", []):
        names |= set(domains[d])
    return frozenset(names)


def apply_gate_mcpserver(mcp_server) -> None:
    """Remove gated tools from a tier-2 MCPServer before serving.

    Call from the server's main() (NOT at import time — the aggregate
    imports these modules and applies its own gate). Raises if the gate
    empties the server: a fully-deferred domain must refuse to start.

    MCP SDK v2 runs synchronous tool functions on worker threads itself, so
    the v1 private-handler rebinding workaround is deliberately gone. This
    gate uses only the public list/remove APIs and remains fail-closed.
    """
    async def _apply() -> None:
        gated = gated_tool_names()
        for tool in await mcp_server.list_tools():
            if tool.name in gated:
                mcp_server.remove_tool(tool.name)
        if not await mcp_server.list_tools():
            raise RuntimeError(
                f"{mcp_server.name}: every tool is deferred by "
                "mcp_bio/deferred.json — this domain is not cleared to "
                "serve standalone")

    anyio.run(_apply)


def apply_gate_tier1(t1) -> None:
    """Remove gated tools from a Tier1Server before serving.

    Serve-time only (call from the server's main()): build_server() must
    stay pristine — the drop-in parity tests and the aggregate consume the
    full schema set. Raises if the gate empties the server.

    Also serializes same-process dispatch (finding 3406443687):
    Tier1Server._call_tool offloads via anyio.to_thread with NO lock, while
    the domain's handlers share one @lru_cache client wrapping a
    requests.Session (not thread-safe, non-atomic stats writes — reviews
    3386234819/3386420557). Each handler is wrapped with a process-wide
    threading.Lock, held inside the worker thread: same one-in-flight
    serialization the aggregate enforces per domain, while the event loop
    stays free.
    """
    gated = gated_tool_names()
    t1.schemas = [s for s in t1.schemas if s["name"] not in gated]
    t1.handlers = {k: v for k, v in t1.handlers.items() if k not in gated}
    if not t1.schemas:
        raise RuntimeError(
            f"{t1.name}: every tool is deferred by mcp_bio/deferred.json — "
            "this domain is not cleared to serve standalone")
    if getattr(t1, "_operon_serialized_dispatch", False):
        return  # idempotent — don't double-wrap
    lock = threading.Lock()

    def _serialized(handler):
        def run(args):
            with lock:
                return handler(args)
        return run

    t1.handlers = {k: _serialized(v) for k, v in t1.handlers.items()}
    t1._operon_serialized_dispatch = True

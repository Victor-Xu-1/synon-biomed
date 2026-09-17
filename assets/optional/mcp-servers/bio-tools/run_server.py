#!/usr/bin/env python3
"""Launcher for the vendored bio-tools MCP servers (stdio).

Usage: python run_server.py <package>   e.g. python run_server.py mcp_pubmed

Registered in the Synon bundled connector directory as
  command: "python", args: ["${MCP_SERVERS_DIR}/bio-tools/run_server.py", "<pkg>"]
The connector pool resolves `python` to the shared Synon MCP environment and substitutes
the staged-assets path; deps come from the registry entry's installPip pins.

All packages (servers + the fleet retrieval packages they import) live flat
under lib/ next to this file — no pip install of the vendored code itself.
"""

import importlib
import sys
from pathlib import Path


def main() -> None:
    lib = Path(__file__).resolve().parent / "lib"
    # Launchable = lib packages that actually ship a server module. Derived
    # from disk (not a third copy of the roster) so it stays in lockstep with
    # vendor-sync.py / bundledRegistry.ts; excludes helper packages like
    # mcp_servers_common, which a bare mcp_ prefix check would accept and
    # then fail cryptically on import (review 3377922629).
    servers = sorted(
        p.name for p in lib.iterdir()
        if p.name.startswith("mcp_") and (p / "server.py").is_file()
    )
    if len(sys.argv) != 2 or sys.argv[1] not in servers:
        sys.stderr.write(
            "usage: run_server.py <server package>\n"
            f"valid: {', '.join(servers)}\n")
        raise SystemExit(2)
    sys.path.insert(0, str(lib))
    try:
        from mcp_servers_common import tls_policy
    except ImportError as exc:
        missing = getattr(exc, "name", None) or str(exc)
        sys.stderr.write(
            f"[tls-policy] x509-strict=unavailable ({missing})\n"
        )
    else:
        if tls_policy.apply_posture() == tls_policy.NOT_ARMED:
            sys.stderr.write(
                "[tls-policy] x509-strict=not-armed "
                "(relaxation was requested but this interpreter does not arm it)\n"
            )
    mod = importlib.import_module(f"{sys.argv[1]}.server")
    mod.main()


if __name__ == "__main__":
    main()

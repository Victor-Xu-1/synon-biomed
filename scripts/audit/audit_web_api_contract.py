#!/usr/bin/env python3
"""Audit active Web API paths against non-test Go ServeMux registrations."""

from __future__ import annotations

import argparse
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import sys
from typing import Any


ROOT = Path(__file__).resolve().parents[2]
REPORT_RELATIVE = Path("docs/governance/web-api-contract.json")

DORMANT_PATHS = {
    "/api/cron/jobs",
    "/api/cron/jobs/{param}",
}

NON_ENDPOINT_PATHS = {
    "/api/",
    "/api/auth/",
    "/api/compute/session",
}

# Template paths discovered from bounded frontend unions must prove every
# supported concrete route. A wildcard match against only one adapter would
# hide a broken channel.
BOUNDED_PATH_EXPANSIONS = {
    "/api/adapters/{param}/qr/start": (
        "/api/adapters/feishu/qr/start",
        "/api/adapters/wechat/qr/start",
    ),
    "/api/adapters/{param}/qr/poll": (
        "/api/adapters/feishu/qr/poll",
        "/api/adapters/wechat/qr/poll",
    ),
}

# Reachable callers hidden behind the ipcBridge barrel are explicit evidence.
CONFIRMED_ACTIVE_PATHS = {
    "/api/auth/register",
    "/api/document/convert",
    "/api/excel-preview/start",
    "/api/excel-preview/stop",
    "/api/fs/copy",
    "/api/fs/dir",
    "/api/fs/fetch-remote-image",
    "/api/fs/image-base64",
    "/api/fs/list",
    "/api/fs/metadata",
    "/api/fs/read",
    "/api/fs/read-buffer",
    "/api/fs/remove",
    "/api/fs/rename",
    "/api/fs/snapshot/baseline",
    "/api/fs/snapshot/branches",
    "/api/fs/snapshot/compare",
    "/api/fs/snapshot/discard",
    "/api/fs/snapshot/dispose",
    "/api/fs/snapshot/info",
    "/api/fs/snapshot/init",
    "/api/fs/snapshot/reset",
    "/api/fs/snapshot/stage",
    "/api/fs/snapshot/stage-all",
    "/api/fs/snapshot/unstage",
    "/api/fs/snapshot/unstage-all",
    "/api/fs/temp",
    "/api/fs/write",
    "/api/fs/zip",
    "/api/fs/zip/cancel",
    "/api/office-watch-proxy",
    "/api/ppt-preview/start",
    "/api/ppt-preview/stop",
    "/api/ppt-proxy",
    "/api/preview-history/get-content",
    "/api/preview-history/list",
    "/api/preview-history/save",
    "/api/shell/check-tool-installed",
    "/api/shell/open-external",
    "/api/shell/open-file",
    "/api/shell/open-folder-with",
    "/api/shell/show-item-in-folder",
    "/api/synonbiomed/catalog",
    "/api/system/provenance-census",
    "/api/word-preview/start",
    "/api/word-preview/stop",
}


def json_bytes(value: Any) -> bytes:
    return (json.dumps(value, ensure_ascii=False, indent=2) + "\n").encode("utf-8")


def normalize_path(value: str) -> str:
    path = value.strip().split("?", 1)[0]
    path = re.sub(r"(?<=[A-Za-z0-9_-])\{param\}$", "", path)
    return re.sub(r"/+", "/", path)


def route_covers(route: str, request_path: str) -> bool:
    request = normalize_path(request_path)
    if route == "/":
        return request == "/"
    if route.endswith("/"):
        return request.startswith(route)
    return request == route


def concrete_request_paths(request_path: str) -> tuple[str, ...]:
    return BOUNDED_PATH_EXPANSIONS.get(request_path, (request_path,))


def load_inventory_module(root: Path) -> Any:
    script = root / "scripts/audit/frontend_source_graph.py"
    spec = importlib.util.spec_from_file_location("frontend_source_graph", script)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load inventory generator: {script}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


def active_path_sources(root: Path) -> dict[str, set[str]]:
    inventory_path = root / "docs/governance/web-api-required-paths.json"
    reference = json.loads(inventory_path.read_text(encoding="utf-8"))
    sources: dict[str, set[str]] = {}
    for path in reference["apiPaths"]:
        sources.setdefault(path, set()).add("declared-web-api-contract")

    target_graph = load_inventory_module(root).active_frontend_graph(root / "frontend")
    for path in target_graph["apiPaths"]:
        sources.setdefault(path, set()).add("target-frontend-active-graph")
    for path in CONFIRMED_ACTIVE_PATHS:
        sources.setdefault(path, set()).add("confirmed-ipcBridge-production-caller")
    return sources


def go_route_inventory(root: Path, go_command: str) -> dict[str, Any]:
    route_helper = root / "scripts/web_api_route_inventory.go"
    result = subprocess.run(
        [go_command, "run", str(route_helper), "-root", str(root)],
        cwd=root,
        check=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        timeout=120,
    )
    return json.loads(result.stdout)


def build_report(root: Path = ROOT, go_command: str | None = None) -> dict[str, Any]:
    go_command = go_command or os.environ.get("GO", "go")
    route_inventory = go_route_inventory(root, go_command)
    route_records = route_inventory["routes"]
    routes = [record["path"] for record in route_records]
    route_locations = {record["path"]: record["locations"] for record in route_records}
    sources = active_path_sources(root)

    dormant = sorted(DORMANT_PATHS)
    non_endpoints = sorted(path for path in sources if path in NON_ENDPOINT_PATHS)
    required = sorted(
        path
        for path in sources
        if normalize_path(path) not in DORMANT_PATHS
        and path not in NON_ENDPOINT_PATHS
    )
    records: list[dict[str, Any]] = []
    missing: list[str] = []
    for path in required:
        expected_paths = concrete_request_paths(path)
        coverage = {
            expected: [route for route in routes if route_covers(route, expected)]
            for expected in expected_paths
        }
        missing_expected = [expected for expected, matches in coverage.items() if not matches]
        covering = sorted({route for matches in coverage.values() for route in matches})
        if missing_expected:
            missing.append(path)
        record = {
            "path": path,
            "sources": sorted(sources[path]),
            "covered": not missing_expected,
            "coveredBy": covering,
            "routeLocations": {
                route: route_locations[route] for route in covering
            },
        }
        if path in BOUNDED_PATH_EXPANSIONS:
            record["expectedPaths"] = list(expected_paths)
            record["missingExpectedPaths"] = missing_expected
        records.append(record)

    return {
        "schemaVersion": 1,
        "generatedBy": "scripts/audit/audit_web_api_contract.py",
        "scope": {
            "frontend": "declared API contracts and current production imports plus confirmed ipcBridge callers",
            "backend": "literal non-test Go ServeMux Handle/HandleFunc registrations",
            "matching": "query-stripped exact routes, ServeMux subtree routes, and explicit expansion of bounded frontend path unions",
        },
        "counts": {
            "requiredActivePaths": len(required),
            "coveredActivePaths": len(required) - len(missing),
            "missingActivePaths": len(missing),
            "goRoutePatterns": len(routes),
            "dormantPaths": len(dormant),
            "nonEndpointPaths": len(non_endpoints),
        },
        "missingActivePaths": missing,
        "dormantPaths": dormant,
        "nonEndpointPaths": non_endpoints,
        "records": records,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument("--write", action="store_true")
    mode.add_argument("--check", action="store_true")
    parser.add_argument("--root", type=Path, default=ROOT)
    args = parser.parse_args()

    root = args.root.resolve()
    report = build_report(root)
    encoded = json_bytes(report)
    report_path = root / REPORT_RELATIVE
    if args.write:
        report_path.parent.mkdir(parents=True, exist_ok=True)
        report_path.write_bytes(encoded)
    else:
        if not report_path.is_file() or report_path.read_bytes() != encoded:
            raise RuntimeError(f"stale: {report_path.relative_to(root)}")
        if report["missingActivePaths"]:
            raise RuntimeError(
                "active Web API paths without Go routes: "
                + ", ".join(report["missingActivePaths"])
            )
    print(json.dumps(report["counts"], sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

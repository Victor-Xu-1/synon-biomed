#!/usr/bin/env python3
"""Focused tests for the active frontend-to-Go route contract gate."""

from __future__ import annotations

import importlib.util
import os
from pathlib import Path
import shutil
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("audit_web_api_contract.py")
SPEC = importlib.util.spec_from_file_location("audit_web_api_contract", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
audit = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = audit
SPEC.loader.exec_module(audit)


class WebAPIContractTest(unittest.TestCase):
    def test_source_graph_follows_live_imports_without_historical_inventory(self) -> None:
        graph = audit.load_inventory_module(audit.ROOT)
        with tempfile.TemporaryDirectory() as temporary:
            frontend = Path(temporary)
            renderer = frontend / "packages/desktop/src/renderer"
            common = frontend / "packages/desktop/src/common"
            renderer.mkdir(parents=True)
            common.mkdir()
            (renderer / "main.tsx").write_text("import '@/common/api'; import('./page');")
            (common / "api.ts").write_text("fetch('/api/projects');")
            (renderer / "page.tsx").write_text("fetch(`/api/projects/${id}/files`);")
            (renderer / "unused.tsx").write_text("fetch('/api/unused');")
            result = graph.active_frontend_graph(frontend)
            self.assertEqual(result["apiPaths"], ["/api/projects", "/api/projects/{param}/files"])
            self.assertEqual(result["activeFiles"], 3)
            self.assertEqual(result["unresolvedLocalImports"], [])

    def test_source_graph_reports_unresolved_local_imports(self) -> None:
        graph = audit.load_inventory_module(audit.ROOT)
        with tempfile.TemporaryDirectory() as temporary:
            frontend = Path(temporary)
            renderer = frontend / "packages/desktop/src/renderer"
            renderer.mkdir(parents=True)
            (renderer / "main.tsx").write_text("import './missing';")
            result = graph.active_frontend_graph(frontend)
            self.assertEqual(result["unresolvedLocalImports"][0]["specifier"], "./missing")

    def test_source_graph_requires_a_real_entrypoint(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            with self.assertRaisesRegex(FileNotFoundError, "frontend entry is missing"):
                audit.load_inventory_module(audit.ROOT).active_frontend_graph(Path(temporary))

    def test_normalizes_queries_and_template_query_suffixes(self) -> None:
        self.assertEqual(audit.normalize_path("/api/projects?shallow=true"), "/api/projects")
        self.assertEqual(audit.normalize_path("/api/memory/context{param}"), "/api/memory/context")
        self.assertEqual(audit.normalize_path("/api/projects/{param}"), "/api/projects/{param}")

    def test_matches_exact_and_servemux_subtree_routes(self) -> None:
        self.assertTrue(audit.route_covers("/api/projects", "/api/projects?shallow=true"))
        self.assertTrue(audit.route_covers("/api/projects/", "/api/projects/{param}/artifacts"))
        self.assertFalse(audit.route_covers("/api/projects", "/api/projects/{param}"))
        self.assertFalse(audit.route_covers("/api/project/", "/api/projects/{param}"))
        self.assertFalse(audit.route_covers("/", "/api/unimplemented"))

    def test_expands_every_supported_message_channel_route(self) -> None:
        self.assertEqual(
            audit.concrete_request_paths("/api/adapters/{param}/qr/start"),
            ("/api/adapters/feishu/qr/start", "/api/adapters/wechat/qr/start"),
        )
        self.assertEqual(
            audit.concrete_request_paths("/api/projects"),
            ("/api/projects",),
        )

    def test_classifies_confirmed_and_dormant_bridge_paths(self) -> None:
        self.assertIn("/api/fs/read", audit.CONFIRMED_ACTIVE_PATHS)
        self.assertIn("/api/shell/open-file", audit.CONFIRMED_ACTIVE_PATHS)
        self.assertIn("/api/auth/register", audit.CONFIRMED_ACTIVE_PATHS)
        self.assertIn("/api/system/provenance-census", audit.CONFIRMED_ACTIVE_PATHS)
        self.assertIn("/api/cron/jobs", audit.DORMANT_PATHS)
        self.assertNotIn("/api/cron/jobs", audit.CONFIRMED_ACTIVE_PATHS)

    def test_real_go_inventory_excludes_tests_and_finds_compatibility_routes(self) -> None:
        go_command = os.environ.get("GO") or shutil.which("go")
        if not go_command:
            self.skipTest("Go toolchain is unavailable")
        inventory = audit.go_route_inventory(audit.ROOT, go_command)
        routes = {record["path"] for record in inventory["routes"]}
        self.assertIn("/api/memory/context", routes)
        self.assertIn("/api/approvals/grants", routes)
        self.assertIn("/api/kernels", routes)
        self.assertNotIn("/test-only", routes)

    def test_real_report_tracks_missing_paths_without_hiding_them(self) -> None:
        go_command = os.environ.get("GO") or shutil.which("go")
        if not go_command:
            self.skipTest("Go toolchain is unavailable")
        report = audit.build_report(audit.ROOT, go_command)
        self.assertGreater(report["counts"]["requiredActivePaths"], 100)
        self.assertEqual(
            report["counts"]["requiredActivePaths"],
            report["counts"]["coveredActivePaths"] + report["counts"]["missingActivePaths"],
        )
        self.assertNotIn("/api/memory/context", report["missingActivePaths"])
        self.assertNotIn("/api/approvals/grants", report["missingActivePaths"])
        self.assertNotIn("/api/kernels", report["missingActivePaths"])
        self.assertNotIn("/api/system/provenance-census", report["missingActivePaths"])
        self.assertIn("/api/cron/jobs", report["dormantPaths"])


if __name__ == "__main__":
    unittest.main()

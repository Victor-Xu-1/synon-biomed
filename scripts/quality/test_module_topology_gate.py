from __future__ import annotations

import json
from pathlib import Path
import subprocess
import tempfile
import unittest

if __package__:
    from . import module_topology_gate as gate
else:
    import module_topology_gate as gate


class ModuleTopologyGateTests(unittest.TestCase):
    def test_resume_dispatch_growth_ceiling_is_explicit_and_bounded(self) -> None:
        repo = Path(__file__).resolve().parents[2]
        spec = json.loads((repo / "docs/governance/module-topology.json").read_text(encoding="utf-8"))
        rule = next(
            (
                entry
                for entry in spec["growthCeilings"]
                if entry.get("path") == "internal/server/frame_resume_dispatch_once.go"
            ),
            None,
        )
        self.assertIsNotNone(rule)
        self.assertEqual(rule["kind"], "lines")
        self.assertEqual(rule["maximum"], 620)
        line_count = len(
            (repo / "internal/server/frame_resume_dispatch_once.go").read_text(encoding="utf-8").splitlines()
        )
        self.assertLessEqual(line_count, rule["maximum"])

    def test_valid_topology_and_drift_detection(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            repo = Path(directory)
            (repo / "skills/synonbiomed/example").mkdir(parents=True)
            (repo / "assets/agents").mkdir(parents=True)
            (repo / "skills/synonbiomed/example/SKILL.md").write_text("# Skill\n", encoding="utf-8")
            (repo / "assets/agents/metadata.yaml").write_text("agent_name: TEST\n", encoding="utf-8")
            (repo / "small.go").write_text("package small\n", encoding="utf-8")
            subprocess.run(["git", "init", "-q", str(repo)], check=True)
            subprocess.run(["git", "-C", str(repo), "add", "."], check=True)
            spec = {
                "schema": gate.SCHEMA,
                "authorityOwner": "repository-steward",
                "requiredDirectories": ["skills/synonbiomed", "assets/agents"],
                "requiredFiles": ["small.go"],
                "bannedPaths": ["tools"],
                "allowedTrackedRootEntries": ["assets", "skills", "small.go"],
                "fileAuthorities": [
                    {"suffix": "/SKILL.md", "prefix": "skills/synonbiomed/"},
                    {"suffix": "/metadata.yaml", "prefix": "assets/agents/"},
                ],
                "growthCeilings": [{"kind": "lines", "path": "small.go", "maximum": 1}],
            }
            self.assertEqual(gate.validate(repo, spec), [])

            (repo / "tools").mkdir()
            (repo / "tools/helper.py").write_text("pass\n", encoding="utf-8")
            (repo / "rogue/SKILL.md").parent.mkdir()
            (repo / "rogue/SKILL.md").write_text("# Rogue\n", encoding="utf-8")
            subprocess.run(["git", "-C", str(repo), "add", "."], check=True)
            failures = gate.validate(repo, spec)
            self.assertIn("banned_path_present:tools", failures)
            self.assertIn("file_authority_drift:rogue/SKILL.md", failures)
            self.assertIn("unexpected_root_entry:rogue", failures)
            self.assertIn("unexpected_root_entry:tools", failures)

    def test_package_dependency_rule_rejects_forbidden_runtime_import(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            repo = Path(directory)
            package = repo / "internal/toolgateway"
            package.mkdir(parents=True)
            source = package / "pipeline.go"
            source.write_text(
                'package toolgateway\n\nimport (\n    "context"\n)\n',
                encoding="utf-8",
            )
            rule = {
                "path": "internal/toolgateway",
                "forbiddenImports": ["net/http", "synon-go/internal/server"],
            }
            self.assertEqual(gate.validate_package_dependency_rule(repo, rule), [])

            source.write_text(
                'package toolgateway\n\nimport (\n    "net/http"\n    server "synon-go/internal/server"\n)\n',
                encoding="utf-8",
            )
            failures = gate.validate_package_dependency_rule(repo, rule)
            self.assertIn("package_dependency_forbidden:internal/toolgateway/pipeline.go:net/http", failures)
            self.assertIn(
                "package_dependency_forbidden:internal/toolgateway/pipeline.go:synon-go/internal/server",
                failures,
            )


class ArchitectureDocumentTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.repo = Path(self.temporary.name)
        self.path = self.repo / "docs/governance/architecture.json"
        self.path.parent.mkdir(parents=True)
        self.rule = {"path": "docs/governance/architecture.json", "objectKey": "architecture",
                     "requiredModuleNames": ["gateway", "runner"], "requiredOrderedModules": ["gateway"]}
        self.document = {"schemaVersion": 1, "architecture": {"modules": [
            {"name": "gateway", "target": "internal/gateway", "owns": ["tool execution"],
             "order": ["admit", "execute", "audit"]},
            {"name": "runner", "target": "internal/runner", "owns": ["task lifecycle"]},
        ]}}

    def check_document(self):
        self.path.write_text(json.dumps(self.document))
        return gate.validate_architecture_authority(self.repo, self.rule)

    def test_product_contract_needs_no_progress_weights_or_test_schedule(self):
        self.assertEqual(self.check_document(), [])

    def test_rejects_missing_required_module(self):
        self.document["architecture"]["modules"].pop()
        self.assertIn("architecture_required_module_missing:runner", self.check_document())

    def test_rejects_duplicate_module_identity(self):
        self.document["architecture"]["modules"][1]["name"] = "gateway"
        self.assertIn("architecture_module_identity_invalid:docs/governance/architecture.json", self.check_document())

    def test_rejects_empty_ownership(self):
        self.document["architecture"]["modules"][0]["owns"] = []
        self.assertIn("architecture_module_ownership_invalid:gateway", self.check_document())

    def test_rejects_missing_or_duplicate_stage_order(self):
        for order in ([], ["admit", "admit"]):
            with self.subTest(order=order):
                self.document["architecture"]["modules"][0]["order"] = order
                self.assertIn("architecture_module_order_invalid:gateway", self.check_document())

    def test_rejects_internal_progress_fields_in_product_contract(self):
        self.document["architecture"]["progress"] = {"verifiedPercent": 100}
        self.assertIn("architecture_document_schema_invalid:docs/governance/architecture.json", self.check_document())

    def test_rejects_path_outside_repository(self):
        self.rule["path"] = "../external.json"
        self.assertEqual(gate.validate_architecture_authority(self.repo, self.rule), ["architecture_authority_rule_invalid"])

    def test_repository_contract_retains_canonical_module_orders(self):
        repo = Path(__file__).resolve().parents[2]
        path = repo / "docs/governance/harness-architecture.json"
        document = json.loads(path.read_text())
        self.assertEqual(set(document), {"schemaVersion", "architecture"})
        self.assertEqual(set(document["architecture"]), {"modules"})
        spec = json.loads((repo / "docs/governance/module-topology.json").read_text())
        rule = next(item for item in spec["architectureAuthorities"] if item["path"] == str(path.relative_to(repo)))
        self.assertEqual(gate.validate_architecture_authority(repo, rule), [])


if __name__ == "__main__":
    unittest.main()

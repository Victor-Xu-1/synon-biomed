#!/usr/bin/env python3
from __future__ import annotations

import copy, json, os
from pathlib import Path
import subprocess, sys, tempfile, unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))

import repository_state


class RepositoryStateTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.repo = Path(self.temporary.name)
        self.git("init", "-q")
        self.git("config", "user.email", "sentinel@example.invalid")
        self.git("config", "user.name", "Sentinel Test")
        (self.repo / "frontend").mkdir()
        (self.repo / "frontend/a.ts").write_text("export const value = 0;\n", encoding="utf-8")
        self.git("add", "frontend/a.ts")
        self.git("commit", "-qm", "baseline")

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def git(self, *args: str) -> str:
        result = subprocess.run(
            ["git", "-C", str(self.repo), *args], check=True,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
        )
        return result.stdout.strip()

    def modified_snapshot(self, content: str = "export const value = 1;\n") -> dict:
        (self.repo / "frontend/a.ts").write_text(content, encoding="utf-8")
        return repository_state.snapshot(str(self.repo), "test")

    @staticmethod
    def record(snapshot: dict, path: str) -> dict:
        matches = [item for item in snapshot["records"] if item.get("path") == path]
        if len(matches) != 1:
            raise AssertionError(f"expected one record for {path}, got {matches!r}")
        return matches[0]

    @staticmethod
    def approved_ledger(snapshot: dict, path: str = "frontend/a.ts") -> dict:
        record = RepositoryStateTests.record(snapshot, path)
        bindings = repository_state.authority_bindings(snapshot)
        ledger = dict(bindings)
        ledger["entries"] = [{
            "path": path, "owner": record["owner"], "category": record["category"],
            "source_confidence": "verified", "port_approved": True, "approved_by": "controller",
            "source_repository_id": bindings["repository_id"], "source_head": bindings["head"],
            "source_status_shape_sha256": bindings["status_shape_sha256"],
            "source_content_manifest_sha256": bindings["content_manifest_sha256"],
            "source_tool_sha256": bindings["tool_sha256"],
            "source_classification_version": bindings["classification_version"],
            "source_snapshot_fingerprint": bindings["snapshot_fingerprint"],
            "source_record_fingerprint": record["record_fingerprint"],
            "source_content_sha256": record["content"].get("sha256", "0" * 64),
        }]
        return ledger

    @staticmethod
    def resign(snapshot: dict) -> dict:
        for record in snapshot["records"]:
            record.pop("record_fingerprint", None)
            record["record_fingerprint"] = repository_state.sha256_bytes(repository_state.canonical_json(record))
        snapshot["content_manifest_sha256"] = repository_state.sha256_bytes(
            repository_state.canonical_json([record["record_fingerprint"] for record in snapshot["records"]])
        )
        authority = {
            "schema_version": snapshot["schema_version"], "repository_id": snapshot["repository_id"],
            "head": snapshot["head"], "status_shape_sha256": snapshot["status_shape_sha256"],
            "content_manifest_sha256": snapshot["content_manifest_sha256"], "tool_sha256": snapshot["tool"]["sha256"],
            "classification_version": snapshot["tool"]["classification_version"], "coverage": snapshot["coverage"],
        }
        snapshot["snapshot_fingerprint"] = repository_state.sha256_bytes(repository_state.canonical_json(authority))
        return snapshot

    def test_path_category_does_not_confuse_source_directories_with_user_data(self) -> None:
        cases = {
            "internal/persistence/workspace/versioned_schema.go": "source", "frontend/pages/conversation/runtime/frameStatus.ts": "source",
            "runtime-data/workspace.sqlite3": "user-data", "uploads/research-subject.pdf": "user-data",
            "fixtures/workspace.db": "user-data", "frontend/package-lock.json": "dependency",
            "internal/kernel/mcp_requirements.lock": "dependency", "frontend/package.json": "source", ".env.example": "source",
            ".env.development.local": "credential", "config/service-account.json": "credential",
            "assets/optional/kernel-compute.manifest.json": "source", "assets/optional/kernels/kernel_worker.R": "source",
            "internal/server/assets/prompt_template.txt": "source",
            ".dockerignore": "source", ".gitattributes": "source", ".gitignore": "source",
            "build/scientific/engine/Containerfile": "source",
            "scripts/dev/source-dev-supervisor.ps1": "source",
            "assets/branding/logo.png": "source", "frontend/public/branding/logo.svg": "source",
            "synon": "build", "server.test": "build",
            "mcp-ketcher": "user-data", "mcp-ketcher-shm": "user-data", "mcp-ketcher-wal": "user-data",
            "frontend/pw_probe.mjs": "cache", "frontend/complex_probe_no_composer.png": "cache",
            "frontend/pw_future_product_module.ts": "source",
            "frontend/public/logo.png:Zone.Identifier": "cache",
            "Makefile": "source", "frontend/public/manifest.webmanifest": "source",
            "frontend/packages/desktop/src/renderer/assets/icons/send.svg": "source",
            "skills/example/LICENSE.txt": "docs", "skills/example/requirements.txt": "dependency",
        }
        for path, expected in cases.items():
            with self.subTest(path=path):
                self.assertEqual(repository_state.path_category(path), expected)

    def test_synon_module_owners_cover_governed_source_areas(self) -> None:
        cases = {
            "assets/optional/kernel.py": "core-runtime",
            "skills/synonbiomed/example/SKILL.md": "core-runtime",
            "scripts/optional/websearch.py": "repository-steward",
            ".dockerignore": "repository-steward",
            "frontend/package.json": "product-ux",
            "scripts/sidecars/runtime.py": "repository-steward",
            ".env.example": "repository-steward",
            "build/scientific/engine/Containerfile": "core-runtime",
            "synon": "core-runtime", "mcp-ketcher": "core-runtime",
            "docs/quality-testing/compute-panel-design-qa.md": "repository-steward",
        }
        for path, expected in cases.items():
            with self.subTest(path=path):
                self.assertEqual(repository_state.path_owner(path), expected)

    def test_hygiene_report_groups_dispositions_without_exposing_user_data(self) -> None:
        (self.repo / "frontend/a.ts").write_text("export const value = 1;\n", encoding="utf-8")
        (self.repo / "synon").write_bytes(b"binary")
        (self.repo / "mcp-ketcher").write_bytes(b"private")
        snapshot = repository_state.snapshot(str(self.repo), "hygiene")
        report = repository_state.hygiene_report(snapshot)
        self.assertEqual("repository-hygiene-report", report["kind"])
        self.assertEqual(1, report["cohort_counts"]["discard_or_regenerate"])
        self.assertEqual(1, report["cohort_counts"]["sensitive_quarantine"])
        rendered = json.dumps(report)
        self.assertNotIn("mcp-ketcher", rendered)
        self.assertIn("redacted:", rendered)
        self.assertFalse(report["completion"]["all_origins_reviewed"])

    def test_hygiene_report_separates_dependency_authority_from_competing_managers(self) -> None:
        (self.repo / "frontend/package-lock.json").write_text("{}\n", encoding="utf-8")
        (self.repo / "frontend/pnpm-lock.yaml").write_text("lockfileVersion: 9\n", encoding="utf-8")
        snapshot = repository_state.snapshot(str(self.repo), "dependencies")
        report = repository_state.hygiene_report(snapshot)
        self.assertEqual(1, report["cohort_counts"]["dependency_change_review"])
        self.assertEqual(1, report["cohort_counts"]["competing_dependency_authority"])
        self.assertEqual(0, report["cohort_counts"]["discard_or_regenerate"])

    def test_porcelain_v2_counts_rename_once(self) -> None:
        raw = (
            b"1 .M N... 100644 100644 100644 aaaaaaa aaaaaaa frontend/a.ts\0"
            b"2 R. N... 100644 100644 100644 bbbbbbb bbbbbbb R100 docs/new.md\0docs/old.md\0"
            b"? internal/new.go\0"
        )
        records = repository_state.parse_status(raw)
        self.assertEqual(3, len(records))
        self.assertEqual("docs/old.md", records[1]["previous_path"])
        self.assertEqual("product-ux", records[0]["owner"])
        self.assertEqual("repository-steward", records[1]["owner"])
        self.assertEqual("core-runtime", records[2]["owner"])

    def test_same_modified_status_with_different_content_is_detected(self) -> None:
        before = self.modified_snapshot("export const value = 1;\n"); after = self.modified_snapshot("export const value = 2;\n")
        self.assertEqual(before["status_shape_sha256"], after["status_shape_sha256"])
        self.assertNotEqual(before["content_manifest_sha256"], after["content_manifest_sha256"])
        delta = repository_state.compare(before, after)
        self.assertEqual((False, False), (delta["unchanged"], delta["evidence_equivalent"]))
        self.assertTrue(repository_state.compare(before, before)["unchanged"])

    def test_snapshot_rejects_content_drift_between_manifest_passes(self) -> None:
        (self.repo / "frontend/a.ts").write_text("export const value = 1;\n", encoding="utf-8")
        original = repository_state.content_manifest
        calls = 0

        def mutate_after_first(root: Path, records: list[dict]):
            nonlocal calls
            result = original(root, records)
            calls += 1
            if calls == 1:
                (self.repo / "frontend/a.ts").write_text("export const value = 2;\n", encoding="utf-8")
            return result

        with mock.patch.object(repository_state, "content_manifest", side_effect=mutate_after_first):
            with self.assertRaisesRegex(repository_state.AuditError, "content manifest drifted"):
                repository_state.snapshot(str(self.repo), "drift")

    def test_sensitive_user_data_and_unknown_are_not_read_and_prevent_full_claim(self) -> None:
        (self.repo / "frontend/a.ts").write_text("export const value = 1;\n", encoding="utf-8")
        (self.repo / ".env").write_text("SECRET=do-not-read\n", encoding="utf-8"); (self.repo / "study.sqlite").write_bytes(b"private-data")
        (self.repo / "mystery").write_text("unknown", encoding="utf-8")
        original = repository_state.hash_regular_file

        def guarded(path: Path) -> str:
            if path.name in {".env", "study.sqlite", "mystery"}:
                raise AssertionError(f"withheld file was read: {path.name}")
            return original(path)

        with mock.patch.object(repository_state, "hash_regular_file", side_effect=guarded):
            first = repository_state.snapshot(str(self.repo), "withheld-1")
            second = repository_state.snapshot(str(self.repo), "withheld-2")
        rendered = json.dumps(first)
        self.assertNotIn(str(self.repo), rendered)
        self.assertNotIn("study.sqlite", rendered)
        self.assertNotIn("mystery", rendered)
        self.assertFalse(first["coverage"]["complete"])
        delta = repository_state.compare(first, second)
        self.assertFalse(delta["unchanged"])
        self.assertTrue(delta["indeterminate"])

    def test_symlink_deleted_and_gitlink_use_non_content_evidence(self) -> None:
        docs = self.repo / "docs"; docs.mkdir()
        (docs / "deleted.md").write_text("historical\n", encoding="utf-8")
        os.symlink("old-target.md", docs / "link.md")
        self.git("add", "docs/deleted.md", "docs/link.md"); self.git("commit", "-qm", "add docs")
        (docs / "deleted.md").unlink(); (docs / "link.md").unlink(); os.symlink("target.md", docs / "link.md")
        snapshot = repository_state.snapshot(str(self.repo), "types")
        self.assertEqual("deleted", self.record(snapshot, "docs/deleted.md")["content"]["kind"])
        self.assertEqual("symlink-target", self.record(snapshot, "docs/link.md")["content"]["kind"])
        record = repository_state.classify_record("docs/submodule", None, "1", ".M")
        record.update({"head_mode": "160000", "index_mode": "160000", "worktree_mode": "160000", "submodule": "S...", "head_oid": "a" * 40, "index_oid": "a" * 40})
        self.assertEqual("gitlink", repository_state.record_content(self.repo, record)["kind"])

    def test_port_validation_accepts_only_exact_controller_recorded_binding(self) -> None:
        snapshot = self.modified_snapshot()
        ledger = self.approved_ledger(snapshot)
        result = repository_state.validate_port(ledger, snapshot, ["frontend/a.ts"])
        self.assertTrue(result["valid"], result)
        self.assertIn("no cryptographic signature", result["approval_semantics"])

    def test_port_validation_rejects_wrong_stale_and_duplicate_evidence(self) -> None:
        snapshot = self.modified_snapshot()
        baseline = self.approved_ledger(snapshot)
        cases = {
            "repo": ("top", "repository_id", "0" * 64), "head": ("top", "head", "deadbeef"),
            "tool": ("top", "tool_sha256", "1" * 64), "classification": ("top", "classification_version", 999),
            "owner": ("entry", "owner", "core-runtime"), "category": ("entry", "category", "docs"),
            "hash": ("entry", "source_content_sha256", "not-a-hash"), "stale": ("entry", "source_snapshot_fingerprint", "2" * 64),
            "approver": ("entry", "approved_by", "reviewer"),
        }
        for name, (scope, field, value) in cases.items():
            with self.subTest(name=name):
                ledger = copy.deepcopy(baseline)
                target = ledger if scope == "top" else ledger["entries"][0]
                target[field] = value
                self.assertFalse(repository_state.validate_port(ledger, snapshot, ["frontend/a.ts"])["valid"])
        duplicate = copy.deepcopy(baseline)
        duplicate["entries"].append(copy.deepcopy(duplicate["entries"][0]))
        self.assertFalse(repository_state.validate_port(duplicate, snapshot, ["frontend/a.ts"])["valid"])
        self.assertFalse(repository_state.validate_port(baseline, snapshot, ["frontend/a.ts", "frontend/a.ts"])["valid"])
        self.assertFalse(repository_state.validate_port(baseline, snapshot, ["../frontend/a.ts"])["valid"])

    def test_compare_rejects_tampered_duplicate_and_forged_coverage(self) -> None:
        snapshot = self.modified_snapshot()
        variants = []
        tampered = copy.deepcopy(snapshot); tampered["records"][0]["owner"] = "core-runtime"; variants.append(tampered)
        duplicate = copy.deepcopy(snapshot); duplicate["records"].append(copy.deepcopy(duplicate["records"][0])); variants.append(duplicate)
        forged = copy.deepcopy(snapshot); forged["coverage"]["captured"] += 1; variants.append(forged)
        for value in variants:
            with self.assertRaises(repository_state.AuditError):
                repository_state.compare(snapshot, value)

    def test_unassigned_source_cannot_be_controller_approved(self) -> None:
        (self.repo / "loose.py").write_text("print('loose')\n", encoding="utf-8")
        snapshot = repository_state.snapshot(str(self.repo), "unassigned")
        record = self.record(snapshot, "loose.py")
        self.assertEqual(("unassigned", "not-covered"), (record["owner"], record["content"]["state"]))
        ledger = self.approved_ledger(snapshot, "loose.py")
        self.assertFalse(repository_state.validate_port(ledger, snapshot, ["loose.py"])["valid"])

    def test_sensitive_previous_rename_is_withheld_without_reading_or_path_disclosure(self) -> None:
        (self.repo / ".env").write_text("SECRET=rename\n", encoding="utf-8")
        self.git("add", ".env"); self.git("commit", "-qm", "add sensitive")
        (self.repo / "docs").mkdir(); self.git("mv", ".env", "docs/safe.md")
        with mock.patch.object(repository_state, "hash_regular_file", side_effect=AssertionError("renamed secret read")):
            snapshot = repository_state.snapshot(str(self.repo), "sensitive-previous")
        renamed = next(record for record in snapshot["records"] if record["record_type"] == "2")
        self.assertEqual(("credential", "withheld"), (renamed["previous_category"], renamed["content"]["state"]))
        rendered = json.dumps(snapshot)
        self.assertNotIn(".env", rendered); self.assertNotIn("docs/safe.md", rendered)

    def test_sensitive_current_rename_withholds_both_paths(self) -> None:
        (self.repo / "docs").mkdir(); (self.repo / "docs/source.md").write_text("secret later\n", encoding="utf-8")
        self.git("add", "docs/source.md"); self.git("commit", "-qm", "add docs")
        self.git("mv", "docs/source.md", ".env.production")
        with mock.patch.object(repository_state, "hash_regular_file", side_effect=AssertionError("current secret read")):
            snapshot = repository_state.snapshot(str(self.repo), "sensitive-current")
        renamed = next(record for record in snapshot["records"] if record["record_type"] == "2")
        self.assertEqual(("credential", "withheld"), (renamed["category"], renamed["content"]["state"]))
        rendered = json.dumps(snapshot)
        self.assertNotIn("docs/source.md", rendered); self.assertNotIn(".env.production", rendered)

    def test_cross_owner_rename_requires_controller_dual_owner_binding(self) -> None:
        (self.repo / "internal").mkdir(); self.git("mv", "frontend/a.ts", "internal/a.ts")
        snapshot = repository_state.snapshot(str(self.repo), "cross-owner")
        record = self.record(snapshot, "internal/a.ts")
        self.assertEqual((True, "product-ux", "core-runtime"), (record["cross_owner"], record["previous_owner"], record["owner"]))
        ledger = self.approved_ledger(snapshot, "internal/a.ts")
        self.assertFalse(repository_state.validate_port(ledger, snapshot, ["internal/a.ts"])["valid"])
        ledger["entries"][0].update({"cross_owner_approved": True, "previous_owner": "product-ux",
                                      "approved_owners": ["product-ux", "core-runtime"]})
        self.assertTrue(repository_state.validate_port(ledger, snapshot, ["internal/a.ts"])["valid"])

    def test_source_retained_sensitive_and_cross_owner_copies_are_origin_unknown(self) -> None:
        (self.repo / ".env").write_text("SECRET=copy\n", encoding="utf-8")
        self.git("add", ".env"); self.git("commit", "-qm", "add sensitive")
        (self.repo / "docs").mkdir(); (self.repo / "docs/safe.md").write_text("SECRET=copy\n", encoding="utf-8")
        self.git("add", "docs/safe.md")
        with mock.patch.object(repository_state, "hash_regular_file", side_effect=AssertionError("added copy read")):
            sensitive = repository_state.snapshot(str(self.repo), "retained-sensitive-copy")
        record = self.record(sensitive, "docs/safe.md")
        self.assertEqual(("1", "unknown", "not-covered"),
                         (record["record_type"], record["origin_provenance"], record["content"]["state"]))
        self.assertNotIn(".env", json.dumps(sensitive))
        self.assertEqual([], repository_state.validate_snapshot(sensitive))
        self.assertFalse(repository_state.validate_port(self.approved_ledger(sensitive, "docs/safe.md"), sensitive,
                                                        ["docs/safe.md"])["valid"])

        self.git("rm", "--cached", "-q", "docs/safe.md"); (self.repo / "docs/safe.md").unlink()
        (self.repo / "internal").mkdir(); (self.repo / "internal/a.ts").write_text("export const value = 0;\n", encoding="utf-8")
        with mock.patch.object(repository_state, "hash_regular_file", side_effect=AssertionError("untracked copy read")):
            cross_owner = repository_state.snapshot(str(self.repo), "retained-cross-owner-copy")
        copied = self.record(cross_owner, "internal/a.ts")
        self.assertEqual(("?", "unknown", "not-covered"),
                         (copied["record_type"], copied["origin_provenance"], copied["content"]["state"]))
        self.assertEqual([], repository_state.validate_snapshot(cross_owner))
        self.assertFalse(repository_state.validate_port(self.approved_ledger(cross_owner, "internal/a.ts"), cross_owner,
                                                        ["internal/a.ts"])["valid"])

    def test_snapshot_rejects_forged_previous_redaction_summary_and_malformed_types(self) -> None:
        baseline = self.modified_snapshot()
        variants = []
        previous = copy.deepcopy(baseline)
        previous["records"][0].update({"previous_path": "docs/old.md", "previous_category": "docs",
                                        "previous_owner": "repository-steward", "previous_sensitivity": "normal",
                                        "cross_owner": True})
        variants.append(self.resign(previous))
        summary = copy.deepcopy(baseline); summary["summary"]["status_records"] += 1; variants.append(summary)
        malformed = copy.deepcopy(baseline); malformed["records"][0]["path"] = []; variants.append(self.resign(malformed))
        malformed_category = copy.deepcopy(baseline); malformed_category["records"][0]["category"] = []
        variants.append(self.resign(malformed_category))
        count = copy.deepcopy(baseline); count["worktree_count"] = 1; variants.append(count)

        (self.repo / ".env").write_text("SECRET=redact\n", encoding="utf-8")
        sensitive = repository_state.snapshot(str(self.repo), "redacted")
        redacted = copy.deepcopy(sensitive); redacted["records"][0]["path"] = "redacted:plaintext"
        variants.append(self.resign(redacted))
        for value in variants:
            with self.assertRaises(repository_state.AuditError):
                repository_state.compare(sensitive if value is variants[-1] else baseline, value)

    def test_invalid_snapshot_cli_fails_without_traceback(self) -> None:
        snapshot = self.modified_snapshot(); ledger = self.approved_ledger(snapshot)
        before_path = self.repo / "before.json"; bad_path = self.repo / "bad.json"; ledger_path = self.repo / "ledger.json"
        before_path.write_text(json.dumps(snapshot), encoding="utf-8")
        ledger_path.write_text(json.dumps(ledger), encoding="utf-8")

        def assert_controlled(command: list[str]) -> None:
            result = subprocess.run([sys.executable, repository_state.__file__, *command], text=True,
                                    stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            self.assertEqual(2, result.returncode, (command, result.stdout, result.stderr))
            self.assertNotIn("Traceback", result.stderr)
            self.assertNotIn(str(bad_path), result.stderr)

        primitives = (None, [], {}, 7, "invalid", True)
        for field in ("tool", "sampling", "coverage", "summary", "records"):
            for primitive in primitives:
                with self.subTest(field=field, primitive=primitive):
                    malformed = copy.deepcopy(snapshot); malformed[field] = primitive
                    if field == "records" and primitive == []:
                        self.assertTrue(repository_state.validate_snapshot(malformed))
                    else:
                        with self.assertRaises(repository_state.AuditError):
                            repository_state.validate_snapshot(malformed)
                    bad_path.write_text(json.dumps(malformed), encoding="utf-8")
                    assert_controlled(["compare", "--before", str(before_path), "--after", str(bad_path)])
                    assert_controlled(["validate-port", "--ledger", str(ledger_path), "--snapshot", str(bad_path),
                                       "--path", "frontend/a.ts"])

        for primitive in primitives:
            malformed = copy.deepcopy(snapshot); malformed["records"][0]["record_type"] = primitive
            with self.assertRaises(repository_state.AuditError):
                repository_state.validate_snapshot(malformed)
            bad_path.write_text(json.dumps(malformed), encoding="utf-8")
            assert_controlled(["compare", "--before", str(before_path), "--after", str(bad_path)])
            assert_controlled(["validate-port", "--ledger", str(ledger_path), "--snapshot", str(bad_path),
                               "--path", "frontend/a.ts"])

        malformed_ledger = copy.deepcopy(ledger); malformed_ledger["entries"] = True
        bad_path.write_text(json.dumps(malformed_ledger), encoding="utf-8")
        assert_controlled(["validate-port", "--ledger", str(bad_path), "--snapshot", str(before_path),
                           "--path", "frontend/a.ts"])

        bad_path.write_bytes(b'{"invalid":"\xff"}')
        assert_controlled(["compare", "--before", str(before_path), "--after", str(bad_path)])
        assert_controlled(["validate-port", "--ledger", str(ledger_path), "--snapshot", str(bad_path),
                           "--path", "frontend/a.ts"])
        assert_controlled(["validate-port", "--ledger", str(bad_path), "--snapshot", str(before_path),
                           "--path", "frontend/a.ts"])

    def test_hygiene_report_cli_is_fingerprint_bound_and_fails_closed(self) -> None:
        snapshot = self.modified_snapshot()
        snapshot_path = self.repo / "snapshot.json"
        output_path = self.repo / "hygiene.json"
        snapshot_path.write_text(json.dumps(snapshot), encoding="utf-8")
        result = subprocess.run(
            [sys.executable, repository_state.__file__, "hygiene-report", "--snapshot", str(snapshot_path),
             "--output", str(output_path)],
            text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )
        self.assertEqual(0, result.returncode, result.stderr)
        report = json.loads(output_path.read_text(encoding="utf-8"))
        self.assertEqual(snapshot["snapshot_fingerprint"], report["snapshot_fingerprint"])
        self.assertEqual(snapshot["summary"], report["summary"])

        malformed = copy.deepcopy(snapshot)
        malformed["summary"]["status_records"] += 1
        snapshot_path.write_text(json.dumps(malformed), encoding="utf-8")
        result = subprocess.run(
            [sys.executable, repository_state.__file__, "hygiene-report", "--snapshot", str(snapshot_path)],
            text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )
        self.assertEqual(2, result.returncode)
        self.assertNotIn("Traceback", result.stderr)
        self.assertNotIn(str(snapshot_path), result.stderr)

    def test_snapshot_shape_rejects_truthy_and_unknown_semantic_smuggling(self) -> None:
        snapshot = self.modified_snapshot(); ledger = self.approved_ledger(snapshot)
        variants = []
        stable = copy.deepcopy(snapshot); stable["sampling"]["stable"] = "true"; variants.append(stable)
        confidence = copy.deepcopy(snapshot); confidence["records"][0]["source_confidence"] = []; variants.append(self.resign(confidence))
        metadata = copy.deepcopy(snapshot); metadata["records"][0]["metadata"] = {}; variants.append(self.resign(metadata))
        content = copy.deepcopy(snapshot); content["records"][0]["content"]["metadata"] = []; variants.append(self.resign(content))
        missing = copy.deepcopy(snapshot); del missing["sampling"]["passes"]; variants.append(missing)
        previous = copy.deepcopy(snapshot); del previous["records"][0]["previous_owner"]; variants.append(self.resign(previous))
        for value in variants:
            with self.subTest(keys=value.keys()):
                with self.assertRaises(repository_state.AuditError):
                    repository_state.validate_snapshot(value)
                with self.assertRaises(repository_state.AuditError):
                    repository_state.compare(snapshot, value)
                with self.assertRaises(repository_state.AuditError):
                    repository_state.validate_port(ledger, value, ["frontend/a.ts"])
        passes = copy.deepcopy(snapshot); passes["sampling"]["passes"] = 0
        writable = copy.deepcopy(snapshot); writable["read_only"] = False
        for value in (passes, writable):
            self.assertTrue(repository_state.validate_snapshot(value))
            with self.assertRaises(repository_state.AuditError):
                repository_state.compare(snapshot, value)
            self.assertFalse(repository_state.validate_port(ledger, value, ["frontend/a.ts"])["valid"])

    def test_ledger_shape_is_complete_presence_aware_and_cli_fail_closed(self) -> None:
        snapshot = self.modified_snapshot(); ledger = self.approved_ledger(snapshot)
        snapshot_path = self.repo / "snapshot.json"; ledger_path = self.repo / "ledger.json"
        snapshot_path.write_text(json.dumps(snapshot), encoding="utf-8")

        def cli(candidate: dict, want: int, paths: tuple[str, ...] = ("frontend/a.ts",)) -> None:
            ledger_path.write_text(json.dumps(candidate), encoding="utf-8")
            result = subprocess.run([sys.executable, repository_state.__file__, "validate-port",
                                     "--ledger", str(ledger_path), "--snapshot", str(snapshot_path),
                                     *sum((["--path", path] for path in paths), [])], text=True,
                                    stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            self.assertEqual(want, result.returncode, (candidate, result.stdout, result.stderr))
            self.assertNotIn("Traceback", result.stderr); self.assertNotIn(str(ledger_path), result.stderr)

        cli(ledger, 0)
        binding_failure = copy.deepcopy(ledger); binding_failure["repository_id"] = "0" * 64; cli(binding_failure, 3)
        approval_failure = copy.deepcopy(ledger); approval_failure["entries"][0]["approved_by"] = "reviewer"; cli(approval_failure, 3)
        duplicate = copy.deepcopy(ledger); duplicate["entries"].append(copy.deepcopy(duplicate["entries"][0])); cli(duplicate, 3)
        cli(ledger, 3, ("frontend/a.ts", "frontend/a.ts"))

        string_top = ("repository_id", "head", "status_shape_sha256", "content_manifest_sha256",
                      "tool_sha256", "snapshot_fingerprint")
        for field in string_top:
            for wrong in (None, [], {}, 7, True):
                candidate = copy.deepcopy(ledger); candidate[field] = wrong
                with self.subTest(top=field, wrong=wrong): cli(candidate, 2)
        for wrong in (None, [], {}, "4", True):
            candidate = copy.deepcopy(ledger); candidate["classification_version"] = wrong; cli(candidate, 2)

        string_bindings = ("source_repository_id", "source_head", "source_status_shape_sha256",
                           "source_content_manifest_sha256", "source_tool_sha256", "source_snapshot_fingerprint",
                           "source_record_fingerprint", "source_content_sha256")
        for field in string_bindings:
            for wrong in (None, [], {}, 7, True):
                candidate = copy.deepcopy(ledger); candidate["entries"][0][field] = wrong
                with self.subTest(binding=field, wrong=wrong): cli(candidate, 2)
        for wrong in (None, [], {}, "4", True):
            candidate = copy.deepcopy(ledger); candidate["entries"][0]["source_classification_version"] = wrong; cli(candidate, 2)
        for field, value in (("source_confidence", []), ("previous_owner", []), ("approved_owners", None)):
            candidate = copy.deepcopy(ledger); candidate["entries"][0][field] = value
            with self.subTest(optional=field): cli(candidate, 2)
        missing = copy.deepcopy(ledger); del missing["entries"][0]["source_head"]; cli(missing, 2)
        unknown = copy.deepcopy(ledger); unknown["entries"][0]["extension"] = {}; cli(unknown, 2)
        with self.assertRaises(repository_state.AuditError):
            repository_state.validate_port(ledger, snapshot, [7])


if __name__ == "__main__":
    unittest.main()

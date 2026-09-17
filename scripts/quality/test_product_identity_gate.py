import copy
import json
import pathlib
import subprocess
import tempfile
import unittest

if __package__:
    from . import product_identity_gate as gate
else:
    import product_identity_gate as gate


ROOT = pathlib.Path(__file__).parents[2]


def write_json(path: pathlib.Path, value: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True); path.write_text(json.dumps(value), encoding="utf-8")


def identity() -> dict:
    return {"schema": gate.IDENTITY_SCHEMA, "display_name": "Synon Biomed", "version": "0.1.0", "machine_slug": "synon-biomed"}


def reference() -> dict:
    return {
        "schema": gate.REFERENCE_SCHEMA, "authority_path": "product-identity.json", "authority_owner": "controller",
        "status": "authoritative-reference-external-release-authorization",
        "current_workspace_schema": {"target": 24, "authority_path": "internal/persistence/workspace/versioned_schema.go"},
        "planned_schema_integration": {"classification": "no-pending-schema-integration", "migration_numbers": [], "not_current_schema": True},
        "rules": ["identity and schema are independent"],
    }


def release_policy() -> dict:
    return {
        "schema": gate.RELEASE_POLICY_SCHEMA,
        "authority_owner": "user",
        "operator_role": "release-operator",
        "repository": "Victor-Xu-1/synon-biomed",
        "product_identity_authority": "product-identity.json",
        "candidate_manifest": {
            "schema": "synon.release-candidate.v1",
            "workflow": ".github/workflows/quality.yml",
            "artifact_name_prefix": "synon-biomed-release-candidate-",
            "required_platforms": ["linux-amd64", "windows-amd64"],
            "build_once": True,
        },
        "authorization_receipt": {
            "schema": "synon.release-authorization-receipt.v1",
            "tracked_in_source": False,
            "signature_algorithm": "hmac-sha256",
            "max_ttl_seconds": 86400,
            "required_bindings": [
                "receipt_id", "authorization_reference", "repository",
                "candidate_run_id", "candidate_run_attempt", "source_commit",
                "source_tree", "product_version", "tag",
                "candidate_manifest_sha256", "artifact_set_sha256",
                "issued_at", "expires_at",
            ],
        },
        "promotion": {
            "tag_pattern": r"^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$",
            "annotated_tag_required": True,
            "same_artifact_bytes_required": True,
            "rebuild_forbidden": True,
            "draft_before_publish": True,
            "immutable_release_required": True,
            "receipt_single_use": True,
        },
        "rules": ["tracked source cannot authorize release"],
    }


def matrix() -> dict:
    classes = {
        "product-user-visible": "exact", "product-version-projection": "exact", "machine-compatibility": "retain",
        "persisted-format": "migrate", "dependency-name-or-version": "independent", "legacy-path": "retain",
        "historical-evidence": "immutable", "schema-or-contract-version": "independent", "test-fixture": "test only",
    }
    consumer = lambda surface, cls, owner, path: {
        "surface_id": surface, "class": cls, "owner": owner, "paths": [path], "current_disposition": "review",
        "target": "consume root authority", "release_blocking": True,
    }
    return {
        "schema": gate.MATRIX_SCHEMA, "authority_path": "product-identity.json",
        "reference_path": "docs/governance/product-identity.json",
        "release_policy_path": "docs/governance/release-policy.json",
        "status": "test", "classification_version": "v3", "classes": classes,
        "legacy_version_terms": ["4.0.2", "5.0.0"],
        "legacy_version_path_rules": [
            {"glob": "*_test.go", "class": "test-fixture", "disposition": "test-only literal"},
            {"glob": "**/*_test.go", "class": "test-fixture", "disposition": "test-only literal"},
            {"glob": "docs/governance/product-identity-consumer-matrix.json", "class": "schema-or-contract-version", "disposition": "gate contract"},
        ],
        "product_version_projections": [
            {"path": "package.json", "kind": "json-pointer", "pointer": "/version"},
            {"path": "identity.go", "kind": "go-embed-authority", "authority_path": "product-identity.json"},
            {"path": "internal/buildinfo/buildinfo.go", "kind": "go-derived-consumer", "required_markers": ["productidentity.Current()"]},
        ],
        "user_visible_name_projections": [
            {"path": "README.md", "kind": "line-prefix", "value_template": "# {full_display}"},
            {"path": "package.json", "kind": "json-prefix", "pointer": "/description", "value_field": "display_name"},
            {"path": "version.go", "kind": "text-occurrence", "value_field": "display_name", "occurrences": 1},
        ],
        "consumer_inventory": [
            consumer("ui", "product-user-visible", "product-ux", "package.json"),
            consumer("cli", "product-user-visible", "core-runtime", "version.go"),
            consumer("release-install", "machine-compatibility", "repository-steward", "README.md"),
            {
                "surface_id": "workspace-schema-and-contracts", "class": "schema-or-contract-version",
                "owner": "controller",
                "paths": ["internal/persistence/workspace/versioned_schema.go", "docs/governance/product-identity.json"],
                "current_disposition": "production workspace schema target is 24 with no pending schema cohort",
                "target": "advance only through reviewed migration predecessors and never infer product version",
                "release_blocking": False,
            },
        ],
        "classified_legacy_consumers": [{"surface": "module", "class": "dependency-name-or-version", "disposition": "retain"}],
        "rename_and_rollback_map": "docs/governance/product-rename-compatibility-map.json",
        "frame_control_correction": "pre-release cleanup",
    }


def seed(repo: pathlib.Path, version: str = "0.1.0", commit: bool = False) -> None:
    write_json(repo / "product-identity.json", identity()); write_json(repo / "docs/governance/product-identity.json", reference())
    write_json(repo / "docs/governance/release-policy.json", release_policy())
    write_json(repo / "docs/governance/product-identity-consumer-matrix.json", matrix())
    write_json(repo / "package.json", {"version": version, "description": "Synon Biomed workbench"})
    (repo / "version.go").write_text(f'const Version = "{version}"\nconst Name = "Synon Biomed"\n', encoding="utf-8")
    (repo / "go.mod").write_text("module synon-go\n\ngo 1.24\n", encoding="utf-8")
    (repo / "identity.go").write_text(
        'package productidentity\n\nimport (\n    _ "embed"\n    "encoding/json"\n)\n\n'
        '//go:embed product-identity.json\nvar embeddedAuthority []byte\n\n'
        'type Identity struct { DisplayName string `json:"display_name"`; Version string `json:"version"`; MachineSlug string `json:"machine_slug"` }\n'
        'var current = mustDecode(embeddedAuthority)\n'
        'func mustDecode(raw []byte) Identity { var value Identity; if err := json.Unmarshal(raw, &value); err != nil { panic(err) }; return value }\n'
        'func Current() Identity { return current }\n', encoding="utf-8",
    )
    (repo / "identity_test.go").write_text(
        'package productidentity\nimport "testing"\n'
        'func TestCurrentUsesRootProductIdentityAuthority(t *testing.T) { value := Current(); if value.DisplayName != "Synon Biomed" || value.Version != "0.1.0" || value.MachineSlug != "synon-biomed" { t.Fatal(value) } }\n',
        encoding="utf-8",
    )
    buildinfo = repo / "internal/buildinfo"; buildinfo.mkdir(parents=True, exist_ok=True)
    (buildinfo / "buildinfo.go").write_text(
        'package buildinfo\nimport productidentity "synon-go"\n'
        'type Info struct { Name, Version, MachineSlug string }\n'
        'func Release() Info { value := productidentity.Current(); return Info{Name: value.DisplayName, Version: value.Version, MachineSlug: value.MachineSlug} }\n',
        encoding="utf-8",
    )
    (buildinfo / "buildinfo_test.go").write_text(
        'package buildinfo\nimport "testing"\n'
        'func TestReleaseInfoUsesSynonBiomedIdentity(t *testing.T) { value := Release(); if value.Name != "Synon Biomed" || value.Version != "0.1.0" || value.MachineSlug != "synon-biomed" { t.Fatal(value) } }\n',
        encoding="utf-8",
    )
    (repo / "README.md").write_text("# Synon Biomed v0.1.0\n", encoding="utf-8")
    schema = repo / "internal/persistence/workspace/versioned_schema.go"; schema.parent.mkdir(parents=True, exist_ok=True)
    schema.write_text("package workspace\nconst workspaceSchemaVersion = 24\n", encoding="utf-8")
    if commit:
        subprocess.run(["git", "init", "-q", str(repo)], check=True)
        subprocess.run(["git", "-C", str(repo), "config", "user.email", "identity@test.invalid"], check=True)
        subprocess.run(["git", "-C", str(repo), "config", "user.name", "Identity Test"], check=True)
        subprocess.run(["git", "-C", str(repo), "add", "."], check=True)
        subprocess.run(["git", "-C", str(repo), "commit", "-qm", "seed"], check=True)


class ProductIdentityGateTests(unittest.TestCase):
    def test_current_candidate_documents_and_projections_are_aligned(self):
        load = lambda path: gate._load(ROOT / path, "test")
        authority = load("product-identity.json")
        result = gate.audit(
            ROOT,
            authority,
            load("docs/governance/product-identity.json"),
            load("docs/governance/release-policy.json"),
            load("docs/governance/product-identity-consumer-matrix.json"),
        )
        self.assertTrue(result["aligned"], result["drifts"])
        self.assertEqual(result["drift_count"], 0)
        self.assertEqual(result["drifts"], [])
        self.assertEqual(result["schema_facts"]["current_workspace_schema"], 67)
        self.assertIn(
            {"path": ".env.example", "kind": "line-prefix", "value_template": "# {full_display} safe local defaults."},
            load("docs/governance/product-identity-consumer-matrix.json")["user_visible_name_projections"],
        )
        self.assertEqual(
            (ROOT / ".env.example").read_text(encoding="utf-8").splitlines()[0],
            f"# {gate.derive_identity(authority)['full_display']} safe local defaults.",
        )
        projections = load("docs/governance/product-identity-consumer-matrix.json")["product_version_projections"]
        self.assertIn(
            {
                "path": "internal/server/server_health_runtime.go",
                "kind": "go-derived-consumer",
                "required_markers": ["buildinfo.Release()"],
            },
            projections,
        )
        self.assertIn(
            {
                "path": "scripts/dev/source-backend-watch.sh",
                "kind": "text-derived-consumer",
                "required_markers": ["./scripts/product-identity", "product_slug", "${product_slug}-dev"],
            },
            projections,
        )
        legacy_rules = load("docs/governance/product-identity-consumer-matrix.json")[
            "legacy_version_path_rules"
        ]
        self.assertIn(
            {
                "glob": "internal/server/scientific_runtime_catalog.go",
                "class": "dependency-name-or-version",
                "disposition": "pinned scientific runtime dependency versions",
            },
            legacy_rules,
        )
        self.assertIn(
            {
                "glob": "docs/licenses/synon-scientific-runtime-warmups/NOTICE.md",
                "class": "dependency-name-or-version",
                "disposition": "documented third-party scientific runtime dependency versions",
            },
            legacy_rules,
        )
        watcher = (ROOT / "scripts/dev/source-backend-watch.sh").read_text(encoding="utf-8")
        self.assertNotIn("4.0.2", watcher)
        rename_map = load("docs/governance/product-rename-compatibility-map.json")
        self.assertEqual(
            rename_map["status"],
            "identity-projections-aligned-external-release-authorization-required",
        )
        # Historical task snapshots are not product identity consumers or
        # clone prerequisites. Their absence must not weaken the real gate.
        self.assertFalse((ROOT / "REWRITE_STATUS.json").exists())
        self.assertFalse((ROOT / "docs/compatibility/evidence/rewrite-status-legacy.json").exists())
        self.assertNotIn(
            "historical-rewrite-status",
            [item["surface_id"] for item in load("docs/governance/product-identity-consumer-matrix.json")["consumer_inventory"]],
        )
        ketcher = load("assets/optional/mcp-servers/ketcher-chemistry.manifest.json")
        self.assertEqual((ketcher["source"], ketcher["version"]), ("EPAM Ketcher", "3.12.0"))
        self.assertNotIn("minimumVersion", ketcher["runtime"])
        for script in ("scripts/install-release.ps1", "scripts/manage-release.ps1"):
            self.assertNotIn("[string]$TrustedIdentityPath", (ROOT / script).read_text(encoding="utf-8"))
        package_script = (ROOT / "scripts/package-release.sh").read_text(encoding="utf-8")
        self.assertNotIn('cp scripts/install-release.sh "$pkg/scripts/install-release.sh"', package_script)
        self.assertNotIn('cp scripts/install-release.ps1 "$pkg/scripts/install-release.ps1"', package_script)
        reference_text = (ROOT / "docs/governance/product-identity.json").read_text(encoding="utf-8")
        for value in (authority["display_name"], authority["version"], authority["machine_slug"]):
            self.assertNotIn(value, reference_text)

    def test_derived_identity_and_schema_separation(self):
        with tempfile.TemporaryDirectory() as directory:
            repo = pathlib.Path(directory); seed(repo)
            result = gate.audit(repo, identity(), reference(), release_policy(), matrix())
            self.assertEqual(result["authority"]["full_display"], "Synon Biomed v0.1.0")
            self.assertEqual(result["authority"]["release_tag"], "v0.1.0")
            self.assertEqual(result["schema_facts"], {"current_workspace_schema": 24, "planned_migrations": []})
            self.assertNotIn("full_display", identity()); self.assertNotIn("release_tag", identity())
            (repo / "internal/persistence/workspace/versioned_schema.go").write_text("package workspace\nconst workspaceSchemaVersion = 25\n")
            with self.assertRaisesRegex(gate.IdentityError, "workspace_schema_mismatch"):
                gate.audit(repo, identity(), reference(), release_policy(), matrix())

    def test_workspace_schema_consumer_projection_must_match_reference(self):
        with tempfile.TemporaryDirectory() as directory:
            repo = pathlib.Path(directory); seed(repo)
            drifted = matrix()
            for item in drifted["consumer_inventory"]:
                if item["surface_id"] == "workspace-schema-and-contracts":
                    item["current_disposition"] = "production workspace schema target is 23 with no pending schema cohort"
            with self.assertRaisesRegex(gate.IdentityError, "identity_matrix_workspace_schema_mismatch"):
                gate.audit(repo, identity(), reference(), release_policy(), drifted)

    def test_candidate_drift_is_nonzero_and_ineligible(self):
        with tempfile.TemporaryDirectory() as directory:
            repo = pathlib.Path(directory); seed(repo, "4.0.2")
            result, code = gate.evaluate(repo, identity(), reference(), release_policy(), matrix(), "candidate")
            self.assertEqual(code, 3); self.assertFalse(result["candidate_eligible"]); self.assertFalse(result["release_ready"])

    def test_unclassified_legacy_product_version_is_ineligible(self):
        with tempfile.TemporaryDirectory() as directory:
            repo = pathlib.Path(directory); seed(repo)
            (repo / "leaked-runtime.txt").write_text("current product version 4.0.2\n", encoding="utf-8")
            result, code = gate.evaluate(repo, identity(), reference(), release_policy(), matrix(), "candidate")
            self.assertEqual(code, 3)
            self.assertIn(
                {"path": "leaked-runtime.txt", "class": "unclassified-product-version", "surface": "legacy-version"},
                result["drifts"],
            )
            classified = matrix()
            classified["legacy_version_path_rules"].append(
                {"glob": "leaked-runtime.txt", "class": "historical-evidence", "disposition": "frozen fixture"},
            )
            self.assertTrue(gate.audit(repo, identity(), reference(), release_policy(), classified)["aligned"])

    def test_retired_versioned_runtime_namespace_is_ineligible(self):
        with tempfile.TemporaryDirectory() as directory:
            repo = pathlib.Path(directory); seed(repo)
            retired = repo / "skills/synonbiomed-v1.1/example/SKILL.md"
            retired.parent.mkdir(parents=True)
            retired.write_text("# retired namespace\n", encoding="utf-8")
            result, code = gate.evaluate(repo, identity(), reference(), release_policy(), matrix(), "candidate")
            self.assertEqual(code, 3)
            self.assertIn(
                {
                    "path": "skills/synonbiomed-v1.1/example/SKILL.md",
                    "class": "product-version-path-drift",
                    "surface": "retired-versioned-runtime-namespace",
                },
                result["drifts"],
            )

    def test_classified_production_path_and_authority_cannot_hide_legacy_version(self):
        with tempfile.TemporaryDirectory() as directory:
            repo = pathlib.Path(directory); seed(repo)
            with (repo / "README.md").open("a", encoding="utf-8") as handle:
                handle.write("Current product version: 4.0.2\n")
            result = gate.audit(repo, identity(), reference(), release_policy(), matrix())
            self.assertIn("README.md", {item["path"] for item in result["drifts"]})
        with tempfile.TemporaryDirectory() as directory:
            repo = pathlib.Path(directory); seed(repo)
            downgraded = identity(); downgraded["version"] = "4.0.2"
            write_json(repo / "product-identity.json", downgraded)
            write_json(repo / "package.json", {"version": "4.0.2", "description": "Synon Biomed workbench"})
            with self.assertRaisesRegex(gate.IdentityError, "identity_authority_legacy_version"):
                gate.audit(repo, downgraded, reference(), release_policy(), matrix())

    def test_dead_embed_and_hardcoded_buildinfo_are_ineligible(self):
        with tempfile.TemporaryDirectory() as directory:
            repo = pathlib.Path(directory); seed(repo)
            (repo / "identity.go").write_text(
                'package productidentity\n/* //go:embed product-identity.json */\n', encoding="utf-8",
            )
            result, code = gate.evaluate(repo, identity(), reference(), release_policy(), matrix(), "candidate")
            self.assertEqual(code, 3)
            self.assertIn("identity.go", {item["path"] for item in result["drifts"]})
        with tempfile.TemporaryDirectory() as directory:
            repo = pathlib.Path(directory); seed(repo)
            (repo / "internal/buildinfo/buildinfo.go").write_text(
                'package buildinfo\ntype Info struct { Name, Version, MachineSlug string }\n'
                'func Release() Info { return Info{Name: "Synon Biomed", Version: "4.0.2", MachineSlug: "synon-go"} }\n',
                encoding="utf-8",
            )
            result, code = gate.evaluate(repo, identity(), reference(), release_policy(), matrix(), "candidate")
            self.assertEqual(code, 3)
            self.assertIn("internal/buildinfo/buildinfo.go", {item["path"] for item in result["drifts"]})

    def test_same_tree_binding_rejects_tamper_and_path_escape(self):
        with tempfile.TemporaryDirectory() as directory:
            repo = pathlib.Path(directory); seed(repo, commit=True)
            changed = identity(); changed["version"] = "9.9.9"; write_json(repo / "product-identity.json", changed)
            with self.assertRaisesRegex(gate.IdentityError, "control_tree_mismatch"): gate.load_control_plane(repo)
            with self.assertRaisesRegex(gate.IdentityError, "authority_path_invalid"):
                gate.load_control_plane(repo, identity_path="../authority/product-identity.json")

    def test_tracked_source_can_validate_policy_but_never_authorize_release(self):
        with tempfile.TemporaryDirectory() as directory:
            repo = pathlib.Path(directory); seed(repo)
            result, code = gate.evaluate(
                repo, identity(), reference(), release_policy(), matrix(), "release",
            )
            self.assertEqual(code, 3)
            self.assertTrue(result["candidate_eligible"])
            self.assertFalse(result["release_ready"])
            self.assertEqual(
                result["blockers"],
                ["identity_external_release_authorization_required"],
            )
            dynamic = release_policy()
            dynamic["release_authorized"] = True
            with self.assertRaisesRegex(
                gate.IdentityError, "release_policy_dynamic_state_forbidden",
            ):
                gate.evaluate(repo, identity(), reference(), dynamic, matrix(), "release")

    def test_duplicate_json_key_fails_closed(self):
        with self.assertRaisesRegex(gate.IdentityError, "duplicate_key"):
            gate._load_bytes(b'{"schema":"one","schema":"two"}', "invalid")


if __name__ == "__main__":
    unittest.main()

#!/usr/bin/env python3
"""Validate the single-tree Synon Biomed product identity control plane."""

from __future__ import annotations

import argparse
import fnmatch
import json
import pathlib
import re
import stat
import subprocess
import sys
from typing import Any


IDENTITY_SCHEMA = "synon.product-identity.v1"
REFERENCE_SCHEMA = "synon.governance.product-identity-reference.v1"
RELEASE_POLICY_SCHEMA = "synon.governance.release-policy.v1"
MATRIX_SCHEMA = "synon.governance.product-identity-consumers.v4"
SEMVER = re.compile(r"^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$")
ACTIVE_RELEASE_LINE = re.compile(r"^0\.1\.(0|[1-9][0-9]*)$")
SLUG = re.compile(r"^[a-z0-9]+(?:-[a-z0-9]+)*$")
SCHEMA_CONST = re.compile(r"(?m)^\s*const\s+workspaceSchemaVersion\s*=\s*([0-9]+)\s*$")
RETIRED_VERSIONED_RUNTIME_PREFIXES = (
    "assets/synonbiomed-v1.1/",
    "skills/synonbiomed-v1.1/",
)


class IdentityError(Exception):
    """Controlled product-identity gate failure."""


def _go_source_without_comments(text: str) -> tuple[str, list[str]]:
    """Return lexical Go source plus active //go directives, excluding comments."""
    output: list[str] = []
    directives: list[str] = []
    index = 0
    state = "code"
    quote = ""
    while index < len(text):
        char = text[index]
        next_char = text[index + 1] if index + 1 < len(text) else ""
        if state == "code":
            if char == "/" and next_char == "/":
                end = text.find("\n", index)
                if end < 0:
                    end = len(text)
                comment = text[index + 2:end]
                if comment.startswith("go:"):
                    directives.append(comment)
                output.extend(" " * (end - index))
                index = end
                continue
            if char == "/" and next_char == "*":
                output.extend("  ")
                index += 2
                state = "block"
                continue
            if char in {'"', "'", "`"}:
                quote = char
                state = "string"
            output.append(char)
            index += 1
            continue
        if state == "block":
            if char == "*" and next_char == "/":
                output.extend("  ")
                index += 2
                state = "code"
                continue
            output.append("\n" if char == "\n" else " ")
            index += 1
            continue
        output.append(char)
        index += 1
        if quote == "`":
            if char == "`":
                state = "code"
            continue
        if char == "\\" and index < len(text):
            output.append(text[index])
            index += 1
        elif char == quote:
            state = "code"
    return "".join(output), directives


def _object(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    value: dict[str, Any] = {}
    for key, item in pairs:
        if key in value:
            raise IdentityError("identity_json_duplicate_key")
        value[key] = item
    return value


def _load_bytes(raw: bytes, code: str) -> dict[str, Any]:
    try:
        value = json.loads(raw.decode("utf-8"), object_pairs_hook=_object)
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise IdentityError(code) from exc
    if type(value) is not dict:
        raise IdentityError(code)
    return value


def _load(path: pathlib.Path, code: str) -> dict[str, Any]:
    try:
        return _load_bytes(path.read_bytes(), code)
    except OSError as exc:
        raise IdentityError(code) from exc


def _safe(repo: pathlib.Path, relative: Any, code: str) -> pathlib.Path:
    if type(relative) is not str or not relative or "\\" in relative:
        raise IdentityError(code)
    pure = pathlib.PurePosixPath(relative)
    if pure.is_absolute() or ".." in pure.parts or pure.as_posix() != relative:
        raise IdentityError(code)
    root = repo.resolve()
    unresolved = root.joinpath(*pure.parts)
    try:
        metadata = unresolved.lstat()
    except OSError as exc:
        raise IdentityError(code) from exc
    if not stat.S_ISREG(metadata.st_mode) or metadata.st_nlink != 1:
        raise IdentityError(code)
    resolved = unresolved.resolve()
    try:
        resolved.relative_to(root)
    except ValueError as exc:
        raise IdentityError(code) from exc
    return resolved


def _git(repo: pathlib.Path, *args: str) -> bytes:
    try:
        result = subprocess.run(
            ["git", "-C", str(repo), *args], check=True, stdout=subprocess.PIPE,
            stderr=subprocess.PIPE, timeout=30,
        )
    except (OSError, subprocess.SubprocessError) as exc:
        raise IdentityError("identity_git_binding_failed") from exc
    return result.stdout


def _repository_paths(repo: pathlib.Path) -> list[str]:
    try:
        raw = _git(repo, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
        values = raw.decode("utf-8").split("\0")
    except UnicodeDecodeError as exc:
        raise IdentityError("identity_git_binding_failed") from exc
    except IdentityError:
        if repo.joinpath(".git").exists():
            raise
        values = [path.relative_to(repo).as_posix() for path in repo.rglob("*") if ".git" not in path.parts]
    paths = sorted(set(value for value in values if value))
    current_paths = []
    for value in paths:
        pure = pathlib.PurePosixPath(value)
        if pure.is_absolute() or ".." in pure.parts or pure.as_posix() != value:
            raise IdentityError("identity_audit_path_invalid")
        candidate = repo.joinpath(*pure.parts)
        try:
            candidate.lstat()
        except FileNotFoundError:
            # git ls-files includes tracked deletions. They are not part of the
            # current candidate tree; required consumers are checked separately
            # above and still fail closed when missing.
            continue
        except OSError as exc:
            raise IdentityError("identity_audit_read_invalid") from exc
        current_paths.append(value)
    return current_paths


def _contains_term(path: pathlib.Path, terms: tuple[bytes, ...]) -> bool:
    overlap = max(len(term) for term in terms) - 1
    tail = b""
    try:
        with path.open("rb") as handle:
            while chunk := handle.read(1024 * 1024):
                content = tail + chunk
                if any(term in content for term in terms):
                    return True
                tail = content[-overlap:] if overlap else b""
    except OSError as exc:
        raise IdentityError("identity_audit_read_invalid") from exc
    return False


def _load_same_tree(repo: pathlib.Path, relative: str, code: str) -> dict[str, Any]:
    path = _safe(repo, relative, code)
    try:
        top = pathlib.Path(_git(repo, "rev-parse", "--show-toplevel").decode("utf-8").strip()).resolve()
        committed = _git(repo, "show", f"HEAD:{relative}")
        working = path.read_bytes()
    except (OSError, UnicodeDecodeError) as exc:
        raise IdentityError("identity_control_tree_mismatch") from exc
    if top != repo.resolve() or committed != working:
        raise IdentityError("identity_control_tree_mismatch")
    return _load_bytes(committed, code)


def load_control_plane(
    repo: pathlib.Path,
    *,
    identity_path: str = "product-identity.json",
    reference_path: str = "docs/governance/product-identity.json",
    release_policy_path: str = "docs/governance/release-policy.json",
    matrix_path: str = "docs/governance/product-identity-consumer-matrix.json",
) -> tuple[dict[str, Any], dict[str, Any], dict[str, Any], dict[str, Any]]:
    repo = repo.resolve()
    return (_load_same_tree(repo, identity_path, "identity_authority_path_invalid"),
            _load_same_tree(repo, reference_path, "identity_reference_path_invalid"),
            _load_same_tree(repo, release_policy_path, "identity_release_policy_path_invalid"),
            _load_same_tree(repo, matrix_path, "identity_matrix_path_invalid"))


def _pointer(document: Any, pointer: Any) -> Any:
    if type(pointer) is not str or not pointer.startswith("/"):
        raise IdentityError("identity_projection_pointer_invalid")
    current = document
    for encoded in pointer[1:].split("/"):
        key = encoded.replace("~1", "/").replace("~0", "~")
        if type(current) is not dict or key not in current:
            raise IdentityError("identity_projection_pointer_missing")
        current = current[key]
    return current


def derive_identity(authority: dict[str, Any]) -> dict[str, str]:
    return {
        "display_name": authority["display_name"],
        "version": authority["version"],
        "machine_slug": authority["machine_slug"],
        "full_display": f'{authority["display_name"]} v{authority["version"]}',
        "release_tag": f'v{authority["version"]}',
    }


def validate_authority(authority: dict[str, Any]) -> None:
    if set(authority) != {"schema", "display_name", "version", "machine_slug"} or authority.get("schema") != IDENTITY_SCHEMA:
        raise IdentityError("identity_authority_shape_invalid")
    display, version, slug = authority.get("display_name"), authority.get("version"), authority.get("machine_slug")
    if type(display) is not str or not display or display.strip() != display or len(display) > 80:
        raise IdentityError("identity_authority_value_invalid")
    if type(version) is not str or not SEMVER.fullmatch(version):
        raise IdentityError("identity_authority_value_invalid")
    if not ACTIVE_RELEASE_LINE.fullmatch(version):
        raise IdentityError("identity_authority_version_line_invalid")
    if type(slug) is not str or not SLUG.fullmatch(slug):
        raise IdentityError("identity_authority_value_invalid")


def validate_reference(reference: dict[str, Any]) -> None:
    required = {
        "schema", "authority_path", "authority_owner", "status", "current_workspace_schema",
        "planned_schema_integration", "rules",
    }
    if set(reference) != required or reference.get("schema") != REFERENCE_SCHEMA:
        raise IdentityError("identity_reference_shape_invalid")
    if (
        reference.get("authority_path") != "product-identity.json"
        or reference.get("authority_owner") != "controller"
        or reference.get("status") != "authoritative-reference-external-release-authorization"
    ):
        raise IdentityError("identity_reference_authority_invalid")
    current = reference.get("current_workspace_schema")
    planned = reference.get("planned_schema_integration")
    if (
        type(current) is not dict
        or set(current) != {"target", "authority_path"}
        or type(current.get("target")) is not int
        or current.get("target") < 1
        or current.get("authority_path") != "internal/persistence/workspace/versioned_schema.go"
    ):
        raise IdentityError("identity_reference_schema_fact_invalid")
    migration_numbers = planned.get("migration_numbers") if type(planned) is dict else None
    if (
        type(planned) is not dict
        or set(planned) != {"classification", "migration_numbers", "not_current_schema"}
        or type(planned.get("classification")) is not str
        or not planned.get("classification")
        or type(migration_numbers) is not list
        or any(type(number) is not int for number in migration_numbers)
        or migration_numbers != sorted(set(migration_numbers))
        or any(number <= current["target"] for number in migration_numbers)
        or planned.get("not_current_schema") is not True
    ):
        raise IdentityError("identity_reference_schema_plan_invalid")
    rules = reference.get("rules")
    if type(rules) is not list or not rules or any(type(item) is not str or not item for item in rules):
        raise IdentityError("identity_reference_rules_invalid")


def validate_release_policy(policy: dict[str, Any]) -> None:
    forbidden_dynamic_fields = {"status", "release_authorized", "tag_authorized", "authorized_at"}
    if forbidden_dynamic_fields.intersection(policy):
        raise IdentityError("identity_release_policy_dynamic_state_forbidden")
    required = {
        "schema", "authority_owner", "operator_role", "repository",
        "product_identity_authority", "candidate_manifest",
        "authorization_receipt", "promotion", "rules",
    }
    if set(policy) != required or policy.get("schema") != RELEASE_POLICY_SCHEMA:
        raise IdentityError("identity_release_policy_shape_invalid")
    if (
        policy.get("authority_owner") != "user"
        or policy.get("operator_role") != "release-operator"
        or policy.get("repository") != "Victor-Xu-1/synon-biomed"
        or policy.get("product_identity_authority") != "product-identity.json"
    ):
        raise IdentityError("identity_release_policy_authority_invalid")

    candidate = policy.get("candidate_manifest")
    if candidate != {
        "schema": "synon.release-candidate.v1",
        "workflow": ".github/workflows/quality.yml",
        "artifact_name_prefix": "synon-biomed-release-candidate-",
        "required_platforms": ["linux-amd64", "windows-amd64"],
        "build_once": True,
    }:
        raise IdentityError("identity_release_policy_candidate_invalid")

    receipt = policy.get("authorization_receipt")
    expected_bindings = {
        "receipt_id", "authorization_reference", "repository", "candidate_run_id",
        "candidate_run_attempt", "source_commit", "source_tree", "product_version",
        "tag", "candidate_manifest_sha256", "artifact_set_sha256", "issued_at", "expires_at",
    }
    if (
        type(receipt) is not dict
        or set(receipt) != {
            "schema", "tracked_in_source", "signature_algorithm", "max_ttl_seconds",
            "required_bindings",
        }
        or receipt.get("schema") != "synon.release-authorization-receipt.v1"
        or receipt.get("tracked_in_source") is not False
        or receipt.get("signature_algorithm") != "hmac-sha256"
        or type(receipt.get("max_ttl_seconds")) is not int
        or not 1 <= receipt["max_ttl_seconds"] <= 86400
        or type(receipt.get("required_bindings")) is not list
        or len(receipt["required_bindings"]) != len(set(receipt["required_bindings"]))
        or set(receipt["required_bindings"]) != expected_bindings
    ):
        raise IdentityError("identity_release_policy_receipt_invalid")

    promotion = policy.get("promotion")
    if (
        type(promotion) is not dict
        or set(promotion) != {
            "tag_pattern", "annotated_tag_required", "same_artifact_bytes_required",
            "rebuild_forbidden", "draft_before_publish", "immutable_release_required",
            "receipt_single_use",
        }
        or promotion.get("tag_pattern") != r"^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$"
        or any(value is not True for key, value in promotion.items() if key != "tag_pattern")
    ):
        raise IdentityError("identity_release_policy_promotion_invalid")
    rules = policy.get("rules")
    if type(rules) is not list or not rules or any(type(item) is not str or not item for item in rules):
        raise IdentityError("identity_release_policy_rules_invalid")


def validate_matrix(matrix: dict[str, Any], reference: dict[str, Any]) -> None:
    required = {
        "schema", "authority_path", "reference_path", "release_policy_path", "status",
        "classification_version", "classes", "product_version_projections",
        "user_visible_name_projections", "consumer_inventory", "classified_legacy_consumers",
        "legacy_version_terms", "legacy_version_path_rules", "rename_and_rollback_map",
        "frame_control_correction",
    }
    if set(matrix) != required or matrix.get("schema") != MATRIX_SCHEMA:
        raise IdentityError("identity_matrix_shape_invalid")
    if (
        matrix.get("authority_path") != "product-identity.json"
        or matrix.get("reference_path") != "docs/governance/product-identity.json"
        or matrix.get("release_policy_path") != "docs/governance/release-policy.json"
    ):
        raise IdentityError("identity_matrix_authority_invalid")
    for key in ("status", "classification_version", "rename_and_rollback_map", "frame_control_correction"):
        if type(matrix.get(key)) is not str or not matrix[key]:
            raise IdentityError("identity_matrix_shape_invalid")
    classes = matrix.get("classes")
    expected_classes = {
        "product-user-visible", "product-version-projection", "machine-compatibility", "persisted-format",
        "dependency-name-or-version", "legacy-path", "historical-evidence", "schema-or-contract-version",
        "test-fixture",
    }
    if type(classes) is not dict or set(classes) != expected_classes or any(type(value) is not str or not value for value in classes.values()):
        raise IdentityError("identity_matrix_classes_invalid")
    for key in ("product_version_projections", "user_visible_name_projections", "consumer_inventory", "classified_legacy_consumers"):
        if type(matrix.get(key)) is not list or not matrix[key] or any(type(item) is not dict for item in matrix[key]):
            raise IdentityError("identity_matrix_consumers_invalid")
    legacy_terms = matrix.get("legacy_version_terms")
    if (
        type(legacy_terms) is not list or not legacy_terms or len(legacy_terms) != len(set(legacy_terms))
        or any(type(term) is not str or not SEMVER.fullmatch(term) for term in legacy_terms)
    ):
        raise IdentityError("identity_matrix_legacy_terms_invalid")
    legacy_rules = matrix.get("legacy_version_path_rules")
    if type(legacy_rules) is not list or not legacy_rules:
        raise IdentityError("identity_matrix_legacy_rules_invalid")
    seen_globs: set[str] = set()
    for rule in legacy_rules:
        if set(rule) != {"glob", "class", "disposition"} or rule.get("class") not in classes:
            raise IdentityError("identity_matrix_legacy_rules_invalid")
        glob = rule.get("glob")
        if (
            type(glob) is not str or not glob or glob in seen_globs or "\\" in glob
            or pathlib.PurePosixPath(glob).is_absolute() or ".." in pathlib.PurePosixPath(glob).parts
            or type(rule.get("disposition")) is not str or not rule["disposition"]
        ):
            raise IdentityError("identity_matrix_legacy_rules_invalid")
        seen_globs.add(glob)
    surface_ids: set[str] = set()
    for item in matrix["consumer_inventory"]:
        required_item = {"surface_id", "class", "owner", "paths", "current_disposition", "target", "release_blocking"}
        if set(item) != required_item or item.get("class") not in classes or item.get("owner") not in {
            "repository-steward", "core-runtime", "product-ux", "controller",
        }:
            raise IdentityError("identity_matrix_consumer_invalid")
        surface_id = item.get("surface_id")
        paths = item.get("paths")
        if (
            type(surface_id) is not str or not surface_id or surface_id in surface_ids
            or type(paths) is not list or not paths or len(paths) != len(set(paths))
            or any(type(item.get(key)) is not str or not item[key] for key in ("current_disposition", "target"))
            or type(item.get("release_blocking")) is not bool
        ):
            raise IdentityError("identity_matrix_consumer_invalid")
        surface_ids.add(surface_id)
        for path in paths:
            if type(path) is not str or not path or "\\" in path:
                raise IdentityError("identity_matrix_consumer_invalid")
            pure = pathlib.PurePosixPath(path)
            if pure.is_absolute() or ".." in pure.parts or pure.as_posix() != path:
                raise IdentityError("identity_matrix_consumer_invalid")
    workspace_consumers = [
        item for item in matrix["consumer_inventory"]
        if item["surface_id"] == "workspace-schema-and-contracts"
    ]
    schema_target = reference["current_workspace_schema"]["target"]
    if len(workspace_consumers) != 1 or workspace_consumers[0] != {
        "surface_id": "workspace-schema-and-contracts",
        "class": "schema-or-contract-version",
        "owner": "controller",
        "paths": ["internal/persistence/workspace/versioned_schema.go", "docs/governance/product-identity.json"],
        "current_disposition": f"production workspace schema target is {schema_target} with no pending schema cohort",
        "target": "advance only through reviewed migration predecessors and never infer product version",
        "release_blocking": False,
    }:
        raise IdentityError("identity_matrix_workspace_schema_mismatch")
    for item in matrix["classified_legacy_consumers"]:
        if set(item) != {"surface", "class", "disposition"} or item.get("class") not in classes or any(
            type(item.get(key)) is not str or not item[key] for key in ("surface", "class", "disposition")
        ):
            raise IdentityError("identity_matrix_legacy_consumer_invalid")


def _schema_facts(repo: pathlib.Path, reference: dict[str, Any]) -> dict[str, Any]:
    current = reference["current_workspace_schema"]
    try:
        text = _safe(repo, current["authority_path"], "identity_workspace_schema_path_invalid").read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError) as exc:
        raise IdentityError("identity_workspace_schema_read_invalid") from exc
    matches = SCHEMA_CONST.findall(text)
    if matches != [str(current["target"])]:
        raise IdentityError("identity_workspace_schema_mismatch")
    planned = reference["planned_schema_integration"]
    return {"current_workspace_schema": current["target"], "planned_migrations": planned["migration_numbers"]}


def _template(value: str, derived: dict[str, str]) -> str:
    allowed = {"{display_name}": derived["display_name"], "{version}": derived["version"], "{full_display}": derived["full_display"]}
    result = value
    for marker, replacement in allowed.items():
        result = result.replace(marker, replacement)
    if "{" in result or "}" in result:
        raise IdentityError("identity_name_projection_shape_invalid")
    return result


def _identity_literal_present(text: str, derived: dict[str, str], versions: list[str]) -> bool:
    # A complete version token must not match a component of an IPv4/CIDR value
    # or a longer dependency version when the product advances to that number.
    return (
        any(derived[key] in text for key in ("display_name", "machine_slug"))
        or any(re.search(r"(?<![0-9.])" + re.escape(version) + r"(?![0-9.])", text)
               for version in [derived["version"], *versions])
    )


def audit(
    repo: pathlib.Path,
    authority: dict[str, Any],
    reference: dict[str, Any],
    release_policy: dict[str, Any],
    matrix: dict[str, Any],
) -> dict[str, Any]:
    validate_authority(authority)
    validate_reference(reference)
    validate_release_policy(release_policy)
    validate_matrix(matrix, reference)
    if authority["version"] in matrix["legacy_version_terms"]:
        raise IdentityError("identity_authority_legacy_version")
    derived = derive_identity(authority)
    drifts: list[dict[str, str]] = []
    checked: set[tuple[str, str, str]] = set()
    for projection in matrix["product_version_projections"]:
        path, kind = projection.get("path"), projection.get("kind")
        if kind == "json-pointer" and set(projection) == {"path", "kind", "pointer"}:
            key = (path, kind, projection["pointer"])
            value = _pointer(_load(_safe(repo, path, "identity_projection_path_invalid"), "identity_projection_json_invalid"), projection["pointer"])
            aligned = value == derived["version"]
        elif kind == "text-occurrence" and set(projection) == {"path", "kind", "occurrences"}:
            count = projection.get("occurrences")
            if type(count) is not int or count <= 0:
                raise IdentityError("identity_projection_shape_invalid")
            key = (path, kind, str(count))
            try:
                text = _safe(repo, path, "identity_projection_path_invalid").read_text(encoding="utf-8")
            except (OSError, UnicodeDecodeError) as exc:
                raise IdentityError("identity_projection_read_invalid") from exc
            aligned = text.count(derived["version"]) == count and all(term not in text for term in matrix["legacy_version_terms"])
        elif kind == "go-embed-authority" and set(projection) == {"path", "kind", "authority_path"}:
            authority_path = projection.get("authority_path")
            if authority_path != matrix["authority_path"]:
                raise IdentityError("identity_projection_shape_invalid")
            key = (path, kind, authority_path)
            try:
                text = _safe(repo, path, "identity_projection_path_invalid").read_text(encoding="utf-8")
            except (OSError, UnicodeDecodeError) as exc:
                raise IdentityError("identity_projection_read_invalid") from exc
            code, directives = _go_source_without_comments(text)
            aligned = (
                directives == [f"go:embed {authority_path}"]
                and re.search(r"(?m)^\s*package\s+productidentity\s*$", code) is not None
                and '_ "embed"' in code
                and re.search(r"(?m)^\s*var\s+embeddedAuthority\s+\[\]byte\s*$", code) is not None
                and re.search(r"(?m)^\s*var\s+current\s*=\s*mustDecode\(embeddedAuthority\)\s*$", code) is not None
                and not _identity_literal_present(code, derived, [])
            )
        elif kind == "go-derived-consumer" and set(projection) == {"path", "kind", "required_markers"}:
            markers = projection.get("required_markers")
            if (
                type(markers) is not list or not markers or len(markers) != len(set(markers))
                or any(type(marker) is not str or not marker or len(marker) > 160 for marker in markers)
            ):
                raise IdentityError("identity_projection_shape_invalid")
            key = (path, kind, "\n".join(markers))
            try:
                text = _safe(repo, path, "identity_projection_path_invalid").read_text(encoding="utf-8")
            except (OSError, UnicodeDecodeError) as exc:
                raise IdentityError("identity_projection_read_invalid") from exc
            code, _ = _go_source_without_comments(text)
            aligned = all(marker in code for marker in markers) and not _identity_literal_present(
                code, derived, matrix["legacy_version_terms"],
            )
        elif kind == "text-derived-consumer" and set(projection) == {"path", "kind", "required_markers"}:
            markers = projection.get("required_markers")
            if (
                type(markers) is not list or not markers or len(markers) != len(set(markers))
                or any(type(marker) is not str or not marker or len(marker) > 200 for marker in markers)
            ):
                raise IdentityError("identity_projection_shape_invalid")
            key = (path, kind, "\n".join(markers))
            try:
                text = _safe(repo, path, "identity_projection_path_invalid").read_text(encoding="utf-8")
            except (OSError, UnicodeDecodeError) as exc:
                raise IdentityError("identity_projection_read_invalid") from exc
            aligned = all(marker in text for marker in markers) and not _identity_literal_present(
                text, derived, matrix["legacy_version_terms"],
            )
        else:
            raise IdentityError("identity_projection_shape_invalid")
        if key in checked:
            raise IdentityError("identity_projection_duplicate")
        checked.add(key)
        if not aligned:
            drifts.append({"path": path, "class": "product-version-projection", "surface": projection.get("pointer", kind)})

    for projection in matrix["user_visible_name_projections"]:
        path, kind = projection.get("path"), projection.get("kind")
        if type(path) is not str or type(kind) is not str:
            raise IdentityError("identity_name_projection_shape_invalid")
        if kind == "line-prefix" and set(projection) == {"path", "kind", "value_template"}:
            expected = _template(projection["value_template"], derived)
            text = _safe(repo, path, "identity_name_projection_path_invalid").read_text(encoding="utf-8")
            aligned = text.splitlines()[:1] == [expected]
        elif kind == "json-prefix" and set(projection) == {"path", "kind", "pointer", "value_field"}:
            expected = derived.get(projection["value_field"])
            value = _pointer(_load(_safe(repo, path, "identity_name_projection_path_invalid"), "identity_name_projection_json_invalid"), projection["pointer"])
            aligned = type(expected) is str and type(value) is str and value.startswith(expected)
        elif kind == "text-occurrence" and set(projection) == {"path", "kind", "value_field", "occurrences"}:
            expected, count = derived.get(projection["value_field"]), projection.get("occurrences")
            if type(expected) is not str or type(count) is not int or count <= 0:
                raise IdentityError("identity_name_projection_shape_invalid")
            text = _safe(repo, path, "identity_name_projection_path_invalid").read_text(encoding="utf-8")
            aligned = text.count(expected) == count
        else:
            raise IdentityError("identity_name_projection_shape_invalid")
        if not aligned:
            drifts.append({"path": path, "class": "product-user-visible", "surface": projection.get("pointer", projection.get("value_template", projection.get("value_field")))})
    for consumer in matrix["consumer_inventory"]:
        for path in consumer["paths"]:
            _safe(repo, path, "identity_consumer_path_invalid")
    legacy_terms = tuple(term.encode("utf-8") for term in matrix["legacy_version_terms"])
    for relative in _repository_paths(repo):
        path = repo.joinpath(*pathlib.PurePosixPath(relative).parts)
        try:
            metadata = path.lstat()
        except OSError as exc:
            raise IdentityError("identity_audit_read_invalid") from exc
        if any(relative.startswith(prefix) for prefix in RETIRED_VERSIONED_RUNTIME_PREFIXES):
            drifts.append({
                "path": relative,
                "class": "product-version-path-drift",
                "surface": "retired-versioned-runtime-namespace",
            })
        elif (
            stat.S_ISREG(metadata.st_mode)
            and _contains_term(path, legacy_terms)
            and not any(fnmatch.fnmatchcase(relative, rule["glob"]) for rule in matrix["legacy_version_path_rules"])
        ):
            drifts.append({"path": relative, "class": "unclassified-product-version", "surface": "legacy-version"})
    drifts = sorted(drifts, key=lambda item: (item["path"], item["class"], item["surface"]))
    return {
        "schema": "synon.governance.product-identity-audit.v4",
        "authority": derived,
        "aligned": not drifts,
        "drift_count": len(drifts),
        "drifts": drifts,
        "classification_version": matrix["classification_version"],
        "schema_facts": _schema_facts(repo, reference),
        "release_policy_schema": release_policy["schema"],
        "external_release_authorization_required": True,
    }


def evaluate(
    repo: pathlib.Path,
    authority: dict[str, Any],
    reference: dict[str, Any],
    release_policy: dict[str, Any],
    matrix: dict[str, Any],
    mode: str,
) -> tuple[dict[str, Any], int]:
    if mode not in {"candidate", "release"}:
        raise IdentityError("identity_gate_mode_invalid")
    result = audit(repo, authority, reference, release_policy, matrix)
    if mode == "candidate":
        blocked = not result["aligned"]
        result.update({
            "mode": mode,
            "gate_status": "candidate-incomplete" if blocked else "candidate-aligned",
            "candidate_eligible": not blocked,
            "release_ready": False,
            "blockers": ["identity_product_projection_drift"] if blocked else [],
        })
        return result, 3 if blocked else 0
    blockers = []
    if not result["aligned"]:
        blockers.append("identity_product_projection_drift")
    blockers.append("identity_external_release_authorization_required")
    result.update({
        "mode": mode,
        "gate_status": (
            "release-policy-valid-external-authorization-required"
            if result["aligned"]
            else "release-blocked"
        ),
        "candidate_eligible": result["aligned"],
        "release_ready": False,
        "blockers": blockers,
    })
    return result, 3


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", required=True)
    parser.add_argument("--identity", default="product-identity.json")
    parser.add_argument("--reference", default="docs/governance/product-identity.json")
    parser.add_argument("--release-policy", default="docs/governance/release-policy.json")
    parser.add_argument("--matrix", default="docs/governance/product-identity-consumer-matrix.json")
    parser.add_argument("--mode", choices=("candidate", "release"), required=True)
    args = parser.parse_args(argv)
    try:
        repo = pathlib.Path(args.repo)
        authority, reference, release_policy, matrix = load_control_plane(
            repo, identity_path=args.identity, reference_path=args.reference,
            release_policy_path=args.release_policy, matrix_path=args.matrix,
        )
        result, code = evaluate(repo, authority, reference, release_policy, matrix, args.mode)
    except IdentityError as exc:
        print(json.dumps({"ok": False, "code": str(exc)}, sort_keys=True))
        return 2
    print(json.dumps({"ok": code == 0, **result}, sort_keys=True))
    return code


if __name__ == "__main__":
    sys.exit(main())

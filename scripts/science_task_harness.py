#!/usr/bin/env python3
"""Audit one real Synon Biomed scientific task without trusting model prose.

The harness is deliberately read-only.  It combines canonical SQLite facts
with a separately captured Codex built-in-browser evidence document.  A task
does not pass merely because an assistant message says that it passed.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sqlite3
import sys
import time
from collections import Counter
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


SCHEMA = "synon.science-task-audit.v1"
SUITE_SCHEMA = "synon.science-task-suite.v1"
BROWSER_SCHEMA = "synon.codex-browser-evidence.v1"
TERMINAL_FRAME_STATUSES = {"completed", "failed", "cancelled", "canceled"}
TERMINAL_ATTEMPT_STATUSES = {"completed", "failed", "cancelled", "canceled", "reclaimed"}
FORBIDDEN_DEFAULT_USER_ARTIFACT_SUFFIXES = (".json", ".jsonl")


class HarnessError(RuntimeError):
    pass


def parse_time(value: str | None) -> datetime | None:
    if not value:
        return None
    normalized = value.strip().replace("Z", "+00:00")
    go_match = re.fullmatch(
        r"(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})(?:\.(\d+))? ([+-]\d{4}) UTC",
        normalized,
    )
    try:
        if go_match:
            fractional = (go_match.group(2) or "")[:6].ljust(6, "0")
            parsed = datetime.strptime(
                f"{go_match.group(1)}.{fractional} {go_match.group(3)}",
                "%Y-%m-%d %H:%M:%S.%f %z",
            )
        else:
            parsed = datetime.fromisoformat(normalized)
    except ValueError as error:
        raise HarnessError("database timestamp format is unsupported") from error
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=timezone.utc)
    return parsed.astimezone(timezone.utc)


def canonical_json(value: Any) -> str:
    return json.dumps(value, ensure_ascii=True, separators=(",", ":"), sort_keys=True)


def load_json(path: Path, label: str) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise HarnessError(f"{label} is not readable JSON") from error
    if not isinstance(value, dict):
        raise HarnessError(f"{label} must be a JSON object")
    return value


def load_task(suite_path: Path, task_id: str) -> dict[str, Any]:
    suite = load_json(suite_path, "task suite")
    if suite.get("schema") != SUITE_SCHEMA:
        raise HarnessError("task suite schema is unsupported")
    tasks = suite.get("tasks")
    if not isinstance(tasks, list):
        raise HarnessError("task suite tasks must be an array")
    matches = [task for task in tasks if isinstance(task, dict) and task.get("id") == task_id]
    if len(matches) != 1:
        raise HarnessError("task id must select exactly one task")
    task = dict(matches[0])
    browser_authority = suite.get("browserAuthority", "codex_builtin")
    if browser_authority not in {
        "codex_builtin",
        "google_chrome",
        "playwright_chromium",
        "google_chrome_playwright",
    }:
        raise HarnessError("task suite browserAuthority is unsupported")
    task["_browserAuthority"] = browser_authority
    return task


def load_review_rubric(task: dict[str, Any]) -> list[dict[str, Any]]:
    raw_rubric = task.get("reviewRubric", [])
    if not isinstance(raw_rubric, list):
        raise HarnessError("reviewRubric must be an array")
    rubric: list[dict[str, Any]] = []
    seen: set[str] = set()
    for item in raw_rubric:
        if not isinstance(item, dict):
            raise HarnessError("reviewRubric entries must be objects")
        rubric_id = item.get("id")
        criterion = item.get("criterion")
        evidence_sources = item.get("evidenceSources")
        if not isinstance(rubric_id, str) or not rubric_id or rubric_id in seen:
            raise HarnessError("reviewRubric ids must be unique non-empty strings")
        if not isinstance(criterion, str) or not criterion:
            raise HarnessError("reviewRubric criterion must be a non-empty string")
        if not isinstance(evidence_sources, list) or not evidence_sources or not all(
            isinstance(source, str) and source for source in evidence_sources
        ):
            raise HarnessError("reviewRubric evidenceSources must contain non-empty strings")
        seen.add(rubric_id)
        rubric.append(
            {
                "id": rubric_id,
                "criterion": criterion,
                "evidenceSources": list(evidence_sources),
            }
        )
    return rubric


def load_required_software_runtime_executions(
    acceptance: dict[str, Any],
) -> list[dict[str, Any]]:
    raw_requirements = acceptance.get("requiredSoftwareRuntimeExecutions", [])
    if not isinstance(raw_requirements, list):
        raise HarnessError("requiredSoftwareRuntimeExecutions must be an array")
    requirements: list[dict[str, Any]] = []
    seen: set[tuple[str, str]] = set()
    identifier = re.compile(r"[a-z0-9][a-z0-9._-]{0,127}")
    for item in raw_requirements:
        if not isinstance(item, dict):
            raise HarnessError("requiredSoftwareRuntimeExecutions entries must be objects")
        capability = item.get("capability")
        provider = item.get("provider", "")
        if (
            not isinstance(capability, str)
            or not identifier.fullmatch(capability)
            or not isinstance(provider, str)
            or (provider and not identifier.fullmatch(provider))
        ):
            raise HarnessError(
                "required software runtime capability and provider must be bounded identifiers"
            )
        key = (capability, provider)
        if key in seen:
            raise HarnessError(
                "required software runtime capability/provider pairs must be unique"
            )
        minimum = item.get("minCount", 1)
        minimum_outputs = item.get("minOutputCount", 1)
        if (
            isinstance(minimum, bool)
            or not isinstance(minimum, int)
            or minimum < 1
            or isinstance(minimum_outputs, bool)
            or not isinstance(minimum_outputs, int)
            or minimum_outputs < 1
        ):
            raise HarnessError(
                "required software runtime counts must be positive integers"
            )
        output_patterns = item.get("requiredOutputPathPatterns", [])
        if not isinstance(output_patterns, list) or not all(
            isinstance(value, str) and value for value in output_patterns
        ):
            raise HarnessError(
                "requiredOutputPathPatterns must contain non-empty regular expressions"
            )
        for pattern in output_patterns:
            try:
                re.compile(pattern)
            except re.error as error:
                raise HarnessError(
                    "requiredOutputPathPatterns contains an invalid regular expression"
                ) from error
        require_managed_packages = item.get("requireManagedPackages", True)
        if not isinstance(require_managed_packages, bool):
            raise HarnessError("requireManagedPackages must be a boolean")
        seen.add(key)
        requirements.append(
            {
                "capability": capability,
                "provider": provider,
                "minCount": minimum,
                "minOutputCount": minimum_outputs,
                "requiredOutputPathPatterns": list(dict.fromkeys(output_patterns)),
                "requireManagedPackages": require_managed_packages,
            }
        )
    return requirements


def is_sha256(value: Any) -> bool:
    return isinstance(value, str) and re.fullmatch(r"[0-9a-f]{64}", value) is not None


def decode_json_object(value: Any) -> dict[str, Any] | None:
    if isinstance(value, bytes):
        try:
            value = value.decode("utf-8")
        except UnicodeDecodeError:
            return None
    if not isinstance(value, str):
        return None
    try:
        decoded = json.loads(value)
    except json.JSONDecodeError:
        return None
    return decoded if isinstance(decoded, dict) else None


def encoded_sha256(value: Any) -> str:
    if isinstance(value, str):
        raw = value.encode("utf-8")
    elif isinstance(value, bytes):
        raw = value
    else:
        return ""
    return hashlib.sha256(raw).hexdigest()


def software_runtime_execution(row: sqlite3.Row) -> dict[str, Any]:
    request = decode_json_object(row["input_json"])
    result = decode_json_object(row["result_json"])
    validation_failures: list[str] = []
    input_digest = str(row["input_sha256"] or "")
    result_digest = str(row["result_sha256"] or "")
    if not is_sha256(input_digest) or encoded_sha256(row["input_json"]) != input_digest:
        validation_failures.append("input_digest")
    if not is_sha256(result_digest) or encoded_sha256(row["result_json"]) != result_digest:
        validation_failures.append("result_digest")
    if request is None:
        validation_failures.append("request_json")
        request = {}
    if result is None:
        validation_failures.append("result_json")
        result = {}

    capability = request.get("capability")
    provider = result.get("provider_id")
    request_provider = request.get("provider", "")
    packages = request.get("packages")
    expected_outputs = request.get("expected_outputs")
    outputs = result.get("outputs")
    cleanup = result.get("cleanup")
    managed_packages = (
        isinstance(packages, list)
        and bool(packages)
        and all(
            isinstance(package, dict)
            and isinstance(package.get("manager"), str)
            and bool(package["manager"])
            and isinstance(package.get("spec"), str)
            and bool(package["spec"])
            for package in packages
        )
    )
    if not managed_packages:
        validation_failures.append("managed_packages")
    if not isinstance(capability, str) or not capability:
        validation_failures.append("capability")
    if (
        not isinstance(provider, str)
        or not provider
        or (isinstance(request_provider, str) and request_provider and request_provider != provider)
    ):
        validation_failures.append("provider")
    if result.get("request_digest") != input_digest:
        validation_failures.append("request_digest")
    for field in ("runtime_generation", "stdout_sha256", "stderr_sha256"):
        if not is_sha256(result.get(field)):
            validation_failures.append(field)
    if not isinstance(expected_outputs, list) or not expected_outputs:
        validation_failures.append("expected_outputs")
        expected_outputs = []
    if not isinstance(outputs, list):
        validation_failures.append("output_receipts")
        outputs = []

    output_by_path: dict[str, dict[str, Any]] = {}
    for output in outputs:
        if not isinstance(output, dict):
            validation_failures.append("output_receipts")
            continue
        path = output.get("path")
        size = output.get("bytes")
        if (
            not isinstance(path, str)
            or not path
            or path in output_by_path
            or isinstance(size, bool)
            or not isinstance(size, int)
            or size < 0
            or not is_sha256(output.get("sha256"))
        ):
            validation_failures.append("output_receipts")
            continue
        output_by_path[path] = output
    expected_paths: set[str] = set()
    for expected in expected_outputs:
        if not isinstance(expected, dict):
            validation_failures.append("expected_outputs")
            continue
        path = expected.get("path")
        minimum = expected.get("min_bytes", 0)
        receipt = output_by_path.get(path) if isinstance(path, str) else None
        if (
            not isinstance(path, str)
            or not path
            or path in expected_paths
            or isinstance(minimum, bool)
            or not isinstance(minimum, int)
            or minimum < 0
            or receipt is None
            or int(receipt["bytes"]) < minimum
        ):
            validation_failures.append("expected_outputs")
            continue
        expected_paths.add(path)
    if set(output_by_path) != expected_paths:
        validation_failures.append("output_receipts")

    cleanup_verified = (
        isinstance(cleanup, dict)
        and cleanup.get("process_group_terminated") is True
        and cleanup.get("process_tree_terminated") is True
        and cleanup.get("temporary_streams_closed") is True
    )
    if row["started_at"] is not None and not cleanup_verified:
        validation_failures.append("cleanup")
    completed = (
        str(row["state"]) == "completed"
        and result.get("ok") is True
        and result.get("status") == "completed"
        and result.get("exit_status") == "ok"
        and result.get("exit_code") == 0
        and result.get("timed_out") is False
        and str(row["approval_decision"] or "") == "allow"
    )
    if str(row["state"]) == "completed" and not completed:
        validation_failures.append("terminal_status")
    return {
        "operationId": str(row["operation_id"]),
        "state": str(row["state"]),
        "started": row["started_at"] is not None,
        "capability": capability if isinstance(capability, str) else "",
        "provider": provider if isinstance(provider, str) else "",
        "environment": result.get("environment", ""),
        "executable": result.get("executable", request.get("executable", "")),
        "managedPackages": managed_packages,
        "outputCount": len(output_by_path),
        "outputs": [output_by_path[path] for path in sorted(output_by_path)],
        "cleanupVerified": cleanup_verified,
        "approval": {
            "decision": str(row["approval_decision"] or ""),
            "source": str(row["approval_source"] or ""),
            "actorId": str(row["approval_actor_id"] or ""),
        },
        "terminalAt": "" if row["terminal_at"] is None else str(row["terminal_at"]),
        "verified": completed and not validation_failures,
        "validationFailures": sorted(set(validation_failures)),
    }


def open_read_only(database: Path) -> sqlite3.Connection:
    resolved = database.expanduser().resolve(strict=True)
    connection = sqlite3.connect(f"file:{resolved}?mode=ro", uri=True)
    connection.row_factory = sqlite3.Row
    connection.execute("PRAGMA query_only=ON")
    return connection


def table_exists(connection: sqlite3.Connection, name: str) -> bool:
    return connection.execute(
        "SELECT 1 FROM sqlite_master WHERE type='table' AND name=?", (name,)
    ).fetchone() is not None


def stream_blob_sha256(connection: sqlite3.Connection, rowid: int) -> str:
    digest = hashlib.sha256()
    with connection.blobopen("artifact_versions", "content", rowid, readonly=True) as blob:
        while True:
            chunk = blob.read(1024 * 1024)
            if not chunk:
                break
            digest.update(chunk)
    return digest.hexdigest()


def resolve_storage_path(raw: str, database: Path, artifact_root: Path | None) -> Path:
    candidate = Path(raw)
    if candidate.is_absolute():
        return candidate
    roots = []
    if artifact_root is not None:
        roots.append(artifact_root)
    resolved_database = database.resolve()
    roots.extend(
        (
            resolved_database.with_name(f"{resolved_database.name}.blobs"),
            resolved_database.parent,
        )
    )
    paths = list(dict.fromkeys((root.resolve() / candidate for root in roots)))
    existing = [path for path in paths if path.is_file()]
    if len(existing) == 1:
        return existing[0]
    if len(existing) > 1:
        raise HarnessError("artifact storage path is ambiguous")
    return paths[0]


def file_sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        while True:
            chunk = handle.read(1024 * 1024)
            if not chunk:
                break
            digest.update(chunk)
    return digest.hexdigest()


def read_browser_evidence(
    path: Path | None,
    task_id: str,
    conversation_id: str,
    browser_authority: str,
    required_checks: list[str],
    review_rubric: list[dict[str, Any]],
) -> tuple[dict[str, Any], list[str]]:
    failures: list[str] = []
    if path is None:
        return {"status": "missing", "checks": []}, [
            "codex_browser_evidence_missing",
            *[
                f"quality_review_check_failed:{criterion['id']}"
                for criterion in review_rubric
            ],
        ]
    document = load_json(path, "browser evidence")
    if document.get("schema") != BROWSER_SCHEMA:
        failures.append("codex_browser_evidence_schema_invalid")
    if document.get("browser") != browser_authority:
        failures.append("codex_browser_authority_invalid")
    if document.get("taskId") != task_id or document.get("conversationId") != conversation_id:
        failures.append("codex_browser_identity_mismatch")
    raw_checks = document.get("checks")
    checks = raw_checks if isinstance(raw_checks, list) else []
    by_id = {
        str(item.get("id")): str(item.get("status"))
        for item in checks
        if isinstance(item, dict) and isinstance(item.get("id"), str)
    }
    for check_id in required_checks:
        if by_id.get(check_id) != "pass":
            failures.append(f"codex_browser_check_failed:{check_id}")
    evidence_by_id = {
        str(item.get("id")): str(item.get("evidence", "")).strip()
        for item in checks
        if isinstance(item, dict) and isinstance(item.get("id"), str)
    }
    for criterion in review_rubric:
        rubric_id = str(criterion["id"])
        if by_id.get(rubric_id) != "pass":
            failures.append(f"quality_review_check_failed:{rubric_id}")
        elif not evidence_by_id.get(rubric_id):
            failures.append(f"quality_review_evidence_missing:{rubric_id}")
    screenshot_sha256 = document.get("screenshotSha256")
    if not isinstance(screenshot_sha256, str) or not re.fullmatch(r"[0-9a-f]{64}", screenshot_sha256):
        failures.append("codex_browser_screenshot_digest_missing")
    return {
        "status": "pass" if not failures else "fail",
        "capturedAt": str(document.get("capturedAt", "")),
        "url": str(document.get("url", "")),
        "screenshotSha256": screenshot_sha256 if isinstance(screenshot_sha256, str) else "",
        "checks": [
            {
                "id": key,
                "status": by_id[key],
                "evidence": evidence_by_id.get(key, ""),
            }
            for key in sorted(by_id)
        ],
    }, failures


def initial_user_prompt_sha256(connection: sqlite3.Connection, stream_uid: str) -> str:
    row = connection.execute(
        "SELECT payload_json FROM transcript_events WHERE stream_uid=? AND event_type='user_message' "
        "ORDER BY event_id LIMIT 1",
        (stream_uid,),
    ).fetchone()
    if row is None:
        return ""
    raw = row["payload_json"]
    try:
        payload = json.loads(raw)
    except (TypeError, UnicodeDecodeError, json.JSONDecodeError):
        return ""
    if not isinstance(payload, dict):
        return ""
    text = payload.get("text")
    if not isinstance(text, str):
        input_data = payload.get("inputData")
        if isinstance(input_data, dict) and isinstance(input_data.get("request"), str):
            text = input_data["request"]
    if not isinstance(text, str):
        content = payload.get("content")
        if isinstance(content, list) and content and isinstance(content[0], dict):
            candidate = content[0].get("text")
            text = candidate if isinstance(candidate, str) else None
    if not isinstance(text, str) or not text:
        return ""
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def audit(
    database: Path,
    suite_path: Path,
    task_id: str,
    conversation_id: str,
    browser_evidence_path: Path | None,
    artifact_root: Path | None,
    now: datetime,
) -> dict[str, Any]:
    task = load_task(suite_path, task_id)
    review_rubric = load_review_rubric(task)
    acceptance = task.get("acceptance")
    if not isinstance(acceptance, dict):
        raise HarnessError("task acceptance must be an object")
    required_browser_checks = acceptance.get("requiredBrowserChecks", [])
    if not isinstance(required_browser_checks, list) or not all(
        isinstance(item, str) and item for item in required_browser_checks
    ):
        raise HarnessError("requiredBrowserChecks must contain non-empty strings")
    required_software_runtime_executions = load_required_software_runtime_executions(
        acceptance
    )

    failures: list[str] = []
    connection = open_read_only(database)
    try:
        frame = connection.execute(
            "SELECT id,project_id,status,created_at,updated_at FROM frames WHERE id=?",
            (conversation_id,),
        ).fetchone()
        if frame is None:
            raise HarnessError("conversation frame was not found")
        streams = connection.execute(
            "SELECT stream_uid,input_revision,consumed_input_revision,next_event_id,"
            "next_publication_seq,next_checkpoint_sequence FROM transcript_streams WHERE frame_id=? "
            "ORDER BY epoch DESC",
            (conversation_id,),
        ).fetchall()
        if len(streams) != 1:
            failures.append("canonical_transcript_stream_count_invalid")
        stream = streams[0] if streams else None
        stream_uid = str(stream["stream_uid"]) if stream is not None else ""
        prompt_sha256 = initial_user_prompt_sha256(connection, stream_uid) if stream_uid else ""
        expected_prompt_sha256 = task.get("initialPromptSha256")
        if expected_prompt_sha256 is not None:
            if not isinstance(expected_prompt_sha256, str) or not re.fullmatch(
                r"[0-9a-f]{64}", expected_prompt_sha256
            ):
                raise HarnessError("initialPromptSha256 must be a lowercase SHA-256")
            if prompt_sha256 != expected_prompt_sha256:
                failures.append("initial_task_prompt_mismatch")

        attempts: list[dict[str, Any]] = []
        stale_running: list[int] = []
        if stream_uid:
            rows = connection.execute(
                "SELECT attempt,status,phase,claimed_at,expires_at,finished_at,finished_event_id "
                "FROM transcript_runner_attempts WHERE stream_uid=? ORDER BY attempt",
                (stream_uid,),
            ).fetchall()
            for row in rows:
                expires = parse_time(row["expires_at"])
                stale = row["status"] == "running" and expires is not None and expires <= now
                if stale:
                    stale_running.append(int(row["attempt"]))
                attempts.append(
                    {
                        "attempt": int(row["attempt"]),
                        "status": str(row["status"]),
                        "phase": str(row["phase"]),
                        "claimedAt": str(row["claimed_at"]),
                        "expiresAt": str(row["expires_at"]),
                        "finishedAt": "" if row["finished_at"] is None else str(row["finished_at"]),
                        "finishedEventId": row["finished_event_id"],
                        "staleRunning": stale,
                    }
                )
        if stale_running:
            failures.append("stale_running_attempt")
        latest_attempt = attempts[-1] if attempts else None
        if latest_attempt is None:
            failures.append("runner_attempt_missing")
        elif frame["status"] in TERMINAL_FRAME_STATUSES and latest_attempt["status"] not in TERMINAL_ATTEMPT_STATUSES:
            failures.append("terminal_frame_has_nonterminal_latest_attempt")

        allowed_statuses = acceptance.get("terminalFrameStatuses", ["completed"])
        if not isinstance(allowed_statuses, list) or frame["status"] not in allowed_statuses:
            failures.append(f"frame_status_not_accepted:{frame['status']}")

        event_counts: dict[str, int] = {}
        if stream_uid:
            event_counts = {
                str(row["event_type"]): int(row["count"])
                for row in connection.execute(
                    "SELECT event_type,COUNT(*) AS count FROM transcript_events "
                    "WHERE stream_uid=? GROUP BY event_type ORDER BY event_type",
                    (stream_uid,),
                )
            }
        if event_counts.get("runner_finished", 0) == 0:
            failures.append("durable_runner_terminal_event_missing")
        user_submission_count = event_counts.get("user_message", 0) - event_counts.get(
            "user_input_response", 0
        )
        if (
            acceptance.get("requireSingleUserSubmission") is True
            and user_submission_count != 1
        ):
            failures.append("single_user_submission_required")

        tool_state_counts: Counter[str] = Counter()
        tool_name_counts: Counter[str] = Counter()
        open_tool_items = 0
        if not table_exists(connection, "transcript_tool_call_items"):
            failures.append("typed_tool_batch_authority_missing")
        elif stream_uid:
            for row in connection.execute(
                "SELECT tool_name,state FROM transcript_tool_call_items WHERE stream_uid=?",
                (stream_uid,),
            ):
                tool_name_counts[str(row["tool_name"])] += 1
                tool_state_counts[str(row["state"])] += 1
                if str(row["state"]) not in {
                    "completed",
                    "failed",
                    "blocked",
                    "cancelled",
                    "outcome_unknown",
                }:
                    open_tool_items += 1
            if open_tool_items:
                failures.append("open_tool_call_items")
        minimum_tool_calls = int(acceptance.get("minimumToolCalls", 1))
        if sum(tool_name_counts.values()) < minimum_tool_calls:
            failures.append("minimum_typed_tool_calls_not_met")

        software_runtime_executions: list[dict[str, Any]] = []
        if required_software_runtime_executions:
            if not table_exists(connection, "kernel_local_operations"):
                failures.append("unified_software_runtime_authority_missing")
            elif stream_uid:
                rows = connection.execute(
                    "SELECT operation_id,state,input_json,input_sha256,result_json,result_sha256,"
                    "approval_decision,approval_source,approval_actor_id,started_at,terminal_at "
                    "FROM kernel_local_operations WHERE stream_uid=? AND project_id=? AND frame_id=? "
                    "AND tool='software_runtime' ORDER BY created_at,operation_id",
                    (stream_uid, str(frame["project_id"]), str(frame["id"])),
                ).fetchall()
                software_runtime_executions = [
                    software_runtime_execution(row) for row in rows
                ]
                for execution in software_runtime_executions:
                    if execution["state"] == "completed" and not execution["verified"]:
                        failures.append(
                            "software_runtime_receipt_invalid:"
                            + execution["operationId"]
                        )
                    if (
                        execution["started"]
                        and execution["state"]
                        in {"completed", "failed", "cancelled", "outcome_unknown"}
                        and not execution["cleanupVerified"]
                    ):
                        failures.append(
                            "software_runtime_cleanup_unverified:"
                            + execution["operationId"]
                        )
                providers = {
                    execution["provider"]
                    for execution in software_runtime_executions
                    if execution["provider"]
                }
                if (
                    acceptance.get("requireSingleSoftwareRuntimeProvider") is True
                    and len(providers) != 1
                ):
                    failures.append("single_software_runtime_provider_required")
                for requirement in required_software_runtime_executions:
                    patterns = [
                        re.compile(pattern)
                        for pattern in requirement["requiredOutputPathPatterns"]
                    ]
                    matches = [
                        execution
                        for execution in software_runtime_executions
                        if execution["verified"]
                        and execution["capability"] == requirement["capability"]
                        and (
                            not requirement["provider"]
                            or execution["provider"] == requirement["provider"]
                        )
                        and (
                            not requirement["requireManagedPackages"]
                            or execution["managedPackages"]
                        )
                        and execution["outputCount"] >= requirement["minOutputCount"]
                        and all(
                            any(
                                pattern.search(str(output["path"]))
                                for output in execution["outputs"]
                            )
                            for pattern in patterns
                        )
                    ]
                    if len(matches) < requirement["minCount"]:
                        failures.append(
                            "required_software_runtime_execution_missing:"
                            + requirement["capability"]
                        )

        artifacts: list[dict[str, Any]] = []
        artifact_name_counts: Counter[str] = Counter()
        duplicate_current_names: list[str] = []
        if stream_uid:
            artifact_rows = connection.execute(
                "SELECT DISTINCT artifact_commit.artifact_id,artifact_commit.version_id,a.name,a.project_id,"
                "a.current_version_number,v.version_number,v.rowid AS version_rowid,"
                "length(v.content) AS content_bytes,v.storage_path,v.content_sha256,v.size_bytes "
                "FROM transcript_artifact_commits artifact_commit "
                "JOIN artifacts a ON a.id=artifact_commit.artifact_id "
                "JOIN artifact_versions v ON v.id=artifact_commit.version_id "
                "AND v.artifact_id=artifact_commit.artifact_id "
                "WHERE artifact_commit.stream_uid=? "
                "ORDER BY a.name,artifact_commit.artifact_id,artifact_commit.version_id",
                (stream_uid,),
            ).fetchall()
            for row in artifact_rows:
                current = int(row["current_version_number"]) == int(row["version_number"])
                digest_status = "not_current"
                if current:
                    expected = str(row["content_sha256"] or "")
                    actual = ""
                    try:
                        if int(row["content_bytes"] or 0) > 0:
                            actual = stream_blob_sha256(connection, int(row["version_rowid"]))
                        elif row["storage_path"]:
                            actual = file_sha256(
                                resolve_storage_path(
                                    str(row["storage_path"]), database, artifact_root
                                ).resolve(strict=True)
                            )
                        digest_status = "pass" if expected and actual == expected else "fail"
                    except (OSError, sqlite3.Error):
                        digest_status = "unreadable"
                    if str(row["project_id"]) != str(frame["project_id"]):
                        failures.append("cross_project_artifact_commit")
                    if digest_status != "pass":
                        failures.append("current_artifact_digest_invalid")
                    if str(row["name"]).lower().endswith(FORBIDDEN_DEFAULT_USER_ARTIFACT_SUFFIXES):
                        failures.append("default_json_scientific_artifact_forbidden")
                    artifact_name_counts[str(row["name"])] += 1
                artifacts.append(
                    {
                        "artifactId": str(row["artifact_id"]),
                        "versionId": str(row["version_id"]),
                        "name": str(row["name"]),
                        "sizeBytes": int(row["size_bytes"] or 0),
                        "current": current,
                        "digest": digest_status,
                    }
                )

        duplicate_current_names = sorted(
            name for name, count in artifact_name_counts.items() if count > 1
        )
        if duplicate_current_names:
            failures.append("duplicate_current_artifact_names")

        required_artifacts = acceptance.get("requiredArtifacts", [])
        if not isinstance(required_artifacts, list):
            raise HarnessError("requiredArtifacts must be an array")
        current_names = list(artifact_name_counts.elements())
        for rule in required_artifacts:
            if not isinstance(rule, dict) or not isinstance(rule.get("nameRegex"), str):
                raise HarnessError("each artifact rule must contain nameRegex")
            pattern = re.compile(str(rule["nameRegex"]))
            minimum = int(rule.get("minCount", 1))
            if sum(1 for name in current_names if pattern.search(name)) < minimum:
                failures.append(f"required_artifact_missing:{rule['nameRegex']}")

        browser, browser_failures = read_browser_evidence(
            browser_evidence_path,
            task_id,
            conversation_id,
            str(task["_browserAuthority"]),
            [str(item) for item in required_browser_checks],
            review_rubric,
        )
        failures.extend(browser_failures)
        failures = sorted(set(failures))
        return {
            "schema": SCHEMA,
            "status": "pass" if not failures else "fail",
            "taskId": task_id,
            "conversationId": conversation_id,
            "capturedAt": now.isoformat().replace("+00:00", "Z"),
            "frame": {
                "status": str(frame["status"]),
                "createdAt": str(frame["created_at"]),
                "updatedAt": str(frame["updated_at"]),
            },
            "transcript": {
                "streamCount": len(streams),
                "streamUid": stream_uid,
                "inputRevision": None if stream is None else int(stream["input_revision"]),
                "consumedInputRevision": None
                if stream is None
                else int(stream["consumed_input_revision"]),
                "eventCounts": event_counts,
                "initialPromptSha256": prompt_sha256,
            },
            "runner": {
                "attemptCount": len(attempts),
                "latest": latest_attempt,
                "staleRunningAttempts": stale_running,
                "attempts": attempts,
            },
            "tools": {
                "count": sum(tool_name_counts.values()),
                "byName": dict(sorted(tool_name_counts.items())),
                "byState": dict(sorted(tool_state_counts.items())),
                "openCount": open_tool_items,
            },
            "artifacts": {
                "committedVersionCount": len(artifacts),
                "currentCommittedCount": sum(1 for item in artifacts if item["current"]),
                "duplicateCurrentNames": duplicate_current_names,
                "items": artifacts,
            },
            "softwareRuntimeExecutions": {
                "count": len(software_runtime_executions),
                "providers": sorted(
                    {
                        execution["provider"]
                        for execution in software_runtime_executions
                        if execution["provider"]
                    }
                ),
                "items": software_runtime_executions,
            },
            "browser": browser,
            "qualityReview": {
                "status": "pass"
                if not any(
                    failure.startswith("quality_review_") for failure in failures
                )
                else "fail",
                "criteria": review_rubric,
            },
            "failures": failures,
            "evidenceDigest": hashlib.sha256(
                canonical_json(
                    {
                        "frame": str(frame["status"]),
                        "stream": stream_uid,
                        "attempts": attempts,
                        "events": event_counts,
                        "tools": dict(tool_state_counts),
                        "artifacts": artifacts,
                        "softwareRuntimeExecutions": software_runtime_executions,
                        "browser": browser,
                        "failures": failures,
                    }
                ).encode("utf-8")
            ).hexdigest(),
        }
    finally:
        connection.close()


def write_report(report: dict[str, Any], output: Path | None) -> None:
    rendered = json.dumps(report, ensure_ascii=True, indent=2, sort_keys=True) + "\n"
    if output is None:
        sys.stdout.write(rendered)
        return
    destination = output.expanduser().resolve()
    destination.parent.mkdir(parents=True, exist_ok=True)
    temporary = destination.with_name(f".{destination.name}.tmp")
    temporary.write_text(rendered, encoding="utf-8")
    temporary.chmod(0o600)
    temporary.replace(destination)


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--database", required=True, type=Path)
    parser.add_argument("--suite", required=True, type=Path)
    parser.add_argument("--task-id", required=True)
    parser.add_argument("--conversation-id", required=True)
    parser.add_argument("--browser-evidence", type=Path)
    parser.add_argument("--artifact-root", type=Path)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--wait", action="store_true", help="wait without a total-duration ceiling")
    parser.add_argument("--poll-seconds", type=float, default=5.0)
    parser.add_argument("--now", help="test-only ISO-8601 clock override")
    args = parser.parse_args(argv)
    if not 0.25 <= args.poll_seconds <= 300:
        parser.error("--poll-seconds must be between 0.25 and 300")
    return args


def main(argv: list[str] | None = None) -> int:
    args = parse_args(sys.argv[1:] if argv is None else argv)
    try:
        fixed_now = parse_time(args.now) if args.now else None
        while True:
            report = audit(
                args.database,
                args.suite,
                args.task_id,
                args.conversation_id,
                args.browser_evidence,
                args.artifact_root,
                fixed_now or datetime.now(timezone.utc),
            )
            if not args.wait:
                break
            frame_status = str(report["frame"]["status"])
            stale = bool(report["runner"]["staleRunningAttempts"])
            if frame_status in TERMINAL_FRAME_STATUSES or stale:
                break
            time.sleep(args.poll_seconds)
        write_report(report, args.output)
        return 0 if report["status"] == "pass" else 1
    except KeyboardInterrupt:
        return 130
    except HarnessError as error:
        sys.stderr.write(f"science task harness: {error}\n")
        return 2


if __name__ == "__main__":
    raise SystemExit(main())

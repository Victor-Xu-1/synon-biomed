#!/usr/bin/env python3
"""Run the credential-gated P9 model and messaging acceptance matrix safely."""

from __future__ import annotations

import argparse
import json
import os
import stat
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import Any


AUTHORIZATION = "I_ACCEPT_NETWORK_COST_AND_MESSAGES"
EXPECTED_MODEL_TARGETS = {"runner", "compact"}
EXPECTED_PLATFORMS = {"wechat", "feishu"}
MAX_OUTPUT_BYTES = 1024 * 1024


class GateError(RuntimeError):
    pass


def executable_path(raw: str, label: str) -> Path:
    path = Path(raw).expanduser().resolve(strict=True)
    if not path.is_file() or not os.access(path, os.X_OK):
        raise GateError(f"{label} is not an executable file")
    return path


def run_json(command: list[str], timeout: int, environment: dict[str, str]) -> tuple[int, dict[str, Any]]:
    try:
        result = subprocess.run(
            command,
            check=False,
            capture_output=True,
            text=False,
            timeout=timeout,
            env=environment,
        )
    except subprocess.TimeoutExpired as error:
        raise GateError("acceptance subprocess timed out") from error
    if len(result.stdout) > MAX_OUTPUT_BYTES or len(result.stderr) > MAX_OUTPUT_BYTES:
        raise GateError("acceptance subprocess output exceeded the 1 MiB limit")
    try:
        document = json.loads(result.stdout.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise GateError("acceptance subprocess did not return valid JSON") from error
    if not isinstance(document, dict) or document.get("secretsRedacted") is not True:
        raise GateError("acceptance subprocess did not attest secret redaction")
    return result.returncode, document


def model_summary(document: dict[str, Any]) -> dict[str, Any]:
    targets = []
    for raw in document.get("targets", []):
        if not isinstance(raw, dict):
            continue
        targets.append(
            {
                "name": str(raw.get("name", "")),
                "provider": str(raw.get("provider", "")),
                "status": str(raw.get("status", "")),
                "configured": raw.get("configured") is True,
                "credentialConfigured": raw.get("credentialConfigured") is True,
                "requiresNetwork": raw.get("requiresNetwork") is True,
                "liveChecked": raw.get("liveChecked") is True,
                "responseNonEmpty": raw.get("responseNonEmpty") is True,
                "failureKind": str(raw.get("failureKind", "")),
            }
        )
    return {"status": str(document.get("status", "")), "targets": targets}


def channel_summary(document: dict[str, Any]) -> dict[str, Any]:
    platforms = []
    for raw in document.get("platforms", []):
        if not isinstance(raw, dict):
            continue
        missing = raw.get("missingCredentialFields", [])
        platforms.append(
            {
                "platform": str(raw.get("platform", "")),
                "ready": raw.get("ready") is True,
                "missingCredentialFields": [str(item) for item in missing if isinstance(item, str)],
            }
        )
    results = []
    for raw in document.get("results", []):
        if not isinstance(raw, dict):
            continue
        results.append(
            {
                "platform": str(raw.get("platform", "")),
                "status": str(raw.get("status", "")),
                "runtimeAuditRecorded": raw.get("runtimeAuditRecorded") is True,
            }
        )
    return {
        "status": str(document.get("status", "")),
        "runtimeAuditConfigured": document.get("runtimeAuditConfigured") is True,
        "runtimeAuditReady": document.get("runtimeAuditReady") is True,
        "platforms": platforms,
        "results": results,
    }


def scope_includes_models(scope: str) -> bool:
    return scope in {"all", "models"}


def scope_includes_channels(scope: str) -> bool:
    return scope in {"all", "channels"}


def plan_ready(
    model: dict[str, Any], channels: dict[str, Any], scope: str = "all"
) -> tuple[bool, list[dict[str, Any]]]:
    blockers: list[dict[str, Any]] = []
    if scope_includes_models(scope):
        model_by_name = {item["name"]: item for item in model["targets"] if item["name"]}
        for name in sorted(EXPECTED_MODEL_TARGETS):
            target = model_by_name.get(name)
            if target is None or not target["configured"]:
                blockers.append({"kind": "model", "name": name, "reason": "missing_config"})

    if scope_includes_channels(scope):
        platform_by_name = {item["platform"]: item for item in channels["platforms"] if item["platform"]}
        for name in sorted(EXPECTED_PLATFORMS):
            platform = platform_by_name.get(name)
            if platform is None or not platform["ready"]:
                blockers.append(
                    {
                        "kind": "channel",
                        "name": name,
                        "reason": "missing_credentials_or_target",
                        "missingCredentialFields": [] if platform is None else platform["missingCredentialFields"],
                    }
                )
        if not channels["runtimeAuditConfigured"]:
            blockers.append({"kind": "runtime_audit", "name": "runtime", "reason": "missing_runtime_url"})
        elif not channels["runtimeAuditReady"]:
            blockers.append({"kind": "runtime_audit", "name": "runtime", "reason": "preflight_failed"})
    return not blockers, blockers


def run_passed(
    model: dict[str, Any],
    channels: dict[str, Any],
    model_code: int,
    channel_code: int,
    scope: str = "all",
) -> bool:
    targets = {item["name"]: item for item in model["targets"] if item["name"]}
    results = {item["platform"]: item for item in channels["results"] if item["platform"]}
    model_passed = not scope_includes_models(scope) or (
        model_code == 0
        and model.get("status") == "passed"
        and set(targets) == EXPECTED_MODEL_TARGETS
        and all(
            targets[name]["status"] == "pass"
            and targets[name]["liveChecked"]
            and targets[name]["responseNonEmpty"]
            for name in EXPECTED_MODEL_TARGETS
        )
    )
    channels_passed = not scope_includes_channels(scope) or (
        channel_code == 0
        and channels.get("status") == "pass"
        and channels.get("runtimeAuditConfigured") is True
        and channels.get("runtimeAuditReady") is True
        and set(results) == EXPECTED_PLATFORMS
        and all(
            results[name]["status"] == "pass" and results[name]["runtimeAuditRecorded"]
            for name in EXPECTED_PLATFORMS
        )
    )
    return model_passed and channels_passed


def emit(report: dict[str, Any], output: str | None) -> None:
    rendered = json.dumps(report, ensure_ascii=True, indent=2, sort_keys=True) + "\n"
    if output:
        destination = Path(output).expanduser().resolve()
        destination.parent.mkdir(parents=True, exist_ok=True)
        descriptor, temporary = tempfile.mkstemp(prefix=f".{destination.name}.", dir=destination.parent)
        try:
            os.fchmod(descriptor, stat.S_IRUSR | stat.S_IWUSR)
            with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
                handle.write(rendered)
            os.replace(temporary, destination)
        except BaseException:
            try:
                os.close(descriptor)
            except OSError:
                pass
            Path(temporary).unlink(missing_ok=True)
            raise
    sys.stdout.write(rendered)


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mode", choices=("plan", "run"), default="plan")
    parser.add_argument(
        "--scope",
        choices=("all", "models", "channels"),
        default="all",
        help="accept all external targets, only model targets, or only messaging channels",
    )
    parser.add_argument("--synon-binary", default=os.environ.get("SYNON_BINARY", "./dist/synon-go"))
    parser.add_argument(
        "--live-im-binary",
        default=os.environ.get("SYNON_LIVE_IM_BINARY", "./dist/synon-go-live-im-smoke"),
    )
    parser.add_argument("--config", help="optional Synon JSON configuration used by both smoke binaries")
    parser.add_argument("--runtime-url", help="optional runtime URL for durable redacted channel evidence")
    parser.add_argument("--timeout-seconds", type=int, default=30)
    parser.add_argument("--max-attempts", type=int, default=1)
    parser.add_argument("--output", help="write the redacted combined report atomically with mode 0600")
    args = parser.parse_args(argv)
    if not 1 <= args.timeout_seconds <= 300:
        parser.error("--timeout-seconds must be between 1 and 300")
    if not 1 <= args.max_attempts <= 5:
        parser.error("--max-attempts must be between 1 and 5")
    return args


def main(argv: list[str] | None = None) -> int:
    args = parse_args(sys.argv[1:] if argv is None else argv)
    try:
        synon = (
            executable_path(args.synon_binary, "Synon binary")
            if scope_includes_models(args.scope)
            else None
        )
        live_im = (
            executable_path(args.live_im_binary, "live IM binary")
            if scope_includes_channels(args.scope)
            else None
        )
        environment = dict(os.environ)
        environment["SYNON_LIVE_IM_SMOKE_TIMEOUT_SECONDS"] = str(args.timeout_seconds)

        model: dict[str, Any] = {"status": "not_requested", "targets": []}
        channels: dict[str, Any] = {
            "status": "not_requested",
            "runtimeAuditConfigured": False,
            "runtimeAuditReady": False,
            "platforms": [],
            "results": [],
        }
        if synon is not None:
            model_plan_command = [str(synon), "model-smoke", "--plan", "--require-all", "--json"]
            if args.config:
                model_plan_command.extend(("--config", args.config))
            _, raw_model_plan = run_json(model_plan_command, args.timeout_seconds + 10, environment)
            model = model_summary(raw_model_plan)
        if live_im is not None:
            channel_plan_command = [str(live_im), "--plan", "--require-all", "--json"]
            if args.config:
                channel_plan_command.extend(("--config", args.config))
            if args.runtime_url:
                channel_plan_command.extend(("--runtime-url", args.runtime_url))
            _, raw_channel_plan = run_json(channel_plan_command, args.timeout_seconds + 10, environment)
            channels = channel_summary(raw_channel_plan)
        ready, blockers = plan_ready(model, channels, args.scope)
        report: dict[str, Any] = {
            "schemaVersion": 1,
            "mode": args.mode,
            "scope": args.scope,
            "status": "ready" if ready else "blocked",
            "secretsRedacted": True,
            "model": model,
            "channels": channels,
            "blockers": blockers,
        }
        if args.mode == "plan" or not ready:
            emit(report, args.output)
            return 0 if ready else 3

        if os.environ.get("SYNON_EXTERNAL_TEST_AUTHORIZED") != AUTHORIZATION:
            report["status"] = "authorization_required"
            report["authorizationEnvironment"] = "SYNON_EXTERNAL_TEST_AUTHORIZED"
            emit(report, args.output)
            return 2

        model_code = 0
        channel_code = 0
        if synon is not None:
            model_run_command = [
                str(synon),
                "model-smoke",
                "--run",
                "--require-all",
                "--json",
                "--timeout",
                f"{args.timeout_seconds}s",
                "--max-attempts",
                str(args.max_attempts),
            ]
            if args.config:
                model_run_command.extend(("--config", args.config))
            model_code, raw_model_run = run_json(
                model_run_command,
                args.timeout_seconds * args.max_attempts * 2 + 20,
                environment,
            )
            model = model_summary(raw_model_run)
        if live_im is not None:
            channel_run_command = [str(live_im), "--require-all", "--json"]
            if args.config:
                channel_run_command.extend(("--config", args.config))
            if args.runtime_url:
                channel_run_command.extend(("--runtime-url", args.runtime_url))
            channel_code, raw_channel_run = run_json(channel_run_command, args.timeout_seconds + 20, environment)
            channels = channel_summary(raw_channel_run)
        passed = run_passed(model, channels, model_code, channel_code, args.scope)
        report.update(
            {
                "status": "passed" if passed else "failed",
                "model": model,
                "channels": channels,
                "blockers": [],
                "liveAuthorized": True,
            }
        )
        emit(report, args.output)
        return 0 if passed else 1
    except (GateError, OSError) as error:
        emit(
            {
                "schemaVersion": 1,
                "mode": args.mode,
                "status": "error",
                "secretsRedacted": True,
                "error": str(error),
            },
            args.output,
        )
        return 1


if __name__ == "__main__":
    raise SystemExit(main())

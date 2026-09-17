#!/usr/bin/env python3

from __future__ import annotations

import json
import os
import stat
import subprocess
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent
GATE = ROOT / "scripts" / "p9_external_acceptance.py"
AUTHORIZATION = "I_ACCEPT_NETWORK_COST_AND_MESSAGES"


FAKE_BINARY = r'''#!/usr/bin/env python3
import json
import os
import pathlib
import sys

ready = os.environ.get("FAKE_READY") == "1"
redacted = os.environ.get("FAKE_REDACTED", "1") == "1"
audit_configured = os.environ.get("FAKE_AUDIT_CONFIGURED") == "1"
audit_ready = os.environ.get("FAKE_AUDIT_READY") == "1"
audit_recorded = os.environ.get("FAKE_AUDIT_RECORDED") == "1"
events = pathlib.Path(os.environ["FAKE_EVENTS"])
is_model = len(sys.argv) > 1 and sys.argv[1] == "model-smoke"
is_plan = "--plan" in sys.argv

if is_model:
    if not is_plan:
        events.write_text(events.read_text() + "model-run\n" if events.exists() else "model-run\n")
    targets = []
    for name in ("runner", "compact"):
        targets.append({
            "name": name,
            "provider": "openai_compatible",
            "status": "planned" if is_plan and ready else ("pass" if ready else "skipped_missing_config"),
            "configured": ready,
            "credentialConfigured": ready,
            "requiresNetwork": ready,
            "liveChecked": ready and not is_plan,
            "responseNonEmpty": ready and not is_plan,
        })
    print(json.dumps({"status": "planned" if is_plan else "passed", "mode": "plan" if is_plan else "run", "targets": targets, "secretsRedacted": redacted}))
else:
    if not is_plan:
        events.write_text(events.read_text() + "channel-run\n" if events.exists() else "channel-run\n")
    platforms = [{"platform": name, "ready": ready, "missingCredentialFields": [] if ready else [name.upper() + "_TOKEN"]} for name in ("wechat", "feishu")]
    results = [] if is_plan else [{"platform": name, "status": "pass", "runtimeAuditRecorded": audit_recorded} for name in ("wechat", "feishu")]
    print(json.dumps({"status": "planned" if is_plan else "pass", "platforms": platforms, "results": results, "secretsRedacted": redacted, "runtimeAuditConfigured": audit_configured, "runtimeAuditReady": audit_ready}))
'''


class ExternalAcceptanceTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.model = self.root / "synon-go"
        self.channels = self.root / "synon-go-live-im-smoke"
        self.events = self.root / "events"
        for binary in (self.model, self.channels):
            binary.write_text(FAKE_BINARY, encoding="utf-8")
            binary.chmod(0o755)

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def run_gate(
        self,
        *arguments: str,
        ready: bool,
        authorized: bool = False,
        redacted: bool = True,
        audit_configured: bool = True,
        audit_ready: bool = True,
        audit_recorded: bool = True,
    ):
        environment = dict(os.environ)
        environment.update(
            {
                "FAKE_READY": "1" if ready else "0",
                "FAKE_REDACTED": "1" if redacted else "0",
                "FAKE_AUDIT_CONFIGURED": "1" if audit_configured else "0",
                "FAKE_AUDIT_READY": "1" if audit_ready else "0",
                "FAKE_AUDIT_RECORDED": "1" if audit_recorded else "0",
                "FAKE_EVENTS": str(self.events),
            }
        )
        if authorized:
            environment["SYNON_EXTERNAL_TEST_AUTHORIZED"] = AUTHORIZATION
        else:
            environment.pop("SYNON_EXTERNAL_TEST_AUTHORIZED", None)
        result = subprocess.run(
            [
                "python3",
                "-B",
                str(GATE),
                "--synon-binary",
                str(self.model),
                "--live-im-binary",
                str(self.channels),
                *arguments,
            ],
            check=False,
            capture_output=True,
            text=True,
            env=environment,
        )
        return result, json.loads(result.stdout)

    def test_plan_reports_every_missing_target_without_live_calls(self) -> None:
        result, report = self.run_gate(ready=False, audit_configured=False, audit_ready=False)
        self.assertEqual(result.returncode, 3)
        self.assertEqual(report["status"], "blocked")
        self.assertEqual(len(report["blockers"]), 5)
        self.assertFalse(self.events.exists())

    def test_ready_plan_does_not_make_live_calls(self) -> None:
        result, report = self.run_gate(ready=True)
        self.assertEqual(result.returncode, 0)
        self.assertEqual(report["status"], "ready")
        self.assertFalse(self.events.exists())

    def test_live_run_requires_exact_authorization(self) -> None:
        result, report = self.run_gate("--mode", "run", ready=True)
        self.assertEqual(result.returncode, 2)
        self.assertEqual(report["status"], "authorization_required")
        self.assertFalse(self.events.exists())

    def test_live_run_requires_runtime_audit_before_delivery(self) -> None:
        result, report = self.run_gate(
            "--mode",
            "run",
            ready=True,
            audit_configured=False,
            audit_ready=False,
            authorized=True,
        )
        self.assertEqual(result.returncode, 3)
        self.assertEqual(report["status"], "blocked")
        self.assertEqual(report["blockers"], [{"kind": "runtime_audit", "name": "runtime", "reason": "missing_runtime_url"}])
        self.assertFalse(self.events.exists())

    def test_live_run_requires_successful_runtime_audit_preflight(self) -> None:
        result, report = self.run_gate("--mode", "run", ready=True, audit_ready=False, authorized=True)
        self.assertEqual(result.returncode, 3)
        self.assertEqual(report["status"], "blocked")
        self.assertEqual(report["blockers"], [{"kind": "runtime_audit", "name": "runtime", "reason": "preflight_failed"}])
        self.assertFalse(self.events.exists())

    def test_authorized_live_run_requires_all_six_results(self) -> None:
        output = self.root / "report.json"
        result, report = self.run_gate(
            "--mode",
            "run",
            "--output",
            str(output),
            ready=True,
            authorized=True,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(report["status"], "passed")
        self.assertEqual(self.events.read_text().splitlines(), ["model-run", "channel-run"])
        self.assertEqual(stat.S_IMODE(output.stat().st_mode), 0o600)
        self.assertEqual(json.loads(output.read_text()), report)

    def test_live_run_rejects_missing_runtime_audit_evidence(self) -> None:
        result, report = self.run_gate(
            "--mode",
            "run",
            ready=True,
            authorized=True,
            audit_recorded=False,
        )
        self.assertEqual(result.returncode, 1)
        self.assertEqual(report["status"], "failed")
        self.assertEqual(self.events.read_text().splitlines(), ["model-run", "channel-run"])

    def test_missing_redaction_attestation_fails_closed(self) -> None:
        result, report = self.run_gate(ready=True, redacted=False)
        self.assertEqual(result.returncode, 1)
        self.assertEqual(report["status"], "error")
        self.assertIn("redaction", report["error"])
        self.assertFalse(self.events.exists())

    def test_model_scope_ignores_missing_channel_binary_and_audit(self) -> None:
        self.channels.unlink()
        result, report = self.run_gate(
            "--scope",
            "models",
            ready=True,
            audit_configured=False,
            audit_ready=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(report["status"], "ready")
        self.assertEqual(report["scope"], "models")
        self.assertEqual(report["channels"]["status"], "not_requested")
        self.assertFalse(self.events.exists())

    def test_authorized_model_scope_runs_only_models(self) -> None:
        self.channels.unlink()
        result, report = self.run_gate(
            "--scope",
            "models",
            "--mode",
            "run",
            ready=True,
            authorized=True,
            audit_configured=False,
            audit_ready=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(report["status"], "passed")
        self.assertEqual(report["model"]["status"], "passed")
        self.assertEqual(self.events.read_text().splitlines(), ["model-run"])

    def test_channel_scope_ignores_missing_model_binary(self) -> None:
        self.model.unlink()
        result, report = self.run_gate("--scope", "channels", ready=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(report["status"], "ready")
        self.assertEqual(report["scope"], "channels")
        self.assertEqual(report["model"]["status"], "not_requested")
        self.assertFalse(self.events.exists())

    def test_authorized_channel_scope_runs_only_channels(self) -> None:
        self.model.unlink()
        result, report = self.run_gate(
            "--scope",
            "channels",
            "--mode",
            "run",
            ready=True,
            authorized=True,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(report["status"], "passed")
        self.assertEqual(report["channels"]["status"], "pass")
        self.assertEqual(self.events.read_text().splitlines(), ["channel-run"])


if __name__ == "__main__":
    unittest.main()

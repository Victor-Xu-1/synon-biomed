#!/usr/bin/env python3

from __future__ import annotations

import hashlib
import importlib.util
import json
import sqlite3
import tempfile
import unittest
from datetime import datetime, timezone
from pathlib import Path


MODULE_PATH = Path(__file__).with_name("science_task_harness.py")
SPEC = importlib.util.spec_from_file_location("science_task_harness", MODULE_PATH)
assert SPEC is not None and SPEC.loader is not None
HARNESS = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(HARNESS)


class ScienceTaskHarnessTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.database = self.root / "workspace.sqlite"
        self.suite = self.root / "suite.json"
        self.browser = self.root / "browser.json"
        self.now = datetime(2026, 8, 3, 12, 0, tzinfo=timezone.utc)
        self.suite.write_text(
            json.dumps(
                {
                    "schema": "synon.science-task-suite.v1",
                    "browserAuthority": "codex_builtin",
                    "scientificDeliverablePolicy": {
                        "defaultForbiddenExtensions": [".json", ".jsonl"],
                        "engineeringEvidenceExtensions": [".json", ".jsonl"],
                        "explicitUserExportTool": "session_export",
                    },
                    "tasks": [
                        {
                            "id": "chem-test",
                            "initialPromptSha256": hashlib.sha256(
                                b"fixed chemistry prompt"
                            ).hexdigest(),
                            "acceptance": {
                                "terminalFrameStatuses": ["completed"],
                                "minimumToolCalls": 1,
                                "requireSingleSoftwareRuntimeProvider": True,
                                "requiredSoftwareRuntimeExecutions": [
                                    {
                                        "capability": "molecular-docking",
                                        "provider": "local-conda",
                                        "minOutputCount": 2,
                                        "requiredOutputPathPatterns": [
                                            "ranked.*\\.pdbqt$",
                                            "execution.*\\.log$",
                                        ],
                                    }
                                ],
                                "requiredArtifacts": [
                                    {"nameRegex": "candidate.*\\.sdf$", "minCount": 1}
                                ],
                                "requiredBrowserChecks": ["sdf_structure_preview"],
                                "requireSingleUserSubmission": True,
                            },
                            "reviewRubric": [
                                {
                                    "id": "actual_docking_evidence",
                                    "criterion": "Docking must be backed by an executed engine and inspectable outputs.",
                                    "evidenceSources": ["transcript", "artifacts"],
                                }
                            ],
                        }
                    ],
                }
            ),
            encoding="utf-8",
        )
        self.browser.write_text(
            json.dumps(
                {
                    "schema": "synon.codex-browser-evidence.v1",
                    "browser": "codex_builtin",
                    "taskId": "chem-test",
                    "conversationId": "frame-1",
                    "capturedAt": "2026-08-03T11:59:00Z",
                    "url": "http://127.0.0.1:8765/#/conversation/frame-1",
                    "screenshotSha256": "a" * 64,
                    "checks": [
                        {"id": "sdf_structure_preview", "status": "pass"},
                        {
                            "id": "actual_docking_evidence",
                            "status": "pass",
                            "evidence": "tool batch batch-1 and candidate_designs.sdf were reviewed",
                        },
                    ],
                }
            ),
            encoding="utf-8",
        )
        self._create_database()

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def _create_database(self) -> None:
        connection = sqlite3.connect(self.database)
        connection.executescript(
            """
            CREATE TABLE frames(id TEXT PRIMARY KEY,project_id TEXT,status TEXT,created_at TEXT,updated_at TEXT);
            CREATE TABLE transcript_streams(stream_uid TEXT PRIMARY KEY,frame_id TEXT,epoch INTEGER,
              input_revision INTEGER,consumed_input_revision INTEGER,next_event_id INTEGER,
              next_publication_seq INTEGER,next_checkpoint_sequence INTEGER);
            CREATE TABLE transcript_runner_attempts(stream_uid TEXT,attempt INTEGER,status TEXT,phase TEXT,
              claimed_at TEXT,expires_at TEXT,finished_at TEXT,finished_event_id INTEGER,
              PRIMARY KEY(stream_uid,attempt));
            CREATE TABLE transcript_events(stream_uid TEXT,event_id INTEGER,event_type TEXT,payload_json BLOB,
              PRIMARY KEY(stream_uid,event_id));
            CREATE TABLE transcript_tool_call_items(batch_id TEXT,stream_uid TEXT,ordinal INTEGER,
              tool_name TEXT,state TEXT,PRIMARY KEY(batch_id,ordinal));
            CREATE TABLE artifacts(id TEXT PRIMARY KEY,project_id TEXT,name TEXT,current_version_number INTEGER);
            CREATE TABLE artifact_versions(id TEXT PRIMARY KEY,artifact_id TEXT,version_number INTEGER,
              content BLOB,storage_path TEXT,content_sha256 TEXT,size_bytes INTEGER);
            CREATE TABLE transcript_artifact_commits(stream_uid TEXT,runner_attempt INTEGER,
              source_event_id INTEGER,artifact_id TEXT,version_id TEXT,
              PRIMARY KEY(stream_uid,runner_attempt,source_event_id,artifact_id,version_id));
            CREATE TABLE kernel_local_operations(
              operation_id TEXT PRIMARY KEY,project_id TEXT,frame_id TEXT,stream_uid TEXT,
              tool TEXT,state TEXT,input_json TEXT,input_sha256 TEXT,result_json TEXT,
              result_sha256 TEXT,approval_decision TEXT,approval_source TEXT,
              approval_actor_id TEXT,started_at TEXT,terminal_at TEXT,created_at TEXT);
            """
        )
        content = b"candidate sdf content"
        connection.execute(
            "INSERT INTO frames VALUES(?,?,?,?,?)",
            ("frame-1", "project-1", "completed", "2026-08-03T10:00:00Z", "2026-08-03T11:00:00Z"),
        )
        connection.execute(
            "INSERT INTO transcript_streams VALUES(?,?,?,?,?,?,?,?)",
            ("frame:frame-1", "frame-1", 1, 1, 1, 4, 4, 2),
        )
        connection.execute(
            "INSERT INTO transcript_runner_attempts VALUES(?,?,?,?,?,?,?,?)",
            (
                "frame:frame-1",
                1,
                "completed",
                "terminal",
                "2026-08-03T10:00:00Z",
                "2026-08-03 10:10:00.123456789 +0000 UTC",
                "2026-08-03T10:05:00Z",
                3,
            ),
        )
        connection.execute(
            "INSERT INTO transcript_events VALUES(?,?,?,?)",
            (
                "frame:frame-1",
                1,
                "user_message",
                json.dumps({"text": "fixed chemistry prompt"}),
            ),
        )
        connection.execute(
            "INSERT INTO transcript_events VALUES(?,?,?,?)",
            ("frame:frame-1", 3, "runner_finished", "{}"),
        )
        connection.execute(
            "INSERT INTO transcript_tool_call_items VALUES(?,?,?,?,?)",
            ("batch-1", "frame:frame-1", 0, "software_runtime", "completed"),
        )
        connection.execute(
            "INSERT INTO artifacts VALUES(?,?,?,?)",
            ("artifact-1", "project-1", "candidate_designs.sdf", 1),
        )
        connection.execute(
            "INSERT INTO artifact_versions VALUES(?,?,?,?,?,?,?)",
            (
                "version-1",
                "artifact-1",
                1,
                content,
                None,
                hashlib.sha256(content).hexdigest(),
                len(content),
            ),
        )
        connection.execute(
            "INSERT INTO transcript_artifact_commits VALUES(?,?,?,?,?)",
            ("frame:frame-1", 1, 2, "artifact-1", "version-1"),
        )
        request = {
            "capability": "molecular-docking",
            "provider": "local-conda",
            "language": "python",
            "packages": [{"manager": "conda", "spec": "vina=1.2.7"}],
            "imports": ["vina"],
            "executable": "python",
            "args": ["dock.py"],
            "working_dir": "/workspace",
            "timeout_seconds": 600,
            "expected_outputs": [
                {"path": "out/ranked-poses.pdbqt", "min_bytes": 64},
                {"path": "out/execution.log", "min_bytes": 64},
            ],
        }
        input_json = json.dumps(request, separators=(",", ":"), sort_keys=True)
        input_digest = hashlib.sha256(input_json.encode()).hexdigest()
        result = {
            "ok": True,
            "status": "completed",
            "exit_status": "ok",
            "provider_id": "local-conda",
            "environment": "swr-test",
            "runtime_generation": "a" * 64,
            "request_digest": input_digest,
            "executable": "python",
            "exit_code": 0,
            "timed_out": False,
            "stdout_sha256": "b" * 64,
            "stderr_sha256": "c" * 64,
            "outputs": [
                {"path": "out/ranked-poses.pdbqt", "bytes": 128, "sha256": "d" * 64},
                {"path": "out/execution.log", "bytes": 256, "sha256": "e" * 64},
            ],
            "cleanup": {
                "process_group_terminated": True,
                "process_tree_terminated": True,
                "temporary_streams_closed": True,
            },
        }
        result_json = json.dumps(result, separators=(",", ":"), sort_keys=True)
        connection.execute(
            "INSERT INTO kernel_local_operations VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
            (
                "operation-docking-1",
                "project-1",
                "frame-1",
                "frame:frame-1",
                "software_runtime",
                "completed",
                input_json,
                input_digest,
                result_json,
                hashlib.sha256(result_json.encode()).hexdigest(),
                "allow",
                "policy",
                "system",
                "2026-08-03T11:20:00Z",
                "2026-08-03T11:30:00Z",
                "2026-08-03T11:15:00Z",
            ),
        )
        connection.commit()
        connection.close()

    def audit(self, browser: bool = True):
        return HARNESS.audit(
            self.database,
            self.suite,
            "chem-test",
            "frame-1",
            self.browser if browser else None,
            None,
            self.now,
        )

    def test_passes_only_with_terminal_tool_artifact_and_codex_browser_evidence(self) -> None:
        report = self.audit()
        self.assertEqual(report["status"], "pass")
        self.assertEqual(report["tools"]["count"], 1)
        self.assertEqual(report["artifacts"]["currentCommittedCount"], 1)
        self.assertEqual(report["browser"]["status"], "pass")
        self.assertEqual(report["softwareRuntimeExecutions"]["count"], 1)
        self.assertEqual(
            report["softwareRuntimeExecutions"]["items"][0]["capability"],
            "molecular-docking",
        )
        self.assertEqual(
            [
                output["path"]
                for output in report["softwareRuntimeExecutions"]["items"][0][
                    "outputs"
                ]
            ],
            ["out/execution.log", "out/ranked-poses.pdbqt"],
        )

    def test_rejects_missing_required_unified_runtime_receipt(self) -> None:
        connection = sqlite3.connect(self.database)
        connection.execute("DELETE FROM kernel_local_operations")
        connection.commit()
        connection.close()

        report = self.audit()
        self.assertEqual(report["status"], "fail")
        self.assertIn(
            "required_software_runtime_execution_missing:molecular-docking",
            report["failures"],
        )

    def test_rejects_completed_runtime_with_tampered_receipt(self) -> None:
        connection = sqlite3.connect(self.database)
        connection.execute(
            "UPDATE kernel_local_operations SET result_json=replace(result_json, "
            "'out/execution.log','out/missing.log')"
        )
        connection.commit()
        connection.close()

        report = self.audit()
        self.assertEqual(report["status"], "fail")
        self.assertIn(
            "software_runtime_receipt_invalid:operation-docking-1",
            report["failures"],
        )

    def test_rejects_runtime_without_verified_process_cleanup(self) -> None:
        connection = sqlite3.connect(self.database)
        row = connection.execute(
            "SELECT result_json FROM kernel_local_operations"
        ).fetchone()
        result = json.loads(row[0])
        result["cleanup"]["process_tree_terminated"] = False
        result_json = json.dumps(result, separators=(",", ":"), sort_keys=True)
        connection.execute(
            "UPDATE kernel_local_operations SET result_json=?,result_sha256=?",
            (result_json, hashlib.sha256(result_json.encode()).hexdigest()),
        )
        connection.commit()
        connection.close()

        report = self.audit()
        self.assertEqual(report["status"], "fail")
        self.assertIn(
            "software_runtime_cleanup_unverified:operation-docking-1",
            report["failures"],
        )

    def test_accepts_every_terminal_tool_item_state_and_rejects_live_states(self) -> None:
        connection = sqlite3.connect(self.database)
        connection.execute("DELETE FROM transcript_tool_call_items")
        for ordinal, state in enumerate(
            ("completed", "failed", "blocked", "cancelled", "outcome_unknown")
        ):
            connection.execute(
                "INSERT INTO transcript_tool_call_items VALUES(?,?,?,?,?)",
                (f"batch-{ordinal}", "frame:frame-1", ordinal, "python", state),
            )
        connection.commit()
        connection.close()

        report = self.audit()
        self.assertEqual(report["status"], "pass")
        self.assertEqual(report["tools"]["openCount"], 0)

        connection = sqlite3.connect(self.database)
        connection.execute(
            "INSERT INTO transcript_tool_call_items VALUES(?,?,?,?,?)",
            ("batch-live", "frame:frame-1", 99, "python", "waiting"),
        )
        connection.commit()
        connection.close()

        report = self.audit()
        self.assertEqual(report["status"], "fail")
        self.assertEqual(report["tools"]["openCount"], 1)
        self.assertIn("open_tool_call_items", report["failures"])

    def test_rejects_model_visible_completion_with_stale_running_attempt(self) -> None:
        connection = sqlite3.connect(self.database)
        connection.execute(
            "UPDATE transcript_runner_attempts SET status='running',phase='executing',"
            "finished_at=NULL,finished_event_id=NULL,expires_at='2026-08-03T11:00:00Z'"
        )
        connection.commit()
        connection.close()
        report = self.audit()
        self.assertEqual(report["status"], "fail")
        self.assertIn("stale_running_attempt", report["failures"])
        self.assertIn("terminal_frame_has_nonterminal_latest_attempt", report["failures"])

    def test_requires_codex_builtin_browser_evidence(self) -> None:
        report = self.audit(browser=False)
        self.assertEqual(report["status"], "fail")
        self.assertIn("codex_browser_evidence_missing", report["failures"])
        self.assertIn(
            "quality_review_check_failed:actual_docking_evidence",
            report["failures"],
        )
        self.assertEqual(report["qualityReview"]["status"], "fail")

    def test_rejects_quality_review_without_specific_evidence(self) -> None:
        document = json.loads(self.browser.read_text(encoding="utf-8"))
        document["checks"][1].pop("evidence")
        self.browser.write_text(json.dumps(document), encoding="utf-8")

        report = self.audit()
        self.assertEqual(report["status"], "fail")
        self.assertIn(
            "quality_review_evidence_missing:actual_docking_evidence",
            report["failures"],
        )

    def test_rejects_more_than_one_user_submission_when_required(self) -> None:
        connection = sqlite3.connect(self.database)
        connection.execute(
            "INSERT INTO transcript_events VALUES(?,?,?,?)",
            (
                "frame:frame-1",
                2,
                "user_message",
                json.dumps({"text": "second submission"}),
            ),
        )
        connection.commit()
        connection.close()

        report = self.audit()
        self.assertEqual(report["status"], "fail")
        self.assertIn("single_user_submission_required", report["failures"])

    def test_ask_user_answers_do_not_count_as_new_task_submissions(self) -> None:
        connection = sqlite3.connect(self.database)
        connection.execute(
            "INSERT INTO transcript_events VALUES(?,?,?,?)",
            (
                "frame:frame-1",
                2,
                "user_message",
                json.dumps({"text": "Ask User answer"}),
            ),
        )
        connection.execute(
            "INSERT INTO transcript_events VALUES(?,?,?,?)",
            (
                "frame:frame-1",
                4,
                "user_input_response",
                json.dumps({"text": "Ask User answer"}),
            ),
        )
        connection.commit()
        connection.close()

        report = self.audit()
        self.assertEqual(report["status"], "pass")

    def test_rejects_a_shortened_or_replaced_initial_task_prompt(self) -> None:
        connection = sqlite3.connect(self.database)
        connection.execute(
            "UPDATE transcript_events SET payload_json=? WHERE event_type='user_message'",
            (json.dumps({"text": "shortened prompt"}),),
        )
        connection.commit()
        connection.close()
        report = self.audit()
        self.assertEqual(report["status"], "fail")
        self.assertIn("initial_task_prompt_mismatch", report["failures"])

    def test_verifies_externalized_artifact_content_from_database_blob_store(self) -> None:
        content = b"external candidate sdf content"
        relative = Path("artifact-versions/ve/version-1.blob")
        external = self.database.with_name(f"{self.database.name}.blobs") / relative
        external.parent.mkdir(parents=True)
        external.write_bytes(content)
        connection = sqlite3.connect(self.database)
        connection.execute(
            "UPDATE artifact_versions SET content=?,storage_path=?,content_sha256=?,size_bytes=? "
            "WHERE id='version-1'",
            (b"", str(relative), hashlib.sha256(content).hexdigest(), len(content)),
        )
        connection.commit()
        connection.close()
        report = self.audit()
        self.assertEqual(report["status"], "pass")
        self.assertEqual(report["artifacts"]["items"][0]["digest"], "pass")

    def test_rejects_two_current_artifacts_with_the_same_name(self) -> None:
        content = b"second candidate sdf content"
        connection = sqlite3.connect(self.database)
        connection.execute(
            "INSERT INTO artifacts VALUES(?,?,?,?)",
            ("artifact-2", "project-1", "candidate_designs.sdf", 1),
        )
        connection.execute(
            "INSERT INTO artifact_versions VALUES(?,?,?,?,?,?,?)",
            (
                "version-2",
                "artifact-2",
                1,
                content,
                None,
                hashlib.sha256(content).hexdigest(),
                len(content),
            ),
        )
        connection.execute(
            "INSERT INTO transcript_artifact_commits VALUES(?,?,?,?,?)",
            ("frame:frame-1", 1, 3, "artifact-2", "version-2"),
        )
        connection.commit()
        connection.close()

        report = self.audit()
        self.assertEqual(report["status"], "fail")
        self.assertIn("duplicate_current_artifact_names", report["failures"])
        self.assertEqual(
            report["artifacts"]["duplicateCurrentNames"],
            ["candidate_designs.sdf"],
        )

    def test_rejects_default_json_scientific_deliverable(self) -> None:
        content = b"{\"computed\":true}\n"
        connection = sqlite3.connect(self.database)
        connection.execute(
            "INSERT INTO artifacts VALUES(?,?,?,?)",
            ("artifact-json", "project-1", "computed_properties.json", 1),
        )
        connection.execute(
            "INSERT INTO artifact_versions VALUES(?,?,?,?,?,?,?)",
            (
                "version-json",
                "artifact-json",
                1,
                content,
                None,
                hashlib.sha256(content).hexdigest(),
                len(content),
            ),
        )
        connection.execute(
            "INSERT INTO transcript_artifact_commits VALUES(?,?,?,?,?)",
            ("frame:frame-1", 1, 4, "artifact-json", "version-json"),
        )
        connection.commit()
        connection.close()

        report = self.audit()
        self.assertEqual(report["status"], "fail")
        self.assertIn("default_json_scientific_artifact_forbidden", report["failures"])


if __name__ == "__main__":
    unittest.main()

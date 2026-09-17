import contextlib
import hashlib
import io
import json
import pathlib
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

import clean_clone_gate


def command_set(argv: list[str]) -> dict:
    return {
        "schema": clean_clone_gate.SCHEMA,
        "commands": [{"name": "probe", "argv": argv, "cwd": ".", "timeout_seconds": 10}],
    }


def git(repo: pathlib.Path, *args: str) -> str:
    return subprocess.check_output(["git", "-C", str(repo), *args], text=True).strip()


class CleanCloneGateTests(unittest.TestCase):
    def make_repo(self, root: pathlib.Path) -> tuple[pathlib.Path, str]:
        repo = root / "repo"
        subprocess.run(["git", "init", "-q", str(repo)], check=True)
        subprocess.run(["git", "-C", str(repo), "config", "user.email", "test@example.invalid"], check=True)
        subprocess.run(["git", "-C", str(repo), "config", "user.name", "Test"], check=True)
        (repo / "tracked.txt").write_text("clean\n", encoding="utf-8")
        subprocess.run(["git", "-C", str(repo), "add", "tracked.txt"], check=True)
        subprocess.run(["git", "-C", str(repo), "commit", "-qm", "base"], check=True)
        return repo, git(repo, "rev-parse", "HEAD")

    def test_runs_exact_sha_and_cleans_temp_clone(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            repo, sha = self.make_repo(root)
            temp_root = root / "temp"
            temp_root.mkdir()
            result, retained = clean_clone_gate.run_gate(
                repo,
                sha,
                command_set([sys.executable, "-c", "from pathlib import Path; assert Path('tracked.txt').read_text() == 'clean\\n'"]),
                temp_root,
            )
            self.assertEqual(result["result"], "PASS")
            self.assertIsNone(retained)
            self.assertEqual(list(temp_root.iterdir()), [])

    def test_cleans_read_only_package_cache_directories(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            repo, sha = self.make_repo(root)
            temp_root = root / "temp"
            temp_root.mkdir()
            probe = (
                "import os,pathlib; "
                "cache=pathlib.Path(os.environ['GOMODCACHE'])/'example@v1'; "
                "cache.mkdir(parents=True); "
                "(cache/'module.go').write_text('package example\\n'); "
                "cache.chmod(0o555)"
            )
            result, retained = clean_clone_gate.run_gate(
                repo, sha, command_set([sys.executable, "-c", probe]), temp_root
            )
            self.assertEqual(result["result"], "PASS")
            self.assertIsNone(retained)
            self.assertEqual(list(temp_root.iterdir()), [])

    def test_strips_inherited_secrets_and_isolates_home(self):
        with tempfile.TemporaryDirectory() as directory, mock.patch.dict(
            "os.environ", {"SYNON_TEST_SECRET": "must-not-cross-boundary"}
        ):
            root = pathlib.Path(directory)
            repo, sha = self.make_repo(root)
            temp_root = root / "temp"
            temp_root.mkdir()
            probe = (
                "import os,pathlib; "
                "assert 'SYNON_TEST_SECRET' not in os.environ; "
                "assert 'isolated/home' in pathlib.Path(os.environ['HOME']).as_posix()"
            )
            result, _ = clean_clone_gate.run_gate(
                repo, sha, command_set([sys.executable, "-c", probe]), temp_root
            )
            self.assertEqual(result["result"], "PASS")

    def test_preserves_standard_proxy_transport_without_forwarding_other_secrets(self):
        with tempfile.TemporaryDirectory() as directory, mock.patch.dict(
            "os.environ",
            {
                "HTTPS_PROXY": "http://127.0.0.1:9080",
                "https_proxy": "http://127.0.0.1:9080",
                "SYNON_TEST_SECRET": "must-not-cross-boundary",
            },
            clear=True,
        ):
            root = pathlib.Path(directory)
            repo, sha = self.make_repo(root)
            temp_root = root / "temp"
            temp_root.mkdir()
            probe = (
                "import os; "
                "assert os.environ['HTTPS_PROXY'] == 'http://127.0.0.1:9080'; "
                "assert os.environ['https_proxy'] == 'http://127.0.0.1:9080'; "
                "assert 'SYNON_TEST_SECRET' not in os.environ"
            )
            result, _ = clean_clone_gate.run_gate(
                repo, sha, command_set([sys.executable, "-c", probe]), temp_root
            )
            self.assertEqual(result["result"], "PASS")

    def test_rejects_dirty_source(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            repo, sha = self.make_repo(root)
            (repo / "untracked.txt").write_text("dirty\n")
            temp_root = root / "temp"
            temp_root.mkdir()
            with self.assertRaisesRegex(clean_clone_gate.CloneGateError, "source_dirty"):
                clean_clone_gate.run_gate(repo, sha, command_set([sys.executable, "-c", "pass"]), temp_root)

    def test_rejects_clean_source_checked_out_at_another_commit(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            repo, old_sha = self.make_repo(root)
            (repo / "tracked.txt").write_text("two\n", encoding="utf-8")
            subprocess.run(["git", "-C", str(repo), "commit", "-qam", "second"], check=True)
            temp_root = root / "temp"
            temp_root.mkdir()
            with self.assertRaisesRegex(clean_clone_gate.CloneGateError, "source_head_mismatch"):
                clean_clone_gate.run_gate(
                    repo, old_sha, command_set([sys.executable, "-c", "pass"]), temp_root
                )

    def test_records_failure_without_output_content(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            repo, sha = self.make_repo(root)
            temp_root = root / "temp"
            temp_root.mkdir()
            result, _ = clean_clone_gate.run_gate(
                repo,
                sha,
                command_set([sys.executable, "-c", "import sys; print('bounded'); sys.exit(7)"]),
                temp_root,
            )
            record = result["commands"][0]
            self.assertEqual(result["result"], "FAIL")
            self.assertEqual(record["exit_code"], 7)
            self.assertNotIn("stdout", record)
            self.assertEqual(record["stdout_bytes"], 8)

    def test_keep_failure_retains_local_only_diagnostic_logs(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            repo, sha = self.make_repo(root)
            temp_root = root / "temp"
            temp_root.mkdir()
            result, retained = clean_clone_gate.run_gate(
                repo,
                sha,
                command_set([sys.executable, "-c", "import sys; print('detail'); sys.exit(9)"]),
                temp_root,
                keep_on_failure=True,
            )
            self.assertEqual(result["result"], "FAIL")
            self.assertIsNotNone(retained)
            self.assertEqual(result["retained_local_id"], retained.name)
            self.assertNotIn(str(temp_root), result["retained_local_id"])
            logs = result["commands"][0]["diagnostic_logs"]
            self.assertTrue(logs["local_only"])
            self.assertEqual((retained / logs["stdout"]).read_text(encoding="utf-8"), "detail\n")

    def test_rejects_secret_argument_and_shell_shaped_command(self):
        with self.assertRaisesRegex(clean_clone_gate.CloneGateError, "secret_arg"):
            clean_clone_gate.validate_command_set(command_set(["tool", "--token=value"]))
        with self.assertRaisesRegex(clean_clone_gate.CloneGateError, "shell_forbidden"):
            clean_clone_gate.validate_command_set(command_set(["sh", "-c", "echo bypass"]))
        invalid = command_set(["tool"])
        invalid["commands"][0]["name"] = "../escape"
        with self.assertRaisesRegex(clean_clone_gate.CloneGateError, "name_invalid"):
            clean_clone_gate.validate_command_set(invalid)

    def test_rejects_escape_and_bool_timeout(self):
        commands = command_set([sys.executable, "-c", "pass"])
        commands["commands"][0]["cwd"] = "../outside"
        with self.assertRaisesRegex(clean_clone_gate.CloneGateError, "cwd_invalid"):
            clean_clone_gate.validate_command_set(commands)
        commands = command_set([sys.executable, "-c", "pass"])
        commands["commands"][0]["timeout_seconds"] = True
        with self.assertRaisesRegex(clean_clone_gate.CloneGateError, "timeout_invalid"):
            clean_clone_gate.validate_command_set(commands)

    def test_cli_rejects_existing_output_before_execution(self):
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory) / "result.json"
            output.write_text("preserve\n", encoding="utf-8")
            argv = [
                "--source", directory,
                "--sha", "a" * 40,
                "--commands", str(output),
                "--temp-root", directory,
                "--output", str(output),
            ]
            with mock.patch.object(clean_clone_gate, "run_gate") as runner:
                with contextlib.redirect_stdout(io.StringIO()):
                    self.assertEqual(clean_clone_gate.main(argv), 2)
                runner.assert_not_called()
            self.assertEqual(output.read_text(encoding="utf-8"), "preserve\n")

    def test_evidence_output_is_new_and_private(self):
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory) / "result.json"
            clean_clone_gate._write_new_json(output, {"result": "PASS"})
            self.assertEqual(output.stat().st_mode & 0o777, 0o600)
            with self.assertRaisesRegex(clean_clone_gate.CloneGateError, "output_exists"):
                clean_clone_gate._write_new_json(output, {"result": "REPLACE"})

    def test_validate_only_accepts_reviewed_command_set(self):
        with tempfile.TemporaryDirectory() as directory:
            command_path = pathlib.Path(directory) / "commands.json"
            command_path.write_text(json.dumps(command_set([sys.executable, "-c", "pass"])), encoding="utf-8")
            with contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(
                    clean_clone_gate.main(["--validate-only", "--commands", str(command_path)]), 0
                )
            self.assertIn('"command_count": 1', output.getvalue())

    def test_clean_checkout_to_other_revision_does_not_validate_original_sha(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            repo, original_sha = self.make_repo(root)
            (repo / "tracked.txt").write_text("next\n")
            git(repo, "commit", "-qam", "next")
            candidate = git(repo, "rev-parse", "HEAD")
            result, _ = clean_clone_gate.run_gate(
                repo, candidate, command_set(["git", "checkout", "--detach", original_sha]), root,
            )
            self.assertEqual(result["result"], "FAIL")
            self.assertEqual(result["failure_classification"], "revision-drift")
            self.assertEqual(git(repo, "rev-parse", "HEAD"), candidate)

    def test_tracked_edit_test_restore_stops_before_unreviewed_code_executes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            repo, sha = self.make_repo(root)
            commands = command_set([sys.executable, "-c", "open('tracked.txt','w').write('changed')"])
            commands["commands"] += [
                {"name": "test-changed", "argv": [sys.executable, "-c", "assert open('tracked.txt').read() == 'changed'"],
                 "cwd": ".", "timeout_seconds": 10},
                {"name": "restore", "argv": ["git", "restore", "tracked.txt"], "cwd": ".", "timeout_seconds": 10},
            ]
            result, _ = clean_clone_gate.run_gate(repo, sha, commands, root)
            self.assertEqual(result["result"], "FAIL")
            self.assertEqual(result["failure_classification"], "source-mutation")
            self.assertEqual(len(result["commands"]), 1)
            self.assertEqual((repo / "tracked.txt").read_text(), "clean\n")

    def test_success_output_retained_externally_matches_receipt_digests(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            repo, sha = self.make_repo(root)
            logs = root / "evidence"
            result, retained = clean_clone_gate.run_gate(
                repo, sha, command_set([sys.executable, "-c", "import sys; print('out'); print('err', file=sys.stderr)"]),
                root, evidence_dir=logs,
            )
            self.assertEqual(result["result"], "PASS")
            self.assertIsNone(retained)
            for stream in ("stdout", "stderr"):
                path = logs / f"probe.{stream}.log"
                self.assertEqual(hashlib.sha256(path.read_bytes()).hexdigest(), result["commands"][0][f"{stream}_sha256"])
                self.assertEqual(path.stat().st_mode & 0o777, 0o600)

    def test_authenticated_and_ambiguous_proxy_values_not_forwarded(self):
        for proxy in ("https://fixture:password@example.invalid:8080", "fixture:password@example.invalid:8080",
                      "https://example.invalid/?token=fixture", "http://example.invalid/#fragment"):
            with self.subTest(proxy_type="unapproved transport"), tempfile.TemporaryDirectory() as directory:
                root = pathlib.Path(directory)
                repo, sha = self.make_repo(root)
                with mock.patch.dict("os.environ", {"HTTPS_PROXY": proxy}), self.assertRaisesRegex(
                    clean_clone_gate.CloneGateError, "authenticated_proxy_requires_separate_ci_configuration",
                ):
                    clean_clone_gate.run_gate(repo, sha, command_set([sys.executable, "-c", "pass"]), root)


if __name__ == "__main__":
    unittest.main()

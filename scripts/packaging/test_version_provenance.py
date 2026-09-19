"""Version-only proposals retain byte-bound provenance and guarded Git writes."""

import base64
import contextlib
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from scripts.packaging import sync_version_proposal as sync
from scripts.packaging import version_provenance as provenance

ROOT = Path(__file__).resolve().parents[2]


class VersionProvenanceTest(unittest.TestCase):
    def setUp(self):
        self.old = provenance.document((ROOT / provenance.IDENTITY).read_bytes())["version"]
        self.new = f"0.1.{int(self.old.split('.')[-1]) + 1}"
        self.proposed = {}
        for path, pointers in provenance.projections(ROOT).items():
            value = provenance.document((ROOT / path).read_bytes())
            for pointer in pointers:
                provenance.replace_pointer(value, pointer, self.old, self.new)
            self.proposed[path] = (json.dumps(value, indent=2) + "\n").encode()

    def plan(self, changed=None):
        with contextlib.redirect_stdout(io.StringIO()):
            return provenance.plan(ROOT, self.proposed, changed or set(self.proposed))

    def test_version_only_change_passes_real_audit_and_is_idempotent(self):
        outputs = self.plan()
        self.assertEqual(set(outputs), provenance.DERIVED)
        with tempfile.TemporaryDirectory() as directory:
            candidate = Path(directory) / "candidate"
            subprocess.run(["git", "clone", "--quiet", "--shared", "--no-hardlinks", str(ROOT), str(candidate)], check=True)
            for path, raw in self.proposed.items():
                (candidate / path).write_bytes(raw)
            before = subprocess.run(["python3", "-B", str(candidate / provenance.AUDIT), "--check"], capture_output=True)
            self.assertNotEqual(before.returncode, 0)
            self.assertIn(b"fingerprint mismatch", before.stderr)
            for path, raw in outputs.items():
                (candidate / path).write_bytes(raw)
            after = subprocess.run(["python3", "-B", str(candidate / provenance.AUDIT), "--check"], capture_output=True)
            self.assertEqual(after.returncode, 0, after.stderr.decode())
        self.proposed.update(outputs)
        self.assertEqual(self.plan(), outputs)

    def test_rejects_dependency_or_script_changes(self):
        for field in ("dependencies", "scripts"):
            with self.subTest(field=field):
                original = self.proposed["frontend/package.json"]
                value = provenance.document(original)
                value[field]["unreviewed"] = "not-a-version-change"
                self.proposed["frontend/package.json"] = json.dumps(value).encode()
                with self.assertRaisesRegex(ValueError, "non-version JSON"):
                    self.plan()
                self.proposed["frontend/package.json"] = original

    def test_rejects_other_source_changes(self):
        with self.assertRaisesRegex(ValueError, "non-version changes"):
            self.plan(set(self.proposed) | {"internal/server/server.go"})

    def test_rejects_unaligned_projection_and_version_jumps(self):
        self.proposed["frontend/packages/desktop/package.json"] = (ROOT / "frontend/packages/desktop/package.json").read_bytes()
        with self.assertRaisesRegex(ValueError, "non-version JSON"):
            self.plan()
        for version in (self.old, "0.2.0", "1.0.0"):
            with self.subTest(version=version):
                self.proposed[provenance.IDENTITY] = json.dumps({"version": version}).encode()
                with self.assertRaisesRegex(ValueError, "one patch"):
                    self.plan()

    def test_duplicate_json_fields_are_rejected(self):
        with self.assertRaisesRegex(ValueError, "duplicate"):
            provenance.document(b'{"version":"0.1.1","version":"0.1.2"}')

    def test_empty_output_does_not_request_access(self):
        with patch.dict("os.environ", {"VERSION_PRS": ""}), patch.object(sync, "synchronize") as update:
            sync.main()
            update.assert_not_called()

    def test_repository_identity_request_has_no_trailing_path_separator(self):
        result = subprocess.CompletedProcess([], 0, '{"id":123}', '')
        with patch.object(sync.subprocess, 'run', return_value=result) as command:
            self.assertEqual(sync.api('owner/product', ''), {'id': 123})
            self.assertEqual(command.call_args.args[0], ['gh', 'api', '--method', 'GET', 'repos/owner/product'])

    def test_publisher_never_forces_or_writes_main(self):
        calls = []
        repo, repo_id, head = "owner/product", 123, "b" * 40
        base = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
        pr = {"state": "open", "head": {"sha": head, "ref": "release-please--branches--main", "repo": {"id": repo_id}},
              "base": {"sha": base, "ref": "main", "repo": {"id": repo_id}}}
        def request(repository, path, method="GET", body=None):
            self.assertEqual(repository, repo)
            calls.append((path, method, body))
            if path == "":
                return {"id": repo_id, "full_name": repo}
            if path == "pulls/24":
                return pr
            if path.startswith("compare/"):
                return {"merge_base_commit": {"sha": base}, "files": [{"filename": p, "status": "modified"} for p in self.proposed]}
            if path.startswith("git/trees/"):
                return {"sha": "c" * 40, "tree": [{"path": p, "sha": p, "type": "blob", "mode": "100644"} for p in self.proposed]}
            if path.startswith("git/blobs/"):
                return {"encoding": "base64", "content": base64.b64encode(self.proposed[path.removeprefix("git/blobs/")]).decode()}
            if path == "git/ref/heads/main":
                return {"object": {"sha": base}}
            if path == "git/trees":
                self.assertEqual({p["path"] for p in body["tree"]}, provenance.DERIVED)
                return {"sha": "d" * 40}
            if path == "git/commits":
                self.assertEqual(body["parents"], [head])
                return {"sha": "e" * 40}
            if method == "PATCH":
                self.assertEqual(path, "git/refs/heads/release-please--branches--main")
                self.assertIs(body["force"], False)
                return {}
            self.fail(path)
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(sync.synchronize(ROOT, repo, repo_id, base, 24, request), "e" * 40)
        self.assertEqual(len([call for call in calls if call[1] == "PATCH"]), 1)
        for race in ("main", "head"):
            with self.subTest(race=race):
                calls.clear()
                def racing_request(repository, path, method="GET", body=None):
                    value = request(repository, path, method, body)
                    if race == "main" and path == "git/ref/heads/main":
                        return {"object": {"sha": "f" * 40}}
                    if race == "head" and path == "pulls/24" and len([c for c in calls if c[0] == path]) == 2:
                        return {"head": {"sha": "f" * 40}}
                    return value
                with contextlib.redirect_stdout(io.StringIO()), self.assertRaisesRegex(ValueError, "changed during verification"):
                    sync.synchronize(ROOT, repo, repo_id, base, 24, racing_request)
                self.assertTrue(all(call[1] == "GET" for call in calls))
        calls.clear()
        pr["head"]["repo"]["id"] = 999
        with self.assertRaisesRegex(ValueError, "expected main baseline"):
            sync.synchronize(ROOT, repo, repo_id, base, 24, request)
        self.assertTrue(all(call[1] == "GET" for call in calls))


if __name__ == "__main__":
    unittest.main()

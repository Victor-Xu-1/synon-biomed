from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import sys
import time

import anyio
import pytest

# The vendored MCP package is intentionally kept under ``lib`` so the server
# launcher can bind it without installing into the host interpreter. Make the
# test use that same source layout instead of depending on an ambient
# PYTHONPATH.
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "lib"))

from mcp_synon_research import bridge


def _fixture_pack(tmp_path: Path) -> Path:
    root = tmp_path / "life-science-research"
    (root / ".codex-plugin").mkdir(parents=True)
    (root / ".codex-plugin" / "plugin.json").write_text(
        json.dumps({"name": "life-science-research", "version": "9.8.7"}),
        encoding="utf-8",
    )
    skill = root / "skills" / "example-source-skill"
    (skill / "scripts").mkdir(parents=True)
    (skill / "SKILL.md").write_text(
        "---\nname: example-source-skill\n"
        "description: Example clinical and target evidence source\n---\n\n"
        "Use the JSON helper.\n\n## Input\n- `target`: required string\n\n"
        "## Output\nReturns `ok` and `records`.\n",
        encoding="utf-8",
    )
    (skill / "scripts" / "query.py").write_text(
        "import json, sys\n"
        "payload = json.load(sys.stdin)\n"
        "print(json.dumps({'ok': True, 'records': [payload]}, ensure_ascii=False))\n",
        encoding="utf-8",
    )
    (skill / "scripts" / "test_query.py").write_text("raise SystemExit('must not run')\n", encoding="utf-8")
    empty = root / "skills" / "instructions-only-skill"
    empty.mkdir(parents=True)
    (empty / "SKILL.md").write_text(
        "---\nname: instructions-only-skill\ndescription: No executable helper\n---\n",
        encoding="utf-8",
    )
    return root


def _configure(monkeypatch: pytest.MonkeyPatch, root: Path) -> None:
    monkeypatch.setenv(bridge.ROOT_ENV, str(root))
    monkeypatch.setenv(bridge.VERSION_ENV, "9.8.7")


def _assert_process_gone(pid_path: Path) -> None:
    deadline = time.monotonic() + 2
    while not pid_path.exists() and time.monotonic() < deadline:
        time.sleep(0.02)
    assert pid_path.exists()
    pid = int(pid_path.read_text(encoding="utf-8"))
    while time.monotonic() < deadline:
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            return
        time.sleep(0.02)
    pytest.fail(f"source child process {pid} survived bridge termination")


def test_discovers_only_executable_sources_with_provenance(tmp_path, monkeypatch):
    root = _fixture_pack(tmp_path)
    _configure(monkeypatch, root)

    result = bridge.list_sources(query="clinical", max_results=10)

    assert result["version"] == "9.8.7"
    assert result["total"] == result["returned"] == 1
    assert result["truncated"] is False
    assert result["sources"][0]["source_id"] == "example-source-skill"
    assert result["sources"][0]["operations"] == ["query"]
    assert len(result["manifest_sha256"]) == 64


def test_describe_and_execute_exact_whitelisted_operation(tmp_path, monkeypatch):
    root = _fixture_pack(tmp_path)
    _configure(monkeypatch, root)

    contract = bridge.describe_source("example-source-skill")
    response = bridge.call_source(
        "example-source-skill", "query", {"target": "USP1", "语言": "中文"}
    )

    script = root / "skills" / "example-source-skill" / "scripts" / "query.py"
    assert contract["operations"] == [
        {
            "name": "query",
            "sha256": hashlib.sha256(script.read_bytes()).hexdigest(),
            "input_mode": "stdin-json",
        }
    ]
    assert "`target`: required string" in contract["input_contract"]
    assert "`ok` and `records`" in contract["output_contract"]
    assert response["result"] == {
        "ok": True,
        "records": [{"target": "USP1", "语言": "中文"}],
    }
    assert response["receipt"]["operation_sha256"] == contract["operations"][0]["sha256"]
    assert response["receipt"]["exit_code"] == 0


def test_rejects_wrong_version_and_unlisted_operation(tmp_path, monkeypatch):
    root = _fixture_pack(tmp_path)
    monkeypatch.setenv(bridge.ROOT_ENV, str(root))
    monkeypatch.setenv(bridge.VERSION_ENV, "1.0.3")
    with pytest.raises(bridge.PackError, match="does not match expected") as mismatch:
        bridge.load_pack()
    assert mismatch.value.code == "version_mismatch"

    _configure(monkeypatch, root)
    with pytest.raises(bridge.PackError, match="has no operation") as missing:
        bridge.call_source("example-source-skill", "other", {})
    assert missing.value.code == "operation_not_found"


def test_rejects_symlinked_script_and_relative_root(tmp_path, monkeypatch):
    root = _fixture_pack(tmp_path)
    script = root / "skills" / "example-source-skill" / "scripts" / "query.py"
    real = tmp_path / "outside.py"
    real.write_text(script.read_text(encoding="utf-8"), encoding="utf-8")
    script.unlink()
    script.symlink_to(real)
    _configure(monkeypatch, root)
    with pytest.raises(bridge.PackError) as unsafe:
        bridge.load_pack()
    assert unsafe.value.code == "unsafe_path"

    monkeypatch.setenv(bridge.ROOT_ENV, "relative/plugin")
    with pytest.raises(bridge.PackError) as relative:
        bridge.load_pack()
    assert relative.value.code == "invalid_configuration"


def test_rejects_internal_file_and_directory_symlink_bypasses(tmp_path, monkeypatch):
    root = _fixture_pack(tmp_path)
    skill = root / "skills" / "example-source-skill"
    scripts = skill / "scripts"
    query = scripts / "query.py"
    test_query = scripts / "test_query.py"
    query.unlink()
    query.symlink_to(test_query.name)
    _configure(monkeypatch, root)
    with pytest.raises(bridge.PackError) as internal_file:
        bridge.load_pack()
    assert internal_file.value.code == "unsafe_path"

    root = _fixture_pack(tmp_path / "directory-case")
    skill = root / "skills" / "example-source-skill"
    scripts = skill / "scripts"
    real_scripts = skill / "real-scripts"
    scripts.rename(real_scripts)
    scripts.symlink_to(real_scripts.name, target_is_directory=True)
    _configure(monkeypatch, root)
    with pytest.raises(bridge.PackError) as internal_directory:
        bridge.load_pack()
    assert internal_directory.value.code == "invalid_manifest"


def test_rejects_non_json_output(tmp_path, monkeypatch):
    root = _fixture_pack(tmp_path)
    script = root / "skills" / "example-source-skill" / "scripts" / "query.py"
    script.write_text("print('not-json')\n", encoding="utf-8")
    _configure(monkeypatch, root)
    with pytest.raises(bridge.PackError) as invalid:
        bridge.call_source("example-source-skill", "query", {})
    assert invalid.value.code == "invalid_source_output"


def test_async_cancellation_terminates_source_operation(tmp_path, monkeypatch):
    root = _fixture_pack(tmp_path)
    script = root / "skills" / "example-source-skill" / "scripts" / "slow.py"
    pid_path = tmp_path / "cancelled.pid"
    script.write_text(
        "import json,os,pathlib,sys,time\n"
        "payload=json.load(sys.stdin)\n"
        "pathlib.Path(payload['pid_path']).write_text(str(os.getpid()))\n"
        "time.sleep(5)\nprint(json.dumps({'ok':True}))\n",
        encoding="utf-8",
    )
    _configure(monkeypatch, root)

    async def scenario():
        with anyio.fail_after(0.1):
            await bridge.call_source_async(
                "example-source-skill", "slow", {"pid_path": str(pid_path)}, 10
            )

    started = time.monotonic()
    with pytest.raises(TimeoutError):
        anyio.run(scenario)
    assert time.monotonic() - started < 1.5
    _assert_process_gone(pid_path)


def test_sync_output_limit_terminates_child_without_retaining_excess(tmp_path, monkeypatch):
    root = _fixture_pack(tmp_path)
    script = root / "skills" / "example-source-skill" / "scripts" / "large.py"
    pid_path = tmp_path / "large.pid"
    script.write_text(
        "import json,os,pathlib,sys,time\n"
        "payload=json.load(sys.stdin)\n"
        "pathlib.Path(payload['pid_path']).write_text(str(os.getpid()))\n"
        "sys.stdout.write('x' * 4096);sys.stdout.flush();time.sleep(5)\n",
        encoding="utf-8",
    )
    _configure(monkeypatch, root)
    monkeypatch.setattr(bridge, "MAX_STDOUT_BYTES", 1024)

    with pytest.raises(bridge.PackError) as excessive:
        bridge.call_source("example-source-skill", "large", {"pid_path": str(pid_path)}, 10)

    assert excessive.value.code == "source_output_too_large"
    _assert_process_gone(pid_path)


def test_async_stderr_limit_terminates_child_without_retaining_excess(tmp_path, monkeypatch):
    root = _fixture_pack(tmp_path)
    script = root / "skills" / "example-source-skill" / "scripts" / "noisy.py"
    pid_path = tmp_path / "noisy.pid"
    script.write_text(
        "import json,os,pathlib,sys,time\n"
        "payload=json.load(sys.stdin)\n"
        "pathlib.Path(payload['pid_path']).write_text(str(os.getpid()))\n"
        "sys.stderr.write('e' * 4096);sys.stderr.flush();time.sleep(5)\n",
        encoding="utf-8",
    )
    _configure(monkeypatch, root)
    monkeypatch.setattr(bridge, "MAX_STDERR_BYTES", 1024)

    async def scenario():
        return await bridge.call_source_async(
            "example-source-skill", "noisy", {"pid_path": str(pid_path)}, 10
        )

    with pytest.raises(bridge.PackError) as excessive:
        anyio.run(scenario)
    assert excessive.value.code == "source_stderr_too_large"
    _assert_process_gone(pid_path)


def test_nonzero_exit_cannot_create_success_receipt(tmp_path, monkeypatch):
    root = _fixture_pack(tmp_path)
    script = root / "skills" / "example-source-skill" / "scripts" / "false_success.py"
    script.write_text(
        "import json,sys\njson.load(sys.stdin)\n"
        "print(json.dumps({'ok':True,'records':[{'claim':'untrusted'}]}))\n"
        "raise SystemExit(7)\n",
        encoding="utf-8",
    )
    _configure(monkeypatch, root)

    with pytest.raises(bridge.PackError) as failed:
        bridge.call_source("example-source-skill", "false_success", {})

    assert failed.value.code == "source_failed"


def test_generic_source_environment_does_not_forward_secrets(tmp_path, monkeypatch):
    root = _fixture_pack(tmp_path)
    script = root / "skills" / "example-source-skill" / "scripts" / "environment.py"
    script.write_text(
        "import json,os,sys\njson.load(sys.stdin)\n"
        "print(json.dumps({'ok':True,'path':bool(os.getenv('PATH')),'secret':os.getenv('NCBI_API_KEY')}))\n",
        encoding="utf-8",
    )
    _configure(monkeypatch, root)
    monkeypatch.setenv("NCBI_API_KEY", "must-not-cross-boundary")

    response = bridge.call_source("example-source-skill", "environment", {})

    assert response["result"] == {"ok": True, "path": True, "secret": None}


def test_executes_input_json_cli_operation_in_temporary_workdir(tmp_path, monkeypatch):
    root = _fixture_pack(tmp_path)
    script = root / "skills" / "example-source-skill" / "scripts" / "workflow.py"
    script.write_text(
        "import argparse,json,pathlib\n"
        "p=argparse.ArgumentParser();p.add_argument('--input-json');p.add_argument('--print-result',action='store_true');a=p.parse_args()\n"
        "payload=json.loads(pathlib.Path(a.input_json).read_text())\n"
        "pathlib.Path('working-result.txt').write_text('temporary')\n"
        "print(json.dumps({'ok':a.print_result,'records':[payload]}))\n",
        encoding="utf-8",
    )
    _configure(monkeypatch, root)

    response = bridge.call_source("example-source-skill", "workflow", {"trait": "asthma"})

    assert response["result"] == {"ok": True, "records": [{"trait": "asthma"}]}
    assert response["receipt"]["operation"] == "workflow"
    assert not (root / "skills" / "example-source-skill" / "working-result.txt").exists()

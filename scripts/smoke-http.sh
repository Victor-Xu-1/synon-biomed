#!/usr/bin/env bash
set -euo pipefail

export NO_PROXY="127.0.0.1,localhost,::1"
export no_proxy="$NO_PROXY"

binary="${1:-dist/synon-go}"

port="$(
	python3 - <<'PY'
import socket

sock = socket.socket()
sock.bind(("127.0.0.1", 0))
print(sock.getsockname()[1])
sock.close()
PY
)"

tmp="$(mktemp -d)"
cfg="$tmp/synon.json"
home="$tmp/home"
adapter_fixture="$tmp/adapter-fixture.py"
plugin_dir="$tmp/plugins/terminal-tools"
plugin_ready="$tmp/terminal-tools.ready"
host_uname="$(uname -s 2>/dev/null || printf unknown)"
host_is_windows=false
case "$host_uname" in
MINGW* | MSYS* | CYGWIN*)
	host_is_windows=true
	;;
esac

cleanup() {
	if [[ -n "${adapter_pid:-}" ]]; then
		kill "$adapter_pid" >/dev/null 2>&1 || true
		wait "$adapter_pid" >/dev/null 2>&1 || true
	fi
	if [[ -n "${pid:-}" ]]; then
		kill "$pid" >/dev/null 2>&1 || true
		wait "$pid" >/dev/null 2>&1 || true
	fi
	if [[ "${host_is_windows:-false}" == true && -n "${plugin_process_script:-}" ]]; then
		plugin_process_script_win="$(cygpath -w "$plugin_process_script" 2>/dev/null || true)"
		tmp_win="$(cygpath -w "$tmp" 2>/dev/null || true)"
		if [[ -n "$plugin_process_script_win" || -n "$tmp_win" ]]; then
			PLUGIN_PROCESS_SCRIPT_WIN="$plugin_process_script_win" PLUGIN_TMP_WIN="$tmp_win" powershell -NoProfile -ExecutionPolicy Bypass -Command "\$script = [Environment]::GetEnvironmentVariable('PLUGIN_PROCESS_SCRIPT_WIN'); \$tmp = [Environment]::GetEnvironmentVariable('PLUGIN_TMP_WIN'); Get-CimInstance Win32_Process | Where-Object { (\$script -and \$_.CommandLine -like ('*' + \$script + '*')) -or (\$tmp -and \$_.CommandLine -like ('*' + \$tmp + '*')) } | ForEach-Object { Stop-Process -Id \$_.ProcessId -Force -ErrorAction SilentlyContinue }" >/dev/null 2>&1 || true
		fi
	fi
	rm -rf "$tmp" || {
		sleep 0.5
		rm -rf "$tmp"
	}
}
trap cleanup EXIT

adapter_port="$(
	python3 - <<'PY'
import socket

sock = socket.socket()
sock.bind(("127.0.0.1", 0))
print(sock.getsockname()[1])
sock.close()
PY
)"
cat >"$adapter_fixture" <<'PY'
import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_args):
        return

    def write_json(self, status, payload):
        body = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/health":
            self.write_json(200, {"ok": True})
            return
        self.write_json(404, {"error": "not_found"})

    def adapter_response(self):
        length = int(self.headers.get("Content-Length", "0"))
        if length:
            self.rfile.read(length)
        path = self.path.split("?", 1)[0]
        if path == "/open-apis/auth/v3/tenant_access_token/internal":
            payload = {"code": 0, "tenant_access_token": "smoke-tenant-token", "expire": 3600}
        elif path == "/open-apis/cardkit/v1/cards":
            payload = {"code": 0, "data": {"card_id": "smoke-card"}}
        elif path.startswith("/open-apis/im/v1/messages"):
            payload = {"code": 0, "data": {"message_id": "smoke-message"}}
        elif path == "/ilink/bot/sendmessage":
            payload = {"ret": 0}
        else:
            payload = {"code": 0}
        self.write_json(200, payload)

    do_POST = adapter_response
    do_PUT = adapter_response
    do_PATCH = adapter_response


ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1])), Handler).serve_forever()
PY
python3 "$adapter_fixture" "$adapter_port" >"$tmp/adapter-fixture.log" 2>&1 &
adapter_pid="$!"
adapter_origin="http://127.0.0.1:$adapter_port"
for _ in $(seq 1 40); do
	if curl -fsS "$adapter_origin/health" >/dev/null 2>&1; then
		break
	fi
	sleep 0.05
	if ! kill -0 "$adapter_pid" >/dev/null 2>&1; then
		cat "$tmp/adapter-fixture.log"
		exit 1
	fi
done
curl -fsS "$adapter_origin/health" >/dev/null

mkdir -p "$plugin_dir/.synon-plugin"
if [[ "$host_is_windows" == true ]]; then
	mkdir -p "$plugin_dir/bin"
	plugin_process_command="cmd.exe"
	plugin_process_script="$plugin_dir/bin/terminal-tools.cmd"
	plugin_ready_host="$(cygpath -w "$plugin_ready")"
	cat >"$plugin_process_script" <<'CMD'
@echo off
setlocal
> "%~1" echo ready
cd /d "%TEMP%" >nul 2>&1
:loop
ping -n 2 127.0.0.1 >nul
goto loop
CMD
	plugin_process_json="$(MSYS_NO_PATHCONV=1 python3 -c 'import json,sys; print(json.dumps({"command":sys.argv[1],"args":sys.argv[2:]}))' "$plugin_process_command" "/C" "$(cygpath -w "$plugin_process_script")" "$plugin_ready_host")"
else
	plugin_process_json="$(python3 -c 'import json,sys; print(json.dumps({"command":"/bin/sh","args":["-c", "printf ready >\"" + sys.argv[1] + "\"; while true; do sleep 1; done"]}))' "$plugin_ready")"
fi
cat >"$plugin_dir/.synon-plugin/plugin.json" <<JSON
{
  "id": "terminal-tools",
  "name": "terminal-tools",
  "version": "0.2.0",
  "description": "Smoke-test external plugin process",
  "author": {"name": "Smoke"},
  "license": "MIT",
  "server": {
    "api": {"mount": "/api/plugins/terminal-tools", "entry": "./server/api.go"},
    "context": {"entry": "./server/context.go"},
    "process": $plugin_process_json
  }
}
JSON

plugin_dir_json="$(python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$plugin_dir")"
# Adapter credentials and origins are isolated fixture values; no external
# platform or user account is contacted by this HTTP smoke.
printf '{"host":"127.0.0.1","port":%s,"synon_link_package":"assets/synon-link/synon-link-extension-v0.6.10.zip","plugin_directories":[%s],"enabled_adapters":["feishu","wechat"],"feishu":{"app_id":"smoke-app","app_secret":"smoke-secret","domain":"%s"},"wechat":{"account_id":"smoke-account","bot_token":"smoke-token","base_url":"%s"}}\n' "$port" "$plugin_dir_json" "$adapter_origin" "$adapter_origin" >"$cfg"
mkdir -p "$home/smoke" "$home/projects/alpha"
if [[ "$host_is_windows" == true ]]; then
	shell_expected_workdir="$(cygpath -w "$home")"
	tool_shell_payload="$(python3 -c 'import json; print(json.dumps({"input":{"command":"cmd.exe","args":["/C","cd"],"workdir":".","timeout":2}}))')"
else
	shell_expected_workdir="$home"
	tool_shell_payload='{"input":{"command":"/bin/pwd","workdir":".","timeout":2}}'
fi

SYNON_HOME="$home" SYNON_CONFIG="$cfg" "$binary" >"$tmp/server.log" 2>&1 &
pid="$!"

for _ in $(seq 1 40); do
	if curl -fsS "http://127.0.0.1:$port/health" >/dev/null 2>&1; then
		break
	fi
	sleep 0.1
	if ! kill -0 "$pid" >/dev/null 2>&1; then
		cat "$tmp/server.log"
		exit 1
	fi
done

health="$(curl -fsS "http://127.0.0.1:$port/health")"
plugins="$(curl -fsS "http://127.0.0.1:$port/api/plugins")"
plugin_manifest="$(curl -fsS "http://127.0.0.1:$port/api/plugins/synon/manifest")"
external_plugin_manifest="$(curl -fsS "http://127.0.0.1:$port/api/plugins/terminal-tools/manifest")"
capabilities="$(curl -fsS "http://127.0.0.1:$port/api/plugins/synon/capabilities")"
link_capabilities="$(curl -fsS "http://127.0.0.1:$port/api/synon-link/capabilities")"
link_doctor="$(curl -fsS "http://127.0.0.1:$port/api/synon-link/doctor?userId=smoke-user")"
link_package="$tmp/synon-link-extension.zip"
curl -fsS "http://127.0.0.1:$port/api/plugins/synon/link/download" -o "$link_package"
printf '2cb251ad8aedf940f6d3a11b41b74ed55f9b912ab48257a21789b78a1ca0c90b  %s\n' "$link_package" | sha256sum -c -
unzip -p "$link_package" manifest.json | grep -q '"version": "0.6.10"'
tools_file="$tmp/tools.json"
curl -fsS "http://127.0.0.1:$port/api/tools" -o "$tools_file"
tool_fetch_file="$tmp/tool-fetch-loopback.json"
tool_fetch_code="$(curl -sS -o "$tool_fetch_file" -w '%{http_code}' -X POST "http://127.0.0.1:$port/api/tools/web_fetch/execute" -H 'Content-Type: application/json' --data "{\"input\":{\"url\":\"http://127.0.0.1:$port/health\",\"limit\":20}}")"
tool_fetch="$(cat "$tool_fetch_file")"
tool_file_write="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/file_write/execute" -H 'Content-Type: application/json' --data '{"input":{"path":"smoke/notes.txt","content":"file tool smoke"}}')"
tool_file_list="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/file_list/execute" -H 'Content-Type: application/json' --data '{"input":{"path":"smoke"}}')"
tool_file_info="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/file_info/execute" -H 'Content-Type: application/json' --data '{"input":{"path":"smoke/notes.txt"}}')"
tool_file_read="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/file_read/execute" -H 'Content-Type: application/json' --data '{"input":{"path":"smoke/notes.txt","limit":30}}')"
tool_file_read_batch="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/file_read_batch/execute" -H 'Content-Type: application/json' --data '{"input":{"file_paths":["smoke/notes.txt","smoke/missing.txt"],"max_bytes_per_file":20,"max_files":5}}')"
tool_write_original="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/Write/execute" -H 'Content-Type: application/json' --data '{"input":{"file_path":"smoke/original.txt","content":"original file tool\nsecond line\n"}}')"
tool_read_original="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/Read/execute" -H 'Content-Type: application/json' --data '{"input":{"file_path":"smoke/original.txt","offset":2,"limit":1}}')"
tool_read_original_full="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/Read/execute" -H 'Content-Type: application/json' --data '{"input":{"file_path":"smoke/original.txt"}}')"
tool_edit_original="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/Edit/execute" -H 'Content-Type: application/json' --data '{"input":{"file_path":"smoke/original.txt","old_string":"second","new_string":"SECOND","replace_all":false}}')"
tool_read_batch_original="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/ReadBatch/execute" -H 'Content-Type: application/json' --data '{"input":{"file_paths":["smoke/original.txt","smoke/notes.txt"],"max_bytes_per_file":80,"max_files":2}}')"
tool_patch_original="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/Patch/execute" -H 'Content-Type: application/json' --data '{"input":{"patch":"--- a/smoke/original.txt\n+++ b/smoke/original.txt\n@@ -1,2 +1,2 @@\n original file tool\n-SECOND line\n+PATCHED line\n"}}')"
tool_file_write_binary="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/file_write/execute" -H 'Content-Type: application/json' --data '{"input":{"path":"smoke/blob.bin","content":"AAFC","encoding":"base64","overwrite":false}}')"
tool_file_read_binary="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/file_read/execute" -H 'Content-Type: application/json' --data '{"input":{"path":"smoke/blob.bin","encoding":"base64","limit":3}}')"
tool_file_mkdir="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/file_mkdir/execute" -H 'Content-Type: application/json' --data '{"input":{"path":"smoke/archive","recursive":true}}')"
tool_file_copy="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/file_copy/execute" -H 'Content-Type: application/json' --data '{"input":{"path":"smoke/notes.txt","targetPath":"smoke/archive/copied.txt"}}')"
tool_file_move="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/file_move/execute" -H 'Content-Type: application/json' --data '{"input":{"path":"smoke/archive/copied.txt","targetPath":"smoke/archive/moved.txt"}}')"
tool_file_delete="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/file_delete/execute" -H 'Content-Type: application/json' --data '{"input":{"path":"smoke/archive/moved.txt"}}')"
tool_file_search="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/file_search/execute" -H 'Content-Type: application/json' --data '{"input":{"path":"smoke","query":"file tool","limit":10}}')"
tool_glob="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/Glob/execute" -H 'Content-Type: application/json' --data '{"input":{"path":"smoke","pattern":"*.txt"}}')"
tool_file_replace="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/file_replace/execute" -H 'Content-Type: application/json' --data '{"input":{"path":"smoke/notes.txt","old":"smoke","new":"verified"}}')"
tool_file_patch="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/file_patch/execute" -H 'Content-Type: application/json' --data '{"input":{"path":"smoke/notes.txt","operations":[{"type":"append","content":"\npatched\n"}]}}')"
tool_file_write_code="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/file_write/execute" -H 'Content-Type: application/json' --data '{"input":{"path":"smoke/code.go","content":"package smoke\n\ntype Runner struct{}\n\nfunc Run() {}\n"}}')"
tool_grep="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/Grep/execute" -H 'Content-Type: application/json' --data '{"input":{"path":"smoke","pattern":"Run","glob":"**/*.go","output_mode":"content","head_limit":10}}')"
tool_code_index="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/code_index/execute" -H 'Content-Type: application/json' --data '{"input":{"path":"smoke","limit":10}}')"
tool_shell="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/shell_exec/execute" -H 'Content-Type: application/json' --data "$tool_shell_payload")"
tool_shell_original="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/Shell/execute" -H 'Content-Type: application/json' --data '{"input":{"command":"printf shell-original","workdir":".","timeout":2000,"description":"Print original Shell output"}}')"
tool_bash_original="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/Bash/execute" -H 'Content-Type: application/json' --data '{"input":{"command":"printf bash-original","workdir":".","timeout":2000,"description":"Print original Bash output"}}')"
tool_sleep="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/Sleep/execute" -H 'Content-Type: application/json' --data '{"input":{"durationMs":1}}')"
tool_ask_user_question="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/ask_user/execute" -H 'Content-Type: application/json' --data '{"input":{"question":"继续验证哪个工具？","header":"工具","options":[{"label":"Sleep","description":"复核已经完成的等待调用。","pros":"可以确认最小等待调用的完整返回。","cons":"不覆盖任务状态写入。","readiness":"等待调用回执不证明其他执行能力就绪。","readiness_status":"unverified","decision_evidence":["tool-call:smoke-sleep"],"readiness_evidence":[],"selection_basis":"scientific_evidence","requirements":"无需额外资源。","expected_outcome":"得到可审计的等待调用结果。","selection_rationale":"推荐先复核已经产生真实回执的调用。","recommended":true},{"label":"TodoWrite","description":"继续验证任务状态写入契约。","pros":"覆盖任务状态的结构化写入。","cons":"需要执行后续写入调用。","readiness":"尚未执行本任务的状态写入核验。","readiness_status":"unverified","decision_evidence":["user-input:current-task"],"readiness_evidence":[],"selection_basis":"user_objective","requirements":"需要继续执行本地状态写入冒烟调用。","expected_outcome":"得到可审计的任务状态结果。","selection_rationale":"在需要优先核验任务状态写入时选择。","recommended":false}],"multi_select":false}}')"
tool_task_create="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/task_create/execute" -H 'Content-Type: application/json' --data '{"input":{"title":"smoke task"}}')"
task_id="$(printf '%s' "$tool_task_create" | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["task"]["id"])')"
tool_task_update="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/task_update/execute" -H 'Content-Type: application/json' --data "{\"input\":{\"id\":\"$task_id\",\"status\":\"done\"}}")"
tool_task_list="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/task_list/execute" -H 'Content-Type: application/json' --data '{"input":{}}')"
tool_task_create_original="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/TaskCreate/execute" -H 'Content-Type: application/json' --data '{"input":{"subject":"smoke original task","description":"Verify original TaskCreate contract","activeForm":"Verifying original task contract","metadata":{"source":"smoke"}}}')"
original_task_id="$(printf '%s' "$tool_task_create_original" | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["task"]["id"])')"
tool_task_update_original="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/TaskUpdate/execute" -H 'Content-Type: application/json' --data "{\"input\":{\"taskId\":\"$original_task_id\",\"status\":\"completed\",\"owner\":\"smoke-agent\"}}")"
tool_task_get_original="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/TaskGet/execute" -H 'Content-Type: application/json' --data "{\"input\":{\"taskId\":\"$original_task_id\"}}")"
tool_task_list_original="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/TaskList/execute" -H 'Content-Type: application/json' --data '{"input":{}}')"
tool_task_output="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/TaskOutput/execute" -H 'Content-Type: application/json' --data "{\"input\":{\"task_id\":\"$original_task_id\",\"block\":true,\"timeout\":10}}")"
tool_task_output_alias="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/AgentOutputTool/execute" -H 'Content-Type: application/json' --data "{\"input\":{\"task_id\":\"$original_task_id\"}}")"
tool_task_stop_created="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/TaskCreate/execute" -H 'Content-Type: application/json' --data '{"input":{"subject":"smoke stoppable task","description":"Verify original TaskStop contract","metadata":{"command":"smoke-long-task"}}}')"
task_stop_id="$(printf '%s' "$tool_task_stop_created" | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["task"]["id"])')"
tool_task_stop_running="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/TaskUpdate/execute" -H 'Content-Type: application/json' --data "{\"input\":{\"taskId\":\"$task_stop_id\",\"status\":\"running\"}}")"
tool_task_stop="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/TaskStop/execute" -H 'Content-Type: application/json' --data "{\"input\":{\"task_id\":\"$task_stop_id\"}}")"
tool_task_stop_alias_status="$(curl -sS -o "$tmp/kill-shell-status.json" -w "%{http_code}" -X POST "http://127.0.0.1:$port/api/tools/KillShell/execute" -H 'Content-Type: application/json' --data "{\"input\":{\"shell_id\":\"$task_stop_id\"}}")"
tool_todo_write="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/TodoWrite/execute" -H 'Content-Type: application/json' --data '{"input":{"sessionId":"smoke-session","todos":[{"content":"run smoke TodoWrite","status":"in_progress","activeForm":"Running smoke TodoWrite"}]}}')"
tool_todo_write_done="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/todo_write/execute" -H 'Content-Type: application/json' --data '{"input":{"sessionId":"smoke-session","todos":[{"content":"run smoke TodoWrite","status":"completed","activeForm":"Running smoke TodoWrite"}]}}')"
tool_settings_set="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/settings_set/execute" -H 'Content-Type: application/json' --data '{"input":{"key":"theme","value":"dark"}}')"
tool_settings_get="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/settings_get/execute" -H 'Content-Type: application/json' --data '{"input":{"key":"theme"}}')"
tool_settings_list="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/settings_list/execute" -H 'Content-Type: application/json' --data '{"input":{}}')"
tool_config_set="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/Config/execute" -H 'Content-Type: application/json' --data '{"input":{"setting":"verbose","value":"true"}}')"
tool_config_get="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/Config/execute" -H 'Content-Type: application/json' --data '{"input":{"setting":"verbose"}}')"
tool_search_select="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/ToolSearch/execute" -H 'Content-Type: application/json' --data '{"input":{"query":"select:Config,TaskUpdate","max_results":5}}')"
tool_search_keyword="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/ToolSearch/execute" -H 'Content-Type: application/json' --data '{"input":{"query":"task metadata owner","max_results":3}}')"
tool_brief="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/SendUserMessage/execute" -H 'Content-Type: application/json' --data '{"input":{"message":"smoke brief","status":"normal","attachments":["smoke/notes.txt"]}}')"
tool_doctor="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/ToolDoctor/execute" -H 'Content-Type: application/json' --data '{"input":{"scope":"tools","smoke":true}}')"
tool_runtime_set="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/runtime_set/execute" -H 'Content-Type: application/json' --data '{"input":{"namespace":"agent","key":"last_goal","value":{"status":"running","source":"smoke"}}}')"
tool_runtime_get="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/runtime_get/execute" -H 'Content-Type: application/json' --data '{"input":{"namespace":"agent","key":"last_goal"}}')"
tool_runtime_list="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/runtime_list/execute" -H 'Content-Type: application/json' --data '{"input":{"namespace":"agent"}}')"
tool_runtime_delete="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/runtime_delete/execute" -H 'Content-Type: application/json' --data '{"input":{"namespace":"agent","key":"last_goal"}}')"
feishu_challenge="$(curl -fsS -X POST "http://127.0.0.1:$port/api/adapters/feishu/event" -H 'Content-Type: application/json' --data '{"type":"url_verification","challenge":"smoke-feishu-challenge"}')"
feishu_payload='{"header":{"event_id":"evt-smoke-feishu","event_type":"im.message.receive_v1"},"event":{"sender":{"sender_id":{"open_id":"ou_smoke"}},"message":{"message_id":"om_smoke","chat_id":"oc_smoke","chat_type":"p2p","message_type":"text","content":"{\"text\":\"smoke Feishu event task\"}"}}}'
feishu_pairing="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/pairing_allow/execute" -H 'Content-Type: application/json' --data '{"input":{"platform":"feishu","userId":"ou_smoke","displayName":"Smoke"}}')"
feishu_event="$(curl -fsS -X POST "http://127.0.0.1:$port/api/adapters/feishu/event" -H 'Content-Type: application/json' --data "$feishu_payload")"
feishu_duplicate="$(curl -fsS -X POST "http://127.0.0.1:$port/api/adapters/feishu/event" -H 'Content-Type: application/json' --data "$feishu_payload")"
wechat_payload='{"message_id":30001,"seq":3,"from_user_id":"wx-smoke","create_time_ms":1710000000000,"context_token":"ctx-smoke","item_list":[{"type":1,"msg_id":"txt-smoke","text_item":{"text":"smoke WeChat event task"}}]}'
wechat_pairing="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/pairing_allow/execute" -H 'Content-Type: application/json' --data '{"input":{"platform":"wechat","userId":"wx-smoke","displayName":"Smoke"}}')"
wechat_event="$(curl -fsS -X POST "http://127.0.0.1:$port/api/adapters/wechat/event" -H 'Content-Type: application/json' --data "$wechat_payload")"
wechat_duplicate="$(curl -fsS -X POST "http://127.0.0.1:$port/api/adapters/wechat/event" -H 'Content-Type: application/json' --data "$wechat_payload")"
feishu_session_id="$(printf '%s' "$feishu_event" | python3 -c 'import json,sys; print(json.load(sys.stdin)["sessionId"])')"
session_runner_pick="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/session_runner_pick/execute" -H 'Content-Type: application/json' --data '{"input":{"runnerId":"runner-pick","ttlSeconds":120,"afterEventId":0,"limit":10}}')"
session_runner_queue="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/session_runner_queue/execute" -H 'Content-Type: application/json' --data '{"input":{}}')"
session_runner_pick_session_id="$(printf '%s' "$session_runner_pick" | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["session"]["id"])')"
session_runner_pick_attempt="$(printf '%s' "$session_runner_pick" | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["runnerAttempt"])')"
session_runner_pick_token="$(printf '%s' "$session_runner_pick" | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["claimToken"])')"
session_runner_pick_release="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/session_release/execute" -H 'Content-Type: application/json' --data "{\"input\":{\"sessionId\":\"$session_runner_pick_session_id\",\"runnerId\":\"runner-pick\",\"runnerAttempt\":$session_runner_pick_attempt,\"claimToken\":\"$session_runner_pick_token\"}}")"
session_list="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/session_list/execute" -H 'Content-Type: application/json' --data '{"input":{}}')"
session_get="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/session_get/execute" -H 'Content-Type: application/json' --data "{\"input\":{\"sessionId\":\"$feishu_session_id\"}}")"
session_replay="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/session_replay/execute" -H 'Content-Type: application/json' --data "{\"input\":{\"sessionId\":\"$feishu_session_id\",\"afterEventId\":0,\"limit\":10}}")"
session_bind_project="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/session_bind_project/execute" -H 'Content-Type: application/json' --data "{\"input\":{\"sessionId\":\"$feishu_session_id\",\"projectId\":\"alpha\",\"projectName\":\"Alpha Project\",\"path\":\"projects/alpha\"}}")"
session_claim_a="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/session_claim/execute" -H 'Content-Type: application/json' --data "{\"input\":{\"sessionId\":\"$feishu_session_id\",\"runnerId\":\"runner-a\",\"ttlSeconds\":120}}")"
session_claim_a_attempt="$(printf '%s' "$session_claim_a" | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["runnerAttempt"])')"
session_claim_a_token="$(printf '%s' "$session_claim_a" | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["claimToken"])')"
session_claim_b_conflict="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/session_claim/execute" -H 'Content-Type: application/json' --data "{\"input\":{\"sessionId\":\"$feishu_session_id\",\"runnerId\":\"runner-b\",\"ttlSeconds\":120}}")"
session_heartbeat_a="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/session_heartbeat/execute" -H 'Content-Type: application/json' --data "{\"input\":{\"sessionId\":\"$feishu_session_id\",\"runnerId\":\"runner-a\",\"runnerAttempt\":$session_claim_a_attempt,\"claimToken\":\"$session_claim_a_token\",\"ttlSeconds\":180}}")"
session_append_wrong_file="$tmp/session-append-wrong.json"
session_append_wrong_payload="$(python3 -c 'import json,sys; print(json.dumps({"input":{"sessionId":sys.argv[1],"runnerId":"runner-b","runnerAttempt":int(sys.argv[2]),"claimToken":sys.argv[3],"role":"assistant","message":{"type":"assistant_message","text":"wrong runner must not append"},"clientMessageId":"smoke-session-append-wrong"}}))' "$feishu_session_id" "$session_claim_a_attempt" "$session_claim_a_token")"
session_append_wrong_code="$(curl -sS -o "$session_append_wrong_file" -w '%{http_code}' -X POST "http://127.0.0.1:$port/api/tools/session_append/execute" -H 'Content-Type: application/json' --data "$session_append_wrong_payload")"
session_append_wrong="$(cat "$session_append_wrong_file")"
session_append_payload="$(python3 -c 'import json,sys; print(json.dumps({"input":{"sessionId":sys.argv[1],"runnerId":"runner-a","runnerAttempt":int(sys.argv[2]),"claimToken":sys.argv[3],"role":"assistant","message":{"type":"assistant_message","text":"smoke session replay ready"},"clientMessageId":"smoke-session-append"}}))' "$feishu_session_id" "$session_claim_a_attempt" "$session_claim_a_token")"
session_append="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/session_append/execute" -H 'Content-Type: application/json' --data "$session_append_payload")"
session_replay_after="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/session_replay/execute" -H 'Content-Type: application/json' --data "{\"input\":{\"sessionId\":\"$feishu_session_id\",\"afterEventId\":1,\"limit\":10}}")"
session_release_b_wrong_file="$tmp/session-release-wrong.json"
session_release_b_wrong_code="$(curl -sS -o "$session_release_b_wrong_file" -w '%{http_code}' -X POST "http://127.0.0.1:$port/api/tools/session_release/execute" -H 'Content-Type: application/json' --data "{\"input\":{\"sessionId\":\"$feishu_session_id\",\"runnerId\":\"runner-b\",\"runnerAttempt\":$session_claim_a_attempt,\"claimToken\":\"$session_claim_a_token\"}}")"
session_release_b_wrong="$(cat "$session_release_b_wrong_file")"
session_release_a="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/session_release/execute" -H 'Content-Type: application/json' --data "{\"input\":{\"sessionId\":\"$feishu_session_id\",\"runnerId\":\"runner-a\",\"runnerAttempt\":$session_claim_a_attempt,\"claimToken\":\"$session_claim_a_token\"}}")"
session_append_released_file="$tmp/session-append-released.json"
session_append_released_payload="$(python3 -c 'import json,sys; print(json.dumps({"input":{"sessionId":sys.argv[1],"runnerId":"runner-a","runnerAttempt":int(sys.argv[2]),"claimToken":sys.argv[3],"role":"assistant","message":{"type":"assistant_message","text":"released runner must not append"},"clientMessageId":"smoke-session-append-released"}}))' "$feishu_session_id" "$session_claim_a_attempt" "$session_claim_a_token")"
session_append_released_code="$(curl -sS -o "$session_append_released_file" -w '%{http_code}' -X POST "http://127.0.0.1:$port/api/tools/session_append/execute" -H 'Content-Type: application/json' --data "$session_append_released_payload")"
session_append_released="$(cat "$session_append_released_file")"
session_claim_b="$(curl -fsS -X POST "http://127.0.0.1:$port/api/tools/session_claim/execute" -H 'Content-Type: application/json' --data "{\"input\":{\"sessionId\":\"$feishu_session_id\",\"runnerId\":\"runner-b\",\"ttlSeconds\":120}}")"

printf 'PORT=%s\n' "$port"
printf 'HEALTH=%s\n' "$health"

printf '%s' "$plugins" | grep -q '"id":"synon"'
printf '%s' "$plugins" | grep -q '"id":"terminal-tools"'
printf '%s' "$plugins" | grep -q '"status":"running"'
printf '%s' "$plugins" | grep -q '/api/plugins/synon/capabilities'
printf '%s' "$plugins" | grep -qi 'admet' && exit 1
printf '%s' "$plugin_manifest" | grep -q '"pluginFormat":"synon_agent_native"'
printf '%s' "$plugin_manifest" | grep -q '"apiMount":"/api/plugins/synon"'
printf '%s' "$external_plugin_manifest" | grep -q '"pluginFormat":"synon_agent_external"'
printf '%s' "$external_plugin_manifest" | grep -q '"apiMount":"/api/plugins/terminal-tools"'
test -f "$plugin_ready"
printf 'PLUGIN_HOST_API=yes\n'
printf 'PLUGIN_HOST_EXTERNAL_PROCESS=yes\n'

printf '%s' "$capabilities" | grep -q 'search_web'
printf 'CAPABILITIES_HAS_SEARCH_WEB=yes\n'

printf '%s' "$capabilities" | grep -q 'receive_feishu_event'
printf '%s' "$capabilities" | grep -q 'build_feishu_stream_router'
printf '%s' "$capabilities" | grep -q 'start_feishu_wsclient'
printf '%s' "$capabilities" | grep -q 'route_feishu_stream_message'
printf '%s' "$capabilities" | grep -q 'connect_adapter_ws_session'
printf '%s' "$capabilities" | grep -q 'reconnect_adapter_ws_session'
printf '%s' "$capabilities" | grep -q 'send_adapter_ws_heartbeat'
printf '%s' "$capabilities" | grep -q 'track_adapter_ws_pong'
printf '%s' "$capabilities" | grep -q 'send_adapter_user_message'
printf '%s' "$capabilities" | grep -q 'send_adapter_permission_response'
printf '%s' "$capabilities" | grep -q 'serialize_adapter_server_messages'
printf '%s' "$capabilities" | grep -q 'create_im_live_session'
printf '%s' "$capabilities" | grep -q 'journal_im_inbound_message'
printf '%s' "$capabilities" | grep -q 'create_feishu_card_entity'
printf '%s' "$capabilities" | grep -q 'send_feishu_card_message'
printf '%s' "$capabilities" | grep -q 'stream_feishu_card_content'
printf '%s' "$capabilities" | grep -q 'process_feishu_server_message'
printf '%s' "$capabilities" | grep -q 'stream_feishu_server_content_delta'
printf '%s' "$capabilities" | grep -q 'finalize_feishu_server_message'
printf '%s' "$capabilities" | grep -q 'abort_feishu_server_message'
printf '%s' "$capabilities" | grep -q 'dispatch_feishu_delta_media'
printf '%s' "$capabilities" | grep -q 'build_feishu_rendered_card'
printf '%s' "$capabilities" | grep -q 'sanitize_feishu_card_tables'
printf '%s' "$capabilities" | grep -q 'throttle_feishu_card_flush'
printf '%s' "$capabilities" | grep -q 'append_feishu_card_reasoning'
printf '%s' "$capabilities" | grep -q 'start_feishu_card_tool_step'
printf '%s' "$capabilities" | grep -q 'complete_feishu_card_tool_step'
printf '%s' "$capabilities" | grep -q 'finalize_feishu_streaming_card'
printf '%s' "$capabilities" | grep -q 'abort_feishu_streaming_card'
printf '%s' "$capabilities" | grep -q 'watch_feishu_outbound_images'
printf '%s' "$capabilities" | grep -q 'watch_feishu_outbound_files'
printf '%s' "$capabilities" | grep -q 'filter_feishu_unsafe_local_uploads'
printf '%s' "$capabilities" | grep -q 'upload_feishu_image'
printf '%s' "$capabilities" | grep -q 'upload_feishu_file'
printf '%s' "$capabilities" | grep -q 'send_feishu_image_message'
printf '%s' "$capabilities" | grep -q 'send_feishu_file_message'
printf '%s' "$capabilities" | grep -q 'dispatch_feishu_outbound_image'
printf '%s' "$capabilities" | grep -q 'dispatch_feishu_outbound_file'
printf '%s' "$capabilities" | grep -q 'update_feishu_card'
printf '%s' "$capabilities" | grep -q 'send_wechat_text'
printf '%s' "$capabilities" | grep -q 'start_wechat_qr_login'
printf '%s' "$capabilities" | grep -q 'poll_wechat_qr_login'
printf '%s' "$capabilities" | grep -q 'get_wechat_updates'
printf '%s' "$capabilities" | grep -q 'start_wechat_polling_adapter'
printf '%s' "$capabilities" | grep -q 'run_wechat_polling_loop'
printf '%s' "$capabilities" | grep -q 'receive_wechat_event'
printf 'CAPABILITIES_HAS_IM_CORE=yes\n'

printf '%s' "$capabilities" | grep -q 'load_external_plugin_manifest'
printf '%s' "$capabilities" | grep -q 'start_external_plugin_process'
printf '%s' "$capabilities" | grep -q 'stop_external_plugin_process'
printf 'CAPABILITIES_HAS_EXTERNAL_PLUGIN_HOST=yes\n'

printf '%s' "$capabilities" | grep -q 'upsert_session'
printf '%s' "$capabilities" | grep -q 'append_session_event'
printf 'CAPABILITIES_HAS_RUNTIME_STORE=yes\n'

if printf '%s' "$link_capabilities" | grep -q 'desktop_'; then
	echo "Synon Link capabilities still expose removed desktop actions" >&2
	exit 1
fi
printf '%s' "$link_capabilities" | grep -q 'open_tab'
printf '%s' "$link_capabilities" | grep -q 'scroll_page'
printf '%s' "$link_capabilities" | grep -q 'click_at'
printf '%s' "$link_capabilities" | grep -q 'screenshot'
printf '%s' "$link_capabilities" | grep -q 'download_file'
printf '%s' "$link_capabilities" | grep -q 'bookmarks_profile'
printf 'SYNON_LINK_BROWSER_CAPABILITIES=yes\n'
printf 'SYNON_LINK_CAPABILITIES=yes\n'

printf '%s' "$link_doctor" | grep -q '"ok":true'
printf '%s' "$link_doctor" | grep -q 'policyMatrix'
printf 'SYNON_LINK_DOCTOR=yes\n'

python3 - "$tools_file" <<'PY'
import json
import pathlib
import sys

payload = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
names = {tool["name"] for tool in payload["tools"]}
required = {
    "web_fetch", "WebSearch", "file_info", "file_read", "Read",
    "file_read_batch", "ReadBatch", "file_write", "Write", "file_mkdir",
    "file_copy", "file_move", "file_delete", "file_search", "Glob", "Grep",
    "file_replace", "Edit", "file_patch", "Patch", "NotebookEdit", "code_index",
    "shell_exec", "Bash", "Shell", "PowerShell", "Sleep", "sleep", "ask_user",
    "SendMessage", "Agent", "Task", "generate_plan", "task_create", "TaskRun",
    "TaskCreate", "TaskGet", "TodoWrite", "TaskUpdate", "TaskList", "TaskOutput",
    "AgentOutputTool", "BashOutputTool", "TaskStop", "KillShell", "todo_write",
    "settings_set", "Config", "ToolSearch", "LSP", "SendUserMessage", "Brief",
    "ToolDoctor", "ListMcpTools", "ListMcpResourcesTool", "ReadMcpResourceTool",
    "MCPTool", "session_list", "session_get", "session_replay", "session_append",
    "session_runner_pick", "session_runner_queue", "session_bind_project",
    "runtime_set", "runtime_get", "runtime_list", "runtime_delete",
}
missing = sorted(required - names)
if missing:
    raise SystemExit(f"required tools are missing: {missing}")
forbidden = {"AskUserQuestion", "ask_user_question", "EnterPlanMode", "ExitPlanMode"}
advertised = sorted(forbidden & names)
if advertised:
    raise SystemExit(f"retired tools are advertised: {advertised}")
if any("admet" in name.lower() for name in names):
    raise SystemExit("retired ADMET tool is advertised")
PY
[[ "$tool_fetch_code" == "400" ]]
printf '%s' "$tool_fetch" | grep -q '"error"'
printf '%s' "$tool_file_write" | grep -q '"ok":true'
printf '%s' "$tool_file_list" | grep -q 'notes.txt'
printf '%s' "$tool_file_info" | grep -q '"sha256"'
printf '%s' "$tool_file_read" | grep -q 'file tool smoke'
printf '%s' "$tool_file_read_batch" | grep -q '"filePath":"smoke/notes.txt"'
printf '%s' "$tool_file_read_batch" | grep -q '"reason":"not_found"'
printf '%s' "$tool_write_original" | grep -q '"type":"create"'
printf '%s' "$tool_read_original" | grep -q 'second line'
printf '%s' "$tool_read_original_full" | grep -q 'original file tool'
printf '%s' "$tool_edit_original" | grep -q '"oldString":"second"'
printf '%s' "$tool_read_batch_original" | grep -q '"filePath":"smoke/original.txt"'
printf '%s' "$tool_patch_original" | grep -q '"applied":true'
printf '%s' "$tool_file_write_binary" | grep -q '"encoding":"base64"'
printf '%s' "$tool_file_read_binary" | grep -q '"content":"AAFC"'
printf '%s' "$tool_file_mkdir" | grep -q '"path":"smoke/archive"'
printf '%s' "$tool_file_copy" | grep -q '"targetPath":"smoke/archive/copied.txt"'
printf '%s' "$tool_file_move" | grep -q '"targetPath":"smoke/archive/moved.txt"'
printf '%s' "$tool_file_delete" | grep -q '"deleted":true'
printf '%s' "$tool_file_search" | grep -q 'notes.txt'
printf '%s' "$tool_glob" | grep -q 'notes.txt'
printf '%s' "$tool_file_replace" | grep -q '"replacements":1'
printf '%s' "$tool_file_patch" | grep -q '"operations":1'
printf '%s' "$tool_file_write_code" | grep -q '"bytes"'
printf '%s' "$tool_grep" | grep -q 'code.go'
printf '%s' "$tool_code_index" | grep -q '"name":"Runner"'
printf '%s' "$tool_code_index" | grep -q '"name":"Run"'
printf '%s' "$tool_shell" | python3 -c 'import json,sys; stdout=json.load(sys.stdin)["result"]["stdout"].strip().replace("\\","/"); expected=sys.argv[1].strip().replace("\\","/"); assert stdout.lower() == expected.lower(), (stdout, expected)' "$shell_expected_workdir"
printf '%s' "$tool_shell" | grep -q '"stdoutTruncated":false'
printf '%s' "$tool_shell" | grep -q '"stderrTruncated":false'
printf '%s' "$tool_shell_original" | grep -q 'shell-original'
printf '%s' "$tool_bash_original" | grep -q 'bash-original'
printf '%s' "$tool_sleep" | grep -q '"requestedMs":1'
printf '%s' "$tool_sleep" | grep -q '"interrupted":false'
printf '%s' "$tool_ask_user_question" | grep -q '"question":"继续验证哪个工具？"'
printf '%s' "$tool_ask_user_question" | grep -q '"继续验证哪个工具？":"Sleep"'
printf '%s' "$tool_task_create" | grep -q '"status":"open"'
printf '%s' "$tool_task_update" | grep -q '"status":"done"'
printf '%s' "$tool_task_list" | grep -q 'smoke task'
printf '%s' "$tool_task_create_original" | grep -q 'smoke original task'
printf '%s' "$tool_task_update_original" | grep -q '"statusChange"'
printf '%s' "$tool_task_get_original" | grep -q '"status":"completed"'
printf '%s' "$tool_task_list_original" | grep -q 'smoke original task'
printf '%s' "$tool_task_output" | grep -q '"retrieval_status":"success"'
printf '%s' "$tool_task_output" | grep -q '"task_type":"go_task"'
printf '%s' "$tool_task_output_alias" | grep -q '"retrieval_status":"success"'
printf '%s' "$tool_task_stop_running" | grep -q '"statusChange"'
printf '%s' "$tool_task_stop" | grep -q '"task_type":"go_task"'
printf '%s' "$tool_task_stop" | grep -q 'smoke-long-task'
test "$tool_task_stop_alias_status" = "400"
grep -q 'not running' "$tmp/kill-shell-status.json"
printf '%s' "$tool_todo_write" | grep -q '"oldTodos":\[\]'
printf '%s' "$tool_todo_write" | grep -q '"status":"in_progress"'
printf '%s' "$tool_todo_write_done" | grep -q '"oldTodos":\['
printf '%s' "$tool_todo_write_done" | grep -q '"status":"completed"'
printf '%s' "$tool_settings_set" | grep -q '"value":"dark"'
printf '%s' "$tool_settings_get" | grep -q '"value":"dark"'
printf '%s' "$tool_settings_list" | grep -q '"theme"'
printf '%s' "$tool_config_set" | grep -q '"newValue":true'
printf '%s' "$tool_config_get" | grep -q '"value":true'
printf '%s' "$tool_search_select" | grep -q 'Config'
printf '%s' "$tool_search_select" | grep -q 'TaskUpdate'
printf '%s' "$tool_search_keyword" | grep -q 'Task'
printf '%s' "$tool_brief" | grep -q '"message":"smoke brief"'
printf '%s' "$tool_brief" | grep -q '"attachments":\['
printf '%s' "$tool_doctor" | grep -q '"ok":true'
printf '%s' "$tool_doctor" | grep -q '"go-tool-registry"'
printf '%s' "$tool_runtime_set" | grep -q '"version":1'
printf '%s' "$tool_runtime_get" | grep -q '"found":true'
printf '%s' "$tool_runtime_get" | grep -q '"source":"smoke"'
printf '%s' "$tool_runtime_list" | grep -q '"key":"last_goal"'
printf '%s' "$tool_runtime_delete" | grep -q '"deleted":true'
printf '%s' "$feishu_challenge" | grep -q 'smoke-feishu-challenge'
printf '%s' "$feishu_event" | grep -q '"platform":"feishu"'
printf '%s' "$feishu_event" | grep -q '"deduplicated":false'
printf '%s' "$feishu_event" | grep -q 'Feishu: smoke Feishu event task'
printf '%s' "$feishu_duplicate" | grep -q '"deduplicated":true'
printf '%s' "$wechat_event" | grep -q '"platform":"wechat"'
printf '%s' "$wechat_event" | grep -q '"deduplicated":false'
printf '%s' "$wechat_event" | grep -q 'WeChat: smoke WeChat event task'
printf '%s' "$wechat_duplicate" | grep -q '"deduplicated":true'
printf '%s' "$session_list" | grep -q "$feishu_session_id"
printf '%s' "$session_get" | grep -q '"lastRole":"user"'
printf '%s' "$session_replay" | grep -q 'smoke Feishu event task'
printf '%s' "$session_bind_project" | grep -q '"id":"alpha"'
printf '%s' "$session_bind_project" | grep -q '"path":"projects/alpha"'
test "$session_append_wrong_code" = "400"
printf '%s' "$session_append_wrong" | grep -q 'stale'
printf '%s' "$session_append" | grep -q '"eventId":2'
printf '%s' "$session_append" | grep -q '"runnerId":"runner-a"'
printf '%s' "$session_replay_after" | grep -q 'smoke session replay ready'
printf '%s' "$session_claim_a" | grep -q '"claimed":true'
printf '%s' "$session_claim_a" | grep -q '"runnerId":"runner-a"'
printf '%s' "$session_claim_b_conflict" | grep -q '"claimed":false'
printf '%s' "$session_claim_b_conflict" | grep -q '"ownerRunnerId":"runner-a"'
printf '%s' "$session_heartbeat_a" | grep -q '"renewed":true'
test "$session_release_b_wrong_code" = "400"
printf '%s' "$session_release_b_wrong" | grep -q 'stale'
printf '%s' "$session_release_a" | grep -q '"released":true'
test "$session_append_released_code" = "400"
printf '%s' "$session_append_released" | grep -q 'stale'
printf '%s' "$session_claim_b" | grep -q '"claimed":true'
printf '%s' "$session_claim_b" | grep -q '"runnerId":"runner-b"'
printf 'SESSION_RUNNER_LEASE_API=yes\n'
printf '%s' "$session_runner_pick" | grep -q '"claimed":true'
printf '%s' "$session_runner_pick" | grep -q '"ownerRunnerId":"runner-pick"'
printf '%s' "$session_runner_pick" | grep -q '"runnerId":"runner-pick"'
printf '%s' "$session_runner_pick" | grep -q '"entries":'
printf '%s' "$session_runner_pick_release" | grep -q '"released":true'
printf 'SESSION_RUNNER_PICK_API=yes\n'
printf '%s' "$session_runner_queue" | grep -q '"totalSessions":'
printf '%s' "$session_runner_queue" | grep -q '"pending":'
printf '%s' "$session_runner_queue" | grep -q '"running":1'
printf 'SESSION_RUNNER_QUEUE_API=yes\n'
python3 - "$home" <<'PY'
import json
import pathlib
import sys
import urllib.parse

home = pathlib.Path(sys.argv[1])
sessions = json.loads((home / "sessions" / "index.json").read_text(encoding="utf-8"))
expected = {
    "im:feishu:oc_smoke": ("feishu", "oc_smoke", "om_smoke", "smoke Feishu event task"),
    "im:wechat:wx-smoke": ("wechat", "wx-smoke", "30001", "smoke WeChat event task"),
}
missing = sorted(set(expected) - set(sessions))
if missing:
    raise SystemExit(f"missing IM live sessions: {missing}")
for session_id, (platform, chat_id, message_id, text) in expected.items():
    session = sessions[session_id]
    minimum_count = 2 if session_id == "im:feishu:oc_smoke" else 1
    if session.get("messageCount", 0) < minimum_count:
        raise SystemExit(f"unexpected IM session state for {session_id}: {session}")
    if session_id == "im:feishu:oc_smoke" and session.get("runner", {}).get("runnerId") != "runner-b":
        raise SystemExit(f"unexpected Feishu session runner lease: {session}")
    if session_id == "im:feishu:oc_smoke":
        project = session.get("project") or {}
        if project.get("id") != "alpha" or project.get("path") != "projects/alpha":
            raise SystemExit(f"unexpected Feishu project binding: {session}")
        if session.get("workDir") != str(home / "projects" / "alpha"):
            raise SystemExit(f"unexpected Feishu project workDir: {session}")
    journal_path = home / "session-events" / (urllib.parse.quote_plus(session_id) + ".jsonl")
    lines = [line for line in journal_path.read_text(encoding="utf-8").splitlines() if line.strip()]
    if len(lines) < minimum_count:
        raise SystemExit(f"unexpected IM journal length for {session_id}: {lines}")
    messages = [json.loads(line)["message"] for line in lines]
    inbound = [
        message for message in messages
        if message.get("type") == "im_message"
        and message.get("role") == "user"
        and message.get("platform") == platform
        and message.get("chatId") == chat_id
        and str(message.get("messageId")) == message_id
    ]
    if len(inbound) != 1:
        raise SystemExit(f"expected exactly one deduplicated inbound message for {session_id}: {messages}")
    if text not in str(inbound[0].get("text", "")) or not inbound[0].get("taskId"):
        raise SystemExit(f"unexpected IM journal content for {session_id}: {inbound[0]}")
PY
printf 'TOOLS_API=yes\n'
printf 'FILE_TOOLS_API=yes\n'
printf 'SHELL_TOOL_API=yes\n'
printf 'TASK_TOOLS_API=yes\n'
printf 'SETTINGS_TOOLS_API=yes\n'
printf 'SESSION_TOOLS_API=yes\n'
printf 'RUNTIME_KV_TOOLS_API=yes\n'
printf 'IM_LIVE_SESSIONS_JOURNALED=yes\n'
printf 'FEISHU_EVENT_API=yes\n'
printf 'WECHAT_EVENT_API=yes\n'

go run ./scripts/smoke-ws-client.go "ws://127.0.0.1:$port/api/synon-link/ws?userId=local"
curl -fsS -X POST "http://127.0.0.1:$port/api/tools/pairing_allow/execute" -H 'Content-Type: application/json' --data '{"input":{"platform":"feishu","userId":"ou_session_ws_smoke","displayName":"Session WS Smoke"}}' >/dev/null
go run ./scripts/smoke-session-ws-client "http://127.0.0.1:$port"

if printf '%s' "$capabilities" | grep -q 'ADMET'; then
	printf 'CAPABILITIES_HAS_REMOVED_ADMET=yes\n'
	exit 1
fi
printf 'CAPABILITIES_HAS_REMOVED_ADMET=no\n'

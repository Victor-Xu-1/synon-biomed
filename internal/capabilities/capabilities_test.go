package capabilities

import (
	"strings"
	"testing"

	"synon-go/internal/tools/registry"
)

func TestCompactSynonCapabilitiesExposeOnlyRetainedSurface(t *testing.T) {
	report := CompactSynon()
	if report.Status != "needs_connection" {
		t.Fatalf("Status = %q, want needs_connection", report.Status)
	}
	for _, id := range []string{"synon-link", "im-message-core", "runtime-store", "tool-registry", "plugin-host"} {
		if !report.ContainsModule(id) {
			t.Fatalf("capability report missing module %q: %#v", id, report.Modules)
		}
	}
	for _, banned := range []string{"Synon Knowledge", "Daily Briefing", "ADMET", "PCC", "Markush", "Molecular Modeling"} {
		if report.ContainsLabel(banned) {
			t.Fatalf("capability report still exposes removed capability %q: %#v", banned, report)
		}
	}
	if !report.ContainsAction("search_web") || !report.ContainsAction("read_page") {
		t.Fatalf("capability report missing retained Synon Link actions: %#v", report)
	}
	for _, action := range []string{"run_task_loop", "ensure_workspace", "open_tab", "get_active_tab", "extract_page", "refresh_tab", "scroll_page", "wait_for_page", "click", "click_at", "type", "screenshot", "find_download_links", "download_file", "open_downloads_folder", "show_downloaded_file", "cleanup_tabs", "close_tab", "local_files_manifest", "local_files_search", "local_file_info", "local_file_copy", "local_file_move", "local_file_delete", "local_file_mkdir", "local_files_organize", "clear_local_files", "bookmarks_status", "bookmarks_profile", "revoke_bookmarks_permission", "open_personalization"} {
		if !report.ContainsAction(action) {
			t.Fatalf("capability report missing browser protocol action %q: %#v", action, report)
		}
	}
	for _, action := range []string{"desktop_status", "desktop_task_loop", "desktop_request_access", "desktop_screenshot", "desktop_click", "desktop_shell", "desktop_batch", "run_go_desktop_agent"} {
		if report.ContainsAction(action) {
			t.Fatalf("capability report still exposes removed computer operation %q: %#v", action, report)
		}
	}
	if report.ContainsWorkflow("local-device-runtime") {
		t.Fatalf("capability report still exposes removed local-device workflow: %#v", report.Workflows)
	}
	for _, action := range []string{"synon_link_ws_connect", "handle_synon_link_hello", "track_synon_link_progress", "complete_synon_link_command", "revoke_synon_link_device", "list_synon_link_logs", "synon_link_doctor", "synon_link_policy_matrix", "enforce_synon_link_policy"} {
		if !report.ContainsAction(action) {
			t.Fatalf("capability report missing Synon Link realtime action %q: %#v", action, report)
		}
	}
	for _, action := range []string{"split_im_message", "buffer_stream_message", "deduplicate_message", "connect_adapter_ws_session", "reconnect_adapter_ws_session", "send_adapter_ws_heartbeat", "track_adapter_ws_pong", "send_adapter_user_message", "send_adapter_goal_driver", "send_adapter_internal_task", "send_adapter_permission_response", "send_adapter_stop_generation", "release_adapter_session", "serialize_adapter_server_messages", "bind_adapter_outbound_handler", "create_im_live_session", "journal_im_inbound_message", "extract_wechat_text", "send_wechat_text", "process_wechat_server_message", "send_wechat_outbound_chunk", "register_wechat_outbound_bridge", "start_wechat_qr_login", "poll_wechat_qr_login", "get_wechat_updates", "run_wechat_polling_loop", "start_feishu_wsclient", "route_feishu_stream_message", "create_feishu_card_entity", "send_feishu_card_message", "stream_feishu_card_content", "set_feishu_card_streaming_mode", "update_feishu_card", "process_feishu_server_message", "stream_feishu_server_content_delta", "finalize_feishu_server_message", "abort_feishu_server_message", "dispatch_feishu_delta_media", "build_initial_feishu_streaming_card", "build_final_feishu_cardkit_card", "build_feishu_rendered_card", "build_feishu_error_card", "normalize_feishu_card_markdown", "optimize_feishu_card_markdown", "sanitize_feishu_card_tables", "find_feishu_markdown_tables", "create_feishu_flush_controller", "throttle_feishu_card_flush", "wait_feishu_card_flush", "cancel_feishu_card_flush", "create_feishu_streaming_card", "append_feishu_streaming_card_text", "append_feishu_card_reasoning", "start_feishu_card_tool_step", "complete_feishu_card_tool_step", "flush_feishu_streaming_card", "finalize_feishu_streaming_card", "abort_feishu_streaming_card", "watch_feishu_outbound_images", "watch_feishu_outbound_files", "classify_feishu_outbound_upload_source", "deduplicate_feishu_outbound_uploads", "filter_feishu_unsafe_local_uploads", "upload_feishu_image", "upload_feishu_file", "send_feishu_image_message", "send_feishu_file_message", "dispatch_feishu_outbound_image", "dispatch_feishu_outbound_file", "fetch_feishu_outbound_image_url", "patch_feishu_message_card", "detect_feishu_card_rate_limit", "detect_feishu_card_table_limit", "check_im_pairing"} {
		if !report.ContainsAction(action) {
			t.Fatalf("capability report missing IM action %q: %#v", action, report)
		}
	}
	for _, removed := range []string{
		"route_feishu_stream_card_action",
		"route_feishu_command",
		"decide_feishu_goal_presentation",
		"match_feishu_ask_user_question",
		"rebind_feishu_ask_user_question_request",
		"delete_feishu_ask_user_question_aliases",
		"build_feishu_ask_user_question_card",
		"parse_feishu_ask_user_question_action",
		"apply_feishu_ask_user_question_selection",
		"create_feishu_ask_user_question_manager",
		"show_feishu_ask_user_question_card",
		"patch_feishu_ask_user_question_card",
		"submit_feishu_ask_user_question",
		"answer_feishu_ask_user_question_with_text",
		"clear_feishu_ask_user_question",
		"parse_feishu_ask_user_questions",
	} {
		if report.ContainsAction(removed) {
			t.Fatalf("capability report still exposes detached Feishu route %q: %#v", removed, report)
		}
	}
	if !report.ContainsAction("append_session_event") || !report.ContainsAction("replay_session_events") || !report.ContainsAction("upsert_session") || !report.ContainsAction("list_sessions") || !report.ContainsAction("claim_session_runner") || !report.ContainsAction("heartbeat_session_runner") || !report.ContainsAction("release_session_runner") || !report.ContainsAction("pick_session_runner") || !report.ContainsAction("summarize_session_runner_queue") || !report.ContainsAction("runtime_set") || !report.ContainsAction("runtime_delete") {
		t.Fatalf("capability report missing durable runtime journal actions: %#v", report)
	}
	if !report.ContainsAction("list_tools") || !report.ContainsAction("search_skills") || !report.ContainsAction("web_search") || !report.ContainsAction("web_research") || !report.ContainsAction("VisualReview") || !report.ContainsAction("LSP") || !report.ContainsAction("SendMessage") || !report.ContainsAction("SendUserMessage") || !report.ContainsAction("ToolDoctor") || !report.ContainsAction("ListMcpTools") || !report.ContainsAction("ListMcpResourcesTool") || !report.ContainsAction("ReadMcpResourceTool") || !report.ContainsAction("MCPTool") || !report.ContainsAction("execute_web_fetch") || !report.ContainsAction("execute_web_search") {
		t.Fatalf("capability report missing tool registry actions: %#v", report)
	}
	if !report.ContainsAction("execute_synon_link") {
		t.Fatalf("capability report missing Synon Link tool execution action: %#v", report)
	}
	for _, action := range []string{"web_fetch", "synon_link"} {
		if !report.ContainsAction(action) {
			t.Fatalf("capability report missing canonical web/link tool action %q: %#v", action, report)
		}
	}
	for _, action := range []string{"file_list", "file_info", "file_read", "Read", "ReadBatch", "file_write", "Write", "NotebookEdit", "file_mkdir", "file_copy", "file_move", "file_delete", "file_search", "Glob", "Grep", "file_replace", "Edit", "file_patch", "Patch", "json_patch", "code_index", "code_references"} {
		if !report.ContainsAction(action) {
			t.Fatalf("capability report missing file tool action %q: %#v", action, report)
		}
	}
	for _, action := range []string{"EnterWorktree", "ExitWorktree"} {
		if !report.ContainsAction(action) {
			t.Fatalf("capability report missing worktree tool action %q: %#v", action, report)
		}
	}
	for _, action := range []string{"artifact_register", "artifact_list", "artifact_get", "artifact_research_audit", "StructuredOutput"} {
		if !report.ContainsAction(action) {
			t.Fatalf("capability report missing artifact tool action %q: %#v", action, report)
		}
	}
	if !capabilitySummaryContains(report, "bounded artifact_get content retrieval") || !capabilitySummaryContains(report, "original Synon research artifact phase") {
		t.Fatalf("capability report missing artifact content/research audit summary: %#v", report.Modules)
	}
	if capabilitySummaryContains(report, "websearch.py") || !capabilitySummaryContains(report, "Go HTTP WebSearch backend") {
		t.Fatalf("capability report has stale WebSearch backend summary: %#v", report.Modules)
	}
	if !capabilitySummaryContains(report, "VisualReview screenshot_command execution") {
		t.Fatalf("capability report missing VisualReview screenshot command summary: %#v", report.Modules)
	}
	if !capabilitySummaryContains(report, "VisualReview dependency diagnostics") {
		t.Fatalf("capability report missing VisualReview dependency diagnostics summary: %#v", report.Modules)
	}
	if !capabilitySummaryContains(report, "TaskRun direct system steps") || !capabilitySummaryContains(report, "web_research, lsp_diagnostics, patch, read_files, notebook_edit") {
		t.Fatalf("capability report missing TaskRun original system operation summary: %#v", report.Modules)
	}
	if !reportContainsCapability(report, "taskrun-original-system-tools") {
		t.Fatalf("capability report missing taskrun-original-system-tools capability: %#v", report.Modules)
	}
	if !capabilitySummaryContains(report, "TaskRun SynonLink browser executors") || !capabilitySummaryContains(report, "needsClient/missingCapability") {
		t.Fatalf("capability report missing TaskRun SynonLink executor summary: %#v", report.Modules)
	}
	if !reportContainsCapability(report, "taskrun-synonlink-direct-executor") {
		t.Fatalf("capability report missing taskrun-synonlink-direct-executor capability: %#v", report.Modules)
	}
	if !capabilitySummaryContains(report, "TaskRun monitor ticks") || !capabilitySummaryContains(report, "self-check pending completions") {
		t.Fatalf("capability report missing TaskRun monitor summary: %#v", report.Modules)
	}
	if !capabilitySummaryContains(report, "explicit task graphs only") || !capabilitySummaryContains(report, "hard-coded task playbooks") {
		t.Fatalf("capability report missing explicit-graph TaskRun boundary summary: %#v", report.Modules)
	}
	if !capabilitySummaryContains(report, "original tool surface audit") || !capabilitySummaryContains(report, "dynamic MCP auth pseudo-tool") {
		t.Fatalf("capability report missing original tool surface audit summary: %#v", report.Modules)
	}
	for _, action := range []string{"shell_exec", "Bash", "Shell", "powershell"} {
		if !report.ContainsAction(action) {
			t.Fatalf("capability report missing shell tool action %q: %#v", action, report)
		}
	}
	for _, action := range []string{"Sleep"} {
		if !report.ContainsAction(action) {
			t.Fatalf("capability report missing sleep tool action %q: %#v", action, report)
		}
	}
	if !report.ContainsAction("ask_user") {
		t.Fatalf("capability report missing canonical ask_user tool action: %#v", report)
	}
	for _, legacy := range []string{"AskUserQuestion", "ask_user_question"} {
		if report.ContainsAction(legacy) {
			t.Fatalf("capability report advertised legacy ask-user alias %q: %#v", legacy, report)
		}
	}
	for _, action := range []string{"Agent", "generate_plan", "CronCreate", "CronUpdate", "CronList", "CronDelete", "cron_tick", "TeamCreate", "TeamDelete", "TaskRun", "task_create", "TaskCreate", "TaskGet", "task_update", "TaskUpdate", "task_list", "TaskList"} {
		if !report.ContainsAction(action) {
			t.Fatalf("capability report missing task tool action %q: %#v", action, report)
		}
	}
	for _, action := range []string{"TaskOutput", "TaskStop"} {
		if !report.ContainsAction(action) {
			t.Fatalf("capability report missing task tool action %q: %#v", action, report)
		}
	}
	for _, action := range []string{"TodoWrite"} {
		if !report.ContainsAction(action) {
			t.Fatalf("capability report missing todo tool action %q: %#v", action, report)
		}
	}
	for _, alias := range []string{"WebFetch", "WebSearch", "WebResearch", "SynonLink", "Brief", "file_read_batch", "glob", "grep", "sleep", "Task", "AgentOutputTool", "BashOutputTool", "KillShell", "PowerShell", "Skill", "SkillSearch", "ToolSearch", "todo_write"} {
		if report.ContainsAction(alias) {
			t.Fatalf("capability report advertised retired inbound alias %q: %#v", alias, report)
		}
	}
	for _, action := range []string{"settings_set", "settings_get", "settings_list", "Config"} {
		if !report.ContainsAction(action) {
			t.Fatalf("capability report missing settings tool action %q: %#v", action, report)
		}
	}
	for _, action := range []string{"runtime_set", "runtime_get", "runtime_list", "runtime_delete"} {
		if !report.ContainsAction(action) {
			t.Fatalf("capability report missing runtime KV tool action %q: %#v", action, report)
		}
	}
	for _, action := range []string{"session_list", "session_get", "session_replay", "session_append", "session_claim", "session_heartbeat", "session_release", "session_runner_checkpoint", "session_runner_next", "session_runner_finish", "session_runner_pick", "session_runner_queue", "session_bind_project"} {
		if !report.ContainsAction(action) {
			t.Fatalf("capability report missing session tool action %q: %#v", action, report)
		}
	}
	for _, action := range []string{"list_plugins", "read_plugin_manifest", "load_external_plugin_manifest", "start_external_plugin_process", "stop_external_plugin_process", "route_synon_plugin_capabilities", "route_synon_link_download"} {
		if !report.ContainsAction(action) {
			t.Fatalf("capability report missing plugin host action %q: %#v", action, report)
		}
	}
}

func TestCompactSynonCapabilityReportTracksRegisteredRuntimeTools(t *testing.T) {
	report := CompactSynon()
	reg := registry.Default()

	for _, name := range reg.Names() {
		if !report.ContainsAction(name) {
			t.Fatalf("capability report missing registered tool action %q", name)
		}
	}

	toolModule, ok := moduleByID(report, "tool-registry")
	if !ok {
		t.Fatal("capability report missing tool-registry module")
	}
	registeredMetric := "," + toolModule.Metrics["registeredTools"] + ","
	for _, name := range reg.Names() {
		if !strings.Contains(registeredMetric, ","+name+",") {
			t.Fatalf("registeredTools metric missing registered tool %q: %s", name, toolModule.Metrics["registeredTools"])
		}
	}
}

func reportContainsCapability(report Report, capability string) bool {
	for _, module := range report.Modules {
		for _, item := range module.Capabilities {
			if item == capability {
				return true
			}
		}
	}
	return false
}

func capabilitySummaryContains(report Report, needle string) bool {
	for _, module := range report.Modules {
		if strings.Contains(module.Summary, needle) {
			return true
		}
	}
	for _, workflow := range report.Workflows {
		if strings.Contains(workflow.Summary, needle) {
			return true
		}
	}
	return false
}

func moduleByID(report Report, id string) (Module, bool) {
	for _, module := range report.Modules {
		if module.ID == id {
			return module, true
		}
	}
	return Module{}, false
}

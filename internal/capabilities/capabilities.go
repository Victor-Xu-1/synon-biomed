package capabilities

import (
	"strings"

	"synon-go/internal/harnesscontract"
	"synon-go/internal/tools/registry"
)

type Module struct {
	ID           string            `json:"id"`
	Label        string            `json:"label"`
	Status       string            `json:"status"`
	Endpoint     string            `json:"endpoint"`
	Summary      string            `json:"summary"`
	Actions      []string          `json:"actions"`
	Capabilities []string          `json:"capabilities"`
	Metrics      map[string]string `json:"metrics"`
}

type Workflow struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Status  string   `json:"status"`
	Summary string   `json:"summary"`
	Steps   []string `json:"steps"`
}

type Report struct {
	Status    string     `json:"status"`
	Modules   []Module   `json:"modules"`
	Workflows []Workflow `json:"workflows"`
}

func CompactSynon() Report {
	linkActions := []string{
		"synon_link_ws_connect",
		"handle_synon_link_hello",
		"track_synon_link_progress",
		"complete_synon_link_command",
		"revoke_synon_link_device",
		"list_synon_link_logs",
		"synon_link_doctor",
		"synon_link_policy_matrix",
		"enforce_synon_link_policy",
		"request_synon_link_access",
		"list_synon_link_access_requests",
		"decide_synon_link_access",
		"revoke_synon_link_access",
		"deliver_synon_link_access_events",
		"list_running",
		"list_clients",
		"run_task_loop",
		"ensure_workspace",
		"open_local_files",
		"search_web",
		"extract_search_results",
		"read_logged_in_page",
		"open_tab",
		"get_active_tab",
		"extract_page",
		"read_page",
		"refresh_tab",
		"scroll_page",
		"wait_for_page",
		"click",
		"click_at",
		"type",
		"screenshot",
		"visual_element_map",
		"find_download_links",
		"download_from_page",
		"download_file",
		"open_downloads_folder",
		"show_downloaded_file",
		"cleanup_tabs",
		"close_tab",
		"local_files_status",
		"local_files_manifest",
		"local_files_list",
		"local_files_search",
		"local_file_info",
		"local_file_read",
		"local_file_write",
		"local_file_copy",
		"local_file_move",
		"local_file_delete",
		"local_file_mkdir",
		"local_files_organize",
		"clear_local_files",
		"bookmarks_status",
		"bookmarks_profile",
		"revoke_bookmarks_permission",
		"open_personalization",
	}
	linkCapabilityIDs := []string{
		"websocket-realtime",
		"command-progress",
		"command-result-routing",
		"device-revocation",
		"durable-task-logs",
		"diagnostic-report",
		"policy-matrix",
		"policy-enforcement",
		"access-request-lifecycle",
		"access-event-websocket-bridge",
		"browser-bridge",
		"browser-workspace",
		"browser-navigation",
		"browser-interaction",
		"downloads",
		"download-discovery",
		"download-recovery",
		"visual-browser-state",
		"local-file-manifest",
		"local-file-search",
		"local-file-preview",
		"local-file-apply",
		"bookmark-permission",
		"bookmark-profile",
	}
	imActions := []string{
		"split_im_message",
		"buffer_stream_message",
		"deduplicate_message",
		"connect_adapter_ws_session",
		"reconnect_adapter_ws_session",
		"send_adapter_ws_heartbeat",
		"track_adapter_ws_pong",
		"send_adapter_user_message",
		"send_adapter_goal_driver",
		"send_adapter_internal_task",
		"send_adapter_permission_response",
		"send_adapter_stop_generation",
		"release_adapter_session",
		"serialize_adapter_server_messages",
		"bind_adapter_outbound_handler",
		"orchestrate_adapter_outbound_delivery",
		"report_adapter_outbound_delivery",
		"create_im_live_session",
		"journal_im_inbound_message",
		"escape_markdown_v2",
		"format_tool_use",
		"format_permission_request",
		"extract_wechat_text",
		"collect_wechat_media",
		"send_wechat_text",
		"process_wechat_server_message",
		"send_wechat_outbound_chunk",
		"register_wechat_outbound_bridge",
		"start_wechat_qr_login",
		"poll_wechat_qr_login",
		"get_wechat_updates",
		"run_wechat_polling_loop",
		"load_wechat_adapter_config",
		"start_wechat_polling_adapter",
		"receive_wechat_event",
		"deduplicate_wechat_message",
		"create_task_from_wechat_message",
		"respond_feishu_url_verification",
		"extract_feishu_inbound_payload",
		"detect_feishu_post_mention",
		"build_feishu_stream_router",
		"start_feishu_wsclient",
		"route_feishu_stream_message",
		"receive_feishu_event",
		"deduplicate_feishu_message",
		"create_task_from_feishu_message",
		"create_feishu_card_entity",
		"send_feishu_card_message",
		"stream_feishu_card_content",
		"set_feishu_card_streaming_mode",
		"update_feishu_card",
		"process_feishu_server_message",
		"stream_feishu_server_content_delta",
		"finalize_feishu_server_message",
		"abort_feishu_server_message",
		"dispatch_feishu_delta_media",
		"build_initial_feishu_streaming_card",
		"build_final_feishu_cardkit_card",
		"build_feishu_rendered_card",
		"build_feishu_error_card",
		"normalize_feishu_card_markdown",
		"optimize_feishu_card_markdown",
		"sanitize_feishu_card_tables",
		"find_feishu_markdown_tables",
		"create_feishu_flush_controller",
		"throttle_feishu_card_flush",
		"wait_feishu_card_flush",
		"cancel_feishu_card_flush",
		"create_feishu_streaming_card",
		"append_feishu_streaming_card_text",
		"append_feishu_card_reasoning",
		"start_feishu_card_tool_step",
		"complete_feishu_card_tool_step",
		"flush_feishu_streaming_card",
		"finalize_feishu_streaming_card",
		"abort_feishu_streaming_card",
		"watch_feishu_outbound_images",
		"watch_feishu_outbound_files",
		"classify_feishu_outbound_upload_source",
		"deduplicate_feishu_outbound_uploads",
		"filter_feishu_unsafe_local_uploads",
		"upload_feishu_image",
		"upload_feishu_file",
		"send_feishu_image_message",
		"send_feishu_file_message",
		"dispatch_feishu_outbound_image",
		"dispatch_feishu_outbound_file",
		"fetch_feishu_outbound_image_url",
		"patch_feishu_message_card",
		"detect_feishu_card_rate_limit",
		"detect_feishu_card_table_limit",
		"parse_permission_callback",
		"parse_permission_command",
		"check_im_pairing",
	}
	imCapabilityIDs := []string{
		"im-formatting",
		"stream-message-buffer",
		"message-deduplication",
		"adapter-ws-bridge",
		"adapter-outbound-bridge",
		"adapter-outbound-delivery-orchestration",
		"adapter-outbound-delivery-telemetry",
		"im-live-session-journal",
		"wechat-protocol-core",
		"wechat-send-transport",
		"wechat-server-message-processor",
		"wechat-qr-binding",
		"wechat-polling-client",
		"wechat-polling-runner",
		"wechat-adapter-process-wiring",
		"wechat-event-intake",
		"wechat-message-to-task",
		"feishu-wsclient-event-bridge",
		"feishu-event-intake",
		"feishu-message-to-task",
		"feishu-cardkit-api",
		"feishu-card-rendering",
		"feishu-markdown-optimizer",
		"feishu-flush-controller",
		"feishu-streaming-card-state-machine",
		"feishu-server-message-processor",
		"feishu-streaming-card-process-rendering",
		"feishu-outbound-media-watcher",
		"feishu-media-upload-send",
		"permission-decisions",
		"pairing-gates",
	}
	runtimeActions := []string{
		"upsert_session",
		"get_session",
		"list_sessions",
		"append_session_message",
		"append_session_event",
		"replay_session_events",
		"read_client_message_events",
		"check_client_message_seen",
		"remove_session_events",
		"claim_session_runner",
		"heartbeat_session_runner",
		"release_session_runner",
		"pick_session_runner",
		"summarize_session_runner_queue",
		"summarize_session_runner_backlog",
		"runtime_set",
		"runtime_get",
		"runtime_list",
		"runtime_delete",
	}
	runtimeCapabilityIDs := []string{
		"jsonl-session-event-journal",
		"json-session-index",
		"cursor-replay",
		"client-message-replay",
		"concurrent-event-append",
		"session-runner-lease",
		"session-runner-backlog",
		"taskrun-runner-auto-advance",
		"namespaced-runtime-kv",
		"runtime-value-versioning",
	}
	toolRegistryActions := []string{
		"list_tools",
		"validate_tool_input",
		"execute_web_fetch",
		"execute_web_search",
		"execute_synon_link",
		"plan_session_runner_goal",
		"record_session_runner_structured_plan",
	}
	registeredToolNames := registry.Default().Names()
	serviceOperationNames := registry.DefaultOperations().Names()
	toolRegistryActions = mergeUnique(toolRegistryActions, registeredToolNames)
	toolRegistryActions = mergeUnique(toolRegistryActions, harnesscontract.RootModelTools())
	toolRegistryActions = mergeUnique(toolRegistryActions, harnesscontract.FixedJobModelTools())
	toolRegistryCapabilityIDs := []string{
		"tool-catalog",
		"input-validation",
		"tool-discovery",
		"original-websearch-contract",
		"original-webfetch-contract",
		"web-fetch-execution",
	}
	serviceOperationCapabilityIDs := []string{
		"original-synonlink-contract",
		"original-sendmessage-contract",
		"user-message-channel",
		"tool-diagnostics",
		"synon-link-tool-execution",
		"file-root-execution",
		"file-search-edit",
		"glob-search",
		"grep-search",
		"structured-file-patch",
		"code-symbol-index",
		"original-lsp-contract",
		"external-stdio-lsp-bridge",
		"external-socket-lsp-bridge",
		"passive-lsp-diagnostic-registry",
		"file-change-diagnostic-reset",
		"static-lsp-code-intelligence",
		"original-notebook-edit-contract",
		"jupyter-notebook-cell-editing",
		"original-mcp-contract",
		"mcp-stdio-json-rpc",
		"mcp-remote-http-sse-json-rpc",
		"mcp-remote-websocket-json-rpc",
		"mcp-sdk-bridge-json-rpc",
		"mcp-resource-access",
		"bounded-shell-execution",
		"original-shell-tools",
		"bounded-runtime-sleep",
		"user-questionnaire-tool",
		"original-agent-contract",
		"original-enter-plan-mode-contract",
		"plan-mode-entry-state",
		"original-exit-plan-mode-contract",
		"plan-mode-exit-state",
		"original-cron-contract",
		"durable-scheduled-tasks",
		"original-team-contract",
		"durable-team-state",
		"durable-agent-delegation",
		"durable-task-tools",
		"original-task-contracts",
		"original-task-run-contract",
		"taskrun-advance-action",
		"taskrun-runner-auto-advance",
		"taskrun-repair-policy",
		"taskrun-dependency-aware-step-queue",
		"taskrun-soft-self-check",
		"taskrun-self-check-repair",
		"taskrun-original-system-tools",
		"taskrun-synonlink-direct-executor",
		"original-task-output-contract",
		"original-task-stop-contract",
		"session-runner-structured-goal-plan",
		"session-todo-state",
		"durable-settings-tools",
		"original-config-contract",
		"durable-session-tools",
		"durable-runtime-kv-tools",
		"artifact-registry",
		"artifact-digest-metadata",
		"artifact-content-retrieval",
		"research-artifact-audit",
		"structured-output",
		"structured-output-schema-validation",
		"git-worktree-session",
		"dirty-worktree-protection",
	}
	pluginHostActions := []string{
		"list_plugins",
		"read_plugin_manifest",
		"load_external_plugin_manifest",
		"start_external_plugin_process",
		"stop_external_plugin_process",
		"route_synon_plugin_capabilities",
		"route_synon_link_download",
	}
	pluginHostCapabilityIDs := []string{
		"builtin-plugin-registry",
		"external-plugin-directory-loader",
		"external-plugin-process-lifecycle",
		"synon-plugin-manifest",
		"plugin-api-routing",
	}
	status := "needs_connection"
	return Report{
		Status: status,
		Modules: []Module{
			{
				ID:           "synon-link",
				Label:        "Synon Link",
				Status:       status,
				Endpoint:     "/api/synon-link",
				Summary:      "Synon Link exposes its browser extension bridge for workspace, navigation, page reading, interaction, screenshots, downloads, browser-local files, bookmarks, progress, results, diagnostics, policy enforcement, and permission lifecycle events.",
				Actions:      linkActions,
				Capabilities: linkCapabilityIDs,
				Metrics: map[string]string{
					"minimumVersion": "0.6.10",
					"packageUrl":     "/api/plugins/synon/link/download",
				},
			},
			{
				ID:           "im-message-core",
				Label:        "IM Message Channels",
				Status:       "started",
				Endpoint:     "/api/adapters/wechat/event",
				Summary:      "Go ports for shared IM formatting, stream buffering, deduplication, adapter-to-core WebSocket bridge with heartbeat, reconnect, outbound handler binding, shared outbound delivery retry/deduplication/result reporting, platform delivery telemetry reports with structured render policies, permission decisions, pairing checks, durable IM live-session journaling for Feishu/WeChat inbound messages, Feishu command routing, Feishu official WSClient event bridge wiring, Feishu HTTP event intake, Feishu CardKit create/send/stream/settings/update API calls, Feishu CardKit card JSON rendering helpers, Feishu CardKit markdown optimizer/table sanitizer, Feishu CardKit flush throttling/mutex controller, Feishu CardKit streaming-card state machine with server-message processing, reasoning/tool process rendering, abort error-card rendering, patch fallback, AskUserQuestion permission-card state/action/send/update manager, outbound image/file markdown watchers, Feishu media upload/send dispatcher, and Feishu HandleWithReport telemetry, WeChat parsing, WeChat sendMessage transport, WeChat server-message outbound processor with telemetry, WeChat QR binding start/poll flow, WeChat getUpdates polling runner with config/autostart wiring, WeChat HTTP event intake are available.",
				Actions:      imActions,
				Capabilities: imCapabilityIDs,
				Metrics: map[string]string{
					"platformsStarted": "feishu,wechat",
					"eventIntake":      "feishu,wechat",
					"wechatQRCode":     "start,poll",
					"platformsPending": "",
				},
			},
			{
				ID:           "runtime-store",
				Label:        "Runtime Store",
				Status:       "started",
				Endpoint:     "internal/persistence/journal",
				Summary:      "Go persistence supports a JSON session index, JSONL session event journal, and namespaced JSON runtime KV store for durable IM live-session anchors, append, cursor replay, client message replay, cross-platform-safe journal filenames, concurrent event id serialization, versioned runtime values, listing, and deletion.",
				Actions:      runtimeActions,
				Capabilities: runtimeCapabilityIDs,
				Metrics: map[string]string{
					"storage": "json,jsonl",
					"scope":   "sessions,session-events,runtime-kv,tasks,settings",
				},
			},
			{
				ID:           "tool-registry",
				Label:        "Tool Registry",
				Status:       "started",
				Endpoint:     "/api/tools",
				Summary:      "The model tool registry exposes only statically registered model roots. Task-owned roots are injected by their owning runtime, and service/API operations cannot be flattened into a model prompt.",
				Actions:      toolRegistryActions,
				Capabilities: toolRegistryCapabilityIDs,
				Metrics: map[string]string{
					"registeredTools": strings.Join(registeredToolNames, ","),
				},
			},
			{
				ID:           "service-operations",
				Label:        "Service Operations",
				Status:       "started",
				Endpoint:     "internal/server and compatibility API boundaries",
				Summary:      "Internal controls and retained compatibility operations use a service-operation catalog that is physically separate from model tools. Exact compatibility identities resolve only for explicit consumers. This boundary retains the Go HTTP WebSearch backend, VisualReview screenshot_command execution and VisualReview dependency diagnostics, bounded artifact_get content retrieval, and original Synon research artifact phase auditing. The compatibility TaskRun boundary accepts explicit task graphs only and delegates execution to the canonical runner; hard-coded task playbooks and keyword auto-selection are absent. TaskRun SynonLink browser executors preserve needsClient/missingCapability blockers, TaskRun monitor ticks reconcile self-check pending completions, and TaskRun direct system steps cover web_research, lsp_diagnostics, patch, read_files, notebook_edit, record_artifact, shell_command, and visual_review. The original tool surface audit and dynamic MCP auth pseudo-tool remain available only through this explicit service boundary.",
				Actions:      serviceOperationNames,
				Capabilities: serviceOperationCapabilityIDs,
				Metrics: map[string]string{
					"registeredOperations": strings.Join(serviceOperationNames, ","),
				},
			},
			{
				ID:           "plugin-host",
				Label:        "Plugin Host",
				Status:       "started",
				Endpoint:     "/api/plugins",
				Summary:      "Go plugin host lists the retained built-in Synon plugin, loads external .synon-plugin/plugin.json manifests from configured directories, manages declared external plugin process lifecycles, serves plugin manifests, and routes the compact Synon capabilities and Synon Link download module surfaces without loading excluded side plugins.",
				Actions:      pluginHostActions,
				Capabilities: pluginHostCapabilityIDs,
				Metrics: map[string]string{
					"plugins":         "synon,external-configured",
					"excludedPlugins": "admet,markush,knowledge,daily-briefing,molecular,pcc",
				},
			},
		},
		Workflows: []Workflow{
			{
				ID:      "browser-bridge-runtime",
				Label:   "Browser Bridge Runtime",
				Status:  status,
				Summary: "Connect Synon Link WebSocket before browser workspace, navigation, read, interaction, screenshot, download, local-file, or bookmark work is available.",
				Steps:   []string{"SynonLink:synon_link_ws_connect", "SynonLink:handle_synon_link_hello", "SynonLink:open_tab", "SynonLink:search_web", "SynonLink:read_page", "SynonLink:scroll_page", "SynonLink:visual_element_map", "SynonLink:click_at", "SynonLink:type", "SynonLink:screenshot", "SynonLink:download_file", "SynonLink:local_files_manifest", "SynonLink:bookmarks_profile", "SynonLink:track_synon_link_progress", "SynonLink:complete_synon_link_command"},
			},
			{
				ID:      "im-message-normalization",
				Label:   "IM Message Normalization",
				Status:  "started",
				Summary: "Normalize IM text, attachments, tool display, permission prompts, pairing, duplicate messages, adapter WebSocket bridge traffic, shared outbound retry/deduplication/result reporting, platform delivery telemetry reports with render policies, durable IM live-session journal entries, WeChat server-message outbound delivery, WeChat QR login/polling/event updates, and Feishu event/CardKit/media updates before platform delivery or task creation.",
				Steps:   prefixedSteps("IM", imActions),
			},
			{
				ID:      "runtime-event-replay",
				Label:   "Runtime Event Replay",
				Status:  "started",
				Summary: "Persist session index metadata, visible server events, runner handoff leases, priority backlog state, and namespaced runtime values, then replay events by cursor or client message id after reconnects.",
				Steps:   []string{"Runtime:upsert_session", "Runtime:append_session_message", "Runtime:append_session_event", "Runtime:replay_session_events", "Runtime:claim_session_runner", "Runtime:heartbeat_session_runner", "Runtime:release_session_runner", "Runtime:pick_session_runner", "Runtime:summarize_session_runner_queue", "Runtime:summarize_session_runner_backlog", "Runtime:runtime_set", "Runtime:runtime_get", "Runtime:runtime_list", "Runtime:runtime_delete"},
			},
			{
				ID:      "tool-execution-contracts",
				Label:   "Tool Execution Contracts",
				Status:  "started",
				Summary: "List and execute retained tools, including capability-gated Synon Link browser actions and TaskRun direct system steps for web_research, lsp_diagnostics, patch, read_files, notebook_edit, record_artifact, shell_command, and visual_review.",
				Steps:   prefixedSteps("Tools", toolRegistryActions),
			},
			{
				ID:      "plugin-host-runtime",
				Label:   "Plugin Host Runtime",
				Status:  "started",
				Summary: "Expose the retained Synon native plugin plus configured external plugin manifests as a Go-managed API module surface, and supervise declared external plugin child processes.",
				Steps:   []string{"Plugins:list_plugins", "Plugins:read_plugin_manifest", "Plugins:load_external_plugin_manifest", "Plugins:start_external_plugin_process", "Plugins:stop_external_plugin_process", "Plugins:route_synon_plugin_capabilities", "Plugins:route_synon_link_download"},
			},
		},
	}
}

func (r Report) ContainsLabel(label string) bool {
	for _, module := range r.Modules {
		if module.Label == label {
			return true
		}
	}
	for _, workflow := range r.Workflows {
		if workflow.Label == label {
			return true
		}
	}
	return false
}

func (r Report) ContainsModule(id string) bool {
	for _, module := range r.Modules {
		if module.ID == id {
			return true
		}
	}
	return false
}

func (r Report) ContainsAction(action string) bool {
	for _, module := range r.Modules {
		for _, candidate := range module.Actions {
			if candidate == action {
				return true
			}
		}
	}
	return false
}

func (r Report) ContainsWorkflow(id string) bool {
	for _, workflow := range r.Workflows {
		if workflow.ID == id {
			return true
		}
	}
	return false
}

func mergeUnique(first []string, rest []string) []string {
	seen := make(map[string]struct{}, len(first)+len(rest))
	merged := make([]string, 0, len(first)+len(rest))
	for _, item := range append(first, rest...) {
		if item == "" {
			continue
		}
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		merged = append(merged, item)
	}
	return merged
}

func prefixedSteps(prefix string, actions []string) []string {
	steps := make([]string, 0, len(actions))
	for _, action := range actions {
		if action == "" {
			continue
		}
		steps = append(steps, prefix+":"+action)
	}
	return steps
}

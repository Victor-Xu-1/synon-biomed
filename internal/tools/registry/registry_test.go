package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	"synon-go/internal/toolcontract"
	"synon-go/internal/tools/webfetch"
	"synon-go/internal/tools/websearch"
)

func TestRegistryRejectsCompetingToolRegistrations(t *testing.T) {
	_, err := Build([]Tool{
		{Name: "source_reader", Executable: true},
		{Name: "SOURCE_READER", Executable: true},
	})
	if err == nil {
		t.Fatal("registry silently accepted competing case-insensitive Tool identities")
	}
	registry, err := Build([]Tool{{Name: "source_reader", Executable: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(
		Tool{Name: "new_reader", Executable: true},
		Tool{Name: "SOURCE_READER", Executable: true},
	); err == nil {
		t.Fatal("incremental registration silently accepted a competing Tool identity")
	}
	if _, found := registry.Get("new_reader"); found {
		t.Fatal("failed incremental registration partially mutated the registry")
	}
}

func TestCanonicalRegistryOwnsModelExposure(t *testing.T) {
	catalogs := DefaultCatalogs()

	search, found := catalogs.ModelTools.Get("web_search")
	if !found || search.Exposure != ToolExposureDirect {
		t.Fatalf("web_search exposure=%q found=%v", search.Exposure, found)
	}
	patent, found := catalogs.Operations.Get("patent_search")
	if !found || patent.Exposure != ToolExposureDeferred {
		t.Fatalf("patent_search exposure=%q found=%v", patent.Exposure, found)
	}
	research, found := catalogs.Operations.Get("web_research")
	if !found || research.Exposure != ToolExposureHidden {
		t.Fatalf("compound web_research exposure=%q found=%v", research.Exposure, found)
	}
}

func testWebFetchOptions() webfetch.Options {
	return webfetch.Options{ClientForURL: func(context.Context, string) (*http.Client, error) {
		return &http.Client{}, nil
	}}
}

func TestDefaultRegistrySeparatesModelToolsFromServiceOperations(t *testing.T) {
	reg := Default()
	operations := DefaultOperations()
	for _, name := range []string{"web_fetch", "WebFetch", "WebSearch", "binding_mode_analysis", "LSP", "synon_link", "SynonLink", "ToolSearch", "SkillSearch", "Skill", "AgentRuntimeDoctor", "approval_remembered_list", "approval_remembered_revoke", "SendMessage", "SendUserMessage", "Brief", "ToolDoctor", "ListMcpTools", "ListMcpResourcesTool", "ReadMcpResourceTool", "MCPTool", "mcp", "file_list", "file_info", "file_read", "Read", "file_read_batch", "ReadBatch", "file_write", "Write", "NotebookEdit", "file_mkdir", "file_copy", "file_move", "file_delete", "file_search", "Glob", "glob", "Grep", "grep", "file_replace", "Edit", "file_patch", "Patch", "json_patch", "code_index", "code_references", "shell_exec", "Bash", "Shell", "powershell", "PowerShell", "Sleep", "sleep", "ask_user", "Agent", "Task", "generate_plan", "update_step_status", "CronCreate", "CronUpdate", "CronList", "CronDelete", "cron_tick", "TeamCreate", "TeamDelete", "TaskRun", "task_create", "TaskCreate", "TaskGet", "task_update", "TaskUpdate", "task_list", "TaskList", "TaskOutput", "AgentOutputTool", "BashOutputTool", "TaskStop", "KillShell", "TodoWrite", "todo_write", "settings_set", "settings_get", "settings_list", "Config", "Compact", "session_compact", "pairing_list", "pairing_allow", "pairing_revoke", "runtime_set", "runtime_get", "runtime_list", "runtime_delete", "session_store", "session_list", "session_get", "session_replay", "session_append", "session_claim", "session_heartbeat", "session_release", "session_runner_checkpoint", "session_runner_next", "session_runner_finish", "session_runner_pick", "session_runner_queue", "session_runner_backlog", "session_bind_project", "session_fork", "session_rewind", "session_export", "session_event_journal", "artifact_register", "artifact_list", "artifact_get", "StructuredOutput", "EnterWorktree", "ExitWorktree", "im_message", "im_config"} {
		if _, ok := reg.Get(name); !ok {
			if name == "ToolSearch" {
				continue
			}
			if _, retired := toolcontract.CanonicalRuntimeAlias(name); retired {
				continue
			}
			t.Fatalf("missing tool %q in %#v", name, reg.Names())
		}
	}
	if _, ok := reg.Get("ToolSearch"); ok {
		t.Fatal("retired ToolSearch remains registered")
	}
	for _, alias := range []string{"WebFetch", "WebSearch", "WebResearch", "SynonLink", "AgentOutputTool", "BashOutputTool", "Brief", "KillShell", "ListMcpToolsTool", "PowerShell", "Skill", "SkillSearch", "Task", "file_read_batch", "glob", "grep", "mcp", "session_compact", "sleep", "todo_write"} {
		if _, ok := reg.Get(alias); ok {
			t.Fatalf("retired alias %q remains registered", alias)
		}
	}
	if got := len(reg.Names()); got != 11 {
		t.Fatalf("model tool registry size = %d, want 11: %#v", got, reg.Names())
	}
	if got := len(operations.Names()); got != 102 {
		t.Fatalf("service operation catalog size = %d, want 102", got)
	}
	for _, name := range operations.Names() {
		if _, leaked := reg.tools[name]; leaked {
			t.Fatalf("service operation %q leaked into model tool storage", name)
		}
	}
	for _, alias := range []string{"AskUserQuestion", "ask_user_question"} {
		if _, ok := reg.Get(alias); ok {
			t.Fatalf("AskUser alias %q leaked into the exact registry", alias)
		}
	}
	for _, name := range reg.Names() {
		if name == "AskUserQuestion" || name == "ask_user_question" {
			t.Fatalf("legacy AskUser alias %q was advertised", name)
		}
	}
	for _, removed := range []string{"admet", "pcc_selector", "markush", "molecular_modeling", "synon_knowledge", "daily_briefing"} {
		if _, ok := reg.Get(removed); ok {
			t.Fatalf("removed tool %q is still registered", removed)
		}
	}
}

func TestSplitPreservesAttachedDeferredAuthority(t *testing.T) {
	attached := Default()
	resplit := Split(attached)
	if _, found := resplit.Operations.Get("web_research"); !found {
		t.Fatal("deferred Tool was not retained in the operation partition")
	}
	resplit.ModelTools.AttachServiceOperations(resplit.Operations)
	if _, found := resplit.ModelTools.Get("web_research"); !found {
		t.Fatal("re-splitting an attached registry discarded deferred Tool authority")
	}
	if got, want := len(resplit.ModelTools.Names()), len(attached.Names()); got != want {
		t.Fatalf("model partition size=%d want=%d", got, want)
	}
}

func TestTaskReadToolDescriptionsDisambiguateFrameAndBackgroundTaskIDs(t *testing.T) {
	reg := Default()
	for _, test := range []struct {
		name     string
		required []string
	}{
		{name: "TaskGet", required: []string{"task-*", "UUID-shaped frame/conversation IDs", "trusted runtime context"}},
		{name: "TaskList", required: []string{"task-*", "not the Synon Biomed project or conversation list", "UUID-shaped frame IDs"}},
	} {
		tool, ok := reg.Get(test.name)
		if !ok {
			t.Fatalf("%s is not registered", test.name)
		}
		for _, required := range test.required {
			if !strings.Contains(tool.Description, required) {
				t.Fatalf("%s description missing %q: %q", test.name, required, tool.Description)
			}
		}
	}
}

func TestWebSearchContractsAdvertiseLayeredBroadBilingualResearch(t *testing.T) {
	reg := Default()
	search, ok := reg.Get("web_search")
	if !ok {
		t.Fatal("web_search is not registered")
	}
	for _, required := range []string{"Chinese and English", "default returns up to 50", "request fewer", "up to 200", "source quality", "deduplicated"} {
		if !strings.Contains(search.Description, required) {
			t.Fatalf("web_search description missing %q: %q", required, search.Description)
		}
	}
	if strings.Contains(search.Description, "dominant language") {
		t.Fatalf("stale single-language guidance remains: %q", search.Description)
	}
	research, ok := reg.Get("web_research")
	if !ok {
		t.Fatal("web_research is not registered")
	}
	for _, required := range []string{"ranked and deduplicated candidate pool", "max_sources controls full-text synthesis"} {
		if !strings.Contains(research.Description, required) {
			t.Fatalf("web_research description missing %q: %q", required, research.Description)
		}
	}
}

func TestTodoWriteSchemaExposesTheValidatedItemContract(t *testing.T) {
	reg := Default()
	for _, name := range []string{"TodoWrite"} {
		tool, ok := reg.Get(name)
		if !ok {
			t.Fatalf("missing %s", name)
		}
		todos := tool.Input["todos"]
		items, ok := todos.Schema["items"].(map[string]any)
		if !todos.Required || todos.Type != "array" || !ok {
			t.Fatalf("%s todos schema = %#v", name, todos)
		}
		properties, ok := items["properties"].(map[string]any)
		if !ok || items["additionalProperties"] != false {
			t.Fatalf("%s item schema = %#v", name, items)
		}
		for _, field := range []string{"content", "status", "activeForm"} {
			if _, ok := properties[field].(map[string]any); !ok {
				t.Fatalf("%s item property %s = %#v", name, field, properties[field])
			}
		}
		for _, field := range []string{"content", "activeForm"} {
			if got := properties[field].(map[string]any)["pattern"]; got != `\S` {
				t.Fatalf("%s item property %s pattern = %#v", name, field, got)
			}
		}
		status := properties["status"].(map[string]any)
		if got := status["enum"]; len(got.([]string)) != 3 {
			t.Fatalf("%s status enum = %#v", name, got)
		}
		required, ok := items["required"].([]string)
		if !ok || strings.Join(required, ",") != "content,status,activeForm" {
			t.Fatalf("%s required fields = %#v", name, items["required"])
		}
	}
}

func TestRegistryValidatesRequiredInputs(t *testing.T) {
	reg := Default()
	if err := reg.Validate("web_fetch", map[string]any{}); err == nil {
		t.Fatal("web_fetch without url should fail")
	}
	if err := reg.Validate("web_fetch", map[string]any{"url": "https://example.com"}); err != nil {
		t.Fatalf("web_fetch valid input error = %v", err)
	}
	if err := reg.Validate("web_fetch", map[string]any{"url": "https://example.com", "prompt": "Extract title"}); err != nil {
		t.Fatalf("web_fetch compatible prompt input error = %v", err)
	}
	if err := reg.Validate("web_fetch", map[string]any{
		"url": "https://example.com/file.pdb", "limit": webfetch.MaxResponseLimit + 1,
	}); err == nil {
		t.Fatal("web_fetch schema admitted a response limit above the transport ceiling")
	}
	if err := reg.Validate("LSP", map[string]any{"operation": "workspaceSymbol", "query": "Server"}); err != nil {
		t.Fatalf("LSP valid input error = %v", err)
	}
	if err := reg.Validate("LSP", map[string]any{}); err == nil {
		t.Fatal("LSP without operation should fail")
	}
	if err := reg.Validate("NotebookEdit", map[string]any{"notebook_path": "analysis.ipynb", "new_source": "print('ok')"}); err != nil {
		t.Fatalf("NotebookEdit valid input error = %v", err)
	}
	if err := reg.Validate("NotebookEdit", map[string]any{"notebook_path": "analysis.ipynb"}); err == nil {
		t.Fatal("NotebookEdit without new_source should fail")
	}
	for _, name := range []string{"web_search"} {
		if err := reg.Validate(name, map[string]any{"query": "Synon agent", "allowed_domains": []any{"example.com"}, "max_results": float64(3)}); err != nil {
			t.Fatalf("%s valid input error = %v", name, err)
		}
		if err := reg.Validate(name, map[string]any{}); err == nil {
			t.Fatalf("%s without query should fail", name)
		}
	}
	for _, name := range []string{"web_research"} {
		if err := reg.Validate(name, map[string]any{"operation": "fetch", "url": "https://example.com"}); err != nil {
			t.Fatalf("%s valid fetch input error = %v", name, err)
		}
		if err := reg.Validate(name, map[string]any{"url": "https://example.com"}); err == nil {
			t.Fatalf("%s without operation should fail", name)
		}
	}
	for _, name := range []string{"web_research"} {
		if err := reg.Validate(name, map[string]any{"operation": "research", "query": "MCL1"}); err == nil {
			t.Fatalf("%s must advertise only canonical WebResearch operations", name)
		}
		tool, ok := reg.Get(name)
		if !ok || tool.Input["operation"].Schema["default"] != "search_and_fetch" {
			t.Fatalf("%s operation schema = %#v", name, tool.Input["operation"])
		}
	}
	if err := reg.Validate("VisualReview", map[string]any{"objective": "inspect screenshot", "image_path": "shot.png"}); err != nil {
		t.Fatalf("VisualReview valid input error = %v", err)
	}
	if err := reg.Validate("VisualReview", map[string]any{"image_path": "shot.png"}); err == nil {
		t.Fatal("VisualReview without objective should fail")
	}
	visualReview, ok := reg.Get("VisualReview")
	if !ok || !strings.Contains(visualReview.Description, "Canonical top-level tool") ||
		!strings.Contains(visualReview.Description, "Do not invoke it solely") ||
		!strings.Contains(visualReview.Description, "host.view_image") {
		t.Fatalf("VisualReview description does not declare its canonical boundary: %#v", visualReview)
	}
	if err := reg.Validate("synon_link", map[string]any{"clientId": "sl_1"}); err == nil {
		t.Fatal("synon_link without action should fail")
	}
	for _, name := range []string{"synon_link"} {
		if err := reg.Validate(name, map[string]any{"userId": "user-1", "clientId": "sl_1", "action": "desktop_status"}); err == nil {
			t.Fatalf("%s accepted removed desktop action", name)
		}
		tool, ok := reg.Get(name)
		if !ok || strings.Contains(tool.Description, "desktop") || strings.Contains(strings.Join(tool.Capabilities, ","), "local-device") {
			t.Fatalf("%s still advertises computer operation: %#v", name, tool)
		}
	}
	if err := reg.Validate("file_info", map[string]any{"path": "notes.txt"}); err != nil {
		t.Fatalf("file_info valid input error = %v", err)
	}
	if err := reg.Validate("file_read", map[string]any{"path": "notes.txt", "limit": 20, "encoding": "base64"}); err != nil {
		t.Fatalf("file_read valid input error = %v", err)
	}
	if err := reg.Validate("Read", map[string]any{"file_path": "notes.txt", "offset": float64(1), "limit": float64(20)}); err != nil {
		t.Fatalf("Read valid input error = %v", err)
	}
	if err := reg.Validate("Read", map[string]any{"limit": float64(20)}); err == nil {
		t.Fatal("Read without file_path should fail")
	}
	if err := reg.Validate("ReadBatch", map[string]any{"file_paths": []string{"a.txt", "b.txt"}, "max_bytes_per_file": float64(1024), "max_files": float64(2)}); err != nil {
		t.Fatalf("ReadBatch valid input error = %v", err)
	}
	if err := reg.Validate("file_read_batch", map[string]any{"file_paths": []string{"a.txt"}}); err == nil {
		t.Fatal("retired file_read_batch alias remains in exact registry")
	}
	if err := reg.Validate("file_write", map[string]any{"path": "notes.txt"}); err == nil {
		t.Fatal("file_write without content should fail")
	}
	if err := reg.Validate("file_write", map[string]any{"path": "notes.txt", "content": "YWJj", "encoding": "base64", "overwrite": false}); err != nil {
		t.Fatalf("file_write encoded input error = %v", err)
	}
	if err := reg.Validate("Write", map[string]any{"file_path": "notes.txt", "content": "abc"}); err != nil {
		t.Fatalf("Write valid input error = %v", err)
	}
	if err := reg.Validate("Write", map[string]any{"file_path": "notes.txt"}); err == nil {
		t.Fatal("Write without content should fail")
	}
	if err := reg.Validate("file_write", map[string]any{"path": "notes.txt", "content": "abc", "overwrite": "false"}); err == nil {
		t.Fatal("file_write with string overwrite should fail")
	}
	if err := reg.Validate("file_mkdir", map[string]any{"path": "archive", "recursive": true}); err != nil {
		t.Fatalf("file_mkdir valid input error = %v", err)
	}
	if err := reg.Validate("file_copy", map[string]any{"path": "notes.txt", "targetPath": "archive/notes.txt", "overwrite": true}); err != nil {
		t.Fatalf("file_copy valid input error = %v", err)
	}
	if err := reg.Validate("file_move", map[string]any{"path": "notes.txt", "targetPath": "archive/notes.txt", "overwrite": false}); err != nil {
		t.Fatalf("file_move valid input error = %v", err)
	}
	if err := reg.Validate("file_delete", map[string]any{"path": "archive/notes.txt", "recursive": false}); err != nil {
		t.Fatalf("file_delete valid input error = %v", err)
	}
	if err := reg.Validate("file_delete", map[string]any{"path": "archive/notes.txt", "recursive": "false"}); err == nil {
		t.Fatal("file_delete with string recursive should fail")
	}
	if err := reg.Validate("shell_exec", map[string]any{"command": "printf", "args": []string{"ok"}, "timeout": 2}); err != nil {
		t.Fatalf("shell_exec valid input error = %v", err)
	}
	if err := reg.Validate("Bash", map[string]any{"command": "printf ok", "description": "Print ok", "run_in_background": false, "timeout": float64(2000)}); err != nil {
		t.Fatalf("Bash valid input error = %v", err)
	}
	if err := reg.Validate("Bash", map[string]any{"command": "printf ok", "run_in_background": "yes"}); err == nil {
		t.Fatal("Bash with non-boolean run_in_background should fail")
	}
	if err := reg.Validate("Shell", map[string]any{"shell": "bash", "command": "printf ok", "workdir": ".", "timeout": float64(2)}); err != nil {
		t.Fatalf("Shell valid input error = %v", err)
	}
	if err := reg.Validate("Shell", map[string]any{"shell": float64(1), "command": "printf ok"}); err == nil {
		t.Fatal("Shell with non-string shell route should fail")
	}
	if err := reg.Validate("powershell", map[string]any{"command": "Write-Output ok", "description": "Print ok", "timeout": float64(2)}); err != nil {
		t.Fatalf("powershell valid input error = %v", err)
	}
	if err := reg.Validate("Bash", map[string]any{"description": "Missing command"}); err == nil {
		t.Fatal("Bash without command should fail")
	}
	if err := reg.Validate("Sleep", map[string]any{"durationMs": float64(5)}); err != nil {
		t.Fatalf("Sleep valid input error = %v", err)
	}
	if err := reg.Validate("Sleep", map[string]any{"durationMs": "5"}); err == nil {
		t.Fatal("Sleep with string durationMs should fail")
	}
	if err := reg.Validate("ask_user", map[string]any{"question": "继续哪一块？", "header": "方向", "options": []any{
		map[string]any{
			"label": "消息通道", "description": "继续消息通道。", "pros": "改善外部交付", "cons": "不修复工具契约",
			"readiness": "尚未核验渠道凭据。", "readiness_status": "unverified",
			"decision_evidence": []any{"user-input:current-task"}, "readiness_evidence": []any{}, "selection_basis": "user_objective",
			"expected_outcome":    "可用的消息通道。",
			"selection_rationale": "适合外部交付优先的场景。", "recommended": false,
		},
		map[string]any{
			"label": "工具契约", "description": "继续工具契约。", "pros": "直接减少调用失败", "cons": "不改善外部渠道",
			"readiness": "该调用不证明执行环境就绪。", "readiness_status": "unverified",
			"decision_evidence": []any{"tool-call:tool-contract"}, "readiness_evidence": []any{}, "selection_basis": "scientific_evidence",
			"expected_outcome":    "经过验证的工具契约。",
			"selection_rationale": "推荐：直接对应当前失败边界。", "recommended": true,
		},
	}}); err != nil {
		t.Fatalf("ask_user valid input error = %v", err)
	}
	askUser, ok := reg.Get("ask_user")
	if !ok || askUser.Input["options"].Schema["items"] == nil ||
		askUser.Input["options"].Schema["minItems"] != 2 ||
		askUser.Input["options"].Schema["maxItems"] != 4 {
		t.Fatalf("ask_user must expose its direct option contract: %#v", askUser.Input["options"])
	}
	if !strings.Contains(askUser.Description, "two or more viable choices") ||
		!strings.Contains(askUser.Description, "canonical typed evidence fields") ||
		!strings.Contains(askUser.Description, "CPU, memory, and GPU/VRAM") ||
		!strings.Contains(askUser.Description, "ordinary completed tool calls never prove") ||
		!strings.Contains(askUser.Description, "service-issued typed readiness attestation") ||
		!strings.Contains(askUser.Description, "internal execution identifiers") ||
		!strings.Contains(askUser.Description, "reversible routine detail") {
		t.Fatalf("ask_user autonomy boundary is missing: %q", askUser.Description)
	}
	if err := reg.Validate("ask_user", map[string]any{"questions": []any{
		map[string]any{"question": "Which structure?", "header": "Structure", "options": []any{map[string]any{"label": "Latest"}, map[string]any{"label": "Best resolution"}}},
	}}); err == nil {
		t.Fatal("ask_user accepted the retired questions wrapper")
	}
	for _, alias := range []string{"AskUserQuestion", "ask_user_question"} {
		if err := reg.Validate(alias, map[string]any{"answers": map[string]any{}}); err == nil {
			t.Fatalf("%s without questions should fail", alias)
		}
	}
	if err := reg.Validate("file_search", map[string]any{"path": ".", "query": "TODO"}); err != nil {
		t.Fatalf("file_search valid input error = %v", err)
	}
	if err := reg.Validate("Glob", map[string]any{"pattern": "**/*.go", "path": ".", "limit": float64(20)}); err != nil {
		t.Fatalf("Glob valid input error = %v", err)
	}
	if err := reg.Validate("Glob", map[string]any{"path": "."}); err == nil {
		t.Fatal("Glob without pattern should fail")
	}
	if err := reg.Validate("Grep", map[string]any{"pattern": "Run", "path": ".", "glob": "**/*.go", "output_mode": "content", "-C": float64(1), "-n": true, "-i": true, "head_limit": float64(10), "offset": float64(0)}); err != nil {
		t.Fatalf("Grep valid input error = %v", err)
	}
	if err := reg.Validate("Grep", map[string]any{"path": "."}); err == nil {
		t.Fatal("Grep without pattern should fail")
	}
	if err := reg.Validate("file_replace", map[string]any{"path": "notes.txt", "old": "TODO", "new": "DONE"}); err != nil {
		t.Fatalf("file_replace valid input error = %v", err)
	}
	if err := reg.Validate("Edit", map[string]any{"file_path": "notes.txt", "old_string": "TODO", "new_string": "DONE", "replace_all": true}); err != nil {
		t.Fatalf("Edit valid input error = %v", err)
	}
	if err := reg.Validate("Edit", map[string]any{"file_path": "notes.txt", "old_string": "TODO"}); err == nil {
		t.Fatal("Edit without new_string should fail")
	}
	if err := reg.Validate("Patch", map[string]any{"patch": "--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-old\n+new\n", "dry_run": true}); err != nil {
		t.Fatalf("Patch valid input error = %v", err)
	}
	if err := reg.Validate("Patch", map[string]any{"dry_run": true}); err == nil {
		t.Fatal("Patch without patch should fail")
	}
	if err := reg.Validate("json_patch", map[string]any{"path": "config.json", "operations": []any{map[string]any{"op": "replace", "path": "/enabled", "value": true}}}); err != nil {
		t.Fatalf("json_patch valid input error = %v", err)
	}
	if err := reg.Validate("json_patch", map[string]any{"path": "config.json"}); err == nil {
		t.Fatal("json_patch without operations should fail")
	}
	if err := reg.Validate("code_index", map[string]any{"path": ".", "extensions": []string{".go"}, "symbolKinds": []string{"func"}, "query": "Run", "symbolLimit": float64(5)}); err != nil {
		t.Fatalf("code_index filtered input error = %v", err)
	}
	if err := reg.Validate("code_references", map[string]any{"path": ".", "symbol": "Run", "extensions": []string{".go"}, "limit": float64(20), "contextLines": float64(2)}); err != nil {
		t.Fatalf("code_references valid input error = %v", err)
	}
	if err := reg.Validate("code_references", map[string]any{"path": "."}); err == nil {
		t.Fatal("code_references without symbol should fail")
	}
	if err := reg.Validate("task_create", map[string]any{"title": "ship"}); err != nil {
		t.Fatalf("task_create valid input error = %v", err)
	}
	if err := reg.Validate("TaskRun", map[string]any{"action": "start", "objective": "Ship durable task", "success_criteria": []any{"returns a run id"}, "task_graph": map[string]any{"steps": []any{}}}); err != nil {
		t.Fatalf("TaskRun valid input error = %v", err)
	}
	if err := reg.Validate("TaskRun", map[string]any{"task_graph": "not-object"}); err == nil {
		t.Fatal("TaskRun with string task_graph should fail")
	}
	if err := reg.Validate("Agent", map[string]any{"description": "Inspect release", "prompt": "Check the release package", "subagent_type": "general-purpose", "run_in_background": true}); err != nil {
		t.Fatalf("Agent valid input error = %v", err)
	}
	if err := reg.Validate("Agent", map[string]any{"description": "Missing prompt"}); err == nil {
		t.Fatal("Agent without prompt should fail")
	}
	if err := reg.Validate("generate_plan", map[string]any{
		"human_description": "Preparing the plan", "task_summary": "Inspect and verify",
		"phases": []any{map[string]any{"name": "Execution", "delegations": []any{map[string]any{
			"name": "Runtime", "steps": []any{map[string]any{"title": "Inspect", "description": "Read the supported interface."}},
		}}}},
		"desired_outputs": []any{"verified report"},
		"feasibility":     map[string]any{"confidence": "high", "rationale": "The required interfaces are available."},
	}); err != nil {
		t.Fatalf("generate_plan valid input error = %v", err)
	}
	if err := reg.Validate("update_step_status", map[string]any{
		"human_description": "Starting inspection", "step": "step-1", "status": "in_progress",
	}); err != nil {
		t.Fatalf("update_step_status valid input error = %v", err)
	}
	for _, retired := range []string{"EnterPlanMode", "ExitPlanMode"} {
		if err := reg.Validate(retired, map[string]any{}); err == nil {
			t.Fatalf("retired plan tool %s remains registered", retired)
		}
	}
	if err := reg.Validate("CronCreate", map[string]any{"cron": "*/5 * * * *", "prompt": "Check release status", "recurring": true, "durable": false}); err != nil {
		t.Fatalf("CronCreate valid input error = %v", err)
	}
	if err := reg.Validate("CronCreate", map[string]any{"cron": "*/5 * * * *"}); err == nil {
		t.Fatal("CronCreate without prompt should fail")
	}
	if err := reg.Validate("CronUpdate", map[string]any{"id": "cron-1", "cron": "0 9 * * *", "prompt": "Daily status", "recurring": false, "worktree": true}); err != nil {
		t.Fatalf("CronUpdate valid input error = %v", err)
	}
	if err := reg.Validate("CronUpdate", map[string]any{"cron": "0 9 * * *"}); err == nil {
		t.Fatal("CronUpdate without id should fail")
	}
	if err := reg.Validate("CronList", map[string]any{}); err != nil {
		t.Fatalf("CronList valid input error = %v", err)
	}
	if err := reg.Validate("CronDelete", map[string]any{"id": "cron-1"}); err != nil {
		t.Fatalf("CronDelete valid input error = %v", err)
	}
	if err := reg.Validate("CronDelete", map[string]any{}); err == nil {
		t.Fatal("CronDelete without id should fail")
	}
	if err := reg.Validate("cron_tick", map[string]any{"now": "2026-07-09T12:05:00Z", "limit": float64(10)}); err != nil {
		t.Fatalf("cron_tick valid input error = %v", err)
	}
	if err := reg.Validate("cron_tick", map[string]any{"limit": "10"}); err == nil {
		t.Fatal("cron_tick with string limit should fail")
	}
	if err := reg.Validate("TeamCreate", map[string]any{"team_name": "release-team", "description": "Coordinate release", "agent_type": "release-lead"}); err != nil {
		t.Fatalf("TeamCreate valid input error = %v", err)
	}
	if err := reg.Validate("TeamCreate", map[string]any{"description": "missing name"}); err == nil {
		t.Fatal("TeamCreate without team_name should fail")
	}
	if err := reg.Validate("TeamDelete", map[string]any{}); err != nil {
		t.Fatalf("TeamDelete valid input error = %v", err)
	}
	if err := reg.Validate("Compact", map[string]any{"sessionId": "session-1", "instructions": "keep task state", "trigger": "manual", "limit": float64(120)}); err != nil {
		t.Fatalf("Compact valid input error = %v", err)
	}
	if err := reg.Validate("Compact", map[string]any{"instructions": "missing session"}); err == nil {
		t.Fatal("Compact without sessionId should fail")
	}
	if err := reg.Validate("skill", map[string]any{"skill": "synon-runtime", "args": "load runtime instructions"}); err != nil {
		t.Fatalf("skill valid input error = %v", err)
	}
	if err := reg.Validate("skill", map[string]any{"args": "missing skill"}); err == nil {
		t.Fatal("skill without skill name should fail")
	}
	if err := reg.Validate("approval_remembered_list", map[string]any{"source": "agent-runtime"}); err != nil {
		t.Fatalf("approval_remembered_list valid input error = %v", err)
	}
	if err := reg.Validate("approval_remembered_revoke", map[string]any{"key": "agent-runtime|agent-runtime|action"}); err != nil {
		t.Fatalf("approval_remembered_revoke valid input error = %v", err)
	}
	if err := reg.Validate("approval_remembered_revoke", map[string]any{"key": 1}); err == nil {
		t.Fatal("approval_remembered_revoke with numeric key should fail")
	}
	if err := reg.Validate("TaskCreate", map[string]any{"subject": "Ship release", "description": "Prepare release package", "activeForm": "Shipping release", "metadata": map[string]any{"priority": "high"}}); err != nil {
		t.Fatalf("TaskCreate valid input error = %v", err)
	}
	if err := reg.Validate("TaskCreate", map[string]any{"subject": "Ship release"}); err == nil {
		t.Fatal("TaskCreate without description should fail")
	}
	if err := reg.Validate("TaskGet", map[string]any{"taskId": "task-1"}); err != nil {
		t.Fatalf("TaskGet valid input error = %v", err)
	}
	if err := reg.Validate("task_update", map[string]any{"status": "done"}); err == nil {
		t.Fatal("task_update without id should fail")
	}
	if err := reg.Validate("TaskUpdate", map[string]any{"taskId": "task-1", "status": "completed", "owner": "agent-a", "addBlockedBy": []string{"task-0"}, "metadata": map[string]any{"priority": nil}}); err != nil {
		t.Fatalf("TaskUpdate valid input error = %v", err)
	}
	if err := reg.Validate("TaskUpdate", map[string]any{"status": "completed"}); err == nil {
		t.Fatal("TaskUpdate without taskId should fail")
	}
	if err := reg.Validate("TaskList", map[string]any{}); err != nil {
		t.Fatalf("TaskList valid input error = %v", err)
	}
	if err := reg.Validate("TaskOutput", map[string]any{"task_id": "task-1", "block": false, "timeout": float64(30000)}); err != nil {
		t.Fatalf("TaskOutput valid input error = %v", err)
	}
	if err := reg.Validate("TaskOutput", map[string]any{"block": false}); err == nil {
		t.Fatal("TaskOutput without task_id should fail")
	}
	if err := reg.Validate("TaskStop", map[string]any{"task_id": "task-1"}); err != nil {
		t.Fatalf("TaskStop valid input error = %v", err)
	}
	if err := reg.Validate("TaskStop", map[string]any{"task_id": 1}); err == nil {
		t.Fatal("TaskStop with numeric task_id should fail")
	}
	if err := reg.Validate("TodoWrite", map[string]any{"todos": []any{map[string]any{"content": "inspect original todo contract", "status": "in_progress", "activeForm": "Inspecting original todo contract"}}}); err != nil {
		t.Fatalf("TodoWrite valid input error = %v", err)
	}
	if err := reg.Validate("settings_set", map[string]any{"key": "theme", "value": "dark"}); err != nil {
		t.Fatalf("settings_set valid input error = %v", err)
	}
	if err := reg.Validate("settings_get", map[string]any{}); err == nil {
		t.Fatal("settings_get without key should fail")
	}
	if err := reg.Validate("Config", map[string]any{"setting": "theme", "value": "dark"}); err != nil {
		t.Fatalf("Config valid input error = %v", err)
	}
	if err := reg.Validate("Config", map[string]any{"value": "dark"}); err == nil {
		t.Fatal("Config without setting should fail")
	}
	if err := reg.Validate("ToolSearch", map[string]any{"query": "task"}); err == nil {
		t.Fatal("retired ToolSearch remains registered")
	}
	if err := reg.Validate("SendMessage", map[string]any{"to": "release-agent", "summary": "Check release", "message": "Please inspect the package"}); err != nil {
		t.Fatalf("SendMessage valid string input error = %v", err)
	}
	if err := reg.Validate("SendMessage", map[string]any{"to": "lead", "message": map[string]any{"type": "shutdown_response", "request_id": "req-1", "approve": true}}); err != nil {
		t.Fatalf("SendMessage valid structured input error = %v", err)
	}
	if err := reg.Validate("SendMessage", map[string]any{"summary": "missing target", "message": "hello"}); err == nil {
		t.Fatal("SendMessage without to should fail")
	}
	if err := reg.Validate("SendUserMessage", map[string]any{"message": "done", "status": "normal", "attachments": []string{"notes.txt"}}); err != nil {
		t.Fatalf("SendUserMessage valid input error = %v", err)
	}
	if err := reg.Validate("SendUserMessage", map[string]any{"status": "normal"}); err == nil {
		t.Fatal("SendUserMessage without message should fail")
	}
	if err := reg.Validate("ToolDoctor", map[string]any{"scope": "tools", "smoke": true}); err != nil {
		t.Fatalf("ToolDoctor valid input error = %v", err)
	}
	if err := reg.Validate("ToolDoctor", map[string]any{"smoke": "true"}); err == nil {
		t.Fatal("ToolDoctor with string smoke should fail")
	}
	if err := reg.Validate("ListMcpTools", map[string]any{"server": "local"}); err != nil {
		t.Fatalf("ListMcpTools valid input error = %v", err)
	}
	if err := reg.Validate("ListMcpResourcesTool", map[string]any{"server": "local"}); err != nil {
		t.Fatalf("ListMcpResourcesTool valid input error = %v", err)
	}
	if err := reg.Validate("ReadMcpResourceTool", map[string]any{"server": "local", "uri": "resource://one"}); err != nil {
		t.Fatalf("ReadMcpResourceTool valid input error = %v", err)
	}
	if err := reg.Validate("ReadMcpResourceTool", map[string]any{"server": "local"}); err == nil {
		t.Fatal("ReadMcpResourceTool without uri should fail")
	}
	if err := reg.Validate("MCPTool", map[string]any{"server": "local", "toolName": "echo", "input": map[string]any{"text": "ok"}}); err != nil {
		t.Fatalf("MCPTool valid input error = %v", err)
	}
	if err := reg.Validate("pairing_allow", map[string]any{"platform": "wechat", "userId": "12345", "displayName": "Victor"}); err != nil {
		t.Fatalf("pairing_allow valid input error = %v", err)
	}
	if err := reg.Validate("pairing_allow", map[string]any{"platform": "wechat"}); err == nil {
		t.Fatal("pairing_allow without userId should fail")
	}
	if err := reg.Validate("pairing_list", map[string]any{}); err != nil {
		t.Fatalf("pairing_list empty input error = %v", err)
	}
	if err := reg.Validate("im_config", map[string]any{"platform": "wechat", "smoke": true}); err != nil {
		t.Fatalf("im_config valid input error = %v", err)
	}
	if err := reg.Validate("im_config", map[string]any{"smoke": "true"}); err == nil {
		t.Fatal("im_config with string smoke should fail")
	}
	if err := reg.Validate("pairing_revoke", map[string]any{"platform": "wechat", "userId": "12345"}); err != nil {
		t.Fatalf("pairing_revoke valid input error = %v", err)
	}
	if err := reg.Validate("runtime_set", map[string]any{"namespace": "agent", "key": "state", "value": map[string]any{"ok": true}}); err != nil {
		t.Fatalf("runtime_set valid input error = %v", err)
	}
	if err := reg.Validate("runtime_get", map[string]any{"namespace": "agent"}); err == nil {
		t.Fatal("runtime_get without key should fail")
	}
	if err := reg.Validate("runtime_list", map[string]any{}); err != nil {
		t.Fatalf("runtime_list empty input error = %v", err)
	}
	if err := reg.Validate("runtime_delete", map[string]any{"namespace": "agent", "key": "state"}); err != nil {
		t.Fatalf("runtime_delete valid input error = %v", err)
	}
	if err := reg.Validate("session_list", map[string]any{}); err != nil {
		t.Fatalf("session_list valid input error = %v", err)
	}
	if err := reg.Validate("session_get", map[string]any{}); err == nil {
		t.Fatal("session_get without sessionId should fail")
	}
	if err := reg.Validate("session_replay", map[string]any{"sessionId": "session-1", "afterEventId": float64(1), "limit": float64(20)}); err != nil {
		t.Fatalf("session_replay valid input error = %v", err)
	}
	if err := reg.Validate("session_append", map[string]any{"sessionId": "session-1", "role": "assistant", "message": map[string]any{"type": "assistant_message"}}); err != nil {
		t.Fatalf("session_append valid input error = %v", err)
	}
	if err := reg.Validate("session_append", map[string]any{"sessionId": "session-1", "role": "assistant", "message": "not-object"}); err == nil {
		t.Fatal("session_append with non-object message should fail")
	}
	if err := reg.Validate("session_claim", map[string]any{"sessionId": "session-1", "runnerId": "runner-a", "ttlSeconds": float64(60)}); err != nil {
		t.Fatalf("session_claim valid input error = %v", err)
	}
	if err := reg.Validate("session_claim", map[string]any{"sessionId": "session-1"}); err == nil {
		t.Fatal("session_claim without runnerId should fail")
	}
	if err := reg.Validate("session_heartbeat", map[string]any{"sessionId": "session-1", "runnerId": "runner-a", "runnerAttempt": float64(1), "claimToken": "claim-a"}); err != nil {
		t.Fatalf("session_heartbeat valid input error = %v", err)
	}
	if err := reg.Validate("session_release", map[string]any{"sessionId": "session-1", "runnerId": "runner-a", "runnerAttempt": float64(1), "claimToken": "claim-a"}); err != nil {
		t.Fatalf("session_release valid input error = %v", err)
	}
	if err := reg.Validate("session_bind_project", map[string]any{"sessionId": "session-1", "projectId": "alpha", "path": "projects/alpha"}); err != nil {
		t.Fatalf("session_bind_project valid input error = %v", err)
	}
	if err := reg.Validate("session_bind_project", map[string]any{"sessionId": "session-1", "path": "projects/alpha"}); err == nil {
		t.Fatal("session_bind_project without projectId should fail")
	}
	if err := reg.Validate("session_fork", map[string]any{"sessionId": "session-2", "sourceSessionId": "session-1", "title": "fork", "resetRunner": true}); err != nil {
		t.Fatalf("session_fork valid input error = %v", err)
	}
	if err := reg.Validate("session_fork", map[string]any{"sessionId": "session-2"}); err == nil {
		t.Fatal("session_fork without sourceSessionId should fail")
	}
	if err := reg.Validate("session_rewind", map[string]any{"sessionId": "session-1", "afterEventId": float64(2), "resetRunner": true}); err != nil {
		t.Fatalf("session_rewind valid input error = %v", err)
	}
	if err := reg.Validate("session_rewind", map[string]any{"sessionId": "session-1"}); err == nil {
		t.Fatal("session_rewind without afterEventId should fail")
	}
	if err := reg.Validate("session_export", map[string]any{"sessionId": "session-1", "format": "jsonl", "outputPath": "exports/session.jsonl", "maxEntries": float64(100), "includeMeta": false}); err != nil {
		t.Fatalf("session_export valid input error = %v", err)
	}
	if err := reg.Validate("session_export", map[string]any{"sessionId": "session-1", "format": "markdown"}); err != nil {
		t.Fatalf("session_export markdown input error = %v", err)
	}
	if err := reg.Validate("session_export", map[string]any{"format": "json"}); err == nil {
		t.Fatal("session_export without sessionId should fail")
	}
	if err := reg.Validate("artifact_register", map[string]any{"path": "exports/report.md", "kind": "report", "sessionId": "session-1"}); err != nil {
		t.Fatalf("artifact_register valid input error = %v", err)
	}
	if err := reg.Validate("artifact_register", map[string]any{"kind": "report"}); err == nil {
		t.Fatal("artifact_register without path should fail")
	}
	if err := reg.Validate("artifact_get", map[string]any{"artifactId": "artifact-1"}); err != nil {
		t.Fatalf("artifact_get valid input error = %v", err)
	}
	if err := reg.Validate("artifact_get", map[string]any{}); err == nil {
		t.Fatal("artifact_get without artifactId should fail")
	}
	if err := reg.Validate("StructuredOutput", map[string]any{"status": "ok", "items": []any{"a", "b"}}); err != nil {
		t.Fatalf("StructuredOutput valid input error = %v", err)
	}
	if err := reg.Validate("StructuredOutput", map[string]any{"schema": map[string]any{"type": "object"}, "value": map[string]any{"status": "ok"}}); err != nil {
		t.Fatalf("StructuredOutput schema/value input error = %v", err)
	}
	if err := reg.Validate("EnterWorktree", map[string]any{"path": "repo", "name": "feature-a", "sessionId": "session-1"}); err != nil {
		t.Fatalf("EnterWorktree valid input error = %v", err)
	}
	if err := reg.Validate("EnterWorktree", map[string]any{"name": float64(1)}); err == nil {
		t.Fatal("EnterWorktree with numeric name should fail")
	}
	if err := reg.Validate("ExitWorktree", map[string]any{"action": "keep", "sessionId": "session-1"}); err != nil {
		t.Fatalf("ExitWorktree keep input error = %v", err)
	}
	if err := reg.Validate("ExitWorktree", map[string]any{"discard_changes": true}); err == nil {
		t.Fatal("ExitWorktree without action should fail")
	}
	if err := reg.Validate("session_runner_checkpoint", map[string]any{"sessionId": "session-1", "runnerId": "runner-a", "runnerAttempt": float64(1), "claimToken": "claim-a", "status": "waiting", "message": "waiting for permission", "afterEventId": float64(1), "clientMessageId": "checkpoint-1"}); err != nil {
		t.Fatalf("session_runner_checkpoint valid input error = %v", err)
	}
	if err := reg.Validate("session_runner_checkpoint", map[string]any{"sessionId": "session-1", "status": "waiting"}); err == nil {
		t.Fatal("session_runner_checkpoint without runnerId should fail")
	}
	if err := reg.Validate("session_runner_next", map[string]any{"sessionId": "session-1", "runnerId": "runner-a", "ttlSeconds": float64(120), "afterEventId": float64(1), "limit": float64(20)}); err != nil {
		t.Fatalf("session_runner_next valid input error = %v", err)
	}
	if err := reg.Validate("session_runner_next", map[string]any{"sessionId": "session-1"}); err == nil {
		t.Fatal("session_runner_next without runnerId should fail")
	}
	if err := reg.Validate("session_runner_finish", map[string]any{"sessionId": "session-1", "runnerId": "runner-a", "runnerAttempt": float64(1), "claimToken": "claim-a", "status": "completed", "message": "done", "afterEventId": float64(2), "clientMessageId": "finish-1"}); err != nil {
		t.Fatalf("session_runner_finish valid input error = %v", err)
	}
	if err := reg.Validate("session_runner_finish", map[string]any{"sessionId": "session-1", "status": "completed"}); err == nil {
		t.Fatal("session_runner_finish without runnerId should fail")
	}
	if err := reg.Validate("session_runner_pick", map[string]any{"runnerId": "runner-a", "ttlSeconds": float64(120), "afterEventId": float64(2), "limit": float64(20)}); err != nil {
		t.Fatalf("session_runner_pick valid input error = %v", err)
	}
	if err := reg.Validate("session_runner_pick", map[string]any{"ttlSeconds": float64(120)}); err == nil {
		t.Fatal("session_runner_pick without runnerId should fail")
	}
	if err := reg.Validate("session_runner_queue", map[string]any{}); err != nil {
		t.Fatalf("session_runner_queue empty input error = %v", err)
	}
	if err := reg.Validate("session_runner_backlog", map[string]any{"limit": float64(20), "includeRunning": true, "includeTerminal": false, "projectId": "alpha", "state": "pending"}); err != nil {
		t.Fatalf("session_runner_backlog valid input error = %v", err)
	}
}

func TestSessionAppendSchemaDocumentsOptionalRunnerID(t *testing.T) {
	reg := Default()
	tool, ok := reg.Get("session_append")
	if !ok {
		t.Fatal("session_append missing from registry")
	}
	field, ok := tool.Input["runnerId"]
	if !ok {
		t.Fatalf("session_append schema should expose optional runnerId: %#v", tool.Input)
	}
	if field.Type != "string" || field.Required {
		t.Fatalf("runnerId field = %#v", field)
	}
}

func TestRegistryExecutesWebFetchAgainstRealHTTPServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/source" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		if r.URL.Path != "/final" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("registry fetch ok"))
	}))
	defer server.Close()

	reg := DefaultWithWebOptions(testWebFetchOptions(), websearch.Options{})
	result, err := reg.Execute(context.Background(), "web_fetch", map[string]any{
		"url":   server.URL + "/source",
		"limit": float64(64),
	})
	if err != nil {
		t.Fatalf("Execute(web_fetch) error = %v", err)
	}
	fetch, ok := result.(WebFetchResult)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	if fetch.StatusCode != http.StatusOK || fetch.Body != "registry fetch ok" || fetch.URL != server.URL+"/final" {
		t.Fatalf("fetch result = %#v", fetch)
	}
}

func TestRegistryExecutesCanonicalMultiVariantWebSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("q")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<a class="result__a" href="https://evidence.example/` +
			url.QueryEscape(query) + `">` + query + ` evidence</a>` +
			`<a class="result__snippet">Primary source for ` + query + `.</a>`))
	}))
	defer server.Close()
	t.Setenv("SYNON_WEBSEARCH_SCRIPT", "/missing/websearch.py")
	t.Setenv("SYNON_WEBSEARCH_ENDPOINT", server.URL+"/search?q={query}")

	reg := DefaultWithWebOptions(testWebFetchOptions(), websearch.Options{
		ClientForURL: func(context.Context, string) (*http.Client, error) {
			return server.Client(), nil
		},
	})
	raw, err := reg.Execute(context.Background(), "web_search", map[string]any{
		"query": "POLQ inhibitor", "query_variants": []any{"POLQ 抑制剂"}, "max_results": float64(10),
	})
	if err != nil {
		t.Fatalf("Execute(web_search) error = %v", err)
	}
	result, ok := raw.(websearch.Output)
	if !ok || len(result.Sources) != 2 || result.Diagnostics["queryVariantCount"] != 2 {
		t.Fatalf("multi-variant result=%#v type=%T", raw, raw)
	}
	for _, source := range result.Sources {
		matched, _ := source.Metadata["matchedQueryVariant"].(string)
		if strings.TrimSpace(matched) == "" {
			t.Fatalf("source lost query-variant provenance: %#v", source)
		}
	}
}

func TestRegistryMarksHTTPErrorUnavailableForCanonicalWebFetch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid identifier"}`))
	}))
	defer server.Close()

	reg := DefaultWithWebOptions(testWebFetchOptions(), websearch.Options{})
	canonical, err := reg.Execute(context.Background(), "web_fetch", map[string]any{"url": server.URL + "/bad"})
	if err != nil {
		t.Fatalf("Execute(web_fetch) error = %v", err)
	}
	fetch, ok := canonical.(WebFetchResult)
	if !ok || fetch.StatusCode != http.StatusBadRequest || !fetch.SourceUnavailable || fetch.Error == "" || fetch.Body == "" {
		t.Fatalf("canonical HTTP error result = %#v", canonical)
	}
	if got := agentruntime.ClassifyToolResult(canonical); got != agentruntime.ToolResultUnavailable {
		t.Fatalf("canonical outcome = %q, want unavailable", got)
	}

}

func TestRegistryExecutesWebFetchWithIntegerLimitAsRecoverablePartial(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("integer-limit"))
	}))
	defer server.Close()

	reg := DefaultWithWebOptions(testWebFetchOptions(), websearch.Options{})
	canonical, err := reg.Execute(context.Background(), "web_fetch", map[string]any{
		"url":   server.URL,
		"limit": 7,
	})
	if err != nil {
		t.Fatalf("Execute(web_fetch) partial error = %v", err)
	}
	fetch, ok := canonical.(WebFetchResult)
	if !ok || fetch.Body != "integer" || !fetch.Truncated || fetch.BytesRead != 7 ||
		agentruntime.ClassifyToolResult(canonical) != agentruntime.ToolResultPartial {
		t.Fatalf("canonical partial result = %#v", canonical)
	}
}

func TestRegistryRejectsUnknownExecution(t *testing.T) {
	reg := Default()
	if _, err := reg.Execute(context.Background(), "unknown", map[string]any{}); err == nil {
		t.Fatal("unknown tool should fail")
	}
	forbiddenLegacyText := "not " + "implemented"
	if _, err := reg.Execute(context.Background(), "synon_link", map[string]any{"userId": "u", "clientId": "sl_1", "action": "search_web"}); err == nil || strings.Contains(err.Error(), forbiddenLegacyText) || !strings.Contains(err.Error(), "server dispatcher") {
		t.Fatalf("synon_link execution should require the server dispatcher path, got err=%v", err)
	}
}

func TestBindingModePDBIDPrefersValidCanonicalValueAndIgnoresPlaceholders(t *testing.T) {
	tests := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{
			name: "canonical value survives invalid aliases",
			input: map[string]any{
				"pdbId":        "9ODR",
				"pdb_id":       "None",
				"structure_id": "null",
			},
			want: "9ODR",
		},
		{
			name:  "snake case alias is normalized",
			input: map[string]any{"pdb_id": " 7bqv "},
			want:  "7BQV",
		},
		{
			name:  "placeholder aliases are rejected",
			input: map[string]any{"pdbId": "None", "pdb_id": "null", "structure_id": ""},
			want:  "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := bindingModePDBID(test.input); got != test.want {
				t.Fatalf("bindingModePDBID() = %q, want %q", got, test.want)
			}
		})
	}
}

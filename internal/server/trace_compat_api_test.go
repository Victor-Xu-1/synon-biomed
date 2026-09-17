package server

import (
	"net/http"
	"path/filepath"
	"strconv"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestTraceShallowFullDeltaAndBackendBenchCompatibility(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, project := range []workspace.CreateProjectInput{
		{ID: "project-1", UserID: "user-1", Name: "One"},
		{ID: "project-2", UserID: "user-2", Name: "Two"},
	} {
		if _, err := store.CreateProject(project); err != nil {
			t.Fatal(err)
		}
	}
	for _, frame := range []workspace.CreateFrameInput{
		{ID: "root", ProjectID: "project-1", AgentName: "synon", Status: "running", ConversationType: "task", Name: "Root"},
		{ID: "child", ProjectID: "project-1", ParentFrameID: "root", AgentName: "worker", Status: "running", ConversationType: "task", Name: "Child"},
		{ID: "grandchild", ProjectID: "project-1", ParentFrameID: "child", AgentName: "worker", Status: "queued", ConversationType: "task", Name: "Grandchild"},
		{ID: "foreign", ProjectID: "project-2", AgentName: "synon", Status: "running", ConversationType: "task"},
	} {
		if _, err := store.CreateFrame(frame); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.AppendFrameEvent(workspace.FrameEventInput{
		FrameID: "child", Type: "assistant_message", Payload: map[string]any{"role": "assistant", "content": "first"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetFrameSubmissionMetadata("child", map[string]any{"request": "private child input"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata("child", workspace.FrameRuntimeMetadata{ContextData: map[string]any{
		"_message_count": 1,
		"_context_used":  84000,
		"private":        "child context",
		"_branch_meta": map[string]any{
			"branches": map[string]any{
				"branch-a": map[string]any{"messages": []any{"private branch"}},
			},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store}).Handler()

	full := runtimeCompatJSON(t, app, http.MethodGet, "/api/frames/root/trace-shallow", "user-1", nil, http.StatusOK)
	if full["id"] != "root" {
		t.Fatalf("full trace=%#v", full)
	}
	if _, found := full["_trace_cursor"]; found {
		t.Fatalf("full trace unexpectedly exposed lite cursor: %#v", full)
	}
	for _, field := range []string{
		"id", "root_frame_id", "parent_frame_id", "agent_name", "delegate_name", "status",
		"input_data", "output_data", "context_data", "created_at", "completed_at", "updated_at",
		"model", "effort", "input_tokens", "output_tokens", "cache_read_tokens",
		"cache_write_tokens", "total_cost", "aux_cost", "context_limit", "context_used",
		"context_usage_percent", "compaction_count", "children", "project_id", "name",
		"conversation_type", "message_count", "task_summary", "status_description",
		"mentioned_files", "specialists_used", "is_hidden", "activity_counts",
	} {
		if _, found := full[field]; !found {
			t.Fatalf("full trace missing field %q: %#v", field, full)
		}
	}
	for _, forbidden := range []string{"rootFrameId", "parentFrameId", "projectId", "messages"} {
		if _, found := full[forbidden]; found {
			t.Fatalf("full trace exposed non-v1 field %q: %#v", forbidden, full)
		}
	}
	children := full["children"].([]any)
	if len(children) != 1 || children[0].(map[string]any)["id"] != "child" {
		t.Fatalf("full trace children=%#v", children)
	}
	child := children[0].(map[string]any)
	leanContext, _ := child["context_data"].(map[string]any)
	if len(child["children"].([]any)) != 1 || child["input_data"] != nil || leanContext["private"] != nil ||
		child["message_count"] != float64(1) {
		t.Fatalf("child trace=%#v", child)
	}
	focused := runtimeCompatJSON(t, app, http.MethodGet, "/api/frames/root/trace-shallow?focus_frame_id=child", "user-1", nil, http.StatusOK)
	focusedChild := focused["children"].([]any)[0].(map[string]any)
	focusedContext := focusedChild["context_data"].(map[string]any)
	if focusedChild["input_data"].(map[string]any)["request"] != "private child input" ||
		focusedContext["private"] != "child context" || len(focusedContext["_messages"].([]any)) != 1 ||
		focusedChild["context_used"] != float64(84000) ||
		focusedChild["context_usage_percent"] != float64(8.4) {
		t.Fatalf("focused child trace=%#v", focusedChild)
	}
	withoutMessages := runtimeCompatJSON(t, app, http.MethodGet,
		"/api/frames/root/trace-shallow?focus_frame_id=child&include_messages=false", "user-1", nil, http.StatusOK)
	withoutChild := withoutMessages["children"].([]any)[0].(map[string]any)
	withoutContext := withoutChild["context_data"].(map[string]any)
	if withoutMessages["_trace_cursor"] == nil {
		t.Fatalf("include_messages=false missing trace cursor: %#v", withoutMessages)
	}
	if _, found := withoutContext["_messages"]; found {
		t.Fatalf("include_messages=false retained messages: %#v", withoutChild)
	}
	branchMeta := withoutContext["_branch_meta"].(map[string]any)
	branch := branchMeta["branches"].(map[string]any)["branch-a"].(map[string]any)
	if branch["messages"] != nil {
		t.Fatalf("include_messages=false retained branch messages: %#v", withoutChild)
	}
	childRoot := runtimeCompatJSON(t, app, http.MethodGet, "/api/frames/child/trace-shallow", "user-1", nil, http.StatusOK)
	if childRoot["id"] != "child" || childRoot["root_frame_id"] != "root" || len(childRoot["children"].([]any)) != 1 {
		t.Fatalf("child-rooted trace=%#v", childRoot)
	}

	cursor := int64(withoutMessages["_trace_cursor"].(float64))
	if _, err := store.UpdateFrame("foreign", workspace.UpdateFrameInput{Status: stringPointer("completed")}); err != nil {
		t.Fatal(err)
	}
	unrelated := runtimeCompatJSON(t, app, http.MethodGet,
		"/api/frames/root/trace-shallow?cursor="+strconv.FormatInt(cursor, 10), "user-1", nil, http.StatusOK)
	if int64(unrelated["cursor"].(float64)) != cursor ||
		int64(unrelated["server_max"].(float64)) != cursor ||
		len(unrelated["changed"].([]any)) != 0 {
		t.Fatalf("unrelated trace update advanced cursor=%#v", unrelated)
	}
	if _, err := store.UpdateFrame("child", workspace.UpdateFrameInput{Status: stringPointer("completed")}); err != nil {
		t.Fatal(err)
	}
	delta := runtimeCompatJSON(t, app, http.MethodGet, "/api/frames/root/trace-shallow?cursor="+strconv.FormatInt(cursor, 10), "user-1", nil, http.StatusOK)
	changed := delta["changed"].([]any)
	if len(delta) != 5 || delta["delta"] != true || int64(delta["cursor"].(float64)) <= cursor ||
		delta["server_max"] != delta["cursor"] || delta["frame_count"] != float64(3) ||
		len(changed) != 1 || changed[0].(map[string]any)["id"] != "child" ||
		changed[0].(map[string]any)["input_data"] != nil {
		t.Fatalf("trace delta=%#v", delta)
	}
	serverMax := int64(delta["server_max"].(float64))
	futureCursor := serverMax + 100
	future := runtimeCompatJSON(t, app, http.MethodGet,
		"/api/frames/root/trace-shallow?cursor="+strconv.FormatInt(futureCursor, 10), "user-1", nil, http.StatusOK)
	if int64(future["cursor"].(float64)) != futureCursor ||
		int64(future["server_max"].(float64)) != serverMax || len(future["changed"].([]any)) != 0 {
		t.Fatalf("future trace cursor=%#v", future)
	}
	runtimeCompatJSON(t, app, http.MethodGet,
		"/api/frames/root/trace-shallow?cursor="+strconv.FormatInt(cursor, 10)+"&focus_frame_id=child",
		"user-1", nil, http.StatusBadRequest)
	runtimeCompatJSON(t, app, http.MethodGet,
		"/api/frames/child/trace-shallow?cursor="+strconv.FormatInt(cursor, 10),
		"user-1", nil, http.StatusBadRequest)
	runtimeCompatJSON(t, app, http.MethodGet, "/api/frames/root/trace-shallow", "user-2", nil, http.StatusNotFound)

	bench := runtimeCompatJSON(t, app, http.MethodGet, "/api/go/benches/root", "user-1", nil, http.StatusOK)
	if bench["bench"].(map[string]any)["id"] != "root" {
		t.Fatalf("bench=%#v", bench)
	}
	runtimeCompatJSON(t, app, http.MethodGet, "/api/go/benches/root", "user-2", nil, http.StatusNotFound)
}

func TestTraceContextUsageFallsBackToMessageTokenMetadata(t *testing.T) {
	used, found := traceContextUsage(map[string]any{"_messages": []any{
		map[string]any{
			"role":    "assistant",
			"_tokens": map[string]any{"input": float64(100)},
		},
		map[string]any{"role": "user", "content": "12345678"},
	}})
	if !found || used != 102 {
		t.Fatalf("context usage found=%v used=%v", found, used)
	}
}

func stringPointer(value string) *string { return &value }

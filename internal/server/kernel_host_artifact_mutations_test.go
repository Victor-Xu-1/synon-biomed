package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runtimekv "synon-go/internal/persistence/runtimekv"
	workspace "synon-go/internal/persistence/workspace"
)

func TestKernelHostArtifactsRenameAndApprovedBatchDelete(t *testing.T) {
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(t.TempDir(), "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	write := func(id, name, content string) {
		t.Helper()
		_, _, err := store.WriteArtifactVersionRealtime(
			workspace.WithMutationIdempotencyKey(context.Background(), "kernel-artifact-"+id+"-"+content),
			workspace.WriteArtifactVersionInput{
				ArtifactID: id, ProjectID: identity.access.Frame.ProjectID, Name: name,
				ContentType: "text/plain", Content: strings.NewReader(content), MaxBytes: 1 << 20,
				RootFrameID: identity.access.Frame.RootFrameID, FrameID: identity.access.Frame.ID,
			},
			identity.access.UserID,
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	write("artifact-one", "one.txt", "one")
	write("artifact-two", "two.txt", "two")

	renamed := runKernelHostCellNamed(t, app, identity, "Operon", `
import host, json
print(json.dumps(host.artifacts.rename("artifact-one", " renamed.txt "), sort_keys=True))
`)
	if renamed["ok"] != true {
		t.Fatalf("rename result=%#v", renamed)
	}
	renamePayload := decodeLastKernelJSONLine(t, renamed["stdout"].(string))
	if renamePayload["artifact_id"] != "artifact-one" || renamePayload["old_filename"] != "one.txt" || renamePayload["new_filename"] != "renamed.txt" {
		t.Fatalf("rename payload=%#v", renamePayload)
	}

	type execution struct {
		result map[string]any
		err    error
	}
	returned := make(chan execution, 1)
	go func() {
		result, err := app.executeAgentKernelTool(context.Background(), identity, "repl", map[string]any{
			"code": `import host, json
print(json.dumps(host.artifacts.delete(["artifact-one", "artifact-two", "artifact-one"], reason="superseded"), sort_keys=True))
`,
		}, []string{"python"})
		returned <- execution{result: result, err: err}
	}()
	approvalID := waitForKernelArtifactDeleteApprovalTest(t, app)
	entry, found, err := app.runtimeStore.Get(agentRuntimeApprovalNamespace, approvalID)
	if err != nil || !found {
		t.Fatalf("approval found=%t err=%v", found, err)
	}
	value := mapValue(entry.Value)
	items, _ := value["items"].([]any)
	if value["approvalType"] != "artifact_delete" || value["rememberable"] != false || len(items) != 2 || value["reason"] != "superseded" {
		t.Fatalf("approval payload=%#v", value)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata(identity.access.Frame.ID)
	if err != nil || !found || len(compatibilityServerPendingInputs(metadata.ContextData)) != 1 {
		t.Fatalf("visible pending approval metadata=%#v found=%t err=%v", metadata.ContextData, found, err)
	}
	confirmationID := "web-confirmation-add:" + identity.access.Frame.ID + ":" + webEventComponent(approvalID)
	confirmation := waitForKernelArtifactRealtimeEventTest(t, store, confirmationID, "confirmation.add")
	options, _ := confirmation.Payload["options"].([]any)
	confirmationItems, _ := confirmation.Payload["items"].([]any)
	if len(options) != 2 || len(confirmationItems) != 2 || confirmation.Payload["total_bytes"] == nil {
		t.Fatalf("visible confirmation=%#v", confirmation)
	}
	current, found, err := store.GetCompatibilityFrame(identity.access.Frame.ID)
	if err != nil || !found {
		t.Fatalf("compatibility frame found=%t err=%v", found, err)
	}
	allow := true
	if forbidden, err := app.resolveCompatibilityInput(
		httptest.NewRequest("POST", "/api/frames/"+current.ID+"/resolve-input", nil),
		current,
		compatibilityResolveInputRequest{Responses: []compatibilityInputResponse{{RequestID: approvalID, Approved: &allow, Action: "allow_always", Scope: "always"}}},
	); err == nil || forbidden.Frame.ID != "" || !strings.Contains(err.Error(), "invocation only") {
		t.Fatalf("persistent approval forbidden=%#v err=%v", forbidden, err)
	}
	entry, found, err = app.runtimeStore.Get(agentRuntimeApprovalNamespace, approvalID)
	if err != nil || !found || mapValue(entry.Value)["status"] != "pending" {
		t.Fatalf("forbidden persistent approval changed authority=%#v found=%t err=%v", entry.Value, found, err)
	}
	resolved, err := app.resolveCompatibilityInput(
		httptest.NewRequest("POST", "/api/frames/"+current.ID+"/resolve-input", nil),
		current,
		compatibilityResolveInputRequest{Responses: []compatibilityInputResponse{{RequestID: approvalID, Approved: &allow, Action: "allow_once"}}},
	)
	if err != nil || resolved.Frame.ID != current.ID {
		t.Fatalf("approval resolve=%#v err=%v", resolved, err)
	}
	entry, found, err = app.runtimeStore.Get(agentRuntimeApprovalNamespace, approvalID)
	if err != nil || !found || mapValue(entry.Value)["status"] != "approved" {
		t.Fatalf("approved authority=%#v found=%t err=%v", entry.Value, found, err)
	}
	metadata, found, err = store.GetFrameRuntimeMetadata(identity.access.Frame.ID)
	if err != nil || !found || len(compatibilityServerPendingInputs(metadata.ContextData)) != 0 {
		t.Fatalf("resolved pending approval metadata=%#v found=%t err=%v", metadata.ContextData, found, err)
	}
	removeID := "web-confirmation-remove:" + identity.access.Frame.ID + ":" + webEventComponent(approvalID) + ":" + webEventComponent(current.Status)
	waitForKernelArtifactRealtimeEventTest(t, store, removeID, "confirmation.remove")
	select {
	case completed := <-returned:
		if completed.err != nil || completed.result["ok"] != true {
			t.Fatalf("delete execution=%#v err=%v", completed.result, completed.err)
		}
		payload := decodeLastKernelJSONLine(t, completed.result["stdout"].(string))
		if payload["requested"] != float64(2) || payload["deleted"] != float64(2) || len(payload["results"].([]any)) != 2 {
			t.Fatalf("delete payload=%#v", payload)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("approved artifact delete did not resume")
	}
	for _, id := range []string{"artifact-one", "artifact-two"} {
		if _, found, err := store.GetArtifact(id); err != nil || found {
			t.Fatalf("artifact %s found=%t err=%v", id, found, err)
		}
	}

	write("artifact-three", "three.txt", "three")
	returned = make(chan execution, 1)
	go func() {
		result, err := app.executeAgentKernelTool(context.Background(), identity, "repl", map[string]any{
			"code": `import host
host.artifacts.delete("artifact-three")
`,
		}, []string{"python"})
		returned <- execution{result: result, err: err}
	}()
	deniedID := waitForKernelArtifactDeleteApprovalTest(t, app)
	if spoofed, err := app.resolveAgentRuntimeApprovalMessage(context.Background(), "", map[string]any{
		"approvalId": deniedID, "approve": true,
	}); err == nil || spoofed != nil || !strings.Contains(err.Error(), "authenticated user") {
		t.Fatalf("agent-side approval spoof result=%#v err=%v", spoofed, err)
	}
	deny := false
	denied, err := app.resolveCompatibilityInput(
		httptest.NewRequest("POST", "/api/frames/"+current.ID+"/resolve-input", nil),
		current,
		compatibilityResolveInputRequest{Responses: []compatibilityInputResponse{{RequestID: deniedID, Approved: &deny, Action: "deny"}}},
	)
	if err != nil || denied.Frame.ID != current.ID {
		t.Fatalf("denied approval=%#v err=%v", denied, err)
	}
	deniedEntry, found, err := app.runtimeStore.Get(agentRuntimeApprovalNamespace, deniedID)
	if err != nil || !found || mapValue(deniedEntry.Value)["status"] != "denied" {
		t.Fatalf("denied authority=%#v found=%t err=%v", deniedEntry.Value, found, err)
	}
	select {
	case completed := <-returned:
		if completed.err != nil || completed.result["ok"] != false || !strings.Contains(stringValue(completed.result["stderr"]), "do not retry or work around it") {
			t.Fatalf("denied delete execution=%#v err=%v", completed.result, completed.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("denied artifact delete did not resume")
	}
	if _, found, err := store.GetArtifact("artifact-three"); err != nil || !found {
		t.Fatalf("denied artifact found=%t err=%v", found, err)
	}
}

func TestKernelHostArtifactDeleteFullAccessSkipsFrontendApproval(t *testing.T) {
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(t.TempDir(), "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	_, _, err := store.WriteArtifactVersionRealtime(
		workspace.WithMutationIdempotencyKey(context.Background(), "full-access-artifact"),
		workspace.WriteArtifactVersionInput{
			ArtifactID: "full-access-artifact", ProjectID: identity.access.Frame.ProjectID, Name: "remove.txt",
			ContentType: "text/plain", Content: strings.NewReader("remove"), MaxBytes: 1 << 20,
			RootFrameID: identity.access.Frame.RootFrameID, FrameID: identity.access.Frame.ID,
		},
		identity.access.UserID,
	)
	if err != nil {
		t.Fatal(err)
	}
	seedPermissionTestAgent(t, store, identity.access.UserID)
	if _, err := store.SetFrameRuntimeMetadata(identity.access.Frame.ID, workspace.FrameRuntimeMetadata{
		FrameID: identity.access.Frame.ID,
		ContextData: map[string]any{"web_assistant": map[string]any{
			"id": "synonbiomed:operon", "conversation_overrides": map[string]any{"permission": "allow"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	result := runKernelHostCellNamed(t, app, identity, "Operon", `
import host, json
print(json.dumps(host.artifacts.delete("full-access-artifact"), sort_keys=True))
`)
	if result["ok"] != true {
		t.Fatalf("full access delete result=%#v", result)
	}
	if _, found, err := store.GetArtifact("full-access-artifact"); err != nil || found {
		t.Fatalf("deleted artifact found=%t err=%v", found, err)
	}
	entries, err := app.runtimeStore.List(agentRuntimeApprovalNamespace)
	if err != nil || len(entries) != 0 {
		t.Fatalf("full access persisted frontend approvals=%#v err=%v", entries, err)
	}
}

func TestKernelHostArtifactDeleteInputAndPreflightFailBeforeApproval(t *testing.T) {
	pendingDelete := map[string]any{"kind": "artifact_delete", "requestId": "approval"}
	if _, _, err := webConfirmationInputResponse("approval", pendingDelete, "proceed_always", false); err == nil || !strings.Contains(err.Error(), "cannot be remembered") {
		t.Fatalf("persistent artifact approval error=%v", err)
	}
	if response, remembered, err := webConfirmationInputResponse("approval", pendingDelete, "proceed_once", false); err != nil || remembered || response.Approved == nil || !*response.Approved || response.Scope != "once" {
		t.Fatalf("one-shot artifact approval response=%#v remembered=%t err=%v", response, remembered, err)
	}
	if ids, reason, err := parseKernelArtifactDeleteWire([]any{[]any{"a", "a", "b"}, " why "}); err != nil || len(ids) != 2 || ids[0] != "a" || ids[1] != "b" || reason != "why" {
		t.Fatalf("parse ids=%#v reason=%q err=%v", ids, reason, err)
	}
	tooMany := make([]any, workspace.MaxKernelArtifactDeleteBatch+1)
	for index := range tooMany {
		tooMany[index] = "artifact-" + strings.Repeat("x", index+1)
	}
	if _, _, err := parseKernelArtifactDeleteWire([]any{tooMany}); err == nil {
		t.Fatal("201 unique artifact ids were accepted")
	}

	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(t.TempDir(), "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	result := runKernelHostCellNamed(t, app, identity, "Operon", `
import host
host.artifacts.delete(["missing"])
`)
	if result["ok"] != false || !strings.Contains(stringValue(result["stderr"]), "not found") {
		t.Fatalf("missing delete result=%#v", result)
	}
	entries, err := app.runtimeStore.List(agentRuntimeApprovalNamespace)
	if err != nil || len(entries) != 0 {
		t.Fatalf("preflight queued approval entries=%#v err=%v", entries, err)
	}
	if _, err := app.runtimeStore.Set(agentRuntimeApprovalNamespace, "secret-approval", map[string]any{"status": "pending"}); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"runtime_get", "runtime_set", "runtime_delete", "runtime_list"} {
		input := map[string]any{"namespace": agentRuntimeApprovalNamespace, "key": "secret-approval", "value": map[string]any{"status": "approved"}}
		if _, err := app.executeRuntimeTool(tool, input); err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("%s accessed reserved approval namespace: %v", tool, err)
		}
	}
	listed, err := app.executeRuntimeTool("runtime_list", map[string]any{})
	if err != nil || len(mapValue(listed)["entries"].([]runtimekv.Entry)) != 0 {
		t.Fatalf("unscoped runtime list leaked approval entries=%#v err=%v", listed, err)
	}
}

func waitForKernelArtifactDeleteApprovalTest(t *testing.T, app *Server) string {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := app.runtimeStore.List(agentRuntimeApprovalNamespace)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			value := mapValue(entry.Value)
			if value["status"] == "pending" && value["approvalSource"] == kernelArtifactDeleteApprovalSource {
				return entry.Key
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("artifact delete approval was not queued")
	return ""
}

func waitForKernelArtifactRealtimeEventTest(
	t *testing.T,
	store *workspace.Store,
	id string,
	eventType string,
) workspace.RealtimeEvent {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		event, found, err := store.GetRealtimeEventByID(id)
		if err != nil {
			t.Fatal(err)
		}
		if found {
			if event.Type != eventType {
				t.Fatalf("realtime event %s type=%q want=%q", id, event.Type, eventType)
			}
			return event
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("realtime event %s (%s) was not persisted", id, eventType)
	return workspace.RealtimeEvent{}
}

func decodeKernelArtifactMutationJSON(t *testing.T, raw string) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

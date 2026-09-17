package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestResolveCompatibilityAgentToolDenialCanonicalizesEmptyScope(t *testing.T) {
	srv := New(Options{FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Close(ctx)
	})
	const approvalID = "approval-deny-no-scope"
	if _, err := srv.runtimeStore.Set(agentRuntimeApprovalNamespace, approvalID, map[string]any{
		"status": "pending", "tool": "edit_file", "toolCallId": "edit-call",
		"input": map[string]any{"file_path": "probe.txt", "content": "not written"},
	}); err != nil {
		t.Fatal(err)
	}

	content, isError, err := srv.resolveCompatibilityAgentToolApproval(
		context.Background(),
		map[string]any{"approval_id": approvalID, "kind": agentToolApprovalKind},
		compatibilityInputResponse{Action: "deny", Message: "Do not change this file"},
	)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if json.Unmarshal([]byte(content), &result) != nil || result["status"] != "denied" || !isError {
		t.Fatalf("denial result content=%q isError=%t", content, isError)
	}
	entry, found, err := srv.runtimeStore.Get(agentRuntimeApprovalNamespace, approvalID)
	if err != nil || !found || mapValue(entry.Value)["status"] != "denied" || mapValue(entry.Value)["reason"] != "Do not change this file" {
		t.Fatalf("stored denial=%#v found=%t err=%v", entry.Value, found, err)
	}
}

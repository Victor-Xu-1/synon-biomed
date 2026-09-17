package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestPermissionCompatibilityAPIProjectsAndRevokesEffectiveStores(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fileRoot := t.TempDir()
	hostPath := filepath.Join(t.TempDir(), "host")
	if err := os.Mkdir(hostPath, 0o700); err != nil {
		t.Fatalf("create host path: %v", err)
	}
	srv := New(Options{Workspace: store, FileRoot: fileRoot})
	if _, err := srv.upsertHostGrant("local", hostPath, "read_write"); err != nil {
		t.Fatalf("create host grant: %v", err)
	}
	if _, err := srv.settingsStore.Set(allowedDomainsSettingKey, []string{"example.org"}); err != nil {
		t.Fatalf("create network grant: %v", err)
	}
	enabled := true
	if _, err := store.CreateMCPServer(workspace.MCPServerInput{
		ID: "custom-mcp", UserID: "local", Name: "Custom MCP", URL: "http://127.0.0.1:9999/mcp", Transport: "streamable-http", Enabled: &enabled,
	}); err != nil {
		t.Fatalf("create MCP server: %v", err)
	}
	if _, err := store.SetMCPToolGrant(workspace.MCPToolGrantInput{
		ID: "grant-1", MCPServerID: "custom-mcp", UserID: "local", AgentName: workspace.GlobalMCPToolGrantAgent, ToolName: "search", Enabled: true,
	}); err != nil {
		t.Fatalf("create MCP grant: %v", err)
	}
	denied := false
	if _, err := store.SetMCPConnectorToolPolicy("local", "bundled:pubmed", "fetch", &denied); err != nil {
		t.Fatalf("create connector policy: %v", err)
	}
	if _, err := srv.rememberAgentRuntimeApproval("shell_exec", map[string]any{"command": "pwd"}, "approved for test", time.Now()); err != nil {
		t.Fatalf("remember approval: %v", err)
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-local-exec", UserID: "local", Name: "Local Exec"}); err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-local-exec-grant", ProjectID: "project-local-exec", AgentName: "OPERON",
		Status: "processing", ConversationType: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:" + frame.ID, OwnerID: "local", ExternalID: frame.ID, SessionID: frame.ID,
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: frame.ProjectID,
		RootFrameID: frame.RootFrameID, FrameID: frame.ID, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "permission-task",
		FrameEventID: "permission-task-event", MessageUUID: "permission-task-message",
		Text: "Run Python.", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append task created=%t err=%v", created, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "permission-runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	claimDigest := sha256.Sum256([]byte(claimed.Claim.ClaimToken))
	inputDigest := sha256.Sum256([]byte("permission input"))
	request := workspace.KernelLocalExecApprovalRequestInput{
		OwnerUserID: "local", ProjectID: frame.ProjectID, FrameID: frame.ID,
		FrameIncarnationID: frame.IncarnationID, RootFrameID: frame.RootFrameID,
		RootFrameIncarnationID: frame.IncarnationID, RequestID: "permission-local-exec", Tool: "python",
		ToolCallID: "permission-call", Environment: "science", InputSHA256: hex.EncodeToString(inputDigest[:]),
		StreamUID: stream.UID, RunnerID: claimed.Claim.RunnerID, RunnerAttempt: claimed.Claim.Attempt,
		ClaimTokenSHA256: hex.EncodeToString(claimDigest[:]), KernelID: "permission-kernel",
		ExpectedGeneration: 1, Code: "print(1)", WorkingDir: fileRoot,
	}
	if _, err := store.AddKernelLocalExecApprovalRequest(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.ResolveKernelLocalExecApproval(context.Background(), workspace.KernelLocalExecApprovalResolutionInput{
		OwnerUserID: request.OwnerUserID, ProjectID: request.ProjectID, FrameID: request.FrameID,
		FrameIncarnationID: request.FrameIncarnationID, RootFrameID: request.RootFrameID,
		RootIncarnationID: request.RootFrameIncarnationID, RequestID: request.RequestID,
		Tool: request.Tool, Environment: request.Environment, InputSHA256: request.InputSHA256,
		StreamUID: request.StreamUID, RunnerID: request.RunnerID, RunnerAttempt: request.RunnerAttempt,
		ClaimTokenSHA256: request.ClaimTokenSHA256, KernelID: request.KernelID,
		ExpectedGeneration: request.ExpectedGeneration, Approved: true, Scope: "project",
	}); err != nil {
		t.Fatal(err)
	}
	app := srv.Handler()

	payload := memoryCompatJSON(t, app, http.MethodGet, "/api/approvals/grants", nil, "local", http.StatusOK)
	rows, _ := payload["grants"].([]any)
	if len(rows) != 6 {
		t.Fatalf("projected grants = %#v", payload)
	}
	for _, raw := range rows {
		grant := raw.(map[string]any)
		memoryCompatJSON(t, app, http.MethodDelete, "/api/approvals/grants", grant, "other-user", http.StatusOK)
	}
	localAfterForeignAttempts := memoryCompatJSON(t, app, http.MethodGet, "/api/approvals/grants", nil, "local", http.StatusOK)
	if localRows, _ := localAfterForeignAttempts["grants"].([]any); len(localRows) != 6 {
		t.Fatalf("foreign revocation changed local grants = %#v", localAfterForeignAttempts)
	}
	otherPayload := memoryCompatJSON(t, app, http.MethodGet, "/api/approvals/grants", nil, "other-user", http.StatusOK)
	if otherRows, _ := otherPayload["grants"].([]any); len(otherRows) != 0 {
		t.Fatalf("cross-owner grants = %#v", otherPayload)
	}

	for _, raw := range rows {
		grant := raw.(map[string]any)
		memoryCompatJSON(t, app, http.MethodDelete, "/api/approvals/grants", grant, "local", http.StatusOK)
	}
	final := memoryCompatJSON(t, app, http.MethodGet, "/api/approvals/grants", nil, "local", http.StatusOK)
	if finalRows, _ := final["grants"].([]any); len(finalRows) != 0 {
		t.Fatalf("grants after revocation = %#v", final)
	}
	if hostGrants, err := srv.loadHostGrants("local"); err != nil || len(hostGrants) != 0 {
		t.Fatalf("host grants after revoke = %#v, %v", hostGrants, err)
	}
	if domains, err := srv.loadAllowedDomains(); err != nil || len(domains) != 0 {
		t.Fatalf("domains after revoke = %#v, %v", domains, err)
	}
	if grants, err := store.ListGlobalMCPToolGrants("custom-mcp", "local"); err != nil || len(grants) != 0 {
		t.Fatalf("MCP grants after revoke = %#v, %v", grants, err)
	}
	if policies, err := store.ListAllMCPConnectorToolPolicies("local"); err != nil || len(policies) != 0 {
		t.Fatalf("connector policies after revoke = %#v, %v", policies, err)
	}
	if decisions, err := srv.loadRememberedApprovalDecisions(); err != nil || len(decisions) != 0 {
		t.Fatalf("remembered approvals after revoke = %#v, %v", decisions, err)
	}
	if grants, err := store.ListApprovalPolicyGrants(context.Background(), "local"); err != nil || len(grants) != 0 {
		t.Fatalf("approval policy grants after revoke = %#v, %v", grants, err)
	}
}

func TestPermissionCompatibilityBulkRevokeHonorsKindAndTier(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv := New(Options{Workspace: store, FileRoot: t.TempDir()})
	if _, err := srv.settingsStore.Set(allowedDomainsSettingKey, []string{"example.org", "example.net"}); err != nil {
		t.Fatal(err)
	}
	app := srv.Handler()
	memoryCompatJSON(t, app, http.MethodDelete, "/api/approvals/grants/all", map[string]any{"kind": "network", "tier": "deny"}, "local", http.StatusOK)
	if domains, _ := srv.loadAllowedDomains(); len(domains) != 2 {
		t.Fatalf("allow grants removed by deny filter: %#v", domains)
	}
	memoryCompatJSON(t, app, http.MethodDelete, "/api/approvals/grants/all", map[string]any{"kind": "network", "tier": "allow"}, "local", http.StatusOK)
	if domains, _ := srv.loadAllowedDomains(); len(domains) != 0 {
		t.Fatalf("network grants after bulk revoke: %#v", domains)
	}
	if _, err := store.MemoryEnabledWithDefault(context.Background(), "local", false); err != nil {
		t.Fatalf("unrelated workspace state failed after bulk revoke: %v", err)
	}
}

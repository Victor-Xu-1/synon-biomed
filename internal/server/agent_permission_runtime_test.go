package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func seedPermissionTestAgent(t *testing.T, store *workspace.Store, userID string) {
	t.Helper()
	enabled := true
	if _, err := store.CreateAgent(workspace.CreateAgentInput{
		ID: "permission-test-operon", UserID: userID, Name: "OPERON",
		DisplayName: "OPERON", Description: "Permission test agent", SystemPrompt: "Test permissions.",
		Unrestricted: true, Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestConversationPermissionTiersDriveOneAgentToolApprovalPath(t *testing.T) {
	tests := []struct {
		name       string
		mode       string
		tool       string
		input      map[string]any
		wantPrompt bool
	}{
		{name: "request approval prompts for routine edit", mode: "default", tool: "edit_file", input: map[string]any{"file_path": "report.md"}, wantPrompt: true},
		{name: "smart auto approves routine edit", mode: "smart", tool: "edit_file", input: map[string]any{"file_path": "report.md"}},
		{name: "smart prompts for authority expansion", mode: "smart", tool: requestNetworkAccessToolName, input: map[string]any{"domain": "files.rcsb.org"}, wantPrompt: true},
		{name: "full access auto approves authority expansion", mode: "allow", tool: requestNetworkAccessToolName, input: map[string]any{"domain": "files.rcsb.org"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			store, _, _ := newTranscriptWebFixture(t)
			frameID := "frame-permission-tier"
			seedTranscriptWebFrame(t, store, "local", "project-permission-tier", frameID)
			seedPermissionTestAgent(t, store, "local")
			if _, err := store.SetFrameRuntimeMetadata(frameID, workspace.FrameRuntimeMetadata{
				FrameID: frameID,
				ContextData: map[string]any{
					"web_assistant": map[string]any{
						"id":                     "synonbiomed:operon",
						"conversation_overrides": map[string]any{"permission": test.mode},
					},
				},
			}); err != nil {
				t.Fatal(err)
			}
			server := New(Options{Workspace: store, FileRoot: root})
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = server.Close(ctx)
			})
			permission := server.agentRuntimePermissionResultForSessionWithContext(
				context.Background(), frameID, test.tool,
				agentruntime.ToolCall{ID: "tier-call", Name: test.tool}, test.input,
			)
			if test.wantPrompt {
				if permission == nil || permission["decision"] != "pending_approval" {
					t.Fatalf("permission=%#v want pending approval", permission)
				}
				return
			}
			if permission != nil {
				t.Fatalf("permission=%#v want backend auto approval", permission)
			}
			entries, err := server.runtimeStore.List(agentRuntimeApprovalNamespace)
			if err != nil || len(entries) != 0 {
				t.Fatalf("auto-approved mode persisted frontend approvals=%#v err=%v", entries, err)
			}
		})
	}
}

func TestNonWebRuntimeSessionKeepsLegacyApprovalDefaults(t *testing.T) {
	store, _, _ := newTranscriptWebFixture(t)
	server := New(Options{Workspace: store, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, err := server.settingsStore.Set(approvalDefaultsSettingKey, map[string]any{"mode": "allow"}); err != nil {
		t.Fatal(err)
	}
	permission := server.agentRuntimePermissionResultForSessionWithContext(
		context.Background(), "im:feishu:permission-regression", "task_list",
		agentruntime.ToolCall{ID: "non-web-call", Name: "task_list"}, map[string]any{},
	)
	if permission != nil {
		t.Fatalf("non-Web runtime session was incorrectly routed through composer permissions: %#v", permission)
	}
}

func TestConversationDenyOverridesRememberedAgentApproval(t *testing.T) {
	store, _, _ := newTranscriptWebFixture(t)
	frameID := "frame-permission-deny"
	seedTranscriptWebFrame(t, store, "local", "project-permission-deny", frameID)
	seedPermissionTestAgent(t, store, "local")
	if _, err := store.SetFrameRuntimeMetadata(frameID, workspace.FrameRuntimeMetadata{
		FrameID: frameID,
		ContextData: map[string]any{"web_assistant": map[string]any{
			"id": "synonbiomed:operon", "conversation_overrides": map[string]any{"permission": "deny"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{Workspace: store, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, err := server.settingsStore.Set(approvalDefaultsSettingKey, map[string]any{
		"mode": "ask", "rememberDecisions": true,
	}); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"file_path": "report.md", "new_string": "updated"}
	if _, err := server.rememberAgentRuntimeApproval("edit_file", input, "previous explicit approval", time.Now()); err != nil {
		t.Fatal(err)
	}
	permission := server.agentRuntimePermissionResultForSessionWithContext(
		context.Background(), frameID, "edit_file",
		agentruntime.ToolCall{ID: "deny-remembered", Name: "edit_file"}, input,
	)
	if permission == nil || permission["decision"] != "denied" {
		t.Fatalf("deny mode was bypassed by a remembered approval: %#v", permission)
	}
	permission = server.agentRuntimePermissionResultForSessionWithContext(
		context.Background(), frameID, managePackagesToolName,
		agentruntime.ToolCall{ID: "deny-package-list", Name: managePackagesToolName}, map[string]any{"mode": "list"},
	)
	if permission == nil || permission["decision"] != "denied" {
		t.Fatalf("deny mode was bypassed by a read-only approval exemption: %#v", permission)
	}
}

func TestChildFrameInheritsRootConversationPermissionMode(t *testing.T) {
	store, _, _ := newTranscriptWebFixture(t)
	rootID := "root-permission-inheritance"
	childID := "child-permission-inheritance"
	projectID := "project-permission-inheritance"
	seedTranscriptWebFrame(t, store, "local", projectID, rootID)
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: childID, ProjectID: projectID, ParentFrameID: rootID,
		AgentName: "OPERON", Status: "processing", ConversationType: "agent", Name: "permission child",
	}); err != nil {
		t.Fatal(err)
	}
	seedPermissionTestAgent(t, store, "local")
	if _, err := store.SetFrameRuntimeMetadata(rootID, workspace.FrameRuntimeMetadata{
		FrameID: rootID,
		ContextData: map[string]any{"web_assistant": map[string]any{
			"id": "synonbiomed:operon", "conversation_overrides": map[string]any{"permission": "allow"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata(childID, workspace.FrameRuntimeMetadata{
		FrameID: childID,
		ContextData: map[string]any{"web_assistant": map[string]any{
			"id": "synonbiomed:operon", "conversation_overrides": map[string]any{"permission": "default"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{Workspace: store, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	mode, err := server.webSessionApprovalMode(childID)
	if err != nil || mode != "allow" {
		t.Fatalf("child permission mode=%q err=%v want allow", mode, err)
	}
	permission := server.agentRuntimePermissionResultForSessionWithContext(
		context.Background(), childID, requestNetworkAccessToolName,
		agentruntime.ToolCall{ID: "child-network", Name: requestNetworkAccessToolName},
		map[string]any{"domain": "files.rcsb.org"},
	)
	if permission != nil {
		t.Fatalf("child did not inherit root full access: %#v", permission)
	}
	childFrame, found, err := store.GetCompatibilityFrame(childID)
	if err != nil || !found {
		t.Fatalf("child frame found=%t err=%v", found, err)
	}
	if err := server.setWebConversationPermissionMode(context.Background(), "local", childFrame, "smart"); err != nil {
		t.Fatalf("set permission from child frame: %v", err)
	}
	rootMetadata, found, err := store.GetFrameRuntimeMetadata(rootID)
	if err != nil || !found {
		t.Fatalf("root metadata found=%t err=%v", found, err)
	}
	rootAssistant := mapValue(rootMetadata.ContextData["web_assistant"])
	rootOverrides := mapValue(rootAssistant["conversation_overrides"])
	if rootOverrides["permission"] != "smart" {
		t.Fatalf("child permission update did not write root authority: %#v", rootOverrides)
	}
	if mode, err = server.webSessionApprovalMode(childID); err != nil || mode != "smart" {
		t.Fatalf("updated child permission mode=%q err=%v want smart", mode, err)
	}
}

func TestNetworkGrantMutationStopsOwnerKernelBeforeReturn(t *testing.T) {
	store, manager, app, identity := newKernelHostTestRuntime(
		t, filepath.Join(t.TempDir(), "workspace.db"), true,
	)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	if _, err := app.executeAgentKernelTool(context.Background(), identity, "repl", map[string]any{
		"code": "print('before-network-grant')",
	}); err != nil || manager.ActiveCount() != 1 {
		t.Fatalf("initial kernel active=%d err=%v", manager.ActiveCount(), err)
	}
	input := map[string]any{
		"domain": "api.example.org", "reason": "Fetch an official task source.",
		"human_description": "Granting task source access",
	}
	result, err := app.executeAgentPermissionTool(
		context.Background(), identity,
		agentruntime.ToolCall{ID: "network-grant", Name: requestNetworkAccessToolName},
		requestNetworkAccessToolName, input,
	)
	if err != nil || mapValue(result)["granted"] != true || manager.ActiveCount() != 0 {
		t.Fatalf("network grant result=%#v active=%d err=%v", result, manager.ActiveCount(), err)
	}
	if _, err := app.executeAgentKernelTool(context.Background(), identity, "repl", map[string]any{
		"code": "print('after-network-grant')",
	}); err != nil || manager.ActiveCount() != 1 {
		t.Fatalf("replacement kernel active=%d err=%v", manager.ActiveCount(), err)
	}
	if _, err := app.executeAgentPermissionTool(
		context.Background(), identity,
		agentruntime.ToolCall{ID: "network-grant-idempotent", Name: requestNetworkAccessToolName},
		requestNetworkAccessToolName, input,
	); err != nil || manager.ActiveCount() != 1 {
		t.Fatalf("idempotent network grant active=%d err=%v", manager.ActiveCount(), err)
	}
}

func TestTranscriptAgentToolApprovalParksResolvesAndRestoresOriginalApprovalResult(t *testing.T) {
	root := t.TempDir()
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-agent-tool-approval", "frame-agent-tool-approval")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: root})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, err := server.settingsStore.Set(approvalDefaultsSettingKey, map[string]any{"mode": "ask"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-agent-tool-approval", MessageUUID: "message-agent-tool-approval",
		ClientMessageID: "client-agent-tool-approval", Text: "Download the verified public structure.",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-agent-tool-approval")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "agent-tool-approval-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	authority := &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim}
	run := &sessionRunnerChatRun{SessionID: stream.SessionID, Attempt: int(claimed.Claim.Attempt), Transcript: authority}
	ctx := withTranscriptRunnerChatRun(context.Background(), run)
	input := map[string]any{
		"domain": "files.rcsb.org", "reason": "Download the selected coordinate file.",
		"human_description": "Requesting RCSB coordinate access",
	}
	arguments, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	call := agentruntime.ToolCall{ID: "network-access-call", Name: requestNetworkAccessToolName, Arguments: arguments}
	engine := server.newAgentRuntimeEngineWithContext(ctx, SessionRunnerChatOptions{
		SessionID: stream.SessionID, AllowedTools: []string{requestNetworkAccessToolName}, OutputLimitBytes: 64 << 10,
	})
	_, err = engine.Tools.Execute(ctx, call)
	var pause *agentruntime.PauseError
	if !errors.As(err, &pause) || pause.Status != "awaiting_approval" ||
		stringValue(pause.Data["approval_kind"]) != agentToolApprovalKind ||
		stringValue(pause.Data["target"]) != "files.rcsb.org" {
		t.Fatalf("pause=%#v err=%v", pause, err)
	}
	if _, err := server.pauseTranscriptRunnerForApproval(context.Background(), authority, pause); err != nil {
		t.Fatal(err)
	}
	frame, found, err := store.GetCompatibilityFrame(stream.FrameID)
	if err != nil || !found || frame.Status != "awaiting_user_response" {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	confirmations, err := server.webConversationPendingConfirmations(stream.FrameID)
	if err != nil || len(confirmations) != 1 || confirmations[0]["kind"] != agentToolApprovalKind ||
		confirmations[0]["tool"] != requestNetworkAccessToolName || confirmations[0]["target"] != "files.rcsb.org" {
		t.Fatalf("confirmations=%#v err=%v", confirmations, err)
	}
	request := httptest.NewRequest("POST", "/api/frames/"+stream.FrameID+"/resolve-input", nil)
	resolved, err := server.resolveCompatibilityInput(request, frame, compatibilityResolveInputRequest{
		Responses: []compatibilityInputResponse{{RequestID: stringValue(confirmations[0]["id"]), Action: "allow_once"}},
	})
	if err != nil || resolved.Status != "accepted" {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	allowed, err := server.loadAllowedDomains()
	if err != nil || !foldedSetContains(foldedSet(allowed...), "files.rcsb.org") {
		t.Fatalf("allowed=%#v err=%v", allowed, err)
	}
	approvalID := stringValue(pause.Data["approval_id"])
	entry, approvalFound, err := server.runtimeStore.Get(agentRuntimeApprovalNamespace, approvalID)
	value := mapValue(entry.Value)
	if err != nil || !approvalFound || value["status"] != "completed" || value["toolCallId"] != call.ID ||
		mapValue(value["result"])["granted"] != true {
		t.Fatalf("approval result=%#v found=%t err=%v", value, approvalFound, err)
	}
}

func TestAgentToolApprovalTargetExposesOnlyNamedResource(t *testing.T) {
	if got := agentToolApprovalTarget("edit_file", map[string]any{
		"file_path": "CRBN_binding_mode_analysis.md", "new_string": "private payload",
	}); got != "CRBN_binding_mode_analysis.md" {
		t.Fatalf("edit target=%q", got)
	}
	if got := agentToolApprovalTarget(requestNetworkAccessToolName, map[string]any{
		"domain": "files.rcsb.org", "reason": "download", "authorization": "secret",
	}); got != "files.rcsb.org" {
		t.Fatalf("network target=%q", got)
	}
}

func TestApprovedHostAndSSHRequestsResumeThroughTheOriginalFrameAuthority(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("target-runtime approval path is Linux")
	}
	root := t.TempDir()
	granted := t.TempDir()
	ssh := filepath.Join(root, "ssh")
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\nprintf 'approved-remote\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))

	store, _, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-approval", "project-approval", "frame-approval")
	if _, err := store.UpdateProject("project-approval", workspace.UpdateProjectInput{Path: &root}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertSSHProvider(workspace.ComputeProviderInput{Name: "ssh:approved", UserID: "owner-approval", Family: "ssh"}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{Workspace: store, FileRoot: root})
	if _, err := server.settingsStore.Set(approvalDefaultsSettingKey, map[string]any{"mode": "ask"}); err != nil {
		t.Fatal(err)
	}

	approveAndExecute := func(toolName, callID string, input map[string]any) map[string]any {
		t.Helper()
		pending := server.agentRuntimePermissionResultForSessionWithContext(context.Background(), "frame-approval", toolName, agentruntime.ToolCall{ID: callID, Name: toolName}, input)
		if pending == nil || pending["decision"] != "pending_approval" {
			t.Fatalf("%s permission=%#v", toolName, pending)
		}
		approvalID := stringValue(pending["approvalId"])
		entry, found, err := server.runtimeStore.Get(agentRuntimeApprovalNamespace, approvalID)
		if err != nil || !found || stringValue(mapValue(entry.Value)["sessionId"]) != "frame-approval" {
			t.Fatalf("%s approval entry=%#v found=%t err=%v", toolName, entry, found, err)
		}
		result, err := server.resolveAgentRuntimeApprovalMessage(context.Background(), "agent-runtime", map[string]any{
			"type": "agent_runtime_approval_response", "approvalId": approvalID, "approve": true,
		})
		if err != nil {
			t.Fatalf("%s approval execution: %v", toolName, err)
		}
		return mapValue(result)
	}

	hostResult := approveAndExecute(requestHostAccessToolName, "host-access", map[string]any{
		"host_path": granted, "mode": "rw", "human_description": "Granting project directory",
	})
	if hostResult["status"] != "completed" {
		t.Fatalf("host approval=%#v", hostResult)
	}
	grants, err := server.loadHostGrants("owner-approval")
	if err != nil || len(grants) != 1 || grants[0].Path != granted || grants[0].Mode != "read_write" {
		t.Fatalf("host grants=%#v err=%v", grants, err)
	}

	sshResult := approveAndExecute(sshComputeToolName, "ssh-access", map[string]any{
		"provider": "ssh:approved", "intent": "Inspect approved host", "command": "printf ok",
		"timeout_seconds": 5, "human_description": "Inspecting approved host",
	})
	if sshResult["status"] != "completed" || stringValue(mapValue(sshResult["result"])["stdout"]) != "approved-remote\n" {
		t.Fatalf("ssh approval=%#v", sshResult)
	}
}

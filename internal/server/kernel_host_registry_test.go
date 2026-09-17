package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/skills"
)

func TestKernelHostSkillsAndAgentsRegistryBridge(t *testing.T) {
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(t.TempDir(), "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)

	personalRoot := filepath.Join(app.fileRoot, "skills", "host_saved_skill")
	if err := os.MkdirAll(personalRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(personalRoot, compatibilitySkillDraftMarker), []byte("draft\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(personalRoot, "SKILL.md"), []byte("---\nname: host_saved_skill\ndescription: A host bridge test skill.\n---\n\nUse the original workflow.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded := skills.Load([]string{personalRoot}, skills.LoadOptions{MaxBodyBytes: 12000})
	if failures := loaded.LoadErrors(); len(failures) != 0 || len(loaded.Skills()) != 1 {
		t.Fatalf("personal test Skill did not load: failures=%#v skills=%#v", failures, loaded.Skills())
	}
	app.skillCatalog.UpsertSkill(loaded.Skills()[0])

	result := runKernelHostCell(t, app, identity, `
import host, json
listed = [item for item in host.skills.list() if item["name"] == "host_saved_skill"]
print(json.dumps(listed, sort_keys=True))
print(json.dumps(host.skills.read("host_saved_skill"), sort_keys=True))
host.skills.edit("host_saved_skill", "SKILL.md", "Use the updated workflow.", "Use the original workflow.")
print(json.dumps(host.skills.publish("host_saved_skill"), sort_keys=True))
created = host.agents.create("HOST_BRIDGE_TEST", "Host bridge test", "Host bridge test agent", "Use the test Skill.", ["host_saved_skill"])
print(json.dumps(created, sort_keys=True))
updated = host.agents.update("HOST_BRIDGE_TEST", {"description": "Updated host bridge test agent"})
print(json.dumps(updated, sort_keys=True))
print(json.dumps(host.agents.attach_skill("HOST_BRIDGE_TEST", "host_saved_skill"), sort_keys=True))
print(json.dumps(host.agents.detach_skill("HOST_BRIDGE_TEST", "host_saved_skill"), sort_keys=True))
print(json.dumps(host.agents.switch("HOST_BRIDGE_TEST"), sort_keys=True))
print(json.dumps(host.agents.delete("HOST_BRIDGE_TEST"), sort_keys=True))
`)
	if result["ok"] != true {
		t.Fatalf("registry bridge result=%#v", result)
	}
	stdout := stringValue(result["stdout"])
	if !strings.Contains(stdout, `"origin": "draft"`) ||
		!strings.Contains(stdout, `"name": "HOST_BRIDGE_TEST"`) ||
		!strings.Contains(stdout, `"switched": true`) ||
		!strings.Contains(stdout, `"deleted": "HOST_BRIDGE_TEST"`) {
		t.Fatalf("registry bridge stdout=%s", stdout)
	}
	if _, found, err := store.GetAgent(identity.access.UserID, "HOST_BRIDGE_TEST"); err != nil || found {
		t.Fatalf("deleted host bridge agent found=%t err=%v", found, err)
	}
	updatedSkill, err := os.ReadFile(filepath.Join(personalRoot, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updatedSkill), "updated workflow") {
		t.Fatalf("updated Skill content=%q", updatedSkill)
	}
	if _, err := os.Stat(filepath.Join(personalRoot, compatibilitySkillDraftMarker)); !os.IsNotExist(err) {
		t.Fatalf("Skill draft marker still exists, err=%v", err)
	}
}

func TestKernelHostRegistryRejectsUnsafeSkillDraftName(t *testing.T) {
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(t.TempDir(), "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)

	result := runKernelHostCell(t, app, identity, `
import host
host.skills.edit("../escape", "SKILL.md", "---\nname: escape\n---\nbody")
`)
	if result["ok"] != false || !strings.Contains(stringValue(result["stderr"]), "invalid Skill name") {
		t.Fatalf("unsafe Skill draft result=%#v", result)
	}
}

func TestKernelCapabilityInstallFullAccessSkipsFrontendApproval(t *testing.T) {
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(t.TempDir(), "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)

	seedPermissionTestAgent(t, store, identity.access.UserID)
	if _, err := store.SetFrameRuntimeMetadata(identity.access.Frame.ID, workspace.FrameRuntimeMetadata{
		FrameID: identity.access.Frame.ID,
		ContextData: map[string]any{"web_assistant": map[string]any{
			"id": "synonbiomed:operon", "conversation_overrides": map[string]any{"permission": "allow"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.requireKernelCapabilityInstallApproval(
		context.Background(), identity.access, "full-access-install", "host.skills.install", "skill",
		map[string]any{"repo": "https://github.com/example/reviewed", "skills": []any{"demo"}}, nil,
	); err != nil {
		t.Fatalf("full access capability decision=%v", err)
	}
	entries, err := app.runtimeStore.List(agentRuntimeApprovalNamespace)
	if err != nil || len(entries) != 0 {
		t.Fatalf("full access persisted frontend approvals=%#v err=%v", entries, err)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata(identity.access.Frame.ID)
	if err != nil || !found || len(compatibilityServerPendingInputs(metadata.ContextData)) != 0 {
		t.Fatalf("full access pending inputs=%#v found=%t err=%v", metadata.ContextData, found, err)
	}
}

func TestSwitchingToFullAccessAutoApprovesVisibleCapabilityRequest(t *testing.T) {
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(t.TempDir(), "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)

	seedPermissionTestAgent(t, store, identity.access.UserID)
	if _, err := store.SetFrameRuntimeMetadata(identity.access.Frame.ID, workspace.FrameRuntimeMetadata{
		FrameID: identity.access.Frame.ID,
		ContextData: map[string]any{"web_assistant": map[string]any{
			"id": "synonbiomed:operon", "conversation_overrides": map[string]any{"permission": "default"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	completed := make(chan error, 1)
	go func() {
		completed <- app.requireKernelCapabilityInstallApproval(
			context.Background(), identity.access, "switch-to-full", "host.skills.install", "skill",
			map[string]any{"repo": "https://github.com/example/reviewed", "skills": []any{"demo"}}, nil,
		)
	}()
	_, _ = waitForKernelCapabilityInstallApproval(t, app, "skill")
	current, found, err := store.GetCompatibilityFrame(identity.access.Frame.ID)
	if err != nil || !found {
		t.Fatalf("frame found=%t err=%v", found, err)
	}
	if err := app.setWebConversationPermissionMode(
		context.Background(), identity.access.UserID, current, "allow",
	); err != nil {
		t.Fatalf("switch to full access: %v", err)
	}
	select {
	case err := <-completed:
		if err != nil {
			t.Fatalf("auto-approved capability request: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("full access did not release the pending capability request")
	}
	confirmations, err := app.webConversationPendingConfirmations(identity.access.Frame.ID)
	if err != nil || len(confirmations) != 0 {
		t.Fatalf("visible approvals after full access=%#v err=%v", confirmations, err)
	}
}

func TestFullAccessConfirmationsReadConvergesLegacyVisibleCapabilityRequest(t *testing.T) {
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(t.TempDir(), "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)

	seedPermissionTestAgent(t, store, identity.access.UserID)
	if _, err := store.SetFrameRuntimeMetadata(identity.access.Frame.ID, workspace.FrameRuntimeMetadata{
		FrameID: identity.access.Frame.ID,
		ContextData: map[string]any{"web_assistant": map[string]any{
			"id": "synonbiomed:operon", "conversation_overrides": map[string]any{"permission": "default"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	completed := make(chan error, 1)
	go func() {
		completed <- app.requireKernelCapabilityInstallApproval(
			context.Background(), identity.access, "legacy-visible-full", "host.skills.install", "skill",
			map[string]any{"repo": "https://github.com/example/reviewed", "skills": []any{"demo"}}, nil,
		)
	}()
	_, _ = waitForKernelCapabilityInstallApproval(t, app, "skill")
	metadata, found, err := store.GetFrameRuntimeMetadata(identity.access.Frame.ID)
	if err != nil || !found {
		t.Fatalf("runtime metadata found=%t err=%v", found, err)
	}
	assistant := mapValue(metadata.ContextData["web_assistant"])
	overrides := mapValue(assistant["conversation_overrides"])
	overrides["permission"] = "allow"
	assistant["conversation_overrides"] = overrides
	metadata.ContextData["web_assistant"] = assistant
	if _, err := store.SetFrameRuntimeMetadata(identity.access.Frame.ID, metadata); err != nil {
		t.Fatal(err)
	}
	current, found, err := store.GetCompatibilityFrame(identity.access.Frame.ID)
	if err != nil || !found {
		t.Fatalf("frame found=%t err=%v", found, err)
	}
	request := httptest.NewRequest("GET", "/api/conversations/"+current.ID+"/confirmations", nil)
	request.Header.Set("X-Synon-User-Id", identity.access.UserID)
	response := httptest.NewRecorder()
	app.handleWebConversationConfirmations(response, request, current)
	if response.Code != 200 {
		t.Fatalf("confirmations status=%d body=%s", response.Code, response.Body.String())
	}
	var confirmations []map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &confirmations); err != nil || len(confirmations) != 0 {
		t.Fatalf("full access confirmations=%#v err=%v", confirmations, err)
	}
	select {
	case err := <-completed:
		if err != nil {
			t.Fatalf("legacy pending request was not auto-approved: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("full access confirmation read did not release the legacy approval")
	}
}

func TestSwitchingRootToFullAccessReleasesChildCapabilityApproval(t *testing.T) {
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(t.TempDir(), "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)

	rootID := identity.access.Frame.ID
	childID := "child-capability-approval"
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: childID, ProjectID: identity.access.Frame.ProjectID, ParentFrameID: rootID,
		AgentName: "OPERON", Status: "processing", ConversationType: "agent", Name: "capability child",
	}); err != nil {
		t.Fatal(err)
	}
	childAccess, found, err := store.GetKernelFrameAccess(childID)
	if err != nil || !found {
		t.Fatalf("child access found=%t err=%v", found, err)
	}
	seedPermissionTestAgent(t, store, identity.access.UserID)
	if _, err := store.SetFrameRuntimeMetadata(rootID, workspace.FrameRuntimeMetadata{
		FrameID: rootID,
		ContextData: map[string]any{"web_assistant": map[string]any{
			"id": "synonbiomed:operon", "conversation_overrides": map[string]any{"permission": "default"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	completed := make(chan error, 1)
	go func() {
		completed <- app.requireKernelCapabilityInstallApproval(
			ctx, childAccess, "child-switch-to-full", "host.skills.install", "skill",
			map[string]any{"repo": "https://github.com/example/reviewed", "skills": []any{"demo"}}, nil,
		)
	}()
	_, _ = waitForKernelCapabilityInstallApproval(t, app, "skill")
	childConfirmations, err := app.webConversationPendingConfirmations(childID)
	if err != nil || len(childConfirmations) != 1 {
		t.Fatalf("child confirmations=%#v err=%v", childConfirmations, err)
	}
	rootFrame, found, err := store.GetCompatibilityFrame(rootID)
	if err != nil || !found {
		t.Fatalf("root frame found=%t err=%v", found, err)
	}
	if err := app.setWebConversationPermissionMode(context.Background(), identity.access.UserID, rootFrame, "allow"); err != nil {
		t.Fatalf("switch root to full access: %v", err)
	}
	select {
	case err := <-completed:
		if err != nil {
			t.Fatalf("child approval was not auto-approved: %v", err)
		}
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("root full access did not release the child approval")
	}
	childConfirmations, err = app.webConversationPendingConfirmations(childID)
	if err != nil || len(childConfirmations) != 0 {
		t.Fatalf("child approvals after root full access=%#v err=%v", childConfirmations, err)
	}
}

func TestKernelHostMCPInstallListAndRemove(t *testing.T) {
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(t.TempDir(), "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)

	returned := make(chan kernelHostCellReturn, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		result, err := app.executeAgentKernelTool(ctx, identity, "Operon", map[string]any{"code": `
import host, json
created = host.mcp.install({"name": "Host MCP Bridge Test", "description": "test", "transport": "stdio", "command": "python3", "args": ["-c", "print('unused')"]})
print(json.dumps(created, sort_keys=True))
listed = [item for item in host.mcp.list() if item["name"] == created["id"]]
print(json.dumps(listed, sort_keys=True))
print(json.dumps(host.mcp.remove(created["id"]), sort_keys=True))
`})
		returned <- kernelHostCellReturn{result: result, err: err}
	}()
	approvalID, approvalValue := waitForKernelCapabilityInstallApproval(t, app, "mcp")
	if approvalValue["mcpName"] != "Host MCP Bridge Test" || approvalValue["mcpCommand"] != "python3" {
		t.Fatalf("MCP install approval metadata=%#v", approvalValue)
	}
	approval, err := app.resolveAgentRuntimeApprovalMessage(context.Background(), "", map[string]any{
		"approvalId": approvalID, "approve": true,
	})
	if err != nil || mapValue(approval)["status"] != "approved" {
		t.Fatalf("MCP install approval=%#v err=%v", approval, err)
	}
	var result map[string]any
	select {
	case completed := <-returned:
		if completed.err != nil {
			t.Fatal(completed.err)
		}
		result = completed.result
	case <-time.After(8 * time.Second):
		t.Fatal("approved MCP installation did not resume")
	}
	if result["ok"] != true {
		t.Fatalf("MCP registry bridge result=%#v", result)
	}
	stdout := stringValue(result["stdout"])
	if !strings.Contains(stdout, `"installed": true`) ||
		!strings.Contains(stdout, `"deleted":`) ||
		!strings.Contains(stdout, `"source": "custom"`) {
		t.Fatalf("MCP registry bridge stdout=%s", stdout)
	}
}

func TestKernelHostSkillInstallRequiresApprovalBeforeNetworkAccess(t *testing.T) {
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(t.TempDir(), "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)

	returned := make(chan kernelHostCellReturn, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		result, err := app.executeAgentKernelTool(ctx, identity, "Operon", map[string]any{"code": `
import host
host.skills.install("https://github.com/example/not-contacted", ["demo"])
`})
		returned <- kernelHostCellReturn{result: result, err: err}
	}()
	approvalID, approvalValue := waitForKernelCapabilityInstallApproval(t, app, "skill")
	if approvalValue["repository"] != "https://github.com/example/not-contacted" {
		t.Fatalf("Skill install approval metadata=%#v", approvalValue)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata(identity.access.Frame.ID)
	if err != nil || !found || len(compatibilityServerPendingInputs(metadata.ContextData)) != 1 {
		t.Fatalf("visible capability approval metadata=%#v found=%t err=%v", metadata.ContextData, found, err)
	}
	confirmationID := "web-confirmation-add:" + identity.access.Frame.ID + ":" + webEventComponent(approvalID)
	confirmation := waitForKernelArtifactRealtimeEventTest(t, store, confirmationID, "confirmation.add")
	options, _ := confirmation.Payload["options"].([]any)
	if confirmation.Payload["kind"] != "capability_install" || len(options) != 2 || confirmation.Payload["rememberable"] != false {
		t.Fatalf("visible capability confirmation=%#v", confirmation)
	}
	current, found, err := store.GetCompatibilityFrame(identity.access.Frame.ID)
	if err != nil || !found {
		t.Fatalf("compatibility frame found=%t err=%v", found, err)
	}
	deny := false
	resolved, err := app.resolveCompatibilityInput(
		httptest.NewRequest("POST", "/api/frames/"+current.ID+"/resolve-input", nil),
		current,
		compatibilityResolveInputRequest{Responses: []compatibilityInputResponse{{RequestID: approvalID, Approved: &deny, Action: "deny", Scope: "once"}}},
	)
	if err != nil || resolved.Frame.ID != current.ID {
		t.Fatalf("Skill install denial resolve=%#v err=%v", resolved, err)
	}
	entry, found, err := app.runtimeStore.Get(agentRuntimeApprovalNamespace, approvalID)
	if err != nil || !found || mapValue(entry.Value)["status"] != "denied" {
		t.Fatalf("Skill install denial authority=%#v found=%t err=%v", entry.Value, found, err)
	}
	metadata, found, err = store.GetFrameRuntimeMetadata(identity.access.Frame.ID)
	if err != nil || !found || len(compatibilityServerPendingInputs(metadata.ContextData)) != 0 {
		t.Fatalf("resolved capability approval metadata=%#v found=%t err=%v", metadata.ContextData, found, err)
	}
	select {
	case completed := <-returned:
		if completed.err != nil {
			t.Fatal(completed.err)
		}
		if completed.result["ok"] != false || !strings.Contains(stringValue(completed.result["stderr"]), "approval was denied") {
			t.Fatalf("denied Skill installation result=%#v", completed.result)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("denied Skill installation did not resume")
	}
}

type kernelHostCellReturn struct {
	result map[string]any
	err    error
}

func waitForKernelCapabilityInstallApproval(t *testing.T, app *Server, capabilityKind string) (string, map[string]any) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := app.runtimeStore.List(agentRuntimeApprovalNamespace)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			value := mapValue(entry.Value)
			if value["status"] == "pending" &&
				value["approvalSource"] == kernelCapabilityInstallApprovalSource &&
				value["capabilityKind"] == capabilityKind {
				metadata, found, metadataErr := app.workspaceStore.GetFrameRuntimeMetadata(stringValue(value["frameId"]))
				if metadataErr != nil {
					t.Fatal(metadataErr)
				}
				if !found {
					continue
				}
				for _, pending := range compatibilityServerPendingInputs(metadata.ContextData) {
					if compatibilityServerPendingInputID(pending) == entry.Key {
						return entry.Key, value
					}
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s capability installation approval was not queued", capabilityKind)
	return "", nil
}

func decodeKernelRegistryJSONLines(t *testing.T, stdout string) []map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	values := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var value map[string]any
		if err := json.Unmarshal([]byte(line), &value); err != nil {
			t.Fatal(err)
		}
		values = append(values, value)
	}
	return values
}

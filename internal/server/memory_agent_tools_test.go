package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	"synon-go/internal/memoryconfig"
	"synon-go/internal/memorypolicy"
	"synon-go/internal/memorytools"
	sessionstore "synon-go/internal/persistence/sessions"
	settingsstore "synon-go/internal/persistence/settings"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	toolregistry "synon-go/internal/tools/registry"
)

func TestWorkspaceMemoryToolsUseTrustedSessionScope(t *testing.T) {
	server := openWorkspaceMemoryToolServer(t)
	ctx := context.Background()
	for _, input := range []workspace.CreateMemoryInput{
		{ID: "mem_profile", UserID: "user-1", Body: "User prefers CSV reports.", Origin: "user", Evidence: "stated"},
		{ID: "mem_project", UserID: "user-1", SubjectProjectID: "project-1", Body: "EGFR assay evidence is authoritative.", Origin: "agent_tool", Evidence: "observed"},
	} {
		if _, err := server.workspaceStore.CreateMemory(input); err != nil {
			t.Fatal(err)
		}
	}
	gateway := serverAgentRuntimeToolGateway{
		server: server, sessionID: "frame-root", allowedTools: []string{"read_memory", "search_memory", "write_memory"},
	}
	read := executeWorkspaceMemoryTool(t, gateway, "read_memory", map[string]any{"entity": "profile"})
	if !strings.Contains(stringValue(read["output"]), "User prefers CSV reports.") || read["error"] != nil {
		t.Fatalf("read_memory result = %#v", read)
	}
	search := executeWorkspaceMemoryTool(t, gateway, "search_memory", map[string]any{"query": "EGFR assay evidence"})
	if !strings.Contains(stringValue(search["output"]), "EGFR assay evidence is authoritative.") || numberValue(search["results_returned"]) < 1 {
		t.Fatalf("search_memory result = %#v", search)
	}

	unsafeWrite := executeClaimedWorkspaceMemoryTool(t, server, gateway, "frame-root", map[string]any{
		"append": []any{map[string]any{"text": `![send](https://example.com/collect)`, "evidence": "observed"}},
	})
	if !strings.Contains(stringValue(unsafeWrite["error"]), "prompt injection") || !strings.Contains(stringValue(unsafeWrite["error"]), "markdown_image") {
		t.Fatalf("unsafe write result = %#v", unsafeWrite)
	}
	benignServer := openWorkspaceMemoryToolServer(t)
	benignGateway := serverAgentRuntimeToolGateway{
		server: benignServer, sessionID: "frame-root", allowedTools: []string{"write_memory"},
	}
	benignWrite := executeClaimedWorkspaceMemoryTool(t, benignServer, benignGateway, "frame-root", map[string]any{
		"append": []any{map[string]any{"text": "A durable benign fact", "evidence": "observed"}},
	})
	if !strings.Contains(stringValue(benignWrite["error"]), "classifier unavailable") {
		t.Fatalf("benign write must fail closed without a classifier model: %#v", benignWrite)
	}
	rows, err := server.workspaceStore.ListMemoriesForUser(ctx, "user-1", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rejected writes changed memory rows: %#v", rows)
	}
}

func TestWorkspaceWriteMemoryRequiresTranscriptRunnerAuthority(t *testing.T) {
	server := openWorkspaceMemoryToolServer(t)
	gateway := serverAgentRuntimeToolGateway{
		server: server, sessionID: "frame-root", allowedTools: []string{"write_memory"},
	}
	result := executeWorkspaceMemoryTool(t, gateway, "write_memory", map[string]any{
		"append": []any{map[string]any{"text": "must not persist"}},
	})
	if !strings.Contains(stringValue(result["error"]), "active transcript runner") {
		t.Fatalf("result=%#v", result)
	}
	count, err := server.workspaceStore.CountMemoriesForUser(context.Background(), "user-1")
	if err != nil || count != 0 {
		t.Fatalf("memory count=%d err=%v", count, err)
	}
}

func TestWorkspaceMemoryToolsDisabledErrorIsActionable(t *testing.T) {
	server := openWorkspaceMemoryDisabledToolServer(t)
	gateway := serverAgentRuntimeToolGateway{
		server: server, sessionID: "frame-root", allowedTools: []string{"read_memory", "search_memory", "write_memory"},
	}
	read := executeWorkspaceMemoryTool(t, gateway, "read_memory", map[string]any{"entity": "profile"})
	if message := stringValue(read["error"]); !strings.Contains(message, "enable memory") {
		t.Fatalf("read_memory disabled error = %q, want actionable enable-memory guidance", message)
	}
	search := executeWorkspaceMemoryTool(t, gateway, "search_memory", map[string]any{"query": "anything"})
	if message := stringValue(search["error"]); !strings.Contains(message, "enable memory") {
		t.Fatalf("search_memory disabled error = %q, want actionable enable-memory guidance", message)
	}
	if message := claimedMemoryToolError(errWorkspaceMemoryDisabled); !strings.Contains(message, "enable memory") {
		t.Fatalf("claimedMemoryToolError(disabled) = %q, want actionable enable-memory guidance", message)
	}
}

func TestWorkspaceClaimedWriteMemoryHonorsSessionAndFrameOptOut(t *testing.T) {
	for _, test := range []struct {
		name    string
		frameID string
		prepare func(*testing.T, *Server)
	}{
		{name: "session", frameID: "frame-root", prepare: func(t *testing.T, server *Server) {
			if err := server.sessionStore.Upsert(sessionstore.Session{
				ID: "frame-root", Project: &sessionstore.Project{ID: "project-1"},
				Orchestration: map[string]any{"frame_id": "frame-root", "sessionConfig": map[string]any{"memory_mode": "off"}},
			}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "frame metadata", frameID: "frame-root", prepare: func(t *testing.T, server *Server) {
			if _, err := server.workspaceStore.SetFrameRuntimeMetadata("frame-root", workspace.FrameRuntimeMetadata{
				ContextData: map[string]any{"_original_input": map[string]any{"memory_mode": "off"}},
			}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "child session", frameID: "frame-child", prepare: func(t *testing.T, server *Server) {
			if _, err := server.workspaceStore.CreateFrame(workspace.CreateFrameInput{
				ID: "frame-child", ProjectID: "project-1", ParentFrameID: "frame-root",
				AgentName: "SPECIALIST", Status: "running", ConversationType: "agent",
			}); err != nil {
				t.Fatal(err)
			}
			if err := server.sessionStore.Upsert(sessionstore.Session{
				ID: "frame-child", Project: &sessionstore.Project{ID: "project-1"},
				Orchestration: map[string]any{"frame_id": "frame-child", "sessionConfig": map[string]any{"memory_mode": "off"}},
			}); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := openWorkspaceMemoryToolServer(t)
			test.prepare(t, server)
			gateway := serverAgentRuntimeToolGateway{server: server, sessionID: test.frameID, allowedTools: []string{"write_memory"}}
			result := executeClaimedWorkspaceMemoryTool(t, server, gateway, test.frameID, map[string]any{
				"entity": "frame", "append": []any{map[string]any{"text": "must remain disabled"}},
			})
			if stringValue(result["error"]) != memorytools.ErrMemoryUnavailable.Error() {
				t.Fatalf("result=%#v", result)
			}
			if count, err := server.workspaceStore.CountMemoriesForUser(context.Background(), "user-1"); err != nil || count != 0 {
				t.Fatalf("memory count=%d err=%v", count, err)
			}
			stream, err := server.transcriptStore.GetStream(context.Background(), "frame:"+test.frameID, "user-1")
			if err != nil || stream.NextEventID != 3 {
				t.Fatalf("stream=%#v err=%v", stream, err)
			}
		})
	}
}

func TestWorkspaceClaimedWriteMemoryReplaysReceiptAfterOptOut(t *testing.T) {
	server := openWorkspaceMemoryToolServer(t)
	server.memoryConfig.PIClassifierEnabled = false
	gateway := serverAgentRuntimeToolGateway{server: server, sessionID: "frame-root", allowedTools: []string{"write_memory"}}
	ctx, call := prepareClaimedWorkspaceMemoryTool(t, server, "frame-root", map[string]any{
		"append": []any{map[string]any{"text": "stable committed memory"}},
	})
	firstRaw, err := gateway.Execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	first, ok := firstRaw.Value.(map[string]any)
	firstIDs, idsOK := first["appended"].([]string)
	if !ok || !idsOK || len(firstIDs) != 1 {
		t.Fatalf("first=%#v", firstRaw.Value)
	}
	streamBefore, err := server.transcriptStore.GetStream(context.Background(), "frame:frame-root", "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := server.sessionStore.Upsert(sessionstore.Session{
		ID: "frame-root", Project: &sessionstore.Project{ID: "project-1"},
		Orchestration: map[string]any{"frame_id": "frame-root", "sessionConfig": map[string]any{"memory_mode": "off"}},
	}); err != nil {
		t.Fatal(err)
	}
	replayedRaw, err := gateway.Execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	replayed, ok := replayedRaw.Value.(map[string]any)
	if !ok || fmt.Sprint(replayed["appended"]) != fmt.Sprint(first["appended"]) || replayed["output"] != first["output"] {
		t.Fatalf("first=%#v replayed=%#v", first, replayed)
	}
	streamAfter, err := server.transcriptStore.GetStream(context.Background(), "frame:frame-root", "user-1")
	if err != nil || streamAfter.NextEventID != streamBefore.NextEventID {
		t.Fatalf("before=%#v after=%#v err=%v", streamBefore, streamAfter, err)
	}
	if count, err := server.workspaceStore.CountMemoriesForUser(context.Background(), "user-1"); err != nil || count != 1 {
		t.Fatalf("memory count=%d err=%v", count, err)
	}
}

func TestWorkspaceChildMemoryToolUsesRootScratchpadAndChildSourceFrame(t *testing.T) {
	server := openWorkspaceMemoryToolServer(t)
	if _, err := server.workspaceStore.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-child", ProjectID: "project-1", ParentFrameID: "frame-root",
		AgentName: "SPECIALIST", Status: "running", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.sessionStore.Upsert(sessionstore.Session{
		ID: "frame-child", Project: &sessionstore.Project{ID: "project-1"},
		Orchestration: map[string]any{"frame_id": "frame-child"},
	}); err != nil {
		t.Fatal(err)
	}
	gateway := serverAgentRuntimeToolGateway{
		server: server, sessionID: "frame-child", allowedTools: []string{"write_memory"},
	}
	result := executeClaimedWorkspaceMemoryTool(t, server, gateway, "frame-child", map[string]any{
		"entity": "frame", "append": []any{map[string]any{"text": "Child private scratch state", "evidence": "observed"}},
	})
	ids, ok := result["appended"].([]string)
	if !ok || len(ids) != 1 || result["error"] != nil {
		t.Fatalf("child write_memory result = %#v", result)
	}
	memory, found, err := server.workspaceStore.GetMemoryOwned(context.Background(), "user-1", ids[0])
	if err != nil || !found || memory.SubjectFrameID != "frame-root" || memory.SourceFrameID != "frame-child" {
		t.Fatalf("child memory provenance = %#v, %v, %v", memory, found, err)
	}
}

func TestWorkspaceMemoryToolsRejectMissingMismatchedAndOptedOutSessions(t *testing.T) {
	server := openWorkspaceMemoryToolServer(t)
	for _, test := range []struct {
		name      string
		sessionID string
		prepare   func()
		want      string
	}{
		{name: "missing", sessionID: "missing", want: "unavailable"},
		{name: "no session", want: "trusted agent session"},
		{name: "project mismatch", sessionID: "frame-root", prepare: func() {
			_ = server.sessionStore.Upsert(sessionstore.Session{
				ID: "frame-root", Project: &sessionstore.Project{ID: "project-other"}, Orchestration: map[string]any{"frame_id": "frame-root"},
			})
		}, want: "does not match"},
		{name: "session opt out", sessionID: "frame-root", prepare: func() {
			_ = server.sessionStore.Upsert(sessionstore.Session{
				ID: "frame-root", Project: &sessionstore.Project{ID: "project-1"},
				Orchestration: map[string]any{"frame_id": "frame-root", "sessionConfig": map[string]any{"memory_mode": "off"}},
			})
		}, want: "unavailable"},
		{name: "persisted frame opt out", sessionID: "frame-root", prepare: func() {
			if _, err := server.workspaceStore.SetFrameRuntimeMetadata("frame-root", workspace.FrameRuntimeMetadata{
				ContextData: map[string]any{"_original_input": map[string]any{"memory_mode": "off"}},
			}); err != nil {
				t.Fatal(err)
			}
		}, want: "unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server = openWorkspaceMemoryToolServer(t)
			if test.prepare != nil {
				test.prepare()
			}
			gateway := serverAgentRuntimeToolGateway{server: server, sessionID: test.sessionID, allowedTools: []string{"read_memory"}}
			result := executeWorkspaceMemoryTool(t, gateway, "read_memory", map[string]any{"entity": "profile"})
			if !strings.Contains(stringValue(result["error"]), test.want) {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestWorkspaceMemoryToolSchemasAreExposedToAgentRuntime(t *testing.T) {
	server := openWorkspaceMemoryToolServer(t)
	schemas := server.agentRuntimeToolSchemas([]string{"read_memory", "write_memory", "search_memory"}, "frame-root")
	found := map[string]agentruntime.ToolSchema{}
	for _, schema := range schemas {
		found[schema.Name] = schema
	}
	for _, name := range []string{"read_memory", "write_memory", "search_memory"} {
		if _, exists := found[name]; !exists {
			t.Fatalf("missing %s schema in %#v", name, schemas)
		}
	}
	readRequired, _ := found["read_memory"].Parameters["required"].([]string)
	if len(readRequired) != 1 || readRequired[0] != "entity" {
		t.Fatalf("read_memory required schema = %#v", found["read_memory"].Parameters)
	}
	writeProperties, _ := found["write_memory"].Parameters["properties"].(map[string]any)
	appendSchema, _ := writeProperties["append"].(map[string]any)
	appendItem, _ := appendSchema["items"].(map[string]any)
	appendProperties, _ := appendItem["properties"].(map[string]any)
	textSchema, _ := appendProperties["text"].(map[string]any)
	if appendSchema["maxItems"] != memorypolicy.OperationsPerKindMax || textSchema["maxLength"] != memorypolicy.TextMaxUTF16Units {
		t.Fatalf("write_memory nested schema = %#v", found["write_memory"].Parameters)
	}
}

func TestWorkspaceMemoryToolSchemasAreOmittedWhenWorkspaceMemoryIsDisabled(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, *Server)
	}{
		{name: "workspace setting", prepare: func(_ *testing.T, _ *Server) {}},
		{name: "session opt out", prepare: func(t *testing.T, server *Server) {
			if err := server.workspaceStore.SetMemoryEnabled(context.Background(), "user-1", true); err != nil {
				t.Fatal(err)
			}
			if err := server.sessionStore.Upsert(sessionstore.Session{
				ID: "frame-root", Project: &sessionstore.Project{ID: "project-1"},
				Orchestration: map[string]any{"frame_id": "frame-root", "sessionConfig": map[string]any{"memory_mode": "off"}},
			}); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := openWorkspaceMemoryDisabledToolServer(t)
			test.prepare(t, server)
			schemas := server.agentRuntimeToolSchemas([]string{"read_memory", "write_memory", "search_memory"}, "frame-root")
			for _, schema := range schemas {
				if schema.Name == "read_memory" || schema.Name == "write_memory" || schema.Name == "search_memory" {
					t.Fatalf("disabled memory tool schema was exposed: %#v", schemas)
				}
			}
		})
	}
}

func executeWorkspaceMemoryTool(t *testing.T, gateway serverAgentRuntimeToolGateway, name string, input map[string]any) map[string]any {
	t.Helper()
	arguments, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{ID: "call-" + name, Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("%s gateway error = %v", name, err)
	}
	value, ok := result.Value.(map[string]any)
	if !ok {
		t.Fatalf("%s gateway value = %#v", name, result.Value)
	}
	return value
}

func executeClaimedWorkspaceMemoryTool(
	t *testing.T,
	server *Server,
	gateway serverAgentRuntimeToolGateway,
	frameID string,
	input map[string]any,
) map[string]any {
	t.Helper()
	ctx, call := prepareClaimedWorkspaceMemoryTool(t, server, frameID, input)
	toolResult, err := gateway.Execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	result, ok := toolResult.Value.(map[string]any)
	if !ok {
		t.Fatalf("write_memory gateway value=%#v", toolResult.Value)
	}
	return result
}

func prepareClaimedWorkspaceMemoryTool(
	t *testing.T,
	server *Server,
	frameID string,
	input map[string]any,
) (context.Context, agentruntime.ToolCall) {
	t.Helper()
	access, found, err := server.workspaceStore.GetKernelFrameAccessContext(context.Background(), frameID)
	if err != nil || !found {
		t.Fatalf("frame access found=%t err=%v", found, err)
	}
	rootFrameID := access.Frame.RootFrameID
	if rootFrameID == "" {
		rootFrameID = access.Frame.ID
	}
	repo := server.transcriptStore
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:" + access.Frame.ID, OwnerID: access.UserID, ExternalID: access.Frame.ID, SessionID: access.Frame.ID,
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: access.Frame.ProjectID,
		RootFrameID: rootFrameID, FrameID: access.Frame.ID, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stream.InputRevision == 0 {
		if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "memory-task", FrameEventID: "memory-task-event",
			MessageUUID: "memory-task-message", Text: "Use memory tools.", Destinations: []string{"ws"},
		}); err != nil || !created {
			t.Fatalf("append input created=%t err=%v", created, err)
		}
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "memory-runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	rawInput, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"toolCallId": "call-write_memory", "toolName": "write_memory", "toolPhase": "start", "toolInput": json.RawMessage(rawInput),
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(rawInput)
	clientID := "memory-source-" + hex.EncodeToString(digest[:])
	_, source, _, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claimed.Claim, ClientMessageID: clientID, Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: rawPayload,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withTranscriptArtifactRun(context.Background(), &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim}, source.EventID)
	arguments, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, agentruntime.ToolCall{ID: "call-write_memory", Name: "write_memory", Arguments: arguments}
}

func openWorkspaceMemoryToolServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	workspaceStore, err := workspace.Open(filepath.Join(root, "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaceStore.Close() })
	if _, err := workspaceStore.CreateProject(workspace.CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := workspaceStore.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-root", ProjectID: "project-1", AgentName: "GENERAL", Status: "running", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	if err := workspaceStore.SetMemoryEnabled(context.Background(), "user-1", true); err != nil {
		t.Fatal(err)
	}
	transcriptStore, err := workspaceStore.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sessions := sessionstore.NewStore(root)
	if err := sessions.Upsert(sessionstore.Session{
		ID: "frame-root", Project: &sessionstore.Project{ID: "project-1"}, Orchestration: map[string]any{"frame_id": "frame-root"},
	}); err != nil {
		t.Fatal(err)
	}
	memoryConfig := memoryconfig.Default()
	memoryConfig.Enabled = true
	return &Server{
		workspaceStore: workspaceStore, sessionStore: sessions, tools: toolregistry.Default(),
		transcriptStore: transcriptStore, memoryConfig: memoryConfig,
		settingsStore: settingsstore.New(filepath.Join(root, "settings.json")),
	}
}

func openWorkspaceMemoryDisabledToolServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	workspaceStore, err := workspace.Open(filepath.Join(root, "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaceStore.Close() })
	if _, err := workspaceStore.CreateProject(workspace.CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := workspaceStore.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-root", ProjectID: "project-1", AgentName: "GENERAL", Status: "running", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	sessions := sessionstore.NewStore(root)
	if err := sessions.Upsert(sessionstore.Session{
		ID: "frame-root", Project: &sessionstore.Project{ID: "project-1"}, Orchestration: map[string]any{"frame_id": "frame-root"},
	}); err != nil {
		t.Fatal(err)
	}
	return &Server{
		workspaceStore: workspaceStore, sessionStore: sessions, tools: toolregistry.Default(),
		memoryConfig: memoryconfig.Default(),
	}
}

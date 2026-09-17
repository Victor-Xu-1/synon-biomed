package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/toolcontract"
	"synon-go/internal/tools/registry"
	"synon-go/internal/tools/webfetch"
	"synon-go/internal/tools/websearch"
)

func TestSessionRunnerReadReuseKeyIgnoresPresentationOnlyDescription(t *testing.T) {
	base := map[string]any{"url": "https://example.org/paper", "limit": 4096}
	withDescription := map[string]any{"url": "https://example.org/paper", "limit": 4096, "human_description": "Reading the paper", "prompt": "Extract methods"}
	first, ok := sessionRunnerReadReuseKey("web_fetch", base)
	if !ok {
		t.Fatal("base input did not produce a reuse key")
	}
	second, ok := sessionRunnerReadReuseKey("WEB_FETCH", withDescription)
	if !ok || first != second {
		t.Fatalf("presentation metadata changed reuse key: %q vs %q", first, second)
	}
	changed, ok := sessionRunnerReadReuseKey("web_fetch", map[string]any{"url": "https://example.org/paper", "limit": 8192})
	if !ok || first == changed {
		t.Fatal("execution arguments were not included in reuse key")
	}
	one, _ := sessionRunnerReadReuseKey("future_read_tool", map[string]any{"url": base["url"], "prompt": "one"})
	other, _ := sessionRunnerReadReuseKey("future_read_tool", map[string]any{"url": base["url"], "prompt": "two"})
	if one == other {
		t.Fatal("execution prompt for an unknown read tool was incorrectly ignored")
	}
}

func TestSessionRunnerReadReuseMarksCachedWebFetchWithoutMutatingOriginal(t *testing.T) {
	run := &sessionRunnerChatRun{}
	input := map[string]any{"url": "https://example.org/paper"}
	original := webfetch.Result{StatusCode: 200, URL: input["url"].(string), Body: "content"}
	run.storeReadReuse("web_fetch", input, original)
	value, ok := run.lookupReadReuse("web_fetch", map[string]any{
		"url": "https://example.org/paper", "human_description": "same source",
	})
	if !ok {
		t.Fatal("cached read was not found")
	}
	cached, ok := value.(webfetch.Result)
	if !ok || !cached.Reused || cached.Body != original.Body {
		t.Fatalf("cached typed result=%#v", value)
	}
	if original.Reused {
		t.Fatal("cache lookup mutated the original result")
	}
}

func TestSessionRunnerReadReuseSkipsUnrecoverableWebFetchResponses(t *testing.T) {
	run := &sessionRunnerChatRun{}
	run.storeReadReuse("web_fetch", map[string]any{"url": "https://example.org/unavailable"}, webfetch.Result{
		SourceUnavailable: true,
	})
	if _, ok := run.lookupReadReuse("web_fetch", map[string]any{"url": "https://example.org/unavailable"}); ok {
		t.Fatal("unavailable source was cached")
	}
}

func TestServerAgentRuntimeReadReuseUsesExplicitToolCapability(t *testing.T) {
	server := &Server{tools: registry.Default()}
	withReuse := serverAgentRuntimeToolGateway{server: server, taskRun: &sessionRunnerChatRun{}}
	if !withReuse.readReuseEnabled("web_fetch") || !withReuse.readReuseEnabled("web_search") {
		t.Fatal("idempotent public reads were not enabled")
	}
	if withReuse.readReuseEnabled("file_write") {
		t.Fatal("mutating tool unexpectedly enabled read reuse")
	}
}

func TestServerAgentRuntimeReadReuseAvoidsDuplicatePublicFetch(t *testing.T) {
	requests := 0
	httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		_, _ = writer.Write([]byte("stable source body"))
	}))
	defer httpServer.Close()
	client := httpServer.Client()
	server := &Server{tools: registry.DefaultWithWebOptions(
		webfetch.Options{ClientForURL: func(context.Context, string) (*http.Client, error) { return client, nil }},
		websearch.Options{},
	)}
	run := &sessionRunnerChatRun{}
	gateway := serverAgentRuntimeToolGateway{server: server, taskRun: run}
	call := agentruntime.ToolCall{ID: "fetch-1", Name: "web_fetch"}
	input := map[string]any{"url": httpServer.URL}
	if _, err := gateway.executeAgentToolResponse(context.Background(), call, "web_fetch", input); err != nil {
		t.Fatalf("first fetch error: %v", err)
	}
	value, err := gateway.executeAgentToolResponse(context.Background(), call, "web_fetch", map[string]any{
		"url": httpServer.URL, "human_description": "same source",
	})
	if err != nil {
		t.Fatalf("cached fetch error: %v", err)
	}
	if requests != 1 {
		t.Fatalf("duplicate fetch reached network %d times", requests)
	}
	envelope, ok := value.(map[string]any)
	if !ok || envelope["reused"] != true {
		t.Fatalf("cached fetch did not expose reuse marker: %#v", value)
	}
}

func TestSessionRunnerReadReuseHydratesCompletedReplayReads(t *testing.T) {
	run := &sessionRunnerChatRun{}
	server := &Server{tools: registry.Default()}
	server.hydrateSessionRunnerReadReuse(run, []eventjournal.Entry{{Message: eventjournal.Message{
		"type": "runner_checkpoint", "toolName": "web_fetch",
		"toolPhase": "completed", "toolInput": map[string]any{"url": "https://example.org/paper"},
		"toolResult": map[string]any{"ok": true, "result": map[string]any{"statusCode": 200, "body": "paper"}},
	}}})
	value, ok := run.lookupReadReuse("web_fetch", map[string]any{"url": "https://example.org/paper"})
	if !ok {
		t.Fatal("completed replay read was not hydrated")
	}
	record, ok := value.(map[string]any)
	if !ok || record["reused"] != true {
		t.Fatalf("hydrated read=%#v", value)
	}
}

func TestSessionRunnerReadReuseHydratesRawJSONReplayReads(t *testing.T) {
	run := &sessionRunnerChatRun{}
	server := &Server{tools: registry.Default()}
	server.hydrateSessionRunnerReadReuse(run, []eventjournal.Entry{{Message: eventjournal.Message{
		"type": "runner_checkpoint", "toolName": "web_fetch", "toolPhase": "completed",
		"toolInput":  json.RawMessage(`{"url":"https://example.org/raw"}`),
		"toolResult": json.RawMessage(`{"ok":true,"result":{"statusCode":200,"body":"raw"}}`),
	}}})
	value, ok := run.lookupReadReuse("web_fetch", map[string]any{"url": "https://example.org/raw"})
	if !ok {
		t.Fatal("raw JSON replay read was not hydrated")
	}
	record, ok := value.(map[string]any)
	if !ok || record["reused"] != true {
		t.Fatalf("raw JSON hydrated read=%#v", value)
	}
}

func TestSessionRunnerReadReuseHydratesExternalizedResultAsRawEvidence(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	raw := []byte(`{"ok":true,"result":{"statusCode":200,"body":"` +
		strings.Repeat("durable source evidence ", 1200) + `"}}`)
	artifactID, _ := runnerLargeToolResultIdentities(fixture.stream, "prior-fetch", "web_fetch")
	record, err := fixture.store.WriteRunnerLargeToolResult(context.Background(), workspace.WriteRunnerLargeToolResultInput{
		ArtifactID: artifactID, ProjectID: fixture.stream.ProjectID, RootFrameID: fixture.stream.RootFrameID,
		FrameID: fixture.stream.FrameID, StreamUID: fixture.stream.UID, OwnerUserID: fixture.stream.OwnerID,
		RunnerID: fixture.claim.RunnerID, ClaimToken: fixture.claim.ClaimToken, Attempt: fixture.claim.Attempt,
		SourceEventID: 1, ToolName: "web_fetch", ToolCallID: "prior-fetch", Content: raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	descriptor := toolcontract.ExternalizedResultDescriptor{
		ArtifactID: record.ArtifactID, VersionID: record.VersionID, SHA256: record.ContentSHA256,
		SizeBytes: record.SizeBytes, ContentType: record.ContentType, Outcome: "succeeded",
		ContentURL: "/api/artifacts/" + record.ArtifactID + "/versions/" + record.VersionID,
		Preview:    string(raw[:128]), Truncated: true,
	}
	descriptorValue := map[string]any{}
	descriptorJSON, err := json.Marshal(descriptor)
	if err != nil || json.Unmarshal(descriptorJSON, &descriptorValue) != nil {
		t.Fatal(err)
	}
	descriptorValue["reused"] = true
	networkReads := 0
	networkSource := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		networkReads++
		_, _ = writer.Write([]byte("fresh network source"))
	}))
	defer networkSource.Close()
	fixture.server.tools = registry.DefaultWithWebOptions(
		webfetch.Options{ClientForURL: func(context.Context, string) (*http.Client, error) {
			return networkSource.Client(), nil
		}},
		websearch.Options{},
	)
	for _, test := range []struct {
		name       string
		toolName   string
		toolCallID string
	}{
		{name: "wrong tool name", toolName: "web_research", toolCallID: "prior-fetch"},
		{name: "wrong tool call id", toolName: "web_fetch", toolCallID: "other-fetch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			negativeRun := &sessionRunnerChatRun{
				SessionID: fixture.stream.SessionID, Attempt: int(fixture.claim.Attempt),
				ClaimToken: fixture.claim.ClaimToken,
				Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
			}
			input := map[string]any{"url": networkSource.URL}
			fixture.server.hydrateSessionRunnerReadReuse(negativeRun, []eventjournal.Entry{{Message: eventjournal.Message{
				"type": "runner_checkpoint", "toolName": test.toolName, "toolPhase": "completed",
				"toolCallId": test.toolCallID, "toolInput": input, "toolResult": descriptorValue,
			}}})
			if _, ok := negativeRun.lookupReadReuse("web_fetch", input); ok {
				t.Fatal("mismatched source identity populated the read cache")
			}
			gateway := serverAgentRuntimeToolGateway{server: fixture.server, taskRun: negativeRun}
			response, err := gateway.executeAgentToolResponse(
				context.Background(), agentruntime.ToolCall{ID: "network-read", Name: "web_fetch"}, "web_fetch", input,
			)
			if err != nil {
				t.Fatalf("normal read path was blocked after cache rejection: %v", err)
			}
			encoded, _ := json.Marshal(response)
			if !strings.Contains(string(encoded), "fresh network source") {
				t.Fatalf("cache rejection did not reach the normal read path: %s", encoded)
			}
		})
	}
	if networkReads != 2 {
		t.Fatalf("normal read path count=%d want=2", networkReads)
	}
	run := &sessionRunnerChatRun{
		SessionID: fixture.stream.SessionID, Attempt: int(fixture.claim.Attempt),
		ClaimToken: fixture.claim.ClaimToken,
		Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
	}
	fixture.server.hydrateSessionRunnerReadReuse(run, []eventjournal.Entry{{Message: eventjournal.Message{
		"type": "runner_checkpoint", "toolName": "web_fetch", "toolPhase": "completed",
		"toolCallId": "prior-fetch",
		"toolInput":  map[string]any{"url": "https://example.org/large-paper"}, "toolResult": descriptorValue,
	}}})

	value, ok := run.lookupReadReuse("web_fetch", map[string]any{"url": "https://example.org/large-paper"})
	if !ok {
		t.Fatal("externalized durable read was not hydrated")
	}
	envelope := mapValue(value)
	result := mapValue(envelope["result"])
	if envelope["reused"] != true || strings.TrimSpace(stringValue(result["body"])) == "" {
		t.Fatalf("hydrated read did not restore raw evidence: %#v", value)
	}
	if stringValue(envelope["artifact_id"]) != "" || stringValue(envelope["version_id"]) != "" {
		t.Fatalf("prior call-bound descriptor leaked into the new read: %#v", value)
	}

	options := SessionRunnerChatOptions{
		SessionID: fixture.stream.SessionID, RunnerID: fixture.claim.RunnerID, OutputLimitBytes: 512,
	}
	call := agentruntime.ToolCall{
		ID: "current-fetch", Name: "web_fetch",
		Arguments: json.RawMessage(`{"url":"https://example.org/large-paper"}`),
	}
	if err := fixture.server.checkpointChatModelToolCalls(options, run, []agentruntime.ToolCall{call}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.server.checkpointChatTool(
		options, run, "running", "tool web_fetch started", call.ID, "start",
		map[string]any{"toolName": call.Name, "toolInput": decodeToolEventJSON(string(call.Arguments))},
	); err != nil {
		t.Fatal(err)
	}
	reusedRaw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	engine := agentruntime.Engine{
		MaxToolResultBytes: options.OutputLimitBytes,
		LargeToolResults:   runnerLargeToolResultAuthority{server: fixture.server},
	}
	materialized, err := engine.MaterializeRawToolResult(
		withTranscriptRunnerChatRun(context.Background(), run), call, reusedRaw,
		agentruntime.ClassifyToolResult(value),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.server.checkpointChatTool(
		options, run, "completed", "tool web_fetch completed", call.ID, "completed",
		map[string]any{
			"toolName": call.Name, "toolInput": decodeToolEventJSON(string(call.Arguments)),
			"toolResult": decodeToolEventJSON(string(materialized.JSON)),
		},
	); err != nil {
		t.Fatal(err)
	}
	batchID := run.ToolBatchIDs[call.ID]
	batch, found, err := fixture.store.GetToolCallBatch(context.Background(), fixture.stream.OwnerID, batchID)
	items, itemsErr := fixture.store.ListToolCallBatchItems(context.Background(), fixture.stream.OwnerID, batchID)
	if err != nil || itemsErr != nil || !found || batch.State != workspace.ToolCallBatchStateSettled ||
		len(items) != 1 || items[0].State != workspace.ToolCallBatchItemStateCompleted ||
		items[0].ResultRef == "artifact-version:"+record.VersionID {
		t.Fatalf("batch=%#v items=%#v found=%t err=%v itemsErr=%v", batch, items, found, err, itemsErr)
	}
}

package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestDurableEvidenceRestoresExternalizedPayload(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	ctx, run := appendLargeToolResultSource(t, fixture, "large-evidence", "web_research")
	raw := []byte(`{"ok":true,"result":{"padding":"` + strings.Repeat("large-data-", 500) + `","documents":[{"url":"https://example.org/record","readReceipt":{"deepRead":true}}]}}`)
	descriptor, err := (runnerLargeToolResultAuthority{server: fixture.server}).Externalize(ctx, agentruntime.LargeToolResultInput{ToolCall: agentruntime.ToolCall{ID: "large-evidence", Name: "web_research"}, RawJSON: raw, Outcome: agentruntime.ToolResultSucceeded, MaxInlineBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"toolName": "web_research", "toolPhase": "completed", "toolCallId": "large-evidence", "toolInput": map[string]any{}, "toolResult": descriptor})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{Claim: fixture.claim, ClientMessageID: "large-evidence-complete", Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: payload}); err != nil {
		t.Fatal(err)
	}
	messages, err := fixture.server.sessionRunnerDurableEvidenceMessages(context.Background(), run)
	if err != nil || len(messages) != 2 || messages[1].Content != string(raw) {
		t.Fatalf("durable evidence replay lost original bytes: messages=%d err=%v", len(messages), err)
	}
	depth := runnerEvidenceRecordDepthIndexFromMessages(messages)
	if !depth.contains("web", runnerEvidenceCanonicalWeb("https://example.org/record")) {
		t.Fatal("completed record disappeared behind the preview")
	}
	encoded, _ := json.Marshal(descriptor)
	checkpoint := sessionRunnerDurableToolCheckpoint{ToolCallID: "large-evidence", ToolName: "web_research"}
	for _, change := range []string{"owner", "frame", "call", "hash"} {
		stream, call, desc := fixture.stream, checkpoint, descriptor
		switch change {
		case "owner":
			stream.OwnerID = "other"
		case "frame":
			stream.FrameID = "other"
		case "call":
			call.ToolCallID = "other"
		case "hash":
			desc.SHA256 = strings.Repeat("0", 64)
		}
		encoded, _ = json.Marshal(desc)
		if value, err := fixture.server.restoreDurableEvidencePayload(context.Background(), stream, call, 1<<30, string(encoded)); err == nil || value != "" {
			t.Fatalf("%s mismatch crossed receipt authority: %v", change, err)
		}
	}
}

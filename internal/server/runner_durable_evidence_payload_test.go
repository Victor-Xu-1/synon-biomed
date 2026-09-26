package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

func TestDurableEvidenceTreatsMissingExternalizedPayloadAsUnavailable(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	ctx, run := appendLargeToolResultSource(t, fixture, "missing-evidence", "web_research")
	raw := []byte(`{"ok":true,"result":{"padding":"` + strings.Repeat("x", 2000) + `","documents":[{"url":"https://example.org/missing"}]}}`)
	descriptor, err := (runnerLargeToolResultAuthority{server: fixture.server}).Externalize(ctx, agentruntime.LargeToolResultInput{
		ToolCall: agentruntime.ToolCall{ID: "missing-evidence", Name: "web_research"}, RawJSON: raw,
		Outcome: agentruntime.ToolResultSucceeded, MaxInlineBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := fixture.store.FindRunnerLargeToolResult(ctx, descriptor.ArtifactID, "missing-evidence", "web_research")
	if err != nil || !found {
		t.Fatalf("externalized record found=%t err=%v", found, err)
	}
	blobPath := filepath.Join(fixture.databasePath+".blobs", filepath.FromSlash(record.StoragePath))
	if err := os.Remove(blobPath); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"owner", "stream", "project", "root", "frame", "call", "tool", "hash", "size", "type", "version", "source"} {
		t.Run("missing_with_"+change+"_conflict", func(t *testing.T) {
			stream, desc := fixture.stream, descriptor
			call := sessionRunnerDurableToolCheckpoint{ToolCallID: "missing-evidence", ToolName: "web_research"}
			completedID := int64(1 << 30)
			switch change {
			case "owner":
				stream.OwnerID = "other"
			case "stream":
				stream.UID = "other"
			case "project":
				stream.ProjectID = "other"
			case "root":
				stream.RootFrameID = "other"
			case "frame":
				stream.FrameID = "other"
			case "call":
				call.ToolCallID = "other"
			case "tool":
				call.ToolName = "other"
			case "hash":
				desc.SHA256 = strings.Repeat("0", 64)
			case "size":
				desc.SizeBytes++
			case "type":
				desc.ContentType = "text/plain"
			case "version":
				desc.VersionID += "-other"
			case "source":
				completedID = record.SourceEventID
			}
			encoded, marshalErr := json.Marshal(desc)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			value, restoreErr := fixture.server.restoreDurableEvidencePayload(ctx, stream, call, completedID, string(encoded))
			if value != "" || restoreErr == nil || errors.Is(restoreErr, errRunnerLargeToolResultUnavailable) {
				t.Fatalf("missing content hid %s authority conflict: value=%q err=%v", change, value, restoreErr)
			}
		})
	}

	encoded, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	value, err := fixture.server.restoreDurableEvidencePayload(ctx, fixture.stream,
		sessionRunnerDurableToolCheckpoint{ToolCallID: "missing-evidence", ToolName: "web_research"},
		1<<30, string(encoded))
	if value != "" || !errors.Is(err, errRunnerLargeToolResultUnavailable) {
		t.Fatalf("missing immutable payload was not classified as unavailable: value=%q err=%v", value, err)
	}
	payload, err := json.Marshal(map[string]any{
		"toolName": "web_research", "toolPhase": "completed", "toolCallId": "missing-evidence",
		"toolInput": map[string]any{}, "toolResult": descriptor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(ctx, transcriptstore.AppendRunnerCheckpointInput{
		Claim: fixture.claim, ClientMessageID: "missing-evidence-complete",
		Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: payload,
	}); err != nil {
		t.Fatal(err)
	}
	messages, err := fixture.server.sessionRunnerDurableEvidenceMessages(ctx, run)
	if err != nil || len(messages) != 0 {
		// Durable evidence replay intentionally omits unavailable historical
		// payloads. It must not silently replace the missing result with its
		// bounded preview.
		t.Fatalf("unavailable durable evidence was replayed: messages=%d err=%v", len(messages), err)
	}
	materials, err := fixture.server.sessionRunnerResearchMaterials(ctx, run)
	if err != nil || len(materials.Attempts) != 1 || materials.Attempts[0].MaterialUsable {
		t.Fatalf("missing research payload did not remain an unreadable attempt: attempts=%d receipts=%d err=%v", len(materials.Attempts), len(materials.Receipts), err)
	}
}

func TestResearchMaterialRecoveryRejectsConflictingExternalizedPayload(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	ctx, run := appendLargeToolResultSource(t, fixture, "conflicting-evidence", "web_research")
	raw := []byte(`{"ok":true,"result":{"padding":"` + strings.Repeat("x", 2000) + `","documents":[{"url":"https://example.org/conflict"}]}}`)
	descriptor, err := (runnerLargeToolResultAuthority{server: fixture.server}).Externalize(ctx, agentruntime.LargeToolResultInput{
		ToolCall: agentruntime.ToolCall{ID: "conflicting-evidence", Name: "web_research"}, RawJSON: raw,
		Outcome: agentruntime.ToolResultSucceeded, MaxInlineBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	descriptor.SHA256 = strings.Repeat("0", 64)
	payload, err := json.Marshal(map[string]any{
		"toolName": "web_research", "toolPhase": "completed", "toolCallId": "conflicting-evidence",
		"toolInput": map[string]any{}, "toolResult": descriptor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(ctx, transcriptstore.AppendRunnerCheckpointInput{
		Claim: fixture.claim, ClientMessageID: "conflicting-evidence-complete",
		Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: payload,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.server.sessionRunnerResearchMaterials(ctx, run); !errors.Is(err, errRunnerLargeToolResultConflict) {
		t.Fatalf("conflicting historical payload was not a hard failure: %v", err)
	}
}

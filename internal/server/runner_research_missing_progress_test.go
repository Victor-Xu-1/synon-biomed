package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestResearchSourceRoutingRecoversAfterMissingProgressReceipt(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	ctx, run := appendLargeToolResultSource(t, fixture, "missing-progress", updateStepStatusToolName)
	appendReceipt := func(id, tool string, input, result any) {
		t.Helper()
		raw, err := json.Marshal(map[string]any{
			"toolName": tool, "toolPhase": "completed", "toolCallId": id,
			"toolInput": input, "toolResult": result,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(ctx, transcriptstore.AppendRunnerCheckpointInput{
			Claim: fixture.claim, ClientMessageID: id + "-completed", Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: raw,
		}); err != nil {
			t.Fatal(err)
		}
	}
	progress := func(status string) map[string]any {
		return map[string]any{"ok": true, "step": "module-1", "status": status}
	}
	appendReceipt("start", updateStepStatusToolName, progress("in_progress"), progress("in_progress"))
	raw, _ := json.Marshal(map[string]any{
		"ok": true, "step": "module-1", "status": "completed", "padding": strings.Repeat("x", 3000),
	})
	descriptor, err := (runnerLargeToolResultAuthority{server: fixture.server}).Externalize(ctx, agentruntime.LargeToolResultInput{
		ToolCall: agentruntime.ToolCall{ID: "missing-progress", Name: updateStepStatusToolName}, RawJSON: raw,
		Outcome: agentruntime.ToolResultSucceeded, MaxInlineBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := fixture.store.GetRunnerLargeToolResult(ctx, descriptor.ArtifactID, fixture.stream.OwnerID)
	if err != nil || !found {
		t.Fatalf("stored progress found=%t err=%v", found, err)
	}
	if err := os.Remove(filepath.Join(fixture.databasePath+".blobs", filepath.FromSlash(record.StoragePath))); err != nil {
		t.Fatal(err)
	}
	// The requested state must not replace the missing applied result, and the
	// previously active module must not retain source routing authority either.
	appendReceipt("missing-progress", updateStepStatusToolName, progress("in_progress"), descriptor)
	appendSource := func(id string) {
		appendReceipt(id, "web_fetch", map[string]any{"url": "https://example.test/" + id},
			map[string]any{"ok": true, "result": map[string]any{"body": strings.Repeat("substantive source evidence ", 80)}})
	}
	appendSource("uncertain-routing")
	appendReceipt("restart", updateStepStatusToolName, progress("in_progress"), progress("in_progress"))
	appendSource("fresh-routing")
	materials, err := fixture.server.sessionRunnerResearchMaterials(context.Background(), run)
	if err != nil {
		t.Fatalf("missing progress prevents recovery: %v", err)
	}
	if len(materials.Receipts) != 2 || len(materials.Receipts[0].InvestigationIDs) != 0 ||
		len(materials.Receipts[1].InvestigationIDs) != 1 || materials.Receipts[1].InvestigationIDs[0] != "module-1" {
		t.Fatalf("missing applied progress invented or retained routing: %#v", materials.Receipts)
	}
	if _, err := fixture.server.sessionRunnerResearchModelContext(context.Background(), run); err != nil {
		t.Fatalf("model context could not resume: %v", err)
	}
}

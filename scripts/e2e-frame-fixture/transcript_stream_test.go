package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestTranscriptStreamFixtureAppendsDurableDeltasAndRejectsTamperedRun(t *testing.T) {
	store, repository, stream := transcriptFixtureTestStream(t)
	response, err := beginTranscriptStreamFixture(store, stream.FrameID, "delta-run")
	if err != nil || response["ok"] != true {
		t.Fatalf("begin response=%#v err=%v", response, err)
	}
	run, ok := response["run"].(transcriptStreamFixtureRun)
	if !ok || run.StreamUID != stream.UID || run.OwnerID != stream.OwnerID || run.ClaimToken == "" {
		t.Fatalf("run=%#v", response["run"])
	}
	input := transcriptStreamFixtureRequest{
		Run: &run, BatchID: "delta-batch", Chunks: []string{"first\n", "第二段"},
	}
	appended, err := appendTranscriptFixtureDeltas(store, stream.FrameID, input)
	if err != nil || appended["eventCount"] != 2 {
		t.Fatalf("appended=%#v err=%v", appended, err)
	}
	if _, err := appendTranscriptFixtureDeltas(store, stream.FrameID, input); err != nil {
		t.Fatalf("idempotent append failed: %v", err)
	}
	projected, err := repository.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, Limit: 20,
	})
	if err != nil || len(projected) != 4 {
		t.Fatalf("projected=%d err=%v", len(projected), err)
	}
	for index, expected := range []string{"user_message", "runner_checkpoint", "content_delta", "content_delta"} {
		if projected[index].Event.Type != expected {
			t.Fatalf("event %d type=%q want=%q", index, projected[index].Event.Type, expected)
		}
	}
	var payload map[string]any
	if err := json.Unmarshal(projected[3].ResolvedPayloadJSON, &payload); err != nil {
		t.Fatal(err)
	}
	segment, present, err := transcriptstore.ParseAssistantSegmentV1(payload)
	if err != nil || !present || segment.Ordinal != 1 || payload["text"] != "第二段" {
		t.Fatalf("payload=%#v segment=%#v present=%t err=%v", payload, segment, present, err)
	}
	tampered := run
	tampered.ClaimToken += "-tampered"
	if _, err := appendTranscriptFixtureDeltas(store, stream.FrameID, transcriptStreamFixtureRequest{
		Run: &tampered, BatchID: "tampered-batch", Chunks: []string{"must fail"},
	}); err == nil {
		t.Fatal("tampered runner claim appended a transcript event")
	}
}

func TestStreamingParityFixturePersistsToolLifecycleArtifactsAndTerminalReceipt(t *testing.T) {
	store, repository, stream := transcriptFixtureTestStream(t)
	_, version, err := store.WriteArtifactVersion(context.Background(), workspace.WriteArtifactVersionInput{
		ArtifactID: "fixture-report", ProjectID: stream.ProjectID, Name: "fixture-report.md",
		ContentType: "text/markdown", Content: bytes.NewBufferString("# Fixture report"), MaxBytes: 1024,
		RootFrameID: stream.RootFrameID, FrameID: stream.FrameID, IsUserUpload: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := beginTranscriptStreamFixture(store, stream.FrameID, "parity-run")
	if err != nil {
		t.Fatal(err)
	}
	run := response["run"].(transcriptStreamFixtureRun)
	copy := &transcriptStreamingParityCopy{
		Thinking: "Reviewing evidence.", Search: "Search structures", Compute: "Score candidates", Final: "Done.",
	}
	input := transcriptStreamFixtureRequest{Run: &run, BatchID: run.RunID, Copy: copy}
	if result, err := beginTranscriptStreamingParity(store, stream.FrameID, input); err != nil || result["eventCount"] != 4 {
		t.Fatalf("begin parity result=%#v err=%v", result, err)
	}
	input.ArtifactRefs = []transcriptFixtureArtifactReference{{
		ArtifactID: "fixture-report", VersionID: version.ID,
	}}
	if result, err := completeTranscriptStreamingParity(store, stream.FrameID, input); err != nil || result["eventCount"] != 3 {
		t.Fatalf("complete parity result=%#v err=%v", result, err)
	}
	projected, err := repository.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, Limit: 20,
	})
	if err != nil || len(projected) != 9 {
		t.Fatalf("projected=%d err=%v", len(projected), err)
	}
	var thinking map[string]any
	if err := json.Unmarshal(projected[2].ResolvedPayloadJSON, &thinking); err != nil ||
		projected[2].Event.Type != "content_delta" || thinking["block_type"] != "thinking" {
		t.Fatalf("thinking event=%#v payload=%#v err=%v", projected[2].Event, thinking, err)
	}
	toolStates := map[string][]string{}
	assistantRefs := 0
	for _, event := range projected {
		var payload map[string]any
		if err := json.Unmarshal(event.ResolvedPayloadJSON, &payload); err != nil {
			t.Fatal(err)
		}
		if event.Event.Type == "runner_checkpoint" {
			if callID, ok := payload["toolCallId"].(string); ok {
				toolStates[callID] = append(toolStates[callID], payload["status"].(string))
			}
		}
		if event.Event.Type == "assistant_message" {
			assistantRefs = len(event.ArtifactReferences)
		}
	}
	if got := toolStates["e2e-parity-search:parity-run"]; len(got) != 2 || got[0] != "running" || got[1] != "completed" {
		t.Fatalf("search states=%#v", got)
	}
	if got := toolStates["e2e-parity-compute:parity-run"]; len(got) != 2 || got[0] != "running" || got[1] != "completed" {
		t.Fatalf("compute states=%#v", got)
	}
	if assistantRefs != 1 || projected[len(projected)-1].Event.Type != "runner_finished" {
		t.Fatalf("assistant refs=%d terminal=%q", assistantRefs, projected[len(projected)-1].Event.Type)
	}
	frame, found, err := store.GetFrame(stream.FrameID)
	if err != nil || !found || frame.Status != "completed" {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
}

func TestStreamingRecoveryFixtureRetainsFailedOperationBeforeIndependentSuccess(t *testing.T) {
	store, repository, stream := transcriptFixtureTestStream(t)
	response, err := beginTranscriptStreamFixture(store, stream.FrameID, "recovery-run")
	if err != nil {
		t.Fatal(err)
	}
	run := response["run"].(transcriptStreamFixtureRun)
	recovery := &transcriptStreamingRecoveryCopy{
		Intro: "Starting recoverable work.", First: "Primary analysis",
		Recovery: "Primary analysis failed; retaining evidence and using the fallback.",
		Second:   "Fallback analysis", Final: "Recovery completed.",
	}
	input := transcriptStreamFixtureRequest{Run: &run, BatchID: run.RunID, Recovery: recovery}
	if result, err := beginTranscriptStreamingRecovery(store, stream.FrameID, input); err != nil || result["eventCount"] != 5 {
		t.Fatalf("begin recovery result=%#v err=%v", result, err)
	}
	if result, err := completeTranscriptStreamingRecovery(store, stream.FrameID, input); err != nil || result["eventCount"] != 3 {
		t.Fatalf("complete recovery result=%#v err=%v", result, err)
	}
	projected, err := repository.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, Limit: 20,
	})
	if err != nil || len(projected) != 10 {
		t.Fatalf("projected=%d err=%v", len(projected), err)
	}
	toolStates := map[string][]string{}
	for _, event := range projected {
		if event.Event.Type != "runner_checkpoint" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.ResolvedPayloadJSON, &payload); err != nil {
			t.Fatal(err)
		}
		if callID, ok := payload["toolCallId"].(string); ok {
			toolStates[callID] = append(toolStates[callID], payload["status"].(string))
		}
	}
	if got := toolStates["e2e-recovery-first:recovery-run"]; len(got) != 2 || got[0] != "running" || got[1] != "failed" {
		t.Fatalf("first operation states=%#v", got)
	}
	if got := toolStates["e2e-recovery-second:recovery-run"]; len(got) != 2 || got[0] != "running" || got[1] != "completed" {
		t.Fatalf("recovery operation states=%#v", got)
	}
	if projected[len(projected)-1].Event.Type != "runner_finished" {
		t.Fatalf("terminal=%q", projected[len(projected)-1].Event.Type)
	}
}

func TestDecodeTranscriptStreamFixtureRejectsUnsafeOrMixedInput(t *testing.T) {
	for _, input := range []string{
		`{"action":"begin-transcript-stream","frameId":"frame","transcript":{"batchId":"../escape"}}`,
		`{"action":"begin-transcript-stream","frameId":"frame","status":"processing","transcript":{"batchId":"run"}}`,
		`{"action":"append-transcript-deltas","frameId":"frame","transcript":{"batchId":"run","chunks":[]}}`,
		`{"action":"complete-streaming-parity","frameId":"frame","transcript":{"batchId":"run","copy":{"thinking":"t","search":"s","compute":"c","final":"f"},"artifactRefs":[]}}`,
		`{"action":"begin-streaming-recovery","frameId":"frame","transcript":{"batchId":"run","recovery":{"intro":"i","first":"f","recovery":"r","second":"s","final":"done"}}}`,
	} {
		if _, err := decodeRequest(strings.NewReader(input)); err == nil {
			t.Fatalf("invalid transcript fixture request accepted: %s", input)
		}
	}
}

func TestTranscriptStreamFixtureRefusesFrameWithExistingTaskIntent(t *testing.T) {
	store, repository, stream := transcriptFixtureTestStream(t)
	if err := ensureTranscriptFixtureTaskIntent(
		context.Background(), store, repository, stream, "existing-fixture-intent",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := beginTranscriptStreamFixture(store, stream.FrameID, "replacement-fixture-intent"); err == nil {
		t.Fatal("transcript fixture replaced an existing task intent")
	}
}

func TestTranscriptStreamFixtureReusesItsExactTaskIntentAfterPartialRetry(t *testing.T) {
	store, repository, stream := transcriptFixtureTestStream(t)
	for attempt := 0; attempt < 2; attempt++ {
		if err := ensureTranscriptFixtureTaskIntent(
			context.Background(), store, repository, stream, "retry-fixture-intent",
		); err != nil {
			t.Fatalf("attempt %d: %v", attempt+1, err)
		}
	}
}

func TestTranscriptStreamFixtureAdoptsOnlyOwnedScrollHistoryIntent(t *testing.T) {
	store, _, stream := transcriptFixtureTestStream(t)
	if _, err := seedScrollHistoryFixture(store, fixtureRequest{
		Action: "seed-scroll-history", FrameID: stream.FrameID, HistoryCount: 2,
	}); err != nil {
		t.Fatal(err)
	}
	response, err := beginTranscriptStreamFixture(store, stream.FrameID, "scroll-stream")
	if err != nil || response["ok"] != true {
		t.Fatalf("begin after owned scroll history response=%#v err=%v", response, err)
	}
}

func transcriptFixtureTestStream(t *testing.T) (*workspace.Store, *transcriptstore.Repository, transcriptstore.Stream) {
	t.Helper()
	store := fixtureStore(t, "processing")
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:frame", OwnerID: "owner", ExternalID: "frame", SessionID: "frame",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project", RootFrameID: "frame", FrameID: "frame", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, repository, stream
}

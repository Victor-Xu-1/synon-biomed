package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestTranscriptWebTreatsProviderTransportRecoveryAsStableIncrementalBoundary(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"status": "interrupted", "reason_code": sessionRunnerProviderTransportTemporaryReasonCode,
		"resume_detail": "provider transport ended before semantic output",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !transcriptWebIncrementalProviderBoundary(transcriptstore.ProjectedEvent{
		Event: transcriptstore.Event{Type: "runner_checkpoint"}, ResolvedPayloadJSON: payload,
	}) {
		t.Fatal("provider transport recovery forced a full projection rebuild")
	}
}

func TestTranscriptWebSyntheticProgressIsAuditOnlyAcrossFullAndIncrementalProjection(t *testing.T) {
	store, repository, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-synthetic-progress", "project-synthetic-progress", "frame-synthetic-progress")
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-synthetic-progress", OwnerID: "owner-synthetic-progress", ExternalID: "frame-synthetic-progress",
		SessionID: "frame-synthetic-progress", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-synthetic-progress", RootFrameID: "frame-synthetic-progress",
		FrameID: "frame-synthetic-progress", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	branch, err := repository.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	seedTranscriptWebIncrementalUserEvents(t, db, stream.UID, branch.ActiveBranchID, 1, "synthetic-progress-user")
	seedTranscriptWebIncrementalRunnerAttempt(t, db, stream.UID)
	seedTranscriptWebIncrementalAssistantEventBatch(
		t, db, stream.UID, branch.ActiveBranchID, 1, 2,
		func(int) (string, map[string]any) {
			return "content_delta", map[string]any{
				"text": "正在分析任务要求并确定下一步…", "synthetic_progress": true,
				"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
			}
		},
	)
	readModel := transcriptstore.NewWebReadModelRepository(db, db)
	server := &Server{workspaceStore: store, transcriptStore: repository, transcriptWebReadModel: readModel}
	if err := server.rebuildTranscriptWebReadModel(
		context.Background(), requireSingleTranscriptWebProjectionWork(t, readModel, stream.OwnerID),
	); err != nil {
		t.Fatal(err)
	}
	assertTail := func(want string, messageCount int, openAttempt bool) {
		t.Helper()
		fence := requireTranscriptWebReadModelFence(t, readModel, stream, branch.ActiveBranchID)
		_, records, found, err := readModel.GetTranscriptWebProjectionCheckpoint(
			context.Background(), stream.OwnerID, stream.UID, branch.ActiveBranchID,
		)
		if err != nil || !found || len(records) != messageCount {
			t.Fatalf("records=%#v found=%t err=%v", records, found, err)
		}
		messages, err := transcriptWebReadModelMessageMaps(records)
		if err != nil {
			t.Fatal(err)
		}
		content, ok := messages[len(messages)-1]["content"].(map[string]any)
		if !ok || webString(content["content"]) != want {
			t.Fatalf("assistant=%#v want=%q", messages[1], want)
		}
		var checkpoint transcriptWebProjectorCheckpointV1
		if err := json.Unmarshal(fence.StateProjectorStateJSON, &checkpoint); err != nil {
			t.Fatal(err)
		}
		if (len(checkpoint.Assistant.Attempts) == 1) != openAttempt {
			t.Fatalf("assistant checkpoint=%#v want open=%t", checkpoint.Assistant, openAttempt)
		}
	}
	assertTail("synthetic-progress-user-000001", 1, false)

	seedTranscriptWebIncrementalAssistantEventBatch(
		t, db, stream.UID, branch.ActiveBranchID, 1, 1,
		func(int) (string, map[string]any) {
			return "content_delta", map[string]any{
				"text": "正在执行：搜索资料…", "public_progress": true,
				"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
			}
		},
	)
	result, err := server.tryIncrementalTranscriptWebReadModel(
		context.Background(), stream, requireSingleTranscriptWebProjectionWork(t, readModel, stream.OwnerID),
	)
	if err != nil || result.Disposition != transcriptWebIncrementalAppliedReady {
		t.Fatalf("retired public progress incremental=%#v err=%v", result, err)
	}
	assertTail("synthetic-progress-user-000001", 1, false)

	seedTranscriptWebIncrementalAssistantEventBatch(
		t, db, stream.UID, branch.ActiveBranchID, 1, 1,
		func(int) (string, map[string]any) {
			return "content_delta", map[string]any{
				"text": "这是实际回复。", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
			}
		},
	)
	result, err = server.tryIncrementalTranscriptWebReadModel(
		context.Background(), stream, requireSingleTranscriptWebProjectionWork(t, readModel, stream.OwnerID),
	)
	if err != nil || result.Disposition != transcriptWebIncrementalAppliedReady {
		t.Fatalf("incremental=%#v err=%v", result, err)
	}
	assertTail("这是实际回复。", 2, true)
}

func TestTranscriptWebThinkingSegmentStaysTypedIncrementallyAndSettlesAtToolBoundary(t *testing.T) {
	store, repository, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-thinking-segment", "project-thinking-segment", "frame-thinking-segment")
	server := &Server{
		workspaceStore: store, transcriptStore: repository,
		transcriptWebReadModel: transcriptstore.NewWebReadModelRepository(db, db),
	}
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-thinking-segment", MessageUUID: "thinking-user", ClientMessageID: "thinking-user-client",
		Text: "inspect the evidence",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repository.GetFrameStreamBySession(
		context.Background(), "owner-thinking-segment", "frame-thinking-segment",
	)
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	branch, err := repository.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-thinking-segment",
		TTL: time.Hour, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	if err := server.rebuildTranscriptWebReadModel(
		context.Background(), requireSingleTranscriptWebProjectionWork(t, server.transcriptWebReadModel, stream.OwnerID),
	); err != nil {
		t.Fatal(err)
	}
	appendRunnerPayloadEvent(t, repository, claimed.Claim, "thinking-delta", "content_delta", map[string]any{
		"text": "Checking the evidence. ", "block_type": "thinking",
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})
	work := requireSingleTranscriptWebProjectionWork(t, server.transcriptWebReadModel, stream.OwnerID)
	result, err := server.tryIncrementalTranscriptWebReadModel(context.Background(), stream, work)
	if err != nil || result.Disposition != transcriptWebIncrementalAppliedReady {
		t.Fatalf("thinking incremental=%#v err=%v", result, err)
	}
	_, incrementalRecords, found, err := server.transcriptWebReadModel.GetTranscriptWebProjectionCheckpoint(
		context.Background(), stream.OwnerID, stream.UID, branch.ActiveBranchID,
	)
	incrementalMessages, decodeErr := transcriptWebReadModelMessageMaps(incrementalRecords)
	if err != nil || decodeErr != nil || !found || len(incrementalMessages) != 2 {
		t.Fatalf("incremental messages=%#v found=%t err=%v decode_err=%v", incrementalMessages, found, err, decodeErr)
	}
	thinking := incrementalMessages[1]
	thinkingContent, ok := thinking["content"].(map[string]any)
	if !ok || thinking["type"] != "thinking" || thinking["status"] != "work" ||
		thinkingContent["content"] != "Checking the evidence. " || thinkingContent["status"] != "thinking" {
		t.Fatalf("open thinking segment=%#v", thinking)
	}

	if err := server.rebuildTranscriptWebReadModel(context.Background(), work); err != nil {
		t.Fatal(err)
	}
	_, fullRecords, found, err := server.transcriptWebReadModel.GetTranscriptWebProjectionCheckpoint(
		context.Background(), stream.OwnerID, stream.UID, branch.ActiveBranchID,
	)
	fullMessages, decodeErr := transcriptWebReadModelMessageMaps(fullRecords)
	if err != nil || decodeErr != nil || !found || !reflect.DeepEqual(incrementalMessages, fullMessages) {
		t.Fatalf("open thinking incremental/full mismatch found=%t err=%v decode_err=%v\nincremental=%#v\nfull=%#v",
			found, err, decodeErr, incrementalMessages, fullMessages)
	}

	appendRunnerToolCheckpoint(t, repository, claimed.Claim, "thinking-tool-start", map[string]any{
		"status": "running", "toolPhase": "start", "toolCallId": "call-thinking-boundary", "toolName": "Read",
		"toolInput": map[string]any{"path": "evidence.txt"},
	})
	if err := server.rebuildTranscriptWebReadModel(
		context.Background(), requireSingleTranscriptWebProjectionWork(t, server.transcriptWebReadModel, stream.OwnerID),
	); err != nil {
		t.Fatal(err)
	}
	_, settledRecords, found, err := server.transcriptWebReadModel.GetTranscriptWebProjectionCheckpoint(
		context.Background(), stream.OwnerID, stream.UID, branch.ActiveBranchID,
	)
	settledMessages, decodeErr := transcriptWebReadModelMessageMaps(settledRecords)
	if err != nil || decodeErr != nil || !found || len(settledMessages) != 3 {
		t.Fatalf("settled messages=%#v found=%t err=%v decode_err=%v", settledMessages, found, err, decodeErr)
	}
	thinking = settledMessages[1]
	thinkingContent, ok = thinking["content"].(map[string]any)
	if !ok || thinking["type"] != "thinking" || thinking["status"] != "finish" || thinkingContent["status"] != "done" {
		t.Fatalf("settled thinking segment=%#v", thinking)
	}
}

func TestTranscriptWebProviderContinuationMergesAcrossSegmentsAndRestartsByteEqual(t *testing.T) {
	store, repository, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-provider-continuation", "project-provider-continuation", "frame-provider-continuation")
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-provider-continuation", OwnerID: "owner-provider-continuation", ExternalID: "frame-provider-continuation",
		SessionID: "frame-provider-continuation", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-provider-continuation", RootFrameID: "frame-provider-continuation",
		FrameID: "frame-provider-continuation", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	branch, err := repository.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	seedTranscriptWebIncrementalUserEvents(t, db, stream.UID, branch.ActiveBranchID, 1, "provider-continuation-user")
	seedTranscriptWebIncrementalRunnerAttempt(t, db, stream.UID)
	readModel := transcriptstore.NewWebReadModelRepository(db, db)
	server := &Server{workspaceStore: store, transcriptStore: repository, transcriptWebReadModel: readModel}
	if err := server.rebuildTranscriptWebReadModel(
		context.Background(), requireSingleTranscriptWebProjectionWork(t, readModel, stream.OwnerID),
	); err != nil {
		t.Fatal(err)
	}

	root := seedTranscriptWebIncrementalAssistantEventBatch(
		t, db, stream.UID, branch.ActiveBranchID, 1, 1,
		func(int) (string, map[string]any) {
			return "content_delta", map[string]any{
				"text": "partial ", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
			}
		},
	)
	seedTranscriptWebProviderContinuationBoundary(
		t, db, stream, branch, root, root, 1, 1, "partial ", "provider_stream_interrupted",
	)
	result, err := server.tryIncrementalTranscriptWebReadModel(
		context.Background(), stream, requireSingleTranscriptWebProjectionWork(t, readModel, stream.OwnerID),
	)
	if err != nil || result.Disposition != transcriptWebIncrementalAppliedReady {
		t.Fatalf("first continuation increment=%#v err=%v", result, err)
	}

	// Restart every repository owner before appending the next execution unit.
	repository = transcriptstore.NewRepository(db)
	readModel = transcriptstore.NewWebReadModelRepository(db, db)
	server = &Server{workspaceStore: store, transcriptStore: repository, transcriptWebReadModel: readModel}
	middle := seedTranscriptWebIncrementalAssistantEventBatch(
		t, db, stream.UID, branch.ActiveBranchID, 1, 1,
		func(int) (string, map[string]any) {
			return "content_delta", map[string]any{
				"text": "middle ", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
			}
		},
	)
	seedTranscriptWebProviderContinuationBoundary(
		t, db, stream, branch, root, middle, 1, 2, "partial middle ", "provider_stream_no_progress",
	)
	result, err = server.tryIncrementalTranscriptWebReadModel(
		context.Background(), stream, requireSingleTranscriptWebProjectionWork(t, readModel, stream.OwnerID),
	)
	if err != nil || result.Disposition != transcriptWebIncrementalAppliedReady {
		t.Fatalf("second continuation increment=%#v err=%v", result, err)
	}

	repository = transcriptstore.NewRepository(db)
	readModel = transcriptstore.NewWebReadModelRepository(db, db)
	server = &Server{workspaceStore: store, transcriptStore: repository, transcriptWebReadModel: readModel}
	seedTranscriptWebIncrementalAssistantEventBatch(
		t, db, stream.UID, branch.ActiveBranchID, 1, 1,
		func(int) (string, map[string]any) {
			return "content_delta", map[string]any{
				"text": "done", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
			}
		},
	)
	finalWork := requireSingleTranscriptWebProjectionWork(t, readModel, stream.OwnerID)
	result, err = server.tryIncrementalTranscriptWebReadModel(context.Background(), stream, finalWork)
	if err != nil || result.Disposition != transcriptWebIncrementalAppliedReady {
		t.Fatalf("final continuation increment=%#v err=%v", result, err)
	}
	incrementalState, incrementalRecords, found, err := readModel.GetTranscriptWebProjectionCheckpoint(
		context.Background(), stream.OwnerID, stream.UID, branch.ActiveBranchID,
	)
	if err != nil || !found {
		t.Fatalf("incremental checkpoint found=%t err=%v", found, err)
	}
	incrementalMessages, err := transcriptWebReadModelMessageMaps(incrementalRecords)
	if err != nil {
		t.Fatal(err)
	}
	if len(incrementalMessages) != 2 {
		t.Fatalf("continued projection messages=%d want=2: %#v", len(incrementalMessages), incrementalMessages)
	}
	content, ok := incrementalMessages[1]["content"].(map[string]any)
	if !ok || webString(content["content"]) != "partial middle done" ||
		incrementalMessages[1]["id"] != "assistant-frame-provider-continuation-1" {
		t.Fatalf("continued assistant=%#v", incrementalMessages[1])
	}

	if err := server.rebuildTranscriptWebReadModel(context.Background(), finalWork); err != nil {
		t.Fatal(err)
	}
	fullState, fullRecords, found, err := readModel.GetTranscriptWebProjectionCheckpoint(
		context.Background(), stream.OwnerID, stream.UID, branch.ActiveBranchID,
	)
	if err != nil || !found || incrementalState.MessageCount != fullState.MessageCount ||
		incrementalState.VisibleMessageCount != fullState.VisibleMessageCount ||
		incrementalState.MessageArtifactReferenceCount != fullState.MessageArtifactReferenceCount ||
		!reflect.DeepEqual(incrementalRecords, fullRecords) {
		t.Fatalf("continued incremental/full mismatch found=%t err=%v", found, err)
	}
}

func seedTranscriptWebIncrementalRunnerAttempt(t *testing.T, db *sql.DB, streamUID string) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	statement, err := tx.Prepare(`INSERT INTO transcript_runner_attempts(
		stream_uid,attempt,runner_id,claim_token_sha256,claimed_input_revision,resume_source,
		resume_checkpoint_sequence,status,phase,phase_sequence,last_checkpoint_sequence,claimed_at,expires_at
	) VALUES(?,?,?,?,0,'fresh',0,'running','executing',1,0,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer statement.Close()
	claimedAt := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	digest := make([]byte, sha256.Size)
	digest[0] = 1
	if _, err := statement.Exec(
		streamUID, 1, "provider-continuation-runner", digest,
		claimedAt, claimedAt.Add(time.Hour),
	); err != nil {
		t.Fatalf("insert runner attempt: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func seedTranscriptWebProviderContinuationBoundary(
	t *testing.T,
	db *sql.DB,
	stream transcriptstore.Stream,
	branch transcriptstore.BranchState,
	root, accepted transcriptstore.Event,
	previousAttempt, segmentIndex int64,
	acceptedText, reason string,
) {
	t.Helper()
	digest := sha256.Sum256([]byte(acceptedText))
	seedTranscriptWebIncrementalAssistantEventBatch(
		t, db, stream.UID, branch.ActiveBranchID, previousAttempt, 1,
		func(int) (string, map[string]any) {
			return "runner_checkpoint", map[string]any{
				"status": "running",
				"detail": "provider generation reached a recoverable content-only segment boundary",
				"provider_continuation": providerContinuationPayloadV1(sessionRunnerProviderContinuationV1{
					ContractVersion: sessionRunnerProviderContinuationContractVersion,
					StreamUID:       stream.UID, OwnerID: stream.OwnerID,
					BranchID: branch.ActiveBranchID, BranchGeneration: branch.Generation,
					RootAttempt: 1, RootSegmentOrdinal: 1,
					RootStartedEventID: root.EventID, RootStartedPublicationSequence: root.PublicationSeq,
					PreviousAttempt: previousAttempt, CurrentSegmentOrdinal: 1, SegmentIndex: segmentIndex,
					AcceptedThroughEventID:             accepted.EventID,
					AcceptedThroughPublicationSequence: accepted.PublicationSeq,
					AcceptedSemanticBytes:              int64(len(acceptedText)), AcceptedSHA256: fmt.Sprintf("%x", digest),
				}),
			}
		},
	)
	seedTranscriptWebIncrementalAssistantEventBatch(
		t, db, stream.UID, branch.ActiveBranchID, previousAttempt, 1,
		func(int) (string, map[string]any) {
			return "runner_checkpoint", map[string]any{"status": "interrupted", "reason_code": reason}
		},
	)
}

func TestTranscriptWebIncrementalAppendDoesNotReadOrRewriteTenThousandMessagePrefix(t *testing.T) {
	store, repository, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-incremental", "project-incremental", "frame-incremental")
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-incremental", OwnerID: "owner-incremental", ExternalID: "frame-incremental",
		SessionID: "frame-incremental", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-incremental", RootFrameID: "frame-incremental", FrameID: "frame-incremental", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	branch, err := repository.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	seedTranscriptWebIncrementalUserEvents(t, db, stream.UID, branch.ActiveBranchID, 10_000, "prefix")

	readModel := transcriptstore.NewWebReadModelRepository(db, db)
	server := &Server{workspaceStore: store, transcriptStore: repository, transcriptWebReadModel: readModel}
	work := requireSingleTranscriptWebProjectionWork(t, readModel, stream.OwnerID)
	if err := server.rebuildTranscriptWebReadModel(context.Background(), work); err != nil {
		t.Fatal(err)
	}
	ready := requireTranscriptWebReadModelFence(t, readModel, stream, branch.ActiveBranchID)
	if ready.StateMessageCount != 10_000 || !validTranscriptWebSourceEventChain(ready.StateSourceChainSHA256) {
		t.Fatalf("initial fence=%#v", ready)
	}

	if _, err := db.Exec(`CREATE TEMP TABLE transcript_web_prefix_write_audit(kind TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TEMP TRIGGER transcript_web_prefix_update_audit
		AFTER UPDATE ON transcript_web_messages WHEN OLD.ordinal<=10000
		BEGIN INSERT INTO transcript_web_prefix_write_audit(kind) VALUES('update'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TEMP TRIGGER transcript_web_prefix_delete_audit
		AFTER DELETE ON transcript_web_messages WHEN OLD.ordinal<=10000
		BEGIN INSERT INTO transcript_web_prefix_write_audit(kind) VALUES('delete'); END`); err != nil {
		t.Fatal(err)
	}
	appended, _, created, err := repository.AppendFrameUserEvent(
		context.Background(), transcriptstore.AppendFrameUserEventInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "tail-client",
			FrameEventID: "tail-frame-event", MessageUUID: "tail-message", Text: "tail",
			Destinations: []string{transcriptWebDestination},
		},
	)
	if err != nil || !created || appended.PublicationSeq != 10_001 {
		t.Fatalf("appended=%#v created=%t err=%v", appended, created, err)
	}
	work = requireSingleTranscriptWebProjectionWork(t, readModel, stream.OwnerID)
	result, err := server.tryIncrementalTranscriptWebReadModel(context.Background(), stream, work)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != transcriptWebIncrementalAppliedReady || result.EventsRead != 1 ||
		result.MessagesWritten != 1 || result.ThroughPublicationSequence != appended.PublicationSeq {
		t.Fatalf("incremental result=%#v", result)
	}
	var prefixWrites int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_web_prefix_write_audit`).Scan(&prefixWrites); err != nil {
		t.Fatal(err)
	}
	if prefixWrites != 0 {
		t.Fatalf("incremental projection rewrote %d prefix rows", prefixWrites)
	}

	incrementalState, incrementalMessages, found, err := readModel.GetTranscriptWebProjectionCheckpoint(
		context.Background(), stream.OwnerID, stream.UID, branch.ActiveBranchID,
	)
	if err != nil || !found {
		t.Fatalf("incremental checkpoint found=%t err=%v", found, err)
	}
	if len(incrementalMessages) != 10_001 || incrementalMessages[10_000].MessageID != "tail-message" {
		t.Fatalf("incremental tail count=%d tail=%#v", len(incrementalMessages), incrementalMessages[len(incrementalMessages)-1])
	}
	if _, err := db.Exec(`DROP TRIGGER transcript_web_prefix_update_audit`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TRIGGER transcript_web_prefix_delete_audit`); err != nil {
		t.Fatal(err)
	}
	if err := server.rebuildTranscriptWebReadModel(context.Background(), work); err != nil {
		t.Fatal(err)
	}
	fullState, fullMessages, found, err := readModel.GetTranscriptWebProjectionCheckpoint(
		context.Background(), stream.OwnerID, stream.UID, branch.ActiveBranchID,
	)
	if err != nil || !found {
		t.Fatalf("full checkpoint found=%t err=%v", found, err)
	}
	if incrementalState.SourceChainSHA256 != fullState.SourceChainSHA256 ||
		incrementalState.MessageCount != fullState.MessageCount ||
		incrementalState.VisibleMessageCount != fullState.VisibleMessageCount ||
		incrementalState.MessageArtifactReferenceCount != fullState.MessageArtifactReferenceCount ||
		!reflect.DeepEqual(incrementalMessages, fullMessages) {
		t.Fatal("incremental projection differs from an immediate full rebuild")
	}
}

func TestTranscriptWebIncrementalBatchResumesFromDurableStateAfterRestart(t *testing.T) {
	store, repository, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-resume", "project-resume", "frame-resume")
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-incremental-resume", OwnerID: "owner-resume", ExternalID: "frame-resume",
		SessionID: "frame-resume", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-resume", RootFrameID: "frame-resume", FrameID: "frame-resume", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	branch, err := repository.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	seedTranscriptWebIncrementalUserEvents(t, db, stream.UID, branch.ActiveBranchID, 1, "initial")
	readModel := transcriptstore.NewWebReadModelRepository(db, db)
	server := &Server{workspaceStore: store, transcriptStore: repository, transcriptWebReadModel: readModel}
	if err := server.rebuildTranscriptWebReadModel(
		context.Background(), requireSingleTranscriptWebProjectionWork(t, readModel, stream.OwnerID),
	); err != nil {
		t.Fatal(err)
	}
	seedTranscriptWebIncrementalUserEvents(t, db, stream.UID, branch.ActiveBranchID, 300, "resume")
	work := requireSingleTranscriptWebProjectionWork(t, readModel, stream.OwnerID)
	first, err := server.tryIncrementalTranscriptWebReadModel(context.Background(), stream, work)
	if err != nil {
		t.Fatal(err)
	}
	if first.Disposition != transcriptWebIncrementalAppliedBuilding ||
		first.EventsRead != transcriptWebIncrementalBatch || first.MessagesWritten != transcriptWebIncrementalBatch {
		t.Fatalf("first batch=%#v", first)
	}
	building := requireTranscriptWebReadModelFence(t, readModel, stream, branch.ActiveBranchID)
	if building.StateStatus != "building" || building.StateMessageCount != 1+transcriptWebIncrementalBatch {
		t.Fatalf("building fence=%#v", building)
	}

	// Recreate the repository and server to prove that no in-memory reducer
	// state is required to resume the bounded projection.
	restartedReadModel := transcriptstore.NewWebReadModelRepository(db, db)
	restarted := &Server{workspaceStore: store, transcriptStore: repository, transcriptWebReadModel: restartedReadModel}
	work = requireSingleTranscriptWebProjectionWork(t, restartedReadModel, stream.OwnerID)
	second, err := restarted.tryIncrementalTranscriptWebReadModel(context.Background(), stream, work)
	if err != nil {
		t.Fatal(err)
	}
	if second.Disposition != transcriptWebIncrementalAppliedReady || second.EventsRead != 44 || second.MessagesWritten != 44 {
		t.Fatalf("resumed batch=%#v", second)
	}
	ready := requireTranscriptWebReadModelFence(t, restartedReadModel, stream, branch.ActiveBranchID)
	if ready.StateStatus != "ready" || ready.StateMessageCount != 301 ||
		ready.StateThroughPublicationSequence != ready.ThroughPublicationSequence {
		t.Fatalf("ready fence=%#v", ready)
	}
	incrementalState, incrementalMessages, found, err := restartedReadModel.GetTranscriptWebProjectionCheckpoint(
		context.Background(), stream.OwnerID, stream.UID, branch.ActiveBranchID,
	)
	if err != nil || !found {
		t.Fatalf("incremental checkpoint found=%t err=%v", found, err)
	}
	if err := restarted.rebuildTranscriptWebReadModel(context.Background(), work); err != nil {
		t.Fatal(err)
	}
	fullState, fullMessages, found, err := restartedReadModel.GetTranscriptWebProjectionCheckpoint(
		context.Background(), stream.OwnerID, stream.UID, branch.ActiveBranchID,
	)
	if err != nil || !found {
		t.Fatalf("full checkpoint found=%t err=%v", found, err)
	}
	if incrementalState.SourceChainSHA256 != fullState.SourceChainSHA256 ||
		!reflect.DeepEqual(incrementalMessages, fullMessages) {
		t.Fatal("one-shot full projection differs from the restarted multi-batch projection")
	}
}

func TestTranscriptWebIncrementalUnsupportedReducersFailClosedToFullProjection(t *testing.T) {
	attempt := int64(7)
	tests := []struct {
		name      string
		eventType string
		attempt   *int64
		want      transcriptWebIncrementalDisposition
	}{
		{name: "tool call start", eventType: "runner_checkpoint", attempt: &attempt, want: transcriptWebIncrementalFallbackTool},
		{name: "AskUser prompt", eventType: transcriptstore.AskUserPromptEventType, attempt: &attempt, want: transcriptWebIncrementalFallbackAskUser},
		{name: "AskUser pending or terminal update", eventType: transcriptstore.AskUserResultEventType, attempt: &attempt, want: transcriptWebIncrementalFallbackAskUser},
		{name: "user input response", eventType: "user_input_response", want: transcriptWebIncrementalFallbackInput},
		{name: "imported assistant history", eventType: "history_assistant_message", want: transcriptWebIncrementalFallbackHistory},
		{name: "unknown event", eventType: "unknown", want: transcriptWebIncrementalFallbackUnsupported},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := classifyTranscriptWebIncrementalEvent(transcriptstore.ProjectedEvent{Event: transcriptstore.Event{
				Type: test.eventType, RunnerAttempt: test.attempt,
			}})
			if got != test.want {
				t.Fatalf("classification=%q want=%q", got, test.want)
			}
		})
	}
}

func TestTranscriptWebIncrementalSameDurableSegmentContinuesAfterToolBoundary(t *testing.T) {
	store, repository, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-same-segment", "project-same-segment", "frame-same-segment")
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-same-segment", OwnerID: "owner-same-segment", ExternalID: "frame-same-segment",
		SessionID: "frame-same-segment", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-same-segment", RootFrameID: "frame-same-segment", FrameID: "frame-same-segment", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	branch, err := repository.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	seedTranscriptWebIncrementalUserEvents(t, db, stream.UID, branch.ActiveBranchID, 1, "same-segment-user")
	claimed, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-same-segment",
		TTL: time.Hour, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	seedTranscriptWebIncrementalAssistantEventBatch(
		t, db, stream.UID, branch.ActiveBranchID, 1, 1,
		func(int) (string, map[string]any) {
			return "content_delta", map[string]any{
				"text": "before tool ", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
			}
		},
	)
	seedTranscriptWebIncrementalAssistantEventBatch(
		t, db, stream.UID, branch.ActiveBranchID, 1, 1,
		func(int) (string, map[string]any) {
			return "runner_checkpoint", map[string]any{
				"status": "completed", "toolPhase": "completed",
				"toolCallId": "same-segment-tool", "toolName": "web_search",
				"message": "tool web_search completed", "toolInput": map[string]any{},
				"toolResult": map[string]any{"ok": true},
			}
		},
	)

	readModel := transcriptstore.NewWebReadModelRepository(db, db)
	server := &Server{workspaceStore: store, transcriptStore: repository, transcriptWebReadModel: readModel}
	if err := server.rebuildTranscriptWebReadModel(
		context.Background(), requireSingleTranscriptWebProjectionWork(t, readModel, stream.OwnerID),
	); err != nil {
		t.Fatal(err)
	}
	fence := requireTranscriptWebReadModelFence(t, readModel, stream, branch.ActiveBranchID)
	var checkpoint transcriptWebProjectorCheckpointV1
	if err := json.Unmarshal(fence.StateProjectorStateJSON, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if len(checkpoint.Assistant.Attempts) != 1 || !checkpoint.Assistant.Attempts[0].SplitPending ||
		checkpoint.Assistant.Attempts[0].CurrentIdentity != "assistant-frame-same-segment-1" {
		t.Fatalf("tool-boundary checkpoint=%#v", checkpoint.Assistant)
	}

	seedTranscriptWebIncrementalAssistantEventBatch(
		t, db, stream.UID, branch.ActiveBranchID, 1, 1,
		func(int) (string, map[string]any) {
			return "content_delta", map[string]any{
				"text": "after tool", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
			}
		},
	)
	work := requireSingleTranscriptWebProjectionWork(t, readModel, stream.OwnerID)
	result, err := server.tryIncrementalTranscriptWebReadModel(context.Background(), stream, work)
	if err != nil || result.Disposition != transcriptWebIncrementalAppliedReady {
		t.Fatalf("same-segment incremental=%#v err=%v", result, err)
	}
	incrementalState, incrementalRecords, found, err := readModel.GetTranscriptWebProjectionCheckpoint(
		context.Background(), stream.OwnerID, stream.UID, branch.ActiveBranchID,
	)
	if err != nil || !found {
		t.Fatalf("incremental checkpoint found=%t err=%v", found, err)
	}
	incrementalMessages, err := transcriptWebReadModelMessageMaps(incrementalRecords)
	if err != nil || len(incrementalMessages) != 3 {
		t.Fatalf("incremental messages=%#v err=%v", incrementalMessages, err)
	}
	content, ok := incrementalMessages[1]["content"].(map[string]any)
	if !ok || webString(content["content"]) != "before tool after tool" ||
		incrementalMessages[1]["id"] != "assistant-frame-same-segment-1" {
		t.Fatalf("continued assistant=%#v", incrementalMessages[1])
	}

	if err := server.rebuildTranscriptWebReadModel(context.Background(), work); err != nil {
		t.Fatal(err)
	}
	fullState, fullRecords, found, err := readModel.GetTranscriptWebProjectionCheckpoint(
		context.Background(), stream.OwnerID, stream.UID, branch.ActiveBranchID,
	)
	if err != nil || !found || incrementalState.SourceChainSHA256 != fullState.SourceChainSHA256 ||
		!reflect.DeepEqual(incrementalRecords, fullRecords) {
		t.Fatalf("same-segment incremental/full mismatch found=%t err=%v\nincremental=%#v\nfull=%#v", found, err, incrementalRecords, fullRecords)
	}

	terminalPayload, err := json.Marshal(map[string]any{
		"status": "completed", "detail": "same durable segment completed",
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repository.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "same-segment-terminal", Status: "completed", PayloadJSON: terminalPayload,
	}); err != nil || !created {
		t.Fatalf("finish created=%t err=%v", created, err)
	}
	terminalWork := requireSingleTranscriptWebProjectionWork(t, readModel, stream.OwnerID)
	terminalResult, err := server.tryIncrementalTranscriptWebReadModel(context.Background(), stream, terminalWork)
	if err != nil || terminalResult.Disposition != transcriptWebIncrementalAppliedReady {
		t.Fatalf("same-segment terminal incremental=%#v err=%v", terminalResult, err)
	}
	terminalState, terminalRecords, found, err := readModel.GetTranscriptWebProjectionCheckpoint(
		context.Background(), stream.OwnerID, stream.UID, branch.ActiveBranchID,
	)
	if err != nil || !found {
		t.Fatalf("terminal checkpoint found=%t err=%v", found, err)
	}
	terminalMessages, err := transcriptWebReadModelMessageMaps(terminalRecords)
	if err != nil || len(terminalMessages) != 3 || terminalMessages[1]["status"] != "finish" ||
		terminalMessages[1]["terminal_status"] != "completed" {
		t.Fatalf("terminal messages=%#v err=%v", terminalMessages, err)
	}
	if err := server.rebuildTranscriptWebReadModel(context.Background(), terminalWork); err != nil {
		t.Fatal(err)
	}
	fullTerminalState, fullTerminalRecords, found, err := readModel.GetTranscriptWebProjectionCheckpoint(
		context.Background(), stream.OwnerID, stream.UID, branch.ActiveBranchID,
	)
	if err != nil || !found || terminalState.SourceChainSHA256 != fullTerminalState.SourceChainSHA256 ||
		!reflect.DeepEqual(terminalRecords, fullTerminalRecords) {
		t.Fatalf("same-segment terminal incremental/full mismatch found=%t err=%v", found, err)
	}
}

func TestTranscriptWebIncrementalAssistantDeltaThenResetInSameBatchMatchesFullProjection(t *testing.T) {
	store, repository, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-same-batch-reset", "project-same-batch-reset", "frame-same-batch-reset")
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-same-batch-reset", OwnerID: "owner-same-batch-reset", ExternalID: "frame-same-batch-reset",
		SessionID: "frame-same-batch-reset", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-same-batch-reset", RootFrameID: "frame-same-batch-reset", FrameID: "frame-same-batch-reset", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	branch, err := repository.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	seedTranscriptWebIncrementalUserEvents(t, db, stream.UID, branch.ActiveBranchID, 1, "same-batch-user")
	claim, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-same-batch-reset",
		TTL: time.Hour, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	readModel := transcriptstore.NewWebReadModelRepository(db, db)
	server := &Server{workspaceStore: store, transcriptStore: repository, transcriptWebReadModel: readModel}
	if err := server.rebuildTranscriptWebReadModel(
		context.Background(), requireSingleTranscriptWebProjectionWork(t, readModel, stream.OwnerID),
	); err != nil {
		t.Fatal(err)
	}
	appendRunnerPayloadEvent(t, repository, claim.Claim, "same-batch-candidate", "content_delta", map[string]any{
		"text":              "discarded candidate",
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})
	resetEvent := seedTranscriptWebIncrementalAssistantReset(
		t, db, stream.UID, branch.ActiveBranchID, claim.Claim.Attempt, "replacement answer", 2,
	)
	work := requireSingleTranscriptWebProjectionWork(t, readModel, stream.OwnerID)
	result, err := server.tryIncrementalTranscriptWebReadModel(context.Background(), stream, work)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != transcriptWebIncrementalAppliedReady || result.EventsRead != 2 ||
		result.MessagesWritten != 1 || result.ThroughPublicationSequence != resetEvent.PublicationSeq {
		t.Fatalf("same-batch result=%#v", result)
	}
	incrementalState, incrementalMessages, found, err := readModel.GetTranscriptWebProjectionCheckpoint(
		context.Background(), stream.OwnerID, stream.UID, branch.ActiveBranchID,
	)
	if err != nil || !found || len(incrementalMessages) != 2 ||
		!strings.Contains(string(incrementalMessages[1].MessageJSON), "replacement answer") ||
		strings.Contains(string(incrementalMessages[1].MessageJSON), "discarded candidate") {
		t.Fatalf("incremental state=%#v messages=%#v found=%t err=%v", incrementalState, incrementalMessages, found, err)
	}
	if err := server.rebuildTranscriptWebReadModel(context.Background(), work); err != nil {
		t.Fatal(err)
	}
	fullState, fullMessages, found, err := readModel.GetTranscriptWebProjectionCheckpoint(
		context.Background(), stream.OwnerID, stream.UID, branch.ActiveBranchID,
	)
	if err != nil || !found || incrementalState.SourceChainSHA256 != fullState.SourceChainSHA256 ||
		!reflect.DeepEqual(incrementalMessages, fullMessages) {
		t.Fatalf("same-batch incremental/full mismatch\nincremental=%#v\nfull=%#v\nerr=%v", incrementalMessages, fullMessages, err)
	}
}

func TestTranscriptWebIncrementalAssistantMessageWithoutTerminalRemainsWorkAndMatchesFullProjection(t *testing.T) {
	store, repository, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-open-assistant", "project-open-assistant", "frame-open-assistant")
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-open-assistant", OwnerID: "owner-open-assistant", ExternalID: "frame-open-assistant",
		SessionID: "frame-open-assistant", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-open-assistant", RootFrameID: "frame-open-assistant",
		FrameID: "frame-open-assistant", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	branch, err := repository.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	seedTranscriptWebIncrementalUserEvents(t, db, stream.UID, branch.ActiveBranchID, 1, "open-assistant-user")
	claimed, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-open-assistant",
		TTL: time.Hour, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	readModel := transcriptstore.NewWebReadModelRepository(db, db)
	server := &Server{workspaceStore: store, transcriptStore: repository, transcriptWebReadModel: readModel}
	if err := server.rebuildTranscriptWebReadModel(
		context.Background(), requireSingleTranscriptWebProjectionWork(t, readModel, stream.OwnerID),
	); err != nil {
		t.Fatal(err)
	}
	appendRunnerPayloadEvent(t, repository, claimed.Claim, "open-assistant-candidate", "assistant_message", map[string]any{
		"text":              "completed_with_limitations",
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})
	work := requireSingleTranscriptWebProjectionWork(t, readModel, stream.OwnerID)
	result, err := server.tryIncrementalTranscriptWebReadModel(context.Background(), stream, work)
	if err != nil || result.Disposition != transcriptWebIncrementalAppliedReady {
		t.Fatalf("incremental=%#v err=%v", result, err)
	}
	_, incrementalRecords, found, err := readModel.GetTranscriptWebProjectionCheckpoint(
		context.Background(), stream.OwnerID, stream.UID, branch.ActiveBranchID,
	)
	if err != nil || !found {
		t.Fatalf("found=%t err=%v", found, err)
	}
	incrementalMessages, err := transcriptWebReadModelMessageMaps(incrementalRecords)
	if err != nil || len(incrementalMessages) != 2 {
		t.Fatalf("messages=%#v err=%v", incrementalMessages, err)
	}
	assistant := incrementalMessages[1]
	content, ok := assistant["content"].(map[string]any)
	if !ok || assistant["status"] != "work" || assistant["terminal_status"] != nil ||
		webString(content["content"]) != "completed_with_limitations" {
		t.Fatalf("open assistant=%#v", assistant)
	}
	if err := server.rebuildTranscriptWebReadModel(context.Background(), work); err != nil {
		t.Fatal(err)
	}
	_, fullRecords, found, err := readModel.GetTranscriptWebProjectionCheckpoint(
		context.Background(), stream.OwnerID, stream.UID, branch.ActiveBranchID,
	)
	fullMessages, decodeErr := transcriptWebReadModelMessageMaps(fullRecords)
	if err != nil || decodeErr != nil || !found || !reflect.DeepEqual(incrementalMessages, fullMessages) {
		t.Fatalf("incremental/full mismatch found=%t err=%v decode_err=%v\nincremental=%#v\nfull=%#v", found, err, decodeErr, incrementalMessages, fullMessages)
	}
	payload, err := json.Marshal(map[string]any{"status": "failed", "detail": "provider failed"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repository.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "open-assistant-terminal", Status: "failed",
		PayloadJSON: payload,
	}); err != nil || !created {
		t.Fatalf("finish created=%t err=%v", created, err)
	}
	result, err = server.tryIncrementalTranscriptWebReadModel(
		context.Background(), stream, requireSingleTranscriptWebProjectionWork(t, readModel, stream.OwnerID),
	)
	if err != nil || result.Disposition != transcriptWebIncrementalAppliedReady {
		t.Fatalf("terminal incremental=%#v err=%v", result, err)
	}
	_, terminalRecords, found, err := readModel.GetTranscriptWebProjectionCheckpoint(
		context.Background(), stream.OwnerID, stream.UID, branch.ActiveBranchID,
	)
	if err != nil || !found {
		t.Fatalf("terminal found=%t err=%v", found, err)
	}
	terminalMessages, err := transcriptWebReadModelMessageMaps(terminalRecords)
	if err != nil || len(terminalMessages) != 2 || terminalMessages[1]["status"] != "error" ||
		terminalMessages[1]["terminal_status"] != "failed" {
		t.Fatalf("terminal messages=%#v err=%v", terminalMessages, err)
	}
	terminalContent, _ := terminalMessages[1]["content"].(map[string]any)
	if detail := webString(terminalContent["content"]); detail == "provider failed" || strings.Contains(detail, "provider failed") {
		t.Fatalf("terminal history leaked raw failure detail: %#v", terminalMessages[1])
	}

	resume, err := store.ResumeCompatibilityFrameConversation(
		stream.RootFrameID, workspace.ResumeCompatibilityFrameInput{},
	)
	if err != nil || resume.Event == nil {
		t.Fatalf("resume=%#v err=%v", resume, err)
	}
	dispatch, dispatchClaimed, err := store.ClaimNextCompatibilityFrameResumeDispatch(
		"open-assistant-resume-dispatch", time.Minute,
	)
	if err != nil || !dispatchClaimed || dispatch.ResumeEvent.ID != resume.Event.ID {
		t.Fatalf("dispatch=%#v claimed=%t err=%v", dispatch, dispatchClaimed, err)
	}
	resumed, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "frame-resume:" + dispatch.ResumeEvent.ID,
		TTL: time.Hour, ResumeSource: transcriptstore.ResumeSourceUserInput,
	})
	if err != nil || !resumed.Claimed || resumed.Claim.ClaimedInputRevision != claimed.Claim.ClaimedInputRevision {
		t.Fatalf("resumed=%#v err=%v", resumed, err)
	}
	appendRunnerPayloadEvent(t, repository, resumed.Claim, "open-assistant-resumed-content", "assistant_message", map[string]any{
		"text":              "recovered answer",
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})
	completedPayload, err := json.Marshal(map[string]any{
		"status": "completed", "detail": "recovered answer",
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repository.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: resumed.Claim, ClientMessageID: "open-assistant-resumed-terminal", Status: "completed",
		PayloadJSON: completedPayload,
	}); err != nil || !created {
		t.Fatalf("resumed finish created=%t err=%v", created, err)
	}
	resumedWork := requireSingleTranscriptWebProjectionWork(t, readModel, stream.OwnerID)
	resumedResult, err := server.tryIncrementalTranscriptWebReadModel(context.Background(), stream, resumedWork)
	if err != nil || resumedResult.Disposition != transcriptWebIncrementalFallbackAssistant {
		t.Fatalf("resumed incremental=%#v err=%v", resumedResult, err)
	}
	if err := server.rebuildTranscriptWebReadModel(context.Background(), resumedWork); err != nil {
		t.Fatal(err)
	}
	_, resumedRecords, found, err := readModel.GetTranscriptWebProjectionCheckpoint(
		context.Background(), stream.OwnerID, stream.UID, branch.ActiveBranchID,
	)
	resumedMessages, decodeErr := transcriptWebReadModelMessageMaps(resumedRecords)
	if err != nil || decodeErr != nil || !found {
		t.Fatalf("resumed projection found=%t err=%v decode_err=%v", found, err, decodeErr)
	}
	for _, message := range resumedMessages {
		if message["status"] == "error" {
			t.Fatalf("resumed projection retained failed alert: %#v", resumedMessages)
		}
	}
	if err := server.rebuildTranscriptWebReadModel(context.Background(), resumedWork); err != nil {
		t.Fatal(err)
	}
	_, refreshedRecords, found, err := readModel.GetTranscriptWebProjectionCheckpoint(
		context.Background(), stream.OwnerID, stream.UID, branch.ActiveBranchID,
	)
	refreshedMessages, decodeErr := transcriptWebReadModelMessageMaps(refreshedRecords)
	if err != nil || decodeErr != nil || !found || !reflect.DeepEqual(resumedMessages, refreshedMessages) {
		t.Fatalf("resumed incremental/full mismatch found=%t err=%v decode_err=%v\nincremental=%#v\nfull=%#v",
			found, err, decodeErr, resumedMessages, refreshedMessages)
	}
	for _, message := range refreshedMessages {
		if message["status"] == "error" {
			t.Fatalf("refreshed projection restored failed alert: %#v", refreshedMessages)
		}
	}
}

func TestTranscriptWebIncrementalAssistantTenThousandDeltasResetTerminalMatchesFullAfterRestart(t *testing.T) {
	store, repository, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-assistant-incremental", "project-assistant-incremental", "frame-assistant-incremental")
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-assistant-incremental", OwnerID: "owner-assistant-incremental",
		ExternalID: "frame-assistant-incremental", SessionID: "frame-assistant-incremental",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project-assistant-incremental",
		RootFrameID: "frame-assistant-incremental", FrameID: "frame-assistant-incremental", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	branch, err := repository.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	seedTranscriptWebIncrementalUserEvents(t, db, stream.UID, branch.ActiveBranchID, 1, "assistant-user")
	claim, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-assistant-incremental",
		TTL: time.Hour, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	readModel := transcriptstore.NewWebReadModelRepository(db, db)
	server := &Server{workspaceStore: store, transcriptStore: repository, transcriptWebReadModel: readModel}
	if err := server.rebuildTranscriptWebReadModel(
		context.Background(), requireSingleTranscriptWebProjectionWork(t, readModel, stream.OwnerID),
	); err != nil {
		t.Fatal(err)
	}

	seedTranscriptWebIncrementalAssistantEvents(
		t, db, stream.UID, branch.ActiveBranchID, claim.Claim.Attempt, 10_000,
	)
	resetEvent := seedTranscriptWebIncrementalAssistantReset(
		t, db, stream.UID, branch.ActiveBranchID, claim.Claim.Attempt,
		"corrected after reset", 2,
	)
	cycles := 0
	for {
		work := requireSingleTranscriptWebProjectionWork(t, readModel, stream.OwnerID)
		result, err := server.tryIncrementalTranscriptWebReadModel(context.Background(), stream, work)
		if err != nil {
			t.Fatal(err)
		}
		cycles++
		if result.EventsRead <= 0 || result.EventsRead > transcriptWebIncrementalBatch ||
			(result.Disposition != transcriptWebIncrementalAppliedBuilding && result.Disposition != transcriptWebIncrementalAppliedReady) {
			t.Fatalf("cycle %d result=%#v", cycles, result)
		}
		if result.Disposition == transcriptWebIncrementalAppliedReady {
			if result.ThroughPublicationSequence != resetEvent.PublicationSeq {
				t.Fatalf("reset through=%d want=%d", result.ThroughPublicationSequence, resetEvent.PublicationSeq)
			}
			break
		}
		if cycles > 50 {
			t.Fatal("incremental assistant projection did not converge")
		}
	}
	if cycles < 40 {
		t.Fatalf("10k assistant events were not consumed in bounded batches: cycles=%d", cycles)
	}

	// Recreate both read-model repository and Server before the terminal events
	// to prove the open assistant reducer is durable, not an in-memory cache.
	restartedReadModel := transcriptstore.NewWebReadModelRepository(db, db)
	restarted := &Server{workspaceStore: store, transcriptStore: repository, transcriptWebReadModel: restartedReadModel}
	appendRunnerPayloadEvent(t, repository, claim.Claim, "assistant-final", "assistant_message", map[string]any{
		"text":              "final corrected answer",
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(2, ""),
	})
	if _, _, _, err := repository.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claim.Claim, ClientMessageID: "assistant-terminal", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed","detail":"done","assistant_segment":{"version":1,"ordinal":2}}`),
	}); err != nil {
		t.Fatal(err)
	}
	work := requireSingleTranscriptWebProjectionWork(t, restartedReadModel, stream.OwnerID)
	terminal, err := restarted.tryIncrementalTranscriptWebReadModel(context.Background(), stream, work)
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Disposition != transcriptWebIncrementalAppliedReady || terminal.EventsRead != 2 || terminal.MessagesWritten != 1 {
		t.Fatalf("terminal incremental=%#v", terminal)
	}
	incrementalState, incrementalMessages, found, err := restartedReadModel.GetTranscriptWebProjectionCheckpoint(
		context.Background(), stream.OwnerID, stream.UID, branch.ActiveBranchID,
	)
	if err != nil || !found {
		t.Fatalf("incremental checkpoint found=%t err=%v", found, err)
	}
	if len(incrementalMessages) != 2 {
		t.Fatalf("incremental messages=%#v", incrementalMessages)
	}
	var tail map[string]any
	if json.Unmarshal(incrementalMessages[1].MessageJSON, &tail) != nil || tail["terminal_status"] != "completed" {
		t.Fatalf("incremental terminal tail=%s", incrementalMessages[1].MessageJSON)
	}
	if err := restarted.rebuildTranscriptWebReadModel(context.Background(), work); err != nil {
		t.Fatal(err)
	}
	fullState, fullMessages, found, err := restartedReadModel.GetTranscriptWebProjectionCheckpoint(
		context.Background(), stream.OwnerID, stream.UID, branch.ActiveBranchID,
	)
	if err != nil || !found {
		t.Fatalf("full checkpoint found=%t err=%v", found, err)
	}
	if incrementalState.SourceChainSHA256 != fullState.SourceChainSHA256 ||
		incrementalState.MessageCount != fullState.MessageCount ||
		incrementalState.VisibleMessageCount != fullState.VisibleMessageCount ||
		incrementalState.MessageArtifactReferenceCount != fullState.MessageArtifactReferenceCount ||
		!reflect.DeepEqual(incrementalMessages, fullMessages) {
		t.Fatalf("incremental assistant projection differs from full rebuild\nincremental=%#v\nfull=%#v", incrementalMessages, fullMessages)
	}
}

func TestTranscriptWebIncrementalTenThousandTerminalAttemptsKeepCheckpointBoundedAfterRestart(t *testing.T) {
	store, repository, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-terminal-compaction", "project-terminal-compaction", "frame-terminal-compaction")
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-terminal-compaction", OwnerID: "owner-terminal-compaction", ExternalID: "frame-terminal-compaction",
		SessionID: "frame-terminal-compaction", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-terminal-compaction", RootFrameID: "frame-terminal-compaction",
		FrameID: "frame-terminal-compaction", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	branch, err := repository.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	seedTranscriptWebIncrementalUserEvents(t, db, stream.UID, branch.ActiveBranchID, 1, "terminal-compaction-user")
	readModel := transcriptstore.NewWebReadModelRepository(db, db)
	server := &Server{workspaceStore: store, transcriptStore: repository, transcriptWebReadModel: readModel}
	if err := server.rebuildTranscriptWebReadModel(
		context.Background(), requireSingleTranscriptWebProjectionWork(t, readModel, stream.OwnerID),
	); err != nil {
		t.Fatal(err)
	}

	const terminalAttempts = 10_000
	seedTranscriptWebIncrementalTerminalAttempts(t, db, stream.UID, branch.ActiveBranchID, terminalAttempts)
	cycles := 0
	var finalWork transcriptstore.TranscriptWebProjectionWork
	for {
		work := requireSingleTranscriptWebProjectionWork(t, readModel, stream.OwnerID)
		result, err := server.tryIncrementalTranscriptWebReadModel(context.Background(), stream, work)
		if err != nil {
			t.Fatal(err)
		}
		cycles++
		if result.EventsRead <= 0 || result.EventsRead > transcriptWebIncrementalBatch ||
			(result.Disposition != transcriptWebIncrementalAppliedBuilding && result.Disposition != transcriptWebIncrementalAppliedReady) {
			t.Fatalf("cycle %d result=%#v", cycles, result)
		}
		if cycles == 20 {
			// Recreate both repositories and the server while 4,880 terminal
			// attempts remain, proving that compaction is durable rather than an
			// in-process cache.
			repository = transcriptstore.NewRepository(db)
			readModel = transcriptstore.NewWebReadModelRepository(db, db)
			server = &Server{workspaceStore: store, transcriptStore: repository, transcriptWebReadModel: readModel}
		}
		if result.Disposition == transcriptWebIncrementalAppliedReady {
			finalWork = work
			break
		}
		if cycles > 50 {
			t.Fatal("10k terminal attempts did not converge in bounded batches")
		}
	}
	if cycles != (terminalAttempts+transcriptWebIncrementalBatch-1)/transcriptWebIncrementalBatch {
		t.Fatalf("terminal attempts were not consumed in exact bounded batches: cycles=%d", cycles)
	}

	incrementalState, incrementalMessages, found, err := readModel.GetTranscriptWebProjectionCheckpoint(
		context.Background(), stream.OwnerID, stream.UID, branch.ActiveBranchID,
	)
	if err != nil || !found {
		t.Fatalf("incremental checkpoint found=%t err=%v", found, err)
	}
	// The checkpoint contains one scalar terminal high-water plus no historical
	// attempt/message bodies. 1 KiB leaves ample serialization headroom while
	// remaining independent of the 10,000-turn history size.
	if len(incrementalState.ProjectorStateJSON) >= 1_024 {
		t.Fatalf("terminal checkpoint grew with turn history: bytes=%d", len(incrementalState.ProjectorStateJSON))
	}
	var checkpoint transcriptWebProjectorCheckpointV1
	if err := json.Unmarshal(incrementalState.ProjectorStateJSON, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint.Assistant.TerminalThroughAttempt != terminalAttempts ||
		len(checkpoint.Assistant.SeenAttempts) != 0 || len(checkpoint.Assistant.SeenIdentities) != 0 ||
		len(checkpoint.Assistant.Attempts) != 0 {
		t.Fatalf("terminal history was retained in checkpoint: %#v", checkpoint.Assistant)
	}
	if len(incrementalMessages) != terminalAttempts+1 {
		t.Fatalf("incremental message count=%d want=%d", len(incrementalMessages), terminalAttempts+1)
	}

	if err := server.rebuildTranscriptWebReadModel(context.Background(), finalWork); err != nil {
		t.Fatal(err)
	}
	fullState, fullMessages, found, err := readModel.GetTranscriptWebProjectionCheckpoint(
		context.Background(), stream.OwnerID, stream.UID, branch.ActiveBranchID,
	)
	if err != nil || !found || incrementalState.SourceChainSHA256 != fullState.SourceChainSHA256 ||
		incrementalState.MessageCount != fullState.MessageCount ||
		incrementalState.VisibleMessageCount != fullState.VisibleMessageCount ||
		incrementalState.MessageArtifactReferenceCount != fullState.MessageArtifactReferenceCount ||
		!reflect.DeepEqual(incrementalMessages, fullMessages) {
		t.Fatalf("terminal compaction incremental/full mismatch: found=%t err=%v", found, err)
	}
	if len(fullState.ProjectorStateJSON) >= 1_024 {
		t.Fatalf("full rebuild retained terminal history in checkpoint: bytes=%d", len(fullState.ProjectorStateJSON))
	}

	seedTranscriptWebIncrementalAssistantEventBatch(
		t, db, stream.UID, branch.ActiveBranchID, terminalAttempts, 1,
		func(int) (string, map[string]any) {
			return "content_delta", map[string]any{
				"text":              "invalid reopened attempt",
				"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
			}
		},
	)
	reopenedWork := requireSingleTranscriptWebProjectionWork(t, readModel, stream.OwnerID)
	if _, err := server.tryIncrementalTranscriptWebReadModel(context.Background(), stream, reopenedWork); !errors.Is(err, transcriptstore.ErrEventConflict) {
		t.Fatalf("terminal attempt was reopened: err=%v", err)
	}
}

func TestTranscriptWebIncrementalFailedTerminalRestoresLegacyResetCandidateAfterRestart(t *testing.T) {
	store, repository, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-reset-rollback", "project-reset-rollback", "frame-reset-rollback")
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-reset-rollback", OwnerID: "owner-reset-rollback", ExternalID: "frame-reset-rollback",
		SessionID: "frame-reset-rollback", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-reset-rollback", RootFrameID: "frame-reset-rollback", FrameID: "frame-reset-rollback", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	branch, err := repository.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	seedTranscriptWebIncrementalUserEvents(t, db, stream.UID, branch.ActiveBranchID, 1, "rollback-user")
	claim, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-reset-rollback",
		TTL: time.Hour, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	appendRunnerPayloadEvent(t, repository, claim.Claim, "rollback-candidate", "content_delta", map[string]any{
		"text":              "candidate must survive failure",
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})
	readModel := transcriptstore.NewWebReadModelRepository(db, db)
	server := &Server{workspaceStore: store, transcriptStore: repository, transcriptWebReadModel: readModel}
	if err := server.rebuildTranscriptWebReadModel(
		context.Background(), requireSingleTranscriptWebProjectionWork(t, readModel, stream.OwnerID),
	); err != nil {
		t.Fatal(err)
	}
	appendRunnerPayloadEvent(t, repository, claim.Claim, "rollback-reset", "content_reset", map[string]any{"text": ""})
	resetWork := requireSingleTranscriptWebProjectionWork(t, readModel, stream.OwnerID)
	resetResult, err := server.tryIncrementalTranscriptWebReadModel(context.Background(), stream, resetWork)
	if err != nil || resetResult.Disposition != transcriptWebIncrementalAppliedReady {
		t.Fatalf("reset result=%#v err=%v", resetResult, err)
	}
	resetState, resetMessages, found, err := readModel.GetTranscriptWebProjectionCheckpoint(
		context.Background(), stream.OwnerID, stream.UID, branch.ActiveBranchID,
	)
	if err != nil || !found || resetState.MessageCount != 1 || len(resetMessages) != 1 {
		t.Fatalf("reset checkpoint=%#v messages=%#v found=%t err=%v", resetState, resetMessages, found, err)
	}

	restartedReadModel := transcriptstore.NewWebReadModelRepository(db, db)
	restarted := &Server{workspaceStore: store, transcriptStore: repository, transcriptWebReadModel: restartedReadModel}
	if _, _, _, err := repository.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claim.Claim, ClientMessageID: "rollback-terminal", Status: "failed",
		PayloadJSON: []byte(`{"status":"failed","detail":"Completion failed before a result was accepted.","assistant_segment":{"version":1,"ordinal":1}}`),
	}); err != nil {
		t.Fatal(err)
	}
	terminalWork := requireSingleTranscriptWebProjectionWork(t, restartedReadModel, stream.OwnerID)
	terminal, err := restarted.tryIncrementalTranscriptWebReadModel(context.Background(), stream, terminalWork)
	if err != nil || terminal.Disposition != transcriptWebIncrementalAppliedReady {
		t.Fatalf("terminal=%#v err=%v", terminal, err)
	}
	incrementalState, incrementalMessages, found, err := restartedReadModel.GetTranscriptWebProjectionCheckpoint(
		context.Background(), stream.OwnerID, stream.UID, branch.ActiveBranchID,
	)
	if err != nil || !found || len(incrementalMessages) != 2 ||
		!strings.Contains(string(incrementalMessages[1].MessageJSON), "candidate must survive failure") {
		t.Fatalf("restored state=%#v messages=%#v found=%t err=%v", incrementalState, incrementalMessages, found, err)
	}
	if err := restarted.rebuildTranscriptWebReadModel(context.Background(), terminalWork); err != nil {
		t.Fatal(err)
	}
	fullState, fullMessages, found, err := restartedReadModel.GetTranscriptWebProjectionCheckpoint(
		context.Background(), stream.OwnerID, stream.UID, branch.ActiveBranchID,
	)
	if err != nil || !found || incrementalState.SourceChainSHA256 != fullState.SourceChainSHA256 ||
		!reflect.DeepEqual(incrementalMessages, fullMessages) {
		t.Fatalf("rollback incremental/full mismatch\nincremental=%#v\nfull=%#v\nerr=%v", incrementalMessages, fullMessages, err)
	}
}

func requireSingleTranscriptWebProjectionWork(
	t *testing.T,
	readModel *transcriptstore.WebReadModelRepository,
	ownerID string,
) transcriptstore.TranscriptWebProjectionWork {
	t.Helper()
	work, err := readModel.ListTranscriptWebProjectionWork(context.Background(), ownerID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(work) != 1 {
		t.Fatalf("projection work=%#v", work)
	}
	return work[0]
}

func seedTranscriptWebIncrementalUserEvents(
	t *testing.T,
	db *sql.DB,
	streamUID, branchID string,
	count int,
	prefix string,
) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var nextEventID, nextPublication int64
	if err := tx.QueryRow(`SELECT next_event_id,next_publication_seq FROM transcript_streams WHERE stream_uid=?`, streamUID).
		Scan(&nextEventID, &nextPublication); err != nil {
		t.Fatal(err)
	}
	var throughOrdinal int64
	if err := tx.QueryRow(`SELECT through_ordinal FROM transcript_branch_heads WHERE stream_uid=? AND branch_id=?`,
		streamUID, branchID).Scan(&throughOrdinal); err != nil {
		t.Fatal(err)
	}
	eventStatement, err := tx.Prepare(`INSERT INTO transcript_events(
		stream_uid,event_id,publication_seq,client_message_id,event_type,source,runner_attempt,
		payload_json,frame_event_id,created_at) VALUES(?,?,?,?,?,'payload',NULL,?,NULL,?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer eventStatement.Close()
	membershipStatement, err := tx.Prepare(`INSERT INTO transcript_branch_events(stream_uid,branch_id,ordinal,event_id)
		VALUES(?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer membershipStatement.Close()
	baseTime := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	for offset := 0; offset < count; offset++ {
		eventID := nextEventID + int64(offset)
		publication := nextPublication + int64(offset)
		identity := fmt.Sprintf("%s-%06d", prefix, publication)
		payload, err := json.Marshal(map[string]any{
			"messageUuid": identity, "clientMessageId": identity, "text": identity,
			"role": "user", "_uuid": identity,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := eventStatement.Exec(
			streamUID, eventID, publication, identity, "user_message", payload,
			baseTime.Add(time.Duration(publication)*time.Millisecond),
		); err != nil {
			t.Fatalf("insert event %d: %v", publication, err)
		}
		if _, err := membershipStatement.Exec(streamUID, branchID, throughOrdinal+int64(offset)+1, eventID); err != nil {
			t.Fatalf("insert membership %d: %v", publication, err)
		}
	}
	if _, err := tx.Exec(`UPDATE transcript_streams
		SET next_event_id=?,next_publication_seq=?,input_revision=input_revision+?,updated_at=? WHERE stream_uid=?`,
		nextEventID+int64(count), nextPublication+int64(count), count, time.Now().UTC(), streamUID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func seedTranscriptWebIncrementalAssistantEvents(
	t *testing.T,
	db *sql.DB,
	streamUID, branchID string,
	attempt int64,
	count int,
) {
	t.Helper()
	seedTranscriptWebIncrementalAssistantEventBatch(t, db, streamUID, branchID, attempt, count, func(offset int) (string, map[string]any) {
		return "content_delta", map[string]any{
			"text": "x", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
		}
	})
}

func seedTranscriptWebIncrementalAssistantReset(
	t *testing.T,
	db *sql.DB,
	streamUID, branchID string,
	attempt int64,
	text string,
	segment int64,
) transcriptstore.Event {
	t.Helper()
	return seedTranscriptWebIncrementalAssistantEventBatch(t, db, streamUID, branchID, attempt, 1, func(int) (string, map[string]any) {
		return "content_reset", map[string]any{
			"text": text, "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(segment, ""),
		}
	})
}

func seedTranscriptWebIncrementalAssistantEventBatch(
	t *testing.T,
	db *sql.DB,
	streamUID, branchID string,
	attempt int64,
	count int,
	event func(int) (string, map[string]any),
) transcriptstore.Event {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var nextEventID, nextPublication int64
	if err := tx.QueryRow(`SELECT next_event_id,next_publication_seq FROM transcript_streams WHERE stream_uid=?`, streamUID).
		Scan(&nextEventID, &nextPublication); err != nil {
		t.Fatal(err)
	}
	var throughOrdinal int64
	if err := tx.QueryRow(`SELECT through_ordinal FROM transcript_branch_heads WHERE stream_uid=? AND branch_id=?`,
		streamUID, branchID).Scan(&throughOrdinal); err != nil {
		t.Fatal(err)
	}
	statement, err := tx.Prepare(`INSERT INTO transcript_events(
		stream_uid,event_id,publication_seq,client_message_id,event_type,source,runner_attempt,
		payload_json,frame_event_id,created_at) VALUES(?,?,?,?,?,'payload',?,?,NULL,?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer statement.Close()
	membership, err := tx.Prepare(`INSERT INTO transcript_branch_events(stream_uid,branch_id,ordinal,event_id) VALUES(?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer membership.Close()
	baseTime := time.Date(2026, time.January, 2, 0, 0, 0, 0, time.UTC)
	var last transcriptstore.Event
	for offset := 0; offset < count; offset++ {
		eventType, payload := event(offset)
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		eventID := nextEventID + int64(offset)
		publication := nextPublication + int64(offset)
		clientID := fmt.Sprintf("assistant-%s-%06d", eventType, publication)
		createdAt := baseTime.Add(time.Duration(publication) * time.Millisecond)
		if _, err := statement.Exec(streamUID, eventID, publication, clientID, eventType, attempt, raw, createdAt); err != nil {
			t.Fatalf("insert assistant event %d: %v", publication, err)
		}
		if _, err := membership.Exec(streamUID, branchID, throughOrdinal+int64(offset)+1, eventID); err != nil {
			t.Fatalf("insert assistant membership %d: %v", publication, err)
		}
		last = transcriptstore.Event{
			StreamUID: streamUID, EventID: eventID, PublicationSeq: publication,
			ClientMessageID: clientID, Type: eventType, Source: transcriptstore.EventSourcePayload,
			RunnerAttempt: &attempt, PayloadJSON: raw, CreatedAt: createdAt,
		}
	}
	if _, err := tx.Exec(`UPDATE transcript_streams SET next_event_id=?,next_publication_seq=?,updated_at=? WHERE stream_uid=?`,
		nextEventID+int64(count), nextPublication+int64(count), time.Now().UTC(), streamUID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return last
}

func seedTranscriptWebIncrementalTerminalAttempts(
	t *testing.T,
	db *sql.DB,
	streamUID, branchID string,
	count int,
) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var nextEventID, nextPublication int64
	if err := tx.QueryRow(`SELECT next_event_id,next_publication_seq FROM transcript_streams WHERE stream_uid=?`, streamUID).
		Scan(&nextEventID, &nextPublication); err != nil {
		t.Fatal(err)
	}
	var throughOrdinal int64
	if err := tx.QueryRow(`SELECT through_ordinal FROM transcript_branch_heads WHERE stream_uid=? AND branch_id=?`,
		streamUID, branchID).Scan(&throughOrdinal); err != nil {
		t.Fatal(err)
	}
	var priorAttempt int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(attempt),0) FROM transcript_runner_attempts WHERE stream_uid=?`, streamUID).
		Scan(&priorAttempt); err != nil {
		t.Fatal(err)
	}
	attemptStatement, err := tx.Prepare(`INSERT INTO transcript_runner_attempts(
		stream_uid,attempt,runner_id,claim_token_sha256,claimed_input_revision,resume_source,
		resume_checkpoint_sequence,status,phase,phase_sequence,last_checkpoint_sequence,claimed_at,expires_at
	) VALUES(?,?,?,?,0,'fresh',0,'running','claimed',1,0,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer attemptStatement.Close()
	eventStatement, err := tx.Prepare(`INSERT INTO transcript_events(
		stream_uid,event_id,publication_seq,client_message_id,event_type,source,runner_attempt,
		payload_json,frame_event_id,created_at
	) VALUES(?,?,?,?, 'runner_finished','payload',?,?,NULL,?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer eventStatement.Close()
	finishStatement, err := tx.Prepare(`UPDATE transcript_runner_attempts
		SET status='completed',phase='terminal',finished_event_id=?,finished_at=?
		WHERE stream_uid=? AND attempt=? AND status='running'`)
	if err != nil {
		t.Fatal(err)
	}
	defer finishStatement.Close()
	receiptStatement, err := tx.Prepare(`INSERT INTO transcript_runner_receipts(
		stream_uid,attempt,event_id,status,finished_at
	) VALUES(?,?,?,'completed',?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer receiptStatement.Close()
	membershipStatement, err := tx.Prepare(`INSERT INTO transcript_branch_events(stream_uid,branch_id,ordinal,event_id)
		VALUES(?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer membershipStatement.Close()
	baseTime := time.Date(2026, time.January, 3, 0, 0, 0, 0, time.UTC)
	claimDigest := make([]byte, 32)
	for offset := 0; offset < count; offset++ {
		attempt := priorAttempt + int64(offset) + 1
		eventID := nextEventID + int64(offset)
		publication := nextPublication + int64(offset)
		createdAt := baseTime.Add(time.Duration(publication) * time.Millisecond)
		clientID := fmt.Sprintf("terminal-compaction-%06d", attempt)
		payload, err := json.Marshal(map[string]any{
			"status": "completed", "detail": clientID,
			"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
		})
		if err != nil {
			t.Fatal(err)
		}
		claimDigest[0] = byte(attempt)
		claimDigest[1] = byte(attempt >> 8)
		if _, err := attemptStatement.Exec(
			streamUID, attempt, fmt.Sprintf("runner-%06d", attempt), claimDigest, createdAt, createdAt.Add(time.Hour),
		); err != nil {
			t.Fatalf("insert attempt %d: %v", attempt, err)
		}
		if _, err := eventStatement.Exec(streamUID, eventID, publication, clientID, attempt, payload, createdAt); err != nil {
			t.Fatalf("insert terminal event %d: %v", attempt, err)
		}
		if result, err := finishStatement.Exec(eventID, createdAt, streamUID, attempt); err != nil {
			t.Fatalf("finish attempt %d: %v", attempt, err)
		} else if affected, err := result.RowsAffected(); err != nil || affected != 1 {
			t.Fatalf("finish attempt %d affected=%d err=%v", attempt, affected, err)
		}
		if _, err := receiptStatement.Exec(streamUID, attempt, eventID, createdAt); err != nil {
			t.Fatalf("insert receipt %d: %v", attempt, err)
		}
		if _, err := membershipStatement.Exec(streamUID, branchID, throughOrdinal+int64(offset)+1, eventID); err != nil {
			t.Fatalf("insert terminal membership %d: %v", attempt, err)
		}
	}
	if _, err := tx.Exec(`UPDATE transcript_streams SET next_event_id=?,next_publication_seq=?,updated_at=? WHERE stream_uid=?`,
		nextEventID+int64(count), nextPublication+int64(count), time.Now().UTC(), streamUID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

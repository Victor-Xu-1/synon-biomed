package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"

	_ "modernc.org/sqlite"
)

func TestServerTranscriptDeliveryRequiresExplicitBackgroundStartup(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	withoutBackground := New(Options{FileRoot: root, Workspace: store, Transcript: repo})
	if withoutBackground.transcriptDeliveryDone != nil {
		t.Fatal("server started transcript delivery without explicit background-service opt-in")
	}
	if withoutBackground.kernelManager != nil {
		t.Fatal("server discovered a kernel manager without explicit background-service opt-in")
	}
	withBackground := New(Options{
		FileRoot: root, Workspace: store, Transcript: repo, StartBackgroundServices: true,
	})
	if withBackground.transcriptDeliveryDone == nil {
		t.Fatal("explicit background-service opt-in did not start transcript delivery")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := withBackground.Close(ctx); err != nil {
		t.Fatalf("close explicit background server: %v", err)
	}
}

func TestTranscriptWebDeliveryCoordinatorStopsScanningWhenIdle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	wake := make(chan struct{}, 1)
	cycles := make(chan int, 4)
	var count atomic.Int32
	done := make(chan struct{})
	go func() {
		runTranscriptWebDeliveryCoordinator(ctx, wake, func(context.Context) (transcriptWebDeliveryCycleState, error) {
			cycle := int(count.Add(1))
			cycles <- cycle
			return transcriptWebDeliveryCycleState{}, nil
		})
		close(done)
	}()

	if cycle := awaitTranscriptWebDeliveryCycle(t, cycles); cycle != 1 {
		t.Fatalf("startup cycle=%d", cycle)
	}
	select {
	case cycle := <-cycles:
		t.Fatalf("idle coordinator repeated cycle=%d", cycle)
	case <-time.After(350 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("coordinator did not stop after cancellation")
	}
}

func TestTranscriptWebDeliveryProjectsModelSelectionInterruptionAsRecoverable(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	const ownerID, projectID, frameID = "owner-model-wait", "project-model-wait", "frame-model-wait"
	seedTranscriptWebFrame(t, store, ownerID, projectID, frameID)
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:" + frameID, OwnerID: ownerID, ExternalID: frameID, SessionID: frameID,
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: projectID, RootFrameID: frameID, FrameID: frameID, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: ownerID, ClientMessageID: "model-wait-user",
		PayloadJSON: []byte(`{"text":"continue after model configuration"}`), Destinations: []string{transcriptWebDestination},
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: ownerID, RunnerID: "model-wait-runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	if _, err := repo.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "model-wait-interruption",
		ReasonCode:   sessionRunnerModelProviderUnavailableReasonCode,
		ResumeDetail: "Configure or select a model, then continue this same task.",
		Resumable:    true, AutoResume: false, Destinations: []string{transcriptWebDestination},
	}); err != nil {
		t.Fatal(err)
	}
	server := &Server{workspaceStore: store, transcriptStore: repo, compatEvents: newCompatEventHub()}
	if err := server.drainTranscriptWebDeliveries(context.Background()); err != nil {
		t.Fatal(err)
	}
	var runtimeWaiting, frameInvalidated bool
	for _, event := range transcriptWebEvents(t, store, ownerID) {
		switch event.Type {
		case "runtime.statusChanged":
			runtimeWaiting = event.Payload["phase"] == "waiting_input" &&
				event.Payload["reason_code"] == sessionRunnerModelProviderUnavailableReasonCode
		case "frame_update":
			if event.Payload["status"] == "paused" &&
				event.Payload["runtime_interruption_reason"] == sessionRunnerModelProviderUnavailableReasonCode {
				frameInvalidated = len(event.Invalidations) > 0
			}
		}
	}
	if !runtimeWaiting || !frameInvalidated {
		t.Fatalf("runtimeWaiting=%t frameInvalidated=%t events=%#v", runtimeWaiting, frameInvalidated, transcriptWebEvents(t, store, ownerID))
	}
}

func TestTranscriptWebDeliveryCoordinatorWakeIsImmediateAndBurstCoalesced(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wake := make(chan struct{}, 1)
	cycles := make(chan int, 8)
	secondEntered := make(chan struct{})
	releaseSecond := make(chan struct{})
	var count atomic.Int32
	go runTranscriptWebDeliveryCoordinator(ctx, wake, func(context.Context) (transcriptWebDeliveryCycleState, error) {
		cycle := int(count.Add(1))
		if cycle == 2 {
			close(secondEntered)
			<-releaseSecond
		}
		cycles <- cycle
		return transcriptWebDeliveryCycleState{}, nil
	})

	if cycle := awaitTranscriptWebDeliveryCycle(t, cycles); cycle != 1 {
		t.Fatalf("startup cycle=%d", cycle)
	}
	server := &Server{transcriptDeliveryWake: wake}
	server.signalTranscriptWebDelivery()
	select {
	case <-secondEntered:
	case <-time.After(time.Second):
		t.Fatal("durable write wake did not start a cycle")
	}
	for range 100 {
		server.signalTranscriptWebDelivery()
	}
	close(releaseSecond)
	if cycle := awaitTranscriptWebDeliveryCycle(t, cycles); cycle != 2 {
		t.Fatalf("woken cycle=%d", cycle)
	}
	if cycle := awaitTranscriptWebDeliveryCycle(t, cycles); cycle != 3 {
		t.Fatalf("coalesced follow-up cycle=%d", cycle)
	}
	select {
	case cycle := <-cycles:
		t.Fatalf("burst produced uncoalesced cycle=%d", cycle)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestTranscriptWebDeliveryCoordinatorSelfDrainsImmediateBatchesAndSchedulesRetry(t *testing.T) {
	t.Run("immediate backlog", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		cycles := make(chan int, 8)
		var count atomic.Int32
		go runTranscriptWebDeliveryCoordinator(ctx, make(chan struct{}, 1), func(context.Context) (transcriptWebDeliveryCycleState, error) {
			cycle := int(count.Add(1))
			cycles <- cycle
			return transcriptWebDeliveryCycleState{Immediate: cycle < 4}, nil
		})
		for want := 1; want <= 4; want++ {
			if cycle := awaitTranscriptWebDeliveryCycle(t, cycles); cycle != want {
				t.Fatalf("cycle=%d want=%d", cycle, want)
			}
		}
		select {
		case cycle := <-cycles:
			t.Fatalf("caught-up coordinator remained busy at cycle=%d", cycle)
		case <-time.After(100 * time.Millisecond):
		}
	})

	t.Run("durable retry", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		cycles := make(chan int, 4)
		var count atomic.Int32
		go runTranscriptWebDeliveryCoordinator(ctx, make(chan struct{}, 1), func(context.Context) (transcriptWebDeliveryCycleState, error) {
			cycle := int(count.Add(1))
			cycles <- cycle
			if cycle == 1 {
				return transcriptWebDeliveryCycleState{RetryAfter: 20 * time.Millisecond}, nil
			}
			return transcriptWebDeliveryCycleState{}, nil
		})
		if cycle := awaitTranscriptWebDeliveryCycle(t, cycles); cycle != 1 {
			t.Fatalf("startup cycle=%d", cycle)
		}
		if cycle := awaitTranscriptWebDeliveryCycle(t, cycles); cycle != 2 {
			t.Fatalf("retry cycle=%d", cycle)
		}
	})
}

func TestTranscriptWebReadModelProgressRequiresObservableAdvance(t *testing.T) {
	key := "owner\x00stream\x00branch"
	before := transcriptWebReadModelProgressSnapshot{
		pending: true,
		states: map[string]transcriptWebReadModelProgressState{
			key: {found: true, status: "building", branchGeneration: 1, through: 256, sourceRevision: 2},
		},
	}
	unchanged := transcriptWebReadModelProgressSnapshot{
		pending: true,
		states: map[string]transcriptWebReadModelProgressState{
			key: {found: true, status: "building", branchGeneration: 1, through: 256, sourceRevision: 2},
		},
	}
	if unchanged.advancedSince(before) {
		t.Fatal("pending read-model work without persisted progress would busy-loop")
	}
	advanced := transcriptWebReadModelProgressSnapshot{
		pending: true,
		states: map[string]transcriptWebReadModelProgressState{
			key: {found: true, status: "building", branchGeneration: 1, through: 512, sourceRevision: 2},
		},
	}
	if !advanced.advancedSince(before) {
		t.Fatal("persisted incremental progress did not schedule the next bounded batch")
	}
}

func TestTranscriptWebDeliveryStartupSelfDrainsMoreThanOneIncrementalSQLiteBatch(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-backlog", "project-backlog", "frame-backlog")
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-backlog", OwnerID: "owner-backlog", ExternalID: "frame-backlog",
		SessionID: "frame-backlog", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-backlog", RootFrameID: "frame-backlog", FrameID: "frame-backlog", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	appendMessage := func(index int) {
		t.Helper()
		if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID,
			ClientMessageID: fmt.Sprintf("backlog-client-%04d", index),
			FrameEventID:    fmt.Sprintf("backlog-frame-event-%04d", index),
			MessageUUID:     fmt.Sprintf("backlog-message-%04d", index),
			Text:            fmt.Sprintf("message %04d", index),
			Destinations:    []string{transcriptWebDestination},
		}); err != nil || !created {
			t.Fatalf("append %d created=%t err=%v", index, created, err)
		}
	}
	appendMessage(0)
	readModel := transcriptstore.NewWebReadModelRepository(db, db)
	server := &Server{workspaceStore: store, transcriptStore: repo, transcriptWebReadModel: readModel}
	if err := server.runTranscriptWebReadModelCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	const incrementalBatches = 6
	for index := 1; index <= transcriptWebIncrementalBatch*incrementalBatches+17; index++ {
		appendMessage(index)
	}
	target, err := repo.GetProjectionSnapshot(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}

	server.startTranscriptWebDelivery()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.stopTranscriptWebDelivery(ctx); err != nil {
			t.Errorf("stop delivery coordinator: %v", err)
		}
	})
	// This is a startup/self-scheduling contract, not a throughput benchmark.
	// The coordinator prioritizes delivery while the 1,554-event backlog is
	// runnable, then catches up the history projection. Scanning every intent
	// every 10ms adds race-instrumented SQLite work to the observation itself;
	// the old one-minute throughput bound was also machine-dependent.
	// Observe durable progress at low frequency instead: lack of progress still
	// fails promptly, and an absolute deadline prevents an endless slow trickle.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	const progressTimeout = 45 * time.Second
	started := time.Now()
	lastProgress := started
	previousDelivered := 0
	previousProjectionSequence := int64(1)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		fence, err := readModel.GetTranscriptWebProjectionFence(
			ctx, stream.OwnerID, stream.UID, target.BranchID,
		)
		if err != nil {
			t.Fatal(err)
		}
		var delivered, unsettled int
		if err := db.QueryRowContext(ctx, `SELECT
			COALESCE(SUM(CASE WHEN status='delivered' THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN status NOT IN ('delivered','revoked') THEN 1 ELSE 0 END),0)
			FROM transcript_delivery_intents WHERE stream_uid=?`, stream.UID).Scan(&delivered, &unsettled); err != nil {
			t.Fatal(err)
		}
		projectionReady := fence.StateFound && fence.StateStatus == "ready" &&
			fence.StateThroughPublicationSequence == target.ThroughPublicationSequence &&
			fence.StateSourceRevision == fence.SourceRevision
		if projectionReady && delivered == int(target.ThroughPublicationSequence) && unsettled == 0 {
			t.Logf("startup self-drained %d events and converged projection in %s", delivered, time.Since(started))
			break
		}
		projectionSequence := fence.StateThroughPublicationSequence
		if delivered < previousDelivered || projectionSequence < previousProjectionSequence {
			t.Fatalf("startup progress regressed: delivery=%d->%d projection=%d->%d",
				previousDelivered, delivered, previousProjectionSequence, projectionSequence)
		}
		if delivered > previousDelivered || projectionSequence > previousProjectionSequence {
			lastProgress = time.Now()
			previousDelivered, previousProjectionSequence = delivered, projectionSequence
		}
		if time.Since(lastProgress) > progressTimeout {
			t.Fatalf("startup made no durable progress for %s: projection=%d/%d status=%s delivered=%d unsettled=%d",
				progressTimeout, projectionSequence, target.ThroughPublicationSequence, fence.StateStatus, delivered, unsettled)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("startup did not converge before absolute deadline: projection=%d/%d status=%s delivered=%d unsettled=%d: %v",
				projectionSequence, target.ThroughPublicationSequence, fence.StateStatus, delivered, unsettled, ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestTranscriptWebDeliveryPublishesWithoutBuildingMissingHistoryProjection(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-stale", "project-stale", "frame-stale")
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-stale", OwnerID: "owner-stale", ExternalID: "frame-stale", SessionID: "frame-stale",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project-stale",
		RootFrameID: "frame-stale", FrameID: "frame-stale", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "stale-user",
		PayloadJSON: []byte(`{"text":"wait for projection"}`), Destinations: []string{transcriptWebDestination},
	}); err != nil {
		t.Fatal(err)
	}
	server := &Server{
		workspaceStore: store, transcriptStore: repo,
		transcriptWebReadModel: transcriptstore.NewWebReadModelRepository(db, db),
		compatEvents:           newCompatEventHub(),
	}
	processed, err := server.drainTranscriptWebOwnerPass(context.Background(), stream.OwnerID, false)
	if err != nil || processed != 1 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	intent, err := repo.GetDeliveryIntent(
		context.Background(), stream.OwnerID, stream.UID, 1, transcriptWebDestination, 1,
	)
	if err != nil || intent.Status != "delivered" || intent.AttemptCount != 1 {
		t.Fatalf("intent=%#v err=%v", intent, err)
	}
	settlement, err := repo.DeliverySettlementState(context.Background(), stream.OwnerID, transcriptWebDestination)
	if err != nil || settlement.Unsettled() {
		t.Fatalf("settlement=%#v err=%v", settlement, err)
	}
	snapshot, err := repo.GetProjectionSnapshot(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := server.transcriptWebReadModel.GetTranscriptWebProjectionFence(
		context.Background(), stream.OwnerID, stream.UID, snapshot.BranchID,
	)
	if err != nil || fence.StateFound {
		t.Fatalf("live delivery synchronously built missing history projection: fence=%#v err=%v", fence, err)
	}
	foundUser := false
	for _, event := range transcriptWebEvents(t, store, stream.OwnerID) {
		foundUser = foundUser || event.Type == "message.userCreated" && webString(event.Payload["msg_id"]) == "stale-user"
	}
	if !foundUser {
		t.Fatal("current stream was not published directly from the durable Transcript claim")
	}
}

func TestTranscriptWebDeliveryPassDoesNotLetUnrelatedQuarantineBlockReadyStream(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-isolated", "project-quarantined", "frame-quarantined")
	seedTranscriptWebFrame(t, store, "owner-isolated", "project-live", "frame-live")

	quarantined, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-quarantined", OwnerID: "owner-isolated", ExternalID: "frame-quarantined",
		SessionID: "frame-quarantined", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-quarantined", RootFrameID: "frame-quarantined", FrameID: "frame-quarantined", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: quarantined.UID, OwnerID: quarantined.OwnerID,
		ClientMessageID: "quarantined-client-1", FrameEventID: "quarantined-event-1",
		MessageUUID: "quarantined-message-1", Text: "historical projection",
	}); err != nil || !created {
		t.Fatalf("seed quarantined projection created=%t err=%v", created, err)
	}
	readModel := transcriptstore.NewWebReadModelRepository(db, db)
	server := &Server{
		workspaceStore: store, transcriptStore: repo, transcriptWebReadModel: readModel,
		compatEvents: newCompatEventHub(),
	}
	if err := server.runTranscriptWebReadModelCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE transcript_web_projection_state
		SET status='quarantined',last_error_code='projection_build_failed'
		WHERE stream_uid=?`, quarantined.UID); err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: quarantined.UID, OwnerID: quarantined.OwnerID,
		ClientMessageID: "quarantined-client-2", FrameEventID: "quarantined-event-2",
		MessageUUID: "quarantined-message-2", Text: "unrepairable historical tail",
	}); err != nil || !created {
		t.Fatalf("dirty quarantined projection created=%t err=%v", created, err)
	}
	if changed, err := store.TransitionCompatibilityFrameStatus("frame-quarantined", "processing", "failed"); err != nil || !changed {
		t.Fatalf("terminal quarantine changed=%t err=%v", changed, err)
	}

	live, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-live", OwnerID: "owner-isolated", ExternalID: "frame-live", SessionID: "frame-live",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project-live",
		RootFrameID: "frame-live", FrameID: "frame-live", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: live.UID, OwnerID: live.OwnerID, ClientMessageID: "live-client",
		FrameEventID: "live-event", MessageUUID: "live-message", Text: "deliver this now",
		Destinations: []string{transcriptWebDestination},
	}); err != nil || !created {
		t.Fatalf("append live event created=%t err=%v", created, err)
	}

	progress, err := server.transcriptWebReadModelProgress(context.Background())
	if err != nil || !progress.pending {
		t.Fatalf("expected unrelated quarantined work before pass: progress=%#v err=%v", progress, err)
	}
	if _, err := server.runTranscriptWebDeliveryPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM transcript_delivery_intents
		WHERE stream_uid=? AND destination=?`, live.UID, transcriptWebDestination).Scan(&status); err != nil || status != "delivered" {
		t.Fatalf("live intent status=%q err=%v", status, err)
	}
	foundLive := false
	for _, event := range transcriptWebEvents(t, store, live.OwnerID) {
		foundLive = foundLive || event.Type == "message.userCreated" && webString(event.Payload["msg_id"]) == "live-client"
	}
	if !foundLive {
		t.Fatal("ready stream was not published while an unrelated projection remained quarantined")
	}
}

func TestTranscriptWebClaimPublishesWithoutWaitingForHistoryProjection(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-covered", "project-covered", "frame-covered")
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-covered", OwnerID: "owner-covered", ExternalID: "frame-covered", SessionID: "frame-covered",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project-covered",
		RootFrameID: "frame-covered", FrameID: "frame-covered", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "covered-user",
		PayloadJSON: []byte(`{"text":"stream it"}`),
	}); err != nil {
		t.Fatal(err)
	}
	runner, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "covered-runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !runner.Claimed {
		t.Fatalf("runner=%#v err=%v", runner, err)
	}
	if _, created, err := repo.AppendRunnerEvent(context.Background(), transcriptstore.AppendEventInput{
		Claim: runner.Claim, ClientMessageID: "covered-delta-1", Type: "content_delta",
		Source: transcriptstore.EventSourcePayload, PayloadJSON: []byte(`{"text":"第一段"}`),
		Destinations: []string{transcriptWebDestination},
	}); err != nil || !created {
		t.Fatalf("covered delta created=%t err=%v", created, err)
	}
	readModel := transcriptstore.NewWebReadModelRepository(db, db)
	server := &Server{
		workspaceStore: store, transcriptStore: repo, transcriptWebReadModel: readModel,
		compatEvents: newCompatEventHub(),
	}
	snapshot, err := repo.GetProjectionSnapshot(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := readModel.GetTranscriptWebProjectionFence(
		context.Background(), stream.OwnerID, stream.UID, snapshot.BranchID,
	)
	if err != nil || fence.StateFound {
		t.Fatalf("history projection unexpectedly ready before live delivery: fence=%#v err=%v", fence, err)
	}
	delivery, err := repo.ClaimNextDelivery(context.Background(), transcriptstore.ClaimDeliveryInput{
		OwnerID: stream.OwnerID, Destination: transcriptWebDestination,
		WorkerID: transcriptWebWorkerID, TTL: transcriptWebClaimTTL,
	})
	if err != nil || !delivery.Claimed || delivery.Claim.Event.ClientMessageID != "covered-delta-1" {
		t.Fatalf("delivery=%#v err=%v", delivery, err)
	}
	if _, created, err := repo.AppendRunnerEvent(context.Background(), transcriptstore.AppendEventInput{
		Claim: runner.Claim, ClientMessageID: "covered-delta-2", Type: "content_delta",
		Source: transcriptstore.EventSourcePayload, PayloadJSON: []byte(`{"text":"第二段"}`),
		Destinations: []string{transcriptWebDestination},
	}); err != nil || !created {
		t.Fatalf("newer delta created=%t err=%v", created, err)
	}
	if err := server.publishTranscriptWebClaim(context.Background(), delivery.Claim); err != nil {
		t.Fatalf("durable live claim waited for history projection: %v", err)
	}
	if _, err := repo.AcknowledgeDelivery(context.Background(), transcriptstore.AcknowledgeDeliveryInput{Claim: delivery.Claim}); err != nil {
		t.Fatal(err)
	}

	newerDelivery, err := repo.ClaimNextDelivery(context.Background(), transcriptstore.ClaimDeliveryInput{
		OwnerID: stream.OwnerID, Destination: transcriptWebDestination,
		WorkerID: transcriptWebWorkerID, TTL: transcriptWebClaimTTL,
	})
	if err != nil || !newerDelivery.Claimed || newerDelivery.Claim.Event.ClientMessageID != "covered-delta-2" {
		t.Fatalf("newer delivery=%#v err=%v", newerDelivery, err)
	}
	if err := server.publishTranscriptWebClaim(context.Background(), newerDelivery.Claim); err != nil {
		t.Fatalf("newer durable live claim waited for history projection: %v", err)
	}
	if _, err := repo.AcknowledgeDelivery(context.Background(), transcriptstore.AcknowledgeDeliveryInput{Claim: newerDelivery.Claim}); err != nil {
		t.Fatal(err)
	}

	foundDeltas := map[string]bool{}
	for _, event := range transcriptWebEvents(t, store, stream.OwnerID) {
		if event.Type != "message.stream" || webString(event.Payload["stream_type"]) != "text" {
			continue
		}
		data := webString(event.Payload["data"])
		if data != "第一段" && data != "第二段" {
			continue
		}
		foundDeltas[data] = true
		if fmt.Sprint(event.Payload["source_publication_sequence"]) == "<nil>" ||
			webString(event.Payload["publication_boundary_id"]) == "" {
			t.Fatalf("delta missing canonical publication identity: %#v", event.Payload)
		}
	}
	if !foundDeltas["第一段"] || !foundDeltas["第二段"] {
		t.Fatalf("published deltas=%#v", foundDeltas)
	}
	fence, err = readModel.GetTranscriptWebProjectionFence(
		context.Background(), stream.OwnerID, stream.UID, snapshot.BranchID,
	)
	if err != nil || fence.StateFound {
		t.Fatalf("live delivery synchronously built history projection: fence=%#v err=%v", fence, err)
	}
}

func TestTranscriptFrameMessageSignalsOnlyAfterNewDurableCommit(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-signal", "project-signal", "frame-signal")
	frameContext, found, err := store.GetFrameRealtimeContext("frame-signal")
	if err != nil || !found {
		t.Fatalf("frame context found=%t err=%v", found, err)
	}
	server := &Server{
		workspaceStore: store, transcriptStore: repo,
		transcriptDeliveryWake: make(chan struct{}, 1),
	}
	input := frameMessageSubmission{
		FrameID: "frame-signal", MessageUUID: "message-signal",
		ClientMessageID: "client-signal", Text: "wake after commit",
	}
	if _, idempotent, err := server.submitTranscriptFrameMessage(context.Background(), frameContext, input); err != nil || idempotent {
		t.Fatalf("initial submission idempotent=%t err=%v", idempotent, err)
	}
	select {
	case <-server.transcriptDeliveryWake:
	case <-time.After(time.Second):
		t.Fatal("durable frame commit did not signal delivery coordinator")
	}
	if _, idempotent, err := server.submitTranscriptFrameMessage(context.Background(), frameContext, input); err != nil || !idempotent {
		t.Fatalf("replayed submission idempotent=%t err=%v", idempotent, err)
	}
	select {
	case <-server.transcriptDeliveryWake:
		t.Fatal("idempotent submission signaled without a new durable commit")
	default:
	}
}

func awaitTranscriptWebDeliveryCycle(t *testing.T, cycles <-chan int) int {
	t.Helper()
	select {
	case cycle := <-cycles:
		return cycle
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Transcript Web delivery cycle")
		return 0
	}
}

func TestWebConversationCreationPersistsImmutableTranscriptMapping(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-a", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	server := newV11TestServer(t, Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		closeContext, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Close(closeContext); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	body := []byte(`{"name":"Transcript mapped","extra":{"project_id":"project-a"}}`)
	request := httptest.NewRequest(http.MethodPost, "/api/conversations", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Synon-User-Id", "local")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	frameID := webString(created["id"])
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", frameID)
	if err != nil || !found || stream.SessionID != frameID || stream.FrameID != frameID || stream.ProjectID != "project-a" {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	if _, found, err := repo.GetFrameStreamBySession(context.Background(), "foreign", frameID); err != nil || found {
		t.Fatalf("foreign mapping found=%t err=%v", found, err)
	}
}

func TestTranscriptBackedFrameProjectionDoesNotBlockRunnerAdmissionOnReplayDrain(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-admission", "frame-admission")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		closeContext, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Close(closeContext); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-admission", MessageUUID: "admission-message", ClientMessageID: "admission-client", Text: "run",
	}); err != nil {
		t.Fatal(err)
	}
	frameContext, found, err := store.GetFrameRealtimeContext("frame-admission")
	if err != nil || !found {
		t.Fatalf("frame context found=%t err=%v", found, err)
	}

	// Realtime history replay is a recoverable projection. Holding its worker
	// lock must not prevent the durable workspace dispatch from reaching the
	// Transcript runner admission path.
	server.transcriptDeliveryMu.Lock()
	done := make(chan error, 1)
	go func() {
		done <- server.publishWebFrameEventProjection(frameContext, workspace.FrameEvent{
			ID: "resume-admission", FrameID: "frame-admission", Type: "frame_resume_dispatch_claimed",
			CreatedAt: time.Now().UTC(),
		})
	}()
	select {
	case err := <-done:
		server.transcriptDeliveryMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(250 * time.Millisecond):
		server.transcriptDeliveryMu.Unlock()
		err := <-done
		if err != nil {
			t.Fatalf("projection failed after replay drain was released: %v", err)
		}
		t.Fatal("transcript-backed frame projection blocked runner admission on synchronous replay")
	}
}

func TestTranscriptWebDeliveryStartsEveryRunnerAttemptBeforeItsContent(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-attempt-start", "frame-attempt-start")
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-attempt-start", OwnerID: "local", ExternalID: "frame-attempt-start",
		SessionID: "frame-attempt-start", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-attempt-start", RootFrameID: "frame-attempt-start",
		FrameID: "frame-attempt-start", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "user-attempt-start",
		PayloadJSON: []byte(`{"text":"resume this task"}`), Destinations: []string{transcriptWebDestination},
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-attempt-start",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	if _, _, created, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claimed.Claim, ClientMessageID: "runner-attempt-started",
		Phase: transcriptstore.RunnerPhasePlanning, Resumable: true,
		PayloadJSON:  []byte(`{"detail":"runner chat started","status":"running"}`),
		Destinations: []string{transcriptWebDestination},
	}); err != nil || !created {
		t.Fatalf("append runner start created=%t err=%v", created, err)
	}

	server := &Server{workspaceStore: store, transcriptStore: repo, compatEvents: newCompatEventHub()}
	cycle, err := server.runTranscriptWebDeliveryPass(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cycle.Immediate || cycle.RetryAfter != 0 || cycle.HeartbeatAfter != 0 {
		t.Fatalf("failed intent scheduled an unclaimable background retry: %#v", cycle)
	}
	starts := make([]workspace.RealtimeEvent, 0, 2)
	for _, event := range transcriptWebEvents(t, store, "local") {
		if event.Type == "message.stream" && event.Payload["stream_type"] == "start" {
			starts = append(starts, event)
		}
	}
	if len(starts) != 2 {
		t.Fatalf("start events=%#v", starts)
	}
	for _, start := range starts {
		if fmt.Sprint(start.Payload["source_publication_sequence"]) == "<nil>" ||
			webString(start.Payload["publication_boundary_id"]) == "" {
			t.Fatalf("start missing canonical publication identity: %#v", start.Payload)
		}
	}
	wantMessageID := fmt.Sprintf("assistant-%s-%d", stream.SessionID, claimed.Claim.Attempt)
	if starts[1].Payload["msg_id"] != wantMessageID || starts[1].Payload["conversation_id"] != stream.SessionID {
		t.Fatalf("runner start=%#v want msg_id=%q", starts[1], wantMessageID)
	}
}

func TestTranscriptWebDeliverySkipsInternalCheckpointsWithoutDelayingVisibleDelta(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-live-lane", "frame-live-lane")
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-live-lane", OwnerID: "local", ExternalID: "frame-live-lane",
		SessionID: "frame-live-lane", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-live-lane", RootFrameID: "frame-live-lane", FrameID: "frame-live-lane", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "live-lane-user",
		PayloadJSON: []byte(`{"text":"请连续输出"}`), Destinations: []string{transcriptWebDestination},
	}); err != nil {
		t.Fatal(err)
	}
	runner, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "live-lane-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !runner.Claimed {
		t.Fatalf("runner=%#v err=%v", runner, err)
	}
	appendCheckpoint := func(clientMessageID, payload string) {
		t.Helper()
		if _, _, created, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
			Claim: runner.Claim, ClientMessageID: clientMessageID,
			Phase: transcriptstore.RunnerPhasePlanning, Resumable: true,
			PayloadJSON: []byte(payload), Destinations: []string{transcriptWebDestination},
		}); err != nil || !created {
			t.Fatalf("append checkpoint %q created=%t err=%v", clientMessageID, created, err)
		}
	}
	appendCheckpoint("live-lane-start", `{"detail":"runner chat started","status":"running"}`)
	for index := 0; index < transcriptWebCoordinatorDeliveryBatch; index++ {
		appendCheckpoint(
			fmt.Sprintf("live-lane-internal-%02d", index),
			fmt.Sprintf(`{"detail":"internal preparation %02d","status":"running"}`, index),
		)
	}
	appendCheckpoint(
		"live-lane-tool",
		`{"status":"running","toolPhase":"start","toolCallId":"call-visible","toolName":"Python","toolInput":{"code":"print(1)"}}`,
	)
	if _, created, err := repo.AppendRunnerEvent(context.Background(), transcriptstore.AppendEventInput{
		Claim: runner.Claim, ClientMessageID: "live-lane-delta", Type: "content_delta",
		Source: transcriptstore.EventSourcePayload, PayloadJSON: []byte(`{"text":"第一段正文"}`),
		Destinations: []string{transcriptWebDestination},
	}); err != nil || !created {
		t.Fatalf("append delta created=%t err=%v", created, err)
	}

	server := &Server{workspaceStore: store, transcriptStore: repo, compatEvents: newCompatEventHub()}
	cycle, err := server.runTranscriptWebDeliveryPass(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cycle.Immediate {
		t.Fatalf("bounded live lane did not self-schedule after making progress: %#v", cycle)
	}

	runtimeEvents := 0
	foundDelta := false
	foundToolBoundary := false
	foundToolUpdate := false
	for _, event := range transcriptWebEvents(t, store, stream.OwnerID) {
		if event.Type == "runtime.statusChanged" {
			runtimeEvents++
			if event.Payload["boundary_kind"] == "tool" {
				foundToolBoundary = event.Payload["tool_call_id"] == "call-visible" &&
					event.Payload["tool_name"] == "Python" &&
					fmt.Sprint(event.Payload["source_publication_sequence"]) != "<nil>" &&
					webString(event.Payload["publication_boundary_id"]) != ""
			}
		}
		if event.Type == "message.stream" && event.Payload["stream_type"] == "text" &&
			webString(event.Payload["data"]) == "第一段正文" {
			foundDelta = true
		}
		if event.Type == "message.stream" && event.Payload["stream_type"] == "tool_call" {
			data, _ := event.Payload["data"].(map[string]any)
			foundToolUpdate = data["call_id"] == "call-visible" && data["name"] == "Python" &&
				data["status"] == "running" &&
				webString(event.Payload["msg_id"]) == fmt.Sprintf(
					"transcript-tool:%s:%d:call-visible", stream.UID, runner.Claim.Attempt,
				) &&
				fmt.Sprint(event.Payload["source_publication_sequence"]) != "<nil>" &&
				webString(event.Payload["publication_boundary_id"]) != ""
		}
	}
	if !foundDelta {
		t.Fatal("visible content delta was not published in the same coordinator pass")
	}
	if !foundToolBoundary {
		t.Fatal("durable tool boundary did not publish its explicit history-refresh identity")
	}
	if !foundToolUpdate {
		t.Fatal("durable tool boundary did not publish the tool lifecycle update on message.stream")
	}
	// User admission, runner start and the real tool boundary each publish one
	// runtime transition. The 16 internal preparation checkpoints publish none.
	if runtimeEvents != 3 {
		t.Fatalf("runtime events=%d want=3", runtimeEvents)
	}
}

func TestFrameSubmissionCommitsCanonicalPayloadWithoutLegacyMessageAuthority(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-a", "frame-a")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		closeContext, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Close(closeContext); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	input := frameMessageSubmission{
		FrameID: "frame-a", MessageUUID: "message-a", ClientMessageID: "client-a", Text: "analyze this target",
	}
	frameEvent, idempotent, err := server.submitFrameMessage(store, input)
	if err != nil || idempotent || frameEvent.ID == "" || frameEvent.Type != "user_message" {
		t.Fatalf("submit event=%#v idempotent=%t err=%v", frameEvent, idempotent, err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-a")
	if err != nil || !found || stream.InputRevision != 1 {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	projected, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: "local", ThroughPublicationSequence: stream.NextPublication - 1, Limit: 10,
	})
	if err != nil || len(projected) != 1 || projected[0].Event.Source != transcriptstore.EventSourcePayload ||
		projected[0].Event.FrameEventID != nil || projected[0].Event.ClientMessageID != input.ClientMessageID ||
		transcriptPayloadText(mustTranscriptPayloadObject(t, projected[0].ResolvedPayloadJSON)) != input.Text {
		t.Fatalf("projected=%#v err=%v", projected, err)
	}
	again, idempotent, err := server.submitFrameMessage(store, input)
	if err != nil || !idempotent || again.ID != frameEvent.ID {
		t.Fatalf("idempotent submit event=%#v idempotent=%t err=%v", again, idempotent, err)
	}
	var frameEvents, transcriptEvents, inputRevision int
	if err := db.QueryRow(`SELECT COUNT(*) FROM frame_events WHERE frame_id='frame-a' AND event_type='user_message'`).Scan(&frameEvents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='user_message'`, stream.UID).Scan(&transcriptEvents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT input_revision FROM transcript_streams WHERE stream_uid=?`, stream.UID).Scan(&inputRevision); err != nil {
		t.Fatal(err)
	}
	if frameEvents != 0 || transcriptEvents != 1 || inputRevision != 1 {
		t.Fatalf("durable counts frame=%d transcript=%d revision=%d", frameEvents, transcriptEvents, inputRevision)
	}
	entries, err := server.eventJournal.ReadByClientMessage("frame-a", "client-a")
	if err != nil || len(entries) != 0 {
		t.Fatalf("frame submission wrote legacy JSONL entries=%d err=%v", len(entries), err)
	}
}

func mustTranscriptPayloadObject(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	value, err := transcriptPayloadObject(payload)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestTranscriptContractFailureStopsDeliveryAndRejectsMutationAndReplay(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-a", "frame-existing")
	if _, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-existing", OwnerID: "local", ExternalID: "frame-existing", SessionID: "frame-existing",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project-a", RootFrameID: "frame-existing", FrameID: "frame-existing", Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP INDEX transcript_artifact_refs_version`); err != nil {
		t.Fatal(err)
	}
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		closeContext, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Close(closeContext); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	if !errors.Is(server.transcriptContractErr, transcriptstore.ErrSchemaUnavailable) {
		t.Fatalf("contract error=%v", server.transcriptContractErr)
	}
	if server.transcriptDeliveryDone != nil {
		t.Fatal("delivery worker started with an invalid transcript contract")
	}
	framesBefore, err := store.ListCompatibilityFrames("local", "project-a", false, 100)
	if err != nil {
		t.Fatal(err)
	}
	eventsBefore := transcriptWebEvents(t, store, "local")

	request := httptest.NewRequest(http.MethodPost, "/api/conversations", bytes.NewReader([]byte(
		`{"name":"Must not persist","extra":{"project_id":"project-a"}}`,
	)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Synon-User-Id", "local")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	var createError map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &createError); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(createError, map[string]any{"message": "unable to process conversation request"}) {
		t.Fatalf("create error=%#v", createError)
	}
	framesAfter, err := store.ListCompatibilityFrames("local", "project-a", false, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(framesAfter, framesBefore) {
		t.Fatalf("failed contract mutated frames: before=%#v after=%#v", framesBefore, framesAfter)
	}
	if eventsAfter := transcriptWebEvents(t, store, "local"); !reflect.DeepEqual(eventsAfter, eventsBefore) {
		t.Fatalf("failed contract mutated realtime events: before=%#v after=%#v", eventsBefore, eventsAfter)
	}

	replay := httptest.NewRequest(http.MethodGet, "/api/events?after_sequence=0", nil)
	replay.Header.Set("X-Synon-User-Id", "local")
	replayResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(replayResponse, replay)
	if replayResponse.Code != http.StatusInternalServerError {
		t.Fatalf("replay status=%d body=%s", replayResponse.Code, replayResponse.Body.String())
	}
	var replayError map[string]any
	if err := json.Unmarshal(replayResponse.Body.Bytes(), &replayError); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replayError, map[string]any{"ok": false, "error": "transcript replay preparation failed"}) {
		t.Fatalf("replay error=%#v", replayError)
	}

	history := httptest.NewRequest(http.MethodGet, "/api/conversations/frame-existing/messages?limit=100", nil)
	history.Header.Set("X-Synon-User-Id", "local")
	historyResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(historyResponse, history)
	if historyResponse.Code != http.StatusInternalServerError {
		t.Fatalf("history status=%d body=%s", historyResponse.Code, historyResponse.Body.String())
	}
	var historyError map[string]any
	if err := json.Unmarshal(historyResponse.Body.Bytes(), &historyError); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(historyError, map[string]any{"message": "unable to process conversation request"}) {
		t.Fatalf("history error=%#v", historyError)
	}
}

func TestTranscriptWebHistoryPaginatesLongRunsThroughTerminalSnapshot(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-a", "frame-long")
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-long", OwnerID: "local", ExternalID: "frame-long", SessionID: "frame-long",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project-a", RootFrameID: "frame-long", FrameID: "frame-long", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "user-long", PayloadJSON: []byte(`{"text":"long"}`),
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-long", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	const deltaCount = 1005
	for index := 0; index < deltaCount; index++ {
		if _, created, err := repo.AppendRunnerEvent(context.Background(), transcriptstore.AppendEventInput{
			Claim: claimed.Claim, ClientMessageID: fmt.Sprintf("delta-%04d", index), Type: "content_delta",
			Source: transcriptstore.EventSourcePayload, PayloadJSON: []byte(`{"text":"x"}`),
		}); err != nil || !created {
			t.Fatalf("delta %d created=%t err=%v", index, created, err)
		}
	}
	if _, _, _, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "finish-long", Status: "completed", PayloadJSON: []byte(`{"status":"completed"}`),
	}); err != nil {
		t.Fatal(err)
	}
	server := &Server{workspaceStore: store, transcriptStore: repo}
	messages, found, err := server.loadTranscriptWebHistory(context.Background(), "local", "frame-long")
	if err != nil || !found || len(messages) != 2 {
		t.Fatalf("messages=%d found=%t err=%v", len(messages), found, err)
	}
	assistant := messages[1]
	content, _ := assistant["content"].(map[string]any)
	if assistant["terminal_status"] != "completed" || assistant["status"] != "finish" || len(webString(content["content"])) != deltaCount {
		t.Fatalf("long assistant=%#v content length=%d", assistant, len(webString(content["content"])))
	}
}

func TestTranscriptWebDeliveryUsesTerminalReceiptAndExactArtifactReferences(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-a", "frame-a")
	artifact, version, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-a", ProjectID: "project-a", Name: "result.txt", Kind: "text",
		Content: []byte("result"), CreatedBy: "runner-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO artifact_runtime_metadata(artifact_id,root_frame_id,frame_id) VALUES(?,?,?)`, artifact.ID, "frame-a", "frame-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO artifact_version_provenance(version_id,frame_id,content_type) VALUES(?,?,?)`, version.ID, "frame-a", "text/plain"); err != nil {
		t.Fatal(err)
	}
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-a", OwnerID: "local", ExternalID: "frame-a", SessionID: "frame-a",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project-a", RootFrameID: "frame-a", FrameID: "frame-a", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "user-1",
		PayloadJSON: []byte(`{"text":"build it"}`), Destinations: []string{"ws"},
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-a", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	finalMessageID := transcriptAssistantMessageID(stream.SessionID, claimed.Claim.Attempt, 2)
	if _, refs, _, err := repo.AppendAssistantEventWithArtifacts(context.Background(), transcriptstore.AppendAssistantEventWithArtifactsInput{
		Claim: claimed.Claim, ClientMessageID: "assistant-1", Source: transcriptstore.EventSourcePayload,
		PayloadJSON: []byte(`{"text":"result ready","assistant_segment":{"version":1,"ordinal":2}}`), Destinations: []string{"ws"},
		References: []transcriptstore.ArtifactReferenceInput{{ArtifactID: artifact.ID, VersionID: version.ID, Relation: transcriptstore.ArtifactRelationProduced}},
	}); err != nil || len(refs) != 1 {
		t.Fatalf("assistant refs=%#v err=%v", refs, err)
	}
	if _, _, _, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "finish-1", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed","detail":"done","assistant_segment":{"version":1,"ordinal":2}}`),
	}); err != nil {
		t.Fatal(err)
	}

	server := &Server{workspaceStore: store, transcriptStore: repo, compatEvents: newCompatEventHub()}
	if err := server.drainTranscriptWebDeliveries(context.Background()); err != nil {
		t.Fatal(err)
	}
	events := transcriptWebEvents(t, store, "local")
	var assistantRefs, terminalRefs []any
	var assistantMessageID, terminalMessageID string
	terminalCounts := map[string]int{}
	terminalBoundaries := map[string]bool{}
	terminalSequences := map[string]bool{}
	recordTerminalBoundary := func(payload map[string]any) {
		terminalBoundaries[webString(payload["publication_boundary_id"])] = true
		terminalSequences[fmt.Sprint(payload["source_publication_sequence"])] = true
	}
	for _, event := range events {
		switch event.Type {
		case "message.stream":
			streamType := webString(event.Payload["stream_type"])
			if streamType == "content" {
				assistantRefs, _ = event.Payload["artifact_refs"].([]any)
				assistantMessageID = webString(event.Payload["msg_id"])
			}
			if streamType == "finish" {
				terminalCounts["stream"]++
				recordTerminalBoundary(event.Payload)
				if event.Payload["terminal_status"] != "completed" {
					t.Fatalf("terminal stream=%#v", event.Payload)
				}
				terminalRefs, _ = event.Payload["artifact_refs"].([]any)
				terminalMessageID = webString(event.Payload["msg_id"])
			}
		case "runtime.statusChanged":
			if event.Payload["terminal_status"] == "completed" {
				terminalCounts["runtime"]++
				recordTerminalBoundary(event.Payload)
			}
		case "turn.completed":
			if event.Payload["terminal_status"] == "completed" {
				terminalCounts["turn"]++
				recordTerminalBoundary(event.Payload)
			}
		}
	}
	if !reflect.DeepEqual(assistantRefs, terminalRefs) || len(terminalRefs) != 1 {
		t.Fatalf("assistant refs=%#v terminal refs=%#v", assistantRefs, terminalRefs)
	}
	if assistantMessageID != finalMessageID || terminalMessageID != finalMessageID {
		t.Fatalf("assistant msg_id=%q terminal msg_id=%q want=%q", assistantMessageID, terminalMessageID, finalMessageID)
	}
	if !reflect.DeepEqual(terminalCounts, map[string]int{"stream": 1, "runtime": 1, "turn": 1}) {
		t.Fatalf("terminal counts=%#v events=%#v", terminalCounts, events)
	}
	if len(terminalBoundaries) != 1 || terminalBoundaries[""] || len(terminalSequences) != 1 || terminalSequences["<nil>"] {
		t.Fatalf("terminal boundaries=%#v sequences=%#v events=%#v", terminalBoundaries, terminalSequences, events)
	}
	frameContext, found, err := store.GetFrameRealtimeContext("frame-a")
	if err != nil || !found {
		t.Fatalf("frame context found=%t err=%v", found, err)
	}
	if err := server.publishWebFrameEventProjection(frameContext, workspace.FrameEvent{
		ID: "legacy-terminal", FrameID: "frame-a", Type: "runner_finished",
		Payload: map[string]any{"status": "completed"}, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if after := len(transcriptWebEvents(t, store, "local")); after != len(events) {
		t.Fatalf("mapped frame used duplicate legacy projection: events=%d want=%d", after, len(events))
	}
	request := httptest.NewRequest(http.MethodGet, "/api/conversations/frame-a/messages?limit=100", nil)
	request.Header.Set("X-Synon-User-Id", "local")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("history status=%d body=%s", response.Code, response.Body.String())
	}
	var history struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	var historyRefs []any
	var historyTerminalMessageID string
	for _, message := range history.Items {
		if message["terminal_status"] == "completed" {
			historyRefs, _ = message["artifact_refs"].([]any)
			historyTerminalMessageID = webString(message["id"])
		}
	}
	if !reflect.DeepEqual(historyRefs, terminalRefs) {
		t.Fatalf("history refs=%#v live refs=%#v", historyRefs, terminalRefs)
	}
	messageRequest := httptest.NewRequest(http.MethodGet, "/api/conversations/frame-a/messages/"+historyTerminalMessageID, nil)
	messageRequest.Header.Set("X-Synon-User-Id", "local")
	messageResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(messageResponse, messageRequest)
	if messageResponse.Code != http.StatusOK {
		t.Fatalf("message status=%d body=%s", messageResponse.Code, messageResponse.Body.String())
	}
	var exactMessage map[string]any
	if err := json.Unmarshal(messageResponse.Body.Bytes(), &exactMessage); err != nil {
		t.Fatal(err)
	}
	if refs, _ := exactMessage["artifact_refs"].([]any); !reflect.DeepEqual(refs, terminalRefs) {
		t.Fatalf("exact message refs=%#v live refs=%#v", refs, terminalRefs)
	}
	before := len(events)
	if err := server.drainTranscriptWebDeliveries(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after := len(transcriptWebEvents(t, store, "local")); after != before {
		t.Fatalf("idempotent drain events=%d want=%d", after, before)
	}
	if _, err := db.Exec(`
		DELETE FROM transcript_delivery_intents
		WHERE stream_uid='stream-a' AND publication_seq=(
			SELECT publication_seq FROM transcript_events WHERE stream_uid='stream-a' AND event_type='runner_finished'
		)`); err != nil {
		t.Fatal(err)
	}
	replayRequest := httptest.NewRequest(http.MethodGet, "/api/events?after_sequence=0", nil)
	replayRequest.Header.Set("X-Synon-User-Id", "local")
	replayResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(replayResponse, replayRequest)
	if replayResponse.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", replayResponse.Code, replayResponse.Body.String())
	}
	var recoveredStatus string
	if err := db.QueryRow(`
		SELECT status FROM transcript_delivery_intents
		WHERE stream_uid='stream-a' AND publication_seq=(
			SELECT publication_seq FROM transcript_events WHERE stream_uid='stream-a' AND event_type='runner_finished'
		) AND destination='ws'`).Scan(&recoveredStatus); err != nil || recoveredStatus != "delivered" {
		t.Fatalf("recovered terminal status=%q err=%v", recoveredStatus, err)
	}
	if _, err := db.Exec(`DELETE FROM artifact_versions WHERE id=?`, version.ID); err != nil {
		t.Fatal(err)
	}
	reload := httptest.NewRequest(http.MethodGet, "/api/conversations/frame-a/messages?limit=100", nil)
	reload.Header.Set("X-Synon-User-Id", "local")
	reloaded := httptest.NewRecorder()
	server.Handler().ServeHTTP(reloaded, reload)
	if reloaded.Code != http.StatusOK {
		t.Fatalf("reload status=%d body=%s", reloaded.Code, reloaded.Body.String())
	}
	var tombstoned struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(reloaded.Body.Bytes(), &tombstoned); err != nil {
		t.Fatal(err)
	}
	foundDeleted := false
	for _, message := range tombstoned.Items {
		refs, _ := message["artifact_refs"].([]any)
		for _, value := range refs {
			ref, _ := value.(map[string]any)
			foundDeleted = foundDeleted || ref["availability"] == "deleted"
		}
	}
	if !foundDeleted {
		t.Fatalf("deleted version did not retain its typed tombstone: %#v", tombstoned.Items)
	}
}

func TestTranscriptWebDeliveryCycleRecoversTerminalIntentCreatedAfterStartup(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-late", "frame-late")
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-late", OwnerID: "local", ExternalID: "frame-late", SessionID: "frame-late",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project-late", RootFrameID: "frame-late", FrameID: "frame-late", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "user-late", PayloadJSON: []byte(`{"text":"work"}`),
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-late", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	terminal, _, _, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "finish-late", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM transcript_delivery_intents WHERE stream_uid=? AND publication_seq=?`, stream.UID, terminal.PublicationSeq); err != nil {
		t.Fatal(err)
	}
	server := &Server{workspaceStore: store, transcriptStore: repo, compatEvents: newCompatEventHub()}
	if err := server.runTranscriptWebDeliveryCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	terminalCount := 0
	for _, event := range transcriptWebEvents(t, store, "local") {
		if event.Type == "message.stream" && event.Payload["terminal_status"] == "completed" {
			terminalCount++
		}
	}
	if terminalCount != 1 {
		t.Fatalf("terminal stream count=%d", terminalCount)
	}
}

func TestTranscriptWebReplayRecoversFailedIntentAndUnblocksTheStreamExactlyOnce(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-a", "project-recover", "frame-recover")
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-recover", OwnerID: "owner-a", ExternalID: "frame-recover", SessionID: "frame-recover",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project-recover", RootFrameID: "frame-recover", FrameID: "frame-recover", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= 2; index++ {
		if _, _, err := repo.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: fmt.Sprintf("user-%d", index),
			PayloadJSON: []byte(fmt.Sprintf(`{"text":"work-%d"}`, index)), Destinations: []string{transcriptWebDestination},
		}); err != nil {
			t.Fatal(err)
		}
	}
	claim, err := repo.ClaimNextDelivery(context.Background(), transcriptstore.ClaimDeliveryInput{
		OwnerID: stream.OwnerID, Destination: transcriptWebDestination, WorkerID: "failure-fixture", TTL: time.Minute,
	})
	if err != nil || !claim.Claimed || claim.Claim.PublicationSeq != 1 {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	if result, err := repo.FailDelivery(context.Background(), transcriptstore.FailDeliveryInput{
		Claim: claim.Claim, ErrorCode: "mapping_unresolved", MaxAttempts: 1,
	}); err != nil || result.Status != "failed" {
		t.Fatalf("fail=%#v err=%v", result, err)
	}
	server := &Server{workspaceStore: store, transcriptStore: repo, compatEvents: newCompatEventHub()}
	if err := server.runTranscriptWebDeliveryCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	cycle, err := server.runTranscriptWebDeliveryPass(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cycle.Immediate || cycle.RetryAfter != 0 || cycle.HeartbeatAfter != 0 {
		t.Fatalf("failed frontier scheduled blocked followers for background retry: %#v", cycle)
	}
	if events := transcriptWebEvents(t, store, "owner-a"); len(events) != 0 {
		t.Fatalf("background cycle retried failed intent: %#v", events)
	}
	if err := server.prepareTranscriptWebReplay(context.Background(), "owner-b"); err != nil {
		t.Fatal(err)
	}
	if events := transcriptWebEvents(t, store, "owner-a"); len(events) != 0 {
		t.Fatalf("foreign replay recovered owner-a intent: %#v", events)
	}
	if err := server.prepareTranscriptWebReplay(context.Background(), "owner-a"); err != nil {
		rows, queryErr := db.Query(`SELECT publication_seq,status,attempt_count,last_max_attempts FROM transcript_delivery_intents WHERE stream_uid=? ORDER BY publication_seq`, stream.UID)
		if queryErr != nil {
			t.Fatalf("prepare replay: %v; inspect intents: %v", err, queryErr)
		}
		defer rows.Close()
		var states []string
		for rows.Next() {
			var publication, attempts, maxAttempts int
			var status string
			if scanErr := rows.Scan(&publication, &status, &attempts, &maxAttempts); scanErr != nil {
				t.Fatal(scanErr)
			}
			states = append(states, fmt.Sprintf("%d:%s:%d:%d", publication, status, attempts, maxAttempts))
		}
		t.Fatalf("prepare replay: %v; intents=%v", err, states)
	}
	events := transcriptWebEvents(t, store, "owner-a")
	if len(events) != 6 {
		t.Fatalf("replayed events=%d want=6: %#v", len(events), events)
	}
	counts := map[string]int{}
	for _, event := range events {
		counts[event.Type]++
		if event.Type == "runtime.statusChanged" && event.Payload["phase"] != "waiting_for_lock" {
			t.Fatalf("runtime event=%#v", event)
		}
	}
	if counts["message.userCreated"] != 2 || counts["message.stream"] != 2 || counts["runtime.statusChanged"] != 2 {
		t.Fatalf("event counts=%#v", counts)
	}
	if err := server.prepareTranscriptWebReplay(context.Background(), "owner-a"); err != nil {
		t.Fatal(err)
	}
	if after := transcriptWebEvents(t, store, "owner-a"); len(after) != len(events) {
		t.Fatalf("duplicate replay events=%d want=%d", len(after), len(events))
	}
	first, err := repo.GetDeliveryIntent(context.Background(), "owner-a", stream.UID, 1, transcriptWebDestination, 1)
	if err != nil || first.Status != "delivered" || first.AttemptCount != 2 || first.LastErrorCode != "mapping_unresolved" {
		t.Fatalf("first intent=%#v err=%v", first, err)
	}
	second, err := repo.GetDeliveryIntent(context.Background(), "owner-a", stream.UID, 2, transcriptWebDestination, 1)
	if err != nil || second.Status != "delivered" || second.AttemptCount != 1 {
		t.Fatalf("second intent=%#v err=%v", second, err)
	}
}

func TestTranscriptWebDeliverySelfRepairsProjectionFailure(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-auto", "project-auto", "frame-auto")
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-auto", OwnerID: "owner-auto", ExternalID: "frame-auto", SessionID: "frame-auto",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project-auto",
		RootFrameID: "frame-auto", FrameID: "frame-auto", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "auto-user",
		PayloadJSON: []byte(`{"text":"self repair"}`), Destinations: []string{transcriptWebDestination},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "auto-follower",
		PayloadJSON: []byte(`{"text":"must remain ordered"}`), Destinations: []string{transcriptWebDestination},
	}); err != nil {
		t.Fatal(err)
	}
	claim, err := repo.ClaimNextDelivery(context.Background(), transcriptstore.ClaimDeliveryInput{
		OwnerID: stream.OwnerID, Destination: transcriptWebDestination, WorkerID: "auto-failure", TTL: time.Minute,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	if result, err := repo.FailDelivery(context.Background(), transcriptstore.FailDeliveryInput{
		Claim: claim.Claim, ErrorCode: "projection_stale", MaxAttempts: 1,
	}); err != nil || result.Status != "failed" {
		t.Fatalf("fail=%#v err=%v", result, err)
	}
	if _, err := db.Exec(`UPDATE transcript_delivery_intents SET attempt_count=?,last_max_attempts=?
		WHERE stream_uid=? AND publication_seq=1 AND destination=?`,
		transcriptProjectionStaleMaxAttempts, transcriptProjectionStaleMaxAttempts,
		stream.UID, transcriptWebDestination); err != nil {
		t.Fatal(err)
	}
	if recovered, err := repo.RecoverRetryableWebProjectionDeliveryIntents(context.Background(), 10); err != nil || recovered != 0 {
		t.Fatalf("recovery before projection ready=%d err=%v", recovered, err)
	}

	server := &Server{
		workspaceStore: store, transcriptStore: repo,
		transcriptWebReadModel: transcriptstore.NewWebReadModelRepository(db, db),
		compatEvents:           newCompatEventHub(),
	}
	if err := server.runTranscriptWebReadModelCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if recovered, err := repo.RecoverRetryableWebProjectionDeliveryIntents(context.Background(), 10); err != nil || recovered != 1 {
		t.Fatalf("recover completed marker=%d err=%v", recovered, err)
	}
	retry, err := repo.ClaimNextDelivery(context.Background(), transcriptstore.ClaimDeliveryInput{
		OwnerID: stream.OwnerID, Destination: transcriptWebDestination, WorkerID: "previous-server", TTL: time.Minute,
	})
	if err != nil || !retry.Claimed || retry.Claim.AttemptCount != transcriptProjectionStaleMaxAttempts+1 {
		t.Fatalf("recovered claim=%#v err=%v", retry, err)
	}
	if err := server.publishTranscriptWebClaim(context.Background(), retry.Claim); err != nil {
		t.Fatal(err)
	}
	if result, err := repo.AcknowledgeDelivery(
		context.Background(), transcriptstore.AcknowledgeDeliveryInput{Claim: retry.Claim},
	); err != nil || result.Status != "delivered" {
		t.Fatalf("ack recovered marker=%#v err=%v", result, err)
	}
	cycle, err := server.runTranscriptWebDeliveryPass(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cycle.Immediate || cycle.RetryAfter != 0 || cycle.HeartbeatAfter != 0 {
		t.Fatalf("settled cycle=%#v", cycle)
	}
	intent, err := repo.GetDeliveryIntent(context.Background(), stream.OwnerID, stream.UID, 1, transcriptWebDestination, 1)
	if err != nil || intent.Status != "delivered" ||
		intent.AttemptCount != transcriptProjectionStaleMaxAttempts+1 || intent.LastErrorCode != "projection_stale" {
		t.Fatalf("intent=%#v err=%v", intent, err)
	}
	follower, err := repo.GetDeliveryIntent(context.Background(), stream.OwnerID, stream.UID, 2, transcriptWebDestination, 1)
	if err != nil || follower.Status != "delivered" || follower.AttemptCount != 1 {
		t.Fatalf("follower=%#v err=%v", follower, err)
	}
	if events := transcriptWebEvents(t, store, stream.OwnerID); len(events) != 6 {
		t.Fatalf("events=%d want=6: %#v", len(events), events)
	}
}

func TestTranscriptWebDeliveryRebasesCompletedIncrementalBacklogToFinalReplacement(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-rebase", "project-rebase", "frame-rebase")
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-rebase", OwnerID: "owner-rebase", ExternalID: "frame-rebase", SessionID: "frame-rebase",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project-rebase",
		RootFrameID: "frame-rebase", FrameID: "frame-rebase", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "rebase-user",
		PayloadJSON: []byte(`{"text":"long task"}`), Destinations: []string{transcriptWebDestination},
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-rebase", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	const deltaCount = 200
	for index := range deltaCount {
		if _, created, err := repo.AppendRunnerEvent(context.Background(), transcriptstore.AppendEventInput{
			Claim: claimed.Claim, ClientMessageID: fmt.Sprintf("rebase-delta-%03d", index),
			Type: "content_delta", Source: transcriptstore.EventSourcePayload,
			PayloadJSON: []byte(`{"text":"x"}`), Destinations: []string{transcriptWebDestination},
		}); err != nil || !created {
			t.Fatalf("append delta %d created=%t err=%v", index, created, err)
		}
	}
	assistant, created, err := repo.AppendRunnerEvent(context.Background(), transcriptstore.AppendEventInput{
		Claim: claimed.Claim, ClientMessageID: "rebase-assistant", Type: "assistant_message",
		Source: transcriptstore.EventSourcePayload, PayloadJSON: []byte(`{"text":"final replacement"}`),
		Destinations: []string{transcriptWebDestination},
	})
	if err != nil || !created {
		t.Fatalf("append assistant created=%t err=%v", created, err)
	}
	terminal, _, created, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "rebase-finish", Status: "completed",
		PayloadJSON:  []byte(`{"status":"completed","detail":"done"}`),
		Destinations: []string{transcriptWebDestination},
	})
	if err != nil || !created {
		t.Fatalf("finish created=%t err=%v", created, err)
	}

	first, err := repo.ClaimNextDelivery(context.Background(), transcriptstore.ClaimDeliveryInput{
		OwnerID: stream.OwnerID, Destination: transcriptWebDestination, WorkerID: "rebase-failure", TTL: time.Minute,
	})
	if err != nil || !first.Claimed || first.Claim.PublicationSeq != 1 {
		t.Fatalf("first delivery=%#v err=%v", first, err)
	}
	if result, err := repo.FailDelivery(context.Background(), transcriptstore.FailDeliveryInput{
		Claim: first.Claim, ErrorCode: "projection_stale", MaxAttempts: 1,
	}); err != nil || result.Status != "failed" {
		t.Fatalf("fail=%#v err=%v", result, err)
	}
	if _, err := db.Exec(`UPDATE transcript_delivery_intents SET attempt_count=?,last_max_attempts=?
		WHERE stream_uid=? AND publication_seq=1 AND destination=?`,
		transcriptProjectionStaleMaxAttempts, transcriptProjectionStaleMaxAttempts,
		stream.UID, transcriptWebDestination); err != nil {
		t.Fatal(err)
	}

	server := &Server{
		workspaceStore: store, transcriptStore: repo,
		transcriptWebReadModel: transcriptstore.NewWebReadModelRepository(db, db),
		compatEvents:           newCompatEventHub(),
	}
	cycle, err := server.runTranscriptWebDeliveryPass(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cycle.Immediate || cycle.RetryAfter != 0 || cycle.HeartbeatAfter != 0 {
		t.Fatalf("settled cycle=%#v", cycle)
	}
	var delivered, unsettled, untouchedDeltas int
	if err := db.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN intent.status='delivered' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN intent.status NOT IN ('delivered','revoked') THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN event.event_type='content_delta' AND intent.attempt_count=0 THEN 1 ELSE 0 END),0)
		FROM transcript_delivery_intents intent
		JOIN transcript_events event ON event.stream_uid=intent.stream_uid
			AND event.publication_seq=intent.publication_seq
		WHERE intent.stream_uid=? AND intent.destination=?`, stream.UID, transcriptWebDestination).
		Scan(&delivered, &unsettled, &untouchedDeltas); err != nil {
		t.Fatal(err)
	}
	if delivered != deltaCount+3 || unsettled != 0 || untouchedDeltas != deltaCount {
		t.Fatalf("delivered=%d unsettled=%d untouched_deltas=%d", delivered, unsettled, untouchedDeltas)
	}
	assistantIntent, err := repo.GetDeliveryIntent(
		context.Background(), stream.OwnerID, stream.UID, assistant.PublicationSeq, transcriptWebDestination, 1,
	)
	if err != nil || assistantIntent.AttemptCount != 1 || assistantIntent.Status != "delivered" {
		t.Fatalf("assistant intent=%#v err=%v", assistantIntent, err)
	}
	terminalIntent, err := repo.GetDeliveryIntent(
		context.Background(), stream.OwnerID, stream.UID, terminal.PublicationSeq, transcriptWebDestination, 1,
	)
	if err != nil || terminalIntent.AttemptCount != 1 || terminalIntent.Status != "delivered" {
		t.Fatalf("terminal intent=%#v err=%v", terminalIntent, err)
	}
	events := transcriptWebEvents(t, store, stream.OwnerID)
	if len(events) != 7 {
		t.Fatalf("events=%d want=7: %#v", len(events), events)
	}
	for _, event := range events {
		if event.Type == "message.stream" && event.Payload["stream_type"] == "text" {
			t.Fatalf("obsolete delta was replayed: %#v", event)
		}
	}
}

func TestTranscriptWebDeliveryRebasesCompletedBacklogAfterRecoveredMarkerRetryFails(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-rebase-retry", "project-rebase-retry", "frame-rebase-retry")
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-rebase-retry", OwnerID: "owner-rebase-retry", ExternalID: "frame-rebase-retry",
		SessionID: "frame-rebase-retry", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-rebase-retry", RootFrameID: "frame-rebase-retry",
		FrameID: "frame-rebase-retry", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "rebase-retry-user",
		PayloadJSON: []byte(`{"text":"long task"}`), Destinations: []string{transcriptWebDestination},
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-rebase-retry",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	const deltaCount = 20
	for index := range deltaCount {
		if _, created, err := repo.AppendRunnerEvent(context.Background(), transcriptstore.AppendEventInput{
			Claim: claimed.Claim, ClientMessageID: fmt.Sprintf("rebase-retry-delta-%03d", index),
			Type: "content_delta", Source: transcriptstore.EventSourcePayload,
			PayloadJSON: []byte(`{"text":"x"}`), Destinations: []string{transcriptWebDestination},
		}); err != nil || !created {
			t.Fatalf("append delta %d created=%t err=%v", index, created, err)
		}
	}
	assistant, created, err := repo.AppendRunnerEvent(context.Background(), transcriptstore.AppendEventInput{
		Claim: claimed.Claim, ClientMessageID: "rebase-retry-assistant", Type: "assistant_message",
		Source: transcriptstore.EventSourcePayload, PayloadJSON: []byte(`{"text":"final replacement"}`),
		Destinations: []string{transcriptWebDestination},
	})
	if err != nil || !created {
		t.Fatalf("append assistant created=%t err=%v", created, err)
	}
	terminal, _, created, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "rebase-retry-finish", Status: "completed",
		PayloadJSON:  []byte(`{"status":"completed","detail":"done"}`),
		Destinations: []string{transcriptWebDestination},
	})
	if err != nil || !created {
		t.Fatalf("finish created=%t err=%v", created, err)
	}
	claimDelivery := func(workerID string) transcriptstore.DeliveryClaim {
		t.Helper()
		result, err := repo.ClaimNextDelivery(context.Background(), transcriptstore.ClaimDeliveryInput{
			OwnerID: stream.OwnerID, Destination: transcriptWebDestination,
			WorkerID: workerID, TTL: time.Minute,
		})
		if err != nil || !result.Claimed {
			t.Fatalf("delivery claim=%#v err=%v", result, err)
		}
		return result.Claim
	}

	user := claimDelivery("rebase-retry-user-delivery")
	if user.PublicationSeq != 1 {
		t.Fatalf("user delivery=%#v", user)
	}
	if _, err := repo.AcknowledgeDelivery(
		context.Background(), transcriptstore.AcknowledgeDeliveryInput{Claim: user},
	); err != nil {
		t.Fatal(err)
	}
	marker := claimDelivery("rebase-retry-marker")
	if marker.PublicationSeq != 2 {
		t.Fatalf("marker delivery=%#v", marker)
	}
	if result, err := repo.FailDelivery(context.Background(), transcriptstore.FailDeliveryInput{
		Claim: marker, ErrorCode: "projection_stale", MaxAttempts: 1,
	}); err != nil || result.Status != "failed" {
		t.Fatalf("marker failure=%#v err=%v", result, err)
	}
	if _, err := db.Exec(`UPDATE transcript_delivery_intents SET attempt_count=?,last_max_attempts=?
		WHERE stream_uid=? AND publication_seq=2 AND destination=?`,
		transcriptProjectionStaleMaxAttempts, transcriptProjectionStaleMaxAttempts,
		stream.UID, transcriptWebDestination); err != nil {
		t.Fatal(err)
	}

	server := &Server{
		workspaceStore: store, transcriptStore: repo,
		transcriptWebReadModel: transcriptstore.NewWebReadModelRepository(db, db),
		compatEvents:           newCompatEventHub(),
	}
	if err := server.runTranscriptWebReadModelCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if recovered, err := repo.RecoverRetryableWebProjectionDeliveryIntents(context.Background(), 10); err != nil || recovered != 1 {
		t.Fatalf("recover marker=%d err=%v", recovered, err)
	}
	retry := claimDelivery("rebase-retry-failed-retry")
	if retry.PublicationSeq != 2 || retry.AttemptCount != transcriptProjectionStaleMaxAttempts+1 {
		t.Fatalf("retry delivery=%#v", retry)
	}
	if result, err := repo.FailDelivery(context.Background(), transcriptstore.FailDeliveryInput{
		Claim: retry, ErrorCode: "projection_failed", MaxAttempts: transcriptProjectionStaleMaxAttempts,
	}); err != nil || result.Status != "failed" {
		t.Fatalf("retry failure=%#v err=%v", result, err)
	}

	cycle, err := server.runTranscriptWebDeliveryPass(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cycle.Immediate || cycle.RetryAfter != 0 || cycle.HeartbeatAfter != 0 {
		t.Fatalf("settled cycle=%#v", cycle)
	}
	var unsettled int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_delivery_intents
		WHERE stream_uid=? AND destination=? AND status NOT IN ('delivered','revoked')`,
		stream.UID, transcriptWebDestination).Scan(&unsettled); err != nil {
		t.Fatal(err)
	}
	if unsettled != 0 {
		t.Fatalf("unsettled=%d", unsettled)
	}
	markerIntent, err := repo.GetDeliveryIntent(
		context.Background(), stream.OwnerID, stream.UID, 2, transcriptWebDestination, 1,
	)
	if err != nil || markerIntent.Status != "delivered" ||
		markerIntent.AttemptCount != transcriptProjectionStaleMaxAttempts+1 {
		t.Fatalf("marker intent=%#v err=%v", markerIntent, err)
	}
	for _, publicationSeq := range []int64{assistant.PublicationSeq, terminal.PublicationSeq} {
		intent, err := repo.GetDeliveryIntent(
			context.Background(), stream.OwnerID, stream.UID, publicationSeq, transcriptWebDestination, 1,
		)
		if err != nil || intent.Status != "delivered" || intent.AttemptCount != 1 {
			t.Fatalf("terminal intent seq=%d intent=%#v err=%v", publicationSeq, intent, err)
		}
	}
	for _, event := range transcriptWebEvents(t, store, stream.OwnerID) {
		if event.Type == "message.stream" && event.Payload["stream_type"] == "text" {
			t.Fatalf("obsolete delta was replayed: %#v", event)
		}
	}
}

func TestTranscriptWebDeliveryAcknowledgesMachineOnlyToolRecoveryWithoutProjection(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-machine", "project-machine", "frame-machine")
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-machine", OwnerID: "owner-machine", ExternalID: "frame-machine", SessionID: "frame-machine",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project-machine",
		RootFrameID: "frame-machine", FrameID: "frame-machine", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "machine-fixture",
		PayloadJSON: []byte(`{"text":"fixture"}`), Destinations: []string{transcriptWebDestination},
	}); err != nil {
		t.Fatal(err)
	}
	machinePayload := `{"version":1,"status":"completed","toolCallId":"tool-call-1","toolPhase":"completed","toolResult":{"ok":true}}`
	if _, err := db.Exec(`UPDATE transcript_events
		SET event_type=?,client_message_id='terminal-tool-recovery:test',payload_json=?
		WHERE stream_uid=? AND publication_seq=1`,
		transcriptstore.TerminalToolRecoveryEventType, machinePayload, stream.UID); err != nil {
		t.Fatal(err)
	}
	server := &Server{workspaceStore: store, transcriptStore: repo, compatEvents: newCompatEventHub()}
	if err := server.runTranscriptWebDeliveryCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	intent, err := repo.GetDeliveryIntent(
		context.Background(), stream.OwnerID, stream.UID, 1, transcriptWebDestination, 1,
	)
	if err != nil || intent.Status != "delivered" || intent.AttemptCount != 1 {
		t.Fatalf("machine-only intent=%#v err=%v", intent, err)
	}
	if events := transcriptWebEvents(t, store, stream.OwnerID); len(events) != 0 {
		t.Fatalf("machine-only event leaked into the web projection: %#v", events)
	}
}

func TestTranscriptWebReplayRejectsTerminalDeliveryPoison(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-poison", "project-poison", "frame-poison")
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-poison", OwnerID: "owner-poison", ExternalID: "frame-poison", SessionID: "frame-poison",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project-poison", RootFrameID: "frame-poison", FrameID: "frame-poison", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "poison-user",
		PayloadJSON: []byte(`{"text":"work"}`), Destinations: []string{transcriptWebDestination},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE transcript_delivery_intents
		SET status='failed',attempt_count=100,last_max_attempts=100,last_error_code='projection_failed'
		WHERE stream_uid=? AND destination=?`, stream.UID, transcriptWebDestination); err != nil {
		t.Fatal(err)
	}
	server := &Server{workspaceStore: store, transcriptStore: repo, compatEvents: newCompatEventHub()}
	if err := server.prepareTranscriptWebReplay(context.Background(), stream.OwnerID); !errors.Is(err, errTranscriptWebReplayPoisoned) {
		t.Fatalf("prepare poison error=%v", err)
	}
	cycle, err := server.runTranscriptWebDeliveryPass(context.Background())
	if err != nil {
		t.Fatalf("background poison pass error=%v", err)
	}
	if cycle.Immediate || cycle.RetryAfter != 0 || cycle.HeartbeatAfter != 0 {
		t.Fatalf("poisoned intent scheduled an unclaimable background retry: %#v", cycle)
	}
	if events := transcriptWebEvents(t, store, stream.OwnerID); len(events) != 0 {
		t.Fatalf("poisoned replay events=%#v", events)
	}
}

func TestDeletedFrameRefIntentDoesNotPoisonOwnerReplay(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-a", "project-delete", "frame-delete")
	app := &Server{workspaceStore: store, transcriptStore: repo, compatEvents: newCompatEventHub()}
	if _, _, err := app.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-delete", MessageUUID: "message-delete", ClientMessageID: "client-delete", Text: "obsolete work",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "owner-a", "frame-delete")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	frame, found, err := store.GetFrame("frame-delete")
	if err != nil || !found {
		t.Fatalf("deleted frame found=%t err=%v", found, err)
	}
	if _, err := store.DeleteCompatibilityFrameTree("frame-delete", "owner-a", frame.IncarnationID); err != nil {
		t.Fatal(err)
	}
	if err := app.prepareTranscriptWebReplay(context.Background(), "owner-a"); err != nil {
		t.Fatalf("deleted transcript poisoned owner replay: %v", err)
	}
	if _, found, err := repo.GetFrameStreamBySession(context.Background(), "owner-a", "frame-delete"); err != nil || found {
		t.Fatalf("deleted stream found=%t err=%v", found, err)
	}
	var deleteOutbox int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM workspace_outbox
		WHERE topic='workspace.realtime' AND event_type='frame_update' AND aggregate_type='frame'
			AND aggregate_id='frame-delete' AND json_extract(payload_json,'$.event.payload.action')='deleted'`,
	).Scan(&deleteOutbox); err != nil {
		t.Fatal(err)
	}
	if deleteOutbox != 1 {
		t.Fatalf("durable frame-delete successors=%d", deleteOutbox)
	}

	seedTranscriptWebFrame(t, store, "owner-a", "project-after-delete", "frame-after-delete")
	if _, _, err := app.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-after-delete", MessageUUID: "message-after-delete",
		ClientMessageID: "client-after-delete", Text: "unrelated work",
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.prepareTranscriptWebReplay(context.Background(), "owner-a"); err != nil {
		t.Fatalf("unrelated replay after delete: %v", err)
	}
}

func TestDeletedProjectRetiresTranscriptOwnerReplay(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-project", "project-delete-all", "frame-delete-all")
	app := &Server{workspaceStore: store, transcriptStore: repo, compatEvents: newCompatEventHub()}
	if _, _, err := app.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-delete-all", MessageUUID: "message-delete-all",
		ClientMessageID: "client-delete-all", Text: "project work",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteProjectRealtime(
		context.Background(), "project-delete-all", "owner-project", "realtime-project-delete-all",
	); err != nil {
		t.Fatal(err)
	}
	if err := app.prepareTranscriptWebReplay(context.Background(), "owner-project"); err != nil {
		t.Fatalf("deleted project poisoned owner replay: %v", err)
	}
	if _, found, err := repo.GetFrameStreamBySession(context.Background(), "owner-project", "frame-delete-all"); err != nil || found {
		t.Fatalf("deleted project stream found=%t err=%v", found, err)
	}
	var deleteOutbox int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM workspace_outbox
		WHERE topic='workspace.realtime' AND event_type='project_deleted'
			AND aggregate_type='project' AND aggregate_id='project-delete-all'`,
	).Scan(&deleteOutbox); err != nil {
		t.Fatal(err)
	}
	if deleteOutbox != 1 {
		t.Fatalf("durable project-delete successors=%d", deleteOutbox)
	}
}

func TestWorkspaceFrameDeleteRetiresExactTranscriptStream(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-raw", "project-raw-delete", "frame-raw-delete")
	app := New(Options{Workspace: store, Transcript: repo})
	t.Cleanup(func() { _ = app.Close(context.Background()) })
	if _, _, err := app.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-raw-delete", MessageUUID: "message-raw-delete",
		ClientMessageID: "client-raw-delete", Text: "raw frame work",
	}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodDelete, "/api/go/frames/frame-raw-delete", nil)
	request.Header.Set("X-Synon-User-Id", "owner-raw")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", response.Code, response.Body.String())
	}
	if _, found, err := repo.GetFrameStreamBySession(context.Background(), "owner-raw", "frame-raw-delete"); err != nil || found {
		t.Fatalf("raw deleted stream found=%t err=%v", found, err)
	}
	if err := app.prepareTranscriptWebReplay(context.Background(), "owner-raw"); err != nil {
		t.Fatalf("raw delete poisoned owner replay: %v", err)
	}
}

func TestCompatibilityDeleteRollsBackTranscriptWhenDeleteSuccessorFails(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-rollback", "project-rollback", "frame-rollback")
	app := &Server{workspaceStore: store, transcriptStore: repo, compatEvents: newCompatEventHub()}
	if _, _, err := app.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-rollback", MessageUUID: "message-rollback",
		ClientMessageID: "client-rollback", Text: "must survive rollback",
	}); err != nil {
		t.Fatal(err)
	}
	frame, found, err := store.GetFrame("frame-rollback")
	if err != nil || !found {
		t.Fatalf("frame found=%t err=%v", found, err)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_frame_delete_successor
		BEFORE INSERT ON workspace_outbox
		WHEN NEW.topic='workspace.realtime'
			AND json_extract(NEW.payload_json,'$.event.payload.action')='deleted'
		BEGIN SELECT RAISE(ABORT,'forced delete successor failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteCompatibilityFrameTree(
		frame.ID, "owner-rollback", frame.IncarnationID,
	); err == nil {
		t.Fatal("delete unexpectedly committed without its durable successor")
	}
	if _, found, err := store.GetFrame(frame.ID); err != nil || !found {
		t.Fatalf("rolled-back frame found=%t err=%v", found, err)
	}
	if _, found, err := repo.GetFrameStreamBySession(
		context.Background(), "owner-rollback", frame.ID,
	); err != nil || !found {
		t.Fatalf("rolled-back transcript found=%t err=%v", found, err)
	}
}

func TestTranscriptWebDeliveryFreezesTerminalStatusMappingAndRejectsForeignMapping(t *testing.T) {
	for _, test := range []struct {
		status         string
		wantStreamType string
		wantTurnStatus string
	}{
		{status: "completed", wantStreamType: "finish", wantTurnStatus: "finished"},
		{status: "cancelled", wantStreamType: "finish", wantTurnStatus: "cancelled"},
		{status: "failed", wantStreamType: "error", wantTurnStatus: "error"},
	} {
		t.Run(test.status, func(t *testing.T) {
			store, repo, _ := newTranscriptWebFixture(t)
			seedTranscriptWebFrame(t, store, "owner-a", "project-a", "frame-a")
			stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
				UID: "stream-a", OwnerID: "owner-a", ExternalID: "frame-a", SessionID: "frame-a",
				Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project-a", RootFrameID: "frame-a", FrameID: "frame-a", Epoch: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := repo.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
				StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "user-1", PayloadJSON: []byte(`{"text":"work"}`),
			}); err != nil {
				t.Fatal(err)
			}
			claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
				StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-a", TTL: time.Minute,
				ResumeSource: transcriptstore.ResumeSourceFresh,
			})
			if err != nil {
				t.Fatal(err)
			}
			if test.status == "cancelled" {
				if result, err := repo.CancelRunner(context.Background(), transcriptstore.CancelRunnerInput{
					StreamUID: stream.UID, OwnerID: stream.OwnerID, ExpectedAttempt: claimed.Claim.Attempt,
					ClientMessageID: "cancel-1", ReasonCode: "user_cancelled",
				}); err != nil || !result.Applied {
					t.Fatalf("cancel=%#v err=%v", result, err)
				}
			} else if _, _, _, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
				Claim: claimed.Claim, ClientMessageID: "finish-1", Status: test.status,
				PayloadJSON: []byte(`{"status":"` + test.status + `"}`),
			}); err != nil {
				t.Fatal(err)
			}
			server := &Server{workspaceStore: store, transcriptStore: repo, compatEvents: newCompatEventHub()}
			if err := server.drainTranscriptWebDeliveries(context.Background()); err != nil {
				t.Fatal(err)
			}
			var streamType, turnStatus string
			for _, event := range transcriptWebEvents(t, store, "owner-a") {
				if event.Type == "message.stream" && event.Payload["terminal_status"] == test.status {
					streamType = webString(event.Payload["stream_type"])
				}
				if event.Type == "turn.completed" && event.Payload["terminal_status"] == test.status {
					turnStatus = webString(event.Payload["status"])
				}
			}
			if streamType != test.wantStreamType || turnStatus != test.wantTurnStatus {
				t.Fatalf("stream=%q turn=%q", streamType, turnStatus)
			}

			if _, err := repo.GetStream(context.Background(), stream.UID, "owner-b"); !errors.Is(err, transcriptstore.ErrOwnerMismatch) {
				t.Fatalf("foreign stream error=%v", err)
			}
		})
	}
}

func TestActivatedLegacyAskUserHistoryRemainsProjectable(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-rebase", "frame-rebase")
	ctx := context.Background()
	stream, err := repo.CreateStream(ctx, transcriptstore.CreateStreamInput{
		UID: "frame:frame-rebase", OwnerID: "local", ExternalID: "frame-rebase", SessionID: "frame-rebase",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project-rebase", RootFrameID: "frame-rebase",
		FrameID: "frame-rebase", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	forceLegacyTranscriptFrameAuthority(t, db, stream)
	for index := 0; index < 8; index++ {
		suffix := fmt.Sprintf("%02d", index)
		if _, _, _, err := repo.AppendFrameUserEvent(ctx, transcriptstore.AppendFrameUserEventInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "rebase-client-" + suffix,
			FrameEventID: "rebase-frame-" + suffix, MessageUUID: "rebase-message-" + suffix, Text: "history " + suffix,
		}); err != nil {
			t.Fatal(err)
		}
	}
	claimed, err := repo.ClaimRunner(ctx, transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "rebase-runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	parked, err := store.ParkAskUser(workspace.ParkAskUserInput{
		FrameID: stream.FrameID, ToolID: "ask-rebase", ToolName: "AskUserQuestion",
		Questions: []any{map[string]any{
			"header": "History", "question": "Keep history?", "options": []any{
				map[string]any{"label": "Keep", "description": "Keep it."},
				map[string]any{"label": "Review", "description": "Review it."},
			},
		}},
	})
	if err != nil || len(parked.Events) != 3 {
		t.Fatalf("parked=%#v err=%v", parked, err)
	}
	err = repo.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		_, appendErr := tx.AppendClaimedFrameAskUserReferences(ctx, transcriptstore.AppendClaimedFrameAskUserReferencesInput{
			Claim: claimed.Claim, FrameID: stream.FrameID, ToolUseID: "ask-rebase",
			ToolUseFrameEventID: parked.Events[0].ID, ToolResultFrameEventID: parked.Events[1].ID,
		})
		return appendErr
	})
	if err != nil {
		t.Fatal(err)
	}
	answer, err := transcriptstore.NewAskUserResultV1(
		transcriptstore.AskUserActionAnswer, map[string]string{"Keep history?": "Keep"}, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := transcriptstore.EncodeAskUserResultV1(answer)
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := store.ResolveCompatibilityPendingInputs(stream.FrameID, []workspace.CompatibilityInputResolution{{
		ToolID: "ask-rebase", Content: string(encoded), IsError: true,
	}}); err != nil || resolved.Status != "accepted" {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	if _, _, _, err := repo.FinishRunner(ctx, transcriptstore.FinishRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "rebase-finished", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`),
	}); err != nil {
		t.Fatal(err)
	}
	server := &Server{workspaceStore: store, transcriptStore: repo}
	legacyMessages, _, err := server.projectTranscriptWebHistory(ctx, stream, stream.OwnerID, stream.SessionID, "")
	if err != nil || len(legacyMessages) != 10 {
		t.Fatalf("legacy messages=%d err=%v", len(legacyMessages), err)
	}
	last := legacyMessages[len(legacyMessages)-1]
	lastContent, _ := last["content"].(map[string]any)
	lastCoordinates, _ := lastContent["synonBiomed"].(map[string]any)
	lastID, _ := last["id"].(string)
	lastIndex, _ := lastCoordinates["messageIndex"].(int)
	frame, found, err := store.GetCompatibilityFrame(stream.FrameID)
	if err != nil || !found || lastID == "" || lastIndex != len(legacyMessages)-1 {
		t.Fatalf("frame=%#v found=%t last=%#v err=%v", frame, found, last, err)
	}
	storedCursor, err := server.putCompatibilityFrameReadCursor(ctx, frame, lastID, lastIndex, "", nil, false)
	if err != nil || storedCursor.MessageUUID != lastID || storedCursor.MessageIndex != lastIndex {
		t.Fatalf("stored cursor=%#v err=%v", storedCursor, err)
	}
	for {
		delivery, err := repo.ClaimNextDelivery(ctx, transcriptstore.ClaimDeliveryInput{
			OwnerID: stream.OwnerID, Destination: "ws", WorkerID: "rebase-test", TTL: time.Minute,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !delivery.Claimed {
			break
		}
		if _, err := repo.AcknowledgeDelivery(ctx, transcriptstore.AcknowledgeDeliveryInput{Claim: delivery.Claim}); err != nil {
			t.Fatal(err)
		}
	}
	audit, _, err := repo.AuditAskUserHistory(ctx, transcriptstore.AuditAskUserHistoryInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, MaxEvents: 100, MaxCandidates: 16, MaxShadowRows: 100,
	})
	if err != nil || audit.EligibleCount != 1 {
		t.Fatalf("audit=%#v err=%v", audit, err)
	}
	backfill, _, err := repo.StageAskUserHistoryBackfill(ctx, transcriptstore.StageAskUserHistoryBackfillInput{
		RunID: audit.RunID, StreamUID: stream.UID, OwnerID: stream.OwnerID, BranchID: audit.BranchID,
		MaxEvents: 100, MaxCandidates: 16, MaxShadowRows: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	cutover, _, err := repo.PrepareAskUserHistoryCutover(ctx, transcriptstore.PrepareAskUserHistoryCutoverInput{
		BackfillID: backfill.BackfillID, StreamUID: stream.UID, OwnerID: stream.OwnerID,
		MaxBranches: 16, MaxEvents: 100, MaxCursorRows: 100, MaxShadowRows: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	activation, _, err := repo.ActivateAskUserHistoryCutover(ctx, transcriptstore.ActivateAskUserHistoryCutoverInput{
		CutoverID: cutover.CutoverID, OwnerID: stream.OwnerID, MaxBranches: 16, MaxEvents: 100,
		MaxAttempts: 16, MaxCheckpoints: 100, MaxBranchEvents: 100, MaxArtifactCommits: 100,
		MaxArtifactRefs: 100, MaxRoutes: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	active, activeFound, activeErr := repo.GetFrameStreamBySession(ctx, stream.OwnerID, stream.SessionID)
	if activeErr != nil || !activeFound {
		t.Fatalf("active=%#v found=%t err=%v", active, activeFound, activeErr)
	}
	projected, err := repo.ListProjectedEvents(ctx, transcriptstore.ListProjectedEventsInput{
		StreamUID: active.UID, OwnerID: active.OwnerID, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	terminalAskUserFound := false
	for _, event := range projected {
		if event.Event.Type != transcriptstore.AskUserResultEventType {
			continue
		}
		decoded, decodeErr := transcriptstore.DecodeAskUserResultEventV1(event.ResolvedPayloadJSON)
		if decodeErr != nil || decoded.Result.Status == transcriptstore.AskUserStatusAwaitingResponse {
			continue
		}
		expectedClientID, expectedErr := transcriptstore.AskUserResultClientMessageIDV1(decoded.Origin)
		if expectedErr != nil || event.Event.ClientMessageID != expectedClientID {
			t.Fatalf("terminal AskUser client=%q expected=%q err=%v", event.Event.ClientMessageID, expectedClientID, expectedErr)
		}
		terminalAskUserFound = true
	}
	if !terminalAskUserFound {
		t.Fatal("activated terminal AskUser fact was not found")
	}
	directMessages, directSnapshot, directErr := server.projectTranscriptWebHistory(
		ctx, active, stream.OwnerID, stream.SessionID, "",
	)
	if directErr != nil {
		t.Fatalf("direct messages=%d snapshot=%#v err=%T %v", len(directMessages), directSnapshot, directErr, directErr)
	}
	messages, snapshot, found, err := server.loadActivatedTranscriptWebHistory(ctx, stream.OwnerID, stream.SessionID, "")
	if err != nil || !found || len(messages) != 10 || snapshot.BranchID != activation.ActiveBranchID {
		t.Fatalf("messages=%d snapshot=%#v found=%t activation=%#v err=%v", len(messages), snapshot, found, activation, err)
	}
	reloadedCursor, cursorFound, err := server.getCompatibilityFrameReadCursor(ctx, frame)
	if err != nil || !cursorFound || reloadedCursor.MessageUUID != lastID || reloadedCursor.MessageIndex != lastIndex {
		t.Fatalf("activated cursor=%#v found=%t err=%v", reloadedCursor, cursorFound, err)
	}
}

func TestPayloadGenesisReadCursorNeverUsesActivationTranslation(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-genesis-cursor", "frame-genesis-cursor")
	ctx := context.Background()
	stream, err := repo.CreateStream(ctx, transcriptstore.CreateStreamInput{
		UID: "frame:frame-genesis-cursor", OwnerID: "local", ExternalID: "frame-genesis-cursor",
		SessionID: "frame-genesis-cursor", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-genesis-cursor", RootFrameID: "frame-genesis-cursor",
		FrameID: "frame-genesis-cursor", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		suffix := fmt.Sprintf("%d", index)
		if _, _, created, err := repo.AppendFrameUserEvent(ctx, transcriptstore.AppendFrameUserEventInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "genesis-client-" + suffix,
			FrameEventID: "unused-genesis-frame-" + suffix, MessageUUID: "genesis-message-" + suffix,
			Text: "genesis history " + suffix,
		}); err != nil || !created {
			t.Fatalf("append %s created=%t err=%v", suffix, created, err)
		}
	}
	server := &Server{workspaceStore: store, transcriptStore: repo}
	frame, found, err := store.GetCompatibilityFrame(stream.FrameID)
	if err != nil || !found {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	messages, _, err := server.projectTranscriptWebHistory(ctx, stream, stream.OwnerID, stream.SessionID, "")
	if err != nil || len(messages) != 2 {
		t.Fatalf("messages=%#v err=%v", messages, err)
	}
	lastID := webString(messages[1]["id"])
	if _, err := server.putCompatibilityFrameReadCursor(ctx, frame, lastID, 1, "", nil, false); err != nil {
		t.Fatal(err)
	}
	if cursor, found, err := server.getCompatibilityFrameReadCursor(ctx, frame); err != nil || !found ||
		cursor.MessageUUID != lastID || cursor.MessageIndex != 1 {
		t.Fatalf("current cursor=%#v found=%t err=%v", cursor, found, err)
	}
	if _, err := db.Exec(`UPDATE frame_read_cursors SET message_uuid=?,message_index=99
		WHERE root_frame_id=?`, lastID, frame.ID); err != nil {
		t.Fatal(err)
	}
	if cursor, found, err := server.getCompatibilityFrameReadCursor(ctx, frame); err != nil || !found ||
		cursor.MessageUUID != lastID || cursor.MessageIndex != 1 {
		t.Fatalf("relocated cursor=%#v found=%t err=%v", cursor, found, err)
	}
	if _, err := db.Exec(`UPDATE frame_read_cursors SET message_uuid='legacy-stale-id',message_index=99
		WHERE root_frame_id=?`, frame.ID); err != nil {
		t.Fatal(err)
	}
	if cursor, found, err := server.getCompatibilityFrameReadCursor(ctx, frame); !errors.Is(err, workspace.ErrReadCursorConflict) || found || cursor.MessageUUID != "" {
		t.Fatalf("stale cursor=%#v found=%t err=%v", cursor, found, err)
	} else {
		var conflict *compatibilityReadCursorAuthorityConflict
		if !errors.As(err, &conflict) || conflict.Observed.MessageUUID != "legacy-stale-id" ||
			conflict.Observed.MessageIndex != 99 || conflict.AuthorityGeneration != 1 || conflict.BranchID == "" {
			t.Fatalf("stale cursor conflict=%#v err=%T %v", conflict, err, err)
		}
	}
	recorder := httptest.NewRecorder()
	server.handleCompatibilityFrameReadCursor(
		recorder, httptest.NewRequest(http.MethodGet, "/api/frames/"+frame.ID+"/read-cursor", nil), frame,
	)
	var response map[string]any
	if recorder.Code != http.StatusConflict || json.Unmarshal(recorder.Body.Bytes(), &response) != nil ||
		response["code"] != "READ_CURSOR_AUTHORITY_CHANGED" {
		t.Fatalf("stale cursor response=%d %s", recorder.Code, recorder.Body.String())
	}
	details, _ := response["details"].(map[string]any)
	observed, _ := details["observed_cursor"].(map[string]any)
	if observed["message_uuid"] != "legacy-stale-id" || numberValue(observed["message_index"]) != 99 ||
		numberValue(details["authority_generation"]) != 1 || details["branch_id"] == "" {
		t.Fatalf("stale cursor details=%#v", details)
	}
	observedIndex := 99
	if _, err := server.putCompatibilityFrameReadCursor(
		ctx, frame, lastID, 1, "different-observed-id", &observedIndex, true,
	); !errors.Is(err, workspace.ErrReadCursorConflict) {
		t.Fatalf("stale cursor wrong CAS err=%v", err)
	}
	if repaired, err := server.putCompatibilityFrameReadCursor(
		ctx, frame, lastID, 1, "legacy-stale-id", &observedIndex, true,
	); err != nil || repaired.MessageUUID != lastID || repaired.MessageIndex != 1 {
		t.Fatalf("stale cursor repair=%#v err=%v", repaired, err)
	}
	stored, found, err := store.GetReadCursor(frame.ID)
	if err != nil || !found || stored.MessageUUID != lastID || stored.MessageIndex != 1 {
		t.Fatalf("stored repaired cursor=%#v found=%t err=%v", stored, found, err)
	}
	var activationRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_history_cutover_cursor_map`).Scan(&activationRows); err != nil || activationRows != 0 {
		t.Fatalf("activation cursor rows=%d err=%v", activationRows, err)
	}
}

func TestPayloadGenesisImportsPlainHistoryIntoStableWebProjection(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-genesis-history", "frame-genesis-history")
	now := time.Now().UTC()
	for index, event := range []struct {
		id, eventType, payload string
	}{
		{"legacy-user", "user_message", `{"role":"user","content":"prior question"}`},
		{"legacy-assistant", "assistant_message", `{"role":"assistant","content":"prior answer"}`},
		{"legacy-system", "system_message", `{"role":"system","content":"hidden harness"}`},
	} {
		if _, err := db.Exec(`INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
			VALUES(?,?,?,?,?,?)`, event.id, "frame-genesis-history", index+1, event.eventType, event.payload, now.Add(time.Duration(index)*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	stream, err := repo.CreateStream(ctx, transcriptstore.CreateStreamInput{
		UID: "frame:frame-genesis-history", OwnerID: "local", ExternalID: "frame-genesis-history",
		SessionID: "frame-genesis-history", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-genesis-history", RootFrameID: "frame-genesis-history",
		FrameID: "frame-genesis-history", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{workspaceStore: store, transcriptStore: repo}
	first, snapshot, err := server.projectTranscriptWebHistory(ctx, stream, stream.OwnerID, stream.SessionID, "")
	if err != nil || len(first) != 2 || snapshot.ThroughPublicationSequence != 3 {
		t.Fatalf("messages=%#v snapshot=%#v err=%v", first, snapshot, err)
	}
	if webString(first[0]["id"]) != "payload-genesis:legacy-user" ||
		webString(first[0]["position"]) != "right" ||
		webString(first[0]["content"].(map[string]any)["content"]) != "prior question" ||
		webString(first[1]["id"]) != "payload-genesis:legacy-assistant" ||
		webString(first[1]["position"]) != "left" ||
		webString(first[1]["content"].(map[string]any)["content"]) != "prior answer" {
		t.Fatalf("projected messages=%#v", first)
	}
	coordinates, coordinateSnapshot, err := server.projectTranscriptWebCoordinates(
		ctx, stream, stream.OwnerID, stream.SessionID, "",
	)
	if err != nil || !reflect.DeepEqual(snapshot, coordinateSnapshot) || len(coordinates) != 2 ||
		coordinates[0].id != webString(first[0]["id"]) || coordinates[1].id != webString(first[1]["id"]) {
		t.Fatalf("coordinates=%#v snapshot=%#v err=%v", coordinates, coordinateSnapshot, err)
	}
	second, secondSnapshot, err := server.projectTranscriptWebHistory(ctx, stream, stream.OwnerID, stream.SessionID, "")
	if err != nil || !reflect.DeepEqual(first, second) || !reflect.DeepEqual(snapshot, secondSnapshot) {
		t.Fatalf("first=%#v second=%#v snapshot=%#v secondSnapshot=%#v err=%v",
			first, second, snapshot, secondSnapshot, err)
	}
	var intents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_delivery_intents WHERE stream_uid=?`, stream.UID).Scan(&intents); err != nil || intents != 0 {
		t.Fatalf("delivery intents=%d err=%v", intents, err)
	}
}

func forceLegacyTranscriptFrameAuthority(t *testing.T, db *sql.DB, stream transcriptstore.Stream) {
	t.Helper()
	if _, err := db.Exec(`DELETE FROM transcript_frame_authority WHERE owner_id=? AND session_id=?`,
		stream.OwnerID, stream.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM transcript_payload_genesis_receipts WHERE stream_uid=?`, stream.UID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_frame_authority(
		owner_id,session_id,active_stream_uid,active_epoch,authority_generation,
		read_authority,write_authority,activation_id,genesis_id,updated_at)
		VALUES(?,?,?,?,1,'legacy_mixed_v1','legacy_frame_ref_v1',NULL,NULL,?)`,
		stream.OwnerID, stream.SessionID, stream.UID, stream.Epoch, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

func newTranscriptWebFixture(t *testing.T) (*workspace.Store, *transcriptstore.Repository, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workspace.db")
	store, err := workspace.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		_ = db.Close()
		_ = store.Close()
	})
	return store, repo, db
}

func seedTranscriptWebFrame(t *testing.T, store *workspace.Store, ownerID, projectID, frameID string) {
	t.Helper()
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: projectID, UserID: ownerID, Name: projectID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: frameID, ProjectID: projectID, ParentFrameID: "", AgentName: "synon", Status: "processing", ConversationType: "chat",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata(frameID, workspace.FrameRuntimeMetadata{
		FrameID: frameID, ContextData: map[string]any{"web_extra": map[string]any{}},
	}); err != nil {
		t.Fatal(err)
	}
}

func transcriptWebEvents(t *testing.T, store *workspace.Store, ownerID string) []workspace.RealtimeEvent {
	t.Helper()
	events, err := store.ListRealtimeEvents(workspace.RealtimeEventFilter{UserID: ownerID, IncludeGlobal: true, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	for index := range events {
		raw, err := json.Marshal(events[index].Payload)
		if err != nil || !json.Valid(raw) {
			t.Fatalf("event %d payload=%#v err=%v", index, events[index].Payload, err)
		}
	}
	return events
}

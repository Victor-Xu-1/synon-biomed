package transcript

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestRepositoryTerminalProjectionUsesReceiptMappingAndAtomicWebIntent(t *testing.T) {
	tests := []struct {
		name       string
		status     string
		streamType string
		detail     string
		reasonCode string
		finish     func(*Repository, RunnerClaim) (Event, error)
	}{
		{
			name: "completed", status: "completed", streamType: "finish", detail: "done",
			finish: func(repo *Repository, claim RunnerClaim) (Event, error) {
				event, _, _, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
					Claim: claim, ClientMessageID: "finish-completed", Status: "completed",
					PayloadJSON: []byte(`{"status":"completed","detail":"done"}`),
				})
				return event, err
			},
		},
		{
			name: "cancelled", status: "cancelled", streamType: "finish", detail: "", reasonCode: "user_cancelled",
			finish: func(repo *Repository, claim RunnerClaim) (Event, error) {
				result, err := repo.CancelRunner(context.Background(), CancelRunnerInput{
					StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, ExpectedAttempt: claim.Attempt,
					ClientMessageID: "finish-cancelled", ReasonCode: "user_cancelled",
				})
				return result.Event, err
			},
		},
		{
			name: "failed", status: "failed", streamType: "error", detail: "provider unavailable", reasonCode: "provider_unavailable",
			finish: func(repo *Repository, claim RunnerClaim) (Event, error) {
				event, _, _, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
					Claim: claim, ClientMessageID: "finish-failed", Status: "failed",
					PayloadJSON: []byte(`{"status":"failed","detail":"provider unavailable","reason_code":"provider_unavailable"}`),
				})
				return event, err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo, db, _ := newTranscriptRepository(t)
			claim := seedArtifactProjectionClaim(t, repo, db, "stream-terminal-"+test.name, "owner-a")
			event, err := test.finish(repo, claim)
			if err != nil {
				t.Fatal(err)
			}
			projection, err := repo.GetTerminalProjection(context.Background(), claim.OwnerID, claim.StreamUID, event.EventID)
			if err != nil || projection.SessionID != "session-a" || projection.FrameID != "frame-a" ||
				projection.Attempt != claim.Attempt || projection.TerminalStatus != test.status ||
				projection.StreamType != test.streamType || projection.Detail != test.detail || projection.ReasonCode != test.reasonCode {
				t.Fatalf("projection=%#v err=%v", projection, err)
			}
			if _, err := repo.GetTerminalProjection(context.Background(), "owner-b", claim.StreamUID, event.EventID); !errors.Is(err, ErrOwnerMismatch) {
				t.Fatalf("foreign projection error=%v", err)
			}
			intent, err := repo.GetDeliveryIntent(context.Background(), claim.OwnerID, claim.StreamUID, event.PublicationSeq, "ws", 1)
			if err != nil || intent.Status != "pending" {
				t.Fatalf("terminal intent=%#v err=%v", intent, err)
			}
		})
	}
}

func TestRepositoryTerminalProjectionRecoveryRepairsOnlyResolvedMappings(t *testing.T) {
	repo, db, dsn := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-terminal-recovery", "owner-a")
	event, _, _, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: claim, ClientMessageID: "finish-recovery", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM transcript_delivery_intents WHERE stream_uid=? AND publication_seq=?`, claim.StreamUID, event.PublicationSeq); err != nil {
		t.Fatal(err)
	}

	reopenedDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedDB.Close()
	reopened := NewRepository(reopenedDB)
	if count, err := reopened.RecoverTerminalDeliveryIntents(context.Background(), "owner-a"); err != nil || count != 1 {
		t.Fatalf("recovered=%d err=%v", count, err)
	}
	if count, err := reopened.RecoverTerminalDeliveryIntents(context.Background(), "owner-a"); err != nil || count != 0 {
		t.Fatalf("idempotent recovered=%d err=%v", count, err)
	}
	if _, err := reopened.GetDeliveryIntent(context.Background(), "owner-a", claim.StreamUID, event.PublicationSeq, "ws", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := reopenedDB.Exec(`DELETE FROM transcript_delivery_intents WHERE stream_uid=? AND publication_seq=?`, claim.StreamUID, event.PublicationSeq); err != nil {
		t.Fatal(err)
	}
	if _, err := reopenedDB.Exec(`DELETE FROM transcript_frame_authority WHERE active_stream_uid=?`, claim.StreamUID); err != nil {
		t.Fatal(err)
	}
	if _, err := reopenedDB.Exec(`DELETE FROM transcript_payload_genesis_receipts WHERE stream_uid=?`, claim.StreamUID); err != nil {
		t.Fatal(err)
	}
	if _, err := reopenedDB.Exec(`UPDATE transcript_streams SET session_id='' WHERE stream_uid=?`, claim.StreamUID); err != nil {
		t.Fatal(err)
	}
	if count, err := reopened.RecoverTerminalDeliveryIntents(context.Background(), "owner-a"); err != nil || count != 0 {
		t.Fatalf("unresolved recovered=%d err=%v", count, err)
	}
	if _, err := reopened.GetTerminalProjection(context.Background(), "owner-a", claim.StreamUID, event.EventID); !errors.Is(err, ErrTerminalProjectionUnavailable) {
		t.Fatalf("unresolved projection error=%v", err)
	}
}

func TestRepositoryTerminalProjectionRejectsPayloadStatusConflict(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-terminal-conflict", "owner-a")
	event, _, _, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: claim, ClientMessageID: "finish-conflict", Status: "failed",
		PayloadJSON: []byte(`{"status":"completed"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetTerminalProjection(context.Background(), claim.OwnerID, claim.StreamUID, event.EventID); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("conflicting projection error=%v", err)
	}
}

func TestRepositoryTerminalProjectionDoesNotExposeUntrustedFailureReason(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-terminal-untrusted-reason", "owner-a")
	event, _, _, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: claim, ClientMessageID: "finish-untrusted-reason", Status: "failed",
		PayloadJSON: []byte(`{"status":"failed","detail":"provider unavailable","reason_code":"sk-secret for alice@example.com"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	projection, err := repo.GetTerminalProjection(context.Background(), claim.OwnerID, claim.StreamUID, event.EventID)
	if err != nil || projection.Detail != "provider unavailable" || projection.ReasonCode != "" {
		t.Fatalf("projection=%#v err=%v", projection, err)
	}
}

func TestRepositoryTerminalProjectionMarksSameInputRetrySuperseded(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-terminal-superseded", "owner-a")
	event, _, _, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
		Claim: claim, ClientMessageID: "finish-superseded", Status: "failed",
		PayloadJSON: []byte(`{"status":"failed","detail":"provider unavailable"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := db.Exec(`
		INSERT INTO transcript_runner_attempts(
			stream_uid,attempt,runner_id,claim_token_sha256,claimed_input_revision,resume_source,
			resume_checkpoint_sequence,status,phase,phase_sequence,last_checkpoint_sequence,claimed_at,expires_at
		)
		SELECT stream_uid,attempt+1,'runner-retry',claim_token_sha256,claimed_input_revision,'retry',
			0,'running','claimed',1,0,?,?
		FROM transcript_runner_attempts WHERE stream_uid=? AND attempt=?`,
		now, now.Add(time.Minute), claim.StreamUID, claim.Attempt); err != nil {
		t.Fatal(err)
	}
	projection, err := repo.GetTerminalProjection(context.Background(), claim.OwnerID, claim.StreamUID, event.EventID)
	if err != nil || !projection.Superseded {
		t.Fatalf("same-input retry projection=%#v err=%v", projection, err)
	}
	if _, err := db.Exec(`
		UPDATE transcript_runner_attempts SET claimed_input_revision=claimed_input_revision+1
		WHERE stream_uid=? AND attempt=?`, claim.StreamUID, claim.Attempt+1); err != nil {
		t.Fatal(err)
	}
	projection, err = repo.GetTerminalProjection(context.Background(), claim.OwnerID, claim.StreamUID, event.EventID)
	if err != nil || projection.Superseded {
		t.Fatalf("different-input retry projection=%#v err=%v", projection, err)
	}
}

func TestMapTerminalStatusFreezesPublicStreamContract(t *testing.T) {
	tests := []struct {
		input, status, streamType string
	}{
		{input: "completed", status: "completed", streamType: "finish"},
		{input: "cancelled", status: "cancelled", streamType: "finish"},
		{input: " canceled ", status: "cancelled", streamType: "finish"},
		{input: "FAILED", status: "failed", streamType: "error"},
	}
	for _, test := range tests {
		status, streamType, err := MapTerminalStatus(test.input)
		if err != nil || status != test.status || streamType != test.streamType {
			t.Fatalf("MapTerminalStatus(%q)=(%q,%q,%v)", test.input, status, streamType, err)
		}
	}
	if status, streamType, err := MapTerminalStatus("done"); !errors.Is(err, ErrTerminalProjectionUnavailable) || status != "" || streamType != "" {
		t.Fatalf("unknown mapping=(%q,%q,%v)", status, streamType, err)
	}
}

func TestListTerminalProjectionOwnersAdvancesBeyondRecoveredPage(t *testing.T) {
	repository, db, _ := newTranscriptRepository(t)
	ctx := context.Background()
	for index := 0; index < 101; index++ {
		ownerID := fmt.Sprintf("owner-%03d", index)
		projectID := fmt.Sprintf("project-%03d", index)
		frameID := fmt.Sprintf("frame-%03d", index)
		if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES(?,?)`, projectID, ownerID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO frames(id,project_id,root_frame_id) VALUES(?,?,?)`, frameID, projectID, frameID); err != nil {
			t.Fatal(err)
		}
		stream, err := repository.CreateStream(ctx, CreateStreamInput{
			UID: "frame:" + frameID, OwnerID: ownerID, ExternalID: frameID, SessionID: frameID,
			Kind: StreamKindFrameRef, ProjectID: projectID, RootFrameID: frameID, FrameID: frameID, Epoch: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := repository.AppendUserEvent(ctx, AppendUserEventInput{
			StreamUID: stream.UID, OwnerID: ownerID, ClientMessageID: "user", PayloadJSON: []byte(`{"text":"work"}`),
		}); err != nil {
			t.Fatal(err)
		}
		claim, err := repository.ClaimRunner(ctx, ClaimRunnerInput{
			StreamUID: stream.UID, OwnerID: ownerID, RunnerID: "runner", TTL: time.Minute, ResumeSource: ResumeSourceFresh,
		})
		if err != nil || !claim.Claimed {
			t.Fatalf("claim owner %s: %#v %v", ownerID, claim, err)
		}
		event, _, _, err := repository.FinishRunner(ctx, FinishRunnerInput{
			Claim: claim.Claim, ClientMessageID: "finish", Status: "completed",
			PayloadJSON: []byte(`{"status":"completed"}`), Destinations: []string{"ws"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`DELETE FROM transcript_delivery_intents WHERE stream_uid=? AND publication_seq=?`, stream.UID, event.PublicationSeq); err != nil {
			t.Fatal(err)
		}
	}

	first, err := repository.ListTerminalProjectionOwners(ctx, 100)
	if err != nil || len(first) != 100 {
		t.Fatalf("first owner page len=%d err=%v", len(first), err)
	}
	for _, ownerID := range first {
		if recovered, err := repository.RecoverTerminalDeliveryIntents(ctx, ownerID); err != nil || recovered != 1 {
			t.Fatalf("recover %s=%d err=%v", ownerID, recovered, err)
		}
	}
	second, err := repository.ListTerminalProjectionOwners(ctx, 100)
	if err != nil || len(second) != 1 || second[0] != "owner-100" {
		t.Fatalf("second owner page=%v err=%v", second, err)
	}
}

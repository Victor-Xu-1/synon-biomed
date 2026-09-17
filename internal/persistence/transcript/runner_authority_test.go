package transcript

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"testing"
	"time"
)

func TestTrustedRunnerAuthorityBindsCurrentFrameStreamAndAttempt(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*TrustedRunnerAuthority)
		db     func(*testing.T, trustedRunnerAuthorityFixture)
	}{
		{name: "valid"},
		{name: "owner", mutate: func(value *TrustedRunnerAuthority) { value.OwnerID = "owner-b" }},
		{name: "project", mutate: func(value *TrustedRunnerAuthority) { value.ProjectID = "project-b" }},
		{name: "root", mutate: func(value *TrustedRunnerAuthority) { value.RootFrameID = "root-b" }},
		{name: "frame", mutate: func(value *TrustedRunnerAuthority) { value.FrameID = "frame-b" }},
		{name: "epoch", mutate: func(value *TrustedRunnerAuthority) { value.StreamEpoch++ }},
		{name: "runner", mutate: func(value *TrustedRunnerAuthority) { value.RunnerID = "runner-b" }},
		{name: "attempt", mutate: func(value *TrustedRunnerAuthority) { value.Attempt++ }},
		{name: "digest", mutate: func(value *TrustedRunnerAuthority) { value.ClaimSHA256 = hex.EncodeToString(make([]byte, 32)) }},
		{name: "revision", mutate: func(value *TrustedRunnerAuthority) { value.ClaimedInputRevision++ }},
		{name: "resume source", mutate: func(value *TrustedRunnerAuthority) { value.ResumeSource = ResumeSourceRetry }},
		{name: "resume checkpoint", mutate: func(value *TrustedRunnerAuthority) { value.ResumeCheckpoint = 1 }},
		{name: "expired", db: func(t *testing.T, fixture trustedRunnerAuthorityFixture) {
			fixture.repository.now = func() time.Time { return fixture.now.Add(2 * time.Minute) }
		}},
		{name: "terminal", db: func(t *testing.T, fixture trustedRunnerAuthorityFixture) {
			if _, _, _, err := fixture.repository.FinishRunner(context.Background(), FinishRunnerInput{
				Claim: fixture.claim, ClientMessageID: "finish-1", Status: "completed",
				PayloadJSON: []byte(`{"status":"completed"}`),
			}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "inactive stream", db: func(t *testing.T, fixture trustedRunnerAuthorityFixture) {
			if _, err := fixture.db.Exec(`DELETE FROM transcript_frame_authority WHERE owner_id=? AND session_id=?`, fixture.authority.OwnerID, fixture.authority.FrameID); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTrustedRunnerAuthorityFixture(t, StreamKindFrameRef)
			authority := fixture.authority
			if test.mutate != nil {
				test.mutate(&authority)
			}
			if test.db != nil {
				test.db(t, fixture)
			}
			var stream Stream
			err := fixture.repository.RunImmediate(context.Background(), func(tx *ImmediateTransaction) error {
				var err error
				stream, err = tx.ValidateLiveTrustedRunnerAuthority(context.Background(), authority)
				return err
			})
			if test.name == "valid" {
				if err != nil || stream.UID != authority.StreamUID {
					t.Fatalf("stream=%#v err=%v", stream, err)
				}
				return
			}
			if !errors.Is(err, ErrClaimStale) {
				t.Fatalf("err=%v want=%v", err, ErrClaimStale)
			}
		})
	}
}

func TestTrustedRunnerAuthorityRejectsStandaloneStream(t *testing.T) {
	fixture := newTrustedRunnerAuthorityFixture(t, StreamKindStandalone)
	err := fixture.repository.RunImmediate(context.Background(), func(tx *ImmediateTransaction) error {
		_, err := tx.ValidateLiveTrustedRunnerAuthority(context.Background(), fixture.authority)
		return err
	})
	if !errors.Is(err, ErrClaimStale) {
		t.Fatalf("err=%v want=%v", err, ErrClaimStale)
	}
}

type trustedRunnerAuthorityFixture struct {
	repository *Repository
	db         *sql.DB
	authority  TrustedRunnerAuthority
	claim      RunnerClaim
	now        time.Time
}

func newTrustedRunnerAuthorityFixture(t *testing.T, kind StreamKind) trustedRunnerAuthorityFixture {
	t.Helper()
	repository, db, _ := newTranscriptRepository(t)
	now := time.Date(2026, 7, 31, 18, 0, 0, 0, time.UTC)
	repository.now = func() time.Time { return now }
	ctx := context.Background()
	stream := CreateStreamInput{UID: "frame:frame-a", OwnerID: "owner-a", ExternalID: "frame-a", SessionID: "frame-a", Kind: kind, Epoch: 1}
	if kind == StreamKindFrameRef {
		if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES('project-a','owner-a')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO frames(id,project_id,root_frame_id) VALUES('frame-a','project-a','frame-a')`); err != nil {
			t.Fatal(err)
		}
		stream.ProjectID, stream.RootFrameID, stream.FrameID = "project-a", "frame-a", "frame-a"
	} else {
		stream.UID, stream.ExternalID, stream.SessionID = "standalone-a", "standalone-a", "standalone-a"
	}
	created, err := repository.CreateStream(ctx, stream)
	if err != nil {
		t.Fatal(err)
	}
	if _, createdEvent, err := repository.AppendUserEvent(ctx, AppendUserEventInput{
		StreamUID: created.UID, OwnerID: created.OwnerID, ClientMessageID: "user-1", PayloadJSON: []byte(`{"text":"work"}`),
	}); err != nil || !createdEvent {
		t.Fatalf("append created=%t err=%v", createdEvent, err)
	}
	claimed, err := repository.ClaimRunner(ctx, ClaimRunnerInput{
		StreamUID: created.UID, OwnerID: created.OwnerID, RunnerID: "runner-a", TTL: time.Minute, ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	digest := sha256.Sum256([]byte(claimed.Claim.ClaimToken))
	return trustedRunnerAuthorityFixture{
		repository: repository,
		db:         db,
		now:        now,
		claim:      claimed.Claim,
		authority: TrustedRunnerAuthority{
			StreamUID: created.UID, OwnerID: created.OwnerID, ProjectID: created.ProjectID,
			RootFrameID: created.RootFrameID, FrameID: created.FrameID, StreamEpoch: created.Epoch,
			RunnerID: claimed.Claim.RunnerID, Attempt: claimed.Claim.Attempt,
			ClaimSHA256: hex.EncodeToString(digest[:]), ClaimedInputRevision: claimed.Claim.ClaimedInputRevision,
			ResumeSource: claimed.Claim.ResumeSource, ResumeCheckpoint: claimed.Claim.ResumeCheckpoint,
		},
	}
}

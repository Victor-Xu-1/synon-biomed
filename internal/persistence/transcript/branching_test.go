package transcript

import (
	"context"
	"errors"
	"testing"
)

func TestCreateStreamInitializesBaseBranchAndAppendsMembership(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	stream, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "stream-branch", OwnerID: "owner-branch", ExternalID: "external-branch",
		Kind: StreamKindStandalone, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || state.StreamUID != stream.UID || len(state.ActiveBranchID) != 11 || state.Generation != 1 {
		t.Fatalf("state=%#v err=%v", state, err)
	}
	event, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "branch-message",
		PayloadJSON: []byte(`{"role":"user","text":"branch foundation"}`), Destinations: []string{"ws"},
	})
	if err != nil || !created {
		t.Fatalf("event=%#v created=%t err=%v", event, created, err)
	}
	if _, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "branch-message",
		PayloadJSON: []byte(`{"role":"user","text":"branch foundation"}`), Destinations: []string{"ws"},
	}); err != nil || created {
		t.Fatalf("idempotent created=%t err=%v", created, err)
	}
	var branchID, kind, mutationID string
	var generation, membershipCount, ordinal, eventID int64
	if err := db.QueryRow(`
		SELECT branch.branch_id,branch.kind,branch.client_mutation_id,state.generation,
			COUNT(membership.event_id),MIN(membership.ordinal),MIN(membership.event_id)
		FROM transcript_branches branch
		JOIN transcript_branch_state state ON state.stream_uid=branch.stream_uid AND state.active_branch_id=branch.branch_id
		LEFT JOIN transcript_branch_events membership ON membership.stream_uid=branch.stream_uid AND membership.branch_id=branch.branch_id
		WHERE branch.stream_uid=? GROUP BY branch.branch_id,branch.kind,branch.client_mutation_id,state.generation`, stream.UID,
	).Scan(&branchID, &kind, &mutationID, &generation, &membershipCount, &ordinal, &eventID); err != nil {
		t.Fatal(err)
	}
	if branchID != state.ActiveBranchID || kind != "base" || mutationID != "base:"+stream.UID ||
		generation != 1 || membershipCount != 1 || ordinal != 1 || eventID != event.EventID {
		t.Fatalf("branch=%q kind=%q mutation=%q generation=%d membership=%d/%d/%d",
			branchID, kind, mutationID, generation, membershipCount, ordinal, eventID)
	}
	if _, err := repo.GetBranchState(context.Background(), stream.UID, "owner-foreign"); !errors.Is(err, ErrOwnerMismatch) {
		t.Fatalf("foreign state error=%v", err)
	}
}

func TestBranchSchemaRejectsInvalidActiveAndCrossStreamMembership(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	for _, uid := range []string{"branch-stream-a", "branch-stream-b"} {
		if _, err := repo.CreateStream(context.Background(), CreateStreamInput{
			UID: uid, OwnerID: "owner", ExternalID: "external-" + uid, Kind: StreamKindStandalone, Epoch: 1,
		}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
			StreamUID: uid, OwnerID: "owner", ClientMessageID: "message-" + uid,
			PayloadJSON: []byte(`{"role":"user","text":"event"}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE transcript_branch_state SET active_branch_id='br_deadbeef' WHERE stream_uid='branch-stream-a'`); err == nil {
		t.Fatal("invalid active branch update succeeded")
	}
	var branchB string
	if err := db.QueryRow(`SELECT active_branch_id FROM transcript_branch_state WHERE stream_uid='branch-stream-b'`).Scan(&branchB); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO transcript_branch_events(stream_uid,branch_id,ordinal,event_id)
		VALUES('branch-stream-a',?,2,1)`, branchB); err == nil {
		t.Fatal("cross-stream branch membership succeeded")
	}
}

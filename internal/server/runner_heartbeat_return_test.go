package server

import (
	"context"
	"errors"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestSessionRunnerHeartbeatReturnPreservesCycleErrors(t *testing.T) {
	originalErr := errors.New("checkpoint failed")
	leaseErr := errors.New("heartbeat write failed")
	alreadyJoined := errors.Join(originalErr, leaseErr)
	server := &Server{}
	for _, test := range []struct {
		name     string
		cycleErr error
		stopErr  error
	}{
		{name: "independent_errors", cycleErr: originalErr, stopErr: leaseErr},
		{name: "already_consumed", cycleErr: originalErr},
		{name: "already_joined", cycleErr: alreadyJoined, stopErr: leaseErr},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := SessionRunnerCycleResult{}
			cycleErr := test.cycleErr
			stops := 0
			server.settleSessionRunnerHeartbeatOnReturn(t.Context(), &result, nil, &cycleErr, func() error {
				stops++
				return test.stopErr
			})
			if stops != 1 || !errors.Is(cycleErr, originalErr) {
				t.Fatalf("return lost its original error: stops=%d err=%v", stops, cycleErr)
			}
			if test.stopErr != nil && !errors.Is(cycleErr, leaseErr) {
				t.Fatalf("return lost the unconsumed lease error: %v", cycleErr)
			}
			if test.name != "independent_errors" && cycleErr != test.cycleErr {
				t.Fatalf("return rewrote an already-consumed or joined error: %v", cycleErr)
			}
		})
	}

	_, repo, _ := newTranscriptWebFixture(t)
	server.transcriptStore = repo
	for _, status := range []string{"completed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			stream, err := repo.CreateStream(ctx, transcriptstore.CreateStreamInput{
				UID: "heartbeat-return-" + status, OwnerID: "owner", ExternalID: status,
				SessionID: status, Kind: transcriptstore.StreamKindStandalone, Epoch: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := repo.AppendUserEvent(ctx, transcriptstore.AppendUserEventInput{
				StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "return-input",
				PayloadJSON: []byte(`{"content":"preserve return errors"}`),
			}); err != nil {
				t.Fatal(err)
			}
			claimed, err := repo.ClaimRunner(ctx, transcriptstore.ClaimRunnerInput{
				StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "return-runner",
				TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
			})
			if err != nil || !claimed.Claimed {
				t.Fatalf("claim=%#v err=%v", claimed, err)
			}
			_, terminal, _, err := repo.FinishRunner(ctx, transcriptstore.FinishRunnerInput{
				Claim: claimed.Claim, ClientMessageID: "return-terminal", Status: status,
				PayloadJSON: []byte(`{"status":"` + status + `"}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			authority := &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim}
			for _, priorErr := range []error{nil, originalErr, context.Canceled} {
				result := SessionRunnerCycleResult{}
				cycleErr := priorErr
				server.settleSessionRunnerHeartbeatOnReturn(ctx, &result, authority, &cycleErr, func() error {
					return transcriptstore.ErrClaimStale
				})
				if cycleErr != priorErr || result.Status != status || result.FinishEventID != terminal.EventID {
					t.Fatalf("terminal reconciliation changed return semantics: prior=%v err=%v result=%#v", priorErr, cycleErr, result)
				}
			}
		})
	}
}

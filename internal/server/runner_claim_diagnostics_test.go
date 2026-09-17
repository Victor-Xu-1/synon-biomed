package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func logTranscriptRunnerClaimState(t *testing.T, repo *transcriptstore.Repository, streamUID, ownerID string, claim *transcriptstore.RunnerClaim) {
	t.Helper()
	t.Logf("runner diagnostic now=%s stream=%s owner=%s", time.Now().UTC(), streamUID, ownerID)
	if claim != nil {
		t.Logf("caller claim runner=%s attempt=%d inputRevision=%d claimedAt=%s expiresAt=%s",
			claim.RunnerID, claim.Attempt, claim.ClaimedInputRevision, claim.ClaimedAt, claim.ExpiresAt)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	state, found, err := repo.GetLatestRunnerRuntimeState(ctx, streamUID, ownerID)
	stateJSON, marshalErr := json.Marshal(state)
	t.Logf("durable runner=%s found=%t err=%v marshalErr=%v", stateJSON, found, err, marshalErr)
}

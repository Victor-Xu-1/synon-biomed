package server

import (
	"encoding/json"
	"testing"
	"time"

	eventjournal "synon-go/internal/persistence/journal"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestTrustedScientificCorrectionReplayDropsStaleUnavailableSourceSignal(t *testing.T) {
	attempt := int64(3)
	payload, err := json.Marshal(eventjournal.Message{
		"status": "completed", "toolPhase": "completed", "toolName": "fetch_article_fulltext",
		"toolResult": map[string]any{
			"ok": true,
			"result": map[string]any{
				"available": false, "status": "not_available", "body": "",
				"sourceUrl": "https://www.ebi.ac.uk/europepmc/webservices/rest/articles/PMC1/fullTextXML",
			},
		},
		trustedScientificReviewSignalsField: []any{
			"scientific-tool:fetch_article_fulltext", "source-host:www.ebi.ac.uk",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	authority := &transcriptRunnerAuthority{
		Stream: transcriptstore.Stream{SessionID: "session-1"},
		Claim:  transcriptstore.RunnerClaim{Attempt: attempt, RunnerID: "runner-1"},
	}
	receipt, found, err := trustedScientificCorrectionReplayReceipt(authority, transcriptstore.RunnerReplayEvent{
		Event: transcriptstore.Event{
			EventID: 9, Type: "runner_checkpoint", RunnerAttempt: &attempt, CreatedAt: time.Now(),
		},
		ResolvedPayloadJSON: payload,
	})
	if err != nil || found {
		t.Fatalf("unavailable source must not produce correction receipt: found=%t receipt=%#v err=%v", found, receipt, err)
	}
}

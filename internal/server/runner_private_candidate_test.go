package server

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestPrivateProviderCandidatePreservesLargeUTF8OutsidePublicHistory(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	run := &sessionRunnerChatRun{SessionID: fixture.stream.SessionID, Transcript: &transcriptRunnerAuthority{
		Stream: fixture.stream, Claim: fixture.claim,
	}}
	want := strings.Repeat("中\"\n", transcriptstore.MaxEventPayloadBytes/16)
	if err := fixture.server.checkpointPrivateProviderCandidate(context.Background(), run, want); err != nil {
		t.Fatal(err)
	}
	if run.ProviderContinuation.Content.String() != want || run.ProviderContinuation.PrivateCandidate.String() != want {
		t.Fatal("private candidate bytes were lost before continuation")
	}
	rows, err := fixture.db.Query(`SELECT event_type, payload_json FROM transcript_events WHERE stream_uid = ? ORDER BY event_id`, fixture.stream.UID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var restored strings.Builder
	chunks := 0
	for rows.Next() {
		var kind string
		var raw []byte
		if err := rows.Scan(&kind, &raw); err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&payload); err != nil {
			t.Fatal(err)
		}
		text, present, err := privateProviderCandidateText(payload)
		if err != nil {
			t.Fatal(err)
		}
		if !present {
			continue
		}
		if kind != "runner_checkpoint" || len(raw) > transcriptstore.MaxEventPayloadBytes || !utf8.ValidString(text) {
			t.Fatal("candidate was published or exceeded a durable event boundary")
		}
		restored.WriteString(text)
		chunks++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if restored.String() != want || chunks < 2 {
		t.Fatalf("private chunks=%d bytes=%d", chunks, restored.Len())
	}
}

func TestPrivateProviderCandidateRejectsMalformedContract(t *testing.T) {
	for _, raw := range []any{nil, "draft", map[string]any{"version": json.Number("1.5"), "text": "draft"}, map[string]any{"version": 1, "text": ""}, map[string]any{"version": 1, "text": "draft", "extra": true}} {
		if _, present, err := privateProviderCandidateText(map[string]any{"provider_private_candidate": raw}); !present || err == nil {
			t.Fatalf("invalid contract accepted: %#v", raw)
		}
	}
}

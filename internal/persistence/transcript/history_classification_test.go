package transcript

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAuditAskUserHistoryPersistsNativeTypedClassificationIdempotently(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	ctx := context.Background()
	stream, claim := seedAskUserHistoryClassificationFrame(t, repo, db, "native", true)

	first, created, err := repo.AuditAskUserHistory(ctx, auditAskUserHistoryInput(stream, ""))
	if err != nil || !created {
		t.Fatalf("first=%#v created=%t err=%v", first, created, err)
	}
	if first.Status != AskUserHistoryNativeV1 || first.ReasonCode != AskUserHistoryReasonNativeV1 ||
		first.CandidateCount != 1 || first.ThroughOrdinal != 5 || first.ThroughPublicationSequence != 5 ||
		len(first.SourceSHA256) != 64 || len(first.RunID) != 64 || first.BranchID == "" || first.BranchGeneration != 1 ||
		len(first.Comparisons) != len(askUserHistoryShadowDimensions) {
		t.Fatalf("native classification=%#v", first)
	}
	if len(first.Details) != 1 || first.Details[0].ToolUseID != "ask-native" ||
		first.Details[0].RunnerAttempt != claim.Attempt || first.Details[0].Status != AskUserHistoryNativeV1 {
		t.Fatalf("native details=%#v claim=%#v", first.Details, claim)
	}
	for _, comparison := range first.Comparisons {
		if comparison.Verdict != AskUserHistoryShadowMatch {
			t.Fatalf("native shadow comparison=%#v", comparison)
		}
	}
	repeated, repeatedCreated, err := repo.AuditAskUserHistory(ctx, auditAskUserHistoryInput(stream, first.BranchID))
	if err != nil || repeatedCreated || repeated.SourceSHA256 != first.SourceSHA256 {
		t.Fatalf("repeated=%#v created=%t err=%v", repeated, repeatedCreated, err)
	}
	stored, found, err := repo.GetAskUserHistoryAudit(ctx, GetAskUserHistoryAuditInput{
		RunID: first.RunID, StreamUID: stream.UID, OwnerID: stream.OwnerID,
	})
	if err != nil || !found || stored.SourceSHA256 != first.SourceSHA256 || len(stored.Details) != 1 {
		t.Fatalf("stored=%#v found=%t err=%v", stored, found, err)
	}
}

func TestAuditAskUserHistoryIgnoresOrdinaryToolsButRejectsStructuredOrphanAskUserResult(t *testing.T) {
	t.Run("ordinary tool pair", func(t *testing.T) {
		repo, db, _ := newTranscriptRepository(t)
		stream, _ := seedAskUserHistoryClassificationFrame(t, repo, db, "ordinary-tool", true)
		appendHistoryFrameReferences(t, repo, db, stream, []historyFrameReferenceFixture{
			{
				id: "history-budget-ordinary-tool-use", eventType: "assistant_message",
				payload: map[string]any{"role": "assistant", "content": []any{map[string]any{
					"type": "tool_use", "id": "ordinary-call", "name": "Bash", "input": map[string]any{"command": "pwd"},
				}}},
			},
			{
				id: "history-budget-ordinary-tool-result", eventType: "user_message",
				payload: map[string]any{"role": "user", "content": []any{map[string]any{
					"type": "tool_result", "tool_use_id": "ordinary-call",
					"content": `{"status":"cancelled"}`, "is_error": false,
				}}},
			},
		})
		audit, created, err := repo.AuditAskUserHistory(context.Background(), auditAskUserHistoryInput(stream, ""))
		if err != nil || !created || audit.Status != AskUserHistoryNativeV1 || audit.CandidateCount != 1 ||
			audit.Details[0].ToolUseID != "ask-ordinary-tool" {
			t.Fatalf("ordinary tool audit=%#v created=%t err=%v", audit, created, err)
		}
	})

	t.Run("structured orphan AskUser result", func(t *testing.T) {
		repo, db, _ := newTranscriptRepository(t)
		stream, _ := seedAskUserHistoryClassificationFrame(t, repo, db, "orphan-result", true)
		appendHistoryFrameReferences(t, repo, db, stream, []historyFrameReferenceFixture{{
			id: "history-budget-orphan-ask-result", eventType: "user_message",
			payload: map[string]any{"role": "user", "content": []any{map[string]any{
				"type": "tool_result", "tool_use_id": "orphan-ask-call",
				"content": `{"version":1,"status":"cancelled","action":"cancel"}`, "is_error": true,
			}}},
		}})
		audit, created, err := repo.AuditAskUserHistory(context.Background(), auditAskUserHistoryInput(stream, ""))
		if err != nil || !created || audit.Status != AskUserHistoryConflict || audit.CandidateCount != 2 {
			t.Fatalf("orphan AskUser audit=%#v created=%t err=%v", audit, created, err)
		}
		orphanFound := false
		for _, detail := range audit.Details {
			if detail.ToolUseID == "orphan-ask-call" && detail.ReasonCode == AskUserHistoryReasonIncompletePair {
				orphanFound = true
			}
		}
		if !orphanFound {
			t.Fatalf("orphan AskUser details=%#v", audit.Details)
		}
	})
}

func TestAuditAskUserHistoryQuarantinesLegacyProseAndDetectsSourceMutation(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	ctx := context.Background()
	stream, _ := seedAskUserHistoryClassificationFrame(t, repo, db, "legacy", false)

	first, created, err := repo.AuditAskUserHistory(ctx, auditAskUserHistoryInput(stream, ""))
	if err != nil || !created {
		t.Fatalf("first=%#v created=%t err=%v", first, created, err)
	}
	if first.Status != AskUserHistoryQuarantined || first.ReasonCode != AskUserHistoryReasonAmbiguousProse ||
		first.CandidateCount != 1 || len(first.Details) != 1 ||
		first.Details[0].ReasonCode != AskUserHistoryReasonAmbiguousProse {
		t.Fatalf("legacy classification=%#v", first)
	}
	foreign := auditAskUserHistoryInput(stream, first.BranchID)
	foreign.OwnerID = "foreign"
	if _, _, err := repo.AuditAskUserHistory(ctx, foreign); !errors.Is(err, ErrOwnerMismatch) {
		t.Fatalf("foreign audit error=%v", err)
	}
	var foreignRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_history_classification_runs WHERE stream_uid=?`,
		stream.UID).Scan(&foreignRows); err != nil || foreignRows != 1 {
		t.Fatalf("foreign rows=%d err=%v", foreignRows, err)
	}

	mutated, err := json.Marshal(map[string]any{
		"role": "user", "content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": "ask-legacy", "content": `{"version":1,"status":"cancelled","action":"cancel"}`,
			"is_error": true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE frame_events SET payload=? WHERE id='legacy-result'`, string(mutated)); err != nil {
		t.Fatal(err)
	}
	mutatedAudit, mutatedCreated, err := repo.AuditAskUserHistory(ctx, auditAskUserHistoryInput(stream, first.BranchID))
	if err != nil || !mutatedCreated || mutatedAudit.RunID == first.RunID || mutatedAudit.SourceSHA256 == first.SourceSHA256 ||
		mutatedAudit.Status != AskUserHistoryQuarantined || mutatedAudit.ReasonCode != AskUserHistoryReasonMissingRunnerAttempt {
		t.Fatalf("mutated audit=%#v created=%t err=%v", mutatedAudit, mutatedCreated, err)
	}
	var retainedRuns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_history_classification_runs WHERE stream_uid=?`,
		stream.UID).Scan(&retainedRuns); err != nil || retainedRuns != 2 {
		t.Fatalf("retained runs=%d err=%v", retainedRuns, err)
	}
}

func TestAuditAskUserHistoryRetainsTypedAuthorityAndRecordsFrameShadowDrift(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	ctx := context.Background()
	stream, _ := seedAskUserHistoryClassificationFrame(t, repo, db, "frame-drift", true)
	first, created, err := repo.AuditAskUserHistory(ctx, auditAskUserHistoryInput(stream, ""))
	if err != nil || !created || first.Status != AskUserHistoryNativeV1 {
		t.Fatalf("first=%#v created=%t err=%v", first, created, err)
	}
	mutated, _ := json.Marshal(map[string]any{
		"role": "user", "content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": "ask-frame-drift",
			"content": `{"version":1,"status":"cancelled","action":"cancel"}`, "is_error": true,
		}},
	})
	if _, err := db.Exec(`UPDATE frame_events SET payload=? WHERE id='frame-drift-result'`, mutated); err != nil {
		t.Fatal(err)
	}
	second, secondCreated, err := repo.AuditAskUserHistory(ctx, auditAskUserHistoryInput(stream, first.BranchID))
	if err != nil || !secondCreated || second.Status != AskUserHistoryNativeV1 ||
		second.RunID == first.RunID || second.SourceSHA256 == first.SourceSHA256 {
		t.Fatalf("second=%#v created=%t err=%v", second, secondCreated, err)
	}
	if shadowVerdict(second, AskUserHistoryShadowStableIDs) != AskUserHistoryShadowMatch ||
		shadowVerdict(second, AskUserHistoryShadowStates) != AskUserHistoryShadowMismatch {
		t.Fatalf("shadow comparisons=%#v", second.Comparisons)
	}
	var retained int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_history_classification_runs WHERE stream_uid=?`, stream.UID).
		Scan(&retained); err != nil || retained != 2 {
		t.Fatalf("retained runs=%d err=%v", retained, err)
	}
}

func TestAuditAskUserHistoryDetectsTerminalAndArtifactShadowDrift(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	ctx := context.Background()
	stream, claim := seedAskUserHistoryClassificationFrame(t, repo, db, "terminal-artifact", true)
	var sourceEventID int64
	if err := db.QueryRow(`SELECT event_id FROM transcript_events WHERE stream_uid=?
		AND client_message_id='ask-user:terminal-artifact:prompt'`, stream.UID).Scan(&sourceEventID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO artifacts(id,project_id) VALUES('artifact-shadow',?)`, stream.ProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO artifact_versions(id,artifact_id) VALUES('version-shadow','artifact-shadow')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_artifact_refs(
			stream_uid,runner_attempt,source_event_id,ordinal,artifact_id,version_id,relation,availability,created_at
		) VALUES(?,?,?,?,?,'version-shadow','produced','available',?)`,
		stream.UID, claim.Attempt, sourceEventID, 0, "artifact-shadow", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	first, created, err := repo.AuditAskUserHistory(ctx, auditAskUserHistoryInput(stream, ""))
	if err != nil || !created || shadowVerdict(first, AskUserHistoryShadowTerminal) != AskUserHistoryShadowMatch ||
		shadowVerdict(first, AskUserHistoryShadowArtifacts) != AskUserHistoryShadowMatch {
		t.Fatalf("first=%#v created=%t err=%v", first, created, err)
	}
	if _, err := db.Exec(`UPDATE frames SET status='completed' WHERE id=?; DELETE FROM artifact_versions WHERE id='version-shadow'`,
		stream.FrameID); err != nil {
		t.Fatal(err)
	}
	second, secondCreated, err := repo.AuditAskUserHistory(ctx, auditAskUserHistoryInput(stream, ""))
	if err != nil || !secondCreated || second.Status != AskUserHistoryNativeV1 ||
		shadowVerdict(second, AskUserHistoryShadowTerminal) != AskUserHistoryShadowMismatch ||
		shadowVerdict(second, AskUserHistoryShadowArtifacts) != AskUserHistoryShadowMismatch ||
		shadowVerdict(second, AskUserHistoryShadowStates) != AskUserHistoryShadowMatch {
		t.Fatalf("second=%#v created=%t err=%v", second, secondCreated, err)
	}
}

func TestAuditAskUserHistoryRejectsSourceMutationBetweenPhasesWithoutLedgerRows(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *sql.DB, Stream)
	}{
		{
			name: "publication sequence",
			mutate: func(t *testing.T, db *sql.DB, stream Stream) {
				_, err := db.Exec(`UPDATE transcript_events SET publication_seq=40
					WHERE stream_uid=? AND client_message_id='ask-user:phase-publication-sequence:prompt'`, stream.UID)
				if err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "runner attempt",
			mutate: func(t *testing.T, db *sql.DB, stream Stream) {
				_, err := db.Exec(`UPDATE transcript_events SET runner_attempt=NULL
					WHERE stream_uid=? AND client_message_id='ask-user:phase-runner-attempt:prompt'`, stream.UID)
				if err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "branch lineage",
			mutate: func(t *testing.T, db *sql.DB, stream Stream) {
				_, err := db.Exec(`UPDATE transcript_branches SET request_sha256=randomblob(32) WHERE stream_uid=?`, stream.UID)
				if err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "branch generation",
			mutate: func(t *testing.T, db *sql.DB, stream Stream) {
				_, err := db.Exec(`UPDATE transcript_branch_state SET generation=generation+1 WHERE stream_uid=?`, stream.UID)
				if err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "frame incarnation",
			mutate: func(t *testing.T, db *sql.DB, stream Stream) {
				_, err := db.Exec(`UPDATE frames SET incarnation_id=incarnation_id||'-new' WHERE id=?`, stream.FrameID)
				if err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo, db, _ := newTranscriptRepository(t)
			suffix := "phase-" + strings.ReplaceAll(test.name, " ", "-")
			stream, _ := seedAskUserHistoryClassificationFrame(t, repo, db, suffix, true)
			entered := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			repo.now = func() time.Time {
				once.Do(func() { close(entered) })
				<-release
				return time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
			}
			type result struct{ err error }
			resultChannel := make(chan result, 1)
			go func() {
				_, _, err := repo.AuditAskUserHistory(context.Background(), auditAskUserHistoryInput(stream, ""))
				resultChannel <- result{err: err}
			}()
			<-entered
			test.mutate(t, db, stream)
			close(release)
			if got := <-resultChannel; !errors.Is(got.err, ErrBranchStateStale) {
				t.Fatalf("audit error=%v", got.err)
			}
			var rows int
			if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_history_classification_runs`).Scan(&rows); err != nil || rows != 0 {
				t.Fatalf("ledger rows=%d err=%v", rows, err)
			}
		})
	}
}

func TestAuditAskUserHistoryUsesCallerResourceBudgetsWithoutLedgerWrites(t *testing.T) {
	t.Run("events", func(t *testing.T) {
		repo, db, _ := newTranscriptRepository(t)
		stream, _ := seedAskUserHistoryClassificationFrame(t, repo, db, "budget-events", true)
		input := auditAskUserHistoryInput(stream, "")
		input.MaxEvents = 1
		_, _, err := repo.AuditAskUserHistory(context.Background(), input)
		assertHistoryAuditBudgetError(t, err)
		assertNoAskUserHistoryAuditRows(t, db)
	})

	t.Run("candidates", func(t *testing.T) {
		repo, db, _ := newTranscriptRepository(t)
		stream, _ := seedAskUserHistoryClassificationFrame(t, repo, db, "budget-candidates", true)
		if _, err := db.Exec(`UPDATE transcript_events SET payload_json=json_set(payload_json,'$.version',2)
			WHERE stream_uid=? AND client_message_id='ask-user:budget-candidates:prompt'`, stream.UID); err != nil {
			t.Fatal(err)
		}
		input := auditAskUserHistoryInput(stream, "")
		input.MaxCandidates = 1
		_, _, err := repo.AuditAskUserHistory(context.Background(), input)
		assertHistoryAuditBudgetError(t, err)
		assertNoAskUserHistoryAuditRows(t, db)
	})

	t.Run("legacy candidates are bounded on first discovery", func(t *testing.T) {
		repo, db, _ := newTranscriptRepository(t)
		stream, _ := seedAskUserHistoryClassificationFrame(t, repo, db, "budget-legacy-candidates", true)
		questionInput := map[string]any{"questions": []any{map[string]any{
			"question": "Choose", "header": "Choice", "options": []any{
				map[string]any{"label": "A", "description": "First"},
				map[string]any{"label": "B", "description": "Second"},
			}, "multiSelect": false,
		}}}
		appendHistoryFrameReferences(t, repo, db, stream, []historyFrameReferenceFixture{{
			id: "history-budget-many-ask-user-tools", eventType: "assistant_message",
			payload: map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": "ask-budget-extra-a", "name": "ask_user", "input": questionInput},
				map[string]any{"type": "tool_use", "id": "ask-budget-extra-b", "name": "ask_user", "input": questionInput},
			}},
		}})
		input := auditAskUserHistoryInput(stream, "")
		input.MaxCandidates = 2
		_, _, err := repo.AuditAskUserHistory(context.Background(), input)
		assertHistoryAuditBudgetError(t, err)
		assertNoAskUserHistoryAuditRows(t, db)
	})

	t.Run("shadow rows", func(t *testing.T) {
		repo, db, _ := newTranscriptRepository(t)
		stream, claim := seedAskUserHistoryClassificationFrame(t, repo, db, "budget-shadow", true)
		var sourceEventID int64
		if err := db.QueryRow(`SELECT event_id FROM transcript_events WHERE stream_uid=?
			AND client_message_id='ask-user:budget-shadow:prompt'`, stream.UID).Scan(&sourceEventID); err != nil {
			t.Fatal(err)
		}
		for ordinal := 0; ordinal < 2; ordinal++ {
			artifactID := fmt.Sprintf("artifact-budget-%d", ordinal)
			versionID := fmt.Sprintf("version-budget-%d", ordinal)
			if _, err := db.Exec(`INSERT INTO artifacts(id,project_id) VALUES(?,?)`, artifactID, stream.ProjectID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO artifact_versions(id,artifact_id) VALUES(?,?)`, versionID, artifactID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO transcript_artifact_refs(
				stream_uid,runner_attempt,source_event_id,ordinal,artifact_id,version_id,relation,availability,created_at
			) VALUES(?,?,?,?,?,?,'produced','available',?)`,
				stream.UID, claim.Attempt, sourceEventID, ordinal, artifactID, versionID, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
		}
		input := auditAskUserHistoryInput(stream, "")
		input.MaxShadowRows = 1
		_, _, err := repo.AuditAskUserHistory(context.Background(), input)
		assertHistoryAuditBudgetError(t, err)
		assertNoAskUserHistoryAuditRows(t, db)
	})
}

func TestAuditAskUserHistoryFailClosedLegacyMatrix(t *testing.T) {
	tests := []struct {
		name   string
		reason AskUserHistoryReasonCode
		mutate func(*testing.T, *sql.DB, string)
	}{
		{
			name: "english prose", reason: AskUserHistoryReasonAmbiguousProse,
			mutate: func(t *testing.T, db *sql.DB, suffix string) {},
		},
		{
			name: "wrong role", reason: AskUserHistoryReasonMalformedFact,
			mutate: func(t *testing.T, db *sql.DB, suffix string) {
				_, err := db.Exec(`UPDATE frame_events SET payload=json_set(payload,'$.role','assistant') WHERE id=?`, suffix+"-result")
				if err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "missing is error", reason: AskUserHistoryReasonMalformedFact,
			mutate: func(t *testing.T, db *sql.DB, suffix string) {
				_, err := db.Exec(`UPDATE frame_events SET payload=json_remove(payload,'$.content[0].is_error') WHERE id=?`, suffix+"-result")
				if err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "duplicate result key", reason: AskUserHistoryReasonMalformedFact,
			mutate: func(t *testing.T, db *sql.DB, suffix string) {
				_, err := db.Exec(`UPDATE frame_events SET payload=json_set(payload,'$.content[0].content',?) WHERE id=?`,
					`{"status":"answered","answers":{"Which structure?":"5FQD"},"answers":{"Which structure?":"Predicted"}}`, suffix+"-result")
				if err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "answer coverage", reason: AskUserHistoryReasonAnswerKeyMismatch,
			mutate: func(t *testing.T, db *sql.DB, suffix string) {
				_, err := db.Exec(`UPDATE frame_events SET payload=json_set(payload,'$.content[0].content',?) WHERE id=?`,
					`{"status":"answered","answers":{"Different question":"5FQD"}}`, suffix+"-result")
				if err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo, db, _ := newTranscriptRepository(t)
			suffix := "strict-" + strings.ReplaceAll(test.name, " ", "-")
			stream, _ := seedAskUserHistoryClassificationFrame(t, repo, db, suffix, false)
			test.mutate(t, db, suffix)
			audit, created, err := repo.AuditAskUserHistory(
				context.Background(), auditAskUserHistoryInput(stream, ""),
			)
			if err != nil || !created || audit.Status != AskUserHistoryQuarantined || audit.ReasonCode != test.reason {
				var toolPayload, resultPayload string
				_ = db.QueryRow(`SELECT payload FROM frame_events WHERE id=?`, suffix+"-tool").Scan(&toolPayload)
				_ = db.QueryRow(`SELECT payload FROM frame_events WHERE id=?`, suffix+"-result").Scan(&resultPayload)
				t.Fatalf("audit status=%s reason=%s want=%s created=%t err=%v tool=%s result=%s",
					audit.Status, audit.ReasonCode, test.reason, created, err, toolPayload, resultPayload)
			}
		})
	}
}

func TestAuditAskUserHistoryFailClosedTypedCorruptionAndLedgerTampering(t *testing.T) {
	t.Run("malformed typed prompt", func(t *testing.T) {
		repo, db, _ := newTranscriptRepository(t)
		stream, _ := seedAskUserHistoryClassificationFrame(t, repo, db, "typed-corrupt", true)
		if _, err := db.Exec(`UPDATE transcript_events SET payload_json=json_set(payload_json,'$.version',2)
			WHERE stream_uid=? AND client_message_id='ask-user:typed-corrupt:prompt'`, stream.UID); err != nil {
			t.Fatal(err)
		}
		audit, created, err := repo.AuditAskUserHistory(
			context.Background(), auditAskUserHistoryInput(stream, ""),
		)
		if err != nil || !created || audit.Status != AskUserHistoryQuarantined ||
			audit.ReasonCode != AskUserHistoryReasonMalformedFact || audit.NativeCount != 0 || audit.PoisonCount == 0 {
			t.Fatalf("audit=%#v created=%t err=%v", audit, created, err)
		}
	})
	t.Run("evidence digest", func(t *testing.T) {
		repo, db, _ := newTranscriptRepository(t)
		stream, _ := seedAskUserHistoryClassificationFrame(t, repo, db, "ledger-tamper", true)
		audit, created, err := repo.AuditAskUserHistory(
			context.Background(), auditAskUserHistoryInput(stream, ""),
		)
		if err != nil || !created {
			t.Fatalf("audit=%#v created=%t err=%v", audit, created, err)
		}
		if _, err := db.Exec(`UPDATE transcript_history_classification_candidates
			SET evidence_sha256=zeroblob(32) WHERE run_id=?`, mustDecodeHistoryHex(t, audit.RunID)); err != nil {
			t.Fatal(err)
		}
		if _, _, err := repo.GetAskUserHistoryAudit(context.Background(), GetAskUserHistoryAuditInput{
			RunID: audit.RunID, StreamUID: stream.UID, OwnerID: stream.OwnerID,
		}); !errors.Is(err, ErrEventConflict) {
			t.Fatalf("tampered ledger error=%v", err)
		}
	})
}

func mustDecodeHistoryHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func auditAskUserHistoryInput(stream Stream, branchID string) AuditAskUserHistoryInput {
	return AuditAskUserHistoryInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, BranchID: branchID,
		MaxEvents: 64, MaxCandidates: 16, MaxShadowRows: 64,
	}
}

func assertNoAskUserHistoryAuditRows(t *testing.T, db *sql.DB) {
	t.Helper()
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_history_classification_runs`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("history audit rows=%d err=%v", rows, err)
	}
}

func assertHistoryAuditBudgetError(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrHistoryAuditBudgetExceeded) || errors.Is(err, ErrSchemaUnavailable) {
		t.Fatalf("history audit budget error=%v", err)
	}
}

type historyFrameReferenceFixture struct {
	id        string
	eventType string
	payload   map[string]any
}

func appendHistoryFrameReferences(
	t *testing.T,
	repo *Repository,
	db *sql.DB,
	stream Stream,
	fixtures []historyFrameReferenceFixture,
) {
	t.Helper()
	var nextSequence int
	if err := db.QueryRow(`SELECT COALESCE(MAX(sequence),0)+1 FROM frame_events WHERE frame_id=?`, stream.FrameID).
		Scan(&nextSequence); err != nil {
		t.Fatal(err)
	}
	eventIDs := make([]string, 0, len(fixtures))
	for index, fixture := range fixtures {
		payload, err := json.Marshal(fixture.payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
			VALUES(?,?,?,?,?,?)`, fixture.id, stream.FrameID, nextSequence+index, fixture.eventType, payload, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		eventIDs = append(eventIDs, fixture.id)
	}
	if err := repo.RunImmediate(context.Background(), func(tx *ImmediateTransaction) error {
		_, err := tx.AppendHistoricalFrameReferences(context.Background(), stream.UID, stream.OwnerID, eventIDs)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func shadowVerdict(audit AskUserHistoryAudit, dimension AskUserHistoryShadowDimension) AskUserHistoryShadowVerdict {
	for _, comparison := range audit.Comparisons {
		if comparison.Dimension == dimension {
			return comparison.Verdict
		}
	}
	return ""
}

func seedAskUserHistoryClassificationFrame(
	t *testing.T,
	repo *Repository,
	db *sql.DB,
	suffix string,
	typed bool,
) (Stream, RunnerClaim) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	ownerID := "owner-" + suffix
	projectID := "project-" + suffix
	frameID := "frame-" + suffix
	toolID := "ask-" + suffix
	toolEventID := suffix + "-tool"
	resultEventID := suffix + "-result"
	var incarnationColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('frames') WHERE name='incarnation_id'`).
		Scan(&incarnationColumns); err != nil {
		t.Fatal(err)
	}
	if incarnationColumns == 0 {
		if _, err := db.Exec(`ALTER TABLE frames ADD COLUMN incarnation_id TEXT NOT NULL DEFAULT ''`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO projects(id,user_id) VALUES(?,?)`, projectID, ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO frames(id,incarnation_id,project_id,root_frame_id,status,updated_at) VALUES(?,?,?,?,?,?)`,
		frameID, "incarnation-"+suffix, projectID, frameID, "processing", now); err != nil {
		t.Fatal(err)
	}
	stream, err := repo.CreateStream(ctx, CreateStreamInput{
		UID: "frame:" + frameID, OwnerID: ownerID, ExternalID: frameID, SessionID: frameID,
		Kind: StreamKindFrameRef, ProjectID: projectID, RootFrameID: frameID, FrameID: frameID, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	forceLegacyFrameAuthorityForTest(t, db, stream)
	questions := []any{map[string]any{
		"question": "Which structure?", "header": "Structure",
		"options": []any{
			map[string]any{"label": "5FQD", "description": "Use the human complex."},
			map[string]any{"label": "Predicted", "description": "Use the predicted structure."},
		},
		"multiSelect": false,
	}}
	toolPayload, _ := json.Marshal(map[string]any{
		"role": "assistant", "content": []any{map[string]any{
			"type": "tool_use", "id": toolID, "name": "ask_user", "input": map[string]any{"questions": questions},
		}},
	})
	resultPayload, _ := json.Marshal(map[string]any{
		"role": "user", "content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": toolID, "content": `{"status":"awaiting_user_response"}`, "is_error": true,
		}},
	})
	for sequence, item := range []struct {
		id, eventType string
		payload       []byte
	}{
		{id: toolEventID, eventType: "assistant_message", payload: toolPayload},
		{id: resultEventID, eventType: "user_message", payload: resultPayload},
	} {
		if _, err := db.Exec(`INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at) VALUES(?,?,?,?,?,?)`,
			item.id, frameID, sequence+1, item.eventType, string(item.payload), now); err != nil {
			t.Fatal(err)
		}
	}
	if _, created, err := repo.AppendUserEvent(ctx, AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: ownerID, ClientMessageID: "seed-input:" + suffix,
		PayloadJSON: []byte(`{"text":"seed AskUser work"}`),
	}); err != nil || !created {
		t.Fatalf("seed input created=%t err=%v", created, err)
	}
	claim, err := repo.ClaimRunner(ctx, ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: ownerID, RunnerID: "runner-" + suffix, TTL: time.Minute,
		ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	err = repo.RunImmediate(ctx, func(tx *ImmediateTransaction) error {
		if _, err := tx.AppendFrameAskUserReferences(ctx, AppendFrameAskUserReferencesInput{
			StreamUID: stream.UID, OwnerID: ownerID, FrameID: frameID, ToolUseID: toolID,
			ToolUseFrameEventID: toolEventID, ToolResultFrameEventID: resultEventID,
		}); err != nil {
			return err
		}
		if !typed {
			return nil
		}
		_, err := tx.AppendFrameAskUserPending(ctx, AppendFrameAskUserPendingInput{
			Claim: claim.Claim, ClientMessageID: "ask-user:" + suffix, FrameID: frameID, ToolUseID: toolID,
			ToolUseFrameEventID: toolEventID, PendingFrameEventID: resultEventID,
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !typed {
		legacyPayload, _ := json.Marshal(map[string]any{
			"role": "user", "content": []any{map[string]any{
				"type": "tool_result", "tool_use_id": toolID, "content": "User cancelled this request", "is_error": true,
			}},
		})
		if _, err := db.Exec(`UPDATE frame_events SET payload=? WHERE id=?`, string(legacyPayload), resultEventID); err != nil {
			t.Fatal(err)
		}
	}
	return stream, claim.Claim
}

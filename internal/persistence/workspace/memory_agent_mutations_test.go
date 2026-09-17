package workspace

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestClaimedAgentMemoryMutationIsAtomicIdempotentAndFenced(t *testing.T) {
	store, repo, stream, claim, sourceEventID := newClaimedMemoryMutationFixture(t)
	input := ClaimedAgentMemoryMutationInput{
		Claim: claim, SourceEventID: sourceEventID,
		Batch: AgentMemoryMutationBatch{Append: []AgentMemoryAppendMutation{{Input: CreateMemoryInput{
			ID: "memory-1", Body: "durable EGFR assay fact", Evidence: "observed", SubjectProjectID: stream.ProjectID,
		}}}},
	}
	first, err := store.ApplyClaimedAgentMemoryMutations(context.Background(), input)
	if err != nil || !first.Applied || len(first.Result.Appended) != 1 || first.Result.Appended[0] != "memory-1" {
		t.Fatalf("first mutation=%#v err=%v", first, err)
	}
	second, err := store.ApplyClaimedAgentMemoryMutations(context.Background(), input)
	if err != nil || second.Applied || len(second.Result.Appended) != 1 || second.Result.Appended[0] != "memory-1" || second.Receipt.Event.EventID != first.Receipt.Event.EventID {
		t.Fatalf("replayed mutation=%#v err=%v", second, err)
	}
	conflict := input
	conflict.Batch.Append = append([]AgentMemoryAppendMutation(nil), input.Batch.Append...)
	conflict.Batch.Append[0].Input.Body = "different fact"
	conflictingReplay, err := store.ApplyClaimedAgentMemoryMutations(context.Background(), conflict)
	if err != nil || conflictingReplay.Applied || conflictingReplay.Receipt.Event.EventID != first.Receipt.Event.EventID {
		t.Fatalf("receipt-first replay=%#v err=%v", conflictingReplay, err)
	}
	var memoryCount, receiptCount int
	var payload string
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM memories WHERE id='memory-1'`).Scan(&memoryCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*),MAX(CAST(payload_json AS TEXT)) FROM transcript_events WHERE stream_uid=? AND event_type='memory_mutation_applied'`, stream.UID).Scan(&receiptCount, &payload); err != nil {
		t.Fatal(err)
	}
	if memoryCount != 1 || receiptCount != 1 || strings.Contains(payload, "durable EGFR") || strings.Contains(payload, claim.ClaimToken) {
		t.Fatalf("durable state memory=%d receipts=%d payload=%q", memoryCount, receiptCount, payload)
	}

	if _, err := store.db.Exec(`UPDATE transcript_runner_attempts SET expires_at=? WHERE stream_uid=? AND attempt=?`, time.Now().UTC().Add(-time.Minute), claim.StreamUID, claim.Attempt); err != nil {
		t.Fatal(err)
	}
	staleInput := input
	staleInput.Batch.Append = append([]AgentMemoryAppendMutation(nil), input.Batch.Append...)
	staleInput.Batch.Append[0].Input.ID = "stale-memory"
	if _, err := store.ApplyClaimedAgentMemoryMutations(context.Background(), staleInput); !errors.Is(err, transcriptstore.ErrClaimStale) {
		t.Fatalf("expired claim error=%v", err)
	}
	reclaimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-2", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !reclaimed.Claimed || reclaimed.Claim.Attempt != claim.Attempt+1 {
		t.Fatalf("reclaimed=%#v err=%v", reclaimed, err)
	}
	replayWithNewClaim := input
	replayWithNewClaim.Claim = reclaimed.Claim
	replayed, err := store.ApplyClaimedAgentMemoryMutations(context.Background(), replayWithNewClaim)
	if err != nil || replayed.Applied || replayed.Receipt.Event.EventID != first.Receipt.Event.EventID {
		t.Fatalf("new-attempt replay=%#v err=%v", replayed, err)
	}
}

func TestClaimedAgentMemoryMutationRejectsUnsafeScopesAndRollsBack(t *testing.T) {

	for _, test := range []struct {
		name   string
		mutate func(*transcriptstore.RunnerClaim)
	}{
		{name: "runner id", mutate: func(claim *transcriptstore.RunnerClaim) { claim.RunnerID = "other-runner" }},
		{name: "attempt", mutate: func(claim *transcriptstore.RunnerClaim) { claim.Attempt++ }},
		{name: "claim token", mutate: func(claim *transcriptstore.RunnerClaim) { claim.ClaimToken += "-stale" }},
		{name: "claimed revision", mutate: func(claim *transcriptstore.RunnerClaim) { claim.ClaimedInputRevision++ }},
		{name: "resume identity", mutate: func(claim *transcriptstore.RunnerClaim) {
			claim.ResumeSource, claim.ResumeCheckpoint = transcriptstore.ResumeSourceCheckpoint, 1
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, _, stream, claim, sourceEventID := newClaimedMemoryMutationFixture(t)
			test.mutate(&claim)
			_, err := store.ApplyClaimedAgentMemoryMutations(context.Background(), claimedMemoryAppendInput(claim, sourceEventID, "stale-claim", "stale-memory", stream.ProjectID))
			if !errors.Is(err, transcriptstore.ErrClaimStale) {
				t.Fatalf("stale claim error=%v", err)
			}
			assertMemoryAndReceiptCounts(t, store, 0, 0)
		})
	}

	t.Run("user authored late operation", func(t *testing.T) {
		store, _, stream, claim, sourceEventID := newClaimedMemoryMutationFixture(t)
		if _, err := store.CreateMemory(CreateMemoryInput{ID: "protected", UserID: stream.OwnerID, Body: "user fact", Origin: "user", Evidence: "stated", SubjectProjectID: stream.ProjectID}); err != nil {
			t.Fatal(err)
		}
		input := claimedMemoryAppendInput(claim, sourceEventID, "late-user", "would-append", stream.ProjectID)
		input.Batch.Remove = []AgentMemoryRemoveMutation{{ActiveID: "protected", ExpectedOrigin: "user"}}
		if _, err := store.ApplyClaimedAgentMemoryMutations(context.Background(), input); !errors.Is(err, ErrAgentMemoryMutationForbidden) {
			t.Fatalf("protected error=%v", err)
		}
		var appended int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM memories WHERE id='would-append'`).Scan(&appended); err != nil || appended != 0 {
			t.Fatalf("rolled-back append count=%d err=%v", appended, err)
		}
	})

	t.Run("other project artifact", func(t *testing.T) {
		store, _, stream, claim, sourceEventID := newClaimedMemoryMutationFixture(t)
		if _, err := store.CreateProject(CreateProjectInput{ID: "other-project", UserID: stream.OwnerID, Name: "Other"}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.SaveArtifactVersion(SaveArtifactVersionInput{ArtifactID: "other-artifact", ProjectID: "other-project", Name: "other.md", Kind: "document", Content: []byte("x")}); err != nil {
			t.Fatal(err)
		}
		input := claimedMemoryAppendInput(claim, sourceEventID, "other-project", "other-project-memory", "")
		input.Batch.Append[0].Input.SubjectArtifactID = "other-artifact"
		if _, err := store.ApplyClaimedAgentMemoryMutations(context.Background(), input); !errors.Is(err, ErrAgentMemoryMutationForbidden) {
			t.Fatalf("cross-project error=%v", err)
		}
		assertMemoryAndReceiptCounts(t, store, 0, 0)
	})

	t.Run("artifact version pair", func(t *testing.T) {
		store, _, stream, claim, sourceEventID := newClaimedMemoryMutationFixture(t)
		if _, _, err := store.SaveArtifactVersion(SaveArtifactVersionInput{ArtifactID: "artifact-a", ProjectID: stream.ProjectID, Name: "a.md", Kind: "document", Content: []byte("a")}); err != nil {
			t.Fatal(err)
		}
		_, versionB, err := store.SaveArtifactVersion(SaveArtifactVersionInput{ArtifactID: "artifact-b", ProjectID: stream.ProjectID, Name: "b.md", Kind: "document", Content: []byte("b")})
		if err != nil {
			t.Fatal(err)
		}
		input := claimedMemoryAppendInput(claim, sourceEventID, "pair-mismatch", "pair-memory", "")
		input.Batch.Append[0].Input.SubjectArtifactID = "artifact-a"
		input.Batch.Append[0].Input.SubjectVersionID = versionB.ID
		if _, err := store.ApplyClaimedAgentMemoryMutations(context.Background(), input); err == nil || !strings.Contains(err.Error(), "does not belong") {
			t.Fatalf("artifact/version pair error=%v", err)
		}
		assertMemoryAndReceiptCounts(t, store, 0, 0)
	})

	for _, test := range []struct {
		operation string
		withFrame bool
	}{
		{operation: "replace"},
		{operation: "remove"},
		{operation: "remove-chain"},
		{operation: "replace", withFrame: true},
		{operation: "remove-chain", withFrame: true},
	} {
		name := "existing artifact version pair " + test.operation
		if test.withFrame {
			name += " frame"
		}
		t.Run(name, func(t *testing.T) {
			store, _, stream, claim, sourceEventID := newClaimedMemoryMutationFixture(t)
			if _, _, err := store.SaveArtifactVersion(SaveArtifactVersionInput{ArtifactID: "artifact-a", ProjectID: stream.ProjectID, Name: "a.md", Kind: "document", Content: []byte("a")}); err != nil {
				t.Fatal(err)
			}
			_, versionB, err := store.SaveArtifactVersion(SaveArtifactVersionInput{ArtifactID: "artifact-b", ProjectID: stream.ProjectID, Name: "b.md", Kind: "document", Content: []byte("b")})
			if err != nil {
				t.Fatal(err)
			}
			targetID := "mismatched"
			if test.operation == "remove-chain" {
				targetID = "active"
				if _, err := store.CreateMemory(CreateMemoryInput{ID: targetID, UserID: stream.OwnerID, Body: "active", Origin: "agent_tool", Evidence: "observed", SubjectProjectID: stream.ProjectID}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.CreateMemory(CreateMemoryInput{ID: "mismatched", UserID: stream.OwnerID, Body: "legacy mismatch", Origin: "agent_tool", Evidence: "observed", SubjectArtifactID: "artifact-a"}); err != nil {
				t.Fatal(err)
			}
			subjectFrameID := ""
			if test.withFrame {
				subjectFrameID = stream.FrameID
			}
			if _, err := store.db.Exec(`UPDATE memories SET subject_version_id=?,subject_frame_id=?,superseded_by=? WHERE id='mismatched'`, versionB.ID, nullableString(subjectFrameID), nullableString(func() string {
				if test.operation == "remove-chain" {
					return "active"
				}
				return ""
			}())); err != nil {
				t.Fatal(err)
			}
			input := ClaimedAgentMemoryMutationInput{Claim: claim, SourceEventID: sourceEventID}
			if test.operation == "replace" {
				input.Batch.Replace = []AgentMemoryReplaceMutation{{ActiveID: targetID, ExpectedOrigin: "agent_tool", Body: "changed", Evidence: "observed"}}
			} else {
				input.Batch.Remove = []AgentMemoryRemoveMutation{{ActiveID: targetID, ExpectedOrigin: "agent_tool"}}
			}
			if _, err := store.ApplyClaimedAgentMemoryMutations(context.Background(), input); !errors.Is(err, ErrAgentMemoryMutationForbidden) {
				t.Fatalf("existing pair error=%v", err)
			}
			var receipts int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE event_type='memory_mutation_applied'`).Scan(&receipts); err != nil || receipts != 0 {
				t.Fatalf("receipts=%d err=%v", receipts, err)
			}
		})
	}

	t.Run("other frame scratchpad", func(t *testing.T) {
		store, _, stream, claim, sourceEventID := newClaimedMemoryMutationFixture(t)
		if _, err := store.CreateFrame(CreateFrameInput{ID: "other-frame", ProjectID: stream.ProjectID, AgentName: "OPERON", Status: "processing", ConversationType: "agent"}); err != nil {
			t.Fatal(err)
		}
		input := claimedMemoryAppendInput(claim, sourceEventID, "other-frame", "other-frame-memory", "")
		input.Batch.Append[0].Input.SubjectFrameID = "other-frame"
		if _, err := store.ApplyClaimedAgentMemoryMutations(context.Background(), input); !errors.Is(err, ErrAgentMemoryMutationForbidden) {
			t.Fatalf("cross-frame error=%v", err)
		}
		assertMemoryAndReceiptCounts(t, store, 0, 0)
	})

	t.Run("invalid source event rolls back memory", func(t *testing.T) {
		store, _, stream, claim, _ := newClaimedMemoryMutationFixture(t)
		input := claimedMemoryAppendInput(claim, 9999, "bad-source", "bad-source-memory", stream.ProjectID)
		if _, err := store.ApplyClaimedAgentMemoryMutations(context.Background(), input); !errors.Is(err, transcriptstore.ErrMemoryMutationReceiptConflict) {
			t.Fatalf("source event error=%v", err)
		}
		assertMemoryAndReceiptCounts(t, store, 0, 0)
	})

	for _, test := range []struct {
		name  string
		setup func(*testing.T, *Store, transcriptstore.Stream)
	}{
		{name: "user predecessor", setup: func(t *testing.T, store *Store, stream transcriptstore.Stream) {
			if _, err := store.CreateMemory(CreateMemoryInput{ID: "predecessor", UserID: stream.OwnerID, Body: "protected history", Origin: "user", Evidence: "stated", SubjectProjectID: stream.ProjectID}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "foreign project predecessor", setup: func(t *testing.T, store *Store, stream transcriptstore.Stream) {
			if _, err := store.CreateProject(CreateProjectInput{ID: "foreign-project", UserID: stream.OwnerID, Name: "Foreign"}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.CreateMemory(CreateMemoryInput{ID: "predecessor", UserID: stream.OwnerID, Body: "foreign history", Origin: "agent_tool", Evidence: "observed", SubjectProjectID: "foreign-project"}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "legacy origin predecessor", setup: func(t *testing.T, store *Store, stream transcriptstore.Stream) {
			if _, err := store.CreateMemory(CreateMemoryInput{ID: "predecessor", UserID: stream.OwnerID, Body: "legacy history", Origin: "extractor", Evidence: "observed", SubjectProjectID: stream.ProjectID}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`UPDATE memories SET origin='agent' WHERE id='predecessor'`); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, _, stream, claim, sourceEventID := newClaimedMemoryMutationFixture(t)
			if _, err := store.CreateMemory(CreateMemoryInput{ID: "active", UserID: stream.OwnerID, Body: "active fact", Origin: "agent_tool", Evidence: "observed", SubjectProjectID: stream.ProjectID}); err != nil {
				t.Fatal(err)
			}
			test.setup(t, store, stream)
			if _, err := store.db.Exec(`UPDATE memories SET superseded_by='active' WHERE id='predecessor'`); err != nil {
				t.Fatal(err)
			}
			input := ClaimedAgentMemoryMutationInput{
				Claim: claim, SourceEventID: sourceEventID,
				Batch: AgentMemoryMutationBatch{Remove: []AgentMemoryRemoveMutation{{ActiveID: "active", ExpectedOrigin: "agent_tool"}}},
			}
			if _, err := store.ApplyClaimedAgentMemoryMutations(context.Background(), input); !errors.Is(err, ErrAgentMemoryMutationForbidden) {
				t.Fatalf("protected chain error=%v", err)
			}
			var rows, receipts int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM memories WHERE id IN ('active','predecessor')`).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE event_type='memory_mutation_applied'`).Scan(&receipts); err != nil {
				t.Fatal(err)
			}
			if rows != 2 || receipts != 0 {
				t.Fatalf("protected chain rows=%d receipts=%d", rows, receipts)
			}
		})
	}
}

func TestClaimedAgentMemoryMutationAppliesMixedBatchOrRollsBackAll(t *testing.T) {
	t.Run("mixed success", func(t *testing.T) {
		store, _, stream, claim, sourceEventID := newClaimedMemoryMutationFixture(t)
		for _, input := range []CreateMemoryInput{
			{ID: "replace-me", UserID: stream.OwnerID, Body: "old replace", Origin: "agent_tool", Evidence: "observed", SubjectProjectID: stream.ProjectID},
			{ID: "remove-me", UserID: stream.OwnerID, Body: "old remove", Origin: "agent_tool", Evidence: "observed", SubjectProjectID: stream.ProjectID},
		} {
			if _, err := store.CreateMemory(input); err != nil {
				t.Fatal(err)
			}
		}
		input := ClaimedAgentMemoryMutationInput{
			Claim: claim, SourceEventID: sourceEventID,
			Batch: AgentMemoryMutationBatch{
				Append:  []AgentMemoryAppendMutation{{Input: CreateMemoryInput{ID: "append-me", Body: "new append", Evidence: "observed", SubjectProjectID: stream.ProjectID}}},
				Replace: []AgentMemoryReplaceMutation{{ActiveID: "replace-me", ExpectedOrigin: "agent_tool", Body: "new replace", Evidence: "inferred"}},
				Remove:  []AgentMemoryRemoveMutation{{ActiveID: "remove-me", ExpectedOrigin: "agent_tool"}},
			},
		}
		result, err := store.ApplyClaimedAgentMemoryMutations(context.Background(), input)
		if err != nil || !result.Applied || len(result.Result.Appended) != 1 || len(result.Result.Replaced) != 1 || len(result.Result.Removed) != 1 {
			t.Fatalf("mixed result=%#v err=%v", result, err)
		}
		replaced, err := scanMemory(store.db.QueryRowContext(context.Background(), memorySelect+` WHERE m.id=? AND m.user_id=?`, "replace-me", stream.OwnerID))
		if err != nil || replaced.Body != "new replace" || replaced.Evidence != "inferred" {
			t.Fatalf("replaced=%#v err=%v", replaced, err)
		}
		if _, err := scanMemory(store.db.QueryRowContext(context.Background(), memorySelect+` WHERE m.id=? AND m.user_id=?`, "remove-me", stream.OwnerID)); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("removed err=%v", err)
		}
	})

	t.Run("extractor remove", func(t *testing.T) {
		store, _, stream, claim, sourceEventID := newClaimedMemoryMutationFixture(t)
		if _, err := store.CreateMemory(CreateMemoryInput{ID: "extractor-memory", UserID: stream.OwnerID, Body: "extracted fact", Origin: "extractor", Evidence: "observed", SubjectProjectID: stream.ProjectID}); err != nil {
			t.Fatal(err)
		}
		result, err := store.ApplyClaimedAgentMemoryMutations(context.Background(), ClaimedAgentMemoryMutationInput{
			Claim: claim, SourceEventID: sourceEventID,
			Batch: AgentMemoryMutationBatch{Remove: []AgentMemoryRemoveMutation{{ActiveID: "extractor-memory", ExpectedOrigin: "extractor"}}},
		})
		if err != nil || !result.Applied || len(result.Result.Removed) != 1 || result.Result.Removed[0] != "extractor-memory" {
			t.Fatalf("extractor remove=%#v err=%v", result, err)
		}
	})

	t.Run("superseded precondition", func(t *testing.T) {
		store, _, stream, claim, sourceEventID := newClaimedMemoryMutationFixture(t)
		for _, id := range []string{"active", "replacement"} {
			if _, err := store.CreateMemory(CreateMemoryInput{ID: id, UserID: stream.OwnerID, Body: id, Origin: "agent_tool", Evidence: "observed", SubjectProjectID: stream.ProjectID}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.db.Exec(`UPDATE memories SET superseded_by='replacement' WHERE id='active'`); err != nil {
			t.Fatal(err)
		}
		input := claimedMemoryAppendInput(claim, sourceEventID, "changed-call", "would-append", stream.ProjectID)
		input.Batch.Replace = []AgentMemoryReplaceMutation{{ActiveID: "active", ExpectedOrigin: "agent_tool", Body: "changed", Evidence: "observed"}}
		if _, err := store.ApplyClaimedAgentMemoryMutations(context.Background(), input); !errors.Is(err, ErrAgentMemoryChanged) {
			t.Fatalf("changed error=%v", err)
		}
		var appended, receipts int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM memories WHERE id='would-append'`).Scan(&appended); err != nil {
			t.Fatal(err)
		}
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE event_type='memory_mutation_applied'`).Scan(&receipts); err != nil {
			t.Fatal(err)
		}
		if appended != 0 || receipts != 0 {
			t.Fatalf("partial state append=%d receipt=%d", appended, receipts)
		}
	})
}

func TestClaimedAgentMemoryMutationConcurrentInvocationAppliesOnce(t *testing.T) {
	store, _, stream, claim, sourceEventID := newClaimedMemoryMutationFixture(t)
	input := claimedMemoryAppendInput(claim, sourceEventID, "concurrent-call", "concurrent-memory", stream.ProjectID)
	const callers = 32
	start := make(chan struct{})
	errorsByCaller := make(chan error, callers)
	var applied atomic.Int32
	var wait sync.WaitGroup
	for index := 0; index < callers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			result, err := store.ApplyClaimedAgentMemoryMutations(context.Background(), input)
			if err == nil && result.Applied {
				applied.Add(1)
			}
			errorsByCaller <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errorsByCaller)
	for err := range errorsByCaller {
		if err != nil {
			t.Fatalf("concurrent mutation error=%v", err)
		}
	}
	if applied.Load() != 1 {
		t.Fatalf("applied callers=%d, want 1", applied.Load())
	}
	assertMemoryAndReceiptCounts(t, store, 1, 1)
}

func TestClaimedAgentMemoryMutationReceiptBindsCanonicalPlan(t *testing.T) {
	store, _, stream, claim, sourceEventID := newClaimedMemoryMutationFixture(t)
	input := claimedMemoryAppendInput(claim, sourceEventID, "canonical-plan", "memory-plan", "")
	first, err := store.ApplyClaimedAgentMemoryMutations(context.Background(), input)
	if err != nil || !first.Applied {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	rootFrameID := stream.RootFrameID
	if rootFrameID == "" {
		rootFrameID = stream.FrameID
	}
	canonical := AgentMemoryMutationBatch{
		OwnerUserID: stream.OwnerID, ProjectID: stream.ProjectID, FrameID: rootFrameID,
		Append: []AgentMemoryAppendMutation{{Input: CreateMemoryInput{
			ID: "memory-plan", UserID: stream.OwnerID, Body: "durable agent fact", Origin: "agent_tool",
			Evidence: "observed",
		}}},
	}
	want, err := claimedAgentMemoryMutationPlanDigest(stream, canonical)
	if err != nil {
		t.Fatal(err)
	}
	if first.Receipt.MutationSHA256 != want {
		t.Fatalf("mutation digest=%s want=%s", first.Receipt.MutationSHA256, want)
	}
	other := stream
	other.UID += "-other"
	changed, err := claimedAgentMemoryMutationPlanDigest(other, canonical)
	if err != nil || changed == want {
		t.Fatalf("authority digest=%s want different from %s err=%v", changed, want, err)
	}
}

func TestClaimedAgentMemoryMutationReceiptOnlyReplaysWithoutMutableRows(t *testing.T) {
	store, repo, stream, claim, sourceEventID := newClaimedMemoryMutationFixture(t)
	first, err := store.ApplyClaimedAgentMemoryMutations(context.Background(), ClaimedAgentMemoryMutationInput{
		Claim: claim, SourceEventID: sourceEventID, Batch: AgentMemoryMutationBatch{},
	})
	if err != nil || !first.Applied || len(first.Result.Appended)+len(first.Result.Replaced)+len(first.Result.Removed) != 0 {
		t.Fatalf("receipt-only=%#v err=%v", first, err)
	}
	if _, err := store.CreateMemory(CreateMemoryInput{
		ID: "appeared-later", UserID: stream.OwnerID, Body: "mutable later row", Origin: "agent_tool",
		Evidence: "observed", SubjectProjectID: stream.ProjectID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE transcript_runner_attempts SET expires_at=? WHERE stream_uid=? AND attempt=?`,
		time.Now().UTC().Add(-time.Minute), claim.StreamUID, claim.Attempt); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-later", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !reclaimed.Claimed {
		t.Fatalf("reclaimed=%#v err=%v", reclaimed, err)
	}
	replayed, err := store.ApplyClaimedAgentMemoryMutations(context.Background(), ClaimedAgentMemoryMutationInput{
		Claim: reclaimed.Claim, SourceEventID: sourceEventID,
		Batch: AgentMemoryMutationBatch{Remove: []AgentMemoryRemoveMutation{{ActiveID: "appeared-later", ExpectedOrigin: "agent_tool"}}},
	})
	if err != nil || replayed.Applied || replayed.Receipt.Event.EventID != first.Receipt.Event.EventID {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
	if _, found, err := store.GetMemoryOwned(context.Background(), stream.OwnerID, "appeared-later"); err != nil || !found {
		t.Fatalf("later row found=%t err=%v", found, err)
	}
}

func newClaimedMemoryMutationFixture(
	t *testing.T,
) (*Store, *transcriptstore.Repository, transcriptstore.Stream, transcriptstore.RunnerClaim, int64) {
	t.Helper()
	store, repo, stream, claim := newParkAskUserTranscriptFixture(t)
	_, event, created, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "memory-tool-source", Phase: transcriptstore.RunnerPhaseExecuting,
		PayloadJSON: []byte(`{"toolCallId":"call-memory","toolName":"write_memory","toolPhase":"start","toolInput":{"append":[{"memory":"durable agent fact"}]}}`),
	})
	if err != nil || !created {
		t.Fatalf("append source checkpoint created=%t err=%v", created, err)
	}
	return store, repo, stream, claim, event.EventID
}

func claimedMemoryAppendInput(
	claim transcriptstore.RunnerClaim,
	sourceEventID int64,
	clientMessageID, memoryID, projectID string,
) ClaimedAgentMemoryMutationInput {
	return ClaimedAgentMemoryMutationInput{
		Claim: claim, SourceEventID: sourceEventID,
		Batch: AgentMemoryMutationBatch{Append: []AgentMemoryAppendMutation{{Input: CreateMemoryInput{
			ID: memoryID, Body: "durable agent fact", Evidence: "observed", SubjectProjectID: projectID,
		}}}},
	}
}

func assertMemoryAndReceiptCounts(t *testing.T, store *Store, wantMemories, wantReceipts int) {
	t.Helper()
	var memories, receipts int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM memories`).Scan(&memories); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE event_type='memory_mutation_applied'`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if memories != wantMemories || receipts != wantReceipts {
		t.Fatalf("memories=%d receipts=%d, want %d/%d", memories, receipts, wantMemories, wantReceipts)
	}
}

package transcript

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestRunnerReplaySeparatesSemanticMessagesFromCheckpointVolume(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-replay-volume", "owner-a")
	if _, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "checkpoint-auto-compact", Phase: RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: []byte(`{"status":"completed","toolPhase":"auto_compact","summary":"durable compact summary"}`),
	}); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 240; index++ {
		if _, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
			Claim: claim, ClientMessageID: fmt.Sprintf("checkpoint-%03d", index), Phase: RunnerPhaseExecuting,
			Resumable: true, PayloadJSON: []byte(fmt.Sprintf(`{"status":"running","index":%d}`, index)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index < 7; index++ {
		if _, _, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
			StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, ClientMessageID: fmt.Sprintf("user-extra-%d", index),
			PayloadJSON: []byte(fmt.Sprintf(`{"text":"message %d"}`, index)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	events, err := repo.ListRunnerReplay(context.Background(), ListRunnerReplayInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, MessageLimit: 10, CheckpointLimit: 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	messages, checkpoints := 0, 0
	for _, event := range events {
		switch event.Event.Type {
		case "user_message", "assistant_message":
			messages++
		case "runner_checkpoint":
			checkpoints++
		default:
			t.Fatalf("unexpected replay event=%#v", event.Event)
		}
	}
	if messages != 8 || checkpoints != 13 || len(events) != 21 {
		t.Fatalf("messages=%d checkpoints=%d total=%d", messages, checkpoints, len(events))
	}
	foreign := ListRunnerReplayInput{StreamUID: claim.StreamUID, OwnerID: "owner-b", MessageLimit: 10, CheckpointLimit: 10}
	if _, err := repo.ListRunnerReplay(context.Background(), foreign); err != ErrOwnerMismatch {
		t.Fatalf("foreign error=%v", err)
	}
}

func TestRunnerReplayRequiredCheckpointRetainsClosureAndFences(t *testing.T) {
	repo, db, stream, source, claim := newFrameBranchForkFixture(t)
	ctx := context.Background()
	installRunnerReplayToolBatchTables(t, db)
	root, terminals := seedRunnerReplayToolBatch(t, repo, db, claim, "required-batch", "settled", 1)
	appendRunnerReplayCheckpoint(t, repo, claim, "later-checkpoint", `{"status":"running"}`)
	input := ListRunnerReplayInput{StreamUID: claim.StreamUID, OwnerID: claim.OwnerID,
		MessageLimit: 1, CheckpointLimit: 1, RequiredCheckpointEventID: terminals[0].EventID}
	events, err := repo.ListRunnerReplay(ctx, input)
	if err != nil || !runnerReplayContainsEvent(events, root.EventID) || !runnerReplayContainsEvent(events, terminals[0].EventID) {
		t.Fatalf("required receipt did not retain protocol closure: events=%d error=%v", len(events), err)
	}
	for _, test := range []struct {
		name   string
		change func(*ListRunnerReplayInput)
		want   error
	}{
		{"foreign-owner", func(in *ListRunnerReplayInput) { in.OwnerID = "owner-b" }, ErrOwnerMismatch},
		{"missing-event", func(in *ListRunnerReplayInput) { in.RequiredCheckpointEventID = 1 << 60 }, ErrCheckpointUnavailable},
		{"closure-budget", func(in *ListRunnerReplayInput) { in.MaxExpandedEvents = 1 }, ErrProviderReplayWindowTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := input
			test.change(&changed)
			if _, err := repo.ListRunnerReplay(ctx, changed); !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
		})
	}
	negative := input
	negative.RequiredCheckpointEventID = -1
	if _, err := repo.ListRunnerReplay(ctx, negative); err == nil {
		t.Fatal("negative checkpoint accepted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := repo.ListRunnerReplay(canceled, input); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lookup returned %v", err)
	}
	// A checkpoint outside the active projection must not be pinned by ID.
	base, err := repo.GetBranchState(ctx, stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ForkFrameUserMessageBranch(ctx, ForkFrameUserMessageBranchInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		SourceBranchID: base.ActiveBranchID, ExpectedActiveBranchID: base.ActiveBranchID, ExpectedGeneration: base.Generation,
		ClientMutationID: "edit-required-receipt-task", SourceClientMessageID: source.ClientMessageID,
		SourceMessageIndex: 0, ReplacementText: "use the corrected task input", Destinations: []string{"ws"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ListRunnerReplay(ctx, input); !errors.Is(err, ErrCheckpointUnavailable) {
		t.Fatalf("inactive checkpoint returned %v", err)
	}
}

func TestRunnerReplayPreservesLoadedSkillBeyondCheckpointSeedWindow(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-replay-skill-continuity", "owner-a")
	if _, _, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, ClientMessageID: "task-intent",
		PayloadJSON: []byte(`{"role":"user","messageOrigin":"task_intent","text":"analyze a cohort"}`),
	}); err != nil {
		t.Fatal(err)
	}
	skill := appendRunnerReplayCheckpoint(t, repo, claim, "skill-completed",
		`{"status":"completed","toolPhase":"completed","toolName":"skill","toolCallId":"load-skill","toolInput":{"skill":"single-cell-rna-analysis"}}`)
	for index := 0; index < 140; index++ {
		appendRunnerReplayCheckpoint(t, repo, claim, fmt.Sprintf("later-%03d", index),
			fmt.Sprintf(`{"status":"running","index":%d}`, index))
	}
	events, err := repo.ListRunnerReplay(context.Background(), ListRunnerReplayInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, MessageLimit: 10, CheckpointLimit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !runnerReplayContainsEvent(events, skill.EventID) {
		t.Fatalf("loaded Skill checkpoint %d was lost beyond the replay seed window", skill.EventID)
	}
}

func TestRunnerReplayUsesLatestAutoCompactAsReplayBoundary(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-replay-after-compact", "owner-a")
	oldUser, _, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, ClientMessageID: "before-compact",
		PayloadJSON: []byte(`{"text":"old task context"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	oldCheckpoint := appendRunnerReplayCheckpoint(t, repo, claim, "before-compact-checkpoint", `{"status":"completed","index":"old"}`)
	resumeUser, _, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, ClientMessageID: "resume-before-compact",
		PayloadJSON: []byte(`{"text":"latest user input before compact"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	autoCompact := appendRunnerReplayCheckpoint(t, repo, claim, "compact-boundary",
		`{"status":"completed","toolPhase":"auto_compact","summary":"durable compact summary"}`)
	newUser, _, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, ClientMessageID: "after-compact",
		PayloadJSON: []byte(`{"text":"continue after compact"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	newCheckpoint := appendRunnerReplayCheckpoint(t, repo, claim, "after-compact-checkpoint", `{"status":"running","index":"new"}`)

	events, err := repo.ListRunnerReplay(context.Background(), ListRunnerReplayInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, MessageLimit: 10, CheckpointLimit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if runnerReplayContainsEvent(events, oldUser.EventID) || runnerReplayContainsEvent(events, oldCheckpoint.EventID) {
		t.Fatalf("replay leaked pre-compact events oldUser=%d oldCheckpoint=%d events=%#v", oldUser.EventID, oldCheckpoint.EventID, events)
	}
	for _, want := range []int64{resumeUser.EventID, autoCompact.EventID, newUser.EventID, newCheckpoint.EventID} {
		if !runnerReplayContainsEvent(events, want) {
			t.Fatalf("replay lost post-compact event=%d events=%#v", want, events)
		}
	}
}

func TestRunnerReplayValidatesProjectionLimitBoundary(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-replay-limits", "owner-a")

	tests := []struct {
		name    string
		limit   int
		wantErr bool
	}{
		{name: "zero", limit: 0, wantErr: true},
		{name: "maximum", limit: MaxRunnerReplayProjection},
		{name: "above maximum", limit: MaxRunnerReplayProjection + 1, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := repo.ListRunnerReplay(context.Background(), ListRunnerReplayInput{
				StreamUID: claim.StreamUID, OwnerID: claim.OwnerID,
				MessageLimit: test.limit, CheckpointLimit: 0,
			})
			if test.wantErr && err == nil {
				t.Fatalf("ListRunnerReplay accepted message limit %d", test.limit)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("ListRunnerReplay rejected message limit %d: %v", test.limit, err)
			}
		})
	}
}

func TestRunnerReplayClosesToolBatchAcrossSeedBoundary(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-replay-boundary", "owner-a")
	installRunnerReplayToolBatchTables(t, db)
	root, terminals := seedRunnerReplayToolBatch(t, repo, db, claim, "batch-boundary", "settled", 1)

	events, err := repo.ListRunnerReplay(context.Background(), ListRunnerReplayInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, MessageLimit: 10, CheckpointLimit: 1,
		MaxExpandedEvents: 16, MaxExpandedBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !runnerReplayContainsEvent(events, root.EventID) || !runnerReplayContainsEvent(events, terminals[0].EventID) {
		t.Fatalf("replay did not close batch root=%d terminal=%d events=%#v", root.EventID, terminals[0].EventID, events)
	}
}

func TestRunnerReplayClosureSurvivesOneThousandAndOneAuditCheckpoints(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-replay-1001-audit", "owner-a")
	installRunnerReplayToolBatchTables(t, db)
	root := appendRunnerReplayToolBatchRoot(t, repo, claim, "batch-1001", "settled", []runnerReplayTestCall{{
		ID: "call-0", Name: "read_file", Arguments: map[string]any{"path": "notes.txt"},
	}})
	for index := 0; index < 1001; index++ {
		appendRunnerReplayCheckpoint(t, repo, claim, fmt.Sprintf("batch-1001-audit-%04d", index), fmt.Sprintf(`{"status":"running","audit":%d}`, index))
	}
	terminal := appendRunnerReplayCheckpoint(t, repo, claim, "batch-1001-terminal", `{"status":"completed","toolCallId":"call-0","toolPhase":"completed","toolResult":{"ok":true}}`)
	setRunnerReplayToolBatchTerminals(t, db, "batch-1001", []Event{terminal})

	events, err := repo.ListRunnerReplay(context.Background(), ListRunnerReplayInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, MessageLimit: 10,
		CheckpointLimit: MaxRunnerReplayProjection, MaxExpandedEvents: 1200, MaxExpandedBytes: 8 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !runnerReplayContainsEvent(events, root.EventID) || !runnerReplayContainsEvent(events, terminal.EventID) {
		t.Fatalf("1001-audit closure lost root=%d terminal=%d eventCount=%d", root.EventID, terminal.EventID, len(events))
	}
}

func TestRunnerReplayClosureDoesNotPullAdjacentUnseededBatch(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-replay-adjacent", "owner-a")
	installRunnerReplayToolBatchTables(t, db)
	firstRoot, firstTerminal := seedRunnerReplayToolBatch(t, repo, db, claim, "batch-adjacent-a", "settled", 1)
	secondRoot, secondTerminal := seedRunnerReplayToolBatch(t, repo, db, claim, "batch-adjacent-b", "settled", 1)

	events, err := repo.ListRunnerReplay(context.Background(), ListRunnerReplayInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, MessageLimit: 10, CheckpointLimit: 1,
		MaxExpandedEvents: 16, MaxExpandedBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if runnerReplayContainsEvent(events, firstRoot.EventID) || runnerReplayContainsEvent(events, firstTerminal[0].EventID) ||
		!runnerReplayContainsEvent(events, secondRoot.EventID) || !runnerReplayContainsEvent(events, secondTerminal[0].EventID) {
		t.Fatalf("adjacent closure events=%#v", events)
	}
}

func TestRunnerReplayRejectsOpenToolBatchEvenOutsideSeed(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-replay-open", "owner-a")
	installRunnerReplayToolBatchTables(t, db)
	seedRunnerReplayToolBatch(t, repo, db, claim, "batch-open", "waiting", 2)
	for index := 0; index < 4; index++ {
		appendRunnerReplayCheckpoint(t, repo, claim, fmt.Sprintf("batch-open-audit-%d", index), fmt.Sprintf(`{"status":"running","audit":%d}`, index))
	}

	_, err := repo.ListRunnerReplay(context.Background(), ListRunnerReplayInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, MessageLimit: 10, CheckpointLimit: 1,
		MaxExpandedEvents: 16, MaxExpandedBytes: 1 << 20,
	})
	if !errors.Is(err, ErrOpenToolBatch) {
		t.Fatalf("open batch error=%v", err)
	}
}

func TestRunnerReplayAllowsParkedWaitingToolBatch(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-replay-parked", "owner-a")
	installRunnerReplayToolBatchTables(t, db)
	root := appendRunnerReplayToolBatchRoot(t, repo, claim, "batch-parked", "waiting", []runnerReplayTestCall{{
		ID: "call-parked", Name: "ask_user", Arguments: map[string]any{"questions": []any{"q1"}},
	}})
	started := appendRunnerReplayCheckpoint(t, repo, claim, "batch-parked-started",
		`{"status":"running","toolCallId":"call-parked","toolPhase":"start"}`)
	waiting := appendRunnerReplayCheckpoint(t, repo, claim, "batch-parked-waiting",
		`{"status":"waiting","toolCallId":"call-parked","toolPhase":"waiting"}`)
	if _, err := db.Exec(`UPDATE transcript_tool_call_batches SET next_ordinal=0,waiting_ordinal=0 WHERE batch_id=?`, "batch-parked"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE transcript_tool_call_items SET state='waiting',started_event_id=?,waiting_event_id=? WHERE batch_id=? AND ordinal=0`,
		started.EventID, waiting.EventID, "batch-parked"); err != nil {
		t.Fatal(err)
	}
	events, err := repo.ListRunnerReplay(context.Background(), ListRunnerReplayInput{
		StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, MessageLimit: 10, CheckpointLimit: 10,
		MaxExpandedEvents: 32, MaxExpandedBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !runnerReplayContainsEvent(events, root.EventID) || !runnerReplayContainsEvent(events, waiting.EventID) {
		t.Fatalf("parked replay lost root=%d waiting=%d events=%#v", root.EventID, waiting.EventID, events)
	}
}

func TestRunnerReplayRejectsClosureBeyondExplicitHardBudgets(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	claim := seedArtifactProjectionClaim(t, repo, db, "stream-replay-budget", "owner-a")
	installRunnerReplayToolBatchTables(t, db)
	seedRunnerReplayToolBatch(t, repo, db, claim, "batch-budget", "settled", 1)

	for name, input := range map[string]ListRunnerReplayInput{
		"events": {
			StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, MessageLimit: 10, CheckpointLimit: 1,
			MaxExpandedEvents: 2, MaxExpandedBytes: 1 << 20,
		},
		"bytes": {
			StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, MessageLimit: 10, CheckpointLimit: 1,
			MaxExpandedEvents: 16, MaxExpandedBytes: 1,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := repo.ListRunnerReplay(context.Background(), input); !errors.Is(err, ErrProviderReplayWindowTooLarge) {
				t.Fatalf("budget error=%v", err)
			}
		})
	}
}

func installRunnerReplayToolBatchTables(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`
		CREATE TABLE transcript_tool_call_batches(
			batch_id TEXT PRIMARY KEY,
			stream_uid TEXT NOT NULL,
			branch_id TEXT NOT NULL,
			branch_generation INTEGER NOT NULL,
			source_event_id INTEGER NOT NULL,
			source_runner_attempt INTEGER NOT NULL DEFAULT 1,
			source_client_message_id TEXT NOT NULL DEFAULT '',
			admitted_input_revision INTEGER NOT NULL DEFAULT 1,
			call_count INTEGER NOT NULL,
			next_ordinal INTEGER NOT NULL DEFAULT 0,
			state TEXT NOT NULL,
			waiting_ordinal INTEGER,
			runner_id TEXT,
			runner_attempt INTEGER,
			runner_claim_sha256 TEXT,
			reason_code TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE transcript_tool_call_items(
			batch_id TEXT NOT NULL,
			stream_uid TEXT NOT NULL,
			ordinal INTEGER NOT NULL,
			tool_call_id TEXT NOT NULL,
			tool_name TEXT NOT NULL,
			arguments_json TEXT NOT NULL,
			arguments_sha256 TEXT NOT NULL,
			state TEXT NOT NULL DEFAULT 'pending',
			started_event_id INTEGER,
			waiting_event_id INTEGER,
			terminal_event_id INTEGER,
			PRIMARY KEY(batch_id,ordinal)
		);
		CREATE TABLE kernel_local_operations(
			operation_id TEXT PRIMARY KEY,
			stream_uid TEXT NOT NULL,
			source_event_id INTEGER NOT NULL,
			tool_call_ordinal INTEGER NOT NULL,
			tool_call_id TEXT NOT NULL,
			tool TEXT NOT NULL,
			input_json TEXT NOT NULL,
			input_sha256 TEXT NOT NULL,
			approval_request_id TEXT NOT NULL,
			state_version INTEGER NOT NULL,
			frame_id TEXT NOT NULL
		);`)
	if err != nil {
		t.Fatal(err)
	}
}

func seedRunnerReplayToolBatch(
	t *testing.T,
	repo *Repository,
	db *sql.DB,
	claim RunnerClaim,
	batchID, state string,
	callCount int,
) (Event, []Event) {
	t.Helper()
	calls := make([]runnerReplayTestCall, 0, callCount)
	for ordinal := 0; ordinal < callCount; ordinal++ {
		calls = append(calls, runnerReplayTestCall{
			ID: fmt.Sprintf("call-%d", ordinal), Name: "read_file",
			Arguments: map[string]any{"path": fmt.Sprintf("notes-%d.txt", ordinal)},
		})
	}
	root := appendRunnerReplayToolBatchRoot(t, repo, claim, batchID, state, calls)
	terminals := make([]Event, 0, callCount)
	if state == "settled" {
		for ordinal := 0; ordinal < callCount; ordinal++ {
			terminals = append(terminals, appendRunnerReplayCheckpoint(t, repo, claim, fmt.Sprintf("%s-terminal-%d", batchID, ordinal),
				fmt.Sprintf(`{"status":"completed","toolCallId":"call-%d","toolPhase":"completed","toolResult":{"ok":true,"ordinal":%d}}`, ordinal, ordinal)))
		}
	}
	setRunnerReplayToolBatchTerminals(t, db, batchID, terminals)
	return root, terminals
}

type runnerReplayTestCall struct {
	ID        string
	Name      string
	Arguments map[string]any
}

func appendRunnerReplayToolBatchRoot(
	t *testing.T,
	repo *Repository,
	claim RunnerClaim,
	batchID, state string,
	calls []runnerReplayTestCall,
) Event {
	t.Helper()
	modelCalls := make([]map[string]any, 0, len(calls))
	for _, call := range calls {
		modelCalls = append(modelCalls, map[string]any{
			"id": call.ID, "type": "function", "name": call.Name, "arguments": call.Arguments,
		})
	}
	payload, err := json.Marshal(map[string]any{"status": "running", "modelToolCalls": modelCalls})
	if err != nil {
		t.Fatal(err)
	}
	_, event, _, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: batchID + "-root", Phase: RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: payload,
		CommitHook: func(ctx context.Context, tx *ImmediateTransaction, event Event, _ bool) (RunnerCheckpointCommitReceipt, error) {
			var branchID string
			var branchGeneration int64
			if err := tx.QueryRowContext(ctx, `SELECT active_branch_id,generation FROM transcript_branch_state WHERE stream_uid=?`, claim.StreamUID).
				Scan(&branchID, &branchGeneration); err != nil {
				return RunnerCheckpointCommitReceipt{}, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO transcript_tool_call_batches(
				batch_id,stream_uid,branch_id,branch_generation,source_event_id,call_count,state
			) VALUES(?,?,?,?,?,?,?)`, batchID, claim.StreamUID, branchID, branchGeneration, event.EventID, len(calls), state); err != nil {
				return RunnerCheckpointCommitReceipt{}, err
			}
			for ordinal, call := range calls {
				arguments, err := json.Marshal(call.Arguments)
				if err != nil {
					return RunnerCheckpointCommitReceipt{}, err
				}
				digest := sha256.Sum256(arguments)
				if _, err := tx.ExecContext(ctx, `INSERT INTO transcript_tool_call_items(
					batch_id,stream_uid,ordinal,tool_call_id,tool_name,arguments_json,arguments_sha256,
					started_event_id,waiting_event_id,terminal_event_id
				) VALUES(?,?,?,?,?,?,?,NULL,NULL,NULL)`, batchID, claim.StreamUID, ordinal, call.ID, call.Name,
					string(arguments), hex.EncodeToString(digest[:])); err != nil {
					return RunnerCheckpointCommitReceipt{}, err
				}
			}
			return RunnerCheckpointCommitReceipt{ToolBatch: &RunnerCheckpointToolBatchReceipt{
				BatchID: batchID, CallCount: int64(len(calls)),
			}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func setRunnerReplayToolBatchTerminals(t *testing.T, db *sql.DB, batchID string, terminals []Event) {
	t.Helper()
	for ordinal, terminal := range terminals {
		if _, err := db.Exec(`UPDATE transcript_tool_call_items SET terminal_event_id=? WHERE batch_id=? AND ordinal=?`,
			terminal.EventID, batchID, ordinal); err != nil {
			t.Fatal(err)
		}
	}
}

func appendRunnerReplayCheckpoint(
	t *testing.T,
	repo *Repository,
	claim RunnerClaim,
	clientMessageID, payload string,
) Event {
	t.Helper()
	_, event, _, err := repo.AppendRunnerCheckpoint(context.Background(), AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: clientMessageID, Phase: RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: []byte(payload),
	})
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func runnerReplayContainsEvent(events []RunnerReplayEvent, eventID int64) bool {
	for _, event := range events {
		if event.Event.EventID == eventID {
			return true
		}
	}
	return false
}

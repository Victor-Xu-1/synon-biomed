package transcript

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAskUserAnswerBranchPreservesSelectedImplementation(t *testing.T) {
	questions, implementations, err := branchAskUserQuestions(map[string]any{
		"question": "Which generator?",
		"options": []any{map[string]any{
			"label": "Pocket-native generation", "implementation": "DiffSBDD",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, continuation, err := buildAskUserBranchResult(AskUserBranchResponse{
		Action: "answer", Answers: map[string]string{
			"Which generator?": "Pocket-native generation · DiffSBDD",
		},
	}, questions, implementations)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(continuation, `"implementations":{"Which generator?":"DiffSBDD"}`) ||
		!strings.Contains(continuation, AskUserSelectedRouteInstruction) {
		t.Fatalf("continuation=%q", continuation)
	}
}

func TestForkFrameAskUserAnswerBranchPersistsStructuredActions(t *testing.T) {
	tests := []struct {
		name       string
		response   AskUserBranchResponse
		wantStatus string
	}{
		{name: "answer", response: AskUserBranchResponse{Action: "answer", Answers: map[string]string{
			"Which structure?": "5FQD", "Run docking?": "yes",
		}}, wantStatus: "answered"},
		{name: "decide", response: AskUserBranchResponse{Action: "decide_for_me"}, wantStatus: "deferred"},
		{name: "discuss", response: AskUserBranchResponse{Action: "discuss", Message: "Compare the assay risks."}, wantStatus: "discussed"},
		{name: "cancel", response: AskUserBranchResponse{Action: "cancel"}, wantStatus: "cancelled"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo, db, stream, _, claim := newFrameBranchForkFixture(t)
			seedAskUserBranchFacts(t, repo, db, stream, "ask_user")
			base, err := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
			if err != nil {
				t.Fatal(err)
			}
			input := ForkFrameAskUserAnswerBranchInput{
				StreamUID: stream.UID, OwnerID: stream.OwnerID,
				SourceBranchID: base.ActiveBranchID, ExpectedActiveBranchID: base.ActiveBranchID, ExpectedGeneration: 1,
				ClientMutationID: "answer-" + test.name, ToolUseID: "ask-1", SourceMessageIndex: 2,
				Response: test.response, Destinations: []string{"ws"},
			}
			result, err := repo.ForkFrameAskUserAnswerBranch(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Created || result.ParentBranchID != base.ActiveBranchID || result.Generation != 2 ||
				result.ForkPoint != 2 || result.ReplacementEvent.Type != "user_input_response" ||
				result.ReplacementFrameEvent.Type != "user_input_response" || !result.RunnerCancellation.Applied {
				t.Fatalf("result=%#v", result)
			}
			var payload map[string]any
			if err := json.Unmarshal(result.ReplacementFrameEvent.PayloadJSON, &payload); err != nil {
				t.Fatal(err)
			}
			blocks, _ := payload["content"].([]any)
			if len(blocks) != 1 {
				t.Fatalf("payload=%#v", payload)
			}
			block, _ := blocks[0].(map[string]any)
			if block["type"] != "tool_result" || block["tool_use_id"] != "ask-1" {
				t.Fatalf("block=%#v", block)
			}
			if _, found := block["is_error"]; found {
				t.Fatalf("result retained is_error: %#v", block)
			}
			var structured map[string]any
			if err := json.Unmarshal([]byte(block["content"].(string)), &structured); err != nil {
				t.Fatal(err)
			}
			if structured["version"] != float64(1) || structured["status"] != test.wantStatus || structured["action"] != test.response.Action {
				t.Fatalf("structured=%#v", structured)
			}
			if text, _ := payload["text"].(string); text == "" || text == block["content"] {
				t.Fatalf("model continuation was not separated: %#v", payload)
			}
			if test.response.Action == "answer" {
				text, _ := payload["text"].(string)
				if !strings.Contains(text, AskUserSelectedRouteInstruction) {
					t.Fatalf("answered continuation lost route authority: %q", text)
				}
			}
			var kind, sourceMessageID string
			var childEvents, oldResultInChild int
			if err := db.QueryRow(`SELECT kind,source_message_id FROM transcript_branches WHERE stream_uid=? AND branch_id=?`,
				stream.UID, result.BranchID).Scan(&kind, &sourceMessageID); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_branch_events WHERE stream_uid=? AND branch_id=?`,
				stream.UID, result.BranchID).Scan(&childEvents); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`
				SELECT COUNT(*) FROM transcript_branch_events membership
				JOIN transcript_events event ON event.stream_uid=membership.stream_uid AND event.event_id=membership.event_id
				WHERE membership.stream_uid=? AND membership.branch_id=? AND event.frame_event_id='ask-result-event'`,
				stream.UID, result.BranchID).Scan(&oldResultInChild); err != nil {
				t.Fatal(err)
			}
			if kind != "answer" || sourceMessageID != "ask-1" || childEvents != 4 || oldResultInChild != 0 {
				t.Fatalf("branch kind=%q source=%q events=%d oldResult=%d", kind, sourceMessageID, childEvents, oldResultInChild)
			}
			projected, err := repo.ListProjectedEvents(context.Background(), ListProjectedEventsInput{
				StreamUID: stream.UID, OwnerID: stream.OwnerID, Limit: 20,
			})
			if err != nil || len(projected) != 4 {
				t.Fatalf("active projection=%#v err=%v", projected, err)
			}
			for _, event := range projected {
				if event.Event.FrameEventID != nil && *event.Event.FrameEventID == "ask-result-event" {
					t.Fatalf("superseded AskUser result remained in projection: %#v", projected)
				}
			}
			replay, err := repo.ListRunnerReplay(context.Background(), ListRunnerReplayInput{
				StreamUID: stream.UID, OwnerID: stream.OwnerID, MessageLimit: 20, CheckpointLimit: 20,
			})
			if err != nil || len(replay) != 4 || replay[len(replay)-1].Event.EventID != result.ReplacementEvent.EventID {
				t.Fatalf("active replay=%#v err=%v", replay, err)
			}
			intent, found, err := repo.GetActiveFrameTaskIntent(context.Background(), stream.UID, stream.OwnerID)
			if err != nil || !found || intent.SourceMessageID != "source-message" {
				t.Fatalf("intent=%#v found=%t err=%v", intent, found, err)
			}
			var rawContext string
			if err := db.QueryRow(`SELECT context_data FROM frame_runtime_metadata WHERE frame_id=?`, stream.FrameID).Scan(&rawContext); err != nil {
				t.Fatal(err)
			}
			var contextData map[string]any
			if json.Unmarshal([]byte(rawContext), &contextData) != nil || contextData["preserved"] != "yes" ||
				contextData["_pending_input_requests"] != nil || contextData["_ask_user_payload"] != nil {
				t.Fatalf("context=%#v", contextData)
			}
			if _, _, _, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
				Claim: claim, ClientMessageID: "late-answer-finish", Status: "completed",
				PayloadJSON: []byte(`{"status":"completed"}`), Destinations: []string{"ws"},
			}); !errors.Is(err, ErrClaimStale) {
				t.Fatalf("late finish error=%v", err)
			}
			retry, err := NewRepository(db).ForkFrameAskUserAnswerBranch(context.Background(), input)
			if err != nil || retry.Created || retry.BranchID != result.BranchID || retry.ReplacementEvent.EventID != result.ReplacementEvent.EventID {
				t.Fatalf("retry=%#v err=%v", retry, err)
			}
			conflict := input
			conflict.SourceMessageIndex++
			if _, err := repo.ForkFrameAskUserAnswerBranch(context.Background(), conflict); !errors.Is(err, ErrEventConflict) {
				t.Fatalf("conflicting retry error=%v", err)
			}
		})
	}
}

func TestAskUserToolAndResultShareOneVisibleBranchIndex(t *testing.T) {
	repo, db, stream, _, _ := newFrameBranchForkFixture(t)
	seedAskUserBranchFacts(t, repo, db, stream, "ask_user")
	state, err := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	var toolIndex, resultIndex int64
	err = repo.withImmediate(context.Background(), func(conn *sql.Conn) error {
		target, err := resolveAskUserBranchTargetConn(
			context.Background(), conn, stream, state.ActiveBranchID, "ask-1",
		)
		if err != nil {
			return err
		}
		toolIndex, err = branchWebMessageIndexConn(
			context.Background(), conn, stream.UID, state.ActiveBranchID, target.ToolEvent.EventID,
		)
		if err != nil {
			return err
		}
		resultIndex, err = branchWebMessageIndexConn(
			context.Background(), conn, stream.UID, state.ActiveBranchID, target.ResultEvent.EventID,
		)
		return err
	})
	if err != nil || toolIndex != 2 || resultIndex != toolIndex {
		t.Fatalf("tool index=%d result index=%d err=%v", toolIndex, resultIndex, err)
	}
}

func TestForkFrameAskUserAnswerAtToolAcceptsTerminalCompatibilityProjection(t *testing.T) {
	repo, db, stream, _, _ := newFrameBranchForkFixture(t)
	seedAskUserBranchFacts(t, repo, db, stream, "ask_user")
	terminalPayload, err := json.Marshal(map[string]any{
		"role": "user", "content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": "ask-1",
			"content": `{"version":1,"status":"answered","action":"answer","answers":{"Which structure?":"5FQD","Run docking?":"yes"}}`,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE frame_events SET payload=? WHERE id='ask-result-event'`, string(terminalPayload)); err != nil {
		t.Fatal(err)
	}
	state, err := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := repo.ForkFrameAskUserAnswerAtTool(context.Background(), ForkFrameAskUserAnswerAtToolInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		SourceBranchID: state.ActiveBranchID, ExpectedActiveBranchID: state.ActiveBranchID,
		ExpectedGeneration: state.Generation, ClientMutationID: "replace-terminal-answer",
		ToolUseID: "ask-1", Response: AskUserBranchResponse{Action: "answer", Answers: map[string]string{
			"Which structure?": "all structures", "Run docking?": "yes",
		}}, Destinations: []string{"ws"},
	})
	if err != nil || !result.Created || result.ParentBranchID != state.ActiveBranchID || result.Generation != 2 {
		t.Fatalf("terminal compatibility answer fork=%#v err=%v", result, err)
	}
}

func TestAppendClaimedFrameAskUserReferencesFencesRunnerAttempt(t *testing.T) {
	t.Run("bind and retry", func(t *testing.T) {
		repo, db, stream, _, claim := newFrameBranchForkFixture(t)
		seedAskUserBranchFacts(t, repo, db, stream, "ask_user")
		input := AppendClaimedFrameAskUserReferencesInput{
			Claim: claim, FrameID: stream.FrameID, ToolUseID: "ask-1",
			ToolUseFrameEventID: "ask-tool-event", ToolResultFrameEventID: "ask-result-event",
		}
		appendReferences := func() []Event {
			t.Helper()
			var events []Event
			err := repo.RunImmediate(context.Background(), func(tx *ImmediateTransaction) error {
				var appendErr error
				events, appendErr = tx.AppendClaimedFrameAskUserReferences(context.Background(), input)
				return appendErr
			})
			if err != nil || len(events) != 2 {
				t.Fatalf("events=%#v err=%v", events, err)
			}
			for _, event := range events {
				if event.RunnerAttempt == nil || *event.RunnerAttempt != claim.Attempt {
					t.Fatalf("event attempt=%#v claim=%#v", event, claim)
				}
			}
			return events
		}
		first := appendReferences()
		retry := appendReferences()
		if retry[0].EventID != first[0].EventID || retry[1].EventID != first[1].EventID {
			t.Fatalf("retry changed events first=%#v retry=%#v", first, retry)
		}
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND frame_event_id IN ('ask-tool-event','ask-result-event')`,
			stream.UID).Scan(&count); err != nil || count != 2 {
			t.Fatalf("event count=%d err=%v", count, err)
		}

		if _, _, _, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
			Claim: claim, ClientMessageID: "finish-claim", Status: "completed",
			PayloadJSON: []byte(`{"status":"completed"}`),
		}); err != nil {
			t.Fatal(err)
		}
		if err := repo.RunImmediate(context.Background(), func(tx *ImmediateTransaction) error {
			_, appendErr := tx.AppendClaimedFrameAskUserReferences(context.Background(), input)
			return appendErr
		}); !errors.Is(err, ErrClaimStale) {
			t.Fatalf("stale claim error=%v", err)
		}
	})

	t.Run("conflict rolls back both bindings", func(t *testing.T) {
		repo, db, stream, _, claim := newFrameBranchForkFixture(t)
		seedAskUserBranchFacts(t, repo, db, stream, "ask_user")
		input := AppendClaimedFrameAskUserReferencesInput{
			Claim: claim, FrameID: stream.FrameID, ToolUseID: "ask-1",
			ToolUseFrameEventID: "ask-tool-event", ToolResultFrameEventID: "ask-result-event",
		}
		if err := repo.RunImmediate(context.Background(), func(tx *ImmediateTransaction) error {
			_, appendErr := tx.AppendClaimedFrameAskUserReferences(context.Background(), input)
			return appendErr
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE transcript_events SET runner_attempt=NULL WHERE stream_uid=? AND frame_event_id='ask-tool-event'`,
			stream.UID); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := repo.FinishRunner(context.Background(), FinishRunnerInput{
			Claim: claim, ClientMessageID: "finish-conflict-attempt", Status: "completed",
			PayloadJSON: []byte(`{"status":"completed"}`),
		}); err != nil {
			t.Fatal(err)
		}
		if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), AppendFrameUserEventInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "next-input",
			FrameEventID: "next-input-frame-event", MessageUUID: "next-input-message", Text: "continue",
		}); err != nil || !created {
			t.Fatalf("next input created=%t err=%v", created, err)
		}
		next, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-next", TTL: time.Minute,
			ResumeSource: ResumeSourceFresh,
		})
		if err != nil || !next.Claimed || next.Claim.Attempt == claim.Attempt {
			t.Fatalf("next claim=%#v err=%v", next, err)
		}
		err = repo.RunImmediate(context.Background(), func(tx *ImmediateTransaction) error {
			conflict := input
			conflict.Claim = next.Claim
			_, appendErr := tx.AppendClaimedFrameAskUserReferences(context.Background(), conflict)
			return appendErr
		})
		if !errors.Is(err, ErrEventConflict) {
			t.Fatalf("conflict error=%v", err)
		}
		var toolAttempt sql.NullInt64
		if err := db.QueryRow(`SELECT runner_attempt FROM transcript_events WHERE stream_uid=? AND frame_event_id='ask-tool-event'`,
			stream.UID).Scan(&toolAttempt); err != nil || toolAttempt.Valid {
			t.Fatalf("partial binding attempt=%#v err=%v", toolAttempt, err)
		}
	})
}

func TestForkFrameAskUserAnswerAtToolResolvesVisibleIndexAndRetriesAfterBranchSwitch(t *testing.T) {
	repo, db, stream, _, _ := newFrameBranchForkFixture(t)
	seedAskUserBranchFacts(t, repo, db, stream, "ask_user")
	base, err := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	input := ForkFrameAskUserAnswerAtToolInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		SourceBranchID: base.ActiveBranchID, ExpectedActiveBranchID: base.ActiveBranchID,
		ExpectedGeneration: base.Generation, ClientMutationID: "public-answer-retry", ToolUseID: "ask-1",
		Response: AskUserBranchResponse{Action: "cancel"}, Destinations: []string{"ws"},
	}
	result, err := repo.ForkFrameAskUserAnswerAtTool(context.Background(), input)
	if err != nil || !result.Created || result.ForkPoint != 2 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	retry, err := repo.ForkFrameAskUserAnswerAtTool(context.Background(), input)
	if err != nil || retry.Created || retry.BranchID != result.BranchID || retry.Generation != result.Generation ||
		retry.ReplacementEvent.EventID != result.ReplacementEvent.EventID {
		t.Fatalf("retry=%#v err=%v", retry, err)
	}
	input.ToolUseID = "missing"
	if _, err := repo.ForkFrameAskUserAnswerAtTool(context.Background(), input); !errors.Is(err, ErrBranchTargetNotFound) {
		t.Fatalf("missing AskUser target error=%v", err)
	}
}

func TestForkFrameAskUserAnswerBranchRejectsInvalidTargetsAndResponsesWithoutMutation(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		toolID   string
		response AskUserBranchResponse
	}{
		{name: "missing tool", toolName: "ask_user", toolID: "missing", response: AskUserBranchResponse{Action: "cancel"}},
		{name: "wrong tool", toolName: "read_file", toolID: "ask-1", response: AskUserBranchResponse{Action: "cancel"}},
		{name: "missing answer", toolName: "ask_user", toolID: "ask-1", response: AskUserBranchResponse{Action: "answer", Answers: map[string]string{"Other": "value"}}},
		{name: "blank discuss", toolName: "ask_user", toolID: "ask-1", response: AskUserBranchResponse{Action: "discuss", Message: "  "}},
		{name: "invalid action", toolName: "ask_user", toolID: "ask-1", response: AskUserBranchResponse{Action: "later"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo, db, stream, _, claim := newFrameBranchForkFixture(t)
			seedAskUserBranchFacts(t, repo, db, stream, test.toolName)
			base, err := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = repo.ForkFrameAskUserAnswerBranch(context.Background(), ForkFrameAskUserAnswerBranchInput{
				StreamUID: stream.UID, OwnerID: stream.OwnerID,
				SourceBranchID: base.ActiveBranchID, ExpectedActiveBranchID: base.ActiveBranchID, ExpectedGeneration: 1,
				ClientMutationID: "invalid-answer", ToolUseID: test.toolID, SourceMessageIndex: 2,
				Response: test.response, Destinations: []string{"ws"},
			})
			if err == nil {
				t.Fatal("invalid AskUser branch was accepted")
			}
			state, stateErr := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
			if stateErr != nil || state != base {
				t.Fatalf("state=%#v base=%#v err=%v", state, base, stateErr)
			}
			runtime, runtimeErr := repo.GetRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID, claim.Attempt)
			if runtimeErr != nil || runtime.Status != "running" {
				t.Fatalf("runner=%#v err=%v", runtime, runtimeErr)
			}
			var branchCount, eventCount int
			if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_branches WHERE stream_uid=?`, stream.UID).Scan(&branchCount); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=?`, stream.UID).Scan(&eventCount); err != nil {
				t.Fatal(err)
			}
			if branchCount != 1 || eventCount != 4 {
				t.Fatalf("branch count=%d event count=%d", branchCount, eventCount)
			}
		})
	}
}

func TestForkFrameAskUserAnswerBranchRollsBackOnInactiveDeliveryRoute(t *testing.T) {
	repo, db, stream, _, claim := newFrameBranchForkFixture(t)
	seedAskUserBranchFacts(t, repo, db, stream, "ask_user")
	base, err := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.ForkFrameAskUserAnswerBranch(context.Background(), ForkFrameAskUserAnswerBranchInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		SourceBranchID: base.ActiveBranchID, ExpectedActiveBranchID: base.ActiveBranchID, ExpectedGeneration: 1,
		ClientMutationID: "answer-no-route", ToolUseID: "ask-1", SourceMessageIndex: 2,
		Response: AskUserBranchResponse{Action: "cancel"}, Destinations: []string{"im:missing"},
	})
	if !errors.Is(err, ErrDeliveryRouteInactive) {
		t.Fatalf("fork error=%v", err)
	}
	state, stateErr := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if stateErr != nil || state != base {
		t.Fatalf("state=%#v base=%#v err=%v", state, base, stateErr)
	}
	runtime, runtimeErr := repo.GetRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID, claim.Attempt)
	if runtimeErr != nil || runtime.Status != "running" {
		t.Fatalf("runner=%#v err=%v", runtime, runtimeErr)
	}
	var branches, events, frameEvents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_branches WHERE stream_uid=?`, stream.UID).Scan(&branches); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=?`, stream.UID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM frame_events WHERE frame_id=?`, stream.FrameID).Scan(&frameEvents); err != nil {
		t.Fatal(err)
	}
	if branches != 1 || events != 4 || frameEvents != 2 {
		t.Fatalf("branches=%d events=%d frameEvents=%d", branches, events, frameEvents)
	}
}

func seedAskUserBranchFacts(t *testing.T, repo *Repository, db *sql.DB, stream Stream, toolName string) {
	t.Helper()
	questions := []any{
		map[string]any{"question": "Which structure?", "header": "Structure"},
		map[string]any{"question": "Run docking?", "header": "Docking"},
	}
	toolPayload, err := json.Marshal(map[string]any{
		"role": "assistant", "content": []any{map[string]any{
			"type": "tool_use", "id": "ask-1", "name": toolName, "input": map[string]any{"questions": questions},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	resultPayload, err := json.Marshal(map[string]any{
		"role": "user", "content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": "ask-1", "content": `{"status":"awaiting_user_response"}`, "is_error": true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := repo.now().UTC()
	pendingContext, err := json.Marshal(map[string]any{
		"preserved": "yes",
		"_pending_input_requests": []any{map[string]any{
			"tool_id": "ask-1", "kind": "ask", "questions": questions,
		}},
		"_ask_user_payload": map[string]any{"tool_id": "ask-1", "questions": questions},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO frame_runtime_metadata(frame_id,context_data) VALUES(?,?)
		ON CONFLICT(frame_id) DO UPDATE SET context_data=excluded.context_data`, stream.FrameID, string(pendingContext)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE frames SET status='awaiting_user_response' WHERE id=?`, stream.FrameID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at) VALUES
		('ask-tool-event',?,2,'assistant_message',?,?),
		('ask-result-event',?,3,'user_message',?,?)`,
		stream.FrameID, string(toolPayload), now, stream.FrameID, string(resultPayload), now,
	); err != nil {
		t.Fatal(err)
	}
	if err := repo.RunImmediate(context.Background(), func(tx *ImmediateTransaction) error {
		var err error
		if toolName == "ask_user" {
			_, err = tx.AppendFrameAskUserReferences(context.Background(), AppendFrameAskUserReferencesInput{
				StreamUID: stream.UID, OwnerID: stream.OwnerID, FrameID: stream.FrameID, ToolUseID: "ask-1",
				ToolUseFrameEventID: "ask-tool-event", ToolResultFrameEventID: "ask-result-event",
			})
		} else {
			_, err = tx.AppendHistoricalFrameReferences(context.Background(), stream.UID, stream.OwnerID,
				[]string{"ask-tool-event", "ask-result-event"})
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

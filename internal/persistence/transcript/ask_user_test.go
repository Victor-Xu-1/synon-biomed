package transcript

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCanonicalAskUserToolNameV1AcceptsOnlyExactKnownAliases(t *testing.T) {
	for _, name := range []string{"AskUserQuestion", "ask_user_question", "ask_user"} {
		if canonical, ok := CanonicalAskUserToolNameV1(name); !ok || canonical != "ask_user" {
			t.Fatalf("name=%q canonical=%q ok=%t", name, canonical, ok)
		}
	}
	for _, name := range []string{"", " AskUserQuestion", "ask_user ", "ask-user", "ASK_USER"} {
		if canonical, ok := CanonicalAskUserToolNameV1(name); ok || canonical != "" {
			t.Fatalf("invalid name=%q canonical=%q ok=%t", name, canonical, ok)
		}
	}
}

func TestAnsweredAskUserContinuationPreservesSelectedRoute(t *testing.T) {
	continuation, err := EncodeAnsweredAskUserModelContinuation(map[string]string{
		"Which route?": "Pocket-native generation",
	}, map[string]string{"Which route?": "Pocket2Mol"})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(continuation), &payload); err != nil {
		t.Fatal(err)
	}
	answers, _ := payload["answers"].(map[string]any)
	implementations, _ := payload["implementations"].(map[string]any)
	if payload["status"] != string(AskUserStatusAnswered) ||
		answers["Which route?"] != "Pocket-native generation" ||
		implementations["Which route?"] != "Pocket2Mol" ||
		payload["instruction"] != AskUserSelectedRouteInstruction {
		t.Fatalf("continuation=%#v", payload)
	}
	if _, err := EncodeAnsweredAskUserModelContinuation(nil, nil); err == nil {
		t.Fatal("empty answered continuation was accepted")
	}
	if _, err := EncodeAnsweredAskUserModelContinuation(
		map[string]string{"Which route?": "Pocket-native generation"},
		map[string]string{"Different question": "Pocket2Mol"},
	); err == nil {
		t.Fatal("orphan selected implementation was accepted")
	}
}

func TestAnsweredAskUserContinuationPreservesAuxiliaryEvidenceResolver(t *testing.T) {
	question := "How should the binding pocket be resolved?"
	continuation, err := EncodeAnsweredAskUserModelContinuationWithEvidenceResolvers(
		map[string]string{question: "Use P2Rank detection"}, nil,
		map[string]AskUserEvidenceResolverSelection{question: {
			EvidenceGroup: "binding-site-center", Skill: "p2rank-pocket-detection", Implementation: "P2Rank",
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(continuation), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["implementations"] != nil || !strings.Contains(continuation, `"evidence_resolvers"`) ||
		!strings.Contains(continuation, `"p2rank-pocket-detection"`) {
		t.Fatalf("continuation=%s", continuation)
	}
}

func TestDelegatedAskUserContinuationHasDistinctProvenance(t *testing.T) {
	continuation, err := EncodeDelegatedAskUserModelContinuation(
		map[string]string{"Which engine?": "AutoDock Vina"},
	)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(continuation), &payload); err != nil {
		t.Fatal(err)
	}
	implementations, _ := payload["implementations"].(map[string]any)
	if payload["status"] != "delegated" || payload["provenance"] != "model-delegated-choice" ||
		payload["answers"] != nil || payload["evidence"] != nil ||
		implementations["Which engine?"] != "AutoDock Vina" {
		t.Fatalf("delegated continuation=%#v", payload)
	}
	if _, err := EncodeDelegatedAskUserModelContinuation(nil); err == nil {
		t.Fatal("empty delegated continuation was accepted")
	}
}

func TestAnsweredAskUserContinuationResolverScope(t *testing.T) {
	first := AskUserEvidenceResolverSelection{EvidenceGroup: "point", Skill: "resolver-a", Implementation: "Resolver A"}
	for _, tc := range []struct {
		name   string
		second AskUserEvidenceResolverSelection
		valid  bool
	}{
		{"duplicate route", first, true},
		{"same route normalized", AskUserEvidenceResolverSelection{EvidenceGroup: " POINT ", Skill: " Resolver-A ", Implementation: " RESOLVER A "}, true},
		{"different group", AskUserEvidenceResolverSelection{EvidenceGroup: "other", Skill: "resolver-b", Implementation: "Resolver B"}, true},
		{"different engine", AskUserEvidenceResolverSelection{EvidenceGroup: "point", Skill: "resolver-a", Implementation: "Resolver B"}, false},
		{"different skill", AskUserEvidenceResolverSelection{EvidenceGroup: "point", Skill: "resolver-b", Implementation: "Resolver A"}, false},
		{"normalized group conflict", AskUserEvidenceResolverSelection{EvidenceGroup: " POINT ", Skill: "resolver-b", Implementation: "Resolver B"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := EncodeAnsweredAskUserModelContinuationWithEvidenceResolvers(
				map[string]string{"first": "first route", "second": "second route"}, nil,
				map[string]AskUserEvidenceResolverSelection{"first": first, "second": tc.second},
			)
			if (err == nil) != tc.valid || (!tc.valid && result != "") {
				t.Fatalf("scope valid=%t got result=%q err=%v", tc.valid, result, err)
			}
		})
	}
}

func TestAskUserResultV1UsesClosedStructuredStates(t *testing.T) {
	tests := []struct {
		name    string
		action  AskUserAction
		answers map[string]string
		message string
		status  AskUserStatus
	}{
		{name: "answer", action: AskUserActionAnswer, answers: map[string]string{"Which structure?": "5FQD"}, status: AskUserStatusAnswered},
		{name: "decide", action: AskUserActionDecideForMe, status: AskUserStatusDeferred},
		{name: "discuss", action: AskUserActionDiscuss, message: "Compare the alternatives first.", status: AskUserStatusDiscussed},
		{name: "cancel", action: AskUserActionCancel, status: AskUserStatusCancelled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := NewAskUserResultV1(test.action, test.answers, test.message)
			if err != nil {
				t.Fatal(err)
			}
			if result.Version != AskUserPayloadVersion || result.Action != test.action || result.Status != test.status {
				t.Fatalf("result=%#v", result)
			}
			encoded, err := EncodeAskUserResultV1(result)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeAskUserResultV1(encoded)
			if err != nil || decoded.Status != test.status || decoded.Action != test.action {
				t.Fatalf("decoded=%#v err=%v", decoded, err)
			}
		})
	}

	pending := NewAskUserPendingResultV1()
	if pending.Version != AskUserPayloadVersion || pending.Status != AskUserStatusAwaitingResponse || pending.Action != "" {
		t.Fatalf("pending=%#v", pending)
	}
	for name, value := range map[string][]byte{
		"unknown version": []byte(`{"version":2,"status":"answered","action":"answer","answers":{"q":"a"}}`),
		"unknown status":  []byte(`{"version":1,"status":"complete","action":"answer","answers":{"q":"a"}}`),
		"missing answers": []byte(`{"version":1,"status":"answered","action":"answer"}`),
		"extra pending":   []byte(`{"version":1,"status":"awaiting_user_response","message":"guess"}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeAskUserResultV1(value); err == nil {
				t.Fatal("invalid AskUser result was accepted")
			}
		})
	}
	validQuestions := []any{map[string]any{
		"question": "Which structure?", "header": "Structure", "multiSelect": false,
		"options": []any{
			map[string]any{
				"label": "5FQD", "description": "Use the experimental structure.",
				"pros": "Experimental evidence", "cons": "May be an older ligand series",
				"metadata": map[string]any{"smiles": "O=C1CCC(N2C(=O)CCC2=O)N1"},
			},
			map[string]any{"label": "Predicted", "cons": "No experimental ligand pose"},
		},
	}}
	if normalized, err := normalizeAskUserQuestions(validQuestions); err != nil || len(normalized) != 1 ||
		normalized[0].Question != "Which structure?" || normalized[0].Options[0].Pros != "Experimental evidence" ||
		normalized[0].Options[0].Cons != "May be an older ligand series" ||
		normalized[0].Options[0].Metadata["smiles"] != "O=C1CCC(N2C(=O)CCC2=O)N1" ||
		normalized[0].Options[1].Description != "" || normalized[0].Options[1].Cons != "No experimental ligand pose" {
		t.Fatalf("normalized questions=%#v err=%v", normalized, err)
	}
	for name, questions := range map[string][]any{
		"unknown field": {map[string]any{
			"question": "Which structure?", "header": "Structure", "unknown": true,
			"options": validQuestions[0].(map[string]any)["options"],
		}},
		"one option": {map[string]any{
			"question": "Which structure?", "header": "Structure",
			"options": []any{map[string]any{"label": "5FQD", "description": "Use it."}},
		}},
		"duplicate option": {map[string]any{
			"question": "Which structure?", "header": "Structure",
			"options": []any{
				map[string]any{"label": "5FQD", "description": "Use it."},
				map[string]any{"label": "5FQD", "description": "Use it again."},
			},
		}},
	} {
		t.Run("questions "+name, func(t *testing.T) {
			if _, err := normalizeAskUserQuestions(questions); err == nil {
				t.Fatal("invalid typed questions were accepted")
			}
		})
	}
}

func TestImmediateTransactionPersistsTypedAskUserFactsOnExactStream(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	ctx := context.Background()
	if _, err := db.Exec(`
		INSERT INTO projects(id,user_id) VALUES('project','owner');
		INSERT INTO frames(id,project_id,root_frame_id,status,updated_at)
		VALUES('frame','project','frame','processing',CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	stream, err := repo.CreateStream(ctx, CreateStreamInput{
		UID: "frame:frame", OwnerID: "owner", ExternalID: "frame", SessionID: "frame",
		Kind: StreamKindFrameRef, ProjectID: "project", RootFrameID: "frame", FrameID: "frame", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(ctx, AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "task", FrameEventID: "frame-task",
		MessageUUID: "task-message", Text: "Analyze CRBN.", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append task created=%t err=%v", created, err)
	}
	claimResult, err := repo.ClaimRunner(ctx, ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner", TTL: time.Minute, ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !claimResult.Claimed {
		t.Fatalf("claim=%#v err=%v", claimResult, err)
	}
	for _, eventType := range []string{AskUserPromptEventType, AskUserResultEventType} {
		if _, _, err := repo.AppendRunnerEvent(ctx, AppendEventInput{
			Claim: claimResult.Claim, ClientMessageID: "forged-" + eventType, Type: eventType,
			Source: EventSourcePayload, PayloadJSON: []byte(`{"version":1}`),
		}); !errors.Is(err, ErrReservedEventType) {
			t.Fatalf("generic append accepted reserved %s: %v", eventType, err)
		}
	}
	now := time.Now().UTC()
	if _, err := db.Exec(`
		INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at) VALUES
		('ask-use','frame',2,'assistant_message','{"role":"assistant","content":[{"type":"tool_use","id":"ask-1","name":"ask_user","input":{"questions":[{"question":"Which structure?","header":"Structure","options":[{"label":"5FQD","description":"Use the experimental structure."},{"label":"Predicted","description":"Use the predicted structure."}],"multiSelect":false}]}}]}',?),
		('ask-pending','frame',3,'user_message','{"role":"user","content":[{"type":"tool_result","tool_use_id":"ask-1","content":"{\"status\":\"awaiting_user_response\"}","is_error":true}]}',?)`, now, now); err != nil {
		t.Fatal(err)
	}
	err = repo.RunImmediate(ctx, func(tx *ImmediateTransaction) error {
		_, appendErr := tx.AppendFrameAskUserPending(ctx, AppendFrameAskUserPendingInput{
			Claim: claimResult.Claim, ClientMessageID: "ask-user:ask-1", FrameID: "frame", ToolUseID: "ask-1",
			ToolUseFrameEventID: "ask-use", PendingFrameEventID: "ask-pending",
		})
		return appendErr
	})
	if !errors.Is(err, ErrEventConflict) {
		t.Fatalf("unbound AskUser facts error=%v", err)
	}

	var pending AppendFrameAskUserPendingResult
	err = repo.RunImmediate(ctx, func(tx *ImmediateTransaction) error {
		if _, appendErr := tx.AppendFrameAskUserReferences(ctx, AppendFrameAskUserReferencesInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID, FrameID: stream.FrameID, ToolUseID: "ask-1",
			ToolUseFrameEventID: "ask-use", ToolResultFrameEventID: "ask-pending",
		}); appendErr != nil {
			return appendErr
		}
		var appendErr error
		pending, appendErr = tx.AppendFrameAskUserPending(ctx, AppendFrameAskUserPendingInput{
			Claim: claimResult.Claim, ClientMessageID: "ask-user:ask-1", FrameID: "frame", ToolUseID: "ask-1",
			ToolUseFrameEventID: "ask-use", PendingFrameEventID: "ask-pending",
		})
		return appendErr
	})
	if err != nil || !pending.Created || pending.Prompt.Type != AskUserPromptEventType || pending.Pending.Type != AskUserResultEventType {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}
	if pending.Origin.StreamUID != stream.UID || pending.Origin.Epoch != 1 || pending.Origin.RunnerAttempt != claimResult.Claim.Attempt ||
		pending.Origin.ToolUseID != "ask-1" || pending.Origin.BranchID == "" || pending.Origin.BranchGeneration != 1 {
		t.Fatalf("origin=%#v", pending.Origin)
	}
	var pendingPayload AskUserResultEventV1
	if err := json.Unmarshal(pending.Pending.PayloadJSON, &pendingPayload); err != nil {
		t.Fatal(err)
	}
	if pending.Pending.Source != EventSourcePayload || pendingPayload.Result.Status != AskUserStatusAwaitingResponse || pendingPayload.Origin != pending.Origin {
		t.Fatalf("pending event=%#v payload=%#v", pending.Pending, pendingPayload)
	}
	terminal, found, err := repo.FindFrameAskUserTerminalResult(ctx, FindFrameAskUserTerminalResultInput{
		OwnerID: stream.OwnerID, FrameID: stream.FrameID, StreamUID: stream.UID,
		BranchID: pending.Origin.BranchID, BranchGeneration: pending.Origin.BranchGeneration, ToolUseID: pending.Origin.ToolUseID,
	})
	if err != nil || found {
		t.Fatalf("pending terminal=%#v found=%t err=%v", terminal, found, err)
	}
	decodedPending, err := DecodeAskUserResultEventV1(pending.Pending.PayloadJSON)
	if err != nil || decodedPending.Origin != pending.Origin || decodedPending.Result.Status != AskUserStatusAwaitingResponse {
		t.Fatalf("decoded pending=%#v err=%v", decodedPending, err)
	}
	invalidPending := map[string]any{}
	if json.Unmarshal(pending.Pending.PayloadJSON, &invalidPending) != nil {
		t.Fatal("decode pending fixture")
	}
	invalidPending["unexpected"] = true
	invalidPendingJSON, _ := json.Marshal(invalidPending)
	if _, err := DecodeAskUserResultEventV1(invalidPendingJSON); err == nil {
		t.Fatal("AskUser result event accepted an unknown field")
	}
	prompt, err := repo.GetFrameAskUserPrompt(ctx, GetFrameAskUserPromptInput{
		OwnerID: stream.OwnerID, FrameID: stream.FrameID, Origin: pending.Origin,
	})
	if err != nil || prompt.Origin != pending.Origin || len(prompt.Questions) != 1 || prompt.Questions[0].Question != "Which structure?" {
		t.Fatalf("prompt=%#v err=%v", prompt, err)
	}
	if _, err := repo.GetFrameAskUserPrompt(ctx, GetFrameAskUserPromptInput{
		OwnerID: "other-owner", FrameID: stream.FrameID, Origin: pending.Origin,
	}); !errors.Is(err, ErrOwnerMismatch) {
		t.Fatalf("foreign prompt error=%v", err)
	}

	answer, err := NewAskUserResultV1(AskUserActionAnswer, map[string]string{"Which structure?": "5FQD"}, "")
	if err != nil {
		t.Fatal(err)
	}
	var resolved Event
	var created bool
	err = repo.RunImmediate(ctx, func(tx *ImmediateTransaction) error {
		var appendErr error
		resolved, created, appendErr = tx.AppendFrameAskUserResult(ctx, AppendFrameAskUserResultInput{
			OwnerID: stream.OwnerID, FrameID: stream.FrameID, Origin: pending.Origin, Result: answer,
			ModelContinuation: `{"status":"answered","answers":{"Which structure?":"5FQD"}}`,
		})
		return appendErr
	})
	if err != nil || !created || resolved.Type != AskUserResultEventType || resolved.Source != EventSourcePayload {
		t.Fatalf("resolved=%#v created=%t err=%v", resolved, created, err)
	}
	expectedResultClientID, err := AskUserResultClientMessageIDV1(pending.Origin)
	if err != nil || resolved.ClientMessageID != expectedResultClientID {
		t.Fatalf("result client id=%q expected=%q err=%v", resolved.ClientMessageID, expectedResultClientID, err)
	}
	decodedResolved, err := DecodeAskUserResultEventV1(resolved.PayloadJSON)
	if err != nil || decodedResolved.Result.Status != AskUserStatusAnswered || decodedResolved.ModelContinuation == "" {
		t.Fatalf("decoded resolved=%#v err=%v", decodedResolved, err)
	}
	terminal, found, err = repo.FindFrameAskUserTerminalResult(ctx, FindFrameAskUserTerminalResultInput{
		OwnerID: stream.OwnerID, FrameID: stream.FrameID, StreamUID: stream.UID,
		BranchID: pending.Origin.BranchID, BranchGeneration: pending.Origin.BranchGeneration, ToolUseID: pending.Origin.ToolUseID,
	})
	if err != nil || !found || terminal.Origin != pending.Origin || terminal.Result.Status != AskUserStatusAnswered ||
		terminal.Result.Answers["Which structure?"] != "5FQD" || terminal.ModelContinuation != decodedResolved.ModelContinuation {
		t.Fatalf("terminal=%#v found=%t err=%v", terminal, found, err)
	}
	invalidOrigin := pending.Origin
	invalidOrigin.StreamUID = ""
	if _, err := AskUserResultClientMessageIDV1(invalidOrigin); err == nil {
		t.Fatal("AskUser result client id accepted incomplete origin")
	}
	err = repo.RunImmediate(ctx, func(tx *ImmediateTransaction) error {
		repeated, repeatedCreated, appendErr := tx.AppendFrameAskUserResult(ctx, AppendFrameAskUserResultInput{
			OwnerID: stream.OwnerID, FrameID: stream.FrameID, Origin: pending.Origin, Result: answer,
			ModelContinuation: `{"status":"answered","answers":{"Which structure?":"5FQD"}}`,
		})
		if appendErr != nil {
			return appendErr
		}
		if repeatedCreated || repeated.EventID != resolved.EventID {
			t.Fatalf("repeated=%#v created=%t", repeated, repeatedCreated)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE transcript_branch_state SET generation=generation+1 WHERE stream_uid=?`, stream.UID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.FindFrameAskUserTerminalResult(ctx, FindFrameAskUserTerminalResultInput{
		OwnerID: stream.OwnerID, FrameID: stream.FrameID, StreamUID: stream.UID,
		BranchID: pending.Origin.BranchID, BranchGeneration: pending.Origin.BranchGeneration, ToolUseID: pending.Origin.ToolUseID,
	}); !errors.Is(err, ErrBranchStateStale) {
		t.Fatalf("stale terminal lookup error=%v", err)
	}
	err = repo.RunImmediate(ctx, func(tx *ImmediateTransaction) error {
		repeated, repeatedCreated, appendErr := tx.AppendFrameAskUserResult(ctx, AppendFrameAskUserResultInput{
			OwnerID: stream.OwnerID, FrameID: stream.FrameID, Origin: pending.Origin, Result: answer,
			ModelContinuation: `{"status":"answered","answers":{"Which structure?":"5FQD"}}`,
		})
		if appendErr != nil {
			return appendErr
		}
		if repeatedCreated || repeated.EventID != resolved.EventID {
			t.Fatalf("post-branch retry=%#v created=%t", repeated, repeatedCreated)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := NewAskUserResultV1(AskUserActionCancel, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	err = repo.RunImmediate(ctx, func(tx *ImmediateTransaction) error {
		_, _, appendErr := tx.AppendFrameAskUserResult(ctx, AppendFrameAskUserResultInput{
			OwnerID: stream.OwnerID, FrameID: stream.FrameID, Origin: pending.Origin, Result: cancelled,
			ModelContinuation: "User cancelled the question. Continue without an answer — use your best judgment or skip this step.",
		})
		return appendErr
	})
	if !errors.Is(err, ErrEventConflict) {
		t.Fatalf("conflicting result error=%v", err)
	}
	err = repo.RunImmediate(ctx, func(tx *ImmediateTransaction) error {
		_, _, appendErr := tx.AppendFrameAskUserResult(ctx, AppendFrameAskUserResultInput{
			OwnerID: stream.OwnerID, FrameID: stream.FrameID, Origin: pending.Origin, Result: answer,
			ModelContinuation: "different model continuation",
		})
		return appendErr
	})
	if !errors.Is(err, ErrEventConflict) {
		t.Fatalf("conflicting model continuation error=%v", err)
	}
}

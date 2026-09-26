package server

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	eventjournal "synon-go/internal/persistence/journal"
	"synon-go/internal/sciencecapability"
	"synon-go/internal/skills"
)

func TestAskUserBindsRegisteredLabelBeforeRecoveryDecision(t *testing.T) {
	f := newAgentSaveArtifactsFixture(t)
	f.server.skillCatalog, f.server.scienceCapabilities = skills.NewCatalog(), &sciencecapability.Catalog{}
	addSelectionRouteEngine(f.server, "analysis", "first-engine", "First Engine")
	addSelectionRouteEngine(f.server, "analysis", "second-engine", "Second Engine")
	run := &sessionRunnerChatRun{
		SessionID: f.stream.SessionID, Transcript: &transcriptRunnerAuthority{Stream: f.stream, Claim: f.claim},
		RequiredScientificCapabilities: []string{"analysis"}, CorrectionReason: "plan_step_status_required",
	}
	var options []any
	for _, name := range []string{"First Engine", "Second Engine"} {
		option := managedExecutionSafeAskUserOptionFields(name, "Run this registered implementation.", "Supports the requested analysis.", "Requires setup.")
		option["recommended"] = len(options) == 0
		options = append(options, option)
	}
	result, err := f.server.executeAgentAskUserQuestion(withTranscriptRunnerChatRun(context.Background(), run), f.stream.SessionID, "registered-choice", "ask_user", map[string]any{
		"question": "Which implementation should perform this analysis?", "header": "Implementation", "options": options,
	})
	if err != nil {
		t.Fatal(err)
	}
	value := mapValue(result)
	if value["status"] != "implementation_decision_contract_incomplete" || len(anySliceValue(value["required_preflights"])) != 2 {
		t.Fatalf("registered choices lost their required preflight action during recovery: %#v", value)
	}
	if len(run.selectedImplementationsSnapshot()) != 0 {
		t.Fatal("option normalization selected an unanswered implementation")
	}
}

func TestRegisteredAnsweredChoiceReconciliationIsClosedAndRegistryBound(t *testing.T) {
	s := &Server{skillCatalog: skills.NewCatalog(), scienceCapabilities: &sciencecapability.Catalog{}}
	dependency := sciencecapability.ExecutionEvidenceResolver{EvidenceGroup: "point", Skill: "auxiliary", Implementation: "Auxiliary Engine"}
	addSelectionRouteEngine(s, "primary", "primary", "Primary Engine", dependency)
	addSelectionRouteEngine(s, "auxiliary", "auxiliary", "Auxiliary Engine")
	addSelectionRouteEngine(s, "other", "other", "Other Engine")
	for _, tc := range []struct {
		label, answer string
		want          bool
	}{
		{"Primary Engine", "Primary Engine", true},
		{"Auxiliary Engine口袋预测 + Primary Engine分子对接", "Auxiliary Engine口袋预测 + Primary Engine分子对接", true},
		{"Primary Engine + Other Engine", "Primary Engine + Other Engine", false},
		{"Primary Engine 999", "Primary Engine 999", false},
		{"Do not use Primary Engine", "Do not use Primary Engine", false},
		{"Primary Engine", "Other Engine", false},
		{"Continue existing setup", "Continue existing setup", false},
	} {
		t.Run(tc.label+"/"+tc.answer, func(t *testing.T) {
			item := map[string]any{"question": "Which route?", "options": []any{map[string]any{"label": tc.label}}}
			answers := map[string]string{"Which route?": tc.answer}
			original, err := compatibilityAnsweredAskUserContinuation(item, answers, nil)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := s.reconcileAnsweredAskUserSelection(item, answers, original)
			if err != nil {
				t.Fatal(err)
			}
			entries := []eventjournal.Entry{{EventID: 1, SourceEventType: "user_input_response", Message: eventjournal.Message{"messageOrigin": "input_response", "text": "choice: " + resolved}}}
			selected := selectedAskUserImplementationsFromRunnerEntries(entries)
			if tc.want && !reflect.DeepEqual(selected, []string{"Primary Engine"}) || !tc.want && len(selected) != 0 {
				t.Fatalf("selection=%v continuation=%s", selected, resolved)
			}
			if tc.want && tc.label != "Primary Engine" && !reflect.DeepEqual(selectedAskUserEvidenceResolversFromRunnerEntries(entries), []sciencecapability.ExecutionEvidenceResolver{dependency}) {
				t.Fatalf("compound answer did not bind its exact registered dependency: %s", resolved)
			}
			again, err := s.reconcileAnsweredAskUserSelection(item, answers, resolved)
			if err != nil || again != resolved {
				t.Fatalf("reconciliation is not idempotent: %s %v", again, err)
			}
		})
	}
}

func TestTypedAnswerReplayRepairsMissingRegisteredIdentityWithoutRewritingHistory(t *testing.T) {
	f := newAgentSaveArtifactsFixture(t)
	s := f.server
	s.skillCatalog, s.scienceCapabilities = skills.NewCatalog(), &sciencecapability.Catalog{}
	dependency := sciencecapability.ExecutionEvidenceResolver{EvidenceGroup: "point", Skill: "auxiliary", Implementation: "Auxiliary Engine"}
	addSelectionRouteEngine(s, "primary", "primary", "Primary Engine", dependency)
	addSelectionRouteEngine(s, "auxiliary", "auxiliary", "Auxiliary Engine")
	label := "Auxiliary Engine口袋预测 + Primary Engine分子对接"
	option := managedExecutionSafeAskUserOptionFields(label, "Use the selected registered route.", "Preserves the requested work.", "Requires execution.")
	option["recommended"] = true
	other := managedExecutionSafeAskUserOptionFields("Discuss inputs", "Discuss the available inputs first.", "Clarifies the goal.", "Defers execution.")
	other["recommended"] = false
	input := map[string]any{"question": "Which route should run?", "header": "Route", "options": []any{option, other}}
	result, err := s.executeAskUserQuestionTool("ask_user", input)
	if err != nil {
		t.Fatal(err)
	}
	questions, err := askUserQuestionsForPersistence(mapValue(result)["questions"])
	if err != nil {
		t.Fatal(err)
	}
	run := &sessionRunnerChatRun{SessionID: f.stream.SessionID, Transcript: &transcriptRunnerAuthority{Stream: f.stream, Claim: f.claim}}
	if _, _, err := s.parkTranscriptAskUser(context.Background(), run.Transcript, transcriptAskUserPause(f.stream.SessionID, "old-choice", "ask_user", questions)); err != nil {
		t.Fatal(err)
	}
	// Simulate an older writer which persisted only the selected visible label.
	catalog := s.skillCatalog
	s.skillCatalog = nil
	compatJSONRequest(t, s.Handler(), http.MethodPost, "/api/frames/"+f.stream.FrameID+"/resolve-input", f.stream.OwnerID, map[string]any{
		"responses": []any{map[string]any{"tool_id": "old-choice", "action": "answer", "answers": map[string]any{"Which route should run?": label}}},
	}, http.StatusOK)
	s.skillCatalog = catalog
	var before string
	if err := f.db.QueryRow(`SELECT json_extract(payload_json,'$.model_continuation') FROM transcript_events WHERE stream_uid=? AND event_type='ask_user_result' ORDER BY event_id DESC LIMIT 1`, f.stream.UID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	var old map[string]any
	if err := json.Unmarshal([]byte(before), &old); err != nil || old["implementations"] != nil {
		t.Fatalf("fixture was not a prose-only persisted answer: %s %v", before, err)
	}
	entries, err := s.runnerAskUserSelectionEntries(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(selectedAskUserImplementationsFromRunnerEntries(entries), []string{"Primary Engine"}) || !reflect.DeepEqual(selectedAskUserEvidenceResolversFromRunnerEntries(entries), []sciencecapability.ExecutionEvidenceResolver{dependency}) {
		t.Fatalf("typed answer projection lost the selected route: %#v", entries)
	}
	var after string
	if err := f.db.QueryRow(`SELECT json_extract(payload_json,'$.model_continuation') FROM transcript_events WHERE stream_uid=? AND event_type='ask_user_result' ORDER BY event_id DESC LIMIT 1`, f.stream.UID).Scan(&after); err != nil || after != before {
		t.Fatalf("immutable answer history changed: %v", err)
	}
}

func TestRegisteredChoiceReconciliationPreservesExplicitResolverAuthority(t *testing.T) {
	s := &Server{skillCatalog: skills.NewCatalog(), scienceCapabilities: &sciencecapability.Catalog{}}
	dependency := sciencecapability.ExecutionEvidenceResolver{EvidenceGroup: "input", Skill: "auxiliary", Implementation: "Auxiliary Engine"}
	addSelectionRouteEngine(s, "primary", "primary", "Primary Engine", dependency)
	addSelectionRouteEngine(s, "auxiliary", "auxiliary", "Auxiliary Engine")
	label := "Primary Engine + Auxiliary Engine"
	item := map[string]any{"question": "Which route?", "options": []any{map[string]any{"label": label}}}
	original := `{"status":"answered","evidence_resolvers":{"Which route?":{"evidence_group":"input","skill":"explicit-resolver","implementation":"Explicit Engine"}}}`
	got, err := s.reconcileAnsweredAskUserSelection(item, map[string]string{"Which route?": label}, original)
	if err != nil || got != original {
		t.Fatalf("label replay overwrote explicit resolver authority: %s %v", got, err)
	}
}

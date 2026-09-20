package server

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	eventjournal "synon-go/internal/persistence/journal"
	"synon-go/internal/sciencecapability"
	"synon-go/internal/skills"
)

func TestSelectedAskUserResolverConflictSurvivesReplay(t *testing.T) {
	a := sciencecapability.ExecutionEvidenceResolver{EvidenceGroup: "point", Skill: "resolver-a", Implementation: "Resolver A"}
	b := sciencecapability.ExecutionEvidenceResolver{EvidenceGroup: "point", Skill: "resolver-b", Implementation: "Resolver B"}
	c := sciencecapability.ExecutionEvidenceResolver{EvidenceGroup: "other", Skill: "resolver-b", Implementation: "Resolver B"}
	entry := func(id int64, call string, choices ...sciencecapability.ExecutionEvidenceResolver) eventjournal.Entry {
		// Historical durable data predates answer validation; preserve it rather
		// than using the current encoder to manufacture an invalid new answer.
		selections := map[string]any{}
		for i, choice := range choices {
			selections[string(rune('a'+i))] = map[string]any{"evidence_group": choice.EvidenceGroup, "skill": choice.Skill, "implementation": choice.Implementation}
		}
		return eventjournal.Entry{EventID: id, SourceEventType: "user_input_response", Message: eventjournal.Message{
			"type": "message", "role": "user", "messageOrigin": "input_response",
			"text": call + ": " + string(mustMarshalRawMessage(map[string]any{"status": "answered", "evidence_resolvers": selections})),
		}}
	}
	batched := entry(1, "first", a)
	batched.Message["text"] = stringValue(batched.Message["text"]) + "\n" + stringValue(entry(1, "second", b).Message["text"])
	for _, tc := range []struct {
		name    string
		entries []eventjournal.Entry
		want    []sciencecapability.ExecutionEvidenceResolver
		valid   bool
	}{
		{"simultaneous alternatives retain conflict", []eventjournal.Entry{entry(1, "first", a, b)}, []sciencecapability.ExecutionEvidenceResolver{a, b}, false},
		{"multiple calls in one event retain conflict", []eventjournal.Entry{batched}, []sciencecapability.ExecutionEvidenceResolver{a, b}, false},
		{"repeated identical answer is one choice", []eventjournal.Entry{entry(1, "first", a, a)}, []sciencecapability.ExecutionEvidenceResolver{a}, true},
		{"later explicit answer resolves conflict", []eventjournal.Entry{entry(1, "first", a, b), entry(2, "correction", b)}, []sciencecapability.ExecutionEvidenceResolver{b}, true},
		{"independent groups remain independent", []eventjournal.Entry{entry(1, "first", a, c)}, []sciencecapability.ExecutionEvidenceResolver{c, a}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := skills.NewCatalog()
			catalog.AddSkill(skills.Skill{Name: "primary", ImplementationIdentities: []string{"Primary"}})
			capabilities := &sciencecapability.Catalog{Capabilities: []sciencecapability.Definition{{ID: "analysis", AcceptedEngines: []sciencecapability.EngineDefinition{{ExecutionPack: sciencecapability.ExecutionPack{
				ID: "primary-pack", Mode: "local", Skill: "primary", EvidenceResolvers: []sciencecapability.ExecutionEvidenceResolver{a, b, c},
			}}}}}}
			original := string(mustMarshalRawMessage(tc.entries))
			for attempt := 0; attempt < 20; attempt++ {
				got := selectedAskUserEvidenceResolversFromRunnerEntries(tc.entries)
				if !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("replay discarded or reordered scope: got=%#v want=%#v", got, tc.want)
				}
				run := &sessionRunnerChatRun{SelectedImplementations: []string{"Primary"}}
				validated, valid := validatedSelectedAskUserEvidenceResolvers(catalog, capabilities, run, got)
				if valid != tc.valid || (!valid && len(validated) != 0) {
					t.Fatalf("conflicting scope authorized: valid=%t selections=%#v", valid, validated)
				}
				if !reflect.DeepEqual(run.selectedImplementationsSnapshot(), []string{"Primary"}) {
					t.Fatal("validation changed primary")
				}
			}
			if string(mustMarshalRawMessage(tc.entries)) != original {
				t.Fatal("replay changed historical evidence")
			}
		})
	}
}

func TestCompatibilityResolveInputResolverConflictCanBeCorrected(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	s := fixture.server
	var questions []any
	for _, question := range []string{"First input route?", "Second input route?"} {
		var options []any
		for _, name := range []string{"A", "B"} {
			option := managedExecutionSafeAskUserOptionFields("Resolver "+name, "Produce the controlled input.", "Uses a registered route.", "Requires execution.")
			option["recommended"] = name == "A"
			option["metadata"] = map[string]any{"evidence_resolver": map[string]any{
				"evidence_group": "point", "skill": "resolver-" + name, "implementation": "Resolver " + name,
			}}
			options = append(options, option)
		}
		questions = append(questions, map[string]any{"question": question, "header": "Input route", "options": options})
	}
	// The durable format still supports historical multi-question cards even
	// though the current public tool emits one question per invocation.
	var durableQuestions []any
	for _, question := range questions {
		parsed, err := s.executeAskUserQuestionTool("ask_user", mapValue(question))
		if err != nil {
			t.Fatal(err)
		}
		normalized, err := askUserQuestionsForPersistence(mapValue(parsed)["questions"])
		if err != nil {
			t.Fatal(err)
		}
		durableQuestions = append(durableQuestions, normalized...)
	}
	questions = durableQuestions
	authority := &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim}
	if _, _, err := s.parkTranscriptAskUser(context.Background(), authority, transcriptAskUserPause(fixture.stream.FrameID, "resolver-choice", "ask_user", questions)); err != nil {
		t.Fatal(err)
	}
	respond := func(second string, status int) {
		compatJSONRequest(t, s.Handler(), http.MethodPost, "/api/frames/"+fixture.stream.FrameID+"/resolve-input", fixture.stream.OwnerID, map[string]any{
			"responses": []any{map[string]any{"tool_id": "resolver-choice", "action": "answer", "answers": map[string]any{"First input route?": "Resolver A", "Second input route?": second}}},
		}, status)
	}
	respond("Resolver B", http.StatusBadRequest)
	pending, err := s.webConversationPendingConfirmations(fixture.stream.FrameID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("conflict consumed the pending question: %#v err=%v", pending, err)
	}
	var count int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='user_input_response'`, fixture.stream.UID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("conflict persisted a selection: count=%d err=%v", count, err)
	}
	respond("Resolver A", http.StatusOK)
	entries, err := s.loadTranscriptRunnerReplay(context.Background(), authority, 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	got := selectedAskUserEvidenceResolversFromRunnerEntries(entries)
	if len(got) != 1 || got[0].Implementation != "Resolver A" {
		t.Fatalf("corrected answer did not resume with exact selection: %#v", got)
	}
}

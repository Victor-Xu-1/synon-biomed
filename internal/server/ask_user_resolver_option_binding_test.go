package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
	transcriptstore "synon-go/internal/persistence/transcript"
	"synon-go/internal/sciencecapability"
	"synon-go/internal/skills"
)

func TestAskUserResolverTopLevelIdentityRemainsAuxiliaryThroughPersistence(t *testing.T) {
	t.Run("public implementation", func(t *testing.T) { testAskUserResolverIdentityPersistence(t, false, false) })
	t.Run("typed resolver without engine prose", func(t *testing.T) { testAskUserResolverIdentityPersistence(t, true, false) })
	t.Run("unique declared parent before first answer", func(t *testing.T) { testAskUserResolverIdentityPersistence(t, false, true) })
}

func testAskUserResolverIdentityPersistence(t *testing.T, typed, resolveParent bool) {
	fixture := newAgentSaveArtifactsFixture(t)
	server := fixture.server
	server.skillCatalog = skills.NewCatalog()
	server.skillCatalog.AddSkill(skills.Skill{Name: "primary-skill", ImplementationIdentities: []string{"Primary Engine"}, RequiredCapabilities: []string{"primary-analysis"}})
	server.skillCatalog.AddSkill(skills.Skill{Name: "resolver-skill", ImplementationIdentities: []string{"Resolver Engine"}, RequiredCapabilities: []string{"auxiliary-analysis"}})
	resolver := sciencecapability.ExecutionEvidenceResolver{EvidenceGroup: "control-point", Skill: "resolver-skill", Implementation: "Resolver Engine"}
	server.scienceCapabilities = &sciencecapability.Catalog{Capabilities: []sciencecapability.Definition{
		{ID: "primary-analysis", AcceptedEngines: []sciencecapability.EngineDefinition{{ExecutionPack: sciencecapability.ExecutionPack{
			ID: "primary-pack", Mode: "local", Skill: "primary-skill",
			Parameters:        []sciencecapability.ExecutionParameter{{Name: "point", Argument: "--point", Type: "number", Evidence: "resolved-user-input", EvidenceGroup: "control-point", EvidenceTerms: []string{"control point"}}},
			EvidenceResolvers: []sciencecapability.ExecutionEvidenceResolver{resolver},
		}}}},
		{ID: "auxiliary-analysis", AcceptedEngines: []sciencecapability.EngineDefinition{{ExecutionPack: sciencecapability.ExecutionPack{
			ID: "resolver-pack", Mode: "local", Skill: "resolver-skill", Packages: []sciencecapability.ExecutionPackage{{Manager: "conda", Spec: "fixture=1.0"}},
		}}}},
	}}
	required := []string{"primary-analysis", "auxiliary-analysis"}
	run := &sessionRunnerChatRun{
		SessionID: fixture.stream.FrameID, SelectedImplementations: []string{"Primary Engine"}, RequiredScientificCapabilities: append([]string(nil), required...),
		Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
	}
	selectionResult := map[string]any{"ok": true, "implementation_selection": map[string]any{
		"provenance": "registry-unique-local-pack", "implementations": []any{"Primary Engine"},
	}}
	if resolveParent {
		run.setSelectedImplementations()
		var resolved bool
		selectionResult, resolved = server.resolveUniqueRegisteredImplementationAskUser(withTranscriptRunnerChatRun(context.Background(), run), run,
			map[string]any{"questions": []askUserQuestion{{Options: []askUserQuestionOption{{Metadata: map[string]any{"implementation": "Resolver Engine"}}}}}})
		if !resolved || selectionResult["status"] != "implementation_selection_resolved" || len(run.selectedEvidenceResolversSnapshot()) != 0 {
			t.Fatalf("initial dependency choice has no primary route or prematurely authorized its resolver: %#v", selectionResult)
		}
		available, ok := selectionResult["available_evidence_resolvers"].(map[string][]sciencecapability.ExecutionEvidenceResolver)
		if !ok || !reflect.DeepEqual(available[resolver.EvidenceGroup], []sciencecapability.ExecutionEvidenceResolver{resolver}) ||
			!reflect.DeepEqual(selectionResult["required_capabilities"], required) {
			t.Fatalf("primary resolution concealed outstanding capabilities or auxiliary choice: %#v", selectionResult)
		}
	}
	if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: fixture.claim, ClientMessageID: "primary-selection", Phase: transcriptstore.RunnerPhaseExecuting,
		PayloadJSON: mustMarshalRawMessage(map[string]any{
			"status": "completed", "toolPhase": "completed", "toolResult": selectionResult,
		}),
	}); err != nil {
		t.Fatal(err)
	}
	option := managedExecutionSafeAskUserOptionFields("Use Resolver Engine", "Resolve the control point with this engine.", "Preserves verified input.", "Requires execution.")
	option["implementation"] = "Resolver Engine"
	option["resources"] = map[string]any{"cpu": "unresolved", "memory": "unresolved", "gpu": "not required"}
	option["recommended"] = true
	if typed {
		option["label"] = "Use registered route"
		delete(option, "implementation")
		delete(option, "resources")
		option["metadata"] = map[string]any{"evidence_resolver": map[string]any{
			"evidence_group": resolver.EvidenceGroup, "skill": resolver.Skill, "implementation": resolver.Implementation,
		}}
	}
	inputOption := managedExecutionSafeAskUserOptionFields("Provide input", "Provide the authoritative control point.", "Uses supplied evidence.", "Requires user input.")
	inputOption["recommended"] = false
	input := map[string]any{"question": "How should the control point be resolved?", "header": "Input source", "options": []any{option, inputOption}}
	original := string(mustMarshalRawMessage(input))
	checkpoint := server.sanitizeManagedAskUserToolCallForCheckpoint(run, agentruntime.ToolCall{ID: "ask-resolver-scope", Name: "ask_user", Arguments: mustMarshalRawMessage(input)})
	normalized := (serverAgentRuntimeToolGateway{server: server, taskRun: run}).normalizeAdmittedToolArguments("ask_user", input)
	if string(mustMarshalRawMessage(normalized)) != string(checkpoint.Arguments) || string(mustMarshalRawMessage(input)) != original {
		t.Fatal("execution/checkpoint normalization diverged or modified the original proposal")
	}
	if again := (serverAgentRuntimeToolGateway{server: server, taskRun: run}).normalizeAdmittedToolArguments("ask_user", normalized); string(mustMarshalRawMessage(again)) != string(checkpoint.Arguments) {
		t.Fatal("repeated normalization changed the resolver decision")
	}
	ctx := withTranscriptRunnerChatRun(context.Background(), run)
	result, err := server.executeAgentAskUserQuestion(ctx, fixture.stream.FrameID, "ask-resolver-scope", "ask_user", normalized)
	var pause *agentruntime.PauseError
	if !errors.As(err, &pause) {
		t.Fatalf("registered auxiliary option was treated as a primary engine choice: result=%#v err=%v", result, err)
	}
	if _, _, err := server.parkTranscriptAskUser(ctx, run.Transcript, pause); err != nil {
		t.Fatal(err)
	}
	pending, err := server.webConversationPendingConfirmations(fixture.stream.FrameID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("durable pending question=%#v err=%v", pending, err)
	}
	questions := anySliceValue(pause.Data["questions"])
	if len(questions) != 1 {
		t.Fatalf("normalized question=%#v", pause.Data)
	}
	question := mapValue(questions[0])
	choices := anySliceValue(question["options"])
	selected := mapValue(choices[0])
	metadata := mapValue(selected["metadata"])
	if stringValue(metadata["implementation"]) != "" || metadata["resources"] != nil || !reflect.DeepEqual(mapValue(metadata["evidence_resolver"]), map[string]any{
		"evidence_group": resolver.EvidenceGroup, "skill": resolver.Skill, "implementation": resolver.Implementation,
	}) {
		t.Fatalf("normalized resolver regained primary fields: %#v", selected)
	}
	_, continuation, _, err := compatibilityAskInputResult(question, compatibilityInputResponse{Action: "answer", Answers: map[string]string{stringValue(question["question"]): stringValue(selected["label"])}})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(continuation), &decoded); err != nil || decoded["implementations"] != nil {
		t.Fatalf("resolver answer supplied primary authority: %s err=%v", continuation, err)
	}
	answered := compatJSONRequest(t, server.Handler(), http.MethodPost, "/api/frames/"+fixture.stream.FrameID+"/resolve-input", fixture.stream.OwnerID, map[string]any{
		"responses": []any{map[string]any{
			"tool_id": "ask-resolver-scope", "action": "answer", "answers": map[string]any{stringValue(question["question"]): stringValue(selected["label"])},
		}},
	}, http.StatusOK)
	if answered["status"] != "accepted" {
		t.Fatalf("answer not accepted: %#v", answered)
	}
	compatJSONRequest(t, server.Handler(), http.MethodPost, "/api/frames/"+fixture.stream.FrameID+"/resolve-input", fixture.stream.OwnerID, map[string]any{
		"responses": []any{map[string]any{
			"tool_id": "ask-resolver-scope", "action": "answer", "answers": map[string]any{stringValue(question["question"]): stringValue(selected["label"])},
		}},
	}, http.StatusOK)
	var answerEvents int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='user_input_response'`, fixture.stream.UID).Scan(&answerEvents); err != nil || answerEvents != 1 {
		t.Fatalf("duplicate answer created another continuation: count=%d err=%v", answerEvents, err)
	}
	entries, err := server.loadTranscriptRunnerReplay(context.Background(), run.Transcript, 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	restored := &sessionRunnerChatRun{RequiredScientificCapabilities: append([]string(nil), required...)}
	restored.setSelectedImplementations(selectedAskUserImplementationsFromRunnerEntries(entries)...)
	validated, valid := validatedSelectedAskUserEvidenceResolvers(server.skillCatalog, server.scienceCapabilities, restored, selectedAskUserEvidenceResolversFromRunnerEntries(entries))
	if !valid || !reflect.DeepEqual(validated, []sciencecapability.ExecutionEvidenceResolver{resolver}) || !reflect.DeepEqual(restored.selectedImplementationsSnapshot(), []string{"Primary Engine"}) {
		t.Fatalf("continuation lost dependency or replaced primary: resolver=%#v primary=%v", validated, restored.selectedImplementationsSnapshot())
	}
	restored.setSelectedEvidenceResolvers(validated...)
	restoredCtx := withTranscriptRunnerChatRun(context.Background(), restored)
	if decision, blocked := server.managedEnvironmentImplementationDecision(restoredCtx, resolver.Implementation, true); blocked || decision != nil {
		t.Fatalf("selected auxiliary setup still blocked: %#v", decision)
	}
	if pack, found := server.registeredManagedEnvironmentExecutionPack(restoredCtx, resolver.Implementation); !found || pack.ID != "resolver-pack" {
		t.Fatalf("wrong auxiliary execution pack: %#v found=%v", pack, found)
	}
	if !reflect.DeepEqual(run.requiredScientificCapabilitiesSnapshot(), required) || len(run.selectedEvidenceResolversSnapshot()) != 0 {
		t.Fatal("proposing a choice authorized it or removed downstream requirements")
	}
	// Model replay may retain the durable answer and pinned Skill history while
	// the older parent-selection checkpoint is outside its window. Recover the
	// same parent from the exact catalog relationship, not a new engine choice.
	var bounded []eventjournal.Entry
	for _, entry := range entries {
		if len(registrySelectedImplementationsFromRunnerEntry(entry)) == 0 {
			bounded = append(bounded, entry)
		}
	}
	recovered := &sessionRunnerChatRun{ExecutedSkillNames: []string{"primary-skill"}, RequiredScientificCapabilities: append([]string(nil), required...)}
	recovered.setSelectedImplementations(selectedAskUserImplementationsFromRunnerEntries(bounded)...)
	if len(recovered.selectedImplementationsSnapshot()) != 0 {
		t.Fatal("bounded replay fixture still contains the parent selection")
	}
	validated, valid = validatedSelectedAskUserEvidenceResolvers(server.skillCatalog, server.scienceCapabilities, recovered, selectedAskUserEvidenceResolversFromRunnerEntries(bounded))
	if !valid || !reflect.DeepEqual(validated, []sciencecapability.ExecutionEvidenceResolver{resolver}) || !reflect.DeepEqual(recovered.selectedImplementationsSnapshot(), []string{"Primary Engine"}) {
		t.Fatalf("bounded durable replay did not recover the parent: resolver=%#v primary=%v", validated, recovered.selectedImplementationsSnapshot())
	}
}

func TestAskUserResolverBindingRespectsExplicitIdentity(t *testing.T) {
	resolver := sciencecapability.ExecutionEvidenceResolver{EvidenceGroup: "control-point", Skill: "resolver-skill", Implementation: "Resolver Engine"}
	for _, tc := range []struct {
		name, identity string
		nested, bind   bool
	}{
		{"top-level exact", "Resolver Engine", false, true},
		{"top-level skill alias", "resolver-skill", false, true},
		{"nested exact", "Resolver Engine", true, true},
		{"top-level unrelated", "Other Engine", false, false},
		{"top-level composite", "Resolver Engine+Other Engine", false, false},
		{"nested composite", "Resolver Engine+Other Engine", true, false},
		{"version not in contract", "Resolver Engine:2", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Mentioning a resolver in comparative prose cannot override the
			// proposed exact identity, at either supported input location.
			option := map[string]any{"label": "Local route", "description": "Compare with Resolver Engine before resolving the control point."}
			if tc.nested {
				option["metadata"] = map[string]any{"implementation": tc.identity, "resources": map[string]any{"cpu": "unresolved"}}
			} else {
				option["implementation"] = tc.identity
				option["resources"] = map[string]any{"cpu": "unresolved"}
			}
			updated, _ := managedExecutionPrioritizeResolverOption([]any{option}, resolver, resolver.EvidenceGroup, nil, "Resolve the control point?")
			foundOriginal := false
			for _, raw := range updated {
				candidate := mapValue(raw)
				if stringValue(candidate["label"]) != "Local route" {
					continue
				}
				foundOriginal = true
				metadata := mapValue(candidate["metadata"])
				bound := mapValue(metadata["evidence_resolver"]) != nil
				if bound != tc.bind {
					t.Fatalf("explicit identity rebound=%v want=%v option=%#v", bound, tc.bind, candidate)
				}
				if bound && (candidate["implementation"] != nil || candidate["resources"] != nil || metadata["implementation"] != nil || metadata["resources"] != nil) {
					t.Fatalf("auxiliary option retained primary authority: %#v", candidate)
				}
				if !bound && !reflect.DeepEqual(candidate, option) {
					t.Fatalf("unmatched proposal was silently changed: got=%#v want=%#v", candidate, option)
				}
			}
			if !foundOriginal {
				t.Fatal("original proposal was silently removed")
			}
		})
	}
}

func TestAskUserResolverBindingPreservesTypedScope(t *testing.T) {
	resolver := sciencecapability.ExecutionEvidenceResolver{EvidenceGroup: "control-point", Skill: "resolver-skill", Implementation: "Resolver Engine"}
	for _, tc := range []struct {
		name     string
		declared any
		bind     bool
	}{
		{"exact tuple without prose", map[string]any{"evidence_group": "control-point", "skill": "resolver-skill", "implementation": "Resolver Engine"}, true},
		{"other group", map[string]any{"evidence_group": "other-point", "skill": "resolver-skill", "implementation": "Resolver Engine"}, false},
		{"other skill", map[string]any{"evidence_group": "control-point", "skill": "other-skill", "implementation": "Resolver Engine"}, false},
		{"other implementation", map[string]any{"evidence_group": "control-point", "skill": "resolver-skill", "implementation": "Other Engine"}, false},
		{"missing implementation", map[string]any{"evidence_group": "control-point", "skill": "resolver-skill"}, false},
		{"wrong value type", map[string]any{"evidence_group": "control-point", "skill": "resolver-skill", "implementation": 1}, false},
		{"empty tuple", map[string]any{}, false},
		{"null tuple", nil, false},
		{"string tuple", "Resolver Engine", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			option := map[string]any{"label": "Use this route", "metadata": map[string]any{"evidence_resolver": tc.declared}}
			if !tc.bind {
				option["description"] = "Compare with Resolver Engine for the control point."
			}
			original := string(mustMarshalRawMessage(option))
			updated, _ := managedExecutionPrioritizeResolverOption([]any{option}, resolver, resolver.EvidenceGroup, nil, "Resolve the control point?")
			wantCount := 2 // A separate registered choice must not relabel this proposal.
			if tc.bind {
				wantCount = 1
			}
			if len(updated) != wantCount {
				t.Fatalf("typed proposal rewritten or duplicated: got=%#v wantCount=%d", updated, wantCount)
			}
			if string(mustMarshalRawMessage(option)) != original {
				t.Fatal("original proposal mutated")
			}
			if !tc.bind && !reflect.DeepEqual(mapValue(updated[1]), option) {
				t.Fatalf("conflicting or malformed typed scope overwritten: %#v", updated)
			}
			if tc.bind {
				question := map[string]any{"question": "Choose an input source", "options": updated}
				selected := compatibilitySelectedAskUserEvidenceResolvers(question, map[string]string{"Choose an input source": "Use this route"})
				if got := selected["Choose an input source"]; got.Implementation != resolver.Implementation || got.Skill != resolver.Skill || got.EvidenceGroup != resolver.EvidenceGroup {
					t.Fatalf("typed answer lost its exact scope: %#v", selected)
				}
			}
		})
	}
}

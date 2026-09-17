package server

import (
	"reflect"
	"testing"

	eventjournal "synon-go/internal/persistence/journal"
	transcriptstore "synon-go/internal/persistence/transcript"
	"synon-go/internal/sciencecapability"
)

func TestSelectedAskUserImplementationsComeOnlyFromDurableAnsweredChoices(t *testing.T) {
	first, err := transcriptstore.EncodeAnsweredAskUserModelContinuation(
		map[string]string{"Which generator?": "Pocket route · DiffSBDD"},
		map[string]string{"Which generator?": "DiffSBDD"},
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := transcriptstore.EncodeAnsweredAskUserModelContinuation(
		map[string]string{"Which generator?": "Pocket route · PocketXMol"},
		map[string]string{"Which generator?": "PocketXMol"},
	)
	if err != nil {
		t.Fatal(err)
	}
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{"type": "message", "role": "user", "text": "use Pocket2Mol"}},
		{EventID: 2, SourceEventType: "user_input_response", Message: eventjournal.Message{
			"type": "message", "role": "user", "messageOrigin": "input_response",
			"text": "Resolved input requests:\nask-1: " + first,
		}},
		{EventID: 3, SourceEventType: "user_input_response", Message: eventjournal.Message{
			"type": "message", "role": "user", "messageOrigin": "input_response",
			"text": "Resolved input requests:\nask-2: " + second,
		}},
	}
	if got := selectedAskUserImplementationsFromRunnerEntries(entries); !reflect.DeepEqual(got, []string{"PocketXMol"}) {
		t.Fatalf("selected implementations=%v", got)
	}
	if got := resolvedAskUserEvidenceFromRunnerEntries(entries); len(got) != 0 {
		t.Fatalf("closed model-authored option became parameter evidence: %v", got)
	}
}

func TestSelectedAskUserEvidenceResolverDoesNotReplacePrimaryImplementation(t *testing.T) {
	question := "How should the binding pocket be resolved?"
	continuation, err := transcriptstore.EncodeAnsweredAskUserModelContinuationWithEvidenceResolvers(
		map[string]string{question: "Use P2Rank detection"}, nil,
		map[string]transcriptstore.AskUserEvidenceResolverSelection{question: {
			EvidenceGroup: "binding-site-center", Skill: "p2rank-pocket-detection", Implementation: "P2Rank",
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	entries := []eventjournal.Entry{{EventID: 3, SourceEventType: "user_input_response", Message: eventjournal.Message{
		"type": "message", "role": "user", "messageOrigin": "input_response",
		"text": "Resolved input requests:\nask-pocket: " + continuation,
	}}}
	if got := selectedAskUserImplementationsFromRunnerEntries(entries); len(got) != 0 {
		t.Fatalf("auxiliary resolver replaced primary implementation: %v", got)
	}
	resolvers := selectedAskUserEvidenceResolversFromRunnerEntries(entries)
	if len(resolvers) != 1 || resolvers[0].EvidenceGroup != "binding-site-center" ||
		resolvers[0].Skill != "p2rank-pocket-detection" || resolvers[0].Implementation != "P2Rank" {
		t.Fatalf("resolvers=%#v", resolvers)
	}
}

func TestAuxiliaryAskUserDecisionPreservesEarlierRegistryPrimaryImplementation(t *testing.T) {
	question := "How should the controlled input be resolved?"
	continuation, err := transcriptstore.EncodeAnsweredAskUserModelContinuationWithEvidenceResolvers(
		map[string]string{question: "Use the registered resolver"}, nil,
		map[string]transcriptstore.AskUserEvidenceResolverSelection{question: {
			EvidenceGroup: "control-point", Skill: "evidence-resolver", Implementation: "Resolver Engine",
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
			"toolResult": map[string]any{
				"ok": true,
				"implementation_selection": map[string]any{
					"provenance":      "registry-unique-local-pack",
					"implementations": []any{"Primary Engine"},
				},
			},
		}},
		{EventID: 2, SourceEventType: "user_input_response", Message: eventjournal.Message{
			"type": "message", "role": "user", "messageOrigin": "input_response",
			"text": "Resolved input requests:\nask-resolver: " + continuation,
		}},
	}
	if got := selectedAskUserImplementationsFromRunnerEntries(entries); !reflect.DeepEqual(got, []string{"Primary Engine"}) {
		t.Fatalf("auxiliary decision retired registry primary implementation: %v", got)
	}
}

func TestLaterResolverRetryDoesNotBecomePrimaryImplementation(t *testing.T) {
	resolverQuestion := "How should the controlled input be resolved?"
	resolverContinuation, err := transcriptstore.EncodeAnsweredAskUserModelContinuationWithEvidenceResolvers(
		map[string]string{resolverQuestion: "Use the registered resolver"}, nil,
		map[string]transcriptstore.AskUserEvidenceResolverSelection{resolverQuestion: {
			EvidenceGroup: "control-point", Skill: "evidence-resolver", Implementation: "Resolver Engine",
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	retryContinuation, err := transcriptstore.EncodeAnsweredAskUserModelContinuation(
		map[string]string{"Retry the resolver?": "Resolver Engine 2.5.1"},
		map[string]string{"Retry the resolver?": "Resolver Engine 2.5.1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
			"toolResult": map[string]any{"ok": true, "implementation_selection": map[string]any{
				"provenance": "registry-unique-local-pack", "implementations": []any{"Primary Engine"},
			}},
		}},
		{EventID: 2, SourceEventType: "user_input_response", Message: eventjournal.Message{
			"type": "message", "role": "user", "messageOrigin": "input_response",
			"text": "Resolved input requests:\nask-resolver: " + resolverContinuation,
		}},
		{EventID: 3, SourceEventType: "user_input_response", Message: eventjournal.Message{
			"type": "message", "role": "user", "messageOrigin": "input_response",
			"text": "Resolved input requests:\nask-retry: " + retryContinuation,
		}},
	}
	if got := selectedAskUserImplementationsFromRunnerEntries(entries); !reflect.DeepEqual(got, []string{"Primary Engine"}) {
		t.Fatalf("resolver retry replaced primary implementation: %v", got)
	}
}

func TestSelectedEvidenceResolverExtendsOnlyRestrictedSkillAllowList(t *testing.T) {
	run := &sessionRunnerChatRun{}
	run.setSelectedEvidenceResolvers(sciencecapability.ExecutionEvidenceResolver{
		EvidenceGroup: "control-point", Skill: "evidence-resolver", Implementation: "Resolver Engine",
	})
	options := allowSelectedEvidenceResolverSkills(SessionRunnerChatOptions{
		RestrictSkillDiscovery: true,
		AllowedSkillNames:      []string{"primary-skill"},
		SelectedSkillNames:     []string{"primary-skill"},
		ExcludedSkillNames:     []string{"blocked-skill"},
	}, run)
	if !reflect.DeepEqual(options.AllowedSkillNames, []string{"primary-skill", "evidence-resolver"}) {
		t.Fatalf("resolver allow-list=%v", options.AllowedSkillNames)
	}
	if !reflect.DeepEqual(options.SelectedSkillNames, []string{"primary-skill"}) ||
		!reflect.DeepEqual(options.ExcludedSkillNames, []string{"blocked-skill"}) {
		t.Fatalf("resolver decision widened selection or exclusions: %#v", options)
	}
	unrestricted := allowSelectedEvidenceResolverSkills(SessionRunnerChatOptions{}, run)
	if len(unrestricted.AllowedSkillNames) != 0 {
		t.Fatalf("unrestricted policy received an unnecessary allow-list: %v", unrestricted.AllowedSkillNames)
	}
}

func TestResolvedAskUserEvidenceUsesOnlyDirectUserParameterText(t *testing.T) {
	question := "Which center?"
	answer := "Use center x=1.0 y=2.0 z=3.0"
	continuation, err := compatibilityAnsweredAskUserContinuation(
		map[string]any{"question": question, "options": []any{
			map[string]any{"label": "Provide center", "description": "Enter the measured center."},
			map[string]any{"label": "Stop", "description": "Stop without a center."},
		}},
		map[string]string{question: answer}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	entries := []eventjournal.Entry{{EventID: 1, SourceEventType: "user_input_response", Message: eventjournal.Message{
		"type": "message", "role": "user", "messageOrigin": "input_response",
		"text": "Resolved input requests:\nask-center: " + continuation,
	}}}
	if got := resolvedAskUserEvidenceFromRunnerEntries(entries); !reflect.DeepEqual(got, map[string]string{question: answer}) {
		t.Fatalf("direct user parameter evidence=%#v", got)
	}
}

func TestSelectedAskUserImplementationIgnoresCumulativeHistoricalChoices(t *testing.T) {
	pocket, err := transcriptstore.EncodeAnsweredAskUserModelContinuation(
		map[string]string{"Initial engine?": "Pocket2Mol"},
		map[string]string{"Initial engine?": "Pocket2Mol"},
	)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := transcriptstore.EncodeAnsweredAskUserModelContinuation(
		map[string]string{"Recovery engine?": "GraphBP"},
		map[string]string{"Recovery engine?": "GraphBP"},
	)
	if err != nil {
		t.Fatal(err)
	}
	entries := []eventjournal.Entry{
		{EventID: 1, SourceEventType: "user_input_response", Message: eventjournal.Message{
			"type": "message", "role": "user", "messageOrigin": "input_response",
			"text": "Resolved input requests:\ncall-pocket: " + pocket,
		}},
		{EventID: 2, SourceEventType: "user_input_response", Message: eventjournal.Message{
			"type": "message", "role": "user", "messageOrigin": "input_response",
			// The product deliberately repeats prior resolved calls so the model
			// receives complete decision context at every continuation.
			"text": "Resolved input requests:\ncall-graph: " + graph + "\ncall-pocket: " + pocket,
		}},
	}
	if got := selectedAskUserImplementationsFromRunnerEntries(entries); !reflect.DeepEqual(got, []string{"GraphBP"}) {
		t.Fatalf("cumulative history replaced the latest implementation: %v", got)
	}
}

func TestDelegatedAskUserImplementationNeverBecomesResolvedUserEvidence(t *testing.T) {
	delegated, err := transcriptstore.EncodeDelegatedAskUserModelContinuation(
		map[string]string{"Which center?": "AutoDock Vina"},
	)
	if err != nil {
		t.Fatal(err)
	}
	entries := []eventjournal.Entry{{EventID: 1, SourceEventType: "user_input_response", Message: eventjournal.Message{
		"type": "message", "role": "user", "messageOrigin": "input_response",
		"text": "Resolved input requests:\nask-center: " + delegated,
	}}}
	if got := selectedAskUserImplementationsFromRunnerEntries(entries); !reflect.DeepEqual(got, []string{"AutoDock Vina"}) {
		t.Fatalf("delegated implementation=%v", got)
	}
	if got := resolvedAskUserEvidenceFromRunnerEntries(entries); len(got) != 0 {
		t.Fatalf("model-delegated option became user evidence: %#v", got)
	}
}

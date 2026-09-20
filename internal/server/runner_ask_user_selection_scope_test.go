package server

import (
	"reflect"
	"testing"

	eventjournal "synon-go/internal/persistence/journal"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestSelectedAskUserResolverScopeFollowsPrimaryTransitions(t *testing.T) {
	primary := func(eventID int64, callID, implementation string) eventjournal.Entry {
		body, err := transcriptstore.EncodeAnsweredAskUserModelContinuation(map[string]string{"Choose engine": implementation}, map[string]string{"Choose engine": implementation})
		if err != nil {
			t.Fatal(err)
		}
		return eventjournal.Entry{EventID: eventID, SourceEventType: "user_input_response", Message: eventjournal.Message{
			"type": "message", "role": "user", "messageOrigin": "input_response", "text": callID + ": " + body,
		}}
	}
	auxiliary := func(eventID int64, callID, implementation string) eventjournal.Entry {
		body, err := transcriptstore.EncodeAnsweredAskUserModelContinuationWithEvidenceResolvers(map[string]string{"Choose source": implementation}, nil,
			map[string]transcriptstore.AskUserEvidenceResolverSelection{"Choose source": {EvidenceGroup: "control-point", Skill: "resolver-skill", Implementation: implementation}})
		if err != nil {
			t.Fatal(err)
		}
		return eventjournal.Entry{EventID: eventID, SourceEventType: "user_input_response", Message: eventjournal.Message{
			"type": "message", "role": "user", "messageOrigin": "input_response", "text": callID + ": " + body,
		}}
	}
	registered := func(eventID int64, implementation string, succeeded bool) eventjournal.Entry {
		return eventjournal.Entry{EventID: eventID, Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed", "toolResult": map[string]any{
				"ok": succeeded, "implementation_selection": map[string]any{"provenance": "registry-unique-local-pack", "implementations": []any{implementation}},
			},
		}}
	}
	for _, tc := range []struct {
		name, primary, resolver string
		entries                 []eventjournal.Entry
	}{
		{"new answered primary retires prior resolver", "Engine B", "", []eventjournal.Entry{primary(1, "primary-a", "Engine A"), auxiliary(2, "resolver-a", "Resolver A"), primary(3, "primary-b", "Engine B")}},
		{"reconfirmed primary retains resolver", "Engine A", "Resolver A", []eventjournal.Entry{primary(1, "primary-a", "Engine A"), auxiliary(2, "resolver-a", "Resolver A"), primary(3, "primary-a-again", "Engine A")}},
		{"new scope accepts its own resolver", "Engine B", "Resolver B", []eventjournal.Entry{primary(1, "primary-a", "Engine A"), auxiliary(2, "resolver-a", "Resolver A"), primary(3, "primary-b", "Engine B"), auxiliary(4, "resolver-b", "Resolver B")}},
		{"registry primary change retires prior resolver", "Engine B", "", []eventjournal.Entry{registered(1, "Engine A", true), auxiliary(2, "resolver-a", "Resolver A"), registered(3, "Engine B", true)}},
		{"same registry primary retains resolver", "Engine A", "Resolver A", []eventjournal.Entry{registered(1, "Engine A", true), auxiliary(2, "resolver-a", "Resolver A"), registered(3, "Engine A", true)}},
		{"failed registry result has no selection authority", "Engine A", "Resolver A", []eventjournal.Entry{registered(1, "Engine A", true), auxiliary(2, "resolver-a", "Resolver A"), registered(3, "Engine B", false)}},
		{"auxiliary after registry change belongs to new scope", "Engine B", "Resolver B", []eventjournal.Entry{registered(1, "Engine A", true), auxiliary(2, "resolver-a", "Resolver A"), registered(3, "Engine B", true), auxiliary(4, "resolver-b", "Resolver B")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := string(mustMarshalRawMessage(tc.entries))
			if got := selectedAskUserImplementationsFromRunnerEntries(tc.entries); !reflect.DeepEqual(got, []string{tc.primary}) {
				t.Fatalf("active primary=%v want=%s", got, tc.primary)
			}
			resolvers := selectedAskUserEvidenceResolversFromRunnerEntries(tc.entries)
			if (tc.resolver == "" && len(resolvers) != 0) || (tc.resolver != "" && (len(resolvers) != 1 || resolvers[0].Implementation != tc.resolver)) {
				t.Fatalf("active auxiliary=%#v want=%s", resolvers, tc.resolver)
			}
			if string(mustMarshalRawMessage(tc.entries)) != original {
				t.Fatal("active-state projection modified historical evidence")
			}
		})
	}
}

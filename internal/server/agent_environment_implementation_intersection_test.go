package server

import (
	"context"
	"reflect"
	"testing"

	transcriptstore "synon-go/internal/persistence/transcript"
	"synon-go/internal/sciencecapability"
	"synon-go/internal/skills"
)

func intersectionSelectionCatalogs() (*skills.Catalog, *sciencecapability.Catalog) {
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{Name: "combined", ImplementationIdentities: []string{"Combined Engine"}, RequiredCapabilities: []string{"analysis-a", "analysis-b"}})
	catalog.AddSkill(skills.Skill{Name: "first", ImplementationIdentities: []string{"First Engine"}, RequiredCapabilities: []string{"analysis-a"}})
	catalog.AddSkill(skills.Skill{Name: "second", ImplementationIdentities: []string{"Second Engine"}, RequiredCapabilities: []string{"analysis-b"}})
	pack := func(name string) sciencecapability.EngineDefinition {
		return sciencecapability.EngineDefinition{ID: name, ExecutionPack: sciencecapability.ExecutionPack{
			ID: name + "-pack", Mode: "local", Skill: name, Provider: "local-conda",
			Packages: []sciencecapability.ExecutionPackage{{Manager: "conda", Spec: "fixture=1.0"}},
		}}
	}
	return catalog, &sciencecapability.Catalog{Capabilities: []sciencecapability.Definition{
		{ID: "analysis-a", AcceptedEngines: []sciencecapability.EngineDefinition{pack("combined"), pack("first")}},
		{ID: "analysis-b", AcceptedEngines: []sciencecapability.EngineDefinition{pack("combined"), pack("second")}},
	}}
}

func TestUniqueRegisteredLocalImplementationIntersectsRequiredCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name     string
		change   func(*skills.Catalog, *sciencecapability.Catalog)
		required []string
		want     string
	}{
		{name: "one complete engine among stage alternatives", required: []string{"analysis-a", "analysis-b"}, want: "Combined Engine"},
		{name: "resource flags are not scientific alternatives", required: []string{"analysis-b", "gpu", "analysis-a", "analysis-a"}, want: "Combined Engine"},
		{name: "genuine multiple complete engines", required: []string{"analysis-a", "analysis-b"}, change: func(_ *skills.Catalog, c *sciencecapability.Catalog) {
			c.Capabilities[1].AcceptedEngines = append(c.Capabilities[1].AcceptedEngines, c.Capabilities[0].AcceptedEngines[1])
		}},
		{name: "disjoint stages cannot imply composition", required: []string{"analysis-a", "analysis-b"}, change: func(_ *skills.Catalog, c *sciencecapability.Catalog) {
			c.Capabilities[0].AcceptedEngines = c.Capabilities[0].AcceptedEngines[1:]
			c.Capabilities[1].AcceptedEngines = c.Capabilities[1].AcceptedEngines[1:]
		}},
		{name: "unavailable common implementation", required: []string{"analysis-a", "analysis-b"}, change: func(_ *skills.Catalog, c *sciencecapability.Catalog) {
			c.Capabilities[1].AcceptedEngines[0].ExecutionPack.Mode = "unavailable"
		}},
		{name: "missing capability stays unresolved", required: []string{"analysis-a", "missing"}},
		{name: "single capability still requires a choice", required: []string{"analysis-a"}},
		{name: "resource flags alone do not select engine", required: []string{"cpu", "gpu"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog, capabilities := intersectionSelectionCatalogs()
			if tc.change != nil {
				tc.change(catalog, capabilities)
			}
			s := &Server{skillCatalog: catalog, scienceCapabilities: capabilities}
			got, found := s.uniqueRegisteredLocalImplementation(tc.required)
			if got != tc.want || found != (tc.want != "") {
				t.Fatalf("full requirement intersection=%q found=%t want=%q", got, found, tc.want)
			}
		})
	}
}

func TestUniqueRegisteredIntersectionPersistsOriginalSelectionAuthority(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	s := fixture.server
	s.skillCatalog, s.scienceCapabilities = intersectionSelectionCatalogs()
	required := []string{"analysis-a", "analysis-b"}
	run := &sessionRunnerChatRun{
		SessionID: fixture.stream.FrameID, RequiredScientificCapabilities: append([]string(nil), required...),
		ImplementationSelectionRequired: true, Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
	}
	ctx := withTranscriptRunnerChatRun(context.Background(), run)
	options := make([]any, 0, 2)
	for _, name := range []string{"Combined Engine", "First Engine"} {
		option := managedExecutionSafeAskUserOptionFields(name, "Run the registered analysis.", "Uses a registered implementation.", "Requires local execution.")
		option["implementation"] = name
		option["resources"] = map[string]any{"cpu": "unresolved", "memory": "unresolved", "gpu": "not required"}
		option["recommended"] = len(options) == 0
		options = append(options, option)
	}
	result, err := s.executeAgentAskUserQuestion(ctx, fixture.stream.FrameID, "ask-common-engine", "ask_user", map[string]any{
		"question": "Which implementation supports the requested analyses?", "header": "Implementation", "options": options,
	})
	if err != nil || stringValue(mapValue(result)["status"]) != "implementation_selection_resolved" {
		t.Fatalf("unique whole-task implementation reopened a choice: result=%#v err=%v", result, err)
	}
	if !reflect.DeepEqual(run.requiredScientificCapabilitiesSnapshot(), required) {
		t.Fatal("selecting a complete implementation removed task capabilities")
	}
	if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(ctx, transcriptstore.AppendRunnerCheckpointInput{
		Claim: fixture.claim, ClientMessageID: "complete-selection", Phase: transcriptstore.RunnerPhaseExecuting,
		PayloadJSON: mustMarshalRawMessage(map[string]any{"status": "completed", "toolPhase": "completed", "toolResult": result}),
	}); err != nil {
		t.Fatal(err)
	}
	entries, err := s.loadTranscriptRunnerReplay(context.Background(), run.Transcript, 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	restored := &sessionRunnerChatRun{RequiredScientificCapabilities: append([]string(nil), required...)}
	restored.setSelectedImplementations(selectedAskUserImplementationsFromRunnerEntries(entries)...)
	if !reflect.DeepEqual(restored.selectedImplementationsSnapshot(), []string{"Combined Engine"}) || len(resolvedAskUserEvidenceFromRunnerEntries(entries)) != 0 {
		t.Fatalf("registry receipt lost or confused with user evidence: %v", restored.selectedImplementationsSnapshot())
	}
	if pack, found := s.registeredManagedEnvironmentExecutionPack(withTranscriptRunnerChatRun(context.Background(), restored), "Combined Engine"); !found || pack.ID != "combined-pack" {
		t.Fatalf("replayed selection did not bind the existing unique pack: %#v found=%t", pack, found)
	}
}

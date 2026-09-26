package server

import (
	"context"
	"strings"
	"testing"

	"synon-go/internal/sciencecapability"
	"synon-go/internal/skills"
)

func addSelectionRouteEngine(s *Server, capability, skill, identity string, resolvers ...sciencecapability.ExecutionEvidenceResolver) {
	s.skillCatalog.AddSkill(skills.Skill{Name: skill, ImplementationIdentities: []string{identity}, RequiredCapabilities: []string{capability}})
	pack := sciencecapability.ExecutionPack{ID: skill + "-pack", Mode: "local", Skill: skill, EvidenceResolvers: resolvers,
		Packages: []sciencecapability.ExecutionPackage{{Manager: "conda", Spec: "fixture=1.0"}},
	}
	groups := map[string]bool{}
	for _, resolver := range resolvers {
		if !groups[resolver.EvidenceGroup] {
			pack.Parameters = append(pack.Parameters, sciencecapability.ExecutionParameter{Name: resolver.EvidenceGroup, Type: "number", Evidence: "resolved-user-input", EvidenceGroup: resolver.EvidenceGroup, EvidenceTerms: []string{"controlled input"}})
			groups[resolver.EvidenceGroup] = true
		}
	}
	engine := sciencecapability.EngineDefinition{ID: skill, ExecutionPack: pack}
	for i := range s.scienceCapabilities.Capabilities {
		if s.scienceCapabilities.Capabilities[i].ID == capability {
			s.scienceCapabilities.Capabilities[i].AcceptedEngines = append(s.scienceCapabilities.Capabilities[i].AcceptedEngines, engine)
			return
		}
	}
	s.scienceCapabilities.Capabilities = append(s.scienceCapabilities.Capabilities, sciencecapability.Definition{ID: capability, AcceptedEngines: []sciencecapability.EngineDefinition{engine}})
}

func TestRegisteredResolverRouteDoesNotGuessUnnamedProvisioningStage(t *testing.T) {
	s := &Server{skillCatalog: skills.NewCatalog(), scienceCapabilities: &sciencecapability.Catalog{}}
	addSelectionRouteEngine(s, "primary-analysis", "primary", "Primary Engine", sciencecapability.ExecutionEvidenceResolver{EvidenceGroup: "point", Skill: "auxiliary", Implementation: "Auxiliary Engine"})
	addSelectionRouteEngine(s, "auxiliary-analysis", "auxiliary", "Auxiliary Engine")
	run := &sessionRunnerChatRun{RequiredScientificCapabilities: []string{"primary-analysis", "auxiliary-analysis"}}
	ctx := withTranscriptRunnerChatRun(context.Background(), run)
	if got := s.canonicalManagedEnvironmentImplementation(ctx, ""); got != "" || len(run.selectedImplementationsSnapshot()) != 0 {
		t.Fatalf("unnamed stage was converted into primary provisioning: got=%q selected=%v", got, run.selectedImplementationsSnapshot())
	}
	if got := s.canonicalManagedEnvironmentImplementation(ctx, "Auxiliary Engine"); got != "Auxiliary Engine" || len(run.selectedImplementationsSnapshot()) != 0 {
		t.Fatalf("unanswered auxiliary replaced its primary: got=%q selected=%v", got, run.selectedImplementationsSnapshot())
	}
	if got := s.canonicalManagedEnvironmentImplementation(ctx, "Primary Engine"); got != "Primary Engine" || len(run.selectedEvidenceResolversSnapshot()) != 0 {
		t.Fatalf("explicit root did not preserve separate auxiliary authorization: got=%q", got)
	}
}

func TestAskUserRegisteredParentRoutesStillRequireTheirOwnPreflights(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	s := fixture.server
	s.skillCatalog, s.scienceCapabilities = skills.NewCatalog(), &sciencecapability.Catalog{}
	resolver := sciencecapability.ExecutionEvidenceResolver{EvidenceGroup: "point", Skill: "auxiliary", Implementation: "Auxiliary Engine"}
	addSelectionRouteEngine(s, "primary-analysis", "first-primary", "First Primary", resolver)
	addSelectionRouteEngine(s, "primary-analysis", "second-primary", "Second Primary", resolver)
	addSelectionRouteEngine(s, "auxiliary-analysis", "auxiliary", "Auxiliary Engine")
	run := &sessionRunnerChatRun{RequiredScientificCapabilities: []string{"primary-analysis", "auxiliary-analysis"}, Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim}}
	var options []any
	for _, identity := range []string{"First Primary", "Second Primary"} {
		option := managedExecutionSafeAskUserOptionFields(identity, "Run the registered primary route.", "Preserves the required analyses.", "Requires setup and execution.")
		option["implementation"] = identity
		option["resources"] = map[string]any{"cpu": "unresolved", "memory": "unresolved", "gpu": "not required"}
		option["recommended"] = len(options) == 0
		options = append(options, option)
	}
	result, err := s.executeAgentAskUserQuestion(withTranscriptRunnerChatRun(context.Background(), run), fixture.stream.FrameID, "compare-parents", "ask_user",
		map[string]any{"question": "Which primary implementation should be used?", "header": "Implementation", "options": options})
	if err != nil {
		t.Fatal(err)
	}
	value := mapValue(result)
	if len(anySliceValue(value["required_preflights"])) != 2 || strings.Contains(strings.Join(stringArrayValue(value["issues"]), " "), "does not provide required capabilities") {
		t.Fatalf("registered primary routes were rejected as partial engines or escaped preflight: %#v", value)
	}
	if len(run.selectedImplementationsSnapshot()) != 0 || len(run.selectedEvidenceResolversSnapshot()) != 0 {
		t.Fatal("comparing routes authorized an implementation")
	}
}

func TestRegisteredResolverRoutesUseShippedContracts(t *testing.T) {
	capabilities, err := sciencecapability.Load("../sciencecapability/scientific-capabilities.v2.json")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{scienceCapabilities: &capabilities, skillCatalog: skills.Load([]string{"../../skills/synonbiomed"})}
	if failures := s.skillCatalog.LoadErrors(); len(failures) != 0 {
		t.Fatalf("load shipped Skills: %#v", failures)
	}
	tested := 0
	for _, capability := range capabilities.Capabilities {
		for _, engine := range capability.AcceptedEngines {
			pack := engine.ExecutionPack
			if pack.Mode != "local" || len(pack.EvidenceResolvers) != 1 {
				continue
			}
			resolver := pack.EvidenceResolvers[0]
			parent, found := findCatalogSkill(s.skillCatalog, pack.Skill)
			if !found {
				t.Fatalf("shipped parent Skill missing: %s", pack.Skill)
			}
			for _, dependency := range capabilities.Capabilities {
				for _, target := range dependency.AcceptedEngines {
					if target.ExecutionPack.Mode != "local" || !strings.EqualFold(target.ExecutionPack.Skill, resolver.Skill) {
						continue
					}
					t.Run(pack.ID+"/"+target.ExecutionPack.ID, func(t *testing.T) {
						candidates := registeredImplementationRouteCandidates(s.skillCatalog, &capabilities, []string{capability.ID, dependency.ID})
						matched := false
						for _, candidate := range candidates {
							matched = matched || skillSupportsSelectedImplementation(parent, []string{candidate})
						}
						if !matched {
							t.Fatalf("shipped declared parent/dependency lost route: candidates=%v", candidates)
						}
						t.Logf("validated shipped route %s with dependency %s; no scientific execution", pack.ID, target.ExecutionPack.ID)
					})
					tested++
				}
			}
		}
	}
	if tested == 0 {
		t.Fatal("no shipped single-resolver route was exercised")
	}
}

func TestUniqueRegisteredImplementationUsesOnlyDeclaredResolverRoutes(t *testing.T) {
	auxiliary := sciencecapability.ExecutionEvidenceResolver{EvidenceGroup: "point", Skill: "auxiliary", Implementation: "Auxiliary Engine"}
	for _, tc := range []struct {
		name     string
		change   func(*Server)
		required []string
		want     string
	}{
		{name: "parent plus declared auxiliary", required: []string{"primary-analysis", "auxiliary-analysis"}, want: "Primary Engine"},
		{name: "unlinked stages cannot be composed", required: []string{"primary-analysis", "auxiliary-analysis"}, change: func(s *Server) {
			s.scienceCapabilities.Capabilities[0].AcceptedEngines[0].ExecutionPack.EvidenceResolvers = nil
		}},
		{name: "unknown input group cannot grant a route", required: []string{"primary-analysis", "auxiliary-analysis"}, change: func(s *Server) {
			s.scienceCapabilities.Capabilities[0].AcceptedEngines[0].ExecutionPack.Parameters = nil
		}},
		{name: "unavailable dependency cannot cover stage", required: []string{"primary-analysis", "auxiliary-analysis"}, change: func(s *Server) {
			s.scienceCapabilities.Capabilities[1].AcceptedEngines[0].ExecutionPack.Mode = "unavailable"
		}},
		{name: "resolver identity must match its skill", required: []string{"primary-analysis", "auxiliary-analysis"}, change: func(s *Server) {
			s.scienceCapabilities.Capabilities[0].AcceptedEngines[0].ExecutionPack.EvidenceResolvers[0].Implementation = "Unregistered Composite"
		}},
		{name: "equivalent alternative dependencies keep parent unique", required: []string{"primary-analysis", "auxiliary-analysis"}, want: "Primary Engine", change: func(s *Server) {
			addSelectionRouteEngine(s, "auxiliary-analysis", "other-auxiliary", "Other Auxiliary")
			p := &s.scienceCapabilities.Capabilities[0].AcceptedEngines[0].ExecutionPack
			p.EvidenceResolvers = append(p.EvidenceResolvers, sciencecapability.ExecutionEvidenceResolver{EvidenceGroup: "point", Skill: "other-auxiliary", Implementation: "Other Auxiliary"})
		}},
		{name: "mutually exclusive alternatives are not unioned", required: []string{"primary-analysis", "auxiliary-analysis", "other-analysis"}, change: func(s *Server) {
			addSelectionRouteEngine(s, "other-analysis", "other-auxiliary", "Other Auxiliary")
			p := &s.scienceCapabilities.Capabilities[0].AcceptedEngines[0].ExecutionPack
			p.EvidenceResolvers = append(p.EvidenceResolvers, sciencecapability.ExecutionEvidenceResolver{EvidenceGroup: "point", Skill: "other-auxiliary", Implementation: "Other Auxiliary"})
		}},
		{name: "two viable parents remain a decision", required: []string{"primary-analysis", "auxiliary-analysis"}, change: func(s *Server) {
			addSelectionRouteEngine(s, "primary-analysis", "other-primary", "Other Primary", auxiliary)
		}},
		{name: "independent input groups preserve all stages", required: []string{"primary-analysis", "auxiliary-analysis", "other-analysis"}, want: "Primary Engine", change: func(s *Server) {
			addSelectionRouteEngine(s, "other-analysis", "other-auxiliary", "Other Auxiliary")
			p := &s.scienceCapabilities.Capabilities[0].AcceptedEngines[0].ExecutionPack
			p.Parameters = append(p.Parameters, sciencecapability.ExecutionParameter{Name: "other", Type: "number", Evidence: "resolved-user-input", EvidenceGroup: "other"})
			p.EvidenceResolvers = append(p.EvidenceResolvers, sciencecapability.ExecutionEvidenceResolver{EvidenceGroup: "other", Skill: "other-auxiliary", Implementation: "Other Auxiliary"})
		}},
		{name: "same identity cannot merge different owners", required: []string{"primary-analysis", "auxiliary-analysis"}, change: func(s *Server) {
			addSelectionRouteEngine(s, "primary-analysis", "other-primary", "Primary Engine", auxiliary)
		}},
		{name: "ambiguous local packs do not select an environment", required: []string{"primary-analysis", "auxiliary-analysis"}, change: func(s *Server) {
			other := s.scienceCapabilities.Capabilities[0].AcceptedEngines[0]
			other.ExecutionPack.ID = "different-provider-pack"
			other.ExecutionPack.Provider = "local-container"
			s.scienceCapabilities.Capabilities[0].AcceptedEngines = append(s.scienceCapabilities.Capabilities[0].AcceptedEngines, other)
		}},
		{name: "dependency cycle cannot manufacture coverage", required: []string{"primary-analysis", "auxiliary-analysis"}, change: func(s *Server) {
			p := &s.scienceCapabilities.Capabilities[1].AcceptedEngines[0].ExecutionPack
			p.Parameters = []sciencecapability.ExecutionParameter{{Name: "feedback", Type: "number", Evidence: "resolved-user-input", EvidenceGroup: "feedback"}}
			p.EvidenceResolvers = []sciencecapability.ExecutionEvidenceResolver{{EvidenceGroup: "feedback", Skill: "primary", Implementation: "Primary Engine"}}
		}},
		{name: "unselected transitive stages stay unresolved", required: []string{"primary-analysis", "auxiliary-analysis", "leaf-analysis"}, change: func(s *Server) {
			addSelectionRouteEngine(s, "leaf-analysis", "leaf", "Leaf Engine")
			p := &s.scienceCapabilities.Capabilities[1].AcceptedEngines[0].ExecutionPack
			p.Parameters = []sciencecapability.ExecutionParameter{{Name: "leaf", Type: "number", Evidence: "resolved-user-input", EvidenceGroup: "leaf"}}
			p.EvidenceResolvers = []sciencecapability.ExecutionEvidenceResolver{{EvidenceGroup: "leaf", Skill: "leaf", Implementation: "Leaf Engine"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{skillCatalog: skills.NewCatalog(), scienceCapabilities: &sciencecapability.Catalog{}}
			addSelectionRouteEngine(s, "primary-analysis", "primary", "Primary Engine", auxiliary)
			addSelectionRouteEngine(s, "auxiliary-analysis", "auxiliary", "Auxiliary Engine")
			if tc.change != nil {
				tc.change(s)
			}
			got, found := s.uniqueRegisteredLocalImplementation(tc.required)
			if got != tc.want || found != (tc.want != "") {
				t.Fatalf("declared route root=%q found=%t want=%q", got, found, tc.want)
			}
		})
	}
}

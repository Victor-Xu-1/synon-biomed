package server

import (
	"reflect"
	"testing"

	"synon-go/internal/sciencecapability"
	"synon-go/internal/skills"
)

func TestAnsweredResolverRestoresOnlyCompatiblePrimaryScope(t *testing.T) {
	for _, tc := range []struct {
		name, selected, expected string
		loaded                   []string
		valid, sharedParent      bool
	}{
		{"unique parent without loaded history", "", "Primary Engine", nil, true, false},
		{"unique parent with loaded history", "", "Primary Engine", []string{"primary-skill"}, true, false},
		{"current primary disambiguates shared resolver", "Primary Engine", "Primary Engine", []string{"other-skill"}, true, true},
		{"retired loaded primary cannot replace current selection", "Other Engine", "Other Engine", []string{"primary-skill"}, false, false},
		{"global fallback cannot replace current selection", "Other Engine", "Other Engine", nil, false, false},
		{"unknown selection cannot inherit old loaded authority", "Unregistered Engine", "Unregistered Engine", []string{"primary-skill"}, false, false},
		{"loaded history does not resolve multiple possible parents", "", "", []string{"primary-skill"}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			skillCatalog := skills.NewCatalog()
			for _, skill := range []skills.Skill{
				{Name: "primary-skill", ImplementationIdentities: []string{"Primary Engine"}},
				{Name: "other-skill", ImplementationIdentities: []string{"Other Engine"}},
				{Name: "resolver-skill", ImplementationIdentities: []string{"Resolver Engine"}},
			} {
				skillCatalog.AddSkill(skill)
			}
			resolver := sciencecapability.ExecutionEvidenceResolver{EvidenceGroup: "control-point", Skill: "resolver-skill", Implementation: "Resolver Engine"}
			primary := sciencecapability.ExecutionPack{ID: "primary-pack", Mode: "local", Skill: "primary-skill", EvidenceResolvers: []sciencecapability.ExecutionEvidenceResolver{resolver}}
			other := sciencecapability.ExecutionPack{ID: "other-pack", Mode: "local", Skill: "other-skill"}
			if tc.sharedParent {
				other.EvidenceResolvers = []sciencecapability.ExecutionEvidenceResolver{resolver}
			}
			catalog := &sciencecapability.Catalog{Capabilities: []sciencecapability.Definition{
				{ID: "primary-analysis", AcceptedEngines: []sciencecapability.EngineDefinition{{ID: "primary", ExecutionPack: primary}}},
				{ID: "other-analysis", AcceptedEngines: []sciencecapability.EngineDefinition{{ID: "other", ExecutionPack: other}}},
			}}
			run := &sessionRunnerChatRun{ExecutedSkillNames: tc.loaded, RequiredScientificCapabilities: []string{"primary-analysis", "auxiliary-analysis"}}
			if tc.selected != "" {
				run.setSelectedImplementations(tc.selected)
			}
			if tc.selected == "Other Engine" && !tc.sharedParent {
				if offered := managedExecutionEvidenceResolversForSelectedImplementations(skillCatalog, catalog, run); len(offered[resolver.EvidenceGroup]) != 0 {
					t.Errorf("retired primary still supplied resolver choices: %#v", offered)
				}
			}
			validated, valid := validatedSelectedAskUserEvidenceResolvers(skillCatalog, catalog, run, []sciencecapability.ExecutionEvidenceResolver{resolver})
			if valid != tc.valid || (valid && !reflect.DeepEqual(validated, []sciencecapability.ExecutionEvidenceResolver{resolver})) || (!valid && len(validated) != 0) {
				t.Errorf("resolver authority valid=%v want=%v validated=%#v", valid, tc.valid, validated)
			}
			expected := []string(nil)
			if tc.expected != "" {
				expected = []string{tc.expected}
			}
			if got := run.selectedImplementationsSnapshot(); len(got) != len(expected) || (len(got) > 0 && !reflect.DeepEqual(got, expected)) {
				t.Errorf("primary restored=%v want=%v", got, expected)
			}
			if !reflect.DeepEqual(run.requiredScientificCapabilitiesSnapshot(), []string{"primary-analysis", "auxiliary-analysis"}) || len(run.selectedEvidenceResolversSnapshot()) != 0 {
				t.Fatal("validation removed task requirements or independently authorized a resolver")
			}
		})
	}
}

package server

import (
	"context"
	"reflect"
	"strings"
	"testing"

	eventjournal "synon-go/internal/persistence/journal"
	"synon-go/internal/sciencecapability"
	"synon-go/internal/skills"
)

func TestManagedEnvironmentScientificImplementationFollowsExactUserChoice(t *testing.T) {
	run := &sessionRunnerChatRun{
		TaskIntent:                     "Generate candidates with a professional pocket-conditioned engine.",
		RequiredScientificCapabilities: []string{"pocket-conditioned-molecule-generation", "gpu"},
	}
	ctx := withTranscriptRunnerChatRun(context.Background(), run)

	decision, required := managedEnvironmentImplementationDecision(ctx, "", false)
	if !required || stringValue(decision["status"]) != "implementation_identity_required" {
		t.Fatalf("missing implementation decision=%#v required=%t", decision, required)
	}
	if decision, required = managedEnvironmentImplementationDecision(ctx, "DiffSBDD", false); required || decision != nil {
		t.Fatalf("exact preflight identity was blocked: decision=%#v required=%t", decision, required)
	}
	decision, required = managedEnvironmentImplementationDecision(ctx, "DiffSBDD", true)
	if !required || stringValue(decision["status"]) != "implementation_selection_required" {
		t.Fatalf("first setup without answer decision=%#v required=%t", decision, required)
	}

	run.setSelectedImplementations("PocketXMol")
	decision, required = managedEnvironmentImplementationDecision(ctx, "DiffSBDD", true)
	if !required || stringValue(decision["status"]) != "selected_implementation_mismatch" {
		t.Fatalf("mismatched selection decision=%#v required=%t", decision, required)
	}
	if decision, required = managedEnvironmentImplementationDecision(ctx, "PocketXMol", true); required || decision != nil {
		t.Fatalf("exact selected implementation was blocked: decision=%#v required=%t", decision, required)
	}
}

func TestSelectedEvidenceResolverUsesAuxiliaryPackWithoutReplacingPrimaryImplementation(t *testing.T) {
	skillCatalog := skills.NewCatalog()
	skillCatalog.AddSkill(skills.Skill{Name: "docking-skill", ImplementationIdentities: []string{"AutoDock Vina"}})
	skillCatalog.AddSkill(skills.Skill{Name: "pocket-skill", ImplementationIdentities: []string{"P2Rank"}})
	capabilityCatalog := &sciencecapability.Catalog{Capabilities: []sciencecapability.Definition{
		{ID: "molecular-docking", AcceptedEngines: []sciencecapability.EngineDefinition{{ExecutionPack: sciencecapability.ExecutionPack{
			ID: "molecular-docking.vina", Mode: "local", Skill: "docking-skill",
			EvidenceResolvers: []sciencecapability.ExecutionEvidenceResolver{{EvidenceGroup: "binding-site-center", Skill: "pocket-skill", Implementation: "P2Rank"}},
		}}}},
		{ID: "binding-pocket-prediction", AcceptedEngines: []sciencecapability.EngineDefinition{{ExecutionPack: sciencecapability.ExecutionPack{
			ID: "binding-pocket-prediction.p2rank", Mode: "local", Skill: "pocket-skill",
			Packages: []sciencecapability.ExecutionPackage{{Manager: "conda", Spec: "openjdk=17"}},
		}}}},
	}}
	run := &sessionRunnerChatRun{RequiredScientificCapabilities: []string{"molecular-docking"}, SelectedImplementations: []string{"AutoDock Vina"}}
	resolver := sciencecapability.ExecutionEvidenceResolver{EvidenceGroup: "binding-site-center", Skill: "pocket-skill", Implementation: "P2Rank"}
	validated, ok := validatedSelectedAskUserEvidenceResolvers(skillCatalog, capabilityCatalog, run, []sciencecapability.ExecutionEvidenceResolver{resolver})
	if !ok || !reflect.DeepEqual(validated, []sciencecapability.ExecutionEvidenceResolver{resolver}) {
		t.Fatalf("validated=%#v ok=%t", validated, ok)
	}
	run.setSelectedEvidenceResolvers(validated...)
	server := &Server{skillCatalog: skillCatalog, scienceCapabilities: capabilityCatalog}
	ctx := withTranscriptRunnerChatRun(context.Background(), run)
	if decision, required := server.managedEnvironmentImplementationDecision(ctx, "P2Rank", true); required || decision != nil {
		t.Fatalf("resolver blocked: decision=%#v required=%t", decision, required)
	}
	pack, found := server.registeredManagedEnvironmentExecutionPack(ctx, "P2Rank")
	if !found || pack.ID != "binding-pocket-prediction.p2rank" || !reflect.DeepEqual(run.selectedImplementationsSnapshot(), []string{"AutoDock Vina"}) {
		t.Fatalf("pack=%#v found=%t primary=%v", pack, found, run.selectedImplementationsSnapshot())
	}
}

func TestVersionQualifiedResolverImplementationUsesSelectedAuxiliaryAuthority(t *testing.T) {
	run := &sessionRunnerChatRun{SelectedImplementations: []string{"Primary Engine"}}
	run.setSelectedEvidenceResolvers(sciencecapability.ExecutionEvidenceResolver{
		EvidenceGroup: "control-point", Skill: "evidence-resolver", Implementation: "Resolver Engine",
	})
	ctx := withTranscriptRunnerChatRun(context.Background(), run)
	canonical := canonicalManagedEnvironmentSelectedImplementation(ctx, "Resolver Engine 2.5.1")
	if canonical != "Resolver Engine" {
		t.Fatalf("version-qualified resolver canonical identity=%q", canonical)
	}
	if result, blocked := managedEnvironmentImplementationDecision(ctx, canonical, true); blocked || result != nil {
		t.Fatalf("selected resolver create was rejected: blocked=%t result=%#v", blocked, result)
	}
	if got := run.selectedImplementationsSnapshot(); !reflect.DeepEqual(got, []string{"Primary Engine"}) {
		t.Fatalf("auxiliary resolver replaced primary implementation: %v", got)
	}
}

func TestManagedEnvironmentExplicitUserImplementationDoesNotRequireAnotherQuestion(t *testing.T) {
	run := &sessionRunnerChatRun{
		TaskIntent:                     "Use DiffSBDD for pocket-conditioned generation.",
		RequiredScientificCapabilities: []string{"pocket-conditioned-molecule-generation"},
	}
	decision, required := managedEnvironmentImplementationDecision(
		withTranscriptRunnerChatRun(context.Background(), run), "DiffSBDD", true,
	)
	if required || decision != nil {
		t.Fatalf("explicit implementation was blocked: decision=%#v required=%t", decision, required)
	}
}

func TestExplicitPrimaryImplementationKeepsItsRegisteredPackBesideNamedResolver(t *testing.T) {
	skillCatalog := skills.NewCatalog()
	skillCatalog.AddSkill(skills.Skill{
		Name: "docking-skill", ImplementationIdentities: []string{"AutoDock Vina"},
	})
	skillCatalog.AddSkill(skills.Skill{
		Name: "pocket-skill", ImplementationIdentities: []string{"P2Rank"},
	})
	dockingPack := sciencecapability.ExecutionPack{
		ID: "molecular-docking.vina", Mode: "local", Skill: "docking-skill",
		Provider: "local-conda", Language: "python",
		Packages: []sciencecapability.ExecutionPackage{
			{Manager: "conda", Spec: "vina=1.2.7"},
			{Manager: "conda", Spec: "gemmi=0.7.5"},
		},
		Imports: []string{"vina", "gemmi"},
		EvidenceResolvers: []sciencecapability.ExecutionEvidenceResolver{{
			EvidenceGroup: "binding-site-center", Skill: "pocket-skill", Implementation: "P2Rank",
		}},
	}
	capabilityCatalog := &sciencecapability.Catalog{Capabilities: []sciencecapability.Definition{
		{ID: "molecular-docking", AcceptedEngines: []sciencecapability.EngineDefinition{{
			ID: "autodock-vina", ExecutionPack: dockingPack,
		}}},
		{ID: "binding-pocket-prediction", AcceptedEngines: []sciencecapability.EngineDefinition{{
			ID: "p2rank", ExecutionPack: sciencecapability.ExecutionPack{
				ID: "binding-pocket-prediction.p2rank", Mode: "local", Skill: "pocket-skill",
				Packages: []sciencecapability.ExecutionPackage{{Manager: "conda", Spec: "openjdk=17"}},
			},
		}}},
	}}
	server := &Server{skillCatalog: skillCatalog, scienceCapabilities: capabilityCatalog}
	run := &sessionRunnerChatRun{
		TaskIntent:                     "Run AutoDock Vina with P2Rank pocket prediction.",
		RequiredScientificCapabilities: []string{"molecular-docking", "binding-pocket-prediction"},
	}
	run.setSelectedEvidenceResolvers(sciencecapability.ExecutionEvidenceResolver{
		EvidenceGroup: "binding-site-center", Skill: "pocket-skill", Implementation: "P2Rank",
	})
	pack, found := server.registeredManagedEnvironmentExecutionPack(
		withTranscriptRunnerChatRun(context.Background(), run), "AutoDock Vina",
	)
	if !found || pack.ID != dockingPack.ID || len(run.selectedImplementationsSnapshot()) != 0 {
		t.Fatalf("explicit primary pack=%#v found=%t selected=%v", pack, found, run.selectedImplementationsSnapshot())
	}
}

func TestSelectedImplementationIdentityRepairsDescriptiveDecoration(t *testing.T) {
	run := &sessionRunnerChatRun{}
	run.setSelectedImplementations("DiffLinker")
	ctx := withTranscriptRunnerChatRun(context.Background(), run)
	if got := canonicalManagedEnvironmentSelectedImplementation(
		ctx, "DiffLinker: Pocket-conditioned molecule generation",
	); got != "DiffLinker" {
		t.Fatalf("canonical implementation=%q", got)
	}
	if got := canonicalManagedEnvironmentSelectedImplementation(ctx, "Pocket2Mol"); got != "Pocket2Mol" {
		t.Fatalf("unselected implementation was rewritten=%q", got)
	}
	if implementationIdentityContained("DiffLinker2", "DiffLinker") {
		t.Fatal("partial implementation token was accepted")
	}
}

func TestSelectedImplementationAuthorityContextUsesLatestExactState(t *testing.T) {
	run := &sessionRunnerChatRun{SelectedImplementations: []string{"Engine A"}}
	context := selectedImplementationAuthorityContext(run)
	if !strings.Contains(context, `["Engine A"]`) ||
		!strings.Contains(context, "Tool admission enforces it") {
		t.Fatalf("implementation authority context=%q", context)
	}
}

func TestUniqueRegisteredLocalImplementationSkipsUnavailableAlternativesWithoutLooping(t *testing.T) {
	skillCatalog := skills.NewCatalog()
	skillCatalog.AddSkill(skills.Skill{
		Name: "local-engine", ImplementationIdentities: []string{"Local Engine"},
		RequiredEnvironmentPackages: []string{"engine-runtime", "structure-parser"},
	})
	skillCatalog.AddSkill(skills.Skill{Name: "unavailable-engine", ImplementationIdentities: []string{"Unavailable Engine_STA"}})
	capabilityCatalog := &sciencecapability.Catalog{Capabilities: []sciencecapability.Definition{{
		ID: "molecular-analysis",
		AcceptedEngines: []sciencecapability.EngineDefinition{
			{ID: "local", ExecutionPack: sciencecapability.ExecutionPack{Mode: "local", Skill: "local-engine"}},
			{ID: "remote", ExecutionPack: sciencecapability.ExecutionPack{Mode: "unavailable", Skill: "unavailable-engine"}},
		},
	}}}
	server := &Server{skillCatalog: skillCatalog, scienceCapabilities: capabilityCatalog}
	run := &sessionRunnerChatRun{RequiredScientificCapabilities: []string{"molecular-analysis"}}
	ctx := withTranscriptRunnerChatRun(context.Background(), run)
	decision, required := server.managedEnvironmentImplementationDecision(ctx, "Local Engine", true)
	if required || decision != nil || !reflect.DeepEqual(run.selectedImplementationsSnapshot(), []string{"Local Engine"}) {
		t.Fatalf("registry singleton decision=%#v required=%t selected=%v", decision, required, run.selectedImplementationsSnapshot())
	}
	result := server.bindRegistrySelectedImplementationReceipt(ctx, map[string]any{"ok": true, "status": "completed"})
	receipt := mapValue(mapValue(result)["implementation_selection"])
	if receipt["provenance"] != "registry-unique-local-pack" {
		t.Fatalf("registry selection receipt=%#v", receipt)
	}
	entries := []eventjournal.Entry{{EventID: 7, Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
		"toolResult": result,
	}}}
	if got := selectedAskUserImplementationsFromRunnerEntries(entries); !reflect.DeepEqual(got, []string{"Local Engine"}) {
		t.Fatalf("durable registry selection=%v", got)
	}
	if got := resolvedAskUserEvidenceFromRunnerEntries(entries); len(got) != 0 {
		t.Fatalf("registry selection became explicit user evidence: %#v", got)
	}

	capabilityCatalog.Capabilities[0].AcceptedEngines = append(
		capabilityCatalog.Capabilities[0].AcceptedEngines,
		sciencecapability.EngineDefinition{ID: "second", ExecutionPack: sciencecapability.ExecutionPack{Mode: "local", Skill: "unavailable-engine"}},
	)
	secondRun := &sessionRunnerChatRun{RequiredScientificCapabilities: []string{"molecular-analysis"}}
	decision, required = server.managedEnvironmentImplementationDecision(
		withTranscriptRunnerChatRun(context.Background(), secondRun), "Local Engine", true,
	)
	if !required || stringValue(decision["status"]) != "implementation_selection_required" || len(secondRun.selectedImplementationsSnapshot()) != 0 {
		t.Fatalf("multiple local implementations bypassed user choice: %#v selected=%v", decision, secondRun.selectedImplementationsSnapshot())
	}
}

func TestUniqueRegisteredLocalImplementationCanonicalizesFirstEnvironmentPreflight(t *testing.T) {
	skillCatalog := skills.NewCatalog()
	skillCatalog.AddSkill(skills.Skill{
		Name: "local-engine", ImplementationIdentities: []string{"Local Engine"},
		RequiredEnvironmentPackages: []string{"engine-runtime", "structure-parser"},
	})
	capabilityCatalog := &sciencecapability.Catalog{Capabilities: []sciencecapability.Definition{{
		ID: "molecular-analysis",
		AcceptedEngines: []sciencecapability.EngineDefinition{{
			ID: "local", ExecutionPack: sciencecapability.ExecutionPack{
				ID: "molecular-analysis.local", Mode: "local", Skill: "local-engine",
				Provider: "local-conda", Language: "python",
				Packages: []sciencecapability.ExecutionPackage{
					{Manager: "conda", Spec: "vina=1.2.7"},
					{Manager: "conda", Spec: "meeko=0.8.0"},
					{Manager: "conda", Spec: "rdkit=2026.03.1"},
					{Manager: "conda", Spec: "gemmi=0.7.5"},
					{Manager: "conda", Spec: "prody=2.6.1"},
					{Manager: "conda", Spec: "biopython=1.88"},
					{Manager: "conda", Spec: "openbabel=3.2.1"},
				},
				Channels: []string{"conda-forge"},
				Imports:  []string{"vina", "meeko", "rdkit", "gemmi", "prody", "Bio", "openbabel"},
			},
		}},
	}}}
	server := &Server{skillCatalog: skillCatalog, scienceCapabilities: capabilityCatalog}
	run := &sessionRunnerChatRun{RequiredScientificCapabilities: []string{"molecular-analysis"}}
	ctx := withTranscriptRunnerChatRun(context.Background(), run)
	if got := server.canonicalManagedEnvironmentImplementation(ctx, ""); got != "Local Engine" ||
		!reflect.DeepEqual(run.selectedImplementationsSnapshot(), []string{"Local Engine"}) {
		t.Fatalf("canonical implementation=%q selected=%v", got, run.selectedImplementationsSnapshot())
	}
	pack, bound := server.registeredManagedEnvironmentExecutionPack(ctx, "Local Engine")
	if !bound {
		t.Fatal("registered execution pack was not bound")
	}
	request := manageEnvironmentsInput{
		Provider: "local-conda", Language: "python", PythonVersion: "3.11",
		Packages: []string{"autodock-vina", "https://example.invalid/guessed.whl"}, Channels: []string{"defaults"},
		PipPhases: [][]string{{"guessed-helper"}}, PipFindLinks: []string{"https://example.invalid/wheels"},
		PipExtraIndexURLs: []string{"https://example.invalid/simple"}, ImportNames: []string{"guessed_import"},
	}
	if err := applyRegisteredExecutionPackEnvironmentContract(&request, pack); err != nil {
		t.Fatal(err)
	}
	wantPackages := []string{
		"vina=1.2.7", "meeko=0.8.0", "rdkit=2026.03.1", "gemmi=0.7.5",
		"prody=2.6.1", "biopython=1.88", "openbabel=3.2.1",
	}
	if !reflect.DeepEqual(request.Packages, wantPackages) || len(request.PipPhases) != 0 ||
		!reflect.DeepEqual(request.Channels, []string{"conda-forge"}) ||
		!reflect.DeepEqual(request.ImportNames, pack.Imports) || len(request.PipFindLinks) != 0 ||
		len(request.PipExtraIndexURLs) != 0 || request.PythonVersion != "" {
		t.Fatalf("materialized request=%#v", request)
	}
	containerRequest := manageEnvironmentsInput{
		Provider: "local-container", Implementation: "Local Engine", Image: "example.invalid/engine:latest",
	}
	contractErr := applyRegisteredExecutionPackEnvironmentContract(&containerRequest, pack)
	if contractErr == nil {
		t.Fatal("container provider bypassed the registered local-conda execution pack")
	}
	contractResult := registeredExecutionPackEnvironmentContractResult(containerRequest, pack, contractErr)
	if contractResult["status"] != "execution_pack_environment_contract_mismatch" ||
		contractResult["required_provider"] != "local-conda" {
		t.Fatalf("container provider mismatch err=%v result=%#v", contractErr, contractResult)
	}
	if _, bound := server.registeredManagedEnvironmentExecutionPack(ctx, "Different Engine"); bound {
		t.Fatal("unrelated implementation received the selected execution pack")
	}
	result := server.bindRegistrySelectedImplementationReceipt(ctx, map[string]any{"ok": true})
	receipt := mapValue(mapValue(result)["implementation_selection"])
	if receipt["provenance"] != "registry-unique-local-pack" {
		t.Fatalf("first preflight selection receipt=%#v", receipt)
	}
}

func TestUniqueRegisteredLocalImplementationSuppressesRedundantAskUserChoice(t *testing.T) {
	skillCatalog := skills.NewCatalog()
	skillCatalog.AddSkill(skills.Skill{Name: "local-engine", ImplementationIdentities: []string{"Local Engine"}})
	capabilityCatalog := &sciencecapability.Catalog{Capabilities: []sciencecapability.Definition{{
		ID: "molecular-analysis",
		AcceptedEngines: []sciencecapability.EngineDefinition{
			{ID: "local", ExecutionPack: sciencecapability.ExecutionPack{Mode: "local", Skill: "local-engine"}},
			{ID: "unavailable", ExecutionPack: sciencecapability.ExecutionPack{Mode: "unavailable", Skill: "other-engine"}},
		},
	}}}
	server := &Server{skillCatalog: skillCatalog, scienceCapabilities: capabilityCatalog}
	run := &sessionRunnerChatRun{RequiredScientificCapabilities: []string{"molecular-analysis"}}
	ctx := withTranscriptRunnerChatRun(context.Background(), run)
	question := map[string]any{"questions": []askUserQuestion{{Options: []askUserQuestionOption{
		{Label: "Reuse local", Metadata: map[string]any{"implementation": "Local Engine"}},
		{Label: "Install local", Metadata: map[string]any{"implementation": "Local Engine"}},
		{Label: "Unavailable", Metadata: map[string]any{"implementation": "Other Engine"}},
	}}}}
	resolution, resolved := server.resolveUniqueRegisteredImplementationAskUser(ctx, run, question)
	if !resolved || resolution["status"] != "implementation_selection_resolved" || resolution["decision_required"] != false ||
		!reflect.DeepEqual(run.selectedImplementationsSnapshot(), []string{"Local Engine"}) {
		t.Fatalf("resolution=%#v resolved=%t selected=%v", resolution, resolved, run.selectedImplementationsSnapshot())
	}
	receipt := mapValue(resolution["implementation_selection"])
	if receipt["provenance"] != "registry-unique-local-pack" {
		t.Fatalf("AskUser singleton receipt=%#v", receipt)
	}
}

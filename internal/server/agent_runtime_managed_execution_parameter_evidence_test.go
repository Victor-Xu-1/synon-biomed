package server

import (
	"os"
	"path/filepath"
	"testing"

	"synon-go/internal/sciencecapability"
	"synon-go/internal/skills"
)

func TestManagedExecutionParameterAcceptsOnlySelectedEvidenceResolver(t *testing.T) {
	pack := sciencecapability.ExecutionPack{
		ID: "example-capability.resolver", Skill: "evidence-resolver",
		Parameters: []sciencecapability.ExecutionParameter{{
			Name: "method", Argument: "--method", Type: "string",
			Evidence: "selected-evidence-resolver",
		}},
	}
	selected := []sciencecapability.ExecutionEvidenceResolver{{
		EvidenceGroup: "control-point", Skill: "evidence-resolver", Implementation: "ResolverEngine",
	}}
	if blocked := managedExecutionPackParameterEvidencePreflight(
		pack, "python resolver.py --method ResolverEngine-2.5.1", nil, "en", selected,
	); blocked != nil {
		t.Fatalf("selected resolver implementation was rejected: %#v", blocked)
	}
	multiline := `python "/task/.synon/runtime/skills/evidence-resolver/scripts/resolver.py" \
  --input source.pdb \
  --method ResolverEngine \
  --profile auto`
	if got := managedExecutionArgumentValues(multiline)["--method"]; got != "ResolverEngine" {
		t.Fatalf("multiline resolver method parsed as %q", got)
	}
	if blocked := managedExecutionPackParameterEvidencePreflight(pack, multiline, nil, "en", selected); blocked != nil {
		t.Fatalf("multiline selected resolver implementation was rejected: %#v", blocked)
	}
	for name, command := range map[string]string{
		"missing":   "python resolver.py",
		"different": "python resolver.py --method DifferentEngine",
	} {
		t.Run(name, func(t *testing.T) {
			blocked := managedExecutionPackParameterEvidencePreflight(pack, command, nil, "en", selected)
			if blocked == nil || blocked["status"] != "execution_selected_resolver_parameter_required" {
				t.Fatalf("unselected resolver parameter was accepted: %#v", blocked)
			}
		})
	}
}

func TestManagedExecutionParameterRequiresSelectedResolverHandoff(t *testing.T) {
	pack := sciencecapability.ExecutionPack{
		ID: "example-capability.primary", Skill: "primary-skill",
		Parameters: []sciencecapability.ExecutionParameter{
			{Name: "point_x", Argument: "--point-x", Type: "number", Evidence: "resolved-user-input", EvidenceGroup: "control-point", EvidenceTerms: []string{"control point"}},
			{Name: "point_y", Argument: "--point-y", Type: "number", Evidence: "resolved-user-input", EvidenceGroup: "control-point"},
			{Name: "point_z", Argument: "--point-z", Type: "number", Evidence: "resolved-user-input", EvidenceGroup: "control-point"},
		},
	}
	selected := []sciencecapability.ExecutionEvidenceResolver{{
		EvidenceGroup: "control-point", Skill: "resolver-skill", Implementation: "Resolver Engine",
	}}
	blocked := managedExecutionPackParameterEvidencePreflight(
		pack,
		"python primary.py --point-x 25.4 --point-y 18.2 --point-z 30.6",
		nil,
		"en",
		selected,
	)
	if stringValue(blocked["status"]) != "execution_selected_resolver_handoff_required" ||
		boolValue(blocked["decision_required"], true) || stringValue(blocked["evidence_group"]) != "control-point" {
		t.Fatalf("selected resolver values were not routed back to their handoff: %#v", blocked)
	}
	if resolver, _ := blocked["selected_resolver"].(sciencecapability.ExecutionEvidenceResolver); resolver != selected[0] {
		t.Fatalf("selected resolver identity was not preserved: %#v", blocked)
	}
	if allowed := managedExecutionPackParameterEvidencePreflight(
		pack,
		"python primary.py --point-x 25.4 --point-y 18.2 --point-z 30.6",
		[]managedExecutionUserEvidence{{
			Source: "task", Question: "What control point should be used?", Text: "Use control point 25.4 / 18.2 / 30.6.",
		}},
		"en",
		selected,
	); allowed != nil {
		t.Fatalf("explicit current-task user values did not override the resolver route: %#v", allowed)
	}
	if allowed := managedExecutionPackParameterEvidencePreflight(
		pack,
		"python primary.py --resolver-output resolver-result.json",
		nil,
		"en",
		selected,
	); allowed != nil {
		t.Fatalf("a parent command without copied controlled values was blocked: %#v", allowed)
	}
	withoutResolver := managedExecutionPackParameterEvidencePreflight(
		pack,
		"python primary.py --point-x 25.4 --point-y 18.2 --point-z 30.6",
		nil,
		"en",
		nil,
	)
	if stringValue(withoutResolver["status"]) != "execution_parameter_evidence_required" {
		t.Fatalf("ordinary user-evidence enforcement changed without a selected resolver: %#v", withoutResolver)
	}
}

func TestExplicitTaskEvidenceBindsUniqueRegisteredResolver(t *testing.T) {
	workspace := t.TempDir()
	script := filepath.Join(workspace, ".synon", "runtime", "skills", "pocket-skill-abcd", "scripts", "run.py")
	if err := os.MkdirAll(filepath.Dir(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("print('managed')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := &sessionRunnerChatRun{
		TaskIntent:         "Use AutoDock Vina with P2Rank for the requested docking run.",
		ExecutedSkillNames: []string{"pocket-skill"},
	}
	skillCatalog := skills.NewCatalog()
	skillCatalog.AddSkill(skills.Skill{Name: "docking-skill", ImplementationIdentities: []string{"AutoDock Vina"}})
	skillCatalog.AddSkill(skills.Skill{Name: "pocket-skill", ImplementationIdentities: []string{"P2Rank"}})
	resolver := sciencecapability.ExecutionEvidenceResolver{
		EvidenceGroup: "binding-site-center", Skill: "pocket-skill", Implementation: "P2Rank",
	}
	scienceCatalog := &sciencecapability.Catalog{Capabilities: []sciencecapability.Definition{
		{ID: "molecular-docking", AcceptedEngines: []sciencecapability.EngineDefinition{{ExecutionPack: sciencecapability.ExecutionPack{
			ID: "molecular-docking.vina", Mode: "local", Skill: "docking-skill",
			EvidenceResolvers: []sciencecapability.ExecutionEvidenceResolver{resolver},
		}}}},
		{ID: "binding-pocket-prediction", AcceptedEngines: []sciencecapability.EngineDefinition{{ExecutionPack: sciencecapability.ExecutionPack{
			ID: "binding-pocket-prediction.p2rank", Mode: "local", Skill: "pocket-skill",
			Executable: "python", Script: "executionpacks/run.py",
			Parameters: []sciencecapability.ExecutionParameter{{
				Name: "method", Argument: "--method", Type: "string", Evidence: "selected-evidence-resolver",
			}},
		}}}},
	}}
	gateway := serverAgentRuntimeToolGateway{
		server: &Server{skillCatalog: skillCatalog, scienceCapabilities: scienceCatalog}, taskRun: run,
		kernel: &agentKernelContext{workspaceDir: workspace},
	}
	input := gateway.normalizeManagedExecutionRuntimeArguments("bash", map[string]any{
		"command": `python "` + script + `"`,
	})
	if got := managedExecutionArgumentValues(stringValue(input["command"]))["--method"]; got != "P2Rank" {
		t.Fatalf("explicit task resolver was not normalized into the pack command: %#v", input)
	}
	if blocked := gateway.agentRuntimeManagedExecutionPackPreflight("bash", input); blocked != nil {
		t.Fatalf("explicit task resolver remained blocked: %#v", blocked)
	}
	if got := run.selectedImplementationsSnapshot(); len(got) != 1 || got[0] != "AutoDock Vina" {
		t.Fatalf("explicit parent implementation was not bound: %v", got)
	}
	if got := run.selectedEvidenceResolversSnapshot(); len(got) != 1 || got[0] != resolver {
		t.Fatalf("explicit evidence resolver was not bound: %#v", got)
	}
	unscopedRun := &sessionRunnerChatRun{
		TaskIntent: "Use P2Rank for a standalone pocket prediction.", ExecutedSkillNames: []string{"pocket-skill"},
	}
	unscopedGateway := serverAgentRuntimeToolGateway{
		server: gateway.server, taskRun: unscopedRun, kernel: gateway.kernel,
	}
	unscoped := unscopedGateway.normalizeManagedExecutionRuntimeArguments("bash", map[string]any{
		"command": `python "` + script + `"`,
	})
	if got := managedExecutionArgumentValues(stringValue(unscoped["command"]))["--method"]; got != "P2Rank" {
		t.Fatalf("explicit standalone implementation did not reach its own pack: %#v", unscoped)
	}
	if blocked := unscopedGateway.agentRuntimeManagedExecutionPackPreflight("bash", unscoped); blocked != nil {
		t.Fatalf("standalone selected pack remained blocked: %#v", blocked)
	}
	if got := unscopedRun.selectedImplementationsSnapshot(); len(got) != 0 {
		t.Fatalf("a standalone resolver mention selected an unrequested parent: %v", got)
	}
	selectedRun := &sessionRunnerChatRun{
		TaskIntent: "Predict binding pockets with the selected method.",
		ExecutedSkillNames: []string{"pocket-skill"},
		SelectedImplementations: []string{"P2Rank"},
	}
	selectedGateway := serverAgentRuntimeToolGateway{server: gateway.server, taskRun: selectedRun, kernel: gateway.kernel}
	selectedInput := selectedGateway.normalizeManagedExecutionRuntimeArguments("bash", map[string]any{
		"command": `python "` + script + `"`,
	})
	if got := managedExecutionArgumentValues(stringValue(selectedInput["command"]))["--method"]; got != "P2Rank" {
		t.Fatalf("current-task primary selection did not reach its pack: %#v", selectedInput)
	}
	if blocked := selectedGateway.agentRuntimeManagedExecutionPackPreflight("bash", selectedInput); blocked != nil {
		t.Fatalf("selected primary pack remained blocked: %#v", blocked)
	}
	noSelection := &sessionRunnerChatRun{TaskIntent: "Predict binding pockets.", ExecutedSkillNames: []string{"pocket-skill"}}
	noSelectionGateway := serverAgentRuntimeToolGateway{server: gateway.server, taskRun: noSelection, kernel: gateway.kernel}
	noSelectionInput := noSelectionGateway.normalizeManagedExecutionRuntimeArguments("bash", map[string]any{
		"command": `python "` + script + `"`,
	})
	if got := managedExecutionArgumentValues(stringValue(noSelectionInput["command"]))["--method"]; got != "" {
		t.Fatalf("skill presence invented a method selection: %#v", noSelectionInput)
	}
	wrong := map[string]any{"command": `python "` + script + `" --method DifferentEngine`}
	if blocked := unscopedGateway.agentRuntimeManagedExecutionPackPreflight("bash", wrong); blocked == nil ||
		blocked["status"] != "execution_selected_resolver_parameter_required" {
		t.Fatalf("unselected standalone method passed: %#v", blocked)
	}
}

func TestShippedExplicitResolverEntrypointAcceptsItsRegisteredMethod(t *testing.T) {
	capabilities, err := sciencecapability.Load("../sciencecapability/scientific-capabilities.v2.json")
	if err != nil {
		t.Fatal(err)
	}
	skillCatalog := skills.Load([]string{"../../skills/synonbiomed"})
	if failures := skillCatalog.LoadErrors(); len(failures) != 0 {
		t.Fatalf("load shipped Skills: %#v", failures)
	}
	workspace := t.TempDir()
	script := filepath.Join(
		workspace, ".synon", "runtime", "skills", "p2rank-pocket-detection-fixture", "scripts", "p2rank_binding_pockets.py",
	)
	if err := os.MkdirAll(filepath.Dir(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("print('managed')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := &sessionRunnerChatRun{
		TaskIntent:              "实际运行 P2Rank 和 AutoDock Vina 完成 docking。",
		ExecutedSkillNames:      []string{"p2rank-pocket-detection"},
		SelectedImplementations: []string{"P2Rank"},
	}
	gateway := serverAgentRuntimeToolGateway{
		server:  &Server{skillCatalog: skillCatalog, scienceCapabilities: &capabilities},
		taskRun: run,
		kernel:  &agentKernelContext{workspaceDir: workspace},
	}
	input := gateway.normalizeManagedExecutionRuntimeArguments("bash", map[string]any{
		"command": "python \"" + script + "\" \\\n+  --structure irf5.pdb \\\n+  --p2rank-archive p2rank_2.5.1.tar.gz \\\n+  --method P2Rank \\\n+  --profile auto --top-k 5 --threads 4 --minimum-box-size 20 --box-padding 6",
	})
	if selected := run.selectedEvidenceResolversSnapshot(); len(selected) != 1 ||
		selected[0].Skill != "p2rank-pocket-detection" || selected[0].Implementation != "P2Rank" {
		t.Fatalf("shipped explicit resolver was not selected: %#v", selected)
	}
	if selected := run.selectedImplementationsSnapshot(); len(selected) != 1 || selected[0] != "AutoDock Vina" {
		t.Fatalf("resolver environment selection was not restored to its explicit parent: %#v", selected)
	}
	if blocked := gateway.agentRuntimeManagedExecutionPackPreflight("bash", input); blocked != nil {
		t.Fatalf("shipped explicit resolver entrypoint was rejected: input=%#v blocked=%#v", input, blocked)
	}
}

package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	"synon-go/internal/skills"
)

func TestSelectedImplementationSkillPreflightBlocksDifferentDedicatedEngine(t *testing.T) {
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{
		Name: "engine-b-skill", ImplementationIdentities: []string{"Engine B"},
	})
	gateway := serverAgentRuntimeToolGateway{
		server:  &Server{skillCatalog: catalog},
		taskRun: &sessionRunnerChatRun{SelectedImplementations: []string{"Engine A"}},
	}
	result := gateway.agentRuntimeSelectedImplementationSkillPreflight("skill", map[string]any{"skill": "engine-b-skill"})
	if stringValue(result["status"]) != "selected_implementation_skill_mismatch" ||
		boolValue(result["executed"], true) {
		t.Fatalf("different dedicated implementation Skill was not blocked: %#v", result)
	}
}

func TestSelectedImplementationFiltersHistoricalDedicatedSkillContext(t *testing.T) {
	candidates := []skills.Skill{
		{Name: "engine-a", ImplementationIdentities: []string{"Engine A"}},
		{Name: "engine-b", ImplementationIdentities: []string{"Engine B"}},
		{Name: "generic-recovery"},
	}
	filtered := runtimeSkillsForSelectedImplementation(candidates, []string{"Engine A"}, "")
	if len(filtered) != 2 || filtered[0].Name != "engine-a" || filtered[1].Name != "generic-recovery" {
		t.Fatalf("filtered skills=%#v", filtered)
	}
}

func TestSelectedImplementationBlocksStaleMaterializedSkillAsset(t *testing.T) {
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{Name: "engine-b-skill", ImplementationIdentities: []string{"Engine B"}})
	gateway := serverAgentRuntimeToolGateway{
		server:  &Server{skillCatalog: catalog},
		taskRun: &sessionRunnerChatRun{SelectedImplementations: []string{"Engine A"}},
	}
	script := filepath.ToSlash(filepath.Join(
		"/tmp/work/.synon/runtime/skills", agentSkillRuntimeDirectoryName("engine-b-skill"),
		"0123456789abcdef", "scripts", "run.py",
	))
	result := gateway.agentRuntimeSelectedImplementationMaterializedSkillPreflight("bash", map[string]any{
		"environment": "analysis", "command": "python " + script + " --help",
	})
	if stringValue(result["status"]) != "selected_implementation_skill_mismatch" ||
		boolValue(result["executed"], true) {
		t.Fatalf("stale materialized Skill asset was not blocked: %#v", result)
	}
}

func TestSelectedImplementationDeactivatesHistoricalProviderSkillReceipt(t *testing.T) {
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{Name: "engine-b-skill", ImplementationIdentities: []string{"Engine B"}})
	messages := []chatCompletionMessage{{
		Role: "tool", ToolCallID: "skill-call",
		Content: "<skill-metadata replay=\"legacy\" name=\"engine-b-skill\" />\n\nOld execution contract",
	}}
	filtered := deactivateProviderSkillResultsForSelectedImplementation(messages, catalog, []string{"Engine A"}, "")
	if !strings.Contains(filtered[0].Content, "historical dedicated capability is inactive") ||
		strings.Contains(filtered[0].Content, "Old execution contract") {
		t.Fatalf("provider receipt was not deactivated: %q", filtered[0].Content)
	}
}

func TestUnselectedHistoricalDedicatedSkillIsInactiveUntilChoice(t *testing.T) {
	candidates := []skills.Skill{
		{Name: "engine-a", ImplementationIdentities: []string{"Engine A"}},
		{Name: "generic-recovery"},
	}
	filtered := runtimeSkillsForSelectedImplementation(candidates, nil, "Design molecules from a binding pocket")
	if len(filtered) != 1 || filtered[0].Name != "generic-recovery" {
		t.Fatalf("unselected dedicated Skill remained active: %#v", filtered)
	}
	filtered = runtimeSkillsForSelectedImplementation(candidates, nil, "Use Engine A for this task")
	if len(filtered) != 2 {
		t.Fatalf("explicitly named dedicated Skill was removed: %#v", filtered)
	}
}

func TestSelectedImplementationSkillPreflightAllowsMatchingAndGenericSkills(t *testing.T) {
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{
		Name: "engine-a-skill", ImplementationIdentities: []string{"Engine A"},
	})
	catalog.AddSkill(skills.Skill{Name: "generic-recovery"})
	gateway := serverAgentRuntimeToolGateway{
		server:  &Server{skillCatalog: catalog},
		taskRun: &sessionRunnerChatRun{SelectedImplementations: []string{"Engine A"}},
	}
	for _, name := range []string{"engine-a-skill", "generic-recovery"} {
		if result := gateway.agentRuntimeSelectedImplementationSkillPreflight("skill", map[string]any{"skill": name}); result != nil {
			t.Fatalf("allowed Skill %s was blocked: %#v", name, result)
		}
	}
}

func TestDedicatedImplementationSkillCanLoadForReadOnlyChoicePreparation(t *testing.T) {
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{
		Name: "engine-a-skill", ImplementationIdentities: []string{"Engine A"},
		RequiredCapabilities: []string{"pocket-conditioned-molecule-generation"},
	})
	run := &sessionRunnerChatRun{TaskIntent: "Design molecules from a binding pocket."}
	run.addRequiredScientificCapabilities("pocket-conditioned-molecule-generation")
	gateway := serverAgentRuntimeToolGateway{server: &Server{skillCatalog: catalog}, taskRun: run}
	if result := gateway.agentRuntimeSelectedImplementationSkillPreflight("skill", map[string]any{"skill": "engine-a-skill"}); result != nil {
		t.Fatalf("read-only dedicated Skill inspection was blocked: %#v", result)
	}
	if !run.implementationSelectionRequiredSnapshot() {
		t.Fatal("dedicated Skill inspection did not retain the implementation-decision requirement")
	}
	run.addExecutedSkillNames("engine-a-skill")
	blocked := gateway.agentRuntimeImplementationExecutionChoicePreflight("python", map[string]any{"code": "run_engine_a()"})
	if stringValue(blocked["status"]) != "implementation_selection_required" ||
		!boolValue(blocked["decision_required"], false) || boolValue(blocked["executed"], true) {
		t.Fatalf("read-only Skill load incorrectly authorized execution: %#v", blocked)
	}

	run.TaskIntent = "Use Engine A to design molecules from the binding pocket."
	if result := gateway.agentRuntimeSelectedImplementationSkillPreflight("skill", map[string]any{"skill": "engine-a-skill"}); result != nil {
		t.Fatalf("explicitly named implementation was blocked: %#v", result)
	}
}

func TestSelectedImplementationLoadsDedicatedSkillBeforeProvisioning(t *testing.T) {
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{
		Name: "engine-a-skill", ImplementationIdentities: []string{"Engine A"},
	})
	run := &sessionRunnerChatRun{SelectedImplementations: []string{"Engine A"}}
	gateway := serverAgentRuntimeToolGateway{server: &Server{skillCatalog: catalog}, taskRun: run}
	input := map[string]any{
		"mode": "create", "implementation": "Engine A", "name": "engine-a-runtime",
	}
	result := gateway.agentRuntimeImplementationProvisioningSkillPreflight("manage_environments", input)
	if stringValue(result["status"]) != "implementation_skill_required" ||
		stringValue(result["required_skill"]) != "engine-a-skill" || boolValue(result["executed"], true) {
		t.Fatalf("dedicated Skill was not required before provisioning: %#v", result)
	}
	run.addExecutedSkillNames("engine-a-skill")
	if result := gateway.agentRuntimeImplementationProvisioningSkillPreflight("manage_environments", input); result != nil {
		t.Fatalf("loaded dedicated Skill did not authorize provisioning: %#v", result)
	}
}

func TestImplementationProvisioningSkillGateLeavesDiscoveryAndSupportPackagesOpen(t *testing.T) {
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{
		Name: "engine-a-skill", ImplementationIdentities: []string{"Engine A"},
	})
	gateway := serverAgentRuntimeToolGateway{
		server:  &Server{skillCatalog: catalog},
		taskRun: &sessionRunnerChatRun{SelectedImplementations: []string{"Engine A"}},
	}
	for _, testCase := range []struct {
		tool  string
		input map[string]any
	}{
		{tool: "manage_environments", input: map[string]any{"mode": "preflight", "implementation": "Engine A"}},
		{tool: "manage_packages", input: map[string]any{"mode": "install", "packages": []any{"gemmi"}}},
		{tool: "manage_packages", input: map[string]any{"mode": "preflight", "implementation": "Engine A"}},
	} {
		if result := gateway.agentRuntimeImplementationProvisioningSkillPreflight(testCase.tool, testCase.input); result != nil {
			t.Fatalf("read-only/support operation was blocked for %s %#v: %#v", testCase.tool, testCase.input, result)
		}
	}
}

func TestSubstantialImplementationExecutionRequiresCurrentChoiceAndDedicatedSkill(t *testing.T) {
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{
		Name: "engine-a-skill", ImplementationIdentities: []string{"Engine A"},
		RequiredCapabilities: []string{"pocket-conditioned-molecule-generation"},
	})
	run := &sessionRunnerChatRun{TaskIntent: "Design molecules from a binding pocket."}
	run.addRequiredScientificCapabilities("pocket-conditioned-molecule-generation")
	gateway := serverAgentRuntimeToolGateway{server: &Server{skillCatalog: catalog}, taskRun: run}
	engineCall := map[string]any{
		"environment": "historical-runtime", "command": "python -m invented_engine --input pocket.json",
	}
	result := gateway.agentRuntimeImplementationExecutionChoicePreflight("bash", engineCall)
	if stringValue(result["status"]) != "implementation_selection_required" ||
		!boolValue(result["decision_required"], false) || boolValue(result["executed"], true) {
		t.Fatalf("historical environment bypassed current choice: %#v", result)
	}

	run.setSelectedImplementations("Engine A")
	result = gateway.agentRuntimeImplementationExecutionChoicePreflight("bash", engineCall)
	if stringValue(result["status"]) != "implementation_skill_required" ||
		stringValue(result["required_skill"]) != "engine-a-skill" || boolValue(result["executed"], true) {
		t.Fatalf("selected implementation bypassed its dedicated Skill: %#v", result)
	}
	gateway.allowedTools = []string{"bash"}
	feedback := gateway.toolCallPreflightDiagnostic(context.Background(), agentruntime.ToolCall{
		ID: "selected-engine", Name: "bash", Arguments: mustMarshalRawMessage(engineCall),
	})
	var diagnostic map[string]any
	if err := json.Unmarshal([]byte(feedback), &diagnostic); err != nil {
		t.Fatalf("invalid model-facing preflight: %v: %s", err, feedback)
	}
	if diagnostic["required_skill"] != "engine-a-skill" {
		t.Fatalf("model-facing preflight discarded the exact required Skill: %#v", diagnostic)
	}
	run.addExecutedSkillNames("engine-a-skill")
	if result := gateway.agentRuntimeImplementationExecutionChoicePreflight("bash", engineCall); result != nil {
		t.Fatalf("selected implementation with loaded Skill was blocked: %#v", result)
	}
}

func TestGenericMaterializedSkillAssetMayPrepareInputsBeforeImplementationChoice(t *testing.T) {
	workspace := t.TempDir()
	skill, script := materializedImplementationChoiceTestSkill(
		t, workspace, "binding-mode-analysis", "scripts/analyze_binding_pocket.py", nil,
	)
	catalog := skills.NewCatalog()
	catalog.AddSkill(skill)
	run := &sessionRunnerChatRun{TaskIntent: "Design molecules from a binding pocket."}
	run.addRequiredScientificCapabilities("pocket-conditioned-molecule-generation")
	run.addExecutedSkillNames("binding-mode-analysis")
	gateway := serverAgentRuntimeToolGateway{
		server: &Server{skillCatalog: catalog}, taskRun: run,
		kernel: &agentKernelContext{workspaceDir: workspace},
	}
	input := map[string]any{
		"environment": "utility", "command": "python " + script + " --structure target.cif",
	}
	if result := gateway.agentRuntimeImplementationExecutionChoicePreflight("bash", input); result != nil {
		t.Fatalf("generic materialized input-preparation Skill was blocked: %#v", result)
	}
	if result := gateway.agentRuntimeSkillExecutionContractPreflight("bash", input); result != nil {
		t.Fatalf("generic materialized input-preparation Skill failed full admission: %#v", result)
	}
	if !run.implementationSelectionRequiredSnapshot() {
		t.Fatal("generic input preparation incorrectly satisfied the implementation choice")
	}
	if result := gateway.agentRuntimeImplementationExecutionChoicePreflight("python", map[string]any{
		"environment": "utility", "code": "run_unselected_engine()",
	}); stringValue(result["status"]) != "implementation_selection_required" {
		t.Fatalf("generic input preparation authorized later engine code: %#v", result)
	}
	if result := gateway.agentRuntimeImplementationExecutionChoicePreflight("bash", map[string]any{
		"environment": "utility", "command": "python " + script + " --structure target.cif && python -m invented_engine",
	}); stringValue(result["status"]) != "implementation_selection_required" {
		t.Fatalf("compound command bypassed implementation choice: %#v", result)
	}
}

func TestGenericMaterializedSkillPreparationExemptionFailsClosed(t *testing.T) {
	workspace := t.TempDir()
	genericLoaded, genericScript := materializedImplementationChoiceTestSkill(
		t, workspace, "generic-loaded", "scripts/prepare.py", nil,
	)
	genericUnloaded, unloadedScript := materializedImplementationChoiceTestSkill(
		t, workspace, "generic-unloaded", "scripts/prepare.py", nil,
	)
	dedicatedLoaded, dedicatedScript := materializedImplementationChoiceTestSkill(
		t, workspace, "dedicated-loaded", "scripts/run.py", []string{"Engine A"},
	)
	tamperedGeneric, tamperedScript := materializedImplementationChoiceTestSkill(
		t, workspace, "generic-tampered", "scripts/prepare.py", nil,
	)
	if err := os.Chmod(tamperedScript, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tamperedScript, []byte("#!/usr/bin/env python3\nprint('tampered')\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	catalog := skills.NewCatalog()
	for _, skill := range []skills.Skill{genericLoaded, genericUnloaded, dedicatedLoaded, tamperedGeneric} {
		catalog.AddSkill(skill)
	}
	run := &sessionRunnerChatRun{TaskIntent: "Run a substantial scientific workflow."}
	run.addRequiredScientificCapabilities("scientific-generation")
	run.addExecutedSkillNames("generic-loaded", "dedicated-loaded", "generic-tampered")
	gateway := serverAgentRuntimeToolGateway{
		server: &Server{skillCatalog: catalog}, taskRun: run,
		kernel: &agentKernelContext{workspaceDir: workspace},
	}
	if result := gateway.agentRuntimeImplementationExecutionChoicePreflight("bash", map[string]any{
		"command": "python " + genericScript,
	}); result != nil {
		t.Fatalf("valid generic preparation fixture was blocked: %#v", result)
	}
	for name, command := range map[string]string{
		"unloaded generic Skill": "python " + unloadedScript,
		"dedicated Skill":        "python " + dedicatedScript,
		"non-preferred asset":    "python " + filepath.ToSlash(filepath.Join(filepath.Dir(genericScript), "other.py")),
		"missing content digest": "python " + filepath.ToSlash(filepath.Join(
			workspace, ".synon/runtime/skills", agentSkillRuntimeDirectoryName("generic-loaded"), "scripts", "prepare.py",
		)),
		"attached interpreter code": "python -c pass " + genericScript,
		"unrecognized interpreter":  "invented-engine " + genericScript,
		"tampered bundle":           "python " + tamperedScript,
	} {
		t.Run(name, func(t *testing.T) {
			result := gateway.agentRuntimeImplementationExecutionChoicePreflight("bash", map[string]any{
				"environment": "utility", "command": command,
			})
			if stringValue(result["status"]) != "implementation_selection_required" ||
				!boolValue(result["decision_required"], false) || boolValue(result["executed"], true) {
				t.Fatalf("untrusted preparation bypassed implementation choice: %#v", result)
			}
		})
	}
	withoutCatalog := serverAgentRuntimeToolGateway{
		server: &Server{}, taskRun: run, kernel: &agentKernelContext{workspaceDir: workspace},
	}.agentRuntimeImplementationExecutionChoicePreflight("bash", map[string]any{"command": "python " + genericScript})
	if stringValue(withoutCatalog["status"]) != "implementation_selection_required" {
		t.Fatalf("missing catalog authority bypassed implementation choice: %#v", withoutCatalog)
	}
	for name, candidate := range map[string]serverAgentRuntimeToolGateway{
		"missing current workspace identity": {server: &Server{skillCatalog: catalog}, taskRun: run},
		"bundle from different workspace": {
			server: &Server{skillCatalog: catalog}, taskRun: run,
			kernel: &agentKernelContext{workspaceDir: t.TempDir()},
		},
	} {
		t.Run(name, func(t *testing.T) {
			result := candidate.agentRuntimeSkillExecutionContractPreflight("bash", map[string]any{
				"environment": "utility", "command": "python " + genericScript,
			})
			if stringValue(result["status"]) != "implementation_selection_required" ||
				!boolValue(result["decision_required"], false) || boolValue(result["executed"], true) {
				t.Fatalf("workspace authority bypassed implementation choice: %#v", result)
			}
		})
	}
}

func materializedImplementationChoiceTestSkill(
	t *testing.T,
	workspace string,
	name string,
	preferredAsset string,
	implementationIdentities []string,
) (skills.Skill, string) {
	t.Helper()
	sourceRoot := t.TempDir()
	skillPath := filepath.Join(sourceRoot, "SKILL.md")
	source := "Run ${SYNON_SKILL_DIR}/" + filepath.ToSlash(preferredAsset) + "\n"
	if err := os.WriteFile(skillPath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(sourceRoot, filepath.FromSlash(preferredAsset))
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o700); err != nil {
		t.Fatal(err)
	}
	script := "#!/usr/bin/env python3\nimport argparse\nparser = argparse.ArgumentParser()\nparser.add_argument('--structure')\nparser.parse_args()\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	skill := skills.Skill{
		Name: name, Path: skillPath, Body: source,
		PreferredExecutionAssets: []string{filepath.ToSlash(preferredAsset)},
		ImplementationIdentities: append([]string(nil), implementationIdentities...),
	}
	bundle, hasRuntimeResources, err := loadAgentSkillRuntimeBundle(skill)
	if err != nil || !hasRuntimeResources {
		t.Fatalf("load test Skill runtime bundle: has_runtime=%t err=%v", hasRuntimeResources, err)
	}
	target, err := materializeAgentSkillRuntimeBundle(workspace, skill.Name, bundle)
	if err != nil {
		t.Fatalf("materialize test Skill runtime bundle: %v", err)
	}
	return skill, filepath.ToSlash(filepath.Join(target, filepath.FromSlash(preferredAsset)))
}

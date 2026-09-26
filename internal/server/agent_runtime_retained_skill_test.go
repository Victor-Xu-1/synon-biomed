package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"synon-go/internal/agentruntime"
	"synon-go/internal/sciencecapability"
	"synon-go/internal/skills"
)

func TestClosedSkillRouteRetainsVerifiedCurrentEntrypoint(t *testing.T) {
	source, workspace := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "scripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"SKILL.md": "Use ${SYNON_SKILL_DIR}/scripts/run.py", "scripts/run.py": "print('reviewed')\n"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	skill := skills.Skill{Name: "engine-skill", Path: filepath.Join(source, "SKILL.md"), Body: "Use ${SYNON_SKILL_DIR}/scripts/run.py", ImplementationIdentities: []string{"Engine"}}
	bundle, resources, err := loadAgentSkillRuntimeBundle(skill)
	if err != nil || !resources {
		t.Fatalf("bundle: %v %v", resources, err)
	}
	directory, err := materializeAgentSkillRuntimeBundle(workspace, skill.Name, bundle)
	if err != nil {
		t.Fatal(err)
	}
	catalog := skills.NewCatalog()
	catalog.AddSkill(skill)
	run := &sessionRunnerChatRun{SelectedImplementations: []string{"Engine"}}
	run.addExecutedSkillNames(skill.Name)
	gateway := serverAgentRuntimeToolGateway{taskRun: run, kernel: &agentKernelContext{workspaceDir: workspace}, server: &Server{
		skillCatalog: catalog, scienceCapabilities: &sciencecapability.Catalog{Capabilities: []sciencecapability.Definition{{
			ID: "capability", AcceptedEngines: []sciencecapability.EngineDefinition{{ID: "engine", ExecutionPack: sciencecapability.ExecutionPack{
				ID: "capability.engine", Mode: "local", Skill: skill.Name, Script: "reviewed/run.py",
			}}},
		}}},
	}}
	call := agentruntime.ToolCall{Name: "skill", Arguments: mustMarshalRawMessage(map[string]any{"skill": skill.Name})}
	for _, base := range []string{runnerCorrectionClosedRouteDiagnostic, noProgressClosedRouteDiagnostic} {
		raw := gateway.closedRouteDiagnostic(call, base)
		var result map[string]any
		if json.Unmarshal([]byte(raw), &result) != nil || result["required_entrypoint"] != filepath.ToSlash(filepath.Join(directory, "scripts/run.py")) {
			t.Fatalf("closed route discarded the verified callable asset: %s", raw)
		}
	}
	if err := os.Chmod(filepath.Join(directory, "scripts/run.py"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "scripts/run.py"), []byte("print('modified')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if result := gateway.retainedSkillExecutionDiagnostic(skill.Name); result != nil {
		t.Fatalf("modified runtime asset became authority: %#v", result)
	}
}

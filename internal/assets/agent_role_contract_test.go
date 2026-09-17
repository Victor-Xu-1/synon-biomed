package assets

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestBundledResearchAgentRoleContracts(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	root := filepath.Join(filepath.Dir(source), "..", "..", "assets", "synonbiomed", "agents")
	type role struct {
		Name         string   `yaml:"agent_name"`
		Internal     bool     `yaml:"internal"`
		Planning     bool     `yaml:"enable_plan_mode"`
		Delegation   bool     `yaml:"enable_subtask_delegation"`
		SkillsLocked bool     `yaml:"skills_locked"`
		WebSearch    bool     `yaml:"enable_web_search"`
		Excluded     []string `yaml:"excluded_tools"`
		Identity     string   `yaml:"identity_prompt"`
		Style        string   `yaml:"working_style_prompt"`
		System       string   `yaml:"system_prompt"`
	}
	load := func(name string) role {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(root, name, "metadata.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var value role
		if err := yaml.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	operon := load("operon")
	if operon.Name != "OPERON" || operon.Internal || !operon.Planning || operon.Delegation ||
		!strings.Contains(operon.Identity, "Synon Biomed") || len(operon.Style) < 100 {
		t.Fatalf("invalid research role: name=%s", operon.Name)
	}
	reviewer := load("reviewer")
	if reviewer.Name != "REVIEWER" || !reviewer.Internal || !reviewer.SkillsLocked ||
		reviewer.Planning || reviewer.Delegation || reviewer.WebSearch {
		t.Fatalf("invalid internal reviewer role: name=%s", reviewer.Name)
	}
	for _, tool := range []string{"python", "bash", "r", "save_artifacts", "edit_file", "manage_environments", "manage_packages"} {
		if !slices.Contains(reviewer.Excluded, tool) {
			t.Errorf("reviewer must exclude mutating execution tool %s", tool)
		}
	}
	for _, field := range []string{"submit_output", "human_description", "findings", "msg_idx", "artifact_version_id"} {
		if !strings.Contains(reviewer.System, field) {
			t.Errorf("reviewer does not describe current submission field %s", field)
		}
	}
}

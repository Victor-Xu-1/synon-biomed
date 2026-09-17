package server

import (
	"strings"
	"testing"

	"synon-go/internal/skills"
	"synon-go/internal/tools/registry"
)

func TestSkillSearchReturnsInvocationWithoutHostFilesystemPath(t *testing.T) {
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{
		Name:        "chem-render",
		Description: "Render chemical structures",
		Path:        "/host/catalog/chem-render/SKILL.md",
		Body:        "Use the managed chemistry renderer.",
		BodyHash:    "body-hash",
	})

	server := &Server{tools: registry.Default(), skillCatalog: catalog}
	searchTool, found := server.tools.Get("search_skills")
	if !found || !strings.Contains(searchTool.Description, "invoking the Skill tool") || !strings.Contains(searchTool.Description, "never by reading") {
		t.Fatalf("search_skills tool description does not enforce the load boundary: %#v", searchTool)
	}
	result, err := server.executeSkillSearchTool(map[string]any{"query": "chemical render"})
	if err != nil {
		t.Fatalf("SkillSearch error = %v", err)
	}

	matches := result.(map[string]any)["skill_matches"].([]map[string]any)
	if len(matches) != 1 {
		t.Fatalf("skill matches = %#v", matches)
	}
	match := matches[0]
	if _, exposed := match["path"]; exposed {
		t.Fatalf("SkillSearch exposed a host filesystem path: %#v", match)
	}
	invoke, ok := match["invoke"].(map[string]any)
	if !ok || invoke["tool"] != "skill" {
		t.Fatalf("search_skills invoke contract = %#v", match["invoke"])
	}
	input, ok := invoke["input"].(map[string]any)
	if !ok || input["skill"] != "chem-render" || input["args"] != "" {
		t.Fatalf("SkillSearch invoke input = %#v", invoke["input"])
	}
	if instruction := result.(map[string]any)["load_instruction"].(string); !strings.Contains(instruction, "skill tool") || !strings.Contains(instruction, "Do not read SKILL.md") {
		t.Fatalf("search_skills load instruction = %q", instruction)
	}
}

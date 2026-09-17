package server

import (
	"testing"

	"synon-go/internal/agentruntime"
	"synon-go/internal/skills"
)

func TestRequiredToolChoiceDoesNotCreateASecondSkillOrSourceWorkflow(t *testing.T) {
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{
		Name: "source-workflow", Keywords: []string{"research report"},
		Tools: []string{"skill", "web_search", "web_fetch"},
	})
	run := &sessionRunnerChatRun{TaskIntent: "research report"}
	run.addExecutedSkillNames("source-workflow")
	tools := []agentruntime.ToolSchema{
		{Name: "skill"},
		{Name: "web_search", Capabilities: []string{"source-discovery"}},
		{Name: "web_fetch", Capabilities: []string{"evidence-read"}},
	}
	gateway := serverAgentRuntimeToolGateway{
		server: &Server{skillCatalog: catalog}, taskRun: run, toolSchemas: tools,
	}
	if choice := gateway.RequiredToolChoice(nil, tools); choice != nil {
		t.Fatalf("Skill metadata forced a tool outside the outer model loop: %#v", choice)
	}
}

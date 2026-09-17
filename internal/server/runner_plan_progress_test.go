package server

import (
	"testing"

	"synon-go/internal/agentruntime"
)

func TestAutonomousPlanProgressExpansionPreservesCapturedPermissions(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	run := &sessionRunnerChatRun{}
	gateway := serverAgentRuntimeToolGateway{
		server: fixture.server, taskRun: run, hasToolSnapshot: true,
		toolSchemas: []agentruntime.ToolSchema{{Name: generatePlanToolName}, {Name: updateStepStatusToolName}},
	}
	if additions := gateway.AdditionalModelToolSchemas(nil); len(additions) != 0 {
		t.Fatalf("progress tool exposed before a durable plan: %v", additions)
	}
	run.planProgressAvailable.Store(true)
	additions := gateway.AdditionalModelToolSchemas(nil)
	if len(additions) != 1 || additions[0].Name != updateStepStatusToolName {
		t.Fatalf("durable plan did not expose existing progress authority: %v", additions)
	}
	if duplicate := gateway.AdditionalModelToolSchemas(additions); len(duplicate) != 0 {
		t.Fatalf("duplicate progress schemas: %v", duplicate)
	}
	gateway.toolSchemas = []agentruntime.ToolSchema{{Name: generatePlanToolName}}
	if additions := gateway.AdditionalModelToolSchemas(nil); len(additions) != 0 || gateway.toolAllowed(updateStepStatusToolName) {
		t.Fatal("plan generation elevated an ungranted progress tool")
	}
	gateway.taskRun = &sessionRunnerChatRun{}
	gateway.toolSchemas = []agentruntime.ToolSchema{{Name: updateStepStatusToolName}}
	if additions := gateway.AdditionalModelToolSchemas(nil); len(additions) != 0 {
		t.Fatal("plan readiness leaked into another task")
	}
}

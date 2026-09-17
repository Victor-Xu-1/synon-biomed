package server

import (
	"testing"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
	"synon-go/internal/skills"
)

func TestSessionRunnerCapabilityStateRestoresOnlyCompletedToolContracts(t *testing.T) {
	run := &sessionRunnerChatRun{}
	run.setToolCapabilityCatalog([]agentruntime.ToolSchema{
		{Name: "source_reader", Capabilities: []string{"source-evidence", "read-only"}},
		{Name: "analysis_kernel", Capabilities: []string{"runtime-execution"}},
	})
	run.restoreToolCapabilitiesFromEntries([]eventjournal.Entry{
		{Message: eventjournal.Message{
			"type": "runner_checkpoint", "toolPhase": "completed", "toolName": "source_reader",
		}},
		{Message: eventjournal.Message{
			"type": "runner_checkpoint", "toolPhase": "failed", "toolName": "analysis_kernel",
		}},
	})
	snapshot := run.capabilitySnapshot()
	if len(snapshot.ActivatedTools) != 1 || snapshot.ActivatedTools[0] != "sourcereader" {
		t.Fatalf("activated tools=%#v", snapshot.ActivatedTools)
	}
	if !run.hasActivatedCapability(runtimeCapabilitySourceEvidence) ||
		run.hasActivatedCapability("runtime-execution") {
		t.Fatalf("activated capabilities=%#v", snapshot.ActivatedCapabilities)
	}
	restored := &sessionRunnerChatRun{}
	restored.setToolCapabilityCatalog([]agentruntime.ToolSchema{{
		Name: "source_reader", Capabilities: []string{"source-evidence"},
	}})
	restored.restoreToolCapabilitiesFromContinuity([]sessionRunnerToolContinuityRecord{{
		ToolName: "source_reader", Successful: true,
		ToolCapabilities: []string{"source-evidence"},
	}})
	if !restored.hasActivatedCapability(runtimeCapabilitySourceEvidence) {
		t.Fatal("compacted continuity lost activated Tool capability")
	}
}

func TestSessionRunnerSourceWorkflowUsesSkillAndToolMetadataNotTaskText(t *testing.T) {
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{
		Name: "generic-source-workflow", Tools: []string{"cross_source_reader"},
	})
	server := &Server{skillCatalog: catalog}
	run := &sessionRunnerChatRun{TaskIntent: "ordinary words with no routing authority"}
	run.setToolCapabilityCatalog([]agentruntime.ToolSchema{{
		Name: "cross_source_reader", Capabilities: []string{"research", "source-evidence"},
	}})
	if server.sessionRunnerSourceWorkflowActive(run) {
		t.Fatal("task prose activated a source workflow without a contract or receipt")
	}
	run.addExecutedSkillNames("generic-source-workflow")
	if !server.sessionRunnerSourceWorkflowActive(run) {
		t.Fatal("selected Skill Tool capabilities did not activate source provenance")
	}

	unrelated := &sessionRunnerChatRun{TaskIntent: "研究 对接 计算 安装 分析"}
	unrelated.setToolCapabilityCatalog([]agentruntime.ToolSchema{{
		Name: "analysis_kernel", Capabilities: []string{"runtime-execution"},
	}})
	if server.sessionRunnerSourceWorkflowActive(unrelated) {
		t.Fatal("biomedical task keywords became capability activation authority")
	}
}

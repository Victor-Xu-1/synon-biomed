package server

import (
	"context"
	"encoding/json"
	"reflect"
	"synon-go/internal/agentruntime"
	"synon-go/internal/skills"
	"testing"
)

func TestAskUserCapabilityCorrectionForcesEveryRequiredSkillLoad(t *testing.T) {
	tools := []agentruntime.ToolSchema{{Name: "skill"}, {Name: "ask_user"}}
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "ask-invalid", Name: "ask_user"}}},
		{Role: "tool", ToolCallID: "ask-invalid", Content: `{"ok":true,"executed":false,"status":"implementation_decision_contract_incomplete","required_skills":["pocket2mol-local","diffsbdd-local"]}`},
	}
	choice, _ := runnerPendingAskUserRequiredSkillChoice(runnerPendingAskUserRequiredSkillNames(messages), tools).(map[string]any)
	if choice["name"] != "skill" {
		t.Fatalf("missing dedicated Skills did not force the Skill tool: %#v", choice)
	}
	messages = append(messages,
		agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "load-pocket", Name: "skill", Arguments: json.RawMessage(`{"skill":"pocket2mol-local"}`)}}},
		agentruntime.Message{Role: "tool", ToolCallID: "load-pocket", Content: `"<skill-metadata name=\"pocket2mol-local\" />\n\n# Pocket2Mol"`},
	)
	choice, _ = runnerPendingAskUserRequiredSkillChoice(runnerPendingAskUserRequiredSkillNames(messages), tools).(map[string]any)
	if choice["name"] != "skill" {
		t.Fatalf("one remaining dedicated Skill did not keep the Skill tool required: %#v", choice)
	}
	messages = append(messages,
		agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "load-diff", Name: "skill", Arguments: json.RawMessage(`{"skill":"diffsbdd-local"}`)}}},
		agentruntime.Message{Role: "tool", ToolCallID: "load-diff", Content: `{"success":true,"loaded":true}`},
	)
	if choice := runnerPendingAskUserRequiredSkillChoice(runnerPendingAskUserRequiredSkillNames(messages), tools); choice != nil {
		t.Fatalf("all required Skills were loaded but the Skill tool remained forced: %#v", choice)
	}
}

func TestAskUserRequiredSkillsEndWithAcceptedDecision(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		pending       bool
	}{
		{"answered", `{"ok":true,"status":"answered"}`, false},
		{"waiting", `{"ok":true,"status":"awaiting_user_response"}`, false},
		{"wrapped answer", `{"ok":true,"result":{"status":"answered"}}`, false},
		{"transport failure", `{"ok":false,"error":"transport failed"}`, true},
		{"failed wrapper", `{"ok":false,"result":{"status":"answered"}}`, true},
		{"non-executing answer", `{"ok":true,"executed":false,"status":"answered"}`, true},
		{"non-executing outer", `{"ok":true,"executed":false,"result":{"status":"answered"}}`, true},
		{"non-executing inner", `{"ok":true,"result":{"executed":false,"status":"answered"}}`, true},
		{"non-executing wait", `{"ok":true,"executed":false,"status":"awaiting_user_response"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := []agentruntime.Message{
				{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "initial", Name: "ask_user"}, {ID: "latest", Name: "ask_user"}}},
				{Role: "tool", ToolCallID: "initial", Content: `{"executed":false,"status":"implementation_decision_contract_incomplete","required_skills":["engine-a"]}`},
			}
			gateway := serverAgentRuntimeToolGateway{taskRun: &sessionRunnerChatRun{}}
			tools := []agentruntime.ToolSchema{{Name: "skill"}, {Name: "ask_user"}}
			gateway.RequiredToolChoice(messages, tools)
			messages = append(messages, agentruntime.Message{Role: "tool", ToolCallID: "latest", Content: tc.content})
			gateway.RequiredToolChoice(messages, tools)
			if got := len(gateway.taskRun.pendingRequiredSkillNamesSnapshot()) > 0; got != tc.pending {
				t.Fatalf("pending=%v want %v", got, tc.pending)
			}
		})
	}
}

func TestPendingAlternativeSkillInspectionPreservesExecutionChoice(t *testing.T) {
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{Name: "engine-b-skill", ImplementationIdentities: []string{"engine-b"}})
	catalog.AddSkill(skills.Skill{Name: "unrelated-skill", ImplementationIdentities: []string{"engine-c"}})
	run := &sessionRunnerChatRun{TaskIntent: "Compare alternatives before asking for a new choice.", SelectedImplementations: []string{"engine-a"}}
	gateway := serverAgentRuntimeToolGateway{server: &Server{skillCatalog: catalog}, taskRun: run}
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "proposal", Name: "ask_user"}}},
		{Role: "tool", ToolCallID: "proposal", Content: `{"executed":false,"status":"implementation_decision_contract_incomplete","required_skills":["engine-b-skill"]}`},
	}
	tools := []agentruntime.ToolSchema{{Name: "skill"}, {Name: "ask_user"}}
	if choice := gateway.RequiredToolChoice(messages, tools); choice == nil {
		t.Fatal("missing Skill was not scheduled")
	}
	if result := gateway.agentRuntimeSelectedImplementationSkillPreflight("skill", map[string]any{"skill": "engine-b-skill"}); result != nil {
		t.Fatalf("required inspection blocked: %v", result)
	}
	if result := gateway.agentRuntimeSkillExecutionContractPreflight("skill", map[string]any{"skill": "engine-b-skill"}); result != nil {
		t.Fatalf("another contract gate blocked the required read: %v", result)
	}
	if result := gateway.agentRuntimeSelectedImplementationSkillPreflight("skill", map[string]any{"skill": "unrelated-skill"}); result == nil || result["status"] != "required_skill_load_pending" {
		t.Fatalf("unrelated inspection bypassed pending contract: %v", result)
	}
	run.addExecutedSkillNames("engine-b-skill")
	messages = append(messages,
		agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "inspect-b", Name: "skill", Arguments: json.RawMessage(`{"skill":"engine-b-skill"}`)}}},
		agentruntime.Message{Role: "tool", ToolCallID: "inspect-b", Content: `{"ok":true,"loaded":true}`},
	)
	if choice := gateway.RequiredToolChoice(messages, tools); choice != nil {
		t.Fatalf("successful inspection did not release forced Skill: %v", choice)
	}
	if got := run.selectedImplementationsSnapshot(); !reflect.DeepEqual(got, []string{"engine-a"}) {
		t.Fatalf("inspection changed selection: %v", got)
	}
	ctx := withTranscriptRunnerChatRun(context.Background(), run)
	if result, blocked := managedEnvironmentImplementationDecision(ctx, "engine-b", true); !blocked || result["status"] != "selected_implementation_mismatch" {
		t.Fatalf("inspection authorized alternate execution: %v", result)
	}
	if result, blocked := managedEnvironmentImplementationDecision(ctx, "engine-a", true); blocked {
		t.Fatalf("selected implementation blocked: %v", result)
	}
}

func TestRequiredSkillLoadCannotSucceedThroughFailedEnvelope(t *testing.T) {
	for _, result := range []string{
		`{"ok":false,"result":{"loaded":true}}`,
		`{"ok":true,"executed":false,"result":{"loaded":true}}`,
		`{"ok":true,"result":{"loaded":true,"executed":false}}`,
	} {
		if runnerSkillLoadResultSucceeded(result) {
			t.Fatalf("failed or non-executed Skill load counted as success: %s", result)
		}
	}
}

func TestGatewayForcesAskUserRequiredSkillWithinTheSameExecutionUnit(t *testing.T) {
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{Name: "pocket2mol-local", ImplementationIdentities: []string{"Pocket2Mol"}})
	catalog.AddSkill(skills.Skill{Name: "unrelated-skill"})
	gateway := serverAgentRuntimeToolGateway{
		server:  &Server{skillCatalog: catalog},
		taskRun: &sessionRunnerChatRun{TaskIntent: "design from a protein pocket"},
	}
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "ask-invalid", Name: "ask_user"}}},
		{Role: "tool", ToolCallID: "ask-invalid", Content: `{"ok":true,"executed":false,"status":"implementation_decision_contract_incomplete","required_skills":["pocket2mol-local"]}`},
	}
	choice, _ := gateway.RequiredToolChoice(messages, []agentruntime.ToolSchema{{Name: "skill"}, {Name: "ask_user"}}).(map[string]any)
	if choice["name"] != "skill" {
		t.Fatalf("same-unit AskUser correction did not force Skill loading: %#v", choice)
	}
	blocked := gateway.agentRuntimeSelectedImplementationSkillPreflight("skill", map[string]any{"skill": "unrelated-skill"})
	if stringValue(blocked["status"]) != "required_skill_load_pending" ||
		!pendingSkillNamesContain(stringArrayValue(blocked["required_skills"]), "pocket2mol-local") {
		t.Fatalf("unrelated Skill bypassed the exact pending requirement: %#v", blocked)
	}
	if allowed := gateway.agentRuntimeSelectedImplementationSkillPreflight("skill", map[string]any{"skill": "pocket2mol-local"}); allowed != nil {
		t.Fatalf("exact pending Skill was blocked: %#v", allowed)
	}
}

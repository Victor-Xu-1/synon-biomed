package server

import (
	"testing"

	eventjournal "synon-go/internal/persistence/journal"
)

func TestAppendRunnerResolvedInputResponsesKeepsReplayNormalizedAnswers(t *testing.T) {
	all := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{
			"role": "user", "type": "message", "messageOrigin": "task_intent",
			"text": "Design 30 diverse CRBN ligands and rank them by docking.",
		}},
		{EventID: 2, Message: eventjournal.Message{
			"role": "user", "type": "message", "messageOrigin": "input_response",
			"text": "Resolved input requests:\\nsynon-standard-intake-initial-1: answered",
		}},
		{EventID: 3, Message: eventjournal.Message{
			"runnerAttempt": 2, "type": "runner_checkpoint", "toolName": "web_search",
		}},
	}
	scoped := appendRunnerResolvedInputResponses(all, []eventjournal.Entry{all[2]})
	if !runnerEntriesContainResolvedInputResponse(scoped) {
		t.Fatalf("replay-normalized resolved input response was dropped: %#v", scoped)
	}
}

func TestAppendedInputResponseRetainsProtocolDiagnosticWithoutInventingAnotherToolRequirement(t *testing.T) {
	all := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{
			"role": "user", "type": "message", "messageOrigin": "task_intent",
			"text": "Design and dock the compounds.",
		}},
		{EventID: 2, Message: eventjournal.Message{
			"role": "user", "type": "message", "messageOrigin": "input_response",
			"text": "Resolved input requests:\nintake: answered",
		}},
		{EventID: 3, Message: eventjournal.Message{
			"runnerAttempt": 2, "type": "runner_checkpoint",
			"reason_code":   sessionRunnerRequiredToolChoiceUnsatisfiedReasonCode,
			"resume_detail": "the provider returned prose where a tool call was required",
		}},
	}
	scoped := appendRunnerResolvedInputResponses(all, []eventjournal.Entry{all[2]})
	correction, found := latestRunnerCorrection(scoped)
	if !found || correction.ReasonCode != sessionRunnerRequiredToolChoiceUnsatisfiedReasonCode {
		t.Fatalf("durable correction hidden by appended input response: correction=%#v found=%t scoped=%#v", correction, found, scoped)
	}
	if recoveredRunnerCorrectionRequiresTool(scoped) {
		t.Fatal("provider protocol noncompliance became a self-sustaining generic tool requirement")
	}
}

func TestProtocolDiagnosticDoesNotHideEarlierSubstantiveCorrection(t *testing.T) {
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{
			"role": "user", "type": "message", "messageOrigin": "task_intent", "text": "Research and deliver.",
		}},
		{EventID: 2, Message: eventjournal.Message{
			"type": "runner_checkpoint", "reason_code": sessionRunnerPlanStepsIncompleteReasonCode,
			"resume_detail": "complete the current durable plan transition",
		}},
		{EventID: 3, Message: eventjournal.Message{
			"type": "runner_checkpoint", "reason_code": sessionRunnerRequiredToolChoiceUnsatisfiedReasonCode,
			"resume_detail": "the provider returned prose where a tool call was required",
		}},
	}
	correction, found := latestRunnerCorrection(entries)
	if !found || correction.ReasonCode != sessionRunnerPlanStepsIncompleteReasonCode ||
		!recoveredRunnerCorrectionRequiresTool(entries) {
		t.Fatalf("substantive correction was lost: correction=%#v found=%t", correction, found)
	}
}

func TestNewTaskIntentStillClearsPriorDurableCorrection(t *testing.T) {
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{
			"type":          "runner_checkpoint",
			"reason_code":   sessionRunnerRequiredToolChoiceUnsatisfiedReasonCode,
			"resume_detail": "the provider returned prose where a tool call was required",
		}},
		{EventID: 2, Message: eventjournal.Message{
			"role": "user", "type": "message", "messageOrigin": "task_intent",
			"text": "Start a different task.",
		}},
	}
	if correction, found := latestRunnerCorrection(entries); found {
		t.Fatalf("prior correction crossed a new task boundary: %#v", correction)
	}
}

func TestAppendRunnerResolvedInputResponsesKeepsAnswersWhenSameTaskIsResubmitted(t *testing.T) {
	task := "寻找最新的CRBN 人源化共晶结构PDB 然后基于结合模式和ligand 设计新的ligand 要求结构多样性，设计30个结构进行docking排序"
	all := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{
			"role": "user", "type": "message", "messageOrigin": "task_intent", "text": task,
		}},
		{EventID: 2, Message: eventjournal.Message{
			"role": "user", "messageOrigin": "input_response",
			"text": "Resolved input requests:\\nsynon-standard-intake-initial-1: answered",
		}},
		{EventID: 3, Message: eventjournal.Message{
			"role": "user", "type": "message", "messageOrigin": "task_intent", "text": task,
		}},
		{EventID: 4, Message: eventjournal.Message{
			"runnerAttempt": 3, "type": "runner_checkpoint", "toolName": "runtime_get",
		}},
	}
	scoped := appendRunnerResolvedInputResponses(all, []eventjournal.Entry{all[3]})
	if !runnerEntriesContainResolvedInputResponse(scoped) {
		t.Fatalf("same-task resubmission dropped the prior resolved input response: %#v", scoped)
	}
}

func TestAppendRunnerResolvedInputResponsesKeepsAnswersAcrossRepeatedSameTaskRetries(t *testing.T) {
	task := "寻找最新的CRBN 人源化共晶结构PDB 然后基于结合模式和ligand 设计新的ligand 要求结构多样性，设计30个结构进行docking排序"
	all := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{
			"role": "user", "type": "message", "messageOrigin": "task_intent", "text": task,
		}},
		{EventID: 2, Message: eventjournal.Message{
			"role": "user", "messageOrigin": "input_response",
			"text": "Resolved input requests:\\nsynon-standard-intake-initial-1: answered",
		}},
		{EventID: 3, Message: eventjournal.Message{
			"role": "user", "type": "message", "messageOrigin": "task_intent", "text": task,
		}},
		{EventID: 4, Message: eventjournal.Message{
			"role": "user", "type": "message", "messageOrigin": "task_intent", "text": task,
		}},
		{EventID: 5, Message: eventjournal.Message{
			"runnerAttempt": 5, "type": "runner_checkpoint", "toolName": "runtime_get",
		}},
	}
	scoped := appendRunnerResolvedInputResponses(all, []eventjournal.Entry{all[4]})
	if !runnerEntriesContainResolvedInputResponse(scoped) {
		t.Fatalf("repeated same-task retries dropped the original resolved input response: %#v", scoped)
	}
}

func TestAppendRunnerResolvedInputResponsesDoesNotCrossDifferentTaskBoundary(t *testing.T) {
	all := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{
			"role": "user", "type": "message", "messageOrigin": "task_intent", "text": "first task",
		}},
		{EventID: 2, Message: eventjournal.Message{
			"role": "user", "messageOrigin": "input_response",
			"text": "Resolved input requests:\\nfirst: answered",
		}},
		{EventID: 3, Message: eventjournal.Message{
			"role": "user", "type": "message", "messageOrigin": "task_intent", "text": "different task",
		}},
		{EventID: 4, Message: eventjournal.Message{
			"runnerAttempt": 2, "type": "runner_checkpoint", "toolName": "runtime_get",
		}},
	}
	scoped := appendRunnerResolvedInputResponses(all, []eventjournal.Entry{all[3]})
	if runnerEntriesContainResolvedInputResponse(scoped) {
		t.Fatalf("resolved input response from a different task crossed the task boundary: %#v", scoped)
	}
}

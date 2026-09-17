package server

import (
	"fmt"
	"strings"
	"testing"

	eventjournal "synon-go/internal/persistence/journal"
)

func TestSessionRunnerToolContinuityPreservesLoadedSkillsAndSourcesAcrossLongCompaction(t *testing.T) {
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{
			"type": "runner_checkpoint", "toolPhase": "start", "toolCallId": "skill-stability",
			"toolName": "skill", "toolInput": map[string]any{"skill": "stability-shelf-life"},
		}},
		{EventID: 2, Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
			"toolCallId": "skill-stability", "toolName": "skill",
			"toolInput":                      map[string]any{"skill": "stability-shelf-life"},
			"requiredScientificCapabilities": []any{"scientific-literature"},
			"toolResult":                     map[string]any{"ok": true},
		}},
		{EventID: 3, Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
			"toolCallId": "mcp-pubmed", "toolName": "mcp__pubmed__search_articles",
			"toolInput":  map[string]any{"query": "aspirin stability"},
			"toolResult": map[string]any{"ok": true},
		}},
	}
	for index := 0; index < 40; index++ {
		entries = append(entries, eventjournal.Entry{EventID: int64(index + 4), Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
			"toolCallId": fmt.Sprintf("python-%02d", index), "toolName": "python",
			"toolResult": map[string]any{"ok": true},
		}})
	}
	records, err := sessionRunnerToolContinuityRecords(entries)
	if err != nil {
		t.Fatal(err)
	}
	context := sessionRunnerToolContinuityContext(records)
	for _, required := range []string{
		"loadedSkill=stability-shelf-life", "mcp__pubmed__search_articles", "aspirin stability",
	} {
		if !strings.Contains(context, required) {
			t.Fatalf("long continuity context missing %q: %s", required, context)
		}
	}
	compacted := []eventjournal.Entry{{EventID: 44, Message: eventjournal.Message{
		"type": "runner_checkpoint", "toolPhase": "auto_compact",
		sessionRunnerToolContinuityField: records,
	}}}
	if got := strings.Join(completedSkillNamesFromRunnerEntries(compacted), ","); got != "stability-shelf-life" {
		t.Fatalf("compacted loaded Skills=%q", got)
	}
	if got := strings.Join(requiredScientificCapabilitiesFromRunnerEntries(compacted), ","); got != "scientific-literature" {
		t.Fatalf("compacted required capabilities=%q", got)
	}
}

func TestSessionRunnerProviderContextTokenEstimateCountsNativeToolProtocol(t *testing.T) {
	largeArgument := strings.Repeat("expensive-input-", 800)
	largeResult := strings.Repeat("validated-result-", 800)
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{"type": "message", "role": "user", "text": "continue"}},
		{EventID: 2, Message: eventjournal.Message{
			"type": "runner_checkpoint",
			"modelToolCalls": []any{map[string]any{
				"id": "call-large", "type": "function", "name": "python",
				"arguments": map[string]any{"code": largeArgument},
			}},
		}},
		{EventID: 3, Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
			"toolCallId": "call-large", "toolName": "python",
			"toolResult": map[string]any{"ok": true, "stdout": largeResult},
		}},
	}

	visibleOnly := estimateChatMessagesTokens(sessionEntriesToChatMessages("system", entries))
	providerEstimate, err := sessionRunnerProviderContextTokenEstimate("system", entries)
	if err != nil {
		t.Fatal(err)
	}
	if providerEstimate <= visibleOnly+5_000 {
		t.Fatalf("provider estimate=%d visible-only=%d; tool protocol was not counted", providerEstimate, visibleOnly)
	}
}

func TestSessionRunnerToolContinuityPreservesPlanAndSourceReadIdentity(t *testing.T) {
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
			"toolCallId": "step-update", "toolName": updateStepStatusToolName,
			"toolInput":  map[string]any{"step": "research-module-2", "status": "in_progress"},
			"toolResult": map[string]any{"ok": true, "step": "research-module-2", "title": "Clinical evidence", "status": "in_progress", "applied": true},
		}},
		{EventID: 2, Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
			"toolCallId": "source-read", "toolName": "read_file",
			"toolInput":  map[string]any{"version_id": "ltr-source", "json_pointer": "/result/documents"},
			"toolResult": map[string]any{"filename": "research.json", "source_version_id": "ltr-source", "json_pointer": "/result/documents", "truncated": false},
		}},
	}
	for index := 0; index < 40; index++ {
		entries = append(entries, eventjournal.Entry{EventID: int64(index + 3), Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
			"toolCallId": fmt.Sprintf("later-%02d", index), "toolName": "python", "toolResult": map[string]any{"ok": true},
		}})
	}
	records, err := sessionRunnerToolContinuityRecords(entries)
	if err != nil {
		t.Fatal(err)
	}
	context := sessionRunnerToolContinuityContext(records)
	for _, required := range []string{
		"research-module-2", "Clinical evidence", "source-read", "ltr-source", "/result/documents",
	} {
		if !strings.Contains(context, required) {
			t.Fatalf("compaction continuity lost %q: %s", required, context)
		}
	}
}

func TestSessionRunnerToolContinuityKeepsSuccessfulReceiptAfterLaterFailureAndCompaction(t *testing.T) {
	entries := []eventjournal.Entry{
		{EventID: 10, Message: eventjournal.Message{
			"type": "runner_checkpoint", "toolPhase": "start", "toolCallId": "software-success",
			"toolName": softwareRuntimeToolName,
			"toolInput": map[string]any{
				"capability": "generic-compute", "language": "python", "executable": "python",
				"args": []any{"pipeline.py"}, "working_dir": "/workspace/task",
				"packages": []any{map[string]any{"manager": "conda", "spec": "numpy"}},
			},
		}},
		{EventID: 11, Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
			"toolCallId": "software-success", "toolName": softwareRuntimeToolName,
			"toolResult": map[string]any{
				"ok": true, "status": "completed", "request_digest": strings.Repeat("a", 64),
				"outputs": []any{map[string]any{"path": "results/data.csv", "sha256": strings.Repeat("b", 64)}},
				"cleanup": map[string]any{"process_tree_terminated": true},
			},
		}},
		{EventID: 12, Message: eventjournal.Message{
			"type": "runner_checkpoint", "toolPhase": "failed", "toolCallId": "software-later-failure",
			"toolName": softwareRuntimeToolName,
			"toolInput": map[string]any{
				"capability": "generic-compute-v2", "language": "python", "executable": "python",
				"args": []any{"pipeline.py"}, "working_dir": "/workspace/task",
			},
			"toolResult": map[string]any{
				"ok": false, "status": "failed", "code": "nonzero_exit", "retryable": false,
				"recovery": "inspect_stderr_then_repair_inputs_or_packages_without_switching_provider",
				"stderr":   "Traceback (most recent call last):\n  File \"/workspace/task/pipeline.py\", line 131, in main\n    value = regimen[\"t_end\"]\nKeyError: 't_end'",
			},
		}},
	}

	records, err := sessionRunnerToolContinuityRecords(entries)
	if err != nil {
		t.Fatal(err)
	}
	context := sessionRunnerToolContinuityContext(records)
	for _, required := range []string{
		"software-success", "successful=true", "results/data.csv", strings.Repeat("a", 64),
		"software-later-failure", "successful=false", "failureDiagnostic=", "nonzero_exit",
		`File "/workspace/task/pipeline.py", line 131`, `KeyError: 't_end'`,
		"Never substitute a speculative alternate cause",
		"does not erase an earlier successful immutable receipt",
	} {
		if !strings.Contains(context, required) {
			t.Fatalf("continuity context missing %q: %s", required, context)
		}
	}

	compacted := append(entries, eventjournal.Entry{EventID: 13, Message: eventjournal.Message{
		"type": "runner_checkpoint", "toolPhase": "auto_compact",
		sessionRunnerToolContinuityField: records,
	}})
	reloaded, err := sessionRunnerToolContinuityRecords(compacted)
	if err != nil {
		t.Fatal(err)
	}
	if got := sessionRunnerToolContinuityContext(reloaded); got != context {
		t.Fatalf("persisted continuity changed after compaction\nwant: %s\n got: %s", context, got)
	}
}

func TestSessionRunnerToolContinuityKeepsArbitraryCapabilityMilestone(t *testing.T) {
	records := make([]sessionRunnerToolContinuityRecord, 0, sessionRunnerContinuityRecentLimit+1)
	records = append(records, sessionRunnerToolContinuityRecord{
		EventID: 1, ToolCallID: "arbitrary-source", ToolName: "future_database_reader",
		Successful: true, ToolCapabilities: []string{"source-evidence", "evidence-read"},
	})
	for index := 0; index < sessionRunnerContinuityRecentLimit+2; index++ {
		records = append(records, sessionRunnerToolContinuityRecord{
			EventID: int64(index + 2), ToolCallID: fmt.Sprintf("ordinary-%d", index),
			ToolName: "ordinary_tool", Successful: true,
			ToolCapabilities: []string{"ordinary-operation"},
		})
	}
	bounded := boundSessionRunnerToolContinuity(records)
	found := false
	for _, record := range bounded {
		if record.ToolCallID == "arbitrary-source" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("capability milestone was dropped after compaction: %#v", bounded)
	}
}

func TestSessionRunnerToolContinuityPreservesValidatedOutputAcceptance(t *testing.T) {
	digest := strings.Repeat("a", 64)
	input := map[string]any{
		"capability": "generic-validation", "provider": "local-conda",
		"language": "python", "executable": "python", "args": []any{"verify.py"},
		"working_dir": "/workspace/chemistry-task",
		"expected_outputs": []any{map[string]any{
			"path": "deliverables/validation.json", "format": "json", "min_bytes": 2,
			"required_json_true": []any{"/checks/artifacts", "/checks/values"},
		}},
	}
	result := map[string]any{
		"ok": true, "status": "completed", "exit_status": "ok", "provider_id": "local-conda",
		"environment": "generic-validation-environment", "executable": "python",
		"runtime_generation": digest, "request_digest": digest,
		"stdout_sha256": digest, "stderr_sha256": digest,
		"cleanup": map[string]any{
			"process_group_terminated": true, "process_tree_terminated": true,
			"temporary_streams_closed": true,
		},
		"outputs": []any{map[string]any{
			"path": "deliverables/validation.json", "bytes": 128, "sha256": digest,
		}},
	}
	if _, err := decodeSoftwareRuntimeRequest(input); err != nil {
		t.Fatalf("valid software request rejected: %v", err)
	}
	if !verifiedSoftwareRuntimeExecutionResult(result) {
		t.Fatalf("valid software result rejected: %#v", result)
	}
	entries := []eventjournal.Entry{
		{EventID: 20, Message: eventjournal.Message{
			"type": "runner_checkpoint", "toolPhase": "start", "toolCallId": "validated-call",
			"toolName": softwareRuntimeToolName, "toolInput": input,
		}},
		{EventID: 21, Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
			"toolCallId": "validated-call", "toolName": softwareRuntimeToolName, "toolResult": result,
		}},
	}
	records, err := sessionRunnerToolContinuityRecords(entries)
	if err != nil {
		t.Fatal(err)
	}
	context := sessionRunnerToolContinuityContext(records)
	for _, required := range []string{
		`"allDeclaredOutputsValidated":true`, `"expectedOutputCount":1`,
		`"requiredJSONTrueCount":2`, `"requestDigest":"` + digest + `"`,
	} {
		if !strings.Contains(context, required) {
			t.Fatalf("acceptance continuity missing %q: %s", required, context)
		}
	}

	compacted := append(entries, eventjournal.Entry{EventID: 22, Message: eventjournal.Message{
		"type": "runner_checkpoint", "toolPhase": "auto_compact",
		sessionRunnerToolContinuityField: records,
	}})
	reloaded, err := sessionRunnerToolContinuityRecords(compacted)
	if err != nil {
		t.Fatal(err)
	}
	if got := sessionRunnerToolContinuityContext(reloaded); got != context {
		t.Fatalf("validated acceptance changed after compaction\nwant: %s\n got: %s", context, got)
	}
}

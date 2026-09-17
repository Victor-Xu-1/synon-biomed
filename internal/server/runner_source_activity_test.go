package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
)

func TestSessionRunnerGenericSourceToolAttemptCountDeduplicatesTerminalReceipts(t *testing.T) {
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{
			"type": "runner_checkpoint", "toolName": "web_search", "toolCallId": "search-1", "toolPhase": "start",
		}},
		{EventID: 2, Message: eventjournal.Message{
			"type": "runner_checkpoint", "toolName": "web_search", "toolCallId": "search-1", "toolPhase": "completed",
		}},
		{EventID: 3, Message: eventjournal.Message{
			"type": "runner_checkpoint", "toolName": "web_search", "toolCallId": "search-1", "toolPhase": "completed",
		}},
		{EventID: 4, Message: eventjournal.Message{
			"type": "runner_checkpoint", "toolName": "WebFetch", "toolCallId": "fetch-1", "toolPhase": "failed",
		}},
		{EventID: 5, Message: eventjournal.Message{
			"type": "runner_checkpoint", "toolName": "python", "toolCallId": "python-1", "toolPhase": "completed",
		}},
	}
	if got := sessionRunnerGenericSourceToolAttemptCount(entries); got != 2 {
		t.Fatalf("generic source attempts=%d want=2", got)
	}
}

func TestSessionRunnerSourceActivityHasNoAttemptCutoff(t *testing.T) {
	activity := newSessionRunnerSourceToolActivity(10)
	for ordinal := 11; ordinal <= 10010; ordinal++ {
		if got := activity.record([]string{"research"}); got != ordinal {
			t.Fatalf("attempt ordinal=%d want=%d", got, ordinal)
		}
	}
	if got := activity.record([]string{"runtime-execution"}); got != 0 {
		t.Fatalf("non-source operation counted: %d", got)
	}
	if got := activity.record([]string{"source-evidence", "evidence-read"}); got != 10011 {
		t.Fatalf("source activity did not continue: %d", got)
	}
}

func TestSessionRunnerSourceActivityIsOptionalAndNormalizesInitialCount(t *testing.T) {
	var absent *sessionRunnerSourceToolActivity
	if got := absent.record([]string{"source-evidence"}); got != 0 {
		t.Fatalf("absent activity=%d", got)
	}
	if got := newSessionRunnerSourceToolActivity(-1).record([]string{"evidence-read"}); got != 1 {
		t.Fatalf("initial activity=%d", got)
	}
}

func TestDecodeWorkspaceMCPToolOutputPreservesStructuredFailure(t *testing.T) {
	decoded := decodeWorkspaceMCPToolOutput(`{"ok":false,"code":"upstream_unavailable","sourceUnavailable":true}`)
	value, ok := decoded.(map[string]any)
	if !ok || value["code"] != "upstream_unavailable" || value["sourceUnavailable"] != true {
		t.Fatalf("decoded MCP failure=%#v", decoded)
	}
	if text := decodeWorkspaceMCPToolOutput("plain text result"); text != "plain text result" {
		t.Fatalf("plain MCP result=%#v", text)
	}
}

func TestWorkspaceMCPToolTimeoutBecomesStructuredSourceFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	time.Sleep(3 * time.Millisecond)
	value := workspaceMCPToolTimeoutResult(ctx, errors.New("context deadline exceeded"))
	if value == nil || value["code"] != "mcp_tool_timeout" || value["sourceUnavailable"] != true ||
		value["retryable"] != true || agentruntime.ClassifyToolResult(value) != agentruntime.ToolResultUnavailable {
		t.Fatalf("timeout result=%#v", value)
	}
	if value := workspaceMCPToolTimeoutResult(context.Background(), errors.New("ordinary connector error")); value != nil {
		t.Fatalf("ordinary connector error was misclassified: %#v", value)
	}
}

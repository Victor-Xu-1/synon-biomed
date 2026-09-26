package server

import (
	"context"
	"errors"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestRunnerToolReplaySafetyUsesCapabilitiesAndFailsClosed(t *testing.T) {
	srv := New(Options{})
	for _, test := range []struct {
		name string
		tool string
		want bool
	}{
		{name: "registered read only search", tool: "WebSearch", want: true},
		{name: "registered bounded fetch", tool: "web_fetch", want: true},
		{name: "workspace mutation", tool: "save_artifacts", want: false},
		{name: "unknown tool", tool: "custom_unknown_tool", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			safe, err := srv.runnerToolCallReplaySafeAfterInterruption(
				context.Background(), SessionRunnerChatOptions{}, workspace.ToolCallBatchItem{
					ToolName: test.tool, ArgumentsJSON: []byte(`{}`),
				},
			)
			if err != nil || safe != test.want {
				t.Fatalf("safe=%t want=%t err=%v", safe, test.want, err)
			}
		})
	}
}

func TestToolReplaySafetyUnavailableIsBoundedCorrection(t *testing.T) {
	err := sessionRunnerToolReplaySafetyPending{toolName: "mcp__source__lookup", cause: context.DeadlineExceeded}
	var correction sessionRunnerBoundedCorrection
	bounded := errors.As(err, &correction)
	if !bounded {
		t.Fatalf("not a typed correction: %v", err)
	}
	cause := correction.runnerCorrection()
	reason, detail := cause.ReasonCode, cause.Detail
	if !bounded || reason != sessionRunnerToolReplaySafetyReasonCode || detail == "" {
		t.Fatalf("reason=%q detail=%q bounded=%t", reason, detail, bounded)
	}
	if !runnerInterruptionMayContinueSameTask(reason) || !runnerInterruptionAutoResume(reason) {
		t.Fatalf("replay-safety correction is not auto-resumable: %q", reason)
	}
}

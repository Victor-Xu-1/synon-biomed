package server

import (
	"context"
	"errors"
	"testing"
	"time"

	kernelruntime "synon-go/internal/kernel"
)

func TestAwaitAgentKernelExecutionStartJoinsTerminalAfterClosedStart(t *testing.T) {
	started := make(chan kernelruntime.ExecutionStarted)
	done := make(chan kernelruntime.ExecutionOutcome, 1)
	close(started)
	wantErr := errors.New("worker exited before startup")
	wantStartedAt := time.Now().UTC().Add(-time.Second)

	go func() {
		time.Sleep(20 * time.Millisecond)
		done <- kernelruntime.ExecutionOutcome{Err: wantErr, StartedAt: wantStartedAt}
		close(done)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	observedStart, observedOutcome, err := awaitAgentKernelExecutionStart(ctx, started, done)
	if err != nil {
		t.Fatal(err)
	}
	if observedStart.ExecID != "" {
		t.Fatalf("unexpected execution start: %#v", observedStart)
	}
	if observedOutcome == nil || !errors.Is(observedOutcome.Err, wantErr) ||
		!observedOutcome.StartedAt.Equal(wantStartedAt) {
		t.Fatalf("terminal handoff=%#v, want exact pre-start outcome", observedOutcome)
	}
}

func TestAgentKernelSyntheticExecutionStartPreservesDurableIdentity(t *testing.T) {
	wantStartedAt := time.Now().UTC().Add(-2 * time.Second)
	request := kernelruntime.SubmitRequest{
		KernelID: "kernel-1", FrameID: "frame-1", ExecID: "exec-1", ToolUseID: "tool-1",
		KernelKind: "analysis", Language: "python", Environment: "managed-python",
		Code: "print('test')", Origin: "agent",
	}
	started := agentKernelSyntheticExecutionStart(request, kernelruntime.ExecutionOutcome{StartedAt: wantStartedAt})
	if started.ExecID != request.ExecID || started.ToolUseID != request.ToolUseID ||
		started.KernelID != request.KernelID || started.FrameID != request.FrameID ||
		started.KernelKind != request.KernelKind || started.Language != request.Language ||
		started.Environment != request.Environment || started.Code != request.Code ||
		started.Origin != request.Origin || !started.StartedAt.Equal(wantStartedAt) {
		t.Fatalf("synthetic execution start=%#v", started)
	}
}

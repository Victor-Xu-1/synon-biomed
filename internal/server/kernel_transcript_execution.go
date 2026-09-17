package server

import (
	"context"
	"strings"

	kernelruntime "synon-go/internal/kernel"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

// kernelTranscriptExecutionBinding binds nested host calls to the exact
// server-authorized runner execution that opened the kernel cell. It is shared
// by host integrations that persist evidence, without coupling those
// integrations to a particular compute implementation.
type kernelTranscriptExecutionBinding struct {
	stream                  transcriptstore.Stream
	claim                   transcriptstore.RunnerClaim
	outerToolUseID          string
	operationID             string
	kernelID                string
	kernelGeneration        uint64
	currentKernelGeneration func() uint64
}

type kernelTranscriptExecutionIdentity struct {
	OwnerID          string
	ProjectID        string
	FrameID          string
	RootFrameID      string
	StreamUID        string
	StreamEpoch      int64
	RunnerID         string
	RunnerAttempt    int64
	RunnerClaim      transcriptstore.RunnerClaim
	OuterToolUseID   string
	KernelID         string
	KernelGeneration uint64
}

func kernelTranscriptExecutionBindingFromContext(
	ctx context.Context,
	outerToolUseID, kernelID string,
	kernelGeneration uint64,
	worker *kernelruntime.Worker,
) kernelTranscriptExecutionBinding {
	binding := kernelTranscriptExecutionBinding{
		outerToolUseID:   strings.TrimSpace(outerToolUseID),
		kernelID:         strings.TrimSpace(kernelID),
		kernelGeneration: kernelGeneration,
	}
	if worker != nil {
		binding.currentKernelGeneration = worker.Generation
	}
	if ctx == nil {
		return binding
	}
	run, _ := ctx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun)
	if run != nil && run.Transcript != nil {
		binding.stream = run.Transcript.Stream
		binding.claim = run.Transcript.Claim
		binding.operationID = strings.TrimSpace(run.KernelOperationIDs[binding.outerToolUseID])
	}
	return binding
}

func (s *Server) validateKernelTranscriptExecution(
	ctx context.Context,
	bound kernelHostExecutionIdentity,
	current workspace.KernelFrameAccess,
) (kernelTranscriptExecutionIdentity, error) {
	binding := bound.transcriptExecution
	if bound.fresh || binding.stream.UID == "" || binding.stream.Epoch <= 0 ||
		binding.claim.StreamUID == "" || binding.claim.OwnerID == "" || binding.claim.RunnerID == "" ||
		binding.claim.Attempt <= 0 || strings.TrimSpace(binding.claim.ClaimToken) == "" ||
		strings.TrimSpace(binding.outerToolUseID) == "" || strings.TrimSpace(binding.kernelID) == "" ||
		binding.kernelGeneration == 0 || binding.currentKernelGeneration == nil {
		return kernelTranscriptExecutionIdentity{}, kernelruntime.NewHostCallError(
			"permission_denied", "kernel transcript execution authority is unavailable",
		)
	}
	if binding.claim.StreamUID != binding.stream.UID || binding.claim.OwnerID != binding.stream.OwnerID ||
		binding.stream.OwnerID != current.UserID || binding.stream.ProjectID != current.Frame.ProjectID ||
		binding.stream.FrameID != current.Frame.ID || binding.stream.RootFrameID != current.Frame.RootFrameID {
		return kernelTranscriptExecutionIdentity{}, kernelruntime.NewHostCallError(
			"permission_denied", "kernel transcript authority does not match the live frame",
		)
	}
	if binding.currentKernelGeneration() != binding.kernelGeneration {
		return kernelTranscriptExecutionIdentity{}, kernelruntime.NewHostCallError(
			"permission_denied", "kernel transcript generation is stale",
		)
	}
	if s == nil || s.transcriptStore == nil {
		return kernelTranscriptExecutionIdentity{}, kernelruntime.NewHostCallError(
			"permission_denied", "kernel transcript authority is unavailable",
		)
	}
	var live transcriptstore.Stream
	err := s.transcriptStore.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		var validateErr error
		live, validateErr = tx.ValidateLiveRunnerClaim(ctx, binding.claim)
		return validateErr
	})
	if err != nil || live.UID != binding.stream.UID || live.Epoch != binding.stream.Epoch ||
		live.OwnerID != binding.stream.OwnerID || live.ProjectID != binding.stream.ProjectID ||
		live.FrameID != binding.stream.FrameID || live.RootFrameID != binding.stream.RootFrameID {
		return kernelTranscriptExecutionIdentity{}, kernelruntime.NewHostCallError(
			"permission_denied", "kernel transcript runner claim is stale",
		)
	}
	return kernelTranscriptExecutionIdentity{
		OwnerID: current.UserID, ProjectID: current.Frame.ProjectID,
		FrameID: current.Frame.ID, RootFrameID: current.Frame.RootFrameID,
		StreamUID: binding.stream.UID, StreamEpoch: binding.stream.Epoch,
		RunnerID: binding.claim.RunnerID, RunnerAttempt: binding.claim.Attempt,
		RunnerClaim: binding.claim, OuterToolUseID: binding.outerToolUseID,
		KernelID: binding.kernelID, KernelGeneration: binding.kernelGeneration,
	}, nil
}

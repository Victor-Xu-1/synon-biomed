package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const sessionRuntimeCleanupTimeout = 20 * time.Second

// releaseSessionRunnerKernels is the terminal resource gate for a logical
// task. Persistent Python/R/REPL workers are useful between tool calls, but
// they must not outlive the completed or cancelled root task. One-shot
// software_runtime executors close at their own durable settlement boundary;
// this closes the remaining in-process session workers without touching other
// tasks.
func (s *Server) releaseSessionRunnerKernels(
	parent context.Context,
	sessionID string,
	authority *transcriptRunnerAuthority,
) error {
	if s == nil || s.kernelManager == nil {
		return nil
	}
	rootFrameID := strings.TrimSpace(sessionID)
	if authority != nil {
		rootFrameID = firstNonEmpty(strings.TrimSpace(authority.Stream.RootFrameID), rootFrameID)
	}
	if rootFrameID == "" {
		return errors.New("terminal runtime cleanup has no root frame authority")
	}
	if parent == nil {
		parent = context.Background()
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), sessionRuntimeCleanupTimeout)
	defer cancel()
	closed, err := s.kernelManager.CloseSession(cleanupCtx, rootFrameID)
	if err != nil {
		return fmt.Errorf("close %d task kernel(s) for %s: %w", closed, rootFrameID, err)
	}
	return nil
}

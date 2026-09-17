package server

import (
	"context"
	"fmt"
	"strings"
	workspace "synon-go/internal/persistence/workspace"
	"time"
)

func (s *Server) waitKernelChildren(ctx context.Context, access workspace.KernelFrameAccess, children []workspace.KernelSupervisedChild, timeout time.Duration, cancelOnContext bool) []any {
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	ids := make([]string, len(children))
	valid := make([]bool, len(children))
	for index := range children {
		ids[index], valid[index] = children[index].FrameID, true
	}
	for {
		allTerminal := true
		for index := range children {
			current, found, _ := s.workspaceStore.GetKernelSupervisedChild(context.Background(), access.Frame.ID, ids[index], access.UserID)
			if found {
				children[index] = current
				if !kernelChildTerminal(current.Status) {
					allTerminal = false
				}
			}
		}
		if allTerminal || (!deadline.IsZero() && !time.Now().Before(deadline)) {
			return s.kernelChildrenResultsAndCollect(access, children, valid)
		}
		select {
		case <-ctx.Done():
			if cancelOnContext {
				for _, child := range children {
					if !kernelChildTerminal(child.Status) {
						s.cancelKernelChildRun(child.FrameID)
						_, _ = s.workspaceStore.CompleteKernelSupervisedChild(context.Background(), child.FrameID, access.UserID, "cancelled", nil, "blocking delegation was cancelled")
					}
				}
			}
			return s.kernelChildrenResultsAndCollect(access, children, valid)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (s *Server) collectKernelChildren(ctx context.Context, access workspace.KernelFrameAccess, frameIDs []string, children []workspace.KernelSupervisedChild, valid []bool, timeout time.Duration) []any {
	deadline := time.Now().Add(timeout)
	misses := make([]int, len(frameIDs))
	for {
		allTerminal := true
		for index, frameID := range frameIDs {
			if !valid[index] {
				continue
			}
			current, found, _ := s.workspaceStore.GetKernelSupervisedChild(context.Background(), access.Frame.ID, frameID, access.UserID)
			if found {
				children[index] = current
			}
			if kernelChildTerminal(children[index].Status) {
				continue
			}
			if kernelChildParked(children[index]) || s.kernelChildRunActive(frameID) {
				misses[index] = 0
			} else {
				misses[index]++
				if misses[index] >= 2 && time.Since(children[index].StartedAt) >= 30*time.Second {
					children[index].Status = "orphaned"
					continue
				}
			}
			if !kernelChildTerminal(children[index].Status) {
				allTerminal = false
			}
		}
		if allTerminal || !time.Now().Before(deadline) {
			return s.kernelCollectResultsAndCollect(access, children, valid, false)
		}
		select {
		case <-ctx.Done():
			return s.kernelCollectResultsAndCollect(access, children, valid, true)
		case <-time.After(2 * time.Second):
		}
	}
}

func (s *Server) kernelChildRunActive(frameID string) bool {
	_, active := kernelChildRuns.Load(kernelChildRunKey{server: s, frameID: frameID})
	return active
}

func kernelChildParked(child workspace.KernelSupervisedChild) bool {
	return strings.HasPrefix(child.Status, "awaiting")
}

func (s *Server) kernelCollectResultsAndCollect(access workspace.KernelFrameAccess, children []workspace.KernelSupervisedChild, valid []bool, cancelled bool) []any {
	results := make([]any, len(children))
	for index, child := range children {
		if !valid[index] {
			results[index] = map[string]any{"status": "failed", "error": "delegated child is unavailable"}
			continue
		}
		if child.Status == "orphaned" {
			results[index] = map[string]any{
				"frame_id": child.FrameID, "agent_name": child.AgentName, "name": child.Name,
				"status": "orphaned", "child_status": "processing", "restart_casualty": true,
				"note": "the child's frame says it is running but no runner holds it — it was orphaned by a daemon restart and will not finish on its own. Re-delegate the task, or send_message the frame to resume it.",
			}
			continue
		}
		if !kernelChildTerminal(child.Status) {
			if cancelled {
				results[index] = map[string]any{"frame_id": child.FrameID, "status": "cancelled", "error": "collect cancelled"}
				continue
			}
			results[index] = kernelChildCollectRunningDescriptor(child)
			continue
		}
		results[index] = kernelChildResult(child)
		if err := s.workspaceStore.CollectKernelChildLanding(context.Background(), access.Frame.ID, access.Frame.RootFrameID, access.UserID, child.FrameID); err != nil {
			results[index] = map[string]any{"status": "failed", "error": "delegated child result could not be collected"}
		}
	}
	return results
}

func kernelChildCollectRunningDescriptor(child workspace.KernelSupervisedChild) map[string]any {
	result := map[string]any{
		"frame_id": child.FrameID, "agent_name": child.AgentName, "name": child.Name,
		"status": "running", "child_status": child.Status,
	}
	if kernelChildParked(child) {
		result["parked_on_input"] = true
		result["note"] = "PARKED on an approval/input card — it will not progress until the card is resolved in the session UI. Not slow — blocked."
	} else {
		result["note"] = "still running at the deadline — steer it via host.send_message()/host.stop_child(), or call host.collect() again."
	}
	return result
}

func (s *Server) kernelChildrenResultsAndCollect(access workspace.KernelFrameAccess, children []workspace.KernelSupervisedChild, valid []bool) []any {
	results := s.kernelChildrenResults(children, valid)
	for index, child := range children {
		if !valid[index] || !kernelChildTerminal(child.Status) {
			continue
		}
		if err := s.workspaceStore.CollectKernelChildLanding(context.Background(), access.Frame.ID, access.Frame.RootFrameID, access.UserID, child.FrameID); err != nil {
			results[index] = map[string]any{"status": "failed", "error": "delegated child result could not be collected"}
		}
	}
	return results
}

func (s *Server) kernelChildrenResults(children []workspace.KernelSupervisedChild, valid []bool) []any {
	results := make([]any, len(children))
	for index, child := range children {
		if !valid[index] {
			results[index] = map[string]any{"status": "failed", "error": "delegated child is unavailable"}
			continue
		}
		results[index] = kernelChildResult(child)
	}
	return results
}

func kernelChildResult(child workspace.KernelSupervisedChild) map[string]any {
	if !kernelChildTerminal(child.Status) {
		return kernelChildRunningDescriptor(child, false)
	}
	result := map[string]any{
		"frame_id": child.FrameID, "status": kernelChildPublicStatus(child),
		"name": child.Name, "agent_name": child.AgentName,
	}
	for key, value := range child.Output {
		result[key] = value
	}
	if child.Error != "" {
		result["error"] = child.Error
	}
	return result
}

func kernelChildPublicStatus(child workspace.KernelSupervisedChild) string {
	if child.Status == "completed" {
		if stopped, _ := child.Output["stopped_by_parent"].(bool); stopped {
			return "cancelled"
		}
	}
	return child.Status
}

func kernelChildRunningDescriptor(child workspace.KernelSupervisedChild, dispatched bool) map[string]any {
	result := map[string]any{
		"frame_id": child.FrameID, "status": "running", "child_status": child.Status,
		"name": child.Name, "agent_name": child.AgentName,
	}
	if dispatched {
		result["dispatched"] = true
	}
	if child.Status == "awaiting_user_response" {
		result["note"] = "child is awaiting user input or approval"
	}
	return result
}

func kernelChildTerminal(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled" || status == "orphaned"
}

func (s *Server) stopKernelChild(ctx context.Context, access workspace.KernelFrameAccess, frameID, reason string) map[string]any {
	child, found, err := s.workspaceStore.GetKernelSupervisedChild(ctx, access.Frame.ID, frameID, access.UserID)
	if err == nil && !found && s.kernelManager != nil {
		interrupted := s.kernelManager.Interrupt(access.Frame.ID, frameID)
		if interrupted.Interrupted || interrupted.Dequeued {
			return map[string]any{
				"exec_id": frameID, "status": "stopped", "outcome": "interrupted",
				"via": interrupted.Via, "dequeued": interrupted.Dequeued,
			}
		}
	}
	if err != nil || !found {
		return map[string]any{"child_frame_id": frameID, "status": "failed", "error": "delegated child is unavailable"}
	}
	if kernelChildTerminal(child.Status) {
		if err := s.workspaceStore.CollectKernelChildLanding(context.Background(), access.Frame.ID, access.Frame.RootFrameID, access.UserID, child.FrameID); err != nil {
			return map[string]any{"child_frame_id": frameID, "status": "failed", "error": "delegated child result could not be discarded"}
		}
		status := kernelChildPublicStatus(child)
		output := child.Output
		if output == nil {
			output = map[string]any{}
		}
		result := map[string]any{
			"status": "already_terminal", "child_frame_id": frameID, "frame_id": frameID,
			"agent_name": child.AgentName, "child_status": status, "output_data": output,
			"message": fmt.Sprintf("Child is already %s; nothing to stop.", status),
		}
		return result
	}
	if strings.TrimSpace(reason) == "" {
		reason = "Parent requested stop"
	}
	stoppedTree, err := s.workspaceStore.StopKernelSupervisedChildTree(ctx, access.Frame.ID, frameID, access.UserID, reason)
	if err != nil {
		return map[string]any{"child_frame_id": frameID, "status": "failed", "error": "delegated child could not be stopped"}
	}
	if !stoppedTree.Applied {
		status := kernelChildPublicStatus(stoppedTree.Target)
		output := stoppedTree.Target.Output
		if output == nil {
			output = map[string]any{}
		}
		return map[string]any{
			"status": "already_terminal", "child_frame_id": frameID, "frame_id": frameID,
			"agent_name": stoppedTree.Target.AgentName, "child_status": status, "output_data": output,
			"message": fmt.Sprintf("Child is already %s; nothing to stop.", status),
		}
	}
	stoppedDescendants := make([]any, 0, len(stoppedTree.StoppedDescendants))
	for _, descendant := range stoppedTree.StoppedDescendants {
		s.cancelKernelChildRun(descendant.FrameID)
		_ = s.workspaceStore.CollectKernelChildLanding(context.Background(), descendant.ParentFrameID, descendant.RootFrameID, access.UserID, descendant.FrameID)
		stoppedDescendants = append(stoppedDescendants, map[string]any{"frame_id": descendant.FrameID, "agent_name": descendant.AgentName})
	}
	s.cancelKernelChildRun(frameID)
	if err := s.workspaceStore.CollectKernelChildLanding(context.Background(), access.Frame.ID, access.Frame.RootFrameID, access.UserID, stoppedTree.Target.FrameID); err != nil {
		return map[string]any{"child_frame_id": frameID, "status": "failed", "error": "delegated child result could not be discarded"}
	}
	result := map[string]any{
		"status": "stopped", "child_frame_id": frameID, "frame_id": frameID,
		"agent_name": stoppedTree.Target.AgentName, "stopped_descendants": stoppedDescendants,
	}
	if len(stoppedTree.Target.Output) > 0 {
		result["output_data"] = stoppedTree.Target.Output
	}
	if len(stoppedDescendants) > 0 {
		result["message"] = fmt.Sprintf("Child stopped along with %d descendant(s). All marked as cancelled (stopped by parent; will not auto-resume).", len(stoppedDescendants))
	} else {
		result["message"] = "Child stopped and marked as cancelled (stopped by parent; will not auto-resume)."
	}
	return result
}

func (s *Server) cancelKernelChildRun(frameID string) {
	if value, ok := kernelChildRuns.Load(kernelChildRunKey{server: s, frameID: frameID}); ok {
		value.(*kernelChildRun).cancel()
	}
}

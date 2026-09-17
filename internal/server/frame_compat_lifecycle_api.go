package server

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"log"
	"net/http"
	"net/url"
	"strings"
	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) handleCompatibilityFrameCancel(w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame) {
	if r.Method != http.MethodPost {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	result, err := s.cancelCompatibilityFrameTree(r.Context(), frame, r.URL.Query().Get("reason"))
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	for _, event := range result.Events {
		if err := s.publishWorkspaceEvent(event); err != nil {
			writeV11StoreError(w, fmt.Errorf("frame cancelled but event delivery failed: %w", err))
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"root_frame_id": result.RootFrameID, "cancelled_frames": result.CancelledFrameIDs,
	})
}

func (s *Server) cancelCompatibilityFrameTree(
	ctx context.Context,
	frame workspace.CompatibilityFrame,
	reason string,
) (workspace.CancelCompatibilityFrameResult, error) {
	// Request submission owns Frame creation, tree mutation, and Transcript
	// admission under this lock. Take the same authority before the tree
	// snapshot so cancellation cannot miss a child or observe a provisional
	// Frame without its canonical stream. Runner settlement is acquired only
	// after this lock; runner finalization releases settlement before advancing
	// the compatibility queue, so the order remains acyclic.
	s.compatRequestMu.Lock()
	defer s.compatRequestMu.Unlock()
	frames, err := s.workspaceStore.ListFramesForRoot(frame.RootFrameID)
	if err != nil {
		return workspace.CancelCompatibilityFrameResult{}, err
	}
	frameIDs := make([]string, 0, len(frames))
	for _, candidate := range frames {
		frameIDs = append(frameIDs, candidate.ID)
	}
	var result workspace.CancelCompatibilityFrameResult
	err = s.commitSessionRunCancellation(frameIDs, func(_ map[string]*transcriptRunnerAuthority) error {
		var cancelErr error
		if s.transcriptStore != nil {
			result, cancelErr = s.workspaceStore.CancelCompatibilityFrameTreeWithTranscript(ctx, frame.ID, reason)
		} else {
			result, cancelErr = s.workspaceStore.CancelCompatibilityFrameTree(frame.ID, reason)
		}
		return cancelErr
	})
	return result, err
}

func (s *Server) handleCompatibilityFrameResume(w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame) {
	if r.Method != http.MethodPost {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	if frame.ID != frame.RootFrameID || frame.ParentFrameID != "" {
		writeV11Detail(w, http.StatusNotFound, "Frame "+frame.ID+" not found")
		return
	}
	var input struct {
		VerifierMode *string `json:"verifier_mode"`
		MemoryMode   *string `json:"memory_mode"`
		PlanMode     *bool   `json:"plan_mode"`
		UltraMode    *bool   `json:"ultra_mode"`
		TargetAgent  *string `json:"target_agent"`
		Model        *string `json:"model"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := decodeAgentCompatJSON(r, &input); err != nil {
			writeV11Detail(w, http.StatusBadRequest, "Invalid resume request: "+err.Error())
			return
		}
	}
	for name, value := range map[string]*string{
		"verifier_mode": input.VerifierMode,
		"memory_mode":   input.MemoryMode,
	} {
		if value != nil && *value != "off" && *value != "on" {
			writeV11Detail(w, http.StatusBadRequest, name+" must be off or on")
			return
		}
	}
	if input.TargetAgent != nil {
		targetAgent := normalizeBundledAgentName(*input.TargetAgent)
		found := false
		if s.agentCatalog != nil {
			_, found = s.agentCatalog.Agent(targetAgent)
		}
		if !found {
			if _, profileFound, err := s.workspaceStore.GetAgent(compatAgentUserID(r), targetAgent); err != nil {
				writeV11StoreError(w, err)
				return
			} else {
				found = profileFound
			}
		}
		if !found {
			writeV11Detail(w, http.StatusBadRequest, "Agent "+targetAgent+" not found in registry")
			return
		}
		input.TargetAgent = &targetAgent
	}
	allowProcessingInterrupted := false
	if s.transcriptStore != nil {
		stream, streamFound, err := s.resolveTranscriptFrameStream(r.Context(), frame.ID)
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		if streamFound && frame.Status == workspace.FrameStatusProcessing {
			if _, hasInterruption, interruptionErr := s.transcriptStore.LatestRunnerInterruption(
				r.Context(), stream.UID, compatAgentUserID(r),
			); interruptionErr != nil {
				writeV11StoreError(w, interruptionErr)
				return
			} else if hasInterruption {
				allowProcessingInterrupted = true
			}
		}
	}
	result, err := s.workspaceStore.ResumeCompatibilityFrameConversation(frame.ID, workspace.ResumeCompatibilityFrameInput{
		VerifierMode:               input.VerifierMode,
		MemoryMode:                 input.MemoryMode,
		PlanMode:                   input.PlanMode,
		UltraMode:                  input.UltraMode,
		TargetAgent:                input.TargetAgent,
		Model:                      input.Model,
		AllowProcessingInterrupted: allowProcessingInterrupted,
	})
	if err != nil {
		message := strings.ToLower(err.Error())
		switch {
		case strings.Contains(message, "does not exist"):
			writeV11Detail(w, http.StatusNotFound, "Frame "+frame.ID+" not found")
		case strings.Contains(message, "not in a resumable state"), strings.Contains(message, "no resumable"):
			status := http.StatusBadRequest
			if strings.Contains(message, "processing") || strings.Contains(message, "running") {
				status = http.StatusConflict
			}
			writeV11Detail(w, status, err.Error())
		default:
			writeV11StoreError(w, err)
		}
		return
	}
	if result.Event == nil {
		writeV11StoreError(w, fmt.Errorf("frame resumed without a durable dispatch event"))
		return
	}
	// Explicit Continue must make an existing fenced recovery dispatch
	// claimable now. ResumeCompatibilityFrameConversation intentionally returns
	// the active dispatch when the logical task is already processing; without
	// consuming its notBefore fence the API would report success while leaving
	// the task visibly failed until the automatic backstop elapsed.
	if wakeEvent, woken, wakeErr := s.workspaceStore.WakeCompatibilityFrameResumeDispatch(result.Event.ID); wakeErr != nil {
		writeV11StoreError(w, fmt.Errorf("frame resumed but dispatch wake failed: %w", wakeErr))
		return
	} else if woken {
		if publishErr := s.publishWorkspaceEvent(wakeEvent); publishErr != nil {
			writeV11StoreError(w, fmt.Errorf("frame resumed but dispatch wake delivery failed: %w", publishErr))
			return
		}
	}
	if err := s.registerFrameResumeDispatch(result.Event.ID, defaultFrameResumeReservationTTL); err != nil {
		writeV11StoreError(w, fmt.Errorf("frame resumed but dispatch registration failed: %w", err))
		return
	}
	if err := s.publishWorkspaceEvent(*result.Event); err != nil {
		writeV11StoreError(w, fmt.Errorf("frame resumed but event delivery failed: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"root_frame_id": result.RootFrameID,
		"resumed_frames": []map[string]any{{
			"frame_id": result.ResumedFrameID, "agent_name": result.AgentName,
		}},
		"leaf_frames_ready": result.LeafFramesReady,
	})
}

func (s *Server) handleCompatibilityQueuedMessage(w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame, rawQueuedID string) {
	if r.Method != http.MethodDelete {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	queuedID, err := url.PathUnescape(rawQueuedID)
	if err != nil {
		writeV11Detail(w, http.StatusBadRequest, "Invalid queued message id: "+rawQueuedID)
		return
	}
	queuedID = strings.TrimSpace(queuedID)
	if _, err := uuid.Parse(queuedID); err != nil {
		writeV11Detail(w, http.StatusBadRequest, "Invalid queued message id: "+queuedID)
		return
	}
	event, err := s.workspaceStore.RetractCompatibilityQueuedMessage(frame.ID, queuedID)
	if err != nil {
		if errors.Is(err, workspace.ErrCompatibilityQueuedMessageTooLate) {
			writeV11Detail(w, http.StatusConflict, "Queued message "+queuedID+" was already picked up for delivery on frame "+frame.ID)
			return
		}
		if strings.Contains(err.Error(), "does not exist") {
			writeV11Detail(w, http.StatusNotFound, "Queued message "+queuedID+" not found on frame "+frame.ID)
			return
		}
		writeV11StoreError(w, err)
		return
	}
	if err := s.publishWorkspaceEvent(event); err != nil {
		writeV11StoreError(w, fmt.Errorf("queued message retracted but event delivery failed: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"frame_id": frame.ID, "id": queuedID, "removed": true,
	})
}

func compatibilityReadCursorResponse(cursor workspace.FrameReadCursor) map[string]any {
	return map[string]any{
		"root_frame_id": cursor.RootFrameID,
		"message_uuid":  nullableCompatibilityString(cursor.MessageUUID),
		"message_index": cursor.MessageIndex,
		"updated_at":    cursor.UpdatedAt.UTC(),
	}
}

func (s *Server) handleCompatibilityFrameRecord(
	w http.ResponseWriter,
	r *http.Request,
	frame workspace.CompatibilityFrame,
	userID string,
) {
	switch r.Method {
	case http.MethodGet:
		projection, err := s.compatibilityFrameResponse(frame, true, map[string]bool{})
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, projection)
	case http.MethodPatch:
		var input struct {
			Name        *string `json:"name"`
			TaskSummary *string `json:"task_summary"`
		}
		if err := decodeAgentCompatJSON(r, &input); err != nil {
			writeV11Detail(w, http.StatusBadRequest, "Invalid frame update: "+err.Error())
			return
		}
		updated, err := s.workspaceStore.UpdateCompatibilityFrame(frame.ID, workspace.UpdateCompatibilityFrameInput{
			Name: input.Name, TaskSummary: input.TaskSummary,
		})
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		if _, err := s.publishProjectEvent(frame.ProjectID, "frame_update", map[string]any{
			"action": "frame_metadata_updated", "frame_id": frame.ID, "root_frame_id": frame.RootFrameID,
		}); err != nil {
			writeV11StoreError(w, fmt.Errorf("frame updated but event delivery failed: %w", err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"root_frame_id": updated.RootFrameID,
			"name":          nullableCompatibilityString(updated.Name),
			"task_summary":  nullableCompatibilityString(updated.TaskSummary),
		})
	case http.MethodDelete:
		runtimeCloseCtx, cancelRuntimeClose := compatibilityRuntimeCleanupContext(r.Context())
		defer cancelRuntimeClose()
		cleanupWarnings := s.stopCompatibilityFrameRuntime(runtimeCloseCtx, frame.RootFrameID)
		deleted, err := s.workspaceStore.DeleteCompatibilityFrameTree(frame.ID, userID, frame.IncarnationID)
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		cleanupWarnings = append(cleanupWarnings, s.stopCompatibilityFrameRuntime(runtimeCloseCtx, deleted.RootFrameID)...)
		cleanupWarnings = append(cleanupWarnings, s.removeCompatibilityFrameRuntime(deleted.RootFrameID)...)
		if deleted.ArtifactCleanupFailures > 0 {
			log.Printf("compatibility frame delete committed with artifact cleanup failures: frame_id=%s root_frame_id=%s failures=%d", frame.ID, deleted.RootFrameID, deleted.ArtifactCleanupFailures)
		}
		if len(cleanupWarnings) > 0 {
			log.Printf("compatibility frame delete committed with runtime cleanup warnings: frame_id=%s root_frame_id=%s failures=%d errors=%v", frame.ID, deleted.RootFrameID, len(cleanupWarnings), errors.Join(cleanupWarnings...))
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"frame_id": deleted.FrameID, "root_frame_id": deleted.RootFrameID,
			"frames_deleted": deleted.FramesDeleted, "artifacts_deleted": deleted.ArtifactsDeleted,
			"artifact_cleanup_failures": deleted.ArtifactCleanupFailures,
			"runtime_cleanup_warnings":  len(cleanupWarnings),
		})
	default:
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

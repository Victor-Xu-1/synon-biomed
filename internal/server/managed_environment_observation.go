package server

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/toolprogress"
)

func (s *Server) recoverManagedEnvironmentObservations() {
	if s.transcriptStore == nil || s.workspaceStore == nil {
		return
	}
	for {
		events, err := s.transcriptStore.RecoverInterruptedToolObservations(context.Background(), s.kernelOperationBootID)
		if err != nil {
			log.Printf("recover background tool observations: %v", err)
			return
		}
		for _, event := range events {
			var payload map[string]any
			if err := json.Unmarshal(event.PayloadJSON, &payload); err != nil {
				log.Printf("decode recovered operation: %v", err)
				continue
			}
			stream, err := s.transcriptStore.GetStream(context.Background(), event.StreamUID, stringValue(payload["observerOwnerId"]))
			if err != nil {
				log.Printf("resolve recovered operation stream: %v", err)
				continue
			}
			id := uuid.NewSHA1(uuid.NameSpaceOID, []byte("synon-environment:"+stream.FrameID+":"+stringValue(payload["toolCallId"]))).String()
			_, _, err = s.workspaceStore.CreateNotification(context.Background(), workspace.CreateNotificationInput{ID: id, SenderFrameID: stream.FrameID, RecipientFrameID: stream.FrameID, RootFrameID: stream.RootFrameID, OwnerUserID: stream.OwnerID, NotificationType: "cell_result", Payload: mapValue(payload["toolResult"])})
			if err != nil {
				log.Printf("persist recovered operation notification: %v", err)
			}
		}
		if len(events) < 100 {
			return
		}
	}
}

// Both ordinary and detached observations serialize through this whitelist.
// No guessed time/percentage, raw installer log, or execution token is public.
func publicToolProgressPayload(update toolprogress.Update, elapsed time.Duration) map[string]any {
	update = toolprogress.Normalize(update)
	result := map[string]any{"phase": update.Phase, "indeterminate": update.Indeterminate, "elapsedMs": elapsed.Milliseconds()}
	if update.Phase == "" {
		result["phase"] = "processing"
	}
	if update.Process != "" {
		result["process"] = update.Process
	}
	if update.Message != "" {
		result["message"] = strings.TrimSpace(truncateUTF8ByBytes(update.Message, 240))
	}
	if update.PhasePercent != nil {
		result["phasePercent"] = *update.PhasePercent
	}
	if update.BytesPerSecond != nil && *update.BytesPerSecond <= 1e15 {
		result["bytesPerSecond"] = *update.BytesPerSecond
	}
	if update.BytesCompleted != nil && *update.BytesCompleted <= 9007199254740991 {
		result["bytesCompleted"] = *update.BytesCompleted
	}
	if update.BytesTotal != nil && *update.BytesTotal <= 9007199254740991 {
		result["bytesTotal"] = *update.BytesTotal
	}
	if update.CompletedItems != nil && *update.CompletedItems <= 9007199254740991 {
		result["completedItems"] = *update.CompletedItems
	}
	if update.TotalItems != nil && *update.TotalItems <= 9007199254740991 {
		result["totalItems"] = *update.TotalItems
	}
	return result
}

func supervisedEnvironmentAccepted(observer *transcriptstore.ToolOperationObserver, operationID, notificationID, toolName string) map[string]any {
	result := map[string]any{"status": "running", "tool": toolName, "operation_id": operationID, "notification_id": notificationID, "recovery": "use wait_for_notification for the durable result"}
	if observer != nil {
		result["stream_progress"] = true
		result["operation_id"] = observer.OperationID
	}
	return result
}

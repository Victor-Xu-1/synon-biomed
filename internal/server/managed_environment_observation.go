package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"synon-go/internal/agentruntime"
	kernelruntime "synon-go/internal/kernel"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/toolprogress"
)

func (s *Server) beginManagedEnvironmentObservation(ctx context.Context, call agentruntime.ToolCall, toolName string) (*transcriptstore.ToolOperationObserver, bool, error) {
	run, _ := ctx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun)
	if run == nil || run.Transcript == nil {
		return nil, true, nil
	}
	observer, created, err := s.transcriptStore.BeginToolOperationObservation(ctx, run.Transcript.Claim, call.ID, toolName, s.kernelOperationBootID)
	if err != nil {
		return nil, false, err
	}
	s.signalTranscriptWebDelivery()
	return &observer, created, nil
}

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

func (s *Server) observeManagedEnvironmentOperation(ctx context.Context, observer *transcriptstore.ToolOperationObserver, toolName string, metadata map[string]any, operation func(context.Context) (kernelruntime.ManagedEnvironment, error)) (kernelruntime.ManagedEnvironment, error) {
	if observer == nil {
		return operation(ctx)
	}
	ordinal := int64(0)
	publish := func(status string, details map[string]any) {
		_, err := s.transcriptStore.AppendToolOperationObservation(context.Background(), *observer, status, ordinal+1, details)
		if err != nil {
			log.Printf("background tool observation %s: %v", observer.OperationID, err)
			return
		}
		ordinal++
		s.signalTranscriptWebDelivery()
	}
	started := time.Now()
	environment, err := toolprogress.Observe(ctx, 0, operation, func(progress *toolprogress.Update, elapsed time.Duration, _ int) {
		if progress != nil {
			publish("running", map[string]any{"progress": publicToolProgressPayload(*progress, elapsed)})
		}
	})
	status, result := managedEnvironmentTerminalObservation(toolName, environment, err, metadata)
	result["operation_id"] = observer.OperationID
	publish(status, map[string]any{"toolResult": result, "progress": map[string]any{"phase": "operation_" + status, "indeterminate": false, "elapsedMs": time.Since(started).Milliseconds()}})
	return environment, err
}

func managedEnvironmentTerminalObservation(toolName string, environment kernelruntime.ManagedEnvironment, err error, metadata map[string]any) (string, map[string]any) {
	if err == nil {
		return "completed", managedEnvironmentOperationReceipt(toolName, "completed", environment, metadata)
	}
	result := managedEnvironmentFailureReceipt(toolName, err, metadata)
	if errors.Is(err, context.Canceled) {
		result["status"] = "cancelled"
		return "cancelled", result
	}
	return "failed", result
}

// Both ordinary and detached observations serialize through this whitelist.
// No guessed time/percentage, raw installer log, or execution token is public.
func publicToolProgressPayload(update toolprogress.Update, elapsed time.Duration) map[string]any {
	update = toolprogress.Normalize(update)
	result := map[string]any{"phase": update.Phase, "indeterminate": update.Indeterminate, "elapsedMs": elapsed.Milliseconds()}
	if update.Phase == "" {
		result["phase"] = "processing"
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

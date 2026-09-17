package server

import (
	"net/http"
	"runtime"
	"strconv"
	"strings"

	"synon-go/internal/buildinfo"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/tools/shellops"
)

const maxOutboxDiagnosticErrorRunes = 1024

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if _, ok := attachmentUserID(w, r); !ok {
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(s.httpMetrics.Prometheus()))
}

func (s *Server) handleRuntimeDiagnostics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if _, ok := attachmentUserID(w, r); !ok {
		return
	}
	errorsByArea := map[string]string{}
	result := map[string]any{
		"release":      buildinfo.Release(),
		"http":         s.httpMetrics.Snapshot(),
		"shellSandbox": shellops.SandboxStatus(),
		"environments": s.environmentStatus(false),
		"go": map[string]any{
			"version": runtime.Version(), "platform": runtime.GOOS + "/" + runtime.GOARCH,
			"goroutines": runtime.NumGoroutine(),
		},
	}
	if s.workspaceStore == nil {
		errorsByArea["workspace"] = "workspace store is not configured"
	} else {
		if status, err := s.workspaceStore.SchemaStatus(r.Context()); err != nil {
			errorsByArea["schema"] = err.Error()
		} else {
			result["schema"] = status
		}
		if status, err := s.workspaceStore.OutboxStats(r.Context()); err != nil {
			errorsByArea["outbox"] = err.Error()
		} else {
			result["outbox"] = status
		}
		if status, err := s.workspaceStore.RecoveryStatus(r.Context()); err != nil {
			errorsByArea["recovery"] = err.Error()
		} else {
			result["recovery"] = status
		}
	}
	if reason := s.kernelUnavailableReason(); reason != "" {
		errorsByArea["kernel"] = reason
	}
	result["errors"] = errorsByArea
	writeWorkspaceJSON(w, http.StatusOK, result)
}

func (s *Server) handleOutboxDeadLetters(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if _, ok := attachmentUserID(w, r); !ok {
		return
	}
	if s.workspaceStore == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "workspace store is not configured"})
		return
	}
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 256 {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"error": "limit must be between 1 and 256"})
			return
		}
		limit = parsed
	}
	events, err := s.workspaceStore.ListOutboxDeadLetters(r.Context(), limit)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	items := make([]map[string]any, 0, len(events))
	for _, event := range events {
		items = append(items, outboxDeadLetterProjection(event))
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (s *Server) handleOutboxDeadLetterMutation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if _, ok := attachmentUserID(w, r); !ok {
		return
	}
	if s.workspaceStore == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "workspace store is not configured"})
		return
	}
	tail := strings.TrimPrefix(r.URL.Path, "/api/go/diagnostics/outbox/dead-letters/")
	parts := strings.Split(tail, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || parts[1] != "requeue" {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"error": "outbox recovery action not found"})
		return
	}
	if err := s.workspaceStore.RequeueOutboxDeadLetter(r.Context(), parts[0]); err != nil {
		writeWorkspaceJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"eventId": parts[0], "status": "pending"})
}

func outboxDeadLetterProjection(event workspace.OutboxEvent) map[string]any {
	return map[string]any{
		"sequence": event.Sequence, "id": event.ID, "topic": event.Topic,
		"partitionKey": event.PartitionKey, "type": event.Type,
		"aggregateType": event.AggregateType, "aggregateId": event.AggregateID,
		"attemptCount": event.AttemptCount, "maxAttempts": event.MaxAttempts,
		"lastError": truncateFeedbackRunes(redactFeedbackString(event.LastError), maxOutboxDiagnosticErrorRunes), "occurredAt": event.OccurredAt,
		"deadLetteredAt": event.DeadLetteredAt,
	}
}

package workspace

import (
	"context"
	"database/sql"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

const maxComputeLogChunkEventBytes = 32 << 10

func (s *Store) enqueueComputeJobUpdateTx(ctx context.Context, tx workspaceTransaction, userID string, job ComputeJob) error {
	rootFrameID := computeOptionalString(job.RootFrameID)
	frameID := computeOptionalString(job.FrameID)
	_, err := s.enqueueRealtimeOutboxTransaction(ctx, tx, RealtimeEventInput{
		ID:          "compute-job:" + job.JobID + ":state:" + job.State,
		UserID:      strings.TrimSpace(userID),
		ProjectID:   job.ProjectID,
		RootFrameID: rootFrameID,
		FrameID:     frameID,
		Type:        "compute_job_update",
		Payload: map[string]any{
			"job_id":        job.JobID,
			"project_id":    job.ProjectID,
			"root_frame_id": rootFrameID,
			"frame_id":      frameID,
			"provider":      job.Provider,
			"state":         job.State,
			"intent":        job.Intent,
		},
	}, "", "")
	return err
}

func (s *Store) enqueueComputeJobLogTx(ctx context.Context, tx *sql.Tx, userID, stream, text string, job ComputeJob) error {
	streamName := "out"
	if stream == "stderr" {
		streamName = "err"
	}
	rootFrameID := computeOptionalString(job.RootFrameID)
	frameID := computeOptionalString(job.FrameID)
	_, err := s.EnqueueRealtimeOutboxTx(ctx, tx, RealtimeEventInput{
		ID:          uuid.NewString(),
		UserID:      strings.TrimSpace(userID),
		ProjectID:   job.ProjectID,
		RootFrameID: rootFrameID,
		FrameID:     frameID,
		Type:        "compute_job_log_chunk",
		Payload: map[string]any{
			"job_id":        job.JobID,
			"project_id":    job.ProjectID,
			"root_frame_id": rootFrameID,
			"frame_id":      frameID,
			"stream":        streamName,
			"chunk":         boundedComputeLogChunk(text),
		},
	}, "")
	return err
}

func (s *Store) enqueueManagedEndpointTx(ctx context.Context, tx *sql.Tx, eventType, action string, endpoint ManagedEndpoint, transcript string) error {
	payload := map[string]any{
		"name":   endpoint.Name,
		"state":  endpoint.State,
		"port":   endpoint.Port,
		"error":  endpoint.LastError,
		"action": action,
	}
	switch eventType {
	case "managed_endpoint_transcript":
		payload["phase"] = "stop"
		payload["text"] = boundedComputeLogChunk(transcript)
		payload["done"] = true
	case "managed_endpoint_removed":
		delete(payload, "state")
		delete(payload, "port")
		delete(payload, "error")
	}
	_, err := s.EnqueueRealtimeOutboxTx(ctx, tx, RealtimeEventInput{
		ID:      uuid.NewString(),
		UserID:  strings.TrimSpace(endpoint.RegisteredBy),
		Type:    eventType,
		Payload: payload,
	}, "")
	return err
}

func computeOptionalString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func boundedComputeLogChunk(value string) string {
	data := []byte(value)
	if len(data) <= maxComputeLogChunkEventBytes {
		return value
	}
	start := len(data) - maxComputeLogChunkEventBytes
	for start < len(data) && !utf8.RuneStart(data[start]) {
		start++
	}
	return string(data[start:])
}

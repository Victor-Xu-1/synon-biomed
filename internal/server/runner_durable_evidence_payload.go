package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

// restoreDurableEvidencePayload is for server-side receipt consumers only.
// Model replay still uses the small descriptor and its normal paginated reader.
// A preview is navigation data and cannot replace the actual tool result in
// completion/provenance checks after a process or execution-unit restart.
func (s *Server) restoreDurableEvidencePayload(ctx context.Context, stream transcriptstore.Stream, checkpoint sessionRunnerDurableToolCheckpoint, completedEventID int64, result string) (content string, resultErr error) {
	var descriptor agentruntime.LargeToolResultDescriptor
	if json.Unmarshal([]byte(result), &descriptor) != nil || !isRunnerLargeToolResultArtifactID(descriptor.ArtifactID) {
		return result, nil
	}
	if s.workspaceStore == nil || descriptor.VersionID == "" {
		return "", errRunnerLargeToolResultAuthority
	}
	// Validate the immutable receipt before opening its blob. A missing payload
	// must never hide a foreign owner or a conflicting frame/call/version.
	record, found, err := s.workspaceStore.GetRunnerLargeToolResult(ctx, descriptor.ArtifactID, stream.OwnerID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", errRunnerLargeToolResultAuthority
	}
	if record.StreamUID != stream.UID || record.OwnerUserID != stream.OwnerID || record.ProjectID != stream.ProjectID ||
		record.RootFrameID != stream.RootFrameID || record.FrameID != stream.FrameID || record.ArtifactID != descriptor.ArtifactID ||
		record.VersionID != descriptor.VersionID ||
		record.ToolCallID != checkpoint.ToolCallID || record.ToolName != checkpoint.ToolName || record.ContentSHA256 != descriptor.SHA256 ||
		record.SizeBytes != descriptor.SizeBytes || record.ContentType != descriptor.ContentType || record.SourceEventID >= completedEventID {
		return "", errRunnerLargeToolResultConflict
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	_, reader, found, err := s.workspaceStore.OpenRunnerLargeToolResultContent(ctx, record.VersionID, stream.OwnerID)
	if err != nil {
		if errors.Is(err, workspace.ErrRunnerLargeToolResultUnavailable) {
			return "", errRunnerLargeToolResultUnavailable
		}
		return "", err
	}
	if !found {
		return "", errRunnerLargeToolResultAuthority
	}
	defer func() { resultErr = errors.Join(resultErr, reader.Close()) }()
	raw, err := io.ReadAll(io.LimitReader(reader, record.SizeBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(raw)) != record.SizeBytes || !json.Valid(raw) {
		return "", errRunnerLargeToolResultConflict
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != record.ContentSHA256 {
		return "", errRunnerLargeToolResultConflict
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// Host MCP adapters return JSON text as a string. Keep those exact encoded
	// bytes and their digest in storage, then use the same evidence projection
	// as an inline result so structured records survive externalization.
	if decoded, valid := sessionRunnerDurableToolResult(raw); valid {
		return decoded, nil
	}
	return string(raw), nil
}

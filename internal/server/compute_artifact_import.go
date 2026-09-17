package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	workspace "synon-go/internal/persistence/workspace"
)

func computeArtifactMutation(r *http.Request, ownerUserID, scope string) (context.Context, string) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		key = r.Header.Get("X-Synon-Idempotency-Key")
	}
	if key == "" {
		key = uuid.NewString()
	}
	identity := strings.Join([]string{"synon-compute-artifact-v1", ownerUserID, scope, key}, "\x00")
	artifactID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(identity)).String()
	return workspace.WithMutationIdempotencyKey(r.Context(), key), artifactID
}

func computeArtifactSourceInteraction(kind, provider, sourcePath string) []map[string]any {
	digest := sha256.Sum256([]byte(strings.Join([]string{kind, provider, sourcePath}, "\x00")))
	return []map[string]any{{
		"source_kind":        kind,
		"source_identity_v1": hex.EncodeToString(digest[:]),
	}}
}

func computeArtifactImportResponse(artifact workspace.Artifact, version workspace.ArtifactVersion, rootID, filePath string) map[string]any {
	return map[string]any{
		"id": artifact.ID, "version_id": version.ID, "version_number": version.VersionNumber,
		"project_id": artifact.ProjectID, "root_frame_id": rootID, "frame_id": nil,
		"creating_frame_id": nil, "filename": artifact.Name, "content_type": artifact.Kind,
		"size_bytes": version.SizeBytes, "created_at": version.CreatedAt.Format(time.RFC3339Nano),
		"is_user_upload": true, "file_path": filePath, "agent_name": nil,
		"language": nil, "is_intermediate": false, "priority": "unknown", "creating_version_id": nil,
	}
}

func writeComputeArtifactMutationError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, workspace.ErrInvalidMutationIdempotencyKey):
		status = http.StatusBadRequest
	case errors.Is(err, workspace.ErrMutationIdempotencyConflict):
		status = http.StatusConflict
	case strings.Contains(err.Error(), "does not exist"), strings.Contains(err.Error(), "owner"):
		status = http.StatusNotFound
	case strings.Contains(err.Error(), "exceeds"):
		status = http.StatusRequestEntityTooLarge
	}
	writeAgentCompatError(w, status, err)
}

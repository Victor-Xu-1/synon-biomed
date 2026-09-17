package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

type webConversationArtifactReferenceInput struct {
	ArtifactID string `json:"artifact_id"`
	VersionID  string `json:"version_id"`
}

func decodeUserArtifactReferences(value any) ([]transcriptstore.UserArtifactReference, error) {
	if value == nil {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, transcriptstore.ErrEventConflict
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var refs []transcriptstore.UserArtifactReference
	if err := decoder.Decode(&refs); err != nil {
		return nil, transcriptstore.ErrEventConflict
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, transcriptstore.ErrEventConflict
	}
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if strings.TrimSpace(ref.ArtifactID) == "" || strings.TrimSpace(ref.VersionID) == "" ||
			strings.TrimSpace(ref.Filename) == "" || strings.TrimSpace(ref.ContentType) == "" ||
			ref.SizeBytes < 0 || strings.TrimSpace(ref.Checksum) == "" {
			return nil, transcriptstore.ErrEventConflict
		}
		key := ref.ArtifactID + "\x00" + ref.VersionID
		if _, duplicate := seen[key]; duplicate {
			return nil, transcriptstore.ErrEventConflict
		}
		seen[key] = struct{}{}
	}
	return refs, nil
}

func transcriptUserArtifactReferences(payload map[string]any) ([]map[string]any, error) {
	refs, err := decodeUserArtifactReferences(payload["artifactRefs"])
	if err != nil {
		return nil, err
	}
	projected := make([]map[string]any, 0, len(refs))
	for _, ref := range refs {
		projected = append(projected, map[string]any{
			"artifact_id": ref.ArtifactID, "version_id": ref.VersionID, "relation": "attached",
			"filename": ref.Filename, "content_type": ref.ContentType, "size_bytes": ref.SizeBytes,
			"checksum": ref.Checksum,
		})
	}
	return projected, nil
}

func (s *Server) validateWebConversationArtifactReferences(
	ctx context.Context,
	ownerID string,
	frame workspace.CompatibilityFrame,
	values []webConversationArtifactReferenceInput,
	messageContext string,
) ([]transcriptstore.UserArtifactReferenceInput, error) {
	if messageContext == "onboarding_first_task" && len(values) == 0 {
		return nil, &webConversationRequestError{
			Status: http.StatusBadRequest, Detail: "onboarding_first_task requires an onboarding profile artifact",
		}
	}
	if len(values) == 0 {
		return nil, nil
	}
	refs := make([]transcriptstore.UserArtifactReferenceInput, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		artifactID := strings.TrimSpace(value.ArtifactID)
		versionID := strings.TrimSpace(value.VersionID)
		if artifactID == "" || versionID == "" {
			return nil, &webConversationRequestError{Status: http.StatusBadRequest, Detail: "artifact_id and version_id are required"}
		}
		key := artifactID + "\x00" + versionID
		if _, duplicate := seen[key]; duplicate {
			return nil, &webConversationRequestError{Status: http.StatusBadRequest, Detail: "artifact_refs must be unique"}
		}
		seen[key] = struct{}{}
		refs = append(refs, transcriptstore.UserArtifactReferenceInput{ArtifactID: artifactID, VersionID: versionID})
	}
	if s == nil || s.transcriptStore == nil {
		return nil, transcriptWebAuthorityUnavailableError()
	}
	stream, err := s.ensureTranscriptFrameStream(ctx, ownerID, frame.ID)
	if err != nil {
		return nil, err
	}
	if stream.ProjectID != frame.ProjectID || stream.FrameID != frame.ID {
		return nil, &webConversationRequestError{Status: http.StatusNotFound, Detail: "artifact reference not found"}
	}
	canonical, err := s.transcriptStore.ValidateUserArtifactReferences(ctx, stream.UID, ownerID, refs)
	if err != nil {
		if errors.Is(err, transcriptstore.ErrArtifactMissing) || errors.Is(err, transcriptstore.ErrArtifactMismatch) ||
			errors.Is(err, transcriptstore.ErrOwnerMismatch) {
			return nil, &webConversationRequestError{Status: http.StatusNotFound, Detail: "artifact reference not found"}
		}
		return nil, transcriptWebStorageError(err)
	}
	if messageContext == "onboarding_first_task" &&
		(canonical[0].Filename != "onboarding-profile.md" || canonical[0].ContentType != "text/markdown") {
		return nil, &webConversationRequestError{
			Status: http.StatusBadRequest, Detail: "onboarding_first_task requires onboarding-profile.md",
		}
	}
	return refs, nil
}

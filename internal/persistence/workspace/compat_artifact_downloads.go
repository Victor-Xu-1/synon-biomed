package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// CompatibilityArtifactDownloadMetadata is the v1.1 sidecar projection used
// by single-artifact and single-version metadata downloads.
type CompatibilityArtifactDownloadMetadata struct {
	ArtifactID       string
	VersionID        string
	VersionNumber    int
	Filename         string
	ContentType      string
	SizeBytes        int64
	CreatedAt        time.Time
	AgentName        *string
	IsUserUpload     bool
	Checksum         *string
	ReproductionCode *string
	Environment      any
}

const compatibilityArtifactDownloadMetadataSelect = `
	SELECT a.id, v.id, v.version_number, a.name,
		COALESCE(NULLIF(p.content_type, ''), a.kind),
		CASE WHEN COALESCE(v.storage_path, '') = '' THEN length(v.content) ELSE v.size_bytes END,
		v.created_at, p.agent_name, COALESCE(m.is_user_upload, 0),
		NULLIF(v.content_sha256, ''), p.extracted_code, p.environment_snapshot
	FROM artifact_versions AS v
	JOIN artifacts AS a ON a.id = v.artifact_id
	JOIN projects AS project ON project.id = a.project_id
	LEFT JOIN artifact_runtime_metadata AS m ON m.artifact_id = a.id
	LEFT JOIN artifact_version_provenance AS p ON p.version_id = v.id`

func (s *Store) GetCompatibilityCurrentArtifactDownloadMetadata(
	ctx context.Context, ownerUserID, artifactID string,
) (CompatibilityArtifactDownloadMetadata, bool, error) {
	return s.getCompatibilityArtifactDownloadMetadata(ctx,
		compatibilityArtifactDownloadMetadataSelect+`
		WHERE project.user_id = ? AND a.id = ? AND v.version_number = a.current_version_number`,
		ownerUserID, artifactID)
}

func (s *Store) GetCompatibilityVersionDownloadMetadata(
	ctx context.Context, ownerUserID, versionID string,
) (CompatibilityArtifactDownloadMetadata, bool, error) {
	return s.getCompatibilityArtifactDownloadMetadata(ctx,
		compatibilityArtifactDownloadMetadataSelect+` WHERE project.user_id = ? AND v.id = ?`,
		ownerUserID, versionID)
}

func (s *Store) getCompatibilityArtifactDownloadMetadata(
	ctx context.Context, query, ownerUserID, resourceID string,
) (CompatibilityArtifactDownloadMetadata, bool, error) {
	if s == nil || s.db == nil {
		return CompatibilityArtifactDownloadMetadata{}, false, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ownerUserID, resourceID = strings.TrimSpace(ownerUserID), strings.TrimSpace(resourceID)
	if ownerUserID == "" || resourceID == "" {
		return CompatibilityArtifactDownloadMetadata{}, false, nil
	}
	var metadata CompatibilityArtifactDownloadMetadata
	var agentName, checksum, reproductionCode, environment sql.NullString
	err := s.db.QueryRowContext(ctx, query, ownerUserID, resourceID).Scan(
		&metadata.ArtifactID, &metadata.VersionID, &metadata.VersionNumber,
		&metadata.Filename, &metadata.ContentType, &metadata.SizeBytes,
		&metadata.CreatedAt, &agentName, &metadata.IsUserUpload,
		&checksum, &reproductionCode, &environment,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return CompatibilityArtifactDownloadMetadata{}, false, nil
	}
	if err != nil {
		return CompatibilityArtifactDownloadMetadata{}, false, fmt.Errorf("get compatibility artifact download metadata: %w", err)
	}
	metadata.AgentName = nullableStringPointer(agentName)
	metadata.Checksum = nullableStringPointer(checksum)
	metadata.ReproductionCode = nullableStringPointer(reproductionCode)
	if environment.Valid && strings.TrimSpace(environment.String) != "" {
		if err := json.Unmarshal([]byte(environment.String), &metadata.Environment); err != nil {
			return CompatibilityArtifactDownloadMetadata{}, false, fmt.Errorf("decode artifact environment snapshot: %w", err)
		}
	}
	return metadata, true, nil
}

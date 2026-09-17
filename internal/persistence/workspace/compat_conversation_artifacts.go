package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type CompatibilityConversationArtifact struct {
	ID                     string    `json:"id"`
	VersionID              string    `json:"version_id"`
	VersionNumber          int       `json:"version_number"`
	ProjectID              string    `json:"project_id"`
	RootFrameID            *string   `json:"root_frame_id"`
	FrameID                *string   `json:"frame_id"`
	CreatingFrameID        *string   `json:"creating_frame_id"`
	Filename               string    `json:"filename"`
	ContentType            string    `json:"content_type"`
	SizeBytes              int64     `json:"size_bytes"`
	CreatedAt              time.Time `json:"created_at"`
	Checksum               *string   `json:"checksum"`
	FilePath               string    `json:"file_path"`
	IsUserUpload           bool      `json:"is_user_upload"`
	AgentName              *string   `json:"agent_name"`
	Language               *string   `json:"language"`
	IsIntermediate         bool      `json:"is_intermediate"`
	RetentionMode          string    `json:"retention_mode"`
	RefHostPath            *string   `json:"ref_host_path"`
	Priority               string    `json:"priority"`
	CreatingVersionID      *string   `json:"creating_version_id"`
	SupersededByArtifactID *string   `json:"superseded_by_artifact_id"`
}

type CompatibilityArtifactVersionReference struct {
	ArtifactID string `json:"artifact_id"`
	VersionID  string `json:"version_id"`
}

var ErrCompatibilityArtifactReferenceNotFound = errors.New("artifact reference not found")

const compatibilityConversationArtifactSelectPrefix = `
	SELECT a.id, v.id, v.version_number, a.project_id,
		m.root_frame_id, COALESCE(NULLIF(m.frame_id, ''), p.frame_id),
		COALESCE((SELECT first_p.frame_id
		 FROM artifact_versions first_v
		 LEFT JOIN artifact_version_provenance first_p ON first_p.version_id = first_v.id
		 WHERE first_v.artifact_id = a.id
		 ORDER BY first_v.version_number, first_v.id LIMIT 1), NULLIF(m.frame_id, '')),
		a.name, COALESCE(NULLIF(p.content_type, ''), a.kind),
		CASE WHEN COALESCE(v.storage_path, '') = '' THEN length(v.content) ELSE v.size_bytes END,
		v.created_at, NULLIF(v.content_sha256, ''), COALESCE(v.storage_path, ''),
		COALESCE(m.is_user_upload, 0), p.agent_name, p.language,
		COALESCE(p.is_intermediate, 0), a.retention_mode, a.priority,
		(SELECT first_v.id FROM artifact_versions first_v
		 WHERE first_v.artifact_id = a.id
		 ORDER BY first_v.version_number, first_v.id LIMIT 1),
		m.superseded_by_artifact_id
	FROM artifacts a
	JOIN projects project ON project.id = a.project_id`

const compatibilityConversationArtifactCurrentVersionJoin = `
	JOIN artifact_versions v ON v.artifact_id = a.id AND v.version_number = a.current_version_number`

const compatibilityConversationArtifactAllVersionsJoin = `
	JOIN artifact_versions v ON v.artifact_id = a.id`

const compatibilityConversationArtifactSelectSuffix = `
	JOIN artifact_runtime_metadata m ON m.artifact_id = a.id
	LEFT JOIN artifact_version_provenance p ON p.version_id = v.id`

const compatibilityConversationArtifactSelect = compatibilityConversationArtifactSelectPrefix +
	compatibilityConversationArtifactCurrentVersionJoin + compatibilityConversationArtifactSelectSuffix

const compatibilityExcludeIntermediateArtifactWhere = ` AND COALESCE(p.is_intermediate, 0) = 0
	AND NOT EXISTS (
		SELECT 1 FROM transcript_artifact_commits consumed
		WHERE consumed.version_id=v.id AND consumed.relation='consumed'
			AND NOT EXISTS (
				SELECT 1 FROM transcript_artifact_commits produced
				WHERE produced.version_id=v.id AND produced.relation='produced'
			)
	)`

func (s *Store) ListCompatibilityConversationArtifacts(
	ctx context.Context, ownerUserID, projectID, rootFrameID string, excludeIntermediate bool,
) ([]CompatibilityConversationArtifact, error) {
	return s.listCompatibilityConversationArtifacts(
		ctx, ownerUserID, projectID, rootFrameID, excludeIntermediate, true,
	)
}

// ListCompatibilityConversationArtifactVersions returns exact immutable versions
// for message-level artifact references. Project and export callers continue to
// use ListCompatibilityConversationArtifacts, whose one-row-per-artifact contract
// represents the current workspace state.
func (s *Store) ListCompatibilityConversationArtifactVersions(
	ctx context.Context, ownerUserID, projectID, rootFrameID string, excludeIntermediate bool,
) ([]CompatibilityConversationArtifact, error) {
	return s.listCompatibilityConversationArtifacts(
		ctx, ownerUserID, projectID, rootFrameID, excludeIntermediate, false,
	)
}

// ListCompatibilityConversationArtifactVersionsByReferences resolves one
// bounded batch of immutable artifact/version pairs inside the authenticated
// conversation root. The requested VALUES table is joined in one SQL query;
// missing, mismatched, foreign-owner, or foreign-root pairs fail the complete
// batch closed rather than silently returning a partial result.
func (s *Store) ListCompatibilityConversationArtifactVersionsByReferences(
	ctx context.Context,
	ownerUserID, projectID, rootFrameID string,
	references []CompatibilityArtifactVersionReference,
) ([]CompatibilityConversationArtifact, error) {
	return s.listCompatibilityConversationArtifactVersionsByReferences(
		ctx, ownerUserID, projectID, rootFrameID, references, true,
	)
}

// ListAvailableCompatibilityConversationArtifactVersionsByReferences resolves
// the owned subset of a batch. It is used by historical message rendering,
// where a deleted or superseded reference must not prevent still-valid links
// from loading. Scope checks remain in the SQL join, so foreign references are
// indistinguishable from missing references and are never returned.
func (s *Store) ListAvailableCompatibilityConversationArtifactVersionsByReferences(
	ctx context.Context,
	ownerUserID, projectID, rootFrameID string,
	references []CompatibilityArtifactVersionReference,
) ([]CompatibilityConversationArtifact, error) {
	return s.listCompatibilityConversationArtifactVersionsByReferences(
		ctx, ownerUserID, projectID, rootFrameID, references, false,
	)
}

func (s *Store) listCompatibilityConversationArtifactVersionsByReferences(
	ctx context.Context,
	ownerUserID, projectID, rootFrameID string,
	references []CompatibilityArtifactVersionReference,
	requireComplete bool,
) ([]CompatibilityConversationArtifact, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ownerUserID = strings.TrimSpace(ownerUserID)
	projectID = strings.TrimSpace(projectID)
	rootFrameID = strings.TrimSpace(rootFrameID)
	if ownerUserID == "" || projectID == "" || rootFrameID == "" {
		return nil, errors.New("artifact owner, project, and root frame are required")
	}
	if len(references) == 0 {
		return []CompatibilityConversationArtifact{}, nil
	}
	if len(references) > 400 {
		return nil, errors.New("artifact reference batch exceeds 400 pairs")
	}

	unique := make([]CompatibilityArtifactVersionReference, 0, len(references))
	seen := make(map[string]struct{}, len(references))
	for _, reference := range references {
		reference.ArtifactID = strings.TrimSpace(reference.ArtifactID)
		reference.VersionID = strings.TrimSpace(reference.VersionID)
		if reference.ArtifactID == "" || reference.VersionID == "" || len(reference.ArtifactID) > 512 || len(reference.VersionID) > 512 {
			return nil, errors.New("artifact reference pair is invalid")
		}
		key := reference.ArtifactID + "\x00" + reference.VersionID
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, reference)
	}

	values := make([]string, len(unique))
	arguments := make([]any, 0, len(unique)*3+3)
	for index, reference := range unique {
		values[index] = "(?, ?, ?)"
		arguments = append(arguments, index, reference.ArtifactID, reference.VersionID)
	}
	arguments = append(arguments, ownerUserID, projectID, rootFrameID)
	query := `WITH requested(ordinal, artifact_id, version_id) AS (VALUES ` + strings.Join(values, ",") + `)
		SELECT a.id, v.id, v.version_number, a.project_id,
			m.root_frame_id, COALESCE(NULLIF(m.frame_id, ''), p.frame_id),
			COALESCE((SELECT first_p.frame_id
			 FROM artifact_versions first_v
			 LEFT JOIN artifact_version_provenance first_p ON first_p.version_id = first_v.id
			 WHERE first_v.artifact_id = a.id
			 ORDER BY first_v.version_number, first_v.id LIMIT 1), NULLIF(m.frame_id, '')),
			a.name, COALESCE(NULLIF(p.content_type, ''), a.kind),
			CASE WHEN COALESCE(v.storage_path, '') = '' THEN length(v.content) ELSE v.size_bytes END,
			v.created_at, NULLIF(v.content_sha256, ''), COALESCE(v.storage_path, ''),
			COALESCE(m.is_user_upload, 0), p.agent_name, p.language,
			COALESCE(p.is_intermediate, 0), a.retention_mode, a.priority,
			(SELECT first_v.id FROM artifact_versions first_v
			 WHERE first_v.artifact_id = a.id
			 ORDER BY first_v.version_number, first_v.id LIMIT 1),
			m.superseded_by_artifact_id
		FROM requested requested
		JOIN artifact_versions v ON v.id = requested.version_id AND v.artifact_id = requested.artifact_id
		JOIN artifacts a ON a.id = requested.artifact_id
		JOIN projects project ON project.id = a.project_id
		JOIN artifact_runtime_metadata m ON m.artifact_id = a.id
		LEFT JOIN artifact_version_provenance p ON p.version_id = v.id
		WHERE project.user_id = ? AND a.project_id = ? AND m.root_frame_id = ?
		ORDER BY requested.ordinal`
	rows, err := s.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("resolve artifact reference batch: %w", err)
	}
	defer rows.Close()
	artifacts := make([]CompatibilityConversationArtifact, 0, len(unique))
	for rows.Next() {
		artifact, err := s.scanCompatibilityConversationArtifact(rows)
		if err != nil {
			return nil, fmt.Errorf("scan artifact reference batch: %w", err)
		}
		artifacts = append(artifacts, artifact)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate artifact reference batch: %w", err)
	}
	if requireComplete && len(artifacts) != len(unique) {
		return nil, ErrCompatibilityArtifactReferenceNotFound
	}
	return artifacts, nil
}

// ListCompatibilityConversationArtifactVersionsByVersionIDs resolves a
// bounded batch of immutable version IDs, then delegates ownership, project,
// root-frame, and artifact/version-pair authorization to the canonical pair
// resolver above. This supports durable message links whose historical
// projection omitted artifact_refs without falling back to a filename search.
func (s *Store) ListCompatibilityConversationArtifactVersionsByVersionIDs(
	ctx context.Context,
	ownerUserID, projectID, rootFrameID string,
	versionIDs []string,
) ([]CompatibilityConversationArtifact, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if len(versionIDs) == 0 {
		return []CompatibilityConversationArtifact{}, nil
	}
	if len(versionIDs) > 400 {
		return nil, errors.New("artifact version id batch exceeds 400 entries")
	}

	unique := make([]string, 0, len(versionIDs))
	seen := make(map[string]struct{}, len(versionIDs))
	for _, versionID := range versionIDs {
		versionID = strings.TrimSpace(versionID)
		if versionID == "" || len(versionID) > 512 {
			return nil, errors.New("artifact version id is invalid")
		}
		if _, exists := seen[versionID]; exists {
			continue
		}
		seen[versionID] = struct{}{}
		unique = append(unique, versionID)
	}

	placeholders := make([]string, len(unique))
	arguments := make([]any, len(unique))
	for index, versionID := range unique {
		placeholders[index] = "?"
		arguments[index] = versionID
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, artifact_id FROM artifact_versions WHERE id IN (`+
		strings.Join(placeholders, ",")+`)`, arguments...)
	if err != nil {
		return nil, fmt.Errorf("resolve artifact version ids: %w", err)
	}
	defer rows.Close()
	artifactByVersion := make(map[string]string, len(unique))
	for rows.Next() {
		var versionID, artifactID string
		if err := rows.Scan(&versionID, &artifactID); err != nil {
			return nil, fmt.Errorf("scan artifact version id: %w", err)
		}
		artifactByVersion[versionID] = artifactID
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate artifact version ids: %w", err)
	}
	if len(artifactByVersion) != len(unique) {
		return nil, ErrCompatibilityArtifactReferenceNotFound
	}

	references := make([]CompatibilityArtifactVersionReference, 0, len(unique))
	for _, versionID := range unique {
		references = append(references, CompatibilityArtifactVersionReference{
			ArtifactID: artifactByVersion[versionID],
			VersionID:  versionID,
		})
	}
	return s.ListCompatibilityConversationArtifactVersionsByReferences(
		ctx, ownerUserID, projectID, rootFrameID, references,
	)
}

// ListAvailableCompatibilityConversationArtifactVersionsByVersionIDs is the
// partial, non-enumerating counterpart used for rendering stale historical
// message links.
func (s *Store) ListAvailableCompatibilityConversationArtifactVersionsByVersionIDs(
	ctx context.Context,
	ownerUserID, projectID, rootFrameID string,
	versionIDs []string,
) ([]CompatibilityConversationArtifact, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if len(versionIDs) == 0 {
		return []CompatibilityConversationArtifact{}, nil
	}
	if len(versionIDs) > 400 {
		return nil, errors.New("artifact version id batch exceeds 400 entries")
	}
	unique := make([]string, 0, len(versionIDs))
	seen := make(map[string]struct{}, len(versionIDs))
	for _, versionID := range versionIDs {
		versionID = strings.TrimSpace(versionID)
		if versionID == "" || len(versionID) > 512 {
			return nil, errors.New("artifact version id is invalid")
		}
		if _, exists := seen[versionID]; exists {
			continue
		}
		seen[versionID] = struct{}{}
		unique = append(unique, versionID)
	}
	placeholders := make([]string, len(unique))
	arguments := make([]any, len(unique))
	for index, versionID := range unique {
		placeholders[index] = "?"
		arguments[index] = versionID
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, artifact_id FROM artifact_versions WHERE id IN (`+
		strings.Join(placeholders, ",")+`)`, arguments...)
	if err != nil {
		return nil, fmt.Errorf("resolve available artifact version ids: %w", err)
	}
	defer rows.Close()
	artifactByVersion := make(map[string]string, len(unique))
	for rows.Next() {
		var versionID, artifactID string
		if err := rows.Scan(&versionID, &artifactID); err != nil {
			return nil, fmt.Errorf("scan available artifact version id: %w", err)
		}
		artifactByVersion[versionID] = artifactID
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate available artifact version ids: %w", err)
	}
	references := make([]CompatibilityArtifactVersionReference, 0, len(artifactByVersion))
	for _, versionID := range unique {
		if artifactID := artifactByVersion[versionID]; artifactID != "" {
			references = append(references, CompatibilityArtifactVersionReference{ArtifactID: artifactID, VersionID: versionID})
		}
	}
	return s.ListAvailableCompatibilityConversationArtifactVersionsByReferences(
		ctx, ownerUserID, projectID, rootFrameID, references,
	)
}

func (s *Store) listCompatibilityConversationArtifacts(
	ctx context.Context, ownerUserID, projectID, rootFrameID string, excludeIntermediate, currentOnly bool,
) ([]CompatibilityConversationArtifact, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ownerUserID, projectID = strings.TrimSpace(ownerUserID), strings.TrimSpace(projectID)
	rootFrameID = strings.TrimSpace(rootFrameID)
	versionJoin := compatibilityConversationArtifactAllVersionsJoin
	if currentOnly {
		versionJoin = compatibilityConversationArtifactCurrentVersionJoin
	}
	query := compatibilityConversationArtifactSelectPrefix + versionJoin + compatibilityConversationArtifactSelectSuffix + `
		WHERE project.user_id = ? AND a.project_id = ? AND m.root_frame_id = ?
			AND COALESCE(m.is_ephemeral, 0) = 0`
	if excludeIntermediate {
		query += compatibilityExcludeIntermediateArtifactWhere
	}
	query += ` ORDER BY a.created_at DESC, a.id DESC`
	if !currentOnly {
		query += `, v.version_number ASC, v.id ASC`
	}
	rows, err := s.db.QueryContext(ctx, query, ownerUserID, projectID, rootFrameID)
	if err != nil {
		return nil, fmt.Errorf("list compatibility conversation artifacts: %w", err)
	}
	defer rows.Close()
	artifacts := make([]CompatibilityConversationArtifact, 0)
	for rows.Next() {
		artifact, err := s.scanCompatibilityConversationArtifact(rows)
		if err != nil {
			return nil, fmt.Errorf("scan compatibility conversation artifact: %w", err)
		}
		artifacts = append(artifacts, artifact)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate compatibility conversation artifacts: %w", err)
	}
	return artifacts, nil
}

func (s *Store) scanCompatibilityConversationArtifact(scanner rowScanner) (CompatibilityConversationArtifact, error) {
	var artifact CompatibilityConversationArtifact
	var rootID, frameID, creatingFrameID, checksum, storagePath sql.NullString
	var agentName, language, creatingVersionID, supersededBy sql.NullString
	if err := scanner.Scan(
		&artifact.ID, &artifact.VersionID, &artifact.VersionNumber, &artifact.ProjectID,
		&rootID, &frameID, &creatingFrameID, &artifact.Filename, &artifact.ContentType,
		&artifact.SizeBytes, &artifact.CreatedAt, &checksum, &storagePath,
		&artifact.IsUserUpload, &agentName, &language, &artifact.IsIntermediate, &artifact.RetentionMode,
		&artifact.Priority, &creatingVersionID, &supersededBy,
	); err != nil {
		return CompatibilityConversationArtifact{}, err
	}
	artifact.RootFrameID = nullableStringPointer(rootID)
	artifact.FrameID = nullableStringPointer(frameID)
	artifact.CreatingFrameID = nullableStringPointer(creatingFrameID)
	artifact.Checksum = nullableStringPointer(checksum)
	artifact.AgentName = nullableStringPointer(agentName)
	artifact.Language = nullableStringPointer(language)
	artifact.CreatingVersionID = nullableStringPointer(creatingVersionID)
	artifact.SupersededByArtifactID = nullableStringPointer(supersededBy)
	artifact.Priority = compatibilityArtifactPriority(artifact.Priority)
	if storagePath.Valid && strings.TrimSpace(storagePath.String) != "" {
		path, err := s.blobAbsolute(storagePath.String)
		if err != nil {
			return CompatibilityConversationArtifact{}, fmt.Errorf("resolve compatibility conversation artifact path: %w", err)
		}
		artifact.FilePath = path
	}
	return artifact, nil
}

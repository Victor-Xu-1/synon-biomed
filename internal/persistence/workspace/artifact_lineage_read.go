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

type ArtifactVersionHistoryEntry struct {
	VersionID       string
	VersionNumber   int
	ArtifactID      string
	FrameID         *string
	AgentName       *string
	Language        *string
	ContentType     string
	SizeBytes       int64
	CreatedAt       time.Time
	FilePath        string
	ParentVersionID *string
}

const artifactVersionHistorySelect = `
	SELECT v.id, v.version_number, v.artifact_id,
		p.frame_id, p.agent_name, p.language,
		COALESCE(NULLIF(p.content_type, ''), a.kind),
		CASE WHEN COALESCE(v.storage_path, '') = '' THEN length(v.content) ELSE v.size_bytes END,
		v.created_at, COALESCE(v.storage_path, ''), v.parent_id
	FROM artifact_versions AS v
	JOIN artifacts AS a ON a.id = v.artifact_id
	LEFT JOIN artifact_version_provenance AS p ON p.version_id = v.id`

type ArtifactLineageRecord struct {
	ArtifactID          string
	ProjectID           string
	VersionID           string
	VersionNumber       int
	Filename            string
	ContentType         string
	SizeBytes           int64
	Checksum            string
	FrameID             *string
	ProducingCellID     *string
	CreatedAt           time.Time
	Code                *string
	CodeDescription     *string
	Messages            any
	EnvironmentSnapshot any
	Language            *string
	Interactions        any
	HasCellSources      bool
	HasMessages         bool
	HasEnvironment      bool
	Pending             bool
	DependencyMappings  any
	RootFrameID         string
	IsUserUpload        bool

	lineageSnapshotHash string
	envSnapshotHash     string
	isUserUpload        bool
}

func (s *Store) ListArtifactVersionHistory(artifactID string) (Artifact, []ArtifactVersionHistoryEntry, bool, error) {
	if s == nil || s.db == nil {
		return Artifact{}, nil, false, errors.New("workspace store is closed")
	}
	artifactID = strings.TrimSpace(artifactID)
	if artifactID == "" {
		return Artifact{}, nil, false, errors.New("artifact id is required")
	}
	artifact, found, err := s.GetArtifact(artifactID)
	if err != nil || !found {
		return artifact, nil, found, err
	}
	entries, err := s.queryArtifactVersionHistory(context.Background(),
		artifactVersionHistorySelect+` WHERE v.artifact_id = ? ORDER BY v.version_number ASC, v.id ASC`, artifactID)
	if err != nil {
		return Artifact{}, nil, false, err
	}
	return artifact, entries, true, nil
}

func (s *Store) queryArtifactVersionHistory(ctx context.Context, query string, args ...any) ([]ArtifactVersionHistoryEntry, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list artifact version history: %w", err)
	}
	defer rows.Close()
	entries := make([]ArtifactVersionHistoryEntry, 0)
	for rows.Next() {
		var entry ArtifactVersionHistoryEntry
		var frameID, agentName, language, storagePath, parentVersionID sql.NullString
		if err := rows.Scan(
			&entry.VersionID, &entry.VersionNumber, &entry.ArtifactID,
			&frameID, &agentName, &language, &entry.ContentType, &entry.SizeBytes,
			&entry.CreatedAt, &storagePath, &parentVersionID,
		); err != nil {
			return nil, fmt.Errorf("scan artifact version history: %w", err)
		}
		entry.FrameID = nullableStringPointer(frameID)
		entry.AgentName = nullableStringPointer(agentName)
		entry.Language = nullableStringPointer(language)
		entry.ParentVersionID = nullableStringPointer(parentVersionID)
		if storagePath.Valid && strings.TrimSpace(storagePath.String) != "" {
			entry.FilePath, err = s.blobAbsolute(storagePath.String)
			if err != nil {
				return nil, fmt.Errorf("resolve artifact version %q path: %w", entry.VersionID, err)
			}
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate artifact version history: %w", err)
	}
	return entries, nil
}

func (s *Store) GetCurrentArtifactLineageRecord(artifactID string, resolveSnapshots bool) (ArtifactLineageRecord, bool, error) {
	artifactID = strings.TrimSpace(artifactID)
	if artifactID == "" {
		return ArtifactLineageRecord{}, false, errors.New("artifact id is required")
	}
	return s.getArtifactLineageRecord(`a.id = ? AND v.version_number = a.current_version_number`, artifactID, resolveSnapshots)
}

func (s *Store) GetArtifactVersionLineageRecord(versionID string, resolveSnapshots bool) (ArtifactLineageRecord, bool, error) {
	versionID = strings.TrimSpace(versionID)
	if versionID == "" {
		return ArtifactLineageRecord{}, false, errors.New("artifact version id is required")
	}
	return s.getArtifactLineageRecord(`v.id = ?`, versionID, resolveSnapshots)
}

func (s *Store) getArtifactLineageRecord(predicate, id string, resolveSnapshots bool) (ArtifactLineageRecord, bool, error) {
	if s == nil || s.db == nil {
		return ArtifactLineageRecord{}, false, errors.New("workspace store is closed")
	}
	query := `
		SELECT a.id, a.project_id, v.id, v.version_number, a.name,
			COALESCE(NULLIF(p.content_type, ''), a.kind),
			CASE WHEN COALESCE(v.storage_path, '') = '' THEN length(v.content) ELSE v.size_bytes END,
			v.content_sha256, p.frame_id, p.producing_cell_id, v.created_at,
			p.extracted_code, p.code_description, p.lineage_messages,
			p.environment_snapshot, p.language, p.cell_sources,
			p.lineage_snapshot_hash, p.env_snapshot_hash, p.dependency_mappings,
			COALESCE(m.root_frame_id, ''), COALESCE(m.is_user_upload, 0)
		FROM artifact_versions AS v
		JOIN artifacts AS a ON a.id = v.artifact_id
		LEFT JOIN artifact_version_provenance AS p ON p.version_id = v.id
		LEFT JOIN artifact_runtime_metadata AS m ON m.artifact_id = a.id
		WHERE ` + predicate + `
		LIMIT 1`
	var record ArtifactLineageRecord
	var code, codeDescription, messages, environment, language, interactions sql.NullString
	var lineageHash, environmentHash, dependencyMappings sql.NullString
	var isUserUpload int
	err := s.db.QueryRowContext(context.Background(), query, id).Scan(
		&record.ArtifactID, &record.ProjectID, &record.VersionID, &record.VersionNumber, &record.Filename,
		&record.ContentType, &record.SizeBytes, &record.Checksum, &record.FrameID, &record.ProducingCellID, &record.CreatedAt,
		&code, &codeDescription, &messages, &environment, &language, &interactions,
		&lineageHash, &environmentHash, &dependencyMappings, &record.RootFrameID, &isUserUpload,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ArtifactLineageRecord{}, false, nil
	}
	if err != nil {
		return ArtifactLineageRecord{}, false, fmt.Errorf("get artifact lineage: %w", err)
	}
	record.Code = nullableStringPointer(code)
	record.CodeDescription = nullableStringPointer(codeDescription)
	record.Language = nullableStringPointer(language)
	if record.Messages, err = decodeArtifactLineageJSON("lineage messages", messages); err != nil {
		return ArtifactLineageRecord{}, false, err
	}
	if record.EnvironmentSnapshot, err = decodeArtifactLineageJSON("environment snapshot", environment); err != nil {
		return ArtifactLineageRecord{}, false, err
	}
	if record.Interactions, err = decodeArtifactLineageJSON("cell sources", interactions); err != nil {
		return ArtifactLineageRecord{}, false, err
	}
	if record.DependencyMappings, err = decodeArtifactLineageJSON("dependency mappings", dependencyMappings); err != nil {
		return ArtifactLineageRecord{}, false, err
	}
	record.lineageSnapshotHash = nullableStringValue(lineageHash)
	record.envSnapshotHash = nullableStringValue(environmentHash)
	record.isUserUpload = isUserUpload != 0
	record.IsUserUpload = record.isUserUpload
	record.HasCellSources = jsonCollectionLength(record.Interactions) > 0
	record.HasMessages = record.lineageSnapshotHash != "" || jsonCollectionLength(record.Messages) > 0
	record.HasEnvironment = record.envSnapshotHash != "" || record.EnvironmentSnapshot != nil
	if resolveSnapshots {
		if record.Messages == nil && record.lineageSnapshotHash != "" {
			record.Messages, err = s.loadArtifactContentSnapshot(record.lineageSnapshotHash)
			if err != nil {
				return ArtifactLineageRecord{}, false, err
			}
		}
		if record.EnvironmentSnapshot == nil && record.envSnapshotHash != "" {
			record.EnvironmentSnapshot, err = s.loadArtifactContentSnapshot(record.envSnapshotHash)
			if err != nil {
				return ArtifactLineageRecord{}, false, err
			}
		}
	}
	record.Pending = artifactLineagePending(record)
	return record, true, nil
}

func (s *Store) loadArtifactContentSnapshot(hash string) (any, error) {
	var content string
	err := s.db.QueryRowContext(context.Background(), `SELECT content FROM content_snapshots WHERE hash = ?`, hash).Scan(&content)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load artifact lineage snapshot %q: %w", hash, err)
	}
	var value any
	if err := json.Unmarshal([]byte(content), &value); err != nil {
		// v1.1 ignores an invalid or stale snapshot and keeps the field null.
		return nil, nil
	}
	return value, nil
}

func decodeArtifactLineageJSON(name string, value sql.NullString) (any, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" || strings.TrimSpace(value.String) == "null" {
		return nil, nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(value.String), &decoded); err != nil {
		return nil, fmt.Errorf("decode artifact %s: %w", name, err)
	}
	return decoded, nil
}

func artifactLineagePending(record ArtifactLineageRecord) bool {
	if record.isUserUpload || !lineageCodeEmpty(record.Code) {
		return false
	}
	if mappings, ok := record.DependencyMappings.(map[string]any); ok && mappings["mapping_status"] == "pending" {
		return true
	}
	language := ""
	if record.Language != nil {
		language = *record.Language
	}
	if language == "text" {
		return false
	}
	if sources, ok := record.Interactions.([]any); ok {
		for _, source := range sources {
			mapping, ok := source.(map[string]any)
			if !ok {
				continue
			}
			kind, hasKind := mapping["kind"]
			if !hasKind || kind == "cell" {
				return true
			}
		}
	}
	return !record.HasCellSources && record.HasMessages
}

func lineageCodeEmpty(value *string) bool {
	return value == nil || *value == ""
}

func jsonCollectionLength(value any) int {
	switch typed := value.(type) {
	case []any:
		return len(typed)
	case map[string]any:
		return len(typed)
	default:
		return 0
	}
}

func nullableStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	copy := value.String
	return &copy
}

func nullableStringValue(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

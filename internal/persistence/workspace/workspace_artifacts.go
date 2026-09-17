package workspace

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"strings"
)

func (s *Store) CreateAgent(input CreateAgentInput) (Agent, error) {
	if s == nil || s.db == nil {
		return Agent{}, errors.New("workspace store is closed")
	}
	for field, value := range map[string]string{
		"agent id": input.ID, "user id": input.UserID, "agent name": input.Name,
		"display name": input.DisplayName, "description": input.Description,
	} {
		if strings.TrimSpace(value) == "" {
			return Agent{}, fmt.Errorf("%s is required", field)
		}
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	skillNames, err := json.Marshal(normalizeSkillNames(input.SkillNames))
	if err != nil {
		return Agent{}, fmt.Errorf("marshal agent skill names: %w", err)
	}
	skillTombstones, err := json.Marshal(normalizeSkillNames(input.SkillTombstones))
	if err != nil {
		return Agent{}, fmt.Errorf("marshal agent skill tombstones: %w", err)
	}
	connectorTombstones, err := json.Marshal(normalizeConnectorIDs(input.ConnectorTombstones))
	if err != nil {
		return Agent{}, fmt.Errorf("marshal agent connector tombstones: %w", err)
	}
	tags, err := json.Marshal(normalizeAgentTags(input.Tags))
	if err != nil {
		return Agent{}, fmt.Errorf("marshal agent tags: %w", err)
	}
	now := s.now().UTC()
	agent := Agent{
		ID: input.ID, UserID: input.UserID, Name: input.Name, DisplayName: input.DisplayName,
		Description: input.Description, SystemPrompt: input.SystemPrompt, IconKey: input.IconKey, ColorKey: input.ColorKey,
		Tags: normalizeAgentTags(input.Tags), SkillNames: normalizeSkillNames(input.SkillNames),
		SkillTombstones: normalizeSkillNames(input.SkillTombstones), ConnectorTombstones: normalizeConnectorIDs(input.ConnectorTombstones),
		Unrestricted: input.Unrestricted,
		Enabled:      enabled, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := s.db.ExecContext(context.Background(), `
		INSERT INTO user_agents (id, user_id, name, display_name, description, system_prompt, icon_key, color_key, tags,
			skill_names, skill_tombstones, connector_tombstones, unrestricted, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		agent.ID, agent.UserID, agent.Name, agent.DisplayName, agent.Description, agent.SystemPrompt, agent.IconKey, agent.ColorKey,
		string(tags), string(skillNames), string(skillTombstones), string(connectorTombstones), agent.Unrestricted, agent.Enabled,
		agent.CreatedAt, agent.UpdatedAt); err != nil {
		return Agent{}, fmt.Errorf("insert user agent: %w", err)
	}
	return agent, nil
}

// SaveArtifactVersion atomically appends one immutable artifact version. The
// artifact's current version and the parent pointer are updated in the same
// SQLite transaction, which prevents broken lineage under concurrent writers.
func (s *Store) SaveArtifactVersion(input SaveArtifactVersionInput) (Artifact, ArtifactVersion, error) {
	if s == nil || s.db == nil {
		return Artifact{}, ArtifactVersion{}, errors.New("workspace store is closed")
	}
	if strings.TrimSpace(input.ArtifactID) == "" || strings.TrimSpace(input.ProjectID) == "" {
		return Artifact{}, ArtifactVersion{}, errors.New("artifact id and project id are required")
	}
	if strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.Kind) == "" {
		return Artifact{}, ArtifactVersion{}, errors.New("artifact name and kind are required")
	}

	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Artifact{}, ArtifactVersion{}, fmt.Errorf("begin artifact transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var currentVersion int
	var storedProjectID string
	err = tx.QueryRowContext(ctx, `SELECT project_id, current_version_number FROM artifacts WHERE id = ?`, input.ArtifactID).Scan(&storedProjectID, &currentVersion)
	now := s.now().UTC()
	if errors.Is(err, sql.ErrNoRows) {
		var exists string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM projects WHERE id = ?`, input.ProjectID).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return Artifact{}, ArtifactVersion{}, fmt.Errorf("project %q does not exist", input.ProjectID)
			}
			return Artifact{}, ArtifactVersion{}, fmt.Errorf("look up project: %w", err)
		}
		currentVersion = 0
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO artifacts (id, project_id, name, kind, current_version_number, created_at, updated_at)
			VALUES (?, ?, ?, ?, 0, ?, ?)`, input.ArtifactID, input.ProjectID, input.Name, input.Kind, now, now); err != nil {
			return Artifact{}, ArtifactVersion{}, fmt.Errorf("insert artifact: %w", err)
		}
	} else if err != nil {
		return Artifact{}, ArtifactVersion{}, fmt.Errorf("look up artifact: %w", err)
	} else if storedProjectID != input.ProjectID {
		return Artifact{}, ArtifactVersion{}, fmt.Errorf("artifact %q belongs to project %q, not %q", input.ArtifactID, storedProjectID, input.ProjectID)
	}

	versionNumber := currentVersion + 1
	parentID := strings.TrimSpace(input.ParentVersionID)
	if parentID != "" {
		var parentArtifactID string
		if err := tx.QueryRowContext(ctx, `
			SELECT artifact_id FROM artifact_versions WHERE id = ?`, parentID).Scan(&parentArtifactID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return Artifact{}, ArtifactVersion{}, fmt.Errorf("parent artifact version %q does not exist", parentID)
			}
			return Artifact{}, ArtifactVersion{}, fmt.Errorf("look up requested parent artifact version: %w", err)
		}
		if parentArtifactID != input.ArtifactID {
			return Artifact{}, ArtifactVersion{}, fmt.Errorf("parent artifact version %q belongs to artifact %q, not %q", parentID, parentArtifactID, input.ArtifactID)
		}
	} else if currentVersion > 0 {
		if err := tx.QueryRowContext(ctx, `
			SELECT id FROM artifact_versions WHERE artifact_id = ? AND version_number = ?`, input.ArtifactID, currentVersion).Scan(&parentID); err != nil {
			return Artifact{}, ArtifactVersion{}, fmt.Errorf("look up parent artifact version: %w", err)
		}
	}
	digest := sha256.Sum256(input.Content)
	version := ArtifactVersion{
		ID:            uuid.NewString(),
		ArtifactID:    input.ArtifactID,
		VersionNumber: versionNumber,
		ParentID:      parentID,
		Content:       append([]byte(nil), input.Content...),
		ContentSHA256: hex.EncodeToString(digest[:]),
		SizeBytes:     int64(len(input.Content)),
		CreatedBy:     input.CreatedBy,
		CreatedAt:     now,
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artifact_versions (id, artifact_id, version_number, parent_id, content, content_sha256, storage_path, size_bytes, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, '', ?, ?, ?)`, version.ID, version.ArtifactID, version.VersionNumber, nullableString(version.ParentID), version.Content, version.ContentSHA256, version.SizeBytes, version.CreatedBy, version.CreatedAt); err != nil {
		return Artifact{}, ArtifactVersion{}, fmt.Errorf("insert artifact version: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE artifacts SET name = ?, kind = ?, current_version_number = ?, updated_at = ? WHERE id = ?`,
		input.Name, input.Kind, versionNumber, now, input.ArtifactID); err != nil {
		return Artifact{}, ArtifactVersion{}, fmt.Errorf("update artifact current version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Artifact{}, ArtifactVersion{}, fmt.Errorf("commit artifact version: %w", err)
	}
	return Artifact{
		ID:                   input.ArtifactID,
		ProjectID:            input.ProjectID,
		Name:                 input.Name,
		Kind:                 input.Kind,
		CurrentVersionNumber: versionNumber,
		CreatedAt:            now,
		UpdatedAt:            now,
	}, version, nil
}

// ArtifactLineage returns the current artifact history from newest to oldest.
func (s *Store) ArtifactLineage(artifactID string) ([]ArtifactVersion, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT id, artifact_id, version_number, COALESCE(parent_id, ''), content, content_sha256,
			COALESCE(storage_path, ''),
			CASE WHEN COALESCE(storage_path, '') = '' THEN length(content) ELSE size_bytes END,
			created_by, created_at
		FROM artifact_versions WHERE artifact_id = ? ORDER BY version_number DESC`, artifactID)
	if err != nil {
		return nil, fmt.Errorf("query artifact lineage: %w", err)
	}
	defer rows.Close()
	lineage := make([]ArtifactVersion, 0)
	for rows.Next() {
		var version ArtifactVersion
		if err := rows.Scan(&version.ID, &version.ArtifactID, &version.VersionNumber, &version.ParentID,
			&version.Content, &version.ContentSHA256, &version.StoragePath, &version.SizeBytes,
			&version.CreatedBy, &version.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan artifact lineage: %w", err)
		}
		lineage = append(lineage, version)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate artifact lineage: %w", err)
	}
	return lineage, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

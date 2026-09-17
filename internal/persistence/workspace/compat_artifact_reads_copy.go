package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

var ErrCompatibilityArtifactCopyFolder = errors.New("compatibility artifact copy folder not found")

type CompatibilityArtifactCopyResult struct {
	OriginalArtifactID string `json:"original_artifact_id"`
	NewArtifactID      string `json:"new_artifact_id"`
	Filename           string `json:"filename"`
}

func (s *Store) GetCompatibilityArtifactMetadata(
	ctx context.Context, ownerUserID, artifactID string,
) (CompatibilityConversationArtifact, bool, error) {
	if s == nil || s.db == nil {
		return CompatibilityConversationArtifact{}, false, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ownerUserID, artifactID = strings.TrimSpace(ownerUserID), strings.TrimSpace(artifactID)
	artifact, err := s.scanCompatibilityConversationArtifact(s.db.QueryRowContext(ctx,
		compatibilityConversationArtifactSelect+` WHERE project.user_id = ? AND a.id = ?`, ownerUserID, artifactID))
	if errors.Is(err, sql.ErrNoRows) {
		return CompatibilityConversationArtifact{}, false, nil
	}
	if err != nil {
		return CompatibilityConversationArtifact{}, false, fmt.Errorf("get compatibility artifact metadata: %w", err)
	}
	return artifact, true, nil
}

func (s *Store) ListCompatibilityArtifactVersions(
	ctx context.Context, ownerUserID, artifactID string,
) ([]ArtifactVersionHistoryEntry, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ownerUserID, artifactID = strings.TrimSpace(ownerUserID), strings.TrimSpace(artifactID)
	entries, err := s.queryArtifactVersionHistory(ctx, artifactVersionHistorySelect+`
		JOIN projects AS project ON project.id = a.project_id AND project.user_id = ?
		WHERE v.artifact_id = ?
		ORDER BY v.version_number ASC, v.id ASC`, ownerUserID, artifactID)
	if err != nil {
		return nil, false, err
	}
	if len(entries) == 0 {
		return []ArtifactVersionHistoryEntry{}, false, nil
	}
	return entries, true, nil
}

func (s *Store) CopyCompatibilityArtifactRealtime(
	ctx context.Context, ownerUserID, artifactID string, newFilename, targetFolderID *string,
) (CompatibilityArtifactCopyResult, error) {
	if s == nil || s.db == nil {
		return CompatibilityArtifactCopyResult{}, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ownerUserID, artifactID = strings.TrimSpace(ownerUserID), strings.TrimSpace(artifactID)
	if ownerUserID == "" || artifactID == "" {
		return CompatibilityArtifactCopyResult{}, ErrCompatibilityArtifactNotFound
	}
	result := CompatibilityArtifactCopyResult{OriginalArtifactID: artifactID}
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var source struct {
			projectID, filename, kind, checksum, storagePath, createdBy, contentType string
			folderID, rootFrameID, extractedCode, agentName, language                sql.NullString
			content                                                                  []byte
			sizeBytes                                                                int64
			isUserUpload, isIntermediate                                             bool
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT a.project_id, a.name, a.kind, a.folder_id,
				m.root_frame_id, COALESCE(m.is_user_upload, 0),
				v.content, v.content_sha256, v.storage_path, v.size_bytes, v.created_by,
				COALESCE(NULLIF(p.content_type, ''), a.kind), p.extracted_code,
				p.agent_name, p.language, COALESCE(p.is_intermediate, 0)
			FROM artifacts a
			JOIN projects project ON project.id = a.project_id AND project.user_id = ?
			JOIN artifact_versions v ON v.artifact_id = a.id AND v.version_number = a.current_version_number
			JOIN artifact_runtime_metadata m ON m.artifact_id = a.id
			LEFT JOIN artifact_version_provenance p ON p.version_id = v.id
			WHERE a.id = ?`, ownerUserID, artifactID).Scan(
			&source.projectID, &source.filename, &source.kind, &source.folderID,
			&source.rootFrameID, &source.isUserUpload,
			&source.content, &source.checksum, &source.storagePath,
			&source.sizeBytes, &source.createdBy, &source.contentType, &source.extractedCode,
			&source.agentName, &source.language, &source.isIntermediate,
		); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrCompatibilityArtifactNotFound
			}
			return fmt.Errorf("look up compatibility artifact for copy: %w", err)
		}

		filename := source.filename
		if newFilename != nil {
			filename = *newFilename
		}
		filename = SanitizeCompatibilityArtifactFilename(filename)
		if filename == "" {
			return errors.New("filename is required")
		}
		folderID := source.folderID
		if targetFolderID != nil {
			requested := strings.TrimSpace(*targetFolderID)
			if requested == "" {
				return ErrCompatibilityArtifactCopyFolder
			}
			var found string
			if err := tx.QueryRowContext(ctx, `SELECT id FROM artifact_folders WHERE id = ? AND project_id = ?`,
				requested, source.projectID).Scan(&found); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return ErrCompatibilityArtifactCopyFolder
				}
				return fmt.Errorf("validate compatibility artifact copy folder: %w", err)
			}
			folderID = sql.NullString{String: requested, Valid: true}
		}
		if source.content == nil {
			source.content = []byte{}
		}

		result.NewArtifactID, result.Filename = uuid.NewString(), filename
		versionID, now := uuid.NewString(), s.now().UTC()
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO artifacts
				(id, project_id, name, kind, current_version_number, folder_id, priority, created_at, updated_at)
			VALUES (?, ?, ?, ?, 1, ?, 'unknown', ?, ?)`,
			result.NewArtifactID, source.projectID, filename, source.kind,
			compatibilityNullableSQLString(folderID), now, now); err != nil {
			return fmt.Errorf("insert compatibility artifact copy: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO artifact_versions
				(id, artifact_id, version_number, parent_id, content, content_sha256, storage_path, size_bytes, created_by, created_at)
			VALUES (?, ?, 1, NULL, ?, ?, ?, ?, ?, ?)`,
			versionID, result.NewArtifactID, source.content, source.checksum, source.storagePath,
			source.sizeBytes, source.createdBy, now); err != nil {
			return fmt.Errorf("insert compatibility artifact copy version: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO artifact_runtime_metadata
				(artifact_id, root_frame_id, frame_id, latest_version_id, is_user_upload, is_branch_mint)
			VALUES (?, ?, NULL, ?, ?, 1)`,
			result.NewArtifactID, compatibilityNullableSQLString(source.rootFrameID), versionID, source.isUserUpload); err != nil {
			return fmt.Errorf("insert compatibility artifact copy metadata: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO artifact_version_provenance
				(version_id, frame_id, content_type, extracted_code, agent_name, language, is_intermediate)
			VALUES (?, NULL, ?, ?, ?, ?, ?)`,
			versionID, source.contentType, compatibilityNullableSQLString(source.extractedCode),
			compatibilityNullableSQLString(source.agentName), compatibilityNullableSQLString(source.language),
			source.isIntermediate); err != nil {
			return fmt.Errorf("insert compatibility artifact copy provenance: %w", err)
		}

		rootFrameID := nullablePayloadString(source.rootFrameID.String)
		folderPayload := nullablePayloadString(folderID.String)
		agentName := nullablePayloadString(source.agentName.String)
		return s.enqueueProjectDomainEventTx(ctx, tx, ownerUserID, source.projectID, "artifact_created", result.NewArtifactID,
			map[string]any{
				"root_frame_id": rootFrameID, "frame_id": nil, "version_root_frame_id": rootFrameID,
				"artifact": map[string]any{
					"id": result.NewArtifactID, "version_id": versionID, "version_number": 1,
					"filename": filename, "content_type": source.contentType, "size_bytes": source.sizeBytes,
					"file_path": nil, "folder_id": folderPayload, "is_user_upload": source.isUserUpload,
					"agent_name": agentName, "creating_frame_id": nil, "is_intermediate": source.isIntermediate,
				},
			})
	})
	return result, err
}

func compatibilityNullableSQLString(value sql.NullString) any {
	if value.Valid && strings.TrimSpace(value.String) != "" {
		return value.String
	}
	return nil
}

func SanitizeCompatibilityArtifactFilename(value string) string {
	value = strings.TrimFunc(value, isCompatibilityECMAScriptWhitespace)
	var output strings.Builder
	output.Grow(min(len(value), 255))
	utf16Units := 0
	for _, char := range value {
		if compatibilityArtifactFilenameControl(char) {
			continue
		}
		width := 1
		if char > 0xffff {
			width = 2
		}
		if utf16Units+width > 255 {
			break
		}
		output.WriteRune(char)
		utf16Units += width
	}
	return output.String()
}

func compatibilityArtifactFilenameControl(char rune) bool {
	return char <= 0x1f || char == 0x7f || char == 0x200e || char == 0x200f ||
		char == 0x2028 || char == 0x2029 || char >= 0x202a && char <= 0x202e ||
		char >= 0x2066 && char <= 0x2069
}

func isCompatibilityECMAScriptWhitespace(char rune) bool {
	return char >= 0x09 && char <= 0x0d || char == 0x20 || char == 0x00a0 || char == 0x1680 ||
		char >= 0x2000 && char <= 0x200a || char == 0x2028 || char == 0x2029 || char == 0x202f ||
		char == 0x205f || char == 0x3000 || char == 0xfeff
}

package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrCompatibilityFolderProjectNotFound     = errors.New("compatibility folder project not found")
	ErrCompatibilityFolderNotFound            = errors.New("compatibility folder not found")
	ErrCompatibilityFolderParentNotFound      = errors.New("compatibility folder parent not found")
	ErrCompatibilityFolderSystem              = errors.New("compatibility system folder is immutable")
	ErrCompatibilityFolderConversationParent  = errors.New("compatibility folder parent is a conversation folder")
	ErrCompatibilityFolderSelfParent          = errors.New("compatibility folder cannot be its own parent")
	ErrCompatibilityFolderCycle               = errors.New("compatibility folder cycle")
	ErrCompatibilityFolderMoveTarget          = errors.New("compatibility folder move target is invalid")
	ErrCompatibilityFolderMoveIntoDeletedTree = errors.New("compatibility folder move target is inside the deleted tree")
	ErrCompatibilityArtifactNotFound          = errors.New("compatibility artifact not found")
	ErrCompatibilityArtifactUploadMove        = errors.New("compatibility user upload move is not allowed")
)

type CompatibilityArtifactFolder struct {
	ID                   string    `json:"id"`
	ProjectID            string    `json:"project_id"`
	ParentID             *string   `json:"parent_id"`
	Name                 string    `json:"name"`
	SortOrder            int       `json:"sort_order"`
	RootFrameID          *string   `json:"root_frame_id"`
	IsConversationFolder bool      `json:"is_conversation_folder"`
	IsUserUploadsFolder  bool      `json:"is_user_uploads_folder"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
	ArtifactCount        int64     `json:"artifact_count"`
}

type UpdateCompatibilityFolderInput struct {
	NameSet      bool
	Name         string
	ParentSet    bool
	ParentID     string
	SortOrderSet bool
	SortOrder    int
}

type CompatibilityFolderDeleteResult struct {
	Status           string   `json:"status"`
	FoldersDeleted   int      `json:"folders_deleted"`
	ArtifactsDeleted int64    `json:"artifacts_deleted"`
	ArtifactsMoved   int64    `json:"artifacts_moved"`
	BlobPaths        []string `json:"-"`
}

type UpdateCompatibilityArtifactFolderInput struct {
	FolderSet    bool
	FolderNull   bool
	FolderID     string
	SortOrderSet bool
	SortOrder    int
}

type CompatibilityArtifactFolderUpdate struct {
	Status    string  `json:"status"`
	FolderID  *string `json:"folder_id"`
	SortOrder *int    `json:"sort_order"`
}

func (s *Store) ListCompatibilityFolders(ctx context.Context, ownerUserID, projectID string) ([]CompatibilityArtifactFolder, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ownerUserID, projectID = strings.TrimSpace(ownerUserID), strings.TrimSpace(projectID)
	var foundID string
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM projects WHERE id = ? AND user_id = ?`, projectID, ownerUserID).Scan(&foundID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return []CompatibilityArtifactFolder{}, false, nil
		}
		return nil, false, fmt.Errorf("get compatibility folder project: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT f.id, f.project_id, f.parent_id, f.name, f.sort_order, f.root_frame_id,
			f.is_conversation_folder, f.is_user_uploads_folder, f.created_at, f.updated_at,
			COUNT(a.id)
		FROM artifact_folders f
		LEFT JOIN artifacts a ON a.folder_id = f.id
		WHERE f.project_id = ?
		GROUP BY f.id
		ORDER BY f.sort_order, f.created_at, f.id`, projectID)
	if err != nil {
		return nil, false, fmt.Errorf("list compatibility folders: %w", err)
	}
	defer rows.Close()
	folders := make([]CompatibilityArtifactFolder, 0)
	for rows.Next() {
		folder, err := scanCompatibilityArtifactFolder(rows)
		if err != nil {
			return nil, false, fmt.Errorf("scan compatibility folder: %w", err)
		}
		folders = append(folders, folder)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate compatibility folders: %w", err)
	}
	return folders, true, nil
}

func (s *Store) CreateCompatibilityFolderRealtime(
	ctx context.Context, ownerUserID, projectID, name, parentID string,
) (CompatibilityArtifactFolder, error) {
	if s == nil || s.db == nil {
		return CompatibilityArtifactFolder{}, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ownerUserID, projectID, parentID = strings.TrimSpace(ownerUserID), strings.TrimSpace(projectID), strings.TrimSpace(parentID)
	if strings.TrimSpace(name) == "" {
		return CompatibilityArtifactFolder{}, errors.New("name is required")
	}
	folder := CompatibilityArtifactFolder{ID: uuid.NewString(), ProjectID: projectID, Name: name, SortOrder: 0}
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		if err := requireCompatibilityFolderProject(ctx, tx, ownerUserID, projectID); err != nil {
			return err
		}
		if err := validateCompatibilityFolderParent(ctx, tx, projectID, parentID, ""); err != nil {
			return err
		}
		now := s.now().UTC()
		folder.CreatedAt, folder.UpdatedAt = now, now
		if parentID != "" {
			copy := parentID
			folder.ParentID = &copy
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO artifact_folders (
				id, project_id, parent_id, name, sort_order, root_frame_id,
				is_conversation_folder, is_user_uploads_folder, created_at, updated_at
			) VALUES (?, ?, ?, ?, 0, NULL, 0, 0, ?, ?)`,
			folder.ID, projectID, nullableString(parentID), name, now, now); err != nil {
			return fmt.Errorf("insert compatibility folder: %w", err)
		}
		return s.enqueueProjectDomainEventTx(ctx, tx, ownerUserID, projectID, "folder_created", folder.ID,
			folderRealtimePayload(compatibilityFolderStorageProjection(folder)))
	})
	return folder, err
}

func (s *Store) UpdateCompatibilityFolderRealtime(
	ctx context.Context, ownerUserID, projectID, folderID string, input UpdateCompatibilityFolderInput,
) (CompatibilityArtifactFolder, error) {
	if s == nil || s.db == nil {
		return CompatibilityArtifactFolder{}, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ownerUserID, projectID, folderID = strings.TrimSpace(ownerUserID), strings.TrimSpace(projectID), strings.TrimSpace(folderID)
	var folder CompatibilityArtifactFolder
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		if err := requireCompatibilityFolderProject(ctx, tx, ownerUserID, projectID); err != nil {
			return err
		}
		var parentID, rootFrameID sql.NullString
		if err := tx.QueryRowContext(ctx, `
			SELECT id, project_id, parent_id, name, sort_order, root_frame_id,
				is_conversation_folder, is_user_uploads_folder, created_at, updated_at, 0
			FROM artifact_folders WHERE id = ? AND project_id = ?`, folderID, projectID,
		).Scan(&folder.ID, &folder.ProjectID, &parentID, &folder.Name, &folder.SortOrder, &rootFrameID,
			&folder.IsConversationFolder, &folder.IsUserUploadsFolder, &folder.CreatedAt, &folder.UpdatedAt, &folder.ArtifactCount); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: %s", ErrCompatibilityFolderNotFound, folderID)
			}
			return fmt.Errorf("get compatibility folder for update: %w", err)
		}
		folder.ParentID, folder.RootFrameID = nullableStringPointer(parentID), nullableStringPointer(rootFrameID)
		if folder.IsConversationFolder || folder.IsUserUploadsFolder {
			return ErrCompatibilityFolderSystem
		}
		if input.ParentSet {
			if folderID == input.ParentID {
				return ErrCompatibilityFolderSelfParent
			}
			if err := validateCompatibilityFolderParent(ctx, tx, projectID, input.ParentID, folderID); err != nil {
				return err
			}
		}
		now := s.now().UTC()
		parentValue := any(nil)
		if input.ParentSet {
			parentValue = nullableString(input.ParentID)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE artifact_folders SET
				name = CASE WHEN ? THEN ? ELSE name END,
				parent_id = CASE WHEN ? THEN ? ELSE parent_id END,
				sort_order = CASE WHEN ? THEN ? ELSE sort_order END,
				updated_at = ?
			WHERE id = ? AND project_id = ?`,
			input.NameSet, input.Name, input.ParentSet, parentValue,
			input.SortOrderSet, input.SortOrder, now, folderID, projectID); err != nil {
			return fmt.Errorf("update compatibility folder: %w", err)
		}
		updated, err := scanCompatibilityArtifactFolder(tx.QueryRowContext(ctx, `
			SELECT f.id, f.project_id, f.parent_id, f.name, f.sort_order, f.root_frame_id,
				f.is_conversation_folder, f.is_user_uploads_folder, f.created_at, f.updated_at, 0
			FROM artifact_folders f WHERE f.id = ?`, folderID))
		if err != nil {
			return fmt.Errorf("read updated compatibility folder: %w", err)
		}
		folder = updated
		return s.enqueueProjectDomainEventTx(ctx, tx, ownerUserID, projectID, "folder_updated", folder.ID,
			folderRealtimePayload(compatibilityFolderStorageProjection(folder)))
	})
	return folder, err
}

func (s *Store) DeleteCompatibilityFolderRealtime(
	ctx context.Context, ownerUserID, projectID, folderID, moveArtifactsTo string, deleteArtifacts bool,
) (CompatibilityFolderDeleteResult, error) {
	result := CompatibilityFolderDeleteResult{Status: "deleted", BlobPaths: []string{}}
	if s == nil || s.db == nil {
		return result, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ownerUserID, projectID = strings.TrimSpace(ownerUserID), strings.TrimSpace(projectID)
	folderID, moveArtifactsTo = strings.TrimSpace(folderID), strings.TrimSpace(moveArtifactsTo)
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		if err := requireCompatibilityFolderProject(ctx, tx, ownerUserID, projectID); err != nil {
			return err
		}
		var conversation, uploads bool
		if err := tx.QueryRowContext(ctx, `
			SELECT is_conversation_folder, is_user_uploads_folder
			FROM artifact_folders WHERE id = ? AND project_id = ?`, folderID, projectID,
		).Scan(&conversation, &uploads); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: %s", ErrCompatibilityFolderNotFound, folderID)
			}
			return fmt.Errorf("get compatibility folder for delete: %w", err)
		}
		if conversation || uploads {
			return ErrCompatibilityFolderSystem
		}

		rows, err := tx.QueryContext(ctx, `
			WITH RECURSIVE folder_tree(id, depth) AS (
				SELECT id, 0 FROM artifact_folders WHERE id = ? AND project_id = ?
				UNION ALL
				SELECT child.id, parent.depth + 1
				FROM artifact_folders child JOIN folder_tree parent ON child.parent_id = parent.id
				WHERE child.project_id = ?
			)
			SELECT id, depth FROM folder_tree ORDER BY depth, id`, folderID, projectID, projectID)
		if err != nil {
			return fmt.Errorf("list compatibility folder descendants: %w", err)
		}
		folderIDs := make([]string, 0)
		for rows.Next() {
			var id string
			var depth int
			if err := rows.Scan(&id, &depth); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan compatibility folder descendant: %w", err)
			}
			folderIDs = append(folderIDs, id)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("iterate compatibility folder descendants: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close compatibility folder descendants: %w", err)
		}
		if len(folderIDs) == 0 {
			return fmt.Errorf("%w: %s", ErrCompatibilityFolderNotFound, folderID)
		}
		if !deleteArtifacts && moveArtifactsTo != "" {
			var targetProjectID string
			if err := tx.QueryRowContext(ctx, `SELECT project_id FROM artifact_folders WHERE id = ?`, moveArtifactsTo).Scan(&targetProjectID); err != nil || targetProjectID != projectID {
				return fmt.Errorf("%w: target folder %s not found", ErrCompatibilityFolderMoveTarget, moveArtifactsTo)
			}
			for _, id := range folderIDs {
				if id == moveArtifactsTo {
					return ErrCompatibilityFolderMoveIntoDeletedTree
				}
			}
		}

		arguments := make([]any, len(folderIDs))
		for i := range folderIDs {
			arguments[i] = folderIDs[i]
		}
		artifactRows, err := tx.QueryContext(ctx, `
			SELECT id, folder_id FROM artifacts
			WHERE folder_id IN (`+sqlQuestionMarks(len(folderIDs))+`) ORDER BY id`, arguments...)
		if err != nil {
			return fmt.Errorf("list compatibility folder artifacts: %w", err)
		}
		artifactIDs := make([]string, 0)
		artifactFolderIDs := make([]string, 0)
		for artifactRows.Next() {
			var artifactID, artifactFolderID string
			if err := artifactRows.Scan(&artifactID, &artifactFolderID); err != nil {
				_ = artifactRows.Close()
				return fmt.Errorf("scan compatibility folder artifact: %w", err)
			}
			artifactIDs = append(artifactIDs, artifactID)
			artifactFolderIDs = append(artifactFolderIDs, artifactFolderID)
		}
		if err := artifactRows.Err(); err != nil {
			_ = artifactRows.Close()
			return fmt.Errorf("iterate compatibility folder artifacts: %w", err)
		}
		if err := artifactRows.Close(); err != nil {
			return fmt.Errorf("close compatibility folder artifacts: %w", err)
		}

		if !deleteArtifacts && len(artifactIDs) > 0 {
			targetInUploads, err := compatibilityFolderWithinUserUploads(ctx, tx, projectID, moveArtifactsTo)
			if err != nil {
				return err
			}
			if !targetInUploads {
				for _, sourceFolderID := range artifactFolderIDs {
					sourceInUploads, err := compatibilityFolderWithinUserUploads(ctx, tx, projectID, sourceFolderID)
					if err != nil {
						return err
					}
					if sourceInUploads {
						return ErrCompatibilityArtifactUploadMove
					}
				}
			}
		}

		if deleteArtifacts && len(artifactIDs) > 0 {
			blobRows, err := tx.QueryContext(ctx, `
				SELECT storage_path FROM artifact_versions
				WHERE artifact_id IN (`+sqlQuestionMarks(len(artifactIDs))+`) AND storage_path <> ''`, stringSliceAny(artifactIDs)...)
			if err != nil {
				return fmt.Errorf("list compatibility deleted artifact blobs: %w", err)
			}
			for blobRows.Next() {
				var path string
				if err := blobRows.Scan(&path); err != nil {
					_ = blobRows.Close()
					return fmt.Errorf("scan compatibility deleted artifact blob: %w", err)
				}
				result.BlobPaths = append(result.BlobPaths, path)
			}
			if err := blobRows.Err(); err != nil {
				_ = blobRows.Close()
				return fmt.Errorf("iterate compatibility deleted artifact blobs: %w", err)
			}
			if err := blobRows.Close(); err != nil {
				return fmt.Errorf("close compatibility deleted artifact blobs: %w", err)
			}
			mutation, err := tx.ExecContext(ctx, `DELETE FROM artifacts WHERE id IN (`+sqlQuestionMarks(len(artifactIDs))+`)`, stringSliceAny(artifactIDs)...)
			if err != nil {
				return fmt.Errorf("delete compatibility folder artifacts: %w", err)
			}
			result.ArtifactsDeleted, err = mutation.RowsAffected()
			if err != nil {
				return fmt.Errorf("count deleted compatibility folder artifacts: %w", err)
			}
		} else if !deleteArtifacts && len(artifactIDs) > 0 {
			moveArguments := []any{nullableString(moveArtifactsTo), s.now().UTC()}
			moveArguments = append(moveArguments, stringSliceAny(folderIDs)...)
			mutation, err := tx.ExecContext(ctx, `
				UPDATE artifacts SET folder_id = ?, updated_at = ?
				WHERE folder_id IN (`+sqlQuestionMarks(len(folderIDs))+`)`, moveArguments...)
			if err != nil {
				return fmt.Errorf("move compatibility folder artifacts: %w", err)
			}
			result.ArtifactsMoved, err = mutation.RowsAffected()
			if err != nil {
				return fmt.Errorf("count moved compatibility folder artifacts: %w", err)
			}
		}

		for i := len(folderIDs) - 1; i >= 0; i-- {
			if _, err := tx.ExecContext(ctx, `DELETE FROM artifact_folders WHERE id = ? AND project_id = ?`, folderIDs[i], projectID); err != nil {
				return fmt.Errorf("delete compatibility folder %s: %w", folderIDs[i], err)
			}
		}
		for _, id := range folderIDs {
			if err := s.enqueueProjectDomainEventTx(ctx, tx, ownerUserID, projectID, "folder_deleted", id, map[string]any{"folder_id": id}); err != nil {
				return err
			}
		}
		result.FoldersDeleted = len(folderIDs)
		return nil
	})
	return result, err
}

func (s *Store) UpdateCompatibilityArtifactFolderRealtime(
	ctx context.Context, ownerUserID, artifactID string, input UpdateCompatibilityArtifactFolderInput,
) (CompatibilityArtifactFolderUpdate, error) {
	result := CompatibilityArtifactFolderUpdate{Status: "updated"}
	if s == nil || s.db == nil {
		return result, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ownerUserID, artifactID = strings.TrimSpace(ownerUserID), strings.TrimSpace(artifactID)
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var projectID string
		var currentFolder sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT project_id, folder_id FROM artifacts WHERE id = ?`, artifactID).Scan(&projectID, &currentFolder); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: %s", ErrCompatibilityArtifactNotFound, artifactID)
			}
			return fmt.Errorf("get compatibility artifact folder: %w", err)
		}
		var ownedProjectID string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM projects WHERE id = ? AND user_id = ?`, projectID, ownerUserID).Scan(&ownedProjectID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: %s", ErrCompatibilityArtifactNotFound, artifactID)
			}
			return fmt.Errorf("verify compatibility artifact owner: %w", err)
		}
		if !input.FolderSet && !input.SortOrderSet {
			result.FolderID = nullableStringPointer(currentFolder)
			return nil
		}
		if input.FolderSet && !input.FolderNull {
			var folderProjectID string
			if err := tx.QueryRowContext(ctx, `SELECT project_id FROM artifact_folders WHERE id = ?`, input.FolderID).Scan(&folderProjectID); err != nil || folderProjectID != projectID {
				return fmt.Errorf("%w: target folder %s not found", ErrCompatibilityFolderMoveTarget, input.FolderID)
			}
		}
		if input.FolderSet {
			sourceInUploads, err := compatibilityFolderWithinUserUploads(ctx, tx, projectID, currentFolder.String)
			if err != nil {
				return err
			}
			if sourceInUploads {
				targetFolderID := input.FolderID
				if input.FolderNull {
					targetFolderID = ""
				}
				targetInUploads, err := compatibilityFolderWithinUserUploads(ctx, tx, projectID, targetFolderID)
				if err != nil {
					return err
				}
				if !targetInUploads {
					return ErrCompatibilityArtifactUploadMove
				}
			}
			folderValue := any(nil)
			if !input.FolderNull {
				folderValue = input.FolderID
			}
			if _, err := tx.ExecContext(ctx, `UPDATE artifacts SET folder_id = ?, updated_at = ? WHERE id = ?`, folderValue, s.now().UTC(), artifactID); err != nil {
				return fmt.Errorf("update compatibility artifact folder: %w", err)
			}
			if !input.FolderNull {
				copy := input.FolderID
				result.FolderID = &copy
			}
		}
		if input.SortOrderSet {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO artifact_runtime_metadata (artifact_id, sort_order) VALUES (?, ?)
				ON CONFLICT(artifact_id) DO UPDATE SET sort_order = excluded.sort_order`, artifactID, input.SortOrder); err != nil {
				return fmt.Errorf("update compatibility artifact sort order: %w", err)
			}
			copy := input.SortOrder
			result.SortOrder = &copy
			if !input.FolderSet {
				if _, err := tx.ExecContext(ctx, `UPDATE artifacts SET updated_at = ? WHERE id = ?`, s.now().UTC(), artifactID); err != nil {
					return fmt.Errorf("touch compatibility artifact sort update: %w", err)
				}
			}
		}
		return s.enqueueProjectDomainEventTx(ctx, tx, ownerUserID, projectID, "artifact_moved", artifactID, map[string]any{
			"artifact_id": artifactID, "old_folder_id": nullableStringPointer(currentFolder), "new_folder_id": result.FolderID,
		})
	})
	return result, err
}

func requireCompatibilityFolderProject(ctx context.Context, tx *sql.Tx, ownerUserID, projectID string) error {
	var found string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM projects WHERE id = ? AND user_id = ?`, projectID, ownerUserID).Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s", ErrCompatibilityFolderProjectNotFound, projectID)
		}
		return fmt.Errorf("get compatibility folder project owner: %w", err)
	}
	return nil
}

func validateCompatibilityFolderParent(ctx context.Context, tx *sql.Tx, projectID, parentID, movingFolderID string) error {
	parentID = strings.TrimSpace(parentID)
	if parentID == "" {
		return nil
	}
	current := parentID
	visited := make(map[string]bool)
	for current != "" {
		if current == movingFolderID {
			return ErrCompatibilityFolderCycle
		}
		if visited[current] {
			return ErrCompatibilityFolderCycle
		}
		visited[current] = true
		var next sql.NullString
		var conversation bool
		err := tx.QueryRowContext(ctx, `
			SELECT parent_id, is_conversation_folder
			FROM artifact_folders WHERE id = ? AND project_id = ?`, current, projectID,
		).Scan(&next, &conversation)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s", ErrCompatibilityFolderParentNotFound, parentID)
		}
		if err != nil {
			return fmt.Errorf("walk compatibility folder ancestry: %w", err)
		}
		if conversation {
			return ErrCompatibilityFolderConversationParent
		}
		current = ""
		if next.Valid {
			current = next.String
		}
	}
	return nil
}

func compatibilityFolderWithinUserUploads(ctx context.Context, tx *sql.Tx, projectID, folderID string) (bool, error) {
	folderID = strings.TrimSpace(folderID)
	if folderID == "" {
		return false, nil
	}
	visited := make(map[string]bool)
	for folderID != "" {
		if visited[folderID] {
			return false, ErrCompatibilityFolderCycle
		}
		visited[folderID] = true
		var parentID sql.NullString
		var uploads bool
		if err := tx.QueryRowContext(ctx, `
			SELECT parent_id, is_user_uploads_folder FROM artifact_folders
			WHERE id = ? AND project_id = ?`, folderID, projectID,
		).Scan(&parentID, &uploads); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil
			}
			return false, fmt.Errorf("walk compatibility user uploads ancestry: %w", err)
		}
		if uploads {
			return true, nil
		}
		folderID = ""
		if parentID.Valid {
			folderID = parentID.String
		}
	}
	return false, nil
}

func stringSliceAny(values []string) []any {
	result := make([]any, len(values))
	for i := range values {
		result[i] = values[i]
	}
	return result
}

func scanCompatibilityArtifactFolder(scanner rowScanner) (CompatibilityArtifactFolder, error) {
	var folder CompatibilityArtifactFolder
	var parentID, rootFrameID sql.NullString
	if err := scanner.Scan(
		&folder.ID, &folder.ProjectID, &parentID, &folder.Name, &folder.SortOrder, &rootFrameID,
		&folder.IsConversationFolder, &folder.IsUserUploadsFolder, &folder.CreatedAt, &folder.UpdatedAt,
		&folder.ArtifactCount,
	); err != nil {
		return CompatibilityArtifactFolder{}, err
	}
	folder.ParentID, folder.RootFrameID = nullableStringPointer(parentID), nullableStringPointer(rootFrameID)
	return folder, nil
}

func compatibilityFolderStorageProjection(folder CompatibilityArtifactFolder) ArtifactFolder {
	projection := ArtifactFolder{
		ID: folder.ID, ProjectID: folder.ProjectID, Name: folder.Name, SortOrder: folder.SortOrder,
		IsConversationFolder: folder.IsConversationFolder, IsUserUploadsFolder: folder.IsUserUploadsFolder,
		CreatedAt: folder.CreatedAt, UpdatedAt: folder.UpdatedAt,
	}
	if folder.ParentID != nil {
		projection.ParentID = *folder.ParentID
	}
	if folder.RootFrameID != nil {
		projection.RootFrameID = *folder.RootFrameID
	}
	return projection
}

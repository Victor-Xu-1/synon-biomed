package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ArtifactFolder struct {
	ID                   string    `json:"id"`
	ProjectID            string    `json:"projectId"`
	ParentID             string    `json:"parentId,omitempty"`
	Name                 string    `json:"name"`
	SortOrder            int       `json:"sortOrder"`
	RootFrameID          string    `json:"rootFrameId,omitempty"`
	IsConversationFolder bool      `json:"isConversationFolder"`
	IsUserUploadsFolder  bool      `json:"isUserUploadsFolder"`
	CreatedAt            time.Time `json:"createdAt"`
	UpdatedAt            time.Time `json:"updatedAt"`
}

type CreateArtifactFolderInput struct {
	ID                   string
	ProjectID            string
	ParentID             string
	Name                 string
	SortOrder            int
	RootFrameID          string
	IsConversationFolder bool
	IsUserUploadsFolder  bool
}

type UpdateArtifactFolderInput struct {
	ParentID  *string
	Name      *string
	SortOrder *int
}

type ProjectNote struct {
	ID                 string    `json:"id"`
	ProjectID          string    `json:"projectId"`
	UserID             string    `json:"userId"`
	TargetType         string    `json:"targetType"`
	TargetFrameID      string    `json:"targetFrameId"`
	TargetMessageIndex *int      `json:"targetMessageIndex,omitempty"`
	TargetArtifactID   string    `json:"targetArtifactId,omitempty"`
	Content            string    `json:"content"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

type CreateProjectNoteInput struct {
	ID                 string
	ProjectID          string
	UserID             string
	TargetType         string
	TargetFrameID      string
	TargetMessageIndex *int
	TargetArtifactID   string
	Content            string
}

func (s *Store) ListArtifactFolders(projectID string) ([]ArtifactFolder, error) {
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT id, project_id, COALESCE(parent_id, ''), name, sort_order,
			COALESCE(root_frame_id, ''), is_conversation_folder, is_user_uploads_folder,
			created_at, updated_at
		FROM artifact_folders WHERE project_id = ?
		ORDER BY COALESCE(parent_id, ''), sort_order, name, id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list artifact folders: %w", err)
	}
	defer rows.Close()
	folders := make([]ArtifactFolder, 0)
	for rows.Next() {
		folder, err := scanArtifactFolder(rows)
		if err != nil {
			return nil, err
		}
		folders = append(folders, folder)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate artifact folders: %w", err)
	}
	return folders, nil
}

func (s *Store) CreateArtifactFolder(input CreateArtifactFolderInput) (ArtifactFolder, error) {
	for field, value := range map[string]string{
		"folder id": input.ID, "folder project id": input.ProjectID, "folder name": input.Name,
	} {
		if strings.TrimSpace(value) == "" {
			return ArtifactFolder{}, fmt.Errorf("%s is required", field)
		}
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ArtifactFolder{}, fmt.Errorf("begin artifact folder create: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateFolderReferences(ctx, tx, input.ProjectID, input.ParentID, input.RootFrameID); err != nil {
		return ArtifactFolder{}, err
	}
	now := s.now().UTC()
	folder := ArtifactFolder{
		ID: input.ID, ProjectID: input.ProjectID, ParentID: input.ParentID, Name: input.Name,
		SortOrder: input.SortOrder, RootFrameID: input.RootFrameID,
		IsConversationFolder: input.IsConversationFolder, IsUserUploadsFolder: input.IsUserUploadsFolder,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artifact_folders (
			id, project_id, parent_id, name, sort_order, root_frame_id,
			is_conversation_folder, is_user_uploads_folder, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		folder.ID, folder.ProjectID, nullableString(folder.ParentID), folder.Name,
		folder.SortOrder, nullableString(folder.RootFrameID), folder.IsConversationFolder,
		folder.IsUserUploadsFolder, folder.CreatedAt, folder.UpdatedAt,
	); err != nil {
		return ArtifactFolder{}, fmt.Errorf("insert artifact folder: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ArtifactFolder{}, fmt.Errorf("commit artifact folder create: %w", err)
	}
	return folder, nil
}

func (s *Store) UpdateArtifactFolder(id string, input UpdateArtifactFolderInput) (ArtifactFolder, error) {
	if input.ParentID == nil && input.Name == nil && input.SortOrder == nil {
		return ArtifactFolder{}, errors.New("at least one folder field is required")
	}
	folder, found, err := s.GetArtifactFolder(id)
	if err != nil {
		return ArtifactFolder{}, err
	}
	if !found {
		return ArtifactFolder{}, fmt.Errorf("folder %q does not exist", id)
	}
	if input.Name != nil && strings.TrimSpace(*input.Name) == "" {
		return ArtifactFolder{}, errors.New("folder name cannot be empty")
	}
	if input.ParentID != nil {
		if *input.ParentID == id {
			return ArtifactFolder{}, errors.New("folder cannot be its own parent")
		}
		if err := validateFolderReferences(context.Background(), s.db, folder.ProjectID, *input.ParentID, ""); err != nil {
			return ArtifactFolder{}, err
		}
	}
	parentValue := any(nil)
	parentChanged := input.ParentID != nil
	if input.ParentID != nil {
		parentValue = nullableString(*input.ParentID)
	}
	result, err := s.db.ExecContext(context.Background(), `
		UPDATE artifact_folders SET
			parent_id = CASE WHEN ? THEN ? ELSE parent_id END,
			name = COALESCE(?, name), sort_order = COALESCE(?, sort_order), updated_at = ?
		WHERE id = ?`,
		parentChanged, parentValue, input.Name, input.SortOrder, s.now().UTC(), id,
	)
	if err != nil {
		return ArtifactFolder{}, fmt.Errorf("update artifact folder: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ArtifactFolder{}, fmt.Errorf("folder %q does not exist", id)
	}
	folder, _, err = s.GetArtifactFolder(id)
	return folder, err
}

func (s *Store) GetArtifactFolder(id string) (ArtifactFolder, bool, error) {
	row := s.db.QueryRowContext(context.Background(), `
		SELECT id, project_id, COALESCE(parent_id, ''), name, sort_order,
			COALESCE(root_frame_id, ''), is_conversation_folder, is_user_uploads_folder,
			created_at, updated_at
		FROM artifact_folders WHERE id = ?`, id)
	folder, err := scanArtifactFolder(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ArtifactFolder{}, false, nil
	}
	if err != nil {
		return ArtifactFolder{}, false, err
	}
	return folder, true, nil
}

func (s *Store) DeleteArtifactFolder(id string) error {
	result, err := s.db.ExecContext(context.Background(), `DELETE FROM artifact_folders WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete artifact folder: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return fmt.Errorf("folder %q does not exist", id)
	}
	return nil
}

func (s *Store) SetArtifactFolder(artifactID, folderID string) (Artifact, error) {
	artifact, found, err := s.GetArtifact(artifactID)
	if err != nil {
		return Artifact{}, err
	}
	if !found {
		return Artifact{}, fmt.Errorf("artifact %q does not exist", artifactID)
	}
	if strings.TrimSpace(folderID) != "" {
		folder, found, err := s.GetArtifactFolder(folderID)
		if err != nil {
			return Artifact{}, err
		}
		if !found || folder.ProjectID != artifact.ProjectID {
			return Artifact{}, fmt.Errorf("folder %q does not belong to artifact project", folderID)
		}
	}
	if _, err := s.db.ExecContext(context.Background(),
		`UPDATE artifacts SET folder_id = ?, updated_at = ? WHERE id = ?`,
		nullableString(folderID), s.now().UTC(), artifactID,
	); err != nil {
		return Artifact{}, fmt.Errorf("set artifact folder: %w", err)
	}
	artifact, _, err = s.GetArtifact(artifactID)
	return artifact, err
}

func (s *Store) ListProjectNotes(projectID string) ([]ProjectNote, error) {
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT id, project_id, user_id, target_type, target_frame_id,
			target_message_index, COALESCE(target_artifact_id, ''), content, created_at, updated_at
		FROM notes WHERE project_id = ? ORDER BY created_at, id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list project notes: %w", err)
	}
	defer rows.Close()
	notes := make([]ProjectNote, 0)
	for rows.Next() {
		note, err := scanProjectNote(rows)
		if err != nil {
			return nil, err
		}
		notes = append(notes, note)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project notes: %w", err)
	}
	return notes, nil
}

func (s *Store) CreateProjectNote(input CreateProjectNoteInput) (ProjectNote, error) {
	for field, value := range map[string]string{
		"note id": input.ID, "note project id": input.ProjectID, "note user id": input.UserID,
		"note target type": input.TargetType, "note target frame id": input.TargetFrameID,
		"note content": input.Content,
	} {
		if strings.TrimSpace(value) == "" {
			return ProjectNote{}, fmt.Errorf("%s is required", field)
		}
	}
	var frameProject string
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT project_id FROM frames WHERE id = ?`, input.TargetFrameID,
	).Scan(&frameProject); err != nil || frameProject != input.ProjectID {
		return ProjectNote{}, errors.New("note target frame must belong to project")
	}
	if input.TargetArtifactID != "" {
		var artifactProject string
		if err := s.db.QueryRowContext(context.Background(),
			`SELECT project_id FROM artifacts WHERE id = ?`, input.TargetArtifactID,
		).Scan(&artifactProject); err != nil || artifactProject != input.ProjectID {
			return ProjectNote{}, errors.New("note target artifact must belong to project")
		}
	}
	now := s.now().UTC()
	note := ProjectNote{
		ID: input.ID, ProjectID: input.ProjectID, UserID: input.UserID,
		TargetType: input.TargetType, TargetFrameID: input.TargetFrameID,
		TargetMessageIndex: input.TargetMessageIndex, TargetArtifactID: input.TargetArtifactID,
		Content: input.Content, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := s.db.ExecContext(context.Background(), `
		INSERT INTO notes (
			id, project_id, user_id, target_type, target_frame_id,
			target_message_index, target_artifact_id, content, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		note.ID, note.ProjectID, note.UserID, note.TargetType, note.TargetFrameID,
		note.TargetMessageIndex, nullableString(note.TargetArtifactID), note.Content,
		note.CreatedAt, note.UpdatedAt,
	); err != nil {
		return ProjectNote{}, fmt.Errorf("insert project note: %w", err)
	}
	return note, nil
}

func (s *Store) UpdateProjectNote(id, userID, content string) (ProjectNote, error) {
	if strings.TrimSpace(content) == "" {
		return ProjectNote{}, errors.New("note content is required")
	}
	result, err := s.db.ExecContext(context.Background(),
		`UPDATE notes SET content = ?, updated_at = ? WHERE id = ? AND user_id = ?`,
		content, s.now().UTC(), id, userID,
	)
	if err != nil {
		return ProjectNote{}, fmt.Errorf("update project note: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ProjectNote{}, fmt.Errorf("note %q does not exist", id)
	}
	return s.GetProjectNote(id, userID)
}

func (s *Store) GetProjectNote(id, userID string) (ProjectNote, error) {
	row := s.db.QueryRowContext(context.Background(), `
		SELECT id, project_id, user_id, target_type, target_frame_id,
			target_message_index, COALESCE(target_artifact_id, ''), content, created_at, updated_at
		FROM notes WHERE id = ? AND user_id = ?`, id, userID)
	note, err := scanProjectNote(row)
	if err != nil {
		return ProjectNote{}, fmt.Errorf("get project note: %w", err)
	}
	return note, nil
}

func (s *Store) DeleteProjectNote(id, userID string) error {
	result, err := s.db.ExecContext(context.Background(),
		`DELETE FROM notes WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return fmt.Errorf("delete project note: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return fmt.Errorf("note %q does not exist", id)
	}
	return nil
}

type folderReferenceQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func validateFolderReferences(ctx context.Context, db folderReferenceQuerier, projectID, parentID, rootFrameID string) error {
	var found string
	if err := db.QueryRowContext(ctx, `SELECT id FROM projects WHERE id = ?`, projectID).Scan(&found); err != nil {
		return fmt.Errorf("folder project %q does not exist", projectID)
	}
	if parentID != "" {
		var parentProject string
		if err := db.QueryRowContext(ctx, `SELECT project_id FROM artifact_folders WHERE id = ?`, parentID).Scan(&parentProject); err != nil || parentProject != projectID {
			return errors.New("folder parent must belong to project")
		}
	}
	if rootFrameID != "" {
		var frameProject string
		if err := db.QueryRowContext(ctx, `SELECT project_id FROM frames WHERE id = ?`, rootFrameID).Scan(&frameProject); err != nil || frameProject != projectID {
			return errors.New("folder root frame must belong to project")
		}
	}
	return nil
}

func scanArtifactFolder(row rowScanner) (ArtifactFolder, error) {
	var folder ArtifactFolder
	if err := row.Scan(
		&folder.ID, &folder.ProjectID, &folder.ParentID, &folder.Name, &folder.SortOrder,
		&folder.RootFrameID, &folder.IsConversationFolder, &folder.IsUserUploadsFolder,
		&folder.CreatedAt, &folder.UpdatedAt,
	); err != nil {
		return ArtifactFolder{}, err
	}
	return folder, nil
}

func scanProjectNote(row rowScanner) (ProjectNote, error) {
	var note ProjectNote
	var messageIndex sql.NullInt64
	if err := row.Scan(
		&note.ID, &note.ProjectID, &note.UserID, &note.TargetType, &note.TargetFrameID,
		&messageIndex, &note.TargetArtifactID, &note.Content, &note.CreatedAt, &note.UpdatedAt,
	); err != nil {
		return ProjectNote{}, err
	}
	if messageIndex.Valid {
		value := int(messageIndex.Int64)
		note.TargetMessageIndex = &value
	}
	return note, nil
}

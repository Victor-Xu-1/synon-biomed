package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	maxMemoryCategoriesPerUser  = 10
	maxMemoryCategoryNameRunes  = 64
	maxMemoryCategoryGuideRunes = 280
)

var reservedMemoryCategoryNames = map[string]struct{}{
	"profile": {}, "project": {}, "artifact": {}, "frame": {}, "category": {},
	"about you": {}, "scratchpad": {},
}

type MemoryCategory struct {
	ID         string    `json:"id"`
	UserID     string    `json:"userId,omitempty"`
	Name       string    `json:"name"`
	Guidance   string    `json:"guidance"`
	AutoRecall bool      `json:"auto_recall"`
	RowCount   int       `json:"row_count"`
	CreatedAt  time.Time `json:"createdAt,omitempty"`
	UpdatedAt  time.Time `json:"updatedAt,omitempty"`
}

type UpdateMemoryCategoryInput struct {
	Name       *string
	Guidance   *string
	AutoRecall *bool
}

type UpdateMemoryInput struct {
	Body              *string
	Evidence          *string
	SubjectProjectID  *string
	SubjectArtifactID *string
	CategoryID        *string
	ClearCategory     bool
}

// ResolvedMemoryEntity separates the stored subject from the owning project
// used for authorization and project-scoped model selection.
type ResolvedMemoryEntity struct {
	SubjectProjectID  string
	SubjectArtifactID string
	OwningProjectID   string
}

type MemoryEntityGroup struct {
	EntityKey string   `json:"entity_key"`
	Label     string   `json:"label"`
	ProjectID string   `json:"project_id,omitempty"`
	Rows      []Memory `json:"rows"`
}

type MemorySessionSummary struct {
	FrameID   string     `json:"frame_id"`
	ProjectID string     `json:"project_id"`
	Label     string     `json:"label"`
	RowCount  int        `json:"row_count"`
	NewestAt  *time.Time `json:"newest_at"`
}

type DeleteMemoryCategoryResult struct {
	Deleted      bool `json:"deleted"`
	FactsDeleted int  `json:"facts_deleted"`
}

func (s *Store) MemoryEnabledWithDefault(ctx context.Context, userID string, configuredDefault bool) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("workspace store is closed")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false, errors.New("memory user id is required")
	}
	var enabled bool
	err := s.db.QueryRowContext(ctx, `SELECT enabled FROM memory_user_settings WHERE user_id = ?`, userID).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return configuredDefault, nil
	}
	if err != nil {
		return false, fmt.Errorf("read memory enabled setting: %w", err)
	}
	return enabled, nil
}

func (s *Store) SetMemoryEnabled(ctx context.Context, userID string, enabled bool) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return errors.New("memory user id is required")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO memory_user_settings (user_id, enabled, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET enabled = excluded.enabled, updated_at = excluded.updated_at`,
		userID, enabled, s.now().UTC())
	if err != nil {
		return fmt.Errorf("persist memory enabled setting: %w", err)
	}
	return nil
}

func (s *Store) ProjectMemoryEnabledSetting(ctx context.Context, projectID, userID string) (*bool, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	var owner string
	var enabled sql.NullBool
	err := s.db.QueryRowContext(ctx, `SELECT user_id, memory_enabled FROM projects WHERE id = ?`, strings.TrimSpace(projectID)).Scan(&owner, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("read project memory setting: %w", err)
	}
	if owner != strings.TrimSpace(userID) {
		return nil, sql.ErrNoRows
	}
	if !enabled.Valid {
		return nil, nil
	}
	value := enabled.Bool
	return &value, nil
}

func (s *Store) SetProjectMemoryEnabled(ctx context.Context, projectID, userID string, enabled bool) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE projects SET memory_enabled = ?, updated_at = ? WHERE id = ? AND user_id = ?`,
		enabled, s.now().UTC(), strings.TrimSpace(projectID), strings.TrimSpace(userID))
	if err != nil {
		return fmt.Errorf("update project memory setting: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count project memory setting update: %w", err)
	}
	if changed != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) ListMemoryCategories(ctx context.Context, userID string) ([]MemoryCategory, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	return s.listMemoryCategories(ctx, strings.TrimSpace(userID))
}

func (s *Store) CreateMemoryCategory(ctx context.Context, userID, name, guidance string, autoRecall bool) (MemoryCategory, error) {
	if s == nil || s.db == nil {
		return MemoryCategory{}, errors.New("workspace store is closed")
	}
	userID = strings.TrimSpace(userID)
	name, guidance, err := normalizeMemoryCategory(name, guidance)
	if err != nil {
		return MemoryCategory{}, err
	}
	var category MemoryCategory
	err = s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_categories WHERE user_id = ?`, userID).Scan(&count); err != nil {
			return fmt.Errorf("count memory categories: %w", err)
		}
		if count >= maxMemoryCategoriesPerUser {
			return fmt.Errorf("category limit reached (%d)", maxMemoryCategoriesPerUser)
		}
		now := s.now().UTC()
		category = MemoryCategory{
			ID: "memcat_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12], UserID: userID,
			Name: name, Guidance: guidance, AutoRecall: autoRecall, CreatedAt: now, UpdatedAt: now,
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_categories
			(id, user_id, name, name_lower, guidance, auto_recall, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, category.ID, userID, name, strings.ToLower(name), guidance, autoRecall, now, now); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				return fmt.Errorf("a category named %q already exists", name)
			}
			return fmt.Errorf("insert memory category: %w", err)
		}
		return nil
	})
	return category, err
}

func (s *Store) UpdateMemoryCategory(ctx context.Context, userID, categoryID string, input UpdateMemoryCategoryInput) (MemoryCategory, error) {
	if s == nil || s.db == nil {
		return MemoryCategory{}, errors.New("workspace store is closed")
	}
	if input.Name == nil && input.Guidance == nil && input.AutoRecall == nil {
		return MemoryCategory{}, errors.New("at least one category field is required")
	}
	var updated MemoryCategory
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var current MemoryCategory
		err := tx.QueryRowContext(ctx, `SELECT id, user_id, name, guidance, auto_recall, created_at, updated_at
			FROM memory_categories WHERE id = ? AND user_id = ?`, strings.TrimSpace(categoryID), strings.TrimSpace(userID)).Scan(
			&current.ID, &current.UserID, &current.Name, &current.Guidance, &current.AutoRecall, &current.CreatedAt, &current.UpdatedAt)
		if err != nil {
			return err
		}
		name, guidance := current.Name, current.Guidance
		if input.Name != nil {
			name = *input.Name
		}
		if input.Guidance != nil {
			guidance = *input.Guidance
		}
		name, guidance, err = normalizeMemoryCategory(name, guidance)
		if err != nil {
			return err
		}
		autoRecall := current.AutoRecall
		if input.AutoRecall != nil {
			autoRecall = *input.AutoRecall
		}
		now := s.now().UTC()
		if _, err := tx.ExecContext(ctx, `UPDATE memory_categories SET name = ?, name_lower = ?, guidance = ?, auto_recall = ?, updated_at = ?
			WHERE id = ? AND user_id = ?`, name, strings.ToLower(name), guidance, autoRecall, now, current.ID, current.UserID); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				return fmt.Errorf("a category named %q already exists", name)
			}
			return fmt.Errorf("update memory category: %w", err)
		}
		updated = current
		updated.Name, updated.Guidance, updated.AutoRecall, updated.UpdatedAt = name, guidance, autoRecall, now
		updated.RowCount, err = countMemoryCategoryRowsTx(ctx, tx, current.ID, current.UserID)
		if err != nil {
			return err
		}
		return nil
	})
	return updated, err
}

func (s *Store) DeleteMemoryCategory(ctx context.Context, userID, categoryID string, deleteFacts bool) (DeleteMemoryCategoryResult, error) {
	if s == nil || s.db == nil {
		return DeleteMemoryCategoryResult{}, errors.New("workspace store is closed")
	}
	result := DeleteMemoryCategoryResult{}
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var owner string
		if err := tx.QueryRowContext(ctx, `SELECT user_id FROM memory_categories WHERE id = ?`, strings.TrimSpace(categoryID)).Scan(&owner); err != nil {
			return err
		}
		if owner != strings.TrimSpace(userID) {
			return sql.ErrNoRows
		}
		if deleteFacts {
			var err error
			result.FactsDeleted, err = deleteMemoryCategoryFactsTx(ctx, tx, categoryID, owner)
			if err != nil {
				return err
			}
		} else if err := clearMemoryCategoryAssignmentsTx(ctx, tx, categoryID, owner); err != nil {
			return err
		}
		deleted, err := tx.ExecContext(ctx, `DELETE FROM memory_categories WHERE id = ? AND user_id = ?`, categoryID, owner)
		if err != nil {
			return fmt.Errorf("delete memory category: %w", err)
		}
		count, err := deleted.RowsAffected()
		if err != nil {
			return fmt.Errorf("count memory category deletion: %w", err)
		}
		result.Deleted = count == 1
		return nil
	})
	return result, err
}

func (s *Store) ResolveMemoryEntityScope(ctx context.Context, userID, entity string) (ResolvedMemoryEntity, error) {
	if s == nil || s.db == nil {
		return ResolvedMemoryEntity{}, errors.New("workspace store is closed")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ResolvedMemoryEntity{}, errors.New("memory user id is required")
	}
	entity = strings.TrimSpace(entity)
	switch {
	case entity == "", entity == "profile":
		return ResolvedMemoryEntity{}, nil
	case strings.HasPrefix(entity, "project:"):
		projectID := strings.TrimSpace(strings.TrimPrefix(entity, "project:"))
		if projectID == "" {
			return ResolvedMemoryEntity{}, errors.New("project memory entity requires an id")
		}
		var owner string
		if err := s.db.QueryRowContext(ctx, `SELECT user_id FROM projects WHERE id = ?`, projectID).Scan(&owner); err != nil {
			return ResolvedMemoryEntity{}, err
		}
		if owner != userID {
			return ResolvedMemoryEntity{}, sql.ErrNoRows
		}
		return ResolvedMemoryEntity{SubjectProjectID: projectID, OwningProjectID: projectID}, nil
	case strings.HasPrefix(entity, "artifact:"):
		artifactID := strings.TrimSpace(strings.TrimPrefix(entity, "artifact:"))
		if artifactID == "" {
			return ResolvedMemoryEntity{}, errors.New("artifact memory entity requires an id")
		}
		var projectID, owner string
		if err := s.db.QueryRowContext(ctx, `SELECT a.project_id, p.user_id FROM artifacts AS a JOIN projects AS p ON p.id = a.project_id WHERE a.id = ?`, artifactID).Scan(&projectID, &owner); err != nil {
			return ResolvedMemoryEntity{}, err
		}
		if owner != userID {
			return ResolvedMemoryEntity{}, sql.ErrNoRows
		}
		return ResolvedMemoryEntity{SubjectArtifactID: artifactID, OwningProjectID: projectID}, nil
	default:
		return ResolvedMemoryEntity{}, fmt.Errorf("unknown memory entity %q", entity)
	}
}

func (s *Store) ResolveMemoryEntity(ctx context.Context, userID, entity string) (projectID, artifactID string, err error) {
	resolved, err := s.ResolveMemoryEntityScope(ctx, userID, entity)
	if err != nil {
		return "", "", err
	}
	return resolved.SubjectProjectID, resolved.SubjectArtifactID, nil
}

func (s *Store) MemoryCategoryIDByName(ctx context.Context, userID, name string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM memory_categories WHERE user_id = ? AND name_lower = ?`,
		strings.TrimSpace(userID), strings.ToLower(strings.TrimSpace(name))).Scan(&id)
	return id, err
}

func (s *Store) MemoryCategoryExistsOwned(ctx context.Context, userID, categoryID string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("workspace store is closed")
	}
	var marker int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM memory_categories WHERE id = ? AND user_id = ?`,
		strings.TrimSpace(categoryID), strings.TrimSpace(userID)).Scan(&marker)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check memory category identity: %w", err)
	}
	return true, nil
}

func (s *Store) GetMemoryOwned(ctx context.Context, ownerUserID, memoryID string) (Memory, bool, error) {
	if s == nil || s.db == nil {
		return Memory{}, false, errors.New("workspace store is closed")
	}
	ownerUserID, memoryID = strings.TrimSpace(ownerUserID), strings.TrimSpace(memoryID)
	if ownerUserID == "" || memoryID == "" {
		return Memory{}, false, nil
	}
	memory, err := scanMemory(s.db.QueryRowContext(ctx, memorySelect+` WHERE m.id = ? AND m.user_id = ?`, memoryID, ownerUserID))
	if errors.Is(err, sql.ErrNoRows) {
		return Memory{}, false, nil
	}
	if err != nil {
		return Memory{}, false, fmt.Errorf("get owned memory: %w", err)
	}
	return memory, true, nil
}

func (s *Store) UpdateMemoryOwned(ctx context.Context, memoryID, userID string, input UpdateMemoryInput) (Memory, error) {
	if s == nil || s.db == nil {
		return Memory{}, errors.New("workspace store is closed")
	}
	var updated Memory
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		current, err := scanMemory(tx.QueryRowContext(ctx, memorySelect+` WHERE m.id = ? AND m.user_id = ?`, strings.TrimSpace(memoryID), strings.TrimSpace(userID)))
		if err != nil {
			return err
		}
		if current.SupersededBy != "" {
			return errors.New("memory has been superseded")
		}
		body, evidence := current.Body, current.Evidence
		if input.Body != nil {
			body = *input.Body
		}
		if input.Evidence != nil {
			evidence = strings.ToLower(strings.TrimSpace(*input.Evidence))
		}
		candidate := CreateMemoryInput{
			ID: current.ID, UserID: current.UserID, Body: body, Origin: current.Origin, Evidence: evidence,
			SubjectProjectID: current.SubjectProjectID, SubjectArtifactID: current.SubjectArtifactID,
			SubjectVersionID: current.SubjectVersionID, SubjectFrameID: current.SubjectFrameID,
			SourceFrameID: current.SourceFrameID, CategoryID: current.CategoryID,
		}
		if input.SubjectProjectID != nil {
			candidate.SubjectProjectID = strings.TrimSpace(*input.SubjectProjectID)
			candidate.SubjectVersionID, candidate.SubjectFrameID, candidate.SourceFrameID = "", "", ""
		}
		if input.SubjectArtifactID != nil {
			candidate.SubjectArtifactID = strings.TrimSpace(*input.SubjectArtifactID)
			candidate.SubjectVersionID, candidate.SubjectFrameID, candidate.SourceFrameID = "", "", ""
		}
		if input.ClearCategory {
			candidate.CategoryID = ""
		} else if input.CategoryID != nil {
			candidate.CategoryID = strings.TrimSpace(*input.CategoryID)
		}
		candidate, err = normalizeMemoryInput(candidate)
		if err != nil {
			return err
		}
		projectID, err := validateMemoryScopeTx(ctx, tx, candidate, userID)
		if err != nil {
			return err
		}
		if candidate.SubjectProjectID == "" {
			candidate.SubjectProjectID = projectID
		}
		now := s.now().UTC()
		result, err := tx.ExecContext(ctx, `UPDATE memories SET body = ?, evidence = ?, subject_project_id = ?,
			subject_artifact_id = ?, subject_version_id = ?, subject_frame_id = ?, source_frame_id = ?, category_id = ?, updated_at = ?
			WHERE id = ? AND user_id = ? AND superseded_by IS NULL`, candidate.Body, candidate.Evidence,
			nullableString(candidate.SubjectProjectID), nullableString(candidate.SubjectArtifactID), nullableString(candidate.SubjectVersionID),
			nullableString(candidate.SubjectFrameID), nullableString(candidate.SourceFrameID), nullableString(candidate.CategoryID), now, current.ID, userID)
		if err != nil {
			return fmt.Errorf("update memory: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return errors.New("memory was changed while updating")
		}
		updated, err = scanMemory(tx.QueryRowContext(ctx, memorySelect+` WHERE m.id = ? AND m.user_id = ?`, current.ID, userID))
		return err
	})
	return updated, err
}

func (s *Store) DeleteMemoryOwned(ctx context.Context, memoryID, userID string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("workspace store is closed")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM memories WHERE id = ? AND user_id = ?`, strings.TrimSpace(memoryID), strings.TrimSpace(userID))
	if err != nil {
		return false, fmt.Errorf("delete memory: %w", err)
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *Store) DeleteAllMemoriesForUser(ctx context.Context, userID string) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("workspace store is closed")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM memories WHERE user_id = ?`, strings.TrimSpace(userID))
	if err != nil {
		return 0, fmt.Errorf("delete user memories: %w", err)
	}
	count, err := result.RowsAffected()
	return int(count), err
}

func (s *Store) ListMemoriesForUser(ctx context.Context, userID, origin, projectID string, includeSuperseded bool) ([]Memory, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	query := memorySelect + ` WHERE m.user_id = ?`
	args := []any{strings.TrimSpace(userID)}
	if !includeSuperseded {
		query += ` AND m.superseded_by IS NULL`
	}
	if origin = strings.TrimSpace(origin); origin != "" {
		query += ` AND m.origin = ?`
		args = append(args, origin)
	}
	if projectID = strings.TrimSpace(projectID); projectID != "" {
		query += ` AND m.subject_project_id = ?`
		args = append(args, projectID)
	}
	query += ` ORDER BY m.updated_at DESC, m.id DESC`
	return scanMemoryRows(ctx, s.db, query, args...)
}

func (s *Store) ListMemoryContextRows(ctx context.Context, userID, projectID string) ([]Memory, error) {
	query := memorySelect + ` WHERE m.user_id = ? AND m.superseded_by IS NULL AND m.subject_frame_id IS NULL`
	args := []any{strings.TrimSpace(userID)}
	if projectID = strings.TrimSpace(projectID); projectID != "" {
		query += ` AND (m.subject_project_id IS NULL OR m.subject_project_id = '' OR m.subject_project_id = ?)`
		args = append(args, projectID)
	}
	query += ` ORDER BY m.updated_at DESC, m.id DESC`
	return scanMemoryRows(ctx, s.db, query, args...)
}

func (s *Store) GroupMemoryEntities(ctx context.Context, memories []Memory) ([]MemoryEntityGroup, error) {
	groups := map[string]*MemoryEntityGroup{}
	for _, memory := range memories {
		key, projectID := "profile", ""
		if memory.SubjectArtifactID != "" {
			key, projectID = "artifact:"+memory.SubjectArtifactID, memory.SubjectProjectID
		} else if memory.SubjectProjectID != "" {
			key, projectID = "project:"+memory.SubjectProjectID, memory.SubjectProjectID
		}
		group := groups[key]
		if group == nil {
			group = &MemoryEntityGroup{EntityKey: key, Label: key, ProjectID: projectID, Rows: []Memory{}}
			groups[key] = group
		}
		group.Rows = append(group.Rows, memory)
	}
	for _, group := range groups {
		switch {
		case group.EntityKey == "profile":
			group.Label = "Profile"
		case strings.HasPrefix(group.EntityKey, "project:"):
			_ = s.db.QueryRowContext(ctx, `SELECT name FROM projects WHERE id = ?`, group.ProjectID).Scan(&group.Label)
		case strings.HasPrefix(group.EntityKey, "artifact:"):
			artifactID := strings.TrimPrefix(group.EntityKey, "artifact:")
			_ = s.db.QueryRowContext(ctx, `SELECT name FROM artifacts WHERE id = ?`, artifactID).Scan(&group.Label)
		}
		if strings.TrimSpace(group.Label) == "" {
			group.Label = group.EntityKey
		}
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]MemoryEntityGroup, 0, len(keys))
	for _, key := range keys {
		result = append(result, *groups[key])
	}
	return result, nil
}

func (s *Store) ListMemorySessions(ctx context.Context, userID, projectID string) ([]MemorySessionSummary, error) {
	query := `SELECT f.id, f.project_id, COALESCE(NULLIF(f.name, ''), 'Untitled session'), COUNT(m.id), MAX(m.updated_at)
		FROM memories AS m JOIN frames AS f ON f.id = m.subject_frame_id JOIN projects AS p ON p.id = f.project_id
		WHERE m.user_id = ? AND p.user_id = ? AND m.superseded_by IS NULL`
	args := []any{strings.TrimSpace(userID), strings.TrimSpace(userID)}
	if projectID = strings.TrimSpace(projectID); projectID != "" {
		query += ` AND f.project_id = ?`
		args = append(args, projectID)
	}
	query += ` GROUP BY f.id ORDER BY MAX(m.updated_at) DESC, f.id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list memory sessions: %w", err)
	}
	defer rows.Close()
	sessions := make([]MemorySessionSummary, 0)
	for rows.Next() {
		var session MemorySessionSummary
		var newest sql.NullTime
		if err := rows.Scan(&session.FrameID, &session.ProjectID, &session.Label, &session.RowCount, &newest); err != nil {
			return nil, fmt.Errorf("scan memory session: %w", err)
		}
		if newest.Valid {
			value := newest.Time.UTC()
			session.NewestAt = &value
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (s *Store) ListFrameMemories(ctx context.Context, userID, frameID string) ([]Memory, error) {
	var owner string
	if err := s.db.QueryRowContext(ctx, `SELECT p.user_id FROM frames AS f JOIN projects AS p ON p.id = f.project_id WHERE f.id = ?`, strings.TrimSpace(frameID)).Scan(&owner); err != nil {
		return nil, err
	}
	if owner != strings.TrimSpace(userID) {
		return nil, sql.ErrNoRows
	}
	return scanMemoryRows(ctx, s.db, memorySelect+` WHERE m.user_id = ? AND m.subject_frame_id = ? AND m.superseded_by IS NULL ORDER BY m.updated_at DESC`, userID, frameID)
}

func (s *Store) ClearFrameMemories(ctx context.Context, userID, frameID string) (int, error) {
	if _, err := s.ListFrameMemories(ctx, userID, frameID); err != nil {
		return 0, err
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM memories WHERE user_id = ? AND subject_frame_id = ?`, strings.TrimSpace(userID), strings.TrimSpace(frameID))
	if err != nil {
		return 0, fmt.Errorf("clear frame memories: %w", err)
	}
	count, err := result.RowsAffected()
	return int(count), err
}

func (s *Store) CountMemoriesForUser(ctx context.Context, userID string) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("workspace store is closed")
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memories WHERE user_id = ?`, strings.TrimSpace(userID)).Scan(&count); err != nil {
		return 0, fmt.Errorf("count user memories: %w", err)
	}
	return count, nil
}

func scanMemoryRows(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, query string, args ...any) ([]Memory, error) {
	rows, err := queryer.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	memories := make([]Memory, 0)
	for rows.Next() {
		memory, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		memories = append(memories, memory)
	}
	return memories, rows.Err()
}

func normalizeMemoryCategory(name, guidance string) (string, string, error) {
	name = strings.TrimSpace(name)
	guidance = strings.TrimSpace(strings.Join(strings.Fields(guidance), " "))
	if name == "" {
		return "", "", errors.New("category name cannot be empty")
	}
	if utf8.RuneCountInString(name) > maxMemoryCategoryNameRunes {
		return "", "", fmt.Errorf("category name is too long (max %d characters)", maxMemoryCategoryNameRunes)
	}
	if strings.Contains(name, ":") {
		return "", "", errors.New("category name cannot contain ':'")
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return "", "", errors.New("category name cannot contain newlines or control characters")
		}
	}
	if _, reserved := reservedMemoryCategoryNames[strings.ToLower(name)]; reserved {
		return "", "", fmt.Errorf("%q is reserved by the built-in memory vocabulary", name)
	}
	if guidance == "" {
		return "", "", errors.New("guidance is required")
	}
	if utf8.RuneCountInString(guidance) > maxMemoryCategoryGuideRunes {
		return "", "", fmt.Errorf("guidance is too long (max %d characters)", maxMemoryCategoryGuideRunes)
	}
	return name, guidance, nil
}

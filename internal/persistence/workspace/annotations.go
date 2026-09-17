package workspace

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Annotation struct {
	ID              string
	ProjectID       string
	TargetKind      string
	TargetKey       string
	LabelIndex      int
	ContentChecksum string
	Body            map[string]any
	CreatedAt       time.Time
	UpdatedAt       *time.Time
}

type CreateAnnotationInput struct {
	ProjectID       string
	TargetKind      string
	TargetKey       string
	ContentChecksum string
	Body            map[string]any
}

type ApplyArtifactEditInput struct {
	ArtifactID              string
	ProjectID               string
	Name                    string
	Kind                    string
	Content                 []byte
	CreatedBy               string
	ParentVersionID         string
	FromAnnotationTargetKey string
}

const annotationSelect = `
	SELECT id, project_id, target_kind, target_key, label_idx,
		COALESCE(content_checksum, ''), body, created_at, updated_at
	FROM annotations`

func (s *Store) ListAnnotations(projectID, targetKey string) ([]Annotation, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	projectID, targetKey = strings.TrimSpace(projectID), strings.TrimSpace(targetKey)
	if projectID == "" || targetKey == "" {
		return nil, errors.New("annotation project id and target key are required")
	}
	rows, err := s.db.QueryContext(context.Background(), annotationSelect+`
		WHERE project_id = ? AND target_key = ? ORDER BY label_idx, created_at, id`, projectID, targetKey)
	if err != nil {
		return nil, fmt.Errorf("list annotations: %w", err)
	}
	defer rows.Close()
	annotations := []Annotation{}
	for rows.Next() {
		annotation, err := scanAnnotation(rows)
		if err != nil {
			return nil, err
		}
		annotations = append(annotations, annotation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate annotations: %w", err)
	}
	return annotations, nil
}

func (s *Store) CreateAnnotation(input CreateAnnotationInput) (Annotation, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		annotation, err := s.createAnnotationOnce(input)
		if err == nil {
			return annotation, nil
		}
		lastErr = err
		if !strings.Contains(err.Error(), "annotations.project_id, annotations.target_key, annotations.label_idx") {
			return Annotation{}, err
		}
	}
	return Annotation{}, fmt.Errorf("allocate annotation label after retries: %w", lastErr)
}

func (s *Store) createAnnotationOnce(input CreateAnnotationInput) (Annotation, error) {
	if s == nil || s.db == nil {
		return Annotation{}, errors.New("workspace store is closed")
	}
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.TargetKind = strings.TrimSpace(input.TargetKind)
	input.TargetKey = strings.TrimSpace(input.TargetKey)
	input.ContentChecksum = strings.TrimSpace(input.ContentChecksum)
	if input.ProjectID == "" || input.TargetKind == "" || input.TargetKey == "" {
		return Annotation{}, errors.New("annotation project, target kind, and target key are required")
	}
	if input.Body == nil {
		return Annotation{}, errors.New("annotation body is required")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Annotation{}, fmt.Errorf("begin annotation transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var projectExists string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM projects WHERE id = ?`, input.ProjectID).Scan(&projectExists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Annotation{}, fmt.Errorf("project %q does not exist", input.ProjectID)
		}
		return Annotation{}, fmt.Errorf("look up annotation project: %w", err)
	}
	var maxIndex sql.NullInt64
	if err := tx.QueryRowContext(ctx, `
		SELECT MAX(label_idx) FROM annotations WHERE project_id = ? AND target_key = ?`,
		input.ProjectID, input.TargetKey).Scan(&maxIndex); err != nil {
		return Annotation{}, fmt.Errorf("allocate annotation label: %w", err)
	}
	labelIndex := 0
	if maxIndex.Valid {
		labelIndex = int(maxIndex.Int64) + 1
	}
	now := s.now().UTC()
	annotation := Annotation{
		ID: uuid.NewString(), ProjectID: input.ProjectID, TargetKind: input.TargetKind,
		TargetKey: input.TargetKey, LabelIndex: labelIndex, ContentChecksum: input.ContentChecksum,
		Body: cloneAnnotationBody(input.Body), CreatedAt: now,
	}
	annotation.decorateBody()
	body, err := json.Marshal(annotation.Body)
	if err != nil {
		return Annotation{}, fmt.Errorf("marshal annotation body: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO annotations
			(id, project_id, target_kind, target_key, label_idx, content_checksum, body, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		annotation.ID, annotation.ProjectID, annotation.TargetKind, annotation.TargetKey,
		annotation.LabelIndex, nullableString(annotation.ContentChecksum), string(body), annotation.CreatedAt); err != nil {
		return Annotation{}, fmt.Errorf("insert annotation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Annotation{}, fmt.Errorf("commit annotation: %w", err)
	}
	return annotation, nil
}

func (s *Store) GetAnnotation(id string) (Annotation, bool, error) {
	if s == nil || s.db == nil {
		return Annotation{}, false, errors.New("workspace store is closed")
	}
	annotation, err := scanAnnotation(s.db.QueryRowContext(context.Background(), annotationSelect+` WHERE id = ?`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return Annotation{}, false, nil
	}
	if err != nil {
		return Annotation{}, false, err
	}
	return annotation, true, nil
}

func (s *Store) UpdateAnnotationText(id, text string) (Annotation, error) {
	if s == nil || s.db == nil {
		return Annotation{}, errors.New("workspace store is closed")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return Annotation{}, errors.New("annotation id is required")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Annotation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	annotation, err := scanAnnotation(tx.QueryRowContext(ctx, annotationSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Annotation{}, errors.New("annotation not found")
	}
	if err != nil {
		return Annotation{}, err
	}
	annotation.Body["text"] = text
	now := s.now().UTC()
	annotation.UpdatedAt = &now
	body, err := json.Marshal(annotation.Body)
	if err != nil {
		return Annotation{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE annotations SET body = ?, updated_at = ? WHERE id = ?`, string(body), now, id); err != nil {
		return Annotation{}, fmt.Errorf("update annotation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Annotation{}, err
	}
	return annotation, nil
}

func (s *Store) DeleteAnnotation(id string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("workspace store is closed")
	}
	result, err := s.db.ExecContext(context.Background(), `DELETE FROM annotations WHERE id = ?`, strings.TrimSpace(id))
	if err != nil {
		return false, fmt.Errorf("delete annotation: %w", err)
	}
	changed, err := result.RowsAffected()
	return changed > 0, err
}

func (s *Store) CarryForwardAnnotations(projectID, fromTargetKey, toTargetKey, checksum string) ([]Annotation, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	projectID = strings.TrimSpace(projectID)
	fromTargetKey = strings.TrimSpace(fromTargetKey)
	toTargetKey = strings.TrimSpace(toTargetKey)
	if projectID == "" || fromTargetKey == "" || toTargetKey == "" {
		return nil, errors.New("annotation project and source/target keys are required")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin annotation carry-forward: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	carried, err := s.carryForwardAnnotationsTx(ctx, tx, projectID, fromTargetKey, toTargetKey, checksum, s.now().UTC())
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit annotation carry-forward: %w", err)
	}
	return carried, nil
}

func (s *Store) ApplyArtifactEdit(input ApplyArtifactEditInput) (Artifact, ArtifactVersion, []Annotation, error) {
	if s == nil || s.db == nil {
		return Artifact{}, ArtifactVersion{}, nil, errors.New("workspace store is closed")
	}
	input.ArtifactID = strings.TrimSpace(input.ArtifactID)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.ParentVersionID = strings.TrimSpace(input.ParentVersionID)
	if input.ArtifactID == "" || input.ProjectID == "" || input.ParentVersionID == "" {
		return Artifact{}, ArtifactVersion{}, nil, errors.New("artifact, project, and parent version ids are required")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Artifact{}, ArtifactVersion{}, nil, fmt.Errorf("begin artifact edit: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var artifact Artifact
	if err := tx.QueryRowContext(ctx, `
		SELECT id, project_id, name, kind, current_version_number,
			COALESCE(folder_id, ''), priority, created_at, updated_at
		FROM artifacts WHERE id = ?`, input.ArtifactID).Scan(
		&artifact.ID, &artifact.ProjectID, &artifact.Name, &artifact.Kind,
		&artifact.CurrentVersionNumber, &artifact.FolderID, &artifact.Priority,
		&artifact.CreatedAt, &artifact.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Artifact{}, ArtifactVersion{}, nil, fmt.Errorf("artifact %q not found", input.ArtifactID)
		}
		return Artifact{}, ArtifactVersion{}, nil, fmt.Errorf("load artifact for edit: %w", err)
	}
	if artifact.ProjectID != input.ProjectID {
		return Artifact{}, ArtifactVersion{}, nil, fmt.Errorf("artifact %q belongs to project %q, not %q", artifact.ID, artifact.ProjectID, input.ProjectID)
	}
	var parentArtifactID string
	if err := tx.QueryRowContext(ctx, `SELECT artifact_id FROM artifact_versions WHERE id = ?`, input.ParentVersionID).Scan(&parentArtifactID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Artifact{}, ArtifactVersion{}, nil, fmt.Errorf("parent artifact version %q not found", input.ParentVersionID)
		}
		return Artifact{}, ArtifactVersion{}, nil, err
	}
	if parentArtifactID != artifact.ID {
		return Artifact{}, ArtifactVersion{}, nil, errors.New("parent artifact version belongs to another artifact")
	}
	now := s.now().UTC()
	digest := sha256.Sum256(input.Content)
	version := ArtifactVersion{
		ID: uuid.NewString(), ArtifactID: artifact.ID,
		VersionNumber: artifact.CurrentVersionNumber + 1, ParentID: input.ParentVersionID,
		Content: append([]byte(nil), input.Content...), ContentSHA256: hex.EncodeToString(digest[:]),
		SizeBytes: int64(len(input.Content)),
		CreatedBy: strings.TrimSpace(input.CreatedBy), CreatedAt: now,
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artifact_versions
			(id, artifact_id, version_number, parent_id, content, content_sha256, storage_path, size_bytes, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, '', ?, ?, ?)`,
		version.ID, version.ArtifactID, version.VersionNumber, version.ParentID,
		version.Content, version.ContentSHA256, len(version.Content), version.CreatedBy, version.CreatedAt); err != nil {
		return Artifact{}, ArtifactVersion{}, nil, fmt.Errorf("insert edited artifact version: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE artifacts SET current_version_number = ?, updated_at = ? WHERE id = ?`,
		version.VersionNumber, now, artifact.ID); err != nil {
		return Artifact{}, ArtifactVersion{}, nil, fmt.Errorf("advance edited artifact: %w", err)
	}
	carried, err := s.carryForwardAnnotationsTx(
		ctx, tx, artifact.ProjectID, strings.TrimSpace(input.FromAnnotationTargetKey),
		"av:"+version.ID, version.ContentSHA256, now,
	)
	if err != nil {
		return Artifact{}, ArtifactVersion{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return Artifact{}, ArtifactVersion{}, nil, fmt.Errorf("commit artifact edit: %w", err)
	}
	artifact.CurrentVersionNumber = version.VersionNumber
	artifact.UpdatedAt = now
	return artifact, version, carried, nil
}

func (s *Store) carryForwardAnnotationsTx(ctx context.Context, tx *sql.Tx, projectID, fromTargetKey, toTargetKey, checksum string, now time.Time) ([]Annotation, error) {
	if strings.TrimSpace(fromTargetKey) == "" {
		return []Annotation{}, nil
	}
	rows, err := tx.QueryContext(ctx, annotationSelect+`
		WHERE project_id = ? AND target_key = ? ORDER BY label_idx, created_at, id`, projectID, fromTargetKey)
	if err != nil {
		return nil, fmt.Errorf("list annotations for carry-forward: %w", err)
	}
	sources := []Annotation{}
	for rows.Next() {
		annotation, err := scanAnnotation(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		sources = append(sources, annotation)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		return []Annotation{}, nil
	}
	var maxIndex sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(label_idx) FROM annotations WHERE project_id = ? AND target_key = ?`, projectID, toTargetKey).Scan(&maxIndex); err != nil {
		return nil, fmt.Errorf("allocate carried annotation labels: %w", err)
	}
	nextIndex := 0
	if maxIndex.Valid {
		nextIndex = int(maxIndex.Int64) + 1
	}
	carried := make([]Annotation, 0, len(sources))
	for index, source := range sources {
		annotation := Annotation{
			ID: uuid.NewString(), ProjectID: projectID, TargetKind: source.TargetKind,
			TargetKey: toTargetKey, LabelIndex: nextIndex + index,
			ContentChecksum: strings.TrimSpace(checksum), Body: cloneAnnotationBody(source.Body), CreatedAt: now,
		}
		delete(annotation.Body, "id")
		delete(annotation.Body, "label")
		delete(annotation.Body, "created_at")
		annotation.decorateBody()
		body, err := json.Marshal(annotation.Body)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO annotations
				(id, project_id, target_kind, target_key, label_idx, content_checksum, body, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			annotation.ID, annotation.ProjectID, annotation.TargetKind, annotation.TargetKey,
			annotation.LabelIndex, nullableString(annotation.ContentChecksum), string(body), annotation.CreatedAt); err != nil {
			return nil, fmt.Errorf("insert carried annotation: %w", err)
		}
		carried = append(carried, annotation)
	}
	return carried, nil
}

func (annotation Annotation) Public() map[string]any {
	output := cloneAnnotationBody(annotation.Body)
	annotation.decorateMap(output)
	return output
}

func (annotation *Annotation) decorateBody() {
	annotation.decorateMap(annotation.Body)
}

func (annotation Annotation) decorateMap(output map[string]any) {
	output["id"] = annotation.ID
	output["label"] = annotationLabel(annotation.LabelIndex)
	output["target_key"] = annotation.TargetKey
	output["created_at"] = annotation.CreatedAt.UTC().Format(time.RFC3339Nano)
	if annotation.ContentChecksum != "" {
		output["content_checksum"] = annotation.ContentChecksum
	} else {
		delete(output, "content_checksum")
	}
}

func annotationLabel(index int) string {
	if index >= 0 && index < 20 {
		return string(rune(9312 + index))
	}
	return fmt.Sprintf("(%d)", index+1)
}

func cloneAnnotationBody(body map[string]any) map[string]any {
	cloned := make(map[string]any, len(body))
	for key, value := range body {
		cloned[key] = value
	}
	return cloned
}

type annotationRow interface {
	Scan(...any) error
}

func scanAnnotation(row annotationRow) (Annotation, error) {
	var annotation Annotation
	var rawBody string
	var updated sql.NullTime
	if err := row.Scan(
		&annotation.ID, &annotation.ProjectID, &annotation.TargetKind, &annotation.TargetKey,
		&annotation.LabelIndex, &annotation.ContentChecksum, &rawBody, &annotation.CreatedAt, &updated,
	); err != nil {
		return Annotation{}, err
	}
	if err := json.Unmarshal([]byte(rawBody), &annotation.Body); err != nil {
		return Annotation{}, fmt.Errorf("decode annotation %q: %w", annotation.ID, err)
	}
	if updated.Valid {
		annotation.UpdatedAt = &updated.Time
	}
	annotation.decorateBody()
	return annotation, nil
}

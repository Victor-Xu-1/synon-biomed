package workspace

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

type mutationIdempotencyContextKey struct{}

const maxMutationIdempotencyKeyBytes = 256

var ErrInvalidMutationIdempotencyKey = errors.New("invalid mutation idempotency key")

type mutationIdempotencyIdentity struct {
	key string
	err error
}

// WithMutationIdempotencyKey binds a caller-owned mutation identity to all
// outbox events produced by the transaction. Reusing the key with different
// event content is rejected by EnqueueOutboxTx.
func WithMutationIdempotencyKey(ctx context.Context, key string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if key == "" {
		key = uuid.NewString()
	}
	err := validateMutationIdempotencyKey(key)
	return context.WithValue(ctx, mutationIdempotencyContextKey{}, mutationIdempotencyIdentity{key: key, err: err})
}

func mutationIdempotencyKey(ctx context.Context) string {
	if ctx != nil {
		if identity, ok := ctx.Value(mutationIdempotencyContextKey{}).(mutationIdempotencyIdentity); ok && identity.err == nil && identity.key != "" {
			return identity.key
		}
	}
	return uuid.NewString()
}

func mutationIdempotencyError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	if identity, ok := ctx.Value(mutationIdempotencyContextKey{}).(mutationIdempotencyIdentity); ok {
		return identity.err
	}
	return nil
}

func validateMutationIdempotencyKey(key string) error {
	if len(key) == 0 || len(key) > maxMutationIdempotencyKeyBytes {
		return fmt.Errorf("%w: key must contain 1-%d bytes", ErrInvalidMutationIdempotencyKey, maxMutationIdempotencyKeyBytes)
	}
	for index := 0; index < len(key); index++ {
		if !isMutationTokenByte(key[index]) {
			return fmt.Errorf("%w: key must be an ASCII HTTP token", ErrInvalidMutationIdempotencyKey)
		}
	}
	return nil
}

func isMutationTokenByte(value byte) bool {
	if value >= '0' && value <= '9' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' {
		return true
	}
	return strings.ContainsRune("!#$%&'*+-.^_`|~", rune(value))
}

func domainRealtimeEventID(ctx context.Context, eventType, aggregateID string) string {
	digest := sha256.Sum256([]byte("synon-domain-realtime-v2\x00" + mutationIdempotencyKey(ctx) + "\x00" + eventType + "\x00" + aggregateID))
	return "domain:" + hex.EncodeToString(digest[:])
}

func (s *Store) enqueueProjectDomainEventTx(ctx context.Context, tx *sql.Tx, ownerUserID, projectID, eventType, aggregateID string, payload map[string]any) error {
	if err := mutationIdempotencyError(ctx); err != nil {
		return err
	}
	ownerUserID, projectID = strings.TrimSpace(ownerUserID), strings.TrimSpace(projectID)
	if err := requireProjectOwnerTx(ctx, tx, projectID, ownerUserID); err != nil {
		return err
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["project_id"] = projectID
	eventID := domainRealtimeEventID(ctx, eventType, aggregateID)
	_, err := s.EnqueueRealtimeOutboxTx(ctx, tx, RealtimeEventInput{
		ID: eventID, UserID: ownerUserID, ProjectID: projectID, Type: eventType, Payload: payload,
	}, "")
	return err
}

func artifactVersionRealtimePayload(artifact Artifact, version ArtifactVersion) map[string]any {
	return map[string]any{
		"artifact_id": artifact.ID, "version_id": version.ID, "version_ids": []string{version.ID},
		"version_number": version.VersionNumber, "name": artifact.Name, "kind": artifact.Kind,
		"artifact": map[string]any{
			"id": artifact.ID, "project_id": artifact.ProjectID, "filename": artifact.Name,
			"content_type": artifact.Kind, "version_id": version.ID, "version_number": version.VersionNumber,
			"size_bytes": version.SizeBytes, "created_by": version.CreatedBy,
		},
	}
}

func (s *Store) enqueueArtifactVersionEventsTx(ctx context.Context, tx *sql.Tx, ownerUserID string, artifact Artifact, version ArtifactVersion) error {
	return s.enqueueArtifactVersionEventsForFrameTx(ctx, tx, ownerUserID, artifact, version, "")
}

func (s *Store) enqueueArtifactVersionEventsForFrameTx(ctx context.Context, tx *sql.Tx, ownerUserID string, artifact Artifact, version ArtifactVersion, frameID string) error {
	payload := artifactVersionRealtimePayload(artifact, version)
	if strings.TrimSpace(frameID) != "" {
		payload["frame_id"] = strings.TrimSpace(frameID)
		if nested, ok := payload["artifact"].(map[string]any); ok {
			nested["frame_id"] = strings.TrimSpace(frameID)
		}
	}
	if err := s.enqueueProjectDomainEventTx(ctx, tx, ownerUserID, artifact.ProjectID, "artifact_created", artifact.ID, cloneDomainPayload(payload)); err != nil {
		return err
	}
	return s.enqueueProjectDomainEventTx(ctx, tx, ownerUserID, artifact.ProjectID, "lineage_ready", artifact.ID, cloneDomainPayload(payload))
}

func cloneDomainPayload(input map[string]any) map[string]any {
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func (s *Store) SaveArtifactVersionRealtime(ctx context.Context, input SaveArtifactVersionInput, ownerUserID string) (Artifact, ArtifactVersion, error) {
	if err := mutationIdempotencyError(ctx); err != nil {
		return Artifact{}, ArtifactVersion{}, err
	}
	input.ArtifactID, input.ProjectID = strings.TrimSpace(input.ArtifactID), strings.TrimSpace(input.ProjectID)
	input.Name, input.Kind, ownerUserID = strings.TrimSpace(input.Name), strings.TrimSpace(input.Kind), strings.TrimSpace(ownerUserID)
	if input.ArtifactID == "" || input.ProjectID == "" || input.Name == "" || input.Kind == "" || ownerUserID == "" {
		return Artifact{}, ArtifactVersion{}, errors.New("artifact id, project id, name, kind, and owner user id are required")
	}
	contentDigest := sha256.Sum256(input.Content)
	requestHash, err := mutationRequestHash(map[string]any{"artifactId": input.ArtifactID, "projectId": input.ProjectID,
		"name": input.Name, "kind": input.Kind, "contentSha256": hex.EncodeToString(contentDigest[:]),
		"createdBy": input.CreatedBy, "parentVersionId": strings.TrimSpace(input.ParentVersionID)})
	if err != nil {
		return Artifact{}, ArtifactVersion{}, err
	}
	idempotencyKey := mutationIdempotencyKey(ctx)
	operation := "artifact.version:" + input.ArtifactID
	var artifact Artifact
	var version ArtifactVersion
	err = s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var persisted artifactVersionLedgerResult
		replayed, err := lookupMutationResultTx(ctx, tx, ownerUserID, idempotencyKey, operation, requestHash, &persisted)
		if err != nil {
			return err
		}
		if replayed {
			artifact, version, err = loadArtifactVersionResultTx(ctx, tx, persisted.ArtifactID, persisted.VersionID)
			return err
		}
		if err := requireProjectOwnerTx(ctx, tx, input.ProjectID, ownerUserID); err != nil {
			return err
		}
		var currentVersion int
		var storedProjectID string
		var createdAt time.Time
		err = tx.QueryRowContext(ctx, `SELECT project_id, current_version_number, created_at FROM artifacts WHERE id = ?`, input.ArtifactID).
			Scan(&storedProjectID, &currentVersion, &createdAt)
		now := s.now().UTC()
		if errors.Is(err, sql.ErrNoRows) {
			createdAt, currentVersion = now, 0
			if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts
				(id, project_id, name, kind, current_version_number, created_at, updated_at) VALUES (?, ?, ?, ?, 0, ?, ?)`,
				input.ArtifactID, input.ProjectID, input.Name, input.Kind, now, now); err != nil {
				return fmt.Errorf("insert artifact: %w", err)
			}
		} else if err != nil {
			return fmt.Errorf("look up artifact: %w", err)
		} else if storedProjectID != input.ProjectID {
			return fmt.Errorf("artifact %q belongs to project %q, not %q", input.ArtifactID, storedProjectID, input.ProjectID)
		}
		parentID := strings.TrimSpace(input.ParentVersionID)
		if parentID != "" {
			var parentArtifactID string
			if err := tx.QueryRowContext(ctx, `SELECT artifact_id FROM artifact_versions WHERE id = ?`, parentID).Scan(&parentArtifactID); err != nil {
				return fmt.Errorf("look up requested parent artifact version: %w", err)
			}
			if parentArtifactID != input.ArtifactID {
				return errors.New("parent artifact version belongs to another artifact")
			}
		} else if currentVersion > 0 {
			if err := tx.QueryRowContext(ctx, `SELECT id FROM artifact_versions WHERE artifact_id = ? AND version_number = ?`, input.ArtifactID, currentVersion).Scan(&parentID); err != nil {
				return fmt.Errorf("look up parent artifact version: %w", err)
			}
		}
		digest := contentDigest
		version = ArtifactVersion{ID: uuid.NewString(), ArtifactID: input.ArtifactID, VersionNumber: currentVersion + 1,
			ParentID: parentID, Content: append([]byte(nil), input.Content...), ContentSHA256: hex.EncodeToString(digest[:]),
			SizeBytes: int64(len(input.Content)), CreatedBy: input.CreatedBy, CreatedAt: now}
		if _, err := tx.ExecContext(ctx, `INSERT INTO artifact_versions
			(id, artifact_id, version_number, parent_id, content, content_sha256, storage_path, size_bytes, created_by, created_at)
			VALUES (?, ?, ?, ?, ?, ?, '', ?, ?, ?)`, version.ID, version.ArtifactID, version.VersionNumber,
			nullableString(version.ParentID), version.Content, version.ContentSHA256, version.SizeBytes, version.CreatedBy, version.CreatedAt); err != nil {
			return fmt.Errorf("insert artifact version: %w", err)
		}
		result, err := tx.ExecContext(ctx, `UPDATE artifacts SET name = ?, kind = ?, current_version_number = ?, updated_at = ?
			WHERE id = ? AND current_version_number = ?`, input.Name, input.Kind, version.VersionNumber, now, input.ArtifactID, currentVersion)
		if err != nil {
			return fmt.Errorf("update artifact current version: %w", err)
		}
		if err := requireOneMutationRow(result, "artifact", input.ArtifactID); err != nil {
			return fmt.Errorf("artifact current version changed concurrently: %w", err)
		}
		artifact = Artifact{ID: input.ArtifactID, ProjectID: input.ProjectID, Name: input.Name, Kind: input.Kind,
			CurrentVersionNumber: version.VersionNumber, CreatedAt: createdAt, UpdatedAt: now}
		if err := s.enqueueArtifactVersionEventsTx(ctx, tx, ownerUserID, artifact, version); err != nil {
			return err
		}
		return insertMutationResultTx(ctx, tx, ownerUserID, idempotencyKey, operation, requestHash,
			artifactVersionLedgerResult{ArtifactID: artifact.ID, VersionID: version.ID}, now)
	})
	if err != nil {
		var persisted artifactVersionLedgerResult
		if found, lookupErr := s.lookupMutationResult(ctx, ownerUserID, idempotencyKey, operation, requestHash, &persisted); lookupErr == nil && found {
			loadedArtifact, loadedVersion, loaded, loadErr := s.GetArtifactVersion(persisted.VersionID)
			if loadErr == nil && loaded && loadedArtifact.ID == persisted.ArtifactID {
				return loadedArtifact, loadedVersion, nil
			}
		}
	}
	return artifact, version, err
}

func requireProjectOwnerTx(ctx context.Context, tx *sql.Tx, projectID, ownerUserID string) error {
	if tx == nil {
		return errors.New("workspace transaction is required")
	}
	projectID, ownerUserID = strings.TrimSpace(projectID), strings.TrimSpace(ownerUserID)
	if projectID == "" || ownerUserID == "" {
		return errors.New("project id and owner user id are required")
	}
	var found string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM projects WHERE id = ? AND user_id = ?`, projectID, ownerUserID).Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("project %q is unavailable to owner %q", projectID, ownerUserID)
		}
		return fmt.Errorf("verify project owner: %w", err)
	}
	return nil
}

func scanArtifactTx(row rowScanner) (Artifact, error) {
	var artifact Artifact
	if err := row.Scan(&artifact.ID, &artifact.ProjectID, &artifact.Name, &artifact.Kind,
		&artifact.CurrentVersionNumber, &artifact.FolderID, &artifact.Priority,
		&artifact.CreatedAt, &artifact.UpdatedAt); err != nil {
		return Artifact{}, err
	}
	return artifact, nil
}

func (s *Store) RenameArtifactRealtime(ctx context.Context, artifactID, ownerUserID, name string) (Artifact, error) {
	artifactID, ownerUserID, name = strings.TrimSpace(artifactID), strings.TrimSpace(ownerUserID), strings.TrimSpace(name)
	if artifactID == "" || ownerUserID == "" || name == "" {
		return Artifact{}, errors.New("artifact id, owner user id, and name are required")
	}
	var artifact Artifact
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var projectID string
		if err := tx.QueryRowContext(ctx, `SELECT project_id FROM artifacts WHERE id = ?`, artifactID).Scan(&projectID); err != nil {
			return fmt.Errorf("look up artifact for rename: %w", err)
		}
		if err := requireProjectOwnerTx(ctx, tx, projectID, ownerUserID); err != nil {
			return err
		}
		now := s.now().UTC()
		result, err := tx.ExecContext(ctx, `UPDATE artifacts SET name = ?, updated_at = ? WHERE id = ? AND project_id = ?`, name, now, artifactID, projectID)
		if err != nil {
			return fmt.Errorf("rename artifact: %w", err)
		}
		if err := requireOneMutationRow(result, "artifact", artifactID); err != nil {
			return err
		}
		artifact, err = scanArtifactTx(tx.QueryRowContext(ctx, artifactSelect+` WHERE id = ?`, artifactID))
		if err != nil {
			return fmt.Errorf("read renamed artifact: %w", err)
		}
		return s.enqueueProjectDomainEventTx(ctx, tx, ownerUserID, projectID, "artifact_renamed", artifact.ID,
			map[string]any{"artifact_id": artifact.ID, "new_filename": artifact.Name})
	})
	return artifact, err
}

func (s *Store) UpdateArtifactPriorityRealtime(ctx context.Context, artifactID, ownerUserID string, priority ArtifactPriority) (Artifact, error) {
	if !priority.Valid() {
		return Artifact{}, ErrArtifactPriority
	}
	var artifact Artifact
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var projectID string
		if err := tx.QueryRowContext(ctx, `SELECT project_id FROM artifacts WHERE id = ?`, artifactID).Scan(&projectID); err != nil {
			return fmt.Errorf("look up artifact for priority update: %w", err)
		}
		if err := requireProjectOwnerTx(ctx, tx, projectID, ownerUserID); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE artifacts SET priority = ?, updated_at = ? WHERE id = ?`, priority, s.now().UTC(), artifactID)
		if err != nil {
			return fmt.Errorf("update artifact priority: %w", err)
		}
		if err := requireOneMutationRow(result, "artifact", artifactID); err != nil {
			return err
		}
		artifact, err = scanArtifactTx(tx.QueryRowContext(ctx, artifactSelect+` WHERE id = ?`, artifactID))
		if err != nil {
			return err
		}
		return s.enqueueProjectDomainEventTx(ctx, tx, ownerUserID, projectID, "artifact_priority_update", artifact.ID,
			map[string]any{"artifact_id": artifact.ID, "priority": artifact.Priority})
	})
	return artifact, err
}

func (s *Store) DeleteArtifactRealtime(ctx context.Context, artifactID, ownerUserID string) ([]string, error) {
	artifactID, ownerUserID = strings.TrimSpace(artifactID), strings.TrimSpace(ownerUserID)
	paths := []string{}
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var projectID string
		if err := tx.QueryRowContext(ctx, `SELECT project_id FROM artifacts WHERE id = ?`, artifactID).Scan(&projectID); err != nil {
			return fmt.Errorf("artifact %q does not exist: %w", artifactID, err)
		}
		if err := requireProjectOwnerTx(ctx, tx, projectID, ownerUserID); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT storage_path FROM artifact_versions WHERE artifact_id = ? AND storage_path <> ''`, artifactID)
		if err != nil {
			return fmt.Errorf("list artifact blobs before delete: %w", err)
		}
		for rows.Next() {
			var path string
			if err := rows.Scan(&path); err != nil {
				_ = rows.Close()
				return err
			}
			paths = append(paths, path)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM artifacts WHERE id = ? AND project_id = ?`, artifactID, projectID)
		if err != nil {
			return fmt.Errorf("delete artifact: %w", err)
		}
		if err := requireOneMutationRow(result, "artifact", artifactID); err != nil {
			return err
		}
		return s.enqueueProjectDomainEventTx(ctx, tx, ownerUserID, projectID, "artifact_deleted", artifactID,
			map[string]any{"artifact_id": artifactID})
	})
	return paths, err
}

func (s *Store) RemoveArtifactBlobs(paths []string) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	seen := make(map[string]struct{}, len(paths))
	for _, rawPath := range paths {
		path := strings.TrimSpace(rawPath)
		if path == "" {
			continue
		}
		if _, duplicate := seen[path]; duplicate {
			continue
		}
		seen[path] = struct{}{}
		var referenced bool
		if err := s.db.QueryRowContext(context.Background(), `
			SELECT EXISTS(SELECT 1 FROM artifact_versions WHERE storage_path = ? LIMIT 1)`, path).Scan(&referenced); err != nil {
			return fmt.Errorf("check deleted artifact blob references: %w", err)
		}
		if referenced {
			continue
		}
		absolute, err := s.blobAbsolute(path)
		if err != nil {
			return fmt.Errorf("resolve deleted artifact blob: %w", err)
		}
		if err := os.Remove(absolute); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove deleted artifact blob: %w", err)
		}
	}
	return nil
}

func (s *Store) SetArtifactFolderRealtime(ctx context.Context, artifactID, folderID, ownerUserID string) (Artifact, error) {
	artifactID, folderID, ownerUserID = strings.TrimSpace(artifactID), strings.TrimSpace(folderID), strings.TrimSpace(ownerUserID)
	var artifact Artifact
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var projectID string
		if err := tx.QueryRowContext(ctx, `SELECT project_id FROM artifacts WHERE id = ?`, artifactID).Scan(&projectID); err != nil {
			return fmt.Errorf("look up artifact for move: %w", err)
		}
		if err := requireProjectOwnerTx(ctx, tx, projectID, ownerUserID); err != nil {
			return err
		}
		if folderID != "" {
			var folderProjectID string
			if err := tx.QueryRowContext(ctx, `SELECT project_id FROM artifact_folders WHERE id = ?`, folderID).Scan(&folderProjectID); err != nil || folderProjectID != projectID {
				return fmt.Errorf("folder %q does not belong to artifact project", folderID)
			}
		}
		result, err := tx.ExecContext(ctx, `UPDATE artifacts SET folder_id = ?, updated_at = ? WHERE id = ?`, nullableString(folderID), s.now().UTC(), artifactID)
		if err != nil {
			return fmt.Errorf("set artifact folder: %w", err)
		}
		if err := requireOneMutationRow(result, "artifact", artifactID); err != nil {
			return err
		}
		artifact, err = scanArtifactTx(tx.QueryRowContext(ctx, artifactSelect+` WHERE id = ?`, artifactID))
		if err != nil {
			return err
		}
		return s.enqueueProjectDomainEventTx(ctx, tx, ownerUserID, projectID, "artifact_moved", artifact.ID,
			map[string]any{"artifact_id": artifact.ID, "new_folder_id": nullablePayloadString(artifact.FolderID)})
	})
	return artifact, err
}

func (s *Store) BulkMoveArtifactsRealtime(ctx context.Context, artifactIDs []string, folderID, ownerUserID string) ([]Artifact, error) {
	seen := map[string]bool{}
	ids := make([]string, 0, len(artifactIDs))
	for _, raw := range artifactIDs {
		if id := strings.TrimSpace(raw); id != "" && !seen[id] {
			seen[id], ids = true, append(ids, id)
		}
	}
	if len(ids) == 0 || len(ids) > 1000 {
		return nil, errors.New("artifact bulk move requires between 1 and 1000 unique ids")
	}
	folderID, ownerUserID = strings.TrimSpace(folderID), strings.TrimSpace(ownerUserID)
	artifacts := make([]Artifact, 0, len(ids))
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		folderProjectID := ""
		if folderID != "" {
			if err := tx.QueryRowContext(ctx, `SELECT project_id FROM artifact_folders WHERE id = ?`, folderID).Scan(&folderProjectID); err != nil {
				return fmt.Errorf("folder %q does not exist", folderID)
			}
			if err := requireProjectOwnerTx(ctx, tx, folderProjectID, ownerUserID); err != nil {
				return err
			}
		}
		for _, id := range ids {
			artifact, err := scanArtifactTx(tx.QueryRowContext(ctx, artifactSelect+` WHERE id = ?`, id))
			if err != nil {
				return fmt.Errorf("look up artifact %q for bulk move: %w", id, err)
			}
			if err := requireProjectOwnerTx(ctx, tx, artifact.ProjectID, ownerUserID); err != nil {
				return err
			}
			if folderProjectID != "" && folderProjectID != artifact.ProjectID {
				return fmt.Errorf("artifact %q and folder %q belong to different projects", id, folderID)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE artifacts SET folder_id = ?, updated_at = ? WHERE id = ?`, nullableString(folderID), s.now().UTC(), id); err != nil {
				return fmt.Errorf("move artifact %q: %w", id, err)
			}
			artifact.FolderID = folderID
			if err := s.enqueueProjectDomainEventTx(ctx, tx, ownerUserID, artifact.ProjectID, "artifact_moved", artifact.ID,
				map[string]any{"artifact_id": artifact.ID, "new_folder_id": nullablePayloadString(folderID)}); err != nil {
				return err
			}
			artifacts = append(artifacts, artifact)
		}
		return nil
	})
	return artifacts, err
}

func nullablePayloadString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func folderRealtimePayload(folder ArtifactFolder) map[string]any {
	return map[string]any{"folder": map[string]any{
		"id": folder.ID, "project_id": folder.ProjectID, "parent_id": nullablePayloadString(folder.ParentID),
		"name": folder.Name, "sort_order": folder.SortOrder, "root_frame_id": nullablePayloadString(folder.RootFrameID),
		"is_conversation_folder": folder.IsConversationFolder, "is_user_uploads_folder": folder.IsUserUploadsFolder,
	}}
}

func (s *Store) CreateArtifactFolderRealtime(ctx context.Context, input CreateArtifactFolderInput, ownerUserID string) (ArtifactFolder, error) {
	var folder ArtifactFolder
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		if err := requireProjectOwnerTx(ctx, tx, input.ProjectID, ownerUserID); err != nil {
			return err
		}
		for field, value := range map[string]string{"folder id": input.ID, "folder project id": input.ProjectID, "folder name": input.Name} {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("%s is required", field)
			}
		}
		if err := validateFolderReferences(ctx, tx, input.ProjectID, input.ParentID, input.RootFrameID); err != nil {
			return err
		}
		now := s.now().UTC()
		folder = ArtifactFolder{ID: input.ID, ProjectID: input.ProjectID, ParentID: input.ParentID, Name: input.Name,
			SortOrder: input.SortOrder, RootFrameID: input.RootFrameID, IsConversationFolder: input.IsConversationFolder,
			IsUserUploadsFolder: input.IsUserUploadsFolder, CreatedAt: now, UpdatedAt: now}
		if _, err := tx.ExecContext(ctx, `INSERT INTO artifact_folders
			(id, project_id, parent_id, name, sort_order, root_frame_id, is_conversation_folder, is_user_uploads_folder, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, folder.ID, folder.ProjectID, nullableString(folder.ParentID), folder.Name,
			folder.SortOrder, nullableString(folder.RootFrameID), folder.IsConversationFolder, folder.IsUserUploadsFolder,
			folder.CreatedAt, folder.UpdatedAt); err != nil {
			return fmt.Errorf("insert artifact folder: %w", err)
		}
		return s.enqueueProjectDomainEventTx(ctx, tx, ownerUserID, folder.ProjectID, "folder_created", folder.ID, folderRealtimePayload(folder))
	})
	return folder, err
}

func (s *Store) UpdateArtifactFolderRealtime(ctx context.Context, id, ownerUserID string, input UpdateArtifactFolderInput) (ArtifactFolder, error) {
	if input.ParentID == nil && input.Name == nil && input.SortOrder == nil {
		return ArtifactFolder{}, errors.New("at least one folder field is required")
	}
	var folder ArtifactFolder
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var err error
		folder, err = scanArtifactFolder(tx.QueryRowContext(ctx, `SELECT id, project_id, COALESCE(parent_id, ''), name, sort_order,
			COALESCE(root_frame_id, ''), is_conversation_folder, is_user_uploads_folder, created_at, updated_at
			FROM artifact_folders WHERE id = ?`, id))
		if err != nil {
			return fmt.Errorf("folder %q does not exist: %w", id, err)
		}
		if err := requireProjectOwnerTx(ctx, tx, folder.ProjectID, ownerUserID); err != nil {
			return err
		}
		if input.Name != nil && strings.TrimSpace(*input.Name) == "" {
			return errors.New("folder name cannot be empty")
		}
		if input.ParentID != nil {
			if *input.ParentID == id {
				return errors.New("folder cannot be its own parent")
			}
			if err := validateFolderReferences(ctx, tx, folder.ProjectID, *input.ParentID, ""); err != nil {
				return err
			}
		}
		parentChanged, parentValue := input.ParentID != nil, any(nil)
		if input.ParentID != nil {
			parentValue = nullableString(*input.ParentID)
		}
		result, err := tx.ExecContext(ctx, `UPDATE artifact_folders SET
			parent_id = CASE WHEN ? THEN ? ELSE parent_id END, name = COALESCE(?, name),
			sort_order = COALESCE(?, sort_order), updated_at = ? WHERE id = ?`, parentChanged, parentValue,
			input.Name, input.SortOrder, s.now().UTC(), id)
		if err != nil {
			return fmt.Errorf("update artifact folder: %w", err)
		}
		if err := requireOneMutationRow(result, "folder", id); err != nil {
			return err
		}
		folder, err = scanArtifactFolder(tx.QueryRowContext(ctx, `SELECT id, project_id, COALESCE(parent_id, ''), name, sort_order,
			COALESCE(root_frame_id, ''), is_conversation_folder, is_user_uploads_folder, created_at, updated_at
			FROM artifact_folders WHERE id = ?`, id))
		if err != nil {
			return err
		}
		return s.enqueueProjectDomainEventTx(ctx, tx, ownerUserID, folder.ProjectID, "folder_updated", folder.ID, folderRealtimePayload(folder))
	})
	return folder, err
}

func (s *Store) DeleteArtifactFolderRealtime(ctx context.Context, id, ownerUserID string) error {
	return s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var projectID string
		if err := tx.QueryRowContext(ctx, `SELECT project_id FROM artifact_folders WHERE id = ?`, id).Scan(&projectID); err != nil {
			return fmt.Errorf("folder %q does not exist: %w", id, err)
		}
		if err := requireProjectOwnerTx(ctx, tx, projectID, ownerUserID); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM artifact_folders WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("delete artifact folder: %w", err)
		}
		if err := requireOneMutationRow(result, "folder", id); err != nil {
			return err
		}
		return s.enqueueProjectDomainEventTx(ctx, tx, ownerUserID, projectID, "folder_deleted", id, map[string]any{"folder_id": id})
	})
}

func (s *Store) CreateProjectNoteRealtime(ctx context.Context, input CreateProjectNoteInput, ownerUserID string) (ProjectNote, error) {
	var note ProjectNote
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		if err := requireProjectOwnerTx(ctx, tx, input.ProjectID, ownerUserID); err != nil {
			return err
		}
		if strings.TrimSpace(input.UserID) != ownerUserID {
			return errors.New("note user must match project owner")
		}
		for field, value := range map[string]string{"note id": input.ID, "note target type": input.TargetType, "note target frame id": input.TargetFrameID, "note content": input.Content} {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("%s is required", field)
			}
		}
		var frameProject string
		if err := tx.QueryRowContext(ctx, `SELECT project_id FROM frames WHERE id = ?`, input.TargetFrameID).Scan(&frameProject); err != nil || frameProject != input.ProjectID {
			return errors.New("note target frame must belong to project")
		}
		if input.TargetArtifactID != "" {
			var artifactProject string
			if err := tx.QueryRowContext(ctx, `SELECT project_id FROM artifacts WHERE id = ?`, input.TargetArtifactID).Scan(&artifactProject); err != nil || artifactProject != input.ProjectID {
				return errors.New("note target artifact must belong to project")
			}
		}
		now := s.now().UTC()
		note = ProjectNote{ID: input.ID, ProjectID: input.ProjectID, UserID: input.UserID, TargetType: input.TargetType,
			TargetFrameID: input.TargetFrameID, TargetMessageIndex: input.TargetMessageIndex, TargetArtifactID: input.TargetArtifactID,
			Content: input.Content, CreatedAt: now, UpdatedAt: now}
		if _, err := tx.ExecContext(ctx, `INSERT INTO notes
			(id, project_id, user_id, target_type, target_frame_id, target_message_index, target_artifact_id, content, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, note.ID, note.ProjectID, note.UserID, note.TargetType, note.TargetFrameID,
			note.TargetMessageIndex, nullableString(note.TargetArtifactID), note.Content, note.CreatedAt, note.UpdatedAt); err != nil {
			return fmt.Errorf("insert project note: %w", err)
		}
		return s.enqueueProjectDomainEventTx(ctx, tx, ownerUserID, note.ProjectID, "note_update", note.ID,
			map[string]any{"note_id": note.ID, "action": "created"})
	})
	return note, err
}

func (s *Store) UpdateProjectNoteRealtime(ctx context.Context, id, ownerUserID, content string) (ProjectNote, error) {
	if strings.TrimSpace(content) == "" {
		return ProjectNote{}, errors.New("note content is required")
	}
	var note ProjectNote
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var projectID, noteUserID string
		if err := tx.QueryRowContext(ctx, `SELECT project_id, user_id FROM notes WHERE id = ?`, id).Scan(&projectID, &noteUserID); err != nil {
			return fmt.Errorf("note %q does not exist: %w", id, err)
		}
		if noteUserID != ownerUserID {
			return fmt.Errorf("note %q is unavailable to owner", id)
		}
		if err := requireProjectOwnerTx(ctx, tx, projectID, ownerUserID); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE notes SET content = ?, updated_at = ? WHERE id = ? AND user_id = ?`, content, s.now().UTC(), id, ownerUserID)
		if err != nil {
			return fmt.Errorf("update project note: %w", err)
		}
		if err := requireOneMutationRow(result, "note", id); err != nil {
			return err
		}
		note, err = scanProjectNote(tx.QueryRowContext(ctx, `SELECT id, project_id, user_id, target_type, target_frame_id,
			target_message_index, COALESCE(target_artifact_id, ''), content, created_at, updated_at FROM notes WHERE id = ?`, id))
		if err != nil {
			return err
		}
		return s.enqueueProjectDomainEventTx(ctx, tx, ownerUserID, projectID, "note_update", id,
			map[string]any{"note_id": id, "action": "updated"})
	})
	return note, err
}

func (s *Store) DeleteProjectNoteRealtime(ctx context.Context, id, ownerUserID string) error {
	return s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var projectID, noteUserID string
		if err := tx.QueryRowContext(ctx, `SELECT project_id, user_id FROM notes WHERE id = ?`, id).Scan(&projectID, &noteUserID); err != nil {
			return fmt.Errorf("note %q does not exist: %w", id, err)
		}
		if noteUserID != ownerUserID {
			return fmt.Errorf("note %q is unavailable to owner", id)
		}
		if err := requireProjectOwnerTx(ctx, tx, projectID, ownerUserID); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM notes WHERE id = ? AND user_id = ?`, id, ownerUserID)
		if err != nil {
			return fmt.Errorf("delete project note: %w", err)
		}
		if err := requireOneMutationRow(result, "note", id); err != nil {
			return err
		}
		return s.enqueueProjectDomainEventTx(ctx, tx, ownerUserID, projectID, "note_update", id,
			map[string]any{"note_id": id, "action": "deleted"})
	})
}

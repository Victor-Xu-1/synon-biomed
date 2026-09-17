package workspace

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// WriteArtifactVersionInput models the v1.1 user-edit path. Content is staged
// before the SQLite transaction and removed unless the metadata commit succeeds.
type WriteArtifactVersionInput struct {
	ArtifactID                    string
	ProjectID                     string
	Name                          string
	ContentType                   string
	Content                       io.Reader
	CreatedBy                     string
	MaxBytes                      int64
	ParentVersionID               string
	ProvenanceSourceID            string
	FreshUserEditMappings         bool
	Interactions                  []map[string]any
	CarryCellSources              bool
	BranchSourceArtifactID        string
	IsBranchMint                  bool
	RootFrameID                   string
	FrameID                       string
	IsUserUpload                  bool
	ReadSourceProjectID           string
	CarryAnnotationsFromTargetKey string
	TranscriptAssociation         *ArtifactTranscriptAssociation
	Language                      string
	Environment                   string
	IsIntermediate                bool
	IsCheckpoint                  bool
	ExecutionLogIDs               []string
}

// ArtifactContentTooLargeError reports a bounded artifact staging rejection.
// Callers may use errors.As without parsing the diagnostic string.
type ArtifactContentTooLargeError struct {
	Limit int64
}

func (err *ArtifactContentTooLargeError) Error() string {
	return fmt.Sprintf("artifact content exceeds %d byte limit", err.Limit)
}

type ArtifactTranscriptAssociation struct {
	StreamUID                      string
	RunnerID                       string
	ClaimToken                     string
	Attempt                        int64
	SourceEventID                  int64
	Relation                       string
	ReuseCurrentVersionIfUnchanged bool
}

func (s *Store) WriteArtifactVersion(ctx context.Context, input WriteArtifactVersionInput) (Artifact, ArtifactVersion, error) {
	return s.writeArtifactVersion(ctx, input, "", false)
}

// WriteArtifactVersionRealtime appends a blob-backed version and both v1.1
// artifact events in the same SQLite transaction. The project owner is checked
// again inside that transaction.
func (s *Store) WriteArtifactVersionRealtime(ctx context.Context, input WriteArtifactVersionInput, ownerUserID string) (Artifact, ArtifactVersion, error) {
	return s.writeArtifactVersion(ctx, input, strings.TrimSpace(ownerUserID), true)
}

func (s *Store) writeArtifactVersion(ctx context.Context, input WriteArtifactVersionInput, ownerUserID string, withRealtime bool) (Artifact, ArtifactVersion, error) {
	if s == nil || s.db == nil {
		return Artifact{}, ArtifactVersion{}, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if withRealtime {
		if err := mutationIdempotencyError(ctx); err != nil {
			return Artifact{}, ArtifactVersion{}, err
		}
	}
	input.ArtifactID = strings.TrimSpace(input.ArtifactID)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.Name = strings.TrimSpace(input.Name)
	input.ContentType = strings.TrimSpace(input.ContentType)
	input.Language = strings.TrimSpace(input.Language)
	input.Environment = strings.TrimSpace(input.Environment)
	input.ExecutionLogIDs = normalizeArtifactWriteExecutionIDs(input.ExecutionLogIDs)
	if input.ArtifactID == "" || input.ProjectID == "" || input.Name == "" || input.ContentType == "" || input.Content == nil {
		return Artifact{}, ArtifactVersion{}, errors.New("artifact id, project id, name, content type, and content are required")
	}
	versionID := uuid.NewString()
	temporary, size, digest, err := s.stageArtifactWrite(ctx, input.Content, input.MaxBytes)
	if err != nil {
		return Artifact{}, ArtifactVersion{}, err
	}
	relativePath := artifactVersionBlobPath(versionID)
	stagingPath, err := s.blobRelative(temporary)
	if err != nil {
		return Artifact{}, ArtifactVersion{}, err
	}
	commitAttempted := false
	defer func() {
		if commitAttempted {
			// The transaction may have committed even when Commit returned an
			// error. The durable marker owns staging cleanup from this point.
			return
		}
		_ = os.Remove(temporary)
	}()
	requestHash := ""
	idempotencyKey := ""
	operation := ""
	if withRealtime {
		requestHash, err = mutationRequestHash(map[string]any{
			"artifactId": input.ArtifactID, "projectId": input.ProjectID, "name": input.Name,
			"contentType": input.ContentType, "contentSha256": digest, "sizeBytes": size,
			"createdBy": input.CreatedBy, "parentVersionId": input.ParentVersionID,
			"provenanceSourceId": input.ProvenanceSourceID, "freshUserEditMappings": input.FreshUserEditMappings,
			"interactions": input.Interactions, "carryCellSources": input.CarryCellSources,
			"branchSourceArtifactId": input.BranchSourceArtifactID, "isBranchMint": input.IsBranchMint,
			"rootFrameId": input.RootFrameID, "frameId": input.FrameID, "isUserUpload": input.IsUserUpload,
			"readSourceProjectId":           input.ReadSourceProjectID,
			"carryAnnotationsFromTargetKey": input.CarryAnnotationsFromTargetKey,
			"transcriptAssociation":         artifactTranscriptAssociationHashInput(input.TranscriptAssociation),
			"language":                      input.Language,
			"environment":                   input.Environment,
			"isIntermediate":                input.IsIntermediate,
			"isCheckpoint":                  input.IsCheckpoint,
			"executionLogIds":               input.ExecutionLogIDs,
		})
		if err != nil {
			return Artifact{}, ArtifactVersion{}, err
		}
		idempotencyKey = mutationIdempotencyKey(ctx)
		operation = "artifact.version:" + input.ArtifactID
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Artifact{}, ArtifactVersion{}, fmt.Errorf("begin artifact write transaction: %w", err)
	}
	defer tx.Rollback()
	if withRealtime {
		var persisted artifactVersionLedgerResult
		replayed, err := lookupMutationResultTx(ctx, tx, ownerUserID, idempotencyKey, operation, requestHash, &persisted)
		if err != nil {
			return Artifact{}, ArtifactVersion{}, err
		}
		if replayed {
			artifact, version, err := loadArtifactVersionResultTx(ctx, tx, persisted.ArtifactID, persisted.VersionID)
			if err != nil {
				return Artifact{}, ArtifactVersion{}, err
			}
			_ = tx.Rollback()
			if err := s.recoverBlobCommits(ctx); err != nil {
				return Artifact{}, ArtifactVersion{}, err
			}
			return artifact, version, nil
		}
	}
	now := s.now().UTC()
	var currentVersion int
	var storedProjectID string
	var createdAt sql.NullTime
	err = tx.QueryRowContext(ctx, `
		SELECT project_id, current_version_number, created_at
		FROM artifacts WHERE id = ?`, input.ArtifactID).Scan(&storedProjectID, &currentVersion, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		var projectExists string
		projectQuery := `SELECT id FROM projects WHERE id = ?`
		projectArgs := []any{input.ProjectID}
		if withRealtime {
			projectQuery += ` AND user_id = ?`
			projectArgs = append(projectArgs, ownerUserID)
		}
		if err := tx.QueryRowContext(ctx, projectQuery, projectArgs...).Scan(&projectExists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return Artifact{}, ArtifactVersion{}, fmt.Errorf("project %q does not exist", input.ProjectID)
			}
			return Artifact{}, ArtifactVersion{}, fmt.Errorf("look up artifact project: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO artifacts (id, project_id, name, kind, current_version_number, created_at, updated_at)
			VALUES (?, ?, ?, ?, 0, ?, ?)`, input.ArtifactID, input.ProjectID, input.Name, input.ContentType, now, now); err != nil {
			return Artifact{}, ArtifactVersion{}, fmt.Errorf("insert artifact: %w", err)
		}
		createdAt = sql.NullTime{Time: now, Valid: true}
		currentVersion = 0
	} else if err != nil {
		return Artifact{}, ArtifactVersion{}, fmt.Errorf("look up artifact: %w", err)
	} else if storedProjectID != input.ProjectID {
		return Artifact{}, ArtifactVersion{}, fmt.Errorf("artifact %q belongs to project %q, not %q", input.ArtifactID, storedProjectID, input.ProjectID)
	}
	if withRealtime {
		if err := requireProjectOwnerTx(ctx, tx, input.ProjectID, ownerUserID); err != nil {
			return Artifact{}, ArtifactVersion{}, err
		}
		if strings.TrimSpace(input.ReadSourceProjectID) != "" {
			if err := requireProjectOwnerTx(ctx, tx, input.ReadSourceProjectID, ownerUserID); err != nil {
				return Artifact{}, ArtifactVersion{}, err
			}
		}
	}
	if input.TranscriptAssociation != nil && input.TranscriptAssociation.ReuseCurrentVersionIfUnchanged &&
		currentVersion > 0 && strings.TrimSpace(input.ParentVersionID) == "" {
		var currentVersionID string
		if err := tx.QueryRowContext(ctx, `
			SELECT id FROM artifact_versions WHERE artifact_id = ? AND version_number = ?`,
			input.ArtifactID, currentVersion,
		).Scan(&currentVersionID); err != nil {
			return Artifact{}, ArtifactVersion{}, fmt.Errorf("look up reusable artifact version: %w", err)
		}
		artifact, version, err := loadArtifactVersionResultTx(ctx, tx, input.ArtifactID, currentVersionID)
		if err != nil {
			return Artifact{}, ArtifactVersion{}, err
		}
		if artifact.Name == input.Name && artifact.Kind == input.ContentType &&
			version.ContentSHA256 == digest && version.SizeBytes == size {
			if err := insertArtifactTranscriptCommitTx(ctx, tx, input, ownerUserID, version, now); err != nil {
				return Artifact{}, ArtifactVersion{}, err
			}
			if withRealtime {
				if err := insertMutationResultTx(ctx, tx, ownerUserID, idempotencyKey, operation, requestHash,
					artifactVersionLedgerResult{ArtifactID: artifact.ID, VersionID: version.ID}, now); err != nil {
					return Artifact{}, ArtifactVersion{}, err
				}
			}
			if err := tx.Commit(); err != nil {
				if withRealtime {
					var persisted artifactVersionLedgerResult
					if found, lookupErr := s.lookupMutationResult(ctx, ownerUserID, idempotencyKey, operation, requestHash, &persisted); lookupErr == nil && found {
						loadedArtifact, loadedVersion, loaded, loadErr := s.GetArtifactVersionMetadata(persisted.VersionID)
						if loadErr == nil && loaded && loadedArtifact.ID == persisted.ArtifactID {
							return loadedArtifact, loadedVersion, nil
						}
					}
				}
				return Artifact{}, ArtifactVersion{}, fmt.Errorf("commit unchanged artifact association: %w", err)
			}
			return artifact, version, nil
		}
	}

	parentID := strings.TrimSpace(input.ParentVersionID)
	if parentID != "" {
		var parentArtifactID string
		if err := tx.QueryRowContext(ctx, `SELECT artifact_id FROM artifact_versions WHERE id = ?`, parentID).Scan(&parentArtifactID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return Artifact{}, ArtifactVersion{}, fmt.Errorf("parent artifact version %q does not exist", parentID)
			}
			return Artifact{}, ArtifactVersion{}, fmt.Errorf("look up parent artifact version: %w", err)
		}
		if parentArtifactID != input.ArtifactID {
			return Artifact{}, ArtifactVersion{}, fmt.Errorf("parent artifact version %q belongs to artifact %q, not %q", parentID, parentArtifactID, input.ArtifactID)
		}
	} else if currentVersion > 0 {
		if err := tx.QueryRowContext(ctx, `
			SELECT id FROM artifact_versions WHERE artifact_id = ? AND version_number = ?`,
			input.ArtifactID, currentVersion).Scan(&parentID); err != nil {
			return Artifact{}, ArtifactVersion{}, fmt.Errorf("look up current artifact version: %w", err)
		}
	}
	version := ArtifactVersion{
		ID: versionID, ArtifactID: input.ArtifactID, VersionNumber: currentVersion + 1,
		ParentID: parentID, ContentSHA256: digest, StoragePath: relativePath,
		SizeBytes: size, CreatedBy: strings.TrimSpace(input.CreatedBy), CreatedAt: now,
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artifact_versions
			(id, artifact_id, version_number, parent_id, content, content_sha256, storage_path, size_bytes, created_by, created_at)
		VALUES (?, ?, ?, ?, X'', ?, ?, ?, ?, ?)`,
		version.ID, version.ArtifactID, version.VersionNumber, nullArtifactWriteString(version.ParentID),
		version.ContentSHA256, version.StoragePath, version.SizeBytes, version.CreatedBy, version.CreatedAt); err != nil {
		return Artifact{}, ArtifactVersion{}, fmt.Errorf("insert artifact version: %w", err)
	}
	if err := copyArtifactWriteProvenance(ctx, tx, input, version); err != nil {
		return Artifact{}, ArtifactVersion{}, err
	}
	if err := insertArtifactTranscriptCommitTx(ctx, tx, input, ownerUserID, version, now); err != nil {
		return Artifact{}, ArtifactVersion{}, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE artifacts SET name = ?, kind = ?, current_version_number = ?, updated_at = ?
		WHERE id = ? AND current_version_number = ?`,
		input.Name, input.ContentType, version.VersionNumber, now, input.ArtifactID, currentVersion)
	if err != nil {
		return Artifact{}, ArtifactVersion{}, fmt.Errorf("advance artifact version: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return Artifact{}, ArtifactVersion{}, fmt.Errorf("artifact %q current version changed concurrently", input.ArtifactID)
	}
	if err := updateArtifactWriteRuntimeMetadata(ctx, tx, input, version); err != nil {
		return Artifact{}, ArtifactVersion{}, err
	}
	if strings.TrimSpace(input.CarryAnnotationsFromTargetKey) != "" {
		if _, err := s.carryForwardAnnotationsTx(ctx, tx, input.ProjectID,
			input.CarryAnnotationsFromTargetKey, "av:"+version.ID, version.ContentSHA256, now); err != nil {
			return Artifact{}, ArtifactVersion{}, fmt.Errorf("carry artifact annotations: %w", err)
		}
	}
	artifact := Artifact{
		ID: input.ArtifactID, ProjectID: input.ProjectID, Name: input.Name, Kind: input.ContentType,
		CurrentVersionNumber: version.VersionNumber, CreatedAt: createdAt.Time, UpdatedAt: now,
	}
	if withRealtime {
		if strings.TrimSpace(input.FrameID) != "" {
			var frameProjectID string
			if err := tx.QueryRowContext(ctx, `SELECT project_id FROM frames WHERE id = ?`, input.FrameID).Scan(&frameProjectID); err != nil {
				return Artifact{}, ArtifactVersion{}, fmt.Errorf("look up artifact frame: %w", err)
			}
			if frameProjectID != input.ProjectID {
				return Artifact{}, ArtifactVersion{}, errors.New("artifact frame belongs to another project")
			}
			if _, err := s.AppendFrameOutboxEventTx(ctx, tx, FrameEventInput{
				ID: domainRealtimeEventID(ctx, "artifact_frame_journal", artifact.ID), FrameID: input.FrameID,
				Type: "artifact_created", Payload: map[string]any{"artifact_id": artifact.ID, "version_id": version.ID,
					"version_number": version.VersionNumber, "name": artifact.Name, "kind": artifact.Kind},
			}); err != nil {
				return Artifact{}, ArtifactVersion{}, err
			}
		}
		if err := s.enqueueArtifactVersionEventsForFrameTx(ctx, tx, ownerUserID, artifact, version, input.FrameID); err != nil {
			return Artifact{}, ArtifactVersion{}, err
		}
		if err := insertMutationResultTx(ctx, tx, ownerUserID, idempotencyKey, operation, requestHash,
			artifactVersionLedgerResult{ArtifactID: artifact.ID, VersionID: version.ID}, now); err != nil {
			return Artifact{}, ArtifactVersion{}, err
		}
	}
	marker := blobCommitMarker{ID: "artifact-version:" + version.ID, Kind: "artifact_version",
		AggregateID: version.ID, StagingPath: stagingPath, FinalPath: relativePath,
		SHA256: version.ContentSHA256, SizeBytes: version.SizeBytes, CreatedAt: now}
	if err := s.insertBlobCommitMarkerTx(ctx, tx, marker); err != nil {
		return Artifact{}, ArtifactVersion{}, err
	}
	commitAttempted = true
	if err := tx.Commit(); err != nil {
		if withRealtime {
			var persisted artifactVersionLedgerResult
			if found, lookupErr := s.lookupMutationResult(ctx, ownerUserID, idempotencyKey, operation, requestHash, &persisted); lookupErr == nil && found {
				if recoverErr := s.recoverBlobCommits(ctx); recoverErr != nil {
					return Artifact{}, ArtifactVersion{}, recoverErr
				}
				artifact, version, found, loadErr := s.GetArtifactVersionMetadata(persisted.VersionID)
				if loadErr == nil && found && artifact.ID == persisted.ArtifactID {
					return artifact, version, nil
				}
			}
		}
		return Artifact{}, ArtifactVersion{}, fmt.Errorf("commit artifact write: %w", err)
	}
	if err := s.finalizeBlobCommit(ctx, marker); err != nil {
		return Artifact{}, ArtifactVersion{}, fmt.Errorf("finalize committed artifact blob: %w", err)
	}
	return artifact, version, nil
}

func normalizeArtifactWriteExecutionIDs(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func (s *Store) stageArtifactWrite(ctx context.Context, content io.Reader, maxBytes int64) (string, int64, string, error) {
	stagingDir := filepath.Join(s.blobRoot, "staging")
	if err := ensurePrivateDirectory(stagingDir); err != nil {
		return "", 0, "", err
	}
	file, err := os.CreateTemp(stagingDir, ".artifact-write-*")
	if err != nil {
		return "", 0, "", fmt.Errorf("create artifact staging file: %w", err)
	}
	path := file.Name()
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return "", 0, "", fmt.Errorf("secure artifact staging file: %w", err)
	}
	reader := io.Reader(&contextReader{ctx: ctx, reader: content})
	if maxBytes > 0 {
		reader = io.LimitReader(reader, maxBytes+1)
	}
	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, hasher), reader)
	if err != nil {
		return "", 0, "", fmt.Errorf("stream artifact content: %w", err)
	}
	if maxBytes > 0 && written > maxBytes {
		return "", 0, "", &ArtifactContentTooLargeError{Limit: maxBytes}
	}
	if err := file.Sync(); err != nil {
		return "", 0, "", fmt.Errorf("sync artifact staging file: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", 0, "", fmt.Errorf("close artifact staging file: %w", err)
	}
	keep = true
	return path, written, hex.EncodeToString(hasher.Sum(nil)), nil
}

func artifactVersionBlobPath(id string) string {
	prefix := id
	if len(prefix) > 2 {
		prefix = prefix[:2]
	}
	return filepath.ToSlash(filepath.Join("artifact-versions", prefix, id+".blob"))
}

type artifactWriteProvenance struct {
	frameID, extractedCode, codeDescription, lineageMessages        sql.NullString
	agentName, language, dependencyMappings, environment            sql.NullString
	annotations, lineageHash, envHash, producingCellID, cellSources sql.NullString
	isIntermediate, isCheckpoint                                    int
}

func copyArtifactWriteProvenance(ctx context.Context, tx workspaceTransaction, input WriteArtifactVersionInput, version ArtifactVersion) error {
	sourceID := strings.TrimSpace(input.ProvenanceSourceID)
	if sourceID == "" {
		sourceID = version.ParentID
	}
	var source artifactWriteProvenance
	if sourceID != "" {
		err := tx.QueryRowContext(ctx, `
			SELECT frame_id, extracted_code, code_description, lineage_messages,
				agent_name, language, dependency_mappings, environment_snapshot,
				annotations, lineage_snapshot_hash, env_snapshot_hash, producing_cell_id,
				cell_sources, is_intermediate, is_checkpoint
			FROM artifact_version_provenance WHERE version_id = ?`, sourceID).Scan(
			&source.frameID, &source.extractedCode, &source.codeDescription, &source.lineageMessages,
			&source.agentName, &source.language, &source.dependencyMappings, &source.environment,
			&source.annotations, &source.lineageHash, &source.envHash, &source.producingCellID,
			&source.cellSources, &source.isIntermediate, &source.isCheckpoint,
		)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read artifact provenance source: %w", err)
		}
	}
	dependencyMappings := source.dependencyMappings
	environment, envHash := source.environment, source.envHash
	if input.FreshUserEditMappings {
		mapping, err := json.Marshal(map[string]any{
			"inputs": []any{},
			"outputs": []map[string]any{{
				"workspace_path": nil, "filename": input.Name,
				"artifact_id": input.ArtifactID, "version_id": version.ID,
			}},
			"user_edit": true,
		})
		if err != nil {
			return fmt.Errorf("encode fresh user edit mappings: %w", err)
		}
		dependencyMappings = sql.NullString{String: string(mapping), Valid: true}
		environment, envHash = sql.NullString{}, sql.NullString{}
	}
	cellSources := sql.NullString{}
	var err error
	producingCellID := sql.NullString{}
	if input.CarryCellSources {
		cellSources, err = mergeArtifactWriteCellSources(source.cellSources, input.Interactions)
		if err != nil {
			return err
		}
		producingCellID = source.producingCellID
	}
	language := sql.NullString{}
	if value := strings.TrimSpace(input.Language); value != "" {
		if len([]byte(value)) > 64 {
			return errors.New("artifact execution language must be at most 64 bytes")
		}
		language = sql.NullString{String: value, Valid: true}
		// A new producer identity cannot inherit an older environment binding.
		environment, envHash = sql.NullString{}, sql.NullString{}
	}
	if value := strings.TrimSpace(input.Environment); value != "" {
		if len([]byte(value)) > 100 {
			return errors.New("artifact execution environment must be at most 100 bytes")
		}
		encoded, encodeErr := json.Marshal(map[string]any{
			"schema_version": 1, "kind": "environment_binding", "environment": value,
			"language": strings.TrimSpace(input.Language), "snapshot_status": "not_captured",
		})
		if encodeErr != nil {
			return fmt.Errorf("encode artifact environment binding: %w", encodeErr)
		}
		environment = sql.NullString{String: string(encoded), Valid: true}
		envHash = sql.NullString{}
	}
	type executionLink struct {
		id        string
		cellIndex int
	}
	links := make([]executionLink, 0, len(input.ExecutionLogIDs))
	seenExecutionIDs := map[string]bool{}
	for _, rawID := range input.ExecutionLogIDs {
		executionID := strings.TrimSpace(rawID)
		if executionID == "" || seenExecutionIDs[executionID] {
			continue
		}
		if len(links) >= 256 {
			return errors.New("artifact execution lineage exceeds 256 records")
		}
		var frameID, projectID string
		var cellIndex int
		if err := tx.QueryRowContext(ctx, `
			SELECT log.frame_id,frame.project_id,log.cell_index
			FROM execution_log log JOIN frames frame ON frame.id=log.frame_id
			WHERE log.id=?`, executionID).Scan(&frameID, &projectID, &cellIndex); err != nil {
			return fmt.Errorf("look up artifact execution %s: %w", executionID, err)
		}
		if projectID != strings.TrimSpace(input.ProjectID) || strings.TrimSpace(input.FrameID) != "" && frameID != strings.TrimSpace(input.FrameID) {
			return errors.New("artifact execution belongs to another frame or project")
		}
		seenExecutionIDs[executionID] = true
		links = append(links, executionLink{id: executionID, cellIndex: cellIndex})
	}
	sort.Slice(links, func(i, j int) bool {
		if links[i].cellIndex != links[j].cellIndex {
			return links[i].cellIndex < links[j].cellIndex
		}
		return links[i].id < links[j].id
	})
	executionIDs := make([]string, 0, len(links))
	executionCellIndexes := make(map[string]int, len(links))
	cellSourceValues := make([]any, 0, len(links))
	for _, link := range links {
		executionIDs = append(executionIDs, link.id)
		executionCellIndexes[link.id] = link.cellIndex
		cellSourceValues = append(cellSourceValues, map[string]any{
			"kind": "cell", "cell_index": link.cellIndex, "execution_log_id": link.id,
		})
	}
	if len(cellSourceValues) > 0 {
		encoded, encodeErr := json.Marshal(cellSourceValues)
		if encodeErr != nil {
			return fmt.Errorf("encode artifact execution lineage: %w", encodeErr)
		}
		cellSources = sql.NullString{String: string(encoded), Valid: true}
		producingCellID = sql.NullString{String: links[len(links)-1].id, Valid: true}
	}
	isIntermediate := 0
	if input.IsIntermediate {
		isIntermediate = 1
	}
	isCheckpoint := 0
	if input.IsCheckpoint {
		isCheckpoint = 1
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO artifact_version_provenance
			(version_id, frame_id, content_type, extracted_code, code_description, lineage_messages,
			 agent_name, language, is_intermediate, dependency_mappings, environment_snapshot,
			 annotations, lineage_snapshot_hash, env_snapshot_hash, producing_cell_id, cell_sources, is_checkpoint)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		version.ID, nullArtifactWriteString(input.FrameID), input.ContentType,
		nullArtifactWriteSQL(source.extractedCode), nullArtifactWriteSQL(source.codeDescription),
		nullArtifactWriteSQL(source.lineageMessages), nullArtifactWriteSQL(source.agentName),
		nullArtifactWriteSQL(language), isIntermediate,
		nullArtifactWriteSQL(dependencyMappings), nullArtifactWriteSQL(environment),
		nil, nullArtifactWriteSQL(source.lineageHash),
		nullArtifactWriteSQL(envHash), nullArtifactWriteSQL(producingCellID),
		nullArtifactWriteSQL(cellSources), isCheckpoint)
	if err != nil {
		return fmt.Errorf("copy artifact version provenance: %w", err)
	}
	for _, executionID := range executionIDs {
		cellIndex := executionCellIndexes[executionID]
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO artifact_version_execution_links(version_id,execution_log_id,cell_index)
			VALUES(?,?,?)`, version.ID, executionID, cellIndex); err != nil {
			return fmt.Errorf("link artifact version to execution: %w", err)
		}
	}
	return nil
}

func mergeArtifactWriteCellSources(source sql.NullString, interactions []map[string]any) (sql.NullString, error) {
	if len(interactions) == 0 {
		return source, nil
	}
	values := make([]any, 0, len(interactions))
	if source.Valid && strings.TrimSpace(source.String) != "" {
		if err := json.Unmarshal([]byte(source.String), &values); err != nil {
			return sql.NullString{}, fmt.Errorf("decode artifact cell sources: %w", err)
		}
	}
	for _, interaction := range interactions {
		values = append(values, interaction)
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("encode artifact cell sources: %w", err)
	}
	return sql.NullString{String: string(encoded), Valid: true}, nil
}

func updateArtifactWriteRuntimeMetadata(ctx context.Context, tx workspaceTransaction, input WriteArtifactVersionInput, version ArtifactVersion) error {
	if input.BranchSourceArtifactID != "" {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO artifact_runtime_metadata
				(artifact_id, root_frame_id, frame_id, latest_version_id, is_user_upload,
				 is_ephemeral, sort_order, superseded_by_artifact_id, consumed_at, is_branch_mint)
			SELECT ?, root_frame_id, frame_id, ?, 0, is_ephemeral, sort_order, NULL, NULL, ?
			FROM artifact_runtime_metadata WHERE artifact_id = ?
			ON CONFLICT(artifact_id) DO UPDATE SET latest_version_id = excluded.latest_version_id,
				is_branch_mint = excluded.is_branch_mint`,
			input.ArtifactID, version.ID, input.IsBranchMint, input.BranchSourceArtifactID); err != nil {
			return fmt.Errorf("copy branch artifact runtime metadata: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artifact_runtime_metadata (artifact_id, root_frame_id, frame_id, latest_version_id, is_user_upload, is_branch_mint)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(artifact_id) DO UPDATE SET latest_version_id = excluded.latest_version_id,
			root_frame_id = COALESCE(excluded.root_frame_id, artifact_runtime_metadata.root_frame_id),
			frame_id = COALESCE(excluded.frame_id, artifact_runtime_metadata.frame_id),
			is_user_upload = CASE WHEN excluded.is_user_upload = 1 THEN 1 ELSE artifact_runtime_metadata.is_user_upload END`,
		input.ArtifactID, nullArtifactWriteString(input.RootFrameID), nullArtifactWriteString(input.FrameID), version.ID, input.IsUserUpload, input.IsBranchMint); err != nil {
		return fmt.Errorf("advance artifact runtime metadata: %w", err)
	}
	return nil
}

func nullArtifactWriteString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nullArtifactWriteSQL(value sql.NullString) any {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil
	}
	return value.String
}

package workspace

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	runnerLargeToolResultVersionPrefix    = "ltr-"
	runnerLargeToolResultArtifactIDPrefix = "large-tool-result-"
)

var (
	// ErrRunnerLargeToolResultUnavailable means the immutable metadata remains,
	// but its externalized content has been pruned or is no longer present.
	// Callers may treat this as an unreadable historical result; metadata,
	// identity, size, and digest conflicts remain hard failures.
	ErrRunnerLargeToolResultUnavailable = errors.New("runner large tool result content is unavailable")
	ErrRunnerLargeToolResultConflict    = errors.New("runner large tool result conflicts with its immutable evidence")
	errRunnerLargeToolResultInvalid     = errors.New("runner large tool result evidence is invalid")
)

// RunnerLargeToolResult is immutable internal runtime evidence produced when a
// tool result exceeds the model inline budget. It is deliberately NOT an
// artifact: it never appears in project files, artifact listings, exports, or
// generated-file counts, and it is served only through the internal evidence
// read path.
type RunnerLargeToolResult struct {
	ArtifactID    string
	VersionID     string
	ProjectID     string
	RootFrameID   string
	FrameID       string
	StreamUID     string
	OwnerUserID   string
	RunnerID      string
	ClaimToken    string
	Attempt       int64
	SourceEventID int64
	ToolName      string
	ToolCallID    string
	ContentType   string
	SizeBytes     int64
	ContentSHA256 string
	StoragePath   string
	CreatedAt     time.Time
}

type WriteRunnerLargeToolResultInput struct {
	ArtifactID    string
	ProjectID     string
	RootFrameID   string
	FrameID       string
	StreamUID     string
	OwnerUserID   string
	RunnerID      string
	ClaimToken    string
	Attempt       int64
	SourceEventID int64
	ToolName      string
	ToolCallID    string
	Content       []byte
}

// IsRunnerLargeToolResultArtifactID reports whether value has the immutable
// internal evidence identity shape (prefix + 32 hex characters).
func IsRunnerLargeToolResultArtifactID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != len(runnerLargeToolResultArtifactIDPrefix)+32 ||
		!strings.HasPrefix(value, runnerLargeToolResultArtifactIDPrefix) {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, runnerLargeToolResultArtifactIDPrefix))
	return err == nil
}

// IsRunnerLargeToolResultVersionID reports whether value belongs to the
// immutable oversized-tool-result namespace. Callers use this as explicit
// type dispatch; these versions remain outside user-visible artifacts.
func IsRunnerLargeToolResultVersionID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != len(runnerLargeToolResultVersionPrefix)+36 ||
		!strings.HasPrefix(value, runnerLargeToolResultVersionPrefix) {
		return false
	}
	_, err := uuid.Parse(strings.TrimPrefix(value, runnerLargeToolResultVersionPrefix))
	return err == nil
}

// WriteRunnerLargeToolResult durably stores one exact, immutable tool result
// outside the artifact/project-file store. Replaying the same identity with
// identical content returns the existing evidence; any byte change conflicts.
func (s *Store) WriteRunnerLargeToolResult(
	ctx context.Context,
	input WriteRunnerLargeToolResultInput,
) (RunnerLargeToolResult, error) {
	if s == nil || s.db == nil {
		return RunnerLargeToolResult{}, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	input.ArtifactID = strings.TrimSpace(input.ArtifactID)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.RootFrameID = strings.TrimSpace(input.RootFrameID)
	input.FrameID = strings.TrimSpace(input.FrameID)
	input.StreamUID = strings.TrimSpace(input.StreamUID)
	input.OwnerUserID = strings.TrimSpace(input.OwnerUserID)
	input.RunnerID = strings.TrimSpace(input.RunnerID)
	input.ClaimToken = strings.TrimSpace(input.ClaimToken)
	input.ToolName = strings.TrimSpace(input.ToolName)
	input.ToolCallID = strings.TrimSpace(input.ToolCallID)
	if !IsRunnerLargeToolResultArtifactID(input.ArtifactID) || input.ProjectID == "" ||
		input.RootFrameID == "" || input.FrameID == "" || input.StreamUID == "" ||
		input.OwnerUserID == "" || input.RunnerID == "" || input.ClaimToken == "" ||
		input.ToolName == "" || input.ToolCallID == "" || input.Attempt < 0 ||
		input.SourceEventID <= 0 || len(input.Content) == 0 ||
		!json.Valid(input.Content) {
		return RunnerLargeToolResult{}, errRunnerLargeToolResultInvalid
	}
	versionID := runnerLargeToolResultVersionPrefix + uuid.NewString()
	// The original result is already materialized by the tool contract. Bound
	// the copy to those exact bytes; the shared writer checks actual remaining
	// disk capacity as it streams, without an unrelated product-size ceiling.
	temporary, size, digest, err := s.stageArtifactWrite(ctx, bytes.NewReader(input.Content), int64(len(input.Content)))
	if err != nil {
		return RunnerLargeToolResult{}, err
	}
	relativePath := runnerLargeToolResultBlobPath(versionID)
	stagingPath, err := s.blobRelative(temporary)
	if err != nil {
		return RunnerLargeToolResult{}, err
	}
	commitAttempted := false
	defer func() {
		if commitAttempted {
			return
		}
		_ = os.Remove(temporary)
	}()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunnerLargeToolResult{}, fmt.Errorf("begin runner large tool result transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var projectOwner string
	if err := tx.QueryRowContext(ctx, `SELECT user_id FROM projects WHERE id = ?`, input.ProjectID).Scan(&projectOwner); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RunnerLargeToolResult{}, fmt.Errorf("project %q does not exist", input.ProjectID)
		}
		return RunnerLargeToolResult{}, fmt.Errorf("look up runner large tool result project: %w", err)
	}
	if projectOwner != input.OwnerUserID {
		return RunnerLargeToolResult{}, errors.New("runner large tool result project belongs to another user")
	}
	var frameProjectID string
	if err := tx.QueryRowContext(ctx, `SELECT project_id FROM frames WHERE id = ?`, input.FrameID).Scan(&frameProjectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RunnerLargeToolResult{}, fmt.Errorf("frame %q does not exist", input.FrameID)
		}
		return RunnerLargeToolResult{}, fmt.Errorf("look up runner large tool result frame: %w", err)
	}
	if frameProjectID != input.ProjectID {
		return RunnerLargeToolResult{}, errors.New("runner large tool result frame belongs to another project")
	}

	existing, found, err := findRunnerLargeToolResultByArtifactTx(ctx, tx, input.ArtifactID)
	if err != nil {
		return RunnerLargeToolResult{}, err
	}
	if found {
		if existing.ToolCallID != input.ToolCallID || existing.ToolName != input.ToolName ||
			existing.SizeBytes != size || existing.ContentSHA256 != digest {
			return RunnerLargeToolResult{}, ErrRunnerLargeToolResultConflict
		}
		return existing, nil
	}

	now := s.now().UTC()
	record := RunnerLargeToolResult{
		ArtifactID: input.ArtifactID, VersionID: versionID, ProjectID: input.ProjectID,
		RootFrameID: input.RootFrameID, FrameID: input.FrameID, StreamUID: input.StreamUID,
		OwnerUserID: input.OwnerUserID, RunnerID: input.RunnerID, ClaimToken: input.ClaimToken,
		Attempt: input.Attempt, SourceEventID: input.SourceEventID, ToolName: input.ToolName,
		ToolCallID: input.ToolCallID, ContentType: "application/json", SizeBytes: size,
		ContentSHA256: digest, StoragePath: relativePath, CreatedAt: now,
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runner_large_tool_results(
		artifact_id,version_id,project_id,root_frame_id,frame_id,stream_uid,owner_user_id,
		runner_id,claim_token,attempt,source_event_id,tool_name,tool_call_id,content_type,
		size_bytes,content_sha256,storage_path,created_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		record.ArtifactID, record.VersionID, record.ProjectID, record.RootFrameID, record.FrameID,
		record.StreamUID, record.OwnerUserID, record.RunnerID, record.ClaimToken, record.Attempt,
		record.SourceEventID, record.ToolName, record.ToolCallID, record.ContentType,
		record.SizeBytes, record.ContentSHA256, record.StoragePath,
		record.CreatedAt.Format(time.RFC3339Nano)); err != nil {
		return RunnerLargeToolResult{}, fmt.Errorf("insert runner large tool result: %w", err)
	}
	marker := blobCommitMarker{
		ID: "runner-large-tool-result:" + versionID, Kind: "runner_large_tool_result",
		AggregateID: versionID, StagingPath: stagingPath, FinalPath: relativePath,
		SHA256: digest, SizeBytes: size, CreatedAt: now,
	}
	if err := s.insertBlobCommitMarkerTx(ctx, tx, marker); err != nil {
		return RunnerLargeToolResult{}, err
	}
	commitAttempted = true
	if err := tx.Commit(); err != nil {
		return RunnerLargeToolResult{}, fmt.Errorf("commit runner large tool result: %w", err)
	}
	if err := s.finalizeBlobCommit(ctx, marker); err != nil {
		return RunnerLargeToolResult{}, fmt.Errorf("finalize committed runner large tool result blob: %w", err)
	}
	return record, nil
}

// FindRunnerLargeToolResult returns the immutable evidence for one exact tool
// call, or false when this identity has never been externalized.
func (s *Store) FindRunnerLargeToolResult(
	ctx context.Context,
	artifactID, toolCallID, toolName string,
) (RunnerLargeToolResult, bool, error) {
	if s == nil || s.db == nil {
		return RunnerLargeToolResult{}, false, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	artifactID, toolCallID, toolName = strings.TrimSpace(artifactID), strings.TrimSpace(toolCallID), strings.TrimSpace(toolName)
	if !IsRunnerLargeToolResultArtifactID(artifactID) || toolCallID == "" || toolName == "" {
		return RunnerLargeToolResult{}, false, errRunnerLargeToolResultInvalid
	}
	return scanRunnerLargeToolResultRow(s.db.QueryRowContext(ctx, `SELECT `+runnerLargeToolResultSelect+`
		WHERE artifact_id=? AND tool_call_id=? AND tool_name=?`, artifactID, toolCallID, toolName))
}

// GetRunnerLargeToolResult returns the current (single) evidence version for
// an internal artifact identity, scoped to the owner.
func (s *Store) GetRunnerLargeToolResult(
	ctx context.Context,
	artifactID, ownerUserID string,
) (RunnerLargeToolResult, bool, error) {
	if s == nil || s.db == nil {
		return RunnerLargeToolResult{}, false, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	artifactID, ownerUserID = strings.TrimSpace(artifactID), strings.TrimSpace(ownerUserID)
	if !IsRunnerLargeToolResultArtifactID(artifactID) || ownerUserID == "" {
		return RunnerLargeToolResult{}, false, errRunnerLargeToolResultInvalid
	}
	return scanRunnerLargeToolResultRow(s.db.QueryRowContext(ctx, `SELECT `+runnerLargeToolResultSelect+`
		WHERE artifact_id=? AND owner_user_id=?`, artifactID, ownerUserID))
}

// OpenRunnerLargeToolResultContent returns the owner-scoped, size-validated
// blob reader for one internal evidence version.
func (s *Store) OpenRunnerLargeToolResultContent(
	ctx context.Context,
	versionID, ownerUserID string,
) (RunnerLargeToolResult, ArtifactContentReader, bool, error) {
	if s == nil || s.db == nil {
		return RunnerLargeToolResult{}, nil, false, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	versionID, ownerUserID = strings.TrimSpace(versionID), strings.TrimSpace(ownerUserID)
	if versionID == "" || ownerUserID == "" {
		return RunnerLargeToolResult{}, nil, false, errRunnerLargeToolResultInvalid
	}
	record, found, err := scanRunnerLargeToolResultRow(s.db.QueryRowContext(ctx, `SELECT `+runnerLargeToolResultSelect+`
		WHERE version_id=? AND owner_user_id=?`, versionID, ownerUserID))
	if err != nil || !found {
		return record, nil, found, err
	}
	reader, err := s.openRunnerLargeToolResultBlob(record)
	if err != nil {
		return RunnerLargeToolResult{}, nil, false, err
	}
	return record, reader, true, nil
}

const runnerLargeToolResultSelect = `artifact_id,version_id,project_id,root_frame_id,frame_id,
	stream_uid,owner_user_id,runner_id,claim_token,attempt,source_event_id,tool_name,
	tool_call_id,content_type,size_bytes,content_sha256,storage_path,created_at
	FROM runner_large_tool_results`

func scanRunnerLargeToolResultRow(row *sql.Row) (RunnerLargeToolResult, bool, error) {
	var record RunnerLargeToolResult
	var createdAt string
	err := row.Scan(
		&record.ArtifactID, &record.VersionID, &record.ProjectID, &record.RootFrameID, &record.FrameID,
		&record.StreamUID, &record.OwnerUserID, &record.RunnerID, &record.ClaimToken, &record.Attempt,
		&record.SourceEventID, &record.ToolName, &record.ToolCallID, &record.ContentType,
		&record.SizeBytes, &record.ContentSHA256, &record.StoragePath, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return RunnerLargeToolResult{}, false, nil
	}
	if err != nil {
		return RunnerLargeToolResult{}, false, fmt.Errorf("get runner large tool result: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return RunnerLargeToolResult{}, false, fmt.Errorf("parse runner large tool result timestamp: %w", err)
	}
	record.CreatedAt = parsed
	return record, true, nil
}

func findRunnerLargeToolResultByArtifactTx(
	ctx context.Context,
	tx *sql.Tx,
	artifactID string,
) (RunnerLargeToolResult, bool, error) {
	var record RunnerLargeToolResult
	var createdAt string
	err := tx.QueryRowContext(ctx, `SELECT `+runnerLargeToolResultSelect+` WHERE artifact_id=?`, artifactID).Scan(
		&record.ArtifactID, &record.VersionID, &record.ProjectID, &record.RootFrameID, &record.FrameID,
		&record.StreamUID, &record.OwnerUserID, &record.RunnerID, &record.ClaimToken, &record.Attempt,
		&record.SourceEventID, &record.ToolName, &record.ToolCallID, &record.ContentType,
		&record.SizeBytes, &record.ContentSHA256, &record.StoragePath, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return RunnerLargeToolResult{}, false, nil
	}
	if err != nil {
		return RunnerLargeToolResult{}, false, fmt.Errorf("get runner large tool result by artifact: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return RunnerLargeToolResult{}, false, fmt.Errorf("parse runner large tool result timestamp: %w", err)
	}
	record.CreatedAt = parsed
	return record, true, nil
}

func (s *Store) openRunnerLargeToolResultBlob(record RunnerLargeToolResult) (ArtifactContentReader, error) {
	if strings.TrimSpace(record.StoragePath) == "" {
		return nil, errors.New("runner large tool result blob path is required")
	}
	absolute, err := s.blobAbsolute(record.StoragePath)
	if err != nil {
		return nil, err
	}
	file, err := openRegularFile(absolute)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errors.Join(ErrRunnerLargeToolResultUnavailable, fmt.Errorf("open runner large tool result blob: %w", err))
		}
		return nil, fmt.Errorf("open runner large tool result blob: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("stat runner large tool result blob: %w", err)
	}
	if info.Size() != record.SizeBytes {
		_ = file.Close()
		return nil, fmt.Errorf("runner large tool result blob size mismatch: got %d, want %d", info.Size(), record.SizeBytes)
	}
	return file, nil
}

func runnerLargeToolResultBlobPath(versionID string) string {
	prefix := versionID
	if len(prefix) > 2 {
		prefix = prefix[:2]
	}
	return filepath.ToSlash(filepath.Join("large-tool-results", prefix, versionID+".blob"))
}

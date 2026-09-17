package transcript

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

const RunnerTaskMemorySnapshotKind = "runner_task_memory_snapshot_v1"

type RunnerTaskMemorySnapshot struct {
	Kind                 string `json:"kind"`
	TaskIntentID         string `json:"task_intent_id"`
	TaskIntentRevision   int64  `json:"task_intent_revision"`
	InitialInputRevision int64  `json:"initial_input_revision"`
	SessionMemory        string `json:"session_memory"`
	WorkspaceMemory      string `json:"workspace_memory"`
	PolicyVersion        string `json:"policy_version"`
	SHA256               string `json:"sha256"`
}

func SealRunnerTaskMemorySnapshot(snapshot RunnerTaskMemorySnapshot) (RunnerTaskMemorySnapshot, error) {
	snapshot.Kind = RunnerTaskMemorySnapshotKind
	snapshot.TaskIntentID = strings.TrimSpace(snapshot.TaskIntentID)
	snapshot.PolicyVersion = strings.TrimSpace(snapshot.PolicyVersion)
	snapshot.SHA256 = ""
	if snapshot.TaskIntentID == "" || snapshot.TaskIntentRevision <= 0 || snapshot.InitialInputRevision <= 0 || snapshot.PolicyVersion == "" {
		return RunnerTaskMemorySnapshot{}, ErrEventConflict
	}
	raw, err := json.Marshal(snapshot)
	if err != nil || len(raw) > MaxEventPayloadBytes {
		return RunnerTaskMemorySnapshot{}, ErrEventConflict
	}
	digest := sha256.Sum256(raw)
	snapshot.SHA256 = hex.EncodeToString(digest[:])
	return snapshot, nil
}

func ValidateRunnerTaskMemorySnapshot(snapshot RunnerTaskMemorySnapshot) error {
	claimedDigest := strings.ToLower(strings.TrimSpace(snapshot.SHA256))
	if len(claimedDigest) != sha256.Size*2 {
		return ErrEventConflict
	}
	sealed, err := SealRunnerTaskMemorySnapshot(snapshot)
	if err != nil || sealed.SHA256 != claimedDigest {
		return ErrEventConflict
	}
	return nil
}

func (r *Repository) GetRunnerTaskMemorySnapshot(
	ctx context.Context,
	streamUID, ownerID string,
	attempt int64,
) (RunnerTaskMemorySnapshot, bool, error) {
	streamUID = strings.TrimSpace(streamUID)
	ownerID = strings.TrimSpace(ownerID)
	if r == nil || r.db == nil || streamUID == "" || ownerID == "" || attempt <= 0 {
		return RunnerTaskMemorySnapshot{}, false, ErrEventConflict
	}
	if _, err := r.GetStream(ctx, streamUID, ownerID); err != nil {
		return RunnerTaskMemorySnapshot{}, false, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT payload_json FROM transcript_events
		WHERE stream_uid=? AND runner_attempt=? AND event_type='runner_checkpoint'
			AND source='payload' AND json_extract(payload_json,'$.kind')=?
		ORDER BY event_id`, streamUID, attempt, RunnerTaskMemorySnapshotKind)
	if err != nil {
		return RunnerTaskMemorySnapshot{}, false, schemaError(err)
	}
	defer rows.Close()
	var result RunnerTaskMemorySnapshot
	found := false
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return RunnerTaskMemorySnapshot{}, false, schemaError(err)
		}
		var snapshot RunnerTaskMemorySnapshot
		if err := json.Unmarshal(raw, &snapshot); err != nil || ValidateRunnerTaskMemorySnapshot(snapshot) != nil {
			return RunnerTaskMemorySnapshot{}, false, ErrEventConflict
		}
		if found && snapshot != result {
			return RunnerTaskMemorySnapshot{}, false, ErrEventConflict
		}
		result = snapshot
		found = true
	}
	if err := rows.Err(); err != nil {
		return RunnerTaskMemorySnapshot{}, false, schemaError(err)
	}
	if err := rows.Close(); err != nil {
		return RunnerTaskMemorySnapshot{}, false, schemaError(err)
	}
	return result, found, nil
}

// GetRunnerTaskMemorySnapshotForTask returns the immutable snapshot for one
// task intent in one runner attempt. A single attempt may contain a resumed
// user correction that starts a newer task intent; those snapshots must not be
// treated as conflicting state for the currently active task.
func (r *Repository) GetRunnerTaskMemorySnapshotForTask(
	ctx context.Context,
	streamUID, ownerID string,
	attempt int64,
	taskIntentID string,
	taskIntentRevision int64,
) (RunnerTaskMemorySnapshot, bool, error) {
	streamUID = strings.TrimSpace(streamUID)
	ownerID = strings.TrimSpace(ownerID)
	taskIntentID = strings.TrimSpace(taskIntentID)
	if r == nil || r.db == nil || streamUID == "" || ownerID == "" || attempt <= 0 || taskIntentID == "" || taskIntentRevision <= 0 {
		return RunnerTaskMemorySnapshot{}, false, ErrEventConflict
	}
	if _, err := r.GetStream(ctx, streamUID, ownerID); err != nil {
		return RunnerTaskMemorySnapshot{}, false, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT payload_json FROM transcript_events
		WHERE stream_uid=? AND runner_attempt=? AND event_type='runner_checkpoint'
			AND source='payload' AND json_extract(payload_json,'$.kind')=?
			AND json_extract(payload_json,'$.task_intent_id')=?
			AND json_extract(payload_json,'$.task_intent_revision')=?
		ORDER BY event_id`, streamUID, attempt, RunnerTaskMemorySnapshotKind, taskIntentID, taskIntentRevision)
	if err != nil {
		return RunnerTaskMemorySnapshot{}, false, schemaError(err)
	}
	defer rows.Close()
	var result RunnerTaskMemorySnapshot
	found := false
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return RunnerTaskMemorySnapshot{}, false, schemaError(err)
		}
		var snapshot RunnerTaskMemorySnapshot
		if err := json.Unmarshal(raw, &snapshot); err != nil || ValidateRunnerTaskMemorySnapshot(snapshot) != nil {
			return RunnerTaskMemorySnapshot{}, false, ErrEventConflict
		}
		if found && snapshot != result {
			return RunnerTaskMemorySnapshot{}, false, ErrEventConflict
		}
		result = snapshot
		found = true
	}
	if err := rows.Err(); err != nil {
		return RunnerTaskMemorySnapshot{}, false, schemaError(err)
	}
	if err := rows.Close(); err != nil {
		return RunnerTaskMemorySnapshot{}, false, schemaError(err)
	}
	return result, found, nil
}

// LatestRunnerTaskMemorySnapshot returns the newest durable task memory
// snapshot for a stream regardless of runner attempt. It is used to carry
// memory context forward when a resumed attempt pauses (for example, kernel
// local execution approval) before it has called the model on that attempt.
func (r *Repository) LatestRunnerTaskMemorySnapshot(
	ctx context.Context,
	streamUID, ownerID string,
) (RunnerTaskMemorySnapshot, bool, error) {
	streamUID = strings.TrimSpace(streamUID)
	ownerID = strings.TrimSpace(ownerID)
	if r == nil || r.db == nil || streamUID == "" || ownerID == "" {
		return RunnerTaskMemorySnapshot{}, false, ErrEventConflict
	}
	if _, err := r.GetStream(ctx, streamUID, ownerID); err != nil {
		return RunnerTaskMemorySnapshot{}, false, err
	}
	var raw []byte
	err := r.db.QueryRowContext(ctx, `
		SELECT payload_json FROM transcript_events
		WHERE stream_uid=? AND event_type='runner_checkpoint' AND source='payload'
			AND json_extract(payload_json,'$.kind')=?
		ORDER BY event_id DESC LIMIT 1`, streamUID, RunnerTaskMemorySnapshotKind,
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return RunnerTaskMemorySnapshot{}, false, nil
	}
	if err != nil {
		return RunnerTaskMemorySnapshot{}, false, schemaError(err)
	}
	var snapshot RunnerTaskMemorySnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil || ValidateRunnerTaskMemorySnapshot(snapshot) != nil {
		return RunnerTaskMemorySnapshot{}, false, ErrEventConflict
	}
	return snapshot, true, nil
}

func (r *Repository) GetRunnerTaskMemorySnapshotForCheckpoint(
	ctx context.Context,
	streamUID, ownerID string,
	checkpointSequence int64,
) (RunnerTaskMemorySnapshot, bool, error) {
	streamUID = strings.TrimSpace(streamUID)
	ownerID = strings.TrimSpace(ownerID)
	if r == nil || r.db == nil || streamUID == "" || ownerID == "" || checkpointSequence <= 0 {
		return RunnerTaskMemorySnapshot{}, false, ErrEventConflict
	}
	if _, err := r.GetStream(ctx, streamUID, ownerID); err != nil {
		return RunnerTaskMemorySnapshot{}, false, err
	}
	var attempt int64
	if err := r.db.QueryRowContext(ctx, `
		SELECT runner_attempt FROM transcript_runner_checkpoints
		WHERE stream_uid=? AND checkpoint_sequence=? AND resumable=1`,
		streamUID, checkpointSequence,
	).Scan(&attempt); err != nil {
		return RunnerTaskMemorySnapshot{}, false, schemaError(err)
	}
	return r.GetRunnerTaskMemorySnapshot(ctx, streamUID, ownerID, attempt)
}

// LatestRunnerTaskMemorySnapshotForTask returns the newest valid snapshot for
// one task intent, ignoring snapshots belonging to other intents in the same
// stream.
func (r *Repository) LatestRunnerTaskMemorySnapshotForTask(
	ctx context.Context,
	streamUID, ownerID, taskIntentID string,
	taskIntentRevision int64,
) (RunnerTaskMemorySnapshot, bool, error) {
	streamUID = strings.TrimSpace(streamUID)
	ownerID = strings.TrimSpace(ownerID)
	taskIntentID = strings.TrimSpace(taskIntentID)
	if r == nil || r.db == nil || streamUID == "" || ownerID == "" || taskIntentID == "" || taskIntentRevision <= 0 {
		return RunnerTaskMemorySnapshot{}, false, ErrEventConflict
	}
	if _, err := r.GetStream(ctx, streamUID, ownerID); err != nil {
		return RunnerTaskMemorySnapshot{}, false, err
	}
	var raw []byte
	err := r.db.QueryRowContext(ctx, `
		SELECT payload_json FROM transcript_events
		WHERE stream_uid=? AND event_type='runner_checkpoint' AND source='payload'
			AND json_extract(payload_json,'$.kind')=?
			AND json_extract(payload_json,'$.task_intent_id')=?
			AND json_extract(payload_json,'$.task_intent_revision')=?
		ORDER BY event_id DESC LIMIT 1`, streamUID, RunnerTaskMemorySnapshotKind, taskIntentID, taskIntentRevision).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return RunnerTaskMemorySnapshot{}, false, nil
	}
	if err != nil {
		return RunnerTaskMemorySnapshot{}, false, schemaError(err)
	}
	var snapshot RunnerTaskMemorySnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil || ValidateRunnerTaskMemorySnapshot(snapshot) != nil {
		return RunnerTaskMemorySnapshot{}, false, ErrEventConflict
	}
	return snapshot, true, nil
}

// GetRunnerTaskMemorySnapshotForCheckpointTask is the checkpoint-scoped form
// of GetRunnerTaskMemorySnapshotForTask. It keeps recovery tied to the active
// task intent even when one runner attempt contains multiple input revisions.
func (r *Repository) GetRunnerTaskMemorySnapshotForCheckpointTask(
	ctx context.Context,
	streamUID, ownerID string,
	checkpointSequence int64,
	taskIntentID string,
	taskIntentRevision int64,
) (RunnerTaskMemorySnapshot, bool, error) {
	streamUID = strings.TrimSpace(streamUID)
	ownerID = strings.TrimSpace(ownerID)
	if r == nil || r.db == nil || streamUID == "" || ownerID == "" || checkpointSequence <= 0 {
		return RunnerTaskMemorySnapshot{}, false, ErrEventConflict
	}
	if _, err := r.GetStream(ctx, streamUID, ownerID); err != nil {
		return RunnerTaskMemorySnapshot{}, false, err
	}
	var attempt int64
	if err := r.db.QueryRowContext(ctx, `
		SELECT runner_attempt FROM transcript_runner_checkpoints
		WHERE stream_uid=? AND checkpoint_sequence=? AND resumable=1`,
		streamUID, checkpointSequence,
	).Scan(&attempt); err != nil {
		return RunnerTaskMemorySnapshot{}, false, schemaError(err)
	}
	return r.GetRunnerTaskMemorySnapshotForTask(ctx, streamUID, ownerID, attempt, taskIntentID, taskIntentRevision)
}

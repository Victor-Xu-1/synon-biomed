package transcript

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type Branch struct {
	StreamUID       string
	BranchID        string
	ParentBranchID  string
	ForkEventID     *int64
	ForkPoint       int64
	Kind            string
	SourceMessageID string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Active          bool
}

func (r *Repository) ListBranches(
	ctx context.Context,
	streamUID, ownerID string,
) (BranchState, []Branch, error) {
	db := r.readDatabase()
	if db == nil {
		return BranchState{}, nil, ErrSchemaUnavailable
	}
	streamUID = strings.TrimSpace(streamUID)
	ownerID = strings.TrimSpace(ownerID)
	if streamUID == "" || ownerID == "" {
		return BranchState{}, nil, errors.New("stream and owner are required")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return BranchState{}, nil, schemaError(err)
	}
	defer func() { _ = tx.Rollback() }()
	var state BranchState
	var storedOwner string
	err = tx.QueryRowContext(ctx, `
		SELECT state.stream_uid,stream.owner_id,state.active_branch_id,state.generation,state.updated_at
		FROM transcript_branch_state state
		JOIN transcript_streams stream ON stream.stream_uid=state.stream_uid
		WHERE state.stream_uid=?`, streamUID,
	).Scan(&state.StreamUID, &storedOwner, &state.ActiveBranchID, &state.Generation, &state.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return BranchState{}, nil, ErrBranchTargetNotFound
	}
	if err != nil {
		return BranchState{}, nil, schemaError(err)
	}
	if storedOwner != ownerID {
		return BranchState{}, nil, ErrOwnerMismatch
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT branch_id,parent_branch_id,fork_event_id,fork_point,kind,source_message_id,created_at,updated_at
		FROM transcript_branches WHERE stream_uid=? ORDER BY created_at,branch_id`, streamUID)
	if err != nil {
		return BranchState{}, nil, schemaError(err)
	}
	branches := []Branch{}
	for rows.Next() {
		var branch Branch
		var parent sql.NullString
		var forkEvent sql.NullInt64
		if err := rows.Scan(
			&branch.BranchID, &parent, &forkEvent, &branch.ForkPoint, &branch.Kind,
			&branch.SourceMessageID, &branch.CreatedAt, &branch.UpdatedAt,
		); err != nil {
			_ = rows.Close()
			return BranchState{}, nil, schemaError(err)
		}
		branch.StreamUID = streamUID
		if parent.Valid {
			branch.ParentBranchID = parent.String
		}
		if forkEvent.Valid {
			value := forkEvent.Int64
			branch.ForkEventID = &value
		}
		branch.Active = branch.BranchID == state.ActiveBranchID
		branches = append(branches, branch)
	}
	if err := rows.Close(); err != nil {
		return BranchState{}, nil, schemaError(err)
	}
	if err := rows.Err(); err != nil {
		return BranchState{}, nil, schemaError(err)
	}
	if len(branches) == 0 {
		return BranchState{}, nil, ErrEventConflict
	}
	if err := tx.Commit(); err != nil {
		return BranchState{}, nil, schemaError(err)
	}
	return state, branches, nil
}

package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	transcriptstore "synon-go/internal/persistence/transcript"
	"synon-go/internal/toolcontract"
)

const (
	ToolCallBatchStateReady          = "ready"
	ToolCallBatchStateRunning        = "running"
	ToolCallBatchStateWaiting        = "waiting"
	ToolCallBatchStateSettled        = "settled"
	ToolCallBatchStateCancelled      = "cancelled"
	ToolCallBatchStateOutcomeUnknown = "outcome_unknown"

	ToolCallBatchItemStatePending        = "pending"
	ToolCallBatchItemStateRunning        = "running"
	ToolCallBatchItemStateWaiting        = "waiting"
	ToolCallBatchItemStateCompleted      = "completed"
	ToolCallBatchItemStateFailed         = "failed"
	ToolCallBatchItemStateBlocked        = "blocked"
	ToolCallBatchItemStateCancelled      = "cancelled"
	ToolCallBatchItemStateOutcomeUnknown = "outcome_unknown"

	toolCallBatchIdentityDomain            = "synon.tool-call-batch.v1"
	maxToolCallIdentityBytes               = 512
	maxToolNameBytes                       = 256
	maxToolArgumentsBytes                  = 1 << 20
	maxToolResultReferenceBytes            = 4096
	detachedKernelRecoveryClaimTokenPrefix = "detached-kernel-recovery:"
)

var (
	ErrToolCallBatchConflict = errors.New("tool call batch conflicts with durable state")
	ErrToolCallBatchStale    = errors.New("tool call batch state is stale")
)

type ToolCallBatch struct {
	BatchID               string
	OwnerUserID           string
	StreamUID             string
	BranchID              string
	BranchGeneration      int64
	SourceEventID         int64
	SourcePublicationSeq  int64
	SourceRunnerAttempt   int64
	SourceClientMessageID string
	AdmittedInputRevision int64
	CallCount             int64
	NextOrdinal           int64
	State                 string
	StateVersion          int64
	WaitingOrdinal        *int64
	RunnerID              string
	RunnerAttempt         int64
	RunnerClaimSHA256     string
	ReasonCode            string
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type ToolCallBatchItem struct {
	BatchID              string
	StreamUID            string
	Ordinal              int64
	ToolCallID           string
	ToolName             string
	ArgumentsJSON        []byte
	ArgumentsSHA256      string
	State                string
	StateVersion         int64
	StartedEventID       int64
	WaitingEventID       int64
	TerminalEventID      int64
	TerminalResultSHA256 string
	ResultRef            string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type ClaimToolCallBatchInput struct {
	Claim                transcriptstore.RunnerClaim
	BatchID              string
	ExpectedStateVersion int64
}

type StartToolCallBatchItemInput struct {
	Claim                     transcriptstore.RunnerClaim
	BatchID                   string
	Ordinal                   int64
	ExpectedBatchStateVersion int64
	ExpectedItemStateVersion  int64
	StartedEvent              transcriptstore.Event
}

type WaitToolCallBatchItemInput struct {
	Claim                     transcriptstore.RunnerClaim
	BatchID                   string
	Ordinal                   int64
	ExpectedBatchStateVersion int64
	ExpectedItemStateVersion  int64
	WaitingEvent              transcriptstore.Event
}

type FinishToolCallBatchItemInput struct {
	Claim                     transcriptstore.RunnerClaim
	BatchID                   string
	Ordinal                   int64
	ExpectedBatchStateVersion int64
	ExpectedItemStateVersion  int64
	TerminalEvent             transcriptstore.Event
	TerminalState             string
	TerminalPhase             string
	RejectedBeforeExecution   bool
	ResultRef                 string
}

// ReconcileKernelToolCallBatchInput identifies a kernel item whose terminal
// protocol checkpoint was committed but whose batch CAS update was lost. The
// operation receipt and checkpoint remain the authority; this method only
// repairs the derived tool-batch cursor under the current live claim.
type ReconcileKernelToolCallBatchInput struct {
	Claim       transcriptstore.RunnerClaim
	BatchID     string
	Ordinal     int64
	OperationID string
}

type canonicalToolCallBatchItem struct {
	ID              string
	Name            string
	ArgumentsJSON   []byte
	ArgumentsSHA256 string
}

// CreateToolCallBatchForCheckpointTx persists the complete immutable model
// tool-call batch in the same SQLite transaction as its canonical Transcript
// checkpoint. Replays must match the stored batch and every item byte-for-byte.
func (s *Store) CreateToolCallBatchForCheckpointTx(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	event transcriptstore.Event,
) (ToolCallBatch, []ToolCallBatchItem, bool, error) {
	if s == nil || tx == nil || ctx == nil || strings.TrimSpace(event.StreamUID) == "" || event.EventID <= 0 {
		return ToolCallBatch{}, nil, false, errors.New("workspace, transcript transaction, and source checkpoint are required")
	}

	var eventType, source, clientMessageID string
	var publicationSeq, runnerAttempt int64
	var payloadJSON []byte
	var createdAt time.Time
	err := tx.QueryRowContext(ctx, `SELECT event_type,source,client_message_id,publication_seq,runner_attempt,payload_json,created_at
		FROM transcript_events WHERE stream_uid=? AND event_id=?`, event.StreamUID, event.EventID).Scan(
		&eventType, &source, &clientMessageID, &publicationSeq, &runnerAttempt, &payloadJSON, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ToolCallBatch{}, nil, false, ErrToolCallBatchConflict
	}
	if err != nil {
		return ToolCallBatch{}, nil, false, err
	}
	if eventType != "runner_checkpoint" || source != string(transcriptstore.EventSourcePayload) ||
		publicationSeq != event.PublicationSeq || clientMessageID != event.ClientMessageID ||
		event.RunnerAttempt == nil || runnerAttempt != *event.RunnerAttempt || !bytes.Equal(payloadJSON, event.PayloadJSON) {
		return ToolCallBatch{}, nil, false, ErrToolCallBatchConflict
	}

	calls, err := canonicalToolCallBatchItems(payloadJSON)
	if err != nil {
		return ToolCallBatch{}, nil, false, err
	}
	batchID := toolCallBatchID(event.StreamUID, event.EventID)
	if existing, found, err := getToolCallBatchQuery(ctx, tx, batchID); err != nil {
		return ToolCallBatch{}, nil, false, err
	} else if found {
		items, err := listToolCallBatchItemsQuery(ctx, tx, existing.BatchID)
		if err != nil {
			return ToolCallBatch{}, nil, false, err
		}
		if !toolCallBatchOriginMatches(existing, event, calls) || !toolCallBatchItemsMatch(items, calls) {
			return ToolCallBatch{}, nil, false, ErrToolCallBatchConflict
		}
		return existing, items, false, nil
	}

	var ownerUserID, branchID string
	var branchGeneration, admittedInputRevision int64
	err = tx.QueryRowContext(ctx, `SELECT stream.owner_id,branch.active_branch_id,branch.generation,attempt.claimed_input_revision
		FROM transcript_streams stream
		JOIN transcript_branch_state branch ON branch.stream_uid=stream.stream_uid
		JOIN transcript_runner_attempts attempt ON attempt.stream_uid=stream.stream_uid AND attempt.attempt=?
		WHERE stream.stream_uid=?`, runnerAttempt, event.StreamUID).Scan(
		&ownerUserID, &branchID, &branchGeneration, &admittedInputRevision,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ToolCallBatch{}, nil, false, ErrToolCallBatchConflict
	}
	if err != nil {
		return ToolCallBatch{}, nil, false, err
	}
	var membership int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcript_branch_events
		WHERE stream_uid=? AND branch_id=? AND event_id=?`, event.StreamUID, branchID, event.EventID).Scan(&membership); err != nil {
		return ToolCallBatch{}, nil, false, err
	}
	if strings.TrimSpace(ownerUserID) == "" || strings.TrimSpace(branchID) == "" || branchGeneration <= 0 ||
		admittedInputRevision <= 0 || membership != 1 {
		return ToolCallBatch{}, nil, false, ErrToolCallBatchConflict
	}

	createdAt = createdAt.UTC()
	batch := ToolCallBatch{
		BatchID: batchID, OwnerUserID: ownerUserID, StreamUID: event.StreamUID,
		BranchID: branchID, BranchGeneration: branchGeneration,
		SourceEventID: event.EventID, SourcePublicationSeq: publicationSeq, SourceRunnerAttempt: runnerAttempt,
		SourceClientMessageID: clientMessageID, AdmittedInputRevision: admittedInputRevision,
		CallCount: int64(len(calls)), NextOrdinal: 0, State: ToolCallBatchStateReady, StateVersion: 1,
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO transcript_tool_call_batches(
		batch_id,owner_user_id,stream_uid,branch_id,branch_generation,source_event_id,source_publication_seq,
		source_runner_attempt,source_client_message_id,admitted_input_revision,call_count,next_ordinal,state,state_version,
		waiting_ordinal,runner_id,runner_attempt,runner_claim_sha256,reason_code,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,0,?,1,NULL,NULL,NULL,NULL,'',?,?) ON CONFLICT(batch_id) DO NOTHING`,
		batch.BatchID, batch.OwnerUserID, batch.StreamUID, batch.BranchID, batch.BranchGeneration,
		batch.SourceEventID, batch.SourcePublicationSeq, batch.SourceRunnerAttempt, batch.SourceClientMessageID,
		batch.AdmittedInputRevision, batch.CallCount, batch.State, batch.CreatedAt.Format(time.RFC3339Nano),
		batch.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return ToolCallBatch{}, nil, false, err
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		if err != nil {
			return ToolCallBatch{}, nil, false, err
		}
		return ToolCallBatch{}, nil, false, ErrToolCallBatchConflict
	}

	items := make([]ToolCallBatchItem, 0, len(calls))
	for ordinal, call := range calls {
		item := ToolCallBatchItem{
			BatchID: batch.BatchID, StreamUID: batch.StreamUID, Ordinal: int64(ordinal),
			ToolCallID: call.ID, ToolName: call.Name, ArgumentsJSON: append([]byte(nil), call.ArgumentsJSON...),
			ArgumentsSHA256: call.ArgumentsSHA256, State: ToolCallBatchItemStatePending, StateVersion: 1,
			CreatedAt: createdAt, UpdatedAt: createdAt,
		}
		inserted, err := tx.ExecContext(ctx, `INSERT INTO transcript_tool_call_items(
			batch_id,stream_uid,ordinal,tool_call_id,tool_name,arguments_json,arguments_sha256,state,state_version,
			started_event_id,waiting_event_id,terminal_event_id,terminal_result_sha256,result_ref,created_at,updated_at
		) VALUES(?,?,?,?,?,?,?, ?,1,NULL,NULL,NULL,NULL,NULL,?,?)`,
			item.BatchID, item.StreamUID, item.Ordinal, item.ToolCallID, item.ToolName, string(item.ArgumentsJSON),
			item.ArgumentsSHA256, item.State, item.CreatedAt.Format(time.RFC3339Nano), item.UpdatedAt.Format(time.RFC3339Nano),
		)
		if err != nil {
			return ToolCallBatch{}, nil, false, err
		}
		if rows, err := inserted.RowsAffected(); err != nil || rows != 1 {
			if err != nil {
				return ToolCallBatch{}, nil, false, err
			}
			return ToolCallBatch{}, nil, false, ErrToolCallBatchConflict
		}
		items = append(items, item)
	}
	return batch, items, true, nil
}

func (s *Store) GetToolCallBatch(
	ctx context.Context,
	ownerUserID, batchID string,
) (ToolCallBatch, bool, error) {
	ownerUserID = strings.TrimSpace(ownerUserID)
	batchID = strings.TrimSpace(batchID)
	if s == nil || s.db == nil || ctx == nil || ownerUserID == "" || batchID == "" {
		return ToolCallBatch{}, false, errors.New("owner and tool call batch id are required")
	}
	batch, found, err := getToolCallBatchQuery(ctx, s.db, batchID)
	if err != nil || !found {
		return batch, found, err
	}
	if batch.OwnerUserID != ownerUserID {
		return ToolCallBatch{}, false, nil
	}
	return batch, true, nil
}

func (s *Store) ListToolCallBatchItems(
	ctx context.Context,
	ownerUserID, batchID string,
) ([]ToolCallBatchItem, error) {
	ownerUserID = strings.TrimSpace(ownerUserID)
	batchID = strings.TrimSpace(batchID)
	if s == nil || s.db == nil || ctx == nil || ownerUserID == "" || batchID == "" {
		return nil, errors.New("owner and tool call batch id are required")
	}
	batch, found, err := getToolCallBatchQuery(ctx, s.db, batchID)
	if err != nil {
		return nil, err
	}
	if !found || batch.OwnerUserID != ownerUserID {
		return []ToolCallBatchItem{}, nil
	}
	return listToolCallBatchItemsQuery(ctx, s.db, batchID)
}

// ListRunnableToolCallBatches returns only active-branch batches admitted to
// the exact live Transcript claim. The caller must claim a returned head by
// state version before starting or resuming its current ordinal.
func (s *Store) ListRunnableToolCallBatches(
	ctx context.Context,
	claim transcriptstore.RunnerClaim,
	limit int,
) ([]ToolCallBatch, error) {
	if s == nil || s.db == nil || ctx == nil || limit <= 0 || limit > 100 {
		return nil, errors.New("a live runner claim and batch limit between 1 and 100 are required")
	}
	repository, err := s.TranscriptRepository(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]ToolCallBatch, 0, limit)
	err = repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		stream, err := tx.ValidateLiveRunnerClaim(ctx, claim)
		if err != nil {
			return err
		}
		if stream.UID != claim.StreamUID || stream.OwnerID != claim.OwnerID || claim.ClaimedInputRevision <= 0 {
			return ErrToolCallBatchConflict
		}
		rows, err := tx.QueryContext(ctx, `SELECT batch.batch_id
			FROM transcript_tool_call_batches batch
			JOIN transcript_branch_state branch ON branch.stream_uid=batch.stream_uid
				AND branch.active_branch_id=batch.branch_id AND branch.generation=batch.branch_generation
			JOIN transcript_branch_events source ON source.stream_uid=batch.stream_uid
				AND source.branch_id=batch.branch_id AND source.event_id=batch.source_event_id
			WHERE batch.owner_user_id=? AND batch.stream_uid=? AND batch.admitted_input_revision<=?
				AND batch.state IN ('ready','running','waiting')
			ORDER BY batch.admitted_input_revision,batch.source_publication_seq,batch.batch_id LIMIT ?`,
			claim.OwnerID, claim.StreamUID, claim.ClaimedInputRevision, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		ids := make([]string, 0, limit)
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range ids {
			batch, found, err := getToolCallBatchQuery(ctx, tx, id)
			if err != nil {
				return err
			}
			if !found || validateToolCallBatchAuthority(ctx, tx, batch, claim) != nil {
				return ErrToolCallBatchConflict
			}
			result = append(result, batch)
		}
		return nil
	})
	return result, err
}

func (s *Store) ClaimToolCallBatch(
	ctx context.Context,
	input ClaimToolCallBatchInput,
) (ToolCallBatch, error) {
	input.BatchID = strings.TrimSpace(input.BatchID)
	if s == nil || s.db == nil || ctx == nil || input.BatchID == "" || input.ExpectedStateVersion <= 0 {
		return ToolCallBatch{}, errors.New("live claim, batch id, and expected state version are required")
	}
	repository, err := s.TranscriptRepository(ctx)
	if err != nil {
		return ToolCallBatch{}, err
	}
	var claimed ToolCallBatch
	err = repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		if _, err := tx.ValidateLiveRunnerClaim(ctx, input.Claim); err != nil {
			return err
		}
		batch, found, err := getToolCallBatchQuery(ctx, tx, input.BatchID)
		if err != nil {
			return err
		}
		if !found || validateToolCallBatchAuthority(ctx, tx, batch, input.Claim) != nil {
			return ErrToolCallBatchConflict
		}
		if batch.StateVersion != input.ExpectedStateVersion || toolCallBatchTerminal(batch.State) {
			return ErrToolCallBatchStale
		}
		now := s.now().UTC()
		updated, err := tx.ExecContext(ctx, `UPDATE transcript_tool_call_batches SET
			runner_id=?,runner_attempt=?,runner_claim_sha256=?,state_version=state_version+1,updated_at=?
			WHERE batch_id=? AND state_version=? AND state IN ('ready','running','waiting')`,
			input.Claim.RunnerID, input.Claim.Attempt, toolCallBatchClaimSHA256(input.Claim.ClaimToken),
			now.Format(time.RFC3339Nano), input.BatchID, input.ExpectedStateVersion)
		if err != nil {
			return err
		}
		if rows, err := updated.RowsAffected(); err != nil || rows != 1 {
			return ErrToolCallBatchStale
		}
		claimed, found, err = getToolCallBatchQuery(ctx, tx, input.BatchID)
		if err != nil {
			return err
		}
		if !found {
			return ErrToolCallBatchConflict
		}
		return nil
	})
	return claimed, err
}

func (s *Store) StartToolCallBatchItemTx(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	input StartToolCallBatchItemInput,
) (ToolCallBatch, ToolCallBatchItem, error) {
	batch, item, err := loadToolCallBatchMutation(ctx, tx, input.Claim, input.BatchID, input.Ordinal,
		input.ExpectedBatchStateVersion, input.ExpectedItemStateVersion)
	if err != nil {
		return ToolCallBatch{}, ToolCallBatchItem{}, err
	}
	// Idempotent: if the item has already advanced past pending (e.g. running
	// or terminal), the start checkpoint was already recorded. Return the
	// current state without error so the runner can continue. This mirrors
	// The reference batch model where re-processing an already-started
	// item is a no-op rather than a conflict.
	if item.State != ToolCallBatchItemStatePending {
		if toolCallBatchItemTerminal(item.State) || item.State == ToolCallBatchItemStateRunning || item.State == ToolCallBatchItemStateWaiting {
			return reloadToolCallBatchMutation(ctx, tx, batch.BatchID, item.Ordinal)
		}
		return ToolCallBatch{}, ToolCallBatchItem{}, ErrToolCallBatchStale
	}
	if batch.State != ToolCallBatchStateReady {
		return ToolCallBatch{}, ToolCallBatchItem{}, ErrToolCallBatchStale
	}
	eventTime, _, _, err := validateToolCallBatchCheckpointEvent(ctx, tx, batch, item, input.Claim, input.StartedEvent, "start", false)
	if err != nil {
		return ToolCallBatch{}, ToolCallBatchItem{}, err
	}
	if err := updateToolCallBatchItemState(ctx, tx, batch, item, ToolCallBatchItemStateRunning,
		`started_event_id=?`, []any{input.StartedEvent.EventID}, eventTime); err != nil {
		return ToolCallBatch{}, ToolCallBatchItem{}, err
	}
	if err := updateToolCallBatchHead(ctx, tx, batch, ToolCallBatchStateRunning, batch.NextOrdinal, nil, eventTime); err != nil {
		return ToolCallBatch{}, ToolCallBatchItem{}, err
	}
	return reloadToolCallBatchMutation(ctx, tx, batch.BatchID, item.Ordinal)
}

func (s *Store) WaitToolCallBatchItemTx(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	input WaitToolCallBatchItemInput,
) (ToolCallBatch, ToolCallBatchItem, error) {
	batch, item, err := loadToolCallBatchMutation(ctx, tx, input.Claim, input.BatchID, input.Ordinal,
		input.ExpectedBatchStateVersion, input.ExpectedItemStateVersion)
	if err != nil {
		return ToolCallBatch{}, ToolCallBatchItem{}, err
	}
	// Idempotent: if the item has already advanced past running (e.g. waiting
	// or terminal), the waiting checkpoint was already recorded.
	if item.State != ToolCallBatchItemStateRunning {
		if toolCallBatchItemTerminal(item.State) || item.State == ToolCallBatchItemStateWaiting {
			return reloadToolCallBatchMutation(ctx, tx, batch.BatchID, item.Ordinal)
		}
		return ToolCallBatch{}, ToolCallBatchItem{}, ErrToolCallBatchStale
	}
	if batch.State != ToolCallBatchStateRunning && batch.State != ToolCallBatchStateWaiting {
		return ToolCallBatch{}, ToolCallBatchItem{}, ErrToolCallBatchStale
	}
	eventTime, _, _, err := validateToolCallBatchCheckpointEvent(ctx, tx, batch, item, input.Claim, input.WaitingEvent, "waiting", false)
	if err != nil {
		return ToolCallBatch{}, ToolCallBatchItem{}, err
	}
	if err := updateToolCallBatchItemState(ctx, tx, batch, item, ToolCallBatchItemStateWaiting,
		`waiting_event_id=?`, []any{input.WaitingEvent.EventID}, eventTime); err != nil {
		return ToolCallBatch{}, ToolCallBatchItem{}, err
	}
	waiting := item.Ordinal
	if err := updateToolCallBatchHead(ctx, tx, batch, ToolCallBatchStateWaiting, batch.NextOrdinal, &waiting, eventTime); err != nil {
		return ToolCallBatch{}, ToolCallBatchItem{}, err
	}
	return reloadToolCallBatchMutation(ctx, tx, batch.BatchID, item.Ordinal)
}

// FinishToolCallBatchItemTx commits the terminal protocol receipt and advances
// the batch cursor in one transaction. It stores only the terminal event id,
// canonical result digest, and optional immutable result reference.
func (s *Store) FinishToolCallBatchItemTx(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	input FinishToolCallBatchItemInput,
) (ToolCallBatch, ToolCallBatchItem, error) {
	input.TerminalState = strings.TrimSpace(input.TerminalState)
	input.TerminalPhase = strings.TrimSpace(input.TerminalPhase)
	if input.TerminalPhase == "" {
		input.TerminalPhase = input.TerminalState
	}
	input.ResultRef = strings.TrimSpace(input.ResultRef)
	if !toolCallBatchItemTerminal(input.TerminalState) || len(input.ResultRef) > maxToolResultReferenceBytes {
		return ToolCallBatch{}, ToolCallBatchItem{}, errors.New("terminal item state and bounded result reference are required")
	}
	batch, item, err := loadToolCallBatchMutation(ctx, tx, input.Claim, input.BatchID, input.Ordinal,
		input.ExpectedBatchStateVersion, input.ExpectedItemStateVersion)
	if err != nil {
		return ToolCallBatch{}, ToolCallBatchItem{}, err
	}
	// Idempotent: if the item is already terminal, the finish checkpoint was
	// already recorded. Return the current state without error.
	if toolCallBatchItemTerminal(item.State) {
		return reloadToolCallBatchMutation(ctx, tx, batch.BatchID, item.Ordinal)
	}
	prestartRejection := input.RejectedBeforeExecution &&
		batch.State == ToolCallBatchStateReady && item.State == ToolCallBatchItemStatePending
	if !prestartRejection && ((batch.State != ToolCallBatchStateRunning && batch.State != ToolCallBatchStateWaiting) ||
		(item.State != ToolCallBatchItemStateRunning && item.State != ToolCallBatchItemStateWaiting)) {
		return ToolCallBatch{}, ToolCallBatchItem{}, ErrToolCallBatchStale
	}
	eventTime, resultSHA, eventResultRef, err := validateToolCallBatchCheckpointEvent(
		ctx, tx, batch, item, input.Claim, input.TerminalEvent, input.TerminalPhase, true,
	)
	if err != nil {
		return ToolCallBatch{}, ToolCallBatchItem{}, err
	}
	if input.ResultRef != "" && input.ResultRef != eventResultRef {
		return ToolCallBatch{}, ToolCallBatchItem{}, ErrToolCallBatchConflict
	}
	input.ResultRef = eventResultRef
	if err := updateToolCallBatchItemState(ctx, tx, batch, item, input.TerminalState,
		`terminal_event_id=?,terminal_result_sha256=?,result_ref=?`,
		[]any{input.TerminalEvent.EventID, resultSHA, nullableToolCallBatchString(input.ResultRef)}, eventTime); err != nil {
		return ToolCallBatch{}, ToolCallBatchItem{}, err
	}
	nextOrdinal := item.Ordinal + 1
	nextState := ToolCallBatchStateReady
	if nextOrdinal == batch.CallCount {
		nextState = ToolCallBatchStateSettled
	}
	if err := updateToolCallBatchHead(ctx, tx, batch, nextState, nextOrdinal, nil, eventTime); err != nil {
		return ToolCallBatch{}, ToolCallBatchItem{}, err
	}
	return reloadToolCallBatchMutation(ctx, tx, batch.BatchID, item.Ordinal)
}

// ReconcileToolCallBatchFromKernelProtocolReceipt advances a kernel batch
// item when its terminal kernel protocol receipt was committed in a previous
// checkpoint transaction but the batch mutation conflicted. This is
// intentionally idempotent and never executes the kernel again.
func (s *Store) ReconcileToolCallBatchFromKernelProtocolReceipt(
	ctx context.Context,
	input ReconcileKernelToolCallBatchInput,
) (bool, error) {
	input.BatchID = strings.TrimSpace(input.BatchID)
	input.OperationID = strings.TrimSpace(input.OperationID)
	if s == nil || s.db == nil || ctx == nil || input.BatchID == "" || input.Ordinal < 0 || input.OperationID == "" {
		return false, errors.New("live claim, batch, ordinal, and kernel operation are required")
	}
	repository, err := s.TranscriptRepository(ctx)
	if err != nil {
		return false, err
	}
	reconciled := false
	err = repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		if _, err := tx.ValidateLiveRunnerClaim(ctx, input.Claim); err != nil {
			return err
		}
		batch, found, err := getToolCallBatchQuery(ctx, tx, input.BatchID)
		if err != nil {
			return err
		}
		if !found || validateToolCallBatchAuthority(ctx, tx, batch, input.Claim) != nil ||
			batch.RunnerID != input.Claim.RunnerID || batch.RunnerAttempt != input.Claim.Attempt ||
			batch.RunnerClaimSHA256 != toolCallBatchClaimSHA256(input.Claim.ClaimToken) {
			return fmt.Errorf("kernel batch reconciliation claim authority mismatch: %w", ErrToolCallBatchConflict)
		}
		item, found, err := getToolCallBatchItemQuery(ctx, tx, input.BatchID, input.Ordinal)
		if err != nil {
			return err
		}
		if !found || item.ToolCallID == "" || item.BatchID != batch.BatchID || item.StreamUID != batch.StreamUID {
			return ErrToolCallBatchConflict
		}
		if toolCallBatchItemTerminal(item.State) {
			reconciled = true
			return nil
		}
		if batch.NextOrdinal != input.Ordinal ||
			(batch.State != ToolCallBatchStateRunning && batch.State != ToolCallBatchStateWaiting) ||
			(item.State != ToolCallBatchItemStateRunning && item.State != ToolCallBatchItemStateWaiting) {
			return ErrToolCallBatchStale
		}
		operation, found, err := getKernelLocalOperationQuery(ctx, tx, input.OperationID)
		if err != nil {
			return err
		}
		if !found || !kernelLocalOperationTerminal(operation.State) ||
			operation.OwnerUserID != input.Claim.OwnerID || operation.StreamUID != batch.StreamUID ||
			operation.ToolCallID != item.ToolCallID || operation.Tool != item.ToolName ||
			operation.AdmittedInputRevision <= 0 || operation.AdmittedInputRevision > input.Claim.ClaimedInputRevision {
			return fmt.Errorf("kernel batch reconciliation operation identity mismatch: %w", ErrToolCallBatchConflict)
		}
		if err := validateKernelLocalOperationCurrentAuthority(ctx, tx, operation, true); err != nil {
			return fmt.Errorf("kernel batch reconciliation operation authority: %w", err)
		}
		var receiptStream, receiptToolCall, receiptResultSHA string
		var receiptResultRef sql.NullString
		var receiptAttempt, receiptEvent int64
		err = tx.QueryRowContext(ctx, `SELECT stream_uid,runner_attempt,event_id,tool_call_id,result_sha256,result_ref
			FROM kernel_local_operation_protocol_receipts WHERE operation_id=?`, input.OperationID).Scan(
			&receiptStream, &receiptAttempt, &receiptEvent, &receiptToolCall, &receiptResultSHA, &receiptResultRef,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if receiptStream != batch.StreamUID || receiptAttempt <= 0 || receiptEvent <= 0 ||
			receiptToolCall != item.ToolCallID || receiptResultSHA == "" {
			return fmt.Errorf("kernel batch reconciliation protocol receipt mismatch: %w", ErrToolCallBatchConflict)
		}
		var eventType, eventSource string
		var eventAttempt int64
		var payloadJSON []byte
		if err := tx.QueryRowContext(ctx, `SELECT event_type,source,runner_attempt,payload_json
			FROM transcript_events WHERE stream_uid=? AND event_id=?`, receiptStream, receiptEvent).Scan(
			&eventType, &eventSource, &eventAttempt, &payloadJSON,
		); err != nil {
			return err
		}
		var payload struct {
			Status     string `json:"status"`
			ToolCallID string `json:"toolCallId"`
			ToolPhase  string `json:"toolPhase"`
		}
		if eventType != "runner_checkpoint" || eventAttempt != receiptAttempt ||
			json.Unmarshal(payloadJSON, &payload) != nil || payload.ToolCallID != item.ToolCallID ||
			(payload.Status != ToolCallBatchItemStateCompleted && payload.Status != ToolCallBatchItemStateFailed &&
				payload.Status != ToolCallBatchItemStateCancelled && payload.Status != ToolCallBatchItemStateOutcomeUnknown) ||
			payload.ToolPhase != payload.Status {
			return fmt.Errorf("kernel batch reconciliation terminal checkpoint mismatch: %w", ErrToolCallBatchConflict)
		}
		event := transcriptstore.Event{
			StreamUID: receiptStream, EventID: receiptEvent, Type: eventType,
			Source: transcriptstore.EventSource(eventSource), RunnerAttempt: &eventAttempt,
			PayloadJSON: payloadJSON,
		}
		err = s.reconcileKernelProtocolReceiptTx(ctx, tx, batch, item, operation,
			receiptResultSHA, receiptResultRef.String, event, payload.Status)
		if err != nil {
			return fmt.Errorf("kernel batch reconciliation terminal projection: %w", err)
		}
		reconciled = true
		return nil
	})
	return reconciled, err
}

func (s *Store) reconcileKernelProtocolReceiptTx(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	batch ToolCallBatch,
	item ToolCallBatchItem,
	operation KernelLocalOperation,
	receiptResultSHA, receiptResultRef string,
	event transcriptstore.Event,
	terminalState string,
) error {
	if tx == nil || event.EventID <= 0 || event.RunnerAttempt == nil ||
		!toolCallBatchItemTerminal(terminalState) || event.StreamUID != batch.StreamUID ||
		operation.OperationID == "" || operation.ToolCallID != item.ToolCallID || operation.Tool != item.ToolName {
		return fmt.Errorf("kernel receipt projection identity mismatch: %w", ErrToolCallBatchConflict)
	}
	var membership int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcript_branch_events
		WHERE stream_uid=? AND branch_id=? AND event_id=?`, batch.StreamUID, batch.BranchID, event.EventID).Scan(&membership); err != nil {
		return err
	}
	if membership != 1 {
		return fmt.Errorf("kernel receipt terminal event is outside the active batch branch: %w", ErrToolCallBatchConflict)
	}
	var payload struct {
		Status     string          `json:"status"`
		ToolCallID string          `json:"toolCallId"`
		ToolPhase  string          `json:"toolPhase"`
		ToolResult json.RawMessage `json:"toolResult"`
	}
	if json.Unmarshal(event.PayloadJSON, &payload) != nil || payload.Status != terminalState ||
		payload.ToolCallID != item.ToolCallID || payload.ToolPhase != terminalState || len(payload.ToolResult) == 0 {
		return fmt.Errorf("kernel receipt terminal payload mismatch: %w", ErrToolCallBatchConflict)
	}
	canonicalResult, err := canonicalToolCallBatchJSON(payload.ToolResult, false)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(canonicalResult)
	if receiptResultSHA != hex.EncodeToString(digest[:]) {
		return fmt.Errorf("kernel receipt result digest mismatch: %w", ErrToolCallBatchConflict)
	}
	materialization, found, err := getKernelToolResultMaterializationQuery(ctx, tx, operation.OperationID)
	if err != nil {
		return err
	}
	if !found || !bytes.Equal(canonicalResult, materialization.TerminalResultJSON) ||
		receiptResultRef != materialization.ResultRef {
		return fmt.Errorf("kernel receipt materialization mismatch: %w", ErrToolCallBatchConflict)
	}
	descriptor, _, externalized, err := toolcontract.DecodeExternalizedResult(canonicalResult)
	if err != nil {
		return err
	}
	if externalized {
		if !IsRunnerLargeToolResultArtifactID(descriptor.ArtifactID) {
			return fmt.Errorf("kernel receipt externalized result is not immutable runner evidence: %w", ErrToolCallBatchConflict)
		}
		var runnerID, claimToken, toolName, toolCallID, contentType, versionID, contentSHA string
		var attempt, sourceEventID, sizeBytes int64
		if err := tx.QueryRowContext(ctx, `SELECT runner_id,claim_token,attempt,source_event_id,
			tool_name,tool_call_id,content_type,version_id,size_bytes,content_sha256
			FROM runner_large_tool_results WHERE artifact_id=?`, descriptor.ArtifactID).Scan(
			&runnerID, &claimToken, &attempt, &sourceEventID, &toolName, &toolCallID,
			&contentType, &versionID, &sizeBytes, &contentSHA,
		); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrToolCallBatchConflict
			}
			return err
		}
		if err := validateDetachedKernelLargeToolResultReceipt(ctx, tx, batch, item,
			runnerID, claimToken, attempt, sourceEventID, toolCallID, toolName,
			contentType, versionID, sizeBytes, contentSHA); err != nil {
			return fmt.Errorf("kernel receipt detached large-result evidence: %w", err)
		}
	}
	now := s.now().UTC()
	if err := updateToolCallBatchItemState(ctx, tx, batch, item, terminalState,
		`terminal_event_id=?,terminal_result_sha256=?,result_ref=?`,
		[]any{event.EventID, receiptResultSHA, nullableToolCallBatchString(receiptResultRef)}, now); err != nil {
		return err
	}
	nextOrdinal := item.Ordinal + 1
	nextState := ToolCallBatchStateReady
	if nextOrdinal == batch.CallCount {
		nextState = ToolCallBatchStateSettled
	}
	return updateToolCallBatchHead(ctx, tx, batch, nextState, nextOrdinal, nil, now)
}

func loadToolCallBatchMutation(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	claim transcriptstore.RunnerClaim,
	batchID string,
	ordinal, expectedBatchVersion, expectedItemVersion int64,
) (ToolCallBatch, ToolCallBatchItem, error) {
	batchID = strings.TrimSpace(batchID)
	if ctx == nil || tx == nil || batchID == "" || ordinal < 0 || expectedBatchVersion <= 0 || expectedItemVersion <= 0 {
		return ToolCallBatch{}, ToolCallBatchItem{}, errors.New("complete tool call batch mutation authority is required")
	}
	if _, err := tx.ValidateLiveRunnerClaim(ctx, claim); err != nil {
		return ToolCallBatch{}, ToolCallBatchItem{}, err
	}
	batch, found, err := getToolCallBatchQuery(ctx, tx, batchID)
	if err != nil {
		return ToolCallBatch{}, ToolCallBatchItem{}, err
	}
	if !found || validateToolCallBatchAuthority(ctx, tx, batch, claim) != nil ||
		batch.RunnerID != claim.RunnerID || batch.RunnerAttempt != claim.Attempt ||
		batch.RunnerClaimSHA256 != toolCallBatchClaimSHA256(claim.ClaimToken) {
		return ToolCallBatch{}, ToolCallBatchItem{}, ErrToolCallBatchConflict
	}
	item, found, err := getToolCallBatchItemQuery(ctx, tx, batchID, ordinal)
	if err != nil {
		return ToolCallBatch{}, ToolCallBatchItem{}, err
	}
	if !found || item.BatchID != batch.BatchID || item.StreamUID != batch.StreamUID ||
		batch.NextOrdinal != ordinal || batch.StateVersion != expectedBatchVersion || item.StateVersion != expectedItemVersion {
		return ToolCallBatch{}, ToolCallBatchItem{}, ErrToolCallBatchStale
	}
	return batch, item, nil
}

func reloadToolCallBatchMutation(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	batchID string,
	ordinal int64,
) (ToolCallBatch, ToolCallBatchItem, error) {
	batch, found, err := getToolCallBatchQuery(ctx, tx, batchID)
	if err != nil {
		return ToolCallBatch{}, ToolCallBatchItem{}, err
	}
	if !found {
		return ToolCallBatch{}, ToolCallBatchItem{}, ErrToolCallBatchConflict
	}
	item, found, err := getToolCallBatchItemQuery(ctx, tx, batchID, ordinal)
	if err != nil {
		return ToolCallBatch{}, ToolCallBatchItem{}, err
	}
	if !found {
		return ToolCallBatch{}, ToolCallBatchItem{}, ErrToolCallBatchConflict
	}
	return batch, item, nil
}

func updateToolCallBatchItemState(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	batch ToolCallBatch,
	item ToolCallBatchItem,
	state, assignment string,
	arguments []any,
	updatedAt time.Time,
) error {
	query := `UPDATE transcript_tool_call_items SET state=?,state_version=state_version+1,` + assignment + `,updated_at=?
		WHERE batch_id=? AND ordinal=? AND state_version=?`
	values := []any{state}
	values = append(values, arguments...)
	values = append(values, updatedAt.UTC().Format(time.RFC3339Nano), batch.BatchID, item.Ordinal, item.StateVersion)
	result, err := tx.ExecContext(ctx, query, values...)
	if err != nil {
		return err
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		return ErrToolCallBatchStale
	}
	return nil
}

func updateToolCallBatchHead(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	batch ToolCallBatch,
	state string,
	nextOrdinal int64,
	waitingOrdinal *int64,
	updatedAt time.Time,
) error {
	result, err := tx.ExecContext(ctx, `UPDATE transcript_tool_call_batches SET
		state=?,state_version=state_version+1,next_ordinal=?,waiting_ordinal=?,updated_at=?
		WHERE batch_id=? AND state_version=?`, state, nextOrdinal, nullableToolCallBatchInt64(waitingOrdinal),
		updatedAt.UTC().Format(time.RFC3339Nano), batch.BatchID, batch.StateVersion)
	if err != nil {
		return err
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		return ErrToolCallBatchStale
	}
	return nil
}

func validateToolCallBatchCheckpointEvent(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	batch ToolCallBatch,
	item ToolCallBatchItem,
	claim transcriptstore.RunnerClaim,
	event transcriptstore.Event,
	expectedPhase string,
	requireResult bool,
) (time.Time, string, string, error) {
	if event.StreamUID != batch.StreamUID || event.EventID <= 0 || event.RunnerAttempt == nil || *event.RunnerAttempt != claim.Attempt {
		return time.Time{}, "", "", ErrToolCallBatchConflict
	}
	var eventType, source string
	var runnerAttempt int64
	var payloadJSON []byte
	var createdAt time.Time
	if err := tx.QueryRowContext(ctx, `SELECT event_type,source,runner_attempt,payload_json,created_at
		FROM transcript_events WHERE stream_uid=? AND event_id=?`, event.StreamUID, event.EventID).Scan(
		&eventType, &source, &runnerAttempt, &payloadJSON, &createdAt,
	); err != nil {
		return time.Time{}, "", "", err
	}
	if eventType != "runner_checkpoint" || source != string(transcriptstore.EventSourcePayload) ||
		runnerAttempt != claim.Attempt || !bytes.Equal(payloadJSON, event.PayloadJSON) {
		return time.Time{}, "", "", ErrToolCallBatchConflict
	}
	var membership int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcript_branch_events
		WHERE stream_uid=? AND branch_id=? AND event_id=?`, batch.StreamUID, batch.BranchID, event.EventID).Scan(&membership); err != nil {
		return time.Time{}, "", "", err
	}
	if membership != 1 || validateToolCallBatchJSON(payloadJSON) != nil {
		return time.Time{}, "", "", ErrToolCallBatchConflict
	}
	var payload struct {
		ToolCallID string          `json:"toolCallId"`
		ToolPhase  string          `json:"toolPhase"`
		ToolResult json.RawMessage `json:"toolResult"`
	}
	if err := json.Unmarshal(payloadJSON, &payload); err != nil || strings.TrimSpace(payload.ToolCallID) != item.ToolCallID ||
		strings.TrimSpace(payload.ToolPhase) != expectedPhase {
		return time.Time{}, "", "", ErrToolCallBatchConflict
	}
	if !requireResult {
		return createdAt.UTC(), "", "", nil
	}
	canonicalResult, err := canonicalToolCallBatchJSON(payload.ToolResult, false)
	if err != nil {
		return time.Time{}, "", "", ErrToolCallBatchConflict
	}
	descriptor, resultRef, externalized, err := toolcontract.DecodeExternalizedResult(canonicalResult)
	if err != nil {
		return time.Time{}, "", "", ErrToolCallBatchConflict
	}
	if externalized {
		if err := validateToolCallBatchArtifactReceipt(ctx, tx, batch, item, claim, descriptor); err != nil {
			return time.Time{}, "", "", err
		}
	}
	digest := sha256.Sum256(canonicalResult)
	return createdAt.UTC(), hex.EncodeToString(digest[:]), resultRef, nil
}

func validateToolCallBatchArtifactReceipt(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	batch ToolCallBatch,
	item ToolCallBatchItem,
	claim transcriptstore.RunnerClaim,
	descriptor toolcontract.ExternalizedResultDescriptor,
) error {
	if IsRunnerLargeToolResultArtifactID(descriptor.ArtifactID) {
		return validateRunnerLargeToolResultReceipt(ctx, tx, batch, item, claim, descriptor)
	}
	if item.StartedEventID <= 0 || claim.Attempt <= 0 || batch.RunnerAttempt != claim.Attempt {
		return ErrToolCallBatchConflict
	}
	var (
		streamOwner, streamProject            string
		artifactProject, artifactKind         string
		versionArtifact, versionSHA, relation string
		versionSize                           int64
		sourcePayload                         []byte
	)
	if err := tx.QueryRowContext(ctx, `
		SELECT stream.owner_id,stream.project_id,
			artifact.project_id,artifact.kind,
			version.artifact_id,version.content_sha256,version.size_bytes,
			commit_row.relation,source.payload_json
		FROM transcript_artifact_commits commit_row
		JOIN transcript_streams stream ON stream.stream_uid=commit_row.stream_uid
		JOIN transcript_events source
			ON source.stream_uid=commit_row.stream_uid
			AND source.runner_attempt=commit_row.runner_attempt
			AND source.event_id=commit_row.source_event_id
		JOIN artifacts artifact ON artifact.id=commit_row.artifact_id
		JOIN artifact_versions version
			ON version.id=commit_row.version_id AND version.artifact_id=commit_row.artifact_id
		WHERE commit_row.stream_uid=? AND commit_row.runner_attempt=?
			AND commit_row.source_event_id=? AND commit_row.artifact_id=? AND commit_row.version_id=?`,
		batch.StreamUID, claim.Attempt, item.StartedEventID,
		descriptor.ArtifactID, descriptor.VersionID,
	).Scan(
		&streamOwner, &streamProject,
		&artifactProject, &artifactKind,
		&versionArtifact, &versionSHA, &versionSize,
		&relation, &sourcePayload,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrToolCallBatchConflict
		}
		return err
	}
	var source struct {
		ToolCallID string `json:"toolCallId"`
		ToolName   string `json:"toolName"`
		ToolPhase  string `json:"toolPhase"`
	}
	if streamOwner != batch.OwnerUserID || streamProject == "" || artifactProject != streamProject ||
		artifactKind != descriptor.ContentType || versionArtifact != descriptor.ArtifactID ||
		versionSHA != descriptor.SHA256 || versionSize != descriptor.SizeBytes || relation != "produced" ||
		json.Unmarshal(sourcePayload, &source) != nil || source.ToolCallID != item.ToolCallID ||
		source.ToolName != item.ToolName || source.ToolPhase != "start" {
		return ErrToolCallBatchConflict
	}
	return nil
}

// validateRunnerLargeToolResultReceipt verifies that an externalized tool
// result is durably recorded as runner internal evidence. Runner large tool
// results deliberately live outside the artifact/project-file store, so their
// receipt is validated against the immutable evidence table instead of
// transcript_artifact_commits/artifacts.
func validateRunnerLargeToolResultReceipt(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	batch ToolCallBatch,
	item ToolCallBatchItem,
	claim transcriptstore.RunnerClaim,
	descriptor toolcontract.ExternalizedResultDescriptor,
) error {
	if item.StartedEventID <= 0 || claim.Attempt <= 0 || batch.RunnerAttempt != claim.Attempt {
		return ErrToolCallBatchConflict
	}
	var (
		versionID, projectID, rootFrameID, frameID   string
		streamUID, ownerUserID, runnerID, claimToken string
		attempt, sourceEventID                       int64
		toolName, toolCallID, contentType, sha       string
		sizeBytes                                    int64
	)
	if err := tx.QueryRowContext(ctx, `
		SELECT version_id,project_id,root_frame_id,frame_id,stream_uid,owner_user_id,
			runner_id,claim_token,attempt,source_event_id,tool_name,tool_call_id,
			content_type,size_bytes,content_sha256
		FROM runner_large_tool_results
		WHERE artifact_id=?`, descriptor.ArtifactID,
	).Scan(
		&versionID, &projectID, &rootFrameID, &frameID,
		&streamUID, &ownerUserID, &runnerID, &claimToken,
		&attempt, &sourceEventID, &toolName, &toolCallID,
		&contentType, &sizeBytes, &sha,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrToolCallBatchConflict
		}
		return err
	}
	if streamUID == batch.StreamUID && ownerUserID == batch.OwnerUserID &&
		projectID != "" && rootFrameID != "" && frameID != "" &&
		runnerID == claim.RunnerID && claimToken == claim.ClaimToken && attempt == claim.Attempt &&
		sourceEventID == item.StartedEventID && toolCallID == item.ToolCallID && toolName == item.ToolName &&
		contentType == descriptor.ContentType && versionID == descriptor.VersionID &&
		sizeBytes == descriptor.SizeBytes && sha == descriptor.SHA256 {
		return nil
	}
	return validateDetachedKernelLargeToolResultReceipt(ctx, tx, batch, item,
		runnerID, claimToken, attempt, sourceEventID, toolCallID, toolName,
		contentType, versionID, sizeBytes, sha)
}

// validateDetachedKernelLargeToolResultReceipt accepts the internal evidence
// written by detached kernel recovery. It is bound to the immutable terminal
// kernel operation and its original model-tool source checkpoint; it never
// turns the recovery token into a live runner claim.
func validateDetachedKernelLargeToolResultReceipt(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	batch ToolCallBatch,
	item ToolCallBatchItem,
	runnerID, claimToken string,
	attempt, sourceEventID int64,
	toolCallID, toolName, contentType, versionID string,
	sizeBytes int64,
	sha string,
) error {
	if !strings.HasPrefix(claimToken, detachedKernelRecoveryClaimTokenPrefix) ||
		strings.TrimPrefix(claimToken, detachedKernelRecoveryClaimTokenPrefix) == "" ||
		toolCallID != item.ToolCallID || toolName != item.ToolName || contentType != "application/json" ||
		versionID == "" || sizeBytes <= 0 || sha == "" || attempt <= 0 || sourceEventID <= 0 {
		return ErrToolCallBatchConflict
	}
	operationID := strings.TrimPrefix(claimToken, detachedKernelRecoveryClaimTokenPrefix)
	operation, found, err := getKernelLocalOperationQuery(ctx, tx, operationID)
	if err != nil {
		return err
	}
	if !found || !kernelLocalOperationTerminal(operation.State) ||
		operation.OwnerUserID != batch.OwnerUserID || operation.StreamUID != batch.StreamUID ||
		operation.ProjectID == "" || operation.RootFrameID == "" || operation.FrameID == "" ||
		operation.RunnerID != runnerID || operation.SourceRunnerAttempt != attempt ||
		operation.SourceEventID != sourceEventID || operation.ToolCallID != toolCallID ||
		operation.Tool != toolName || operation.AdmittedInputRevision <= 0 {
		return ErrToolCallBatchConflict
	}
	if err := validateKernelLocalOperationCurrentAuthority(ctx, tx, operation, true); err != nil {
		return err
	}
	var eventType string
	var eventAttempt int64
	var payloadJSON []byte
	if err := tx.QueryRowContext(ctx, `SELECT event_type,runner_attempt,payload_json
		FROM transcript_events WHERE stream_uid=? AND event_id=?`, operation.StreamUID, operation.SourceEventID).Scan(
		&eventType, &eventAttempt, &payloadJSON,
	); err != nil {
		return err
	}
	if eventType != "runner_checkpoint" || eventAttempt != operation.SourceRunnerAttempt ||
		!durableKernelSourcePayloadMatches(payloadJSON, operation.ToolCallID, operation.Tool) {
		return ErrToolCallBatchConflict
	}
	return nil
}

func durableKernelSourcePayloadMatches(payloadJSON []byte, toolCallID, toolName string) bool {
	var direct struct {
		ToolCallID string `json:"toolCallId"`
		ToolName   string `json:"toolName"`
	}
	if json.Unmarshal(payloadJSON, &direct) == nil && direct.ToolCallID == toolCallID && direct.ToolName == toolName {
		return true
	}
	var batch struct {
		ModelToolCalls []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"modelToolCalls"`
	}
	if json.Unmarshal(payloadJSON, &batch) != nil {
		return false
	}
	for _, call := range batch.ModelToolCalls {
		if call.ID == toolCallID && call.Name == toolName {
			return true
		}
	}
	return false
}

func toolCallBatchImmutableResultRef(raw []byte) (string, error) {
	_, resultRef, _, err := toolcontract.DecodeExternalizedResult(raw)
	return resultRef, err
}

func validateToolCallBatchAuthority(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	batch ToolCallBatch,
	claim transcriptstore.RunnerClaim,
) error {
	if batch.OwnerUserID != claim.OwnerID || batch.StreamUID != claim.StreamUID ||
		batch.AdmittedInputRevision <= 0 || batch.AdmittedInputRevision > claim.ClaimedInputRevision {
		return ErrToolCallBatchConflict
	}
	var matches int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM transcript_streams stream
		JOIN transcript_branch_state branch ON branch.stream_uid=stream.stream_uid
			AND branch.active_branch_id=? AND branch.generation=?
		JOIN transcript_branch_events source ON source.stream_uid=stream.stream_uid
			AND source.branch_id=? AND source.event_id=?
		WHERE stream.stream_uid=? AND stream.owner_id=?`, batch.BranchID, batch.BranchGeneration,
		batch.BranchID, batch.SourceEventID, batch.StreamUID, batch.OwnerUserID).Scan(&matches); err != nil {
		return err
	}
	if matches != 1 {
		return ErrToolCallBatchConflict
	}
	return nil
}

func canonicalToolCallBatchItems(payload []byte) ([]canonicalToolCallBatchItem, error) {
	if len(payload) == 0 || !utf8.Valid(payload) || validateToolCallBatchJSON(payload) != nil {
		return nil, errors.New("canonical model tool-call checkpoint is invalid")
	}
	var checkpoint struct {
		ModelToolCalls []struct {
			ID        string          `json:"id"`
			Type      string          `json:"type"`
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"modelToolCalls"`
	}
	if err := json.Unmarshal(payload, &checkpoint); err != nil || len(checkpoint.ModelToolCalls) == 0 {
		return nil, errors.New("canonical model tool-call checkpoint is invalid")
	}
	seen := make(map[string]struct{}, len(checkpoint.ModelToolCalls))
	result := make([]canonicalToolCallBatchItem, 0, len(checkpoint.ModelToolCalls))
	for _, call := range checkpoint.ModelToolCalls {
		id, name := strings.TrimSpace(call.ID), strings.TrimSpace(call.Name)
		if id == "" || id != call.ID || len(id) > maxToolCallIdentityBytes || name == "" || name != call.Name ||
			len(name) > maxToolNameBytes || call.Type != "function" {
			return nil, errors.New("canonical model tool-call identity is invalid")
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, errors.New("canonical model tool-call identity is duplicated")
		}
		seen[id] = struct{}{}
		arguments, err := canonicalToolCallBatchJSON(call.Arguments, true)
		if err != nil || len(arguments) > maxToolArgumentsBytes {
			return nil, errors.New("canonical model tool-call arguments are invalid")
		}
		digest := sha256.Sum256(arguments)
		result = append(result, canonicalToolCallBatchItem{
			ID: id, Name: name, ArgumentsJSON: arguments, ArgumentsSHA256: hex.EncodeToString(digest[:]),
		})
	}
	return result, nil
}

func canonicalToolCallBatchJSON(raw []byte, requireObject bool) ([]byte, error) {
	if len(raw) == 0 || len(raw) > maxToolArgumentsBytes || !utf8.Valid(raw) || validateToolCallBatchJSON(raw) != nil {
		return nil, errors.New("tool call batch JSON is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil || decoder.Decode(&struct{}{}) == nil {
		return nil, errors.New("tool call batch JSON is invalid")
	}
	if requireObject {
		if _, ok := value.(map[string]any); !ok {
			return nil, errors.New("tool call arguments must be an object")
		}
	}
	canonical, err := json.Marshal(value)
	if err != nil || len(canonical) > maxToolArgumentsBytes {
		return nil, errors.New("tool call batch JSON exceeds the durable limit")
	}
	return canonical, nil
}

func validateToolCallBatchJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := validateToolCallBatchJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("tool call batch JSON contains trailing data")
	}
	return nil
}

func validateToolCallBatchJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("tool call batch object key is invalid")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("tool call batch JSON contains a duplicate key")
			}
			seen[key] = struct{}{}
			if err := validateToolCallBatchJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("tool call batch object is invalid")
		}
	case '[':
		for decoder.More() {
			if err := validateToolCallBatchJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("tool call batch array is invalid")
		}
	default:
		return errors.New("tool call batch JSON delimiter is invalid")
	}
	return nil
}

func toolCallBatchID(streamUID string, eventID int64) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		toolCallBatchIdentityDomain, strings.TrimSpace(streamUID), strconv.FormatInt(eventID, 10),
	}, "\x00")))
	return "tcb_" + hex.EncodeToString(digest[:])
}

func toolCallBatchClaimSHA256(claimToken string) string {
	digest := sha256.Sum256([]byte(claimToken))
	return hex.EncodeToString(digest[:])
}

func toolCallBatchTerminal(state string) bool {
	return state == ToolCallBatchStateSettled || state == ToolCallBatchStateCancelled || state == ToolCallBatchStateOutcomeUnknown
}

func toolCallBatchItemTerminal(state string) bool {
	switch state {
	case ToolCallBatchItemStateCompleted, ToolCallBatchItemStateFailed, ToolCallBatchItemStateBlocked,
		ToolCallBatchItemStateCancelled, ToolCallBatchItemStateOutcomeUnknown:
		return true
	default:
		return false
	}
}

type toolCallBatchQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getToolCallBatchQuery(
	ctx context.Context,
	query toolCallBatchQuery,
	batchID string,
) (ToolCallBatch, bool, error) {
	var batch ToolCallBatch
	var waitingOrdinal sql.NullInt64
	var runnerID, runnerClaimSHA sql.NullString
	var reasonCode, createdAt, updatedAt string
	var runnerAttempt sql.NullInt64
	err := query.QueryRowContext(ctx, `SELECT batch_id,owner_user_id,stream_uid,branch_id,branch_generation,
		source_event_id,source_publication_seq,source_runner_attempt,source_client_message_id,admitted_input_revision,
		call_count,next_ordinal,state,state_version,waiting_ordinal,runner_id,runner_attempt,runner_claim_sha256,
		reason_code,created_at,updated_at
		FROM transcript_tool_call_batches WHERE batch_id=?`, batchID).Scan(
		&batch.BatchID, &batch.OwnerUserID, &batch.StreamUID, &batch.BranchID, &batch.BranchGeneration,
		&batch.SourceEventID, &batch.SourcePublicationSeq, &batch.SourceRunnerAttempt, &batch.SourceClientMessageID,
		&batch.AdmittedInputRevision, &batch.CallCount, &batch.NextOrdinal, &batch.State, &batch.StateVersion,
		&waitingOrdinal, &runnerID, &runnerAttempt, &runnerClaimSHA, &reasonCode, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ToolCallBatch{}, false, nil
	}
	if err != nil {
		return ToolCallBatch{}, false, err
	}
	batch.WaitingOrdinal = nullToolCallBatchInt64(waitingOrdinal)
	batch.RunnerID = runnerID.String
	if runnerAttempt.Valid {
		batch.RunnerAttempt = runnerAttempt.Int64
	}
	batch.RunnerClaimSHA256 = runnerClaimSHA.String
	batch.ReasonCode = reasonCode
	batch.CreatedAt, err = parseToolCallBatchTime(createdAt)
	if err != nil {
		return ToolCallBatch{}, false, err
	}
	batch.UpdatedAt, err = parseToolCallBatchTime(updatedAt)
	if err != nil {
		return ToolCallBatch{}, false, err
	}
	return batch, true, nil
}

func getToolCallBatchItemQuery(
	ctx context.Context,
	query toolCallBatchQuery,
	batchID string,
	ordinal int64,
) (ToolCallBatchItem, bool, error) {
	var item ToolCallBatchItem
	var argumentsJSON, createdAt, updatedAt string
	var startedEvent, waitingEvent, terminalEvent sql.NullInt64
	var terminalSHA, resultRef sql.NullString
	err := query.QueryRowContext(ctx, `SELECT batch_id,stream_uid,ordinal,tool_call_id,tool_name,arguments_json,
		arguments_sha256,state,state_version,started_event_id,waiting_event_id,terminal_event_id,
		terminal_result_sha256,result_ref,created_at,updated_at
		FROM transcript_tool_call_items WHERE batch_id=? AND ordinal=?`, batchID, ordinal).Scan(
		&item.BatchID, &item.StreamUID, &item.Ordinal, &item.ToolCallID, &item.ToolName, &argumentsJSON,
		&item.ArgumentsSHA256, &item.State, &item.StateVersion, &startedEvent, &waitingEvent, &terminalEvent,
		&terminalSHA, &resultRef, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ToolCallBatchItem{}, false, nil
	}
	if err != nil {
		return ToolCallBatchItem{}, false, err
	}
	item.ArgumentsJSON = []byte(argumentsJSON)
	if startedEvent.Valid {
		item.StartedEventID = startedEvent.Int64
	}
	if waitingEvent.Valid {
		item.WaitingEventID = waitingEvent.Int64
	}
	if terminalEvent.Valid {
		item.TerminalEventID = terminalEvent.Int64
	}
	item.TerminalResultSHA256 = terminalSHA.String
	item.ResultRef = resultRef.String
	item.CreatedAt, err = parseToolCallBatchTime(createdAt)
	if err != nil {
		return ToolCallBatchItem{}, false, err
	}
	item.UpdatedAt, err = parseToolCallBatchTime(updatedAt)
	if err != nil {
		return ToolCallBatchItem{}, false, err
	}
	return item, true, nil
}

func listToolCallBatchItemsQuery(
	ctx context.Context,
	query toolCallBatchQuery,
	batchID string,
) ([]ToolCallBatchItem, error) {
	rows, err := query.QueryContext(ctx, `SELECT ordinal FROM transcript_tool_call_items WHERE batch_id=? ORDER BY ordinal`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ordinals := []int64{}
	for rows.Next() {
		var ordinal int64
		if err := rows.Scan(&ordinal); err != nil {
			return nil, err
		}
		ordinals = append(ordinals, ordinal)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	items := make([]ToolCallBatchItem, 0, len(ordinals))
	for _, ordinal := range ordinals {
		item, found, err := getToolCallBatchItemQuery(ctx, query, batchID, ordinal)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, ErrToolCallBatchConflict
		}
		items = append(items, item)
	}
	return items, nil
}

func toolCallBatchOriginMatches(
	batch ToolCallBatch,
	event transcriptstore.Event,
	calls []canonicalToolCallBatchItem,
) bool {
	return batch.BatchID == toolCallBatchID(event.StreamUID, event.EventID) && batch.StreamUID == event.StreamUID &&
		batch.SourceEventID == event.EventID && batch.SourcePublicationSeq == event.PublicationSeq &&
		event.RunnerAttempt != nil && batch.SourceRunnerAttempt == *event.RunnerAttempt &&
		batch.SourceClientMessageID == event.ClientMessageID && batch.CallCount == int64(len(calls))
}

func toolCallBatchItemsMatch(items []ToolCallBatchItem, calls []canonicalToolCallBatchItem) bool {
	if len(items) != len(calls) {
		return false
	}
	for index, call := range calls {
		item := items[index]
		if item.Ordinal != int64(index) || item.ToolCallID != call.ID || item.ToolName != call.Name ||
			!bytes.Equal(item.ArgumentsJSON, call.ArgumentsJSON) || item.ArgumentsSHA256 != call.ArgumentsSHA256 {
			return false
		}
	}
	return true
}

func parseToolCallBatchTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse tool call batch time: %w", err)
	}
	return parsed.UTC(), nil
}

func nullableToolCallBatchInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullToolCallBatchInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func nullableToolCallBatchString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

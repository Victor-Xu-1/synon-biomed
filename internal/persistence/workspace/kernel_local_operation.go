package workspace

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"synon-go/internal/kernelcontract"
	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	KernelLocalOperationStatePendingApproval = "pending_approval"
	KernelLocalOperationStateApproved        = "approved"
	KernelLocalOperationStatePrepared        = "prepared"
	KernelLocalOperationStateStarted         = "started"
	KernelLocalOperationStateCompleted       = "completed"
	KernelLocalOperationStateFailed          = "failed"
	KernelLocalOperationStateCancelled       = "cancelled"
	KernelLocalOperationStateOutcomeUnknown  = "outcome_unknown"

	kernelLocalOperationIdentityDomain = "synon.kernel-operation.v1"
)

var (
	ErrKernelLocalOperationConflict = errors.New("kernel local operation conflicts with durable state")
	ErrKernelLocalOperationStale    = errors.New("kernel local operation state is stale")
)

type KernelLocalOperation struct {
	OperationID            string
	OwnerUserID            string
	ProjectID              string
	RootFrameID            string
	RootFrameIncarnationID string
	FrameID                string
	FrameIncarnationID     string
	StreamUID              string
	BranchID               string
	BranchGeneration       int64
	SourceEventID          int64
	SourcePublicationSeq   int64
	SourceRunnerAttempt    int64
	SourceClientMessageID  string
	ToolCallOrdinal        int64
	ToolCallID             string
	Tool                   string
	Environment            string
	InputJSON              []byte
	InputSHA256            string
	ConfinementSHA256      string
	State                  string
	StateVersion           int64
	ApprovalRequestID      string
	ApprovalDecisionID     string
	ApprovalDecision       string
	ApprovalScope          string
	ApprovalSource         string
	ApprovalActorID        string
	AdmittedInputRevision  int64
	RunnerID               string
	RunnerAttempt          int64
	RunnerClaimSHA256      string
	BootID                 string
	KernelID               string
	KernelGeneration       int64
	ExecutionID            string
	ExecutionLogID         string
	ResultJSON             []byte
	ExecutionLogRef        string
	ResultSHA256           string
	ReasonCode             string
	CreatedAt              time.Time
	ApprovedAt             *time.Time
	DecidedAt              *time.Time
	PreparedAt             *time.Time
	StartedAt              *time.Time
	TerminalAt             *time.Time
	UpdatedAt              time.Time
}

// KernelLocalExecutionBindings returns the immutable execution-log to kernel
// identity bindings for terminal operations owned by one exact Frame
// incarnation. Artifact publication uses this projection to recognize both
// persistent language kernels and operation-scoped software runtimes without
// embedding provider-specific kernel ID formats.
func (s *Store) KernelLocalExecutionBindings(
	ctx context.Context,
	access KernelFrameAccess,
) (map[string]string, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(access.UserID) == "" || strings.TrimSpace(access.Frame.ProjectID) == "" ||
		strings.TrimSpace(access.Frame.RootFrameID) == "" || strings.TrimSpace(access.RootFrameIncarnationID) == "" ||
		strings.TrimSpace(access.Frame.ID) == "" || strings.TrimSpace(access.Frame.IncarnationID) == "" {
		return nil, errors.New("complete kernel Frame authority is required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT execution_log_id, kernel_id
		FROM kernel_local_operations
		WHERE owner_user_id=? AND project_id=? AND root_frame_id=? AND root_frame_incarnation_id=?
			AND frame_id=? AND frame_incarnation_id=?
			AND state IN (?, ?, ?)
			AND execution_log_id<>'' AND kernel_id<>''
		ORDER BY execution_log_id, operation_id`,
		access.UserID, access.Frame.ProjectID, access.Frame.RootFrameID, access.RootFrameIncarnationID,
		access.Frame.ID, access.Frame.IncarnationID,
		KernelLocalOperationStateCompleted, KernelLocalOperationStateFailed, KernelLocalOperationStateCancelled,
	)
	if err != nil {
		return nil, fmt.Errorf("query kernel local execution bindings: %w", err)
	}
	defer rows.Close()
	bindings := map[string]string{}
	for rows.Next() {
		var executionLogID, kernelID string
		if err := rows.Scan(&executionLogID, &kernelID); err != nil {
			return nil, fmt.Errorf("scan kernel local execution binding: %w", err)
		}
		executionLogID, kernelID = strings.TrimSpace(executionLogID), strings.TrimSpace(kernelID)
		if executionLogID == "" || kernelID == "" {
			return nil, ErrKernelLocalOperationConflict
		}
		if existing, found := bindings[executionLogID]; found && existing != kernelID {
			return nil, ErrKernelLocalOperationConflict
		}
		bindings[executionLogID] = kernelID
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate kernel local execution bindings: %w", err)
	}
	return bindings, nil
}

type kernelLocalToolCallV1 struct {
	ID                      string          `json:"id"`
	Type                    string          `json:"type"`
	Name                    string          `json:"name"`
	Arguments               json.RawMessage `json:"arguments"`
	RejectedBeforeExecution bool            `json:"rejectedBeforeExecution,omitempty"`
}

type ResolveKernelLocalOperationApprovalInput struct {
	OwnerUserID          string
	OperationID          string
	ExpectedStateVersion int64
	ApprovalRequestID    string
	Approved             bool
	DecisionID           string
	Scope                string
	Source               string
	ActorID              string
	ReasonCode           string
	AdmitRunnerRevision  bool
	CurrentClaim         transcriptstore.RunnerClaim
}

type PrepareKernelLocalOperationInput struct {
	OwnerUserID          string
	OperationID          string
	ExpectedStateVersion int64
	Claim                transcriptstore.RunnerClaim
	BootID               string
	KernelID             string
	KernelGeneration     int64
	ConfinementSHA256    string
}

type StartKernelLocalOperationInput struct {
	OwnerUserID          string
	OperationID          string
	ExpectedStateVersion int64
	Claim                transcriptstore.RunnerClaim
	BootID               string
	ExecutionID          string
}

type ReclaimPreparedKernelLocalOperationInput struct {
	OwnerUserID          string
	OperationID          string
	ExpectedStateVersion int64
	RunnerID             string
	RunnerAttempt        int64
	RunnerClaimSHA256    string
	BootID               string
	ReasonCode           string
}

type ReleasePreparedKernelLocalOperationInput struct {
	OwnerUserID          string
	OperationID          string
	ExpectedStateVersion int64
	Claim                transcriptstore.RunnerClaim
	BootID               string
	ReasonCode           string
}

type MarkKernelLocalOperationOutcomeUnknownInput struct {
	OwnerUserID          string
	OperationID          string
	ExpectedStateVersion int64
	ExecutionID          string
	CurrentBootID        string
	ReasonCode           string
}

type KernelLocalOperationRecoveryCandidate struct {
	Operation   KernelLocalOperation
	AttemptLive bool
}

// ListExpiredApprovedKernelLocalOperationRecoveryCandidates returns only
// operations whose package/setup phase never reached prepared or started.
// Their source runner lease is no longer live, so replaying the earlier
// resumable checkpoint cannot duplicate an admitted side effect.
func (s *Store) ListExpiredApprovedKernelLocalOperationRecoveryCandidates(
	ctx context.Context,
	limit int,
) ([]KernelLocalOperation, error) {
	return s.ListExpiredApprovedKernelLocalOperationRecoveryCandidatesPage(ctx, 0, limit)
}

// ListExpiredApprovedKernelLocalOperationRecoveryCandidatesPage returns one
// deterministic scanner page. The recovery coordinator drains all pages so a
// large cohort cannot strand an older approved operation behind a page limit.
func (s *Store) ListExpiredApprovedKernelLocalOperationRecoveryCandidatesPage(
	ctx context.Context,
	offset, limit int,
) ([]KernelLocalOperation, error) {
	if s == nil || s.db == nil || ctx == nil || offset < 0 || limit <= 0 || limit > 1000 {
		return nil, errors.New("approved kernel recovery requires a nonnegative offset and page size between 1 and 1000")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT operation.operation_id
		FROM kernel_local_operations operation
		JOIN transcript_runner_attempts attempt
			ON attempt.stream_uid=operation.stream_uid
			AND attempt.attempt=operation.source_runner_attempt
		WHERE operation.state='approved'
			AND operation.execution_id IS NULL
			AND (attempt.status!='running' OR attempt.expires_at<=?)
			AND NOT EXISTS (
				SELECT 1 FROM kernel_local_operation_materializations materialization
				WHERE materialization.operation_id=operation.operation_id
			)
		ORDER BY operation.updated_at,operation.operation_id LIMIT ? OFFSET ?`, s.now().UTC(), limit, offset)
	if err != nil {
		return nil, err
	}
	operationIDs := make([]string, 0, limit)
	for rows.Next() {
		var operationID string
		if err := rows.Scan(&operationID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		operationIDs = append(operationIDs, operationID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	result := make([]KernelLocalOperation, 0, len(operationIDs))
	for _, operationID := range operationIDs {
		operation, found, err := getKernelLocalOperationQuery(ctx, s.db, operationID)
		if err != nil {
			return nil, err
		}
		if !found || operation.State != KernelLocalOperationStateApproved || operation.ExecutionID != "" {
			return nil, ErrKernelLocalOperationConflict
		}
		result = append(result, operation)
	}
	return result, nil
}

// CreateKernelLocalOperationsForCheckpointTx binds every local kernel call to
// its immutable canonical model-tool-call checkpoint in the same SQLite
// transaction. A replay must match the existing rows byte-for-byte.
func (s *Store) CreateKernelLocalOperationsForCheckpointTx(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	event transcriptstore.Event,
) ([]KernelLocalOperation, error) {
	if s == nil || tx == nil || strings.TrimSpace(event.StreamUID) == "" || event.EventID <= 0 {
		return nil, errors.New("workspace, transcript transaction, and source checkpoint are required")
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
		return nil, ErrKernelLocalOperationConflict
	}
	if err != nil {
		return nil, err
	}
	if eventType != "runner_checkpoint" || source != string(transcriptstore.EventSourcePayload) ||
		publicationSeq != event.PublicationSeq || clientMessageID != event.ClientMessageID ||
		event.RunnerAttempt == nil || runnerAttempt != *event.RunnerAttempt || !bytes.Equal(payloadJSON, event.PayloadJSON) {
		return nil, ErrKernelLocalOperationConflict
	}
	var authority struct {
		OwnerUserID, ProjectID, RootFrameID, RootIncarnationID string
		FrameID, FrameIncarnationID, Kind                      string
	}
	err = tx.QueryRowContext(ctx, `SELECT stream.owner_id,stream.project_id,stream.root_frame_id,root.incarnation_id,
		stream.frame_id,frame.incarnation_id,stream.kind
		FROM transcript_streams stream
		JOIN projects project ON project.id=stream.project_id AND project.user_id=stream.owner_id
		JOIN frames frame ON frame.id=stream.frame_id AND frame.project_id=stream.project_id
		JOIN frames root ON root.id=stream.root_frame_id AND root.project_id=stream.project_id
		WHERE stream.stream_uid=?`, event.StreamUID).Scan(
		&authority.OwnerUserID, &authority.ProjectID, &authority.RootFrameID, &authority.RootIncarnationID,
		&authority.FrameID, &authority.FrameIncarnationID, &authority.Kind,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrKernelLocalOperationConflict
	}
	if err != nil {
		return nil, err
	}
	if authority.Kind != string(transcriptstore.StreamKindFrameRef) {
		return nil, ErrKernelLocalOperationConflict
	}
	var checkpoint struct {
		ModelToolCalls []kernelLocalToolCallV1 `json:"modelToolCalls"`
	}
	if !utf8.Valid(payloadJSON) || validateKernelLocalOperationJSON(payloadJSON) != nil {
		return nil, errors.New("canonical model tool-call checkpoint is invalid")
	}
	if err := json.Unmarshal(payloadJSON, &checkpoint); err != nil || len(checkpoint.ModelToolCalls) == 0 {
		return nil, errors.New("canonical model tool-call checkpoint is invalid")
	}
	seenCallIDs := make(map[string]struct{}, len(checkpoint.ModelToolCalls))
	operations := make([]KernelLocalOperation, 0, len(checkpoint.ModelToolCalls))
	var activeBranchID string
	var activeBranchGeneration int64
	for ordinal, call := range checkpoint.ModelToolCalls {
		call.ID = strings.TrimSpace(call.ID)
		call.Name = strings.TrimSpace(call.Name)
		if call.ID == "" || len(call.ID) > 512 || call.Type != "function" {
			return nil, errors.New("canonical model tool call identity is invalid")
		}
		if _, duplicate := seenCallIDs[call.ID]; duplicate {
			return nil, errors.New("canonical model tool call identity is duplicated")
		}
		seenCallIDs[call.ID] = struct{}{}
		if call.RejectedBeforeExecution {
			continue
		}
		if !kernelcontract.IsTool(call.Name) {
			continue
		}
		canonicalInput, environment, err := canonicalKernelLocalOperationInput(call.Name, call.Arguments)
		if err != nil {
			// The model call remains durably represented by the ordinary tool
			// batch. It is deliberately not admitted to installation/execution;
			// the tool handler will return its validation error to the model so
			// the same task can repair the arguments and continue.
			continue
		}
		inputDigest := sha256.Sum256(canonicalInput)
		operationID := kernelLocalOperationID(event.StreamUID, event.EventID, int64(ordinal))
		existing, found, err := getKernelLocalOperationBySourceQuery(ctx, tx, event.StreamUID, event.EventID, int64(ordinal))
		if err != nil {
			return nil, err
		}
		branchID, branchGeneration := activeBranchID, activeBranchGeneration
		if found {
			branchID, branchGeneration = existing.BranchID, existing.BranchGeneration
		} else if branchID == "" {
			if err := tx.QueryRowContext(ctx, `SELECT active_branch_id,generation FROM transcript_branch_state WHERE stream_uid=?`,
				event.StreamUID).Scan(&activeBranchID, &activeBranchGeneration); err != nil {
				return nil, err
			}
			branchID, branchGeneration = activeBranchID, activeBranchGeneration
		}
		belongs, err := kernelLocalOperationEventBelongsToBranch(ctx, tx, event.StreamUID, branchID, event.EventID)
		if err != nil {
			return nil, err
		}
		if branchID == "" || branchGeneration <= 0 || !belongs {
			return nil, ErrKernelLocalOperationConflict
		}
		approvalRequestID := existing.ApprovalRequestID
		if !found {
			approvalRequestID, err = newKernelLocalOperationApprovalRequestID()
			if err != nil {
				return nil, err
			}
		}
		operation := KernelLocalOperation{
			OperationID: operationID, OwnerUserID: authority.OwnerUserID, ProjectID: authority.ProjectID,
			RootFrameID: authority.RootFrameID, RootFrameIncarnationID: authority.RootIncarnationID,
			FrameID: authority.FrameID, FrameIncarnationID: authority.FrameIncarnationID,
			StreamUID: event.StreamUID, BranchID: branchID, BranchGeneration: branchGeneration,
			SourceEventID: event.EventID, SourcePublicationSeq: publicationSeq, SourceRunnerAttempt: runnerAttempt,
			SourceClientMessageID: clientMessageID, ToolCallOrdinal: int64(ordinal), ToolCallID: call.ID,
			Tool: call.Name, Environment: environment, InputJSON: canonicalInput,
			InputSHA256: hex.EncodeToString(inputDigest[:]), State: KernelLocalOperationStatePendingApproval,
			StateVersion: 1, ApprovalRequestID: approvalRequestID,
			CreatedAt: createdAt.UTC(), UpdatedAt: createdAt.UTC(),
		}
		if found {
			if !kernelLocalOperationOriginMatches(existing, operation) {
				return nil, ErrKernelLocalOperationConflict
			}
			operation = existing
		} else {
			created, err := insertKernelLocalOperationTx(ctx, tx, operation)
			if err != nil {
				return nil, err
			}
			if !created {
				return nil, ErrKernelLocalOperationConflict
			}
		}
		if _, err := projectKernelLocalOperationApprovalRequestedTx(ctx, s, tx, operation); err != nil {
			return nil, err
		}
		operations = append(operations, operation)
	}
	return operations, nil
}

func canonicalKernelLocalOperationInput(tool string, raw json.RawMessage) ([]byte, string, error) {
	return kernelcontract.CanonicalInput(tool, raw)
}

func validateKernelLocalOperationJSON(raw []byte) error {
	return kernelcontract.ValidateJSON(raw)
}

func kernelLocalOperationID(streamUID string, eventID, ordinal int64) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		kernelLocalOperationIdentityDomain, streamUID,
		strconv.FormatInt(eventID, 10), strconv.FormatInt(ordinal, 10),
	}, "\x00")))
	return "kop_" + hex.EncodeToString(digest[:])
}

func newKernelLocalOperationApprovalRequestID() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "kop_approval_" + hex.EncodeToString(value), nil
}

func insertKernelLocalOperationTx(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	operation KernelLocalOperation,
) (bool, error) {
	result, err := tx.ExecContext(ctx, `INSERT INTO kernel_local_operations(
		operation_id,owner_user_id,project_id,root_frame_id,root_frame_incarnation_id,frame_id,frame_incarnation_id,
		stream_uid,branch_id,branch_generation,source_event_id,source_publication_seq,source_runner_attempt,
		source_client_message_id,tool_call_ordinal,tool_call_id,tool,environment,input_json,input_sha256,
		confinement_sha256,state,state_version,approval_request_id,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL,?,1,?,?,?) ON CONFLICT(operation_id) DO NOTHING`,
		operation.OperationID, operation.OwnerUserID, operation.ProjectID, operation.RootFrameID,
		operation.RootFrameIncarnationID, operation.FrameID, operation.FrameIncarnationID, operation.StreamUID,
		operation.BranchID, operation.BranchGeneration, operation.SourceEventID, operation.SourcePublicationSeq,
		operation.SourceRunnerAttempt, operation.SourceClientMessageID, operation.ToolCallOrdinal, operation.ToolCallID,
		operation.Tool, operation.Environment, string(operation.InputJSON), operation.InputSHA256,
		operation.State, operation.ApprovalRequestID, operation.CreatedAt.Format(time.RFC3339Nano), operation.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil || rows == 0 {
		return false, err
	}
	if rows != 1 {
		return false, ErrKernelLocalOperationConflict
	}
	return true, nil
}

func (s *Store) GetKernelLocalOperation(
	ctx context.Context,
	ownerUserID, operationID string,
) (KernelLocalOperation, bool, error) {
	ownerUserID = strings.TrimSpace(ownerUserID)
	operationID = strings.TrimSpace(operationID)
	if s == nil || ownerUserID == "" || operationID == "" {
		return KernelLocalOperation{}, false, errors.New("owner and operation id are required")
	}
	operation, found, err := getKernelLocalOperationQuery(ctx, s.db, operationID)
	if err != nil || !found {
		return operation, found, err
	}
	if operation.OwnerUserID != ownerUserID {
		return KernelLocalOperation{}, false, nil
	}
	return operation, true, nil
}

// ListApprovedKernelLocalOperations returns approved, nonterminal operations
// that still need a live runner turn. It is used only by restart recovery to
// rebuild the canonical frame resume dispatch; it never executes code or
// changes operation state.
func (s *Store) ListApprovedKernelLocalOperations(
	ctx context.Context,
	limit int,
) ([]KernelLocalOperation, error) {
	db := s.readDatabase()
	if db == nil {
		return nil, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 || limit > 1000 {
		return nil, errors.New("approved kernel local operation limit must be between 1 and 1000")
	}
	rows, err := db.QueryContext(ctx, `
		SELECT operation_id FROM kernel_local_operations
		WHERE state=? AND COALESCE(execution_log_id,'')=''
		ORDER BY COALESCE(approved_at,updated_at), operation_id LIMIT ?`,
		KernelLocalOperationStateApproved, limit)
	if err != nil {
		return nil, fmt.Errorf("list approved kernel local operations: %w", err)
	}
	defer rows.Close()
	operations := make([]KernelLocalOperation, 0, limit)
	for rows.Next() {
		var operationID string
		if err := rows.Scan(&operationID); err != nil {
			return nil, fmt.Errorf("scan approved kernel local operation id: %w", err)
		}
		operation, found, err := getKernelLocalOperationQuery(ctx, db, operationID)
		if err != nil {
			return nil, err
		}
		if found && operation.State == KernelLocalOperationStateApproved && operation.ExecutionLogID == "" {
			operations = append(operations, operation)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate approved kernel local operations: %w", err)
	}
	return operations, nil
}

// ListPendingKernelLocalOperations returns immutable v2 operations that are
// still waiting for policy resolution after their source runner is no longer
// live. A live runner owns synchronous policy admission; excluding it here
// prevents recovery from invalidating the same claim while the tool is starting.
// Recovery may apply a remembered allow grant to the remaining exact operations;
// it never reconstructs input from the web pending-request projection.
func (s *Store) ListPendingKernelLocalOperations(
	ctx context.Context,
	limit int,
) ([]KernelLocalOperation, error) {
	db := s.readDatabase()
	if db == nil {
		return nil, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 || limit > 1000 {
		return nil, errors.New("pending kernel local operation limit must be between 1 and 1000")
	}
	now := s.now().UTC()
	rows, err := db.QueryContext(ctx, `SELECT operation_id FROM kernel_local_operations operation
		WHERE operation.state=? AND NOT EXISTS (
			SELECT 1 FROM transcript_runner_attempts attempt
			WHERE attempt.stream_uid=operation.stream_uid
				AND attempt.attempt=operation.source_runner_attempt
				AND attempt.status='running' AND attempt.expires_at>?
		) ORDER BY operation.created_at,operation.operation_id LIMIT ?`,
		KernelLocalOperationStatePendingApproval, now, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending kernel local operations: %w", err)
	}
	defer rows.Close()
	operations := make([]KernelLocalOperation, 0, limit)
	for rows.Next() {
		var operationID string
		if err := rows.Scan(&operationID); err != nil {
			return nil, fmt.Errorf("scan pending kernel local operation id: %w", err)
		}
		operation, found, err := getKernelLocalOperationQuery(ctx, db, operationID)
		if err != nil {
			return nil, err
		}
		if found && operation.State == KernelLocalOperationStatePendingApproval {
			operations = append(operations, operation)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending kernel local operations: %w", err)
	}
	return operations, nil
}

// GetKernelLocalOperationByApprovalRequest resolves the opaque approval
// locator to its immutable operation. The owner predicate is part of the
// query so callers cannot use the locator as a bearer credential or ownership
// oracle.
func (s *Store) GetKernelLocalOperationByApprovalRequest(
	ctx context.Context,
	ownerUserID, approvalRequestID string,
) (KernelLocalOperation, bool, error) {
	ownerUserID = strings.TrimSpace(ownerUserID)
	approvalRequestID = strings.TrimSpace(approvalRequestID)
	if s == nil || ownerUserID == "" || approvalRequestID == "" {
		return KernelLocalOperation{}, false, errors.New("owner and approval request id are required")
	}
	operation, found, err := getKernelLocalOperationByApprovalRequestQuery(
		ctx, s.db, ownerUserID, approvalRequestID,
	)
	return operation, found, err
}

// GetKernelLocalOperationByToolCall returns the single durable operation for
// a canonical model tool call. Tool-call IDs are not globally unique, so the
// stream and owner are mandatory parts of the lookup authority.
func (s *Store) GetKernelLocalOperationByToolCall(
	ctx context.Context,
	ownerUserID, streamUID, toolCallID string,
) (KernelLocalOperation, bool, error) {
	ownerUserID = strings.TrimSpace(ownerUserID)
	streamUID = strings.TrimSpace(streamUID)
	toolCallID = strings.TrimSpace(toolCallID)
	if s == nil || ownerUserID == "" || streamUID == "" || toolCallID == "" {
		return KernelLocalOperation{}, false, errors.New("owner, stream, and tool call id are required")
	}
	operation, found, err := getKernelLocalOperationByToolCallQuery(
		ctx, s.db, ownerUserID, streamUID, toolCallID,
	)
	return operation, found, err
}

// ListRunnableKernelLocalOperations returns only operations admitted to the
// exact live claim. Branch membership, frame authority, input revision, and
// the absence of a native protocol receipt are checked in one immediate
// transaction so an older claim can never execute a later approval.
func (s *Store) ListRunnableKernelLocalOperations(
	ctx context.Context,
	claim transcriptstore.RunnerClaim,
	limit int,
) ([]KernelLocalOperation, error) {
	if s == nil || limit <= 0 || limit > 100 {
		return nil, errors.New("a live runner claim and operation limit between 1 and 100 are required")
	}
	repository, err := s.TranscriptRepository(ctx)
	if err != nil {
		return nil, err
	}
	operations := make([]KernelLocalOperation, 0, limit)
	err = repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		stream, err := tx.ValidateLiveRunnerClaim(ctx, claim)
		if err != nil {
			return err
		}
		if stream.UID != claim.StreamUID || stream.OwnerID != claim.OwnerID || claim.ClaimedInputRevision <= 0 {
			return ErrKernelLocalOperationConflict
		}
		rows, err := tx.QueryContext(ctx, `SELECT operation.operation_id,
			COALESCE(operation.admitted_input_revision,source_attempt.claimed_input_revision)
			FROM kernel_local_operations operation
			JOIN transcript_tool_call_batches batch ON batch.stream_uid=operation.stream_uid
				AND batch.source_event_id=operation.source_event_id
				AND batch.state IN ('ready','running','waiting')
			JOIN transcript_tool_call_items item ON item.batch_id=batch.batch_id
				AND item.ordinal=operation.tool_call_ordinal
				AND item.tool_call_id=operation.tool_call_id
				AND item.state IN ('pending','running','waiting')
			JOIN transcript_branch_state branch ON branch.stream_uid=operation.stream_uid
				AND branch.active_branch_id=operation.branch_id AND branch.generation=operation.branch_generation
			JOIN transcript_branch_events source ON source.stream_uid=operation.stream_uid
				AND source.branch_id=operation.branch_id AND source.event_id=operation.source_event_id
			JOIN transcript_runner_attempts source_attempt ON source_attempt.stream_uid=operation.stream_uid
				AND source_attempt.attempt=operation.source_runner_attempt
			LEFT JOIN kernel_local_operation_protocol_receipts receipt ON receipt.operation_id=operation.operation_id
			WHERE operation.owner_user_id=? AND operation.stream_uid=?
				AND ((operation.admitted_input_revision IS NOT NULL AND operation.admitted_input_revision<=?) OR
					(operation.admitted_input_revision IS NULL AND operation.state='cancelled'
						AND operation.approval_decision='deny' AND operation.approval_source='policy'
						AND operation.approval_actor_id='system' AND operation.execution_id IS NULL
						AND operation.result_sha256 IS NOT NULL
						AND ((operation.result_json IS NOT NULL)!=(operation.result_ref IS NOT NULL))
						AND source_attempt.claimed_input_revision<=?))
				AND receipt.operation_id IS NULL
				AND batch.next_ordinal=item.ordinal
				AND ((operation.state='approved' AND operation.approval_decision='allow') OR
					(operation.state IN ('completed','failed','cancelled','outcome_unknown')
						AND operation.result_sha256 IS NOT NULL
						AND ((operation.result_json IS NOT NULL)!=(operation.result_ref IS NOT NULL))))
			ORDER BY COALESCE(operation.admitted_input_revision,source_attempt.claimed_input_revision),
				operation.source_publication_seq,
				operation.tool_call_ordinal,operation.operation_id LIMIT ?`,
			claim.OwnerID, claim.StreamUID, claim.ClaimedInputRevision, claim.ClaimedInputRevision, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		type runnableOperation struct {
			operationID            string
			effectiveInputRevision int64
		}
		ids := make([]runnableOperation, 0, limit)
		for rows.Next() {
			var candidate runnableOperation
			if err := rows.Scan(&candidate.operationID, &candidate.effectiveInputRevision); err != nil {
				return err
			}
			if candidate.effectiveInputRevision <= 0 || candidate.effectiveInputRevision > claim.ClaimedInputRevision {
				return ErrKernelLocalOperationConflict
			}
			ids = append(ids, candidate)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, candidate := range ids {
			operation, found, err := getKernelLocalOperationQuery(ctx, tx, candidate.operationID)
			if err != nil {
				return err
			}
			if !found || operation.OwnerUserID != claim.OwnerID || operation.StreamUID != claim.StreamUID {
				return ErrKernelLocalOperationConflict
			}
			if operation.AdmittedInputRevision == 0 {
				operation.AdmittedInputRevision = candidate.effectiveInputRevision
			}
			if operation.AdmittedInputRevision <= 0 || operation.AdmittedInputRevision > claim.ClaimedInputRevision {
				return ErrKernelLocalOperationConflict
			}
			if err := validateKernelLocalOperationCurrentAuthority(ctx, tx, operation, true); err != nil {
				return err
			}
			operations = append(operations, operation)
		}
		return nil
	})
	return operations, err
}

func (s *Store) ListKernelLocalOperationRecoveryCandidates(
	ctx context.Context,
	currentBootID string,
	limit int,
) ([]KernelLocalOperationRecoveryCandidate, bool, error) {
	currentBootID = strings.TrimSpace(currentBootID)
	if s == nil || currentBootID == "" || limit <= 0 || limit > 1000 {
		return nil, false, errors.New("kernel local operation recovery limit must be between 1 and 1000")
	}
	now := s.now().UTC()
	rows, err := s.db.QueryContext(ctx, `SELECT operation_id FROM kernel_local_operations
		WHERE (state='prepared' OR (state='started' AND boot_id!=?)) AND NOT EXISTS (
			SELECT 1 FROM kernel_detached_executions detached
			WHERE detached.operation_id=kernel_local_operations.operation_id
				AND detached.state!='evidence_lost'
		) AND NOT EXISTS (
			SELECT 1 FROM transcript_runner_attempts attempt
			WHERE attempt.stream_uid=kernel_local_operations.stream_uid
				AND attempt.attempt=kernel_local_operations.runner_attempt
				AND attempt.runner_id=kernel_local_operations.runner_id
				AND lower(hex(attempt.claim_token_sha256))=kernel_local_operations.runner_claim_sha256
				AND attempt.status='running' AND attempt.expires_at>?
		) AND NOT EXISTS (
			SELECT 1 FROM workspace_outbox settlement
			WHERE settlement.topic=? AND settlement.aggregate_type='kernel_local_operation'
				AND settlement.aggregate_id=kernel_local_operations.operation_id
				AND settlement.status IN ('pending','inflight','dead_letter')
		) AND NOT EXISTS (
			SELECT 1 FROM kernel_local_operation_materializations materialization
			WHERE materialization.operation_id=kernel_local_operations.operation_id
		) ORDER BY updated_at,operation_id LIMIT ?`, currentBootID, now,
		KernelResultSettlementOutboxTopic, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	ids := make([]string, 0, limit+1)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, false, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(ids) > limit
	if more {
		ids = ids[:limit]
	}
	candidates := make([]KernelLocalOperationRecoveryCandidate, 0, len(ids))
	for _, id := range ids {
		operation, found, err := getKernelLocalOperationQuery(ctx, s.db, id)
		if err != nil {
			return nil, false, err
		}
		if !found || (operation.State != KernelLocalOperationStatePrepared &&
			operation.State != KernelLocalOperationStateStarted) {
			return nil, false, ErrKernelLocalOperationConflict
		}
		candidates = append(candidates, KernelLocalOperationRecoveryCandidate{
			Operation: operation, AttemptLive: false,
		})
	}
	return candidates, more, nil
}

// NextKernelLocalOperationRecoveryDue returns the earliest live runner lease
// whose expiry can make a pending-approval, prepared, or foreign-boot started
// operation recoverable. Callers take KernelRetentionWake before this query, so
// a state transition committed between the query and wait cannot be lost.
func (s *Store) NextKernelLocalOperationRecoveryDue(
	ctx context.Context,
	currentBootID string,
) (time.Time, bool, error) {
	currentBootID = strings.TrimSpace(currentBootID)
	if s == nil || s.db == nil || currentBootID == "" {
		return time.Time{}, false, errors.New("kernel local operation recovery authority is required")
	}
	now := s.now().UTC()
	var due time.Time
	err := s.db.QueryRowContext(ctx, `SELECT attempt.expires_at
		FROM kernel_local_operations operation
		JOIN transcript_runner_attempts attempt
			ON attempt.stream_uid=operation.stream_uid
			AND ((operation.state='pending_approval'
					AND attempt.attempt=operation.source_runner_attempt) OR
				(operation.state IN ('prepared','started')
					AND attempt.attempt=operation.runner_attempt
					AND attempt.runner_id=operation.runner_id
					AND lower(hex(attempt.claim_token_sha256))=operation.runner_claim_sha256))
		WHERE (operation.state='pending_approval' OR operation.state='prepared' OR
			(operation.state='started' AND operation.boot_id!=?))
			AND attempt.status='running' AND attempt.expires_at>?
			AND NOT EXISTS (
				SELECT 1 FROM workspace_outbox settlement
				WHERE settlement.topic=? AND settlement.aggregate_type='kernel_local_operation'
					AND settlement.aggregate_id=operation.operation_id
					AND settlement.status IN ('pending','inflight','dead_letter')
			) AND NOT EXISTS (
				SELECT 1 FROM kernel_local_operation_materializations materialization
				WHERE materialization.operation_id=operation.operation_id
			)
		ORDER BY attempt.expires_at,operation.operation_id LIMIT 1`,
		currentBootID, now, KernelResultSettlementOutboxTopic).Scan(&due)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return due.UTC(), true, nil
}

func (s *Store) ResolveKernelLocalOperationApproval(
	ctx context.Context,
	input ResolveKernelLocalOperationApprovalInput,
) (KernelLocalOperation, error) {
	input.OwnerUserID = strings.TrimSpace(input.OwnerUserID)
	input.OperationID = strings.TrimSpace(input.OperationID)
	input.ApprovalRequestID = strings.TrimSpace(input.ApprovalRequestID)
	input.DecisionID = strings.TrimSpace(input.DecisionID)
	input.Scope = strings.TrimSpace(input.Scope)
	input.Source = strings.TrimSpace(input.Source)
	input.ActorID = strings.TrimSpace(input.ActorID)
	var err error
	input.ReasonCode, err = normalizeKernelLocalOperationReason(input.ReasonCode)
	if err != nil {
		return KernelLocalOperation{}, err
	}
	if s == nil || input.OwnerUserID == "" || input.OperationID == "" || input.ExpectedStateVersion <= 0 ||
		input.ApprovalRequestID == "" || len(input.ApprovalRequestID) > 512 || input.DecisionID == "" ||
		len(input.DecisionID) > 512 || !validKernelLocalOperationApprovalScope(input.Scope) ||
		!validKernelLocalOperationApprovalSource(input.Source) || input.ActorID == "" || len(input.ActorID) > 512 ||
		(input.Source == "user" && input.ActorID != input.OwnerUserID) {
		return KernelLocalOperation{}, errors.New("complete kernel local operation approval authority is required")
	}
	target := KernelLocalOperationStateFailed
	decision := "deny"
	if input.Approved {
		target = KernelLocalOperationStateApproved
		decision = "allow"
	} else if input.ReasonCode == "" {
		input.ReasonCode = "approval_denied"
	}
	return s.resolveKernelLocalOperationApprovalAndProject(ctx, input, target, decision)
}

func (s *Store) PrepareKernelLocalOperation(
	ctx context.Context,
	input PrepareKernelLocalOperationInput,
) (KernelLocalOperation, error) {
	input.OwnerUserID = strings.TrimSpace(input.OwnerUserID)
	input.OperationID = strings.TrimSpace(input.OperationID)
	input.BootID = strings.TrimSpace(input.BootID)
	input.KernelID = strings.TrimSpace(input.KernelID)
	input.ConfinementSHA256 = strings.TrimSpace(input.ConfinementSHA256)
	if s == nil || input.OwnerUserID == "" || input.OperationID == "" || input.ExpectedStateVersion <= 0 ||
		strings.TrimSpace(input.Claim.RunnerID) == "" || input.Claim.Attempt <= 0 || strings.TrimSpace(input.Claim.ClaimToken) == "" || input.BootID == "" ||
		input.KernelID == "" || input.KernelGeneration <= 0 || !validLowerHexSHA256(input.ConfinementSHA256) {
		return KernelLocalOperation{}, errors.New("complete kernel local operation preparation authority is required")
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	claimSHA := kernelLocalOperationClaimSHA256(input.Claim.ClaimToken)
	operation, err := s.transitionKernelLocalOperation(ctx, kernelLocalOperationTransitionInput{
		ownerUserID: input.OwnerUserID, operationID: input.OperationID,
		expectedState: KernelLocalOperationStateApproved, expectedVersion: input.ExpectedStateVersion,
		targetState: KernelLocalOperationStatePrepared,
		setClause: `runner_id=?,runner_attempt=?,runner_claim_sha256=?,boot_id=?,kernel_id=?,kernel_generation=?,
			confinement_sha256=COALESCE(confinement_sha256,?),prepared_at=?`,
		setArgs: []any{input.Claim.RunnerID, input.Claim.Attempt, claimSHA, input.BootID,
			input.KernelID, input.KernelGeneration, input.ConfinementSHA256, now},
		extraPredicate: `AND (confinement_sha256 IS NULL OR confinement_sha256=?)`,
		extraArgs:      []any{input.ConfinementSHA256},
		runnerAttempt:  input.Claim.Attempt, runnerClaimSHA256: claimSHA, bootID: input.BootID,
		validate: func(tx *transcriptstore.ImmediateTransaction, operation KernelLocalOperation) error {
			if operation.AdmittedInputRevision <= 0 || operation.AdmittedInputRevision > input.Claim.ClaimedInputRevision {
				return ErrKernelLocalOperationStale
			}
			return validateKernelLocalOperationLiveClaim(ctx, tx, operation, input.Claim)
		},
		idempotent: func(operation KernelLocalOperation) bool {
			return operation.RunnerID == input.Claim.RunnerID && operation.RunnerAttempt == input.Claim.Attempt &&
				operation.RunnerClaimSHA256 == claimSHA && operation.BootID == input.BootID &&
				operation.KernelID == input.KernelID && operation.KernelGeneration == input.KernelGeneration &&
				operation.ConfinementSHA256 == input.ConfinementSHA256
		},
		after: func(tx *transcriptstore.ImmediateTransaction, operation KernelLocalOperation) error {
			return appendKernelLocalOperationApprovalConsumedEvent(ctx, s, tx, operation)
		},
	})
	if err == nil {
		s.signalKernelRetentionWake()
	}
	return operation, err
}

func (s *Store) StartKernelLocalOperation(
	ctx context.Context,
	input StartKernelLocalOperationInput,
) (KernelLocalOperation, error) {
	return s.startKernelLocalOperation(ctx, input, nil)
}

func (s *Store) startKernelLocalOperation(
	ctx context.Context,
	input StartKernelLocalOperationInput,
	after func(*transcriptstore.ImmediateTransaction, KernelLocalOperation) error,
) (KernelLocalOperation, error) {
	input.OwnerUserID = strings.TrimSpace(input.OwnerUserID)
	input.OperationID = strings.TrimSpace(input.OperationID)
	input.BootID = strings.TrimSpace(input.BootID)
	input.ExecutionID = strings.TrimSpace(input.ExecutionID)
	if s == nil || input.OwnerUserID == "" || input.OperationID == "" || input.ExpectedStateVersion <= 0 ||
		strings.TrimSpace(input.Claim.RunnerID) == "" || input.Claim.Attempt <= 0 || strings.TrimSpace(input.Claim.ClaimToken) == "" ||
		input.BootID == "" || input.ExecutionID == "" || len(input.ExecutionID) > 512 {
		return KernelLocalOperation{}, errors.New("complete kernel local operation start authority is required")
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	claimSHA := kernelLocalOperationClaimSHA256(input.Claim.ClaimToken)
	operation, err := s.transitionKernelLocalOperation(ctx, kernelLocalOperationTransitionInput{
		ownerUserID: input.OwnerUserID, operationID: input.OperationID,
		expectedState: KernelLocalOperationStatePrepared, expectedVersion: input.ExpectedStateVersion,
		targetState: KernelLocalOperationStateStarted, setClause: `execution_id=?,started_at=?`,
		setArgs:        []any{input.ExecutionID, now},
		extraPredicate: `AND runner_id=? AND runner_attempt=? AND runner_claim_sha256=? AND boot_id=?`,
		extraArgs:      []any{input.Claim.RunnerID, input.Claim.Attempt, claimSHA, input.BootID},
		runnerAttempt:  input.Claim.Attempt, runnerClaimSHA256: claimSHA, bootID: input.BootID,
		validate: func(tx *transcriptstore.ImmediateTransaction, operation KernelLocalOperation) error {
			return validateKernelLocalOperationLiveClaim(ctx, tx, operation, input.Claim)
		},
		idempotent: func(operation KernelLocalOperation) bool {
			return operation.ExecutionID == input.ExecutionID && operation.RunnerID == input.Claim.RunnerID &&
				operation.RunnerAttempt == input.Claim.Attempt && operation.RunnerClaimSHA256 == claimSHA &&
				operation.BootID == input.BootID
		},
		after: after,
	})
	if err == nil {
		s.signalKernelRetentionWake()
	}
	return operation, err
}

func (s *Store) ReclaimPreparedKernelLocalOperation(
	ctx context.Context,
	input ReclaimPreparedKernelLocalOperationInput,
) (KernelLocalOperation, error) {
	input.OwnerUserID = strings.TrimSpace(input.OwnerUserID)
	input.OperationID = strings.TrimSpace(input.OperationID)
	input.RunnerID = strings.TrimSpace(input.RunnerID)
	input.RunnerClaimSHA256 = strings.TrimSpace(input.RunnerClaimSHA256)
	input.BootID = strings.TrimSpace(input.BootID)
	var err error
	input.ReasonCode, err = normalizeKernelLocalOperationReason(input.ReasonCode)
	if err != nil {
		return KernelLocalOperation{}, err
	}
	if s == nil || input.OwnerUserID == "" || input.OperationID == "" || input.ExpectedStateVersion <= 0 ||
		input.RunnerID == "" || input.RunnerAttempt <= 0 || !validLowerHexSHA256(input.RunnerClaimSHA256) || input.BootID == "" ||
		input.ReasonCode == "" {
		return KernelLocalOperation{}, errors.New("complete stale prepared operation authority is required")
	}
	operation, err := s.transitionKernelLocalOperation(ctx, kernelLocalOperationTransitionInput{
		ownerUserID: input.OwnerUserID, operationID: input.OperationID,
		expectedState: KernelLocalOperationStatePrepared, expectedVersion: input.ExpectedStateVersion,
		targetState: KernelLocalOperationStateApproved, reasonCode: input.ReasonCode,
		setClause: `runner_id=NULL,runner_attempt=NULL,runner_claim_sha256=NULL,boot_id=NULL,
			kernel_id=NULL,kernel_generation=NULL,prepared_at=NULL`,
		extraPredicate: `AND runner_id=? AND runner_attempt=? AND runner_claim_sha256=? AND boot_id=? AND execution_id IS NULL`,
		extraArgs:      []any{input.RunnerID, input.RunnerAttempt, input.RunnerClaimSHA256, input.BootID},
		runnerAttempt:  input.RunnerAttempt, runnerClaimSHA256: input.RunnerClaimSHA256, bootID: input.BootID,
		validate: func(tx *transcriptstore.ImmediateTransaction, operation KernelLocalOperation) error {
			if err := validateKernelLocalOperationCurrentAuthority(ctx, tx, operation, true); err != nil {
				return err
			}
			live, err := kernelLocalOperationAttemptIsLive(ctx, tx, operation, s.now().UTC())
			if err != nil {
				return err
			}
			if live {
				return ErrKernelLocalOperationStale
			}
			return nil
		},
		before: func(tx *transcriptstore.ImmediateTransaction, operation KernelLocalOperation) (string, []any, error) {
			now := s.now().UTC()
			var revision int64
			if err := tx.QueryRowContext(ctx, `UPDATE transcript_streams
				SET input_revision=input_revision+1,updated_at=? WHERE stream_uid=?
				RETURNING input_revision`, now, operation.StreamUID).Scan(&revision); err != nil {
				return "", nil, err
			}
			if revision <= operation.AdmittedInputRevision {
				return "", nil, ErrKernelLocalOperationConflict
			}
			if err := reactivateKernelOperationFrameForRecoveryTx(ctx, tx, operation, now); err != nil {
				return "", nil, err
			}
			return "admitted_input_revision=?", []any{revision}, nil
		},
		idempotent: func(operation KernelLocalOperation) bool {
			return operation.RunnerID == "" && operation.ExecutionID == "" &&
				operation.ReasonCode == input.ReasonCode
		},
	})
	if err == nil {
		s.signalKernelRetentionWake()
	}
	return operation, err
}

// ReleasePreparedKernelLocalOperation returns a prepared operation to its
// approved boundary when infrastructure setup failed before an execution was
// durably accepted. The same live runner claim that prepared the operation is
// required, so this transition cannot rewind another worker's side effect.
// Unlike stale-runner reclamation it does not advance the input revision: the
// admitted tool call is unchanged and may be retried by durable recovery.
func (s *Store) ReleasePreparedKernelLocalOperation(
	ctx context.Context,
	input ReleasePreparedKernelLocalOperationInput,
) (KernelLocalOperation, error) {
	input.OwnerUserID = strings.TrimSpace(input.OwnerUserID)
	input.OperationID = strings.TrimSpace(input.OperationID)
	input.BootID = strings.TrimSpace(input.BootID)
	var err error
	input.ReasonCode, err = normalizeKernelLocalOperationReason(input.ReasonCode)
	if err != nil {
		return KernelLocalOperation{}, err
	}
	if s == nil || input.OwnerUserID == "" || input.OperationID == "" || input.ExpectedStateVersion <= 0 ||
		strings.TrimSpace(input.Claim.RunnerID) == "" || input.Claim.Attempt <= 0 ||
		strings.TrimSpace(input.Claim.ClaimToken) == "" || input.BootID == "" || input.ReasonCode == "" {
		return KernelLocalOperation{}, errors.New("complete prepared operation release authority is required")
	}
	claimSHA := kernelLocalOperationClaimSHA256(input.Claim.ClaimToken)
	operation, err := s.transitionKernelLocalOperation(ctx, kernelLocalOperationTransitionInput{
		ownerUserID: input.OwnerUserID, operationID: input.OperationID,
		expectedState: KernelLocalOperationStatePrepared, expectedVersion: input.ExpectedStateVersion,
		targetState: KernelLocalOperationStateApproved, reasonCode: input.ReasonCode,
		setClause: `runner_id=NULL,runner_attempt=NULL,runner_claim_sha256=NULL,boot_id=NULL,
			kernel_id=NULL,kernel_generation=NULL,prepared_at=NULL`,
		extraPredicate: `AND runner_id=? AND runner_attempt=? AND runner_claim_sha256=? AND boot_id=? AND execution_id IS NULL`,
		extraArgs:      []any{input.Claim.RunnerID, input.Claim.Attempt, claimSHA, input.BootID},
		runnerAttempt:  input.Claim.Attempt, runnerClaimSHA256: claimSHA, bootID: input.BootID,
		validate: func(tx *transcriptstore.ImmediateTransaction, operation KernelLocalOperation) error {
			return validateKernelLocalOperationLiveClaim(ctx, tx, operation, input.Claim)
		},
		idempotent: func(operation KernelLocalOperation) bool {
			return operation.RunnerID == "" && operation.ExecutionID == "" &&
				operation.ReasonCode == input.ReasonCode
		},
	})
	if err == nil {
		s.signalKernelRetentionWake()
	}
	return operation, err
}

func (s *Store) MarkKernelLocalOperationOutcomeUnknown(
	ctx context.Context,
	input MarkKernelLocalOperationOutcomeUnknownInput,
) (KernelLocalOperation, error) {
	input.OwnerUserID = strings.TrimSpace(input.OwnerUserID)
	input.OperationID = strings.TrimSpace(input.OperationID)
	input.ExecutionID = strings.TrimSpace(input.ExecutionID)
	input.CurrentBootID = strings.TrimSpace(input.CurrentBootID)
	var err error
	input.ReasonCode, err = normalizeKernelLocalOperationReason(input.ReasonCode)
	if err != nil {
		return KernelLocalOperation{}, err
	}
	if s == nil || input.OwnerUserID == "" || input.OperationID == "" || input.ExpectedStateVersion <= 0 ||
		input.ExecutionID == "" || input.CurrentBootID == "" || input.ReasonCode == "" {
		return KernelLocalOperation{}, errors.New("complete unknown-outcome authority is required")
	}
	unknownResult, err := json.Marshal(map[string]any{
		"ok": false, "error": map[string]any{
			"code": "execution_outcome_unknown", "message": "Kernel execution outcome is unknown after service restart; it was not retried.",
		},
	})
	if err != nil {
		return KernelLocalOperation{}, err
	}
	unknownDigest := sha256.Sum256(unknownResult)
	operation, err := s.transitionKernelLocalOperation(ctx, kernelLocalOperationTransitionInput{
		ownerUserID: input.OwnerUserID, operationID: input.OperationID,
		expectedState: KernelLocalOperationStateStarted, expectedVersion: input.ExpectedStateVersion,
		targetState: KernelLocalOperationStateOutcomeUnknown, reasonCode: input.ReasonCode,
		setClause: `terminal_at=?,result_json=?,result_sha256=?`, setArgs: []any{
			s.now().UTC().Format(time.RFC3339Nano), string(unknownResult), hex.EncodeToString(unknownDigest[:]),
		},
		extraPredicate: `AND execution_id=?`, extraArgs: []any{input.ExecutionID},
		validate: func(tx *transcriptstore.ImmediateTransaction, operation KernelLocalOperation) error {
			if operation.BootID == input.CurrentBootID {
				return ErrKernelLocalOperationStale
			}
			live, err := kernelLocalOperationAttemptIsLive(ctx, tx, operation, s.now().UTC())
			if err != nil {
				return err
			}
			if live {
				return ErrKernelLocalOperationStale
			}
			return nil
		},
		before: func(tx *transcriptstore.ImmediateTransaction, operation KernelLocalOperation) (string, []any, error) {
			now := s.now().UTC()
			var frameStatus string
			if err := tx.QueryRowContext(ctx, `SELECT status FROM frames
				WHERE id=? AND project_id=? AND incarnation_id=?`, operation.FrameID, operation.ProjectID,
				operation.FrameIncarnationID).Scan(&frameStatus); err != nil {
				return "", nil, err
			}
			if !kernelOperationFrameAllowsRecovery(frameStatus) {
				// The operation still needs a terminal evidence record, but a
				// completed or user-cancelled task must never be re-enqueued merely
				// because its detached executor disappeared during settlement.
				return "", nil, nil
			}
			var revision int64
			if err := tx.QueryRowContext(ctx, `UPDATE transcript_streams
				SET input_revision=input_revision+1,updated_at=? WHERE stream_uid=?
				RETURNING input_revision`, now, operation.StreamUID).Scan(&revision); err != nil {
				return "", nil, err
			}
			if revision <= operation.AdmittedInputRevision {
				return "", nil, ErrKernelLocalOperationConflict
			}
			if err := reactivateKernelOperationFrameForRecoveryTx(ctx, tx, operation, now); err != nil {
				return "", nil, err
			}
			return "admitted_input_revision=?", []any{revision}, nil
		},
		idempotent: func(operation KernelLocalOperation) bool {
			return operation.ExecutionID == input.ExecutionID && operation.ReasonCode == input.ReasonCode &&
				bytes.Equal(operation.ResultJSON, unknownResult) && operation.ResultSHA256 == hex.EncodeToString(unknownDigest[:])
		},
		after: func(tx *transcriptstore.ImmediateTransaction, operation KernelLocalOperation) error {
			materialization, err := normalizeKernelToolResultMaterialization(
				operation.OperationID, unknownResult, "", "", "native_v41", operation.UpdatedAt,
			)
			if err != nil {
				return err
			}
			if err := insertKernelToolResultMaterializationTx(ctx, tx, materialization); err != nil {
				return err
			}
			if _, err := appendKernelLocalOperationTerminalEvent(ctx, s, tx, operation, operation.UpdatedAt); err != nil {
				return err
			}
			var input map[string]any
			if err := json.Unmarshal(operation.InputJSON, &input); err != nil {
				return ErrKernelLocalOperationConflict
			}
			background, _ := input["background"].(bool)
			if !background {
				return nil
			}
			_, _, err = createNotificationTx(ctx, tx, CreateNotificationInput{
				ID:            "kernel-operation-unknown:" + operation.OperationID,
				SenderFrameID: operation.FrameID, RecipientFrameID: operation.FrameID,
				RootFrameID: operation.RootFrameID, OwnerUserID: operation.OwnerUserID,
				NotificationType: "cell_result", Payload: map[string]any{
					"status": "outcome_unknown", "operation_id": operation.OperationID,
					"exec_id": operation.ExecutionID, "tool_call_id": operation.ToolCallID,
					"error": "Execution outcome is unknown after service restart and was not retried.",
				},
			}, operation.UpdatedAt)
			return err
		},
	})
	if err == nil {
		s.signalKernelRetentionWake()
	}
	return operation, err
}

func kernelOperationFrameAllowsRecovery(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case FrameStatusCompleted, FrameStatusCancelled, "canceled":
		return false
	default:
		return true
	}
}

func reactivateKernelOperationFrameForRecoveryTx(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	operation KernelLocalOperation,
	now time.Time,
) error {
	result, err := tx.ExecContext(ctx, `UPDATE frames SET status=?,updated_at=?
		WHERE id=? AND project_id=? AND incarnation_id=? AND status=?`,
		FrameStatusProcessing, now, operation.FrameID, operation.ProjectID,
		operation.FrameIncarnationID, FrameStatusFailed)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed > 1 {
		return ErrKernelLocalOperationConflict
	}
	return nil
}

type kernelLocalOperationTransitionInput struct {
	ownerUserID, operationID, expectedState, targetState string
	expectedVersion                                      int64
	reasonCode, setClause, extraPredicate                string
	setArgs, extraArgs                                   []any
	runnerAttempt                                        int64
	runnerClaimSHA256, bootID                            string
	validate                                             func(*transcriptstore.ImmediateTransaction, KernelLocalOperation) error
	before                                               func(*transcriptstore.ImmediateTransaction, KernelLocalOperation) (string, []any, error)
	idempotent                                           func(KernelLocalOperation) bool
	after                                                func(*transcriptstore.ImmediateTransaction, KernelLocalOperation) error
}

func (s *Store) transitionKernelLocalOperation(
	ctx context.Context,
	input kernelLocalOperationTransitionInput,
) (KernelLocalOperation, error) {
	repository, err := s.TranscriptRepository(ctx)
	if err != nil {
		return KernelLocalOperation{}, err
	}
	var operation KernelLocalOperation
	err = repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		current, found, err := getKernelLocalOperationQuery(ctx, tx, input.operationID)
		if err != nil {
			return err
		}
		if !found || current.OwnerUserID != input.ownerUserID {
			return ErrKernelLocalOperationConflict
		}
		if current.State != input.expectedState || current.StateVersion != input.expectedVersion {
			if current.State == input.targetState && current.StateVersion == input.expectedVersion+1 &&
				input.idempotent != nil && input.idempotent(current) {
				operation = current
				return nil
			}
			return ErrKernelLocalOperationStale
		}
		if input.validate != nil {
			if err := input.validate(tx, current); err != nil {
				return err
			}
		}
		if input.before != nil {
			extraSetClause, extraSetArgs, err := input.before(tx, current)
			if err != nil {
				return err
			}
			extraSetClause = strings.TrimSpace(extraSetClause)
			if extraSetClause != "" {
				if strings.TrimSpace(input.setClause) != "" {
					input.setClause += ","
				}
				input.setClause += extraSetClause
				input.setArgs = append(input.setArgs, extraSetArgs...)
			}
		}
		now := s.now().UTC().Format(time.RFC3339Nano)
		setClause := strings.TrimSpace(input.setClause)
		if setClause != "" {
			setClause += ","
		}
		query := `UPDATE kernel_local_operations SET ` + setClause + `state=?,state_version=state_version+1,
			reason_code=?,updated_at=? WHERE operation_id=? AND owner_user_id=? AND state=? AND state_version=? ` + input.extraPredicate
		args := append([]any{}, input.setArgs...)
		args = append(args, input.targetState, input.reasonCode, now, input.operationID, input.ownerUserID,
			input.expectedState, input.expectedVersion)
		args = append(args, input.extraArgs...)
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rows != 1 {
			return ErrKernelLocalOperationStale
		}
		var loaded bool
		operation, loaded, err = getKernelLocalOperationQuery(ctx, tx, input.operationID)
		if err != nil {
			return err
		}
		if !loaded {
			return ErrKernelLocalOperationConflict
		}
		if input.after != nil {
			if err := input.after(tx, operation); err != nil {
				return err
			}
		}
		return nil
	})
	return operation, err
}

func normalizeKernelLocalOperationReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if len(reason) > 128 {
		return "", errors.New("kernel local operation reason code is invalid")
	}
	for _, char := range reason {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' {
			return "", errors.New("kernel local operation reason code is invalid")
		}
	}
	return reason, nil
}

func validLowerHexSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func validKernelLocalOperationApprovalScope(value string) bool {
	return value == "once" || value == "conversation" || value == "project" || value == "always"
}

func validKernelLocalOperationApprovalSource(value string) bool {
	return value == "user" || value == "policy" || value == "remembered" || value == "stale_reconcile"
}

func kernelLocalOperationClaimSHA256(claimToken string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(claimToken)))
	return hex.EncodeToString(digest[:])
}

func validateKernelLocalOperationLiveClaim(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	operation KernelLocalOperation,
	claim transcriptstore.RunnerClaim,
) error {
	stream, err := tx.ValidateLiveRunnerClaim(ctx, claim)
	if err != nil {
		return err
	}
	if stream.UID != operation.StreamUID || stream.OwnerID != operation.OwnerUserID ||
		stream.ProjectID != operation.ProjectID || stream.RootFrameID != operation.RootFrameID ||
		stream.FrameID != operation.FrameID {
		return ErrKernelLocalOperationConflict
	}
	return validateKernelLocalOperationCurrentAuthority(ctx, tx, operation, true)
}

func validateKernelLocalOperationCurrentAuthority(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	operation KernelLocalOperation,
	requireActiveBranch bool,
) error {
	var ownerID, projectID, rootFrameID, rootIncarnationID, frameID, frameIncarnationID string
	var branchID string
	var generation int64
	err := tx.QueryRowContext(ctx, `SELECT project.user_id,stream.project_id,stream.root_frame_id,root.incarnation_id,
		stream.frame_id,frame.incarnation_id,branch.active_branch_id,branch.generation
		FROM transcript_streams stream
		JOIN projects project ON project.id=stream.project_id
		JOIN frames frame ON frame.id=stream.frame_id AND frame.project_id=stream.project_id
		JOIN frames root ON root.id=stream.root_frame_id AND root.project_id=stream.project_id
		JOIN transcript_branch_state branch ON branch.stream_uid=stream.stream_uid
		WHERE stream.stream_uid=?`, operation.StreamUID).Scan(
		&ownerID, &projectID, &rootFrameID, &rootIncarnationID, &frameID, &frameIncarnationID,
		&branchID, &generation,
	)
	if err != nil {
		return err
	}
	if ownerID != operation.OwnerUserID || projectID != operation.ProjectID || rootFrameID != operation.RootFrameID ||
		rootIncarnationID != operation.RootFrameIncarnationID || frameID != operation.FrameID ||
		frameIncarnationID != operation.FrameIncarnationID {
		return ErrKernelLocalOperationConflict
	}
	if requireActiveBranch && (branchID != operation.BranchID || generation != operation.BranchGeneration) {
		return ErrKernelLocalOperationStale
	}
	belongs, err := kernelLocalOperationEventBelongsToBranch(ctx, tx, operation.StreamUID, operation.BranchID, operation.SourceEventID)
	if err != nil {
		return err
	}
	if !belongs {
		return ErrKernelLocalOperationConflict
	}
	return nil
}

func kernelLocalOperationAttemptIsLive(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	operation KernelLocalOperation,
	now time.Time,
) (bool, error) {
	if operation.RunnerID == "" || operation.RunnerAttempt <= 0 || operation.RunnerClaimSHA256 == "" {
		return false, ErrKernelLocalOperationConflict
	}
	var status string
	var expiresAt time.Time
	err := tx.QueryRowContext(ctx, `SELECT status,expires_at FROM transcript_runner_attempts
		WHERE stream_uid=? AND attempt=? AND runner_id=? AND lower(hex(claim_token_sha256))=?
		`, operation.StreamUID, operation.RunnerAttempt, operation.RunnerID, operation.RunnerClaimSHA256).Scan(&status, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return status == "running" && expiresAt.After(now), nil
}

type kernelLocalOperationQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getKernelLocalOperationBySourceQuery(
	ctx context.Context,
	query kernelLocalOperationQuery,
	streamUID string,
	sourceEventID, ordinal int64,
) (KernelLocalOperation, bool, error) {
	var operationID string
	err := query.QueryRowContext(ctx, `SELECT operation_id FROM kernel_local_operations
		WHERE stream_uid=? AND source_event_id=? AND tool_call_ordinal=?`, streamUID, sourceEventID, ordinal).Scan(&operationID)
	if errors.Is(err, sql.ErrNoRows) {
		return KernelLocalOperation{}, false, nil
	}
	if err != nil {
		return KernelLocalOperation{}, false, err
	}
	return getKernelLocalOperationQuery(ctx, query, operationID)
}

func getKernelLocalOperationByApprovalRequestQuery(
	ctx context.Context,
	query kernelLocalOperationQuery,
	ownerUserID, approvalRequestID string,
) (KernelLocalOperation, bool, error) {
	var operationID string
	err := query.QueryRowContext(ctx, `SELECT operation_id FROM kernel_local_operations
		WHERE owner_user_id=? AND approval_request_id=?`, ownerUserID, approvalRequestID).Scan(&operationID)
	if errors.Is(err, sql.ErrNoRows) {
		return KernelLocalOperation{}, false, nil
	}
	if err != nil {
		return KernelLocalOperation{}, false, err
	}
	return getKernelLocalOperationQuery(ctx, query, operationID)
}

func getKernelLocalOperationByToolCallQuery(
	ctx context.Context,
	query kernelLocalOperationQuery,
	ownerUserID, streamUID, toolCallID string,
) (KernelLocalOperation, bool, error) {
	var operationID string
	err := query.QueryRowContext(ctx, `SELECT operation_id FROM kernel_local_operations
		WHERE owner_user_id=? AND stream_uid=? AND tool_call_id=?
		ORDER BY source_event_id DESC,tool_call_ordinal DESC LIMIT 1`,
		ownerUserID, streamUID, toolCallID).Scan(&operationID)
	if errors.Is(err, sql.ErrNoRows) {
		return KernelLocalOperation{}, false, nil
	}
	if err != nil {
		return KernelLocalOperation{}, false, err
	}
	return getKernelLocalOperationQuery(ctx, query, operationID)
}

func kernelLocalOperationEventBelongsToBranch(
	ctx context.Context,
	query kernelLocalOperationQuery,
	streamUID, branchID string,
	eventID int64,
) (bool, error) {
	var found int
	err := query.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcript_branch_events
		WHERE stream_uid=? AND branch_id=? AND event_id=?`, streamUID, branchID, eventID).Scan(&found)
	return found == 1, err
}

func getKernelLocalOperationQuery(
	ctx context.Context,
	query kernelLocalOperationQuery,
	operationID string,
) (KernelLocalOperation, bool, error) {
	var operation KernelLocalOperation
	var confinement, approvalRequest, decisionID, decision, scope, approvalSource, actorID sql.NullString
	var runnerID, runnerClaim, bootID, kernelID sql.NullString
	var executionID, executionLogID, resultJSON, resultRef, resultSHA sql.NullString
	var admittedInputRevision, runnerAttempt, kernelGeneration sql.NullInt64
	var createdAt, updatedAt string
	var approvedAt, decidedAt, preparedAt, startedAt, terminalAt sql.NullString
	err := query.QueryRowContext(ctx, `SELECT operation_id,owner_user_id,project_id,root_frame_id,root_frame_incarnation_id,
		frame_id,frame_incarnation_id,stream_uid,branch_id,branch_generation,source_event_id,source_publication_seq,
		source_runner_attempt,source_client_message_id,tool_call_ordinal,tool_call_id,tool,environment,input_json,input_sha256,
		confinement_sha256,state,state_version,approval_request_id,approval_decision_id,approval_decision,approval_scope,
		approval_source,approval_actor_id,decided_at,admitted_input_revision,runner_id,runner_attempt,runner_claim_sha256,boot_id,
		kernel_id,kernel_generation,execution_id,execution_log_id,result_json,result_ref,result_sha256,reason_code,
		created_at,approved_at,prepared_at,started_at,terminal_at,updated_at
		FROM kernel_local_operations WHERE operation_id=?`, operationID).Scan(
		&operation.OperationID, &operation.OwnerUserID, &operation.ProjectID, &operation.RootFrameID,
		&operation.RootFrameIncarnationID, &operation.FrameID, &operation.FrameIncarnationID, &operation.StreamUID,
		&operation.BranchID, &operation.BranchGeneration, &operation.SourceEventID, &operation.SourcePublicationSeq,
		&operation.SourceRunnerAttempt, &operation.SourceClientMessageID, &operation.ToolCallOrdinal, &operation.ToolCallID,
		&operation.Tool, &operation.Environment, &operation.InputJSON, &operation.InputSHA256, &confinement,
		&operation.State, &operation.StateVersion, &approvalRequest, &decisionID, &decision, &scope, &approvalSource,
		&actorID, &decidedAt, &admittedInputRevision, &runnerID, &runnerAttempt, &runnerClaim, &bootID,
		&kernelID, &kernelGeneration, &executionID, &executionLogID, &resultJSON, &resultRef, &resultSHA,
		&operation.ReasonCode, &createdAt, &approvedAt, &preparedAt, &startedAt, &terminalAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return KernelLocalOperation{}, false, nil
	}
	if err != nil {
		return KernelLocalOperation{}, false, err
	}
	operation.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return KernelLocalOperation{}, false, ErrKernelLocalOperationConflict
	}
	operation.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return KernelLocalOperation{}, false, ErrKernelLocalOperationConflict
	}
	operation.ConfinementSHA256 = confinement.String
	operation.ApprovalRequestID = approvalRequest.String
	operation.ApprovalDecisionID, operation.ApprovalDecision = decisionID.String, decision.String
	operation.ApprovalScope, operation.ApprovalSource, operation.ApprovalActorID = scope.String, approvalSource.String, actorID.String
	operation.AdmittedInputRevision = admittedInputRevision.Int64
	operation.RunnerID, operation.RunnerAttempt = runnerID.String, runnerAttempt.Int64
	operation.RunnerClaimSHA256, operation.BootID = runnerClaim.String, bootID.String
	operation.KernelID, operation.KernelGeneration = kernelID.String, kernelGeneration.Int64
	operation.ExecutionID, operation.ExecutionLogID = executionID.String, executionLogID.String
	operation.ResultJSON, operation.ExecutionLogRef, operation.ResultSHA256 = []byte(resultJSON.String), resultRef.String, resultSHA.String
	operation.ApprovedAt, err = parseKernelLocalOperationTime(approvedAt)
	if err != nil {
		return KernelLocalOperation{}, false, err
	}
	operation.DecidedAt, err = parseKernelLocalOperationTime(decidedAt)
	if err != nil {
		return KernelLocalOperation{}, false, err
	}
	operation.PreparedAt, err = parseKernelLocalOperationTime(preparedAt)
	if err != nil {
		return KernelLocalOperation{}, false, err
	}
	operation.StartedAt, err = parseKernelLocalOperationTime(startedAt)
	if err != nil {
		return KernelLocalOperation{}, false, err
	}
	operation.TerminalAt, err = parseKernelLocalOperationTime(terminalAt)
	if err != nil {
		return KernelLocalOperation{}, false, err
	}
	return operation, true, nil
}

func parseKernelLocalOperationTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil, ErrKernelLocalOperationConflict
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

func kernelLocalOperationOriginMatches(left, right KernelLocalOperation) bool {
	return left.OperationID == right.OperationID && left.OwnerUserID == right.OwnerUserID &&
		left.ProjectID == right.ProjectID && left.RootFrameID == right.RootFrameID &&
		left.RootFrameIncarnationID == right.RootFrameIncarnationID && left.FrameID == right.FrameID &&
		left.FrameIncarnationID == right.FrameIncarnationID && left.StreamUID == right.StreamUID &&
		left.BranchID == right.BranchID && left.BranchGeneration == right.BranchGeneration &&
		left.SourceEventID == right.SourceEventID && left.SourcePublicationSeq == right.SourcePublicationSeq &&
		left.SourceRunnerAttempt == right.SourceRunnerAttempt &&
		left.SourceClientMessageID == right.SourceClientMessageID && left.ToolCallOrdinal == right.ToolCallOrdinal &&
		left.ToolCallID == right.ToolCallID && left.Tool == right.Tool && left.Environment == right.Environment &&
		bytes.Equal(left.InputJSON, right.InputJSON) && left.InputSHA256 == right.InputSHA256 &&
		left.ApprovalRequestID == right.ApprovalRequestID
}

func formatKernelLocalOperationConflict(operationID string) error {
	return fmt.Errorf("%w: %s", ErrKernelLocalOperationConflict, strings.TrimSpace(operationID))
}

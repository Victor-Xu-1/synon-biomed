package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	KernelExecutionHostCallPending        = "pending"
	KernelExecutionHostCallExecuting      = "executing"
	KernelExecutionHostCallCompleted      = "completed"
	KernelExecutionHostCallFailed         = "failed"
	KernelExecutionHostCallOutcomeUnknown = "outcome_unknown"
)

var ErrKernelExecutionHostCallConflict = errors.New("kernel execution host call conflicts with durable state")

type KernelExecutionHostCallRequestV1 struct {
	Version    int            `json:"version"`
	HostCallID string         `json:"host_call_id"`
	CellID     string         `json:"cell_id"`
	Method     string         `json:"method"`
	Args       []any          `json:"args"`
	Kwargs     map[string]any `json:"kwargs"`
}

type KernelExecutionHostCall struct {
	ExecutionID      string
	HostCallID       string
	Ordinal          int64
	Method           string
	RequestJSON      string
	RequestSHA256    string
	State            string
	StateVersion     int64
	ClaimEpoch       int64
	ClaimTokenSHA256 []byte
	ClaimExpiresAt   *time.Time
	ResultJSON       string
	ResultRef        string
	ResultSHA256     string
	ReasonCode       string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	TerminalAt       *time.Time
}

type CreateKernelExecutionHostCallInput struct {
	ExecutionID        string
	BackendGeneration  int64
	ExecutorInstanceID string
	Ordinal            int64
	Request            KernelExecutionHostCallRequestV1
}

type ClaimKernelExecutionHostCallInput struct {
	ExecutionID       string
	HostCallID        string
	BackendGeneration int64
	ControllerEpoch   int64
	ControllerToken   string
	ExpectedVersion   int64
	ClaimToken        string
	ClaimExpiresAt    time.Time
}

type CompleteKernelExecutionHostCallInput struct {
	ExecutionID     string
	HostCallID      string
	ClaimEpoch      int64
	ClaimToken      string
	ExpectedVersion int64
	State           string
	ResultJSON      string
	ResultRef       string
	ResultSHA256    string
	ReasonCode      string
}

func (s *Store) CreateKernelExecutionHostCall(
	ctx context.Context,
	input CreateKernelExecutionHostCallInput,
) (KernelExecutionHostCall, error) {
	input.ExecutionID = strings.TrimSpace(input.ExecutionID)
	input.ExecutorInstanceID = strings.TrimSpace(input.ExecutorInstanceID)
	request, requestJSON, requestSHA256, err := canonicalKernelExecutionHostCallRequest(input.Request)
	input.Request = request
	if s == nil || s.db == nil || ctx == nil || !validDetachedIdentity(input.ExecutionID) ||
		input.BackendGeneration <= 0 || !validDetachedIdentity(input.ExecutorInstanceID) ||
		input.Ordinal < 0 || input.Ordinal >= 32 || err != nil || input.Request.CellID != input.ExecutionID {
		return KernelExecutionHostCall{}, errors.New("complete kernel execution host call identity is required")
	}
	repository, err := s.TranscriptRepository(ctx)
	if err != nil {
		return KernelExecutionHostCall{}, err
	}
	now := s.now().UTC()
	var result KernelExecutionHostCall
	err = repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		execution, found, queryErr := getDetachedKernelExecutionQuery(ctx, tx, input.ExecutionID)
		if queryErr != nil {
			return queryErr
		}
		if !found || execution.State != DetachedKernelExecutionStateStarted ||
			execution.BackendGeneration != input.BackendGeneration {
			return ErrKernelExecutionHostCallConflict
		}
		if executorErr := validateKernelExecutionExecutorTx(ctx, tx, execution, input.BackendGeneration,
			input.ExecutorInstanceID); executorErr != nil {
			return executorErr
		}
		_, insertErr := tx.ExecContext(ctx, `INSERT INTO kernel_execution_host_calls(
			execution_id,host_call_id,ordinal,method,request_json,request_sha256,
			state,state_version,created_at,updated_at
		) VALUES(?,?,?,?,?,?,?,1,?,?) ON CONFLICT(execution_id,host_call_id) DO NOTHING`,
			input.ExecutionID, input.Request.HostCallID, input.Ordinal, input.Request.Method,
			requestJSON, requestSHA256, KernelExecutionHostCallPending,
			now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		if insertErr != nil {
			return insertErr
		}
		result, found, queryErr = getKernelExecutionHostCallQuery(ctx, tx, input.ExecutionID, input.Request.HostCallID)
		if queryErr != nil {
			return queryErr
		}
		if !found || result.Ordinal != input.Ordinal || result.Method != input.Request.Method ||
			result.RequestJSON != requestJSON || result.RequestSHA256 != requestSHA256 {
			return ErrKernelExecutionHostCallConflict
		}
		return nil
	})
	if err == nil {
		s.signalKernelRetentionWake()
	}
	return result, err
}

func (s *Store) ClaimKernelExecutionHostCall(
	ctx context.Context,
	input ClaimKernelExecutionHostCallInput,
) (KernelExecutionHostCall, error) {
	normalizeKernelExecutionHostCallClaim(&input)
	now := s.now().UTC()
	if s == nil || s.db == nil || ctx == nil || !validDetachedIdentity(input.ExecutionID) ||
		!validDetachedIdentity(input.HostCallID) || input.BackendGeneration <= 0 || input.ControllerEpoch <= 0 ||
		!validKernelExecutionControlToken(input.ControllerToken) || input.ExpectedVersion <= 0 ||
		!validKernelExecutionControlToken(input.ClaimToken) || !input.ClaimExpiresAt.After(now) {
		return KernelExecutionHostCall{}, errors.New("complete kernel execution host call claim is required")
	}
	repository, err := s.TranscriptRepository(ctx)
	if err != nil {
		return KernelExecutionHostCall{}, err
	}
	claimDigest := sha256.Sum256([]byte(input.ClaimToken))
	var result KernelExecutionHostCall
	err = repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		execution, found, queryErr := getDetachedKernelExecutionQuery(ctx, tx, input.ExecutionID)
		if queryErr != nil {
			return queryErr
		}
		if !found || execution.BackendGeneration != input.BackendGeneration ||
			(execution.State != DetachedKernelExecutionStateStarted &&
				execution.State != DetachedKernelExecutionStateCancelRequested) {
			return ErrKernelExecutionHostCallConflict
		}
		if controlErr := validateKernelExecutionControllerTx(ctx, tx, execution, input.BackendGeneration,
			input.ControllerEpoch, input.ControllerToken, now); controlErr != nil {
			return controlErr
		}
		current, found, queryErr := getKernelExecutionHostCallQuery(ctx, tx, input.ExecutionID, input.HostCallID)
		if queryErr != nil {
			return queryErr
		}
		if !found {
			return ErrKernelExecutionHostCallConflict
		}
		if current.State == KernelExecutionHostCallExecuting && current.ClaimEpoch == input.ControllerEpoch &&
			subtle.ConstantTimeCompare(current.ClaimTokenSHA256, claimDigest[:]) == 1 &&
			current.ClaimExpiresAt != nil && current.ClaimExpiresAt.Equal(input.ClaimExpiresAt) {
			result = current
			return nil
		}
		if current.State != KernelExecutionHostCallPending || current.StateVersion != input.ExpectedVersion {
			return ErrKernelExecutionHostCallConflict
		}
		update, updateErr := tx.ExecContext(ctx, `UPDATE kernel_execution_host_calls SET
			state=?,state_version=state_version+1,claim_epoch=?,claim_token_sha256=?,claim_expires_at=?,updated_at=?
			WHERE execution_id=? AND host_call_id=? AND state='pending' AND state_version=?`,
			KernelExecutionHostCallExecuting, input.ControllerEpoch, claimDigest[:],
			input.ClaimExpiresAt.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
			input.ExecutionID, input.HostCallID, current.StateVersion)
		if updateErr != nil {
			return updateErr
		}
		if rows, rowsErr := update.RowsAffected(); rowsErr != nil || rows != 1 {
			if rowsErr != nil {
				return rowsErr
			}
			return ErrKernelExecutionHostCallConflict
		}
		result, _, queryErr = getKernelExecutionHostCallQuery(ctx, tx, input.ExecutionID, input.HostCallID)
		return queryErr
	})
	return result, err
}

func (s *Store) CompleteKernelExecutionHostCall(
	ctx context.Context,
	input CompleteKernelExecutionHostCallInput,
) (KernelExecutionHostCall, error) {
	normalizeCompleteKernelExecutionHostCall(&input)
	if s == nil || s.db == nil || ctx == nil || !validDetachedIdentity(input.ExecutionID) ||
		!validDetachedIdentity(input.HostCallID) || input.ClaimEpoch <= 0 ||
		!validKernelExecutionControlToken(input.ClaimToken) || input.ExpectedVersion <= 0 ||
		(input.State != KernelExecutionHostCallCompleted && input.State != KernelExecutionHostCallFailed &&
			input.State != KernelExecutionHostCallOutcomeUnknown) ||
		!validKernelExecutionHostCallResult(input.ResultJSON, input.ResultSHA256, input.ResultRef) ||
		len(input.ReasonCode) > 256 || strings.ContainsAny(input.ReasonCode, "\x00\r\n") {
		return KernelExecutionHostCall{}, errors.New("complete kernel execution host call result is required")
	}
	repository, err := s.TranscriptRepository(ctx)
	if err != nil {
		return KernelExecutionHostCall{}, err
	}
	claimDigest := sha256.Sum256([]byte(input.ClaimToken))
	now := s.now().UTC()
	var result KernelExecutionHostCall
	err = repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		current, found, queryErr := getKernelExecutionHostCallQuery(ctx, tx, input.ExecutionID, input.HostCallID)
		if queryErr != nil {
			return queryErr
		}
		if !found {
			return ErrKernelExecutionHostCallConflict
		}
		if current.State == input.State && current.ResultJSON == input.ResultJSON &&
			current.ResultRef == input.ResultRef && current.ResultSHA256 == input.ResultSHA256 &&
			current.ReasonCode == input.ReasonCode {
			result = current
			return nil
		}
		if current.State != KernelExecutionHostCallExecuting || current.StateVersion != input.ExpectedVersion ||
			current.ClaimEpoch != input.ClaimEpoch || subtle.ConstantTimeCompare(current.ClaimTokenSHA256, claimDigest[:]) != 1 {
			return ErrKernelExecutionHostCallConflict
		}
		update, updateErr := tx.ExecContext(ctx, `UPDATE kernel_execution_host_calls SET
			state=?,state_version=state_version+1,result_json=?,result_ref=?,result_sha256=?,reason_code=?,
			terminal_at=?,updated_at=? WHERE execution_id=? AND host_call_id=? AND state='executing' AND state_version=?`,
			input.State, input.ResultJSON, nullableDetachedText(input.ResultRef), input.ResultSHA256,
			input.ReasonCode, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
			input.ExecutionID, input.HostCallID, current.StateVersion)
		if updateErr != nil {
			return updateErr
		}
		if rows, rowsErr := update.RowsAffected(); rowsErr != nil || rows != 1 {
			if rowsErr != nil {
				return rowsErr
			}
			return ErrKernelExecutionHostCallConflict
		}
		result, _, queryErr = getKernelExecutionHostCallQuery(ctx, tx, input.ExecutionID, input.HostCallID)
		return queryErr
	})
	if err == nil {
		s.signalKernelRetentionWake()
	}
	return result, err
}

func (s *Store) GetKernelExecutionHostCall(
	ctx context.Context,
	executionID string,
	hostCallID string,
) (KernelExecutionHostCall, bool, error) {
	executionID = strings.TrimSpace(executionID)
	hostCallID = strings.TrimSpace(hostCallID)
	if s == nil || s.db == nil || ctx == nil || !validDetachedIdentity(executionID) ||
		!validDetachedIdentity(hostCallID) {
		return KernelExecutionHostCall{}, false, errors.New("kernel execution host call identity is required")
	}
	return getKernelExecutionHostCallQuery(ctx, s.db, executionID, hostCallID)
}

// ListKernelExecutionHostCalls returns the complete durable host-call ledger
// for one detached kernel execution in execution order. Review gates use this
// server-owned ledger instead of trusting model-authored stdout claims about
// which host methods ran.
func (s *Store) ListKernelExecutionHostCalls(
	ctx context.Context,
	executionID string,
) ([]KernelExecutionHostCall, error) {
	executionID = strings.TrimSpace(executionID)
	if s == nil || s.db == nil || ctx == nil || !validDetachedIdentity(executionID) {
		return nil, errors.New("kernel execution host call identity is required")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT execution_id,host_call_id,ordinal,method,request_json,
		request_sha256,state,state_version,claim_epoch,claim_token_sha256,claim_expires_at,
		result_json,result_ref,result_sha256,reason_code,created_at,updated_at,terminal_at
		FROM kernel_execution_host_calls WHERE execution_id=? ORDER BY ordinal,host_call_id`, executionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]KernelExecutionHostCall, 0)
	for rows.Next() {
		call, found, err := scanKernelExecutionHostCall(rows)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, ErrKernelExecutionHostCallConflict
		}
		result = append(result, call)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func canonicalKernelExecutionHostCallRequest(
	input KernelExecutionHostCallRequestV1,
) (KernelExecutionHostCallRequestV1, string, string, error) {
	input.HostCallID = strings.TrimSpace(input.HostCallID)
	input.CellID = strings.TrimSpace(input.CellID)
	input.Method = strings.TrimSpace(input.Method)
	if input.Args == nil {
		input.Args = []any{}
	}
	if input.Kwargs == nil {
		input.Kwargs = map[string]any{}
	}
	if input.Version != 1 || !validKernelExecutionHostCallID(input.HostCallID) ||
		!validDetachedIdentity(input.CellID) || len(input.CellID) > 128 ||
		!validDetachedIdentity(input.Method) || len(input.Method) > 128 || len(input.Kwargs) > 256 {
		return KernelExecutionHostCallRequestV1{}, "", "", errors.New("kernel execution host call request is invalid")
	}
	for key := range input.Kwargs {
		if !validDetachedIdentity(key) {
			return KernelExecutionHostCallRequestV1{}, "", "", errors.New("kernel execution host call keyword is invalid")
		}
	}
	encoded, err := json.Marshal(input)
	if err != nil || len(encoded) > 1048576 || !utf8.Valid(encoded) {
		return KernelExecutionHostCallRequestV1{}, "", "", errors.New("kernel execution host call request exceeds the durable limit")
	}
	digest := sha256.Sum256(encoded)
	return input, string(encoded), hex.EncodeToString(digest[:]), nil
}

func DecodeKernelExecutionHostCallRequestV1(raw string) (KernelExecutionHostCallRequestV1, error) {
	var request KernelExecutionHostCallRequestV1
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return KernelExecutionHostCallRequestV1{}, errors.New("kernel execution host call request is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return KernelExecutionHostCallRequestV1{}, errors.New("kernel execution host call request must contain one object")
	}
	normalized, canonical, _, err := canonicalKernelExecutionHostCallRequest(request)
	if err != nil || canonical != raw {
		return KernelExecutionHostCallRequestV1{}, errors.New("kernel execution host call request is not canonical")
	}
	return normalized, nil
}

func getKernelExecutionHostCallQuery(
	ctx context.Context,
	query detachedKernelExecutionQuery,
	executionID string,
	hostCallID string,
) (KernelExecutionHostCall, bool, error) {
	return scanKernelExecutionHostCall(query.QueryRowContext(ctx, `SELECT execution_id,host_call_id,ordinal,method,request_json,
		request_sha256,state,state_version,claim_epoch,claim_token_sha256,claim_expires_at,
		result_json,result_ref,result_sha256,reason_code,created_at,updated_at,terminal_at
		FROM kernel_execution_host_calls WHERE execution_id=? AND host_call_id=?`, executionID, hostCallID))
}

type kernelExecutionHostCallScanner interface {
	Scan(dest ...any) error
}

func scanKernelExecutionHostCall(scanner kernelExecutionHostCallScanner) (KernelExecutionHostCall, bool, error) {
	var call KernelExecutionHostCall
	var claimEpoch sql.NullInt64
	var claimDigest []byte
	var claimExpires, resultJSON, resultRef, resultSHA, terminalAt sql.NullString
	var createdAt, updatedAt string
	err := scanner.Scan(
		&call.ExecutionID, &call.HostCallID, &call.Ordinal, &call.Method, &call.RequestJSON,
		&call.RequestSHA256, &call.State, &call.StateVersion, &claimEpoch, &claimDigest, &claimExpires,
		&resultJSON, &resultRef, &resultSHA, &call.ReasonCode, &createdAt, &updatedAt, &terminalAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return KernelExecutionHostCall{}, false, nil
	}
	if err != nil {
		return KernelExecutionHostCall{}, false, err
	}
	call.ClaimEpoch, call.ClaimTokenSHA256 = claimEpoch.Int64, append([]byte(nil), claimDigest...)
	call.ResultJSON, call.ResultRef, call.ResultSHA256 = resultJSON.String, resultRef.String, resultSHA.String
	var parseErr error
	call.ClaimExpiresAt, parseErr = parseDetachedOptionalTime(claimExpires)
	if parseErr != nil {
		return KernelExecutionHostCall{}, false, parseErr
	}
	call.TerminalAt, parseErr = parseDetachedOptionalTime(terminalAt)
	if parseErr != nil {
		return KernelExecutionHostCall{}, false, parseErr
	}
	if call.CreatedAt, parseErr = time.Parse(time.RFC3339Nano, createdAt); parseErr != nil {
		return KernelExecutionHostCall{}, false, ErrKernelExecutionHostCallConflict
	}
	if call.UpdatedAt, parseErr = time.Parse(time.RFC3339Nano, updatedAt); parseErr != nil {
		return KernelExecutionHostCall{}, false, ErrKernelExecutionHostCallConflict
	}
	if call.RequestSHA256 != sha256HexString(call.RequestJSON) {
		return KernelExecutionHostCall{}, false, ErrKernelExecutionHostCallConflict
	}
	if _, decodeErr := DecodeKernelExecutionHostCallRequestV1(call.RequestJSON); decodeErr != nil {
		return KernelExecutionHostCall{}, false, ErrKernelExecutionHostCallConflict
	}
	return call, true, nil
}

func normalizeKernelExecutionHostCallClaim(input *ClaimKernelExecutionHostCallInput) {
	input.ExecutionID = strings.TrimSpace(input.ExecutionID)
	input.HostCallID = strings.TrimSpace(input.HostCallID)
	input.ControllerToken = strings.TrimSpace(input.ControllerToken)
	input.ClaimToken = strings.TrimSpace(input.ClaimToken)
	input.ClaimExpiresAt = input.ClaimExpiresAt.UTC()
}

func normalizeCompleteKernelExecutionHostCall(input *CompleteKernelExecutionHostCallInput) {
	input.ExecutionID = strings.TrimSpace(input.ExecutionID)
	input.HostCallID = strings.TrimSpace(input.HostCallID)
	input.ClaimToken = strings.TrimSpace(input.ClaimToken)
	input.State = strings.TrimSpace(input.State)
	input.ResultJSON = strings.TrimSpace(input.ResultJSON)
	input.ResultRef = strings.TrimSpace(input.ResultRef)
	input.ResultSHA256 = strings.TrimSpace(input.ResultSHA256)
	input.ReasonCode = strings.TrimSpace(input.ReasonCode)
}

func validKernelExecutionHostCallResult(resultJSON string, resultSHA256 string, resultRef string) bool {
	if len(resultJSON) == 0 || len(resultJSON) > 1048576 || !json.Valid([]byte(resultJSON)) ||
		!validLowerHexSHA256(resultSHA256) || resultSHA256 != sha256HexString(resultJSON) ||
		len(resultRef) > 4096 || strings.ContainsAny(resultRef, "\x00\r\n") {
		return false
	}
	return resultRef == "" || strings.HasPrefix(resultRef, "artifact-version:")
}

func validKernelExecutionHostCallID(value string) bool {
	if len(value) != 35 || !strings.HasPrefix(value, "hc-") {
		return false
	}
	for _, character := range value[3:] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

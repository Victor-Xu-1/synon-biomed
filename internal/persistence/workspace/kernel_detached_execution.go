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
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"synon-go/internal/kernelcontract"
	"synon-go/internal/networkpolicy"
	"synon-go/internal/networktls"
	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	KernelExecutionBackendStateStarting     = "starting"
	KernelExecutionBackendStateReady        = "ready"
	KernelExecutionBackendStateDraining     = "draining"
	KernelExecutionBackendStateStopped      = "stopped"
	KernelExecutionBackendStateEvidenceLost = "evidence_lost"

	DetachedKernelExecutionStateAccepted          = "accepted"
	DetachedKernelExecutionStateDispatchCommitted = "dispatch_committed"
	DetachedKernelExecutionStateStarted           = "started"
	DetachedKernelExecutionStateCancelRequested   = "cancel_requested"
	DetachedKernelExecutionStateTerminal          = "terminal"
	DetachedKernelExecutionStateEvidenceLost      = "evidence_lost"

	// Provider cancellation confirms the execution owner accepted cancellation,
	// not that a particular operating-system signal was sent.
	KernelExecutionCancelProvider = "provider_cancel"
)

var (
	ErrKernelExecutionBackendConflict  = errors.New("kernel execution backend conflicts with durable state")
	ErrKernelExecutionBackendStale     = errors.New("kernel execution backend authority is stale")
	ErrDetachedKernelExecutionConflict = errors.New("detached kernel execution conflicts with durable state")
)

type KernelExecutionBackend struct {
	BackendID                string
	OwnerUserID              string
	ProjectID                string
	RootFrameID              string
	RootFrameIncarnationID   string
	FrameID                  string
	FrameIncarnationID       string
	KernelID                 string
	KernelGeneration         int64
	SessionSpecJSON          string
	SessionSpecSHA256        string
	ProtocolVersion          int64
	ExecutorInstanceID       string
	MachineBootID            string
	ExecutorPID              int64
	ExecutorPIDStartTicks    int64
	WorkerPID                int64
	WorkerPIDStartTicks      int64
	WorkerPGID               int64
	CgroupPath               string
	SocketPath               string
	BackendGeneration        int64
	State                    string
	StateVersion             int64
	HeartbeatSequence        int64
	HeartbeatAt              *time.Time
	ControllerEpoch          int64
	ControllerTokenSHA256    []byte
	ControllerLeaseExpiresAt *time.Time
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

type CreateKernelExecutionBackendInput struct {
	BackendID              string
	OwnerUserID            string
	ProjectID              string
	RootFrameID            string
	RootFrameIncarnationID string
	FrameID                string
	FrameIncarnationID     string
	KernelID               string
	KernelGeneration       int64
	SessionSpec            KernelExecutionSessionSpecV1
	ExecutorInstanceID     string
	MachineBootID          string
	SocketPath             string
	BackendGeneration      int64
}

type ActivateKernelExecutionBackendInput struct {
	BackendID             string
	BackendGeneration     int64
	ExecutorInstanceID    string
	ExecutorPID           int64
	ExecutorPIDStartTicks int64
	WorkerPID             int64
	WorkerPIDStartTicks   int64
	WorkerPGID            int64
	CgroupPath            string
	HeartbeatSequence     int64
}

type FinishKernelExecutionBackendInput struct {
	BackendID          string
	BackendGeneration  int64
	ExecutorInstanceID string
	EvidenceLost       bool
}

type RecreateKernelExecutionBackendInput struct {
	BackendID          string
	ExecutorInstanceID string
	MachineBootID      string
	SocketPath         string
	KernelID           string
	KernelGeneration   int64
	SessionSpec        KernelExecutionSessionSpecV1
}

type KernelExecutionControlLease struct {
	BackendID         string
	BackendGeneration int64
	Epoch             int64
	Token             string
	ExpiresAt         time.Time
}

type AcquireKernelExecutionBackendControlInput struct {
	BackendID         string
	BackendGeneration int64
	Token             string
	LeaseExpiresAt    time.Time
}

type DetachedKernelExecution struct {
	ExecutionID             string
	OperationID             string
	BackendID               string
	BackendGeneration       int64
	RequestJSON             string
	RequestSHA256           string
	ConfinementSHA256       string
	State                   string
	StateVersion            int64
	DispatchSequence        int64
	AcceptedAt              time.Time
	DispatchCommittedAt     *time.Time
	RequestWrittenAt        *time.Time
	WorkerStartedAt         *time.Time
	LastObservationSequence int64
	StdoutTail              string
	CancelRequestID         string
	CancelRequestedAt       *time.Time
	CancelAckSequence       int64
	CancelAckAt             *time.Time
	CancelSignal            string
	TerminalReceiptID       string
	ReasonCode              string
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

type DetachedKernelExecutionRecoveryCandidate struct {
	Operation KernelLocalOperation
	Execution DetachedKernelExecution
}

type StartDetachedKernelLocalOperationInput struct {
	Start             StartKernelLocalOperationInput
	BackendID         string
	BackendGeneration int64
	ControllerEpoch   int64
	ControllerToken   string
	Request           KernelDetachedExecutionRequestV1
}

type KernelExecutionMountSpecV1 struct {
	Path     string `json:"path"`
	Writable bool   `json:"writable"`
	Trusted  bool   `json:"trusted,omitempty"`
}

type KernelExecutionSessionSpecV1 struct {
	Version                int                          `json:"version"`
	KernelID               string                       `json:"kernel_id"`
	OwnerUserID            string                       `json:"owner_user_id"`
	ProjectID              string                       `json:"project_id"`
	RootFrameID            string                       `json:"root_frame_id"`
	RootFrameIncarnationID string                       `json:"root_frame_incarnation_id"`
	FrameID                string                       `json:"frame_id"`
	FrameIncarnationID     string                       `json:"frame_incarnation_id"`
	AgentName              string                       `json:"agent_name"`
	DelegateName           string                       `json:"delegate_name,omitempty"`
	KernelKind             string                       `json:"kernel_kind"`
	Language               string                       `json:"language"`
	Environment            string                       `json:"environment"`
	RuntimeGeneration      string                       `json:"runtime_generation,omitempty"`
	WorkspaceDir           string                       `json:"workspace_dir"`
	Mounts                 []KernelExecutionMountSpecV1 `json:"mounts"`
	ProtectedPaths         []string                     `json:"protected_paths"`
	EgressAllowedDomains   []string                     `json:"egress_allowed_domains,omitempty"`
	EgressDeniedDomains    []string                     `json:"egress_denied_domains,omitempty"`
	CABundle               string                       `json:"ca_bundle,omitempty"`
	UpstreamProxy          string                       `json:"upstream_proxy,omitempty"`
	Fresh                  bool                         `json:"fresh"`
}

type KernelDetachedExecutionRequestV1 struct {
	Version                int      `json:"version"`
	OperationID            string   `json:"operation_id"`
	ExecutionID            string   `json:"execution_id"`
	OwnerUserID            string   `json:"owner_user_id"`
	ProjectID              string   `json:"project_id"`
	RootFrameID            string   `json:"root_frame_id"`
	RootFrameIncarnationID string   `json:"root_frame_incarnation_id"`
	FrameID                string   `json:"frame_id"`
	FrameIncarnationID     string   `json:"frame_incarnation_id"`
	KernelID               string   `json:"kernel_id"`
	KernelGeneration       int64    `json:"kernel_generation"`
	ToolCallID             string   `json:"tool_call_id"`
	ToolName               string   `json:"tool_name"`
	Language               string   `json:"language"`
	KernelKind             string   `json:"kernel_kind"`
	Environment            string   `json:"environment"`
	Code                   string   `json:"code"`
	WorkingDir             string   `json:"working_dir,omitempty"`
	Background             bool     `json:"background"`
	Fresh                  bool     `json:"fresh"`
	TimeoutMillis          int64    `json:"timeout_millis,omitempty"`
	OutputLimitBytes       int64    `json:"output_limit_bytes"`
	Origin                 string   `json:"origin"`
	HostCallMethods        []string `json:"host_call_methods"`
	HostCallMaxCalls       int      `json:"host_call_max_calls"`
	HostCallTimeoutMillis  int64    `json:"host_call_timeout_millis"`
}

func (s *Store) CreateKernelExecutionBackend(
	ctx context.Context,
	input CreateKernelExecutionBackendInput,
) (KernelExecutionBackend, error) {
	normalizeCreateKernelExecutionBackendInput(&input)
	sessionSpec, sessionSpecJSON, sessionSpecSHA256, sessionErr := canonicalKernelExecutionSessionSpec(input.SessionSpec)
	input.SessionSpec = sessionSpec
	if s == nil || s.db == nil || ctx == nil || !validDetachedIdentity(input.BackendID) ||
		!validDetachedIdentity(input.OwnerUserID) || !validDetachedIdentity(input.ProjectID) ||
		!validDetachedIdentity(input.RootFrameID) || !validDetachedIdentity(input.RootFrameIncarnationID) ||
		!validDetachedIdentity(input.FrameID) || !validDetachedIdentity(input.FrameIncarnationID) ||
		!validDetachedIdentity(input.KernelID) || input.KernelGeneration <= 0 ||
		!validDetachedIdentity(input.ExecutorInstanceID) || !validDetachedIdentity(input.MachineBootID) ||
		input.BackendGeneration <= 0 || sessionErr != nil || !kernelExecutionSessionSpecMatchesCreate(input.SessionSpec, input) ||
		input.SocketPath == "" || !filepath.IsAbs(input.SocketPath) ||
		len(input.SocketPath) > 4096 || strings.ContainsAny(input.SocketPath, "\x00\r\n") {
		return KernelExecutionBackend{}, errors.New("complete kernel execution backend identity is required")
	}
	repository, err := s.TranscriptRepository(ctx)
	if err != nil {
		return KernelExecutionBackend{}, err
	}
	now := s.now().UTC()
	err = repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		var ownerID, rootIncarnationID, frameIncarnationID string
		if err := tx.QueryRowContext(ctx, `SELECT project.user_id,root.incarnation_id,frame.incarnation_id
			FROM projects project
			JOIN frames root ON root.id=? AND root.project_id=project.id
			JOIN frames frame ON frame.id=? AND frame.project_id=project.id
			WHERE project.id=?`, input.RootFrameID, input.FrameID, input.ProjectID).Scan(
			&ownerID, &rootIncarnationID, &frameIncarnationID,
		); err != nil || ownerID != input.OwnerUserID || rootIncarnationID != input.RootFrameIncarnationID ||
			frameIncarnationID != input.FrameIncarnationID {
			return ErrKernelExecutionBackendConflict
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO kernel_execution_backends(
			backend_id,owner_user_id,project_id,root_frame_id,root_frame_incarnation_id,
			frame_id,frame_incarnation_id,kernel_id,kernel_generation,session_spec_json,session_spec_sha256,protocol_version,
			executor_instance_id,machine_boot_id,socket_path,backend_generation,state,state_version,
			created_at,updated_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,1,?,?,?,?,?,1,?,?) ON CONFLICT(backend_id) DO NOTHING`,
			input.BackendID, input.OwnerUserID, input.ProjectID, input.RootFrameID, input.RootFrameIncarnationID,
			input.FrameID, input.FrameIncarnationID, input.KernelID, input.KernelGeneration,
			sessionSpecJSON, sessionSpecSHA256,
			input.ExecutorInstanceID, input.MachineBootID, input.SocketPath, input.BackendGeneration,
			KernelExecutionBackendStateStarting, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
		)
		return err
	})
	if err != nil {
		return KernelExecutionBackend{}, err
	}
	backend, found, err := s.GetKernelExecutionBackend(ctx, input.BackendID)
	if err != nil {
		return KernelExecutionBackend{}, err
	}
	if !found || !kernelExecutionBackendOriginMatches(backend, input) {
		return KernelExecutionBackend{}, ErrKernelExecutionBackendConflict
	}
	return backend, nil
}

// RecreateKernelExecutionBackend resets one terminal evidence row in place so
// a fresh executor can own the same kernel session identity. Historical
// detached executions keep their backend_id/backend_generation references;
// only the live session row advances to a new executor generation.
func (s *Store) RecreateKernelExecutionBackend(
	ctx context.Context,
	input RecreateKernelExecutionBackendInput,
) (KernelExecutionBackend, error) {
	input.BackendID = strings.TrimSpace(input.BackendID)
	input.ExecutorInstanceID = strings.TrimSpace(input.ExecutorInstanceID)
	input.MachineBootID = strings.TrimSpace(input.MachineBootID)
	input.SocketPath = strings.TrimSpace(input.SocketPath)
	input.KernelID = strings.TrimSpace(input.KernelID)
	if s == nil || s.db == nil || ctx == nil || !validDetachedIdentity(input.BackendID) ||
		!validDetachedIdentity(input.ExecutorInstanceID) || !validDetachedIdentity(input.MachineBootID) ||
		!validDetachedIdentity(input.KernelID) || input.KernelGeneration <= 0 ||
		input.SocketPath == "" || !filepath.IsAbs(input.SocketPath) ||
		len(input.SocketPath) > 4096 || strings.ContainsAny(input.SocketPath, "\x00\r\n") {
		return KernelExecutionBackend{}, errors.New("complete kernel execution backend recreation identity is required")
	}
	current, found, err := s.GetKernelExecutionBackend(ctx, input.BackendID)
	if err != nil || !found {
		if err != nil {
			return KernelExecutionBackend{}, err
		}
		return KernelExecutionBackend{}, ErrKernelExecutionBackendStale
	}
	if current.KernelID != input.KernelID || current.KernelGeneration != input.KernelGeneration {
		return KernelExecutionBackend{}, ErrKernelExecutionBackendConflict
	}
	canonicalSpec, specJSON, specSHA, specErr := canonicalKernelExecutionSessionSpec(input.SessionSpec)
	if specErr != nil || current.SessionSpecJSON != specJSON || current.SessionSpecSHA256 != specSHA {
		return KernelExecutionBackend{}, ErrKernelExecutionBackendConflict
	}
	_ = canonicalSpec
	if current.State != KernelExecutionBackendStateStopped &&
		current.State != KernelExecutionBackendStateEvidenceLost {
		return KernelExecutionBackend{}, ErrKernelExecutionBackendStale
	}
	now := s.now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE kernel_execution_backends SET
		executor_instance_id=?,machine_boot_id=?,socket_path=?,backend_generation=backend_generation+1,
		executor_pid=NULL,executor_pid_start_ticks=NULL,worker_pid=NULL,worker_pid_start_ticks=NULL,
		worker_pgid=NULL,cgroup_path='',state=?,state_version=1,heartbeat_sequence=0,heartbeat_at=NULL,
		controller_epoch=0,controller_token_sha256=NULL,controller_lease_expires_at=NULL,updated_at=?
		WHERE backend_id=? AND backend_generation=? AND executor_instance_id=?
			AND state IN ('stopped','evidence_lost')`,
		input.ExecutorInstanceID, input.MachineBootID, input.SocketPath, KernelExecutionBackendStateStarting,
		now.Format(time.RFC3339Nano), input.BackendID, current.BackendGeneration,
		current.ExecutorInstanceID)
	if err != nil {
		return KernelExecutionBackend{}, err
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		if rowsErr != nil {
			return KernelExecutionBackend{}, rowsErr
		}
		return KernelExecutionBackend{}, ErrKernelExecutionBackendStale
	}
	backend, found, err := s.GetKernelExecutionBackend(ctx, input.BackendID)
	if err != nil || !found {
		if err != nil {
			return KernelExecutionBackend{}, err
		}
		return KernelExecutionBackend{}, ErrKernelExecutionBackendStale
	}
	return backend, nil
}

func (s *Store) ActivateKernelExecutionBackend(
	ctx context.Context,
	input ActivateKernelExecutionBackendInput,
) (KernelExecutionBackend, error) {
	input.BackendID = strings.TrimSpace(input.BackendID)
	input.ExecutorInstanceID = strings.TrimSpace(input.ExecutorInstanceID)
	input.CgroupPath = strings.TrimSpace(input.CgroupPath)
	if s == nil || s.db == nil || ctx == nil || !validDetachedIdentity(input.BackendID) ||
		input.BackendGeneration <= 0 || !validDetachedIdentity(input.ExecutorInstanceID) ||
		input.ExecutorPID <= 0 || input.ExecutorPIDStartTicks <= 0 || input.WorkerPID <= 0 ||
		input.WorkerPIDStartTicks <= 0 || input.WorkerPGID <= 0 || input.HeartbeatSequence <= 0 ||
		len(input.CgroupPath) > 4096 || strings.ContainsAny(input.CgroupPath, "\x00\r\n") {
		return KernelExecutionBackend{}, errors.New("complete kernel execution backend process identity is required")
	}
	now := s.now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE kernel_execution_backends SET
		executor_pid=?,executor_pid_start_ticks=?,worker_pid=?,worker_pid_start_ticks=?,worker_pgid=?,
		cgroup_path=?,state=?,state_version=state_version+1,heartbeat_sequence=?,heartbeat_at=?,updated_at=?
		WHERE backend_id=? AND backend_generation=? AND executor_instance_id=? AND state='starting'`,
		input.ExecutorPID, input.ExecutorPIDStartTicks, input.WorkerPID, input.WorkerPIDStartTicks, input.WorkerPGID,
		input.CgroupPath, KernelExecutionBackendStateReady, input.HeartbeatSequence,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), input.BackendID,
		input.BackendGeneration, input.ExecutorInstanceID)
	if err != nil {
		return KernelExecutionBackend{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return KernelExecutionBackend{}, err
	}
	backend, found, getErr := s.GetKernelExecutionBackend(ctx, input.BackendID)
	if getErr != nil {
		return KernelExecutionBackend{}, getErr
	}
	if rows == 0 && (!found || backend.State != KernelExecutionBackendStateReady ||
		backend.BackendGeneration != input.BackendGeneration || backend.ExecutorInstanceID != input.ExecutorInstanceID ||
		backend.ExecutorPID != input.ExecutorPID || backend.ExecutorPIDStartTicks != input.ExecutorPIDStartTicks ||
		backend.WorkerPID != input.WorkerPID || backend.WorkerPIDStartTicks != input.WorkerPIDStartTicks ||
		backend.WorkerPGID != input.WorkerPGID || backend.CgroupPath != input.CgroupPath ||
		backend.HeartbeatSequence != input.HeartbeatSequence) {
		return KernelExecutionBackend{}, ErrKernelExecutionBackendStale
	}
	return backend, nil
}

func (s *Store) FinishKernelExecutionBackend(
	ctx context.Context,
	input FinishKernelExecutionBackendInput,
) (KernelExecutionBackend, error) {
	input.BackendID = strings.TrimSpace(input.BackendID)
	input.ExecutorInstanceID = strings.TrimSpace(input.ExecutorInstanceID)
	if s == nil || s.db == nil || ctx == nil || !validDetachedIdentity(input.BackendID) ||
		input.BackendGeneration <= 0 || !validDetachedIdentity(input.ExecutorInstanceID) {
		return KernelExecutionBackend{}, errors.New("complete kernel execution backend finish authority is required")
	}
	current, found, err := s.GetKernelExecutionBackend(ctx, input.BackendID)
	if err != nil || !found {
		if err != nil {
			return KernelExecutionBackend{}, err
		}
		return KernelExecutionBackend{}, ErrKernelExecutionBackendStale
	}
	if current.BackendGeneration != input.BackendGeneration ||
		current.ExecutorInstanceID != input.ExecutorInstanceID {
		return KernelExecutionBackend{}, ErrKernelExecutionBackendStale
	}
	target := KernelExecutionBackendStateStopped
	if input.EvidenceLost {
		target = KernelExecutionBackendStateEvidenceLost
	}
	if current.State == target {
		return current, nil
	}
	if current.State != KernelExecutionBackendStateStarting && current.State != KernelExecutionBackendStateReady &&
		current.State != KernelExecutionBackendStateDraining {
		return KernelExecutionBackend{}, ErrKernelExecutionBackendStale
	}
	now := s.now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE kernel_execution_backends SET
		state=?,state_version=state_version+1,controller_token_sha256=NULL,
		controller_lease_expires_at=NULL,updated_at=?
		WHERE backend_id=? AND backend_generation=? AND executor_instance_id=? AND state_version=?`,
		target, now.Format(time.RFC3339Nano), input.BackendID, input.BackendGeneration,
		input.ExecutorInstanceID, current.StateVersion)
	if err != nil {
		return KernelExecutionBackend{}, err
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		if rowsErr != nil {
			return KernelExecutionBackend{}, rowsErr
		}
		return KernelExecutionBackend{}, ErrKernelExecutionBackendStale
	}
	finished, _, err := s.GetKernelExecutionBackend(ctx, input.BackendID)
	return finished, err
}

func (s *Store) AcquireKernelExecutionBackendControl(
	ctx context.Context,
	input AcquireKernelExecutionBackendControlInput,
) (KernelExecutionBackend, KernelExecutionControlLease, error) {
	input.BackendID = strings.TrimSpace(input.BackendID)
	input.Token = strings.TrimSpace(input.Token)
	input.LeaseExpiresAt = input.LeaseExpiresAt.UTC()
	if s == nil || s.db == nil || ctx == nil || !validDetachedIdentity(input.BackendID) ||
		input.BackendGeneration <= 0 || len(input.Token) < 32 || len(input.Token) > 4096 ||
		strings.ContainsAny(input.Token, "\x00\r\n") || input.LeaseExpiresAt.IsZero() {
		return KernelExecutionBackend{}, KernelExecutionControlLease{},
			errors.New("complete kernel execution backend control lease is required")
	}
	digest := sha256.Sum256([]byte(input.Token))
	repository, err := s.TranscriptRepository(ctx)
	if err != nil {
		return KernelExecutionBackend{}, KernelExecutionControlLease{}, err
	}
	var backend KernelExecutionBackend
	err = repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		now := s.now().UTC()
		if !input.LeaseExpiresAt.After(now) {
			return ErrKernelExecutionBackendStale
		}
		result, err := tx.ExecContext(ctx, `UPDATE kernel_execution_backends SET
		controller_epoch=controller_epoch+1,controller_token_sha256=?,controller_lease_expires_at=?,
		state_version=state_version+1,updated_at=?
		WHERE backend_id=? AND backend_generation=? AND state IN ('ready','draining')`, digest[:],
			input.LeaseExpiresAt.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
			input.BackendID, input.BackendGeneration)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rows != 1 {
			return ErrKernelExecutionBackendStale
		}
		var found bool
		backend, found, err = getKernelExecutionBackendQuery(ctx, tx, input.BackendID)
		if err != nil {
			return err
		}
		if !found {
			return ErrKernelExecutionBackendStale
		}
		return nil
	})
	if err != nil {
		return KernelExecutionBackend{}, KernelExecutionControlLease{}, err
	}
	return backend, KernelExecutionControlLease{
		BackendID: input.BackendID, BackendGeneration: input.BackendGeneration,
		Epoch: backend.ControllerEpoch, Token: input.Token, ExpiresAt: input.LeaseExpiresAt,
	}, nil
}

func (s *Store) GetKernelExecutionBackend(
	ctx context.Context,
	backendID string,
) (KernelExecutionBackend, bool, error) {
	backendID = strings.TrimSpace(backendID)
	if s == nil || s.db == nil || ctx == nil || !validDetachedIdentity(backendID) {
		return KernelExecutionBackend{}, false, errors.New("kernel execution backend id is required")
	}
	return getKernelExecutionBackendQuery(ctx, s.db, backendID)
}

func (s *Store) FindKernelExecutionBackendForSession(
	ctx context.Context,
	spec KernelExecutionSessionSpecV1,
) (KernelExecutionBackend, bool, error) {
	_, canonical, digest, err := canonicalKernelExecutionSessionSpec(spec)
	if s == nil || s.db == nil || ctx == nil || err != nil {
		if err != nil {
			return KernelExecutionBackend{}, false, err
		}
		return KernelExecutionBackend{}, false, errors.New("kernel execution session authority is required")
	}
	var backendID string
	err = s.db.QueryRowContext(ctx, `SELECT backend_id FROM kernel_execution_backends
		WHERE owner_user_id=? AND project_id=? AND root_frame_id=? AND root_frame_incarnation_id=?
		AND frame_id=? AND frame_incarnation_id=? AND kernel_id=? AND session_spec_sha256=?
		AND session_spec_json=? AND state IN ('starting','ready','draining','stopped','evidence_lost')
		ORDER BY created_at DESC,backend_id DESC LIMIT 1`,
		spec.OwnerUserID, spec.ProjectID, spec.RootFrameID, spec.RootFrameIncarnationID,
		spec.FrameID, spec.FrameIncarnationID, spec.KernelID, digest, canonical).Scan(&backendID)
	if errors.Is(err, sql.ErrNoRows) {
		return KernelExecutionBackend{}, false, nil
	}
	if err != nil {
		return KernelExecutionBackend{}, false, err
	}
	return s.GetKernelExecutionBackend(ctx, backendID)
}

// FindLatestKernelExecutionBackendForIdentity returns the latest immutable
// backend authority for one stable kernel identity without pretending that a
// changed mount or egress policy is the same session specification. Callers
// use this only after an exact session lookup misses: a terminal predecessor
// determines the next kernel generation, while a live predecessor is a real
// authority conflict that must settle before replacement.
func (s *Store) FindLatestKernelExecutionBackendForIdentity(
	ctx context.Context,
	spec KernelExecutionSessionSpecV1,
) (KernelExecutionBackend, bool, error) {
	normalized, _, _, err := canonicalKernelExecutionSessionSpec(spec)
	if s == nil || s.db == nil || ctx == nil || err != nil {
		if err != nil {
			return KernelExecutionBackend{}, false, err
		}
		return KernelExecutionBackend{}, false, errors.New("kernel execution session authority is required")
	}
	var backendID string
	err = s.db.QueryRowContext(ctx, `SELECT backend_id FROM kernel_execution_backends
		WHERE owner_user_id=? AND project_id=? AND root_frame_id=? AND root_frame_incarnation_id=?
		AND frame_id=? AND frame_incarnation_id=? AND kernel_id=?
		ORDER BY kernel_generation DESC,created_at DESC,backend_id DESC LIMIT 1`,
		normalized.OwnerUserID, normalized.ProjectID, normalized.RootFrameID, normalized.RootFrameIncarnationID,
		normalized.FrameID, normalized.FrameIncarnationID, normalized.KernelID).Scan(&backendID)
	if errors.Is(err, sql.ErrNoRows) {
		return KernelExecutionBackend{}, false, nil
	}
	if err != nil {
		return KernelExecutionBackend{}, false, err
	}
	return s.GetKernelExecutionBackend(ctx, backendID)
}

func (s *Store) StartDetachedKernelLocalOperation(
	ctx context.Context,
	input StartDetachedKernelLocalOperationInput,
) (KernelLocalOperation, DetachedKernelExecution, error) {
	input.BackendID = strings.TrimSpace(input.BackendID)
	input.ControllerToken = strings.TrimSpace(input.ControllerToken)
	if !validDetachedIdentity(input.BackendID) || input.BackendGeneration <= 0 ||
		input.ControllerEpoch <= 0 || len(input.ControllerToken) < 32 || len(input.ControllerToken) > 4096 {
		return KernelLocalOperation{}, DetachedKernelExecution{},
			errors.New("complete detached kernel execution authority is required")
	}
	var accepted DetachedKernelExecution
	operation, err := s.startKernelLocalOperation(ctx, input.Start, func(
		tx *transcriptstore.ImmediateTransaction,
		operation KernelLocalOperation,
	) error {
		var acceptErr error
		accepted, acceptErr = acceptDetachedKernelExecutionTx(ctx, tx, operation, input, s.now().UTC())
		return acceptErr
	})
	if err != nil {
		return KernelLocalOperation{}, DetachedKernelExecution{}, err
	}
	if accepted.ExecutionID == "" {
		var found bool
		accepted, found, err = s.GetDetachedKernelExecution(ctx, operation.ExecutionID)
		if err != nil || !found {
			if err != nil {
				return KernelLocalOperation{}, DetachedKernelExecution{}, err
			}
			return KernelLocalOperation{}, DetachedKernelExecution{}, ErrDetachedKernelExecutionConflict
		}
	}
	return operation, accepted, nil
}

func (s *Store) GetDetachedKernelExecution(
	ctx context.Context,
	executionID string,
) (DetachedKernelExecution, bool, error) {
	executionID = strings.TrimSpace(executionID)
	if s == nil || s.db == nil || ctx == nil || !validDetachedIdentity(executionID) {
		return DetachedKernelExecution{}, false, errors.New("detached kernel execution id is required")
	}
	return getDetachedKernelExecutionQuery(ctx, s.db, executionID)
}

// HasActiveDetachedKernelExecutionForFrame reports only durable executor-owned
// work that can still produce an exact terminal receipt. A nonterminal local
// operation row alone is intentionally not treated as live execution evidence.
func (s *Store) HasActiveDetachedKernelExecutionForFrame(ctx context.Context, frameID string) (bool, error) {
	frameID = strings.TrimSpace(frameID)
	if s == nil || s.db == nil || ctx == nil || frameID == "" {
		return false, errors.New("detached kernel frame identity is required")
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM kernel_detached_executions detached
		JOIN kernel_local_operations operation ON operation.operation_id=detached.operation_id
		WHERE operation.frame_id=? AND operation.state='started'
			AND detached.state IN ('accepted','dispatch_committed','started','cancel_requested')`, frameID).Scan(&count)
	return count > 0, err
}

// ListDetachedKernelExecutionRecoveryCandidates returns durable executions
// whose local operation has not yet been terminally materialized. Restarted
// executions are always eligible; a same-boot execution is eligible only after
// its immutable terminal receipt exists, so a lost observer cannot strand it
// while live physical work remains owned by its original observer.
func (s *Store) ListDetachedKernelExecutionRecoveryCandidates(
	ctx context.Context,
	currentBootID string,
	limit int,
) ([]DetachedKernelExecutionRecoveryCandidate, bool, error) {
	currentBootID = strings.TrimSpace(currentBootID)
	if s == nil || s.db == nil || ctx == nil || !validDetachedIdentity(currentBootID) || limit <= 0 || limit > 1000 {
		return nil, false, errors.New("detached kernel recovery limit must be between 1 and 1000")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT detached.execution_id
		FROM kernel_detached_executions detached
		JOIN kernel_local_operations operation ON operation.operation_id=detached.operation_id
		WHERE operation.state='started' AND (operation.boot_id!=? OR detached.state='terminal')
			AND detached.state!='evidence_lost'
			AND NOT EXISTS (
				SELECT 1 FROM kernel_local_operation_materializations materialization
				WHERE materialization.operation_id=operation.operation_id
			)
		ORDER BY detached.updated_at,detached.execution_id LIMIT ?`, currentBootID, limit+1)
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
	candidates := make([]DetachedKernelExecutionRecoveryCandidate, 0, len(ids))
	for _, id := range ids {
		execution, found, err := getDetachedKernelExecutionQuery(ctx, s.db, id)
		if err != nil {
			return nil, false, err
		}
		if !found {
			return nil, false, ErrDetachedKernelExecutionConflict
		}
		request, err := DecodeKernelDetachedExecutionRequestV1(execution.RequestJSON)
		if err != nil {
			return nil, false, err
		}
		operation, found, err := getKernelLocalOperationQuery(ctx, s.db, execution.OperationID)
		if err != nil {
			return nil, false, err
		}
		if !found || operation.OwnerUserID != request.OwnerUserID ||
			operation.State != KernelLocalOperationStateStarted || operation.ExecutionID != execution.ExecutionID {
			return nil, false, ErrDetachedKernelExecutionConflict
		}
		candidates = append(candidates, DetachedKernelExecutionRecoveryCandidate{
			Operation: operation, Execution: execution,
		})
	}
	return candidates, more, nil
}

func acceptDetachedKernelExecutionTx(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	operation KernelLocalOperation,
	input StartDetachedKernelLocalOperationInput,
	now time.Time,
) (DetachedKernelExecution, error) {
	if operation.State != KernelLocalOperationStateStarted || operation.ExecutionID == "" ||
		operation.ConfinementSHA256 == "" {
		return DetachedKernelExecution{}, fmt.Errorf("%w: operation start authority is incomplete", ErrDetachedKernelExecutionConflict)
	}
	if err := validateKernelExecutionBackendControlTx(ctx, tx, operation, input, now); err != nil {
		return DetachedKernelExecution{}, err
	}
	backend, found, err := getKernelExecutionBackendQuery(ctx, tx, input.BackendID)
	if err != nil || !found {
		if err != nil {
			return DetachedKernelExecution{}, err
		}
		return DetachedKernelExecution{}, ErrKernelExecutionBackendStale
	}
	request, requestJSON, requestSHA, err := canonicalKernelDetachedExecutionRequest(operation, backend, input.Request)
	if err != nil {
		return DetachedKernelExecution{}, err
	}
	input.Request = request
	_, err = tx.ExecContext(ctx, `INSERT INTO kernel_detached_executions(
		execution_id,operation_id,backend_id,backend_generation,request_json,request_sha256,confinement_sha256,
		state,state_version,accepted_at,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?,1,?,?,?) ON CONFLICT(execution_id) DO NOTHING`,
		operation.ExecutionID, operation.OperationID, input.BackendID, input.BackendGeneration,
		requestJSON, requestSHA, operation.ConfinementSHA256, DetachedKernelExecutionStateAccepted,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return DetachedKernelExecution{}, err
	}
	execution, found, err := getDetachedKernelExecutionQuery(ctx, tx, operation.ExecutionID)
	if err != nil {
		return DetachedKernelExecution{}, err
	}
	if !found || execution.OperationID != operation.OperationID || execution.BackendID != input.BackendID ||
		execution.BackendGeneration != input.BackendGeneration || execution.RequestJSON != requestJSON ||
		execution.RequestSHA256 != requestSHA ||
		execution.ConfinementSHA256 != operation.ConfinementSHA256 || execution.State != DetachedKernelExecutionStateAccepted {
		return DetachedKernelExecution{}, fmt.Errorf("%w: accepted execution did not round-trip exactly", ErrDetachedKernelExecutionConflict)
	}
	return execution, nil
}

func validateKernelExecutionBackendControlTx(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	operation KernelLocalOperation,
	input StartDetachedKernelLocalOperationInput,
	now time.Time,
) error {
	var ownerID, projectID, rootFrameID, rootIncarnationID, frameID, frameIncarnationID, kernelID, state string
	var kernelGeneration, generation, epoch int64
	var tokenDigest []byte
	var expiresAtText string
	if err := tx.QueryRowContext(ctx, `SELECT owner_user_id,project_id,root_frame_id,root_frame_incarnation_id,
		frame_id,frame_incarnation_id,kernel_id,kernel_generation,backend_generation,state,controller_epoch,
		controller_token_sha256,controller_lease_expires_at
		FROM kernel_execution_backends WHERE backend_id=?`, input.BackendID).Scan(
		&ownerID, &projectID, &rootFrameID, &rootIncarnationID, &frameID, &frameIncarnationID,
		&kernelID, &kernelGeneration, &generation, &state, &epoch, &tokenDigest, &expiresAtText,
	); err != nil {
		return ErrKernelExecutionBackendStale
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, expiresAtText)
	if err != nil {
		return ErrKernelExecutionBackendConflict
	}
	digest := sha256.Sum256([]byte(input.ControllerToken))
	if ownerID != operation.OwnerUserID || projectID != operation.ProjectID ||
		rootFrameID != operation.RootFrameID || rootIncarnationID != operation.RootFrameIncarnationID ||
		frameID != operation.FrameID || frameIncarnationID != operation.FrameIncarnationID ||
		kernelID != operation.KernelID || kernelGeneration != operation.KernelGeneration ||
		generation != input.BackendGeneration || state != KernelExecutionBackendStateReady ||
		epoch != input.ControllerEpoch || subtle.ConstantTimeCompare(tokenDigest, digest[:]) != 1 || !expiresAt.After(now) {
		return ErrKernelExecutionBackendStale
	}
	return nil
}

func normalizeCreateKernelExecutionBackendInput(input *CreateKernelExecutionBackendInput) {
	input.BackendID = strings.TrimSpace(input.BackendID)
	input.OwnerUserID = strings.TrimSpace(input.OwnerUserID)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.RootFrameID = strings.TrimSpace(input.RootFrameID)
	input.RootFrameIncarnationID = strings.TrimSpace(input.RootFrameIncarnationID)
	input.FrameID = strings.TrimSpace(input.FrameID)
	input.FrameIncarnationID = strings.TrimSpace(input.FrameIncarnationID)
	input.KernelID = strings.TrimSpace(input.KernelID)
	input.ExecutorInstanceID = strings.TrimSpace(input.ExecutorInstanceID)
	input.MachineBootID = strings.TrimSpace(input.MachineBootID)
	input.SocketPath = strings.TrimSpace(input.SocketPath)
}

func canonicalKernelExecutionSessionSpec(
	input KernelExecutionSessionSpecV1,
) (KernelExecutionSessionSpecV1, string, string, error) {
	input.KernelID = strings.TrimSpace(input.KernelID)
	input.OwnerUserID = strings.TrimSpace(input.OwnerUserID)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.RootFrameID = strings.TrimSpace(input.RootFrameID)
	input.RootFrameIncarnationID = strings.TrimSpace(input.RootFrameIncarnationID)
	input.FrameID = strings.TrimSpace(input.FrameID)
	input.FrameIncarnationID = strings.TrimSpace(input.FrameIncarnationID)
	input.AgentName = strings.TrimSpace(input.AgentName)
	input.DelegateName = strings.TrimSpace(input.DelegateName)
	input.KernelKind = strings.TrimSpace(input.KernelKind)
	input.Language = strings.ToLower(strings.TrimSpace(input.Language))
	input.Environment = strings.TrimSpace(input.Environment)
	input.RuntimeGeneration = strings.TrimSpace(input.RuntimeGeneration)
	input.WorkspaceDir = filepath.Clean(strings.TrimSpace(input.WorkspaceDir))
	if input.Version != 1 || !validDetachedIdentity(input.KernelID) || !validDetachedIdentity(input.OwnerUserID) ||
		!validDetachedIdentity(input.ProjectID) || !validDetachedIdentity(input.RootFrameID) ||
		!validDetachedIdentity(input.RootFrameIncarnationID) || !validDetachedIdentity(input.FrameID) ||
		!validDetachedIdentity(input.FrameIncarnationID) || !validDetachedIdentity(input.AgentName) ||
		(input.DelegateName != "" && !validDetachedIdentity(input.DelegateName)) ||
		!validDetachedIdentity(input.KernelKind) || (input.Language != "python" && input.Language != "r") ||
		!validDetachedIdentity(input.Environment) ||
		(input.RuntimeGeneration != "" && !validDetachedIdentity(input.RuntimeGeneration)) ||
		!validDetachedAbsolutePath(input.WorkspaceDir) || len(input.Mounts) > 64 || len(input.ProtectedPaths) > 128 {
		return KernelExecutionSessionSpecV1{}, "", "", errors.New("kernel execution session spec is invalid")
	}
	mounts := append([]KernelExecutionMountSpecV1{}, input.Mounts...)
	for index := range mounts {
		mounts[index].Path = filepath.Clean(strings.TrimSpace(mounts[index].Path))
		if !validDetachedAbsolutePath(mounts[index].Path) || (mounts[index].Trusted && mounts[index].Writable) {
			return KernelExecutionSessionSpecV1{}, "", "", errors.New("kernel execution session mount is invalid")
		}
	}
	sort.Slice(mounts, func(left, right int) bool { return mounts[left].Path < mounts[right].Path })
	for index := 1; index < len(mounts); index++ {
		if mounts[index-1].Path == mounts[index].Path {
			return KernelExecutionSessionSpecV1{}, "", "", errors.New("kernel execution session mount is duplicated")
		}
	}
	input.Mounts = mounts
	protected := append([]string{}, input.ProtectedPaths...)
	for index := range protected {
		protected[index] = filepath.Clean(strings.TrimSpace(protected[index]))
		if !validDetachedAbsolutePath(protected[index]) {
			return KernelExecutionSessionSpecV1{}, "", "", errors.New("kernel execution protected path is invalid")
		}
	}
	sort.Strings(protected)
	for index := 1; index < len(protected); index++ {
		if protected[index-1] == protected[index] {
			return KernelExecutionSessionSpecV1{}, "", "", errors.New("kernel execution protected path is duplicated")
		}
	}
	input.ProtectedPaths = protected
	var err error
	input.EgressAllowedDomains, err = networkpolicy.NormalizePatterns(input.EgressAllowedDomains, 128)
	if err != nil {
		return KernelExecutionSessionSpecV1{}, "", "", errors.New("kernel execution allowed-domain policy is invalid")
	}
	sort.Strings(input.EgressAllowedDomains)
	input.EgressDeniedDomains, err = normalizeKernelExecutionDeniedDomains(input.EgressDeniedDomains)
	if err != nil {
		return KernelExecutionSessionSpecV1{}, "", "", errors.New("kernel execution denied-domain policy is invalid")
	}
	sort.Strings(input.EgressDeniedDomains)
	input.UpstreamProxy, err = networktls.NormalizeProxyURL(input.UpstreamProxy)
	if err != nil {
		return KernelExecutionSessionSpecV1{}, "", "", errors.New("kernel execution upstream proxy is invalid")
	}
	if strings.TrimSpace(input.CABundle) != "" {
		input.CABundle = filepath.Clean(strings.TrimSpace(input.CABundle))
		if !validDetachedAbsolutePath(input.CABundle) {
			return KernelExecutionSessionSpecV1{}, "", "", errors.New("kernel execution CA bundle is invalid")
		}
	}
	encoded, err := json.Marshal(input)
	if err != nil || len(encoded) > 1048576 {
		return KernelExecutionSessionSpecV1{}, "", "", errors.New("kernel execution session spec exceeds the durable limit")
	}
	digest := sha256.Sum256(encoded)
	return input, string(encoded), hex.EncodeToString(digest[:]), nil
}

func normalizeKernelExecutionDeniedDomains(values []string) ([]string, error) {
	builtIn := make(map[string]struct{})
	for _, value := range networkpolicy.BuiltInDeniedPatterns() {
		builtIn[value] = struct{}{}
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(value, ".")))
		if _, trusted := builtIn[value]; !trusted {
			normalized, err := networkpolicy.NormalizePattern(value)
			if err != nil {
				return nil, err
			}
			value = normalized
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	if len(result) > 128 {
		return nil, errors.New("denied-domain policy exceeds 128 entries")
	}
	sort.Strings(result)
	return result, nil
}

func DecodeKernelExecutionSessionSpecV1(raw string) (KernelExecutionSessionSpecV1, error) {
	var input KernelExecutionSessionSpecV1
	if err := decodeClosedDetachedJSONObject(raw, &input); err != nil {
		return KernelExecutionSessionSpecV1{}, err
	}
	normalized, canonical, _, err := canonicalKernelExecutionSessionSpec(input)
	if err != nil || canonical != raw {
		return KernelExecutionSessionSpecV1{}, errors.New("kernel execution session spec is not canonical")
	}
	return normalized, nil
}

func canonicalKernelDetachedExecutionRequest(
	operation KernelLocalOperation,
	backend KernelExecutionBackend,
	input KernelDetachedExecutionRequestV1,
) (KernelDetachedExecutionRequestV1, string, string, error) {
	input.OperationID = strings.TrimSpace(input.OperationID)
	input.ExecutionID = strings.TrimSpace(input.ExecutionID)
	input.OwnerUserID = strings.TrimSpace(input.OwnerUserID)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.RootFrameID = strings.TrimSpace(input.RootFrameID)
	input.RootFrameIncarnationID = strings.TrimSpace(input.RootFrameIncarnationID)
	input.FrameID = strings.TrimSpace(input.FrameID)
	input.FrameIncarnationID = strings.TrimSpace(input.FrameIncarnationID)
	input.KernelID = strings.TrimSpace(input.KernelID)
	input.ToolCallID = strings.TrimSpace(input.ToolCallID)
	input.ToolName = strings.TrimSpace(input.ToolName)
	input.Language = strings.ToLower(strings.TrimSpace(input.Language))
	input.KernelKind = strings.TrimSpace(input.KernelKind)
	input.Environment = strings.TrimSpace(input.Environment)
	input.WorkingDir = strings.TrimSpace(input.WorkingDir)
	input.Origin = strings.TrimSpace(input.Origin)
	if input.WorkingDir != "" {
		input.WorkingDir = filepath.Clean(input.WorkingDir)
	}
	methods := append([]string{}, input.HostCallMethods...)
	for index := range methods {
		methods[index] = strings.TrimSpace(methods[index])
		if !validDetachedIdentity(methods[index]) {
			return KernelDetachedExecutionRequestV1{}, "", "", errors.New("kernel execution host method is invalid")
		}
	}
	sort.Strings(methods)
	for index := 1; index < len(methods); index++ {
		if methods[index-1] == methods[index] {
			return KernelDetachedExecutionRequestV1{}, "", "", errors.New("kernel execution host method is duplicated")
		}
	}
	input.HostCallMethods = methods
	if input.Version != 1 || operation.State != KernelLocalOperationStateStarted ||
		input.OperationID != operation.OperationID || input.ExecutionID != operation.ExecutionID ||
		input.OwnerUserID != operation.OwnerUserID || input.ProjectID != operation.ProjectID ||
		input.RootFrameID != operation.RootFrameID || input.RootFrameIncarnationID != operation.RootFrameIncarnationID ||
		input.FrameID != operation.FrameID || input.FrameIncarnationID != operation.FrameIncarnationID ||
		input.KernelID != operation.KernelID || input.KernelGeneration != operation.KernelGeneration ||
		input.ToolCallID != operation.ToolCallID || input.ToolName != operation.Tool ||
		input.Environment != operation.Environment || input.OutputLimitBytes <= 0 || input.Origin != "agent" ||
		!utf8.ValidString(input.Code) || len(input.Code) == 0 || len(input.Code) > 100000 ||
		(input.WorkingDir != "" && !validDetachedAbsolutePath(input.WorkingDir)) ||
		input.TimeoutMillis < 0 || input.TimeoutMillis > int64((24*time.Hour)/time.Millisecond) || len(input.HostCallMethods) > 32 {
		return KernelDetachedExecutionRequestV1{}, "", "", errors.New("kernel detached execution request is invalid")
	}
	if len(input.HostCallMethods) == 0 {
		if input.HostCallMaxCalls != 0 || input.HostCallTimeoutMillis != 0 {
			return KernelDetachedExecutionRequestV1{}, "", "", errors.New("kernel execution host policy has no methods")
		}
	} else if input.HostCallMaxCalls <= 0 || input.HostCallMaxCalls > 32 ||
		input.HostCallTimeoutMillis <= 0 || input.HostCallTimeoutMillis > int64((24*time.Hour)/time.Millisecond) {
		return KernelDetachedExecutionRequestV1{}, "", "", errors.New("kernel execution host policy is invalid")
	}
	session, err := DecodeKernelExecutionSessionSpecV1(backend.SessionSpecJSON)
	if err != nil || backend.SessionSpecSHA256 != sha256HexString(backend.SessionSpecJSON) ||
		input.OwnerUserID != session.OwnerUserID || input.ProjectID != session.ProjectID ||
		input.RootFrameID != session.RootFrameID || input.RootFrameIncarnationID != session.RootFrameIncarnationID ||
		input.FrameID != session.FrameID || input.FrameIncarnationID != session.FrameIncarnationID ||
		input.KernelID != session.KernelID || input.Language != session.Language ||
		input.KernelKind != session.KernelKind || input.Environment != session.Environment || input.Fresh != session.Fresh {
		return KernelDetachedExecutionRequestV1{}, "", "", ErrKernelExecutionBackendConflict
	}
	code := ""
	workingDir := ""
	background := false
	fresh := false
	if operation.Tool == kernelcontract.SoftwareRuntimeTool {
		request, authorityErr := kernelcontract.ValidateSoftwareRuntimeExecutionSource(
			operation.InputJSON, operation.Environment, input.Code,
		)
		if authorityErr != nil {
			return KernelDetachedExecutionRequestV1{}, "", "", fmt.Errorf(
				"%w: software runtime launcher authority: %v", ErrDetachedKernelExecutionConflict, authorityErr,
			)
		}
		code = input.Code
		if !kernelcontract.SoftwareRuntimeWorkingDirBound(
			request.WorkingDir, input.WorkingDir, session.WorkspaceDir,
		) {
			return KernelDetachedExecutionRequestV1{}, "", "", fmt.Errorf(
				"%w: software runtime working directory authority", ErrDetachedKernelExecutionConflict,
			)
		}
		workingDir = strings.TrimSpace(input.WorkingDir)
		background = request.Background
	} else {
		var source map[string]any
		if err := json.Unmarshal(operation.InputJSON, &source); err != nil || source == nil {
			return KernelDetachedExecutionRequestV1{}, "", "", ErrDetachedKernelExecutionConflict
		}
		code, _ = source["code"].(string)
		if operation.Tool == kernelcontract.BashTool {
			command, _ := source["command"].(string)
			decoded, decodeErr := kernelcontract.BashCommandFromWrapper(input.Code)
			if decodeErr != nil || decoded != command {
				return KernelDetachedExecutionRequestV1{}, "", "", fmt.Errorf(
					"%w: bash execution envelope changed after admission", ErrDetachedKernelExecutionConflict,
				)
			}
			code = input.Code
		}
		workingDir, _ = source["working_dir"].(string)
		workingDir = strings.TrimSpace(workingDir)
		if workingDir != "" && !filepath.IsAbs(workingDir) {
			// The durable operation preserves the exact model-authored relative
			// path for approval/audit, while the execution request carries the
			// confinement-resolved absolute path. Reconstruct that same meaning
			// from the immutable session workspace before comparing identities.
			// This is equivalence validation, not permission expansion: the
			// execution path must still match exactly after resolution.
			workingDir = filepath.Join(session.WorkspaceDir, filepath.Clean(workingDir))
		}
		background, _ = source["background"].(bool)
		fresh, _ = source["fresh"].(bool)
	}
	if workingDir != "" {
		workingDir = filepath.Clean(workingDir)
	}
	if input.Code != code || input.WorkingDir != workingDir ||
		input.Background != background || input.Fresh != fresh {
		return KernelDetachedExecutionRequestV1{}, "", "", fmt.Errorf(
			"%w: execution source identity changed after admission", ErrDetachedKernelExecutionConflict,
		)
	}
	encoded, err := json.Marshal(input)
	if err != nil || len(encoded) > 1048576 {
		return KernelDetachedExecutionRequestV1{}, "", "", errors.New("kernel detached execution request exceeds the durable limit")
	}
	digest := sha256.Sum256(encoded)
	return input, string(encoded), hex.EncodeToString(digest[:]), nil
}

func DecodeKernelDetachedExecutionRequestV1(raw string) (KernelDetachedExecutionRequestV1, error) {
	var input KernelDetachedExecutionRequestV1
	if err := decodeClosedDetachedJSONObject(raw, &input); err != nil {
		return KernelDetachedExecutionRequestV1{}, err
	}
	encoded, err := json.Marshal(input)
	if err != nil || string(encoded) != raw || !validKernelDetachedExecutionRequestShape(input) {
		return KernelDetachedExecutionRequestV1{}, errors.New("kernel detached execution request is not canonical")
	}
	return input, nil
}

func validKernelDetachedExecutionRequestShape(input KernelDetachedExecutionRequestV1) bool {
	if input.Version != 1 || !validDetachedIdentity(input.OperationID) || !validDetachedIdentity(input.ExecutionID) ||
		!validDetachedIdentity(input.OwnerUserID) || !validDetachedIdentity(input.ProjectID) ||
		!validDetachedIdentity(input.RootFrameID) || !validDetachedIdentity(input.RootFrameIncarnationID) ||
		!validDetachedIdentity(input.FrameID) || !validDetachedIdentity(input.FrameIncarnationID) ||
		!validDetachedIdentity(input.KernelID) || input.KernelGeneration <= 0 ||
		!validDetachedIdentity(input.ToolCallID) || !validDetachedIdentity(input.ToolName) ||
		(input.Language != "python" && input.Language != "r") || !validDetachedIdentity(input.KernelKind) ||
		!validDetachedIdentity(input.Environment) || !utf8.ValidString(input.Code) || len(input.Code) == 0 ||
		len(input.Code) > 100000 || input.OutputLimitBytes <= 0 || input.Origin != "agent" ||
		(input.WorkingDir != "" && !validDetachedAbsolutePath(input.WorkingDir)) ||
		input.TimeoutMillis < 0 || input.TimeoutMillis > int64((24*time.Hour)/time.Millisecond) || len(input.HostCallMethods) > 32 {
		return false
	}
	for index, method := range input.HostCallMethods {
		if !validDetachedIdentity(method) || (index > 0 && input.HostCallMethods[index-1] >= method) {
			return false
		}
	}
	if len(input.HostCallMethods) == 0 {
		return input.HostCallMaxCalls == 0 && input.HostCallTimeoutMillis == 0
	}
	return input.HostCallMaxCalls > 0 && input.HostCallMaxCalls <= 32 &&
		input.HostCallTimeoutMillis > 0 && input.HostCallTimeoutMillis <= int64((24*time.Hour)/time.Millisecond)
}

func kernelExecutionSessionSpecMatchesCreate(
	spec KernelExecutionSessionSpecV1,
	input CreateKernelExecutionBackendInput,
) bool {
	return spec.KernelID == input.KernelID && spec.OwnerUserID == input.OwnerUserID &&
		spec.ProjectID == input.ProjectID && spec.RootFrameID == input.RootFrameID &&
		spec.RootFrameIncarnationID == input.RootFrameIncarnationID && spec.FrameID == input.FrameID &&
		spec.FrameIncarnationID == input.FrameIncarnationID
}

func validDetachedAbsolutePath(value string) bool {
	return value != "" && filepath.IsAbs(value) && len(value) <= 4096 &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func decodeClosedDetachedJSONObject(raw string, target any) error {
	if len(raw) < 2 || len(raw) > 1048576 || !utf8.ValidString(raw) {
		return errors.New("detached execution JSON is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("detached execution JSON is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("detached execution JSON must contain one object")
	}
	return nil
}

func sha256HexString(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func validDetachedIdentity(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 512 &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func kernelExecutionBackendOriginMatches(
	backend KernelExecutionBackend,
	input CreateKernelExecutionBackendInput,
) bool {
	_, sessionSpecJSON, sessionSpecSHA256, err := canonicalKernelExecutionSessionSpec(input.SessionSpec)
	if err != nil {
		return false
	}
	return backend.BackendID == input.BackendID && backend.OwnerUserID == input.OwnerUserID &&
		backend.ProjectID == input.ProjectID && backend.RootFrameID == input.RootFrameID &&
		backend.RootFrameIncarnationID == input.RootFrameIncarnationID && backend.FrameID == input.FrameID &&
		backend.FrameIncarnationID == input.FrameIncarnationID && backend.KernelID == input.KernelID &&
		backend.KernelGeneration == input.KernelGeneration && backend.ProtocolVersion == 1 &&
		backend.SessionSpecJSON == sessionSpecJSON && backend.SessionSpecSHA256 == sessionSpecSHA256 &&
		backend.ExecutorInstanceID == input.ExecutorInstanceID && backend.MachineBootID == input.MachineBootID &&
		backend.SocketPath == input.SocketPath && backend.BackendGeneration == input.BackendGeneration
}

type detachedKernelExecutionQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getKernelExecutionBackendQuery(
	ctx context.Context,
	query detachedKernelExecutionQuery,
	backendID string,
) (KernelExecutionBackend, bool, error) {
	var backend KernelExecutionBackend
	var executorPID, executorTicks, workerPID, workerTicks, workerPGID sql.NullInt64
	var heartbeatAt, controllerExpiry sql.NullString
	var createdAt, updatedAt string
	err := query.QueryRowContext(ctx, `SELECT backend_id,owner_user_id,project_id,root_frame_id,
		root_frame_incarnation_id,frame_id,frame_incarnation_id,kernel_id,kernel_generation,
		session_spec_json,session_spec_sha256,protocol_version,executor_instance_id,machine_boot_id,executor_pid,executor_pid_start_ticks,
		worker_pid,worker_pid_start_ticks,worker_pgid,cgroup_path,socket_path,backend_generation,
		state,state_version,heartbeat_sequence,heartbeat_at,controller_epoch,controller_token_sha256,
		controller_lease_expires_at,created_at,updated_at
		FROM kernel_execution_backends WHERE backend_id=?`, backendID).Scan(
		&backend.BackendID, &backend.OwnerUserID, &backend.ProjectID, &backend.RootFrameID,
		&backend.RootFrameIncarnationID, &backend.FrameID, &backend.FrameIncarnationID,
		&backend.KernelID, &backend.KernelGeneration, &backend.SessionSpecJSON, &backend.SessionSpecSHA256,
		&backend.ProtocolVersion,
		&backend.ExecutorInstanceID, &backend.MachineBootID, &executorPID, &executorTicks,
		&workerPID, &workerTicks, &workerPGID, &backend.CgroupPath, &backend.SocketPath,
		&backend.BackendGeneration, &backend.State, &backend.StateVersion, &backend.HeartbeatSequence,
		&heartbeatAt, &backend.ControllerEpoch, &backend.ControllerTokenSHA256, &controllerExpiry,
		&createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return KernelExecutionBackend{}, false, nil
	}
	if err != nil {
		return KernelExecutionBackend{}, false, err
	}
	backend.ExecutorPID, backend.ExecutorPIDStartTicks = executorPID.Int64, executorTicks.Int64
	backend.WorkerPID, backend.WorkerPIDStartTicks, backend.WorkerPGID = workerPID.Int64, workerTicks.Int64, workerPGID.Int64
	var parseErr error
	backend.HeartbeatAt, parseErr = parseDetachedOptionalTime(heartbeatAt)
	if parseErr != nil {
		return KernelExecutionBackend{}, false, parseErr
	}
	backend.ControllerLeaseExpiresAt, parseErr = parseDetachedOptionalTime(controllerExpiry)
	if parseErr != nil {
		return KernelExecutionBackend{}, false, parseErr
	}
	backend.CreatedAt, parseErr = time.Parse(time.RFC3339Nano, createdAt)
	if parseErr != nil {
		return KernelExecutionBackend{}, false, ErrKernelExecutionBackendConflict
	}
	backend.UpdatedAt, parseErr = time.Parse(time.RFC3339Nano, updatedAt)
	if parseErr != nil {
		return KernelExecutionBackend{}, false, ErrKernelExecutionBackendConflict
	}
	if backend.SessionSpecSHA256 != sha256HexString(backend.SessionSpecJSON) {
		return KernelExecutionBackend{}, false, ErrKernelExecutionBackendConflict
	}
	if _, decodeErr := DecodeKernelExecutionSessionSpecV1(backend.SessionSpecJSON); decodeErr != nil {
		return KernelExecutionBackend{}, false, ErrKernelExecutionBackendConflict
	}
	return backend, true, nil
}

func getDetachedKernelExecutionQuery(
	ctx context.Context,
	query detachedKernelExecutionQuery,
	executionID string,
) (DetachedKernelExecution, bool, error) {
	var execution DetachedKernelExecution
	var dispatchAt, writtenAt, startedAt, cancelAt, cancelAckAt sql.NullString
	var cancelAck sql.NullInt64
	var cancelID, cancelSignal, receiptID sql.NullString
	var acceptedAt, createdAt, updatedAt string
	err := query.QueryRowContext(ctx, `SELECT execution_id,operation_id,backend_id,backend_generation,
		request_json,request_sha256,confinement_sha256,state,state_version,dispatch_sequence,accepted_at,
		dispatch_committed_at,request_written_at,worker_started_at,last_observation_sequence,
		stdout_tail,cancel_request_id,cancel_requested_at,cancel_ack_sequence,cancel_ack_at,
		cancel_signal,terminal_receipt_id,reason_code,created_at,updated_at
		FROM kernel_detached_executions WHERE execution_id=?`, executionID).Scan(
		&execution.ExecutionID, &execution.OperationID, &execution.BackendID, &execution.BackendGeneration,
		&execution.RequestJSON, &execution.RequestSHA256, &execution.ConfinementSHA256, &execution.State, &execution.StateVersion,
		&execution.DispatchSequence, &acceptedAt, &dispatchAt, &writtenAt, &startedAt,
		&execution.LastObservationSequence, &execution.StdoutTail, &cancelID, &cancelAt,
		&cancelAck, &cancelAckAt, &cancelSignal, &receiptID, &execution.ReasonCode, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return DetachedKernelExecution{}, false, nil
	}
	if err != nil {
		return DetachedKernelExecution{}, false, err
	}
	execution.CancelRequestID, execution.CancelSignal, execution.TerminalReceiptID = cancelID.String, cancelSignal.String, receiptID.String
	execution.CancelAckSequence = cancelAck.Int64
	var parseErr error
	execution.AcceptedAt, parseErr = time.Parse(time.RFC3339Nano, acceptedAt)
	if parseErr != nil {
		return DetachedKernelExecution{}, false, ErrDetachedKernelExecutionConflict
	}
	execution.DispatchCommittedAt, parseErr = parseDetachedOptionalTime(dispatchAt)
	if parseErr != nil {
		return DetachedKernelExecution{}, false, parseErr
	}
	execution.RequestWrittenAt, parseErr = parseDetachedOptionalTime(writtenAt)
	if parseErr != nil {
		return DetachedKernelExecution{}, false, parseErr
	}
	execution.WorkerStartedAt, parseErr = parseDetachedOptionalTime(startedAt)
	if parseErr != nil {
		return DetachedKernelExecution{}, false, parseErr
	}
	execution.CancelRequestedAt, parseErr = parseDetachedOptionalTime(cancelAt)
	if parseErr != nil {
		return DetachedKernelExecution{}, false, parseErr
	}
	execution.CancelAckAt, parseErr = parseDetachedOptionalTime(cancelAckAt)
	if parseErr != nil {
		return DetachedKernelExecution{}, false, parseErr
	}
	execution.CreatedAt, parseErr = time.Parse(time.RFC3339Nano, createdAt)
	if parseErr != nil {
		return DetachedKernelExecution{}, false, ErrDetachedKernelExecutionConflict
	}
	execution.UpdatedAt, parseErr = time.Parse(time.RFC3339Nano, updatedAt)
	if parseErr != nil {
		return DetachedKernelExecution{}, false, ErrDetachedKernelExecutionConflict
	}
	if execution.RequestSHA256 != sha256HexString(execution.RequestJSON) {
		return DetachedKernelExecution{}, false, ErrDetachedKernelExecutionConflict
	}
	if _, decodeErr := DecodeKernelDetachedExecutionRequestV1(execution.RequestJSON); decodeErr != nil {
		return DetachedKernelExecution{}, false, ErrDetachedKernelExecutionConflict
	}
	return execution, true, nil
}

func parseDetachedOptionalTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil, ErrDetachedKernelExecutionConflict
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

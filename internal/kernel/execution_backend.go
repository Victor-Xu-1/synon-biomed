package kernel

import (
	"context"
	"io"
	"time"
)

// ExecutionBackend owns the physical kernel process lifetime independently of
// the Web service. Implementations may expose live observations, but durable
// execution identity and terminal receipts remain in the workspace database.
type ExecutionBackend interface {
	EnsureSession(context.Context, SessionSpec) (BackendSessionRef, error)
	AcquireSessionControl(context.Context, BackendSessionRef, time.Duration) (BackendControlLease, error)
	Start(context.Context, BackendExecutionRef, BackendStartFence) (BackendDispatchReceipt, error)
	AcquireControl(context.Context, BackendExecutionRef, time.Duration) (BackendControlLease, BackendExecutionSnapshot, error)
	RenewControl(context.Context, BackendControlLease, time.Duration) (BackendControlLease, error)
	Probe(context.Context, BackendExecutionRef, BackendControlLease) (BackendExecutionSnapshot, error)
	Watch(context.Context, BackendExecutionRef, BackendControlLease, int64) (ExecutionEventStream, error)
	Cancel(context.Context, BackendExecutionRef, BackendControlLease, BackendCancelRequest) (BackendCancelReceipt, error)
	CloseSession(context.Context, BackendSessionRef, BackendControlLease) error
}

type BackendSessionRef struct {
	BackendID          string `json:"backend_id"`
	BackendGeneration  int64  `json:"backend_generation"`
	KernelID           string `json:"kernel_id"`
	KernelGeneration   int64  `json:"kernel_generation"`
	ExecutorInstanceID string `json:"executor_instance_id"`
	SocketPath         string `json:"socket_path"`
}

type BackendExecutionRef struct {
	ExecutionID       string `json:"execution_id"`
	OperationID       string `json:"operation_id"`
	BackendID         string `json:"backend_id"`
	BackendGeneration int64  `json:"backend_generation"`
	RequestSHA256     string `json:"request_sha256"`
	ConfinementSHA256 string `json:"confinement_sha256"`
}

type BackendStartFence struct {
	ExecutionStateVersion int64  `json:"execution_state_version"`
	ControllerEpoch       int64  `json:"controller_epoch"`
	ControllerToken       string `json:"controller_token"`
	DispatchSequence      int64  `json:"dispatch_sequence"`
}

type BackendDispatchReceipt struct {
	ExecutionID         string    `json:"execution_id"`
	DispatchSequence    int64     `json:"dispatch_sequence"`
	StateVersion        int64     `json:"state_version"`
	DispatchCommittedAt time.Time `json:"dispatch_committed_at"`
}

type BackendControlLease struct {
	BackendID         string    `json:"backend_id"`
	BackendGeneration int64     `json:"backend_generation"`
	Epoch             int64     `json:"epoch"`
	Token             string    `json:"token"`
	ExpiresAt         time.Time `json:"expires_at"`
}

type BackendExecutionSnapshot struct {
	ExecutionID             string     `json:"execution_id"`
	State                   string     `json:"state"`
	StateVersion            int64      `json:"state_version"`
	DispatchSequence        int64      `json:"dispatch_sequence"`
	LastObservationSequence int64      `json:"last_observation_sequence"`
	StdoutTail              string     `json:"stdout_tail"`
	CancelRequestID         string     `json:"cancel_request_id"`
	TerminalReceiptID       string     `json:"terminal_receipt_id"`
	ReasonCode              string     `json:"reason_code"`
	StartedAt               *time.Time `json:"started_at,omitempty"`
	ObservedAt              time.Time  `json:"observed_at"`
}

type BackendCancelRequest struct {
	CancelRequestID string `json:"cancel_request_id"`
	ExpectedVersion int64  `json:"expected_version"`
	Reason          string `json:"reason,omitempty"`
}

type BackendCancelReceipt struct {
	ExecutionID     string `json:"execution_id"`
	CancelRequestID string `json:"cancel_request_id"`
	StateVersion    int64  `json:"state_version"`
	Acknowledged    bool   `json:"acknowledged"`
	AckSequence     int64  `json:"ack_sequence"`
	Signal          string `json:"signal"`
}

type BackendExecutionEvent struct {
	ExecutionID string    `json:"execution_id"`
	Sequence    int64     `json:"sequence"`
	Type        string    `json:"type"`
	Data        string    `json:"data"`
	ObservedAt  time.Time `json:"observed_at"`
}

type ExecutionEventStream interface {
	Recv(context.Context) (BackendExecutionEvent, error)
	Close() error
}

var _ io.Closer = (ExecutionEventStream)(nil)

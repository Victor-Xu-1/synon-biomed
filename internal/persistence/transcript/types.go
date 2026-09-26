package transcript

import (
	"context"
	"errors"
	"time"
)

type StreamKind string

const (
	StreamKindFrameRef   StreamKind = "frame_ref"
	StreamKindTaskRun    StreamKind = "taskrun"
	StreamKindStandalone StreamKind = "standalone"
)

type EventSource string

const (
	EventSourceFrameRef EventSource = "frame_ref"
	EventSourcePayload  EventSource = "payload"
)

type ResumeSource string

const (
	ResumeSourceFresh      ResumeSource = "fresh"
	ResumeSourceCheckpoint ResumeSource = "checkpoint"
	ResumeSourceRetry      ResumeSource = "retry"
	ResumeSourceUserInput  ResumeSource = "user_input"
)

type RunnerPhase string

const (
	RunnerPhaseClaimed         RunnerPhase = "claimed"
	RunnerPhasePlanning        RunnerPhase = "planning"
	RunnerPhaseExecuting       RunnerPhase = "executing"
	RunnerPhaseWaitingUser     RunnerPhase = "waiting_user"
	RunnerPhaseWaitingApproval RunnerPhase = "waiting_approval"
	RunnerPhaseWaitingExternal RunnerPhase = "waiting_external"
	RunnerPhaseTerminal        RunnerPhase = "terminal"
)

var (
	ErrSchemaUnavailable             = errors.New("transcript schema is unavailable")
	ErrOwnerMismatch                 = errors.New("transcript owner does not match")
	ErrClaimStale                    = errors.New("transcript runner claim is stale")
	ErrEventConflict                 = errors.New("transcript event conflicts with durable state")
	ErrArtifactMismatch              = errors.New("artifact version does not belong to transcript authority")
	ErrArtifactMissing               = errors.New("artifact version is unavailable")
	ErrDeliveryClaimStale            = errors.New("transcript delivery claim is stale")
	ErrCheckpointUnavailable         = errors.New("transcript checkpoint is unavailable")
	ErrReservedEventType             = errors.New("transcript event type requires a typed authority")
	ErrDeliveryRouteInactive         = errors.New("transcript delivery route is inactive")
	ErrDeliveryRouteStale            = errors.New("transcript delivery route generation is stale")
	ErrDeliveryRouteImmutable        = errors.New("transcript delivery route is immutable")
	ErrTerminalProjectionUnavailable = errors.New("transcript terminal projection is unavailable")
	ErrBranchStateStale              = errors.New("transcript branch state is stale")
	ErrProjectionResourceLimit       = errors.New("transcript projection resource limit exceeded")
	ErrMemoryMutationReceiptConflict = errors.New("transcript memory mutation receipt conflicts with durable state")
	ErrOpenToolBatch                 = errors.New("transcript provider replay contains an open tool batch")
	ErrProviderReplayWindowTooLarge  = errors.New("transcript provider replay window exceeds its hard budget")
)

type Stream struct {
	UID                   string
	OwnerID               string
	ExternalID            string
	SessionID             string
	Kind                  StreamKind
	ProjectID             string
	RootFrameID           string
	FrameID               string
	Epoch                 int64
	InputRevision         int64
	ConsumedInputRevision int64
	NextEventID           int64
	NextPublication       int64
	NextCheckpoint        int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type FrameAuthority struct {
	OwnerID             string
	SessionID           string
	ActiveStreamUID     string
	ActiveEpoch         int64
	AuthorityGeneration int64
	ReadAuthority       string
	WriteAuthority      string
	ActivationID        []byte
	GenesisID           []byte
	UpdatedAt           time.Time
}

// FrameRealtimeRebase is the durable boundary between the legacy workspace
// realtime sequence and an activated transcript payload epoch. Realtime
// delivery may use this receipt to discard pre-activation events for this
// frame and instruct clients to replace, rather than merge, message history.
type FrameRealtimeRebase struct {
	OwnerID             string
	SessionID           string
	ProjectID           string
	RootFrameID         string
	ActiveStreamUID     string
	ActiveEpoch         int64
	AuthorityGeneration int64
	ActivationSHA256    string
	ActiveBranchID      string
	BranchGeneration    int64
	RealtimeHighWater   int64
	RebaseSequence      int64
	ActivatedAt         time.Time
}

type ActivatedLegacyCursor struct {
	TargetBranchID     string
	TargetMessageIndex int
	StableMessageID    string
}

func (authority FrameAuthority) TranscriptPayloadActive() bool {
	return authority.ReadAuthority == "transcript_payload_v1" &&
		authority.WriteAuthority == "transcript_payload_v1" &&
		((len(authority.ActivationID) == 32 && len(authority.GenesisID) == 0) ||
			(len(authority.ActivationID) == 0 && len(authority.GenesisID) == 32))
}

func (authority FrameAuthority) LegacyActivationActive() bool {
	return authority.ReadAuthority == "transcript_payload_v1" &&
		authority.WriteAuthority == "transcript_payload_v1" &&
		len(authority.ActivationID) == 32 && len(authority.GenesisID) == 0
}

// CanonicalProjectionReadable reports whether Web history is served from the
// active Transcript stream. Legacy frame-ref streams and activated payload
// streams both expose canonical visible message coordinates; their write and
// cursor-translation rules remain distinct.
func (authority FrameAuthority) CanonicalProjectionReadable() bool {
	return authority.TranscriptPayloadActive() ||
		(authority.ReadAuthority == "legacy_mixed_v1" &&
			authority.WriteAuthority == "legacy_frame_ref_v1" &&
			len(authority.ActivationID) == 0 && len(authority.GenesisID) == 0)
}

type CreateStreamInput struct {
	UID         string
	OwnerID     string
	ExternalID  string
	SessionID   string
	Kind        StreamKind
	ProjectID   string
	RootFrameID string
	FrameID     string
	Epoch       int64
}

type StageUserEventResult struct {
	Event    Event
	Created  bool
	Admitted bool
}

type AdmitUserEventInput struct {
	StreamUID       string
	OwnerID         string
	ClientMessageID string
}

type RunnerClaim struct {
	StreamUID               string
	OwnerID                 string
	RunnerID                string
	Attempt                 int64
	ClaimToken              string
	ClaimedInputRevision    int64
	ResumeSource            ResumeSource
	ResumeCheckpoint        int64
	ResumeCheckpointAttempt int64
	ClaimedAt               time.Time
	ExpiresAt               time.Time
}

type ClaimRunnerInput struct {
	StreamUID        string
	OwnerID          string
	RunnerID         string
	TTL              time.Duration
	ResumeSource     ResumeSource
	ResumeCheckpoint int64
}

type ClaimRunnerResult struct {
	Claim         RunnerClaim
	Claimed       bool
	OwnerRunnerID string
}

type ClaimNextFrameRunnerInput struct {
	RunnerID string
	TTL      time.Duration
}

type ClaimNextFrameRunnerResult struct {
	Stream  Stream
	Claim   RunnerClaim
	Claimed bool
}

type ClaimNextRunnerInput struct {
	RunnerID string
	TTL      time.Duration
}

type ClaimNextRunnerResult struct {
	Stream  Stream
	Claim   RunnerClaim
	Claimed bool
}

type HeartbeatRunnerInput struct {
	Claim RunnerClaim
	TTL   time.Duration
}

type HeartbeatRunnerResult struct {
	Renewed   bool
	ExpiresAt time.Time
}

type AppendUserEventInput struct {
	StreamUID       string
	OwnerID         string
	ClientMessageID string
	PayloadJSON     []byte
	Destinations    []string
}

// StartInternalFrameRunnerInput atomically creates an internal Frame stream,
// admits its initial user event, and claims its runner. Keeping these three
// mutations in one transaction prevents the general runner dispatcher from
// claiming internal reviewer work in the gap between input admission and the
// dedicated reviewer claim.
type StartInternalFrameRunnerInput struct {
	Stream    CreateStreamInput
	UserEvent AppendUserEventInput
	RunnerID  string
	TTL       time.Duration
}

type StartInternalFrameRunnerResult struct {
	Stream       Stream
	Event        Event
	Claim        RunnerClaim
	EventCreated bool
	Claimed      bool
}

type AppendFrameUserEventInput struct {
	StreamUID          string
	OwnerID            string
	ClientMessageID    string
	FrameEventID       string
	MessageUUID        string
	Text               string
	MessageOrigin      string
	InputData          map[string]any
	RuntimeConfig      map[string]any
	ArtifactReferences []UserArtifactReferenceInput
	MessageContext     string
	Destinations       []string
	// DeferFrameActivation persists the input while leaving a terminal frame
	// unchanged. Recovery flows use this to make the input durable before they
	// publish a claimable resume dispatch.
	DeferFrameActivation bool
}

type UserArtifactReferenceInput struct {
	ArtifactID string `json:"artifact_id"`
	VersionID  string `json:"version_id"`
}

type UserArtifactReference struct {
	ArtifactID  string `json:"artifact_id"`
	VersionID   string `json:"version_id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	Checksum    string `json:"checksum"`
}

type AppendFrameInputResponseInput struct {
	StreamUID       string
	OwnerID         string
	FrameID         string
	ClientMessageID string
	FrameEventID    string
	PayloadJSON     []byte
	Destinations    []string
}

type FrameReferenceEvent struct {
	ID          string
	FrameID     string
	Sequence    int64
	Type        string
	PayloadJSON []byte
	CreatedAt   time.Time
}

type FrameTaskIntent struct {
	ID              string
	FrameID         string
	Revision        int64
	SourceEventID   string
	SourceMessageID string
	Origin          string
	Language        string
	Text            string
	CreatedAt       time.Time
}

type Event struct {
	StreamUID       string
	EventID         int64
	PublicationSeq  int64
	ClientMessageID string
	Type            string
	Source          EventSource
	RunnerAttempt   *int64
	PayloadJSON     []byte
	FrameEventID    *string
	CreatedAt       time.Time
}

type AppendEventInput struct {
	Claim           RunnerClaim
	ClientMessageID string
	Type            string
	Source          EventSource
	PayloadJSON     []byte
	FrameEventID    *string
	Destinations    []string
}

type MemoryMutationReceiptResult struct {
	Appended []string `json:"appended"`
	Replaced []string `json:"replaced"`
	Removed  []string `json:"removed"`
}

type MemoryMutationReceiptInput struct {
	Claim          RunnerClaim
	SourceEventID  int64
	InputSHA256    string
	MutationSHA256 string
	Result         MemoryMutationReceiptResult
}

type MemoryMutationReceipt struct {
	Event          Event
	SourceEventID  int64
	InputSHA256    string
	MutationSHA256 string
	Result         MemoryMutationReceiptResult
}

type MemoryMutationInvocation struct {
	Stream          Stream
	SourceEventID   int64
	SourceAttempt   int64
	ToolCallID      string
	ToolInputJSON   []byte
	InputSHA256     string
	ClientMessageID string
	Receipt         *MemoryMutationReceipt
}

type FinishRunnerInput struct {
	Claim           RunnerClaim
	ClientMessageID string
	Status          string
	PayloadJSON     []byte
	Destinations    []string
}

type FinishReceipt struct {
	StreamUID  string
	Attempt    int64
	EventID    int64
	Status     string
	FinishedAt time.Time
}

type TerminalProjection struct {
	StreamUID          string
	OwnerID            string
	SessionID          string
	ProjectID          string
	RootFrameID        string
	FrameID            string
	Attempt            int64
	EventID            int64
	PublicationSeq     int64
	StreamType         string
	TerminalStatus     string
	Superseded         bool
	Detail             string
	ReasonCode         string
	CreatedAt          time.Time
	ArtifactReferences []ArtifactReference
}

type CancelRunnerInput struct {
	StreamUID       string
	OwnerID         string
	ExpectedAttempt int64
	ClientMessageID string
	ReasonCode      string
	Destinations    []string
}

type CancelRunnerResult struct {
	Applied       bool
	CurrentStatus string
	Event         Event
	Receipt       FinishReceipt
}

// InterruptRunnerInput records a resumable infrastructure interruption for
// an exact live runner claim. Unlike cancellation or failure, interruption
// never consumes the claimed input or moves the Frame to a terminal state.
type InterruptRunnerInput struct {
	Claim                    RunnerClaim
	ClientMessageID          string
	ReasonCode               string
	ResumeDetail             string
	Cause                    *RunnerInterruptionCause
	RecoveryContractRevision int64
	// Resumable preserves an exact checkpoint for an explicit user or
	// configuration action without making the interruption eligible for
	// unattended execution. AutoResume remains the independent scheduler flag.
	Resumable    bool
	AutoResume   bool
	Destinations []string
}

type InterruptRunnerResult struct {
	Checkpoint RunnerCheckpoint
	Event      Event
	Created    bool
}

type AppendRunnerCheckpointInput struct {
	Claim           RunnerClaim
	ClientMessageID string
	Phase           RunnerPhase
	Resumable       bool
	PayloadJSON     []byte
	Destinations    []string
	CommitHook      RunnerCheckpointCommitHook
}

// RunnerCheckpointCommitHook joins a dependent durable authority to the
// canonical checkpoint transaction. The hook must be deterministic and may
// only mutate the same SQLite database through tx. Returning an error rolls
// back both the checkpoint and the dependent authority.
type RunnerCheckpointCommitHook func(
	ctx context.Context,
	tx *ImmediateTransaction,
	event Event,
	created bool,
) (RunnerCheckpointCommitReceipt, error)

type RunnerCheckpointCommitReceipt struct {
	ToolBatch        *RunnerCheckpointToolBatchReceipt
	KernelOperations []RunnerCheckpointKernelOperationReceipt
}

type RunnerCheckpointToolBatchReceipt struct {
	BatchID   string
	CallCount int64
}

type RunnerCheckpointKernelOperationReceipt struct {
	OperationID         string
	Ordinal             int64
	ToolCallID          string
	Tool                string
	InputSHA256         string
	ApprovalRequestID   string
	RequestedEventID    string
	InitialStateVersion int64
}

type RunnerCheckpoint struct {
	StreamUID string
	Sequence  int64
	Attempt   int64
	EventID   int64
	Phase     RunnerPhase
	Resumable bool
	CreatedAt time.Time
}

type RunnerRuntimeState struct {
	StreamUID              string
	Attempt                int64
	RunnerID               string
	ClaimedInputRevision   int64
	Status                 string
	Phase                  RunnerPhase
	PhaseSequence          int64
	LastCheckpointSequence int64
	ResumeSource           ResumeSource
	ResumeCheckpoint       int64
	ClaimedAt              time.Time
	ExpiresAt              time.Time
	FinishedEventID        int64
	FinishedAt             *time.Time
}

// RunnerTimingSummary is the durable active-work clock for the latest logical
// user input. Retries of the same input revision accumulate their claimed
// intervals, while idle gaps and prior input revisions are excluded.
type RunnerTimingSummary struct {
	StreamUID       string
	Attempt         int64
	InputRevision   int64
	Status          string
	StartedAt       time.Time
	FinishedAt      *time.Time
	ObservedAt      time.Time
	Elapsed         time.Duration
	Active          bool
	FinishedEventID int64
}

// RunnerTaskTimingSummary is the durable active-work clock for the entire
// frame task. Every runner attempt contributes its claimed interval, while
// idle gaps between logical user-input revisions remain excluded.
type RunnerTaskTimingSummary struct {
	StreamUID     string
	Attempt       int64
	InputRevision int64
	Status        string
	StartedAt     time.Time
	FinishedAt    *time.Time
	ObservedAt    time.Time
	Elapsed       time.Duration
	Active        bool
}

type DeliveryIntent struct {
	StreamUID       string
	PublicationSeq  int64
	Destination     string
	RouteGeneration int64
	Status          string
	AttemptCount    int
	WorkerID        string
	LeaseExpiresAt  *time.Time
	NextAttemptAt   *time.Time
	LastErrorCode   string
	LastRetryAfter  time.Duration
	LastMaxAttempts int
	DeliveredAt     *time.Time
	UpdatedAt       time.Time
}

type DeliveryClaim struct {
	StreamUID           string
	OwnerID             string
	PublicationSeq      int64
	Destination         string
	RouteGeneration     int64
	WorkerID            string
	ClaimToken          string
	AttemptCount        int
	ExpiresAt           time.Time
	Event               Event
	ResolvedPayloadJSON []byte
	ArtifactReferences  []ArtifactReference
}

type ClaimDeliveryInput struct {
	OwnerID     string
	Destination string
	WorkerID    string
	TTL         time.Duration
}

type ClaimDeliveryResult struct {
	Claim   DeliveryClaim
	Claimed bool
}

type AcknowledgeDeliveryInput struct {
	Claim DeliveryClaim
}

type HeartbeatDeliveryInput struct {
	Claim DeliveryClaim
	TTL   time.Duration
}

type HeartbeatDeliveryResult struct {
	Renewed   bool
	ExpiresAt time.Time
}

type FailDeliveryInput struct {
	Claim       DeliveryClaim
	ErrorCode   string
	RetryAfter  time.Duration
	MaxAttempts int
}

type DeliveryTransitionResult struct {
	Applied bool
	Status  string
}

type DeliveryRoute struct {
	StreamUID   string
	Destination string
	Generation  int64
	Status      string
	UpdatedAt   time.Time
}

type ArtifactRelation string

const (
	ArtifactRelationProduced ArtifactRelation = "produced"
	ArtifactRelationConsumed ArtifactRelation = "consumed"
	ArtifactRelationCited    ArtifactRelation = "cited"
	ArtifactRelationAttached ArtifactRelation = "attached"
)

type ArtifactAvailability string

const (
	ArtifactAvailable ArtifactAvailability = "available"
	ArtifactDeleted   ArtifactAvailability = "deleted"
	ArtifactMissing   ArtifactAvailability = "missing"
)

type ArtifactReferenceInput struct {
	ArtifactID string
	VersionID  string
	Relation   ArtifactRelation
}

type AppendAssistantEventWithArtifactsInput struct {
	Claim           RunnerClaim
	ClientMessageID string
	Source          EventSource
	PayloadJSON     []byte
	FrameEventID    *string
	Destinations    []string
	References      []ArtifactReferenceInput
}

type ProjectedEvent struct {
	Event               Event
	ResolvedPayloadJSON []byte
	ArtifactReferences  []ArtifactReference
}

type ProjectionSnapshot struct {
	StreamUID                  string
	BranchID                   string
	BranchGeneration           int64
	ThroughPublicationSequence int64
}

type ListProjectedEventsInput struct {
	StreamUID                  string
	OwnerID                    string
	BranchID                   string
	BranchGeneration           int64
	AfterPublicationSequence   int64
	ThroughPublicationSequence int64
	Limit                      int
}

type ListRunnerReplayInput struct {
	StreamUID       string
	OwnerID         string
	MessageLimit    int
	CheckpointLimit int
	// RequiredCheckpointEventID pins one caller-resolved canonical checkpoint
	// and its complete tool transaction independently of the ordinary seed size.
	RequiredCheckpointEventID int64
	// MaxExpandedEvents and MaxExpandedBytes are hard limits applied after
	// transaction closure. Zero selects the bounded defaults; closure is never
	// truncated to satisfy either limit.
	MaxExpandedEvents int
	MaxExpandedBytes  int64
}

type RunnerReplayEvent struct {
	Event               Event
	ResolvedPayloadJSON []byte
}

type ArtifactReference struct {
	StreamUID     string
	RunnerAttempt int64
	SourceEventID int64
	Ordinal       int
	ArtifactID    string
	VersionID     string
	Relation      ArtifactRelation
	Availability  ArtifactAvailability
	CreatedAt     time.Time
}

// ArtifactCommitSnapshot is one owner-scoped, branch-consistent recovery view
// of the current immutable artifact heads through a runner attempt.
type ArtifactCommitSnapshot struct {
	StreamUID        string
	BranchID         string
	BranchGeneration int64
	ThroughAttempt   int64
	References       []ArtifactReference
}

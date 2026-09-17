package server

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
	"synon-go/internal/providers"
	"synon-go/internal/sciencecapability"
	runnermachine "synon-go/internal/sessionrunner"
)

type SessionRunnerChatOptions struct {
	SessionID                         string
	RunnerID                          string
	Endpoint                          string
	APIKey                            string
	Model                             string
	SystemPrompt                      string
	AllowedTools                      []string
	SelectedSkillNames                []string
	ExcludedSkillNames                []string
	AllowedSkillNames                 []string
	RestrictSkillDiscovery            bool
	RequestTimeout                    time.Duration
	PreparationTimeout                time.Duration
	CompactSummaryTimeout             time.Duration
	MaxToolRounds                     int
	MaxToolCallsPerRound              int
	MaxConsecutiveIdenticalToolRounds int
	MaxAttempts                       int
	LeaseTTL                          time.Duration
	PollInterval                      time.Duration
	ReplayLimit                       int64
	OutputLimitBytes                  int64
	ModelResponseLimitBytes           int64
	TranscriptResumeSource            transcriptstore.ResumeSource
	TranscriptCheckpoint              int64
	DisableSkillDiscovery             bool
	DisableMCPDiscovery               bool
	DisableThinking                   bool
	ModelProfile                      *providers.ModelProfile
	ModelAudit                        func(providers.AuditRecord)
	RequireSavedModel                 bool
	sourceToolActivity                *sessionRunnerSourceToolActivity
	RuntimeSessionConfig              map[string]any
}

type sessionRunnerChatRun struct {
	SessionID                          string
	Attempt                            int
	ClaimToken                         string
	AfterEventID                       int64
	Transcript                         *transcriptRunnerAuthority
	TaskIntent                         string
	TaskIntentID                       string
	TaskIntentRevision                 int64
	ResponseLanguage                   string
	ToolSourceEventIDs                 map[string]int64
	KernelOperationIDs                 map[string]string
	ToolBatchIDs                       map[string]string
	ToolBatchOrdinals                  map[string]int64
	AssistantSegmentOrdinal            int64
	AssistantSegmentHasContent         bool
	AssistantSegmentContent            strings.Builder
	PlanModeDenials                    int
	AutonomousPlanning                 bool
	planProgressAvailable              atomic.Bool
	ProviderContinuation               *sessionRunnerProviderContinuationState
	ProviderAttemptSemanticBytes       int64
	NoProgressRecovery                 *sessionRunnerNoProgressRecovery
	managedEnvironmentMu               sync.Mutex
	managedEnvironmentBindings         map[string]string
	managedEnvironmentImplementations  map[string][]string
	managedEnvironmentInvalidations    map[string]managedEnvironmentInvalidation
	managedEnvironmentBindingsHydrated bool
	managedEnvironmentHydration        *managedEnvironmentHydration
	readReuse                          *sessionRunnerReadReuseCache
	scientificCapabilitiesMu           sync.Mutex
	toolCapabilityCatalog              map[string][]string
	activatedToolNames                 []string
	activatedToolCapabilities          []string
	InputAttachmentReaders             []sessionRunnerInputAttachmentReader
	executedSkillInvocationKeys        map[string]struct{}
	ReviewPolicy                       *sessionRunnerResolvedReviewPolicy
	// VerificationExplicitlyDisabled records the resolved user-owned
	// verifier mode. It keeps ordinary completion and artifact saving from
	// falling back to legacy strict-review behavior when review is off.
	VerificationExplicitlyDisabled       bool
	RequiredScientificCapabilities       []string
	ExecutedSkillNames                   []string
	PendingRequiredSkillNames            []string
	SelectedImplementations              []string
	SelectedEvidenceResolvers            []sciencecapability.ExecutionEvidenceResolver
	RegistrySelectedImplementations      []string
	ResolvedUserEvidence                 map[string]string
	ImplementationSelectionRequired      bool
	TrustedScientificReviewSignals       []string
	TrustedScientificCapabilityWitnesses []sciencecapability.Witness
	ContinuationArtifactReferences       []transcriptstore.ArtifactReferenceInput
	ScientificEvidenceVerified           bool
	CorrectionReason                     string
	CorrectionDetail                     string
	requiredMCPSourceClass               string
	phaseMachine                         *runnermachine.Machine
	// SuppressReviewCheckpoints keeps a detached, user-triggered review from
	// appending checkpoints to an already-terminal main runner. The reviewer
	// still owns its hidden child-frame transcript and durable verification
	// records; the completed task transcript remains immutable.
	SuppressReviewCheckpoints bool
}

package taskruns

import (
	"sync"
	"time"
)

const SchemaVersion = 1

type Input struct {
	Action          string   `json:"action"`
	RunID           string   `json:"run_id"`
	Objective       string   `json:"objective"`
	SuccessCriteria []string `json:"success_criteria"`
	Constraints     []string `json:"constraints"`
	ExecutorScope   []string `json:"executor_scope"`
	TaskGraph       *Graph   `json:"task_graph"`
	Message         string   `json:"message"`
	// Orchestration is internal runtime metadata. It is never accepted from a
	// direct TaskRun request and cannot become a second planning authority.
	Orchestration map[string]any `json:"-"`
}

type Graph struct {
	Steps     []StepInput      `json:"steps"`
	Artifacts []map[string]any `json:"artifacts,omitempty"`
}

type StepInput struct {
	ID              string         `json:"id"`
	Title           string         `json:"title"`
	Description     string         `json:"description"`
	Intent          string         `json:"intent,omitempty"`
	ExpectedOutput  string         `json:"expected_output,omitempty"`
	AcceptanceCheck string         `json:"acceptance_check,omitempty"`
	RiskLevel       string         `json:"risk_level,omitempty"`
	MaxRecoveries   int            `json:"max_recoveries,omitempty"`
	DependsOn       []string       `json:"depends_on,omitempty"`
	Executor        map[string]any `json:"executor"`
}

type Record struct {
	SchemaVersion   int             `json:"schema_version"`
	RunID           string          `json:"run_id"`
	GoalLedgerRunID string          `json:"goal_ledger_run_id,omitempty"`
	Playbook        string          `json:"playbook,omitempty"`
	PlaybookOptions map[string]any  `json:"playbook_options,omitempty"`
	Orchestration   map[string]any  `json:"orchestration,omitempty"`
	Status          string          `json:"status"`
	Objective       string          `json:"objective"`
	SuccessCriteria []string        `json:"success_criteria"`
	Constraints     []string        `json:"constraints"`
	ExecutorScope   []string        `json:"executor_scope"`
	CreatedAt       int64           `json:"created_at"`
	UpdatedAt       int64           `json:"updated_at"`
	OutputPath      string          `json:"output_path"`
	SessionID       string          `json:"session_id,omitempty"`
	Steps           []Step          `json:"steps"`
	ActiveChildren  []ActiveChild   `json:"active_children"`
	Artifacts       []Artifact      `json:"artifacts"`
	EvidenceIndex   []Evidence      `json:"evidence_index"`
	Blockers        []Blocker       `json:"blockers"`
	NextActions     []string        `json:"next_actions"`
	Acceptance      []Acceptance    `json:"acceptance"`
	Quality         Quality         `json:"quality"`
	Completion      Completion      `json:"completion"`
	SelfCheck       SelfCheck       `json:"self_check"`
	RepairPolicy    RepairPolicy    `json:"repair_policy"`
	ExecutionTrace  []TraceEvent    `json:"execution_trace"`
	CapabilityGaps  []CapabilityGap `json:"capability_gaps"`
	History         []HistoryEvent  `json:"history"`
}

type Step struct {
	ID              string         `json:"id"`
	Title           string         `json:"title"`
	Description     string         `json:"description"`
	Intent          string         `json:"intent"`
	ExpectedOutput  string         `json:"expected_output"`
	AcceptanceCheck string         `json:"acceptance_check"`
	RiskLevel       string         `json:"risk_level"`
	MaxRecoveries   int            `json:"max_recoveries"`
	DependsOn       []string       `json:"depends_on,omitempty"`
	Executor        map[string]any `json:"executor"`
	Status          string         `json:"status"`
	ChildTaskID     string         `json:"child_task_id,omitempty"`
	OutputPath      string         `json:"output_path,omitempty"`
	StartedAt       int64          `json:"started_at,omitempty"`
	CompletedAt     int64          `json:"completed_at,omitempty"`
	UpdatedAt       int64          `json:"updated_at"`
	RecoveryCount   int            `json:"recovery_count"`
	Blocker         *Blocker       `json:"blocker,omitempty"`
	Error           string         `json:"error,omitempty"`
	Artifacts       []Artifact     `json:"artifacts,omitempty"`
}

type ActiveChild struct {
	TaskID     string `json:"task_id"`
	StepID     string `json:"step_id"`
	Executor   string `json:"executor"`
	OutputPath string `json:"output_path,omitempty"`
}

type Artifact struct {
	StepID      string         `json:"step_id"`
	Kind        string         `json:"kind"`
	Path        string         `json:"path,omitempty"`
	Description string         `json:"description"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

type Evidence struct {
	ID         string         `json:"id"`
	StepID     string         `json:"step_id"`
	Kind       string         `json:"kind"`
	Path       string         `json:"path,omitempty"`
	Summary    string         `json:"summary"`
	ProducedAt int64          `json:"produced_at"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

type Blocker struct {
	Kind                string   `json:"kind"`
	Message             string   `json:"message"`
	StepID              string   `json:"step_id,omitempty"`
	ClientKind          string   `json:"client_kind,omitempty"`
	MissingCapabilities []string `json:"missing_capabilities,omitempty"`
}

type Acceptance struct {
	ID           string `json:"id"`
	Description  string `json:"description"`
	Status       string `json:"status"`
	StepID       string `json:"step_id,omitempty"`
	EvidencePath string `json:"evidence_path,omitempty"`
	Details      string `json:"details,omitempty"`
	UpdatedAt    int64  `json:"updated_at"`
}

type Quality struct {
	Score                    int     `json:"score"`
	StepCompletionRate       float64 `json:"step_completion_rate"`
	AcceptancePassRate       float64 `json:"acceptance_pass_rate"`
	ArtifactCoverageRate     float64 `json:"artifact_coverage_rate"`
	RecoveryPenalty          int     `json:"recovery_penalty"`
	IssueResolutionRate      float64 `json:"issue_resolution_rate,omitempty"`
	VerificationCoverageRate float64 `json:"verification_coverage_rate,omitempty"`
	Freshness                float64 `json:"freshness,omitempty"`
	UpdatedAt                int64   `json:"updated_at"`
}

type Completion struct {
	State      string `json:"state"`
	Verified   bool   `json:"verified"`
	Confidence string `json:"confidence"`
	Reason     string `json:"reason"`
	NextAction string `json:"next_action,omitempty"`
	UpdatedAt  int64  `json:"updated_at"`
}

type Issue struct {
	Severity   string `json:"severity"`
	Kind       string `json:"kind"`
	Message    string `json:"message"`
	StepID     string `json:"step_id,omitempty"`
	Repairable bool   `json:"repairable"`
}

type SelfCheck struct {
	Status           string         `json:"status"`
	Rounds           int            `json:"rounds"`
	Issues           []Issue        `json:"issues"`
	RepairsAttempted int            `json:"repairs_attempted"`
	RemainingIssues  []Issue        `json:"remaining_issues"`
	Recommendations  []string       `json:"recommendations"`
	StopReason       string         `json:"stop_reason,omitempty"`
	LastCheckedAt    int64          `json:"last_checked_at,omitempty"`
	Metrics          map[string]any `json:"metrics,omitempty"`
}

type RepairPolicy struct {
	Status          string           `json:"status"`
	Mode            string           `json:"mode"`
	MaxRounds       int              `json:"max_rounds"`
	RoundsAttempted int              `json:"rounds_attempted"`
	Queueable       bool             `json:"queueable"`
	Blocked         bool             `json:"blocked"`
	Reason          string           `json:"reason"`
	Decisions       []RepairDecision `json:"decisions"`
	LastEvaluatedAt int64            `json:"last_evaluated_at,omitempty"`
}

type RepairDecision struct {
	IssueKind  string `json:"issue_kind"`
	Severity   string `json:"severity"`
	StepID     string `json:"step_id,omitempty"`
	Action     string `json:"action"`
	Repairable bool   `json:"repairable"`
	Reason     string `json:"reason"`
}

type TraceEvent struct {
	At       int64          `json:"at"`
	Event    string         `json:"event"`
	StepID   string         `json:"step_id,omitempty"`
	Message  string         `json:"message,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type CapabilityGap struct {
	Kind                string   `json:"kind"`
	Message             string   `json:"message"`
	StepID              string   `json:"step_id,omitempty"`
	ClientKind          string   `json:"client_kind,omitempty"`
	MissingCapabilities []string `json:"missing_capabilities,omitempty"`
}

type HistoryEvent struct {
	At      int64  `json:"at"`
	Event   string `json:"event"`
	Message string `json:"message,omitempty"`
}

type Store struct {
	root string
	mu   sync.Mutex
	now  func() time.Time
}

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"synon-go/internal/executionprep"
	"synon-go/internal/networkpolicy"
	"synon-go/internal/networktls"
)

const (
	defaultManagedInterruptGrace = 5 * time.Second
	defaultExecutionStartTimeout = 5 * time.Second
	maxManagedKernelCodeBytes    = 100_000
	maxExecStreamStdoutBytes     = 10 * 1024 * 1024
)

type SessionSpec struct {
	KernelID               string
	OwnerID                string
	ProjectID              string
	FrameID                string
	FrameIncarnationID     string
	RootFrameID            string
	RootFrameIncarnationID string
	AgentName              string
	DelegateName           string
	KernelKind             string
	Language               string
	Environment            string
	RuntimeGeneration      string
	WorkspaceDir           string
	Mounts                 []WorkerMount
	ProtectedPaths         []string
	EgressAllowedDomains   []string
	EgressDeniedDomains    []string
	CABundle               string
	UpstreamProxy          string
	Fresh                  bool
}

type WorkerMount struct {
	Path     string
	Writable bool
	regular  bool
	trusted  bool
	frozen   *os.File
}

// TrustedReadOnlyDirectoryMount marks a server-owned, pre-verified runtime
// directory (for example a bundled Skill tree) as a read-only confinement
// mount. Host grants must never use this constructor.
func TrustedReadOnlyDirectoryMount(path string) WorkerMount {
	return WorkerMount{Path: path, trusted: true}
}

// IsTrustedReadOnlyDirectory reports whether this mount was created from a
// server-owned, pre-verified directory. Detached execution persists this bit
// so that recovery applies the same confinement policy as the live manager.
func (mount WorkerMount) IsTrustedReadOnlyDirectory() bool {
	return mount.trusted && !mount.Writable && !mount.regular
}

type CurrentCell struct {
	Source           string    `json:"source"`
	Origin           string    `json:"origin"`
	StartedAt        time.Time `json:"started_at"`
	HumanDescription string    `json:"human_description,omitempty"`
	Truncated        bool      `json:"truncated,omitempty"`
}

type LastCell struct {
	Source  string    `json:"source"`
	EndedAt time.Time `json:"ended_at"`
}

// SessionKernel is the v1.1 live inventory projection. RootFrameID is exposed
// for project-level grouping while incarnation fences remain internal.
type SessionKernel struct {
	FrameID                string                `json:"frame_id"`
	FrameIncarnationID     string                `json:"-"`
	RootFrameIncarnationID string                `json:"-"`
	RootFrameID            string                `json:"root_frame_id"`
	ProjectID              string                `json:"project_id"`
	AgentName              string                `json:"agent_name"`
	ProjectName            string                `json:"project_name,omitempty"`
	SessionTitle           string                `json:"session_title,omitempty"`
	LastDescription        string                `json:"last_description,omitempty"`
	LastCell               *LastCell             `json:"last_cell"`
	Environment            string                `json:"environment"`
	RuntimeGeneration      string                `json:"runtime_generation,omitempty"`
	Language               string                `json:"language"`
	KernelID               string                `json:"kernel_id"`
	Busy                   bool                  `json:"busy"`
	Starting               bool                  `json:"starting"`
	PIDVisible             bool                  `json:"pid_visible"`
	RSSBytes               *uint64               `json:"rss_bytes"`
	CPUPct                 *float64              `json:"cpu_pct"`
	KernelKind             string                `json:"kind"`
	CurrentCellTag         *string               `json:"current_cell_tag"`
	CurrentCell            *CurrentCell          `json:"current_cell"`
	ExecutionObservation   *ExecutionObservation `json:"execution_observation,omitempty"`
	ExecutionCount         int                   `json:"execution_count"`
	LastUsed               time.Time             `json:"last_used"`
	DelegateName           *string               `json:"delegate_name"`
	CellCount              int                   `json:"cell_count"`
}

// IdleCandidate is an immutable observation used by the server-owned idle
// policy. Generation and LastUsed form the CAS fence; the Manager remains the
// sole authority allowed to close a worker.
type IdleCandidate struct {
	KernelID    string
	OwnerID     string
	ProjectID   string
	FrameID     string
	RootFrameID string
	Generation  uint64
	LastUsed    time.Time
}

type SubmitRequest struct {
	KernelID               string
	ExpectedGeneration     uint64
	OwnerID                string
	ProjectID              string
	FrameID                string
	FrameIncarnationID     string
	RootFrameIncarnationID string
	KernelKind             string
	Language               string
	Environment            string
	ExecID                 string
	ToolUseID              string
	ToolName               string
	Code                   string
	WorkingDir             string
	Background             bool
	Fresh                  bool
	Origin                 string
	// Timeout is an explicit active-cell wall-clock deadline. Zero is unbounded
	// and must not be replaced with the worker idle timeout.
	Timeout            time.Duration
	InterruptGrace     time.Duration
	HostCalls          *HostCallPolicy
	StartAuthorization <-chan error
	// Host-only obligation. The existing identity/generation contract and
	// execute lock bind this to one invocation, never a model parameter.
	Observation *executionprep.Observation
	// For a host-rendered Bash envelope, bind the generated interpreter source
	// separately from Observation.SourceSHA256 (the original Bash command).
	ObservationCodeSHA256 string
}

type ExecutionStarted struct {
	ExecID      string
	ToolUseID   string
	KernelID    string
	FrameID     string
	Language    string
	Environment string
	KernelKind  string
	Code        string
	Origin      string
	StartedAt   time.Time
}

type ExecutionOutcome struct {
	Response     Response
	Err          error
	TimedOut     bool
	Dequeued     bool
	FilesWritten []FileWrite
	DroppedRoots []string
	CellIndex    int
	StartedAt    time.Time
	FinishedAt   time.Time
	Generation   uint64
	// Set only by the host for a proof-bearing request rejected before the
	// execution acknowledgement. A guest response alone cannot set this bit.
	ObservationRefused bool
}

type InterruptResult struct {
	Interrupted bool   `json:"interrupted"`
	Via         string `json:"via,omitempty"`
	Dequeued    bool   `json:"dequeued,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

type ExecStream struct {
	ExecID               string    `json:"exec_id"`
	ToolUseID            string    `json:"tool_use_id"`
	ToolName             string    `json:"tool_name,omitempty"`
	StartedAt            time.Time `json:"started_at"`
	Background           bool      `json:"background,omitempty"`
	Status               string    `json:"status"`
	Stdout               string    `json:"stdout"`
	ThroughChunkSequence uint64    `json:"through_chunk_sequence"`
	StdoutStartByte      uint64    `json:"stdout_start_byte"`
	StdoutEndByte        uint64    `json:"stdout_end_byte"`
	StdoutTruncated      bool      `json:"stdout_truncated,omitempty"`
}

type ExecutionHandle struct {
	execution *managedExecution
}

func (h *ExecutionHandle) Started() <-chan ExecutionStarted {
	if h == nil || h.execution == nil {
		closed := make(chan ExecutionStarted)
		close(closed)
		return closed
	}
	return h.execution.started
}

func (h *ExecutionHandle) Done() <-chan ExecutionOutcome {
	if h == nil || h.execution == nil {
		closed := make(chan ExecutionOutcome)
		close(closed)
		return closed
	}
	return h.execution.done
}

// AcknowledgePersistence releases a completed background execution only after
// its durable execution log and notification have committed. Foreground
// executions are released by the lifecycle immediately and ignore this call.
func (h *ExecutionHandle) AcknowledgePersistence() {
	if h == nil || h.execution == nil || !h.execution.request.Background {
		return
	}
	h.execution.acknowledgePersistence()
}

type workerLifecycle struct {
	mu               sync.Mutex
	spec             SessionSpec
	generation       uint64
	lastUsed         time.Time
	executionCount   int
	current          *currentExecution
	backgroundQueued int
	workingDir       string
	pendingRestart   string
	noticeGeneration uint64
	closing          bool
}

type currentExecution struct {
	execID     string
	toolUseID  string
	toolName   string
	source     string
	origin     string
	startedAt  time.Time
	background bool
}

type managedExecution struct {
	worker         *Worker
	state          *workerLifecycle
	request        SubmitRequest
	key            executionKey
	generation     uint64
	stdoutObserver func(ExecStdoutChunk)

	mu              sync.Mutex
	status          string
	timedOut        bool
	beforeFiles     workspaceFilesSnapshot
	workingDir      string
	startedAt       time.Time
	stdout          string
	stdoutSeq       uint64
	stdoutBytes     uint64
	stopReason      string
	interrupt       chan struct{}
	resourceFailure chan *ResourcePressureFailure
	hostCancel      chan struct{}
	started         chan ExecutionStarted
	done            chan ExecutionOutcome
	finished        chan struct{}
	startedOnce     sync.Once
	finishOnce      sync.Once
}

type executionKey struct {
	manager string
	frameID string
	execID  string
}

var lifecycleRegistry = struct {
	mu         sync.Mutex
	workers    map[*Worker]*workerLifecycle
	executions map[executionKey]*managedExecution
}{
	workers:    map[*Worker]*workerLifecycle{},
	executions: map[executionKey]*managedExecution{},
}

var lifecycleGeneration atomic.Uint64
var lifecycleStartMu sync.Mutex

func (m *Manager) StartSession(spec SessionSpec) (*Worker, error) {
	if m == nil {
		return nil, errors.New("kernel manager is not configured")
	}
	spec = normalizeSessionSpec(spec)
	if err := validateSessionSpec(spec); err != nil {
		return nil, err
	}

	// Start() is idempotent by kernel id. Serialize the identity check and
	// attachment so concurrent callers cannot rebind one id to two frames.
	lifecycleStartMu.Lock()
	defer lifecycleStartMu.Unlock()
	return m.startSessionLocked(spec)
}

// StartFreshSession creates one disposable repl kernel. The reference runtime permits
// exactly two in-flight fresh repl kernels per Frame, including workers that
// are still starting.
func (m *Manager) StartFreshSession(spec SessionSpec) (*Worker, error) {
	if m == nil {
		return nil, errors.New("kernel manager is not configured")
	}
	spec = normalizeSessionSpec(spec)
	spec.Fresh = true
	if err := validateSessionSpec(spec); err != nil {
		return nil, err
	}
	if spec.KernelKind != "operon" {
		return nil, errors.New("fresh kernels are available only for repl")
	}
	lifecycleStartMu.Lock()
	defer lifecycleStartMu.Unlock()
	fresh := 0
	m.mu.Lock()
	workers := make([]*Worker, 0, len(m.workers))
	for _, worker := range m.workers {
		workers = append(workers, worker)
	}
	m.mu.Unlock()
	for _, worker := range workers {
		state := lifecycleWorkerState(worker)
		if state == nil {
			continue
		}
		state.mu.Lock()
		matches := state.spec.Fresh && state.spec.FrameID == spec.FrameID
		state.mu.Unlock()
		if matches {
			fresh++
		}
	}
	if fresh >= 2 {
		return nil, errors.New("fresh repl kernel cap reached (2 per frame) — wait for an in-flight fresh cell to finish, or use the primary kernel")
	}
	return m.startSessionLocked(spec)
}

func (m *Manager) startSessionLocked(spec SessionSpec) (*Worker, error) {
	if existing := m.workerByID(spec.KernelID); existing != nil {
		if state := lifecycleWorkerState(existing); state != nil {
			state.mu.Lock()
			existingSpec := state.spec
			closing := state.closing
			state.mu.Unlock()
			if closing {
				return nil, errors.New("kernel is closing")
			}
			if !sameSessionSpec(existingSpec, spec) {
				return nil, fmt.Errorf("kernel id %q is already bound to frame %q", spec.KernelID, existingSpec.FrameID)
			}
			return existing, nil
		}
	}
	restartKey := sessionRestartKey(spec)
	pendingRestart, hasPendingRestart := m.peekKernelRestart(restartKey)
	worker, err := m.startSessionWorker(spec)
	if err != nil {
		return nil, err
	}
	select {
	case <-worker.done:
		return nil, worker.stoppedError()
	default:
	}
	state := &workerLifecycle{
		spec: spec, generation: lifecycleGeneration.Add(1), lastUsed: time.Now().UTC(), workingDir: spec.WorkspaceDir,
	}
	if spec.Language == "r" && hasPendingRestart && m.consumeKernelRestart(restartKey, pendingRestart) {
		state.pendingRestart = formatRKernelRestartNotice(spec.Environment, pendingRestart.reason)
		state.noticeGeneration = state.generation
	}
	lifecycleRegistry.mu.Lock()
	if existing := lifecycleRegistry.workers[worker]; existing != nil {
		state = existing
	} else {
		lifecycleRegistry.workers[worker] = state
		go releaseLifecycleWorker(worker, worker.done)
	}
	lifecycleRegistry.mu.Unlock()
	m.notifyIdleChange()
	return worker, nil
}

func sessionRestartKey(spec SessionSpec) string {
	return strings.Join([]string{
		spec.KernelID, spec.OwnerID, spec.ProjectID, spec.FrameID, spec.FrameIncarnationID,
		spec.RootFrameID, spec.RootFrameIncarnationID, spec.Language, spec.Environment, spec.RuntimeGeneration,
	}, "\x00")
}

func releaseLifecycleWorker(worker *Worker, done <-chan struct{}) {
	<-done
	lifecycleRegistry.mu.Lock()
	delete(lifecycleRegistry.workers, worker)
	lifecycleRegistry.mu.Unlock()
}

func normalizeSessionSpec(spec SessionSpec) SessionSpec {
	spec.KernelID = strings.TrimSpace(spec.KernelID)
	spec.OwnerID = strings.TrimSpace(spec.OwnerID)
	spec.ProjectID = strings.TrimSpace(spec.ProjectID)
	spec.FrameID = strings.TrimSpace(spec.FrameID)
	spec.FrameIncarnationID = strings.TrimSpace(spec.FrameIncarnationID)
	spec.RootFrameID = strings.TrimSpace(spec.RootFrameID)
	spec.RootFrameIncarnationID = strings.TrimSpace(spec.RootFrameIncarnationID)
	if spec.RootFrameID == "" {
		spec.RootFrameID = spec.FrameID
	}
	spec.AgentName = strings.TrimSpace(spec.AgentName)
	if spec.AgentName == "" {
		spec.AgentName = "OPERON"
	}
	spec.DelegateName = strings.TrimSpace(spec.DelegateName)
	spec.KernelKind = strings.ToLower(strings.TrimSpace(spec.KernelKind))
	if spec.KernelKind == "" {
		spec.KernelKind = "analysis"
	}
	spec.Language = strings.ToLower(strings.TrimSpace(spec.Language))
	spec.Environment = strings.TrimSpace(spec.Environment)
	spec.RuntimeGeneration = strings.TrimSpace(spec.RuntimeGeneration)
	spec.WorkspaceDir = strings.TrimSpace(spec.WorkspaceDir)
	spec.Mounts = append([]WorkerMount(nil), spec.Mounts...)
	for index := range spec.Mounts {
		spec.Mounts[index].Path = filepath.Clean(strings.TrimSpace(spec.Mounts[index].Path))
	}
	sort.Slice(spec.Mounts, func(i, j int) bool {
		leftDepth := strings.Count(spec.Mounts[i].Path, string(os.PathSeparator))
		rightDepth := strings.Count(spec.Mounts[j].Path, string(os.PathSeparator))
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		if spec.Mounts[i].Path != spec.Mounts[j].Path {
			return spec.Mounts[i].Path < spec.Mounts[j].Path
		}
		return !spec.Mounts[i].Writable && spec.Mounts[j].Writable
	})
	spec.ProtectedPaths = append([]string(nil), spec.ProtectedPaths...)
	for index := range spec.ProtectedPaths {
		spec.ProtectedPaths[index] = filepath.Clean(strings.TrimSpace(spec.ProtectedPaths[index]))
	}
	sort.Strings(spec.ProtectedPaths)
	spec.EgressAllowedDomains = normalizeSessionDomains(spec.EgressAllowedDomains)
	spec.EgressDeniedDomains = normalizeSessionDomains(spec.EgressDeniedDomains)
	if strings.TrimSpace(spec.CABundle) != "" {
		spec.CABundle = filepath.Clean(strings.TrimSpace(spec.CABundle))
	}
	if proxy, err := networktls.NormalizeProxyURL(spec.UpstreamProxy); err == nil {
		spec.UpstreamProxy = proxy
	} else {
		spec.UpstreamProxy = strings.TrimSpace(spec.UpstreamProxy)
	}
	return spec
}

func normalizeSessionDomains(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(value, ".")))
		if value == "" {
			continue
		}
		if _, found := seen[value]; found {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	if _, found := seen[networkpolicy.PublicWildcard]; found {
		return []string{networkpolicy.PublicWildcard}
	}
	sort.Strings(result)
	return result
}

func validateSessionSpec(spec SessionSpec) error {
	for label, value := range map[string]string{
		"kernel id": spec.KernelID, "frame id": spec.FrameID, "root frame id": spec.RootFrameID,
	} {
		if value == "" || len(value) > 128 || strings.ContainsAny(value, "/\\\x00\r\n") {
			return fmt.Errorf("%s must be a bounded path-free string", label)
		}
	}
	identityValues := []string{spec.OwnerID, spec.ProjectID, spec.FrameIncarnationID, spec.RootFrameIncarnationID}
	identityCount := 0
	for _, value := range identityValues {
		if value != "" {
			identityCount++
			if len(value) > 128 || strings.ContainsAny(value, "/\\\x00\r\n") {
				return errors.New("kernel owner, project, frame incarnation, and root incarnation must be bounded path-free strings")
			}
		}
	}
	if identityCount != 0 && identityCount != len(identityValues) {
		return errors.New("kernel owner, project, frame incarnation, and root incarnation must be supplied together")
	}
	if spec.Language != "python" && spec.Language != "r" {
		return errors.New("kernel language must be python or r")
	}
	if spec.KernelKind != "analysis" && spec.KernelKind != "operon" && spec.KernelKind != "r" && spec.KernelKind != "bash" && spec.KernelKind != "provider" {
		return errors.New("kernel kind must be analysis, operon, provider, r, or bash")
	}
	if !ValidEnvironmentName(spec.Environment) {
		return errors.New("kernel environment must be a bounded path-free identifier")
	}
	if spec.RuntimeGeneration != "" && !ValidEnvironmentName(spec.RuntimeGeneration) {
		return errors.New("kernel runtime generation must be a bounded path-free identifier")
	}
	if spec.WorkspaceDir == "" || !filepath.IsAbs(spec.WorkspaceDir) {
		return errors.New("kernel workspace must be an absolute directory")
	}
	for index, mount := range spec.Mounts {
		if mount.Path == "." || !filepath.IsAbs(mount.Path) || strings.ContainsAny(mount.Path, "\x00\r\n") {
			return errors.New("kernel mount must be an absolute canonical directory")
		}
		if index > 0 && spec.Mounts[index-1].Path == mount.Path {
			return errors.New("kernel mounts contain duplicate authority")
		}
		for previous := 0; previous < index; previous++ {
			if sessionMountPathsOverlap(spec.Mounts[previous].Path, mount.Path) {
				return errors.New("kernel mounts contain overlapping authority")
			}
		}
	}
	for index, path := range spec.ProtectedPaths {
		if path == "." || !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
			return errors.New("kernel protected path must be an absolute canonical path")
		}
		if index > 0 && spec.ProtectedPaths[index-1] == path {
			return errors.New("kernel protected paths contain duplicates")
		}
		if sessionMountPathsOverlap(spec.WorkspaceDir, path) {
			return errors.New("kernel workspace overlaps a protected path")
		}
	}
	for _, patterns := range [][]string{spec.EgressAllowedDomains, spec.EgressDeniedDomains} {
		if len(patterns) > 128 {
			return errors.New("kernel egress domain policy is too large")
		}
		for _, pattern := range patterns {
			if len(pattern) > 253 || strings.ContainsAny(pattern, "\x00\r\n") {
				return errors.New("kernel egress domain policy is invalid")
			}
		}
	}
	if _, err := networktls.NormalizeProxyURL(spec.UpstreamProxy); err != nil {
		return errors.New("kernel upstream proxy is invalid")
	}
	if spec.CABundle != "" && (!filepath.IsAbs(spec.CABundle) || strings.ContainsAny(spec.CABundle, "\x00\r\n")) {
		return errors.New("kernel CA bundle path is invalid")
	}
	return nil
}

func sessionMountPathsOverlap(left, right string) bool {
	contains := func(root, candidate string) bool {
		relative, err := filepath.Rel(root, candidate)
		return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
	}
	return contains(left, right) || contains(right, left)
}

func sameSessionSpec(left, right SessionSpec) bool {
	return left.KernelID == right.KernelID && left.OwnerID == right.OwnerID &&
		left.ProjectID == right.ProjectID && left.FrameID == right.FrameID &&
		left.FrameIncarnationID == right.FrameIncarnationID &&
		left.RootFrameID == right.RootFrameID && left.RootFrameIncarnationID == right.RootFrameIncarnationID &&
		left.Language == right.Language &&
		left.Environment == right.Environment && left.RuntimeGeneration == right.RuntimeGeneration &&
		left.WorkspaceDir == right.WorkspaceDir &&
		left.KernelKind == right.KernelKind && left.AgentName == right.AgentName && left.DelegateName == right.DelegateName &&
		left.Fresh == right.Fresh && sameWorkerMounts(left.Mounts, right.Mounts) &&
		sameProtectedPaths(left.ProtectedPaths, right.ProtectedPaths) &&
		sameProtectedPaths(left.EgressAllowedDomains, right.EgressAllowedDomains) &&
		sameProtectedPaths(left.EgressDeniedDomains, right.EgressDeniedDomains) &&
		left.CABundle == right.CABundle && left.UpstreamProxy == right.UpstreamProxy
}

func sameProtectedPaths(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sameWorkerMounts(left, right []WorkerMount) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Path != right[index].Path || left[index].Writable != right[index].Writable ||
			left[index].regular != right[index].regular || left[index].trusted != right[index].trusted {
			return false
		}
	}
	return true
}

func (m *Manager) workerByID(id string) *Worker {
	m.mu.Lock()
	defer m.mu.Unlock()
	worker := m.workers[strings.TrimSpace(id)]
	if worker == nil {
		return nil
	}
	select {
	case <-worker.done:
		return nil
	default:
		return worker
	}
}

func lifecycleWorkerState(worker *Worker) *workerLifecycle {
	lifecycleRegistry.mu.Lock()
	defer lifecycleRegistry.mu.Unlock()
	return lifecycleRegistry.workers[worker]
}

func (w *Worker) Generation() uint64 {
	state := lifecycleWorkerState(w)
	if state == nil {
		return 0
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.generation
}

func (m *Manager) ListSessionKernels(rootFrameID string) []SessionKernel {
	if m == nil {
		return []SessionKernel{}
	}
	rootFrameID = strings.TrimSpace(rootFrameID)
	m.mu.Lock()
	workers := make([]*Worker, 0, len(m.workers))
	for _, worker := range m.workers {
		workers = append(workers, worker)
	}
	m.mu.Unlock()
	result := make([]SessionKernel, 0, len(workers))
	for _, worker := range workers {
		state := lifecycleWorkerState(worker)
		if state == nil {
			continue
		}
		state.mu.Lock()
		if state.spec.RootFrameID != rootFrameID {
			state.mu.Unlock()
			continue
		}
		projection := snapshotSessionKernel(state)
		state.mu.Unlock()
		result = append(result, projection)
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i], result[j]
		if left.FrameID != right.FrameID {
			return left.FrameID < right.FrameID
		}
		if left.Language != right.Language {
			return left.Language < right.Language
		}
		if left.Environment != right.Environment {
			return left.Environment < right.Environment
		}
		return left.KernelID < right.KernelID
	})
	return result
}

// SnapshotIdleCandidates returns only persistent session kernels with no
// queued, running, or persistence-pending execution. Fresh kernels are owned
// by their request scope and are never transferred to the idle reaper.
func (m *Manager) SnapshotIdleCandidates() []IdleCandidate {
	if m == nil {
		return []IdleCandidate{}
	}
	m.mu.Lock()
	workers := make([]*Worker, 0, len(m.workers))
	for _, worker := range m.workers {
		workers = append(workers, worker)
	}
	m.mu.Unlock()
	managerKey := fmt.Sprintf("%p", m)
	result := make([]IdleCandidate, 0, len(workers))
	for _, worker := range workers {
		state := lifecycleWorkerState(worker)
		if state == nil {
			continue
		}
		state.mu.Lock()
		if state.spec.Fresh || state.closing || state.current != nil || state.backgroundQueued > 0 {
			state.mu.Unlock()
			continue
		}
		candidate := IdleCandidate{
			KernelID: worker.id, OwnerID: state.spec.OwnerID, ProjectID: state.spec.ProjectID,
			FrameID: state.spec.FrameID, RootFrameID: state.spec.RootFrameID,
			Generation: state.generation, LastUsed: state.lastUsed,
		}
		lifecycleRegistry.mu.Lock()
		protected := false
		for key, execution := range lifecycleRegistry.executions {
			if key.manager == managerKey && execution.worker == worker {
				protected = true
				break
			}
		}
		lifecycleRegistry.mu.Unlock()
		state.mu.Unlock()
		if !protected {
			result = append(result, candidate)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].KernelID < result[j].KernelID })
	return result
}

// CloseIdleCandidate closes exactly the generation and activity observation
// approved by the server policy. A concurrent submit, touch, restart, or prior
// close makes the candidate stale and returns closed=false.
func (m *Manager) CloseIdleCandidate(ctx context.Context, candidate IdleCandidate) (closed bool, err error) {
	if m == nil {
		return false, nil
	}
	worker := m.workerByID(candidate.KernelID)
	if worker == nil {
		return false, nil
	}
	state := lifecycleWorkerState(worker)
	if state == nil {
		return false, nil
	}
	managerKey := fmt.Sprintf("%p", m)
	state.mu.Lock()
	if state.spec.Fresh || state.closing || state.generation != candidate.Generation ||
		!state.lastUsed.Equal(candidate.LastUsed) || state.current != nil || state.backgroundQueued > 0 {
		state.mu.Unlock()
		return false, nil
	}
	lifecycleRegistry.mu.Lock()
	protected := false
	for key, execution := range lifecycleRegistry.executions {
		if key.manager == managerKey && execution.worker == worker {
			protected = true
			break
		}
	}
	if !protected {
		state.closing = true
	}
	lifecycleRegistry.mu.Unlock()
	state.mu.Unlock()
	if protected {
		return false, nil
	}
	if err := worker.Close(ctx); err != nil {
		state.mu.Lock()
		if state.generation == candidate.Generation {
			state.closing = false
		}
		state.mu.Unlock()
		return false, err
	}
	return true, nil
}

func snapshotSessionKernel(state *workerLifecycle) SessionKernel {
	projection := SessionKernel{
		FrameID: state.spec.FrameID, FrameIncarnationID: state.spec.FrameIncarnationID,
		RootFrameID: state.spec.RootFrameID, RootFrameIncarnationID: state.spec.RootFrameIncarnationID,
		ProjectID: state.spec.ProjectID,
		AgentName: state.spec.AgentName, Environment: state.spec.Environment,
		RuntimeGeneration: state.spec.RuntimeGeneration,
		Language:          state.spec.Language, KernelID: state.spec.KernelID,
		KernelKind: state.spec.KernelKind,
		Starting:   false, ExecutionCount: state.executionCount, LastUsed: state.lastUsed,
	}
	if state.spec.DelegateName != "" {
		value := state.spec.DelegateName
		projection.DelegateName = &value
	}
	if state.current != nil {
		projection.Busy = true
		tag := state.current.toolUseID
		projection.CurrentCellTag = &tag
		projection.CurrentCell = &CurrentCell{
			Source: state.current.source, Origin: state.current.origin, StartedAt: state.current.startedAt,
		}
	}
	return projection
}

func (m *Manager) Submit(input SubmitRequest) (*ExecutionHandle, error) {
	if m == nil {
		return nil, errors.New("kernel manager is not configured")
	}
	if input.Timeout == 0 {
		input.Timeout = m.config.ExecutionTimeout
	}
	if input.InterruptGrace <= 0 {
		input.InterruptGrace = m.config.InterruptGrace
	}
	input = normalizeSubmitRequest(input)
	if err := validateSubmitRequest(input); err != nil {
		return nil, err
	}
	worker, state := m.findSessionWorker(input)
	if worker == nil || state == nil {
		return nil, fmt.Errorf("no running %s kernel for environment %q on frame %s", input.Language, input.Environment, input.FrameID)
	}
	state.mu.Lock()
	if state.closing {
		state.mu.Unlock()
		return nil, errors.New("kernel is closing")
	}
	if input.ExpectedGeneration != 0 && state.generation != input.ExpectedGeneration {
		state.mu.Unlock()
		return nil, errors.New("kernel generation changed before execution")
	}
	if input.Background {
		// A persistent kernel is already a serialized execution queue. Background
		// requests must join that queue instead of failing merely because another
		// background cell is running; otherwise healthy long tasks lose work at
		// execution-unit boundaries. The count also fences idle reaping while any
		// queued/running background unit remains.
		state.backgroundQueued++
	}
	generation := state.generation
	state.mu.Unlock()
	key := executionKey{manager: fmt.Sprintf("%p", m), frameID: input.FrameID, execID: input.ExecID}
	execution := &managedExecution{
		worker: worker, state: state, request: input, key: key, generation: generation,
		stdoutObserver: m.currentStdoutObserver(),
		status:         "queued", interrupt: make(chan struct{}, 1), hostCancel: make(chan struct{}, 1), resourceFailure: make(chan *ResourcePressureFailure, 1),
		started: make(chan ExecutionStarted, 1), done: make(chan ExecutionOutcome, 1), finished: make(chan struct{}),
	}
	lifecycleRegistry.mu.Lock()
	if _, exists := lifecycleRegistry.executions[key]; exists {
		lifecycleRegistry.mu.Unlock()
		if input.Background {
			state.mu.Lock()
			if state.backgroundQueued > 0 {
				state.backgroundQueued--
			}
			state.mu.Unlock()
		}
		return nil, fmt.Errorf("terminal execution %q already exists on frame %q", input.ExecID, input.FrameID)
	}
	lifecycleRegistry.executions[key] = execution
	lifecycleRegistry.mu.Unlock()
	m.notifyIdleChange()
	go execution.run()
	return &ExecutionHandle{execution: execution}, nil
}

func normalizeSubmitRequest(input SubmitRequest) SubmitRequest {
	input.Observation = input.Observation.Clone()
	input.KernelID = strings.TrimSpace(input.KernelID)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.FrameID = strings.TrimSpace(input.FrameID)
	input.FrameIncarnationID = strings.TrimSpace(input.FrameIncarnationID)
	input.RootFrameIncarnationID = strings.TrimSpace(input.RootFrameIncarnationID)
	input.Language = strings.ToLower(strings.TrimSpace(input.Language))
	input.Environment = strings.TrimSpace(input.Environment)
	input.ExecID = strings.TrimSpace(input.ExecID)
	input.ToolUseID = strings.TrimSpace(input.ToolUseID)
	input.ToolName = strings.TrimSpace(input.ToolName)
	input.WorkingDir = strings.TrimSpace(input.WorkingDir)
	input.KernelKind = strings.ToLower(strings.TrimSpace(input.KernelKind))
	if input.KernelKind == "" {
		input.KernelKind = "analysis"
	}
	if input.Origin != "user" {
		input.Origin = "agent"
	}
	if input.InterruptGrace <= 0 {
		input.InterruptGrace = defaultManagedInterruptGrace
	}
	return input
}

func validateSubmitRequest(input SubmitRequest) error {
	if input.FrameID == "" || input.ExecID == "" || input.ToolUseID == "" {
		return errors.New("frame id, execution id, and tool use id are required")
	}
	if input.WorkingDir != "" && !filepath.IsAbs(input.WorkingDir) {
		return errors.New("working_dir must be an absolute path")
	}
	if input.Timeout < 0 {
		return errors.New("kernel execution timeout must not be negative")
	}
	if input.Fresh && input.KernelID == "" {
		return errors.New("fresh kernel execution requires an exact kernel id")
	}
	identityValues := []string{input.OwnerID, input.ProjectID, input.FrameIncarnationID, input.RootFrameIncarnationID}
	identityCount := 0
	for _, value := range identityValues {
		if value != "" {
			identityCount++
			if len(value) > 128 || strings.ContainsAny(value, "/\\\x00\r\n") {
				return errors.New("kernel execution owner, project, frame incarnation, and root incarnation must be bounded path-free strings")
			}
		}
	}
	if identityCount != 0 && identityCount != len(identityValues) {
		return errors.New("kernel execution owner, project, frame incarnation, and root incarnation must be supplied together")
	}
	if input.Language != "python" && input.Language != "r" {
		return errors.New("kernel language must be python or r")
	}
	if !ValidEnvironmentName(input.Environment) {
		return errors.New("kernel environment must be a bounded path-free identifier")
	}
	if strings.TrimSpace(input.Code) == "" {
		return errors.New("kernel code must not be empty")
	}
	if len([]byte(input.Code)) > maxManagedKernelCodeBytes {
		if input.KernelKind != "analysis" && input.KernelKind != "operon" && input.KernelKind != "r" && input.KernelKind != "bash" {
			return errors.New("kernel kind must be analysis, operon, r, or bash")
		}
		return fmt.Errorf("kernel code exceeds %d bytes", maxManagedKernelCodeBytes)
	}
	return nil
}

func (m *Manager) findSessionWorker(input SubmitRequest) (*Worker, *workerLifecycle) {
	m.mu.Lock()
	workers := make([]*Worker, 0, len(m.workers))
	for _, worker := range m.workers {
		workers = append(workers, worker)
	}
	m.mu.Unlock()
	var selected *Worker
	var selectedState *workerLifecycle
	var generation uint64
	for _, worker := range workers {
		state := lifecycleWorkerState(worker)
		if state == nil {
			continue
		}
		state.mu.Lock()
		matches := state.spec.FrameID == input.FrameID && state.spec.KernelKind == input.KernelKind &&
			state.spec.Language == input.Language && state.spec.Environment == input.Environment &&
			state.spec.Fresh == input.Fresh
		if matches && input.KernelID != "" {
			matches = state.spec.KernelID == input.KernelID
		}
		if matches && input.FrameIncarnationID != "" {
			matches = state.spec.OwnerID == input.OwnerID && state.spec.ProjectID == input.ProjectID &&
				state.spec.FrameIncarnationID == input.FrameIncarnationID &&
				state.spec.RootFrameIncarnationID == input.RootFrameIncarnationID
		}
		candidateGeneration := state.generation
		state.mu.Unlock()
		if matches && candidateGeneration >= generation {
			selected, selectedState, generation = worker, state, candidateGeneration
		}
	}
	return selected, selectedState
}

func (execution *managedExecution) run() {
	if execution.request.Background {
		defer func() {
			execution.state.mu.Lock()
			if execution.state.backgroundQueued > 0 {
				execution.state.backgroundQueued--
			}
			execution.state.mu.Unlock()
		}()
	}
	if authorization := execution.request.StartAuthorization; authorization != nil {
		var err error
		var ok bool
		select {
		case err, ok = <-authorization:
		case <-execution.finished:
			return
		}
		if !ok {
			err = errors.New("kernel execution start authorization closed without a decision")
		}
		if err != nil {
			execution.finish(ExecutionOutcome{Err: err, Dequeued: true})
			return
		}
	}
	worker := execution.worker
	worker.executeMu.Lock()
	defer worker.executeMu.Unlock()
	execution.mu.Lock()
	if execution.status != "queued" {
		execution.mu.Unlock()
		return
	}
	execution.status = "running"
	execution.startedAt = time.Now().UTC()
	startedAt := execution.startedAt
	execution.mu.Unlock()
	if reason := worker.validateObservationExecution(execution.request); reason != "" {
		execution.finish(ExecutionOutcome{Dequeued: true, ObservationRefused: true, Response: Response{
			ID: execution.request.ExecID, Preflight: executionprep.ObservationDeclined(reason),
		}})
		return
	}
	execution.state.mu.Lock()
	execution.workingDir = execution.request.WorkingDir
	if execution.workingDir == "" {
		execution.workingDir = execution.state.workingDir
	} else {
		execution.state.workingDir = execution.workingDir
	}
	if execution.workingDir == "" {
		execution.workingDir = worker.workspaceDir
		execution.state.workingDir = worker.workspaceDir
	}
	execution.state.current = &currentExecution{
		execID: execution.request.ExecID, toolUseID: execution.request.ToolUseID,
		toolName: execution.request.ToolName,
		source:   execution.request.Code, origin: execution.request.Origin, startedAt: startedAt,
		background: execution.request.Background,
	}
	execution.state.lastUsed = startedAt
	execution.state.mu.Unlock()
	execution.beforeFiles = snapshotWorkspaceFiles(worker.workspaceDir, execution.workingDir)

	payload, err := json.Marshal(request{
		ID: execution.request.ExecID, Code: execution.request.Code, Origin: execution.request.Origin,
		ToolName:     execution.request.ToolName,
		WorkspaceDir: worker.workspaceDir, WorkingDir: execution.workingDir,
		HostEnabled: execution.request.HostCalls != nil, Fresh: execution.request.Fresh,
		Observation:           execution.request.Observation,
		ObservationCodeSHA256: observationExecutionDigest(execution.request),
	})
	if err != nil {
		execution.finish(ExecutionOutcome{Err: err, StartedAt: startedAt})
		return
	}
	hostCalls := make(chan HostCall, 1)
	if err := worker.bindHostCalls(execution.request.ExecID, hostCalls); err != nil {
		execution.finish(ExecutionOutcome{Err: err, StartedAt: startedAt})
		return
	}
	defer worker.unbindHostCalls(execution.request.ExecID)
	if err := worker.bindStdoutStream(execution.request.ExecID, execution.appendStdout); err != nil {
		execution.finish(ExecutionOutcome{Err: err, StartedAt: startedAt})
		return
	}
	defer worker.unbindStdoutStream(execution.request.ExecID)
	hostContext, cancelHostCalls := context.WithCancel(context.Background())
	defer cancelHostCalls()
	policy := normalizeHostCallPolicy(execution.request.HostCalls)
	hostCallCount := 0
	hostCompletions := make(chan hostCallCompletion, 1)
	hostResultWrites := make(chan error, 1)
	hostCallPending := false
	hostResultWriting := false
	var nextHostCall *HostCall
	var pendingHostResponse *Response
	select {
	case <-worker.done:
		execution.finish(ExecutionOutcome{Err: worker.stoppedError(), StartedAt: startedAt})
		return
	default:
	}
	executionStarted, err := worker.bindExecutionStart(execution.request.ExecID)
	if err != nil {
		execution.finish(ExecutionOutcome{Err: err, StartedAt: startedAt})
		return
	}
	defer worker.unbindExecutionStart(execution.request.ExecID)
	if execution.request.Observation == nil {
		worker.observationTainted = true
	}
	if err := worker.writeProtocol(payload); err != nil {
		execution.finish(ExecutionOutcome{Err: fmt.Errorf("write kernel request: %w", err), StartedAt: startedAt})
		return
	}
	var grace *time.Timer
	var graceC <-chan time.Time
	var retry *time.Ticker
	var retryC <-chan time.Time
	preStartSignalSent := false
	postStartSignalCount := 0
	executionAcknowledged := false
	preStartInterruptPending := false
	var pressureFailure *ResourcePressureFailure
	finish := func(outcome ExecutionOutcome) {
		if pressureFailure != nil {
			// A buffered response does not prove physical termination. Preserve
			// ownership until the same worker owner has reaped the process.
			<-worker.done
			outcome.Err = pressureFailure
			outcome.TimedOut, outcome.Response.Interrupted = false, false
			if outcome.Response.Trace == nil {
				outcome.Response.Trace = map[string]any{}
			}
			outcome.Response.Trace["resource_pressure"] = pressureFailure.Observation
		}
		execution.finish(outcome)
	}
	requestInterrupt := func() {
		cancelHostCalls()
		// A single pre-start signal preserves prompt cancellation for runtimes
		// whose preparation is interruptible. Python intentionally ignores it
		// during source preflight, so retain the existing bounded four-signal
		// sequence for after the worker acknowledges the user-code boundary.
		// The retry ticker stays live across that acknowledgement race.
		if executionAcknowledged && postStartSignalCount < 4 {
			_ = worker.process.signal(os.Interrupt)
			postStartSignalCount++
		} else if !executionAcknowledged && !preStartSignalSent {
			_ = worker.process.signal(os.Interrupt)
			preStartSignalSent = true
		}
		if grace == nil {
			grace = time.NewTimer(execution.request.InterruptGrace)
			graceC = grace.C
			if execution.request.Language != "r" {
				retry = time.NewTicker(25 * time.Millisecond)
				retryC = retry.C
			}
		}
	}
	finishAfterInterruptGrace := func() {
		if execution.isTimedOut() {
			worker.setStopReason("was shut down after a cell timeout")
		} else {
			worker.setStopReason("was shut down")
		}
		_ = worker.process.kill()
		finish(ExecutionOutcome{
			Err:      execution.withUserStopError(errors.New("kernel cell did not stop within interrupt grace period")),
			TimedOut: execution.isTimedOut(), StartedAt: startedAt,
		})
	}
	defer func() {
		if grace != nil {
			grace.Stop()
		}
		if retry != nil {
			retry.Stop()
		}
	}()
	startTimer := time.NewTimer(defaultExecutionStartTimeout)
	var responseAfterStart *Response
	for executionStarted != nil {
		select {
		case <-executionStarted:
			startTimer.Stop()
			executionStarted = nil
			executionAcknowledged = true
			if preStartInterruptPending {
				requestInterrupt()
			}
		case response := <-worker.responses:
			if response.ID != execution.request.ExecID {
				continue
			}
			startTimer.Stop()
			select {
			case <-executionStarted:
				// The worker writes execution_started before a normal terminal
				// response, but both channels may be ready when this goroutine is
				// scheduled. Preserve that protocol order deterministically.
				executionStarted = nil
				executionAcknowledged = true
				responseAfterStart = &response
				continue
			default:
			}
			// Syntax/import preflight is a terminal, non-executing response and
			// deliberately has no execution_started event.
			response = execution.withUserStopReason(response)
			execution.finish(ExecutionOutcome{Response: response})
			return
		case <-worker.done:
			startTimer.Stop()
			execution.finish(ExecutionOutcome{Err: execution.withUserStopError(worker.stoppedError()), StartedAt: startedAt})
			return
		case <-execution.interrupt:
			preStartInterruptPending = true
			requestInterrupt()
		case <-execution.hostCancel:
			cancelHostCalls()
		case <-retryC:
			requestInterrupt()
			if executionAcknowledged && postStartSignalCount >= 4 {
				retry.Stop()
				retryC = nil
			}
		case <-graceC:
			startTimer.Stop()
			finishAfterInterruptGrace()
			return
		case <-startTimer.C:
			worker.setStopReason("was aborted during startup")
			_ = worker.process.kill()
			execution.finish(ExecutionOutcome{Err: errors.New("kernel worker did not acknowledge execution start"), StartedAt: startedAt})
			return
		}
	}
	execution.startedOnce.Do(func() {
		execution.started <- ExecutionStarted{
			ExecID: execution.request.ExecID, ToolUseID: execution.request.ToolUseID,
			KernelID: execution.state.spec.KernelID, FrameID: execution.request.FrameID,
			KernelKind: execution.state.spec.KernelKind,
			Language:   execution.request.Language, Environment: execution.request.Environment,
			Code: execution.request.Code, Origin: execution.request.Origin, StartedAt: startedAt,
		}
		close(execution.started)
	})
	if responseAfterStart != nil {
		response := execution.withUserStopReason(*responseAfterStart)
		execution.finish(ExecutionOutcome{Response: response, StartedAt: startedAt})
		return
	}

	// The reference runtime treats the worker idle timeout as a lifetime boundary for
	// an unused kernel, not as a wall-clock deadline for an active cell. Keep
	// foreground and background cells cancellable, but arm a deadline only when
	// the caller explicitly configures one. This lets a logical long task keep
	// an active local computation running while preserving finite budgets for
	// providers that actually impose them.
	var timeout *time.Timer
	var timeoutC <-chan time.Time
	if execution.request.Timeout > 0 {
		timeout = time.NewTimer(execution.request.Timeout)
		timeoutC = timeout.C
		defer timeout.Stop()
	}

	for {
		incomingHostCalls := hostCalls
		drainingHostCall := nextHostCall != nil && !hostCallPending
		if drainingHostCall {
			incomingHostCalls = make(chan HostCall, 1)
			incomingHostCalls <- *nextHostCall
		}
		select {
		case call := <-incomingHostCalls:
			if drainingHostCall {
				nextHostCall = nil
			}
			// Python can emit its next synchronous request as soon as it reads
			// the terminal frame, before the writer goroutine publishes completion.
			// Queue that one request without admitting concurrent handlers.
			if hostResultWriting && nextHostCall == nil {
				nextHostCall = &call
				continue
			}
			hostCallCount++
			if hostContext.Err() != nil {
				_ = worker.writeHostResult(call, nil, hostContext.Err())
				continue
			}
			if hostCallPending {
				_ = worker.writeHostResult(call, nil, NewHostCallError("concurrent_call", "only one synchronous host call may be active per cell"))
				continue
			}
			if hostCallCount > policy.maxCalls {
				_ = worker.writeHostResult(call, nil, NewHostCallError("call_limit_exceeded", fmt.Sprintf("cell host-call limit of %d was exceeded", policy.maxCalls)))
				continue
			}
			if policy.handler == nil {
				_ = worker.writeHostResult(call, nil, NewHostCallError("unavailable", "host calls are not available in this execution context"))
				continue
			}
			if _, allowed := policy.allowed[call.Method]; !allowed {
				_ = worker.writeHostResult(call, nil, NewHostCallError("method_not_allowed", "host method is not allowed in this execution context"))
				continue
			}
			hostCallPending = true
			go executeHostCall(hostContext, policy, call, hostCompletions)
		case completed := <-hostCompletions:
			// Keep cancellation, execution deadlines and worker exit observable
			// while a large successful result is flowing through the host pipe.
			hostResultWriting = true
			go func() {
				hostResultWrites <- worker.writeHostResultContext(hostContext, completed.call, completed.result, completed.err)
			}()
		case err := <-hostResultWrites:
			hostCallPending = false
			hostResultWriting = false
			if err != nil {
				finish(ExecutionOutcome{Err: fmt.Errorf("write kernel host result: %w", err), TimedOut: execution.isTimedOut(), StartedAt: startedAt})
				return
			}
			if pendingHostResponse != nil {
				response := execution.withUserStopReason(*pendingHostResponse)
				finish(ExecutionOutcome{Response: response, TimedOut: execution.isTimedOut(), StartedAt: startedAt})
				return
			}
		case response := <-worker.responses:
			if response.ID != execution.request.ExecID {
				continue
			}
			if hostResultWriting {
				// Python can consume the terminal result and finish the cell
				// before its host writer publishes completion. Settling here
				// cancels hostContext and can kill a healthy persistent worker.
				// Retain the response while cancellation/exit remain observable.
				pendingHostResponse = &response
				continue
			}
			response = execution.withUserStopReason(response)
			finish(ExecutionOutcome{Response: response, TimedOut: execution.isTimedOut(), StartedAt: startedAt})
			return
		case <-worker.done:
			finish(ExecutionOutcome{Err: execution.withUserStopError(worker.stoppedError()), TimedOut: execution.isTimedOut(), StartedAt: startedAt})
			return
		case failure := <-execution.resourceFailure:
			if failure == nil || grace != nil || pressureFailure != nil {
				continue
			}
			// Let an already completed response win before requesting termination.
			select {
			case response := <-worker.responses:
				if response.ID == execution.request.ExecID {
					finish(ExecutionOutcome{Response: response, StartedAt: startedAt})
					return
				}
			default:
			}
			pressureFailure = failure
			cancelHostCalls()
			worker.setStopReason(failure.Error())
			// Release the exhausted in-memory state. The same process owner reaps
			// the worker; only its terminal event can settle this failed execution.
			if err := worker.process.kill(); err != nil {
				_, _ = worker.diagnostics.Write([]byte("resource-pressure termination failed: " + err.Error() + "\n"))
			}
		case <-execution.interrupt:
			requestInterrupt()
		case <-execution.hostCancel:
			cancelHostCalls()
		case <-timeoutC:
			execution.mu.Lock()
			execution.timedOut = true
			execution.mu.Unlock()
			cancelHostCalls()
			requestInterrupt()
		case <-retryC:
			requestInterrupt()
			if postStartSignalCount >= 4 {
				retry.Stop()
				retryC = nil
			}
		case <-graceC:
			finishAfterInterruptGrace()
			return
		}
	}
}

func (execution *managedExecution) appendStdout(chunk string) {
	execution.mu.Lock()
	if execution.status != "running" || chunk == "" {
		execution.mu.Unlock()
		return
	}
	execution.stdout += chunk
	execution.stdoutSeq++
	sequence := execution.stdoutSeq
	startedAt := execution.startedAt
	background := execution.request.Background
	status := execution.status
	startByte := execution.stdoutBytes
	execution.stdoutBytes += uint64(len(chunk))
	endByte := execution.stdoutBytes
	if len(execution.stdout) > maxExecStreamStdoutBytes {
		execution.stdout = validUTF8Tail(execution.stdout, maxExecStreamStdoutBytes)
	}
	execution.mu.Unlock()
	if execution.stdoutObserver == nil {
		return
	}
	execution.state.mu.Lock()
	rootFrameID := execution.state.spec.RootFrameID
	execution.state.mu.Unlock()
	execution.stdoutObserver(ExecStdoutChunk{
		FrameID: execution.request.FrameID, RootFrameID: rootFrameID,
		ExecID: execution.request.ExecID, ToolUseID: execution.request.ToolUseID,
		ToolName: execution.request.ToolName, StartedAt: startedAt, Background: background, Status: status,
		Chunk: chunk, Sequence: sequence,
		StartByte: startByte, EndByte: endByte,
	})
}

func (execution *managedExecution) setUserStopReason(reason string) {
	reason = strings.TrimSpace(reason)
	if execution == nil || reason == "" {
		return
	}
	execution.mu.Lock()
	if execution.stopReason == "" {
		execution.stopReason = reason
	}
	execution.mu.Unlock()
}

func (execution *managedExecution) userStopReason() string {
	if execution == nil {
		return ""
	}
	execution.mu.Lock()
	defer execution.mu.Unlock()
	return strings.TrimSpace(execution.stopReason)
}

func (execution *managedExecution) withUserStopReason(response Response) Response {
	reason := execution.userStopReason()
	if reason == "" {
		return response
	}
	message := "stopped by user: " + reason
	if reason == "stopped by the user" {
		message = reason
	}
	if response.Stderr == "" {
		response.Stderr = message
	} else if !strings.Contains(response.Stderr, message) {
		response.Stderr += "\n" + message
	}
	return response
}

func (execution *managedExecution) withUserStopError(err error) error {
	reason := execution.userStopReason()
	if reason == "" {
		return err
	}
	if reason == "stopped by the user" {
		if err == nil {
			return errors.New(reason)
		}
		return fmt.Errorf("%s: %w", reason, err)
	}
	if err == nil {
		return errors.New("stopped by user: " + reason)
	}
	return fmt.Errorf("stopped by user: %s: %w", reason, err)
}

func validUTF8Tail(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	start := len(value) - limit
	for start < len(value) && !utf8.RuneStart(value[start]) {
		start++
	}
	return value[start:]
}

func (execution *managedExecution) isTimedOut() bool {
	execution.mu.Lock()
	defer execution.mu.Unlock()
	return execution.timedOut
}

func (execution *managedExecution) finish(outcome ExecutionOutcome) {
	execution.finishOnce.Do(func() {
		if execution.request.Observation != nil && outcome.StartedAt.IsZero() &&
			executionprep.IsObservationDeclined(outcome.Response.Preflight) &&
			outcome.Response.Stdout == "" && outcome.Response.Stderr == "" && outcome.Response.Error == "" {
			outcome.ObservationRefused = true
		}
		finishedAt := time.Now().UTC()
		if !outcome.Dequeued {
			afterFiles := snapshotWorkspaceFiles(execution.worker.workspaceDir, execution.workingDir)
			outcome.FilesWritten, outcome.DroppedRoots = changedWorkspaceFiles(execution.beforeFiles, afterFiles)
		}
		execution.mu.Lock()
		if outcome.Err != nil && outcome.Response.Stdout == "" {
			outcome.Response.Stdout = execution.stdout
		}
		execution.status = "done"
		if execution.request.Background && !outcome.Dequeued {
			execution.status = "persisting"
		}
		execution.mu.Unlock()
		execution.state.mu.Lock()
		if execution.state.current != nil && execution.state.current.execID == execution.request.ExecID {
			execution.state.current = nil
		}
		if !outcome.Dequeued && !outcome.StartedAt.IsZero() {
			execution.state.executionCount++
			outcome.CellIndex = execution.state.executionCount
		}
		execution.state.lastUsed = finishedAt
		outcome.Generation = execution.generation
		if outcome.Response.ID != "" {
			outcome.Response = consumeRKernelRestartNotice(execution.state, execution.generation, outcome.Response)
		}
		execution.state.mu.Unlock()
		outcome.FinishedAt = finishedAt
		execution.startedOnce.Do(func() { close(execution.started) })
		if !execution.request.Background || outcome.Dequeued {
			lifecycleRegistry.mu.Lock()
			delete(lifecycleRegistry.executions, execution.key)
			lifecycleRegistry.mu.Unlock()
		}
		execution.done <- outcome
		close(execution.done)
		close(execution.finished)
		if execution.worker != nil && execution.worker.manager != nil {
			execution.worker.manager.notifyIdleChange()
		}
	})
}

func formatRKernelRestartNotice(environment, reason string) string {
	return fmt.Sprintf(
		"This cell ran on a fresh kernel process: the previous R kernel for environment '%s' %s. Variables, imports, and other in-memory state from earlier cells are gone; workspace files on disk are unaffected. Re-run setup before relying on earlier state.",
		environment, reason,
	)
}

func consumeRKernelRestartNotice(state *workerLifecycle, generation uint64, response Response) Response {
	if state == nil || generation == 0 || state.generation != generation || state.noticeGeneration != generation || state.pendingRestart == "" {
		return response
	}
	notice := state.pendingRestart
	state.pendingRestart = ""
	state.noticeGeneration = 0
	prefix := "[kernel restarted]\n" + notice
	if response.Stderr != "" {
		response.Stderr = prefix + "\n" + response.Stderr
	} else {
		response.Stderr = prefix
	}
	return response
}

func (execution *managedExecution) acknowledgePersistence() {
	lifecycleRegistry.mu.Lock()
	if lifecycleRegistry.executions[execution.key] == execution {
		delete(lifecycleRegistry.executions, execution.key)
	}
	lifecycleRegistry.mu.Unlock()
	if execution.worker != nil && execution.worker.manager != nil {
		execution.worker.manager.notifyIdleChange()
	}
}

func (m *Manager) Interrupt(frameID, execID string) InterruptResult {
	return m.interrupt(frameID, "", "", execID)
}

func (m *Manager) InterruptSession(frameID, frameIncarnationID, rootFrameIncarnationID, execID string) InterruptResult {
	return m.interrupt(frameID, strings.TrimSpace(frameIncarnationID), strings.TrimSpace(rootFrameIncarnationID), execID)
}

func (m *Manager) InterruptSessionWithReason(
	frameID, frameIncarnationID, rootFrameIncarnationID, execID, reason string,
) InterruptResult {
	m.setExecutionUserStopReason(frameID, execID, reason)
	return m.InterruptSession(frameID, frameIncarnationID, rootFrameIncarnationID, execID)
}

func (m *Manager) InterruptKernel(kernelID string) InterruptResult {
	return m.InterruptKernelWithReason(kernelID, "")
}

func (m *Manager) InterruptKernelWithReason(kernelID, reason string) InterruptResult {
	worker := m.workerByID(strings.TrimSpace(kernelID))
	if worker == nil {
		return InterruptResult{Reason: "no such kernel"}
	}
	state := lifecycleWorkerState(worker)
	if state == nil {
		return InterruptResult{Reason: "no such kernel"}
	}
	state.mu.Lock()
	if state.current == nil {
		state.mu.Unlock()
		return InterruptResult{Reason: "kernel is idle"}
	}
	frameID := state.spec.FrameID
	frameIncarnationID := state.spec.FrameIncarnationID
	rootFrameIncarnationID := state.spec.RootFrameIncarnationID
	execID := state.current.execID
	state.mu.Unlock()
	m.setExecutionUserStopReason(frameID, execID, reason)
	return m.InterruptSession(frameID, frameIncarnationID, rootFrameIncarnationID, execID)
}

// AttachKernelStopReason records a user-supplied stop reason on the exact
// active cell without interrupting or clearing the kernel. The Compute UI uses
// this for the late attach_only delivery path.
func (m *Manager) AttachKernelStopReason(kernelID, reason string) bool {
	if m == nil {
		return false
	}
	worker := m.workerByID(strings.TrimSpace(kernelID))
	if worker == nil {
		return false
	}
	state := lifecycleWorkerState(worker)
	if state == nil {
		return false
	}
	state.mu.Lock()
	if state.current == nil {
		state.mu.Unlock()
		return true
	}
	frameID := state.spec.FrameID
	execID := state.current.execID
	state.mu.Unlock()
	m.setExecutionUserStopReason(frameID, execID, reason)
	return true
}

func (m *Manager) setExecutionUserStopReason(frameID, execID, reason string) {
	if m == nil || strings.TrimSpace(reason) == "" {
		return
	}
	key := executionKey{manager: fmt.Sprintf("%p", m), frameID: strings.TrimSpace(frameID), execID: strings.TrimSpace(execID)}
	lifecycleRegistry.mu.Lock()
	execution := lifecycleRegistry.executions[key]
	lifecycleRegistry.mu.Unlock()
	execution.setUserStopReason(reason)
}

func (m *Manager) CancelHostCalls(frameID, frameIncarnationID, rootFrameIncarnationID, execID string) InterruptResult {
	return m.cancelHostCalls(frameID, strings.TrimSpace(frameIncarnationID), strings.TrimSpace(rootFrameIncarnationID), execID)
}

func (m *Manager) cancelHostCalls(frameID, frameIncarnationID, rootFrameIncarnationID, execID string) InterruptResult {
	key := executionKey{manager: fmt.Sprintf("%p", m), frameID: strings.TrimSpace(frameID), execID: strings.TrimSpace(execID)}
	lifecycleRegistry.mu.Lock()
	execution := lifecycleRegistry.executions[key]
	lifecycleRegistry.mu.Unlock()
	if execution == nil || frameIncarnationID != "" &&
		(execution.request.FrameIncarnationID != frameIncarnationID ||
			execution.request.RootFrameIncarnationID != rootFrameIncarnationID) {
		return InterruptResult{Reason: "no such terminal cell"}
	}
	execution.mu.Lock()
	switch execution.status {
	case "queued":
		execution.mu.Unlock()
		return InterruptResult{Reason: "cell had not started"}
	case "running":
		execution.mu.Unlock()
		select {
		case execution.hostCancel <- struct{}{}:
		default:
		}
		return InterruptResult{Interrupted: true, Via: "host-cancel"}
	default:
		execution.mu.Unlock()
		return InterruptResult{Reason: "no such terminal cell"}
	}
}

func (m *Manager) interrupt(frameID, frameIncarnationID, rootFrameIncarnationID, execID string) InterruptResult {
	key := executionKey{manager: fmt.Sprintf("%p", m), frameID: strings.TrimSpace(frameID), execID: strings.TrimSpace(execID)}
	lifecycleRegistry.mu.Lock()
	execution := lifecycleRegistry.executions[key]
	lifecycleRegistry.mu.Unlock()
	if execution == nil || frameIncarnationID != "" &&
		(execution.request.FrameIncarnationID != frameIncarnationID ||
			execution.request.RootFrameIncarnationID != rootFrameIncarnationID) {
		return InterruptResult{Reason: "no such terminal cell"}
	}
	execution.mu.Lock()
	switch execution.status {
	case "queued":
		execution.status = "dequeued"
		execution.mu.Unlock()
		execution.finish(ExecutionOutcome{Dequeued: true})
		return InterruptResult{Dequeued: true, Reason: "cell had not started \u2014 dequeued"}
	case "running":
		execution.mu.Unlock()
		select {
		case execution.interrupt <- struct{}{}:
		default:
		}
		return InterruptResult{Interrupted: true, Via: "sigint"}
	default:
		execution.mu.Unlock()
		return InterruptResult{Reason: "no such terminal cell"}
	}
}

func (m *Manager) ActiveExecutionCount() int {
	if m == nil {
		return 0
	}
	managerKey := fmt.Sprintf("%p", m)
	lifecycleRegistry.mu.Lock()
	defer lifecycleRegistry.mu.Unlock()
	count := 0
	for key := range lifecycleRegistry.executions {
		if key.manager == managerKey {
			count++
		}
	}
	return count
}

func (m *Manager) ListExecStreams(frameID string) []ExecStream {
	if m == nil {
		return []ExecStream{}
	}
	managerKey := fmt.Sprintf("%p", m)
	frameID = strings.TrimSpace(frameID)
	lifecycleRegistry.mu.Lock()
	executions := make([]*managedExecution, 0)
	for key, execution := range lifecycleRegistry.executions {
		if key.manager == managerKey && key.frameID == frameID {
			executions = append(executions, execution)
		}
	}
	lifecycleRegistry.mu.Unlock()
	type streamWithOrder struct {
		stream    ExecStream
		startedAt time.Time
		execID    string
	}
	ordered := make([]streamWithOrder, 0, len(executions))
	for _, execution := range executions {
		execution.mu.Lock()
		if execution.status == "running" || execution.status == "queued" || execution.status == "persisting" {
			ordered = append(ordered, streamWithOrder{
				stream: ExecStream{
					ExecID: execution.request.ExecID, ToolUseID: execution.request.ToolUseID,
					ToolName: execution.request.ToolName, StartedAt: execution.startedAt,
					Background: execution.request.Background, Status: execution.status, Stdout: execution.stdout,
					ThroughChunkSequence: execution.stdoutSeq,
					StdoutStartByte:      execution.stdoutBytes - uint64(len(execution.stdout)),
					StdoutEndByte:        execution.stdoutBytes,
					StdoutTruncated:      execution.stdoutBytes > uint64(len(execution.stdout)),
				},
				startedAt: execution.startedAt, execID: execution.request.ExecID,
			})
		}
		execution.mu.Unlock()
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].startedAt.Equal(ordered[j].startedAt) {
			return ordered[i].execID < ordered[j].execID
		}
		return ordered[i].startedAt.Before(ordered[j].startedAt)
	})
	streams := make([]ExecStream, len(ordered))
	for index := range ordered {
		streams[index] = ordered[index].stream
	}
	return streams
}

func (m *Manager) Restart(ctx context.Context, kernelID string) (*Worker, error) {
	if m == nil {
		return nil, errors.New("kernel manager is not configured")
	}
	worker := m.workerByID(kernelID)
	if worker == nil {
		return nil, fmt.Errorf("kernel %q is not running", strings.TrimSpace(kernelID))
	}
	state := lifecycleWorkerState(worker)
	if state == nil {
		return nil, fmt.Errorf("kernel %q has no session identity", strings.TrimSpace(kernelID))
	}
	state.mu.Lock()
	spec := state.spec
	state.mu.Unlock()
	m.interruptWorkerExecutions(worker)
	worker.setStopReason("was shut down")
	if err := worker.Close(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return nil, err
	}
	if err := m.waitForWorkerExecutions(ctx, worker); err != nil {
		return nil, err
	}
	return m.StartSession(spec)
}

func (m *Manager) interruptWorkerExecutions(worker *Worker) {
	lifecycleRegistry.mu.Lock()
	executions := make([]*managedExecution, 0)
	for _, execution := range lifecycleRegistry.executions {
		if execution.worker == worker {
			executions = append(executions, execution)
		}
	}
	lifecycleRegistry.mu.Unlock()
	for _, execution := range executions {
		m.Interrupt(execution.request.FrameID, execution.request.ExecID)
	}
}

func (m *Manager) waitForWorkerExecutions(ctx context.Context, worker *Worker) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		lifecycleRegistry.mu.Lock()
		active := false
		for _, execution := range lifecycleRegistry.executions {
			if execution.worker == worker {
				active = true
				break
			}
		}
		lifecycleRegistry.mu.Unlock()
		if !active {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Refresh interrupts active cells before closing their workers. This avoids
// making the public refresh request wait for arbitrary user code to finish.
func (m *Manager) Refresh(ctx context.Context) (int, error) {
	if m == nil {
		return 0, errors.New("kernel manager is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	workers := make([]*Worker, 0, len(m.workers))
	for _, worker := range m.workers {
		workers = append(workers, worker)
	}
	m.mu.Unlock()
	for _, worker := range workers {
		m.interruptWorkerExecutions(worker)
	}
	closed, err := m.CloseAll(ctx)
	for _, worker := range workers {
		if waitErr := m.waitForWorkerExecutions(ctx, worker); err == nil && waitErr != nil {
			err = waitErr
		}
	}
	return closed, err
}

package kernel

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"synon-go/internal/assets"
	"synon-go/internal/processsupervisor"
)

const (
	defaultShutdownTimeout = 5 * time.Second
	maxProtocolLineBytes   = 48 * 1024 * 1024
	maxDiagnosticBytes     = 64 * 1024
	maxPendingRestartAge   = time.Hour
)

type Config struct {
	Python                   string
	Micromamba               string
	CondaHome                string
	CondaEnvsPath            string
	CondaRuntimeCatalog      string
	ManagedPythonEnvironment string
	PythonHelperPath         string
	SDFValidatorPath         string
	RWorkerPath              string
	DefaultREnv              string
	RSharedLibsBase          string
	RSharedPackages          []string
	DisableROperationLog     bool
	AssetRoot                string
	ManifestPath             string
	WorkerPath               string
	WorkerResourceDirectory  string
	ShutdownTimeout          time.Duration
	// ManagedEnvironmentInstallerInactivityTimeout bounds only periods with no
	// observed installer output, CPU work, process-tree changes, or I/O. It is
	// deliberately separate from a wall-clock execution deadline.
	ManagedEnvironmentInstallerInactivityTimeout time.Duration
	// InstallerProxy is the normalized product network proxy used only for
	// bundled package installation. It is never inherited by task runtimes.
	InstallerProxy string
	// ExecutionTimeout is an optional active-cell wall-clock deadline. Zero
	// leaves active cells running until completion, explicit cancellation, or
	// worker shutdown; the separate worker idle policy owns unused lifetimes.
	ExecutionTimeout time.Duration
	InterruptGrace   time.Duration
	Environment      map[string]string
}

type Response struct {
	ID          string         `json:"id"`
	Stdout      string         `json:"stdout"`
	Stderr      string         `json:"stderr"`
	Error       string         `json:"error"`
	Interrupted bool           `json:"interrupted"`
	Preflight   map[string]any `json:"preflight,omitempty"`
	Trace       map[string]any `json:"trace"`
	Usage       map[string]any `json:"usage"`
}

type request struct {
	ID           string `json:"id"`
	Code         string `json:"code"`
	Origin       string `json:"origin"`
	ToolName     string `json:"tool_name,omitempty"`
	WorkspaceDir string `json:"workspace_dir"`
	WorkingDir   string `json:"working_dir,omitempty"`
	HostEnabled  bool   `json:"host_enabled"`
	Fresh        bool   `json:"fresh,omitempty"`
}

type protocolMessage struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Data string `json:"data"`
}

type Manager struct {
	config Config

	verifyMu      sync.Mutex
	verified      bool
	verifyErr     error
	environmentMu sync.Mutex

	mu      sync.Mutex
	workers map[string]*Worker

	restartMu      sync.Mutex
	pendingRestart map[string]pendingKernelRestart

	managedPythonState                managedRuntimeProvisioningState
	managedRState                     managedRuntimeProvisioningState
	managedRReadiness                 managedRReadinessCache
	scientificRuntimeMu               sync.Mutex
	managedEnvironmentMu              sync.Mutex
	managedEnvironmentSupervisor      context.Context
	managedEnvironmentSupervisorReady chan struct{}
	managedEnvironmentSupervisorOnce  sync.Once
	managedEnvironmentOperations      map[string]*managedEnvironmentOperation
	managedEnvironmentHealthMu        sync.Mutex
	managedEnvironmentHealth          map[string]*managedEnvironmentHealthCheck
	idleWake                          chan struct{}
	runtimeWake                       chan struct{}
	resourceSampler                   *resourceSampler
	providerMu                        sync.Mutex
	providerWorkers                   map[string]*providerKernelState
	stdoutObserverMu                  sync.RWMutex
	stdoutObserver                    func(ExecStdoutChunk)
}

// ExecStdoutChunk is one ordered stdout fragment from a live execution.
type ExecStdoutChunk struct {
	FrameID     string
	RootFrameID string
	ExecID      string
	ToolUseID   string
	ToolName    string
	StartedAt   time.Time
	Background  bool
	Status      string
	Chunk       string
	Sequence    uint64
	StartByte   uint64
	EndByte     uint64
}

// SetStdoutObserver binds the service-owned durable publication sink. Kernel
// execution remains authoritative when no observer is attached.
func (m *Manager) SetStdoutObserver(observer func(ExecStdoutChunk)) {
	if m == nil {
		return
	}
	m.stdoutObserverMu.Lock()
	m.stdoutObserver = observer
	m.stdoutObserverMu.Unlock()
}

func (m *Manager) currentStdoutObserver() func(ExecStdoutChunk) {
	m.stdoutObserverMu.RLock()
	defer m.stdoutObserverMu.RUnlock()
	return m.stdoutObserver
}

type managedRuntimeProvision struct {
	done           chan struct{}
	err            error
	phase          string
	startedAt      time.Time
	lastProgressAt time.Time
}

type managedRuntimeProvisioningState struct {
	mu        sync.Mutex
	provision *managedRuntimeProvision
}

type pendingKernelRestart struct {
	reason     string
	recordedAt time.Time
}

type Worker struct {
	id           string
	workspaceDir string
	manager      *Manager
	command      *exec.Cmd
	process      *workerProcess
	stdin        io.WriteCloser
	responses    chan Response
	done         chan struct{}
	diagnostics  *tailBuffer

	executeMu       sync.Mutex
	writeMu         sync.Mutex
	hostMu          sync.Mutex
	hostCell        string
	hostCalls       chan<- HostCall
	startMu         sync.Mutex
	startWait       map[string]chan struct{}
	streamMu        sync.Mutex
	streamCell      string
	streamSink      func(string)
	closeOnce       sync.Once
	waitMu          sync.Mutex
	waitErr         error
	egressProxy     *kernelEgressProxy
	stopMu          sync.Mutex
	stopReason      string
	restartKey      string
	restartLanguage string
}

func NewManager(config Config) *Manager {
	if config.ShutdownTimeout <= 0 {
		config.ShutdownTimeout = defaultShutdownTimeout
	}
	if config.ManagedEnvironmentInstallerInactivityTimeout <= 0 {
		config.ManagedEnvironmentInstallerInactivityTimeout = processsupervisor.DefaultInactivityTimeout
	}
	config.AssetRoot = cleanOptionalPath(config.AssetRoot)
	config.ManifestPath = cleanOptionalPath(config.ManifestPath)
	config.WorkerPath = cleanOptionalPath(config.WorkerPath)
	config.CondaRuntimeCatalog = cleanOptionalPath(config.CondaRuntimeCatalog)
	config.PythonHelperPath = cleanOptionalPath(config.PythonHelperPath)
	config.SDFValidatorPath = cleanOptionalPath(config.SDFValidatorPath)
	if strings.TrimSpace(config.ManagedPythonEnvironment) == "" {
		config.ManagedPythonEnvironment = defaultManagedPythonEnvironment
	}
	if strings.TrimSpace(config.RWorkerPath) == "" && strings.TrimSpace(config.AssetRoot) != "" {
		config.RWorkerPath = filepath.Join(config.AssetRoot, "kernels", "kernel_worker.R")
	}
	config.RWorkerPath = cleanOptionalPath(config.RWorkerPath)
	if strings.TrimSpace(config.DefaultREnv) == "" {
		config.DefaultREnv = defaultManagedREnvironment
	}
	if strings.TrimSpace(config.CondaHome) == "" && strings.TrimSpace(config.CondaEnvsPath) != "" {
		// Keep installer caches beside an explicitly supplied environment root;
		// never fall back to a relative `pkgs` directory or the process cwd.
		config.CondaHome = filepath.Dir(filepath.Clean(config.CondaEnvsPath))
	}
	if strings.TrimSpace(config.CondaEnvsPath) == "" && strings.TrimSpace(config.CondaHome) != "" {
		config.CondaEnvsPath = filepath.Join(config.CondaHome, "envs")
	}
	if strings.TrimSpace(config.RSharedLibsBase) == "" && strings.TrimSpace(config.CondaHome) != "" {
		config.RSharedLibsBase = filepath.Join(config.CondaHome, "r-shared-libs")
	}
	config.RSharedLibsBase = cleanOptionalPath(config.RSharedLibsBase)
	return &Manager{
		config: config, workers: map[string]*Worker{}, pendingRestart: map[string]pendingKernelRestart{},
		managedEnvironmentSupervisorReady: make(chan struct{}),
		managedEnvironmentOperations:      map[string]*managedEnvironmentOperation{},
		managedEnvironmentHealth:          map[string]*managedEnvironmentHealthCheck{},
		providerWorkers:                   map[string]*providerKernelState{},
		idleWake:                          make(chan struct{}, 1), runtimeWake: make(chan struct{}, 1),
	}
}

func (m *Manager) RuntimeWake() <-chan struct{} {
	if m == nil {
		return nil
	}
	return m.runtimeWake
}

func (m *Manager) notifyRuntimeChange() {
	if m == nil || m.runtimeWake == nil {
		return
	}
	select {
	case m.runtimeWake <- struct{}{}:
	default:
	}
}

func (m *Manager) IdleWake() <-chan struct{} {
	if m == nil {
		return nil
	}
	return m.idleWake
}

func (m *Manager) notifyIdleChange() {
	if m == nil || m.idleWake == nil {
		return
	}
	select {
	case m.idleWake <- struct{}{}:
	default:
	}
}

func cleanOptionalPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return filepath.Clean(value)
}

func (m *Manager) Start(id, workspaceDir string) (*Worker, error) {
	if err := m.Verify(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(m.config.Python) == "" {
		return nil, errors.New("kernel Python executable is not configured")
	}
	mounts, err := platformSessionRuntimeMounts(m.config.Python, "", m.config.WorkerPath)
	if err != nil {
		return nil, err
	}
	return m.startWorker(id, workspaceDir, m.config.Python, pythonWorkerArguments(m.config.WorkerPath), kernelEnvironment(m.config.Environment), mounts, nil, "", "")
}

func pythonWorkerArguments(workerPath string) []string {
	workerPath = strings.TrimSpace(workerPath)
	arguments := []string{workerPath}
	directory := filepath.Dir(workerPath)
	for _, name := range append([]string{
		"synon_host_bridge.py",
		"sitecustomize.py",
	}, pythonRuntimePackageAssets()...) {
		path := filepath.Join(directory, name)
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			arguments = append(arguments, path)
		}
	}
	return arguments
}

func (m *Manager) startWorker(id, workspaceDir, executable string, arguments, environment []string, mounts []WorkerMount, protected []string, restartKey, restartLanguage string) (*Worker, error) {
	return m.startWorkerWithAuxiliary(id, workspaceDir, executable, arguments, environment, mounts, protected, restartKey, restartLanguage, nil)
}

func (m *Manager) startWorkerWithAuxiliary(
	id, workspaceDir, executable string,
	arguments, environment []string,
	mounts []WorkerMount,
	protected []string,
	restartKey, restartLanguage string,
	auxiliary []*os.File,
) (*Worker, error) {
	return m.startWorkerWithRuntime(
		id, workspaceDir, executable, arguments, environment, mounts, protected,
		restartKey, restartLanguage, auxiliary, nil,
	)
}

func (m *Manager) startWorkerWithRuntime(
	id, workspaceDir, executable string,
	arguments, environment []string,
	mounts []WorkerMount,
	protected []string,
	restartKey, restartLanguage string,
	auxiliary []*os.File,
	egressProxy *kernelEgressProxy,
) (*Worker, error) {
	if egressProxy != nil {
		child := egressProxy.takeChild()
		if child == nil {
			return nil, errors.New("kernel egress child descriptor is unavailable")
		}
		controlFD := 4 + len(mounts) + len(auxiliary)
		environment = mergeKernelEnvironment(environment, map[string]string{
			"SYNON_KERNEL_EGRESS_CONTROL_FD": strconv.Itoa(controlFD),
		})
		auxiliary = append(auxiliary, child)
	}
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 128 || strings.ContainsAny(id, "/\\\x00\r\n") {
		return nil, errors.New("kernel id must be a bounded path-free string")
	}
	workspaceDir = strings.TrimSpace(workspaceDir)
	if workspaceDir == "" || !filepath.IsAbs(workspaceDir) {
		return nil, errors.New("kernel workspace must be an absolute directory")
	}
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		return nil, fmt.Errorf("create kernel workspace: %w", err)
	}
	resolvedWorkspace, err := filepath.EvalSymlinks(workspaceDir)
	if err != nil {
		return nil, fmt.Errorf("resolve kernel workspace: %w", err)
	}
	executable = strings.TrimSpace(executable)
	if executable == "" || !filepath.IsAbs(executable) {
		return nil, errors.New("kernel runtime executable must be an absolute path")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if existing := m.workers[id]; existing != nil {
		select {
		case <-existing.done:
			delete(m.workers, id)
		default:
			for _, file := range auxiliary {
				_ = file.Close()
			}
			return existing, nil
		}
	}
	command, err := newConfinedWorkerCommandWithAuxiliary(resolvedWorkspace, executable, arguments, environment, mounts, protected, auxiliary)
	if err != nil {
		return nil, err
	}
	defer closeKernelCommandExtraFiles(command)
	// Use the validated drive-rooted workspace on Windows; "/" has no
	// well-defined native working-directory meaning there.
	if runtime.GOOS == "windows" {
		command.Dir = resolvedWorkspace
	} else {
		command.Dir = "/"
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open kernel stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("open kernel stdout: %w", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("open kernel stderr: %w", err)
	}
	process, err := startWorkerProcess(command, m.config.WorkerResourceDirectory)
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("start kernel worker: %w", err)
	}
	worker := &Worker{
		id: id, workspaceDir: resolvedWorkspace, manager: m, command: command, process: process, stdin: stdin,
		responses: make(chan Response, 8), done: make(chan struct{}), diagnostics: newTailBuffer(maxDiagnosticBytes),
		startWait: make(map[string]chan struct{}), restartKey: restartKey, restartLanguage: restartLanguage,
		egressProxy: egressProxy,
	}
	m.workers[id] = worker
	go worker.readProtocol(stdout)
	go func() { _, _ = io.Copy(worker.diagnostics, stderr) }()
	go worker.wait()
	return worker, nil
}

func closeKernelCommandExtraFiles(command *exec.Cmd) {
	if command == nil {
		return
	}
	for _, file := range command.ExtraFiles {
		_ = file.Close()
	}
	command.ExtraFiles = nil
}

// Verify validates the optional kernel sidecar once and caches the result.
// RetryVerification exists for the environment-retry compatibility contract.
func (m *Manager) Verify() error {
	if m == nil {
		return errors.New("kernel manager is not configured")
	}
	m.verifyMu.Lock()
	defer m.verifyMu.Unlock()
	if !m.verified {
		m.verifyErr = m.verifyAssets()
		m.verified = true
	}
	return m.verifyErr
}

func (m *Manager) RetryVerification() error {
	if m == nil {
		return errors.New("kernel manager is not configured")
	}
	m.verifyMu.Lock()
	defer m.verifyMu.Unlock()
	m.verifyErr = m.verifyAssets()
	m.verified = true
	return m.verifyErr
}

func (m *Manager) ActiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.workers)
}

func (m *Manager) CloseAll(ctx context.Context) (int, error) {
	m.mu.Lock()
	workers := make([]*Worker, 0, len(m.workers))
	for _, worker := range m.workers {
		workers = append(workers, worker)
	}
	m.mu.Unlock()
	var errs []error
	for _, worker := range workers {
		if err := worker.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close kernel %s: %w", worker.id, err))
		}
	}
	return len(workers), errors.Join(errs...)
}

// CloseSession stops every managed kernel belonging to one root frame while
// leaving kernels for other conversations untouched.
func (m *Manager) CloseSession(ctx context.Context, rootFrameID string) (int, error) {
	if m == nil {
		return 0, nil
	}
	rootFrameID = strings.TrimSpace(rootFrameID)
	if rootFrameID == "" {
		return 0, errors.New("root frame id is required")
	}
	m.mu.Lock()
	workers := make([]*Worker, 0)
	for _, worker := range m.workers {
		state := lifecycleWorkerState(worker)
		if state == nil {
			continue
		}
		state.mu.Lock()
		matches := state.spec.RootFrameID == rootFrameID
		state.mu.Unlock()
		if matches {
			workers = append(workers, worker)
		}
	}
	m.mu.Unlock()
	var errs []error
	for _, worker := range workers {
		if err := worker.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close kernel %s: %w", worker.id, err))
		}
	}
	return len(workers), errors.Join(errs...)
}

// CloseOwner synchronously invalidates every kernel namespace whose mount
// authority was derived from one owner's host grants. A grant mutation is not
// complete until the old namespaces have exited.
func (m *Manager) CloseOwner(ctx context.Context, ownerID string) (int, error) {
	if m == nil {
		return 0, nil
	}
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return 0, errors.New("kernel owner id is required")
	}
	m.mu.Lock()
	workers := make([]*Worker, 0)
	for _, worker := range m.workers {
		state := lifecycleWorkerState(worker)
		if state == nil {
			continue
		}
		state.mu.Lock()
		matches := state.spec.OwnerID == ownerID
		state.mu.Unlock()
		if matches {
			workers = append(workers, worker)
		}
	}
	m.mu.Unlock()
	var errs []error
	for _, worker := range workers {
		if err := worker.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close kernel %s: %w", worker.id, err))
		}
	}
	return len(workers), errors.Join(errs...)
}

// TerminateOwner is the security-sensitive counterpart to CloseOwner. It
// sends SIGKILL to every captured namespace before waiting, so a revoked or
// downgraded host grant cannot remain usable during a graceful shutdown.
func (m *Manager) TerminateOwner(ctx context.Context, ownerID string) (int, error) {
	if m == nil {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return 0, errors.New("kernel owner id is required")
	}
	m.mu.Lock()
	workers := make([]*Worker, 0)
	for _, worker := range m.workers {
		state := lifecycleWorkerState(worker)
		if state == nil {
			continue
		}
		state.mu.Lock()
		matches := state.spec.OwnerID == ownerID
		state.mu.Unlock()
		if matches {
			workers = append(workers, worker)
		}
	}
	m.mu.Unlock()
	var errs []error
	for _, worker := range workers {
		if err := worker.process.kill(); err != nil {
			errs = append(errs, fmt.Errorf("terminate kernel %s: %w", worker.id, err))
		}
	}
	for _, worker := range workers {
		select {
		case <-worker.done:
		case <-ctx.Done():
			errs = append(errs, fmt.Errorf("terminate kernel %s: %w", worker.id, ctx.Err()))
		}
	}
	return len(workers), errors.Join(errs...)
}

// CloseKernel stops one exact managed kernel without affecting siblings.
func (m *Manager) CloseKernel(ctx context.Context, kernelID string) error {
	return m.CloseKernelWithReason(ctx, kernelID, "")
}

// CloseKernelWithReason stops one exact managed kernel and preserves a bounded,
// user-visible reason for the active cell and a future R restart notice.
func (m *Manager) CloseKernelWithReason(ctx context.Context, kernelID, reason string) error {
	if m == nil {
		return nil
	}
	kernelID = strings.TrimSpace(kernelID)
	if kernelID == "" {
		return errors.New("kernel id is required")
	}
	worker := m.workerByID(kernelID)
	if worker == nil {
		return nil
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		worker.setStopReason("was stopped by user: " + reason)
	}
	return worker.Close(ctx)
}

// TerminateKernel immediately kills one exact managed kernel. It is reserved
// for the explicit force path after a cooperative interrupt failed to settle.
func (m *Manager) TerminateKernel(ctx context.Context, kernelID, reason string) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	kernelID = strings.TrimSpace(kernelID)
	if kernelID == "" {
		return errors.New("kernel id is required")
	}
	worker := m.workerByID(kernelID)
	if worker == nil {
		return nil
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		worker.setStopReason("was force-killed by user: " + reason)
	} else {
		worker.setStopReason("was force-killed by user")
	}
	if err := worker.process.kill(); err != nil {
		return err
	}
	select {
	case <-worker.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(worker.manager.config.ShutdownTimeout):
		return errors.New("kernel process did not exit after force kill")
	}
}

func (m *Manager) verifyAssets() error {
	if strings.TrimSpace(m.config.AssetRoot) == "" || strings.TrimSpace(m.config.ManifestPath) == "" || strings.TrimSpace(m.config.WorkerPath) == "" {
		return errors.New("kernel asset root, manifest, and worker paths are required")
	}
	manifest, err := assets.Load(m.config.ManifestPath)
	if err != nil {
		return fmt.Errorf("load kernel assets: %w", err)
	}
	if manifest.Entrypoint == "" {
		return errors.New("kernel asset manifest has no entrypoint")
	}
	if _, err := assets.Verify(m.config.AssetRoot, manifest); err != nil {
		return fmt.Errorf("verify kernel assets: %w", err)
	}
	expected, err := filepath.EvalSymlinks(filepath.Join(m.config.AssetRoot, filepath.FromSlash(manifest.Entrypoint)))
	if err != nil {
		return fmt.Errorf("resolve manifest kernel worker: %w", err)
	}
	configured, err := filepath.EvalSymlinks(m.config.WorkerPath)
	if err != nil {
		return fmt.Errorf("resolve configured kernel worker: %w", err)
	}
	if expected != configured {
		return errors.New("configured kernel worker does not match verified manifest entrypoint")
	}
	return nil
}

func (m *Manager) remove(id string, worker *Worker) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.workers[id] == worker {
		delete(m.workers, id)
	}
}

func (w *Worker) Execute(ctx context.Context, code, origin string) (Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	w.executeMu.Lock()
	defer w.executeMu.Unlock()
	if origin != "user" {
		origin = "agent"
	}
	payload, err := json.Marshal(request{
		ID: uuid.NewString(), Code: code, Origin: origin, WorkspaceDir: w.workspaceDir,
	})
	if err != nil {
		return Response{}, err
	}
	select {
	case <-w.done:
		return Response{}, w.stoppedError()
	default:
	}
	if err := w.writeProtocol(payload); err != nil {
		return Response{}, fmt.Errorf("write kernel request: %w", err)
	}
	for {
		select {
		case response := <-w.responses:
			var sent request
			_ = json.Unmarshal(payload, &sent)
			if response.ID == sent.ID {
				if state := lifecycleWorkerState(w); state != nil {
					state.mu.Lock()
					response = consumeRKernelRestartNotice(state, state.generation, response)
					state.mu.Unlock()
				}
				return response, nil
			}
		case <-w.done:
			return Response{}, w.stoppedError()
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				w.setStopReason("was shut down after a cell timeout")
			} else {
				w.setStopReason("was shut down")
			}
			_ = w.process.kill()
			return Response{}, ctx.Err()
		}
	}
}

func (w *Worker) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	w.setStopReason("was shut down")
	w.closeOnce.Do(func() { _ = w.stdin.Close() })
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		_ = w.process.kill()
		select {
		case <-w.done:
			return ctx.Err()
		case <-time.After(w.manager.config.ShutdownTimeout):
			return errors.New("kernel process did not exit after kill")
		}
	}
}

func (w *Worker) readProtocol(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), maxProtocolLineBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		var message protocolMessage
		if err := json.Unmarshal(line, &message); err != nil {
			_, _ = w.diagnostics.Write(append(append([]byte("invalid protocol line: "), line...), '\n'))
			_ = w.process.kill()
			return
		}
		if message.Type == "host_call" {
			call, err := decodeHostCall(line)
			if err != nil {
				_, _ = w.diagnostics.Write([]byte(err.Error() + "\n"))
				_ = w.process.kill()
				return
			}
			w.routeHostCall(call)
			continue
		}
		if message.Type == "execution_started" {
			w.routeExecutionStarted(message.ID)
			continue
		}
		if message.Type == "stdout_chunk" {
			w.routeStdoutChunk(message.Data)
			continue
		}
		if message.Type != "" || message.ID == "" {
			continue
		}
		var response Response
		if err := json.Unmarshal(line, &response); err != nil {
			_, _ = w.diagnostics.Write([]byte(err.Error() + "\n"))
			_ = w.process.kill()
			return
		}
		select {
		case w.responses <- response:
		case <-w.done:
			return
		}
	}
	if err := scanner.Err(); err != nil {
		_, _ = w.diagnostics.Write([]byte(err.Error() + "\n"))
		_ = w.process.kill()
	}
}

func (w *Worker) bindExecutionStart(id string) (<-chan struct{}, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("kernel execution start requires an id")
	}
	w.startMu.Lock()
	defer w.startMu.Unlock()
	if _, exists := w.startWait[id]; exists {
		return nil, fmt.Errorf("kernel execution start %q is already registered", id)
	}
	started := make(chan struct{})
	w.startWait[id] = started
	return started, nil
}

func (w *Worker) unbindExecutionStart(id string) {
	w.startMu.Lock()
	delete(w.startWait, strings.TrimSpace(id))
	w.startMu.Unlock()
}

func (w *Worker) routeExecutionStarted(id string) {
	w.startMu.Lock()
	started := w.startWait[strings.TrimSpace(id)]
	if started != nil {
		delete(w.startWait, strings.TrimSpace(id))
		close(started)
	}
	w.startMu.Unlock()
}

func (w *Worker) bindStdoutStream(cellID string, sink func(string)) error {
	w.streamMu.Lock()
	defer w.streamMu.Unlock()
	if w.streamCell != "" || w.streamSink != nil {
		return errors.New("kernel stdout stream is already bound")
	}
	if strings.TrimSpace(cellID) == "" || sink == nil {
		return errors.New("kernel stdout stream requires a cell id and sink")
	}
	w.streamCell, w.streamSink = cellID, sink
	return nil
}

func (w *Worker) unbindStdoutStream(cellID string) {
	w.streamMu.Lock()
	defer w.streamMu.Unlock()
	if w.streamCell == cellID {
		w.streamCell, w.streamSink = "", nil
	}
}

func (w *Worker) routeStdoutChunk(chunk string) {
	if chunk == "" {
		return
	}
	w.streamMu.Lock()
	sink := w.streamSink
	w.streamMu.Unlock()
	if sink != nil {
		sink(chunk)
	}
}

func (w *Worker) writeProtocol(payload []byte) error {
	if len(payload) == 0 || len(payload) > maxProtocolLineBytes {
		return errors.New("kernel protocol payload is empty or too large")
	}
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	_, err := w.stdin.Write(append(append([]byte(nil), payload...), '\n'))
	return err
}

func (w *Worker) bindHostCalls(cellID string, calls chan<- HostCall) error {
	w.hostMu.Lock()
	defer w.hostMu.Unlock()
	if w.hostCell != "" || w.hostCalls != nil {
		return errors.New("kernel host-call execution is already bound")
	}
	w.hostCell, w.hostCalls = cellID, calls
	return nil
}

func (w *Worker) unbindHostCalls(cellID string) {
	w.hostMu.Lock()
	defer w.hostMu.Unlock()
	if w.hostCell == cellID {
		w.hostCell, w.hostCalls = "", nil
	}
}

func (w *Worker) routeHostCall(call HostCall) {
	if err := w.writeHostAck(call.ID); err != nil {
		_, _ = w.diagnostics.Write([]byte("write host ack: " + err.Error() + "\n"))
		_ = w.process.kill()
		return
	}
	w.hostMu.Lock()
	cellID, calls := w.hostCell, w.hostCalls
	w.hostMu.Unlock()
	if cellID == "" || calls == nil || call.CellID != cellID {
		_ = w.writeHostResult(call, nil, NewHostCallError("context_mismatch", "host call does not belong to the active cell"))
		return
	}
	select {
	case calls <- call:
	case <-w.done:
	}
}

func (w *Worker) writeHostAck(callID string) error {
	if !hostCallIDPattern.MatchString(callID) {
		return errors.New("invalid host call id")
	}
	payload, err := json.Marshal(map[string]string{"type": "host_ack", "for": callID})
	if err != nil {
		return err
	}
	return w.writeProtocol(payload)
}

func (w *Worker) writeHostResult(call HostCall, result any, callErr error) error {
	wire := hostResultWire{Type: "host_result", ID: call.ID, CellID: call.CellID, OK: callErr == nil, Result: result}
	if callErr != nil {
		wire.Result = nil
		wire.Error = hostCallFailure(callErr)
	}
	payload, err := json.Marshal(wire)
	if err != nil {
		wire.OK, wire.Result = false, nil
		wire.Error = &hostResultError{Code: "invalid_result", Message: "host result is not JSON serializable"}
		payload, err = json.Marshal(wire)
	}
	if err != nil {
		return err
	}
	if len(payload) > maxHostResultBytes {
		wire.OK, wire.Result = false, nil
		wire.Error = &hostResultError{Code: "result_too_large", Message: fmt.Sprintf("host result exceeds %d bytes", maxHostResultBytes)}
		payload, err = json.Marshal(wire)
		if err != nil {
			return err
		}
	}
	return w.writeProtocol(payload)
}

func (w *Worker) wait() {
	err := w.command.Wait()
	// Capture the physical exit before another lifecycle owner can close the
	// already-dead worker and replace an OOM/SIGKILL diagnostic with the generic
	// "was shut down" reason. Explicit cancellation sets its reason first and
	// remains authoritative because setStopReason never overwrites it.
	w.setStopReason(unexpectedKernelStopReason(w.command, err))
	if w.process != nil {
		w.process.close()
	}
	if w.egressProxy != nil {
		w.egressProxy.Close()
	}
	w.waitMu.Lock()
	w.waitErr = err
	w.waitMu.Unlock()
	if w.restartLanguage == "r" && w.restartKey != "" {
		w.manager.recordKernelRestart(w.restartKey, w.restartReason(err))
	}
	w.manager.remove(w.id, w)
	close(w.done)
}

func (w *Worker) setStopReason(reason string) {
	if w == nil || strings.TrimSpace(reason) == "" {
		return
	}
	w.stopMu.Lock()
	if w.stopReason == "" {
		w.stopReason = reason
	}
	w.stopMu.Unlock()
}

func (w *Worker) restartReason(waitErr error) string {
	w.stopMu.Lock()
	reason := w.stopReason
	w.stopMu.Unlock()
	if reason != "" {
		return reason
	}
	return unexpectedKernelStopReason(w.command, waitErr)
}

func (m *Manager) recordKernelRestart(restartKey, reason string) {
	if m == nil || strings.TrimSpace(restartKey) == "" || strings.TrimSpace(reason) == "" {
		return
	}
	m.restartMu.Lock()
	defer m.restartMu.Unlock()
	now := time.Now().UTC()
	for id, pending := range m.pendingRestart {
		if now.Sub(pending.recordedAt) > maxPendingRestartAge {
			delete(m.pendingRestart, id)
		}
	}
	if len(m.pendingRestart) >= 1024 {
		var oldestID string
		var oldest time.Time
		for id, pending := range m.pendingRestart {
			if oldestID == "" || pending.recordedAt.Before(oldest) {
				oldestID, oldest = id, pending.recordedAt
			}
		}
		delete(m.pendingRestart, oldestID)
	}
	m.pendingRestart[restartKey] = pendingKernelRestart{reason: reason, recordedAt: now}
}

func (m *Manager) takeKernelRestart(restartKey string) string {
	pending, ok := m.peekKernelRestart(restartKey)
	if !ok || !m.consumeKernelRestart(restartKey, pending) {
		return ""
	}
	return pending.reason
}

func (m *Manager) peekKernelRestart(restartKey string) (pendingKernelRestart, bool) {
	if m == nil {
		return pendingKernelRestart{}, false
	}
	m.restartMu.Lock()
	defer m.restartMu.Unlock()
	pending := m.pendingRestart[restartKey]
	if pending.recordedAt.IsZero() || time.Since(pending.recordedAt) > maxPendingRestartAge {
		delete(m.pendingRestart, restartKey)
		return pendingKernelRestart{}, false
	}
	return pending, true
}

func (m *Manager) consumeKernelRestart(restartKey string, expected pendingKernelRestart) bool {
	if m == nil {
		return false
	}
	m.restartMu.Lock()
	defer m.restartMu.Unlock()
	current := m.pendingRestart[restartKey]
	if current.reason != expected.reason || !current.recordedAt.Equal(expected.recordedAt) {
		return false
	}
	delete(m.pendingRestart, restartKey)
	return true
}

func (w *Worker) stoppedError() error {
	w.waitMu.Lock()
	err := w.waitErr
	w.waitMu.Unlock()
	w.stopMu.Lock()
	stopReason := strings.TrimSpace(w.stopReason)
	w.stopMu.Unlock()
	if stopReason != "" {
		diagnostics := strings.TrimSpace(w.diagnostics.String())
		if diagnostics != "" {
			return fmt.Errorf("kernel worker %s: %s", stopReason, diagnostics)
		}
		return errors.New("kernel worker " + stopReason)
	}
	diagnostics := strings.TrimSpace(w.diagnostics.String())
	if diagnostics != "" {
		return fmt.Errorf("kernel worker stopped: %v: %s", err, diagnostics)
	}
	if err != nil {
		return fmt.Errorf("kernel worker stopped: %w", err)
	}
	return errors.New("kernel worker stopped")
}

// TerminalError returns the private operator diagnostic only after the worker
// process has stopped. Callers must not expose this error directly through a
// public API because it can contain runtime paths or interpreter diagnostics.
func (w *Worker) TerminalError() error {
	if w == nil {
		return errors.New("kernel worker is unavailable")
	}
	select {
	case <-w.done:
		return w.stoppedError()
	default:
		return nil
	}
}

// Stopped closes only after the physical process has exited, diagnostics have
// been captured and the manager has withdrawn its execution authority.
func (w *Worker) Stopped() <-chan struct{} {
	return w.done
}

func (w *Worker) ExecutionCount() int {
	state := lifecycleWorkerState(w)
	if state == nil {
		return 0
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.executionCount
}

func kernelEnvironment(extra map[string]string) []string {
	allowed := map[string]bool{
		"HOME": true, "PATH": true, "LANG": true, "LC_ALL": true, "LC_CTYPE": true,
		"TMPDIR": true, "TMP": true, "TEMP": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
	}
	if runtime.GOOS == "windows" {
		// Native process creation and Conda package scripts require the OS
		// directory and command-shell contract even when task runtimes receive
		// an otherwise narrow environment. These keys identify directories and
		// executables, never provider credentials.
		for _, key := range []string{
			"USERPROFILE", "HOMEDRIVE", "HOMEPATH", "SYSTEMROOT", "WINDIR",
			"COMSPEC", "PATHEXT", "APPDATA", "LOCALAPPDATA", "PROGRAMDATA",
			"PROCESSOR_ARCHITECTURE",
		} {
			allowed[key] = true
		}
	}
	values := make(map[string]string, len(allowed)+len(extra)+1)
	order := make([]string, 0, len(allowed)+len(extra)+1)
	remember := func(key, value string) {
		if runtime.GOOS == "windows" {
			key = strings.ToUpper(key)
		}
		if _, found := values[key]; !found {
			order = append(order, key)
		}
		values[key] = value
	}
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		lookupKey := key
		if runtime.GOOS == "windows" {
			lookupKey = strings.ToUpper(key)
		}
		if ok && allowed[lookupKey] {
			remember(key, value)
		}
	}
	// Native R accepts only a positive integer timeout (zero/Inf are ignored).
	// Use its representable ceiling, not an hours-long product cutoff. Setting
	// this at the launch boundary also covers Rscript started by Bash/Python.
	remember("R_DEFAULT_INTERNET_TIMEOUT", strconv.FormatInt(math.MaxInt32, 10))
	// Pip's timeout is per socket read, not a transfer deadline. Mamba's
	// minimum-rate heuristic must not reject a slow but progressing transfer;
	// the existing process supervisor owns observable inactivity and cancel.
	remember("PIP_TIMEOUT", strconv.Itoa(int(processsupervisor.DefaultInactivityTimeout/time.Second)))
	remember("MAMBA_NO_LOW_SPEED_LIMIT", "1")
	for key, value := range extra {
		if key == "" || strings.ContainsAny(key, "=\x00\r\n") || strings.ContainsAny(value, "\x00") {
			continue
		}
		// Exec environments are key/value maps in practice, but POSIX permits
		// duplicate entries and getenv commonly returns the first one. Replace
		// inherited values in place so a managed environment's PATH and locale
		// authority cannot be shadowed by the daemon process.
		remember(key, value)
	}
	remember("PYTHONUNBUFFERED", "1")
	environment := make([]string, 0, len(order))
	for _, key := range order {
		environment = append(environment, key+"="+values[key])
	}
	return environment
}

type tailBuffer struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func newTailBuffer(limit int) *tailBuffer {
	return &tailBuffer{limit: limit}
}

func (b *tailBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	original := len(value)
	if len(value) >= b.limit {
		b.data = append(b.data[:0], value[len(value)-b.limit:]...)
		return original, nil
	}
	if overflow := len(b.data) + len(value) - b.limit; overflow > 0 {
		copy(b.data, b.data[overflow:])
		b.data = b.data[:len(b.data)-overflow]
	}
	b.data = append(b.data, value...)
	return original, nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(append([]byte(nil), b.data...))
}

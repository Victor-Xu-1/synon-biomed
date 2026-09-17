//go:build linux

package detached

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

const (
	defaultBackendStartTimeout = 45 * time.Second
	defaultBackendPollInterval = 200 * time.Millisecond
	defaultBackendCloseTimeout = 15 * time.Second
)

// DefaultSocketRoot returns a stable, per-user and per-installation private
// control-plane directory whose longest backend socket remains inside Linux's
// sockaddr_un path budget. Durable state and logs continue to live under the
// configured HomeDir; only restart-surviving executor control sockets use this
// ephemeral machine-local directory.
func DefaultSocketRoot(homeDir string) (string, error) {
	homeDir = filepath.Clean(strings.TrimSpace(homeDir))
	if !filepath.IsAbs(homeDir) {
		return "", errors.New("detached kernel home must be absolute")
	}
	digest := sha256.Sum256([]byte(homeDir))
	root := filepath.Join("/tmp", fmt.Sprintf("synon-kernel-%d-%s", os.Geteuid(), hex.EncodeToString(digest[:6])))
	longest := filepath.Join(root, "kernel-backend-"+strings.Repeat("0", 36)+".sock")
	if len([]byte(longest)) > maxUnixSocketPathBytes {
		return "", errors.New("detached kernel socket root exceeds the unix path budget")
	}
	return root, nil
}

// Backend is the Web-service controller for durable detached kernel
// executors. It never owns the worker process and can be reconstructed after a
// service restart from the workspace database and private Unix socket.
type Backend struct {
	Store         *workspace.Store
	Executable    string
	HomeDir       string
	CondaHome     string
	CondaEnvsPath string
	SocketRoot    string
	LogDir        string
	StartTimeout  time.Duration
	IdleTimeout   time.Duration
	Launcher      ExecutorLauncher

	mu sync.Mutex

	sessionLocksMu sync.Mutex
	sessionLocks   map[string]*backendSessionLock
}

type backendSessionLock struct {
	mu         sync.Mutex
	references int
}

var _ kernelruntime.ExecutionBackend = (*Backend)(nil)

func (b *Backend) EnsureSession(ctx context.Context, spec kernelruntime.SessionSpec) (kernelruntime.BackendSessionRef, error) {
	if ctx == nil || b == nil || b.Store == nil {
		return kernelruntime.BackendSessionRef{}, errors.New("detached kernel backend is unavailable")
	}
	b.mu.Lock()
	if err := b.normalize(); err != nil {
		b.mu.Unlock()
		return kernelruntime.BackendSessionRef{}, err
	}
	b.mu.Unlock()
	if spec.KernelID == "" {
		stableID, err := kernelruntime.StableSessionID(spec)
		if err != nil {
			return kernelruntime.BackendSessionRef{}, err
		}
		spec.KernelID = stableID
	}
	unlockSession := b.lockSession(spec.KernelID)
	defer unlockSession()
	durableSpec := durableSessionSpec(spec)
	if existing, found, err := b.Store.FindKernelExecutionBackendForSession(ctx, durableSpec); err != nil {
		return kernelruntime.BackendSessionRef{}, err
	} else if found {
		if existing.State == workspace.KernelExecutionBackendStateReady &&
			existing.HeartbeatAt != nil && time.Since(*existing.HeartbeatAt) <= 3*defaultExecutorHeartbeatInterval &&
			validateTrustedUnixSocket(existing.SocketPath) == nil {
			return backendSessionRef(existing), nil
		}
		if existing.State == workspace.KernelExecutionBackendStateStarting {
			if ready, waitErr := b.waitForReady(ctx, existing.BackendID, existing.BackendGeneration, 2*time.Second); waitErr == nil {
				return backendSessionRef(ready), nil
			}
		}
		machineBootID, bootErr := machineBootID()
		if bootErr != nil {
			return kernelruntime.BackendSessionRef{}, bootErr
		}
		instanceID := "kernel-executor-" + uuid.NewString()
		socketPath := filepath.Join(b.SocketRoot, existing.BackendID+".sock")
		recreate := workspace.RecreateKernelExecutionBackendInput{
			BackendID: existing.BackendID, ExecutorInstanceID: instanceID,
			MachineBootID: machineBootID, SocketPath: socketPath,
			KernelID: existing.KernelID, KernelGeneration: existing.KernelGeneration,
			SessionSpec: durableSpec,
		}
		var backend workspace.KernelExecutionBackend
		var recreateErr error
		if existing.State == workspace.KernelExecutionBackendStateStarting && existing.ExecutorPID == 0 {
			backend, recreateErr = b.Store.RestartStartingKernelExecutionBackend(ctx, recreate)
		} else {
			// The persisted executor is stale: the process died, its heartbeat
			// expired, or it was killed by a service restart. Mark the evidence
			// lost, then reuse the same backend identity with a new executor
			// generation. Returning an error here would permanently block every
			// python/repl tool call for the frame after a restart.
			if _, finishErr := b.Store.FinishKernelExecutionBackend(ctx, workspace.FinishKernelExecutionBackendInput{
				BackendID: existing.BackendID, BackendGeneration: existing.BackendGeneration,
				ExecutorInstanceID: existing.ExecutorInstanceID, EvidenceLost: true,
			}); finishErr != nil && !errors.Is(finishErr, workspace.ErrKernelExecutionBackendStale) {
				return kernelruntime.BackendSessionRef{}, finishErr
			}
			backend, recreateErr = b.Store.RecreateKernelExecutionBackend(ctx, recreate)
		}
		if recreateErr != nil {
			return kernelruntime.BackendSessionRef{}, recreateErr
		}
		if err := b.launchExecutor(backend); err != nil {
			return kernelruntime.BackendSessionRef{}, err
		}
		ready, err := b.waitForReady(ctx, backend.BackendID, backend.BackendGeneration, b.StartTimeout)
		if err != nil {
			return kernelruntime.BackendSessionRef{}, err
		}
		return backendSessionRef(ready), nil
	}
	machineBootID, err := machineBootID()
	if err != nil {
		return kernelruntime.BackendSessionRef{}, err
	}
	kernelGeneration := int64(1)
	if predecessor, found, findErr := b.Store.FindLatestKernelExecutionBackendForIdentity(ctx, durableSpec); findErr != nil {
		return kernelruntime.BackendSessionRef{}, findErr
	} else if found {
		predecessor, dead, waitErr := waitForPredecessorKernelAuthorityRelease(
			ctx, predecessor,
			func(refreshCtx context.Context) (workspace.KernelExecutionBackend, bool, error) {
				return b.Store.FindLatestKernelExecutionBackendForIdentity(refreshCtx, durableSpec)
			},
		)
		if waitErr != nil {
			return kernelruntime.BackendSessionRef{}, waitErr
		}
		if dead && predecessor.State != workspace.KernelExecutionBackendStateStopped &&
			predecessor.State != workspace.KernelExecutionBackendStateEvidenceLost {
			if _, finishErr := b.Store.FinishKernelExecutionBackend(ctx, workspace.FinishKernelExecutionBackendInput{
				BackendID: predecessor.BackendID, BackendGeneration: predecessor.BackendGeneration,
				ExecutorInstanceID: predecessor.ExecutorInstanceID, EvidenceLost: true,
			}); finishErr != nil && !errors.Is(finishErr, workspace.ErrKernelExecutionBackendStale) {
				return kernelruntime.BackendSessionRef{}, finishErr
			}
		}
		kernelGeneration = predecessor.KernelGeneration + 1
	}
	backendID := "kernel-backend-" + uuid.NewString()
	instanceID := "kernel-executor-" + uuid.NewString()
	socketPath := filepath.Join(b.SocketRoot, backendID+".sock")
	backend, err := b.Store.CreateKernelExecutionBackend(ctx, workspace.CreateKernelExecutionBackendInput{
		BackendID: backendID, OwnerUserID: spec.OwnerID, ProjectID: spec.ProjectID,
		RootFrameID: spec.RootFrameID, RootFrameIncarnationID: spec.RootFrameIncarnationID,
		FrameID: spec.FrameID, FrameIncarnationID: spec.FrameIncarnationID,
		KernelID: spec.KernelID, KernelGeneration: kernelGeneration, SessionSpec: durableSpec,
		ExecutorInstanceID: instanceID, MachineBootID: machineBootID,
		SocketPath: socketPath, BackendGeneration: 1,
	})
	if err != nil {
		return kernelruntime.BackendSessionRef{}, err
	}
	if err := b.launchExecutor(backend); err != nil {
		return kernelruntime.BackendSessionRef{}, err
	}
	ready, err := b.waitForReady(ctx, backend.BackendID, backend.BackendGeneration, b.StartTimeout)
	if err != nil {
		return kernelruntime.BackendSessionRef{}, err
	}
	return backendSessionRef(ready), nil
}

// lockSession serializes creation and replacement of one durable kernel
// identity without coupling unrelated Frames or environments. EnsureSession
// may intentionally wait for a live predecessor to drain; a process-wide lock
// around that wait would make one slow scientific environment stall every
// other task and would also prevent an approved operation from recovering.
func (b *Backend) lockSession(kernelID string) func() {
	kernelID = strings.TrimSpace(kernelID)
	b.sessionLocksMu.Lock()
	if b.sessionLocks == nil {
		b.sessionLocks = make(map[string]*backendSessionLock)
	}
	lock := b.sessionLocks[kernelID]
	if lock == nil {
		lock = &backendSessionLock{}
		b.sessionLocks[kernelID] = lock
	}
	lock.references++
	b.sessionLocksMu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		b.sessionLocksMu.Lock()
		lock.references--
		if lock.references == 0 && b.sessionLocks[kernelID] == lock {
			delete(b.sessionLocks, kernelID)
		}
		b.sessionLocksMu.Unlock()
	}
}

// waitForPredecessorKernelAuthorityRelease follows the durable predecessor
// until it closes or is provably dead. A succeeding tool call must not fail
// merely because the previous executor is still draining its terminal result.
// The caller's lifecycle context owns cancellation; there is no task-duration
// cutoff here.
func waitForPredecessorKernelAuthorityRelease(
	ctx context.Context,
	predecessor workspace.KernelExecutionBackend,
	refresh func(context.Context) (workspace.KernelExecutionBackend, bool, error),
) (workspace.KernelExecutionBackend, bool, error) {
	for {
		if predecessor.State == workspace.KernelExecutionBackendStateStopped ||
			predecessor.State == workspace.KernelExecutionBackendStateEvidenceLost {
			return predecessor, false, nil
		}
		dead, err := predecessorBackendDefinitelyDead(predecessor, time.Now().UTC())
		if err != nil || dead {
			return predecessor, dead, err
		}
		timer := time.NewTimer(defaultBackendPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return predecessor, false, ctx.Err()
		case <-timer.C:
		}
		latest, found, err := refresh(ctx)
		if err != nil {
			return predecessor, false, err
		}
		if !found {
			return predecessor, false, workspace.ErrKernelExecutionBackendStale
		}
		predecessor = latest
	}
}

// predecessorBackendDefinitelyDead proves that a persisted predecessor no longer
// owns a live executor before its authority is reclaimed. Exact process identity
// is preferred; heartbeat age is only a fallback for records without a PID.
func predecessorBackendDefinitelyDead(backend workspace.KernelExecutionBackend, now time.Time) (bool, error) {
	if backend.ExecutorPID > 0 && backend.ExecutorPIDStartTicks > 0 {
		alive, err := kernelruntime.ProcessIdentityAlive(backend.ExecutorPID, backend.ExecutorPIDStartTicks)
		if err != nil {
			return false, err
		}
		return !alive, nil
	}
	lastLiveness := backend.UpdatedAt.UTC()
	if backend.HeartbeatAt != nil {
		lastLiveness = backend.HeartbeatAt.UTC()
	}
	return !lastLiveness.IsZero() && now.UTC().Sub(lastLiveness) >= 45*time.Second, nil
}

func (b *Backend) Start(
	ctx context.Context,
	ref kernelruntime.BackendExecutionRef,
	fence kernelruntime.BackendStartFence,
) (kernelruntime.BackendDispatchReceipt, error) {
	if err := b.validateExecutionRef(ctx, ref); err != nil {
		return kernelruntime.BackendDispatchReceipt{}, err
	}
	execution, err := b.Store.CommitKernelExecutionDispatch(ctx, workspace.CommitKernelExecutionDispatchInput{
		KernelDetachedExecutionControlInput: workspace.KernelDetachedExecutionControlInput{
			ExecutionID: ref.ExecutionID, BackendGeneration: ref.BackendGeneration,
			ControllerEpoch: fence.ControllerEpoch, ControllerToken: fence.ControllerToken,
			ExpectedVersion: fence.ExecutionStateVersion,
		},
		DispatchSequence: fence.DispatchSequence,
	})
	if err != nil {
		return kernelruntime.BackendDispatchReceipt{}, err
	}
	backend, found, err := b.Store.GetKernelExecutionBackend(ctx, ref.BackendID)
	if err != nil || !found {
		if err != nil {
			return kernelruntime.BackendDispatchReceipt{}, err
		}
		return kernelruntime.BackendDispatchReceipt{}, workspace.ErrKernelExecutionBackendStale
	}
	response, err := (Client{SocketPath: backend.SocketPath}).Call(ctx, CommandRequest{
		Version: ProtocolVersion, RequestID: "dispatch-" + uuid.NewString(), Command: CommandDispatch,
		BackendID: ref.BackendID, BackendGeneration: ref.BackendGeneration,
		ExecutionID: ref.ExecutionID, ExpectedVersion: execution.StateVersion,
		ControllerEpoch: fence.ControllerEpoch, ControllerToken: fence.ControllerToken,
		DispatchSequence: fence.DispatchSequence,
	})
	if err != nil || response.Dispatch == nil {
		if err != nil {
			return kernelruntime.BackendDispatchReceipt{}, err
		}
		return kernelruntime.BackendDispatchReceipt{}, errors.New("detached kernel dispatch receipt is unavailable")
	}
	return *response.Dispatch, nil
}

func (b *Backend) AcquireSessionControl(
	ctx context.Context,
	ref kernelruntime.BackendSessionRef,
	ttl time.Duration,
) (kernelruntime.BackendControlLease, error) {
	if ttl <= 0 || ttl > 24*time.Hour || ref.BackendID == "" || ref.BackendGeneration <= 0 {
		return kernelruntime.BackendControlLease{}, errors.New("detached kernel session control lease is invalid")
	}
	token, err := randomControlToken()
	if err != nil {
		return kernelruntime.BackendControlLease{}, err
	}
	_, lease, err := b.Store.AcquireKernelExecutionBackendControl(ctx, workspace.AcquireKernelExecutionBackendControlInput{
		BackendID: ref.BackendID, BackendGeneration: ref.BackendGeneration,
		Token: token, LeaseExpiresAt: time.Now().UTC().Add(ttl),
	})
	if err != nil {
		return kernelruntime.BackendControlLease{}, err
	}
	return kernelruntime.BackendControlLease{
		BackendID: lease.BackendID, BackendGeneration: lease.BackendGeneration,
		Epoch: lease.Epoch, Token: lease.Token, ExpiresAt: lease.ExpiresAt,
	}, nil
}

func (b *Backend) RenewControl(
	ctx context.Context,
	lease kernelruntime.BackendControlLease,
	ttl time.Duration,
) (kernelruntime.BackendControlLease, error) {
	if ttl <= 0 || ttl > 24*time.Hour {
		return kernelruntime.BackendControlLease{}, errors.New("detached kernel control renewal duration is invalid")
	}
	_, renewed, err := b.Store.RenewKernelExecutionBackendControl(ctx, workspace.RenewKernelExecutionBackendControlInput{
		BackendID: lease.BackendID, BackendGeneration: lease.BackendGeneration,
		ControllerEpoch: lease.Epoch, ControllerToken: lease.Token,
		LeaseExpiresAt: time.Now().UTC().Add(ttl),
	})
	if err != nil {
		return kernelruntime.BackendControlLease{}, err
	}
	return kernelruntime.BackendControlLease{
		BackendID: renewed.BackendID, BackendGeneration: renewed.BackendGeneration,
		Epoch: renewed.Epoch, Token: renewed.Token, ExpiresAt: renewed.ExpiresAt,
	}, nil
}

func (b *Backend) AcquireControl(
	ctx context.Context,
	ref kernelruntime.BackendExecutionRef,
	ttl time.Duration,
) (kernelruntime.BackendControlLease, kernelruntime.BackendExecutionSnapshot, error) {
	if ttl <= 0 || ttl > 24*time.Hour {
		return kernelruntime.BackendControlLease{}, kernelruntime.BackendExecutionSnapshot{},
			errors.New("detached kernel control lease duration is invalid")
	}
	if err := b.validateExecutionRef(ctx, ref); err != nil {
		return kernelruntime.BackendControlLease{}, kernelruntime.BackendExecutionSnapshot{}, err
	}
	token, err := randomControlToken()
	if err != nil {
		return kernelruntime.BackendControlLease{}, kernelruntime.BackendExecutionSnapshot{}, err
	}
	_, lease, err := b.Store.AcquireKernelExecutionBackendControl(ctx, workspace.AcquireKernelExecutionBackendControlInput{
		BackendID: ref.BackendID, BackendGeneration: ref.BackendGeneration,
		Token: token, LeaseExpiresAt: time.Now().UTC().Add(ttl),
	})
	if err != nil {
		return kernelruntime.BackendControlLease{}, kernelruntime.BackendExecutionSnapshot{}, err
	}
	control := kernelruntime.BackendControlLease{
		BackendID: lease.BackendID, BackendGeneration: lease.BackendGeneration,
		Epoch: lease.Epoch, Token: lease.Token, ExpiresAt: lease.ExpiresAt,
	}
	snapshot, err := b.Probe(ctx, ref, control)
	return control, snapshot, err
}

func (b *Backend) Probe(
	ctx context.Context,
	ref kernelruntime.BackendExecutionRef,
	lease kernelruntime.BackendControlLease,
) (kernelruntime.BackendExecutionSnapshot, error) {
	backend, found, err := b.Store.GetKernelExecutionBackend(ctx, ref.BackendID)
	if err != nil || !found {
		if err != nil {
			return kernelruntime.BackendExecutionSnapshot{}, err
		}
		return kernelruntime.BackendExecutionSnapshot{}, workspace.ErrKernelExecutionBackendStale
	}
	response, err := (Client{SocketPath: backend.SocketPath}).Call(ctx, CommandRequest{
		Version: ProtocolVersion, RequestID: "probe-" + uuid.NewString(), Command: CommandProbe,
		BackendID: ref.BackendID, BackendGeneration: ref.BackendGeneration,
		ExecutionID: ref.ExecutionID, ControllerEpoch: lease.Epoch, ControllerToken: lease.Token,
	})
	if err != nil || response.Snapshot == nil {
		if err != nil {
			return kernelruntime.BackendExecutionSnapshot{}, err
		}
		return kernelruntime.BackendExecutionSnapshot{}, errors.New("detached kernel execution snapshot is unavailable")
	}
	return *response.Snapshot, nil
}

func (b *Backend) Watch(
	ctx context.Context,
	ref kernelruntime.BackendExecutionRef,
	lease kernelruntime.BackendControlLease,
	after int64,
) (kernelruntime.ExecutionEventStream, error) {
	if after < 0 {
		return nil, errors.New("detached kernel observation cursor is invalid")
	}
	return &pollingEventStream{backend: b, ref: ref, lease: lease, after: after}, nil
}

func (b *Backend) Cancel(
	ctx context.Context,
	ref kernelruntime.BackendExecutionRef,
	lease kernelruntime.BackendControlLease,
	request kernelruntime.BackendCancelRequest,
) (kernelruntime.BackendCancelReceipt, error) {
	current, err := b.Store.RequestKernelExecutionCancel(ctx, workspace.RequestKernelExecutionCancelInput{
		KernelDetachedExecutionControlInput: workspace.KernelDetachedExecutionControlInput{
			ExecutionID: ref.ExecutionID, BackendGeneration: ref.BackendGeneration,
			ControllerEpoch: lease.Epoch, ControllerToken: lease.Token, ExpectedVersion: request.ExpectedVersion,
		},
		CancelRequestID: request.CancelRequestID, Reason: request.Reason,
	})
	if err != nil {
		return kernelruntime.BackendCancelReceipt{}, err
	}
	backend, found, err := b.Store.GetKernelExecutionBackend(ctx, ref.BackendID)
	if err != nil || !found {
		if err != nil {
			return kernelruntime.BackendCancelReceipt{}, err
		}
		return kernelruntime.BackendCancelReceipt{}, workspace.ErrKernelExecutionBackendStale
	}
	response, err := (Client{SocketPath: backend.SocketPath}).Call(ctx, CommandRequest{
		Version: ProtocolVersion, RequestID: "cancel-" + uuid.NewString(), Command: CommandCancel,
		BackendID: ref.BackendID, BackendGeneration: ref.BackendGeneration,
		ExecutionID: ref.ExecutionID, ExpectedVersion: current.StateVersion,
		ControllerEpoch: lease.Epoch, ControllerToken: lease.Token,
		CancelRequestID: request.CancelRequestID,
	})
	if err != nil || response.Cancel == nil {
		if err != nil {
			return kernelruntime.BackendCancelReceipt{}, err
		}
		return kernelruntime.BackendCancelReceipt{}, errors.New("detached kernel cancellation receipt is unavailable")
	}
	return *response.Cancel, nil
}

func (b *Backend) CloseSession(
	ctx context.Context,
	ref kernelruntime.BackendSessionRef,
	lease kernelruntime.BackendControlLease,
) error {
	if ctx == nil || b == nil || b.Store == nil {
		return errors.New("detached kernel backend is unavailable")
	}
	backend, found, err := b.Store.GetKernelExecutionBackend(ctx, ref.BackendID)
	if err != nil || !found {
		if err != nil {
			return err
		}
		return workspace.ErrKernelExecutionBackendStale
	}
	if backend.BackendGeneration != ref.BackendGeneration || backend.KernelID != ref.KernelID ||
		backend.KernelGeneration != ref.KernelGeneration {
		return workspace.ErrKernelExecutionBackendConflict
	}
	if backend.State == workspace.KernelExecutionBackendStateStopped {
		return nil
	}
	if backend.State == workspace.KernelExecutionBackendStateEvidenceLost {
		return errors.New("detached kernel session cleanup evidence is unavailable")
	}
	response, err := (Client{SocketPath: ref.SocketPath}).Call(ctx, CommandRequest{
		Version: ProtocolVersion, RequestID: "close-" + uuid.NewString(), Command: CommandClose,
		BackendID: ref.BackendID, BackendGeneration: ref.BackendGeneration,
		ControllerEpoch: lease.Epoch, ControllerToken: lease.Token,
	})
	if err == nil && !response.OK {
		return errors.New("detached kernel session did not close")
	}
	waitErr := b.waitForStopped(ctx, ref)
	if waitErr == nil {
		return nil
	}
	if err != nil {
		return errors.Join(err, waitErr)
	}
	return waitErr
}

// waitForStopped turns a successful close acknowledgement into proof that the
// executor and its kernel process tree have exited and the durable backend row
// reached a terminal state. This keeps task completion from racing ahead of
// cleanup while retaining idempotence when settlement itself is retried.
func (b *Backend) waitForStopped(ctx context.Context, ref kernelruntime.BackendSessionRef) error {
	waitCtx, cancel := context.WithTimeout(ctx, defaultBackendCloseTimeout)
	defer cancel()
	ticker := time.NewTicker(defaultBackendPollInterval)
	defer ticker.Stop()
	for {
		backend, found, err := b.Store.GetKernelExecutionBackend(waitCtx, ref.BackendID)
		if err != nil {
			return err
		}
		if !found || backend.BackendGeneration != ref.BackendGeneration {
			return workspace.ErrKernelExecutionBackendStale
		}
		switch backend.State {
		case workspace.KernelExecutionBackendStateStopped:
			return nil
		case workspace.KernelExecutionBackendStateEvidenceLost:
			return errors.New("detached kernel session stopped without cleanup evidence")
		}
		select {
		case <-waitCtx.Done():
			return errors.New("detached kernel session did not stop within the cleanup deadline")
		case <-ticker.C:
		}
	}
}

func (b *Backend) normalize() error {
	b.Executable = filepath.Clean(strings.TrimSpace(b.Executable))
	b.HomeDir = filepath.Clean(strings.TrimSpace(b.HomeDir))
	b.SocketRoot = filepath.Clean(strings.TrimSpace(b.SocketRoot))
	b.LogDir = filepath.Clean(strings.TrimSpace(b.LogDir))
	if !filepath.IsAbs(b.Executable) || !filepath.IsAbs(b.HomeDir) || !filepath.IsAbs(b.SocketRoot) ||
		!filepath.IsAbs(b.LogDir) {
		return errors.New("detached kernel backend paths must be absolute")
	}
	if b.StartTimeout <= 0 {
		b.StartTimeout = defaultBackendStartTimeout
	}
	if b.StartTimeout < 5*time.Second || b.StartTimeout > 5*time.Minute {
		return errors.New("detached kernel backend start timeout is invalid")
	}
	if b.Launcher == nil {
		return errors.New("detached kernel executor supervisor is not configured")
	}
	longestSocket := filepath.Join(b.SocketRoot, "kernel-backend-"+strings.Repeat("0", 36)+".sock")
	if len([]byte(longestSocket)) > maxUnixSocketPathBytes {
		return errors.New("detached kernel socket root exceeds the unix path budget")
	}
	for _, directory := range []string{b.SocketRoot, b.LogDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return err
		}
		if info, err := os.Stat(directory); err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
			return errors.New("detached kernel backend directory is not private")
		}
	}
	return nil
}

func (b *Backend) launchExecutor(backend workspace.KernelExecutionBackend) error {
	logPath := filepath.Join(b.LogDir, backend.BackendID+".log")
	workingDirectory, err := os.Getwd()
	if err != nil || !filepath.IsAbs(workingDirectory) {
		return errors.New("detached kernel executor working directory is unavailable")
	}
	args := []string{"kernel-executor", "--home", b.HomeDir,
		"--backend-id", backend.BackendID,
		"--backend-generation", strconv.FormatInt(backend.BackendGeneration, 10),
		"--executor-instance-id", backend.ExecutorInstanceID,
		"--socket", backend.SocketPath,
	}
	idleTimeout := b.IdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = defaultExecutorIdleTimeout
	}
	if idleTimeout < time.Second || idleTimeout > 24*time.Hour {
		return errors.New("detached kernel executor idle timeout is invalid")
	}
	args = append(args, "--idle-timeout", idleTimeout.String())
	if strings.TrimSpace(b.CondaHome) != "" {
		args = append(args, "--conda-home", strings.TrimSpace(b.CondaHome))
	}
	if strings.TrimSpace(b.CondaEnvsPath) != "" {
		args = append(args, "--conda-envs-path", strings.TrimSpace(b.CondaEnvsPath))
	}
	return b.Launcher.Launch(ExecutorLaunchRequest{
		BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
		Executable: b.Executable, Arguments: args, WorkingDirectory: workingDirectory, LogPath: logPath,
	})
}

func (b *Backend) waitForReady(
	ctx context.Context,
	backendID string,
	generation int64,
	timeout time.Duration,
) (workspace.KernelExecutionBackend, error) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(defaultBackendPollInterval)
	defer ticker.Stop()
	for {
		backend, found, err := b.Store.GetKernelExecutionBackend(waitCtx, backendID)
		if err != nil {
			return workspace.KernelExecutionBackend{}, err
		}
		if found && backend.BackendGeneration == generation && backend.State == workspace.KernelExecutionBackendStateReady &&
			validateTrustedUnixSocket(backend.SocketPath) == nil {
			return backend, nil
		}
		select {
		case <-waitCtx.Done():
			return workspace.KernelExecutionBackend{}, errors.New("detached kernel executor did not become ready")
		case <-ticker.C:
		}
	}
}

func (b *Backend) validateExecutionRef(ctx context.Context, ref kernelruntime.BackendExecutionRef) error {
	execution, found, err := b.Store.GetDetachedKernelExecution(ctx, ref.ExecutionID)
	if err != nil {
		return err
	}
	if !found || execution.OperationID != ref.OperationID || execution.BackendID != ref.BackendID ||
		execution.BackendGeneration != ref.BackendGeneration || execution.RequestSHA256 != ref.RequestSHA256 ||
		execution.ConfinementSHA256 != ref.ConfinementSHA256 {
		return workspace.ErrDetachedKernelExecutionConflict
	}
	return nil
}

type pollingEventStream struct {
	backend *Backend
	ref     kernelruntime.BackendExecutionRef
	lease   kernelruntime.BackendControlLease
	after   int64
	closed  bool
}

func (s *pollingEventStream) Recv(ctx context.Context) (kernelruntime.BackendExecutionEvent, error) {
	if s == nil || s.closed {
		return kernelruntime.BackendExecutionEvent{}, io.EOF
	}
	ticker := time.NewTicker(defaultBackendPollInterval)
	defer ticker.Stop()
	for {
		snapshot, err := s.backend.Probe(ctx, s.ref, s.lease)
		if err != nil {
			return kernelruntime.BackendExecutionEvent{}, err
		}
		if snapshot.LastObservationSequence > s.after || snapshot.StateVersion > s.after {
			sequence := snapshot.LastObservationSequence
			if sequence <= s.after {
				sequence = snapshot.StateVersion
			}
			data, _ := jsonMarshalSnapshot(snapshot)
			s.after = sequence
			return kernelruntime.BackendExecutionEvent{
				ExecutionID: s.ref.ExecutionID, Sequence: sequence, Type: snapshot.State,
				Data: data, ObservedAt: snapshot.ObservedAt,
			}, nil
		}
		select {
		case <-ctx.Done():
			return kernelruntime.BackendExecutionEvent{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *pollingEventStream) Close() error {
	if s != nil {
		s.closed = true
	}
	return nil
}

func durableSessionSpec(spec kernelruntime.SessionSpec) workspace.KernelExecutionSessionSpecV1 {
	mounts := make([]workspace.KernelExecutionMountSpecV1, 0, len(spec.Mounts))
	for _, mount := range spec.Mounts {
		mounts = append(mounts, workspace.KernelExecutionMountSpecV1{
			Path: mount.Path, Writable: mount.Writable,
			Trusted: mount.IsTrustedReadOnlyDirectory(),
		})
	}
	return workspace.KernelExecutionSessionSpecV1{
		Version: 1, KernelID: spec.KernelID, OwnerUserID: spec.OwnerID, ProjectID: spec.ProjectID,
		RootFrameID: spec.RootFrameID, RootFrameIncarnationID: spec.RootFrameIncarnationID,
		FrameID: spec.FrameID, FrameIncarnationID: spec.FrameIncarnationID,
		AgentName: spec.AgentName, DelegateName: spec.DelegateName, KernelKind: spec.KernelKind,
		Language: spec.Language, Environment: spec.Environment, RuntimeGeneration: spec.RuntimeGeneration,
		WorkspaceDir: spec.WorkspaceDir, Mounts: mounts, ProtectedPaths: append([]string(nil), spec.ProtectedPaths...),
		EgressAllowedDomains: append([]string(nil), spec.EgressAllowedDomains...),
		EgressDeniedDomains:  append([]string(nil), spec.EgressDeniedDomains...),
		CABundle:             spec.CABundle,
		UpstreamProxy:        spec.UpstreamProxy,
		Fresh:                spec.Fresh,
	}
}

func backendSessionRef(backend workspace.KernelExecutionBackend) kernelruntime.BackendSessionRef {
	return kernelruntime.BackendSessionRef{
		BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
		KernelID: backend.KernelID, KernelGeneration: backend.KernelGeneration,
		ExecutorInstanceID: backend.ExecutorInstanceID, SocketPath: backend.SocketPath,
	}
}

func machineBootID() (string, error) {
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	value := strings.TrimSpace(string(raw))
	if err != nil || value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("machine boot identity is unavailable")
	}
	return value, nil
}

func randomControlToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", errors.New("generate detached kernel control token")
	}
	return hex.EncodeToString(raw), nil
}

func jsonMarshalSnapshot(snapshot kernelruntime.BackendExecutionSnapshot) (string, error) {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"synon-go/internal/kernelcontract"
	transcriptstore "synon-go/internal/persistence/transcript"
	"synon-go/internal/software"
	"synon-go/internal/software/localconda"
)

func TestKernelExecutionBackendFencesAtomicDetachedAcceptance(t *testing.T) {
	ctx := context.Background()
	store, repo, claim := newKernelLocalOperationFixture(t)
	operation := createKernelLocalOperationForTest(t, store, repo, claim, "detached-api", "call-detached-api")

	approved, err := store.ResolveKernelLocalOperationApproval(ctx, ResolveKernelLocalOperationApprovalInput{
		OwnerUserID: "owner", OperationID: operation.OperationID, ExpectedStateVersion: 1,
		ApprovalRequestID: operation.ApprovalRequestID, Approved: true,
		DecisionID: "decision-detached-api", Scope: "once", Source: "user", ActorID: "owner",
		CurrentClaim: claim,
	})
	if err != nil || approved.State != KernelLocalOperationStateApproved || approved.StateVersion != 2 {
		t.Fatalf("approved=%#v err=%v", approved, err)
	}
	prepared, err := store.PrepareKernelLocalOperation(ctx, PrepareKernelLocalOperationInput{
		OwnerUserID: "owner", OperationID: operation.OperationID, ExpectedStateVersion: 2,
		Claim: claim, BootID: "service-boot-a", KernelID: "kernel-detached-api", KernelGeneration: 7,
		ConfinementSHA256: strings.Repeat("a", 64),
	})
	if err != nil || prepared.State != KernelLocalOperationStatePrepared || prepared.StateVersion != 3 {
		t.Fatalf("prepared=%#v err=%v", prepared, err)
	}

	createInput := CreateKernelExecutionBackendInput{
		BackendID: "backend-detached-api", OwnerUserID: prepared.OwnerUserID,
		ProjectID: prepared.ProjectID, RootFrameID: prepared.RootFrameID,
		RootFrameIncarnationID: prepared.RootFrameIncarnationID, FrameID: prepared.FrameID,
		FrameIncarnationID: prepared.FrameIncarnationID, KernelID: prepared.KernelID,
		KernelGeneration: prepared.KernelGeneration, ExecutorInstanceID: "executor-detached-api",
		MachineBootID: "machine-boot-a", SocketPath: "/run/user/1000/synon-biomed/backend-detached-api.sock",
		BackendGeneration: 1, SessionSpec: kernelExecutionSessionSpecForTest(prepared),
	}
	backend, err := store.CreateKernelExecutionBackend(ctx, createInput)
	if err != nil || backend.State != KernelExecutionBackendStateStarting || backend.StateVersion != 1 {
		t.Fatalf("backend=%#v err=%v", backend, err)
	}
	if replay, err := store.CreateKernelExecutionBackend(ctx, createInput); err != nil || replay.BackendID != backend.BackendID || replay.StateVersion != 1 {
		t.Fatalf("create replay=%#v err=%v", replay, err)
	}
	conflictInput := createInput
	conflictInput.SocketPath = "/run/user/1000/synon-biomed/conflict.sock"
	if _, err := store.CreateKernelExecutionBackend(ctx, conflictInput); !errors.Is(err, ErrKernelExecutionBackendConflict) {
		t.Fatalf("conflicting create error=%v", err)
	}

	activateInput := ActivateKernelExecutionBackendInput{
		BackendID: createInput.BackendID, BackendGeneration: createInput.BackendGeneration,
		ExecutorInstanceID: createInput.ExecutorInstanceID, ExecutorPID: 1101, ExecutorPIDStartTicks: 90101,
		WorkerPID: 1102, WorkerPIDStartTicks: 90102, WorkerPGID: 1102,
		CgroupPath: "/user.slice/user-1000.slice/synon-biomed-kernel.scope", HeartbeatSequence: 1,
	}
	backend, err = store.ActivateKernelExecutionBackend(ctx, activateInput)
	if err != nil || backend.State != KernelExecutionBackendStateReady || backend.StateVersion != 2 ||
		backend.HeartbeatAt == nil {
		t.Fatalf("activated=%#v err=%v", backend, err)
	}
	if replay, err := store.ActivateKernelExecutionBackend(ctx, activateInput); err != nil || replay.StateVersion != 2 {
		t.Fatalf("activate replay=%#v err=%v", replay, err)
	}
	conflictActivate := activateInput
	conflictActivate.CgroupPath += "/other"
	if _, err := store.ActivateKernelExecutionBackend(ctx, conflictActivate); !errors.Is(err, ErrKernelExecutionBackendStale) {
		t.Fatalf("conflicting activate error=%v", err)
	}

	leaseAInput := AcquireKernelExecutionBackendControlInput{
		BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
		Token: strings.Repeat("control-a-", 5), LeaseExpiresAt: time.Now().UTC().Add(time.Minute),
	}
	backend, leaseA, err := store.AcquireKernelExecutionBackendControl(ctx, leaseAInput)
	if err != nil || leaseA.Epoch != 1 || backend.ControllerEpoch != 1 {
		t.Fatalf("lease A=%#v backend=%#v err=%v", leaseA, backend, err)
	}
	leaseBInput := leaseAInput
	leaseBInput.Token = strings.Repeat("control-b-", 5)
	leaseBInput.LeaseExpiresAt = leaseAInput.LeaseExpiresAt.Add(time.Minute)
	backend, leaseB, err := store.AcquireKernelExecutionBackendControl(ctx, leaseBInput)
	if err != nil || leaseB.Epoch != 2 || backend.ControllerEpoch != 2 ||
		bytes.Equal(backend.ControllerTokenSHA256, []byte(leaseB.Token)) {
		t.Fatalf("lease B=%#v backend=%#v err=%v", leaseB, backend, err)
	}
	var persistedToken []byte
	if err := store.db.QueryRow(`SELECT controller_token_sha256 FROM kernel_execution_backends WHERE backend_id=?`,
		backend.BackendID).Scan(&persistedToken); err != nil || bytes.Contains(persistedToken, []byte("control-b")) {
		t.Fatalf("persisted controller authority=%x err=%v", persistedToken, err)
	}

	start := StartKernelLocalOperationInput{
		OwnerUserID: "owner", OperationID: prepared.OperationID, ExpectedStateVersion: prepared.StateVersion,
		Claim: claim, BootID: prepared.BootID, ExecutionID: "execution-detached-api",
	}
	if _, _, err := store.StartDetachedKernelLocalOperation(ctx, StartDetachedKernelLocalOperationInput{
		Start: start, BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
		ControllerEpoch: leaseA.Epoch, ControllerToken: leaseA.Token,
		Request: kernelDetachedExecutionRequestForTest(prepared, start.ExecutionID, createInput.SessionSpec),
	}); !errors.Is(err, ErrKernelExecutionBackendStale) {
		t.Fatalf("stale controller start error=%v", err)
	}
	rolledBack, found, err := store.GetKernelLocalOperation(ctx, "owner", prepared.OperationID)
	if err != nil || !found || rolledBack.State != KernelLocalOperationStatePrepared || rolledBack.ExecutionID != "" ||
		rolledBack.StateVersion != prepared.StateVersion {
		t.Fatalf("rolled back operation=%#v found=%t err=%v", rolledBack, found, err)
	}
	if _, found, err := store.GetDetachedKernelExecution(ctx, start.ExecutionID); err != nil || found {
		t.Fatalf("stale start execution found=%t err=%v", found, err)
	}
	request := kernelDetachedExecutionRequestForTest(prepared, start.ExecutionID, createInput.SessionSpec)
	tampered := request
	tampered.Code = "print('different code')"
	if _, _, err := store.StartDetachedKernelLocalOperation(ctx, StartDetachedKernelLocalOperationInput{
		Start: start, BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
		ControllerEpoch: leaseB.Epoch, ControllerToken: leaseB.Token, Request: tampered,
	}); !errors.Is(err, ErrDetachedKernelExecutionConflict) {
		t.Fatalf("tampered request start error=%v", err)
	}
	if rolledBack, found, err := store.GetKernelLocalOperation(ctx, "owner", prepared.OperationID); err != nil ||
		!found || rolledBack.State != KernelLocalOperationStatePrepared || rolledBack.StateVersion != prepared.StateVersion {
		t.Fatalf("tampered start rollback=%#v found=%t err=%v", rolledBack, found, err)
	}

	started, execution, err := store.StartDetachedKernelLocalOperation(ctx, StartDetachedKernelLocalOperationInput{
		Start: start, BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
		ControllerEpoch: leaseB.Epoch, ControllerToken: leaseB.Token,
		Request: request,
	})
	if err != nil || started.State != KernelLocalOperationStateStarted || started.StateVersion != 4 ||
		execution.State != DetachedKernelExecutionStateAccepted || execution.OperationID != started.OperationID ||
		execution.ExecutionID != started.ExecutionID || execution.RequestJSON == "" || execution.RequestSHA256 == "" ||
		execution.ConfinementSHA256 != started.ConfinementSHA256 {
		t.Fatalf("started=%#v execution=%#v err=%v", started, execution, err)
	}
	if active, err := store.HasActiveDetachedKernelExecutionForFrame(ctx, started.FrameID); err != nil || !active {
		t.Fatalf("active detached frame=%t err=%v", active, err)
	}
	if active, err := store.HasActiveDetachedKernelExecutionForFrame(ctx, "unrelated-frame"); err != nil || active {
		t.Fatalf("unrelated detached frame=%t err=%v", active, err)
	}
	recovery, more, err := store.ListKernelLocalOperationRecoveryCandidates(ctx, "service-boot-b", 10)
	if err != nil || more || len(recovery) != 0 {
		t.Fatalf("detached execution was claimed by legacy recovery: candidates=%#v more=%t err=%v", recovery, more, err)
	}
	if replayOperation, replayExecution, err := store.StartDetachedKernelLocalOperation(ctx, StartDetachedKernelLocalOperationInput{
		Start: start, BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
		ControllerEpoch: leaseB.Epoch, ControllerToken: leaseB.Token,
		Request: kernelDetachedExecutionRequestForTest(prepared, start.ExecutionID, createInput.SessionSpec),
	}); err != nil || replayOperation.StateVersion != started.StateVersion || replayExecution.ExecutionID != execution.ExecutionID ||
		replayExecution.StateVersion != execution.StateVersion {
		t.Fatalf("start replay operation=%#v execution=%#v err=%v", replayOperation, replayExecution, err)
	}
	if _, _, err := store.CancelFrameWithTranscript(ctx, started.FrameID); err != nil {
		t.Fatal(err)
	}
	cancelled, found, err := store.GetDetachedKernelExecution(ctx, execution.ExecutionID)
	if err != nil || !found || cancelled.State != DetachedKernelExecutionStateCancelRequested ||
		cancelled.CancelRequestID != "frame-cancel:"+execution.ExecutionID {
		t.Fatalf("frame-cancelled detached execution=%#v found=%t err=%v", cancelled, found, err)
	}
	decoded, err := DecodeKernelDetachedExecutionRequestV1(execution.RequestJSON)
	if err != nil || decoded.Code != request.Code || decoded.ExecutionID != execution.ExecutionID ||
		execution.RequestSHA256 != sha256HexString(execution.RequestJSON) {
		t.Fatalf("decoded request=%#v err=%v", decoded, err)
	}
}

func TestCanonicalKernelDetachedExecutionRequestBindsBashCommand(t *testing.T) {
	inputJSON, _, err := kernelcontract.CanonicalInput(kernelcontract.BashTool, []byte(`{
		"command":"python workflow.py --output results.csv",
		"environment":"science",
		"background":true,
		"human_description":"运行受管工作流"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	operation := KernelLocalOperation{
		OperationID: "operation-bash-detached", ExecutionID: "execution-bash-detached",
		OwnerUserID: "owner", ProjectID: "project", RootFrameID: "root-frame",
		RootFrameIncarnationID: "root-incarnation", FrameID: "frame", FrameIncarnationID: "frame-incarnation",
		KernelID: "kernel-bash-detached", KernelGeneration: 3, ToolCallID: "call-bash-detached",
		Tool: kernelcontract.BashTool, Environment: "science", InputJSON: inputJSON,
		State: KernelLocalOperationStateStarted,
	}
	session := KernelExecutionSessionSpecV1{
		Version: 1, KernelID: operation.KernelID, OwnerUserID: operation.OwnerUserID,
		ProjectID: operation.ProjectID, RootFrameID: operation.RootFrameID,
		RootFrameIncarnationID: operation.RootFrameIncarnationID, FrameID: operation.FrameID,
		FrameIncarnationID: operation.FrameIncarnationID, AgentName: "OPERON",
		KernelKind: "bash", Language: "python", Environment: operation.Environment,
		WorkspaceDir: "/tmp/synon-biomed-detached-bash", Mounts: []KernelExecutionMountSpecV1{},
		ProtectedPaths: []string{},
	}
	_, sessionJSON, sessionSHA, err := canonicalKernelExecutionSessionSpec(session)
	if err != nil {
		t.Fatal(err)
	}
	backend := KernelExecutionBackend{SessionSpecJSON: sessionJSON, SessionSpecSHA256: sessionSHA}
	bashWrapper, err := kernelcontract.BashPythonWrapper("python workflow.py --output results.csv")
	if err != nil {
		t.Fatal(err)
	}
	request := KernelDetachedExecutionRequestV1{
		Version: 1, OperationID: operation.OperationID, ExecutionID: operation.ExecutionID,
		OwnerUserID: operation.OwnerUserID, ProjectID: operation.ProjectID,
		RootFrameID: operation.RootFrameID, RootFrameIncarnationID: operation.RootFrameIncarnationID,
		FrameID: operation.FrameID, FrameIncarnationID: operation.FrameIncarnationID,
		KernelID: operation.KernelID, KernelGeneration: operation.KernelGeneration,
		ToolCallID: operation.ToolCallID, ToolName: operation.Tool, Language: session.Language,
		KernelKind: session.KernelKind, Environment: operation.Environment,
		Code: bashWrapper, Background: true, Origin: "agent",
		OutputLimitBytes: 1 << 20,
	}
	normalized, _, _, err := canonicalKernelDetachedExecutionRequest(operation, backend, request)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Code != request.Code || !normalized.Background {
		t.Fatalf("normalized request = %#v", normalized)
	}

	tampered := request
	tampered.Code, err = kernelcontract.BashPythonWrapper("python other.py")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := canonicalKernelDetachedExecutionRequest(operation, backend, tampered); !errors.Is(err, ErrDetachedKernelExecutionConflict) {
		t.Fatalf("tampered bash request error = %v", err)
	}
}

func TestCanonicalKernelDetachedExecutionRequestResolvesRelativeWorkingDir(t *testing.T) {
	inputJSON, _, err := kernelcontract.CanonicalInput(kernelcontract.PythonTool, []byte(`{
		"code":"print('workspace-bound')",
		"environment":"science",
		"working_dir":".",
		"human_description":"运行任务脚本"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	operation := KernelLocalOperation{
		OperationID: "operation-python-relative-cwd", ExecutionID: "execution-python-relative-cwd",
		OwnerUserID: "owner", ProjectID: "project", RootFrameID: "root-frame",
		RootFrameIncarnationID: "root-incarnation", FrameID: "frame", FrameIncarnationID: "frame-incarnation",
		KernelID: "kernel-python-relative-cwd", KernelGeneration: 3, ToolCallID: "call-python-relative-cwd",
		Tool: kernelcontract.PythonTool, Environment: "science", InputJSON: inputJSON,
		State: KernelLocalOperationStateStarted,
	}
	const workspaceDir = "/tmp/synon-biomed-relative-cwd"
	session := KernelExecutionSessionSpecV1{
		Version: 1, KernelID: operation.KernelID, OwnerUserID: operation.OwnerUserID,
		ProjectID: operation.ProjectID, RootFrameID: operation.RootFrameID,
		RootFrameIncarnationID: operation.RootFrameIncarnationID, FrameID: operation.FrameID,
		FrameIncarnationID: operation.FrameIncarnationID, AgentName: "OPERON",
		KernelKind: "python", Language: "python", Environment: operation.Environment,
		WorkspaceDir: workspaceDir, Mounts: []KernelExecutionMountSpecV1{}, ProtectedPaths: []string{},
	}
	_, sessionJSON, sessionSHA, err := canonicalKernelExecutionSessionSpec(session)
	if err != nil {
		t.Fatal(err)
	}
	backend := KernelExecutionBackend{SessionSpecJSON: sessionJSON, SessionSpecSHA256: sessionSHA}
	request := kernelDetachedExecutionRequestForTest(operation, operation.ExecutionID, session)
	request.WorkingDir = workspaceDir
	if _, _, _, err := canonicalKernelDetachedExecutionRequest(operation, backend, request); err != nil {
		t.Fatalf("relative cwd request error=%v", err)
	}
	tampered := request
	tampered.WorkingDir = "/tmp/other-workspace"
	if _, _, _, err := canonicalKernelDetachedExecutionRequest(operation, backend, tampered); !errors.Is(err, ErrDetachedKernelExecutionConflict) {
		t.Fatalf("tampered cwd request error=%v", err)
	}
}

func TestSoftwareRuntimeDetachedAcceptanceBindsGeneratedLauncherToDurableRequest(t *testing.T) {
	ctx := context.Background()
	store, repo, claim := newKernelLocalOperationFixture(t)
	rawRequest := map[string]any{
		"capability": "launcher-authority", "language": "python", "executable": "python",
		"args": []any{"-c", "print('bound')"}, "timeout_seconds": 45,
		"working_dir": "/tmp/model-guessed/workspace/frame-detached",
	}
	payload, err := json.Marshal(map[string]any{
		"status": "running", "modelToolCalls": []any{map[string]any{
			"id": "call-software-detached", "type": "function", "name": "software_runtime",
			"arguments": rawRequest,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var operations []KernelLocalOperation
	_, _, _, err = repo.AppendRunnerCheckpoint(ctx, transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "software-detached-batch", Phase: transcriptstore.RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: payload,
		CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			batch, _, _, createErr := store.CreateToolCallBatchForCheckpointTx(ctx, tx, event)
			if createErr != nil {
				return transcriptstore.RunnerCheckpointCommitReceipt{}, createErr
			}
			operations, createErr = store.CreateKernelLocalOperationsForCheckpointTx(ctx, tx, event)
			return kernelLocalOperationCommitReceiptWithBatch(batch, operations), createErr
		},
	})
	if err != nil || len(operations) != 1 {
		t.Fatalf("operations=%#v err=%v", operations, err)
	}
	operation := operations[0]
	approved, err := store.ResolveKernelLocalOperationApproval(ctx, ResolveKernelLocalOperationApprovalInput{
		OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
		ExpectedStateVersion: operation.StateVersion, ApprovalRequestID: operation.ApprovalRequestID,
		Approved: true, DecisionID: "decision-software-detached", Scope: "once",
		Source: "user", ActorID: operation.OwnerUserID, CurrentClaim: claim,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.PrepareKernelLocalOperation(ctx, PrepareKernelLocalOperationInput{
		OwnerUserID: approved.OwnerUserID, OperationID: approved.OperationID,
		ExpectedStateVersion: approved.StateVersion, Claim: claim,
		BootID: "software-detached-boot", KernelID: "kernel-software-detached", KernelGeneration: 1,
		ConfinementSHA256: strings.Repeat("e", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionSpec := kernelExecutionSessionSpecForTest(prepared)
	backend, err := store.CreateKernelExecutionBackend(ctx, CreateKernelExecutionBackendInput{
		BackendID: "backend-software-detached", OwnerUserID: prepared.OwnerUserID,
		ProjectID: prepared.ProjectID, RootFrameID: prepared.RootFrameID,
		RootFrameIncarnationID: prepared.RootFrameIncarnationID, FrameID: prepared.FrameID,
		FrameIncarnationID: prepared.FrameIncarnationID, KernelID: prepared.KernelID,
		KernelGeneration: prepared.KernelGeneration, ExecutorInstanceID: "executor-software-detached",
		MachineBootID: "machine-software-detached", SocketPath: "/run/user/1000/synon-biomed/backend-software-detached.sock",
		BackendGeneration: 1, SessionSpec: sessionSpec,
	})
	if err != nil {
		t.Fatal(err)
	}
	backend, err = store.ActivateKernelExecutionBackend(ctx, ActivateKernelExecutionBackendInput{
		BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
		ExecutorInstanceID: backend.ExecutorInstanceID, ExecutorPID: 3101, ExecutorPIDStartTicks: 90301,
		WorkerPID: 3102, WorkerPIDStartTicks: 90302, WorkerPGID: 3102,
		CgroupPath: "/user.slice/user-1000.slice/synon-biomed-software-detached.scope", HeartbeatSequence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	backend, lease, err := store.AcquireKernelExecutionBackendControl(ctx, AcquireKernelExecutionBackendControlInput{
		BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
		Token: strings.Repeat("software-detached-token-", 2), LeaseExpiresAt: time.Now().UTC().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := software.DecodeRequestJSON(prepared.InputJSON)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := software.RequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	harness, err := localconda.BuildPythonHarness(software.Plan{
		ProviderID: software.LocalProviderID, Request: request, RequestDigest: digest, Environment: prepared.Environment,
	}, software.ProvisionReceipt{
		ProviderID: software.LocalProviderID, Environment: prepared.Environment, Generation: "generation-software-detached",
		Executable: request.Executable, Local: true, Verified: true, Preflight: true,
		Disposition: software.ProvisionDispositionReused,
	})
	if err != nil {
		t.Fatal(err)
	}
	start := StartKernelLocalOperationInput{
		OwnerUserID: prepared.OwnerUserID, OperationID: prepared.OperationID,
		ExpectedStateVersion: prepared.StateVersion, Claim: claim,
		BootID: prepared.BootID, ExecutionID: "execution-software-detached",
	}
	detachedRequest := kernelDetachedExecutionRequestForTest(prepared, start.ExecutionID, sessionSpec)
	detachedRequest.Code = harness
	// The server maps a nonexistent, narrowly recognized legacy workspace alias
	// to the durable session workspace before any user code starts. Detached
	// persistence must bind that authorized effective cwd without treating the
	// already-admitted model request as a competing execution.
	detachedRequest.WorkingDir = sessionSpec.WorkspaceDir
	detachedRequest.Background = request.Background
	detachedRequest.TimeoutMillis = (time.Duration(request.TimeoutSeconds)*time.Second + 30*time.Second).Milliseconds()
	tampered := detachedRequest
	tampered.Code += "\nprint('tampered')"
	if _, _, err := store.StartDetachedKernelLocalOperation(ctx, StartDetachedKernelLocalOperationInput{
		Start: start, BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
		ControllerEpoch: lease.Epoch, ControllerToken: lease.Token, Request: tampered,
	}); !errors.Is(err, ErrDetachedKernelExecutionConflict) {
		t.Fatalf("tampered software launcher error=%v", err)
	}
	started, execution, err := store.StartDetachedKernelLocalOperation(ctx, StartDetachedKernelLocalOperationInput{
		Start: start, BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
		ControllerEpoch: lease.Epoch, ControllerToken: lease.Token, Request: detachedRequest,
	})
	if err != nil || started.State != KernelLocalOperationStateStarted ||
		execution.State != DetachedKernelExecutionStateAccepted || execution.RequestSHA256 == "" {
		t.Fatalf("started=%#v execution=%#v err=%v", started, execution, err)
	}
}

func TestKernelProtocolRecognizesForegroundWaitDetachedAuthority(t *testing.T) {
	store, _, _, execution := newAcceptedDetachedKernelExecutionFixture(t)
	operation, found, err := store.GetKernelLocalOperation(context.Background(), "owner", execution.OperationID)
	if err != nil || !found {
		t.Fatalf("operation=%#v found=%t err=%v", operation, found, err)
	}
	var authorized bool
	repo, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = repo.RunImmediate(context.Background(), func(tx *transcriptstore.ImmediateTransaction) error {
		var checkErr error
		authorized, checkErr = kernelLocalOperationProtocolBackgroundStarted(context.Background(), tx, operation)
		return checkErr
	})
	if err != nil || !authorized {
		t.Fatalf("foreground-detached authority=%t err=%v", authorized, err)
	}
	operation.ExecutionID = "missing-detached-execution"
	err = repo.RunImmediate(context.Background(), func(tx *transcriptstore.ImmediateTransaction) error {
		var checkErr error
		authorized, checkErr = kernelLocalOperationProtocolBackgroundStarted(context.Background(), tx, operation)
		return checkErr
	})
	if err != nil || authorized {
		t.Fatalf("missing detached authority=%t err=%v", authorized, err)
	}
}

func TestKernelDetachedExecutionLifecyclePersistsFencedFacts(t *testing.T) {
	ctx := context.Background()
	store, backend, lease, execution := newAcceptedDetachedKernelExecutionFixture(t)

	heartbeat, err := store.HeartbeatKernelExecutionBackend(ctx, HeartbeatKernelExecutionBackendInput{
		BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
		ExecutorInstanceID: backend.ExecutorInstanceID, ExpectedHeartbeatSequence: 2,
	})
	if err != nil || heartbeat.HeartbeatSequence != 2 || heartbeat.HeartbeatAt == nil {
		t.Fatalf("heartbeat=%#v err=%v", heartbeat, err)
	}
	if replay, err := store.HeartbeatKernelExecutionBackend(ctx, HeartbeatKernelExecutionBackendInput{
		BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
		ExecutorInstanceID: backend.ExecutorInstanceID, ExpectedHeartbeatSequence: 2,
	}); err != nil || replay.StateVersion != heartbeat.StateVersion {
		t.Fatalf("heartbeat replay=%#v err=%v", replay, err)
	}
	if _, err := store.HeartbeatKernelExecutionBackend(ctx, HeartbeatKernelExecutionBackendInput{
		BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
		ExecutorInstanceID: backend.ExecutorInstanceID, ExpectedHeartbeatSequence: 4,
	}); !errors.Is(err, ErrKernelExecutionBackendStale) {
		t.Fatalf("skipped heartbeat error=%v", err)
	}

	renewedExpiry := lease.ExpiresAt.Add(time.Minute)
	backend, renewed, err := store.RenewKernelExecutionBackendControl(ctx, RenewKernelExecutionBackendControlInput{
		BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
		ControllerEpoch: lease.Epoch, ControllerToken: lease.Token, LeaseExpiresAt: renewedExpiry,
	})
	if err != nil || !renewed.ExpiresAt.Equal(renewedExpiry) || backend.ControllerEpoch != lease.Epoch {
		t.Fatalf("renewed=%#v backend=%#v err=%v", renewed, backend, err)
	}
	lease = renewed
	if _, _, err := store.RenewKernelExecutionBackendControl(ctx, RenewKernelExecutionBackendControlInput{
		BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
		ControllerEpoch: lease.Epoch, ControllerToken: lease.Token, LeaseExpiresAt: renewedExpiry.Add(-time.Second),
	}); !errors.Is(err, ErrKernelExecutionBackendStale) {
		t.Fatalf("shortened renewal error=%v", err)
	}

	control := KernelDetachedExecutionControlInput{
		ExecutionID: execution.ExecutionID, BackendGeneration: backend.BackendGeneration,
		ControllerEpoch: lease.Epoch, ControllerToken: lease.Token, ExpectedVersion: execution.StateVersion,
	}
	execution, err = store.CommitKernelExecutionDispatch(ctx, CommitKernelExecutionDispatchInput{
		KernelDetachedExecutionControlInput: control, DispatchSequence: 1,
	})
	if err != nil || execution.State != DetachedKernelExecutionStateDispatchCommitted ||
		execution.StateVersion != 2 || execution.DispatchSequence != 1 || execution.DispatchCommittedAt == nil {
		t.Fatalf("dispatch=%#v err=%v", execution, err)
	}
	if replay, err := store.CommitKernelExecutionDispatch(ctx, CommitKernelExecutionDispatchInput{
		KernelDetachedExecutionControlInput: control, DispatchSequence: 1,
	}); err != nil || replay.StateVersion != execution.StateVersion {
		t.Fatalf("dispatch replay=%#v err=%v", replay, err)
	}
	control.ExpectedVersion = execution.StateVersion
	execution, err = store.MarkKernelExecutionRequestWritten(ctx, MarkKernelExecutionRequestWrittenInput{
		KernelDetachedExecutionControlInput: control, DispatchSequence: 1,
	})
	if err != nil || execution.StateVersion != 3 || execution.RequestWrittenAt == nil {
		t.Fatalf("request written=%#v err=%v", execution, err)
	}
	execution, err = store.MarkKernelExecutionStarted(ctx, MarkKernelExecutionStartedInput{
		ExecutionID: execution.ExecutionID, BackendGeneration: backend.BackendGeneration,
		ExecutorInstanceID: backend.ExecutorInstanceID, ExpectedVersion: execution.StateVersion,
		ObservationSequence: 1,
	})
	if err != nil || execution.State != DetachedKernelExecutionStateStarted || execution.StateVersion != 4 ||
		execution.WorkerStartedAt == nil || execution.LastObservationSequence != 1 {
		t.Fatalf("worker started=%#v err=%v", execution, err)
	}
	control.ExpectedVersion = execution.StateVersion
	execution, err = store.RequestKernelExecutionCancel(ctx, RequestKernelExecutionCancelInput{
		KernelDetachedExecutionControlInput: control, CancelRequestID: "cancel-detached-lifecycle",
	})
	if err != nil || execution.State != DetachedKernelExecutionStateCancelRequested || execution.StateVersion != 5 ||
		execution.CancelRequestedAt == nil {
		t.Fatalf("cancel requested=%#v err=%v", execution, err)
	}
	execution, err = store.AcknowledgeKernelExecutionCancel(ctx, AcknowledgeKernelExecutionCancelInput{
		ExecutionID: execution.ExecutionID, BackendGeneration: backend.BackendGeneration,
		ExecutorInstanceID: backend.ExecutorInstanceID, ExpectedVersion: execution.StateVersion,
		CancelRequestID: execution.CancelRequestID, AckSequence: 2, Signal: "sigint",
	})
	if err != nil || execution.StateVersion != 6 || execution.CancelAckAt == nil ||
		execution.CancelAckSequence != 2 || execution.CancelSignal != "sigint" {
		t.Fatalf("cancel acknowledged=%#v err=%v", execution, err)
	}
	startedOperation, found, err := store.GetKernelLocalOperation(ctx, "owner", execution.OperationID)
	if err != nil || !found || startedOperation.State != KernelLocalOperationStateStarted {
		t.Fatalf("started operation before terminal receipt=%#v found=%t err=%v", startedOperation, found, err)
	}
	if candidates, more, err := store.ListDetachedKernelExecutionRecoveryCandidates(ctx, startedOperation.BootID, 10); err != nil ||
		more || len(candidates) != 0 {
		t.Fatalf("same-boot nonterminal recovery candidates=%#v more=%t err=%v", candidates, more, err)
	}

	resultJSON := `{"ok":false,"error":"cancelled"}`
	resultDigest := sha256.Sum256([]byte(resultJSON))
	finishedAt := time.Now().UTC()
	resultInput := CommitKernelExecutionResultInput{
		ReceiptID: "receipt-detached-lifecycle", ExecutionID: execution.ExecutionID,
		BackendGeneration: backend.BackendGeneration, ExecutorInstanceID: backend.ExecutorInstanceID,
		TerminalSequence: 3, Outcome: KernelExecutionResultCancelled, ResultJSON: resultJSON,
		ResultSHA256: hex.EncodeToString(resultDigest[:]), Interrupted: true,
		TerminationSignal: "SIGINT", StartedAt: finishedAt.Add(-time.Second), FinishedAt: finishedAt,
		FilesWrittenJSON: `[]`, DroppedRootsJSON: `[]`,
	}
	resultInput.ResultRef = "kernel-spool-sha256:" + strings.Repeat("f", 64)
	wake := store.KernelRetentionWake()
	execution, receipt, err := store.CommitKernelExecutionResult(ctx, resultInput)
	if err != nil || execution.State != DetachedKernelExecutionStateTerminal || execution.StateVersion != 7 ||
		execution.TerminalReceiptID != resultInput.ReceiptID || receipt.ReceiptID != resultInput.ReceiptID ||
		receipt.Outcome != KernelExecutionResultCancelled || !receipt.Interrupted ||
		receipt.ResultRef != resultInput.ResultRef {
		t.Fatalf("terminal execution=%#v receipt=%#v err=%v", execution, receipt, err)
	}
	select {
	case <-wake:
	default:
		t.Fatal("terminal detached result did not wake settlement recovery")
	}
	if replayExecution, replayReceipt, err := store.CommitKernelExecutionResult(ctx, resultInput); err != nil ||
		replayExecution.StateVersion != execution.StateVersion || replayReceipt.ResultSHA256 != receipt.ResultSHA256 {
		t.Fatalf("terminal replay execution=%#v receipt=%#v err=%v", replayExecution, replayReceipt, err)
	}
	conflicting := resultInput
	conflicting.Outcome = KernelExecutionResultFailed
	if _, _, err := store.CommitKernelExecutionResult(ctx, conflicting); !errors.Is(err, ErrDetachedKernelExecutionConflict) {
		t.Fatalf("conflicting terminal error=%v", err)
	}
	loaded, found, err := store.GetKernelExecutionResultReceipt(ctx, receipt.ReceiptID)
	if err != nil || !found || !kernelExecutionResultReceiptMatches(loaded, resultInput) {
		t.Fatalf("loaded receipt=%#v found=%t err=%v", loaded, found, err)
	}
	operation, found, err := store.GetKernelLocalOperation(ctx, "owner", execution.OperationID)
	if err != nil || !found || operation.State != KernelLocalOperationStateStarted {
		t.Fatalf("started operation=%#v found=%t err=%v", operation, found, err)
	}
	if candidates, more, err := store.ListDetachedKernelExecutionRecoveryCandidates(ctx, operation.BootID, 10); err != nil ||
		more || len(candidates) != 1 || candidates[0].Execution.ExecutionID != execution.ExecutionID {
		t.Fatalf("same-boot detached recovery candidates=%#v more=%t err=%v", candidates, more, err)
	}
	candidates, more, err := store.ListDetachedKernelExecutionRecoveryCandidates(ctx, "service-boot-restarted", 10)
	if err != nil || more || len(candidates) != 1 ||
		candidates[0].Execution.ExecutionID != execution.ExecutionID {
		t.Fatalf("detached recovery candidates=%#v more=%t err=%v", candidates, more, err)
	}
	finishInput := FinishKernelLocalOperationInput{
		OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
		ExpectedStateVersion: operation.StateVersion, BootID: operation.BootID,
		ExecutionID: operation.ExecutionID, TerminalState: KernelLocalOperationStateCancelled,
		ReasonCode: "execution_cancelled", TerminalResultJSON: json.RawMessage(`{"ok":false,"cancelled":true}`),
		ExecutionLog: SaveExecutionLogInput{
			Record: ExecutionLogRecord{
				ID: operation.ExecutionID, FrameID: operation.FrameID, KernelID: operation.KernelID,
				KernelKind: "operon", CondaEnv: operation.Environment, Language: "python",
				Source: "print('detached')", ExitStatus: "cancelled", Origin: "agent",
				ExecutedAt: receipt.StartedAt,
			},
			ExpectedOwnerID: operation.OwnerUserID, ExpectedProjectID: operation.ProjectID,
			ExpectedFrameIncarnationID:     operation.FrameIncarnationID,
			ExpectedRootFrameIncarnationID: operation.RootFrameIncarnationID,
		},
	}
	settled, err := store.FinishDetachedKernelLocalOperation(ctx, finishInput, receipt.ReceiptID)
	if err != nil || !settled.Created || settled.Operation.State != KernelLocalOperationStateCancelled {
		t.Fatalf("detached settlement=%#v err=%v", settled, err)
	}
	if replay, err := store.FinishDetachedKernelLocalOperation(ctx, finishInput, receipt.ReceiptID); err != nil ||
		replay.Created || replay.Operation.StateVersion != settled.Operation.StateVersion {
		t.Fatalf("detached settlement replay=%#v err=%v", replay, err)
	}
	if candidates, more, err := store.ListDetachedKernelExecutionRecoveryCandidates(ctx, "service-boot-restarted", 10); err != nil ||
		more || len(candidates) != 0 {
		t.Fatalf("settled detached recovery candidates=%#v more=%t err=%v", candidates, more, err)
	}
}

func TestKernelExecutionHostCallUsesDurableClaimAndExactReceipt(t *testing.T) {
	ctx := context.Background()
	store, backend, lease, execution := newAcceptedDetachedKernelExecutionFixture(t)
	control := KernelDetachedExecutionControlInput{
		ExecutionID: execution.ExecutionID, BackendGeneration: backend.BackendGeneration,
		ControllerEpoch: lease.Epoch, ControllerToken: lease.Token, ExpectedVersion: execution.StateVersion,
	}
	execution, err := store.CommitKernelExecutionDispatch(ctx, CommitKernelExecutionDispatchInput{
		KernelDetachedExecutionControlInput: control, DispatchSequence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	control.ExpectedVersion = execution.StateVersion
	execution, err = store.MarkKernelExecutionRequestWritten(ctx, MarkKernelExecutionRequestWrittenInput{
		KernelDetachedExecutionControlInput: control, DispatchSequence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	execution, err = store.MarkKernelExecutionStarted(ctx, MarkKernelExecutionStartedInput{
		ExecutionID: execution.ExecutionID, BackendGeneration: backend.BackendGeneration,
		ExecutorInstanceID: backend.ExecutorInstanceID, ExpectedVersion: execution.StateVersion,
		ObservationSequence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	hostCallID := "hc-0123456789abcdef0123456789abcdef"
	call, err := store.CreateKernelExecutionHostCall(ctx, CreateKernelExecutionHostCallInput{
		ExecutionID: execution.ExecutionID, BackendGeneration: backend.BackendGeneration,
		ExecutorInstanceID: backend.ExecutorInstanceID, Ordinal: 0,
		Request: KernelExecutionHostCallRequestV1{
			Version: 1, HostCallID: hostCallID, CellID: execution.ExecutionID,
			Method: "artifact.read", Args: []any{"artifact-a"}, Kwargs: map[string]any{"version_id": "version-a"},
		},
	})
	if err != nil || call.State != KernelExecutionHostCallPending || call.StateVersion != 1 ||
		call.RequestSHA256 != sha256HexString(call.RequestJSON) {
		t.Fatalf("pending host call=%#v err=%v", call, err)
	}
	if replay, err := store.CreateKernelExecutionHostCall(ctx, CreateKernelExecutionHostCallInput{
		ExecutionID: execution.ExecutionID, BackendGeneration: backend.BackendGeneration,
		ExecutorInstanceID: backend.ExecutorInstanceID, Ordinal: 0,
		Request: KernelExecutionHostCallRequestV1{
			Version: 1, HostCallID: hostCallID, CellID: execution.ExecutionID,
			Method: "artifact.read", Args: []any{"artifact-a"}, Kwargs: map[string]any{"version_id": "version-a"},
		},
	}); err != nil || replay.StateVersion != call.StateVersion {
		t.Fatalf("host call replay=%#v err=%v", replay, err)
	}
	claimToken := strings.Repeat("host-call-claim-", 3)
	claimExpiry := time.Now().UTC().Add(time.Minute)
	call, err = store.ClaimKernelExecutionHostCall(ctx, ClaimKernelExecutionHostCallInput{
		ExecutionID: call.ExecutionID, HostCallID: call.HostCallID,
		BackendGeneration: backend.BackendGeneration, ControllerEpoch: lease.Epoch,
		ControllerToken: lease.Token, ExpectedVersion: call.StateVersion,
		ClaimToken: claimToken, ClaimExpiresAt: claimExpiry,
	})
	if err != nil || call.State != KernelExecutionHostCallExecuting || call.StateVersion != 2 ||
		call.ClaimEpoch != lease.Epoch || call.ClaimExpiresAt == nil || !call.ClaimExpiresAt.Equal(claimExpiry) {
		t.Fatalf("claimed host call=%#v err=%v", call, err)
	}
	if bytes.Contains(call.ClaimTokenSHA256, []byte("host-call-claim")) {
		t.Fatalf("claim token was stored in plaintext: %x", call.ClaimTokenSHA256)
	}
	backend, replacementLease, err := store.AcquireKernelExecutionBackendControl(ctx,
		AcquireKernelExecutionBackendControlInput{
			BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
			Token: strings.Repeat("replacement-controller-", 2), LeaseExpiresAt: time.Now().UTC().Add(2 * time.Minute),
		})
	if err != nil || replacementLease.Epoch != lease.Epoch+1 {
		t.Fatalf("replacement lease=%#v backend=%#v err=%v", replacementLease, backend, err)
	}
	if _, err := store.ClaimKernelExecutionHostCall(ctx, ClaimKernelExecutionHostCallInput{
		ExecutionID: call.ExecutionID, HostCallID: call.HostCallID,
		BackendGeneration: backend.BackendGeneration, ControllerEpoch: replacementLease.Epoch,
		ControllerToken: replacementLease.Token, ExpectedVersion: call.StateVersion,
		ClaimToken: strings.Repeat("new-host-claim-", 3), ClaimExpiresAt: time.Now().UTC().Add(time.Minute),
	}); !errors.Is(err, ErrKernelExecutionHostCallConflict) {
		t.Fatalf("executing host call was reclaimed: %v", err)
	}
	resultJSON := `{"ok":true,"value":{"artifact_id":"artifact-a"}}`
	resultSHA256 := sha256HexString(resultJSON)
	call, err = store.CompleteKernelExecutionHostCall(ctx, CompleteKernelExecutionHostCallInput{
		ExecutionID: call.ExecutionID, HostCallID: call.HostCallID, ClaimEpoch: lease.Epoch,
		ClaimToken: claimToken, ExpectedVersion: call.StateVersion, State: KernelExecutionHostCallCompleted,
		ResultJSON: resultJSON, ResultSHA256: resultSHA256,
	})
	if err != nil || call.State != KernelExecutionHostCallCompleted || call.StateVersion != 3 ||
		call.ResultSHA256 != resultSHA256 || call.TerminalAt == nil {
		t.Fatalf("completed host call=%#v err=%v", call, err)
	}
	listed, err := store.ListKernelExecutionHostCalls(ctx, execution.ExecutionID)
	if err != nil || len(listed) != 1 || listed[0].HostCallID != hostCallID ||
		listed[0].State != KernelExecutionHostCallCompleted {
		t.Fatalf("listed host calls=%#v err=%v", listed, err)
	}
	if replay, err := store.CompleteKernelExecutionHostCall(ctx, CompleteKernelExecutionHostCallInput{
		ExecutionID: call.ExecutionID, HostCallID: call.HostCallID, ClaimEpoch: lease.Epoch,
		ClaimToken: claimToken, ExpectedVersion: 2, State: KernelExecutionHostCallCompleted,
		ResultJSON: resultJSON, ResultSHA256: resultSHA256,
	}); err != nil || replay.StateVersion != call.StateVersion {
		t.Fatalf("host call completion replay=%#v err=%v", replay, err)
	}
	conflictingJSON := `{"ok":false}`
	if _, err := store.CompleteKernelExecutionHostCall(ctx, CompleteKernelExecutionHostCallInput{
		ExecutionID: call.ExecutionID, HostCallID: call.HostCallID, ClaimEpoch: lease.Epoch,
		ClaimToken: claimToken, ExpectedVersion: 2, State: KernelExecutionHostCallFailed,
		ResultJSON: conflictingJSON, ResultSHA256: sha256HexString(conflictingJSON), ReasonCode: "conflict",
	}); !errors.Is(err, ErrKernelExecutionHostCallConflict) {
		t.Fatalf("conflicting host call result error=%v", err)
	}
}

func TestKernelExecutionBackendRecreatesInPlaceAfterEvidenceLost(t *testing.T) {
	ctx := context.Background()
	store, backend, _, execution := newAcceptedDetachedKernelExecutionFixture(t)
	if backend.BackendGeneration != 1 || backend.State != KernelExecutionBackendStateReady {
		t.Fatalf("fixture backend=%#v", backend)
	}
	var spec KernelExecutionSessionSpecV1
	if err := json.Unmarshal([]byte(backend.SessionSpecJSON), &spec); err != nil {
		t.Fatal(err)
	}

	// One live executor is allowed per frame+kernel identity. A second row
	// with the same identity must be rejected; this is exactly why the stale
	// row is recreated in place instead of inserting a new backend_id.
	if duplicate, err := store.CreateKernelExecutionBackend(ctx, CreateKernelExecutionBackendInput{
		BackendID: "backend-detached-lifecycle-2", OwnerUserID: backend.OwnerUserID,
		ProjectID: backend.ProjectID, RootFrameID: backend.RootFrameID,
		RootFrameIncarnationID: backend.RootFrameIncarnationID, FrameID: backend.FrameID,
		FrameIncarnationID: backend.FrameIncarnationID, KernelID: backend.KernelID,
		KernelGeneration: backend.KernelGeneration, ExecutorInstanceID: "executor-replacement-new-row",
		MachineBootID:     "machine-boot-new-row",
		SocketPath:        "/run/user/1000/synon-biomed/backend-replacement-new.sock",
		BackendGeneration: 1, SessionSpec: spec,
	}); err == nil || duplicate.BackendID != "" {
		t.Fatalf("duplicate session row unexpectedly accepted: %#v err=%v", duplicate, err)
	}

	// Restart simulation: heartbeat expiry marks the executor evidence lost.
	finished, err := store.FinishKernelExecutionBackend(ctx, FinishKernelExecutionBackendInput{
		BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
		ExecutorInstanceID: backend.ExecutorInstanceID, EvidenceLost: true,
	})
	if err != nil || finished.State != KernelExecutionBackendStateEvidenceLost ||
		finished.StateVersion != backend.StateVersion+1 {
		t.Fatalf("evidence lost=%#v err=%v", finished, err)
	}
	if again, err := store.FinishKernelExecutionBackend(ctx, FinishKernelExecutionBackendInput{
		BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
		ExecutorInstanceID: backend.ExecutorInstanceID, EvidenceLost: true,
	}); err != nil || again.StateVersion != finished.StateVersion {
		t.Fatalf("evidence lost replay=%#v err=%v", again, err)
	}

	// The session lookup must surface terminal rows so EnsureSession can
	// reuse the identity instead of colliding on the unique index.
	foundBackend, found, err := store.FindKernelExecutionBackendForSession(ctx, spec)
	if err != nil || !found || foundBackend.BackendID != backend.BackendID ||
		foundBackend.State != KernelExecutionBackendStateEvidenceLost {
		t.Fatalf("session lookup=%#v found=%t err=%v", foundBackend, found, err)
	}

	// In-place recreation advances the executor generation and clears all
	// controller/process authority while preserving the session identity.
	recreated, err := store.RecreateKernelExecutionBackend(ctx, RecreateKernelExecutionBackendInput{
		BackendID: backend.BackendID, ExecutorInstanceID: "executor-recreated",
		MachineBootID: "machine-boot-recreated",
		SocketPath:    "/run/user/1000/synon-biomed/backend-detached-lifecycle.sock",
		KernelID:      backend.KernelID, KernelGeneration: backend.KernelGeneration,
		SessionSpec: spec,
	})
	if err != nil || recreated.BackendGeneration != 2 ||
		recreated.State != KernelExecutionBackendStateStarting || recreated.StateVersion != 1 ||
		recreated.ExecutorInstanceID != "executor-recreated" || recreated.ExecutorPID != 0 ||
		recreated.ExecutorPIDStartTicks != 0 || recreated.WorkerPID != 0 ||
		recreated.WorkerPIDStartTicks != 0 || recreated.WorkerPGID != 0 || recreated.CgroupPath != "" ||
		recreated.HeartbeatAt != nil ||
		recreated.ControllerEpoch != 0 || len(recreated.ControllerTokenSHA256) != 0 ||
		recreated.ControllerLeaseExpiresAt != nil || recreated.SessionSpecSHA256 != backend.SessionSpecSHA256 {
		t.Fatalf("recreated=%#v err=%v", recreated, err)
	}

	// An unstarted generation must first settle an explicit failure receipt;
	// it can then use the same recreation path as every other terminal backend.
	if err := store.FailKernelExecutorStartup(ctx, KernelStartupFailure{
		BackendID: recreated.BackendID, BackendGeneration: recreated.BackendGeneration,
		ExecutorInstanceID: recreated.ExecutorInstanceID, Stage: "worker_start",
	}); err != nil {
		t.Fatal(err)
	}
	restarted, err := store.RecreateKernelExecutionBackend(ctx, RecreateKernelExecutionBackendInput{
		BackendID: backend.BackendID, ExecutorInstanceID: "executor-restarted-before-ready",
		MachineBootID: "machine-boot-restarted-before-ready",
		SocketPath:    "/run/user/1000/synon-biomed/backend-detached-lifecycle.sock",
		KernelID:      backend.KernelID, KernelGeneration: backend.KernelGeneration,
		SessionSpec: spec,
	})
	if err != nil || restarted.BackendGeneration != 3 ||
		restarted.State != KernelExecutionBackendStateStarting || restarted.StateVersion != 1 ||
		restarted.ExecutorInstanceID != "executor-restarted-before-ready" || restarted.ExecutorPID != 0 ||
		restarted.WorkerPID != 0 || restarted.HeartbeatAt != nil || restarted.ControllerEpoch != 0 ||
		len(restarted.ControllerTokenSHA256) != 0 || restarted.ControllerLeaseExpiresAt != nil ||
		restarted.SessionSpecSHA256 != backend.SessionSpecSHA256 {
		t.Fatalf("restarted=%#v err=%v", restarted, err)
	}
	if _, err := store.RecreateKernelExecutionBackend(ctx, RecreateKernelExecutionBackendInput{
		BackendID: backend.BackendID, ExecutorInstanceID: "executor-recreated",
		MachineBootID: "machine-boot-recreated",
		SocketPath:    "/run/user/1000/synon-biomed/backend-detached-lifecycle.sock",
		KernelID:      backend.KernelID, KernelGeneration: backend.KernelGeneration,
		SessionSpec: spec,
	}); !errors.Is(err, ErrKernelExecutionBackendStale) {
		t.Fatalf("live recreate error=%v", err)
	}

	// Historical detached executions keep their original generation so their
	// durable references remain valid.
	var persistedGeneration int64
	if err := store.db.QueryRow(`SELECT backend_generation FROM kernel_detached_executions WHERE execution_id=?`,
		execution.ExecutionID).Scan(&persistedGeneration); err != nil || persistedGeneration != 1 {
		t.Fatalf("historical execution generation=%d err=%v", persistedGeneration, err)
	}

	// The new executor generation can be activated and used.
	activated, err := store.ActivateKernelExecutionBackend(ctx, ActivateKernelExecutionBackendInput{
		BackendID: restarted.BackendID, BackendGeneration: restarted.BackendGeneration,
		ExecutorInstanceID: restarted.ExecutorInstanceID, ExecutorPID: 3101, ExecutorPIDStartTicks: 90301,
		WorkerPID: 3102, WorkerPIDStartTicks: 90302, WorkerPGID: 3102,
		CgroupPath: "/user.slice/user-1000.slice/synon-biomed-recreated.scope", HeartbeatSequence: 1,
	})
	if err != nil || activated.State != KernelExecutionBackendStateReady ||
		activated.BackendGeneration != 3 || activated.ExecutorPID != 3101 {
		t.Fatalf("activated=%#v err=%v", activated, err)
	}
	if _, err := store.RecreateKernelExecutionBackend(ctx, RecreateKernelExecutionBackendInput{
		BackendID: backend.BackendID, ExecutorInstanceID: "executor-stale-restart",
		MachineBootID: "machine-boot-stale-restart",
		SocketPath:    "/run/user/1000/synon-biomed/backend-detached-lifecycle.sock",
		KernelID:      backend.KernelID, KernelGeneration: backend.KernelGeneration,
		SessionSpec: spec,
	}); !errors.Is(err, ErrKernelExecutionBackendStale) {
		t.Fatalf("stale starting restart error=%v", err)
	}

	// A mismatched session spec must not hijack an existing session row.
	wrongSpec := spec
	wrongSpec.WorkspaceDir = "/tmp/different"
	if _, err := store.RecreateKernelExecutionBackend(ctx, RecreateKernelExecutionBackendInput{
		BackendID: backend.BackendID, ExecutorInstanceID: "executor-wrong-spec",
		MachineBootID: "machine-boot-wrong-spec", SocketPath: "/run/user/1000/synon-biomed/wrong.sock",
		KernelID: backend.KernelID, KernelGeneration: backend.KernelGeneration,
		SessionSpec: wrongSpec,
	}); !errors.Is(err, ErrKernelExecutionBackendConflict) {
		t.Fatalf("wrong spec recreate error=%v", err)
	}
}

func TestKernelExecutionBackendAdvancesKernelGenerationForChangedSessionAuthority(t *testing.T) {
	ctx := context.Background()
	store, backend, _, _ := newAcceptedDetachedKernelExecutionFixture(t)
	if _, err := store.FinishKernelExecutionBackend(ctx, FinishKernelExecutionBackendInput{
		BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
		ExecutorInstanceID: backend.ExecutorInstanceID, EvidenceLost: true,
	}); err != nil {
		t.Fatal(err)
	}
	var changed KernelExecutionSessionSpecV1
	if err := json.Unmarshal([]byte(backend.SessionSpecJSON), &changed); err != nil {
		t.Fatal(err)
	}
	changed.EgressAllowedDomains = []string{"data.example.org"}
	if exact, found, err := store.FindKernelExecutionBackendForSession(ctx, changed); err != nil || found || exact.BackendID != "" {
		t.Fatalf("changed authority matched exact backend=%#v found=%t err=%v", exact, found, err)
	}
	predecessor, found, err := store.FindLatestKernelExecutionBackendForIdentity(ctx, changed)
	if err != nil || !found || predecessor.BackendID != backend.BackendID ||
		predecessor.State != KernelExecutionBackendStateEvidenceLost {
		t.Fatalf("identity predecessor=%#v found=%t err=%v", predecessor, found, err)
	}
	replacement, err := store.CreateKernelExecutionBackend(ctx, CreateKernelExecutionBackendInput{
		BackendID: "backend-authority-successor", OwnerUserID: backend.OwnerUserID,
		ProjectID: backend.ProjectID, RootFrameID: backend.RootFrameID,
		RootFrameIncarnationID: backend.RootFrameIncarnationID, FrameID: backend.FrameID,
		FrameIncarnationID: backend.FrameIncarnationID, KernelID: backend.KernelID,
		KernelGeneration: predecessor.KernelGeneration + 1, SessionSpec: changed,
		ExecutorInstanceID: "executor-authority-successor", MachineBootID: "boot-authority-successor",
		SocketPath: "/run/user/1000/synon-biomed/backend-authority-successor.sock", BackendGeneration: 1,
	})
	if err != nil || replacement.KernelGeneration != 2 || replacement.State != KernelExecutionBackendStateStarting {
		t.Fatalf("authority successor=%#v err=%v", replacement, err)
	}
}

func newAcceptedDetachedKernelExecutionFixture(
	t *testing.T,
) (*Store, KernelExecutionBackend, KernelExecutionControlLease, DetachedKernelExecution) {
	t.Helper()
	store, repo, claim := newKernelLocalOperationFixture(t)
	return acceptDetachedKernelExecutionFixture(t, store, repo, claim)
}

func acceptDetachedKernelExecutionFixture(t *testing.T, store *Store, repo *transcriptstore.Repository, claim transcriptstore.RunnerClaim) (*Store, KernelExecutionBackend, KernelExecutionControlLease, DetachedKernelExecution) {
	t.Helper()
	ctx := context.Background()
	operation := createKernelLocalOperationForTest(t, store, repo, claim, "detached-lifecycle", "call-detached-lifecycle")
	approved, err := store.ResolveKernelLocalOperationApproval(ctx, ResolveKernelLocalOperationApprovalInput{
		OwnerUserID: "owner", OperationID: operation.OperationID, ExpectedStateVersion: 1,
		ApprovalRequestID: operation.ApprovalRequestID, Approved: true,
		DecisionID: "decision-detached-lifecycle", Scope: "once", Source: "user", ActorID: "owner",
		CurrentClaim: claim,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.PrepareKernelLocalOperation(ctx, PrepareKernelLocalOperationInput{
		OwnerUserID: "owner", OperationID: operation.OperationID, ExpectedStateVersion: approved.StateVersion,
		Claim: claim, BootID: "service-boot-lifecycle", KernelID: "kernel-detached-lifecycle", KernelGeneration: 1,
		ConfinementSHA256: strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	backend, err := store.CreateKernelExecutionBackend(ctx, CreateKernelExecutionBackendInput{
		BackendID: "backend-detached-lifecycle", OwnerUserID: prepared.OwnerUserID,
		ProjectID: prepared.ProjectID, RootFrameID: prepared.RootFrameID,
		RootFrameIncarnationID: prepared.RootFrameIncarnationID, FrameID: prepared.FrameID,
		FrameIncarnationID: prepared.FrameIncarnationID, KernelID: prepared.KernelID,
		KernelGeneration: prepared.KernelGeneration, ExecutorInstanceID: "executor-detached-lifecycle",
		MachineBootID: "machine-boot-lifecycle",
		SocketPath:    "/run/user/1000/synon-biomed/backend-detached-lifecycle.sock", BackendGeneration: 1,
		SessionSpec: kernelExecutionSessionSpecForTest(prepared),
	})
	if err != nil {
		t.Fatal(err)
	}
	backend, err = store.ActivateKernelExecutionBackend(ctx, ActivateKernelExecutionBackendInput{
		BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
		ExecutorInstanceID: backend.ExecutorInstanceID, ExecutorPID: 2101, ExecutorPIDStartTicks: 90201,
		WorkerPID: 2102, WorkerPIDStartTicks: 90202, WorkerPGID: 2102,
		CgroupPath: "/user.slice/user-1000.slice/synon-biomed-detached-lifecycle.scope", HeartbeatSequence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	backend, lease, err := store.AcquireKernelExecutionBackendControl(ctx, AcquireKernelExecutionBackendControlInput{
		BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
		Token: strings.Repeat("lifecycle-token-", 3), LeaseExpiresAt: time.Now().UTC().Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, execution, err := store.StartDetachedKernelLocalOperation(ctx, StartDetachedKernelLocalOperationInput{
		Start: StartKernelLocalOperationInput{
			OwnerUserID: "owner", OperationID: prepared.OperationID, ExpectedStateVersion: prepared.StateVersion,
			Claim: claim, BootID: prepared.BootID, ExecutionID: "execution-detached-lifecycle",
		}, BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
		ControllerEpoch: lease.Epoch, ControllerToken: lease.Token,
		Request: kernelDetachedExecutionRequestForTest(prepared, "execution-detached-lifecycle",
			kernelExecutionSessionSpecForTest(prepared)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, backend, lease, execution
}

func TestKernelExecutionSessionSpecPreservesCanonicalEgressPolicy(t *testing.T) {
	spec := KernelExecutionSessionSpecV1{
		Version: 1, KernelID: "kernel-egress", OwnerUserID: "owner", ProjectID: "project",
		RootFrameID: "root", RootFrameIncarnationID: "root-incarnation",
		FrameID: "frame", FrameIncarnationID: "frame-incarnation", AgentName: "agent",
		KernelKind: "analysis", Language: "python", Environment: "python",
		WorkspaceDir: "/tmp/synon-egress-spec", Mounts: []KernelExecutionMountSpecV1{}, ProtectedPaths: []string{},
		EgressAllowedDomains: []string{"API.Example.COM", "*.example.org"},
		EgressDeniedDomains:  []string{"blocked.example.org", "s3.*.amazonaws.com"},
		CABundle:             "/etc/synon/company.pem",
		UpstreamProxy:        "http://Proxy.Example:8080/",
	}
	normalized, encoded, _, err := canonicalKernelExecutionSessionSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.EgressAllowedDomains[0] != "*.example.org" || normalized.EgressAllowedDomains[1] != "api.example.com" {
		t.Fatalf("normalized egress allowlist = %#v", normalized.EgressAllowedDomains)
	}
	decoded, err := DecodeKernelExecutionSessionSpecV1(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.EgressDeniedDomains) != 2 || decoded.EgressDeniedDomains[0] != "blocked.example.org" ||
		decoded.EgressDeniedDomains[1] != "s3.*.amazonaws.com" {
		t.Fatalf("decoded egress denylist = %#v", decoded.EgressDeniedDomains)
	}
	if decoded.UpstreamProxy != "http://proxy.example:8080" {
		t.Fatalf("decoded upstream proxy = %q", decoded.UpstreamProxy)
	}
	if decoded.CABundle != "/etc/synon/company.pem" {
		t.Fatalf("decoded CA bundle = %q", decoded.CABundle)
	}
}

func TestKernelExecutionSessionSpecPreservesTrustedReadOnlyMount(t *testing.T) {
	spec := kernelExecutionSessionSpecForTest(KernelLocalOperation{
		KernelID: "kernel-trusted-mount", OwnerUserID: "owner", ProjectID: "project",
		RootFrameID: "root", RootFrameIncarnationID: "root-incarnation",
		FrameID: "frame", FrameIncarnationID: "frame-incarnation", Environment: "python",
	})
	spec.Mounts = []KernelExecutionMountSpecV1{{Path: "/opt/synon/skills", Trusted: true}}
	normalized, encoded, _, err := canonicalKernelExecutionSessionSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized.Mounts) != 1 || !normalized.Mounts[0].Trusted || normalized.Mounts[0].Writable {
		t.Fatalf("normalized trusted mount = %#v", normalized.Mounts)
	}
	decoded, err := DecodeKernelExecutionSessionSpecV1(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.Mounts) != 1 || !decoded.Mounts[0].Trusted {
		t.Fatalf("decoded trusted mount = %#v", decoded.Mounts)
	}

	spec.Mounts[0].Writable = true
	if _, _, _, err := canonicalKernelExecutionSessionSpec(spec); err == nil {
		t.Fatal("trusted writable mount was accepted")
	}
}

func kernelExecutionSessionSpecForTest(operation KernelLocalOperation) KernelExecutionSessionSpecV1 {
	return KernelExecutionSessionSpecV1{
		Version: 1, KernelID: operation.KernelID, OwnerUserID: operation.OwnerUserID,
		ProjectID: operation.ProjectID, RootFrameID: operation.RootFrameID,
		RootFrameIncarnationID: operation.RootFrameIncarnationID, FrameID: operation.FrameID,
		FrameIncarnationID: operation.FrameIncarnationID, AgentName: "OPERON",
		KernelKind: "operon", Language: "python", Environment: operation.Environment,
		WorkspaceDir: "/tmp/synon-biomed-detached-test", Mounts: []KernelExecutionMountSpecV1{},
		ProtectedPaths: []string{},
	}
}

func kernelDetachedExecutionRequestForTest(
	operation KernelLocalOperation,
	executionID string,
	session KernelExecutionSessionSpecV1,
) KernelDetachedExecutionRequestV1 {
	var input map[string]any
	if err := json.Unmarshal(operation.InputJSON, &input); err != nil {
		panic(err)
	}
	code, _ := input["code"].(string)
	workingDir, _ := input["working_dir"].(string)
	background, _ := input["background"].(bool)
	fresh, _ := input["fresh"].(bool)
	return KernelDetachedExecutionRequestV1{
		Version: 1, OperationID: operation.OperationID, ExecutionID: executionID,
		OwnerUserID: operation.OwnerUserID, ProjectID: operation.ProjectID,
		RootFrameID: operation.RootFrameID, RootFrameIncarnationID: operation.RootFrameIncarnationID,
		FrameID: operation.FrameID, FrameIncarnationID: operation.FrameIncarnationID,
		KernelID: operation.KernelID, KernelGeneration: operation.KernelGeneration,
		ToolCallID: operation.ToolCallID, ToolName: operation.Tool, Language: session.Language,
		KernelKind: session.KernelKind, Environment: operation.Environment, Code: code,
		WorkingDir: workingDir, Background: background, Fresh: fresh, Origin: "agent",
		OutputLimitBytes: 1 << 20,
		HostCallMethods:  []string{},
	}
}

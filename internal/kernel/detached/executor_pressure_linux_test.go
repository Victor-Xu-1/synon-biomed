//go:build linux

package detached

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	kernelruntime "synon-go/internal/kernel"
	"synon-go/internal/processsupervisor"
	"syscall"
	"testing"
	"time"
)

func TestMemoryPressureReliefKeepsRealWorkWithinHardBudget(t *testing.T) {
	runRealPressureService(t, "direct")
}

func TestExecutorMemoryPressureSupervisorRelievesRealWorker(t *testing.T) {
	runRealPressureService(t, "supervisor")
}

func runRealPressureService(t *testing.T, mode string) {
	t.Helper()
	if os.Getenv("SYNON_TEST_SYSTEMD_PRESSURE") != "1" {
		t.Skip("opt-in isolated systemd memory pressure probe")
	}
	id := "kernel-backend-pressure-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	unit := executorUnitName(id, 1)
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = exec.CommandContext(cleanup, "systemctl", "--user", "stop", unit).Run()
	})
	command := exec.CommandContext(ctx, "systemd-run", "--user", "--quiet", "--collect", "--wait", "--pipe", "--unit="+unit,
		"--working-directory="+workingDirectory,
		"--property=Delegate=memory", "--property=DelegateSubgroup=control", "--property=MemoryHigh=335544320", "--property=MemoryMax=335544320", "--property=MemorySwapMax=0", "--property=OOMPolicy=continue",
		"--setenv=SYNON_TEST_PRESSURE_HELPER="+id, "--setenv=SYNON_TEST_PRESSURE_MODE="+mode, "--", os.Args[0], "-test.run=^TestMemoryPressureReliefHelper$", "-test.v")
	out, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "REAL_PRESSURE_RELIEVED_WITHOUT_BUDGET_INCREASE") {
		t.Fatalf("real resource relief failed: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}

func TestMemoryPressureReliefHelper(t *testing.T) {
	id := os.Getenv("SYNON_TEST_PRESSURE_HELPER")
	if id == "" {
		t.Skip("owned test service helper")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	domain, err := prepareExecutorMemoryDomain(id, 1)
	if err != nil || domain == nil {
		t.Fatalf("resource domain: %v", err)
	}
	defer domain.root.Close()
	if os.Getenv("SYNON_TEST_PRESSURE_MODE") == "supervisor" {
		testRealPressureSupervisor(t, domain)
		return
	}
	if err := writeResourceControl(domain.root, "memory.high", "67108864"); err != nil {
		t.Fatal(err)
	}
	groupFile, err := os.Open(domain.directory)
	if err != nil {
		t.Fatal(err)
	}
	defer groupFile.Close()
	command := exec.CommandContext(ctx, "python3", "-c", "data = bytearray(160 * 1024 * 1024); print('PAYLOAD_COMPLETED', flush=True)")
	command.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(groupFile.Fd())}
	done := make(chan error, 1)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer command.Process.Kill()
	go func() { done <- command.Wait() }()
	policy := processsupervisor.NewMemoryPressurePolicy(time.Second, 10*time.Second)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	relieved := false
	var limit uint64
	for {
		select {
		case err := <-done:
			if err != nil || !relieved {
				t.Fatalf("work ended without observed relief: %v, relieved=%t", err, relieved)
			}
			after, err := domain.sample()
			if err != nil || after.LimitBytes != limit || after.HighBytes != limit {
				t.Fatalf("hard budget changed: %+v %v", after, err)
			}
			t.Log("REAL_PRESSURE_RELIEVED_WITHOUT_BUDGET_INCREASE")
			return
		case <-ctx.Done():
			t.Fatal("isolated pressure probe did not finish")
		case <-ticker.C:
			sample, err := domain.sample()
			if err != nil {
				t.Fatal(err)
			}
			if policy.Observe(sample) == processsupervisor.MemoryPressureRelieve {
				limit = sample.LimitBytes
				if err := domain.relieve(sample); err != nil {
					t.Fatal(err)
				}
				relieved = true
			}
		}
	}
}

func testRealPressureSupervisor(t *testing.T, domain *executorMemoryDomain) {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	assets := filepath.Join(root, "assets", "optional")
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	manager := kernelruntime.NewManager(kernelruntime.Config{Python: python, AssetRoot: assets,
		WorkerPath: filepath.Join(assets, "kernels", "kernel_worker.py"), ManifestPath: filepath.Join(assets, "kernel-compute.manifest.json")})
	defer manager.CloseAll(context.Background())
	if err := manager.ConfigureWorkerResourceDomain(domain.directory); err != nil {
		t.Fatal(err)
	}
	spec := kernelruntime.SessionSpec{KernelID: "pressure-worker", FrameID: "pressure-frame", RootFrameID: "pressure-frame", AgentName: "OPERON", KernelKind: "analysis", Language: "python", Environment: "python", WorkspaceDir: t.TempDir()}
	session, err := manager.EnsureSession(spec)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := domain.sample()
	if err != nil || initial.CurrentBytes >= initial.HighBytes {
		t.Fatalf("invalid initial budget: %+v %v", initial, err)
	}
	// Allocate just above the soft boundary, well below the unchanged hard
	// ceiling. Use the actual worker footprint, not an interpreter-specific guess.
	allocation := initial.HighBytes - initial.CurrentBytes + (8 << 20)
	handle, err := manager.Submit(kernelruntime.SubmitRequest{KernelID: session.ID, FrameID: spec.FrameID, KernelKind: spec.KernelKind, Language: spec.Language, Environment: spec.Environment,
		ExecID: "pressure-execution", ToolUseID: "pressure-call", Origin: "agent", Code: "data = bytearray(" + strconv.FormatUint(allocation, 10) + ")\nprint('PAYLOAD_COMPLETED', flush=True)"})
	if err != nil {
		t.Fatal(err)
	}
	executor := &Executor{memoryDomain: domain, active: map[string]*activeExecution{"pressure-execution": {kernel: handle}}}
	ctx, cancel := context.WithTimeout(context.Background(), 85*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); executor.runMemoryPressureSupervisor(ctx) }()
	defer func() { cancel(); <-done }()
	diagnostics := time.NewTicker(5 * time.Second)
	defer diagnostics.Stop()
	for waiting := true; waiting; {
		select {
		case outcome := <-handle.Done():
			if outcome.Err != nil || outcome.Response.Error != "" || !strings.Contains(outcome.Response.Stdout, "PAYLOAD_COMPLETED") {
				t.Fatalf("pressure execution: %+v", outcome)
			}
			waiting = false
		case <-ctx.Done():
			t.Fatal("real executor did not relieve pressure")
		case <-diagnostics.C:
			observed, sampleErr := domain.sample()
			t.Logf("active=%t output=%d sample=%+v err=%v", handle.IsRunning(), handle.OutputProgressSequence(), observed, sampleErr)
		}
	}
	after, err := domain.sample()
	if err != nil || after.HighBytes != initial.LimitBytes || after.LimitBytes != initial.LimitBytes {
		t.Fatalf("invalid relief: %+v %v", after, err)
	}
	t.Log("REAL_PRESSURE_RELIEVED_WITHOUT_BUDGET_INCREASE")
}

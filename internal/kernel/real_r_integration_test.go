package kernel

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRealMicromambaRepairAndRWorkerProtocol(t *testing.T) {
	runtimeRoot := strings.TrimSpace(os.Getenv("SYNON_TEST_REAL_R_ROOT"))
	if runtimeRoot == "" {
		t.Skip("SYNON_TEST_REAL_R_ROOT is not configured")
	}
	runtimeRoot, err := filepath.Abs(runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	volumeRoot := filepath.Clean(filepath.VolumeName(runtimeRoot) + string(os.PathSeparator))
	if runtimeRoot == volumeRoot {
		t.Fatal("SYNON_TEST_REAL_R_ROOT must not be a filesystem root")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	environmentName := "r-go-protocol-gate"
	environmentRoot := filepath.Join(runtimeRoot, "envs")
	environmentPrefix := filepath.Join(environmentRoot, environmentName)
	if err := os.RemoveAll(environmentPrefix); err != nil {
		t.Fatalf("reset disposable R environment: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(environmentPrefix) })

	manager := newLifecycleTestManager(t, Config{
		Micromamba:          filepath.Join(repositoryRoot, "assets", "optional", "micromamba", "linux-x86_64", "micromamba"),
		CondaHome:           filepath.Join(runtimeRoot, "mamba"),
		CondaEnvsPath:       environmentRoot,
		DefaultREnv:         environmentName,
		DefaultREnvPackages: []string{"r-base=4.4", "r-jsonlite"},
		ExecutionTimeout:    10 * time.Second,
	})
	repairCtx, cancelRepair := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancelRepair()
	if err := manager.RepairDefaultREnvironment(repairCtx); err != nil {
		t.Fatalf("repair real R environment: %v", err)
	}
	statuses := manager.RuntimeEnvironmentStatuses(false)
	if len(statuses) != 2 || statuses[1].Language != "r" || statuses[1].Status != "ready" || statuses[1].PackageCount < 2 {
		t.Fatalf("real R environment status = %#v", statuses)
	}

	workspace := t.TempDir()
	worker, err := manager.StartSession(SessionSpec{
		KernelID: "real-r-worker", FrameID: "real-r-frame", RootFrameID: "real-r-frame",
		AgentName: "OPERON", KernelKind: "r", Language: "r", Environment: environmentName,
		WorkspaceDir: workspace,
	})
	if err != nil {
		t.Fatalf("start real R worker: %v", err)
	}
	executeCtx, cancelExecute := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelExecute()
	first, err := worker.Execute(executeCtx, `state_value <- 40; cat(state_value + 2, "\n"); writeLines("r-worker-ok", "r-worker-evidence.txt")`, "user")
	if err != nil {
		t.Fatalf("execute first real R request: %v", err)
	}
	if strings.TrimSpace(first.Stdout) != "42" || first.Error != "" || first.Interrupted || first.Usage["wall_s"] == nil {
		t.Fatalf("first real R response = %#v", first)
	}
	second, err := worker.Execute(executeCtx, `cat(state_value + 3, "\n")`, "agent")
	if err != nil {
		t.Fatalf("execute persistent real R request: %v", err)
	}
	if strings.TrimSpace(second.Stdout) != "43" || second.Error != "" {
		t.Fatalf("persistent real R response = %#v", second)
	}
	failure, err := worker.Execute(executeCtx, `stop("r-protocol-error")`, "user")
	if err != nil {
		t.Fatalf("execute real R failure request: %v", err)
	}
	if !strings.Contains(failure.Error, "r-protocol-error") || failure.Trace["error_call"] == nil {
		t.Fatalf("structured real R failure = %#v", failure)
	}
	handle, err := manager.Submit(SubmitRequest{
		FrameID: "real-r-frame", KernelKind: "r", Language: "r", Environment: environmentName,
		ExecID: "real-r-interrupt", ToolUseID: "real-r-interrupt-tool",
		Code: `repeat { Sys.sleep(0.05) }`, Origin: "user", Timeout: 30 * time.Second, InterruptGrace: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("submit interruptible real R request: %v", err)
	}
	select {
	case <-handle.Started():
	case <-time.After(5 * time.Second):
		t.Fatal("real R interrupt request did not start")
	}
	interrupt := manager.Interrupt("real-r-frame", "real-r-interrupt")
	if !interrupt.Interrupted || interrupt.Via != "sigint" {
		t.Fatalf("real R interrupt result = %#v", interrupt)
	}
	interrupted := waitForLifecycleOutcome(t, handle)
	if interrupted.Err != nil || !interrupted.Response.Interrupted || !strings.Contains(interrupted.Response.Error, "Interrupted") {
		t.Fatalf("real R interrupted response = %#v err=%v", interrupted.Response, interrupted.Err)
	}
	afterInterrupt, err := worker.Execute(executeCtx, `cat(state_value + 4, "\n")`, "user")
	if err != nil || strings.TrimSpace(afterInterrupt.Stdout) != "44" || afterInterrupt.Error != "" {
		t.Fatalf("real R post-interrupt response = %#v err=%v", afterInterrupt, err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, "r-worker-evidence.txt"))
	if err != nil || strings.TrimSpace(string(raw)) != "r-worker-ok" {
		t.Fatalf("real R workspace evidence = %q err=%v", raw, err)
	}
}

func TestManagedRRuntimeHardeningAndWireCompatibility(t *testing.T) {
	environmentRoot := strings.TrimSpace(os.Getenv("SYNON_TEST_MANAGED_R_ENVS"))
	if environmentRoot == "" {
		t.Skip("SYNON_TEST_MANAGED_R_ENVS is not configured")
	}
	environmentRoot, err := filepath.Abs(environmentRoot)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(environmentRoot, "r", "bin", "Rscript")); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("Claude R environment is unavailable: %v", err)
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, ".Rprofile"), []byte(`Sys.setenv(SYNON_PROFILE_MARKER="loaded")`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".Renviron"), []byte("SYNON_ENVIRON_MARKER=loaded\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := newLifecycleTestManager(t, Config{
		CondaHome: filepath.Join(t.TempDir(), "conda"), CondaEnvsPath: environmentRoot,
		DisableROperationLog: true,
		RWorkerPath:          filepath.Join(repositoryRoot, "assets", "optional", "kernels", "kernel_worker.R"),
		DefaultREnv:          "r", ExecutionTimeout: 15 * time.Second,
		Environment: map[string]string{
			"OPERON_SECRET_VARS": "SYNON_TEST_R_SECRET", "SYNON_TEST_R_SECRET": "must-not-leak",
		},
	})
	worker, err := manager.StartSession(SessionSpec{
		KernelID: "claude-r-wire", FrameID: "claude-r-frame", RootFrameID: "claude-r-frame",
		AgentName: "OPERON", KernelKind: "r", Language: "r", Environment: "r", WorkspaceDir: workspace,
	})
	if err != nil {
		t.Fatalf("start Claude R runtime: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	hardened, err := worker.Execute(ctx, `cat(Sys.getenv("SYNON_TEST_R_SECRET", "missing"), "|", Sys.getenv("SYNON_PROFILE_MARKER", "missing"), "|", Sys.getenv("SYNON_ENVIRON_MARKER", "missing"), "|", Sys.getenv("OPERON_WRITABLE_ROOTS", "missing"), "\n", sep="")`, "user")
	if err != nil || strings.TrimSpace(hardened.Stdout) != "missing|missing|missing|missing" || hardened.Error != "" {
		t.Fatalf("R startup hardening response=%#v err=%v", hardened, err)
	}
	dlopen, err := worker.Execute(ctx, `dyn.load(file.path(getwd(), "untrusted.so"))`, "user")
	if err != nil || !strings.Contains(dlopen.Error, "Refusing to dyn.load shared library from writable path") {
		t.Fatalf("R dyn.load guard response=%#v err=%v", dlopen, err)
	}
	exempt, err := worker.Execute(ctx, `dyn.load(file.path(Sys.getenv("R_LIBS_USER"), "missing.so"))`, "user")
	if err != nil || exempt.Error == "" || strings.Contains(exempt.Error, "Refusing to dyn.load") {
		t.Fatalf("R dyn.load exemption response=%#v err=%v", exempt, err)
	}
	lineError, err := worker.Execute(ctx, "value <- 1\nstop(\"line-two\")", "user")
	if err != nil || lineError.Error != "line-two" || lineError.Trace["error_lineno"] != float64(2) {
		t.Fatalf("R error line response=%#v err=%v", lineError, err)
	}
	partial, err := worker.Execute(ctx, `cat("partial")`, "user")
	if err != nil || partial.Stdout != "partial" || partial.Error != "" {
		t.Fatalf("R partial stdout response=%#v err=%v", partial, err)
	}
	quitAttempt, err := worker.Execute(ctx, `q()`, "user")
	if err != nil || !strings.Contains(quitAttempt.Error, "q()/quit() is disabled here") {
		t.Fatalf("R quit guard response=%#v err=%v", quitAttempt, err)
	}
	afterQuit, err := worker.Execute(ctx, `cat("still-alive\n")`, "user")
	if err != nil || strings.TrimSpace(afterQuit.Stdout) != "still-alive" || afterQuit.Error != "" {
		t.Fatalf("R worker after quit response=%#v err=%v", afterQuit, err)
	}
	multibyte, err := worker.Execute(ctx, `cat(paste(rep("é", 600000), collapse=""))`, "user")
	if err != nil || multibyte.Error != "" || strings.Contains(multibyte.Stdout, "truncated") || len([]rune(multibyte.Stdout)) != 600000 {
		t.Fatalf("R multibyte output runes=%d bytes=%d error=%q err=%v", len([]rune(multibyte.Stdout)), len(multibyte.Stdout), multibyte.Error, err)
	}
	state, err := worker.Execute(ctx, `state_value <- 40; cat(state_value + 2, "\n")`, "user")
	if err != nil || strings.TrimSpace(state.Stdout) != "42" || state.Error != "" {
		t.Fatalf("R persistent state seed=%#v err=%v", state, err)
	}
	streamHandle, err := manager.Submit(SubmitRequest{
		KernelID: "claude-r-wire", FrameID: "claude-r-frame", KernelKind: "r", Language: "r", Environment: "r",
		ExecID: "claude-r-stream", ToolUseID: "claude-r-stream-tool",
		Code: `for (i in 1:3) { cat(sprintf("stream-%d\n", i)); Sys.sleep(0.2) }`, Origin: "user",
		Timeout: 10 * time.Second, InterruptGrace: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("submit real R streaming request: %v", err)
	}
	select {
	case <-streamHandle.Started():
	case <-time.After(5 * time.Second):
		t.Fatal("real R streaming request did not start")
	}
	streamDeadline := time.Now().Add(2 * time.Second)
	streamedBeforeCompletion := false
	for time.Now().Before(streamDeadline) {
		for _, stream := range manager.ListExecStreams("claude-r-frame") {
			if stream.ExecID == "claude-r-stream" && strings.Contains(stream.Stdout, "stream-1") {
				streamedBeforeCompletion = !strings.Contains(stream.Stdout, "stream-3")
				break
			}
		}
		if streamedBeforeCompletion {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	streamOutcome := waitForLifecycleOutcome(t, streamHandle)
	if !streamedBeforeCompletion || streamOutcome.Err != nil ||
		!strings.Contains(streamOutcome.Response.Stdout, "stream-1") ||
		!strings.Contains(streamOutcome.Response.Stdout, "stream-3") {
		t.Fatalf("R live stream before completion=%t response=%#v err=%v",
			streamedBeforeCompletion, streamOutcome.Response, streamOutcome.Err)
	}
	handle, err := manager.Submit(SubmitRequest{
		KernelID: "claude-r-wire", FrameID: "claude-r-frame", KernelKind: "r", Language: "r", Environment: "r",
		ExecID: "claude-r-interrupt", ToolUseID: "claude-r-interrupt-tool",
		Code: `repeat { Sys.sleep(0.05) }`, Origin: "user", Timeout: 30 * time.Second, InterruptGrace: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("submit real R interrupt: %v", err)
	}
	select {
	case <-handle.Started():
	case <-time.After(5 * time.Second):
		t.Fatal("real R interrupt request did not start")
	}
	interrupt := manager.Interrupt("claude-r-frame", "claude-r-interrupt")
	if !interrupt.Interrupted || interrupt.Via != "sigint" {
		t.Fatalf("real R interrupt result=%#v", interrupt)
	}
	interrupted := waitForLifecycleOutcome(t, handle)
	if interrupted.Err != nil || !interrupted.Response.Interrupted || interrupted.Response.Error != "Interrupted" {
		t.Fatalf("real R interrupted response=%#v err=%v", interrupted.Response, interrupted.Err)
	}
	afterInterrupt, err := worker.Execute(ctx, `cat(state_value + 3, "\n")`, "user")
	if err != nil || strings.TrimSpace(afterInterrupt.Stdout) != "43" || afterInterrupt.Error != "" {
		t.Fatalf("R state after interrupt=%#v err=%v", afterInterrupt, err)
	}
	library := filepath.Join(workspace, ".r-libs", "claude-r-wire", "r")
	marker := filepath.Join(library, "stale-marker")
	if err := os.WriteFile(marker, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 3*time.Second)
	if err := worker.Close(closeCtx); err != nil {
		closeCancel()
		t.Fatalf("close real R worker: %v", err)
	}
	closeCancel()
	restarted, err := manager.StartSession(SessionSpec{
		KernelID: "claude-r-wire", FrameID: "claude-r-frame", RootFrameID: "claude-r-frame",
		AgentName: "OPERON", KernelKind: "r", Language: "r", Environment: "r", WorkspaceDir: workspace,
	})
	if err != nil {
		t.Fatalf("restart real R worker: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("stale R session library survived restart: %v", err)
	}
	restartState, err := restarted.Execute(ctx, `cat(exists("state_value"), "\n")`, "user")
	wantRestart := "[kernel restarted]\nThis cell ran on a fresh kernel process: the previous R kernel for environment 'r' was shut down. Variables, imports, and other in-memory state from earlier cells are gone; workspace files on disk are unaffected. Re-run setup before relying on earlier state."
	if err != nil || strings.TrimSpace(restartState.Stdout) != "FALSE" || restartState.Error != "" || !strings.HasPrefix(restartState.Stderr, wantRestart) {
		t.Fatalf("R namespace after restart=%#v err=%v", restartState, err)
	}
	secondState, err := restarted.Execute(ctx, `cat("second\n")`, "user")
	if err != nil || strings.TrimSpace(secondState.Stdout) != "second" || strings.Contains(secondState.Stderr, "[kernel restarted]") {
		t.Fatalf("R second restart response=%#v err=%v", secondState, err)
	}
}

func TestManagedRPackageOperationLogWireContract(t *testing.T) {
	environmentRoot := strings.TrimSpace(os.Getenv("SYNON_TEST_MANAGED_R_ENVS"))
	if environmentRoot == "" {
		t.Skip("SYNON_TEST_MANAGED_R_ENVS is not configured")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	workspace := t.TempDir()
	opLogPath := filepath.Join(workspace, ".operon_metadata.r.ndjson")
	manager := newLifecycleTestManager(t, Config{
		CondaHome: filepath.Join(t.TempDir(), "conda"), CondaEnvsPath: environmentRoot,
		RWorkerPath: filepath.Join(repositoryRoot, "assets", "optional", "kernels", "kernel_worker.R"),
		DefaultREnv: "r", DisableROperationLog: true,
		Environment: map[string]string{"OPERON_R_OPLOG_PATH": opLogPath},
	})
	worker, err := manager.StartSession(SessionSpec{
		KernelID: "claude-r-oplog", FrameID: "claude-r-oplog-frame", RootFrameID: "claude-r-oplog-frame",
		AgentName: "OPERON", KernelKind: "r", Language: "r", Environment: "r", WorkspaceDir: workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	response, err := worker.Execute(ctx, `
assign("original", function(pkgs, ...) invisible(pkgs), envir=environment(utils::install.packages))
assign(".operon_packages_installed", function(packages) TRUE, envir=.GlobalEnv)
invisible(install.packages(c("alpha", "beta")))
.operon_log_install("r_github_install", c("org/pkg@main"), "error", list(github_ref="org/pkg@main"))
cat("logged\n")
`, "user")
	if err != nil || response.Error != "" || strings.TrimSpace(response.Stdout) != "logged" {
		t.Fatalf("R op log response=%#v err=%v", response, err)
	}
	raw, err := os.ReadFile(opLogPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("operation log=%s", raw)
	}
	cran, err := decodeRPackageOperation([]byte(lines[0]), 0)
	if err != nil || cran.Operation != "r_cran_install" || cran.Result != "success" || strings.Join(cran.Packages, ",") != "alpha,beta" {
		t.Fatalf("CRAN operation=%#v err=%v", cran, err)
	}
	github, err := decodeRPackageOperation([]byte(lines[1]), 1)
	if err != nil || github.Operation != "r_github_install" || github.Result != "error" || strings.Join(github.GitHubRef, ",") != "org/pkg@main" {
		t.Fatalf("GitHub operation=%#v err=%v", github, err)
	}
}

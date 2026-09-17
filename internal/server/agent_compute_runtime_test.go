package server

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	workspace "synon-go/internal/persistence/workspace"
)

func TestAgentComputeAskUserProducesEvidenceBackedBilingualDecisionCards(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.UpsertSSHProvider(workspace.ComputeProviderInput{
		Name: "ssh:fixture", UserID: "owner-compute-card", Family: "ssh",
	}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{Workspace: store, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	access := workspace.KernelFrameAccess{
		UserID: "owner-compute-card",
		Frame:  workspace.Frame{ID: "standalone-compute-card", RootFrameID: "standalone-compute-card"},
	}
	for _, test := range []struct {
		name          string
		question      string
		wantHeader    string
		wantReadiness string
	}{
		{name: "English", question: "Which host account should be used?", wantHeader: "Compute host", wantReadiness: "The environment or service is configured; representative execution is still pending."},
		{name: "Chinese", question: "应该使用哪个主机账号？", wantHeader: "计算环境", wantReadiness: "所需环境或服务已配置；首次运行仍需验证。"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := server.executeAgentAskAboutCompute(
				context.Background(), access, agentruntime.ToolCall{ID: "ask-compute-" + strings.ToLower(test.name)},
				map[string]any{"provider": "fixture", "question": test.question},
			)
			questions, ok := mapValue(result)["questions"].([]askUserQuestion)
			if err != nil || !ok || len(questions) != 1 || questions[0].Header != test.wantHeader || len(questions[0].Options) != 2 {
				t.Fatalf("questions=%#v err=%v", questions, err)
			}
			if questions[0].Options[0].Metadata["readiness"] != test.wantReadiness ||
				strings.Contains(questions[0].Options[0].Description, "Resources") ||
				strings.Contains(questions[0].Options[0].Description, "资源") ||
				questions[0].Options[0].Metadata["decision_contract"] != "ask_user_option_v3" ||
				questions[0].Options[0].Metadata["recommended"] != true ||
				questions[0].Options[1].Metadata["recommended"] != false {
				t.Fatalf("decision options=%#v", questions[0].Options)
			}
			evidence, ok := questions[0].Options[0].Metadata["readiness_evidence"].([]string)
			if !ok || len(evidence) != 1 || evidence[0] != "compute-provider:fixture" {
				t.Fatalf("readiness evidence=%#v", questions[0].Options[0].Metadata["readiness_evidence"])
			}
		})
	}
}

func TestAgentComputeRuntimeListsExecutesAndCompletesOneDurableSSHJob(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the production SSH command path is exercised in the Linux target runtime")
	}
	root := t.TempDir()
	remoteHome := filepath.Join(root, "remote")
	if err := os.MkdirAll(remoteHome, 0o700); err != nil {
		t.Fatal(err)
	}
	remoteDataRoot := filepath.Join(remoteHome, "data")
	if err := os.MkdirAll(remoteDataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	remoteInput := filepath.Join(remoteDataRoot, "remote.txt")
	if err := os.WriteFile(remoteInput, []byte("remote-input"), 0o600); err != nil {
		t.Fatal(err)
	}
	ssh := filepath.Join(root, "ssh")
	if err := os.WriteFile(ssh, []byte("#!/usr/bin/env bash\nset -eu\ncommand=${!#}\nexport HOME=\"$SYNON_TEST_REMOTE_HOME\"\nexec bash -c \"$command\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	scp := filepath.Join(root, "scp")
	if err := os.WriteFile(scp, []byte("#!/usr/bin/env bash\nset -eu\nargs=(\"$@\")\nsrc=${args[${#args[@]}-2]}\ndst=${args[${#args[@]}-1]}\n[[ $src == *:* ]] && src=${src#*:}\n[[ $dst == *:* ]] && dst=${dst#*:}\nmkdir -p \"$(dirname \"$dst\")\"\ncp -- \"$src\" \"$dst\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sbatch"), []byte("#!/usr/bin/env bash\nset -eu\nscript=${!#}\nbash \"$script\" >/dev/null 2>&1 &\necho $!\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "squeue"), []byte("#!/usr/bin/env bash\nset -eu\nid=${!#}\nkill -0 \"$id\" 2>/dev/null && echo \"$id RUNNING\" || true\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scancel"), []byte("#!/usr/bin/env bash\nkill -TERM \"$1\" 2>/dev/null || true\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SYNON_TEST_REMOTE_HOME", remoteHome)

	store, _, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-compute", "project-compute", "frame-compute")
	if _, err := store.UpsertSSHProvider(workspace.ComputeProviderInput{
		Name: "ssh:fixture", UserID: "owner-compute", Family: "ssh", DetailsMD: "scheduler: none",
		DataRoots: []string{filepath.ToSlash(remoteDataRoot)},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertSSHProvider(workspace.ComputeProviderInput{
		Name: "ssh:slurm-fixture", UserID: "owner-compute", Family: "ssh", Scheduler: "slurm", DetailsMD: "scheduler: slurm",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetBYOCEnabled("modal", "owner-compute", true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateInferenceProvider(workspace.ComputeProviderInput{
		Name: "infer:fixture", UserID: "owner-compute", Family: "infer", Endpoint: "http://127.0.0.1:9444/v1", SkillName: "using-model-endpoint",
	}); err != nil {
		t.Fatal(err)
	}
	access, found, err := store.GetKernelFrameAccessContext(context.Background(), "frame-compute")
	if err != nil || !found {
		t.Fatalf("compute frame found=%t err=%v", found, err)
	}
	server := New(Options{
		Workspace: store, FileRoot: root,
		RuntimeAssetsDir: filepath.Join(repositoryRootForServerTest(t), "assets", "optional"),
	})
	identity := &agentKernelContext{access: access, workspaceDir: root}

	listed, err := server.executeAgentComputeTool(context.Background(), identity, agentruntime.ToolCall{ID: "list-compute"}, listComputeToolName, map[string]any{})
	listedProviders := anySliceValue(mapValue(listed)["providers"])
	if err != nil || len(listedProviders) != 2 {
		t.Fatalf("list_compute=%#v err=%v", listed, err)
	}
	if provider, found, err := server.kernelComputeProvider(access, "byoc:modal"); err != nil || found || provider.Name != "" {
		t.Fatalf("unavailable BYOC provider leaked into agent execution: provider=%#v found=%t err=%v", provider, found, err)
	}
	if provider, found, err := server.kernelComputeProvider(access, "infer:fixture"); err != nil || found || provider.Name != "" {
		t.Fatalf("unavailable inference provider leaked into agent execution: provider=%#v found=%t err=%v", provider, found, err)
	}
	direct, err := server.executeAgentComputeTool(context.Background(), identity, agentruntime.ToolCall{ID: "ssh-direct"}, sshComputeToolName, map[string]any{
		"provider": "fixture", "intent": "Inspect fixture host", "command": "printf ok", "timeout_seconds": 5,
	})
	if err != nil || stringValue(mapValue(direct)["stdout"]) != "ok" || numberValue(mapValue(direct)["exit_code"]) != 0 {
		t.Fatalf("ssh=%#v err=%v", direct, err)
	}

	submitted, err := server.executeAgentComputeTool(context.Background(), identity, agentruntime.ToolCall{ID: "submit-fixture"}, submitComputeJobToolName, map[string]any{
		"provider": "fixture", "intent": "Run fixture job", "command": "mkdir -p out; cat remote.txt > out/result.txt; printf parked > out/parked.bin; printf job", "timeout_seconds": 5,
		"inputs":  []any{map[string]any{"src": "ssh://fixture" + filepath.ToSlash(remoteInput), "dst": "remote.txt"}},
		"outputs": []any{map[string]any{"glob": "out/**/*.txt", "visibility": "featured"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	jobID := stringValue(mapValue(submitted)["job_id"])
	if jobID == "" {
		t.Fatalf("submit_job=%#v", submitted)
	}
	waited, err := server.waitAgentComputeJob(context.Background(), access, map[string]any{
		"job_id": jobID, "provider": "fixture", "timeout_seconds": 5,
	})
	if err != nil || stringValue(mapValue(waited)["state"]) != workspace.ComputeJobDone {
		t.Fatalf("wait_job=%#v err=%v", waited, err)
	}
	if len(anySliceValue(mapValue(waited)["featured_files"])) != 1 || len(anySliceValue(mapValue(waited)["output_files"])) < 3 {
		t.Fatalf("SSH harvested result=%#v", waited)
	}
	if len(anySliceValue(mapValue(waited)["left_on_remote"])) != 1 {
		t.Fatalf("SSH unselected remote output=%#v", waited)
	}
	harvestedRemoteInput, err := os.ReadFile(filepath.Join(root, "hpc", jobID, "out", "result.txt"))
	if err != nil || string(harvestedRemoteInput) != "remote-input" {
		t.Fatalf("SSH remote input harvest=%q err=%v", harvestedRemoteInput, err)
	}
	closed, err := server.handleKernelComputeHostCall(
		context.Background(), access, root, "close-ssh-fixture", "host.compute.close", []any{"fixture", nil}, nil,
	)
	if err != nil || numberValue(mapValue(closed)["removed_workdirs"]) != 1 {
		t.Fatalf("SSH close=%#v err=%v", closed, err)
	}
	recoveryJobID := "job-ssh-recovery"
	recoveryStage, recoveryArchive, err := server.stageAgentBYOCJob(recoveryJobID, root, map[string]any{
		"command": "mkdir -p out; printf recovered > out/recovered.txt",
		"outputs": []any{"out/recovered.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	recoveryFrame, recoveryRoot, recoveryOrigin := "frame-compute", "frame-compute", "ssh-recovery-origin"
	recoveryHardware := map[string]any{
		"managed_ssh": true, "workspace_dir": root,
		"remote_workdir": filepath.ToSlash(filepath.Join(remoteHome, ".synon-biomed", "jobs", recoveryJobID)),
		"staging_dir":    recoveryStage, "archive_sha256": recoveryArchive.SHA256, "archive_bytes": recoveryArchive.Bytes,
		"outputs": []any{"out/recovered.txt"}, "timeout_seconds": 30,
	}
	recoveryJob, err := store.CreateComputeJob("owner-compute", workspace.ComputeJob{
		JobID: recoveryJobID, ProjectID: "project-compute", Provider: "ssh:fixture",
		Environment: "remote", TierType: "remote", FrameID: &recoveryFrame, RootFrameID: &recoveryRoot,
		OriginToolUseID: &recoveryOrigin, Intent: "Recover SSH staging", HardwareDetails: recoveryHardware,
		ProviderFamily: "ssh", ProviderLabel: "fixture", SupportsTail: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	recoveryOwned := workspace.OwnedComputeJob{OwnerUserID: "owner-compute", Job: recoveryJob}
	recoveryDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(recoveryDeadline) {
		if server.reconcileAgentSSHJob(context.Background(), recoveryOwned) {
			break
		}
		current, found, getErr := store.GetComputeJob("owner-compute", recoveryJobID)
		if getErr != nil || !found {
			t.Fatalf("SSH recovery state found=%t err=%v", found, getErr)
		}
		recoveryOwned.Job = current
		time.Sleep(50 * time.Millisecond)
	}
	recovered, found, err := store.GetComputeJob("owner-compute", recoveryJobID)
	if err != nil || !found || recovered.State != workspace.ComputeJobDone {
		t.Fatalf("recovered SSH job=%#v found=%t err=%v", recovered, found, err)
	}
	if _, err := os.Stat(recoveryStage); !os.IsNotExist(err) {
		t.Fatalf("SSH recovery stage was not removed: %v", err)
	}
	slurmSubmitted, err := server.executeAgentComputeTool(context.Background(), identity, agentruntime.ToolCall{ID: "submit-slurm-fixture"}, submitComputeJobToolName, map[string]any{
		"provider": "slurm-fixture", "intent": "Run fixture Slurm job",
		"command":         "#!/bin/bash\n#SBATCH --time=00:01:00\nmkdir -p out; printf slurm > out/slurm.txt",
		"timeout_seconds": 30, "outputs": []any{"out/slurm.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	slurmJobID := stringValue(mapValue(slurmSubmitted)["job_id"])
	slurmWaited, err := server.waitAgentComputeJob(context.Background(), access, map[string]any{
		"job_id": slurmJobID, "provider": "slurm-fixture", "timeout_seconds": 5,
	})
	if err != nil || stringValue(mapValue(slurmWaited)["state"]) != workspace.ComputeJobDone {
		t.Fatalf("Slurm wait=%#v err=%v", slurmWaited, err)
	}
	log, found, err := store.GetComputeJobLog("owner-compute", jobID, "combined", 1024)
	if err != nil || !found || log.Text == "" {
		t.Fatalf("compute log=%#v found=%t err=%v", log, found, err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		server.computeRunsMu.Lock()
		_, running := server.computeRuns[jobID]
		server.computeRunsMu.Unlock()
		if !running {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("completed compute process remained registered")
}

func TestAgentSSHSchedulerContractHoistsOnlyLeadingSlurmDirectives(t *testing.T) {
	provider := workspace.ComputeProvider{Scheduler: "slurm"}
	scheduler, directives, err := agentSSHSchedulerContract(provider, map[string]any{
		"command": "#!/bin/bash\n#SBATCH --gres=gpu:1\n#SBATCH --time=00:10:00\npython run.py",
	})
	if err != nil || scheduler != "slurm" || len(directives) != 2 {
		t.Fatalf("scheduler=%s directives=%v err=%v", scheduler, directives, err)
	}
	if _, _, err := agentSSHSchedulerContract(provider, map[string]any{
		"command": "python run.py\n#SBATCH --gres=gpu:1",
	}); err == nil {
		t.Fatal("late Slurm directive was accepted")
	}
}

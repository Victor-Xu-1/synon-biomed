//go:build linux && amd64

package kernel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestSynonKernelConfinementPolicyIdentity(t *testing.T) {
	var raw bytes.Buffer
	for _, instruction := range synonKernelFilter {
		if err := binary.Write(&raw, binary.LittleEndian, instruction); err != nil {
			t.Fatal(err)
		}
	}
	digest := sha256.Sum256(raw.Bytes())
	if len(raw.Bytes()) != 296 || hex.EncodeToString(digest[:]) != synonKernelPolicySHA256 {
		t.Fatalf("kernel filter bytes=%d sha256=%x", len(raw.Bytes()), digest)
	}
	evidence := platformConfinementEvidence()
	if !evidence.Available || evidence.Mode != "synon-bwrap-unix-block-v1" ||
		evidence.PolicySHA256 != synonKernelPolicySHA256 || evidence.Reason != "" {
		t.Fatalf("kernel confinement evidence=%#v", evidence)
	}
}

func TestKernelConfinementReadinessExecutesFullBoundary(t *testing.T) {
	evidence := probePlatformConfinement()
	if !evidence.Available || evidence.Mode != "synon-bwrap-unix-block-v1" ||
		evidence.PolicySHA256 != synonKernelPolicySHA256 || evidence.Reason != "" {
		t.Fatalf("probed kernel confinement evidence=%#v", evidence)
	}
	manager := NewManager(Config{})
	if cached := manager.ConfinementEvidence(); cached != evidence {
		t.Fatalf("cached kernel confinement evidence=%#v want=%#v", cached, evidence)
	}
}

func TestKernelConfinementReadinessRejectsBrokenBubblewrap(t *testing.T) {
	directory := t.TempDir()
	bubblewrap := filepath.Join(directory, "bwrap")
	if err := os.WriteFile(bubblewrap, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	evidence := probePlatformConfinement()
	if evidence.Available || evidence.Mode != "unavailable" || evidence.PolicySHA256 != "" ||
		evidence.Reason != "kernel confinement probe failed" {
		t.Fatalf("broken bubblewrap evidence=%#v", evidence)
	}
}

func TestKernelConfinementSealsSyntheticEtcAfterPopulatingMountTargets(t *testing.T) {
	workspace := t.TempDir()
	request := confinedWorkerRequest{
		WorkspaceDir: workspace,
		WorkspaceFD:  openKernelTestDirectoryFD(t, workspace),
		Executable:   "/usr/bin/python3",
	}
	arguments, err := kernelBubblewrapArguments(request, "/opt/synon/kernel-helper", 3, 4)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(arguments, "\x00")
	populate := "--perms\x000755\x00--tmpfs\x00/etc"
	seal := "--chmod\x000555\x00/etc"
	populateIndex := strings.Index(joined, populate)
	passwdIndex := strings.Index(joined, "--ro-bind\x00/etc/passwd\x00/etc/passwd")
	sealIndex := strings.Index(joined, seal)
	if populateIndex < 0 || passwdIndex < 0 || sealIndex < 0 ||
		populateIndex >= passwdIndex || passwdIndex >= sealIndex {
		t.Fatalf("synthetic /etc population order is unsafe: %#v", arguments)
	}
	if strings.Contains(joined, "--perms\x000555\x00--tmpfs\x00/etc") {
		t.Fatalf("synthetic /etc was sealed before bind targets were populated: %#v", arguments)
	}
}

func TestManagerConfinesKernelFilesystemToWorkspaceAndRuntime(t *testing.T) {
	outside := t.TempDir()
	secret := filepath.Join(outside, "must-not-read.txt")
	escape := filepath.Join(outside, "must-not-write.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	manager := newLifecycleTestManager(t, Config{})
	worker, err := manager.StartSession(SessionSpec{
		KernelID: "kernel-filesystem", FrameID: "frame-filesystem", RootFrameID: "frame-filesystem",
		AgentName: "OPERON", Language: "python", Environment: "python", WorkspaceDir: workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response, err := worker.Execute(ctx, fmt.Sprintf(`
import json, os
secret = %q
escape = %q
result = {"secret_visible": os.path.exists(secret), "write_outside": "allowed"}
try:
    open(escape, "w").write("escaped")
except OSError:
    result["write_outside"] = "denied"
open("workspace-ok.txt", "w").write("ok")
result["workspace"] = open("workspace-ok.txt").read()
result["system"] = os.path.exists("/usr")
print(json.dumps(result, sort_keys=True))
`, secret, escape), "user")
	if err != nil || response.Error != "" {
		t.Fatalf("filesystem confinement response=%#v err=%v", response, err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(response.Stdout)), &result); err != nil {
		t.Fatal(err)
	}
	if result["secret_visible"] != false || result["write_outside"] != "denied" ||
		result["workspace"] != "ok" || result["system"] != true {
		t.Fatalf("filesystem confinement result=%#v", result)
	}
	if _, err := os.Stat(escape); !os.IsNotExist(err) {
		t.Fatalf("kernel wrote outside workspace: %v", err)
	}
}

func TestManagerMountsMaterializedSkillRuntimeReadOnly(t *testing.T) {
	workspace := t.TempDir()
	skillRuntime := filepath.Join(workspace, ".synon", "runtime", "skills", "reviewed-skill", "scripts")
	if err := os.MkdirAll(skillRuntime, 0o700); err != nil {
		t.Fatal(err)
	}
	asset := filepath.Join(skillRuntime, "run.py")
	if err := os.WriteFile(asset, []byte("trusted\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	manager := newLifecycleTestManager(t, Config{})
	worker, err := manager.StartSession(SessionSpec{
		KernelID: "kernel-skill-readonly", FrameID: "frame-skill-readonly", RootFrameID: "frame-skill-readonly",
		AgentName: "OPERON", Language: "python", Environment: "python", WorkspaceDir: workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response, err := worker.Execute(ctx, fmt.Sprintf(`
import json, os
asset = %q
result = {"asset_before": open(asset).read(), "asset_write": "allowed"}
try:
    os.chmod(asset, 0o700)
    open(asset, "w").write("modified")
except OSError:
    result["asset_write"] = "denied"
open("ordinary-output.txt", "w").write("ok")
result["ordinary_output"] = open("ordinary-output.txt").read()
print(json.dumps(result, sort_keys=True))
`, asset), "user")
	if err != nil || response.Error != "" {
		t.Fatalf("Skill runtime confinement response=%#v err=%v", response, err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(response.Stdout)), &result); err != nil {
		t.Fatal(err)
	}
	if result["asset_before"] != "trusted\n" || result["asset_write"] != "denied" || result["ordinary_output"] != "ok" {
		t.Fatalf("Skill runtime confinement result=%#v", result)
	}
	content, err := os.ReadFile(asset)
	if err != nil || string(content) != "trusted\n" {
		t.Fatalf("Skill runtime asset changed content=%q err=%v", content, err)
	}
}

func TestManagerExposesDetectedWSLAcceleratorDevice(t *testing.T) {
	info, err := os.Lstat("/dev/dxg")
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("WSL accelerator device is unavailable")
	}
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		t.Fatalf("WSL accelerator device is invalid: info=%#v err=%v", info, err)
	}
	workspace := t.TempDir()
	manager := newLifecycleTestManager(t, Config{})
	worker, err := manager.StartSession(SessionSpec{
		KernelID: "kernel-wsl-accelerator", FrameID: "frame-wsl-accelerator", RootFrameID: "frame-wsl-accelerator",
		AgentName: "OPERON", Language: "python", Environment: "python", WorkspaceDir: workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response, err := worker.Execute(ctx, `
import json, os, stat
device = os.stat("/dev/dxg")
print(json.dumps({"visible": stat.S_ISCHR(device.st_mode)}))
`, "user")
	if err != nil || response.Error != "" {
		t.Fatalf("accelerator confinement response=%#v err=%v", response, err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(response.Stdout)), &result); err != nil {
		t.Fatal(err)
	}
	if result["visible"] != true {
		t.Fatalf("accelerator device result=%#v", result)
	}
}

func TestConfinedWorkerPreservesCUDAVisibilityWhenAvailable(t *testing.T) {
	python := strings.TrimSpace(os.Getenv("SYNON_TEST_CUDA_PYTHON"))
	if python == "" {
		t.Skip("set SYNON_TEST_CUDA_PYTHON to a CUDA-enabled managed Python")
	}
	if !filepath.IsAbs(python) {
		t.Fatal("SYNON_TEST_CUDA_PYTHON must be absolute")
	}
	direct := exec.Command(python, "-c", "import torch; assert torch.cuda.is_available()")
	if output, err := direct.CombinedOutput(); err != nil {
		t.Skipf("selected Python has no directly usable CUDA runtime: %v: %s", err, output)
	}
	workspace := t.TempDir()
	home := t.TempDir()
	prefix := filepath.Dir(filepath.Dir(python))
	command, err := newConfinedWorkerCommand(
		workspace,
		python,
		[]string{"-c", "import json, torch; x=torch.tensor([1.0], device='cuda'); print(json.dumps({'available': torch.cuda.is_available(), 'value': x.item(), 'name': torch.cuda.get_device_name(0)}))"},
		[]string{"HOME=" + home, "PATH=" + filepath.Join(prefix, "bin") + ":/usr/bin:/bin", "CONDA_PREFIX=" + prefix},
		nil,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer closeKernelCommandExtraFiles(command)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("confined CUDA witness failed: %v\n%s", err, output)
	}
	var result map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output), &result); err != nil {
		t.Fatalf("decode confined CUDA witness %q: %v", output, err)
	}
	if result["available"] != true || result["value"] != float64(1) || strings.TrimSpace(fmt.Sprint(result["name"])) == "" {
		t.Fatalf("confined CUDA witness=%#v", result)
	}
}

func TestConfinedWorkerMountsExternalPythonStandardLibrary(t *testing.T) {
	python := strings.TrimSpace(os.Getenv("SYNON_TEST_EXTERNAL_PYTHON"))
	if python == "" {
		t.Skip("set SYNON_TEST_EXTERNAL_PYTHON to a non-system Python distribution")
	}
	resolved, err := filepath.EvalSymlinks(python)
	if err != nil || !filepath.IsAbs(resolved) {
		t.Fatalf("resolve external Python %q: %v", python, err)
	}
	prefix := kernelPythonRuntimePrefix(resolved)
	if prefix == "" {
		t.Fatalf("external Python prefix was not discovered for %q", resolved)
	}
	workspace := t.TempDir()
	command, err := newConfinedWorkerCommand(
		workspace,
		resolved,
		[]string{"-c", "import encodings, json, sys; print(json.dumps({'prefix': sys.prefix, 'encoding': encodings.__name__}))"},
		[]string{"HOME=" + t.TempDir(), "PATH=" + filepath.Dir(resolved) + ":/usr/bin:/bin"},
		nil,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer closeKernelCommandExtraFiles(command)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("external Python confinement failed: %v\n%s", err, output)
	}
	var result map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output), &result); err != nil {
		t.Fatalf("decode external Python witness %q: %v", output, err)
	}
	if result["prefix"] != prefix || result["encoding"] != "encodings" {
		t.Fatalf("external Python witness=%#v prefix=%q", result, prefix)
	}
}

func TestKernelPIDNamespaceKillsSetsidDescendant(t *testing.T) {
	workspace := t.TempDir()
	marker := filepath.Join(workspace, "escaped-descendant.txt")
	manager := newLifecycleTestManager(t, Config{})
	worker, err := manager.StartSession(SessionSpec{
		KernelID: "kernel-descendant", FrameID: "frame-descendant", RootFrameID: "frame-descendant",
		AgentName: "OPERON", Language: "python", Environment: "python", WorkspaceDir: workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response, err := worker.Execute(ctx, fmt.Sprintf(`
import subprocess
subprocess.Popen(["setsid", "sh", "-c", %q])
print("spawned")
`, "sleep 1; printf survived > "+marker), "user")
	if err != nil || strings.TrimSpace(response.Stdout) != "spawned" || response.Error != "" {
		t.Fatalf("setsid spawn response=%#v err=%v", response, err)
	}
	if err := manager.CloseKernel(ctx, "kernel-descendant"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("setsid descendant survived kernel close: %v", err)
	}
}

func TestManagerAppliesWorkspaceKernelConfinementToRealWorker(t *testing.T) {
	manager := newLifecycleTestManager(t, Config{Environment: map[string]string{
		kernelConfinementHelperEnv:  "forged",
		kernelConfinementPayloadEnv: "forged",
	}})
	worker, err := manager.StartSession(SessionSpec{
		KernelID: "kernel-confinement", FrameID: "frame-confinement", RootFrameID: "frame-confinement",
		AgentName: "OPERON", Language: "python", Environment: "python", WorkspaceDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response, err := worker.Execute(ctx, `
import ctypes, errno, json, os, socket
result = {}
for name, family in (("inet", socket.AF_INET), ("unix", socket.AF_UNIX), ("netlink", socket.AF_NETLINK)):
    try:
        value = socket.socket(family, socket.SOCK_STREAM)
        value.close()
        result[name] = "allowed"
    except OSError as exc:
        result[name] = exc.errno
libc = ctypes.CDLL(None, use_errno=True)
result["ptrace"] = [libc.ptrace(0, 0, None, None), ctypes.get_errno()]
result["dumpable"] = libc.prctl(3, 0, 0, 0, 0)
network = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
network.settimeout(0.2)
try:
    network.connect(("1.1.1.1", 53))
    result["network"] = "allowed"
except OSError:
    result["network"] = "isolated"
finally:
    network.close()
result["helper_env_absent"] = (
    "SYNON_INTERNAL_KERNEL_CONFINEMENT_HELPER" not in os.environ
    and "SYNON_INTERNAL_KERNEL_CONFINEMENT_PAYLOAD" not in os.environ
)
print(json.dumps(result, sort_keys=True))
`, "user")
	if err != nil || response.Error != "" {
		t.Fatalf("confined execution response=%#v err=%v", response, err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(response.Stdout)), &result); err != nil {
		t.Fatalf("decode confinement result %q: %v", response.Stdout, err)
	}
	ptrace, ptraceOK := result["ptrace"].([]any)
	if result["inet"] != "allowed" || result["unix"] != float64(errnoEPERM) ||
		result["netlink"] != float64(errnoEPERM) || !ptraceOK || len(ptrace) != 2 ||
		ptrace[0] != float64(-1) || ptrace[1] != float64(errnoEPERM) || result["dumpable"] != float64(0) ||
		result["network"] != "isolated" || result["helper_env_absent"] != true {
		t.Fatalf("confinement result=%#v", result)
	}
}

func TestConfinementTargetEnvironmentRemovesHelperAuthority(t *testing.T) {
	clean, err := confinementTargetEnvironment([]string{
		"PATH=/usr/bin", kernelConfinementHelperEnv + "=forged",
		kernelConfinementPayloadEnv + "=forged", "LANG=C.UTF-8",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(clean, "\n") != "PATH=/usr/bin\nLANG=C.UTF-8" {
		t.Fatalf("clean environment=%q", clean)
	}
	for _, malformed := range [][]string{{"MISSING_EQUALS"}, {"=empty"}, {"BAD\x00KEY=value"}} {
		if _, err := confinementTargetEnvironment(malformed); err == nil {
			t.Fatalf("malformed environment was accepted: %#v", malformed)
		}
	}
}

func TestKernelConfinementRejectsProtectedAndOverlappingMounts(t *testing.T) {
	workspace := t.TempDir()
	allowed := t.TempDir()
	conda := t.TempDir()
	home := filepath.Dir(allowed)
	request := confinedWorkerRequest{
		WorkspaceDir: workspace, Executable: "/usr/bin/python3", Arguments: []string{"/opt/kernel/worker.py"},
		WorkspaceFD: openKernelTestDirectoryFD(t, workspace), Environment: []string{"HOME=" + home, "CONDA_PREFIX=" + conda},
	}
	request.Mounts = []confinedWorkerMount{{Path: allowed, FD: openKernelTestDirectoryFD(t, allowed)}}
	if _, err := kernelBubblewrapArguments(request, "/opt/synon/kernel-helper", 3, 4); err != nil {
		t.Fatalf("allowed mount rejected: %v", err)
	}
	for _, path := range []string{"/", "/usr", "/usr/local", "/etc", "/proc", "/dev", "/sys", "/run", "/opt", conda, workspace} {
		request.Mounts = []confinedWorkerMount{{Path: path, FD: openKernelTestDirectoryFD(t, path)}}
		if _, err := kernelBubblewrapArguments(request, "/opt/synon/kernel-helper", 3, 4); err == nil || !strings.Contains(err.Error(), "protected path") {
			t.Fatalf("protected mount %q err=%v", path, err)
		}
	}
	protected := t.TempDir()
	protectedChild := filepath.Join(protected, "secrets")
	if err := os.MkdirAll(protectedChild, 0o700); err != nil {
		t.Fatal(err)
	}
	request.Protected = []string{protected}
	request.Mounts = []confinedWorkerMount{{Path: protectedChild, FD: openKernelTestDirectoryFD(t, protectedChild)}}
	if _, err := kernelBubblewrapArguments(request, "/opt/synon/kernel-helper", 3, 4); err == nil || !strings.Contains(err.Error(), "protected path") {
		t.Fatalf("application data mount err=%v", err)
	}
}

func TestKernelConfinementMountDescriptorSurvivesPathReplacement(t *testing.T) {
	workspace := t.TempDir()
	mount := t.TempDir()
	originalMarker := filepath.Join(mount, "original.txt")
	if err := os.WriteFile(originalMarker, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	fd := openKernelTestDirectoryFD(t, mount)
	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		t.Fatal(err)
	}
	moved := mount + "-moved"
	if err := os.Rename(mount, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(mount, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mount, "replacement.txt"), []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	var frozen unix.Stat_t
	if err := unix.Fstat(fd, &frozen); err != nil {
		t.Fatal(err)
	}
	var replacement unix.Stat_t
	if err := unix.Stat(mount, &replacement); err != nil {
		t.Fatal(err)
	}
	if before.Ino != frozen.Ino || frozen.Ino == replacement.Ino {
		t.Fatalf("descriptor before=%d frozen=%d replacement=%d", before.Ino, frozen.Ino, replacement.Ino)
	}
	request := confinedWorkerRequest{
		WorkspaceDir: workspace, WorkspaceFD: openKernelTestDirectoryFD(t, workspace),
		Executable: "/usr/bin/python3", Mounts: []confinedWorkerMount{{Path: mount, FD: fd}},
	}
	arguments, err := kernelBubblewrapArguments(request, "/opt/synon/kernel-helper", 3, 4)
	if err != nil {
		t.Fatal(err)
	}
	want := "--ro-bind-fd\x00" + strconv.Itoa(fd) + "\x00" + mount
	if !strings.Contains(strings.Join(arguments, "\x00"), want) {
		t.Fatalf("frozen mount arguments=%#v", arguments)
	}
}

func TestKernelConfinementOverlaysImmutableWorkspaceChildAfterWritableRoot(t *testing.T) {
	workspace := t.TempDir()
	output := filepath.Join(workspace, "results")
	if err := os.MkdirAll(output, 0o700); err != nil {
		t.Fatal(err)
	}
	workspaceFD := openKernelTestDirectoryFD(t, workspace)
	outputFD := openKernelTestDirectoryFD(t, output)
	request := confinedWorkerRequest{
		WorkspaceDir: workspace, WorkspaceFD: workspaceFD, Executable: "/usr/bin/python3",
		Mounts: []confinedWorkerMount{{Path: output, FD: outputFD, Trusted: true}},
	}
	arguments, err := kernelBubblewrapArguments(request, "/opt/synon/kernel-helper", 3, 4)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(arguments, "\x00")
	workspaceBind := strings.Index(joined, "--bind-fd\x00"+strconv.Itoa(workspaceFD)+"\x00"+workspace)
	outputOverlay := strings.Index(joined, "--ro-bind-fd\x00"+strconv.Itoa(outputFD)+"\x00"+output)
	if workspaceBind < 0 || outputOverlay < 0 || outputOverlay < workspaceBind {
		t.Fatalf("immutable workspace overlay order=%#v", arguments)
	}
}

func TestKernelConfinementPreventsPythonMutationOfImmutableWorkspaceOutput(t *testing.T) {
	workspace := t.TempDir()
	output := filepath.Join(workspace, "results")
	if err := os.MkdirAll(output, 0o700); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(output, "report.md")
	if err := os.WriteFile(report, []byte("validated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code := fmt.Sprintf(`from pathlib import Path
p=Path(%q)
blocked=False
try:
 p.write_text('mutated')
except OSError:
 blocked=True
print(blocked, p.read_text().strip())`, report)
	command, err := newConfinedWorkerCommand(
		workspace, "/usr/bin/python3", []string{"-c", code},
		[]string{"HOME=" + t.TempDir(), "PATH=/usr/bin"},
		[]WorkerMount{TrustedReadOnlyDirectoryMount(output)}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	command.Dir, command.Stdout, command.Stderr = "/", &stdout, &stderr
	configureWorkerProcess(command)
	if err := command.Start(); err != nil {
		closeKernelCommandExtraFiles(command)
		t.Fatal(err)
	}
	closeKernelCommandExtraFiles(command)
	if err := command.Wait(); err != nil {
		t.Fatalf("bubblewrap: %v: %s", err, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "True validated" {
		t.Fatalf("immutable output stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestKernelConfinementUsesFrozenMountInRealBubblewrapAfterPathReplacement(t *testing.T) {
	workspace := t.TempDir()
	mount := t.TempDir()
	if err := os.WriteFile(filepath.Join(mount, "original.txt"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	code := fmt.Sprintf(`from pathlib import Path; root=Path(%q); print((root/'original.txt').read_text()); print((root/'replacement.txt').exists())`, mount)
	command, err := newConfinedWorkerCommand(
		workspace, "/usr/bin/python3", []string{"-c", code},
		[]string{"HOME=" + t.TempDir(), "PATH=/usr/bin"}, []WorkerMount{{Path: mount}}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	moved := mount + "-moved"
	if err := os.Rename(mount, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(mount, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mount, "replacement.txt"), []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	command.Dir, command.Stdout, command.Stderr = "/", &stdout, &stderr
	configureWorkerProcess(command)
	if err := command.Start(); err != nil {
		closeKernelCommandExtraFiles(command)
		t.Fatal(err)
	}
	closeKernelCommandExtraFiles(command)
	if err := command.Wait(); err != nil {
		t.Fatalf("bubblewrap: %v: %s", err, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "original\nFalse" {
		t.Fatalf("frozen mount stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestKernelConfinementAllowsOnlyFrozenROperationLogFileInsideReadOnlyEnvironment(t *testing.T) {
	workspace := t.TempDir()
	prefix := t.TempDir()
	path, frozen, err := secureROperationLog(prefix)
	if err != nil {
		t.Fatal(err)
	}
	code := fmt.Sprintf(`from pathlib import Path; path=Path(%q); path.open('a').write('logged\n'); blocked=False
try:
 (path.parent/'blocked.txt').write_text('blocked')
except OSError:
 blocked=True
print(blocked)`, path)
	command, err := newConfinedWorkerCommand(
		workspace, "/usr/bin/python3", []string{"-c", code},
		[]string{"HOME=" + t.TempDir(), "PATH=/usr/bin", "CONDA_PREFIX=" + prefix},
		[]WorkerMount{{Path: path, Writable: true, regular: true, trusted: true, frozen: frozen}}, nil,
	)
	if err != nil {
		_ = frozen.Close()
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	command.Dir, command.Stdout, command.Stderr = "/", &stdout, &stderr
	configureWorkerProcess(command)
	if err := command.Start(); err != nil {
		closeKernelCommandExtraFiles(command)
		t.Fatal(err)
	}
	closeKernelCommandExtraFiles(command)
	if err := command.Wait(); err != nil {
		t.Fatalf("bubblewrap: %v: %s", err, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "True" {
		t.Fatalf("write isolation stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "logged\n" {
		t.Fatalf("operation log=%q err=%v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(prefix, "blocked.txt")); !os.IsNotExist(err) {
		t.Fatalf("environment sibling write escaped: %v", err)
	}
}

func TestConfinedWorkerCommandOwnsFrozenExtraFilesUntilStart(t *testing.T) {
	workspace := t.TempDir()
	mount := t.TempDir()
	command, err := newConfinedWorkerCommand(
		workspace, "/usr/bin/python3", []string{"-c", "pass"},
		[]string{"HOME=" + t.TempDir(), "PATH=/usr/bin"}, []WorkerMount{{Path: mount}}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(command.ExtraFiles) != 2 {
		t.Fatalf("extra files=%d", len(command.ExtraFiles))
	}
	encoded := environmentValue(command.Env, kernelConfinementPayloadEnv)
	raw, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	var request confinedWorkerRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	if request.WorkspaceFD != 3 || len(request.Mounts) != 1 || request.Mounts[0].FD != 4 {
		t.Fatalf("request=%#v", request)
	}
	files := append([]*os.File(nil), command.ExtraFiles...)
	closeKernelCommandExtraFiles(command)
	if len(command.ExtraFiles) != 0 {
		t.Fatalf("extra files retained=%d", len(command.ExtraFiles))
	}
	for _, file := range files {
		if _, err := file.Stat(); err == nil {
			t.Fatalf("descriptor %s remains open", file.Name())
		}
	}
}

func TestConfinedWorkerPreservesAuditedAuxiliarySocketWithoutAllowingNewUnixSockets(t *testing.T) {
	workspace := t.TempDir()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	parent := os.NewFile(uintptr(pair[0]), "provider-parent")
	child := os.NewFile(uintptr(pair[1]), "provider-child")
	defer parent.Close()
	command, err := newConfinedWorkerCommandWithAuxiliary(
		workspace, os.Args[0], []string{"-test.run=^TestConfinedWorkerAuxiliarySocketHelper$"},
		[]string{"HOME=" + workspace, "PATH=/usr/bin:/bin", "GO_WANT_PROVIDER_AUX_HELPER=1", "PROVIDER_AUX_FD=4"},
		nil, nil, []*os.File{child},
	)
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		closeKernelCommandExtraFiles(command)
		t.Fatal(err)
	}
	closeKernelCommandExtraFiles(command)
	if _, err := parent.Write([]byte("fd-ok")); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(stdout)
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(output)) != "fd-ok" {
		t.Fatalf("auxiliary socket output=%q", output)
	}
}

func TestConfinedWorkerAuxiliarySocketHelper(t *testing.T) {
	if os.Getenv("GO_WANT_PROVIDER_AUX_HELPER") != "1" {
		return
	}
	fd, err := strconv.Atoi(os.Getenv("PROVIDER_AUX_FD"))
	if err != nil {
		os.Exit(91)
	}
	file := os.NewFile(uintptr(fd), "provider-aux")
	if file == nil {
		os.Exit(92)
	}
	payload := make([]byte, len("fd-ok"))
	if _, err := io.ReadFull(file, payload); err != nil {
		os.Exit(93)
	}
	if descriptor, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0); err != unix.EPERM {
		if descriptor >= 0 {
			_ = unix.Close(descriptor)
		}
		os.Exit(94)
	}
	_, _ = os.Stdout.Write(payload)
	os.Exit(0)
}

func TestCopyProviderOperationFileRejectsSymlinkAndNonPrivateFile(t *testing.T) {
	stage := t.TempDir()
	out := filepath.Join(stage, "out.bin")
	if err := os.WriteFile(out, []byte("provider-output"), 0o600); err != nil {
		t.Fatal(err)
	}
	var copied bytes.Buffer
	if written, err := CopyProviderOperationFile(stage, "out.bin", &copied, 1024); err != nil || written != int64(len("provider-output")) || copied.String() != "provider-output" {
		t.Fatalf("provider output written=%d content=%q err=%v", written, copied.String(), err)
	}
	if err := os.Chmod(out, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CopyProviderOperationFile(stage, "out.bin", io.Discard, 1024); err == nil {
		t.Fatal("provider output accepted a world-readable file")
	}
	if err := os.Remove(out); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, out); err != nil {
		t.Fatal(err)
	}
	if _, err := CopyProviderOperationFile(stage, "out.bin", io.Discard, 1024); err == nil {
		t.Fatal("provider output accepted a symlink")
	}
}

func openKernelTestDirectoryFD(t *testing.T, path string) int {
	t.Helper()
	fd, err := unix.Open(path, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	return fd
}

func TestKernelPIDNamespaceKillsSetsidDescendantOnTimeoutAndRestart(t *testing.T) {
	for _, operation := range []string{"timeout", "restart"} {
		t.Run(operation, func(t *testing.T) {
			workspace := t.TempDir()
			marker := filepath.Join(workspace, operation+"-descendant.txt")
			manager := newLifecycleTestManager(t, Config{})
			spec := SessionSpec{
				KernelID: "kernel-" + operation, FrameID: "frame-" + operation, RootFrameID: "frame-" + operation,
				AgentName: "OPERON", KernelKind: "analysis", Language: "python", Environment: "python", WorkspaceDir: workspace,
			}
			worker, err := manager.StartSession(spec)
			if err != nil {
				t.Fatal(err)
			}
			code := fmt.Sprintf(`
import subprocess, time
subprocess.Popen(["setsid", "sh", "-c", %q])
print("spawned")
%s
`, "sleep 1; printf survived > "+marker, map[string]string{"timeout": "time.sleep(10)", "restart": ""}[operation])
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if operation == "timeout" {
				handle, err := manager.Submit(SubmitRequest{
					KernelID: spec.KernelID, FrameID: spec.FrameID, KernelKind: spec.KernelKind,
					Language: spec.Language, Environment: spec.Environment, ExecID: "exec-timeout", ToolUseID: "tool-timeout",
					Code: code, Timeout: 100 * time.Millisecond, InterruptGrace: 100 * time.Millisecond,
				})
				if err != nil {
					cancel()
					t.Fatal(err)
				}
				outcome := <-handle.Done()
				if !outcome.TimedOut {
					cancel()
					t.Fatalf("timeout outcome=%#v", outcome)
				}
			} else {
				response, err := worker.Execute(ctx, code, "user")
				if err != nil || strings.TrimSpace(response.Stdout) != "spawned" {
					cancel()
					t.Fatalf("restart spawn response=%#v err=%v", response, err)
				}
				if _, err := manager.Restart(ctx, spec.KernelID); err != nil {
					cancel()
					t.Fatal(err)
				}
			}
			cancel()
			time.Sleep(1500 * time.Millisecond)
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("setsid descendant survived %s: %v", operation, err)
			}
		})
	}
}

const errnoEPERM = 1

package shellops

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"synon-go/internal/executionprep"
)

func nativePowerShellObservation(t *testing.T, source string) *executionprep.Observation {
	t.Helper()
	executable, err := executionprep.NativePowerShell()
	if err != nil {
		t.Skip(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := executionprep.Analyze(ctx, executionprep.Request{Language: "powershell", Source: source},
		func(ctx context.Context, language, source string) ([]executionprep.Fact, error) {
			command := exec.CommandContext(ctx, executable, "-NoProfile", "-NonInteractive", "-Command", executionprep.PowerShellParser())
			command.Stdin = strings.NewReader(base64.StdEncoding.EncodeToString([]byte(source)))
			output, err := command.Output()
			if err != nil {
				return nil, err
			}
			return executionprep.DecodeNative(language, output)
		})
	if err != nil || !result.Observation.Matches("powershell", source) {
		t.Fatalf("native observation unavailable: %#v %v", result, err)
	}
	return result.Observation
}

func TestObservationPowerShellNativeExecution(t *testing.T) {
	root := t.TempDir()
	source := "# native observation\nGet-Location\nWrite-Output 'ready'\n'$(Set-Content forbidden x)'\n1"
	plan := nativePowerShellObservation(t, source)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ctx = executionprep.WithObservation(ctx, plan)
	result, err := ExecuteShellCommand(ctx, root, "PowerShell", source, "", 10000)
	if err != nil || result.ExitCode != 0 || !strings.Contains(result.Stdout, "ready") || !strings.Contains(result.Stdout, "Set-Content forbidden") {
		t.Fatalf("native guarded execution: %#v %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(root, "forbidden")); !os.IsNotExist(err) {
		t.Fatal("quoted data was invoked")
	}
	if result, err := ExecuteShellCommand(ctx, root, "PowerShell", "Write-Output 'changed source'", "", 10000); err == nil || result.Stdout != "" {
		t.Fatalf("changed source reused proof: %#v %v", result, err)
	}
	running, err := StartShellCommand(ctx, root, "PowerShell", source, "")
	if err != nil {
		t.Fatal(err)
	}
	background, err := running.Wait()
	if err != nil || !strings.Contains(background.Stdout, "ready") {
		t.Fatalf("background lost proof: %#v %v", background, err)
	}
}

func TestObservationPowerShellNativeBindingShadowDeclines(t *testing.T) {
	source := "Get-Location"
	plan := nativePowerShellObservation(t, source)
	guarded, err := executionprep.GuardedPowerShell(source, plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, setup := range []string{
		"function Get-Location { Write-Output 'shadow-ran' }; ",
		"Set-Alias Get-Location Write-Output; ",
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		result, err := ExecuteShellCommand(ctx, t.TempDir(), "PowerShell", setup+guarded, "", 10000)
		cancel()
		if err == nil || strings.Contains(result.Stdout, "shadow-ran") || !strings.Contains(result.Stderr, "diagnostic_binding_unproved") {
			t.Fatalf("native command shadow was not refused: %#v %v", result, err)
		}
	}
}

func TestObservationPowerShellRequiresNativeRuntime(t *testing.T) {
	if _, err := executionprep.NativePowerShell(); err == nil {
		t.Skip("native runtime is installed")
	}
	source := "Get-Location"
	plan := &executionprep.Observation{Schema: executionprep.ObservationSchema, Language: "powershell", SourceSHA256: executionprep.SourceSHA256(source), Operations: []string{"powershell.directory"}}
	ctx := executionprep.WithObservation(context.Background(), plan)
	result, err := ExecuteShellCommand(ctx, t.TempDir(), "PowerShell", source, "", 1000)
	if err == nil || result.Stdout != "" {
		t.Fatalf("compatibility runtime satisfied native proof: %#v %v", result, err)
	}
	if _, err := StartShellCommand(ctx, t.TempDir(), "PowerShell", source, ""); err == nil {
		t.Fatal("background bypassed native runtime requirement")
	}
}

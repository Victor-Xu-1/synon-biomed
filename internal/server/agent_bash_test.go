package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"synon-go/internal/tools/shellops"
)

func TestAgentBashSchemaUsesTheSelectedManagedEnvironment(t *testing.T) {
	schema := agentKernelBashToolSchema()
	if schema.Name != "bash" || !strings.Contains(schema.Description, "manage_environments") ||
		!strings.Contains(schema.Description, "manage_packages") {
		t.Fatalf("bash schema=%#v", schema)
	}
	validator := compileAgentRuntimeMCPValidator(schema)
	if value := validator.Validate(map[string]any{
		"command": "python analysis.py", "environment": "scanpy", "background": true,
		"human_description": "Running scanpy analysis",
	}); value != nil {
		t.Fatalf("valid bash input rejected: %#v", value)
	}
	for _, input := range []map[string]any{
		{"command": "python analysis.py", "human_description": "Running analysis"},
		{"command": "python analysis.py", "environment": "scanpy"},
		{"command": "python analysis.py", "environment": "scanpy", "human_description": "Running analysis", "executable": "/bin/bash"},
	} {
		if value := validator.Validate(input); value == nil || value["code"] != "invalid_tool_arguments" {
			t.Fatalf("invalid bash input admitted: input=%#v result=%#v", input, value)
		}
	}
}

func TestAgentBashWrapperRoundTripsCommandAndTerminalStatus(t *testing.T) {
	command := "printf 'result=%s\\n' ok\nprintf 'warning\\n' >&2"
	wrapper, err := agentBashPythonWrapper(command)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := agentBashCommandFromWrapper(wrapper)
	if err != nil || decoded != command {
		t.Fatalf("decoded=%q err=%v", decoded, err)
	}

	stderr, status, exitCode, err := normalizeAgentBashTerminal("warning\n\n"+agentBashExitPrefix+"0\n", "ok")
	if err != nil || stderr != "warning\n" || status != "ok" || exitCode != 0 {
		t.Fatalf("success stderr=%q status=%q exit=%d err=%v", stderr, status, exitCode, err)
	}
	stderr, status, exitCode, err = normalizeAgentBashTerminal("\n"+agentBashExitPrefix+"7\n", "ok")
	if err != nil || status != "error" || exitCode != 7 || !strings.Contains(stderr, "status 7") {
		t.Fatalf("failure stderr=%q status=%q exit=%d err=%v", stderr, status, exitCode, err)
	}
}

func TestAgentBashRejectsUnsafeShellPaths(t *testing.T) {
	for _, command := range []string{"sudo id"} {
		err := validateAgentBashCommand(command)
		var safetyErr shellops.SafetyError
		if !errors.As(err, &safetyErr) {
			t.Fatalf("command %q error=%v", command, err)
		}
	}
	if err := validateAgentBashCommand("python analysis.py && ls -la outputs"); err != nil {
		t.Fatalf("ordinary command rejected: %v", err)
	}
}

func TestAgentBashExecutesThroughThePersistentManagedKernel(t *testing.T) {
	root := t.TempDir()
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(root, "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	result, err := app.executeAgentKernelTool(ctx, identity, "bash", map[string]any{
		"command": "printf 'BASH-READY\\n'", "environment": "python",
		"human_description": "Running Bash kernel witness",
	})
	if err != nil || result["ok"] != true || result["exit_status"] != "ok" || result["exit_code"] != 0 ||
		!strings.Contains(stringValue(result["stdout"]), "BASH-READY") {
		t.Fatalf("bash result=%#v err=%v", result, err)
	}
	skillRoot := filepath.Join(root, "trusted-skill")
	if err := os.MkdirAll(filepath.Join(skillRoot, "scripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	skillScript := filepath.Join(skillRoot, "scripts", "witness.py")
	if err := os.WriteFile(skillScript, []byte("print('SKILL-SCRIPT-READY')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	app.skillDirectories = []string{skillRoot}
	skillResult, err := app.executeAgentKernelTool(ctx, identity, "bash", map[string]any{
		"command": "python3 " + strconv.Quote(skillScript), "environment": "python",
		"human_description": "Running trusted Skill script",
	})
	if err != nil || skillResult["ok"] != true || !strings.Contains(stringValue(skillResult["stdout"]), "SKILL-SCRIPT-READY") {
		t.Fatalf("Skill Bash result=%#v err=%v", skillResult, err)
	}

	failed, err := app.executeAgentKernelTool(ctx, identity, "bash", map[string]any{
		"command": "printf 'EXPECTED-FAILURE\\n' >&2; exit 7", "environment": "python",
		"human_description": "Running Bash failure witness",
	})
	if err != nil || failed["ok"] != false || failed["exit_status"] != "error" || failed["exit_code"] != 7 ||
		failed["code"] != "bash_nonzero_exit" || !strings.Contains(stringValue(failed["stderr"]), "EXPECTED-FAILURE") {
		t.Fatalf("bash failure=%#v err=%v", failed, err)
	}

	masked, err := app.executeAgentKernelTool(ctx, identity, "bash", map[string]any{
		"command": "python3 -c 'raise SystemExit(9)'\nprintf 'MASKED-SUCCESS\\n'", "environment": "python",
		"human_description": "Checking multi-command failure propagation",
	})
	if err != nil || masked["ok"] != false || masked["exit_status"] != "error" || masked["exit_code"] != 9 ||
		strings.Contains(stringValue(masked["stdout"]), "MASKED-SUCCESS") {
		t.Fatalf("bash masked failure=%#v err=%v", masked, err)
	}
}

func TestAgentKernelMountsTrustedSkillScriptsReadOnlyWithoutDuplicateRoots(t *testing.T) {
	root := t.TempDir()
	workspaceDir := filepath.Join(root, "workspace")
	skillRoot := filepath.Join(root, "skills")
	nested := filepath.Join(skillRoot, "nested")
	for _, path := range []string{workspaceDir, nested} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	app := &Server{skillDirectories: []string{nested, skillRoot}}
	mounts, err := app.agentKernelConfinementMounts("owner", workspaceDir, nil)
	if err != nil || len(mounts) != 1 || mounts[0].Path != skillRoot || mounts[0].Writable {
		t.Fatalf("trusted Skill mounts=%#v err=%v", mounts, err)
	}
}

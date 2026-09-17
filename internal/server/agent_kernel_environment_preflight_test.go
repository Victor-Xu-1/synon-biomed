package server

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAgentKernelEnvironmentRequiresManagedGeneration(t *testing.T) {
	tests := []struct {
		name        string
		tool        string
		environment string
		want        bool
	}{
		{name: "system python", tool: "python", environment: "python", want: false},
		{name: "repl alias", tool: "bash", environment: "repl", want: false},
		{name: "managed python", tool: "python", environment: agentKernelManagedPythonEnvironment, want: false},
		{name: "custom python", tool: "python", environment: "analysis", want: true},
		{name: "custom bash", tool: "bash", environment: "analysis", want: true},
		{name: "r", tool: "r", environment: "r-analysis", want: true},
		{name: "software runtime", tool: softwareRuntimeToolName, environment: "runtime", want: true},
		{name: "repl tool", tool: "repl", environment: "analysis", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := agentKernelEnvironmentRequiresManagedGeneration(test.tool, test.environment); got != test.want {
				t.Fatalf("requires managed generation=%t, want %t", got, test.want)
			}
		})
	}
}

func TestAgentKernelEnvironmentReadinessPreflightIsNonExecutingAndBounded(t *testing.T) {
	result := agentKernelEnvironmentReadinessPreflight(errors.New("private runtime detail"))
	if result["ok"] != false || result["executed"] != false || result["status"] != "environment_preflight_required" {
		t.Fatalf("preflight=%#v", result)
	}
	if result["retryable"] != true || result["message"] != "The selected analysis environment is not ready for execution." {
		t.Fatalf("preflight metadata=%#v", result)
	}
	if got := result["recovery"].(string); got == "" || got == "private runtime detail" {
		t.Fatalf("recovery leaked or empty: %q", got)
	}
}

func TestExecuteAgentKernelToolDoesNotStartUnavailableCustomEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("kernel manager integration requires Linux")
	}
	root := t.TempDir()
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(root, "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	result, err := app.executeAgentKernelTool(context.Background(), identity, "python", map[string]any{
		"code":        "print('should not start')",
		"environment": "missing-analysis",
	})
	if err != nil {
		t.Fatalf("execute err=%v", err)
	}
	if result == nil || result["status"] != "environment_preflight_required" || result["executed"] != false {
		t.Fatalf("result=%#v", result)
	}
}

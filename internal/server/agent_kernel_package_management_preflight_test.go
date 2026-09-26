package server

import (
	"context"
	"os/exec"
	kernelruntime "synon-go/internal/kernel"
	"testing"
)

func TestAgentKernelPackageManagerMutationPreflightRejectsBypassPaths(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	preparer := kernelruntime.NewManager(kernelruntime.Config{Python: python})
	tests := []struct {
		name  string
		tool  string
		input map[string]any
	}{
		{
			name: "python subprocess list", tool: "python",
			input: map[string]any{"code": `import subprocess,sys
cmd = [sys.executable, "-m", "pip", "install", "torch-scatter"]
subprocess.run(cmd, check=True)`},
		},
		{
			name: "repl os system", tool: "repl",
			input: map[string]any{"code": `import os
os.system("conda install -y rdkit")`},
		},
		{
			name: "bash pip", tool: "bash",
			input: map[string]any{"command": "python3 -m pip install package-name"},
		},
		{
			name: "bash micromamba", tool: "bash",
			input: map[string]any{"command": "micromamba install -n analysis package-name"},
		},
		{
			name: "python embedded package interface", tool: "python",
			input: map[string]any{"code": `from rpy2.robjects.packages import importr as load_package
utils = load_package("utils")
utils.install_packages("package-name")`},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := agentExecutionPreparationPreflight(context.Background(), test.tool, test.input, nil, preparer)
			if stringValue(result["status"]) != "managed_package_authority_required" ||
				boolValue(result["executed"], true) {
				t.Fatalf("package-manager bypass was not rejected: %#v", result)
			}
		})
	}
}

func TestAgentKernelPackageManagerMutationPreflightAllowsScientificExecution(t *testing.T) {
	allowed := []struct {
		tool  string
		input map[string]any
	}{
		{"python", map[string]any{"code": `import subprocess
subprocess.run(["vina", "--config", "dock.conf"], check=True)`}},
		{"bash", map[string]any{"command": "python run_inference.py --input pocket.pdb"}},
		{"r", map[string]any{"code": `result <- read.csv("input.csv")`}},
	}
	for _, test := range allowed {
		if result := agentExecutionPreparationPreflight(context.Background(), test.tool, test.input, nil, nil); result != nil {
			t.Fatalf("scientific execution was blocked for %s: %#v", test.tool, result)
		}
	}
}

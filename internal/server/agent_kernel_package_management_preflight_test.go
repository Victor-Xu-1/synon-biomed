package server

import "testing"

func TestAgentKernelPackageManagerMutationPreflightRejectsBypassPaths(t *testing.T) {
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
			name: "r install packages", tool: "r",
			input: map[string]any{"code": `install.packages("Seurat")`},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := agentKernelPackageManagerMutationPreflight(test.tool, test.input)
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
		if result := agentKernelPackageManagerMutationPreflight(test.tool, test.input); result != nil {
			t.Fatalf("scientific execution was blocked for %s: %#v", test.tool, result)
		}
	}
}

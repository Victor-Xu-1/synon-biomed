package server

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	kernelruntime "synon-go/internal/kernel"
)

func TestExecutionPreparationHasOneAdmissionAndExecutionDecision(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	fixture.server.kernelManager = kernelruntime.NewManager(kernelruntime.Config{Python: python})
	gateway := serverAgentRuntimeToolGateway{server: fixture.server, kernel: fixture.identity, allowedTools: []string{"python"}}
	for _, test := range []struct{ source, status string }{
		{`import subprocess as child
child.run(["python", "-m", "pip", "install", "package-name"], check=True)`, "managed_package_authority_required"},
		{`import requests
response=requests.get("https://example.org/data.csv")
open("data.csv","wb").write(response.content)`, "durable_download_preflight_required"},
	} {
		input := map[string]any{"code": test.source, "environment": "python", "human_description": "Execution preparation witness"}
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		diagnostic := gateway.ToolCallPreflightDiagnostic(agentruntime.ToolCall{Name: "python", Arguments: raw})
		if !strings.Contains(diagnostic, test.status) {
			t.Fatalf("admission missed source effect: %s", diagnostic)
		}
		result, err := fixture.server.executeAgentKernelTool(context.Background(), fixture.identity, "python", input)
		if err != nil || result["status"] != test.status || result["executed"] != false {
			t.Fatalf("execution decision diverged: %#v %v", result, err)
		}
	}
	var operations int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM kernel_local_operations`).Scan(&operations); err != nil || operations != 0 {
		t.Fatalf("preparation created a competing execution: %d %v", operations, err)
	}
}

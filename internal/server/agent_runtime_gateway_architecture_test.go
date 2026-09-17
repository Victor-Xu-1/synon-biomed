package server

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestToolGatewayActiveEntrypointsUseOneOrderedPipeline(t *testing.T) {
	t.Parallel()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve tool gateway architecture test path")
	}
	serverDir := filepath.Dir(currentFile)
	production := map[string]string{}
	err := filepath.WalkDir(serverDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		production[entry.Name()] = string(raw)
		return nil
	})
	if err != nil {
		t.Fatalf("read server production sources: %v", err)
	}
	allSource := strings.Join(mapValues(production), "\n")
	if count := strings.Count(allSource, "func (g serverAgentRuntimeToolGateway) Execute("); count != 1 {
		t.Fatalf("serverAgentRuntimeToolGateway.Execute definitions = %d, want 1", count)
	}
	pipelineSource := production["agent_runtime_gateway_pipeline.go"]
	for _, required := range []string{
		"serverAgentRuntimeToolPipeline().Run(invocation)",
		"func (s *Server) executeExactToolGateway(",
		"toolgateway.StageNormalize",
		"toolgateway.StageAudit",
	} {
		if !strings.Contains(pipelineSource, required) {
			t.Errorf("unified gateway pipeline is missing %q", required)
		}
	}
	for _, entrypoint := range []struct {
		file string
		call string
	}{
		{"server_tool_dispatch.go", "executeExactToolGateway("},
		{"agent_runtime_approved_execution.go", "executeExactToolGateway("},
		{"task_run_system_steps.go", "executeExactToolGateway("},
	} {
		if !strings.Contains(production[entrypoint.file], entrypoint.call) {
			t.Errorf("active entrypoint %s does not delegate through %s", entrypoint.file, entrypoint.call)
		}
	}
	for _, legacy := range []string{
		"executeDirectToolGatewayLegacyResponse(",
		"executeApprovedAgentRuntimeToolLegacy(",
		"executeChatToolCall(",
	} {
		if count := strings.Count(allSource, legacy); count != 0 {
			t.Errorf("retired implementation %s remains in production source %d times", legacy, count)
		}
	}
}

func mapValues(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

package agentruntime

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAgentEngineResponsibilitiesHaveOneOwner(t *testing.T) {
	t.Parallel()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve agent engine architecture path")
	}
	directory := filepath.Dir(currentFile)
	required := map[string][]string{
		"engine.go":                 {"type Engine struct", "type ToolGateway interface"},
		"engine_run.go":             {"func (e Engine) Run("},
		"engine_tool_preamble.go":   {"type toolPreamblePublication struct", "func (e Engine) publishToolPreamble("},
		"engine_protocol.go":        {"func applyToolSchemaDefaults("},
		"engine_tool_feedback.go":   {"func collectToolCallRejections(", "func (e Engine) executeToolCallRoundWithRejections("},
		"engine_tool_batch.go":      {"func (e Engine) ExecuteToolBatch(", "func (e Engine) executeTool("},
		"engine_materialization.go": {"func (e Engine) MaterializeToolResult("},
		"openai_client.go":          {"func (c OpenAIChatClient) Complete("},
	}
	all := ""
	for fileName, markers := range required {
		raw, err := os.ReadFile(filepath.Join(directory, fileName))
		if err != nil {
			t.Errorf("read %s: %v", fileName, err)
			continue
		}
		content := string(raw)
		all += "\n" + content
		for _, marker := range markers {
			if !strings.Contains(content, marker) {
				t.Errorf("%s is missing %q", fileName, marker)
			}
		}
	}
	for _, authority := range []string{
		"func (e Engine) Run(",
		"func (e Engine) publishToolPreamble(",
		"func (e Engine) ExecuteToolBatch(",
		"func (e Engine) MaterializeToolResult(",
	} {
		if count := strings.Count(all, authority); count != 1 {
			t.Errorf("agent engine authority %q count = %d, want 1", authority, count)
		}
	}
}

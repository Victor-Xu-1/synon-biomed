package server

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestScientificRuntimeServerAdaptersHaveCohesiveModuleOwners(t *testing.T) {
	t.Parallel()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve scientific runtime architecture path")
	}
	directory := filepath.Dir(currentFile)
	required := map[string][]string{
		"agent_kernel.go":                   {"type agentKernelContext struct", "func (s *Server) resolveAgentKernelContext("},
		"agent_kernel_catalog.go":           {"func (s *Server) agentKernelToolSchemas("},
		"agent_kernel_execution.go":         {"func (s *Server) executeAgentKernelToolInternal("},
		"agent_kernel_policy.go":            {"func (s *Server) agentKernelEgressPolicy("},
		"agent_kernel_settlement.go":        {"func (s *Server) finishAgentKernelExecution("},
		"agent_kernel_preflight.go":         {"func agentKernelPythonCodePreflight("},
		"agent_kernel_result.go":            {"func kernelOutcomeStatus("},
		"runner_artifact_completion.go":     {"func (s *Server) runSessionAgentWithArtifactReferenceRepair("},
		"runner_completion_deliverables.go": {"func sessionRunnerExplicitDeliverableNames("},
		"runner_completion_references.go":   {"func unsupportedSessionRunnerCitationReferences("},
		"runner_completion_evidence.go":     {"func collectVerifiedSessionRunnerReferences("},
		"runner_completion_syntax.go":       {"func normalizeSessionRunnerMalformedArtifactReferences("},
		"runner_completion_lineage.go":      {"func (s *Server) sessionRunnerArtifactCommitReferences("},
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
		"func (s *Server) executeAgentKernelToolInternal(",
		"func (s *Server) runSessionAgentWithArtifactReferenceRepair(",
	} {
		if count := strings.Count(all, authority); count != 1 {
			t.Errorf("scientific runtime authority %q count = %d, want 1", authority, count)
		}
	}
	for _, retired := range []string{
		"agentKernelAuthoritativeSourcePreflight",
		"Scientific execution was safely deferred because externally defined inputs",
	} {
		if strings.Contains(all, retired) {
			t.Errorf("retired coarse scientific execution gate remains: %q", retired)
		}
	}
}

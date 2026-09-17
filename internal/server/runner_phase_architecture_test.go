package server

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSessionRunnerModulesBindOnePhaseMachine(t *testing.T) {
	t.Parallel()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve runner phase architecture path")
	}
	serverDir := filepath.Dir(currentFile)
	requiredFiles := map[string][]string{
		"runner_cycle.go": {"func (s *Server) runSessionRunnerChatOnce("},
		"runner_execution.go": {
			"func (s *Server) runSessionRunnerChat(",
			"advanceSessionRunnerEventPhase(run, event)",
		},
		"runner_context_scope.go":        {"func (s *Server) runnerEntriesForCurrentLogicalInput("},
		"memory_extraction_runtime.go":   {"type memoryExtractionRuntime struct", "func (runtime *memoryExtractionRuntime) RunCompletedRoot("},
		"runner_model_transport.go":      {"func (s *Server) resolveSessionRunnerModelAuthority("},
		"runner_plan_mode.go":            {"func (s *Server) runSessionAgentWithPlanMode(", "func sessionRunnerPlanModeEnabled("},
		"runner_recovery_corrections.go": {"func latestRunnerCorrection("},
		"runner_streaming_checkpoint.go": {"func (s *Server) checkpointChatTool("},
		"runner_task_contract.go":        {"func buildSessionRunnerTaskContract("},
	}
	for fileName, markers := range requiredFiles {
		raw, err := os.ReadFile(filepath.Join(serverDir, fileName))
		if err != nil {
			t.Errorf("read %s: %v", fileName, err)
			continue
		}
		for _, marker := range markers {
			if !strings.Contains(string(raw), marker) {
				t.Errorf("runner module %s is missing %q", fileName, marker)
			}
		}
	}
	allSource := ""
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
		allSource += "\n" + string(raw)
		return nil
	})
	if err != nil {
		t.Fatalf("read runner production sources: %v", err)
	}
	if count := strings.Count(allSource, "func (s *Server) runSessionRunnerChatOnce("); count != 1 {
		t.Errorf("runSessionRunnerChatOnce definitions = %d, want 1", count)
	}
	if count := strings.Count(allSource, "func (s *Server) runSessionRunnerChat("); count != 1 {
		t.Errorf("runSessionRunnerChat definitions = %d, want 1", count)
	}
	if count := strings.Count(allSource, "executeChatToolCall("); count != 0 {
		t.Errorf("retired static chat tool remains in production source %d times", count)
	}
}

func TestRunnerRecoveryCorrectionHasNoTaskSpecificToolRouting(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve runner recovery architecture path")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(currentFile), "runner_recovery_corrections.go"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	for _, forbidden := range []string{
		"Begin with ", "validation.json", "scenario.csv", "web_fetch on", "VisualReview action=",
		"runnerTextRequestsOfficialGuidelineSource", "runnerCorrectionRequiresCleanupRepair",
	} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("recovery correction retained task-specific route %q", forbidden)
		}
	}
}

func TestScientificEvidenceGateHasNoPharmaceuticalRouteClassifier(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve scientific evidence architecture path")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(currentFile), "runner_scientific_real_evidence.go"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	for _, forbidden := range []string{
		"externallyDefinedPharmaceuticalInputPhrases", `"molecular docking"`, `"分子对接"`,
	} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("scientific evidence gate retained domain route classifier %q", forbidden)
		}
	}
}

func TestRunnerCapabilityRoutingHasOneMetadataAuthority(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve capability routing architecture path")
	}
	serverDir := filepath.Dir(currentFile)
	checks := map[string][]string{
		"agent_runtime_engine_factory.go": {
			"generatedPlanActiveResearchStep(",
			`schema.Name == "web_research"`,
		},
		"runner_scientific_execution_authority.go": {
			"taskIntent", "sessionRunnerTaskRequires",
		},
		"runner_scientific_real_evidence.go": {
			"scientificTaskPhrases", "scientificExecutionEvidencePhrases",
			"sessionRunnerTaskRequiresRuntimeExecution",
		},
	}
	for fileName, forbidden := range checks {
		raw, err := os.ReadFile(filepath.Join(serverDir, fileName))
		if err != nil {
			t.Fatal(err)
		}
		content := string(raw)
		for _, marker := range forbidden {
			if strings.Contains(content, marker) {
				t.Fatalf("%s retained competing capability authority %q", fileName, marker)
			}
		}
	}
}

func TestRunnerDeliverableAndEvidenceContractsDoNotRouteFromTaskPhrases(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve task contract architecture path")
	}
	serverDir := filepath.Dir(currentFile)
	checks := map[string][]string{
		"runner_completion_deliverables.go": {
			"sessionRunnerReportArtifactIntentPatterns",
			"sessionRunnerEditableLedgerIntentPatterns",
			"sessionRunnerReusableCalculationIntentPatterns",
		},
		"runner_evidence_record_depth.go": {
			"runnerEvidenceDepthRequestedClasses",
			"published research", "clinical development", "company disclosure",
		},
	}
	for fileName, forbidden := range checks {
		raw, err := os.ReadFile(filepath.Join(serverDir, fileName))
		if err != nil {
			t.Fatal(err)
		}
		content := string(raw)
		for _, marker := range forbidden {
			if strings.Contains(content, marker) {
				t.Fatalf("%s retained task-phrase routing %q", fileName, marker)
			}
		}
	}
}

func TestRunnerHasOneModelDirectedSkillDiscoveryPath(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve Skill routing architecture path")
	}
	serverDir := filepath.Dir(currentFile)
	for _, fileName := range []string{"runner_execution.go", "runner_policy_context.go"} {
		raw, err := os.ReadFile(filepath.Join(serverDir, fileName))
		if err != nil {
			t.Fatal(err)
		}
		content := string(raw)
		for _, forbidden := range []string{"runtimeSkillsWithPolicy(", "runtimeSelectedSkills(", "mergeRuntimeSkills("} {
			if strings.Contains(content, forbidden) {
				t.Fatalf("%s retained competing automatic Skill route %q", fileName, forbidden)
			}
		}
	}
}

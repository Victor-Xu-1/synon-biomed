package server

import (
	"encoding/json"
	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/processsupervisor"
	"testing"
	"time"
)

func TestKernelResourceFailureSurvivesSerializationForEveryLanguage(t *testing.T) {
	for _, language := range []string{"python", "r", "bash"} {
		t.Run(language, func(t *testing.T) {
			pressure := processsupervisor.MemoryPressure{Status: "pressured", CurrentBytes: 95, HighBytes: 100, LimitBytes: 100, FullStallPercent: 90}
			response := kernelruntime.Response{Stdout: "completed checkpoint", Trace: map[string]any{"resource_pressure": pressure}}
			raw, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(raw, &response); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			spec := kernelruntime.SessionSpec{Language: language, KernelKind: language, Environment: "runtime"}
			_, result, _ := prepareAgentKernelExecution(workspace.KernelFrameAccess{}, spec, kernelruntime.EnsuredSession{}, kernelruntime.ExecutionStarted{StartedAt: now}, kernelruntime.ExecutionOutcome{Response: response, Err: &kernelruntime.ResourcePressureFailure{Observation: pressure}, StartedAt: now, FinishedAt: now.Add(time.Second)})
			if result["ok"] != false || result["code"] != "kernel_memory_pressure" || result["recoverable"] != true || result["retry_unchanged"] != false || result["in_memory_state_lost"] != true || result["resource_pressure"] == nil {
				t.Fatalf("resource contract lost: %#v", result)
			}
		})
	}
}

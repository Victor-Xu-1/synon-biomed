package server

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	kernelruntime "synon-go/internal/kernel"
)

func TestManagedEnvironmentReuseBindsRequestedNameToSelectedEnvironment(t *testing.T) {
	run := &sessionRunnerChatRun{}
	run.bindManagedEnvironmentToolResult(
		manageEnvironmentsToolName,
		map[string]any{"name": "drug-development"},
		map[string]any{
			"mode":           "reuse",
			"requested_name": "drug-development",
			"environment": map[string]any{
				"name": "existing-analysis", "status": "ready",
			},
		},
	)

	normalized := normalizeSelectedManagedEnvironment(run, "python", map[string]any{
		"environment": "drug-development", "code": "print(1)",
	})
	if got := stringValue(normalized["environment"]); got != "existing-analysis" {
		t.Fatalf("normalized environment=%q", got)
	}
}

func TestManagedEnvironmentHydrationFailureDoesNotPublishReady(t *testing.T) {
	run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{}}
	server := &Server{}
	server.hydrateSessionRunnerManagedEnvironmentBindings(context.Background(), run)
	if run.managedEnvironmentBindingsHydrated {
		t.Fatal("failed transcript read permanently published hydrated state")
	}
}

func TestManagedEnvironmentHydrationRetriesFailureAndSerializesReaders(t *testing.T) {
	run := &sessionRunnerChatRun{}
	wantErr := errors.New("transcript temporarily unavailable")
	if err := run.hydrateManagedEnvironmentBindings(context.Background(), func() ([]agentruntime.Message, error) { return nil, wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("error=%v", err)
	}
	var loads atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- run.hydrateManagedEnvironmentBindings(context.Background(), func() ([]agentruntime.Message, error) { loads.Add(1); close(started); <-release; return nil, nil })
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := run.hydrateManagedEnvironmentBindings(ctx, func() ([]agentruntime.Message, error) { loads.Add(1); return nil, nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("concurrent reader passed before hydration: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := run.hydrateManagedEnvironmentBindings(context.Background(), func() ([]agentruntime.Message, error) { loads.Add(1); return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if loads.Load() != 1 || !run.managedEnvironmentBindingsHydrated {
		t.Fatalf("loads=%d ready=%v", loads.Load(), run.managedEnvironmentBindingsHydrated)
	}
}

func TestManagedEnvironmentPreflightBindsRequestedNameToRecommendedEnvironment(t *testing.T) {
	run := &sessionRunnerChatRun{}
	run.bindManagedEnvironmentToolResult(
		manageEnvironmentsToolName,
		map[string]any{"mode": "preflight", "name": "ligand-image-runtime"},
		map[string]any{
			"mode": "preflight",
			"recommended_environment": map[string]any{
				"name": "swr-ready-rdkit", "status": "ready",
			},
		},
	)

	normalized := normalizeSelectedManagedEnvironment(run, "python", map[string]any{
		"environment": "ligand-image-runtime", "code": "print(1)",
	})
	if got := stringValue(normalized["environment"]); got != "swr-ready-rdkit" {
		t.Fatalf("normalized preflight environment=%q", got)
	}
}

func TestManagedEnvironmentBindingDoesNotRewriteUnrelatedOrCanonicalNames(t *testing.T) {
	run := &sessionRunnerChatRun{}
	run.bindManagedEnvironment("requested-analysis", "existing-analysis")

	canonical := normalizeSelectedManagedEnvironment(run, "python", map[string]any{
		"environment": "existing-analysis", "code": "print(1)",
	})
	if got := stringValue(canonical["environment"]); got != "existing-analysis" {
		t.Fatalf("canonical environment=%q", got)
	}
	unrelated := normalizeSelectedManagedEnvironment(run, "read_file", map[string]any{
		"environment": "requested-analysis", "path": "report.md",
	})
	if got := stringValue(unrelated["environment"]); got != "requested-analysis" {
		t.Fatalf("unrelated environment=%q", got)
	}
}

func TestManagedEnvironmentBindingRehydratesFromDurableToolMessages(t *testing.T) {
	arguments, err := json.Marshal(map[string]any{
		"mode": "create", "name": "drug-development", "implementation": "Engine A", "packages": []string{"pandas"},
	})
	if err != nil {
		t.Fatal(err)
	}
	run := &sessionRunnerChatRun{}
	run.bindManagedEnvironmentMessages([]agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "environment-call", Name: manageEnvironmentsToolName, Arguments: arguments,
		}}},
		{Role: "tool", ToolCallID: "environment-call", Content: `{
			"status":"completed","mode":"reuse","requested_name":"drug-development",
			"environment":{"name":"existing-analysis","status":"ready"}
		}`},
	})

	if got := run.selectedManagedEnvironment("drug-development"); got != "existing-analysis" {
		t.Fatalf("rehydrated environment=%q", got)
	}
	if got := run.managedEnvironmentImplementationsSnapshot("existing-analysis"); len(got) != 1 || got[0] != "Engine A" {
		t.Fatalf("rehydrated implementation bindings=%v", got)
	}
}

func TestManagedPackageBindingIgnoresLegacyCrossEnvironmentReuse(t *testing.T) {
	run := &sessionRunnerChatRun{}
	run.bindManagedEnvironmentToolResult(
		managePackagesToolName,
		map[string]any{"environment": "pocket-generator", "implementation": "Generator A"},
		map[string]any{
			"status": "completed", "mode": "reuse", "requested_environment": "pocket-generator",
			"environment": map[string]any{"name": "unrelated-docking", "status": "ready"},
		},
	)
	if got := run.selectedManagedEnvironment("pocket-generator"); got != "pocket-generator" {
		t.Fatalf("legacy cross-environment alias=%q", got)
	}
	if got := run.managedEnvironmentImplementationsSnapshot("unrelated-docking"); len(got) != 0 {
		t.Fatalf("legacy result polluted implementation bindings: %v", got)
	}
}

func TestSelectedImplementationEnvironmentPreflightRejectsOlderEngineEnvironment(t *testing.T) {
	run := &sessionRunnerChatRun{SelectedImplementations: []string{"Engine A"}}
	run.bindManagedEnvironmentImplementation("engine-b-runtime", "Engine B")
	gateway := serverAgentRuntimeToolGateway{taskRun: run}
	result := gateway.agentRuntimeSelectedImplementationEnvironmentPreflight("bash", map[string]any{
		"environment": "engine-b-runtime", "command": "tool --version",
	})
	if stringValue(result["status"]) != "selected_implementation_environment_mismatch" ||
		boolValue(result["executed"], true) {
		t.Fatalf("older implementation environment was not blocked: %#v", result)
	}
	if allowed := gateway.agentRuntimeSelectedImplementationEnvironmentPreflight("python", map[string]any{
		"environment": "generic-runtime", "code": "print(1)",
	}); allowed != nil {
		t.Fatalf("unbound generic environment was blocked: %#v", allowed)
	}
	run.bindManagedEnvironmentImplementation("shared-runtime", "Engine A")
	run.bindManagedEnvironmentImplementation("shared-runtime", "Engine B")
	if allowed := gateway.agentRuntimeSelectedImplementationEnvironmentPreflight("python", map[string]any{
		"environment": "shared-runtime", "code": "print(1)",
	}); allowed != nil {
		t.Fatalf("shared environment containing selected implementation was blocked: %#v", allowed)
	}
}

func TestManagedEnvironmentImplementationRehydratesFromBackgroundNotification(t *testing.T) {
	run := &sessionRunnerChatRun{}
	run.bindManagedEnvironmentMessages([]agentruntime.Message{{
		Role: "tool", ToolCallID: "wait-call", Content: `{
			"notifications":[{"payload":{
				"tool":"manage_environments","status":"completed","implementation":"Engine B",
				"environment":{"name":"engine-b-runtime","status":"ready"}
			}}]
		}`,
	}})
	if got := run.managedEnvironmentImplementationsSnapshot("engine-b-runtime"); len(got) != 1 || got[0] != "Engine B" {
		t.Fatalf("background implementation bindings=%v", got)
	}
}

func TestManagedEnvironmentCreationClearsOlderReuseBinding(t *testing.T) {
	run := &sessionRunnerChatRun{}
	run.bindManagedEnvironment("analysis", "existing-analysis")
	run.bindManagedEnvironmentToolResult(
		manageEnvironmentsToolName,
		map[string]any{"name": "analysis"},
		map[string]any{"status": "completed", "environment": map[string]any{
			"name": "analysis", "status": "ready",
		}},
	)
	if got := run.selectedManagedEnvironment("analysis"); got != "analysis" {
		t.Fatalf("selected environment=%q", got)
	}
}

func TestGatewayUsesTaskRunFromInvocationContextForEnvironmentAliases(t *testing.T) {
	run := &sessionRunnerChatRun{}
	run.bindManagedEnvironment("formulation-dev-plan", "swr-ready")
	gateway, boundContext := (serverAgentRuntimeToolGateway{}).bindTaskRunContext(
		withTranscriptRunnerChatRun(context.Background(), run),
	)
	if gateway.taskRun != run {
		t.Fatal("gateway did not adopt the task run carried by the invocation context")
	}
	if got := boundContext.Value(transcriptRunnerChatRunContextKey{}); got != run {
		t.Fatalf("bound context run=%p want=%p", got, run)
	}
	normalized := gateway.normalizeAdmittedToolArguments("python", map[string]any{
		"environment": "formulation-dev-plan", "code": "print(1)",
	})
	if got := stringValue(normalized["environment"]); got != "swr-ready" {
		t.Fatalf("context-bound normalized environment=%q", got)
	}
}

func TestManagedEnvironmentRuntimeInvalidationRehydratesAndClearsOnlyForReplacement(t *testing.T) {
	arguments, err := json.Marshal(map[string]any{
		"environment": "accelerated-runtime", "command": "run-workload",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := &sessionRunnerChatRun{}
	run.bindManagedEnvironmentMessages([]agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "runtime-call", Name: "bash", Arguments: arguments,
		}}},
		{Role: "tool", ToolCallID: "runtime-call", Content: `{
			"ok":false,"exit_status":"error",
			"stderr":"RuntimeError: installed framework does not contain the observed GPU architecture sm_120"
		}`},
	})
	invalid, found := run.managedEnvironmentInvalidationSnapshot("accelerated-runtime")
	if !found || invalid.Code != "managed_environment_accelerator_incompatible" {
		t.Fatalf("rehydrated invalidation=%#v found=%t", invalid, found)
	}
	if filtered := run.filterReusableManagedEnvironmentCandidates([]kernelruntime.ManagedEnvironment{{
		Name: "accelerated-runtime", Generation: "old-generation", Status: "ready",
	}}); len(filtered) != 0 {
		t.Fatalf("invalid environment remained reusable: %#v", filtered)
	}
	run.bindManagedEnvironmentToolResult(
		manageEnvironmentsToolName,
		map[string]any{"mode": "create", "name": "accelerated-runtime"},
		map[string]any{"status": "completed", "environment": map[string]any{
			"name": "accelerated-runtime", "generation": "replacement-generation", "status": "ready",
		}},
	)
	if _, found := run.managedEnvironmentInvalidationSnapshot("accelerated-runtime"); found {
		t.Fatal("replacement generation did not clear task-scoped invalidation")
	}
}

package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"synon-go/internal/software"
)

func TestStructureInteractionWarmupUsesExecutionEnvironmentIdentity(t *testing.T) {
	warmup := newStructureInteractionRuntimeRequest("")
	execution := newStructureInteractionRuntimeRequest(strings.Repeat("a", 64))
	warmupEnvironment, err := software.EnvironmentName(software.LocalProviderID, warmup)
	if err != nil {
		t.Fatalf("warmup environment: %v", err)
	}
	executionEnvironment, err := software.EnvironmentName(software.LocalProviderID, execution)
	if err != nil {
		t.Fatalf("execution environment: %v", err)
	}
	if warmupEnvironment != executionEnvironment {
		t.Fatalf("warmup environment %q differs from execution %q", warmupEnvironment, executionEnvironment)
	}
	packages := make([]string, 0, len(warmup.Packages))
	for _, requirement := range warmup.Packages {
		packages = append(packages, requirement.Spec)
	}
	if !warmupContainsString(packages, "rdkit==2024.3.5") || warmupContainsString(packages, "rdkit==2025.3.6") {
		t.Fatalf("warmup packages do not reuse the managed RDKit baseline: %v", packages)
	}
}

func TestScientificRuntimeWarmupCatalogKeepsEstimatesWithoutSizeCeilings(t *testing.T) {
	definitions := scientificRuntimeWarmupDefinitions()
	if len(definitions) != 13 {
		t.Fatalf("warmup definitions=%d", len(definitions))
	}
	defaultCount := 0
	for _, definition := range definitions {
		if definition.EstimatedInstallBytes <= 0 {
			t.Fatalf("runtime %q has no storage estimate", definition.ID)
		}
		if definition.DefaultEnabled {
			defaultCount++
		}
		request, _, err := definition.BuildRequest()
		if err != nil {
			t.Fatalf("build %s request: %v", definition.ID, err)
		}
		if request.Provider != software.LocalProviderID || request.Executable != "python" || len(request.Packages) == 0 {
			t.Fatalf("runtime %s request=%#v", definition.ID, request)
		}
	}
	if defaultCount != 0 {
		t.Fatalf("optional runtime selections=%d, want none by default", defaultCount)
	}
	vina, found := scientificRuntimeWarmupDefinitionByID(autoDockVinaRuntimeID)
	if !found || vina.DefaultEnabled {
		t.Fatalf("Vina runtime default contract=%#v found=%t", vina, found)
	}
	common, found := scientificRuntimeWarmupDefinitionByID(commonStructureRuntimeID)
	if !found || common.DefaultEnabled {
		t.Fatalf("common runtime default contract=%#v found=%t", common, found)
	}
	electrostatics, found := scientificRuntimeWarmupDefinitionByID(biomolecularElectrostaticsRuntimeID)
	if !found || electrostatics.DefaultEnabled || electrostatics.EstimatedInstallBytes >= 1024*1024*1024 {
		t.Fatalf("electrostatics runtime size/default contract=%#v found=%t", electrostatics, found)
	}
}

func TestRunScientificRuntimeWarmupRetriesThenReusesReadyEnvironment(t *testing.T) {
	attempts := 0
	states := []string{}
	status := runScientificRuntimeWarmup(
		context.Background(),
		[]time.Duration{0, 0, 0},
		func(context.Context) (scientificRuntimeWarmupResult, error) {
			attempts++
			if attempts < 3 {
				return scientificRuntimeWarmupResult{}, software.NewOperationError(
					"software_install_timeout", "bounded timeout", "retry", true,
				)
			}
			return scientificRuntimeWarmupResult{Environment: "swr-ready", Generation: "generation-1"}, nil
		},
		func(current scientificRuntimeWarmupStatus) { states = append(states, current.State) },
	)

	if attempts != 3 || status.State != "ready" || status.Environment != "swr-ready" || status.Generation != "generation-1" {
		t.Fatalf("unexpected warmup result: attempts=%d status=%+v", attempts, status)
	}
	if !warmupContainsString(states, "preparing") || states[len(states)-1] != "ready" {
		t.Fatalf("unexpected warmup states: %v", states)
	}
}

func TestRunScientificRuntimeWarmupStopsOnPermanentFailure(t *testing.T) {
	attempts := 0
	status := runScientificRuntimeWarmup(
		context.Background(),
		[]time.Duration{0, 0, 0},
		func(context.Context) (scientificRuntimeWarmupResult, error) {
			attempts++
			return scientificRuntimeWarmupResult{}, software.NewOperationError(
				"software_dependency_unavailable", "missing dependency", "change plan", false,
			)
		},
		nil,
	)

	if attempts != 1 || status.State != "failed" || status.LastErrorCode != "software_dependency_unavailable" {
		t.Fatalf("unexpected permanent failure handling: attempts=%d status=%+v", attempts, status)
	}
}

func TestRunScientificRuntimeWarmupHonorsServiceCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	status := runScientificRuntimeWarmup(
		ctx,
		[]time.Duration{time.Hour},
		func(context.Context) (scientificRuntimeWarmupResult, error) {
			called = true
			return scientificRuntimeWarmupResult{}, errors.New("must not run")
		},
		nil,
	)

	if called || status.State != "stopped" {
		t.Fatalf("cancelled warmup called=%t status=%+v", called, status)
	}
}

func TestScientificRuntimeWarmupHealthOmitsInstallerDiagnostics(t *testing.T) {
	server := &Server{scientificRuntimeWarmups: map[string]scientificRuntimeWarmupStatus{}}
	server.setScientificRuntimeWarmupStatus(structureInteractionRuntimeID, scientificRuntimeWarmupStatus{
		State: "retrying", Attempt: 2, MaxAttempts: 4,
		LastErrorCode: "software_install_timeout", RetryAt: time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
	})
	health := scientificRuntimeWarmupHealthValue(server.scientificRuntimeWarmupStatus(structureInteractionRuntimeID))
	if health["status"] != "retrying" || health["last_error_code"] != "software_install_timeout" || health["retry_at"] == "" {
		t.Fatalf("unexpected health snapshot: %#v", health)
	}
	if _, leaked := health["message"]; leaked {
		t.Fatalf("health snapshot leaked installer diagnostics: %#v", health)
	}
}

func TestScientificRuntimeWarmupSelectionAPIPersistsOnlyRegisteredChoices(t *testing.T) {
	server := New(Options{FileRoot: t.TempDir()})
	app := server.Handler()

	initial := runtimeCompatJSON(t, app, http.MethodGet, "/api/preferences/scientific-runtimes", "local", nil, http.StatusOK)
	if initial["configured"] != false {
		t.Fatalf("initial runtime selection=%#v", initial)
	}
	for _, retiredLimit := range []string{"automatic_limit_bytes", "automatic_limit_mb", "selection_limit_bytes", "selection_limit_mb"} {
		if _, found := initial[retiredLimit]; found {
			t.Fatalf("runtime selection reintroduced fixed size ceiling %q: %#v", retiredLimit, initial)
		}
	}
	options, ok := initial["options"].([]any)
	if !ok || len(options) != len(scientificRuntimeWarmupDefinitions())+2 {
		t.Fatalf("runtime options=%#v", initial["options"])
	}
	for _, raw := range options {
		option, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("runtime option is not an object: %#v", raw)
		}
		if option["required"] == true {
			if option["selected"] != true || option["default_enabled"] != true || option["kind"] != "core" {
				t.Fatalf("required runtime contract=%#v", option)
			}
		} else if option["selected"] != false || option["default_enabled"] != false || option["kind"] != "optional" {
			t.Fatalf("optional runtime is selected by default: %#v", raw)
		}
		if _, found := option["requires_user_selection"]; found {
			t.Fatalf("runtime option reintroduced a size-gated selection contract: %#v", raw)
		}
	}
	// Clients may submit the complete catalog, including required rows; only
	// optional choices are persisted.
	runtimeCompatJSON(t, app, http.MethodPut, "/api/preferences/scientific-runtimes", "local", map[string]any{
		"enabled_ids": []string{managedPythonScientificRuntimeID, managedRScientificRuntimeID},
	}, http.StatusOK)
	selected, configured, err := server.loadScientificRuntimeWarmupSelection()
	if err != nil || !configured || len(selected) != 0 {
		t.Fatalf("required runtime selection was persisted: %v configured=%t err=%v", selected, configured, err)
	}

	stored := runtimeCompatJSON(t, app, http.MethodPut, "/api/preferences/scientific-runtimes", "local", map[string]any{
		"enabled_ids": []string{autoDockVinaRuntimeID},
	}, http.StatusOK)
	if stored["configured"] != true {
		t.Fatalf("stored runtime selection=%#v", stored)
	}
	selected, configured, err = server.loadScientificRuntimeWarmupSelection()
	if err != nil || !configured || len(selected) != 1 || selected[0] != autoDockVinaRuntimeID {
		t.Fatalf("persisted selection=%v configured=%t err=%v", selected, configured, err)
	}
	if status := server.scientificRuntimeWarmupStatus(autoDockVinaRuntimeID); status.State != "disabled" {
		t.Fatalf("selected Vina status=%+v", status)
	}

	runtimeCompatJSON(t, app, http.MethodPut, "/api/preferences/scientific-runtimes", "local", map[string]any{
		"enabled_ids": []string{"invented-runtime"},
	}, http.StatusBadRequest)
}

func warmupContainsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

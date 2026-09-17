package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	compute "synon-go/internal/compute"
	secretstore "synon-go/internal/persistence/secrets"
	workspace "synon-go/internal/persistence/workspace"
)

func TestComputeBioNeMoSettingsRequireOwnerCredentialAndPersist(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := New(Options{Workspace: store, FileRoot: root})
	mux := http.NewServeMux()
	server.registerComputeWorkbenchRoutes(mux)

	defaults := requestJSON(t, mux, http.MethodGet, "/api/compute/bionemo/enabled", nil, "owner-a", http.StatusOK).(map[string]any)
	if defaults["enabled"] != false || defaults["override"] != nil || defaults["mode"] != "local" ||
		defaults["hostedHost"] != "health.api.nvidia.com" {
		t.Fatalf("defaults=%#v", defaults)
	}
	missing := requestCompute(t, mux, http.MethodPut, "/api/compute/bionemo/enabled", map[string]any{
		"enabled": true, "mode": "hosted",
	}, "owner-a")
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("missing credential status=%d body=%s", missing.Code, missing.Body.String())
	}
	if _, err := server.secretStore.Create(secretstore.Secret{
		ID: "NVIDIA_API_KEY", UserID: "owner-a", Provider: "nvidia", Value: "test-key",
	}); err != nil {
		t.Fatal(err)
	}
	enabled := requestJSON(t, mux, http.MethodPut, "/api/compute/bionemo/enabled", map[string]any{
		"enabled": true, "mode": "hosted", "hostedHost": "https://API.NVIDIA.EXAMPLE/",
	}, "owner-a", http.StatusOK).(map[string]any)
	if enabled["enabled"] != true || enabled["mode"] != "hosted" ||
		enabled["hostedHost"] != "api.nvidia.example" {
		t.Fatalf("enabled=%#v", enabled)
	}
	persisted := requestJSON(t, mux, http.MethodGet, "/api/compute/bionemo/enabled", nil, "owner-a", http.StatusOK).(map[string]any)
	if persisted["enabled"] != true || persisted["hostedHost"] != "api.nvidia.example" {
		t.Fatalf("persisted=%#v", persisted)
	}
	foreign := requestCompute(t, mux, http.MethodPut, "/api/compute/bionemo/enabled", map[string]any{
		"enabled": true, "mode": "local",
	}, "owner-b")
	if foreign.Code != http.StatusBadRequest {
		t.Fatalf("foreign credential status=%d body=%s", foreign.Code, foreign.Body.String())
	}
	invalidMode := requestCompute(t, mux, http.MethodPut, "/api/compute/bionemo/enabled", map[string]any{
		"enabled": false, "mode": "remote",
	}, "owner-a")
	if invalidMode.Code != http.StatusBadRequest {
		t.Fatalf("invalid mode status=%d body=%s", invalidMode.Code, invalidMode.Body.String())
	}
	invalidHost := requestCompute(t, mux, http.MethodPut, "/api/compute/bionemo/enabled", map[string]any{
		"enabled": false, "hostedHost": "http://api.nvidia.example",
	}, "owner-a")
	if invalidHost.Code != http.StatusBadRequest {
		t.Fatalf("invalid host status=%d body=%s", invalidHost.Code, invalidHost.Body.String())
	}
	var stopCalled atomic.Bool
	stopServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stopCalled.Store(true)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer stopServer.Close()
	registration := compute.ManagedEndpointRegistration{
		Name: "bionemo-fixture", URL: "http://127.0.0.1:9444", Port: 9444,
		CredentialName: "NVIDIA_API_KEY", SkillName: "using-model-endpoint",
		StartScript: "start", StopScript: "/usr/bin/curl -fsS -X POST '" + stopServer.URL + "'", LivePath: "/health",
	}
	now := time.Now().UTC()
	if err := store.UpsertManagedEndpoint(workspace.ManagedEndpoint{
		Name: registration.Name, RegisteredBy: "owner-a", URL: registration.URL, Port: registration.Port,
		State: "live", SkillName: registration.SkillName, CredentialName: &registration.CredentialName,
		LivePath: registration.LivePath, StartScript: registration.StartScript, StopScript: registration.StopScript,
		ApprovedScriptHash: compute.ApprovedManagedEndpointHash(registration), StateChangedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}
	disabled := requestJSON(t, mux, http.MethodPut, "/api/compute/bionemo/enabled", map[string]any{
		"enabled": false, "mode": "local",
	}, "owner-a", http.StatusOK).(map[string]any)
	if disabled["enabled"] != false || disabled["teardown"] == nil {
		t.Fatalf("disabled=%#v", disabled)
	}
	teardown := disabled["teardown"].(map[string]any)
	removed := teardown["removed"].([]any)
	if len(removed) != 1 || removed[0] != "bionemo-fixture" || len(teardown["failed"].([]any)) != 0 {
		t.Fatalf("teardown=%#v", teardown)
	}
	if !stopCalled.Load() {
		t.Fatal("BioNeMo disable did not execute the approved stop script")
	}
	if endpoints, err := store.ListManagedEndpoints("owner-a", "", false); err != nil || len(endpoints) != 0 {
		t.Fatalf("endpoints after teardown=%#v err=%v", endpoints, err)
	}
	method := requestCompute(t, mux, http.MethodPost, "/api/compute/bionemo/enabled", nil, "owner-a")
	if method.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method status=%d body=%s", method.Code, method.Body.String())
	}
}

func TestComputeBioNeMoDisableKeepsConsentWhenTeardownFails(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.SetComputeBioNeMoSettings(workspace.ComputeBioNeMoSettings{
		Enabled: true, Mode: "local", HostedHost: compute.DefaultBioNeMoHostedHost,
	}); err != nil {
		t.Fatal(err)
	}
	registration := compute.ManagedEndpointRegistration{
		Name: "drifted-fixture", URL: "http://127.0.0.1:9555", Port: 9555,
		CredentialName: "NVIDIA_API_KEY", SkillName: "using-model-endpoint",
		StartScript: "start", StopScript: "exit 0", LivePath: "/health",
	}
	now := time.Now().UTC()
	if err := store.UpsertManagedEndpoint(workspace.ManagedEndpoint{
		Name: registration.Name, RegisteredBy: "owner-a", URL: registration.URL, Port: registration.Port,
		State: "live", SkillName: registration.SkillName, CredentialName: &registration.CredentialName,
		LivePath: registration.LivePath, StartScript: registration.StartScript, StopScript: registration.StopScript,
		ApprovedScriptHash: "approval-drift", StateChangedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{Workspace: store, FileRoot: root})
	mux := http.NewServeMux()
	server.registerComputeWorkbenchRoutes(mux)

	response := requestJSON(t, mux, http.MethodPut, "/api/compute/bionemo/enabled", map[string]any{
		"enabled": false,
	}, "owner-a", http.StatusOK).(map[string]any)
	if response["enabled"] != true || response["incomplete"] != true {
		t.Fatalf("response=%#v", response)
	}
	settings, err := store.GetComputeBioNeMoSettings()
	if err != nil || !settings.Enabled {
		t.Fatalf("settings=%#v err=%v", settings, err)
	}
	endpoints, err := store.ListManagedEndpoints("owner-a", "drifted-fixture", false)
	if err != nil || len(endpoints) != 1 || endpoints[0].State != "failed" {
		t.Fatalf("endpoints=%#v err=%v", endpoints, err)
	}
}

package workspace

import (
	"path/filepath"
	"testing"
	"time"
)

func TestMCPGrantAndModelProviderAreUserScoped(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	provider, err := store.RegisterModelProvider(ModelProviderInput{ID: "provider-1", UserID: "user-1", Name: "primary", Type: "openai-compatible", BaseURL: "https://models.example.test/v1", Model: "model-a", SecretRef: "secret://primary"})
	if err != nil {
		t.Fatalf("register model provider: %v", err)
	}
	if !provider.Enabled || provider.SecretRef != "secret://primary" {
		t.Fatalf("provider = %#v", provider)
	}
	server, err := store.CreateMCPServer(MCPServerInput{ID: "mcp-1", UserID: "user-1", Name: "evidence", URL: "https://mcp.example.test", Transport: "streamable-http"})
	if err != nil {
		t.Fatalf("create mcp server: %v", err)
	}
	grant, err := store.SetMCPToolGrant(MCPToolGrantInput{ID: "grant-1", MCPServerID: server.ID, UserID: "user-1", AgentName: "research", ToolName: "search", Enabled: true})
	if err != nil {
		t.Fatalf("set mcp grant: %v", err)
	}
	if !grant.Enabled || grant.MCPServerID != server.ID {
		t.Fatalf("grant = %#v", grant)
	}
	if _, err := store.SetMCPToolGrant(MCPToolGrantInput{ID: "grant-2", MCPServerID: server.ID, UserID: "user-2", AgentName: "research", ToolName: "search", Enabled: true}); err == nil {
		t.Fatal("cross-user mcp grant was accepted")
	}
}

func TestModelProviderGenerationControlsPersistAcrossUpsertAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	temperature := 0.35
	maxTokens := 32768
	provider, err := store.RegisterModelProvider(ModelProviderInput{
		ID: "provider-generation", UserID: "user-1", Name: "primary", Type: "openai-compatible",
		BaseURL: "https://models.example.test/v1", Model: "model-a", SecretRef: "secret://primary",
		Temperature: &temperature, MaxTokens: &maxTokens,
	})
	if err != nil {
		t.Fatalf("register model provider: %v", err)
	}
	if provider.Temperature == nil || *provider.Temperature != temperature || provider.MaxTokens == nil || *provider.MaxTokens != maxTokens {
		t.Fatalf("provider generation controls = %#v", provider)
	}

	updated, err := store.UpsertModelProvider(ModelProviderInput{
		ID: provider.ID, UserID: provider.UserID, Name: "renamed", Type: provider.Type,
		BaseURL: provider.BaseURL, Model: provider.Model, SecretRef: provider.SecretRef,
	})
	if err != nil {
		t.Fatalf("upsert provider without generation controls: %v", err)
	}
	if updated.Temperature == nil || *updated.Temperature != temperature || updated.MaxTokens == nil || *updated.MaxTokens != maxTokens {
		t.Fatalf("upsert lost generation controls = %#v", updated)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	persisted, found, err := reopened.GetModelProvider("user-1", provider.ID)
	if err != nil || !found {
		t.Fatalf("get reopened provider found=%v err=%v", found, err)
	}
	if persisted.Temperature == nil || *persisted.Temperature != temperature || persisted.MaxTokens == nil || *persisted.MaxTokens != maxTokens {
		t.Fatalf("reopened provider generation controls = %#v", persisted)
	}
}

func TestComputeUsageUsesValidatedStateTransitions(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.UpsertComputeProvider(ComputeProviderInput{Name: "local", Family: "local", Environments: []string{"python"}}); err != nil {
		t.Fatalf("upsert compute provider: %v", err)
	}
	usage, err := store.CreateComputeUsage(ComputeUsageInput{ID: "usage-1", JobID: "job-1", Environment: "python", TierType: "cpu", Provider: "local", StartedAt: time.Now().UTC()})
	if err != nil {
		t.Fatalf("create compute usage: %v", err)
	}
	if usage.State != ComputeStatePending {
		t.Fatalf("initial compute state = %q", usage.State)
	}
	if _, err := store.UpdateComputeUsageState(usage.ID, ComputeStateDone, time.Now().UTC()); err == nil {
		t.Fatal("pending job transitioned directly to done")
	}
	running, err := store.UpdateComputeUsageState(usage.ID, ComputeStateRunning, time.Now().UTC())
	if err != nil {
		t.Fatalf("transition to running: %v", err)
	}
	finished, err := store.UpdateComputeUsageState(running.ID, ComputeStateDone, time.Now().UTC())
	if err != nil {
		t.Fatalf("transition to done: %v", err)
	}
	if finished.EndedAt == nil || finished.State != ComputeStateDone {
		t.Fatalf("finished usage = %#v", finished)
	}
}

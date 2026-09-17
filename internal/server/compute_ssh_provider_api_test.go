package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestComputeSSHProviderHTTPFullLifecycleWithRealConfigAndSQLite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "config"), []byte("Host research-*\n  HostName cluster.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(sshDir, "id_research")
	if err := os.WriteFile(identity, []byte("fixture-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := New(Options{Workspace: store, FileRoot: t.TempDir()}).Handler()

	created := computeCompatRequest(t, app, http.MethodPost, "/api/compute/ssh-hosts", map[string]any{
		"alias": "research-a", "initialContext": "approved by operator",
		"dataRoots": []string{"/datasets", "relative"},
		"overrides": map[string]any{"user": "scientist", "port": 2222, "identityFile": "~/.ssh/id_research"},
	})
	if created.Code != http.StatusNoContent || created.Body.Len() != 0 {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	provider, found, err := store.GetComputeProvider("ssh:research-a")
	if err != nil || !found {
		t.Fatalf("provider=%#v found=%v err=%v", provider, found, err)
	}
	if provider.Family != "ssh" || len(provider.DataRoots) != 1 || provider.DataRoots[0] != "/datasets" ||
		provider.SSHOverrides["identityFile"] != identity || provider.SSHOverrides["port"] != float64(2222) {
		t.Fatalf("persisted provider=%#v", provider)
	}
	repeated := computeCompatRequest(t, app, http.MethodPost, "/api/compute/ssh-hosts", map[string]any{
		"alias": "research-a", "initialContext": "second note",
	})
	if repeated.Code != http.StatusNoContent {
		t.Fatalf("repeat status=%d body=%s", repeated.Code, repeated.Body.String())
	}
	provider, found, err = store.GetComputeProvider("ssh:research-a")
	if err != nil || !found || len(provider.DataRoots) != 1 || provider.SSHOverrides["identityFile"] != identity ||
		provider.DetailsMD != "## User-provided context\napproved by operator\n\n## User-provided context\nsecond note" {
		t.Fatalf("provider after repeated add=%#v found=%v err=%v", provider, found, err)
	}

	detail := computeCompatRequest(t, app, http.MethodGet, "/api/compute/providers/ssh:research-a", nil)
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	var value map[string]any
	if err := json.NewDecoder(detail.Body).Decode(&value); err != nil {
		t.Fatal(err)
	}
	if value["name"] != "ssh:research-a" || value["family"] != "ssh" ||
		value["detailsMd"] != "## User-provided context\napproved by operator\n\n## User-provided context\nsecond note" ||
		len(value["dataRoots"].([]any)) != 1 {
		t.Fatalf("detail=%#v", value)
	}
	for _, private := range []string{"userId", "sshOverrides", "detailsRev", "updatedAt"} {
		if _, leaked := value[private]; leaked {
			t.Fatalf("detail leaked %s: %#v", private, value)
		}
	}

	patched := computeCompatRequest(t, app, http.MethodPatch, "/api/compute/providers/ssh:research-a", map[string]any{
		"detailsMd": "operator notes", "maxConcurrentJobs": 3, "maxTimeoutSec": 7200,
	})
	if patched.Code != http.StatusNoContent {
		t.Fatalf("patch status=%d body=%s", patched.Code, patched.Body.String())
	}
	roots := computeCompatRequest(t, app, http.MethodPut, "/api/compute/providers/ssh:research-a/data-roots", map[string]any{
		"roots": []string{"/new/data", "relative", " /new/data/../archive "},
	})
	if roots.Code != http.StatusNoContent {
		t.Fatalf("roots status=%d body=%s", roots.Code, roots.Body.String())
	}
	scratch := computeCompatRequest(t, app, http.MethodPut, "/api/compute/providers/ssh:research-a/scratch-root", map[string]any{
		"scratchRoot": "/scratch/jobs",
	})
	if scratch.Code != http.StatusNoContent {
		t.Fatalf("scratch status=%d body=%s", scratch.Code, scratch.Body.String())
	}
	provider, found, err = store.GetComputeProvider("ssh:research-a")
	if err != nil || !found || provider.DetailsMD != "operator notes" || provider.MaxConcurrentJobs == nil ||
		*provider.MaxConcurrentJobs != 3 || provider.MaxTimeoutSec == nil || *provider.MaxTimeoutSec != 7200 ||
		provider.ScratchRoot != "/scratch/jobs" || len(provider.DataRoots) != 2 || provider.DataRoots[1] != "/new/archive" {
		t.Fatalf("updated provider=%#v found=%v err=%v", provider, found, err)
	}

	foreignRequest := httptest.NewRequest(http.MethodGet, "/api/compute/providers/ssh:research-a", nil)
	missingRoots := computeCompatRequest(t, app, http.MethodPut, "/api/compute/providers/ssh:research-a/data-roots", map[string]any{})
	if missingRoots.Code != http.StatusBadRequest {
		t.Fatalf("missing roots status=%d body=%s", missingRoots.Code, missingRoots.Body.String())
	}
	missingScratch := computeCompatRequest(t, app, http.MethodPut, "/api/compute/providers/ssh:research-a/scratch-root", map[string]any{})
	if missingScratch.Code != http.StatusBadRequest {
		t.Fatalf("missing scratch status=%d body=%s", missingScratch.Code, missingScratch.Body.String())
	}
	foreignRequest.Header.Set("X-Synon-User-Id", "owner-b")
	foreignResponse := httptest.NewRecorder()
	app.ServeHTTP(foreignResponse, foreignRequest)
	if foreignResponse.Code != http.StatusNotFound {
		t.Fatalf("foreign status=%d body=%s", foreignResponse.Code, foreignResponse.Body.String())
	}
	invalid := computeCompatRequest(t, app, http.MethodPost, "/api/compute/ssh-hosts", map[string]any{"alias": "not-configured"})
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid alias status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	deleted := computeCompatRequest(t, app, http.MethodDelete, "/api/compute/providers/ssh:research-a", nil)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	afterDelete := computeCompatRequest(t, app, http.MethodGet, "/api/compute/providers/ssh:research-a", nil)
	if afterDelete.Code != http.StatusNotFound {
		t.Fatalf("after delete status=%d body=%s", afterDelete.Code, afterDelete.Body.String())
	}
}

func TestComputeProviderRejectsInvalidLimitsAndNonSSHScratchRoot(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := New(Options{Workspace: store}).Handler()
	computeCompatJSON(t, app, http.MethodPost, "/api/compute/inference-providers", map[string]any{
		"name": "limits", "endpoint": 9444, "skillName": "using-model-endpoint",
	}, http.StatusOK)
	invalidLimit := computeCompatRequest(t, app, http.MethodPatch, "/api/compute/providers/infer:limits", map[string]any{"maxConcurrentJobs": 65})
	if invalidLimit.Code != http.StatusBadRequest {
		t.Fatalf("invalid limit status=%d body=%s", invalidLimit.Code, invalidLimit.Body.String())
	}
	scratch := computeCompatRequest(t, app, http.MethodPut, "/api/compute/providers/infer:limits/scratch-root", map[string]any{"scratchRoot": "/tmp"})
	if scratch.Code != http.StatusBadRequest {
		t.Fatalf("infer scratch status=%d body=%s", scratch.Code, scratch.Body.String())
	}
	deleted := computeCompatRequest(t, app, http.MethodDelete, "/api/compute/providers/infer:limits", nil)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("generic inference delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	afterDelete := computeCompatRequest(t, app, http.MethodGet, "/api/compute/providers/infer:limits", nil)
	if afterDelete.Code != http.StatusNotFound {
		t.Fatalf("generic inference delete retained provider: %d body=%s", afterDelete.Code, afterDelete.Body.String())
	}
}

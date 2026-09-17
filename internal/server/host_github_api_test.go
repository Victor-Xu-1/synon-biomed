package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
)

func TestCompatibilitySecretHostGitHubCredentialLifecycle(t *testing.T) {
	fixtureDir, err := filepath.Abs(filepath.Join("testdata", "host-github"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fixtureDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	t.Run("gh discovery and encrypted import", func(t *testing.T) {
		t.Setenv("SYNON_HOST_GITHUB_FIXTURE_MODE", "gh")
		root := t.TempDir()
		server := New(Options{FileRoot: root})
		app := server.Handler()

		probe := compatibilitySecretObjectRequest(t, app, http.MethodGet, "/api/credentials/github/host-probe", "owner", nil, http.StatusOK)
		if len(probe) != 4 || probe["found"] != true || probe["login"] != "oracle-user" ||
			probe["masked_preview"] != "ghp_\u00b7\u00b7\u00b7\u00b7CDEF" || probe["source"] != "gh" {
			t.Fatalf("host probe = %#v", probe)
		}
		assertCompatibilitySecretRedacted(t, probe, "ghp_1234567890ABCDEF")

		created := compatibilitySecretObjectRequest(t, app, http.MethodPost, "/api/credentials/github/use-host", "owner", map[string]any{}, http.StatusCreated)
		assertCompatibilitySecretProjection(t, created, "github-oracle-user", "github")
		if created["credential_type"] != "host_discovered" ||
			created["description"] != "Discovered from host `gh auth token`" ||
			created["masked_preview"] != "ghp_\u00b7\u00b7\u00b7\u00b7CDEF" {
			t.Fatalf("imported host credential = %#v", created)
		}
		id := created["id"].(string)
		stored, found, err := server.secretStore.ResolveForUser(id, "owner")
		if err != nil || !found {
			t.Fatalf("resolve imported host credential: found=%v err=%v", found, err)
		}
		credentials := stored.CredentialObject()
		if credentials["token"] != "ghp_1234567890ABCDEF" || credentials["login"] != "oracle-user" ||
			credentials["auto_discovered"] != true || credentials["source"] != "gh" {
			t.Fatalf("stored host credentials = %#v", credentials)
		}
		vault, err := os.ReadFile(filepath.Join(root, "secrets", "vault.enc"))
		if err != nil || bytes.Contains(vault, []byte("ghp_1234567890ABCDEF")) {
			t.Fatalf("host credential plaintext in vault: err=%v", err)
		}

		restarted := New(Options{FileRoot: root}).Handler()
		afterRestart := compatibilitySecretArrayRequest(t, restarted, http.MethodGet, "/api/secrets", "owner", nil, http.StatusOK)
		if len(afterRestart) != 1 || afterRestart[0]["id"] != id {
			t.Fatalf("host credential after restart = %#v", afterRestart)
		}
		configured := compatibilitySecretObjectRequest(t, restarted, http.MethodGet, "/api/credentials/github/host-probe", "owner", nil, http.StatusOK)
		if len(configured) != 2 || configured["found"] != false || configured["reason"] != "already_configured" {
			t.Fatalf("configured probe = %#v", configured)
		}
		duplicate := compatibilitySecretObjectRequest(t, restarted, http.MethodPost, "/api/credentials/github/use-host", "owner", map[string]any{}, http.StatusConflict)
		if duplicate["detail"] != "A GitHub credential is already configured." {
			t.Fatalf("duplicate import = %#v", duplicate)
		}
	})

	t.Run("git credential fallback", func(t *testing.T) {
		t.Setenv("SYNON_HOST_GITHUB_FIXTURE_MODE", "git")
		app := New(Options{FileRoot: t.TempDir()}).Handler()
		probe := compatibilitySecretObjectRequest(t, app, http.MethodGet, "/api/credentials/github/host-probe", "owner", nil, http.StatusOK)
		if probe["found"] != true || probe["login"] != "git-user" || probe["source"] != "git-credential" ||
			probe["masked_preview"] != "gith\u00b7\u00b7\u00b7\u00b7IJKL" {
			t.Fatalf("git credential probe = %#v", probe)
		}
		created := compatibilitySecretObjectRequest(t, app, http.MethodPost, "/api/credentials/github/use-host", "owner", map[string]any{}, http.StatusCreated)
		if created["name"] != "github-git-user" || created["description"] != "Discovered from host `git credential fill`" {
			t.Fatalf("git credential import = %#v", created)
		}
	})

	t.Run("no host credential", func(t *testing.T) {
		t.Setenv("SYNON_HOST_GITHUB_FIXTURE_MODE", "none")
		app := New(Options{FileRoot: t.TempDir()}).Handler()
		probe := compatibilitySecretObjectRequest(t, app, http.MethodGet, "/api/credentials/github/host-probe", "owner", nil, http.StatusOK)
		if len(probe) != 1 || probe["found"] != false {
			t.Fatalf("empty host probe = %#v", probe)
		}
		missing := compatibilitySecretObjectRequest(t, app, http.MethodPost, "/api/credentials/github/use-host", "owner", map[string]any{}, http.StatusNotFound)
		if missing["detail"] != "No host GitHub credential found." {
			t.Fatalf("empty host import = %#v", missing)
		}
	})

	t.Run("concurrent import is unique", func(t *testing.T) {
		t.Setenv("SYNON_HOST_GITHUB_FIXTURE_MODE", "gh")
		app := New(Options{FileRoot: t.TempDir()}).Handler()
		requests := []*http.Request{
			compatibilitySecretRequest(t, http.MethodPost, "/api/credentials/github/use-host", "owner", map[string]any{}),
			compatibilitySecretRequest(t, http.MethodPost, "/api/credentials/github/use-host", "owner", map[string]any{}),
		}
		start := make(chan struct{})
		statuses := make(chan int, len(requests))
		var wait sync.WaitGroup
		for _, request := range requests {
			wait.Add(1)
			go func(request *http.Request) {
				defer wait.Done()
				<-start
				response := httptest.NewRecorder()
				app.ServeHTTP(response, request)
				statuses <- response.Code
			}(request)
		}
		close(start)
		wait.Wait()
		close(statuses)
		got := make([]int, 0, len(requests))
		for status := range statuses {
			got = append(got, status)
		}
		sort.Ints(got)
		if len(got) != 2 || got[0] != http.StatusCreated || got[1] != http.StatusConflict {
			t.Fatalf("concurrent host import statuses = %#v", got)
		}
		if listed := compatibilitySecretArrayRequest(t, app, http.MethodGet, "/api/secrets", "owner", nil, http.StatusOK); len(listed) != 1 {
			t.Fatalf("concurrent host imports = %#v", listed)
		}
	})
}

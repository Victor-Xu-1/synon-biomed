package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHostGrantAdmissionRejectsProtectedKernelNamespaceWithoutChangingGrants(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("Linux kernel namespace contract")
	}
	s := New(Options{FileRoot: t.TempDir()})
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	safe := t.TempDir()
	if _, err := s.upsertHostGrant("owner", safe, "read"); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "namespace-alias")
	if err := os.Symlink("/tmp", alias); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"/api/preferences/host-grants", "/api/preferences/host-grants/picker", "/api/go/preferences/host-grants"} {
		for _, path := range []string{"/tmp", "/proc", alias} {
			mode := "rw"
			if strings.Contains(route, "/go/") {
				mode = "read_write"
			}
			runtimeCompatJSON(t, s.Handler(), http.MethodPost, route, "owner", map[string]any{"path": path, "mode": mode}, http.StatusBadRequest)
		}
	}
	grants, err := s.loadHostGrants("owner")
	if err != nil || len(grants) != 1 || grants[0].Path != safe {
		t.Fatalf("rejected grants changed existing permission: %#v %v", grants, err)
	}
	// Historical invalid permissions remain visible and revocable, not silently
	// ignored or rewritten. Changing their mode cannot reinstall them.
	if _, err := s.settingsStore.Set(hostGrantsSettingKeyForUser("owner"), []hostGrant{{ID: "/tmp", Path: "/tmp", Mode: "read_write"}}); err != nil {
		t.Fatal(err)
	}
	runtimeCompatJSON(t, s.Handler(), http.MethodPatch, "/api/preferences/host-grants", "owner", map[string]any{"path": "/tmp", "mode": "ro"}, http.StatusBadRequest)
	if _, err := s.agentKernelConfinementMounts("owner", t.TempDir(), nil); err == nil {
		t.Fatal("legacy invalid grant reached process startup without admission feedback")
	}
	runtimeCompatJSON(t, s.Handler(), http.MethodDelete, "/api/preferences/host-grants", "owner", map[string]any{"path": "/tmp"}, http.StatusOK)
	if grants, err := s.loadHostGrants("owner"); err != nil || len(grants) != 0 {
		t.Fatalf("legacy invalid grant could not be explicitly revoked: %#v %v", grants, err)
	}
}

func TestCompatibilityHostGrantsListCreatePickerAndRestart(t *testing.T) {
	root := t.TempDir()
	direct := filepath.Join(t.TempDir(), "direct-grant")
	picked := filepath.Join(t.TempDir(), "picked-grant")
	for _, directory := range []string{direct, picked} {
		if err := os.MkdirAll(filepath.Join(directory, "child"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	fixtureDir, err := filepath.Abs(filepath.Join("testdata", "host-picker"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fixtureDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SYNON_HOST_PICKER_FIXTURE_MODE", "select")
	t.Setenv("SYNON_HOST_PICKER_FIXTURE_PATH", picked)

	picker := func(ctx context.Context) (string, error) {
		return runHostDirectoryPickerCandidates(ctx, []hostPickerCommand{{
			name: "zenity", args: []string{"--file-selection", "--directory"},
		}})
	}
	app := New(Options{FileRoot: root, HostDirectoryPicker: picker}).Handler()
	empty := runtimeCompatJSON(t, app, http.MethodGet, "/api/preferences/host-grants", "owner", nil, http.StatusOK)
	if len(empty) != 1 || len(empty["grants"].([]any)) != 0 {
		t.Fatalf("empty host grants = %#v", empty)
	}
	missingPath := runtimeCompatJSON(t, app, http.MethodPost, "/api/preferences/host-grants", "owner", map[string]any{
		"mode": "ro",
	}, http.StatusBadRequest)
	if missingPath["detail"] != "path: string required" {
		t.Fatalf("missing path = %#v", missingPath)
	}
	invalidMode := runtimeCompatJSON(t, app, http.MethodPost, "/api/preferences/host-grants", "owner", map[string]any{
		"path": direct, "mode": "read",
	}, http.StatusBadRequest)
	if invalidMode["detail"] != "mode: 'ro' | 'rw' required" {
		t.Fatalf("invalid mode = %#v", invalidMode)
	}
	relative := runtimeCompatJSON(t, app, http.MethodPost, "/api/preferences/host-grants", "owner", map[string]any{
		"path": "relative/path", "mode": "ro",
	}, http.StatusBadRequest)
	if relative["detail"] != "Directory could not be accessed." {
		t.Fatalf("relative path = %#v", relative)
	}

	created := runtimeCompatJSON(t, app, http.MethodPost, "/api/preferences/host-grants", "owner", map[string]any{
		"path": direct, "mode": "ro",
	}, http.StatusOK)
	assertCompatibilityHostGrant(t, created, direct, "ro")
	pickerGrant := runtimeCompatJSON(t, app, http.MethodPost, "/api/preferences/host-grants/picker", "owner", map[string]any{
		"mode": "rw",
	}, http.StatusOK)
	assertCompatibilityHostGrant(t, pickerGrant, picked, "rw")

	foreign := runtimeCompatJSON(t, app, http.MethodGet, "/api/preferences/host-grants", "foreign", nil, http.StatusOK)
	if len(foreign["grants"].([]any)) != 0 {
		t.Fatalf("foreign host grants = %#v", foreign)
	}
	listed := runtimeCompatJSON(t, app, http.MethodGet, "/api/preferences/host-grants", "owner", nil, http.StatusOK)
	grants := listed["grants"].([]any)
	if len(grants) != 2 {
		t.Fatalf("listed host grants = %#v", listed)
	}
	paths := []string{grants[0].(map[string]any)["hostPath"].(string), grants[1].(map[string]any)["hostPath"].(string)}
	if !sort.StringsAreSorted(paths) {
		t.Fatalf("host grants are not sorted = %#v", paths)
	}

	restarted := New(Options{FileRoot: root}).Handler()
	afterRestart := runtimeCompatJSON(t, restarted, http.MethodGet, "/api/preferences/host-grants", "owner", nil, http.StatusOK)
	if len(afterRestart["grants"].([]any)) != 2 {
		t.Fatalf("host grants after restart = %#v", afterRestart)
	}
	legacy := runtimeCompatJSON(t, restarted, http.MethodGet, "/api/go/preferences/host-grants", "owner", nil, http.StatusOK)
	if legacy["ok"] != true || len(legacy["grants"].([]any)) != 2 {
		t.Fatalf("legacy host grant API = %#v", legacy)
	}

	t.Setenv("SYNON_HOST_PICKER_FIXTURE_MODE", "cancel")
	response := httptest.NewRecorder()
	request := compatibilitySecretRequest(t, http.MethodPost, "/api/preferences/host-grants/picker", "cancelled-user", map[string]any{"mode": "ro"})
	app.ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != "null" {
		t.Fatalf("cancelled picker status=%d body=%s", response.Code, response.Body.String())
	}
	cancelled := runtimeCompatJSON(t, app, http.MethodGet, "/api/preferences/host-grants", "cancelled-user", nil, http.StatusOK)
	if len(cancelled["grants"].([]any)) != 0 {
		t.Fatalf("cancelled picker persisted a grant = %#v", cancelled)
	}
}

func TestCompatibilityHostGrantConcurrentCreateIsUnique(t *testing.T) {
	directory := t.TempDir()
	app := New(Options{FileRoot: t.TempDir()}).Handler()
	requests := []*http.Request{
		compatibilitySecretRequest(t, http.MethodPost, "/api/preferences/host-grants", "owner", map[string]any{"path": directory, "mode": "ro"}),
		compatibilitySecretRequest(t, http.MethodPost, "/api/preferences/host-grants", "owner", map[string]any{"path": directory, "mode": "rw"}),
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
	for status := range statuses {
		if status != http.StatusOK {
			t.Fatalf("concurrent grant status = %d", status)
		}
	}
	listed := runtimeCompatJSON(t, app, http.MethodGet, "/api/preferences/host-grants", "owner", nil, http.StatusOK)
	grants := listed["grants"].([]any)
	if len(grants) != 1 {
		t.Fatalf("concurrent grants = %#v", listed)
	}
	mode := grants[0].(map[string]any)["mode"]
	if mode != "ro" && mode != "rw" {
		t.Fatalf("concurrent grant mode = %#v", mode)
	}
}

func TestCompatibilityHostGrantHomeChangeAndRevoke(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(t.TempDir(), "mode-grant")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	app := New(Options{FileRoot: root}).Handler()
	home := runtimeCompatJSON(t, app, http.MethodGet, "/api/preferences/host-home", "owner", nil, http.StatusOK)
	wantHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if len(home) != 1 || home["path"] != filepath.Clean(wantHome) {
		t.Fatalf("host home = %#v", home)
	}
	runtimeCompatJSON(t, app, http.MethodPost, "/api/preferences/host-grants", "owner", map[string]any{
		"path": directory, "mode": "ro",
	}, http.StatusOK)

	foreignChange := runtimeCompatJSON(t, app, http.MethodPatch, "/api/preferences/host-grants", "foreign", map[string]any{
		"path": directory, "mode": "rw",
	}, http.StatusNotFound)
	if foreignChange["detail"] != "No grant at that path that you can modify." {
		t.Fatalf("foreign mode change = %#v", foreignChange)
	}
	foreignRevoke := runtimeCompatJSON(t, app, http.MethodDelete, "/api/preferences/host-grants", "foreign", map[string]any{
		"path": directory,
	}, http.StatusNotFound)
	if foreignRevoke["detail"] != "No grant at that path that you can revoke." {
		t.Fatalf("foreign revoke = %#v", foreignRevoke)
	}
	ownerList := runtimeCompatJSON(t, app, http.MethodGet, "/api/preferences/host-grants", "owner", nil, http.StatusOK)
	if len(ownerList["grants"].([]any)) != 1 {
		t.Fatalf("owner grant after foreign revoke = %#v", ownerList)
	}

	changed := runtimeCompatJSON(t, app, http.MethodPatch, "/api/preferences/host-grants", "owner", map[string]any{
		"path": directory, "mode": "rw",
	}, http.StatusOK)
	assertCompatibilityHostGrantModeChange(t, changed, directory, "rw")
	restarted := New(Options{FileRoot: root}).Handler()
	afterRestart := runtimeCompatJSON(t, restarted, http.MethodGet, "/api/preferences/host-grants", "owner", nil, http.StatusOK)
	grants := afterRestart["grants"].([]any)
	if len(grants) != 1 || grants[0].(map[string]any)["mode"] != "rw" {
		t.Fatalf("changed grant after restart = %#v", afterRestart)
	}
	sameMode := runtimeCompatJSON(t, restarted, http.MethodPatch, "/api/preferences/host-grants", "owner", map[string]any{
		"path": directory, "mode": "rw",
	}, http.StatusOK)
	assertCompatibilityHostGrantModeChange(t, sameMode, directory, "rw")

	revoked := runtimeCompatJSON(t, restarted, http.MethodDelete, "/api/preferences/host-grants", "owner", map[string]any{
		"path": directory,
	}, http.StatusOK)
	if len(revoked) != 0 {
		t.Fatalf("revoke response = %#v", revoked)
	}
	afterRevokeRestart := New(Options{FileRoot: root}).Handler()
	empty := runtimeCompatJSON(t, afterRevokeRestart, http.MethodGet, "/api/preferences/host-grants", "owner", nil, http.StatusOK)
	if len(empty["grants"].([]any)) != 0 {
		t.Fatalf("grants after revoke restart = %#v", empty)
	}
	repeated := runtimeCompatJSON(t, afterRevokeRestart, http.MethodDelete, "/api/preferences/host-grants", "owner", map[string]any{
		"path": directory,
	}, http.StatusNotFound)
	if repeated["detail"] != "No grant at that path that you can revoke." {
		t.Fatalf("repeat revoke = %#v", repeated)
	}
	missing := runtimeCompatJSON(t, afterRevokeRestart, http.MethodPatch, "/api/preferences/host-grants", "owner", map[string]any{
		"path": directory, "mode": "ro",
	}, http.StatusNotFound)
	if missing["detail"] != "No grant at that path that you can modify." {
		t.Fatalf("missing mode change = %#v", missing)
	}
}

func TestCompatibilityHostGrantConcurrentChangeAndRevokeIsAtomic(t *testing.T) {
	directory := t.TempDir()
	app := New(Options{FileRoot: t.TempDir()}).Handler()
	runtimeCompatJSON(t, app, http.MethodPost, "/api/preferences/host-grants", "owner", map[string]any{
		"path": directory, "mode": "ro",
	}, http.StatusOK)
	requests := []*http.Request{
		compatibilitySecretRequest(t, http.MethodPatch, "/api/preferences/host-grants", "owner", map[string]any{"path": directory, "mode": "rw"}),
		compatibilitySecretRequest(t, http.MethodDelete, "/api/preferences/host-grants", "owner", map[string]any{"path": directory}),
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
	if len(got) != 2 || got[0] != http.StatusOK || (got[1] != http.StatusOK && got[1] != http.StatusNotFound) {
		t.Fatalf("concurrent mode/revoke statuses = %#v", got)
	}
	listed := runtimeCompatJSON(t, app, http.MethodGet, "/api/preferences/host-grants", "owner", nil, http.StatusOK)
	if len(listed["grants"].([]any)) != 0 {
		t.Fatalf("grant survived concurrent revoke = %#v", listed)
	}
}

func TestCompatibilityHostGrantDirectoryBrowseMatchesPublicContract(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	outside := filepath.Join(t.TempDir(), "outside")
	for _, directory := range []string{filepath.Join(home, "z-directory"), outside} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "a-file.txt"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, "m-link")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	app := New(Options{FileRoot: t.TempDir()}).Handler()

	entries := compatibilityHostBrowseJSON(t, app, "/api/preferences/host-browse?path="+url.QueryEscape(home), "owner", http.StatusOK).([]any)
	if len(entries) != 3 {
		t.Fatalf("home entries = %#v", entries)
	}
	want := []struct {
		name      string
		directory bool
	}{{"z-directory", true}, {"a-file.txt", false}, {"m-link", false}}
	for index, expected := range want {
		entry := entries[index].(map[string]any)
		if len(entry) != 2 || entry["name"] != expected.name || entry["isDirectory"] != expected.directory {
			t.Fatalf("entry %d = %#v", index, entry)
		}
	}
	escape := compatibilityHostBrowseJSON(t, app, "/api/preferences/host-browse?path="+url.QueryEscape(filepath.Join(home, "m-link")), "owner", http.StatusBadRequest).(map[string]any)
	if escape["detail"] != "Directory is not under $HOME or a granted root." {
		t.Fatalf("symlink escape = %#v", escape)
	}
	missing := compatibilityHostBrowseJSON(t, app, "/api/preferences/host-browse", "owner", http.StatusBadRequest).(map[string]any)
	if missing["detail"] != "absolute path required" {
		t.Fatalf("missing path = %#v", missing)
	}
	relative := compatibilityHostBrowseJSON(t, app, "/api/preferences/host-browse?path=relative", "owner", http.StatusBadRequest).(map[string]any)
	if relative["detail"] != "path must be absolute" {
		t.Fatalf("relative path = %#v", relative)
	}

	grantRoot := filepath.Join(t.TempDir(), "grant")
	if err := os.MkdirAll(filepath.Join(grantRoot, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	runtimeCompatJSON(t, app, http.MethodPost, "/api/preferences/host-grants", "owner", map[string]any{
		"path": grantRoot, "mode": "ro",
	}, http.StatusOK)
	granted := compatibilityHostBrowseJSON(t, app, "/api/preferences/host-browse?path="+url.QueryEscape(grantRoot), "owner", http.StatusOK).([]any)
	if len(granted) != 1 || granted[0].(map[string]any)["name"] != "child" {
		t.Fatalf("granted entries = %#v", granted)
	}
	foreign := compatibilityHostBrowseJSON(t, app, "/api/preferences/host-browse?path="+url.QueryEscape(grantRoot), "foreign", http.StatusBadRequest).(map[string]any)
	if foreign["detail"] != "Directory is not under $HOME or a granted root." {
		t.Fatalf("foreign browse = %#v", foreign)
	}
}

func compatibilityHostBrowseJSON(t *testing.T, app http.Handler, target, userID string, wantStatus int) any {
	t.Helper()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.Header.Set("X-Synon-User-Id", userID)
	app.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("GET %s status=%d want=%d body=%s", target, response.Code, wantStatus, response.Body.String())
	}
	var decoded any
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode GET %s: %v body=%s", target, err, response.Body.String())
	}
	return decoded
}

func assertCompatibilityHostGrantModeChange(t *testing.T, grant map[string]any, path, mode string) {
	t.Helper()
	if len(grant) != 6 || grant["id"] != path || grant["hostPath"] != path || grant["guestPath"] != path ||
		grant["mountName"] != filepath.Base(path) || grant["mode"] != mode {
		t.Fatalf("host grant mode projection = %#v", grant)
	}
	createdAt, ok := grant["createdAt"].(string)
	if !ok {
		t.Fatalf("host grant createdAt = %#v", grant["createdAt"])
	}
	if _, err := time.Parse("2006-01-02T15:04:05.000Z", createdAt); err != nil {
		t.Fatalf("host grant createdAt = %q: %v", createdAt, err)
	}
}

func assertCompatibilityHostGrant(t *testing.T, grant map[string]any, path, mode string) {
	t.Helper()
	if len(grant) != 5 || grant["id"] != path || grant["hostPath"] != path || grant["guestPath"] != path ||
		grant["mountName"] != filepath.Base(path) || grant["mode"] != mode {
		t.Fatalf("host grant projection = %#v", grant)
	}
	encoded := mustJSON(t, grant)
	if bytes.Contains([]byte(encoded), []byte("createdAt")) || bytes.Contains([]byte(encoded), []byte("updatedAt")) {
		t.Fatalf("host grant leaked internal timestamps = %s", encoded)
	}
}

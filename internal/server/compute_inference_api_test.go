package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

func TestComputeProviderHTTPAuthorizationMatrix(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"ready": true})
	}))
	defer upstream.Close()

	remoteRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(remoteRoot, "shared.txt"), []byte("shared"), 0o600); err != nil {
		t.Fatal(err)
	}
	dialer, _, closeSFTP := startLoopbackSFTPServer(t)
	defer closeSFTP()

	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	for _, input := range []workspace.ComputeProviderInput{
		{Name: "infer:owner", UserID: "owner-a", Family: "infer", Endpoint: upstream.URL, SkillName: "using-model-endpoint", Scheduler: "none"},
		{Name: "infer:foreign", UserID: "owner-b", Family: "infer", Endpoint: upstream.URL, SkillName: "using-model-endpoint", Scheduler: "none"},
		{Name: "infer:global", UserID: "*", Family: "infer", Endpoint: upstream.URL, SkillName: "using-model-endpoint", Scheduler: "none"},
	} {
		if _, err := store.CreateInferenceProvider(input); err != nil {
			t.Fatalf("create %s: %v", input.Name, err)
		}
	}
	for _, input := range []workspace.ComputeProviderInput{
		{Name: "ssh:owner", UserID: "owner-a", Family: "ssh", DetailsMD: "owner details"},
		{Name: "ssh:foreign", UserID: "owner-b", Family: "ssh", DetailsMD: "foreign details"},
		{Name: "ssh:global", UserID: "*", Family: "ssh", DetailsMD: "global details"},
	} {
		if _, err := store.UpsertSSHProvider(input); err != nil {
			t.Fatalf("create %s: %v", input.Name, err)
		}
		if _, err := store.SetComputeProviderScratchRoot(input.Name, input.UserID, &remoteRoot); err != nil {
			t.Fatalf("set %s scratch root: %v", input.Name, err)
		}
	}

	app := New(Options{Workspace: store, HTTPClient: upstream.Client(), ComputeRemoteDialer: dialer}).Handler()

	list := computeCompatRequestAs(t, app, http.MethodGet, "/api/compute/providers", nil, "owner-a")
	if list.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	var listed []map[string]any
	if err := json.NewDecoder(list.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	wantNames := map[string]bool{"infer:global": true, "infer:owner": true, "ssh:global": true, "ssh:owner": true}
	if len(listed) != len(wantNames) {
		t.Fatalf("listed providers=%#v", listed)
	}
	for _, provider := range listed {
		name, _ := provider["name"].(string)
		if !wantNames[name] {
			t.Fatalf("list leaked provider %q: %#v", name, listed)
		}
	}
	conflict := computeCompatRequestAs(t, app, http.MethodPost, "/api/compute/inference-providers", map[string]any{
		"name": "foreign", "endpoint": upstream.URL, "skillName": "using-model-endpoint",
	}, "owner-a")
	if conflict.Code != http.StatusConflict {
		t.Fatalf("foreign create conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	for _, leaked := range []string{"another user", "belongs", "owner-b"} {
		if strings.Contains(strings.ToLower(conflict.Body.String()), leaked) {
			t.Fatalf("foreign create conflict leaked %q: %s", leaked, conflict.Body.String())
		}
	}

	for _, test := range []struct {
		name       string
		path       string
		userID     string
		wantStatus int
	}{
		{name: "owner detail", path: "/api/compute/providers/infer:owner", userID: "owner-a", wantStatus: http.StatusOK},
		{name: "foreign detail", path: "/api/compute/providers/infer:foreign", userID: "owner-a", wantStatus: http.StatusNotFound},
		{name: "global detail", path: "/api/compute/providers/infer:global", userID: "owner-a", wantStatus: http.StatusOK},
		{name: "owner ssh read", path: "/api/compute/providers/ssh:owner/files?path=" + url.QueryEscape(remoteRoot), userID: "owner-a", wantStatus: http.StatusOK},
		{name: "foreign ssh read", path: "/api/compute/providers/ssh:foreign/files?path=" + url.QueryEscape(remoteRoot), userID: "owner-a", wantStatus: http.StatusBadRequest},
		{name: "global ssh read", path: "/api/compute/providers/ssh:global/files?path=" + url.QueryEscape(remoteRoot), userID: "owner-a", wantStatus: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := computeCompatRequestAs(t, app, http.MethodGet, test.path, nil, test.userID)
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}

	for _, test := range []struct {
		name       string
		provider   string
		wantStatus int
	}{
		{name: "owner probe", provider: "infer:owner", wantStatus: http.StatusOK},
		{name: "foreign probe", provider: "infer:foreign", wantStatus: http.StatusNotFound},
		{name: "global probe", provider: "infer:global", wantStatus: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := computeCompatRequestAs(t, app, http.MethodPost, "/api/compute/providers/"+test.provider+"/probe", nil, "owner-a")
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}
	globalInference, found, err := store.GetComputeProvider("infer:global")
	if err != nil || !found || globalInference.UserID != "*" || globalInference.DetailsRev != 1 || globalInference.ProbedAt == nil {
		t.Fatalf("global provider after probe=%#v found=%v err=%v", globalInference, found, err)
	}
	if upstreamCalls.Load() != 2 {
		t.Fatalf("upstream calls=%d want=2", upstreamCalls.Load())
	}

	mutationCases := []struct {
		name   string
		method string
		suffix string
		body   any
	}{
		{name: "patch", method: http.MethodPatch, body: map[string]any{"detailsMd": "changed"}},
		{name: "data roots", method: http.MethodPut, suffix: "/data-roots", body: map[string]any{"roots": []string{"/changed"}}},
		{name: "scratch root", method: http.MethodPut, suffix: "/scratch-root", body: map[string]any{"scratchRoot": "/changed"}},
		{name: "owner transfer", method: http.MethodPatch, body: map[string]any{"ownerUserId": "owner-a"}},
		{name: "delete", method: http.MethodDelete},
	}
	for _, scope := range []struct {
		name       string
		provider   string
		wantStatus int
	}{
		{name: "foreign", provider: "ssh:foreign", wantStatus: http.StatusNotFound},
		{name: "global", provider: "ssh:global", wantStatus: http.StatusForbidden},
	} {
		for _, mutation := range mutationCases {
			t.Run(scope.name+" "+mutation.name, func(t *testing.T) {
				response := computeCompatRequestAs(t, app, mutation.method, "/api/compute/providers/"+scope.provider+mutation.suffix, mutation.body, "owner-a")
				if response.Code != scope.wantStatus {
					t.Fatalf("status=%d want=%d body=%s", response.Code, scope.wantStatus, response.Body.String())
				}
			})
		}
	}

	for _, mutation := range mutationCases[:3] {
		t.Run("owner "+mutation.name, func(t *testing.T) {
			response := computeCompatRequestAs(t, app, mutation.method, "/api/compute/providers/ssh:owner"+mutation.suffix, mutation.body, "owner-a")
			if response.Code != http.StatusNoContent {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	ownerTransfer := computeCompatRequestAs(t, app, http.MethodPatch, "/api/compute/providers/ssh:owner", map[string]any{"ownerUserId": "owner-b"}, "owner-a")
	if ownerTransfer.Code != http.StatusBadRequest {
		t.Fatalf("owner transfer status=%d body=%s", ownerTransfer.Code, ownerTransfer.Body.String())
	}
	ownerDelete := computeCompatRequestAs(t, app, http.MethodDelete, "/api/compute/providers/ssh:owner", nil, "owner-a")
	if ownerDelete.Code != http.StatusNoContent {
		t.Fatalf("owner delete status=%d body=%s", ownerDelete.Code, ownerDelete.Body.String())
	}

	globalSSH, found, err := store.GetComputeProvider("ssh:global")
	if err != nil || !found || globalSSH.UserID != "*" || globalSSH.DetailsMD != "global details" || globalSSH.ScratchRoot != remoteRoot || len(globalSSH.DataRoots) != 0 {
		t.Fatalf("global provider changed=%#v found=%v err=%v", globalSSH, found, err)
	}
	globalInferenceDelete := computeCompatRequestAs(t, app, http.MethodDelete, "/api/compute/inference-providers/global", nil, "owner-a")
	if globalInferenceDelete.Code != http.StatusNotFound {
		t.Fatalf("global inference delete status=%d body=%s", globalInferenceDelete.Code, globalInferenceDelete.Body.String())
	}
}

func TestInferenceProviderCreateThenImmediateAndConcurrentProbe(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ready":true}`))
	}))
	defer upstream.Close()

	database := filepath.Join(t.TempDir(), "workspace.db")
	store, err := workspace.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store, HTTPClient: upstream.Client()}).Handler()
	created := computeCompatJSON(t, app, http.MethodPost, "/api/compute/inference-providers", map[string]any{
		"name": "fixture-infer", "endpoint": upstream.URL, "skillName": "using-model-endpoint",
	}, http.StatusOK)
	if len(created) != 0 {
		t.Fatalf("create response = %#v, want empty object", created)
	}

	probe := computeCompatJSON(t, app, http.MethodPost, "/api/compute/providers/infer:fixture-infer/probe", nil, http.StatusOK)
	if probe["scheduler"] != "none" || probe["cpus"] != float64(0) || probe["gpus"] != float64(0) {
		t.Fatalf("immediate probe = %#v", probe)
	}

	const concurrent = 12
	statuses := make(chan int, concurrent)
	var wait sync.WaitGroup
	for range concurrent {
		wait.Add(1)
		go func() {
			defer wait.Done()
			request := httptest.NewRequest(http.MethodPost, "/api/compute/providers/infer:fixture-infer/probe", nil)
			request.Header.Set("X-Synon-User-Id", "local")
			response := httptest.NewRecorder()
			app.ServeHTTP(response, request)
			statuses <- response.Code
		}()
	}
	wait.Wait()
	close(statuses)
	for status := range statuses {
		if status != http.StatusOK {
			t.Fatalf("concurrent probe status = %d", status)
		}
	}
	provider, found, err := store.GetComputeProvider("infer:fixture-infer")
	if err != nil || !found {
		t.Fatalf("provider found=%v err=%v", found, err)
	}
	if provider.DetailsRev != concurrent+1 || provider.ProbedAt == nil || provider.Endpoint != upstream.URL || provider.SkillName != "using-model-endpoint" {
		t.Fatalf("provider after probes = %#v", provider)
	}
	if upstreamCalls.Load() != concurrent+1 {
		t.Fatalf("upstream calls = %d, want %d", upstreamCalls.Load(), concurrent+1)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := workspace.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	provider, found, err = reopened.GetComputeProvider("infer:fixture-infer")
	if err != nil || !found || provider.DetailsRev != concurrent+1 {
		t.Fatalf("provider after restart = %#v found=%v err=%v", provider, found, err)
	}
}

func TestInferenceProviderProbeFailureIsClassifiedAndNeverInternalCAS(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client := &http.Client{Timeout: 200 * time.Millisecond}
	app := New(Options{Workspace: store, HTTPClient: client}).Handler()
	computeCompatJSON(t, app, http.MethodPost, "/api/compute/inference-providers", map[string]any{
		"name": "offline-infer", "endpoint": "http://127.0.0.1:1", "skillName": "using-model-endpoint",
	}, http.StatusOK)
	response := computeCompatRequest(t, app, http.MethodPost, "/api/compute/providers/infer:offline-infer/probe", nil)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("offline probe status=%d body=%s", response.Code, response.Body.String())
	}
	if bytes.Contains(response.Body.Bytes(), []byte("CAS conflict")) || bytes.Contains(response.Body.Bytes(), []byte("internal")) {
		t.Fatalf("offline probe leaked internal failure: %s", response.Body.String())
	}
	provider, found, err := store.GetComputeProvider("infer:offline-infer")
	if err != nil || !found || provider.DetailsRev != 1 || provider.DetailsMD == "" {
		t.Fatalf("offline provider = %#v found=%v err=%v", provider, found, err)
	}
}

func TestInferenceProviderListAndDeleteMatchV11PublicShape(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := New(Options{Workspace: store}).Handler()
	computeCompatJSON(t, app, http.MethodPost, "/api/compute/inference-providers", map[string]any{
		"name": "public-shape", "endpoint": 9444, "skillName": "using-model-endpoint",
	}, http.StatusOK)

	response := computeCompatRequest(t, app, http.MethodGet, "/api/compute/providers", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", response.Code, response.Body.String())
	}
	var providers []map[string]any
	if err := json.NewDecoder(response.Body).Decode(&providers); err != nil {
		t.Fatal(err)
	}
	if len(providers) != 1 {
		t.Fatalf("providers = %#v", providers)
	}
	provider := providers[0]
	if provider["name"] != "infer:public-shape" || provider["displayName"] != "public-shape" || provider["location"] != "local" || provider["checked"] != true {
		t.Fatalf("public provider = %#v", provider)
	}
	for _, private := range []string{"userId", "detailsRev", "updatedAt"} {
		if _, leaked := provider[private]; leaked {
			t.Fatalf("public provider leaked %s: %#v", private, provider)
		}
	}

	response = computeCompatRequest(t, app, http.MethodDelete, "/api/compute/inference-providers/public-shape", nil)
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("delete status=%d body=%q", response.Code, response.Body.String())
	}
	response = computeCompatRequest(t, app, http.MethodGet, "/api/compute/providers", nil)
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != "[]" {
		t.Fatalf("list after delete status=%d body=%s", response.Code, response.Body.String())
	}
}

func computeCompatJSON(t *testing.T, handler http.Handler, method, path string, body any, wantStatus int) map[string]any {
	t.Helper()
	response := computeCompatRequest(t, handler, method, path, body)
	if response.Code != wantStatus {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, path, response.Code, wantStatus, response.Body.String())
	}
	var value map[string]any
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func computeCompatRequest(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return computeCompatRequestAs(t, handler, method, path, body, "local")
}

func computeCompatRequestAs(t *testing.T, handler http.Handler, method, path string, body any, userID string) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(raw))
	request.Header.Set("X-Synon-User-Id", userID)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

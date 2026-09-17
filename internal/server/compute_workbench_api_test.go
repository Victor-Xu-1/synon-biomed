package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	compute "synon-go/internal/compute"
	workspace "synon-go/internal/persistence/workspace"
)

func TestComputeWorkbenchHTTPContractsAndOwnerIsolation(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-a", UserID: "owner-a", Name: "Compute fixture"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "root-a", ProjectID: "project-a", AgentName: "OPERON", Status: "processing", ConversationType: "chat"}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	frame := "frame-a"
	root := "root-a"
	tool := "tool-compute-1"
	external := "sandbox-1"
	externalURL := "https://compute.example/jobs/sandbox-1"
	var stoppedRemotely atomic.Bool
	stopServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stoppedRemotely.Store(true)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer stopServer.Close()
	registration := compute.ManagedEndpointRegistration{Name: "fixture-nim", URL: "http://127.0.0.1:9444/v1", Port: 9444, SkillName: "using-model-endpoint", StartScript: "start", StopScript: "/usr/bin/curl -fsS -X POST '" + stopServer.URL + "'", LivePath: "/health"}
	if err := store.UpsertManagedEndpoint(workspace.ManagedEndpoint{Name: registration.Name, RegisteredBy: "owner-a", URL: registration.URL, Port: registration.Port, State: "live", Location: "local", SkillName: registration.SkillName, LivePath: registration.LivePath, StartScript: registration.StartScript, StopScript: registration.StopScript, ApprovedScriptHash: compute.ApprovedManagedEndpointHash(registration), StateChangedAt: &now}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateInferenceProvider(workspace.ComputeProviderInput{Name: "fixture-nim", UserID: "owner-a", Family: "infer", Endpoint: registration.URL, SkillName: registration.SkillName}); err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateComputeJob("owner-a", workspace.ComputeJob{JobID: "compute-fixture-job-0001", Environment: "python", TierType: "gpu", Provider: "fixture-nim", FrameID: &frame, ProjectID: "project-a", StartedAt: now, Intent: map[string]any{"kind": "inference"}, HardwareDetails: map[string]any{"gpu": "fixture"}, OriginToolUseID: &tool, RootFrameID: &root, ProviderFamily: "infer", ProviderLabel: "fixture-nim", SupportsTail: true, LeftOnRemote: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BindComputeJobExternal("owner-a", job.JobID, external, externalURL); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendComputeJobLog("owner-a", job.JobID, "stdout", strings.Repeat("a", 70000)+"complete\n"); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendComputeJobLog("owner-a", job.JobID, "stderr", "warning\n"); err != nil {
		t.Fatal(err)
	}
	server := New(Options{Workspace: store, HostGPUDetector: func(context.Context) compute.GPUInfo {
		return compute.UnavailableGPUInfo()
	}})
	mux := http.NewServeMux()
	server.registerComputeWorkbenchRoutes(mux)

	requestJSON(t, mux, http.MethodPut, "/api/compute/gpu/enabled", map[string]any{"enabled": true}, "owner-a", http.StatusOK)
	requestJSON(t, mux, http.MethodPut, "/api/compute/gpu", map[string]any{"enabled": true}, "owner-a", http.StatusOK)
	gpu := requestJSON(t, mux, http.MethodGet, "/api/compute/gpu", nil, "owner-a", http.StatusOK)
	if gpu.(map[string]any)["available"] != false || gpu.(map[string]any)["gpu_name"] != nil || gpu.(map[string]any)["gpu_count"] != float64(0) {
		t.Fatalf("gpu=%#v", gpu)
	}
	if enabledGPU := requestJSON(t, mux, http.MethodGet, "/api/compute/gpu/enabled", nil, "owner-a", http.StatusOK).(map[string]any); enabledGPU["enabled"] != true {
		t.Fatalf("enabled gpu=%#v", enabledGPU)
	}
	detectedGPU := requestJSON(t, mux, http.MethodGet, "/api/compute/gpu/detect", nil, "owner-a", http.StatusOK).(map[string]any)
	if detectedGPU["available"] != false || detectedGPU["gpu_name"] != nil || detectedGPU["gpu_count"] != float64(0) {
		t.Fatalf("detected gpu=%#v", detectedGPU)
	}
	sshAliasesMethod := requestCompute(t, mux, http.MethodPost, "/api/compute/ssh-config-aliases", nil, "owner-a")
	if sshAliasesMethod.Code != http.StatusMethodNotAllowed {
		t.Fatalf("ssh aliases method status=%d body=%s", sshAliasesMethod.Code, sshAliasesMethod.Body.String())
	}
	hostInfo := requestJSON(t, mux, http.MethodGet, "/api/compute/local/hostinfo", nil, "owner-a", http.StatusOK).(map[string]any)
	if strings.TrimSpace(hostInfo["hostLabel"].(string)) == "" {
		t.Fatalf("host info=%#v", hostInfo)
	}
	localRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(localRoot, "fixture.txt"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	localFiles := requestJSON(t, mux, http.MethodGet, "/api/compute/local/files?path="+url.QueryEscape(localRoot), nil, "owner-a", http.StatusOK).(map[string]any)
	if localFiles["resolvedPath"] != localRoot || len(localFiles["entries"].([]any)) != 1 {
		t.Fatalf("local files=%#v", localFiles)
	}
	downloadRequest := httptest.NewRequest(http.MethodGet, "/api/compute/local/download?path="+url.QueryEscape(filepath.Join(localRoot, "fixture.txt")), nil)
	downloadRequest.Header.Set("Range", "bytes=1-3")
	downloadResponse := httptest.NewRecorder()
	mux.ServeHTTP(downloadResponse, downloadRequest)
	if downloadResponse.Code != http.StatusPartialContent || downloadResponse.Body.String() != "ixt" ||
		downloadResponse.Header().Get("Content-Range") != "bytes 1-3/7" ||
		!strings.Contains(downloadResponse.Header().Get("Content-Disposition"), "fixture.txt") {
		t.Fatalf("local download status=%d headers=%v body=%q", downloadResponse.Code, downloadResponse.Header(), downloadResponse.Body.String())
	}
	imported := requestJSON(t, mux, http.MethodPost, "/api/compute/local/import", map[string]any{
		"path": filepath.Join(localRoot, "fixture.txt"), "projectId": "project-a",
	}, "owner-a", http.StatusOK).(map[string]any)
	if imported["filename"] != "fixture.txt" || imported["size_bytes"] != float64(7) ||
		imported["root_frame_id"] != "root-a" || imported["is_user_upload"] != true ||
		imported["frame_id"] != nil || imported["creating_frame_id"] != nil ||
		!filepath.IsAbs(imported["file_path"].(string)) {
		t.Fatalf("local import=%#v", imported)
	}
	record, found, err := store.GetCurrentArtifactLineageRecord(imported["id"].(string), false)
	if err != nil || !found || !record.IsUserUpload || record.RootFrameID != "root-a" || record.VersionID != imported["version_id"] {
		t.Fatalf("imported lineage=%#v found=%v err=%v", record, found, err)
	}
	foreignImport := requestCompute(t, mux, http.MethodPost, "/api/compute/local/import", map[string]any{
		"path": filepath.Join(localRoot, "fixture.txt"), "projectId": "project-a",
	}, "owner-b")
	if foreignImport.Code != http.StatusNotFound {
		t.Fatalf("foreign import status=%d body=%s", foreignImport.Code, foreignImport.Body.String())
	}
	endpoints := requestJSON(t, mux, http.MethodGet, "/api/compute/managed-endpoints?sizes=1", nil, "owner-a", http.StatusOK).([]any)
	if len(endpoints) != 1 || endpoints[0].(map[string]any)["name"] != "fixture-nim" {
		t.Fatalf("endpoints=%#v", endpoints)
	}
	foreign := requestJSON(t, mux, http.MethodGet, "/api/compute/managed-endpoints", nil, "owner-b", http.StatusOK).([]any)
	if len(foreign) != 0 {
		t.Fatalf("foreign endpoints=%#v", foreign)
	}
	jobs := requestJSON(t, mux, http.MethodGet, "/api/compute/jobs?projectId=project-a", nil, "owner-a", http.StatusOK).(map[string]any)["jobs"].([]any)
	if len(jobs) != 1 || jobs[0].(map[string]any)["externalId"] != "sandbox-1" {
		t.Fatalf("jobs=%#v", jobs)
	}
	detail := requestJSON(t, mux, http.MethodGet, "/api/compute/jobs/compute-fixture-job-0001", nil, "owner-a", http.StatusOK).(map[string]any)
	if detail["rootFrameId"] != "root-a" || detail["supportsTail"] != true {
		t.Fatalf("detail=%#v", detail)
	}
	if detail["endedAtIso"] != nil || detail["errorKind"] != nil || len(detail["leftOnRemote"].([]any)) != 0 ||
		detail["harvest"].(map[string]any)["stdout"].(map[string]any)["exists"] != true {
		t.Fatalf("detail fixed fields=%#v", detail)
	}
	log := requestJSON(t, mux, http.MethodGet, "/api/compute/jobs/compute-fixture-job-0001/logs?tail=16", nil, "owner-a", http.StatusOK).(map[string]any)
	if log["truncated"] != true || log["text"] != "aaaaaaacomplete\n" {
		t.Fatalf("log=%#v", log)
	}
	response := requestCompute(t, mux, http.MethodGet, "/api/compute/jobs/compute-fixture-job-0001/logs", nil, "owner-b")
	if response.Code != http.StatusNotFound {
		t.Fatalf("foreign log status=%d body=%s", response.Code, response.Body.String())
	}
	foreignEnable := requestCompute(t, mux, http.MethodPut, "/api/compute/session/root-a/enabled/infer:fixture-nim", map[string]any{"checked": true}, "owner-b")
	if foreignEnable.Code != http.StatusNotFound {
		t.Fatalf("foreign enable status=%d body=%s", foreignEnable.Code, foreignEnable.Body.String())
	}
	if _, err := store.SetBYOCEnabled("modal", "owner-a", false); err != nil {
		t.Fatal(err)
	}
	draftDefault := requestJSON(t, mux, http.MethodGet, "/api/compute/session/draft-tab-a/enabled", nil, "owner-a", http.StatusOK).([]any)
	if len(draftDefault) != 1 || draftDefault[0] != "fixture-nim" {
		t.Fatalf("draft default=%#v", draftDefault)
	}
	if _, err := store.SetBYOCEnabled("modal", "owner-a", true); err != nil {
		t.Fatal(err)
	}
	draftWithBYOC := requestJSON(t, mux, http.MethodGet, "/api/compute/session/draft-tab-byoc/enabled", nil, "owner-a", http.StatusOK).([]any)
	if len(draftWithBYOC) != 2 || draftWithBYOC[0] != "byoc:modal" || draftWithBYOC[1] != "fixture-nim" {
		t.Fatalf("draft with BYOC=%#v", draftWithBYOC)
	}
	if _, err := store.SetBYOCEnabled("modal", "owner-a", false); err != nil {
		t.Fatal(err)
	}
	requestJSON(t, mux, http.MethodPut, "/api/compute/session/draft-tab-a/enabled/fixture-nim", map[string]any{"checked": false}, "owner-a", http.StatusNoContent)
	draftDisabled := requestJSON(t, mux, http.MethodGet, "/api/compute/session/draft-tab-a/enabled", nil, "owner-a", http.StatusOK).([]any)
	if len(draftDisabled) != 0 {
		t.Fatalf("draft disabled=%#v", draftDisabled)
	}
	requestJSON(t, mux, http.MethodPut, "/api/compute/session/draft-tab-a/enabled/ssh:migrated", map[string]any{"checked": true}, "owner-a", http.StatusNoContent)
	requestJSON(t, mux, http.MethodPut, "/api/compute/session/root-a/enabled/infer:fixture-nim", map[string]any{"checked": true}, "owner-a", http.StatusNoContent)
	enabled := requestJSON(t, mux, http.MethodGet, "/api/compute/session/root-a/enabled", nil, "owner-a", http.StatusOK).([]any)
	if len(enabled) != 1 || enabled[0] != "infer:fixture-nim" {
		t.Fatalf("enabled=%#v", enabled)
	}
	requestJSON(t, mux, http.MethodPost, "/api/compute/session/migrate", map[string]any{"fromKey": "draft-tab-a", "toKey": "root-a"}, "owner-a", http.StatusNoContent)
	migrated := requestJSON(t, mux, http.MethodGet, "/api/compute/session/root-a/enabled", nil, "owner-a", http.StatusOK).([]any)
	if len(migrated) != 1 || migrated[0] != "ssh:migrated" {
		t.Fatalf("migrated=%#v", migrated)
	}
	draft, err := store.ListSessionComputeProviders("owner-a", "draft-tab-a")
	if err != nil || len(draft) != 0 {
		t.Fatalf("draft after migration=%#v err=%v", draft, err)
	}
	if _, configured, err := store.SessionComputeProviderSelection("owner-a", "root-a"); err != nil || !configured {
		t.Fatalf("migrated selection configured=%v err=%v", configured, err)
	}
	if err := store.SetSessionComputeProvider("owner-b", "draft-foreign", "ssh:foreign", true); err != nil {
		t.Fatal(err)
	}
	foreignMigration := requestCompute(t, mux, http.MethodPost, "/api/compute/session/migrate", map[string]any{"fromKey": "draft-foreign", "toKey": "root-a"}, "owner-b")
	if foreignMigration.Code != http.StatusNotFound {
		t.Fatalf("foreign migration status=%d body=%s", foreignMigration.Code, foreignMigration.Body.String())
	}
	invalidDraft := requestCompute(t, mux, http.MethodGet, "/api/compute/session/"+"draft-"+strings.Repeat("x", 250)+"/enabled", nil, "owner-a")
	if invalidDraft.Code != http.StatusBadRequest {
		t.Fatalf("invalid draft status=%d body=%s", invalidDraft.Code, invalidDraft.Body.String())
	}
	missingStop := requestJSON(t, mux, http.MethodPost, "/api/compute/managed-endpoints/oracle-missing/stop", nil, "owner-a", http.StatusNotFound).(map[string]any)
	if missingStop["detail"] != "unknown managed endpoint 'oracle-missing'" {
		t.Fatalf("missing stop=%#v", missingStop)
	}
	stopped := requestJSON(t, mux, http.MethodPost, "/api/compute/managed-endpoints/fixture-nim/stop", nil, "owner-a", http.StatusOK).(map[string]any)
	if stopped["state"] != "stopped" {
		t.Fatalf("stopped=%#v", stopped)
	}
	if !stoppedRemotely.Load() {
		t.Fatal("approved stop script did not reach the loopback endpoint")
	}
	drifted := compute.ManagedEndpointRegistration{Name: "fixture-drift", URL: "http://127.0.0.1:9555/v1", Port: 9555, SkillName: "using-model-endpoint", StartScript: "start", StopScript: "exit 0", LivePath: "/health"}
	if err := store.UpsertManagedEndpoint(workspace.ManagedEndpoint{Name: drifted.Name, RegisteredBy: "owner-a", URL: drifted.URL, Port: drifted.Port, State: "live", Location: "local", SkillName: drifted.SkillName, LivePath: drifted.LivePath, StartScript: drifted.StartScript, StopScript: drifted.StopScript, ApprovedScriptHash: "invalid-approval", StateChangedAt: &now}); err != nil {
		t.Fatal(err)
	}
	failedStop := requestCompute(t, mux, http.MethodPost, "/api/compute/managed-endpoints/fixture-drift/stop", nil, "owner-a")
	if failedStop.Code != http.StatusBadGateway {
		t.Fatalf("failed stop status=%d body=%s", failedStop.Code, failedStop.Body.String())
	}
	failedEndpoint := requestJSON(t, mux, http.MethodGet, "/api/compute/managed-endpoints?name=fixture-drift", nil, "owner-a", http.StatusOK).([]any)
	if len(failedEndpoint) != 1 || failedEndpoint[0].(map[string]any)["state"] != "failed" ||
		!strings.Contains(failedEndpoint[0].(map[string]any)["lastError"].(string), "approved hash") {
		t.Fatalf("failed endpoint=%#v", failedEndpoint)
	}
}

func TestComputeLocalImportIsIdempotentAndOutboxBacked(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-import", UserID: "owner-a", Name: "Import fixture"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "root-import", ProjectID: "project-import", AgentName: "OPERON", Status: "processing", ConversationType: "chat"}); err != nil {
		t.Fatal(err)
	}

	localRoot := t.TempDir()
	path := filepath.Join(localRoot, "result.csv")
	if err := os.WriteFile(path, []byte("compound,score\nA,1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := New(Options{Workspace: store})
	mux := http.NewServeMux()
	server.registerComputeWorkbenchRoutes(mux)
	body := map[string]any{"path": path, "projectId": "project-import"}

	first := requestComputeWithHeaders(t, mux, http.MethodPost, "/api/compute/local/import", body, "owner-a", map[string]string{
		"Idempotency-Key": "compute-import-fixture-1",
	})
	if first.Code != http.StatusOK {
		t.Fatalf("first import status=%d body=%s", first.Code, first.Body.String())
	}
	var firstValue map[string]any
	if err := json.NewDecoder(first.Body).Decode(&firstValue); err != nil {
		t.Fatal(err)
	}
	countAfterFirst, err := store.CountOutboxEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if countAfterFirst != 2 {
		t.Fatalf("outbox count after first import=%d want=2", countAfterFirst)
	}
	frameEvents, err := store.ListFrameEvents("root-import", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(frameEvents) != 1 || frameEvents[0].Type != "artifact_created" {
		t.Fatalf("frame journal events=%#v", frameEvents)
	}

	replayed := requestComputeWithHeaders(t, mux, http.MethodPost, "/api/compute/local/import", body, "owner-a", map[string]string{
		"Idempotency-Key": "compute-import-fixture-1",
	})
	if replayed.Code != http.StatusOK {
		t.Fatalf("replayed import status=%d body=%s", replayed.Code, replayed.Body.String())
	}
	var replayedValue map[string]any
	if err := json.NewDecoder(replayed.Body).Decode(&replayedValue); err != nil {
		t.Fatal(err)
	}
	if replayedValue["id"] != firstValue["id"] || replayedValue["version_id"] != firstValue["version_id"] || replayedValue["version_number"] != float64(1) {
		t.Fatalf("replayed import=%#v first=%#v", replayedValue, firstValue)
	}
	countAfterReplay, err := store.CountOutboxEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if countAfterReplay != countAfterFirst {
		t.Fatalf("replay duplicated outbox events: first=%d replay=%d", countAfterFirst, countAfterReplay)
	}

	if err := os.WriteFile(path, []byte("compound,score\nB,2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	conflict := requestComputeWithHeaders(t, mux, http.MethodPost, "/api/compute/local/import", body, "owner-a", map[string]string{
		"Idempotency-Key": "compute-import-fixture-1",
	})
	if conflict.Code != http.StatusConflict {
		t.Fatalf("changed replay status=%d want=%d body=%s", conflict.Code, http.StatusConflict, conflict.Body.String())
	}
	invalid := requestComputeWithHeaders(t, mux, http.MethodPost, "/api/compute/local/import", body, "owner-a", map[string]string{
		"Idempotency-Key": strings.Repeat("x", 257),
	})
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid idempotency key status=%d want=%d body=%s", invalid.Code, http.StatusBadRequest, invalid.Body.String())
	}

	wantTypes := []string{"artifact_created", "lineage_ready"}
	gotTypes := make([]string, 0, len(wantTypes))
	for range wantTypes {
		events, err := store.ClaimOutbox(context.Background(), workspace.ClaimOutboxInput{
			WorkerID: "compute-import-test", Limit: 1, Lease: time.Minute,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 1 {
			t.Fatalf("claimed events=%d want=1", len(events))
		}
		gotTypes = append(gotTypes, events[0].Type)
		if err := store.AckOutbox(context.Background(), events[0].ID, events[0].ClaimToken); err != nil {
			t.Fatal(err)
		}
	}
	slices.Sort(gotTypes)
	slices.Sort(wantTypes)
	if !slices.Equal(gotTypes, wantTypes) {
		t.Fatalf("outbox event types=%v want=%v", gotTypes, wantTypes)
	}
}

func requestCompute(t *testing.T, handler http.Handler, method, path string, body any, user string) *httptest.ResponseRecorder {
	return requestComputeWithHeaders(t, handler, method, path, body, user, nil)
}

func requestComputeWithHeaders(t *testing.T, handler http.Handler, method, path string, body any, user string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == nil {
		reader = strings.NewReader("")
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = strings.NewReader(string(raw))
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("X-Synon-User-Id", user)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
func requestJSON(t *testing.T, handler http.Handler, method, path string, body any, user string, want int) any {
	t.Helper()
	response := requestCompute(t, handler, method, path, body, user)
	if response.Code != want {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, path, response.Code, want, response.Body.String())
	}
	if want == http.StatusNoContent {
		return nil
	}
	var value any
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

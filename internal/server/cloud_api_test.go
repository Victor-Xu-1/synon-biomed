package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"synon-go/internal/cloudstore"
	"synon-go/internal/compute"
	workspace "synon-go/internal/persistence/workspace"
)

func TestCloudCredentialAPIAndStreamingArtifactTransfer(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-2", UserID: "user-2", Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	cloud := &recordingCloudClient{objects: map[string][]byte{"bucket/source/input.txt": []byte("cloud payload")}}
	server := New(Options{FileRoot: t.TempDir(), Workspace: store, CloudFactory: fixedCloudFactory{client: cloud}})
	startServerRealtimeOutbox(t, store, server)
	handler := server.Handler()
	secretRequestJSON(t, handler, http.MethodPost, "/api/go/secrets", "user-1", map[string]any{
		"id": "cloud-1", "provider": "aws", "name": "Primary",
		"credential_type": "access_key", "region": "us-east-1", "buckets": []string{"bucket"},
		"credentials": map[string]string{"access_key_id": "AKID", "secret_access_key": "SECRET"},
	}, http.StatusOK)

	listed := cloudRequestAny(t, handler, http.MethodGet, "/api/cloud-credentials", "user-1", nil, http.StatusOK).([]any)
	if len(listed) != 1 {
		t.Fatalf("cloud credentials = %#v", listed)
	}
	projection := listed[0].(map[string]any)
	if projection["provider"] != "s3" || projection["default_bucket"] != "bucket" || projection["is_connected"] != true {
		t.Fatalf("cloud projection = %#v", projection)
	}
	encoded, _ := json.Marshal(projection)
	if bytes.Contains(encoded, []byte("AKID")) || bytes.Contains(encoded, []byte("SECRET")) {
		t.Fatalf("cloud projection leaked credentials: %s", encoded)
	}
	foreign := cloudRequestAny(t, handler, http.MethodGet, "/api/cloud-credentials", "user-2", nil, http.StatusOK).([]any)
	if len(foreign) != 0 {
		t.Fatalf("foreign cloud credentials = %#v", foreign)
	}

	testResult := cloudRequestAny(t, handler, http.MethodPost, "/api/cloud-credentials/cloud-1/test", "user-1", nil, http.StatusOK).(map[string]any)
	if testResult["success"] != true {
		t.Fatalf("test result = %#v", testResult)
	}
	buckets := cloudRequestAny(t, handler, http.MethodGet, "/api/cloud-credentials/cloud-1/buckets", "user-1", nil, http.StatusOK).([]any)
	if len(buckets) != 1 || buckets[0] != "bucket" {
		t.Fatalf("buckets = %#v", buckets)
	}
	objects := cloudRequestAny(t, handler, http.MethodGet, "/api/cloud-credentials/cloud-1/objects?bucket=bucket&prefix=source/", "user-1", nil, http.StatusOK).([]any)
	if len(objects) != 1 || objects[0].(map[string]any)["key"] != "source/input.txt" {
		t.Fatalf("objects = %#v", objects)
	}
	folder := cloudRequestAny(t, handler, http.MethodGet, "/api/cloud-credentials/cloud-1/folder?bucket=bucket&prefix=source/&page_limit=4", "user-1", nil, http.StatusOK).(map[string]any)
	if len(folder["folders"].([]any)) != 1 || len(folder["files"].([]any)) != 2 {
		t.Fatalf("folder = %#v", folder)
	}

	downloadRequest := httptest.NewRequest(http.MethodGet, "/api/cloud-credentials/cloud-1/download?bucket=bucket&key=source/input.txt&disposition=inline", nil)
	downloadRequest.Header.Set("X-Synon-User-Id", "user-1")
	downloadResponse := httptest.NewRecorder()
	handler.ServeHTTP(downloadResponse, downloadRequest)
	if downloadResponse.Code != http.StatusOK || downloadResponse.Body.String() != "cloud payload" || downloadResponse.Header().Get("Content-Type") != "text/plain" {
		t.Fatalf("download status=%d headers=%v body=%q", downloadResponse.Code, downloadResponse.Header(), downloadResponse.Body.String())
	}

	imported := cloudRequestAny(t, handler, http.MethodPost, "/api/cloud-credentials/cloud-1/import", "user-1", map[string]any{
		"bucket": "bucket", "key": "source/input.txt", "project_id": "project-1",
	}, http.StatusOK).(map[string]any)
	artifactID := imported["id"].(string)
	artifact, version, found, err := store.GetCurrentArtifactVersionMetadata(artifactID)
	if err != nil || !found || artifact.ProjectID != "project-1" || version.StoragePath == "" || version.SizeBytes != int64(len("cloud payload")) {
		t.Fatalf("imported artifact=%+v version=%+v found=%v err=%v", artifact, version, found, err)
	}
	waitServerRealtimeOutbox(t, store)
	realtime := runtimeCompatJSON(t, handler, http.MethodGet, "/api/events?project_id=project-1", "user-1", nil, http.StatusOK)
	events := realtime["events"].([]any)
	if len(events) != 2 || events[0].(map[string]any)["type"] != "artifact_created" || events[1].(map[string]any)["type"] != "lineage_ready" {
		t.Fatalf("cloud import realtime events=%#v", events)
	}
	rangeRequest := httptest.NewRequest(http.MethodGet, "/api/artifacts/"+artifactID, nil)
	rangeRequest.Header.Set("X-Synon-User-Id", "user-1")
	rangeRequest.Header.Set("Range", "bytes=6-12")
	rangeResponse := httptest.NewRecorder()
	handler.ServeHTTP(rangeResponse, rangeRequest)
	if rangeResponse.Code != http.StatusPartialContent || rangeResponse.Body.String() != "payload" {
		t.Fatalf("streamed artifact range=%d body=%q", rangeResponse.Code, rangeResponse.Body.String())
	}
	for _, endpoint := range []string{"/api/artifacts/" + artifactID, "/api/artifacts/versions/" + version.ID} {
		foreignDownload := httptest.NewRequest(http.MethodGet, endpoint, nil)
		foreignDownload.Header.Set("X-Synon-User-Id", "user-2")
		foreignResponse := httptest.NewRecorder()
		handler.ServeHTTP(foreignResponse, foreignDownload)
		if foreignResponse.Code != http.StatusNotFound {
			t.Fatalf("foreign download %s status=%d body=%s", endpoint, foreignResponse.Code, foreignResponse.Body.String())
		}
	}

	cloudRequestAny(t, handler, http.MethodPost, "/api/cloud-credentials/cloud-1/export", "user-1", map[string]any{
		"artifact_id": artifactID, "bucket": "bucket", "key": "exports/output.txt",
	}, http.StatusOK)
	cloud.mu.Lock()
	exported := string(cloud.objects["bucket/exports/output.txt"])
	puts := cloud.puts
	cloud.mu.Unlock()
	if exported != "cloud payload" || puts != 1 {
		t.Fatalf("exported=%q puts=%d", exported, puts)
	}
	cloudRequestAny(t, handler, http.MethodPost, "/api/cloud-credentials/cloud-1/export", "user-2", map[string]any{
		"artifact_id": artifactID, "bucket": "bucket", "key": "exports/stolen.txt",
	}, http.StatusNotFound)
	cloud.mu.Lock()
	if cloud.puts != 1 {
		t.Fatal("cross-tenant export reached cloud provider")
	}
	cloud.mu.Unlock()

	cloudRequestAny(t, handler, http.MethodDelete, "/api/cloud-credentials/cloud-1", "user-1", nil, http.StatusOK)
	cloudRequestAny(t, handler, http.MethodGet, "/api/cloud-credentials/cloud-1", "user-1", nil, http.StatusNotFound)
}

func TestCloudImportRejectsForeignProjectBeforeProviderRead(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "foreign", UserID: "user-2", Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	cloud := &recordingCloudClient{objects: map[string][]byte{"bucket/key": []byte("secret")}}
	server := New(Options{FileRoot: t.TempDir(), Workspace: store, CloudFactory: fixedCloudFactory{client: cloud}})
	handler := server.Handler()
	secretRequestJSON(t, handler, http.MethodPost, "/api/go/secrets", "user-1", map[string]any{
		"id": "cloud-1", "provider": "aws", "credentials": map[string]string{"access_key_id": "x", "secret_access_key": "y"},
	}, http.StatusOK)
	cloudRequestAny(t, handler, http.MethodPost, "/api/cloud-credentials/cloud-1/import", "user-1", map[string]any{
		"bucket": "bucket", "key": "key", "project_id": "foreign",
	}, http.StatusNotFound)
	cloud.mu.Lock()
	defer cloud.mu.Unlock()
	if cloud.opens != 0 {
		t.Fatal("foreign project import reached cloud provider")
	}
}

func TestCloudImportRejectsDeclaredOversizeBeforeOpeningObject(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	objectKey := "bucket/oversized.bin"
	cloud := &recordingCloudClient{
		objects: map[string][]byte{objectKey: []byte("small fixture body")},
		sizes:   map[string]int64{objectKey: compute.RemoteImportMaxBytes + 1},
	}
	server := New(Options{FileRoot: t.TempDir(), Workspace: store, CloudFactory: fixedCloudFactory{client: cloud}})
	handler := server.Handler()
	secretRequestJSON(t, handler, http.MethodPost, "/api/go/secrets", "user-1", map[string]any{
		"id": "cloud-1", "provider": "aws", "credentials": map[string]string{"access_key_id": "x", "secret_access_key": "y"},
	}, http.StatusOK)

	response := cloudRequestAny(t, handler, http.MethodPost, "/api/cloud-credentials/cloud-1/import", "user-1", map[string]any{
		"bucket": "bucket", "key": "oversized.bin", "project_id": "project-1",
	}, http.StatusRequestEntityTooLarge).(map[string]any)
	if response["remoteKind"] != "too_large" || response["detail"] != "cloud object exceeds the 52428800 byte import limit" {
		t.Fatalf("response=%#v", response)
	}
	cloud.mu.Lock()
	heads, opens := cloud.heads, cloud.opens
	cloud.mu.Unlock()
	if heads != 1 || opens != 0 {
		t.Fatalf("provider reads heads=%d opens=%d", heads, opens)
	}
	artifacts, err := store.ListArtifacts("project-1", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 0 {
		t.Fatalf("oversized import created artifacts=%#v", artifacts)
	}
}

func TestCloudImportRejectsObjectThatGrowsWhileStreaming(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	objectKey := "bucket/growing.bin"
	cloud := &recordingCloudClient{
		objects: map[string][]byte{objectKey: []byte("metadata fixture")},
		sizes:   map[string]int64{objectKey: 16},
		readers: map[string]func() io.ReadCloser{
			objectKey: func() io.ReadCloser {
				return io.NopCloser(&zeroReader{remaining: compute.RemoteImportMaxBytes + 1})
			},
		},
	}
	server := New(Options{FileRoot: t.TempDir(), Workspace: store, CloudFactory: fixedCloudFactory{client: cloud}})
	handler := server.Handler()
	secretRequestJSON(t, handler, http.MethodPost, "/api/go/secrets", "user-1", map[string]any{
		"id": "cloud-1", "provider": "aws", "credentials": map[string]string{"access_key_id": "x", "secret_access_key": "y"},
	}, http.StatusOK)

	response := cloudRequestAny(t, handler, http.MethodPost, "/api/cloud-credentials/cloud-1/import", "user-1", map[string]any{
		"bucket": "bucket", "key": "growing.bin", "project_id": "project-1",
	}, http.StatusRequestEntityTooLarge).(map[string]any)
	if response["remoteKind"] != "too_large" {
		t.Fatalf("response=%#v", response)
	}
	artifacts, err := store.ListArtifacts("project-1", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 0 {
		t.Fatalf("growing import created artifacts=%#v", artifacts)
	}
}

type fixedCloudFactory struct {
	client cloudstore.Client
}

func (f fixedCloudFactory) New(string, cloudstore.Credentials) (cloudstore.Client, error) {
	return f.client, nil
}

type recordingCloudClient struct {
	mu      sync.Mutex
	objects map[string][]byte
	sizes   map[string]int64
	readers map[string]func() io.ReadCloser
	heads   int
	opens   int
	puts    int
}

func (c *recordingCloudClient) ListBuckets(context.Context) ([]string, error) {
	return []string{"bucket"}, nil
}

func (c *recordingCloudClient) ListPage(_ context.Context, bucket, prefix, delimiter string, _ int, token string) (cloudstore.Page, error) {
	if delimiter == "" {
		return cloudstore.Page{Objects: []cloudstore.Object{{Key: "source/input.txt", Size: 13}}}, nil
	}
	if token == "" {
		return cloudstore.Page{
			Objects:  []cloudstore.Object{{Key: prefix, Size: 0}, {Key: "source/input.txt", Size: 13}},
			Prefixes: []string{"source/nested/"}, NextToken: "page-2",
		}, nil
	}
	return cloudstore.Page{Objects: []cloudstore.Object{{Key: "source/second.txt", Size: 6}}}, nil
}

func (c *recordingCloudClient) HeadObject(_ context.Context, bucket, key string) (cloudstore.ObjectInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.heads++
	payload, ok := c.objects[bucket+"/"+key]
	if !ok {
		return cloudstore.ObjectInfo{}, &cloudstore.HTTPError{StatusCode: http.StatusNotFound, Message: "not found"}
	}
	size := int64(len(payload))
	if override, exists := c.sizes[bucket+"/"+key]; exists {
		size = override
	}
	return cloudstore.ObjectInfo{Size: size, ContentType: "text/plain"}, nil
}

func (c *recordingCloudClient) OpenObject(_ context.Context, bucket, key string) (io.ReadCloser, cloudstore.ObjectInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	payload, ok := c.objects[bucket+"/"+key]
	if !ok {
		return nil, cloudstore.ObjectInfo{}, &cloudstore.HTTPError{StatusCode: http.StatusNotFound, Message: "not found"}
	}
	c.opens++
	size := int64(len(payload))
	if override, exists := c.sizes[bucket+"/"+key]; exists {
		size = override
	}
	if factory := c.readers[bucket+"/"+key]; factory != nil {
		return factory(), cloudstore.ObjectInfo{Size: size, ContentType: "application/octet-stream"}, nil
	}
	copyPayload := append([]byte(nil), payload...)
	return io.NopCloser(bytes.NewReader(copyPayload)), cloudstore.ObjectInfo{Size: size, ContentType: "text/plain"}, nil
}

func (c *recordingCloudClient) PutObject(_ context.Context, bucket, key string, reader io.Reader, size int64, _ string) (int64, error) {
	payload, err := io.ReadAll(reader)
	if err != nil {
		return 0, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.objects[bucket+"/"+key] = payload
	c.puts++
	return size, nil
}

func cloudRequestAny(t *testing.T, handler http.Handler, method, path, userID string, body any, wantStatus int) any {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("X-Synon-User-Id", userID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("%s %s status=%d body=%s", method, path, response.Code, response.Body.String())
	}
	if response.Body.Len() == 0 {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if object, ok := decoded.(map[string]any); ok {
		if detail, exists := object["detail"]; exists && !strings.Contains(strings.ToLower(detail.(string)), "not found") {
			t.Logf("cloud API detail: %v", detail)
		}
	}
	return decoded
}

type zeroReader struct {
	remaining int64
}

func (reader *zeroReader) Read(buffer []byte) (int, error) {
	if reader.remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(buffer)) > reader.remaining {
		buffer = buffer[:reader.remaining]
	}
	clear(buffer)
	reader.remaining -= int64(len(buffer))
	return len(buffer), nil
}

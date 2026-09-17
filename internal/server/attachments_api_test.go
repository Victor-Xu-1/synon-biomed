package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestAttachmentStoreErrorReportsInsufficientStorage(t *testing.T) {
	response := httptest.NewRecorder()
	writeAttachmentStoreError(response, &workspace.InsufficientAttachmentSpaceError{
		RequiredBytes:  1024,
		AvailableBytes: 256,
	})
	if response.Code != http.StatusInsufficientStorage {
		t.Fatalf("insufficient storage status = %d: %s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != "insufficient_upload_storage" || body["required_bytes"] != float64(1024) || body["available_bytes"] != float64(256) {
		t.Fatalf("insufficient storage response = %#v", body)
	}
}

func TestBaselineAttachmentUploadMetadataAndRangeDownload(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store}).Handler()
	notMultipartRequest := httptest.NewRequest(http.MethodPost, "/api/projects/project-1/attachments", nil)
	notMultipartRequest.Header.Set("X-Synon-User-Id", "user-1")
	notMultipart := httptest.NewRecorder()
	app.ServeHTTP(notMultipart, notMultipartRequest)
	if notMultipart.Code != http.StatusNotAcceptable || !strings.Contains(notMultipart.Body.String(), "the request is not multipart") {
		t.Fatalf("non-multipart upload = %d: %s", notMultipart.Code, notMultipart.Body.String())
	}
	payload := []byte("attachment-range-content")
	body, contentType := multipartAttachmentBody(t, "file", "evidence.txt", payload, nil)
	request := httptest.NewRequest(http.MethodPost, "/api/projects/project-1/attachments?ephemeral=true", body)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("X-Synon-User-Id", "user-1")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("upload = %d: %s", response.Code, response.Body.String())
	}
	var uploaded map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &uploaded); err != nil {
		t.Fatal(err)
	}
	attachmentID, _ := uploaded["id"].(string)
	if attachmentID == "" || uploaded["filename"] != "evidence.txt" || uploaded["ephemeral"] != true {
		t.Fatalf("uploaded = %#v", uploaded)
	}

	metadataRequest := httptest.NewRequest(http.MethodGet, "/api/attachments/"+attachmentID+"/metadata", nil)
	metadataRequest.Header.Set("X-Synon-User-Id", "user-1")
	metadata := httptest.NewRecorder()
	app.ServeHTTP(metadata, metadataRequest)
	expectedDigest := sha256.Sum256(payload)
	if metadata.Code != http.StatusOK || !strings.Contains(metadata.Body.String(), hex.EncodeToString(expectedDigest[:])) ||
		!strings.Contains(metadata.Body.String(), "\"content_type\":\"text/plain") {
		t.Fatalf("metadata = %d: %s", metadata.Code, metadata.Body.String())
	}

	foreignRequest := httptest.NewRequest(http.MethodGet, "/api/attachments/"+attachmentID+"/metadata", nil)
	foreignRequest.Header.Set("X-Synon-User-Id", "user-2")
	foreign := httptest.NewRecorder()
	app.ServeHTTP(foreign, foreignRequest)
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("cross-user metadata = %d: %s", foreign.Code, foreign.Body.String())
	}
	missingRequest := httptest.NewRequest(http.MethodGet, "/api/attachments/missing-attachment/metadata", nil)
	missingRequest.Header.Set("X-Synon-User-Id", "user-1")
	missing := httptest.NewRecorder()
	app.ServeHTTP(missing, missingRequest)
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), "Attachment missing-attachment not found") {
		t.Fatalf("missing metadata = %d: %s", missing.Code, missing.Body.String())
	}

	downloadRequest := httptest.NewRequest(http.MethodGet, "/api/attachments/"+attachmentID, nil)
	downloadRequest.Header.Set("X-Synon-User-Id", "user-1")
	downloadRequest.Header.Set("Range", "bytes=11-15")
	download := httptest.NewRecorder()
	app.ServeHTTP(download, downloadRequest)
	if download.Code != http.StatusPartialContent || download.Body.String() != "range" {
		t.Fatalf("range download = %d %q", download.Code, download.Body.String())
	}
	if !strings.Contains(download.Header().Get("Content-Disposition"), "evidence.txt") ||
		download.Header().Get("ETag") == "" {
		t.Fatalf("download headers = %#v", download.Header())
	}
}

func TestAttachmentUploadRejectsForeignProjectBeforeMultipartParsing(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "private-project", UserID: "owner-a", Name: "Private",
	}); err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store}).Handler()
	request := httptest.NewRequest(http.MethodPost, "/api/projects/private-project/attachments", strings.NewReader("not multipart"))
	request.Header.Set("Content-Type", "text/plain")
	request.Header.Set("X-Synon-User-Id", "owner-b")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "project not found") {
		t.Fatalf("foreign preflight status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestBaselineChunkedAttachmentUploadLifecycle(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store}).Handler()
	chunkSize := 1 << 20
	payload := bytes.Repeat([]byte("abcdefghij"), (2*chunkSize+10)/10)
	initBody := bytes.NewBufferString(fmt.Sprintf(`{"project_id":"project-1","filename":"large.bin","total_size":%d,"content_type":"application/octet-stream","chunk_size":%d}`, len(payload), chunkSize))
	initRequest := httptest.NewRequest(http.MethodPost, "/api/artifacts/upload/init", initBody)
	initRequest.Header.Set("Content-Type", "application/json")
	initRequest.Header.Set("X-Synon-User-Id", "user-1")
	initialized := httptest.NewRecorder()
	app.ServeHTTP(initialized, initRequest)
	if initialized.Code != http.StatusOK {
		t.Fatalf("init = %d: %s", initialized.Code, initialized.Body.String())
	}
	var initResult map[string]any
	if err := json.Unmarshal(initialized.Body.Bytes(), &initResult); err != nil {
		t.Fatal(err)
	}
	uploadID, _ := initResult["upload_id"].(string)
	if len(initResult) != 3 || uploadID == "" || initResult["chunk_size"] != float64(chunkSize) || initResult["total_chunks"] != float64(3) {
		t.Fatalf("init result = %#v", initResult)
	}

	for index, chunk := range [][]byte{payload[:chunkSize], payload[chunkSize : 2*chunkSize], payload[2*chunkSize:]} {
		fields := map[string]string{"upload_id": uploadID, "chunk_index": string(rune('0' + index))}
		body, contentType := multipartAttachmentBody(t, "chunk", "chunk.bin", chunk, fields)
		request := httptest.NewRequest(http.MethodPost, "/api/artifacts/upload/chunk", body)
		request.Header.Set("Content-Type", contentType)
		request.Header.Set("X-Synon-User-Id", "user-1")
		response := httptest.NewRecorder()
		app.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("chunk %d = %d: %s", index, response.Code, response.Body.String())
		}
		var chunkResult map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &chunkResult); err != nil {
			t.Fatal(err)
		}
		if len(chunkResult) != 3 || chunkResult["chunk_index"] != float64(index) || chunkResult["received"] != true || chunkResult["chunks_remaining"] != float64(2-index) {
			t.Fatalf("chunk %d response=%#v", index, chunkResult)
		}
	}
	statusRequest := httptest.NewRequest(http.MethodGet, "/api/artifacts/upload/status/"+uploadID, nil)
	statusRequest.Header.Set("X-Synon-User-Id", "user-1")
	status := httptest.NewRecorder()
	app.ServeHTTP(status, statusRequest)
	if status.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", status.Code, status.Body.String())
	}
	var statusResult map[string]any
	if err := json.Unmarshal(status.Body.Bytes(), &statusResult); err != nil {
		t.Fatal(err)
	}
	if len(statusResult) != 9 || statusResult["chunk_size"] != float64(chunkSize) || statusResult["received_chunks"] != float64(3) || statusResult["percent_complete"] != float64(100) || len(statusResult["missing_chunks"].([]any)) != 0 {
		t.Fatalf("status = %#v", statusResult)
	}

	digest := sha256.Sum256(payload)
	finalize := func(start <-chan struct{}, responses chan<- *httptest.ResponseRecorder) {
		<-start
		request := httptest.NewRequest(http.MethodPost,
			"/api/artifacts/upload/finalize?upload_id="+uploadID+"&checksum="+hex.EncodeToString(digest[:]), nil)
		request.Header.Set("X-Synon-User-Id", "user-1")
		request.Header.Set("Idempotency-Key", "attachment-finalize-http-retry")
		response := httptest.NewRecorder()
		app.ServeHTTP(response, request)
		responses <- response
	}
	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, 2)
	var finalizers sync.WaitGroup
	for range 2 {
		finalizers.Add(1)
		go func() {
			defer finalizers.Done()
			finalize(start, responses)
		}()
	}
	close(start)
	finalizers.Wait()
	close(responses)
	var finalized *httptest.ResponseRecorder
	for response := range responses {
		if response.Code != http.StatusOK {
			t.Fatalf("concurrent finalize = %d: %s", response.Code, response.Body.String())
		}
		if finalized == nil {
			finalized = response
		} else if response.Body.String() != finalized.Body.String() {
			t.Fatalf("concurrent finalize result mismatch: %s != %s", response.Body.String(), finalized.Body.String())
		}
	}
	if finalized == nil {
		t.Fatal("concurrent finalize produced no response")
	}
	var result map[string]any
	if err := json.Unmarshal(finalized.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	artifactID, _ := result["artifact_id"].(string)
	versionID, _ := result["version_id"].(string)
	if len(result) != 5 || artifactID == "" || versionID == "" || result["checksum"] != hex.EncodeToString(digest[:]) || result["size_bytes"] != float64(len(payload)) {
		t.Fatalf("finalized = %#v", result)
	}
	artifacts, err := store.ListCompatibilityProjectArtifacts(context.Background(), "user-1", "project-1", true, 10)
	if err != nil || len(artifacts) != 1 || artifacts[0].ID != artifactID || artifacts[0].VersionID != versionID || !artifacts[0].IsUserUpload {
		t.Fatalf("finalized project artifact=%#v err=%v", artifacts, err)
	}
	conflictRequest := httptest.NewRequest(http.MethodPost,
		"/api/artifacts/upload/finalize?upload_id="+uploadID+"&checksum="+strings.Repeat("0", 64), nil)
	conflictRequest.Header.Set("X-Synon-User-Id", "user-1")
	conflictRequest.Header.Set("Idempotency-Key", "attachment-finalize-http-retry")
	conflict := httptest.NewRecorder()
	app.ServeHTTP(conflict, conflictRequest)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("finalize changed retry = %d: %s", conflict.Code, conflict.Body.String())
	}
	downloadRequest := httptest.NewRequest(http.MethodGet, "/api/artifacts/"+artifactID, nil)
	downloadRequest.Header.Set("X-Synon-User-Id", "user-1")
	download := httptest.NewRecorder()
	app.ServeHTTP(download, downloadRequest)
	if download.Code != http.StatusOK || !bytes.Equal(download.Body.Bytes(), payload) {
		t.Fatalf("download = %d: %q", download.Code, download.Body.Bytes())
	}
	if _, err := os.Stat(filepath.Join(databasePath+".blobs", "uploads", uploadID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("finalized upload chunks still exist: %v", err)
	}

	cancelInit := httptest.NewRequest(http.MethodPost, "/api/artifacts/upload/init", bytes.NewBufferString(fmt.Sprintf(
		`{"project_id":"project-1","filename":"cancel.bin","total_size":%d,"chunk_size":%d}`, chunkSize, chunkSize,
	)))
	cancelInit.Header.Set("Content-Type", "application/json")
	cancelInit.Header.Set("X-Synon-User-Id", "user-1")
	duplicateRequest := httptest.NewRequest(http.MethodGet, "/api/attachments/"+artifactID+"/metadata", nil)
	duplicateRequest.Header.Set("X-Synon-User-Id", "user-1")
	duplicate := httptest.NewRecorder()
	app.ServeHTTP(duplicate, duplicateRequest)
	if duplicate.Code != http.StatusNotFound {
		t.Fatalf("chunked artifact retained duplicate attachment = %d: %s", duplicate.Code, duplicate.Body.String())
	}

	cancelInitialized := httptest.NewRecorder()
	app.ServeHTTP(cancelInitialized, cancelInit)
	if cancelInitialized.Code != http.StatusOK {
		t.Fatalf("cancel init = %d: %s", cancelInitialized.Code, cancelInitialized.Body.String())
	}
	var cancelState map[string]any
	if err := json.Unmarshal(cancelInitialized.Body.Bytes(), &cancelState); err != nil {
		t.Fatal(err)
	}
	cancelID, _ := cancelState["upload_id"].(string)
	cancelRequest := httptest.NewRequest(http.MethodDelete, "/api/artifacts/upload/"+cancelID, nil)
	cancelRequest.Header.Set("X-Synon-User-Id", "user-1")
	cancelled := httptest.NewRecorder()
	app.ServeHTTP(cancelled, cancelRequest)
	if cancelled.Code != http.StatusOK || cancelled.Body.String() != fmt.Sprintf("{\"message\":\"Upload %s cancelled\"}\n", cancelID) {
		t.Fatalf("cancel = %d: %s", cancelled.Code, cancelled.Body.String())
	}
	if _, err := os.Stat(filepath.Join(databasePath+".blobs", "uploads", cancelID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled upload chunks still exist: %v", err)
	}
}

func multipartAttachmentBody(t *testing.T, fileField, filename string, content []byte, fields map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := writer.CreateFormFile(fileField, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body, writer.FormDataContentType()
}

package server

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"testing"
)

func TestWebFSZipCreatesArchiveFromContentAndSourceFiles(t *testing.T) {
	stateRoot := t.TempDir()
	projectRoot := filepath.Join(stateRoot, "project")
	writeWebFSTestFile(t, filepath.Join(projectRoot, "source.txt"), "source-data")
	writeWebFSTestFile(t, filepath.Join(projectRoot, "nested", "other.txt"), "other-data")
	srv := newWebFSProjectTestServer(t, stateRoot, projectRoot, "owner")
	handler := http.HandlerFunc(srv.handleWebZip)

	response := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/zip", "owner", map[string]any{
		"path":        "bundle.zip",
		"workspace":   projectRoot,
		"source_root": projectRoot,
		"request_id":  "zip-real-content",
		"files": []map[string]any{
			{"name": "text/readme.txt", "content": "hello"},
			{"name": "bytes/data.bin", "content": []int{0, 1, 2, 255}},
			{"name": "bytes/object.bin", "content": map[string]int{"0": 65, "1": 66}},
			{"name": "source/camel.txt", "sourcePath": "source.txt"},
			{"name": "source/snake.txt", "source_path": "nested/other.txt"},
		},
	})
	requireWebFSStatus(t, response, http.StatusOK)
	var created bool
	decodeWebFSTestResponse(t, response, &created)
	if !created {
		t.Fatal("zip response was false")
	}

	entries := readWebFSTestZip(t, filepath.Join(projectRoot, "bundle.zip"))
	want := map[string][]byte{
		"text/readme.txt":  []byte("hello"),
		"bytes/data.bin":   {0, 1, 2, 255},
		"bytes/object.bin": []byte("AB"),
		"source/camel.txt": []byte("source-data"),
		"source/snake.txt": []byte("other-data"),
	}
	if len(entries) != len(want) {
		t.Fatalf("zip entries=%v", entries)
	}
	for name, expected := range want {
		actual, found := entries[name]
		if !found || string(actual) != string(expected) {
			t.Fatalf("entry %q=%v found=%v want=%v", name, actual, found, expected)
		}
	}

	conflict := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/zip", "owner", map[string]any{
		"path": "bundle.zip", "workspace": projectRoot,
		"files": []map[string]any{{"name": "new.txt", "content": "new"}},
	})
	requireWebFSStatus(t, conflict, http.StatusConflict)
	entries = readWebFSTestZip(t, filepath.Join(projectRoot, "bundle.zip"))
	if string(entries["text/readme.txt"]) != "hello" {
		t.Fatalf("existing archive was replaced: %v", entries)
	}
}

func TestWebFSZipRejectsUnsafeInputsAndSupportsCancellation(t *testing.T) {
	stateRoot := t.TempDir()
	projectRoot := filepath.Join(stateRoot, "project")
	srv := newWebFSProjectTestServer(t, stateRoot, projectRoot, "owner")
	handler := http.HandlerFunc(srv.handleWebZip)

	cases := map[string]struct {
		body   map[string]any
		status int
	}{
		"traversal": {
			body: map[string]any{
				"path": "traversal.zip", "workspace": projectRoot,
				"files": []map[string]any{{"name": "../escape.txt", "content": "bad"}},
			},
			status: http.StatusBadRequest,
		},
		"duplicate": {
			body: map[string]any{
				"path": "duplicate.zip", "workspace": projectRoot,
				"files": []map[string]any{
					{"name": "folder/../same.txt", "content": "first"},
					{"name": "same.txt", "content": "second"},
				},
			},
			status: http.StatusConflict,
		},
		"ambiguous-source": {
			body: map[string]any{
				"path": "ambiguous.zip", "workspace": projectRoot,
				"files": []map[string]any{{
					"name": "both.txt", "content": "data", "source_path": "source.txt",
				}},
			},
			status: http.StatusBadRequest,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			response := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/zip", "owner", testCase.body)
			requireWebFSStatus(t, response, testCase.status)
		})
	}

	ctx, done, err := srv.beginWebZip(context.Background(), "cancel-me")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if _, _, duplicateErr := srv.beginWebZip(context.Background(), "cancel-me"); !errors.Is(duplicateErr, errWebFSConflict) {
		t.Fatalf("duplicate request error=%v", duplicateErr)
	}
	canceled := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/zip/cancel", "owner", map[string]any{
		"request_id": "cancel-me",
	})
	requireWebFSStatus(t, canceled, http.StatusOK)
	var found bool
	decodeWebFSTestResponse(t, canceled, &found)
	if !found {
		t.Fatal("active zip request was not found")
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("active zip context was not canceled")
	}

	missing := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/zip/cancel", "owner", map[string]any{
		"request_id": "not-active",
	})
	requireWebFSStatus(t, missing, http.StatusOK)
	decodeWebFSTestResponse(t, missing, &found)
	if found {
		t.Fatal("unknown zip request was reported active")
	}
}

func readWebFSTestZip(t *testing.T, path string) map[string][]byte {
	t.Helper()
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	entries := make(map[string][]byte, len(reader.File))
	for _, file := range reader.File {
		input, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(input)
		closeErr := input.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		entries[file.Name] = data
	}
	return entries
}

package server

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCompatibilityExecutionLogAndSessionExport(t *testing.T) {
	app, rootFrameID, _, firstVersionID, _ := exportFixture(t)
	logRequest := httptest.NewRequest(http.MethodGet,
		"/api/frames/"+rootFrameID+"/execution-log?versionId="+firstVersionID, nil)
	logRequest.Header.Set("X-Synon-User-Id", "user-1")
	logResponse := httptest.NewRecorder()
	app.ServeHTTP(logResponse, logRequest)
	if logResponse.Code != http.StatusOK {
		t.Fatalf("execution log = %d: %s", logResponse.Code, logResponse.Body.String())
	}
	var records []map[string]any
	if err := json.Unmarshal(logResponse.Body.Bytes(), &records); err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("execution records = %#v", records)
	}
	record := records[0]
	if !strings.Contains(record["source"].(string), `{{artifact:artifact-data}}`) ||
		record["agent_name"] != "planner" || record["exit_status"] != "success" {
		t.Fatalf("execution record = %#v", record)
	}
	if len(record) != 17 || record["frame_id"] != rootFrameID || record["cell_index"] != float64(1) ||
		record["files_written"].([]any)[0] != "report.txt" || record["error_lineno"] != nil ||
		record["delegate_name"] != nil {
		t.Fatalf("execution v1.1 projection = %#v", record)
	}
	if _, found := record["files_read"]; found {
		t.Fatalf("execution record leaked Go-only files_read: %#v", record)
	}
	if _, err := time.Parse("2006-01-02T15:04:05.000Z", record["executed_at"].(string)); err != nil {
		t.Fatalf("execution timestamp = %#v: %v", record["executed_at"], err)
	}

	// v1.1 accepts an unambiguous root prefix and streams gzip when requested.
	exportRequest := httptest.NewRequest(http.MethodGet, "/api/frames/frame-ro/export", nil)
	exportRequest.Header.Set("X-Synon-User-Id", "user-1")
	exportRequest.Header.Set("Accept-Encoding", "gzip")
	exported := httptest.NewRecorder()
	app.ServeHTTP(exported, exportRequest)
	if exported.Code != http.StatusOK || exported.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("session export = %d headers=%#v body=%s", exported.Code, exported.Header(), exported.Body.String())
	}
	compressed, err := gzip.NewReader(exported.Body)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(compressed)
	_ = compressed.Close()
	if err != nil {
		t.Fatal(err)
	}
	session := decodeJSONMap(t, data)
	if session["export_version"] != "1.0" || session["root_frame_id"] != rootFrameID ||
		session["project_id"] != "project-1" || session["conversation_name"] != "Export Session" {
		t.Fatalf("session export identity = %#v", session)
	}
	summary := session["summary"].(map[string]any)
	if summary["total_frames"] != float64(1) || summary["status"] != "completed" ||
		!strings.Contains(summary["totals_note"].(string), "subtree-cumulative") {
		t.Fatalf("session summary = %#v", summary)
	}
	frames := session["frames"].([]any)
	if len(frames) != 1 || len(frames[0].(map[string]any)["messages"].([]any)) != 1 {
		t.Fatalf("session frames = %#v", frames)
	}
	artifacts := session["artifacts"].([]any)
	if len(artifacts) != 1 || artifacts[0].(map[string]any)["version_id"] != firstVersionID {
		t.Fatalf("session artifacts = %#v", artifacts)
	}
	if disposition := exported.Header().Get("Content-Disposition"); !strings.Contains(disposition, "frame-ro_") || !strings.Contains(disposition, ".json") {
		t.Fatalf("session export disposition = %q", disposition)
	}
}

func TestCompatibilitySessionFullAndSlicedBundles(t *testing.T) {
	app, rootFrameID, _, firstVersionID, _ := exportFixture(t)
	for _, test := range []struct {
		name   string
		target string
		scope  string
	}{
		{name: "full", target: "/api/frames/" + rootFrameID + "/bundle?scope=full", scope: "full"},
		{name: "sliced", target: "/api/frames/" + rootFrameID + "/bundle?scope=sliced&version_id=" + firstVersionID, scope: "sliced"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.target, nil)
			request.Header.Set("X-Synon-User-Id", "user-1")
			response := httptest.NewRecorder()
			app.ServeHTTP(response, request)
			if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/zip" {
				t.Fatalf("session bundle = %d: %s", response.Code, response.Body.String())
			}
			if !strings.Contains(response.Header().Get("Content-Disposition"), "bundle-frame-ro-"+test.scope+".zip") {
				t.Fatalf("bundle disposition = %q", response.Header().Get("Content-Disposition"))
			}
			entries := zipEntryContents(t, response.Body.Bytes())
			base := "frames/planner-frame-ro/01-replay/"
			for _, name := range []string{
				"manifest.json", "README.md", base + "notebook.ipynb", base + "environment.yml",
				base + "inputs/data.json", base + "outputs/report.txt", base + "run.sh",
			} {
				if _, ok := entries[name]; !ok {
					t.Fatalf("session bundle missing %q: %#v", name, entries)
				}
			}
			if string(entries[base+"inputs/data.json"]) != `{"ok":true}` || string(entries[base+"outputs/report.txt"]) != "version one" {
				t.Fatalf("bundle captured files = %#v", entries)
			}
			notebook := decodeJSONMap(t, entries[base+"notebook.ipynb"])
			if notebook["nbformat"] != float64(4) || !strings.Contains(string(entries[base+"notebook.ipynb"]), "inputs/data.json") ||
				strings.Contains(string(entries[base+"notebook.ipynb"]), "{{artifact:") {
				t.Fatalf("replay notebook = %s", entries[base+"notebook.ipynb"])
			}
			manifest := decodeJSONMap(t, entries["manifest.json"])
			if manifest["root_frame_id"] != rootFrameID || manifest["scope"] != test.scope {
				t.Fatalf("bundle manifest = %#v", manifest)
			}
			if mode := zipEntryMode(t, response.Body.Bytes(), base+"run.sh"); mode&0o111 == 0 {
				t.Fatalf("bundle run.sh mode = %v", mode)
			}
		})
	}

	missingVersion := httptest.NewRequest(http.MethodGet, "/api/frames/"+rootFrameID+"/bundle?scope=sliced", nil)
	missingVersion.Header.Set("X-Synon-User-Id", "user-1")
	missingResponse := httptest.NewRecorder()
	app.ServeHTTP(missingResponse, missingVersion)
	if missingResponse.Code != http.StatusBadRequest || !strings.Contains(missingResponse.Body.String(), "version_id is required") {
		t.Fatalf("missing sliced version = %d: %s", missingResponse.Code, missingResponse.Body.String())
	}
}

func TestCompatibilityScriptBundleUsesExtractedCodeAndInputs(t *testing.T) {
	app, _, _, firstVersionID, _ := exportFixture(t)
	request := httptest.NewRequest(http.MethodGet, "/api/artifacts/"+firstVersionID+"/script-bundle", nil)
	request.Header.Set("X-Synon-User-Id", "user-1")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("script bundle = %d: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Header().Get("Content-Disposition"), "script-"+firstVersionID[:8]+".zip") {
		t.Fatalf("script disposition = %q", response.Header().Get("Content-Disposition"))
	}
	entries := zipEntryContents(t, response.Body.Bytes())
	for _, name := range []string{"run.py", "run.sh", "README.md", "environment.yml", "inputs/data.json"} {
		if _, ok := entries[name]; !ok {
			t.Fatalf("script bundle missing %q: %#v", name, entries)
		}
	}
	if !strings.Contains(string(entries["run.py"]), "inputs/data.json") || strings.Contains(string(entries["run.py"]), "{{artifact:") {
		t.Fatalf("script source = %q", entries["run.py"])
	}
	if string(entries["inputs/data.json"]) != `{"ok":true}` || !strings.Contains(string(entries["environment.yml"]), "python=3.12") {
		t.Fatalf("script bundle content = %#v", entries)
	}
	if mode := zipEntryMode(t, response.Body.Bytes(), "run.sh"); mode&0o111 == 0 {
		t.Fatalf("script run.sh mode = %v", mode)
	}

	missing := httptest.NewRequest(http.MethodGet, "/api/artifacts/missing-version/script-bundle", nil)
	missing.Header.Set("X-Synon-User-Id", "user-1")
	missingResponse := httptest.NewRecorder()
	app.ServeHTTP(missingResponse, missing)
	if missingResponse.Code != http.StatusNotFound || !strings.Contains(missingResponse.Body.String(), "Version missing-version not found") {
		t.Fatalf("missing script version = %d: %s", missingResponse.Code, missingResponse.Body.String())
	}
}

func zipEntryMode(t *testing.T, content []byte, name string) uint32 {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range reader.File {
		if file.Name == name {
			return uint32(file.Mode().Perm())
		}
	}
	t.Fatalf("ZIP entry %q not found", name)
	return 0
}

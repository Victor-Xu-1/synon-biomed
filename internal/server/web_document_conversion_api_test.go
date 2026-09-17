package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestWebDocumentConversionUsesNativeParsers(t *testing.T) {
	root := t.TempDir()
	files := writeWebDocumentFixtures(t, filepath.Join(root, "project"))

	docx, err := convertWebDOCXToMarkdown(files["docx"])
	if err != nil || docx != "# Title\n\nBody text" {
		t.Fatalf("DOCX markdown = %q err=%v", docx, err)
	}
	csv, err := convertWebSpreadsheetToJSON(files["csv"])
	if err != nil || len(csv.Sheets) != 1 || len(csv.Sheets[0].Data) != 2 ||
		csv.Sheets[0].Data[1][0] != "alpha" {
		t.Fatalf("CSV workbook = %#v err=%v", csv, err)
	}
	xlsx, err := convertWebSpreadsheetToJSON(files["xlsx"])
	if err != nil {
		t.Fatal(err)
	}
	if len(xlsx.Sheets) != 1 || xlsx.Sheets[0].Name != "Data" ||
		len(xlsx.Sheets[0].Data) != 2 || xlsx.Sheets[0].Data[0][0] != "name" ||
		xlsx.Sheets[0].Data[0][1] != float64(7) ||
		xlsx.Sheets[0].Data[1][0] != "alpha" || xlsx.Sheets[0].Data[1][1] != true ||
		len(xlsx.Sheets[0].Merges) != 1 ||
		xlsx.Sheets[0].Merges[0].End != (webExcelCellRef{Row: 0, Col: 1}) {
		t.Fatalf("XLSX workbook = %#v", xlsx)
	}
	pptx, err := convertWebPPTXToJSON(files["pptx"])
	if err != nil || len(pptx.Slides) != 2 ||
		pptx.Slides[0].SlideNumber != 1 || pptx.Slides[1].SlideNumber != 2 {
		t.Fatalf("PPTX = %#v err=%v", pptx, err)
	}
	first := pptx.Slides[0].Content.(map[string]any)
	if first["text"] != "First" {
		t.Fatalf("first slide = %#v", first)
	}
}

func TestWebDocumentConversionAPIContract(t *testing.T) {
	stateRoot := t.TempDir()
	projectRoot := filepath.Join(stateRoot, "project")
	files := writeWebDocumentFixtures(t, projectRoot)
	srv := newWebFSProjectTestServer(t, stateRoot, projectRoot, "local")
	handler := srv.Handler()
	tests := []struct {
		name string
		file string
		to   string
	}{
		{"markdown", files["markdown"], "markdown"},
		{"docx", files["docx"], "markdown"},
		{"csv", files["csv"], "excel-json"},
		{"xlsx", files["xlsx"], "excel-json"},
		{"pptx", files["pptx"], "ppt-json"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := webFSTestRequest(t, handler, http.MethodPost,
				"/api/document/convert", "local", map[string]any{
					"file_path": test.file, "workspace": projectRoot, "to": test.to,
				})
			requireWebFSStatus(t, response, http.StatusOK)
			var result struct {
				To     string                      `json:"to"`
				Result webDocumentConversionResult `json:"result"`
			}
			decodeWebFSTestResponse(t, response, &result)
			if result.To != test.to || !result.Result.Success || result.Result.Data == nil {
				t.Fatalf("conversion result = %#v", result)
			}
		})
	}

	unsupported := webFSTestRequest(t, handler, http.MethodPost,
		"/api/document/convert", "local", map[string]any{
			"file_path": files["markdown"], "workspace": projectRoot, "to": "ppt-json",
		})
	requireWebFSStatus(t, unsupported, http.StatusOK)
	var failed struct {
		Result webDocumentConversionResult `json:"result"`
	}
	decodeWebFSTestResponse(t, unsupported, &failed)
	if failed.Result.Success || failed.Result.Error == "" {
		t.Fatalf("unsupported conversion = %#v", failed)
	}

	invalid := webFSTestRequest(t, handler, http.MethodPost,
		"/api/document/convert", "local", map[string]any{
			"file_path": files["markdown"], "to": "html",
		})
	requireWebFSStatus(t, invalid, http.StatusBadRequest)
}

func TestWebDocumentConversionAPIConvertsOwnedArtifactVersion(t *testing.T) {
	stateRoot := t.TempDir()
	projectRoot := filepath.Join(stateRoot, "project")
	files := writeWebDocumentFixtures(t, projectRoot)
	srv := newWebFSProjectTestServer(t, stateRoot, projectRoot, "local")

	source, err := os.Open(files["docx"])
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	_, version, err := srv.workspaceStore.SaveArtifactVersionFromReader(
		context.Background(),
		workspace.SaveArtifactVersionReaderInput{
			ArtifactID: "document-artifact", ProjectID: "web-fs-project", Name: "report.docx",
			Kind: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", Content: source,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	response := webFSTestRequest(t, srv.Handler(), http.MethodPost,
		"/api/document/convert", "local", map[string]any{
			"artifact_id": "document-artifact", "version_id": version.ID, "to": "markdown",
		})
	requireWebFSStatus(t, response, http.StatusOK)
	var result struct {
		To     string                      `json:"to"`
		Result webDocumentConversionResult `json:"result"`
	}
	decodeWebFSTestResponse(t, response, &result)
	if result.To != "markdown" || !result.Result.Success || result.Result.Data != "# Title\n\nBody text" {
		t.Fatalf("artifact conversion result = %#v", result)
	}

	conflict := webFSTestRequest(t, srv.Handler(), http.MethodPost,
		"/api/document/convert", "local", map[string]any{
			"artifact_id": "document-artifact", "file_path": files["docx"], "to": "markdown",
		})
	requireWebFSStatus(t, conflict, http.StatusBadRequest)

	missing := webFSTestRequest(t, srv.Handler(), http.MethodPost,
		"/api/document/convert", "local", map[string]any{
			"artifact_id": "missing-artifact", "to": "markdown",
		})
	requireWebFSStatus(t, missing, http.StatusNotFound)

	orphanVersion := webFSTestRequest(t, srv.Handler(), http.MethodPost,
		"/api/document/convert", "local", map[string]any{
			"version_id": version.ID, "to": "markdown",
		})
	requireWebFSStatus(t, orphanVersion, http.StatusBadRequest)
}

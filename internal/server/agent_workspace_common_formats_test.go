package server

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentWorkspaceReadsCommonDocumentsWithRegisteredBuiltinReaders(t *testing.T) {
	root := t.TempDir()
	files := writeWebDocumentFixtures(t, filepath.Join(root, "documents"))

	tests := []struct {
		name       string
		fixtureKey string
		filename   string
		formatID   string
		assert     func(*testing.T, map[string]any)
	}{
		{
			name: "DOCX detected from its container instead of its extension", fixtureKey: "docx", filename: "opaque-upload.bin", formatID: "word_ooxml",
			assert: func(t *testing.T, value map[string]any) {
				if content := stringValue(value["content"]); content != "# Title\n\nBody text" {
					t.Fatalf("DOCX content = %q", content)
				}
			},
		},
		{
			name: "XLSX", fixtureKey: "xlsx", filename: "table.xlsx", formatID: "excel_ooxml",
			assert: func(t *testing.T, value map[string]any) {
				workbook, ok := value["workbook"].(webExcelWorkbook)
				if !ok || len(workbook.Sheets) != 1 || workbook.Sheets[0].Data[1][0] != "alpha" {
					t.Fatalf("XLSX workbook = %#v", value["workbook"])
				}
			},
		},
		{
			name: "PPTX", fixtureKey: "pptx", filename: "slides.pptx", formatID: "powerpoint_ooxml",
			assert: func(t *testing.T, value map[string]any) {
				presentation, ok := value["presentation"].(webPPTJSON)
				if !ok || len(presentation.Slides) != 2 {
					t.Fatalf("PPTX presentation = %#v", value["presentation"])
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, err := os.ReadFile(files[test.fixtureKey])
			if err != nil {
				t.Fatal(err)
			}
			result, err := readAgentWorkspaceFile(
				context.Background(), bytes.NewReader(raw), test.filename,
				"application/octet-stream", int64(len(raw)), map[string]any{"version_id": "immutable-input"},
			)
			if err != nil {
				t.Fatal(err)
			}
			value, ok := result.(map[string]any)
			if !ok {
				t.Fatalf("result = %#v", result)
			}
			contract := value["reader_contract"].(map[string]any)
			if contract["format_id"] != test.formatID || contract["status"] != "parsed" || contract["recognition"] != "container_signature" {
				t.Fatalf("reader contract = %#v", contract)
			}
			if contract["preferred_tools"] != nil {
				t.Fatalf("parsed built-in document advertised an unnecessary external reader: %#v", contract)
			}
			test.assert(t, value)
		})
	}
}

func TestAgentWorkspaceReadsHTMLAndDelimitedTablesAsSemanticContent(t *testing.T) {
	htmlSource := []byte(`<!doctype html><html><head><title>Study page</title><script>secretNoise()</script></head><body><nav>menu</nav><h1>Results</h1><p>Active compound</p></body></html>`)
	result, err := readAgentWorkspaceFile(context.Background(), bytes.NewReader(htmlSource), "study.html", "text/html", int64(len(htmlSource)), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	htmlValue := result.(map[string]any)
	if content := stringValue(htmlValue["content"]); content != "Results\nActive compound" || strings.Contains(content, "secretNoise") || strings.Contains(content, "menu") {
		t.Fatalf("HTML readable content = %q", content)
	}
	if htmlValue["title"] != "Study page" || htmlValue["reader_contract"].(map[string]any)["format_id"] != "html_document" {
		t.Fatalf("HTML metadata = %#v", htmlValue)
	}

	for _, test := range []struct {
		name, filename, mediaType, source string
	}{
		{name: "CSV", filename: "assay.csv", mediaType: "text/csv", source: "compound,score\nA,-8.2\n"},
		{name: "TSV", filename: "assay.tsv", mediaType: "text/tab-separated-values", source: "compound\tscore\nA\t-8.2\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := readAgentWorkspaceFile(context.Background(), strings.NewReader(test.source), test.filename, test.mediaType, int64(len(test.source)), map[string]any{})
			if err != nil {
				t.Fatal(err)
			}
			value := result.(map[string]any)
			workbook := value["workbook"].(webExcelWorkbook)
			if len(workbook.Sheets) != 1 || len(workbook.Sheets[0].Data) != 2 || workbook.Sheets[0].Data[1][0] != "A" {
				t.Fatalf("workbook = %#v", workbook)
			}
			if value["reader_contract"].(map[string]any)["format_id"] != "delimited_table" {
				t.Fatalf("reader contract = %#v", value["reader_contract"])
			}
		})
	}
}

func TestAgentWorkspaceMacroDocumentUsesPassiveReaderWithoutExecutingMacros(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "report.docm")
	writeWebDocumentZip(t, filePath, map[string]string{
		"word/document.xml":   `<w:document xmlns:w="urn:word"><w:body><w:p><w:r><w:t>Safe text</w:t></w:r></w:p></w:body></w:document>`,
		"word/vbaProject.bin": "must never execute",
	})
	raw, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := readAgentWorkspaceFile(context.Background(), bytes.NewReader(raw), "report.docm", "application/octet-stream", int64(len(raw)), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	value := result.(map[string]any)
	contract := value["reader_contract"].(map[string]any)
	if contract["format_id"] != "word_ooxml" || contract["active_content_ignored"] != true || value["content"] != "Safe text" {
		t.Fatalf("macro-safe result = %#v", value)
	}
}

func TestAgentWorkspaceUncommonFormatReturnsModelReaderSelectionContract(t *testing.T) {
	raw := []byte("PAR1\x00opaque-columnar-data")
	result, err := readAgentWorkspaceFile(context.Background(), bytes.NewReader(raw), "assay.parquet", "application/octet-stream", int64(len(raw)), map[string]any{"version_id": "version-input"})
	if err != nil {
		t.Fatal(err)
	}
	value := result.(map[string]any)
	contract := value["reader_contract"].(map[string]any)
	selection := value["reader_selection"].(map[string]any)
	if value["inspection_status"] != "reader_required" || contract["format_id"] != "columnar_table" || contract["recognition"] != "filename_hint" {
		t.Fatalf("uncommon format contract = %#v", value)
	}
	if selection["mode"] != "model_select_verified_reader" || len(selection["requirements"].([]string)) != 4 {
		t.Fatalf("reader selection = %#v", selection)
	}
}

func TestAgentWorkspaceLargeCommonMediaKeepsOriginalAndRoutesAReader(t *testing.T) {
	for _, test := range []struct {
		name, filename, mediaType string
		raw                       []byte
		size                      int64
		formatID                  string
	}{
		{name: "large image", filename: "figure.png", mediaType: "image/png", raw: []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, size: agentWorkspaceVisualMaxBytes + 1, formatID: "raster_image"},
		{name: "large PDF", filename: "report.pdf", mediaType: "application/pdf", raw: []byte("%PDF-1.7\n"), size: agentWorkspacePDFMaxBytes + 1, formatID: "pdf_document"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := readAgentWorkspaceFile(context.Background(), bytes.NewReader(test.raw), test.filename, test.mediaType, test.size, map[string]any{"version_id": "immutable-large-input"})
			if err != nil {
				t.Fatal(err)
			}
			value := result.(map[string]any)
			contract := value["reader_contract"].(map[string]any)
			if value["inspection_status"] != "reader_required" || value["_policy_skip"] == true || contract["format_id"] != test.formatID {
				t.Fatalf("large media routing = %#v", value)
			}
			if value["source_reference"].(map[string]any)["version_id"] != "immutable-large-input" {
				t.Fatalf("large media lost immutable source = %#v", value)
			}
		})
	}
}

func TestAgentWorkspaceFileFormatRegistryIsUnambiguousAndCoversCommonFormats(t *testing.T) {
	if problems := validateAgentWorkspaceFileFormatRegistry(); len(problems) > 0 {
		t.Fatalf("format registry problems = %v", problems)
	}
	for _, filename := range []string{"page.html", "data.csv", "data.tsv", "report.docx", "workbook.xlsx", "slides.pptx", "paper.pdf", "legacy.doc", "legacy.xls", "legacy.ppt", "unknown.parquet"} {
		format, _, found := agentWorkspaceFormatHint(filename, "application/octet-stream")
		if !found || format.ID == "unknown_binary" {
			t.Errorf("%s is missing from the format registry", filename)
		}
	}
	docx, _, _ := agentWorkspaceFormatHint("report.docx", "application/octet-stream")
	if metadata := agentWorkspaceFormatMetadata(docx, "filename_hint", "available", "report.docx"); metadata["preferred_tools"] != nil {
		t.Fatalf("available built-in reader advertised an unnecessary external tool: %#v", metadata)
	}
	unknown, _, _ := agentWorkspaceFormatHint("opaque.bin", "application/octet-stream")
	if metadata := agentWorkspaceFormatMetadata(unknown, "unknown", "reader_required", "opaque.bin"); len(stringArrayValue(metadata["preferred_tools"])) != 2 {
		t.Fatalf("unknown reader has no model-selectable recovery tools: %#v", metadata)
	}
}

package lspstatic

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"synon-go/internal/tools/fileevents"
)

func TestPassiveLSPDiagnosticRegistryDeduplicatesAndPreservesOtherFiles(t *testing.T) {
	ResetAllLSPDiagnosticState()
	defer ResetAllLSPDiagnosticState()
	diagnostic := json.RawMessage(`{"range":{"start":{"line":1,"character":2},"end":{"line":1,"character":5}},"severity":1,"message":"first error","source":"fixture"}`)
	RegisterPendingLSPDiagnostic("fixture-lsp", []DiagnosticFile{
		{URI: "file:///workspace/a.go", Diagnostics: []json.RawMessage{diagnostic, diagnostic}},
		{URI: "file:///workspace/b.go", Diagnostics: []json.RawMessage{json.RawMessage(`{"severity":2,"message":"second warning"}`)}},
	})

	first := CheckForLSPDiagnosticsForURI("file:///workspace/a.go")
	if len(first) != 1 || len(first[0].Files) != 1 || len(first[0].Files[0].Diagnostics) != 1 {
		t.Fatalf("first URI diagnostics = %#v", first)
	}
	if PendingLSPDiagnosticCount() != 1 {
		t.Fatalf("pending count after first URI = %d", PendingLSPDiagnosticCount())
	}
	repeated := CheckForLSPDiagnosticsForURI("file:///workspace/a.go")
	if len(repeated) != 0 {
		t.Fatalf("repeated delivered diagnostics should be deduplicated: %#v", repeated)
	}
	remaining := CheckForLSPDiagnostics()
	if len(remaining) != 1 || len(remaining[0].Files) != 1 || remaining[0].Files[0].URI != "file:///workspace/b.go" {
		t.Fatalf("remaining diagnostics = %#v", remaining)
	}
}

func TestPendingDiagnosticsOutputFormatsRegistryResult(t *testing.T) {
	ResetAllLSPDiagnosticState()
	defer ResetAllLSPDiagnosticState()
	RegisterPendingLSPDiagnostic("fixture-lsp", []DiagnosticFile{{
		URI:         "file:///workspace/sample.go",
		Diagnostics: []json.RawMessage{json.RawMessage(`{"severity":"Error","message":"broken symbol"}`)},
	}})

	output, ok := pendingDiagnosticsOutput("diagnostics", "sample.go", "file:///workspace/sample.go")
	if !ok {
		t.Fatal("expected pending diagnostics output")
	}
	if output.ResultCount != 1 || output.FileCount != 1 {
		t.Fatalf("counts = result %d file %d", output.ResultCount, output.FileCount)
	}
	if !strings.Contains(output.Result, "broken symbol") || !strings.Contains(output.Result, "passive diagnostic registry") {
		t.Fatalf("result = %s", output.Result)
	}
}

func TestFileChangeEventClearsDeliveredDiagnostics(t *testing.T) {
	ResetAllLSPDiagnosticState()
	defer ResetAllLSPDiagnosticState()
	path := filepath.Join(t.TempDir(), "sample.go")
	uri := fileURI(path)
	diagnostic := json.RawMessage(`{"severity":1,"message":"same error"}`)

	RegisterPendingLSPDiagnostic("fixture-lsp", []DiagnosticFile{{URI: uri, Diagnostics: []json.RawMessage{diagnostic}}})
	if first := CheckForLSPDiagnosticsForURI(uri); len(first) != 1 {
		t.Fatalf("first diagnostics = %#v", first)
	}
	RegisterPendingLSPDiagnostic("fixture-lsp", []DiagnosticFile{{URI: uri, Diagnostics: []json.RawMessage{diagnostic}}})
	if repeated := CheckForLSPDiagnosticsForURI(uri); len(repeated) != 0 {
		t.Fatalf("repeated diagnostics should be filtered before file change: %#v", repeated)
	}
	RegisterPendingLSPDiagnostic("fixture-lsp", []DiagnosticFile{{URI: uri, Diagnostics: []json.RawMessage{diagnostic}}})
	fileevents.NotifyChanged(path)
	if afterChange := CheckForLSPDiagnosticsForURI(uri); len(afterChange) != 1 {
		t.Fatalf("diagnostics after file change = %#v", afterChange)
	}
}

package server

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
)

func TestAgentWorkspacePDFTextPagesReachTailWithoutChangingOriginal(t *testing.T) {
	verifyAgentWorkspacePDFTextPaging(t, workspacePDFTextFixture(200))
}

// Opt-in read-only replay verifies the reader against an operator-selected
// original. Test work uses an isolated copy, never the captured task's state.
func TestAgentWorkspacePDFCapturedTextReplay(t *testing.T) {
	path := os.Getenv("SYNON_TEST_PDF_SOURCE")
	if path == "" {
		t.Skip("no captured PDF source selected")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	verifyAgentWorkspacePDFTextPaging(t, raw)
}

func verifyAgentWorkspacePDFTextPaging(t *testing.T, raw []byte) {
	t.Helper()
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext is not installed")
	}
	fixture := newAgentSaveArtifactsFixture(t)
	path := filepath.Join(fixture.projectPath, "long-document.pdf")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx := withAgentWorkspaceReadBudget(context.Background(), 16000)
	input := map[string]any{"file_path": "long-document.pdf"}
	baselineCommand := exec.Command("pdftotext", "-layout", "-nopgbrk", "-enc", "UTF-8", path, "-")
	var baselineError strings.Builder
	baselineCommand.Stderr = &baselineError
	baseline, err := baselineCommand.Output()
	if err != nil || len(baseline) <= agentWorkspaceReadMaxBytes {
		t.Fatalf("PDF fixture extraction failed or does not exceed one window: %v; bytes=%d stderr=%q", err, len(baseline), baselineError.String())
	}
	// PDF drawing operations do not define extracted line boundaries. Compare
	// every line, including whitespace and the tail, with independent native
	// extraction instead of assuming one display line per drawing operation.
	expected := strings.Split(strings.TrimSuffix(string(baseline), "\n"), "\n")
	seen := make(map[int]string)
	previous, calls := 0, 0
	for calls < len(expected) {
		result, err := fixture.server.executeAgentWorkspaceFileTool(ctx, fixture.identity, "read_file", input)
		if err != nil {
			t.Fatal(err)
		}
		value, parts := unwrapAgentRuntimeRichToolResponse(result)
		view := mapValue(value)
		if calls == 0 {
			if len(parts) != 1 || parts[0].Type != agentruntime.ContentPartDocument || !bytes.Equal(parts[0].Media.Source.Data, raw) {
				t.Fatal("default read lost the original PDF attachment")
			}
		} else if len(parts) != 0 {
			t.Fatal("explicit text continuation unexpectedly resent visual media")
		}
		if view["view_format"] != "pdf-extracted-text-lines" || !agentWorkspaceReadResultFits(ctx, view) || numberValue(view["truncated_lines"]) != 0 {
			t.Fatalf("PDF continuation is missing, lossy or oversized: format=%v fits=%v truncated_lines=%v", view["view_format"], agentWorkspaceReadResultFits(ctx, view), view["truncated_lines"])
		}
		if view["file_path_scope"] != "original_source" || view["file_path"] != path {
			t.Fatalf("lost source location: %#v", view["file_path"])
		}
		for _, entry := range strings.Split(strings.TrimSuffix(stringValue(view["content"]), "\n"), "\n") {
			position, text, ok := strings.Cut(entry, "\t")
			line, parseErr := strconv.Atoi(position)
			if !ok || parseErr != nil || line < 1 || line > len(expected) {
				t.Fatalf("invalid indexed PDF text entry %q", entry)
			}
			if _, duplicate := seen[line]; duplicate {
				t.Fatalf("PDF text line %d repeated across windows", line)
			}
			seen[line] = text
		}
		calls++
		if view["truncated"] != true {
			break
		}
		next := int(numberValue(view["next_offset"]))
		if next <= previous {
			t.Fatalf("continuation did not advance: next=%d previous=%d", next, previous)
		}
		input["offset"], input["limit"] = next, 200
		previous = next
	}
	if len(seen) != len(expected) || calls < 2 {
		t.Fatalf("reached %d/%d extracted lines in %d windows", len(seen), len(expected), calls)
	}
	for line, text := range expected {
		if seen[line+1] != text {
			t.Fatalf("extracted line %d changed or was lost", line+1)
		}
	}
	t.Logf("read all %d native text bytes across %d windows", len(baseline), calls)
	stored, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(stored, raw) {
		t.Fatal("text view modified the original PDF")
	}
}

func TestAgentWorkspacePDFExplicitTextOffset(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext is not installed")
	}
	raw := workspacePDFTextFixture(5)
	result, err := readAgentWorkspaceFile(context.Background(), bytes.NewReader(raw), "document.pdf", "application/pdf", int64(len(raw)), map[string]any{"offset": 4, "limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	value, parts := unwrapAgentRuntimeRichToolResponse(result)
	view := mapValue(value)
	if len(parts) != 0 || !strings.Contains(stringValue(view["content"]), "ROW-0004") || strings.Contains(stringValue(view["content"]), "ROW-0001") {
		t.Fatalf("PDF offset ignored: parts=%d content=%q", len(parts), view["content"])
	}
	if view["next_offset"] != 5 || view["visual_parts"] != 0 {
		t.Fatalf("text-only continuation metadata=%#v", view)
	}
}

func TestAgentWorkspacePDFTextCancellationReapsExtractor(t *testing.T) {
	converter, err := exec.LookPath("pdftotext")
	if err != nil {
		t.Skip("pdftotext is not installed")
	}
	path := filepath.Join(t.TempDir(), "document.pdf")
	if err := os.WriteFile(path, workspacePDFTextFixture(200), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, finished := streamAgentWorkspacePDFText(ctx, converter, path, nil)
	defer reader.Close()
	buffer := make([]byte, 64)
	if count, err := reader.Read(buffer); err != nil || count == 0 {
		t.Fatalf("real extractor did not begin output: count=%d err=%v", count, err)
	}
	cancel()
	_ = reader.Close()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled PDF extractor was not reaped")
	}
}

func TestAgentWorkspacePDFTextFailureDoesNotPretendToRead(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext is not installed")
	}
	raw := []byte("%PDF-1.4\ninvalid-document\n")
	if _, err := readAgentWorkspaceFile(context.Background(), bytes.NewReader(raw), "broken.pdf", "application/pdf", int64(len(raw)), map[string]any{"offset": 1}); err == nil {
		t.Fatal("invalid PDF was reported as a successful text read")
	}
	result, err := readAgentWorkspaceFile(context.Background(), bytes.NewReader(raw), "broken.pdf", "application/pdf", int64(len(raw)), nil)
	if err != nil {
		t.Fatal(err)
	}
	value, parts := unwrapAgentRuntimeRichToolResponse(result)
	if len(parts) != 1 || mapValue(mapValue(value)["text_extraction"])["status"] != "unavailable" {
		t.Fatal("default attachment failure lost original media or invented extracted text")
	}
}

func TestAgentWorkspacePDFEmptyTextKeepsVisualReadingSeparate(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext is not installed")
	}
	raw := workspacePDFTextFixture(0)
	result, err := readAgentWorkspaceFile(context.Background(), bytes.NewReader(raw), "empty.pdf", "application/pdf", int64(len(raw)), nil)
	if err != nil {
		t.Fatal(err)
	}
	value, parts := unwrapAgentRuntimeRichToolResponse(result)
	view := mapValue(value)
	if len(parts) != 1 || mapValue(view["text_extraction"])["status"] != "no_extractable_text" || mapValue(view["reader_contract"])["status"] != "attached" {
		t.Fatalf("empty PDF text invented reading coverage: %#v", view)
	}
}

func TestAgentWorkspacePDFPageReadExposesValidTextContinuation(t *testing.T) {
	for _, tool := range []string{"pdftotext", "pdftoppm"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " is not installed")
		}
	}
	raw := minimalAgentWorkspacePDF()
	ctx := withAgentWorkspaceReadBudget(context.Background(), 4096)
	result, err := readAgentWorkspaceFile(ctx, bytes.NewReader(raw), "pages.pdf", "application/pdf", int64(len(raw)), map[string]any{
		"file_path": "pages.pdf", "pages": []any{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	value, parts := unwrapAgentRuntimeRichToolResponse(result)
	view := mapValue(value)
	readWith := mapValue(view["text_read_with"])
	if len(parts) != 1 || parts[0].Type != agentruntime.ContentPartImage || !agentWorkspaceReadResultFits(ctx, view) ||
		readWith["pages"] != nil || readWith["offset"] != 1 || readWith["file_path"] != "pages.pdf" || view["next_offset"] != nil {
		t.Fatalf("visual page read exposed an invalid text cursor: %#v", view)
	}
	if err := validateAgentWorkspaceReadFileInput(readWith); err != nil {
		t.Fatalf("text continuation is not an admissible read_file request: %v", err)
	}
}

func TestAgentWorkspacePDFStageReadsAuthorizedHandleNotReplacedPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.pdf")
	original := workspacePDFTextFixture(5)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := os.Rename(path, path+".held"); err != nil {
		t.Skip("platform cannot rename an open fixture")
	}
	replacement := bytes.Replace(original, []byte("ROW-0001"), []byte("OTHER001"), 1)
	if err := os.WriteFile(path, replacement, 0600); err != nil {
		t.Fatal(err)
	}
	staged, cleanup, err := stageAgentWorkspacePDFSource(context.Background(), file, int64(len(original)))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	actual, err := os.ReadFile(staged)
	if err != nil || !bytes.Equal(actual, original) {
		t.Fatal("PDF reader reopened an unverified same-sized replacement")
	}
}

func workspacePDFTextFixture(rows int) []byte {
	var content strings.Builder
	// Keep the synthetic page within common PDF geometry bounds while its
	// extracted text remains substantially larger than one read window.
	fmt.Fprintf(&content, "BT /F1 6 Tf 8 TL 10 %d Td\n", rows*8+20)
	for row := 1; row <= rows; row++ {
		fmt.Fprintf(&content, "(ROW-%04d %s) Tj T*\n", row, strings.Repeat("abcdefghij", 100))
	}
	content.WriteString("ET\n")
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 3600 %d] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>", rows*8+40),
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", content.Len(), content.String()),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	for index, object := range objects {
		offsets = append(offsets, pdf.Len())
		fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	xref := pdf.Len()
	fmt.Fprintf(&pdf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&pdf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return pdf.Bytes()
}

package memorytools

import (
	"strings"
	"testing"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

func TestWorkspaceSearchFooterOnlyNamesActuallyTruncatedEntities(t *testing.T) {
	previousNow := toolNow
	toolNow = func() time.Time { return time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { toolNow = previousNow })

	short := []workspace.Memory{{
		ID: "short", Body: "brief durable fact", Evidence: "observed", SubjectProjectID: "project-a", UpdatedAt: toolNow(),
	}}
	output, _ := renderSearchResults(short, nil)
	if strings.Contains(output, "Bodies truncated") {
		t.Fatalf("short search result received a truncation footer: %q", output)
	}

	longBody := strings.Repeat("x", 201)
	rows := []workspace.Memory{
		{ID: "one", Body: longBody, Evidence: "observed", SubjectProjectID: "project-a", UpdatedAt: toolNow()},
		{ID: "short-artifact", Body: "not truncated", Evidence: "observed", SubjectArtifactID: "short-artifact", UpdatedAt: toolNow()},
		{ID: "two", Body: longBody, Evidence: "observed", SubjectArtifactID: "artifact-a", UpdatedAt: toolNow()},
	}
	output, _ = renderSearchResults(rows, nil)
	if !strings.Contains(output, strings.Repeat("x", 200)+"…") {
		t.Fatalf("search preview did not preserve the reference 200+ellipsis contract: %q", output)
	}
	if !strings.Contains(output, `read_memory("project:project-a")`) || !strings.Contains(output, `"artifact:artifact-a"`) {
		t.Fatalf("truncation footer omitted truncated entities: %q", output)
	}
	if strings.Contains(output, `"artifact:short-artifact"`) {
		t.Fatalf("truncation footer named an untruncated entity: %q", output)
	}
}

func TestWorkspaceToolInlineSanitizerPreservesOrdinarySpacing(t *testing.T) {
	got := sanitizeInline("## Heading\n[Memory] durable  detail\n  next")
	if got != "Heading durable  detail next" {
		t.Fatalf("tool inline sanitizer = %q", got)
	}
}

func TestWorkspaceReadLimitUsesJavaScriptNegativeSliceSemantics(t *testing.T) {
	rows := []workspace.Memory{
		{ID: "mem_one", Body: "first", Evidence: "stated"},
		{ID: "mem_two", Body: "second", Evidence: "stated"},
		{ID: "mem_three", Body: "third", Evidence: "stated"},
	}
	got := renderEntityDocument(resolvedEntity{key: "profile"}, rows, nil, -1)
	if !strings.Contains(got, "mem_one") || !strings.Contains(got, "mem_two") || strings.Contains(got, "mem_three") {
		t.Fatalf("negative read_tool_max slice = %q", got)
	}
	if !strings.Contains(got, "…4 more") {
		t.Fatalf("negative read_tool_max count parity = %q", got)
	}
}

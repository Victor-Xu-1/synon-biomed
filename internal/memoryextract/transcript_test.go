package memoryextract

import (
	"strings"
	"testing"

	"synon-go/internal/memorypolicy"
	"synon-go/internal/memorytools"
)

func TestWorkspaceHasUserProseSince(t *testing.T) {
	messages := []Message{{Role: "user", Content: []Block{
		{Type: BlockText, Text: "[System] ignored words here"},
		{Type: BlockText, Text: "[Memory] ignored words here"},
		{Type: BlockText, Text: "only two"},
		{Type: BlockText, Text: "好"},
	}}}
	if HasUserProseSince(messages) {
		t.Fatal("notice and sub-three-word content counted as prose")
	}
	messages[0].Content = append(messages[0].Content, Block{Type: BlockText, Text: "three durable words"})
	if !HasUserProseSince(messages) {
		t.Fatal("three-word user prose was missed")
	}
	messages[0].Content = append(messages[0].Content, Block{Type: BlockText, Text: "中文请求没有空格"})
	if !HasUserProseSince(messages) {
		t.Fatal("CJK user prose was missed")
	}
}

func TestWorkspaceHasMemoryWritesSince(t *testing.T) {
	failed := []Message{
		{Role: "assistant", Content: []Block{{Type: BlockToolUse, ToolName: "write_memory", ToolUseID: "call-1", ToolInput: map[string]any{"replace": []any{map[string]any{"id": "mem"}}}}}},
		{Role: "user", Content: []Block{{Type: BlockToolResult, ToolUseID: "call-1", ToolError: true, Text: memorytools.ErrMemoryClassifierUnavailable.Error()}}},
	}
	if HasMemoryWritesSince(failed, memorypolicy.ExtractMaxPerRun) {
		t.Fatal("failed PI-gated write suppressed extraction")
	}
	successfulReplace := failed[:1]
	if !HasMemoryWritesSince(successfulReplace, memorypolicy.ExtractMaxPerRun) {
		t.Fatal("successful replacement was not treated as a durable write")
	}

	appendCalls := make([]Block, 0, memorypolicy.ExtractMaxPerRun+1)
	appendCalls = append(appendCalls, Block{Type: BlockToolUse, ToolName: "write_memory", ToolUseID: "frame", ToolInput: map[string]any{"entity": "frame"}})
	for index := 0; index < memorypolicy.ExtractMaxPerRun-1; index++ {
		appendCalls = append(appendCalls, Block{Type: BlockToolUse, ToolName: "write_memory", ToolUseID: string(rune('a' + index)), ToolInput: map[string]any{"entity": "project:p"}})
	}
	if HasMemoryWritesSince([]Message{{Role: "assistant", Content: appendCalls}}, memorypolicy.ExtractMaxPerRun) {
		t.Fatal("frame write or fewer than max durable append calls suppressed extraction")
	}
	appendCalls = append(appendCalls, Block{Type: BlockToolUse, ToolName: "write_memory", ToolUseID: "last", ToolInput: map[string]any{"entity": "project:p"}})
	if !HasMemoryWritesSince([]Message{{Role: "assistant", Content: appendCalls}}, memorypolicy.ExtractMaxPerRun) {
		t.Fatal("max durable append calls did not suppress extraction")
	}
	if HasMemoryWritesSince([]Message{{Role: "assistant", Content: []Block{{Type: BlockToolUse, ToolName: "compute_details", ToolInput: map[string]any{"mode": "read"}}}}}, memorypolicy.ExtractMaxPerRun) {
		t.Fatal("compute_details read counted as a write")
	}
	if !HasMemoryWritesSince([]Message{{Role: "assistant", Content: []Block{{Type: BlockToolUse, ToolName: "compute_details", ToolInput: map[string]any{"mode": "append"}}}}}, memorypolicy.ExtractMaxPerRun) {
		t.Fatal("compute_details mutation was missed")
	}
}

func TestWorkspaceDigestTranscriptAndServerToolStubs(t *testing.T) {
	stub := "[System] Prior-turn result\n<persisted-output>\n  - https://example.test — Example title\n  - artifact.csv\n</persisted-output>"
	messages := []Message{
		{Role: "user", Content: []Block{{Type: BlockText, Text: "[Memory] recalled"}, {Type: BlockText, Text: "real request"}, {Type: BlockText, Text: stub, HarnessNotice: true}}},
		{Role: "assistant", Content: []Block{{Type: BlockToolUse, ToolName: "search_memory"}, {Type: BlockToolResult, Text: "secret result"}, {Type: BlockText, Text: "final answer"}}},
	}
	digest := DigestTranscript(messages, 8000)
	want := "user: real request\n---\nassistant [tool_use search_memory]\n---\nassistant: final answer"
	if digest != want {
		t.Fatalf("digest = %q", digest)
	}
	bodies := CollectServerToolStubBodies(messages)
	if len(bodies) != 4 || bodies[0] != stub || bodies[1] != "https://example.test" || bodies[2] != "Example title" || bodies[3] != "artifact.csv" {
		t.Fatalf("stub bodies = %#v", bodies)
	}

	long := DigestTranscript([]Message{{Role: "user", Content: []Block{{Type: BlockText, Text: strings.Repeat("x", 9000)}}}}, 8000)
	if !strings.HasPrefix(long, "…") || memorypolicy.UTF16Length(strings.TrimPrefix(long, "…")) != 8000 {
		t.Fatalf("long digest length = %d", memorypolicy.UTF16Length(long))
	}
}

func TestWorkspaceCompactProjectionElidesDiskImagesAndAnchorsAssistantDelta(t *testing.T) {
	messages := []Message{
		{Role: "user", Ignore: true, Content: []Block{{Type: BlockText, Text: "obsolete compacted row"}}},
		{Role: "assistant", Content: []Block{
			{Type: BlockImage, DiskReference: true},
			{Type: BlockImage},
			{Type: BlockText, Text: "durable finding"},
		}},
	}
	projected := ProjectCompactMessages(messages)
	if len(projected) != 2 || projected[0].Role != "user" || projected[0].Content[0].Text != extractionSessionContinued {
		t.Fatalf("projected messages = %#v", projected)
	}
	digest := DigestTranscript(projected, memorypolicy.ExtractionTranscriptMaxUTF16Units)
	want := "user: [session continued]\n---\nassistant: [image elided for extraction pass]\n---\nassistant: durable finding"
	if digest != want {
		t.Fatalf("digest = %q, want %q", digest, want)
	}
	if messages[1].Content[0].Type != BlockImage || messages[1].Content[0].Text != "" {
		t.Fatalf("source messages were mutated: %#v", messages)
	}
}

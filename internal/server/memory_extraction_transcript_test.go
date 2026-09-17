package server

import (
	"encoding/json"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	"synon-go/internal/memoryextract"
	"synon-go/internal/memorypolicy"
	"synon-go/internal/memorytools"
	eventjournal "synon-go/internal/persistence/journal"
)

func TestWorkspaceMemoryExtractionTranscriptUsesDurableModelMessages(t *testing.T) {
	arguments, err := json.Marshal(map[string]any{
		"replace": []any{map[string]any{"id": "mem_old", "text": "new value"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	messages := []agentruntime.Message{
		{Role: "user", Parts: []agentruntime.ContentPart{
			{Type: agentruntime.ContentPartText, Text: "Please retain this durable preference"},
			{Type: agentruntime.ContentPartImage, Media: &agentruntime.MediaContent{Source: agentruntime.MediaSource{Type: agentruntime.MediaSourceFile, Path: "/private/image.png"}}},
			{Type: agentruntime.ContentPartImage, Media: &agentruntime.MediaContent{Source: agentruntime.MediaSource{Type: agentruntime.MediaSourceData, Data: []byte("image")}}},
		}},
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "call-memory", Name: "write_memory", Arguments: arguments}}},
		{Role: "tool", ToolCallID: "call-memory", Content: `{"error":"` + memorytools.ErrMemoryClassifierUnavailable.Error() + `"}`},
		{Role: "user", Content: "[Memory] recalled prior value"},
		{Role: "user", Content: "[System] Prior-turn result\n<persisted-output>\n  - https://example.test — Example\n</persisted-output>"},
	}
	converted := memoryExtractionMessages(messages)
	if len(converted) != len(messages) || converted[2].Role != "user" || !converted[2].Content[0].ToolError {
		t.Fatalf("converted messages = %#v", converted)
	}
	if memoryextract.HasMemoryWritesSince(converted, memorypolicy.ExtractMaxPerRun) {
		t.Fatal("failed memory write incorrectly suppressed post-turn extraction")
	}
	digest := memoryextract.DigestTranscript(
		memoryextract.ProjectCompactMessages(converted), memorypolicy.ExtractionTranscriptMaxUTF16Units,
	)
	for _, want := range []string{
		"user: Please retain this durable preference",
		"user: [image elided for extraction pass]",
		"assistant [tool_use write_memory]",
	} {
		if !strings.Contains(digest, want) {
			t.Fatalf("digest missing %q: %q", want, digest)
		}
	}
	for _, forbidden := range []string{"image.png", "recalled prior value", "example.test", "classifier unavailable"} {
		if strings.Contains(digest, forbidden) {
			t.Fatalf("digest leaked %q: %q", forbidden, digest)
		}
	}
}

func TestWorkspaceMemoryExtractionTranscriptToleratesMalformedToolArguments(t *testing.T) {
	converted := memoryExtractionMessages([]agentruntime.Message{{
		Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "bad", Name: "write_memory", Arguments: json.RawMessage(`{`)}},
	}})
	if len(converted) != 1 || len(converted[0].Content) != 1 || len(converted[0].Content[0].ToolInput) != 0 {
		t.Fatalf("converted malformed tool call = %#v", converted)
	}
}

func TestWorkspaceMemoryExtractionJournalProjectionPreservesStructuredBlocks(t *testing.T) {
	entry := eventjournal.Entry{Message: eventjournal.Message{
		"type": "message", "role": "user",
		"content": []any{
			map[string]any{"type": "text", "text": "Retain this durable assay decision"},
			map[string]any{"type": "image", "source": map[string]any{"type": "_disk_ref", "path": "/private/assay.png"}},
			map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": "https://example.test/remote.png"}},
			map[string]any{"type": "tool_use", "id": "call-memory", "name": "write_memory", "input": map[string]any{
				"append": []any{map[string]any{"text": "Retain this durable assay decision", "evidence": "stated"}},
			}},
			map[string]any{"type": "tool_result", "tool_use_id": "call-memory", "is_error": true, "content": memorytools.ErrMemoryClassifierUnavailable.Error()},
		},
	}}
	converted := memoryExtractionMessagesFromJournalEntry(entry)
	if len(converted) != 1 || len(converted[0].Content) != 5 {
		t.Fatalf("journal projection = %#v", converted)
	}
	if !converted[0].Content[1].DiskReference || converted[0].Content[2].DiskReference {
		t.Fatalf("journal image projection = %#v", converted[0].Content)
	}
	if converted[0].Content[3].ToolName != "write_memory" || converted[0].Content[3].ToolInput["append"] == nil ||
		!converted[0].Content[4].ToolError || converted[0].Content[4].ToolUseID != "call-memory" {
		t.Fatalf("journal tool projection = %#v", converted[0].Content)
	}
	digest := memoryextract.DigestTranscript(memoryextract.ProjectCompactMessages(converted), memorypolicy.ExtractionTranscriptMaxUTF16Units)
	if !strings.Contains(digest, "[image elided for extraction pass]") || strings.Contains(digest, "assay.png") || strings.Contains(digest, "remote.png") {
		t.Fatalf("journal digest = %q", digest)
	}
}

func TestWorkspaceMemoryExtractionJournalProjectionDropsFailedPartialAssistant(t *testing.T) {
	converted := memoryExtractionMessagesFromJournalEntry(eventjournal.Entry{Message: eventjournal.Message{
		"type": "message", "role": "assistant", "partial": true,
		"text": "unfinished model output must not become durable memory",
	}})
	if len(converted) != 0 {
		t.Fatalf("partial assistant projection = %#v", converted)
	}
}

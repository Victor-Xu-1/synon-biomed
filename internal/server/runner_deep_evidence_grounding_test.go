package server

import (
	"encoding/json"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
)

func TestCitationGroundingRejectsMCPDiscoveryButAcceptsARecordRead(t *testing.T) {
	doi := "10.1234/deep-record"
	search := agentruntime.ToolCall{ID: "search", Name: "mcp__pubmed__search_articles"}
	detail := agentruntime.ToolCall{ID: "detail", Name: "mcp__pubmed__get_article_metadata"}
	searchMessages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{search}},
		{Role: "tool", ToolCallID: search.ID, Content: `{"ok":true,"result":"{\"records\":[{\"doi\":\"` + doi + `\",\"title\":\"candidate\"}]}"}`},
	}
	if got := unsupportedSessionRunnerCitationCount(searchMessages, 0, "DOI: "+doi, nil); got != 1 {
		t.Fatalf("MCP discovery grounded a claim-level citation: %d", got)
	}
	detailMessages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{detail}},
		{Role: "tool", ToolCallID: detail.ID, Content: `{"ok":true,"result":"{\"doi\":\"` + doi + `\",\"abstract\":\"substantive abstract\"}"}`},
	}
	if got := unsupportedSessionRunnerCitationCount(detailMessages, 0, "DOI: "+doi, nil); got != 0 {
		t.Fatalf("MCP structured record did not ground its identifier: %d", got)
	}
}

func TestCitationGroundingUsesOnlyDeepReadWebResearchDocuments(t *testing.T) {
	candidateDOI := "10.1234/candidate-only"
	readDOI := "10.1234/read-record"
	call := agentruntime.ToolCall{ID: "research", Name: "web_research"}
	payload := map[string]any{
		"ok": true,
		"result": map[string]any{
			"candidateSources": []any{map[string]any{"doi": candidateDOI, "title": "candidate"}},
			"documents": []any{
				map[string]any{"content": "DOI: " + candidateDOI, "readReceipt": map[string]any{"deepRead": false}},
				map[string]any{"content": "DOI: " + readDOI, "readReceipt": map[string]any{"deepRead": true}},
			},
		},
	}
	raw, _ := json.Marshal(payload)
	messages := []agentruntime.Message{{Role: "assistant", ToolCalls: []agentruntime.ToolCall{call}}, {Role: "tool", ToolCallID: call.ID, Content: string(raw)}}
	if got := unsupportedSessionRunnerCitationCount(messages, 0, "DOI: "+candidateDOI, nil); got != 1 {
		t.Fatalf("web research candidate grounded a citation: %d", got)
	}
	if got := unsupportedSessionRunnerCitationCount(messages, 0, "DOI: "+readDOI, nil); got != 0 {
		t.Fatalf("deep-read web research document did not ground citation: %d", got)
	}
}

func TestCitationGroundingRejectsThinOrPartialWebFetch(t *testing.T) {
	doi := "10.1234/web-full-record"
	call := agentruntime.ToolCall{ID: "fetch", Name: "web_fetch"}
	messages := func(body string, partial bool) []agentruntime.Message {
		payload, _ := json.Marshal(map[string]any{"ok": true, "result": map[string]any{
			"statusCode": 200, "url": "https://example.test/article", "body": body, "partial": partial,
		}})
		return []agentruntime.Message{{Role: "assistant", ToolCalls: []agentruntime.ToolCall{call}}, {Role: "tool", ToolCallID: call.ID, Content: string(payload)}}
	}
	if got := unsupportedSessionRunnerCitationCount(messages("DOI: "+doi, false), 0, "DOI: "+doi, nil); got != 1 {
		t.Fatalf("thin web page grounded a citation: %d", got)
	}
	substantive := "DOI: " + doi + " " + strings.Repeat("primary source methods results limitations. ", 40)
	if got := unsupportedSessionRunnerCitationCount(messages(substantive, true), 0, "DOI: "+doi, nil); got != 1 {
		t.Fatalf("partial web page grounded a citation: %d", got)
	}
	if got := unsupportedSessionRunnerCitationCount(messages(substantive, false), 0, "DOI: "+doi, nil); got != 0 {
		t.Fatalf("complete substantive web page did not ground citation: %d", got)
	}
}

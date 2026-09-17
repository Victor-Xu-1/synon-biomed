package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestWorkspaceLargeJSONReadMakesLosslessProgressWithinRunnerBudget(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"record": map[string]any{
			"metadata": "A structured source",
			"text":     strings.Repeat("原始结果\\n引号 \"x\" 和数据\n", 12000) + "SOURCE_TAIL",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	expected, wrapped, err := agentWorkspaceJSONReadView(raw)
	if err != nil || !wrapped {
		t.Fatalf("wrapped=%t err=%v", wrapped, err)
	}
	ctx := withAgentWorkspaceReadBudget(context.Background(), runnerLargeToolResultInlineLimitBytes)
	offset := 1
	var assembled strings.Builder
	for page := 0; page < 1000; page++ {
		value, err := readAgentWorkspaceFile(ctx, bytes.NewReader(raw), "tool-result.json", "application/json", int64(len(raw)), map[string]any{"offset": offset, "limit": 2000})
		if err != nil {
			t.Fatal(err)
		}
		result := mapValue(value)
		if !agentWorkspaceReadResultFits(ctx, result) {
			t.Fatalf("page %d would externalize recursively", page)
		}
		if numberValue(result["truncated_lines"]) != 0 {
			t.Fatalf("page %d discarded source bytes", page)
		}
		content := stringValue(result["content"])
		if content == "" {
			t.Fatalf("empty page at offset %d", offset)
		}
		for _, line := range strings.Split(strings.TrimSuffix(content, "\n"), "\n") {
			_, text, found := strings.Cut(line, "\t")
			if !found {
				t.Fatalf("missing view line number: %q", line)
			}
			assembled.WriteString(text)
			assembled.WriteByte('\n')
		}
		next := int(numberValue(result["next_offset"]))
		if next == 0 {
			break
		}
		if next <= offset {
			t.Fatalf("cursor failed to advance %d -> %d", offset, next)
		}
		offset = next
	}
	if strings.TrimSuffix(assembled.String(), "\n") != string(expected) {
		t.Fatalf("paginated source changed: got=%d expected=%d", assembled.Len(), len(expected))
	}
	if !strings.Contains(assembled.String(), "SOURCE_TAIL") {
		t.Fatal("source tail inaccessible")
	}
}

func TestWorkspaceReadBudgetAccountsForJSONEscaping(t *testing.T) {
	raw := []byte(strings.Repeat("quoted \"text\" and \\slashes\\\n", 1800))
	ctx := withAgentWorkspaceReadBudget(context.Background(), runnerLargeToolResultInlineLimitBytes)
	value, err := readAgentWorkspaceFile(ctx, bytes.NewReader(raw), "text.txt", "text/plain", int64(len(raw)), map[string]any{"offset": 1, "limit": 2000})
	if err != nil {
		t.Fatal(err)
	}
	if !agentWorkspaceReadResultFits(ctx, value) || int(numberValue(mapValue(value)["next_offset"])) <= 1 {
		t.Fatal("escaped result exceeded the transport or lost its cursor")
	}
}

func TestWorkspaceJSONDirectoryLocatesLateStructuredFields(t *testing.T) {
	raw, err := json.Marshal(map[string]any{"result": map[string]any{
		"candidates": strings.Repeat("discovery ", 15000),
		"documents":  map[string]any{"text": "ACTUAL_RETRIEVED_DOCUMENT"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	view, _, err := agentWorkspaceJSONReadView(raw)
	if err != nil {
		t.Fatal(err)
	}
	var first, last int
	for _, line := range strings.Split(string(view), "\n") {
		if strings.HasPrefix(line, "\"/result/documents\"\t") {
			if _, err := fmt.Sscanf(strings.TrimPrefix(line, "\"/result/documents\"\t"), "%d-%d", &first, &last); err != nil {
				t.Fatal(err)
			}
		}
	}
	if first < 2 || last < first {
		t.Fatalf("missing directory range %d-%d", first, last)
	}
	value, err := readAgentWorkspaceFile(withAgentWorkspaceReadBudget(context.Background(), 16000), bytes.NewReader(raw), "result.json", "application/json", int64(len(raw)), map[string]any{"offset": first, "limit": last - first + 1})
	if err != nil || !strings.Contains(stringValue(mapValue(value)["content"]), "ACTUAL_RETRIEVED_DOCUMENT") {
		t.Fatalf("directory did not resolve to data: result=%#v err=%v", value, err)
	}
}

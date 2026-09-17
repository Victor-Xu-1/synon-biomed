package mcpstdio

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFormatToolCallResultReturnsStructuredNotFoundAsData(t *testing.T) {
	payload := map[string]any{
		"isError": true,
		"content": []any{map[string]any{
			"type": "text",
			"text": `{"found":false,"nct_id":"NCT00000000","error":"Trial NCT00000000 not found"}`,
		}},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	result, err := formatToolCallResult(raw)
	if err != nil || !strings.Contains(result, `"found":false`) {
		t.Fatalf("structured miss result=%q err=%v", result, err)
	}
}

func TestFormatToolCallResultKeepsUnavailableMissAsError(t *testing.T) {
	payload := map[string]any{
		"isError": true,
		"content": []any{map[string]any{
			"type": "text",
			"text": `{"found":false,"sourceUnavailable":true,"retryable":true,"error":"upstream not found"}`,
		}},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := formatToolCallResult(raw); err == nil || result != "" {
		t.Fatalf("unavailable miss result=%q err=%v", result, err)
	}
}

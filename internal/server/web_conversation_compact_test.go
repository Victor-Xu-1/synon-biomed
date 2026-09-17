package server

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestCompactWebConversationPreservesAskUserInteractionContract(t *testing.T) {
	question := strings.Repeat("material decision ", 100)
	input := map[string]any{
		"questions": []any{map[string]any{
			"header":   "Round 2",
			"question": question,
			"options": []any{
				map[string]any{"label": "Strict selectivity", "description": strings.Repeat("bounded rationale ", 60)},
			},
		}},
	}
	output := `{"version":1,"status":"answered","action":"answer","answers":{"question":"Strict selectivity"}}`
	for _, name := range []string{"ask_user", "AskUserQuestion", "ask_user_question"} {
		message := map[string]any{
			"type": "tool_call",
			"content": map[string]any{
				"name":   name,
				"input":  input,
				"output": output,
				"status": "completed",
			},
		}
		compacted := compactWebConversationMessage(message)
		content, _ := compacted["content"].(map[string]any)
		if !reflect.DeepEqual(content["input"], input) || content["output"] != output {
			t.Fatalf("%s interaction contract was truncated: %#v", name, content)
		}
		if _, truncated := content["_compact"]; truncated {
			t.Fatalf("%s interaction contract was marked compact: %#v", name, content)
		}
	}
}

func TestCompactWebConversationKeepsStructuredPreviewAndStableHumanDescription(t *testing.T) {
	message := map[string]any{
		"type": "tool_call",
		"content": map[string]any{
			"name": "python",
			"input": map[string]any{
				"human_description": "Compare the candidate structures",
				"records": []any{
					map[string]any{"id": "record-1", "score": 0.91, "payload": strings.Repeat("x", 4000)},
				},
			},
			"output": `{"records":[{"id":"record-1","nested":{"score":0.91,"evidence":"` +
				strings.Repeat("y", 4000) + `"}}]}`,
		},
	}

	compacted := compactWebConversationMessage(message)
	content := compacted["content"].(map[string]any)
	input, ok := content["input"].(map[string]any)
	if !ok {
		t.Fatalf("compact input is not structured: %#v", content["input"])
	}
	if input["human_description"] != "Compare the candidate structures" {
		t.Fatalf("compact input=%#v", input)
	}
	compact := content["_compact"].(map[string]any)
	if compact["human_description"] != "Compare the candidate structures" {
		t.Fatalf("compact metadata=%#v", compact)
	}
	output, ok := content["output"].(string)
	if !ok || !json.Valid([]byte(output)) {
		t.Fatalf("compact output is not valid structured JSON: %#v", content["output"])
	}
	var outputPreview map[string]any
	if err := json.Unmarshal([]byte(output), &outputPreview); err != nil || outputPreview["records"] == nil {
		t.Fatalf("output preview=%#v err=%v", outputPreview, err)
	}
	for _, field := range []struct {
		name  string
		limit int
	}{{"input", compactWebToolInputBytes}, {"output", compactWebToolOutputBytes}} {
		encoded, err := json.Marshal(content[field.name])
		if err != nil || len(encoded) > field.limit {
			t.Fatalf("%s preview bytes=%d limit=%d err=%v", field.name, len(encoded), field.limit, err)
		}
	}
}

func TestCompactWebConversationTextUsesAValidUTF8JSONBudget(t *testing.T) {
	preview := compactWebConversationText(strings.Repeat("生\"", 100), 32)
	encoded, err := json.Marshal(preview)
	if err != nil || len(encoded) > 32 || !strings.HasSuffix(preview, "…") {
		t.Fatalf("preview=%q encodedBytes=%d err=%v", preview, len(encoded), err)
	}
}

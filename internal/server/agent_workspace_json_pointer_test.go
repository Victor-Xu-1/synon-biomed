package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
)

func TestWorkspaceReadJSONPointerReachesLateData(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"a_large_first_field": strings.Repeat("unrelated", 50000), "data": map[string]any{"a/b": map[string]any{"~key": []any{"正文\n完整数值 1.25"}}}})
	input := map[string]any{"version_id": "ltr-test", "human_description": "读取目标数据", "json_pointer": "/data/a~1b/~0key/0"}
	if err := validateAgentWorkspaceReadFileInput(input); err != nil {
		t.Fatal(err)
	}
	ctx := withAgentWorkspaceReadBudget(context.Background(), runnerLargeToolResultInlineLimitBytes)
	value, err := readAgentWorkspaceFile(ctx, bytes.NewReader(raw), "data.json", "application/json", int64(len(raw)), input)
	if err != nil {
		t.Fatal(err)
	}
	r := mapValue(value)
	if !strings.Contains(stringValue(r["content"]), "完整数值 1.25") || strings.Contains(stringValue(r["content"]), "unrelated") {
		t.Fatalf("selected data unavailable: %#v", r)
	}
	if r["json_pointer"] != input["json_pointer"] || !agentWorkspaceReadResultFits(ctx, r) {
		t.Fatalf("selection lost its cursor or exceeded transport: %#v", r)
	}
}

func TestWorkspaceJSONPointerThroughAuthorizedToolGateway(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	schemas := []agentruntime.ToolSchema{agentWorkspaceReadFileToolSchema(), agentWorkspaceEditFileToolSchema()}
	gateway := serverAgentRuntimeToolGateway{server: fixture.server, kernel: fixture.identity, sessionID: fixture.stream.SessionID, toolSchemas: schemas, toolValidators: agentRuntimeToolValidators(schemas), hasToolSnapshot: true, suppressHooks: true}
	executeAgentWorkspaceToolForTest(t, gateway, "edit_file", map[string]any{"file_path": "structured.json", "old_string": "", "new_string": `{"measurements":[{"value":1.25},{"value":2.5}]}`})
	result := executeAgentWorkspaceToolForTest(t, gateway, "read_file", map[string]any{"file_path": "structured.json", "json_pointer": "/measurements/1/value"})
	if !strings.Contains(stringValue(result["content"]), "2.5") || result["json_pointer"] != "/measurements/1/value" {
		t.Fatalf("pointer lost at gateway: %#v", result)
	}
	for _, input := range []map[string]any{
		{"file_path": "structured.json", "json_pointer": 3},
		{"file_path": "structured.json", "json_pointer": "/a~9"},
		{"file_path": "structured.json", "json_pointer": "/a", "pages": []int{1}},
	} {
		if validateAgentWorkspaceReadFileInput(input) == nil {
			t.Fatalf("accepted incompatible selection: %#v", input)
		}
	}
}

func TestWorkspaceJSONPointerSyntaxAndEnvelope(t *testing.T) {
	for _, test := range []struct {
		raw, pointer, want string
		invalid            bool
	}{
		{`{"a":[null,false,9007199254740993]}`, "/a/2", `9007199254740993`, false},
		{`{"a":[null,false]}`, "/a/0", `null`, false},
		{`{"a":[null,false]}`, "/a/1", `false`, false},
		{`{"":7}`, "/", `7`, false},
		{`{"a~1b":"literal"}`, "/a~01b", `"literal"`, false},
		{`[1,2]`, "", `[1,2]`, false},
		{`{"a":[1]}`, "/a/01", "", true},
		{`{"a":[1]}`, "/a/-", "", true},
		{`{"a":1}`, "/a/x", "", true},
		{`{"a":1}`, "#/a", "", true},
		{`{"a":1}`, "/a~2", "", true},
		{`{"a":1}`, "/missing", "", true},
		{`{"a":1} {}`, "/a", "", true},
		{`{"a":1,"later":[}`, "/a", "", true},
		{`{"a":{"x":1},"a":{"y":2}}`, "/a/y", "", true},
	} {
		value, err := selectAgentWorkspaceJSONValue(context.Background(), strings.NewReader(test.raw), test.pointer)
		if test.invalid {
			if err == nil {
				t.Fatalf("accepted invalid selection %q in %s", test.pointer, test.raw)
			}
			continue
		}
		if err != nil || string(value) != test.want {
			t.Fatalf("pointer %q: got %s err=%v want=%s", test.pointer, value, err, test.want)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := selectAgentWorkspaceJSONValue(ctx, strings.NewReader(`{"a":1}`), "/a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
	deep := `{"skipped":` + strings.Repeat("[", 10001) + `0` + strings.Repeat("]", 10001) + `,"selected":1}`
	if _, err := selectAgentWorkspaceJSONValue(context.Background(), strings.NewReader(deep), "/selected"); err == nil {
		t.Fatal("skipping bypassed JSON nesting validation")
	}
}

func TestWorkspaceJSONPointerReadsBeyondFormattingThreshold(t *testing.T) {
	raw := `{"large":"` + strings.Repeat("x", 9<<20) + `","selected":{"value":42}}`
	value, err := readAgentWorkspaceFile(context.Background(), strings.NewReader(raw), "large.json", "application/json", int64(len(raw)), map[string]any{"json_pointer": "/selected"})
	if err != nil || !strings.Contains(stringValue(mapValue(value)["content"]), `"value": 42`) {
		t.Fatalf("large document field inaccessible: %#v %v", value, err)
	}
}

func TestWorkspaceJSONPointerPagesDecodedTextWithoutLoss(t *testing.T) {
	text := strings.Repeat("原始文字 \"引号\" \\路径\n", 3000) + "END_OF_SOURCE"
	raw, _ := json.Marshal(map[string]any{"payload": text})
	expected := string(agentWorkspaceSelectedTextView(text))
	ctx := withAgentWorkspaceReadBudget(context.Background(), runnerLargeToolResultInlineLimitBytes)
	offset := 1
	var got strings.Builder
	for page := 0; page < 1000; page++ {
		value, err := readAgentWorkspaceFile(ctx, bytes.NewReader(raw), "source.json", "application/json", int64(len(raw)), map[string]any{"json_pointer": "/payload", "offset": offset})
		if err != nil {
			t.Fatal(err)
		}
		r := mapValue(value)
		if !agentWorkspaceReadResultFits(ctx, r) || numberValue(r["truncated_lines"]) != 0 {
			t.Fatalf("lossy/oversized selected page: %#v", r)
		}
		for _, line := range strings.Split(strings.TrimSuffix(stringValue(r["content"]), "\n"), "\n") {
			_, content, ok := strings.Cut(line, "\t")
			if !ok {
				t.Fatal("missing line prefix")
			}
			got.WriteString(content)
			got.WriteByte('\n')
		}
		next := int(numberValue(r["next_offset"]))
		if next == 0 {
			break
		}
		if next <= offset {
			t.Fatal("selection cursor did not advance")
		}
		offset = next
	}
	if strings.TrimSuffix(got.String(), "\n") != expected {
		t.Fatalf("decoded source changed: got=%d want=%d", got.Len(), len(expected))
	}
}

func TestWorkspaceJSONDefaultIndexProvidesExecutableReadHandles(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"result": map[string]any{"candidates": strings.Repeat("locator", 50000), "documents": []any{map[string]any{"body": "COMPLETE_PRIMARY_DOCUMENT"}}}})
	ctx := withAgentWorkspaceReadBudget(context.Background(), runnerLargeToolResultInlineLimitBytes)
	value, err := readAgentWorkspaceFile(ctx, bytes.NewReader(raw), "source.json", "application/json", int64(len(raw)), map[string]any{"version_id": "ltr-source"})
	if err != nil {
		t.Fatal(err)
	}
	result := mapValue(value)
	if result["view_format"] != "json-field-preview" || result["source_content_included"] != true || !agentWorkspaceReadResultFits(ctx, result) {
		t.Fatalf("default read hid partial source as content: %#v", result)
	}
	found := false
	for _, section := range result["sections"].([]map[string]any) {
		if section["json_pointer"] != "/result/documents" {
			continue
		}
		found = true
		encoded, _ := json.Marshal(section)
		if !bytes.Contains(encoded, []byte("COMPLETE_PRIMARY_DOCUMENT")) {
			t.Fatal("default read returned navigation without the small late source value")
		}
		read := section["read_with"].(map[string]any)
		if err := validateAgentWorkspaceReadFileInput(read); err != nil {
			t.Fatal(err)
		}
		selected, err := readAgentWorkspaceFile(ctx, bytes.NewReader(raw), "source.json", "application/json", int64(len(raw)), read)
		if err != nil || !strings.Contains(stringValue(mapValue(selected)["content"]), "COMPLETE_PRIMARY_DOCUMENT") {
			t.Fatalf("returned selection handle could not read the document: %#v %v", selected, err)
		}
	}
	if !found {
		t.Fatal("late field was not discoverable in first read")
	}
	fullPage, err := readAgentWorkspaceFile(ctx, bytes.NewReader(raw), "source.json", "application/json", int64(len(raw)), map[string]any{"version_id": "ltr-source", "offset": 1})
	if err != nil || mapValue(fullPage)["view_format"] == "json-field-preview" {
		t.Fatalf("explicit raw pagination was replaced by index: %#v %v", fullPage, err)
	}
}

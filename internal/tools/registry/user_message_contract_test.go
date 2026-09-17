package registry

import (
	"reflect"
	"testing"
)

func TestUserMessageStatusSchemaMatchesExecutionContract(t *testing.T) {
	reg := Default()
	for _, toolName := range []string{"SendUserMessage"} {
		tool, ok := reg.Get(toolName)
		if !ok {
			t.Fatalf("missing %s", toolName)
		}
		status := tool.Input["status"]
		if status.Required {
			t.Fatalf("%s status must remain optional because execution defaults it to normal", toolName)
		}
		if got, want := status.Schema["enum"], []string{"normal", "proactive"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("%s status enum = %#v, want %#v", toolName, got, want)
		}
		if got := status.Schema["default"]; got != "normal" {
			t.Fatalf("%s status default = %#v, want normal", toolName, got)
		}

		if err := reg.Validate(toolName, map[string]any{"message": "done"}); err != nil {
			t.Fatalf("%s omitted status must use the execution default: %v", toolName, err)
		}
		if err := reg.Validate(toolName, map[string]any{"message": "done", "status": "normal"}); err != nil {
			t.Fatalf("%s normal status rejected: %v", toolName, err)
		}
		if err := reg.Validate(toolName, map[string]any{"message": "done", "status": "proactive"}); err != nil {
			t.Fatalf("%s proactive status rejected: %v", toolName, err)
		}
		if err := reg.Validate(toolName, map[string]any{"message": "done", "status": "completed"}); err == nil {
			t.Fatalf("%s completed status must be rejected by the advertised schema", toolName)
		}
	}
}

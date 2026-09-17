package registry

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"synon-go/internal/memorypolicy"
)

func TestWorkspaceMemoryToolsAreRegisteredButRequireTrustedSessionDispatch(t *testing.T) {
	registry := Default()
	for _, name := range []string{"read_memory", "write_memory", "search_memory"} {
		tool, exists := registry.Get(name)
		if !exists || !tool.Executable {
			t.Fatalf("memory tool %q = %#v, exists=%v", name, tool, exists)
		}
		if _, err := registry.Execute(context.Background(), name, validMemoryRegistryInput(name)); err == nil || !strings.Contains(err.Error(), "trusted agent session") {
			t.Fatalf("direct %s error = %v", name, err)
		}
	}
	if err := registry.Validate("read_memory", map[string]any{}); err == nil {
		t.Fatal("read_memory accepted a missing entity")
	}
	if err := registry.Validate("search_memory", map[string]any{}); err == nil {
		t.Fatal("search_memory accepted a missing query")
	}
	if err := registry.Validate("write_memory", map[string]any{"append": []any{map[string]any{"text": "fact"}}}); err != nil {
		t.Fatalf("write_memory schema rejected an append array: %v", err)
	}
}

func TestWorkspaceMemoryToolSchemasPreserveNestedLimitsAndEvidenceContract(t *testing.T) {
	registry := Default()
	tool, exists := registry.Get("write_memory")
	if !exists {
		t.Fatal("write_memory is not registered")
	}
	for _, fieldName := range []string{"append", "replace", "remove"} {
		field := tool.Input[fieldName]
		if field.Type != "array" || field.Schema["maxItems"] != memorypolicy.OperationsPerKindMax {
			t.Fatalf("write_memory.%s schema = %#v", fieldName, field)
		}
	}
	appendItem := memoryToolSchemaObject(t, tool.Input["append"].Schema["items"], "append.items")
	if appendItem["additionalProperties"] != false {
		t.Fatalf("append item must be closed: %#v", appendItem)
	}
	appendProperties := memoryToolSchemaObject(t, appendItem["properties"], "append.items.properties")
	assertMemoryTextSchema(t, memoryToolSchemaObject(t, appendProperties["text"], "append.text"))
	assertMemoryEvidenceSchema(t, memoryToolSchemaObject(t, appendProperties["evidence"], "append.evidence"))
	if !reflect.DeepEqual(appendItem["required"], []string{"text"}) {
		t.Fatalf("append required = %#v", appendItem["required"])
	}

	replaceItem := memoryToolSchemaObject(t, tool.Input["replace"].Schema["items"], "replace.items")
	if replaceItem["additionalProperties"] != false {
		t.Fatalf("replace item must be closed: %#v", replaceItem)
	}
	replaceProperties := memoryToolSchemaObject(t, replaceItem["properties"], "replace.items.properties")
	idSchema := memoryToolSchemaObject(t, replaceProperties["id"], "replace.id")
	if idSchema["maxLength"] != memorypolicy.MemoryIDMaxLength {
		t.Fatalf("replace id schema = %#v", idSchema)
	}
	assertMemoryTextSchema(t, memoryToolSchemaObject(t, replaceProperties["text"], "replace.text"))
	assertMemoryEvidenceSchema(t, memoryToolSchemaObject(t, replaceProperties["evidence"], "replace.evidence"))
	if !reflect.DeepEqual(replaceItem["required"], []string{"id", "text"}) {
		t.Fatalf("replace required = %#v", replaceItem["required"])
	}
	removeItem := memoryToolSchemaObject(t, tool.Input["remove"].Schema["items"], "remove.items")
	if removeItem["type"] != "string" || removeItem["maxLength"] != memorypolicy.MemoryIDMaxLength {
		t.Fatalf("remove item schema = %#v", removeItem)
	}
}

func assertMemoryTextSchema(t *testing.T, schema map[string]any) {
	t.Helper()
	if schema["type"] != "string" || schema["maxLength"] != memorypolicy.TextMaxUTF16Units {
		t.Fatalf("memory text schema = %#v", schema)
	}
}

func assertMemoryEvidenceSchema(t *testing.T, schema map[string]any) {
	t.Helper()
	if schema["type"] != "string" || !reflect.DeepEqual(schema["enum"], []string{"stated", "observed", "inferred"}) {
		t.Fatalf("memory evidence schema = %#v", schema)
	}
}

func memoryToolSchemaObject(t *testing.T, value any, label string) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s = %#v", label, value)
	}
	return object
}

func validMemoryRegistryInput(name string) map[string]any {
	switch name {
	case "read_memory":
		return map[string]any{"entity": "profile"}
	case "write_memory":
		return map[string]any{"append": []any{map[string]any{"text": "fact"}}}
	default:
		return map[string]any{"query": "known project preference"}
	}
}

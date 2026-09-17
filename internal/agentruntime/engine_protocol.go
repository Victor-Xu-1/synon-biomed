package agentruntime

import (
	"bytes"

	"encoding/json"

	"fmt"

	"strings"

	"synon-go/internal/toolcontract"
)

func runtimeToolSchemaKey(name string) string {
	if canonical, ok := toolcontract.NormalizeRuntimeName(strings.TrimSpace(name)); ok {
		return strings.ToLower(strings.TrimSpace(canonical))
	}
	return strings.ToLower(strings.TrimSpace(name))
}

func appendUniqueToolSchemas(target []ToolSchema, schemas ...ToolSchema) []ToolSchema {
	seen := make(map[string]struct{}, len(target)+len(schemas))
	for _, schema := range target {
		key := strings.ToLower(strings.TrimSpace(schema.Name))
		if key != "" {
			seen[key] = struct{}{}
		}
	}
	for _, schema := range schemas {
		key := strings.ToLower(strings.TrimSpace(schema.Name))
		if key == "" {
			continue
		}
		if _, found := seen[key]; found {
			continue
		}
		seen[key] = struct{}{}
		target = append(target, schema)
	}
	return target
}

func toolSchemaNamedFolded(schemas []ToolSchema, name string) bool {
	for _, schema := range schemas {
		if strings.EqualFold(strings.TrimSpace(schema.Name), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

type initialToolConstraint struct {
	required  bool
	forbidden bool
	name      string
}

// InitialToolChoiceRequiresCall reports whether a model request carries a
// server-enforced first-tool constraint. Boundary decorators must defer their
// response validation while this is true so Engine can privately discard and
// repair provider responses that violate the tool protocol.
func InitialToolChoiceRequiresCall(choice any) bool {
	return parseInitialToolConstraint(choice).required
}

func parseInitialToolConstraint(choice any) initialToolConstraint {
	switch value := choice.(type) {
	case string:
		switch {
		case strings.EqualFold(strings.TrimSpace(value), "required"):
			return initialToolConstraint{required: true}
		case strings.EqualFold(strings.TrimSpace(value), "none"):
			return initialToolConstraint{forbidden: true}
		}
	case map[string]any:
		kind := strings.TrimSpace(stringValueRuntime(value["type"]))
		name := strings.TrimSpace(stringValueRuntime(value["name"]))
		if name == "" {
			if function, ok := value["function"].(map[string]any); ok {
				name = strings.TrimSpace(stringValueRuntime(function["name"]))
			}
		}
		if name != "" && (strings.EqualFold(kind, "tool") || strings.EqualFold(kind, "function")) {
			return initialToolConstraint{required: true, name: name}
		}
	}
	return initialToolConstraint{}
}

func (constraint initialToolConstraint) satisfied(calls []ToolCall) bool {
	if constraint.forbidden {
		return len(calls) == 0
	}
	if !constraint.required {
		return true
	}
	if constraint.name == "" {
		return len(calls) > 0
	}
	return len(calls) == 1 && strings.EqualFold(strings.TrimSpace(calls[0].Name), constraint.name)
}

func (constraint initialToolConstraint) requestTools(tools []ToolSchema) ([]ToolSchema, bool) {
	if constraint.forbidden {
		return nil, true
	}
	if !constraint.required || constraint.name == "" {
		return tools, true
	}
	for _, tool := range tools {
		if strings.EqualFold(strings.TrimSpace(tool.Name), constraint.name) {
			return []ToolSchema{tool}, true
		}
	}
	return nil, false
}

func (constraint initialToolConstraint) repairMessage(attempt int) Message {
	instruction := "The previous provider response violated the required tool-use protocol. Return no prose and begin this bounded execution unit with one advertised tool call that directly repairs the durable failure. You must choose the tool and provide its real arguments; do not claim completion."
	if constraint.forbidden {
		instruction = "The previous provider response violated the closed tool-use protocol. Return the terminal response without any tool call. The outer runtime has frozen the current state for immutable validation, so another tool call cannot execute."
	} else if constraint.name != "" {
		instruction = "The previous provider response violated the required tool-use protocol. Return no prose and begin this bounded execution unit with exactly one call to the advertised " + constraint.name + " tool. Do not call another tool and do not claim completion."
	}
	return Message{
		// This is a private, in-memory protocol response to the rejected model
		// turn. A trailing user-role message is honored by OpenAI-compatible
		// coding gateways that accept tools but do not support native required
		// tool_choice. It never enters the canonical transcript.
		Role:    "user",
		Content: fmt.Sprintf("Tool protocol repair %d/%d. %s", attempt, maxInitialToolChoiceRepairAttempts, instruction),
	}
}

// namespaceReusedToolCallIDs keeps provider tool-call identities unique for
// the complete in-memory conversation. Some OpenAI-compatible providers reuse
// a short ID such as call_0 on every response; persisting that raw ID would
// make a later durable replay indistinguishable from a second result for the
// earlier call. IDs duplicated within one response are deliberately left
// unchanged so validateToolCallBatch still rejects that malformed batch.
func namespaceReusedToolCallIDs(messages []Message, calls []ToolCall, round int) []ToolCall {
	if len(calls) == 0 {
		return calls
	}
	used := make(map[string]struct{}, len(messages)+len(calls))
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			if id := strings.TrimSpace(call.ID); id != "" {
				used[id] = struct{}{}
			}
		}
	}
	seenThisRound := make(map[string]struct{}, len(calls))
	duplicateIDs := make(map[string]struct{})
	for _, call := range calls {
		id := strings.TrimSpace(call.ID)
		if id == "" || id != call.ID {
			continue
		}
		if _, duplicate := seenThisRound[id]; duplicate {
			duplicateIDs[id] = struct{}{}
			continue
		}
		seenThisRound[id] = struct{}{}
	}
	normalized := append([]ToolCall(nil), calls...)
	changed := false
	for index, call := range calls {
		id := strings.TrimSpace(call.ID)
		if id == "" || id != call.ID {
			continue
		}
		if _, duplicate := duplicateIDs[id]; duplicate {
			// Keep same-response duplicates intact so the existing validation
			// reports the provider protocol error before any tool runs.
			continue
		}
		if _, duplicate := used[id]; !duplicate {
			used[id] = struct{}{}
			continue
		}
		candidate := fmt.Sprintf("%s-synon-%d", id, round)
		for suffix := 2; ; suffix++ {
			if _, exists := used[candidate]; !exists {
				break
			}
			candidate = fmt.Sprintf("%s-synon-%d-%d", id, round, suffix)
		}
		normalized[index].ID = candidate
		used[candidate] = struct{}{}
		changed = true
	}
	if !changed {
		return calls
	}
	return normalized
}

// applyToolSchemaDefaults canonicalizes model-produced tool arguments before
// they are emitted, persisted, or executed. OpenAI-compatible providers often
// omit fields that are described with a JSON Schema default; relying on the
// model to echo a default makes durable tool execution provider-dependent.
// Only explicit top-level property defaults are applied here. Validation of
// required fields and unknown fields remains the responsibility of the tool
// boundary, so malformed model output is never silently broadened.
func applyToolSchemaDefaults(calls []ToolCall, schemas []ToolSchema) []ToolCall {
	if len(calls) == 0 || len(schemas) == 0 {
		return calls
	}
	schemaByName := make(map[string]ToolSchema, len(schemas))
	for _, schema := range schemas {
		name := strings.TrimSpace(schema.Name)
		if name != "" {
			schemaByName[name] = schema
		}
	}
	var normalized []ToolCall
	for index, call := range calls {
		schema, found := schemaByName[strings.TrimSpace(call.Name)]
		if !found {
			continue
		}
		properties, ok := schema.Parameters["properties"].(map[string]any)
		if !ok || len(properties) == 0 {
			continue
		}
		arguments := map[string]any{}
		if len(bytes.TrimSpace(call.Arguments)) > 0 {
			if err := json.Unmarshal(call.Arguments, &arguments); err != nil || arguments == nil {
				continue
			}
		}
		changed := false
		for propertyName, rawProperty := range properties {
			property, ok := rawProperty.(map[string]any)
			if !ok {
				continue
			}
			defaultValue, hasDefault := property["default"]
			if hasDefault {
				if _, present := arguments[propertyName]; !present {
					arguments[propertyName] = defaultValue
					changed = true
				}
			}
		}
		if !changed {
			continue
		}
		encoded, err := json.Marshal(arguments)
		if err != nil {
			continue
		}
		if normalized == nil {
			normalized = append([]ToolCall(nil), calls...)
		}
		normalized[index].Arguments = encoded
	}
	if normalized == nil {
		return calls
	}
	return normalized
}

// toolCallRoundSignature returns a stable semantic identity for one model
// tool-call round. Executable calls retain normalized execution arguments, but
// omit human_description because it is presentation metadata and cannot turn
// an otherwise identical action into semantic progress. Calls
// rejected before execution use the stable tool plus rejection family instead:
// rephrasing an operational AskUser question or another invalid call is not
// progress and must not evade the bounded loop guard.
func toolCallRoundSignature(calls []ToolCall, rejections map[int]toolCallRejection) string {
	if len(calls) == 0 {
		return ""
	}
	var builder strings.Builder
	for index, call := range calls {
		builder.WriteString(strings.TrimSpace(call.Name))
		builder.WriteByte(0)
		if rejection, rejected := rejections[index]; rejected {
			builder.WriteString("rejected:")
			builder.WriteString(strings.ToLower(strings.TrimSpace(rejection.Code)))
		} else {
			builder.Write(toolCallExecutionArguments(call.Arguments))
		}
		builder.WriteByte(1)
	}
	return builder.String()
}

func toolCallExecutionArguments(arguments json.RawMessage) []byte {
	trimmed := bytes.TrimSpace(arguments)
	if len(trimmed) == 0 {
		return trimmed
	}
	var input map[string]any
	if err := json.Unmarshal(trimmed, &input); err != nil || input == nil {
		return trimmed
	}
	delete(input, "human_description")
	encoded, err := json.Marshal(input)
	if err != nil {
		return trimmed
	}
	return encoded
}

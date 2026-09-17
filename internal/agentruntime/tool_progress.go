package agentruntime

import (
	"reflect"
	"sort"
	"strings"
)

const ToolEffectSchema = "synon.tool_effect.v1"

type ToolEffectState string

const (
	ToolEffectChanged   ToolEffectState = "changed"
	ToolEffectUnchanged ToolEffectState = "unchanged"
)

// ToolEffectValue describes what this invocation actually changed. It is
// separate from idempotency: an idempotent operation can still create new
// state on its first successful execution.
func ToolEffectValue(state ToolEffectState, categories ...string) map[string]any {
	normalized := make([]string, 0, len(categories))
	seen := make(map[string]struct{}, len(categories))
	for _, category := range categories {
		category = strings.ToLower(strings.TrimSpace(category))
		if category == "" {
			continue
		}
		if _, duplicate := seen[category]; duplicate {
			continue
		}
		seen[category] = struct{}{}
		normalized = append(normalized, category)
	}
	sort.Strings(normalized)
	value := map[string]any{"schema": ToolEffectSchema, "state": string(state)}
	if len(normalized) > 0 {
		value["categories"] = normalized
	}
	return value
}

func toolResultExplicitlyUnchanged(value any) bool {
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	effect, ok := object["effect"].(map[string]any)
	if !ok || strings.TrimSpace(stringValueAt(effect, "schema")) != ToolEffectSchema {
		return false
	}
	return strings.TrimSpace(stringValueAt(effect, "state")) == string(ToolEffectUnchanged)
}

func stringValueAt(value map[string]any, key string) string {
	text, _ := value[key].(string)
	return text
}

// toolResultExplicitlyNoMutation recognizes a successful mutator that reports
// authoritative state was unchanged. It deliberately requires an explicit
// marker; legacy tools without changed/unchanged metadata remain progress-
// capable so the retry guard cannot suppress a real mutation.
func toolResultExplicitlyNoMutation(value any) bool {
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	if changed, recorded := object["changed"].(bool); recorded && changed {
		return false
	}
	if unchanged, recorded := object["unchanged"].(bool); recorded && !unchanged {
		return false
	}
	if written, ok := object["files_written"].([]any); ok && len(written) > 0 {
		return false
	}
	if artifacts, ok := object["artifacts"].([]any); ok && len(artifacts) > 0 {
		for _, raw := range artifacts {
			artifact, ok := raw.(map[string]any)
			if !ok {
				return false
			}
			unchanged, recorded := artifact["unchanged"].(bool)
			if !recorded || !unchanged {
				return false
			}
		}
		return true
	}
	if changed, recorded := object["changed"].(bool); recorded {
		return !changed
	}
	if unchanged, recorded := object["unchanged"].(bool); recorded {
		return unchanged
	}
	if nested, ok := object["result"].(map[string]any); ok {
		return toolResultExplicitlyNoMutation(nested)
	}
	return false
}

// toolResultWorkspaceMutationReport separates workspace content effects from
// other valid runtime effects such as variables, stdout, jobs, or caches. A
// present empty files_written/artifacts collection authoritatively reports no
// workspace mutation, but does not classify the whole tool call as no-progress.
func toolResultWorkspaceMutationReport(value any) (reported, committed bool) {
	object, ok := value.(map[string]any)
	if !ok {
		return false, false
	}
	if changed, recorded := object["changed"].(bool); recorded {
		reported = true
		committed = committed || changed
	}
	if unchanged, recorded := object["unchanged"].(bool); recorded {
		reported = true
		committed = committed || !unchanged
	}
	if written, present := object["files_written"]; present {
		reported = true
		if length, collection := toolResultCollectionLength(written); !collection || length > 0 {
			committed = true
		}
	}
	if droppedRoots, present := object["dropped_roots"]; present {
		if length, collection := toolResultCollectionLength(droppedRoots); !collection || length > 0 {
			// An incomplete workspace scan cannot prove that no durable mutation
			// occurred. Preserve the conservative legacy epoch transition.
			reported = true
			committed = true
		}
	}
	if rawArtifacts, present := object["artifacts"]; present {
		reported = true
		artifacts, collection := rawArtifacts.([]any)
		if !collection {
			if length, recognized := toolResultCollectionLength(rawArtifacts); !recognized || length > 0 {
				committed = true
			}
		} else {
			for _, raw := range artifacts {
				artifact, object := raw.(map[string]any)
				if !object {
					committed = true
					break
				}
				unchanged, recorded := artifact["unchanged"].(bool)
				if !recorded || !unchanged {
					committed = true
					break
				}
			}
		}
	}
	if nested, ok := object["result"].(map[string]any); ok {
		nestedReported, nestedCommitted := toolResultWorkspaceMutationReport(nested)
		reported = reported || nestedReported
		committed = committed || nestedCommitted
	}
	return reported, committed
}

func toolResultCollectionLength(value any) (int, bool) {
	if value == nil {
		return 0, true
	}
	collection := reflect.ValueOf(value)
	if collection.Kind() != reflect.Array && collection.Kind() != reflect.Slice {
		return 0, false
	}
	return collection.Len(), true
}

func workspaceMutationTool(toolName string, capabilities []string) bool {
	if len(capabilities) > 0 {
		return toolCapabilitySetContainsAny(
			capabilities,
			"artifact-write",
			"artifact-publication",
			"runtime-execution",
			"software-provisioning",
			"state-mutation",
		)
	}
	return isWorkspaceMutationTool(toolName)
}

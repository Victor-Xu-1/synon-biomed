package registry

import "synon-go/internal/memorypolicy"

func memoryToolDefinitions() []Tool {
	return []Tool{
		{
			Name:         "read_memory",
			Description:  "Expand one memory entity to its full row list. `entity` is `profile`, `project:<pid>`, `artifact:<aid>`, `category:<name>` (as returned by `search_memory` / shown in a `[Memory]` recall block), or `frame` for this session's private scratchpad. Each row is prefixed with `[relative age]` (when it was written) and `[evidence]`, suffixed with `[mem_id · ⚠staleness?]`.",
			Capabilities: []string{"memory", "durable-state", "read", "trusted-session-required", "synon-contract"},
			Input: map[string]Field{
				"entity": {
					Type:     "string",
					Required: true,
					Schema: map[string]any{
						"description": "Entity key: 'profile', 'project:<pid>', 'artifact:<aid>', 'frame' (this session's private scratchpad), or 'category:<name>' (a user-defined category — pulls its rows across all projects). Bare 'project' resolves to the current project.",
					},
				},
			},
			Executable: true,
		},
		{
			Name:         "write_memory",
			Description:  "Write durable memory. `entity` defaults to the current project (`project:<pid>`); use `profile` for user-global facts, `artifact:<aid>` for file-specific, or `frame` for a private per-session scratchpad (notes to your future self — what you tried, dead ends, working state — that survive context compaction but are never visible to other sessions and are deleted with this conversation). Pass `append` to add new rows, `replace` (by `mem_id`) to correct existing rows, `remove` (by `mem_id`) to delete. Each row is a single fact with an `evidence` tag (`stated`/`observed`/`inferred`). Future sessions inherit non-`frame` rows — write only what should outlive this conversation.",
			Capabilities: []string{"memory", "durable-state", "write", "prompt-injection-gated", "trusted-session-required", "synon-contract"},
			Input: map[string]Field{
				"entity": {
					Type: "string",
					Schema: map[string]any{
						"description": "Where to file new facts: 'profile' (user-global), 'project:<pid>', 'artifact:<aid>', or 'frame' (private scratchpad for this session only — not visible to other sessions). Defaults to the current project. Only used for `append` — `replace`/`remove` address rows by mem_id.",
					},
				},
				"category": {
					Type: "string",
					Schema: map[string]any{
						"description": "Optional user-defined category name (from the '### Categories' list in the ## Memory section, if any). Applies to `append` rows. Set only when the fact clearly matches the category's guidance.",
					},
				},
				"append": {
					Type: "array",
					Schema: map[string]any{
						"description": "New facts to add under `entity`.",
						"maxItems":    memorypolicy.OperationsPerKindMax,
						"items":       memoryAppendItemSchema(),
					},
				},
				"replace": {
					Type: "array",
					Schema: map[string]any{
						"description": "Correct existing rows by mem_id (from a <memory_recall> block or read_memory/search_memory).",
						"maxItems":    memorypolicy.OperationsPerKindMax,
						"items":       memoryReplaceItemSchema(),
					},
				},
				"remove": {
					Type: "array",
					Schema: map[string]any{
						"description": "mem_ids to delete.",
						"maxItems":    memorypolicy.OperationsPerKindMax,
						"items":       map[string]any{"type": "string", "maxLength": memorypolicy.MemoryIDMaxLength},
					},
				},
			},
			Executable: true,
		},
		{
			Name:         "search_memory",
			Description:  "Search your persistent memory pool (all entities, all projects) by describing what you're looking for. Each matching row is prefixed with `[relative age]` (when it was written) and `[evidence]`, suffixed with `[mem_id · entity · ⚠staleness?]`. Use when `<memory_recall>` auto-surfacing missed something you suspect you've learned before. For structured queries or joins against artifacts/frames, use `host.query(\"SELECT * FROM memories WHERE …\")` instead.",
			Capabilities: []string{"memory", "durable-state", "search", "bm25", "trusted-session-required", "synon-contract"},
			Input: map[string]Field{
				"query": {
					Type:     "string",
					Required: true,
					Schema: map[string]any{
						"description": "Natural-language query over your memory pool (all projects). Use when auto-recall (<memory_recall> blocks) missed something you suspect exists.",
					},
				},
			},
			Executable: true,
		},
	}
}

func memoryAppendItemSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"text": map[string]any{
				"type":      "string",
				"maxLength": memorypolicy.TextMaxUTF16Units,
			},
			"evidence": memoryEvidenceSchema(true),
		},
		"required": []string{"text"},
	}
}

func memoryReplaceItemSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"id": map[string]any{"type": "string", "maxLength": memorypolicy.MemoryIDMaxLength},
			"text": map[string]any{
				"type":      "string",
				"maxLength": memorypolicy.TextMaxUTF16Units,
			},
			"evidence": memoryEvidenceSchema(false),
		},
		"required": []string{"id", "text"},
	}
}

func memoryEvidenceSchema(withDescription bool) map[string]any {
	schema := map[string]any{
		"type": "string",
		"enum": []string{"stated", "observed", "inferred"},
	}
	if withDescription {
		schema["description"] = "'stated' (user told you), 'observed' (seen in a tool result/artifact), 'inferred' (your conclusion). Defaults to 'observed'."
	}
	return schema
}

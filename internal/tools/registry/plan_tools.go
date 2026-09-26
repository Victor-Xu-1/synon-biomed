package registry

func planToolDefinitions() []Tool {
	return []Tool{
		{
			Name:        "generate_plan",
			Description: "Create or revise one durable working plan using the complete current plan content. Phases and steps are ordered control state. Use execution steps with an execution_tool for actual computation or tool-produced results; completion requires a successful current-task execution receipt. Research steps name an output_module and research_question. Synthesis combines findings and delivery creates outputs. The working plan pauses only in explicit plan-review mode.",
			Capabilities: []string{
				"plan", "approval", "durable-state", "artifact-version", "single-plan-authority", "synon-plan-v3",
			},
			Input: map[string]Field{
				"human_description": {Type: "string", Required: false},
				"task_summary":      {Type: "string", Required: false},
				"phases": {Type: "array", Required: false, Schema: map[string]any{
					"minItems": 1, "maxItems": 16,
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name": map[string]any{"type": "string"},
							"delegations": map[string]any{
								"type": "array", "minItems": 1, "maxItems": 64,
								"items": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"name": map[string]any{"type": "string"},
										"steps": map[string]any{
											"type": "array", "minItems": 1, "maxItems": 256,
											"items": map[string]any{
												"type": "object",
												"properties": map[string]any{
													"title":       map[string]any{"type": "string"},
													"description": map[string]any{"type": "string"},
													"kind": map[string]any{
														"type": "string", "enum": []string{"work", "execution", "research", "synthesis", "delivery"},
													},
													"execution_tool": map[string]any{"type": "string"},
													"output_module": map[string]any{
														"type": "string",
													},
													"research_question": map[string]any{
														"type": "string",
													},
													"research_depth": map[string]any{
														"type": "string", "enum": []string{"focused", "deep", "systematic"},
													},
													"discovery_queries": map[string]any{
														"type": "array", "maxItems": 6,
														"items": map[string]any{
															"type": "object",
															"properties": map[string]any{
																"language": map[string]any{"type": "string", "enum": []string{"zh", "en"}},
																"query":    map[string]any{"type": "string"},
															},
															"required":             []string{"language", "query"},
															"additionalProperties": false,
														},
													},
												},
												"required":             []string{"title", "description", "kind"},
												"additionalProperties": false,
											},
										},
									},
									"required":             []string{"name", "steps"},
									"additionalProperties": false,
								},
							},
						},
						"required":             []string{"name", "delegations"},
						"additionalProperties": false,
					},
				}},
				"desired_outputs": {Type: "array", Required: false, Schema: map[string]any{
					"maxItems": 256, "items": map[string]any{"type": "string"},
				}},
				"feasibility": {Type: "object", Required: false, Schema: map[string]any{
					"properties": map[string]any{
						"confidence": map[string]any{"type": "string", "enum": []string{"high", "medium", "low"}},
						"rationale":  map[string]any{"type": "string"},
					},
					"required":             []string{"confidence", "rationale"},
					"additionalProperties": false,
				}},
				"approve": {Type: "boolean", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "update_step_status",
			Description:  "Update a currently actionable step in the durable plan. Execution completion binds a successful current-task receipt: a unique matching receipt is resolved automatically, or execution_ref selects its exact tool-call ID. Ambiguous and legacy tool bindings return verified candidates; reuse completed work instead of rerunning it. A receipt cannot complete another execution step. Research receipts and source follow-ups remain visible. Omitted navigation fields preserve prior values; empty values clear model-authored data.",
			Capabilities: []string{"plan", "progress", "research-navigation", "durable-state", "single-plan-authority"},
			Input: map[string]Field{
				"human_description": {Type: "string", Required: false},
				"step":              {Type: "string", Required: true},
				"status": {Type: "string", Required: true, Schema: map[string]any{
					"enum": []string{"in_progress", "completed", "blocked", "skipped"},
				}},
				"notes":         {Type: "string", Required: false},
				"execution_ref": {Type: "string", Required: false},
				"observations": {Type: "array", Required: false, Schema: map[string]any{
					"maxItems": 256, "items": map[string]any{"type": "string"},
				}},
				"source_refs": {Type: "array", Required: false, Schema: map[string]any{
					"maxItems": 256, "items": map[string]any{"type": "string"},
				}},
				"follow_ups": {Type: "array", Required: false, Schema: map[string]any{
					"maxItems": 256, "items": map[string]any{"type": "string"},
				}},
			},
			Executable: true,
		},
	}
}

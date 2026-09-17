package server

import (
	"context"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestAskUserCanonicalInputNormalizesToOneDurableQuestion(t *testing.T) {
	server := New(Options{FileRoot: t.TempDir()})
	result, err := server.executeAskUserQuestionTool("ask_user", map[string]any{
		"question": "Which acceptance result should be primary?",
		"header":   "Acceptance",
		"options": []any{
			askUserDecisionOptionWithResources(askUserDecisionOption(
				"Browser", "Verify the real browser path.", "User-visible",
				"Requires an interactive browser session.", "Ready from the deployed runtime.",
				[]any{"tool-call:browser-smoke"}, "Browser access and the deployed frontend.",
				"A rendered-path acceptance result.", "Recommended because it proves the user-visible path.", true,
			), "4 cores", "8 GB", "Not required"),
			askUserDecisionOption(
				"API", "Verify the deployed API path.", "Fast and deterministic",
				"Does not prove the rendered path.", "Ready from the deployed runtime.",
				[]any{"tool-call:api-smoke"}, "Loopback access to the deployed API.",
				"An API acceptance result.", "Choose when only the service contract is in scope.", false,
			),
		},
		"multi_select": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	questions, ok := mapValue(result)["questions"].([]askUserQuestion)
	if !ok || len(questions) != 1 || questions[0].Question != "Which acceptance result should be primary?" {
		t.Fatalf("ask_user result=%#v", result)
	}
	if questions[0].Options[0].Label != "Browser" ||
		questions[0].Options[0].Pros != "User-visible" ||
		questions[0].Options[0].Metadata["recommended"] != true {
		t.Fatalf("ask_user option metadata was not preserved: %#v", questions[0].Options)
	}
	for _, exact := range []string{
		"Route: Verify the real browser path.",
		"Resources to verify: CPU: 4 cores · Memory: 8 GB · GPU: Not required",
	} {
		if !strings.Contains(questions[0].Options[0].Description, exact) {
			t.Errorf("English decision description missing %q: %q", exact, questions[0].Options[0].Description)
		}
	}
	for _, internal := range []string{"tool-call:", "Selection basis:", "Task and scientific evidence:", "Execution-readiness evidence:"} {
		if strings.Contains(questions[0].Options[0].Description, internal) || strings.Contains(questions[0].Options[1].Description, internal) {
			t.Errorf("English decision card exposed internal audit detail %q: %#v", internal, questions[0].Options)
		}
	}
	if strings.Contains(questions[0].Options[0].Description, "Recommended:") || strings.Contains(questions[0].Options[1].Description, "Recommended:") {
		t.Errorf("English option description duplicates the recommendation badge: %#v", questions[0].Options)
	}
}

func TestAskUserNormalizesTypedControlledExecutionParameters(t *testing.T) {
	server := New(Options{FileRoot: t.TempDir()})
	primary := askUserDecisionOption(
		"Measured center", "Use center x=1.0 y=2.0 z=3.0.", "Uses measured coordinates",
		"Requires the stated site", "Execution readiness is not established.",
		[]any{askUserCurrentTaskEvidenceReference}, "No readiness authority.",
		"A site-bounded result.", "Use when these values came from the user.", true,
	)
	primary["execution_parameter_values"] = []any{map[string]any{
		"evidence_group": "binding-site-center", "values": []any{1.0, 2.0, 3.0},
	}}
	primary["metadata"] = map[string]any{
		"execution_parameter_values": []any{map[string]any{"evidence_group": "forged", "values": []any{99.0}}},
	}
	result, err := server.executeAskUserQuestionTool("ask_user", map[string]any{
		"question": "Which binding-site input should be used?", "header": "Binding site",
		"options": []any{
			primary,
			askUserDecisionOption(
				"Provide site", "Ask for an authoritative binding-site input.", "Avoids inferred values",
				"Requires user input", "No execution is attempted yet.",
				[]any{askUserCurrentTaskEvidenceReference}, "No readiness authority.",
				"A user-owned binding-site input.", "Choose when no measured center was supplied.", false,
			),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	questions := mapValue(result)["questions"].([]askUserQuestion)
	values, ok := questions[0].Options[0].Metadata["execution_parameter_values"].([]askUserExecutionParameterValues)
	if !ok || len(values) != 1 || values[0].EvidenceGroup != "binding-site-center" ||
		len(values[0].Values) != 3 || values[0].Values[2] != 3.0 {
		t.Fatalf("typed execution parameter values=%#v", questions[0].Options[0].Metadata["execution_parameter_values"])
	}
}

func TestAskUserPreservesChineseScientificTradeoffsWithoutChangingTheContract(t *testing.T) {
	server := New(Options{FileRoot: t.TempDir()})
	result, err := server.executeAskUserQuestionTool("ask_user", map[string]any{
		"question": "哪一种可执行的分子生成路线更符合本次研究目标？",
		"header":   "生成路线",
		"options": []any{
			askUserDecisionOptionWithImplementationAndResources(askUserDecisionOption(
				"口袋三维生成", "直接使用已核验的结合口袋生成可编辑三维候选。",
				"几何条件与当前结构假设最一致", "计算和模型准备要求较高",
				"可配置：已确认远程计算路线，本机路线仍需 GPU 预检。", []any{"tool-call:remote-preflight", "tool-call:structure-search"},
				"需要兼容的 GPU 或已认证远程服务；专有结构会离开本机。",
				"可编辑三维候选、生成来源和后续验证记录。",
				"推荐依据：本题明确要求口袋条件生成和新骨架。", true,
			), "Pocket2Mol", "8 核", "16 GB", "CUDA，显存 8 GB"),
			askUserDecisionOption(
				"配体骨架扩展", "围绕参考配体扩展化学空间并交付可编辑候选。",
				"更容易保留已知结合假设", "新骨架覆盖范围较窄",
				"已就绪：本机轻量路线可用。", []any{"tool-call:local-preflight", "tool-call:structure-search"},
				"需要常规 CPU 和本地存储；不需要外部数据传输。",
				"可编辑类似物、结构多样性表和验证记录。",
				"适合优先保留已知配体骨架的项目。", false,
			),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	questions, ok := mapValue(result)["questions"].([]askUserQuestion)
	if !ok || len(questions) != 1 || questions[0].Header != "生成路线" {
		t.Fatalf("Chinese ask_user result=%#v", result)
	}
	if questions[0].Options[0].Label != "口袋三维生成 · Pocket2Mol" ||
		questions[0].Options[0].Pros != "几何条件与当前结构假设最一致" ||
		questions[0].Options[1].Cons != "新骨架覆盖范围较窄" {
		t.Fatalf("Chinese ask_user tradeoffs changed: %#v", questions[0].Options)
	}
	for _, exact := range []string{
		"方案：直接使用已核验的结合口袋生成可编辑三维候选。",
		"资源待核验：CPU：8 核 · 内存：16 GB · GPU：CUDA，显存 8 GB",
	} {
		if !strings.Contains(questions[0].Options[0].Description, exact) {
			t.Errorf("Chinese decision description missing %q: %q", exact, questions[0].Options[0].Description)
		}
	}
	for _, internal := range []string{"tool-call:", "选择依据类型：", "任务与科学依据：", "运行就绪依据："} {
		if strings.Contains(questions[0].Options[0].Description, internal) || strings.Contains(questions[0].Options[1].Description, internal) {
			t.Errorf("Chinese decision card exposed internal audit detail %q: %#v", internal, questions[0].Options)
		}
	}
	if strings.Contains(questions[0].Options[0].Description, "推荐：") || strings.Contains(questions[0].Options[1].Description, "推荐：") || strings.Contains(questions[0].Options[0].Description, "产出：") {
		t.Errorf("Chinese option description duplicates the recommendation badge: %#v", questions[0].Options)
	}
}

func TestAskUserPublicOptionLabelKeepsMachineImplementationKeyPrivate(t *testing.T) {
	if got := askUserPublicOptionLabel("Pocket2Mol 本地 GPU", "pocket2mol-local"); got != "Pocket2Mol 本地 GPU" {
		t.Fatalf("machine implementation key leaked into public label: %q", got)
	}
	if got := askUserPublicOptionLabel("口袋三维生成", "Pocket2Mol"); got != "口袋三维生成 · Pocket2Mol" {
		t.Fatalf("public product name was not preserved: %q", got)
	}
}

func TestAskUserResourceProfileRejectsAnyFourthPublicDimension(t *testing.T) {
	server := New(Options{FileRoot: t.TempDir()})
	primary := askUserDecisionOptionWithResources(askUserDecisionOption(
		"Local engine", "Run the pocket-conditioned engine.", "Uses pocket geometry", "Needs local setup",
		"Not checked.", []any{askUserCurrentTaskEvidenceReference}, "legacy details",
		"Editable 3D candidates.", "Matches the requested conditioning.", true,
	), "8 cores", "16 GB", "CUDA with 8 GB VRAM")
	primary["resources"].(map[string]any)["disk"] = "20 GB"
	_, err := server.executeAskUserQuestionTool("ask_user", map[string]any{
		"question": "Which local engine should run?", "header": "Engine",
		"options": []any{
			primary,
			askUserDecisionOptionWithResources(askUserDecisionOption(
				"Hosted engine", "Run a hosted pocket-conditioned engine.", "Avoids local setup", "Transfers inputs",
				"Not checked.", []any{askUserCurrentTaskEvidenceReference}, "legacy details",
				"Editable 3D candidates.", "Choose when remote processing is acceptable.", false,
			), "2 cores", "4 GB", "Remote"),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "resources.disk") {
		t.Fatalf("ask_user accepted a fourth public resource dimension: %v", err)
	}
}

func TestAskUserComputeChoiceRequiresOneConcreteImplementation(t *testing.T) {
	server := New(Options{FileRoot: t.TempDir()})
	primary := askUserDecisionOption(
		"Pocket-native generation", "Generate molecules from pocket geometry.", "Creates new scaffolds", "Needs setup",
		"Not checked.", []any{askUserCurrentTaskEvidenceReference}, "legacy details",
		"Editable 3D candidates.", "Matches the requested conditioning.", true,
	)
	primary["resources"] = map[string]any{"cpu": "8 cores", "memory": "16 GB", "gpu": "8 GB VRAM"}
	_, err := server.executeAskUserQuestionTool("ask_user", map[string]any{
		"question": "Which engine should run?", "header": "Engine",
		"options": []any{
			primary,
			askUserDecisionOptionWithImplementationAndResources(askUserDecisionOption(
				"Hosted generation", "Generate molecules using the hosted service.", "Avoids local setup", "Transfers inputs",
				"Not checked.", []any{askUserCurrentTaskEvidenceReference}, "legacy details",
				"Editable 3D candidates.", "Choose when remote processing is acceptable.", false,
			), "Example Hosted Generator", "2 cores", "4 GB", "Remote"),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "implementation is required") {
		t.Fatalf("ask_user accepted a compute method category without an implementation: %v", err)
	}
}

func TestAskUserUnverifiedReadinessUsesAuthoritativeUserFacingText(t *testing.T) {
	server := New(Options{FileRoot: t.TempDir()})
	result, err := server.executeAskUserQuestionTool("ask_user", map[string]any{
		"question": "Which route should be considered first?", "header": "Route",
		"options": []any{
			askUserDecisionOption(
				"Primary", "Use the primary scientific route.", "Matches the stated objective.", "Needs a live preflight.",
				"The GPU and every package are already ready.", []any{askUserCurrentTaskEvidenceReference}, "A live preflight is required.",
				"A candidate result if executable.", "Recommended from the stated objective only.", true,
			),
			askUserDecisionOption(
				"Alternate", "Use the alternate scientific route.", "Broadens the comparison.", "Needs a live preflight.",
				"Every remote service is available.", []any{askUserCurrentTaskEvidenceReference}, "A live preflight is required.",
				"An alternate result if executable.", "Choose for a broader comparison.", false,
			),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	questions := mapValue(result)["questions"].([]askUserQuestion)
	description := questions[0].Options[0].Description
	if strings.Contains(description, "already ready") ||
		description != "Route: Use the primary scientific route." ||
		questions[0].Options[0].Metadata["readiness"] != "The required environment or service has not been preflighted yet." {
		t.Fatalf("unverified readiness projection=%q", description)
	}
	if questions[0].Options[0].Metadata["reported_readiness"] != "The GPU and every package are already ready." {
		t.Fatalf("raw reported readiness was not retained for audit: %#v", questions[0].Options[0].Metadata)
	}
}

func TestAskUserRejectsShallowOrUnsupportedDecisionOptions(t *testing.T) {
	server := New(Options{FileRoot: t.TempDir()})
	for _, test := range []struct {
		name    string
		options []any
		field   string
	}{
		{
			name: "missing decision evidence",
			options: []any{
				map[string]any{"label": "Local", "description": "Run locally.", "pros": "Private", "cons": "Needs GPU"},
				map[string]any{"label": "Remote", "description": "Run remotely.", "pros": "Capacity", "cons": "External"},
			},
			field: "readiness",
		},
		{
			name: "empty decision evidence",
			options: []any{
				askUserDecisionOption("Local", "Run locally.", "Private", "Needs GPU", "Ready.", nil, "GPU.", "Local outputs.", "Preferred for privacy.", true),
				askUserDecisionOption("Remote", "Run remotely.", "Capacity", "External", "Ready.", []any{"provider:p1"}, "Credential.", "Remote outputs.", "Alternative for capacity.", false),
			},
			field: "decision_evidence",
		},
		{
			name: "multiple recommendations",
			options: []any{
				askUserDecisionOption("Local", "Run locally.", "Private", "Needs GPU", "Ready.", []any{"machine:m1"}, "GPU.", "Local outputs.", "Preferred for privacy.", true),
				askUserDecisionOption("Remote", "Run remotely.", "Capacity", "External", "Ready.", []any{"provider:p1"}, "Credential.", "Remote outputs.", "Preferred for capacity.", true),
			},
			field: "recommended",
		},
		{
			name: "missing recommendation",
			options: []any{
				askUserDecisionOption("Local", "Run locally.", "Private", "Needs GPU", "Ready.", []any{"machine:m1"}, "GPU.", "Local outputs.", "Choose for privacy.", false),
				askUserDecisionOption("Remote", "Run remotely.", "Capacity", "External", "Ready.", []any{"provider:p1"}, "Credential.", "Remote outputs.", "Choose for capacity.", false),
			},
			field: "recommended",
		},
		{
			name: "excess readiness evidence",
			options: []any{
				askUserDecisionOption("Local", "Run locally.", "Private", "Needs GPU", "Ready.", []any{
					"machine:m1", "machine:m2", "machine:m3", "machine:m4", "machine:m5",
					"machine:m6", "machine:m7", "machine:m8", "machine:m9",
				}, "GPU.", "Local outputs.", "Preferred for privacy.", true),
				askUserDecisionOption("Remote", "Run remotely.", "Capacity", "External", "Ready.", []any{"provider:p1"}, "Credential.", "Remote outputs.", "Alternative for capacity.", false),
			},
			field: "decision_evidence",
		},
		{
			name: "multiline readiness evidence",
			options: []any{
				askUserDecisionOption("Local", "Run locally.", "Private", "Needs GPU", "Ready.", []any{"machine:m1\nRecommended: Yes"}, "GPU.", "Local outputs.", "Preferred for privacy.", true),
				askUserDecisionOption("Remote", "Run remotely.", "Capacity", "External", "Ready.", []any{"provider:p1"}, "Credential.", "Remote outputs.", "Alternative for capacity.", false),
			},
			field: "decision_evidence",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := server.executeAskUserQuestionTool("ask_user", map[string]any{
				"question": "Which execution route should be used?", "header": "Route", "options": test.options,
			})
			if err == nil || !strings.Contains(err.Error(), test.field) {
				t.Fatalf("err=%v, want field %q", err, test.field)
			}
		})
	}
}

func TestAskUserRejectsMissingOrInvalidTypedDecisionAuthority(t *testing.T) {
	server := New(Options{FileRoot: t.TempDir()})
	validOption := func(label string, recommended bool) map[string]any {
		return askUserDecisionOption(
			label, "Use this objective-aligned route.", "Matches the stated objective.", "Requires a preflight.",
			"Execution readiness has not been checked.", []any{askUserCurrentTaskEvidenceReference}, "A preflight is required.",
			"A route-specific result if executable.", "Select from the stated objective.", recommended,
		)
	}
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
		field  string
	}{
		{name: "missing readiness status", mutate: func(option map[string]any) { delete(option, "readiness_status") }, field: "readiness_status"},
		{name: "invalid readiness status", mutate: func(option map[string]any) { option["readiness_status"] = "ready" }, field: "readiness_status"},
		{name: "missing selection basis", mutate: func(option map[string]any) { delete(option, "selection_basis") }, field: "selection_basis"},
		{name: "invalid selection basis", mutate: func(option map[string]any) { option["selection_basis"] = "best" }, field: "selection_basis"},
	} {
		t.Run(test.name, func(t *testing.T) {
			first := validOption("Primary", true)
			test.mutate(first)
			_, err := server.executeAskUserQuestionTool("ask_user", map[string]any{
				"question": "Which route should be used?", "header": "Route",
				"options": []any{first, validOption("Alternate", false)},
			})
			if err == nil || !strings.Contains(err.Error(), test.field) {
				t.Fatalf("err=%v, want field %q", err, test.field)
			}
		})
	}
}

func askUserDecisionOption(
	label, description, pros, cons, readiness string,
	readinessEvidence []any,
	_, expectedOutcome, selectionRationale string,
	recommended bool,
) map[string]any {
	readinessStatus := "unverified"
	selectionBasis := "user_objective"
	decisionEvidence := append([]any(nil), readinessEvidence...)
	operationalEvidence := []any{}
	hasScientificReceipt := false
	hasReadinessAuthority := false
	for _, raw := range readinessEvidence {
		reference, _ := raw.(string)
		switch {
		case strings.HasPrefix(reference, "tool-call:"):
			hasScientificReceipt = true
		case strings.HasPrefix(reference, "compute-provider:"):
			readinessStatus = "configured"
			hasReadinessAuthority = true
			operationalEvidence = append(operationalEvidence, reference)
		case strings.HasPrefix(reference, "readiness-attestation:"):
			readinessStatus = "verified_ready"
			hasReadinessAuthority = true
			operationalEvidence = append(operationalEvidence, reference)
		}
	}
	switch {
	case hasScientificReceipt && hasReadinessAuthority:
		selectionBasis = "balanced_tradeoff"
	case hasScientificReceipt:
		selectionBasis = "scientific_evidence"
	case hasReadinessAuthority:
		selectionBasis = "execution_readiness"
	}
	return map[string]any{
		"label": label, "description": description, "pros": pros, "cons": cons,
		"readiness": readiness, "readiness_status": readinessStatus,
		"decision_evidence": decisionEvidence, "readiness_evidence": operationalEvidence,
		"selection_basis":     selectionBasis,
		"expected_outcome":    expectedOutcome,
		"selection_rationale": selectionRationale, "recommended": recommended,
	}
}

func askUserDecisionOptionWithResources(option map[string]any, cpu, memory, gpu string) map[string]any {
	return askUserDecisionOptionWithImplementationAndResources(option, stringValue(option["label"]), cpu, memory, gpu)
}

func askUserDecisionOptionWithImplementationAndResources(option map[string]any, implementation, cpu, memory, gpu string) map[string]any {
	option["implementation"] = implementation
	option["resources"] = map[string]any{"cpu": cpu, "memory": memory, "gpu": gpu}
	return option
}

func TestNormalizeGeneratedPlanAcceptsCanonicalNestedPlanAndRejectsFlatInput(t *testing.T) {
	canonical := map[string]any{
		"task_summary": "Verify the scientific runtime",
		"phases": []any{map[string]any{
			"name": "Verification",
			"delegations": []any{map[string]any{
				"name":  "Runtime",
				"steps": []any{map[string]any{"title": "Exercise the real path", "description": "Run the unchanged task and preserve raw evidence."}},
			}},
		}},
		"desired_outputs": []any{"runtime audit report"},
		"feasibility":     map[string]any{"confidence": "high", "rationale": "The isolated runtime is available."},
	}
	document, normalized, _, err := normalizeGeneratedPlan(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if len(document.DesiredOutputs) != 1 || len(document.Phases) != 1 || normalized["version"] != float64(generatePlanSchemaVersion) {
		t.Fatalf("normalized plan=%#v document=%#v", normalized, document)
	}
	if _, _, _, err := normalizeGeneratedPlan(map[string]any{
		"task_summary": "Retired flat plan",
		"steps":        []any{map[string]any{"title": "Old", "description": "Old path"}},
		"feasibility":  map[string]any{"confidence": "high", "rationale": "Legacy"},
	}); err == nil {
		t.Fatal("retired flat generate_plan input was accepted")
	}
}

func TestManagedEnvironmentSchemasExposeEveryCanonicalMode(t *testing.T) {
	schemas := agentEnvironmentManagementToolSchemas()
	environments := compileAgentRuntimeMCPValidator(schemas[0])
	packages := compileAgentRuntimeMCPValidator(schemas[1])
	for _, input := range []map[string]any{
		{"mode": "delete", "name": "obsolete", "human_description": "Removing obsolete environment"},
		{"mode": "register", "name": "project-dev", "source_path": "/srv/project", "venv_path": "/srv/project/.venv", "human_description": "Registering project environment"},
	} {
		if failure := environments.Validate(input); failure != nil {
			t.Fatalf("manage_environments rejected %#v: %#v", input, failure)
		}
	}
	for _, input := range []map[string]any{
		{"mode": "list", "environment": "project-dev", "human_description": "Listing project packages"},
		{"mode": "uninstall", "environment": "project-dev", "packages": []any{"old-package"}, "human_description": "Removing obsolete package"},
		{"mode": "preflight", "environment": "project-dev", "packages": []any{"new-package"}, "resource_requirements": managedEnvironmentTestResources(), "human_description": "Checking project environment"},
		{"mode": "install", "environment": "project-dev", "packages": []any{"new-package"}, "fork_to": "project-next", "pip_args": []any{"--no-deps"}, "use_pip": true,
			"human_description": "Forking project environment"},
	} {
		if failure := packages.Validate(input); failure != nil {
			t.Fatalf("manage_packages rejected %#v: %#v", input, failure)
		}
	}
}

func TestSaveArtifactsSchemaAcceptsExplicitStorageDestination(t *testing.T) {
	validator := compileAgentRuntimeMCPValidator(agentSaveArtifactsToolSchema())
	if failure := validator.Validate(map[string]any{
		"files": []any{"large.parquet"}, "language": "python", "human_description": "Saving working dataset",
		"destination": map[string]any{"large.parquet": "working_data"},
	}); failure != nil {
		t.Fatalf("save_artifacts destination was rejected: %#v", failure)
	}
}

func TestReplHostPolicyIncludesRemoteComputeAsOneDistinctAuthority(t *testing.T) {
	server := &Server{}
	policy := server.agentKernelHostCallPolicy(context.Background(), workspace.KernelFrameAccess{}, "", []string{"repl"}, false)
	if policy == nil {
		t.Fatal("host policy is nil")
	}
	want := map[string]bool{
		"host.compute.create":                false,
		"host.compute.ledger":                false,
		"host.compute.status":                false,
		"host.compute.set_concurrency_limit": false,
	}
	for _, method := range policy.AllowedMethods {
		if _, found := want[method]; found {
			want[method] = true
		}
	}
	for method, found := range want {
		if !found {
			t.Fatalf("remote compute host method %q is missing from %v", method, policy.AllowedMethods)
		}
	}
}

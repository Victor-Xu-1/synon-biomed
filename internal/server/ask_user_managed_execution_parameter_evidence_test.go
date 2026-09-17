package server

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	"synon-go/internal/sciencecapability"
	"synon-go/internal/skills"
)

func TestAskUserRejectsModelAuthoredControlledParameterTuple(t *testing.T) {
	skillCatalog := skills.NewCatalog()
	skillCatalog.AddSkill(skills.Skill{Name: "managed-engine", ImplementationIdentities: []string{"Managed Engine"}})
	capabilityCatalog := &sciencecapability.Catalog{Capabilities: []sciencecapability.Definition{{
		ID: "managed-analysis",
		AcceptedEngines: []sciencecapability.EngineDefinition{{
			ID: "engine",
			ExecutionPack: sciencecapability.ExecutionPack{
				Mode: "local", Skill: "managed-engine",
				Parameters: []sciencecapability.ExecutionParameter{
					{Name: "center_x", Argument: "--center-x", Type: "number", Evidence: "resolved-user-input", EvidenceGroup: "binding-site-center", EvidenceTerms: []string{"binding-site center", "binding pocket", "center coordinates", "结合口袋", "中心"}},
					{Name: "center_y", Argument: "--center-y", Type: "number", Evidence: "resolved-user-input", EvidenceGroup: "binding-site-center"},
					{Name: "center_z", Argument: "--center-z", Type: "number", Evidence: "resolved-user-input", EvidenceGroup: "binding-site-center"},
				},
				EvidenceResolvers: []sciencecapability.ExecutionEvidenceResolver{{
					EvidenceGroup: "binding-site-center", Skill: "pocket-resolver", Implementation: "P2Rank",
				}},
			},
		}},
	}}}
	run := &sessionRunnerChatRun{TaskIntent: "Analyze the attached inputs", SelectedImplementations: []string{"Managed Engine"}}
	for name, route := range map[string]string{
		"parenthesized": "Use the model-estimated binding-site center (25.4, 18.2, 30.6).",
		"labelled":      "Use center x=25.4 y=18.2 z=30.6.",
		"slash":         "Use center 25.4 / 18.2 / 30.6.",
	} {
		t.Run(name, func(t *testing.T) {
			result := map[string]any{"questions": []askUserQuestion{{Options: []askUserQuestionOption{{
				Label: "Estimated point", Metadata: map[string]any{
					"route_description": route,
					"execution_parameter_values": []askUserExecutionParameterValues{{
						EvidenceGroup: "binding-site-center", Values: []float64{25.4, 18.2, 30.6},
					}},
				},
			}}}}}
			correction := askUserManagedExecutionParameterEvidenceCorrection(skillCatalog, capabilityCatalog, run, result)
			if stringValue(correction["status"]) != "ask_user_parameter_evidence_required" ||
				stringValue(correction["evidence_group"]) != "binding-site-center" {
				t.Fatalf("model-authored tuple correction=%#v", correction)
			}
		})
	}
	omittedDeclaration := map[string]any{"questions": []askUserQuestion{{Options: []askUserQuestionOption{{
		Label: "Estimated point", Metadata: map[string]any{
			"route_description": "Use center x=25.4 y=18.2 z=30.6.",
		},
	}}}}}
	correction := askUserManagedExecutionParameterEvidenceCorrection(skillCatalog, capabilityCatalog, run, omittedDeclaration)
	if stringValue(correction["status"]) != "ask_user_parameter_contract_invalid" ||
		stringValue(correction["evidence_group"]) != "binding-site-center" {
		t.Fatalf("omitted controlled tuple declaration correction=%#v", correction)
	}
	visibleLabelOnly := map[string]any{"questions": []askUserQuestion{{Options: []askUserQuestionOption{{
		Label:       "使用默认参数（中心：(44.0, 31.0, 31.0)，大小：20×20×20 Å）",
		Description: "使用基于受体结构质心计算得到的盒子中心。",
		Metadata: map[string]any{
			"route_description": "使用基于受体结构质心计算得到的盒子中心。",
		},
	}}}}}
	correction = askUserManagedExecutionParameterEvidenceCorrection(skillCatalog, capabilityCatalog, run, visibleLabelOnly)
	if stringValue(correction["status"]) != "ask_user_parameter_contract_invalid" ||
		stringValue(correction["evidence_group"]) != "binding-site-center" {
		t.Fatalf("visible model-authored center escaped the controlled parameter contract: %#v", correction)
	}
	option := func(label, description string, recommended bool) map[string]any {
		return map[string]any{
			"label": label, "description": description, "pros": "Continue with verified input.",
			"cons": "Additional input may be required.", "readiness": "Waiting for user input.",
			"readiness_status": "not_applicable", "decision_evidence": []any{"user-input:current-task"},
			"readiness_evidence": []any{}, "selection_basis": "user_objective",
			"expected_outcome": "Continue the analysis.", "selection_rationale": "Use verified input.",
			"recommended": recommended,
		}
	}
	rawArguments, err := json.Marshal(map[string]any{
		"question": "Should the estimated center be used?", "header": "Binding site",
		"options": []any{
			option("Use default center (44.0, 31.0, 31.0)", "Use the model-computed binding-site center.", true),
			option("Provide values", "I will provide an authoritative binding-site center.", false),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{skillCatalog: skillCatalog, scienceCapabilities: capabilityCatalog}
	sanitized := srv.sanitizeManagedAskUserToolCallForCheckpoint(run, agentruntime.ToolCall{
		ID: "ask-safe", Name: "ask_user", Arguments: rawArguments,
	})
	var sanitizedInput map[string]any
	if err := json.Unmarshal(sanitized.Arguments, &sanitizedInput); err != nil {
		t.Fatal(err)
	}
	encodedSanitized := string(sanitized.Arguments)
	sanitizedOptions, _ := sanitizedInput["options"].([]any)
	recommended := 0
	for _, raw := range sanitizedOptions {
		if option, _ := raw.(map[string]any); option["recommended"] == true {
			recommended++
		}
	}
	if len(sanitizedOptions) != 4 || recommended != 1 || strings.Contains(encodedSanitized, "44.0") ||
		!strings.Contains(encodedSanitized, "P2Rank") || !strings.Contains(encodedSanitized, "Provide values") ||
		!strings.Contains(encodedSanitized, "authoritative input") || !strings.Contains(encodedSanitized, "Stop with explanation") {
		t.Fatalf("pre-checkpoint AskUser sanitization=%s", encodedSanitized)
	}
	normalizedInput, err := normalizeAskUserToolInput(sanitizedInput)
	if err != nil {
		t.Fatal(err)
	}
	normalizedQuestions, err := askUserQuestionValue(normalizedInput)
	if err != nil {
		t.Fatal(err)
	}
	if correction := askUserManagedExecutionParameterEvidenceCorrection(
		skillCatalog, capabilityCatalog, run, map[string]any{"questions": normalizedQuestions},
	); correction != nil {
		t.Fatalf("sanitized AskUser remained unsafe: %#v", correction)
	}
	unsafeNarration := "如果你不知道准确参数，我可以使用受体质心作为默认中心（44.0, 31.0, 31.0）。"
	sanitizedNarration, narrationCalls := srv.sanitizeManagedAskUserModelTurnForCheckpoint(
		run, unsafeNarration, []agentruntime.ToolCall{sanitized},
	)
	if strings.Contains(sanitizedNarration, "44.0") ||
		sanitizedNarration != "继续执行前需要补充可验证的输入，请在下方选择提供方式。" ||
		len(narrationCalls) != 1 {
		t.Fatalf("unsafe AskUser narration was published: %q calls=%#v", sanitizedNarration, narrationCalls)
	}
	ordinaryNarration := "需要提供中心坐标；预计后续运行 3 个重复任务，每个使用 2 个工作线程。"
	preservedNarration, _ := srv.sanitizeManagedAskUserModelTurnForCheckpoint(
		run, ordinaryNarration, []agentruntime.ToolCall{sanitized},
	)
	if preservedNarration != ordinaryNarration {
		t.Fatalf("unrelated numeric narration was rewritten: %q", preservedNarration)
	}

	resolverQuestion, err := json.Marshal(map[string]any{
		"question": "How should the binding-site center be determined?", "header": "Binding site",
		"options": []any{
			option("Provide center", "I will provide an authoritative binding-site center.", true),
			option("Provide reference", "I will provide a receptor with a bound reference ligand.", false),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resolverSanitized := srv.sanitizeManagedAskUserToolCallForCheckpoint(run, agentruntime.ToolCall{
		ID: "ask-resolver", Name: "ask_user", Arguments: resolverQuestion,
	})
	if !strings.Contains(string(resolverSanitized.Arguments), "P2Rank") {
		t.Fatalf("registered evidence resolver was not offered: %s", resolverSanitized.Arguments)
	}
	preNamedResolverQuestion, err := json.Marshal(map[string]any{
		"question": "受体没有已知口袋，请选择处理方式", "header": "结合口袋",
		"options": []any{
			option("P2Rank自动预测口袋", "运行P2Rank预测候选口袋。", true),
			option("手动提供对接中心坐标", "我将提供已知坐标。", false),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	preNamedSanitized := srv.sanitizeManagedAskUserToolCallForCheckpoint(run, agentruntime.ToolCall{
		ID: "ask-resolver-pre-named", Name: "ask_user", Arguments: preNamedResolverQuestion,
	})
	if !strings.Contains(string(preNamedSanitized.Arguments), `"evidence_resolver"`) ||
		!strings.Contains(string(preNamedSanitized.Arguments), "结束并说明原因") {
		t.Fatalf("pre-named resolver option was not bound or lacked terminal route: %s", preNamedSanitized.Arguments)
	}
	var preNamedInput map[string]any
	if err := json.Unmarshal(preNamedResolverQuestion, &preNamedInput); err != nil {
		t.Fatal(err)
	}
	admitted := (serverAgentRuntimeToolGateway{server: srv, taskRun: run}).normalizeAdmittedToolArguments(
		"ask_user", preNamedInput,
	)
	admittedOptions, _ := admitted["options"].([]any)
	if len(admittedOptions) != 3 ||
		!strings.Contains(string(mustMarshalRawMessage(admitted)), `"evidence_resolver"`) ||
		!strings.Contains(string(mustMarshalRawMessage(admitted)), "结束并说明原因") {
		t.Fatalf("execution AskUser arguments diverged from the checkpoint: %#v", admitted)
	}
	chineseResolverQuestion, err := json.Marshal(map[string]any{
		"question": "受体中没有内置配体，请选择结合口袋的确定方式", "header": "分子对接口袋",
		"options": []any{
			option("自动预测结合口袋", "使用口袋预测工具选择最高评分口袋。", true),
			option("手动提供中心坐标", "我将提供已知的结合位点坐标。", false),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	chineseResolverSanitized := srv.sanitizeManagedAskUserToolCallForCheckpoint(run, agentruntime.ToolCall{
		ID: "ask-resolver-zh", Name: "ask_user", Arguments: chineseResolverQuestion,
	})
	if !strings.Contains(string(chineseResolverSanitized.Arguments), "使用P2Rank自动识别") ||
		!strings.Contains(string(chineseResolverSanitized.Arguments), `"evidence_resolver"`) {
		t.Fatalf("generic Chinese pocket choice was not bound to the registered resolver: %s", chineseResolverSanitized.Arguments)
	}
	unrelatedQuestion, err := json.Marshal(map[string]any{
		"question": "How should the final report be formatted?", "header": "Report format",
		"options": []any{
			option("Markdown report", "Create an editable Markdown report.", true),
			option("HTML report", "Create a portable HTML report.", false),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	unrelatedSanitized := srv.sanitizeManagedAskUserToolCallForCheckpoint(run, agentruntime.ToolCall{
		ID: "ask-unrelated", Name: "ask_user", Arguments: unrelatedQuestion,
	})
	if string(unrelatedSanitized.Arguments) != string(unrelatedQuestion) {
		t.Fatalf("resolver leaked into an unrelated decision: %s", unrelatedSanitized.Arguments)
	}

	run.TaskIntent = "Use binding-site center (25.4, 18.2, 30.6) for the attached inputs"
	result := map[string]any{"questions": []askUserQuestion{{Options: []askUserQuestionOption{{
		Label: "Confirmed point", Metadata: map[string]any{
			"route_description": "Use binding-site center 25.4 / 18.2 / 30.6.",
			"execution_parameter_values": []askUserExecutionParameterValues{{
				EvidenceGroup: "binding-site-center", Values: []float64{25.4, 18.2, 30.6},
			}},
		},
	}}}}}
	if correction := askUserManagedExecutionParameterEvidenceCorrection(skillCatalog, capabilityCatalog, run, result); correction != nil {
		t.Fatalf("explicit user-supplied tuple was rejected: %#v", correction)
	}

	run.TaskIntent = "Analyze the attached inputs"
	placeholder := map[string]any{"questions": []askUserQuestion{{Options: []askUserQuestionOption{
		{Label: "Provide values", Metadata: map[string]any{"route_description": "Ask the user to provide x, y, z."}},
		{Label: "Stop", Metadata: map[string]any{"route_description": "Stop until an authoritative input is available."}},
	}}}}
	if correction := askUserManagedExecutionParameterEvidenceCorrection(skillCatalog, capabilityCatalog, run, placeholder); correction != nil {
		t.Fatalf("non-numeric input request was rejected: %#v", correction)
	}
	unrelatedNumbers := map[string]any{"questions": []askUserQuestion{{Options: []askUserQuestionOption{{
		Label: "Resource profile", Metadata: map[string]any{
			"route_description": "Use 3 repeats, a 20 minute budget, and 2 workers.",
		},
	}}}}}
	if correction := askUserManagedExecutionParameterEvidenceCorrection(skillCatalog, capabilityCatalog, run, unrelatedNumbers); correction != nil {
		t.Fatalf("unrelated numeric option text was rejected: %#v", correction)
	}
	mixedContext := map[string]any{"questions": []askUserQuestion{{Options: []askUserQuestionOption{{
		Label: "Validated site", Metadata: map[string]any{
			"route_description": "Use the validated binding-site center; run 3 repeats, 20 minutes, 2 workers.",
		},
	}}}}}
	if correction := askUserManagedExecutionParameterEvidenceCorrection(skillCatalog, capabilityCatalog, run, mixedContext); correction != nil {
		t.Fatalf("separate resource numbers were bound to the referenced center group: %#v", correction)
	}
	sameClauseBoxSize := map[string]any{"questions": []askUserQuestion{{Options: []askUserQuestionOption{{
		Label: "提供中心坐标，使用默认盒子尺寸20×20×20埃", Metadata: map[string]any{
			"route_description": "用户提供x、y、z三个中心坐标数值，使用预设的盒子尺寸20×20×20埃。",
		},
	}}}}}
	for _, gap := range []string{"坐标，使用默认盒子尺寸", "坐标数值，使用预设的盒子尺寸"} {
		if managedExecutionControlledSeparator(gap, capabilityCatalog.Capabilities[0].AcceptedEngines[0].ExecutionPack.Parameters) {
			t.Fatalf("unrelated box-size separator was accepted: %q", gap)
		}
	}
	if correction := askUserManagedExecutionParameterEvidenceCorrection(skillCatalog, capabilityCatalog, run, sameClauseBoxSize); correction != nil {
		route := askUserManagedExecutionOptionText(sameClauseBoxSize["questions"].([]askUserQuestion)[0].Options[0])
		t.Fatalf("same-clause box-size values were bound to the center group: %#v route=%q", correction, route)
	}
	manualValues := map[string]any{"questions": []askUserQuestion{{Options: []askUserQuestionOption{{
		Label: "Confirmed point", Metadata: map[string]any{
			"route_description": "Use binding-site center 3 / 20 / 2.",
			"execution_parameter_values": []askUserExecutionParameterValues{{
				EvidenceGroup: "binding-site-center", Values: []float64{3, 20, 2},
			}},
		},
	}}}}}
	run.setResolvedUserEvidence(map[string]string{"How many repeats, minutes, and workers?": "3 / 20 / 2"})
	if correction := askUserManagedExecutionParameterEvidenceCorrection(skillCatalog, capabilityCatalog, run, manualValues); stringValue(correction["status"]) != "ask_user_parameter_evidence_required" {
		t.Fatalf("unrelated direct answer authorized center values: %#v", correction)
	}
	run.setResolvedUserEvidence(map[string]string{"What binding-site center should be used?": "3 / 20 / 2"})
	if correction := askUserManagedExecutionParameterEvidenceCorrection(skillCatalog, capabilityCatalog, run, manualValues); correction != nil {
		t.Fatalf("question-bound direct center answer was rejected: %#v", correction)
	}
}

func TestManagedExecutionResolverChoiceCopyIsDomainNeutral(t *testing.T) {
	parameters := []sciencecapability.ExecutionParameter{{
		Name: "point_x", Argument: "--point-x", Type: "number",
		Evidence: "resolved-user-input", EvidenceGroup: "control-point",
		EvidenceTerms: []string{"control point", "控制点"},
	}}
	resolver := sciencecapability.ExecutionEvidenceResolver{
		EvidenceGroup: "control-point", Skill: "evidence-resolver", Implementation: "Resolver Engine",
	}
	for name, value := range map[string]any{
		"resolver": managedExecutionResolverAskUserOption(
			resolver, "control-point", parameters, "How should the control point be resolved?",
		),
		"stop": managedExecutionEnsureStopOption(
			nil, "control-point", parameters, "How should the control point be resolved?",
		),
	} {
		encoded := strings.ToLower(string(mustMarshalRawMessage(value)))
		for _, forbidden := range []string{"pocket", "docking", "coordinate", "口袋", "对接", "坐标"} {
			if strings.Contains(encoded, forbidden) {
				t.Fatalf("%s copy leaked domain-specific term %q: %s", name, forbidden, encoded)
			}
		}
		if !strings.Contains(encoded, "control point") {
			t.Fatalf("%s copy omitted registry-authored evidence label: %s", name, encoded)
		}
	}
}

func TestDurableExecutedSkillRetainsResolverAuthorityOutsideSelectionWindow(t *testing.T) {
	skillCatalog := skills.NewCatalog()
	skillCatalog.AddSkill(skills.Skill{
		Name: "primary-skill", ImplementationIdentities: []string{"Primary Engine"},
	})
	skillCatalog.AddSkill(skills.Skill{
		Name: "primary-skill", ImplementationIdentities: []string{"Primary Engine"},
	})
	capabilityCatalog := &sciencecapability.Catalog{Capabilities: []sciencecapability.Definition{{
		ID: "primary-capability", AcceptedEngines: []sciencecapability.EngineDefinition{{
			ID: "primary", ExecutionPack: sciencecapability.ExecutionPack{
				ID: "primary-capability.primary", Mode: "local", Skill: "primary-skill",
				Parameters: []sciencecapability.ExecutionParameter{{
					Name: "point_x", Argument: "--point-x", Type: "number",
					Evidence: "resolved-user-input", EvidenceGroup: "control-point",
					EvidenceTerms: []string{"control point"},
				}},
				EvidenceResolvers: []sciencecapability.ExecutionEvidenceResolver{{
					EvidenceGroup: "control-point", Skill: "resolver-skill", Implementation: "Resolver Engine",
				}},
			},
		}},
	}}}
	run := &sessionRunnerChatRun{ExecutedSkillNames: []string{"primary-skill"}}
	candidate := sciencecapability.ExecutionEvidenceResolver{
		EvidenceGroup: "control-point", Skill: "resolver-skill", Implementation: "Resolver Engine",
	}
	validated, ok := validatedSelectedAskUserEvidenceResolvers(
		skillCatalog, capabilityCatalog, run, []sciencecapability.ExecutionEvidenceResolver{candidate},
	)
	if !ok || len(validated) != 1 || validated[0] != candidate {
		t.Fatalf("durable Skill resolver authority validated=%#v ok=%t", validated, ok)
	}
}

func TestAnsweredResolverUsesUniqueRegistryParentOutsideReplayWindow(t *testing.T) {
	skillCatalog := skills.NewCatalog()
	skillCatalog.AddSkill(skills.Skill{
		Name: "primary-skill", ImplementationIdentities: []string{"Primary Engine"},
	})
	skillCatalog.AddSkill(skills.Skill{
		Name: "resolver-skill", ImplementationIdentities: []string{"Resolver Engine"},
	})
	resolver := sciencecapability.ExecutionEvidenceResolver{
		EvidenceGroup: "control-point", Skill: "resolver-skill", Implementation: "Resolver Engine",
	}
	pack := sciencecapability.ExecutionPack{
		ID: "primary-capability.primary", Mode: "local", Skill: "primary-skill",
		EvidenceResolvers: []sciencecapability.ExecutionEvidenceResolver{resolver},
	}
	catalog := &sciencecapability.Catalog{Capabilities: []sciencecapability.Definition{{
		ID: "primary-capability", AcceptedEngines: []sciencecapability.EngineDefinition{{
			ID: "primary", ExecutionPack: pack,
		}},
	}}}
	run := &sessionRunnerChatRun{}
	validated, ok := validatedSelectedAskUserEvidenceResolvers(
		skillCatalog, catalog, run, []sciencecapability.ExecutionEvidenceResolver{resolver},
	)
	if !ok || len(validated) != 1 || validated[0] != resolver {
		t.Fatalf("unique parent resolver validated=%#v ok=%t", validated, ok)
	}
	if got := run.selectedImplementationsSnapshot(); !reflect.DeepEqual(got, []string{"Primary Engine"}) {
		t.Fatalf("unique parent implementation was not recovered: %v", got)
	}
	catalog.Capabilities = append(catalog.Capabilities, sciencecapability.Definition{
		ID: "other-capability", AcceptedEngines: []sciencecapability.EngineDefinition{{
			ID: "other", ExecutionPack: sciencecapability.ExecutionPack{
				ID: "other-capability.other", Mode: "local", Skill: "other-primary",
				EvidenceResolvers: []sciencecapability.ExecutionEvidenceResolver{resolver},
			},
		}},
	})
	if validated, ok := validatedSelectedAskUserEvidenceResolvers(
		skillCatalog, catalog, &sessionRunnerChatRun{}, []sciencecapability.ExecutionEvidenceResolver{resolver},
	); ok || len(validated) != 0 {
		t.Fatalf("ambiguous parent resolver was accepted: %#v ok=%t", validated, ok)
	}
}

func TestAskUserRejectsModelAuthoredSingleControlledParameter(t *testing.T) {
	skillCatalog := skills.NewCatalog()
	skillCatalog.AddSkill(skills.Skill{Name: "managed-engine", ImplementationIdentities: []string{"Managed Engine"}})
	capabilityCatalog := &sciencecapability.Catalog{Capabilities: []sciencecapability.Definition{{
		ID: "managed-analysis",
		AcceptedEngines: []sciencecapability.EngineDefinition{{
			ID: "engine",
			ExecutionPack: sciencecapability.ExecutionPack{
				Mode: "local", Skill: "managed-engine",
				Parameters: []sciencecapability.ExecutionParameter{{
					Name: "temperature", Type: "number", Evidence: "resolved-user-input", EvidenceGroup: "temperature", EvidenceTerms: []string{"temperature"},
				}},
			},
		}},
	}}}
	run := &sessionRunnerChatRun{TaskIntent: "Analyze the attached inputs", SelectedImplementations: []string{"Managed Engine"}}
	result := map[string]any{"questions": []askUserQuestion{{Options: []askUserQuestionOption{{
		Label: "Estimated temperature", Metadata: map[string]any{
			"route_description": "Run at temperature 310 K.",
			"execution_parameter_values": []askUserExecutionParameterValues{{
				EvidenceGroup: "temperature", Values: []float64{310},
			}},
		},
	}}}}}
	correction := askUserManagedExecutionParameterEvidenceCorrection(skillCatalog, capabilityCatalog, run, result)
	if stringValue(correction["status"]) != "ask_user_parameter_evidence_required" ||
		stringValue(correction["evidence_group"]) != "temperature" {
		t.Fatalf("model-authored scalar correction=%#v", correction)
	}
	omittedDeclaration := map[string]any{"questions": []askUserQuestion{{Options: []askUserQuestionOption{{
		Label: "Estimated temperature", Metadata: map[string]any{"route_description": "Run at temperature 310 K."},
	}}}}}
	correction = askUserManagedExecutionParameterEvidenceCorrection(skillCatalog, capabilityCatalog, run, omittedDeclaration)
	if stringValue(correction["status"]) != "ask_user_parameter_contract_invalid" ||
		stringValue(correction["evidence_group"]) != "temperature" {
		t.Fatalf("omitted controlled scalar declaration correction=%#v", correction)
	}

	run.TaskIntent = "Run the attached inputs at temperature 310 K"
	if correction := askUserManagedExecutionParameterEvidenceCorrection(skillCatalog, capabilityCatalog, run, result); correction != nil {
		t.Fatalf("explicit user-supplied scalar was rejected: %#v", correction)
	}
}

func TestAskUserControlledGroupCorrectionIsDeterministic(t *testing.T) {
	skillCatalog := skills.NewCatalog()
	skillCatalog.AddSkill(skills.Skill{Name: "managed-engine", ImplementationIdentities: []string{"Managed Engine"}})
	capabilityCatalog := &sciencecapability.Catalog{Capabilities: []sciencecapability.Definition{{
		ID: "managed-analysis",
		AcceptedEngines: []sciencecapability.EngineDefinition{{
			ID: "engine",
			ExecutionPack: sciencecapability.ExecutionPack{
				Mode: "local", Skill: "managed-engine",
				Parameters: []sciencecapability.ExecutionParameter{
					{Name: "zeta_value", Type: "number", Evidence: "resolved-user-input", EvidenceGroup: "zeta"},
					{Name: "alpha_value", Type: "number", Evidence: "resolved-user-input", EvidenceGroup: "alpha"},
				},
			},
		}},
	}}}
	run := &sessionRunnerChatRun{TaskIntent: "Analyze the attached inputs", SelectedImplementations: []string{"Managed Engine"}}
	result := map[string]any{"questions": []askUserQuestion{{Options: []askUserQuestionOption{{
		Label: "Estimated values", Metadata: map[string]any{"route_description": "Use alpha 1 and zeta 2."},
	}}}}}
	for attempt := 0; attempt < 20; attempt++ {
		correction := askUserManagedExecutionParameterEvidenceCorrection(skillCatalog, capabilityCatalog, run, result)
		if stringValue(correction["evidence_group"]) != "alpha" {
			t.Fatalf("attempt %d returned nondeterministic group: %#v", attempt, correction)
		}
	}
}

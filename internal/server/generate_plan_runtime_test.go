package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestGeneratePlanPersistsOneArtifactPausesAndResumesAfterExactApproval(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	const frameID = "frame-generate-plan"
	seedTranscriptWebFrame(t, store, "local", "project-generate-plan", frameID)

	plan := map[string]any{
		"human_description": "Preparing the execution plan",
		"task_summary":      "Compare two validated analysis routes and deliver one reviewed report.",
		"phases": []any{map[string]any{"name": "Execution", "delegations": []any{map[string]any{
			"name": "Runtime", "steps": []any{
				map[string]any{"title": "Inspect the supported interfaces", "description": "Load the relevant Skill or inspect the installed API before execution.", "kind": "synthesis"},
				map[string]any{"title": "Execute and review", "description": "Run the admitted route, verify its evidence, and save the final report.", "kind": "delivery"},
			},
		}}}},
		"desired_outputs": []any{"reviewed report"},
		"feasibility":     map[string]any{"confidence": "high", "rationale": "The required local interfaces and bounded verification path are available."},
	}
	planArguments, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sequence := requests.Add(1)
		var request chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for _, tool := range request.Tools {
			if tool.Function.Name == "EnterPlanMode" || tool.Function.Name == "ExitPlanMode" {
				t.Fatalf("provider request advertised retired plan tool %s", tool.Function.Name)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(mustJSON(t, request.Messages)), "Write a brief expert orientation before the task begins.") {
			requests.Add(-1)
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": "Evidence coverage will guide the analysis."},
			}}})
			return
		}
		switch sequence {
		case 1:
			var advertised bool
			for _, tool := range request.Tools {
				advertised = advertised || tool.Function.Name == generatePlanToolName
			}
			if !advertised {
				t.Fatalf("provider request did not advertise generate_plan: %#v", request.Tools)
			}
			messages, _ := json.Marshal(request.Messages)
			if !strings.Contains(string(messages), "record one concise working plan") {
				t.Fatalf("provider request omitted the planning contract: %s", messages)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
					"id": "plan-call-1", "type": "function",
					"function": map[string]any{"name": generatePlanToolName, "arguments": string(planArguments)},
				}}},
			}}})
		case 2:
			encoded, _ := json.Marshal(request.Messages)
			if !strings.Contains(string(encoded), compatibilityPlanApprovalText) {
				t.Fatalf("approved plan response was not present on resume: %s", encoded)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "tool_calls": []any{map[string]any{
					"id": "plan-start-1", "type": "function", "function": map[string]any{"name": updateStepStatusToolName, "arguments": `{"step":"Inspect the supported interfaces","status":"in_progress"}`},
				}},
			}}}})
		case 3:
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "tool_calls": []any{map[string]any{
					"id": "plan-status-1", "type": "function", "function": map[string]any{"name": updateStepStatusToolName, "arguments": `{"step":"Inspect the supported interfaces","status":"completed"}`},
				}},
			}}}})
		case 4:
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "tool_calls": []any{map[string]any{
					"id": "plan-start-2", "type": "function", "function": map[string]any{"name": updateStepStatusToolName, "arguments": `{"step":"Execute and review","status":"in_progress"}`},
				}},
			}}}})
		case 5:
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "tool_calls": []any{map[string]any{
					"id": "plan-status-2", "type": "function", "function": map[string]any{"name": updateStepStatusToolName, "arguments": `{"step":"Execute and review","status":"completed"}`},
				}},
			}}}})
		case 6:
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": "Approved plan execution completed."},
			}}})
		default:
			http.Error(w, "unexpected provider request", http.StatusConflict)
		}
	}))
	defer provider.Close()

	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: frameID, MessageUUID: "message-generate-plan", ClientMessageID: "client-generate-plan",
		Text: "Complete this multi-stage analysis and ask me to approve the plan before execution.",
	}); err != nil {
		t.Fatal(err)
	}
	seedAnsweredTaskIntake(t, server, "local", frameID)
	options := SessionRunnerChatOptions{
		SessionID: frameID, RunnerID: "generate-plan-runner-1",
		Endpoint: provider.URL + "/v1/chat/completions", APIKey: "test-key", Model: "test-model",
		AllowedTools: []string{generatePlanToolName, updateStepStatusToolName}, LeaseTTL: time.Minute,
		MaxAttempts: 1, MaxToolRounds: 5, DisableSkillDiscovery: true, DisableMCPDiscovery: true,
		RuntimeSessionConfig: map[string]any{"planMode": true},
	}
	paused, err := server.RunSessionRunnerChatOnce(context.Background(), options)
	if err != nil || !paused.Claimed || paused.Status != "awaiting_approval" || requests.Load() != 1 {
		t.Fatalf("paused=%#v requests=%d err=%v", paused, requests.Load(), err)
	}
	frame, found, err := store.GetCompatibilityFrame(frameID)
	if err != nil || !found || frame.Status != "awaiting_plan_approval" {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		t.Fatalf("metadata=%#v found=%t err=%v", metadata, found, err)
	}
	artifactID := stringValue(metadata.ContextData["_plan_artifact_id"])
	versionID := stringValue(metadata.ContextData["_plan_version_id"])
	if artifactID == "" || versionID == "" || metadata.ContextData["_plan_tool_call_id"] != "plan-call-1" ||
		metadata.ContextData["_plan_approved"] != false {
		t.Fatalf("plan metadata=%#v", metadata.ContextData)
	}
	if mode, found, err := store.ArtifactRetentionMode(artifactID); err != nil || !found || mode != "working_data" {
		t.Fatalf("plan retention mode=%q found=%t err=%v", mode, found, err)
	}
	artifact, version, reader, found, err := store.OpenArtifactVersionContent(versionID)
	if err != nil || !found || artifact.ID != artifactID || version.ID != versionID {
		t.Fatalf("artifact=%#v version=%#v found=%t err=%v", artifact, version, found, err)
	}
	content, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || !strings.Contains(string(content), `"version": 3`) {
		t.Fatalf("plan artifact content=%q read_err=%v close_err=%v", content, readErr, closeErr)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", frameID)
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	state, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID, 1)
	if err != nil || state.Status != "running" || state.Phase != transcriptstore.RunnerPhaseWaitingApproval {
		t.Fatalf("runner state=%#v err=%v", state, err)
	}

	approved := compatJSONRequest(t, server.Handler(), http.MethodPost,
		"/api/frames/"+frameID+"/approve-plan", "local", map[string]any{}, http.StatusOK)
	if approved["status"] != "accepted" {
		t.Fatalf("approval response=%#v", approved)
	}
	options.SessionID = ""
	options.RunnerID = "generate-plan-runner-2"
	resumed, err := server.RunFrameResumeDispatchOnce(context.Background(), FrameResumeDispatchOptions{
		WorkerID: "generate-plan-dispatch", ClaimTTL: time.Second, Chat: options,
	})
	if err != nil || !resumed.Claimed || !resumed.Runner.Claimed || resumed.Status != "completed" || requests.Load() != 6 {
		var terminal any
		if resumed.TerminalEvent != nil {
			terminal = *resumed.TerminalEvent
		}
		t.Fatalf("resumed=%#v terminal=%#v requests=%d err=%v", resumed, terminal, requests.Load(), err)
	}
	frame, found, err = store.GetCompatibilityFrame(frameID)
	if err != nil || !found || frame.Status != "completed" {
		t.Fatalf("completed frame=%#v found=%t err=%v", frame, found, err)
	}
}

func TestNormalizeGeneratedPlanUsesDistinctBilingualResearchModules(t *testing.T) {
	clone := func(input map[string]any) map[string]any {
		encoded, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		var cloned map[string]any
		if err := json.Unmarshal(encoded, &cloned); err != nil {
			t.Fatal(err)
		}
		return cloned
	}
	base := map[string]any{
		"task_summary": "Research a scientific decision and deliver a report.",
		"phases": []any{
			map[string]any{
				"name": "Research",
				"delegations": []any{
					map[string]any{
						"name": "Modules",
						"steps": []any{
							map[string]any{
								"title":             "Mechanism evidence",
								"description":       "Investigate the mechanism module.",
								"kind":              "research",
								"output_module":     "Mechanism",
								"research_question": "What evidence establishes the mechanism?",
								"research_depth":    "deep",
								"discovery_queries": []any{
									map[string]any{"language": "zh", "query": "作用机制 关键证据"},
									map[string]any{"language": "en", "query": "mechanism primary evidence"},
								},
							},
						},
					},
				},
			},
		},
		"desired_outputs": []any{"report"},
		"feasibility":     map[string]any{"confidence": "high", "rationale": "Sources are available."},
	}
	document, _, _, err := normalizeGeneratedPlan(base)
	if err != nil {
		t.Fatal(err)
	}
	step := document.Phases[0].Delegations[0].Steps[0]
	if step.OutputModule != "Mechanism" || step.ResearchDepth != "deep" || len(step.DiscoveryQueries) != 2 {
		t.Fatalf("research module contract was not retained: %#v", step)
	}

	modelDirectedQuery := clone(base)
	phase := mapValue(anySliceValue(modelDirectedQuery["phases"])[0])
	delegation := mapValue(anySliceValue(phase["delegations"])[0])
	modelDirectedStep := mapValue(anySliceValue(delegation["steps"])[0])
	delete(modelDirectedStep, "research_depth")
	delete(modelDirectedStep, "discovery_queries")
	document, _, _, err = normalizeGeneratedPlan(modelDirectedQuery)
	if err != nil {
		t.Fatalf("optional execution hints rejected an otherwise valid research module: %v", err)
	}
	step = document.Phases[0].Delegations[0].Steps[0]
	if step.ResearchDepth != "deep" || len(step.DiscoveryQueries) != 0 {
		t.Fatalf("model-directed research defaults=%#v", step)
	}

	missingQueries := clone(base)
	phase = mapValue(anySliceValue(missingQueries["phases"])[0])
	delegation = mapValue(anySliceValue(phase["delegations"])[0])
	mapValue(anySliceValue(delegation["steps"])[0])["discovery_queries"] = []any{
		map[string]any{"language": "zh", "query": "只有中文查询"},
	}
	if _, _, _, err := normalizeGeneratedPlan(missingQueries); err == nil || !strings.Contains(err.Error(), "Chinese and English") {
		t.Fatalf("monolingual research module was accepted: %v", err)
	}

	duplicate := clone(base)
	phase = mapValue(anySliceValue(duplicate["phases"])[0])
	delegation = mapValue(anySliceValue(phase["delegations"])[0])
	first := mapValue(anySliceValue(delegation["steps"])[0])
	second := copyMapAny(first)
	second["title"] = "Duplicate mechanism process"
	delegation["steps"] = append(anySliceValue(delegation["steps"]), second)
	if _, _, _, err := normalizeGeneratedPlan(duplicate); err == nil || !strings.Contains(err.Error(), "one authoritative investigation") {
		t.Fatalf("duplicate process steps impersonated one output module: %v", err)
	}
}

func TestOrdinaryMultiStageTaskRecordsPlanAndContinuesWithoutApprovalPause(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	const frameID = "frame-autonomous-plan"
	seedTranscriptWebFrame(t, store, "local", "project-autonomous-plan", frameID)

	planArguments, err := json.Marshal(map[string]any{
		"human_description": "Organizing the work",
		"task_summary":      "Inspect the evidence and prepare a checked result.",
		"phases": []any{map[string]any{"name": "Work", "delegations": []any{map[string]any{
			"name": "Analysis", "steps": []any{map[string]any{
				"title": "Inspect and verify", "description": "Inspect the evidence and verify the result before answering.",
			}},
		}}}},
		"desired_outputs": []any{"checked result"},
		"feasibility": map[string]any{
			"confidence": "high", "rationale": "The required evidence is available.",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int64
	var revisedStepIDs []string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sequence := requests.Add(1)
		var request chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(mustJSON(t, request.Messages)), "Write a brief expert orientation before the task begins.") {
			requests.Add(-1)
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": "Evidence coverage will guide the analysis."},
			}}})
			return
		}
		switch sequence {
		case 1:
			if !strings.Contains(string(mustJSON(t, request.Messages)), "continue without waiting for approval") {
				t.Fatalf("ordinary planning guidance missing: %#v", request.Messages)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
					"id": "autonomous-plan-call", "type": "function",
					"function": map[string]any{"name": generatePlanToolName, "arguments": string(planArguments)},
				}}},
			}}})
		case 2:
			encoded := string(mustJSON(t, request.Messages))
			if !strings.Contains(encoded, "working_plan_ready") {
				t.Fatalf("working plan result missing from continuation: %s", encoded)
			}
			if !strings.Contains(string(mustJSON(t, request.Tools)), updateStepStatusToolName) {
				t.Errorf("autonomous plan continuation cannot update its own steps")
			}
			var receipt map[string]any
			if err := json.Unmarshal([]byte(request.Messages[len(request.Messages)-1].Content), &receipt); err != nil {
				t.Errorf("decode plan receipt: %v", err)
			} else if steps := anySliceValue(receipt["steps"]); len(steps) != 1 ||
				stringValue(mapValue(steps[0])["id"]) == "" {
				t.Errorf("plan receipt did not return its generated step identities: %#v", receipt)
			}
			if strings.Contains(encoded, "The working plan is recorded") {
				t.Fatalf("internal plan instruction leaked into continuation: %s", encoded)
			}
			var revision map[string]any
			if err := json.Unmarshal(planArguments, &revision); err != nil {
				t.Fatal(err)
			}
			phase := mapValue(anySliceValue(revision["phases"])[0])
			track := mapValue(anySliceValue(phase["delegations"])[0])
			track["steps"] = append(anySliceValue(track["steps"]), map[string]any{"title": "Follow the new finding", "description": "Inspect the new source before synthesis."})
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "tool_calls": []any{map[string]any{"id": "autonomous-plan-revision", "type": "function", "function": map[string]any{"name": generatePlanToolName, "arguments": string(mustJSON(t, revision))}}},
			}}}})
		case 3:
			var receipt map[string]any
			if err := json.Unmarshal([]byte(request.Messages[len(request.Messages)-1].Content), &receipt); err != nil || len(anySliceValue(receipt["steps"])) != 2 || receipt["status"] != "working_plan_ready" {
				t.Fatalf("revised research plan not available in next provider request: %#v %v", receipt, err)
			}
			revisedStepIDs = revisedStepIDs[:0]
			for _, rawStep := range anySliceValue(receipt["steps"]) {
				revisedStepIDs = append(revisedStepIDs, stringValue(mapValue(rawStep)["id"]))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
					"id": "complete-first-step", "type": "function",
					"function": map[string]any{"name": updateStepStatusToolName, "arguments": string(mustJSON(t, map[string]any{
						"step": revisedStepIDs[0], "status": "completed",
					}))},
				}}},
			}}})
		case 4:
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
					"id": "complete-second-step", "type": "function",
					"function": map[string]any{"name": updateStepStatusToolName, "arguments": string(mustJSON(t, map[string]any{
						"step": revisedStepIDs[1], "status": "completed",
					}))},
				}}},
			}}})
		case 5:
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": "The checked result is ready."},
			}}})
		default:
			http.Error(w, "unexpected provider request", http.StatusConflict)
		}
	}))
	defer provider.Close()

	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: frameID, MessageUUID: "message-autonomous-plan", ClientMessageID: "client-autonomous-plan",
		Text: "Inspect the available evidence in several stages and prepare a checked result.",
	}); err != nil {
		t.Fatal(err)
	}
	seedAnsweredTaskIntake(t, server, "local", frameID)
	result, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{
		SessionID: frameID, RunnerID: "autonomous-plan-runner",
		Endpoint: provider.URL + "/v1/chat/completions", APIKey: "test-key", Model: "test-model",
		AllowedTools: []string{generatePlanToolName, updateStepStatusToolName}, LeaseTTL: time.Minute,
		MaxAttempts: 1, MaxToolRounds: 4, DisableSkillDiscovery: true, DisableMCPDiscovery: true,
	})
	if err != nil || !result.Claimed || result.Status != "completed" || requests.Load() != 5 {
		t.Fatalf("result=%#v requests=%d err=%v", result, requests.Load(), err)
	}
	frame, found, err := store.GetCompatibilityFrame(frameID)
	if err != nil || !found || frame.Status != "completed" {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	metadata, _, err := store.GetFrameRuntimeMetadata(frameID)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.ContextData["_plan_approved"] != nil {
		t.Fatalf("ordinary working plan created an approval state: %#v", metadata.ContextData)
	}
	if metadata.ContextData["_plan_execution_authorized"] != true ||
		metadata.ContextData["_plan_control_mode"] != "autonomous" ||
		metadata.ContextData["_plan_json"] == nil ||
		stringValue(metadata.ContextData["_plan_artifact_id"]) == "" {
		t.Fatalf("ordinary working plan was not preserved as executable state: %#v", metadata.ContextData)
	}
	context := server.generatedPlanDesiredOutputsContext(frameID)
	for _, required := range []string{"synon.research_navigation.v1", "desired_outputs", "checked result", "Inspect and verify"} {
		if !strings.Contains(context, required) {
			t.Fatalf("autonomous plan context missing %q: %s", required, context)
		}
	}
}

func TestPlanModeRejectsProseCompletionThenPersistsGeneratedPlan(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	const frameID = "frame-plan-mode-gate"
	seedTranscriptWebFrame(t, store, "local", "project-plan-mode-gate", frameID)
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: frameID, MessageUUID: "message-plan-mode-gate", ClientMessageID: "client-plan-mode-gate",
		Text: "Create a multi-stage evidence plan and wait for approval.",
	}); err != nil {
		t.Fatal(err)
	}
	planArguments, err := json.Marshal(map[string]any{
		"human_description": "Preparing the execution plan",
		"task_summary":      "Prepare a bounded evidence workflow.",
		"phases": []any{map[string]any{"name": "Planning", "delegations": []any{map[string]any{
			"name": "Evidence", "steps": []any{map[string]any{
				"title": "Inspect sources", "description": "Confirm the admitted source interfaces before execution.",
			}},
		}}}},
		"desired_outputs": []any{"reviewed report"},
		"feasibility": map[string]any{
			"confidence": "high", "rationale": "The required interfaces are available.",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sequence := requests.Add(1)
		var request chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(mustJSON(t, request.Messages)), "Write a brief expert orientation before the task begins.") {
			requests.Add(-1)
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": "The evidence plan will be checked before execution."},
			}}})
			return
		}
		if sequence == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": "Here is a prose plan."},
			}}})
			return
		}
		if sequence == 2 {
			encoded, _ := json.Marshal(request.Messages)
			if !strings.Contains(string(encoded), "Plan-mode completion gate 1:") {
				t.Fatalf("second request omitted plan-mode correction: %s", encoded)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
					"id": "plan-mode-call", "type": "function",
					"function": map[string]any{"name": generatePlanToolName, "arguments": string(planArguments)},
				}}},
			}}})
			return
		}
		http.Error(w, "unexpected provider request", http.StatusConflict)
	}))
	defer provider.Close()

	result, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{
		SessionID: frameID, RunnerID: "plan-mode-gate-runner",
		Endpoint: provider.URL + "/v1/chat/completions", APIKey: "test-key", Model: "test-model",
		AllowedTools: []string{generatePlanToolName}, LeaseTTL: time.Minute, ReplayLimit: 100,
		MaxAttempts: 1, MaxToolRounds: 2, DisableSkillDiscovery: true, DisableMCPDiscovery: true,
		RuntimeSessionConfig: map[string]any{"planMode": true},
	})
	if err != nil || !result.Claimed || result.Status != "awaiting_approval" || requests.Load() != 2 {
		failedFrame, _, _ := store.GetCompatibilityFrame(frameID)
		t.Fatalf("result=%#v requests=%d frame=%#v err=%v", result, requests.Load(), failedFrame, err)
	}
	frame, found, err := store.GetCompatibilityFrame(frameID)
	if err != nil || !found || frame.Status != "awaiting_plan_approval" {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", frameID)
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	replay, err := repo.ListRunnerReplay(context.Background(), transcriptstore.ListRunnerReplayInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, MessageLimit: 100, CheckpointLimit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	foundDenial := false
	for _, event := range replay {
		var payload map[string]any
		if json.Unmarshal(event.ResolvedPayloadJSON, &payload) == nil &&
			stringValue(payload["toolPhase"]) == planModeDenialToolPhase && numberValue(payload["planModeDenial"]) == 1 {
			foundDenial = true
		}
	}
	if !foundDenial {
		t.Fatal("plan-mode completion denial was not persisted")
	}
}

type planModeCompletionModel struct {
	calls    int
	requests []agentruntime.ModelRequest
}

func (model *planModeCompletionModel) Complete(
	_ context.Context,
	request agentruntime.ModelRequest,
) (agentruntime.ModelResponse, error) {
	model.calls++
	model.requests = append(model.requests, request)
	return agentruntime.ModelResponse{Message: agentruntime.Message{
		Role: "assistant", Content: "Prose plan candidate",
	}}, nil
}

func TestPlanModeUsesExplicitSessionControlAndBoundedCompletionDenials(t *testing.T) {
	model := &planModeCompletionModel{}
	denials := []int{}
	session := sessionstore.Session{ID: "frame-plan-mode", Orchestration: map[string]any{
		"sessionConfig": map[string]any{"planMode": true},
	}}
	result, err := (&Server{}).runSessionAgentWithPlanMode(
		context.Background(), session, agentruntime.Engine{Model: model}, agentruntime.RunRequest{
			Messages: []agentruntime.Message{{Role: "user", Content: "Prepare a plan."}},
		}, nil, func(denial int, correction string) error {
			denials = append(denials, denial)
			if !strings.Contains(correction, "generate_plan") {
				t.Fatalf("plan-mode correction=%q", correction)
			}
			return nil
		},
	)
	var pending sessionRunnerPlanApprovalRequired
	if !errors.As(err, &pending) || result.FinalMessage.Content != "Prose plan candidate" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if model.calls != maxSessionRunnerPlanModeUnitDenials ||
		!slices.Equal(denials, []int{1, 2, 3}) {
		t.Fatalf("calls=%d denials=%#v", model.calls, denials)
	}
	for index, request := range model.requests {
		if request.ToolChoice != nil {
			t.Fatalf("request %d manufactured tool choice %#v", index, request.ToolChoice)
		}
	}
}

func TestOrdinaryTurnDoesNotEnterPlanModeFromTaskWording(t *testing.T) {
	model := &planModeCompletionModel{}
	session := sessionstore.Session{ID: "frame-ordinary"}
	result, err := (&Server{}).runSessionAgentWithPlanMode(
		context.Background(), session, agentruntime.Engine{Model: model}, agentruntime.RunRequest{
			Messages: []agentruntime.Message{{Role: "user", Content: "Create an execution plan and wait for approval."}},
		}, nil, nil,
	)
	if err != nil || result.FinalMessage.Content == "" || model.calls != 1 {
		t.Fatalf("result=%#v calls=%d err=%v", result, model.calls, err)
	}
}

func TestWorkingPlanIsAvailableWithoutExposingApprovalStatusTool(t *testing.T) {
	schemas := []agentruntime.ToolSchema{
		{Name: "web_fetch"}, {Name: generatePlanToolName}, {Name: updateStepStatusToolName},
	}
	ordinary := filterSessionRunnerPlanningTools(schemas, false)
	if !agentRuntimeToolSchemaNamed(ordinary, generatePlanToolName) ||
		agentRuntimeToolSchemaNamed(ordinary, updateStepStatusToolName) ||
		!agentRuntimeToolSchemaNamed(ordinary, "web_fetch") {
		t.Fatalf("ordinary tools=%#v", ordinary)
	}
	planning := filterSessionRunnerPlanningTools(schemas, true)
	if !agentRuntimeToolSchemaNamed(planning, generatePlanToolName) ||
		!agentRuntimeToolSchemaNamed(planning, updateStepStatusToolName) {
		t.Fatalf("plan-mode tools=%#v", planning)
	}
}

func TestApprovedPlanDoesNotReenterGeneratePlanOnResume(t *testing.T) {
	schemas := []agentruntime.ToolSchema{
		{Name: "web_search"}, {Name: generatePlanToolName}, {Name: updateStepStatusToolName},
	}
	resumed := filterSessionRunnerPlanningToolsForState(schemas, true, true)
	if agentRuntimeToolSchemaNamed(resumed, generatePlanToolName) {
		t.Fatal("approved plan was re-exposed to resumed execution")
	}
	if !agentRuntimeToolSchemaNamed(resumed, updateStepStatusToolName) || !agentRuntimeToolSchemaNamed(resumed, "web_search") {
		t.Fatalf("approved plan removed unrelated tools: %#v", resumed)
	}
}

func TestPlanModeDenialCountRestoresDurableBudget(t *testing.T) {
	entries := []eventjournal.Entry{
		{Message: eventjournal.Message{"type": "runner_checkpoint", "toolPhase": planModeDenialToolPhase, "planModeDenial": 1}},
		{Message: eventjournal.Message{"type": "runner_checkpoint", "toolPhase": "completed", "planModeDenial": 9}},
		{Message: eventjournal.Message{"type": "runner_checkpoint", "toolPhase": planModeDenialToolPhase, "planModeDenial": 3}},
	}
	if got := sessionRunnerPlanModeDenialCount(entries); got != 3 {
		t.Fatalf("durable plan-mode denial count=%d", got)
	}
}

func TestNormalizeGeneratedPlanRejectsCompetingOrUnboundedShapes(t *testing.T) {
	valid := map[string]any{
		"version": 3, "task_summary": "Bounded plan",
		"phases": []any{map[string]any{"name": "Phase", "delegations": []any{map[string]any{
			"name": "Work", "steps": []any{map[string]any{"title": "Inspect", "description": "Read the supported interface first."}},
		}}}},
		"feasibility": map[string]any{"confidence": "medium", "rationale": "One external dependency may be unavailable."},
	}
	for name, mutate := range map[string]func(map[string]any){
		"wrong version": func(plan map[string]any) { plan["version"] = 2 },
		"unknown route": func(plan map[string]any) { plan["fallback_plan"] = map[string]any{"version": 3} },
		"empty phases":  func(plan map[string]any) { plan["phases"] = []any{} },
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(valid)
			var candidate map[string]any
			_ = json.Unmarshal(raw, &candidate)
			mutate(candidate)
			if _, _, _, err := normalizeGeneratedPlan(candidate); err == nil {
				t.Fatalf("normalizeGeneratedPlan accepted %s", name)
			}
		})
	}
}

func TestNormalizeGeneratedPlanUsesHumanDescriptionAsMissingTaskSummary(t *testing.T) {
	input := map[string]any{
		"human_description": "Design the translational and clinical pharmacology program.",
		"phases": []any{map[string]any{"name": "Evidence", "delegations": []any{map[string]any{
			"name": "Clinical pharmacology", "steps": []any{map[string]any{
				"title": "Build the evidence map", "description": "Verify the source evidence and calculations.",
			}},
		}}}},
		"desired_outputs": []any{"decision report"},
		"feasibility": map[string]any{
			"confidence": "medium", "rationale": "Candidate-specific inputs remain necessary for numeric dosing.",
		},
	}
	document, normalized, _, err := normalizeGeneratedPlan(input)
	if err != nil {
		t.Fatal(err)
	}
	if document.TaskSummary != input["human_description"] || normalized["task_summary"] != input["human_description"] {
		t.Fatalf("document=%#v normalized=%#v", document, normalized)
	}
}

func TestNormalizeGeneratePlanToolArgumentsStripsRuntimeOwnedFields(t *testing.T) {
	normalized := normalizeLegacyGeneratePlanArguments(map[string]any{
		"version": 3, "task_summary": "Prepare the report.",
		"phases": []any{map[string]any{
			"id": "phase-model", "name": "Evidence", "steps": []any{map[string]any{
				"id": "step-model", "title": "Collect evidence", "description": "Use the approved source route.",
				"status": "pending", "notes": "model-owned",
			}},
		}},
		"feasibility": map[string]any{"confidence": "high", "rationale": "Sources are available."},
	})
	encoded, err := json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"\"version\"", "\"id\"", "\"agent_name\"", "\"status\"", "\"notes\""} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("normalized plan retained runtime field %s: %s", forbidden, encoded)
		}
	}
	phases := anySliceValue(normalized["phases"])
	delegations := anySliceValue(mapValue(phases[0])["delegations"])
	steps := anySliceValue(mapValue(delegations[0])["steps"])
	if len(phases) != 1 || len(delegations) != 1 || len(steps) != 1 || stringValue(mapValue(steps[0])["title"]) != "Collect evidence" {
		t.Fatalf("normalized plan shape=%#v", normalized)
	}
}

func TestNormalizeGeneratePlanToolArgumentsAcceptsProviderNativeSinglePhase(t *testing.T) {
	normalized := normalizeLegacyGeneratePlanArguments(map[string]any{
		"name": "Structure-guided design",
		"delegations": []any{map[string]any{
			"id": "delegation-model", "agent_name": "scientist", "name": "Evidence and computation",
			"steps": []any{map[string]any{
				"id": "step-model", "status": "pending", "notes": "model-owned",
				"title": "Verify inputs", "description": "Confirm source data before computation.",
			}},
		}},
		"desired_outputs": []any{"validated candidate report"},
	})
	document, canonical, _, err := normalizeGeneratedPlan(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if document.TaskSummary != "Structure-guided design" || canonical["task_summary"] != "Structure-guided design" {
		t.Fatalf("task summary was not preserved: document=%#v canonical=%#v", document, canonical)
	}
	if stringValue(mapValue(normalized["feasibility"])["confidence"]) != "medium" {
		t.Fatalf("default feasibility=%#v", normalized["feasibility"])
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"\"id\"", "\"agent_name\"", "\"status\"", "\"notes\""} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("provider-native plan retained runtime field %s: %s", forbidden, encoded)
		}
	}
	if phases := anySliceValue(normalized["phases"]); len(phases) != 1 || stringValue(mapValue(phases[0])["name"]) != "Structure-guided design" {
		t.Fatalf("normalized phases=%#v", phases)
	}
}

func TestGeneratePlanAdmissionAcceptsEmptyOptionalDiscoveryQueries(t *testing.T) {
	server := New(Options{FileRoot: t.TempDir()})
	tool, found := server.tools.Get(generatePlanToolName)
	if !found {
		t.Fatal("generate_plan tool is unavailable")
	}
	schema := agentruntime.ToolSchema{
		Name: tool.Name, Description: tool.Description, Parameters: chatToolParameters(tool),
	}
	gateway := serverAgentRuntimeToolGateway{
		server: server, toolSchemas: []agentruntime.ToolSchema{schema},
		toolValidators: agentRuntimeToolValidators([]agentruntime.ToolSchema{schema}),
	}
	input := gateway.normalizeAdmittedToolArguments(generatePlanToolName, map[string]any{
		"name": "Research",
		"delegations": []any{map[string]any{
			"name": "Evidence", "steps": []any{map[string]any{
				"title": "Investigate", "description": "Investigate the evidence.",
				"kind": "research", "output_module": "Evidence",
				"research_question": "What evidence answers the question?",
				"research_depth":    "deep", "discovery_queries": []any{},
			}},
		}},
	})
	if failure := gateway.validateAdmittedToolArguments(generatePlanToolName, input); failure != nil {
		t.Fatalf("empty optional discovery_queries failed admission: %#v", failure)
	}
	if _, _, _, err := normalizeGeneratedPlan(input); err != nil {
		t.Fatalf("admitted model-directed research plan failed normalization: %v", err)
	}
}

func TestGeneratePlanAdmissionNormalizesProviderContainerAliases(t *testing.T) {
	server := New(Options{FileRoot: t.TempDir()})
	tool, found := server.tools.Get(generatePlanToolName)
	if !found {
		t.Fatal("generate_plan tool is unavailable")
	}
	schema := agentruntime.ToolSchema{
		Name: tool.Name, Description: tool.Description, Parameters: chatToolParameters(tool),
	}
	gateway := serverAgentRuntimeToolGateway{
		server: server, toolSchemas: []agentruntime.ToolSchema{schema},
		toolValidators: agentRuntimeToolValidators([]agentruntime.ToolSchema{schema}),
	}
	input := gateway.normalizeAdmittedToolArguments(generatePlanToolName, map[string]any{
		"name": "Provider plan",
		"phases": []any{map[string]any{
			"title": "Evidence", "description": "Collect the evidence.", "kind": "research",
			"delegations": []any{map[string]any{
				"title": "Collection", "description": "Collect records.",
				"steps": []any{map[string]any{
					"name": "Inspect", "human_description": "Inspect the source records.",
				}},
			}},
		}},
	})
	if failure := gateway.validateAdmittedToolArguments(generatePlanToolName, input); failure != nil {
		t.Fatalf("provider container aliases failed admission: %#v input=%#v", failure, input)
	}
	document, _, _, err := normalizeGeneratedPlan(input)
	if err != nil {
		t.Fatal(err)
	}
	step := document.Phases[0].Delegations[0].Steps[0]
	if document.TaskSummary != "Provider plan" || document.Phases[0].Name != "Evidence" ||
		document.Phases[0].Delegations[0].Name != "Collection" || step.Title != "Inspect" ||
		step.Description != "Inspect the source records." || step.Kind != generatedPlanStepKindWork {
		t.Fatalf("provider plan aliases were not normalized: %#v", document)
	}
}

func TestGeneratePlanTextApprovalUsesApproveOnlyCallBeforeExecution(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	const frameID = "frame-plan-text-approval"
	seedTranscriptWebFrame(t, store, "local", "project-plan-text-approval", frameID)
	plan := map[string]any{
		"task_summary": "Verify text approval",
		"phases": []any{map[string]any{"name": "Execution", "delegations": []any{map[string]any{
			"name": "Runtime", "steps": []any{map[string]any{"title": "Run approved work", "description": "Execute only after approval.", "kind": "delivery"}},
		}}}},
		"desired_outputs": []any{"approval audit"},
		"feasibility":     map[string]any{"confidence": "high", "rationale": "The local verification path is available."},
	}
	planArguments, _ := json.Marshal(plan)
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sequence := requests.Add(1)
		var request chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch sequence {
		case 1:
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "tool_calls": []any{map[string]any{"id": "plan-text-create", "type": "function", "function": map[string]any{"name": generatePlanToolName, "arguments": string(planArguments)}}},
			}}}})
		case 2:
			raw, _ := json.Marshal(request.Messages)
			if !strings.Contains(string(raw), "go ahead") {
				t.Fatalf("text approval was not delivered to the model: %s", raw)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "tool_calls": []any{map[string]any{"id": "plan-text-approve", "type": "function", "function": map[string]any{"name": generatePlanToolName, "arguments": `{"approve":true}`}}},
			}}}})
		case 3:
			raw, _ := json.Marshal(request.Messages)
			if !strings.Contains(string(raw), "plan_approved") {
				t.Fatalf("approve-only result was not delivered before execution: %s", raw)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "tool_calls": []any{map[string]any{"id": "plan-text-start", "type": "function", "function": map[string]any{"name": updateStepStatusToolName, "arguments": `{"step":"Run approved work","status":"in_progress"}`}}},
			}}}})
		case 4:
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "tool_calls": []any{map[string]any{"id": "plan-text-status", "type": "function", "function": map[string]any{"name": updateStepStatusToolName, "arguments": `{"step":"Run approved work","status":"completed"}`}}},
			}}}})
		case 5:
			raw, _ := json.Marshal(request.Messages)
			if !strings.Contains(string(raw), "plan-text-status") {
				t.Fatalf("terminal plan status was not delivered before completion: %s", raw)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "Approved execution completed."}}}})
		default:
			http.Error(w, "unexpected provider request", http.StatusConflict)
		}
	}))
	defer provider.Close()

	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	defer server.Close(context.Background())
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: frameID, MessageUUID: "message-plan-text", ClientMessageID: "client-plan-text", Text: "Prepare and run an approval-gated plan.",
	}); err != nil {
		t.Fatal(err)
	}
	seedAnsweredTaskIntake(t, server, "local", frameID)
	options := SessionRunnerChatOptions{
		SessionID: frameID, RunnerID: "plan-text-runner-1", Endpoint: provider.URL + "/v1/chat/completions",
		APIKey: "test-key", Model: "test-model", AllowedTools: []string{generatePlanToolName, updateStepStatusToolName},
		LeaseTTL: time.Minute, MaxAttempts: 1, MaxToolRounds: 4, DisableSkillDiscovery: true, DisableMCPDiscovery: true,
		RuntimeSessionConfig: map[string]any{"planMode": true},
	}
	paused, err := server.RunSessionRunnerChatOnce(context.Background(), options)
	if err != nil || paused.Status != "awaiting_approval" || requests.Load() != 1 {
		t.Fatalf("paused=%#v requests=%d err=%v", paused, requests.Load(), err)
	}
	message := compatJSONRequest(t, server.Handler(), http.MethodPost, "/api/frames/"+frameID+"/message", "local", map[string]any{
		"input_data": map[string]any{"request": "go ahead"},
	}, http.StatusOK)
	if message["status"] != "accepted" {
		t.Fatalf("text approval message=%#v", message)
	}
	options.SessionID = ""
	options.RunnerID = "plan-text-runner-2"
	resumed, err := server.RunFrameResumeDispatchOnce(context.Background(), FrameResumeDispatchOptions{
		WorkerID: "plan-text-dispatch", ClaimTTL: time.Second, Chat: options,
	})
	if err != nil || resumed.Status != "completed" || requests.Load() != 5 {
		terminal := any(nil)
		if resumed.TerminalEvent != nil {
			terminal = resumed.TerminalEvent.Payload
		}
		failedFrame, _, _ := store.GetFrame(frameID)
		t.Fatalf("resumed=%#v terminal=%#v description=%q requests=%d err=%v", resumed, terminal, failedFrame.StatusDescription, requests.Load(), err)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found || metadata.ContextData["_plan_approved"] != true || stringValue(metadata.ContextData["_plan_approval_id"]) == "" {
		t.Fatalf("approved metadata=%#v found=%t err=%v", metadata, found, err)
	}
}

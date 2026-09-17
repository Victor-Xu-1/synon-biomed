package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestNormalizeAskUserEvidenceAuthoritiesDropsInventedReferences(t *testing.T) {
	questions, err := askUserQuestionValue(map[string]any{
		"question": "Which route should be used?", "header": "Route",
		"options": []any{
			askUserDecisionOption(
				"Local", "Use the local route.", "Keeps data local.", "Uses local resources.",
				"Ready from the current task.", []any{"user-input:current-task", "tool-call:verified-call"},
				"Local compute.", "A local result.", "Recommended for local data.", true,
			),
			askUserDecisionOption(
				"Remote", "Use the remote route.", "Adds capacity.", "Transfers data.",
				"The remote tool is already installed.", []any{"tool is installed", "tool-call:invented-call"},
				"Remote credentials.", "A remote result.", "Choose for capacity.", false,
			),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	normalized := normalizeAskUserEvidenceAuthorities(questions, map[string]askUserEvidenceAuthority{
		"user-input:current-task": {Class: askUserEvidenceUserObjective},
		"tool-call:verified-call": {Class: askUserEvidenceCompletedTool},
	})
	first := normalized[0].Options[0].Metadata
	if got := strings.Join(stringArrayValue(first["decision_evidence"]), "|"); got != "user-input:current-task|tool-call:verified-call" {
		t.Fatalf("first decision evidence=%q", got)
	}
	second := normalized[0].Options[1].Metadata
	if got := strings.Join(stringArrayValue(second["decision_evidence"]), "|"); got != askUserCurrentTaskEvidenceReference {
		t.Fatalf("invented evidence was not replaced by current task authority: %q", got)
	}
	if got := stringValue(second["selection_basis"]); got != "user_objective" {
		t.Fatalf("selection basis=%q want user_objective", got)
	}
}

func TestNormalizeAskUserEvidenceAuthoritiesBindsMatchingPreflightReceipt(t *testing.T) {
	call := agentruntime.ToolCall{
		ID: "preflight-engine-b", Name: manageEnvironmentsToolName, VerifiedEvidence: true,
		Arguments: json.RawMessage(`{"mode":"preflight","implementation":"Engine B"}`),
	}
	authority := askUserCompletedToolEvidenceAuthority(call, `{
		"ok":true,"mode":"preflight","implementation":"Engine B: pocket-conditioned generation",
		"resource_requirements_verified":true,
		"status":"capacity_checked","feasible":false,"setup_state":"new_setup_blocked",
		"blockers":["insufficient_accelerator_memory"],
		"requirements":{"min_cpu_cores":8,"min_memory_mb":12288,"accelerator":"required","min_accelerator_memory_mb":6144}
	}`, askUserEvidenceAuthority{Class: askUserEvidenceCompletedTool, Ordinal: 4})
	questions := []askUserQuestion{{
		Question: "Which implementation should be used?",
		Options: []askUserQuestionOption{{
			Label: "Engine B", Description: "Use Engine B.",
			Metadata: map[string]any{
				"implementation":     "Engine B",
				"decision_evidence":  []string{askUserCurrentTaskEvidenceReference},
				"readiness_evidence": []string{},
				"readiness_status":   "unverified",
				"selection_basis":    "scientific_evidence",
				"recommended":        true,
				"resources":          map[string]any{"cpu": "unknown", "memory": "unknown", "gpu": "unknown"},
			},
		}},
	}}
	authorities := map[string]askUserEvidenceAuthority{
		askUserCurrentTaskEvidenceReference: {Class: askUserEvidenceUserObjective},
		"tool-call:preflight-engine-b":      authority,
	}
	normalized := normalizeAskUserEvidenceAuthorities(questions, authorities)
	metadata := normalized[0].Options[0].Metadata
	if got := strings.Join(stringArrayValue(metadata["decision_evidence"]), "|"); got != askUserCurrentTaskEvidenceReference+"|tool-call:preflight-engine-b" {
		t.Fatalf("decision evidence=%q", got)
	}
	resources := mapValue(metadata["resources"])
	if stringValue(resources["cpu"]) != "8 cores" || stringValue(resources["memory"]) != "12 GB" ||
		stringValue(resources["gpu"]) != "required, 6 GB VRAM" {
		t.Fatalf("resources=%#v", resources)
	}
	if stringValue(metadata["selection_basis"]) != "scientific_evidence" || !boolValue(metadata["recommended"], false) {
		t.Fatalf("preflight-backed recommendation was not preserved: %#v", metadata)
	}
	if boolValue(metadata["preflight_feasible"], true) ||
		stringValue(metadata["preflight_status"]) != "capacity_checked" ||
		stringValue(metadata["preflight_setup_state"]) != "new_setup_blocked" ||
		strings.Join(stringArrayValue(metadata["preflight_blockers"]), "|") != "insufficient_accelerator_memory" {
		t.Fatalf("preflight continuity evidence was not preserved: %#v", metadata)
	}
}

func TestAskUserPreflightDoesNotPublishUnsourcedResourceNumbersOrCapacityBlockers(t *testing.T) {
	call := agentruntime.ToolCall{
		ID: "preflight-unsourced", Name: managePackagesToolName, VerifiedEvidence: true,
		Arguments: json.RawMessage(`{"mode":"preflight","implementation":"Engine U"}`),
	}
	authority := askUserCompletedToolEvidenceAuthority(call, `{
		"ok":true,"mode":"preflight","implementation":"Engine U",
		"feasible":false,"setup_state":"new_setup_blocked","blockers":["insufficient_memory"],
		"requirements":{"min_cpu_cores":64,"min_memory_mb":262144,"accelerator":"required"}
	}`, askUserEvidenceAuthority{Class: askUserEvidenceCompletedTool})
	if authority.Resources == nil || authority.Resources.CPU != "unresolved" ||
		authority.Resources.Memory != "unresolved" || authority.Resources.GPU != "unresolved" {
		t.Fatalf("unsourced resource numbers were published: %#v", authority)
	}
	if authority.Feasible != nil || authority.SetupState != "" || len(authority.Blockers) != 0 {
		t.Fatalf("unsourced capacity claim became blocking authority: %#v", authority)
	}
}

func TestAskUserCompletedToolEvidenceAuthorityIgnoresFailedPreflight(t *testing.T) {
	call := agentruntime.ToolCall{
		ID: "failed-preflight", Name: managePackagesToolName,
		Arguments: json.RawMessage(`{"mode":"preflight","implementation":"Engine C"}`),
	}
	authority := askUserCompletedToolEvidenceAuthority(
		call, `{"ok":false,"mode":"preflight","implementation":"Engine C"}`,
		askUserEvidenceAuthority{Class: askUserEvidenceCompletedTool},
	)
	if authority.Implementation != "" || authority.Resources != nil {
		t.Fatalf("failed preflight became implementation authority: %#v", authority)
	}
}

func TestNormalizeAskUserEvidenceAuthoritiesSeparatesTaskFitFromExecutionReadiness(t *testing.T) {
	questions, err := askUserQuestionValue(map[string]any{
		"question": "Which route should be used?", "header": "Route",
		"options": []any{
			askUserDecisionOption(
				"Objective fit", "Choose by the stated objective.", "Matches the requested scope.", "Readiness is not yet known.",
				"Execution readiness has not been checked.", []any{askUserCurrentTaskEvidenceReference}, "A preflight is still required.",
				"The objective-aligned route is selected before preflight.", "Recommended only from the user's stated objective.", true,
			),
			askUserDecisionOption(
				"Evidence-backed route", "Choose the route supported by a completed analysis action.", "Has a live task receipt.", "Execution readiness is not established.",
				"The completed action is decision evidence only.", []any{"tool-call:analysis-ok"}, "A separate readiness preflight remains required.",
				"The evidence-backed route is selected before readiness verification.", "Choose when current scientific evidence is the primary criterion.", false,
			),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	authorities := map[string]askUserEvidenceAuthority{
		askUserCurrentTaskEvidenceReference: {Class: askUserEvidenceUserObjective},
		"tool-call:analysis-ok":             {Class: askUserEvidenceCompletedTool},
	}
	normalized := normalizeAskUserEvidenceAuthorities(questions, authorities)
	if got := stringValue(normalized[0].Options[0].Metadata["selection_basis"]); got != "user_objective" {
		t.Fatalf("valid objective basis=%q", got)
	}
	if got := stringValue(normalized[0].Options[1].Metadata["selection_basis"]); got != "scientific_evidence" {
		t.Fatalf("valid scientific basis=%q", got)
	}
	questions[0].Options[0].Metadata["readiness_status"] = "verified_ready"
	questions[0].Options[0].Metadata["readiness_evidence"] = []string{askUserCurrentTaskEvidenceReference}
	questions[0].Options[0].Metadata["selection_basis"] = "execution_readiness"
	normalized = normalizeAskUserEvidenceAuthorities(questions, authorities)
	overclaimed := normalized[0].Options[0].Metadata
	if got := stringValue(overclaimed["readiness_status"]); got != "unverified" {
		t.Fatalf("overclaimed readiness=%q want unverified", got)
	}
	if got := stringValue(overclaimed["selection_basis"]); got != "user_objective" {
		t.Fatalf("overclaimed selection basis=%q want user_objective", got)
	}
	if got := stringArrayValue(overclaimed["readiness_evidence"]); len(got) != 0 {
		t.Fatalf("overclaimed readiness evidence=%v want empty", got)
	}
	if boolValue(overclaimed["recommended"], false) || overclaimed["reported_recommended"] != true || overclaimed["recommendation_supported"] != false {
		t.Fatalf("unsupported runtime recommendation remained public: %#v", overclaimed)
	}
	attestationReference := "readiness-attestation:verified-preflight"
	authorities[attestationReference] = askUserEvidenceAuthority{
		Class: askUserEvidenceReadinessAttestation, Schema: askUserReadinessAttestationSchemaV1,
		ReadinessStatus: "verified_ready", Scope: "bounded representative preflight",
		ResultSHA256: strings.Repeat("a", 64),
	}
	questions[0].Options[0].Metadata["decision_evidence"] = []string{"tool-call:analysis-ok"}
	questions[0].Options[0].Metadata["readiness_evidence"] = []string{attestationReference}
	questions[0].Options[0].Metadata["selection_basis"] = "balanced_tradeoff"
	normalized = normalizeAskUserEvidenceAuthorities(questions, authorities)
	attested := normalized[0].Options[0].Metadata
	if got := stringValue(attested["readiness_status"]); got != "verified_ready" {
		t.Fatalf("attested readiness=%q", got)
	}
	if got := stringValue(attested["selection_basis"]); got != "balanced_tradeoff" {
		t.Fatalf("attested selection basis=%q", got)
	}
	if attested["recommended"] != true || attested["recommendation_supported"] != true {
		t.Fatalf("attested recommendation was not preserved: %#v", attested)
	}
}

func TestAskUserPublicProseAuthorityRejectsInternalExecutionNamesButAllowsPublicSoftware(t *testing.T) {
	for _, test := range []struct {
		text       string
		skillNames []string
		want       bool
	}{
		{text: "Use RDKit for local cheminformatics checks.", want: false},
		{text: "Measure the MCP-1 response in the assay.", want: false},
		{text: "Call MCP before starting.", want: true},
		{text: "Invoke mcp__vendor__generator.", want: true},
		{text: "Read /home/operator/private.json.", want: true},
		{text: "Load medicinal-chemistry-optimization.", skillNames: []string{"medicinal-chemistry-optimization"}, want: true},
		{text: "Use a structure-guided medicinal chemistry workflow.", skillNames: []string{"medicinal-chemistry-optimization"}, want: false},
	} {
		if got := askUserPublicProseExposesInternalIdentifier(test.text, test.skillNames); got != test.want {
			t.Errorf("text=%q got=%t want=%t", test.text, got, test.want)
		}
	}
}

func TestAskUserPublicProseAuthorityDoesNotTreatTypedImplementationAsVisibleText(t *testing.T) {
	questions := []askUserQuestion{{Options: []askUserQuestionOption{{
		Label: "Pocket2Mol 本地 GPU",
		Metadata: map[string]any{
			"implementation":    "pocket2mol-local",
			"route_description": "使用 Pocket2Mol 进行口袋条件三维生成。",
		},
	}}}}
	if issues := askUserPublicProseAuthorityIssues(questions, []string{"pocket2mol-local"}); len(issues) != 0 {
		t.Fatalf("typed implementation metadata was treated as public prose: %v", issues)
	}
	questions[0].Options[0].Label = "加载 pocket2mol-local"
	if issues := askUserPublicProseAuthorityIssues(questions, []string{"pocket2mol-local"}); len(issues) != 1 {
		t.Fatalf("visible internal Skill name was not rejected: %v", issues)
	}
}

func TestTranscriptRunnerNormalizesUnverifiedEvidenceBeforeParking(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-ask-evidence", "frame-ask-evidence")
	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sequence := requests.Add(1)
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if sequence == 1 {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "tool_calls": []any{map[string]any{
					"id": "readiness-preflight", "type": "function", "function": map[string]any{
						"name": "search_skills", "arguments": `{"query":"analysis"}`,
					},
				}},
			}}}})
			return
		}
		evidence := []any{"tool-call:readiness-preflight"}
		callID := "ask-unverified"
		if sequence > 2 {
			t.Fatalf("unexpected model request %d", sequence)
		}
		continueOption := askUserDecisionOption(
			"Continue", "Continue the requested route.", "Preserves the objective.", "Uses the stated scope.",
			"The route passed the current-task readiness check.", evidence, "No additional resources are proven.",
			"The requested route continues.", "Recommended by the cited current-task evidence.", true,
		)
		stopOption := askUserDecisionOption(
			"Stop", "Stop before execution.", "Avoids further work.", "Leaves the task incomplete.",
			"Stopping needs no additional execution environment.", []any{askUserCurrentTaskEvidenceReference}, "No additional resources.",
			"The task remains stopped.", "Choose when the objective should not continue.", false,
		)
		if sequence == 2 {
			continueOption["readiness_status"] = "verified_ready"
			continueOption["readiness_evidence"] = []any{"tool-call:readiness-preflight"}
			continueOption["selection_basis"] = "execution_readiness"
		}
		arguments := map[string]any{
			"question": "Which execution route should be used?", "header": "Route",
			"options": []any{continueOption, stopOption},
		}
		rawArguments, _ := json.Marshal(arguments)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
			"role": "assistant", "tool_calls": []any{map[string]any{
				"id": callID, "type": "function", "function": map[string]any{
					"name": "ask_user", "arguments": string(rawArguments),
				},
			}},
		}}}})
	}))
	defer modelAPI.Close()

	server := New(Options{FileRoot: t.TempDir(), Workspace: store, Transcript: repo})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-ask-evidence", MessageUUID: "message-ask-evidence",
		ClientMessageID: "client-ask-evidence", Text: "Compare the available routes and ask before choosing.",
	}); err != nil {
		t.Fatal(err)
	}
	result, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{
		SessionID: "frame-ask-evidence", RunnerID: "ask-evidence-runner",
		Endpoint: modelAPI.URL + "/v1/chat/completions", Model: "test-model",
		AllowedTools: []string{"search_skills", "ask_user"}, LeaseTTL: time.Minute, ReplayLimit: 100, MaxToolRounds: 4,
	})
	if err != nil || !result.Claimed || result.Status != "awaiting_user_response" || requests.Load() != 2 {
		t.Fatalf("result=%#v requests=%d err=%v", result, requests.Load(), err)
	}
	confirmations, err := server.webConversationPendingConfirmations("frame-ask-evidence")
	if err != nil || len(confirmations) != 1 || confirmations[0]["id"] != "ask-unverified" {
		t.Fatalf("confirmations=%#v err=%v", confirmations, err)
	}
}

func TestAvailableAgentAskUserEvidenceAuthoritiesClassifyDurableReceipts(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-ask-receipt", "frame-ask-receipt")
	if _, err := store.UpsertSSHProvider(workspace.ComputeProviderInput{
		Name: "ssh:fixture", UserID: "local", Family: "ssh",
	}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{FileRoot: t.TempDir(), Workspace: store, Transcript: repo})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-ask-receipt", MessageUUID: "message-ask-receipt",
		ClientMessageID: "client-ask-receipt", Text: "Run the bounded check, then ask which route to use.",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-ask-receipt")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "ask-receipt-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	intent, found, err := repo.EnsureActiveFrameTaskIntent(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found {
		t.Fatalf("intent=%#v found=%t err=%v", intent, found, err)
	}
	run := &sessionRunnerChatRun{
		SessionID: stream.SessionID, Attempt: int(claimed.Claim.Attempt), ClaimToken: claimed.Claim.ClaimToken,
		Transcript: &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim},
		TaskIntent: intent.Text, TaskIntentID: intent.ID, TaskIntentRevision: intent.Revision,
	}
	call := agentruntime.ToolCall{
		ID: "read-receipt", Name: "read_file",
		Arguments: json.RawMessage(`{"file_path":"receipt.txt","human_description":"Reading the bounded receipt"}`),
	}
	options := SessionRunnerChatOptions{RunnerID: claimed.Claim.RunnerID}
	if err := server.checkpointChatModelToolCalls(options, run, []agentruntime.ToolCall{call}); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"file_path": "receipt.txt", "human_description": "Reading the bounded receipt"}
	if err := server.checkpointChatTool(options, run, "running", "tool read_file started", call.ID, "start", map[string]any{
		"toolName": "read_file", "toolInput": input, "lifecyclePhase": "tool",
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.checkpointChatTool(options, run, "completed", "tool read_file completed", call.ID, "completed", map[string]any{
		"toolName": "read_file", "toolInput": input, "toolResult": map[string]any{"ok": true, "content": "verified receipt"},
		"lifecyclePhase": "tool",
	}); err != nil {
		t.Fatal(err)
	}
	allowed, err := server.availableAgentAskUserEvidenceAuthorities(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if authority, found := allowed["tool-call:read-receipt"]; !found || authority.Class != askUserEvidenceCompletedTool {
		durable, durableErr := server.sessionRunnerDurableExplicitToolContractMessages(context.Background(), run)
		projected, projectedErr := repo.ListProjectedCoordinateEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID, Limit: 100,
		})
		payloads := make([]string, 0, len(projected))
		for _, event := range projected {
			payloads = append(payloads, string(event.ResolvedPayloadJSON))
		}
		t.Fatalf("completed durable receipt not available: allowed=%#v durable=%#v durableErr=%v projectedErr=%v payloads=%v", allowed, durable, durableErr, projectedErr, payloads)
	}
	if authority, found := allowed["compute-provider:fixture"]; !found ||
		authority.Class != askUserEvidenceConfiguredProvider || authority.ReadinessStatus != "configured" {
		t.Fatalf("enabled compute provider not available: %#v", allowed)
	}
}

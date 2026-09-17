package server

import (
	"context"
	"encoding/json"
	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
	"testing"
)

func TestAskUserIdentityPreservesOpaqueVersionsAndLocations(t *testing.T) {
	for _, tc := range []struct {
		observed, proposed string
		match              bool
	}{
		{"python:3.11-slim", "python:3.12-slim", false},
		{"engine:v1", "engine:v2", false},
		{"https://host.example/a", "https://host.example/b", false},
		{"Engine A: first description", "Engine A: another description", true},
		{"engine:v1: description", "engine:v1", true},
		{"engine:v1: description", "engine:v2: description", false},
		{"Engine A:\tdescription", "Engine A", true},
		{"", "", false},
	} {
		if got := askUserImplementationIdentityMatches(tc.observed, tc.proposed); got != tc.match {
			t.Errorf("match(%q,%q)=%v want %v", tc.observed, tc.proposed, got, tc.match)
		}
	}
}

func TestAskUserCorrectionIncludesExactMissingPreflightArguments(t *testing.T) {
	run := &sessionRunnerChatRun{ImplementationSelectionRequired: true}
	result := map[string]any{"questions": []askUserQuestion{{Options: []askUserQuestionOption{
		{Metadata: map[string]any{"implementation": "engine:v1"}},
		{Metadata: map[string]any{"implementation": "engine:v2", "decision_evidence": []string{"tool-call:verified"}}},
		{Metadata: map[string]any{"implementation": "https://example.org/engine"}},
	}}}}
	correction := askUserImplementationSelectionContractCorrection(run, result)
	missing := anySliceValue(correction["required_preflights"])
	if len(missing) != 2 {
		t.Fatalf("missing actionable identities: %#v", correction)
	}
	for index, implementation := range []string{"engine:v1", "https://example.org/engine"} {
		request := mapValue(missing[index])
		arguments := mapValue(request["arguments"])
		if request["tool"] != manageEnvironmentsToolName || arguments["mode"] != "preflight" || arguments["implementation"] != implementation {
			t.Fatalf("identity changed or mutation suggested: %#v", request)
		}
		if len(arguments) != 2 {
			t.Fatalf("recovery invented a provider, package, or environment: %#v", arguments)
		}
	}
	if len(run.selectedImplementationsSnapshot()) != 0 {
		t.Fatal("proposed alternatives became user selections")
	}
}

func TestAskUserManagedPreflightRecoveryThroughDurableReceipts(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	server, identity := managedEnvironmentToolFixture(t)
	authority := &recordingManagedEnvironmentAuthority{}
	questions := []askUserQuestion{{Question: "Choose an implementation"}}
	for _, implementation := range []string{"https://host.example/EngineA", "https://host.example/enginea", "registry.example/engine:ReleaseA"} {
		callID := "preflight-" + implementation
		input := map[string]any{
			"mode": "preflight", "provider": "local-conda", "network": "egress", "implementation": implementation,
			"name": "candidate", "packages": []any{"example-library"},
			"resource_requirements": managedEnvironmentTestResources(), "human_description": "Inspect candidate resources",
		}
		result, err := server.executeAgentEnvironmentManagementToolWithAuthority(context.Background(), identity, agentruntime.ToolCall{ID: callID}, manageEnvironmentsToolName, input, authority)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(map[string]any{
			"lifecyclePhase": "tool", "toolName": manageEnvironmentsToolName, "toolPhase": "completed",
			"toolCallId": callID, "toolInput": input, "toolResult": result,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
			Claim: fixture.claim, ClientMessageID: callID, Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: payload,
		}); err != nil {
			t.Fatal(err)
		}
		questions[0].Options = append(questions[0].Options, askUserQuestionOption{Label: implementation, Metadata: map[string]any{
			"implementation": implementation, "route_description": "Analyze the supplied data",
		}})
	}
	run := &sessionRunnerChatRun{ImplementationSelectionRequired: true, Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim}}
	result := map[string]any{"questions": questions}
	if err := fixture.server.normalizeAgentAskUserDecisionEvidence(context.Background(), run, result); err != nil {
		t.Fatal(err)
	}
	if correction := askUserImplementationSelectionContractCorrection(run, result); correction != nil {
		t.Fatalf("successful exact preflights did not unlock the decision: %#v", correction)
	}
	for _, option := range result["questions"].([]askUserQuestion)[0].Options {
		implementation := stringValue(option.Metadata["implementation"])
		if !containsAskUserEvidenceReference(stringArrayValue(option.Metadata["decision_evidence"]), "tool-call:preflight-"+implementation) {
			t.Fatalf("durable identity binding lost: %#v", option.Metadata)
		}
	}
	if authority.createInput.Name != "" || len(run.selectedImplementationsSnapshot()) != 0 {
		t.Fatal("read-only recovery installed or selected an implementation")
	}
}

func TestAskUserPreflightReceiptRoundTripPreservesIdentity(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	payload, err := json.Marshal(map[string]any{
		"lifecyclePhase": "tool", "toolName": manageEnvironmentsToolName, "toolPhase": "completed",
		"toolCallId": "preflight-v1", "toolInput": map[string]any{"mode": "preflight", "implementation": "engine:v1"},
		"toolResult": map[string]any{"ok": true, "executed": false, "mode": "preflight", "implementation": "engine:v1", "status": "container_preflight_ready", "feasible": true, "resource_requirements_verified": false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: fixture.claim, ClientMessageID: "preflight-v1", Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: payload,
	}); err != nil {
		t.Fatal(err)
	}
	run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim}}
	for _, identity := range []string{"engine:v1", "engine:v2"} {
		result := map[string]any{"questions": []askUserQuestion{{Question: "Which implementation?", Options: []askUserQuestionOption{{Label: identity, Metadata: map[string]any{"implementation": identity, "route_description": "Analyze the supplied data", "preflight_feasible": false, "preflight_setup_state": "new_setup_blocked"}}}}}}
		if err := fixture.server.normalizeAgentAskUserDecisionEvidence(context.Background(), run, result); err != nil {
			t.Fatal(err)
		}
		available := anySliceValue(result["available_preflights"])
		if len(available) != 1 || stringValue(mapValue(available[0])["implementation"]) != "engine:v1" {
			t.Fatalf("durable preflight repair data was lost: %#v", available)
		}
		if identity == "engine:v2" {
			correction := askUserImplementationSelectionContractCorrection(run, result)
			if len(anySliceValue(correction["available_preflights"])) != 1 {
				t.Fatalf("correction omitted exact available identities: %#v", correction)
			}
		}
		metadata := result["questions"].([]askUserQuestion)[0].Options[0].Metadata
		hasReceipt := containsAskUserEvidenceReference(stringArrayValue(metadata["decision_evidence"]), "tool-call:preflight-v1")
		if hasReceipt != (identity == "engine:v1") {
			t.Fatalf("receipt leaked across versions: %s %#v", identity, metadata)
		}
		if _, found := metadata["preflight_feasible"]; found {
			t.Fatalf("unsupported capacity claim survived durable normalization: %#v", metadata)
		}
	}
}

func TestAskUserAvailablePreflightsKeepVersionsAndLatestReceipt(t *testing.T) {
	resources := &askUserResourceProfile{CPU: "unresolved", Memory: "unresolved", GPU: "unresolved"}
	available := askUserAvailablePreflightIdentities(map[string]askUserEvidenceAuthority{
		"tool-call:old":     {Class: askUserEvidenceCompletedTool, Implementation: "engine:v1: old", Resources: resources, Ordinal: 1},
		"tool-call:new":     {Class: askUserEvidenceCompletedTool, Implementation: "engine:v1: new", Resources: resources, Ordinal: 3},
		"tool-call:v2":      {Class: askUserEvidenceCompletedTool, Implementation: "engine:v2", Resources: resources, Ordinal: 2},
		"tool-call:unknown": {Class: askUserEvidenceCompletedTool, Implementation: "unknown"},
	})
	if len(available) != 2 || stringValue(mapValue(available[0])["evidence_ref"]) != "tool-call:new" || stringValue(mapValue(available[1])["implementation"]) != "engine:v2" {
		t.Fatalf("invalid available identities: %#v", available)
	}
}

func TestAskUserPreflightPreservesOuterFailure(t *testing.T) {
	call := agentruntime.ToolCall{Name: manageEnvironmentsToolName, Arguments: json.RawMessage(`{"mode":"preflight","implementation":"Engine A"}`)}
	for _, raw := range []string{
		`{"ok":false,"result":{"ok":true,"mode":"preflight","implementation":"Engine A"}}`,
		`{"partial":true,"result":{"ok":true,"mode":"preflight","implementation":"Engine A"}}`,
	} {
		a := askUserCompletedToolEvidenceAuthority(call, raw, askUserEvidenceAuthority{Class: askUserEvidenceCompletedTool})
		if a.Implementation != "" || a.Resources != nil {
			t.Fatalf("failed envelope became preflight authority: %#v", a)
		}
	}
	// A real read-only preflight deliberately does not execute the software.
	a := askUserCompletedToolEvidenceAuthority(call, `{"ok":true,"executed":false,"result":{"ok":true,"mode":"preflight","implementation":"Engine A"}}`, askUserEvidenceAuthority{Class: askUserEvidenceCompletedTool})
	if a.Implementation != "Engine A" || a.Resources == nil {
		t.Fatalf("valid read-only preflight discarded: %#v", a)
	}
}

func TestAskUserNormalizationClearsStaleDerivedPreflightFields(t *testing.T) {
	questions := []askUserQuestion{{Question: "Select a route", Options: []askUserQuestionOption{{Label: "Engine A", Metadata: map[string]any{
		"implementation": "Engine A", "preflight_feasible": false, "preflight_status": "capacity_checked", "preflight_setup_state": "new_setup_blocked", "preflight_blockers": []string{"insufficient_memory"},
	}}}}}
	normalized := normalizeAskUserEvidenceAuthorities(questions, map[string]askUserEvidenceAuthority{})
	for _, key := range []string{"preflight_feasible", "preflight_status", "preflight_setup_state", "preflight_blockers"} {
		if _, found := normalized[0].Options[0].Metadata[key]; found {
			t.Errorf("stale derived authority retained: %s", key)
		}
	}
	if _, found := questions[0].Options[0].Metadata["preflight_feasible"]; !found {
		t.Fatal("normalization mutated the original audit record")
	}
}

func TestAskUserCannotCiteAnotherVersionPreflightToAuthorizeOption(t *testing.T) {
	questions := []askUserQuestion{{Question: "Choose version", Options: []askUserQuestionOption{{Label: "engine:v2", Metadata: map[string]any{
		"implementation": "engine:v2", "decision_evidence": []string{"tool-call:v1"},
	}}}}}
	authorities := map[string]askUserEvidenceAuthority{"tool-call:v1": {Class: askUserEvidenceCompletedTool, Implementation: "engine:v1", Resources: &askUserResourceProfile{CPU: "unresolved", GPU: "unresolved", Memory: "unresolved"}}}
	normalized := normalizeAskUserEvidenceAuthorities(questions, authorities)
	if containsAskUserEvidenceReference(stringArrayValue(normalized[0].Options[0].Metadata["decision_evidence"]), "tool-call:v1") {
		t.Fatal("explicit wrong-version citation authorized the option")
	}
}

func TestAskUserCompletedResultSeparatesDiscoveryFromRejectedPreflight(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{`{"ok":false,"result":{"ok":true}}`, false},
		{`{"ok":true,"executed":false,"decision_required":true}`, false},
		{`{"ok":true,"result":{"executed":false,"decision_required":true}}`, false},
		{`{"ok":true,"executed":false,"mode":"preflight","feasible":true}`, true},
		{`{"ok":true,"records":[{"decision_required":true,"executed":false}]}`, true},
		{`"official software documentation"`, true},
	} {
		if got := askUserCompletedResultSupportsDecision(tc.raw); got != tc.want {
			t.Fatalf("supportsDecision(%s)=%v want %v", tc.raw, got, tc.want)
		}
	}
}

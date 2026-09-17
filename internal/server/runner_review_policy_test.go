package server

import (
	"encoding/json"
	"strings"
	"testing"

	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestResolveSessionRunnerReviewPolicyDoesNotReviewScientificTaskWhenVerifierIsOff(t *testing.T) {
	policy, err := resolveSessionRunnerReviewPolicy(sessionRunnerReviewPolicyInput{
		SessionID: "frame-medchem", StreamUID: "frame:frame-medchem", RunnerAttempt: 3,
		ClaimedInputRevision: 7, TaskIntentID: "intent-medchem", TaskIntentRevision: 2,
		RootAgent:                   "OPERON",
		UserRequestedEvidenceReview: false,
		SelectedSkillNames:          []string{"literature-review", "drug-discovery-pipeline"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if policy.EvidenceReviewRequired || len(policy.ScientificReviewerProfiles) != 0 {
		t.Fatalf("resolved policy = %#v", policy)
	}
	if policy.Authority != "server" || policy.Schema != sessionRunnerReviewPolicySchema || policy.PolicyID == "" {
		t.Fatalf("resolved policy identity = %#v", policy)
	}
	encoded, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"model"`) || strings.Contains(string(encoded), "ark-code-latest") {
		t.Fatalf("mutable model leaked into durable policy: %s", encoded)
	}
}

func TestResolveSessionRunnerReviewPolicyEnablesSingleReviewerOnlyWhenVerifierIsOn(t *testing.T) {
	policy, err := resolveSessionRunnerReviewPolicy(sessionRunnerReviewPolicyInput{
		SessionID: "frame-medchem-on", StreamUID: "frame:frame-medchem-on", RunnerAttempt: 1,
		ClaimedInputRevision: 1, RootAgent: "OPERON",
		UserRequestedEvidenceReview: true,
		SelectedSkillNames:          []string{"drug-discovery-pipeline"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !policy.EvidenceReviewRequired || len(policy.ScientificReviewerProfiles) != 0 {
		t.Fatalf("resolved policy = %#v", policy)
	}
}

func TestResolveSessionRunnerReviewPolicyUsesGeneralProfileAndExcludesFixedJobs(t *testing.T) {
	general, err := resolveSessionRunnerReviewPolicy(sessionRunnerReviewPolicyInput{
		SessionID: "frame-general", StreamUID: "frame:frame-general", RunnerAttempt: 1,
		ClaimedInputRevision: 1, RootAgent: "OPERON",
		UserRequestedEvidenceReview: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !general.EvidenceReviewRequired || len(general.ScientificReviewerProfiles) != 0 {
		t.Fatalf("general policy = %#v", general)
	}

	fixedJob, err := resolveSessionRunnerReviewPolicy(sessionRunnerReviewPolicyInput{
		SessionID: "frame-reviewer", StreamUID: "frame:frame-reviewer", RunnerAttempt: 1,
		ClaimedInputRevision: 1, RootAgent: "REVIEWER",
		UserRequestedEvidenceReview: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fixedJob.EvidenceReviewRequired || len(fixedJob.ScientificReviewerProfiles) != 0 {
		t.Fatalf("fixed-job policy = %#v", fixedJob)
	}
}

func TestSessionRunnerReviewPolicyDigestRejectsTampering(t *testing.T) {
	policy, err := resolveSessionRunnerReviewPolicy(sessionRunnerReviewPolicyInput{
		SessionID: "frame-digest", StreamUID: "frame:frame-digest", RunnerAttempt: 2,
		ClaimedInputRevision: 4, RootAgent: "OPERON",
		UserRequestedEvidenceReview: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	policy.EvidenceReviewRequired = false
	if err := validateSessionRunnerResolvedReviewPolicy(policy); err == nil {
		t.Fatal("tampered review policy was accepted")
	}
}

func TestSessionRunnerReviewPolicyRestoresOnlyExactTranscriptAuthority(t *testing.T) {
	policy, err := resolveSessionRunnerReviewPolicy(sessionRunnerReviewPolicyInput{
		SessionID: "frame-restore", StreamUID: "frame:frame-restore", RunnerAttempt: 5,
		ClaimedInputRevision: 9, TaskIntentID: "intent-restore", TaskIntentRevision: 3,
		RootAgent: "OPERON", UserRequestedEvidenceReview: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	var projected map[string]any
	if err := json.Unmarshal(encoded, &projected); err != nil {
		t.Fatal(err)
	}
	entry := eventjournal.Entry{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "completed", "toolPhase": "review_policy",
		"resolvedReviewPolicy": projected,
	}}
	run := &sessionRunnerChatRun{
		SessionID: "frame-restore", Attempt: 5, TaskIntentID: "intent-restore", TaskIntentRevision: 3,
		Transcript: &transcriptRunnerAuthority{
			Stream: transcriptstore.Stream{UID: "frame:frame-restore"},
			Claim:  transcriptstore.RunnerClaim{ClaimedInputRevision: 9},
		},
	}
	restored, found, err := sessionRunnerReviewPolicyFromEntries([]eventjournal.Entry{entry}, run)
	if err != nil || !found || restored.PolicyID != policy.PolicyID {
		t.Fatalf("restored=%#v found=%t err=%v", restored, found, err)
	}
	run.TaskIntentRevision++
	if _, found, err := sessionRunnerReviewPolicyFromEntries([]eventjournal.Entry{entry}, run); err != nil || found {
		t.Fatalf("stale task policy found=%t err=%v", found, err)
	}
}

func TestResolveSessionRunnerReviewPolicyForRunDropsStalePolicyWhenVerifierIsOff(t *testing.T) {
	policy, err := resolveSessionRunnerReviewPolicy(sessionRunnerReviewPolicyInput{
		SessionID: "frame-disabled-restore", StreamUID: "frame:frame-disabled-restore", RunnerAttempt: 2,
		ClaimedInputRevision: 4, RootAgent: "OPERON",
		UserRequestedEvidenceReview: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	projected, err := sessionRunnerReviewPolicyProjection(policy)
	if err != nil {
		t.Fatal(err)
	}
	entries := []eventjournal.Entry{{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "completed", "toolPhase": "review_policy",
		"resolvedReviewPolicy": projected,
	}}}
	run := &sessionRunnerChatRun{
		SessionID: "frame-disabled-restore", Attempt: 2,
		Transcript: &transcriptRunnerAuthority{
			Stream: transcriptstore.Stream{UID: "frame:frame-disabled-restore"},
			Claim:  transcriptstore.RunnerClaim{ClaimedInputRevision: 4},
		},
		ReviewPolicy: &policy,
	}
	session := sessionstore.Session{ID: run.SessionID, Orchestration: map[string]any{
		"sessionConfig": map[string]any{"verifier_mode": "off", "resolvedReviewPolicy": projected},
	}}
	resolved, err := (&Server{}).resolveSessionRunnerReviewPolicyForRun(
		session, SessionRunnerChatOptions{}, entries, nil, run,
	)
	if err != nil {
		t.Fatal(err)
	}
	if run.ReviewPolicy != nil {
		t.Fatalf("disabled automatic review retained a stale policy: %#v", run.ReviewPolicy)
	}
	config := resolved.Orchestration["sessionConfig"].(map[string]any)
	if config["verifier_mode"] != "off" || config["resolvedReviewPolicy"] != nil {
		t.Fatalf("session verifier setting changed: %#v", resolved.Orchestration)
	}
}

func TestSessionRunnerEvidenceReviewRequiredHonorsDisabledVerifierOverStalePolicy(t *testing.T) {
	policy, err := resolveSessionRunnerReviewPolicy(sessionRunnerReviewPolicyInput{
		SessionID: "frame-off", StreamUID: "frame:frame-off", RunnerAttempt: 1,
		ClaimedInputRevision: 1, RootAgent: "OPERON", UserRequestedEvidenceReview: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	session := sessionstore.Session{ID: "frame-off", Orchestration: map[string]any{
		"sessionConfig": map[string]any{"verifierMode": "off"},
	}}
	required, err := sessionRunnerEvidenceReviewRequired(session, &sessionRunnerChatRun{ReviewPolicy: &policy})
	if err != nil || required {
		t.Fatalf("disabled verifier required review=%t err=%v", required, err)
	}
}

func TestSessionRunnerReviewPolicyCarriesMandatoryGateAcrossSameInputRetry(t *testing.T) {
	prior, err := resolveSessionRunnerReviewPolicy(sessionRunnerReviewPolicyInput{
		SessionID: "frame-retry", StreamUID: "frame:frame-retry", RunnerAttempt: 7,
		ClaimedInputRevision: 6, TaskIntentID: "intent-retry", TaskIntentRevision: 5,
		RootAgent: "OPERON", UserRequestedEvidenceReview: true,
		TrustedScientificSignals: []string{"source-connector:bundled:chembl"},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(prior)
	if err != nil {
		t.Fatal(err)
	}
	var projected map[string]any
	if err := json.Unmarshal(encoded, &projected); err != nil {
		t.Fatal(err)
	}
	entries := []eventjournal.Entry{{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "completed", "toolPhase": "review_policy",
		"resolvedReviewPolicy": projected,
	}}}
	run := &sessionRunnerChatRun{
		SessionID: "frame-retry", Attempt: 8, TaskIntentID: "intent-retry", TaskIntentRevision: 5,
		Transcript: &transcriptRunnerAuthority{
			Stream: transcriptstore.Stream{UID: "frame:frame-retry"},
			Claim:  transcriptstore.RunnerClaim{ClaimedInputRevision: 6},
		},
	}
	carried, found, err := sessionRunnerReviewPolicyForLogicalInputFromEntries(entries, run)
	if err != nil || !found || carried.PolicyID != prior.PolicyID {
		t.Fatalf("carried=%#v found=%t err=%v", carried, found, err)
	}
	rebound, err := rebindSessionRunnerReviewPolicyForRetry(carried, run)
	if err != nil {
		t.Fatal(err)
	}
	if rebound.RunnerAttempt != 8 ||
		!rebound.EvidenceReviewRequired || len(rebound.ScientificReviewerProfiles) != 0 || rebound.PolicyID == prior.PolicyID {
		t.Fatalf("rebound policy=%#v", rebound)
	}

	run.Transcript.Claim.ClaimedInputRevision++
	if _, found, err := sessionRunnerReviewPolicyForLogicalInputFromEntries(entries, run); err != nil || found {
		t.Fatalf("prior-input policy crossed revision: found=%t err=%v", found, err)
	}
}

func TestSessionRunnerReviewPolicyMigratesAuthenticatedV2WithoutModelIdentity(t *testing.T) {
	current, err := resolveSessionRunnerReviewPolicy(sessionRunnerReviewPolicyInput{
		SessionID: "frame-v2", StreamUID: "frame:frame-v2", RunnerAttempt: 4,
		ClaimedInputRevision: 6, TaskIntentID: "intent-v2", TaskIntentRevision: 2,
		RootAgent: "OPERON", UserRequestedEvidenceReview: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	legacy := sessionRunnerResolvedReviewPolicyV2{
		sessionRunnerResolvedReviewPolicy: current,
		Model:                             "ark-code-latest",
	}
	legacy.Schema = previousSessionRunnerReviewPolicySchema
	legacy.PolicyID = sessionRunnerReviewPolicyV2ID(legacy)
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var projected map[string]any
	if err := json.Unmarshal(encoded, &projected); err != nil {
		t.Fatal(err)
	}
	entry := eventjournal.Entry{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "completed", "toolPhase": "review_policy",
		"resolvedReviewPolicy": projected,
	}}
	run := &sessionRunnerChatRun{
		SessionID: "frame-v2", Attempt: 4, TaskIntentID: "intent-v2", TaskIntentRevision: 2,
		Transcript: &transcriptRunnerAuthority{
			Stream: transcriptstore.Stream{UID: "frame:frame-v2"},
			Claim:  transcriptstore.RunnerClaim{ClaimedInputRevision: 6},
		},
	}
	migrated, found, err := sessionRunnerReviewPolicyFromEntries([]eventjournal.Entry{entry}, run)
	if err != nil || !found {
		t.Fatalf("migrated=%#v found=%t err=%v", migrated, found, err)
	}
	if migrated.Schema != sessionRunnerReviewPolicySchema || migrated.PolicyID == legacy.PolicyID {
		t.Fatalf("v2 policy was not rebound to model-independent v3: %#v", migrated)
	}
	migratedEncoded, err := json.Marshal(migrated)
	if err != nil || strings.Contains(string(migratedEncoded), `"model"`) {
		t.Fatalf("migrated policy retained model identity: %s err=%v", migratedEncoded, err)
	}

	legacy.Model = "deepseek-v4-flash"
	tampered, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrateSessionRunnerReviewPolicyV2(tampered); err == nil {
		t.Fatal("tampered v2 policy was accepted")
	}
}

func TestSessionRunnerReviewPolicyQuarantinesLegacyCurrentInputSignals(t *testing.T) {
	legacyPolicy := map[string]any{
		"schema":               legacySessionRunnerReviewPolicySchema,
		"resolvedReviewPolicy": true,
	}
	legacyEntry := eventjournal.Entry{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "completed", "toolPhase": "review_policy",
		"runnerAttempt": 8, "resolvedReviewPolicy": legacyPolicy,
	}}
	oldScientificTool := eventjournal.Entry{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
		"runnerAttempt": 8, "toolName": "mcp__chembl__drug_search",
		trustedScientificReviewSignalsField: []string{"source-connector:bundled:chembl"},
	}}
	currentStart := eventjournal.Entry{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "running", "runnerAttempt": 14,
	}}
	entries := []eventjournal.Entry{legacyEntry, oldScientificTool, currentStart}

	quarantine, err := runnerEntriesRequireLegacyReviewPolicyQuarantine(entries)
	if err != nil || !quarantine {
		t.Fatalf("legacy quarantine=%t err=%v", quarantine, err)
	}
	current := filterRunnerEntriesByAttempt(entries, 14)
	if len(current) != 1 || len(trustedScientificReviewSignalsFromRunnerEntries(current)) != 0 {
		t.Fatalf("legacy scientific signals crossed retry: %#v", current)
	}
	if _, present, err := sessionRunnerReviewPolicyFromEntry(legacyEntry); err != nil || present {
		t.Fatalf("legacy policy restored: present=%t err=%v", present, err)
	}

	currentPolicy, err := resolveSessionRunnerReviewPolicy(sessionRunnerReviewPolicyInput{
		SessionID: "frame-current", StreamUID: "frame:frame-current", RunnerAttempt: 14,
		ClaimedInputRevision: 7, RootAgent: "OPERON",
		TrustedScientificSignals: []string{"source-connector:bundled:chembl"},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(currentPolicy)
	if err != nil {
		t.Fatal(err)
	}
	var projected map[string]any
	if err := json.Unmarshal(encoded, &projected); err != nil {
		t.Fatal(err)
	}
	entries = append(entries, eventjournal.Entry{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "completed", "toolPhase": "review_policy",
		"runnerAttempt": 14, "resolvedReviewPolicy": projected,
	}})
	quarantine, err = runnerEntriesRequireLegacyReviewPolicyQuarantine(entries)
	if err != nil || quarantine {
		t.Fatalf("current scoped policy quarantined: quarantine=%t err=%v", quarantine, err)
	}
}

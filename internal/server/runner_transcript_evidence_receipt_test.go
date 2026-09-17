package server

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestArtifactReferenceCorrectionCarriesOnlyTrustedScientificEvidenceReceipt(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-evidence-receipt", "frame-evidence-receipt")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-evidence-receipt", MessageUUID: "message-evidence-receipt",
		ClientMessageID: "client-evidence-receipt", Text: "complete the chemistry calculation",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-evidence-receipt")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "evidence-receipt-attempt-1",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	appendRunnerToolCheckpoint(t, repo, claimed.Claim, "evidence-source-complete", map[string]any{
		"status": "completed", "toolPhase": "completed", "toolCallId": "source-call",
		"toolName": "download_public_scientific_file", "toolResult": map[string]any{"ok": true},
		trustedScientificReviewSignalsField: []any{trustedScientificValidatedPublicDownloadSignal},
	})
	appendRunnerToolCheckpoint(t, repo, claimed.Claim, "evidence-runtime-complete", map[string]any{
		"status": "completed", "toolPhase": "completed", "toolCallId": "runtime-call",
		"toolName": softwareRuntimeToolName, "toolResult": map[string]any{"ok": true},
		trustedScientificReviewSignalsField: []any{"execution-tool:software_runtime"},
	})
	appendRunnerPayloadEvent(t, repo, claimed.Claim, "evidence-rejected-assistant", "assistant_message", map[string]any{
		"content": "untrusted prior answer with {{artifact:stale-version}}",
	})
	finishPayload, err := json.Marshal(map[string]any{
		"status":            "failed",
		"detail":            "runner completion reference integrity failed (unresolved_artifacts=1 malformed_artifact_references=0 unsupported_citations=0)",
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "evidence-reference-failure", Status: "failed", PayloadJSON: finishPayload,
	}); err != nil || !created {
		t.Fatalf("finish created=%t err=%v", created, err)
	}

	resumeClaim := claimed.Claim
	resumeClaim.Attempt = claimed.Claim.Attempt + 1
	resumeClaim.RunnerID = "evidence-receipt-attempt-2"
	resumeClaim.ResumeSource = transcriptstore.ResumeSourceCheckpoint
	authority := &transcriptRunnerAuthority{Stream: stream, Claim: resumeClaim}
	entries, err := server.loadTranscriptRunnerReplay(context.Background(), authority, 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	wantSignals := []string{
		"execution-tool:software_runtime",
		"scientific-tool:download_public_scientific_file",
		trustedScientificValidatedPublicDownloadSignal,
	}
	if got := trustedScientificReviewSignalsFromRunnerEntries(entries); !reflect.DeepEqual(got, wantSignals) {
		t.Fatalf("trusted correction signals=%#v want=%#v entries=%#v", got, wantSignals, entries)
	}
	messages, err := sessionEntriesToProviderMessages("system", entries)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.Role == "tool" || strings.Contains(message.Content, "untrusted prior answer") {
			t.Fatalf("correction replay leaked prior provider content: %#v", messages)
		}
	}
	scoped, err := server.runnerEntriesForCurrentLogicalInput(context.Background(), entries, &sessionRunnerChatRun{
		Attempt: int(resumeClaim.Attempt), Transcript: authority,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := trustedScientificReviewSignalsFromRunnerEntries(scoped); !reflect.DeepEqual(got, wantSignals) {
		t.Fatalf("task-scoped correction signals=%#v want=%#v entries=%#v", got, wantSignals, scoped)
	}
}

func TestLongTaskReplayCarriesDurableScientificStateOutsideProviderWindow(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-long-evidence", "frame-long-evidence")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-long-evidence", MessageUUID: "message-long-evidence",
		ClientMessageID: "client-long-evidence", Text: "run a long chemistry calculation with public parameters",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-long-evidence")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "long-evidence-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	sourceURL := "https://webbook.nist.gov/cgi/cbook.cgi?ID=C64175&Mask=4"
	appendRunnerToolCheckpoint(t, repo, claimed.Claim, "long-evidence-source", map[string]any{
		"status": "completed", "toolPhase": "completed", "toolCallId": "long-source-call", "toolName": "WebFetch",
		"toolInput":  map[string]any{"url": sourceURL},
		"toolResult": map[string]any{"code": float64(200), "url": sourceURL, "result": "NIST Chemistry WebBook"},
	})
	appendRunnerToolCheckpoint(t, repo, claimed.Claim, "long-evidence-skill", map[string]any{
		"status": "completed", "toolPhase": "completed", "toolCallId": "long-skill-call", "toolName": "Skill",
		"toolInput":                      map[string]any{"skill": "phase-equilibrium"},
		"requiredScientificCapabilities": []any{"thermodynamic-equilibrium"},
		"toolResult":                     map[string]any{"ok": true},
	})
	appendRunnerToolCheckpoint(t, repo, claimed.Claim, "long-evidence-runtime", map[string]any{
		"status": "completed", "toolPhase": "completed", "toolCallId": "long-runtime-call", "toolName": softwareRuntimeToolName,
		"toolResult": map[string]any{"ok": true},
		trustedScientificReviewSignalsField: []any{
			"execution-tool:software_runtime", "capability-execution:thermodynamic-equilibrium",
		},
	})
	for index := 0; index < 240; index++ {
		appendRunnerToolCheckpoint(t, repo, claimed.Claim, fmt.Sprintf("long-evidence-audit-%03d", index), map[string]any{
			"status": "running", "audit": index,
		})
	}

	authority := &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim}
	entries, err := server.loadTranscriptRunnerReplay(context.Background(), authority, 20, 20)
	if err != nil {
		t.Fatal(err)
	}
	providerMessages, err := sessionEntriesToProviderMessages("system", entries)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range providerMessages {
		if strings.Contains(message.Content, "NIST Chemistry WebBook") || message.ToolCallID != "" {
			t.Fatalf("private durable evidence leaked into provider replay: %#v", providerMessages)
		}
	}

	run := &sessionRunnerChatRun{Attempt: int(claimed.Claim.Attempt), Transcript: authority}
	scoped, err := server.runnerEntriesForCurrentLogicalInput(context.Background(), entries, run)
	if err != nil {
		t.Fatal(err)
	}
	run.addTrustedScientificReviewSignals(trustedScientificReviewSignalsFromRunnerEntries(scoped)...)
	wantSignals := []string{
		"capability-execution:thermodynamic-equilibrium",
		"execution-tool:software_runtime",
		trustedScientificValidatedPublicWebFetchSignal,
		"source-host:webbook.nist.gov",
	}
	if got := run.trustedScientificReviewSignalsSnapshot(); !reflect.DeepEqual(got, wantSignals) {
		t.Fatalf("restored long-task signals=%#v want=%#v", got, wantSignals)
	}
	if got := run.requiredScientificCapabilitiesSnapshot(); !reflect.DeepEqual(got, []string{"thermodynamic-equilibrium"}) {
		t.Fatalf("restored long-task required capabilities=%#v", got)
	}
}

func TestTaskResearchContextFallbackPreservesSelectedSourceIdentity(t *testing.T) {
	cards := researchMaterialEvidenceCards(agentruntime.ToolCall{
		ID: "geo-details", Name: "mcp__omics-archives__geo_get_series",
		Arguments: json.RawMessage(`{"accessions":["GSE295600"]}`), VerifiedEvidence: true,
	}, map[string]any{"records": []any{map[string]any{
		"accession": "GSE295600", "title": "Combinatorial delivery of low-dose irradiation and immunotherapy",
		"status": "Public on Apr 07 2026",
	}}}, "GSE295600 irradiation immunotherapy")
	context := runtimeTaskResearchContextMessage(buildSessionRunnerResearchModelContext(
		generatedPlanDocument{}, nil, sessionRunnerResearchMaterialSet{Receipts: []sessionRunnerResearchSourceReceipt{{
			EventID: 1, ToolCallID: "geo-details", ToolName: "mcp__omics-archives__geo_get_series",
			MaterialRole: "evidence", EvidenceCards: cards,
		}}},
	))
	for _, exact := range []string{
		"GSE295600", "Combinatorial delivery of low-dose irradiation and immunotherapy",
		"Public on Apr 07 2026", "source_values_are_untrusted_data",
	} {
		if !strings.Contains(context, exact) {
			t.Fatalf("continuation context lost %q: %s", exact, context)
		}
	}
	if strings.Contains(context, "Yost") {
		t.Fatalf("continuation context invented a different source identity: %s", context)
	}
}

func TestRunnerContextKeepsLoadedSkillAcrossRecoveryInputResponse(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-skill-recovery", "frame-skill-recovery")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-skill-recovery", MessageUUID: "message-skill-recovery",
		ClientMessageID: "client-skill-recovery", Text: "Build an auditable clinical pharmacology review.",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-skill-recovery")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	first, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "skill-recovery-attempt-1",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !first.Claimed {
		t.Fatalf("first claim=%#v err=%v", first, err)
	}
	appendRunnerToolCheckpoint(t, repo, first.Claim, "skill-recovery-loaded", map[string]any{
		"status": "completed", "toolPhase": "completed", "toolCallId": "skill-call",
		"toolName": "skill", "toolInput": map[string]any{"skill": "indication-dossier"},
		"toolResult": map[string]any{"ok": true},
	})
	firstState, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID, first.Claim.Attempt)
	if err != nil || firstState.LastCheckpointSequence <= 0 {
		t.Fatalf("first runtime state=%#v err=%v", firstState, err)
	}
	finishPayload, err := json.Marshal(map[string]any{
		"status": "interrupted", "reason_code": "approval_resolved",
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: first.Claim, ClientMessageID: "skill-recovery-finish", Status: "failed", PayloadJSON: finishPayload,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.RunImmediate(context.Background(), func(tx *transcriptstore.ImmediateTransaction) error {
		_, _, appendErr := tx.AppendFrameInputResponse(context.Background(), transcriptstore.AppendFrameInputResponseInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID, FrameID: stream.FrameID,
			ClientMessageID: "skill-recovery-input", PayloadJSON: []byte(`{"role":"user","messageOrigin":"input_response","text":"approval resolved"}`),
		})
		return appendErr
	}); err != nil {
		t.Fatal(err)
	}
	stream, err = repo.GetStream(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || stream.InputRevision <= first.Claim.ClaimedInputRevision {
		t.Fatalf("recovery input revision did not advance: stream=%#v err=%v", stream, err)
	}
	resumeClaim := first.Claim
	resumeClaim.Attempt++
	resumeClaim.RunnerID = "skill-recovery-attempt-2"
	resumeClaim.ClaimedInputRevision = stream.InputRevision
	resumeClaim.ResumeSource = transcriptstore.ResumeSourceCheckpoint
	resumeClaim.ResumeCheckpoint = firstState.LastCheckpointSequence
	replayAuthority := &transcriptRunnerAuthority{Stream: stream, Claim: first.Claim}
	entries, err := server.loadTranscriptRunnerReplay(context.Background(), replayAuthority, 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	authority := &transcriptRunnerAuthority{Stream: stream, Claim: resumeClaim}
	scoped, err := server.runnerEntriesForCurrentLogicalInput(context.Background(), entries, &sessionRunnerChatRun{
		Attempt: int(resumeClaim.Attempt), Transcript: authority,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := completedSkillNamesFromRunnerEntries(scoped); !reflect.DeepEqual(got, []string{"indication-dossier"}) {
		t.Fatalf("loaded Skill was lost across recovery input response: %#v", got)
	}
}

package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func seedAnsweredTaskIntake(t *testing.T, server *Server, ownerID, frameID string) {
	t.Helper()
	stream, found, err := server.transcriptStore.GetFrameStreamBySession(context.Background(), ownerID, frameID)
	if err != nil || !found {
		t.Fatalf("load intake stream found=%t err=%v", found, err)
	}
	if _, _, created, err := server.transcriptStore.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		ClientMessageID: frameID + "-intake-answer", FrameEventID: frameID + "-intake-answer-event",
		MessageUUID: frameID + "-intake-answer-message", Text: "Use the recommended scope.",
		MessageOrigin: "input_response",
	}); err != nil || !created {
		t.Fatalf("append intake answer created=%t err=%v", created, err)
	}
	if _, _, err := server.transcriptStore.EnsureActiveFrameTaskIntent(context.Background(), stream.UID, stream.OwnerID); err != nil {
		t.Fatalf("ensure intake task intent: %v", err)
	}
}

func TestBuildSessionReviewerPromptUsesCanonicalTaskIntent(t *testing.T) {
	prompt := buildSessionReviewerPrompt(
		sessionstore.Session{ID: "frame"},
		[]agentruntime.Message{
			{Role: "user", Content: "real research task"},
			{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "source-call", Name: "mcp__pubmed__search_articles"}}},
			{Role: "tool", ToolCallID: "source-call", Content: `{"ok":true}`},
			{Role: "user", Content: "[harness notice] verifier_mode=on"},
		},
		agentruntime.RunResult{FinalMessage: agentruntime.Message{Role: "assistant", Content: "candidate"}},
		sessionRunnerTaskContract{},
		"NEK7 靶点开发报告、专利全景与竞品分析",
		sessionReviewerWorkspaceEvidence{},
		map[string]any{"schema": sessionReviewerEvidenceSchema},
	)
	if !strings.Contains(prompt, "USER REQUEST:\nNEK7 靶点开发报告、专利全景与竞品分析") ||
		strings.Contains(prompt, "[harness notice]") ||
		!strings.Contains(prompt, `"mcp__pubmed__search_articles":{"calls":1,"results":1}`) {
		t.Fatalf("reviewer prompt did not isolate canonical task intent: %s", prompt)
	}
}

func TestBuildSessionReviewerPromptUsesTraceWorkflow(t *testing.T) {
	prompt := buildSessionReviewerPrompt(
		sessionstore.Session{ID: "frame"},
		[]agentruntime.Message{{Role: "user", Content: "review the report"}},
		agentruntime.RunResult{FinalMessage: agentruntime.Message{Role: "assistant", Content: "candidate"}},
		sessionRunnerTaskContract{}, "review the report",
		sessionReviewerWorkspaceEvidence{
			ArtifactsAvailable: true,
			Artifacts: []sessionReviewerArtifactEvidence{{
				ArtifactID: "artifact", Name: "report.md", VersionID: "version",
			}},
		},
		map[string]any{"schema": sessionReviewerEvidenceSchema},
	)
	for _, required := range []string{
		"Trace the bound execution record", "do not re-run the full analysis",
		"targeted representative checks on recorded inputs are allowed",
		`"name":"report.md"`, "CANDIDATE FINAL ANSWER", "Do not emit a finding solely because an artifact body retains",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("reviewer prompt missing %q: %s", required, prompt)
		}
	}
	for _, forbidden := range []string{
		"MANDATORY TOOL ORDER", "FIRST TOOL ACTION", "REQUIRED IMMUTABLE READ SET",
	} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("review prompt retained strict workflow %q: %s", forbidden, prompt)
		}
	}
}

func TestBuildSessionReviewerPromptDoesNotRequireIndependentRecomputation(t *testing.T) {
	prompt := buildSessionReviewerPrompt(
		sessionstore.Session{ID: "frame"},
		[]agentruntime.Message{{Role: "user", Content: "review the quantitative result"}},
		agentruntime.RunResult{FinalMessage: agentruntime.Message{Role: "assistant", Content: "candidate"}},
		sessionRunnerTaskContract{}, "review the quantitative result",
		sessionReviewerWorkspaceEvidence{
			ArtifactsAvailable: true,
			Artifacts: []sessionReviewerArtifactEvidence{
				{ArtifactID: "report", Name: "report.md", Kind: "text/markdown", VersionID: "version-report"},
				{ArtifactID: "results", Name: "results.csv", Kind: "text/csv", VersionID: "version-results"},
				{ArtifactID: "validation", Name: "validation.json", Kind: "application/json", VersionID: "version-validation"},
			},
		},
		map[string]any{"schema": sessionReviewerEvidenceSchema},
	)
	for _, forbidden := range []string{
		"MANDATORY INDEPENDENT QUANTITATIVE CHECK", "EVERY version_id", "independently recompute every",
	} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("reviewer prompt still requires independent recomputation %q: %s", forbidden, prompt)
		}
	}
}

func TestSessionReviewerCleanPassDoesNotRequireArtifactReadsOrRecomputation(t *testing.T) {
	binding := map[string]any{
		"stream_uid": "stream-trace", "runner_attempt": 1, "review_index": 0,
		"artifact_inventory_sha256": strings.Repeat("a", 64),
	}
	evidence := sessionReviewerWorkspaceEvidence{Artifacts: []sessionReviewerArtifactEvidence{
		{ArtifactID: "report", Name: "report.md", Kind: "text/markdown", VersionID: "version-report", ContentSHA256: strings.Repeat("b", 64)},
		{ArtifactID: "results", Name: "results.csv", Kind: "text/csv", VersionID: "version-results", ContentSHA256: strings.Repeat("c", 64)},
	}}
	scope, err := newSessionReviewerEvidenceScope(binding, evidence)
	if err != nil {
		t.Fatal(err)
	}
	valid := json.RawMessage(`{"human_description":"verified","findings":[]}`)
	if err := scope.submit("submit-traced", valid); err != nil {
		t.Fatalf("trace-only clean review required an exhaustive artifact pass: %v", err)
	}
}

func TestSessionReviewerArtifactFindingRequiresThatArtifactRead(t *testing.T) {
	binding := map[string]any{
		"stream_uid": "stream-artifact-finding", "runner_attempt": 1, "review_index": 0,
		"artifact_inventory_sha256": strings.Repeat("a", 64),
	}
	evidence := sessionReviewerWorkspaceEvidence{Artifacts: []sessionReviewerArtifactEvidence{{
		ArtifactID: "report", Name: "report.md", Kind: "text/markdown",
		VersionID: "version-report", ContentSHA256: strings.Repeat("b", 64),
	}}}
	scope, err := newSessionReviewerEvidenceScope(binding, evidence)
	if err != nil {
		t.Fatal(err)
	}
	finding := json.RawMessage(`{"human_description":"found mismatch","findings":[{"msg_idx":2,"claim":"Caption conflicts with result","verdict":"fail","evidence":"The artifact caption says 12 but the recorded result says 8.","severity":"high","artifact_version_id":"version-report"}]}`)
	if err := scope.submit("submit-before-read", finding); err == nil || !strings.Contains(err.Error(), "was not read") {
		t.Fatalf("artifact finding was accepted without tracing its exact version: %v", err)
	}
	scope.receipts["read-report"] = sessionReviewerEvidenceReceipt{VersionID: "version-report"}
	if err := scope.submit("submit-after-read", finding); err != nil {
		t.Fatalf("artifact finding with a bound read was rejected: %v", err)
	}
}

func TestRecoveredCompletionReviewCorrectionRemovesRecursiveArtifactSelfLink(t *testing.T) {
	entries := []eventjournal.Entry{{EventID: 1, Message: eventjournal.Message{
		"type": "runner_finished", "status": "failed",
		"detail": "completion reviewer rejected the current candidate; the report self-reference points to an older version",
	}}}
	context := recoveredRunnerCorrectionContext(entries)
	if !strings.Contains(context, "synon.runner_recovery.v1") ||
		!strings.Contains(context, `"required_transition":"repair_rejected_acceptance_condition"`) ||
		!strings.Contains(context, "self-reference points to an older version") ||
		!recoveredRunnerCorrectionRequiresTool(entries) {
		t.Fatalf("completion review recovery lost its structured failed condition: %q", context)
	}
}

func TestSessionRunnerReviewStageFailurePreservesCauseForRecoverableInterruption(t *testing.T) {
	infrastructure := wrapSessionRunnerReviewStageError(&sessionRunnerReviewerEvidenceUnavailableError{
		Detail: "provider returned malformed tool-call arguments after bounded retries",
	})
	var stageErr *sessionRunnerReviewStageError
	if !errors.As(infrastructure, &stageErr) || stageErr.Cause == nil ||
		!strings.Contains(infrastructure.Error(), "malformed tool-call") {
		t.Fatalf("review-stage failure lost its diagnostic cause: %v", infrastructure)
	}
	if !runnerInterruptionMayContinueSameTask(sessionRunnerCompletionReviewRecoveryReasonCode) ||
		!runnerInterruptionAutoResume(sessionRunnerCompletionReviewRecoveryReasonCode) ||
		!runnerInterruptionNeedsRecoveryBackoff(sessionRunnerCompletionReviewRecoveryReasonCode) {
		t.Fatal("review runtime outage lost nonterminal capped-backoff recovery")
	}
}

func TestSessionRunnerEmptyFinalCandidateIsNotReviewCompletion(t *testing.T) {
	cause := wrapSessionRunnerReviewStageError(&sessionRunnerReviewerEvidenceUnavailableError{
		Detail: "reviewer transport failed",
	})
	err := &sessionRunnerEmptyFinalCandidateError{Cause: cause}
	if !strings.Contains(err.Error(), "empty final candidate") && !strings.Contains(err.Error(), "user-visible final answer") {
		t.Fatalf("empty candidate error=%q", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Fatal("empty candidate did not preserve the review cause")
	}
	if !runnerInterruptionAutoResume("completion_review_correction_required") ||
		!runnerInterruptionMayContinueSameTask("completion_review_correction_required") {
		t.Fatal("reviewer correction must resume as bounded Harness feedback without user input")
	}
}

func TestCompletionReviewReviseRejectsCandidateWithoutAutoResumeLoop(t *testing.T) {
	correction := sessionRunnerCompletionReviewCorrection{
		Summary: "The requested docking result is missing.",
		Issues: []sessionRunnerReviewIssue{{
			Claim: "No docking calculation or ranked output was produced.", Severity: "high",
		}},
	}
	cause := correction.runnerCorrection()
	reason, detail := cause.ReasonCode, cause.Detail
	if reason != "completion_review_correction_required" ||
		!strings.Contains(detail, "completion reviewer rejected the current candidate") ||
		!strings.Contains(detail, "high: No docking calculation or ranked output was produced.") {
		t.Fatalf("reason=%q detail=%q", reason, detail)
	}
	if !runnerInterruptionAutoResume(reason) || !runnerInterruptionMayContinueSameTask(reason) {
		t.Fatal("review correction did not enter the bounded automatic correction path")
	}
}

func TestPersistSessionRunnerReviewLinksFindingToCitedArtifactVersion(t *testing.T) {
	store, _, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner", "review-artifact-project", "review-artifact-root")
	_, version, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "review-artifact", ProjectID: "review-artifact-project",
		Name: "CDK12_project_plan.md", Kind: "markdown",
		Content: []byte("# CDK12 project plan\n"), CreatedBy: "synon",
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{workspaceStore: store}
	artifactVersionID := version.ID
	review := sessionRunnerReview{
		Verdict: "revise", Summary: "The plan has one evidence-grounded issue.",
		Issues: []sessionRunnerReviewIssue{{
			Claim: "The external DMPK dependency is not declared", Verdict: "fail", Severity: "high",
			Evidence:          "The plan requires mouse PK without an available capability.",
			ArtifactVersionID: &artifactVersionID,
		}},
	}
	sourceRef := map[string]any{"kind": "message_span", "frame_id": "review-artifact-root", "msg_idx": 0}
	if _, err := server.persistSessionRunnerReview(
		"review-artifact-root", "review-artifact-root", "", "ark-code-latest",
		"candidate", review, 0, sourceRef,
	); err != nil {
		t.Fatal(err)
	}
	checks, err := store.ListVerificationChecks("review-artifact-root", "")
	if err != nil || len(checks) != 1 {
		t.Fatalf("verification checks=%#v err=%v", checks, err)
	}
	if checks[0].ArtifactVersionID == nil || *checks[0].ArtifactVersionID != version.ID {
		t.Fatalf("artifact version link=%#v want=%q", checks[0].ArtifactVersionID, version.ID)
	}
	artifactChecks, found, err := store.ListArtifactVerificationChecks(version.ID, "owner")
	if err != nil || !found || len(artifactChecks) != 1 || artifactChecks[0].ID != checks[0].ID {
		t.Fatalf("artifact verification checks=%#v found=%t err=%v", artifactChecks, found, err)
	}
}

func TestPersistSessionRunnerReviewKeepsReviewerPassFindingForUser(t *testing.T) {
	store, _, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner", "review-pass-project", "review-pass-root")
	server := &Server{workspaceStore: store}
	review := sessionRunnerReview{
		Verdict: "pass", Summary: "Review complete",
		Issues: []sessionRunnerReviewIssue{{
			MessageIndex: 4, Claim: "Reported total matches the execution record", Verdict: "pass",
			Evidence: "Execution log row 8 reports the same total.",
		}},
	}
	sourceRef := map[string]any{"kind": "message_span", "frame_id": "review-pass-root", "msg_idx": 4, "review_verdict": "pass"}
	if _, err := server.persistSessionRunnerReview(
		"review-pass-root", "review-pass-root", "", "review-model",
		"candidate", review, 0, sourceRef,
	); err != nil {
		t.Fatal(err)
	}
	checks, err := store.ListVerificationChecks("review-pass-root", "")
	if err != nil || len(checks) != 1 {
		t.Fatalf("verification checks=%#v err=%v", checks, err)
	}
	if checks[0].Verdict != "pass" || checks[0].Claim == nil || *checks[0].Claim != review.Issues[0].Claim ||
		checks[0].Evidence == nil || *checks[0].Evidence != review.Issues[0].Evidence || checks[0].ReviewerModel == nil {
		t.Fatalf("reviewer pass finding was replaced by a generic completion check: %#v", checks[0])
	}
}

func TestSessionRunnerReviewArtifactVersionIDRejectsCrossArtifactAttribution(t *testing.T) {
	sourceRef := map[string]any{"read_receipts": []any{
		map[string]any{"receiptId": "receipt-a", "versionId": "version-a"},
		map[string]any{"receiptId": "receipt-b", "versionId": "version-b"},
	}}
	if got := sessionRunnerReviewArtifactVersionID([]string{"receipt-a", "receipt-b"}, sourceRef); got != nil {
		t.Fatalf("cross-artifact finding was attributed to %q", *got)
	}
	if got := sessionRunnerReviewArtifactVersionID([]string{"receipt-a", "auxiliary-tool"}, sourceRef); got == nil || *got != "version-a" {
		t.Fatalf("single-artifact finding attribution=%#v", got)
	}
}

func TestSessionReviewerValidatesAnyTraceEvidenceItUses(t *testing.T) {
	content := "PMID,Journal,Year,Volume,Issue\n32242624,Biochem J,2020,477,8\n"
	digest := sha256.Sum256([]byte(content))
	evidence := sessionReviewerWorkspaceEvidence{InventoryComplete: true, ArtifactsAvailable: true, Artifacts: []sessionReviewerArtifactEvidence{{
		ArtifactID: "artifact-literature", Name: "literature_matrix.csv", Kind: "text/csv",
		VersionID: "version-literature", ContentSHA256: hex.EncodeToString(digest[:]),
	}}}
	binding := map[string]any{
		"stream_uid": "stream-review", "runner_attempt": 31, "review_index": 1,
		"artifact_inventory_sha256": strings.Repeat("a", 64),
	}
	newScope := func() *sessionReviewerEvidenceScope {
		scope, err := newSessionReviewerEvidenceScope(binding, evidence)
		if err != nil {
			t.Fatal(err)
		}
		return scope
	}
	passArguments := json.RawMessage(`{"human_description":"looks complete","findings":[]}`)
	if err := newScope().submit("submit-without-read", passArguments); err != nil {
		t.Fatalf("trace-only reviewer required an exhaustive artifact read: %v", err)
	}
	failedReview := sessionRunnerReview{Verdict: "revise", Summary: "wrong issue", Issues: []sessionRunnerReviewIssue{{Claim: "Issue is 12"}}}
	if _, err := validateSessionReviewerEvidence(failedReview, []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "read-1", Name: "read_file", Arguments: json.RawMessage(`{"version_id":"version-literature"}`),
		}}},
		{Role: "tool", ToolCallID: "read-1", Content: `{"ok":false,"error":"resource not found"}`},
		{Role: "assistant", Content: `{"verdict":"revise"}`},
	}, newScope()); err == nil || !strings.Contains(err.Error(), "read-1 failed") {
		t.Fatalf("failed reviewer evidence error = %v", err)
	}

	pass := sessionRunnerReview{Verdict: "pass", Summary: "looks complete"}
	if receipts, err := validateSessionReviewerEvidence(pass, []agentruntime.Message{
		{Role: "assistant", Content: `{"verdict":"pass"}`},
	}, newScope()); err != nil || len(receipts) != 0 {
		t.Fatalf("clean trace-only review evidence=%#v error=%v", receipts, err)
	}

	scope := newScope()
	readResult, err := scope.record("read_file", "read-2", map[string]any{"version_id": "version-literature"}, map[string]any{
		"filename": "literature_matrix.csv", "content_type": "text/csv", "size_bytes": len(content), "content": content,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	receipts := scope.receiptsSnapshot()
	if len(receipts) != 1 {
		t.Fatalf("receipts=%#v", receipts)
	}
	if err := scope.submit("submit-after-read", passArguments); err != nil {
		t.Fatalf("submission after immutable artifact trace failed: %v", err)
	}
	pass.EvidenceRefs = []string{receipts[0].ReceiptID}
	encodedRead, _ := json.Marshal(readResult)
	validated, err := validateSessionReviewerEvidence(pass, []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "read-2", Name: "read_file", Arguments: json.RawMessage(`{"version_id":"version-literature"}`),
		}}},
		{Role: "tool", ToolCallID: "read-2", Content: string(encodedRead)},
		{Role: "assistant", Content: `{"verdict":"pass"}`},
	}, scope)
	if err != nil || len(validated) != 1 || validated[0].VersionID != "version-literature" {
		t.Fatalf("validated=%#v error=%v", validated, err)
	}
	validated, err = validateSessionReviewerEvidence(pass, []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "read-2", Name: "read_file", Arguments: json.RawMessage(`{"version_id":"version-literature"}`),
		}}},
		{Role: "tool", ToolCallID: "read-2", Content: string(encodedRead)},
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "fetch-optional", Name: "web_fetch", Arguments: json.RawMessage(`{"url":"https://example.test","limit":5000}`),
		}}},
		{Role: "tool", ToolCallID: "fetch-optional", Content: `{"ok":false,"error":"web response exceeds the in-memory limit 5000"}`},
		{Role: "assistant", Content: `{"verdict":"pass"}`},
	}, scope)
	if err != nil || len(validated) != 1 {
		t.Fatalf("optional reviewer lookup invalidated bound evidence: receipts=%#v error=%v", validated, err)
	}
	policySkip := `{"filename":"literature_matrix.csv","content_type":"text/csv","size_bytes":128000,` +
		`"error":"read_file line window exceeds the output limit",` +
		`"system_hint":"Retry with a smaller limit or a later offset.","_policy_skip":true}`
	recoveredMessages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "read-too-wide", Name: "read_file", Arguments: json.RawMessage(`{"version_id":"version-literature","offset":1,"limit":500}`),
		}}},
		{Role: "tool", ToolCallID: "read-too-wide", Content: policySkip},
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "read-2", Name: "read_file", Arguments: json.RawMessage(`{"version_id":"version-literature","offset":1,"limit":20}`),
		}}},
		{Role: "tool", ToolCallID: "read-2", Content: string(encodedRead)},
	}
	if recovered, err := validateSessionReviewerEvidence(pass, recoveredMessages, scope); err != nil || len(recovered) != 1 {
		t.Fatalf("corrected bounded read was not accepted: receipts=%#v err=%v", recovered, err)
	}
	if sessionReviewerRecoveredReadRejection("read_file", "version-literature", policySkip, nil) {
		t.Fatal("unrecovered bounded read rejection was accepted without a successful receipt")
	}
	labelInput := map[string]any{"version_id": "version-literature", "file_path": "literature_matrix.csv"}
	if err := newScope().authorize("read_file", "read-label", labelInput); err != nil || labelInput["file_path"] != nil {
		t.Fatalf("reviewer rejected or retained the exact redundant artifact label: input=%#v error=%v", labelInput, err)
	}
	if err := newScope().authorize("read_file", "read-path", map[string]any{
		"version_id": "version-literature", "file_path": "/tmp/literature_matrix.csv",
	}); err == nil {
		t.Fatal("reviewer accepted a filesystem path as an artifact label")
	}
	if err := newScope().authorize("read_file", "read-label-only", map[string]any{"file_path": "literature_matrix.csv"}); err == nil {
		t.Fatal("reviewer accepted an artifact label without immutable version authority")
	}
}

func TestSessionReviewerRejectsRecursiveSelfVersionFindingAndContinuesReview(t *testing.T) {
	content := "# report\n"
	digest := sha256.Sum256([]byte(content))
	evidence := sessionReviewerWorkspaceEvidence{InventoryComplete: true, ArtifactsAvailable: true, Artifacts: []sessionReviewerArtifactEvidence{{
		ArtifactID: "artifact-report", Name: "report.md", Kind: "text/markdown",
		VersionID: "version-current", ContentSHA256: hex.EncodeToString(digest[:]),
	}}}
	binding := map[string]any{
		"stream_uid": "stream-review", "runner_attempt": 32, "review_index": 2,
		"artifact_inventory_sha256": strings.Repeat("b", 64),
	}
	scope, err := newSessionReviewerEvidenceScope(binding, evidence)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scope.record("read_file", "read-report", map[string]any{"version_id": "version-current"}, map[string]any{
		"filename": "report.md", "content_type": "text/markdown", "size_bytes": len(content), "content": content,
	}, nil); err != nil {
		t.Fatal(err)
	}
	arguments := json.RawMessage(`{"human_description":"reviewed","findings":[{"msg_idx":0,"claim":"The report self-reference points to an older version ID","verdict":"fail","evidence":"The self-reference uses the old version ID","severity":"medium","artifact_version_id":"version-current"}]}`)
	if err := scope.submit("submit-self-version", arguments); err == nil ||
		!strings.Contains(err.Error(), "inspect the remaining acceptance criteria") {
		t.Fatalf("recursive self-version finding was accepted: %v", err)
	}
	zhArguments := json.RawMessage(`{"human_description":"reviewed","findings":[{"msg_idx":0,"claim":"报告中自我引用的版本ID仍然不正确","verdict":"fail","evidence":"自我引用仍指向旧版本ID","severity":"medium","artifact_version_id":"version-current"}]}`)
	if err := scope.submit("submit-self-version-zh", zhArguments); err == nil ||
		!strings.Contains(err.Error(), "inspect the remaining acceptance criteria") {
		t.Fatalf("Chinese recursive self-version finding was accepted: %v", err)
	}
}

func TestSessionReviewerSubmissionKeepsPassFindingsForUser(t *testing.T) {
	scope, err := newSessionReviewerEvidenceScope(map[string]any{
		"stream_uid": "stream-pass-normalization", "runner_attempt": 1, "review_index": 0,
		"artifact_inventory_sha256": strings.Repeat("c", 64),
	}, sessionReviewerWorkspaceEvidence{InventoryComplete: true})
	if err != nil {
		t.Fatal(err)
	}
	arguments := json.RawMessage(`{
		"human_description":"reviewed",
		"findings":[
			{"msg_idx":0,"claim":"R002 is incomplete","verdict":"warn","evidence":"R002 has empty severity","severity":"medium","artifact_version_id":null},
			{"msg_idx":0,"claim":"source evidence passed","verdict":"pass","evidence":"all identifiers resolved","severity":"low","artifact_version_id":null}
		]
	}`)
	if err := scope.submit("submit-mixed-findings", arguments); err != nil {
		t.Fatal(err)
	}
	submission, found := scope.submissionSnapshot()
	if !found || submission.Review.Verdict != "pass" || len(submission.Review.Issues) != 2 ||
		submission.Review.Issues[0].Verdict != "warn" || submission.Review.Issues[0].Claim != "R002 is incomplete" ||
		submission.Review.Issues[1].Verdict != "pass" || submission.Review.Issues[1].Claim != "source evidence passed" {
		t.Fatalf("submission=%#v found=%t", submission, found)
	}
}

func TestSessionReviewerReadBoundsEmbeddedDataURIWithoutLosingEvidenceContext(t *testing.T) {
	content := "<html>before evidence data:image/png;base64," + strings.Repeat("A", 370_000) + " after evidence</html>"
	digest := sha256.Sum256([]byte(content))
	evidence := sessionReviewerWorkspaceEvidence{InventoryComplete: true, ArtifactsAvailable: true, Artifacts: []sessionReviewerArtifactEvidence{{
		ArtifactID: "artifact-html", Name: "synthesis_route_v2.html", Kind: "text/html",
		VersionID: "version-html", ContentSHA256: hex.EncodeToString(digest[:]),
	}}}
	scope, err := newSessionReviewerEvidenceScope(map[string]any{
		"stream_uid": "stream-large-review", "runner_attempt": 8, "review_index": 0,
		"artifact_inventory_sha256": strings.Repeat("b", 64),
	}, evidence)
	if err != nil {
		t.Fatal(err)
	}
	result, err := scope.record("read_file", "read-large-html", map[string]any{
		"version_id": "version-html", "offset": 345, "limit": 20,
	}, map[string]any{
		"filename": "synthesis_route_v2.html", "content_type": "text/html", "showing_lines": "345-364",
		"size_bytes": len(content), "content": content,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > sessionReviewerTextEvidenceMaxBytes+4096 {
		t.Fatalf("bounded reviewer result bytes=%d", len(encoded))
	}
	if strings.Contains(string(encoded), strings.Repeat("A", 1024)) ||
		!strings.Contains(string(encoded), "embedded data URI omitted") ||
		!strings.Contains(string(encoded), "before evidence") ||
		!strings.Contains(string(encoded), "after evidence") {
		t.Fatalf("bounded reviewer result lost context or retained payload: %s", string(encoded))
	}
	resultMap := sessionReviewerMapValue(result)
	originalBytes, originalBytesOK := exactPositiveInt(resultMap["original_content_bytes"])
	if resultMap["content_truncated"] != true || !originalBytesOK || originalBytes != len(content) ||
		stringValue(resultMap["original_content_sha256"]) != hex.EncodeToString(digest[:]) {
		t.Fatalf("bounded reviewer metadata=%#v", resultMap)
	}
	receipts := scope.receiptsSnapshot()
	if len(receipts) != 1 || receipts[0].Complete || !strings.HasSuffix(receipts[0].ReadScope, ";bounded") ||
		!strings.Contains(receipts[0].content, "after evidence") {
		t.Fatalf("bounded reviewer receipt=%#v", receipts)
	}
}

func TestSessionReviewerToolFailureCheckpointPersistsBoundedRedactedDiagnostic(t *testing.T) {
	event := agentruntime.Event{
		Type:       agentruntime.EventToolFailed,
		ToolName:   sessionReviewerSubmitToolName,
		ToolCallID: "submit-bad",
		Message:    `review issue 0 evidence_quote is missing; Authorization: Bearer reviewer-secret`,
		Arguments:  `{"verdict":"revise","api_key":"private-input"}`,
		Result:     `{"ok":false,"code":"invalid_review_evidence","error":"review issue 0 evidence_quote is missing; token=result-secret","retryable":true}`,
	}
	message, details := sessionReviewerToolFailureCheckpoint(event)
	encoded, err := json.Marshal(details)
	if err != nil {
		t.Fatal(err)
	}
	text := message + "\n" + string(encoded)
	for _, secret := range []string{"reviewer-secret", "result-secret", "private-input"} {
		if strings.Contains(text, secret) {
			t.Fatalf("reviewer failure checkpoint leaked %q: %s", secret, text)
		}
	}
	if !strings.Contains(message, "evidence_quote is missing") ||
		stringValue(details["failureCode"]) != "invalid_review_evidence" ||
		!strings.Contains(stringValue(details["failureMessage"]), "evidence_quote is missing") ||
		numberValue(details["toolInputBytes"]) == 0 || stringValue(details["toolInputSha256"]) == "" ||
		numberValue(details["toolResultBytes"]) == 0 || stringValue(details["toolResultSha256"]) == "" {
		t.Fatalf("reviewer failure checkpoint is not actionable: message=%q details=%#v", message, details)
	}
	result, _ := details["toolResult"].(map[string]any)
	if result["retryable"] != true || stringValue(result["error"]) == "" {
		t.Fatalf("reviewer failure result projection=%#v", result)
	}
}

func TestSessionReviewerArtifactAuthorityAdvertisesOnlyCanonicalImmutableRead(t *testing.T) {
	srv := New(Options{FileRoot: t.TempDir(), KernelManager: newServerKernelTestManager(t)})
	schemas := srv.sessionReviewerRuntimeToolSchemas(t.Context(), SessionRunnerChatOptions{
		AllowedTools: []string{"Read", "file_read", "ReadBatch", "ReadMcpResourceTool"},
	}, true)
	readCount := 0
	submitCount := 0
	replCount := 0
	for _, schema := range schemas {
		if schema.Name == sessionReviewerSubmitToolName {
			submitCount++
			properties, _ := schema.Parameters["properties"].(map[string]any)
			findings, _ := properties["findings"].(map[string]any)
			finding, _ := findings["items"].(map[string]any)
			findingProperties, _ := finding["properties"].(map[string]any)
			if schema.Name != "submit_output" || findingProperties["msg_idx"] == nil ||
				findingProperties["verdict"] == nil || findingProperties["artifact_version_id"] == nil {
				t.Fatalf("reviewer submit schema does not match the fixed-job findings contract: %#v", schema.Parameters)
			}
			verdict, _ := findingProperties["verdict"].(map[string]any)
			if values, _ := verdict["enum"].([]string); !sameStringSet(values, []string{"pass", "warn", "fail"}) {
				t.Fatalf("reviewer verdicts do not match the review contract: %#v", verdict)
			}
			continue
		}
		if legacyFrameModelFileTool(schema.Name) {
			t.Fatalf("reviewer retained mutable/legacy file authority %q", schema.Name)
		}
		if schema.Name == "repl" {
			properties, _ := schema.Parameters["properties"].(map[string]any)
			description := strings.ToLower(schema.Description)
			if properties["working_dir"] != nil || properties["background"] != nil ||
				!strings.Contains(schema.Description, "host.artifact_path") ||
				!strings.Contains(description, "trace") ||
				!strings.Contains(description, "do not re-run or independently recompute") ||
				strings.Contains(description, "try to falsify") {
				t.Fatalf("reviewer repl retained unsafe root-workspace controls: %#v", schema)
			}
			replCount++
			continue
		}
		if normalizeAgentToolName(schema.Name) != "readfile" {
			t.Fatalf("reviewer retained an unexpected tool %q", schema.Name)
		}
		readCount++
		properties, _ := schema.Parameters["properties"].(map[string]any)
		pathProperty, hasPath := properties["file_path"].(map[string]any)
		if !hasPath || !strings.Contains(stringValue(pathProperty["description"]), "never authorizes") {
			t.Fatalf("reviewer read_file lacks the bounded redundant label contract: %#v", schema.Parameters)
		}
		required, _ := schema.Parameters["required"].([]string)
		if !sameStringSet(required, []string{"version_id", "human_description"}) {
			t.Fatalf("reviewer read_file required=%#v", schema.Parameters["required"])
		}
	}
	if readCount != 1 {
		t.Fatalf("canonical reviewer read_file count=%d schemas=%#v", readCount, schemas)
	}
	if submitCount != 1 {
		t.Fatalf("private reviewer submit count=%d schemas=%#v", submitCount, schemas)
	}
	if replCount != 1 {
		t.Fatalf("private reviewer repl count=%d schemas=%#v", replCount, schemas)
	}
}

func TestSessionReviewerVerdictUsesPrivateSingleSubmissionChannel(t *testing.T) {
	binding := map[string]any{
		"stream_uid": "stream-submit", "runner_attempt": 2, "review_index": 0,
		"artifact_inventory_sha256": strings.Repeat("a", 64),
	}
	newScope := func() *sessionReviewerEvidenceScope {
		scope, err := newSessionReviewerEvidenceScope(binding, sessionReviewerWorkspaceEvidence{InventoryComplete: true})
		if err != nil {
			t.Fatal(err)
		}
		return scope
	}
	valid := json.RawMessage(`{"human_description":"verified","findings":[]}`)
	scope := newScope()
	var delegated atomic.Int32
	gateway := sessionReviewerToolGateway(agentruntime.FuncToolGateway(func(context.Context, agentruntime.ToolCall) (agentruntime.ToolResult, error) {
		delegated.Add(1)
		return agentruntime.ToolResult{Value: map[string]any{"ok": true}}, nil
	}), scope)
	terminal, err := gateway.Execute(t.Context(), agentruntime.ToolCall{ID: "submit-1", Name: sessionReviewerSubmitToolName, Arguments: valid})
	if err != nil {
		t.Fatal(err)
	}
	if !terminal.Terminal {
		t.Fatalf("successful reviewer submission was not terminal: %#v", terminal)
	}
	submission, found := scope.submissionSnapshot()
	if !found || submission.ToolCallID != "submit-1" || submission.Review.Verdict != "pass" {
		t.Fatalf("submission=%#v found=%t", submission, found)
	}
	if _, err := gateway.Execute(t.Context(), agentruntime.ToolCall{ID: "submit-2", Name: sessionReviewerSubmitToolName, Arguments: valid}); err == nil {
		t.Fatal("accepted a second reviewer submission")
	}
	if _, err := gateway.Execute(t.Context(), agentruntime.ToolCall{ID: "read-after-submit", Name: "read_file", Arguments: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("accepted a tool call after reviewer submission")
	}
	if delegated.Load() != 0 {
		t.Fatalf("delegated calls=%d", delegated.Load())
	}
	for _, invalid := range []json.RawMessage{
		json.RawMessage(`{"human_description":"verified"}`),
		json.RawMessage(`{"human_description":"verified","findings":[],"unknown":true}`),
		json.RawMessage(`{"human_description":"x","findings":[{"msg_idx":1,"claim":"x","verdict":"maybe","evidence":"x","severity":"high","artifact_version_id":null}]}`),
		json.RawMessage(`{"human_description":"x","findings":[{"msg_idx":1,"claim":"x","verdict":"fail","evidence":"","severity":"high","artifact_version_id":null}]}`),
	} {
		gateway := sessionReviewerToolGateway(nil, newScope())
		result, err := gateway.Execute(t.Context(), agentruntime.ToolCall{
			ID: "invalid", Name: sessionReviewerSubmitToolName, Arguments: invalid,
		})
		value, _ := result.Value.(map[string]any)
		if err != nil || stringValue(value["code"]) != "invalid_review_evidence" || value["retryable"] != true || result.Terminal {
			t.Fatalf("invalid reviewer submission result=%#v err=%v input=%s", result.Value, err, invalid)
		}
		result, err = gateway.Execute(t.Context(), agentruntime.ToolCall{
			ID: "invalid-correction", Name: sessionReviewerSubmitToolName, Arguments: invalid,
		})
		value, _ = result.Value.(map[string]any)
		if err != nil || stringValue(value["code"]) != "invalid_review_evidence" || value["retryable"] != false || result.Terminal {
			t.Fatalf("exhausted reviewer submission result=%#v err=%v input=%s", result.Value, err, invalid)
		}
	}
}

func TestSessionReviewerReplBudgetMatchesContract(t *testing.T) {
	binding := map[string]any{
		"stream_uid": "stream-repl-budget", "runner_attempt": 1, "review_index": 0,
		"artifact_inventory_sha256": strings.Repeat("a", 64),
	}
	scope, err := newSessionReviewerEvidenceScope(binding, sessionReviewerWorkspaceEvidence{InventoryComplete: true})
	if err != nil {
		t.Fatal(err)
	}
	var delegated atomic.Int32
	gateway := sessionReviewerToolGateway(agentruntime.FuncToolGateway(func(context.Context, agentruntime.ToolCall) (agentruntime.ToolResult, error) {
		delegated.Add(1)
		return agentruntime.ToolResult{Value: map[string]any{"ok": true}}, nil
	}), scope)
	for index := 0; index < sessionReviewerMaxReplCalls; index++ {
		result, err := gateway.Execute(t.Context(), agentruntime.ToolCall{
			ID: fmt.Sprintf("repl-%d", index), Name: "repl", Arguments: json.RawMessage(`{"code":"print(1)","human_description":"Tracing evidence"}`),
		})
		if err != nil || agentruntime.ClassifyToolResult(result.Value).Failed() {
			t.Fatalf("repl call %d result=%#v err=%v", index+1, result.Value, err)
		}
	}
	exhausted, err := gateway.Execute(t.Context(), agentruntime.ToolCall{
		ID: "repl-exhausted", Name: "repl", Arguments: json.RawMessage(`{"code":"print(1)","human_description":"Tracing evidence"}`),
	})
	value, _ := exhausted.Value.(map[string]any)
	if err != nil || stringValue(value["code"]) != "review_repl_budget_exhausted" || !agentruntime.ClassifyToolResult(exhausted.Value).Failed() {
		t.Fatalf("exhausted repl result=%#v err=%v", exhausted.Value, err)
	}
	if delegated.Load() != sessionReviewerMaxReplCalls {
		t.Fatalf("delegated repl calls=%d want=%d", delegated.Load(), sessionReviewerMaxReplCalls)
	}
}

func TestParseSessionRunnerReviewRequiresActionableSingleJSON(t *testing.T) {
	review, err := parseSessionRunnerReview("{\"human_description\":\"Review complete\",\"findings\":[{\"msg_idx\":7,\"claim\":\"Unsupported claim\",\"verdict\":\"fail\",\"evidence\":\"No source\",\"severity\":\"high\",\"artifact_version_id\":null}]}")
	if err != nil || review.Verdict != "revise" || len(review.Issues) != 1 || review.Issues[0].Severity != "high" || review.Issues[0].MessageIndex != 7 {
		t.Fatalf("review=%#v error=%v", review, err)
	}
	pass, err := parseSessionRunnerReview(`{"human_description":"Review complete","findings":[{"msg_idx":3,"claim":"Recorded total is supported","verdict":"pass","evidence":"Tool result 7 reports the same total."}]}`)
	if err != nil || pass.Verdict != "pass" || len(pass.Issues) != 1 || pass.Issues[0].Verdict != "pass" || pass.Issues[0].Severity != "" {
		t.Fatalf("review pass finding=%#v error=%v", pass, err)
	}
	for _, invalid := range []string{
		"```json\n{\"human_description\":\"x\",\"findings\":[]}\n```",
		`review: {"human_description":"x","findings":[]}`,
		`{"human_description":"x","findings":[{"msg_idx":0,"claim":"x","verdict":"maybe","evidence":"x"}]}`,
		`{"human_description":"x","findings":[{"msg_idx":0,"claim":"","verdict":"fail","evidence":"x","severity":"high","artifact_version_id":null}]}`,
		`{"human_description":"x","findings":[]} {"human_description":"x","findings":[]}`,
		`{"human_description":"x","findings":[],"unexpected":true}`,
	} {
		if _, err := parseSessionRunnerReview(invalid); err == nil {
			t.Fatalf("accepted invalid reviewer output %q", invalid)
		}
	}
}

func TestSessionRunnerVerificationSourceRefBindsCandidateAttemptAndArtifactInventory(t *testing.T) {
	run := &sessionRunnerChatRun{
		Attempt: 7,
		Transcript: &transcriptRunnerAuthority{Claim: transcriptstore.RunnerClaim{
			StreamUID: "stream-a", Attempt: 7, ClaimedInputRevision: 3,
		}},
	}
	evidence := sessionReviewerWorkspaceEvidence{InventoryComplete: true, Artifacts: []sessionReviewerArtifactEvidence{
		{ArtifactID: "artifact-report", Name: "report.md", Kind: "text/markdown", VersionID: "version-report", ContentSHA256: strings.Repeat("b", 64)},
		{ArtifactID: "artifact-claims", Name: "claims.jsonl", Kind: "application/jsonl", VersionID: "version-claims", ContentSHA256: strings.Repeat("a", 64)},
	}}
	ref, err := sessionRunnerVerificationSourceRef("root-a", "session-a", 2, "  candidate answer  ", run, evidence)
	if err != nil {
		t.Fatal(err)
	}
	if ref["schema"] != sessionReviewerEvidenceSchema || ref["root_frame_id"] != "root-a" || ref["runner_attempt"] != 7 ||
		ref["stream_uid"] != "stream-a" || numberValue(ref["claimed_input_revision"]) != 3 ||
		ref["artifact_count"] != 2 || ref["inventory_complete"] != true {
		t.Fatalf("source ref identity=%#v", ref)
	}
	if ref["candidate_sha256"] != "001928091a85daad3d89ef095d1576af028522e198f2f91c0a89abbf514cd81a" ||
		ref["artifact_inventory_sha256"] != "059a612f11a589f121d2c11c2d945dac5824fb735d021856496a53ed4841b47b" {
		t.Fatalf("candidate binding=%#v", ref["candidate_sha256"])
	}
	artifacts, ok := ref["artifact_refs"].([]sessionReviewerArtifactEvidence)
	if !ok || len(artifacts) != 2 || artifacts[0].Name != "claims.jsonl" || artifacts[1].Name != "report.md" {
		t.Fatalf("artifact binding=%#v", ref["artifact_refs"])
	}
	reordered := evidence
	reordered.Artifacts = []sessionReviewerArtifactEvidence{evidence.Artifacts[1], evidence.Artifacts[0]}
	reorderedRef, err := sessionRunnerVerificationSourceRef("root-a", "session-a", 2, "candidate answer", run, reordered)
	if err != nil {
		t.Fatal(err)
	}
	if reorderedRef["artifact_inventory_sha256"] != ref["artifact_inventory_sha256"] {
		t.Fatalf("inventory digest is order-sensitive: %#v %#v", ref, reorderedRef)
	}
	changed := reordered
	changed.Artifacts = append([]sessionReviewerArtifactEvidence(nil), reordered.Artifacts...)
	changed.Artifacts[0].VersionID = "version-claims-new"
	changedRef, err := sessionRunnerVerificationSourceRef("root-a", "session-a", 2, "candidate answer", run, changed)
	if err != nil {
		t.Fatal(err)
	}
	if changedRef["artifact_inventory_sha256"] == ref["artifact_inventory_sha256"] {
		t.Fatalf("new artifact version reused old review binding: %#v", changedRef)
	}
	if _, err := sessionRunnerVerificationSourceRef("root-a", "session-a", 2, "candidate answer", nil, evidence); err == nil {
		t.Fatal("accepted missing runner authority")
	}
	incomplete := evidence
	incomplete.InventoryComplete = false
	if _, err := sessionRunnerVerificationSourceRef("root-a", "session-a", 2, "candidate answer", run, incomplete); err == nil {
		t.Fatal("accepted incomplete artifact inventory")
	}
	duplicate := evidence
	duplicate.Artifacts = append([]sessionReviewerArtifactEvidence(nil), evidence.Artifacts...)
	duplicate.Artifacts[1].Name = duplicate.Artifacts[0].Name
	if _, err := sessionRunnerVerificationSourceRef("root-a", "session-a", 2, "candidate answer", run, duplicate); err == nil {
		t.Fatal("accepted duplicate artifact name")
	}
}

func TestCurrentSessionReviewerArtifactsSelectsNewestWorkspaceIdentityPerName(t *testing.T) {
	now := time.Now().UTC()
	artifacts, err := currentSessionReviewerArtifacts([]workspace.Artifact{
		{ID: "old-report", Name: "report.md", Priority: workspace.ArtifactPriorityUserStarred, UpdatedAt: now.Add(-time.Hour)},
		{ID: "claims", Name: "claims.jsonl", UpdatedAt: now.Add(-time.Minute)},
		{ID: "new-report", Name: "report.md", Priority: workspace.ArtifactPriorityUnknown, UpdatedAt: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 2 || artifacts[0].ID != "claims" || artifacts[1].ID != "new-report" {
		t.Fatalf("current artifact inventory=%#v", artifacts)
	}
	if _, err := currentSessionReviewerArtifacts([]workspace.Artifact{{ID: "missing-name"}}); err == nil {
		t.Fatal("accepted artifact without a current workspace name")
	}
}

func TestSessionReviewerWorkspaceEvidenceExcludesInternalToolResults(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-evidence", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "root-evidence", ProjectID: "project-evidence", AgentName: "OPERON", Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	srv := New(Options{FileRoot: root, Workspace: store})
	if _, _, err := store.WriteArtifactVersion(context.Background(), workspace.WriteArtifactVersionInput{
		ArtifactID: "report-evidence", ProjectID: "project-evidence", Name: "report.md",
		ContentType: "text/markdown", Content: strings.NewReader("# report"), CreatedBy: "runner",
		RootFrameID: "root-evidence", FrameID: "root-evidence",
	}); err != nil {
		t.Fatal(err)
	}
	internalID := "large-tool-result-" + strings.Repeat("a", 32)
	if _, _, err := store.WriteArtifactVersion(context.Background(), workspace.WriteArtifactVersionInput{
		ArtifactID: internalID, ProjectID: "project-evidence", Name: "tool-result-deadbeef.json",
		ContentType: "application/json", Content: strings.NewReader(`{"results":[]}`), CreatedBy: "runner",
		RootFrameID: "root-evidence", FrameID: "root-evidence",
	}); err != nil {
		t.Fatal(err)
	}
	evidence, err := srv.sessionReviewerWorkspaceEvidence(sessionstore.Session{WorkDir: root}, "root-evidence")
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence.Artifacts) != 1 || evidence.Artifacts[0].ArtifactID != "report-evidence" {
		t.Fatalf("reviewer workspace evidence=%#v", evidence.Artifacts)
	}
}

func TestTranscriptRunnerReviewerToolBudgetExhaustionKeepsCompletedTaskAndVisibleFailure(t *testing.T) {
	var reviewerCalls atomic.Int32
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		message := map[string]any{"role": "assistant", "content": "candidate"}
		if len(request.Messages) > 0 && strings.Contains(request.Messages[0].Content, "You are the REVIEWER") {
			call := reviewerCalls.Add(1)
			message = map[string]any{
				"role": "assistant", "content": "",
				"tool_calls": []any{map[string]any{
					"id": fmt.Sprintf("review-search-%d", call), "type": "function",
					"function": map[string]any{
						"name": "ToolSearch", "arguments": fmt.Sprintf(`{"query":"read-%d","max_results":1}`, call),
					},
				}},
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message}}})
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-review-budget", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-review-budget", ProjectID: "project-review-budget", AgentName: "OPERON", Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	repo, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Options{FileRoot: root, Workspace: store, Transcript: repo})
	if err := srv.sessionStore.Upsert(sessionstore.Session{
		ID: "frame-review-budget", Title: "Review budget", WorkDir: root,
		Project:       &sessionstore.Project{ID: "project-review-budget", Name: "Project", Path: root, BoundAt: time.Now().UTC()},
		Orchestration: map[string]any{"sessionConfig": map[string]any{"verifier_mode": "off"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := srv.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-review-budget", MessageUUID: "review-budget-message", ClientMessageID: "review-budget-user",
		Text: "Produce an evidence-backed result.", RuntimeConfig: map[string]any{"verifier_mode": "on"},
	}); err != nil {
		t.Fatal(err)
	}
	seedAnsweredTaskIntake(t, srv, "owner", "frame-review-budget")
	options := SessionRunnerChatOptions{
		SessionID: "frame-review-budget", RunnerID: "review-budget-runner", Endpoint: modelAPI.URL,
		Model: "test-model", AllowedTools: []string{"ToolSearch"}, LeaseTTL: time.Minute,
		ReplayLimit: 100, OutputLimitBytes: 64 << 10, MaxAttempts: 1, DisableSkillDiscovery: true,
		DisableMCPDiscovery: true,
	}
	result, err := srv.RunSessionRunnerChatOnce(t.Context(), options)
	if err != nil || !result.Claimed || result.Status != "completed" ||
		result.InterruptionReasonCode != "" || result.InterruptionAutoResume ||
		result.FinishEventID <= 0 || result.Attempt != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	rootFrame, found, err := store.GetFrame("frame-review-budget")
	if err != nil || !found || rootFrame.Status != "completed" {
		t.Fatalf("root frame=%#v found=%t err=%v", rootFrame, found, err)
	}
	frames, err := store.ListFramesForRoot("frame-review-budget")
	frameStatuses := map[string]string{}
	for _, frame := range frames {
		if frame.ID != "frame-review-budget" {
			frameStatuses[frame.AgentName] = frame.Status
		}
	}
	if err != nil || len(frames) != 3 || frameStatuses["REVIEWER"] != "failed" || frameStatuses["BOOKMARKER"] != "failed" {
		t.Fatalf("reviewer frames=%#v err=%v", frames, err)
	}
	checks, err := store.ListVerificationChecks("frame-review-budget", "")
	if err != nil || len(checks) != 0 {
		t.Fatalf("false verification checks=%#v err=%v", checks, err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "owner", "frame-review-budget")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	events, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: "owner", ThroughPublicationSequence: stream.NextPublication - 1, Limit: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	interruptions, terminals := 0, 0
	for _, event := range events {
		switch event.Event.Type {
		case "runner_checkpoint":
			var payload map[string]any
			if json.Unmarshal(event.ResolvedPayloadJSON, &payload) == nil && payload["reason_code"] == sessionRunnerCompletionReviewRecoveryReasonCode {
				interruptions++
			}
		case "runner_finished":
			terminals++
		}
	}
	if interruptions != 0 || terminals != 1 || reviewerCalls.Load() != 3 {
		t.Fatalf("interruptions=%d terminals=%d reviewerCalls=%d", interruptions, terminals, reviewerCalls.Load())
	}
	priorReviewerCalls := reviewerCalls.Load()
	options.SessionID = ""
	options.RunnerID = "review-budget-runner-idle"
	resumed, err := srv.RunSessionRunnerChatOnce(t.Context(), options)
	if err != nil || resumed.Claimed || reviewerCalls.Load() != priorReviewerCalls {
		t.Fatalf("resumed=%#v reviewerCalls=%d prior=%d err=%v", resumed, reviewerCalls.Load(), priorReviewerCalls, err)
	}
}

func TestSessionRunnerReviewerFrameSettlesCancelledWhenRootIsCancelled(t *testing.T) {
	reviewerStarted := make(chan struct{})
	var reviewerStartedOnce sync.Once
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(request.Messages) > 0 && strings.Contains(request.Messages[0].Content, "You are the REVIEWER") {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			reviewerStartedOnce.Do(func() { close(reviewerStarted) })
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "Candidate answer"}}}})
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "root", ProjectID: "project", AgentName: "OPERON", Status: "processing", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	repo, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Options{FileRoot: root, Workspace: store, Transcript: repo})
	if err := srv.sessionStore.Upsert(sessionstore.Session{
		ID: "root", Title: "Verification cancellation", WorkDir: root,
		Project:       &sessionstore.Project{ID: "project", Name: "Project", Path: root, BoundAt: time.Now().UTC()},
		Orchestration: map[string]any{"sessionConfig": map[string]any{"verifier_mode": "off"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := srv.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "root", MessageUUID: "review-cancel-message-1", ClientMessageID: "user-1",
		Text: "Produce an answer and verify it.", RuntimeConfig: map[string]any{"verifier_mode": "on"},
	}); err != nil {
		t.Fatal(err)
	}
	seedAnsweredTaskIntake(t, srv, "owner", "root")

	type runnerReturn struct {
		result SessionRunnerCycleResult
		err    error
	}
	runCtx, cancelRun := context.WithCancel(context.Background())
	runnerDone := make(chan runnerReturn, 1)
	go func() {
		result, err := srv.RunSessionRunnerChatOnce(runCtx, SessionRunnerChatOptions{
			SessionID: "root", RunnerID: "review-runner", Endpoint: modelAPI.URL,
			Model: "test-model", RequestTimeout: time.Minute, MaxAttempts: 1,
			LeaseTTL: time.Minute, ReplayLimit: 100, OutputLimitBytes: 64 << 10,
			DisableMCPDiscovery: true,
		})
		runnerDone <- runnerReturn{result: result, err: err}
	}()
	select {
	case <-reviewerStarted:
	case <-time.After(8 * time.Second):
		select {
		case early := <-runnerDone:
			t.Fatalf("completion reviewer request did not start; runner already returned: result=%#v err=%v", early.result, early.err)
		default:
			t.Fatal("completion reviewer request did not start")
		}
	}
	running, err := store.ListRunningVerification("root")
	if err != nil || len(running) != 1 || running[0].TargetFrameID == nil || *running[0].TargetFrameID != "root" {
		t.Fatalf("running verification = %#v error=%v", running, err)
	}
	reviewerFrameID := running[0].FrameID
	cancelRun()
	var returned runnerReturn
	select {
	case returned = <-runnerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled reviewer run did not settle")
	}
	if returned.err != nil || !returned.result.Claimed || returned.result.Status != "cancelled" ||
		returned.result.InterruptionReasonCode != "" || returned.result.InterruptionAutoResume ||
		returned.result.FinishEventID <= 0 {
		t.Fatalf("root cancellation result = %#v error=%v", returned.result, returned.err)
	}
	frame, found, err := store.GetFrame(reviewerFrameID)
	if err != nil || !found || frame.Status != "cancelled" {
		t.Fatalf("cancelled reviewer frame = %#v found=%v error=%v", frame, found, err)
	}
	events, err := store.ListFrameEvents(reviewerFrameID, 0, 10)
	if err != nil || len(events) != 2 || events[0].Type != "verification_started" || events[1].Type != "verification_cancelled" {
		t.Fatalf("cancelled reviewer events = %#v error=%v", events, err)
	}
	running, err = store.ListRunningVerification("root")
	if err != nil || len(running) != 0 {
		t.Fatalf("running verification after cancellation = %#v error=%v", running, err)
	}
	checks, err := store.ListVerificationChecks("root", "")
	if err != nil || len(checks) != 0 {
		t.Fatalf("cancelled reviewer persisted unsupported checks = %#v error=%v", checks, err)
	}
}

func TestSessionReviewerRecoverableRejection(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		content  string
		want     bool
	}{
		{
			name:     "blocked read of out-of-binding version is recoverable",
			toolName: "read_file",
			content:  `{"error":"read rejected: file version abc123 is outside the current review binding"}`,
			want:     true,
		},
		{
			name:     "failed read with unrelated error is not recoverable",
			toolName: "read_file",
			content:  `{"error":"file not found"}`,
			want:     false,
		},
		{
			name:     "review submit validation rejection is recoverable",
			toolName: sessionReviewerSubmitToolName,
			content:  `{"error":"review issue 3 does not cite a current immutable read receipt"}`,
			want:     true,
		},
		{
			name:     "non-review tool failure is not recoverable",
			toolName: "web_search",
			content:  `{"error":"request timeout"}`,
			want:     false,
		},
		{
			name:     "malformed tool result is not recoverable",
			toolName: "read_file",
			content:  "not-json",
			want:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sessionReviewerRecoverableRejection(tt.toolName, tt.content); got != tt.want {
				t.Fatalf("sessionReviewerRecoverableRejection(%q, %q) = %v, want %v", tt.toolName, tt.content, got, tt.want)
			}
		})
	}
}

func TestSessionReviewerAuxiliaryToolFailureDoesNotIncludeEvidenceOrSubmissionTools(t *testing.T) {
	for _, toolName := range []string{"web_fetch", "web_search", "mcp__pubmed__search_articles"} {
		if !sessionReviewerAuxiliaryToolFailure(toolName) {
			t.Fatalf("auxiliary reviewer tool %q was not classified", toolName)
		}
	}
	for _, toolName := range []string{"read_file", sessionReviewerSubmitToolName} {
		if sessionReviewerAuxiliaryToolFailure(toolName) {
			t.Fatalf("strict reviewer tool %q was classified as auxiliary", toolName)
		}
	}
}

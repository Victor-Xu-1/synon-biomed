package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	kernelruntime "synon-go/internal/kernel"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/toolcontract"
)

func TestKernelMCPLargeEvidenceRetainsStructuredSourceSemanticsOnReplay(t *testing.T) {
	for _, copies := range []int{1, 600000} {
		name := "inline"
		if copies > 1 {
			name = "externalized"
		}
		t.Run(name, func(t *testing.T) { testKernelMCPStructuredSourceReplay(t, copies) })
	}
}

func testKernelMCPStructuredSourceReplay(t *testing.T, copies int) {
	t.Helper()
	fixture := newAgentSaveArtifactsFixture(t)
	ctx := context.Background()
	authority, run := largeMCPStartedOperation(t, fixture)
	const tool = "mcp__literature__openalex_get_work"
	const doi = "10.1000/complete-source"
	call := kernelruntime.HostCall{ID: "hc-0123456789abcdef0123456789abcdef", CellID: authority.operation.ExecutionID}
	input := map[string]any{"work_id": "https://doi.org/" + doi}
	structuredOutput := map[string]any{
		"aa_padding": strings.Repeat("preserved-source-bytes ", copies),
		"doi":        doi, "title": "A complete structured record",
		"abstract_inverted_index": map[string]any{"measured": []any{0}, "effect": []any{1}},
	}
	structuredJSON, _ := json.Marshal(structuredOutput)
	// The real MCP adapter returns structured JSON as a string. Preserve that
	// exact host return type in storage, and the existing decoded evidence view.
	output := string(structuredJSON)
	audit := workspace.KernelMCPAuditInput{CallID: call.ID, FrameID: fixture.stream.FrameID, RootFrameID: fixture.stream.RootFrameID,
		OwnerUserID: fixture.stream.OwnerID, Server: "literature", Method: "openalex_get_work", Input: input}
	if _, err := fixture.server.workspaceStore.BeginKernelMCPAudit(ctx, audit); err != nil {
		t.Fatal(err)
	}
	attestation := kernelMCPSourceEvidenceAttestation{Class: workspace.KernelMCPEvidenceClassBundledReadOnly,
		ConnectorID: "bundled:literature", ConnectorSource: "bundled", InputSchemaSHA256: strings.Repeat("a", 64), ReadOnlyHint: true}
	if err := fixture.server.commitKernelMCPSourceEvidence(ctx, authority, audit, call, tool, input, output, attestation); err != nil {
		t.Fatal(err)
	}
	events, err := fixture.repo.ListProjectedEvents(ctx, transcriptstore.ListProjectedEventsInput{StreamUID: fixture.stream.UID, OwnerID: fixture.stream.OwnerID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint sessionRunnerDurableToolCheckpoint
	var eventID int64
	for _, event := range events {
		var current sessionRunnerDurableToolCheckpoint
		if json.Unmarshal(event.ResolvedPayloadJSON, &current) == nil && current.ToolCallID == call.ID {
			checkpoint, eventID = current, event.Event.EventID
		}
	}
	descriptor, _, externalized, err := toolcontract.DecodeExternalizedResult(checkpoint.ToolResult)
	if err != nil || externalized != (copies > 1) || !validSessionRunnerMCPDurableCheckpoint(checkpoint) || len(checkpoint.ToolResult) > int(runnerLargeToolResultInlineLimitBytes) {
		t.Fatalf("missing bounded, digest-bound receipt: externalized=%t valid=%t error=%v", externalized, validSessionRunnerMCPDurableCheckpoint(checkpoint), err)
	}
	original, _ := json.Marshal(output)
	if checkpoint.ResultSHA256 != kernelMCPEvidenceSHA256(original) || externalized && descriptor.SizeBytes != int64(len(original)) {
		t.Fatal("original digest/size lost")
	}
	messages, err := fixture.server.sessionRunnerDurableEvidenceMessages(ctx, run)
	if err != nil || len(messages) != 2 || !messages[0].ToolCalls[0].VerifiedEvidence || messages[1].Content != string(structuredJSON) {
		t.Fatalf("durable replay lost exact structured result: messages=%d error=%v", len(messages), err)
	}
	var restored map[string]any
	if err := json.Unmarshal([]byte(messages[1].Content), &restored); err != nil {
		t.Fatal(err)
	}
	if restored["doi"] != doi || restored["abstract_inverted_index"] == nil {
		t.Fatal("source record collapsed to unstructured text")
	}
	signals := trustedScientificEvidenceRecordSignals(tool, input, restored)
	if !stringSliceContains(signals, "evidence-record:publication:doi:"+doi) {
		t.Fatalf("substantive source record no longer verifies: %v", signals)
	}
	unsupported := unsupportedSessionRunnerCitationReferencesWithArtifactCandidatesUsing(messages, len(messages), "Source DOI: "+doi+".", nil, nil, fixture.server.sessionRunnerEvidenceTool)
	if len(unsupported) != 0 {
		t.Fatalf("source identity no longer grounds citations: %v", unsupported)
	}
	materials, err := fixture.server.sessionRunnerResearchMaterials(ctx, run)
	if err != nil || len(materials.Receipts) == 0 || materials.Receipts[0].MaterialRole != "evidence" || len(materials.Attempts) != 1 || !materials.Attempts[0].MaterialUsable {
		t.Fatalf("source routing lost substantive record: receipts=%v error=%v", materials.Receipts, err)
	}
	if materials.Attempts[0].ResultSHA256 != checkpoint.ResultSHA256 || materials.Receipts[0].ResultSHA256 != checkpoint.ResultSHA256 {
		t.Fatal("decoded projection replaced the original source digest")
	}
	for name, corrupt := range map[string]func(*sessionRunnerDurableToolCheckpoint){
		"original digest":  func(value *sessionRunnerDurableToolCheckpoint) { value.ResultSHA256 = strings.Repeat("b", 64) },
		"original call":    func(value *sessionRunnerDurableToolCheckpoint) { value.ToolCallID = "forged-call" },
		"original request": func(value *sessionRunnerDurableToolCheckpoint) { value.ToolInput = json.RawMessage(`{"forged":true}`) },
	} {
		t.Run(name, func(t *testing.T) {
			forged := checkpoint
			corrupt(&forged)
			got := researchMaterialsFromCheckpoints(fixture.server, []sessionRunnerResearchCheckpoint{{
				EventID: eventID, Checkpoint: forged, verifiedResult: structuredJSON,
			}})
			if len(got.Receipts) != 0 || len(got.Attempts) != 0 {
				t.Fatal("a valid projection bypassed the original checkpoint attestation")
			}
		})
	}
	unreadable := researchMaterialsFromCheckpoints(fixture.server, []sessionRunnerResearchCheckpoint{{
		EventID: eventID, Checkpoint: checkpoint, verifiedResult: structuredJSON, ResultUnreadable: true,
	}})
	if len(unreadable.Receipts) != 0 || len(unreadable.Attempts) != 1 || unreadable.Attempts[0].MaterialUsable {
		t.Fatal("unreadable source projection became qualified evidence")
	}
	if !externalized {
		return
	}
	// The normal read_file authority serves the immutable version and rejects
	// a different owner/project. No extra transport-only read endpoint exists.
	readable, found, err := fixture.server.openAgentWorkspaceReadableVersion(ctx, fixture.stream.OwnerID, fixture.stream.ProjectID, descriptor.VersionID)
	if err != nil || !found {
		t.Fatalf("read_file version unavailable: %v", err)
	}
	_ = readable.reader.Close()
	tail, err := fixture.server.executeAgentWorkspaceFileTool(ctx, fixture.identity, "read_file", map[string]any{
		"version_id": descriptor.VersionID, "byte_offset": len(original) - 256, "byte_limit": 256,
	})
	if err != nil || string(decodeWorkspaceBytePage(t, mapValue(tail))) != string(original[len(original)-256:]) {
		t.Fatalf("normal read_file could not continue through source tail: %v", err)
	}
	if _, found, _ := fixture.server.openAgentWorkspaceReadableVersion(ctx, "another-owner", fixture.stream.ProjectID, descriptor.VersionID); found {
		t.Fatal("foreign owner read large source")
	}
	if _, found, _ := fixture.server.openAgentWorkspaceReadableVersion(ctx, fixture.stream.OwnerID, "another-project", descriptor.VersionID); found {
		t.Fatal("foreign project read large source")
	}
	for name, corrupt := range map[string]func(*sessionRunnerDurableToolCheckpoint){
		"call": func(value *sessionRunnerDurableToolCheckpoint) { value.ToolCallID = "another-call" },
		"tool": func(value *sessionRunnerDurableToolCheckpoint) { value.ToolName = "another-tool" },
	} {
		t.Run(name, func(t *testing.T) {
			forged := checkpoint
			corrupt(&forged)
			if _, err := fixture.server.restoreDurableEvidencePayload(ctx, fixture.stream, forged, eventID, string(forged.ToolResult)); err == nil {
				t.Fatal("foreign receipt identity was restored")
			}
		})
	}
}

func largeMCPStartedOperation(t *testing.T, fixture *agentSaveArtifactsFixture) (*kernelMCPTranscriptEvidenceAuthority, *sessionRunnerChatRun) {
	t.Helper()
	ctx := context.Background()
	run := &sessionRunnerChatRun{SessionID: fixture.stream.SessionID, Attempt: int(fixture.claim.Attempt), ClaimToken: fixture.claim.ClaimToken,
		Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim}}
	call := agentruntime.ToolCall{ID: "large-source-repl", Name: "repl", Arguments: json.RawMessage(`{"code":"import host"}`)}
	if err := fixture.server.checkpointChatModelToolCalls(SessionRunnerChatOptions{RunnerID: fixture.claim.RunnerID}, run, []agentruntime.ToolCall{call}); err != nil {
		t.Fatal(err)
	}
	store := fixture.server.workspaceStore
	operation, found, err := store.GetKernelLocalOperationByToolCall(ctx, fixture.stream.OwnerID, fixture.stream.UID, call.ID)
	if err != nil || !found {
		t.Fatalf("operation unavailable: %v", err)
	}
	operation, err = store.ResolveKernelLocalOperationApproval(ctx, workspace.ResolveKernelLocalOperationApprovalInput{
		OwnerUserID: fixture.stream.OwnerID, OperationID: operation.OperationID, ExpectedStateVersion: operation.StateVersion,
		ApprovalRequestID: operation.ApprovalRequestID, Approved: true, DecisionID: "large-source-approved", Scope: "once", Source: "user", ActorID: fixture.stream.OwnerID, CurrentClaim: fixture.claim})
	if err != nil {
		t.Fatal(err)
	}
	operation, err = store.PrepareKernelLocalOperation(ctx, workspace.PrepareKernelLocalOperationInput{
		OwnerUserID: fixture.stream.OwnerID, OperationID: operation.OperationID, ExpectedStateVersion: operation.StateVersion,
		Claim: fixture.claim, BootID: "large-source-boot", KernelID: "large-source-kernel", KernelGeneration: 1, ConfinementSHA256: strings.Repeat("c", 64)})
	if err != nil {
		t.Fatal(err)
	}
	operation, err = store.StartKernelLocalOperation(ctx, workspace.StartKernelLocalOperationInput{
		OwnerUserID: fixture.stream.OwnerID, OperationID: operation.OperationID, ExpectedStateVersion: operation.StateVersion,
		Claim: fixture.claim, BootID: "large-source-boot", ExecutionID: "large-source-cell"})
	if err != nil {
		t.Fatal(err)
	}
	return &kernelMCPTranscriptEvidenceAuthority{stream: fixture.stream, operation: operation,
		identity: kernelTranscriptExecutionIdentity{RunnerClaim: fixture.claim, FrameID: fixture.stream.FrameID}}, run
}

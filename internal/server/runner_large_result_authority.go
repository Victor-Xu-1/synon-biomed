package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"unicode/utf8"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/toolcontract"
)

var (
	errRunnerLargeToolResultAuthority   = errors.New("runner large tool result authority is unavailable")
	errRunnerLargeToolResultUnavailable = errors.New("runner large tool result content is unavailable")
	errRunnerLargeToolResultConflict    = errors.New("runner large tool result conflicts with its immutable artifact")
)

const (
	runnerLargeToolResultArtifactIDPrefix = "large-tool-result-"
	// Compact a tool result once its canonical payload reaches 16,000 bytes.
	// Keep this separate from the gateway capture budget: the
	// latter may be larger so the immutable authority can persist the exact
	// result before replacing it in model context.
	runnerLargeToolResultInlineLimitBytes int64 = 16_000
	// The persisted result remains available through read_file(version_id).
	// A compact preview is navigation evidence, not a second copy of the result.
	runnerLargeToolResultPreviewLimitBytes = 2_000
)

type runnerLargeToolResultAuthority struct {
	server           *Server
	durableOperation *workspace.KernelLocalOperation
}

func (s *Server) largeToolResultAuthorityForEngine(ctx context.Context, sessionID string) agentruntime.LargeToolResultAuthority {
	if s == nil || s.workspaceStore == nil || s.transcriptStore == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	stream, authoritative, err := s.resolveTranscriptFrameStream(ctx, sessionID)
	if err != nil || !authoritative || stream.Kind != transcriptstore.StreamKindFrameRef ||
		stream.ProjectID == "" || stream.RootFrameID == "" || stream.FrameID == "" {
		return nil
	}
	return runnerLargeToolResultAuthority{server: s}
}

func (s *Server) largeToolResultAuthorityForKernelOperation(
	operation workspace.KernelLocalOperation,
) agentruntime.LargeToolResultAuthority {
	if s == nil || s.workspaceStore == nil || s.transcriptStore == nil {
		return nil
	}
	return runnerLargeToolResultAuthority{server: s, durableOperation: &operation}
}

func (authority runnerLargeToolResultAuthority) Externalize(
	ctx context.Context,
	input agentruntime.LargeToolResultInput,
) (agentruntime.LargeToolResultDescriptor, error) {
	s := authority.server
	if s == nil || s.workspaceStore == nil || s.transcriptStore == nil || ctx == nil {
		return agentruntime.LargeToolResultDescriptor{}, errRunnerLargeToolResultAuthority
	}
	callID := strings.TrimSpace(input.ToolCall.ID)
	toolName := strings.TrimSpace(input.ToolCall.Name)
	if callID == "" || callID != input.ToolCall.ID || toolName == "" || toolName != input.ToolCall.Name ||
		len(callID) > 512 || len(toolName) > 512 || input.MaxInlineBytes <= 0 ||
		int64(len(input.RawJSON)) <= input.MaxInlineBytes || !json.Valid(input.RawJSON) ||
		!validLargeToolResultOutcome(input.Outcome) {
		return agentruntime.LargeToolResultDescriptor{}, errRunnerLargeToolResultAuthority
	}
	if authority.durableOperation != nil {
		return authority.externalizeDurableKernelOperation(ctx, input, *authority.durableOperation)
	}
	run, _ := ctx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun)
	if run == nil || run.Transcript == nil || run.ToolSourceEventIDs == nil {
		return agentruntime.LargeToolResultDescriptor{}, errRunnerLargeToolResultAuthority
	}
	streamSnapshot, claim := run.Transcript.Stream, run.Transcript.Claim
	sourceEventID := run.ToolSourceEventIDs[callID]
	if sourceEventID <= 0 || run.SessionID != streamSnapshot.SessionID || int64(run.Attempt) != claim.Attempt ||
		run.ClaimToken != claim.ClaimToken || claim.StreamUID != streamSnapshot.UID || claim.OwnerID != streamSnapshot.OwnerID {
		return agentruntime.LargeToolResultDescriptor{}, errRunnerLargeToolResultAuthority
	}
	stream, err := s.transcriptStore.ValidateRunnerToolArtifactSource(
		ctx, claim, sourceEventID, callID, toolName,
	)
	if err != nil || !sameLargeToolResultStream(stream, streamSnapshot) {
		return agentruntime.LargeToolResultDescriptor{}, errors.Join(errRunnerLargeToolResultAuthority, err)
	}
	// Bind scientific provenance from the exact full result before the engine
	// replaces it with a bounded immutable descriptor. The later checkpoint
	// persists this server-derived state, so compaction, restart, and resume do
	// not turn a successful deep read into an apparent title-only search.
	s.bindTrustedScientificLargeToolResult(run, input)

	artifactID, _ := runnerLargeToolResultIdentities(stream, callID, toolName)
	existing, found, err := s.workspaceStore.FindRunnerLargeToolResult(ctx, artifactID, callID, toolName)
	if err != nil {
		return agentruntime.LargeToolResultDescriptor{}, errors.Join(errRunnerLargeToolResultAuthority, err)
	}
	if found {
		return s.replayRunnerLargeToolResult(ctx, input, stream, artifactID, existing)
	}

	record, err := s.workspaceStore.WriteRunnerLargeToolResult(ctx, workspace.WriteRunnerLargeToolResultInput{
		ArtifactID: artifactID, ProjectID: stream.ProjectID, RootFrameID: stream.RootFrameID,
		FrameID: stream.FrameID, StreamUID: stream.UID, OwnerUserID: stream.OwnerID,
		RunnerID: claim.RunnerID, ClaimToken: claim.ClaimToken, Attempt: claim.Attempt,
		SourceEventID: sourceEventID, ToolName: toolName, ToolCallID: callID,
		Content: input.RawJSON,
	})
	if err != nil {
		return agentruntime.LargeToolResultDescriptor{}, errors.Join(errRunnerLargeToolResultAuthority, err)
	}
	return buildRunnerLargeToolResultDescriptor(ctx, input, record)
}

// recoverPersistedRunnerLargeToolResult settles a non-kernel tool batch item
// when the immutable large-result write committed but the live runner was
// interrupted before its terminal lifecycle checkpoint. This is the same
// authority and descriptor path used by live execution; it never re-runs the
// external tool and never substitutes an outcome-unknown placeholder for
// evidence that is already durable.
func (s *Server) recoverPersistedRunnerLargeToolResult(
	ctx context.Context,
	options SessionRunnerChatOptions,
	run *sessionRunnerChatRun,
	item workspace.ToolCallBatchItem,
) (bool, error) {
	if s == nil || s.workspaceStore == nil || run == nil || run.Transcript == nil ||
		item.ToolCallID == "" || item.ToolName == "" || item.StartedEventID <= 0 {
		return false, nil
	}
	stream := run.Transcript.Stream
	artifactID, _ := runnerLargeToolResultIdentities(stream, item.ToolCallID, item.ToolName)
	record, found, err := s.workspaceStore.FindRunnerLargeToolResult(
		ctx, artifactID, item.ToolCallID, item.ToolName,
	)
	if err != nil || !found {
		return false, err
	}
	if record.StreamUID != stream.UID || record.OwnerUserID != stream.OwnerID ||
		record.ProjectID != stream.ProjectID || record.RootFrameID != stream.RootFrameID ||
		record.FrameID != stream.FrameID || record.SourceEventID != item.StartedEventID {
		return false, errRunnerLargeToolResultConflict
	}
	_, content, found, err := s.workspaceStore.OpenRunnerLargeToolResultContent(
		ctx, record.VersionID, stream.OwnerID,
	)
	if err != nil || !found {
		if err == nil {
			err = errRunnerLargeToolResultAuthority
		}
		return false, err
	}
	defer content.Close()
	raw, err := io.ReadAll(io.LimitReader(content, record.SizeBytes+1))
	if err != nil {
		return false, fmt.Errorf("read persisted runner large tool result: %w", err)
	}
	if int64(len(raw)) != record.SizeBytes || !json.Valid(raw) {
		return false, errRunnerLargeToolResultConflict
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != record.ContentSHA256 {
		return false, errRunnerLargeToolResultConflict
	}
	value := decodeToolEventJSON(string(raw))
	outcome := agentruntime.ClassifyToolResult(value)
	inlineLimit := runnerLargeToolResultInlineLimit(options.OutputLimitBytes)
	if inlineLimit >= record.SizeBytes {
		inlineLimit = record.SizeBytes - 1
	}
	engine := agentruntime.Engine{
		MaxToolResultBytes: inlineLimit,
		LargeToolResults:   runnerLargeToolResultAuthority{server: s},
	}
	materialized, err := engine.MaterializeRawToolResult(
		withTranscriptRunnerChatRun(ctx, run),
		agentruntime.ToolCall{ID: item.ToolCallID, Name: item.ToolName, Arguments: item.ArgumentsJSON},
		raw,
		outcome,
	)
	if err != nil {
		return false, fmt.Errorf("rematerialize persisted runner large tool result: %w", err)
	}
	status, phase := "completed", "completed"
	if outcome.HardFailed() {
		status, phase = "failed", "failed"
	}
	if err := s.checkpointChatTool(
		options, run, status, "recovered durable externalized tool result",
		item.ToolCallID, phase, map[string]any{
			"toolName": item.ToolName, "toolInput": decodeToolEventJSON(string(item.ArgumentsJSON)),
			"toolResult": decodeToolEventJSON(string(materialized.JSON)),
		},
	); err != nil {
		return false, err
	}
	return true, nil
}

func (authority runnerLargeToolResultAuthority) externalizeDurableKernelOperation(
	ctx context.Context,
	input agentruntime.LargeToolResultInput,
	operation workspace.KernelLocalOperation,
) (agentruntime.LargeToolResultDescriptor, error) {
	s := authority.server
	if operation.ToolCallID != input.ToolCall.ID || operation.Tool != input.ToolCall.Name ||
		operation.SourceEventID <= 0 || operation.SourceRunnerAttempt <= 0 ||
		operation.RunnerID == "" || operation.OperationID == "" {
		return agentruntime.LargeToolResultDescriptor{}, errRunnerLargeToolResultAuthority
	}
	stream, err := s.transcriptStore.ValidateDurableRunnerToolArtifactSource(
		ctx, operation.StreamUID, operation.OwnerUserID, operation.SourceEventID,
		operation.SourceRunnerAttempt, operation.ToolCallID, operation.Tool,
	)
	if err != nil || stream.ProjectID != operation.ProjectID || stream.RootFrameID != operation.RootFrameID ||
		stream.FrameID != operation.FrameID || stream.UID != operation.StreamUID || stream.OwnerID != operation.OwnerUserID {
		if err == nil {
			err = errRunnerLargeToolResultConflict
		}
		return agentruntime.LargeToolResultDescriptor{}, errors.Join(errRunnerLargeToolResultAuthority, err)
	}
	artifactID, _ := runnerLargeToolResultIdentities(stream, input.ToolCall.ID, input.ToolCall.Name)
	existing, found, err := s.workspaceStore.FindRunnerLargeToolResult(
		ctx, artifactID, input.ToolCall.ID, input.ToolCall.Name,
	)
	if err != nil {
		return agentruntime.LargeToolResultDescriptor{}, errors.Join(errRunnerLargeToolResultAuthority, err)
	}
	if found {
		return s.replayRunnerLargeToolResult(ctx, input, stream, artifactID, existing)
	}
	record, err := s.workspaceStore.WriteRunnerLargeToolResult(ctx, workspace.WriteRunnerLargeToolResultInput{
		ArtifactID: artifactID, ProjectID: stream.ProjectID, RootFrameID: stream.RootFrameID,
		FrameID: stream.FrameID, StreamUID: stream.UID, OwnerUserID: stream.OwnerID,
		RunnerID:   operation.RunnerID,
		ClaimToken: "detached-kernel-recovery:" + operation.OperationID,
		Attempt:    operation.SourceRunnerAttempt, SourceEventID: operation.SourceEventID,
		ToolName: input.ToolCall.Name, ToolCallID: input.ToolCall.ID, Content: input.RawJSON,
	})
	if err != nil {
		return agentruntime.LargeToolResultDescriptor{}, errors.Join(errRunnerLargeToolResultAuthority, err)
	}
	return buildRunnerLargeToolResultDescriptor(ctx, input, record)
}

func (s *Server) replayRunnerLargeToolResult(
	ctx context.Context,
	input agentruntime.LargeToolResultInput,
	stream transcriptstore.Stream,
	artifactID string,
	record workspace.RunnerLargeToolResult,
) (agentruntime.LargeToolResultDescriptor, error) {
	digest := sha256.Sum256(input.RawJSON)
	if record.ArtifactID != artifactID || record.ProjectID != stream.ProjectID ||
		record.ToolCallID != input.ToolCall.ID || record.ToolName != input.ToolCall.Name ||
		record.ContentType != "application/json" || record.SizeBytes != int64(len(input.RawJSON)) ||
		record.ContentSHA256 != hex.EncodeToString(digest[:]) {
		return agentruntime.LargeToolResultDescriptor{}, errRunnerLargeToolResultConflict
	}
	_, content, found, err := s.workspaceStore.OpenRunnerLargeToolResultContent(ctx, record.VersionID, stream.OwnerID)
	if err != nil || !found {
		if err == nil {
			err = errRunnerLargeToolResultAuthority
		}
		return agentruntime.LargeToolResultDescriptor{}, errors.Join(errRunnerLargeToolResultAuthority, err)
	}
	defer content.Close()
	return buildRunnerLargeToolResultDescriptor(ctx, input, record)
}

func sameLargeToolResultStream(left, right transcriptstore.Stream) bool {
	return left.UID == right.UID && left.OwnerID == right.OwnerID && left.SessionID == right.SessionID &&
		left.Kind == right.Kind && left.ProjectID == right.ProjectID && left.RootFrameID == right.RootFrameID &&
		left.FrameID == right.FrameID && left.Epoch == right.Epoch
}

func runnerLargeToolResultIdentities(stream transcriptstore.Stream, toolCallID, toolName string) (string, string) {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		"runner-large-tool-result-v1", stream.OwnerID, stream.ProjectID, stream.RootFrameID,
		stream.FrameID, stream.UID, toolCallID, toolName,
	}, "\x00")))
	encoded := hex.EncodeToString(digest[:])
	return runnerLargeToolResultArtifactIDPrefix + encoded[:32], "runner-large-tool-result-" + encoded
}

func isRunnerLargeToolResultArtifactID(value string) bool {
	return workspace.IsRunnerLargeToolResultArtifactID(value)
}

func runnerLargeToolResultFilename(toolName, toolCallID string) string {
	digest := sha256.Sum256([]byte(toolName + "\x00" + toolCallID))
	return "tool-result-" + hex.EncodeToString(digest[:8]) + ".json"
}

func buildRunnerLargeToolResultDescriptor(
	ctx context.Context,
	input agentruntime.LargeToolResultInput,
	record workspace.RunnerLargeToolResult,
) (agentruntime.LargeToolResultDescriptor, error) {
	digest := sha256.Sum256(input.RawJSON)
	if record.ArtifactID == "" || record.VersionID == "" || record.ContentType != "application/json" ||
		record.SizeBytes != int64(len(input.RawJSON)) || record.ContentSHA256 != hex.EncodeToString(digest[:]) {
		return agentruntime.LargeToolResultDescriptor{}, errRunnerLargeToolResultConflict
	}
	if input.MaxInlineBytes <= 0 || len(input.RawJSON) <= 1 {
		return agentruntime.LargeToolResultDescriptor{}, errRunnerLargeToolResultAuthority
	}
	if descriptor, matched, err := runnerSourceFeedbackDescriptor(ctx, input, record); matched || err != nil {
		return descriptor, err
	}
	maxPreview := len(input.RawJSON) - 1
	if maxPreview > runnerLargeToolResultPreviewLimitBytes {
		maxPreview = runnerLargeToolResultPreviewLimitBytes
	}
	if input.MaxInlineBytes < int64(maxPreview) {
		maxPreview = int(input.MaxInlineBytes)
	}
	fit := func(readWith string) (agentruntime.LargeToolResultDescriptor, error) {
		low, high := 1, maxPreview
		var best agentruntime.LargeToolResultDescriptor
		for low <= high {
			candidateBytes := low + (high-low)/2
			preview := boundedRunnerLargeToolResultPreview(input.RawJSON, candidateBytes)
			descriptor := agentruntime.LargeToolResultDescriptor{
				ArtifactID: record.ArtifactID, VersionID: record.VersionID, SHA256: record.ContentSHA256,
				SizeBytes: record.SizeBytes, ContentType: "application/json", Outcome: input.Outcome,
				ContentURL: "/api/artifacts/" + url.PathEscape(record.ArtifactID) + "/versions/" + url.PathEscape(record.VersionID),
				ReadWith:   readWith, Preview: string(preview), Truncated: len(preview) < len(input.RawJSON),
			}
			encoded, err := json.Marshal(descriptor)
			if err != nil {
				return agentruntime.LargeToolResultDescriptor{}, err
			}
			if int64(len(encoded)) <= input.MaxInlineBytes && descriptor.Preview != "" && descriptor.Truncated {
				best = descriptor
				low = candidateBytes + 1
				continue
			}
			high = candidateBytes - 1
		}
		return best, nil
	}
	readWith := toolcontract.CanonicalReadWith(record.VersionID)
	best, err := fit(readWith)
	if err != nil {
		return agentruntime.LargeToolResultDescriptor{}, errors.Join(errRunnerLargeToolResultAuthority, err)
	}
	if best.Preview != "" {
		return best, nil
	}
	// A deliberately tiny model-output budget may fit the historical closed
	// descriptor but not the optional exact read hint. Preserve the externalized
	// result and its immutable version identity instead of failing the task. The
	// normal runtime budget keeps read_with; only the constrained fallback omits
	// it and remains decodable by older and current readers.
	best, err = fit("")
	if err != nil {
		return agentruntime.LargeToolResultDescriptor{}, errors.Join(errRunnerLargeToolResultAuthority, err)
	}
	if best.Preview != "" {
		return best, nil
	}
	return agentruntime.LargeToolResultDescriptor{}, fmt.Errorf(
		"%w: descriptor cannot fit %d byte inline budget", errRunnerLargeToolResultAuthority, input.MaxInlineBytes,
	)
}

func runnerLargeToolResultInlineLimit(configured int64) int64 {
	if configured <= 0 {
		configured = defaultSessionRunnerOutputLimitBytes
	}
	if configured > runnerLargeToolResultInlineLimitBytes {
		return runnerLargeToolResultInlineLimitBytes
	}
	return configured
}

func boundedRunnerLargeToolResultPreview(raw []byte, limit int) []byte {
	if limit >= len(raw) {
		limit = len(raw) - 1
	}
	if limit <= 0 {
		return nil
	}
	preview := raw[:limit]
	for len(preview) > 0 && !utf8.Valid(preview) {
		preview = preview[:len(preview)-1]
	}
	return preview
}

// compactRunnerLargeToolResultDescriptorForReplay applies the current model
// context preview contract to a historical externalized descriptor without
// rewriting its durable event or immutable full-result artifact. This lets a
// task created by an older build resume under the same bounded context policy
// as a newly executed tool call.
func compactRunnerLargeToolResultDescriptorForReplay(raw string) (string, error) {
	normalized, err := normalizeLegacyReusedRunnerLargeToolResultDescriptor(raw)
	if err != nil {
		return "", err
	}
	descriptor, _, found, err := toolcontract.DecodeExternalizedResult([]byte(normalized))
	if !found {
		return normalized, nil
	}
	if err != nil {
		return "", err
	}
	if runnerSourceFeedbackPreview(descriptor) {
		return normalized, nil
	}
	if len([]byte(descriptor.Preview)) <= runnerLargeToolResultPreviewLimitBytes {
		return normalized, nil
	}
	descriptor.Preview = string(boundedRunnerLargeToolResultPreview(
		[]byte(descriptor.Preview), runnerLargeToolResultPreviewLimitBytes,
	))
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// normalizeLegacyReusedRunnerLargeToolResultDescriptor removes exactly one
// historical presentation-only marker from an otherwise valid closed
// externalized-result descriptor. Older read-reuse hydration cached the small
// descriptor instead of its immutable raw result, and markReadReuse appended
// reused=true before the terminal checkpoint. The durable event remains audit
// evidence; provider replay receives the canonical nine-field descriptor.
// Any other extra field, false marker, or malformed descriptor still fails the
// strict toolcontract decoder.
func normalizeLegacyReusedRunnerLargeToolResultDescriptor(raw string) (string, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fields) != nil || len(fields) != 10 {
		return raw, nil
	}
	marker, found := fields["reused"]
	if !found {
		return raw, nil
	}
	var reused bool
	if json.Unmarshal(marker, &reused) != nil || !reused {
		return raw, nil
	}
	delete(fields, "reused")
	candidate, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	if _, _, found, err := toolcontract.DecodeExternalizedResult(candidate); err != nil || !found {
		return raw, nil
	}
	return string(candidate), nil
}

func validLargeToolResultOutcome(outcome agentruntime.ToolResultOutcome) bool {
	switch outcome {
	case agentruntime.ToolResultSucceeded, agentruntime.ToolResultFailed,
		agentruntime.ToolResultUnavailable, agentruntime.ToolResultPartial:
		return true
	default:
		return false
	}
}

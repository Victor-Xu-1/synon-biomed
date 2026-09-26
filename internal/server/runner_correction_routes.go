package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
	transcriptstore "synon-go/internal/persistence/transcript"
)

const runnerCorrectionClosedRouteDiagnostic = `{"code":"correction_route_closed","message":"This unchanged action already led to rejection of the current recovery condition.","recovery":"Reuse its receipt, read the complete recovery condition, or select different execution arguments or another advertised capability. A verified change to this action's material inputs permits re-evaluation; unrelated successes do not discharge the condition."}`

// Membership is derived from the same canonical transcript as correction
// hydration. Only the proposed batch is retained in memory, never the task's
// growing history of attempted strategies. No new persistence or executor is
// involved. Rejection closes a strategy, not the logical task.
type runnerCorrectionRoute struct {
	keys      map[string]bool
	resources map[string]string
	current   string
	pending   bool
	closed    bool
}

func (g serverAgentRuntimeToolGateway) correctionRoutePreflight(ctx context.Context, calls []agentruntime.ToolCall) (map[int]string, error) {
	run := g.taskRun
	if run == nil || run.Transcript == nil || run.CorrectionReason == "" {
		return nil, nil
	}
	if err := g.validateCorrectionRouteClaim(ctx); err != nil {
		return nil, err
	}
	routes := make(map[int]*runnerCorrectionRoute, len(calls))
	for index, call := range calls {
		// Reading the host condition is inspection, never an attempted repair.
		// Its native cursor/reuse authority already distinguishes novel pages.
		if runnerCorrectionReadCall(call) || !g.AdmitsToolCall(call) {
			continue
		}
		name, err := canonicalRuntimeToolName(call.Name)
		if err != nil {
			continue
		}
		input := map[string]any{}
		if len(call.Arguments) > 0 && json.Unmarshal(call.Arguments, &input) != nil {
			continue
		}
		normalized := g.normalizeAdmittedToolArguments(name, input)
		raw, err := json.Marshal(normalized)
		if err != nil {
			return nil, err
		}
		routes[index] = &runnerCorrectionRoute{
			keys:      map[string]bool{agentruntime.ExecutionCallFingerprint(call.Name, call.Arguments): true, agentruntime.ExecutionCallFingerprint(name, raw): true},
			resources: g.correctionActionResources(normalized),
		}
	}
	if len(routes) == 0 {
		return nil, nil
	}
	// First derive each proposal's current relevant material state. A second
	// paged pass asks whether that exact state/action/condition has already
	// failed. This retains old membership even beyond bounded model previews.
	err := g.server.scanSessionRunnerRecoveryEntries(ctx, run.Transcript, func(entry eventjournal.Entry) error {
		for _, route := range routes {
			if runnerEntryStartsNewLogicalTask(entry) {
				route.resetMaterials()
			}
			g.observeCorrectionRouteMaterials(route, entry)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, route := range routes {
		route.current = route.materialFingerprint()
		route.resetMaterials()
	}
	expected := runnerCorrectionFingerprint(run.correctionCause())
	activeCondition := ""
	err = g.server.scanSessionRunnerRecoveryEntries(ctx, run.Transcript, func(entry eventjournal.Entry) error {
		if runnerRecoveryCheckpointSuperseded(entry) {
			// Admission decisions belong to the runtime contract that made them.
			// Preserve receipts and material identities, but reevaluate a repaired
			// contract instead of carrying an obsolete quarantine across upgrades.
			for _, route := range routes {
				route.pending, route.closed = false, false
			}
			return nil
		}
		if runnerEntryStartsNewLogicalTask(entry) {
			activeCondition = ""
		}
		for _, route := range routes {
			if runnerEntryStartsNewLogicalTask(entry) {
				route.resetMaterials()
				route.pending, route.closed = false, false
			}
			g.observeCorrectionRouteMaterials(route, entry)
		}
		if correction, found := latestRunnerCorrection([]eventjournal.Entry{entry}); found {
			// Protocol-only failures cannot replace a substantive obligation.
			if correction.ReasonCode == sessionRunnerRequiredToolChoiceUnsatisfiedReasonCode && run.CorrectionReason != correction.ReasonCode {
				return nil
			}
			fingerprint := runnerCorrectionFingerprint(correction.cause())
			for _, route := range routes {
				if fingerprint == expected && (activeCondition == "" || activeCondition == expected) && route.pending && route.materialFingerprint() == route.current {
					route.closed = true
				}
				route.pending = false
			}
			activeCondition = fingerprint
			return nil
		}
		if reason := stringValue(entry.Message["reason_code"]); reason == sessionRunnerToolRoundNoProgressReasonCode || reason == sessionRunnerToolRoundNoProgressExhaustedReasonCode {
			// These routes already belong to their recorded no-progress scope.
			// Do not retroactively assign them to a later unrelated correction.
			for _, route := range routes {
				route.pending = false
			}
		}
		if !runnerCorrectionRouteReceipt(entry) {
			return nil
		}
		for _, input := range []any{entry.Message["toolInput"], runnerCheckpointExecutedToolInput(entry.Message)} {
			if _, ok := input.(map[string]any); !ok {
				continue
			}
			raw, err := json.Marshal(input)
			if err != nil {
				return err
			}
			key := agentruntime.ExecutionCallFingerprint(stringValue(entry.Message["toolName"]), raw)
			for _, route := range routes {
				route.pending = route.pending || route.keys[key]
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := g.validateCorrectionRouteClaim(ctx); err != nil {
		return nil, err
	}
	diagnostics := make(map[int]string)
	for index, route := range routes {
		if route.closed {
			diagnostics[index] = g.closedRouteDiagnostic(calls[index], runnerCorrectionClosedRouteDiagnostic)
		}
	}
	return diagnostics, nil
}

func (g serverAgentRuntimeToolGateway) validateCorrectionRouteClaim(ctx context.Context) error {
	run := g.taskRun
	if g.server == nil || g.server.transcriptStore == nil {
		return transcriptstore.ErrSchemaUnavailable
	}
	if run.Transcript.Claim.StreamUID != run.Transcript.Stream.UID || run.Transcript.Claim.OwnerID != run.Transcript.Stream.OwnerID || run.SessionID != run.Transcript.Stream.SessionID {
		return transcriptstore.ErrClaimStale
	}
	stream, err := g.server.transcriptStore.ValidateLiveRunnerClaim(ctx, run.Transcript.Claim)
	if err != nil {
		return err
	}
	if stream.InputRevision != run.Transcript.Claim.ClaimedInputRevision {
		return transcriptstore.ErrClaimStale
	}
	return nil
}

func runnerCorrectionRouteReceipt(entry eventjournal.Entry) bool {
	m := entry.Message
	if entry.SourceEventType != "runner_checkpoint" || m["type"] != "runner_checkpoint" || strings.TrimSpace(stringValue(m["toolName"])) == "" {
		return false
	}
	if m["status"] == "failed" && m["toolPhase"] == prestartToolFailurePhase &&
		boolValue(m["rejectedBeforeExecution"], false) && runnerDerivedClosedRouteResult(mapValue(m["toolResult"])) {
		return false
	}
	if m["status"] == "completed" && m["toolPhase"] == "completed" {
		return !agentruntime.ToolResultDidNotExecute(m["toolResult"])
	}
	return m["status"] == "failed" && (m["toolPhase"] == "failed" || m["toolPhase"] == prestartToolFailurePhase)
}

// A guard's own "route closed" reply is a projection of earlier evidence,
// not another attempted action. Replaying it as a fresh rejection makes old
// tasks permanently unable to revisit a route after its original cause is
// repaired or reclassified.
func runnerDerivedClosedRouteResult(result map[string]any) bool {
	if result["executed"] != false {
		return false
	}
	switch strings.TrimSpace(stringValue(result["code"])) {
	case "correction_route_closed", "durable_no_progress_route_closed":
		return true
	default:
		return false
	}
}

// Only explicit resource fields are dependencies. Prose, code comments, tool
// labels and arbitrary successful operations cannot manufacture a new input.
// Paths are compared lexically in the task workspace; execution still passes
// through the original path, owner and permission authorities.
func (g serverAgentRuntimeToolGateway) correctionActionResources(input map[string]any) map[string]string {
	resources := map[string]string{}
	for _, field := range []string{"file_path", "file_paths", "path", "paths", "files", "filenames", "url", "urls", "artifact_id", "version_id", "step_id"} {
		value, found := input[field]
		if !found {
			continue
		}
		values := stringArrayValue(value)
		if text, ok := value.(string); ok {
			values = []string{text}
		}
		for _, text := range values {
			text = strings.TrimSpace(text)
			if text == "" {
				continue
			}
			kind := field
			switch field {
			case "file_path", "file_paths", "path", "paths", "files", "filenames":
				kind = "file"
				if g.kernel != nil && g.kernel.workspaceDir != "" && !filepath.IsAbs(text) {
					text = filepath.Join(g.kernel.workspaceDir, text)
				}
				text = filepath.Clean(text)
			case "urls":
				kind = "url"
			}
			resources[kind+"\x00"+text] = ""
		}
	}
	return resources
}

func (g serverAgentRuntimeToolGateway) observeCorrectionRouteMaterials(route *runnerCorrectionRoute, entry eventjournal.Entry) {
	if !runnerCorrectionRouteReceipt(entry) || !runnerCheckpointHasMaterialProgress(entry.Message) {
		return
	}
	result := mapValue(entry.Message["toolResult"])
	// Explicit change evidence is required. A generic success, a read with no
	// content identity, and a non-executing preflight cannot reopen a route.
	if !boolValue(result["changed"], false) && stringValue(mapValue(result["effect"])["state"]) != string(agentruntime.ToolEffectChanged) {
		return
	}
	input := mapValue(runnerCheckpointExecutedToolInput(entry.Message))
	raw, err := json.Marshal(input)
	if err != nil {
		return
	}
	witness := agentruntime.ExecutionCallFingerprint(stringValue(entry.Message["toolName"]), raw)
	// A complete native file view identifies bytes independently of the edit
	// route that produced them. A truncated view must never stand in for them.
	if view := mapValue(result["file_view"]); len(view) > 0 && view["truncated"] == false {
		if content, ok := view["content"].(string); ok && int64(len(content)) == numberValue(view["size_bytes"]) {
			digest := sha256.Sum256([]byte(content))
			witness = hex.EncodeToString(digest[:])
		}
	}
	// Prefer an immutable content identity if the native receipt supplies it.
	for _, field := range []string{"content_sha256", "sha256", "final_sha256"} {
		if digest := stringValue(result[field]); validSHA256Hex(digest) {
			witness = strings.ToLower(digest)
			break
		}
	}
	for resource := range g.correctionActionResources(input) {
		if _, depends := route.resources[resource]; depends {
			route.resources[resource] = witness
		}
	}
}

func (route *runnerCorrectionRoute) resetMaterials() {
	for resource := range route.resources {
		route.resources[resource] = ""
	}
}

func (route *runnerCorrectionRoute) materialFingerprint() string {
	encoded, _ := json.Marshal(route.resources)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

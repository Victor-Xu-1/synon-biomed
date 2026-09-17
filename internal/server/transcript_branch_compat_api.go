package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) handleCompatibilityTranscriptBranches(
	w http.ResponseWriter,
	r *http.Request,
	frame workspace.CompatibilityFrame,
) {
	if r.Method != http.MethodGet {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	if s.transcriptStore == nil {
		writeV11Detail(w, http.StatusConflict, branchAuthorityUnavailableDetail)
		return
	}
	if s.transcriptContractErr != nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Transcript runtime is not available")
		return
	}
	ownerID := compatAgentUserID(r)
	stream, found, err := s.transcriptStore.GetFrameStreamBySession(r.Context(), ownerID, frame.ID)
	if err != nil {
		writeCanonicalTranscriptBranchError(w, err, "Conversation not found")
		return
	}
	if !found {
		writeV11Detail(w, http.StatusConflict, branchAuthorityUnavailableDetail)
		return
	}
	state, branches, err := s.transcriptStore.ListBranches(r.Context(), stream.UID, ownerID)
	if err != nil {
		writeCanonicalTranscriptBranchError(w, err, "Conversation branch not found")
		return
	}
	items := make([]map[string]any, 0, len(branches))
	for _, branch := range branches {
		item := map[string]any{
			"id": branch.BranchID, "parent_id": nil, "fork_point": nil,
			"kind": branch.Kind, "source_message_id": nullableCompatibilityString(branch.SourceMessageID),
			"created_at": branch.CreatedAt.UTC(), "updated_at": branch.UpdatedAt.UTC(), "active": branch.Active,
		}
		if branch.ParentBranchID != "" {
			item["parent_id"] = branch.ParentBranchID
		}
		if branch.ForkEventID != nil {
			item["fork_point"] = branch.ForkPoint
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"root_frame_id": frame.ID, "active_branch_id": state.ActiveBranchID,
		"generation": state.Generation, "branches": items,
	})
}

type canonicalTranscriptBranchContext struct {
	Stream                 transcriptstore.Stream
	State                  transcriptstore.BranchState
	SourceBranch           string
	ExpectedActiveBranchID string
	ExpectedGeneration     int64
}

func (s *Server) handleCanonicalTranscriptRootFork(
	w http.ResponseWriter,
	r *http.Request,
	frame workspace.CompatibilityFrame,
	input compatibilityRootForkRequest,
) {
	branchContext, ok := s.canonicalTranscriptBranchContext(
		w, r, frame, input.SourceBranchID, input.ExpectedBranchID, input.ExpectedGeneration,
	)
	if !ok {
		return
	}
	runtimeConfig := compatibilityBranchRuntimeConfig(
		input.VerifierMode, input.MemoryMode, input.UltraMode, input.PlanMode, input.TargetAgent,
	)
	mutationID, err := canonicalTranscriptBranchMutationID(
		r, input.ClientMutationID, map[string]any{
			"version": 1, "kind": "edit", "owner_id": branchContext.Stream.OwnerID,
			"root_frame_id": frame.ID, "source_branch_id": branchContext.SourceBranch,
			"source_message_index":     *input.MessageIndex,
			"source_client_message_id": strings.TrimSpace(input.SourceClientMessageID),
			"replacement_text":         input.EditedContent, "runtime_config": runtimeConfig,
		},
	)
	if err != nil {
		writeV11Detail(w, http.StatusBadRequest, "Invalid branch mutation id")
		return
	}
	result, err := s.transcriptStore.ForkFrameUserMessageAtIndex(
		r.Context(), transcriptstore.ForkFrameUserMessageAtIndexInput{
			StreamUID: branchContext.Stream.UID, OwnerID: branchContext.Stream.OwnerID,
			SourceBranchID:         branchContext.SourceBranch,
			ExpectedActiveBranchID: branchContext.ExpectedActiveBranchID,
			ExpectedGeneration:     branchContext.ExpectedGeneration, ClientMutationID: mutationID,
			SourceMessageIndex:     int64(*input.MessageIndex),
			ExpectedSourceClientID: strings.TrimSpace(input.SourceClientMessageID),
			ReplacementText:        input.EditedContent, RuntimeConfig: runtimeConfig, Destinations: []string{"ws"},
		},
	)
	if err != nil {
		writeCanonicalTranscriptBranchError(w, err, "Source message not found")
		return
	}
	if result.Created {
		s.signalTranscriptWebDelivery()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"root_frame_id": frame.ID, "branch_id": result.BranchID,
		"generation": result.Generation, "status": "accepted",
	})
}

func (s *Server) handleCanonicalTranscriptRootForkAtAnswer(
	w http.ResponseWriter,
	r *http.Request,
	frame workspace.CompatibilityFrame,
	input compatibilityRootAnswerForkRequest,
) {
	branchContext, ok := s.canonicalTranscriptBranchContext(
		w, r, frame, input.SourceBranchID, input.ExpectedBranchID, input.ExpectedGeneration,
	)
	if !ok {
		return
	}
	response := transcriptstore.AskUserBranchResponse{
		Action: strings.TrimSpace(input.Response.Action), Message: strings.TrimSpace(input.Response.Message),
	}
	if len(input.Response.Answers) > 0 && string(input.Response.Answers) != "null" {
		if err := json.Unmarshal(input.Response.Answers, &response.Answers); err != nil {
			writeV11Detail(w, http.StatusBadRequest, "response.answers must be an object of string answers")
			return
		}
	}
	runtimeConfig := compatibilityBranchRuntimeConfig(
		input.VerifierMode, input.MemoryMode, input.UltraMode, input.PlanMode, input.TargetAgent,
	)
	mutationID, err := canonicalTranscriptBranchMutationID(
		r, input.ClientMutationID, map[string]any{
			"version": 1, "kind": "answer", "owner_id": branchContext.Stream.OwnerID,
			"root_frame_id": frame.ID, "source_branch_id": branchContext.SourceBranch,
			"tool_use_id": input.ToolUseID, "response": response, "runtime_config": runtimeConfig,
		},
	)
	if err != nil {
		writeV11Detail(w, http.StatusBadRequest, "Invalid branch mutation id")
		return
	}
	result, err := s.transcriptStore.ForkFrameAskUserAnswerAtTool(
		r.Context(), transcriptstore.ForkFrameAskUserAnswerAtToolInput{
			StreamUID: branchContext.Stream.UID, OwnerID: branchContext.Stream.OwnerID,
			SourceBranchID:         branchContext.SourceBranch,
			ExpectedActiveBranchID: branchContext.ExpectedActiveBranchID,
			ExpectedGeneration:     branchContext.ExpectedGeneration, ClientMutationID: mutationID,
			ToolUseID: input.ToolUseID, Response: response, RuntimeConfig: runtimeConfig, Destinations: []string{"ws"},
		},
	)
	if err != nil {
		writeCanonicalTranscriptBranchError(
			w, err, "tool_use_id "+input.ToolUseID+" not found in messages",
		)
		return
	}
	if result.Created {
		s.signalTranscriptWebDelivery()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"root_frame_id": frame.ID, "branch_id": result.BranchID,
		"generation": result.Generation, "status": "accepted",
	})
}

func (s *Server) canonicalTranscriptBranchContext(
	w http.ResponseWriter,
	r *http.Request,
	frame workspace.CompatibilityFrame,
	sourceBranch, expectedBranch *string,
	expectedGeneration *int64,
) (canonicalTranscriptBranchContext, bool) {
	if s.transcriptStore == nil {
		writeV11Detail(w, http.StatusConflict, branchAuthorityUnavailableDetail)
		return canonicalTranscriptBranchContext{}, false
	}
	if s.transcriptContractErr != nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Transcript runtime is not available")
		return canonicalTranscriptBranchContext{}, false
	}
	if frame.ID == "" || frame.RootFrameID != frame.ID {
		writeV11Detail(w, http.StatusBadRequest, "Conversation branch mutations require a root frame")
		return canonicalTranscriptBranchContext{}, false
	}
	ownerID := compatAgentUserID(r)
	stream, found, err := s.transcriptStore.GetFrameStreamBySession(r.Context(), ownerID, frame.ID)
	if err != nil {
		writeCanonicalTranscriptBranchError(w, err, "Conversation not found")
		return canonicalTranscriptBranchContext{}, false
	}
	if !found {
		writeV11Detail(w, http.StatusConflict, branchAuthorityUnavailableDetail)
		return canonicalTranscriptBranchContext{}, false
	}
	state, err := s.transcriptStore.GetBranchState(r.Context(), stream.UID, ownerID)
	if err != nil {
		writeCanonicalTranscriptBranchError(w, err, "Conversation not found")
		return canonicalTranscriptBranchContext{}, false
	}
	if (expectedBranch == nil) != (expectedGeneration == nil) {
		writeV11Detail(w, http.StatusBadRequest, "expected_branch_id and expected_generation must be provided together")
		return canonicalTranscriptBranchContext{}, false
	}
	if expectedBranch != nil {
		if strings.TrimSpace(*expectedBranch) == "" || *expectedGeneration <= 0 {
			writeV11Detail(w, http.StatusBadRequest, "expected branch state is invalid")
			return canonicalTranscriptBranchContext{}, false
		}
	}
	expectedActiveBranchID, expectedGenerationValue := state.ActiveBranchID, state.Generation
	if expectedBranch != nil {
		expectedActiveBranchID = strings.TrimSpace(*expectedBranch)
		expectedGenerationValue = *expectedGeneration
	}
	selected := ""
	if sourceBranch != nil {
		selected = strings.TrimSpace(*sourceBranch)
	}
	if selected == "" {
		selected, _, _ = transcriptstore.BaseBranchIdentity(stream.UID)
	}
	return canonicalTranscriptBranchContext{
		Stream: stream, State: state, SourceBranch: selected,
		ExpectedActiveBranchID: expectedActiveBranchID, ExpectedGeneration: expectedGenerationValue,
	}, true
}

func compatibilityBranchRuntimeConfig(
	verifierMode, memoryMode *string,
	ultraMode, planMode *bool,
	targetAgent *string,
) map[string]any {
	config := map[string]any{}
	if verifierMode != nil {
		config["verifier_mode"] = *verifierMode
	}
	if memoryMode != nil {
		config["memory_mode"] = *memoryMode
	}
	if ultraMode != nil {
		config["ultra_mode"] = *ultraMode
	}
	if planMode != nil {
		config["plan_mode"] = *planMode
	}
	if targetAgent != nil {
		config["target_agent"] = *targetAgent
	}
	return config
}

func canonicalTranscriptBranchMutationID(r *http.Request, bodyValue string, semantic any) (string, error) {
	bodyValue = strings.TrimSpace(bodyValue)
	headerValue := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if headerValue == "" {
		headerValue = strings.TrimSpace(r.Header.Get("X-Synon-Idempotency-Key"))
	}
	if bodyValue != "" && headerValue != "" && bodyValue != headerValue {
		return "", errors.New("branch mutation ids conflict")
	}
	if bodyValue != "" {
		return bodyValue, nil
	}
	if headerValue != "" {
		return headerValue, nil
	}
	encoded, err := json.Marshal(semantic)
	if err != nil || len(encoded) > 1<<20 {
		return "", errors.New("branch mutation identity is invalid")
	}
	digest := sha256.Sum256(encoded)
	return "http-" + hex.EncodeToString(digest[:16]), nil
}

func writeCanonicalTranscriptBranchError(w http.ResponseWriter, err error, missingDetail string) {
	switch {
	case errors.Is(err, transcriptstore.ErrBranchTargetNotFound):
		writeV11Detail(w, http.StatusNotFound, missingDetail)
	case errors.Is(err, transcriptstore.ErrOwnerMismatch):
		writeV11Detail(w, http.StatusNotFound, "Conversation not found")
	case errors.Is(err, transcriptstore.ErrBranchRequestInvalid):
		writeV11Detail(w, http.StatusBadRequest, "Invalid branch request")
	case errors.Is(err, transcriptstore.ErrBranchStateStale), errors.Is(err, transcriptstore.ErrEventConflict):
		writeV11Detail(w, http.StatusConflict, "Conversation branch changed; refresh and retry")
	case errors.Is(err, transcriptstore.ErrSchemaUnavailable):
		writeV11Detail(w, http.StatusServiceUnavailable, "Transcript runtime is not available")
	default:
		writeV11Detail(w, http.StatusInternalServerError, "Internal workspace error")
	}
}

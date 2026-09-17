package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	workspace "synon-go/internal/persistence/workspace"
)

type compatibilityRootForkRequest struct {
	MessageIndex          *int    `json:"message_index"`
	EditedContent         string  `json:"edited_content"`
	SourceBranchID        *string `json:"source_branch_id"`
	SourceClientMessageID string  `json:"source_client_message_id"`
	ExpectedBranchID      *string `json:"expected_branch_id"`
	ExpectedGeneration    *int64  `json:"expected_generation"`
	ClientMutationID      string  `json:"client_mutation_id"`
	VerifierMode          *string `json:"verifier_mode"`
	MemoryMode            *string `json:"memory_mode"`
	UltraMode             *bool   `json:"ultra_mode"`
	PlanMode              *bool   `json:"plan_mode"`
	TargetAgent           *string `json:"target_agent"`
}

type compatibilityRootAnswerForkRequest struct {
	ToolUseID string `json:"tool_use_id"`
	Response  struct {
		Action  string          `json:"action"`
		Answers json.RawMessage `json:"answers"`
		Message string          `json:"message"`
	} `json:"response"`
	SourceBranchID     *string `json:"source_branch_id"`
	ExpectedBranchID   *string `json:"expected_branch_id"`
	ExpectedGeneration *int64  `json:"expected_generation"`
	ClientMutationID   string  `json:"client_mutation_id"`
	VerifierMode       *string `json:"verifier_mode"`
	MemoryMode         *string `json:"memory_mode"`
	UltraMode          *bool   `json:"ultra_mode"`
	PlanMode           *bool   `json:"plan_mode"`
	TargetAgent        *string `json:"target_agent"`
}

func (s *Server) handleCompatibilityRootFork(w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame) {
	if r.Method != http.MethodPost {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var input compatibilityRootForkRequest
	if err := decodeAgentCompatJSON(r, &input); err != nil {
		writeV11Detail(w, http.StatusBadRequest, "Invalid fork request: "+err.Error())
		return
	}
	if input.MessageIndex == nil || *input.MessageIndex < 0 {
		writeV11Detail(w, http.StatusBadRequest, "Invalid message index: "+fmt.Sprint(valueOrNegative(input.MessageIndex)))
		return
	}
	input.EditedContent = strings.TrimSpace(input.EditedContent)
	if input.EditedContent == "" {
		writeV11Detail(w, http.StatusBadRequest, "edited_content cannot be empty")
		return
	}
	if len(input.EditedContent) > 1<<20 {
		writeV11Detail(w, http.StatusBadRequest, "edited_content exceeds 1MB limit")
		return
	}
	for name, value := range map[string]*string{"verifier_mode": input.VerifierMode, "memory_mode": input.MemoryMode} {
		if value != nil && *value != "off" && *value != "on" {
			writeV11Detail(w, http.StatusBadRequest, name+" must be off or on")
			return
		}
	}
	found, err := s.validateCompatibilityTargetAgent(r, input.TargetAgent)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	if !found {
		writeV11Detail(w, http.StatusBadRequest, "Agent "+stringPointerValue(input.TargetAgent)+" not found in registry")
		return
	}
	s.handleCanonicalTranscriptRootFork(w, r, frame, input)
}

func (s *Server) handleCompatibilityRootForkAtAnswer(w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame) {
	if r.Method != http.MethodPost {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var input compatibilityRootAnswerForkRequest
	if err := decodeAgentCompatJSON(r, &input); err != nil {
		writeV11Detail(w, http.StatusBadRequest, "Invalid fork-at-answer request: "+err.Error())
		return
	}
	input.ToolUseID = strings.TrimSpace(input.ToolUseID)
	if input.ToolUseID == "" {
		writeV11Detail(w, http.StatusBadRequest, "tool_use_id is required")
		return
	}
	switch input.Response.Action {
	case "answer", "decide_for_me", "discuss", "cancel":
	default:
		writeV11Detail(w, http.StatusBadRequest, "response.action must be answer, decide_for_me, discuss, or cancel")
		return
	}
	for name, value := range map[string]*string{"verifier_mode": input.VerifierMode, "memory_mode": input.MemoryMode} {
		if value != nil && *value != "off" && *value != "on" {
			writeV11Detail(w, http.StatusBadRequest, name+" must be off or on")
			return
		}
	}
	found, err := s.validateCompatibilityTargetAgent(r, input.TargetAgent)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	if !found {
		writeV11Detail(w, http.StatusBadRequest, "Agent "+stringPointerValue(input.TargetAgent)+" not found in registry")
		return
	}
	s.handleCanonicalTranscriptRootForkAtAnswer(w, r, frame, input)
}

func (s *Server) validateCompatibilityTargetAgent(r *http.Request, target *string) (bool, error) {
	if target == nil {
		return true, nil
	}
	name := normalizeBundledAgentName(*target)
	found := false
	if s.agentCatalog != nil {
		_, found = s.agentCatalog.Agent(name)
	}
	if !found {
		_, profileFound, err := s.workspaceStore.GetAgent(compatAgentUserID(r), name)
		if err != nil {
			return false, fmt.Errorf("validate target agent: %w", err)
		}
		found = profileFound
	}
	*target = name
	return found, nil
}

func valueOrNegative(value *int) int {
	if value == nil {
		return -1
	}
	return *value
}

func stringPointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

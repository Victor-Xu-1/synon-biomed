package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	agentruntime "synon-go/internal/agentruntime"
	"synon-go/internal/providers"
)

// promptOptimizeMaxChars bounds the draft handed to the optimizer so a single
// rewrite stays inside one small model call.
const promptOptimizeMaxChars = 8000

const promptOptimizeSystemPrompt = `You rewrite the user's draft message into one concrete, executable scientific task instruction for a biomedical research workbench.

- Turn a vague request into an explicit task: first name the task type and state the research objective in one short sentence.
- Lay out the work as numbered steps (1. 2. 3. ..., usually three to five), each step ONE short actionable line: which database, dataset or method to use and what to do with it. Write terse instructions, not long explanatory sentences.
- End with the key deliverables (report, table, figure, protocol, script) and brief acceptance criteria, each on one short line.
- Keep the whole instruction compact — the rewrite must stay noticeably shorter than three paragraphs; drop any step the draft does not need.
- Ground the instruction in the draft only: never invent datasets, papers, parameters or results the user did not mention. If the draft lacks a needed detail, mark it briefly as an assumption inside the relevant step.
- Keep the order: objective, numbered steps, deliverables, acceptance criteria, in the draft's language.
- Do not answer the request or perform the task; output only the rewritten instruction without commentary or code fences.`

func (s *Server) handleLLMPromptOptimize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	_, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	var input struct {
		Text           string `json:"text"`
		ConversationID string `json:"conversationId"`
		ProfileID      string `json:"profileId"`
	}
	if err := decodeWorkspaceJSON(r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	draft := strings.TrimSpace(input.Text)
	if draft == "" {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "draft text is required"})
		return
	}
	if len(draft) > promptOptimizeMaxChars {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "draft text is too long"})
		return
	}
	conversationID := strings.TrimSpace(input.ConversationID)
	if conversationID == "" {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "conversation id is required"})
		return
	}
	frame, _, owned := s.webConversationAccess(w, r, conversationID)
	if !owned {
		return
	}
	userID := strings.TrimSpace(resolveUserID(r, nil))
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	selection := ""
	if profileID := strings.TrimSpace(input.ProfileID); profileID != "" {
		selection = webConversationProviderSelectionPrefix + profileID
	} else {
		var err error
		selection, err = s.webConversationFrameModelSelection(frame)
		if err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "unable to load conversation model"})
			return
		}
	}
	profile, err := s.resolveUserModelSelectionProfileWithContext(ctx, userID, selection, providers.ResolutionInput{
		RequestTimeout: 30 * time.Second, MaxAttempts: 1, MaxResponseBytes: 1 << 20,
	})
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		status := http.StatusBadRequest
		if selection == "" {
			status = http.StatusNotFound
		}
		writeWorkspaceJSON(w, status, map[string]any{"ok": false, "error": "selected model provider is unavailable"})
		return
	}
	client, err := providers.NewRuntimeModelClient(profile, s.httpClient, nil)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "selected model provider is unavailable"})
		return
	}
	result, err := client.Complete(ctx, agentruntime.ModelRequest{
		Messages: []agentruntime.Message{
			{Role: "system", Content: promptOptimizeSystemPrompt},
			{Role: "user", Content: draft},
		},
	})
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": "model provider request failed"})
		return
	}
	text := strings.TrimSpace(result.Message.Content)
	if text == "" {
		writeWorkspaceJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": "model provider returned an empty response"})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "result": map[string]any{
		"text": text, "model": firstNonEmpty(result.Model, profile.Model),
	}})
}

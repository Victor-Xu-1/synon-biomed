package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	workspace "synon-go/internal/persistence/workspace"

	agentruntime "synon-go/internal/agentruntime"
	"synon-go/internal/providers"
)

// promptOptimizeMaxChars bounds the draft handed to the optimizer so a single
// rewrite stays inside one small model call.
const promptOptimizeMaxChars = 8000

const promptOptimizeSystemPrompt = `You rewrite the user's draft message into one concrete, executable scientific task instruction for a biomedical research workbench.

- Turn a vague request into an explicit task: first name the task type (literature investigation, dataset analysis, experiment design, protocol or script development, and so on) and state the research objective in one sentence.
- Lay out the concrete work as numbered steps (1. 2. 3. ...), each step one actionable action: which databases, datasets, methods or experiments to use, what to compute or compare, and how to validate the result.
- Finish with the expected deliverables (report, comparison table, figure, protocol, reproducible script) and brief acceptance criteria for "done".
- Ground the instruction in the draft only: never invent datasets, papers, parameters or results the user did not mention. If the draft lacks a needed detail, mark it as an assumption or open question inside the relevant step.
- Keep the section order: objective first, then the numbered steps, then deliverables, then acceptance criteria, using wording natural to the draft's language.
- Reply in the same language as the draft.
- Do not answer the request or perform the task; output only the rewritten instruction without commentary or code fences.`

func (s *Server) handleLLMPromptOptimize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	var input struct {
		Text      string `json:"text"`
		ProfileID string `json:"profileId"`
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
	userID := strings.TrimSpace(resolveUserID(r, nil))
	provider, found, err := s.resolvePromptOptimizeProvider(store, userID, strings.TrimSpace(input.ProfileID))
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if !found {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "no enabled model provider configured"})
		return
	}
	profile, err := providers.BuildModelProfile(provider, s.secretStore, userID, providers.ResolutionInput{
		RequestTimeout: 30 * time.Second, MaxAttempts: 1, MaxResponseBytes: 1 << 20,
	})
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	client, err := providers.NewRuntimeModelClient(profile, s.httpClient, nil)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	result, err := client.Complete(ctx, agentruntime.ModelRequest{
		Messages: []agentruntime.Message{
			{Role: "system", Content: promptOptimizeSystemPrompt},
			{Role: "user", Content: draft},
		},
	})
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	text := strings.TrimSpace(result.Message.Content)
	if text == "" {
		writeWorkspaceJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": "model provider returned an empty response"})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "result": map[string]any{
		"text": text, "model": firstNonEmpty(result.Model, provider.Model),
	}})
}

// resolvePromptOptimizeProvider picks the provider a rewrite should run on:
// an explicit profile when given, otherwise the active conversation provider,
// falling back to the first enabled provider with a model.
func (s *Server) resolvePromptOptimizeProvider(store *workspace.Store, userID, profileID string) (workspace.ModelProvider, bool, error) {
	if profileID != "" {
		return store.GetModelProvider(userID, profileID)
	}
	providersList, err := store.ListModelProviders(userID)
	if err != nil {
		return workspace.ModelProvider{}, false, err
	}
	activeID := ""
	if s.settingsStore != nil {
		if setting, found, settingErr := s.settingsStore.Get(webConversationActiveProviderSetting); settingErr == nil && found {
			activeID = strings.TrimSpace(webString(setting.Value))
		}
	}
	var fallback *workspace.ModelProvider
	for index := range providersList {
		provider := providersList[index]
		if !provider.Enabled || strings.TrimSpace(provider.Model) == "" {
			continue
		}
		if activeID != "" && provider.ID == activeID {
			return provider, true, nil
		}
		if fallback == nil {
			fallback = &providersList[index]
		}
	}
	if fallback != nil {
		return *fallback, true, nil
	}
	return workspace.ModelProvider{}, false, nil
}

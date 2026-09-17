package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"synon-go/internal/agentruntime"
	"synon-go/internal/compat/contracts"
	secretstore "synon-go/internal/persistence/secrets"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/providers"
)

const workspaceRequestLimit = 1024 * 1024

func (s *Server) handleContractSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	summary := contracts.V11Summary()
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"ok": true, "service_methods": summary.ServiceMethodCount,
		"http_routes": summary.HTTPRouteCount, "event_types": summary.EventTypeCount,
		"query_keys": summary.QueryKeyCount,
	})
}

func (s *Server) handleContractServiceMethods(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, contracts.ServiceMethods)
}

func (s *Server) handleContractHTTPRoutes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, contracts.HTTPRoutes)
}

func (s *Server) handleContractEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, contracts.EventTypes)
}

func (s *Server) handleContractQueryKeys(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, contracts.QueryKeys)
}

func (s *Server) handleContractDomains(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, contracts.Domains())
}

func (s *Server) handleWorkspaceProjects(w http.ResponseWriter, r *http.Request) {
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		projects, err := store.ListProjectsForUser(userID, workspaceListQuery(r, "limit", 100, 1000), workspaceListQuery(r, "offset", 0, 0))
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "projects": projects})
		return
	}
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	var input struct {
		ID     string `json:"id"`
		UserID string `json:"userId"`
		Name   string `json:"name"`
		Path   string `json:"path"`
	}
	if err := decodeWorkspaceJSON(r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	project, err := store.CreateProjectRealtime(r.Context(), workspace.CreateProjectInput{ID: input.ID, UserID: userID, Name: input.Name, Path: input.Path}, "")
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "project": project})
}

func workspaceListQuery(r *http.Request, key string, defaultValue, minimum int) int {
	value := r.URL.Query().Get(key)
	if value == "" {
		return defaultValue
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum {
		return defaultValue
	}
	return parsed
}

func (s *Server) handleLLMProviders(w http.ResponseWriter, r *http.Request) {
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		userID := firstNonEmpty(resolveUserID(r, nil), r.URL.Query().Get("user_id"), r.URL.Query().Get("userId"))
		s.writeLLMProvidersSnapshot(w, store, userID)
	case http.MethodPost:
		var input struct {
			ID             string          `json:"id"`
			UserID         string          `json:"userId"`
			Name           string          `json:"name"`
			Provider       string          `json:"provider"`
			Type           string          `json:"type"`
			BaseURL        string          `json:"baseUrl"`
			Model          string          `json:"model"`
			APIKey         string          `json:"apiKey"`
			CopyAPIKeyFrom string          `json:"copyApiKeyFrom"`
			SecretRef      string          `json:"secretRef"`
			Enabled        *bool           `json:"enabled"`
			Temperature    *float64        `json:"temperature"`
			MaxTokens      json.RawMessage `json:"maxTokens"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		var maxTokens *int
		if len(input.MaxTokens) > 0 {
			if err := json.Unmarshal(input.MaxTokens, &maxTokens); err != nil {
				writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "maxTokens must be a positive integer or null"})
				return
			}
		}
		userID := firstNonEmpty(resolveUserID(r, nil), input.UserID)
		if strings.TrimSpace(userID) == "" {
			writeWorkspaceJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "model provider user id is required"})
			return
		}
		profileStyle := strings.TrimSpace(input.Provider) != ""
		providerID := strings.TrimSpace(input.ID)
		if providerID == "" {
			providerID = uuid.NewString()
		}
		existing, found, err := store.GetModelProvider(userID, providerID)
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		secretRef := firstNonEmpty(input.SecretRef, existing.SecretRef)
		if strings.TrimSpace(input.CopyAPIKeyFrom) != "" && secretRef == "" {
			if source, sourceFound, sourceErr := store.GetModelProvider(userID, input.CopyAPIKeyFrom); sourceErr != nil {
				writeWorkspaceJSON(w, workspaceStatus(sourceErr), map[string]any{"ok": false, "error": sourceErr.Error()})
				return
			} else if sourceFound {
				secretRef = source.SecretRef
			}
		}
		if strings.TrimSpace(input.APIKey) != "" {
			secretRef, err = s.saveModelProviderAPIKey(userID, providerID, secretRef, input.Name, input.APIKey)
			if err != nil {
				writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
				return
			}
		}
		providerInput := workspace.ModelProviderInput{
			ID: providerID, UserID: userID, Name: input.Name,
			Type: firstNonEmpty(input.Provider, input.Type), BaseURL: input.BaseURL,
			Model: input.Model, SecretRef: secretRef, Enabled: input.Enabled,
			Temperature: input.Temperature, MaxTokens: maxTokens, MaxTokensSet: len(input.MaxTokens) > 0,
		}
		var provider workspace.ModelProvider
		if profileStyle || found {
			provider, err = store.UpsertModelProvider(providerInput)
		} else {
			provider, err = store.RegisterModelProvider(providerInput)
		}
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if profileStyle && s.settingsStore != nil {
			if _, err := s.settingsStore.Set(webConversationActiveProviderSetting, provider.ID); err != nil {
				writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
				return
			}
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{
			"ok": true, "provider": modelProviderProjection(provider), "profile": llmProfileProjection(provider),
		})
	default:
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
	}
}

func (s *Server) handleLLMProvider(w http.ResponseWriter, r *http.Request) {
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	if r.Method != http.MethodDelete {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	userID := strings.TrimSpace(resolveUserID(r, nil))
	providerID, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/api/llm/providers/"))
	if err != nil || strings.TrimSpace(providerID) == "" || strings.Contains(providerID, "/") {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid model provider id"})
		return
	}
	removed, found, err := store.DeleteModelProvider(userID, providerID)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if !found {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "model provider not found"})
		return
	}
	s.deleteOwnedModelProviderSecret(userID, removed.SecretRef)
	if s.settingsStore != nil {
		if setting, activeFound, getErr := s.settingsStore.Get(webConversationActiveProviderSetting); getErr == nil && activeFound && strings.TrimSpace(webString(setting.Value)) == providerID {
			_, _ = s.settingsStore.Delete(webConversationActiveProviderSetting)
		}
	}
	s.writeLLMProvidersSnapshot(w, store, userID)
}

func (s *Server) handleLLMProviderTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	var input struct {
		ProfileID string `json:"profileId"`
	}
	if err := decodeWorkspaceJSON(r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	userID := strings.TrimSpace(resolveUserID(r, nil))
	provider, found, err := store.GetModelProvider(userID, input.ProfileID)
	if err != nil || !found {
		if err == nil {
			err = errors.New("model provider not found")
		}
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": err.Error()})
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
		Messages: []agentruntime.Message{{Role: "user", Content: "Reply with exactly: Provider reachable."}},
	})
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if strings.TrimSpace(result.Message.Content) == "" {
		writeWorkspaceJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": "model provider returned an empty response"})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "result": map[string]any{
		"text": result.Message.Content, "model": firstNonEmpty(result.Model, provider.Model),
	}})
}

func (s *Server) writeLLMProvidersSnapshot(w http.ResponseWriter, store *workspace.Store, userID string) {
	providersList, err := store.ListModelProviders(userID)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	output := make([]modelProviderResponse, 0, len(providersList))
	profiles := make([]map[string]any, 0, len(providersList))
	for _, provider := range providersList {
		output = append(output, modelProviderProjection(provider))
		profiles = append(profiles, llmProfileProjection(provider))
	}
	activeID := ""
	if s.settingsStore != nil {
		if setting, found, settingErr := s.settingsStore.Get(webConversationActiveProviderSetting); settingErr == nil && found {
			activeID = strings.TrimSpace(webString(setting.Value))
		}
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"ok": true, "providers": output, "profiles": profiles,
		"activeProfileId": activeID, "templates": llmProviderTemplates,
	})
}

func llmProfileProjection(provider workspace.ModelProvider) map[string]any {
	return map[string]any{
		"id": provider.ID, "name": provider.Name, "provider": provider.Type,
		"baseUrl": provider.BaseURL, "model": provider.Model,
		"temperature": provider.Temperature, "maxTokens": provider.MaxTokens,
		"createdAt":    provider.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updatedAt":    provider.UpdatedAt.UTC().Format(time.RFC3339Nano),
		"hasApiKey":    strings.TrimSpace(provider.SecretRef) != "",
		"apiKeySource": map[bool]string{true: "stored", false: "missing"}[strings.TrimSpace(provider.SecretRef) != ""],
	}
}

var llmProviderTemplates = []map[string]any{
	{"provider": "deepseek", "label": "DeepSeek", "defaultBaseUrl": "https://api.deepseek.com/v1", "modelExamples": []string{"deepseek-chat", "deepseek-reasoner"}, "protocol": "openai"},
	{"provider": "dashscope", "label": "DashScope / Qwen", "defaultBaseUrl": "https://dashscope.aliyuncs.com/compatible-mode/v1", "modelExamples": []string{"qwen-plus", "qwen-max", "qwen-turbo"}, "protocol": "openai"},
	{"provider": "dashscope-coding", "label": "DashScope Coding Plan", "defaultBaseUrl": "https://coding.dashscope.aliyuncs.com/v1", "modelExamples": []string{"qwen3-coder-plus"}, "protocol": "openai"},
	{"provider": "zhipu", "label": "Zhipu GLM", "defaultBaseUrl": "https://open.bigmodel.cn/api/paas/v4", "modelExamples": []string{"glm-4.5", "glm-4.5-air", "glm-4-plus"}, "protocol": "openai"},
	{"provider": "moonshot", "label": "Moonshot China", "defaultBaseUrl": "https://api.moonshot.cn/v1", "modelExamples": []string{"kimi-k2-0711-preview", "moonshot-v1-128k", "moonshot-v1-32k"}, "protocol": "openai"},
	{"provider": "siliconflow-cn", "label": "SiliconFlow China", "defaultBaseUrl": "https://api.siliconflow.cn/v1", "modelExamples": []string{"deepseek-ai/DeepSeek-V3", "Qwen/Qwen3-235B-A22B", "moonshotai/Kimi-K2-Instruct"}, "protocol": "openai"},
	{"provider": "siliconflow", "label": "SiliconFlow Global", "defaultBaseUrl": "https://api.siliconflow.com/v1", "modelExamples": []string{"deepseek-ai/DeepSeek-V3"}, "protocol": "openai"},
	{"provider": "moonshot-global", "label": "Moonshot Global", "defaultBaseUrl": "https://api.moonshot.ai/v1", "modelExamples": []string{"kimi-k2"}, "protocol": "openai"},
	{"provider": "minimax", "label": "MiniMax", "defaultBaseUrl": "https://api.minimaxi.com/v1", "modelExamples": []string{"MiniMax-M1", "abab6.5s-chat"}, "protocol": "openai"},
	{"provider": "xai", "label": "xAI", "defaultBaseUrl": "https://api.x.ai/v1", "modelExamples": []string{"grok-4"}, "protocol": "openai"},
	{"provider": "volcengine-ark", "label": "Volcengine Ark", "defaultBaseUrl": "https://ark.cn-beijing.volces.com/api/v3", "modelExamples": []string{"doubao-seed-1-6", "doubao-1-5-pro", "deepseek-v3"}, "protocol": "openai"},
	{"provider": "baidu-qianfan", "label": "Baidu Qianfan", "defaultBaseUrl": "https://qianfan.baidubce.com/v2", "modelExamples": []string{"ernie-4.5-turbo-128k", "ernie-x1-turbo-32k"}, "protocol": "openai"},
	{"provider": "tencent-hunyuan", "label": "Tencent Hunyuan", "defaultBaseUrl": "https://api.hunyuan.cloud.tencent.com/v1", "modelExamples": []string{"hunyuan-turbos-latest", "hunyuan-large"}, "protocol": "openai"},
	{"provider": "novita", "label": "Novita", "defaultBaseUrl": "https://api.novita.ai/openai/v1", "modelExamples": []string{"deepseek/deepseek-v3"}, "protocol": "openai"},
	{"provider": "modelscope", "label": "ModelScope", "defaultBaseUrl": "https://api-inference.modelscope.cn/v1", "modelExamples": []string{"Qwen/Qwen3-235B-A22B", "deepseek-ai/DeepSeek-V3"}, "protocol": "openai"},
	{"provider": "openrouter", "label": "OpenRouter", "defaultBaseUrl": "https://openrouter.ai/api/v1", "modelExamples": []string{"qwen/qwen3-235b-a22b", "deepseek/deepseek-chat-v3.1", "moonshotai/kimi-k2"}, "protocol": "openai"},
	{"provider": "openai", "label": "OpenAI", "defaultBaseUrl": "https://api.openai.com/v1", "modelExamples": []string{"gpt-5", "gpt-4.1"}, "protocol": "openai"},
	{"provider": "openai-responses", "label": "OpenAI Responses", "defaultBaseUrl": "https://api.openai.com/v1/responses", "modelExamples": []string{"gpt-5", "o3"}, "protocol": "openai-responses"},
	{"provider": "anthropic", "label": "Anthropic", "defaultBaseUrl": "https://api.anthropic.com", "modelExamples": []string{"claude-sonnet-4-5", "claude-opus-4-1"}, "protocol": "anthropic"},
	{"provider": "gemini", "label": "Google Gemini", "defaultBaseUrl": "https://generativelanguage.googleapis.com", "modelExamples": []string{"gemini-2.5-pro", "gemini-2.5-flash"}, "protocol": "gemini"},
	{"provider": "ppio", "label": "PPIO", "defaultBaseUrl": "https://api.ppinfra.com/v3/openai", "modelExamples": []string{"deepseek/deepseek-v3"}, "protocol": "openai"},
	{"provider": "infiniai", "label": "InfiniAI", "defaultBaseUrl": "https://cloud.infini-ai.com/maas/v1", "modelExamples": []string{"deepseek-v3"}, "protocol": "openai"},
	{"provider": "stepfun", "label": "StepFun", "defaultBaseUrl": "https://api.stepfun.com/v1", "modelExamples": []string{"step-2-16k"}, "protocol": "openai"},
	{"provider": "ollama", "label": "Ollama (local)", "defaultBaseUrl": "http://127.0.0.1:11434/v1", "modelExamples": []string{"qwen3", "llama3.3"}, "protocol": "openai"},
	{"provider": "lm-studio", "label": "LM Studio (local)", "defaultBaseUrl": "http://127.0.0.1:1234/v1", "modelExamples": []string{"local-model"}, "protocol": "openai"},
	{"provider": "vllm", "label": "vLLM (self-hosted)", "defaultBaseUrl": "http://127.0.0.1:8000/v1", "modelExamples": []string{"served-model"}, "protocol": "openai"},
	{"provider": "custom", "label": "Custom / OpenAI compatible", "defaultBaseUrl": "", "modelExamples": []string{}, "protocol": "openai"},
}

func (s *Server) saveModelProviderAPIKey(userID, providerID, currentRef, name, value string) (string, error) {
	if s.secretStore == nil {
		return "", errors.New("secret store is not configured")
	}
	secretID := modelProviderSecretID(currentRef)
	if secretID == "" {
		secretID = "model-provider-" + providerID
	}
	if _, found, err := s.secretStore.ResolveForUser(secretID, userID); err != nil {
		return "", err
	} else if found {
		if _, err := s.secretStore.UpdateForUser(secretID, userID, func(secret *secretstore.Secret) error {
			secret.Provider, secret.Name, secret.Value = "llm", strings.TrimSpace(name), strings.TrimSpace(value)
			return nil
		}); err != nil {
			return "", err
		}
	} else if _, err := s.secretStore.Create(secretstore.Secret{
		ID: secretID, UserID: userID, Provider: "llm", Name: strings.TrimSpace(name), Value: strings.TrimSpace(value),
	}); err != nil {
		return "", err
	}
	return "secret://" + secretID, nil
}

func (s *Server) deleteOwnedModelProviderSecret(userID, ref string) {
	secretID := modelProviderSecretID(ref)
	if strings.HasPrefix(secretID, "model-provider-") && s.secretStore != nil {
		_, _ = s.secretStore.DeleteForUser(secretID, userID)
	}
}

func modelProviderSecretID(ref string) string {
	parsed, err := url.Parse(strings.TrimSpace(ref))
	if err != nil || parsed.Scheme != "secret" {
		return ""
	}
	return strings.TrimPrefix(strings.TrimSpace(parsed.Host+parsed.Path), "/")
}

func (s *Server) handleWorkspaceAgents(w http.ResponseWriter, r *http.Request) {
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		agents, err := store.ListAgents(r.URL.Query().Get("user_id"))
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "agents": agents})
	case http.MethodPost:
		var input struct {
			ID           string   `json:"id"`
			UserID       string   `json:"userId"`
			Name         string   `json:"name"`
			DisplayName  string   `json:"displayName"`
			Description  string   `json:"description"`
			SystemPrompt string   `json:"systemPrompt"`
			SkillNames   []string `json:"skillNames"`
			Enabled      *bool    `json:"enabled"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		agent, err := store.CreateAgent(workspace.CreateAgentInput{ID: input.ID, UserID: input.UserID, Name: input.Name, DisplayName: input.DisplayName, Description: input.Description, SystemPrompt: input.SystemPrompt, SkillNames: input.SkillNames, Enabled: input.Enabled})
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "agent": agent})
	default:
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
	}
}

func (s *Server) handleWorkspaceAgent(w http.ResponseWriter, r *http.Request) {
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	segments := workspacePathSegments(strings.TrimPrefix(r.URL.Path, "/api/go/agents/"))
	if len(segments) == 0 || len(segments) > 3 {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "workspace endpoint not found"})
		return
	}
	name, err := url.PathUnescape(segments[0])
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid agent name"})
		return
	}

	if len(segments) == 1 {
		s.handleWorkspaceAgentRecord(w, r, store, name)
		return
	}
	switch segments[1] {
	case "enabled":
		if len(segments) != 2 || r.Method != http.MethodPut {
			break
		}
		var input struct {
			UserID  string `json:"userId"`
			Enabled bool   `json:"enabled"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if err := store.SetAgentEnabled(input.UserID, name, input.Enabled); err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	case "prompt":
		if len(segments) == 2 {
			s.handleWorkspaceAgentPrompt(w, r, store, name)
			return
		}
	case "connectors":
		s.handleWorkspaceAgentConnectors(w, r, store, name, segments)
		return
	case "connector-exclusions":
		if len(segments) == 2 {
			s.handleWorkspaceAgentConnectorExclusions(w, r, store, name)
			return
		}
	case "skills":
		s.handleWorkspaceAgentSkills(w, r, store, name, segments)
		return
	}
	writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "workspace endpoint not found"})
}

func (s *Server) handleWorkspaceAgentRecord(w http.ResponseWriter, r *http.Request, store *workspace.Store, name string) {
	switch r.Method {
	case http.MethodGet:
		agent, found, err := store.GetAgent(r.URL.Query().Get("user_id"), name)
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if !found {
			writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "agent not found"})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "agent": agent})
	case http.MethodPatch:
		var input struct {
			UserID       string  `json:"userId"`
			DisplayName  *string `json:"displayName"`
			Description  *string `json:"description"`
			SystemPrompt *string `json:"systemPrompt"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		agent, err := store.UpdateAgent(input.UserID, name, workspace.UpdateAgentInput{
			DisplayName: input.DisplayName, Description: input.Description, SystemPrompt: input.SystemPrompt,
		})
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "agent": agent})
	case http.MethodDelete:
		if err := store.DeleteAgent(r.URL.Query().Get("user_id"), name); err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
	}
}

func (s *Server) handleWorkspaceAgentSkills(w http.ResponseWriter, r *http.Request, store *workspace.Store, name string, segments []string) {
	if len(segments) == 2 && r.Method == http.MethodPut {
		var input struct {
			UserID     string   `json:"userId"`
			SkillNames []string `json:"skillNames"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		agent, err := store.SetAgentSkills(input.UserID, name, input.SkillNames)
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "agent": agent})
		return
	}
	if len(segments) != 3 || (r.Method != http.MethodPost && r.Method != http.MethodDelete) {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "workspace endpoint not found"})
		return
	}
	skillName, err := url.PathUnescape(segments[2])
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid skill name"})
		return
	}
	userID := r.URL.Query().Get("user_id")
	if r.Method == http.MethodPost {
		var input struct {
			UserID string `json:"userId"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		userID = input.UserID
	}
	var agent workspace.Agent
	if r.Method == http.MethodPost {
		agent, err = store.AddAgentSkill(userID, name, skillName)
	} else {
		agent, err = store.RemoveAgentSkill(userID, name, skillName)
	}
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "agent": agent})
}

type modelProviderResponse struct {
	ID                   string   `json:"id"`
	UserID               string   `json:"userId"`
	Name                 string   `json:"name"`
	Type                 string   `json:"type"`
	BaseURL              string   `json:"baseUrl"`
	Model                string   `json:"model"`
	Temperature          *float64 `json:"temperature,omitempty"`
	MaxTokens            *int     `json:"maxTokens,omitempty"`
	Enabled              bool     `json:"enabled"`
	CredentialConfigured bool     `json:"credentialConfigured"`
}

func modelProviderProjection(provider workspace.ModelProvider) modelProviderResponse {
	return modelProviderResponse{ID: provider.ID, UserID: provider.UserID, Name: provider.Name, Type: provider.Type, BaseURL: provider.BaseURL, Model: provider.Model, Temperature: provider.Temperature, MaxTokens: provider.MaxTokens, Enabled: provider.Enabled, CredentialConfigured: provider.SecretRef != ""}
}

func (s *Server) handleWorkspaceProject(w http.ResponseWriter, r *http.Request) {
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	segments := workspacePathSegments(strings.TrimPrefix(r.URL.Path, "/api/go/projects/"))
	if len(segments) == 0 || len(segments) > 2 {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "workspace endpoint not found"})
		return
	}
	if len(segments) == 1 && segments[0] == "dashboard" {
		s.handleWorkspaceDashboard(w, r, store, userID)
		return
	}
	if len(segments) == 1 && segments[0] == "processing-counts" {
		s.handleWorkspaceProcessingCounts(w, r, store, userID)
		return
	}
	if len(segments) == 2 && segments[0] == "batch" {
		switch segments[1] {
		case "benches", "artifacts":
			s.handleWorkspaceProjectBatch(w, r, store, userID, segments[1])
			return
		default:
			writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "workspace batch endpoint not found"})
			return
		}
	}

	projectID, err := url.PathUnescape(segments[0])
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid project id"})
		return
	}
	if !workspaceProjectOwned(w, store, projectID, userID) {
		return
	}
	if len(segments) == 1 {
		switch r.Method {
		case http.MethodGet:
			project, found, err := store.GetProject(projectID)
			if err != nil {
				writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
				return
			}
			if !found {
				writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "project not found"})
				return
			}
			writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "project": project})
		case http.MethodPatch:
			var input struct {
				Name *string `json:"name"`
				Path *string `json:"path"`
			}
			if err := decodeWorkspaceJSON(r, &input); err != nil {
				writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			project, err := store.UpdateProjectRealtime(r.Context(), projectID, userID, workspace.UpdateProjectInput{Name: input.Name, Path: input.Path}, "")
			if err != nil {
				writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "project": project})
		case http.MethodDelete:
			rootFrameIDs, err := store.ListCompatibilityProjectRootFrameIDs(r.Context(), userID, projectID)
			if err != nil {
				writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
				return
			}
			cleanupCtx, cancelCleanup := compatibilityRuntimeCleanupContext(r.Context())
			defer cancelCleanup()
			cleanupWarnings := make([]error, 0)
			for _, rootFrameID := range rootFrameIDs {
				cleanupWarnings = append(cleanupWarnings, s.stopCompatibilityFrameRuntime(cleanupCtx, rootFrameID)...)
			}
			blobPaths, err := store.DeleteProjectRealtime(r.Context(), projectID, userID, "")
			if err != nil {
				writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
				return
			}
			if err := store.RemoveProjectArtifactBlobs(blobPaths); err != nil {
				cleanupWarnings = append(cleanupWarnings, err)
			}
			for _, rootFrameID := range rootFrameIDs {
				cleanupWarnings = append(cleanupWarnings, s.stopCompatibilityFrameRuntime(cleanupCtx, rootFrameID)...)
				cleanupWarnings = append(cleanupWarnings, s.removeCompatibilityFrameRuntime(rootFrameID)...)
			}
			if err := s.removeCompatibilityProjectRuntime(projectID); err != nil {
				cleanupWarnings = append(cleanupWarnings, err)
			}
			if len(cleanupWarnings) > 0 {
				log.Printf("workspace project delete committed with cleanup warnings: project_id=%s failures=%d errors=%v",
					projectID, len(cleanupWarnings), errors.Join(cleanupWarnings...))
			}
			writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "cleanup_warnings": len(cleanupWarnings)})
		default:
			writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		}
		return
	}
	if segments[1] == "request" {
		s.handleWorkspaceProjectRequest(w, r, store, projectID)
		return
	}
	if segments[1] == "benches" {
		s.handleWorkspaceProjectBenches(w, r, store, projectID)
		return
	}
	if segments[1] == "folders" {
		s.handleWorkspaceProjectFolders(w, r, store, projectID, userID)
		return
	}
	if segments[1] == "notes" {
		s.handleWorkspaceProjectNotes(w, r, store, projectID, userID)
		return
	}
	if segments[1] == "artifacts" {
		if r.Method != http.MethodGet {
			writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
			return
		}
		artifacts, err := store.ListArtifacts(projectID, workspaceListQuery(r, "limit", 100, 1000), workspaceListQuery(r, "offset", 0, 0))
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		visible := make([]workspace.Artifact, 0, len(artifacts))
		for _, artifact := range artifacts {
			if isRunnerLargeToolResultArtifactID(artifact.ID) {
				continue
			}
			retention, intermediate, found, stateErr := store.ArtifactCurrentPresentationState(artifact.ID)
			if stateErr != nil {
				writeWorkspaceJSON(w, workspaceStatus(stateErr), map[string]any{"ok": false, "error": stateErr.Error()})
				return
			}
			if !found || retention == "working_data" || intermediate {
				continue
			}
			visible = append(visible, artifact)
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "artifacts": visible})
		return
	}
	if segments[1] != "frames" {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "workspace endpoint not found"})
		return
	}
	if r.Method == http.MethodGet {
		frames, err := store.ListFrames(projectID, workspaceListQuery(r, "limit", 100, 1000), workspaceListQuery(r, "offset", 0, 0))
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "frames": frames})
		return
	}
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	var input struct {
		ID               string `json:"id"`
		ParentFrameID    string `json:"parentFrameId"`
		AgentName        string `json:"agentName"`
		Status           string `json:"status"`
		ConversationType string `json:"conversationType"`
		Name             string `json:"name"`
	}
	if err := decodeWorkspaceJSON(r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	frame, err := store.CreateFrameRealtime(r.Context(), workspace.CreateFrameInput{ID: input.ID, ProjectID: projectID, ParentFrameID: input.ParentFrameID, AgentName: input.AgentName, Status: input.Status, ConversationType: input.ConversationType, Name: input.Name}, userID, "", "")
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "frame": frame})
}

func (s *Server) handleWorkspaceFrame(w http.ResponseWriter, r *http.Request) {
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	segments := workspacePathSegments(strings.TrimPrefix(r.URL.Path, "/api/go/frames/"))
	if len(segments) == 0 || len(segments) > 4 {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "workspace endpoint not found"})
		return
	}
	if len(segments) == 2 && isRemovedWorkspaceFrameControl(segments[1]) {
		writeRemovedWorkspaceFrameControl(w, segments[1])
		return
	}
	frameID, err := url.PathUnescape(segments[0])
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid frame id"})
		return
	}
	frame, found, err := store.GetFrame(frameID)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if !found || !workspaceProjectOwned(w, store, frame.ProjectID, userID) {
		if !found {
			writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "frame not found"})
		}
		return
	}
	if len(segments) > 1 {
		switch segments[1] {
		case "messages":
			s.handleWorkspaceFrameMessages(w, r, store, frameID, segments)
			return
		case "read-cursor":
			if len(segments) == 2 {
				s.handleWorkspaceFrameReadCursor(w, r, store, frameID)
				return
			}
		case "cancel":
			if len(segments) == 2 {
				s.handleWorkspaceFrameCancel(w, r, store, frameID)
				return
			}
		case "move":
			if len(segments) == 2 {
				s.handleWorkspaceFrameMove(w, r, store, userID, frameID)
				return
			}
		case "resume":
			if len(segments) == 2 {
				s.handleWorkspaceFrameResume(w, r, store, frameID)
				return
			}
		case "fork":
			if len(segments) == 2 {
				s.handleWorkspaceFrameBranch(w, r, store, frameID, workspace.FrameBranchEdit)
				return
			}
		case "fork-at-answer":
			if len(segments) == 2 {
				s.handleWorkspaceFrameBranch(w, r, store, frameID, workspace.FrameBranchAnswer)
				return
			}
		case "aside":
			if len(segments) == 2 {
				s.handleWorkspaceFrameBranch(w, r, store, frameID, workspace.FrameBranchAside)
				return
			}
		case "discard-plan":
			if len(segments) == 2 {
				s.handleWorkspaceFrameControl(w, r, store, frameID, frameControlDiscardPlan)
				return
			}
		case "streaming":
			if len(segments) == 2 {
				s.handleWorkspaceFrameStreaming(w, r, store, frameID)
				return
			}
		case "streaming-batch":
			if len(segments) == 2 {
				s.handleWorkspaceFrameStreamingBatch(w, r, store)
				return
			}
		case "cross-session-refs":
			if len(segments) == 2 {
				s.handleWorkspaceCrossSessionReferences(w, r, store, frameID)
				return
			}
		case "compaction-archives":
			if len(segments) == 3 {
				s.handleWorkspaceCompactionArchive(w, r, store, frameID, segments[2])
				return
			}
		case "compaction-messages":
			if len(segments) == 2 {
				s.handleWorkspaceCompactionMessages(w, r, store, frameID)
				return
			}
		case "artifacts":
			if len(segments) == 2 {
				s.handleWorkspaceFrameArtifacts(w, r, store, frameID)
				return
			}
		}
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "workspace endpoint not found"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "frame": frame})
	case http.MethodPatch:
		var input struct {
			Status *string `json:"status"`
			Name   *string `json:"name"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		frame, err := store.UpdateFrameRealtime(r.Context(), frameID, userID, workspace.UpdateFrameInput{Status: input.Status, Name: input.Name}, "", "")
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "frame": frame})
	case http.MethodDelete:
		if err := store.DeleteFrameRealtime(r.Context(), frame, userID, ""); err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
	}
}

func (s *Server) handleWorkspaceArtifact(w http.ResponseWriter, r *http.Request) {
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	segments := workspacePathSegments(strings.TrimPrefix(r.URL.Path, "/api/go/artifacts/"))
	if len(segments) == 0 || len(segments) > 3 {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "workspace endpoint not found"})
		return
	}
	if len(segments) == 1 && segments[0] == "bulk-move" {
		s.handleWorkspaceArtifactBulkMove(w, r, store, userID)
		return
	}
	artifactID, err := url.PathUnescape(segments[0])
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid artifact id"})
		return
	}
	existingArtifact, artifactFound, err := store.GetArtifact(artifactID)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if artifactFound && !workspaceProjectOwned(w, store, existingArtifact.ProjectID, userID) {
		return
	}
	if len(segments) == 3 {
		if segments[1] == "versions" && segments[2] == "binary" {
			s.handleWorkspaceArtifactBinaryVersion(w, r, store, userID, artifactID)
			return
		}
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "workspace endpoint not found"})
		return
	}
	if len(segments) == 1 {
		switch r.Method {
		case http.MethodGet:
			artifact, found, err := store.GetArtifact(artifactID)
			if err != nil {
				writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
				return
			}
			if !found {
				writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "artifact not found"})
				return
			}
			writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "artifact": artifact})
		case http.MethodPatch:
			var input struct {
				Name string `json:"name"`
			}
			if err := decodeWorkspaceJSON(r, &input); err != nil {
				writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			artifact, err := store.RenameArtifactRealtime(workspaceMutationContext(r), artifactID, userID, input.Name)
			if err != nil {
				writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "artifact": artifact})
		case http.MethodDelete:
			blobPaths, err := store.DeleteArtifactRealtime(workspaceMutationContext(r), artifactID, userID)
			if err != nil {
				writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
				return
			}
			if err := store.RemoveArtifactBlobs(blobPaths); err != nil {
				log.Printf("clean deleted artifact %s blobs: %v", artifactID, err)
			}
			writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true})
		default:
			writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		}
		return
	}
	switch segments[1] {
	case "versions":
		if r.Method == http.MethodGet {
			lineage, err := store.ArtifactLineage(artifactID)
			if err != nil {
				writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
				return
			}
			versions := make([]map[string]any, 0, len(lineage))
			for _, version := range lineage {
				versions = append(versions, artifactVersionMetadata(version))
			}
			writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "versions": versions})
			return
		}
		if r.Method != http.MethodPost {
			writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
			return
		}
		var input struct {
			ProjectID string `json:"projectId"`
			Name      string `json:"name"`
			Kind      string `json:"kind"`
			Content   string `json:"content"`
			CreatedBy string `json:"createdBy"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if !workspaceProjectOwned(w, store, input.ProjectID, userID) {
			return
		}
		artifact, version, err := store.SaveArtifactVersionRealtime(workspaceMutationContext(r), workspace.SaveArtifactVersionInput{ArtifactID: artifactID, ProjectID: input.ProjectID, Name: input.Name, Kind: input.Kind, Content: []byte(input.Content), CreatedBy: input.CreatedBy}, userID)
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "artifact": artifact, "version": version})
	case "content":
		s.handleWorkspaceArtifactCurrentContent(w, r, store, artifactID)
	case "text":
		s.handleWorkspaceArtifactText(w, r, store, artifactID)
	case "copy":
		s.handleWorkspaceArtifactCopy(w, r, store, userID, artifactID)
	case "lineage":
		if r.Method != http.MethodGet {
			writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
			return
		}
		lineage, err := store.ArtifactLineage(artifactID)
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "lineage": lineage})
	case "priority":
		s.handleWorkspaceArtifactPriority(w, r, store, artifactID, userID)
	case "folder":
		s.handleWorkspaceArtifactFolder(w, r, store, artifactID, userID)
	default:
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "workspace endpoint not found"})
	}
}

func (s *Server) workspaceForRequest(w http.ResponseWriter) (*workspace.Store, bool) {
	if s.workspaceStore == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "workspace runtime is not configured"})
		return nil, false
	}
	return s.workspaceStore, true
}

func workspaceProjectOwned(w http.ResponseWriter, store *workspace.Store, projectID, userID string) bool {
	owned, err := store.ProjectOwnedBy(projectID, userID)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return false
	}
	if !owned {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "resource not found"})
		return false
	}
	return true
}

func workspacePathSegments(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool { return r == '/' })
}

func decodeWorkspaceJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, workspaceRequestLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return err
	}
	return nil
}

func writeWorkspaceJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func workspaceStatus(err error) int {
	if errors.Is(err, workspace.ErrMutationIdempotencyConflict) || errors.Is(err, workspace.ErrReadCursorConflict) ||
		errors.Is(err, transcriptstore.ErrBranchStateStale) {
		return http.StatusConflict
	}
	return http.StatusBadRequest
}

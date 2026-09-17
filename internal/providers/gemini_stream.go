package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"synon-go/internal/agentruntime"
)

type geminiStreamAccumulator struct {
	requestID        string
	content          strings.Builder
	toolCalls        []agentruntime.ToolCall
	usage            providerTokenUsage
	emitted          bool
	chunks           int
	terminated       bool
	toolBoundarySent bool
	reasoningSent    bool
}

func (c *streamingRuntimeModelClient) completeGeminiStream(ctx context.Context, request agentruntime.ModelRequest, emit func(agentruntime.ModelStreamEvent) error) (agentruntime.ModelResponse, error) {
	systemInstruction, contents, err := geminiContentsFromRuntime(request.Messages)
	if err != nil {
		return agentruntime.ModelResponse{}, err
	}
	generationConfig := map[string]any{"temperature": *modelTemperature(request)}
	if request.MaxTokens > 0 {
		generationConfig["maxOutputTokens"] = request.MaxTokens
	}
	payloadBody := geminiGenerateContentRequest{
		Contents: contents, Tools: geminiToolsFromRuntime(request.Tools),
		GenerationConfig: generationConfig, ToolConfig: geminiToolConfig(request.ToolChoice),
	}
	if systemInstruction != nil {
		payloadBody.SystemInstruction = systemInstruction
	}
	payload, err := json.Marshal(payloadBody)
	if err != nil {
		return agentruntime.ModelResponse{}, err
	}
	endpoint, err := geminiStreamEndpoint(c.profile.Provider.Endpoint)
	if err != nil {
		return agentruntime.ModelResponse{}, err
	}
	return c.completeNativeStream(ctx, request, emit, endpoint, payload, c.geminiResponseDecoder, readGeminiStream)
}

func geminiStreamEndpoint(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", errors.New("Gemini stream endpoint must be an absolute HTTP URL")
	}
	switch {
	case strings.HasSuffix(parsed.Path, ":generateContent"):
		parsed.Path = strings.TrimSuffix(parsed.Path, ":generateContent") + ":streamGenerateContent"
	case strings.HasSuffix(parsed.Path, ":streamGenerateContent"):
	default:
		return "", errors.New("Gemini endpoint must end with :generateContent or :streamGenerateContent")
	}
	query := parsed.Query()
	query.Set("alt", "sse")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func readGeminiStream(client *runtimeModelClient, response http.Response, emit func(agentruntime.ModelStreamEvent) error) (agentruntime.ModelResponse, string, providerTokenUsage, bool, error) {
	acc := geminiStreamAccumulator{}
	err := readNativeSSE(response.Body, client.profile.Request.MaxResponseBytes, func(_ string, raw []byte) error {
		var chunk geminiGenerateContentResponse
		if err := json.Unmarshal(raw, &chunk); err != nil {
			return fmt.Errorf("decode Gemini stream chunk: %w", err)
		}
		acc.chunks++
		if value := strings.TrimSpace(chunk.ResponseID); value != "" {
			acc.requestID = value
		}
		if chunk.UsageMetadata.PromptTokenCount != 0 {
			acc.usage.PromptTokens = chunk.UsageMetadata.PromptTokenCount
		}
		if chunk.UsageMetadata.CandidatesTokenCount != 0 {
			acc.usage.CompletionTokens = chunk.UsageMetadata.CandidatesTokenCount
		}
		if chunk.UsageMetadata.CachedContentTokenCount != 0 {
			acc.usage.CacheReadTokens = chunk.UsageMetadata.CachedContentTokenCount
		}
		if chunk.UsageMetadata.TotalTokenCount != 0 {
			acc.usage.TotalTokens = chunk.UsageMetadata.TotalTokenCount
		}
		if len(chunk.Candidates) == 0 {
			return nil
		}
		if len(chunk.Candidates) != 1 {
			return errors.New("Gemini stream returned an unexpected candidate count")
		}
		candidate := chunk.Candidates[0]
		hasSemanticOutput := len(candidate.Content.Parts) != 0
		if acc.terminated && hasSemanticOutput {
			return errors.New("Gemini stream emitted output after its terminal reason")
		}
		for _, part := range candidate.Content.Parts {
			if part.Thought && part.Text != "" && !acc.reasoningSent {
				if err := emit(agentruntime.ModelStreamEvent{
					Kind: agentruntime.ModelStreamEventPrivateReasoning, ReasoningActive: true,
				}); err != nil {
					return err
				}
				acc.reasoningSent = true
			}
			if !part.Thought && part.Text != "" {
				acc.content.WriteString(part.Text)
				acc.emitted = true
				if err := emit(agentruntime.ModelStreamEvent{
					Kind: agentruntime.ModelStreamEventContentDelta, ContentDelta: part.Text,
				}); err != nil {
					return err
				}
			}
			if part.FunctionCall != nil {
				name, err := normalizeModelToolName(part.FunctionCall.Name)
				if err != nil {
					return err
				}
				if !acc.toolBoundarySent {
					if err := emit(agentruntime.ModelStreamEvent{Kind: agentruntime.ModelStreamEventToolCallBoundary}); err != nil {
						return err
					}
					acc.toolBoundarySent = true
				}
				rawArguments, err := json.Marshal(part.FunctionCall.Args)
				if err != nil {
					return err
				}
				if len(rawArguments) == 0 || string(rawArguments) == "null" {
					rawArguments = []byte("{}")
				}
				acc.toolCalls = append(acc.toolCalls, agentruntime.ToolCall{
					ID:   fmt.Sprintf("gemini_call_%d", len(acc.toolCalls)+1),
					Name: name, Arguments: json.RawMessage(rawArguments),
				})
			}
		}
		if finishReason := strings.ToUpper(strings.TrimSpace(candidate.FinishReason)); finishReason != "" {
			switch finishReason {
			case "STOP":
				acc.terminated = true
			case "MAX_TOKENS":
				return fmt.Errorf("%w: finish reason max_tokens", errProviderResponseTruncated)
			default:
				return errors.New("Gemini stream terminated without a usable result")
			}
		}
		return nil
	})
	if err != nil {
		if acc.emitted && len(acc.toolCalls) == 0 && isRecoverableProviderStreamFailure(err) {
			err = newProviderStreamRecoveryCandidate(err)
		}
		return agentruntime.ModelResponse{}, acc.requestID, acc.usage, acc.emitted, err
	}
	if acc.usage.TotalTokens == 0 {
		acc.usage.TotalTokens = acc.usage.PromptTokens + acc.usage.CompletionTokens
	}
	if acc.chunks == 0 {
		return agentruntime.ModelResponse{}, acc.requestID, acc.usage, acc.emitted, errors.New("Gemini stream returned no chunks")
	}
	if !acc.terminated {
		streamErr := fmt.Errorf("%w: %w", errProviderResponseTruncated, errProviderStreamIncomplete)
		if acc.emitted && len(acc.toolCalls) == 0 {
			streamErr = newProviderStreamRecoveryCandidate(streamErr)
		}
		return agentruntime.ModelResponse{}, acc.requestID, acc.usage, acc.emitted,
			streamErr
	}
	content := acc.content.String()
	if strings.TrimSpace(content) == "" && len(acc.toolCalls) == 0 {
		return agentruntime.ModelResponse{}, acc.requestID, acc.usage, acc.emitted,
			newProviderEmptyResponseError("Gemini stream response has no output")
	}
	return agentruntime.ModelResponse{Message: agentruntime.Message{
		Role: "assistant", Content: content, ToolCalls: acc.toolCalls,
	}}, acc.requestID, acc.usage, acc.emitted, nil
}

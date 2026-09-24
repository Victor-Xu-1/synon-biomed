package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"synon-go/internal/agentruntime"
	"synon-go/internal/persistence/runtimekv"
)

// Context usage describes one main-agent request, never the cumulative billing
// counter or a reviewer/translation request. Only numeric metadata is retained.
type runnerContextUsage struct {
	SessionID      string                  `json:"sessionId"`
	RequestID      string                  `json:"requestId"`
	Attempt        int                     `json:"attempt"`
	Model          string                  `json:"model"`
	ObservedAt     time.Time               `json:"observedAt"`
	State          string                  `json:"state"`
	Source         string                  `json:"source"`
	UsedTokens     int                     `json:"usedTokens"`
	LimitTokens    int                     `json:"limitTokens"`
	LimitSource    string                  `json:"limitSource"`
	OutputTokens   int                     `json:"outputTokens"`
	HasMedia       bool                    `json:"hasMedia"`
	InputEstimates []runnerContextUsageRow `json:"inputEstimates"`
}

type runnerContextUsageRow struct {
	Key    string `json:"key"`
	Tokens int    `json:"tokens"`
}

var errLegacyRunnerContextUsage = errors.New("legacy three-category context usage")

var contextUsageCategoryKeys = [...]string{"systemPrompt", "tools", "messages", "mcp", "skills"}

type sessionContextUsageRecorder struct {
	store       *runtimekv.Store
	sessionID   string
	attempt     int
	limit       int
	limitSource string
	frameExists func() (bool, error)
}

type auxiliaryContextUsageKey struct{}

// Localization and other presentation-only calls use the same dynamic model
// client but are not the agent's actual context window. Their usage remains in
// provider audit; it must not replace the main request shown in the composer.
func withAuxiliaryContextUsage(ctx context.Context) context.Context {
	return context.WithValue(ctx, auxiliaryContextUsageKey{}, true)
}

func contextUsageRecorderForCall(ctx context.Context, recorder *sessionContextUsageRecorder) *sessionContextUsageRecorder {
	if ctx != nil && ctx.Value(auxiliaryContextUsageKey{}) == true {
		return nil
	}
	return recorder
}

func newSessionContextUsageRecorder(s *Server, sessionID string, attempt int, options SessionRunnerChatOptions) *sessionContextUsageRecorder {
	if s == nil || s.runtimeStore == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	limitSource := "runner_default"
	for _, key := range []string{"contextWindow", "context_window", "contextLimit", "context_limit"} {
		if value := int(numberValue(options.RuntimeSessionConfig[key])); value > 0 && value <= 10_000_000 {
			limitSource = "configured"
			break
		}
	}
	recorder := &sessionContextUsageRecorder{
		store: s.runtimeStore, sessionID: sessionID, attempt: attempt,
		limit: runnerContextWindow(options), limitSource: limitSource,
	}
	if s.workspaceStore != nil {
		recorder.frameExists = func() (bool, error) {
			_, found, err := s.workspaceStore.GetFrame(sessionID)
			return found, err
		}
	}
	return recorder
}

func contextUsageNamespace(sessionID string) string {
	digest := sha256.Sum256([]byte(sessionID))
	return "context-usage-" + hex.EncodeToString(digest[:])
}

func estimateRunnerRequestUsage(request agentruntime.ModelRequest) ([]runnerContextUsageRow, bool, error) {
	rows := make([]runnerContextUsageRow, len(contextUsageCategoryKeys))
	for index, key := range contextUsageCategoryKeys {
		rows[index].Key = key
	}
	hasMedia := false
	for _, message := range request.Messages {
		tokens := 4 + estimateTextTokens(message.Role) + estimateTextTokens(message.Content)
		for _, part := range message.Parts {
			if part.Type == agentruntime.ContentPartText {
				tokens += estimateTextTokens(part.Text)
			} else {
				hasMedia = true
			}
		}
		for _, call := range message.ToolCalls {
			tokens += estimateTextTokens(call.ID) + estimateTextTokens(call.Name) + estimateTextTokens(string(call.Arguments))
		}
		category := 2 // Conversation history and generated/tool messages.
		switch message.ContextUsageSource {
		case agentruntime.ContextUsageSystemPrompt:
			category = 0
		case agentruntime.ContextUsageMCP:
			category = 3
		case agentruntime.ContextUsageSkills:
			category = 4
		case agentruntime.ContextUsageMessages:
			// Explicitly attributed replay may retain system priority.
		default:
			if message.Role == "system" || message.Role == "developer" {
				category = 0
			}
		}
		rows[category].Tokens += tokens
	}
	for _, tool := range request.Tools {
		// Runtime-only capabilities, exposure and output contracts are not sent
		// as provider function definitions and must not inflate their estimate.
		raw, err := json.Marshal(struct {
			Name        string         `json:"name"`
			Description string         `json:"description,omitempty"`
			Parameters  map[string]any `json:"parameters,omitempty"`
		}{tool.Name, tool.Description, tool.Parameters})
		if err != nil {
			return nil, false, fmt.Errorf("estimate context tool schema: %w", err)
		}
		category := 1 // Provider-visible tool/subagent definition.
		for _, capability := range tool.Capabilities {
			if capability == "mcp" {
				category = 3
				break
			}
		}
		rows[category].Tokens += estimateTextTokens(string(raw))
	}
	return rows, hasMedia, nil
}

func (recorder *sessionContextUsageRecorder) begin(model string, request agentruntime.ModelRequest) *runnerContextUsage {
	if recorder == nil {
		return nil
	}
	if recorder.frameExists != nil {
		found, err := recorder.frameExists()
		if err != nil {
			log.Printf("context_usage_frame_check_failed session=%s: %v", recorder.sessionID, err)
			return nil
		}
		if !found {
			return nil
		}
	}
	rows, hasMedia, err := estimateRunnerRequestUsage(request)
	if err != nil {
		log.Printf("context_usage_estimate_failed session=%s: %v", recorder.sessionID, err)
		return nil
	}
	snapshot := runnerContextUsage{
		SessionID: recorder.sessionID, RequestID: uuid.NewString(), Attempt: recorder.attempt,
		Model: model, ObservedAt: time.Now().UTC(), State: "request", Source: "estimated",
		LimitTokens: recorder.limit, LimitSource: recorder.limitSource,
		InputEstimates: rows, HasMedia: hasMedia,
	}
	for _, row := range rows {
		snapshot.UsedTokens += row.Tokens
	}
	if err := recorder.persist(snapshot, false); err != nil {
		log.Printf("context_usage_write_failed session=%s request=%s: %v", recorder.sessionID, snapshot.RequestID, err)
		return nil
	}
	return &snapshot
}

func (recorder *sessionContextUsageRecorder) finish(snapshot *runnerContextUsage, response agentruntime.ModelResponse, callErr error) {
	if recorder == nil || snapshot == nil {
		return
	}
	snapshot.State = "failed"
	if callErr == nil {
		snapshot.State = "complete"
		usage := response.Usage
		if usage.InputTokens >= 0 && usage.OutputTokens >= 0 && usage.TotalTokens >= 0 &&
			(usage.InputTokens > 0 || usage.OutputTokens > 0 || usage.TotalTokens > 0) {
			snapshot.Source = "provider"
			snapshot.OutputTokens = usage.OutputTokens
			// Some providers report cached input separately; their total already
			// includes it. Never add cache counters to that total a second time.
			snapshot.UsedTokens = maxInt(usage.TotalTokens, usage.InputTokens+usage.OutputTokens)
		} else {
			rows, _, err := estimateRunnerRequestUsage(agentruntime.ModelRequest{Messages: []agentruntime.Message{response.Message}})
			if err == nil {
				for _, row := range rows {
					snapshot.OutputTokens += row.Tokens
				}
				snapshot.UsedTokens += snapshot.OutputTokens
			}
		}
	}
	// A metrics write must not turn successful model execution into a retry.
	// On failure the durable request estimate remains explicitly an estimate.
	if err := recorder.persist(*snapshot, true); err != nil {
		log.Printf("context_usage_write_failed session=%s request=%s: %v", recorder.sessionID, snapshot.RequestID, err)
	}
}

func (recorder *sessionContextUsageRecorder) persist(snapshot runnerContextUsage, completing bool) error {
	return recorder.store.EditNamespace(contextUsageNamespace(recorder.sessionID), func(entries map[string]runtimekv.Entry) (bool, error) {
		// Task deletion commits the frame removal before taking this same
		// runtime-store lock for scoped cleanup. Checking under the lock means
		// a pending write either precedes cleanup or observes the removed frame.
		if recorder.frameExists != nil {
			found, err := recorder.frameExists()
			if err != nil || !found {
				return false, err
			}
		}
		if entry, found := entries["latest"]; found {
			previous, err := decodeRunnerContextUsage(entry)
			if err != nil && !(errors.Is(err, errLegacyRunnerContextUsage) && !completing) {
				return false, err
			}
			if previous.SessionID != recorder.sessionID {
				return false, errors.New("context usage record belongs to another session")
			}
			// The SQLite namespace lock orders begins within an attempt. Wall
			// clocks can move backwards and must not fence a newer request.
			// A completion can update only the exact request it began.
			if previous.Attempt > snapshot.Attempt || (completing && previous.RequestID != snapshot.RequestID) {
				return false, nil
			}
		} else if completing {
			return false, nil // Do not recreate a deleted task's operational state.
		}
		entries["latest"] = runtimekv.Entry{Value: snapshot}
		return true, nil
	})
}

func decodeRunnerContextUsage(entry runtimekv.Entry) (runnerContextUsage, error) {
	var snapshot runnerContextUsage
	raw, err := json.Marshal(entry.Value)
	var fields map[string]json.RawMessage
	if err == nil {
		err = json.Unmarshal(raw, &snapshot)
	}
	if err == nil {
		err = json.Unmarshal(raw, &fields)
	}
	if err != nil {
		return snapshot, fmt.Errorf("decode context usage: %w", err)
	}
	for _, field := range []string{"sessionId", "requestId", "attempt", "model", "observedAt", "state", "source", "usedTokens", "limitTokens", "limitSource", "outputTokens", "hasMedia", "inputEstimates"} {
		value, found := fields[field]
		if !found || len(value) == 0 || string(value) == "null" {
			return snapshot, errors.New("invalid context usage record")
		}
	}
	var rowFields []map[string]json.RawMessage
	if err := json.Unmarshal(fields["inputEstimates"], &rowFields); err != nil || len(rowFields) != len(snapshot.InputEstimates) {
		return snapshot, errors.New("invalid context usage breakdown")
	}
	for _, row := range rowFields {
		for _, field := range []string{"key", "tokens"} {
			value, found := row[field]
			if !found || len(value) == 0 || string(value) == "null" {
				return snapshot, errors.New("invalid context usage breakdown")
			}
		}
	}
	if snapshot.SessionID == "" || snapshot.RequestID == "" || snapshot.Attempt < 0 || snapshot.UsedTokens < 0 || snapshot.LimitTokens <= 0 ||
		snapshot.OutputTokens < 0 || snapshot.OutputTokens > snapshot.UsedTokens || snapshot.ObservedAt.IsZero() ||
		(snapshot.State != "request" && snapshot.State != "complete" && snapshot.State != "failed") ||
		(snapshot.Source != "estimated" && snapshot.Source != "provider") ||
		(snapshot.LimitSource != "configured" && snapshot.LimitSource != "runner_default") ||
		(snapshot.State != "complete" && (snapshot.Source == "provider" || snapshot.OutputTokens != 0)) ||
		len(snapshot.InputEstimates) != len(contextUsageCategoryKeys) && len(snapshot.InputEstimates) != 3 {
		return snapshot, errors.New("invalid context usage record")
	}
	keys := contextUsageCategoryKeys[:]
	legacy := len(snapshot.InputEstimates) == 3
	if legacy {
		keys = []string{"systemPrompt", "messages", "toolDefinitions"}
	}
	for index, key := range keys {
		if snapshot.InputEstimates[index].Key != key || snapshot.InputEstimates[index].Tokens < 0 {
			return snapshot, errors.New("invalid context usage breakdown")
		}
	}
	if legacy {
		return snapshot, errLegacyRunnerContextUsage
	}
	return snapshot, nil
}

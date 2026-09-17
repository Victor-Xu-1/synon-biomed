package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"synon-go/internal/agentruntime"
	"synon-go/internal/persistence/runtimekv"
	"synon-go/internal/providers"
)

// State is a hint for a future request, not a claimed model maximum. Rejection
// bounds apply only to the exact generation context that produced them.
type outputBudgetState struct {
	Next     int    `json:"next"`
	Lower    int    `json:"lower"`
	Rejected int    `json:"rejected"`
	Ceiling  int    `json:"ceiling"`
	Context  string `json:"context"`
	Owner    string `json:"ownerUserId"`
	Session  string `json:"sessionId"`
}

type sessionOutputBudgetClient struct {
	delegate                  agentruntime.ModelClient
	store                     *runtimekv.Store
	namespace, owner, session string
}

func newSessionOutputBudgetClient(delegate agentruntime.ModelClient, store *runtimekv.Store, profile providers.ModelProfile, session, role string) agentruntime.ModelClient {
	if store == nil || profile.MaxTokens != nil {
		return delegate
	}
	identity, _ := json.Marshal([]string{profile.Provider.UserID, session, role, profile.Provider.ID, profile.Provider.Protocol, profile.Provider.Endpoint, profile.Model})
	digest := sha256.Sum256(identity)
	return &sessionOutputBudgetClient{delegate: delegate, store: store, namespace: "output-budget-" + hex.EncodeToString(digest[:]), owner: profile.Provider.UserID, session: session}
}

func (client *sessionOutputBudgetClient) read() (outputBudgetState, error) {
	entry, found, err := client.store.Get(client.namespace, "state")
	if err != nil || !found {
		return outputBudgetState{}, err
	}
	return client.decode(entry)
}

func (client *sessionOutputBudgetClient) decode(entry runtimekv.Entry) (outputBudgetState, error) {
	var state outputBudgetState
	raw, err := json.Marshal(entry.Value)
	if err == nil {
		err = json.Unmarshal(raw, &state)
	}
	if err != nil {
		return state, fmt.Errorf("decode output budget state: %w", err)
	}
	if state.Owner != client.owner || state.Session != client.session || state.Next < 0 ||
		state.Lower < 0 || state.Rejected < 0 || state.Ceiling < 0 {
		return state, errors.New("output budget state authority is invalid")
	}
	return state, nil
}

func outputBudgetContext(request agentruntime.ModelRequest) (string, error) {
	request.MaxTokens = 0
	// Headers and metadata can contain credentials or per-attempt counters;
	// neither belongs in the generation-context identity or durable state.
	request.Headers, request.Metadata = nil, nil
	raw, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func (client *sessionOutputBudgetClient) observe(
	contextID string,
	details providers.OutputLimitDetails,
	rejected int,
	providerMaximum ...int,
) error {
	return client.store.EditNamespace(client.namespace, func(entries map[string]runtimekv.Entry) (bool, error) {
		state := outputBudgetState{Owner: client.owner, Session: client.session}
		entry, found := entries["state"]
		if found {
			var err error
			state, err = client.decode(entry)
			if err != nil {
				return false, err
			}
		}
		if state.Context != contextID {
			state.Rejected = 0
			state.Context = contextID
		}
		if rejected > 0 && (state.Rejected == 0 || rejected < state.Rejected) {
			state.Rejected = rejected
		}
		used := details.OutputTokens
		if used > 0 {
			state.Lower = used
			if state.Rejected > 0 && used >= state.Rejected {
				// Fresh output disproves an older rejected range (for example
				// after upstream capacity or context availability changes).
				state.Rejected = 0
			}
			if len(providerMaximum) > 0 && providerMaximum[0] > 0 &&
				(state.Ceiling == 0 || providerMaximum[0] < state.Ceiling) {
				state.Ceiling = providerMaximum[0]
			}
			// The provider stopped below our explicit request. Do not grow the
			// same ineffective request indefinitely. Narrow the candidate range.
			if details.RequestedTokens > used && (state.Rejected == 0 || details.RequestedTokens < state.Rejected) {
				state.Rejected = details.RequestedTokens
			}
			if state.Rejected > 0 {
				state.Next = used
				if state.Rejected > used && state.Rejected-used > 1 {
					state.Next = used + (state.Rejected-used)/2
				}
			} else if used <= int(^uint(0)>>1)/2 {
				state.Next = used * 2
			} else {
				state.Next = used
			}
		} else if rejected > 0 {
			// No observed safe lower bound: use the original provider default.
			state.Next = 0
		}
		if state.Ceiling > 0 && state.Next > state.Ceiling {
			state.Next = state.Ceiling
		}
		entry.Value = state
		entry.Version++
		entry.UpdatedAt = time.Now().UTC()
		entries["state"] = entry
		return true, nil
	})
}

func (client *sessionOutputBudgetClient) Complete(ctx context.Context, request agentruntime.ModelRequest) (agentruntime.ModelResponse, error) {
	return client.run(ctx, request, nil)
}

func (client *sessionOutputBudgetClient) CompleteStream(ctx context.Context, request agentruntime.ModelRequest, emit func(agentruntime.ModelStreamEvent) error) (agentruntime.ModelResponse, error) {
	if emit == nil {
		emit = func(agentruntime.ModelStreamEvent) error { return nil }
	}
	return client.run(ctx, request, emit)
}

func (client *sessionOutputBudgetClient) run(ctx context.Context, request agentruntime.ModelRequest, emit func(agentruntime.ModelStreamEvent) error) (agentruntime.ModelResponse, error) {
	var visible atomic.Bool
	tracked := func(event agentruntime.ModelStreamEvent) error {
		if event.ContentDelta != "" {
			visible.Store(true)
		}
		if emit != nil {
			return emit(event)
		}
		return nil
	}
	invoke := func(input agentruntime.ModelRequest) (agentruntime.ModelResponse, error) {
		if emit != nil {
			if streaming, ok := client.delegate.(agentruntime.StreamingModelClient); ok {
				return streaming.CompleteStream(ctx, input, tracked)
			}
		}
		return client.delegate.Complete(ctx, input)
	}
	if request.MaxTokens > 0 {
		return invoke(request)
	}
	state, err := client.read()
	if err != nil {
		return agentruntime.ModelResponse{}, err
	}
	contextID, err := outputBudgetContext(request)
	if err != nil {
		return agentruntime.ModelResponse{}, err
	}
	adaptive := request
	adaptive.MaxTokens = state.Next
	if state.Ceiling > 0 && adaptive.MaxTokens > state.Ceiling {
		adaptive.MaxTokens = state.Ceiling
	}
	response, callErr := invoke(adaptive)
	if callErr == nil || ctx.Err() != nil {
		return response, callErr
	}
	rejected := 0
	providerMaximum, _ := providers.ProviderOutputTokenMaximum(callErr)
	if status, ok := providers.HTTPStatus(callErr); ok && (status == http.StatusBadRequest || status == http.StatusUnprocessableEntity) && adaptive.MaxTokens > 0 && !visible.Load() {
		// Confirm the diagnosis by changing only our budget override. This is
		// one bounded transport comparison, never a tool execution replay.
		fallback, fallbackErr := invoke(request)
		if ctx.Err() != nil {
			return fallback, ctx.Err()
		}
		if fallbackErr == nil || providers.IsProviderOutputTokenLimit(fallbackErr) {
			rejected = adaptive.MaxTokens
		}
		// The fallback can already have emitted durable content. Its response
		// and interruption own the current continuation, even when they do
		// not establish why the earlier request was rejected.
		response, callErr = fallback, fallbackErr
	}
	if fallbackMaximum, ok := providers.ProviderOutputTokenMaximum(callErr); ok &&
		(providerMaximum == 0 || fallbackMaximum < providerMaximum) {
		providerMaximum = fallbackMaximum
	}
	details, limited := providers.ProviderOutputLimitDetails(callErr)
	if limited || rejected > 0 {
		if saveErr := client.observe(contextID, details, rejected, providerMaximum); saveErr != nil {
			// Never invalidate an accepted response merely because saving a
			// future-call hint failed. Failed generations retain both errors.
			if callErr != nil {
				return response, errors.Join(callErr, saveErr)
			}
			log.Printf("output budget recovery state could not be saved: %v", saveErr)
		}
	}
	return response, callErr
}

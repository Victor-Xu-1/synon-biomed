package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	"synon-go/internal/persistence/runtimekv"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/providers"
)

func TestOutputBudgetRecoveryPersistsAcrossModelClientRecreation(t *testing.T) {
	var budgets []int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			MaxTokens int              `json:"max_tokens"`
			Messages  []map[string]any `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		budgets = append(budgets, request.MaxTokens)
		if len(request.Messages) != 1 || request.Messages[0]["content"] != "preserve the task" {
			t.Errorf("recovery changed the messages: %#v", request.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		if request.MaxTokens <= 40 {
			_, _ = w.Write([]byte(`{"id":"limited","choices":[{"finish_reason":"length","message":{"role":"assistant","tool_calls":[{"id":"c1","function":{"name":"write_file","arguments":"{"}}]}}],"usage":{"prompt_tokens":100,"completion_tokens":40}}`))
		} else {
			_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"complete"}}]}`))
		}
	}))
	defer api.Close()
	enabled := true
	srv, store, project, frame := newDynamicModelTestRuntime(t, "budget-test", []workspace.ModelProviderInput{{ID: "budget-provider", UserID: "dynamic-user", Name: "Budget", Type: "openai-compatible", BaseURL: api.URL, Model: "budget-test", Enabled: &enabled}})
	request := agentruntime.ModelRequest{Messages: []agentruntime.Message{{Role: "user", Content: "preserve the task"}}}
	_, err := newDynamicModelTestClient(srv, project, frame).Complete(context.Background(), request)
	if !providers.IsProviderOutputTokenLimit(err) {
		t.Fatalf("expected durable interruption: %v", err)
	}
	response, err := newDynamicModelTestClient(srv, project, frame).Complete(context.Background(), request)
	if err != nil || response.Message.Content != "complete" || len(budgets) != 2 || budgets[0] != 0 || budgets[1] <= 40 {
		t.Fatalf("budget did not recover: budgets=%v response=%#v err=%v", budgets, response, err)
	}
	profiles, err := store.ListModelProvidersWithContext(context.Background(), "dynamic-user")
	if err != nil || len(profiles) != 1 || profiles[0].MaxTokens != nil {
		t.Fatalf("runtime recovery changed saved settings: %#v %v", profiles, err)
	}
}

func TestOutputBudgetRejectedProposalFallsBackAndSurvivesRestart(t *testing.T) {
	var budgets []int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			MaxTokens int `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		budgets = append(budgets, request.MaxTokens)
		if request.MaxTokens > 60 {
			http.Error(w, "request not accepted", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if request.MaxTokens <= 40 {
			_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"length","message":{"role":"assistant","tool_calls":[{"id":"c1","function":{"name":"write_file","arguments":"{"}}]}}],"usage":{"prompt_tokens":100,"completion_tokens":40}}`))
		} else {
			_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"complete"}}]}`))
		}
	}))
	defer api.Close()
	profile := providers.ModelProfile{Provider: providers.ProviderProfile{ID: "test", UserID: "owner", Protocol: providers.ProtocolOpenAICompatible, Endpoint: api.URL}, Model: "model", Request: providers.RequestProfile{MaxAttempts: 1}}
	path := filepath.Join(t.TempDir(), "state.sqlite")
	store := runtimekv.New(path)
	makeClient := func() agentruntime.ModelClient {
		delegate, err := providers.NewRuntimeModelClient(profile, api.Client(), nil)
		if err != nil {
			t.Fatal(err)
		}
		return newSessionOutputBudgetClient(delegate, store, profile, "task", "agent")
	}
	request := agentruntime.ModelRequest{Messages: []agentruntime.Message{{Role: "user", Content: "same task"}}}
	for i := 0; i < 2; i++ {
		if _, err := makeClient().Complete(context.Background(), request); !providers.IsProviderOutputTokenLimit(err) {
			t.Fatalf("expected preserved token stop, got %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		store = runtimekv.New(path)
	}
	defer store.Close()
	response, err := makeClient().Complete(context.Background(), request)
	if err != nil || response.Message.Content != "complete" || !reflect.DeepEqual(budgets, []int{0, 80, 0, 60}) {
		t.Fatalf("recovery budgets=%v response=%#v err=%v", budgets, response, err)
	}
}

func TestOutputBudgetUsesProviderDeclaredMaximumAfterRejectedProposal(t *testing.T) {
	var budgets []int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			MaxTokens int `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		budgets = append(budgets, request.MaxTokens)
		if request.MaxTokens > 150 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":"InvalidParameter","message":"The parameter max_tokens is invalid: integer above maximum value, expected a value <= 150, but got 256 instead.","param":"max_tokens","type":"BadRequest"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if request.MaxTokens == 0 {
			_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"length","message":{"role":"assistant","tool_calls":[{"id":"c1","function":{"name":"write_file","arguments":"{"}}]}}],"usage":{"completion_tokens":128}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"complete"}}]}`))
	}))
	defer api.Close()
	profile := providers.ModelProfile{
		Provider: providers.ProviderProfile{
			ID: "test", UserID: "owner", Protocol: providers.ProtocolOpenAICompatible, Endpoint: api.URL,
		},
		Model: "model", Request: providers.RequestProfile{MaxAttempts: 1},
	}
	store := runtimekv.New(filepath.Join(t.TempDir(), "state.sqlite"))
	defer store.Close()
	makeClient := func() agentruntime.ModelClient {
		delegate, err := providers.NewRuntimeModelClient(profile, api.Client(), nil)
		if err != nil {
			t.Fatal(err)
		}
		return newSessionOutputBudgetClient(delegate, store, profile, "task", "agent")
	}
	request := agentruntime.ModelRequest{Messages: []agentruntime.Message{{Role: "user", Content: "same task"}}}
	if _, err := makeClient().Complete(context.Background(), request); !providers.IsProviderOutputTokenLimit(err) {
		t.Fatalf("initial provider stop was not retained: %v", err)
	}
	if _, err := makeClient().Complete(context.Background(), request); !providers.IsProviderOutputTokenLimit(err) {
		t.Fatalf("fallback provider stop was not retained: %v", err)
	}
	response, err := makeClient().Complete(context.Background(), request)
	if err != nil || response.Message.Content != "complete" || !reflect.DeepEqual(budgets, []int{0, 256, 0, 150}) {
		t.Fatalf("declared maximum was not used: budgets=%v response=%#v err=%v", budgets, response, err)
	}
	state, err := makeClient().(*sessionOutputBudgetClient).read()
	if err != nil || state.Ceiling != 150 || state.Next != 150 {
		t.Fatalf("declared maximum was not durable: state=%#v err=%v", state, err)
	}
}

func TestOutputBudgetIgnoredProposalDoesNotGrowWithoutEffect(t *testing.T) {
	var budgets []int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			MaxTokens int `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		budgets = append(budgets, request.MaxTokens)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"length","message":{"role":"assistant","tool_calls":[{"id":"c1","function":{"name":"write_file","arguments":"{"}}]}}],"usage":{"completion_tokens":40}}`))
	}))
	defer api.Close()
	profile := providers.ModelProfile{Provider: providers.ProviderProfile{ID: "test", UserID: "owner", Protocol: providers.ProtocolOpenAICompatible, Endpoint: api.URL}, Model: "model", Request: providers.RequestProfile{MaxAttempts: 1}}
	store := runtimekv.New(filepath.Join(t.TempDir(), "state.sqlite"))
	defer store.Close()
	for i := 0; i < 4; i++ {
		delegate, err := providers.NewRuntimeModelClient(profile, api.Client(), nil)
		if err != nil {
			t.Fatal(err)
		}
		client := newSessionOutputBudgetClient(delegate, store, profile, "task", "agent")
		if _, err := client.Complete(context.Background(), agentruntime.ModelRequest{}); !providers.IsProviderOutputTokenLimit(err) {
			t.Fatalf("provider limit was relabelled: %v", err)
		}
	}
	if !reflect.DeepEqual(budgets, []int{0, 80, 60, 50}) {
		t.Fatalf("ineffective budget kept growing: %v", budgets)
	}
}

func TestOutputBudgetStateIsScopedAndExplicitLimitsRemainAuthoritative(t *testing.T) {
	store := runtimekv.New(filepath.Join(t.TempDir(), "state.sqlite"))
	defer store.Close()
	profile := providers.ModelProfile{Provider: providers.ProviderProfile{ID: "provider", UserID: "owner", Protocol: providers.ProtocolOpenAICompatible, Endpoint: "http://127.0.0.1/provider"}, Model: "model"}
	client := newSessionOutputBudgetClient(nil, store, profile, "task", "agent").(*sessionOutputBudgetClient)
	if err := client.observe("context", providers.OutputLimitDetails{OutputTokens: 40}, 0); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"owner", "model", "endpoint", "task", "role"} {
		p, task, role := profile, "task", "agent"
		switch change {
		case "owner":
			p.Provider.UserID = "other"
		case "model":
			p.Model = "other"
		case "endpoint":
			p.Provider.Endpoint = "http://127.0.0.1/other"
		case "task":
			task = "other"
		case "role":
			role = "other"
		}
		other := newSessionOutputBudgetClient(nil, store, p, task, role).(*sessionOutputBudgetClient)
		state, err := other.read()
		if err != nil || state.Next != 0 {
			t.Fatalf("%s crossed scope: %#v %v", change, state, err)
		}
	}
	value := 20
	profile.MaxTokens = &value
	if newSessionOutputBudgetClient(nil, store, profile, "task", "agent") != nil {
		t.Fatal("explicit saved limit was overridden")
	}
}

func TestOutputBudgetFallbackPreservesAcceptedTextAndTransportFailure(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			MaxTokens int `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if request.MaxTokens > 0 {
			http.Error(w, "request rejected", 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"accepted prefix\"}}]}\n\n"))
	}))
	defer api.Close()
	store := runtimekv.New(filepath.Join(t.TempDir(), "state.sqlite"))
	defer store.Close()
	profile := providers.ModelProfile{Provider: providers.ProviderProfile{ID: "p", UserID: "owner", Protocol: providers.ProtocolOpenAICompatible, Endpoint: api.URL}, Model: "model", Request: providers.RequestProfile{MaxAttempts: 1}}
	delegate, err := providers.NewRuntimeModelClient(profile, api.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	client := newSessionOutputBudgetClient(delegate, store, profile, "task", "agent").(*sessionOutputBudgetClient)
	if err := client.observe("ctx", providers.OutputLimitDetails{OutputTokens: 40}, 0); err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	_, err = client.CompleteStream(context.Background(), agentruntime.ModelRequest{}, func(e agentruntime.ModelStreamEvent) error { text.WriteString(e.ContentDelta); return nil })
	if text.String() != "accepted prefix" || !providers.IsRecoverableStreamInterruption(err) {
		t.Fatalf("fallback lost its accepted output/error: text=%q err=%v", text.String(), err)
	}
	if _, ok := providers.HTTPStatus(err); ok {
		t.Fatal("old budget rejection replaced fallback stream interruption")
	}
	state, err := client.read()
	if err != nil || state.Rejected != 0 {
		t.Fatalf("transport interruption falsely proved budget rejection: %#v %v", state, err)
	}
}

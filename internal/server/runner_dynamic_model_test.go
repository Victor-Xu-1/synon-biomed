package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"synon-go/internal/agentruntime"
	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/providers"
)

type preResolvedDynamicModelClient struct {
	calls atomic.Int64
}

func (client *preResolvedDynamicModelClient) Complete(
	_ context.Context,
	_ agentruntime.ModelRequest,
) (agentruntime.ModelResponse, error) {
	client.calls.Add(1)
	return agentruntime.ModelResponse{Message: agentruntime.Message{Role: "assistant", Content: "pre-resolved"}}, nil
}

func TestSessionRunnerDynamicModelClientUsesPreResolvedClientOnFirstCall(t *testing.T) {
	delegate := &preResolvedDynamicModelClient{}
	client := &sessionRunnerDynamicModelClient{
		initial: sessionRunnerResolvedModelClient{
			client:   delegate,
			identity: "provider\x00endpoint\x00model",
			model:    "model",
		},
		initialReady: true,
	}

	response, err := client.Complete(context.Background(), agentruntime.ModelRequest{})
	if err != nil {
		t.Fatalf("pre-resolved model call error = %v", err)
	}
	if response.Message.Content != "pre-resolved" {
		t.Fatalf("response content = %q, want pre-resolved", response.Message.Content)
	}
	if got := delegate.calls.Load(); got != 1 {
		t.Fatalf("pre-resolved provider calls = %d, want 1", got)
	}
}

func TestSessionRunnerDynamicModelClientObservesConversationSwitchOnNextCall(t *testing.T) {
	providerServer := func(expectedModel, content string, calls *atomic.Int64) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			var request struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Model != expectedModel {
				t.Errorf("provider request=%#v err=%v want=%q", request, err, expectedModel)
				http.Error(w, "unexpected model", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"` + content + `"}}]}`))
		}))
	}
	arkCalls := atomic.Int64{}
	arkAPI := providerServer("ark-code-latest", "ARK", &arkCalls)
	defer arkAPI.Close()
	deepseekCalls := atomic.Int64{}
	deepseekAPI := providerServer("deepseek-v4-flash", "DeepSeek", &deepseekCalls)
	defer deepseekAPI.Close()
	mimoCalls := atomic.Int64{}
	mimoAPI := providerServer("mimo-v2.5", "Mimo", &mimoCalls)
	defer mimoAPI.Close()

	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv := New(Options{FileRoot: root, Workspace: store})
	project, _, err := store.CreateCompatibilityProject(workspace.CreateCompatibilityProjectInput{
		ID: "dynamic-model-project", UserID: "dynamic-user", Name: "Dynamic model task",
	})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "dynamic-model-session", ProjectID: project.ID, AgentName: "GENERAL",
		Status: "processing", ConversationType: "agent", Name: "Dynamic model task",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata(frame.ID, workspace.FrameRuntimeMetadata{
		ContextData: map[string]any{"web_assistant": map[string]any{
			"id": "synonbiomed:operon", "conversation_overrides": map[string]any{"model": "ark-code-latest"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	for _, provider := range []workspace.ModelProviderInput{
		{ID: "dynamic-ark", UserID: "dynamic-user", Name: "ARK", Type: "openai-compatible", BaseURL: arkAPI.URL + "/v1", Model: "ark-code-latest", Enabled: &enabled},
		{ID: "dynamic-deepseek", UserID: "dynamic-user", Name: "DeepSeek", Type: "openai-compatible", BaseURL: deepseekAPI.URL + "/v1", Model: "deepseek-v4-flash", Enabled: &enabled},
		{ID: "dynamic-mimo", UserID: "dynamic-user", Name: "Mimo", Type: "openai-compatible", BaseURL: mimoAPI.URL + "/v1", Model: "mimo-v2.5", Enabled: &enabled},
	} {
		if _, err := store.RegisterModelProvider(provider); err != nil {
			t.Fatal(err)
		}
	}
	if _, found, err := srv.sessionStore.Get(frame.ID); err != nil || found {
		t.Fatalf("session store unexpectedly contains task: found=%v err=%v", found, err)
	}
	client := &sessionRunnerDynamicModelClient{
		server: srv, sessionID: frame.ID,
		session: sessionstore.Session{
			ID:      frame.ID,
			Project: &sessionstore.Project{ID: project.ID, Name: project.Name, Path: root},
		},
		resolutionInput: providers.ResolutionInput{ProjectID: project.ID, MaxAttempts: 1, MaxResponseBytes: 64 * 1024},
	}
	request := agentruntime.ModelRequest{Messages: []agentruntime.Message{{Role: "user", Content: "continue the same task"}}}

	response, err := client.Complete(context.Background(), request)
	if err != nil || response.Message.Content != "ARK" {
		t.Fatalf("ARK response=%#v err=%v", response, err)
	}
	if _, err := store.SetCompatibilityConversationModel(frame.ID, "deepseek-v4-flash"); err != nil {
		t.Fatal(err)
	}
	response, err = client.Complete(context.Background(), request)
	if err != nil || response.Message.Content != "DeepSeek" {
		t.Fatalf("DeepSeek response=%#v err=%v", response, err)
	}
	if _, err := store.SetCompatibilityConversationModel(frame.ID, "mimo-v2.5"); err != nil {
		t.Fatal(err)
	}
	response, err = client.Complete(context.Background(), request)
	if err != nil || response.Message.Content != "Mimo" {
		t.Fatalf("Mimo response=%#v err=%v", response, err)
	}
	if _, err := store.SetCompatibilityConversationModel(frame.ID, "ark-code-latest"); err != nil {
		t.Fatal(err)
	}
	response, err = client.Complete(context.Background(), request)
	if err != nil || response.Message.Content != "ARK" {
		t.Fatalf("second ARK response=%#v err=%v", response, err)
	}
	if arkCalls.Load() != 2 || deepseekCalls.Load() != 1 || mimoCalls.Load() != 1 {
		t.Fatalf("provider calls ARK=%d DeepSeek=%d Mimo=%d", arkCalls.Load(), deepseekCalls.Load(), mimoCalls.Load())
	}
}

func newDynamicModelTestRuntime(
	t *testing.T,
	initialModel string,
	modelProviders []workspace.ModelProviderInput,
) (*Server, *workspace.Store, string, string) {
	t.Helper()
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv := New(Options{FileRoot: root, Workspace: store})
	project, _, err := store.CreateCompatibilityProject(workspace.CreateCompatibilityProjectInput{
		ID: "dynamic-project", UserID: "dynamic-user", Name: "Dynamic model task",
	})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "dynamic-session", ProjectID: project.ID, AgentName: "GENERAL",
		Status: "processing", ConversationType: "agent", Name: "Dynamic model task",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(initialModel) != "" {
		if _, err := store.SetFrameRuntimeMetadata(frame.ID, workspace.FrameRuntimeMetadata{
			ContextData: map[string]any{"web_assistant": map[string]any{
				"id": "synonbiomed:operon", "conversation_overrides": map[string]any{"model": initialModel},
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, provider := range modelProviders {
		if _, err := store.RegisterModelProvider(provider); err != nil {
			t.Fatal(err)
		}
	}
	return srv, store, project.ID, frame.ID
}

func newDynamicModelTestClient(srv *Server, projectID, frameID string) *sessionRunnerDynamicModelClient {
	return &sessionRunnerDynamicModelClient{
		server: srv, sessionID: frameID,
		session: sessionstore.Session{
			ID:      frameID,
			Project: &sessionstore.Project{ID: projectID, Name: "Dynamic model task", Path: srv.fileRoot},
		},
		resolutionInput: providers.ResolutionInput{ProjectID: projectID, MaxAttempts: 1, MaxResponseBytes: 64 * 1024},
	}
}

func TestSessionRunnerDynamicModelClientHandsOffFailedUnstartedCallAfterSwitch(t *testing.T) {
	var switchSelection func() error
	arkCalls := atomic.Int64{}
	arkAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		arkCalls.Add(1)
		if err := switchSelection(); err != nil {
			t.Errorf("switch model: %v", err)
		}
		http.Error(w, "quota exhausted", http.StatusUnauthorized)
	}))
	defer arkAPI.Close()
	deepseekCalls := atomic.Int64{}
	deepseekAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		deepseekCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"continued on DeepSeek"}}]}`))
	}))
	defer deepseekAPI.Close()
	enabled := true
	srv, store, projectID, frameID := newDynamicModelTestRuntime(t, "ark-code-latest", []workspace.ModelProviderInput{
		{ID: "handoff-ark", UserID: "dynamic-user", Name: "ARK", Type: "openai-compatible", BaseURL: arkAPI.URL + "/v1", Model: "ark-code-latest", Enabled: &enabled},
		{ID: "handoff-deepseek", UserID: "dynamic-user", Name: "DeepSeek", Type: "openai-compatible", BaseURL: deepseekAPI.URL + "/v1", Model: "deepseek-v4-flash", Enabled: &enabled},
	})
	switchSelection = func() error {
		_, err := store.SetCompatibilityConversationModel(frameID, "deepseek-v4-flash")
		return err
	}
	client := newDynamicModelTestClient(srv, projectID, frameID)
	response, err := client.Complete(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "continue the same task"}},
	})
	if err != nil || response.Message.Content != "continued on DeepSeek" || response.Model != "deepseek-v4-flash" {
		t.Fatalf("handoff response=%#v err=%v", response, err)
	}
	if arkCalls.Load() != 1 || deepseekCalls.Load() != 1 || client.LastSuccessfulModel() != "deepseek-v4-flash" {
		t.Fatalf("handoff calls ARK=%d DeepSeek=%d last=%q", arkCalls.Load(), deepseekCalls.Load(), client.LastSuccessfulModel())
	}
}

func TestSessionRunnerDynamicModelClientHandsOffAcrossRepeatedUserSwitches(t *testing.T) {
	var switchToDeepSeek func() error
	var switchToMimo func() error
	arkCalls := atomic.Int64{}
	arkAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		arkCalls.Add(1)
		if err := switchToDeepSeek(); err != nil {
			t.Errorf("switch to DeepSeek: %v", err)
		}
		http.Error(w, "ARK quota exhausted", http.StatusTooManyRequests)
	}))
	defer arkAPI.Close()
	deepseekCalls := atomic.Int64{}
	deepseekAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		deepseekCalls.Add(1)
		if err := switchToMimo(); err != nil {
			t.Errorf("switch to Mimo: %v", err)
		}
		http.Error(w, "DeepSeek quota exhausted", http.StatusTooManyRequests)
	}))
	defer deepseekAPI.Close()
	mimoCalls := atomic.Int64{}
	mimoAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mimoCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"continued on Mimo without restarting the task"}}]}`))
	}))
	defer mimoAPI.Close()

	enabled := true
	srv, store, projectID, frameID := newDynamicModelTestRuntime(t, "ark-code-latest", []workspace.ModelProviderInput{
		{ID: "multi-ark", UserID: "dynamic-user", Name: "ARK", Type: "openai-compatible", BaseURL: arkAPI.URL + "/v1", Model: "ark-code-latest", Enabled: &enabled},
		{ID: "multi-deepseek", UserID: "dynamic-user", Name: "DeepSeek", Type: "openai-compatible", BaseURL: deepseekAPI.URL + "/v1", Model: "deepseek-v4-flash", Enabled: &enabled},
		{ID: "multi-mimo", UserID: "dynamic-user", Name: "Mimo", Type: "openai-compatible", BaseURL: mimoAPI.URL + "/v1", Model: "mimo-v2.5", Enabled: &enabled},
	})
	switchToDeepSeek = func() error {
		_, err := store.SetCompatibilityConversationModel(frameID, "deepseek-v4-flash")
		return err
	}
	switchToMimo = func() error {
		_, err := store.SetCompatibilityConversationModel(frameID, "mimo-v2.5")
		return err
	}

	client := newDynamicModelTestClient(srv, projectID, frameID)
	response, err := client.Complete(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "continue the same biomedical task across every model switch"}},
	})
	if err != nil || response.Message.Content != "continued on Mimo without restarting the task" || response.Model != "mimo-v2.5" {
		t.Fatalf("multi-switch response=%#v err=%v", response, err)
	}
	if arkCalls.Load() != 1 || deepseekCalls.Load() != 1 || mimoCalls.Load() != 1 || client.LastSuccessfulModel() != "mimo-v2.5" {
		t.Fatalf("multi-switch calls ARK=%d DeepSeek=%d Mimo=%d last=%q", arkCalls.Load(), deepseekCalls.Load(), mimoCalls.Load(), client.LastSuccessfulModel())
	}
}

func TestSessionRunnerModelFailureDetectsEveryCommittedSelectionRevision(t *testing.T) {
	enabled := true
	srv, store, _, frameID := newDynamicModelTestRuntime(t, "ark-code-latest", []workspace.ModelProviderInput{
		{ID: "revision-ark", UserID: "dynamic-user", Name: "ARK", Type: "openai-compatible", BaseURL: "https://ark.example.test/v1", Model: "ark-code-latest", Enabled: &enabled},
		{ID: "revision-deepseek", UserID: "dynamic-user", Name: "DeepSeek", Type: "openai-compatible", BaseURL: "https://deepseek.example.test/v1", Model: "deepseek-v4-flash", Enabled: &enabled},
	})
	failure := wrapSessionRunnerModelCallError(
		errors.New("provider quota exhausted"),
		sessionConversationModelSnapshot{Selection: "ark-code-latest", Revision: 0},
		"ark-code-latest", "revision-ark",
	)
	if srv.sessionRunnerModelSelectionAdvancedSinceFailure(frameID, failure) {
		t.Fatal("unchanged selection was treated as a switch")
	}
	first, err := store.SetCompatibilityConversationModel(frameID, "deepseek-v4-flash")
	if err != nil || first.Revision != 1 {
		t.Fatalf("first switch=%#v err=%v", first, err)
	}
	second, err := store.SetCompatibilityConversationModel(frameID, "ark-code-latest")
	if err != nil || second.Revision != 2 {
		t.Fatalf("second switch=%#v err=%v", second, err)
	}
	if !srv.sessionRunnerModelSelectionAdvancedSinceFailure(frameID, failure) {
		t.Fatal("switching away and back was not detected by revision")
	}
}

func TestSessionRunnerDynamicModelClientHandsOffTimedOutUnstartedCallAfterSwitch(t *testing.T) {
	var switchSelection func() error
	deepseekCalls := atomic.Int64{}
	enabled := true
	srv, store, projectID, frameID := newDynamicModelTestRuntime(t, "ark-code-latest", []workspace.ModelProviderInput{
		{ID: "timeout-ark", UserID: "dynamic-user", Name: "ARK", Type: "openai-compatible", BaseURL: "https://ark.example.test/v1", Model: "ark-code-latest", Enabled: &enabled},
		{ID: "timeout-deepseek", UserID: "dynamic-user", Name: "DeepSeek", Type: "openai-compatible", BaseURL: "https://deepseek.example.test/v1", Model: "deepseek-v4-flash", Enabled: &enabled},
	})
	switchSelection = func() error {
		_, err := store.SetCompatibilityConversationModel(frameID, "deepseek-v4-flash")
		return err
	}
	srv.httpClient = &http.Client{Transport: runnerStreamRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Hostname() {
		case "ark.example.test":
			if err := switchSelection(); err != nil {
				t.Fatalf("switch model: %v", err)
			}
			// The provider-local deadline is observed only after the user's
			// durable model selection has advanced. This removes scheduler and
			// SQLite timing from the handoff contract under test.
			return nil, context.DeadlineExceeded
		case "deepseek.example.test":
			deepseekCalls.Add(1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(
					`{"choices":[{"message":{"role":"assistant","content":"continued after timeout"}}]}`,
				)),
				Request: request,
			}, nil
		default:
			t.Fatalf("unexpected provider host %q", request.URL.Hostname())
			return nil, errors.New("unexpected provider host")
		}
	})}
	client := newDynamicModelTestClient(srv, projectID, frameID)
	response, err := client.Complete(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "continue the same task after provider timeout"}},
	})
	if err != nil || response.Message.Content != "continued after timeout" || response.Model != "deepseek-v4-flash" {
		t.Fatalf("timeout handoff response=%#v err=%v", response, err)
	}
	if deepseekCalls.Load() != 1 {
		t.Fatalf("DeepSeek calls=%d, want 1", deepseekCalls.Load())
	}
}

func TestSessionRunnerDynamicModelClientNeverReplaysAfterVisibleStreamOutput(t *testing.T) {
	var switchSelection func() error
	arkAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := switchSelection(); err != nil {
			t.Errorf("switch model: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"ark-request\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial\"}}]}\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = w.Write([]byte("data: not-json\n\n"))
	}))
	defer arkAPI.Close()
	deepseekCalls := atomic.Int64{}
	deepseekAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		deepseekCalls.Add(1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"must not replay"}}]}`))
	}))
	defer deepseekAPI.Close()
	enabled := true
	srv, store, projectID, frameID := newDynamicModelTestRuntime(t, "ark-code-latest", []workspace.ModelProviderInput{
		{ID: "visible-ark", UserID: "dynamic-user", Name: "ARK", Type: "openai-compatible", BaseURL: arkAPI.URL + "/v1", Model: "ark-code-latest", Enabled: &enabled},
		{ID: "visible-deepseek", UserID: "dynamic-user", Name: "DeepSeek", Type: "openai-compatible", BaseURL: deepseekAPI.URL + "/v1", Model: "deepseek-v4-flash", Enabled: &enabled},
	})
	switchSelection = func() error {
		_, err := store.SetCompatibilityConversationModel(frameID, "deepseek-v4-flash")
		return err
	}
	client := newDynamicModelTestClient(srv, projectID, frameID)
	var visible strings.Builder
	_, err := client.CompleteStream(context.Background(), agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "continue the same task"}},
	}, func(event agentruntime.ModelStreamEvent) error {
		visible.WriteString(event.ContentDelta)
		return nil
	})
	if err == nil || visible.String() != "partial" || deepseekCalls.Load() != 0 {
		t.Fatalf("visible output=%q DeepSeek calls=%d err=%v", visible.String(), deepseekCalls.Load(), err)
	}
}

func TestSessionRunnerDynamicModelClientDoesNotHandoffCancellation(t *testing.T) {
	var switchSelection func() error
	var cancel context.CancelFunc
	arkAPI := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		if err := switchSelection(); err != nil {
			t.Errorf("switch model: %v", err)
		}
		cancel()
	}))
	defer arkAPI.Close()
	deepseekCalls := atomic.Int64{}
	deepseekAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		deepseekCalls.Add(1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"must not run"}}]}`))
	}))
	defer deepseekAPI.Close()
	enabled := true
	srv, store, projectID, frameID := newDynamicModelTestRuntime(t, "ark-code-latest", []workspace.ModelProviderInput{
		{ID: "cancel-ark", UserID: "dynamic-user", Name: "ARK", Type: "openai-compatible", BaseURL: arkAPI.URL + "/v1", Model: "ark-code-latest", Enabled: &enabled},
		{ID: "cancel-deepseek", UserID: "dynamic-user", Name: "DeepSeek", Type: "openai-compatible", BaseURL: deepseekAPI.URL + "/v1", Model: "deepseek-v4-flash", Enabled: &enabled},
	})
	switchSelection = func() error {
		_, err := store.SetCompatibilityConversationModel(frameID, "deepseek-v4-flash")
		return err
	}
	ctx, cancelContext := context.WithCancel(context.Background())
	cancel = cancelContext
	_, err := newDynamicModelTestClient(srv, projectID, frameID).Complete(ctx, agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "continue the same task"}},
	})
	if err == nil || deepseekCalls.Load() != 0 {
		t.Fatalf("cancellation err=%v DeepSeek calls=%d", err, deepseekCalls.Load())
	}
}

func TestResolveSessionModelProfilePrioritizesConversationThenRoleFallbackThenDelegate(t *testing.T) {
	providerAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer providerAPI.Close()
	enabled := true
	providersInput := []workspace.ModelProviderInput{
		{ID: "precedence-ark", UserID: "dynamic-user", Name: "ARK", Type: "openai-compatible", BaseURL: providerAPI.URL + "/ark", Model: "ark-code-latest", Enabled: &enabled},
		{ID: "precedence-deepseek", UserID: "dynamic-user", Name: "DeepSeek", Type: "openai-compatible", BaseURL: providerAPI.URL + "/deepseek", Model: "deepseek-v4-flash", Enabled: &enabled},
		{ID: "precedence-mimo", UserID: "dynamic-user", Name: "Mimo", Type: "openai-compatible", BaseURL: providerAPI.URL + "/mimo", Model: "mimo-v2.5", Enabled: &enabled},
	}
	srv, store, projectID, frameID := newDynamicModelTestRuntime(t, "ark-code-latest", providersInput)
	input := providers.ResolutionInput{ProjectID: projectID, MaxAttempts: 1}
	session := sessionstore.Session{
		ID: frameID, Project: &sessionstore.Project{ID: projectID},
		Orchestration: map[string]any{"delegate_model": "mimo-v2.5"},
	}
	profile, source, err := srv.resolveSessionModelProfileWithFallback(session, input, "deepseek-v4-flash", "workspace-reviewer-model")
	if err != nil || profile == nil || profile.Model != "ark-code-latest" || source != "conversation-model-override" {
		t.Fatalf("conversation precedence profile=%#v source=%q err=%v", profile, source, err)
	}
	frame, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "dynamic-session-without-override", ProjectID: projectID, AgentName: "GENERAL",
		Status: "processing", ConversationType: "agent", Name: "Dynamic model fallback task",
	})
	if err != nil {
		t.Fatal(err)
	}
	session.ID = frame.ID
	profile, source, err = srv.resolveSessionModelProfileWithFallback(session, input, "deepseek-v4-flash", "workspace-reviewer-model")
	if err != nil || profile == nil || profile.Model != "deepseek-v4-flash" || source != "workspace-reviewer-model" {
		t.Fatalf("reviewer fallback profile=%#v source=%q err=%v", profile, source, err)
	}
	profile, source, err = srv.resolveSessionModelProfile(session, input)
	if err != nil || profile == nil || profile.Model != "mimo-v2.5" || source != "workspace-delegated-model" {
		t.Fatalf("delegate fallback profile=%#v source=%q err=%v", profile, source, err)
	}
}

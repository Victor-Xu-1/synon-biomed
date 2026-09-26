package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestNormalizeSessionRunnerChatOptionsUsesV11RetryDefault(t *testing.T) {
	got := normalizeSessionRunnerChatOptions(SessionRunnerChatOptions{}).MaxAttempts
	if got != 4 {
		t.Fatalf("MaxAttempts = %d, want v1.1 default 4", got)
	}
}

func TestExpiredRunnerLeaseRecoveryIsSameTaskAutoResumable(t *testing.T) {
	reason := sessionRunnerExpiredLeaseRecoveryReasonCode
	if !runnerInterruptionMayContinueSameTask(reason) || !runnerInterruptionAutoResume(reason) {
		t.Fatalf("expired runner lease reason must auto-resume the same task: %q", reason)
	}
}

func TestNormalizeSessionRunnerChatOptionsBoundsPreparation(t *testing.T) {
	got := normalizeSessionRunnerChatOptions(SessionRunnerChatOptions{}).PreparationTimeout
	if got != defaultSessionRunnerChatPreparationTimeout {
		t.Fatalf("PreparationTimeout = %s, want %s", got, defaultSessionRunnerChatPreparationTimeout)
	}
	if !runnerInterruptionMayContinueSameTask(sessionRunnerPreparationTimeoutReasonCode) ||
		!runnerInterruptionAutoResume(sessionRunnerPreparationTimeoutReasonCode) {
		t.Fatal("preparation timeout must preserve and automatically resume the same task")
	}
}

func TestSessionRunnerPreparationCheckpointDeadlineIsBoundedAndResumable(t *testing.T) {
	operationCtx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-operationCtx.Done()

	err := classifySessionRunnerPreparationStageError(
		"history_replay", sessionRunnerContentDeltaPersistenceTimeout,
		operationCtx, context.DeadlineExceeded,
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("classified preparation error must preserve its deadline cause")
	}
	reason, detail, bounded := sessionRunnerBoundedCorrectionDetails(err)
	if !bounded || reason != sessionRunnerPreparationTimeoutReasonCode ||
		!strings.Contains(detail, "history_replay") {
		t.Fatalf("preparation checkpoint correction = (%q, %q, %t)", reason, detail, bounded)
	}
	if !runnerInterruptionMayContinueSameTask(reason) || !runnerInterruptionAutoResume(reason) {
		t.Fatal("preparation checkpoint deadline must automatically resume the same task")
	}

	ordinary := errors.New("ordinary preparation failure")
	if got := classifySessionRunnerPreparationStageError(
		"history_replay", sessionRunnerContentDeltaPersistenceTimeout,
		context.Background(), ordinary,
	); !errors.Is(got, ordinary) {
		t.Fatalf("ordinary preparation error = %v", got)
	}
}

func TestSessionRunnerKernelRecoveryTimeoutIsBoundedAndResumable(t *testing.T) {
	correction := sessionRunnerKernelRecoveryTimeout{
		timeout: defaultSessionRunnerKernelRecoveryTimeout,
		cause:   errSessionRunnerKernelRecoveryDeadline,
	}
	if !errors.Is(correction, errSessionRunnerKernelRecoveryDeadline) {
		t.Fatal("kernel recovery timeout must preserve its deadline cause")
	}
	reason, detail := correction.runnerCorrection()
	if reason != sessionRunnerKernelRecoveryTimeoutReasonCode ||
		!strings.Contains(detail, "durable operation") {
		t.Fatalf("kernel recovery correction = (%q, %q)", reason, detail)
	}
	if !runnerInterruptionMayContinueSameTask(reason) || !runnerInterruptionAutoResume(reason) {
		t.Fatal("kernel recovery timeout must automatically resume the same task")
	}
}

func TestSessionRunnerPreparationStageIdentityIncludesResumeFence(t *testing.T) {
	run := &sessionRunnerChatRun{
		AssistantSegmentOrdinal: 3,
		Transcript: &transcriptRunnerAuthority{Claim: transcriptstore.RunnerClaim{
			ResumeCheckpoint: 230,
		}},
	}
	got := sessionRunnerPreparationStageIdentity(run, "resume_state")
	if got != "runner-preparation-000003-000230-resume_state" {
		t.Fatalf("preparation stage identity = %q", got)
	}
}

func TestSessionRunnerResolvesBundledAgentFromFrame(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-agent", UserID: "local", Name: "Agent runtime"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame-aidd", ProjectID: "project-agent", AgentName: "AIDD_EXPERT", Status: "running", ConversationType: "task"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame-onboarding", ProjectID: "project-agent", AgentName: "ONBOARDING", Status: "running", ConversationType: "task"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame-operon", ProjectID: "project-agent", AgentName: "OPERON", Status: "running", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	srv := New(Options{Workspace: store, SkillDirectories: []string{v11SkillsDir(t)}})

	options, selected := srv.applySessionRunnerBundledAgent(sessionstore.Session{ID: "frame-aidd"}, normalizeSessionRunnerChatOptions(SessionRunnerChatOptions{}))
	if selected != "AIDD_EXPERT" || !strings.Contains(options.SystemPrompt, "You are AI Drug Discovery") || options.SystemPrompt == defaultSessionRunnerChatSystemPrompt {
		t.Fatalf("AIDD runner selection=%q prompt=%q", selected, options.SystemPrompt)
	}
	if options.DisableSkillDiscovery || options.OutputLimitBytes != 50000 {
		t.Fatalf("AIDD runner options=%#v", options)
	}
	if options.ModelResponseLimitBytes != defaultSessionRunnerModelResponseLimitBytes {
		t.Fatalf(
			"AIDD model response limit = %d, want independent default %d",
			options.ModelResponseLimitBytes, defaultSessionRunnerModelResponseLimitBytes,
		)
	}

	onboarding, selected := srv.applySessionRunnerBundledAgent(sessionstore.Session{ID: "frame-onboarding"}, normalizeSessionRunnerChatOptions(SessionRunnerChatOptions{}))
	if selected != "ONBOARDING" || !onboarding.DisableSkillDiscovery {
		t.Fatalf("ONBOARDING runner selection=%q options=%#v", selected, onboarding)
	}
	for _, tool := range onboarding.AllowedTools {
		if strings.EqualFold(tool, "Shell") || strings.EqualFold(tool, "WebResearch") || strings.EqualFold(tool, "SkillSearch") {
			t.Fatalf("ONBOARDING retained excluded tool %q", tool)
		}
	}
	modelTools := map[string]int{}
	for _, tool := range srv.chatRunnerTools(onboarding.AllowedTools) {
		modelTools[tool.Function.Name]++
	}
	if modelTools["ask_user"] != 1 || modelTools["AskUserQuestion"] != 0 || modelTools["ask_user_question"] != 0 || modelTools[generatePlanToolName] != 0 {
		t.Fatalf("ONBOARDING model AskUser tools=%#v", modelTools)
	}

	operon, selected := srv.applySessionRunnerBundledAgent(sessionstore.Session{ID: "frame-operon"}, normalizeSessionRunnerChatOptions(SessionRunnerChatOptions{}))
	if selected != "OPERON" {
		t.Fatalf("OPERON runner selection=%q", selected)
	}
	operonTools := map[string]bool{}
	for _, tool := range srv.chatRunnerTools(operon.AllowedTools) {
		operonTools[tool.Function.Name] = true
	}
	if !operonTools[generatePlanToolName] || !operonTools["ask_user"] {
		t.Fatalf("OPERON model tools=%#v", operonTools)
	}
}

func TestTaskContractPreservesCanonicalIntentAndAcceptance(t *testing.T) {
	const canonicalTask = "请基于截至2026年9月10日可公开获取的资料，寻找最新的 CRBN 人源化共晶结构并完成验证。\n验收：所有明确结构与交付物均有真实证据。"
	contract := buildSessionRunnerTaskContract(canonicalTask, "intent-crbn", 4)
	if contract.Version != 2 || contract.TaskIntentID != "intent-crbn" || contract.TaskIntentRevision != 4 ||
		contract.TaskIntentSHA256 != generatedPlanTaskIntentSHA(canonicalTask) {
		t.Fatalf("task contract identity = %#v", contract)
	}
	joined := strings.Join(contract.AcceptanceChecks, "\n")
	for _, required := range []string{"explicit canonical entity", "proxy or representative scenario", "complete canonical task"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("task contract missing %q: %s", required, joined)
		}
	}
	if len(contract.TemporalScopes) != 1 || contract.TemporalScopes[0].Kind != "public_information_available_through" ||
		contract.TemporalScopes[0].Date != "2026-09-10" || contract.TemporalScopes[0].Precision != "day" ||
		contract.TemporalScopes[0].SourceText != "截至2026年9月10日" {
		t.Fatalf("task temporal scope=%#v", contract.TemporalScopes)
	}
}

func TestTaskContractDoesNotInventTemporalScopeFromOrdinaryOrInvalidDates(t *testing.T) {
	for name, task := range map[string]string{
		"ordinary study date": "Compare the trial published on 2025-03-27 with its follow-up.",
		"invalid as-of date":  "Use public information as of 2026-02-31.",
	} {
		t.Run(name, func(t *testing.T) {
			if contract := buildSessionRunnerTaskContract(task, "intent-date", 1); len(contract.TemporalScopes) != 0 {
				t.Fatalf("invented temporal scope=%#v", contract.TemporalScopes)
			}
		})
	}
}

func TestSessionRunnerChatOnceUsesBuiltinFallbackEndpoint(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	allowPairingForTest(t, srv, "feishu", "ou_builtin_runner")
	httpServer := httptestServer(t, srv)

	first := postFeishuEvent(t, httpServer.URL, []byte(`{
		"schema": "2.0",
		"header": {"event_id": "evt-builtin-runner-1", "event_type": "im.message.receive_v1"},
		"event": {
			"sender": {"sender_id": {"open_id": "ou_builtin_runner"}},
			"message": {
				"message_id": "om_builtin_runner_1",
				"chat_id": "oc_builtin_runner",
				"chat_type": "p2p",
				"message_type": "text",
				"content": "{\"text\":\"summarize builtin fallback work\"}"
			}
		}
	}`))
	sessionID := first["sessionId"].(string)

	result, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{RunnerID: "builtin-chat-runner",
		Endpoint:         "builtin://synon-go/deterministic-chat",
		Model:            "synon-go-deterministic",
		LeaseTTL:         time.Minute,
		ReplayLimit:      20,
		OutputLimitBytes: 64 * 1024,
	})
	if err != nil {
		t.Fatalf("RunSessionRunnerChatOnce() error = %v", err)
	}
	if !result.Claimed || result.SessionID != sessionID || result.Status != "completed" || result.AssistantEventID == 0 || result.FinishEventID == 0 {
		t.Fatalf("builtin runner result = %+v", result)
	}

	replayed := postToolInput(t, httpServer.URL, "session_replay", map[string]any{
		"sessionId":    sessionID,
		"afterEventId": float64(0),
		"limit":        float64(10),
	})
	entries := replayed["result"].(map[string]any)["entries"].([]any)
	if !hasJournalEntry(entries, "message", "", "summarize builtin fallback work") || !hasJournalEntry(entries, "runner_finished", "completed", "") {
		t.Fatalf("builtin runner journal entries = %#v", entries)
	}
}

func TestSessionRunnerChatPoolRunsIndependentConversationsConcurrently(t *testing.T) {
	for _, workers := range []int{1, 4, 8, 10} {
		t.Run(fmt.Sprintf("workers-%d", workers), func(t *testing.T) {
			testSessionRunnerChatPoolConcurrency(t, workers)
		})
	}
}

func TestSessionRunnerChatPoolKeepsBasePollCadenceWhileStaggeringStartup(t *testing.T) {
	options := normalizeSessionRunnerChatOptions(SessionRunnerChatOptions{RunnerID: "pool-cadence",
		PollInterval: time.Second,
	})
	workers := 16
	for workerIndex := 0; workerIndex < workers; workerIndex++ {
		workerOptions, initialDelay := sessionRunnerChatPoolWorkerTiming(options, workers, workerIndex)
		if workerOptions.PollInterval != options.PollInterval {
			t.Fatalf("worker %d poll interval = %s, want base %s", workerIndex, workerOptions.PollInterval, options.PollInterval)
		}
		if workerOptions.RunnerID != fmt.Sprintf("pool-cadence/chat-%d", workerIndex+1) {
			t.Fatalf("worker %d runner id = %q", workerIndex, workerOptions.RunnerID)
		}
		if initialDelay < 0 || initialDelay >= options.PollInterval {
			t.Fatalf("worker %d initial delay = %s, want [0,%s)", workerIndex, initialDelay, options.PollInterval)
		}
	}
}

func TestSessionRunnerChatLoopIsolatesClaimedTaskFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int64
	cycle := func(context.Context, SessionRunnerChatOptions) (SessionRunnerCycleResult, error) {
		if calls.Add(1) == 1 {
			return SessionRunnerCycleResult{
				Claimed: true, SessionID: "failed-task", RunnerID: "isolated-worker",
			}, errors.New("task-local failure")
		}
		cancel()
		return SessionRunnerCycleResult{}, nil
	}
	err := (&Server{}).runSessionRunnerChatLoop(ctx, normalizeSessionRunnerChatOptions(SessionRunnerChatOptions{
		RunnerID: "isolated-worker", PollInterval: time.Millisecond,
	}), cycle)
	if err != nil || calls.Load() < 2 {
		t.Fatalf("claimed failure cancelled worker loop: calls=%d err=%v", calls.Load(), err)
	}
}

func TestSessionRunnerChatLoopReturnsUnclaimedInfrastructureFailure(t *testing.T) {
	want := errors.New("global store unavailable")
	cycle := func(context.Context, SessionRunnerChatOptions) (SessionRunnerCycleResult, error) {
		return SessionRunnerCycleResult{}, want
	}
	err := (&Server{}).runSessionRunnerChatLoop(
		context.Background(), normalizeSessionRunnerChatOptions(SessionRunnerChatOptions{}), cycle,
	)
	if !errors.Is(err, want) {
		t.Fatalf("unclaimed infrastructure error=%v", err)
	}
}

func testSessionRunnerChatPoolConcurrency(t *testing.T, workers int) {
	t.Helper()
	var active atomic.Int64
	var maxActive atomic.Int64
	var completed atomic.Int64
	release := make(chan struct{})
	var releaseOnce sync.Once
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role    string
				Content string
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode parallel model request: %v", err)
			return
		}
		expected := ""
		for index := len(request.Messages) - 1; index >= 0; index-- {
			if request.Messages[index].Role == "user" {
				expected = request.Messages[index].Content
				break
			}
		}
		if expected == "" {
			t.Errorf("parallel model request did not contain a user message: %#v", request.Messages)
			return
		}
		current := active.Add(1)
		defer active.Add(-1)
		for observed := maxActive.Load(); current > observed && !maxActive.CompareAndSwap(observed, current); observed = maxActive.Load() {
		}
		if current >= int64(workers) {
			releaseOnce.Do(func() { close(release) })
		}
		select {
		case <-release:
		case <-time.After(10 * time.Second):
			t.Errorf("runner pool did not issue %d model requests concurrently", workers)
		}
		response := map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{
					"role":    "assistant",
					"content": "parallel result for " + expected,
				},
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
		completed.Add(1)
	}))
	defer modelAPI.Close()

	srv := New(Options{FileRoot: t.TempDir()})
	httpServer := httptestServer(t, srv)
	sessionExpected := map[string]string{}
	for index := 1; index <= workers; index++ {
		openID := fmt.Sprintf("ou_parallel_%d_%d", workers, index)
		allowPairingForTest(t, srv, "feishu", openID)
		admitted := postFeishuEvent(t, httpServer.URL, []byte(fmt.Sprintf(`{
			"schema":"2.0",
			"header":{"event_id":"evt-parallel-%d-%d","event_type":"im.message.receive_v1"},
			"event":{"sender":{"sender_id":{"open_id":%q}},"message":{
				"message_id":"om_parallel_%d_%d","chat_id":"oc_parallel_%d_%d","chat_type":"p2p",
				"message_type":"text","content":"{\"text\":\"parallel work %d\"}"}}
		}`, workers, index, openID, workers, index, workers, index, index)))
		sessionID := admitted["sessionId"].(string)
		sessionExpected[sessionID] = fmt.Sprintf("parallel work %d", index)
	}
	if len(sessionExpected) != workers {
		t.Fatalf("admitted sessions = %d, want %d: %#v", len(sessionExpected), workers, sessionExpected)
	}
	for sessionID := range sessionExpected {
		session, found, err := srv.sessionStore.Get(sessionID)
		if err != nil || !found || session.LastRole != "user" || session.Runner != nil {
			t.Fatalf("session %s is not runnable: session=%+v found=%t err=%v", sessionID, session, found, err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- srv.RunSessionRunnerChatPool(ctx, SessionRunnerChatOptions{RunnerID: "parallel-runner", Endpoint: modelAPI.URL, Model: "parallel-model",
			PollInterval: 5 * time.Millisecond, LeaseTTL: time.Minute, RequestTimeout: 5 * time.Second,
			ReplayLimit: 20, OutputLimitBytes: 64 * 1024,
			DisableSkillDiscovery: true,
		}, workers)
	}()

	deadline := time.Now().Add(12 * time.Second)
	for completed.Load() < int64(workers) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	terminalDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(terminalDeadline) {
		allCompleted := true
		for sessionID := range sessionExpected {
			session, found, err := srv.sessionStore.Get(sessionID)
			if err != nil {
				t.Fatalf("read completed session %s: %v", sessionID, err)
			}
			if !found || session.Runner == nil || session.Runner.Status != "completed" {
				allCompleted = false
				break
			}
		}
		if allCompleted {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("RunSessionRunnerChatPool() error = %v", err)
	}
	if completed.Load() != int64(workers) || maxActive.Load() < int64(workers) {
		t.Fatalf("pool completed=%d max_concurrent=%d, want %d", completed.Load(), maxActive.Load(), workers)
	}
	for sessionID, expected := range sessionExpected {
		replayed := postToolInput(t, httpServer.URL, "session_replay", map[string]any{
			"sessionId":    sessionID,
			"afterEventId": float64(0),
			"limit":        float64(40),
		})
		entries := replayed["result"].(map[string]any)["entries"].([]any)
		ownText := "parallel result for " + expected
		if !hasJournalEntry(entries, "message", "", ownText) {
			t.Fatalf("session %s is missing its own response %q: %#v", sessionID, ownText, entries)
		}
		for _, raw := range entries {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			message, ok := item["message"].(map[string]any)
			if !ok || message["type"] != "message" {
				continue
			}
			text, _ := message["text"].(string)
			if strings.Contains(text, "parallel result for ") && !strings.Contains(text, ownText) {
				t.Fatalf("session %s received another session response: %q", sessionID, text)
			}
		}
	}
}
func TestSessionRunnerChatOnceUsesOpenAICompatibleEndpoint(t *testing.T) {
	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("model request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		var request struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		if request.Model != "test-model" {
			t.Fatalf("model = %q", request.Model)
		}
		if len(request.Messages) < 2 || request.Messages[0].Role != "system" {
			t.Fatalf("messages = %#v", request.Messages)
		}
		last := request.Messages[len(request.Messages)-1]
		if last.Role != "user" || !strings.Contains(last.Content, "run model work") {
			t.Fatalf("last message = %#v", last)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices": [{
				"message": {
					"role": "assistant",
					"content": "model processed: run model work"
				}
			}]
		}`))
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	allowPairingForTest(t, srv, "feishu", "ou_chat_runner")
	httpServer := httptestServer(t, srv)

	first := postFeishuEvent(t, httpServer.URL, []byte(`{
		"schema": "2.0",
		"header": {"event_id": "evt-chat-runner-1", "event_type": "im.message.receive_v1"},
		"event": {
			"sender": {"sender_id": {"open_id": "ou_chat_runner"}},
			"message": {
				"message_id": "om_chat_runner_1",
				"chat_id": "oc_chat_runner",
				"chat_type": "p2p",
				"message_type": "text",
				"content": "{\"text\":\"run model work\"}"
			}
		}
	}`))
	sessionID := first["sessionId"].(string)

	result, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{RunnerID: "chat-runner-a",
		Endpoint:         modelAPI.URL + "/v1/chat/completions",
		APIKey:           "test-key",
		Model:            "test-model",
		LeaseTTL:         time.Minute,
		ReplayLimit:      20,
		OutputLimitBytes: 64 * 1024,
	})
	if err != nil {
		t.Fatalf("RunSessionRunnerChatOnce() error = %v", err)
	}
	if !result.Claimed || result.SessionID != sessionID || result.Status != "completed" || result.AssistantEventID == 0 || result.FinishEventID == 0 {
		t.Fatalf("runner result = %+v", result)
	}
	if requests.Load() != 1 {
		t.Fatalf("model requests = %d", requests.Load())
	}

	replayed := postToolInput(t, httpServer.URL, "session_replay", map[string]any{
		"sessionId":    sessionID,
		"afterEventId": float64(0),
		"limit":        float64(10),
	})
	entries := replayed["result"].(map[string]any)["entries"].([]any)
	if !hasJournalEntry(entries, "runner_checkpoint", "running", "") {
		t.Fatalf("missing runner checkpoint entry: %#v", entries)
	}
	if !hasJournalEntry(entries, "message", "", "model processed: run model work") {
		t.Fatalf("missing model assistant message: %#v", entries)
	}
	if !hasJournalEntry(entries, "runner_finished", "completed", "") {
		t.Fatalf("missing runner finished entry: %#v", entries)
	}

	second, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{RunnerID: "chat-runner-a",
		Endpoint:    modelAPI.URL + "/v1/chat/completions",
		APIKey:      "test-key",
		Model:       "test-model",
		LeaseTTL:    time.Minute,
		ReplayLimit: 20,
	})
	if err != nil {
		t.Fatalf("second RunSessionRunnerChatOnce() error = %v", err)
	}
	if second.Claimed {
		t.Fatalf("completed assistant session should not be pending again: %+v", second)
	}
}

func TestSessionRunnerChatUsesCompactSummaryForResumeContext(t *testing.T) {
	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		var compactSystem string
		nonSystem := []string{}
		for _, message := range request.Messages {
			if message.Role == "system" {
				if strings.Contains(message.Content, "Synon compact handoff context:") {
					compactSystem = message.Content
				}
				continue
			}
			nonSystem = append(nonSystem, message.Content)
		}
		if !strings.Contains(compactSystem, "Compact session summary") || !strings.Contains(compactSystem, "old investigation step") {
			t.Fatalf("compact system context = %q messages=%#v", compactSystem, request.Messages)
		}
		for _, content := range nonSystem {
			if strings.Contains(content, "old investigation step") || strings.Contains(content, "old assistant result") {
				t.Fatalf("old pre-compact raw message leaked into model context: %#v", request.Messages)
			}
		}
		if len(nonSystem) == 0 || !strings.Contains(nonSystem[len(nonSystem)-1], "continue after compact") {
			t.Fatalf("post-compact user message missing: %#v", request.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"continued from compact context"}}]}`))
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	httpServer := httptestServer(t, srv)
	sessionID := "compact-runner-session"
	postToolInput(t, httpServer.URL, "session_store", map[string]any{
		"sessionId": sessionID,
		"title":     "Compact Runner Session",
	})
	postToolInput(t, httpServer.URL, "session_append", map[string]any{
		"sessionId":       sessionID,
		"role":            "user",
		"message":         map[string]any{"type": "message", "text": "old investigation step"},
		"clientMessageId": "compact-runner-old-user",
	})
	postToolInput(t, httpServer.URL, "session_append", map[string]any{
		"sessionId":       sessionID,
		"role":            "assistant",
		"message":         map[string]any{"type": "message", "text": "old assistant result"},
		"clientMessageId": "compact-runner-old-assistant",
	})
	postToolInput(t, httpServer.URL, "Compact", map[string]any{
		"sessionId":    sessionID,
		"instructions": "preserve implementation state",
		"trigger":      "manual",
		"limit":        float64(20),
	})
	postToolInput(t, httpServer.URL, "session_append", map[string]any{
		"sessionId":       sessionID,
		"role":            "user",
		"message":         map[string]any{"type": "message", "text": "continue after compact"},
		"clientMessageId": "compact-runner-new-user",
	})
	session, ok, err := srv.sessionStore.Get(sessionID)
	if err != nil || !ok {
		t.Fatalf("session get ok=%v err=%v", ok, err)
	}
	entries, err := srv.eventJournal.ReadAfter(sessionID, 0, 50)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	result, err := srv.runSessionRunnerChat(context.Background(), SessionRunnerChatOptions{RunnerID: "compact-runner",
		Endpoint:         modelAPI.URL + "/v1/chat/completions",
		Model:            "compact-model",
		MaxToolRounds:    1,
		MaxAttempts:      1,
		ReplayLimit:      50,
		OutputLimitBytes: 64 * 1024,
	}, session, entries, nil)
	if err != nil {
		t.Fatalf("runSessionRunnerChat() error = %v", err)
	}
	if result != "continued from compact context" {
		t.Fatalf("assistant result = %q", result)
	}
	if requests.Load() != 1 {
		t.Fatalf("model requests = %d", requests.Load())
	}
}

func TestSessionRunnerChatInjectsProviderResumeCacheMarkers(t *testing.T) {
	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("X-Synon-Provider-Cache") != "1" {
			t.Fatalf("provider cache header missing: %#v", r.Header)
		}
		if r.Header.Get("X-Synon-Compacted-Through-Event-Id") != "2" {
			t.Fatalf("compact event header = %q", r.Header.Get("X-Synon-Compacted-Through-Event-Id"))
		}
		if r.Header.Get("X-Synon-Resume-Cache-Keys") != "tool:Read:call-1" {
			t.Fatalf("resume cache header = %q", r.Header.Get("X-Synon-Resume-Cache-Keys"))
		}
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Metadata map[string]any `json:"metadata"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		cache, _ := request.Metadata["synon_provider_cache"].(map[string]any)
		if cache["compactedThroughEventId"] != float64(2) {
			t.Fatalf("provider cache metadata missing compact id: %#v", request.Metadata)
		}
		resumeKeys, _ := cache["resumeCacheKeys"].([]any)
		if len(resumeKeys) != 1 || resumeKeys[0] != "tool:Read:call-1" {
			t.Fatalf("provider cache metadata missing resume keys: %#v", request.Metadata)
		}
		joined := ""
		for _, message := range request.Messages {
			joined += "\n" + message.Content
		}
		if !strings.Contains(joined, "Synon provider resume cache context:") ||
			!strings.Contains(joined, "compactedThroughEventId=2") ||
			!strings.Contains(joined, "resumeCacheKeys=tool:Read:call-1") {
			t.Fatalf("provider resume cache context missing: %#v", request.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"provider cache markers present"}}]}`))
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	session := sessionstore.Session{ID: "provider-cache-session", Title: "Provider Cache Session", WorkDir: root}
	entries := []eventjournal.Entry{
		{SessionID: session.ID, EventID: 1, Message: eventjournal.Message{"type": "message", "role": "user", "text": "resume using provider cache markers"}},
		{SessionID: session.ID, EventID: 2, Message: eventjournal.Message{"type": "session_compact", "role": "system", "summary": "Compact session summary\nold work"}},
		{SessionID: session.ID, EventID: 3, Message: eventjournal.Message{"type": "runner_checkpoint", "role": "system", "status": "completed", "resumeCacheKey": "tool:Read:call-1"}},
	}
	result, err := srv.runSessionRunnerChat(context.Background(), SessionRunnerChatOptions{RunnerID: "provider-cache-runner",
		Endpoint:         modelAPI.URL + "/v1/chat/completions",
		Model:            "runner-model",
		MaxToolRounds:    1,
		MaxAttempts:      1,
		ReplayLimit:      50,
		OutputLimitBytes: 64 * 1024,
	}, session, entries, nil)
	if err != nil {
		t.Fatalf("runSessionRunnerChat() error = %v", err)
	}
	if result != "provider cache markers present" || requests.Load() != 1 {
		t.Fatalf("result=%q requests=%d", result, requests.Load())
	}
}

func TestSessionRunnerChatAutoCompactsWhenContextPressureExceedsThreshold(t *testing.T) {
	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		var compactSystem string
		var latestUser string
		for _, message := range request.Messages {
			if message.Role == "system" && strings.Contains(message.Content, "Synon compact handoff context:") {
				compactSystem = message.Content
			}
			if message.Role == "user" {
				latestUser = message.Content
			}
			if message.Role == "assistant" && strings.Contains(message.Content, "old assistant detail") {
				t.Fatalf("pre-compact assistant leaked into raw model messages: %#v", request.Messages)
			}
		}
		if !strings.Contains(compactSystem, "Compact session summary") || !strings.Contains(compactSystem, "auto compact current request") {
			t.Fatalf("compact system context = %q messages=%#v", compactSystem, request.Messages)
		}
		if !strings.Contains(latestUser, "auto compact current request") {
			t.Fatalf("latest user after auto compact = %q messages=%#v", latestUser, request.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"auto compact response"}}]}`))
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	if _, err := srv.settingsStore.Set(configStoreKey("autoCompactEnabled"), true); err != nil {
		t.Fatalf("set auto compact enabled: %v", err)
	}
	if _, err := srv.settingsStore.Set(configStoreKey("autoCompactTokenThreshold"), float64(20)); err != nil {
		t.Fatalf("set auto compact threshold: %v", err)
	}
	httpServer := httptestServer(t, srv)
	sessionID := "auto-compact-runner-session"
	postToolInput(t, httpServer.URL, "session_store", map[string]any{"sessionId": sessionID})
	postToolInput(t, httpServer.URL, "session_append", map[string]any{
		"sessionId":       sessionID,
		"role":            "assistant",
		"message":         map[string]any{"type": "message", "text": strings.Repeat("old assistant detail ", 20)},
		"clientMessageId": "auto-compact-old-assistant",
	})
	postToolInput(t, httpServer.URL, "session_append", map[string]any{
		"sessionId":       sessionID,
		"role":            "user",
		"message":         map[string]any{"type": "message", "text": "auto compact current request " + strings.Repeat("context ", 80)},
		"clientMessageId": "auto-compact-user",
	})
	session, ok, err := srv.sessionStore.Get(sessionID)
	if err != nil || !ok {
		t.Fatalf("session get ok=%v err=%v", ok, err)
	}
	entries, err := srv.eventJournal.ReadAfter(sessionID, 0, 50)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	result, err := srv.runSessionRunnerChat(context.Background(), SessionRunnerChatOptions{RunnerID: "auto-compact-runner",
		Endpoint:         modelAPI.URL + "/v1/chat/completions",
		Model:            "auto-compact-model",
		MaxToolRounds:    1,
		MaxAttempts:      1,
		ReplayLimit:      50,
		OutputLimitBytes: 64 * 1024,
	}, session, entries, nil)
	if err != nil {
		t.Fatalf("runSessionRunnerChat() error = %v", err)
	}
	if result != "auto compact response" {
		t.Fatalf("assistant result = %q", result)
	}
	replayed, err := srv.eventJournal.ReadAfter(sessionID, 0, 50)
	if err != nil {
		t.Fatalf("read replayed journal: %v", err)
	}
	foundAutoCompact := false
	for _, entry := range replayed {
		if stringValue(entry.Message["type"]) == "session_compact" && stringValue(entry.Message["trigger"]) == "auto" {
			foundAutoCompact = true
		}
	}
	if !foundAutoCompact {
		t.Fatalf("missing auto compact journal event: %#v", replayed)
	}
	if requests.Load() != 1 {
		t.Fatalf("model requests = %d", requests.Load())
	}
}

func TestSessionRunnerAutoCompactUsesProviderWindowAndHonorsExplicitThreshold(t *testing.T) {
	srv := New(Options{FileRoot: t.TempDir()})
	if !srv.autoCompactEnabled() {
		t.Fatal("auto compact should be enabled when no explicit setting exists")
	}
	if defaultRunnerContextWindow != 1_000_000 {
		t.Fatalf("default runner context window = %d", defaultRunnerContextWindow)
	}
	want := 800_000
	if got := srv.autoCompactTokenThreshold(defaultRunnerContextWindow); got != want {
		t.Fatalf("default auto compact threshold = %d, want %d", got, want)
	}
	if defaultRunnerAutoCompactContextPercent != 80 {
		t.Fatalf("default auto compact context percent = %d", defaultRunnerAutoCompactContextPercent)
	}
	if got := srv.autoCompactTokenThreshold(250_000); got != 200_000 {
		t.Fatalf("250k context auto compact threshold = %d, want 200000", got)
	}
	if _, err := srv.settingsStore.Set(configStoreKey("autoCompactTokenThreshold"), float64(123_456)); err != nil {
		t.Fatalf("set explicit auto compact threshold: %v", err)
	}
	if got := srv.autoCompactTokenThreshold(defaultRunnerContextWindow); got != 123_456 {
		t.Fatalf("explicit auto compact threshold = %d, want 123456", got)
	}
	if got := runnerContextWindow(SessionRunnerChatOptions{Model: "mimo-v2.5"}); got != defaultRunnerContextWindow {
		t.Fatalf("default model context window = %d, want %d", got, defaultRunnerContextWindow)
	}
	custom := SessionRunnerChatOptions{RuntimeSessionConfig: map[string]any{"contextWindow": float64(250000)}}
	if got := runnerContextWindow(custom); got != 250000 {
		t.Fatalf("runtime context window = %d, want 250000", got)
	}
}

func TestProviderContextPressureRequiresOneDurableCompaction(t *testing.T) {
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{"type": "message", "role": "user", "text": "continue the scientific task"}},
		{EventID: 2, Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "interrupted",
			"reason_code": sessionRunnerProviderContextPressureReasonCode,
		}},
	}
	if !providerContextPressureRequiresCompaction(entries) {
		t.Fatal("unresolved provider context pressure must force compaction")
	}

	withNewUserInput := append(append([]eventjournal.Entry(nil), entries...), eventjournal.Entry{
		EventID: 3, Message: eventjournal.Message{"type": "message", "role": "user", "text": "please continue"},
	})
	if !providerContextPressureRequiresCompaction(withNewUserInput) {
		t.Fatal("a follow-up must not erase unresolved provider context pressure")
	}

	compacted := append(append([]eventjournal.Entry(nil), withNewUserInput...), eventjournal.Entry{
		EventID: 4, Message: eventjournal.Message{
			"type": "session_compact", "trigger": "auto", "summary": "durable compact summary",
		},
	})
	if providerContextPressureRequiresCompaction(compacted) {
		t.Fatal("a durable compact boundary must clear provider context pressure")
	}

	completedWithoutCompaction := append(append([]eventjournal.Entry(nil), entries...), eventjournal.Entry{
		EventID: 3, Message: eventjournal.Message{"type": "message", "role": "assistant", "text": "completed after switching provider"},
	})
	if providerContextPressureRequiresCompaction(completedWithoutCompaction) {
		t.Fatal("a newer completed assistant turn must clear stale provider context pressure")
	}
}

func TestSessionRunnerAutoCompactForcesRecoveryAfterProviderContextPressure(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	if _, err := srv.settingsStore.Set(configStoreKey("autoCompactEnabled"), true); err != nil {
		t.Fatalf("set auto compact enabled: %v", err)
	}
	if _, err := srv.settingsStore.Set(configStoreKey("autoCompactTokenThreshold"), float64(900000)); err != nil {
		t.Fatalf("set high auto compact threshold: %v", err)
	}
	session := sessionstore.Session{ID: "context-pressure-compact", Title: "Context pressure compact", WorkDir: root}
	if err := srv.sessionStore.Save(session); err != nil {
		t.Fatalf("save session: %v", err)
	}
	for index, message := range []eventjournal.Message{
		{"type": "message", "role": "user", "text": "continue the complex biomedical task"},
		{
			"type": "runner_checkpoint", "role": "system", "status": "interrupted",
			"reasonCode": sessionRunnerProviderContextPressureReasonCode,
		},
	} {
		if _, err := srv.eventJournal.Append(session.ID, message, eventjournal.Metadata{
			ClientMessageID: fmt.Sprintf("context-pressure-%d", index+1),
		}); err != nil {
			t.Fatalf("append context pressure fixture %d: %v", index+1, err)
		}
	}
	entries, err := srv.eventJournal.ReadAfter(session.ID, 0, 20)
	if err != nil {
		t.Fatalf("read context pressure fixture: %v", err)
	}
	updated, result, err := srv.autoCompactSessionForRunner(
		context.Background(), SessionRunnerChatOptions{ReplayLimit: 20}, session, entries, nil,
	)
	if err != nil {
		t.Fatalf("autoCompactSessionForRunner() error = %v", err)
	}
	if !result.Triggered {
		t.Fatalf("context pressure did not force compact: %#v", result)
	}
	if result.EstimatedTokens >= result.Threshold {
		t.Fatalf("fixture unexpectedly reached configured threshold: %#v", result)
	}
	if !strings.Contains(result.Message, "reason=provider_context_pressure") {
		t.Fatalf("forced compact result did not preserve reason: %#v", result)
	}
	if providerContextPressureRequiresCompaction(updated) {
		t.Fatalf("forced compact did not clear context pressure: %#v", updated)
	}
}

func TestSessionRunnerTrustedRuntimeContextUsesServerTimeAndDurableTaskStart(t *testing.T) {
	now := time.Date(2026, 8, 1, 2, 3, 4, 500, time.UTC)
	streamStarted := time.Date(2026, 7, 31, 17, 7, 32, 0, time.UTC)
	sessionStarted := streamStarted.Add(-time.Hour)
	run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{
		Stream: transcriptstore.Stream{CreatedAt: streamStarted},
	}}
	if got := sessionRunnerTaskStartedAt(sessionstore.Session{CreatedAt: sessionStarted}, run); !got.Equal(streamStarted) {
		t.Fatalf("task started at = %s, want %s", got, streamStarted)
	}
	context := sessionRunnerTrustedRuntimeContext(now, streamStarted, sessionstore.Session{
		ID:      "frame-identity",
		Project: &sessionstore.Project{ID: "project-identity"},
	}, nil)
	for _, required := range []string{
		"current_time_utc: 2026-08-01T02:03:04.0000005Z",
		"project_id: project-identity",
		"root_frame_id: frame-identity",
		"frame_id: frame-identity",
		"project_id is the authoritative project key",
		"a task or conversation ID means frame_id",
		"UUID-shaped frame IDs are valid Synon Biomed task IDs",
		"TaskGet and TaskList read only the separate delegated/background task store",
		"Never pass project_id, root_frame_id, or frame_id to those tools",
		"answer questions about the current project or task directly from this trusted context",
		"current_date: 2026-08-01",
		"task_started_at: 2026-07-31T17:07:32Z",
		"retrieval_as_of: 2026-08-01T02:03:04.0000005Z",
		"explicitly provisional hypotheses only",
		"Discovery lists, snippets, and bibliographic identity alone do not prove a scientific proposition",
		"exact successful tool_call_id",
		"separate search hits are not interchangeable evidence",
		"keep the assertion in the unresolved queue",
		"Heuristic arithmetic must never be described as docking",
		"Never mark a stage complete merely because a draft file exists",
		"Before save_artifacts and again before the final answer",
		"software version, entity or record count, unit, identifier, and headline result",
		"Do not repeat an already valid expensive source computation",
		"latest relevant tool result failed",
		"every domain statement in the opening, body, and closing must be traceable",
		"terminology copied from unrelated tasks",
		"once the latest bounded validation covers the unchanged current source and artifact bytes",
		"Do not rerun the same validator, reread the same files, or resave unchanged bytes",
	} {
		if !strings.Contains(context, required) {
			t.Fatalf("trusted context missing %q:\n%s", required, context)
		}
	}
	temporal := sessionRunnerTemporalGroundingContext(now)
	for _, required := range []string{
		"Trusted temporal grounding (server supplied)",
		"current_date: 2026-08-01",
		"retrieval_as_of: 2026-08-01T02:03:04.0000005Z",
		"past N years against current_date",
		"conflicting cutoff is stale task data",
	} {
		if !strings.Contains(temporal, required) {
			t.Fatalf("temporal grounding missing %q:\n%s", required, temporal)
		}
	}
	combined := appendSessionRunnerTrustedRuntimeContext("Custom agent profile.", context)
	if !strings.HasPrefix(combined, "Custom agent profile.\n\nTrusted Synon runtime context") ||
		!strings.HasSuffix(combined, "the latest check explicitly failed or omitted a required assertion.") {
		t.Fatalf("combined system prompt ordering = %q", combined)
	}
	if got := sessionRunnerTaskStartedAt(sessionstore.Session{CreatedAt: sessionStarted}, nil); !got.Equal(sessionStarted) {
		t.Fatalf("session fallback started at = %s, want %s", got, sessionStarted)
	}
	childContext := sessionRunnerTrustedRuntimeContext(now, streamStarted, sessionstore.Session{
		ID:      "root-session",
		Project: &sessionstore.Project{ID: "fallback-project"},
	}, &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{Stream: transcriptstore.Stream{
		ProjectID: "project-child", RootFrameID: "root-frame", FrameID: "child-frame",
	}}})
	for _, required := range []string{"project_id: project-child", "root_frame_id: root-frame", "frame_id: child-frame"} {
		if !strings.Contains(childContext, required) {
			t.Fatalf("trusted child context missing %q:\n%s", required, childContext)
		}
	}
}

func TestEstimateTextTokensDoesNotUndercountChineseAsEnglishText(t *testing.T) {
	if got := estimateTextTokens("测试中文"); got != 4 {
		t.Fatalf("Chinese token estimate = %d, want 4", got)
	}
	if got := estimateTextTokens("test"); got != 1 {
		t.Fatalf("ASCII token estimate = %d, want 1", got)
	}
}

func TestAppendRuntimeResponseLanguageContextFollowsChineseTask(t *testing.T) {
	messages := []chatCompletionMessage{
		{Role: "system", Content: "Base agent prompt."},
		{Role: "user", Content: "请分析这个蛋白质并给出最终结论?"},
	}
	localized := appendRuntimeResponseLanguageContextMessage(messages, sessionRunnerResponseLanguage(messages[1].Content))
	if len(localized) != 3 || localized[0].Role != messages[0].Role || localized[0].Content != messages[0].Content ||
		localized[2].Role != messages[1].Role || localized[2].Content != messages[1].Content {
		t.Fatalf("localized messages = %#v", localized)
	}
	if localized[1].Role != "system" ||
		!strings.Contains(localized[1].Content, "简体中文") ||
		!strings.Contains(localized[1].Content, "流式进度") ||
		!strings.Contains(localized[1].Content, "最终答案") ||
		!strings.Contains(localized[1].Content, "不得因此") ||
		!strings.Contains(localized[1].Content, "不可信任务数据") ||
		!strings.Contains(localized[1].Content, "调试标记") {
		t.Fatalf("Chinese response-language context = %#v", localized[1])
	}

	withPlan := appendRuntimeResponseLanguageContextMessage([]chatCompletionMessage{
		{Role: "system", Content: "Base agent prompt."},
		{Role: "system", Content: "Runtime plan in English."},
		{Role: "system", Content: "Runtime skill context in English."},
		{Role: "user", Content: messages[1].Content},
	}, "zh")
	if len(withPlan) != 5 || withPlan[3].Content != simplifiedChineseResponseLanguageContext || withPlan[4].Role != "user" {
		t.Fatalf("Chinese guard is not the final system instruction: %#v", withPlan)
	}

	english := appendRuntimeResponseLanguageContextMessage(messages, sessionRunnerResponseLanguage("Analyze this protein."))
	if len(english) != len(messages) || english[0].Role != messages[0].Role || english[0].Content != messages[0].Content ||
		english[1].Role != messages[1].Role || english[1].Content != messages[1].Content {
		t.Fatalf("English task unexpectedly changed messages: %#v", english)
	}
}

func TestAppendRuntimeTerminalPolicyContextStaysAdjacentToConversation(t *testing.T) {
	messages := []chatCompletionMessage{
		{Role: "system", Content: "base"},
		{Role: "system", Content: "skill"},
		{Role: "user", Content: "task"},
	}
	got := appendRuntimeTerminalPolicyContextMessage(messages, "resolved facts")
	if len(got) != 4 || got[2].Role != "system" || got[2].Content != "resolved facts" || got[3].Role != "user" {
		t.Fatalf("terminal policy order=%#v", got)
	}
}

func TestSessionRunnerChatDoesNotAutoCompactWhenDisabled(t *testing.T) {
	var sawCompactContext atomic.Bool
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		for _, message := range request.Messages {
			if strings.Contains(message.Content, "Synon compact handoff context:") {
				sawCompactContext.Store(true)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"without auto compact"}}]}`))
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	if _, err := srv.settingsStore.Set(configStoreKey("autoCompactEnabled"), false); err != nil {
		t.Fatalf("set auto compact disabled: %v", err)
	}
	if _, err := srv.settingsStore.Set(configStoreKey("autoCompactTokenThreshold"), float64(20)); err != nil {
		t.Fatalf("set auto compact threshold: %v", err)
	}
	httpServer := httptestServer(t, srv)
	sessionID := "auto-compact-disabled-session"
	postToolInput(t, httpServer.URL, "session_store", map[string]any{"sessionId": sessionID})
	postToolInput(t, httpServer.URL, "session_append", map[string]any{
		"sessionId":       sessionID,
		"role":            "user",
		"message":         map[string]any{"type": "message", "text": "disabled auto compact " + strings.Repeat("context ", 80)},
		"clientMessageId": "auto-compact-disabled-user",
	})
	session, ok, err := srv.sessionStore.Get(sessionID)
	if err != nil || !ok {
		t.Fatalf("session get ok=%v err=%v", ok, err)
	}
	entries, err := srv.eventJournal.ReadAfter(sessionID, 0, 50)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if _, err := srv.runSessionRunnerChat(context.Background(), SessionRunnerChatOptions{RunnerID: "auto-compact-disabled-runner",
		Endpoint:         modelAPI.URL + "/v1/chat/completions",
		Model:            "auto-compact-model",
		MaxToolRounds:    1,
		MaxAttempts:      1,
		ReplayLimit:      50,
		OutputLimitBytes: 64 * 1024,
	}, session, entries, nil); err != nil {
		t.Fatalf("runSessionRunnerChat() error = %v", err)
	}
	if sawCompactContext.Load() {
		t.Fatalf("model request unexpectedly included compact context")
	}
	replayed, err := srv.eventJournal.ReadAfter(sessionID, 0, 50)
	if err != nil {
		t.Fatalf("read replayed journal: %v", err)
	}
	for _, entry := range replayed {
		if stringValue(entry.Message["type"]) == "session_compact" {
			t.Fatalf("auto compact disabled but compact event was written: %#v", replayed)
		}
	}
}

func TestSessionRunnerChatInjectsAllowedDynamicMCPContext(t *testing.T) {
	t.Skip("legacy generic-session MCP projection retired; owner-scoped MCP methods are exposed through the repl/host bridge")
	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		joined := ""
		for _, message := range request.Messages {
			joined += "\n" + message.Content
		}
		if !strings.Contains(joined, "Synon MCP tool context:") ||
			!strings.Contains(joined, "Invocation contract:") ||
			!strings.Contains(joined, "mcp__remote__echo") ||
			!strings.Contains(joined, "Preserve JSON Schema types exactly") {
			t.Fatalf("model request missing MCP context: %#v", request.Messages)
		}
		foundSchema := false
		for _, tool := range request.Tools {
			if tool.Function.Name == "mcp__remote__echo" {
				foundSchema = true
			}
		}
		if !foundSchema {
			t.Fatalf("model request missing MCP tool schema: %#v", request.Tools)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"mcp context available"}}]}`))
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	mcpClient := configureMCPHTTPFixture(t, root)
	srv := New(Options{FileRoot: root, HTTPClient: mcpClient})
	httpServer := httptestServer(t, srv)
	sessionID := "mcp-context-session"
	postToolInput(t, httpServer.URL, "session_store", map[string]any{"sessionId": sessionID})
	postToolInput(t, httpServer.URL, "session_append", map[string]any{
		"sessionId":       sessionID,
		"role":            "user",
		"message":         map[string]any{"type": "message", "text": "use the remote echo MCP tool if needed"},
		"clientMessageId": "mcp-context-user-1",
	})
	result, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{RunnerID: "mcp-context-runner",
		Endpoint:         modelAPI.URL + "/v1/chat/completions",
		Model:            "runner-model",
		AllowedTools:     []string{"mcp__remote__echo"},
		MaxToolRounds:    1,
		MaxAttempts:      1,
		ReplayLimit:      50,
		OutputLimitBytes: 64 * 1024,
	})
	if err != nil {
		t.Fatalf("RunSessionRunnerChatOnce() error = %v", err)
	}
	if !result.Claimed || result.SessionID != sessionID || result.Status != "completed" {
		t.Fatalf("runner result = %+v", result)
	}
	if requests.Load() != 1 {
		t.Fatalf("model requests = %d", requests.Load())
	}
}

func TestSessionRunnerChatRanksMCPContextFromSelectedSkillTools(t *testing.T) {
	t.Skip("legacy generic-session MCP projection retired; owner-scoped MCP methods are exposed through the repl/host bridge")
	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		joined := ""
		for _, message := range request.Messages {
			joined += "\n" + message.Content
		}
		if !strings.Contains(joined, "Runtime skill context:") ||
			!strings.Contains(joined, "remote-browser") {
			t.Fatalf("model request missing selected skill context: %#v", request.Messages)
		}
		if !strings.Contains(joined, "Synon MCP tool context:") ||
			!strings.Contains(joined, "mcp__remote__echo") ||
			!strings.Contains(joined, "selectedBySkill=remote-browser") {
			t.Fatalf("model request missing skill-ranked MCP context: %#v", request.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"skill-ranked mcp context available"}}]}`))
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	mcpClient := configureMCPHTTPFixture(t, root)
	skillDir := filepath.Join(root, "skills", "remote-browser")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: remote-browser
description: Use the remote browser echo connector for delegated page inspection
tags: [mcp, browser]
keywords: [browser, page, inspection]
tools:
  - mcp__remote__echo
---
Use the remote MCP echo tool when browser page inspection needs a connector.
`), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	srv := New(Options{FileRoot: root, HTTPClient: mcpClient, SkillDirectories: []string{filepath.Join(root, "skills")}})
	httpServer := httptestServer(t, srv)
	sessionID := "mcp-skill-context-session"
	postToolInput(t, httpServer.URL, "session_store", map[string]any{"sessionId": sessionID})
	postToolInput(t, httpServer.URL, "session_append", map[string]any{
		"sessionId":       sessionID,
		"role":            "user",
		"message":         map[string]any{"type": "message", "text": "inspect the browser page through the connector"},
		"clientMessageId": "mcp-skill-context-user-1",
	})
	result, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{RunnerID: "mcp-skill-context-runner",
		Endpoint:         modelAPI.URL + "/v1/chat/completions",
		Model:            "runner-model",
		AllowedTools:     []string{"mcp__remote__echo"},
		MaxToolRounds:    1,
		MaxAttempts:      1,
		ReplayLimit:      50,
		OutputLimitBytes: 64 * 1024,
	})
	if err != nil {
		t.Fatalf("RunSessionRunnerChatOnce() error = %v", err)
	}
	if !result.Claimed || result.SessionID != sessionID || result.Status != "completed" {
		t.Fatalf("runner result = %+v", result)
	}
	if requests.Load() != 1 {
		t.Fatalf("model requests = %d", requests.Load())
	}
}

func TestSessionRunnerChatDoesNotInjectSkillsWhoseBodyRequiresForbiddenTools(t *testing.T) {
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		joined := ""
		for _, message := range request.Messages {
			joined += "\n" + message.Content
		}
		if strings.Contains(joined, "FORBIDDEN_SKILL_BODY") {
			t.Fatalf("model request leaked a skill outside the runtime tool authority: %#v", request.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"authority preserved"}}]}`))
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "forbidden-shell-analysis")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: forbidden-shell-analysis
description: Run the distinctive forbidden shell analysis workflow
keywords: [distinctive, forbidden, shell, analysis]
---
Standard tools: Bash

FORBIDDEN_SKILL_BODY
`), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := New(Options{FileRoot: root, SkillDirectories: []string{filepath.Join(root, "skills")}})
	httpServer := httptestServer(t, srv)
	sessionID := "forbidden-skill-context-session"
	postToolInput(t, httpServer.URL, "session_store", map[string]any{"sessionId": sessionID})
	postToolInput(t, httpServer.URL, "session_append", map[string]any{
		"sessionId": sessionID, "role": "user",
		"message":         map[string]any{"type": "message", "text": "run the distinctive forbidden shell analysis workflow"},
		"clientMessageId": "forbidden-skill-context-user-1",
	})
	result, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{RunnerID: "forbidden-skill-context-runner", Endpoint: modelAPI.URL + "/v1/chat/completions",
		Model: "runner-model", AllowedTools: []string{"Read"}, MaxToolRounds: 1, MaxAttempts: 1,
		ReplayLimit: 50, OutputLimitBytes: 64 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Claimed || result.Status != "completed" {
		t.Fatalf("runner result = %+v", result)
	}
}

func TestSessionRunnerChatOnceConsumesTaskRunThroughAgentRuntimeLoop(t *testing.T) {
	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		joined := ""
		for _, message := range request.Messages {
			joined += "\n" + message.Content
		}
		if !strings.Contains(joined, "TaskRun objective:") ||
			!strings.Contains(joined, "Prepare runtime loop evidence") ||
			!strings.Contains(joined, "internal/agentruntime") {
			t.Fatalf("TaskRun model request missing runtime context: %#v", request.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"TaskRun completed through agentruntime chat loop with evidence."}}]}`))
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	httpServer := httptestServer(t, srv)
	started := postToolInput(t, httpServer.URL, "TaskRun", map[string]any{
		"action":           "start",
		"objective":        "Prepare runtime loop evidence",
		"success_criteria": []any{"chat runner consumes the TaskRun session"},
		"constraints":      []any{"use focused test evidence"},
		"task_graph":       explicitTaskRunTestGraph("execute"),
	})
	startedRun := started["result"].(map[string]any)
	runID := startedRun["run_id"].(string)
	sessionID := startedRun["session_id"].(string)

	result, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{RunnerID: "taskrun-chat-runner",
		Endpoint:         modelAPI.URL + "/v1/chat/completions",
		Model:            "test-model",
		LeaseTTL:         time.Minute,
		ReplayLimit:      20,
		OutputLimitBytes: 64 * 1024,
		MaxToolRounds:    1,
		MaxAttempts:      1,
	})
	if err != nil {
		t.Fatalf("RunSessionRunnerChatOnce() error = %v", err)
	}
	if !result.Claimed || result.SessionID != sessionID || result.Status != "completed" || result.AssistantEventID == 0 || result.FinishEventID == 0 {
		t.Fatalf("runner result = %+v", result)
	}
	if requests.Load() != 1 {
		t.Fatalf("model requests = %d", requests.Load())
	}
	status := postToolInput(t, httpServer.URL, "TaskRun", map[string]any{"action": "status", "run_id": runID})
	statusRun := status["result"].(map[string]any)
	if statusRun["status"] != "completed" || len(statusRun["evidence_index"].([]any)) == 0 {
		t.Fatalf("TaskRun status after chat runner = %#v", statusRun)
	}
	active := statusRun["active_children"].([]any)
	if len(active) != 0 {
		t.Fatalf("completed explicit TaskRun retained active work: %#v", statusRun)
	}
	steps := statusRun["steps"].([]any)
	if len(steps) != 1 || steps[0].(map[string]any)["status"] != "completed" {
		t.Fatalf("TaskRun step states after chat runner = %#v", steps)
	}
	replayed := postToolInput(t, httpServer.URL, "session_replay", map[string]any{
		"sessionId": sessionID,
		"limit":     float64(20),
	})
	entries := replayed["result"].(map[string]any)["entries"].([]any)
	if !hasJournalEntry(entries, "runner_checkpoint", "running", "") ||
		!hasJournalEntry(entries, "message", "", "TaskRun completed through agentruntime chat loop") ||
		!hasJournalEntry(entries, "runner_finished", "completed", "") {
		t.Fatalf("TaskRun chat runner journal entries = %#v", entries)
	}
}

func TestSessionRunnerChatInjectsRuntimeSkillContext(t *testing.T) {
	var requests atomic.Int64
	skillsDir := filepath.Join(t.TempDir(), "skills")
	if err := os.MkdirAll(filepath.Join(skillsDir, "skill-a"), 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	skillPath := filepath.Join(skillsDir, "skill-a", "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(`---
name: runtime-skill-work
description: Execute the runtime skill work workflow
tags: [runtime, skill, work]
references:
  - handoff.md
---
When asked for runtime work, use task tools and summarize state.
`), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "skill-a", "handoff.md"), []byte("handoff reference instructions"), 0o600); err != nil {
		t.Fatalf("write skill reference: %v", err)
	}

	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		systemContent := []string{}
		for _, message := range request.Messages {
			if message.Role == "system" {
				systemContent = append(systemContent, message.Content)
			}
		}
		joined := strings.Join(systemContent, "\n")
		if !strings.Contains(joined, "Runtime skill context:") ||
			!strings.Contains(joined, "runtime-skill-work") ||
			!strings.Contains(joined, "Source: "+skillPath) ||
			!strings.Contains(joined, "References: handoff.md") ||
			!strings.Contains(joined, "handoff reference instructions") {
			t.Fatalf("model request missing runtime skill context injection: %v", systemContent)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"context-aware response"}}]}`))
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	srv := New(Options{FileRoot: root, SkillDirectories: []string{skillsDir}})
	session := sessionstore.Session{ID: "session-skill-1"}
	entries := []eventjournal.Entry{
		{
			Message: eventjournal.Message{
				"role":    "user",
				"content": "Run runtime skill work",
			},
		},
	}
	result, err := srv.runSessionRunnerChat(context.Background(), SessionRunnerChatOptions{RunnerID: "chat-runner-skill",
		SelectedSkillNames: []string{"runtime-skill-work"},
		Endpoint:           modelAPI.URL + "/v1/chat/completions",
		Model:              "context-model",
		MaxToolRounds:      1,
		MaxAttempts:        1,
		ReplayLimit:        20,
		OutputLimitBytes:   64 * 1024,
	}, session, entries, nil)
	if err != nil {
		t.Fatalf("runSessionRunnerChat() error = %v", err)
	}
	if result != "context-aware response" {
		t.Fatalf("assistant result = %q", result)
	}
	if requests.Load() != 1 {
		t.Fatalf("model requests = %d", requests.Load())
	}
}

func TestSessionRunnerChatPreparationTimeoutInterruptsBlockedLifecycleHook(t *testing.T) {
	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"unexpected"}}]}`))
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	if _, err := srv.settingsStore.Set("hooks", map[string]any{
		"UserPromptSubmit": []any{
			map[string]any{
				"hooks": []any{
					map[string]any{"type": "command", "shell": "Bash", "command": "sleep 5", "timeout": 10},
				},
			},
		},
	}); err != nil {
		t.Fatalf("set blocking lifecycle hook: %v", err)
	}
	session := sessionstore.Session{ID: "preparation-timeout-session", Title: "Preparation timeout", WorkDir: root}
	entries := []eventjournal.Entry{{
		SessionID: session.ID, EventID: 1,
		Message: eventjournal.Message{"type": "message", "role": "user", "text": "run the bounded task"},
	}}
	started := time.Now()
	_, err := srv.runSessionRunnerChat(context.Background(), SessionRunnerChatOptions{RunnerID: "preparation-timeout-runner",
		Endpoint:              modelAPI.URL + "/v1/chat/completions",
		Model:                 "preparation-timeout-model",
		PreparationTimeout:    200 * time.Millisecond,
		RequestTimeout:        time.Second,
		MaxToolRounds:         1,
		MaxAttempts:           1,
		ReplayLimit:           20,
		OutputLimitBytes:      64 * 1024,
		DisableMCPDiscovery:   true,
		DisableSkillDiscovery: true,
	}, session, entries, nil)
	var preparationErr sessionRunnerPreparationTimeout
	if !errors.As(err, &preparationErr) {
		t.Fatalf("runSessionRunnerChat() error = %v, want sessionRunnerPreparationTimeout", err)
	}
	if preparationErr.stage != "lifecycle_hooks" {
		t.Fatalf("preparation timeout stage = %q, want lifecycle_hooks", preparationErr.stage)
	}
	reason, detail := preparationErr.runnerCorrection()
	if reason != sessionRunnerPreparationTimeoutReasonCode || !strings.Contains(detail, "lifecycle_hooks") {
		t.Fatalf("runner correction reason=%q detail=%q", reason, detail)
	}
	if requests.Load() != 0 {
		t.Fatalf("model requests = %d, want 0 before preparation completes", requests.Load())
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("preparation cancellation took %s, want <= 3s", elapsed)
	}
}

func TestSessionRunnerChatRunsSessionStartAndUserPromptSubmitHooksBeforeModel(t *testing.T) {
	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		joined := ""
		for _, message := range request.Messages {
			if message.Role == "system" {
				joined += "\n" + message.Content
			}
		}
		if !strings.Contains(joined, "session context from hook") || !strings.Contains(joined, "prompt context from hook") {
			t.Fatalf("model request missing runtime hook context: %#v", request.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hooked response"}}]}`))
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	sessionStartCommand := `$stdin = [Console]::In.ReadToEnd(); Set-Content -LiteralPath 'session-start-hook.json' -Value $stdin; Write-Output '{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"session context from hook"}}'`
	userPromptCommand := `$stdin = [Console]::In.ReadToEnd(); Set-Content -LiteralPath 'user-prompt-hook.json' -Value $stdin; Write-Output '{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":"prompt context from hook"}}'`
	if _, err := srv.settingsStore.Set("hooks", map[string]any{
		"SessionStart": []any{
			map[string]any{
				"matcher": "resume",
				"hooks": []any{
					map[string]any{"type": "command", "shell": "PowerShell", "command": sessionStartCommand, "timeout": commandHookFixtureTimeoutSeconds},
				},
			},
		},
		"UserPromptSubmit": []any{
			map[string]any{
				"hooks": []any{
					map[string]any{"type": "command", "shell": "PowerShell", "command": userPromptCommand, "timeout": commandHookFixtureTimeoutSeconds},
				},
			},
		},
	}); err != nil {
		t.Fatalf("set lifecycle hooks: %v", err)
	}
	allowPairingForTest(t, srv, "feishu", "ou_lifecycle_hooks")
	httpServer := httptestServer(t, srv)
	first := postFeishuEvent(t, httpServer.URL, []byte(`{
		"schema": "2.0",
		"header": {"event_id": "evt-lifecycle-hooks-1", "event_type": "im.message.receive_v1"},
		"event": {
			"sender": {"sender_id": {"open_id": "ou_lifecycle_hooks"}},
			"message": {
				"message_id": "om_lifecycle_hooks_1",
				"chat_id": "oc_lifecycle_hooks",
				"chat_type": "p2p",
				"message_type": "text",
				"content": "{\"text\":\"run lifecycle hooks\"}"
			}
		}
	}`))
	sessionID := first["sessionId"].(string)

	result, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{RunnerID: "chat-runner-hooks",
		Endpoint:         modelAPI.URL + "/v1/chat/completions",
		Model:            "hook-model",
		LeaseTTL:         time.Minute,
		ReplayLimit:      20,
		OutputLimitBytes: 64 * 1024,
	})
	if err != nil {
		t.Fatalf("RunSessionRunnerChatOnce() error = %v", err)
	}
	if !result.Claimed || result.SessionID != sessionID || result.Status != "completed" || requests.Load() != 1 {
		t.Fatalf("runner result = %+v requests=%d", result, requests.Load())
	}
	sessionHookInput, err := os.ReadFile(filepath.Join(root, "session-start-hook.json"))
	if err != nil {
		t.Fatalf("read session start hook input: %v", err)
	}
	if !strings.Contains(string(sessionHookInput), `"hook_event_name":"SessionStart"`) || !strings.Contains(string(sessionHookInput), `"source":"resume"`) || !strings.Contains(string(sessionHookInput), `"model":"hook-model"`) {
		t.Fatalf("session hook input = %s", sessionHookInput)
	}
	userHookInput, err := os.ReadFile(filepath.Join(root, "user-prompt-hook.json"))
	if err != nil {
		t.Fatalf("read user prompt hook input: %v", err)
	}
	if !strings.Contains(string(userHookInput), `"hook_event_name":"UserPromptSubmit"`) || !strings.Contains(string(userHookInput), `"prompt":"run lifecycle hooks"`) {
		t.Fatalf("user prompt hook input = %s", userHookInput)
	}
	entries, err := srv.runtimeStore.List(agentRuntimeHookAuditNamespace)
	if err != nil {
		t.Fatalf("list hook audit entries: %v", err)
	}
	if len(entries) < 2 {
		t.Fatalf("expected lifecycle hook audit entries, got %#v", entries)
	}
}

func TestSessionRunnerChatUserPromptSubmitHookCanBlockModelCall(t *testing.T) {
	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		t.Fatalf("model should not be called when UserPromptSubmit hook blocks")
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	blockCommand := `Write-Output '{"decision":"block","reason":"blocked by user prompt hook"}'`
	if _, err := srv.settingsStore.Set("hooks", map[string]any{
		"UserPromptSubmit": []any{
			map[string]any{
				"hooks": []any{
					map[string]any{"type": "command", "shell": "PowerShell", "command": blockCommand, "timeout": commandHookFixtureTimeoutSeconds},
				},
			},
		},
	}); err != nil {
		t.Fatalf("set blocking hook: %v", err)
	}
	allowPairingForTest(t, srv, "feishu", "ou_prompt_block")
	httpServer := httptestServer(t, srv)
	postFeishuEvent(t, httpServer.URL, []byte(`{
		"schema": "2.0",
		"header": {"event_id": "evt-prompt-block-1", "event_type": "im.message.receive_v1"},
		"event": {
			"sender": {"sender_id": {"open_id": "ou_prompt_block"}},
			"message": {
				"message_id": "om_prompt_block_1",
				"chat_id": "oc_prompt_block",
				"chat_type": "p2p",
				"message_type": "text",
				"content": "{\"text\":\"this should be blocked\"}"
			}
		}
	}`))

	result, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{RunnerID: "chat-runner-prompt-block",
		Endpoint:         modelAPI.URL + "/v1/chat/completions",
		Model:            "hook-model",
		LeaseTTL:         time.Minute,
		ReplayLimit:      20,
		OutputLimitBytes: 64 * 1024,
	})
	if err != nil {
		t.Fatalf("RunSessionRunnerChatOnce() error = %v", err)
	}
	if !result.Claimed || result.Status != "failed" || requests.Load() != 0 {
		t.Fatalf("runner result = %+v requests=%d", result, requests.Load())
	}
	entries, err := srv.runtimeStore.List(agentRuntimeHookAuditNamespace)
	if err != nil {
		t.Fatalf("list hook audit entries: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("missing blocking hook audit entry")
	}
}

func TestSessionRunnerChatRunsStopHookAfterAssistantMessage(t *testing.T) {
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"assistant final for stop hook"}}]}`))
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	stopCommand := `$stdin = [Console]::In.ReadToEnd(); Set-Content -LiteralPath 'stop-hook-input.json' -Value $stdin; Write-Output '{"systemMessage":"stop hook system message"}'`
	if _, err := srv.settingsStore.Set("hooks", map[string]any{
		"Stop": []any{
			map[string]any{
				"hooks": []any{
					map[string]any{"type": "command", "shell": "PowerShell", "command": stopCommand, "timeout": commandHookFixtureTimeoutSeconds},
				},
			},
		},
	}); err != nil {
		t.Fatalf("set stop hook: %v", err)
	}
	allowPairingForTest(t, srv, "feishu", "ou_stop_hook")
	httpServer := httptestServer(t, srv)
	first := postFeishuEvent(t, httpServer.URL, []byte(`{
		"schema": "2.0",
		"header": {"event_id": "evt-stop-hook-1", "event_type": "im.message.receive_v1"},
		"event": {
			"sender": {"sender_id": {"open_id": "ou_stop_hook"}},
			"message": {
				"message_id": "om_stop_hook_1",
				"chat_id": "oc_stop_hook",
				"chat_type": "p2p",
				"message_type": "text",
				"content": "{\"text\":\"run stop hook\"}"
			}
		}
	}`))
	sessionID := first["sessionId"].(string)

	result, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{RunnerID: "chat-runner-stop-hook",
		Endpoint:         modelAPI.URL + "/v1/chat/completions",
		Model:            "hook-model",
		LeaseTTL:         time.Minute,
		ReplayLimit:      20,
		OutputLimitBytes: 64 * 1024,
	})
	if err != nil {
		t.Fatalf("RunSessionRunnerChatOnce() error = %v", err)
	}
	if !result.Claimed || result.SessionID != sessionID || result.Status != "completed" {
		t.Fatalf("runner result = %+v", result)
	}
	hookInput, err := os.ReadFile(filepath.Join(root, "stop-hook-input.json"))
	if err != nil {
		t.Fatalf("read stop hook input: %v", err)
	}
	if !strings.Contains(string(hookInput), `"hook_event_name":"Stop"`) || !strings.Contains(string(hookInput), `"last_assistant_message":"assistant final for stop hook"`) {
		t.Fatalf("stop hook input = %s", hookInput)
	}
	replayed := postToolInput(t, httpServer.URL, "session_replay", map[string]any{
		"sessionId":    sessionID,
		"afterEventId": float64(0),
		"limit":        float64(20),
	})
	entries := replayed["result"].(map[string]any)["entries"].([]any)
	if !hasJournalEntry(entries, "hook_message", "", "stop hook system message") {
		t.Fatalf("missing Stop hook system message: %#v", entries)
	}
}

func TestSessionRunnerChatStopHookCanMarkRunnerFailed(t *testing.T) {
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"assistant before stop block"}}]}`))
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	if _, err := srv.settingsStore.Set("hooks", map[string]any{
		"Stop": []any{
			map[string]any{
				"hooks": []any{
					map[string]any{"type": "command", "shell": "PowerShell", "command": `Write-Output '{"decision":"block","reason":"blocked by stop hook"}'`, "timeout": commandHookFixtureTimeoutSeconds},
				},
			},
		},
	}); err != nil {
		t.Fatalf("set stop blocking hook: %v", err)
	}
	allowPairingForTest(t, srv, "feishu", "ou_stop_block")
	httpServer := httptestServer(t, srv)
	postFeishuEvent(t, httpServer.URL, []byte(`{
		"schema": "2.0",
		"header": {"event_id": "evt-stop-block-1", "event_type": "im.message.receive_v1"},
		"event": {
			"sender": {"sender_id": {"open_id": "ou_stop_block"}},
			"message": {
				"message_id": "om_stop_block_1",
				"chat_id": "oc_stop_block",
				"chat_type": "p2p",
				"message_type": "text",
				"content": "{\"text\":\"run stop block\"}"
			}
		}
	}`))

	result, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{RunnerID: "chat-runner-stop-block",
		Endpoint:         modelAPI.URL + "/v1/chat/completions",
		Model:            "hook-model",
		LeaseTTL:         time.Minute,
		ReplayLimit:      20,
		OutputLimitBytes: 64 * 1024,
	})
	if err != nil {
		t.Fatalf("RunSessionRunnerChatOnce() error = %v", err)
	}
	if !result.Claimed || result.Status != "failed" {
		t.Fatalf("runner result = %+v", result)
	}
	entries, err := srv.runtimeStore.List(agentRuntimeHookAuditNamespace)
	if err != nil {
		t.Fatalf("list hook audit entries: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("missing Stop hook audit entry")
	}
}

func TestSessionRunnerChatDoesNotInjectCompetingRuntimePlan(t *testing.T) {
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		messages, ok := request["messages"].([]any)
		if !ok {
			t.Fatalf("messages missing from model request: %#v", request)
		}
		encoded, _ := json.Marshal(messages)
		for _, retired := range []string{"Runtime plan:", "Execution mode:", "Milestones:"} {
			if strings.Contains(string(encoded), retired) {
				t.Fatalf("model request contains retired generic plan marker %q: %s", retired, encoded)
			}
		}
		userTaskObserved := false
		for _, raw := range messages {
			message := mapValue(raw)
			if stringValue(message["role"]) == "user" && stringValue(message["content"]) == "create a task for the follow-up" {
				userTaskObserved = true
				break
			}
		}
		if !userTaskObserved {
			t.Fatalf("model request missing canonical user task: %#v", messages)
		}
		if hasChatSystemMessage(messages, "The user's request", "create a task for the follow-up") {
			t.Fatalf("model request duplicated the task through a retired system prompt: %#v", messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"answered without a competing plan"}}]}`))
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	allowPairingForTest(t, srv, "feishu", "ou_chat_no_runtime_plan")
	httpServer := httptestServer(t, srv)
	first := postFeishuEvent(t, httpServer.URL, []byte(`{
		"schema": "2.0",
		"header": {"event_id": "evt-chat-no-runtime-plan", "event_type": "im.message.receive_v1"},
		"event": {
			"sender": {"sender_id": {"open_id": "ou_chat_no_runtime_plan"}},
			"message": {
				"message_id": "om_chat_no_runtime_plan",
				"chat_id": "oc_chat_no_runtime_plan",
				"chat_type": "p2p",
				"message_type": "text",
				"content": "{\"text\":\"create a task for the follow-up\"}"
			}
		}
	}`))
	sessionID := first["sessionId"].(string)
	result, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{RunnerID: "chat-no-runtime-plan", Endpoint: modelAPI.URL + "/v1/chat/completions",
		Model: "test-model", AllowedTools: []string{"task_create", "task_list"},
		LeaseTTL: time.Minute, ReplayLimit: 20, OutputLimitBytes: 64 * 1024,
	})
	if err != nil || !result.Claimed || result.SessionID != sessionID || result.Status != "completed" {
		t.Fatalf("runner result=%#v err=%v", result, err)
	}
	replayed := postToolInput(t, httpServer.URL, "session_replay", map[string]any{
		"sessionId": sessionID, "afterEventId": float64(0), "limit": float64(20),
	})
	entries := replayed["result"].(map[string]any)["entries"].([]any)
	for _, entry := range entries {
		message := journalEntryMessage(entry)
		if _, found := message["runtimePlan"]; found || strings.Contains(stringValue(message["message"]), "Runtime plan:") {
			t.Fatalf("retired runtime plan checkpoint remains: %#v", message)
		}
	}
	if !hasJournalEntry(entries, "message", "", "answered without a competing plan") {
		t.Fatalf("missing assistant answer: %#v", entries)
	}
}

func TestSessionRunnerChatOnceExecutesModelToolCalls(t *testing.T) {
	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sequence := requests.Add(1)
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode model request %d: %v", sequence, err)
		}
		switch sequence {
		case 1:
			tools, ok := request["tools"].([]any)
			if !ok || !hasChatToolNamed(tools, "search_skills") {
				t.Fatalf("first request missing search_skills tool: %#v", request["tools"])
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"choices": [{
					"message": {
						"role": "assistant",
						"tool_calls": [{
							"id": "call_skill_search_1",
							"type": "function",
							"function": {
								"name": "search_skills",
								"arguments": "{\"query\":\"runtime\"}"
							}
						}]
					}
				}]
			}`))
		case 2:
			messages, ok := request["messages"].([]any)
			if !ok || !hasToolResultMessage(messages, "call_skill_search_1", "") {
				t.Fatalf("second request missing tool result message: %#v", request["messages"])
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"choices": [{
					"message": {
						"role": "assistant",
						"content": "completed capability search through the fixed tool surface"
					}
				}]
			}`))
		default:
			t.Fatalf("unexpected model request %d", sequence)
		}
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	allowPairingForTest(t, srv, "feishu", "ou_chat_tool_runner")
	httpServer := httptestServer(t, srv)

	first := postFeishuEvent(t, httpServer.URL, []byte(`{
		"schema": "2.0",
		"header": {"event_id": "evt-chat-tool-runner-1", "event_type": "im.message.receive_v1"},
		"event": {
			"sender": {"sender_id": {"open_id": "ou_chat_tool_runner"}},
			"message": {
				"message_id": "om_chat_tool_runner_1",
				"chat_id": "oc_chat_tool_runner",
				"chat_type": "p2p",
				"message_type": "text",
				"content": "{\"text\":\"search the runtime capability catalog\"}"
			}
		}
	}`))
	sessionID := first["sessionId"].(string)

	result, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{RunnerID: "chat-tool-runner-a",
		Endpoint:         modelAPI.URL + "/v1/chat/completions",
		Model:            "test-model",
		LeaseTTL:         time.Minute,
		ReplayLimit:      20,
		OutputLimitBytes: 64 * 1024,
	})
	if err != nil {
		t.Fatalf("RunSessionRunnerChatOnce() error = %v", err)
	}
	if !result.Claimed || result.SessionID != sessionID || result.Status != "completed" || result.AssistantEventID == 0 || result.FinishEventID == 0 {
		t.Fatalf("runner result = %+v", result)
	}
	if requests.Load() != 2 {
		t.Fatalf("model requests = %d", requests.Load())
	}

	replayed := postToolInput(t, httpServer.URL, "session_replay", map[string]any{
		"sessionId":    sessionID,
		"afterEventId": float64(0),
		"limit":        float64(10),
	})
	entries := replayed["result"].(map[string]any)["entries"].([]any)
	if !hasModelToolCallCheckpoint(entries, "search_skills", "call_skill_search_1", "runtime") {
		t.Fatalf("missing model tool-call checkpoint: %#v", entries)
	}
	if !hasJournalEntry(entries, "runner_checkpoint", "running", "tool search_skills started") {
		t.Fatalf("missing tool start checkpoint: %#v", entries)
	}
	if !hasJournalEntry(entries, "runner_checkpoint", "completed", "tool search_skills completed") {
		t.Fatalf("missing tool completion checkpoint: %#v", entries)
	}
	if !hasRunnerToolCheckpoint(entries, "completed", "search_skills", "call_skill_search_1", "") {
		t.Fatalf("missing structured tool completion checkpoint: %#v", entries)
	}
	if !hasJournalEntry(entries, "message", "", "completed capability search through the fixed tool surface") {
		t.Fatalf("missing final assistant message: %#v", entries)
	}
	exported := postToolInput(t, httpServer.URL, "session_export", map[string]any{
		"sessionId": sessionID,
		"format":    "markdown",
	})
	markdown := exported["result"].(map[string]any)["content"].(string)
	if !strings.Contains(markdown, "Model Tool Calls:") || !strings.Contains(markdown, "Tool: search_skills") || !strings.Contains(markdown, "Tool Call: call_skill_search_1") || !strings.Contains(markdown, "runtime") {
		t.Fatalf("tool transcript markdown = %q", markdown)
	}
}

func TestSessionRunnerChatOnceExecutesFourToolCallsWithoutFixtureDerivedBatchLimit(t *testing.T) {
	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sequence := requests.Add(1)
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode model request %d: %v", sequence, err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch sequence {
		case 1:
			_, _ = w.Write([]byte(`{
				"choices": [{
					"message": {
						"role": "assistant",
						"tool_calls": [
							{"id":"call_skill_1","type":"function","function":{"name":"search_skills","arguments":"{\"query\":\"runtime\"}"}},
							{"id":"call_skill_2","type":"function","function":{"name":"search_skills","arguments":"{\"query\":\"runtime\"}"}},
							{"id":"call_skill_3","type":"function","function":{"name":"search_skills","arguments":"{\"query\":\"runtime\"}"}},
							{"id":"call_skill_4","type":"function","function":{"name":"search_skills","arguments":"{\"query\":\"runtime\"}"}}
						]
					}
				}]
			}`))
		case 2:
			messages, ok := request["messages"].([]any)
			if !ok {
				t.Fatalf("second request messages = %#v", request["messages"])
			}
			for index := 1; index <= 4; index++ {
				if !hasToolResultMessage(messages, fmt.Sprintf("call_skill_%d", index), "skills") {
					t.Fatalf("second request missing tool result %d: %#v", index, messages)
				}
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"four tools completed"}}]}`))
		default:
			t.Fatalf("unexpected model request %d", sequence)
		}
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	allowPairingForTest(t, srv, "feishu", "ou_chat_four_tools")
	httpServer := httptestServer(t, srv)
	first := postFeishuEvent(t, httpServer.URL, []byte(`{
		"schema":"2.0",
		"header":{"event_id":"evt-chat-four-tools","event_type":"im.message.receive_v1"},
		"event":{"sender":{"sender_id":{"open_id":"ou_chat_four_tools"}},"message":{"message_id":"om_chat_four_tools","chat_id":"oc_chat_four_tools","chat_type":"p2p","message_type":"text","content":"{\"text\":\"run four independent reads\"}"}}
	}`))
	sessionID := first["sessionId"].(string)

	result, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{RunnerID: "chat-four-tools-runner",
		Endpoint:         modelAPI.URL + "/v1/chat/completions",
		Model:            "test-model",
		AllowedTools:     []string{"search_skills"},
		LeaseTTL:         time.Minute,
		ReplayLimit:      40,
		OutputLimitBytes: 64 * 1024,
	})
	if err != nil {
		t.Fatalf("RunSessionRunnerChatOnce() error = %v", err)
	}
	if !result.Claimed || result.SessionID != sessionID || result.Status != "completed" {
		t.Fatalf("runner result = %+v", result)
	}
	if requests.Load() != 2 {
		t.Fatalf("model requests = %d", requests.Load())
	}
	replayed := postToolInput(t, httpServer.URL, "session_replay", map[string]any{
		"sessionId": sessionID, "afterEventId": float64(0), "limit": float64(40),
	})
	entries := replayed["result"].(map[string]any)["entries"].([]any)
	for index := 1; index <= 4; index++ {
		callID := fmt.Sprintf("call_skill_%d", index)
		if !hasModelToolCallCheckpoint(entries, "search_skills", callID, "") ||
			!hasRunnerToolCheckpoint(entries, "completed", "search_skills", callID, "skills") {
			t.Fatalf("missing durable checkpoint for %s: %#v", callID, entries)
		}
	}
}

func TestSessionRunnerChatRecoversToolTranscriptContext(t *testing.T) {
	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		var recovered string
		for _, message := range request.Messages {
			if message.Role == "system" && strings.Contains(message.Content, "Recovered tool transcript:") {
				recovered = message.Content
			}
		}
		if !strings.Contains(recovered, "tool=task_create") || !strings.Contains(recovered, "call_task_1") || !strings.Contains(recovered, "tool-created task") {
			t.Fatalf("recovered tool transcript context = %q messages=%#v", recovered, request.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"continued with recovered tool context"}}]}`))
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	httpServer := httptestServer(t, srv)
	sessionID := "tool-context-session"
	postToolInput(t, httpServer.URL, "session_store", map[string]any{
		"sessionId": sessionID,
		"title":     "Tool Context Session",
	})
	postToolInput(t, httpServer.URL, "session_append", map[string]any{
		"sessionId":       sessionID,
		"role":            "user",
		"message":         map[string]any{"type": "message", "text": "create a task"},
		"clientMessageId": "tool-context-user-1",
	})
	if _, err := srv.eventJournal.Append(sessionID, eventjournal.Message{
		"type":       "runner_checkpoint",
		"role":       "system",
		"status":     "completed",
		"text":       "tool task_create completed",
		"toolName":   "task_create",
		"toolCallId": "call_task_1",
		"toolInput":  map[string]any{"title": "tool-created task"},
		"toolResult": map[string]any{"ok": true, "task": map[string]any{"title": "tool-created task"}},
	}, eventjournal.Metadata{}); err != nil {
		t.Fatalf("append tool checkpoint: %v", err)
	}
	postToolInput(t, httpServer.URL, "session_append", map[string]any{
		"sessionId":       sessionID,
		"role":            "user",
		"message":         map[string]any{"type": "message", "text": "continue after tool"},
		"clientMessageId": "tool-context-user-2",
	})
	session, ok, err := srv.sessionStore.Get(sessionID)
	if err != nil || !ok {
		t.Fatalf("session get ok=%v err=%v", ok, err)
	}
	entries, err := srv.eventJournal.ReadAfter(sessionID, 0, 20)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	result, err := srv.runSessionRunnerChat(context.Background(), SessionRunnerChatOptions{RunnerID: "tool-context-runner",
		Endpoint:         modelAPI.URL + "/v1/chat/completions",
		Model:            "test-model",
		MaxToolRounds:    1,
		MaxAttempts:      1,
		ReplayLimit:      20,
		OutputLimitBytes: 64 * 1024,
	}, session, entries, nil)
	if err != nil {
		t.Fatalf("runSessionRunnerChat() error = %v", err)
	}
	if result != "continued with recovered tool context" || requests.Load() != 1 {
		t.Fatalf("result=%q requests=%d", result, requests.Load())
	}
}

func TestSessionEntriesRestoreBoundedCorrectionsAcrossAttempts(t *testing.T) {
	for _, test := range []struct {
		name       string
		reasonCode string
		detail     string
	}{
		{name: "artifact", reasonCode: "artifact_reference_correction_required", detail: "unsupported identifiers doi:10.1000/example"},
		{name: "malformed artifact link", reasonCode: "artifact_reference_correction_required", detail: "malformed artifact references malformed_placeholder@byte_1280"},
		{name: "unresolved artifact link", reasonCode: "artifact_reference_correction_required", detail: "runner completion reference integrity failed (unresolved_artifacts=3 malformed_artifact_references=0 unsupported_citations=0)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			entries := []eventjournal.Entry{
				{EventID: 1, Message: eventjournal.Message{
					"type": "message", "role": "user", "content": "Complete the original research task.",
				}},
				{EventID: 2, Message: eventjournal.Message{
					"type": "runner_checkpoint", "status": "interrupted",
					"reason_code": test.reasonCode, "resume_detail": test.detail,
				}},
			}
			messages := sessionEntriesToChatMessages("system prompt", entries)
			if len(messages) != 3 || messages[1].Role != "system" ||
				!strings.Contains(messages[1].Content, "same logical task") ||
				!strings.Contains(messages[1].Content, "synon.runner_recovery.v1") ||
				!strings.Contains(messages[1].Content, `"required_transition":"repair_current_candidate"`) ||
				!strings.Contains(messages[1].Content, `"preserve_completed_state":true`) ||
				!strings.Contains(messages[1].Content, test.reasonCode) ||
				!strings.Contains(messages[1].Content, test.detail) ||
				messages[2].Role != "user" {
				t.Fatalf("chat messages=%#v", messages)
			}
			if (test.name == "malformed artifact link" || test.name == "unresolved artifact link") &&
				(!strings.Contains(messages[1].Content, `"reject_unchanged_repeat":true`) ||
					!strings.Contains(messages[1].Content, `"revalidate_after_state_change":true`)) {
				t.Fatalf("artifact correction lacks the generic convergence contract: %#v", messages)
			}
			if test.name == "material decision input fidelity" &&
				(!strings.Contains(messages[1].Content, "Treat every absent material input as unknown") ||
					!strings.Contains(messages[1].Content, "do not ask the user to approve")) {
				t.Fatalf("material decision correction lacks a changed-output repair contract: %#v", messages)
			}

			entries = append(entries, eventjournal.Entry{EventID: 3, Message: eventjournal.Message{
				"type": "message", "role": "user", "content": "Start an unrelated new task.",
			}})
			messages = sessionEntriesToChatMessages("system prompt", entries)
			for _, message := range messages {
				if strings.Contains(message.Content, test.detail) || strings.Contains(message.Content, test.reasonCode) {
					t.Fatalf("stale correction crossed a new user boundary: %#v", messages)
				}
			}
		})
	}
}

func TestSessionTranscriptPreservesMultimodalContentBlocks(t *testing.T) {
	entries := []eventjournal.Entry{
		{
			SessionID: "multimodal-session",
			EventID:   1,
			CreatedAt: "2026-07-08T19:30:00Z",
			Message: eventjournal.Message{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": "inspect the attached screenshot"},
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": "file:///tmp/screen.png"}},
					map[string]any{"type": "file", "file_name": "notes.pdf"},
				},
			},
		},
	}

	messages := sessionEntriesToChatMessages("system prompt", entries)
	if len(messages) != 2 || messages[1].Role != "user" {
		t.Fatalf("chat messages = %#v", messages)
	}
	if !strings.Contains(messages[1].Content, "inspect the attached screenshot") ||
		!strings.Contains(messages[1].Content, "[image: file:///tmp/screen.png]") ||
		!strings.Contains(messages[1].Content, "[file: notes.pdf]") {
		t.Fatalf("multimodal chat content = %q", messages[1].Content)
	}

	summary, err := buildCompactModelContextSummary(sessionstore.Session{ID: "multimodal-session"}, entries, "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "inspect the attached screenshot") ||
		!strings.Contains(summary, "[image: file:///tmp/screen.png]") ||
		!strings.Contains(summary, "[file: notes.pdf]") {
		t.Fatalf("compact summary = %q", summary)
	}

	session := sessionstore.Session{ID: "multimodal-session", Title: "Multimodal Session"}
	markdown := formatSessionTranscriptMarkdown(session, entries, false)
	textTranscript := formatSessionTranscriptText(session, entries, false)
	for _, transcript := range []string{markdown, textTranscript} {
		if !strings.Contains(transcript, "inspect the attached screenshot") ||
			!strings.Contains(transcript, "[image: file:///tmp/screen.png]") ||
			!strings.Contains(transcript, "[file: notes.pdf]") {
			t.Fatalf("multimodal transcript = %q", transcript)
		}
	}
}

func TestSessionRunnerChatToolAllowlistBlocksUnlistedToolCalls(t *testing.T) {
	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sequence := requests.Add(1)
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode model request %d: %v", sequence, err)
		}
		switch sequence {
		case 1:
			tools, ok := request["tools"].([]any)
			if !ok || !hasChatToolNamed(tools, "search_skills") {
				t.Fatalf("first request missing allowed search_skills tool: %#v", request["tools"])
			}
			if hasChatToolNamed(tools, "edit_file") {
				t.Fatalf("edit_file should not be advertised when chat tools are restricted: %#v", request["tools"])
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"choices": [{
					"message": {
						"role": "assistant",
						"tool_calls": [{
							"id": "call_edit_1",
							"type": "function",
							"function": {
								"name": "edit_file",
								"arguments": "{\"file_path\":\"blocked.txt\",\"old_string\":\"before\",\"new_string\":\"after\"}"
							}
						}]
					}
				}]
			}`))
		case 2:
			messages, ok := request["messages"].([]any)
			if !ok || !hasToolResultMessage(messages, "call_edit_1", "unadvertised_tool") {
				t.Fatalf("second request missing durable tool rejection: %#v", request["messages"])
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"choices": [{
					"message": {
						"role": "assistant",
						"content": "blocked unsafe tool"
					}
				}]
			}`))
		default:
			t.Fatalf("unexpected model request %d", sequence)
		}
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	allowPairingForTest(t, srv, "feishu", "ou_chat_allowlist_runner")
	httpServer := httptestServer(t, srv)

	first := postFeishuEvent(t, httpServer.URL, []byte(`{
		"schema": "2.0",
		"header": {"event_id": "evt-chat-allowlist-runner-1", "event_type": "im.message.receive_v1"},
		"event": {
			"sender": {"sender_id": {"open_id": "ou_chat_allowlist_runner"}},
			"message": {
				"message_id": "om_chat_allowlist_runner_1",
				"chat_id": "oc_chat_allowlist_runner",
				"chat_type": "p2p",
				"message_type": "text",
				"content": "{\"text\":\"try a restricted tool\"}"
			}
		}
	}`))
	sessionID := first["sessionId"].(string)

	result, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{RunnerID: "chat-allowlist-runner-a",
		Endpoint:         modelAPI.URL + "/v1/chat/completions",
		Model:            "test-model",
		AllowedTools:     []string{"search_skills"},
		LeaseTTL:         time.Minute,
		ReplayLimit:      20,
		OutputLimitBytes: 64 * 1024,
	})
	if err != nil {
		t.Fatalf("RunSessionRunnerChatOnce() error = %v", err)
	}
	if !result.Claimed || result.SessionID != sessionID || result.Status != "completed" || result.AssistantEventID == 0 || result.FinishEventID == 0 {
		t.Fatalf("runner result = %+v", result)
	}
	if requests.Load() != 2 {
		t.Fatalf("model requests = %d", requests.Load())
	}
	if _, err := os.Stat(filepath.Join(root, "blocked.txt")); !os.IsNotExist(err) {
		t.Fatalf("blocked file should not exist, stat err=%v", err)
	}

	replayed := postToolInput(t, httpServer.URL, "session_replay", map[string]any{
		"sessionId":    sessionID,
		"afterEventId": float64(0),
		"limit":        float64(10),
	})
	entries := replayed["result"].(map[string]any)["entries"].([]any)
	if !hasJournalEntry(entries, "message", "", "blocked unsafe tool") {
		t.Fatalf("missing final assistant message: %#v", entries)
	}
}

func TestSessionRunnerChatEnforcesReadOnlyAgentToolPolicy(t *testing.T) {
	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sequence := requests.Add(1)
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode model request %d: %v", sequence, err)
		}
		switch sequence {
		case 1:
			tools, ok := request["tools"].([]any)
			if !ok || !hasChatToolNamed(tools, "search_skills") {
				t.Fatalf("first request missing read-only search_skills tool: %#v", request["tools"])
			}
			for _, blocked := range []string{"edit_file", "file_write", "Write", "Edit", "Patch", "TaskCreate", "Agent", "Config"} {
				if hasChatToolNamed(tools, blocked) {
					t.Fatalf("%s should not be advertised to read_only Agent: %#v", blocked, request["tools"])
				}
			}
			messages := request["messages"].([]any)
			if !hasChatSystemMessage(messages, "Agent tool policy: read_only", "must not edit files") {
				t.Fatalf("read_only policy context missing from model request: %#v", messages)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"choices": [{
					"message": {
						"role": "assistant",
						"tool_calls": [{
							"id": "call_readonly_edit_file",
							"type": "function",
							"function": {
								"name": "edit_file",
								"arguments": "{\"file_path\":\"read-only-agent-should-not-write.txt\",\"old_string\":\"before\",\"new_string\":\"after\"}"
							}
						}]
					}
				}]
			}`))
		case 2:
			messages, ok := request["messages"].([]any)
			if !ok || !hasToolResultMessage(messages, "call_readonly_edit_file", "unadvertised_tool") {
				t.Fatalf("second request missing durable read-only rejection: %#v", request["messages"])
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"read-only agent refused mutation"}}]}`))
		default:
			t.Fatalf("unexpected model request %d", sequence)
		}
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	httpServer := httptestServer(t, srv)
	launched := postToolInput(t, httpServer.URL, "Agent", map[string]any{
		"prompt":            "Inspect the repo without mutating files.",
		"description":       "Read-only delegated inspection",
		"name":              "readonly-agent",
		"tool_policy":       "read_only",
		"run_in_background": true,
	})
	sessionID := launched["result"].(map[string]any)["sessionId"].(string)

	result, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{RunnerID: "readonly-agent-runner",
		Endpoint:         modelAPI.URL + "/v1/chat/completions",
		Model:            "test-model",
		AllowedTools:     []string{"search_skills", "edit_file", "Agent"},
		LeaseTTL:         time.Minute,
		ReplayLimit:      20,
		OutputLimitBytes: 64 * 1024,
		MaxToolRounds:    2,
		MaxAttempts:      1,
	})
	if err != nil {
		t.Fatalf("RunSessionRunnerChatOnce() error = %v", err)
	}
	if !result.Claimed || result.SessionID != sessionID || result.Status != "completed" || result.AssistantEventID == 0 {
		t.Fatalf("runner result = %+v", result)
	}
	if requests.Load() != 2 {
		t.Fatalf("model requests = %d", requests.Load())
	}
	if _, err := os.Stat(filepath.Join(root, "read-only-agent-should-not-write.txt")); !os.IsNotExist(err) {
		t.Fatalf("read_only Agent should not write file, stat err=%v", err)
	}
}

func TestReadOnlyAgentAllowedToolsEmptyIntersectionDeniesAllTools(t *testing.T) {
	allowed := readOnlyAgentAllowedTools([]string{"file_write", "Agent"})
	if len(allowed) != 1 || allowed[0] != noChatToolsAllowedSentinel {
		t.Fatalf("empty read_only intersection should use deny-all sentinel, got %#v", allowed)
	}
	if chatRunnerToolAllowed("file_write", chatRunnerAllowedToolSet(allowed)) {
		t.Fatalf("deny-all sentinel should not allow file_write")
	}
	if chatRunnerToolAllowed("Read", chatRunnerAllowedToolSet(allowed)) {
		t.Fatalf("deny-all sentinel should not allow Read without parent permission")
	}
}

func TestInvalidAskUserAllowlistFailsClosed(t *testing.T) {
	for _, invalid := range []string{" ASK_USER", "ASK_USER", "\ufeffask_user"} {
		options := normalizeSessionRunnerChatOptions(SessionRunnerChatOptions{AllowedTools: []string{invalid}})
		if len(options.AllowedTools) != 1 || options.AllowedTools[0] != noChatToolsAllowedSentinel {
			t.Fatalf("invalid allowlist %q normalized to %#v", invalid, options.AllowedTools)
		}
		allowed := chatRunnerAllowedToolSet(options.AllowedTools)
		if chatRunnerToolAllowed("Read", allowed) || chatRunnerToolAllowed("ask_user", allowed) {
			t.Fatalf("invalid allowlist %q broadened tool access: %#v", invalid, allowed)
		}
	}
}

func TestSessionRunnerChatEnforcesRestrictedAgentToolPolicy(t *testing.T) {
	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		tools, ok := request["tools"].([]any)
		if !ok || !hasChatToolNamed(tools, "search_skills") || !hasChatToolNamed(tools, "ask_user") {
			t.Fatalf("restricted Agent missing low-risk tools: %#v", request["tools"])
		}
		if hasChatToolNamed(tools, "AskUserQuestion") || hasChatToolNamed(tools, "ask_user_question") {
			t.Fatalf("restricted Agent advertised legacy ask-user aliases: %#v", request["tools"])
		}
		for _, blocked := range []string{"edit_file", "file_write", "Write", "Bash", "Shell", "Agent", "task_create", "TaskCreate", "runtime_set", "settings_set", "MCPTool"} {
			if hasChatToolNamed(tools, blocked) {
				t.Fatalf("%s should not be advertised to restricted Agent: %#v", blocked, request["tools"])
			}
		}
		messages := request["messages"].([]any)
		if !hasChatSystemMessage(messages, "Agent tool policy: restricted", "parent runner tool boundary") {
			t.Fatalf("restricted policy context missing from model request: %#v", messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"restricted policy honored"}}]}`))
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	httpServer := httptestServer(t, srv)
	launched := postToolInput(t, httpServer.URL, "Agent", map[string]any{
		"prompt":            "Use only restricted tools.",
		"description":       "Restricted delegated inspection",
		"name":              "restricted-agent",
		"tool_policy":       "safe",
		"run_in_background": true,
	})
	sessionID := launched["result"].(map[string]any)["sessionId"].(string)
	if launched["result"].(map[string]any)["tool_policy"] != "restricted" {
		t.Fatalf("Agent policy alias was not normalized: %#v", launched)
	}

	result, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{RunnerID: "restricted-agent-runner",
		Endpoint:         modelAPI.URL + "/v1/chat/completions",
		Model:            "test-model",
		AllowedTools:     []string{"search_skills", "ask_user", "edit_file", "Agent"},
		LeaseTTL:         time.Minute,
		ReplayLimit:      20,
		OutputLimitBytes: 64 * 1024,
		MaxToolRounds:    1,
		MaxAttempts:      1,
	})
	if err != nil {
		t.Fatalf("RunSessionRunnerChatOnce() error = %v", err)
	}
	if !result.Claimed || result.SessionID != sessionID || result.Status != "completed" {
		t.Fatalf("runner result = %+v", result)
	}
	if requests.Load() != 1 {
		t.Fatalf("model requests = %d", requests.Load())
	}
}

func TestSessionRunnerChatFullAccessDelegatedSessionUsesExactParentSnapshot(t *testing.T) {
	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		tools, ok := request["tools"].([]any)
		if !ok || !hasChatToolNamed(tools, "search_skills") {
			t.Fatalf("full_access delegated session should inherit the canonical parent allowance: %#v", request["tools"])
		}
		if hasChatToolNamed(tools, "Agent") || hasChatToolNamed(tools, "runtime_set") {
			t.Fatalf("full_access Agent expanded beyond parent allowed tools: %#v", request["tools"])
		}
		messages := request["messages"].([]any)
		if !hasChatSystemMessage(messages, "Agent tool policy: full_access", "approval, hook, and sandbox gates") {
			t.Fatalf("full_access policy context missing from model request: %#v", messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"full access inherited parent"}}]}`))
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	httpServer := httptestServer(t, srv)
	launched := postToolInput(t, httpServer.URL, "Agent", map[string]any{
		"prompt":            "Use inherited full access.",
		"description":       "Full access delegated inspection",
		"name":              "full-access-agent",
		"tool_policy":       "full",
		"run_in_background": true,
	})
	sessionID := launched["result"].(map[string]any)["sessionId"].(string)
	if launched["result"].(map[string]any)["tool_policy"] != "full_access" {
		t.Fatalf("Agent full policy alias was not normalized: %#v", launched)
	}

	result, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{RunnerID: "full-access-agent-runner",
		Endpoint:         modelAPI.URL + "/v1/chat/completions",
		Model:            "test-model",
		AllowedTools:     []string{"search_skills", "Agent"},
		LeaseTTL:         time.Minute,
		ReplayLimit:      20,
		OutputLimitBytes: 64 * 1024,
		MaxToolRounds:    1,
		MaxAttempts:      1,
	})
	if err != nil {
		t.Fatalf("RunSessionRunnerChatOnce() error = %v", err)
	}
	if !result.Claimed || result.SessionID != sessionID || result.Status != "completed" {
		t.Fatalf("runner result = %+v", result)
	}
	if requests.Load() != 1 {
		t.Fatalf("model requests = %d", requests.Load())
	}
}

func TestSessionRunnerChatOnceTimesOutSlowModelEndpoint(t *testing.T) {
	requestStarted := make(chan struct{}, 1)
	requestCancelled := make(chan error, 1)
	releaseHandler := make(chan struct{})
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain the request so the real HTTP server can observe a disconnected
		// client while the handler deliberately withholds its response.
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Errorf("read model request: %v", err)
			return
		}
		select {
		case requestStarted <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
			select {
			case requestCancelled <- r.Context().Err():
			default:
			}
		case <-releaseHandler:
		}
	}))
	defer modelAPI.Close()
	defer close(releaseHandler)

	root := t.TempDir()
	srv := New(Options{FileRoot: root, HTTPClient: modelAPI.Client()})
	allowPairingForTest(t, srv, "feishu", "ou_chat_timeout_runner")
	httpServer := httptestServer(t, srv)

	first := postFeishuEvent(t, httpServer.URL, []byte(`{
		"schema": "2.0",
		"header": {"event_id": "evt-chat-timeout-runner-1", "event_type": "im.message.receive_v1"},
		"event": {
			"sender": {"sender_id": {"open_id": "ou_chat_timeout_runner"}},
			"message": {
				"message_id": "om_chat_timeout_runner_1",
				"chat_id": "oc_chat_timeout_runner",
				"chat_type": "p2p",
				"message_type": "text",
				"content": "{\"text\":\"slow model should timeout\"}"
			}
		}
	}`))
	sessionID := first["sessionId"].(string)

	// This deadline only bounds a broken test. RequestTimeout must cancel the
	// HTTP call while this parent context is still live; runner bookkeeping is
	// not part of the model request's 40 ms budget.
	watchdog, cancelWatchdog := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelWatchdog()
	result, err := srv.RunSessionRunnerChatOnce(watchdog, SessionRunnerChatOptions{RunnerID: "chat-timeout-runner-a",
		Endpoint:         modelAPI.URL + "/v1/chat/completions",
		Model:            "test-model",
		RequestTimeout:   40 * time.Millisecond,
		LeaseTTL:         time.Minute,
		ReplayLimit:      20,
		OutputLimitBytes: 64 * 1024,
	})
	if err := watchdog.Err(); err != nil {
		t.Fatalf("outer watchdog ended the runner instead of its request timeout: %v", err)
	}
	if err != nil {
		t.Fatalf("RunSessionRunnerChatOnce() error = %v", err)
	}
	if !result.Claimed || result.SessionID != sessionID || result.Status != "failed" || result.FinishEventID == 0 {
		t.Fatalf("runner result = %+v", result)
	}
	select {
	case <-requestStarted:
	case <-watchdog.Done():
		t.Fatal("model endpoint never received the request")
	}
	select {
	case err := <-requestCancelled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("model request context error = %v, want client cancellation", err)
		}
	case <-watchdog.Done():
		t.Fatal("model request timeout did not cancel the server-side request")
	}
	if err := watchdog.Err(); err != nil {
		t.Fatalf("request cancellation was only observed after the outer watchdog: %v", err)
	}

	replayed := postToolInput(t, httpServer.URL, "session_replay", map[string]any{
		"sessionId":    sessionID,
		"afterEventId": float64(0),
		"limit":        float64(10),
	})
	entries := replayed["result"].(map[string]any)["entries"].([]any)
	if !hasJournalEntry(entries, "runner_finished", "failed", "This run did not finish completely") {
		t.Fatalf("missing user-safe failed timeout finish entry: %#v", entries)
	}
	if hasJournalEntry(entries, "runner_finished", "failed", "context deadline") {
		t.Fatalf("provider timeout detail leaked into the public finish entry: %#v", entries)
	}
}

func TestSessionRunnerChatOnceInterruptsWhenToolRoundLimitExceeded(t *testing.T) {
	t.Setenv("SYNON_WEBSEARCH_MODE", "disabled")
	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sequence := requests.Add(1)
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode model request %d: %v", sequence, err)
		}
		switch sequence {
		case 1:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"choices": [{
					"message": {
						"role": "assistant",
						"tool_calls": [{
							"id": "call_web_search_1",
							"type": "function",
							"function": {
								"name": "web_search",
								"arguments": "{\"query\":\"round-limit first\"}"
							}
						}]
					}
				}]
			}`))
		case 2:
			messages, ok := request["messages"].([]any)
			if !ok || !hasToolResultMessage(messages, "call_web_search_1", "disabled") {
				t.Fatalf("second request missing first tool result: %#v", request["messages"])
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"choices": [{
					"message": {
						"role": "assistant",
						"tool_calls": [{
							"id": "call_web_search_2",
							"type": "function",
							"function": {
								"name": "web_search",
								"arguments": "{\"query\":\"round-limit second\"}"
							}
						}]
					}
				}]
			}`))
		default:
			t.Fatalf("unexpected model request %d", sequence)
		}
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	allowPairingForTest(t, srv, "feishu", "ou_chat_round_limit_runner")
	httpServer := httptestServer(t, srv)

	first := postFeishuEvent(t, httpServer.URL, []byte(`{
		"schema": "2.0",
		"header": {"event_id": "evt-chat-round-limit-runner-1", "event_type": "im.message.receive_v1"},
		"event": {
			"sender": {"sender_id": {"open_id": "ou_chat_round_limit_runner"}},
			"message": {
				"message_id": "om_chat_round_limit_runner_1",
				"chat_id": "oc_chat_round_limit_runner",
				"chat_type": "p2p",
				"message_type": "text",
				"content": "{\"text\":\"keep calling tools\"}"
			}
		}
	}`))
	sessionID := first["sessionId"].(string)

	result, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{RunnerID: "chat-round-limit-runner-a",
		Endpoint:         modelAPI.URL + "/v1/chat/completions",
		Model:            "test-model",
		AllowedTools:     []string{"web_search"},
		MaxToolRounds:    1,
		LeaseTTL:         time.Minute,
		ReplayLimit:      20,
		OutputLimitBytes: 64 * 1024,
	})
	if err != nil {
		t.Fatalf("RunSessionRunnerChatOnce() error = %v", err)
	}
	if !result.Claimed || result.SessionID != sessionID || result.Status != "interrupted" ||
		result.InterruptionReasonCode != sessionRunnerToolRoundLimitReasonCode ||
		!result.InterruptionAutoResume || result.FinishEventID != 0 {
		t.Fatalf("runner result = %+v", result)
	}
	if requests.Load() != 2 {
		t.Fatalf("model requests = %d", requests.Load())
	}
	replayed := postToolInput(t, httpServer.URL, "session_replay", map[string]any{
		"sessionId":    sessionID,
		"afterEventId": float64(0),
		"limit":        float64(10),
	})
	entries := replayed["result"].(map[string]any)["entries"].([]any)
	if hasJournalEntry(entries, "runner_finished", "failed", "exceeded 1 tool call rounds") {
		t.Fatalf("bounded round-limit interruption must not publish a terminal failed finish entry: %#v", entries)
	}
	if !hasJournalEntry(entries, "runner_checkpoint", "running", "runner interrupted; resumable lease released") {
		t.Fatalf("missing resumable round-limit checkpoint: %#v", entries)
	}
}

func TestSessionRunnerChatStopsBeforeToolExecutionWhenDurableCheckpointLosesLease(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	allowPairingForTest(t, srv, "feishu", "ou_chat_checkpoint_fence")
	httpServer := httptestServer(t, srv)
	first := postFeishuEvent(t, httpServer.URL, []byte(`{
		"schema": "2.0",
		"header": {"event_id": "evt-chat-checkpoint-fence-1", "event_type": "im.message.receive_v1"},
		"event": {
			"sender": {"sender_id": {"open_id": "ou_chat_checkpoint_fence"}},
			"message": {
				"message_id": "om_chat_checkpoint_fence_1",
				"chat_id": "oc_chat_checkpoint_fence",
				"chat_type": "p2p",
				"message_type": "text",
				"content": "{\"text\":\"write only while the durable lease is owned\"}"
			}
		}
	}`))
	sessionID := first["sessionId"].(string)

	takeoverResult := make(chan error, 1)
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		owned, found, err := srv.sessionStore.Get(sessionID)
		if err != nil || !found {
			takeoverResult <- fmt.Errorf("load owned runner: found=%t: %w", found, err)
			http.Error(w, "load failed", http.StatusInternalServerError)
			return
		}
		if _, released, err := srv.sessionStore.ReleaseRunner(sessionstore.RunnerClaimFromSession(owned)); err != nil {
			takeoverResult <- err
			http.Error(w, "release failed", http.StatusInternalServerError)
			return
		} else if !released {
			takeoverResult <- errors.New("original runner lease was not released")
			http.Error(w, "release conflict", http.StatusConflict)
			return
		}
		if _, claimed, err := srv.sessionStore.ClaimRunner(sessionID, "checkpoint-fence-runner-b", time.Minute); err != nil {
			takeoverResult <- err
			http.Error(w, "claim failed", http.StatusInternalServerError)
			return
		} else if !claimed {
			takeoverResult <- errors.New("replacement runner did not claim the released lease")
			http.Error(w, "claim conflict", http.StatusConflict)
			return
		}
		takeoverResult <- nil
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices": [{
				"message": {
					"role": "assistant",
					"tool_calls": [{
						"id": "call_checkpoint_fenced_write",
						"type": "function",
						"function": {
							"name": "file_write",
							"arguments": "{\"path\":\"checkpoint-fence-should-not-write.txt\",\"content\":\"unsafe\"}"
						}
					}]
				}
			}]
		}`))
	}))
	defer modelAPI.Close()

	result, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{RunnerID: "checkpoint-fence-runner-a",
		Endpoint:         modelAPI.URL + "/v1/chat/completions",
		Model:            "test-model",
		AllowedTools:     []string{"file_write"},
		LeaseTTL:         time.Minute,
		ReplayLimit:      20,
		OutputLimitBytes: 64 * 1024,
	})
	if err == nil {
		t.Fatalf("RunSessionRunnerChatOnce() result = %+v, want lost-lease error", result)
	}
	if takeoverErr := <-takeoverResult; takeoverErr != nil {
		t.Fatalf("lease takeover failed: %v", takeoverErr)
	}
	if _, statErr := os.Stat(filepath.Join(root, "checkpoint-fence-should-not-write.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("tool side effect occurred after durable checkpoint lost ownership: %v", statErr)
	}
	owned, found, getErr := srv.sessionStore.Get(sessionID)
	if getErr != nil || !found || owned.Runner == nil || owned.Runner.RunnerID != "checkpoint-fence-runner-b" {
		t.Fatalf("replacement lease was not preserved: session=%#v found=%v error=%v", owned, found, getErr)
	}
}

func TestSessionRunnerChatOnceRenewsLeaseDuringLongModelTurn(t *testing.T) {
	requestStarted := make(chan struct{}, 2)
	releaseRequest := make(chan struct{})
	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		requestStarted <- struct{}{}
		<-releaseRequest
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices": [{"message": {"role": "assistant", "content": "long turn completed"}}]
		}`))
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	allowPairingForTest(t, srv, "feishu", "ou_chat_heartbeat_runner")
	httpServer := httptestServer(t, srv)
	first := postFeishuEvent(t, httpServer.URL, []byte(`{
		"schema": "2.0",
		"header": {"event_id": "evt-chat-heartbeat-runner-1", "event_type": "im.message.receive_v1"},
		"event": {
			"sender": {"sender_id": {"open_id": "ou_chat_heartbeat_runner"}},
			"message": {
				"message_id": "om_chat_heartbeat_runner_1",
				"chat_id": "oc_chat_heartbeat_runner",
				"chat_type": "p2p",
				"message_type": "text",
				"content": "{\"text\":\"perform a long model turn\"}"
			}
		}
	}`))
	sessionID := first["sessionId"].(string)

	type runOutcome struct {
		result SessionRunnerCycleResult
		err    error
	}
	done := make(chan runOutcome, 1)
	go func() {
		result, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{RunnerID: "chat-heartbeat-runner-a", Endpoint: modelAPI.URL + "/v1/chat/completions", Model: "test-model",
			LeaseTTL: 80 * time.Millisecond, ReplayLimit: 20, OutputLimitBytes: 64 * 1024,
		})
		done <- runOutcome{result: result, err: err}
	}()
	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("model request did not start")
	}
	firstLease, found, err := srv.sessionStore.Get(sessionID)
	if err != nil || !found || firstLease.Runner == nil {
		t.Fatalf("first lease=%#v found=%t err=%v", firstLease, found, err)
	}
	firstClaim := sessionstore.RunnerClaimFromSession(firstLease)
	duplicate, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{SessionID: sessionID, RunnerID: "chat-heartbeat-runner-a",
		Endpoint: modelAPI.URL + "/v1/chat/completions", Model: "test-model",
		LeaseTTL: 80 * time.Millisecond, ReplayLimit: 20, OutputLimitBytes: 64 * 1024,
	})
	if err != nil || duplicate.Claimed {
		t.Fatalf("duplicate same-runner result=%+v err=%v", duplicate, err)
	}
	if requests.Load() != 1 {
		t.Fatalf("duplicate same-runner started another model request: %d", requests.Load())
	}
	if _, err := srv.sessionStore.ValidateRunnerClaim(firstClaim, true); err != nil {
		t.Fatalf("duplicate same-runner invalidated the first lease: %v", err)
	}
	time.Sleep(140 * time.Millisecond)
	_, claimed, err := srv.sessionStore.ClaimRunner(sessionID, "chat-heartbeat-runner-b", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Fatal("second runner reclaimed a live long-running chat lease")
	}
	close(releaseRequest)
	select {
	case outcome := <-done:
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		if !outcome.result.Claimed || outcome.result.Status != "completed" {
			t.Fatalf("result = %+v", outcome.result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("long model turn did not finish")
	}
}

func TestSessionRunnerChatOnceRetriesTransientModelFailure(t *testing.T) {
	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sequence := requests.Add(1)
		if sequence == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"temporary outage"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices": [{
				"message": {
					"role": "assistant",
					"content": "recovered after retry"
				}
			}]
		}`))
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	allowPairingForTest(t, srv, "feishu", "ou_chat_retry_runner")
	httpServer := httptestServer(t, srv)

	first := postFeishuEvent(t, httpServer.URL, []byte(`{
		"schema": "2.0",
		"header": {"event_id": "evt-chat-retry-runner-1", "event_type": "im.message.receive_v1"},
		"event": {
			"sender": {"sender_id": {"open_id": "ou_chat_retry_runner"}},
			"message": {
				"message_id": "om_chat_retry_runner_1",
				"chat_id": "oc_chat_retry_runner",
				"chat_type": "p2p",
				"message_type": "text",
				"content": "{\"text\":\"retry transient model failure\"}"
			}
		}
	}`))
	sessionID := first["sessionId"].(string)

	result, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{RunnerID: "chat-retry-runner-a",
		Endpoint:         modelAPI.URL + "/v1/chat/completions",
		Model:            "test-model",
		MaxAttempts:      2,
		LeaseTTL:         time.Minute,
		ReplayLimit:      20,
		OutputLimitBytes: 64 * 1024,
	})
	if err != nil {
		t.Fatalf("RunSessionRunnerChatOnce() error = %v", err)
	}
	if !result.Claimed || result.SessionID != sessionID || result.Status != "completed" || result.AssistantEventID == 0 || result.FinishEventID == 0 {
		t.Fatalf("runner result = %+v", result)
	}
	if requests.Load() != 2 {
		t.Fatalf("model requests = %d", requests.Load())
	}

	replayed := postToolInput(t, httpServer.URL, "session_replay", map[string]any{
		"sessionId":    sessionID,
		"afterEventId": float64(0),
		"limit":        float64(10),
	})
	entries := replayed["result"].(map[string]any)["entries"].([]any)
	if !hasJournalEntry(entries, "message", "", "recovered after retry") {
		t.Fatalf("missing retried assistant message: %#v", entries)
	}
}

func hasChatToolNamed(tools []any, name string) bool {
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok || tool["type"] != "function" {
			continue
		}
		function, ok := tool["function"].(map[string]any)
		if !ok {
			continue
		}
		if function["name"] == name {
			return true
		}
	}
	return false
}

func hasToolResultMessage(messages []any, toolCallID string, textContains string) bool {
	for _, rawMessage := range messages {
		message, ok := rawMessage.(map[string]any)
		if !ok || message["role"] != "tool" || message["tool_call_id"] != toolCallID {
			continue
		}
		content, _ := message["content"].(string)
		if strings.Contains(content, textContains) {
			return true
		}
	}
	return false
}

func hasRunnerToolCheckpoint(entries []any, status string, toolName string, toolCallID string, textContains string) bool {
	for _, rawEntry := range entries {
		entry, ok := rawEntry.(map[string]any)
		if !ok {
			continue
		}
		message, ok := entry["message"].(map[string]any)
		if !ok || message["type"] != "runner_checkpoint" || message["status"] != status {
			continue
		}
		if message["toolName"] != toolName || message["toolCallId"] != toolCallID {
			continue
		}
		if textContains == "" {
			return true
		}
		encoded, err := json.Marshal(message)
		if err != nil {
			continue
		}
		if strings.Contains(string(encoded), textContains) {
			return true
		}
	}
	return false
}

func hasModelToolCallCheckpoint(entries []any, toolName string, toolCallID string, textContains string) bool {
	for _, rawEntry := range entries {
		entry, ok := rawEntry.(map[string]any)
		if !ok {
			continue
		}
		message, ok := entry["message"].(map[string]any)
		if !ok || message["type"] != "runner_checkpoint" {
			continue
		}
		calls, ok := message["modelToolCalls"].([]any)
		if !ok {
			continue
		}
		for _, rawCall := range calls {
			call, ok := rawCall.(map[string]any)
			if !ok || call["name"] != toolName || call["id"] != toolCallID {
				continue
			}
			encoded, err := json.Marshal(call)
			if err != nil {
				continue
			}
			if textContains == "" || strings.Contains(string(encoded), textContains) {
				return true
			}
		}
	}
	return false
}

func hasChatSystemMessage(messages []any, required ...string) bool {
	for _, rawMessage := range messages {
		message, ok := rawMessage.(map[string]any)
		if !ok || message["role"] != "system" {
			continue
		}
		content, _ := message["content"].(string)
		matched := true
		for _, text := range required {
			if !strings.Contains(content, text) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func journalEntryMessage(entry any) map[string]any {
	item, ok := entry.(map[string]any)
	if !ok {
		return nil
	}
	message, _ := item["message"].(map[string]any)
	return message
}

func hasTaskTitle(tasks []any, title string) bool {
	for _, rawTask := range tasks {
		task, ok := rawTask.(map[string]any)
		if !ok {
			continue
		}
		if task["title"] == title {
			return true
		}
	}
	return false
}

func TestSessionRunnerFailureReasonCodeClassifiesResumableFailures(t *testing.T) {
	tests := map[string]struct {
		message string
		want    string
	}{
		"reference integrity": {
			message: "runner completion reference integrity failed (unresolved_artifacts=0 unsupported_citations=2 invalid_reference_artifacts=0 invalid_scientific_artifacts=0 invalid_research_artifacts=0): unsupported identifiers doi:10.9999/x",
			want:    "artifact_reference_correction_required",
		},
		"provider stream interrupted": {
			message: "provider stream interrupted mid-response",
			want:    "provider_stream_interrupted",
		},
		"ark max message tokens": {
			message: `provider endpoint returned 400: {"error":{"code":"InvalidParameter","message":"Total tokens of image and text exceed max message tokens."}}`,
			want:    sessionRunnerProviderContextPressureReasonCode,
		},
		"openai context length": {
			message: `provider endpoint returned 400: {"error":{"code":"context_length_exceeded","message":"This model's maximum context length is 131072 tokens"}}`,
			want:    sessionRunnerProviderContextPressureReasonCode,
		},
		"tool result reported failure": {
			message: "tool result reported failure: exit=1",
			want:    sessionRunnerToolFailedReasonCode,
		},
		"tool prefixed failed": {
			message: "tool WebFetch failed: timeout",
			want:    sessionRunnerToolFailedReasonCode,
		},
		"transient sqlite contention": {
			message: "start completion reviewer frame: insert frame: database is locked (5) (SQLITE_BUSY)",
			want:    sessionRunnerStoreContentionReasonCode,
		},
		"provider http failure": {
			message: "model provider returned HTTP 500",
			want:    sessionRunnerModelProviderTemporaryReasonCode,
		},
		"provider quota": {
			message: `provider endpoint returned 429: {"error":{"message":"quota exhausted"}}`,
			want:    sessionRunnerModelProviderUnavailableReasonCode,
		},
		"provider authentication": {
			message: `provider endpoint returned 401: {"error":{"message":"invalid api key"}}`,
			want:    sessionRunnerModelProviderUnavailableReasonCode,
		},
		"provider network": {
			message: `Post "https://models.example.test/v1/chat/completions": dial tcp: connection refused`,
			want:    sessionRunnerModelProviderTemporaryReasonCode,
		},
		"provider timeout": {
			message: "context deadline exceeded",
			want:    sessionRunnerModelProviderTemporaryReasonCode,
		},
		"selected model disabled": {
			message: `model "ark-code-latest" is not enabled for this user`,
			want:    sessionRunnerModelProviderUnavailableReasonCode,
		},
		"provider secret missing": {
			message: "secret ark-api-key not found",
			want:    sessionRunnerModelProviderUnavailableReasonCode,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := sessionRunnerFailureReasonCode(test.message); got != test.want {
				t.Fatalf("reason code for %q = %q, want %q", test.message, got, test.want)
			}
		})
	}
}

func TestRecoveredRunnerMissingDeliverableCorrectionRequiresTool(t *testing.T) {
	entries := []eventjournal.Entry{{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "interrupted",
		"reason_code":   "artifact_reference_correction_required",
		"resume_detail": "runner completion reference integrity failed (missing_required_deliverables=1): missing required deliverables machine-readable validation record (.json)",
	}}}
	if !recoveredRunnerCorrectionRequiresTool(entries) {
		t.Fatal("missing deliverable correction allowed another prose-only candidate")
	}
	if choice := recoveredRunnerInitialToolChoice(entries, []agentruntime.ToolSchema{{Name: "edit_file"}, {Name: "save_artifacts"}}); choice != "required" {
		t.Fatalf("recovered correction did not require a model-selected action: %#v", choice)
	}
	context := recoveredRunnerCorrectionContext(entries)
	for _, required := range []string{"synon.runner_recovery.v1", `"required_transition":"repair_current_candidate"`, `"requires_tool":true`, "machine-readable validation record"} {
		if !strings.Contains(context, required) {
			t.Fatalf("correction context=%q missing %q", context, required)
		}
	}
}

func TestRecoveredRunnerMissingSourceCorrectionLeavesToolSelectionToModel(t *testing.T) {
	entries := []eventjournal.Entry{{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "interrupted",
		"reason_code":   sessionRunnerRealScientificEvidenceRequiredReasonCode,
		"resume_detail": "scientific execution requires real evidence: no durable authoritative source evidence",
	}}}
	choice := recoveredRunnerInitialToolChoice(entries, []agentruntime.ToolSchema{{Name: "web_search"}, {Name: "web_fetch"}, {Name: "save_artifacts"}})
	if choice != "required" {
		t.Fatalf("source correction did not require a model-selected tool: %#v", choice)
	}
}

func TestRecoveredRunnerCrossArtifactFailureRequiresModelChosenTool(t *testing.T) {
	entries := []eventjournal.Entry{{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "interrupted",
		"reason_code":   "artifact_reference_correction_required",
		"resume_detail": "runner completion reference integrity failed (cross_artifact_failures=1 missing_required_deliverables=0): machine_validation_missing_passing_check:validation_record.json",
	}}}
	if runnerCorrectionReportsMissingDeliverable(stringValue(entries[0].Message["resume_detail"])) {
		t.Fatal("zero missing deliverables was treated as a missing deliverable")
	}
	if !recoveredRunnerCorrectionRequiresTool(entries) {
		t.Fatal("cross-artifact correction allowed another prose-only candidate")
	}
}

func TestRecoveredRunnerSourceClaimCorrectionExplainsAttestedExcerptContract(t *testing.T) {
	entries := []eventjournal.Entry{{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "interrupted",
		"reason_code":   "artifact_reference_correction_required",
		"resume_detail": "runner completion reference integrity failed (cross_artifact_failures=1): cross-artifact consistency failures source_claim_missing_attested_excerpt:source_evidence.json source=source-1 claim=claim-1",
	}}}
	context := recoveredRunnerCorrectionContext(entries)
	for _, required := range []string{
		"synon.runner_recovery.v1",
		`"required_transition":"repair_current_candidate"`,
		`"choose_from_advertised_capabilities":true`,
		"source_claim_missing_attested_excerpt",
	} {
		if !strings.Contains(context, required) {
			t.Fatalf("source claim correction context=%q missing %q", context, required)
		}
	}
}

func TestRecoveredRunnerFinalAnswerReferenceCorrectionDoesNotForceTool(t *testing.T) {
	entries := []eventjournal.Entry{{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "interrupted",
		"reason_code":   "artifact_reference_correction_required",
		"resume_detail": "runner completion reference integrity failed (unresolved_artifacts=1 malformed_artifact_references=0 unsupported_citations=0 invalid_reference_artifacts=0 invalid_scientific_artifacts=0 cross_artifact_failures=0 invalid_research_artifacts=0 missing_local_artifacts=0 missing_required_deliverables=0)",
	}}}
	if recoveredRunnerCorrectionRequiresTool(entries) {
		t.Fatal("final-answer-only reference correction forced an unrelated tool call")
	}
	if choice := recoveredRunnerInitialToolChoice(entries); choice != nil {
		t.Fatalf("presentation-only correction initial choice=%#v", choice)
	}
}

func TestRecoveredRunnerUnsupportedCitationCorrectionAllowsRemovalWithoutForcingTool(t *testing.T) {
	entries := []eventjournal.Entry{{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "interrupted",
		"reason_code":   "artifact_reference_correction_required",
		"resume_detail": "runner completion reference integrity failed (unresolved_artifacts=0 malformed_artifact_references=0 unsupported_citations=1 invalid_reference_artifacts=0 invalid_scientific_artifacts=0 cross_artifact_failures=0 invalid_research_artifacts=0 missing_local_artifacts=0 missing_required_deliverables=0): unsupported identifiers accession:geo:GSE000001",
	}}}
	if recoveredRunnerCorrectionRequiresTool(entries) {
		t.Fatal("unsupported citation removal was forced through an unnecessary source-tool gate")
	}
	if choice := recoveredRunnerInitialToolChoice(entries, []agentruntime.ToolSchema{{Name: "web_search"}}); choice != nil {
		t.Fatalf("unsupported citation removal forced tool choice=%#v", choice)
	}
	context := recoveredRunnerCorrectionContext(entries)
	for _, required := range []string{"synon.runner_recovery.v1", `"requires_tool":false`, "unsupported identifiers accession:geo:GSE000001"} {
		if !strings.Contains(context, required) {
			t.Fatalf("unsupported citation correction context=%q missing %q", context, required)
		}
	}
}

func TestRecoveredRunnerRealScientificEvidenceCorrectionRequiresRealTool(t *testing.T) {
	entries := []eventjournal.Entry{{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "interrupted",
		"reason_code":   sessionRunnerRealScientificEvidenceRequiredReasonCode,
		"resume_detail": "the task has successful governed runtime execution evidence but no durable authoritative source evidence",
	}}}
	if !recoveredRunnerCorrectionRequiresTool(entries) {
		t.Fatal("real scientific evidence correction allowed another prose-only candidate")
	}
	context := recoveredRunnerCorrectionContext(entries)
	for _, required := range []string{
		"synon.runner_recovery.v1",
		`"required_transition":"satisfy_missing_capability"`,
		`"choose_from_advertised_capabilities":true`,
		"no durable authoritative source evidence",
	} {
		if !strings.Contains(context, required) {
			t.Fatalf("real scientific evidence correction context=%q missing %q", context, required)
		}
	}
}

func TestRecoveredRunnerSourceGroundedExecutionExplainsMaterialRoute(t *testing.T) {
	entries := []eventjournal.Entry{{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "interrupted",
		"reason_code": sessionRunnerRealScientificEvidenceRequiredReasonCode,
		"resume_detail": "the task has durable authoritative source evidence and preparatory runtime execution, " +
			"but no successful source-grounded runtime execution materialized analysis output",
	}}}
	context := recoveredRunnerCorrectionContext(entries)
	for _, required := range []string{
		"synon.runner_recovery.v1",
		`"required_transition":"satisfy_missing_capability"`,
		"no successful source-grounded runtime execution",
		`"reject_unchanged_repeat":true`,
	} {
		if !strings.Contains(context, required) {
			t.Fatalf("source-grounded correction context=%q missing %q", context, required)
		}
	}
}

func TestRecoveredRunnerMissingMCPReviewCorrectionExplainsLiveExecution(t *testing.T) {
	entries := []eventjournal.Entry{{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "interrupted",
		"reason_code":   "completion_review_correction_required",
		"resume_detail": "completion reviewer rejected the current candidate; explicit user-required tool evidence is incomplete; high: the explicitly requested real MCP method execution has no completed MCP tool result",
	}}}
	context := recoveredRunnerCorrectionContext(entries)
	for _, required := range []string{"synon.runner_recovery.v1", `"required_transition":"repair_rejected_acceptance_condition"`, "no completed MCP tool result"} {
		if !strings.Contains(context, required) {
			t.Fatalf("correction context=%q missing %q", context, required)
		}
	}
}

func TestRecoveredRunnerDoesNotInferToolOrderFromExistingValidation(t *testing.T) {
	entries := []eventjournal.Entry{
		{Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "interrupted",
			"reason_code":   "artifact_reference_correction_required",
			"resume_detail": "runner completion reference integrity failed (missing_required_deliverables=1): missing required deliverables machine-readable validation record (.json)",
		}},
		{Message: eventjournal.Message{
			"type": "runner_checkpoint", "runnerAttempt": 4,
			"status": "completed", "toolPhase": "completed", "toolName": "edit_file",
			"toolInput":  map[string]any{"file_path": "validation.json"},
			"toolResult": map[string]any{"success": true},
		}},
		{Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "interrupted",
			"reason_code":   "artifact_reference_correction_required",
			"resume_detail": "runner completion reference integrity failed (missing_required_deliverables=1): missing required deliverables machine-readable validation record (.json)",
		}},
	}
	context := recoveredRunnerCorrectionContext(entries)
	for _, required := range []string{"synon.runner_recovery.v1", `"choose_from_advertised_capabilities":true`, "missing required deliverables"} {
		if !strings.Contains(context, required) {
			t.Fatalf("correction context=%q missing %q", context, required)
		}
	}
}

func TestRecoveredRunnerCrossArtifactCorrectionRequiresTool(t *testing.T) {
	entries := []eventjournal.Entry{{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "interrupted",
		"reason_code":   "artifact_reference_correction_required",
		"resume_detail": "runner completion reference integrity failed (cross_artifact_failures=1): cross-artifact consistency failures machine_validation_missing_passing_check:validation.json",
	}}}
	if !recoveredRunnerCorrectionRequiresTool(entries) {
		t.Fatal("cross-artifact correction allowed another prose-only candidate")
	}
}

func TestRecoveredRunnerArtifactContentCorrectionIsModelDirected(t *testing.T) {
	entries := []eventjournal.Entry{{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "interrupted",
		"reason_code":   "artifact_reference_correction_required",
		"resume_detail": "runner completion reference integrity failed (unresolved_artifacts=1 malformed_artifact_references=0 unsupported_citations=0 invalid_reference_artifacts=0 invalid_scientific_artifacts=0 cross_artifact_failures=1 invalid_research_artifacts=0 missing_local_artifacts=0 missing_required_deliverables=0): cross-artifact consistency failures malformed_artifact_placeholder:report.md value=art_plot_png",
	}}}
	context := recoveredRunnerCorrectionContext(entries)
	for _, marker := range []string{"synon.runner_recovery.v1", `"required_transition":"repair_current_candidate"`, "malformed_artifact_placeholder:report.md value=art_plot_png", `"revalidate_after_state_change":true`} {
		if !strings.Contains(context, marker) {
			t.Fatalf("correction context=%q missing %q", context, marker)
		}
	}
}

func TestRecoveredRunnerValidationCorrectionIsModelDirected(t *testing.T) {
	entries := []eventjournal.Entry{{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "interrupted",
		"reason_code":   "artifact_reference_correction_required",
		"resume_detail": "runner completion reference integrity failed (cross_artifact_failures=1): cross-artifact consistency failures machine_validation_missing_passing_check:validation.json",
	}}}
	context := recoveredRunnerCorrectionContext(entries)
	for _, required := range []string{"synon.runner_recovery.v1", "machine_validation_missing_passing_check:validation.json", `"required_transition":"repair_current_candidate"`, `"reject_unchanged_repeat":true`} {
		if !strings.Contains(context, required) {
			t.Fatalf("correction context=%q missing %q", context, required)
		}
	}
}

func TestRecoveredRunnerCleanupCorrectionIsModelDirected(t *testing.T) {
	entries := []eventjournal.Entry{{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "interrupted",
		"reason_code":   "artifact_reference_correction_required",
		"resume_detail": "runner completion reference integrity failed (cross_artifact_failures=1): cross-artifact consistency failures machine_validation_reported_failure:validation.json path=process_cleanup/temporary_files_removed",
	}}}
	context := recoveredRunnerCorrectionContext(entries)
	for _, required := range []string{"synon.runner_recovery.v1", "process_cleanup/temporary_files_removed", `"preserve_completed_state":true`} {
		if !strings.Contains(context, required) {
			t.Fatalf("cleanup context=%q missing %q", context, required)
		}
	}
}

func TestRecoveredRunnerRankScoreCorrectionIsModelDirected(t *testing.T) {
	entries := []eventjournal.Entry{{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "interrupted",
		"reason_code":   "artifact_reference_correction_required",
		"resume_detail": "runner completion reference integrity failed (cross_artifact_failures=1): rank_score_pair_missing:scenario.csv rank=rank_neutral score=score_neutral",
	}}}
	context := recoveredRunnerCorrectionContext(entries)
	for _, required := range []string{"rank_score_pair_missing:scenario.csv", "synon.runner_recovery.v1", `"choose_from_advertised_capabilities":true`} {
		if !strings.Contains(context, required) {
			t.Fatalf("correction context=%q; missing %q", context, required)
		}
	}
}

func TestRecoveredSourceCorrectionDoesNotHardcodeFetchOrder(t *testing.T) {
	entries := []eventjournal.Entry{
		{Message: eventjournal.Message{
			"role": "user", "type": "message",
			"text": "外部方法学引用只允许使用可核验官方 ICH Q2(R2)/Q14 来源。",
		}},
		{Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "interrupted",
			"reason_code":   "artifact_reference_correction_required",
			"resume_detail": "runner completion reference integrity failed (unsupported_citations=1): unsupported identifiers url:https://www.fda.gov/q2r2",
		}},
	}
	context := recoveredRunnerCorrectionContext(entries)
	for _, required := range []string{"unsupported identifiers url:https://www.fda.gov/q2r2", "synon.runner_recovery.v1", `"choose_from_advertised_capabilities":true`} {
		if !strings.Contains(context, required) {
			t.Fatalf("correction context=%q missing %q", context, required)
		}
	}
	for _, forbidden := range []string{"start with repl", "web_fetch", "patent_search"} {
		if strings.Contains(strings.ToLower(context), forbidden) {
			t.Fatalf("generic correction context prescribed %q: %s", forbidden, context)
		}
	}
}

func TestSessionRunnerInterruptionMayContinueSameTaskIncludesAutoResumeReasons(t *testing.T) {
	for _, reason := range []string{
		"runtime_draining", sessionRunnerResumeDispatchInterruptedReasonCode,
		sessionRunnerToolLifecyclePersistenceReasonCode,
		"provider_stream_interrupted", "provider_stream_no_progress",
		sessionRunnerProviderContextPressureReasonCode,
		sessionRunnerToolRoundNoProgressReasonCode,
		sessionRunnerToolRoundNoProgressExhaustedReasonCode,
		sessionRunnerCompletionReviewRecoveryReasonCode,
		sessionRunnerEmptyFinalResponseReasonCode,
		sessionRunnerContentDeltaPersistenceDeadlineReasonCode,
		sessionRunnerToolRoundLimitReasonCode,
		sessionRunnerToolFailedReasonCode,
		sessionRunnerModelProtocolErrorReasonCode,
		sessionRunnerVisualMediaUnsupportedReasonCode,
		sessionRunnerVisualArtifactValidationReasonCode,
		sessionRunnerStoreContentionReasonCode,
		sessionRunnerProviderTransportTemporaryReasonCode,
		sessionRunnerModelProviderTemporaryReasonCode,
		sessionRunnerModelProviderUnavailableReasonCode,
		sessionRunnerSelectedSkillContractUnavailableReasonCode,
		"artifact_reference_correction_required",
	} {
		if !runnerInterruptionMayContinueSameTask(reason) {
			t.Fatalf("reason %q should allow same-task continuation", reason)
		}
	}
	if runnerInterruptionMayContinueSameTask("unclassified_failure") {
		t.Fatal("unclassified failure must remain terminal")
	}
}

func TestSessionRunnerInterruptionAutoResumeIncludesQualityCorrections(t *testing.T) {
	for _, reason := range []string{
		"runtime_draining", "provider_stream_interrupted", "provider_stream_no_progress",
		sessionRunnerToolLifecyclePersistenceReasonCode,
		sessionRunnerProviderContextPressureReasonCode,
		sessionRunnerToolRoundNoProgressReasonCode,
		sessionRunnerToolRoundNoProgressExhaustedReasonCode,
		sessionRunnerCompletionReviewRecoveryReasonCode,
		sessionRunnerEmptyFinalResponseReasonCode,
		sessionRunnerContentDeltaPersistenceDeadlineReasonCode,
		sessionRunnerToolRoundLimitReasonCode,
		sessionRunnerToolFailedReasonCode,
		sessionRunnerModelProtocolErrorReasonCode,
		sessionRunnerVisualMediaUnsupportedReasonCode,
		sessionRunnerVisualArtifactValidationReasonCode,
		sessionRunnerStoreContentionReasonCode,
		sessionRunnerProviderTransportTemporaryReasonCode,
		sessionRunnerModelProviderTemporaryReasonCode,
		sessionRunnerSelectedSkillContractUnavailableReasonCode,
		"artifact_reference_correction_required",
	} {
		if !runnerInterruptionAutoResume(reason) {
			t.Fatalf("reason %q should auto-resume", reason)
		}
	}
	for _, reason := range []string{
		sessionRunnerModelProviderUnavailableReasonCode, "",
	} {
		if runnerInterruptionAutoResume(reason) {
			t.Fatalf("reason %q must wait for a new user message or explicit continue", reason)
		}
	}
	if !runnerInterruptionIsProgressBoundary(sessionRunnerToolRoundLimitReasonCode) ||
		!runnerInterruptionIsProgressBoundary("provider_stream_interrupted") ||
		!runnerInterruptionIsProgressBoundary(sessionRunnerProviderOutputTokenLimitReasonCode) ||
		!runnerInterruptionIsProgressBoundary("runtime_draining") ||
		!runnerInterruptionIsProgressBoundary(sessionRunnerResumeDispatchInterruptedReasonCode) ||
		!runnerInterruptionIsProgressBoundary(sessionRunnerStoreContentionReasonCode) ||
		runnerInterruptionIsProgressBoundary("provider_stream_no_progress") ||
		runnerInterruptionIsProgressBoundary(sessionRunnerToolRoundNoProgressReasonCode) {
		t.Fatal("progress and durable infrastructure handoff boundaries must bypass semantic no-progress backoff")
	}
}

func TestSessionRunnerFailureReasonRequiresSingleChainRecoveryForTextOnlyVisualReview(t *testing.T) {
	for _, message := range []string{
		`provider endpoint returned 400: {"error":{"message":"Model only support text input"}}`,
		"image input is not supported by the selected model",
	} {
		reason := sessionRunnerFailureReasonCode(message)
		if reason != sessionRunnerVisualMediaUnsupportedReasonCode || !runnerInterruptionAutoResume(reason) {
			t.Fatalf("visual media failure reason=%q auto_resume=%t", reason, runnerInterruptionAutoResume(reason))
		}
	}
}

func TestSessionRunnerFailureReasonSeparatesTemporaryProviderCapacity(t *testing.T) {
	for _, message := range []string{
		`provider endpoint returned 429: {"error":{"message":"concurrent limit exceeded: running=6 max=6"}}`,
		"provider endpoint returned 503: service unavailable",
		"dial tcp 127.0.0.1:443: connection refused",
	} {
		got := sessionRunnerFailureReasonCode(message)
		if got != sessionRunnerModelProviderTemporaryReasonCode {
			t.Fatalf("reason for %q = %q", message, got)
		}
		if !runnerInterruptionAutoResume(got) {
			t.Fatalf("temporary provider reason %q must auto-resume", got)
		}
	}
	for _, message := range []string{
		`provider endpoint returned 429: {"error":{"message":"quota exhausted"}}`,
		`provider endpoint returned 429: {"error":{"code":"insufficient_quota"}}`,
		"provider credits exhausted",
	} {
		got := sessionRunnerFailureReasonCode(message)
		if got != sessionRunnerModelProviderUnavailableReasonCode {
			t.Fatalf("exhausted quota reason for %q = %q", message, got)
		}
		if runnerInterruptionAutoResume(got) {
			t.Fatalf("exhausted quota reason %q must wait for a model switch", got)
		}
	}
	if got := sessionRunnerFailureReasonCode("secret api-key not found"); got != sessionRunnerModelProviderUnavailableReasonCode {
		t.Fatalf("permanent provider reason = %q", got)
	}
}

func TestSessionRunnerFailureReasonSeparatesZeroProgressProviderTimeout(t *testing.T) {
	if got := sessionRunnerFailureReasonCode("provider stream made no progress"); got != "provider_stream_no_progress" {
		t.Fatalf("continuation no-progress reason=%q", got)
	}
	if got := sessionRunnerFailureReasonCode("provider stream response headers timeout"); got != sessionRunnerProviderTransportTemporaryReasonCode || !runnerInterruptionAutoResume(got) {
		t.Fatalf("response-header timeout reason=%q auto_resume=%t", got, runnerInterruptionAutoResume(got))
	}
	for _, message := range []string{"provider stream idle timeout", "provider stream total timeout"} {
		got := sessionRunnerFailureReasonCode(message)
		if got != "provider_stream_interrupted" || !runnerInterruptionAutoResume(got) {
			t.Fatalf("reason for %q = %q auto_resume=%t", message, got, runnerInterruptionAutoResume(got))
		}
	}
}

func TestProviderResponseHeadersTimeoutDetectionRequiresZeroProgress(t *testing.T) {
	err := errors.New("provider stream response headers timeout")
	if !providerResponseHeadersTimeoutWithoutProgress(err, 0, false) {
		t.Fatal("zero-byte response-header timeout was not detected")
	}
	if providerResponseHeadersTimeoutWithoutProgress(err, 1, false) ||
		providerResponseHeadersTimeoutWithoutProgress(err, 0, true) ||
		providerResponseHeadersTimeoutWithoutProgress(errors.New("provider stream idle timeout"), 0, false) {
		t.Fatal("progressing or continuation-safe provider failure was misclassified as a zero-byte header timeout")
	}
}

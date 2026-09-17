package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
)

func TestKernelHostSupervisionRunsRealChildCollectsStructuredAndResumes(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, manager, app, identity := newKernelHostSupervisionTestRuntime(t, databasePath, true)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = app.Close(ctx)
		closeKernelHostTestRuntime(t, app, manager, store)
	}()

	var requests atomic.Int64
	var mu sync.Mutex
	seenModels := []string{}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sequence := requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer child-provider-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		model, _ := body["model"].(string)
		mu.Lock()
		seenModels = append(seenModels, model)
		mu.Unlock()
		messages, _ := body["messages"].([]any)
		if sequence == 1 {
			tools, _ := body["tools"].([]any)
			if !hasChatToolNamed(tools, kernelDelegateSubmitToolName) || hasChatToolNamed(tools, "StructuredOutput") {
				t.Errorf("delegated output snapshot tools=%#v", tools)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		hasToolResult := false
		prompt := ""
		for _, raw := range messages {
			message, _ := raw.(map[string]any)
			role, _ := message["role"].(string)
			if role == "tool" {
				hasToolResult = true
			}
			if role == "user" {
				if text, ok := message["content"].(string); ok {
					prompt += text
				}
			}
		}
		if strings.Contains(prompt, "slow-child") && !hasToolResult {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(700 * time.Millisecond):
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if sequence == 1 && !hasToolResult {
			_, _ = w.Write([]byte(`{"id":"child-tool","choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"structured-call","type":"function","function":{"name":"submit_output","arguments":"{\"answer\":\"from-real-child\"}"}}]}}],"usage":{"prompt_tokens":8,"completion_tokens":2,"total_tokens":10}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"child-answer","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"real child completed"}}],"usage":{"prompt_tokens":6,"completion_tokens":3,"total_tokens":9}}`))
	}))
	defer provider.Close()
	configureKernelHostProvider(t, app, identity.access, "child-provider", "openai", provider.URL+"/v1", "pinned-child-model", "child-provider-secret", "child-provider-key")

	result := runKernelHostCell(t, app, identity, `
import host, json
handle = host.delegate({
    "task": "structured-child",
    "name": "schema-worker",
    "model": "pinned-child-model",
    "output_schema": {
        "type": "object",
        "properties": {"answer": {"type": "string"}},
        "required": ["answer"],
    },
}, wait=False)
collected = host.collect([handle], timeout=20)[0]
print(json.dumps({"handle": handle, "collected": collected, "stats": host.delegation_stats()}, sort_keys=True))
`)
	if result["ok"] != true {
		t.Fatalf("kernel supervision result = %#v", result)
	}
	output := decodeLastKernelJSONLine(t, result["stdout"].(string))
	handle := output["handle"].(map[string]any)
	collected := output["collected"].(map[string]any)
	if handle["status"] != "running" || handle["dispatched"] != true {
		t.Fatalf("async descriptor = %#v", handle)
	}
	structured, structuredOK := collected["structured_output"].(map[string]any)
	if collected["status"] != "completed" || !structuredOK || structured["answer"] != "from-real-child" {
		t.Fatalf("collected result = %#v", collected)
	}
	if output["stats"].(map[string]any)["spawned_this_task"] != float64(1) {
		t.Fatalf("delegation stats = %#v", output["stats"])
	}
	childID := handle["frame_id"].(string)
	child, found, err := store.GetKernelSupervisedChild(context.Background(), identity.access.Frame.ID, childID, identity.access.UserID)
	if err != nil || !found || child.Status != "completed" || child.Model != "pinned-child-model" {
		t.Fatalf("persisted child = %#v found=%t err=%v", child, found, err)
	}
	mu.Lock()
	models := append([]string(nil), seenModels...)
	mu.Unlock()
	if !kernelContainsString(models, "pinned-child-model") {
		t.Fatalf("delegated model pin did not preserve provider authority: %#v", models)
	}
	parentEvents, err := store.ListFrameEvents(identity.access.Frame.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !kernelFrameEventsContain(parentEvents, "tool_use") || !kernelFrameEventsContain(parentEvents, "notification") {
		t.Fatalf("parent tool mapping/completion events = %#v", parentEvents)
	}
	childEvents, err := store.ListFrameEvents(childID, 0, 100)
	if err != nil || !kernelFrameEventsContain(childEvents, "user_message") || !kernelFrameEventsContain(childEvents, "assistant_message") {
		t.Fatalf("child message projection = %#v err=%v", childEvents, err)
	}

	resumed := runKernelHostCell(t, app, identity, `
import host, json
print(json.dumps(host.send_message("`+childID+`", "follow-up after terminal", kind="question"), sort_keys=True))
`)
	if resumed["ok"] != true || !strings.Contains(resumed["stdout"].(string), `"status": "resumed"`) {
		t.Fatalf("terminal child resume receipt = %#v", resumed)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		current, found, _ := store.GetKernelSupervisedChild(context.Background(), identity.access.Frame.ID, childID, identity.access.UserID)
		if found && current.Status == "completed" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if requests.Load() < 3 {
		t.Fatalf("terminal resume did not run a real follow-up provider turn: %d", requests.Load())
	}
}

func TestKernelDelegateSubmitOutputIsSchemaBoundAndAtMostOnce(t *testing.T) {
	root := t.TempDir()
	app := newV11TestServer(t, Options{FileRoot: root})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = app.Close(ctx)
	})
	now := time.Now().UTC()
	session := sessionstore.Session{
		ID: "delegate-submit-session", Title: "Structured child", WorkDir: root,
		CreatedAt: now, UpdatedAt: now,
		Orchestration: map[string]any{
			"output_schema": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{"answer": map[string]any{"type": "string"}},
				"required":   []any{"answer"},
			},
		},
	}
	if err := app.sessionStore.Save(session); err != nil {
		t.Fatal(err)
	}
	if _, err := app.executeKernelDelegateSubmitOutput(session.ID, map[string]any{"wrong": true}); err == nil {
		t.Fatal("schema-invalid delegated output was accepted")
	}
	result, err := app.executeKernelDelegateSubmitOutput(session.ID, map[string]any{"answer": "verified"})
	if err != nil {
		t.Fatal(err)
	}
	structured, _ := result["structured_output"].(map[string]any)
	if structured["answer"] != "verified" || result["stored"] != true {
		t.Fatalf("delegated submit result=%#v", result)
	}
	if _, err := app.executeKernelDelegateSubmitOutput(session.ID, map[string]any{"answer": "replacement"}); err == nil {
		t.Fatal("second successful delegated output replaced the accepted result")
	}
}

func TestKernelHostSupervisionQueuesMessageDuringActiveProviderCall(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, manager, app, identity := newKernelHostSupervisionTestRuntime(t, databasePath, true)
	defer closeKernelHostTestRuntime(t, app, manager, store)

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var requests atomic.Int64
	var followUpSeen atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sequence := requests.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		raw, _ := json.Marshal(body["messages"])
		if strings.Contains(string(raw), "message delivered during active call") {
			followUpSeen.Store(true)
		}
		if sequence == 1 {
			close(firstStarted)
			select {
			case <-r.Context().Done():
				return
			case <-releaseFirst:
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"provider turn completed"}}]}`))
	}))
	defer provider.Close()
	configureKernelHostProvider(t, app, identity.access, "queue-provider", "openai", provider.URL+"/v1", "queue-model", "queue-secret", "queue-key")

	delegated := runKernelHostCell(t, app, identity, `
import host, json
print(json.dumps(host.delegate("active queue child", model="queue-model", wait=False), sort_keys=True))
`)
	if delegated["ok"] != true {
		t.Fatalf("delegate result = %#v", delegated)
	}
	descriptor := decodeLastKernelJSONLine(t, delegated["stdout"].(string))
	childID := descriptor["frame_id"].(string)
	select {
	case <-firstStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("child provider request did not start")
	}
	receipt, err := app.sendKernelChildMessage(context.Background(), identity.access, childID, "message delivered during active call", "info", "hc-00000000000000000000000000000001", nil)
	if err != nil || receipt["status"] != "injected" {
		t.Fatalf("active message receipt=%#v err=%v", receipt, err)
	}
	close(releaseFirst)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		child, found, _ := store.GetKernelSupervisedChild(context.Background(), identity.access.Frame.ID, childID, identity.access.UserID)
		if found && child.Status == "completed" && requests.Load() >= 2 && followUpSeen.Load() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if requests.Load() < 2 || !followUpSeen.Load() {
		t.Fatalf("queued active message was not consumed by a later provider turn: requests=%d seen=%t", requests.Load(), followUpSeen.Load())
	}
	if pending, err := store.KernelChildHasPendingMessages(context.Background(), childID, identity.access.UserID); err != nil || pending {
		t.Fatalf("pending queue after follow-up=%t err=%v", pending, err)
	}
}

func TestKernelHostSupervisionRecoversPendingMessageAfterRuntimeRestart(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, manager, app, identity := newKernelHostSupervisionTestRuntime(t, databasePath, true)
	providerRequests := atomic.Int64{}
	providerStarted := make(chan struct{})
	releaseProvider := make(chan struct{})
	var providerStartedOnce, releaseProviderOnce sync.Once
	releaseProviderRequest := func() { releaseProviderOnce.Do(func() { close(releaseProvider) }) }
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerRequests.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		raw, _ := json.Marshal(body["messages"])
		if !strings.Contains(string(raw), "restart-persisted follow-up") {
			t.Errorf("recovered request omitted queued message: %s", raw)
		}
		providerStartedOnce.Do(func() { close(providerStarted) })
		select {
		case <-r.Context().Done():
			return
		case <-releaseProvider:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"restart recovery completed"}}]}`))
	}))
	t.Cleanup(func() {
		releaseProviderRequest()
		provider.Close()
	})
	configureKernelHostProvider(t, app, identity.access, "restart-provider", "openai", provider.URL+"/v1", "restart-model", "restart-secret", "restart-key")
	children, err := store.CreateKernelSupervisedChildren(context.Background(), workspace.CreateKernelDelegatesInput{
		ParentFrameID: identity.access.Frame.ID, OwnerUserID: identity.access.UserID, ToolUseID: "restart-tool",
		Requests: []workspace.KernelDelegateRequest{{Task: "initial task", Name: "restart-child", Model: "restart-model"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	childID := children[0].FrameID
	queued, err := store.QueueKernelSupervisionMessage(context.Background(), identity.access.Frame.ID, childID, identity.access.UserID, "restart-persisted follow-up", "question")
	if err != nil || queued.Queued == nil {
		t.Fatalf("queue before restart=%#v err=%v", queued, err)
	}
	closeKernelHostTestRuntime(t, app, manager, store)

	store, manager, app, identity = newKernelHostSupervisionTestRuntime(t, databasePath, false)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	type collectOutcome struct {
		result map[string]any
		err    error
	}
	collectDone := make(chan collectOutcome, 1)
	collectCtx, cancelCollect := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancelCollect()
	go func() {
		result, err := app.executeAgentKernelTool(collectCtx, identity, "Operon", map[string]any{"code": `
import host, json
print(json.dumps(host.collect(["` + childID + `"], timeout=20)[0], sort_keys=True))
`})
		collectDone <- collectOutcome{result: result, err: err}
	}()
	waitForKernelSupervisionSignal(t, providerStarted, "recovered child provider request")
	recoveredRun := kernelSupervisionRunAtBoundary(t, app, childID)
	releaseProviderRequest()
	waitForKernelSupervisionRun(t, recoveredRun, "recovered child")
	var collected map[string]any
	select {
	case outcome := <-collectDone:
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		collected = outcome.result
	case <-time.After(5 * time.Second):
		t.Fatal("host.collect did not return after recovered child completion")
	}
	if collected["ok"] != true {
		t.Fatalf("restart collect=%#v", collected)
	}
	result := decodeLastKernelJSONLine(t, collected["stdout"].(string))
	if result["status"] != "completed" || providerRequests.Load() != 1 {
		t.Fatalf("restart result=%#v requests=%d", result, providerRequests.Load())
	}
}

func TestKernelHostSupervisionParksForQuestionAndResumesWithParentAnswer(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, manager, app, identity := newKernelHostSupervisionTestRuntime(t, databasePath, true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	var requests atomic.Int64
	questionStarted := make(chan struct{})
	releaseQuestion := make(chan struct{})
	answerStarted := make(chan struct{})
	releaseAnswer := make(chan struct{})
	var questionStartedOnce, answerStartedOnce, releaseQuestionOnce, releaseAnswerOnce sync.Once
	releaseQuestionRequest := func() { releaseQuestionOnce.Do(func() { close(releaseQuestion) }) }
	releaseAnswerRequest := func() { releaseAnswerOnce.Do(func() { close(releaseAnswer) }) }
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sequence := requests.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		raw, _ := json.Marshal(body["messages"])
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(raw), "Use the approved assay window") {
			answerStartedOnce.Do(func() { close(answerStarted) })
			select {
			case <-r.Context().Done():
				return
			case <-releaseAnswer:
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"parent answer consumed"}}]}`))
			return
		}
		if sequence == 1 {
			questionStartedOnce.Do(func() { close(questionStarted) })
			select {
			case <-r.Context().Done():
				return
			case <-releaseQuestion:
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"ask-child","type":"function","function":{"name":"AskUserQuestion","arguments":"{\"questions\":[{\"question\":\"Which assay window?\",\"header\":\"Assay\",\"options\":[{\"label\":\"Short\",\"description\":\"Use the short assay window.\",\"pros\":\"Returns an earlier readout.\",\"cons\":\"May miss delayed effects.\",\"readiness\":\"Assay capacity has not been checked in this task.\",\"readiness_status\":\"unverified\",\"decision_evidence\":[\"user-input:current-task\"],\"readiness_evidence\":[],\"selection_basis\":\"user_objective\",\"expected_outcome\":\"Results from the short assay window if executable.\",\"selection_rationale\":\"Recommended when the user prioritizes early effects.\",\"recommended\":true},{\"label\":\"Long\",\"description\":\"Use the long assay window.\",\"pros\":\"Captures delayed effects.\",\"cons\":\"Requires a longer observation period.\",\"readiness\":\"Assay capacity has not been checked in this task.\",\"readiness_status\":\"unverified\",\"decision_evidence\":[\"user-input:current-task\"],\"readiness_evidence\":[],\"selection_basis\":\"user_objective\",\"expected_outcome\":\"Results from the long assay window if executable.\",\"selection_rationale\":\"Choose when the user prioritizes delayed effects.\",\"recommended\":false}]}]}"}}]}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"waiting for the parent answer"}}]}`))
	}))
	t.Cleanup(func() {
		releaseQuestionRequest()
		releaseAnswerRequest()
		provider.Close()
	})
	configureKernelHostProvider(t, app, identity.access, "question-provider", "openai", provider.URL+"/v1", "question-model", "question-secret", "question-key")

	delegated := runKernelHostCell(t, app, identity, `
import host, json
print(json.dumps(host.delegate("ask the parent when needed", model="question-model", wait=False), sort_keys=True))
`)
	descriptor := decodeLastKernelJSONLine(t, delegated["stdout"].(string))
	childID := descriptor["frame_id"].(string)
	waitForKernelSupervisionSignal(t, questionStarted, "child question provider request")
	questionRun := kernelSupervisionRunAtBoundary(t, app, childID)
	releaseQuestionRequest()
	waitForKernelSupervisionRun(t, questionRun, "child question parking")
	parked, found, err := store.GetKernelSupervisedChild(context.Background(), identity.access.Frame.ID, childID, identity.access.UserID)
	if err != nil || !found || parked.Status != "awaiting_user_response" || parked.Error != "" {
		t.Fatalf("parked child=%#v found=%t err=%v", parked, found, err)
	}
	childEvents, err := store.ListFrameEvents(childID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range childEvents {
		if event.Type == "frame_failed" {
			t.Fatalf("parked child emitted a failure event: %#v", event)
		}
	}
	parentEvents, err := store.ListFrameEvents(identity.access.Frame.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range parentEvents {
		if event.Type == "notification" && event.Payload["notification_type"] == "child_landed" {
			t.Fatalf("parked child emitted premature completion: %#v", event)
		}
	}
	view := runKernelHostCell(t, app, identity, `
import host, json
print(json.dumps(host.children(), sort_keys=True))
`)
	childrenView := decodeLastKernelJSONLine(t, view["stdout"].(string))
	if childrenView["count"] != float64(1) {
		t.Fatalf("parked children view=%#v", childrenView)
	}
	receipt, err := app.sendKernelChildMessage(context.Background(), identity.access, childID, "Use the approved assay window", "question", "hc-00000000000000000000000000000002", nil)
	if err != nil || receipt["status"] != "injected" {
		t.Fatalf("parked answer receipt=%#v err=%v", receipt, err)
	}
	waitForKernelSupervisionSignal(t, answerStarted, "answered child provider request")
	answerRun := kernelSupervisionRunAtBoundary(t, app, childID)
	releaseAnswerRequest()
	waitForKernelSupervisionRun(t, answerRun, "answered child completion")
	child, found, err := store.GetKernelSupervisedChild(context.Background(), identity.access.Frame.ID, childID, identity.access.UserID)
	if err != nil || !found || child.Status != "completed" || requests.Load() != 3 ||
		!strings.Contains(stringValue(child.Output["response"]), "parent answer consumed") {
		t.Fatalf("answered child did not complete: child=%#v found=%t err=%v requests=%d", child, found, err, requests.Load())
	}
}

func waitForKernelSupervisionSignal(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not arrive", label)
	}
}

func kernelSupervisionRunAtBoundary(t *testing.T, app *Server, childID string) *kernelChildRun {
	t.Helper()
	value, found := kernelChildRuns.Load(kernelChildRunKey{server: app, frameID: childID})
	if !found {
		t.Fatalf("kernel child run %s is absent at provider boundary", childID)
	}
	run, ok := value.(*kernelChildRun)
	if !ok || run == nil {
		t.Fatalf("kernel child run %s has invalid state %#v", childID, value)
	}
	return run
}

func waitForKernelSupervisionRun(t *testing.T, run *kernelChildRun, label string) {
	t.Helper()
	select {
	case <-run.done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not settle", label)
	}
}

func TestKernelHostSupervisionTimeoutStopTopologyAndUpfrontLimits(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, manager, app, identity := newKernelHostSupervisionTestRuntime(t, databasePath, true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		select {
		case <-r.Context().Done():
			return
		case <-time.After(600 * time.Millisecond):
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"slow done"}}]}`))
	}))
	defer provider.Close()
	configureKernelHostProvider(t, app, identity.access, "slow-provider", "openai", provider.URL+"/v1", "slow-model", "slow-secret", "slow-key")

	timed := runKernelHostCell(t, app, identity, `
import host, json
r = host.delegate("slow-child", timeout=0.05)
print(json.dumps(r, sort_keys=True))
`)
	if timed["ok"] != true {
		t.Fatalf("timed delegation = %#v", timed)
	}
	descriptor := decodeLastKernelJSONLine(t, timed["stdout"].(string))
	if descriptor["status"] != "running" || descriptor["dispatched"] != nil {
		t.Fatalf("deadline descriptor = %#v", descriptor)
	}
	childID := descriptor["frame_id"].(string)
	stopped := runKernelHostCell(t, app, identity, `
import host, json
print(json.dumps(host.stop_child("`+childID+`", reason="test stop"), sort_keys=True))
`)
	if stopped["ok"] != true || !strings.Contains(stopped["stdout"].(string), `"status": "stopped"`) ||
		!strings.Contains(stopped["stdout"].(string), `"stopped_by_parent": true`) {
		t.Fatalf("stop result = %#v", stopped)
	}
	again := runKernelHostCell(t, app, identity, `
import host, json
print(json.dumps(host.stop_child("`+childID+`"), sort_keys=True))
`)
	if again["ok"] != true || !strings.Contains(again["stdout"].(string), `"status": "already_terminal"`) ||
		!strings.Contains(again["stdout"].(string), `"child_status": "cancelled"`) {
		t.Fatalf("idempotent stop result = %#v", again)
	}
	batchStopped := runKernelHostCell(t, app, identity, `
import host, json
wave = host.delegate([{"task":"batch stop one"}, {"task":"batch stop two"}], wait=False)
result = host.stop_child([wave[0], wave[1], {"missing":"frame"}], reason="batch stop")
print(json.dumps(result, sort_keys=True))
`)
	if batchStopped["ok"] != true {
		t.Fatalf("batch stop cell = %#v", batchStopped)
	}
	var batchResult []any
	lines := strings.Split(strings.TrimSpace(batchStopped["stdout"].(string)), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &batchResult); err != nil {
		t.Fatalf("decode batch stop: %v stdout=%q", err, batchStopped["stdout"])
	}
	if len(batchResult) != 3 || batchResult[0].(map[string]any)["status"] != "stopped" || batchResult[1].(map[string]any)["status"] != "stopped" || batchResult[2].(map[string]any)["status"] != "failed" {
		t.Fatalf("batch stop shape/order = %#v", batchResult)
	}
	rawStopped, found, err := store.GetKernelSupervisedChild(context.Background(), identity.access.Frame.ID, childID, identity.access.UserID)
	if err != nil || !found || rawStopped.Status != "completed" || rawStopped.Output["stopped_by_parent"] != true {
		t.Fatalf("durable stopped child=%#v found=%t err=%v", rawStopped, found, err)
	}
	collectHandle := runKernelHostCell(t, app, identity, `
import host, json
print(json.dumps(host.delegate("collect cancellation child", model="slow-model", wait=False), sort_keys=True))
`)
	collectChildID := decodeLastKernelJSONLine(t, collectHandle["stdout"].(string))["frame_id"].(string)
	collectCtx, cancelCollect := context.WithCancel(context.Background())
	type collectOutcome struct {
		value any
		err   error
	}
	collectDone := make(chan collectOutcome, 1)
	go func() {
		value, err := app.handleKernelSupervisionHostCall(collectCtx, kernelHostExecutionIdentity{}, identity.access, "host.collect", []any{
			map[string]any{"frame_ids": []any{collectChildID}, "timeout": float64(30)},
		}, nil, "collect-cancelled", nil)
		collectDone <- collectOutcome{value: value, err: err}
	}()
	time.Sleep(20 * time.Millisecond)
	cancelCollect()
	collectOutcomeValue := <-collectDone
	collectRaw, collectErr := collectOutcomeValue.value, collectOutcomeValue.err
	if collectErr != nil {
		t.Fatalf("cancelled collect error=%v", collectErr)
	}
	collectResults := collectRaw.([]any)
	if len(collectResults) != 1 || collectResults[0].(map[string]any)["status"] != "cancelled" || collectResults[0].(map[string]any)["error"] != "collect cancelled" {
		t.Fatalf("cancelled collect result=%#v", collectResults)
	}
	stillRunning, found, err := store.GetKernelSupervisedChild(context.Background(), identity.access.Frame.ID, collectChildID, identity.access.UserID)
	if err != nil || !found || kernelChildTerminal(stillRunning.Status) {
		t.Fatalf("collect cancellation stopped child=%#v found=%t err=%v", stillRunning, found, err)
	}
	_ = app.stopKernelChild(context.Background(), identity.access, collectChildID, "test cleanup")

	before, err := store.KernelDelegationStats(context.Background(), identity.access.Frame.ID, identity.access.UserID, workspace.DefaultKernelDelegationSpawnCap)
	if err != nil {
		t.Fatal(err)
	}
	oversized := runKernelHostCell(t, app, identity, `
import host
try:
    host.delegate([{"task": "must-not-spawn"} for _ in range(49)], wait=False)
except Exception as exc:
    print(type(exc).__name__, str(exc))
`)
	if oversized["ok"] != true || !strings.Contains(oversized["stdout"].(string), "max 48") {
		t.Fatalf("49 upfront reject = %#v", oversized)
	}
	after, err := store.KernelDelegationStats(context.Background(), identity.access.Frame.ID, identity.access.UserID, workspace.DefaultKernelDelegationSpawnCap)
	if err != nil || after.SpawnedThisTask != before.SpawnedThisTask {
		t.Fatalf("49 request wrote children before=%#v after=%#v err=%v", before, after, err)
	}
	legacyConcurrency := runKernelHostCell(t, app, identity, `
import host
child = host.delegate({"task":"legacy concurrency ignored"}, max_concurrency="ignored", wait=False)
print(child["status"])
host.stop_child(child)
`)
	if legacyConcurrency["ok"] != true || !strings.Contains(legacyConcurrency["stdout"].(string), "running") {
		t.Fatalf("legacy max_concurrency = %#v", legacyConcurrency)
	}
	partialSchema := runKernelHostCell(t, app, identity, `
import host, json
result = host.delegate([
    {"task":"valid schema child", "output_schema":{"type":"object","properties":{"answer":{"type":"string"}}}},
    {"task":"invalid schema child", "output_schema":{"type":"string"}},
], wait=False)
print(json.dumps(result, sort_keys=True))
host.stop_child(result[0])
`)
	if partialSchema["ok"] != true {
		t.Fatalf("partial schema cell=%#v", partialSchema)
	}
	lines = strings.Split(strings.TrimSpace(partialSchema["stdout"].(string)), "\n")
	var schemaResults []any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &schemaResults); err != nil {
		t.Fatalf("decode partial schema results: %v stdout=%q", err, partialSchema["stdout"])
	}
	if len(schemaResults) != 2 || schemaResults[0].(map[string]any)["status"] != "running" || schemaResults[1].(map[string]any)["status"] != "failed" {
		t.Fatalf("partial schema results=%#v", schemaResults)
	}
	partialAuthority := runKernelHostCell(t, app, identity, `
import host, json
result = host.delegate([
    {"task":"valid authority child", "output_schema":{"$schema":"http://json-schema.org/draft-07/schema", "type":"object"}},
    {"task":"invalid profile child", "profile":"missing-profile"},
], wait=False)
print(json.dumps(result, sort_keys=True))
host.stop_child(result[0])
`)
	if partialAuthority["ok"] != true {
		t.Fatalf("partial authority cell=%#v", partialAuthority)
	}
	authorityLines := strings.Split(strings.TrimSpace(partialAuthority["stdout"].(string)), "\n")
	var authorityResults []any
	if err := json.Unmarshal([]byte(authorityLines[len(authorityLines)-1]), &authorityResults); err != nil {
		t.Fatalf("decode partial authority results: %v stdout=%q", err, partialAuthority["stdout"])
	}
	if len(authorityResults) != 2 || authorityResults[0].(map[string]any)["status"] != "running" || authorityResults[1].(map[string]any)["status"] != "failed" {
		t.Fatalf("partial authority results=%#v", authorityResults)
	}
	collectNone := runKernelHostCell(t, app, identity, `
import host
for value in (None, 1801):
    try:
        host.collect(["`+childID+`"], timeout=value)
    except Exception as exc:
        print(type(exc).__name__, str(exc))
`)
	if collectNone["ok"] != true || !strings.Contains(collectNone["stdout"].(string), "timeout=None") || !strings.Contains(collectNone["stdout"].(string), "1800") {
		t.Fatalf("collect timeout validation = %#v", collectNone)
	}
	batchLimits := runKernelHostCell(t, app, identity, `
import host
for fn in (host.collect, host.stop_child):
    try:
        fn(["missing"] * 1001)
    except Exception as exc:
        print(str(exc))
try:
    host.delegate("too long", timeout=86401)
except Exception as exc:
    print(str(exc))
`)
	if batchLimits["ok"] != true || strings.Count(batchLimits["stdout"].(string), "max 1000") != 2 || !strings.Contains(batchLimits["stdout"].(string), "86400") {
		t.Fatalf("oracle batch/timeout limits = %#v", batchLimits)
	}

	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "unrelated", ProjectID: identity.access.Frame.ProjectID, AgentName: "GENERAL", Status: "processing", ConversationType: "main"}); err != nil {
		t.Fatal(err)
	}
	denied := runKernelHostCell(t, app, identity, `
import host, json
print(json.dumps(host.send_message("unrelated", "must deny"), sort_keys=True))
`)
	if denied["ok"] != true || !strings.Contains(denied["stdout"].(string), "outside the direct supervision topology") {
		t.Fatalf("topology denial = %#v", denied)
	}
	forged := *identity
	forged.access.UserID = "other-user"
	_, forgedErr := app.executeAgentKernelTool(context.Background(), &forged, "Operon", map[string]any{"code": `import host
host.children()`})
	if forgedErr == nil || !strings.Contains(forgedErr.Error(), "ownership") {
		t.Fatalf("cross-user host identity error=%v", forgedErr)
	}
}

func TestKernelHostSupervisionParallelOrderChildrenAndBlockingCancel(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, manager, app, identity := newKernelHostSupervisionTestRuntime(t, databasePath, true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	parallelReady := make(chan struct{})
	var parallelCalls atomic.Int64
	var parallelOverlapFailed atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		model := stringValue(body["model"])
		if model == "parallel-a" || model == "parallel-b" {
			if parallelCalls.Add(1) == 2 {
				close(parallelReady)
			}
			select {
			case <-parallelReady:
			case <-time.After(time.Second):
				parallelOverlapFailed.Store(true)
				w.WriteHeader(http.StatusGatewayTimeout)
				return
			}
		}
		delay := 20 * time.Millisecond
		if model == "parallel-a" {
			delay = 120 * time.Millisecond
		}
		if model == "cancel-model" {
			<-r.Context().Done()
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(delay):
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done-` + model + `"}}]}`))
	}))
	defer provider.Close()
	configureKernelHostProvider(t, app, identity.access, "parallel-a-provider", "openai", provider.URL+"/v1", "parallel-a", "parallel-a-secret", "parallel-key")
	configureKernelHostProvider(t, app, identity.access, "parallel-b-provider", "openai", provider.URL+"/v1", "parallel-b", "parallel-b-secret", "parallel-key")
	configureKernelHostProvider(t, app, identity.access, "cancel-provider", "openai", provider.URL+"/v1", "cancel-model", "cancel-secret", "parallel-key")

	parallel := runKernelHostCell(t, app, identity, `
import host, json
results = host.delegate([
    {"task":"parallel first", "name":"first", "model":"parallel-a"},
    {"task":"parallel second", "name":"second", "model":"parallel-b"},
])
print(json.dumps({"results":results, "children":host.children()}, sort_keys=True))
	`)
	if parallel["ok"] != true {
		frames, _ := store.ListFramesForRoot(identity.access.Frame.RootFrameID)
		children, _ := store.ListKernelActiveChildren(context.Background(), identity.access.Frame.ID, identity.access.UserID)
		t.Fatalf("parallel delegation = %#v frames=%#v children=%#v", parallel, frames, children)
	}
	if parallelOverlapFailed.Load() || parallelCalls.Load() < 2 {
		t.Fatalf("parallel provider calls did not overlap: calls=%d result=%#v", parallelCalls.Load(), parallel)
	}
	output := decodeLastKernelJSONLine(t, parallel["stdout"].(string))
	results := output["results"].([]any)
	if len(results) != 2 || !strings.Contains(stringValue(results[0].(map[string]any)["response"]), "parallel-a") ||
		!strings.Contains(stringValue(results[1].(map[string]any)["response"]), "parallel-b") {
		t.Fatalf("position-preserving parallel results = %#v", results)
	}
	childrenView := output["children"].(map[string]any)
	if childrenView["count"] != float64(0) {
		t.Fatalf("completed children leaked into active view = %#v", childrenView)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = app.handleKernelSupervisionHostCall(ctx, kernelHostExecutionIdentity{}, identity.access, "host.delegate", []any{
			[]any{map[string]any{"task": "cancel blocking", "name": "cancelled", "model": "cancel-model"}},
		}, nil, "blocking-cancel-tool", nil)
	}()
	var cancelChild workspace.KernelSupervisedChild
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		active, err := store.ListKernelActiveChildren(context.Background(), identity.access.Frame.ID, identity.access.UserID)
		if err != nil {
			t.Fatal(err)
		}
		for _, child := range active {
			if child.ToolUseID == "blocking-cancel-tool" {
				cancelChild = child
				break
			}
		}
		if cancelChild.FrameID != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if cancelChild.FrameID == "" {
		t.Fatal("blocking child was not durably created")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("blocking delegate did not release after cell cancellation")
	}
	cancelled, found, err := store.GetKernelSupervisedChild(context.Background(), identity.access.Frame.ID, cancelChild.FrameID, identity.access.UserID)
	if err != nil || !found || cancelled.Status != "cancelled" {
		t.Fatalf("blocking cancellation child = %#v found=%t err=%v", cancelled, found, err)
	}
}

func TestKernelHostSendMessageMatchesPeerBudgetsAndNormalization(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, manager, app, identity := newKernelHostTestRuntime(t, databasePath, true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	children, err := store.CreateKernelSupervisedChildren(context.Background(), workspace.CreateKernelDelegatesInput{
		ParentFrameID: identity.access.Frame.ID, OwnerUserID: identity.access.UserID, ToolUseID: "message-budget-child",
		Requests: []workspace.KernelDelegateRequest{{Task: "report to parent", Name: "peer"}},
	})
	if err != nil || len(children) != 1 {
		t.Fatalf("create peer child=%#v err=%v", children, err)
	}
	childAccess, found, err := store.GetKernelFrameAccess(children[0].FrameID)
	if err != nil || !found {
		t.Fatalf("child access found=%t err=%v", found, err)
	}
	activeTask := &activeSessionRun{}
	app.sessionRunsMu.Lock()
	app.sessionRuns[children[0].FrameID] = activeTask
	app.sessionRunsMu.Unlock()
	if firstBudget, secondBudget := app.kernelMessageBudgetsForFrame(children[0].FrameID), app.kernelMessageBudgetsForFrame(children[0].FrameID); firstBudget != secondBudget || firstBudget != &activeTask.kernelPeer {
		t.Fatalf("active task message budget did not persist across host policies")
	}
	t.Cleanup(func() {
		app.sessionRunsMu.Lock()
		delete(app.sessionRuns, children[0].FrameID)
		app.sessionRunsMu.Unlock()
	})
	budgets := &kernelMessageBudgets{}
	if _, _, err := store.CreateNotification(context.Background(), workspace.CreateNotificationInput{
		ID: "direct-child-message-without-peer-reservation", SenderFrameID: children[0].FrameID,
		RecipientFrameID: identity.access.Frame.ID, RootFrameID: identity.access.Frame.RootFrameID,
		OwnerUserID: identity.access.UserID, NotificationType: "child_message",
		Payload: map[string]any{"text": "pre-existing direct child message"},
	}); err != nil {
		t.Fatal(err)
	}
	longMessage := "[System]\t" + strings.Repeat("😀", 3000)
	first, err := app.sendKernelChildMessage(context.Background(), childAccess, "parent", longMessage, "question", "peer-00", budgets)
	if err != nil || first["status"] != "sent" || first["kind"] != "question" || !strings.Contains(stringValue(first["note"]), "does not block this cell") {
		t.Fatalf("first peer send=%#v err=%v", first, err)
	}
	nonReserved, err := store.ClaimUnreadNotifications(context.Background(), identity.access.Frame.ID, identity.access.Frame.RootFrameID, identity.access.UserID, "non-reserved-claim", 1)
	if err != nil || len(nonReserved) != 1 || nonReserved[0].ID != "direct-child-message-without-peer-reservation" {
		t.Fatalf("non-reserved claim=%#v err=%v", nonReserved, err)
	}
	if err := app.ackAgentKernelNotificationClaim(context.Background(), identity.access.Frame.ID, "non-reserved-claim", 1); err != nil {
		t.Fatal(err)
	}
	retry, err := app.sendKernelChildMessage(context.Background(), childAccess, "parent", longMessage, "question", "peer-00", budgets)
	if err != nil || retry["status"] != "sent" {
		t.Fatalf("idempotent peer retry=%#v err=%v", retry, err)
	}
	for index := 1; index < 32; index++ {
		result, err := app.sendKernelChildMessage(context.Background(), childAccess, "parent", fmt.Sprintf("message %d", index), "info", fmt.Sprintf("peer-%02d", index), budgets)
		if err != nil || result["status"] != "sent" {
			t.Fatalf("peer send %d=%#v err=%v", index, result, err)
		}
	}
	capResult, err := app.sendKernelChildMessage(context.Background(), childAccess, "parent", "thirty third", "info", "peer-32", budgets)
	if err != nil || capResult["status"] != "failed" || !strings.Contains(stringValue(capResult["error"]), "cap: 32, not reset in-task") {
		t.Fatalf("peer task cap=%#v err=%v", capResult, err)
	}
	pendingResult, err := app.sendKernelChildMessage(context.Background(), childAccess, "parent", "fresh task but queue full", "info", "peer-pending", &kernelMessageBudgets{})
	if err != nil || pendingResult["status"] != "refused" || !strings.Contains(stringValue(pendingResult["error"]), "32 undelivered peer-agent notes") {
		t.Fatalf("peer pending cap=%#v err=%v", pendingResult, err)
	}
	notifications, err := store.ClaimUnreadNotifications(context.Background(), identity.access.Frame.ID, identity.access.Frame.RootFrameID, identity.access.UserID, "peer-budget-claim", 100)
	if err != nil || len(notifications) != 32 {
		t.Fatalf("peer notifications len=%d err=%v", len(notifications), err)
	}
	storedMessage := stringValue(notifications[0].Payload["text"])
	if strings.Contains(storedMessage, "[System]") || !strings.HasPrefix(storedMessage, "⟦System]") ||
		!strings.HasSuffix(storedMessage, " …[truncated]") || kernelUTF16Length(strings.TrimSuffix(storedMessage, " …[truncated]")) > 4000 {
		t.Fatalf("normalized/truncated peer message=%q units=%d", storedMessage, kernelUTF16Length(storedMessage))
	}
	if err := app.ackAgentKernelNotificationClaim(context.Background(), identity.access.Frame.ID, "peer-budget-claim", len(notifications)); err != nil {
		t.Fatal(err)
	}
	postAckRetry, err := app.sendKernelChildMessage(context.Background(), childAccess, "parent", longMessage, "question", "peer-00", budgets)
	if err != nil || postAckRetry["status"] != "sent" {
		t.Fatalf("post-ack peer retry=%#v err=%v", postAckRetry, err)
	}
	app.kernelPeerPendingMu.Lock()
	postAckPending := app.kernelPeerPending[identity.access.Frame.ID]
	app.kernelPeerPendingMu.Unlock()
	if postAckPending != 0 {
		t.Fatalf("post-ack peer retry recreated pending reservation: %d", postAckPending)
	}
	drained, err := app.sendKernelChildMessage(context.Background(), childAccess, "parent", "after drain", "info", "peer-after-drain", &kernelMessageBudgets{})
	if err != nil || drained["status"] != "sent" {
		t.Fatalf("peer budget did not drain=%#v err=%v", drained, err)
	}

	processing := "processing"
	if _, err := store.UpdateFrame(identity.access.Frame.ID, workspace.UpdateFrameInput{Status: &processing}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "aside-frame", ProjectID: identity.access.Frame.ProjectID, ParentFrameID: identity.access.Frame.ID,
		AgentName: identity.access.Frame.AgentName, Status: "processing", ConversationType: "aside", Name: "Aside",
	}); err != nil {
		t.Fatal(err)
	}
	asideAccess, found, err := store.GetKernelFrameAccess("aside-frame")
	if err != nil || !found {
		t.Fatalf("aside access found=%t err=%v", found, err)
	}
	asideBudget := &kernelMessageBudgets{}
	firstAside, err := app.sendKernelChildMessage(context.Background(), asideAccess, "main", "aside\tmessage 0", "question", "aside-0", asideBudget)
	if err != nil || firstAside["status"] != "injected" {
		t.Fatalf("first aside send=%#v err=%v", firstAside, err)
	}
	retryAside, err := app.sendKernelChildMessage(context.Background(), asideAccess, "main", "aside\tmessage 0", "question", "aside-0", asideBudget)
	if err != nil || retryAside["status"] != "injected" {
		t.Fatalf("idempotent aside retry=%#v err=%v", retryAside, err)
	}
	for index := 1; index < 8; index++ {
		result, err := app.sendKernelChildMessage(context.Background(), asideAccess, "main", fmt.Sprintf("aside\tmessage %d", index), "question", fmt.Sprintf("aside-%d", index), asideBudget)
		if err != nil || result["status"] != "injected" || result["main_status"] != "processing" {
			t.Fatalf("aside send %d=%#v err=%v", index, result, err)
		}
	}
	asideNotifications, err := store.ClaimUnreadNotifications(context.Background(), identity.access.Frame.ID, identity.access.Frame.RootFrameID, identity.access.UserID, "aside-budget-claim", 100)
	if err != nil || len(asideNotifications) != 9 {
		t.Fatalf("aside notifications len=%d err=%v", len(asideNotifications), err)
	}
	if err := app.ackAgentKernelNotificationClaim(context.Background(), identity.access.Frame.ID, "aside-budget-claim", len(asideNotifications)); err != nil {
		t.Fatal(err)
	}
	postAckAside, err := app.sendKernelChildMessage(context.Background(), asideAccess, "main", "aside\tmessage 0", "question", "aside-0", asideBudget)
	if err != nil || postAckAside["status"] != "injected" {
		t.Fatalf("post-ack aside retry=%#v err=%v", postAckAside, err)
	}
	app.kernelPeerPendingMu.Lock()
	postAckAsidePending := app.kernelPeerPending[identity.access.Frame.ID]
	app.kernelPeerPendingMu.Unlock()
	if postAckAsidePending != 0 {
		t.Fatalf("post-ack aside retry recreated pending reservation: %d", postAckAsidePending)
	}
	asideCap, err := app.sendKernelChildMessage(context.Background(), asideAccess, identity.access.Frame.ID, "ninth", "info", "aside-8", asideBudget)
	if err != nil || asideCap["status"] != "refused" || !strings.Contains(stringValue(asideCap["error"]), "cap reached (8") {
		t.Fatalf("aside cap=%#v err=%v", asideCap, err)
	}
	tooLong, err := app.sendKernelChildMessage(context.Background(), asideAccess, "main", strings.Repeat("😀", 8193), "info", "aside-long", &kernelMessageBudgets{})
	if err != nil || tooLong["status"] != "refused" || !strings.Contains(stringValue(tooLong["error"]), "16386 chars; max 16384") {
		t.Fatalf("aside length=%#v err=%v", tooLong, err)
	}
}

func decodeLastKernelJSONLine(t *testing.T, stdout string) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	var value map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &value); err != nil {
		t.Fatalf("decode kernel JSON: %v stdout=%q", err, stdout)
	}
	return value
}

func kernelFrameEventsContain(events []workspace.FrameEvent, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

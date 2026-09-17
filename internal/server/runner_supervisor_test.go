package server

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	sessionstore "synon-go/internal/persistence/sessions"
)

func TestSessionRunnerHeartbeatIntervalRenewsBeforeShortLeaseBoundary(t *testing.T) {
	if got := sessionRunnerHeartbeatInterval(100 * time.Millisecond); got != 25*time.Millisecond {
		t.Fatalf("100ms heartbeat interval = %s, want 25ms", got)
	}
	if got := sessionRunnerHeartbeatInterval(2 * time.Millisecond); got <= 0 || got >= 2*time.Millisecond {
		t.Fatalf("2ms heartbeat interval = %s, want a positive interval below the lease TTL", got)
	}
	if got := sessionRunnerHeartbeatInterval(5 * time.Minute); got != 30*time.Second {
		t.Fatalf("default-scale heartbeat interval = %s, want 30s cap", got)
	}
}

func TestSessionRunnerCommandOnceExecutesRealExternalWorker(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	allowPairingForTest(t, srv, "feishu", "ou_runner_supervisor")
	httpServer := httptestServer(t, srv)

	first := postFeishuEvent(t, httpServer.URL, []byte(`{
		"schema": "2.0",
		"header": {"event_id": "evt-runner-supervisor-1", "event_type": "im.message.receive_v1"},
		"event": {
			"sender": {"sender_id": {"open_id": "ou_runner_supervisor"}},
			"message": {
				"message_id": "om_runner_supervisor_1",
				"chat_id": "oc_runner_supervisor",
				"chat_type": "p2p",
				"message_type": "text",
				"content": "{\"text\":\"run supervised work\"}"
			}
		}
	}`))
	sessionID := first["sessionId"].(string)

	helperPath := writeRunnerWorkerHelper(t)
	result, err := srv.RunSessionRunnerCommandOnce(context.Background(), SessionRunnerCommandOptions{
		RunnerID:         "worker-a",
		Command:          goToolPath(t),
		Args:             []string{"run", helperPath},
		LeaseTTL:         time.Minute,
		ReplayLimit:      20,
		OutputLimitBytes: 64 * 1024,
	})
	if err != nil {
		t.Fatalf("RunSessionRunnerCommandOnce() error = %v", err)
	}
	if !result.Claimed || result.SessionID != sessionID || result.Status != "completed" || result.AssistantEventID == 0 || result.FinishEventID == 0 {
		t.Fatalf("runner result = %+v", result)
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
	if !hasJournalEntry(entries, "message", "", "worker processed: run supervised work") {
		t.Fatalf("missing worker assistant message: %#v", entries)
	}
	if !hasJournalEntry(entries, "runner_finished", "completed", "") {
		t.Fatalf("missing runner finished entry: %#v", entries)
	}

	second, err := srv.RunSessionRunnerCommandOnce(context.Background(), SessionRunnerCommandOptions{
		RunnerID:    "worker-a",
		Command:     goToolPath(t),
		Args:        []string{"run", helperPath},
		LeaseTTL:    time.Minute,
		ReplayLimit: 20,
	})
	if err != nil {
		t.Fatalf("second RunSessionRunnerCommandOnce() error = %v", err)
	}
	if second.Claimed {
		t.Fatalf("completed assistant session should not be pending again: %+v", second)
	}
}

func TestSessionRunnerCommandOnceAutoAdvancesTaskRun(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	httpServer := httptestServer(t, srv)

	started := postToolInput(t, httpServer.URL, "TaskRun", map[string]any{
		"action":           "start",
		"objective":        "Prepare runner auto advance release handoff",
		"success_criteria": []any{"first step completes", "next explicit graph step is queued automatically"},
		"constraints":      []any{"do not run full test suite"},
		"task_graph":       explicitTaskRunTestGraph("execute", "verify", "review"),
	})
	startedRun := started["result"].(map[string]any)
	runID := startedRun["run_id"].(string)
	sessionID := startedRun["session_id"].(string)
	if startedRun["status"] != "running" || runID == "" || !strings.HasPrefix(sessionID, "taskrun:") {
		t.Fatalf("started TaskRun = %#v", startedRun)
	}

	helperPath := writeRunnerWorkerHelper(t)
	first, err := srv.RunSessionRunnerCommandOnce(context.Background(), SessionRunnerCommandOptions{
		RunnerID:         "taskrun-worker",
		Command:          goToolPath(t),
		Args:             []string{"run", helperPath},
		LeaseTTL:         time.Minute,
		ReplayLimit:      20,
		OutputLimitBytes: 64 * 1024,
	})
	if err != nil {
		t.Fatalf("first RunSessionRunnerCommandOnce() error = %v", err)
	}
	if !first.Claimed || first.SessionID != sessionID || first.Status != "completed" || !first.AutoAdvanced {
		t.Fatalf("first runner result = %+v", first)
	}

	status := postToolInput(t, httpServer.URL, "TaskRun", map[string]any{"action": "status", "run_id": runID})
	statusRun := status["result"].(map[string]any)
	active := statusRun["active_children"].([]any)
	if statusRun["status"] != "running" || len(active) != 1 {
		t.Fatalf("TaskRun after auto advance = %#v", statusRun)
	}
	if stepID := active[0].(map[string]any)["step_id"]; stepID != "verify" {
		t.Fatalf("auto-advanced active step = %#v", active[0])
	}
	steps := statusRun["steps"].([]any)
	if steps[0].(map[string]any)["status"] != "completed" || steps[1].(map[string]any)["status"] != "running" {
		t.Fatalf("TaskRun step states after auto advance = %#v", steps)
	}

	second, err := srv.RunSessionRunnerCommandOnce(context.Background(), SessionRunnerCommandOptions{
		RunnerID:         "taskrun-worker",
		Command:          goToolPath(t),
		Args:             []string{"run", helperPath},
		LeaseTTL:         time.Minute,
		ReplayLimit:      20,
		OutputLimitBytes: 64 * 1024,
	})
	if err != nil {
		t.Fatalf("second RunSessionRunnerCommandOnce() error = %v", err)
	}
	if !second.Claimed || second.SessionID != sessionID || second.Status != "completed" || !second.AutoAdvanced {
		t.Fatalf("second runner result = %+v", second)
	}
	status = postToolInput(t, httpServer.URL, "TaskRun", map[string]any{"action": "status", "run_id": runID})
	statusRun = status["result"].(map[string]any)
	active = statusRun["active_children"].([]any)
	if len(active) != 1 || active[0].(map[string]any)["step_id"] != "review" {
		t.Fatalf("TaskRun should continue through the caller-authored graph: %#v", statusRun)
	}
}

func TestSessionRunnerCommandOnceAutoQueuesTaskRunRepair(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	httpServer := httptestServer(t, srv)

	started := postToolInput(t, httpServer.URL, "TaskRun", map[string]any{
		"action":           "start",
		"objective":        "Prepare evidence repair handoff",
		"success_criteria": []any{"repair policy queues missing evidence"},
		"task_graph":       explicitTaskRunTestGraph("execute"),
	})
	startedRun := started["result"].(map[string]any)
	runID := startedRun["run_id"].(string)

	helperPath := writeRunnerWorkerNoEvidenceHelper(t)
	first, err := srv.RunSessionRunnerCommandOnce(context.Background(), SessionRunnerCommandOptions{
		RunnerID:         "taskrun-repair-worker",
		Command:          goToolPath(t),
		Args:             []string{"run", helperPath},
		LeaseTTL:         time.Minute,
		ReplayLimit:      20,
		OutputLimitBytes: 64 * 1024,
	})
	if err != nil {
		t.Fatalf("first RunSessionRunnerCommandOnce() error = %v", err)
	}
	if !first.Claimed || !first.AutoAdvanced {
		t.Fatalf("first runner result = %+v", first)
	}

	status := postToolInput(t, httpServer.URL, "TaskRun", map[string]any{"action": "status", "run_id": runID})
	statusRun := status["result"].(map[string]any)
	if statusRun["status"] != "running" || len(statusRun["active_children"].([]any)) != 1 {
		t.Fatalf("TaskRun after auto repair = %#v", statusRun)
	}
	repairPolicy := statusRun["repair_policy"].(map[string]any)
	if repairPolicy["status"] != "queued" || repairPolicy["rounds_attempted"] != float64(1) {
		t.Fatalf("repair policy = %#v", repairPolicy)
	}
	steps := statusRun["steps"].([]any)
	repair := steps[len(steps)-1].(map[string]any)
	if repair["intent"] != "repair" || repair["status"] != "running" {
		t.Fatalf("repair step = %#v", repair)
	}
}

func TestSessionRunnerCommandOnceTimesOutSlowExternalWorker(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	allowPairingForTest(t, srv, "feishu", "ou_runner_timeout")
	httpServer := httptestServer(t, srv)

	first := postFeishuEvent(t, httpServer.URL, []byte(`{
		"schema": "2.0",
		"header": {"event_id": "evt-runner-timeout-1", "event_type": "im.message.receive_v1"},
		"event": {
			"sender": {"sender_id": {"open_id": "ou_runner_timeout"}},
			"message": {
				"message_id": "om_runner_timeout_1",
				"chat_id": "oc_runner_timeout",
				"chat_type": "p2p",
				"message_type": "text",
				"content": "{\"text\":\"run slow supervised work\"}"
			}
		}
	}`))
	sessionID := first["sessionId"].(string)

	helperPath := writeSlowRunnerWorkerHelper(t)
	started := time.Now()
	result, err := srv.RunSessionRunnerCommandOnce(context.Background(), SessionRunnerCommandOptions{
		RunnerID:         "worker-timeout",
		Command:          goToolPath(t),
		Args:             []string{"run", helperPath},
		CommandTimeout:   80 * time.Millisecond,
		LeaseTTL:         time.Minute,
		ReplayLimit:      20,
		OutputLimitBytes: 64 * 1024,
	})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("RunSessionRunnerCommandOnce() error = %v", err)
	}
	if !result.Claimed || result.SessionID != sessionID || result.Status != "failed" || result.FinishEventID == 0 {
		t.Fatalf("runner result = %+v", result)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("runner did not respect command timeout, elapsed=%s", elapsed)
	}

	replayed := postToolInput(t, httpServer.URL, "session_replay", map[string]any{
		"sessionId":    sessionID,
		"afterEventId": float64(0),
		"limit":        float64(10),
	})
	entries := replayed["result"].(map[string]any)["entries"].([]any)
	if !hasJournalEntry(entries, "runner_finished", "failed", "timed out") {
		t.Fatalf("missing failed timeout finish entry: %#v", entries)
	}
}

func TestSessionRunnerCommandOnceRenewsLeaseDuringSlowExternalWorker(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	allowPairingForTest(t, srv, "feishu", "ou_runner_renew")
	httpServer := httptestServer(t, srv)

	first := postFeishuEvent(t, httpServer.URL, []byte(`{
		"schema": "2.0",
		"header": {"event_id": "evt-runner-renew-1", "event_type": "im.message.receive_v1"},
		"event": {
			"sender": {"sender_id": {"open_id": "ou_runner_renew"}},
			"message": {
				"message_id": "om_runner_renew_1",
				"chat_id": "oc_runner_renew",
				"chat_type": "p2p",
				"message_type": "text",
				"content": "{\"text\":\"run beyond lease ttl\"}"
			}
		}
	}`))
	sessionID := first["sessionId"].(string)

	helperPath := writeLeaseRenewalRunnerWorkerHelper(t)
	result, err := srv.RunSessionRunnerCommandOnce(context.Background(), SessionRunnerCommandOptions{
		RunnerID: "worker-renew",
		Command:  goToolPath(t),
		Args:     []string{"run", helperPath},
		// Keep the lease intentionally shorter than the worker, while leaving
		// enough headroom for process startup and repository-wide test load.
		CommandTimeout:   6 * time.Second,
		LeaseTTL:         2 * time.Second,
		ReplayLimit:      20,
		OutputLimitBytes: 64 * 1024,
	})
	if err != nil {
		t.Fatalf("RunSessionRunnerCommandOnce() error = %v", err)
	}
	if !result.Claimed || result.SessionID != sessionID || result.Status != "completed" || result.FinishEventID == 0 {
		t.Fatalf("runner result = %+v", result)
	}

	replayed := postToolInput(t, httpServer.URL, "session_replay", map[string]any{
		"sessionId":    sessionID,
		"afterEventId": float64(0),
		"limit":        float64(10),
	})
	entries := replayed["result"].(map[string]any)["entries"].([]any)
	if !hasJournalEntry(entries, "runner_finished", "completed", "lease renewed") {
		t.Fatalf("missing completed finish entry after lease renewal: %#v", entries)
	}
}

func TestSessionRunnerCommandOnceCancelsProcessTreeWhenLeaseIsLost(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	allowPairingForTest(t, srv, "feishu", "ou_runner_lease_loss")
	httpServer := httptestServer(t, srv)

	first := postFeishuEvent(t, httpServer.URL, []byte(`{
		"schema":"2.0",
		"header":{"event_id":"evt-runner-lease-loss","event_type":"im.message.receive_v1"},
		"event":{"sender":{"sender_id":{"open_id":"ou_runner_lease_loss"}},"message":{"message_id":"om_runner_lease_loss","chat_id":"oc_runner_lease_loss","chat_type":"p2p","message_type":"text","content":"{\"text\":\"cancel stale worker immediately\"}"}}
	}`))
	sessionID := first["sessionId"].(string)
	startedMarker := filepath.Join(t.TempDir(), "started")
	completedMarker := filepath.Join(t.TempDir(), "completed")
	helperPath := writeLeaseLossRunnerWorkerHelper(t)
	type outcome struct {
		result SessionRunnerCycleResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := srv.RunSessionRunnerCommandOnce(context.Background(), SessionRunnerCommandOptions{
			RunnerID:         "worker-lease-loss",
			Command:          goToolPath(t),
			Args:             []string{"run", helperPath, startedMarker, completedMarker},
			CommandTimeout:   5 * time.Second,
			LeaseTTL:         80 * time.Millisecond,
			ReplayLimit:      20,
			OutputLimitBytes: 64 * 1024,
		})
		done <- outcome{result: result, err: err}
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(startedMarker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("runner process did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	session, found, err := srv.sessionStore.Get(sessionID)
	if err != nil || !found || session.Runner == nil {
		t.Fatalf("get active runner: found=%v session=%#v err=%v", found, session, err)
	}
	if _, released, err := srv.sessionStore.ReleaseRunner(sessionstore.RunnerClaimFromSession(session)); err != nil || !released {
		t.Fatalf("release active runner: released=%v err=%v", released, err)
	}

	select {
	case got := <-done:
		if got.err == nil || !strings.Contains(got.err.Error(), "runner command lease") {
			t.Fatalf("runner result=%#v err=%v, want lease-loss error", got.result, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runner process was not cancelled after lease loss")
	}
	if _, err := os.Stat(completedMarker); !os.IsNotExist(err) {
		t.Fatalf("stale runner reached post-sleep side effect, stat err=%v", err)
	}
	replayed := postToolInput(t, httpServer.URL, "session_replay", map[string]any{
		"sessionId": sessionID, "afterEventId": float64(0), "limit": float64(20),
	})
	entries := replayed["result"].(map[string]any)["entries"].([]any)
	if !hasJournalEntry(entries, "runner_checkpoint", "running", "runner command started") {
		t.Fatalf("missing pre-loss runner checkpoint: %#v", entries)
	}
	if hasJournalEntry(entries, "runner_finished", "", "") || hasJournalEntry(entries, "message", "", "lease-loss helper completed") {
		t.Fatalf("stale runner wrote assistant or terminal state after losing lease: %#v", entries)
	}
}

func httptestServer(t *testing.T, srv *Server) *httptest.Server {
	t.Helper()
	httpServer := httptest.NewServer(srv.Handler())
	t.Cleanup(httpServer.Close)
	return httpServer
}

func writeRunnerWorkerHelper(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runner_worker_helper.go")
	source := `package main

import (
	"encoding/json"
	"fmt"
	"os"
)

type inputEnvelope struct {
	Session struct {
		ID string
	}
	Entries []struct {
		Message map[string]any
	}
}

func main() {
	var input inputEnvelope
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	lastUserText := ""
	for index := len(input.Entries) - 1; index >= 0; index-- {
		message := input.Entries[index].Message
		if role, _ := message["role"].(string); role != "user" {
			continue
		}
		if text, _ := message["text"].(string); text != "" {
			lastUserText = text
			break
		}
	}
	if lastUserText == "" {
		fmt.Fprintln(os.Stderr, "missing user text")
		os.Exit(3)
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"status":           "completed",
		"message":          "done " + input.Session.ID,
		"assistantMessage": "worker processed: " + lastUserText,
	})
}
`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeRunnerWorkerNoEvidenceHelper(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runner_worker_no_evidence_helper.go")
	source := `package main

import (
	"encoding/json"
	"os"
)

func main() {
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"status":  "completed",
		"message": "completed without durable assistant evidence",
	})
}
`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeSlowRunnerWorkerHelper(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "slow_runner_worker_helper.go")
	source := `package main

import (
	"encoding/json"
	"os"
	"time"
)

func main() {
	time.Sleep(2 * time.Second)
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"status": "completed",
		"message": "slow done",
	})
}
`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeLeaseRenewalRunnerWorkerHelper(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lease_renewal_runner_worker_helper.go")
	source := `package main

import (
	"encoding/json"
	"os"
	"time"
)

func main() {
	time.Sleep(3 * time.Second)
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"status": "completed",
		"message": "lease renewed",
	})
}
`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeLeaseLossRunnerWorkerHelper(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lease_loss_runner_worker_helper.go")
	source := `package main

import (
	"encoding/json"
	"os"
	"time"
)

func main() {
	if len(os.Args) != 3 {
		os.Exit(2)
	}
	if err := os.WriteFile(os.Args[1], []byte("started"), 0o600); err != nil {
		os.Exit(3)
	}
	time.Sleep(2 * time.Second)
	if err := os.WriteFile(os.Args[2], []byte("completed"), 0o600); err != nil {
		os.Exit(4)
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"status": "completed",
		"message": "lease-loss helper completed",
	})
}
`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func goToolPath(t *testing.T) string {
	t.Helper()
	name := "go"
	if runtime.GOOS == "windows" {
		name = "go.exe"
	}
	path := filepath.Join(runtime.GOROOT(), "bin", name)
	if _, err := os.Stat(path); err == nil {
		return path
	}
	return name
}

func hasJournalEntry(entries []any, entryType string, status string, textContains string) bool {
	for _, entry := range entries {
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		message, ok := item["message"].(map[string]any)
		if !ok {
			continue
		}
		if message["type"] != entryType {
			continue
		}
		if status != "" && message["status"] != status {
			continue
		}
		if textContains != "" {
			text, _ := message["text"].(string)
			if !strings.Contains(text, textContains) {
				continue
			}
		}
		return true
	}
	return false
}

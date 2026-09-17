package agentruntime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenAIChatClientRetriesTransientFailureWithBackoff(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "temporary", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	client := OpenAIChatClient{Endpoint: server.URL, Model: "retry-model", HTTPClient: server.Client(), MaxAttempts: 2}
	started := time.Now()
	response, err := client.Complete(t.Context(), ModelRequest{Messages: []Message{{Role: "user", Content: "continue"}}})
	if err != nil || response.Message.Content != "ok" {
		t.Fatalf("response=%#v error=%v", response, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d", calls.Load())
	}
	if elapsed := time.Since(started); elapsed < modelRetryBaseDelay/2 {
		t.Fatalf("retry happened without backoff: %s", elapsed)
	}
}

func TestModelRetryTimingMatchesV11Policy(t *testing.T) {
	if modelRetryBaseDelay != 800*time.Millisecond {
		t.Fatalf("modelRetryBaseDelay = %s, want 800ms", modelRetryBaseDelay)
	}
	if modelRetryMaxDelay != 6*time.Second {
		t.Fatalf("modelRetryMaxDelay = %s, want 6s", modelRetryMaxDelay)
	}
}

func TestOpenAIChatClientRetryWaitHonorsCancellation(t *testing.T) {
	var calls atomic.Int32
	firstCall := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		select {
		case firstCall <- struct{}{}:
		default:
		}
		w.Header().Set("Retry-After", "30")
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := OpenAIChatClient{Endpoint: server.URL, Model: "retry-model", HTTPClient: server.Client(), MaxAttempts: 3}
	completed := make(chan error, 1)
	go func() {
		_, err := client.Complete(ctx, ModelRequest{Messages: []Message{{Role: "user", Content: "continue"}}})
		completed <- err
	}()
	select {
	case <-firstCall:
		cancel()
	case <-time.After(2 * time.Second):
		t.Fatal("first model request did not reach the test server")
	}
	var err error
	select {
	case err = <-completed:
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not interrupt retry wait")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d; cancellation must stop replay", calls.Load())
	}
}

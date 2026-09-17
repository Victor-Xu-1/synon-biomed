package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
)

func TestRuntimeModelClientBacksOffBeforeRetry(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"recovered"}}]}`))
	}))
	defer server.Close()

	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "retry", Type: "openai", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL},
		Model:    "retry-model", Request: RequestProfile{MaxAttempts: 2},
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	response, err := client.Complete(t.Context(), agentruntime.ModelRequest{Messages: []agentruntime.Message{{Role: "user", Content: "continue"}}})
	if err != nil || response.Message.Content != "recovered" {
		t.Fatalf("response=%#v error=%v", response, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d", calls.Load())
	}
	if elapsed := time.Since(started); elapsed < providerRetryBaseDelay/2 {
		t.Fatalf("retry happened without backoff: %s", elapsed)
	}
}

func TestProviderRetryTimingMatchesV11Policy(t *testing.T) {
	if providerRetryBaseDelay != 800*time.Millisecond {
		t.Fatalf("providerRetryBaseDelay = %s, want 800ms", providerRetryBaseDelay)
	}
	if providerRetryMaxDelay != 6*time.Second {
		t.Fatalf("providerRetryMaxDelay = %s, want 6s", providerRetryMaxDelay)
	}
}

func TestRuntimeModelClientRetryWaitHonorsCancellation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "30")
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer server.Close()

	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "retry", Type: "openai", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL},
		Model:    "retry-model", Request: RequestProfile{MaxAttempts: 3},
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = client.Complete(ctx, agentruntime.ModelRequest{Messages: []agentruntime.Message{{Role: "user", Content: "continue"}}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d; cancellation must stop replay", calls.Load())
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("cancellation took too long: %s", elapsed)
	}
}

func TestParseProviderRetryAfterCapsServerDelay(t *testing.T) {
	if got := parseProviderRetryAfter("120", time.Now()); got != providerRetryAfterCap {
		t.Fatalf("retry delay = %s", got)
	}
}
func TestRuntimeModelClientDoesNotReplayExhaustedQuota(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"AccountQuotaExceeded","message":"usage quota exhausted"}}`))
	}))
	defer server.Close()
	client, err := NewRuntimeModelClient(ModelProfile{
		Provider: ProviderProfile{ID: "quota", Type: "openai", Protocol: ProtocolOpenAICompatible, Endpoint: server.URL},
		Model:    "quota-model", Request: RequestProfile{MaxAttempts: 3},
	}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Complete(t.Context(), agentruntime.ModelRequest{Messages: []agentruntime.Message{{Role: "user", Content: "continue"}}})
	if err == nil || calls.Load() != 1 {
		t.Fatalf("error=%v calls=%d; exhausted quota must stop replay", err, calls.Load())
	}
}

package compute

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunApprovedStopCallsLoopbackAndRejectsDrift(t *testing.T) {
	for _, key := range []string{"HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(key, "http://127.0.0.1:1")
	}
	t.Setenv("NO_PROXY", "example.invalid")
	t.Setenv("no_proxy", "example.invalid")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	registration := ManagedEndpointRegistration{Name: "fixture", URL: "http://127.0.0.1:9444/v1", Port: 9444, SkillName: "using-model-endpoint", StartScript: "start", StopScript: "/usr/bin/curl -fsS -X POST '" + server.URL + "'", LivePath: "/health"}
	result, err := RunApprovedStop(context.Background(), registration, ApprovedManagedEndpointHash(registration), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || result.Duration <= 0 {
		t.Fatalf("calls=%d result=%#v", calls.Load(), result)
	}
	drifted := registration
	drifted.StopScript = "exit 0"
	if _, err := RunApprovedStop(context.Background(), drifted, ApprovedManagedEndpointHash(registration), time.Second); err == nil || !strings.Contains(err.Error(), "approved hash") {
		t.Fatalf("drift err=%v", err)
	}
}

func TestRunApprovedStopBoundsOutputAndTimesOut(t *testing.T) {
	registration := ManagedEndpointRegistration{Name: "fixture", URL: "http://127.0.0.1:9444/v1", Port: 9444, SkillName: "using-model-endpoint", StartScript: "start", StopScript: "exec sleep 5", LivePath: "/health"}
	result, err := RunApprovedStop(context.Background(), registration, ApprovedManagedEndpointHash(registration), 100*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err=%v", err)
	}
	if len(result.Transcript) > maxTranscriptBytes {
		t.Fatalf("transcript bytes=%d", len(result.Transcript))
	}
}

func TestRunApprovedStartExecutesApprovedBytesAndUsesBoundedLoopbackReadiness(t *testing.T) {
	for _, key := range []string{"HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(key, "http://127.0.0.1:1")
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/health" {
			http.NotFound(w, request)
			return
		}
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	registration := ManagedEndpointRegistration{
		Name: "fixture", URL: server.URL + "/v1", Port: 25000, SkillName: "using-model-endpoint",
		StartScript: "printf 'started\\n'", StopScript: "true", LivePath: "/health",
	}
	result, err := RunApprovedStart(context.Background(), registration, ApprovedManagedEndpointHash(registration), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || !strings.Contains(result.Transcript, "started") || result.Duration <= 0 {
		t.Fatalf("calls=%d result=%#v", calls.Load(), result)
	}
	drifted := registration
	drifted.StartScript = "false"
	if _, err := RunApprovedStart(context.Background(), drifted, ApprovedManagedEndpointHash(registration), time.Second); err == nil || !strings.Contains(err.Error(), "approved hash") {
		t.Fatalf("drift err=%v", err)
	}
}

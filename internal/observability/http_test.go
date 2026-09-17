package observability

import (
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPRegistryCapturesBoundedRoutesStatusAndTraceIdentity(t *testing.T) {
	registry := NewHTTPRegistry()
	mux := http.NewServeMux()
	var inFlight int64
	mux.HandleFunc("/widgets/{id}", func(w http.ResponseWriter, _ *http.Request) {
		inFlight = registry.Snapshot().InFlight
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("created"))
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/implicit", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
		w.WriteHeader(http.StatusInternalServerError)
	})
	handler := registry.Wrap(mux)

	request := httptest.NewRequest(http.MethodPost, "/widgets/widget-123", nil)
	request.Header.Set("X-Request-ID", "request-123")
	request.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || inFlight != 1 {
		t.Fatalf("response=%d in_flight=%d", response.Code, inFlight)
	}
	if response.Header().Get("X-Request-ID") != "request-123" || response.Header().Get("X-Trace-ID") != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("identity headers=%v", response.Header())
	}

	implicit := httptest.NewRecorder()
	handler.ServeHTTP(implicit, httptest.NewRequest(http.MethodGet, "/implicit", nil))
	if implicit.Code != http.StatusOK {
		t.Fatalf("implicit status=%d", implicit.Code)
	}
	unknownRequest := httptest.NewRequest(http.MethodGet, "/api/private/123456", nil)
	unknownRequest.Header.Set("X-Request-ID", "invalid request id")
	unknown := httptest.NewRecorder()
	handler.ServeHTTP(unknown, unknownRequest)
	generatedID := unknown.Header().Get("X-Request-ID")
	if len(generatedID) != 32 {
		t.Fatalf("generated request id=%q", generatedID)
	}
	if _, err := hex.DecodeString(generatedID); err != nil {
		t.Fatalf("generated request id is not hex: %v", err)
	}

	snapshot := registry.Snapshot()
	if snapshot.InFlight != 0 || len(snapshot.Metrics) != 3 {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	assertHTTPMetric(t, snapshot, http.MethodPost, "/widgets/{id}", http.StatusCreated)
	assertHTTPMetric(t, snapshot, http.MethodGet, "/implicit", http.StatusOK)
	assertHTTPMetric(t, snapshot, http.MethodGet, "/api/unmatched", http.StatusNotFound)
	metrics := registry.Prometheus()
	if !strings.Contains(metrics, `synon_http_requests_total{method="POST",route="/widgets/{id}",status="201"} 1`) ||
		!strings.Contains(metrics, "synon_http_requests_in_flight 0") {
		t.Fatalf("prometheus output:\n%s", metrics)
	}
}

func TestHTTPRegistryRejectsMalformedTraceparentAndBoundsUnmatchedRoutes(t *testing.T) {
	registry := NewHTTPRegistry()
	handler := registry.Wrap(http.NotFoundHandler())
	request := httptest.NewRequest(http.MethodGet, "/ws/session-specific-secret", nil)
	request.Header.Set("traceparent", "00-not-hex-00f067aa0ba902b7-01")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	traceID := response.Header().Get("X-Trace-ID")
	if len(traceID) != 32 {
		t.Fatalf("generated trace id=%q", traceID)
	}
	if _, err := hex.DecodeString(traceID); err != nil {
		t.Fatalf("generated trace id is not hex: %v", err)
	}
	assertHTTPMetric(t, registry.Snapshot(), http.MethodGet, "/ws/unmatched", http.StatusNotFound)
}

func assertHTTPMetric(t *testing.T, snapshot HTTPSnapshot, method, route string, status int) {
	t.Helper()
	for _, metric := range snapshot.Metrics {
		if metric.Method == method && metric.Route == route && metric.Status == status && metric.Requests == 1 && metric.DurationSeconds >= 0 {
			return
		}
	}
	t.Fatalf("metric method=%s route=%s status=%d not found in %#v", method, route, status, snapshot.Metrics)
}

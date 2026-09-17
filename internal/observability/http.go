package observability

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type HTTPMetric struct {
	Method          string  `json:"method"`
	Route           string  `json:"route"`
	Status          int     `json:"status"`
	Requests        uint64  `json:"requests"`
	DurationSeconds float64 `json:"durationSeconds"`
	MaxSeconds      float64 `json:"maxSeconds"`
}

type HTTPSnapshot struct {
	StartedAt time.Time    `json:"startedAt"`
	InFlight  int64        `json:"inFlight"`
	Metrics   []HTTPMetric `json:"metrics"`
}

type HTTPRegistry struct {
	startedAt time.Time
	inFlight  atomic.Int64
	mu        sync.Mutex
	metrics   map[httpMetricKey]*httpMetricValue
}

type httpMetricKey struct {
	method string
	route  string
	status int
}

type httpMetricValue struct {
	requests uint64
	duration time.Duration
	maximum  time.Duration
}

type statusRecorder struct {
	http.ResponseWriter
	status     int
	wroteFinal bool
}

func NewHTTPRegistry() *HTTPRegistry {
	return &HTTPRegistry{startedAt: time.Now().UTC(), metrics: map[httpMetricKey]*httpMetricValue{}}
}

func (r *HTTPRegistry) Wrap(next http.Handler) http.Handler {
	if r == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requestID := boundedRequestID(request.Header.Get("X-Request-ID"))
		if requestID == "" {
			requestID = randomHex(16)
		}
		traceID := traceIDFromHeader(request.Header.Get("traceparent"))
		if traceID == "" {
			traceID = randomHex(16)
		}
		w.Header().Set("X-Request-ID", requestID)
		w.Header().Set("X-Trace-ID", traceID)
		request.Header.Set("X-Synon-Request-ID", requestID)
		request.Header.Set("X-Synon-Trace-ID", traceID)
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		started := time.Now()
		r.inFlight.Add(1)
		defer func() {
			r.inFlight.Add(-1)
			r.observe(request.Method, request.Pattern, request.URL.Path, recorder.status, time.Since(started))
		}()
		next.ServeHTTP(recorder, request)
	})
}

func (w *statusRecorder) WriteHeader(status int) {
	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if w.wroteFinal {
		return
	}
	w.wroteFinal = true
	if status >= 100 && status <= 999 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(value []byte) (int, error) {
	if !w.wroteFinal {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(value)
}

func (w *statusRecorder) Flush() {
	if !w.wroteFinal {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *statusRecorder) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hijacker.Hijack()
}

func (w *statusRecorder) Push(target string, options *http.PushOptions) error {
	pusher, ok := w.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, options)
}

func (r *HTTPRegistry) observe(method, pattern, path string, status int, duration time.Duration) {
	key := httpMetricKey{method: strings.ToUpper(strings.TrimSpace(method)), route: boundedRoute(pattern, path), status: status}
	r.mu.Lock()
	metric := r.metrics[key]
	if metric == nil {
		metric = &httpMetricValue{}
		r.metrics[key] = metric
	}
	metric.requests++
	metric.duration += duration
	if duration > metric.maximum {
		metric.maximum = duration
	}
	r.mu.Unlock()
}

func (r *HTTPRegistry) Snapshot() HTTPSnapshot {
	if r == nil {
		return HTTPSnapshot{Metrics: []HTTPMetric{}}
	}
	r.mu.Lock()
	metrics := make([]HTTPMetric, 0, len(r.metrics))
	for key, value := range r.metrics {
		metrics = append(metrics, HTTPMetric{
			Method: key.method, Route: key.route, Status: key.status, Requests: value.requests,
			DurationSeconds: value.duration.Seconds(), MaxSeconds: value.maximum.Seconds(),
		})
	}
	r.mu.Unlock()
	sort.Slice(metrics, func(i, j int) bool {
		if metrics[i].Route != metrics[j].Route {
			return metrics[i].Route < metrics[j].Route
		}
		if metrics[i].Method != metrics[j].Method {
			return metrics[i].Method < metrics[j].Method
		}
		return metrics[i].Status < metrics[j].Status
	})
	return HTTPSnapshot{StartedAt: r.startedAt, InFlight: r.inFlight.Load(), Metrics: metrics}
}

func (r *HTTPRegistry) Prometheus() string {
	snapshot := r.Snapshot()
	var builder strings.Builder
	builder.WriteString("# HELP synon_http_requests_total Completed HTTP requests.\n")
	builder.WriteString("# TYPE synon_http_requests_total counter\n")
	for _, metric := range snapshot.Metrics {
		labels := prometheusLabels(metric)
		fmt.Fprintf(&builder, "synon_http_requests_total%s %d\n", labels, metric.Requests)
	}
	builder.WriteString("# HELP synon_http_request_duration_seconds HTTP request duration.\n")
	builder.WriteString("# TYPE synon_http_request_duration_seconds summary\n")
	for _, metric := range snapshot.Metrics {
		labels := prometheusLabels(metric)
		fmt.Fprintf(&builder, "synon_http_request_duration_seconds_sum%s %s\n", labels, strconv.FormatFloat(metric.DurationSeconds, 'f', 6, 64))
		fmt.Fprintf(&builder, "synon_http_request_duration_seconds_count%s %d\n", labels, metric.Requests)
		fmt.Fprintf(&builder, "synon_http_request_duration_seconds_max%s %s\n", labels, strconv.FormatFloat(metric.MaxSeconds, 'f', 6, 64))
	}
	builder.WriteString("# HELP synon_http_requests_in_flight Current HTTP requests.\n")
	builder.WriteString("# TYPE synon_http_requests_in_flight gauge\n")
	fmt.Fprintf(&builder, "synon_http_requests_in_flight %d\n", snapshot.InFlight)
	builder.WriteString("# HELP synon_process_start_time_seconds Process observability start time.\n")
	builder.WriteString("# TYPE synon_process_start_time_seconds gauge\n")
	fmt.Fprintf(&builder, "synon_process_start_time_seconds %d\n", snapshot.StartedAt.Unix())
	return builder.String()
}

func prometheusLabels(metric HTTPMetric) string {
	return fmt.Sprintf(`{method="%s",route="%s",status="%d"}`, escapeLabel(metric.Method), escapeLabel(metric.Route), metric.Status)
}

func escapeLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return strings.ReplaceAll(value, `"`, `\"`)
}

func boundedRoute(pattern, path string) string {
	pattern = strings.TrimSpace(pattern)
	if pattern != "" {
		if len(pattern) > 256 {
			return pattern[:256]
		}
		return pattern
	}
	if path == "/health" || path == "/login" || path == "/logout" || path == "/metrics" {
		return path
	}
	if strings.HasPrefix(path, "/api/") {
		return "/api/unmatched"
	}
	if strings.HasPrefix(path, "/ws/") {
		return "/ws/unmatched"
	}
	return "/unmatched"
}

func boundedRequestID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return ""
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return ""
		}
	}
	return value
}

func traceIDFromHeader(value string) string {
	parts := strings.Split(strings.TrimSpace(value), "-")
	if len(parts) != 4 || len(parts[1]) != 32 || parts[1] == strings.Repeat("0", 32) {
		return ""
	}
	if _, err := hex.DecodeString(parts[1]); err != nil {
		return ""
	}
	return strings.ToLower(parts[1])
}

func randomHex(bytes int) string {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return fmt.Sprintf("%032x", time.Now().UnixNano())
	}
	return hex.EncodeToString(value)
}

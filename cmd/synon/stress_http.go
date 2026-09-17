package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type stressConfig struct {
	BaseURL     string
	Requests    int
	Concurrency int
	Timeout     time.Duration
}

type stressStats struct {
	StartedAt     time.Time        `json:"startedAt"`
	DurationMS    int64            `json:"durationMs"`
	BaseURL       string           `json:"baseUrl"`
	Requests      int              `json:"requests"`
	Concurrency   int              `json:"concurrency"`
	HTTPRequests  int64            `json:"httpRequests"`
	Failures      int64            `json:"failures"`
	RPS           float64          `json:"rps"`
	LatencyP50MS  float64          `json:"latencyP50Ms"`
	LatencyP95MS  float64          `json:"latencyP95Ms"`
	LatencyP99MS  float64          `json:"latencyP99Ms"`
	StatusBuckets map[string]int64 `json:"statusBuckets"`
	ErrorSamples  []string         `json:"errorSamples,omitempty"`
	Workloads     map[string]int64 `json:"workloads"`
}

type stressRecorder struct {
	httpRequests int64
	failures     int64
	mu           sync.Mutex
	latencies    []time.Duration
	statuses     map[string]int64
	errors       []string
	workloads    map[string]int64
}

func runStressHTTP(args []string, out io.Writer) error {
	cfg, err := parseStressConfig(args)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: cfg.Timeout}
	recorder := &stressRecorder{
		statuses:  map[string]int64{},
		workloads: map[string]int64{},
	}
	started := time.Now().UTC()
	if err := runStressWorkload(context.Background(), client, cfg, recorder); err != nil {
		return err
	}
	duration := time.Since(started)
	stats := recorder.stats(started, duration, cfg)
	encoded, err := json.Marshal(stats)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "STRESS_HTTP=%s\n", encoded)
	if stats.Failures > 0 {
		return fmt.Errorf("stress-http failed: %d failed requests", stats.Failures)
	}
	return nil
}

func parseStressConfig(args []string) (stressConfig, error) {
	fs := flag.NewFlagSet("stress-http", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	baseURL := fs.String("base-url", "http://127.0.0.1:38080", "base Synon HTTP URL")
	requests := fs.Int("requests", 1000, "logical workload iterations")
	concurrency := fs.Int("concurrency", 32, "concurrent workers")
	timeout := fs.Duration("timeout", 5*time.Second, "per-request timeout")
	if err := fs.Parse(args); err != nil {
		return stressConfig{}, err
	}
	cleanBase, err := normalizeBaseURL(*baseURL)
	if err != nil {
		return stressConfig{}, err
	}
	if *requests <= 0 {
		return stressConfig{}, errors.New("requests must be greater than zero")
	}
	if *concurrency <= 0 {
		return stressConfig{}, errors.New("concurrency must be greater than zero")
	}
	if *timeout <= 0 {
		return stressConfig{}, errors.New("timeout must be greater than zero")
	}
	if *concurrency > *requests {
		*concurrency = *requests
	}
	return stressConfig{
		BaseURL:     cleanBase,
		Requests:    *requests,
		Concurrency: *concurrency,
		Timeout:     *timeout,
	}, nil
}

func normalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("base-url is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("base-url must use http or https")
	}
	if parsed.Host == "" {
		return "", errors.New("base-url host is required")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func runStressWorkload(ctx context.Context, client *http.Client, cfg stressConfig, recorder *stressRecorder) error {
	jobs := make(chan int)
	var workers sync.WaitGroup
	for worker := 0; worker < cfg.Concurrency; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for jobID := range jobs {
				runStressIteration(ctx, client, cfg.BaseURL, jobID, recorder)
			}
		}()
	}
	for i := 0; i < cfg.Requests; i++ {
		jobs <- i
	}
	close(jobs)
	workers.Wait()
	return nil
}

func runStressIteration(ctx context.Context, client *http.Client, baseURL string, jobID int, recorder *stressRecorder) {
	switch jobID % 5 {
	case 0:
		recorder.recordWorkload("health")
		stressGET(ctx, client, baseURL, "/health", recorder)
	case 1:
		recorder.recordWorkload("capabilities")
		stressGET(ctx, client, baseURL, "/api/plugins/synon/capabilities", recorder)
	case 2:
		recorder.recordWorkload("synon_link_capabilities")
		stressGET(ctx, client, baseURL, "/api/synon-link/capabilities", recorder)
	case 3:
		recorder.recordWorkload("tools")
		stressGET(ctx, client, baseURL, "/api/tools", recorder)
	default:
		recorder.recordWorkload("runtime_kv_cycle")
		key := fmt.Sprintf("item-%06d", jobID)
		value := map[string]any{"job": jobID, "source": "stress-http"}
		stressPOST(ctx, client, baseURL, "/api/tools/runtime_set/execute", map[string]any{"input": map[string]any{"namespace": "stress-http", "key": key, "value": value}}, recorder)
		stressPOST(ctx, client, baseURL, "/api/tools/runtime_get/execute", map[string]any{"input": map[string]any{"namespace": "stress-http", "key": key}}, recorder)
		stressPOST(ctx, client, baseURL, "/api/tools/runtime_delete/execute", map[string]any{"input": map[string]any{"namespace": "stress-http", "key": key}}, recorder)
	}
}

func stressGET(ctx context.Context, client *http.Client, baseURL string, path string, recorder *stressRecorder) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
	if err != nil {
		recorder.recordFailure(path, 0, 0, err)
		return
	}
	stressDo(client, req, path, recorder)
}

func stressPOST(ctx context.Context, client *http.Client, baseURL string, path string, body any, recorder *stressRecorder) {
	raw, err := json.Marshal(body)
	if err != nil {
		recorder.recordFailure(path, 0, 0, err)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+path, bytes.NewReader(raw))
	if err != nil {
		recorder.recordFailure(path, 0, 0, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	stressDo(client, req, path, recorder)
}

func stressDo(client *http.Client, req *http.Request, path string, recorder *stressRecorder) {
	started := time.Now()
	resp, err := client.Do(req)
	elapsed := time.Since(started)
	if err != nil {
		recorder.recordFailure(path, 0, elapsed, err)
		return
	}
	defer resp.Body.Close()
	_, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, 2*1024*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		recorder.recordFailure(path, resp.StatusCode, elapsed, fmt.Errorf("status %d", resp.StatusCode))
		return
	}
	if readErr != nil {
		recorder.recordFailure(path, resp.StatusCode, elapsed, readErr)
		return
	}
	recorder.recordSuccess(resp.StatusCode, elapsed)
}

func (r *stressRecorder) recordWorkload(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.workloads[name]++
}

func (r *stressRecorder) recordSuccess(status int, latency time.Duration) {
	atomic.AddInt64(&r.httpRequests, 1)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statuses[fmt.Sprintf("%d", status)]++
	r.latencies = append(r.latencies, latency)
}

func (r *stressRecorder) recordFailure(path string, status int, latency time.Duration, err error) {
	atomic.AddInt64(&r.httpRequests, 1)
	atomic.AddInt64(&r.failures, 1)
	r.mu.Lock()
	defer r.mu.Unlock()
	if status > 0 {
		r.statuses[fmt.Sprintf("%d", status)]++
	}
	if latency > 0 {
		r.latencies = append(r.latencies, latency)
	}
	if len(r.errors) < 10 {
		r.errors = append(r.errors, fmt.Sprintf("%s: %v", path, err))
	}
}

func (r *stressRecorder) stats(started time.Time, duration time.Duration, cfg stressConfig) stressStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	latencies := append([]time.Duration(nil), r.latencies...)
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	statuses := make(map[string]int64, len(r.statuses))
	for status, count := range r.statuses {
		statuses[status] = count
	}
	workloads := make(map[string]int64, len(r.workloads))
	for workload, count := range r.workloads {
		workloads[workload] = count
	}
	httpRequests := atomic.LoadInt64(&r.httpRequests)
	rps := 0.0
	if duration > 0 {
		rps = float64(httpRequests) / duration.Seconds()
	}
	return stressStats{
		StartedAt:     started,
		DurationMS:    duration.Milliseconds(),
		BaseURL:       cfg.BaseURL,
		Requests:      cfg.Requests,
		Concurrency:   cfg.Concurrency,
		HTTPRequests:  httpRequests,
		Failures:      atomic.LoadInt64(&r.failures),
		RPS:           roundFloat(rps, 2),
		LatencyP50MS:  percentileMillis(latencies, 0.50),
		LatencyP95MS:  percentileMillis(latencies, 0.95),
		LatencyP99MS:  percentileMillis(latencies, 0.99),
		StatusBuckets: statuses,
		ErrorSamples:  append([]string(nil), r.errors...),
		Workloads:     workloads,
	}
}

func percentileMillis(values []time.Duration, percentile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	if percentile <= 0 {
		return roundFloat(float64(values[0].Microseconds())/1000, 2)
	}
	if percentile >= 1 {
		return roundFloat(float64(values[len(values)-1].Microseconds())/1000, 2)
	}
	index := int(math.Ceil(float64(len(values))*percentile)) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(values) {
		index = len(values) - 1
	}
	return roundFloat(float64(values[index].Microseconds())/1000, 2)
}

func roundFloat(value float64, places int) float64 {
	scale := math.Pow10(places)
	return math.Round(value*scale) / scale
}

package oracle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunHTTPScenarioCapturesRealOrderedRequestsAndResponses(t *testing.T) {
	var requests atomic.Int32
	newRuntime := func(agentOrder string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			requests.Add(1)
			response.Header().Set("Content-Type", "application/json; charset=utf-8")
			switch request.URL.Path {
			case "/api/agents":
				if request.Method != http.MethodGet {
					t.Fatalf("agent method = %s", request.Method)
				}
				if agentOrder == "baseline" {
					_, _ = response.Write([]byte("{\"requestId\":\"v1-random\",\"agents\":[{\"name\":\"OPERON\"},{\"name\":\"REVIEWER\"}]}"))
				} else {
					_, _ = response.Write([]byte("{\"agents\":[{\"name\":\"OPERON\"},{\"name\":\"REVIEWER\"}],\"requestId\":\"go-random\"}"))
				}
			case "/api/projects":
				if request.Method != http.MethodPost {
					t.Fatalf("project method = %s", request.Method)
				}
				var body map[string]any
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Fatalf("decode project body: %v", err)
				}
				if body["name"] != "Oracle fixture" {
					t.Fatalf("project body = %#v", body)
				}
				response.WriteHeader(http.StatusCreated)
				if agentOrder == "baseline" {
					_, _ = response.Write([]byte("{\"id\":\"v1-project\",\"name\":\"Oracle fixture\"}"))
				} else {
					_, _ = response.Write([]byte("{\"name\":\"Oracle fixture\",\"id\":\"go-project\"}"))
				}
			default:
				http.NotFound(response, request)
			}
		}))
	}
	baselineServer := newRuntime("baseline")
	defer baselineServer.Close()
	candidateServer := newRuntime("candidate")
	defer candidateServer.Close()

	spec := ScenarioSpec{
		SchemaVersion: 1,
		ID:            "agents-and-project-create",
		Contracts: []ContractRef{
			{Kind: ServiceContract, Name: "getAgents"},
			{Kind: ServiceContract, Name: "createProject"},
		},
		Steps: []HTTPStep{
			{Name: "list-agents", Method: http.MethodGet, Path: "/api/agents", ExpectedStatus: []int{http.StatusOK}},
			{Name: "create-project", Method: http.MethodPost, Path: "/api/projects", Body: json.RawMessage("{\"name\":\"Oracle fixture\"}"), ExpectedStatus: []int{http.StatusCreated}},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	baseline, err := RunHTTPScenario(ctx, baselineServer.Client(), baselineServer.URL, "synonbiomed-v1.1", spec)
	if err != nil {
		t.Fatalf("RunHTTPScenario(baseline) error = %v", err)
	}
	candidate, err := RunHTTPScenario(ctx, candidateServer.Client(), candidateServer.URL, "synon-go-v4.0.2", spec)
	if err != nil {
		t.Fatalf("RunHTTPScenario(candidate) error = %v", err)
	}
	if requests.Load() != 4 || len(baseline.Steps) != 2 || len(candidate.Steps) != 2 {
		t.Fatalf("requests=%d baseline=%+v candidate=%+v", requests.Load(), baseline, candidate)
	}

	baselineJSON, err := MarshalCapture(baseline)
	if err != nil {
		t.Fatal(err)
	}
	candidateJSON, err := MarshalCapture(candidate)
	if err != nil {
		t.Fatal(err)
	}
	err = CompareJSON(baselineJSON, candidateJSON, []string{
		"/runtime",
		"/steps/0/response/body/requestId",
		"/steps/1/response/body/id",
	})
	if err != nil {
		t.Fatalf("CompareJSON(real captures) error = %v\nbaseline=%s\ncandidate=%s", err, baselineJSON, candidateJSON)
	}
}

func TestRunHTTPScenarioRejectsUnknownContractRemoteHostAndBadStatus(t *testing.T) {
	spec := ScenarioSpec{
		SchemaVersion: 1,
		ID:            "invalid",
		Contracts:     []ContractRef{{Kind: ServiceContract, Name: "method-1"}},
		Steps:         []HTTPStep{{Name: "health", Method: http.MethodGet, Path: "/health", ExpectedStatus: []int{http.StatusOK}}},
	}
	_, err := RunHTTPScenario(context.Background(), http.DefaultClient, "http://127.0.0.1:1", "runtime", spec)
	if !errors.Is(err, ErrInvalidScenario) || !strings.Contains(err.Error(), "unknown service") {
		t.Fatalf("RunHTTPScenario(unknown contract) error = %v", err)
	}

	spec.Contracts = []ContractRef{{Kind: ServiceContract, Name: "healthCheck"}}
	_, err = RunHTTPScenario(context.Background(), http.DefaultClient, "https://example.com", "runtime", spec)
	if !errors.Is(err, ErrInvalidScenario) || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("RunHTTPScenario(remote) error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusTeapot)
		_, _ = response.Write([]byte("{\"error\":\"teapot\"}"))
	}))
	defer server.Close()
	_, err = RunHTTPScenario(context.Background(), server.Client(), server.URL, "runtime", spec)
	if !errors.Is(err, ErrScenarioFailed) || !strings.Contains(err.Error(), "got 418") {
		t.Fatalf("RunHTTPScenario(status) error = %v", err)
	}
}

func TestRunHTTPScenarioBoundsResponseAndRejectsInvalidBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "text/plain")
		_, _ = response.Write([]byte(strings.Repeat("x", 2048)))
	}))
	defer server.Close()
	spec := ScenarioSpec{
		SchemaVersion: 1,
		ID:            "bounded",
		Contracts:     []ContractRef{{Kind: ServiceContract, Name: "healthCheck"}},
		Steps:         []HTTPStep{{Name: "health", Method: http.MethodGet, Path: "/health", ExpectedStatus: []int{http.StatusOK}}},
		MaxBodyBytes:  128,
	}
	_, err := RunHTTPScenario(context.Background(), server.Client(), server.URL, "runtime", spec)
	if !errors.Is(err, ErrScenarioFailed) || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("RunHTTPScenario(oversized) error = %v", err)
	}

	spec.Steps[0].Method = http.MethodPost
	spec.Steps[0].Body = json.RawMessage("{")
	_, err = RunHTTPScenario(context.Background(), server.Client(), server.URL, "runtime", spec)
	if !errors.Is(err, ErrInvalidScenario) || !strings.Contains(err.Error(), "body") {
		t.Fatalf("RunHTTPScenario(invalid body) error = %v", err)
	}

	spec.Steps[0].Body = json.RawMessage("{}")
	spec.Steps[0].Headers = map[string]string{"Bad Header": "value"}
	_, err = RunHTTPScenario(context.Background(), server.Client(), server.URL, "runtime", spec)
	if !errors.Is(err, ErrInvalidScenario) || !strings.Contains(err.Error(), "header") {
		t.Fatalf("RunHTTPScenario(invalid header) error = %v", err)
	}
}

func TestRunHTTPScenarioCarriesCSRFContextWithoutPersistingSecrets(t *testing.T) {
	const token = "runtime-secret-token"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/csrf":
			if request.Header.Get("Origin") != "http://"+request.Host {
				t.Fatalf("csrf origin = %q", request.Header.Get("Origin"))
			}
			http.SetCookie(response, &http.Cookie{Name: "operon_csrf", Value: token, Path: "/"})
			response.WriteHeader(http.StatusNoContent)
		case "/api/agents":
			cookie, err := request.Cookie("operon_csrf")
			if err != nil || cookie.Value != token || request.Header.Get("X-Operon-CSRF") != token {
				t.Fatalf("csrf request cookie=%#v err=%v header=%q", cookie, err, request.Header.Get("X-Operon-CSRF"))
			}
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	spec := ScenarioSpec{
		SchemaVersion: 1,
		ID:            "csrf-context",
		Contracts:     []ContractRef{{Kind: ServiceContract, Name: "createAgent"}},
		Steps: []HTTPStep{
			{Name: "csrf", Method: http.MethodGet, Path: "/api/csrf", Headers: map[string]string{"Origin": "${origin}"}, ExpectedStatus: []int{http.StatusNoContent}},
			{Name: "create", Method: http.MethodPost, Path: "/api/agents", Headers: map[string]string{"Origin": "${origin}", "X-Operon-CSRF": "${cookie:operon_csrf}"}, Body: json.RawMessage(`{}`), ExpectedStatus: []int{http.StatusOK}},
		},
	}
	capture, err := RunHTTPScenario(context.Background(), server.Client(), server.URL, "runtime", spec)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := MarshalCapture(capture)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(token)) || bytes.Contains(encoded, []byte("operon_csrf")) {
		t.Fatalf("capture persisted CSRF material: %s", encoded)
	}
}

func TestRunHTTPScenarioResolvesCapturedResponseValuesInLaterRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/projects":
			if request.Method != http.MethodPost {
				t.Fatalf("project method = %s", request.Method)
			}
			response.WriteHeader(http.StatusCreated)
			_, _ = response.Write([]byte(`{"id":"project/alpha","metadata":{"owner":"local"}}`))
		case "/api/frames":
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatalf("decode frame body: %v", err)
			}
			if body["project_id"] != "project/alpha" || body["owner"] != "local" {
				t.Fatalf("resolved frame body = %#v", body)
			}
			response.WriteHeader(http.StatusCreated)
			_, _ = response.Write([]byte(`{"id":"frame alpha"}`))
		case "/api/frames/frame alpha":
			if request.URL.EscapedPath() != "/api/frames/frame%20alpha" || request.URL.Query().Get("project") != "project/alpha" {
				t.Fatalf("resolved frame URL = path %q escaped %q query %q", request.URL.Path, request.URL.EscapedPath(), request.URL.RawQuery)
			}
			_, _ = response.Write([]byte(`{"id":"frame alpha","project_id":"project/alpha"}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	spec := ScenarioSpec{
		SchemaVersion: 1,
		ID:            "captured-lifecycle",
		Contracts:     []ContractRef{{Kind: ServiceContract, Name: "createProject"}, {Kind: ServiceContract, Name: "createFrame"}, {Kind: ServiceContract, Name: "getFrame"}},
		Steps: []HTTPStep{
			{Name: "create-project", Method: http.MethodPost, Path: "/api/projects", Body: json.RawMessage(`{"name":"Fixture"}`), ExpectedStatus: []int{http.StatusCreated}, Capture: map[string]string{"projectId": "/id", "owner": "/metadata/owner"}},
			{Name: "create-frame", Method: http.MethodPost, Path: "/api/frames", Body: json.RawMessage(`{"project_id":"${capture:projectId}","owner":"${capture:owner}"}`), ExpectedStatus: []int{http.StatusCreated}, Capture: map[string]string{"frameId": "/id"}},
			{Name: "get-frame", Method: http.MethodGet, Path: "/api/frames/${capture:frameId}?project=${capture:projectId}", ExpectedStatus: []int{http.StatusOK}},
		},
	}
	capture, err := RunHTTPScenario(context.Background(), server.Client(), server.URL, "runtime", spec)
	if err != nil {
		t.Fatal(err)
	}
	if got := capture.Steps[1].Request.Body.(map[string]any); got["project_id"] != "project/alpha" || got["owner"] != "local" {
		t.Fatalf("captured resolved request body = %#v", got)
	}
	if capture.Steps[2].Request.Path != "/api/frames/frame%20alpha?project=project%2Falpha" {
		t.Fatalf("captured resolved request path = %q", capture.Steps[2].Request.Path)
	}
}

func TestRunHTTPScenarioRejectsInvalidOrForwardCaptureReferences(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"id":"value"}`))
	}))
	defer server.Close()

	base := ScenarioSpec{
		SchemaVersion: 1,
		ID:            "invalid-capture",
		Contracts:     []ContractRef{{Kind: ServiceContract, Name: "getFrame"}},
		Steps: []HTTPStep{{
			Name: "get", Method: http.MethodGet, Path: "/api/frames/${capture:later}",
			ExpectedStatus: []int{http.StatusOK}, Capture: map[string]string{"later": "/id"},
		}},
	}
	_, err := RunHTTPScenario(context.Background(), server.Client(), server.URL, "runtime", base)
	if !errors.Is(err, ErrInvalidScenario) || !strings.Contains(err.Error(), "before it is captured") {
		t.Fatalf("RunHTTPScenario(forward capture) error = %v", err)
	}

	base.Steps[0].Path = "/api/frames"
	base.Steps[0].Capture = map[string]string{"bad-name": "/id"}
	_, err = RunHTTPScenario(context.Background(), server.Client(), server.URL, "runtime", base)
	if !errors.Is(err, ErrInvalidScenario) || !strings.Contains(err.Error(), "capture variable") {
		t.Fatalf("RunHTTPScenario(invalid capture name) error = %v", err)
	}

	base.Steps[0].Capture = map[string]string{"frameId": "not-a-pointer"}
	_, err = RunHTTPScenario(context.Background(), server.Client(), server.URL, "runtime", base)
	if !errors.Is(err, ErrInvalidScenario) || !strings.Contains(err.Error(), "capture pointer") {
		t.Fatalf("RunHTTPScenario(invalid capture pointer) error = %v", err)
	}
}

func TestRunHTTPScenarioExecutesAndAwaitsRealConcurrentRequest(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/slow":
			close(started)
			<-release
			_, _ = response.Write([]byte(`{"state":"completed"}`))
		case "/mutate":
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("concurrent request did not start")
			}
			close(release)
			_, _ = response.Write([]byte(`{"state":"mutated"}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	spec := ScenarioSpec{
		SchemaVersion: 1,
		ID:            "concurrent-request",
		Contracts:     []ContractRef{{Kind: ServiceContract, Name: "healthCheck"}},
		Steps: []HTTPStep{
			{Name: "slow", Method: http.MethodPost, Path: "/slow", ExpectedStatus: []int{http.StatusOK}, Async: true, SettleMillis: 10},
			{Name: "mutate", Method: http.MethodPost, Path: "/mutate", ExpectedStatus: []int{http.StatusOK}},
			{Name: "await-slow", Await: "slow"},
		},
	}
	capture, err := RunHTTPScenario(context.Background(), server.Client(), server.URL, "runtime", spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(capture.Steps) != 2 || capture.Steps[0].Name != "slow" || capture.Steps[1].Name != "mutate" {
		t.Fatalf("captured asynchronous step order = %#v", capture.Steps)
	}
	if capture.Steps[0].Response.Status != http.StatusOK || capture.Steps[1].Response.Status != http.StatusOK {
		t.Fatalf("captured asynchronous responses = %#v", capture.Steps)
	}
}

func TestValidateScenarioRejectsInvalidAsyncLifecycle(t *testing.T) {
	validRequest := HTTPStep{Name: "slow", Method: http.MethodGet, Path: "/slow", ExpectedStatus: []int{http.StatusOK}, Async: true}
	base := func(steps ...HTTPStep) ScenarioSpec {
		return ScenarioSpec{
			SchemaVersion: 1,
			ID:            "invalid-async",
			Contracts:     []ContractRef{{Kind: ServiceContract, Name: "healthCheck"}},
			Steps:         steps,
		}
	}
	tests := []struct {
		name string
		spec ScenarioSpec
		want string
	}{
		{name: "unawaited", spec: base(validRequest), want: "not awaited"},
		{name: "unknown", spec: base(HTTPStep{Name: "await", Await: "missing"}), want: "unknown or later"},
		{name: "capture", spec: base(HTTPStep{Name: "slow", Method: http.MethodGet, Path: "/slow", ExpectedStatus: []int{http.StatusOK}, Async: true, Capture: map[string]string{"value": "/value"}}, HTTPStep{Name: "await", Await: "slow"}), want: "cannot capture"},
		{name: "duplicate await", spec: base(validRequest, HTTPStep{Name: "await-one", Await: "slow"}, HTTPStep{Name: "await-two", Await: "slow"}), want: "more than once"},
		{name: "request fields", spec: base(validRequest, HTTPStep{Name: "await", Await: "slow", Method: http.MethodGet}), want: "must not define"},
		{name: "sync settle", spec: base(HTTPStep{Name: "request", Method: http.MethodGet, Path: "/", ExpectedStatus: []int{http.StatusOK}, SettleMillis: 1}), want: "synchronous"},
		{name: "settle bound", spec: base(HTTPStep{Name: "slow", Method: http.MethodGet, Path: "/", ExpectedStatus: []int{http.StatusOK}, Async: true, SettleMillis: 5001}, HTTPStep{Name: "await", Await: "slow"}), want: "between 0 and 5000"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateScenario(test.spec)
			if !errors.Is(err, ErrInvalidScenario) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateScenario() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRunHTTPScenarioPollsRealIdempotentRequestUntilJSONCondition(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body["intent_id"] != "stable-intent" {
			t.Fatalf("poll request body=%#v err=%v", body, err)
		}
		response.Header().Set("Content-Type", "application/json")
		if attempts.Add(1) < 3 {
			_, _ = response.Write([]byte(`{"status":"message_queued"}`))
			return
		}
		_, _ = response.Write([]byte(`{"status":"already_delivered"}`))
	}))
	defer server.Close()
	spec := ScenarioSpec{
		SchemaVersion: 1, ID: "poll-idempotent-request",
		Contracts: []ContractRef{{Kind: ServiceContract, Name: "submitRequest"}},
		Steps: []HTTPStep{{
			Name: "poll", Method: http.MethodPost, Path: "/api/request",
			Body: json.RawMessage(`{"intent_id":"stable-intent"}`), ExpectedStatus: []int{http.StatusOK},
			Poll: &HTTPPoll{Pointer: "/status", Equals: "already_delivered", IntervalMillis: 5, TimeoutMillis: 100},
		}},
	}
	capture, err := RunHTTPScenario(context.Background(), server.Client(), server.URL, "runtime", spec)
	if err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 3 || capture.Steps[0].Response.Body.(map[string]any)["status"] != "already_delivered" {
		t.Fatalf("attempts=%d capture=%#v", attempts.Load(), capture)
	}
}

func TestRunHTTPScenarioPollTimeoutAndValidationAreBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"status":"message_queued"}`))
	}))
	defer server.Close()
	spec := ScenarioSpec{
		SchemaVersion: 1, ID: "poll-timeout",
		Contracts: []ContractRef{{Kind: ServiceContract, Name: "submitRequest"}},
		Steps: []HTTPStep{{
			Name: "poll", Method: http.MethodGet, Path: "/status", ExpectedStatus: []int{http.StatusOK},
			Poll: &HTTPPoll{Pointer: "/status", Equals: "done", IntervalMillis: 5, TimeoutMillis: 15},
		}},
	}
	_, err := RunHTTPScenario(context.Background(), server.Client(), server.URL, "runtime", spec)
	if !errors.Is(err, ErrScenarioFailed) || !strings.Contains(err.Error(), "poll timed out") {
		t.Fatalf("poll timeout error = %v", err)
	}
	spec.Steps[0].Poll.IntervalMillis = 0
	if err := validateScenario(spec); !errors.Is(err, ErrInvalidScenario) || !strings.Contains(err.Error(), "poll interval") {
		t.Fatalf("poll interval validation = %v", err)
	}
	spec.Steps[0].Poll = &HTTPPoll{Pointer: "bad", Equals: "done", IntervalMillis: 5, TimeoutMillis: 10}
	if err := validateScenario(spec); !errors.Is(err, ErrInvalidScenario) || !strings.Contains(err.Error(), "poll pointer") {
		t.Fatalf("poll pointer validation = %v", err)
	}
}

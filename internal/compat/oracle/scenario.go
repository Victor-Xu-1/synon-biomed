package oracle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	ErrInvalidScenario = errors.New("invalid behavior scenario")
	ErrScenarioFailed  = errors.New("behavior scenario failed")
)

const (
	defaultScenarioBodyLimit = int64(8 << 20)
	maximumScenarioBodyLimit = int64(64 << 20)
)

type ContractRef struct {
	Kind ContractKind `json:"kind"`
	Name string       `json:"name"`
}

type HTTPStep struct {
	Name           string            `json:"name"`
	Method         string            `json:"method"`
	Path           string            `json:"path"`
	Headers        map[string]string `json:"headers,omitempty"`
	Body           json.RawMessage   `json:"body,omitempty"`
	ExpectedStatus []int             `json:"expectedStatus"`
	Capture        map[string]string `json:"capture,omitempty"`
	Async          bool              `json:"async,omitempty"`
	Await          string            `json:"await,omitempty"`
	SettleMillis   int               `json:"settleMilliseconds,omitempty"`
	Poll           *HTTPPoll         `json:"poll,omitempty"`
}

type HTTPPoll struct {
	Pointer        string `json:"pointer"`
	Equals         any    `json:"equals"`
	IntervalMillis int    `json:"intervalMilliseconds"`
	TimeoutMillis  int    `json:"timeoutMilliseconds"`
}

type ScenarioSpec struct {
	SchemaVersion  int            `json:"schemaVersion"`
	ID             string         `json:"id"`
	Contracts      []ContractRef  `json:"contracts"`
	Steps          []HTTPStep     `json:"steps"`
	MaxBodyBytes   int64          `json:"maxBodyBytes,omitempty"`
	ComparisonMode ComparisonMode `json:"comparisonMode,omitempty"`
	Normalization  []string       `json:"normalization"`
	Redactions     []string       `json:"redactions"`
}

type RequestCapture struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Body   any    `json:"body,omitempty"`
}

type ResponseCapture struct {
	Status      int    `json:"status"`
	ContentType string `json:"contentType,omitempty"`
	Body        any    `json:"body,omitempty"`
}

type StepCapture struct {
	Name     string          `json:"name"`
	Request  RequestCapture  `json:"request"`
	Response ResponseCapture `json:"response"`
}

type ScenarioCapture struct {
	SchemaVersion int           `json:"schemaVersion"`
	Scenario      string        `json:"scenario"`
	Runtime       string        `json:"runtime"`
	Contracts     []ContractRef `json:"contracts"`
	Steps         []StepCapture `json:"steps"`
	redactions    []string
}

var (
	scenarioIDPattern       = regexp.MustCompile("^[a-z0-9][a-z0-9._-]*$")
	headerNamePattern       = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_|~-]+$")
	captureNamePattern      = regexp.MustCompile("^[A-Za-z][A-Za-z0-9_]*$")
	captureReferencePattern = regexp.MustCompile(`\$\{capture:([^}]*)\}`)
)

func RunHTTPScenario(
	ctx context.Context,
	client *http.Client,
	baseURL string,
	runtimeName string,
	spec ScenarioSpec,
) (ScenarioCapture, error) {
	scenarioCtx, cancelScenario := context.WithCancel(ctx)
	defer cancelScenario()
	if err := validateScenario(spec); err != nil {
		return ScenarioCapture{}, err
	}
	if strings.TrimSpace(runtimeName) == "" {
		return ScenarioCapture{}, fmt.Errorf("%w: runtime name is empty", ErrInvalidScenario)
	}
	base, err := validateLoopbackBaseURL(baseURL)
	if err != nil {
		return ScenarioCapture{}, err
	}
	if client == nil {
		client = http.DefaultClient
	}
	clientCopy := *client
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	if clientCopy.Jar == nil {
		jar, jarErr := cookiejar.New(nil)
		if jarErr != nil {
			return ScenarioCapture{}, fmt.Errorf("%w: initialize cookie jar: %v", ErrScenarioFailed, jarErr)
		}
		clientCopy.Jar = jar
	}

	bodyLimit := spec.MaxBodyBytes
	if bodyLimit == 0 {
		bodyLimit = defaultScenarioBodyLimit
	}
	capture := ScenarioCapture{
		SchemaVersion: 1,
		Scenario:      spec.ID,
		Runtime:       strings.TrimSpace(runtimeName),
		Contracts:     append([]ContractRef{}, spec.Contracts...),
		Steps:         make([]StepCapture, 0, len(spec.Steps)),
		redactions:    append([]string{}, spec.Redactions...),
	}
	capturedValues := make(map[string]any)
	type asyncResult struct {
		response ResponseCapture
		err      error
	}
	type pendingRequest struct {
		index  int
		result <-chan asyncResult
	}
	pending := make(map[string]pendingRequest)
	performRequest := func(step HTTPStep, request *http.Request) (ResponseCapture, error) {
		started := time.Now()
		attempt := 0
		for {
			attempt++
			attemptRequest, err := cloneScenarioRequest(request, attempt > 1)
			if err != nil {
				return ResponseCapture{}, fmt.Errorf("%w: step %q clone request: %v", ErrScenarioFailed, step.Name, err)
			}
			response, err := clientCopy.Do(attemptRequest)
			if err != nil {
				return ResponseCapture{}, fmt.Errorf("%w: step %q request: %v", ErrScenarioFailed, step.Name, err)
			}
			responseBody, readErr := readBoundedBody(response.Body, bodyLimit)
			closeErr := response.Body.Close()
			if readErr != nil {
				return ResponseCapture{}, fmt.Errorf("%w: step %q response: %v", ErrScenarioFailed, step.Name, readErr)
			}
			if closeErr != nil {
				return ResponseCapture{}, fmt.Errorf("%w: step %q close response: %v", ErrScenarioFailed, step.Name, closeErr)
			}
			if !expectedStatus(step.ExpectedStatus, response.StatusCode) {
				return ResponseCapture{}, fmt.Errorf(
					"%w: step %q got %d, want one of %v", ErrScenarioFailed, step.Name,
					response.StatusCode, step.ExpectedStatus,
				)
			}
			capture := ResponseCapture{
				Status: response.StatusCode, ContentType: normalizedContentType(response.Header.Get("Content-Type")),
				Body: decodeResponseBody(responseBody),
			}
			if step.Poll == nil {
				return capture, nil
			}
			value, found, lookupErr := lookupCapturePointer(capture.Body, step.Poll.Pointer)
			if lookupErr != nil {
				return ResponseCapture{}, fmt.Errorf("%w: step %q poll pointer: %v", ErrScenarioFailed, step.Name, lookupErr)
			}
			if found && equalJSONValue(value, step.Poll.Equals) {
				return capture, nil
			}
			if time.Since(started)+time.Duration(step.Poll.IntervalMillis)*time.Millisecond > time.Duration(step.Poll.TimeoutMillis)*time.Millisecond {
				return ResponseCapture{}, fmt.Errorf(
					"%w: step %q poll timed out after %d attempts; pointer %q last value=%v",
					ErrScenarioFailed, step.Name, attempt, step.Poll.Pointer, value,
				)
			}
			timer := time.NewTimer(time.Duration(step.Poll.IntervalMillis) * time.Millisecond)
			select {
			case <-scenarioCtx.Done():
				timer.Stop()
				return ResponseCapture{}, fmt.Errorf("%w: step %q poll: %v", ErrScenarioFailed, step.Name, scenarioCtx.Err())
			case <-timer.C:
			}
		}
	}

	for _, step := range spec.Steps {
		if step.Await != "" {
			background, exists := pending[step.Await]
			if !exists {
				return ScenarioCapture{}, fmt.Errorf("%w: step %q awaits unavailable request %q", ErrScenarioFailed, step.Name, step.Await)
			}
			result := <-background.result
			if result.err != nil {
				return ScenarioCapture{}, result.err
			}
			capture.Steps[background.index].Response = result.response
			delete(pending, step.Await)
			continue
		}
		resolvedPath, err := resolveCaptureURI(step.Path, capturedValues)
		if err != nil {
			return ScenarioCapture{}, fmt.Errorf("%w: step %q path: %v", ErrScenarioFailed, step.Name, err)
		}
		stepURL, err := base.Parse(resolvedPath)
		if err != nil {
			return ScenarioCapture{}, fmt.Errorf("%w: step %q URL: %v", ErrInvalidScenario, step.Name, err)
		}
		if !sameOrigin(base, stepURL) {
			return ScenarioCapture{}, fmt.Errorf("%w: step %q escaped base origin", ErrInvalidScenario, step.Name)
		}

		var requestBody io.Reader
		requestCaptureBody, err := decodeOptionalJSON(step.Body)
		if err != nil {
			return ScenarioCapture{}, fmt.Errorf("%w: step %q body: %v", ErrInvalidScenario, step.Name, err)
		}
		if len(step.Body) > 0 {
			requestCaptureBody, err = resolveCaptureValue(requestCaptureBody, capturedValues)
			if err != nil {
				return ScenarioCapture{}, fmt.Errorf("%w: step %q body: %v", ErrScenarioFailed, step.Name, err)
			}
			resolvedBody, marshalErr := json.Marshal(requestCaptureBody)
			if marshalErr != nil {
				return ScenarioCapture{}, fmt.Errorf("%w: step %q body: %v", ErrScenarioFailed, step.Name, marshalErr)
			}
			requestBody = bytes.NewReader(resolvedBody)
		}
		request, err := http.NewRequestWithContext(scenarioCtx, step.Method, stepURL.String(), requestBody)
		if err != nil {
			return ScenarioCapture{}, fmt.Errorf("%w: step %q request: %v", ErrInvalidScenario, step.Name, err)
		}
		request.Header.Set("Accept", "application/json")
		if len(step.Body) > 0 {
			request.Header.Set("Content-Type", "application/json")
		}
		for name, value := range step.Headers {
			resolvedCapture, resolveErr := replaceCaptureReferences(value, capturedValues, func(value string) string { return value })
			if resolveErr != nil {
				return ScenarioCapture{}, fmt.Errorf("%w: step %q header %q: %v", ErrScenarioFailed, step.Name, name, resolveErr)
			}
			resolved, resolveErr := resolveScenarioHeader(resolvedCapture, base, stepURL, clientCopy.Jar)
			if resolveErr != nil {
				return ScenarioCapture{}, fmt.Errorf("%w: step %q header %q: %v", ErrScenarioFailed, step.Name, name, resolveErr)
			}
			request.Header.Set(name, resolved)
		}

		requestRecord := RequestCapture{Method: step.Method, Path: resolvedPath, Body: requestCaptureBody}
		if step.Async {
			result := make(chan asyncResult, 1)
			index := len(capture.Steps)
			capture.Steps = append(capture.Steps, StepCapture{Name: step.Name, Request: requestRecord})
			pending[step.Name] = pendingRequest{index: index, result: result}
			go func(step HTTPStep, request *http.Request, result chan<- asyncResult) {
				response, err := performRequest(step, request)
				result <- asyncResult{response: response, err: err}
			}(step, request, result)
			if step.SettleMillis > 0 {
				timer := time.NewTimer(time.Duration(step.SettleMillis) * time.Millisecond)
				select {
				case <-scenarioCtx.Done():
					timer.Stop()
					return ScenarioCapture{}, fmt.Errorf("%w: step %q settle: %v", ErrScenarioFailed, step.Name, scenarioCtx.Err())
				case <-timer.C:
				}
			}
			continue
		}
		responseCapture, err := performRequest(step, request)
		if err != nil {
			return ScenarioCapture{}, err
		}
		for name, pointer := range step.Capture {
			value, found, lookupErr := lookupCapturePointer(responseCapture.Body, pointer)
			if lookupErr != nil {
				return ScenarioCapture{}, fmt.Errorf("%w: step %q capture %q: %v", ErrScenarioFailed, step.Name, name, lookupErr)
			}
			if !found {
				return ScenarioCapture{}, fmt.Errorf("%w: step %q capture %q pointer %q does not exist", ErrScenarioFailed, step.Name, name, pointer)
			}
			capturedValues[name] = value
		}
		capture.Steps = append(capture.Steps, StepCapture{
			Name: step.Name, Request: requestRecord, Response: responseCapture,
		})
	}
	if len(pending) != 0 {
		names := make([]string, 0, len(pending))
		for name := range pending {
			names = append(names, name)
		}
		sort.Strings(names)
		return ScenarioCapture{}, fmt.Errorf("%w: asynchronous steps were not awaited: %s", ErrScenarioFailed, strings.Join(names, ", "))
	}
	return capture, nil
}

func resolveCaptureURI(raw string, captures map[string]any) (string, error) {
	parts := strings.SplitN(raw, "?", 2)
	path, err := replaceCaptureReferences(parts[0], captures, url.PathEscape)
	if err != nil {
		return "", err
	}
	if len(parts) == 1 {
		return path, nil
	}
	query, err := replaceCaptureReferences(parts[1], captures, url.QueryEscape)
	if err != nil {
		return "", err
	}
	return path + "?" + query, nil
}

func replaceCaptureReferences(raw string, captures map[string]any, escape func(string) string) (string, error) {
	var resolveErr error
	resolved := captureReferencePattern.ReplaceAllStringFunc(raw, func(reference string) string {
		if resolveErr != nil {
			return reference
		}
		matches := captureReferencePattern.FindStringSubmatch(reference)
		if len(matches) != 2 || !captureNamePattern.MatchString(matches[1]) {
			resolveErr = fmt.Errorf("invalid capture reference %q", reference)
			return reference
		}
		value, ok := captures[matches[1]]
		if !ok {
			resolveErr = fmt.Errorf("capture variable %q is unavailable", matches[1])
			return reference
		}
		text, err := captureScalarString(value)
		if err != nil {
			resolveErr = fmt.Errorf("capture variable %q: %v", matches[1], err)
			return reference
		}
		return escape(text)
	})
	if resolveErr != nil {
		return "", resolveErr
	}
	if strings.Contains(resolved, "${capture:") {
		return "", errors.New("malformed capture reference")
	}
	return resolved, nil
}

func resolveCaptureValue(value any, captures map[string]any) (any, error) {
	switch typed := value.(type) {
	case string:
		matches := captureReferencePattern.FindStringSubmatch(typed)
		if len(matches) == 2 && matches[0] == typed {
			captured, ok := captures[matches[1]]
			if !ok {
				return nil, fmt.Errorf("capture variable %q is unavailable", matches[1])
			}
			return captured, nil
		}
		return replaceCaptureReferences(typed, captures, func(value string) string { return value })
	case map[string]any:
		resolved := make(map[string]any, len(typed))
		for key, child := range typed {
			value, err := resolveCaptureValue(child, captures)
			if err != nil {
				return nil, err
			}
			resolved[key] = value
		}
		return resolved, nil
	case []any:
		resolved := make([]any, len(typed))
		for index, child := range typed {
			value, err := resolveCaptureValue(child, captures)
			if err != nil {
				return nil, err
			}
			resolved[index] = value
		}
		return resolved, nil
	default:
		return value, nil
	}
}

func captureScalarString(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case json.Number:
		return typed.String(), nil
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64), nil
	case bool:
		return strconv.FormatBool(typed), nil
	default:
		return "", fmt.Errorf("value of type %T is not a scalar string", value)
	}
}

func lookupCapturePointer(root any, pointer string) (any, bool, error) {
	tokens, err := parseJSONPointer(pointer)
	if err != nil {
		return nil, false, err
	}
	current := root
	for _, token := range tokens {
		switch typed := current.(type) {
		case map[string]any:
			value, ok := typed[token]
			if !ok {
				return nil, false, nil
			}
			current = value
		case []any:
			index, indexErr := strconv.Atoi(token)
			if indexErr != nil || index < 0 || index >= len(typed) {
				return nil, false, nil
			}
			current = typed[index]
		default:
			return nil, false, nil
		}
	}
	return current, true, nil
}

func resolveScenarioHeader(value string, base, requestURL *url.URL, jar http.CookieJar) (string, error) {
	switch value {
	case "${origin}":
		return base.Scheme + "://" + base.Host, nil
	}
	const cookiePrefix = "${cookie:"
	if strings.HasPrefix(value, cookiePrefix) && strings.HasSuffix(value, "}") {
		name := strings.TrimSuffix(strings.TrimPrefix(value, cookiePrefix), "}")
		if name == "" || strings.ContainsAny(name, "\r\n;= \t") {
			return "", errors.New("invalid cookie placeholder")
		}
		if jar == nil {
			return "", errors.New("cookie jar is unavailable")
		}
		for _, cookie := range jar.Cookies(requestURL) {
			if cookie.Name == name {
				return cookie.Value, nil
			}
		}
		return "", fmt.Errorf("cookie %q is unavailable", name)
	}
	if strings.HasPrefix(value, "${") && strings.HasSuffix(value, "}") {
		return "", fmt.Errorf("unsupported dynamic header placeholder %q", value)
	}
	return value, nil
}

func MarshalCapture(capture ScenarioCapture) ([]byte, error) {
	data, err := json.Marshal(capture)
	if err != nil {
		return nil, fmt.Errorf("marshal behavior capture: %w", err)
	}
	if len(capture.redactions) > 0 {
		redacted, err := RedactJSON(data, capture.redactions)
		if err != nil {
			return nil, fmt.Errorf("redact behavior capture: %w", err)
		}
		return redacted, nil
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, data, "", "  "); err != nil {
		return nil, fmt.Errorf("format behavior capture: %w", err)
	}
	indented.WriteByte('\n')
	return indented.Bytes(), nil
}

func LoadScenario(path string) (ScenarioSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ScenarioSpec{}, fmt.Errorf("read behavior scenario: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var spec ScenarioSpec
	if err := decoder.Decode(&spec); err != nil {
		return ScenarioSpec{}, fmt.Errorf("%w: decode scenario: %v", ErrInvalidScenario, err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return ScenarioSpec{}, fmt.Errorf("%w: scenario has trailing JSON", ErrInvalidScenario)
	}
	if err := validateScenario(spec); err != nil {
		return ScenarioSpec{}, err
	}
	return spec, nil
}

func validateScenario(spec ScenarioSpec) error {
	if spec.SchemaVersion != 1 {
		return fmt.Errorf("%w: schemaVersion=%d, want 1", ErrInvalidScenario, spec.SchemaVersion)
	}
	if !scenarioIDPattern.MatchString(strings.TrimSpace(spec.ID)) {
		return fmt.Errorf("%w: invalid id %q", ErrInvalidScenario, spec.ID)
	}
	if len(spec.Contracts) == 0 {
		return fmt.Errorf("%w: scenario has no contracts", ErrInvalidScenario)
	}
	expected := expectedContracts()
	seenContracts := map[string]struct{}{}
	for _, contract := range spec.Contracts {
		key := contractKey(contract.Kind, contract.Name)
		if _, exists := expected[key]; !exists {
			return fmt.Errorf("%w: unknown %s contract %q", ErrInvalidScenario, contract.Kind, contract.Name)
		}
		if _, duplicate := seenContracts[key]; duplicate {
			return fmt.Errorf("%w: duplicate contract %s", ErrInvalidScenario, key)
		}
		seenContracts[key] = struct{}{}
	}
	if len(spec.Steps) == 0 {
		return fmt.Errorf("%w: scenario has no HTTP steps", ErrInvalidScenario)
	}
	if spec.MaxBodyBytes < 0 || spec.MaxBodyBytes > maximumScenarioBodyLimit {
		return fmt.Errorf("%w: maxBodyBytes must be between 1 and %d when set", ErrInvalidScenario, maximumScenarioBodyLimit)
	}
	if spec.ComparisonMode != "" && spec.ComparisonMode != ExactComparison && spec.ComparisonMode != CandidateSuperset {
		return fmt.Errorf("%w: comparisonMode %q is invalid", ErrInvalidScenario, spec.ComparisonMode)
	}
	for _, pointer := range append(append([]string{}, spec.Normalization...), spec.Redactions...) {
		if _, err := parseJSONPointer(pointer); err != nil {
			return fmt.Errorf("%w: invalid capture pointer %q: %v", ErrInvalidScenario, pointer, err)
		}
	}
	seenSteps := map[string]struct{}{}
	availableCaptures := map[string]struct{}{}
	asyncSteps := map[string]struct{}{}
	awaitedSteps := map[string]struct{}{}
	for _, step := range spec.Steps {
		if strings.TrimSpace(step.Name) == "" {
			return fmt.Errorf("%w: HTTP step name is empty", ErrInvalidScenario)
		}
		if _, duplicate := seenSteps[step.Name]; duplicate {
			return fmt.Errorf("%w: duplicate HTTP step %q", ErrInvalidScenario, step.Name)
		}
		seenSteps[step.Name] = struct{}{}
		if step.Await != "" {
			if step.Async || step.Method != "" || step.Path != "" || len(step.Headers) != 0 || len(step.Body) != 0 || len(step.ExpectedStatus) != 0 || len(step.Capture) != 0 || step.SettleMillis != 0 || step.Poll != nil {
				return fmt.Errorf("%w: await step %q must not define an HTTP request", ErrInvalidScenario, step.Name)
			}
			if _, exists := asyncSteps[step.Await]; !exists {
				return fmt.Errorf("%w: step %q awaits unknown or later asynchronous step %q", ErrInvalidScenario, step.Name, step.Await)
			}
			if _, duplicate := awaitedSteps[step.Await]; duplicate {
				return fmt.Errorf("%w: asynchronous step %q is awaited more than once", ErrInvalidScenario, step.Await)
			}
			awaitedSteps[step.Await] = struct{}{}
			continue
		}
		switch step.Method {
		case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		default:
			return fmt.Errorf("%w: step %q method %q is not allowed", ErrInvalidScenario, step.Name, step.Method)
		}
		if step.Async {
			if len(step.Capture) != 0 {
				return fmt.Errorf("%w: asynchronous step %q cannot capture response values", ErrInvalidScenario, step.Name)
			}
			if step.Poll != nil {
				return fmt.Errorf("%w: asynchronous step %q cannot poll", ErrInvalidScenario, step.Name)
			}
			asyncSteps[step.Name] = struct{}{}
		} else if step.SettleMillis != 0 {
			return fmt.Errorf("%w: synchronous step %q cannot define settleMilliseconds", ErrInvalidScenario, step.Name)
		}
		if step.SettleMillis < 0 || step.SettleMillis > 5000 {
			return fmt.Errorf("%w: step %q settleMilliseconds must be between 0 and 5000", ErrInvalidScenario, step.Name)
		}
		if step.Poll != nil {
			if _, err := parseJSONPointer(step.Poll.Pointer); err != nil {
				return fmt.Errorf("%w: step %q poll pointer is invalid: %v", ErrInvalidScenario, step.Name, err)
			}
			if step.Poll.IntervalMillis < 1 || step.Poll.IntervalMillis > 5000 {
				return fmt.Errorf("%w: step %q poll interval must be between 1 and 5000 milliseconds", ErrInvalidScenario, step.Name)
			}
			if step.Poll.TimeoutMillis < step.Poll.IntervalMillis || step.Poll.TimeoutMillis > 120000 {
				return fmt.Errorf("%w: step %q poll timeout must be at least the interval and no more than 120000 milliseconds", ErrInvalidScenario, step.Name)
			}
		}
		for _, raw := range append([]string{step.Path, string(step.Body)}, headerValues(step.Headers)...) {
			references, err := captureReferences(raw)
			if err != nil {
				return fmt.Errorf("%w: step %q: %v", ErrInvalidScenario, step.Name, err)
			}
			for _, name := range references {
				if _, exists := availableCaptures[name]; !exists {
					return fmt.Errorf("%w: step %q uses capture variable %q before it is captured", ErrInvalidScenario, step.Name, name)
				}
			}
		}
		validationPath := captureReferencePattern.ReplaceAllString(step.Path, "captured")
		parsed, err := url.ParseRequestURI(validationPath)
		if err != nil || !strings.HasPrefix(step.Path, "/") || parsed.IsAbs() || parsed.Host != "" {
			return fmt.Errorf("%w: step %q path %q is not an origin-relative request URI", ErrInvalidScenario, step.Name, step.Path)
		}
		if len(step.Body) > 0 && !json.Valid(step.Body) {
			return fmt.Errorf("%w: step %q body is not valid JSON", ErrInvalidScenario, step.Name)
		}
		if len(step.ExpectedStatus) == 0 {
			return fmt.Errorf("%w: step %q has no expected status", ErrInvalidScenario, step.Name)
		}
		statuses := append([]int{}, step.ExpectedStatus...)
		sort.Ints(statuses)
		for index, status := range statuses {
			if status < 100 || status > 599 || index > 0 && statuses[index-1] == status {
				return fmt.Errorf("%w: step %q has invalid expected statuses %v", ErrInvalidScenario, step.Name, step.ExpectedStatus)
			}
		}
		for name, value := range step.Headers {
			canonical := http.CanonicalHeaderKey(strings.TrimSpace(name))
			if !headerNamePattern.MatchString(canonical) || strings.ContainsAny(value, "\r\n") {
				return fmt.Errorf("%w: step %q has invalid header %q", ErrInvalidScenario, step.Name, name)
			}
			switch strings.ToLower(canonical) {
			case "authorization", "cookie", "proxy-authorization", "x-api-key":
				return fmt.Errorf("%w: step %q must not store sensitive header %q", ErrInvalidScenario, step.Name, name)
			}
		}
		captureNames := make([]string, 0, len(step.Capture))
		for name := range step.Capture {
			captureNames = append(captureNames, name)
		}
		sort.Strings(captureNames)
		for _, name := range captureNames {
			if !captureNamePattern.MatchString(name) {
				return fmt.Errorf("%w: step %q has invalid capture variable %q", ErrInvalidScenario, step.Name, name)
			}
			if _, duplicate := availableCaptures[name]; duplicate {
				return fmt.Errorf("%w: step %q redefines capture variable %q", ErrInvalidScenario, step.Name, name)
			}
			pointer := step.Capture[name]
			if _, err := parseJSONPointer(pointer); err != nil {
				return fmt.Errorf("%w: step %q capture pointer %q is invalid: %v", ErrInvalidScenario, step.Name, pointer, err)
			}
			availableCaptures[name] = struct{}{}
		}
	}
	if len(asyncSteps) != len(awaitedSteps) {
		unawaited := make([]string, 0, len(asyncSteps)-len(awaitedSteps))
		for name := range asyncSteps {
			if _, ok := awaitedSteps[name]; !ok {
				unawaited = append(unawaited, name)
			}
		}
		sort.Strings(unawaited)
		return fmt.Errorf("%w: asynchronous steps were not awaited: %s", ErrInvalidScenario, strings.Join(unawaited, ", "))
	}
	return nil
}

func captureReferences(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	matches := captureReferencePattern.FindAllStringSubmatch(raw, -1)
	values := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) != 2 || !captureNamePattern.MatchString(match[1]) {
			return nil, fmt.Errorf("invalid capture reference %q", match[0])
		}
		values = append(values, match[1])
	}
	remainder := captureReferencePattern.ReplaceAllString(raw, "")
	if strings.Contains(remainder, "${capture:") {
		return nil, errors.New("malformed capture reference")
	}
	return values, nil
}

func headerValues(headers map[string]string) []string {
	values := make([]string, 0, len(headers))
	for _, value := range headers {
		values = append(values, value)
	}
	return values
}

func validateLoopbackBaseURL(raw string) (*url.URL, error) {
	base, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("%w: invalid base URL %q", ErrInvalidScenario, raw)
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return nil, fmt.Errorf("%w: base URL scheme must be http or https", ErrInvalidScenario)
	}
	if base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("%w: base URL must not contain user info, query, or fragment", ErrInvalidScenario)
	}
	host := strings.ToLower(base.Hostname())
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("%w: base URL host must be loopback", ErrInvalidScenario)
	}
	if base.Path == "" {
		base.Path = "/"
	}
	if base.Path != "/" {
		return nil, fmt.Errorf("%w: base URL must not contain a path", ErrInvalidScenario)
	}
	return base, nil
}

func sameOrigin(left *url.URL, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Host, right.Host)
}

func decodeOptionalJSON(data []byte) (any, error) {
	if len(data) == 0 {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func decodeResponseBody(data []byte) any {
	if len(data) == 0 {
		return nil
	}
	value, err := decodeOptionalJSON(data)
	if err == nil {
		return value
	}
	return string(data)
}

func readBoundedBody(body io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response body exceeds %d bytes", limit)
	}
	return data, nil
}

func normalizedContentType(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(value))
	}
	return strings.ToLower(mediaType)
}

func expectedStatus(expected []int, actual int) bool {
	for _, status := range expected {
		if status == actual {
			return true
		}
	}
	return false
}

func cloneScenarioRequest(request *http.Request, renewBody bool) (*http.Request, error) {
	if !renewBody {
		return request, nil
	}
	clone := request.Clone(request.Context())
	if request.Body == nil {
		return clone, nil
	}
	if request.GetBody == nil {
		return nil, errors.New("request body cannot be replayed")
	}
	body, err := request.GetBody()
	if err != nil {
		return nil, err
	}
	clone.Body = body
	return clone, nil
}

func equalJSONValue(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

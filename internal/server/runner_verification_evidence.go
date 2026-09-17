package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"synon-go/internal/agentruntime"
)

const sessionReviewerEvidenceSchema = "synon.runner_completion_review.source_ref.v2"

// Reviewer evidence must stay comfortably below every supported provider's
// tool-result envelope. The canonical artifact remains unchanged and its full
// digest is still verified before any reviewer-only bounding is applied.
const sessionReviewerTextEvidenceMaxBytes = 32 * 1024

const sessionReviewerSubmitToolName = "submit_output"

// A reviewer may repair one invalid verdict submission in place. The first
// rejection remains in the same tool transcript with a recovery instruction;
// a second rejection terminates the bounded review.
const sessionReviewerMaxSubmissionRejections = 2

type sessionReviewerSubmissionMode string

const (
	sessionReviewerSubmissionCompletion sessionReviewerSubmissionMode = "completion"
)

type sessionRunnerReviewerEvidenceUnavailableError struct {
	Detail string
}

func (err *sessionRunnerReviewerEvidenceUnavailableError) Error() string {
	detail := "independent completion reviewer evidence is unavailable"
	if err != nil && strings.TrimSpace(err.Detail) != "" {
		detail += ": " + strings.TrimSpace(err.Detail)
	}
	return truncateSessionRunnerReferenceDiagnostic(detail, maxRunnerCorrectionResumeDetailBytes)
}

type sessionReviewerSubmissionProtocolError struct {
	Attempts int
	Detail   string
}

func (err *sessionReviewerSubmissionProtocolError) Error() string {
	detail := "reviewer verdict submission correction budget was exhausted"
	if err != nil && strings.TrimSpace(err.Detail) != "" {
		detail += ": " + strings.TrimSpace(err.Detail)
	}
	return truncateSessionRunnerReferenceDiagnostic(detail, maxRunnerCorrectionResumeDetailBytes)
}

type sessionReviewerEvidenceReceipt struct {
	ReceiptID             string `json:"receiptId"`
	ToolCallID            string `json:"toolCallId"`
	ArtifactID            string `json:"artifactId"`
	VersionID             string `json:"versionId"`
	ExpectedContentSHA256 string `json:"expectedContentSha256"`
	ReadScope             string `json:"readScope"`
	ResultSHA256          string `json:"resultSha256"`
	Complete              bool   `json:"complete"`

	content string
}

type sessionReviewerEvidenceScope struct {
	mu               sync.Mutex
	sessionID        string
	streamUID        string
	runnerAttempt    int
	reviewIndex      int
	reviewUnitID     string
	inventorySHA256  string
	artifacts        map[string]sessionReviewerArtifactEvidence
	receipts         map[string]sessionReviewerEvidenceReceipt
	submissionMode   sessionReviewerSubmissionMode
	submission       *sessionReviewerSubmission
	submitRejections int
	lastSubmitError  string
	replCalls        int
}

type sessionReviewerSubmission struct {
	ToolCallID string
	Review     sessionRunnerReview
}

type sessionReviewerEvidenceScopeContextKey struct{}

func withSessionReviewerEvidenceScope(ctx context.Context, scope *sessionReviewerEvidenceScope) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sessionReviewerEvidenceScopeContextKey{}, scope)
}

func sessionReviewerEvidenceScopeFromContext(ctx context.Context) *sessionReviewerEvidenceScope {
	if ctx == nil {
		return nil
	}
	scope, _ := ctx.Value(sessionReviewerEvidenceScopeContextKey{}).(*sessionReviewerEvidenceScope)
	return scope
}

func newSessionReviewerEvidenceScope(binding map[string]any, evidence sessionReviewerWorkspaceEvidence) (*sessionReviewerEvidenceScope, error) {
	return newSessionReviewerEvidenceScopeForMode(binding, evidence, sessionReviewerSubmissionCompletion)
}

func newSessionReviewerEvidenceScopeForMode(
	binding map[string]any,
	evidence sessionReviewerWorkspaceEvidence,
	mode sessionReviewerSubmissionMode,
) (*sessionReviewerEvidenceScope, error) {
	if mode != sessionReviewerSubmissionCompletion {
		return nil, errors.New("completion reviewer submission mode is invalid")
	}
	streamUID := strings.TrimSpace(stringValue(binding["stream_uid"]))
	runnerAttempt := int(numberValue(binding["runner_attempt"]))
	reviewIndex := int(numberValue(binding["review_index"]))
	reviewUnitID := strings.TrimSpace(stringValue(binding["review_unit_id"]))
	if reviewUnitID == "" {
		reviewUnitID = fmt.Sprintf("review:%s:%d", streamUID, reviewIndex)
	}
	inventorySHA256 := strings.TrimSpace(stringValue(binding["artifact_inventory_sha256"]))
	if streamUID == "" || runnerAttempt <= 0 || reviewIndex < 0 ||
		len(inventorySHA256) != sha256.Size*2 {
		return nil, errors.New("completion reviewer evidence scope identity is invalid")
	}
	artifacts := make(map[string]sessionReviewerArtifactEvidence, len(evidence.Artifacts))
	for _, artifact := range evidence.Artifacts {
		versionID := strings.TrimSpace(artifact.VersionID)
		if versionID == "" || strings.TrimSpace(artifact.ArtifactID) == "" || len(strings.TrimSpace(artifact.ContentSHA256)) != sha256.Size*2 {
			return nil, errors.New("completion reviewer evidence artifact identity is invalid")
		}
		if _, duplicate := artifacts[versionID]; duplicate {
			return nil, errors.New("completion reviewer evidence contains a duplicate artifact version")
		}
		artifacts[versionID] = artifact
	}
	return &sessionReviewerEvidenceScope{
		streamUID: streamUID, runnerAttempt: runnerAttempt, reviewIndex: reviewIndex, reviewUnitID: reviewUnitID,
		inventorySHA256: inventorySHA256, artifacts: artifacts, submissionMode: mode,
		receipts: make(map[string]sessionReviewerEvidenceReceipt, len(artifacts)),
	}, nil
}

func (scope *sessionReviewerEvidenceScope) authorize(toolName, toolCallID string, input map[string]any) error {
	if scope == nil || normalizeAgentToolName(toolName) != "readfile" {
		return nil
	}
	toolCallID = strings.TrimSpace(toolCallID)
	versionID := strings.TrimSpace(stringValue(input["version_id"]))
	if toolCallID == "" || versionID == "" {
		return errors.New("completion reviewer read_file requires exact tool_call_id and version_id")
	}
	scope.mu.Lock()
	artifact, allowed := scope.artifacts[versionID]
	if !allowed {
		scope.mu.Unlock()
		return errors.New("completion reviewer artifact version is outside the current review binding")
	}
	if _, duplicate := scope.receipts[toolCallID]; duplicate {
		scope.mu.Unlock()
		return errors.New("completion reviewer tool_call_id was already used")
	}
	scope.mu.Unlock()
	if rawLabel, supplied := input["file_path"]; supplied {
		label, ok := rawLabel.(string)
		label = strings.TrimSpace(label)
		if !ok || label == "" || label != strings.TrimSpace(artifact.Name) {
			return errors.New("completion reviewer file_path label does not match the bound artifact name")
		}
		// file_path is only a redundant model-facing label. Removing it before
		// execution ensures immutable version_id remains the sole read authority.
		delete(input, "file_path")
	}
	return nil
}

func (scope *sessionReviewerEvidenceScope) submit(toolCallID string, arguments json.RawMessage) error {
	if scope == nil {
		return errors.New("completion reviewer submission scope is unavailable")
	}
	toolCallID = strings.TrimSpace(toolCallID)
	if toolCallID == "" {
		return errors.New("completion reviewer submission requires a tool_call_id")
	}
	if scope.submissionMode != sessionReviewerSubmissionCompletion {
		return errors.New("completion reviewer submission mode is invalid")
	}
	review, err := parseSessionRunnerReview(string(arguments))
	if err != nil {
		return scope.recordSubmissionRejection(err)
	}
	if filtered, removed := filterSessionReviewerRecursiveSelfVersionFindings(review); removed {
		if len(filtered.Issues) == 0 {
			return scope.recordSubmissionRejection(errors.New(
				"an artifact self-version finding is non-actionable because saving creates a new version; inspect the remaining acceptance criteria and submit only substantive findings",
			))
		}
		review = filtered
	}
	scope.mu.Lock()
	defer scope.mu.Unlock()
	if scope.submission != nil {
		return errors.New("completion reviewer already submitted a verdict")
	}
	for index, issue := range review.Issues {
		if issue.ArtifactVersionID == nil {
			continue
		}
		versionID := strings.TrimSpace(*issue.ArtifactVersionID)
		if _, found := scope.artifacts[versionID]; !found {
			return scope.recordSubmissionRejectionLocked(fmt.Errorf("review finding %d names an artifact version outside the bound inventory", index))
		}
		read := false
		for _, receipt := range scope.receipts {
			if strings.TrimSpace(receipt.VersionID) == versionID {
				read = true
				break
			}
		}
		if !read {
			return scope.recordSubmissionRejectionLocked(fmt.Errorf("review finding %d names an artifact that was not read", index))
		}
	}
	scope.submission = &sessionReviewerSubmission{
		ToolCallID: toolCallID, Review: review,
	}
	return nil
}

func filterSessionReviewerRecursiveSelfVersionFindings(review sessionRunnerReview) (sessionRunnerReview, bool) {
	issues := make([]sessionRunnerReviewIssue, 0, len(review.Issues))
	removed := false
	for _, issue := range review.Issues {
		text := strings.ToLower(strings.TrimSpace(issue.Claim + " " + issue.Evidence))
		selfReference := strings.Contains(text, "self-reference") || strings.Contains(text, "self reference") ||
			strings.Contains(text, "自引用") || strings.Contains(text, "自我引用")
		versionMismatch := strings.Contains(text, "older version") || strings.Contains(text, "old version") ||
			strings.Contains(text, "旧版本") || strings.Contains(text, "版本id") || strings.Contains(text, "version id")
		if selfReference && versionMismatch {
			removed = true
			continue
		}
		issues = append(issues, issue)
	}
	if !removed {
		return review, false
	}
	review.Issues = issues
	review.Verdict = "pass"
	feedback := make([]string, 0, len(issues))
	for _, issue := range issues {
		if issue.Verdict == "fail" {
			review.Verdict = "revise"
		}
		if strings.TrimSpace(issue.Evidence) != "" {
			feedback = append(feedback, strings.TrimSpace(issue.Evidence))
		}
	}
	review.Feedback = truncateReviewerText(strings.Join(feedback, "\n\n"), 32<<10)
	return review, true
}

func (scope *sessionReviewerEvidenceScope) recordSubmissionRejection(err error) error {
	if scope == nil || err == nil {
		return err
	}
	scope.mu.Lock()
	defer scope.mu.Unlock()
	return scope.recordSubmissionRejectionLocked(err)
}

func (scope *sessionReviewerEvidenceScope) recordSubmissionRejectionLocked(err error) error {
	if scope == nil || err == nil {
		return err
	}
	scope.submitRejections++
	scope.lastSubmitError = truncateSessionRunnerReferenceDiagnostic(err.Error(), maxRunnerCorrectionResumeDetailBytes)
	return err
}

func (scope *sessionReviewerEvidenceScope) submissionRejectionSnapshot() (int, string) {
	if scope == nil {
		return 0, ""
	}
	scope.mu.Lock()
	defer scope.mu.Unlock()
	return scope.submitRejections, scope.lastSubmitError
}

func (scope *sessionReviewerEvidenceScope) submissionProtocolError() error {
	attempts, detail := scope.submissionRejectionSnapshot()
	if attempts < sessionReviewerMaxSubmissionRejections {
		return nil
	}
	return &sessionReviewerSubmissionProtocolError{Attempts: attempts, Detail: detail}
}

func (scope *sessionReviewerEvidenceScope) submissionToolName() string {
	return sessionReviewerSubmitToolName
}

func (scope *sessionReviewerEvidenceScope) ensureSubmissionOpen() error {
	if scope == nil {
		return errors.New("completion reviewer submission scope is unavailable")
	}
	scope.mu.Lock()
	defer scope.mu.Unlock()
	if scope.submission != nil {
		return errors.New("completion reviewer cannot call tools after submitting a verdict")
	}
	return nil
}

func (scope *sessionReviewerEvidenceScope) consumeReplCall() bool {
	if scope == nil {
		return false
	}
	scope.mu.Lock()
	defer scope.mu.Unlock()
	if scope.replCalls >= sessionReviewerMaxReplCalls {
		return false
	}
	scope.replCalls++
	return true
}

func (scope *sessionReviewerEvidenceScope) submissionSnapshot() (sessionReviewerSubmission, bool) {
	if scope == nil {
		return sessionReviewerSubmission{}, false
	}
	scope.mu.Lock()
	defer scope.mu.Unlock()
	if scope.submission == nil {
		return sessionReviewerSubmission{}, false
	}
	return *scope.submission, true
}

func sessionReviewerSubmitToolSchema() agentruntime.ToolSchema {
	finding := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"msg_idx":             map[string]any{"type": "integer", "minimum": 0},
			"claim":               map[string]any{"type": "string", "minLength": 1},
			"verdict":             map[string]any{"type": "string", "enum": []string{"pass", "warn", "fail"}},
			"evidence":            map[string]any{"type": "string", "minLength": 1},
			"severity":            map[string]any{"type": []string{"string", "null"}, "enum": []any{"critical", "high", "medium", "low", nil}},
			"artifact_version_id": map[string]any{"type": []string{"string", "null"}},
		},
		"required": []string{"msg_idx", "claim", "verdict", "evidence"},
	}
	return agentruntime.ToolSchema{
		Name:        sessionReviewerSubmitToolName,
		Description: "Submit the REVIEWER findings exactly once and stop. A pass finding records a claim traced to supporting evidence; use an empty findings array when there is nothing useful to record.",
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"human_description": map[string]any{"type": "string"},
				"findings":          map[string]any{"type": "array", "items": finding, "maxItems": 32},
			},
			"required": []string{"human_description", "findings"},
		},
	}
}

type sessionReviewerScopedToolGateway struct {
	delegate agentruntime.ToolGateway
	scope    *sessionReviewerEvidenceScope
}

func (gateway *sessionReviewerScopedToolGateway) AllowsCorrectedRegisteredExecution(toolName string) bool {
	return gateway != nil && gateway.scope != nil && normalizeAgentToolName(toolName) == "repl"
}

func (gateway *sessionReviewerScopedToolGateway) Execute(
	ctx context.Context,
	call agentruntime.ToolCall,
) (agentruntime.ToolResult, error) {
	scope := gateway.scope
	if strings.TrimSpace(call.Name) == scope.submissionToolName() {
		before, _ := scope.submissionRejectionSnapshot()
		if err := scope.submit(call.ID, call.Arguments); err != nil {
			after, detail := scope.submissionRejectionSnapshot()
			if after > before {
				retryable := after < sessionReviewerMaxSubmissionRejections
				recovery := "correct every rejected field and resubmit once using evidence from the traced execution record"
				if !retryable {
					recovery = "start a fresh bounded reviewer generation and trace the execution record before submitting"
				}
				return agentruntime.ToolResult{Value: map[string]any{
					"ok": false, "code": "invalid_review_evidence", "error": detail,
					"retryable": retryable, "attempts": after,
					"max_attempts": sessionReviewerMaxSubmissionRejections,
					"recovery":     recovery,
				}}, nil
			}
			return agentruntime.ToolResult{}, err
		}
		return agentruntime.ToolResult{Value: map[string]any{
			"ok": true, "submission_id": strings.TrimSpace(call.ID),
		}, Terminal: true}, nil
	}
	if err := scope.ensureSubmissionOpen(); err != nil {
		return agentruntime.ToolResult{}, err
	}
	if normalizeAgentToolName(call.Name) == "repl" && !scope.consumeReplCall() {
		return agentruntime.ToolResult{Value: map[string]any{
			"ok": false, "code": "review_repl_budget_exhausted",
			"error": fmt.Sprintf(
				"repl budget exhausted for this review (%d calls); finish tracing from the inlined record or bound artifacts, then submit the findings",
				sessionReviewerMaxReplCalls,
			),
		}}, nil
	}
	if gateway.delegate == nil {
		return agentruntime.ToolResult{}, errors.New("completion reviewer tool gateway is unavailable")
	}
	return gateway.delegate.Execute(ctx, call)
}

func sessionReviewerToolGateway(delegate agentruntime.ToolGateway, scope *sessionReviewerEvidenceScope) agentruntime.ToolGateway {
	return &sessionReviewerScopedToolGateway{delegate: delegate, scope: scope}
}

func (scope *sessionReviewerEvidenceScope) record(
	toolName, toolCallID string,
	input map[string]any,
	response any,
	parts []agentruntime.ContentPart,
) (any, error) {
	if scope == nil {
		return response, nil
	}
	if normalizeAgentToolName(toolName) != "readfile" {
		return response, nil
	}
	if err := scope.authorize(toolName, toolCallID, input); err != nil {
		return nil, err
	}
	versionID := strings.TrimSpace(stringValue(input["version_id"]))
	scope.mu.Lock()
	artifact := scope.artifacts[versionID]
	scope.mu.Unlock()
	if agentruntime.ClassifyToolResult(response).Failed() {
		return nil, errors.New("completion reviewer artifact read did not complete")
	}
	readScope := "complete"
	complete := true
	if showing := strings.TrimSpace(stringValue(sessionReviewerMapValue(response)["showing_lines"])); showing != "" {
		readScope, complete = "lines:"+showing, false
	}
	if pages := reviewerPageScope(input["pages"]); pages != "" {
		readScope, complete = "pages:"+pages, false
	}
	responseMap := sessionReviewerMapValue(response)
	contentRaw, contentPresent := responseMap["content"]
	content, contentIsString := contentRaw.(string)
	if contentPresent && !contentIsString {
		return nil, errors.New("completion reviewer text read content is invalid")
	}
	_, completeContentSHA256, err := sessionReviewerReadResultSHA256(response, parts, content, contentPresent, complete)
	if err != nil {
		return nil, err
	}
	expectedSHA256 := strings.ToLower(strings.TrimSpace(artifact.ContentSHA256))
	if complete && completeContentSHA256 != "" && completeContentSHA256 != expectedSHA256 {
		return nil, errors.New("completion reviewer artifact bytes do not match the bound immutable version")
	}
	if contentPresent && len(parts) == 0 {
		bounded, transformed, originalBytes, originalSHA256 := boundSessionReviewerTextEvidence(content)
		if transformed {
			responseMap = copyMapAny(responseMap)
			responseMap["content"] = bounded
			responseMap["content_truncated"] = true
			responseMap["original_content_bytes"] = originalBytes
			responseMap["original_content_sha256"] = originalSHA256
			response = responseMap
			content = bounded
			complete = false
			readScope += ";bounded"
		}
	}
	resultSHA256, _, err := sessionReviewerReadResultSHA256(response, parts, content, contentPresent, complete)
	if err != nil {
		return nil, err
	}
	receiptID := sessionReviewerReceiptID(
		scope.streamUID, scope.runnerAttempt, scope.reviewIndex, scope.reviewUnitID, scope.inventorySHA256,
		strings.TrimSpace(toolCallID), artifact, readScope, resultSHA256,
	)
	receipt := sessionReviewerEvidenceReceipt{
		ReceiptID: receiptID, ToolCallID: strings.TrimSpace(toolCallID),
		ArtifactID: strings.TrimSpace(artifact.ArtifactID), VersionID: versionID,
		ExpectedContentSHA256: expectedSHA256, ReadScope: readScope,
		ResultSHA256: resultSHA256, Complete: complete, content: content,
	}
	scope.mu.Lock()
	if _, duplicate := scope.receipts[receipt.ToolCallID]; duplicate {
		scope.mu.Unlock()
		return nil, errors.New("completion reviewer read receipt already exists")
	}
	scope.receipts[receipt.ToolCallID] = receipt
	scope.mu.Unlock()
	result := copyMapAny(sessionReviewerMapValue(response))
	result["review_evidence_receipt"] = receipt
	return result, nil
}

func boundSessionReviewerTextEvidence(content string) (string, bool, int, string) {
	originalBytes := len([]byte(content))
	originalDigest := sha256.Sum256([]byte(content))
	originalSHA256 := hex.EncodeToString(originalDigest[:])
	bounded, redacted := redactSessionReviewerDataURIs(content)
	if len([]byte(bounded)) <= sessionReviewerTextEvidenceMaxBytes {
		return bounded, redacted, originalBytes, originalSHA256
	}
	marker := fmt.Sprintf(
		"\n[reviewer evidence bounded: original_bytes=%d, sha256=%s]",
		originalBytes, originalSHA256,
	)
	limit := sessionReviewerTextEvidenceMaxBytes - len([]byte(marker))
	if limit < 0 {
		limit = 0
	}
	prefix := []byte(bounded)
	if len(prefix) > limit {
		prefix = prefix[:limit]
	}
	for len(prefix) > 0 && !utf8.Valid(prefix) {
		prefix = prefix[:len(prefix)-1]
	}
	return string(prefix) + marker, true, originalBytes, originalSHA256
}

func redactSessionReviewerDataURIs(content string) (string, bool) {
	const maxHeaderBytes = 512
	var output strings.Builder
	cursor := 0
	redacted := false
	for cursor < len(content) {
		relativeStart := strings.Index(content[cursor:], "data:")
		if relativeStart < 0 {
			output.WriteString(content[cursor:])
			break
		}
		start := cursor + relativeStart
		headerLimit := start + maxHeaderBytes
		if headerLimit > len(content) {
			headerLimit = len(content)
		}
		headerRelativeEnd := strings.Index(content[start:headerLimit], ";base64,")
		if headerRelativeEnd < 0 {
			output.WriteString(content[cursor : start+len("data:")])
			cursor = start + len("data:")
			continue
		}
		headerEnd := start + headerRelativeEnd
		mime := content[start+len("data:") : headerEnd]
		if mime == "" || !strings.Contains(mime, "/") || strings.ContainsAny(mime, " \t\r\n\"'<>;") {
			output.WriteString(content[cursor : start+len("data:")])
			cursor = start + len("data:")
			continue
		}
		dataStart := headerEnd + len(";base64,")
		dataEnd := dataStart
		for dataEnd < len(content) && sessionReviewerBase64Byte(content[dataEnd]) {
			dataEnd++
		}
		if dataEnd == dataStart {
			output.WriteString(content[cursor:dataStart])
			cursor = dataStart
			continue
		}
		encoded := content[dataStart:dataEnd]
		digest := sha256.Sum256([]byte(encoded))
		output.WriteString(content[cursor:start])
		output.WriteString(fmt.Sprintf(
			"[embedded data URI omitted: mime=%s, encoded_bytes=%d, sha256=%s]",
			mime, len(encoded), hex.EncodeToString(digest[:]),
		))
		cursor = dataEnd
		redacted = true
	}
	return output.String(), redacted
}

func sessionReviewerBase64Byte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' || value == '+' || value == '/' ||
		value == '=' || value == '-' || value == '_'
}

func sessionReviewerMapValue(value any) map[string]any {
	result, _ := value.(map[string]any)
	if result == nil {
		return map[string]any{}
	}
	return result
}

func reviewerPageScope(raw any) string {
	pages := agentWorkspacePageValues(raw)
	if len(pages) == 0 {
		return ""
	}
	values := make([]string, len(pages))
	for index, page := range pages {
		values[index] = fmt.Sprint(page)
	}
	return strings.Join(values, ",")
}

func sessionReviewerReadResultSHA256(response any, parts []agentruntime.ContentPart, content string, contentPresent, complete bool) (string, string, error) {
	hash := sha256.New()
	completeContentSHA256 := ""
	if len(parts) > 0 {
		for _, part := range parts {
			if part.Media == nil || len(part.Media.Source.Data) == 0 {
				return "", "", errors.New("completion reviewer visual read receipt is incomplete")
			}
			writeSessionReviewDigestString(hash, string(part.Type))
			writeSessionReviewDigestString(hash, part.Media.MIMEType)
			writeSessionReviewDigestString(hash, part.Media.Filename)
			writeSessionReviewDigestString(hash, string(part.Media.Source.Data))
		}
		if complete && len(parts) == 1 {
			digest := sha256.Sum256(parts[0].Media.Source.Data)
			completeContentSHA256 = hex.EncodeToString(digest[:])
		}
	} else {
		encoded, err := json.Marshal(response)
		if err != nil {
			return "", "", errors.New("completion reviewer read result is not serializable")
		}
		_, _ = hash.Write(encoded)
		if complete && contentPresent {
			digest := sha256.Sum256([]byte(content))
			completeContentSHA256 = hex.EncodeToString(digest[:])
		} else if complete {
			return "", "", errors.New("completion reviewer complete text read returned no content")
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), completeContentSHA256, nil
}

func sessionReviewerReceiptID(
	streamUID string,
	runnerAttempt int,
	reviewIndex int,
	reviewUnitID string,
	inventorySHA256 string,
	toolCallID string,
	artifact sessionReviewerArtifactEvidence,
	readScope string,
	resultSHA256 string,
) string {
	hash := sha256.New()
	for _, value := range []string{
		"synon.runner-review.read-receipt.v1", streamUID, fmt.Sprint(runnerAttempt), fmt.Sprint(reviewIndex),
		reviewUnitID, inventorySHA256, toolCallID, artifact.ArtifactID, artifact.VersionID,
		artifact.ContentSHA256, readScope, resultSHA256,
	} {
		writeSessionReviewDigestString(hash, strings.TrimSpace(value))
	}
	return "review-read-" + hex.EncodeToString(hash.Sum(nil)[:16])
}

func (scope *sessionReviewerEvidenceScope) receiptsSnapshot() []sessionReviewerEvidenceReceipt {
	if scope == nil {
		return nil
	}
	scope.mu.Lock()
	defer scope.mu.Unlock()
	receipts := make([]sessionReviewerEvidenceReceipt, 0, len(scope.receipts))
	for _, receipt := range scope.receipts {
		receipts = append(receipts, receipt)
	}
	sort.Slice(receipts, func(i, j int) bool { return receipts[i].ReceiptID < receipts[j].ReceiptID })
	return receipts
}

func validateSessionReviewerEvidence(
	review sessionRunnerReview,
	messages []agentruntime.Message,
	scope *sessionReviewerEvidenceScope,
) ([]sessionReviewerEvidenceReceipt, error) {
	type callEvidence struct {
		name      string
		versionID string
		count     int
		results   []agentruntime.Message
	}
	calls := map[string]*callEvidence{}
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			callID := strings.TrimSpace(call.ID)
			if callID == "" {
				return nil, errors.New("reviewer tool call identity is missing")
			}
			evidence := calls[callID]
			if evidence == nil {
				evidence = &callEvidence{name: strings.TrimSpace(call.Name)}
				if sessionReviewerArtifactReadTool(call.Name) {
					var input map[string]any
					if json.Unmarshal(call.Arguments, &input) == nil {
						evidence.versionID = strings.TrimSpace(stringValue(input["version_id"]))
					}
				}
				calls[callID] = evidence
			}
			evidence.count++
			if evidence.name != strings.TrimSpace(call.Name) {
				return nil, errors.New("reviewer tool call identity conflicts")
			}
		}
	}
	for _, message := range messages {
		if message.Role != "tool" || strings.TrimSpace(message.ToolCallID) == "" {
			continue
		}
		call := calls[strings.TrimSpace(message.ToolCallID)]
		if call == nil {
			return nil, errors.New("reviewer tool result has no matching call")
		}
		call.results = append(call.results, message)
	}
	receipts := scope.receiptsSnapshot()
	byCallID := make(map[string]sessionReviewerEvidenceReceipt, len(receipts))
	byVersionID := make(map[string]bool, len(receipts))
	for _, receipt := range receipts {
		byCallID[receipt.ToolCallID] = receipt
		byVersionID[strings.TrimSpace(receipt.VersionID)] = true
	}
	skipped := map[string]bool{}
	for callID, call := range calls {
		if call.count != 1 || len(call.results) != 1 {
			return nil, fmt.Errorf("reviewer tool call %s is missing, duplicated, or ambiguous", callID)
		}
		var value any
		if json.Unmarshal([]byte(call.results[0].Content), &value) != nil || agentruntime.ClassifyToolResult(value).Failed() {
			if sessionReviewerRecoverableRejection(call.name, call.results[0].Content) ||
				sessionReviewerRecoveredReadRejection(call.name, call.versionID, call.results[0].Content, byVersionID) ||
				sessionReviewerAuxiliaryToolFailure(call.name) {
				skipped[callID] = true
				continue
			}
			return nil, fmt.Errorf("reviewer tool call %s failed", callID)
		}
	}
	for callID, call := range calls {
		if skipped[callID] {
			continue
		}
		if sessionReviewerArtifactReadTool(call.name) {
			if _, found := byCallID[callID]; !found {
				return nil, fmt.Errorf("reviewer artifact read %s has no server-verified receipt", callID)
			}
		}
	}
	return receipts, nil
}

// sessionReviewerRecoverableRejection reports whether a failed reviewer tool
// result is a recoverable protocol rejection rather than failed evidence. A
// blocked read of an out-of-binding version leaks no content and can be
// retried against the exact bound version IDs; a rejected verdict submission
// can be corrected and resubmitted. Treating these as hard failures would
// invalidate an otherwise complete review and fail the whole runner.
func sessionReviewerRecoverableRejection(toolName, content string) bool {
	switch toolName {
	case sessionReviewerSubmitToolName:
		return true
	case "read_file":
		var value map[string]any
		if json.Unmarshal([]byte(content), &value) != nil {
			return false
		}
		return strings.Contains(stringValue(value["error"]), "outside the current review binding")
	default:
		return false
	}
}

// sessionReviewerAuxiliaryToolFailure keeps an optional trace lookup failure
// visible in the durable tool history without invalidating a later verdict.
// Bound read_file calls remain strict evidence; verdict submission failures
// remain governed by their bounded correction protocol.
func sessionReviewerAuxiliaryToolFailure(toolName string) bool {
	toolName = strings.TrimSpace(toolName)
	return toolName != "" &&
		!sessionReviewerArtifactReadTool(toolName) &&
		toolName != sessionReviewerSubmitToolName
}

func sessionReviewerRecoveredReadRejection(toolName, versionID, content string, recoveredVersions map[string]bool) bool {
	if normalizeAgentToolName(toolName) != "readfile" ||
		!recoveredVersions[strings.TrimSpace(versionID)] {
		return false
	}
	var value map[string]any
	if json.Unmarshal([]byte(content), &value) != nil || value["_policy_skip"] != true {
		return false
	}
	errorText := strings.TrimSpace(stringValue(value["error"]))
	hint := strings.TrimSpace(stringValue(value["system_hint"]))
	return errorText == "read_file line window exceeds the output limit" &&
		strings.Contains(hint, "Retry with a smaller limit or a later offset")
}

func bindSessionReviewerEvidence(
	binding map[string]any,
	receipts []sessionReviewerEvidenceReceipt,
	messages []agentruntime.Message,
) (map[string]any, error) {
	result := copyMapAny(binding)
	result["schema"] = sessionReviewerEvidenceSchema
	result["read_receipts"] = receipts
	receiptJSON, err := json.Marshal(receipts)
	if err != nil {
		return nil, errors.New("completion reviewer receipts are not serializable")
	}
	receiptHash := sha256.Sum256(receiptJSON)
	result["read_receipts_sha256"] = hex.EncodeToString(receiptHash[:])
	messageJSON, err := json.Marshal(messages)
	if err != nil {
		return nil, errors.New("completion reviewer transcript is not serializable")
	}
	messageHash := sha256.Sum256(messageJSON)
	result["reviewer_transcript_sha256"] = hex.EncodeToString(messageHash[:])
	return result, nil
}

func sessionReviewerArtifactReadToolSchema() agentruntime.ToolSchema {
	schema := agentWorkspaceReadFileToolSchema()
	schema.Description = "Read one exact immutable artifact version from the current completion-review source binding. version_id is the sole authority; optional file_path is only a redundant exact artifact-name label and is ignored after validation."
	properties := schema.Parameters["properties"].(map[string]any)
	properties["file_path"] = map[string]any{
		"type": "string", "description": "Optional exact artifact name copied from the current source binding. It is not a filesystem path and never authorizes the read.",
	}
	schema.Parameters["required"] = []string{"version_id", "human_description"}
	return schema
}

func sessionReviewerReplToolSchema() agentruntime.ToolSchema {
	schema := agentKernelReplToolSchema()
	schema.Description = "Trace the recorded execution in the isolated read-only control kernel. Use host.artifact_path(version_id) to inspect a relevant bound artifact and the archive/query helpers to retrieve a cited prior result. Do not re-run or independently recompute the target analysis, install software, edit files, or mutate the root task workspace."
	properties := schema.Parameters["properties"].(map[string]any)
	delete(properties, "working_dir")
	delete(properties, "background")
	return schema
}

func (s *Server) sessionReviewerRuntimeToolSchemas(
	ctx context.Context,
	options SessionRunnerChatOptions,
	artifactsAvailable bool,
) []agentruntime.ToolSchema {
	return s.sessionReviewerRuntimeToolSchemasForSubmission(
		ctx, options, artifactsAvailable, sessionReviewerSubmitToolSchema(),
	)
}

func (s *Server) sessionReviewerRuntimeToolSchemasForSubmission(
	ctx context.Context,
	options SessionRunnerChatOptions,
	artifactsAvailable bool,
	submitSchema agentruntime.ToolSchema,
) []agentruntime.ToolSchema {
	// Fixed-job Reviewer authority is intentionally tiny and independent of
	// the root agent's empty/unrestricted allowlist. Archive search/page live
	// inside the scoped repl host bridge; no global catalog, MCP, file, shell,
	// planning, or delegation tools are admitted here.
	schemas := []agentruntime.ToolSchema{sessionReviewerReplToolSchema()}
	if artifactsAvailable {
		schemas = append(schemas, sessionReviewerArtifactReadToolSchema())
	}
	return append(schemas, submitSchema)
}
